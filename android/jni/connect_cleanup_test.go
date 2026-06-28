package jni

import (
	"testing"

	jnipkg "github.com/AndroidGoLab/jni"
	"github.com/AndroidGoLab/jni/bluetooth"
)

func TestReleaseConnectedPeripheralCleansGattCallbackAfterConnectCancel(t *testing.T) {
	d := &device{}
	p := newPeripheral(d, nil, "04:A8:5A:58:60:95", "OsmoPocket3")
	gattRef := &jnipkg.Object{}
	gattObj := &bluetooth.Gatt{Obj: gattRef}
	callbackRef := &jnipkg.Object{}

	cleanupCalls := 0
	p.gattObj = gattObj
	p.gattCallbackCleanup = func() {
		t.Fatalf("stored cleanup function must be cleared, not called through peripheral state")
	}

	closeCalls := 0
	deleteCalls := 0
	err := d.releaseConnectedPeripheral(
		p,
		androidGATTConnectionResources{
			gatt:        gattObj,
			callbackRef: callbackRef,
			callbackCleanup: func() {
				cleanupCalls++
			},
			closeGATT: func(got *bluetooth.Gatt) error {
				closeCalls++
				if got != gattObj {
					t.Fatalf("closeGATT got %p, want %p", got, gattObj)
				}
				return nil
			},
			deleteGlobalRef: func(got *jnipkg.Object) error {
				deleteCalls++
				switch got {
				case gattRef:
				case callbackRef:
				default:
					t.Fatalf("deleteGlobalRef got unexpected object %p", got)
				}
				return nil
			},
		},
	)
	if err != nil {
		t.Fatalf("releaseConnectedPeripheral returned error: %v", err)
	}
	if closeCalls != 1 {
		t.Fatalf("closeGATT calls = %d, want 1", closeCalls)
	}
	if deleteCalls != 2 {
		t.Fatalf("deleteGlobalRef calls = %d, want 2", deleteCalls)
	}
	if cleanupCalls != 1 {
		t.Fatalf("callbackCleanup calls = %d, want 1", cleanupCalls)
	}
	if p.gattObj != nil {
		t.Fatalf("releaseConnectedPeripheral left gattObj = %p, want nil", p.gattObj)
	}
	if p.gattCallbackCleanup != nil {
		t.Fatalf("releaseConnectedPeripheral left callback cleanup set")
	}
	if gattObj.Obj != nil {
		t.Fatalf("releaseConnectedPeripheral left BluetoothGatt global ref set")
	}
}
