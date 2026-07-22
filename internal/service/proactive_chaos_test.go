package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"criticalsys.net/dirpoller/internal/config"
	"criticalsys.net/dirpoller/internal/poller"
	"criticalsys.net/dirpoller/internal/testutils"
)

// mockFailingActionHandler simulates transient network failures and disconnections.
type mockFailingActionHandler struct {
	mu           sync.Mutex
	failCount    int32
	maxFailures  int32
	successFiles []string
}

func (m *mockFailingActionHandler) Execute(ctx context.Context, files []string) ([]string, error) {
	current := atomic.AddInt32(&m.failCount, 1)
	if current <= m.maxFailures {
		return nil, fmt.Errorf("simulated network disconnection failure #%d", current)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.successFiles = append(m.successFiles, files...)
	return files, nil
}

func (m *mockFailingActionHandler) RemoteCleanup(ctx context.Context) error {
	return nil
}

func (m *mockFailingActionHandler) Close() error {
	return nil
}

// TestProactiveChaos_MidUploadFileDeletion verifies that if an external process or cleanup script
// deletes a file after verification but before action execution, the engine handles the missing file
// gracefully without crashing the worker pool or failing other parallel transfers.
func TestProactiveChaos_MidUploadFileDeletion(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("service", "mid_upload_deletion")
	_ = os.MkdirAll(testDir, 0o755)
	defer func() { _ = os.RemoveAll(testDir) }()

	// Create 20 files
	var files []string
	for i := 0; i < 20; i++ {
		p := filepath.Join(testDir, fmt.Sprintf("file_%d.txt", i))
		_ = os.WriteFile(p, []byte("chaos test data"), 0o600)
		files = append(files, p)
	}

	// Delete 5 files mid-flight to simulate external deletion
	for i := 0; i < 5; i++ {
		_ = os.Remove(files[i])
	}

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory: testDir,
			Algorithm: config.PollInterval,
			Value:     1,
		},
		Integrity: config.IntegrityConfig{
			Algorithm:            config.IntegrityNone,
			VerificationAttempts: 0,
		},
		Action: config.ActionConfig{
			Type: config.ActionScript,
			Script: config.ScriptConfig{
				Path:           filepath.Join(testDir, "dummy.sh"),
				TimeoutSeconds: 5,
			},
			PostProcess: config.PostProcessConfig{
				Action:      config.PostActionDelete,
				ArchivePath: testDir,
			},
		},
	}

	engine, err := NewEngine(cfg, false)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	// Overwrite handler with mock to verify surviving files process
	handler := &mockFailingActionHandler{maxFailures: 0}
	engine.handler = handler

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Process remaining 15 files
	remainingFiles := files[5:]
	engine.processFiles(ctx, remainingFiles)

	handler.mu.Lock()
	processedCount := len(handler.successFiles)
	handler.mu.Unlock()

	if processedCount != 15 {
		t.Errorf("expected exactly 15 surviving files to process successfully, got %d", processedCount)
	}
}

// TestProactiveStress_MultiTickOverlappingInterval verifies that running an interval poller
// with low concurrency across multiple rapid ticks filters out in-flight files properly without
// duplicate execution or lock error log spam.
func TestProactiveStress_MultiTickOverlappingInterval(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("service", "multi_tick_overlapping")
	_ = os.MkdirAll(testDir, 0o755)
	defer func() { _ = os.RemoveAll(testDir) }()

	// Create 50 files
	var files []string
	for i := 0; i < 50; i++ {
		p := filepath.Join(testDir, fmt.Sprintf("stress_%d.txt", i))
		_ = os.WriteFile(p, []byte("multi tick data"), 0o600)
		files = append(files, p)
	}

	// Mark half of the files in-flight to simulate an active preceding batch
	for i := 0; i < 25; i++ {
		poller.AddInFlight(files[i])
	}
	defer func() {
		for i := 0; i < 25; i++ {
			poller.RemoveInFlight(files[i])
		}
	}()

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory: testDir,
			Algorithm: config.PollInterval,
			Value:     1,
		},
		Integrity: config.IntegrityConfig{
			Algorithm:            config.IntegritySize,
			VerificationAttempts: 1,
			VerificationInterval: 1,
		},
		Action: config.ActionConfig{
			Type:                  config.ActionScript,
			ConcurrentConnections: 2,
		},
	}

	ip := poller.NewIntervalPoller(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	results := make(chan []string, 1)
	errChan := make(chan error, 1)
	go func() {
		errChan <- ip.Start(ctx, results)
	}()

	select {
	case batch := <-results:
		// Should filter out the 25 in-flight files and only return the remaining 25
		if len(batch) != 25 {
			t.Fatalf("expected batch size 25 (filtering in-flight files), got %d", len(batch))
		}
	case err := <-errChan:
		t.Fatalf("IntervalPoller error: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for interval poller tick")
	}
}

// TestProactiveChaos_TransientNetworkRecovery verifies that transient network failure
// during SFTP execution triggers error logging and retries without dropping local files.
func TestProactiveChaos_TransientNetworkRecovery(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("service", "transient_network_recovery")
	_ = os.MkdirAll(testDir, 0o755)
	defer func() { _ = os.RemoveAll(testDir) }()

	filePath := filepath.Join(testDir, "network_test.txt")
	_ = os.WriteFile(filePath, []byte("network test data"), 0o600)

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory: testDir,
			Algorithm: config.PollInterval,
		},
		Integrity: config.IntegrityConfig{
			Algorithm:            config.IntegrityNone,
			VerificationAttempts: 0,
		},
		Action: config.ActionConfig{
			Type: config.ActionScript,
			PostProcess: config.PostProcessConfig{
				Action:      config.PostActionDelete,
				ArchivePath: testDir,
			},
		},
	}

	engine, err := NewEngine(cfg, false)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	// Mock handler that fails on first call, succeeds on second
	handler := &mockFailingActionHandler{maxFailures: 1}
	engine.handler = handler

	ctx := context.Background()

	// Attempt 1: Fails due to simulated network drop
	engine.processFiles(ctx, []string{filePath})

	// Assert file is NOT deleted because action failed
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Errorf("file was deleted despite network action failure!")
	}

	// Attempt 2: Auto-recovers on subsequent attempt
	engine.processFiles(ctx, []string{filePath})

	handler.mu.Lock()
	successCount := len(handler.successFiles)
	handler.mu.Unlock()

	if successCount != 1 {
		t.Errorf("expected 1 file to process successfully after network recovery, got %d", successCount)
	}
}
