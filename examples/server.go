//go:build ignore
// +build ignore

package main

import (
	"context"
	"fmt"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/gatt"
	"github.com/xaionaro-go/gatt/examples/option"
	"github.com/xaionaro-go/gatt/examples/service"
)

func main() {
	ctx := context.Background()

	d, err := gatt.NewDevice(ctx, option.DefaultServerOptions...)
	if err != nil {
		logger.Fatalf(ctx, "Failed to open device, err: %s", err)
	}

	// Register optional handlers.
	d.Handle(
		ctx,
		gatt.CentralConnected(func(ctx context.Context, c gatt.Central) { fmt.Println("Connect: ", c.ID()) }),
		gatt.CentralDisconnected(func(ctx context.Context, c gatt.Central) { fmt.Println("Disconnect: ", c.ID()) }),
	)

	// A mandatory handler for monitoring device state.
	onStateChanged := func(ctx context.Context, d gatt.Device, s gatt.State) {
		fmt.Printf("State: %s\n", s)
		switch s {
		case gatt.StatePoweredOn:
			// Setup GAP and GATT services for Linux implementation.
			// OS X doesn't export the access of these services.
			d.AddService(ctx, service.NewGapService("Gopher")) // no effect on OS X
			d.AddService(ctx, service.NewGattService())        // no effect on OS X

			// A simple count service for demo.
			s1 := service.NewCountService()
			d.AddService(ctx, s1)

			// A fake battery service for demo.
			s2 := service.NewBatteryService()
			d.AddService(ctx, s2)

			// Advertise device name and service's UUIDs.
			d.AdvertiseNameAndServices(ctx, "Gopher", []gatt.UUID{s1.UUID(), s2.UUID()})

			// Advertise as an OpenBeacon iBeacon
			d.AdvertiseIBeacon(ctx, gatt.MustParseUUID("AA6062F098CA42118EC4193EB73CCEB6"), 1, 2, -59)

		default:
		}
	}

	d.Start(ctx, onStateChanged)
	select {}
}
