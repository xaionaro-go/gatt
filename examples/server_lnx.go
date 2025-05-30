//go:build ignore
// +build ignore

package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"

	"time"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/gatt"
	"github.com/xaionaro-go/gatt/examples/service"
	"github.com/xaionaro-go/gatt/linux/cmd"
)

// server_lnx implements a GATT server.
// It uses some linux specific options for more finer control over the device.

var (
	mc    = flag.Int("mc", 1, "Maximum concurrent connections")
	id    = flag.Duration("id", 0, "ibeacon duration")
	ii    = flag.Duration("ii", 5*time.Second, "ibeacon interval")
	name  = flag.String("name", "Gopher", "Device Name")
	chmap = flag.Int("chmap", 0x7, "Advertising channel map")
	dev   = flag.Int("dev", -1, "HCI device ID")
	chk   = flag.Bool("chk", true, "Check device LE support")
)

// cmdReadBDAddr implements cmd.CmdParam for demonstrating LnxSendHCIRawCommand()
type cmdReadBDAddr struct{}

func (c cmdReadBDAddr) Marshal(b []byte) {}
func (c cmdReadBDAddr) Opcode() int      { return 0x1009 }
func (c cmdReadBDAddr) Len() int         { return 0 }

// Get bdAddr with LnxSendHCIRawCommand() for demo purpose
func bdAddr(ctx context.Context, d gatt.Device) {
	resp := bytes.NewBuffer(nil)
	if err := d.Option(gatt.LnxSendHCIRawCommand(ctx, &cmdReadBDAddr{}, resp)); err != nil {
		fmt.Printf("Failed to send HCI raw command, err: %s", err)
	}
	b := resp.Bytes()
	if b[0] != 0 {
		fmt.Printf("Failed to get bdaddr with HCI Raw command, status: %d", b[0])
	}
	logger.Debugf(ctx, "BD Addr: %02X:%02X:%02X:%02X:%02X:%02X", b[6], b[5], b[4], b[3], b[2], b[1])
}

func main() {
	ctx := context.Background()
	flag.Parse()
	d, err := gatt.NewDevice(
		ctx,
		gatt.LnxMaxConnections(*mc),
		gatt.LnxDeviceID(*dev, *chk),
		gatt.LnxSetAdvertisingParameters(&cmd.LESetAdvertisingParameters{
			AdvertisingIntervalMin: 0x00f4,
			AdvertisingIntervalMax: 0x00f4,
			AdvertisingChannelMap:  0x07,
		}),
	)

	if err != nil {
		logger.Fatalf(ctx, "failed to open device, err: %s", err)
	}

	// Register optional handlers.
	d.Handle(
		ctx,
		gatt.CentralConnected(func(ctx context.Context, c gatt.Central) {
			fmt.Println("Connect: ", c.ID())
		}),
		gatt.CentralDisconnected(func(ctx context.Context, c gatt.Central) {
			fmt.Println("Disconnect: ", c.ID())
		}),
	)

	// A mandatory handler for monitoring device state.
	onStateChanged := func(ctx context.Context, d gatt.Device, s gatt.State) {
		fmt.Printf("State: %s\n", s)
		switch s {
		case gatt.StatePoweredOn:
			// Get bdaddr with LnxSendHCIRawCommand()
			bdAddr(ctx, d)

			// Setup GAP and GATT services.
			d.AddService(ctx, service.NewGapService(*name))
			d.AddService(ctx, service.NewGattService())

			// Add a simple counter service.
			s1 := service.NewCountService()
			d.AddService(ctx, s1)

			// Add a simple counter service.
			s2 := service.NewBatteryService()
			d.AddService(ctx, s2)
			uuids := []gatt.UUID{s1.UUID(), s2.UUID()}

			// If id is zero, advertise name and services statically.
			if *id == time.Duration(0) {
				d.AdvertiseNameAndServices(ctx, *name, uuids)
				break
			}

			// If id is non-zero, advertise name and services and iBeacon alternately.
			go func() {
				for {
					// Advertise as a RedBear Labs iBeacon.
					d.AdvertiseIBeacon(ctx, gatt.MustParseUUID("5AFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"), 1, 2, -59)
					time.Sleep(*id)

					// Advertise name and services.
					d.AdvertiseNameAndServices(ctx, *name, uuids)
					time.Sleep(*ii)
				}
			}()

		default:
		}
	}

	d.Start(ctx, onStateChanged)
	<-ctx.Done()
}
