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

// Android GATT server response status constants.
const (
	gattServerSuccess       int32 = 0
	gattServerInvalidOffset int32 = 7
)

// Android BluetoothGattService type constants.
const (
	serviceTypePrimary int32 = 0
)

// CCCD UUID for client characteristic configuration descriptor.
const cccdUUID = "00002902-0000-1000-8000-00805f9b34fb"

// Ensure *serverNotifier satisfies gatt.Notifier at compile time.
var _ gatt.Notifier = (*serverNotifier)(nil)

// gattServerState holds the JNI resources for an active GATT server.
type gattServerState struct {
	server  *bluetooth.GattServer
	cleanup func()

	// Maps Java characteristic/descriptor UUID strings to gatt objects for dispatch.
	charByUUID map[string]*gatt.Characteristic
	descByUUID map[string]*gatt.Descriptor

	// Maps Java characteristic UUID strings to their JNI global refs
	// (needed for sending notifications).
	charObjByUUID map[string]*jnipkg.Object

	// Connected centrals keyed by device address.
	mu       sync.Mutex
	centrals map[string]*central

	// Notifiers keyed by characteristic UUID + central address.
	notifiers map[string]*serverNotifier
}

// serverNotifier implements gatt.Notifier for a GATT server characteristic.
type serverNotifier struct {
	gs      *gattServerState
	d       *device
	c       *central
	char    *gatt.Characteristic
	charObj *jnipkg.Object
	confirm bool // true for indications, false for notifications

	mu   sync.RWMutex
	done bool
}

func (n *serverNotifier) Write(data []byte) (int, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.done {
		return 0, fmt.Errorf("central stopped notifications")
	}

	vm := n.d.vm

	// Set the characteristic value first (deprecated API for broad compat).
	err := vm.Do(func(env *jnipkg.Env) error {
		byteArr := env.NewByteArray(int32(len(data)))
		if byteArr == nil {
			return fmt.Errorf("failed to allocate byte array")
		}
		defer env.DeleteLocalRef(&byteArr.Object)

		if len(data) > 0 {
			env.SetByteArrayRegion(byteArr, 0, int32(len(data)), unsafe.Pointer(&data[0]))
		}

		charW := &bluetooth.GattCharacteristic{VM: vm, Obj: n.charObj}
		if _, err := charW.SetValue1(&byteArr.Object); err != nil {
			return fmt.Errorf("setValue: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}

	ok, err := n.gs.server.NotifyCharacteristicChanged3(
		n.c.btDev.Obj, n.charObj, n.confirm,
	)
	if err != nil {
		return 0, fmt.Errorf("notifyCharacteristicChanged: %w", err)
	}
	if !ok {
		return 0, fmt.Errorf("notifyCharacteristicChanged returned false")
	}

	return len(data), nil
}

func (n *serverNotifier) Done() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.done
}

func (n *serverNotifier) Cap() int {
	return n.c.MTU() - 3 // ATT header overhead
}

func (n *serverNotifier) stop() {
	n.mu.Lock()
	n.done = true
	n.mu.Unlock()
}

// notifierKey builds a map key for a notifier from characteristic UUID and central address.
func notifierKey(charUUID string, centralAddr string) string {
	return charUUID + ":" + centralAddr
}

// openGattServer creates the BluetoothGattServer and registers all services.
func (d *device) openGattServer(ctx context.Context) (_err error) {
	logger.Tracef(ctx, "jni.device.openGattServer")
	defer func() { logger.Tracef(ctx, "/jni.device.openGattServer: %v", _err) }()

	d.mu.Lock()
	services := make([]*gatt.Service, len(d.services))
	copy(services, d.services)
	d.mu.Unlock()

	if len(services) == 0 {
		return nil
	}

	gs := &gattServerState{
		charByUUID:    make(map[string]*gatt.Characteristic),
		descByUUID:    make(map[string]*gatt.Descriptor),
		charObjByUUID: make(map[string]*jnipkg.Object),
		centrals:      make(map[string]*central),
		notifiers:     make(map[string]*serverNotifier),
	}

	vm := d.vm

	// Get the BluetoothManager and open GATT server.
	var gattServerObj *jnipkg.Object
	var serverCallbackCleanup func()

	err := vm.Do(func(env *jnipkg.Env) error {
		// Create the server callback proxy.
		cls, err := env.FindClass("android/bluetooth/BluetoothGattServerCallback")
		if err != nil {
			return fmt.Errorf("find BluetoothGattServerCallback: %w", err)
		}
		defer env.DeleteLocalRef(&cls.Object)

		handler := func(env *jnipkg.Env, methodName string, args []*jnipkg.Object) (*jnipkg.Object, error) {
			d.handleGattServerCallback(ctx, env, gs, methodName, args)
			return nil, nil
		}

		proxy, proxyCleanup, err := env.NewProxy([]*jnipkg.Class{cls}, handler)
		if err != nil {
			return fmt.Errorf("create GattServerCallback proxy: %w", err)
		}
		callbackGlobal := env.NewGlobalRef(proxy)
		env.DeleteLocalRef(proxy)
		serverCallbackCleanup = func() {
			_ = vm.Do(func(env *jnipkg.Env) error {
				env.DeleteGlobalRef(callbackGlobal)
				return nil
			})
			proxyCleanup()
		}

		// Get BluetoothManager via system service.
		svc, err := d.appCtx.GetSystemService("bluetooth")
		if err != nil {
			serverCallbackCleanup()
			return fmt.Errorf("get bluetooth service: %w", err)
		}
		if svc == nil {
			serverCallbackCleanup()
			return fmt.Errorf("bluetooth service not available")
		}
		defer env.DeleteGlobalRef(svc)

		// Call BluetoothManager.openGattServer(context, callback).
		bmClass, err := env.FindClass("android/bluetooth/BluetoothManager")
		if err != nil {
			serverCallbackCleanup()
			return fmt.Errorf("find BluetoothManager: %w", err)
		}
		defer env.DeleteLocalRef(&bmClass.Object)

		openGattServerMid, err := env.GetMethodID(bmClass, "openGattServer",
			"(Landroid/content/Context;Landroid/bluetooth/BluetoothGattServerCallback;)Landroid/bluetooth/BluetoothGattServer;")
		if err != nil {
			serverCallbackCleanup()
			return fmt.Errorf("get openGattServer: %w", err)
		}

		serverObj, err := env.CallObjectMethod(svc, openGattServerMid,
			jnipkg.ObjectValue(d.appCtx.Obj), jnipkg.ObjectValue(callbackGlobal))
		if err != nil {
			serverCallbackCleanup()
			return fmt.Errorf("call openGattServer: %w", err)
		}
		if serverObj == nil {
			serverCallbackCleanup()
			return fmt.Errorf("openGattServer returned null")
		}

		gattServerObj = env.NewGlobalRef(serverObj)
		env.DeleteLocalRef(serverObj)
		return nil
	})
	if err != nil {
		return err
	}

	gs.server = &bluetooth.GattServer{VM: vm, Obj: gattServerObj}
	gs.cleanup = serverCallbackCleanup

	// Convert gatt.Services to Java BluetoothGattService objects and add them.
	for _, svc := range services {
		if err := d.addServiceToGattServer(ctx, gs, svc); err != nil {
			gs.server.Close()
			gs.cleanup()
			return fmt.Errorf("adding service %s: %w", svc.UUID(), err)
		}
	}

	d.mu.Lock()
	d.gattServer = gs
	d.mu.Unlock()

	return nil
}

// closeGattServer tears down the GATT server.
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

	// Delete characteristic JNI global refs.
	_ = d.vm.Do(func(env *jnipkg.Env) error {
		for _, obj := range gs.charObjByUUID {
			env.DeleteGlobalRef(obj)
		}
		return nil
	})

	_ = gs.server.ClearServices()
	_ = gs.server.Close()

	if gs.cleanup != nil {
		gs.cleanup()
	}
}

// addServiceToGattServer creates a Java BluetoothGattService from a gatt.Service
// and adds it to the GATT server.
func (d *device) addServiceToGattServer(
	ctx context.Context,
	gs *gattServerState,
	svc *gatt.Service,
) error {
	vm := d.vm

	return vm.Do(func(env *jnipkg.Env) error {
		// Create the Java UUID for the service.
		svcUUID, err := gattUUIDToJavaUUID(env, svc.UUID())
		if err != nil {
			return fmt.Errorf("converting service UUID: %w", err)
		}
		defer env.DeleteLocalRef(svcUUID)

		// Create BluetoothGattService(uuid, serviceType).
		svcCls, err := env.FindClass("android/bluetooth/BluetoothGattService")
		if err != nil {
			return fmt.Errorf("find BluetoothGattService: %w", err)
		}
		defer env.DeleteLocalRef(&svcCls.Object)

		svcCtor, err := env.GetMethodID(svcCls, "<init>", "(Ljava/util/UUID;I)V")
		if err != nil {
			return fmt.Errorf("get BluetoothGattService constructor: %w", err)
		}

		svcObj, err := env.NewObject(svcCls, svcCtor,
			jnipkg.ObjectValue(svcUUID), jnipkg.IntValue(serviceTypePrimary))
		if err != nil {
			return fmt.Errorf("create BluetoothGattService: %w", err)
		}
		defer env.DeleteLocalRef(svcObj)

		javaSvc := &bluetooth.GattService{VM: vm, Obj: env.NewGlobalRef(svcObj)}
		defer func() {
			_ = vm.Do(func(env *jnipkg.Env) error {
				env.DeleteGlobalRef(javaSvc.Obj)
				return nil
			})
		}()

		// Add characteristics.
		for _, c := range svc.Characteristics() {
			charObj, err := d.createJavaCharacteristic(ctx, env, c)
			if err != nil {
				logger.Warnf(ctx, "skipping characteristic %s: %v", c.UUID(), err)
				continue
			}

			ok, err := javaSvc.AddCharacteristic(charObj)
			if err != nil {
				env.DeleteLocalRef(charObj)
				logger.Warnf(ctx, "addCharacteristic %s failed: %v", c.UUID(), err)
				continue
			}
			if !ok {
				env.DeleteLocalRef(charObj)
				logger.Warnf(ctx, "addCharacteristic %s returned false", c.UUID())
				continue
			}

			// Store the global ref for notification dispatch.
			charUUIDStr := formatUUIDForJava(c.UUID())
			gs.charByUUID[charUUIDStr] = c
			gs.charObjByUUID[charUUIDStr] = env.NewGlobalRef(charObj)
			env.DeleteLocalRef(charObj)

			// Index descriptors.
			for _, desc := range c.Descriptors() {
				descUUIDStr := formatUUIDForJava(desc.UUID())
				gs.descByUUID[descUUIDStr] = desc
			}
		}

		// Add the service to the GATT server.
		ok, err := gs.server.AddService(svcObj)
		if err != nil {
			return fmt.Errorf("addService: %w", err)
		}
		if !ok {
			return fmt.Errorf("addService returned false")
		}

		return nil
	})
}

// createJavaCharacteristic creates a BluetoothGattCharacteristic JNI object.
func (d *device) createJavaCharacteristic(
	ctx context.Context,
	env *jnipkg.Env,
	c *gatt.Characteristic,
) (*jnipkg.Object, error) {
	charUUID, err := gattUUIDToJavaUUID(env, c.UUID())
	if err != nil {
		return nil, fmt.Errorf("converting characteristic UUID: %w", err)
	}
	defer env.DeleteLocalRef(charUUID)

	charCls, err := env.FindClass("android/bluetooth/BluetoothGattCharacteristic")
	if err != nil {
		return nil, fmt.Errorf("find BluetoothGattCharacteristic: %w", err)
	}
	defer env.DeleteLocalRef(&charCls.Object)

	// BluetoothGattCharacteristic(UUID uuid, int properties, int permissions)
	charCtor, err := env.GetMethodID(charCls, "<init>", "(Ljava/util/UUID;II)V")
	if err != nil {
		return nil, fmt.Errorf("get BluetoothGattCharacteristic constructor: %w", err)
	}

	props := int32(c.Properties())
	perms := propsToPermissions(c.Properties())

	charObj, err := env.NewObject(charCls, charCtor,
		jnipkg.ObjectValue(charUUID), jnipkg.IntValue(props), jnipkg.IntValue(perms))
	if err != nil {
		return nil, fmt.Errorf("create BluetoothGattCharacteristic: %w", err)
	}

	charW := &bluetooth.GattCharacteristic{VM: d.vm, Obj: env.NewGlobalRef(charObj)}
	defer func() {
		_ = d.vm.Do(func(env *jnipkg.Env) error {
			env.DeleteGlobalRef(charW.Obj)
			return nil
		})
	}()

	// Add descriptors.
	for _, desc := range c.Descriptors() {
		descObj, err := d.createJavaDescriptor(env, desc)
		if err != nil {
			logger.Warnf(ctx, "skipping descriptor %s: %v", desc.UUID(), err)
			continue
		}

		ok, err := charW.AddDescriptor(descObj)
		if err != nil {
			env.DeleteLocalRef(descObj)
			continue
		}
		if !ok {
			env.DeleteLocalRef(descObj)
			continue
		}
		env.DeleteLocalRef(descObj)
	}

	return charObj, nil
}

// createJavaDescriptor creates a BluetoothGattDescriptor JNI object.
func (d *device) createJavaDescriptor(
	env *jnipkg.Env,
	desc *gatt.Descriptor,
) (*jnipkg.Object, error) {
	descUUID, err := gattUUIDToJavaUUID(env, desc.UUID())
	if err != nil {
		return nil, fmt.Errorf("converting descriptor UUID: %w", err)
	}
	defer env.DeleteLocalRef(descUUID)

	descCls, err := env.FindClass("android/bluetooth/BluetoothGattDescriptor")
	if err != nil {
		return nil, fmt.Errorf("find BluetoothGattDescriptor: %w", err)
	}
	defer env.DeleteLocalRef(&descCls.Object)

	// BluetoothGattDescriptor(UUID uuid, int permissions)
	descCtor, err := env.GetMethodID(descCls, "<init>", "(Ljava/util/UUID;I)V")
	if err != nil {
		return nil, fmt.Errorf("get BluetoothGattDescriptor constructor: %w", err)
	}

	// CCCD needs read+write permissions.
	perms := int32(0x01 | 0x10) // PERMISSION_READ | PERMISSION_WRITE

	descObj, err := env.NewObject(descCls, descCtor,
		jnipkg.ObjectValue(descUUID), jnipkg.IntValue(perms))
	if err != nil {
		return nil, fmt.Errorf("create BluetoothGattDescriptor: %w", err)
	}

	return descObj, nil
}

// propsToPermissions converts gatt.Property flags to Android BluetoothGattCharacteristic permissions.
func propsToPermissions(p gatt.Property) int32 {
	var perms int32
	if p&gatt.CharRead != 0 {
		perms |= 0x01 // PERMISSION_READ
	}
	if p&(gatt.CharWrite|gatt.CharWriteNR) != 0 {
		perms |= 0x10 // PERMISSION_WRITE
	}
	return perms
}

// handleGattServerCallback dispatches GattServerCallback events.
func (d *device) handleGattServerCallback(
	ctx context.Context,
	env *jnipkg.Env,
	gs *gattServerState,
	methodName string,
	args []*jnipkg.Object,
) {
	switch methodName {
	case "onConnectionStateChange":
		d.handleServerConnectionStateChange(ctx, env, gs, args)
	case "onCharacteristicReadRequest":
		d.handleCharacteristicReadRequest(ctx, env, gs, args)
	case "onCharacteristicWriteRequest":
		d.handleCharacteristicWriteRequest(ctx, env, gs, args)
	case "onDescriptorReadRequest":
		d.handleDescriptorReadRequest(ctx, env, gs, args)
	case "onDescriptorWriteRequest":
		d.handleDescriptorWriteRequest(ctx, env, gs, args)
	case "onNotificationSent":
		// args: device, status — acknowledgement of notification delivery.
		logger.Tracef(ctx, "onNotificationSent")
	case "onMtuChanged":
		d.handleServerMtuChanged(ctx, env, gs, args)
	}
}

// handleServerConnectionStateChange processes onConnectionStateChange for the GATT server.
// args: device, status, newState
func (d *device) handleServerConnectionStateChange(
	ctx context.Context,
	env *jnipkg.Env,
	gs *gattServerState,
	args []*jnipkg.Object,
) {
	if len(args) < 3 {
		return
	}

	newState := unboxInt(env, args[2])
	if args[0] == nil {
		return
	}

	btDev := &bluetooth.Device{VM: d.vm, Obj: env.NewGlobalRef(args[0])}
	addr, err := btDev.GetAddress()
	if err != nil {
		logger.Warnf(ctx, "onConnectionStateChange: getAddress failed: %v", err)
		return
	}

	switch newState {
	case stateConnected:
		c := newCentral(d, btDev, addr)
		gs.mu.Lock()
		gs.centrals[addr] = c
		gs.mu.Unlock()

		handler := d.CentralConnected()
		if handler != nil {
			go handler(ctx, c)
		}

	case stateDisconnected:
		gs.mu.Lock()
		c, exists := gs.centrals[addr]
		delete(gs.centrals, addr)

		// Stop any notifiers for this central.
		for key, n := range gs.notifiers {
			if n.c.addr == addr {
				n.stop()
				delete(gs.notifiers, key)
			}
		}
		gs.mu.Unlock()

		// Release the btDev global ref we just created (we used it only to get the address).
		_ = d.vm.Do(func(env *jnipkg.Env) error {
			env.DeleteGlobalRef(btDev.Obj)
			return nil
		})

		if exists {
			handler := d.CentralDisconnected()
			if handler != nil {
				go handler(ctx, c)
			}
		}

	default:
		// Transitional state — release the global ref.
		_ = d.vm.Do(func(env *jnipkg.Env) error {
			env.DeleteGlobalRef(btDev.Obj)
			return nil
		})
	}
}

// handleCharacteristicReadRequest processes onCharacteristicReadRequest.
// args: device, requestId, offset, characteristic
func (d *device) handleCharacteristicReadRequest(
	ctx context.Context,
	env *jnipkg.Env,
	gs *gattServerState,
	args []*jnipkg.Object,
) {
	if len(args) < 4 {
		return
	}

	requestID := unboxInt(env, args[1])
	offset := unboxInt(env, args[2])

	charUUIDStr := getObjectUUIDString(env, d.vm, args[3])
	if charUUIDStr == "" {
		d.sendGattResponse(ctx, gs, args[0], requestID, gattServerSuccess, offset, nil)
		return
	}

	c := gs.charByUUID[charUUIDStr]
	if c == nil {
		d.sendGattResponse(ctx, gs, args[0], requestID, gattServerSuccess, offset, nil)
		return
	}

	cent := d.findCentral(env, gs, args[0])

	rh := c.GetReadHandler()
	if rh != nil {
		rw := gatt.NewResponseWriter(cent.MTU() - 1)
		req := &gatt.ReadRequest{
			Request: gatt.Request{Central: cent},
			Cap:     cent.MTU() - 1,
			Offset:  int(offset),
		}
		rh.ServeRead(ctx, rw, req)
		data := rw.Bytes()
		if int(offset) < len(data) {
			data = data[offset:]
		} else {
			data = nil
		}
		d.sendGattResponse(ctx, gs, args[0], requestID, gattServerSuccess, offset, data)
		return
	}

	// No handler — return empty success.
	d.sendGattResponse(ctx, gs, args[0], requestID, gattServerSuccess, offset, nil)
}

// handleCharacteristicWriteRequest processes onCharacteristicWriteRequest.
// args: device, requestId, characteristic, preparedWrite, responseNeeded, offset, value
func (d *device) handleCharacteristicWriteRequest(
	ctx context.Context,
	env *jnipkg.Env,
	gs *gattServerState,
	args []*jnipkg.Object,
) {
	if len(args) < 7 {
		return
	}

	requestID := unboxInt(env, args[1])
	responseNeeded := unboxInt(env, args[4]) != 0
	offset := unboxInt(env, args[5])
	value := byteArrayToGoBytes(env, args[6])

	charUUIDStr := getObjectUUIDString(env, d.vm, args[2])
	if charUUIDStr == "" {
		if responseNeeded {
			d.sendGattResponse(ctx, gs, args[0], requestID, gattServerSuccess, offset, nil)
		}
		return
	}

	c := gs.charByUUID[charUUIDStr]
	if c == nil {
		if responseNeeded {
			d.sendGattResponse(ctx, gs, args[0], requestID, gattServerSuccess, offset, nil)
		}
		return
	}

	cent := d.findCentral(env, gs, args[0])

	wh := c.GetWriteHandler()
	if wh != nil {
		status := wh.ServeWrite(ctx, gatt.Request{Central: cent}, value)
		if responseNeeded {
			d.sendGattResponse(ctx, gs, args[0], requestID, int32(status), offset, nil)
		}
		return
	}

	if responseNeeded {
		d.sendGattResponse(ctx, gs, args[0], requestID, gattServerSuccess, offset, nil)
	}
}

// handleDescriptorReadRequest processes onDescriptorReadRequest.
// args: device, requestId, offset, descriptor
func (d *device) handleDescriptorReadRequest(
	ctx context.Context,
	env *jnipkg.Env,
	gs *gattServerState,
	args []*jnipkg.Object,
) {
	if len(args) < 4 {
		return
	}

	requestID := unboxInt(env, args[1])
	offset := unboxInt(env, args[2])

	descUUIDStr := getObjectUUIDString(env, d.vm, args[3])

	// For CCCD, return the current subscription state.
	if descUUIDStr == cccdUUID {
		// Return 0x0000 (notifications disabled) as default.
		// The actual subscription state is tracked per-central in notifiers.
		cent := d.findCentral(env, gs, args[0])
		charUUID := getCharUUIDFromDescriptor(env, d.vm, args[3])

		var val []byte
		gs.mu.Lock()
		key := notifierKey(charUUID, cent.addr)
		if n := gs.notifiers[key]; n != nil && !n.Done() {
			if n.confirm {
				val = []byte{0x02, 0x00} // Indications enabled
			} else {
				val = []byte{0x01, 0x00} // Notifications enabled
			}
		} else {
			val = []byte{0x00, 0x00}
		}
		gs.mu.Unlock()

		d.sendGattResponse(ctx, gs, args[0], requestID, gattServerSuccess, offset, val)
		return
	}

	// For other descriptors, return empty data.
	d.sendGattResponse(ctx, gs, args[0], requestID, gattServerSuccess, offset, nil)
}

// handleDescriptorWriteRequest processes onDescriptorWriteRequest.
// args: device, requestId, descriptor, preparedWrite, responseNeeded, offset, value
func (d *device) handleDescriptorWriteRequest(
	ctx context.Context,
	env *jnipkg.Env,
	gs *gattServerState,
	args []*jnipkg.Object,
) {
	if len(args) < 7 {
		return
	}

	requestID := unboxInt(env, args[1])
	responseNeeded := unboxInt(env, args[4]) != 0
	value := byteArrayToGoBytes(env, args[6])

	descUUIDStr := getObjectUUIDString(env, d.vm, args[2])

	// Handle CCCD writes to enable/disable notifications.
	if descUUIDStr == cccdUUID {
		charUUID := getCharUUIDFromDescriptor(env, d.vm, args[2])
		cent := d.findCentral(env, gs, args[0])

		d.handleCCCDWrite(ctx, gs, cent, charUUID, value)

		if responseNeeded {
			d.sendGattResponse(ctx, gs, args[0], requestID, gattServerSuccess, 0, nil)
		}
		return
	}

	if responseNeeded {
		d.sendGattResponse(ctx, gs, args[0], requestID, gattServerSuccess, 0, nil)
	}
}

// handleCCCDWrite processes a write to the Client Characteristic Configuration Descriptor.
func (d *device) handleCCCDWrite(
	ctx context.Context,
	gs *gattServerState,
	cent *central,
	charUUID string,
	value []byte,
) {
	c := gs.charByUUID[charUUID]
	if c == nil {
		return
	}

	// Decode the CCCD value.
	var cccdVal uint16
	if len(value) >= 2 {
		cccdVal = uint16(value[0]) | uint16(value[1])<<8
	}

	key := notifierKey(charUUID, cent.addr)

	gs.mu.Lock()
	existing := gs.notifiers[key]
	gs.mu.Unlock()

	switch {
	case cccdVal == 0:
		// Disable notifications/indications.
		if existing != nil {
			existing.stop()
			gs.mu.Lock()
			delete(gs.notifiers, key)
			gs.mu.Unlock()
		}

	default:
		// Enable notifications or indications.
		if existing != nil {
			existing.stop()
		}

		charObj := gs.charObjByUUID[charUUID]
		confirm := cccdVal&0x02 != 0 // Indication flag

		n := &serverNotifier{
			gs:      gs,
			d:       d,
			c:       cent,
			char:    c,
			charObj: charObj,
			confirm: confirm,
		}

		gs.mu.Lock()
		gs.notifiers[key] = n
		gs.mu.Unlock()

		// Call the characteristic's NotifyHandler.
		nh := c.GetNotifyHandler()
		if nh != nil {
			go nh.ServeNotify(ctx, gatt.Request{Central: cent}, n)
		}
	}
}

// handleServerMtuChanged processes onMtuChanged.
// args: device, mtu
func (d *device) handleServerMtuChanged(
	ctx context.Context,
	env *jnipkg.Env,
	gs *gattServerState,
	args []*jnipkg.Object,
) {
	if len(args) < 2 {
		return
	}

	mtu := unboxInt(env, args[1])
	if args[0] == nil {
		return
	}

	addr := getDeviceAddress(env, d.vm, args[0])

	gs.mu.Lock()
	c := gs.centrals[addr]
	gs.mu.Unlock()

	if c != nil {
		c.setMTU(int(mtu))
		logger.Debugf(ctx, "GATT server: MTU changed to %d for %s", mtu, addr)
	}
}

// sendGattResponse sends a response to a GATT read/write request.
func (d *device) sendGattResponse(
	ctx context.Context,
	gs *gattServerState,
	deviceObj *jnipkg.Object,
	requestID int32,
	status int32,
	offset int32,
	value []byte,
) {
	vm := d.vm

	_ = vm.Do(func(env *jnipkg.Env) error {
		var byteArrObj *jnipkg.Object
		if len(value) > 0 {
			byteArr := env.NewByteArray(int32(len(value)))
			if byteArr != nil {
				env.SetByteArrayRegion(byteArr, 0, int32(len(value)), unsafe.Pointer(&value[0]))
				byteArrObj = &byteArr.Object
				defer env.DeleteLocalRef(byteArrObj)
			}
		}

		_, err := gs.server.SendResponse(deviceObj, requestID, status, offset, byteArrObj)
		if err != nil {
			logger.Warnf(ctx, "sendResponse failed: %v", err)
		}
		return nil
	})
}

// findCentral looks up a connected central by its BluetoothDevice object.
func (d *device) findCentral(
	env *jnipkg.Env,
	gs *gattServerState,
	deviceObj *jnipkg.Object,
) *central {
	addr := getDeviceAddress(env, d.vm, deviceObj)

	gs.mu.Lock()
	defer gs.mu.Unlock()

	c := gs.centrals[addr]
	if c == nil {
		// Create a temporary central for the request.
		btDev := &bluetooth.Device{VM: d.vm, Obj: env.NewGlobalRef(deviceObj)}
		c = newCentral(d, btDev, addr)
		gs.centrals[addr] = c
	}
	return c
}

// getDeviceAddress extracts the MAC address string from a BluetoothDevice object.
func getDeviceAddress(
	env *jnipkg.Env,
	vm *jnipkg.VM,
	deviceObj *jnipkg.Object,
) string {
	if deviceObj == nil {
		return ""
	}

	btDev := &bluetooth.Device{VM: vm, Obj: env.NewGlobalRef(deviceObj)}
	defer func() {
		_ = vm.Do(func(env *jnipkg.Env) error {
			env.DeleteGlobalRef(btDev.Obj)
			return nil
		})
	}()

	addr, err := btDev.GetAddress()
	if err != nil {
		return ""
	}
	return addr
}

// getObjectUUIDString calls getUuid().toString() on a BluetoothGattCharacteristic
// or BluetoothGattDescriptor JNI object and returns the lowercase UUID string.
func getObjectUUIDString(
	env *jnipkg.Env,
	_ *jnipkg.VM,
	obj *jnipkg.Object,
) string {
	if obj == nil {
		return ""
	}

	// Both BluetoothGattCharacteristic and BluetoothGattDescriptor
	// have getUuid() returning java.util.UUID.
	objCls := env.GetObjectClass(obj)
	if objCls == nil {
		return ""
	}
	defer env.DeleteLocalRef(&objCls.Object)

	getUuidMid, err := env.GetMethodID(objCls, "getUuid", "()Ljava/util/UUID;")
	if err != nil {
		return ""
	}

	uuidObj, err := env.CallObjectMethod(obj, getUuidMid)
	if err != nil || uuidObj == nil {
		return ""
	}
	defer env.DeleteLocalRef(uuidObj)

	s, _ := callToString(env, uuidObj)
	return s
}

// getCharUUIDFromDescriptor extracts the parent characteristic's UUID string
// from a BluetoothGattDescriptor by calling getCharacteristic().getUuid().toString().
func getCharUUIDFromDescriptor(
	env *jnipkg.Env,
	vm *jnipkg.VM,
	descObj *jnipkg.Object,
) string {
	if descObj == nil {
		return ""
	}

	descCls := env.GetObjectClass(descObj)
	if descCls == nil {
		return ""
	}
	defer env.DeleteLocalRef(&descCls.Object)

	getCharMid, err := env.GetMethodID(descCls, "getCharacteristic",
		"()Landroid/bluetooth/BluetoothGattCharacteristic;")
	if err != nil {
		return ""
	}

	charObj, err := env.CallObjectMethod(descObj, getCharMid)
	if err != nil || charObj == nil {
		return ""
	}
	defer env.DeleteLocalRef(charObj)

	return getObjectUUIDString(env, vm, charObj)
}
