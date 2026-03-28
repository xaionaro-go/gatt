package binder

import (
	"context"
	"fmt"
	"sync"

	"github.com/facebookincubator/go-belt/tool/logger"

	"github.com/xaionaro-go/gatt"
)

// peripheral implements gatt.Peripheral for the binder backend.
type peripheral struct {
	d    *device
	addr string
	name string

	mu       sync.Mutex
	clientIf int32
	rawGatt  *rawGATT

	services []*gatt.Service

	mtu         int
	subscribers map[uint16]func(*gatt.Characteristic, []byte, error)

	// Synchronization channels for async GATT callbacks (buffered size 1).
	clientRegistered chan int32
	connStateChanged chan bool // true=connected, false=disconnected
	searchComplete   chan error
	charRead         chan charReadResult
	charWritten      chan error
	descRead         chan descReadResult
	descWritten      chan error
	rssiRead         chan rssiResult
	mtuChanged       chan mtuResult
}

type charReadResult struct {
	data []byte
	err  error
}

type descReadResult struct {
	data []byte
	err  error
}

type rssiResult struct {
	rssi int32
	err  error
}

type mtuResult struct {
	mtu int32
	err error
}

func newPeripheral(d *device, addr string, name string) *peripheral {
	return &peripheral{
		d:                d,
		addr:             addr,
		name:             name,
		mtu:              23, // BLE default
		subscribers:      make(map[uint16]func(*gatt.Characteristic, []byte, error)),
		clientRegistered: make(chan int32, 1),
		connStateChanged: make(chan bool, 1),
		searchComplete:   make(chan error, 1),
		charRead:         make(chan charReadResult, 1),
		charWritten:      make(chan error, 1),
		descRead:         make(chan descReadResult, 1),
		descWritten:      make(chan error, 1),
		rssiRead:         make(chan rssiResult, 1),
		mtuChanged:       make(chan mtuResult, 1),
	}
}

func (p *peripheral) Device() gatt.Device                        { return p.d }
func (p *peripheral) ID() string                                 { return p.addr }
func (p *peripheral) Name() string                               { return p.name }
func (p *peripheral) Services(_ context.Context) []*gatt.Service { return p.services }

func (p *peripheral) DiscoverServices(
	ctx context.Context,
	_ []gatt.UUID,
) (_ []*gatt.Service, _err error) {
	logger.Tracef(ctx, "peripheral.DiscoverServices")
	defer func() { logger.Tracef(ctx, "/peripheral.DiscoverServices: %v", _err) }()

	p.mu.Lock()
	rg := p.rawGatt
	clientIf := p.clientIf
	p.mu.Unlock()

	if rg == nil {
		return nil, fmt.Errorf("not connected")
	}

	err := rg.discoverServices(ctx, clientIf, p.addr)
	if err != nil {
		return nil, fmt.Errorf("discoverServices: %w", err)
	}

	// Wait for OnSearchComplete callback.
	select {
	case err := <-p.searchComplete:
		if err != nil {
			return nil, err
		}
		return p.services, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *peripheral) DiscoverIncludedServices(
	_ context.Context,
	_ []gatt.UUID,
	_ *gatt.Service,
) ([]*gatt.Service, error) {
	// Android's GATT stack discovers included services as part of
	// DiscoverServices; there is no separate binder transaction for this.
	return nil, fmt.Errorf("included service discovery not supported on Android binder backend")
}

func (p *peripheral) DiscoverCharacteristics(
	_ context.Context,
	_ []gatt.UUID,
	s *gatt.Service,
) ([]*gatt.Characteristic, error) {
	// On Android, characteristics are discovered as part of DiscoverServices.
	return s.Characteristics(), nil
}

func (p *peripheral) DiscoverDescriptors(
	_ context.Context,
	_ []gatt.UUID,
	c *gatt.Characteristic,
) ([]*gatt.Descriptor, error) {
	// On Android, descriptors are discovered as part of DiscoverServices.
	return c.Descriptors(), nil
}

func (p *peripheral) ReadCharacteristic(
	ctx context.Context,
	c *gatt.Characteristic,
) (_ []byte, _err error) {
	logger.Tracef(ctx, "peripheral.ReadCharacteristic")
	defer func() { logger.Tracef(ctx, "/peripheral.ReadCharacteristic: %v", _err) }()

	p.mu.Lock()
	rg := p.rawGatt
	clientIf := p.clientIf
	p.mu.Unlock()

	if rg == nil {
		return nil, fmt.Errorf("not connected")
	}

	err := rg.readCharacteristic(ctx, clientIf, p.addr, int32(c.VHandle()), 0)
	if err != nil {
		return nil, fmt.Errorf("readCharacteristic: %w", err)
	}

	// Wait for OnCharacteristicRead callback.
	select {
	case result := <-p.charRead:
		if result.err != nil {
			return nil, result.err
		}
		return result.data, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *peripheral) ReadLongCharacteristic(
	ctx context.Context,
	c *gatt.Characteristic,
) ([]byte, error) {
	return p.ReadCharacteristic(ctx, c)
}

func (p *peripheral) WriteCharacteristic(
	ctx context.Context,
	c *gatt.Characteristic,
	value []byte,
	noRsp bool,
) (_err error) {
	logger.Tracef(ctx, "peripheral.WriteCharacteristic")
	defer func() { logger.Tracef(ctx, "/peripheral.WriteCharacteristic: %v", _err) }()

	p.mu.Lock()
	rg := p.rawGatt
	clientIf := p.clientIf
	p.mu.Unlock()

	if rg == nil {
		return fmt.Errorf("not connected")
	}

	// writeType: 1 = WRITE_TYPE_NO_RESPONSE, 2 = WRITE_TYPE_DEFAULT
	writeType := int32(2)
	if noRsp {
		writeType = 1
	}

	err := rg.writeCharacteristic(ctx, clientIf, p.addr, int32(c.VHandle()), writeType, 0, value)
	if err != nil {
		return fmt.Errorf("writeCharacteristic: %w", err)
	}

	// Wait for OnCharacteristicWrite callback.
	select {
	case err := <-p.charWritten:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *peripheral) ReadDescriptor(
	ctx context.Context,
	d *gatt.Descriptor,
) (_ []byte, _err error) {
	logger.Tracef(ctx, "peripheral.ReadDescriptor")
	defer func() { logger.Tracef(ctx, "/peripheral.ReadDescriptor: %v", _err) }()

	p.mu.Lock()
	rg := p.rawGatt
	clientIf := p.clientIf
	p.mu.Unlock()

	if rg == nil {
		return nil, fmt.Errorf("not connected")
	}

	err := rg.readDescriptor(ctx, clientIf, p.addr, int32(d.Handle()), 0)
	if err != nil {
		return nil, fmt.Errorf("readDescriptor: %w", err)
	}

	// Wait for OnDescriptorRead callback.
	select {
	case result := <-p.descRead:
		if result.err != nil {
			return nil, result.err
		}
		return result.data, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *peripheral) WriteDescriptor(
	ctx context.Context,
	desc *gatt.Descriptor,
	value []byte,
) (_err error) {
	logger.Tracef(ctx, "peripheral.WriteDescriptor")
	defer func() { logger.Tracef(ctx, "/peripheral.WriteDescriptor: %v", _err) }()

	p.mu.Lock()
	rg := p.rawGatt
	clientIf := p.clientIf
	p.mu.Unlock()

	if rg == nil {
		return fmt.Errorf("not connected")
	}

	err := rg.writeDescriptor(ctx, clientIf, p.addr, int32(desc.Handle()), 0, value)
	if err != nil {
		return fmt.Errorf("writeDescriptor: %w", err)
	}

	// Wait for OnDescriptorWrite callback.
	select {
	case err := <-p.descWritten:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *peripheral) Subscribe(
	vh uint16,
	f func(*gatt.Characteristic, []byte, error),
) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if f == nil {
		delete(p.subscribers, vh)
		return
	}
	p.subscribers[vh] = f
}

func (p *peripheral) SetNotifyValue(
	ctx context.Context,
	c *gatt.Characteristic,
	f func(*gatt.Characteristic, []byte, error),
) (_err error) {
	logger.Tracef(ctx, "peripheral.SetNotifyValue")
	defer func() { logger.Tracef(ctx, "/peripheral.SetNotifyValue: %v", _err) }()

	p.mu.Lock()
	rg := p.rawGatt
	clientIf := p.clientIf
	p.mu.Unlock()

	if rg == nil {
		return fmt.Errorf("not connected")
	}

	enable := f != nil
	p.Subscribe(c.VHandle(), f)

	err := rg.registerForNotification(ctx, clientIf, p.addr, int32(c.VHandle()), enable)
	if err != nil {
		if enable {
			p.Subscribe(c.VHandle(), nil) // roll back subscription
		}
		return fmt.Errorf("registerForNotification: %w", err)
	}
	return nil
}

func (p *peripheral) SetIndicateValue(
	ctx context.Context,
	c *gatt.Characteristic,
	f func(*gatt.Characteristic, []byte, error),
) (_err error) {
	logger.Tracef(ctx, "peripheral.SetIndicateValue")
	defer func() { logger.Tracef(ctx, "/peripheral.SetIndicateValue: %v", _err) }()

	// On Android, registerForNotification handles both notifications and
	// indications; the stack determines which to use based on the
	// characteristic's properties.
	return p.SetNotifyValue(ctx, c, f)
}

func (p *peripheral) ReadRSSI(ctx context.Context) int {
	logger.Tracef(ctx, "peripheral.ReadRSSI")
	defer func() { logger.Tracef(ctx, "/peripheral.ReadRSSI") }()

	p.mu.Lock()
	rg := p.rawGatt
	clientIf := p.clientIf
	p.mu.Unlock()

	if rg == nil {
		return -1
	}

	err := rg.readRemoteRssi(ctx, clientIf, p.addr)
	if err != nil {
		logger.Warnf(ctx, "readRemoteRssi: %v", err)
		return -1
	}

	// Wait for OnReadRemoteRssi callback.
	select {
	case result := <-p.rssiRead:
		if result.err != nil {
			logger.Warnf(ctx, "readRemoteRssi result: %v", result.err)
			return -1
		}
		return int(result.rssi)
	case <-ctx.Done():
		return -1
	}
}

func (p *peripheral) SetMTU(
	ctx context.Context,
	mtu uint16,
) (_err error) {
	logger.Tracef(ctx, "peripheral.SetMTU")
	defer func() { logger.Tracef(ctx, "/peripheral.SetMTU: %v", _err) }()

	p.mu.Lock()
	rg := p.rawGatt
	clientIf := p.clientIf
	p.mu.Unlock()

	if rg == nil {
		return fmt.Errorf("not connected")
	}

	err := rg.configureMTU(ctx, clientIf, p.addr, int32(mtu))
	if err != nil {
		return fmt.Errorf("configureMTU: %w", err)
	}

	// Wait for OnConfigureMTU callback.
	select {
	case result := <-p.mtuChanged:
		if result.err != nil {
			return result.err
		}
		p.mu.Lock()
		p.mtu = int(result.mtu)
		p.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Ensure *peripheral satisfies gatt.Peripheral at compile time.
var _ gatt.Peripheral = (*peripheral)(nil)
