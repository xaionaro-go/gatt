package gatt

import (
	"context"
	"sync"

	"github.com/xaionaro-go/gatt/xpc"
)

type central struct {
	dev            *device
	uuid           UUID
	mtu            int
	notifiers      map[uint16]*notifier
	notifiersMutex *sync.Mutex
}

func newCentral(d *device, u UUID) *central {
	return &central{
		dev:            d,
		mtu:            23,
		uuid:           u,
		notifiers:      make(map[uint16]*notifier),
		notifiersMutex: &sync.Mutex{},
	}
}

func (c *central) ID() string   { return c.uuid.String() }
func (c *central) Close() error { return nil }
func (c *central) MTU() int     { return c.mtu }

func (c *central) sendNotification(ctx context.Context, a *attr, b []byte) (int, error) {
	data := make([]byte, len(b))
	copy(data, b) // have to make a copy, why?
	c.dev.sendCmd(ctx, 15, xpc.Dict{
		// "kCBMsgArgUUIDs": [][]byte{reverse(c.uuid.b)}, // connection interrupted
		// "kCBMsgArgUUIDs": [][]byte{c.uuid.b}, // connection interrupted
		// "kCBMsgArgUUIDs": []xpc.UUID{xpc.UUID(reverse(c.uuid.b))},
		// "kCBMsgArgUUIDs": []xpc.UUID{xpc.UUID(c.uuid.b)},
		// "kCBMsgArgUUIDs": reverse(c.uuid.b),
		//
		// FIXME: Sigh... tried to targeting the central, but couldn't get work.
		// So, broadcast to all subscribed centrals. Either of the following works.
		// "kCBMsgArgUUIDs": []xpc.UUID{},
		"kCBMsgArgUUIDs":       [][]byte{},
		"kCBMsgArgAttributeID": a.h,
		"kCBMsgArgData":        data,
	})
	return len(b), nil
}

func (c *central) startNotify(ctx context.Context, a *attr, maxLen int) {
	c.notifiersMutex.Lock()
	defer c.notifiersMutex.Unlock()
	if _, found := c.notifiers[a.h]; found {
		return
	}
	n := newNotifier(c, a, maxLen)
	c.notifiers[a.h] = n
	char := a.pvt.(*Characteristic)
	go char.notifyHandler.ServeNotify(ctx, Request{Central: c}, n)
}

func (c *central) stopNotify(a *attr) {
	c.notifiersMutex.Lock()
	defer c.notifiersMutex.Unlock()
	if n, found := c.notifiers[a.h]; found {
		n.stop()
		delete(c.notifiers, a.h)
	}
}
