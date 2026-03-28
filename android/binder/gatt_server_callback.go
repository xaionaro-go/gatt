package binder

import (
	"context"
	"fmt"
	"sync"

	"github.com/facebookincubator/go-belt/tool/logger"

	bt "github.com/AndroidGoLab/binder/android/bluetooth"

	"github.com/xaionaro-go/gatt"
)

// gattServerState holds the state for an active GATT server.
type gattServerState struct {
	serverIf int32

	// Maps from handle to gatt objects for dispatch.
	charByHandle map[int32]*gatt.Characteristic
	descByHandle map[int32]*gatt.Descriptor

	// Connected centrals keyed by device address.
	mu       sync.Mutex
	centrals map[string]*central

	// Notifiers keyed by handle + central address.
	notifiers map[string]*serverNotifier
}

// serverNotifier implements gatt.Notifier for a GATT server characteristic.
type serverNotifier struct {
	gs      *gattServerState
	d       *device
	c       *central
	char    *gatt.Characteristic
	handle  int32
	confirm bool // true for indications, false for notifications

	mu   sync.RWMutex
	done bool
}

func (n *serverNotifier) Write(data []byte) (int, error) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if n.done {
		return 0, fmt.Errorf("central stopped notifications")
	}

	rg := n.d.getRawGatt()
	if rg == nil {
		return 0, fmt.Errorf("rawGATT not available")
	}

	ctx := context.Background()
	err := rg.sendNotification(ctx, n.gs.serverIf, n.c.addr, n.handle, n.confirm, data)
	if err != nil {
		return 0, fmt.Errorf("sendNotification: %w", err)
	}
	return len(data), nil
}

func (n *serverNotifier) Done() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.done
}

func (n *serverNotifier) Cap() int {
	return n.c.MTU() - 3 // ATT header overhead
}

func (n *serverNotifier) stop() {
	n.mu.Lock()
	n.done = true
	n.mu.Unlock()
}

// Ensure *serverNotifier satisfies gatt.Notifier at compile time.
var _ gatt.Notifier = (*serverNotifier)(nil)

// notifierKey builds a map key for a notifier from handle and central address.
func notifierKey(handle int32, centralAddr string) string {
	return fmt.Sprintf("%d:%s", handle, centralAddr)
}

// gattServerCallback implements bt.IBluetoothGattServerCallbackServer.
type gattServerCallback struct {
	d  *device
	gs *gattServerState
}

var _ bt.IBluetoothGattServerCallbackServer = (*gattServerCallback)(nil)

func (c *gattServerCallback) OnServerRegistered(
	ctx context.Context,
	status int32,
) error {
	logger.Debugf(ctx, "binder: server registered: status=%d", status)
	// The new AIDL no longer sends serverIf in OnServerRegistered.
	// The server is identified by its callback object.
	return nil
}

func (c *gattServerCallback) OnServerConnectionState(
	ctx context.Context,
	status int32,
	connected bool,
	device bt.BluetoothDevice,
) error {
	// BluetoothDevice has only AddressType; address is not available
	// from the parcelable. Use AddressType as a placeholder identifier.
	address := fmt.Sprintf("device-%d", device.AddressType)
	logger.Debugf(ctx, "binder: server connection state: status=%d connected=%v addr=%s",
		status, connected, address)

	if connected {
		cent := newCentral(c.d, address)
		c.gs.mu.Lock()
		c.gs.centrals[address] = cent
		c.gs.mu.Unlock()

		handler := c.d.CentralConnected()
		if handler != nil {
			go handler(ctx, cent)
		}
	} else {
		c.gs.mu.Lock()
		cent, exists := c.gs.centrals[address]
		delete(c.gs.centrals, address)

		// Stop any notifiers for this central.
		for key, n := range c.gs.notifiers {
			if n.c.addr == address {
				n.stop()
				delete(c.gs.notifiers, key)
			}
		}
		c.gs.mu.Unlock()

		if exists {
			handler := c.d.CentralDisconnected()
			if handler != nil {
				go handler(ctx, cent)
			}
		}
	}
	return nil
}

func (c *gattServerCallback) OnServiceAdded(
	ctx context.Context,
	status int32,
	service bt.BluetoothGattService,
) error {
	logger.Debugf(ctx, "binder: service added: status=%d", status)
	// BluetoothGattService is empty parcelable — no useful data.
	return nil
}

func (c *gattServerCallback) OnCharacteristicReadRequest(
	ctx context.Context,
	device bt.BluetoothDevice,
	transId int32,
	offset int32,
	isLong bool,
	handle int32,
) error {
	address := fmt.Sprintf("device-%d", device.AddressType)
	logger.Debugf(ctx, "binder: char read request: addr=%s transId=%d offset=%d handle=%d",
		address, transId, offset, handle)

	ch := c.gs.charByHandle[handle]
	if ch == nil {
		c.sendResponse(ctx, address, transId, 0, offset, nil)
		return nil
	}

	cent := c.findCentral(address)

	rh := ch.GetReadHandler()
	if rh != nil {
		rw := gatt.NewResponseWriter(cent.MTU() - 1)
		req := &gatt.ReadRequest{
			Request: gatt.Request{Central: cent},
			Cap:     cent.MTU() - 1,
			Offset:  int(offset),
		}
		rh.ServeRead(ctx, rw, req)
		data := rw.Bytes()
		if int(offset) < len(data) {
			data = data[offset:]
		} else {
			data = nil
		}
		c.sendResponse(ctx, address, transId, 0, offset, data)
		return nil
	}

	// No handler — return empty success.
	c.sendResponse(ctx, address, transId, 0, offset, nil)
	return nil
}

func (c *gattServerCallback) OnDescriptorReadRequest(
	ctx context.Context,
	device bt.BluetoothDevice,
	transId int32,
	offset int32,
	isLong bool,
	handle int32,
) error {
	address := fmt.Sprintf("device-%d", device.AddressType)
	logger.Debugf(ctx, "binder: desc read request: addr=%s transId=%d offset=%d handle=%d",
		address, transId, offset, handle)

	// For CCCD, return the current subscription state.
	cent := c.findCentral(address)

	c.gs.mu.Lock()
	key := notifierKey(handle, cent.addr)
	if n := c.gs.notifiers[key]; n != nil && !n.Done() {
		var val []byte
		if n.confirm {
			val = []byte{0x02, 0x00} // Indications enabled
		} else {
			val = []byte{0x01, 0x00} // Notifications enabled
		}
		c.gs.mu.Unlock()
		c.sendResponse(ctx, address, transId, 0, offset, val)
		return nil
	}
	c.gs.mu.Unlock()

	c.sendResponse(ctx, address, transId, 0, offset, []byte{0x00, 0x00})
	return nil
}

func (c *gattServerCallback) OnCharacteristicWriteRequest(
	ctx context.Context,
	device bt.BluetoothDevice,
	transId int32,
	offset int32,
	length int32,
	isPrep bool,
	needRsp bool,
	handle int32,
	value []byte,
) error {
	address := fmt.Sprintf("device-%d", device.AddressType)
	logger.Debugf(ctx, "binder: char write request: addr=%s transId=%d handle=%d len=%d needRsp=%v",
		address, transId, handle, length, needRsp)

	ch := c.gs.charByHandle[handle]
	if ch == nil {
		if needRsp {
			c.sendResponse(ctx, address, transId, 0, offset, nil)
		}
		return nil
	}

	cent := c.findCentral(address)

	wh := ch.GetWriteHandler()
	if wh != nil {
		status := wh.ServeWrite(ctx, gatt.Request{Central: cent}, value)
		if needRsp {
			c.sendResponse(ctx, address, transId, int32(status), offset, nil)
		}
		return nil
	}

	if needRsp {
		c.sendResponse(ctx, address, transId, 0, offset, nil)
	}
	return nil
}

func (c *gattServerCallback) OnDescriptorWriteRequest(
	ctx context.Context,
	device bt.BluetoothDevice,
	transId int32,
	offset int32,
	length int32,
	isPrep bool,
	needRsp bool,
	handle int32,
	value []byte,
) error {
	address := fmt.Sprintf("device-%d", device.AddressType)
	logger.Debugf(ctx, "binder: desc write request: addr=%s transId=%d handle=%d len=%d needRsp=%v",
		address, transId, handle, length, needRsp)

	// Handle CCCD writes to enable/disable notifications.
	cent := c.findCentral(address)
	c.handleCCCDWrite(ctx, cent, handle, value)

	if needRsp {
		c.sendResponse(ctx, address, transId, 0, 0, nil)
	}
	return nil
}

func (c *gattServerCallback) OnExecuteWrite(
	ctx context.Context,
	device bt.BluetoothDevice,
	transId int32,
	execWrite bool,
) error {
	logger.Debugf(ctx, "binder: execute write: transId=%d execWrite=%v",
		transId, execWrite)
	return nil
}

func (c *gattServerCallback) OnNotificationSent(
	ctx context.Context,
	device bt.BluetoothDevice,
	status int32,
) error {
	logger.Debugf(ctx, "binder: notification sent: status=%d", status)
	return nil
}

func (c *gattServerCallback) OnMtuChanged(
	ctx context.Context,
	device bt.BluetoothDevice,
	mtu int32,
) error {
	address := fmt.Sprintf("device-%d", device.AddressType)
	logger.Debugf(ctx, "binder: server MTU changed: addr=%s mtu=%d", address, mtu)

	c.gs.mu.Lock()
	cent := c.gs.centrals[address]
	c.gs.mu.Unlock()

	if cent != nil {
		cent.setMTU(int(mtu))
	}
	return nil
}

func (c *gattServerCallback) OnPhyUpdate(
	ctx context.Context,
	device bt.BluetoothDevice,
	txPhy int32,
	rxPhy int32,
	status int32,
) error {
	logger.Debugf(ctx, "binder: server phy update: txPhy=%d rxPhy=%d status=%d",
		txPhy, rxPhy, status)
	return nil
}

func (c *gattServerCallback) OnPhyRead(
	ctx context.Context,
	device bt.BluetoothDevice,
	txPhy int32,
	rxPhy int32,
	status int32,
) error {
	logger.Debugf(ctx, "binder: server phy read: txPhy=%d rxPhy=%d status=%d",
		txPhy, rxPhy, status)
	return nil
}

func (c *gattServerCallback) OnConnectionUpdated(
	ctx context.Context,
	device bt.BluetoothDevice,
	interval int32,
	latency int32,
	timeout int32,
	status int32,
) error {
	logger.Debugf(ctx, "binder: server connection updated: interval=%d latency=%d timeout=%d status=%d",
		interval, latency, timeout, status)
	return nil
}

func (c *gattServerCallback) OnSubrateChange(
	ctx context.Context,
	device bt.BluetoothDevice,
	subrateMode int32,
	status int32,
) error {
	logger.Debugf(ctx, "binder: server subrate change: subrateMode=%d status=%d",
		subrateMode, status)
	return nil
}

// findCentral looks up a connected central by address.
func (c *gattServerCallback) findCentral(address string) *central {
	c.gs.mu.Lock()
	defer c.gs.mu.Unlock()

	cent := c.gs.centrals[address]
	if cent == nil {
		// Create a temporary central for the request.
		cent = newCentral(c.d, address)
		c.gs.centrals[address] = cent
	}
	return cent
}

// sendResponse sends a GATT server response via raw binder transaction.
func (c *gattServerCallback) sendResponse(
	ctx context.Context,
	address string,
	requestID int32,
	status int32,
	offset int32,
	value []byte,
) {
	logger.Debugf(ctx, "binder: sendResponse: addr=%s reqId=%d status=%d offset=%d len=%d",
		address, requestID, status, offset, len(value))

	rg := c.d.getRawGatt()
	if rg == nil {
		logger.Warnf(ctx, "binder: sendResponse: rawGATT not available")
		return
	}

	if err := rg.sendResponse(ctx, c.gs.serverIf, address, requestID, status, offset, value); err != nil {
		logger.Warnf(ctx, "binder: sendResponse failed: %v", err)
	}
}

// handleCCCDWrite processes a write to the Client Characteristic Configuration Descriptor.
func (c *gattServerCallback) handleCCCDWrite(
	ctx context.Context,
	cent *central,
	handle int32,
	value []byte,
) {
	// Find the parent characteristic for this descriptor handle.
	desc := c.gs.descByHandle[handle]
	if desc == nil {
		return
	}
	ch := desc.Characteristic()
	if ch == nil {
		return
	}

	// Decode the CCCD value.
	var cccdVal uint16
	if len(value) >= 2 {
		cccdVal = uint16(value[0]) | uint16(value[1])<<8
	}

	// Use the characteristic's VHandle as the key since handle refers to the descriptor.
	charHandle := int32(ch.VHandle())
	key := notifierKey(charHandle, cent.addr)

	c.gs.mu.Lock()
	existing := c.gs.notifiers[key]
	c.gs.mu.Unlock()

	switch {
	case cccdVal == 0:
		// Disable notifications/indications.
		if existing != nil {
			existing.stop()
			c.gs.mu.Lock()
			delete(c.gs.notifiers, key)
			c.gs.mu.Unlock()
		}

	default:
		// Enable notifications or indications.
		if existing != nil {
			existing.stop()
		}

		confirm := cccdVal&0x02 != 0 // Indication flag

		n := &serverNotifier{
			gs:      c.gs,
			d:       c.d,
			c:       cent,
			char:    ch,
			handle:  charHandle,
			confirm: confirm,
		}

		c.gs.mu.Lock()
		c.gs.notifiers[key] = n
		c.gs.mu.Unlock()

		// Call the characteristic's NotifyHandler.
		nh := ch.GetNotifyHandler()
		if nh != nil {
			go nh.ServeNotify(ctx, gatt.Request{Central: cent}, n)
		}
	}
}
