//go:build linux

// Package action (Linux Key Resolver) provides Linux-specific master key resolution.
//
// Objective:
// Implement master key retrieval from the filesystem, defaulting to
// ${HOME}/.secretprotector.key.
//
// Core Functionality:
//   - File-based Resolution: Specifically designed for Linux environments where
//     keys are stored in protected files.
//   - Default Pathing: Automatically falls back to the user's home directory.
//
// Data Flow:
// 1. Path Resolution: Uses the configured MasterKeyFile or defaults to ~/.secretprotector.key.
// 2. Retrieval: Calls libsecsecrets.ResolveKey to read and validate the key from disk.
package action

import (
	"context"
	"criticalsys.net/secretprotector/pkg/libsecsecrets"
	"fmt"
	"os"
	"path/filepath"

	"criticalsys.net/dirpoller/internal/config"
)

type linuxKeyResolver struct{}

// ResolveMasterKey retrieves the master key from a local file on Linux.
//
// Data Flow:
// 1. Path Resolution: Uses the configured MasterKeyFile or defaults to ~/.secretprotector.key.
// 2. Retrieval: Calls libsecsecrets.ResolveKey to read and validate the key from disk.
func (r *linuxKeyResolver) ResolveMasterKey(ctx context.Context, sftpCfg *config.SFTPConfig) ([]byte, error) {
	keyPath := sftpCfg.MasterKeyFile
	if keyPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get user home directory: %w", err)
		}
		keyPath = filepath.Join(home, ".secretprotector.key")
	}
	return libsecsecrets.ResolveKey(ctx, "", "", keyPath)
}

func newPlatformKeyResolver() keyResolver {
	return &linuxKeyResolver{}
}
