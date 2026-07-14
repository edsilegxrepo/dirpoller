# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.0.0] - 2026-07-14

### Added
- **Event Coalescing**: Event-based poller now coalesces rapid-burst filesystem write events (under 50ms) into unified micro-batch slices, mitigating redundant directory scans and file-access collisions.
- **Lazy Connection Pruning**: Dynamic pruning of idle connections in the SFTP connection pool. Sessions idle for more than 5 minutes are closed and discarded during checkout to prevent socket leaks.
- **Dynamic Test Workspace Isolation**: Relocated test credentials and key generation to runtime-isolated temp workspaces, keeping the repository 100% clean of hardcoded secrets.

### Fixed
- **NTFS Sharing Violations**: Enforced strict handle closure ordering in `internal/archive` to guarantee file and directory descriptors are closed before OS-level unlinks (fixing Windows-native locks).
- **Security Compliance**: Addressed all Gosec, Semgrep, and AST-Grep warnings across platform-specific scripts.
- **Linter Cleanliness**: Fixed all GolangCI-Lint warnings (buffer pools, empty branches, unchecked defer closes).

### Changed
- **Zero-Allocation Buffer Pooling**: Converted `sync.Pool` byte slice caches in `internal/action` (SFTP) and `internal/integrity` (Hashing) to use slice pointers (`*[]byte`). This avoids Go runtime interface conversion heap allocations and drastically decreases GC overhead at 1M scale.
- **Documentation**: Updated Architecture, Design, and Testing specifications to align with latest design changes.

## [0.1.2] - 2026-03-25

### Fixed
- **SFTP Authentication**: Added support for `keyboard-interactive` authentication to resolve "no supported methods remain" errors with modern SFTP servers like SFTPGo 2.7.0.
- **SFTP Connectivity**: Strict `HostKeyAlgorithms` is able to match multiple key types (RSA, ECDSA, Ed25519), preventing related "host key mismatch" errors.
- **Session Management**: Fixed a race condition where `RemoteCleanup` would attempt to connect before password decryption was complete by centralizing decryption logic in `getOrCreateClient`.
- **Build**: Resolved a Go vet/typechecking regression in `internal/action/sftp_test.go` by updating unit tests to provide the required `context.Context` to session management functions.

### Added
- **Observability**: Introduced diagnostic logging for the password decryption phase (`[Action:SFTP] security: password decrypted successfully`) to help distinguish between local security failures and remote connection issues.
- **Security**: Enhanced memory hygiene in `RemoteCleanup` by ensuring decrypted passwords are wiped from memory (ZeroBuffer) immediately after use.

### Changed
- **Architecture**: Moved password decryption from `Execute` to `getOrCreateClient` to ensure consistent credential availability across all SFTP operations (Uploads and Cleanup).
- **Performance**: Optimized session heartbeats to use `Stat(".")` instead of `Getwd()` for lighter connectivity checks.
