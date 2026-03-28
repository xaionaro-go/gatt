package binder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/facebookincubator/go-belt/tool/logger"

	bt "github.com/AndroidGoLab/binder/android/bluetooth"
	btle "github.com/AndroidGoLab/binder/android/bluetooth/le"
	"github.com/AndroidGoLab/binder/android/content"
	androidOS "github.com/AndroidGoLab/binder/android/os"
	binderpkg "github.com/AndroidGoLab/binder/binder"
	"github.com/AndroidGoLab/binder/binder/versionaware"
	"github.com/AndroidGoLab/binder/kernelbinder"
	"github.com/AndroidGoLab/binder/servicemanager"

	"github.com/xaionaro-go/gatt"
	"github.com/xaionaro-go/gatt/android"
)

func init() {
	android.Register(android.BackendBinder, newDevice)
}

type device struct {
	gatt.DeviceHandler

	cfg config

	driver    *kernelbinder.Driver
	transport *versionaware.Transport

	btMgr    bt.IBluetoothManager
	btMgrCb  bt.IBluetoothManagerCallback
	btProxy  *bt.BluetoothProxy
	rawGatt  *rawGATT
	btGattMu sync.Mutex

	// Scan proxy (obtained lazily from btProxy.GetBluetoothScan).
	scanProxy *bt.BluetoothScanProxy

	state gatt.State

	mu       sync.Mutex
	services []*gatt.Service

	// Scanning state (protected by mu).
	scannerID     int32
	scanCallback  *scanCallback
	scanCtxCancel context.CancelFunc

	// Known peripherals by address (protected by mu).
	peripherals map[string]*peripheral

	// GATT server state (protected by mu).
	gattServer *gattServerState
}

func newDevice(
	ctx context.Context,
	opts ...gatt.Option,
) (gatt.Device, error) {
	d := &device{
		peripherals: make(map[string]*peripheral),
	}
	for _, opt := range opts {
		if err := opt(d); err != nil {
			return nil, fmt.Errorf("applying option: %w", err)
		}
	}
	return d, nil
}

func (d *device) ID() int { return 0 }

func (d *device) Start(
	ctx context.Context,
	stateChanged func(context.Context, gatt.Device, gatt.State),
) (_err error) {
	logger.Tracef(ctx, "binder.device.Start")
	defer func() { logger.Tracef(ctx, "/binder.device.Start: %v", _err) }()

	// Open the binder driver.
	var driverOpts []binderpkg.Option
	if d.cfg.mapSize > 0 {
		driverOpts = append(driverOpts, binderpkg.WithMapSize(d.cfg.mapSize))
	}

	drv, err := kernelbinder.Open(ctx, driverOpts...)
	if err != nil {
		return fmt.Errorf("opening binder driver: %w", err)
	}
	d.driver = drv

	// Create version-aware transport.
	transport, err := versionaware.NewTransport(ctx, drv, d.cfg.targetAPI)
	if err != nil {
		_ = drv.Close(ctx)
		return fmt.Errorf("creating version-aware transport: %w", err)
	}
	d.transport = transport

	// Get the Bluetooth service from the service manager.
	sm := servicemanager.New(transport)
	// The generated constant BluetoothService is "bluetooth", but
	// the actual Android service name is "bluetooth_manager".
	btBinder, err := sm.GetService(ctx, "bluetooth_manager")
	if err != nil {
		_ = drv.Close(ctx)
		return fmt.Errorf("getting bluetooth service: %w", err)
	}

	// Create a typed proxy for IBluetoothManager.
	btMgr := bt.NewBluetoothManagerProxy(btBinder)
	d.btMgr = btMgr

	// Register adapter callback to get the IBluetooth proxy.
	mgrCb := &managerCallback{d: d, ctx: ctx}
	d.btMgrCb = bt.NewBluetoothManagerCallbackStub(mgrCb)

	btAdapterBinder, err := btMgr.RegisterAdapter(ctx, d.btMgrCb)
	if err != nil {
		_ = drv.Close(ctx)
		return fmt.Errorf("registering adapter: %w", err)
	}
	d.btProxy = bt.NewBluetoothProxy(btAdapterBinder)

	d.state = gatt.StatePoweredOn
	d.SetStateChanged(stateChanged)
	go stateChanged(ctx, d, d.state)
	return nil
}

func (d *device) Stop() (_err error) {
	_ = d.StopScanning()

	d.closeGattServer()
	_ = d.StopAdvertising(context.Background())

	if d.btMgr != nil && d.btMgrCb != nil {
		_ = d.btMgr.UnregisterAdapter(context.Background(), d.btMgrCb)
	}

	if d.driver != nil {
		_ = d.driver.Close(context.Background())
		d.driver = nil
	}
	d.state = gatt.StatePoweredOff
	return nil
}

func (d *device) Handle(ctx context.Context, hh ...gatt.Handler) {
	for _, h := range hh {
		h(ctx, d)
	}
}

func (d *device) Option(opts ...gatt.Option) error {
	var errs []error
	for _, opt := range opts {
		if err := opt(d); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// getBluetoothGatt lazily obtains the IBluetoothGatt binder handle
// using the typed BluetoothProxy.GetBluetoothGatt() method.
func (d *device) getBluetoothGatt(ctx context.Context) (*rawGATT, error) {
	d.btGattMu.Lock()
	defer d.btGattMu.Unlock()

	if d.rawGatt != nil {
		return d.rawGatt, nil
	}

	if d.btProxy == nil {
		return nil, fmt.Errorf("bluetooth adapter not available")
	}

	gattBinder, err := d.btProxy.GetBluetoothGatt(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetBluetoothGatt: %w", err)
	}

	if gattBinder == nil {
		return nil, fmt.Errorf("GetBluetoothGatt returned nil (Bluetooth may be off)")
	}

	rg := &rawGATT{
		gattBinder: gattBinder,
		transport:  d.transport,
	}
	d.rawGatt = rg
	return rg, nil
}

// getBluetoothScan lazily obtains the IBluetoothScan typed proxy
// using BluetoothProxy.GetBluetoothScan().
func (d *device) getBluetoothScan(ctx context.Context) (*bt.BluetoothScanProxy, error) {
	d.btGattMu.Lock()
	defer d.btGattMu.Unlock()

	if d.scanProxy != nil {
		return d.scanProxy, nil
	}

	if d.btProxy == nil {
		return nil, fmt.Errorf("bluetooth adapter not available")
	}

	scanBinder, err := d.btProxy.GetBluetoothScan(ctx)
	if err != nil {
		return nil, fmt.Errorf("GetBluetoothScan: %w", err)
	}

	if scanBinder == nil {
		return nil, fmt.Errorf("GetBluetoothScan returned nil (Bluetooth may be off)")
	}

	d.scanProxy = bt.NewBluetoothScanProxy(scanBinder)
	return d.scanProxy, nil
}

// shellAttribution returns an AttributionSource for the current process,
// identifying as com.android.shell (required for unprivileged binder access).
func shellAttribution() content.AttributionSource {
	return content.AttributionSource{
		AttributionSourceState: content.AttributionSourceState{
			Pid:         int32(os.Getpid()),
			Uid:         int32(os.Getuid()),
			PackageName: "com.android.shell",
		},
	}
}

// getRawGatt returns the cached rawGATT or nil.
// For use in callbacks that cannot return errors.
func (d *device) getRawGatt() *rawGATT {
	d.btGattMu.Lock()
	defer d.btGattMu.Unlock()
	return d.rawGatt
}

func (d *device) Advertise(
	ctx context.Context,
	a *gatt.AdvPacket,
) (_err error) {
	logger.Tracef(ctx, "binder.device.Advertise")
	defer func() { logger.Tracef(ctx, "/binder.device.Advertise: %v", _err) }()

	return fmt.Errorf("binder limitation: advertising requires IBluetoothGatt.StartAdvertisingSet " +
		"which has the 'receiver any' serialization limitation")
}

func (d *device) AdvertiseNameAndServices(
	ctx context.Context,
	name string,
	ss []gatt.UUID,
) (_err error) {
	logger.Tracef(ctx, "binder.device.AdvertiseNameAndServices")
	defer func() { logger.Tracef(ctx, "/binder.device.AdvertiseNameAndServices: %v", _err) }()

	return fmt.Errorf("binder limitation: advertising requires IBluetoothGatt.StartAdvertisingSet " +
		"which has the 'receiver any' serialization limitation")
}

func (d *device) AdvertiseIBeaconData(
	ctx context.Context,
	b []byte,
) (_err error) {
	logger.Tracef(ctx, "binder.device.AdvertiseIBeaconData")
	defer func() { logger.Tracef(ctx, "/binder.device.AdvertiseIBeaconData: %v", _err) }()

	return fmt.Errorf("binder limitation: advertising requires IBluetoothGatt.StartAdvertisingSet " +
		"which has the 'receiver any' serialization limitation")
}

func (d *device) AdvertiseNameAndIBeaconData(
	ctx context.Context,
	name string,
	b []byte,
) (_err error) {
	logger.Tracef(ctx, "binder.device.AdvertiseNameAndIBeaconData")
	defer func() { logger.Tracef(ctx, "/binder.device.AdvertiseNameAndIBeaconData: %v", _err) }()

	return fmt.Errorf("binder limitation: advertising requires IBluetoothGatt.StartAdvertisingSet " +
		"which has the 'receiver any' serialization limitation")
}

func (d *device) AdvertiseIBeacon(
	ctx context.Context,
	u gatt.UUID,
	major, minor uint16,
	pwr int8,
) (_err error) {
	logger.Tracef(ctx, "binder.device.AdvertiseIBeacon")
	defer func() { logger.Tracef(ctx, "/binder.device.AdvertiseIBeacon: %v", _err) }()

	return fmt.Errorf("binder limitation: advertising requires IBluetoothGatt.StartAdvertisingSet " +
		"which has the 'receiver any' serialization limitation")
}

func (d *device) StopAdvertising(
	ctx context.Context,
) (_err error) {
	logger.Tracef(ctx, "binder.device.StopAdvertising")
	defer func() { logger.Tracef(ctx, "/binder.device.StopAdvertising: %v", _err) }()

	// Nothing to do — advertising was never started.
	return nil
}

func (d *device) RemoveAllServices(ctx context.Context) error {
	logger.Tracef(ctx, "binder.device.RemoveAllServices")
	defer func() { logger.Tracef(ctx, "/binder.device.RemoveAllServices") }()

	d.closeGattServer()

	d.mu.Lock()
	defer d.mu.Unlock()
	d.services = nil
	return nil
}

func (d *device) AddService(ctx context.Context, s *gatt.Service) error {
	logger.Tracef(ctx, "binder.device.AddService")
	defer func() { logger.Tracef(ctx, "/binder.device.AddService") }()

	d.mu.Lock()
	d.services = append(d.services, s)
	d.mu.Unlock()
	return nil
}

func (d *device) SetServices(ctx context.Context, ss []*gatt.Service) error {
	logger.Tracef(ctx, "binder.device.SetServices")
	defer func() { logger.Tracef(ctx, "/binder.device.SetServices") }()

	d.closeGattServer()

	d.mu.Lock()
	d.services = ss
	d.mu.Unlock()
	return nil
}

func (d *device) Scan(
	ctx context.Context,
	ss []gatt.UUID,
	dup bool,
) (_err error) {
	logger.Tracef(ctx, "binder.device.Scan")
	defer func() { logger.Tracef(ctx, "/binder.device.Scan: %v", _err) }()

	if d.btProxy == nil {
		return fmt.Errorf("adapter not initialized; call Start first")
	}

	d.mu.Lock()
	if d.scanCallback != nil {
		d.mu.Unlock()
		return fmt.Errorf("already scanning")
	}
	d.mu.Unlock()

	sp, err := d.getBluetoothScan(ctx)
	if err != nil {
		return fmt.Errorf("getting IBluetoothScan: %w", err)
	}

	scanCtx, scanCancel := context.WithCancel(ctx)

	cb := &scanCallback{
		d:           d,
		ctx:         scanCtx,
		scannerIDCh: make(chan int32, 1),
		filterUUIDs: ss,
		dup:         dup,
	}

	// Create the IScannerCallback stub.
	scannerCb := btle.NewScannerCallbackStub(cb)

	// Register the scanner via IBluetoothScan typed proxy.
	err = sp.RegisterScanner(ctx, scannerCb, androidOS.WorkSource{}, shellAttribution())
	if err != nil {
		scanCancel()
		return fmt.Errorf("RegisterScanner: %w", err)
	}

	// Wait for OnScannerRegistered callback.
	var scannerID int32
	select {
	case <-ctx.Done():
		scanCancel()
		return ctx.Err()
	case scannerID = <-cb.scannerIDCh:
	}

	if scannerID <= 0 {
		scanCancel()
		return fmt.Errorf("scanner registration failed (id=%d)", scannerID)
	}

	// Start the scan via IBluetoothScan typed proxy.
	err = sp.StartScan(ctx, scannerID, btle.ScanSettings{}, nil, shellAttribution())
	if err != nil {
		scanCancel()
		return fmt.Errorf("StartScan: %w", err)
	}

	d.mu.Lock()
	d.scannerID = scannerID
	d.scanCallback = cb
	d.scanCtxCancel = scanCancel
	d.mu.Unlock()

	return nil
}

func (d *device) StopScanning() (_err error) {
	d.mu.Lock()
	scannerID := d.scannerID
	cb := d.scanCallback
	cancel := d.scanCtxCancel

	d.scannerID = 0
	d.scanCallback = nil
	d.scanCtxCancel = nil
	d.mu.Unlock()

	if cb == nil {
		return nil
	}

	if cancel != nil {
		cancel()
	}

	sp, err := d.getBluetoothScan(context.Background())
	if err != nil {
		return fmt.Errorf("getting IBluetoothScan for StopScan: %w", err)
	}

	ctx := context.Background()
	attr := shellAttribution()

	if err := sp.StopScan(ctx, scannerID, attr); err != nil {
		_err = fmt.Errorf("StopScan: %w", err)
	}

	if err := sp.UnregisterScanner(ctx, scannerID, attr); err != nil && _err == nil {
		_err = fmt.Errorf("UnregisterScanner: %w", err)
	}

	return _err
}

func (d *device) Connect(ctx context.Context, p gatt.Peripheral) {
	logger.Tracef(ctx, "binder.device.Connect")
	defer func() { logger.Tracef(ctx, "/binder.device.Connect") }()

	per, ok := p.(*peripheral)
	if !ok {
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, fmt.Errorf("peripheral is not a binder peripheral"))
		}
		return
	}

	rg, err := d.getBluetoothGatt(ctx)
	if err != nil {
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, fmt.Errorf("getting IBluetoothGatt: %w", err))
		}
		return
	}

	// Create the IBluetoothGattCallback stub.
	gattCb := &gattClientCallback{
		d:   d,
		per: per,
	}
	gattCbStub := bt.NewBluetoothGattCallbackStub(gattCb)

	// Register a GATT client via raw two-way transaction.
	var appUUID [16]byte // zero UUID — unique client registration
	err = rg.registerClient(ctx, appUUID, gattCbStub, false)
	if err != nil {
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, fmt.Errorf("registerClient: %w", err))
		}
		return
	}

	// Wait for OnClientRegistered.
	var clientIf int32
	select {
	case <-ctx.Done():
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, ctx.Err())
		}
		return
	case clientIf = <-per.clientRegistered:
	}

	if clientIf <= 0 {
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, fmt.Errorf("client registration failed (if=%d)", clientIf))
		}
		return
	}

	per.mu.Lock()
	per.clientIf = clientIf
	per.rawGatt = rg
	per.mu.Unlock()

	// Connect to the peripheral via raw transaction.
	err = rg.clientConnect(
		ctx,
		clientIf,
		per.addr,
		0,     // addressType: public
		true,  // isDirect
		2,     // transport: LE
		false, // opportunistic
		1,     // phy: LE 1M
	)
	if err != nil {
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, fmt.Errorf("clientConnect: %w", err))
		}
		return
	}

	// Wait for OnClientConnectionState.
	select {
	case <-ctx.Done():
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, ctx.Err())
		}
		return
	case connected := <-per.connStateChanged:
		if !connected {
			handler := d.PeripheralConnected()
			if handler != nil {
				handler(ctx, p, fmt.Errorf("connection failed"))
			}
			return
		}
	}

	handler := d.PeripheralConnected()
	if handler != nil {
		handler(ctx, p, nil)
	}

	// Monitor for disconnection.
	go d.monitorDisconnection(ctx, per)
}

func (d *device) monitorDisconnection(ctx context.Context, per *peripheral) {
	select {
	case <-ctx.Done():
	case <-per.connStateChanged:
		// Disconnected.
	}

	per.mu.Lock()
	clientIf := per.clientIf
	rg := per.rawGatt
	per.clientIf = 0
	per.rawGatt = nil
	per.mu.Unlock()

	if rg != nil && clientIf > 0 {
		_ = rg.clientDisconnect(context.Background(), clientIf, per.addr)
	}

	handler := d.PeripheralDisconnected()
	if handler != nil {
		handler(ctx, per, nil)
	}
}

func (d *device) CancelConnection(ctx context.Context, p gatt.Peripheral) {
	logger.Tracef(ctx, "binder.device.CancelConnection")
	defer func() { logger.Tracef(ctx, "/binder.device.CancelConnection") }()

	per, ok := p.(*peripheral)
	if !ok {
		return
	}

	per.mu.Lock()
	clientIf := per.clientIf
	rg := per.rawGatt
	per.mu.Unlock()

	if rg == nil || clientIf <= 0 {
		return
	}

	_ = rg.clientDisconnect(ctx, clientIf, per.addr)
}

// closeGattServer tears down the GATT server if running.
func (d *device) closeGattServer() {
	d.mu.Lock()
	gs := d.gattServer
	d.gattServer = nil
	d.mu.Unlock()

	if gs == nil {
		return
	}

	// Stop all notifiers.
	gs.mu.Lock()
	for _, n := range gs.notifiers {
		n.stop()
	}
	gs.notifiers = make(map[string]*serverNotifier)
	gs.mu.Unlock()
}

// Ensure *device satisfies gatt.Device at compile time.
var _ gatt.Device = (*device)(nil)
