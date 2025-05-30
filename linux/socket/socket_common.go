//go:build !386
// +build !386

package socket

import (
	"syscall"
	"unsafe"
)

func bind(s int, addr unsafe.Pointer, addrLen SockLen) (err error) {
	_, _, e1 := syscall.Syscall(syscall.SYS_BIND, uintptr(s), uintptr(addr), uintptr(addrLen))
	if e1 != 0 {
		err = e1
	}
	return
}

func setsockopt(s int, level int, name int, val unsafe.Pointer, valueLen uintptr) (err error) {
	_, _, e1 := syscall.Syscall6(syscall.SYS_SETSOCKOPT, uintptr(s), uintptr(level), uintptr(name), uintptr(val), uintptr(valueLen), 0)
	if e1 != 0 {
		err = e1
	}
	return
}
