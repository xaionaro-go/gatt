package binder

import (
	"github.com/xaionaro-go/gatt"
)

type config struct {
	mapSize   uint32
	targetAPI int
}

// WithMapSize sets the mmap size for the Binder driver.
// Default is 1 MB. Must be a multiple of the page size.
func WithMapSize(n uint32) gatt.Option {
	return func(d gatt.Device) error {
		dd, ok := d.(*device)
		if !ok {
			return nil
		}
		dd.cfg.mapSize = n
		return nil
	}
}

// WithTargetAPI sets the Android API level for version-aware binder
// transaction code resolution. If zero, the library auto-detects it.
func WithTargetAPI(n int) gatt.Option {
	return func(d gatt.Device) error {
		dd, ok := d.(*device)
		if !ok {
			return nil
		}
		dd.cfg.targetAPI = n
		return nil
	}
}
