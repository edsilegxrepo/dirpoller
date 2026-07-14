//go:build linux

// Package service_test provides unit tests for the Linux-specific service installer.
//
// Objective:
// Validate the systemd unit installation and removal logic on Linux. It
// ensures that the installer correctly handles administrative privileges
// (sudo), parses service templates, performs string substitutions for users
// and paths, and interacts with systemctl for service management.
//
// Scenarios Covered:
//   - Privilege Enforcement: Verifies that root/sudo rights are required for installation.
//   - Unit Parsing: Tests the logic for replacing placeholders in systemd templates.
//   - Name Parsing: Validates the instance-naming convention (unit@instance).
//   - Systemd Interaction: Mocks privileged commands (tee, systemctl) to verify
//     the full install/remove workflow.
//   - User/Group Substitution: Confirms that custom users/groups are correctly
//     injected into the service unit.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupTestUnit creates a dummy unit template for testing.
func _() {
	_ = setupTestUnit
	_ = execSudo
}

func setupTestUnit(t *testing.T) string {
	content := `[Unit]
Description=Test Service
After=network.target

[Service]
Type=simple
User=dirpoller
Group=users
ExecStart=/usr/local/bin/dirpoller -config /etc/dirpoller/config.json

[Install]
WantedBy=multi-user.target
`
	tmpFile := filepath.Join(t.TempDir(), "dirpoller.service")
	if err := os.WriteFile(tmpFile, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to create test unit template: %v", err)
	}
	return tmpFile
}

// execSudo runs a command with sudo.
func execSudo(name string, args ...string) ([]byte, error) {
	fullArgs := append([]string{name}, args...)
	cmd := exec.Command("sudo", fullArgs...)
	return cmd.CombinedOutput()
}

// TestLinuxInstaller_RootPrivileges verifies that the installer enforces administrative rights.
//
// Scenario:
// 1. Mock CanSudo to return false.
// 2. Attempt to install the service.
//
// Success Criteria:
// - The installer must return an error indicating missing privileges.
func TestLinuxInstaller_RootPrivileges(t *testing.T) {
	// This test assumes it's NOT running as root to verify the check.
	// If it IS running as root (e.g. CI), we skip this specific check.
	if CheckRoot() {
		t.Skip("Running as root, cannot verify privilege enforcement")
	}

	// Create a dummy dirpoller.service in the current directory so os.Stat succeeds
	_ = os.WriteFile("dirpoller.service", []byte("[Unit]\nDescription=Test"), 0o644)
	defer func() { _ = os.Remove("dirpoller.service") }()

	// Mock CanSudo to return false to simulate lack of privileges
	oldCanSudo := CanSudo
	CanSudo = func() bool { return false }
	defer func() { CanSudo = oldCanSudo }()

	err := InstallServiceLinux("test@config", "user:group")
	if err == nil || !strings.Contains(err.Error(), "administrative privileges") {
		t.Errorf("expected root privilege error, got %v", err)
	}
}

// TestLinuxInstaller_Lifecycle validates the parsing and orchestration logic of the Linux installer.
//
// Scenario:
// 1. TemplateParsing: Verifies string replacement logic for User/Group/ExecStart.
// 2. NameParsing: Verifies unit name and instance ID extraction.
// 3. Install/Remove: Mocks system commands to verify the high-level install/remove workflow.
//
// Success Criteria:
// - Placeholders in the systemd unit must be replaced correctly.
// - Valid service names must be parsed without error.
// - The installer must correctly sequence systemctl commands (daemon-reload, enable, stop, etc.).
func TestLinuxInstaller_Lifecycle(t *testing.T) {
	// 1. Template Parsing
	t.Run("TemplateParsing", func(t *testing.T) {
		content := `User=dirpoller
Group=users
ExecStart=/usr/local/bin/dirpoller -config /etc/dirpoller/config.json`

		processed := replaceLine(content, "User=", "User=testuser")
		processed = replaceLine(processed, "Group=", "Group=testgroup")

		binaryPath := "/usr/local/bin/dirpoller"
		configPath := "/etc/dirpoller/%i.json"
		execStart := fmt.Sprintf("ExecStart=%s -config %s", binaryPath, configPath)
		processed = replaceLine(processed, "ExecStart=", execStart)

		if !strings.Contains(processed, "User=testuser") {
			t.Error("User substitution failed")
		}
		if !strings.Contains(processed, "Group=testgroup") {
			t.Error("Group substitution failed")
		}
		if !strings.Contains(processed, "ExecStart=/usr/local/bin/dirpoller -config /etc/dirpoller/%i.json") {
			t.Error("ExecStart substitution failed")
		}
	})

	// 2. Name Parsing
	t.Run("NameParsing", func(t *testing.T) {
		unit, inst, err := parseServiceName("myapp@site1")
		if err != nil {
			t.Fatalf("failed to parse valid name: %v", err)
		}
		if unit != "myapp" || inst != "site1" {
			t.Errorf("wrong parse results: unit=%s, inst=%s", unit, inst)
		}

		_, _, err = parseServiceName("invalid-name")
		if err == nil {
			t.Error("accepted invalid name format")
		}
	})

	// 3. Full Installation Mocked
	t.Run("InstallServiceLinux_Mocked", func(t *testing.T) {
		// Mock dependencies
		oldCanSudo := CanSudo
		oldRunCommand := runCommandFunc
		oldRunPrivilegedWithStdin := runPrivilegedCommandWithStdin

		CanSudo = func() bool { return true }
		runCommandFunc = func(name string, args ...string) error { return nil }
		runPrivilegedCommandWithStdin = func(name string, args []string, stdin string) error { return nil }

		defer func() {
			CanSudo = oldCanSudo
			runCommandFunc = oldRunCommand
			runPrivilegedCommandWithStdin = oldRunPrivilegedWithStdin
		}()

		// Create dummy service file in CWD for the installer to find
		_ = os.WriteFile("dirpoller.service", []byte("[Unit]\nDescription=Test"), 0o644)
		defer func() { _ = os.Remove("dirpoller.service") }()

		err := InstallServiceLinux("dirpoller@config1", "testuser:testgroup")
		if err != nil {
			t.Errorf("expected nil error for mocked installation, got %v", err)
		}
	})

	// 4. Full Removal Mocked
	t.Run("RemoveServiceLinux_Mocked", func(t *testing.T) {
		// Mock dependencies
		oldCanSudo := CanSudo
		oldRunCommand := runCommandFunc

		CanSudo = func() bool { return true }
		runCommandFunc = func(name string, args ...string) error { return nil }

		defer func() {
			CanSudo = oldCanSudo
			runCommandFunc = oldRunCommand
		}()

		err := RemoveServiceLinux("dirpoller@config1")
		if err != nil {
			t.Errorf("expected nil error for mocked removal, got %v", err)
		}
	})

	// 5. removePrivilegedFile
	t.Run("removePrivilegedFile", func(t *testing.T) {
		oldRunCommand := runCommandFunc
		runCommandFunc = func(name string, args ...string) error { return nil }
		defer func() { runCommandFunc = oldRunCommand }()

		err := removePrivilegedFile("/tmp/test")
		if err != nil {
			t.Errorf("expected nil error, got %v", err)
		}
	})

	// 6. runCommand
	t.Run("runCommand", func(t *testing.T) {
		err := runCommand("echo", "test")
		if err != nil {
			t.Errorf("expected nil error, got %v", err)
		}
	})

	// 7. runPrivilegedCommand with CheckRoot fallback
	t.Run("runPrivilegedCommand_Root", func(t *testing.T) {
		// Mock CheckRoot to return true
		// Note: CheckRoot is not a variable, but we can mock the behavior if we had a variable.
		// Since it's not, we just exercise it.
		if CheckRoot() {
			err := runPrivilegedCommand("echo", "test")
			if err != nil {
				t.Errorf("expected nil error, got %v", err)
			}
		}
	})
	// 8. writePrivilegedFile Root
	t.Run("writePrivilegedFile_Root", func(t *testing.T) {
		if CheckRoot() {
			err := writePrivilegedFile(filepath.Join(t.TempDir(), "test"), []byte("test"), 0o644)
			if err != nil {
				t.Errorf("expected nil error, got %v", err)
			}
		}
	})

	// 9. runCommand Error
	t.Run("runCommand_Error", func(t *testing.T) {
		err := runCommand("nonexistentcommand")
		if err == nil {
			t.Error("expected error for nonexistent command, got nil")
		}
	})

	// 11. writePrivilegedFile Error (CanSudo false)
	t.Run("writePrivilegedFile_NoSudo", func(t *testing.T) {
		if !CheckRoot() {
			oldCanSudo := CanSudo
			CanSudo = func() bool { return false }
			defer func() { CanSudo = oldCanSudo }()

			err := writePrivilegedFile("/etc/shadow", []byte("test"), 0o644)
			if err == nil || !strings.Contains(err.Error(), "administrative privileges") {
				t.Errorf("expected sudo privilege error, got %v", err)
			}
		}
	})

	// 12. runPrivilegedCommandWithStdin Error
	t.Run("runPrivilegedCommandWithStdin_Error", func(t *testing.T) {
		err := runPrivilegedCommandWithStdin("nonexistentcommand", []string{"arg"}, "stdin")
		if err == nil {
			t.Error("expected error for nonexistent command, got nil")
		}
	})

	// 13. InstallServiceLinux - Invalid Name
	t.Run("InstallServiceLinux_InvalidName", func(t *testing.T) {
		err := InstallServiceLinux("invalid-name", "")
		if err == nil {
			t.Error("expected error for invalid service name, got nil")
		}
	})

	// 14. InstallServiceLinux - Template Read Error
	t.Run("InstallServiceLinux_TemplateReadError", func(t *testing.T) {
		// Ensure no dirpoller.service exists in current dir
		_ = os.Remove("dirpoller.service")

		err := InstallServiceLinux("unit@instance", "")
		if err == nil {
			t.Error("expected error when template is missing, got nil")
		}
	})
	// 15. RemoveServiceLinux - Invalid Name
	t.Run("RemoveServiceLinux_InvalidName", func(t *testing.T) {
		err := RemoveServiceLinux("invalid-name")
		if err == nil {
			t.Error("expected error for invalid service name, got nil")
		}
	})

	// 16. InstallServiceLinux - Privileged Write Error
	t.Run("InstallServiceLinux_WriteError", func(t *testing.T) {
		oldRunPrivilegedWithStdin := runPrivilegedCommandWithStdin
		runPrivilegedCommandWithStdin = func(name string, args []string, stdin string) error {
			return fmt.Errorf("write error")
		}
		defer func() { runPrivilegedCommandWithStdin = oldRunPrivilegedWithStdin }()

		_ = os.WriteFile("dirpoller.service", []byte("[Unit]\nDescription=Test"), 0o644)
		defer func() { _ = os.Remove("dirpoller.service") }()

		err := InstallServiceLinux("unit@instance", "")
		if err == nil || !strings.Contains(err.Error(), "failed to write template unit") {
			t.Errorf("expected write error, got %v", err)
		}
	})

	// 17. runPrivilegedCommand - No Sudo Error
	t.Run("runPrivilegedCommand_NoSudo", func(t *testing.T) {
		if !CheckRoot() {
			oldCanSudo := CanSudo
			CanSudo = func() bool { return false }
			defer func() { CanSudo = oldCanSudo }()

			err := runPrivilegedCommand("ls")
			if err == nil || !strings.Contains(err.Error(), "sudo' is not available") {
				t.Errorf("expected sudo error, got %v", err)
			}
		}
	})
	// 18. RemoveServiceLinux - Disable Error
	t.Run("RemoveServiceLinux_DisableError", func(t *testing.T) {
		oldRunCommand := runCommandFunc
		runCommandFunc = func(name string, args ...string) error {
			if name == "sudo" && len(args) > 1 && args[1] == "disable" {
				return fmt.Errorf("disable error")
			}
			return nil
		}
		defer func() { runCommandFunc = oldRunCommand }()

		err := RemoveServiceLinux("unit@instance")
		if err == nil || !strings.Contains(err.Error(), "failed to disable service instance") {
			t.Errorf("expected disable error, got %v", err)
		}
	})

	// 19. InstallServiceLinux - Enable Error
	t.Run("InstallServiceLinux_EnableError", func(t *testing.T) {
		oldRunCommand := runCommandFunc
		runCommandFunc = func(name string, args ...string) error {
			if name == "sudo" && len(args) > 1 && args[1] == "enable" {
				return fmt.Errorf("enable error")
			}
			return nil
		}
		defer func() { runCommandFunc = oldRunCommand }()

		_ = os.WriteFile("dirpoller.service", []byte("[Unit]\nDescription=Test"), 0o644)
		defer func() { _ = os.Remove("dirpoller.service") }()

		err := InstallServiceLinux("unit@instance", "")
		if err == nil || !strings.Contains(err.Error(), "failed to enable service instance") {
			t.Errorf("expected enable error, got %v", err)
		}
	})
	// 20. InstallServiceLinux - Daemon Reload Error
	t.Run("InstallServiceLinux_ReloadError", func(t *testing.T) {
		oldRunCommand := runCommandFunc
		runCommandFunc = func(name string, args ...string) error {
			if name == "sudo" && len(args) > 1 && args[1] == "daemon-reload" {
				return fmt.Errorf("reload error")
			}
			return nil
		}
		defer func() { runCommandFunc = oldRunCommand }()

		_ = os.WriteFile("dirpoller.service", []byte("[Unit]\nDescription=Test"), 0o644)
		defer func() { _ = os.Remove("dirpoller.service") }()

		err := InstallServiceLinux("unit@instance", "")
		if err == nil || !strings.Contains(err.Error(), "reload error") {
			t.Errorf("expected reload error, got %v", err)
		}
	})

	// 21. writePrivilegedFile - CanSudo fallback check
	t.Run("writePrivilegedFile_NoSudo_Fallback", func(t *testing.T) {
		if !CheckRoot() {
			oldCanSudo := CanSudo
			CanSudo = func() bool { return false }
			defer func() { CanSudo = oldCanSudo }()

			err := writePrivilegedFile("/tmp/protected", []byte("test"), 0o644)
			if err == nil || !strings.Contains(err.Error(), "administrative privileges") {
				t.Errorf("expected sudo error, got %v", err)
			}
		}
	})
	// 22. RemoveServiceLinux - Stop Error
	t.Run("RemoveServiceLinux_StopError", func(t *testing.T) {
		oldRunCommand := runCommandFunc
		runCommandFunc = func(name string, args ...string) error {
			if name == "sudo" && len(args) > 1 && args[1] == "stop" {
				return fmt.Errorf("stop error")
			}
			return nil
		}
		defer func() { runCommandFunc = oldRunCommand }()

		err := RemoveServiceLinux("unit@instance")
		if err != nil {
			t.Errorf("expected nil error (stop error is ignored), got %v", err)
		}
	})

	// 23. InstallServiceLinux - User/Group substitution
	t.Run("InstallServiceLinux_UserGroup", func(t *testing.T) {
		oldRunCommand := runCommandFunc
		oldRunPrivilegedWithStdin := runPrivilegedCommandWithStdin
		var capturedContent string

		runCommandFunc = func(name string, args ...string) error { return nil }
		runPrivilegedCommandWithStdin = func(name string, args []string, stdin string) error {
			if len(args) > 1 && args[0] == "tee" {
				capturedContent = stdin
			}
			return nil
		}
		defer func() {
			runCommandFunc = oldRunCommand
			runPrivilegedCommandWithStdin = oldRunPrivilegedWithStdin
		}()

		_ = os.WriteFile("dirpoller.service", []byte("User=\nGroup=\nEnvironment=HOME=\nExecStart="), 0o644)
		defer func() { _ = os.Remove("dirpoller.service") }()

		err := InstallServiceLinux("unit@instance", "customuser:customgroup")
		if err != nil {
			t.Fatalf("failed to install: %v", err)
		}

		if !strings.Contains(capturedContent, "User=customuser") {
			t.Error("User substitution failed")
		}
		if !strings.Contains(capturedContent, "Group=customgroup") {
			t.Error("Group substitution failed")
		}
		if !strings.Contains(capturedContent, "Environment=HOME=/home/customuser") {
			t.Error("HOME substitution failed")
		}
	})
	// 24. InstallServiceLinux - User substitution only
	t.Run("InstallServiceLinux_UserOnly", func(t *testing.T) {
		oldRunCommand := runCommandFunc
		oldRunPrivilegedWithStdin := runPrivilegedCommandWithStdin
		var capturedContent string

		runCommandFunc = func(name string, args ...string) error { return nil }
		runPrivilegedCommandWithStdin = func(name string, args []string, stdin string) error {
			if len(args) > 1 && args[0] == "tee" {
				capturedContent = stdin
			}
			return nil
		}
		defer func() {
			runCommandFunc = oldRunCommand
			runPrivilegedCommandWithStdin = oldRunPrivilegedWithStdin
		}()

		_ = os.WriteFile("dirpoller.service", []byte("User=\nEnvironment=HOME=\nExecStart="), 0o644)
		defer func() { _ = os.Remove("dirpoller.service") }()

		err := InstallServiceLinux("unit@instance", "root")
		if err != nil {
			t.Fatalf("failed to install: %v", err)
		}

		if !strings.Contains(capturedContent, "User=root") {
			t.Error("User substitution failed")
		}
		if !strings.Contains(capturedContent, "Environment=HOME=/root") {
			t.Error("HOME substitution failed for root")
		}
	})

	// 25. removePrivilegedFile - Sudo path
	t.Run("removePrivilegedFile_Sudo", func(t *testing.T) {
		if !CheckRoot() {
			oldRunCommand := runCommandFunc
			var capturedName string
			runCommandFunc = func(name string, args ...string) error {
				capturedName = name
				return nil
			}
			defer func() { runCommandFunc = oldRunCommand }()

			err := removePrivilegedFile("/tmp/test")
			if err != nil {
				t.Errorf("expected nil error, got %v", err)
			}
			if capturedName != "sudo" {
				t.Errorf("expected sudo command, got %s", capturedName)
			}
		}
	})
}
