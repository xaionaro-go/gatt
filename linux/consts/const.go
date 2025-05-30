package consts

type PacketType uint8

// HCI Packet types
const (
	PacketTypeCommand = PacketType(0x01)
	PacketTypeACLData = PacketType(0x02)
	PacketTypeSCOData = PacketType(0x03)
	PacketTypeEvent   = PacketType(0x04)
	PacketTypeVendor  = PacketType(0xFF)
)

// Event Type
// TODO: explain: is "Adv" "Advertising", "Advertise" or something else; what is "ind"?
const (
	AdvInd        = 0x00 // Connectable undirected advertising (ADV_IND).
	AdvDirectInd  = 0x01 // Connectable directed advertising (ADV_DIRECT_IND)
	AdvScanInd    = 0x02 // Scannable undirected advertising (ADV_SCAN_IND)
	AdvNonconnInd = 0x03 // Non connectable undirected advertising (ADV_NONCONN_IND)
	ScanResp      = 0x04 // Scan Response (SCAN_RSP)
)
