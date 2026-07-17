// Package config provides the configuration schema and validation logic for DirPoller.
//
// Objective:
// Defines the complete configuration structure used to orchestrate the polling,
// integrity verification, action handling, and logging components. It ensures
// that all operational parameters are type-safe and within security boundaries.
//
// Data Flow:
// 1. Loading: Reads JSON from disk via LoadConfig.
// 2. Defaults: Injects sensible defaults for optional fields (setDefaults).
// 3. Validation: Enforces mandatory fields and security constraints (validate).
// 4. Consumption: The resulting Config struct is passed to the Engine and its sub-components.
package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/crypto/ssh"
)

// PollAlgorithm defines the supported directory scanning strategies.
type PollAlgorithm string

const (
	// PollInterval triggers a scan every N seconds.
	PollInterval PollAlgorithm = "interval"
	// PollBatch triggers a scan when a file count threshold is reached.
	PollBatch PollAlgorithm = "batch"
	// PollEvent uses OS-native events (ReadDirectoryChangesW) for real-time detection.
	PollEvent PollAlgorithm = "event"
	// PollTrigger waits for a specific trigger file to appear.
	PollTrigger PollAlgorithm = "trigger"
)

// IntegrityAlgorithm defines the methods used to ensure a file is fully written and consistent.
type IntegrityAlgorithm string

const (
	// IntegrityHash verifies file consistency using XXH3-128.
	IntegrityHash IntegrityAlgorithm = "hash"
	// IntegrityTimestamp verifies consistency by checking the Last Modified timestamp.
	IntegrityTimestamp IntegrityAlgorithm = "timestamp"
	// IntegritySize verifies consistency by checking the file size.
	IntegritySize IntegrityAlgorithm = "size"
)

// ActionType defines the primary operation to perform on discovered and verified files.
type ActionType string

const (
	// ActionSFTP performs a multi-threaded upload to a remote server.
	ActionSFTP ActionType = "sftp"
	// ActionScript executes a local script with the file path as an argument.
	ActionScript ActionType = "script"
)

// PostAction defines what happens to the local file after a successful Action.
type PostAction string

const (
	// PostActionDelete removes the file immediately.
	PostActionDelete PostAction = "delete"
	// PostActionMoveArchive moves the file to a datestamped subfolder.
	PostActionMoveArchive PostAction = "move_archive"
	// PostActionMoveCompress adds the file to a consolidated multi-threaded zstd archive.
	PostActionMoveCompress PostAction = "move_compress"
)

// Config represents the root configuration structure for the DirPoller application.
//
// Objective: Define the complete schema for application behavior, covering
// polling, integrity, actions, and logging.
//
// Data Flow:
// 1. Initialized by LoadConfig from a JSON source.
// 2. Passed to the Engine during bootstrap.
// 3. Used by individual components (Poller, Verifier, ActionHandler) to guide their behavior.
type Config struct {
	ServiceName   string          `json:"service_name,omitempty"` // Windows only: Custom name for the service instance. In Linux, the service name is strictly managed via CLI.
	Poll          PollConfig      `json:"poll"`
	Integrity     IntegrityConfig `json:"integrity"`
	Action        ActionConfig    `json:"action"`
	Logging       []LoggingConfig `json:"logging,omitempty"`
	GCPercent     int             `json:"gc_percent,omitempty"`
	MemoryLimitMB int             `json:"memory_limit_mb,omitempty"`
}

// LoggingConfig contains parameters for the custom logging facility.
type LoggingConfig struct {
	LogName      string `json:"log_name"`
	LogRetention int    `json:"log_retention"` // in days
}

// PollConfig contains parameters for the directory scanning engine.
type PollConfig struct {
	Directory              string        `json:"directory"`
	Algorithm              PollAlgorithm `json:"algorithm"`
	Value                  interface{}   `json:"value"` // Interval/Batch count (int) or Trigger pattern (string)
	BatchTimeoutSeconds    int           `json:"batch_timeout_seconds"`
	MaxBatchSize           int           `json:"max_batch_size,omitempty"`
	MaxVerificationWorkers int           `json:"max_verification_workers,omitempty"`
}

// IntegrityConfig contains parameters for the file verification logic.
type IntegrityConfig struct {
	Algorithm            IntegrityAlgorithm `json:"algorithm"`
	VerificationAttempts int                `json:"attempts"`
	VerificationInterval int                `json:"interval"`
}

// ActionConfig contains parameters for the upload or execution phase.
type ActionConfig struct {
	Type                  ActionType        `json:"type"`
	ConcurrentConnections int               `json:"concurrent_connections"`
	PostProcess           PostProcessConfig `json:"post_process"`
	SFTP                  SFTPConfig        `json:"sftp,omitempty"`
	Script                ScriptConfig      `json:"script,omitempty"`
}

// PostProcessConfig contains parameters for the file lifecycle after action execution.
type PostProcessConfig struct {
	Action           PostAction `json:"action"`
	ArchivePath      string     `json:"archive_path,omitempty"`
	ArchiveRetention *int       `json:"archive_retention,omitempty"`
}

// SFTPConfig contains credentials and path information for SFTP transfers.
//
// Security Note:
// The 'Password' field is intentionally excluded from JSON marshaling ('-')
// and is only populated in memory during active execution after decryption.
type SFTPConfig struct {
	Host                string `json:"host"`
	Port                int    `json:"port"`
	Username            string `json:"username"`
	Password            string `json:"-"`                            // Plaintext password NOT stored in JSON
	EncryptedPassword   string `json:"encrypted_password,omitempty"` // #nosec G117 - Base64 encoded encrypted password
	MasterKeyFile       string `json:"master_key_file"`              // Linux only: defaults to ${HOME}/.secretprotector.key
	MasterKeyEnv        string `json:"master_key_env"`               // Windows only: defaults to SECRETPROTECTOR_KEY
	SSHKeyPath          string `json:"ssh_key_path,omitempty"`       // Path to private key
	SSHKeyPassphrase    string `json:"ssh_key_passphrase,omitempty"` // #nosec G117 - Passphrase to decrypt the private key
	HostKey             string `json:"host_key,omitempty"`           // Base64 encoded public host key
	RemotePath          string `json:"remote_path"`
	DisableAtomicCommit bool   `json:"disable_atomic_commit,omitempty"`
}

// ScriptConfig contains parameters for executing external logic.
type ScriptConfig struct {
	Path           string `json:"path"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// LoadConfig reads, unmarshals, and validates the JSON configuration file.
//
// Objective: Transform a raw JSON configuration file into a validated,
// default-populated Config struct used throughout the application.
//
// Data Flow:
// 1. Read: Loads raw bytes from the specified file path.
// 2. Unmarshal: Parses JSON into the Config structure.
// 3. Defaults: Applies sensible default values for missing optional fields.
// 4. Validation: Enforces mandatory fields and security constraints (e.g., script paths).
func LoadConfig(path string) (*Config, []byte, error) {
	data, err := os.ReadFile(filepath.Clean(path)) // #nosec G304
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, nil, fmt.Errorf("failed to parse config JSON: %w", err)
	}

	setDefaults(&cfg)

	if err := validate(&cfg); err != nil {
		return nil, nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return &cfg, data, nil
}

// setDefaults applies sensible default values for missing optional fields.
//
// Logic:
//   - ServiceName: Defaults to "DirPoller" (Windows only). In Linux, it's NOT defaulted from config.
//   - Poll Algorithm: Defaults to "interval".
//
// - Batch/Trigger Timeout: Defaults to 600s if not specified.
// - Integrity Algorithm: Defaults to "timestamp" with 3 attempts at 5s intervals.
// - Concurrency: Defaults to 2x CPU count.
// - SFTP Port: Defaults to 22.
func setDefaults(cfg *Config) {
	if runtime.GOOS == "windows" && cfg.ServiceName == "" {
		cfg.ServiceName = "DirPoller"
	}
	if cfg.Poll.Algorithm == "" {
		cfg.Poll.Algorithm = PollInterval
	}
	if (cfg.Poll.Algorithm == PollBatch || cfg.Poll.Algorithm == PollTrigger) && cfg.Poll.BatchTimeoutSeconds == 0 {
		cfg.Poll.BatchTimeoutSeconds = 600 // 10 minutes default
	}
	if cfg.Poll.MaxBatchSize == 0 {
		cfg.Poll.MaxBatchSize = 1000 // 1k default limit
	}
	if cfg.GCPercent == 0 {
		cfg.GCPercent = 100 // default Go GC percent
	}
	if cfg.Poll.MaxVerificationWorkers == 0 {
		cfg.Poll.MaxVerificationWorkers = runtime.NumCPU() * 2
		if cfg.Poll.MaxVerificationWorkers > 64 {
			cfg.Poll.MaxVerificationWorkers = 64
		}
	}

	if cfg.Integrity.Algorithm == "" {
		cfg.Integrity.Algorithm = IntegrityTimestamp
	}
	if cfg.Integrity.VerificationAttempts == 0 {
		cfg.Integrity.VerificationAttempts = 1
	}
	if cfg.Integrity.VerificationInterval == 0 {
		cfg.Integrity.VerificationInterval = 5
	}

	if cfg.Action.ConcurrentConnections == 0 {
		cfg.Action.ConcurrentConnections = runtime.NumCPU() * 2
	}

	if cfg.Action.PostProcess.Action == "" {
		cfg.Action.PostProcess.Action = PostActionDelete
	}

	if cfg.Action.PostProcess.ArchivePath != "" && cfg.Action.PostProcess.ArchiveRetention == nil {
		defaultRetention := 7
		cfg.Action.PostProcess.ArchiveRetention = &defaultRetention
	}

	if cfg.Action.Type == ActionSFTP && cfg.Action.SFTP.Port == 0 {
		cfg.Action.SFTP.Port = 22
	}
}

// validate enforces mandatory fields, path safety, and security constraints.
//
// Security Checks:
// 1. Path Traversal: Strictly forbids ".." in all local and remote paths.
// 2. Absolute Paths: Requires absolute paths for polling, script, and archive directories.
// 3. Existence: Verifies that local polling and script paths exist on the filesystem.
// 4. Auth Completeness: Ensures either SSH keys or encrypted passwords are provided for SFTP.
// 5. Host Keys: Validates Base64 encoding and format of provided host keys.
func validate(cfg *Config) error {
	for _, logCfg := range cfg.Logging {
		if logCfg.LogName == "" || !filepath.IsAbs(logCfg.LogName) {
			return fmt.Errorf("log_name must be an absolute path: %s", logCfg.LogName)
		}
	}

	if cfg.Poll.Directory == "" || !filepath.IsAbs(cfg.Poll.Directory) {
		return fmt.Errorf("poll directory must be an absolute path: %s", cfg.Poll.Directory)
	}

	if cfg.Poll.MaxBatchSize != 0 && (cfg.Poll.MaxBatchSize < 100 || cfg.Poll.MaxBatchSize > 10000) {
		return fmt.Errorf("max_batch_size must be between 100 and 10000 inclusive: got %d", cfg.Poll.MaxBatchSize)
	}
	if cfg.Poll.MaxVerificationWorkers < 0 {
		return fmt.Errorf("max_verification_workers must be positive")
	}

	// Security: Prevent path traversal in Poll Directory
	if strings.Contains(cfg.Poll.Directory, "..") {
		return fmt.Errorf("poll directory path traversal is not allowed")
	}

	if _, err := os.Stat(cfg.Poll.Directory); err != nil {
		return fmt.Errorf("poll directory does not exist or is inaccessible: %w", err)
	}

	switch cfg.Poll.Algorithm {
	case PollInterval, PollBatch, PollEvent, PollTrigger:
	default:
		return fmt.Errorf("unsupported poll algorithm: %s", cfg.Poll.Algorithm)
	}

	if cfg.Poll.Algorithm == PollTrigger {
		if _, ok := cfg.Poll.Value.(string); !ok {
			return fmt.Errorf("trigger algorithm requires a string value for file pattern")
		}
	}

	switch cfg.Integrity.Algorithm {
	case IntegrityHash, IntegrityTimestamp, IntegritySize:
	default:
		return fmt.Errorf("unsupported integrity algorithm: %s", cfg.Integrity.Algorithm)
	}

	if cfg.Action.ConcurrentConnections < 0 {
		return fmt.Errorf("concurrent_connections must be positive")
	}

	switch cfg.Action.Type {
	case ActionSFTP:
		if cfg.Action.SFTP.Host == "" || cfg.Action.SFTP.Username == "" {
			return fmt.Errorf("SFTP host and username are required")
		}

		if cfg.Action.SFTP.RemotePath == "" {
			return fmt.Errorf("SFTP remote_path is required")
		}
		if !strings.HasPrefix(cfg.Action.SFTP.RemotePath, "/") {
			return fmt.Errorf("SFTP remote_path must be an absolute path (starting with /)")
		}
		// Security: Prevent path traversal in RemotePath
		// RemotePath is expected to be an absolute path on the SFTP server.
		// We strictly forbid ".." and ensure it follows the absolute path format.
		if strings.Contains(cfg.Action.SFTP.RemotePath, "..") {
			return fmt.Errorf("SFTP remote_path traversal is not allowed")
		}
		cleanRemote := filepath.ToSlash(filepath.Clean(cfg.Action.SFTP.RemotePath))
		if !strings.HasPrefix(cleanRemote, "/") {
			return fmt.Errorf("SFTP remote_path must be an absolute path")
		}

		if cfg.Action.SFTP.HostKey != "" {
			// Handle full OpenSSH format (e.g., "ssh-ed25519 AAAAC3NzaC...")
			parts := strings.Fields(cfg.Action.SFTP.HostKey)
			base64Key := parts[0]
			if len(parts) > 1 {
				base64Key = parts[1]
			}

			pubKeyData, err := base64.StdEncoding.DecodeString(base64Key)
			if err != nil {
				return fmt.Errorf("failed to decode host key: %w", err)
			}
			_, err = ssh.ParsePublicKey(pubKeyData)
			if err != nil {
				return fmt.Errorf("failed to parse host key: %w", err)
			}
		}

		if cfg.Action.SFTP.EncryptedPassword != "" {
			if err := validatePlatformSFTP(cfg); err != nil {
				return err
			}
		} else if cfg.Action.SFTP.SSHKeyPath != "" {
			if !filepath.IsAbs(cfg.Action.SFTP.SSHKeyPath) {
				return fmt.Errorf("ssh_key_path must be an absolute path: %s", cfg.Action.SFTP.SSHKeyPath)
			}
			if strings.Contains(cfg.Action.SFTP.SSHKeyPath, "..") {
				return fmt.Errorf("ssh_key_path traversal is not allowed")
			}
			if _, err := os.Stat(cfg.Action.SFTP.SSHKeyPath); err != nil {
				return fmt.Errorf("ssh_key_path does not exist: %w", err)
			}
		} else if cfg.Action.SFTP.SSHKeyPath == "" {
			return fmt.Errorf("either encrypted_password or ssh_key_path must be provided for SFTP")
		}
	case ActionScript:
		if cfg.Action.Script.Path == "" || !filepath.IsAbs(cfg.Action.Script.Path) {
			return fmt.Errorf("script path must be an absolute path: %s", cfg.Action.Script.Path)
		}
		// Security: Prevent path traversal in Script Path
		if strings.Contains(cfg.Action.Script.Path, "..") {
			return fmt.Errorf("script path traversal is not allowed")
		}
		if _, err := os.Stat(cfg.Action.Script.Path); err != nil {
			return fmt.Errorf("script path does not exist: %w", err)
		}
	default:
		return fmt.Errorf("unsupported action type: %s", cfg.Action.Type)
	}

	switch cfg.Action.PostProcess.Action {
	case PostActionDelete, PostActionMoveArchive, PostActionMoveCompress:
	default:
		return fmt.Errorf("unsupported post-processing action: %s", cfg.Action.PostProcess.Action)
	}

	if cfg.Action.PostProcess.Action != "" {
		if cfg.Action.PostProcess.ArchivePath == "" || !filepath.IsAbs(cfg.Action.PostProcess.ArchivePath) {
			return fmt.Errorf("archive_path must be an absolute path: %s", cfg.Action.PostProcess.ArchivePath)
		}
		// Security: Prevent path traversal in ArchivePath
		if strings.Contains(cfg.Action.PostProcess.ArchivePath, "..") {
			return fmt.Errorf("archive_path traversal is not allowed")
		}
	}
	return nil
}
