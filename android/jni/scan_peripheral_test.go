package jni

import (
	"testing"

	jnipkg "github.com/AndroidGoLab/jni"
	"github.com/AndroidGoLab/jni/bluetooth"
)

func TestRememberScanDeviceRetainsOnlyNewBluetoothDeviceGlobalRef(t *testing.T) {
	d := &device{
		peripherals: make(map[string]*peripheral),
	}
	firstDev := &bluetooth.Device{Obj: &jnipkg.Object{}}

	firstPeripheral, retained, report := d.rememberScanDevice("04:A8:5A:CE:07:63", "OsmoPocket3-0762", firstDev, false)
	if firstPeripheral == nil {
		t.Fatalf("rememberScanDevice returned nil peripheral for first discovery")
	}
	if !retained {
		t.Fatalf("rememberScanDevice retained = false for first discovery, want true")
	}
	if !report {
		t.Fatalf("rememberScanDevice report = false for first discovery, want true")
	}
	if firstPeripheral.btDev != firstDev {
		t.Fatalf("rememberScanDevice stored btDev = %p, want %p", firstPeripheral.btDev, firstDev)
	}

	duplicateDev := &bluetooth.Device{Obj: &jnipkg.Object{}}
	duplicatePeripheral, retained, report := d.rememberScanDevice("04:A8:5A:CE:07:63", "OsmoPocket3-0762", duplicateDev, false)
	if duplicatePeripheral != firstPeripheral {
		t.Fatalf("rememberScanDevice returned duplicate peripheral = %p, want first %p", duplicatePeripheral, firstPeripheral)
	}
	if retained {
		t.Fatalf("rememberScanDevice retained = true for duplicate with dup=false, want false")
	}
	if report {
		t.Fatalf("rememberScanDevice report = true for duplicate with dup=false, want false")
	}
	if firstPeripheral.btDev == duplicateDev {
		t.Fatalf("rememberScanDevice replaced retained btDev on duplicate")
	}

	duplicateReportPeripheral, retained, report := d.rememberScanDevice("04:A8:5A:CE:07:63", "OsmoPocket3-0762", duplicateDev, true)
	if duplicateReportPeripheral != firstPeripheral {
		t.Fatalf("rememberScanDevice returned duplicate report peripheral = %p, want first %p", duplicateReportPeripheral, firstPeripheral)
	}
	if retained {
		t.Fatalf("rememberScanDevice retained = true for duplicate with dup=true, want false")
	}
	if !report {
		t.Fatalf("rememberScanDevice report = false for duplicate with dup=true, want true")
	}
}
