package gatt

import (
	"context"
	"errors"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/gatt/xpc"
)

type peripheral struct {
	// NameChanged is called whenever the peripheral GAP Device name has changed.
	NameChanged func(Peripheral)

	// ServicesModified is called when one or more service of a peripheral have changed.
	// A list of invalid service is provided in the parameter.
	ServicesModified func(Peripheral, []*Service)

	device *device
	svcs   []*Service

	sub *subscriber

	id   xpc.UUID
	name string

	reqc   chan message
	respCh chan message
	quitc  chan struct{}
}

func NewPeripheral(u UUID) Peripheral { return &peripheral{id: xpc.UUID(u.b)} }

func (p *peripheral) Device() Device                          { return p.device }
func (p *peripheral) ID() string                              { return p.id.String() }
func (p *peripheral) Name() string                            { return p.name }
func (p *peripheral) Services(ctx context.Context) []*Service { return p.svcs }

func (p *peripheral) DiscoverServices(ctx context.Context, ss []UUID) ([]*Service, error) {
	resp := p.sendReq(ctx, 45, xpc.Dict{
		"kCBMsgArgDeviceUUID": p.id,
		"kCBMsgArgUUIDs":      uuidSlice(ss),
	})
	if res := resp.MustGetInt("kCBMsgArgResult"); res != 0 {
		return nil, AttrECode(res)
	}
	svcs := []*Service{}
	for _, xss := range resp["kCBMsgArgServices"].(xpc.Array) {
		xs := xss.(xpc.Dict)
		u := MustParseUUID(xs.MustGetHexBytes("kCBMsgArgUUID"))
		h := uint16(xs.MustGetInt("kCBMsgArgServiceStartHandle"))
		endh := uint16(xs.MustGetInt("kCBMsgArgServiceEndHandle"))
		svcs = append(svcs, &Service{uuid: u, h: h, endh: endh})
	}
	p.svcs = svcs
	return svcs, nil
}

func (p *peripheral) DiscoverIncludedServices(ctx context.Context, ss []UUID, s *Service) ([]*Service, error) {
	resp := p.sendReq(ctx, 60, xpc.Dict{
		"kCBMsgArgDeviceUUID":         p.id,
		"kCBMsgArgServiceStartHandle": s.h,
		"kCBMsgArgServiceEndHandle":   s.endh,
		"kCBMsgArgUUIDs":              uuidSlice(ss),
	})
	if res := resp.MustGetInt("kCBMsgArgResult"); res != 0 {
		return nil, AttrECode(res)
	}
	// TODO
	return nil, errNotImplemented
}

func (p *peripheral) DiscoverCharacteristics(ctx context.Context, cs []UUID, s *Service) ([]*Characteristic, error) {
	resp := p.sendReq(ctx, 62, xpc.Dict{
		"kCBMsgArgDeviceUUID":         p.id,
		"kCBMsgArgServiceStartHandle": s.h,
		"kCBMsgArgServiceEndHandle":   s.endh,
		"kCBMsgArgUUIDs":              uuidSlice(cs),
	})
	if res := resp.MustGetInt("kCBMsgArgResult"); res != 0 {
		return nil, AttrECode(res)
	}
	for _, xcs := range resp.MustGetArray("kCBMsgArgCharacteristics") {
		xc := xcs.(xpc.Dict)
		u := MustParseUUID(xc.MustGetHexBytes("kCBMsgArgUUID"))
		ch := uint16(xc.MustGetInt("kCBMsgArgCharacteristicHandle"))
		vh := uint16(xc.MustGetInt("kCBMsgArgCharacteristicValueHandle"))
		props := Property(xc.MustGetInt("kCBMsgArgCharacteristicProperties"))
		c := &Characteristic{uuid: u, svc: s, props: props, h: ch, vh: vh}
		s.chars = append(s.chars, c)
	}
	return s.chars, nil
}

func (p *peripheral) DiscoverDescriptors(ctx context.Context, ds []UUID, c *Characteristic) ([]*Descriptor, error) {
	resp := p.sendReq(ctx, 70, xpc.Dict{
		"kCBMsgArgDeviceUUID":                p.id,
		"kCBMsgArgCharacteristicHandle":      c.h,
		"kCBMsgArgCharacteristicValueHandle": c.vh,
		"kCBMsgArgUUIDs":                     uuidSlice(ds),
	})
	for _, xds := range resp.MustGetArray("kCBMsgArgDescriptors") {
		xd := xds.(xpc.Dict)
		u := MustParseUUID(xd.MustGetHexBytes("kCBMsgArgUUID"))
		h := uint16(xd.MustGetInt("kCBMsgArgDescriptorHandle"))
		d := &Descriptor{uuid: u, char: c, h: h}
		c.descs = append(c.descs, d)
	}
	return c.descs, nil
}

func (p *peripheral) ReadCharacteristic(ctx context.Context, c *Characteristic) ([]byte, error) {
	resp := p.sendReq(ctx, 65, xpc.Dict{
		"kCBMsgArgDeviceUUID":                p.id,
		"kCBMsgArgCharacteristicHandle":      c.h,
		"kCBMsgArgCharacteristicValueHandle": c.vh,
	})
	if res := resp.MustGetInt("kCBMsgArgResult"); res != 0 {
		return nil, AttrECode(res)
	}
	b := resp.MustGetBytes("kCBMsgArgData")
	return b, nil
}

func (p *peripheral) ReadLongCharacteristic(ctx context.Context, c *Characteristic) ([]byte, error) {
	return nil, errors.New("Not implemented")
}

func (p *peripheral) WriteCharacteristic(ctx context.Context, c *Characteristic, b []byte, noResp bool) error {
	args := xpc.Dict{
		"kCBMsgArgDeviceUUID":                p.id,
		"kCBMsgArgCharacteristicHandle":      c.h,
		"kCBMsgArgCharacteristicValueHandle": c.vh,
		"kCBMsgArgData":                      b,
		"kCBMsgArgType":                      map[bool]int{false: 0, true: 1}[noResp],
	}
	if noResp {
		p.sendCmd(ctx, 66, args)
		return nil
	}
	resp := p.sendReq(ctx, 66, args)
	if res := resp.MustGetInt("kCBMsgArgResult"); res != 0 {
		return AttrECode(res)
	}
	return nil
}

func (p *peripheral) ReadDescriptor(ctx context.Context, d *Descriptor) ([]byte, error) {
	resp := p.sendReq(ctx, 77, xpc.Dict{
		"kCBMsgArgDeviceUUID":       p.id,
		"kCBMsgArgDescriptorHandle": d.h,
	})
	if res := resp.MustGetInt("kCBMsgArgResult"); res != 0 {
		return nil, AttrECode(res)
	}
	b := resp.MustGetBytes("kCBMsgArgData")
	return b, nil
}

func (p *peripheral) WriteDescriptor(ctx context.Context, d *Descriptor, b []byte) error {
	resp := p.sendReq(ctx, 78, xpc.Dict{
		"kCBMsgArgDeviceUUID":       p.id,
		"kCBMsgArgDescriptorHandle": d.h,
		"kCBMsgArgData":             b,
	})
	if res := resp.MustGetInt("kCBMsgArgResult"); res != 0 {
		return AttrECode(res)
	}
	return nil
}

func (p *peripheral) SetNotifyValue(ctx context.Context, c *Characteristic, f func(*Characteristic, []byte, error)) error {
	set := 1
	if f == nil {
		set = 0
	}
	// To avoid race condition, registration is handled before requesting the server.
	if f != nil {
		// Note: when notified, core bluetooth reports characteristic handle, not value's handle.
		p.sub.subscribe(c.h, func(b []byte, err error) { f(c, b, err) })
	}
	resp := p.sendReq(ctx, 68, xpc.Dict{
		"kCBMsgArgDeviceUUID":                p.id,
		"kCBMsgArgCharacteristicHandle":      c.h,
		"kCBMsgArgCharacteristicValueHandle": c.vh,
		"kCBMsgArgState":                     set,
	})
	if res := resp.MustGetInt("kCBMsgArgResult"); res != 0 {
		return AttrECode(res)
	}
	// To avoid race condition, unregistration is handled after server responses.
	if f == nil {
		p.sub.unsubscribe(c.h)
	}
	return nil
}

func (p *peripheral) SetIndicateValue(ctx context.Context, c *Characteristic,
	f func(*Characteristic, []byte, error)) error {
	// TODO: Implement set indications logic for darwin (https://github.com/paypal/gatt/issues/32)
	return nil
}

func (p *peripheral) ReadRSSI(ctx context.Context) int {
	resp := p.sendReq(ctx, 43, xpc.Dict{"kCBMsgArgDeviceUUID": p.id})
	return resp.MustGetInt("kCBMsgArgData")
}

func (p *peripheral) SetMTU(ctx context.Context, mtu uint16) error {
	return errors.New("Not implemented")
}

func uuidSlice(uu []UUID) [][]byte {
	us := [][]byte{}
	for _, u := range uu {
		us = append(us, reverse(u.b))
	}
	return us
}

type message struct {
	id     int
	args   xpc.Dict
	respCh chan xpc.Dict
}

func (p *peripheral) sendCmd(ctx context.Context, id int, args xpc.Dict) {
	p.reqc <- message{id: id, args: args}
}

func (p *peripheral) sendReq(ctx context.Context, id int, args xpc.Dict) xpc.Dict {
	m := message{id: id, args: args, respCh: make(chan xpc.Dict)}
	p.reqc <- m
	return <-m.respCh
}

func (p *peripheral) loop(ctx context.Context) {
	respCh := make(chan message)

	go func() {
		for {
			select {
			case req := <-p.reqc:
				p.device.sendCBMsg(ctx, req.id, req.args)
				if req.respCh == nil {
					break
				}
				m := <-respCh
				req.respCh <- m.args
			case <-p.quitc:
				return
			}
		}
	}()

	for {
		select {
		case resp := <-p.respCh:
			// Notification
			if resp.id == 71 && resp.args.GetInt("kCBMsgArgIsNotification", 0) != 0 {
				// While we're notified with the value's handle, blued reports the characteristic handle.
				ch := uint16(resp.args.MustGetInt("kCBMsgArgCharacteristicHandle"))
				b := resp.args.MustGetBytes("kCBMsgArgData")
				f := p.sub.fn(ch)
				if f == nil {
					logger.Debugf(ctx, "notified by unsubscribed handle")
					// FIXME: should terminate the connection?
				} else {
					go f(b, nil)
				}
				break
			}
			respCh <- resp
		case <-p.quitc:
			return
		}
	}
}
