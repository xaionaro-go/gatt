package gatt

// This file includes constants from the BLE spec.

var (
	attrGAPUUID  = UUID16(0x1800)
	attrGATTUUID = UUID16(0x1801)

	attrPrimaryServiceUUID   = UUID16(0x2800)
	attrSecondaryServiceUUID = UUID16(0x2801)
	attrIncludeUUID          = UUID16(0x2802)
	attrCharacteristicUUID   = UUID16(0x2803)

	attrClientCharacteristicConfigUUID = UUID16(0x2902)
	attrServerCharacteristicConfigUUID = UUID16(0x2903)

	attrDeviceNameUUID        = UUID16(0x2A00)
	attrAppearanceUUID        = UUID16(0x2A01)
	attrPeripheralPrivacyUUID = UUID16(0x2A02)
	attrReconnectionAddrUUID  = UUID16(0x2A03)
	attrPreferredParamsUUID   = UUID16(0x2A04)
	attrServiceChangedUUID    = UUID16(0x2A05)
)

const (
	gattCCCNotifyFlag   = 0x0001
	gattCCCIndicateFlag = 0x0002
)

const (
	attrOpError               = 0x01
	attrOpMtuReq              = 0x02
	attrOpMtuResp             = 0x03
	attrOpFindInfoReq         = 0x04
	attrOpFindInfoResp        = 0x05
	attrOpFindByTypeValueReq  = 0x06
	attrOpFindByTypeValueResp = 0x07
	attrOpReadByTypeReq       = 0x08
	attrOpReadByTypeResp      = 0x09
	attrOpReadReq             = 0x0a
	attrOpReadResp            = 0x0b
	attrOpReadBlobReq         = 0x0c
	attrOpReadBlobResp        = 0x0d
	attrOpReadMultiReq        = 0x0e
	attrOpReadMultiResp       = 0x0f
	attrOpReadByGroupReq      = 0x10
	attrOpReadByGroupResp     = 0x11
	attrOpWriteReq            = 0x12
	attrOpWriteResp           = 0x13
	attrOpWriteCmd            = 0x52
	attrOpPrepWriteReq        = 0x16
	attrOpPrepWriteResp       = 0x17
	attrOpExecWriteReq        = 0x18
	attrOpExecWriteResp       = 0x19
	attrOpHandleNotify        = 0x1b
	attrOpHandleInd           = 0x1d
	attrOpHandleCnf           = 0x1e
	attrOpSignedWriteCmd      = 0xd2
)

type AttrECode byte

const (
	AttrECodeSuccess           AttrECode = 0x00 // Success
	AttrECodeInvalidHandle     AttrECode = 0x01 // The attribute handle given was not valid on this server.
	AttrECodeReadNotPerm       AttrECode = 0x02 // The attribute cannot be read.
	AttrECodeWriteNotPerm      AttrECode = 0x03 // The attribute cannot be written.
	AttrECodeInvalidPDU        AttrECode = 0x04 // The attribute PDU was invalid.
	AttrECodeAuthentication    AttrECode = 0x05 // The attribute requires authentication before it can be read or written.
	AttrECodeReqNotSupp        AttrECode = 0x06 // Attribute server does not support the request received from the client.
	AttrECodeInvalidOffset     AttrECode = 0x07 // Offset specified was past the end of the attribute.
	AttrECodeAuthorization     AttrECode = 0x08 // The attribute requires authorization before it can be read or written.
	AttrECodePrepQueueFull     AttrECode = 0x09 // Too many prepare writes have been queued.
	AttrECodeAttrNotFound      AttrECode = 0x0a // No attribute found within the given attribute handle range.
	AttrECodeAttrNotLong       AttrECode = 0x0b // The attribute cannot be read or written using the Read Blob Request.
	AttrECodeInsuffEncrKeySize AttrECode = 0x0c // The Encryption Key Size used for encrypting this link is insufficient.
	AttrECodeInvalAttrValueLen AttrECode = 0x0d // The attribute value length is invalid for the operation.
	AttrECodeUnlikely          AttrECode = 0x0e // The attribute request that was requested has encountered an error that was unlikely, and therefore could not be completed as requested.
	AttrECodeInsuffEnc         AttrECode = 0x0f // The attribute requires encryption before it can be read or written.
	AttrECodeUnsuppGrpType     AttrECode = 0x10 // The attribute type is not a supported grouping attribute as defined by a higher layer specification.
	AttrECodeInsuffResources   AttrECode = 0x11 // Insufficient Resources to complete the request.
)

func (a AttrECode) Error() string {
	switch i := int(a); {
	case i < 0x11:
		return AttrECodeName[a]
	case i >= 0x12 && i <= 0x7F: // Reserved for future use
		return "reserved error code"
	case i >= 0x80 && i <= 0x9F: // Application Error, defined by higher level
		return "reserved error code"
	case i >= 0xA0 && i <= 0xDF: // Reserved for future use
		return "reserved error code"
	case i >= 0xE0 && i <= 0xFF: // Common profile and service error codes
		return "profile or service error"
	default: // can't happen, just make compiler happy
		return "unknown error"
	}
}

var AttrECodeName = map[AttrECode]string{
	AttrECodeSuccess:           "success",
	AttrECodeInvalidHandle:     "invalid handle",
	AttrECodeReadNotPerm:       "read not permitted",
	AttrECodeWriteNotPerm:      "write not permitted",
	AttrECodeInvalidPDU:        "invalid PDU",
	AttrECodeAuthentication:    "insufficient authentication",
	AttrECodeReqNotSupp:        "request not supported",
	AttrECodeInvalidOffset:     "invalid offset",
	AttrECodeAuthorization:     "insufficient authorization",
	AttrECodePrepQueueFull:     "prepare queue full",
	AttrECodeAttrNotFound:      "attribute not found",
	AttrECodeAttrNotLong:       "attribute not long",
	AttrECodeInsuffEncrKeySize: "insufficient encryption key size",
	AttrECodeInvalAttrValueLen: "invalid attribute value length",
	AttrECodeUnlikely:          "unlikely error",
	AttrECodeInsuffEnc:         "insufficient encryption",
	AttrECodeUnsuppGrpType:     "unsupported group type",
	AttrECodeInsuffResources:   "insufficient resources",
}

func attErrorResp(op byte, h uint16, s AttrECode) []byte {
	return attrErr{opCode: op, attr: h, status: s}.Marshal()
}

// attRespFor maps from att request
// codes to att response codes.
var attRespFor = map[byte]byte{
	attrOpMtuReq:             attrOpMtuResp,
	attrOpFindInfoReq:        attrOpFindInfoResp,
	attrOpFindByTypeValueReq: attrOpFindByTypeValueResp,
	attrOpReadByTypeReq:      attrOpReadByTypeResp,
	attrOpReadReq:            attrOpReadResp,
	attrOpReadBlobReq:        attrOpReadBlobResp,
	attrOpReadMultiReq:       attrOpReadMultiResp,
	attrOpReadByGroupReq:     attrOpReadByGroupResp,
	attrOpWriteReq:           attrOpWriteResp,
	attrOpPrepWriteReq:       attrOpPrepWriteResp,
	attrOpExecWriteReq:       attrOpExecWriteResp,
}

type attrErr struct {
	opCode uint8
	attr   uint16
	status AttrECode
}

// TODO: Reformulate in a way that lets the caller avoid allocs.
// Accept a []byte? Write directly to an io.Writer?
func (e attrErr) Marshal() []byte {
	// little-endian encoding for attr
	return []byte{attrOpError, e.opCode, byte(e.attr), byte(e.attr >> 8), byte(e.status)}
}
