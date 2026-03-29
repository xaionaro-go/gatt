# Package gatt provides a Bluetooth Low Energy GATT implementation.

Gatt (Generic Attribute Profile) is the protocol used to write BLE peripherals (servers) and centrals (clients).

As a peripheral, you can create services, characteristics, and descriptors,
advertise, accept connections, and handle requests.

As a central, you can scan, connect, discover services, and make requests.

This is a fork of [github.com/paypal/gatt](https://github.com/paypal/gatt) with
Android support, refactoring, bug fixes, and ongoing maintenance.

## Supported Platforms

- **Linux** -- native HCI socket access via `HCI_CHANNEL_USER`
- **macOS (Darwin)** -- via XPC
- **Android** -- three pluggable backends (see [Android](#android) below)

## Setup

### Linux

To gain complete and exclusive control of the HCI device, gatt uses
`HCI_CHANNEL_USER` (introduced in Linux v3.14) instead of `HCI_CHANNEL_RAW`.
Those who must use an older kernel may patch in these relevant commits
from Marcel Holtmann:

    Bluetooth: Introduce new HCI socket channel for user operation
    Bluetooth: Introduce user channel flag for HCI devices
    Bluetooth: Refactor raw socket filter into more readable code

Note that because gatt uses `HCI_CHANNEL_USER`, once gatt has opened the
device no other program may access it.

Before starting a gatt program, make sure that your BLE device is down:

    sudo hciconfig
    sudo hciconfig hci0 down  # or whatever hci device you want to use

If you have BlueZ 5.14+ (or aren't sure), stop the built-in
bluetooth server, which interferes with gatt, e.g.:

    sudo service bluetooth stop

Because gatt programs administer network devices, they must
either be run as root, or be granted appropriate capabilities:

    sudo <executable>
    # OR
    sudo setcap 'cap_net_raw,cap_net_admin=eip' <executable>
    <executable>

### Android

Android is supported via three backends, selectable through the registry in
`android/registry.go`:

| Backend | Package | CGO | Description |
|---------|---------|-----|-------------|
| **Binder** | `android/binder` | No | Pure-Go Binder IPC to the Android Bluetooth stack |
| **JNI** | `android/jni` | Yes | JNI calls into the Android Bluetooth Java API |
| **JNIProxy** | `android/jniproxy` | No | gRPC-based remote access to JNI on another device |

See `examples/android/` for scanning examples using each backend.

## Usage

Please see [pkg.go.dev](https://pkg.go.dev/github.com/xaionaro-go/gatt) for API documentation.

## Examples

### Linux / macOS

    # Build and run the sample server.
    sudo go run examples/server.go

    # Discover surrounding peripherals.
    sudo go run examples/discoverer.go

    # Connect to and explore a peripheral device.
    sudo go run examples/explorer.go <peripheral ID>

See `examples/server_lnx.go` for Linux-specific options giving
finer-grained control over the HCI device.

### Cross-compile for a target device

    # Build for an ARMv5 Linux target.
    GOARCH=arm GOARM=5 GOOS=linux go build examples/server.go
    # Copy to target and run.
    sudo ./server

### Android

    # Scan using the Binder backend (pure Go, no CGO).
    GOOS=android GOARCH=arm64 CGO_ENABLED=0 go build examples/android/binder_scan.go

    # Scan using JNI (requires NDK cross-compiler).
    GOOS=android GOARCH=arm64 CGO_ENABLED=1 go build examples/android/jni_scan.go

    # Scan using JNIProxy (pure Go client, connects to an Android device via gRPC).
    # Runs on any host platform, not only Android.
    CGO_ENABLED=0 go build examples/android/jniproxy_scan.go

## Simulated Device

`sim_device.go` provides an in-memory simulated BLE device for testing
without physical hardware. Use `NewSimDeviceClient()` to create one.

## Note

Some BLE central devices, particularly iOS, may aggressively
cache results from previous connections. If you change your services or
characteristics, you may need to reboot the other device to pick up the
changes. This is a common source of confusion and apparent bugs. For an
OS X central, see http://stackoverflow.com/questions/20553957.

## References

gatt started life as a port of [bleno](https://github.com/sandeepmistry/bleno),
to which it is indebted. If you are having problems with gatt, particularly
around installation, issues filed with bleno might also be helpful references.

To try out your GATT server, it is useful to experiment with a generic BLE
client. [LightBlue](https://punchthrough.com/lightblue/) is a good choice,
available free for both iOS and macOS.

gatt is released under a [BSD-style license](./LICENSE.md).
