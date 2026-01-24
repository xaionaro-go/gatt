package linux

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestL2CAP_ReassemblyLarge(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c := &conn{
		aclCh:  make(chan *aclData),
		dataCh: make(chan []byte, 1),
	}
	go c.loop(ctx)

	tLen := 600
	payload := make([]byte, tLen)
	for i := range payload {
		payload[i] = byte(i % 256)
	}

	// First fragment: L2CAP header + first 20 bytes of payload
	firstFrag := make([]byte, 4+20)
	firstFrag[0] = byte(tLen & 0xff)
	firstFrag[1] = byte(tLen >> 8)
	firstFrag[2] = 0x40 // CID low
	firstFrag[3] = 0x00 // CID high
	copy(firstFrag[4:], payload[:20])

	c.aclCh <- &aclData{
		flags: 0x02, // First segment
		b:     firstFrag,
	}

	// Continuing fragments
	remaining := payload[20:]
	for len(remaining) > 0 {
		chunkSize := 100
		if len(remaining) < chunkSize {
			chunkSize = len(remaining)
		}
		chunk := remaining[0:chunkSize]

		c.aclCh <- &aclData{
			flags: 0x01, // Continuing fragment
			b:     chunk,
		}
		remaining = remaining[chunkSize:]
	}

	select {
	case result := <-c.dataCh:
		if !bytes.Equal(result, payload) {
			t.Errorf("Result does not match payload. Got len %d, want %d", len(result), len(payload))
		}
	case <-ctx.Done():
		t.Fatal("Timeout waiting for reassembled packet")
	}
}

func TestL2CAP_ReassemblyWithPadding(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c := &conn{
		aclCh:  make(chan *aclData),
		dataCh: make(chan []byte, 1),
	}
	go c.loop(ctx)

	tLen := 21
	payload := bytes.Repeat([]byte{0xAA}, tLen)

	// First fragment: L2CAP header + 21 bytes of payload + 59 bytes of padding
	firstFrag := make([]byte, 4+21+59)
	firstFrag[0] = byte(tLen & 0xff)
	firstFrag[1] = byte(tLen >> 8)
	firstFrag[2] = 0x40 // CID low
	firstFrag[3] = 0x00 // CID high
	copy(firstFrag[4:], payload)

	c.aclCh <- &aclData{
		flags: 0x02, // First segment
		b:     firstFrag,
	}

	select {
	case result := <-c.dataCh:
		if !bytes.Equal(result, payload) {
			t.Errorf("Result does not match payload. Got len %d, want %d", len(result), len(payload))
		}
	case <-ctx.Done():
		t.Fatal("Timeout waiting for packet with padding")
	}
}
