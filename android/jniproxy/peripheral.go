package jniproxy

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/facebookincubator/go-belt/tool/logger"

	jniraw "github.com/AndroidGoLab/jni-proxy/proto/jni_raw"
	"github.com/xaionaro-go/gatt"
)

// Android BluetoothGatt status constants.
const (
	gattSuccess int32 = 0
)

// Android BluetoothProfile connection state constants.
const (
	stateDisconnected int32 = 0
	stateConnected    int32 = 2
)

// Android BluetoothGattCharacteristic write type constants.
const (
	writeTypeDefault int32 = 2
	writeTypeNoResp  int32 = 1
)

// charReadResult bundles a characteristic read response.
type charReadResult struct {
	data []byte
	err  error
}

// descReadResult bundles a descriptor read response.
type descReadResult struct {
	data []byte
	err  error
}

type rssiResult struct {
	rssi int32
	err  error
}

type mtuResult struct {
	mtu int32
	err error
}

// Ensure *peripheral satisfies gatt.Peripheral at compile time.
var _ gatt.Peripheral = (*peripheral)(nil)

// peripheral implements gatt.Peripheral for jni-proxy BLE connections.
type peripheral struct {
	d           *device
	btDevHandle int64  // handle to android.bluetooth.BluetoothDevice
	addr        string
	name        string

	mu          sync.Mutex
	gattHandle  int64 // handle to android.bluetooth.BluetoothGatt (0 = not connected)
	proxyStream jniraw.JNIService_ProxyClient
	proxyDone   chan struct{}

	services []*gatt.Service

	// Maps from VHandle to the server-side handle of the characteristic/descriptor.
	charHandles map[uint16]int64
	descHandles map[uint16]int64

	// Maps from VHandle to the gatt.Characteristic for notification dispatch.
	charByVH map[uint16]*gatt.Characteristic

	mtu         int
	subscribers map[uint16]func(*gatt.Characteristic, []byte, error)

	// Synchronization channels for async GATT callbacks (buffered size 1).
	connStateChanged   chan int32
	servicesDiscovered chan error
	charRead           chan charReadResult
	charWritten        chan error
	descRead           chan descReadResult
	descWritten        chan error
	rssiRead           chan rssiResult
	mtuChanged         chan mtuResult
}

func newPeripheral(
	d *device,
	addr string,
	name string,
	btDevHandle int64,
) *peripheral {
	return &peripheral{
		d:                  d,
		btDevHandle:        btDevHandle,
		addr:               addr,
		name:               name,
		mtu:                23, // BLE default
		subscribers:        make(map[uint16]func(*gatt.Characteristic, []byte, error)),
		charHandles:        make(map[uint16]int64),
		descHandles:        make(map[uint16]int64),
		charByVH:           make(map[uint16]*gatt.Characteristic),
		connStateChanged:   make(chan int32, 1),
		servicesDiscovered: make(chan error, 1),
		charRead:           make(chan charReadResult, 1),
		charWritten:        make(chan error, 1),
		descRead:           make(chan descReadResult, 1),
		descWritten:        make(chan error, 1),
		rssiRead:           make(chan rssiResult, 1),
		mtuChanged:         make(chan mtuResult, 1),
	}
}

func (p *peripheral) Device() gatt.Device                        { return p.d }
func (p *peripheral) ID() string                                 { return p.addr }
func (p *peripheral) Name() string                               { return p.name }
func (p *peripheral) Services(_ context.Context) []*gatt.Service { return p.services }

func (p *peripheral) DiscoverServices(
	ctx context.Context,
	filter []gatt.UUID,
) (_ []*gatt.Service, _err error) {
	logger.Tracef(ctx, "jniproxy.peripheral.DiscoverServices")
	defer func() { logger.Tracef(ctx, "/jniproxy.peripheral.DiscoverServices: %v", _err) }()

	p.mu.Lock()
	g := p.gattHandle
	p.mu.Unlock()

	if g == 0 {
		return nil, fmt.Errorf("not connected")
	}

	ok, err := p.callGattBooleanMethod(ctx, g, "discoverServices", "()Z")
	if err != nil {
		return nil, fmt.Errorf("discoverServices: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("discoverServices returned false")
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case err := <-p.servicesDiscovered:
		if err != nil {
			return nil, fmt.Errorf("service discovery failed: %w", err)
		}
	}

	if err := p.populateServices(ctx, g, filter); err != nil {
		return nil, fmt.Errorf("populating services: %w", err)
	}

	return p.services, nil
}

// populateServices reads the discovered services and their characteristics/descriptors
// from the BluetoothGatt Java object using raw JNI and creates the corresponding
// gatt.* structs.
func (p *peripheral) populateServices(
	ctx context.Context,
	gattHandle int64,
	filter []gatt.UUID,
) error {
	p.services = nil
	p.charHandles = make(map[uint16]int64)
	p.descHandles = make(map[uint16]int64)
	p.charByVH = make(map[uint16]*gatt.Characteristic)

	raw := &p.d.raw
	var handle uint16 = 1

	// gatt.getServices() -> List<BluetoothGattService>
	gattCls, err := raw.findClass(ctx, "android/bluetooth/BluetoothGatt")
	if err != nil {
		return fmt.Errorf("find BluetoothGatt class: %w", err)
	}
	getServicesMethod, err := raw.getMethodID(ctx, gattCls, "getServices", "()Ljava/util/List;")
	if err != nil {
		return fmt.Errorf("get getServices method: %w", err)
	}
	svcListHandle, err := raw.callObjectMethod(ctx, gattHandle, getServicesMethod)
	if err != nil {
		return fmt.Errorf("getServices: %w", err)
	}
	if svcListHandle == 0 {
		return nil
	}
	defer func() {
		_ = p.d.raw.deleteGlobalRef(ctx, svcListHandle)
	}()

	svcHandles, err := raw.javaListToSlice(ctx, svcListHandle)
	if err != nil {
		return fmt.Errorf("iterating services list: %w", err)
	}

	for _, svcHandle := range svcHandles {
		uuidStr, err := p.getObjectUUIDString(ctx, svcHandle)
		if err != nil {
			logger.Warnf(ctx, "skipping service: unable to get UUID: %v", err)
			_ = p.d.raw.deleteGlobalRef(ctx, svcHandle)
			continue
		}

		gattUUID, err := gatt.ParseUUID(uuidStr)
		if err != nil {
			logger.Warnf(ctx, "skipping service: unable to parse UUID %q: %v", uuidStr, err)
			_ = p.d.raw.deleteGlobalRef(ctx, svcHandle)
			continue
		}

		if !gatt.UUIDContains(filter, gattUUID) {
			_ = p.d.raw.deleteGlobalRef(ctx, svcHandle)
			continue
		}

		s := gatt.NewService(gattUUID)
		svcATTHandle := handle
		s.SetHandle(svcATTHandle)
		handle++

		// Enumerate characteristics.
		if err := p.populateCharacteristics(ctx, svcHandle, s, &handle); err != nil {
			logger.Warnf(ctx, "populating characteristics for service %s: %v", uuidStr, err)
		}

		s.SetEndHandle(handle - 1)
		p.services = append(p.services, s)
		_ = p.d.raw.deleteGlobalRef(ctx, svcHandle)
	}

	return nil
}

func (p *peripheral) populateCharacteristics(
	ctx context.Context,
	svcHandle int64,
	s *gatt.Service,
	handle *uint16,
) error {
	raw := &p.d.raw

	svcCls, err := raw.findClass(ctx, "android/bluetooth/BluetoothGattService")
	if err != nil {
		return err
	}
	getCharsMethod, err := raw.getMethodID(ctx, svcCls, "getCharacteristics", "()Ljava/util/List;")
	if err != nil {
		return err
	}
	charListHandle, err := raw.callObjectMethod(ctx, svcHandle, getCharsMethod)
	if err != nil {
		return fmt.Errorf("getCharacteristics: %w", err)
	}
	if charListHandle == 0 {
		return nil
	}
	defer func() {
		_ = p.d.raw.deleteGlobalRef(ctx, charListHandle)
	}()

	charHandles, err := raw.javaListToSlice(ctx, charListHandle)
	if err != nil {
		return fmt.Errorf("iterating characteristics list: %w", err)
	}

	for _, charHandle := range charHandles {
		charUUIDStr, err := p.getObjectUUIDString(ctx, charHandle)
		if err != nil {
			logger.Warnf(ctx, "skipping characteristic: unable to get UUID: %v", err)
			_ = p.d.raw.deleteGlobalRef(ctx, charHandle)
			continue
		}

		charUUID, err := gatt.ParseUUID(charUUIDStr)
		if err != nil {
			logger.Warnf(ctx, "skipping characteristic: unable to parse UUID %q: %v", charUUIDStr, err)
			_ = p.d.raw.deleteGlobalRef(ctx, charHandle)
			continue
		}

		props, err := p.callGetProperties(ctx, charHandle)
		if err != nil {
			logger.Warnf(ctx, "getProperties failed: %v", err)
		}

		charATTHandle := *handle
		*handle++
		charVHandle := *handle
		*handle++

		c := gatt.NewCharacteristic(charUUID, s, gatt.Property(props), charATTHandle, charVHandle)
		p.charHandles[charVHandle] = charHandle
		p.charByVH[charVHandle] = c

		// Enumerate descriptors.
		if err := p.populateDescriptors(ctx, charHandle, c, handle); err != nil {
			logger.Warnf(ctx, "populating descriptors: %v", err)
		}

		c.SetEndHandle(*handle - 1)
	}

	return nil
}

func (p *peripheral) populateDescriptors(
	ctx context.Context,
	charHandle int64,
	c *gatt.Characteristic,
	handle *uint16,
) error {
	raw := &p.d.raw

	charCls, err := raw.findClass(ctx, "android/bluetooth/BluetoothGattCharacteristic")
	if err != nil {
		return err
	}
	getDescsMethod, err := raw.getMethodID(ctx, charCls, "getDescriptors", "()Ljava/util/List;")
	if err != nil {
		return err
	}
	descListHandle, err := raw.callObjectMethod(ctx, charHandle, getDescsMethod)
	if err != nil {
		return fmt.Errorf("getDescriptors: %w", err)
	}
	if descListHandle == 0 {
		return nil
	}
	defer func() {
		_ = p.d.raw.deleteGlobalRef(ctx, descListHandle)
	}()

	descHandles, err := raw.javaListToSlice(ctx, descListHandle)
	if err != nil {
		return fmt.Errorf("iterating descriptors list: %w", err)
	}

	for _, descHandle := range descHandles {
		descUUIDStr, err := p.getObjectUUIDString(ctx, descHandle)
		if err != nil {
			logger.Warnf(ctx, "skipping descriptor: unable to get UUID: %v", err)
			_ = p.d.raw.deleteGlobalRef(ctx, descHandle)
			continue
		}

		descUUID, err := gatt.ParseUUID(descUUIDStr)
		if err != nil {
			logger.Warnf(ctx, "skipping descriptor: unable to parse UUID %q: %v", descUUIDStr, err)
			_ = p.d.raw.deleteGlobalRef(ctx, descHandle)
			continue
		}

		dh := *handle
		*handle++

		d := gatt.NewDescriptor(descUUID, dh, c)
		p.descHandles[dh] = descHandle

		c.SetDescriptors(append(c.Descriptors(), d))

		// Track the CCCD.
		if descUUIDStr == "00002902-0000-1000-8000-00805f9b34fb" {
			c.SetDescriptor(d)
		}
	}

	return nil
}

// getObjectUUIDString calls getUuid().toString() on a Java object that has a getUuid() method
// returning java.util.UUID (e.g., BluetoothGattService, BluetoothGattCharacteristic, BluetoothGattDescriptor).
func (p *peripheral) getObjectUUIDString(ctx context.Context, objHandle int64) (string, error) {
	raw := &p.d.raw

	// Get the object's class.
	objCls, err := raw.findClass(ctx, "java/lang/Object")
	if err != nil {
		return "", err
	}
	getClassMethod, err := raw.getMethodID(ctx, objCls, "getClass", "()Ljava/lang/Class;")
	if err != nil {
		return "", err
	}
	clsHandle, err := raw.callObjectMethod(ctx, objHandle, getClassMethod)
	if err != nil {
		return "", err
	}
	if clsHandle != 0 {
		_ = p.d.raw.deleteGlobalRef(ctx, clsHandle)
	}

	// We know the object has getUuid() method. Look for it via raw JNI using the actual class.
	actualCls, err := func() (int64, error) {
		resp, err := raw.client.GetObjectClass(ctx, &jniraw.GetObjectClassRequest{
			ObjectHandle: objHandle,
		})
		if err != nil {
			return 0, err
		}
		return resp.GetClassHandle(), nil
	}()
	if err != nil {
		return "", fmt.Errorf("GetObjectClass: %w", err)
	}

	getUuidMethod, err := raw.getMethodID(ctx, actualCls, "getUuid", "()Ljava/util/UUID;")
	if err != nil {
		return "", fmt.Errorf("getMethodID(getUuid): %w", err)
	}

	uuidHandle, err := raw.callObjectMethod(ctx, objHandle, getUuidMethod)
	if err != nil {
		return "", fmt.Errorf("getUuid: %w", err)
	}
	if uuidHandle == 0 {
		return "", fmt.Errorf("getUuid returned null")
	}
	defer func() {
		_ = p.d.raw.deleteGlobalRef(ctx, uuidHandle)
	}()

	return raw.objectToString(ctx, uuidHandle)
}

func (p *peripheral) DiscoverIncludedServices(
	_ context.Context,
	_ []gatt.UUID,
	_ *gatt.Service,
) ([]*gatt.Service, error) {
	// Android does not expose included service enumeration.
	return nil, nil
}

func (p *peripheral) DiscoverCharacteristics(
	_ context.Context,
	_ []gatt.UUID,
	s *gatt.Service,
) ([]*gatt.Characteristic, error) {
	// On Android, characteristics are discovered as part of DiscoverServices.
	return s.Characteristics(), nil
}

func (p *peripheral) DiscoverDescriptors(
	_ context.Context,
	_ []gatt.UUID,
	c *gatt.Characteristic,
) ([]*gatt.Descriptor, error) {
	// On Android, descriptors are discovered as part of DiscoverServices.
	return c.Descriptors(), nil
}

func (p *peripheral) ReadCharacteristic(
	ctx context.Context,
	c *gatt.Characteristic,
) (_ []byte, _err error) {
	logger.Tracef(ctx, "jniproxy.peripheral.ReadCharacteristic")
	defer func() { logger.Tracef(ctx, "/jniproxy.peripheral.ReadCharacteristic: %v", _err) }()

	p.mu.Lock()
	g := p.gattHandle
	charHandle := p.charHandles[c.VHandle()]
	p.mu.Unlock()

	if g == 0 {
		return nil, fmt.Errorf("not connected")
	}
	if charHandle == 0 {
		return nil, fmt.Errorf("characteristic not found (vh=%d)", c.VHandle())
	}

	ok, err := p.callGattReadCharacteristic(ctx, g, charHandle)
	if err != nil {
		return nil, fmt.Errorf("readCharacteristic: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("readCharacteristic returned false")
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-p.charRead:
		return r.data, r.err
	}
}

func (p *peripheral) ReadLongCharacteristic(
	ctx context.Context,
	c *gatt.Characteristic,
) ([]byte, error) {
	// Android handles long reads internally.
	return p.ReadCharacteristic(ctx, c)
}

func (p *peripheral) WriteCharacteristic(
	ctx context.Context,
	c *gatt.Characteristic,
	value []byte,
	noResp bool,
) (_err error) {
	logger.Tracef(ctx, "jniproxy.peripheral.WriteCharacteristic")
	defer func() { logger.Tracef(ctx, "/jniproxy.peripheral.WriteCharacteristic: %v", _err) }()

	p.mu.Lock()
	g := p.gattHandle
	charHandle := p.charHandles[c.VHandle()]
	p.mu.Unlock()

	if g == 0 {
		return fmt.Errorf("not connected")
	}
	if charHandle == 0 {
		return fmt.Errorf("characteristic not found (vh=%d)", c.VHandle())
	}

	// Set write type and value, then write via raw JNI.
	wt := writeTypeDefault
	if noResp {
		wt = writeTypeNoResp
	}
	if err := p.callCharSetWriteType(ctx, charHandle, wt); err != nil {
		return fmt.Errorf("setWriteType: %w", err)
	}

	if err := p.callCharSetValue(ctx, charHandle, value); err != nil {
		return fmt.Errorf("setValue: %w", err)
	}

	ok, err := p.callGattWriteCharacteristic(ctx, g, charHandle)
	if err != nil {
		return fmt.Errorf("writeCharacteristic: %w", err)
	}
	if !ok {
		return fmt.Errorf("writeCharacteristic returned false")
	}

	if noResp {
		return nil
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-p.charWritten:
		return err
	}
}

func (p *peripheral) ReadDescriptor(
	ctx context.Context,
	d *gatt.Descriptor,
) (_ []byte, _err error) {
	logger.Tracef(ctx, "jniproxy.peripheral.ReadDescriptor")
	defer func() { logger.Tracef(ctx, "/jniproxy.peripheral.ReadDescriptor: %v", _err) }()

	p.mu.Lock()
	g := p.gattHandle
	descHandle := p.descHandles[d.Handle()]
	p.mu.Unlock()

	if g == 0 {
		return nil, fmt.Errorf("not connected")
	}
	if descHandle == 0 {
		return nil, fmt.Errorf("descriptor not found (h=%d)", d.Handle())
	}

	ok, err := p.callGattReadDescriptor(ctx, g, descHandle)
	if err != nil {
		return nil, fmt.Errorf("readDescriptor: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("readDescriptor returned false")
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-p.descRead:
		return r.data, r.err
	}
}

func (p *peripheral) WriteDescriptor(
	ctx context.Context,
	d *gatt.Descriptor,
	value []byte,
) (_err error) {
	logger.Tracef(ctx, "jniproxy.peripheral.WriteDescriptor")
	defer func() { logger.Tracef(ctx, "/jniproxy.peripheral.WriteDescriptor: %v", _err) }()

	p.mu.Lock()
	g := p.gattHandle
	descHandle := p.descHandles[d.Handle()]
	p.mu.Unlock()

	if g == 0 {
		return fmt.Errorf("not connected")
	}
	if descHandle == 0 {
		return fmt.Errorf("descriptor not found (h=%d)", d.Handle())
	}

	// Set value and write via raw JNI.
	if err := p.callDescSetValue(ctx, descHandle, value); err != nil {
		return fmt.Errorf("setValue: %w", err)
	}

	ok, err := p.callGattWriteDescriptor(ctx, g, descHandle)
	if err != nil {
		return fmt.Errorf("writeDescriptor: %w", err)
	}
	if !ok {
		return fmt.Errorf("writeDescriptor returned false")
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-p.descWritten:
		return err
	}
}

func (p *peripheral) Subscribe(
	vh uint16,
	f func(*gatt.Characteristic, []byte, error),
) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if f == nil {
		delete(p.subscribers, vh)
		return
	}
	p.subscribers[vh] = f
}

func (p *peripheral) SetNotifyValue(
	ctx context.Context,
	c *gatt.Characteristic,
	f func(*gatt.Characteristic, []byte, error),
) error {
	return p.setNotifyValue(ctx, c, gattCCCNotifyFlag, f)
}

func (p *peripheral) SetIndicateValue(
	ctx context.Context,
	c *gatt.Characteristic,
	f func(*gatt.Characteristic, []byte, error),
) error {
	return p.setNotifyValue(ctx, c, gattCCCIndicateFlag, f)
}

const (
	gattCCCNotifyFlag   uint16 = 0x0001
	gattCCCIndicateFlag uint16 = 0x0002
)

func (p *peripheral) setNotifyValue(
	ctx context.Context,
	c *gatt.Characteristic,
	flag uint16,
	f func(*gatt.Characteristic, []byte, error),
) (_err error) {
	logger.Tracef(ctx, "jniproxy.peripheral.setNotifyValue")
	defer func() { logger.Tracef(ctx, "/jniproxy.peripheral.setNotifyValue: %v", _err) }()

	p.mu.Lock()
	g := p.gattHandle
	charHandle := p.charHandles[c.VHandle()]
	p.mu.Unlock()

	if g == 0 {
		return fmt.Errorf("not connected")
	}
	if charHandle == 0 {
		return fmt.Errorf("characteristic not found (vh=%d)", c.VHandle())
	}

	enable := f != nil
	ok, err := p.callGattSetCharacteristicNotification(ctx, g, charHandle, enable)
	if err != nil {
		return fmt.Errorf("setCharacteristicNotification: %w", err)
	}
	if !ok {
		return fmt.Errorf("setCharacteristicNotification returned false")
	}

	// Write the CCCD descriptor to enable/disable on the remote device.
	cccd := c.Descriptor()
	if cccd != nil {
		var val []byte
		if enable {
			val = []byte{byte(flag), byte(flag >> 8)}
		} else {
			val = []byte{0x00, 0x00}
		}
		if err := p.WriteDescriptor(ctx, cccd, val); err != nil {
			return fmt.Errorf("writing CCCD: %w", err)
		}
	}

	p.Subscribe(c.VHandle(), f)
	return nil
}

func (p *peripheral) ReadRSSI(ctx context.Context) int {
	logger.Tracef(ctx, "jniproxy.peripheral.ReadRSSI")
	defer func() { logger.Tracef(ctx, "/jniproxy.peripheral.ReadRSSI") }()

	p.mu.Lock()
	g := p.gattHandle
	p.mu.Unlock()

	if g == 0 {
		return -1
	}

	ok, err := p.callGattBooleanMethod(ctx, g, "readRemoteRssi", "()Z")
	if err != nil || !ok {
		logger.Warnf(ctx, "readRemoteRssi failed: ok=%v err=%v", ok, err)
		return -1
	}

	select {
	case <-ctx.Done():
		return -1
	case r := <-p.rssiRead:
		if r.err != nil {
			logger.Warnf(ctx, "readRemoteRssi callback error: %v", r.err)
			return -1
		}
		return int(r.rssi)
	}
}

func (p *peripheral) SetMTU(
	ctx context.Context,
	mtu uint16,
) (_err error) {
	logger.Tracef(ctx, "jniproxy.peripheral.SetMTU(%d)", mtu)
	defer func() { logger.Tracef(ctx, "/jniproxy.peripheral.SetMTU: %v", _err) }()

	p.mu.Lock()
	g := p.gattHandle
	p.mu.Unlock()

	if g == 0 {
		return fmt.Errorf("not connected")
	}

	ok, err := p.callGattRequestMtu(ctx, g, int32(mtu))
	if err != nil {
		return fmt.Errorf("requestMtu: %w", err)
	}
	if !ok {
		return fmt.Errorf("requestMtu returned false")
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case r := <-p.mtuChanged:
		if r.err != nil {
			return fmt.Errorf("mtu negotiation failed: %w", r.err)
		}
		p.mu.Lock()
		p.mtu = int(r.mtu)
		p.mu.Unlock()
		return nil
	}
}

// handleGattCallback dispatches GATT callback events from the Proxy stream.
func (p *peripheral) handleGattCallback(
	ctx context.Context,
	cb *jniraw.CallbackEvent,
) {
	args := cb.GetArgHandles()

	switch cb.GetMethodName() {
	case "onConnectionStateChange":
		// args: gatt, status, newState
		if len(args) >= 3 {
			newState, err := p.unboxInt(ctx, args[2])
			if err != nil {
				logger.Warnf(ctx, "unboxInt for newState: %v", err)
				return
			}
			select {
			case p.connStateChanged <- newState:
			default:
			}
		}

	case "onServicesDiscovered":
		// args: gatt, status
		var err error
		if len(args) >= 2 {
			status, err2 := p.unboxInt(ctx, args[1])
			if err2 != nil {
				err = err2
			} else if status != gattSuccess {
				err = fmt.Errorf("onServicesDiscovered status=%d", status)
			}
		}
		select {
		case p.servicesDiscovered <- err:
		default:
		}

	case "onCharacteristicRead":
		var result charReadResult
		switch {
		case len(args) >= 4:
			// New API (API 33+): args: gatt, characteristic, value (byte[]), status
			status, _ := p.unboxInt(ctx, args[3])
			if status != gattSuccess {
				result.err = fmt.Errorf("onCharacteristicRead status=%d", status)
			} else {
				result.data, _ = p.d.raw.getByteArrayData(ctx, args[2])
			}
		case len(args) >= 3:
			// Old API: args: gatt, characteristic, status
			status, _ := p.unboxInt(ctx, args[2])
			if status != gattSuccess {
				result.err = fmt.Errorf("onCharacteristicRead status=%d", status)
			} else if args[1] != 0 {
				valHandle, err := p.callGetValue(ctx, args[1])
				if err == nil && valHandle != 0 {
					result.data, _ = p.d.raw.getByteArrayData(ctx, valHandle)
					_ = p.d.raw.deleteGlobalRef(ctx, valHandle)
				}
			}
		}
		select {
		case p.charRead <- result:
		default:
		}

	case "onCharacteristicWrite":
		// args: gatt, characteristic, status
		var err error
		if len(args) >= 3 {
			status, _ := p.unboxInt(ctx, args[2])
			if status != gattSuccess {
				err = fmt.Errorf("onCharacteristicWrite status=%d", status)
			}
		}
		select {
		case p.charWritten <- err:
		default:
		}

	case "onCharacteristicChanged":
		// Newer API (API 33+): args: gatt, characteristic, value (byte[])
		// Older API: args: gatt, characteristic
		var data []byte
		switch {
		case len(args) >= 3 && args[2] != 0:
			data, _ = p.d.raw.getByteArrayData(ctx, args[2])
		case len(args) >= 2 && args[1] != 0:
			valHandle, err := p.callGetValue(ctx, args[1])
			if err == nil && valHandle != 0 {
				data, _ = p.d.raw.getByteArrayData(ctx, valHandle)
				_ = p.d.raw.deleteGlobalRef(ctx, valHandle)
			}
		}
		p.dispatchNotification(ctx, args, data)

	case "onDescriptorRead":
		var result descReadResult
		if len(args) >= 3 {
			status, _ := p.unboxInt(ctx, args[2])
			if status != gattSuccess {
				result.err = fmt.Errorf("onDescriptorRead status=%d", status)
			} else if args[1] != 0 {
				valHandle, err := p.callGetValue(ctx, args[1])
				if err == nil && valHandle != 0 {
					result.data, _ = p.d.raw.getByteArrayData(ctx, valHandle)
					_ = p.d.raw.deleteGlobalRef(ctx, valHandle)
				}
			}
		}
		select {
		case p.descRead <- result:
		default:
		}

	case "onDescriptorWrite":
		var err error
		if len(args) >= 3 {
			status, _ := p.unboxInt(ctx, args[2])
			if status != gattSuccess {
				err = fmt.Errorf("onDescriptorWrite status=%d", status)
			}
		}
		select {
		case p.descWritten <- err:
		default:
		}

	case "onReadRemoteRssi":
		// args: gatt, rssi, status
		var r rssiResult
		if len(args) >= 3 {
			status, _ := p.unboxInt(ctx, args[2])
			if status != gattSuccess {
				r.err = fmt.Errorf("onReadRemoteRssi status=%d", status)
			} else {
				r.rssi, _ = p.unboxInt(ctx, args[1])
			}
		}
		select {
		case p.rssiRead <- r:
		default:
		}

	case "onMtuChanged":
		// args: gatt, mtu, status
		var r mtuResult
		if len(args) >= 3 {
			status, _ := p.unboxInt(ctx, args[2])
			if status != gattSuccess {
				r.err = fmt.Errorf("onMtuChanged status=%d", status)
			} else {
				r.mtu, _ = p.unboxInt(ctx, args[1])
			}
		}
		select {
		case p.mtuChanged <- r:
		default:
		}
	}
}

// unboxInt extracts an int from a boxed java.lang.Integer handle.
func (p *peripheral) unboxInt(ctx context.Context, handle int64) (int32, error) {
	if handle == 0 {
		return 0, nil
	}
	intCls, err := p.d.raw.findClass(ctx, "java/lang/Integer")
	if err != nil {
		return 0, err
	}
	intValueMethod, err := p.d.raw.getMethodID(ctx, intCls, "intValue", "()I")
	if err != nil {
		return 0, err
	}
	return p.d.raw.callIntMethod(ctx, handle, intValueMethod)
}

// dispatchNotification finds the subscriber for a changed characteristic
// and calls the callback.
func (p *peripheral) dispatchNotification(
	ctx context.Context,
	args []int64,
	data []byte,
) {
	if len(args) < 2 || args[1] == 0 {
		return
	}
	charObjHandle := args[1]

	// Match by iterating over known characteristic handles and comparing.
	p.mu.Lock()
	var matchedVH uint16
	var matchedChar *gatt.Characteristic
	for vh, h := range p.charHandles {
		if h == charObjHandle {
			matchedVH = vh
			matchedChar = p.charByVH[vh]
			break
		}
	}
	fn := p.subscribers[matchedVH]
	p.mu.Unlock()

	if fn != nil && matchedChar != nil {
		go fn(matchedChar, data, nil)
	}
}

// processGattProxy reads callback events from the Proxy stream and dispatches
// them to handleGattCallback.
func (p *peripheral) processGattProxy(
	ctx context.Context,
	stream jniraw.JNIService_ProxyClient,
	done chan struct{},
) {
	defer close(done)

	for {
		msg, err := stream.Recv()
		if err != nil {
			if err != io.EOF && ctx.Err() == nil {
				logger.Warnf(ctx, "gatt proxy stream error: %v", err)
			}
			return
		}

		cb := msg.GetCallback()
		if cb == nil {
			continue
		}

		p.handleGattCallback(ctx, cb)

		// Respond if the callback expects a response.
		if cb.GetExpectsResponse() {
			_ = stream.Send(&jniraw.ProxyClientMessage{
				Msg: &jniraw.ProxyClientMessage_CallbackResponse{
					CallbackResponse: &jniraw.CallbackResponse{
						CallbackId:   cb.GetCallbackId(),
						ResultHandle: 0,
					},
				},
			})
		}
	}
}
