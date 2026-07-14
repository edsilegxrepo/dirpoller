// Package action (Key Resolver) provides platform-agnostic master key resolution.
//
// Objective:
// Resolve the master encryption key used to decrypt sensitive credentials.
// It abstracts the storage mechanism (Environment Variable or File) based on
// the operating system.
//
// Core Components:
// - keyResolver Interface: Universal API for platform-specific key retrieval.
// - newKeyResolver: Factory function that returns the appropriate platform implementation.
//
// Data Flow:
// 1. Initialization: The Engine or SFTPHandler requests a key resolver.
// 2. Platform Selection: build tags determine which implementation (Windows/Linux) is compiled.
// 3. Resolution: The resolver fetches the key from the OS-native source (Env or File).
package action

import (
	"context"

	"criticalsys.net/dirpoller/internal/config"
)

// keyResolver defines the interface for resolving the master encryption key.
//
// Logic:
// 1. ResolveMasterKey: Retrieves the key from the configured platform source.
type keyResolver interface {
	ResolveMasterKey(ctx context.Context, sftpCfg *config.SFTPConfig) ([]byte, error)
}

// newKeyResolver is a factory function that returns a platform-specific key resolver.
func newKeyResolver() keyResolver {
	return newPlatformKeyResolver()
}
