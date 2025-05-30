package gatt

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"

	"sync"
	"time"

	"github.com/xaionaro-go/gatt/xpc"
)

const (
	peripheralDiscovered   = 37
	peripheralConnected    = 38
	peripheralDisconnected = 40
	// below constants for Yosemite
	rssiRead                   = 55
	includedServicesDiscovered = 63
	serviceDiscovered          = 56
	characteristicsDiscovered  = 64
	characteristicRead         = 71
	characteristicWritten      = 72
	notificationValueSet       = 74
	descriptorsDiscovered      = 76
	descriptorRead             = 79
	descriptorWritten          = 80

	peripheralDiscovered_2 = 48
	peripheralDiscovered_3 = 51
	peripheralDiscovered_4 = 57

	peripheralConnected_2       = 67
	characteristicsDiscovered_2 = 89
)

type device struct {
	deviceHandler

	conn xpc.XPC

	role int // 1: peripheralManager (server), 0: centralManager (client)

	reqc   chan message
	respCh chan message

	// Only used in client/centralManager implementation
	plist      map[string]*peripheral
	plistMutex *sync.Mutex

	// Only used in server/peripheralManager implementation

	attrN int
	attrs map[int]*attr

	subscribers map[string]*central
}

func NewDevice(opts ...Option) (Device, error) {
	d := &device{
		reqc:       make(chan message),
		respCh:     make(chan message),
		plist:      map[string]*peripheral{},
		plistMutex: &sync.Mutex{},

		attrN: 1,
		attrs: make(map[int]*attr),

		subscribers: make(map[string]*central),
	}
	d.Option(opts...)
	d.conn = xpc.XpcConnect("com.apple.blued", d)
	return d, nil
}

func (d *device) Start(ctx context.Context, f func(context.Context, Device, State)) error {
	go d.loop(ctx)
	resp := d.sendReq(ctx, 1, xpc.Dict{
		"kCBMsgArgName":    fmt.Sprintf("gopher-%v", time.Now().Unix()),
		"kCBMsgArgOptions": xpc.Dict{"kCBInitOptionShowPowerAlert": 1},
		"kCBMsgArgType":    d.role,
	})
	d.stateChanged = f
	go d.stateChanged(d, State(resp.MustGetInt("kCBMsgArgState")))
	return nil
}

func (d *device) Stop() error {
	// No Implementation
	defer d.stateChanged(context.TODO(), d, StatePoweredOff)
	return errors.New("FIXME: Advertise error")
}

func (d *device) ID() int {
	return -1
}

func (d *device) Advertise(ctx context.Context, a *AdvPacket) error {
	resp := d.sendReq(ctx, 8, xpc.Dict{
		"kCBAdvDataAppleMfgData": a.b, // not a.Bytes(). should be slice
	})

	if res := resp.MustGetInt("kCBMsgArgResult"); res != 0 {
		return errors.New("FIXME: Advertise error")
	}
	return nil
}

func (d *device) AdvertiseNameAndServices(ctx context.Context, name string, ss []UUID) error {
	us := uuidSlice(ss)
	resp := d.sendReq(ctx, 8, xpc.Dict{
		"kCBAdvDataLocalName":    name,
		"kCBAdvDataServiceUUIDs": us},
	)
	if res := resp.MustGetInt("kCBMsgArgResult"); res != 0 {
		return errors.New("FIXME: Advertise error")
	}
	return nil
}

func (d *device) AdvertiseNameAndIBeaconData(ctx context.Context, name string, b []byte) error {
	return errors.New("Method not supported")
}

func (d *device) AdvertiseIBeaconData(ctx context.Context, data []byte) error {
	var utsName xpc.Utsname
	xpc.Uname(&utsName)

	var resp xpc.Dict

	if utsName.Release >= "14." {
		l := len(data)
		buf := bytes.NewBuffer([]byte{byte(l + 5), 0xFF, 0x4C, 0x00, 0x02, byte(l)})
		buf.Write(data)
		resp = d.sendReq(ctx, 8, xpc.Dict{"kCBAdvDataAppleMfgData": buf.Bytes()})
	} else {
		resp = d.sendReq(ctx, 8, xpc.Dict{"kCBAdvDataAppleBeaconKey": data})
	}

	if res := resp.MustGetInt("kCBMsgArgResult"); res != 0 {
		return errors.New("FIXME: Advertise error")
	}

	return nil
}

func (d *device) AdvertiseIBeacon(ctx context.Context, u UUID, major, minor uint16, pwr int8) error {
	b := make([]byte, 21)
	copy(b, reverse(u.b))                     // Big endian
	binary.BigEndian.PutUint16(b[16:], major) // Big endian
	binary.BigEndian.PutUint16(b[18:], minor) // Big endian
	b[20] = uint8(pwr)                        // Measured Tx Power
	return d.AdvertiseIBeaconData(ctx, b)
}

func (d *device) StopAdvertising(ctx context.Context) error {
	resp := d.sendReq(ctx, 9, nil)
	if res := resp.MustGetInt("kCBMsgArgResult"); res != 0 {
		return errors.New("FIXME: Stop Advertise error")
	}
	return nil
}

func (d *device) RemoveAllServices(ctx context.Context) error {
	d.sendCmd(ctx, 12, nil)
	return nil
}

func (d *device) AddService(ctx context.Context, s *Service) error {
	if s.uuid.Equal(attrGAPUUID) || s.uuid.Equal(attrGATTUUID) {
		// skip GATT and GAP services
		return nil
	}

	xs := xpc.Dict{
		"kCBMsgArgAttributeID":     d.attrN,
		"kCBMsgArgAttributeIDs":    []int{},
		"kCBMsgArgCharacteristics": nil,
		"kCBMsgArgType":            1, // 1 => primary, 0 => excluded
		"kCBMsgArgUUID":            reverse(s.uuid.b),
	}
	d.attrN++

	xcs := xpc.Array{}
	for _, c := range s.Characteristics() {
		props := 0
		perm := 0
		if c.props&CharRead != 0 {
			props |= 0x02
			if CharRead&c.secure != 0 {
				perm |= 0x04
			} else {
				perm |= 0x01
			}
		}
		if c.props&CharWriteNR != 0 {
			props |= 0x04
			if c.secure&CharWriteNR != 0 {
				perm |= 0x08
			} else {
				perm |= 0x02
			}
		}
		if c.props&CharWrite != 0 {
			props |= 0x08
			if c.secure&CharWrite != 0 {
				perm |= 0x08
			} else {
				perm |= 0x02
			}
		}
		if c.props&CharNotify != 0 {
			if c.secure&CharNotify != 0 {
				props |= 0x100
			} else {
				props |= 0x10
			}
		}
		if c.props&CharIndicate != 0 {
			if c.secure&CharIndicate != 0 {
				props |= 0x200
			} else {
				props |= 0x20
			}
		}

		xc := xpc.Dict{
			"kCBMsgArgAttributeID":              d.attrN,
			"kCBMsgArgUUID":                     reverse(c.uuid.b),
			"kCBMsgArgAttributePermissions":     perm,
			"kCBMsgArgCharacteristicProperties": props,
			"kCBMsgArgData":                     c.value,
		}
		d.attrs[d.attrN] = &attr{h: uint16(d.attrN), value: c.value, pvt: c}
		d.attrN++

		xds := xpc.Array{}
		for _, d := range c.Descriptors() {
			if d.uuid.Equal(attrClientCharacteristicConfigUUID) {
				// skip CCCD
				continue
			}
			var v interface{}
			if len(d.valueStr) > 0 {
				v = d.valueStr
			} else {
				v = d.value
			}
			xd := xpc.Dict{
				"kCBMsgArgData": v,
				"kCBMsgArgUUID": reverse(d.uuid.b),
			}
			xds = append(xds, xd)
		}
		xc["kCBMsgArgDescriptors"] = xds
		xcs = append(xcs, xc)
	}
	xs["kCBMsgArgCharacteristics"] = xcs

	resp := d.sendReq(ctx, 10, xs)
	if res := resp.MustGetInt("kCBMsgArgResult"); res != 0 {
		return errors.New("FIXME: Add Service error")
	}
	return nil
}

func (d *device) SetServices(ctx context.Context, ss []*Service) error {
	d.RemoveAllServices(ctx)
	for _, s := range ss {
		d.AddService(ctx, s)
	}
	return nil
}

func (d *device) Scan(ctx context.Context, services []UUID, dup bool) error {
	args := xpc.Dict{
		"kCBMsgArgUUIDs": uuidSlice(services),
		"kCBMsgArgOptions": xpc.Dict{
			"kCBScanOptionAllowDuplicates": map[bool]int{true: 1, false: 0}[dup],
		},
	}

	msg := 29

	var utsName xpc.Utsname
	xpc.Uname(&utsName)

	if utsName.Release >= "19." {
		msg = 51
	} else if utsName.Release >= "18." {
		msg = 46
	} else if utsName.Release >= "17." {
		msg = 44
	}

	d.sendCmd(ctx, msg, args)
	return nil
}

func (d *device) StopScanning() error {
	msg := 30

	var utsName xpc.Utsname
	xpc.Uname(&utsName)

	if utsName.Release >= "19." {
		msg = 52
	} else if utsName.Release >= "18." {
		msg = 47
	}

	d.sendCmd(context.TODO(), msg, nil)
	return nil
}

func (d *device) Connect(ctx context.Context, p Peripheral) {
	msg := 31

	var utsName xpc.Utsname
	xpc.Uname(&utsName)

	if utsName.Release >= "18." {
		msg = 48
	}

	pp := p.(*peripheral)
	d.plist[pp.id.String()] = pp
	d.sendCmd(ctx, msg,
		xpc.Dict{
			"kCBMsgArgDeviceUUID": pp.id,
			"kCBMsgArgOptions": xpc.Dict{
				"kCBConnectOptionNotifyOnDisconnection": 1,
			},
		})
}

func (d *device) respondToRequest(ctx context.Context, id int, args xpc.Dict) {

	switch id {
	case 19: // ReadRequest
		u := UUID{args.MustGetUUID("kCBMsgArgDeviceUUID")}
		t := args.MustGetInt("kCBMsgArgTransactionID")
		a := args.MustGetInt("kCBMsgArgAttributeID")
		o := args.MustGetInt("kCBMsgArgOffset")

		attr := d.attrs[a]
		v := attr.value
		if v == nil {
			c := newCentral(d, u)
			req := &ReadRequest{
				Request: Request{Central: c},
				Cap:     int(c.mtu - 1),
				Offset:  o,
			}
			resp := newResponseWriter(int(c.mtu - 1))
			if c, ok := attr.pvt.(*Characteristic); ok {
				c.readHandler.ServeRead(ctx, resp, req)
				v = resp.bytes()
			}
		}

		d.sendCmd(ctx, 13, xpc.Dict{
			"kCBMsgArgAttributeID":   a,
			"kCBMsgArgData":          v,
			"kCBMsgArgTransactionID": t,
			"kCBMsgArgResult":        0,
		})

	case 20: // WriteRequest
		u := UUID{args.MustGetUUID("kCBMsgArgDeviceUUID")}
		t := args.MustGetInt("kCBMsgArgTransactionID")
		a := 0
		result := byte(0)
		noResp := false
		xxws := args.MustGetArray("kCBMsgArgATTWrites") // TODO: what is "xxw" exactly?
		for _, xxw := range xxws {
			xw := xxw.(xpc.Dict)
			if a == 0 {
				a = xw.MustGetInt("kCBMsgArgAttributeID")
			}
			o := xw.MustGetInt("kCBMsgArgOffset")
			i := xw.MustGetInt("kCBMsgArgIgnoreResponse")
			b := xw.MustGetBytes("kCBMsgArgData")
			_ = o
			attr := d.attrs[a]
			c := newCentral(d, u)
			r := Request{Central: c}
			result = attr.pvt.(*Characteristic).writeHandler.ServeWrite(ctx, r, b)
			if i == 1 {
				noResp = true
			}

		}
		if noResp {
			break
		}
		d.sendCmd(ctx, 13, xpc.Dict{
			"kCBMsgArgAttributeID":   a,
			"kCBMsgArgData":          nil,
			"kCBMsgArgTransactionID": t,
			"kCBMsgArgResult":        result,
		})

	case 21: // subscribed
		u := UUID{args.MustGetUUID("kCBMsgArgDeviceUUID")}
		a := args.MustGetInt("kCBMsgArgAttributeID")
		attr := d.attrs[a]
		c := newCentral(d, u)
		d.subscribers[u.String()] = c
		c.startNotify(ctx, attr, c.mtu)

	case 22: // unsubscribed
		u := UUID{args.MustGetUUID("kCBMsgArgDeviceUUID")}
		a := args.MustGetInt("kCBMsgArgAttributeID")
		attr := d.attrs[a]
		if c := d.subscribers[u.String()]; c != nil {
			c.stopNotify(attr)
		}

	case 23: // notificationSent
	}
}

func (d *device) CancelConnection(ctx context.Context, p Peripheral) {
	d.sendCmd(ctx, 32, xpc.Dict{"kCBMsgArgDeviceUUID": p.(*peripheral).id})
}

// process device events and asynchronous errors
// (implements XpcEventHandler)
func (d *device) HandleXpcEvent(ctx context.Context, event xpc.Dict, err error) {
	if err != nil {
		log.Println("error:", err)
		return
	}

	id := event.MustGetInt("kCBMsgId")
	args := event.MustGetDict("kCBMsgArgs")
	//logger.Debugf(ctx, ">> %d, %v", id, args)

	switch id {
	case // device event
		4,  // StateChanged (new)
		6,  // StateChanged (old)
		16, // AdvertisingStarted
		17, // AdvertisingStopped
		18: // ServiceAdded
		d.respCh <- message{id: id, args: args}

	case
		19, // ReadRequest
		20, // WriteRequest
		21, // Subscribe
		22, // Unsubscribe
		23: // Confirmation
		d.respondToRequest(ctx, id, args)

	case peripheralDiscovered,
		peripheralDiscovered_2,
		peripheralDiscovered_3,
		peripheralDiscovered_4:
		xa := args.MustGetDict("kCBMsgArgAdvertisementData")
		if len(xa) == 0 {
			return
		}
		u := UUID{args.MustGetUUID("kCBMsgArgDeviceUUID")}
		a := &Advertisement{
			LocalName:        xa.GetString("kCBAdvDataLocalName", args.GetString("kCBMsgArgName", "")),
			TxPowerLevel:     xa.GetInt("kCBAdvDataTxPowerLevel", 0),
			ManufacturerData: xa.GetBytes("kCBAdvDataManufacturerData", nil),
		}

		rssi := args.MustGetInt("kCBMsgArgRssi")

		if xu, ok := xa["kCBAdvDataServiceUUIDs"]; ok {
			for _, xs := range xu.(xpc.Array) {
				s := UUID{reverse(xs.([]byte))}
				a.Services = append(a.Services, s)
			}
		}
		if xsds, ok := xa["kCBAdvDataServiceData"]; ok {
			xsd := xsds.(xpc.Array)
			for i := 0; i < len(xsd); i += 2 {
				sd := ServiceData{
					UUID: UUID{xsd[i].([]byte)},
					Data: xsd[i+1].([]byte),
				}
				a.ServiceData = append(a.ServiceData, sd)
			}
		}
		if d.peripheralDiscovered != nil {
			go d.peripheralDiscovered(ctx, &peripheral{id: xpc.UUID(u.b), device: d}, a, rssi)
		}

	case peripheralConnected, peripheralConnected_2:
		var utsName xpc.Utsname
		xpc.Uname(&utsName)

		if utsName.Release >= "18." {
			// this is not a connect (it doesn't have kCBMsgArgDeviceUUID,
			// but instead has kCBAdvDataDeviceAddress)
			break
		}

		u := UUID{args.MustGetUUID("kCBMsgArgDeviceUUID")}
		p := &peripheral{
			id:     xpc.UUID(u.b),
			device: d,
			reqc:   make(chan message),
			respCh: make(chan message),
			quitc:  make(chan struct{}),
			sub:    newSubscriber(),
		}
		d.plistMutex.Lock()
		d.plist[u.String()] = p
		d.plistMutex.Unlock()
		go p.loop(ctx)

		if d.peripheralConnected != nil {
			go d.peripheralConnected(ctx, p, nil)
		}

	case peripheralDisconnected:
		u := UUID{args.MustGetUUID("kCBMsgArgDeviceUUID")}
		d.plistMutex.Lock()
		p := d.plist[u.String()]
		delete(d.plist, u.String())
		d.plistMutex.Unlock()
		if p != nil {
			if d.peripheralDisconnected != nil {
				d.peripheralDisconnected(ctx, p, nil) // TODO: Get Result as error?
			}
			close(p.quitc)
		}

	case // Peripheral events
		rssiRead,
		serviceDiscovered,
		includedServicesDiscovered,
		characteristicsDiscovered,
		characteristicsDiscovered_2,
		characteristicRead,
		characteristicWritten,
		notificationValueSet,
		descriptorsDiscovered,
		descriptorRead,
		descriptorWritten:

		u := UUID{args.MustGetUUID("kCBMsgArgDeviceUUID")}
		d.plistMutex.Lock()
		p := d.plist[u.String()]
		d.plistMutex.Unlock()
		if p != nil {
			p.respCh <- message{id: id, args: args}
		}
	default:
		//logger.Debugf(ctx, "Unhandled event: %#v", event)
	}
}

func (d *device) sendReq(ctx context.Context, id int, args xpc.Dict) xpc.Dict {
	m := message{id: id, args: args, respCh: make(chan xpc.Dict)}
	d.reqc <- m
	return <-m.respCh
}

func (d *device) sendCmd(ctx context.Context, id int, args xpc.Dict) {
	d.reqc <- message{id: id, args: args}
}

func (d *device) loop(ctx context.Context) {
	for req := range d.reqc {
		d.sendCBMsg(ctx, req.id, req.args)
		if req.respCh == nil {
			continue
		}
		m := <-d.respCh
		req.respCh <- m.args
	}
}

func (d *device) sendCBMsg(ctx context.Context, id int, args xpc.Dict) {
	// logger.Debugf(ctx, "<< %d, %v", id, args)
	d.conn.Send(ctx, xpc.Dict{"kCBMsgId": id, "kCBMsgArgs": args}, false)
}
