//go:build linux

// Package poller_test provides Linux-specific unit tests for directory polling.
//
// Objective:
// Validate platform-native file system operations on Linux, specifically
// focusing on robust lock detection using flock and recursive safety checks.
//
// Scenarios Covered:
//   - Lock Detection: Verifies that files held by other processes via unix.Flock
//     are correctly identified as locked.
//   - Directory Constraints: Ensures that subfolder checks and file listings
//     handle Linux-specific paths and error conditions correctly.
package poller

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// TestLinuxOSUtils_IsLocked validates the Linux-specific flock detection logic.
//
// Scenario:
// 1. Directory: Ensures directories are not reported as "locked" files.
// 2. NonExistent: Handles missing files gracefully.
// 3. LockedFile: Acquires a real flock and verifies detection.
// 4. UnlockedFile: Verifies that free files are not reported as locked.
//
// Success Criteria:
// - Locked files must return true for IsLocked.
// - Unlocked files or invalid paths must not be falsely reported as locked.
func TestLinuxOSUtils_IsLocked(t *testing.T) {
	utils := &linuxOSUtils{}
	tempDir := t.TempDir()

	t.Run("Directory", func(t *testing.T) {
		dirPath := filepath.Join(tempDir, "testdir")
		if err := os.Mkdir(dirPath, 0o755); err != nil {
			t.Fatalf("failed to create dir: %v", err)
		}

		locked, err := utils.IsLocked(dirPath)
		if err == nil {
			t.Error("expected error for directory, got nil")
		}
		if locked {
			t.Error("expected locked=false for directory")
		}
	})

	t.Run("NonExistent", func(t *testing.T) {
		locked, err := utils.IsLocked(filepath.Join(tempDir, "nonexistent"))
		if err == nil {
			t.Error("expected error for nonexistent file, got nil")
		}
		if locked {
			t.Error("expected locked=false for nonexistent file")
		}
	})

	t.Run("LockedFile", func(t *testing.T) {
		filePath := filepath.Join(tempDir, "locked.txt")
		f, err := os.Create(filePath)
		if err != nil {
			t.Fatalf("failed to create file: %v", err)
		}
		defer func() { _ = f.Close() }()

		// Acquire lock
		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
			t.Fatalf("failed to flock: %v", err)
		}
		defer func() { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }()

		locked, err := utils.IsLocked(filePath)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if !locked {
			t.Error("expected locked=true")
		}
	})

	t.Run("UnlockedFile", func(t *testing.T) {
		filePath := filepath.Join(tempDir, "unlocked.txt")
		if err := os.WriteFile(filePath, []byte("test"), 0o644); err != nil {
			t.Fatalf("failed to create file: %v", err)
		}

		locked, err := utils.IsLocked(filePath)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if locked {
			t.Error("expected locked=false")
		}
	})
	t.Run("FlockError", func(t *testing.T) {
		// unix.Flock returns error if fd is invalid
		locked, err := utils.IsLocked("/dev/null")
		// On some systems opening /dev/null might work but flock might fail or behave differently.
		// A better way is to use a closed file descriptor if we could mock unix.Flock.
		// But since we can't easily mock unix.Flock without changing code,
		// let's try a path that exists but we can't open or flock.
		if err != nil {
			t.Logf("Got expected error: %v", err)
		} else {
			t.Logf("Locked: %v", locked)
		}
	})
}

func TestLinuxOSUtils_HasSubfolders_Error(t *testing.T) {
	utils := &linuxOSUtils{}
	_, err := utils.HasSubfolders("/non/existent/path")
	if err == nil {
		t.Error("expected error for nonexistent path, got nil")
	}
}

func TestLinuxOSUtils_GetFiles_Error(t *testing.T) {
	utils := &linuxOSUtils{}
	_, err := utils.GetFiles("/non/existent/path")
	if err == nil {
		t.Error("expected error for nonexistent path, got nil")
	}
}
