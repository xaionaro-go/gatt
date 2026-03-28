package jniproxy

import (
	"context"
	"fmt"
	"io"

	"github.com/facebookincubator/go-belt/tool/logger"

	jniraw "github.com/AndroidGoLab/jni-proxy/proto/jni_raw"
	"github.com/xaionaro-go/gatt"
)

func (d *device) Scan(
	ctx context.Context,
	ss []gatt.UUID,
	dup bool,
) (_err error) {
	logger.Tracef(ctx, "jniproxy.device.Scan")
	defer func() { logger.Tracef(ctx, "/jniproxy.device.Scan: %v", _err) }()

	if d.adapterHandle == 0 {
		return fmt.Errorf("adapter not initialized; call Start first")
	}

	d.mu.Lock()
	if d.scanCtxCancel != nil {
		d.mu.Unlock()
		return fmt.Errorf("already scanning")
	}
	d.mu.Unlock()

	// Get the BLE scanner handle from the adapter via raw JNI.
	adapterCls, err := d.raw.findClass(ctx, "android/bluetooth/BluetoothAdapter")
	if err != nil {
		return fmt.Errorf("find BluetoothAdapter class: %w", err)
	}
	getScannerMethod, err := d.raw.getMethodID(ctx, adapterCls, "getBluetoothLeScanner",
		"()Landroid/bluetooth/le/BluetoothLeScanner;")
	if err != nil {
		return fmt.Errorf("get getBluetoothLeScanner method: %w", err)
	}
	scannerHandle, err := d.raw.callObjectMethod(ctx, d.adapterHandle, getScannerMethod)
	if err != nil {
		return fmt.Errorf("getBluetoothLeScanner: %w", err)
	}
	if scannerHandle == 0 {
		return fmt.Errorf("BluetoothLeScanner is null (Bluetooth may be disabled)")
	}
	d.scannerHandle = scannerHandle

	// Find the ScanCallback class (abstract class).
	scanCallbackCls, err := d.raw.findClass(ctx, "android/bluetooth/le/ScanCallback")
	if err != nil {
		return fmt.Errorf("find ScanCallback class: %w", err)
	}

	// Open a Proxy stream and create a callback proxy.
	proxyStream, err := d.raw.client.Proxy(ctx)
	if err != nil {
		return fmt.Errorf("opening Proxy stream: %w", err)
	}

	err = proxyStream.Send(&jniraw.ProxyClientMessage{
		Msg: &jniraw.ProxyClientMessage_Create{
			Create: &jniraw.CreateProxyRequest{
				InterfaceClassHandles: []int64{scanCallbackCls},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("sending CreateProxyRequest: %w", err)
	}

	resp, err := proxyStream.Recv()
	if err != nil {
		return fmt.Errorf("receiving CreateProxyResponse: %w", err)
	}
	created := resp.GetCreated()
	if created == nil {
		return fmt.Errorf("expected CreateProxyResponse, got %T", resp.GetMsg())
	}
	proxyHandle := created.GetProxyHandle()

	// Start scan: scanner.startScan(callback) via raw JNI.
	scannerCls, err := d.raw.findClass(ctx, "android/bluetooth/le/BluetoothLeScanner")
	if err != nil {
		return fmt.Errorf("find BluetoothLeScanner class: %w", err)
	}
	startScanMethod, err := d.raw.getMethodID(ctx, scannerCls, "startScan",
		"(Landroid/bluetooth/le/ScanCallback;)V")
	if err != nil {
		return fmt.Errorf("get startScan method: %w", err)
	}
	err = d.raw.callVoidMethod(ctx, scannerHandle, startScanMethod, jvalObj(proxyHandle))
	if err != nil {
		return fmt.Errorf("startScan: %w", err)
	}

	scanCtx, scanCancel := context.WithCancel(ctx)
	done := make(chan struct{})

	d.mu.Lock()
	d.scanCtxCancel = scanCancel
	d.scanProxyDone = done
	d.mu.Unlock()

	// Background goroutine to process scan callbacks.
	go d.processScanProxy(scanCtx, proxyStream, ss, dup, proxyHandle, scannerHandle, done)

	return nil
}

// processScanProxy reads callback events from the Proxy stream and dispatches
// scan results to the peripheralDiscovered handler.
func (d *device) processScanProxy(
	ctx context.Context,
	stream jniraw.JNIService_ProxyClient,
	filterUUIDs []gatt.UUID,
	dup bool,
	proxyHandle int64,
	scannerHandle int64,
	done chan struct{},
) {
	defer close(done)

	for {
		msg, err := stream.Recv()
		if err != nil {
			if err != io.EOF && ctx.Err() == nil {
				logger.Warnf(ctx, "scan proxy stream error: %v", err)
			}
			return
		}

		cb := msg.GetCallback()
		if cb == nil {
			continue
		}

		switch cb.GetMethodName() {
		case "onScanResult":
			d.handleOnScanResult(ctx, cb, filterUUIDs, dup)
		case "onScanFailed":
			if len(cb.GetArgHandles()) >= 1 {
				logger.Warnf(ctx, "BLE scan failed: errorCode handle=%d", cb.GetArgHandles()[0])
			}
		}

		// Respond to void callbacks (expects_response=false typically for void,
		// but respond if required).
		if cb.GetExpectsResponse() {
			_ = stream.Send(&jniraw.ProxyClientMessage{
				Msg: &jniraw.ProxyClientMessage_CallbackResponse{
					CallbackResponse: &jniraw.CallbackResponse{
						CallbackId:   cb.GetCallbackId(),
						ResultHandle: 0, // void return
					},
				},
			})
		}
	}
}

// handleOnScanResult extracts device info from a scan result callback.
// Callback args: [callbackType (Integer), scanResult]
func (d *device) handleOnScanResult(
	ctx context.Context,
	cb *jniraw.CallbackEvent,
	filterUUIDs []gatt.UUID,
	dup bool,
) {
	args := cb.GetArgHandles()
	if len(args) < 2 {
		return
	}
	scanResultHandle := args[1]
	if scanResultHandle == 0 {
		return
	}

	// Extract device from ScanResult.
	// ScanResult.getDevice() -> BluetoothDevice
	scanResultCls, err := d.raw.findClass(ctx, "android/bluetooth/le/ScanResult")
	if err != nil {
		logger.Debugf(ctx, "find ScanResult class: %v", err)
		return
	}
	getDeviceMethod, err := d.raw.getMethodID(ctx, scanResultCls, "getDevice", "()Landroid/bluetooth/BluetoothDevice;")
	if err != nil {
		logger.Debugf(ctx, "get getDevice method: %v", err)
		return
	}
	deviceHandle, err := d.raw.callObjectMethod(ctx, scanResultHandle, getDeviceMethod)
	if err != nil || deviceHandle == 0 {
		logger.Debugf(ctx, "ScanResult.getDevice failed: %v", err)
		return
	}

	// Get address via raw JNI (the typed DeviceClient doesn't take a handle parameter).
	addr, err := d.callDeviceStringMethod(ctx, deviceHandle, "getAddress", "()Ljava/lang/String;")
	if err != nil {
		logger.Debugf(ctx, "device.getAddress failed: %v", err)
		return
	}

	// Get name (may be empty).
	name, _ := d.callDeviceStringMethod(ctx, deviceHandle, "getName", "()Ljava/lang/String;")

	// Get RSSI from ScanResult.
	getRssiMethod, err := d.raw.getMethodID(ctx, scanResultCls, "getRssi", "()I")
	if err != nil {
		logger.Debugf(ctx, "get getRssi method: %v", err)
		return
	}
	rssi, _ := d.raw.callIntMethod(ctx, scanResultHandle, getRssiMethod)

	// Find or create the peripheral.
	d.mu.Lock()
	p, exists := d.peripherals[addr]
	if !exists {
		p = newPeripheral(d, addr, name, deviceHandle)
		d.peripherals[addr] = p
	} else {
		if p.name == "" && name != "" {
			p.name = name
		}
		// Update device handle.
		p.btDevHandle = deviceHandle
		if !dup {
			d.mu.Unlock()
			return
		}
	}
	d.mu.Unlock()

	adv := &gatt.Advertisement{
		LocalName: name,
	}

	handler := d.PeripheralDiscovered()
	if handler != nil {
		handler(ctx, p, adv, int(rssi))
	}
}

// callDeviceStringMethod calls a String-returning method on a BluetoothDevice handle.
func (d *device) callDeviceStringMethod(ctx context.Context, deviceHandle int64, methodName, sig string) (string, error) {
	cls, err := d.raw.findClass(ctx, "android/bluetooth/BluetoothDevice")
	if err != nil {
		return "", err
	}
	method, err := d.raw.getMethodID(ctx, cls, methodName, sig)
	if err != nil {
		return "", err
	}
	strHandle, err := d.raw.callObjectMethod(ctx, deviceHandle, method)
	if err != nil {
		return "", err
	}
	if strHandle == 0 {
		return "", nil
	}
	defer func() {
		_ = d.raw.deleteGlobalRef(ctx, strHandle)
	}()
	return d.raw.getString(ctx, strHandle)
}

func (d *device) StopScanning() (_err error) {
	d.mu.Lock()
	cancel := d.scanCtxCancel
	done := d.scanProxyDone

	d.scanCtxCancel = nil
	d.scanProxyDone = nil
	d.mu.Unlock()

	if cancel == nil {
		return nil
	}

	cancel()

	// Wait for the proxy goroutine to finish.
	if done != nil {
		<-done
	}

	return nil
}
