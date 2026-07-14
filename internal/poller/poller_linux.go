//go:build linux

// Package poller (Linux) provides Linux-native implementations for directory monitoring.
//
// Objective:
// Implement the OSUtils interface using Linux-native system calls and standard library functions
// to ensure robust file locking and non-recursive directory scanning.
//
// Core Functionality:
// - File Locking: Uses the flock system call for advisory file locking.
// - Directory Scanning: Uses standard POSIX-compliant directory reading.
//
// Data Flow:
// 1. Locking: IsLocked attempts to acquire a non-blocking exclusive lock via flock.
// 2. Constraints: HasSubfolders and GetFiles enforce the non-recursive directory requirement.
package poller

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// linuxOSUtils provides Linux-native implementations for file locking and directory scanning.
//
// Objective: Maintain cross-platform parity by implementing native Linux logic
// for directory constraints and file integrity.
type linuxOSUtils struct {
	limit int
}

func newOSUtils(limit int) OSUtils {
	return &linuxOSUtils{limit: limit}
}

// IsLocked checks if a file is locked by another process using flock.
//
// Objective: Detect active writes or locks on Linux to prevent processing
// incomplete files.
//
// Logic:
// - Opens the file for reading.
// - Performs an os.Stat to check if the path is a directory.
// - Attempts to acquire an exclusive lock (LOCK_EX) in non-blocking mode (LOCK_NB).
// - If flock fails with EWOULDBLOCK or EAGAIN, the file is considered locked.
func (l *linuxOSUtils) IsLocked(path string) (bool, error) {
	// 1. Initial Stat to check if it's a directory
	stat, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if stat.IsDir() {
		return false, fmt.Errorf("IsLocked: %s is a directory", path)
	}

	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return false, err
	}
	defer func() {
		_ = f.Close()
	}()

	// Try to acquire an exclusive lock in non-blocking mode.
	// If another process has the file open/locked, this will fail.
	fd := f.Fd()
	// #nosec G115 - file descriptor conversion is safe on Linux
	err = unix.Flock(int(fd), unix.LOCK_EX|unix.LOCK_NB)
	if err != nil {
		if err == unix.EWOULDBLOCK || err == unix.EAGAIN {
			return true, nil
		}
		return false, fmt.Errorf("flock failed for %s: %w", path, err)
	}

	// Lock acquired successfully, release it.
	// #nosec G115 - file descriptor conversion is safe on Linux
	_ = unix.Flock(int(fd), unix.LOCK_UN)
	return false, nil
}

func (l *linuxOSUtils) HasSubfolders(dir string) (bool, error) {
	f, err := os.Open(filepath.Clean(dir))
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()

	for {
		entries, err := f.ReadDir(1000)
		if err != nil {
			if err == io.EOF {
				break
			}
			return false, err
		}

		for _, entry := range entries {
			if entry.IsDir() {
				return true, &ErrSubfolderDetected{Path: entry.Name()}
			}
		}
	}

	return false, nil
}

func (l *linuxOSUtils) GetFiles(dir string) ([]string, error) {
	f, err := os.Open(filepath.Clean(dir))
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var files []string
	for {
		entries, err := f.ReadDir(1000)
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}

		for _, entry := range entries {
			if entry.IsDir() {
				return nil, &ErrSubfolderDetected{Path: entry.Name()}
			}
			fullPath := filepath.Join(dir, entry.Name())
			if IsExcluded(fullPath) {
				continue
			}
			files = append(files, fullPath)
			if l.limit > 0 && len(files) >= l.limit {
				return files, nil
			}
		}
	}

	return files, nil
}

func (l *linuxOSUtils) Stat(path string) (os.FileInfo, error) {
	return os.Stat(path)
}
