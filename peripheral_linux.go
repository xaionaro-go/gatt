package gatt

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/gatt/linux"
)

type peripheral struct {
	// NameChanged is called whenever the peripheral GAP device name has changed.
	NameChanged func(*peripheral)

	// ServicedModified is called when one or more service of a peripheral have changed.
	// A list of invalid service is provided in the parameter.
	ServicesModified func(*peripheral, []*Service)

	d        *device
	services []*Service

	sub *subscriber

	mtu uint16
	l2c io.ReadWriteCloser

	reqCh  chan message
	quitCh chan struct{}

	pd *linux.PlatData // platform specific data
}

func (p *peripheral) Device() Device { return p.d }
func (p *peripheral) ID() string {
	return strings.ToUpper(net.HardwareAddr(p.pd.Address[:]).String())
}
func (p *peripheral) Name() string                            { return p.pd.Name }
func (p *peripheral) Services(ctx context.Context) []*Service { return p.services }

func finish(op byte, h uint16, b []byte) (bool, error) {
	done := b[0] == attrOpError && b[1] == op && b[2] == byte(h) && b[3] == byte(h>>8)
	var err error
	if b[0] == attrOpError {
		err = AttrECode(b[4])
		if err == AttrECodeAttrNotFound {
			// Expect attribute not found errors
			err = nil
		} else {
			// logger.Debugf(ctx, "unexpected protocol error: %s", e)
			// FIXME: terminate the connection
		}
	}
	return done, err
}

func (p *peripheral) DiscoverServices(ctx context.Context, ds []UUID) ([]*Service, error) {
	// p.pd.Conn.Write([]byte{0x02, 0x87, 0x00}) // MTU
	done := false
	start := uint16(0x0001)
	var err error
	for !done {
		op := byte(attrOpReadByGroupReq)
		b := make([]byte, 7)
		b[0] = op
		binary.LittleEndian.PutUint16(b[1:3], start)
		binary.LittleEndian.PutUint16(b[3:5], 0xFFFF)
		binary.LittleEndian.PutUint16(b[5:7], 0x2800)

		b, err = p.sendReq(ctx, op, b)
		if err != nil {
			return nil, fmt.Errorf("unable to send the request: %w", err)
		}
		done, err = finish(op, start, b)
		if done {
			break
		}
		b = b[1:]
		l, b := int(b[0]), b[1:]
		switch {
		case l == 6 && (len(b)%6 == 0):
		case l == 20 && (len(b)%20 == 0):
		default:
			return nil, ErrInvalidLength
		}

		for len(b) != 0 {
			endh := binary.LittleEndian.Uint16(b[2:4])
			u := UUID{b[4:l]}

			if UUIDContains(ds, u) {
				s := &Service{
					uuid: u,
					h:    binary.LittleEndian.Uint16(b[:2]),
					endh: endh,
				}
				p.services = append(p.services, s)
			}

			b = b[l:]
			done = endh == 0xFFFF
			start = endh + 1
		}
	}
	return p.services, err
}

func (p *peripheral) DiscoverIncludedServices(ctx context.Context, ss []UUID, s *Service) ([]*Service, error) {
	// TODO
	return nil, nil
}

func (p *peripheral) DiscoverCharacteristics(ctx context.Context, cs []UUID, s *Service) ([]*Characteristic, error) {
	done := false
	start := s.h
	var prev *Characteristic
	var err error
	for !done {
		op := byte(attrOpReadByTypeReq)
		b := make([]byte, 7)
		b[0] = op
		binary.LittleEndian.PutUint16(b[1:3], start)
		binary.LittleEndian.PutUint16(b[3:5], s.endh)
		binary.LittleEndian.PutUint16(b[5:7], 0x2803)

		b, err = p.sendReq(ctx, op, b)
		if err != nil {
			return nil, fmt.Errorf("unable to send the request: %w", err)
		}
		if done = b[0] != byte(attrOpReadByTypeResp); done {
			break
		}

		b = b[1:]
		l, b := int(b[0]), b[1:]
		switch {
		case l == 7 && (len(b)%7 == 0):
		case l == 21 && (len(b)%21 == 0):
		default:
			return nil, ErrInvalidLength
		}

		for len(b) != 0 {
			h := binary.LittleEndian.Uint16(b[:2])
			props := Property(b[2])
			vh := binary.LittleEndian.Uint16(b[3:5])
			uuid := UUID{b[5:l]}
			service := searchService(ctx, p.services, h, vh)
			if service == nil {
				return nil, fmt.Errorf("unable to find service range that contains 0x%04X - 0x%04X", h, vh)
			}
			c := &Characteristic{
				uuid:    uuid,
				service: service,
				props:   props,
				h:       h,
				vh:      vh,
			}
			if UUIDContains(cs, uuid) {
				service.chars = append(service.chars, c)
			}
			b = b[l:]
			done = vh == service.endh
			start = vh + 1
			if prev != nil {
				prev.endh = c.h - 1
			}
			prev = c
		}
	}
	if len(s.chars) > 1 {
		s.chars[len(s.chars)-1].endh = s.endh
	}
	return s.chars, err
}

func (p *peripheral) DiscoverDescriptors(ctx context.Context, ds []UUID, c *Characteristic) ([]*Descriptor, error) {
	done := false
	start := c.vh + 1
	var err error
	for !done {
		if c.endh == 0 {
			c.endh = c.service.endh
		}
		op := byte(attrOpFindInfoReq)
		b := make([]byte, 5)
		b[0] = op
		binary.LittleEndian.PutUint16(b[1:3], start)
		binary.LittleEndian.PutUint16(b[3:5], c.endh)

		b, err = p.sendReq(ctx, op, b)
		if err != nil {
			return nil, fmt.Errorf("unable to send the request: %w", err)
		}
		done, err = finish(op, start, b)
		if done {
			break
		}
		b = b[1:]

		var l int
		f, b := int(b[0]), b[1:]
		switch {
		case f == 1 && (len(b)%4 == 0):
			l = 4
		case f == 2 && (len(b)%18 == 0):
			l = 18
		default:
			return nil, ErrInvalidLength
		}

		for len(b) != 0 {
			h := binary.LittleEndian.Uint16(b[:2])
			u := UUID{b[2:l]}
			d := &Descriptor{uuid: u, h: h, char: c}
			if UUIDContains(ds, u) {
				c.descs = append(c.descs, d)
			}
			if u.Equal(attrClientCharacteristicConfigUUID) {
				c.cccd = d
			}
			b = b[l:]
			done = h == c.endh
			start = h + 1
		}
	}
	return c.descs, err
}

func (p *peripheral) ReadCharacteristic(ctx context.Context, c *Characteristic) ([]byte, error) {
	b := make([]byte, 3)
	op := byte(attrOpReadReq)
	b[0] = op
	binary.LittleEndian.PutUint16(b[1:3], c.vh)

	b, err := p.sendReq(ctx, op, b)
	if err != nil {
		return nil, fmt.Errorf("unable to send the request: %w", err)
	}
	_, err = finish(op, c.vh, b)
	b = b[1:]
	return b, err
}

func (p *peripheral) ReadLongCharacteristic(ctx context.Context, c *Characteristic) ([]byte, error) {
	// The spec says that a read blob request should fail if the characteristic
	// is smaller than mtu - 1.  To simplify the API, the first read is done
	// with a regular read request.  If the buffer received is equal to mtu -1,
	// then we read the rest of the data using read blob.
	firstRead, err := p.ReadCharacteristic(ctx, c)
	if err != nil {
		return nil, err
	}
	if len(firstRead) < int(p.mtu)-1 {
		return firstRead, nil
	}

	var buf bytes.Buffer
	buf.Write(firstRead)
	off := uint16(len(firstRead))
	done := false
	err = AttrECodeSuccess
	for {
		b := make([]byte, 5)
		op := byte(attrOpReadBlobReq)
		b[0] = op
		binary.LittleEndian.PutUint16(b[1:3], c.vh)
		binary.LittleEndian.PutUint16(b[3:5], off)

		b, err = p.sendReq(ctx, op, b)
		if err != nil {
			return nil, fmt.Errorf("unable to send the request: %w", err)
		}
		done, err = finish(op, c.vh, b)
		if done {
			break
		}
		b = b[1:]
		if len(b) == 0 {
			break
		}
		buf.Write(b)
		off += uint16(len(b))
		if len(b) < int(p.mtu)-1 {
			break
		}
	}
	return buf.Bytes(), err
}

func (p *peripheral) WriteCharacteristic(ctx context.Context, c *Characteristic, value []byte, noResp bool) error {
	b := make([]byte, 3+len(value))
	op := byte(attrOpWriteReq)
	b[0] = op
	if noResp {
		b[0] = attrOpWriteCmd
	}
	binary.LittleEndian.PutUint16(b[1:3], c.vh)
	copy(b[3:], value)

	if noResp {
		if err := p.sendCmd(ctx, op, b); err != nil {
			return fmt.Errorf("unable to send the command: %w", err)
		}
		return nil
	}
	b, err := p.sendReq(ctx, op, b)
	if err != nil {
		return fmt.Errorf("unable to send the request: %w", err)
	}
	_, err = finish(op, c.vh, b)
	b = b[1:]
	_ = b
	return err
}

func (p *peripheral) ReadDescriptor(ctx context.Context, d *Descriptor) ([]byte, error) {
	b := make([]byte, 3)
	op := byte(attrOpReadReq)
	b[0] = op
	binary.LittleEndian.PutUint16(b[1:3], d.h)

	b, err := p.sendReq(ctx, op, b)
	if err != nil {
		return nil, fmt.Errorf("unable to send the request: %w", err)
	}
	_, err = finish(op, d.h, b)
	b = b[1:]
	_ = b
	return b, err
}

func (p *peripheral) WriteDescriptor(ctx context.Context, d *Descriptor, value []byte) error {
	b := make([]byte, 3+len(value))
	op := byte(attrOpWriteReq)
	b[0] = op
	binary.LittleEndian.PutUint16(b[1:3], d.h)
	copy(b[3:], value)

	b, err := p.sendReq(ctx, op, b)
	if err != nil {
		return fmt.Errorf("unable to send the request: %w", err)
	}
	_, err = finish(op, d.h, b)
	b = b[1:]
	_ = b
	return err
}

func (p *peripheral) setNotifyValue(ctx context.Context, c *Characteristic, flag uint16,
	f func(*Characteristic, []byte, error)) error {
	if c.cccd == nil {
		return errors.New("no cccd") // FIXME
	}
	ccc := uint16(0)
	if f != nil {
		ccc = flag
		p.sub.subscribe(c.vh, func(b []byte, err error) { f(c, b, err) })
	}
	b := make([]byte, 5)
	op := byte(attrOpWriteReq)
	b[0] = op
	binary.LittleEndian.PutUint16(b[1:3], c.cccd.h)
	binary.LittleEndian.PutUint16(b[3:5], ccc)

	b, err := p.sendReq(ctx, op, b)
	if err != nil {
		return fmt.Errorf("unable to send the request: %w", err)
	}
	_, err = finish(op, c.cccd.h, b)
	b = b[1:]
	_ = b
	if f == nil {
		p.sub.unsubscribe(c.vh)
	}
	return err
}

func (p *peripheral) SetNotifyValue(ctx context.Context, c *Characteristic,
	f func(*Characteristic, []byte, error)) error {
	return p.setNotifyValue(ctx, c, gattCCCNotifyFlag, f)
}

func (p *peripheral) SetIndicateValue(ctx context.Context, c *Characteristic,
	f func(*Characteristic, []byte, error)) error {
	return p.setNotifyValue(ctx, c, gattCCCIndicateFlag, f)
}

func (p *peripheral) ReadRSSI(ctx context.Context) int {
	// TODO: implement
	return -1
}

func searchService(_ context.Context, services []*Service, start, end uint16) *Service {
	for _, service := range services {
		if service.h < start && service.endh >= end {
			return service
		}
	}
	return nil
}

// TODO: unify the message with OS X pots and refactor
type message struct {
	op     byte
	b      []byte
	respCh chan []byte
}

func (p *peripheral) sendCmd(ctx context.Context, op byte, b []byte) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case p.reqCh <- message{op: op, b: b}:
		return nil
	}
}

func (p *peripheral) sendReq(ctx context.Context, op byte, b []byte) ([]byte, error) {
	m := message{op: op, b: b, respCh: make(chan []byte)}

	// send
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case p.reqCh <- m:
	}

	// receive
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-m.respCh:
		return r, nil
	}
}

func (p *peripheral) loop(ctx context.Context) {
	logger.Tracef(ctx, "loop")
	defer logger.Tracef(ctx, "/loop")

	// Serialize the request.
	respCh := make(chan []byte)

	// Dequeue request loop
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case req := <-p.reqCh:
				p.l2c.Write(req.b)
				if req.respCh == nil {
					break
				}

				for {
					r := <-respCh
					reqOp, respOp := req.b[0], r[0]
					if respOp == attRespFor[reqOp] || (respOp == attrOpError && r[1] == reqOp) {
						req.respCh <- r
						break
					}
					logger.Debugf(ctx, "request 0x%02x got a mismatched response: 0x%02x", reqOp, respOp)
					p.l2c.Write(attErrorResp(respOp, 0x0000, AttrECodeReqNotSupp))
				}
			case <-p.quitCh:
				return
			}
		}
	}()

	// L2CAP implementations shall support a minimum MTU size of 48 bytes.
	// The default value is 672 bytes
	buf := make([]byte, 672)

	// Handling response or notification/indication
	for {
		n, err := p.l2c.Read(buf)
		if n == 0 || err != nil {
			close(p.quitCh)
			return
		}

		b := make([]byte, n)
		copy(b, buf)

		if (b[0] != attrOpHandleNotify) && (b[0] != attrOpHandleInd) {
			logger.Debugf(ctx, "response 0x%x", b[0])
			respCh <- b
			continue
		}

		h := binary.LittleEndian.Uint16(b[1:3])
		f := p.sub.fn(h)
		if f == nil {
			logger.Debugf(ctx, "notified by unsubscribed handle")
			// FIXME: terminate the connection?
		} else {
			go f(b[3:], nil)
		}

		if b[0] == attrOpHandleInd {
			// write acknowledgement for indication
			p.l2c.Write([]byte{attrOpHandleCnf})
		}

	}
}

func (p *peripheral) SetMTU(ctx context.Context, mtu uint16) error {
	b := make([]byte, 3)
	op := byte(attrOpMtuReq)
	b[0] = op
	h := uint16(mtu)
	binary.LittleEndian.PutUint16(b[1:3], h)

	b, err := p.sendReq(ctx, op, b)
	if err != nil {
		return fmt.Errorf("unable to send the request: %w", err)
	}
	done, err := finish(op, h, b)
	if !done {
		serverMTU := binary.LittleEndian.Uint16(b[1:3])
		if serverMTU < mtu {
			mtu = serverMTU
		}
		p.mtu = mtu
	}
	return err
}
