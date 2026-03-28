package jniproxy

import (
	"sync"

	"github.com/xaionaro-go/gatt"
)

// Ensure *central satisfies gatt.Central at compile time.
var _ gatt.Central = (*central)(nil)

// central represents a remote central device that connected to our GATT server.
type central struct {
	d    *device
	addr string

	mu  sync.Mutex
	mtu int
}

func newCentral(d *device, addr string) *central {
	return &central{
		d:    d,
		addr: addr,
		mtu:  23, // BLE default
	}
}

func (c *central) ID() string   { return c.addr }
func (c *central) Close() error { return nil }

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
