//go:build !windows

package service

import (
	"os"
)

func getOpenHandles() (int, error) {
	files, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return 0, err
	}
	return len(files) - 1, nil // Subtract 1 for the ReadDir descriptor itself
}
