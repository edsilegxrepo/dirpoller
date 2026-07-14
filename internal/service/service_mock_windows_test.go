//go:build windows

// Package service_test provides mocks and unit tests for Windows service management.
//
// Objective:
// Validate the interaction with the Windows Service Control Manager (SCM)
// using high-fidelity mocks. It ensures that service installation, removal,
// and status queries are handled correctly without requiring actual
// administrative privileges during the test run.
//
// Core Components:
// - mockServiceManager: Simulates the top-level SCM connection.
// - mockManager: Simulates service creation and opening operations.
// - mockService: Simulates control signals (Stop/Pause) and status queries.
//
// Data Flow:
// 1. Setup: Test sets up a chain of mocks (Manager -> Service).
// 2. Action: Calls InstallService or RemoveService which uses the mocked manager.
// 3. Verification: Inspects the mock state to ensure the correct SCM APIs were called.
package service

import (
	"fmt"
	"strings"
	"testing"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

type mockServiceManager struct {
	manager Manager
	connErr error
}

func (m *mockServiceManager) Connect() (Manager, error) {
	if m.connErr != nil {
		return nil, m.connErr
	}
	return m.manager, nil
}

type mockManager struct {
	openErr   error
	createErr error
	service   Service
	closed    bool
}

func (m *mockManager) OpenService(name string) (Service, error) {
	if m.openErr != nil {
		return nil, m.openErr
	}
	return m.service, nil
}

func (m *mockManager) CreateService(name, exepath string, config mgr.Config, args ...string) (Service, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	return m.service, nil
}

func (m *mockManager) Close() error {
	m.closed = true
	return nil
}

type mockService struct {
	controlErr error
	queryErr   error
	deleteErr  error
	status     svc.Status
	closed     bool
}

func (m *mockService) Control(c svc.Cmd) (svc.Status, error) {
	return m.status, m.controlErr
}

func (m *mockService) Query() (svc.Status, error) {
	return m.status, m.queryErr
}

func (m *mockService) Delete() error {
	return m.deleteErr
}

func (m *mockService) Close() error {
	m.closed = true
	return nil
}

// TestInstallRemoveService validates the standard SCM lifecycle operations.
//
// Scenario:
// 1. Install: Simulates a fresh installation where the service does not exist.
// 2. Duplicate Install: Verifies rejection when the service already exists.
// 3. Remove: Simulates stopping and deleting an existing service.
// 4. Missing Remove: Verifies error handling when removing a non-existent service.
//
// Success Criteria:
// - Services are created/deleted only when appropriate.
// - Duplicate installations are correctly blocked.
// - SCM errors are propagated back to the caller.
func TestInstallRemoveService(t *testing.T) {
	// Mock EventLog functions
	oldInstall := eventLogInstall
	oldRemove := eventLogRemove
	eventLogInstall = func(name string, levels uint32) error { return nil }
	eventLogRemove = func(name string) error { return nil }
	defer func() {
		eventLogInstall = oldInstall
		eventLogRemove = oldRemove
	}()

	ms := &mockService{}
	mm := &mockManager{openErr: fmt.Errorf("not found"), service: ms}
	oldManager := defaultManager
	defaultManager = &mockServiceManager{manager: mm}
	defer func() { defaultManager = oldManager }()

	name := "TestPoller"
	display := "Test Poller Service"
	cfgPath := "C:\\config.json"

	// 1. Test Install
	// Initial open fails (service doesn't exist), so we proceed to create
	mm.openErr = fmt.Errorf("not found")
	mm.createErr = nil
	err := InstallService(name, display, cfgPath, "", "")
	if err != nil {
		t.Fatalf("InstallService failed: %v", err)
	}

	// 2. Test Install Duplicate
	// Service exists now
	mm.openErr = nil
	err = InstallService(name, display, cfgPath, "", "")
	if err == nil {
		t.Error("expected error installing duplicate service, got nil")
	}

	// 3. Test Remove
	mm.openErr = nil
	ms.status = svc.Status{State: svc.Stopped}
	err = RemoveService(name)
	if err != nil {
		t.Fatalf("RemoveService failed: %v", err)
	}

	// 4. Test Remove Non-existent
	mm.openErr = fmt.Errorf("not found")
	err = RemoveService("NoSuchService")
	if err == nil {
		t.Error("expected error removing non-existent service, got nil")
	}

	t.Run("DeleteFail", func(t *testing.T) {
		mm.openErr = nil
		ms.deleteErr = fmt.Errorf("del fail")
		ms.status = svc.Status{State: svc.Stopped} // Set to Stopped to avoid 10s wait
		err := RemoveService(name)
		if err == nil || !strings.Contains(err.Error(), "del fail") {
			t.Errorf("expected del fail, got %v", err)
		}
	})

	t.Run("EventLogRemoveFail", func(t *testing.T) {
		mm.openErr = nil
		ms.deleteErr = nil
		ms.status = svc.Status{State: svc.Stopped} // Set to Stopped to avoid 10s wait
		oldRemove := eventLogRemove
		eventLogRemove = func(name string) error { return fmt.Errorf("evt rem fail") }
		defer func() { eventLogRemove = oldRemove }()
		err := RemoveService(name)
		if err == nil || !strings.Contains(err.Error(), "evt rem fail") {
			t.Errorf("expected evt rem fail, got %v", err)
		}
	})
}

func TestInstallServiceErrors(t *testing.T) {
	oldManager := defaultManager
	ms := &mockService{}
	mm := &mockManager{service: ms}
	defaultManager = &mockServiceManager{manager: mm}
	defer func() { defaultManager = oldManager }()

	name := "ErrorPoller"

	t.Run("AlreadyExists", func(t *testing.T) {
		mm.openErr = nil
		err := InstallService(name, "Display", "C:\\cfg.json", "", "")
		if err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Errorf("expected already exists error, got %v", err)
		}
	})

	t.Run("EventLogFail", func(t *testing.T) {
		oldInstall := eventLogInstall
		eventLogInstall = func(name string, levels uint32) error { return fmt.Errorf("evt error") }
		defer func() { eventLogInstall = oldInstall }()

		mm.openErr = fmt.Errorf("not found")
		mm.createErr = nil
		err := InstallService(name, "Display", "C:\\cfg.json", "", "")
		if err == nil || !strings.Contains(err.Error(), "InstallAsEventCreate() failed") {
			t.Errorf("expected event log failure error, got %v", err)
		}
	})
}
