package android

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/xaionaro-go/gatt"
)

// BackendName identifies an Android BLE backend.
type BackendName string

const (
	// BackendBinder uses AndroidGoLab/binder for pure-Go Binder IPC.
	BackendBinder BackendName = "binder"

	// BackendJNI uses AndroidGoLab/jni for JNI-based access to Android Bluetooth API.
	BackendJNI BackendName = "jni"

	// BackendJNIProxy uses AndroidGoLab/jni-proxy for gRPC-based remote access.
	BackendJNIProxy BackendName = "jniproxy"
)

// BackendFactory creates a Device for the given backend.
type BackendFactory func(ctx context.Context, opts ...gatt.Option) (gatt.Device, error)

var (
	mu       sync.RWMutex
	backends = map[BackendName]BackendFactory{}
)

// Register registers a backend factory. Called by backend packages in init().
func Register(name BackendName, f BackendFactory) {
	mu.Lock()
	defer mu.Unlock()
	if _, dup := backends[name]; dup {
		panic(fmt.Sprintf("android: backend %q already registered", name))
	}
	backends[name] = f
}

// NewDevice creates a Device using the specified backend.
func NewDevice(
	ctx context.Context,
	backend BackendName,
	opts ...gatt.Option,
) (gatt.Device, error) {
	mu.RLock()
	f, ok := backends[backend]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("android: unknown backend %q (registered: %v)", backend, Backends())
	}
	return f(ctx, opts...)
}

// Backends returns the names of all registered backends, sorted.
func Backends() []BackendName {
	mu.RLock()
	defer mu.RUnlock()
	result := make([]BackendName, 0, len(backends))
	for name := range backends {
		result = append(result, name)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
