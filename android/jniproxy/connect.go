package jniproxy

import (
	"context"
	"fmt"

	"github.com/facebookincubator/go-belt/tool/logger"

	jniraw "github.com/AndroidGoLab/jni-proxy/proto/jni_raw"
	"github.com/xaionaro-go/gatt"
)

func (d *device) Connect(ctx context.Context, p gatt.Peripheral) {
	logger.Tracef(ctx, "jniproxy.device.Connect")
	defer func() { logger.Tracef(ctx, "/jniproxy.device.Connect") }()

	per, ok := p.(*peripheral)
	if !ok {
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, fmt.Errorf("peripheral is not a jniproxy peripheral"))
		}
		return
	}

	// Find BluetoothGattCallback class.
	gattCallbackCls, err := d.raw.findClass(ctx, "android/bluetooth/BluetoothGattCallback")
	if err != nil {
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, fmt.Errorf("find BluetoothGattCallback class: %w", err))
		}
		return
	}

	// Open a Proxy stream for GATT callbacks.
	proxyStream, err := d.raw.client.Proxy(ctx)
	if err != nil {
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, fmt.Errorf("opening Proxy stream: %w", err))
		}
		return
	}

	// Create a callback proxy.
	err = proxyStream.Send(&jniraw.ProxyClientMessage{
		Msg: &jniraw.ProxyClientMessage_Create{
			Create: &jniraw.CreateProxyRequest{
				InterfaceClassHandles: []int64{gattCallbackCls},
			},
		},
	})
	if err != nil {
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, fmt.Errorf("sending CreateProxyRequest: %w", err))
		}
		return
	}

	resp, err := proxyStream.Recv()
	if err != nil {
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, fmt.Errorf("receiving CreateProxyResponse: %w", err))
		}
		return
	}
	created := resp.GetCreated()
	if created == nil {
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, fmt.Errorf("expected CreateProxyResponse, got %T", resp.GetMsg()))
		}
		return
	}
	callbackProxyHandle := created.GetProxyHandle()

	// Call device.connectGatt(context, autoConnect=false, callback) via raw JNI.
	btDevCls, err := d.raw.findClass(ctx, "android/bluetooth/BluetoothDevice")
	if err != nil {
		_ = d.raw.deleteGlobalRef(ctx, callbackProxyHandle)
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, fmt.Errorf("find BluetoothDevice class: %w", err))
		}
		return
	}
	connectGattMethod, err := d.raw.getMethodID(ctx, btDevCls, "connectGatt",
		"(Landroid/content/Context;ZLandroid/bluetooth/BluetoothGattCallback;)Landroid/bluetooth/BluetoothGatt;")
	if err != nil {
		_ = d.raw.deleteGlobalRef(ctx, callbackProxyHandle)
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, fmt.Errorf("get connectGatt method: %w", err))
		}
		return
	}

	gattHandle, err := d.raw.callObjectMethod(ctx, per.btDevHandle, connectGattMethod,
		jvalObj(d.appCtxHandle), jvalBool(false), jvalObj(callbackProxyHandle))
	if err != nil {
		_ = d.raw.deleteGlobalRef(ctx, callbackProxyHandle)
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, fmt.Errorf("connectGatt: %w", err))
		}
		return
	}
	if gattHandle == 0 {
		_ = d.raw.deleteGlobalRef(ctx, callbackProxyHandle)
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, fmt.Errorf("connectGatt returned null"))
		}
		return
	}

	proxyDone := make(chan struct{})

	per.mu.Lock()
	per.gattHandle = gattHandle
	per.proxyStream = proxyStream
	per.proxyDone = proxyDone
	per.mu.Unlock()

	// Start processing GATT callbacks.
	go per.processGattProxy(ctx, proxyStream, proxyDone)

	// Wait for onConnectionStateChange.
	select {
	case <-ctx.Done():
		handler := d.PeripheralConnected()
		if handler != nil {
			handler(ctx, p, ctx.Err())
		}
		return
	case newState := <-per.connStateChanged:
		if newState != stateConnected {
			_ = d.raw.deleteGlobalRef(ctx, callbackProxyHandle)
			_ = d.raw.deleteGlobalRef(ctx, gattHandle)

			per.mu.Lock()
			per.gattHandle = 0
			per.proxyStream = nil
			per.proxyDone = nil
			per.mu.Unlock()

			handler := d.PeripheralConnected()
			if handler != nil {
				handler(ctx, p, fmt.Errorf("connection failed: state=%d", newState))
			}
			return
		}
	}

	handler := d.PeripheralConnected()
	if handler != nil {
		handler(ctx, p, nil)
	}

	// Monitor for disconnection in a separate goroutine.
	go d.monitorDisconnection(ctx, per, callbackProxyHandle)
}

// monitorDisconnection waits for a disconnection event and cleans up resources.
func (d *device) monitorDisconnection(
	ctx context.Context,
	per *peripheral,
	callbackProxyHandle int64,
) {
	select {
	case <-ctx.Done():
	case newState := <-per.connStateChanged:
		_ = newState // Always means disconnected at this point.
	}

	per.mu.Lock()
	g := per.gattHandle
	per.gattHandle = 0
	per.mu.Unlock()

	if g != 0 {
		d.closeGatt(ctx, g)
		_ = d.raw.deleteGlobalRef(ctx, g)
	}
	_ = d.raw.deleteGlobalRef(ctx, callbackProxyHandle)

	handler := d.PeripheralDisconnected()
	if handler != nil {
		handler(ctx, per, nil)
	}
}

// closeGatt calls BluetoothGatt.close() via raw JNI.
func (d *device) closeGatt(ctx context.Context, gattHandle int64) {
	gattCls, err := d.raw.findClass(ctx, "android/bluetooth/BluetoothGatt")
	if err != nil {
		return
	}
	closeMethod, err := d.raw.getMethodID(ctx, gattCls, "close", "()V")
	if err != nil {
		return
	}
	_ = d.raw.callVoidMethod(ctx, gattHandle, closeMethod)
}

// disconnectGatt calls BluetoothGatt.disconnect() via raw JNI.
func (d *device) disconnectGatt(ctx context.Context, gattHandle int64) {
	gattCls, err := d.raw.findClass(ctx, "android/bluetooth/BluetoothGatt")
	if err != nil {
		return
	}
	disconnectMethod, err := d.raw.getMethodID(ctx, gattCls, "disconnect", "()V")
	if err != nil {
		return
	}
	_ = d.raw.callVoidMethod(ctx, gattHandle, disconnectMethod)
}

func (d *device) CancelConnection(ctx context.Context, p gatt.Peripheral) {
	logger.Tracef(ctx, "jniproxy.device.CancelConnection")
	defer func() { logger.Tracef(ctx, "/jniproxy.device.CancelConnection") }()

	per, ok := p.(*peripheral)
	if !ok {
		return
	}

	per.mu.Lock()
	g := per.gattHandle
	per.mu.Unlock()

	if g == 0 {
		return
	}

	// Disconnect triggers onConnectionStateChange -> disconnected,
	// which is handled by monitorDisconnection.
	d.disconnectGatt(ctx, g)
}
