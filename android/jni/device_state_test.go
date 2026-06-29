package jni

import (
	"context"
	"testing"

	"github.com/xaionaro-go/gatt"
)

func TestDeviceNotifyStateChangedUsesProvidedStateSnapshot(t *testing.T) {
	d := &device{state: gatt.StatePoweredOn}
	var got gatt.State

	d.state = gatt.StatePoweredOff
	d.notifyStateChanged(
		context.Background(),
		func(_ context.Context, gotDevice gatt.Device, state gatt.State) {
			if gotDevice != d {
				t.Fatalf("stateChanged device = %p, want %p", gotDevice, d)
			}
			got = state
		},
		gatt.StatePoweredOn,
	)

	if got != gatt.StatePoweredOn {
		t.Fatalf("stateChanged state = %v, want %v", got, gatt.StatePoweredOn)
	}
}
