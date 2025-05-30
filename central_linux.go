package gatt

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
)

type security int

const (
	securityLow = iota
	securityMed
	securityHigh
)

type central struct {
	attrs          *attrRange
	mtu            uint16
	addr           net.HardwareAddr
	security       security
	l2Conn         io.ReadWriteCloser
	notifiers      map[uint16]*notifier
	notifiersMutex *sync.Mutex
}

func newCentral(a *attrRange, addr net.HardwareAddr, l2Conn io.ReadWriteCloser) *central {
	return &central{
		attrs:          a,
		mtu:            23,
		addr:           addr,
		security:       securityLow,
		l2Conn:         l2Conn,
		notifiers:      make(map[uint16]*notifier),
		notifiersMutex: &sync.Mutex{},
	}
}

func (c *central) ID() string {
	return c.addr.String()
}

func (c *central) Close() error {
	c.notifiersMutex.Lock()
	defer c.notifiersMutex.Unlock()
	for _, n := range c.notifiers {
		n.stop()
	}
	return c.l2Conn.Close()
}

func (c *central) MTU() int {
	return int(c.mtu)
}

func (c *central) loop(ctx context.Context) {
	for {
		// L2CAP implementations shall support a minimum MTU size of 48 bytes.
		// The default value is 672 bytes
		b := make([]byte, 672)
		n, err := c.l2Conn.Read(b)
		if n == 0 || err != nil {
			c.Close()
			break
		}
		if resp := c.handleReq(ctx, b[:n]); resp != nil {
			c.l2Conn.Write(resp)
		}
	}
}

// handleReq dispatches a raw request from the central shim
// to an appropriate handler, based on its type.
// It panics if len(b) == 0.
func (c *central) handleReq(ctx context.Context, b []byte) []byte {
	var resp []byte
	switch reqType, req := b[0], b[1:]; reqType {
	case attrOpMtuReq:
		resp = c.handleMTU(req)
	case attrOpFindInfoReq:
		resp = c.handleFindInfo(req)
	case attrOpFindByTypeValueReq:
		resp = c.handleFindByTypeValue(req)
	case attrOpReadByTypeReq:
		resp = c.handleReadByType(ctx, req)
	case attrOpReadReq:
		resp = c.handleRead(ctx, req)
	case attrOpReadBlobReq:
		resp = c.handleReadBlob(ctx, req)
	case attrOpReadByGroupReq:
		resp = c.handleReadByGroup(req)
	case attrOpWriteReq, attrOpWriteCmd:
		resp = c.handleWrite(ctx, reqType, req)
	case attrOpReadMultiReq, attrOpPrepWriteReq, attrOpExecWriteReq, attrOpSignedWriteCmd:
		fallthrough
	default:
		resp = attErrorResp(reqType, 0x0000, AttrECodeReqNotSupp)
	}
	return resp
}

func (c *central) handleMTU(b []byte) []byte {
	c.mtu = binary.LittleEndian.Uint16(b[:2])
	if c.mtu < 23 {
		c.mtu = 23
	}
	if c.mtu >= 256 {
		c.mtu = 256
	}
	return []byte{attrOpMtuResp, uint8(c.mtu), uint8(c.mtu >> 8)}
}

// REQ: FindInfoReq(0x04), StartHandle, EndHandle
// RSP: FindInfoRsp(0x05), UUIDFormat, Handle, UUID, Handle, UUID, ...
func (c *central) handleFindInfo(b []byte) []byte {
	start, end := readHandleRange(b[:4])

	w := newL2capWriter(c.mtu)
	w.WriteByteFit(attrOpFindInfoResp)

	uuidLen := -1
	for _, a := range c.attrs.Subrange(start, end) {
		if uuidLen == -1 {
			uuidLen = a.typ.Len()
			if uuidLen == 2 {
				w.WriteByteFit(0x01) // TODO: constants for 16bit vs 128bit uuid magic numbers here
			} else {
				w.WriteByteFit(0x02)
			}
		}
		if a.typ.Len() != uuidLen {
			break
		}
		w.Chunk()
		w.WriteUint16Fit(a.h)
		w.WriteUUIDFit(a.typ)
		if ok := w.Commit(); !ok {
			break
		}
	}

	if uuidLen == -1 {
		return attErrorResp(attrOpFindInfoReq, start, AttrECodeAttrNotFound)
	}
	return w.Bytes()
}

// REQ: FindByTypeValueReq(0x06), StartHandle, EndHandle, Type(UUID), Value
// RSP: FindByTypeValueRsp(0x07), AttrHandle, GroupEndHandle, AttrHandle, GroupEndHandle, ...
func (c *central) handleFindByTypeValue(b []byte) []byte {
	start, end := readHandleRange(b[:4])
	t := UUID{b[4:6]}
	u := UUID{b[6:]}

	// Only support the ATT ReadByGroupReq for GATT Primary Service Discovery.
	// More specifically, the "Discover Primary Services By Service UUID" sub-procedure
	if !t.Equal(attrPrimaryServiceUUID) {
		return attErrorResp(attrOpFindByTypeValueReq, start, AttrECodeAttrNotFound)
	}

	w := newL2capWriter(c.mtu)
	w.WriteByteFit(attrOpFindByTypeValueResp)

	var wrote bool
	for _, a := range c.attrs.Subrange(start, end) {
		if !a.typ.Equal(attrPrimaryServiceUUID) {
			continue
		}
		if !(UUID{a.value}.Equal(u)) {
			continue
		}
		s := a.pvt.(*Service)
		w.Chunk()
		w.WriteUint16Fit(s.h)
		w.WriteUint16Fit(s.endh)
		if ok := w.Commit(); !ok {
			break
		}
		wrote = true
	}
	if !wrote {
		return attErrorResp(attrOpFindByTypeValueReq, start, AttrECodeAttrNotFound)
	}

	return w.Bytes()
}

// REQ: ReadByType(0x08), StartHandle, EndHandle, Type(UUID)
// RSP: ReadByType(0x09), LenOfEachDataField, DataField, DataField, ...
func (c *central) handleReadByType(ctx context.Context, b []byte) []byte {
	start, end := readHandleRange(b[:4])
	t := UUID{b[4:]}

	w := newL2capWriter(c.mtu)
	w.WriteByteFit(attrOpReadByTypeResp)
	uuidLen := -1
	for _, a := range c.attrs.Subrange(start, end) {
		if !a.typ.Equal(t) {
			continue
		}
		if (a.secure&CharRead) != 0 && c.security > securityLow {
			return attErrorResp(attrOpReadByTypeReq, start, AttrECodeAuthentication)
		}
		v := a.value
		if v == nil {
			resp := newResponseWriter(int(c.mtu - 1))
			req := &ReadRequest{
				Request: Request{Central: c},
				Cap:     int(c.mtu - 1),
				Offset:  0,
			}
			if c, ok := a.pvt.(*Characteristic); ok {
				c.readHandler.ServeRead(ctx, resp, req)
			} else if d, ok := a.pvt.(*Descriptor); ok {
				d.readHandler.ServeRead(ctx, resp, req)
			}
			v = resp.bytes()
		}
		if uuidLen == -1 {
			uuidLen = len(v)
			w.WriteByteFit(byte(uuidLen) + 2)
		}
		if len(v) != uuidLen {
			break
		}
		w.Chunk()
		w.WriteUint16Fit(a.h)
		w.WriteFit(v)
		if ok := w.Commit(); !ok {
			break
		}
	}
	if uuidLen == -1 {
		return attErrorResp(attrOpReadByTypeReq, start, AttrECodeAttrNotFound)
	}
	return w.Bytes()
}

// REQ: ReadReq(0x0A), Handle
// RSP: ReadRsp(0x0B), Value
func (c *central) handleRead(ctx context.Context, b []byte) []byte {
	h := binary.LittleEndian.Uint16(b)
	a, ok := c.attrs.At(h)
	if !ok {
		return attErrorResp(attrOpReadReq, h, AttrECodeInvalidHandle)
	}
	if a.props&CharRead == 0 {
		return attErrorResp(attrOpReadReq, h, AttrECodeReadNotPerm)
	}
	if a.secure&CharRead != 0 && c.security > securityLow {
		return attErrorResp(attrOpReadReq, h, AttrECodeAuthentication)
	}
	v := a.value
	if v == nil {
		req := &ReadRequest{
			Request: Request{Central: c},
			Cap:     int(c.mtu - 1),
			Offset:  0,
		}
		resp := newResponseWriter(int(c.mtu - 1))
		if c, ok := a.pvt.(*Characteristic); ok {
			c.readHandler.ServeRead(ctx, resp, req)
		} else if d, ok := a.pvt.(*Descriptor); ok {
			d.readHandler.ServeRead(ctx, resp, req)
		}
		v = resp.bytes()
	}

	w := newL2capWriter(c.mtu)
	w.WriteByteFit(attrOpReadResp)
	w.Chunk()
	w.WriteFit(v)
	w.CommitFit()
	return w.Bytes()
}

// FIXME: check this, untested, might be broken
func (c *central) handleReadBlob(ctx context.Context, b []byte) []byte {
	h := binary.LittleEndian.Uint16(b)
	offset := binary.LittleEndian.Uint16(b[2:])
	a, ok := c.attrs.At(h)
	if !ok {
		return attErrorResp(attrOpReadBlobReq, h, AttrECodeInvalidHandle)
	}
	if a.props&CharRead == 0 {
		return attErrorResp(attrOpReadBlobReq, h, AttrECodeReadNotPerm)
	}
	if a.secure&CharRead != 0 && c.security > securityLow {
		return attErrorResp(attrOpReadBlobReq, h, AttrECodeAuthentication)
	}
	v := a.value
	if v == nil {
		req := &ReadRequest{
			Request: Request{Central: c},
			Cap:     int(c.mtu - 1),
			Offset:  int(offset),
		}
		resp := newResponseWriter(int(c.mtu - 1))
		if c, ok := a.pvt.(*Characteristic); ok {
			c.readHandler.ServeRead(ctx, resp, req)
		} else if d, ok := a.pvt.(*Descriptor); ok {
			d.readHandler.ServeRead(ctx, resp, req)
		}
		v = resp.bytes()
		offset = 0 // the server has already adjusted for the offset
	}
	w := newL2capWriter(c.mtu)
	w.WriteByteFit(attrOpReadBlobResp)
	w.Chunk()
	w.WriteFit(v)
	if ok := w.ChunkSeek(offset); !ok {
		return attErrorResp(attrOpReadBlobReq, h, AttrECodeInvalidOffset)
	}
	w.CommitFit()
	return w.Bytes()
}

func (c *central) handleReadByGroup(b []byte) []byte {
	start, end := readHandleRange(b)
	t := UUID{b[4:]}

	// Only support the ATT ReadByGroupReq for GATT Primary Service Discovery.
	// More specifically, the "Discover All Primary Services" sub-procedure.
	if !t.Equal(attrPrimaryServiceUUID) {
		return attErrorResp(attrOpReadByGroupReq, start, AttrECodeUnsuppGrpType)
	}

	w := newL2capWriter(c.mtu)
	w.WriteByteFit(attrOpReadByGroupResp)
	uuidLen := -1
	for _, a := range c.attrs.Subrange(start, end) {
		if !a.typ.Equal(attrPrimaryServiceUUID) {
			continue
		}
		if uuidLen == -1 {
			uuidLen = len(a.value)
			w.WriteByteFit(byte(uuidLen + 4))
		}
		if uuidLen != len(a.value) {
			break
		}
		s := a.pvt.(*Service)
		w.Chunk()
		w.WriteUint16Fit(s.h)
		w.WriteUint16Fit(s.endh)
		w.WriteFit(a.value)
		if ok := w.Commit(); !ok {
			break
		}
	}
	if uuidLen == -1 {
		return attErrorResp(attrOpReadByGroupReq, start, AttrECodeAttrNotFound)
	}
	return w.Bytes()
}

func (c *central) handleWrite(ctx context.Context, reqType byte, b []byte) []byte {
	h := binary.LittleEndian.Uint16(b[:2])
	value := b[2:]

	a, ok := c.attrs.At(h)
	if !ok {
		return attErrorResp(reqType, h, AttrECodeInvalidHandle)
	}

	noResp := reqType == attrOpWriteCmd
	charFlag := CharWrite
	if noResp {
		charFlag = CharWriteNR
	}
	if a.props&charFlag == 0 {
		return attErrorResp(reqType, h, AttrECodeWriteNotPerm)
	}
	if a.secure&charFlag == 0 && c.security > securityLow {
		return attErrorResp(reqType, h, AttrECodeAuthentication)
	}

	// Props of Service and Characteristic declarations are read only.
	// So we only need deal with writable descriptors here.
	// (Characteristic's value is implemented with descriptor)
	if !a.typ.Equal(attrClientCharacteristicConfigUUID) {
		// Regular write, not CCC
		r := Request{Central: c}
		result := byte(0)
		if c, ok := a.pvt.(*Characteristic); ok {
			result = c.writeHandler.ServeWrite(ctx, r, value)
		} else if d, ok := a.pvt.(*Characteristic); ok {
			result = d.writeHandler.ServeWrite(ctx, r, value)
		}
		if noResp {
			return nil
		} else {
			resultEcode := AttrECode(result)
			if resultEcode == AttrECodeSuccess {
				return []byte{attrOpWriteResp}
			} else {
				return attErrorResp(reqType, h, resultEcode)
			}
		}
	}

	// CCC/descriptor write
	if len(value) != 2 {
		return attErrorResp(reqType, h, AttrECodeInvalAttrValueLen)
	}
	ccc := binary.LittleEndian.Uint16(value)
	// char := a.pvt.(*Descriptor).char
	if ccc&(gattCCCNotifyFlag|gattCCCIndicateFlag) != 0 {
		c.startNotify(ctx, &a, int(c.mtu-3))
	} else {
		c.stopNotify(&a)
	}
	if noResp {
		return nil
	}
	return []byte{attrOpWriteResp}
}

func (c *central) sendNotification(_ context.Context, a *attr, data []byte) (int, error) {
	w := newL2capWriter(c.mtu)
	added := 0
	if w.WriteByteFit(attrOpHandleNotify) {
		added += 1
	}
	if w.WriteUint16Fit(a.pvt.(*Descriptor).char.vh) {
		added += 2
	}
	w.WriteFit(data)
	n, err := c.l2Conn.Write(w.Bytes())
	if err != nil {
		return n, err
	}
	return n - added, err
}

func readHandleRange(b []byte) (start, end uint16) {
	return binary.LittleEndian.Uint16(b), binary.LittleEndian.Uint16(b[2:])
}

func (c *central) startNotify(ctx context.Context, a *attr, maxLen int) {
	c.notifiersMutex.Lock()
	defer c.notifiersMutex.Unlock()
	if _, found := c.notifiers[a.h]; found {
		return
	}
	char := a.pvt.(*Descriptor).char
	n := newNotifier(c, a, maxLen)
	c.notifiers[a.h] = n
	go char.notifyHandler.ServeNotify(ctx, Request{Central: c}, n)
}

func (c *central) stopNotify(a *attr) {
	c.notifiersMutex.Lock()
	defer c.notifiersMutex.Unlock()
	// char := a.pvt.(*Characteristic)
	if n, found := c.notifiers[a.h]; found {
		n.stop()
		delete(c.notifiers, a.h)
	}
}
