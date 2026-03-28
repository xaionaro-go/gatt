package gatt

import (
	"context"
	"errors"
)

var errNotImplemented = errors.New("not implemented")

type State int

const (
	StateUnknown      State = 0
	StateResetting    State = 1
	StateUnsupported  State = 2
	StateUnauthorized State = 3
	StatePoweredOff   State = 4
	StatePoweredOn    State = 5
)

func (s State) String() string {
	str := []string{
		"Unknown",
		"Resetting",
		"Unsupported",
		"Unauthorized",
		"PoweredOff",
		"PoweredOn",
	}
	return str[int(s)]
}

// Device defines the interface for a BLE device.
// Since an interface can't define fields(properties). To implement the
// callback support for certain events, deviceHandler is defined and
// implementation of Device on different platforms should embed it in
// order to keep have keep compatible in API level.
// Package users can use the Handler to set these handlers.
type Device interface {
	ID() int

	Start(ctx context.Context, stateChanged func(context.Context, Device, State)) error

	// Stop calls OS specific close calls
	Stop() error

	// Advertise advertise AdvPacket
	Advertise(ctx context.Context, a *AdvPacket) error

	// AdvertiseNameAndServices advertises device name, and specified service UUIDs.
	// It tres to fit the UUIDs in the advertising packet as much as possible.
	// If name doesn't fit in the advertising packet, it will be put in scan response.
	AdvertiseNameAndServices(ctx context.Context, name string, ss []UUID) error

	AdvertiseNameAndIBeaconData(ctx context.Context, name string, b []byte) error

	// AdvertiseIBeaconData advertise iBeacon with given manufacturer data.
	AdvertiseIBeaconData(ctx context.Context, b []byte) error

	// AdvertisingIBeacon advertises iBeacon with specified parameters.
	AdvertiseIBeacon(ctx context.Context, u UUID, major, minor uint16, pwr int8) error

	// StopAdvertising stops advertising.
	StopAdvertising(ctx context.Context) error

	// RemoveAllServices removes all services that are currently in the database.
	RemoveAllServices(ctx context.Context) error

	// Add Service add a service to database.
	AddService(ctx context.Context, s *Service) error

	// SetServices set the specified service to the database.
	// It removes all currently added services, if any.
	SetServices(ctx context.Context, ss []*Service) error

	// Scan discovers surrounding remote peripherals that have the Service UUID specified in ss.
	// If ss is set to nil, all devices scanned are reported.
	// dup specifies weather duplicated advertisement should be reported or not.
	// When a remote peripheral is discovered, the PeripheralDiscovered Handler is called.
	Scan(ctx context.Context, ss []UUID, dup bool) error

	// StopScanning stops scanning.
	StopScanning() error

	// Connect connects to a remote peripheral.
	Connect(ctx context.Context, p Peripheral)

	// CancelConnection disconnects a remote peripheral.
	CancelConnection(ctx context.Context, p Peripheral)

	// Handle registers the specified handlers.
	Handle(ctx context.Context, h ...Handler)

	// Option sets the options specified.
	Option(o ...Option) error
}

// deviceHandler is the handlers(callbacks) of the Device.
type deviceHandler struct {
	// stateChanged is called when the device states changes.
	stateChanged func(ctx context.Context, d Device, s State)

	// connect is called when a remote central device connects to the device.
	centralConnected func(ctx context.Context, c Central)

	// disconnect is called when a remote central device disconnects to the device.
	centralDisconnected func(ctx context.Context, c Central)

	// peripheralDiscovered is called when a remote peripheral device is found during scan procedure.
	peripheralDiscovered func(ctx context.Context, p Peripheral, a *Advertisement, rssi int)

	// peripheralConnected is called when a remote peripheral is connected.
	peripheralConnected func(ctx context.Context, p Peripheral, err error)

	// peripheralConnected is called when a remote peripheral is disconnected.
	peripheralDisconnected func(ctx context.Context, p Peripheral, err error)
}

// DeviceHandler provides exported access to device callbacks for external backend packages.
// External packages (e.g. android backends in separate modules) can embed DeviceHandler
// in their device types to satisfy the deviceHandlerProvider interface.
type DeviceHandler struct {
	deviceHandler
}

func (h *DeviceHandler) getDeviceHandler() *deviceHandler {
	return &h.deviceHandler
}

// StateChanged returns the current state-changed callback, or nil if not set.
func (h *DeviceHandler) StateChanged() func(context.Context, Device, State) {
	return h.stateChanged
}

// SetStateChanged sets the state-changed callback.
func (h *DeviceHandler) SetStateChanged(f func(context.Context, Device, State)) {
	h.stateChanged = f
}

// CentralConnected returns the current central-connected callback, or nil if not set.
func (h *DeviceHandler) CentralConnected() func(context.Context, Central) {
	return h.centralConnected
}

// SetCentralConnected sets the central-connected callback.
func (h *DeviceHandler) SetCentralConnected(f func(context.Context, Central)) {
	h.centralConnected = f
}

// CentralDisconnected returns the current central-disconnected callback, or nil if not set.
func (h *DeviceHandler) CentralDisconnected() func(context.Context, Central) {
	return h.centralDisconnected
}

// SetCentralDisconnected sets the central-disconnected callback.
func (h *DeviceHandler) SetCentralDisconnected(f func(context.Context, Central)) {
	h.centralDisconnected = f
}

// PeripheralDiscovered returns the current peripheral-discovered callback, or nil if not set.
func (h *DeviceHandler) PeripheralDiscovered() func(context.Context, Peripheral, *Advertisement, int) {
	return h.peripheralDiscovered
}

// SetPeripheralDiscovered sets the peripheral-discovered callback.
func (h *DeviceHandler) SetPeripheralDiscovered(f func(context.Context, Peripheral, *Advertisement, int)) {
	h.peripheralDiscovered = f
}

// PeripheralConnected returns the current peripheral-connected callback, or nil if not set.
func (h *DeviceHandler) PeripheralConnected() func(context.Context, Peripheral, error) {
	return h.peripheralConnected
}

// SetPeripheralConnected sets the peripheral-connected callback.
func (h *DeviceHandler) SetPeripheralConnected(f func(context.Context, Peripheral, error)) {
	h.peripheralConnected = f
}

// PeripheralDisconnected returns the current peripheral-disconnected callback, or nil if not set.
func (h *DeviceHandler) PeripheralDisconnected() func(context.Context, Peripheral, error) {
	return h.peripheralDisconnected
}

// SetPeripheralDisconnected sets the peripheral-disconnected callback.
func (h *DeviceHandler) SetPeripheralDisconnected(f func(context.Context, Peripheral, error)) {
	h.peripheralDisconnected = f
}

type deviceHandlerProvider interface {
	getDeviceHandler() *deviceHandler
}

func getDeviceHandler(d Device) *deviceHandler {
	if p, ok := d.(deviceHandlerProvider); ok {
		return p.getDeviceHandler()
	}
	return nil
}

// A Handler is a self-referential function, which registers the options specified.
// See http://commandcenter.blogspot.com.au/2014/01/self-referential-functions-and-design.html for more discussion.
type Handler func(context.Context, Device)

// Handle registers the specified handlers.
func (d *device) Handle(ctx context.Context, hh ...Handler) {
	for _, h := range hh {
		h(ctx, d)
	}
}

// CentralConnected returns a Handler, which sets the specified function to be called when a device connects to the server.
func CentralConnected(f func(context.Context, Central)) Handler {
	return func(ctx context.Context, d Device) { getDeviceHandler(d).centralConnected = f }
}

// CentralDisconnected returns a Handler, which sets the specified function to be called when a device disconnects from the server.
func CentralDisconnected(f func(context.Context, Central)) Handler {
	return func(ctx context.Context, d Device) { getDeviceHandler(d).centralDisconnected = f }
}

// PeripheralDiscovered returns a Handler, which sets the specified function to be called when a remote peripheral device is found during scan procedure.
func PeripheralDiscovered(f func(context.Context, Peripheral, *Advertisement, int)) Handler {
	return func(ctx context.Context, d Device) { getDeviceHandler(d).peripheralDiscovered = f }
}

// PeripheralConnected returns a Handler, which sets the specified function to be called when a remote peripheral device connects.
func PeripheralConnected(f func(context.Context, Peripheral, error)) Handler {
	return func(ctx context.Context, d Device) { getDeviceHandler(d).peripheralConnected = f }
}

// PeripheralDisconnected returns a Handler, which sets the specified function to be called when a remote peripheral device disconnects.
func PeripheralDisconnected(f func(context.Context, Peripheral, error)) Handler {
	return func(ctx context.Context, d Device) { getDeviceHandler(d).peripheralDisconnected = f }
}

// An Option is a self-referential function, which sets the option specified.
// Most Options are platform-specific, which gives more fine-grained control over the device at a cost of losing portability.
// See http://commandcenter.blogspot.com.au/2014/01/self-referential-functions-and-design.html for more discussion.
type Option func(Device) error

// Option sets the options specified.
// Some options can only be set before the device is initialized; they are best used with NewDevice instead of Option.
func (d *device) Option(opts ...Option) error {
	var err error
	for _, opt := range opts {
		err = opt(d)
	}
	return err
}
