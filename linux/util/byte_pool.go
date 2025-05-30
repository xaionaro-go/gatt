package util

// TODO: rename this package: "util" is a too generic name that could easily fit any God object

import "sync"

type BytePool struct {
	Pool  chan []byte
	Width int
	Mutex sync.RWMutex
}

// Why this exists? Is this just a ring buffer on top of a channel?
// If so, then:
// * Why it is named "Pool"?
// * Can we just use a normal sync.Pool instead of a ring buffer?
// * If not can we just reuse an existing implementation?
func NewBytePool(width int, depth int) *BytePool {
	return &BytePool{
		Pool:  make(chan []byte, depth),
		Width: width,
	}
}

func (p *BytePool) Close() {
	p.Mutex.Lock()
	close(p.Pool)
	p.Pool = nil
	p.Mutex.Unlock()
}

func (p *BytePool) Get() (b []byte) {
	p.Mutex.RLock()
	defer p.Mutex.RUnlock()

	if p.Pool == nil {
		return nil
	}

	select {
	case b = <-p.Pool:
	default:
		b = make([]byte, p.Width)
	}
	return b
}

func (p *BytePool) Put(b []byte) {
	p.Mutex.RLock()
	defer p.Mutex.RUnlock()

	if p.Pool == nil {
		return
	}

	// TODO: o_0, we just silently discard data? Can we at least log this?
	select {
	case p.Pool <- b:
	default:
	}
}
