package util

import "sync"

type BytePool struct {
	pool  chan []byte
	width int
	mu    sync.RWMutex
}

func NewBytePool(width int, depth int) *BytePool {
	return &BytePool{
		pool:  make(chan []byte, depth),
		width: width,
	}
}

func (p *BytePool) Close() {
	p.mu.Lock()
	close(p.pool)
	p.pool = nil
	p.mu.Unlock()
}

func (p *BytePool) Get() (b []byte) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.pool == nil {
		return nil
	}

	select {
	case b = <-p.pool:
	default:
		b = make([]byte, p.width)
	}
	return b
}

func (p *BytePool) Put(b []byte) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	if p.pool == nil {
		return
	}

	select {
	case p.pool <- b:
	default:
	}
}
