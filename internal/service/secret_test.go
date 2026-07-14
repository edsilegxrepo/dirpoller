// Package service_test provides unit tests for secret decryption within the Engine.
//
// Objective:
// Validate the platform-specific resolution of master keys and the secure
// decryption of SFTP passwords. It ensures that sensitive credentials
// are only decrypted when needed and that the engine correctly handles
// environment-based (Windows) and file-based (Linux) key storage.
//
// Scenarios Covered:
// - Windows Resolution: Verification of master key retrieval from environment variables.
// - Linux Resolution: Verification of master key retrieval from protected local files.
// - Decryption Timing: Ensures credentials are NOT decrypted prematurely during engine setup.
package service

import (
	"context"
	"criticalsys/secretprotector/pkg/libsecsecrets"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"criticalsys.net/dirpoller/internal/config"
	"criticalsys.net/dirpoller/internal/testutils"
)

func setupLinuxSecureEnv(t *testing.T, keyPath string) func() {
	oldStat := libsecsecrets.OsStat
	oldGOOS := libsecsecrets.RuntimeGOOS
	oldEngineGOOS := goos
	libsecsecrets.RuntimeGOOS = "linux"
	goos = "linux"
	libsecsecrets.OsStat = func(name string) (os.FileInfo, error) {
		if name == keyPath || strings.HasSuffix(name, filepath.Base(keyPath)) {
			return &testutils.MockFileInfo{FName: filepath.Base(keyPath), FSize: 64, FMode: 0o600}, nil
		}
		return os.Stat(name)
	}
	return func() {
		libsecsecrets.OsStat = oldStat
		libsecsecrets.RuntimeGOOS = oldGOOS
		goos = oldEngineGOOS
	}
}

// TestEngine_SecretDecryption_Windows validates master key resolution via environment variables.
//
// Scenario:
// 1. Encrypt a test password using a generated master key.
// 2. Configure the Engine to use a specific environment variable for the key.
// 3. Initialize the Engine and verify that it does NOT decrypt the password immediately.
//
// Success Criteria:
// - The engine must initialize without error.
// - cfg.Action.SFTP.Password must remain empty after NewEngine (deferring decryption to execution).
func TestEngine_SecretDecryption_Windows(t *testing.T) {
	// 1. Setup
	masterKeyStr, _ := libsecsecrets.GenerateKey()
	testPass := "win-secret-123"

	// Override GOOS to windows for this test
	oldGOOS := goos
	goos = "windows"
	defer func() { goos = oldGOOS }()

	// Resolve key once to encrypt the password for the test
	masterKey, _ := libsecsecrets.ResolveKey(context.Background(), masterKeyStr, "", "")
	encryptedPass, _ := libsecsecrets.Encrypt(context.Background(), testPass, masterKey)
	libsecsecrets.ZeroBuffer(masterKey)

	cfg := &config.Config{
		Poll: config.PollConfig{Directory: testutils.GetUniqueTestDir("service", "secret_win"), Algorithm: config.PollInterval},
		Action: config.ActionConfig{
			Type: config.ActionSFTP,
			SFTP: config.SFTPConfig{
				Host:              "localhost",
				Username:          "user",
				EncryptedPassword: encryptedPass,
				MasterKeyEnv:      "TEST_MASTER_KEY",
				RemotePath:        "/remote",
			},
		},
	}

	// 2. Set environment variable
	_ = os.Setenv("TEST_MASTER_KEY", masterKeyStr)
	defer func() { _ = os.Unsetenv("TEST_MASTER_KEY") }()

	// 3. Initialize Engine
	engine, err := NewEngine(cfg, false)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer engine.Close()

	// 4. Verify: Decryption no longer happens in NewEngine.
	// It happens in SFTPHandler.Execute.
	if cfg.Action.SFTP.Password != "" {
		t.Errorf("Expected password to be empty after NewEngine, got %q", cfg.Action.SFTP.Password)
	}

	// We can verify that the handler is correctly initialized
	if engine.handler == nil {
		t.Fatal("Expected engine.handler to be initialized")
	}
}

// TestEngine_SecretDecryption_Linux validates master key resolution via local files.
//
// Scenario:
// 1. Create a protected master key file.
// 2. Configure the Engine to use the file path for the master key.
// 3. Initialize the Engine and verify that it does NOT decrypt the password immediately.
//
// Success Criteria:
// - The engine must correctly resolve the file path and initialize.
// - cfg.Action.SFTP.Password must remain empty after setup.
func TestEngine_SecretDecryption_Linux(t *testing.T) {
	// 1. Setup
	tempDir := testutils.GetUniqueTestDir("service", "secret_linux")
	keyFile := filepath.Join(tempDir, "master.key")
	masterKeyStr, _ := libsecsecrets.GenerateKey()
	testPass := "linux-secret-456"

	// Mock security checks and override GOOS to linux
	defer setupLinuxSecureEnv(t, keyFile)()

	_ = os.WriteFile(keyFile, []byte(masterKeyStr), 0o600)

	// Resolve key once to encrypt the password for the test
	masterKey, _ := libsecsecrets.ResolveKey(context.Background(), masterKeyStr, "", "")
	encryptedPass, _ := libsecsecrets.Encrypt(context.Background(), testPass, masterKey)
	libsecsecrets.ZeroBuffer(masterKey)

	cfg := &config.Config{
		Poll: config.PollConfig{Directory: testutils.GetUniqueTestDir("service", "secret_linux_poll"), Algorithm: config.PollInterval},
		Action: config.ActionConfig{
			Type: config.ActionSFTP,
			SFTP: config.SFTPConfig{
				Host:              "localhost",
				Username:          "user",
				EncryptedPassword: encryptedPass,
				MasterKeyFile:     keyFile,
				RemotePath:        "/remote",
			},
		},
	}

	// 2. Initialize Engine
	engine, err := NewEngine(cfg, false)
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}
	defer engine.Close()

	// 3. Verify: Decryption no longer happens in NewEngine.
	if cfg.Action.SFTP.Password != "" {
		t.Errorf("Expected password to be empty after NewEngine, got %q", cfg.Action.SFTP.Password)
	}
}
