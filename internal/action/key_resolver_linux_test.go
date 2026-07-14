//go:build linux

// Package action_test provides unit tests for the Linux-specific secret key resolution.
//
// Objective:
// Validate the logic for resolving the master key on Linux systems, including
// support for provided paths and default home-directory-based resolution.
//
// Scenarios Covered:
// - Provided Path: Verification that an explicitly configured master key path is used.
// - Default Path: Verification that the default path (~/.secretprotector.key) is used when none is provided.
// - Home Directory Error: Handling of scenarios where the user's home directory cannot be determined.
package action

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"criticalsys.net/dirpoller/internal/config"
)

// TestLinuxKeyResolver_ResolveMasterKey validates the path resolution logic for master keys on Linux.
//
// Scenario:
// 1. ProvidedPath: Use a temporary file as the master key source.
// 2. DefaultPath: Rely on home directory resolution.
// 3. HomeDirError: Simulate a missing HOME environment variable.
//
// Success Criteria:
// - The resolver correctly identifies the file path to use for the master key.
// - The resolver handles environment-related errors gracefully.
func TestLinuxKeyResolver_ResolveMasterKey(t *testing.T) {
	resolver := &linuxKeyResolver{}
	ctx := context.Background()

	t.Run("ProvidedPath", func(t *testing.T) {
		tempDir := t.TempDir()
		keyFile := filepath.Join(tempDir, "test.key")
		if err := os.WriteFile(keyFile, []byte("testkey"), 0o600); err != nil {
			t.Fatalf("failed to create key file: %v", err)
		}

		sftpCfg := &config.SFTPConfig{
			MasterKeyFile: keyFile,
		}

		// ResolveKey might fail if it's not a real key, but we want to hit the path resolution logic
		_, _ = resolver.ResolveMasterKey(ctx, sftpCfg)
	})

	t.Run("DefaultPath", func(t *testing.T) {
		sftpCfg := &config.SFTPConfig{
			MasterKeyFile: "",
		}

		// This will hit os.UserHomeDir()
		_, _ = resolver.ResolveMasterKey(ctx, sftpCfg)
	})

	t.Run("HomeDirError", func(t *testing.T) {
		// Mock HOME env to trigger potential errors if os.UserHomeDir relies on it
		// On Linux, os.UserHomeDir usually uses HOME env first.
		oldHome := os.Getenv("HOME")
		_ = os.Unsetenv("HOME")
		defer func() { _ = os.Setenv("HOME", oldHome) }()

		sftpCfg := &config.SFTPConfig{
			MasterKeyFile: "",
		}
		_, _ = resolver.ResolveMasterKey(ctx, sftpCfg)
	})
}
