package jniproxy

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/facebookincubator/go-belt/tool/logger"

	jniraw "github.com/AndroidGoLab/jni-proxy/proto/jni_raw"
	"github.com/xaionaro-go/gatt"
	"github.com/xaionaro-go/gatt/android"
	"google.golang.org/grpc"
)

func init() {
	android.Register(android.BackendJNIProxy, newDevice)
}

// device implements gatt.Device using jni-proxy gRPC calls.
type device struct {
	gatt.DeviceHandler

	cfg config

	conn grpc.ClientConnInterface

	// Raw JNI helper — used for ALL JNI operations.
	raw rawJNI

	// Android object handles.
	appCtxHandle    int64
	adapterHandle   int64
	scannerHandle   int64

	state gatt.State

	mu       sync.Mutex
	services []*gatt.Service

	// Scanning state (protected by mu).
	scanCtxCancel context.CancelFunc
	scanProxyDone chan struct{}

	// Known peripherals by address (protected by mu).
	peripherals map[string]*peripheral
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
	if d.cfg.conn == nil {
		return nil, fmt.Errorf("jniproxy: WithGRPCConn option is required")
	}
	d.conn = d.cfg.conn

	// Create raw JNI client — used for all operations.
	d.raw = rawJNI{client: jniraw.NewJNIServiceClient(d.conn)}

	return d, nil
}

func (d *device) ID() int { return 0 }

func (d *device) Start(
	ctx context.Context,
	stateChanged func(context.Context, gatt.Device, gatt.State),
) error {
	logger.Tracef(ctx, "jniproxy.device.Start")
	defer func() { logger.Tracef(ctx, "/jniproxy.device.Start") }()

	// Get the Android application context handle.
	appCtx, err := d.raw.getAppContext(ctx)
	if err != nil {
		return fmt.Errorf("getting app context: %w", err)
	}
	d.appCtxHandle = appCtx

	// Get BluetoothManager via Context.getSystemService("bluetooth").
	ctxCls, err := d.raw.findClass(ctx, "android/content/Context")
	if err != nil {
		return fmt.Errorf("find Context class: %w", err)
	}
	getSystemServiceMethod, err := d.raw.getMethodID(ctx, ctxCls, "getSystemService",
		"(Ljava/lang/String;)Ljava/lang/Object;")
	if err != nil {
		return fmt.Errorf("get getSystemService method: %w", err)
	}
	btServiceStr, err := d.raw.newStringUTF(ctx, "bluetooth")
	if err != nil {
		return fmt.Errorf("create bluetooth string: %w", err)
	}
	defer func() { _ = d.raw.deleteGlobalRef(ctx, btServiceStr) }()

	managerHandle, err := d.raw.callObjectMethod(ctx, d.appCtxHandle, getSystemServiceMethod, jvalObj(btServiceStr))
	if err != nil {
		return fmt.Errorf("Context.getSystemService(bluetooth): %w", err)
	}
	if managerHandle == 0 {
		return fmt.Errorf("BluetoothManager is null (device may not support Bluetooth)")
	}
	defer func() { _ = d.raw.deleteGlobalRef(ctx, managerHandle) }()

	// Get BluetoothAdapter via BluetoothManager.getAdapter().
	managerCls, err := d.raw.findClass(ctx, "android/bluetooth/BluetoothManager")
	if err != nil {
		return fmt.Errorf("find BluetoothManager class: %w", err)
	}
	getAdapterMethod, err := d.raw.getMethodID(ctx, managerCls, "getAdapter",
		"()Landroid/bluetooth/BluetoothAdapter;")
	if err != nil {
		return fmt.Errorf("get getAdapter method: %w", err)
	}
	adapterHandle, err := d.raw.callObjectMethod(ctx, managerHandle, getAdapterMethod)
	if err != nil {
		return fmt.Errorf("BluetoothManager.getAdapter: %w", err)
	}
	if adapterHandle == 0 {
		return fmt.Errorf("BluetoothAdapter is null (device may not support Bluetooth)")
	}
	d.adapterHandle = adapterHandle

	// Check if adapter is enabled via BluetoothAdapter.isEnabled().
	adapterCls, err := d.raw.findClass(ctx, "android/bluetooth/BluetoothAdapter")
	if err != nil {
		logger.Warnf(ctx, "find BluetoothAdapter class: %v", err)
	} else {
		isEnabledMethod, err := d.raw.getMethodID(ctx, adapterCls, "isEnabled", "()Z")
		if err != nil {
			logger.Warnf(ctx, "get isEnabled method: %v", err)
		} else {
			enabled, err := d.raw.callBooleanMethod(ctx, d.adapterHandle, isEnabledMethod)
			if err != nil {
				logger.Warnf(ctx, "checking adapter enabled: %v", err)
			} else if !enabled {
				logger.Warnf(ctx, "Bluetooth adapter is not enabled")
			}
		}
	}

	d.state = gatt.StatePoweredOn
	d.SetStateChanged(stateChanged)
	go stateChanged(ctx, d, d.state)
	return nil
}

func (d *device) Stop() error {
	_ = d.StopScanning()

	// Release handles.
	ctx := context.Background()
	if d.scannerHandle != 0 {
		_ = d.raw.deleteGlobalRef(ctx, d.scannerHandle)
		d.scannerHandle = 0
	}
	if d.adapterHandle != 0 {
		_ = d.raw.deleteGlobalRef(ctx, d.adapterHandle)
		d.adapterHandle = 0
	}
	if d.appCtxHandle != 0 {
		_ = d.raw.deleteGlobalRef(ctx, d.appCtxHandle)
		d.appCtxHandle = 0
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

func (d *device) RemoveAllServices(ctx context.Context) error {
	logger.Tracef(ctx, "jniproxy.device.RemoveAllServices")
	d.mu.Lock()
	defer d.mu.Unlock()
	d.services = nil
	return nil
}

func (d *device) AddService(ctx context.Context, s *gatt.Service) error {
	logger.Tracef(ctx, "jniproxy.device.AddService")
	d.mu.Lock()
	defer d.mu.Unlock()
	d.services = append(d.services, s)
	return nil
}

func (d *device) SetServices(ctx context.Context, ss []*gatt.Service) error {
	logger.Tracef(ctx, "jniproxy.device.SetServices")
	d.mu.Lock()
	defer d.mu.Unlock()
	d.services = ss
	return nil
}

// Advertising is not currently supported via jni-proxy.
func (d *device) Advertise(_ context.Context, _ *gatt.AdvPacket) error {
	return fmt.Errorf("jniproxy: Advertise not implemented")
}

func (d *device) AdvertiseNameAndServices(_ context.Context, _ string, _ []gatt.UUID) error {
	return fmt.Errorf("jniproxy: AdvertiseNameAndServices not implemented")
}

func (d *device) AdvertiseNameAndIBeaconData(_ context.Context, _ string, _ []byte) error {
	return fmt.Errorf("jniproxy: AdvertiseNameAndIBeaconData not implemented")
}

func (d *device) AdvertiseIBeaconData(_ context.Context, _ []byte) error {
	return fmt.Errorf("jniproxy: AdvertiseIBeaconData not implemented")
}

func (d *device) AdvertiseIBeacon(_ context.Context, _ gatt.UUID, _, _ uint16, _ int8) error {
	return fmt.Errorf("jniproxy: AdvertiseIBeacon not implemented")
}

func (d *device) StopAdvertising(_ context.Context) error {
	return nil
}

// Ensure *device satisfies gatt.Device at compile time.
var _ gatt.Device = (*device)(nil)
