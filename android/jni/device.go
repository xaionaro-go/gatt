package jni

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"github.com/facebookincubator/go-belt/tool/logger"

	jnipkg "github.com/AndroidGoLab/jni"
	"github.com/AndroidGoLab/jni/app"
	"github.com/AndroidGoLab/jni/bluetooth"
	"github.com/AndroidGoLab/jni/bluetooth/le"
	"github.com/xaionaro-go/gatt"
	"github.com/xaionaro-go/gatt/android"
)

func init() {
	android.Register(android.BackendJNI, newDevice)
}

type device struct {
	gatt.DeviceHandler

	cfg config

	vm     *jnipkg.VM
	appCtx *app.Context

	adapter *bluetooth.Adapter

	state gatt.State

	mu       sync.Mutex
	services []*gatt.Service

	// Scanning state (protected by mu).
	scanner             *le.BluetoothLeScanner
	scanCallbackObj     *jnipkg.Object
	scanCallbackCleanup func()
	scanCtxCancel       context.CancelFunc

	// Known peripherals by address (protected by mu).
	peripherals map[string]*peripheral

	// Advertising state (protected by mu).
	advState *advertiseState

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
	if d.cfg.vm == nil {
		return nil, fmt.Errorf("jni: WithVM option is required")
	}
	d.vm = d.cfg.vm
	if d.cfg.appCtx != nil {
		d.appCtx = d.cfg.appCtx
	}
	return d, nil
}

func (d *device) ID() int { return 0 }

func (d *device) Start(
	ctx context.Context,
	stateChanged func(context.Context, gatt.Device, gatt.State),
) error {
	logger.Tracef(ctx, "jni.device.Start")
	defer func() { logger.Tracef(ctx, "/jni.device.Start") }()

	if d.appCtx == nil {
		return fmt.Errorf("jni: WithContext option is required")
	}

	adapter, err := bluetooth.NewAdapter(d.appCtx)
	if err != nil {
		return fmt.Errorf("creating bluetooth adapter: %w", err)
	}
	d.adapter = adapter

	d.state = gatt.StatePoweredOn
	d.SetStateChanged(stateChanged)
	go stateChanged(ctx, d, d.state)
	return nil
}

func (d *device) Stop() error {
	_ = d.StopScanning()

	d.closeGattServer()
	_ = d.StopAdvertising(context.Background())

	if d.adapter != nil {
		d.adapter.Close()
		d.adapter = nil
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

// Advertising methods are in advertising.go.
// GATT server methods are in gatt_server.go.

func (d *device) RemoveAllServices(ctx context.Context) error {
	logger.Tracef(ctx, "jni.device.RemoveAllServices")
	defer func() { logger.Tracef(ctx, "/jni.device.RemoveAllServices") }()

	d.closeGattServer()

	d.mu.Lock()
	defer d.mu.Unlock()
	d.services = nil
	return nil
}

func (d *device) AddService(ctx context.Context, s *gatt.Service) error {
	logger.Tracef(ctx, "jni.device.AddService")
	defer func() { logger.Tracef(ctx, "/jni.device.AddService") }()

	d.mu.Lock()
	d.services = append(d.services, s)
	gs := d.gattServer
	d.mu.Unlock()

	// If the GATT server is already running, add the new service to it.
	if gs != nil {
		if err := d.addServiceToGattServer(ctx, gs, s); err != nil {
			return fmt.Errorf("adding service to GATT server: %w", err)
		}
	}
	return nil
}

func (d *device) SetServices(ctx context.Context, ss []*gatt.Service) error {
	logger.Tracef(ctx, "jni.device.SetServices")
	defer func() { logger.Tracef(ctx, "/jni.device.SetServices") }()

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
	logger.Tracef(ctx, "jni.device.Scan")
	defer func() { logger.Tracef(ctx, "/jni.device.Scan: %v", _err) }()

	if d.adapter == nil {
		return fmt.Errorf("adapter not initialized; call Start first")
	}

	d.mu.Lock()
	if d.scanner != nil {
		d.mu.Unlock()
		return fmt.Errorf("already scanning")
	}
	d.mu.Unlock()

	// Get the BLE scanner from the adapter.
	scannerObj, err := d.adapter.GetBluetoothLeScanner()
	if err != nil {
		return fmt.Errorf("getBluetoothLeScanner: %w", err)
	}
	if scannerObj == nil {
		return fmt.Errorf("BluetoothLeScanner is null (Bluetooth may be disabled)")
	}

	scanner := &le.BluetoothLeScanner{
		VM:  d.vm,
		Obj: scannerObj,
	}

	// Create a ScanCallback proxy.
	var callbackObj *jnipkg.Object
	var cleanup func()

	scanCtx, scanCancel := context.WithCancel(ctx)

	err = d.vm.Do(func(env *jnipkg.Env) error {
		cls, err := env.FindClass("android/bluetooth/le/ScanCallback")
		if err != nil {
			return fmt.Errorf("find ScanCallback class: %w", err)
		}
		defer env.DeleteLocalRef(&cls.Object)

		handler := func(env *jnipkg.Env, methodName string, args []*jnipkg.Object) (*jnipkg.Object, error) {
			d.handleScanCallback(scanCtx, env, methodName, args, ss, dup)
			return nil, nil
		}

		proxy, proxyCleanup, err := env.NewProxy([]*jnipkg.Class{cls}, handler)
		if err != nil {
			return fmt.Errorf("create ScanCallback proxy: %w", err)
		}

		// Promote the proxy to a global ref so it survives this vm.Do scope.
		callbackObj = env.NewGlobalRef(proxy)
		env.DeleteLocalRef(proxy)
		cleanup = proxyCleanup
		return nil
	})
	if err != nil {
		scanCancel()
		return err
	}

	// Start the scan.
	if err := scanner.StartScan(callbackObj); err != nil {
		scanCancel()
		cleanup()
		return fmt.Errorf("startScan: %w", err)
	}

	d.mu.Lock()
	d.scanner = scanner
	d.scanCallbackObj = callbackObj
	d.scanCallbackCleanup = cleanup
	d.scanCtxCancel = scanCancel
	d.mu.Unlock()

	return nil
}

// handleScanCallback is invoked by the ScanCallback proxy for each scan event.
func (d *device) handleScanCallback(
	ctx context.Context,
	env *jnipkg.Env,
	methodName string,
	args []*jnipkg.Object,
	filterUUIDs []gatt.UUID,
	dup bool,
) {
	switch methodName {
	case "onScanResult":
		// args[0] = callbackType (Integer), args[1] = ScanResult
		if len(args) < 2 || args[1] == nil {
			return
		}

		d.processScanResult(ctx, env, args[1], filterUUIDs, dup)

	case "onScanFailed":
		if len(args) >= 1 {
			errorCode := unboxInt(env, args[0])
			logger.Warnf(ctx, "BLE scan failed: errorCode=%d", errorCode)
		}
	}
}

// processScanResult extracts device info from a ScanResult and calls the
// peripheralDiscovered handler.
func (d *device) processScanResult(
	ctx context.Context,
	env *jnipkg.Env,
	scanResultObj *jnipkg.Object,
	filterUUIDs []gatt.UUID,
	dup bool,
) {
	// Wrap into the typed ScanResult to use the generated methods.
	scanResultGlobal := env.NewGlobalRef(scanResultObj)
	sr := &le.ScanResult{
		VM:  d.vm,
		Obj: scanResultGlobal,
	}
	defer func() {
		_ = d.vm.Do(func(env *jnipkg.Env) error {
			env.DeleteGlobalRef(scanResultGlobal)
			return nil
		})
	}()

	// Get the BluetoothDevice from the ScanResult.
	devObj, err := sr.GetDevice()
	if err != nil {
		logger.Debugf(ctx, "scanResult.GetDevice failed: %v", err)
		return
	}
	if devObj == nil {
		return
	}

	btDev := &bluetooth.Device{VM: d.vm, Obj: devObj}

	addr, err := btDev.GetAddress()
	if err != nil {
		logger.Debugf(ctx, "device.GetAddress failed: %v", err)
		return
	}

	name, _ := btDev.GetName()

	rssi, err := sr.GetRssi()
	if err != nil {
		logger.Debugf(ctx, "scanResult.GetRssi failed: %v", err)
	}

	// Find or create the peripheral.
	d.mu.Lock()
	p, exists := d.peripherals[addr]
	if !exists {
		p = newPeripheral(d, btDev, addr, name)
		d.peripherals[addr] = p
	} else {
		// Update the name if it was previously empty.
		if p.name == "" && name != "" {
			p.name = name
		}
		// Don't report duplicates unless requested.
		if !dup {
			d.mu.Unlock()
			return
		}
	}
	d.mu.Unlock()

	// Build a minimal Advertisement.
	adv := &gatt.Advertisement{
		LocalName: name,
	}

	handler := d.PeripheralDiscovered()
	if handler != nil {
		handler(ctx, p, adv, int(rssi))
	}
}

func (d *device) StopScanning() (_err error) {
	d.mu.Lock()
	scanner := d.scanner
	callbackObj := d.scanCallbackObj
	cleanup := d.scanCallbackCleanup
	cancel := d.scanCtxCancel

	d.scanner = nil
	d.scanCallbackObj = nil
	d.scanCallbackCleanup = nil
	d.scanCtxCancel = nil
	d.mu.Unlock()

	if scanner == nil {
		return nil
	}

	if cancel != nil {
		cancel()
	}

	if err := scanner.StopScan1_1(callbackObj); err != nil {
		_err = fmt.Errorf("stopScan: %w", err)
	}

	// Release the callback proxy global ref and cleanup handler registration.
	_ = d.vm.Do(func(env *jnipkg.Env) error {
		if callbackObj != nil {
			env.DeleteGlobalRef(callbackObj)
		}
		return nil
	})

	if cleanup != nil {
		cleanup()
	}

	return _err
}

func (d *device) Connect(ctx context.Context, p gatt.Peripheral) {
	logger.Tracef(ctx, "jni.device.Connect")
	defer func() { logger.Tracef(ctx, "/jni.device.Connect") }()

	per, ok := p.(*peripheral)
	if !ok {
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, fmt.Errorf("peripheral is not a JNI peripheral"))
		}
		return
	}

	// Create the BluetoothGattCallback proxy and connect.
	var gattCallbackObj *jnipkg.Object
	var gattCleanup func()

	err := d.vm.Do(func(env *jnipkg.Env) error {
		cls, err := env.FindClass("android/bluetooth/BluetoothGattCallback")
		if err != nil {
			return fmt.Errorf("find BluetoothGattCallback class: %w", err)
		}
		defer env.DeleteLocalRef(&cls.Object)

		proxy, proxyCleanup, err := env.NewProxy(
			[]*jnipkg.Class{cls},
			per.handleGattCallback,
		)
		if err != nil {
			return fmt.Errorf("create BluetoothGattCallback proxy: %w", err)
		}

		gattCallbackObj = env.NewGlobalRef(proxy)
		env.DeleteLocalRef(proxy)
		gattCleanup = proxyCleanup
		return nil
	})
	if err != nil {
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, fmt.Errorf("creating GATT callback: %w", err))
		}
		return
	}

	// Call connectGatt on the BluetoothDevice.
	// ConnectGatt3(context, autoConnect, callback) -> BluetoothGatt
	gattObj, err := per.btDev.ConnectGatt3(
		d.appCtx.Obj,
		false,
		gattCallbackObj,
	)
	if err != nil {
		_ = d.vm.Do(func(env *jnipkg.Env) error {
			env.DeleteGlobalRef(gattCallbackObj)
			return nil
		})
		gattCleanup()
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, fmt.Errorf("connectGatt: %w", err))
		}
		return
	}
	if gattObj == nil {
		_ = d.vm.Do(func(env *jnipkg.Env) error {
			env.DeleteGlobalRef(gattCallbackObj)
			return nil
		})
		gattCleanup()
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, fmt.Errorf("connectGatt returned null"))
		}
		return
	}

	gatt := &bluetooth.Gatt{VM: d.vm, Obj: gattObj}

	per.mu.Lock()
	per.gattObj = gatt
	per.gattCallbackCleanup = gattCleanup
	per.mu.Unlock()

	// Store the callback object global ref for cleanup.
	gattCallbackRef := gattCallbackObj

	// Wait for onConnectionStateChange.
	select {
	case <-ctx.Done():
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, ctx.Err())
		}
		return
	case newState := <-per.connStateChanged:
		if newState != stateConnected {
			// Connection failed.
			_ = d.vm.Do(func(env *jnipkg.Env) error {
				env.DeleteGlobalRef(gattCallbackRef)
				return nil
			})
			gattCleanup()
			per.mu.Lock()
			per.gattObj = nil
			per.gattCallbackCleanup = nil
			per.mu.Unlock()

			handler := d.PeripheralConnected()
			if handler != nil {
				handler(ctx, p, fmt.Errorf("connection failed: state=%d", newState))
			}
			return
		}
	}

	handler := d.PeripheralConnected()
	if handler != nil {
		handler(ctx, p, nil)
	}

	// Monitor for disconnection in a separate goroutine.
	go d.monitorDisconnection(ctx, per, gattCallbackRef, gattCleanup)
}

// monitorDisconnection waits for a disconnection event and calls the
// peripheralDisconnected handler.
func (d *device) monitorDisconnection(
	ctx context.Context,
	per *peripheral,
	callbackRef *jnipkg.Object,
	callbackCleanup func(),
) {
	select {
	case <-ctx.Done():
	case newState := <-per.connStateChanged:
		_ = newState // Always means disconnected at this point.
	}

	per.mu.Lock()
	g := per.gattObj
	per.gattObj = nil
	per.gattCallbackCleanup = nil
	per.mu.Unlock()

	if g != nil {
		_ = g.Close()
	}
	_ = d.vm.Do(func(env *jnipkg.Env) error {
		if callbackRef != nil {
			env.DeleteGlobalRef(callbackRef)
		}
		return nil
	})
	if callbackCleanup != nil {
		callbackCleanup()
	}

	handler := d.PeripheralDisconnected()
	if handler != nil {
		handler(ctx, per, nil)
	}
}

func (d *device) CancelConnection(ctx context.Context, p gatt.Peripheral) {
	logger.Tracef(ctx, "jni.device.CancelConnection")
	defer func() { logger.Tracef(ctx, "/jni.device.CancelConnection") }()

	per, ok := p.(*peripheral)
	if !ok {
		return
	}

	per.mu.Lock()
	g := per.gattObj
	per.mu.Unlock()

	if g == nil {
		return
	}

	// Disconnect triggers onConnectionStateChange -> disconnected,
	// which is handled by monitorDisconnection.
	_ = g.Disconnect()
}

// Ensure *peripheral satisfies gatt.Peripheral at compile time.
var _ gatt.Peripheral = (*peripheral)(nil)

// Ensure *device satisfies gatt.Device at compile time.
var _ gatt.Device = (*device)(nil)

