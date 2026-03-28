package jniproxy

import (
	"github.com/xaionaro-go/gatt"
	"google.golang.org/grpc"
)

type config struct {
	conn grpc.ClientConnInterface
}

// WithGRPCConn sets the gRPC connection used for raw JNI calls.
func WithGRPCConn(cc grpc.ClientConnInterface) gatt.Option {
	return func(d gatt.Device) error {
		dd, ok := d.(*device)
		if !ok {
			return nil
		}
		dd.cfg.conn = cc
		return nil
	}
}
