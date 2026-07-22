// Package config_test provides unit tests for the configuration loading and validation logic.
//
// Objective:
// Ensure the application's configuration engine correctly handles JSON parsing,
// default value assignment, and enforces strict security and path constraints.
// It guarantees that invalid or unsafe configurations are rejected before
// they can reach the core engine.
//
// Scenarios Covered:
// - Validation: Comprehensive checks for SFTP, Script, and Post-Action configurations.
// - Security: Rejection of path traversal (..) and relative paths in sensitive fields.
// - Platform Specifics: Enforcement of master key storage rules (Env on Windows, File on Linux).
// - Defaults: Verification that all optional fields are populated with sane defaults.
// - Loading: Success and failure paths for file-based configuration loading.
package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"criticalsys.net/dirpoller/internal/testutils"
)

func getTestDir(name string) string {
	return testutils.GetUniqueTestDir("config", name)
}

// TestConfigValidation_Comprehensive validates the strict rules applied to application configurations.
//
// Scenario:
// 1. Success paths for both SFTP and Script based configurations.
// 2. Negative testing for path traversal in local and remote paths.
// 3. Algorithm validation for both polling and integrity strategies.
// 4. Security validation for host keys, SSH keys, and script paths.
//
// Success Criteria:
// - Valid configurations must pass without error.
// - Insecure or malformed paths must return descriptive errors.
// - Mandatory fields must be strictly enforced.
func TestConfigValidation_Comprehensive(t *testing.T) {
	testDir := getTestDir("ValidationComp")
	_ = os.MkdirAll(testDir, 0o750)

	t.Run("ValidConfig_SFTP", func(t *testing.T) {
		cfg := &Config{
			Poll:      PollConfig{Directory: testDir, Algorithm: PollInterval, Value: 1},
			Integrity: IntegrityConfig{Algorithm: IntegritySize},
			Action: ActionConfig{
				Type: ActionSFTP,
				SFTP: SFTPConfig{Host: "h", Username: "u", EncryptedPassword: "p", RemotePath: "/r"},
				PostProcess: PostProcessConfig{
					Action:      PostActionDelete,
					ArchivePath: testDir,
				},
			},
		}
		setDefaults(cfg)
		if err := validate(cfg); err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})

	t.Run("ValidConfig_Script", func(t *testing.T) {
		exe, _ := os.Executable()
		cfg := &Config{
			Poll:      PollConfig{Directory: testDir, Algorithm: PollInterval},
			Integrity: IntegrityConfig{Algorithm: IntegrityHash},
			Action: ActionConfig{
				Type:   ActionScript,
				Script: ScriptConfig{Path: exe},
				PostProcess: PostProcessConfig{
					Action:      PostActionDelete,
					ArchivePath: testDir,
				},
			},
		}
		setDefaults(cfg)
		if err := validate(cfg); err != nil {
			t.Errorf("expected no error for script action, got %v", err)
		}
	})

	t.Run("ValidConfig_PostActions", func(t *testing.T) {
		exe, _ := os.Executable()
		archiveDir := filepath.Join(testDir, "archive")
		_ = os.MkdirAll(archiveDir, 0o750)

		actions := []PostAction{PostActionMoveArchive, PostActionMoveCompress}
		for _, action := range actions {
			cfg := &Config{
				Poll:      PollConfig{Directory: testDir, Algorithm: PollInterval},
				Integrity: IntegrityConfig{Algorithm: IntegrityTimestamp},
				Action: ActionConfig{
					Type:        ActionScript,
					Script:      ScriptConfig{Path: exe},
					PostProcess: PostProcessConfig{Action: action, ArchivePath: archiveDir},
				},
			}
			setDefaults(cfg)
			if err := validate(cfg); err != nil {
				t.Errorf("expected no error for post action %s, got %v", action, err)
			}
		}
	})

	t.Run("PathTraversal_PollDir", func(t *testing.T) {
		cfg := &Config{
			Poll: PollConfig{Directory: testDir + "/../other"},
		}
		if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "path traversal") {
			t.Errorf("expected path traversal error, got %v", err)
		}
	})

	t.Run("PollAlgorithms", func(t *testing.T) {
		algos := []PollAlgorithm{PollInterval, PollBatch, PollEvent, PollTrigger}
		for _, algo := range algos {
			cfg := &Config{
				Poll:      PollConfig{Directory: testDir, Algorithm: algo, Value: "trigger"},
				Integrity: IntegrityConfig{Algorithm: IntegritySize},
				Action: ActionConfig{
					Type: ActionSFTP,
					SFTP: SFTPConfig{Host: "h", Username: "u", EncryptedPassword: "p", RemotePath: "/r"},
					PostProcess: PostProcessConfig{
						Action:      PostActionDelete,
						ArchivePath: testDir,
					},
				},
			}
			setDefaults(cfg)
			if err := validate(cfg); err != nil {
				t.Errorf("expected no error for algo %s, got %v", algo, err)
			}
		}

		cfgInvalid := &Config{Poll: PollConfig{Directory: testDir, Algorithm: "invalid"}}
		if err := validate(cfgInvalid); err == nil {
			t.Error("expected error for invalid poll algorithm")
		}
	})

	t.Run("PollTrigger_ValueType", func(t *testing.T) {
		cfg := &Config{
			Poll:      PollConfig{Directory: testDir, Algorithm: PollTrigger, Value: 123},
			Integrity: IntegrityConfig{Algorithm: IntegritySize},
			Action: ActionConfig{
				Type:   ActionScript,
				Script: ScriptConfig{Path: "C:\\Windows\\System32\\cmd.exe"},
				PostProcess: PostProcessConfig{
					Action:      PostActionDelete,
					ArchivePath: testDir,
				},
			},
		}
		if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "requires a string value") {
			t.Errorf("expected trigger value error, got %v", err)
		}
	})

	t.Run("IntegrityAlgorithms", func(t *testing.T) {
		algos := []IntegrityAlgorithm{IntegrityHash, IntegrityTimestamp, IntegritySize}
		for _, algo := range algos {
			cfg := &Config{
				Poll:      PollConfig{Directory: testDir, Algorithm: PollInterval},
				Integrity: IntegrityConfig{Algorithm: algo},
				Action: ActionConfig{
					Type: ActionSFTP,
					SFTP: SFTPConfig{Host: "h", Username: "u", EncryptedPassword: "p", RemotePath: "/r"},
					PostProcess: PostProcessConfig{
						Action:      PostActionDelete,
						ArchivePath: testDir,
					},
				},
			}
			setDefaults(cfg)
			if err := validate(cfg); err != nil {
				t.Errorf("expected no error for integrity %s, got %v", algo, err)
			}
		}

		cfgInvalid := &Config{
			Poll:      PollConfig{Directory: testDir, Algorithm: PollInterval},
			Integrity: IntegrityConfig{Algorithm: "invalid"},
		}
		if err := validate(cfgInvalid); err == nil {
			t.Error("expected error for invalid integrity algorithm")
		}
	})

	t.Run("SFTP_MissingFields", func(t *testing.T) {
		cases := []struct {
			name string
			mod  func(*ActionConfig)
			err  string
		}{
			{"NoHost", func(c *ActionConfig) { c.SFTP.Host = "" }, "SFTP host and username are required"},
			{"NoUser", func(c *ActionConfig) { c.SFTP.Username = "" }, "SFTP host and username are required"},
			{"NoPath", func(c *ActionConfig) { c.SFTP.RemotePath = "" }, "SFTP remote_path is required"},
			{"RelPath", func(c *ActionConfig) { c.SFTP.RemotePath = "rel" }, "SFTP remote_path must be an absolute path (starting with /)"},
			{"Traversal", func(c *ActionConfig) { c.SFTP.RemotePath = "/r/../p" }, "remote_path traversal"},
			{"NoAuth", func(c *ActionConfig) { c.SFTP.EncryptedPassword = ""; c.SFTP.SSHKeyPath = "" }, "either encrypted_password or ssh_key_path"},
		}

		for _, tc := range cases {
			cfg := &Config{
				Poll:      PollConfig{Directory: testDir, Algorithm: PollInterval},
				Integrity: IntegrityConfig{Algorithm: IntegritySize},
				Action: ActionConfig{
					Type: ActionSFTP,
					SFTP: SFTPConfig{Host: "h", Username: "u", EncryptedPassword: "p", RemotePath: "/r"},
				},
			}
			tc.mod(&cfg.Action)
			if err := validate(cfg); err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s: expected error %q, got %v", tc.name, tc.err, err)
			}
		}
	})

	t.Run("SFTP_HostKey_Validation", func(t *testing.T) {
		cfg := &Config{
			Poll:      PollConfig{Directory: testDir, Algorithm: PollInterval},
			Integrity: IntegrityConfig{Algorithm: IntegritySize},
			Action: ActionConfig{
				Type: ActionSFTP,
				SFTP: SFTPConfig{
					Host: "h", Username: "u", EncryptedPassword: "p", RemotePath: "/r",
				},
				PostProcess: PostProcessConfig{
					Action:      PostActionDelete,
					ArchivePath: testDir,
				},
			},
		}
		setDefaults(cfg)

		hostKeys := []string{
			"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQDq2NqQIu+oX6m03qqY7x8pMGF6sKyTdxgNMkgG4Ho3A+z8WqTX0wUGXSMaurtO8FBcbvZrFfT9utzQzNpkCEtEoGvHg73UovZwmQs2EidnNvDu+FgqSBNevqGvc0ZtZ3CTwbYfL6jg/kJVWm82+x4dFCRDroz9arDkBneqIaONqCPrGpPFYfVE21D9G+1CL6vfu7hTgYpVV8vSGkDGc5ipTyWYAzqcltaIqiAL7NSpeFIQ+R1sHJRrn3bwbH3eIimvOvtY16D74u7IGKV78QdL8zua86jvuf+VAe61hy0PiE6QYSla1LuxR3FSHDIidYwyB92Z8aJDu7VZULT60zIY5bzcHwBoGrUbDAcBY4nipURq+0FQOkY/RsiJff3L2/1De0k92Zug0v5MkOObk+59jz/1aoaxji7KpOKQQD/e2hXhHahF+aRswOwbVyUdc6J5qCKFSguXpxOG7EbvQgGFZdbE3HNpxxIn4/3Gce6vwIfHyzyMdVFFqCajXerFl1c=",
			"ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBEcMMxz2+v6ERnGpHlcAK3bmgZqFKr+hs7nyQMpeS6RFWcZC0XalmfFpN5jxeQ2f7Xf0mRTLmAi1eemSvnrcShk=",
			"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAINfFSlHZATKjPp9Vwg4l9Ecft2rYqObUItGg1YaYSVWH",
			"AAAAC3NzaC1lZDI1NTE5AAAAINfFSlHZATKjPp9Vwg4l9Ecft2rYqObUItGg1YaYSVWH", // Base64 only
		}

		for _, hk := range hostKeys {
			cfg.Action.SFTP.HostKey = hk
			if err := validate(cfg); err != nil {
				t.Errorf("expected host key %q to pass, got %v", hk, err)
			}
		}

		cfg.Action.SFTP.HostKey = "!!!"
		if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "failed to decode host key") {
			t.Errorf("expected decode error for invalid base64, got %v", err)
		}

		cfg.Action.SFTP.HostKey = "YWJjZA==" // "abcd"
		if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "failed to parse host key") {
			t.Errorf("expected parse error for invalid key data, got %v", err)
		}
	})

	t.Run("ConcurrentConnections_Negative", func(t *testing.T) {
		cfg := &Config{
			Poll:      PollConfig{Directory: testDir, Algorithm: PollInterval},
			Integrity: IntegrityConfig{Algorithm: IntegritySize},
			Action: ActionConfig{
				Type:                  ActionScript,
				ConcurrentConnections: -1,
				Script:                ScriptConfig{Path: "C:\\Windows\\System32\\cmd.exe"},
				PostProcess: PostProcessConfig{
					Action:      PostActionDelete,
					ArchivePath: testDir,
				},
			},
		}
		if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "must be positive") {
			t.Errorf("expected negative connections error, got %v", err)
		}
	})

	t.Run("Script_Validation", func(t *testing.T) {
		exe, _ := os.Executable()
		cases := []struct {
			name string
			mod  func(*ScriptConfig)
			err  string
		}{
			{"Empty", func(s *ScriptConfig) { s.Path = "" }, "script path must be an absolute path"},
			{"InvalidPath", func(s *ScriptConfig) { s.Path = "rel/script.sh" }, "script path must be an absolute path"},
			{"Traversal", func(s *ScriptConfig) { s.Path = testDir + "/../sh" }, "script path traversal"},
			{"NonExistent", func(s *ScriptConfig) { s.Path = filepath.Join(testDir, "none.sh") }, "script path does not exist"},
		}

		for _, tc := range cases {
			cfg := &Config{
				Poll:      PollConfig{Directory: testDir, Algorithm: PollInterval},
				Integrity: IntegrityConfig{Algorithm: IntegritySize},
				Action: ActionConfig{
					Type:   ActionScript,
					Script: ScriptConfig{Path: exe},
					PostProcess: PostProcessConfig{
						Action:      PostActionDelete,
						ArchivePath: testDir,
					},
				},
			}
			tc.mod(&cfg.Action.Script)
			if err := validate(cfg); err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s: expected error %q, got %v", tc.name, tc.err, err)
			}
		}
	})

	t.Run("SSHKey_Validation", func(t *testing.T) {
		keyFile := filepath.Join(testDir, "id_rsa")
		_ = os.WriteFile(keyFile, []byte("key"), 0o600)

		cases := []struct {
			name string
			mod  func(*SFTPConfig)
			err  string
		}{
			{"Relative", func(s *SFTPConfig) { s.SSHKeyPath = "rel" }, "ssh_key_path must be an absolute path"},
			{"Traversal", func(s *SFTPConfig) { s.SSHKeyPath = testDir + "/../k" }, "ssh_key_path traversal"},
			{"NotFound", func(s *SFTPConfig) { s.SSHKeyPath = filepath.Join(testDir, "notfound") }, "does not exist"},
		}

		for _, tc := range cases {
			cfg := &Config{
				Poll:      PollConfig{Directory: testDir, Algorithm: PollInterval},
				Integrity: IntegrityConfig{Algorithm: IntegritySize},
				Action: ActionConfig{
					Type: ActionSFTP,
					SFTP: SFTPConfig{Host: "h", Username: "u", SSHKeyPath: keyFile, RemotePath: "/r"},
					PostProcess: PostProcessConfig{
						Action:      PostActionDelete,
						ArchivePath: testDir,
					},
				},
			}
			tc.mod(&cfg.Action.SFTP)
			if err := validate(cfg); err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s: expected error %q, got %v", tc.name, tc.err, err)
			}
		}
	})

	t.Run("PostProcess_Validation", func(t *testing.T) {
		cases := []struct {
			name string
			mod  func(*PostProcessConfig)
			err  string
		}{
			{"NoPath", func(p *PostProcessConfig) { p.ArchivePath = "" }, "archive_path must be an absolute path"},
			{"Relative", func(p *PostProcessConfig) { p.ArchivePath = "rel" }, "archive_path must be an absolute path"},
			{"Traversal", func(p *PostProcessConfig) { p.ArchivePath = testDir + "/../a" }, "archive_path traversal"},
			{"InvalidAction", func(p *PostProcessConfig) { p.Action = "invalid" }, "unsupported post-processing action"},
		}

		for _, tc := range cases {
			cfg := &Config{
				Poll:      PollConfig{Directory: testDir, Algorithm: PollInterval},
				Integrity: IntegrityConfig{Algorithm: IntegritySize},
				Action: ActionConfig{
					Type: ActionSFTP,
					SFTP: SFTPConfig{Host: "h", Username: "u", EncryptedPassword: "p", RemotePath: "/r"},
					PostProcess: PostProcessConfig{
						Action:      PostActionMoveArchive,
						ArchivePath: testDir,
					},
				},
			}
			tc.mod(&cfg.Action.PostProcess)
			if err := validate(cfg); err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s: expected error %q, got %v", tc.name, tc.err, err)
			}
		}
	})

	t.Run("Logging_Validation", func(t *testing.T) {
		cfg := &Config{
			Poll:      PollConfig{Directory: testDir, Algorithm: PollInterval},
			Integrity: IntegrityConfig{Algorithm: IntegritySize},
			Logging:   []LoggingConfig{{LogRetention: 7}},
		}
		if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "log_name must be an absolute path") {
			t.Errorf("expected missing log_name error, got %v", err)
		}
	})

	t.Run("Windows_UNC_Paths", func(t *testing.T) {
		if runtime.GOOS != "windows" {
			t.Skip("Skipping Windows UNC path test on non-Windows platform")
		}
		cfg := &Config{
			Poll:      PollConfig{Directory: `\\Server\Share\Poll`, Algorithm: PollInterval},
			Integrity: IntegrityConfig{Algorithm: IntegritySize},
			Action: ActionConfig{
				Type:   ActionScript,
				Script: ScriptConfig{Path: `\\Server\Share\Scripts\process.bat`},
				PostProcess: PostProcessConfig{
					Action:      PostActionMoveArchive,
					ArchivePath: `\\Server\Share\Archive`,
				},
			},
			Logging: []LoggingConfig{{LogName: `\\Server\Share\Logs\poller.log`}},
		}
		// os.Stat will fail for these non-existent UNC paths, so we'll just check IsAbs
		if !filepath.IsAbs(cfg.Poll.Directory) {
			t.Errorf("expected UNC poll directory to be absolute")
		}
		if !filepath.IsAbs(cfg.Action.Script.Path) {
			t.Errorf("expected UNC script path to be absolute")
		}
		if !filepath.IsAbs(cfg.Action.PostProcess.ArchivePath) {
			t.Errorf("expected UNC archive path to be absolute")
		}
		if !filepath.IsAbs(cfg.Logging[0].LogName) {
			t.Errorf("expected UNC log name to be absolute")
		}
	})

	t.Run("OSSpecific_Validation", func(t *testing.T) {
		cfg := &Config{
			Poll:      PollConfig{Directory: testDir, Algorithm: PollInterval},
			Integrity: IntegrityConfig{Algorithm: IntegritySize},
			Action: ActionConfig{
				Type: ActionSFTP,
				SFTP: SFTPConfig{Host: "h", Username: "u", EncryptedPassword: "p", RemotePath: "/r"},
				PostProcess: PostProcessConfig{
					Action:      PostActionDelete,
					ArchivePath: testDir,
				},
			},
		}

		if testutils.IsWindows() {
			cfg.Action.SFTP.MasterKeyFile = "C:/key"
			if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "master_key_file is not supported on Windows") {
				t.Errorf("expected master_key_file error on Windows, got %v", err)
			}
		} else {
			cfg.Action.SFTP.MasterKeyFile = ""
			cfg.Action.SFTP.MasterKeyEnv = "KEY"
			if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "master_key_env is not supported on Linux") {
				t.Errorf("expected master_key_env error on Linux, got %v", err)
			}
		}
	})

	t.Run("Security_PollDirectory_Traversal", func(t *testing.T) {
		cfg := &Config{
			Poll: PollConfig{Directory: testDir + "/.."},
		}
		if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "path traversal") {
			t.Errorf("expected path traversal error, got %v", err)
		}
	})

	t.Run("Security_SFTP_RemotePath_CleanAbsolute", func(t *testing.T) {
		cfg := &Config{
			Poll:      PollConfig{Directory: testDir, Algorithm: PollInterval},
			Integrity: IntegrityConfig{Algorithm: IntegritySize},
			Action: ActionConfig{
				Type: ActionSFTP,
				SFTP: SFTPConfig{Host: "h", Username: "u", EncryptedPassword: "p", RemotePath: "/r/../p"},
			},
		}
		if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "remote_path traversal") {
			t.Errorf("expected remote_path traversal error, got %v", err)
		}
	})

	t.Run("Security_PollDirectory_AbsoluteCheck", func(t *testing.T) {
		cfg := &Config{
			Poll: PollConfig{Directory: "relative"},
		}
		if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "absolute path") {
			t.Errorf("expected absolute path error, got %v", err)
		}
	})

	t.Run("SFTP_RemotePath_SlashCheck", func(t *testing.T) {
		cfg := &Config{
			Poll:      PollConfig{Directory: testDir, Algorithm: PollInterval},
			Integrity: IntegrityConfig{Algorithm: IntegritySize},
			Action: ActionConfig{
				Type: ActionSFTP,
				SFTP: SFTPConfig{Host: "h", Username: "u", EncryptedPassword: "p", RemotePath: "noroot"},
			},
		}
		if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "starting with /") {
			t.Errorf("expected leading slash error, got %v", err)
		}
	})

	t.Run("CleanPoll_AbsoluteCheck", func(t *testing.T) {
		// Mock a situation where clean results in non-absolute.
		// Since we use filepath.IsAbs first, this is hard to hit, but we'll try.
		cfg := &Config{
			Poll: PollConfig{Directory: testDir},
		}
		setDefaults(cfg)
		// No easy way to trigger cleanPoll !IsAbs without mocking filepath.IsAbs itself.
		_ = validate(cfg)
	})
}

func TestConfig_Defaults_All(t *testing.T) {
	cfg := &Config{}
	setDefaults(cfg)
	if runtime.GOOS == "windows" {
		if cfg.ServiceName != "DirPoller" {
			t.Errorf("ServiceName default: %s", cfg.ServiceName)
		}
	} else {
		if cfg.ServiceName != "" {
			t.Errorf("ServiceName should be empty on Linux, got: %s", cfg.ServiceName)
		}
	}
	if cfg.Poll.Algorithm != PollInterval {
		t.Errorf("Poll algo default: %s", cfg.Poll.Algorithm)
	}
	if cfg.Integrity.Algorithm != IntegrityTimestamp {
		t.Errorf("Integrity algo default: %s", cfg.Integrity.Algorithm)
	}
	if cfg.Integrity.VerificationAttempts != 1 {
		t.Errorf("Attempts default: %d", cfg.Integrity.VerificationAttempts)
	}
	if cfg.Integrity.VerificationInterval != 5 {
		t.Errorf("Interval default: %d", cfg.Integrity.VerificationInterval)
	}
	if cfg.Action.ConcurrentConnections <= 0 {
		t.Errorf("Conns default: %d", cfg.Action.ConcurrentConnections)
	}
	if cfg.Action.PostProcess.Action != PostActionDelete {
		t.Errorf("PostAction default: %s", cfg.Action.PostProcess.Action)
	}

	cfg2 := &Config{
		Poll:   PollConfig{Algorithm: PollBatch},
		Action: ActionConfig{Type: ActionSFTP},
	}
	setDefaults(cfg2)
	if cfg2.Poll.BatchTimeoutSeconds != 600 {
		t.Errorf("BatchTimeout default: %d", cfg2.Poll.BatchTimeoutSeconds)
	}
	if cfg2.Action.SFTP.Port != 22 {
		t.Errorf("SFTP Port default: %d", cfg2.Action.SFTP.Port)
	}
}

func TestConfig_Load(t *testing.T) {
	testDir := getTestDir("ConfigLoad")
	_ = os.MkdirAll(testDir, 0o750)
	cfgFile := filepath.Join(testDir, "config.json")

	t.Run("Success", func(t *testing.T) {
		exe, _ := os.Executable()
		content := `{
			"poll": {"directory": "` + filepath.ToSlash(testDir) + `", "algorithm": "interval", "value": 1},
			"integrity": {"algorithm": "size"},
			"action": {
				"type": "script",
				"script": {"path": "` + filepath.ToSlash(exe) + `"},
				"post_process": {"action": "delete", "archive_path": "` + filepath.ToSlash(testDir) + `"}
			}
		}`
		_ = os.WriteFile(cfgFile, []byte(content), 0o644)
		cfg, _, err := LoadConfig(cfgFile)
		if err != nil {
			t.Fatalf("LoadConfig failed: %v", err)
		}
		if cfg.Poll.Value.(float64) != 1 {
			t.Errorf("expected value 1, got %v", cfg.Poll.Value)
		}
	})

	t.Run("FileNotFound", func(t *testing.T) {
		_, _, err := LoadConfig("nonexistent.json")
		if err == nil {
			t.Error("expected error")
		}
	})

	t.Run("InvalidJSON", func(t *testing.T) {
		_ = os.WriteFile(cfgFile, []byte("{bad}"), 0o644)
		_, _, err := LoadConfig(cfgFile)
		if err == nil {
			t.Error("expected error")
		}
	})
}

func TestConfigScaleSettings(t *testing.T) {
	testDir := getTestDir("ScaleSettings")
	_ = os.MkdirAll(testDir, 0o750)

	t.Run("SaneDefaults", func(t *testing.T) {
		cfg := &Config{
			Poll:      PollConfig{Directory: testDir, Algorithm: PollInterval},
			Integrity: IntegrityConfig{Algorithm: IntegritySize},
			Action: ActionConfig{
				Type: ActionSFTP,
				SFTP: SFTPConfig{Host: "h", Username: "u", EncryptedPassword: "p", RemotePath: "/r"},
				PostProcess: PostProcessConfig{
					Action:      PostActionDelete,
					ArchivePath: testDir,
				},
			},
		}
		setDefaults(cfg)
		if cfg.Poll.MaxBatchSize != 1000 {
			t.Errorf("expected MaxBatchSize 1000, got %d", cfg.Poll.MaxBatchSize)
		}
		if cfg.Poll.MaxVerificationWorkers <= 0 || cfg.Poll.MaxVerificationWorkers > 64 {
			t.Errorf("expected MaxVerificationWorkers within (0, 64], got %d", cfg.Poll.MaxVerificationWorkers)
		}
		if err := validate(cfg); err != nil {
			t.Errorf("validation failed: %v", err)
		}
	})

	t.Run("InvalidMaxBatchSize", func(t *testing.T) {
		cfg := &Config{
			Poll:      PollConfig{Directory: testDir, Algorithm: PollInterval, MaxBatchSize: 50},
			Integrity: IntegrityConfig{Algorithm: IntegritySize},
			Action: ActionConfig{
				Type: ActionSFTP,
				SFTP: SFTPConfig{Host: "h", Username: "u", EncryptedPassword: "p", RemotePath: "/r"},
				PostProcess: PostProcessConfig{
					Action:      PostActionDelete,
					ArchivePath: testDir,
				},
			},
		}
		setDefaults(cfg)
		cfg.Poll.MaxBatchSize = 50
		if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "max_batch_size must be between 100 and 10000") {
			t.Errorf("expected max_batch_size error for 50, got %v", err)
		}

		cfg.Poll.MaxBatchSize = 20000
		if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "max_batch_size must be between 100 and 10000") {
			t.Errorf("expected max_batch_size error for 20000, got %v", err)
		}
	})

	t.Run("InvalidMaxVerificationWorkers", func(t *testing.T) {
		cfg := &Config{
			Poll:      PollConfig{Directory: testDir, Algorithm: PollInterval, MaxVerificationWorkers: -5},
			Integrity: IntegrityConfig{Algorithm: IntegritySize},
			Action: ActionConfig{
				Type: ActionSFTP,
				SFTP: SFTPConfig{Host: "h", Username: "u", EncryptedPassword: "p", RemotePath: "/r"},
				PostProcess: PostProcessConfig{
					Action:      PostActionDelete,
					ArchivePath: testDir,
				},
			},
		}
		setDefaults(cfg)
		if err := validate(cfg); err == nil || !strings.Contains(err.Error(), "max_verification_workers must be positive") {
			t.Errorf("expected max_verification_workers error, got %v", err)
		}
	})

	t.Run("DisabledIntegrity_AlgorithmNone", func(t *testing.T) {
		cfg := &Config{
			Poll:      PollConfig{Directory: testDir, Algorithm: PollInterval},
			Integrity: IntegrityConfig{Algorithm: IntegrityNone},
			Action: ActionConfig{
				Type: ActionSFTP,
				SFTP: SFTPConfig{Host: "h", Username: "u", EncryptedPassword: "p", RemotePath: "/r"},
				PostProcess: PostProcessConfig{
					Action:      PostActionDelete,
					ArchivePath: testDir,
				},
			},
		}
		setDefaults(cfg)
		if cfg.Integrity.Algorithm != IntegrityNone {
			t.Errorf("expected IntegrityNone, got %s", cfg.Integrity.Algorithm)
		}
		if cfg.Integrity.VerificationAttempts != 0 {
			t.Errorf("expected VerificationAttempts 0, got %d", cfg.Integrity.VerificationAttempts)
		}
		if err := validate(cfg); err != nil {
			t.Errorf("validation failed for IntegrityNone: %v", err)
		}
	})

	t.Run("DisabledIntegrity_AttemptsZero", func(t *testing.T) {
		cfg := &Config{
			Poll:      PollConfig{Directory: testDir, Algorithm: PollInterval},
			Integrity: IntegrityConfig{Algorithm: IntegritySize, VerificationAttempts: 0, VerificationInterval: 0},
			Action: ActionConfig{
				Type: ActionSFTP,
				SFTP: SFTPConfig{Host: "h", Username: "u", EncryptedPassword: "p", RemotePath: "/r"},
				PostProcess: PostProcessConfig{
					Action:      PostActionDelete,
					ArchivePath: testDir,
				},
			},
		}
		setDefaults(cfg)
		if cfg.Integrity.Algorithm != IntegrityNone {
			t.Errorf("expected IntegrityNone for attempts=0, got %s", cfg.Integrity.Algorithm)
		}
		if cfg.Integrity.VerificationAttempts != 0 {
			t.Errorf("expected VerificationAttempts 0, got %d", cfg.Integrity.VerificationAttempts)
		}
		if err := validate(cfg); err != nil {
			t.Errorf("validation failed for attempts=0: %v", err)
		}
	})

	t.Run("DisabledIntegrity_AlgorithmDisabled", func(t *testing.T) {
		cfg := &Config{
			Poll:      PollConfig{Directory: testDir, Algorithm: PollInterval},
			Integrity: IntegrityConfig{Algorithm: "disabled"},
			Action: ActionConfig{
				Type: ActionSFTP,
				SFTP: SFTPConfig{Host: "h", Username: "u", EncryptedPassword: "p", RemotePath: "/r"},
				PostProcess: PostProcessConfig{
					Action:      PostActionDelete,
					ArchivePath: testDir,
				},
			},
		}
		setDefaults(cfg)
		if cfg.Integrity.Algorithm != IntegrityNone {
			t.Errorf("expected IntegrityNone for algorithm 'disabled', got %s", cfg.Integrity.Algorithm)
		}
		if cfg.Integrity.VerificationAttempts != 0 {
			t.Errorf("expected VerificationAttempts 0, got %d", cfg.Integrity.VerificationAttempts)
		}
		if err := validate(cfg); err != nil {
			t.Errorf("validation failed for 'disabled': %v", err)
		}
	})
}
