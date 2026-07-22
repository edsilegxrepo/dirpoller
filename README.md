# DirPoller

Efficient Go-based Multi-Platform Directory Poller (Windows Server 2019+, Linux) for automated file monitoring, verification, and secure transfer.

## Overview and Objectives
DirPoller is designed for high-performance directory monitoring in enterprise environments. It provides a robust way to:
1.  **Monitor** directories using multiple polling strategies (Interval, Batch, Event, or Trigger).
2.  **Verify** file integrity before processing using high-performance XXH3-128 hashing or property stability (size/timestamp).
3.  **Execute** automated actions such as high-performance multi-threaded SFTP uploads (with Atomic Upload Protocol) or local script execution.
4.  **Archive** processed files using datestamped folders or consolidated high-efficiency `zstd` compression.

The application can run as a standalone CLI on Windows and Linux, or be installed as a native Windows Service (with full EventLog integration) or a Linux Systemd unit.

## Command Line Arguments
| Argument | Type | Default | Description |
| :--- | :--- | :--- | :--- |
| `-config` | String | (Required) | Full absolute path to the JSON configuration file. |
| `-log` | String | `""` | Enable custom logging with a specific base log name. |
| `-log-retention`| Integer | `0` | Number of days to keep logs (0 = disabled). |
| `-name` | String | `""` | Service/Unit name. **Windows**: custom name (optional). **Linux**: REQUIRED `<unit_name>@<config_name>`. |
| `-install` | Boolean | `false` | Install as a service (Windows Service / Linux systemd unit). |
| `-remove` | Boolean | `false` | Remove a service (Windows Service / Linux systemd unit). |
| `-user` | String | `""` | Service account. Windows: `DOMAIN\User` (optional). Linux: `user:group` (REQUIRED). |
| `-pass` | String | `""` | Password for the service account (**Windows only**). |
| `-debug` | Boolean | `false` | Run in interactive debug mode (console output). |
| `-version` | Boolean | `false` | Print the application version and exit. |

---

## Configuration File Reference

This section provides a comprehensive reference for all configuration directives available in the DirPoller JSON configuration file. Its objective is to serve as a unified lookup for parameter names, their contexts, default values, and any constraints or enforcement rules applied during validation.

| logical configuration block | config name | context | purpose | default value | constraint of context or enforcement of value |
| :--- | :--- | :--- | :--- | :--- | :--- |
| **Global** | `service_name` | String | Root | Custom name for the service instance (**Windows Only**). | `DirPoller` | Used as the Windows Service Name. On Linux, this is ignored; use the `-name` CLI flag instead. |
| **Poll** | `directory` | `poll` | The local path to monitor for new files. | **Required** | Must be an absolute path. Path traversal (`..`) is forbidden. Must exist. |
| **Poll** | `algorithm` | `poll` | Strategy for discovering files. | `interval` | Supported: `interval`, `batch`, `event`, `trigger`. |
| **Poll** | `value` | `poll` | Parameter for the chosen algorithm. | `0` | `interval`: seconds (int). `batch`: file count (int). `trigger`: file pattern (string). |
| **Poll** | `batch_timeout_seconds` | `poll` | Force processing timeout for batch/trigger. | `600` | Applied when algorithm is `batch` or `trigger`. |
| **Poll** | `max_batch_size` | `poll` | Maximum number of files processed in a single batch. | `1000` | Constraint: must be between `100` and `10000` inclusive. |
| **Integrity** | `algorithm` | `integrity` | Method to verify file stability. | `timestamp` | Supported: `hash` (XXH3-128), `timestamp`, `size`, `none` (or `disabled`). |
| **Integrity** | `attempts` | `integrity` | Number of stability checks. | `1` | Must be an integer $\ge 0$. Set to `0` to disable integrity verification. |
| **Integrity** | `interval` | `integrity` | Seconds between stability checks. | `5` | Must be an integer $\ge 0$. Ignored if integrity checking is disabled. |
| **Action** | `type` | `action` | The processing engine to execute. | **Required** | Supported: `sftp`, `script`. |
| **Action** | `concurrent_connections`| `action` | Size of the worker pool for parallel tasks. | `CPU x 2` | Must be a positive integer. |
| **Post-Process** | `action` | `action.post_process`| Lifecycle step after successful action. | `delete` | Supported: `delete`, `move_archive`, `move_compress`. |
| **Post-Process** | `archive_path` | `action.post_process`| Target path for archive/compress actions. | Optional | Required for all post-actions (`move`, `compress` or `delete`) to host the `.staging` directory. Must be absolute. |
| **Post-Process** | `archive_retention`| `action.post_process`| Number of days to retain files in `archive_path`.| `7` | Set to `0` to keep archives forever. |
| **SFTP** | `host` | `action.sftp` | Remote SFTP server hostname/IP. | **Required** | Must be provided if action type is `sftp`. |
| **SFTP** | `port` | `action.sftp` | Remote SSH/SFTP port. | `22` | Standard SSH port. |
| **SFTP** | `username` | `action.sftp` | SSH authentication username. | **Required** | Must be provided if action type is `sftp`. |
| **SFTP** | `encrypted_password` | `action.sftp` | AES-256-GCM encrypted password. | Optional | Required if `ssh_key_path` is not used. Must be Base64 encoded. |
| **SFTP** | `master_key_file` | `action.sftp` | Path to master key file (Linux). | `~/.secretprotector.key`| **Linux Only**. Must have owner-only permissions. |
| **SFTP** | `master_key_env` | `action.sftp` | Name of master key env var (Windows). | `SECRETPROTECTOR_KEY` | **Windows Only**. Should be a user-level environment variable. |
| **SFTP** | `ssh_key_path` | `action.sftp` | Path to private SSH key. | Optional | Must be an absolute path. Path traversal forbidden. |
| **SFTP** | `ssh_key_passphrase` | `action.sftp` | Passphrase for the private SSH key. | Optional | Used to decrypt the private key if it's encrypted. |
| **SFTP** | `host_key` | `action.sftp` | Public host key. | Optional | Used for server identity verification. Supports: <br> - **RSA**: `ssh-rsa AAAAB3...` <br> - **ECDSA**: `ecdsa-sha2-nistp256 AAAAE2...` <br> - **ED25519**: `ssh-ed25519 AAAAC3...` <br> - **Raw Base64**: `AAAAC3...` |
| **SFTP** | `remote_path` | `action.sftp` | Target directory on the SFTP server. | **Required** | Must be an absolute path (starts with `/`). Path traversal forbidden. |
| **SFTP** | `disable_atomic_commit`| `action.sftp` | Bypass temp staging, remote rename, and stat. | `false` | When true, uploads directly to final destination. Optimizes latency on slow networks. |
| **Script** | `path` | `action.script` | Path to the local script or binary. | **Required** | Must be an absolute path. Path traversal forbidden. Must exist. |
| **Script** | `timeout_seconds` | `action.script` | Max execution time per file. | `60` | Script is killed if it exceeds this duration. |
| **Logging** | `log_name` | `logging[]` | Base absolute path for log files. | **Required** | Must be an absolute path. |
| **Logging** | `log_retention` | `logging[]` | Number of days to retain logs. | `0` | `0` means retention is disabled (logs kept indefinitely). |

---

## Service Management (Windows & Linux)
DirPoller provides native integration with the Windows Service Control Manager (SCM). On Linux, it can be run as a systemd unit using the provided service file.

### Platform Differences
- **Windows**: Supports automated installation/removal via `-install` and `-remove`. Uses the Service Control Manager and logs to the **Windows Event Log**. Requires Administrator privileges.
- **Linux**: Supports automated installation/removal via `-install` and `-remove` (using `sudo`). Uses **Systemd Parameterized Units** (`unit@config.service`) and logs to `syslog` or `journald`. Requires root privileges.

### Installation
**1. Windows (PowerShell Administrator):**
The `-install` command handles service creation and system log source registration.

- **Using LocalSystem (Default):**
```powershell
.\dirpoller.exe -install -config "C:\Program Files\DirPoller\config.json"
```

- **Using a Service Account:**
```powershell
.\dirpoller.exe -install -config "C:\Program Files\DirPoller\config.json" -user "MYDOMAIN\svc_poller" -pass "SecurePass123"
```

**2. Linux (Sudo):**
The `-install` command handles template deployment to `/etc/systemd/system/` and instance enablement via `systemctl enable`. It does not start the service. File `dirpoller.service` is provided in the repository as an indication of the unit specs.

- **Standard Installation:**
```bash
sudo ./dirpoller -install -name "dirpoller@config1" -config "/etc/dirpoller/config1.json" -user "polleruser:users"
```
*Note: This creates `/etc/systemd/system/dirpoller@.service` and enables the `dirpoller@config1` instance. Configuration file `/etc/dirpoller/config1.json` must be created prior. Service can then be started via `systemctl start dirpoller@config1`.*

### Removal
**Windows:**
The `-remove` command gracefully stops the service and removes it from the system. It deletes the service from the SCM and unregisters the EventLog source.
```powershell
.\dirpoller.exe -remove -config "C:\Program Files\DirPoller\config.json"
```

**Linux:**
The `-remove` command stops the instance via `systemctl stop`, disables it via `systemctl disable`, and removes the template unit file from `/etc/systemd/system/`.
```bash
sudo ./dirpoller -remove -name "dirpoller@config1"
```

### Service Control and Monitoring
Once installed, the service (named `DirPoller` or a custom name)) can be managed using standard OS commands or GUI.

#### Windows (PowerShell)
- **Start**: `Start-Service DirPoller` or `net start DirPoller`
- **Stop**: `Stop-Service DirPoller` or `net stop DirPoller`
- **Restart**: `Restart-Service DirPoller`
- **Status**: `Get-Service DirPoller` or `sc.exe query DirPoller`


#### Linux (Systemd)
- **Start**: `sudo systemctl start dirpoller@config1`
- **Stop**: `sudo systemctl stop dirpoller@config1`
- **Restart**: `sudo systemctl restart dirpoller@config1`
- **Status**: `sudo systemctl status dirpoller@config1`

*Note: Replace `dirpoller@config1` with your specific `<unit_name>@<config_name>`.*

### Verifying Logs
DirPoller logs all critical events (startup, errors, processing summaries) to the native system logger.

- **Windows**: 
  1. Open **Event Viewer** (`eventvwr.msc`).
  2. Navigate to `Windows Logs` -> `Application`.
  3. Look for the Source: `DirPoller`.
- **Linux**: Use `journalctl -u dirpoller@config1` or check `/var/log/syslog`.

#### Troubleshooting
If the service fails to start:
1. Check the Event Log (Windows) or Journal (Linux) for specific error messages.
2. Ensure the configuration file path provided during installation is an **absolute path** and is readable by the service account.
3. Run `.\dirpoller.exe -debug -config ...` (Windows) or `./dirpoller -debug -config ...` (Linux) to see real-time output in the console.

## Configuration JSON File
DirPoller is entirely driven by a structured JSON configuration. For reliability—especially when running as a system service—all file and directory paths **must be absolute**.

### 1. Global Settings
| Property | Type | Default | Logic / Purpose |
| :--- | :--- | :--- | :--- |
| `service_name` | String | `DirPoller` | Custom name for the service instance (**Windows ONLY**). Ignored on Linux. |

### 2. Polling Strategy (`poll`)
Determines how the application discovers files in the target directory.

| Property | Type | Default | Logic / Purpose |
| :--- | :--- | :--- | :--- |
| `directory` | String | **Required** | The absolute local path to watch. Must exist and be accessible. |
| `algorithm` | String | `interval` | The detection method: `interval`, `batch`, `event`, or `trigger`. |
| `value` | Mix | `0` | **Context-sensitive**: In `interval` mode, this is seconds. In `batch` mode, this is file count. In `trigger` mode, this is a file pattern (string). |
| `batch_timeout_seconds` | Integer | `600` | Used in `batch` and `trigger` modes. Forces processing if the threshold or trigger is not met within this period. |
| `max_batch_size` | Integer | `1000` | Limits how many files are processed in a single batch (Must be between `100` and `10000`). |

**Algorithm Details:**
*   **`interval`**: Performs a full directory scan at fixed time steps. Reliable for all storage types and OS platforms.
*   **`batch`**: Collects files as they arrive but waits until a specific volume is reached before executing actions.
*   **`event`**: Uses real-time OS APIs (`ReadDirectoryChangesW` on Windows, `inotify` on Linux) for low-overhead detection. Best for high-traffic local disks.
*   **`trigger`**: Waits for a specific "trigger file" (exact name or wildcard) to appear before processing all pending files in the directory.

---

### 2. Integrity Verification (`integrity`)
A safety gate that ensures files are fully written and closed by the source process before DirPoller touches them.

| Property | Type | Default | Logic / Purpose |
| :--- | :--- | :--- | :--- |
| `algorithm` | String | `timestamp` | The property to monitor for stability: `hash`, `timestamp`, `size`, or `none` (`disabled`). |
| `attempts` | Integer | `1` | Number of consecutive checks where the property must remain identical for the file to be considered "stable". Set to `0` to disable integrity checks. |
| `interval` | Integer | `5` | Seconds to wait between each verification attempt (ignored if `algorithm` is `none` or `attempts` is `0`). |

**Verification Flow:**
1.  **Short-Circuit Check (Disabled Mode)**:
    - If `algorithm` is set to `"none"` (or `"disabled"`), or `attempts` is set to `0`, integrity verification is **bypassed completely**.
    - Skips OS lock checks, `os.Stat` sampling, and interval delays, returning `verified = true` immediately.
2.  **Lock Check**: 
    - **Windows**: Attempts to open the file with exclusive access (`FILE_SHARE_NONE`) using native `CreateFile`. If Windows reports a sharing violation, the file is skipped.
    - **Linux**: Uses `flock` (`LOCK_EX|LOCK_NB`) to detect active writes or locks.
3.  **Stability Check**: Once unlocked/available, the chosen `algorithm` is used. If the property (e.g., file size) changes between any of the `attempts`, the counter resets.

**Disabling Integrity Verification Example:**
```json
"integrity": {
    "algorithm": "none",
    "attempts": 0,
    "interval": 0
}
```

---

### 3. Action Handlers (`action`)
Defines the primary task to perform on verified files.

| Property | Type | Default | Logic / Purpose |
| :--- | :--- | :--- | :--- |
| `type` | String | **Required** | The processing engine to use: `sftp` or `script`. |
| `concurrent_connections` | Integer | CPU x 2 | The size of the worker pool. Limits how many files are processed in parallel across all batches. |

#### SFTP Handler (`action.sftp`)
| Property | Type | Default | Logic / Purpose |
| :--- | :--- | :--- | :--- |
| `host` / `port` | Mix | Req / `22` | Connection details for the remote server. |
| `username` | String | **Required** | SSH/SFTP username. |
| `encrypted_password` | String | Optional | AES-256-GCM encrypted password (Base64). Mandatory if not using SSH key. |
| `master_key_file` | String | Optional | **Linux ONLY**: Path to the master key file (default: `~/.secretprotector.key`). |
| `master_key_env` | String | Optional | **Windows ONLY**: Name of the user-level environment variable (default: `SECRETPROTECTOR_KEY`). |
| `ssh_key_path` | String | Optional | Absolute path to a private SSH key (OpenSSH format). |
| `ssh_key_passphrase` | String | Optional | The passphrase required to decrypt the private key file (if encrypted). |
| `host_key` | String | Optional | Public host key for server verification. Supports: <br> - **RSA**: `ssh-rsa AAAAB3...` <br> - **ECDSA**: `ecdsa-sha2-nistp256 AAAAE2...` <br> - **ED25519**: `ssh-ed25519 AAAAC3...` <br> - **Raw Base64**: `AAAAC3...` |
| `remote_path` | String | **Required** | The target directory on the SFTP server. |
| `disable_atomic_commit` | Boolean | `false` | When `true`, uploads files directly to the final path, skipping remote Rename and size check. |

> [!NOTE]
> **High-Latency Link Performance Tuning**:
> When transferring files over high-latency networks (e.g. WAN connections with 100ms+ round-trip times), the default Atomic Commit Protocol (Stage -> Write -> Close -> Rename -> Stat) creates a bottleneck by executing 5 sequential RTTs per file. 
> To maximize throughput over such links:
> 1. Set `"disable_atomic_commit": true` to skip remote Rename and Stat operations, reducing round-trips from 5 to 3.
> 2. Increase `"concurrent_connections"` in the `action` config to `32` or `64`. Since DirPoller uses an optimized multi-socket connection pool, scaling concurrent threads hiding latency is safe and highly effective.

#### Script Handler (`action.script`)
| Property | Type | Default | Logic / Purpose |
| :--- | :--- | :--- | :--- |
| `path` | String | **Required** | Absolute path to the script or executable. Supports `.exe`, `.bat`, `.cmd`, `.ps1` (Windows) or any executable script/binary (Linux). |
| `timeout_seconds` | Integer | `60` | Maximum time allowed for the script to run per file before being killed. |

---

### 4. Post-Action Lifecycle (`action.post_process`)
Determines what happens to the local file after the Action Handler confirms a successful operation.

| Property | Type | Default | Logic / Purpose |
| :--- | :--- | :--- | :--- |
| `action` | String | `delete` | Local lifecycle step: `delete`, `move_archive`, or `move_compress`. |
| `archive_path` | String | Optional | Required for all post-actions (`move`, `compress` or `delete`) to host the `.staging` directory. |

**Action Details:**
*   **`delete`**: The file is permanently removed from the local `directory`.
*   **`move_archive`**: The file is moved to a timestamped subfolder (`YYYYMMDD-HHMMSS`) under the `archive_path`.
*   **`move_compress`**: The file is added to a high-performance, multi-threaded `zstd` archive in the `archive_path`, then the original is deleted.

---

### 5. Custom Logging (`logging`)
DirPoller implements a dual-track logging system to separate operational events from data processing results.

| Property | Type | Default | Logic / Purpose |
| :--- | :--- | :--- | :--- |
| `log_name` | String | **Required** | Base name for the log file (e.g., `C:\Logs\poller.log` or `/var/log/dirpoller.log`). |
| `log_retention` | Integer | `0` | Number of days to keep logs. If `0`, retention is disabled. |

#### Log Separation Concept
1.  **System Process Log (Daily)**: Records process-level events such as service start/stop, OS resource issues, and critical system errors (e.g., SFTP connection failures).
    - **System Integration**: These events are always logged to the **Windows Application Event Log** (Source: `DirPoller`) or **Linux Syslog/Journald**. If `-log` is specified, they are also mirrored to the daily process log file.
    - **Naming**: `base_process_YYYYMMDD.log`
    - **Format**: `date stamp|message`
2.  **Activity Log (Daily)**: Detailed report of files discovered, verified, and processed across all execution cycles during the day. Includes individual file integrity or action errors.
    - **System Integration**: Activity logs are **NOT** logged to the system event log. They are file-system ONLY (requires `-log` or JSON config). Multiple execution cycles in the same day are appended to the same daily file.
    - **Naming**: `base_activity_YYYYMMDD.log`
    - **Format**: Structured sections for Status, Successes, and Errors. Includes XXH3-128 hashes for all processed files.

#### Example: System Process Log
`C:\Logs\poller_process_20260310.log`
```text
2026-03-10 08:00:00|Engine starting...
2026-03-10 08:05:22|ERROR: Poller error: failed to add directory to watcher
2026-03-10 17:30:00|Engine stopping (context canceled)...
```

#### Example: Activity Log
`C:\Logs\poller_activity_20260310.log`
```text
# Status
2026-03-10 08:00:05|total number of files picked up: 3
2026-03-10 08:00:05|number of files processed OK: 2
2026-03-10 08:00:05|number of files in error: 1
------
# List of files processed successfully
2026-03-10 08:00:05|C:\Data\In\file1.txt|1024|a1b2c3d4e5f6g7h8a1b2c3d4e5f6g7h8
2026-03-10 08:00:05|C:\Data\In\file2.txt|2048|b2c3d4e5f6g7h8i9b2c3d4e5f6g7h8i9
------
# List of files in error
2026-03-10 08:00:05|C:\Data\In\file3.txt in error|512|c3d4e5f6g7h8i9j0c3d4e5f6g7h8i9j0|action execution failed
```

**Auto-Purge**: Log retention logic executes **once per day** (at the start of the first execution after 00:00:00). Both process and activity logs older than the `log_retention` period are automatically deleted to minimize filesystem overhead.

### 6. Password Encryption (`action.sftp`)
DirPoller implements mandatory password encryption for SFTP to ensure sensitive credentials are never stored in plaintext.

#### Objective
Secure SFTP credentials at rest and in memory. Plaintext passwords are strictly forbidden in the JSON configuration; only AES-256-GCM encrypted ciphertexts are accepted.

#### 1. Generate the Master Key
Use the `secretprotector` CLI utility to generate a cryptographically secure 32-byte master key (64-character hex string).
```powershell
secretprotector -generate
# Output: a1b2c3d4e5f6... (64 chars)
```

#### 2. Store the Key Securely
The application performs platform-specific security checks and follows strict location rules:

##### **Windows (Environment Variable)**
The master key **must** be stored in a **user-level** environment variable for the account running the service.
1.  Open **PowerShell** as the user who will run the service (or use `setx` for permanent user-level storage). The variable name is configurable via `master_key_env`.
2.  Set the variable (default name: `SECRETPROTECTOR_KEY`):
    ```powershell
    # Permanent user-level assignment
    setx SECRETPROTECTOR_KEY "YOUR_64_CHAR_HEX_KEY"
    ```
3.  **Note**: If running as `LocalSystem`, you must set this as a **System** environment variable via System Properties > Environment Variables, or by using the `setx SECRETPROTECTOR_KEY "YOUR_64_CHAR_HEX_KEY" /M` command.

##### **Linux (Key File)**
The master key **must** be stored in a file with owner-only permissions. The master key file name/location is configurable via `master_key_file`.
1.  Create the key file (default: `${HOME}/.secretprotector.key`):
    ```bash
    echo "YOUR_64_CHAR_HEX_KEY" > ~/.secretprotector.key
    ```
2.  Restrict permissions:
    ```bash
    chmod 0400 ~/.secretprotector.key
    ```

#### 3. Generate the Encrypted Password
Once the master key is set in your environment (Windows) or file (Linux), use `secretprotector` to encrypt your plaintext SFTP password.

**Windows:**
```powershell
# Ensure the variable is available in the current session
$env:SECRETPROTECTOR_KEY = "YOUR_64_CHAR_HEX_KEY"
.\secretprotector.exe -encrypt "YourPlaintextPassword"
# Output: BASE64_ENCRYPTED_STRING...
```

**Linux:**
```bash
./secretprotector -key-file "~/.secretprotector.key" -encrypt "YourPlaintextPassword"
# Output: BASE64_ENCRYPTED_STRING...
```

#### 4. Update the Configuration
Insert the `BASE64_ENCRYPTED_STRING` into your `config.json` under `encrypted_password`.

**Example:**
```json
{
  "action": {
    "type": "sftp",
    "sftp": {
      "host": "sftp.example.com",
      "username": "svc_poller",
      "encrypted_password": "BASE64_ENCRYPTED_STRING...",
      "master_key_env": "SECRETPROTECTOR_KEY"
    }
  }
}
```

#### 5. Resolution Logic
The engine resolves the key based on the operating system:
- **Windows**: Resolves **ONLY** from the user-level environment variable specified in `master_key_env` (or the default). Key files 
are ignored.
- **Linux**: Resolves **ONLY** from the path specified in `master_key_file` (or the default). Environment variables are 
ignored.

---

### 7. Multi-Directory Support (Concurrent Services)
DirPoller supports running multiple instances as separate system services. Each instance must have a unique `service_name` and a unique `poll.directory`.

#### Service Name Precedence (Windows ONLY)
The service name on Windows is determined using the following priority:
1.  **CLI Flag (`-name`)**: Overrides everything else if provided.
2.  **Config File (`service_name`)**: Used if the CLI flag is omitted.
3.  **Default**: Defaults to `"DirPoller"` if not specified in either.

**Note**: On Linux, the service name is strictly managed via the `-name` CLI flag in `unit@instance` format. The `service_name` config directive is ignored.

#### Installation Example (Windows)
To install two separate pollers monitoring different directories:

**Instance 1 (Direct to SFTP):**
```powershell
.\dirpoller.exe -install -name "Poller_Finance" -config "C:\Configs\finance.json"
```

**Instance 2 (Local Script Processing):**
```powershell
.\dirpoller.exe -install -name "Poller_HR" -config "C:\Configs\hr.json"
```

#### Management Example (Windows)
You can then manage these services independently using their assigned names:
```powershell
# Start the Finance poller
Start-Service Poller_Finance

# Check status of the HR poller
Get-Service Poller_HR

# Remove the Finance poller
.\dirpoller.exe -remove -name "Poller_Finance" -config "C:\Configs\finance.json"

#### Installation Example (Linux)
To install two separate pollers monitoring different directories using the same unit template:

**Instance 1 (Finance):**
```bash
sudo ./dirpoller -install -name "dirpoller@finance" -config "/etc/dirpoller/finance.json" -user "financeuser:users"
```

**Instance 2 (HR):**
```bash
sudo ./dirpoller -install -name "dirpoller@hr" -config "/etc/dirpoller/hr.json" -user "hruser:users"
```

---

### Full Configuration Example (Windows - SFTP)
```json
{
  "service_name": "MyCustomPoller",
  "poll": {
    "directory": "C:\\Data\\Incoming",
    "algorithm": "event",
    "batch_timeout_seconds": 600
  },
  "integrity": {
    "algorithm": "hash",
    "attempts": 3,
    "interval": 2
  },
  "action": {
    "type": "sftp",
    "concurrent_connections": 4,
    "post_process": {
      "action": "move_compress",
      "archive_path": "C:\\Data\\Archive",
      "archive_retention": 3
    },
    "sftp": {
      "host": "sftp.internal.net",
      "port": 22,
      "username": "svc_poller",
      "encrypted_password": "BASE64_ENCRYPTED_STRING...",
      "master_key_env": "SECRETPROTECTOR_KEY",
      "ssh_key_path": "C:\\ProgramData\\DirPoller\\keys\\id_ed25519",
      "ssh_key_passphrase": "optional-passphrase",
      "host_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINfFSlHZATKjPp9Vwg4l9Ecft2rYqObUItGg1YaYSVWH",
      "remote_path": "/incoming/raw",
      "disable_atomic_commit": true
    }
  },
  "logging": [
    {
      "log_name": "C:\\Logs\\poller.log",
      "log_retention": 7
    }
  ]
}
```

### Full Configuration Example (Linux - SFTP)
```json
{
  "service_name": "dirpoller-finance",
  "poll": {
    "directory": "/opt/dirpoller/incoming",
    "algorithm": "interval",
    "value": 60
  },
  "integrity": {
    "algorithm": "timestamp",
    "attempts": 5,
    "interval": 10
  },
  "action": {
    "type": "sftp",
    "concurrent_connections": 8,
    "post_process": {
      "action": "move_archive",
      "archive_path": "/opt/dirpoller/archive",
      "archive_retention": 14
    },
    "sftp": {
      "host": "sftp.example.com",
      "port": 2222,
      "username": "poller_user",
      "encrypted_password": "BASE64_ENCRYPTED_STRING...",
      "master_key_file": "/home/dirpoller/.secretprotector.key",
      "ssh_key_path": "/home/dirpoller/.ssh/id_rsa",
      "ssh_key_passphrase": "secure-passphrase",
      "host_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINfFSlHZATKjPp9Vwg4l9Ecft2rYqObUItGg1YaYSVWH",
      "remote_path": "/remote/upload",
      "disable_atomic_commit": true
    }
  },
  "logging": [
    {
      "log_name": "/var/log/dirpoller.log",
      "log_retention": 30
    }
  ]
}
```

### Full Configuration Example (Windows - Script)
```json
{
  "service_name": "ScriptPollerWin",
  "poll": {
    "directory": "C:\\Data\\Manual",
    "algorithm": "trigger",
    "value": "ready.txt",
    "batch_timeout_seconds": 300
  },
  "integrity": {
    "algorithm": "size",
    "attempts": 2,
    "interval": 1
  },
  "action": {
    "type": "script",
    "concurrent_connections": 2,
    "post_process": {
      "action": "delete"
    },
    "script": {
      "path": "C:\\Scripts\\process_batch.ps1",
      "timeout_seconds": 120
    }
  }
}
```

### Full Configuration Example (Linux - Script)
```json
{
  "service_name": "script-poller-linux",
  "poll": {
    "directory": "/tmp/poller/in",
    "algorithm": "batch",
    "value": 10,
    "batch_timeout_seconds": 120
  },
  "integrity": {
    "algorithm": "size",
    "attempts": 3,
    "interval": 2
  },
  "action": {
    "type": "script",
    "concurrent_connections": 4,
    "post_process": {
      "action": "move_archive",
      "archive_path": "/tmp/poller/done"
    },
    "script": {
      "path": "/usr/local/bin/process_files.sh",
      "timeout_seconds": 30
    }
  }
}
```

## Build
To build production-ready binaries, use the following templates. This ensures statically linked, position-independent executables (PIE) with debug symbols stripped.

### Windows Build (PowerShell)
```powershell
# Environment Setup
$env:GOPROXY="https://proxy.golang.org,direct"
$env:CGO_ENABLED="0"

# Production Build Command
$version = Get-Content version.txt
if (-not (Test-Path ./bin)) { New-Item -ItemType Directory -Path ./bin }
go build -v `
    -buildvcs=false `
    -trimpath `
    -buildmode=pie `
    -ldflags "-s -w -X main.version=$version-$(git rev-parse --short HEAD)" `
    -o ./bin/dirpoller.exe `
    ./cmd/dirpoller

go build -v -trimpath -buildmode=pie -ldflags "-s -w -X main.version=$version" -o ./bin/dirpoller.exe ./cmd/dirpoller
```

### Linux Build (Bash)
```bash
export GOPROXY="https://proxy.golang.org,direct"
export CGO_ENABLED=0
go build -v -buildvcs=false -trimpath -buildmode=pie \
  -ldflags "-s -w -X main.version=$(cat version.txt)" -o ./bin/ ./cmd/dirpoller
```

### Build Parameters:
-   **GOPROXY**: Uses the official Go proxy with direct fallback for dependency resolution.
-   **CGO_ENABLED=0**: Ensures a fully static binary with no external C library dependencies.
-   **-buildvcs=false**: Suppresses VCS information stamping for reproducible build environments.
-   **-trimpath**: Removes local absolute file paths from the binary (improves security/privacy).
-   **-buildmode=pie**: Generates a Position Independent Executable for enhanced exploit mitigation.
-   **-ldflags "-s -w ..."**: 
    - `-s -w`: Strips the symbol table and DWARF debug information (reduces binary size).
    - `-X main.version`: Injects the specific version into the application.

---

## Examples

### 1. Interactive CLI Mode
Run DirPoller directly in your terminal to monitor a directory.

**Windows:**
```powershell
.\dirpoller.exe -config "C:\Configs\prod_config.json"
```

**Linux:**
```bash
/usr/local/bin/dirpoller -config "/etc/dirpoller/prod_config.json"
```

### 2. Troubleshooting with Debug Mode
Use the `-debug` flag to enable verbose logging. This is highly recommended when testing new configurations or SFTP connectivity.

### 3. Version Verification
Check the installed version and build hash.
```powershell
dirpoller -version
```

### 4. Service Installation (Service Account)

**Windows:**
Install as a background service on Windows using a dedicated service account (administrative account required).
```powershell
.\dirpoller.exe -install -config "C:\DirPoller\config.json" -user "CORP\svc_poller" -pass "P@ssword123"
```

**Linux:**
The installer uses the `dirpoller@.service` parameterized units (root privileges required).
  - **Unit Name**: Configured via the `-name <unit_name>@<config_name>` flag.
  - **Config Mapping**: The `%i` specifier in the unit file maps to `<config_name>`, pointing to `/etc/dirpoller/<config_name>.json`.

```bash
dirpoller -install -name dirpoller@site-a -user dirpoller:apps
```
References `dirpoller@site-a.service` and uses `/etc/dirpoller/site-a.json` (as specified in the unit, path is manually configurable there). User and group can be specified via the `-user user:group` flag


### 5. Service Removal
Stop and uninstall the service from the system.

**Windows:**
```powershell
.\dirpoller.exe -remove -config "C:\DirPoller\config.json"
```

**Linux:**
```bash
sudo ./dirpoller -remove -config "/etc/dirpoller/config.json"
```

### 6. Configuration with Logging
Run DirPoller with custom logging and 14-day retention.

**Windows:**
```powershell
.\dirpoller.exe -config "C:\Configs\prod_config.json" -log "C:\Logs\prod_poller.log" -log-retention 14
```

**Linux:**
```bash
/usr/local/bindirpoller -config "/etc/dirpoller/config.json -log "/var/log/dirpoller/poller.log" -log-retention 30
```

### 7. SSH Host Key Formats (config.json)
The `host_key` field supports four formats. Specifying the full OpenSSH format is recommended as it automatically restricts the handshake to the matching algorithm.

**1. ED25519 (Recommended)**
```json
"host_key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINfFSlHZATKjPp9Vwg4l9Ecft2rYqObUItGg1YaYSVWH"
```

**2. RSA**
```json
"host_key": "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDq2NqQIu+oX6m03qqY7x8pMGF6sKyTdxgNMkgG4Ho3A..."
```

**3. ECDSA**
```json
"host_key": "ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBEcMMxz2+v6ERnGpHlcAK3bmgZqFKr+hs7nyQMpeS6RFWcZC0XalmfFpN5jxeQ2f7Xf0mRTLmAi1eemSvnrcShk="
```

**4. Raw Base64 (Legacy/Key-only)**
```json
"host_key": "AAAAC3NzaC1lZDI1NTE5AAAAINfFSlHZATKjPp9Vwg4l9Ecft2rYqObUItGg1YaYSVWH"
```
*Note: When using Raw Base64, the engine will attempt to detect the key type, but providing the prefix (e.g., `ssh-ed25519`) is safer for multi-algorithm servers.*

## Unit Tests
DirPoller includes a comprehensive unit testing suite designed for high reliability and realistic Windows behavior.

### Objectives
- **Realistic Simulation**: Tests use the Windows `%TEMP%` / Linux `$TEMP` directory for real filesystem interactions.
- **Isolated Testing**: All external dependencies (SFTP, Windows Service Manager) are refactored into interfaces and fully mocked, allowing for complete logic verification without specialized infrastructure or administrator rights.
- **Race Condition Prevention**: Test subdirectories are uniquely isolated per-test to support parallel execution.

### Running Tests
To run the entire test suite and verify code coverage:
```powershell
go test ./internal/... -v -cover
```

For more detailed information on test categories and specific test cases, see the [TESTING.md](./TESTING.md) file.
