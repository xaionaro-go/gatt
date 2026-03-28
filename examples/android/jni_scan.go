//go:build android

// jni_scan demonstrates BLE scanning using the gatt JNI backend.
//
// This example is built as a c-shared library and packaged into an APK
// using the Makefile. It runs as an Android NativeActivity.
//
// Build and install:
//
//	cd examples/android && make -f jni_scan.mk install run
package main

/*
#include <android/native_activity.h>
#include <android/log.h>
extern void goOnResume(ANativeActivity*);
static void _onResume(ANativeActivity* a) { goOnResume(a); }
extern void goOnNativeWindowCreated(ANativeActivity*, ANativeWindow*);
static void _onWindowCreated(ANativeActivity* a, ANativeWindow* w) { goOnNativeWindowCreated(a, w); }
static void _setCallbacks(ANativeActivity* a) { a->callbacks->onResume = _onResume; a->callbacks->onNativeWindowCreated = _onWindowCreated; }
static uintptr_t _getVM(ANativeActivity* a) { return (uintptr_t)a->vm; }
static uintptr_t _getClazz(ANativeActivity* a) { return (uintptr_t)a->clazz; }
*/
import "C"
import (
	"bytes"
	"context"
	"fmt"
	"time"
	"unsafe"

	"github.com/AndroidGoLab/jni"
	"github.com/AndroidGoLab/jni/examples/common/ui"
	"github.com/xaionaro-go/gatt"
	"github.com/xaionaro-go/gatt/android"
	androidjni "github.com/xaionaro-go/gatt/android/jni"
)

func main() {}

func init() { ui.Register(run) }

//export ANativeActivity_onCreate
func ANativeActivity_onCreate(activity *C.ANativeActivity, savedState unsafe.Pointer, savedStateSize C.size_t) {
	ui.OnCreate(
		jni.VMFromUintptr(uintptr(C._getVM(activity))),
		jni.ObjectFromUintptr(uintptr(C._getClazz(activity))),
	)
	C._setCallbacks(activity)
}

//export goOnResume
func goOnResume(activity *C.ANativeActivity) {
	ui.OnResume(
		jni.ObjectFromUintptr(uintptr(C._getClazz(activity))),
	)
}

//export goOnNativeWindowCreated
func goOnNativeWindowCreated(activity *C.ANativeActivity, window *C.ANativeWindow) {
	ui.OnNativeWindowCreated(unsafe.Pointer(window))
}

func run(vm *jni.VM, output *bytes.Buffer) error {
	ctx := context.Background()

	appCtx, err := ui.GetAppContext(vm)
	if err != nil {
		return fmt.Errorf("get app context: %w", err)
	}
	defer appCtx.Close()

	// Set the APK classloader so proxy classes (GoInvocationHandler,
	// GoAbstractDispatch) can be found from native threads.
	if err := vm.Do(func(env *jni.Env) error {
		ctxCls, err := env.FindClass("android/content/Context")
		if err != nil {
			return fmt.Errorf("find Context class: %w", err)
		}
		defer env.DeleteLocalRef(&ctxCls.Object)
		mid, err := env.GetMethodID(ctxCls, "getClassLoader", "()Ljava/lang/ClassLoader;")
		if err != nil {
			return fmt.Errorf("get getClassLoader method: %w", err)
		}
		clObj, err := env.CallObjectMethod(appCtx.Obj, mid)
		if err != nil {
			return fmt.Errorf("call getClassLoader: %w", err)
		}
		if clObj != nil {
			globalCL := env.NewGlobalRef(clObj)
			env.DeleteLocalRef(clObj)
			jni.SetProxyClassLoader(globalCL)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("set classloader: %w", err)
	}

	dev, err := android.NewDevice(ctx, android.BackendJNI,
		androidjni.WithVM(vm),
		androidjni.WithContext(appCtx),
	)
	if err != nil {
		return fmt.Errorf("NewDevice: %w", err)
	}

	dev.Handle(ctx,
		gatt.PeripheralDiscovered(func(
			ctx context.Context,
			p gatt.Peripheral,
			a *gatt.Advertisement,
			rssi int,
		) {
			fmt.Fprintf(output, "  %s %q rssi=%d\n", p.ID(), a.LocalName, rssi)
		}),
	)

	if err := dev.Start(ctx, func(ctx context.Context, d gatt.Device, s gatt.State) {
		fmt.Fprintf(output, "state: %s\n", s)
	}); err != nil {
		return fmt.Errorf("Start: %w", err)
	}

	fmt.Fprintln(output, "scanning for 10 seconds...")

	if err := dev.Scan(ctx, nil, false); err != nil {
		return fmt.Errorf("Scan: %w", err)
	}

	time.Sleep(10 * time.Second)

	_ = dev.StopScanning()
	_ = dev.Stop()
	fmt.Fprintln(output, "done.")
	return nil
}

