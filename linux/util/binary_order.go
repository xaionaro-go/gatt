package util

import "encoding/binary"

type binaryOrder struct{ binary.ByteOrder }

var BinaryOrder = binaryOrder{binary.LittleEndian}

func (o binaryOrder) Int8(b []byte) int8   { return int8(b[0]) }
func (o binaryOrder) Uint8(b []byte) uint8 { return b[0] }
func (o binaryOrder) MAC(b []byte) [6]byte { return [6]byte{b[5], b[4], b[3], b[2], b[1], b[0]} }

func (o binaryOrder) PutUint8(b []byte, v uint8) { b[0] = v }
func (o binaryOrder) PutMAC(b []byte, m [6]byte) {
	b[0], b[1], b[2], b[3], b[4], b[5] = m[5], m[4], m[3], m[2], m[1], m[0]
}
