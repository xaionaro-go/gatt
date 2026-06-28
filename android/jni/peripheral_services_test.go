package jni

import (
	"testing"

	"github.com/xaionaro-go/gatt"
)

func TestAddAndroidDiscoveredCharacteristicToService(t *testing.T) {
	service := gatt.NewService(gatt.MustParseUUID("0000fff0-0000-1000-8000-00805f9b34fb"))
	characteristic := gatt.NewCharacteristic(
		gatt.MustParseUUID("0000fff4-0000-1000-8000-00805f9b34fb"),
		service,
		gatt.CharNotify,
		0x002C,
		0x002D,
	)

	addAndroidDiscoveredCharacteristicToService(service, characteristic)

	characteristics := service.Characteristics()
	if len(characteristics) != 1 {
		t.Fatalf("service has %d characteristics, want 1", len(characteristics))
	}
	if characteristics[0] != characteristic {
		t.Fatalf("service characteristic = %p, want %p", characteristics[0], characteristic)
	}
}
