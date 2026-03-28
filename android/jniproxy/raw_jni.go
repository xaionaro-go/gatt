package jniproxy

import (
	"context"
	"fmt"

	jniraw "github.com/AndroidGoLab/jni-proxy/proto/jni_raw"
)

// rawJNI wraps the raw JNI gRPC service to provide convenient helpers for
// common JNI operations (class lookup, method calls, string handling, etc.).
type rawJNI struct {
	client jniraw.JNIServiceClient
}

func (r *rawJNI) findClass(ctx context.Context, name string) (int64, error) {
	resp, err := r.client.FindClass(ctx, &jniraw.FindClassRequest{Name: name})
	if err != nil {
		return 0, fmt.Errorf("FindClass(%q): %w", name, err)
	}
	return resp.GetClassHandle(), nil
}

func (r *rawJNI) getMethodID(ctx context.Context, cls int64, name, sig string) (int64, error) {
	resp, err := r.client.GetMethodID(ctx, &jniraw.GetMethodIDRequest{
		ClassHandle: cls,
		Name:        name,
		Sig:         sig,
	})
	if err != nil {
		return 0, fmt.Errorf("GetMethodID(%q, %q): %w", name, sig, err)
	}
	return resp.GetMethodId(), nil
}

func (r *rawJNI) getStaticMethodID(ctx context.Context, cls int64, name, sig string) (int64, error) {
	resp, err := r.client.GetStaticMethodID(ctx, &jniraw.GetStaticMethodIDRequest{
		ClassHandle: cls,
		Name:        name,
		Sig:         sig,
	})
	if err != nil {
		return 0, fmt.Errorf("GetStaticMethodID(%q, %q): %w", name, sig, err)
	}
	return resp.GetMethodId(), nil
}

func (r *rawJNI) callMethod(ctx context.Context, obj, method int64, retType jniraw.JType, args ...*jniraw.JValue) (*jniraw.JValue, error) {
	resp, err := r.client.CallMethod(ctx, &jniraw.CallMethodRequest{
		ObjectHandle: obj,
		MethodId:     method,
		ReturnType:   retType,
		Args:         args,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetResult(), nil
}

func (r *rawJNI) callStaticMethod(ctx context.Context, cls, method int64, retType jniraw.JType, args ...*jniraw.JValue) (*jniraw.JValue, error) {
	resp, err := r.client.CallStaticMethod(ctx, &jniraw.CallStaticMethodRequest{
		ClassHandle: cls,
		MethodId:    method,
		ReturnType:  retType,
		Args:        args,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetResult(), nil
}

func (r *rawJNI) callBooleanMethod(ctx context.Context, obj, method int64, args ...*jniraw.JValue) (bool, error) {
	result, err := r.callMethod(ctx, obj, method, jniraw.JType_BOOLEAN, args...)
	if err != nil {
		return false, err
	}
	if result == nil {
		return false, nil
	}
	return result.GetZ(), nil
}

func (r *rawJNI) callObjectMethod(ctx context.Context, obj, method int64, args ...*jniraw.JValue) (int64, error) {
	result, err := r.callMethod(ctx, obj, method, jniraw.JType_OBJECT, args...)
	if err != nil {
		return 0, err
	}
	if result == nil {
		return 0, nil
	}
	return result.GetL(), nil
}

func (r *rawJNI) callVoidMethod(ctx context.Context, obj, method int64, args ...*jniraw.JValue) error {
	_, err := r.callMethod(ctx, obj, method, jniraw.JType_VOID, args...)
	return err
}

func (r *rawJNI) callIntMethod(ctx context.Context, obj, method int64, args ...*jniraw.JValue) (int32, error) {
	result, err := r.callMethod(ctx, obj, method, jniraw.JType_INT, args...)
	if err != nil {
		return 0, err
	}
	if result == nil {
		return 0, nil
	}
	return result.GetI(), nil
}

func (r *rawJNI) getString(ctx context.Context, handle int64) (string, error) {
	if handle == 0 {
		return "", nil
	}
	resp, err := r.client.GetStringUTFChars(ctx, &jniraw.GetStringUTFCharsRequest{
		StringHandle: handle,
	})
	if err != nil {
		return "", fmt.Errorf("GetStringUTFChars: %w", err)
	}
	return resp.GetValue(), nil
}

func (r *rawJNI) newStringUTF(ctx context.Context, s string) (int64, error) {
	resp, err := r.client.NewStringUTF(ctx, &jniraw.NewStringUTFRequest{Value: s})
	if err != nil {
		return 0, fmt.Errorf("NewStringUTF: %w", err)
	}
	return resp.GetStringHandle(), nil
}

func (r *rawJNI) getAppContext(ctx context.Context) (int64, error) {
	resp, err := r.client.GetAppContext(ctx, &jniraw.GetAppContextRequest{})
	if err != nil {
		return 0, fmt.Errorf("GetAppContext: %w", err)
	}
	return resp.GetContextHandle(), nil
}

func (r *rawJNI) getByteArrayData(ctx context.Context, handle int64) ([]byte, error) {
	if handle == 0 {
		return nil, nil
	}
	resp, err := r.client.GetByteArrayData(ctx, &jniraw.GetByteArrayDataRequest{
		ArrayHandle: handle,
	})
	if err != nil {
		return nil, fmt.Errorf("GetByteArrayData: %w", err)
	}
	return resp.GetData(), nil
}

func (r *rawJNI) newPrimitiveArray(ctx context.Context, elemType jniraw.JType, length int32) (int64, error) {
	resp, err := r.client.NewPrimitiveArray(ctx, &jniraw.NewPrimitiveArrayRequest{
		ElementType: elemType,
		Length:      length,
	})
	if err != nil {
		return 0, fmt.Errorf("NewPrimitiveArray: %w", err)
	}
	return resp.GetArrayHandle(), nil
}

func (r *rawJNI) setArrayRegion(ctx context.Context, handle int64, elemType jniraw.JType, start int32, elements []*jniraw.JValue) error {
	_, err := r.client.SetArrayRegion(ctx, &jniraw.SetArrayRegionRequest{
		ArrayHandle: handle,
		ElementType: elemType,
		Start:       start,
		Elements:    elements,
	})
	return err
}

func (r *rawJNI) getArrayLength(ctx context.Context, handle int64) (int32, error) {
	resp, err := r.client.GetArrayLength(ctx, &jniraw.GetArrayLengthRequest{
		ArrayHandle: handle,
	})
	if err != nil {
		return 0, err
	}
	return resp.GetLength(), nil
}

func (r *rawJNI) getObjectArrayElement(ctx context.Context, handle int64, index int32) (int64, error) {
	resp, err := r.client.GetObjectArrayElement(ctx, &jniraw.GetObjectArrayElementRequest{
		ArrayHandle: handle,
		Index:       index,
	})
	if err != nil {
		return 0, err
	}
	return resp.GetElementHandle(), nil
}

func (r *rawJNI) deleteGlobalRef(ctx context.Context, handle int64) error {
	if handle == 0 {
		return nil
	}
	_, err := r.client.DeleteGlobalRef(ctx, &jniraw.DeleteGlobalRefRequest{
		RefHandle: handle,
	})
	return err
}

// objectToString calls Object.toString() on a JNI object handle and returns the Go string.
func (r *rawJNI) objectToString(ctx context.Context, handle int64) (string, error) {
	if handle == 0 {
		return "", nil
	}
	objCls, err := r.findClass(ctx, "java/lang/Object")
	if err != nil {
		return "", err
	}
	toStringMethod, err := r.getMethodID(ctx, objCls, "toString", "()Ljava/lang/String;")
	if err != nil {
		return "", err
	}
	strHandle, err := r.callObjectMethod(ctx, handle, toStringMethod)
	if err != nil {
		return "", err
	}
	if strHandle == 0 {
		return "", nil
	}
	s, err := r.getString(ctx, strHandle)
	if err != nil {
		return "", err
	}
	return s, nil
}

// javaListToSlice converts a java.util.List handle to a slice of object handles.
func (r *rawJNI) javaListToSlice(ctx context.Context, listHandle int64) ([]int64, error) {
	if listHandle == 0 {
		return nil, nil
	}
	listCls, err := r.findClass(ctx, "java/util/List")
	if err != nil {
		return nil, err
	}
	sizeMethod, err := r.getMethodID(ctx, listCls, "size", "()I")
	if err != nil {
		return nil, err
	}
	getMethod, err := r.getMethodID(ctx, listCls, "get", "(I)Ljava/lang/Object;")
	if err != nil {
		return nil, err
	}
	size, err := r.callIntMethod(ctx, listHandle, sizeMethod)
	if err != nil {
		return nil, fmt.Errorf("List.size(): %w", err)
	}
	result := make([]int64, 0, size)
	for i := int32(0); i < size; i++ {
		elem, err := r.callObjectMethod(ctx, listHandle, getMethod, jvalInt(i))
		if err != nil {
			return result, fmt.Errorf("List.get(%d): %w", i, err)
		}
		result = append(result, elem)
	}
	return result, nil
}

// jvalInt creates a JValue wrapping an int.
func jvalInt(v int32) *jniraw.JValue {
	return &jniraw.JValue{Value: &jniraw.JValue_I{I: v}}
}

// jvalObj creates a JValue wrapping an object handle.
func jvalObj(handle int64) *jniraw.JValue {
	return &jniraw.JValue{Value: &jniraw.JValue_L{L: handle}}
}

// jvalBool creates a JValue wrapping a boolean.
func jvalBool(v bool) *jniraw.JValue {
	return &jniraw.JValue{Value: &jniraw.JValue_Z{Z: v}}
}
