package binder

import (
	"context"
	"fmt"

	"github.com/facebookincubator/go-belt/tool/logger"

	bt "github.com/AndroidGoLab/binder/android/bluetooth"

	"github.com/xaionaro-go/gatt"
)

// gattClientCallback implements bt.IBluetoothGattCallbackServer for GATT
// client operations. It dispatches callback events to the appropriate
// synchronization channels on the peripheral.
type gattClientCallback struct {
	d   *device
	per *peripheral
}

var _ bt.IBluetoothGattCallbackServer = (*gattClientCallback)(nil)

func (c *gattClientCallback) OnClientRegistered(
	ctx context.Context,
	status int32,
) error {
	logger.Debugf(ctx, "binder: client registered: status=%d", status)
	if status != 0 {
		select {
		case c.per.clientRegistered <- 0:
		default:
		}
		return nil
	}
	// The new AIDL no longer sends clientIf in OnClientRegistered.
	// Signal success; the peripheral uses the callback identity instead.
	select {
	case c.per.clientRegistered <- 1:
	default:
	}
	return nil
}

func (c *gattClientCallback) OnClientConnectionState(
	ctx context.Context,
	status int32,
	connected bool,
	device bt.BluetoothDevice,
) error {
	logger.Debugf(ctx, "binder: connection state: status=%d connected=%v",
		status, connected)
	select {
	case c.per.connStateChanged <- connected:
	default:
	}
	return nil
}

func (c *gattClientCallback) OnPhyUpdate(
	ctx context.Context,
	device bt.BluetoothDevice,
	txPhy int32,
	rxPhy int32,
	status int32,
) error {
	logger.Debugf(ctx, "binder: phy update: txPhy=%d rxPhy=%d status=%d",
		txPhy, rxPhy, status)
	return nil
}

func (c *gattClientCallback) OnPhyRead(
	ctx context.Context,
	device bt.BluetoothDevice,
	txPhy int32,
	rxPhy int32,
	status int32,
) error {
	logger.Debugf(ctx, "binder: phy read: txPhy=%d rxPhy=%d status=%d",
		txPhy, rxPhy, status)
	return nil
}

func (c *gattClientCallback) OnSearchComplete(
	ctx context.Context,
	device bt.BluetoothDevice,
	services []bt.BluetoothGattService,
	status int32,
) error {
	logger.Debugf(ctx, "binder: search complete: services=%d status=%d",
		len(services), status)
	var err error
	if status != 0 {
		err = fmt.Errorf("onSearchComplete status=%d", status)
	}
	select {
	case c.per.searchComplete <- err:
	default:
	}
	return nil
}

func (c *gattClientCallback) OnCharacteristicRead(
	ctx context.Context,
	device bt.BluetoothDevice,
	status int32,
	handle int32,
	value []byte,
) error {
	logger.Debugf(ctx, "binder: characteristic read: status=%d handle=%d len=%d",
		status, handle, len(value))
	var result charReadResult
	if status != 0 {
		result.err = fmt.Errorf("onCharacteristicRead status=%d", status)
	} else {
		result.data = value
	}
	select {
	case c.per.charRead <- result:
	default:
	}
	return nil
}

func (c *gattClientCallback) OnCharacteristicWrite(
	ctx context.Context,
	device bt.BluetoothDevice,
	status int32,
	handle int32,
	value []byte,
) error {
	logger.Debugf(ctx, "binder: characteristic write: status=%d handle=%d",
		status, handle)
	var err error
	if status != 0 {
		err = fmt.Errorf("onCharacteristicWrite status=%d", status)
	}
	select {
	case c.per.charWritten <- err:
	default:
	}
	return nil
}

func (c *gattClientCallback) OnExecuteWrite(
	ctx context.Context,
	device bt.BluetoothDevice,
	status int32,
) error {
	logger.Debugf(ctx, "binder: execute write: status=%d", status)
	return nil
}

func (c *gattClientCallback) OnDescriptorRead(
	ctx context.Context,
	device bt.BluetoothDevice,
	status int32,
	handle int32,
	value []byte,
) error {
	logger.Debugf(ctx, "binder: descriptor read: status=%d handle=%d len=%d",
		status, handle, len(value))
	var result descReadResult
	if status != 0 {
		result.err = fmt.Errorf("onDescriptorRead status=%d", status)
	} else {
		result.data = value
	}
	select {
	case c.per.descRead <- result:
	default:
	}
	return nil
}

func (c *gattClientCallback) OnDescriptorWrite(
	ctx context.Context,
	device bt.BluetoothDevice,
	status int32,
	handle int32,
	value []byte,
) error {
	logger.Debugf(ctx, "binder: descriptor write: status=%d handle=%d",
		status, handle)
	var err error
	if status != 0 {
		err = fmt.Errorf("onDescriptorWrite status=%d", status)
	}
	select {
	case c.per.descWritten <- err:
	default:
	}
	return nil
}

func (c *gattClientCallback) OnNotify(
	ctx context.Context,
	device bt.BluetoothDevice,
	handle int32,
	value []byte,
) error {
	logger.Debugf(ctx, "binder: notify: handle=%d len=%d",
		handle, len(value))
	vh := uint16(handle)
	c.per.mu.Lock()
	fn := c.per.subscribers[vh]
	c.per.mu.Unlock()

	if fn != nil {
		// Look up the characteristic by handle.
		var matchedChar *gatt.Characteristic
		for _, svc := range c.per.services {
			for _, ch := range svc.Characteristics() {
				if ch.VHandle() == vh {
					matchedChar = ch
					break
				}
			}
			if matchedChar != nil {
				break
			}
		}
		if matchedChar != nil {
			go fn(matchedChar, value, nil)
		}
	}
	return nil
}

func (c *gattClientCallback) OnReadRemoteRssi(
	ctx context.Context,
	device bt.BluetoothDevice,
	rssi int32,
	status int32,
) error {
	logger.Debugf(ctx, "binder: read remote rssi: rssi=%d status=%d",
		rssi, status)
	var r rssiResult
	if status != 0 {
		r.err = fmt.Errorf("onReadRemoteRssi status=%d", status)
	} else {
		r.rssi = rssi
	}
	select {
	case c.per.rssiRead <- r:
	default:
	}
	return nil
}

func (c *gattClientCallback) OnConfigureMTU(
	ctx context.Context,
	device bt.BluetoothDevice,
	mtu int32,
	status int32,
) error {
	logger.Debugf(ctx, "binder: configure MTU: mtu=%d status=%d",
		mtu, status)
	var r mtuResult
	if status != 0 {
		r.err = fmt.Errorf("onConfigureMTU status=%d", status)
	} else {
		r.mtu = mtu
	}
	select {
	case c.per.mtuChanged <- r:
	default:
	}
	return nil
}

func (c *gattClientCallback) OnConnectionUpdated(
	ctx context.Context,
	device bt.BluetoothDevice,
	interval int32,
	latency int32,
	timeout int32,
	status int32,
) error {
	logger.Debugf(ctx, "binder: connection updated: interval=%d latency=%d timeout=%d status=%d",
		interval, latency, timeout, status)
	return nil
}

func (c *gattClientCallback) OnServiceChanged(
	ctx context.Context,
	device bt.BluetoothDevice,
) error {
	logger.Debugf(ctx, "binder: service changed")
	return nil
}

func (c *gattClientCallback) OnSubrateChange(
	ctx context.Context,
	device bt.BluetoothDevice,
	subrateMode int32,
	status int32,
) error {
	logger.Debugf(ctx, "binder: subrate change: subrateMode=%d status=%d",
		subrateMode, status)
	return nil
}
