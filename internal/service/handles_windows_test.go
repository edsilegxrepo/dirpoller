//go:build windows

package service

import (
	"syscall"
	"unsafe"
)

var (
	modkernel32               = syscall.NewLazyDLL("kernel32.dll")
	procGetProcessHandleCount = modkernel32.NewProc("GetProcessHandleCount")
)

func getOpenHandles() (int, error) {
	var count uint32
	r1, _, err := procGetProcessHandleCount.Call(
		uintptr(0xffffffffffffffff), // Current process pseudo-handle
		uintptr(unsafe.Pointer(&count)),
	)
	if r1 == 0 {
		return 0, err
	}
	return int(count), nil
}
