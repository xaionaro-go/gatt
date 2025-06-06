package gatt

import (
	"context"

	"github.com/facebookincubator/go-belt/tool/logger"
)

// attr is a BLE attribute. It is not exported;
// managing attributes is an implementation detail.
type attr struct {
	h      uint16   // attribute handle
	typ    UUID     // attribute type in UUID
	props  Property // attribute property
	secure Property // attribute secure (implementation specific usage)
	value  []byte   // attribute value

	pvt any // point to the corresponding Service/Characteristic/Descriptor; TODO: what "pvt" means exactly?
}

// A attrRange is a contiguous range of attributes.
type attrRange struct {
	aa   []attr // TODO: what is "aa"?
	base uint16 // handle for first attr in aa
}

const (
	tooSmall = -1
	tooLarge = -2
)

// idx returns the index into aa corresponding to attr a.
// If h is too small, idx returns tooSmall (-1).
// If h is too large, idx returns tooLarge (-2).
func (r *attrRange) idx(h int) int {
	if h < int(r.base) {
		return tooSmall
	}
	if int(h) >= int(r.base)+len(r.aa) {
		return tooLarge
	}
	return h - int(r.base)
}

// At returns attr a.
func (r *attrRange) At(h uint16) (a attr, ok bool) {
	i := r.idx(int(h))
	if i < 0 {
		return attr{}, false
	}
	return r.aa[i], true
}

// Subrange returns attributes in range [start, end]; it may
// return an empty slice. Subrange does not panic for
// out-of-range start or end.
func (r *attrRange) Subrange(start, end uint16) []attr {
	startIdx := r.idx(int(start))
	switch startIdx {
	case tooSmall:
		startIdx = 0
	case tooLarge:
		return []attr{}
	}

	endIdx := r.idx(int(end) + 1) // [start, end] includes its upper bound!
	switch endIdx {
	case tooSmall:
		return []attr{}
	case tooLarge:
		endIdx = len(r.aa)
	}
	return r.aa[startIdx:endIdx]
}

func dumpAttributes(ctx context.Context, aa []attr) {
	logger.Debugf(ctx, "Generating attribute table:")
	logger.Debugf(ctx, "handle\ttype\tprops\tsecure\tpvt\tvalue")
	for _, a := range aa {
		logger.Debugf(ctx, "0x%04X\t0x%s\t0x%02X\t0x%02x\t%T\t[ % X ]",
			a.h, a.typ, int(a.props), int(a.secure), a.pvt, a.value)
	}
}

func generateAttributes(ctx context.Context, services []*Service, base uint16) *attrRange {
	var aa []attr
	h := base
	last := len(services) - 1
	for i, s := range services {
		var a []attr
		h, a = generateServiceAttributes(s, h, i == last)
		aa = append(aa, a...)
	}
	dumpAttributes(ctx, aa)
	return &attrRange{aa: aa, base: base}
}

func generateServiceAttributes(s *Service, h uint16, last bool) (uint16, []attr) {
	s.h = h
	// endh set later
	a := attr{
		h:     h,
		typ:   attrPrimaryServiceUUID,
		value: s.uuid.b,
		props: CharRead,
		pvt:   s,
	}
	aa := []attr{a}
	h++

	for _, c := range s.Characteristics() {
		var a []attr
		h, a = generateCharAttributes(c, h)
		aa = append(aa, a...)
	}

	s.endh = h - 1
	if last {
		h = 0xFFFF
		s.endh = h
	}

	return h, aa
}

func generateCharAttributes(c *Characteristic, h uint16) (uint16, []attr) {
	c.h = h
	c.vh = h + 1
	ca := attr{
		h:     c.h,
		typ:   attrCharacteristicUUID,
		value: append([]byte{byte(c.props), byte(c.vh), byte((c.vh) >> 8)}, c.uuid.b...),
		props: c.props,
		pvt:   c,
	}
	va := attr{
		h:     c.vh,
		typ:   c.uuid,
		value: c.value,
		props: c.props,
		pvt:   c,
	}
	h += 2

	aa := []attr{ca, va}
	for _, d := range c.descs {
		aa = append(aa, generateDescAttributes(d, h))
		h++
	}

	return h, aa
}

func generateDescAttributes(d *Descriptor, h uint16) attr {
	d.h = h
	a := attr{
		h:     h,
		typ:   d.uuid,
		value: d.value,
		props: d.props,
		pvt:   d,
	}
	if len(d.valueStr) > 0 {
		a.value = []byte(d.valueStr)
	}
	return a
}
