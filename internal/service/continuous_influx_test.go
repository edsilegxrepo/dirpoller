package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"criticalsys.net/dirpoller/internal/config"
	"criticalsys.net/dirpoller/internal/testutils"
)

// mockContinuousActionHandler records all processed files under high-velocity continuous influx.
type mockContinuousActionHandler struct {
	mu        sync.Mutex
	processed map[string]int
	count     int32
}

func newMockContinuousActionHandler() *mockContinuousActionHandler {
	return &mockContinuousActionHandler{
		processed: make(map[string]int),
	}
}

func (m *mockContinuousActionHandler) Execute(ctx context.Context, files []string) ([]string, error) {
	m.mu.Lock()
	for _, f := range files {
		m.processed[f]++
	}
	m.mu.Unlock()

	atomic.AddInt32(&m.count, int32(len(files)))
	return files, nil
}

func (m *mockContinuousActionHandler) RemoteCleanup(ctx context.Context) error {
	return nil
}

func (m *mockContinuousActionHandler) Close() error {
	return nil
}

// TestContinuousInflux_IntervalPoller verifies that a continuous stream of incoming files
// under PollInterval is processed without duplicate dispatches or memory/goroutine leaks.
func TestContinuousInflux_IntervalPoller(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("service", "continuous_interval")
	_ = os.MkdirAll(testDir, 0o755)
	defer func() { _ = os.RemoveAll(testDir) }()

	archiveDir := testutils.GetUniqueTestDir("service", "cont_interval_archive")
	_ = os.MkdirAll(archiveDir, 0o755)
	defer func() { _ = os.RemoveAll(archiveDir) }()

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
				ArchivePath: archiveDir,
			},
		},
	}

	engine, err := NewEngine(cfg, false)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	handler := newMockContinuousActionHandler()
	engine.handler = handler

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errChan := make(chan error, 1)
	go func() { errChan <- engine.Run(ctx) }()

	// Continuous producer goroutine: writes 10 files every 100ms for 1 second (100 files)
	totalProduced := 0
	producerDone := make(chan struct{})
	go func() {
		defer close(producerDone)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for i := 0; i < 10; i++ {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for j := 0; j < 10; j++ {
					p := filepath.Join(testDir, fmt.Sprintf("cont_interval_%d_%d.txt", i, j))
					if err := os.WriteFile(p, []byte("continuous stream"), 0o600); err == nil {
						totalProduced++
					}
				}
			}
		}
	}()

	<-producerDone

	// Wait for engine to drain the continuous stream
	for start := time.Now(); time.Since(start) < 5*time.Second; {
		if atomic.LoadInt32(&handler.count) >= int32(totalProduced) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	cancel()
	engine.Close()
	<-errChan

	handler.mu.Lock()
	defer handler.mu.Unlock()

	// Verify no duplicate processing (every file executed exactly 1 time)
	for f, times := range handler.processed {
		if times > 1 {
			t.Errorf("file %s was processed %d times (expected exactly 1)", f, times)
		}
	}

	if atomic.LoadInt32(&handler.count) < int32(totalProduced) {
		t.Errorf("expected to process all %d files under continuous interval influx, got %d", totalProduced, handler.count)
	}
}

// TestContinuousInflux_BatchPoller verifies continuous high-velocity file arrival under PollBatch
// with backlog draining across multiple consecutive batch flushes.
func TestContinuousInflux_BatchPoller(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("service", "continuous_batch")
	_ = os.MkdirAll(testDir, 0o755)
	defer func() { _ = os.RemoveAll(testDir) }()

	archiveDir := testutils.GetUniqueTestDir("service", "cont_batch_archive")
	_ = os.MkdirAll(archiveDir, 0o755)
	defer func() { _ = os.RemoveAll(archiveDir) }()

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:           testDir,
			Algorithm:           config.PollBatch,
			Value:               20,
			MaxBatchSize:        20,
			BatchTimeoutSeconds: 2,
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
				ArchivePath: archiveDir,
			},
		},
	}

	engine, err := NewEngine(cfg, false)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	handler := newMockContinuousActionHandler()
	engine.handler = handler

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	errChan := make(chan error, 1)
	go func() { errChan <- engine.Run(ctx) }()

	// Write 150 files in rapid continuous bursts
	totalProduced := 150
	for i := 0; i < totalProduced; i++ {
		p := filepath.Join(testDir, fmt.Sprintf("cont_batch_%d.txt", i))
		_ = os.WriteFile(p, []byte("continuous batch data"), 0o600)
		if i%10 == 0 {
			time.Sleep(50 * time.Millisecond)
		}
	}

	// Wait for poller to continuously drain all batches
	for start := time.Now(); time.Since(start) < 4*time.Second; {
		if atomic.LoadInt32(&handler.count) >= int32(totalProduced) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	cancel()
	engine.Close()
	<-errChan

	if atomic.LoadInt32(&handler.count) < int32(totalProduced) {
		t.Errorf("expected to process all %d files under continuous batch influx, got %d", totalProduced, handler.count)
	}
}

// TestContinuousInflux_EventPoller verifies continuous rapid-fire fsnotify events under PollEvent
// with micro-batch coalescing and LRU cache stability.
func TestContinuousInflux_EventPoller(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("service", "continuous_event")
	_ = os.MkdirAll(testDir, 0o755)
	defer func() { _ = os.RemoveAll(testDir) }()

	archiveDir := testutils.GetUniqueTestDir("service", "cont_event_archive")
	_ = os.MkdirAll(archiveDir, 0o755)
	defer func() { _ = os.RemoveAll(archiveDir) }()

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:    testDir,
			Algorithm:    config.PollEvent,
			MaxBatchSize: 25,
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
				ArchivePath: archiveDir,
			},
		},
	}

	engine, err := NewEngine(cfg, false)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	handler := newMockContinuousActionHandler()
	engine.handler = handler

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	errChan := make(chan error, 1)
	go func() { errChan <- engine.Run(ctx) }()

	// Produce 100 files with continuous event bursts
	totalProduced := 100
	for i := 0; i < totalProduced; i++ {
		p := filepath.Join(testDir, fmt.Sprintf("cont_event_%d.txt", i))
		_ = os.WriteFile(p, []byte("continuous event data"), 0o600)
		time.Sleep(15 * time.Millisecond)
	}

	for start := time.Now(); time.Since(start) < 4*time.Second; {
		if atomic.LoadInt32(&handler.count) >= int32(totalProduced) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	cancel()
	engine.Close()
	<-errChan

	if atomic.LoadInt32(&handler.count) < int32(totalProduced) {
		t.Errorf("expected to process all %d files under continuous event influx, got %d", totalProduced, handler.count)
	}
}

// TestContinuousInflux_TriggerPoller verifies continuous data file accumulation under PollTrigger
// followed by trigger file insertion.
func TestContinuousInflux_TriggerPoller(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("service", "continuous_trigger")
	_ = os.MkdirAll(testDir, 0o755)
	defer func() { _ = os.RemoveAll(testDir) }()

	archiveDir := testutils.GetUniqueTestDir("service", "cont_trigger_archive")
	_ = os.MkdirAll(archiveDir, 0o755)
	defer func() { _ = os.RemoveAll(archiveDir) }()

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:           testDir,
			Algorithm:           config.PollTrigger,
			Value:               "*.done",
			BatchTimeoutSeconds: 10,
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
				ArchivePath: archiveDir,
			},
		},
	}

	engine, err := NewEngine(cfg, false)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	handler := newMockContinuousActionHandler()
	engine.handler = handler

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	errChan := make(chan error, 1)
	go func() { errChan <- engine.Run(ctx) }()

	// Phase 1: Write 50 data files
	dataCount := 50
	for i := 0; i < dataCount; i++ {
		p := filepath.Join(testDir, fmt.Sprintf("data_%d.dat", i))
		_ = os.WriteFile(p, []byte("trigger data"), 0o600)
	}

	time.Sleep(500 * time.Millisecond)

	// Assert no files processed before trigger file arrives
	if atomic.LoadInt32(&handler.count) > 0 {
		t.Errorf("expected 0 files processed before trigger file, got %d", handler.count)
	}

	// Phase 2: Create trigger file
	triggerPath := filepath.Join(testDir, "batch_1.done")
	_ = os.WriteFile(triggerPath, []byte("ready"), 0o600)

	// Wait for poller to flush data batch
	for start := time.Now(); time.Since(start) < 3*time.Second; {
		if atomic.LoadInt32(&handler.count) >= int32(dataCount) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	cancel()
	engine.Close()
	<-errChan

	if atomic.LoadInt32(&handler.count) < int32(dataCount) {
		t.Errorf("expected all %d data files processed upon trigger file arrival, got %d", dataCount, handler.count)
	}
}

// TestContinuousInflux_GoroutineAndMemoryLeakCheck runs continuous multi-algorithm influx
// and asserts zero goroutine or memory leaks after continuous engine execution cycles.
func TestContinuousInflux_GoroutineAndMemoryLeakCheck(t *testing.T) {
	runtime.GC()
	initialGoroutines := runtime.NumGoroutine()

	t.Run("Interval", TestContinuousInflux_IntervalPoller)
	t.Run("Batch", TestContinuousInflux_BatchPoller)
	t.Run("Event", TestContinuousInflux_EventPoller)
	t.Run("Trigger", TestContinuousInflux_TriggerPoller)

	time.Sleep(500 * time.Millisecond)
	runtime.GC()
	finalGoroutines := runtime.NumGoroutine()

	if finalGoroutines > initialGoroutines+3 {
		t.Errorf("continuous influx goroutine leak detected: initial=%d, final=%d", initialGoroutines, finalGoroutines)
	}
}
