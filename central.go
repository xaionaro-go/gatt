package gatt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
)

// Central is the interface that represent a remote central device.
type Central interface {
	ID() string   // ID returns platform specific ID of the remote central device.
	Close() error // Close disconnects the connection.
	MTU() int     // MTU returns the current connection mtu.
}

type ResponseWriter interface {
	// Write writes data to return as the characteristic value.
	Write([]byte) (int, error)

	// SetStatus reports the result of the read operation. See the Status* constants.
	SetStatus(byte)
}

// responseWriter is the default implementation of ResponseWriter.
type responseWriter struct {
	capacity int
	buf      *bytes.Buffer
	status   byte
}

func newResponseWriter(c int) *responseWriter {
	return &responseWriter{
		capacity: c,
		buf:      new(bytes.Buffer),
		status:   StatusSuccess,
	}
}

func (w *responseWriter) Write(b []byte) (int, error) {
	if avail := w.capacity - w.buf.Len(); avail < len(b) {
		return 0, fmt.Errorf("requested write %d bytes, %d available", len(b), avail)
	}
	return w.buf.Write(b)
}

func (w *responseWriter) SetStatus(status byte) { w.status = status }
func (w *responseWriter) bytes() []byte         { return w.buf.Bytes() }

// A ReadHandler handles GATT read requests.
type ReadHandler interface {
	ServeRead(ctx context.Context, resp ResponseWriter, req *ReadRequest)
}

// ReadHandlerFunc is an adapter to allow the use of
// ordinary functions as ReadHandlers. If f is a function
// with the appropriate signature, ReadHandlerFunc(f) is a
// ReadHandler that calls f.
type ReadHandlerFunc func(ctx context.Context, resp ResponseWriter, req *ReadRequest)

// ServeRead returns f(r, maxLen, offset).
func (f ReadHandlerFunc) ServeRead(ctx context.Context, resp ResponseWriter, req *ReadRequest) {
	f(ctx, resp, req)
}

// A WriteHandler handles GATT write requests.
// Write and WriteNR requests are presented identically;
// the server will ensure that a response is sent if appropriate.
type WriteHandler interface {
	ServeWrite(ctx context.Context, r Request, data []byte) (status byte)
}

// WriteHandlerFunc is an adapter to allow the use of
// ordinary functions as WriteHandlers. If f is a function
// with the appropriate signature, WriteHandlerFunc(f) is a
// WriteHandler that calls f.
type WriteHandlerFunc func(ctx context.Context, r Request, data []byte) byte

// ServeWrite returns f(r, data).
func (f WriteHandlerFunc) ServeWrite(ctx context.Context, r Request, data []byte) byte {
	return f(ctx, r, data)
}

// A NotifyHandler handles GATT notification requests.
// Notifications can be sent using the provided notifier.
type NotifyHandler interface {
	ServeNotify(ctx context.Context, r Request, n Notifier)
}

// NotifyHandlerFunc is an adapter to allow the use of
// ordinary functions as NotifyHandlers. If f is a function
// with the appropriate signature, NotifyHandlerFunc(f) is a
// NotifyHandler that calls f.
type NotifyHandlerFunc func(ctx context.Context, r Request, n Notifier)

// ServeNotify calls f(r, n).
func (f NotifyHandlerFunc) ServeNotify(ctx context.Context, r Request, n Notifier) {
	f(ctx, r, n)
}

// A Notifier provides a means for a GATT server to send
// notifications about value changes to a connected device.
// Notifiers are provided by NotifyHandlers.
type Notifier interface {
	// Write sends data to the central.
	Write(data []byte) (int, error)

	// Done reports whether the central has requested not to
	// receive any more notifications with this notifier.
	Done() bool

	// Cap returns the maximum number of bytes that may be sent
	// in a single notification.
	Cap() int
}

type notifier struct {
	central   *central
	a         *attr
	maxLen    int
	doneMutex sync.RWMutex
	done      bool
}

func newNotifier(c *central, a *attr, maxLen int) *notifier {
	return &notifier{central: c, a: a, maxLen: maxLen}
}

func (n *notifier) Write(b []byte) (int, error) {
	return n.WriteContext(context.Background(), b)
}

func (n *notifier) WriteContext(ctx context.Context, b []byte) (int, error) {
	n.doneMutex.RLock()
	defer n.doneMutex.RUnlock()
	if n.done {
		return 0, errors.New("central stopped notifications")
	}
	return n.central.sendNotification(ctx, n.a, b)
}

func (n *notifier) Cap() int {
	return n.maxLen
}

func (n *notifier) Done() bool {
	n.doneMutex.RLock()
	defer n.doneMutex.RUnlock()
	return n.done
}

func (n *notifier) stop() {
	n.doneMutex.Lock()
	n.done = true
	n.doneMutex.Unlock()
}
