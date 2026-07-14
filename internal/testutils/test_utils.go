// Package testutils provides shared utilities for cross-platform unit and integration testing.
//
// Objective:
// Centralize test environment setup, directory management, and platform-detection
// logic to ensure consistent test behavior across Windows and Linux.
//
// Core Components:
// - Directory Management: Helpers for creating unique, clean test environments.
// - Platform Detection: Centralized IsWindows checks.
// - Mocks: Shared interface implementations for mocking system components.
//
// Data Flow:
// 1. Setup: Tests call GetUniqueTestDir to isolate their filesystem impact.
// 2. Execution: Tests use Mocks (e.g., MockSFTPClient) to simulate external systems.
// 3. Cleanup: Utilities handle recursive removal of temporary test artifacts.
package testutils

import (
	"os"
	"path/filepath"
	"runtime"
)

// GetTestBaseDir returns the centralized test base directory.
// On Windows: $env:TEMP\dirpoller_UTESTS
// On Linux: $TEMP/dirpoller_UTESTS
func GetTestBaseDir() string {
	temp := os.Getenv("TEMP")
	if temp == "" {
		temp = os.TempDir()
	}

	baseDir := filepath.Join(filepath.Clean(temp), "dirpoller_UTESTS")

	// Ensure the base directory exists
	_ = os.MkdirAll(baseDir, 0o750)

	return baseDir
}

// GetPackageTestDir returns a unique directory for a package within the test base directory.
func GetPackageTestDir(packageName string) string {
	dir := filepath.Join(GetTestBaseDir(), packageName)
	_ = os.MkdirAll(dir, 0o750)
	return dir
}

// GetUniqueTestDir returns a unique directory for a specific test within a package.
func GetUniqueTestDir(packageName, testName string) string {
	dir := filepath.Join(GetPackageTestDir(packageName), testName)
	// Ensure a clean state for the specific test
	_ = os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0o750)
	return dir
}

// IsWindows returns true if the current OS is Windows.
func IsWindows() bool {
	return runtime.GOOS == "windows"
}
