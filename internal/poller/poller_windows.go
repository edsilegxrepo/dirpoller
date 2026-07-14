//go:build windows

// Package poller (Windows) provides Windows-native implementations for directory monitoring.
//
// Objective:
// Implement the OSUtils interface using Windows-native APIs to ensure robust
// file locking and non-recursive directory scanning.
//
// Core Functionality:
// - File Locking: Uses Windows CreateFile with FILE_SHARE_NONE for mandatory-like lock detection.
// - Directory Scanning: Uses Windows-specific path handling and standard directory reading.
//
// Data Flow:
// 1. Locking: IsLocked attempts to open the file with zero sharing to detect external locks.
// 2. Constraints: HasSubfolders and GetFiles enforce the non-recursive directory requirement.
package poller

import (
	"io"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// windowsOSUtils provides Windows-native implementations for file locking and directory scanning.
//
// Objective: Leverage the Windows API to provide robust file lifecycle management
// and enforce directory constraints.
type windowsOSUtils struct {
	limit int
}

func newOSUtils(limit int) OSUtils {
	return &windowsOSUtils{limit: limit}
}

// IsLocked checks if a file is locked by another process using Windows-native CreateFile.
//
// Objective: Prevent processing of files that are still being written to by other
// applications (e.g., large file transfers or log writes).
//
// Logic:
// - Attempts to open the file with FILE_SHARE_NONE.
// - If the open fails with ERROR_SHARING_VIOLATION, the file is considered locked.
func (w *windowsOSUtils) IsLocked(path string) (bool, error) {
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return false, err
	}

	// Try to open the file with FILE_SHARE_NONE to check for locks.
	// If it fails with ERROR_SHARING_VIOLATION, the file is locked.
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ,
		0, // FILE_SHARE_NONE
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		if err == windows.ERROR_SHARING_VIOLATION {
			return true, nil
		}
		return false, err
	}
	defer func() {
		if closeErr := windows.CloseHandle(handle); closeErr != nil {
			log.Printf("Warning: failed to close file handle for %s: %v\n", path, closeErr)
		}
	}()

	return false, nil
}

// HasSubfolders performs a non-recursive scan of the directory.
//
// Objective: Enforce the "flat directory" constraint to avoid accidental recursive processing.
//
// Logic:
// - Reads all entries in the configured directory.
// - Returns true and an error if any entry is a directory.
func (w *windowsOSUtils) HasSubfolders(dir string) (bool, error) {
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

// GetFiles retrieves a list of all files in the directory.
// It returns an error if a subfolder is detected during the scan.
func (w *windowsOSUtils) GetFiles(dir string) ([]string, error) {
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
			if w.limit > 0 && len(files) >= w.limit {
				return files, nil
			}
		}
	}

	return files, nil
}

func (w *windowsOSUtils) Stat(path string) (os.FileInfo, error) {
	return os.Stat(path)
}
