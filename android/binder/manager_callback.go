package binder

import (
	"context"

	"github.com/facebookincubator/go-belt/tool/logger"

	bt "github.com/AndroidGoLab/binder/android/bluetooth"
	binderpkg "github.com/AndroidGoLab/binder/binder"
)

// managerCallback implements bt.IBluetoothManagerCallbackServer.
// It receives notifications when the Bluetooth service comes up/down.
type managerCallback struct {
	d   *device
	ctx context.Context
}

var _ bt.IBluetoothManagerCallbackServer = (*managerCallback)(nil)

func (c *managerCallback) OnBluetoothServiceUp(
	ctx context.Context,
	bluetoothService binderpkg.IBinder,
) error {
	logger.Debugf(c.ctx, "binder: bluetooth service up")
	c.d.btProxy = bt.NewBluetoothProxy(bluetoothService)
	return nil
}

func (c *managerCallback) OnBluetoothServiceDown(ctx context.Context) error {
	logger.Debugf(c.ctx, "binder: bluetooth service down")
	c.d.btProxy = nil
	return nil
}

func (c *managerCallback) OnBluetoothOn(ctx context.Context) error {
	logger.Debugf(c.ctx, "binder: bluetooth on")
	return nil
}

func (c *managerCallback) OnBluetoothOff(ctx context.Context) error {
	logger.Debugf(c.ctx, "binder: bluetooth off")
	return nil
}
