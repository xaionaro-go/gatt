//go:build android

package jni

/*
#include <android/log.h>
#include <stdlib.h>

static void gattLogcatInfo(const char* tag, const char* msg) {
	__android_log_print(ANDROID_LOG_INFO, tag, "%s", msg);
}
*/
import "C"

import "unsafe"

func logcatInfo(
	tag string,
	msg string,
) {
	cTag := C.CString(tag)
	cMsg := C.CString(msg)
	C.gattLogcatInfo(cTag, cMsg)
	C.free(unsafe.Pointer(cTag))
	C.free(unsafe.Pointer(cMsg))
}
