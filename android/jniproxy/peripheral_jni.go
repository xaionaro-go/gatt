package jniproxy

import (
	"context"
	"fmt"

	jniraw "github.com/AndroidGoLab/jni-proxy/proto/jni_raw"
)

// Raw JNI helper methods for peripheral GATT operations.
// These are needed because the typed gRPC clients don't include object handles
// in their request messages, making them unsuitable for multi-device scenarios.

// callGattBooleanMethod calls a boolean-returning method on a BluetoothGatt handle.
func (p *peripheral) callGattBooleanMethod(ctx context.Context, gattHandle int64, methodName, sig string) (bool, error) {
	raw := &p.d.raw
	gattCls, err := raw.findClass(ctx, "android/bluetooth/BluetoothGatt")
	if err != nil {
		return false, err
	}
	method, err := raw.getMethodID(ctx, gattCls, methodName, sig)
	if err != nil {
		return false, err
	}
	return raw.callBooleanMethod(ctx, gattHandle, method)
}

// callGattReadCharacteristic calls gatt.readCharacteristic(characteristic).
func (p *peripheral) callGattReadCharacteristic(ctx context.Context, gattHandle, charHandle int64) (bool, error) {
	raw := &p.d.raw
	gattCls, err := raw.findClass(ctx, "android/bluetooth/BluetoothGatt")
	if err != nil {
		return false, err
	}
	method, err := raw.getMethodID(ctx, gattCls, "readCharacteristic",
		"(Landroid/bluetooth/BluetoothGattCharacteristic;)Z")
	if err != nil {
		return false, err
	}
	return raw.callBooleanMethod(ctx, gattHandle, method, jvalObj(charHandle))
}

// callGattWriteCharacteristic calls gatt.writeCharacteristic(characteristic).
func (p *peripheral) callGattWriteCharacteristic(ctx context.Context, gattHandle, charHandle int64) (bool, error) {
	raw := &p.d.raw
	gattCls, err := raw.findClass(ctx, "android/bluetooth/BluetoothGatt")
	if err != nil {
		return false, err
	}
	method, err := raw.getMethodID(ctx, gattCls, "writeCharacteristic",
		"(Landroid/bluetooth/BluetoothGattCharacteristic;)Z")
	if err != nil {
		return false, err
	}
	return raw.callBooleanMethod(ctx, gattHandle, method, jvalObj(charHandle))
}

// callGattReadDescriptor calls gatt.readDescriptor(descriptor).
func (p *peripheral) callGattReadDescriptor(ctx context.Context, gattHandle, descHandle int64) (bool, error) {
	raw := &p.d.raw
	gattCls, err := raw.findClass(ctx, "android/bluetooth/BluetoothGatt")
	if err != nil {
		return false, err
	}
	method, err := raw.getMethodID(ctx, gattCls, "readDescriptor",
		"(Landroid/bluetooth/BluetoothGattDescriptor;)Z")
	if err != nil {
		return false, err
	}
	return raw.callBooleanMethod(ctx, gattHandle, method, jvalObj(descHandle))
}

// callGattWriteDescriptor calls gatt.writeDescriptor(descriptor).
func (p *peripheral) callGattWriteDescriptor(ctx context.Context, gattHandle, descHandle int64) (bool, error) {
	raw := &p.d.raw
	gattCls, err := raw.findClass(ctx, "android/bluetooth/BluetoothGatt")
	if err != nil {
		return false, err
	}
	method, err := raw.getMethodID(ctx, gattCls, "writeDescriptor",
		"(Landroid/bluetooth/BluetoothGattDescriptor;)Z")
	if err != nil {
		return false, err
	}
	return raw.callBooleanMethod(ctx, gattHandle, method, jvalObj(descHandle))
}

// callGattSetCharacteristicNotification calls gatt.setCharacteristicNotification(char, enable).
func (p *peripheral) callGattSetCharacteristicNotification(ctx context.Context, gattHandle, charHandle int64, enable bool) (bool, error) {
	raw := &p.d.raw
	gattCls, err := raw.findClass(ctx, "android/bluetooth/BluetoothGatt")
	if err != nil {
		return false, err
	}
	method, err := raw.getMethodID(ctx, gattCls, "setCharacteristicNotification",
		"(Landroid/bluetooth/BluetoothGattCharacteristic;Z)Z")
	if err != nil {
		return false, err
	}
	return raw.callBooleanMethod(ctx, gattHandle, method, jvalObj(charHandle), jvalBool(enable))
}

// callGattRequestMtu calls gatt.requestMtu(mtu).
func (p *peripheral) callGattRequestMtu(ctx context.Context, gattHandle int64, mtu int32) (bool, error) {
	raw := &p.d.raw
	gattCls, err := raw.findClass(ctx, "android/bluetooth/BluetoothGatt")
	if err != nil {
		return false, err
	}
	method, err := raw.getMethodID(ctx, gattCls, "requestMtu", "(I)Z")
	if err != nil {
		return false, err
	}
	return raw.callBooleanMethod(ctx, gattHandle, method, jvalInt(mtu))
}

// callCharSetWriteType calls characteristic.setWriteType(writeType).
func (p *peripheral) callCharSetWriteType(ctx context.Context, charHandle int64, writeType int32) error {
	raw := &p.d.raw
	charCls, err := raw.findClass(ctx, "android/bluetooth/BluetoothGattCharacteristic")
	if err != nil {
		return err
	}
	method, err := raw.getMethodID(ctx, charCls, "setWriteType", "(I)V")
	if err != nil {
		return err
	}
	return raw.callVoidMethod(ctx, charHandle, method, jvalInt(writeType))
}

// callCharSetValue calls characteristic.setValue(byte[]).
func (p *peripheral) callCharSetValue(ctx context.Context, charHandle int64, value []byte) error {
	raw := &p.d.raw
	byteArrHandle, err := p.createByteArray(ctx, value)
	if err != nil {
		return err
	}
	defer func() {
		_ = p.d.raw.deleteGlobalRef(ctx, byteArrHandle)
	}()

	charCls, err := raw.findClass(ctx, "android/bluetooth/BluetoothGattCharacteristic")
	if err != nil {
		return err
	}
	method, err := raw.getMethodID(ctx, charCls, "setValue", "([B)Z")
	if err != nil {
		return err
	}
	_, err = raw.callBooleanMethod(ctx, charHandle, method, jvalObj(byteArrHandle))
	return err
}

// callDescSetValue calls descriptor.setValue(byte[]).
func (p *peripheral) callDescSetValue(ctx context.Context, descHandle int64, value []byte) error {
	raw := &p.d.raw
	byteArrHandle, err := p.createByteArray(ctx, value)
	if err != nil {
		return err
	}
	defer func() {
		_ = p.d.raw.deleteGlobalRef(ctx, byteArrHandle)
	}()

	descCls, err := raw.findClass(ctx, "android/bluetooth/BluetoothGattDescriptor")
	if err != nil {
		return err
	}
	method, err := raw.getMethodID(ctx, descCls, "setValue", "([B)Z")
	if err != nil {
		return err
	}
	_, err = raw.callBooleanMethod(ctx, descHandle, method, jvalObj(byteArrHandle))
	return err
}

// callGetValue calls obj.getValue() which returns a byte[].
func (p *peripheral) callGetValue(ctx context.Context, objHandle int64) (int64, error) {
	raw := &p.d.raw
	// Get the object's actual class to find getValue.
	resp, err := raw.client.GetObjectClass(ctx, &jniraw.GetObjectClassRequest{
		ObjectHandle: objHandle,
	})
	if err != nil {
		return 0, err
	}
	cls := resp.GetClassHandle()
	method, err := raw.getMethodID(ctx, cls, "getValue", "()[B")
	if err != nil {
		return 0, err
	}
	return raw.callObjectMethod(ctx, objHandle, method)
}

// callGetProperties calls characteristic.getProperties().
func (p *peripheral) callGetProperties(ctx context.Context, charHandle int64) (int32, error) {
	raw := &p.d.raw
	charCls, err := raw.findClass(ctx, "android/bluetooth/BluetoothGattCharacteristic")
	if err != nil {
		return 0, err
	}
	method, err := raw.getMethodID(ctx, charCls, "getProperties", "()I")
	if err != nil {
		return 0, err
	}
	return raw.callIntMethod(ctx, charHandle, method)
}

// createByteArray creates a JNI byte[] and fills it with the given data.
func (p *peripheral) createByteArray(ctx context.Context, data []byte) (int64, error) {
	raw := &p.d.raw
	handle, err := raw.newPrimitiveArray(ctx, jniraw.JType_BYTE, int32(len(data)))
	if err != nil {
		return 0, fmt.Errorf("creating byte array: %w", err)
	}

	if len(data) > 0 {
		elements := make([]*jniraw.JValue, len(data))
		for i, b := range data {
			elements[i] = &jniraw.JValue{Value: &jniraw.JValue_B{B: int32(int8(b))}}
		}
		if err := raw.setArrayRegion(ctx, handle, jniraw.JType_BYTE, 0, elements); err != nil {
			_ = p.d.raw.deleteGlobalRef(ctx, handle)
			return 0, fmt.Errorf("setting byte array data: %w", err)
		}
	}

	return handle, nil
}
