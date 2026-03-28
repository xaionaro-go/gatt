package jni

import (
	jnipkg "github.com/AndroidGoLab/jni"
	"github.com/AndroidGoLab/jni/app"
	"github.com/xaionaro-go/gatt"
)

type config struct {
	vm     *jnipkg.VM
	appCtx *app.Context
}

// WithVM sets the JNI VM pointer for the device.
func WithVM(vm *jnipkg.VM) gatt.Option {
	return func(d gatt.Device) error {
		dd, ok := d.(*device)
		if !ok {
			return nil
		}
		dd.cfg.vm = vm
		return nil
	}
}

// WithContext sets the Android app.Context for the device.
func WithContext(ctx *app.Context) gatt.Option {
	return func(d gatt.Device) error {
		dd, ok := d.(*device)
		if !ok {
			return nil
		}
		dd.cfg.appCtx = ctx
		return nil
	}
}
