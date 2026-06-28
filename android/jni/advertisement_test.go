package jni

import (
	"bytes"
	"testing"
)

const (
	testAdvertisementTypeFlags            byte = 0x01
	testAdvertisementTypeManufacturerData byte = 0xFF
)

func TestAdvertisementFromScanRecordUsesParsedManufacturerData(t *testing.T) {
	raw := []byte{
		0x02, testAdvertisementTypeFlags, 0x06,
		0x05, testAdvertisementTypeManufacturerData, 0xAA, 0x08, 0x20, 0x00,
	}

	adv, err := advertisementFromScanRecord("DJI Osmo Pocket 3", raw)
	if err != nil {
		t.Fatalf("advertisementFromScanRecord returned error: %v", err)
	}
	if got, want := adv.LocalName, "DJI Osmo Pocket 3"; got != want {
		t.Fatalf("advertisementFromScanRecord local name = %q, want %q", got, want)
	}
	if got, want := adv.CompanyID, uint16(0x08AA); got != want {
		t.Fatalf("advertisementFromScanRecord company ID = 0x%04X, want 0x%04X", got, want)
	}
	if got, want := adv.ManufacturerData, []byte{0xAA, 0x08, 0x20, 0x00}; !bytes.Equal(got, want) {
		t.Fatalf("advertisementFromScanRecord manufacturer data = %X, want %X", got, want)
	}
}

func TestAdvertisementFromScanRecordReturnsFallbackAdvertisementOnMalformedData(t *testing.T) {
	raw := []byte{0x05, testAdvertisementTypeManufacturerData, 0xAA}

	adv, err := advertisementFromScanRecord("DJI Osmo Pocket 3", raw)
	if err == nil {
		t.Fatalf("advertisementFromScanRecord returned nil error, want malformed advertisement error")
	}
	if got, want := adv.LocalName, "DJI Osmo Pocket 3"; got != want {
		t.Fatalf("advertisementFromScanRecord local name = %q, want %q", got, want)
	}
	if len(adv.ManufacturerData) != 0 {
		t.Fatalf("advertisementFromScanRecord manufacturer data = %X, want empty fallback", adv.ManufacturerData)
	}
}
