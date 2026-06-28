package jni

import (
	"context"
	"errors"
	"testing"
)

func TestPeripheralWaitForCharacteristicWriteConsumesAndroidCallback(t *testing.T) {
	p := newPeripheral(nil, nil, "", "")
	p.charWritten <- nil

	if err := p.waitForCharacteristicWrite(context.Background()); err != nil {
		t.Fatalf("waitForCharacteristicWrite returned error: %v", err)
	}

	select {
	case err := <-p.charWritten:
		t.Fatalf("waitForCharacteristicWrite left callback in channel: %v", err)
	default:
	}
}

func TestPeripheralWaitForCharacteristicWriteReturnsAndroidCallbackError(t *testing.T) {
	expectedErr := errors.New("write failed")
	p := newPeripheral(nil, nil, "", "")
	p.charWritten <- expectedErr

	if err := p.waitForCharacteristicWrite(context.Background()); !errors.Is(err, expectedErr) {
		t.Fatalf("waitForCharacteristicWrite returned %v, want %v", err, expectedErr)
	}
}
