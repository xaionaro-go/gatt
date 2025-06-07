package linux

import (
	"context"
	"errors"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/facebookincubator/go-belt/tool/logger"
	"github.com/xaionaro-go/gatt/linux/gioctl"
	"github.com/xaionaro-go/gatt/linux/socket"
)

type device struct {
	fd         int
	fds        []unix.PollFd
	id         int
	name       string
	readMutex  *sync.Mutex
	writeMutex *sync.Mutex
}

func newDevice(ctx context.Context, deviceID int, checkFeatureList bool) (*device, error) {
	fd, err := socket.Socket(socket.AF_BLUETOOTH, unix.SOCK_RAW, socket.BTPROTO_HCI)
	if err != nil {
		logger.Debugf(ctx, "could not create AF_BLUETOOTH raw socket: %v", err)
		return nil, err
	}
	if deviceID != -1 {
		return newSocket(ctx, fd, deviceID, checkFeatureList)
	}

	req := devListRequest{devNum: hciMaxDevices}
	if err := gioctl.Ioctl(uintptr(fd), hciGetDeviceList, uintptr(unsafe.Pointer(&req))); err != nil {
		logger.Debugf(ctx, "hciGetDeviceList failed: %v", err)
		return nil, err
	}
	logger.Debugf(ctx, "got %d devices", req.devNum)
	for deviceID := range int(req.devNum) {
		device, err := newSocket(ctx, fd, deviceID, checkFeatureList)
		if err == nil {
			logger.Debugf(ctx, "dev: %s opened", strings.Trim(device.name, "\000"))
			return device, nil
		}
		logger.Errorf(ctx, "error while opening device %d: %v", deviceID, err)
	}
	return nil, errors.New("no supported devices available")
}

func newSocket(
	ctx context.Context,
	fd int,
	deviceID int,
	checkFeatureList bool,
) (*device, error) {
	i := hciDevInfo{id: uint16(deviceID)}
	if err := gioctl.Ioctl(uintptr(fd), hciGetDeviceInfo, uintptr(unsafe.Pointer(&i))); err != nil {
		logger.Debugf(ctx, "hciGetDeviceInfo failed: %v", err)
		return nil, err
	}
	name := string(i.name[:])
	// Check the feature list returned feature list.
	if checkFeatureList && i.features[4]&0x40 == 0 {
		err := errors.New("does not support LE")
		logger.Debugf(ctx, "dev: %s %s", strings.Trim(name, "\000"), err)
		return nil, err
	}
	logger.Debugf(ctx, "dev: %s up", strings.Trim(name, "\000"))
	if err := gioctl.Ioctl(uintptr(fd), hciUpDevice, uintptr(deviceID)); err != nil {
		if err != unix.EALREADY {
			return nil, err
		}
		logger.Debugf(ctx, "dev: %s reset", strings.Trim(name, "\000"))
		if err := gioctl.Ioctl(uintptr(fd), hciResetDevice, uintptr(deviceID)); err != nil {
			logger.Debugf(ctx, "hciResetDevice failed: %v", err)
			return nil, err
		}
	}
	logger.Debugf(ctx, "dev: %s down", strings.Trim(name, "\000"))
	if err := gioctl.Ioctl(uintptr(fd), hciDownDevice, uintptr(deviceID)); err != nil {
		return nil, err
	}

	// Attempt to use the linux 3.14 feature, if this fails with EINVAL fall back to raw access
	// on older kernels.
	sa := socket.SockaddrHCI{Dev: deviceID, Channel: socket.HCI_CHANNEL_USER}
	if err := socket.Bind(fd, &sa); err != nil {
		if err != unix.EINVAL {
			return nil, err
		}
		logger.Debugf(ctx, "dev: %s can't bind to hci user channel, err: %s.", name, err)
		sa := socket.SockaddrHCI{Dev: deviceID, Channel: socket.HCI_CHANNEL_RAW}
		if err := socket.Bind(fd, &sa); err != nil {
			logger.Debugf(ctx, "dev: %s can't bind to hci raw channel, err: %s.", name, err)
			return nil, err
		}
	}

	fds := make([]unix.PollFd, 1)
	fds[0].Fd = int32(fd)
	fds[0].Events = unix.POLLIN

	return &device{
		fd:         fd,
		fds:        fds,
		id:         deviceID,
		name:       name,
		readMutex:  &sync.Mutex{},
		writeMutex: &sync.Mutex{},
	}, nil
}

func (d device) DevId() int {
	return d.id
}

func (d device) Read(b []byte) (int, error) {
	d.readMutex.Lock()
	defer d.readMutex.Unlock()
	// Use poll to avoid blocking on Read
	n, err := unix.Poll(d.fds, 100)
	if n == 0 || err != nil {
		return 0, err
	}
	return unix.Read(d.fd, b)
}

func (d device) Write(b []byte) (int, error) {
	d.writeMutex.Lock()
	defer d.writeMutex.Unlock()
	return unix.Write(d.fd, b)
}

func (d device) Close() error {
	ctx := context.TODO()
	logger.Debugf(ctx, "linux.device.Close()")
	return unix.Close(d.fd)
}
