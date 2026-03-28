//go:build ignore

// binder_scan demonstrates BLE scanning using the binder backend.
//
// The binder backend talks to Android's Bluetooth service directly via
// /dev/binder IPC. It is pure Go (no cgo), but requires shell-level
// access to the device (adb shell) and BLUETOOTH_SCAN / BLUETOOTH_CONNECT
// permissions granted to com.android.shell.
//
// Build and run:
//
//	GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build -o /tmp/binder_scan examples/android/binder_scan.go
//	adb push /tmp/binder_scan /data/local/tmp/
//	adb shell pm grant com.android.shell android.permission.BLUETOOTH_SCAN
//	adb shell pm grant com.android.shell android.permission.BLUETOOTH_CONNECT
//	adb shell /data/local/tmp/binder_scan
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/facebookincubator/go-belt/tool/logger/implementation/logrus"

	"github.com/xaionaro-go/gatt"
	"github.com/xaionaro-go/gatt/android"
	_ "github.com/xaionaro-go/gatt/android/binder"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	ctx = logger.CtxWithLogger(ctx, logrus.Default())

	dev, err := android.NewDevice(ctx, android.BackendBinder)
	if err != nil {
		fmt.Fprintf(os.Stderr, "NewDevice: %v\n", err)
		os.Exit(1)
	}

	dev.Handle(ctx,
		gatt.PeripheralDiscovered(func(
			ctx context.Context,
			p gatt.Peripheral,
			a *gatt.Advertisement,
			rssi int,
		) {
			fmt.Printf("discovered %s %q rssi=%d\n", p.ID(), a.LocalName, rssi)
		}),
	)

	if err := dev.Start(ctx, func(ctx context.Context, d gatt.Device, s gatt.State) {
		fmt.Printf("state: %s\n", s)
	}); err != nil {
		fmt.Fprintf(os.Stderr, "Start: %v\n", err)
		os.Exit(1)
	}

	if err := dev.Scan(ctx, nil, false); err != nil {
		fmt.Fprintf(os.Stderr, "Scan: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("scanning... (ctrl-c to stop)")

	select {
	case <-ctx.Done():
	case <-time.After(30 * time.Second):
	}

	_ = dev.StopScanning()
	_ = dev.Stop()
}
