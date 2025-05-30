//go:build ignore
// +build ignore

package main

import (
	"context"
	"fmt"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/gatt"
	"github.com/xaionaro-go/gatt/examples/option"
)

func onStateChanged(ctx context.Context, d gatt.Device, s gatt.State) {
	fmt.Println("State:", s)
	switch s {
	case gatt.StatePoweredOn:
		fmt.Println("scanning...")
		d.Scan(ctx, []gatt.UUID{}, false)
		return
	default:
		d.StopScanning()
	}
}

func onPeriphDiscovered(ctx context.Context, p gatt.Peripheral, a *gatt.Advertisement, rssi int) {
	fmt.Printf("\nPeripheral ID:%s, NAME:(%s)\n", p.ID(), p.Name())
	fmt.Println("  Local Name        =", a.LocalName)
	fmt.Println("  TX Power Level    =", a.TxPowerLevel)
	fmt.Println("  Manufacturer Data =", a.ManufacturerData)
	fmt.Println("  Service Data      =", a.ServiceData)
}

func main() {
	ctx := context.Background()
	d, err := gatt.NewDevice(ctx, option.DefaultClientOptions...)
	if err != nil {
		logger.Fatalf(ctx, "failed to open device, err: %s\n", err)
		return
	}

	// Register handlers.
	d.Handle(ctx, gatt.PeripheralDiscovered(onPeriphDiscovered))
	d.Start(ctx, onStateChanged)
	<-ctx.Done()
}
