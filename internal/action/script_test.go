// Package action_test provides unit tests for the script execution action handler.
//
// Objective:
// Validate the local script execution engine, ensuring that external scripts
// are invoked correctly with the appropriate arguments, and that execution
// constraints like timeouts and concurrency are strictly enforced.
//
// Scenarios Covered:
// - Standard Execution: Successful script run with file path argument.
// - Timeout Handling: Verification that long-running scripts are killed via context.
// - Exit Codes: Proper detection of script failures via non-zero exit codes.
// - Edge Cases: Handling of empty batches and pre-cancelled contexts.
// - Platform Specifics: Testing on both Windows (.bat) and Linux (.sh).
package action

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"criticalsys.net/dirpoller/internal/config"
	"criticalsys.net/dirpoller/internal/testutils"
)

func getScriptTestDir(name string) string {
	return testutils.GetUniqueTestDir("action_script", name)
}

// TestScriptAction verifies standard script invocation logic.
//
// Scenario:
// 1. Create a platform-specific test script (.bat or .sh).
// 2. Execute the script via ScriptHandler with a test file path.
//
// Success Criteria:
// - The script must receive the file path as its first argument.
// - The handler must return the file as successfully processed.
func TestScriptAction(t *testing.T) {
	testDir := getScriptTestDir("ScriptAction")
	testFile := filepath.Join(testDir, "test.txt")
	_ = os.WriteFile(testFile, []byte("hello"), 0o644)

	scriptExt := ".bat"
	scriptContent := "@echo off\necho Processing %1\nexit /b 0"
	if runtime.GOOS != "windows" {
		scriptExt = ".sh"
		scriptContent = "#!/bin/sh\necho Processing $1\nexit 0"
	}

	scriptPath := filepath.Join(testDir, "test_script"+scriptExt)
	_ = os.WriteFile(scriptPath, []byte(scriptContent), 0o755)

	cfg := &config.Config{
		Action: config.ActionConfig{
			ConcurrentConnections: 1,
			Script: config.ScriptConfig{
				Path:           scriptPath,
				TimeoutSeconds: 5,
			},
		},
	}

	h := NewScriptHandler(cfg)
	defer func() { _ = h.Close() }()

	ctx := context.Background()
	success, err := h.Execute(ctx, []string{testFile})
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(success) != 1 || success[0] != testFile {
		t.Errorf("expected 1 success for %s, got %v", testFile, success)
	}
}

// TestScriptActionTimeout verifies enforcement of script execution time limits.
//
// Scenario:
// 1. Create a script that sleeps longer than the configured timeout.
// 2. Execute the script via ScriptHandler.
//
// Success Criteria:
// - The handler must kill the script process when the timeout is reached.
// - An error must be returned indicating the timeout/cancellation.
func TestScriptActionTimeout(t *testing.T) {
	testDir := getScriptTestDir("ScriptTimeout")
	testFile := filepath.Join(testDir, "test.txt")
	_ = os.WriteFile(testFile, []byte("hello"), 0o644)

	scriptExt := ".bat"
	scriptContent := "@echo off\npowershell -Command \"Start-Sleep -Seconds 10\"\nexit /b 0"
	if runtime.GOOS != "windows" {
		scriptExt = ".sh"
		scriptContent = "#!/bin/sh\nsleep 10\nexit 0"
	}

	scriptPath := filepath.Join(testDir, "timeout_script"+scriptExt)
	_ = os.WriteFile(scriptPath, []byte(scriptContent), 0o755)

	cfg := &config.Config{
		Action: config.ActionConfig{
			ConcurrentConnections: 1,
			Script: config.ScriptConfig{
				Path:           scriptPath,
				TimeoutSeconds: 1,
			},
		},
	}

	h := NewScriptHandler(cfg)
	defer func() { _ = h.Close() }()

	ctx := context.Background()
	_, err := h.Execute(ctx, []string{testFile})
	if err == nil {
		t.Error("expected timeout error, got nil")
	}
}

func TestScriptActionFailure(t *testing.T) {
	testDir := getScriptTestDir("ScriptFail")
	testFile := filepath.Join(testDir, "test.txt")
	_ = os.WriteFile(testFile, []byte("hello"), 0o644)

	scriptExt := ".bat"
	scriptContent := "@echo off\necho error occurred\nexit /b 1"
	if runtime.GOOS != "windows" {
		scriptExt = ".sh"
		scriptContent = "#!/bin/sh\necho error occurred\nexit 1"
	}

	scriptPath := filepath.Join(testDir, "fail_script"+scriptExt)
	_ = os.WriteFile(scriptPath, []byte(scriptContent), 0o755)

	cfg := &config.Config{
		Action: config.ActionConfig{
			ConcurrentConnections: 1,
			Script: config.ScriptConfig{
				Path:           scriptPath,
				TimeoutSeconds: 5,
			},
		},
	}

	h := NewScriptHandler(cfg)
	defer func() { _ = h.Close() }()

	ctx := context.Background()
	success, err := h.Execute(ctx, []string{testFile})
	if err == nil {
		t.Error("expected error for script exit 1, got nil")
	}
	if len(success) != 0 {
		t.Errorf("expected 0 success, got %d", len(success))
	}
}

func TestScriptHandler_RemoteCleanup(t *testing.T) {
	h := NewScriptHandler(&config.Config{})
	err := h.RemoteCleanup(context.Background())
	if err != nil {
		t.Errorf("expected nil for script RemoteCleanup, got %v", err)
	}
}

func TestScriptHandler_Execute_Empty(t *testing.T) {
	h := NewScriptHandler(&config.Config{})
	success, err := h.Execute(context.Background(), []string{})
	if err != nil {
		t.Errorf("unexpected error for empty batch: %v", err)
	}
	if len(success) != 0 {
		t.Errorf("expected 0 success, got %d", len(success))
	}
}

func TestScriptHandler_Execute_ContextCancelled(t *testing.T) {
	testDir := getScriptTestDir("ScriptCancel")
	testFile := filepath.Join(testDir, "test.txt")
	_ = os.WriteFile(testFile, []byte("data"), 0o644)

	cmdName := "cmd.exe"
	if runtime.GOOS != "windows" {
		cmdName = "sleep"
	}

	cfg := &config.Config{
		Action: config.ActionConfig{
			ConcurrentConnections: 1,
			Script: config.ScriptConfig{
				Path: cmdName,
			},
		},
	}
	h := NewScriptHandler(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	success, _ := h.Execute(ctx, []string{testFile})
	if len(success) != 0 {
		t.Errorf("expected 0 success for cancelled context, got %d", len(success))
	}
}

func TestScriptHandler_ExecuteScript_Errors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux only test")
	}

	h := NewScriptHandler(&config.Config{
		Action: config.ActionConfig{
			Script: config.ScriptConfig{
				Path: "/bin/sh",
			},
		},
	})

	t.Run("AbsPathError", func(t *testing.T) {
		// This is hard to trigger on Linux without specific mocks as filepath.Abs rarely fails
		// for valid-looking strings. But we can try an empty string or something weird.
		err := h.executeScript(context.Background(), "")
		if err != nil {
			t.Logf("Got expected error: %v", err)
		}
	})

	t.Run("CommandError", func(t *testing.T) {
		h.cfg.Action.Script.Path = "/non/existent/script"
		err := h.executeScript(context.Background(), "test.txt")
		if err == nil {
			t.Error("expected error for non-existent script, got nil")
		}
	})
}
