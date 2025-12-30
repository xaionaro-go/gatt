package gatt

import (
	"context"
	"errors"
	"sync"
)

const (
	DefaultMTU = 1024
)

type simDevice struct {
	deviceHandler

	service          *Service
	advertisedName   string
	manufacturerData []byte

	subscribersLocker sync.Mutex
	subscribers       map[uint16]func(*Characteristic, []byte, error)
}

func NewSimDeviceClient(service *Service, advertisedName string) *simDevice {
	return &simDevice{
		service:        service,
		advertisedName: advertisedName,
		subscribers:    make(map[uint16]func(*Characteristic, []byte, error)),
	}
}

func (d *simDevice) SetManufacturerData(b []byte) {
	d.manufacturerData = b
}

func (d *simDevice) Start(
	ctx context.Context,
	stateChanged func(context.Context, Device, State),
) error {
	d.stateChanged = stateChanged
	go stateChanged(ctx, d, StatePoweredOn)
	return nil
}

func (d *simDevice) Stop() error {
	go d.stateChanged(context.TODO(), d, StatePoweredOff)
	return nil
}

func (d *simDevice) Advertise(ctx context.Context, a *AdvPacket) error {
	return ErrMethodNotSupported
}

func (d *simDevice) AdvertiseNameAndServices(ctx context.Context, name string, ss []UUID) error {
	return ErrMethodNotSupported
}

func (d *simDevice) AdvertiseIBeaconData(ctx context.Context, b []byte) error {
	return ErrMethodNotSupported
}

func (d *simDevice) AdvertiseNameAndIBeaconData(ctx context.Context, name string, b []byte) error {
	return ErrMethodNotSupported
}

func (d *simDevice) AdvertiseIBeacon(ctx context.Context, u UUID, major, minor uint16, pwr int8) error {
	return ErrMethodNotSupported
}

func (d *simDevice) StopAdvertising(ctx context.Context) error {
	return ErrMethodNotSupported
}

func (d *simDevice) RemoveAllServices(ctx context.Context) error {
	return ErrMethodNotSupported
}

func (d *simDevice) AddService(ctx context.Context, s *Service) error {
	return ErrMethodNotSupported
}

func (d *simDevice) SetServices(ctx context.Context, ss []*Service) error {
	return ErrMethodNotSupported
}

func (d *simDevice) Scan(ctx context.Context, ss []UUID, dup bool) error {
	// Fix: support nil/empty ss to allow discovery by djictl
	if len(ss) == 0 {
		go d.peripheralDiscovered(
			ctx,
			&simPeripheral{d},
			&Advertisement{
				LocalName:        d.advertisedName,
				ManufacturerData: d.manufacturerData,
			},
			0,
		)
		return nil
	}
	for _, s := range ss {
		if s.Equal(d.service.UUID()) {
			go d.peripheralDiscovered(
				ctx,
				&simPeripheral{d},
				&Advertisement{
					LocalName:        d.advertisedName,
					ManufacturerData: d.manufacturerData,
				},
				0,
			)
		}
	}
	return nil
}

func (d *simDevice) StopScanning() error {
	return nil
}

func (d *simDevice) Connect(ctx context.Context, p Peripheral) {
	go d.peripheralConnected(ctx, p, nil)
}

func (d *simDevice) CancelConnection(ctx context.Context, p Peripheral) {
	go d.peripheralDisconnected(ctx, p, nil)
}

func (d *simDevice) Handle(ctx context.Context, hh ...Handler) {
	for _, h := range hh {
		h(ctx, d)
	}
}

func (d *simDevice) Option(o ...Option) error {
	return ErrMethodNotSupported
}

// Added helper for notifications
func (d *simDevice) SendNotification(vh uint16, b []byte) {
	d.subscribersLocker.Lock()
	defer d.subscribersLocker.Unlock()
	f, ok := d.subscribers[vh]
	if ok {
		// Find characteristic
		var char *Characteristic
		for _, c := range d.service.Characteristics() {
			if c.VHandle() == vh {
				char = c
				break
			}
		}
		if char != nil {
			go f(char, b, nil)
		}
	}
}

type simPeripheral struct {
	d *simDevice
}

func (p *simPeripheral) Device() Device {
	return p.d
}

func (p *simPeripheral) ID() string {
	return "00:11:22:33:44:55"
}

func (d *simDevice) ID() int {
	return -1
}

func (p *simPeripheral) Name() string {
	return "Sim"
}

func (p *simPeripheral) Services(ctx context.Context) []*Service {
	return []*Service{p.d.service}
}

func (p *simPeripheral) DiscoverServices(ctx context.Context, ss []UUID) ([]*Service, error) {
	if len(ss) == 0 {
		return []*Service{p.d.service}, nil
	}
	for _, s := range ss {
		if s.Equal(p.d.service.UUID()) {
			return []*Service{p.d.service}, nil
		}
	}
	return []*Service{}, nil
}

func (p *simPeripheral) DiscoverIncludedServices(ctx context.Context, ss []UUID, s *Service) ([]*Service, error) {
	return nil, nil // Fix: don't return error
}

func (p *simPeripheral) DiscoverCharacteristics(ctx context.Context, cc []UUID, s *Service) ([]*Characteristic, error) {
	if len(cc) == 0 {
		return s.Characteristics(), nil
	}
	requestedUUIDs := make(map[string]bool)
	for _, c := range cc {
		requestedUUIDs[c.String()] = true
	}
	foundChars := make([]*Characteristic, 0)
	for _, c := range s.Characteristics() {
		if _, present := requestedUUIDs[c.UUID().String()]; present {
			foundChars = append(foundChars, c)
		}
	}
	return foundChars, nil
}

func (p *simPeripheral) DiscoverDescriptors(ctx context.Context, d []UUID, c *Characteristic) ([]*Descriptor, error) {
	return nil, nil // Fix: don't return error
}

func (p *simPeripheral) ReadCharacteristic(ctx context.Context, c *Characteristic) ([]byte, error) {
	readHandler := c.GetReadHandler()
	if readHandler != nil {
		resp := newResponseWriter(DefaultMTU)
		req := &ReadRequest{}
		readHandler.ServeRead(ctx, resp, req)
		return resp.buf.Bytes(), nil
	} else {
		return nil, AttrECodeReadNotPerm
	}
}

func (p *simPeripheral) ReadLongCharacteristic(ctx context.Context, c *Characteristic) ([]byte, error) {
	return p.ReadCharacteristic(ctx, c)
}

var ErrMethodNotSupported = errors.New("method not supported")

func (p *simPeripheral) ReadDescriptor(ctx context.Context, d *Descriptor) ([]byte, error) {
	return nil, ErrMethodNotSupported
}

func (p *simPeripheral) WriteCharacteristic(ctx context.Context, c *Characteristic, b []byte, noResp bool) error {
	writeHandler := c.GetWriteHandler()
	if writeHandler != nil {
		r := Request{}
		if res := writeHandler.ServeWrite(ctx, r, b); res != 0 {
			return AttrECode(res)
		} else {
			return nil
		}
	} else {
		return AttrECodeWriteNotPerm
	}
}

func (p *simPeripheral) WriteDescriptor(ctx context.Context, d *Descriptor, b []byte) error {
	return ErrMethodNotSupported
}

func (p *simPeripheral) Subscribe(vh uint16, f func(*Characteristic, []byte, error)) {
	// Fix: implement subscription
	p.d.subscribersLocker.Lock()
	p.d.subscribers[vh] = f
	p.d.subscribersLocker.Unlock()

	// Trigger notify handler if any
	for _, c := range p.d.service.Characteristics() {
		if c.VHandle() == vh {
			if h := c.GetNotifyHandler(); h != nil {
				go h.ServeNotify(context.Background(), Request{}, nil)
			}
			break
		}
	}
}

func (p *simPeripheral) SetNotifyValue(ctx context.Context, c *Characteristic, f func(*Characteristic, []byte, error)) error {
	p.Subscribe(c.VHandle(), f)
	return nil
}

func (p *simPeripheral) SetIndicateValue(ctx context.Context, c *Characteristic, f func(*Characteristic, []byte, error)) error {
	p.Subscribe(c.VHandle(), f)
	return nil
}

func (p *simPeripheral) ReadRSSI(ctx context.Context) int {
	return 0
}

func (p *simPeripheral) SetMTU(ctx context.Context, mtu uint16) error {
	return nil // Fix: don't return error
}
