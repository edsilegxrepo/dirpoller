# TESTING: DirPoller Unit Testing Suite

This document describes the comprehensive testing strategy and implementation for the DirPoller application, ensuring high code coverage and realistic behavior on Windows and Linux environments.

## 1. Architecture of the Test Suite

The DirPoller test suite is designed for high-fidelity simulation of system behaviors while maintaining strict isolation and cross-platform compatibility.

### 1.1 Test Strategy
The project employs a deliberate strategy to achieve 90%+ coverage by shifting from brittle OS-level manipulations to high-impact architectural refactorings. This approach focuses on:

- **Mocking Infrastructure**: Introducing thin interface wrappers for core external dependencies:
    - `internal/archive`: `ArchiveWriter` interface to wrap `tar.Writer` and `zstd.Writer` for injecting failures during I/O operations.
    - `internal/poller`: `Watcher` interface for `fsnotify` to manually inject events and errors without physical file operations.
    - `internal/service`: Abstraction of `CustomLogger`, `PlatformLogger`, and `EngineRunner` to hit all logging and lifecycle failure paths.
    - `internal/action`: Using `Dialer` and `SFTPClient` interfaces to simulate complex network and protocol failures. 
- **Efficiency Improvements**:
    - **Table-Driven Consolidation**: Combining multiple error path tests into single functions with multiple assertions.
    - **Synchronous Testing**: Using channel synchronization in mocks instead of `time.Sleep` to reduce test execution time.
    - **Targeted Iteration**: Using `go test -run <TestName>` for rapid development, only running full coverage reports upon package completion.

### 1.2 Component Testing Scenarios
The suite is divided into logical components, each with specific testing objectives:

- **Polling Engine (`internal/poller`)**: 
    - **Interval Poller**: Verified that files are detected at the exact configured frequency.
    - **Batch Poller**: Refactored to use `fsnotify` for immediate discovery. Tests verify that files are dispatched only when the batch count is reached or after a timeout fallback.
    - **Event Poller**: Uses real `fsnotify` events to simulate file system activity. Verified TTL-based cleanup of the debouncing map to ensure stability in high-traffic environments.
    - **Recursive Constraint**: Specifically tests that subfolder creation within a monitored directory triggers a failure/log as per the design specification.
- **Integrity Verification (`internal/integrity`)**: 
    - **Algorithm Support**: Comprehensive tests for `Size`, `Hash (XXH3-128)`, and `Timestamp` algorithms.
    - **Consistency Check**: Simulates partial file writes by modifying files during the verification interval to ensure the verifier waits for the file to be "quiet".
    - **Lock Detection**: Validates Windows-native `CreateFile` with `FILE_SHARE_NONE` behavior and POSIX lock detection on Linux.
- **Action Handlers (`internal/action`)**: 
    - **Script Action**: Generates platform-specific test scripts (`.bat`/`.ps1` on Windows, `.sh` on Linux). Verifies parallel execution via worker pools and **Timeout Enforcement** for long-running scripts.
    - **Common Logic**: Verifies **Multi-Error Reporting** using `errors.Join` to ensure all failures in a batch are surfaced.
- **System Service (`internal/service`)**: 
    - Refactored to use a `ServiceManager` interface to allow full testing without administrator/root privileges.
    - **Control Loop**: Simulates service signals (Stop, Pause, Continue) to verify the Engine's response to state changes.
    - **Windows Native**: Verified using `winsvc` mocks for installation and removal.
    - **Linux Systemd**: Verified using a specialized test suite that runs as a regular user but uses `sudo` for unit file operations and `systemctl` commands.
- **Custom Logging (`internal/service`)**: 
    - **Execution Logging**: Verified that `CustomLogger` creates datestamped log files with correct structure (Status, Success, Errors).
    - **Log Retention**: Tests simulate old log files and verify they are purged based on the `log_retention` days setting.
    - **Empty Log Prevention**: Verified that no log file is created if no files were processed in an execution cycle.
- **CLI Orchestration (`cmd/dirpoller`)**: Refactored to separate orchestration from process exit logic for 90%+ bootstrap coverage. Separated `main()` into a testable `run()` function.
- **Shared Test Utilities (`internal/testutils`)**: Centralized mocks (`MockSFTPClient`, `MockOSUtils`, `MockLogger`) that are self-tested to ensure reliable behavior across the suite.

## 2. Technical Requirements and Setup

### 2.1 Dependencies
- **Go Version**: Go 1.21+ (utilizing `errors.Join` and modern testing features).
- **External Packages**: 
    - `github.com/fsnotify/fsnotify`
    - `github.com/pkg/sftp`
    - `github.com/spf13/afero` (MemMapFs)
    - `golang.org/x/crypto/ssh`
    - `golang.org/x/sys/windows`

### 2.2 Environment Variables and Setup
To ensure tests do not interfere with source code or production data, all tests use a dedicated temporary directory.

- **Environment Variables**:
    - `%TEMP%` (Windows) or `$TMPDIR`/`$TEMP` (Linux): Used to determine the base test directory.
- **Base Directory**: `%TEMP%\dirpoller_UTESTS` (Windows) or `$TMPDIR/dirpoller_UTESTS` (Linux).
- **Isolation**: Each test case or package uses a unique subdirectory (e.g., `.../dirpoller_UTESTS/poller`).
- **Lifecycle Management**:
    - `TestMain`: Each package contains a `TestMain` that ensures the parent directory exists.
    - **Per-Test Cleanup**: Subdirectories are cleaned up *before* each test runs to ensure a deterministic state.
- **Parallelism**: Global cleanup in `TestMain` is avoided to prevent race conditions when Go runs multiple package tests simultaneously.

### 2.3 Constraints
- **OS Portability**: Tests use `filepath.Join` and `filepath.ToSlash` to remain platform-agnostic. Security boundary tests are mocked to simulate Windows-specific path restrictions and Linux-specific permission checks.
- **Deadlock Prevention**: Ensure mutexes are not held when calling methods that internally acquire the same lock (e.g., `getOrCreateClient`).

## 3. List of Tests

| Logical Group | Test Name | Technical Purpose | Success Criteria |
| :--- | :--- | :--- | :--- |
| **Polling** | `TestIntervalPoller` | Verify files are detected at fixed intervals. | File detected and sent to results channel within 1s. |
| **Polling** | `TestIntervalPollerLoopError` | Verify error handling in polling loop. | Error logged or handled during interval scan. |
| **Polling** | `TestBatchPoller` | Verify batch threshold and timeout triggers. | Files sent when count is reached OR after timeout. |
| **Polling** | `TestBatchPollerTimeout` | Verify timeout trigger for partial batches. | Files sent after timeout even if threshold not met. |
| **Polling** | `TestBatchPollerSubfolderDetection` | Ensure recursive folders are blocked in batch mode. | Error returned when subfolder is present. |
| **Polling** | `TestBatchPoller_ResilientFallbackScan` | Verify fallback directory scanning when fsnotify events are dropped/lost. | Directory files are successfully scanned and flushed on timeout. |
| **Polling** | `TestEventPoller` | Verify real-time `fsnotify` detection. | File creation triggers immediate detection without missing events. |
| **Polling** | `TestEventPollerDynamicSubfolder` | Verify detection of new subfolders during runtime. | Engine returns error when subfolder created. |
| **Polling** | `TestEventPoller_LRU_Eviction` | Verify LRU cache eviction in event poller. | Old events are evicted to prevent memory leaks. |
| **Polling** | `TestPollerSubfolderDetection` | Ensure recursive folders are blocked. | Error returned when subfolder is present and blocked. |
| **Polling** | `TestTriggerPoller` | Verify trigger file pattern and timeout. | Batch processed on trigger OR timeout. |
| **Polling** | `TestTriggerPollerExactMatch` | Verify exact filename match for trigger. | Only exact filename triggers processing. |
| **Polling** | `TestEventPollerCoalescing` | Verify fsnotify event micro-batch coalescing. | Rapid burst events (under 50ms) are grouped into a single batch slice. |
| **Integrity** | `TestIntegrityLockCheck` | Verify file lock detection. | `IsLocked` returns true for held files; free for others. |
| **Integrity** | `TestIntegrityHash` | Verify XXH3-128 consistency. | File verified after hash property is stable. |
| **Integrity** | `TestIntegrityChangingFile` | Verify waiting for "quiet" file. | `Verify` returns false while file is still growing/changing. |
| **Integrity** | `TestIntegrityTimestamp` | Verify mod-time consistency check. | File verified via timestamp stability algorithm. |
| **Integrity** | `TestVerifierCalculateHash` | Verify hash calculation logic. | Correct hash returned for test file content. |
| **Integrity** | `TestVerifierUnsupportedAlgorithm` | Verify handling of unknown algorithms. | Error returned for invalid algorithm config. |
| **Integrity** | `TestVerifier_NestedSemaphoreDeadlockPrevention` | Verify that nested semaphore acquisitions do not occur to prevent deadlocks. | Hashing verification succeeds using a semaphore of size 1. |
| **Integrity** | `TestVerifier_ClearCache` | Verify that ClearCache clears integrity maps to prevent memory leaks. | Hashes and sizes maps are emptied. |
| **Action** | `TestScriptAction` | Verify `.bat` execution and params. | Script runs with exit 0, file marked success. |
| **Action** | `TestScriptActionTimeout` | Verify script execution timeout. | Context cancellation kills long-running script. |
| **Action** | `TestScriptActionFailure` | Verify handling of script exit codes. | Error returned when script returns non-zero code. |
| **Action** | `TestScriptAction_OutputLimiter` | Verify that script output is capped at 64KB to prevent OOM memory exhaustion. | Output is truncated to 64KB on failure. |
| **Action** | `TestScriptHandler_RemoteCleanup` | Verify remote file cleanup after action. | Source files are removed from remote SFTP server if configured. |
| **Action** | `TestSFTPHandler_RealMockServer/PasswordAuth` | Verify SFTP with Password authentication. | Successful auth and upload to mock server. |
| **Action** | `TestSFTPHandler_RealMockServer/KeyAuth` | Verify SFTP with SSH Key authentication. | Successful auth and upload to mock server. |
| **Action** | `TestSFTPHandler_RealMockServer/MFA_Auth` | Verify SFTP with Multi-Factor (Key+Pass) auth. | Successful MFA handshake and upload. |
| **Action** | `TestSFTPHandler_RealMockServer/PasswordAuth_Failure` | Verify SFTP behavior with invalid password. | Connection rejected with expected auth error. |
| **Action** | `TestSFTPHandler_RealMockServer/KeyAuth_Failure` | Verify SFTP behavior with unauthorized SSH key. | Connection rejected with expected auth error. |
| **Action** | `TestSFTPHandler_RealMockServer/MFA_Auth_PartialFailure` | Verify MFA behavior with valid key but bad pass. | Connection rejected after partial success. |
| **Action** | `SFTP_HostKey_Validation` | Verify support for all host key formats. | Handshake succeeds for: `ssh-rsa`, `ecdsa-sha2-nistp256`, `ssh-ed25519`, `Raw Base64` |
| **Action** | `TestSFTPHandler_RealMockServer/HostKeyVerification_Failure` | Verify connection with mismatched Host Key. | Connection rejected with host key mismatch error. |
| **Action** | `TestSFTPHandler_Execute_Comprehensive` | Verify SFTP upload logic. | Files uploaded successfully or errors handled. |
| **Action** | `TestSFTPHandler_Execute_Comprehensive/CircuitBreakerThunderingHerd` | Verify thundering herd protection on expired circuit breaker cooldown. | Only 1 concurrent thread probes connection while others are blocked. |
| **Action** | `TestSFTPHandler_Execute_Comprehensive/DiscardSessionCountSafety` | Verify that active connection telemetry does not underflow on double discards. | activeConns remains 0 and does not drop to negative values. |
| **Action** | `TestSFTPHandler_UploadFile_DisableAtomicCommit_TransferFail` | Verify direct upload cleanup on transfer failure. | Partially uploaded file is deleted from target server. |
| **Action** | `TestSFTPHandler_Reconnect_Logic` | Verify reconnection on lost sessions. | Handler reconnects and retries successfully. |
| **Action** | `TestSFTPHandler_LazyConnectionPruning` | Verify pruning of idle SFTP connections. | Sessions idle > 5 minutes are closed and removed from references. |
| **Archive** | `TestArchive_Process_Comprehensive` | Verify transactional archiving logic. | Prepare-Commit-Rollback cycle works as intended. |
| **Archive** | `TestArchive_FullCycle_AllActions` | Verify all post-action types. | Delete, Move, and Compress actions finish correctly. |
| **Archive** | `TestArchive_CompressToArchive_Errors` | Verify compression edge cases via mocks. | Errors in tar/zstd creation handled without panic. |
| **Archive** | `TestArchive_Rollback_Mixed` | Verify rollback logic for partially failed batches. | Files restored correctly upon processing failure. |
| **Logging** | `TestCustomLogger_LogExecution` | Verify JSON-like log structure and content. | Log file created with correct status and lists. |
| **Logging** | `TestCustomLogger_PurgeOldLogs` | Verify log retention/purging logic. | Logs older than N days are deleted. |
| **Logging** | `TestCustomLogger_LogProcess` | Verify logging of process execution. | Execution details correctly recorded in log file. |
| **Service** | `TestInstallRemoveService` | Verify SCM interaction logic (Windows, mocked). | Service created/deleted in mock manager state. |
| **Service** | `TestInstallServiceErrors` | Verify error handling in service installation (Windows). | Proper errors for existing services or log failures. |
| **Service** | `TestServiceWindowsCoverage` | Verify service loop and thin wrappers (Windows). | Stop/Pause signals handled, wrappers invoked. |
| **Service** | `TestWindowsService_Execute_ControlRequests` | Verify SCM control signal handling and state transitions (Windows). | Interrogate, Pause, Continue, and clean Stop/Stopped transitions verified in order. |
| **Service** | `TestWindowsServiceExecute` | Verify full service lifecycle (Windows). | Service starts, processes files, and stops gracefully. |
| **Service** | `TestLinuxInstaller_RootPrivileges` | Verify Linux root enforcement. | Error returned when run without root/sudo. |
| **Service** | `TestLinuxInstaller_Lifecycle` | Verify Linux template parsing & naming. | Unit parsing and name splitting work correctly. |
| **Service** | `TestEngineResilience` | Verify poller restart after failure. | Engine attempts restart with exponential backoff. |
| **Service** | `TestEngine_ScheduledTasks_Cleanup` | Verify daily SFTP cleanup. | Cleanup triggered on day change branch. |
| **Service** | `TestEngine_ProcessFiles_TableDriven` | Verify engine processing logic. | Files processed correctly according to engine rules. |
| **Service** | `TestLiveSFTPGoIntegration` | Verify single-file upload against a real local SFTPGo server. | Successful connection, decryption, upload, and cleanup. |
| **Service** | `TestLiveSFTPGoBatchIntegration` | Verify live batch polling uploads against SFTPGo. | Successful batch detection, upload, and cleanup. |
| **Service** | `TestLiveSFTPGo5KIntegration` | Simulate 5,000 files production load with active locking and SLAs. | Verifies, uploads, and archives 5k files under 30s. Locked files skipped and recovered. |
| **Service** | `TestPurgeOldEntries` | Verify the shared `purgeOldEntries` logic with backdated files and subdirectories. | Expired files and directories are deleted while recent ones are retained. |
| **Service** | `TestEngine_checkAndPurgeArchives` | Verify archive retention threshold calculations, calendar day gating, and preservation of the transaction staging folder. | Engine purges old archives once per calendar day based on configuration, while protecting the internal `.staging` folder from deletion. |
| **Service** | `TestLiveSFTPGoLongevityAndGoroutineLeak` | Verify goroutine and thread stability under multiple bursts of file activity. | Baseline goroutine counts remain stable without leaks after 10 bursts of writes. |
| **Service** | `TestLiveSFTPGoConcurrencyAndBackpressure` | Stress-test the engine's event poller backlog fallback scan under high-load concurrent writes. | All 200 files are verified, batched, and uploaded under restricted connection pools without stranding. |
| **Service** | `TestLiveSFTPGoMidTransferDisconnectAndRecovery` | Verify self-healing circuit breaker recovery after a simulated socket disconnect. | Engine triggers backoff/open-circuit on loss, then auto-recovers uploads once SFTPGo restarts. |
| **Service** | `TestLiveSFTPGoConstantInfluxAndHandleLeak` | Stream 500 files at high velocity (20 files/100ms) under concurrent verification/action workers. | All files are processed, reaped, and process handle counts remain stable without leaking descriptors. |
| **CLI** | `TestMainFlags` | Verify flag parsing and overrides. | All flag combinations and config overrides handled correctly. |
| **CLI** | `TestMain` | Verify main entry point logic. | Logic bridges correctly to internal run function. |
| **CLI** | `TestIsAdmin` | Verify administrative check. | Function returns boolean without panicking. |
| **TestUtils** | `TestMockSFTPClient_Errors` | Verify mock SFTP error simulation. | All methods return configured errors correctly. |
| **TestUtils** | `TestUtils_Directories` | Verify test directory management. | Absolute paths created correctly for test isolation. |
| **TestUtils** | `TestUtils_Env` | Verify environment variable handling. | Temp directory and env vars retrieved correctly. |

## 4. Code Coverage Report

The suite maintains high coverage across all core packages, exceeding the 90% objective. Coverage is calculated using consolidated profiles to ensure no logic branches are missed.

### 4.1 Coverage Statistics (Final)
The suite maintains high coverage across all packages, with a total application coverage of **92.0%**:

| Package | Coverage % |
| :--- | :--- |
| **Total Application** | **92.0%** |
| `cmd/dirpoller` (CLI) | 93.3% |
| `internal/action` | 91.7% |
| `internal/archive` | 93.9% |
| `internal/config` | 94.6% |
| `internal/integrity` | 92.1% |
| `internal/poller` | 91.2% |
| `internal/service` | 90.3% |
| `internal/testutils` | 95.5% |

## 5. Realistic Data Simulation

### 5.1 SFTP High-Fidelity Mock Server
The SFTP action handler is tested using a high-fidelity, in-memory mock SFTP server implemented in `internal/action/sftp_mock_server_test.go`. This server uses `golang.org/x/crypto/ssh`, `github.com/pkg/sftp` and `afero.MemMapFs` to provide a realistic testing environment.

- **Auth Matrix Mode**: Supports User+Pass, User+Key, and User+Pass+Key (MFA).
- **Connection Lifecycle**: Implements full TCP listen, SSH handshake, channel handling, subsystem request, and SFTP initialization.
- **Integration**: Validates real-world authentication, file transfer success, and host key verification.

### 5.2 File System Activity Simulation
- **fsnotify Events**: Real `ReadDirectoryChangesW` (Windows) and `inotify` (Linux) events are used to simulate live activity.
- **Consistency Verification**: Modification of files during the verification interval is simulated to test verifier stability logic.
- **Subfolder detection**: Verification that subfolder creation triggers the expected design behavior.

## 6. How to Run the Tests

### 6.1 Linux Service Testing (Regular User with Sudo)
The Linux service installer test suite (`internal/service/service_linux_test.go`) is designed to be executed by a **regular user**. It does not require the entire test process to be run as root. Instead, it leverages `sudo` internally for specific operations that require elevation:

- **Deployment**: Writing the template to `/etc/systemd/system/`.
- **Management**: Invoking `systemctl` for daemon-reload and enablement.

**Prerequisites**:
1. The user must have `sudo` privileges.
2. The tests use `exec.Command("sudo", ...)` to elevate as needed.

### 6.2 Windows (PowerShell)
Run the entire suite with coverage reporting:
```powershell
# Standard run
go test ./... -v -cover

# Detailed coverage breakdown
go test ./... -coverprofile="$env:TEMP\total.cov"; if ($?) { go tool cover -func="$env:TEMP\total.cov" }
```

### 6.3 Linux (Bash)
Run the entire suite with coverage reporting:
```bash
# Standard run
go test ./... -v -cover

# Detailed coverage breakdown
go test ./... -coverprofile="$TEMP/total.cov" && go tool cover -func="$TEMP/total.cov"
```

### 6.4 Running Integration and Scale Tests

To run the high-volume scale and live SFTP integration tests (which are skipped by default to keep the standard test suite fast), you must set the following environment variables:

- **`TEST_SCALE`**: Set to `"true"` to enable the engine scale performance tests.
- **`SCALE_LIMIT`**: Set to the desired file count limit for the scale test (e.g., `"10000"` or `"1000000"`).
- **`TEST_LIVE_SFTP`**: Set to `"true"` to enable the single-file live SFTPGo integration test.
- **`TEST_LIVE_SFTP_5K`**: Set to `"true"` to enable the 5,000-file realistic production load simulation test.
- **`TEST_LIVE_SFTP_1M`**: Set to `"true"` to enable the 1,000,000 files live SFTPGo integration test.

#### Windows (PowerShell)
```powershell
# Run the 10k scale benchmark
$env:TEST_SCALE="true"
$env:SCALE_LIMIT="10000"
go test -v -run=TestEngineScalePerformance ./internal/service

# Run the 5k live SFTP integration test (simulates 5,000 files production load with strict SLAs)
$env:TEST_LIVE_SFTP_5K="true"
go test -v -run=TestLiveSFTPGo5KIntegration ./internal/service

# Run the 1M live SFTP integration test (requires ~3-4 hours on consumer workstations)
$env:TEST_SCALE="true"
$env:SCALE_LIMIT="10000"
$env:TEST_LIVE_SFTP_1M="true"
go test -v -timeout=4h -run=TestLiveSFTPGo1MIntegration ./internal/service
```

#### Linux (Bash)
```bash
# Run the 10k scale benchmark
TEST_SCALE="true" SCALE_LIMIT="10000" go test -v -run=TestEngineScalePerformance ./internal/service

# Run the 5k live SFTP integration test
TEST_LIVE_SFTP_5K="true" go test -v -run=TestLiveSFTPGo5KIntegration ./internal/service

# Run the 1M live SFTP integration test
TEST_SCALE="true" SCALE_LIMIT="10000" TEST_LIVE_SFTP_1M="true" go test -v -timeout=4h -run=TestLiveSFTPGo1MIntegration ./internal/service
```

## 7. Maintenance and Troubleshooting

### 7.1 Troubleshooting Common Failures
- **Path Length Errors**: On Windows, ensure `TEMP` does not exceed length limits if using deep nesting.
- **Permission Denied**: If running tests that simulate Windows Service installation without mocks, ensure the shell is elevated (though current tests use mocks to avoid this).
- **Hanging Tests**: Usually caused by unbuffered channels in mocks or `engine.Run` not receiving a cancellation signal. Use `context.WithTimeout` in new tests.

### 7.2 Maintenance Guidelines
- **Adding New Features**: Ensure any new external dependency is wrapped in an interface and added to `internal/testutils`.
- **Refactoring**: When refactoring `main` or `service` logic, ensure mock variables in `main.go` are updated and tests still use `defer` for restoration.
- **Coverage Maintenance**: Periodically run the full coverage suite to ensure no package drops below 90%.
- **Safety**: Global variables (`os.Args`, mock functions) must be restored using `defer` to ensure test independence.
