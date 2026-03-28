package jni

import (
	"context"
	"encoding/binary"
	"fmt"
	"unsafe"

	"github.com/facebookincubator/go-belt/tool/logger"

	jnipkg "github.com/AndroidGoLab/jni"
	"github.com/AndroidGoLab/jni/bluetooth/le"
	"github.com/xaionaro-go/gatt"
)

// Android AdvertiseSettings mode constants.
const (
	advertiseModeBalanced  int32 = 1
	txPowerLevelMedium     int32 = 2
)

// advertiseState holds the JNI resources for an active advertising session.
type advertiseState struct {
	advertiser  *le.BluetoothLeAdvertiser
	callbackObj *jnipkg.Object
	cleanup     func()
}

func (d *device) Advertise(
	ctx context.Context,
	a *gatt.AdvPacket,
) (_err error) {
	logger.Tracef(ctx, "jni.device.Advertise")
	defer func() { logger.Tracef(ctx, "/jni.device.Advertise: %v", _err) }()

	if d.adapter == nil {
		return fmt.Errorf("adapter not initialized; call Start first")
	}

	d.mu.Lock()
	if d.advState != nil {
		d.mu.Unlock()
		return fmt.Errorf("already advertising")
	}
	d.mu.Unlock()

	// For the raw AdvPacket path, advertise with connectable=true and no specific service UUIDs.
	return d.startAdvertising(ctx, true, nil, nil)
}

func (d *device) AdvertiseNameAndServices(
	ctx context.Context,
	name string,
	ss []gatt.UUID,
) (_err error) {
	logger.Tracef(ctx, "jni.device.AdvertiseNameAndServices")
	defer func() { logger.Tracef(ctx, "/jni.device.AdvertiseNameAndServices: %v", _err) }()

	if d.adapter == nil {
		return fmt.Errorf("adapter not initialized; call Start first")
	}

	d.mu.Lock()
	if d.advState != nil {
		d.mu.Unlock()
		return fmt.Errorf("already advertising")
	}
	d.mu.Unlock()

	return d.startAdvertising(ctx, true, ss, nil)
}

func (d *device) AdvertiseIBeaconData(
	ctx context.Context,
	b []byte,
) (_err error) {
	logger.Tracef(ctx, "jni.device.AdvertiseIBeaconData")
	defer func() { logger.Tracef(ctx, "/jni.device.AdvertiseIBeaconData: %v", _err) }()

	if d.adapter == nil {
		return fmt.Errorf("adapter not initialized; call Start first")
	}

	d.mu.Lock()
	if d.advState != nil {
		d.mu.Unlock()
		return fmt.Errorf("already advertising")
	}
	d.mu.Unlock()

	// Apple company ID 0x004C with the provided iBeacon data.
	mfr := manufacturerData{companyID: 0x004C, data: b}
	return d.startAdvertising(ctx, false, nil, &mfr)
}

func (d *device) AdvertiseNameAndIBeaconData(
	ctx context.Context,
	name string,
	b []byte,
) (_err error) {
	logger.Tracef(ctx, "jni.device.AdvertiseNameAndIBeaconData")
	defer func() { logger.Tracef(ctx, "/jni.device.AdvertiseNameAndIBeaconData: %v", _err) }()

	if d.adapter == nil {
		return fmt.Errorf("adapter not initialized; call Start first")
	}

	d.mu.Lock()
	if d.advState != nil {
		d.mu.Unlock()
		return fmt.Errorf("already advertising")
	}
	d.mu.Unlock()

	mfr := manufacturerData{companyID: 0x004C, data: b}
	return d.startAdvertising(ctx, false, nil, &mfr)
}

func (d *device) AdvertiseIBeacon(
	ctx context.Context,
	u gatt.UUID,
	major, minor uint16,
	pwr int8,
) (_err error) {
	logger.Tracef(ctx, "jni.device.AdvertiseIBeacon")
	defer func() { logger.Tracef(ctx, "/jni.device.AdvertiseIBeacon: %v", _err) }()

	// Build the iBeacon manufacturer-specific data payload.
	b := make([]byte, 23)
	b[0] = 0x02 // iBeacon sub-type
	b[1] = 0x15 // iBeacon data length: 21 bytes
	// UUID in big-endian (reverse the little-endian gatt.UUID bytes).
	uuidBytes := u.Bytes()
	for i, j := 0, len(uuidBytes)-1; i < j; i, j = i+1, j-1 {
		uuidBytes[i], uuidBytes[j] = uuidBytes[j], uuidBytes[i]
	}
	copy(b[2:], uuidBytes)
	binary.BigEndian.PutUint16(b[18:], major)
	binary.BigEndian.PutUint16(b[20:], minor)
	b[22] = uint8(pwr)

	return d.AdvertiseIBeaconData(ctx, b)
}

func (d *device) StopAdvertising(
	ctx context.Context,
) (_err error) {
	logger.Tracef(ctx, "jni.device.StopAdvertising")
	defer func() { logger.Tracef(ctx, "/jni.device.StopAdvertising: %v", _err) }()

	d.mu.Lock()
	as := d.advState
	d.advState = nil
	d.mu.Unlock()

	if as == nil {
		return nil
	}

	if err := as.advertiser.StopAdvertising(as.callbackObj); err != nil {
		_err = fmt.Errorf("stopAdvertising: %w", err)
	}

	_ = d.vm.Do(func(env *jnipkg.Env) error {
		env.DeleteGlobalRef(as.callbackObj)
		return nil
	})

	if as.cleanup != nil {
		as.cleanup()
	}

	return _err
}

// manufacturerData holds a manufacturer-specific data payload for advertising.
type manufacturerData struct {
	companyID int32
	data      []byte
}

// startAdvertising performs the common advertising setup:
// 1. Obtain BluetoothLeAdvertiser from adapter
// 2. Build AdvertiseSettings via raw JNI
// 3. Build AdvertiseData via raw JNI
// 4. Create AdvertiseCallback proxy
// 5. Call startAdvertising
func (d *device) startAdvertising(
	ctx context.Context,
	connectable bool,
	serviceUUIDs []gatt.UUID,
	mfr *manufacturerData,
) error {
	advObj, err := d.adapter.GetBluetoothLeAdvertiser()
	if err != nil {
		return fmt.Errorf("getBluetoothLeAdvertiser: %w", err)
	}
	if advObj == nil {
		return fmt.Errorf("BluetoothLeAdvertiser is null (Bluetooth may be disabled)")
	}

	advertiser := &le.BluetoothLeAdvertiser{
		VM:  d.vm,
		Obj: advObj,
	}

	var settingsObj *jnipkg.Object
	var dataObj *jnipkg.Object
	var callbackObj *jnipkg.Object
	var callbackCleanup func()

	err = d.vm.Do(func(env *jnipkg.Env) error {
		var err error

		settingsObj, err = buildAdvertiseSettings(env, connectable)
		if err != nil {
			return fmt.Errorf("building advertise settings: %w", err)
		}

		dataObj, err = buildAdvertiseData(env, serviceUUIDs, mfr, true)
		if err != nil {
			return fmt.Errorf("building advertise data: %w", err)
		}

		// Create AdvertiseCallback proxy.
		cls, err := env.FindClass("android/bluetooth/le/AdvertiseCallback")
		if err != nil {
			return fmt.Errorf("find AdvertiseCallback class: %w", err)
		}
		defer env.DeleteLocalRef(&cls.Object)

		advStarted := make(chan error, 1)
		handler := func(env *jnipkg.Env, methodName string, args []*jnipkg.Object) (*jnipkg.Object, error) {
			switch methodName {
			case "onStartSuccess":
				logger.Debugf(ctx, "advertising started successfully")
				select {
				case advStarted <- nil:
				default:
				}
			case "onStartFailure":
				var errCode int32
				if len(args) >= 1 {
					errCode = unboxInt(env, args[0])
				}
				logger.Warnf(ctx, "advertising start failed: errorCode=%d", errCode)
				select {
				case advStarted <- fmt.Errorf("advertising start failed: errorCode=%d", errCode):
				default:
				}
			}
			return nil, nil
		}

		proxy, proxyCleanup, err := env.NewProxy([]*jnipkg.Class{cls}, handler)
		if err != nil {
			return fmt.Errorf("create AdvertiseCallback proxy: %w", err)
		}

		callbackObj = env.NewGlobalRef(proxy)
		env.DeleteLocalRef(proxy)
		callbackCleanup = proxyCleanup
		return nil
	})
	if err != nil {
		return err
	}

	if err := advertiser.StartAdvertising3(settingsObj, dataObj, callbackObj); err != nil {
		_ = d.vm.Do(func(env *jnipkg.Env) error {
			env.DeleteGlobalRef(callbackObj)
			return nil
		})
		callbackCleanup()
		return fmt.Errorf("startAdvertising: %w", err)
	}

	d.mu.Lock()
	d.advState = &advertiseState{
		advertiser:  advertiser,
		callbackObj: callbackObj,
		cleanup:     callbackCleanup,
	}
	hasServices := len(d.services) > 0
	d.mu.Unlock()

	// Open the GATT server if services are registered.
	if hasServices {
		if err := d.openGattServer(ctx); err != nil {
			logger.Warnf(ctx, "failed to open GATT server: %v", err)
			// Advertising is still active, just no GATT server.
		}
	}

	return nil
}

// buildAdvertiseSettings creates an AdvertiseSettings object via JNI builders.
func buildAdvertiseSettings(
	env *jnipkg.Env,
	connectable bool,
) (*jnipkg.Object, error) {
	builderCls, err := env.FindClass("android/bluetooth/le/AdvertiseSettings$Builder")
	if err != nil {
		return nil, fmt.Errorf("find AdvertiseSettings$Builder: %w", err)
	}
	defer env.DeleteLocalRef(&builderCls.Object)

	ctor, err := env.GetMethodID(builderCls, "<init>", "()V")
	if err != nil {
		return nil, fmt.Errorf("get AdvertiseSettings$Builder constructor: %w", err)
	}

	builder, err := env.NewObject(builderCls, ctor)
	if err != nil {
		return nil, fmt.Errorf("create AdvertiseSettings$Builder: %w", err)
	}
	defer env.DeleteLocalRef(builder)

	setMode, err := env.GetMethodID(builderCls, "setAdvertiseMode",
		"(I)Landroid/bluetooth/le/AdvertiseSettings$Builder;")
	if err != nil {
		return nil, fmt.Errorf("get setAdvertiseMode: %w", err)
	}
	if _, err := env.CallObjectMethod(builder, setMode, jnipkg.IntValue(advertiseModeBalanced)); err != nil {
		return nil, fmt.Errorf("call setAdvertiseMode: %w", err)
	}

	setConnectable, err := env.GetMethodID(builderCls, "setConnectable",
		"(Z)Landroid/bluetooth/le/AdvertiseSettings$Builder;")
	if err != nil {
		return nil, fmt.Errorf("get setConnectable: %w", err)
	}
	var boolVal uint8
	if connectable {
		boolVal = 1
	}
	if _, err := env.CallObjectMethod(builder, setConnectable, jnipkg.BooleanValue(boolVal)); err != nil {
		return nil, fmt.Errorf("call setConnectable: %w", err)
	}

	setTxPower, err := env.GetMethodID(builderCls, "setTxPowerLevel",
		"(I)Landroid/bluetooth/le/AdvertiseSettings$Builder;")
	if err != nil {
		return nil, fmt.Errorf("get setTxPowerLevel: %w", err)
	}
	if _, err := env.CallObjectMethod(builder, setTxPower, jnipkg.IntValue(txPowerLevelMedium)); err != nil {
		return nil, fmt.Errorf("call setTxPowerLevel: %w", err)
	}

	build, err := env.GetMethodID(builderCls, "build",
		"()Landroid/bluetooth/le/AdvertiseSettings;")
	if err != nil {
		return nil, fmt.Errorf("get build: %w", err)
	}
	settings, err := env.CallObjectMethod(builder, build)
	if err != nil {
		return nil, fmt.Errorf("call build: %w", err)
	}

	return settings, nil
}

// buildAdvertiseData creates an AdvertiseData object via JNI builders.
func buildAdvertiseData(
	env *jnipkg.Env,
	serviceUUIDs []gatt.UUID,
	mfr *manufacturerData,
	includeDeviceName bool,
) (*jnipkg.Object, error) {
	builderCls, err := env.FindClass("android/bluetooth/le/AdvertiseData$Builder")
	if err != nil {
		return nil, fmt.Errorf("find AdvertiseData$Builder: %w", err)
	}
	defer env.DeleteLocalRef(&builderCls.Object)

	ctor, err := env.GetMethodID(builderCls, "<init>", "()V")
	if err != nil {
		return nil, fmt.Errorf("get AdvertiseData$Builder constructor: %w", err)
	}

	builder, err := env.NewObject(builderCls, ctor)
	if err != nil {
		return nil, fmt.Errorf("create AdvertiseData$Builder: %w", err)
	}
	defer env.DeleteLocalRef(builder)

	if includeDeviceName {
		setIncludeName, err := env.GetMethodID(builderCls, "setIncludeDeviceName",
			"(Z)Landroid/bluetooth/le/AdvertiseData$Builder;")
		if err != nil {
			return nil, fmt.Errorf("get setIncludeDeviceName: %w", err)
		}
		if _, err := env.CallObjectMethod(builder, setIncludeName, jnipkg.BooleanValue(1)); err != nil {
			return nil, fmt.Errorf("call setIncludeDeviceName: %w", err)
		}
	}

	// Add service UUIDs.
	if len(serviceUUIDs) > 0 {
		addUUID, err := env.GetMethodID(builderCls, "addServiceUuid",
			"(Landroid/os/ParcelUuid;)Landroid/bluetooth/le/AdvertiseData$Builder;")
		if err != nil {
			return nil, fmt.Errorf("get addServiceUuid: %w", err)
		}

		for _, u := range serviceUUIDs {
			parcelUuid, err := gattUUIDToParcelUuid(env, u)
			if err != nil {
				return nil, fmt.Errorf("converting UUID %s to ParcelUuid: %w", u, err)
			}

			if _, err := env.CallObjectMethod(builder, addUUID, jnipkg.ObjectValue(parcelUuid)); err != nil {
				env.DeleteLocalRef(parcelUuid)
				return nil, fmt.Errorf("call addServiceUuid: %w", err)
			}
			env.DeleteLocalRef(parcelUuid)
		}
	}

	// Add manufacturer data if present.
	if mfr != nil {
		addMfr, err := env.GetMethodID(builderCls, "addManufacturerData",
			"(I[B)Landroid/bluetooth/le/AdvertiseData$Builder;")
		if err != nil {
			return nil, fmt.Errorf("get addManufacturerData: %w", err)
		}

		byteArr := env.NewByteArray(int32(len(mfr.data)))
		if byteArr == nil {
			return nil, fmt.Errorf("failed to allocate byte array for manufacturer data")
		}
		defer env.DeleteLocalRef(&byteArr.Object)

		if len(mfr.data) > 0 {
			env.SetByteArrayRegion(byteArr, 0, int32(len(mfr.data)), unsafe.Pointer(&mfr.data[0]))
		}

		if _, err := env.CallObjectMethod(builder, addMfr,
			jnipkg.IntValue(mfr.companyID), jnipkg.ObjectValue(&byteArr.Object)); err != nil {
			return nil, fmt.Errorf("call addManufacturerData: %w", err)
		}
	}

	build, err := env.GetMethodID(builderCls, "build",
		"()Landroid/bluetooth/le/AdvertiseData;")
	if err != nil {
		return nil, fmt.Errorf("get build: %w", err)
	}
	data, err := env.CallObjectMethod(builder, build)
	if err != nil {
		return nil, fmt.Errorf("call build: %w", err)
	}

	return data, nil
}

// gattUUIDToParcelUuid converts a gatt.UUID to an android.os.ParcelUuid JNI object.
func gattUUIDToParcelUuid(env *jnipkg.Env, u gatt.UUID) (*jnipkg.Object, error) {
	javaUUID, err := gattUUIDToJavaUUID(env, u)
	if err != nil {
		return nil, err
	}
	defer env.DeleteLocalRef(javaUUID)

	parcelCls, err := env.FindClass("android/os/ParcelUuid")
	if err != nil {
		return nil, fmt.Errorf("find ParcelUuid: %w", err)
	}
	defer env.DeleteLocalRef(&parcelCls.Object)

	ctor, err := env.GetMethodID(parcelCls, "<init>", "(Ljava/util/UUID;)V")
	if err != nil {
		return nil, fmt.Errorf("get ParcelUuid constructor: %w", err)
	}

	parcel, err := env.NewObject(parcelCls, ctor, jnipkg.ObjectValue(javaUUID))
	if err != nil {
		return nil, fmt.Errorf("create ParcelUuid: %w", err)
	}

	return parcel, nil
}

// gattUUIDToJavaUUID converts a gatt.UUID to a java.util.UUID JNI object.
func gattUUIDToJavaUUID(env *jnipkg.Env, u gatt.UUID) (*jnipkg.Object, error) {
	uuidStr := formatUUIDForJava(u)

	uuidCls, err := env.FindClass("java/util/UUID")
	if err != nil {
		return nil, fmt.Errorf("find UUID class: %w", err)
	}
	defer env.DeleteLocalRef(&uuidCls.Object)

	fromString, err := env.GetStaticMethodID(uuidCls, "fromString",
		"(Ljava/lang/String;)Ljava/util/UUID;")
	if err != nil {
		return nil, fmt.Errorf("get UUID.fromString: %w", err)
	}

	jStr, err := env.NewStringUTF(uuidStr)
	if err != nil {
		return nil, fmt.Errorf("create UUID string: %w", err)
	}
	defer env.DeleteLocalRef(&jStr.Object)

	uuidObj, err := env.CallStaticObjectMethod(uuidCls, fromString,
		jnipkg.ObjectValue(&jStr.Object))
	if err != nil {
		return nil, fmt.Errorf("call UUID.fromString(%q): %w", uuidStr, err)
	}

	return uuidObj, nil
}

// formatUUIDForJava formats a gatt.UUID into the standard
// "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx" form that java.util.UUID.fromString expects.
// For 2-byte (16-bit) UUIDs, expands to the standard Bluetooth base UUID.
func formatUUIDForJava(u gatt.UUID) string {
	switch u.Len() {
	case 2:
		// Expand 16-bit UUID to full 128-bit Bluetooth base UUID:
		// 0000XXXX-0000-1000-8000-00805F9B34FB
		b := u.Bytes()
		// gatt.UUID stores 16-bit UUIDs as 2 bytes in little-endian.
		return fmt.Sprintf("0000%02x%02x-0000-1000-8000-00805f9b34fb", b[1], b[0])
	case 16:
		// gatt.UUID stores 128-bit UUIDs in little-endian byte order.
		// Java expects big-endian dashed form.
		b := u.Bytes()
		// Reverse to big-endian.
		be := make([]byte, 16)
		for i := 0; i < 16; i++ {
			be[i] = b[15-i]
		}
		return fmt.Sprintf("%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
			be[0], be[1], be[2], be[3],
			be[4], be[5],
			be[6], be[7],
			be[8], be[9],
			be[10], be[11], be[12], be[13], be[14], be[15])
	default:
		return u.String()
	}
}
