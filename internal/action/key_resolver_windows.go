//go:build windows

// Package action (Windows Key Resolver) provides Windows-specific master key resolution.
//
// Objective:
// Implement master key retrieval from environment variables, defaulting to
// SECRETPROTECTOR_KEY.
//
// Core Functionality:
//   - Environment-based Resolution: Specifically designed for Windows environments where
//     keys are stored in environment variables.
//   - Default Naming: Automatically falls back to SECRETPROTECTOR_KEY if not specified.
//
// Data Flow:
// 1. Env Resolution: Uses the configured MasterKeyEnv or defaults to SECRETPROTECTOR_KEY.
// 2. Retrieval: Calls libsecsecrets.ResolveKey to fetch and validate the key from the environment.
package action

import (
	"context"
	"criticalsys/secretprotector/pkg/libsecsecrets"

	"criticalsys.net/dirpoller/internal/config"
)

type windowsKeyResolver struct{}

// ResolveMasterKey retrieves the master key from an environment variable on Windows.
//
// Data Flow:
// 1. Env Resolution: Uses the configured MasterKeyEnv or defaults to SECRETPROTECTOR_KEY.
// 2. Retrieval: Calls libsecsecrets.ResolveKey to fetch and validate the key from the environment.
func (r *windowsKeyResolver) ResolveMasterKey(ctx context.Context, sftpCfg *config.SFTPConfig) ([]byte, error) {
	envName := sftpCfg.MasterKeyEnv
	if envName == "" {
		envName = "SECRETPROTECTOR_KEY"
	}
	return libsecsecrets.ResolveKey(ctx, "", envName, "")
}

func newPlatformKeyResolver() keyResolver {
	return &windowsKeyResolver{}
}
