//go:build windows

// Package main_test provides unit tests for Windows-specific service orchestration.
//
// Objective:
// Validate the Windows service management logic, including installation,
// removal, and execution under the Service Control Manager (SCM). It ensures
// that administrative privileges are correctly checked and that service
// signals are handled appropriately.
//
// Scenarios Covered:
// - Service Mode: Detection and execution when running as a native Windows service.
// - Installation: Successful service creation and failure paths (e.g., non-admin).
// - Removal: Successful service deletion and failure paths.
// - Privilege Check: Verification of administrative rights for service operations.
package main

import (
	"fmt"
	"testing"

	"criticalsys.net/dirpoller/internal/config"
)

// TestWindowsServiceMocks validates the handleWindowsService orchestration logic using mocks.
//
// Scenario:
// 1. ServiceMode: Simulates running as a native service.
// 2. ServiceError: Handles failures in service detection.
// 3. Install/Remove: Tests elevation requirements and SCM interaction.
// 4. Defaulting: Verifies service name resolution.
//
// Success Criteria:
// - Correct exit codes are returned for each scenario.
// - SCM operations are only attempted when administrative privileges are present.
// - Global state is restored using defer blocks.
func TestWindowsServiceMocks(t *testing.T) {
	// 1. Test isService = true (Service mode)
	t.Run("ServiceMode", func(t *testing.T) {
		oldIsService := isWindowsService
		oldRunService := runService
		isWindowsService = func() (bool, error) { return true, nil }
		runService = func(name, config string, debug bool) {}
		defer func() {
			isWindowsService = oldIsService
			runService = oldRunService
		}()

		cfg := &config.Config{ServiceName: "TestSvc"}
		handled, exitCode := handleWindowsService(cfg, "config.json", false, false, false, "", "")
		if !handled || exitCode != 0 {
			t.Errorf("expected handled=true, exitCode=0; got handled=%v, exitCode=%d", handled, exitCode)
		}
	})

	// 2. Test isService error
	t.Run("ServiceError", func(t *testing.T) {
		oldIsService := isWindowsService
		isWindowsService = func() (bool, error) { return false, fmt.Errorf("mock error") }
		defer func() { isWindowsService = oldIsService }()

		cfg := &config.Config{}
		handled, exitCode := handleWindowsService(cfg, "config.json", false, false, false, "", "")
		if !handled || exitCode != 1 {
			t.Errorf("expected handled=true, exitCode=1; got handled=%v, exitCode=%d", handled, exitCode)
		}
	})

	// 3. Test Install - Not Admin
	t.Run("Install_NotAdmin", func(t *testing.T) {
		oldIsService := isWindowsService
		oldIsAdmin := isAdminFunc
		isWindowsService = func() (bool, error) { return false, nil }
		isAdminFunc = func() bool { return false }
		defer func() {
			isWindowsService = oldIsService
			isAdminFunc = oldIsAdmin
		}()

		cfg := &config.Config{ServiceName: "TestSvc"}
		handled, exitCode := handleWindowsService(cfg, "config.json", false, true, false, "", "")
		if !handled || exitCode != 1 {
			t.Errorf("expected handled=true, exitCode=1; got handled=%v, exitCode=%d", handled, exitCode)
		}
	})

	// 4. Test Install - Admin - Success
	t.Run("Install_Admin_Success", func(t *testing.T) {
		oldIsService := isWindowsService
		oldIsAdmin := isAdminFunc
		oldInstall := installService
		isWindowsService = func() (bool, error) { return false, nil }
		isAdminFunc = func() bool { return true }
		installService = func(name, display, config, user, pass string) error { return nil }
		defer func() {
			isWindowsService = oldIsService
			isAdminFunc = oldIsAdmin
			installService = oldInstall
		}()

		cfg := &config.Config{ServiceName: "TestSvc"}
		handled, exitCode := handleWindowsService(cfg, "config.json", false, true, false, "", "")
		if !handled || exitCode != 0 {
			t.Errorf("expected handled=true, exitCode=0; got handled=%v, exitCode=%d", handled, exitCode)
		}
	})

	// 5. Test Install - Admin - Success (Default Name)
	t.Run("Install_Admin_Success_DefaultName", func(t *testing.T) {
		oldIsService := isWindowsService
		oldIsAdmin := isAdminFunc
		oldInstall := installService
		isWindowsService = func() (bool, error) { return false, nil }
		isAdminFunc = func() bool { return true }
		installService = func(name, display, config, user, pass string) error { return nil }
		defer func() {
			isWindowsService = oldIsService
			isAdminFunc = oldIsAdmin
			installService = oldInstall
		}()

		cfg := &config.Config{ServiceName: "DirPoller"}
		handled, exitCode := handleWindowsService(cfg, "config.json", false, true, false, "", "")
		if !handled || exitCode != 0 {
			t.Errorf("expected handled=true, exitCode=0; got handled=%v, exitCode=%d", handled, exitCode)
		}
	})

	// 6. Test Install - Admin - Error
	t.Run("Install_Admin_Error", func(t *testing.T) {
		oldIsService := isWindowsService
		oldIsAdmin := isAdminFunc
		oldInstall := installService
		isWindowsService = func() (bool, error) { return false, nil }
		isAdminFunc = func() bool { return true }
		installService = func(name, display, config, user, pass string) error { return fmt.Errorf("install fail") }
		defer func() {
			isWindowsService = oldIsService
			isAdminFunc = oldIsAdmin
			installService = oldInstall
		}()

		cfg := &config.Config{ServiceName: "TestSvc"}
		handled, exitCode := handleWindowsService(cfg, "config.json", false, true, false, "", "")
		if !handled || exitCode != 1 {
			t.Errorf("expected handled=true, exitCode=1; got handled=%v, exitCode=%d", handled, exitCode)
		}
	})

	// 7. Test Remove - Not Admin
	t.Run("Remove_NotAdmin", func(t *testing.T) {
		oldIsService := isWindowsService
		oldIsAdmin := isAdminFunc
		isWindowsService = func() (bool, error) { return false, nil }
		isAdminFunc = func() bool { return false }
		defer func() {
			isWindowsService = oldIsService
			isAdminFunc = oldIsAdmin
		}()

		cfg := &config.Config{ServiceName: "TestSvc"}
		handled, exitCode := handleWindowsService(cfg, "config.json", false, false, true, "", "")
		if !handled || exitCode != 1 {
			t.Errorf("expected handled=true, exitCode=1; got handled=%v, exitCode=%d", handled, exitCode)
		}
	})

	// 8. Test Remove - Admin - Success
	t.Run("Remove_Admin_Success", func(t *testing.T) {
		oldIsService := isWindowsService
		oldIsAdmin := isAdminFunc
		oldRemove := removeService
		isWindowsService = func() (bool, error) { return false, nil }
		isAdminFunc = func() bool { return true }
		removeService = func(name string) error { return nil }
		defer func() {
			isWindowsService = oldIsService
			isAdminFunc = oldIsAdmin
			removeService = oldRemove
		}()

		cfg := &config.Config{ServiceName: "TestSvc"}
		handled, exitCode := handleWindowsService(cfg, "config.json", false, false, true, "", "")
		if !handled || exitCode != 0 {
			t.Errorf("expected handled=true, exitCode=0; got handled=%v, exitCode=%d", handled, exitCode)
		}
	})

	// 9. Test Remove - Admin - Error
	t.Run("Remove_Admin_Error", func(t *testing.T) {
		oldIsService := isWindowsService
		oldIsAdmin := isAdminFunc
		oldRemove := removeService
		isWindowsService = func() (bool, error) { return false, nil }
		isAdminFunc = func() bool { return true }
		removeService = func(name string) error { return fmt.Errorf("remove fail") }
		defer func() {
			isWindowsService = oldIsService
			isAdminFunc = oldIsAdmin
			removeService = oldRemove
		}()

		cfg := &config.Config{ServiceName: "TestSvc"}
		handled, exitCode := handleWindowsService(cfg, "config.json", false, false, true, "", "")
		if !handled || exitCode != 1 {
			t.Errorf("expected handled=true, exitCode=1; got handled=%v, exitCode=%d", handled, exitCode)
		}
	})

	// 10. Test No flags
	t.Run("NoFlags", func(t *testing.T) {
		oldIsService := isWindowsService
		isWindowsService = func() (bool, error) { return false, nil }
		defer func() { isWindowsService = oldIsService }()

		cfg := &config.Config{}
		handled, exitCode := handleWindowsService(cfg, "config.json", false, false, false, "", "")
		if handled || exitCode != 0 {
			t.Errorf("expected handled=false, exitCode=0; got handled=%v, exitCode=%d", handled, exitCode)
		}
	})
}

func TestWindowsIsAdmin(t *testing.T) {
	// We can't easily mock windows.SID or Token, but we can verify it runs without crashing.
	_ = isAdmin()
}
