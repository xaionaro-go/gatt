package jni

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestPeripheralWaitForCharacteristicWriteConsumesAndroidCallback(t *testing.T) {
	p := newPeripheral(nil, nil, "", "")
	writeCompleted, err := p.beginCharacteristicWrite()
	if err != nil {
		t.Fatalf("beginCharacteristicWrite returned error: %v", err)
	}
	p.handleCharacteristicWrite(nil)

	if err := p.waitForCharacteristicWrite(context.Background(), writeCompleted); err != nil {
		t.Fatalf("waitForCharacteristicWrite returned error: %v", err)
	}

	if p.hasPendingCharacteristicWrite() {
		t.Fatalf("waitForCharacteristicWrite left pending write state")
	}
}

func TestPeripheralWaitForCharacteristicWriteReturnsAndroidCallbackError(t *testing.T) {
	expectedErr := errors.New("write failed")
	p := newPeripheral(nil, nil, "", "")
	writeCompleted, err := p.beginCharacteristicWrite()
	if err != nil {
		t.Fatalf("beginCharacteristicWrite returned error: %v", err)
	}
	p.handleCharacteristicWrite(expectedErr)

	if err := p.waitForCharacteristicWrite(context.Background(), writeCompleted); !errors.Is(err, expectedErr) {
		t.Fatalf("waitForCharacteristicWrite returned %v, want %v", err, expectedErr)
	}
}

func TestPeripheralCharacteristicWriteCancelRejectsLaterWritesAndLateCallback(t *testing.T) {
	p := newPeripheral(nil, nil, "", "")
	writeCompleted, err := p.beginCharacteristicWrite()
	if err != nil {
		t.Fatalf("beginCharacteristicWrite returned error: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := p.waitForCharacteristicWrite(ctx, writeCompleted); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForCharacteristicWrite returned %v, want context cancellation", err)
	}

	p.handleCharacteristicWrite(nil)

	_, err = p.beginCharacteristicWrite()
	switch {
	case err == nil:
		t.Fatalf("beginCharacteristicWrite returned nil error after write cancellation")
	case !strings.Contains(err.Error(), "previous characteristic write did not complete"):
		t.Fatalf("beginCharacteristicWrite returned %q, want previous-write failure", err)
	}
}
