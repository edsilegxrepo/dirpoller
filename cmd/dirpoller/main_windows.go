//go:build windows

// Package main (Windows) provides Windows-specific service orchestration logic.
//
// Objective:
// Implement the platform-specific glue for service installation, removal,
// and execution on Windows systems using the Service Control Manager (SCM).
//
// Data Flow:
// 1. handleWindowsService: Dispatches to Windows-specific installers or runners.
// 2. SCM Check: Determines if the process is running as a service or a standalone CLI.
// 3. Privilege Check: Verifies Administrator rights for service installation/removal.
package main

import (
	"fmt"
	"log"

	"criticalsys.net/dirpoller/internal/config"
	"criticalsys.net/dirpoller/internal/service"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

var (
	// isWindowsService is used to mock svc.IsWindowsService in tests.
	isWindowsService = svc.IsWindowsService
	// runService is used to mock service.RunService in tests.
	runService = service.RunService
	// installService is used to mock service.InstallService in tests.
	installService = service.InstallService
	// removeService is used to mock service.RemoveService in tests.
	removeService = service.RemoveService
	// isAdminFunc is used to mock the admin check in tests.
	isAdminFunc = isAdmin
)

// handleWindowsService manages Windows-specific service lifecycle and execution.
//
// Data Flow:
// 1. SCM Detection: Checks if the current process is running as a Windows Service.
// 2. Service Execution: If running as a service, starts the engine via service.RunService.
// 3. Installation: If -install flag is set, creates a new Windows Service.
// 4. Removal: If -remove flag is set, deletes an existing Windows Service.
func handleWindowsService(cfg *config.Config, absConfigPath string, debug bool, install bool, remove bool, user string, pass string) (bool, int) {
	// Check if running as a service
	isService, err := isWindowsService()
	if err != nil {
		log.Printf("Failed to determine if running as service: %v", err)
		return true, 1
	}

	if isService {
		runService(cfg.ServiceName, absConfigPath, debug)
		return true, 0
	}

	if install {
		if !isAdminFunc() {
			log.Println("Administrative privileges are required to install the service. Please run as Administrator.")
			return true, 1
		}
		displayName := fmt.Sprintf("Directory Poller (%s)", cfg.ServiceName)
		if cfg.ServiceName == "DirPoller" {
			displayName = "Directory Poller"
		}
		err = installService(cfg.ServiceName, displayName, absConfigPath, user, pass)
		if err != nil {
			log.Printf("Failed to install service: %v", err)
			return true, 1
		}
		fmt.Printf("Service '%s' installed successfully.\n", cfg.ServiceName)
		return true, 0
	}

	if remove {
		if !isAdminFunc() {
			log.Println("Administrative privileges are required to remove the service. Please run as Administrator.")
			return true, 1
		}
		err = removeService(cfg.ServiceName)
		if err != nil {
			log.Printf("Failed to remove service: %v", err)
			return true, 1
		}
		fmt.Printf("Service '%s' removed successfully.\n", cfg.ServiceName)
		return true, 0
	}

	return false, 0
}

// isAdmin checks if the current process is running with administrative privileges.
// This is required for installing or removing Windows services and EventLog sources.
func isAdmin() bool {
	var sid *windows.SID
	if err := windows.AllocateAndInitializeSid(
		&windows.SECURITY_NT_AUTHORITY, 2,
		windows.SECURITY_BUILTIN_DOMAIN_RID, windows.DOMAIN_ALIAS_RID_ADMINS,
		0, 0, 0, 0, 0, 0, &sid,
	); err != nil {
		return false
	}
	defer func() {
		_ = windows.FreeSid(sid)
	}()
	member, err := windows.Token(0).IsMember(sid)
	return err == nil && member
}
