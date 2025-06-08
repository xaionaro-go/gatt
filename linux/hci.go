package linux

import (
	"context"
	"fmt"
	"io"
	"sync"

	"golang.org/x/sys/unix"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/gatt/linux/cmd"
	"github.com/xaionaro-go/gatt/linux/consts"
	"github.com/xaionaro-go/gatt/linux/event"
	"github.com/xaionaro-go/gatt/linux/util"
)

type HCI struct {
	AcceptMasterHandler  func(pd *PlatData)
	AcceptSlaveHandler   func(pd *PlatData)
	AdvertisementHandler func(pd *PlatData)

	device io.ReadWriteCloser
	cmd    *cmd.Cmd
	event  *event.Event

	plist      map[bdaddr]*PlatData
	plistMutex *sync.Mutex

	bufCnt  chan struct{}
	bufSize int

	pool     *util.BytePool
	loopDone chan bool

	maxConn    int
	connsMutex *sync.Mutex
	conns      map[uint16]*conn

	adv      bool
	advMutex *sync.Mutex
}

type bdaddr [6]byte

type PlatData struct {
	Name        string
	AddressType uint8
	Address     [6]byte
	Data        []byte
	Connectable bool
	RSSI        int8

	Conn io.ReadWriteCloser
}

func NewHCI(ctx context.Context, devID int, chk bool, maxConn int) (*HCI, error) {
	device, err := newDevice(ctx, devID, chk)
	if err != nil {
		return nil, err
	}
	cmd := cmd.NewCmd(ctx, device)
	ev := event.NewEvent()

	h := &HCI{
		device: device,
		cmd:    cmd,
		event:  ev,

		plist:      make(map[bdaddr]*PlatData),
		plistMutex: &sync.Mutex{},

		bufCnt:  make(chan struct{}, 15-1),
		bufSize: 27,

		pool:     util.NewBytePool(4096, 16),
		loopDone: make(chan bool),

		maxConn:    maxConn,
		connsMutex: &sync.Mutex{},
		conns:      map[uint16]*conn{},

		advMutex: &sync.Mutex{},
	}

	ev.HandleEvent(event.LEMeta, event.HandlerFunc(h.handleLEMeta))
	ev.HandleEvent(event.DisconnectionComplete, event.HandlerFunc(h.handleDisconnectionComplete))
	ev.HandleEvent(event.NumberOfCompletedPkts, event.HandlerFunc(h.handleNumberOfCompletedPkts))
	ev.HandleEvent(event.CommandComplete, event.HandlerFunc(cmd.HandleComplete))
	ev.HandleEvent(event.CommandStatus, event.HandlerFunc(cmd.HandleStatus))

	go h.mainLoop(ctx)
	h.resetDevice(ctx)
	return h, nil
}

func (h *HCI) Close() error {
	ctx := context.TODO()
	logger.Debugf(ctx, "hci.Close()")
	h.pool.Close()
	<-h.loopDone
	logger.Debugf(ctx, "mainLoop exited")
	for _, c := range h.conns {
		logger.Debugf(ctx, "closing connection %v", c)
		c.Close()
	}
	logger.Debugf(ctx, "closing %v", h.device)
	return h.device.Close()
}

func (h *HCI) SetAdvertiseEnable(ctx context.Context, en bool) error {
	h.advMutex.Lock()
	h.adv = en
	h.advMutex.Unlock()
	return h.setAdvertiseEnable(ctx, en)
}

func (h *HCI) setAdvertiseEnable(ctx context.Context, en bool) error {
	h.advMutex.Lock()
	defer h.advMutex.Unlock()
	if en && h.adv && (len(h.conns) == h.maxConn) {
		return nil
	}
	return h.cmd.SendAndCheckResp(ctx, cmd.LESetAdvertiseEnable{
		AdvertisingEnable: btoi(en),
	}, []byte{0x00})
}

func (h *HCI) SendCmdWithAdvOff(ctx context.Context, c cmd.CmdParam) error {
	h.setAdvertiseEnable(ctx, false)
	err := h.cmd.SendAndCheckResp(ctx, c, nil)
	if h.adv {
		h.setAdvertiseEnable(ctx, true)
	}
	return err
}

func (h *HCI) SetScanEnable(ctx context.Context, en bool, dup bool) error {
	return h.cmd.SendAndCheckResp(
		ctx,
		cmd.LESetScanEnable{
			LEScanEnable:     btoi(en),
			FilterDuplicates: btoi(!dup),
		}, []byte{0x00})
}

func (h *HCI) Connect(ctx context.Context, pd *PlatData) error {
	h.cmd.Send(ctx, cmd.LECreateConn{
		LEScanInterval:        0x0004,         // N x 0.625ms
		LEScanWindow:          0x0004,         // N x 0.625ms
		InitiatorFilterPolicy: 0x00,           // allow list not used
		PeerAddressType:       pd.AddressType, // public or random
		PeerAddress:           pd.Address,     //
		OwnAddressType:        0x00,           // public
		ConnIntervalMin:       0x0006,         // N x 0.125ms
		ConnIntervalMax:       0x0006,         // N x 0.125ms
		ConnLatency:           0x0000,         //
		SupervisionTimeout:    0x0048,         // N x 10ms
		MinimumCELength:       0x0000,         // N x 0.625ms
		MaximumCELength:       0x0000,         // N x 0.625ms
	})
	return nil
}

func (h *HCI) CancelConnection(ctx context.Context, pd *PlatData) (_err error) {
	logger.Debugf(ctx, "CancelConnection")
	defer func() { logger.Debugf(ctx, "/CancelConnection: %v", _err) }()
	if pd != nil && pd.Conn != nil {
		return pd.Conn.Close()
	}
	return nil
}

func (h *HCI) SendRawCommand(ctx context.Context, c cmd.CmdParam) ([]byte, error) {
	return h.cmd.Send(ctx, c)
}

func btoi(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

func (h *HCI) mainLoop(ctx context.Context) {
	logger.Debugf(ctx, "hci.mainLoop started")
	defer func() {
		h.loopDone <- true
	}()

	for {
		b := h.pool.Get()
		if b == nil {
			logger.Debugf(ctx, "got nil buffer, breaking mainLoop")
			break
		}
		logger.Tracef(ctx, "h.device.Read")
		n, err := h.device.Read(b)
		logger.Tracef(ctx, "/h.device.Read: %v %X", err, b[:n])
		if err != nil {
			if err == unix.EINTR {
				continue
			}

			logger.Debugf(ctx, "mainLoop err: %v", err)
			return
		}
		if n > 0 {
			h.handlePacket(ctx, b, n)
		}
	}
	logger.Debugf(ctx, "hci.mainLoop stopped")
}

var ErrVendorNotSupported = fmt.Errorf("vendor packet not supported")

func (h *HCI) handlePacket(ctx context.Context, buf []byte, n int) {
	logger.Tracef(ctx, "handlePacket(ctx, %X)", buf[:n])
	defer func() { logger.Tracef(ctx, "/handlePacket(ctx, %X)", buf[:n]) }()

	b := buf[:n]
	t, b := consts.PacketType(b[0]), b[1:]
	var err error
	handled := true
	switch t {
	case consts.PacketTypeCommand:
		op := uint16(b[0]) | uint16(b[1])<<8
		logger.Debugf(ctx, "unmanaged cmd: opcode (%04x) [ % X ]\n", op, b)
	case consts.PacketTypeACLData:
		err = h.handleL2CAP(ctx, b)
	case consts.PacketTypeSCOData:
		err = fmt.Errorf("SCO packet not supported")
	case consts.PacketTypeEvent:
		handled = false
		go func() {
			err := h.event.Dispatch(ctx, b)
			if err != nil {
				logger.Debugf(ctx, "hci: %s, [ % X]", err, b)
			}
			h.pool.Put(buf)
		}()
	case consts.PacketTypeVendor:
		err = ErrVendorNotSupported
	default:
		logger.Fatalf(ctx, "Unknown event: 0x%02X [ % X ]\n", t, b)
	}
	if err != nil {
		logger.Debugf(ctx, "hci: %s, [ % X]", err, b)
	}
	if handled {
		h.pool.Put(buf)
	}
}

func (h *HCI) resetDevice(ctx context.Context) error {
	// TODO: explain all the magic numbers below:
	seq := []cmd.CmdParam{
		cmd.Reset{},
		cmd.SetEventMask{EventMask: 0x3dbff807fffbffff},
		cmd.LESetEventMask{LEEventMask: 0x000000000000001F},
		cmd.WriteSimplePairingMode{SimplePairingMode: 1},
		cmd.WriteLEHostSupported{LESupportedHost: 1, SimultaneousLEHost: 0},
		cmd.WriteInquiryMode{InquiryMode: 2},
		cmd.WritePageScanType{PageScanType: 1},
		cmd.WriteInquiryScanType{ScanType: 1},
		cmd.WriteDeviceClass{DeviceClass: [3]byte{0x40, 0x02, 0x04}},
		cmd.WritePageTimeout{PageTimeout: 0x2000},
		cmd.WriteDefaultLinkPolicy{DefaultLinkPolicySettings: 0x5},
		cmd.HostBufferSize{
			HostACLDataPacketLength:            0x1000,
			HostSynchronousDataPacketLength:    0xff,
			HostTotalNumACLDataPackets:         0x0014,
			HostTotalNumSynchronousDataPackets: 0x000a,
		},
		cmd.LESetScanParameters{
			LEScanType:           0x01,   // [0x00]: passive, 0x01: active
			LEScanInterval:       0x0010, // [0x10]: 0.625ms * 16
			LEScanWindow:         0x0010, // [0x10]: 0.625ms * 16
			OwnAddressType:       0x00,   // [0x00]: public, 0x01: random
			ScanningFilterPolicy: 0x00,   // [0x00]: accept all, 0x01: ignore non-allow-listed.
		},
	}
	for _, s := range seq {
		if err := h.cmd.SendAndCheckResp(ctx, s, []byte{0x00}); err != nil {
			return err
		}
	}
	return nil
}

func (h *HCI) handleAdvertisement(b []byte) {
	// If no one is interested, don't bother.
	if h.AdvertisementHandler == nil {
		return
	}
	ep := &event.LEAdvertisingReportEP{}
	if err := ep.Unmarshal(b); err != nil {
		return
	}
	for i := range int(ep.NumReports) {
		addr := bdaddr(ep.Address[i])
		et := ep.EventType[i]
		connectable := et == consts.AdvInd || et == consts.AdvDirectInd
		//scannable := et == consts.AdvInd || et == consts.AdvScanInd

		if et == consts.ScanResp {
			h.plistMutex.Lock()
			pd, ok := h.plist[addr]
			h.plistMutex.Unlock()
			if ok {
				pd.Data = append(pd.Data, ep.Data[i]...)
				h.AdvertisementHandler(pd)
			}
			continue
		}

		pd := &PlatData{
			AddressType: ep.AddressType[i],
			Address:     ep.Address[i],
			Data:        ep.Data[i],
			Connectable: connectable,
			RSSI:        ep.RSSI[i],
		}
		h.plistMutex.Lock()
		h.plist[addr] = pd
		h.plistMutex.Unlock()

		h.AdvertisementHandler(pd)
	}
}

func (h *HCI) handleNumberOfCompletedPkts(ctx context.Context, b []byte) error {
	ep := &event.NumberOfCompletedPktsEP{}
	if err := ep.Unmarshal(b); err != nil {
		return err
	}
	for _, r := range ep.Packets {
		for range int(r.NumOfCompletedPkts) {
			<-h.bufCnt
		}
	}
	return nil
}

func (h *HCI) handleConnection(ctx context.Context, b []byte) {
	defer func() {
		if err := recover(); err != nil {
			logger.Debugf(ctx, "error while handling connection: %v", err)
		}
	}()

	ep := &event.LEConnectionCompleteEP{}
	if err := ep.Unmarshal(b); err != nil {
		return // FIXME
	}
	hh := ep.ConnectionHandle
	c := newConn(ctx, h, hh)
	h.connsMutex.Lock()
	h.conns[hh] = c
	h.connsMutex.Unlock()
	h.setAdvertiseEnable(ctx, true)

	// FIXME: sloppiness. This call should be called by the package user once we
	// flesh out the support of l2cap signaling packets (CID:0x0001,0x0005)
	if ep.ConnLatency != 0 || ep.ConnInterval > 0x18 {
		c.updateConnection()
	}

	// master connection
	if ep.Role == 0x01 {
		pd := &PlatData{
			Address: ep.PeerAddress,
			Conn:    c,
		}
		h.AcceptMasterHandler(pd)
		return
	}
	h.plistMutex.Lock()
	if pd := h.plist[ep.PeerAddress]; pd != nil {
		h.plistMutex.Unlock()
		pd.Conn = c
		h.AcceptSlaveHandler(pd)
	} else {
		logger.Debugf(ctx, "HCI: can't find data for %v", ep.PeerAddress)
	}
}

func (h *HCI) handleDisconnectionComplete(ctx context.Context, b []byte) error {
	ep := &event.DisconnectionCompleteEP{}
	if err := ep.Unmarshal(b); err != nil {
		return err
	}
	hh := ep.ConnectionHandle
	h.connsMutex.Lock()
	defer h.connsMutex.Unlock()
	c, found := h.conns[hh]
	if !found {
		// should not happen, just be cautious for now.
		logger.Debugf(ctx, "l2conn: disconnecting a disconnected 0x%04X connection", hh)
		return nil
	}
	delete(h.conns, hh)
	close(c.aclCh)
	h.setAdvertiseEnable(ctx, true)
	return nil
}

func (h *HCI) handleLTKRequest(ctx context.Context, b []byte) {
	ep := &event.LELTKRequestEP{}
	if err := ep.Unmarshal(b); err != nil {
		logger.Debugf(ctx, "ltkrequest: error, parsing request")
		return
	}
	hh := ep.ConnectionHandle
	h.connsMutex.Lock()
	defer h.connsMutex.Unlock()
	_, found := h.conns[hh]
	if !found {
		// should not happen, just be cautious for now.
		logger.Debugf(ctx, "ltkrequest: error, connection 0x%04X probably expired", hh)
		return
	}
	h.cmd.Send(ctx, cmd.LELTKNegReply{ConnectionHandle: hh})
	// TODO: implement proper key management
}

func (h *HCI) handleLEMeta(ctx context.Context, b []byte) error {
	defer func() {
		if err := recover(); err != nil {
			logger.Debugf(ctx, "error while handling meta: %v", err)
		}
	}()

	code := event.LEEventCode(b[0])
	switch code {
	case event.LEConnectionComplete:
		go h.handleConnection(ctx, b)
	case event.LEConnectionUpdateComplete:
		// anything to do here?
	case event.LEAdvertisingReport:
		go h.handleAdvertisement(b)
	// case event.LEReadRemoteUsedFeaturesComplete:
	case event.LELTKRequest:
		go h.handleLTKRequest(ctx, b)
	// case event.LERemoteConnectionParameterRequest:
	default:
		return fmt.Errorf("unhandled LE event: 0x%02x, [ % X ]", code, b)
	}
	return nil
}

func (h *HCI) handleL2CAP(ctx context.Context, b []byte) (_err error) {
	logger.Tracef(ctx, "handleL2CAP(ctx, %X)", b)
	defer func() { logger.Tracef(ctx, "/handleL2CAP(ctx, %X): %v", b, _err) }()
	a := &aclData{}
	if err := a.unmarshal(b); err != nil {
		return err
	}
	h.connsMutex.Lock()
	defer h.connsMutex.Unlock()
	c, found := h.conns[a.attr]
	if !found {
		// should not happen, just be cautious for now.
		logger.Debugf(ctx, "l2conn: got data for disconnected handle: 0x%04x", a.attr)
		return nil
	}
	c.aclCh <- a
	return nil
}
