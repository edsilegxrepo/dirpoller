// Package service provides the core engine, logging facilities, and platform-specific service management.
//
// Objective:
// Acts as the central orchestration layer for DirPoller. It coordinates the
// high-level data pipeline, manages component lifecycles, and ensures system
// resilience through error recovery and scheduled maintenance tasks.
//
// Core Components:
// - Engine: The primary orchestrator that manages the main processing loop.
// - CustomLogger: Provides dual-track (Process/Activity) file-based auditing.
// - PlatformLogger: Integrates with OS-native logging (EventLog/Syslog).
// - FileVerifier/PostArchiver: Interfaces for integrity and lifecycle management.
//
// Data Flow (The "Main Loop"):
// 1. Polling: The Engine listens for file batches from the Poller.
// 2. Verification: Discovered files are verified for stability and locks in parallel.
// 3. Action: Verified files are processed via the ActionHandler (SFTP/Script).
// 4. Archiving: Successfully processed files are moved or deleted by the Archiver.
// 5. Auditing: Detailed execution summaries are recorded by the CustomLogger.
package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// FileProcessInfo contains metadata and results for a single file processed during a cycle.
type FileProcessInfo struct {
	Path  string // Full path to the local file
	Size  int64  // Size in bytes
	Hash  string // XXH3-128 hex string
	Error string // Error message if the file failed processing
}

// ExecutionSummary aggregates the results of a single polling execution cycle.
type ExecutionSummary struct {
	StartTime time.Time         // When the cycle began
	Processed []FileProcessInfo // Files successfully transferred/processed
	Errors    []FileProcessInfo // Files that failed integrity or action steps
}

// CustomLogger implements the dual-track logging system specified in Section 6.
//
// Objective:
// Provide comprehensive auditing of application operations through two
// distinct log types:
// 1. Process Logs: Daily files tracking system-level events (start, stop, errors).
// 2. Activity Logs: Per-cycle reports detailing every file's processing status.
//
// Data Flow:
// 1. LogProcess: Appends a single event line to the current day's process log.
// 2. LogExecution: Creates a new, unique activity log for a non-empty batch.
// 3. Purging: Automatically deletes logs older than the configured retention period.
type CustomLogger struct {
	mu            sync.Mutex
	logBaseName   string // The base name from config (e.g., C:\Logs\poller.log)
	retention     int    // Number of days to keep logs (0 = disabled)
	lastPurgeDate string // Tracked as YYYYMMDD to ensure purge runs only once per day
}

// NewCustomLogger initializes a new dual-track logger.
func NewCustomLogger(logName string, retention int) *CustomLogger {
	return &CustomLogger{
		logBaseName: logName,
		retention:   retention,
	}
}

// LogProcess logs operational events (start, stop, system errors) to a daily process log.
//
// Objective: Maintain a continuous record of application health and lifecycle events.
//
// Data Flow:
// 1. Retention: Checks if a log purge is required for the new calendar day.
// 2. File Construction: Builds a daily filename: base_process_YYYYMMDD.log.
// 3. Persistent Storage: Appends the message with a high-resolution timestamp to the file.
func (l *CustomLogger) LogProcess(msg string) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Ensure retention runs at most once per calendar day
	l.checkAndPurge()

	// 1. Check for absolute paths for logs
	if l.logBaseName == "" || !filepath.IsAbs(l.logBaseName) {
		return fmt.Errorf("absolute log_name required: %s", l.logBaseName)
	}

	timestamp := time.Now().Format("20060102")
	ext := filepath.Ext(l.logBaseName)
	base := strings.TrimSuffix(l.logBaseName, ext)
	logFileName := fmt.Sprintf("%s_process_%s%s", base, timestamp, ext)

	// Open in append mode, restricted permissions (0600)
	// #nosec G304 - Log file name is constructed from safe base name and timestamp
	f, err := os.OpenFile(filepath.Clean(logFileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("[Logger:LogProcess] failed to open process log file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	now := time.Now().Format("2006-01-02 15:04:05")
	_, err = fmt.Fprintf(f, "%s|%s\n", now, msg)
	return err
}

// LogExecution generates a detailed report for a single polling cycle.
//
// Objective: Provide an audit trail for every file processed, including size and hash verification.
//
// Data Flow:
// 1. Filter: Skips log generation if no files were picked up or errored.
// 2. File Construction: Builds a daily filename: base_activity_YYYYMMDD.log.
// 3. Reporting: Writes categorized sections for Summary, Successes, and Errors.
func (l *CustomLogger) LogExecution(summary ExecutionSummary) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.checkAndPurge()

	// Don't generate empty logs if nothing was found or errored
	if len(summary.Processed) == 0 && len(summary.Errors) == 0 {
		return nil
	}

	// 1. Check for absolute paths for logs
	if l.logBaseName == "" || !filepath.IsAbs(l.logBaseName) {
		return fmt.Errorf("absolute log_name required: %s", l.logBaseName)
	}

	timestamp := time.Now().Format("20060102")
	ext := filepath.Ext(l.logBaseName)
	base := strings.TrimSuffix(l.logBaseName, ext)
	logFileName := fmt.Sprintf("%s_activity_%s%s", base, timestamp, ext)

	// Open or create the daily activity log file in append mode
	// #nosec G304 - Log file name is constructed from safe base name and timestamp
	f, err := os.OpenFile(filepath.Clean(logFileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("[Logger:LogExecution] failed to open activity log file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	// If the file already has content, prepend a blank line to separate cycles
	stat, err := f.Stat()
	if err == nil && stat.Size() > 0 {
		_, _ = fmt.Fprintln(f)
	}

	now := time.Now().Format("2006-01-02 15:04:05")

	// Section 1: Summary Counts
	_, _ = fmt.Fprintln(f, "# Status")
	total := len(summary.Processed) + len(summary.Errors)
	_, _ = fmt.Fprintf(f, "%s|total number of files picked up: %d\n", now, total)
	_, _ = fmt.Fprintf(f, "%s|number of files processed OK: %d\n", now, len(summary.Processed))
	_, _ = fmt.Fprintf(f, "%s|number of files in error: %d\n", now, len(summary.Errors))
	_, _ = fmt.Fprintln(f, "------")

	// Section 2: Successful Files
	// Format: date stamp|path|size|XXH3-128
	_, _ = fmt.Fprintln(f, "# List of files processed successfully")
	for _, info := range summary.Processed {
		_, _ = fmt.Fprintf(f, "%s|%s|%d|%s\n", now, info.Path, info.Size, info.Hash)
	}
	_, _ = fmt.Fprintln(f, "------")

	// Section 3: Error Files
	// Format: date stamp|path in error|size|XXH3-128|error cause
	_, _ = fmt.Fprintln(f, "# List of files in error")
	for _, info := range summary.Errors {
		_, _ = fmt.Fprintf(f, "%s|%s in error|%d|%s|%s\n", now, info.Path, info.Size, info.Hash, info.Error)
	}

	return nil
}

// checkAndPurge determines if the log retention logic should execute.
// It limits purging to once per calendar day to minimize filesystem impact.
func (l *CustomLogger) checkAndPurge() {
	if l.retention <= 0 {
		return
	}

	today := time.Now().Format("20060102")
	if l.lastPurgeDate == today {
		return
	}

	l.purgeOldLogs()
	l.lastPurgeDate = today
}

// purgeOldLogs scans the log directory for files matching the base name prefix
// and deletes those whose modification time is older than the retention period.
func (l *CustomLogger) purgeOldLogs() {
	dir := filepath.Dir(l.logBaseName)
	base := filepath.Base(l.logBaseName)
	ext := filepath.Ext(base)
	prefix := strings.TrimSuffix(base, ext) + "_"
	cutoff := time.Now().AddDate(0, 0, -l.retention)

	filter := func(entry os.DirEntry) bool {
		if entry.IsDir() {
			return false
		}
		name := entry.Name()
		return strings.HasPrefix(name, prefix) &&
			strings.HasSuffix(name, ext) &&
			(strings.Contains(name, "_process_") || strings.Contains(name, "_activity_"))
	}

	_ = purgeOldEntries(dir, cutoff, filter)
}
