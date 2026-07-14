# DESIGN: Directory Poller

This document outlines the technical design and implementation details of the Directory Poller.

## 1. System Overview
DirPoller is a high-performance Go-based utility for Windows Server (2019+) and Linux designed to monitor directories and process files based on specific polling and integrity rules. It can run as a CLI or as a system service (Windows Service / Linux Systemd).

## 2. Architecture & Components

### 2.0 OS Isolation & Portability
To ensure maximum native performance across platforms, the application employs a strict interface-driven isolation strategy:

- **Platform-Specific Implementations**:
  - **Logging**: A generic `Logger` interface is implemented via `windowsLogger` (using the native **Windows EventLog**) and `linuxLogger` (using **syslog/journald**).
  - **OS Utilities**: The `OSUtils` interface abstracts platform-specific file operations. On Windows, this includes using `CreateFile` with `FILE_SHARE_NONE` for robust lock detection. On Linux, it uses `flock` (LOCK_EX|LOCK_NB) for mandatory-style advisory locking.
- **Build Constraints**: Files are isolated using `//go:build` tags (e.g., `*_windows.go`, `*_linux.go`).
- **Interface Injection**: Core components like the `Engine`, `Poller`, and `Verifier` are entirely platform-agnostic, receiving OS-specific implementations via interfaces at runtime.

### 2.1 Polling Engine (`internal/poller`)
The engine supports four mutually exclusive algorithms:
- **Interval**: Scans the directory every `n` seconds. (Cross-platform)
- **Batch**: Accumulates files until a count threshold is reached. If the threshold is not reached, files are processed after a fallback timeout. (Cross-platform)
- **Event**: Real-time monitoring using OS-native APIs (`ReadDirectoryChangesW` on Windows, `inotify` on Linux). Files are processed once they are fully committed. Includes a debounce mechanism (500ms) and event coalescing to group rapid-burst file write notifications (bursts within 50ms) into unified micro-batch slices to prevent file-access collisions.
- **Trigger**: Waits for a specific file pattern (exact name or wildcard like `name_*.ext`) to appear before processing all pending files. (Cross-platform)

**Fallback Timeout**: For **Batch** and **Trigger** modes, a configurable timeout (default 10m) forces processing of any pending files even if the threshold or trigger file hasn't appeared.

**Recursive Constraint**: Recursive scanning is strictly forbidden. If a subfolder is detected:
- **CLI**: Aborts immediately with an error.
- **Service/Daemon**: Logs the error to the system event log and continues to wait for the next polling cycle.

### 2.2 Integrity Verification (`internal/integrity`)
Before any action, files must pass an integrity check (configurable `n` attempts every `n` seconds):
- **Lock Check**: 
  - **Windows**: Uses Windows-native `CreateFile` with `FILE_SHARE_NONE` to ensure the file is not being written to by another process.
  - **Linux**: Uses `flock` (LOCK_EX|LOCK_NB) to detect active writes or locks by other processes.
- **Hash-based**: Uses `XXH3-128` to verify content consistency, utilizing pooled byte buffers (`sync.Pool` with `*[]byte`) to eliminate heap allocation overhead.
- **Timestamp-based**: Monitors `LastWriteTime` (Windows) or `mtime` (Linux) for changes.
- **Size-based**: Monitors file size for changes.

### 2.3 Action Handlers (`internal/action`)
- **Internal (SFTP)**: 
  - Multi-threaded upload using a semaphore-controlled worker pool. (Cross-platform)
  - Supports Password, SSH Key, or Multi-factor (Key + Password) authentication.
  - Supports incremental/resume methods (delete vs move) as post-actions.
  - Performance: Parallel connections default to `Cores x 2`.
- **External (Script)**: 
  - Executes a configured script with the file path as an argument.
  - **Platform Support**: Supports `.exe`, `.bat`, `.ps1` on Windows; any executable binary or shell script on Linux.
  - **Performance**: Parallelized using a semaphore-controlled worker pool.
  - Enforces a maximum execution timeout and absolute path validation.

### 2.4 Post-Processing (`internal/archive`)
After successful action execution, files undergo one of the following exclusive post-actions:
- **Delete**: Immediate removal of processed files.
- **Move (Archive)**: Moves files to a datestamped folder (`YYYYMMDD-HHMMSS`).
- **Move (Compress)**: Consolidates the batch into a single `.zst` archive using multi-threaded compression (`klauspost/compress/zstd`).

## 3. Configuration System (`internal/config`)
All parameters are specified in a JSON file. The system implements:
- **Validation**: Ensures mandatory fields (directory, host, paths) are present and valid.
- **Defaults**: Sensible defaults for intervals, attempts, and performance settings. `service_name` is defaulted to "DirPoller" on **Windows only**.
- **Security**: Ensures script paths are absolute and validated.

## 4. System Integration (`internal/service`)
- **Windows**: Implements `winsvc` for Start/Stop/Pause/Continue. Uses the native Windows EventLog (`DirPoller` source).
- **Linux**: Implements standard Systemd unit lifecycle support. Logs to `syslog` or `journald`.

### 4.2 Linux Service Installation
DirPoller supports native Linux service management via systemd template units. This allows running multiple instances of the poller, each with its own configuration.
- **Unit Names**: In Linux, the service name is strictly managed via the `-name` CLI flag (e.g., `-name dirpoller@siteA`). The `service_name` directive in `config.json` is **ignored** on Linux.
- **Parameterized Units**: The installer uses the `dirpoller@.service` template.
  - **Unit Name**: Configured via the `-name <unit_name>@<config_name>` flag.
  - **Config Mapping**: The `%i` specifier in the unit file maps to `<config_name>`, pointing to `/etc/dirpoller/<config_name>.json`.
  - **Example**: `-name dirpoller@siteA` references `dirpoller@siteA.service` and uses `siteA.json`.

- **Installer Logic (`internal/service/service_linux.go`)**:
  - **Prerequisite**: Requires root privileges (verified via `os.Getuid()` or `sudo`).
  - **Installation (`-install`)**:
    - Copies the unit template to `/etc/systemd/system/<unit_name>@.service`.
    - Substitutes `User` and `Group` in the unit file if provided via the `-user user:group` flag.
    - Updates `ExecStart` to the parameterized format: `/usr/local/bin/dirpoller -config /etc/dirpoller/%i.json`.
    - Executes `systemctl daemon-reload` and `systemctl enable`.
    - Overwrites existing templates with a warning.
  - **Removal (`-remove`)**:
    - Disables and stops the specific instance (`systemctl disable`, `systemctl stop`).
    - Removes the template unit file from `/etc/systemd/system/`.

## 5. Logging Facility (`internal/service/custom_logger.go`)
The application implements a dual-track logging system (configurable via CLI or JSON):

- **System Logs (Daily)**: Tracks process-level events (start, stop, OS issues).
  - Format: `base_process_YYYYMMDD.log`
  - Integration: Mirrored to OS system logs (EventLog on Windows, Syslog on Linux).
- **Activity Logs (Per Execution)**: Detailed report of data movement.
  - Format: `base_activity_YYYYMMDD-HHMMSS.log`
  - Structure: Includes a #Status summary (total, OK, error) and categorized lists of files with size and XXH3-128 hash.

- **Log Retention**: If `log_retention` > 0, the engine automatically purges both process and activity logs older than the specified number of days. Purge executes once a day at 00:00:00.

## 7. Password Encryption (`internal/config`)
To ensure that sensitive credentials are never stored in plaintext, DirPoller implements mandatory password encryption for SFTP actions using the `secretprotector` library.

### 7.1 Security Architecture
- **Mandatory Encryption**: The `password` field is no longer accepted in the JSON configuration. Users must provide an `encrypted_password` (Base64-encoded AES-256-GCM ciphertext).
- **Master Key Resolution**: The 32-byte master key is resolved per-batch during the action execution phase. The source is strictly platform-dependent:
    - **Linux**: Secured Key File. Path is configurable via `master_key_file` (default: `~/.secretprotector.key`).
    - **Windows**: User-level Environment Variable. Name is configurable via `master_key_env` (default: `SECRETPROTECTOR_KEY`).
- **Restriction**: Master keys must be provided via the appropriate platform-specific source. CLI overrides and cross-platform sources (e.g., environment variables on Linux or key files on Windows) are explicitly ignored and will fail validation.
- **Memory Safety**: 
    - The master key is resolved and used to decrypt the password exactly once.
    - Immediately after decryption, the master key buffer is zeroed using `libsecsecrets.ZeroBuffer`.
    - The decrypted password is held in memory for the duration of the process and never logged or written to disk.
- **Fail-Fast**: If the master key cannot be resolved or decryption fails, the engine will abort immediately with a security error.

### 7.2 Implementation Details
- **Dependency**: Linked to `criticalsys/secretprotector` via Go module replace directive.
- **Validation**: The configuration validator enforces that either an `encrypted_password` or an `ssh_key_path` is present for SFTP actions.
- **Efficiency**: Per-batch decryption ensures that sensitive credentials only exist in memory during active processing, minimizing the window of exposure.
- **Platform Specifics**: 
    - **Windows**: The key MUST be stored in a **user-level** environment variable. The library blocks access to insecure file locations, ensuring only memory-resident keys are used on Windows.
    - **Linux**: The key MUST be stored in a file with owner-only permissions (`0400` or `0600`). Defaults to `${HOME}/.secretprotector.key`.

## 8. Security Design
- **Input Sanitization**: All file paths are converted to absolute paths and 
validated before processing.
- **Resource Management**: Throttled concurrency for both SFTP and Script actions. 
Streaming I/O and context-aware cancellation are used throughout to prevent memory/
socket exhaustion and ensure clean shutdowns.
- **Authentication**: Secure handling of SSH keys and encrypted passwords via the 
`golang.org/x/crypto/ssh` and `secretprotector` modules.

## 9. Strict Absolute Path Enforcement

To ensure maximum security and prevent accidental data loss or unauthorized access, DirPoller enforces a strict **Absolute Path Only** policy for all critical filesystem operations.

### 9.1 Enforcement Scope
The following configuration fields and CLI arguments **MUST** be provided as absolute paths:
- **Poll Directory**: `poll.directory`
- **Archive Path**: `action.post_process.archive_path` (Mandatory for all post-actions, including `delete`)
- **Script Path**: `action.script.path`
- **SSH Key Path**: `action.sftp.ssh_key_path`
- **Log Name**: `logging[].log_name`
- **CLI Flags**: `-config` and `-log` arguments.

### 9.2 Validation Logic
- **Fail-Fast**: Validation occurs during configuration loading (`LoadConfig`) and CLI flag parsing. The application will abort immediately with a descriptive error if any path is relative or contains path traversal sequences (`..`).
- **Platform Agnostic**: Uses `filepath.IsAbs` to enforce OS-native absolute path semantics:
    - **Windows**: Supports drive-letter paths (e.g., `C:\Data\Poll`) and **UNC paths** (e.g., `\\Server\Share\Path`).
    - **Linux**: Supports standard root-anchored paths (e.g., `/var/lib/dirpoller`).
- **No Fallbacks**: The system does not attempt to resolve relative paths or provide default relative locations (e.g., `./logs`).
- **Redundant Safety**: In addition to early validation, core components (Archiver, Logger) perform secondary checks before any filesystem interaction to prevent logic bypass.

### 9.3 Security Rationale
- **Path Traversal Prevention**: Strictly forbids `..` in all paths to prevent escaping the intended directory structure.
- **Service Stability**: Ensures that when running as a background service (where the working directory may be non-obvious), all resources are explicitly and predictably located.
- **Script Integrity**: The `ActionScript` handler verifies the existence and absolute path of the target executable to prevent execution of shadowed binaries in the system PATH.

## 10. High-Performance SFTP Upload Engine

The SFTP engine is designed for high-throughput, resilient transfers, specifically optimized for modern SFTP servers like **SFTPGo**.

### 10.1 Concurrency & Performance
- **Worker Pool**: Uses a semaphore-controlled pool of goroutines to process multiple files in parallel.
- **Session Multiplexing & Lazy Pruning**: Reuses a single `ssh.Client` to multiplex multiple `sftp.Client` sessions. Sessions are retrieved from a connection pool and validated prior to checkout. Idle connections older than 5 minutes are dynamically closed and pruned from references to prevent resource leaks.
- **TCP Optimization**: 
  - **Max Packet Size**: Configured with `sftp.MaxPacket(1MB)` to maximize throughput for modern servers.
  - **Buffer Management**: Uses `io.CopyBuffer` with a matching **1MB buffer** to minimize syscalls and optimize TCP packet sizing. Buffers are pooled using `sync.Pool` with slice pointers (`*[]byte`) to prevent Go interface conversions and eliminate heap allocations.
  - **Legacy Fallback**: Automatically falls back to standard packets if the server does not support large packets.

### 10.2 Atomic Upload Protocol (Data Integrity)
To prevent the remote server from processing partial or corrupted files, a strict three-step sequence is enforced:
1. **Stage**: Create the file on the remote server with a unique temporary extension: `filename.ext.<UUID>.tmp`.
2. **Transfer**: Stream data into the `.tmp` file using the optimized 1MB buffer.
3. **Commit**: Perform an atomic remote `Rename` from the `.tmp` path to the final destination.
4. **Verification**: Immediately follow the rename with an SFTP `Stat` to verify that the remote file size matches the local source.

### 10.3 Resilience & Error Handling
- **Categorized Retries**: 
  - **Retriable**: `EOF`, `connection reset`, `timeout`, `broken pipe`. Implements exponential backoff (1s to 30s).
  - **Non-Retriable**: `Permission Denied`, `Disk Full`. Fails immediately to prevent resource waste.
- **Circuit Breaker**: If 3 consecutive workers fail with connection-level errors, the entire pool is halted to prevent log flooding and resource exhaustion.
- **Forced Disconnect**: Uses `context.WithTimeout` to ensure hangs in the SSH layer do not block the engine indefinitely.

### 10.4 Remote Cleanup & Orphan Management
- **Immediate Cleanup**: Within the upload lifecycle, a `defer` block ensures that if any step before `Rename` fails, the partial `.tmp` file is removed from the remote server.
- **RemoteCleanup Schedule**:
  - **CLI Mode**: Executes once during application bootstrap.
  - **Service Mode**: Executes daily at **00:00:00** (during the first polling cycle after midnight) to minimize performance impact during peak hours.
- **Cleanup Logic**: Scans the remote directory using `ReadDir`. Any `*.tmp` file older than 24 hours is deleted to prevent orphan accumulation.
