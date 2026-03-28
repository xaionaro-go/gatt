EXAMPLE_NAME        := jni_scan
EXAMPLE_PERMISSIONS := android.permission.BLUETOOTH_CONNECT android.permission.BLUETOOTH_SCAN android.permission.BLUETOOTH_ADVERTISE
EXAMPLE_NEEDS_PROXY := true
PACKAGE_NAME        := center.dx.gatt.examples.jni_scan

# Point to the jni repo's shared build infrastructure.
JNI_REPO := $(HOME)/go/src/github.com/xaionaro-go/jni

ANDROID_SDK ?= $(shell \
	if [ -n "$$ANDROID_HOME" ]; then echo "$$ANDROID_HOME"; \
	elif [ -d "$$HOME/Android/Sdk" ]; then echo "$$HOME/Android/Sdk"; \
	elif [ -d "$$HOME/android-sdk" ]; then echo "$$HOME/android-sdk"; \
	fi)
NDK_VERSION  ?= $(shell ls $(ANDROID_SDK)/ndk 2>/dev/null | sort -V | tail -1)
NDK          ?= $(ANDROID_SDK)/ndk/$(NDK_VERSION)
BUILD_TOOLS  ?= $(shell ls $(ANDROID_SDK)/build-tools 2>/dev/null | sort -V | tail -1)
PLATFORM     ?= $(shell ls -d $(ANDROID_SDK)/platforms/android-* 2>/dev/null | sort -V | tail -1)
MIN_SDK      ?= 24
TARGET_SDK   ?= $(shell basename $(PLATFORM) | sed 's/android-//')
ADB       := $(ANDROID_SDK)/platform-tools/adb
AAPT2     := $(ANDROID_SDK)/build-tools/$(BUILD_TOOLS)/aapt2
D8        := $(ANDROID_SDK)/build-tools/$(BUILD_TOOLS)/d8
ZIPALIGN  := $(ANDROID_SDK)/build-tools/$(BUILD_TOOLS)/zipalign
APKSIGNER := $(ANDROID_SDK)/build-tools/$(BUILD_TOOLS)/apksigner
NDK_TOOLCHAIN := $(NDK)/toolchains/llvm/prebuilt/linux-x86_64/bin
CC_ARM64 := $(NDK_TOOLCHAIN)/aarch64-linux-android$(MIN_SDK)-clang

BUILD        := build
HANDLER_DIR  := $(JNI_REPO)/internal/testjvm/testdata

.PHONY: all build install run clean

all: build

build: $(BUILD)/$(EXAMPLE_NAME).apk

$(BUILD)/debug.keystore:
	@mkdir -p $(BUILD)
	keytool -genkeypair -keystore $@ -storepass android -alias debug \
		-keyalg RSA -keysize 2048 -validity 10000 \
		-dname "CN=Debug" -noprompt 2>/dev/null

$(BUILD)/AndroidManifest.xml:
	@mkdir -p $(BUILD)
	@printf '<?xml version="1.0" encoding="utf-8"?>\n' > $@
	@printf '<manifest xmlns:android="http://schemas.android.com/apk/res/android"\n' >> $@
	@printf '    package="%s">\n' '$(PACKAGE_NAME)' >> $@
	@$(foreach perm,$(EXAMPLE_PERMISSIONS), \
		printf '    <uses-permission android:name="%s" />\n' '$(perm)' >> $@;)
	@printf '    <application android:label="%s" android:hasCode="true" android:debuggable="true">\n' '$(EXAMPLE_NAME)' >> $@
	@printf '        <activity android:name="android.app.NativeActivity"\n' >> $@
	@printf '                  android:exported="true"\n' >> $@
	@printf '                  android:configChanges="orientation|keyboardHidden">\n' >> $@
	@printf '            <meta-data android:name="android.app.lib_name" android:value="example" />\n' >> $@
	@printf '            <intent-filter>\n' >> $@
	@printf '                <action android:name="android.intent.action.MAIN" />\n' >> $@
	@printf '                <category android:name="android.intent.category.LAUNCHER" />\n' >> $@
	@printf '            </intent-filter>\n' >> $@
	@printf '        </activity>\n' >> $@
	@printf '    </application>\n' >> $@
	@printf '</manifest>\n' >> $@

HANDLER_JAVA     := $(HANDLER_DIR)/center/dx/jni/internal/GoInvocationHandler.java
DISPATCH_JAVA    := $(HANDLER_DIR)/center/dx/jni/internal/GoAbstractDispatch.java
SCAN_CB_JAVA     := java/center/dx/gatt/internal/GoScanCallback.java
GATT_CB_JAVA     := java/center/dx/gatt/internal/GoGattCallback.java

$(BUILD)/classes.dex: $(HANDLER_JAVA) $(DISPATCH_JAVA) $(SCAN_CB_JAVA) $(GATT_CB_JAVA)
	@mkdir -p $(BUILD)/java
	javac --release 17 -classpath $(PLATFORM)/android.jar \
		-d $(BUILD)/java $(HANDLER_JAVA) $(DISPATCH_JAVA) $(SCAN_CB_JAVA) $(GATT_CB_JAVA)
	$(D8) --lib $(PLATFORM)/android.jar --output $(BUILD) \
		$$(find $(BUILD)/java -name '*.class')

$(BUILD)/lib/arm64-v8a/libexample.so: jni_scan.go
	@mkdir -p $(dir $@)
	cd ../.. && CGO_ENABLED=1 GOOS=android GOARCH=arm64 CC=$(CC_ARM64) \
		CGO_LDFLAGS="-llog -landroid" \
		go build -buildmode=c-shared \
		-o examples/android/$@ \
		./examples/android/jni_scan.go
	@rm -f $(@:.so=.h)

$(BUILD)/$(EXAMPLE_NAME).apk: $(BUILD)/AndroidManifest.xml $(BUILD)/classes.dex $(BUILD)/lib/arm64-v8a/libexample.so $(BUILD)/debug.keystore
	$(AAPT2) link --manifest $(BUILD)/AndroidManifest.xml \
		-I $(PLATFORM)/android.jar \
		--min-sdk-version $(MIN_SDK) \
		--target-sdk-version $(TARGET_SDK) \
		-o $(BUILD)/base.apk
	cd $(BUILD) && zip -j base.apk classes.dex
	cd $(BUILD) && zip -r base.apk lib/
	$(ZIPALIGN) -f 4 $(BUILD)/base.apk $(BUILD)/aligned.apk
	$(APKSIGNER) sign --ks $(BUILD)/debug.keystore --ks-pass pass:android \
		--out $@ $(BUILD)/aligned.apk
	@rm -f $(BUILD)/base.apk $(BUILD)/aligned.apk
	@echo "Built: $@"

install: $(BUILD)/$(EXAMPLE_NAME).apk
	$(ADB) install -r $<

run: install
	@$(foreach perm,$(EXAMPLE_PERMISSIONS), \
		$(ADB) shell pm grant $(PACKAGE_NAME) $(perm) 2>/dev/null || true;)
	$(ADB) shell am start -n $(PACKAGE_NAME)/android.app.NativeActivity

clean:
	rm -rf $(BUILD)
