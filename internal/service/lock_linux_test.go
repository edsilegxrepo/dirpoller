//go:build !windows

package service

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func lockFileExclusively(path string) (io.Closer, error) {
	f, err := os.OpenFile(filepath.Clean(path), os.O_CREATE|os.O_RDWR, 0o666)
	if err != nil {
		return nil, err
	}
	fd := f.Fd()
	err = unix.Flock(int(fd), unix.LOCK_EX|unix.LOCK_NB)
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("flock LOCK_EX failed: %w", err)
	}
	return f, nil
}
