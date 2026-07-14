// Package poller_test provides functional and scenario-based tests for the polling engine.
//
// Objective:
// Validate the robust detection of files under various operational strategies
// (Interval, Batch, Event, Trigger) while ensuring strict adherence to
// non-recursive directory constraints.
//
// Scenarios Covered:
// - Interval Polling: Basic time-based discovery.
// - Batch Polling: Threshold-based and timeout-based flushing.
// - Event Polling: Real-time discovery via OS-native file system events.
// - Security: Dynamic subfolder detection and rejection of nested structures.
package poller

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"criticalsys.net/dirpoller/internal/config"
	"criticalsys.net/dirpoller/internal/testutils"
)

// TestIntervalPoller verifies basic time-based file discovery.
//
// Scenario:
// 1. Initialize IntervalPoller with a 1-second interval.
// 2. Create a test file in the monitored directory.
// 3. Start the poller and wait for the file to be emitted.
//
// Success Criteria:
// The poller must detect the file and send its path to the results channel
// within the configured interval.
func TestIntervalPoller(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("poller", "IntervalPoller")

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory: testDir,
			Value:     1, // 1 second interval
		},
	}

	p := NewIntervalPoller(cfg)
	results := make(chan []string, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Create a test file
	testFile := filepath.Join(testDir, "test1.txt")
	_ = os.WriteFile(testFile, []byte("hello"), 0o644)

	go func() {
		if err := p.Start(ctx, results); err != nil && err != context.DeadlineExceeded && err != context.Canceled {
			t.Errorf("Poller failed: %v", err)
		}
	}()

	select {
	case files := <-results:
		if len(files) != 1 || files[0] != testFile {
			t.Errorf("expected %s, got %v", testFile, files)
		}
	case <-ctx.Done():
		t.Errorf("timeout waiting for files")
	}
}

// TestBatchPoller verifies threshold-based and timeout-based batching.
//
// Scenario:
// 1. Initialize BatchPoller with a threshold of 2 files and a 2-second timeout.
// 2. Add 2 files to trigger an immediate threshold-based flush.
// 3. Add 1 file and wait for the timeout-based flush.
//
// Success Criteria:
// - First batch is emitted as soon as the 2nd file is detected.
// - Second batch is emitted only after the timeout occurs.
func TestBatchPoller(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("poller", "BatchPoller")

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:           testDir,
			Value:               2, // Batch size 2
			BatchTimeoutSeconds: 2,
		},
	}

	p := NewBatchPoller(cfg)
	results := make(chan []string, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Test threshold trigger
	file1 := filepath.Join(testDir, "batch1.txt")
	file2 := filepath.Join(testDir, "batch2.txt")
	_ = os.WriteFile(file1, []byte("1"), 0o644)
	_ = os.WriteFile(file2, []byte("2"), 0o644)

	go func() {
		if err := p.Start(ctx, results); err != nil && err != context.DeadlineExceeded && err != context.Canceled {
			t.Errorf("Poller failed: %v", err)
		}
	}()

	select {
	case files := <-results:
		if len(files) < 2 {
			t.Errorf("expected at least 2 files, got %d", len(files))
		}
	case <-ctx.Done():
		t.Errorf("timeout waiting for batch threshold")
	}

	// 2. Test timeout trigger
	file3 := filepath.Join(testDir, "batch3.txt")
	_ = os.WriteFile(file3, []byte("3"), 0o644)

	select {
	case files := <-results:
		found := false
		for _, f := range files {
			if f == file3 {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected to find %s in timeout results", file3)
		}
	case <-ctx.Done():
		t.Errorf("timeout waiting for batch timeout trigger")
	}
}

func TestBatchPollerTimeout(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("poller", "BatchPollerTimeout")
	testFile := filepath.Join(testDir, "test.txt")
	_ = os.WriteFile(testFile, []byte("data"), 0o644)

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:           testDir,
			Algorithm:           config.PollBatch,
			Value:               10, // High threshold
			BatchTimeoutSeconds: 1,  // Short timeout
		},
	}

	p := NewBatchPoller(cfg)
	results := make(chan []string, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	go func() {
		_ = p.Start(ctx, results)
	}()

	select {
	case files := <-results:
		if len(files) != 1 || files[0] != testFile {
			t.Errorf("expected 1 file in result due to timeout, got %v", files)
		}
	case <-ctx.Done():
		t.Error("timeout waiting for batch poller result")
	}
}

// TestEventPoller verifies real-time discovery via OS events.
//
// Scenario:
// 1. Initialize EventPoller and start its watcher.
// 2. Create a file after the watcher is active.
//
// Success Criteria:
// The poller must receive the FS event and immediately emit the file path.
func TestEventPoller(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("poller", "EventPoller")

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory: testDir,
		},
	}

	p := NewEventPoller(cfg)
	results := make(chan []string, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		if err := p.Start(ctx, results); err != nil && err != context.DeadlineExceeded && err != context.Canceled {
			t.Errorf("Poller failed: %v", err)
		}
	}()

	// Wait for watcher to start
	time.Sleep(500 * time.Millisecond)

	// Create a file
	testFile := filepath.Join(testDir, "event1.txt")
	_ = os.WriteFile(testFile, []byte("event"), 0o644)

	select {
	case files := <-results:
		if len(files) == 0 || files[0] != testFile {
			t.Errorf("expected %s, got %v", testFile, files)
		}
	case <-ctx.Done():
		t.Errorf("timeout waiting for event")
	}
}

// TestEventPollerDynamicSubfolder verifies the non-recursive safety constraint at runtime.
//
// Scenario:
// 1. Start EventPoller on an empty directory.
// 2. Create a subfolder while the poller is running.
//
// Success Criteria:
// The poller must detect the subfolder creation and return a fatal
// ErrSubfolderDetected to stop processing.
func TestEventPollerDynamicSubfolder(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("poller", "EventPollerDynamic")

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory: testDir,
		},
	}

	p := NewEventPoller(cfg)
	results := make(chan []string, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- p.Start(ctx, results)
	}()

	// Wait for watcher to start
	time.Sleep(500 * time.Millisecond)

	// Create a subfolder
	subDir := filepath.Join(testDir, "dynamic_sub")
	if err := os.Mkdir(subDir, 0o750); err != nil {
		t.Fatalf("failed to create subfolder: %v", err)
	}

	select {
	case err := <-errChan:
		if err == nil || err.Error() == "" {
			t.Error("expected error for dynamic subfolder detection, got nil")
		}
	case <-time.After(2 * time.Second):
		t.Error("timeout waiting for dynamic subfolder error")
	}
}

func TestPollerSubfolderDetection(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("poller", "SubfolderDetection")

	// Create subfolder
	subDir := filepath.Join(testDir, "sub")
	if err := os.Mkdir(subDir, 0o750); err != nil {
		t.Fatalf("failed to create subfolder: %v", err)
	}

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory: testDir,
			Value:     1,
		},
	}

	// Test IntervalPoller
	p := NewIntervalPoller(cfg)
	if err := p.poll(context.Background(), make(chan []string, 1)); err == nil {
		t.Error("expected error for subfolder in IntervalPoller, got nil")
	}

	// Test EventPoller initial check
	ep := NewEventPoller(cfg)
	if err := ep.Start(context.Background(), make(chan []string, 1)); err == nil {
		t.Error("expected error for subfolder in EventPoller Start, got nil")
	}
}

// TestEventPollerCoalescing verifies that multiple file creation/write events
// arriving in quick succession are coalesced into a single batch.
func TestEventPollerCoalescing(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("poller", "EventPollerCoalesce")

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:    testDir,
			MaxBatchSize: 10,
		},
	}

	p := NewEventPoller(cfg)
	results := make(chan []string, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		if err := p.Start(ctx, results); err != nil && err != context.DeadlineExceeded && err != context.Canceled {
			t.Errorf("Poller failed: %v", err)
		}
	}()

	// Wait for watcher to start
	time.Sleep(500 * time.Millisecond)

	// Create 3 files in quick succession (under 10ms)
	file1 := filepath.Join(testDir, "c1.txt")
	file2 := filepath.Join(testDir, "c2.txt")
	file3 := filepath.Join(testDir, "c3.txt")

	_ = os.WriteFile(file1, []byte("c1"), 0o644)
	_ = os.WriteFile(file2, []byte("c2"), 0o644)
	_ = os.WriteFile(file3, []byte("c3"), 0o644)

	// Since the flush timer is 50ms, we wait for a single batch to arrive
	select {
	case files := <-results:
		// We expect the files to be coalesced into a single slice batch
		if len(files) < 2 {
			t.Errorf("expected coalescing of at least 2 files, got: %v", files)
		}
		t.Logf("Coalesced batch received: %v", files)
	case <-ctx.Done():
		t.Errorf("timeout waiting for coalesced events")
	}
}
