# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.0.4] - 2026-07-18

### Changed
- **Daily Activity Logging**: Refactored the custom activity logging system to group execution summaries into a single daily log file (`base_activity_YYYYMMDD.log`) instead of creating a new file per execution cycle. Execution cycles within the same day are appended to the daily file and separated by a blank line for readability. This reduces log file accumulation, improves directory traversal performance, and decreases filesystem fragmentation.

## [1.0.3] - 2026-07-17

### Added
- **Direct Upload Mode (Disable Atomic Commit)**: Introduced the `disable_atomic_commit` boolean config setting to `SFTPConfig`. When enabled, files are streamed directly to the final destination path, bypassing the `.tmp` staging, remote `Rename`, and remote `Stat` size verification. This reduces the sequential round-trip times (RTT) from 5 to 3 per file, providing an immediate ~40% latency-reduction speedup in high-latency (e.g. 120ms East->West) environments.
- **Dynamic Backlog Draining**: Added backlog tracking (`hasBacklog`) to `BatchPoller`, `EventPoller`, and `TriggerPoller`. If a directory scan returns a full batch (`MaxBatchSize`), a 2-second ticker is activated to drain the remaining backlog in chunks, preventing files from being stranded in the folder. Once drained, the ticker shuts down to conserve CPU.
- **In-Flight File Tracking**: Added a thread-safe global `inFlight` registry in the `poller` package to track files currently being processed in the engine. This prevents duplicate batching/processing of files during catch-up or backlog directory scans.
- **Live Batch Integration Test**: Added `TestLiveSFTPGoBatchIntegration` to verify end-to-end functionality of the batching strategy against a live SFTPGo instance.
- **Archive Retention and Cleanup**: Introduced the `archive_retention` configuration option in `PostProcessConfig` (defaulting to 7 days if omitted/nil). The Engine now automatically purges archives older than this retention period once per calendar day, protecting the system from storage exhaustion. The cleanups are executed via a new shared, generic folder-purging utility.
- **Resilient Event Fallback Scan**: Enabled event-driven pollers to run 2-second fallback directory scans unconditionally when inactive to recover stranded files from dropped OS filesystem events.

### Fixed
- **Nested Semaphore Deadlock**: Decoupled public and internal hash execution in `integrity.go` to prevent workers from double-acquiring slots from the I/O semaphore and causing circular pipeline deadlocks.
- **Circuit Breaker Thundering Herd**: Serialized Half-Open circuit breaker connection probes to allow only a single thread to test the connection while blocking concurrent requests.
- **Active Connection Underflow**: Protected active connection telemetry counters from underflow by verifying session presence before decrementing.
- **SFTP Socket & Session Leak**: Delegated connection pooling session releases to `discardSession` to ensure both SFTP client channels and underlying SSH TCP connections are closed, and session pointers are pruned.
- **Staging Area Preservation**: Excluded the `.staging` folder from the daily archive retention cleanup to prevent deleting active transaction folders and causing transfer failures.
- **Script Memory Exhaustion Bounding**: Implemented `maxBytesWriter` to restrict the maximum captured console output of user-executed scripts to `64KB`, preventing OOM crashes on large outputs.
- **Trigger Poller Backlog Flush Bypass**: Refactored the backlog ticker in `trigger.go` to ensure scanned data files are not flushed prematurely unless the trigger file is also present on disk (or the timeout fires).
- **JSON Unmarshal Poll.Value Types**: Fixed a bug where `Poll.Value` checks failed type assertions (`ok := Value.(int)`) when parsed from JSON (since numbers are unmarshaled as `float64`), which caused interval/batch thresholds to fall back to their defaults (60s / 1).
- **Synchronous Channel Sends (Backpressure)**: Removed goroutine wrappers around results sends in `BatchPoller`, `EventPoller`, and `TriggerPoller` to enforce synchronous channel backpressure, matching the closed-loop design in the `IntervalPoller` and preventing memory/goroutine leaks.
- **Windows Service Control State transition**: Fixed a bug in `service_windows.go` where `StopPending` was sent just before returning (instead of immediately when the stop command is received), preventing the service from correctly reporting `Stopped` to the SCM. SCM now receives `StopPending` instantly and transitioned to `Stopped` upon clean exit, stopping SCM from registering the exit as an abnormal crash.
- **Logger Shutdown Order**: Modified `Engine.Close()` to close the action handler before closing the logger. This prevents warnings or errors encountered during the handler's shutdown phase from attempting to write to a closed event log handle.

## [1.0.2] - 2026-07-15

### Changed
- **High-Speed SFTP Pipelining**: Exposed the `io.ReaderFrom` interface contract in the `SFTPFile` wrapper to trigger `pkg/sftp`'s native concurrent sliding-window write pipeline, resolving the sequential write-wait bottleneck and matching `rclone` parallel upload performance over slow network links.

## [1.0.1] - 2026-07-15

### Added
- **I/O Throttling Semaphore**: Introduced a 64-capacity active I/O semaphore inside the file verifier (`internal/integrity`) to restrict concurrent file descriptor accesses. This protects systems from file descriptor exhaustion (`too many open files` errors) and excessive disk contention under massive concurrent loads.

### Changed
- **Concurrent Sleep Verification**: Refactored the file verification lifecycle to execute verification sleeps (stability checks) in parallel using goroutines. Spawning $N$ concurrent subroutines reduces the verification phase of 5,000 files from 10.4 minutes to exactly the sleep interval (e.g., 1 second) on both Windows and Linux.
- **Sane Configuration Defaults**: Lowered default `max_batch_size` from `10000` to `1000` and default `attempts` from `3` to `1` to prevent out-of-the-box system resource exhaustion.
- **MaxBatchSize Bounds Validation**: Enforced strict configuration limits on `max_batch_size`, requiring it to be between `100` and `10000` inclusive.

### Fixed
- **Integration Test Portability**: Eliminated hardcoded absolute paths to `sftpgo.exe` in integration tests, adding dynamic PATH discovery, environment variable overrides (`SFTPGO_PATH`), and configurable port binding (`SFTPGO_PORT`) to avoid local port conflicts.

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
