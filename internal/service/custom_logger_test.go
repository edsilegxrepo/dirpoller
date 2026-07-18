// Package service_test provides unit tests for the CustomLogger.
//
// Objective:
// Validate the dual-track file-based logging system, ensuring that process
// events and per-execution activity reports are correctly formatted,
// persisted, and automatically purged based on retention policies.
//
// Scenarios Covered:
// - Activity Logging: Verification of JSON-like summary and file list formatting.
// - Process Logging: Verification of daily process log creation and appending.
// - Retention: Confirms that logs older than N days are correctly identified and deleted.
// - Error Handling: Graceful behavior when log directories are inaccessible.
package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"criticalsys.net/dirpoller/internal/testutils"
)

// TestCustomLogger_LogExecution verifies the formatting and creation of daily activity logs.
//
// Scenario:
// 1. Initialize CustomLogger with a temporary test directory.
// 2. Log a first execution summary containing both processed and failed files.
// 3. Log a second execution summary containing processed files.
// 4. Inspect the resulting daily activity log file content.
//
// Success Criteria:
// - Only one file must be created, named with the correct "activity" prefix and YYYYMMDD suffix.
// - Content must append both execution cycles.
// - Content must include all metadata (path, size, hash) for each file.
// - Sections (# Status, # List of files...) must be correctly delimited.
func TestCustomLogger_LogExecution(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("service", "logger_exec")

	logName := filepath.Join(testDir, "test.log")
	logger := NewCustomLogger(logName, 0)

	summary1 := ExecutionSummary{
		StartTime: time.Now(),
		Processed: []FileProcessInfo{
			{Path: "file1.txt", Size: 100, Hash: "a1b2c3d4e5f6g7h8a1b2c3d4e5f6g7h8"},
		},
		Errors: []FileProcessInfo{
			{Path: "file2.txt", Size: 200, Hash: "b2c3d4e5f6g7h8i9b2c3d4e5f6g7h8i9", Error: "some error"},
		},
	}

	if err := logger.LogExecution(summary1); err != nil {
		t.Fatalf("LogExecution failed: %v", err)
	}

	summary2 := ExecutionSummary{
		StartTime: time.Now(),
		Processed: []FileProcessInfo{
			{Path: "file3.txt", Size: 300, Hash: "c3d4e5f6g7h8i9j0c3d4e5f6g7h8i9j0"},
		},
	}

	if err := logger.LogExecution(summary2); err != nil {
		t.Fatalf("LogExecution second cycle failed: %v", err)
	}

	// Check if activity log was created
	files, _ := os.ReadDir(testDir)
	found := false
	var activityFileName string
	for _, f := range files {
		if strings.Contains(f.Name(), "test_activity_") {
			if found {
				t.Errorf("expected only one activity log file, but found multiple: %s and %s", activityFileName, f.Name())
			}
			found = true
			activityFileName = f.Name()

			content, _ := os.ReadFile(filepath.Join(testDir, f.Name()))
			sContent := string(content)
			if !strings.Contains(sContent, "# Status") {
				t.Errorf("log missing # Status section")
			}
			if !strings.Contains(sContent, "file1.txt|100|a1b2c3d4e5f6g7h8a1b2c3d4e5f6g7h8") {
				t.Errorf("log missing first cycle processed file info")
			}
			if !strings.Contains(sContent, "file2.txt in error|200|b2c3d4e5f6g7h8i9b2c3d4e5f6g7h8i9|some error") {
				t.Errorf("log missing first cycle error file info or incorrect format")
			}
			if !strings.Contains(sContent, "file3.txt|300|c3d4e5f6g7h8i9j0c3d4e5f6g7h8i9j0") {
				t.Errorf("log missing second cycle processed file info")
			}

			// Verify name format (only YYYYMMDD, no HHMMSS)
			expectedName := "test_activity_" + time.Now().Format("20060102") + ".log"
			if f.Name() != expectedName {
				t.Errorf("expected activity log file name %s, got %s", expectedName, f.Name())
			}
		}
	}
	if !found {
		t.Error("activity log file not found")
	}
}

func TestCustomLogger_LogProcess(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("service", "logger_process")

	logName := filepath.Join(testDir, "test.log")
	logger := NewCustomLogger(logName, 0)

	if err := logger.LogProcess("System started"); err != nil {
		t.Fatalf("LogProcess failed: %v", err)
	}

	// Check if process log was created
	files, _ := os.ReadDir(testDir)
	found := false
	for _, f := range files {
		if strings.Contains(f.Name(), "test_process_") {
			found = true
			content, _ := os.ReadFile(filepath.Join(testDir, f.Name()))
			if !strings.Contains(string(content), "System started") {
				t.Errorf("log missing process message")
			}
		}
	}
	if !found {
		t.Error("process log file not found")
		t.Errorf("log file was not created")
	}
}

func TestCustomLogger_PurgeOldLogs_ReadDirError(t *testing.T) {
	// Use a directory that doesn't exist to trigger ReadDir error
	logger := NewCustomLogger("Z:\\non_existent_dir_123\\test.log", 1)
	// Manually set lastPurgeDate to force purgeOldLogs execution
	logger.lastPurgeDate = "20000101"

	// This calls checkAndPurge -> purgeOldLogs
	// LogProcess will fail to open the file, but we want to ensure purgeOldLogs returns gracefully
	_ = logger.LogProcess("test")
}

func TestCustomLogger_PurgeOldLogs_SkipDir(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("service", "logger_purge_skipdir")

	logName := filepath.Join(testDir, "test.log")
	logger := NewCustomLogger(logName, 1)
	logger.lastPurgeDate = "20000101"

	// Create a sub-directory that matches the prefix to cover the entry.IsDir() skip
	subDir := filepath.Join(testDir, "test_process_20200101.log")
	_ = os.MkdirAll(subDir, 0o750)

	if err := logger.LogProcess("Triggering purge"); err != nil {
		t.Fatalf("LogProcess failed: %v", err)
	}

	// subDir should still exist
	if _, err := os.Stat(subDir); err != nil {
		t.Errorf("expected subDir to still exist, got %v", err)
	}
}

func TestCustomLogger_PurgeOldLogs(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("service", "logger_purge")

	logName := filepath.Join(testDir, "test.log")
	logger := NewCustomLogger(logName, 2) // 2 days retention

	// Create an old process log file
	oldProcessLog := filepath.Join(testDir, "test_process_20200101.log")
	if err := os.WriteFile(oldProcessLog, []byte("old process"), 0o644); err != nil {
		t.Fatalf("failed to create old process log: %v", err)
	}

	// Create an old activity log file
	oldActivityLog := filepath.Join(testDir, "test_activity_20200101.log")
	if err := os.WriteFile(oldActivityLog, []byte("old activity"), 0o644); err != nil {
		t.Fatalf("failed to create old activity log: %v", err)
	}

	// Set their mod times to 10 days ago
	oldTime := time.Now().AddDate(0, 0, -10)
	_ = os.Chtimes(oldProcessLog, oldTime, oldTime)
	_ = os.Chtimes(oldActivityLog, oldTime, oldTime)

	// Create a recent process log
	recentLog := filepath.Join(testDir, "test_process_"+time.Now().Format("20060102")+".log")
	if err := os.WriteFile(recentLog, []byte("recent"), 0o644); err != nil {
		t.Fatalf("failed to create recent log: %v", err)
	}

	// Trigger purge via LogProcess
	if err := logger.LogProcess("Triggering purge"); err != nil {
		t.Fatalf("LogProcess failed: %v", err)
	}

	// Verify old logs are gone, recent one stays
	if _, err := os.Stat(oldProcessLog); !os.IsNotExist(err) {
		t.Errorf("old process log was not purged")
	}
	if _, err := os.Stat(oldActivityLog); !os.IsNotExist(err) {
		t.Errorf("old activity log was not purged")
	}
	if _, err := os.Stat(recentLog); err != nil {
		t.Errorf("recent log was purged or missing: %v", err)
	}
}
