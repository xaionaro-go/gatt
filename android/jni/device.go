package jni

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/facebookincubator/go-belt/tool/logger"

	jnipkg "github.com/AndroidGoLab/jni"
	"github.com/AndroidGoLab/jni/app"
	"github.com/AndroidGoLab/jni/bluetooth"
	"github.com/AndroidGoLab/jni/bluetooth/le"
	"github.com/xaionaro-go/gatt"
	"github.com/xaionaro-go/gatt/android"
	"github.com/xaionaro-go/observability"
)

const (
	androidScanModeLowLatency int32 = 2
	scanDiagnosticLogLimit          = 0
	scanDiagnosticLogTag            = "GATT_SCAN"
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
	scanDiagnosticsSeen map[string]struct{}
	scanDiagnosticsLogs int

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
	observability.Go(ctx, func(ctx context.Context) {
		stateChanged(ctx, d, d.state)
	})
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
	d.scanDiagnosticsSeen = make(map[string]struct{})
	d.scanDiagnosticsLogs = 0
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

	if err := d.startScanWithSettings(ctx, scanner, callbackObj); err != nil {
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

func (d *device) startScanWithSettings(
	ctx context.Context,
	scanner *le.BluetoothLeScanner,
	callbackObj *jnipkg.Object,
) error {
	logger.Debugf(ctx, "starting Android BLE scan with explicit low-latency settings")

	return d.vm.Do(func(env *jnipkg.Env) error {
		filters, err := newEmptyArrayList(env)
		if err != nil {
			return fmt.Errorf("create empty scan filter list: %w", err)
		}
		defer env.DeleteLocalRef(filters)

		settings, err := newLowLatencyScanSettings(env)
		if err != nil {
			return fmt.Errorf("create scan settings: %w", err)
		}
		defer env.DeleteLocalRef(settings)

		scannerCls, err := env.FindClass("android/bluetooth/le/BluetoothLeScanner")
		if err != nil {
			return fmt.Errorf("find BluetoothLeScanner class: %w", err)
		}
		defer env.DeleteLocalRef(&scannerCls.Object)

		startScan, err := env.GetMethodID(
			scannerCls,
			"startScan",
			"(Ljava/util/List;Landroid/bluetooth/le/ScanSettings;Landroid/bluetooth/le/ScanCallback;)V",
		)
		if err != nil {
			return fmt.Errorf("find BluetoothLeScanner.startScan overload: %w", err)
		}

		if err := env.CallVoidMethod(
			scanner.Obj,
			startScan,
			jnipkg.ObjectValue(filters),
			jnipkg.ObjectValue(settings),
			jnipkg.ObjectValue(callbackObj),
		); err != nil {
			return fmt.Errorf("call BluetoothLeScanner.startScan overload: %w", err)
		}
		return nil
	})
}

func newEmptyArrayList(env *jnipkg.Env) (*jnipkg.Object, error) {
	listCls, err := env.FindClass("java/util/ArrayList")
	if err != nil {
		return nil, fmt.Errorf("find ArrayList class: %w", err)
	}
	defer env.DeleteLocalRef(&listCls.Object)

	ctor, err := env.GetMethodID(listCls, "<init>", "()V")
	if err != nil {
		return nil, fmt.Errorf("find ArrayList constructor: %w", err)
	}

	listObj, err := env.NewObject(listCls, ctor)
	if err != nil {
		return nil, fmt.Errorf("construct ArrayList: %w", err)
	}
	return listObj, nil
}

func newLowLatencyScanSettings(env *jnipkg.Env) (*jnipkg.Object, error) {
	builderCls, err := env.FindClass("android/bluetooth/le/ScanSettings$Builder")
	if err != nil {
		return nil, fmt.Errorf("find ScanSettings.Builder class: %w", err)
	}
	defer env.DeleteLocalRef(&builderCls.Object)

	ctor, err := env.GetMethodID(builderCls, "<init>", "()V")
	if err != nil {
		return nil, fmt.Errorf("find ScanSettings.Builder constructor: %w", err)
	}

	builderObj, err := env.NewObject(builderCls, ctor)
	if err != nil {
		return nil, fmt.Errorf("construct ScanSettings.Builder: %w", err)
	}
	defer env.DeleteLocalRef(builderObj)

	setScanMode, err := env.GetMethodID(
		builderCls,
		"setScanMode",
		"(I)Landroid/bluetooth/le/ScanSettings$Builder;",
	)
	if err != nil {
		return nil, fmt.Errorf("find ScanSettings.Builder.setScanMode: %w", err)
	}
	if err := callBuilderMethod(env, builderObj, setScanMode, jnipkg.IntValue(androidScanModeLowLatency)); err != nil {
		return nil, fmt.Errorf("set scan mode: %w", err)
	}

	setReportDelay, err := env.GetMethodID(
		builderCls,
		"setReportDelay",
		"(J)Landroid/bluetooth/le/ScanSettings$Builder;",
	)
	if err != nil {
		return nil, fmt.Errorf("find ScanSettings.Builder.setReportDelay: %w", err)
	}
	if err := callBuilderMethod(env, builderObj, setReportDelay, jnipkg.LongValue(0)); err != nil {
		return nil, fmt.Errorf("set report delay: %w", err)
	}

	build, err := env.GetMethodID(builderCls, "build", "()Landroid/bluetooth/le/ScanSettings;")
	if err != nil {
		return nil, fmt.Errorf("find ScanSettings.Builder.build: %w", err)
	}

	settingsObj, err := env.CallObjectMethod(builderObj, build)
	if err != nil {
		return nil, fmt.Errorf("build ScanSettings: %w", err)
	}
	return settingsObj, nil
}

func callBuilderMethod(
	env *jnipkg.Env,
	builderObj *jnipkg.Object,
	method jnipkg.MethodID,
	args ...jnipkg.Value,
) error {
	ret, err := env.CallObjectMethod(builderObj, method, args...)
	if ret != nil {
		env.DeleteLocalRef(ret)
	}
	return err
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
	releaseDevObj := true
	defer func() {
		if releaseDevObj {
			env.DeleteGlobalRef(devObj)
		}
	}()

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

	p, retainedDevObj, shouldReport := d.rememberScanDevice(addr, name, btDev, dup)
	releaseDevObj = !retainedDevObj
	if !shouldReport {
		return
	}

	adv := &gatt.Advertisement{
		LocalName: name,
	}
	scanRecordObj, err := sr.GetScanRecord()
	if err != nil {
		logger.Debugf(ctx, "scanResult.GetScanRecord failed: %v", err)
	}
	if scanRecordObj != nil {
		defer env.DeleteGlobalRef(scanRecordObj)

		scanRecord := &le.ScanRecord{
			VM:  d.vm,
			Obj: scanRecordObj,
		}
		rawObj, err := scanRecord.GetBytes()
		if err != nil {
			logger.Debugf(ctx, "scanRecord.GetBytes failed: %v", err)
		}
		if rawObj != nil {
			raw := byteArrayToGoBytes(env, rawObj)
			env.DeleteGlobalRef(rawObj)

			parsedAdv, err := advertisementFromScanRecord(name, raw)
			if err != nil {
				logger.Debugf(ctx, "unable to parse raw scan record %X: %v", raw, err)
			}
			adv = parsedAdv
		}
	}
	d.logScanDiagnostic(addr, name, int(rssi), adv)

	handler := d.PeripheralDiscovered()
	if handler != nil {
		handler(ctx, p, adv, int(rssi))
	}
}

func (d *device) rememberScanDevice(
	addr string,
	name string,
	btDev *bluetooth.Device,
	dup bool,
) (_ *peripheral, retainedDeviceRef bool, report bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	p, exists := d.peripherals[addr]
	if !exists {
		p = newPeripheral(d, btDev, addr, name)
		d.peripherals[addr] = p
		return p, true, true
	}

	if p.name == "" && name != "" {
		p.name = name
	}
	if !dup {
		return p, false, false
	}
	return p, false, true
}

func (d *device) logScanDiagnostic(
	addr string,
	name string,
	rssi int,
	adv *gatt.Advertisement,
) {
	if adv == nil {
		return
	}

	interesting := scanDiagnosticIsInteresting(name, adv)
	key := fmt.Sprintf("%s|%s|%X", addr, name, adv.ManufacturerData)

	d.mu.Lock()
	if _, ok := d.scanDiagnosticsSeen[key]; ok {
		d.mu.Unlock()
		return
	}
	if !interesting && d.scanDiagnosticsLogs >= scanDiagnosticLogLimit {
		d.mu.Unlock()
		return
	}
	d.scanDiagnosticsSeen[key] = struct{}{}
	d.scanDiagnosticsLogs++
	d.mu.Unlock()

	logcatInfo(
		scanDiagnosticLogTag,
		fmt.Sprintf(
			"scan-result addr=%s name=%q rssi=%d company=0x%04X manufacturer=%X",
			addr,
			name,
			rssi,
			adv.CompanyID,
			adv.ManufacturerData,
		),
	)
}

func scanDiagnosticIsInteresting(
	name string,
	adv *gatt.Advertisement,
) bool {
	lowerName := strings.ToLower(name)
	switch {
	case strings.Contains(lowerName, "dji"):
		return true
	case strings.Contains(lowerName, "osmo"):
		return true
	case strings.Contains(lowerName, "pocket"):
		return true
	case bytes.HasPrefix(adv.ManufacturerData, []byte{0xAA, 0x08}):
		return true
	default:
		return false
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

type androidGATTConnectionResources struct {
	gatt            *bluetooth.Gatt
	callbackRef     *jnipkg.Object
	callbackCleanup func()
	closeGATT       func(*bluetooth.Gatt) error
	deleteGlobalRef func(*jnipkg.Object) error
}

func (d *device) releaseConnectedPeripheral(
	per *peripheral,
	resources androidGATTConnectionResources,
) error {
	closeGATT := resources.closeGATT
	if closeGATT == nil {
		closeGATT = closeAndroidGATT
	}

	deleteGlobalRef := resources.deleteGlobalRef
	if deleteGlobalRef == nil {
		deleteGlobalRef = d.deleteGlobalRef
	}

	per.mu.Lock()
	if per.gattObj == resources.gatt {
		per.gattObj = nil
		per.gattCallbackCleanup = nil
	}
	per.mu.Unlock()

	var errs []error
	if resources.gatt != nil {
		if err := closeGATT(resources.gatt); err != nil {
			errs = append(errs, fmt.Errorf("close BluetoothGatt: %w", err))
		}
		if resources.gatt.Obj != nil {
			if err := deleteGlobalRef(resources.gatt.Obj); err != nil {
				errs = append(errs, fmt.Errorf("delete BluetoothGatt global ref: %w", err))
			}
			resources.gatt.Obj = nil
		}
	}
	if resources.callbackRef != nil {
		if err := deleteGlobalRef(resources.callbackRef); err != nil {
			errs = append(errs, fmt.Errorf("delete GATT callback global ref: %w", err))
		}
	}
	if resources.callbackCleanup != nil {
		resources.callbackCleanup()
	}

	return errors.Join(errs...)
}

func closeAndroidGATT(
	g *bluetooth.Gatt,
) error {
	if g == nil {
		return nil
	}
	return g.Close()
}

func (d *device) deleteGlobalRef(
	obj *jnipkg.Object,
) error {
	if obj == nil {
		return nil
	}

	return d.vm.Do(func(env *jnipkg.Env) error {
		env.DeleteGlobalRef(obj)
		return nil
	})
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
			func(env *jnipkg.Env, methodName string, args []*jnipkg.Object) (*jnipkg.Object, error) {
				return per.handleGattCallback(ctx, env, methodName, args)
			},
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

	connectionResources := androidGATTConnectionResources{
		gatt:            gatt,
		callbackRef:     gattCallbackObj,
		callbackCleanup: gattCleanup,
	}

	// Wait for onConnectionStateChange.
	select {
	case <-ctx.Done():
		cleanupErr := d.releaseConnectedPeripheral(per, connectionResources)
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, errors.Join(ctx.Err(), cleanupErr))
		}
		return
	case newState := <-per.connStateChanged:
		if newState != stateConnected {
			cleanupErr := d.releaseConnectedPeripheral(per, connectionResources)
			handler := d.PeripheralConnected()
			if handler != nil {
				handler(ctx, p, errors.Join(fmt.Errorf("connection failed: state=%d", newState), cleanupErr))
			}
			return
		}
	}

	handler := d.PeripheralConnected()
	if handler != nil {
		handler(ctx, p, nil)
	}

	// Monitor for disconnection in a separate goroutine.
	observability.Go(ctx, func(ctx context.Context) {
		d.monitorDisconnection(ctx, per, connectionResources)
	})
}

// monitorDisconnection waits for a disconnection event and calls the
// peripheralDisconnected handler.
func (d *device) monitorDisconnection(
	ctx context.Context,
	per *peripheral,
	connectionResources androidGATTConnectionResources,
) {
	select {
	case <-ctx.Done():
	case newState := <-per.connStateChanged:
		_ = newState // Always means disconnected at this point.
	}

	cleanupErr := d.releaseConnectedPeripheral(per, connectionResources)

	handler := d.PeripheralDisconnected()
	if handler != nil {
		handler(ctx, per, cleanupErr)
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
