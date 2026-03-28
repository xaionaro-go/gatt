package jni

import (
	"fmt"
	"unsafe"

	jnipkg "github.com/AndroidGoLab/jni"
	"github.com/AndroidGoLab/jni/bluetooth"
)

// unboxInt extracts an int value from a boxed java.lang.Integer object.
// If the object is nil, returns 0.
func unboxInt(env *jnipkg.Env, obj *jnipkg.Object) int32 {
	if obj == nil || obj.Ref() == 0 {
		return 0
	}

	cls, err := env.FindClass("java/lang/Integer")
	if err != nil || cls == nil {
		return 0
	}
	defer env.DeleteLocalRef(&cls.Object)

	mid, err := env.GetMethodID(cls, "intValue", "()I")
	if err != nil {
		return 0
	}

	val, err := env.CallIntMethod(obj, mid)
	if err != nil {
		return 0
	}
	return val
}

// byteArrayToGoBytes converts a JNI byte[] (jbyteArray) object to a Go []byte.
// Returns nil if the object is nil.
func byteArrayToGoBytes(env *jnipkg.Env, obj *jnipkg.Object) []byte {
	if obj == nil || obj.Ref() == 0 {
		return nil
	}

	ba := &jnipkg.ByteArray{Array: jnipkg.Array{Object: *obj}}
	length := env.GetArrayLength(&ba.Array)
	if length <= 0 {
		return nil
	}

	buf := make([]byte, length)
	env.GetByteArrayRegion(ba, 0, length, unsafe.Pointer(&buf[0]))
	return buf
}

// javaUUIDToString calls UUID.toString() on a BluetoothGattService's UUID.
func javaUUIDToString(
	env *jnipkg.Env,
	_ *jnipkg.VM,
	svc *bluetooth.GattService,
) (string, error) {
	uuidObj, err := svc.GetUuid()
	if err != nil {
		return "", fmt.Errorf("getUuid: %w", err)
	}
	if uuidObj == nil {
		return "", fmt.Errorf("getUuid returned nil")
	}
	defer env.DeleteGlobalRef(uuidObj)

	return callToString(env, uuidObj)
}

// javaCharUUIDToString calls UUID.toString() on a BluetoothGattCharacteristic's UUID.
func javaCharUUIDToString(
	env *jnipkg.Env,
	_ *jnipkg.VM,
	c *bluetooth.GattCharacteristic,
) (string, error) {
	uuidObj, err := c.GetUuid()
	if err != nil {
		return "", fmt.Errorf("getUuid: %w", err)
	}
	if uuidObj == nil {
		return "", fmt.Errorf("getUuid returned nil")
	}
	defer env.DeleteGlobalRef(uuidObj)

	return callToString(env, uuidObj)
}

// javaDescUUIDToString calls UUID.toString() on a BluetoothGattDescriptor's UUID.
func javaDescUUIDToString(
	env *jnipkg.Env,
	_ *jnipkg.VM,
	d *bluetooth.GattDescriptor,
) (string, error) {
	uuidObj, err := d.GetUuid()
	if err != nil {
		return "", fmt.Errorf("getUuid: %w", err)
	}
	if uuidObj == nil {
		return "", fmt.Errorf("getUuid returned nil")
	}
	defer env.DeleteGlobalRef(uuidObj)

	return callToString(env, uuidObj)
}

// callToString calls Object.toString() on a JNI object and returns the Go string.
func callToString(env *jnipkg.Env, obj *jnipkg.Object) (string, error) {
	cls, err := env.FindClass("java/lang/Object")
	if err != nil {
		return "", fmt.Errorf("find Object class: %w", err)
	}
	defer env.DeleteLocalRef(&cls.Object)

	mid, err := env.GetMethodID(cls, "toString", "()Ljava/lang/String;")
	if err != nil {
		return "", fmt.Errorf("get toString method: %w", err)
	}

	strObj, err := env.CallObjectMethod(obj, mid)
	if err != nil {
		return "", fmt.Errorf("call toString: %w", err)
	}
	if strObj == nil {
		return "", nil
	}
	defer env.DeleteLocalRef(strObj)

	return env.GoString((*jnipkg.String)(unsafe.Pointer(strObj))), nil
}

// javaListToSlice converts a java.util.List to a slice of global-ref JNI objects.
// The caller is responsible for eventually calling env.DeleteGlobalRef on each element.
func javaListToSlice(env *jnipkg.Env, listObj *jnipkg.Object) ([]*jnipkg.Object, error) {
	listCls, err := env.FindClass("java/util/List")
	if err != nil {
		return nil, fmt.Errorf("find List class: %w", err)
	}
	defer env.DeleteLocalRef(&listCls.Object)

	sizeMid, err := env.GetMethodID(listCls, "size", "()I")
	if err != nil {
		return nil, fmt.Errorf("get size method: %w", err)
	}

	getMid, err := env.GetMethodID(listCls, "get", "(I)Ljava/lang/Object;")
	if err != nil {
		return nil, fmt.Errorf("get get method: %w", err)
	}

	size, err := env.CallIntMethod(listObj, sizeMid)
	if err != nil {
		return nil, fmt.Errorf("call size: %w", err)
	}

	result := make([]*jnipkg.Object, 0, size)
	for i := int32(0); i < size; i++ {
		elem, err := env.CallObjectMethod(listObj, getMid, jnipkg.IntValue(i))
		if err != nil {
			return result, fmt.Errorf("call get(%d): %w", i, err)
		}
		if elem == nil {
			continue
		}
		globalElem := env.NewGlobalRef(elem)
		env.DeleteLocalRef(elem)
		result = append(result, globalElem)
	}

	return result, nil
}
