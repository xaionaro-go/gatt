package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"time"

	bt "github.com/AndroidGoLab/binder/android/bluetooth"
	btle "github.com/AndroidGoLab/binder/android/bluetooth/le"
	"github.com/AndroidGoLab/binder/android/content"
	androidOS "github.com/AndroidGoLab/binder/android/os"
	binderpkg "github.com/AndroidGoLab/binder/binder"
	"github.com/AndroidGoLab/binder/binder/versionaware"
	"github.com/AndroidGoLab/binder/kernelbinder"
	"github.com/AndroidGoLab/binder/parcel"
	"github.com/AndroidGoLab/binder/servicemanager"
)

// shellAttribution returns an AttributionSource for the current process.
func shellAttribution() content.AttributionSource {
	return content.AttributionSource{
		AttributionSourceState: content.AttributionSourceState{
			Pid:         int32(os.Getpid()),
			Uid:         int32(os.Getuid()),
			PackageName: "com.android.shell",
		},
	}
}

// writeRawAttributionSource writes an AttributionSourceState for the shell process
// (used only for raw GATT transactions that still build parcels manually).
func writeRawAttributionSource(p *parcel.Parcel) {
	attr := shellAttribution()
	p.WriteInt32(1) // non-null marker
	if err := attr.MarshalParcel(p); err != nil {
		panic(fmt.Sprintf("MarshalParcel: %v", err))
	}
}

type noopMgrCallback struct{}

func (n *noopMgrCallback) OnBluetoothServiceUp(context.Context, binderpkg.IBinder) error { return nil }
func (n *noopMgrCallback) OnBluetoothServiceDown(context.Context) error                  { return nil }
func (n *noopMgrCallback) OnBluetoothOn(context.Context) error                           { return nil }
func (n *noopMgrCallback) OnBluetoothOff(context.Context) error                          { return nil }

// rawGattCallback handles IBluetoothGattCallback at the raw parcel level.
type rawGattCallback struct {
	registeredCh chan int32
}

func (c *rawGattCallback) Descriptor() string {
	return bt.DescriptorIBluetoothGattCallback
}

func (c *rawGattCallback) OnTransaction(
	_ context.Context,
	code binderpkg.TransactionCode,
	data *parcel.Parcel,
) (*parcel.Parcel, error) {
	if _, err := data.ReadInterfaceToken(); err != nil {
		return nil, err
	}
	// TRANSACTION_onClientRegistered = first call on API 36.
	if code == binderpkg.FirstCallTransaction+0 {
		status, err := data.ReadInt32()
		if err != nil {
			return nil, err
		}
		select {
		case c.registeredCh <- status:
		default:
		}
	}
	return nil, nil
}

// scanCallbackSpy captures scan callback events.
type scanCallbackSpy struct {
	registeredCh chan scanRegisteredEvent
	resultCh     chan btle.ScanResult
}

type scanRegisteredEvent struct {
	status    int32
	scannerID int32
}

func (s *scanCallbackSpy) OnScannerRegistered(_ context.Context, status, scannerID int32) error {
	select {
	case s.registeredCh <- scanRegisteredEvent{status: status, scannerID: scannerID}:
	default:
	}
	return nil
}
func (s *scanCallbackSpy) OnScanResult(_ context.Context, result btle.ScanResult) error {
	select {
	case s.resultCh <- result:
	default:
	}
	return nil
}
func (s *scanCallbackSpy) OnBatchScanResults(context.Context, []btle.ScanResult) error { return nil }
func (s *scanCallbackSpy) OnFoundOrLost(context.Context, bool, btle.ScanResult) error  { return nil }
func (s *scanCallbackSpy) OnScanManagerErrorCallback(context.Context, int32) error      { return nil }

var _ btle.IScannerCallbackServer = (*scanCallbackSpy)(nil)

func main() {
	ctx := context.Background()

	fmt.Println("=== Direct binder Bluetooth test (typed proxies) ===")

	fmt.Println("\n1. Opening binder driver...")
	drv, err := kernelbinder.Open(ctx, binderpkg.WithMapSize(128*1024))
	if err != nil {
		fmt.Fprintf(os.Stderr, "kernelbinder.Open: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = drv.Close(ctx) }()
	fmt.Println("   OK")

	fmt.Println("\n2. Creating version-aware transport...")
	transport, err := versionaware.NewTransport(ctx, drv, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "versionaware.NewTransport: %v\n", err)
		os.Exit(1)
	}
	defer func() { _ = transport.Close(ctx) }()
	fmt.Println("   OK")

	fmt.Println("\n3. Getting bluetooth_manager service...")
	sm := servicemanager.New(transport)
	svc, err := sm.GetService(ctx, "bluetooth_manager")
	if err != nil {
		fmt.Fprintf(os.Stderr, "GetService: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   OK (handle=%d)\n", svc.Handle())

	mgr := bt.NewBluetoothManagerProxy(svc)

	fmt.Println("\n4. Checking Bluetooth state...")
	state, err := mgr.GetState(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "GetState: %v\n", err)
	} else {
		fmt.Printf("   State: %d\n", state)
	}

	fmt.Println("\n5. RegisterAdapter...")
	cb := bt.NewBluetoothManagerCallbackStub(&noopMgrCallback{})
	btAdapterBinder, err := mgr.RegisterAdapter(ctx, cb)
	if err != nil {
		fmt.Fprintf(os.Stderr, "RegisterAdapter: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("   OK (IBluetooth handle=%d)\n", btAdapterBinder.Handle())

	// Create typed BluetoothProxy.
	btProxy := bt.NewBluetoothProxy(btAdapterBinder)

	// 6. Get IBluetoothGatt via typed proxy.
	fmt.Println("\n6. Getting IBluetoothGatt via BluetoothProxy.GetBluetoothGatt()...")
	gattBinder, err := btProxy.GetBluetoothGatt(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "GetBluetoothGatt: %v\n", err)
		os.Exit(1)
	}
	if gattBinder == nil {
		fmt.Fprintf(os.Stderr, "GetBluetoothGatt returned nil\n")
		os.Exit(1)
	}
	fmt.Printf("   OK (IBluetoothGatt handle=%d)\n", gattBinder.Handle())

	// 7. RegisterClient on IBluetoothGatt (raw transaction — typed proxy has issues).
	fmt.Println("\n7. RegisterClient (two-way, flags=0)...")
	{
		regCode, err := gattBinder.ResolveCode(ctx, bt.DescriptorIBluetoothGatt, "registerClient")
		if err != nil {
			fmt.Fprintf(os.Stderr, "ResolveCode(registerClient): %v\n", err)
			os.Exit(1)
		}

		spy := &rawGattCallback{registeredCh: make(chan int32, 1)}
		gattCallbackBinder := binderpkg.NewStubBinder(spy)

		regData := parcel.New()
		regData.WriteInterfaceToken(bt.DescriptorIBluetoothGatt)
		regData.WriteInt32(1) // non-null ParcelUuid
		regData.WriteInt64(0x0000180000001000)
		regData.WriteInt64(-9223371485494954757)
		binderpkg.WriteBinderToParcel(ctx, regData, gattCallbackBinder, transport)
		regData.WriteBool(false) // eatt_support
		regData.WriteInt32(0)    // transport = auto
		writeRawAttributionSource(regData)

		regReply, err := gattBinder.Transact(ctx, regCode, 0, regData)
		regData.Recycle()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Transact(registerClient): %v\n", err)
			os.Exit(1)
		}
		if statusErr := binderpkg.ReadStatus(regReply); statusErr != nil {
			fmt.Fprintf(os.Stderr, "registerClient status: %v\n", statusErr)
			os.Exit(1)
		}

		select {
		case status := <-spy.registeredCh:
			fmt.Printf("   OnClientRegistered: status=%d\n", status)
		case <-time.After(5 * time.Second):
			fmt.Fprintf(os.Stderr, "   TIMEOUT: OnClientRegistered never arrived\n")
			os.Exit(1)
		}
	}

	// 8. BLE Scan via IBluetoothScan typed proxy.
	fmt.Println("\n8. BLE Scan via BluetoothScanProxy...")
	{
		scanBinder, err := btProxy.GetBluetoothScan(ctx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "GetBluetoothScan: %v\n", err)
			os.Exit(1)
		}
		if scanBinder == nil {
			fmt.Fprintf(os.Stderr, "GetBluetoothScan returned nil\n")
			os.Exit(1)
		}
		fmt.Printf("   IBluetoothScan handle=%d\n", scanBinder.Handle())

		scanProxy := bt.NewBluetoothScanProxy(scanBinder)

		scanSpy := &scanCallbackSpy{
			registeredCh: make(chan scanRegisteredEvent, 1),
			resultCh:     make(chan btle.ScanResult, 10),
		}
		scanCallback := btle.NewScannerCallbackStub(scanSpy)
		attr := shellAttribution()

		// Register scanner via typed proxy.
		err = scanProxy.RegisterScanner(ctx, scanCallback, androidOS.WorkSource{}, attr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "RegisterScanner: %v\n", err)
			os.Exit(1)
		}

		var scannerID int32
		select {
		case event := <-scanSpy.registeredCh:
			fmt.Printf("   OnScannerRegistered: status=%d scannerID=%d\n", event.status, event.scannerID)
			if event.status != 0 {
				fmt.Fprintf(os.Stderr, "   Scanner registration failed\n")
				os.Exit(1)
			}
			scannerID = event.scannerID
		case <-time.After(5 * time.Second):
			fmt.Fprintf(os.Stderr, "   TIMEOUT: OnScannerRegistered never arrived\n")
			os.Exit(1)
		}

		// Start scan via typed proxy.
		err = scanProxy.StartScan(ctx, scannerID, btle.ScanSettings{}, nil, attr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "StartScan: %v\n", err)
			os.Exit(1)
		}

		fmt.Println("   Scanning for 3 seconds...")
		deadline := time.After(3 * time.Second)
		resultCount := 0
	collectLoop:
		for {
			select {
			case <-scanSpy.resultCh:
				resultCount++
			case <-deadline:
				break collectLoop
			}
		}
		fmt.Printf("   Received %d scan result callbacks\n", resultCount)

		// Stop scan via typed proxy.
		err = scanProxy.StopScan(ctx, scannerID, attr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "StopScan: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("   StopScan OK")

		// Unregister scanner.
		err = scanProxy.UnregisterScanner(ctx, scannerID, attr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "UnregisterScanner: %v\n", err)
		} else {
			fmt.Println("   UnregisterScanner OK")
		}
	}

	fmt.Println("\n=== TEST PASSED ===")
}

// Ensure binary.BigEndian is used (for UUID conversion in library code).
var _ = binary.BigEndian
