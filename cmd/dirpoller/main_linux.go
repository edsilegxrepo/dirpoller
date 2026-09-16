//go:build linux

// Package main (Linux) provides Linux-specific service orchestration logic.
//
// Objective:
// Implement the platform-specific glue for service installation and removal
// on Linux systems using systemd.
//
// Data Flow:
// 1. handleWindowsService: Dispatches to Linux-specific installers based on flags.
// 2. Privilege Check: Verifies root/sudo access before attempting system changes.
// 3. Service Management: Calls internal/service functions for systemd unit creation/deletion.
package main

import (
	"log"

	"criticalsys.net/dirpoller/internal/config"
	"criticalsys.net/dirpoller/internal/service"
)

var (
	// These variables will be used to mock the service functions in main_linux.go
	installServiceFunc = service.InstallServiceLinux
	removeServiceFunc  = service.RemoveServiceLinux

	// isAdminFunc is used to mock the admin check in tests.
	isAdminFunc = isAdmin
)

// handleWindowsService manages Linux-specific service installation and removal.
// Although named 'handleWindowsService' for cross-platform interface compatibility,
// on Linux it handles systemd unit management.
//
// Data Flow:
// 1. Flag Check: Inspects 'install' and 'remove' flags.
// 2. Privilege Check: Ensures the user has root/sudo rights.
// 3. Validation: Ensures a service name in 'unit@instance' format is provided.
// 4. Execution: Calls service.InstallServiceLinux or service.RemoveServiceLinux.
func handleWindowsService(cfg *config.Config, absConfigPath string, debug, install, remove bool, user, pass string) (bool, int) {
	// For Linux, ServiceName is strictly expected to come from the -name CLI flag (which overrides cfg.ServiceName in main.go)

	if install {
		if !isAdminFunc() {
			log.Println("Administrative privileges (root or sudo) are required to install the service.")
			return true, 1
		}
		// In Linux, the service name is strictly unit@instance and MUST be provided via CLI
		// cfg.ServiceName here contains either the -name flag or is empty (since it's not defaulted on Linux)
		if cfg.ServiceName == "" {
			log.Println("Error: A service name in 'unit@instance' format must be provided via -name for Linux installation.")
			return true, 1
		}
		err := installServiceFunc(cfg.ServiceName, user)
		if err != nil {
			log.Printf("Failed to install Linux service: %v", err)
			return true, 1
		}
		return true, 0
	}

	if remove {
		if !isAdminFunc() {
			log.Println("Administrative privileges (root or sudo) are required to remove the service.")
			return true, 1
		}
		if cfg.ServiceName == "" {
			log.Println("Error: A service name in 'unit@instance' format must be provided via -name for Linux removal.")
			return true, 1
		}
		err := removeServiceFunc(cfg.ServiceName)
		if err != nil {
			log.Printf("Failed to remove Linux service: %v", err)
			return true, 1
		}
		return true, 0
	}

	return false, 0
}

func isAdmin() bool {
	return service.CheckRoot() || service.CanSudo()
}
