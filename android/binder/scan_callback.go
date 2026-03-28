package binder

import (
	"context"

	"github.com/facebookincubator/go-belt/tool/logger"

	btle "github.com/AndroidGoLab/binder/android/bluetooth/le"

	"github.com/xaionaro-go/gatt"
)

// scanCallback implements btle.IScannerCallbackServer for BLE scanning.
type scanCallback struct {
	d   *device
	ctx context.Context

	scannerIDCh chan int32
	filterUUIDs []gatt.UUID
	dup         bool
}

var _ btle.IScannerCallbackServer = (*scanCallback)(nil)

func (c *scanCallback) OnScannerRegistered(
	ctx context.Context,
	status int32,
	scannerId int32,
) error {
	logger.Debugf(c.ctx, "binder: scanner registered: status=%d scannerId=%d", status, scannerId)
	if status != 0 {
		// Registration failed — send 0 to signal error.
		select {
		case c.scannerIDCh <- 0:
		default:
		}
		return nil
	}
	select {
	case c.scannerIDCh <- scannerId:
	default:
	}
	return nil
}

func (c *scanCallback) OnScanResult(
	ctx context.Context,
	scanResult btle.ScanResult,
) error {
	// ScanResult is an empty parcelable in the binder library — we cannot
	// extract device address, RSSI, or advertisement data from it.
	// Create a placeholder peripheral and invoke the handler.
	handler := c.d.PeripheralDiscovered()
	if handler != nil {
		p := c.d.getOrCreatePeripheral("unknown")
		handler(c.ctx, p, &gatt.Advertisement{}, 0)
	}
	return nil
}

func (c *scanCallback) OnBatchScanResults(
	ctx context.Context,
	batchResults []btle.ScanResult,
) error {
	logger.Debugf(c.ctx, "binder: batch scan results: count=%d", len(batchResults))
	// ScanResult is empty parcelable — cannot extract data.
	return nil
}

func (c *scanCallback) OnFoundOrLost(
	ctx context.Context,
	onFound bool,
	scanResult btle.ScanResult,
) error {
	logger.Debugf(c.ctx, "binder: scan found/lost: onFound=%v", onFound)
	// ScanResult is empty parcelable — cannot extract data.
	return nil
}

func (c *scanCallback) OnScanManagerErrorCallback(
	ctx context.Context,
	errorCode int32,
) error {
	logger.Warnf(c.ctx, "binder: scan manager error: errorCode=%d", errorCode)
	return nil
}

// getOrCreatePeripheral returns an existing peripheral by address or creates a new one.
func (d *device) getOrCreatePeripheral(addr string) *peripheral {
	d.mu.Lock()
	defer d.mu.Unlock()

	p, exists := d.peripherals[addr]
	if !exists {
		p = newPeripheral(d, addr, "")
		d.peripherals[addr] = p
	}
	return p
}
