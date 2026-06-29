package jni

import (
	"errors"
	"sync"

	jnipkg "github.com/AndroidGoLab/jni"
	"github.com/AndroidGoLab/jni/bluetooth"
	"github.com/xaionaro-go/gatt"
)

// Ensure *central satisfies gatt.Central at compile time.
var _ gatt.Central = (*central)(nil)

var errCentralClosed = errors.New("central closed")

// central represents a remote central device that connected to our GATT server.
type central struct {
	d               *device
	btDev           *bluetooth.Device
	addr            string
	deleteGlobalRef func(*jnipkg.Object) error

	mu  sync.Mutex
	mtu int
}

func newCentral(
	d *device,
	btDev *bluetooth.Device,
	addr string,
) *central {
	c := &central{
		d:     d,
		btDev: btDev,
		addr:  addr,
		mtu:   23, // BLE default
	}
	if d != nil {
		c.deleteGlobalRef = d.deleteGlobalRef
	}
	return c
}

func (c *central) ID() string { return c.addr }

func (c *central) Close() error {
	c.mu.Lock()
	btDev := c.btDev
	var obj *jnipkg.Object
	if btDev != nil {
		obj = btDev.Obj
	}
	c.btDev = nil
	deleteGlobalRef := c.deleteGlobalRef
	c.mu.Unlock()

	if obj == nil || deleteGlobalRef == nil {
		return nil
	}
	return deleteGlobalRef(obj)
}

func (c *central) withBluetoothDeviceObject(
	fn func(*jnipkg.Object) error,
) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.btDev == nil || c.btDev.Obj == nil {
		return errCentralClosed
	}
	return fn(c.btDev.Obj)
}

func (c *central) MTU() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mtu
}

func (c *central) setMTU(mtu int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mtu = mtu
}
