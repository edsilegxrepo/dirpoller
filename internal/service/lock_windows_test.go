//go:build windows

package service

import (
	"io"
	"syscall"
)

func lockFileExclusively(path string) (io.Closer, error) {
	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := syscall.CreateFile(
		pathPtr,
		syscall.GENERIC_READ|syscall.GENERIC_WRITE,
		0, // FILE_SHARE_NONE
		nil,
		syscall.OPEN_ALWAYS,
		syscall.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		return nil, err
	}
	return handleCloser(handle), nil
}

type handleCloser syscall.Handle

func (h handleCloser) Close() error {
	return syscall.CloseHandle(syscall.Handle(h))
}
