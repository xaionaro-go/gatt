package binder

import (
	"context"
	"encoding/binary"
	"fmt"

	"github.com/facebookincubator/go-belt/tool/logger"

	bt "github.com/AndroidGoLab/binder/android/bluetooth"
	binderpkg "github.com/AndroidGoLab/binder/binder"
	"github.com/AndroidGoLab/binder/parcel"
)

// rawGATT wraps raw binder transactions to IBluetoothGatt, working around
// the generated proxy's unserializable "receiver any" parameter.
// Each method builds the parcel manually and uses synchronous two-way
// transactions (flags=0), matching the verified E2E test pattern.
//
// Scanning has been moved to the typed BluetoothScanProxy.
type rawGATT struct {
	gattBinder binderpkg.IBinder
	transport  binderpkg.VersionAwareTransport
}

// writeRawAttributionSource writes an AttributionSourceState for the shell process
// into a raw parcel, matching the wire format expected by the Bluetooth service.
func writeRawAttributionSource(p *parcel.Parcel) {
	attr := shellAttribution()
	p.WriteInt32(1) // non-null marker
	if err := attr.MarshalParcel(p); err != nil {
		// MarshalParcel on AttributionSource should not fail.
		panic(fmt.Sprintf("MarshalParcel(AttributionSource): %v", err))
	}
}

// resolveAndTransact resolves a method code and performs a synchronous two-way
// transaction (flags=0), then checks the reply status.
func (g *rawGATT) resolveAndTransact(
	ctx context.Context,
	method string,
	data *parcel.Parcel,
) error {
	code, err := g.gattBinder.ResolveCode(ctx, bt.DescriptorIBluetoothGatt, method)
	if err != nil {
		return fmt.Errorf("resolving %s: %w", method, err)
	}

	reply, err := g.gattBinder.Transact(ctx, code, 0, data)
	if err != nil {
		return fmt.Errorf("transacting %s: %w", method, err)
	}
	if statusErr := binderpkg.ReadStatus(reply); statusErr != nil {
		return fmt.Errorf("%s status: %w", method, statusErr)
	}
	return nil
}

func (g *rawGATT) registerClient(
	ctx context.Context,
	appUUID [16]byte,
	callback bt.IBluetoothGattCallback,
	eattSupport bool,
) (_err error) {
	logger.Tracef(ctx, "rawGATT.registerClient")
	defer func() { logger.Tracef(ctx, "/rawGATT.registerClient: %v", _err) }()

	data := parcel.New()
	defer data.Recycle()
	data.WriteInterfaceToken(bt.DescriptorIBluetoothGatt)

	// ParcelUuid: non-null marker + mostSigBits + leastSigBits
	data.WriteInt32(1)
	data.WriteInt64(int64(binary.BigEndian.Uint64(appUUID[:8])))
	data.WriteInt64(int64(binary.BigEndian.Uint64(appUUID[8:])))

	// IBluetoothGattCallback binder
	binderpkg.WriteBinderToParcel(ctx, data, callback.AsBinder(), g.transport)

	// eatt_support
	data.WriteBool(eattSupport)

	// transport = auto (0)
	data.WriteInt32(0)

	// AttributionSource
	writeRawAttributionSource(data)

	return g.resolveAndTransact(ctx, bt.MethodIBluetoothGattRegisterClient, data)
}

func (g *rawGATT) unregisterClient(
	ctx context.Context,
	clientIf int32,
) (_err error) {
	logger.Tracef(ctx, "rawGATT.unregisterClient")
	defer func() { logger.Tracef(ctx, "/rawGATT.unregisterClient: %v", _err) }()

	data := parcel.New()
	defer data.Recycle()
	data.WriteInterfaceToken(bt.DescriptorIBluetoothGatt)
	data.WriteInt32(clientIf)
	writeRawAttributionSource(data)

	return g.resolveAndTransact(ctx, bt.MethodIBluetoothGattUnregisterClient, data)
}

func (g *rawGATT) clientConnect(
	ctx context.Context,
	clientIf int32,
	address string,
	addressType int32,
	isDirect bool,
	transport int32,
	opportunistic bool,
	phy int32,
) (_err error) {
	logger.Tracef(ctx, "rawGATT.clientConnect")
	defer func() { logger.Tracef(ctx, "/rawGATT.clientConnect: %v", _err) }()

	data := parcel.New()
	defer data.Recycle()
	data.WriteInterfaceToken(bt.DescriptorIBluetoothGatt)
	data.WriteInt32(clientIf)
	data.WriteString16(address)
	data.WriteInt32(addressType)
	data.WriteBool(isDirect)
	data.WriteInt32(transport)
	data.WriteBool(opportunistic)
	data.WriteInt32(phy)
	writeRawAttributionSource(data)

	return g.resolveAndTransact(ctx, bt.MethodIBluetoothGattClientConnect, data)
}

func (g *rawGATT) clientDisconnect(
	ctx context.Context,
	clientIf int32,
	address string,
) (_err error) {
	logger.Tracef(ctx, "rawGATT.clientDisconnect")
	defer func() { logger.Tracef(ctx, "/rawGATT.clientDisconnect: %v", _err) }()

	data := parcel.New()
	defer data.Recycle()
	data.WriteInterfaceToken(bt.DescriptorIBluetoothGatt)
	data.WriteInt32(clientIf)
	data.WriteString16(address)
	writeRawAttributionSource(data)

	return g.resolveAndTransact(ctx, bt.MethodIBluetoothGattClientDisconnect, data)
}

func (g *rawGATT) discoverServices(
	ctx context.Context,
	clientIf int32,
	address string,
) (_err error) {
	logger.Tracef(ctx, "rawGATT.discoverServices")
	defer func() { logger.Tracef(ctx, "/rawGATT.discoverServices: %v", _err) }()

	data := parcel.New()
	defer data.Recycle()
	data.WriteInterfaceToken(bt.DescriptorIBluetoothGatt)
	data.WriteInt32(clientIf)
	data.WriteString16(address)
	writeRawAttributionSource(data)

	return g.resolveAndTransact(ctx, bt.MethodIBluetoothGattDiscoverServices, data)
}

func (g *rawGATT) readCharacteristic(
	ctx context.Context,
	clientIf int32,
	address string,
	handle int32,
	authReq int32,
) (_err error) {
	logger.Tracef(ctx, "rawGATT.readCharacteristic")
	defer func() { logger.Tracef(ctx, "/rawGATT.readCharacteristic: %v", _err) }()

	data := parcel.New()
	defer data.Recycle()
	data.WriteInterfaceToken(bt.DescriptorIBluetoothGatt)
	data.WriteInt32(clientIf)
	data.WriteString16(address)
	data.WriteInt32(handle)
	data.WriteInt32(authReq)
	writeRawAttributionSource(data)

	return g.resolveAndTransact(ctx, bt.MethodIBluetoothGattReadCharacteristic, data)
}

func (g *rawGATT) writeCharacteristic(
	ctx context.Context,
	clientIf int32,
	address string,
	handle int32,
	writeType int32,
	authReq int32,
	value []byte,
) (_err error) {
	logger.Tracef(ctx, "rawGATT.writeCharacteristic")
	defer func() { logger.Tracef(ctx, "/rawGATT.writeCharacteristic: %v", _err) }()

	data := parcel.New()
	defer data.Recycle()
	data.WriteInterfaceToken(bt.DescriptorIBluetoothGatt)
	data.WriteInt32(clientIf)
	data.WriteString16(address)
	data.WriteInt32(handle)
	data.WriteInt32(writeType)
	data.WriteInt32(authReq)
	data.WriteByteArray(value)
	writeRawAttributionSource(data)

	return g.resolveAndTransact(ctx, bt.MethodIBluetoothGattWriteCharacteristic, data)
}

func (g *rawGATT) readDescriptor(
	ctx context.Context,
	clientIf int32,
	address string,
	handle int32,
	authReq int32,
) (_err error) {
	logger.Tracef(ctx, "rawGATT.readDescriptor")
	defer func() { logger.Tracef(ctx, "/rawGATT.readDescriptor: %v", _err) }()

	data := parcel.New()
	defer data.Recycle()
	data.WriteInterfaceToken(bt.DescriptorIBluetoothGatt)
	data.WriteInt32(clientIf)
	data.WriteString16(address)
	data.WriteInt32(handle)
	data.WriteInt32(authReq)
	writeRawAttributionSource(data)

	return g.resolveAndTransact(ctx, bt.MethodIBluetoothGattReadDescriptor, data)
}

func (g *rawGATT) writeDescriptor(
	ctx context.Context,
	clientIf int32,
	address string,
	handle int32,
	authReq int32,
	value []byte,
) (_err error) {
	logger.Tracef(ctx, "rawGATT.writeDescriptor")
	defer func() { logger.Tracef(ctx, "/rawGATT.writeDescriptor: %v", _err) }()

	data := parcel.New()
	defer data.Recycle()
	data.WriteInterfaceToken(bt.DescriptorIBluetoothGatt)
	data.WriteInt32(clientIf)
	data.WriteString16(address)
	data.WriteInt32(handle)
	data.WriteInt32(authReq)
	data.WriteByteArray(value)
	writeRawAttributionSource(data)

	return g.resolveAndTransact(ctx, bt.MethodIBluetoothGattWriteDescriptor, data)
}

func (g *rawGATT) registerForNotification(
	ctx context.Context,
	clientIf int32,
	address string,
	handle int32,
	enable bool,
) (_err error) {
	logger.Tracef(ctx, "rawGATT.registerForNotification")
	defer func() { logger.Tracef(ctx, "/rawGATT.registerForNotification: %v", _err) }()

	data := parcel.New()
	defer data.Recycle()
	data.WriteInterfaceToken(bt.DescriptorIBluetoothGatt)
	data.WriteInt32(clientIf)
	data.WriteString16(address)
	data.WriteInt32(handle)
	data.WriteBool(enable)
	writeRawAttributionSource(data)

	return g.resolveAndTransact(ctx, bt.MethodIBluetoothGattRegisterForNotification, data)
}

func (g *rawGATT) readRemoteRssi(
	ctx context.Context,
	clientIf int32,
	address string,
) (_err error) {
	logger.Tracef(ctx, "rawGATT.readRemoteRssi")
	defer func() { logger.Tracef(ctx, "/rawGATT.readRemoteRssi: %v", _err) }()

	data := parcel.New()
	defer data.Recycle()
	data.WriteInterfaceToken(bt.DescriptorIBluetoothGatt)
	data.WriteInt32(clientIf)
	data.WriteString16(address)
	writeRawAttributionSource(data)

	return g.resolveAndTransact(ctx, bt.MethodIBluetoothGattReadRemoteRssi, data)
}

func (g *rawGATT) configureMTU(
	ctx context.Context,
	clientIf int32,
	address string,
	mtu int32,
) (_err error) {
	logger.Tracef(ctx, "rawGATT.configureMTU")
	defer func() { logger.Tracef(ctx, "/rawGATT.configureMTU: %v", _err) }()

	data := parcel.New()
	defer data.Recycle()
	data.WriteInterfaceToken(bt.DescriptorIBluetoothGatt)
	data.WriteInt32(clientIf)
	data.WriteString16(address)
	data.WriteInt32(mtu)
	writeRawAttributionSource(data)

	return g.resolveAndTransact(ctx, bt.MethodIBluetoothGattConfigureMTU, data)
}

func (g *rawGATT) registerServer(
	ctx context.Context,
	appUUID [16]byte,
	callback bt.IBluetoothGattServerCallback,
	eattSupport bool,
) (_err error) {
	logger.Tracef(ctx, "rawGATT.registerServer")
	defer func() { logger.Tracef(ctx, "/rawGATT.registerServer: %v", _err) }()

	data := parcel.New()
	defer data.Recycle()
	data.WriteInterfaceToken(bt.DescriptorIBluetoothGatt)

	// ParcelUuid: non-null marker + mostSigBits + leastSigBits
	data.WriteInt32(1)
	data.WriteInt64(int64(binary.BigEndian.Uint64(appUUID[:8])))
	data.WriteInt64(int64(binary.BigEndian.Uint64(appUUID[8:])))

	// IBluetoothGattServerCallback binder
	binderpkg.WriteBinderToParcel(ctx, data, callback.AsBinder(), g.transport)

	// eatt_support
	data.WriteBool(eattSupport)

	// AttributionSource
	writeRawAttributionSource(data)

	return g.resolveAndTransact(ctx, bt.MethodIBluetoothGattRegisterServer, data)
}

func (g *rawGATT) sendResponse(
	ctx context.Context,
	serverIf int32,
	address string,
	requestID int32,
	status int32,
	offset int32,
	value []byte,
) (_err error) {
	logger.Tracef(ctx, "rawGATT.sendResponse")
	defer func() { logger.Tracef(ctx, "/rawGATT.sendResponse: %v", _err) }()

	data := parcel.New()
	defer data.Recycle()
	data.WriteInterfaceToken(bt.DescriptorIBluetoothGatt)
	data.WriteInt32(serverIf)
	data.WriteString16(address)
	data.WriteInt32(requestID)
	data.WriteInt32(status)
	data.WriteInt32(offset)
	data.WriteByteArray(value)
	writeRawAttributionSource(data)

	return g.resolveAndTransact(ctx, bt.MethodIBluetoothGattSendResponse, data)
}

func (g *rawGATT) sendNotification(
	ctx context.Context,
	serverIf int32,
	address string,
	handle int32,
	confirm bool,
	value []byte,
) (_err error) {
	logger.Tracef(ctx, "rawGATT.sendNotification")
	defer func() { logger.Tracef(ctx, "/rawGATT.sendNotification: %v", _err) }()

	data := parcel.New()
	defer data.Recycle()
	data.WriteInterfaceToken(bt.DescriptorIBluetoothGatt)
	data.WriteInt32(serverIf)
	data.WriteString16(address)
	data.WriteInt32(handle)
	data.WriteBool(confirm)
	data.WriteByteArray(value)
	writeRawAttributionSource(data)

	return g.resolveAndTransact(ctx, bt.MethodIBluetoothGattSendNotification, data)
}
