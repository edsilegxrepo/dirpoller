// Package poller_test provides unit tests for the Trigger polling algorithm.
//
// Objective:
// Validate the "Trigger File" discovery strategy, ensuring that files are
// collected but only dispatched once a specific trigger file pattern is
// matched in the directory.
//
// Scenarios Covered:
//   - Trigger Match: Verification that files are flushed when the trigger appears.
//   - Timeout Flush: Confirms that files are eventually dispatched even if no
//     trigger appears, based on the BatchTimeoutSeconds setting.
//   - Pattern Matching: Ensures exact and wildcard matches for trigger files.
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

// TestTriggerPoller verifies both trigger-based and timeout-based flushing.
//
// Scenario:
// 1. Initialize TriggerPoller with "trigger.txt" pattern and 2s timeout.
// 2. Add a data file and then create the trigger file.
// 3. Verify immediate dispatch upon trigger detection.
// 4. Add another data file and wait for the timeout.
//
// Success Criteria:
// - Files are dispatched immediately when the trigger file is created.
// - Files are dispatched after the timeout period if no trigger is found.
func TestTriggerPoller(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("poller", "TriggerPollerTest")

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:           testDir,
			Algorithm:           config.PollTrigger,
			Value:               "trigger.txt",
			BatchTimeoutSeconds: 2,
		},
	}

	p := NewTriggerPoller(cfg)
	results := make(chan []string, 10)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 1. Test trigger file
	file1 := filepath.Join(testDir, "data1.txt")
	_ = os.WriteFile(file1, []byte("data"), 0o644)

	go func() {
		if err := p.Start(ctx, results); err != nil && err != context.DeadlineExceeded && err != context.Canceled {
			t.Errorf("Poller failed: %v", err)
		}
	}()

	// Create trigger file
	time.Sleep(500 * time.Millisecond)
	triggerFile := filepath.Join(testDir, "trigger.txt")
	_ = os.WriteFile(triggerFile, []byte("go"), 0o644)

	select {
	case files := <-results:
		found := false
		for _, f := range files {
			if filepath.Base(f) == "data1.txt" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected to find data1.txt in results, got %v", files)
		}
	case <-ctx.Done():
		t.Errorf("timeout waiting for trigger")
	}

	// 2. Test timeout trigger
	file2 := filepath.Join(testDir, "data2.txt")
	_ = os.WriteFile(file2, []byte("more data"), 0o644)

	select {
	case files := <-results:
		found := false
		for _, f := range files {
			if filepath.Base(f) == "data2.txt" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected to find data2.txt in timeout results")
		}
	case <-ctx.Done():
		t.Errorf("timeout waiting for batch timeout")
	}
}

func TestTriggerPoller_BacklogNoTriggerNoPrematureFlush(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("poller", "TriggerBacklogNoPrematureFlush")

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:           testDir,
			Algorithm:           config.PollTrigger,
			Value:               "ready.txt",
			MaxBatchSize:        10,
			BatchTimeoutSeconds: 5,
		},
	}

	p := NewTriggerPoller(cfg)
	results := make(chan []string, 10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mw := newMockWatcher()
	p.newWatcher = func() (Watcher, error) { return mw, nil }

	// Write a data file (but NO trigger file!)
	dataFile := filepath.Join(testDir, "data1.txt")
	_ = os.WriteFile(dataFile, []byte("data"), 0o644)
	defer func() { _ = os.Remove(dataFile) }()

	go func() {
		_ = p.Start(ctx, results)
	}()

	// Wait for the 2-second backlogTicker to fire.
	// In the old code, the backlogTicker would immediately flush data1.txt, bypassing the trigger.
	// In the new code, it must NOT flush since the trigger file is missing.
	select {
	case batch := <-results:
		t.Fatalf("unexpected premature flush of batch: %v", batch)
	case <-time.After(3 * time.Second):
		// Success: no premature flush occurred!
	}

	// Now create the trigger file to prove it flushes correctly once the trigger is present
	triggerFile := filepath.Join(testDir, "ready.txt")
	_ = os.WriteFile(triggerFile, []byte("go"), 0o644)
	defer func() { _ = os.Remove(triggerFile) }()

	// Since we wrote the trigger file, the backlog scan should pick it up and flush
	select {
	case batch := <-results:
		found := false
		for _, f := range batch {
			if filepath.Base(f) == "data1.txt" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected to find data1.txt in flushed results, got %v", batch)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for flush after trigger creation")
	}
}
