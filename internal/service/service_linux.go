//go:build linux

// Package service (Linux) provides Linux-specific service management logic.
//
// Objective:
// Implement systemd unit management and privilege escalation helpers for Linux.
// It handles the creation of parameterized service units (unit@instance) and
// ensures the application has the necessary rights for system-level changes.
//
// Data Flow:
// 1. Privilege Check: UID 0 (root) or sudo capability checks.
// 2. Unit Installation: Reads template, performs substitutions, and writes to /etc/systemd/system/.
// 3. Lifecycle: Enables and starts/stops service instances via systemctl.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// CheckRoot verifies if the current process has root privileges (UID 0).
func CheckRoot() bool {
	return os.Getuid() == 0
}

// CanSudo checks if the 'sudo' command is available and the user has permissions.
var CanSudo = func() bool {
	if _, err := exec.LookPath("sudo"); err != nil {
		return false
	}

	// Execute sudo -l -n to check permissions without prompting for password
	// We use -n (non-interactive) to avoid hanging if a password is required.
	cmd := exec.Command("sudo", "-l", "-n")
	output, _ := cmd.CombinedOutput()
	outStr := string(output)

	// User spec: check if output contains "(root) ..... ALL" or "(ALL) ..... ALL"
	// This usually looks like: (root) ALL, (ALL) ALL, or (ALL : ALL) ALL
	lowerOut := strings.ToLower(outStr)
	if (strings.Contains(lowerOut, "(root)") || strings.Contains(lowerOut, "(all)")) && strings.Contains(lowerOut, "all") {
		return true
	}

	// Fallback for systems where sudo -n -l might fail but sudo is still functional
	// If the binary exists, we'll try to use it and let the actual command fail if unauthorized.
	_, err := exec.LookPath("sudo")
	return err == nil
}

// runCommand is used to mock exec.Command in tests.
var runCommandFunc = runCommand

// runPrivilegedCommand runs a command with sudo if not already root.
func runPrivilegedCommand(name string, args ...string) error {
	if CheckRoot() {
		return runCommandFunc(name, args...)
	}
	if !CanSudo() {
		return fmt.Errorf("administrative privileges required, but 'sudo' is not available")
	}
	sudoArgs := append([]string{name}, args...)
	return runCommandFunc("sudo", sudoArgs...)
}

// writePrivilegedFile writes a file to a protected location using sudo tee if not root.
func writePrivilegedFile(path string, content []byte, mode os.FileMode) error {
	cleanPath := filepath.Clean(path)
	if CheckRoot() {
		// #nosec G304
		// #nosec G703
		return os.WriteFile(cleanPath, content, mode)
	}
	if !CanSudo() {
		return fmt.Errorf("administrative privileges required to write %s, but 'sudo' is not available", cleanPath)
	}

	// Use sudo tee to write to the protected path
	// #nosec G204 - systemd unit path is controlled by installer logic
	return runPrivilegedCommandWithStdin("sudo", []string{"tee", cleanPath}, string(content))
}

// runPrivilegedCommandWithStdin is a helper for commands that need stdin.
// #nosec G204 - command execution is controlled by the installer logic
var runPrivilegedCommandWithStdin = func(name string, args []string, stdin string) error {
	// #nosec G204 - command execution is controlled by the installer logic
	// nosemgrep
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("command %s %v failed: %w (output: %s)", name, args, err, string(output))
	}
	return nil
}

// removePrivilegedFile removes a file from a protected location using sudo.
func removePrivilegedFile(path string) error {
	if CheckRoot() {
		return os.Remove(path)
	}
	return runPrivilegedCommand("rm", "-f", path)
}

// InstallServiceLinux installs the systemd template unit and enables a specific instance.
//
// Objective: Register the application as a systemd service using a parameterized unit file.
//
// Data Flow:
// 1. Parsing: Extracts unit and instance names from the provided service name.
// 2. Template Loading: Locates and reads the 'dirpoller.service' template file.
// 3. Substitution: Replaces placeholders (User, Group, ExecStart) with absolute paths and config values.
// 4. Persistence: Writes the final unit file to /etc/systemd/system/ using root/sudo.
// 5. Activation: Reloads systemd and enables the specific service instance.
func InstallServiceLinux(name, userGroup string) error {
	unitName, instanceName, err := parseServiceName(name)
	if err != nil {
		return err
	}

	// 1. Read template
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to get executable path: %w", err)
	}

	// Check relative to binary first, then repo root fallback for development
	templatePath := filepath.Join(filepath.Dir(exePath), "dirpoller.service")
	if _, err := os.Stat(templatePath); err != nil {
		repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(exePath)))
		templatePath = filepath.Join(repoRoot, "dirpoller.service")
		if _, err := os.Stat(templatePath); err != nil {
			templatePath = "dirpoller.service" // Final fallback to CWD
		}
	}

	// #nosec G304 - template path resolution is controlled by the installer
	content, err := os.ReadFile(filepath.Clean(templatePath))
	if err != nil {
		return fmt.Errorf("failed to read service template from %s: %w", templatePath, err)
	}

	// 2. Perform substitutions
	processed := string(content)
	user := "dirpoller"
	if userGroup != "" {
		parts := strings.Split(userGroup, ":")
		user = parts[0]
		group := ""
		if len(parts) > 1 {
			group = parts[1]
		}

		processed = replaceLine(processed, "User=", "User="+user)
		if group != "" {
			processed = replaceLine(processed, "Group=", "Group="+group)
		}
	}

	// Update HOME environment variable based on user
	homeDir := "/home/" + user
	if user == "root" {
		homeDir = "/root"
	}
	processed = replaceLine(processed, "Environment=HOME=", "Environment=HOME="+homeDir)

	// Always use the standard ExecStart for parameterized units
	binaryPath := "/usr/local/bin/dirpoller"
	configPath := "/etc/dirpoller/%i.json"
	execStart := fmt.Sprintf("ExecStart=%s -config %s", binaryPath, configPath)
	processed = replaceLine(processed, "ExecStart=", execStart)

	// 3. Write template to systemd
	targetTemplate := fmt.Sprintf("/etc/systemd/system/%s@.service", unitName)
	if _, err := os.Stat(targetTemplate); err == nil {
		fmt.Printf("Warning: Template unit %s already exists, overwriting...\n", targetTemplate)
	}

	if err := writePrivilegedFile(targetTemplate, []byte(processed), 0o644); err != nil {
		return fmt.Errorf("failed to write template unit: %w", err)
	}

	// 4. Systemd integration
	if err := runPrivilegedCommand("systemctl", "daemon-reload"); err != nil {
		return err
	}

	fullInstance := fmt.Sprintf("%s@%s", unitName, instanceName)
	if err := runPrivilegedCommand("systemctl", "enable", fullInstance); err != nil {
		return fmt.Errorf("failed to enable service instance %s: %w", fullInstance, err)
	}

	fmt.Printf("Service template %s@.service installed and instance %s enabled.\n", unitName, fullInstance)
	return nil
}

// RemoveServiceLinux disables the instance and removes the template if requested.
func RemoveServiceLinux(name string) error {
	unitName, instanceName, err := parseServiceName(name)
	if err != nil {
		return err
	}

	fullInstance := fmt.Sprintf("%s@%s", unitName, instanceName)

	// 1. Stop and Disable instance
	_ = runPrivilegedCommand("systemctl", "stop", fullInstance)
	if err := runPrivilegedCommand("systemctl", "disable", fullInstance); err != nil {
		return fmt.Errorf("failed to disable service instance %s: %w", fullInstance, err)
	}

	// 2. Reload daemon
	_ = runPrivilegedCommand("systemctl", "daemon-reload")

	// 3. Remove template unit
	targetTemplate := fmt.Sprintf("/etc/systemd/system/%s@.service", unitName)
	if _, err := os.Stat(targetTemplate); err == nil {
		if err := removePrivilegedFile(targetTemplate); err != nil {
			return fmt.Errorf("failed to remove template unit %s: %w", targetTemplate, err)
		}
		fmt.Printf("Template unit %s removed.\n", targetTemplate)
	}

	fmt.Printf("Service instance %s disabled and stopped.\n", fullInstance)
	return nil
}

func parseServiceName(name string) (unit, instance string, err error) {
	parts := strings.Split(name, "@")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid service name format. Expected unit@config (e.g. dirpoller@siteA)")
	}
	return parts[0], parts[1], nil
}

func replaceLine(content, prefix, newLine string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), prefix) {
			lines[i] = newLine
		}
	}
	return strings.Join(lines, "\n")
}

func runCommand(name string, args ...string) error {
	// #nosec G204 - system management commands are controlled by the installer logic
	// nosemgrep
	cmd := exec.Command(name, args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("command %s %v failed: %w (output: %s)", name, args, err, string(output))
	}
	return nil
}
