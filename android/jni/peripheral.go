package jni

import (
	"context"
	"fmt"
	"sync"
	"unsafe"

	"github.com/facebookincubator/go-belt/tool/logger"

	jnipkg "github.com/AndroidGoLab/jni"
	"github.com/AndroidGoLab/jni/bluetooth"
	"github.com/xaionaro-go/gatt"
)

// gattStatus constants from Android BluetoothGatt.
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
	writeTypeDefault   int32 = 2
	writeTypeNoResp    int32 = 1
	writeTypeSigned    int32 = 4
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

// peripheral implements gatt.Peripheral for Android JNI BLE connections.
type peripheral struct {
	d     *device
	btDev *bluetooth.Device
	addr  string
	name  string

	mu      sync.Mutex
	gattObj *bluetooth.Gatt

	services []*gatt.Service

	// Maps from VHandle to the JNI global ref of the characteristic object,
	// and from descriptor handle to its JNI global ref.
	charObjs map[uint16]*jnipkg.Object
	descObjs map[uint16]*jnipkg.Object

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

	// Cleanup function for the GATT callback proxy.
	gattCallbackCleanup func()
}

type rssiResult struct {
	rssi int32
	err  error
}

type mtuResult struct {
	mtu int32
	err error
}

func newPeripheral(
	d *device,
	btDev *bluetooth.Device,
	addr string,
	name string,
) *peripheral {
	return &peripheral{
		d:                  d,
		btDev:              btDev,
		addr:               addr,
		name:               name,
		mtu:                23, // BLE default
		subscribers:        make(map[uint16]func(*gatt.Characteristic, []byte, error)),
		charObjs:           make(map[uint16]*jnipkg.Object),
		descObjs:           make(map[uint16]*jnipkg.Object),
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
	logger.Tracef(ctx, "peripheral.DiscoverServices")
	defer func() { logger.Tracef(ctx, "/peripheral.DiscoverServices: %v", _err) }()

	p.mu.Lock()
	g := p.gattObj
	p.mu.Unlock()

	if g == nil {
		return nil, fmt.Errorf("not connected")
	}

	ok, err := g.DiscoverServices()
	if err != nil {
		return nil, fmt.Errorf("discoverServices JNI call: %w", err)
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

	// Enumerate services from the BluetoothGatt object.
	if err := p.populateServices(ctx, g, filter); err != nil {
		return nil, fmt.Errorf("populating services: %w", err)
	}

	return p.services, nil
}

// populateServices reads the discovered services and their characteristics/descriptors
// from the BluetoothGatt Java object and creates the corresponding gatt.* structs.
func (p *peripheral) populateServices(
	ctx context.Context,
	g *bluetooth.Gatt,
	filter []gatt.UUID,
) error {
	p.services = nil
	p.charObjs = make(map[uint16]*jnipkg.Object)
	p.descObjs = make(map[uint16]*jnipkg.Object)
	p.charByVH = make(map[uint16]*gatt.Characteristic)

	vm := p.d.vm
	var handle uint16 = 1

	err := vm.Do(func(env *jnipkg.Env) error {
		// Get List<BluetoothGattService> from gatt.getServices().
		svcListObj, err := g.GetServices()
		if err != nil {
			return fmt.Errorf("getServices: %w", err)
		}
		if svcListObj == nil {
			return nil
		}
		defer env.DeleteGlobalRef(svcListObj)

		svcObjs, err := javaListToSlice(env, svcListObj)
		if err != nil {
			return fmt.Errorf("iterating services list: %w", err)
		}

		for _, svcObj := range svcObjs {
			svc := &bluetooth.GattService{VM: vm, Obj: svcObj}

			uuidStr, err := javaUUIDToString(env, vm, svc)
			if err != nil {
				logger.Warnf(ctx, "skipping service: unable to get UUID: %v", err)
				env.DeleteGlobalRef(svcObj)
				continue
			}

			gattUUID, err := gatt.ParseUUID(uuidStr)
			if err != nil {
				logger.Warnf(ctx, "skipping service: unable to parse UUID %q: %v", uuidStr, err)
				env.DeleteGlobalRef(svcObj)
				continue
			}

			if !gatt.UUIDContains(filter, gattUUID) {
				env.DeleteGlobalRef(svcObj)
				continue
			}

			s := gatt.NewService(gattUUID)
			svcHandle := handle
			s.SetHandle(svcHandle)
			handle++

			// Enumerate characteristics.
			charListObj, err := svc.GetCharacteristics()
			if err != nil {
				logger.Warnf(ctx, "getCharacteristics failed for service %s: %v", uuidStr, err)
				env.DeleteGlobalRef(svcObj)
				p.services = append(p.services, s)
				continue
			}

			if charListObj != nil {
				charObjs, err := javaListToSlice(env, charListObj)
				env.DeleteGlobalRef(charListObj)
				if err != nil {
					logger.Warnf(ctx, "iterating characteristics list: %v", err)
				}

				for _, charObj := range charObjs {
					charW := &bluetooth.GattCharacteristic{VM: vm, Obj: charObj}

					charUUIDStr, err := javaCharUUIDToString(env, vm, charW)
					if err != nil {
						logger.Warnf(ctx, "skipping characteristic: unable to get UUID: %v", err)
						env.DeleteGlobalRef(charObj)
						continue
					}

					charUUID, err := gatt.ParseUUID(charUUIDStr)
					if err != nil {
						logger.Warnf(ctx, "skipping characteristic: unable to parse UUID %q: %v", charUUIDStr, err)
						env.DeleteGlobalRef(charObj)
						continue
					}

					props, err := charW.GetProperties()
					if err != nil {
						logger.Warnf(ctx, "getProperties failed: %v", err)
					}

					charHandle := handle
					handle++
					charVHandle := handle
					handle++

					c := gatt.NewCharacteristic(charUUID, s, gatt.Property(props), charHandle, charVHandle)

					p.charObjs[charVHandle] = charObj
					p.charByVH[charVHandle] = c

					// Enumerate descriptors.
					descListObj, err := charW.GetDescriptors()
					if err != nil {
						logger.Warnf(ctx, "getDescriptors failed: %v", err)
					}
					if descListObj != nil {
						descObjs, err := javaListToSlice(env, descListObj)
						env.DeleteGlobalRef(descListObj)
						if err != nil {
							logger.Warnf(ctx, "iterating descriptors list: %v", err)
						}

						for _, descObj := range descObjs {
							descW := &bluetooth.GattDescriptor{VM: vm, Obj: descObj}

							descUUIDStr, err := javaDescUUIDToString(env, vm, descW)
							if err != nil {
								logger.Warnf(ctx, "skipping descriptor: unable to get UUID: %v", err)
								env.DeleteGlobalRef(descObj)
								continue
							}

							descUUID, err := gatt.ParseUUID(descUUIDStr)
							if err != nil {
								logger.Warnf(ctx, "skipping descriptor: unable to parse UUID %q: %v", descUUIDStr, err)
								env.DeleteGlobalRef(descObj)
								continue
							}

							dh := handle
							handle++

							d := gatt.NewDescriptor(descUUID, dh, c)
							p.descObjs[dh] = descObj

							c.SetDescriptors(append(c.Descriptors(), d))

							// Track the CCCD.
							if descUUIDStr == "00002902-0000-1000-8000-00805f9b34fb" {
								c.SetDescriptor(d)
							}
						}
					}

					c.SetEndHandle(handle - 1)
				}
			}

			s.SetEndHandle(handle - 1)
			p.services = append(p.services, s)
			env.DeleteGlobalRef(svcObj)
		}

		return nil
	})
	return err
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
	logger.Tracef(ctx, "peripheral.ReadCharacteristic")
	defer func() { logger.Tracef(ctx, "/peripheral.ReadCharacteristic: %v", _err) }()

	p.mu.Lock()
	g := p.gattObj
	charObj := p.charObjs[c.VHandle()]
	p.mu.Unlock()

	if g == nil {
		return nil, fmt.Errorf("not connected")
	}
	if charObj == nil {
		return nil, fmt.Errorf("characteristic not found (vh=%d)", c.VHandle())
	}

	ok, err := g.ReadCharacteristic(charObj)
	if err != nil {
		return nil, fmt.Errorf("readCharacteristic JNI call: %w", err)
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
	logger.Tracef(ctx, "peripheral.WriteCharacteristic")
	defer func() { logger.Tracef(ctx, "/peripheral.WriteCharacteristic: %v", _err) }()

	p.mu.Lock()
	g := p.gattObj
	charObj := p.charObjs[c.VHandle()]
	p.mu.Unlock()

	if g == nil {
		return fmt.Errorf("not connected")
	}
	if charObj == nil {
		return fmt.Errorf("characteristic not found (vh=%d)", c.VHandle())
	}

	vm := p.d.vm

	// Use the deprecated setValue+writeCharacteristic API for broad compatibility.
	err := vm.Do(func(env *jnipkg.Env) error {
		byteArr := env.NewByteArray(int32(len(value)))
		if byteArr == nil {
			return fmt.Errorf("failed to allocate byte array")
		}
		defer env.DeleteLocalRef(&byteArr.Object)

		if len(value) > 0 {
			env.SetByteArrayRegion(byteArr, 0, int32(len(value)), unsafe.Pointer(&value[0]))
		}

		charW := &bluetooth.GattCharacteristic{VM: vm, Obj: charObj}
		if noResp {
			if err := charW.SetWriteType(writeTypeNoResp); err != nil {
				return fmt.Errorf("setWriteType: %w", err)
			}
		} else {
			if err := charW.SetWriteType(writeTypeDefault); err != nil {
				return fmt.Errorf("setWriteType: %w", err)
			}
		}

		if _, err := charW.SetValue1(&byteArr.Object); err != nil {
			return fmt.Errorf("setValue: %w", err)
		}

		return nil
	})
	if err != nil {
		return err
	}

	ok, err := g.WriteCharacteristic1(charObj)
	if err != nil {
		return fmt.Errorf("writeCharacteristic JNI call: %w", err)
	}
	if !ok {
		return fmt.Errorf("writeCharacteristic returned false")
	}

	if noResp {
		// Write-without-response does not get a callback.
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
	logger.Tracef(ctx, "peripheral.ReadDescriptor")
	defer func() { logger.Tracef(ctx, "/peripheral.ReadDescriptor: %v", _err) }()

	p.mu.Lock()
	g := p.gattObj
	descObj := p.descObjs[d.Handle()]
	p.mu.Unlock()

	if g == nil {
		return nil, fmt.Errorf("not connected")
	}
	if descObj == nil {
		return nil, fmt.Errorf("descriptor not found (h=%d)", d.Handle())
	}

	ok, err := g.ReadDescriptor(descObj)
	if err != nil {
		return nil, fmt.Errorf("readDescriptor JNI call: %w", err)
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
	logger.Tracef(ctx, "peripheral.WriteDescriptor")
	defer func() { logger.Tracef(ctx, "/peripheral.WriteDescriptor: %v", _err) }()

	p.mu.Lock()
	g := p.gattObj
	descObj := p.descObjs[d.Handle()]
	p.mu.Unlock()

	if g == nil {
		return fmt.Errorf("not connected")
	}
	if descObj == nil {
		return fmt.Errorf("descriptor not found (h=%d)", d.Handle())
	}

	vm := p.d.vm

	err := vm.Do(func(env *jnipkg.Env) error {
		byteArr := env.NewByteArray(int32(len(value)))
		if byteArr == nil {
			return fmt.Errorf("failed to allocate byte array")
		}
		defer env.DeleteLocalRef(&byteArr.Object)

		if len(value) > 0 {
			env.SetByteArrayRegion(byteArr, 0, int32(len(value)), unsafe.Pointer(&value[0]))
		}

		descW := &bluetooth.GattDescriptor{VM: vm, Obj: descObj}
		if _, err := descW.SetValue(&byteArr.Object); err != nil {
			return fmt.Errorf("setValue: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}

	ok, err := g.WriteDescriptor1(descObj)
	if err != nil {
		return fmt.Errorf("writeDescriptor JNI call: %w", err)
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

// gattCCCNotifyFlag and gattCCCIndicateFlag are CCCD descriptor values
// for enabling notifications/indications.
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
	logger.Tracef(ctx, "peripheral.setNotifyValue")
	defer func() { logger.Tracef(ctx, "/peripheral.setNotifyValue: %v", _err) }()

	p.mu.Lock()
	g := p.gattObj
	charObj := p.charObjs[c.VHandle()]
	p.mu.Unlock()

	if g == nil {
		return fmt.Errorf("not connected")
	}
	if charObj == nil {
		return fmt.Errorf("characteristic not found (vh=%d)", c.VHandle())
	}

	enable := f != nil
	ok, err := g.SetCharacteristicNotification(charObj, enable)
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
	logger.Tracef(ctx, "peripheral.ReadRSSI")
	defer func() { logger.Tracef(ctx, "/peripheral.ReadRSSI") }()

	p.mu.Lock()
	g := p.gattObj
	p.mu.Unlock()

	if g == nil {
		return -1
	}

	ok, err := g.ReadRemoteRssi()
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
	logger.Tracef(ctx, "peripheral.SetMTU(%d)", mtu)
	defer func() { logger.Tracef(ctx, "/peripheral.SetMTU: %v", _err) }()

	p.mu.Lock()
	g := p.gattObj
	p.mu.Unlock()

	if g == nil {
		return fmt.Errorf("not connected")
	}

	ok, err := g.RequestMtu(int32(mtu))
	if err != nil {
		return fmt.Errorf("requestMtu JNI call: %w", err)
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

// handleGattCallback dispatches GATT callback events from the Java side
// to the appropriate synchronization channels.
func (p *peripheral) handleGattCallback(
	env *jnipkg.Env,
	methodName string,
	args []*jnipkg.Object,
) (*jnipkg.Object, error) {
	switch methodName {
	case "onConnectionStateChange":
		// args: gatt, status, newState
		if len(args) >= 3 {
			newState := unboxInt(env, args[2])
			select {
			case p.connStateChanged <- newState:
			default:
			}
		}

	case "onServicesDiscovered":
		// args: gatt, status
		var err error
		if len(args) >= 2 {
			status := unboxInt(env, args[1])
			if status != gattSuccess {
				err = fmt.Errorf("onServicesDiscovered status=%d", status)
			}
		}
		select {
		case p.servicesDiscovered <- err:
		default:
		}

	case "onCharacteristicRead":
		// Newer API (API 33+): args: gatt, characteristic, value (byte[]), status
		// Older API: args: gatt, characteristic, status
		var result charReadResult
		switch {
		case len(args) >= 4:
			// New API: args[2] = byte[], args[3] = status
			status := unboxInt(env, args[3])
			if status != gattSuccess {
				result.err = fmt.Errorf("onCharacteristicRead status=%d", status)
			} else {
				result.data = byteArrayToGoBytes(env, args[2])
			}
		case len(args) >= 3:
			// Old API: args[2] = status, read value from characteristic object
			status := unboxInt(env, args[2])
			if status != gattSuccess {
				result.err = fmt.Errorf("onCharacteristicRead status=%d", status)
			} else if args[1] != nil {
				charW := &bluetooth.GattCharacteristic{
					VM:  p.d.vm,
					Obj: args[1],
				}
				valObj, err := charW.GetValue()
				if err == nil && valObj != nil {
					result.data = byteArrayToGoBytes(env, valObj)
					env.DeleteGlobalRef(valObj)
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
			status := unboxInt(env, args[2])
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
		case len(args) >= 3 && args[2] != nil:
			data = byteArrayToGoBytes(env, args[2])
		case len(args) >= 2 && args[1] != nil:
			charW := &bluetooth.GattCharacteristic{
				VM:  p.d.vm,
				Obj: args[1],
			}
			valObj, err := charW.GetValue()
			if err == nil && valObj != nil {
				data = byteArrayToGoBytes(env, valObj)
				env.DeleteGlobalRef(valObj)
			}
		}
		p.dispatchNotification(env, args, data)

	case "onDescriptorRead":
		// args: gatt, descriptor, status
		var result descReadResult
		if len(args) >= 3 {
			status := unboxInt(env, args[2])
			if status != gattSuccess {
				result.err = fmt.Errorf("onDescriptorRead status=%d", status)
			} else if args[1] != nil {
				descW := &bluetooth.GattDescriptor{
					VM:  p.d.vm,
					Obj: args[1],
				}
				valObj, err := descW.GetValue()
				if err == nil && valObj != nil {
					result.data = byteArrayToGoBytes(env, valObj)
					env.DeleteGlobalRef(valObj)
				}
			}
		}
		select {
		case p.descRead <- result:
		default:
		}

	case "onDescriptorWrite":
		// args: gatt, descriptor, status
		var err error
		if len(args) >= 3 {
			status := unboxInt(env, args[2])
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
			status := unboxInt(env, args[2])
			if status != gattSuccess {
				r.err = fmt.Errorf("onReadRemoteRssi status=%d", status)
			} else {
				r.rssi = unboxInt(env, args[1])
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
			status := unboxInt(env, args[2])
			if status != gattSuccess {
				r.err = fmt.Errorf("onMtuChanged status=%d", status)
			} else {
				r.mtu = unboxInt(env, args[1])
			}
		}
		select {
		case p.mtuChanged <- r:
		default:
		}
	}

	return nil, nil
}

// dispatchNotification finds the subscriber for a changed characteristic
// and calls the callback.
func (p *peripheral) dispatchNotification(
	env *jnipkg.Env,
	args []*jnipkg.Object,
	data []byte,
) {
	if len(args) < 2 || args[1] == nil {
		return
	}

	// Match by iterating over known characteristic objects and comparing refs.
	p.mu.Lock()
	var matchedVH uint16
	var matchedChar *gatt.Characteristic
	for vh, obj := range p.charObjs {
		if obj.Ref() == args[1].Ref() {
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
