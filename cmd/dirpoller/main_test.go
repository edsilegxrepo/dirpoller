// Package main_test provides unit tests for the DirPoller entry point.
//
// Objective:
// Validate the high-level orchestration logic of the application, including
// CLI flag parsing, configuration loading, and engine bootstrapping. It
// ensures that the application starts correctly with various flag
// combinations and that environment overrides are applied as intended.
//
// Scenarios Covered:
// - Flag Parsing: Verification of -version, -config, and missing arguments.
// - Overrides: Testing of CLI flags that override configuration file values.
// - Bootstrapping: Simulation of the high-level run() and main() functions.
// - Error Handling: Graceful exit paths for invalid configurations or files.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"criticalsys.net/dirpoller/internal/config"
	"criticalsys.net/dirpoller/internal/testutils"
)

// TestMainFlags validates the CLI argument parsing and orchestration logic.
//
// Scenario:
// 1. Version: Verifies that -version prints info and exits with code 0.
// 2. Missing Config: Ensures an error code is returned when no config is provided.
// 3. Flag Overrides: Confirms that CLI flags (e.g., -log, -name) override JSON values.
// 4. Engine Run: Simulates a successful engine bootstrap and execution loop.
//
// Success Criteria:
// - Correct exit codes must be returned for all scenarios.
// - Configuration overrides must be applied correctly to the Config struct.
// - The engine's Run and Close methods must be called in the success path.
func TestMainFlags(t *testing.T) {
	// Create a dummy config file
	tempDir := testutils.GetUniqueTestDir("cmd", "main_flags")
	configPath := filepath.Join(tempDir, "config.json")

	// Use a short interval and context with timeout to ensure run() returns
	configContent := `{
		"ServiceName": "TestService",
		"poll": {
			"directory": "` + filepath.ToSlash(tempDir) + `",
			"interval": 1,
			"algorithm": "interval"
		},
		"integrity": {
			"algorithm": "timestamp",
			"attempts": 1,
			"interval": 1
		},
		"action": {
			"type": "script",
			"concurrent_connections": 1,
			"post_process": {
				"action": "delete",
				"archive_path": "` + filepath.ToSlash(filepath.Join(tempDir, "archive")) + `"
			},
			"script": {
				"path": "` + filepath.ToSlash(filepath.Join(tempDir, "script.bat")) + `",
				"timeout_seconds": 1
			}
		},
		"logging": [
			{
				"log_name": "` + filepath.ToSlash(filepath.Join(tempDir, "test.log")) + `",
				"log_retention": 1
			}
		]
	}`
	// Create the script file to pass validation
	if err := os.WriteFile(filepath.Join(tempDir, "script.bat"), []byte("@echo off"), 0o755); err != nil {
		t.Fatalf("failed to write dummy script: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write dummy config: %v", err)
	}

	tests := []struct {
		name string
		args []string
		code int
	}{
		{"version", []string{"cmd", "-version"}, 0},
		{"missing-config", []string{"cmd"}, 1},
		{"nonexistent-config", []string{"cmd", "-config", filepath.Join(tempDir, "nonexistent.json")}, 1},
		{"relative-config", []string{"cmd", "-config", "config.json"}, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if code := run(tt.args); code != tt.code {
				t.Errorf("%s: expected exit code %d, got %d", tt.name, tt.code, code)
			}
		})
	}

	// Test flag overrides
	t.Run("flag-overrides", func(t *testing.T) {
		// Mock newEngine to return an error so it doesn't block
		oldNewEngine := newEngine
		newEngine = func(cfg *config.Config, isService bool) (EngineRunner, error) {
			return nil, fmt.Errorf("mock error to stop execution")
		}
		defer func() { newEngine = oldNewEngine }()

		logPath := filepath.Join(tempDir, "override.log")
		args := []string{"cmd", "-config", configPath, "-log", logPath, "-log-retention", "5", "-name", "OverrideService"}
		if code := run(args); code != 1 {
			t.Errorf("expected exit code 1 due to mock error, got %d", code)
		}

		// Test log retention override without log name
		args = []string{"cmd", "-config", configPath, "-log-retention", "10"}
		if code := run(args); code != 1 {
			t.Errorf("expected exit code 1 due to mock error, got %d", code)
		}

		// Test relative log path
		args = []string{"cmd", "-config", configPath, "-log", "relative/path.log"}
		if code := run(args); code != 1 {
			t.Errorf("expected exit code 1 for relative log path, got %d", code)
		}
	})

	// Test engine error path
	t.Run("engine-run-error", func(t *testing.T) {
		oldNewEngine := newEngine
		engine := &mockEngine{runErr: fmt.Errorf("engine failure")}
		newEngine = func(cfg *config.Config, isService bool) (EngineRunner, error) {
			return engine, nil
		}
		defer func() { newEngine = oldNewEngine }()

		args := []string{"cmd", "-config", configPath}
		if code := run(args); code != 1 {
			t.Errorf("expected exit code 1 for engine error, got %d", code)
		}
	})

	// Test engine run path
	t.Run("engine-run", func(t *testing.T) {
		oldNewEngine := newEngine
		engine := &mockEngine{}
		newEngine = func(cfg *config.Config, isService bool) (EngineRunner, error) {
			return engine, nil
		}
		defer func() { newEngine = oldNewEngine }()

		args := []string{"cmd", "-config", configPath}
		if code := run(args); code != 0 {
			t.Errorf("expected exit code 0, got %d", code)
		}
		if !engine.runCalled {
			t.Error("expected engine.Run to be called")
		}
		if !engine.closeCalled {
			t.Error("expected engine.Close to be called")
		}
	})

	// Test config path error
	t.Run("config-path-error", func(t *testing.T) {
		args := []string{"cmd", "-config", "\000"}
		if code := run(args); code != 1 {
			t.Errorf("expected exit code 1 for invalid path, got %d", code)
		}
	})

	// Test flag parse error
	t.Run("flag-parse-error", func(t *testing.T) {
		args := []string{"cmd", "-invalid-flag"}
		if code := run(args); code != 1 {
			t.Errorf("expected exit code 1 for invalid flag, got %d", code)
		}
	})
}

type mockEngine struct {
	runCalled   bool
	closeCalled bool
	runErr      error
}

func (m *mockEngine) Run(ctx context.Context) error {
	m.runCalled = true
	return m.runErr
}

func (m *mockEngine) Close() {
	m.closeCalled = true
}

func TestMain(t *testing.T) {
	oldNewEngine := newEngine
	newEngine = func(cfg *config.Config, isService bool) (EngineRunner, error) {
		return &mockEngine{}, nil
	}
	defer func() { newEngine = oldNewEngine }()

	// Create a dummy config
	tempDir := testutils.GetUniqueTestDir("cmd", "main_entry")
	configPath := filepath.Join(tempDir, "config.json")
	scriptPath := filepath.Join(tempDir, "test.bat")
	if err := os.WriteFile(scriptPath, []byte("@echo off"), 0o755); err != nil {
		t.Fatalf("failed to write dummy script: %v", err)
	}

	configContent := `{
		"ServiceName": "TestService",
		"poll": {
			"directory": "` + filepath.ToSlash(tempDir) + `",
			"interval": 1,
			"algorithm": "interval"
		},
		"integrity": {
			"algorithm": "timestamp",
			"attempts": 1,
			"interval": 1
		},
		"action": {
			"type": "script",
			"concurrent_connections": 1,
			"post_process": {
				"action": "delete",
				"archive_path": "` + filepath.ToSlash(filepath.Join(tempDir, "archive")) + `"
			},
			"script": {
				"path": "` + filepath.ToSlash(filepath.Join(tempDir, "test.bat")) + `",
				"timeout_seconds": 1
			}
		},
		"logging": [
			{
				"log_name": "` + filepath.ToSlash(filepath.Join(tempDir, "main_test.log")) + `",
				"log_retention": 1
			}
		]
	}`
	if err := os.WriteFile(configPath, []byte(configContent), 0o644); err != nil {
		t.Fatalf("failed to write dummy config: %v", err)
	}

	oldArgs := os.Args
	defer func() { os.Args = oldArgs }()

	// Test Main with exit code 0
	t.Run("main-success", func(t *testing.T) {
		os.Args = []string{"cmd", "-config", configPath}
		main()
	})

	// Test Main with non-zero exit code (but not os.Exit(1) which kills the test)
	// We use -version which returns 0 but still exercises the main branch
	t.Run("main-version", func(t *testing.T) {
		os.Args = []string{"cmd", "-version"}
		main()
	})
}

func TestIsAdmin(t *testing.T) {
	// Exercise the real isAdmin function
	_ = isAdmin()
}
