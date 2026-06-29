package jni

import (
	"errors"
	"testing"

	jnipkg "github.com/AndroidGoLab/jni"
	"github.com/AndroidGoLab/jni/bluetooth"
)

func TestCentralCloseDeletesBluetoothDeviceGlobalRefOnce(t *testing.T) {
	ref := &jnipkg.Object{}
	c := newCentral(nil, &bluetooth.Device{Obj: ref}, "04:A8:5A:58:60:95")

	var deleted []*jnipkg.Object
	c.deleteGlobalRef = func(obj *jnipkg.Object) error {
		deleted = append(deleted, obj)
		return nil
	}

	if err := c.Close(); err != nil {
		t.Fatalf("first Close returned error: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("second Close returned error: %v", err)
	}
	if len(deleted) != 1 {
		t.Fatalf("deleteGlobalRef calls = %d, want 1", len(deleted))
	}
	if deleted[0] != ref {
		t.Fatalf("deleteGlobalRef object = %p, want %p", deleted[0], ref)
	}
	if c.btDev != nil {
		t.Fatalf("Close left btDev = %p, want nil", c.btDev)
	}
}

func TestCentralWithBluetoothDeviceObjectRejectsClosedCentral(t *testing.T) {
	c := newCentral(nil, &bluetooth.Device{Obj: &jnipkg.Object{}}, "04:A8:5A:58:60:95")
	c.deleteGlobalRef = func(*jnipkg.Object) error { return nil }

	if err := c.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	err := c.withBluetoothDeviceObject(func(*jnipkg.Object) error {
		t.Fatalf("withBluetoothDeviceObject called callback for closed central")
		return nil
	})
	if !errors.Is(err, errCentralClosed) {
		t.Fatalf("withBluetoothDeviceObject returned %v, want %v", err, errCentralClosed)
	}
}
