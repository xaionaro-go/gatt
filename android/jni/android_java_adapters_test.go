package jni

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAndroidBluetoothGattCallbackAdapterMatchesJNIAbstractLookup(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller returned ok=false")
	}

	javaFile := filepath.Join(
		filepath.Dir(testFile),
		"java",
		"center",
		"dx",
		"gatt",
		"internal",
		"GoBluetoothGattCallback.java",
	)

	data, err := os.ReadFile(filepath.Clean(javaFile))
	if err != nil {
		t.Fatalf("reading BluetoothGattCallback adapter source: %v", err)
	}

	source := string(data)
	for _, required := range []string{
		"package center.dx.gatt.internal;",
		"public class GoBluetoothGattCallback extends GoGattCallback",
		"public GoBluetoothGattCallback(long handlerID)",
		"super(handlerID);",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("BluetoothGattCallback adapter source does not contain %q", required)
		}
	}
}
