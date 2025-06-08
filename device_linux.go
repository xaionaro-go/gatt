package gatt

import (
	"context"
	"encoding/binary"
	"net"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/ctxflow"
	"github.com/xaionaro-go/gatt/linux"
	"github.com/xaionaro-go/gatt/linux/cmd"
)

type device struct {
	deviceHandler

	mainLoop ctxflow.StartStopper[ctxflow.StartStopperBackendFuncs]
	scanner  ctxflow.StartStopper[ctxflow.StartStopperBackendFuncs]

	hci   *linux.HCI
	state State

	// All the following fields are only used peripheralManager (server) implementation.
	services []*Service
	attrs    *attrRange

	devID   int
	chkLE   bool
	maxConn int

	advData   *cmd.LESetAdvertisingData
	scanResp  *cmd.LESetScanResponseData
	advParam  *cmd.LESetAdvertisingParameters
	scanParam *cmd.LESetScanParameters
}

func NewDevice(ctx context.Context, opts ...Option) (Device, error) {
	d := &device{
		maxConn: 1,    // Support 1 connection at a time.
		devID:   -1,   // Find an available HCI device.
		chkLE:   true, // Check if the device supports LE.

		advParam: &cmd.LESetAdvertisingParameters{
			AdvertisingIntervalMin:  0x800,     // [0x0800]: 0.625 ms * 0x0800 = 1280.0 ms
			AdvertisingIntervalMax:  0x800,     // [0x0800]: 0.625 ms * 0x0800 = 1280.0 ms
			AdvertisingType:         0x00,      // [0x00]: ADV_IND, 0x01: DIRECT(HIGH), 0x02: SCAN, 0x03: NONCONN, 0x04: DIRECT(LOW)
			OwnAddressType:          0x00,      // [0x00]: public, 0x01: random
			DirectAddressType:       0x00,      // [0x00]: public, 0x01: random
			DirectAddress:           [6]byte{}, // Public or Random Address of the device to be connected
			AdvertisingChannelMap:   0x7,       // [0x07] 0x01: ch37, 0x2: ch38, 0x4: ch39
			AdvertisingFilterPolicy: 0x00,
		},
		scanParam: &cmd.LESetScanParameters{
			LEScanType:           0x01,   // [0x00]: passive, 0x01: active
			LEScanInterval:       0x0010, // [0x10]: 0.625ms * 16
			LEScanWindow:         0x0010, // [0x10]: 0.625ms * 16
			OwnAddressType:       0x00,   // [0x00]: public, 0x01: random
			ScanningFilterPolicy: 0x00,   // [0x00]: accept all, 0x01: ignore non-allow-listed.
		},
	}

	d.mainLoop = ctxflow.StartStopper[ctxflow.StartStopperBackendFuncs]{
		StartStopper: ctxflow.StartStopperBackendFuncs{
			StartFunc: d.doStartMainLoop,
			StopFunc:  d.doStopMainLoop,
		},
	}
	d.scanner = ctxflow.StartStopper[ctxflow.StartStopperBackendFuncs]{
		StartStopper: ctxflow.StartStopperBackendFuncs{
			StartFunc: d.doStartScanning,
			StopFunc:  d.doStopScanning,
		},
	}

	d.Option(opts...)
	h, err := linux.NewHCI(ctx, d.devID, d.chkLE, d.maxConn)
	if err != nil {
		return nil, err
	}

	d.hci = h
	return d, nil
}

func (d *device) Start(ctx context.Context, f func(context.Context, Device, State)) error {
	return d.mainLoop.Start(ctx, f)
}

func (d *device) Stop() error {
	return d.mainLoop.Stop()
}

func (d *device) doStartMainLoop(ctx context.Context, args ...any) error {
	f := args[0].(func(context.Context, Device, State))

	d.hci.AcceptMasterHandler = func(pd *linux.PlatData) {
		addr := pd.Address
		central := newCentral(d.attrs, net.HardwareAddr([]byte{addr[5], addr[4], addr[3], addr[2], addr[1], addr[0]}), pd.Conn)
		if d.centralConnected != nil {
			d.centralConnected(ctx, central)
		}
		central.loop(ctx)
		if d.centralDisconnected != nil {
			d.centralDisconnected(ctx, central)
		}
	}
	d.hci.AcceptSlaveHandler = func(pd *linux.PlatData) {
		logger.Tracef(ctx, "d.hci.AcceptSlaveHandler()")
		defer func() { logger.Tracef(ctx, "/d.hci.AcceptSlaveHandler()") }()
		periph := &peripheral{
			d:      d,
			pd:     pd,
			l2c:    pd.Conn,
			reqCh:  make(chan message),
			quitCh: make(chan struct{}),
			sub:    newSubscriber(),
		}
		if d.peripheralConnected != nil {
			go d.peripheralConnected(ctx, periph, nil)
		}
		periph.loop(ctx)
		if d.peripheralDisconnected != nil {
			d.peripheralDisconnected(ctx, periph, nil)
		}
	}
	d.hci.AdvertisementHandler = func(pd *linux.PlatData) {
		adv := &Advertisement{}
		adv.unmarshall(pd.Data)
		adv.Connectable = pd.Connectable
		periph := &peripheral{pd: pd, d: d}
		if d.peripheralDiscovered != nil {
			pd.Name = adv.LocalName
			d.peripheralDiscovered(ctx, periph, adv, int(pd.RSSI))
		}
	}
	d.state = StatePoweredOn
	d.stateChanged = f
	go d.stateChanged(ctx, d, d.state)
	return nil
}

func (d *device) doStopMainLoop(ctx context.Context) error {
	d.state = StatePoweredOff
	defer d.stateChanged(ctx, d, d.state)
	return d.hci.Close()
}

func (d *device) ID() int {
	return d.devID
}

func (d *device) AddService(ctx context.Context, s *Service) error {
	d.services = append(d.services, s)
	d.attrs = generateAttributes(ctx, d.services, uint16(1)) // ble attrs start at 1
	return nil
}

func (d *device) RemoveAllServices(ctx context.Context) error {
	d.services = nil
	d.attrs = nil
	return nil
}

func (d *device) SetServices(ctx context.Context, s []*Service) error {
	d.RemoveAllServices(ctx)
	d.services = append(d.services, s...)
	d.attrs = generateAttributes(ctx, d.services, uint16(1)) // ble attrs start at 1
	return nil
}

func (d *device) Advertise(ctx context.Context, a *AdvPacket) error {
	d.advData = &cmd.LESetAdvertisingData{
		AdvertisingDataLength: uint8(a.Len()),
		AdvertisingData:       a.Bytes(),
	}

	if err := d.update(ctx); err != nil {
		return err
	}

	return d.hci.SetAdvertiseEnable(ctx, true)
}

func (d *device) AdvertiseNameAndServices(ctx context.Context, name string, uu []UUID) error {
	a := &AdvPacket{}
	a.AppendFlags(flagGeneralDiscoverable | flagLEOnly)
	a.AppendUUIDFit(uu)

	if len(a.b)+len(name)+2 < MaxEIRPacketLength {
		a.AppendName(name)
		d.scanResp = nil
	} else {
		a := &AdvPacket{}
		a.AppendName(name)
		d.scanResp = &cmd.LESetScanResponseData{
			ScanResponseDataLength: uint8(a.Len()),
			ScanResponseData:       a.Bytes(),
		}
	}

	return d.Advertise(ctx, a)
}

func (d *device) AdvertiseNameAndIBeaconData(ctx context.Context, name string, b []byte) error {
	a := &AdvPacket{}
	a.AppendFlags(flagGeneralDiscoverable | flagLEOnly)
	a.AppendManufacturerData(0x004C, b)

	if len(a.b)+len(name)+2 < MaxEIRPacketLength {
		a.AppendName(name)
		d.scanResp = nil
	} else {
		a := &AdvPacket{}
		a.AppendName(name)
		d.scanResp = &cmd.LESetScanResponseData{
			ScanResponseDataLength: uint8(a.Len()),
			ScanResponseData:       a.Bytes(),
		}
	}

	return d.Advertise(ctx, a)
}

func (d *device) AdvertiseIBeaconData(ctx context.Context, b []byte) error {
	a := &AdvPacket{}
	a.AppendFlags(flagGeneralDiscoverable | flagLEOnly)
	a.AppendManufacturerData(0x004C, b)

	return d.Advertise(ctx, a)
}

func (d *device) AdvertiseIBeacon(ctx context.Context, u UUID, major, minor uint16, pwr int8) error {
	b := make([]byte, 23)
	b[0] = 0x02                               // Data type: iBeacon
	b[1] = 0x15                               // Data length: 21 bytes
	copy(b[2:], reverse(u.b))                 // Big endian
	binary.BigEndian.PutUint16(b[18:], major) // Big endian
	binary.BigEndian.PutUint16(b[20:], minor) // Big endian
	b[22] = uint8(pwr)                        // Measured Tx Power
	return d.AdvertiseIBeaconData(ctx, b)
}

func (d *device) StopAdvertising(ctx context.Context) error {
	return d.hci.SetAdvertiseEnable(ctx, false)
}

func (d *device) Scan(ctx context.Context, services []UUID, dup bool) error {
	return d.scanner.Start(ctx, services, dup)
}

func (d *device) StopScanning() error {
	return d.scanner.Stop()
}

func (d *device) doStartScanning(ctx context.Context, args ...any) error {
	_, dup := args[0].([]UUID), args[1].(bool)
	return d.hci.SetScanEnable(ctx, true, dup)
}

func (d *device) doStopScanning(ctx context.Context) error {
	return d.hci.SetScanEnable(ctx, false, true)
}

func (d *device) Connect(ctx context.Context, p Peripheral) {
	d.hci.Connect(ctx, p.(*peripheral).pd)
}

func (d *device) CancelConnection(ctx context.Context, p Peripheral) {
	d.hci.CancelConnection(ctx, p.(*peripheral).pd)
}

func (d *device) SendHCIRawCommand(ctx context.Context, c cmd.CmdParam) ([]byte, error) {
	return d.hci.SendRawCommand(ctx, c)
}

// Flush pending advertising settings to the device.
func (d *device) update(ctx context.Context) error {
	if d.advParam != nil {
		if err := d.hci.SendCmdWithAdvOff(ctx, d.advParam); err != nil {
			return err
		}
		d.advParam = nil
	}
	if d.scanResp != nil {
		if err := d.hci.SendCmdWithAdvOff(ctx, d.scanResp); err != nil {
			return err
		}
		d.scanResp = nil
	}
	if d.advData != nil {
		if err := d.hci.SendCmdWithAdvOff(ctx, d.advData); err != nil {
			return err
		}
		d.advData = nil
	}
	return nil
}
