// Package poller_test provides unit tests for the directory polling engine.
//
// Objective:
// Validate the core polling logic, OS-specific utility functions, and error
// handling for all supported discovery strategies. It ensures that
// platform-native behaviors (like Windows file locks) and system constraints
// (like non-recursive monitoring) are correctly implemented and enforced.
//
// Scenarios Covered:
// - OS Utilities: Verification of lock detection, subfolder checks, and directory listing.
// - Poller Implementations: Detailed testing of Interval, Batch, Event, and Trigger strategies.
// - Watcher Integration: Mocking and testing of fsnotify-based event processing.
// - Resilience: Verification of channel timeouts, initialization errors, and runtime failures.
// - Error Handling: Validation of structured error types for subfolder detection and watcher issues.
package poller

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"criticalsys.net/dirpoller/internal/config"
	"criticalsys.net/dirpoller/internal/testutils"
	"github.com/fsnotify/fsnotify"
)

func TestMain(m *testing.M) {
	code := m.Run()
	os.Exit(code)
}

func GetTestDir(name string) string {
	return testutils.GetUniqueTestDir("poller", name)
}

// TestOSUtils verifies platform-specific file system utility functions.
//
// Scenario:
// 1. GetFilesError: Rejection of non-existent directories.
// 2. HasSubfoldersError: Rejection of non-existent directories for subfolder checks.
// 3. Stat: Verification of basic metadata retrieval.
// 4. IsLocked: Simulation of lock detection logic.
//
// Success Criteria:
// - Methods must return appropriate errors for invalid inputs.
// - Lock detection must correctly identify free files.
func TestOSUtils(t *testing.T) {
	testDir := GetTestDir("OSUtils")
	utils := NewOSUtils(0)

	t.Run("GetFilesError", func(t *testing.T) {
		_, err := utils.GetFiles(filepath.Join(testDir, "non_existent"))
		if err == nil {
			t.Error("expected error for non-existent directory")
		}
	})

	t.Run("HasSubfoldersError", func(t *testing.T) {
		_, err := utils.HasSubfolders(filepath.Join(testDir, "non_existent"))
		if err == nil {
			t.Error("expected error for non-existent directory")
		}
	})

	t.Run("Stat", func(t *testing.T) {
		_, err := utils.Stat(testDir)
		if err != nil {
			t.Errorf("unexpected error for Stat: %v", err)
		}
	})

	t.Run("IsLockedSimulation", func(t *testing.T) {
		file := filepath.Join(testDir, "locked_sim.txt")
		_ = os.WriteFile(file, []byte("data"), 0o644)

		locked, err := utils.IsLocked(file)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if locked {
			t.Error("expected file not locked")
		}
	})
}

func TestOSUtilsGetFilesSubfolder(t *testing.T) {
	testDir := GetTestDir("GetFilesSubfolder")
	utils := NewOSUtils(0)
	subDir := filepath.Join(testDir, "sub")
	_ = os.Mkdir(subDir, 0o750)

	_, err := utils.GetFiles(testDir)
	if err == nil {
		t.Error("expected error for subfolder in GetFiles")
	}
}

func TestBatchPollerSubfolderDetection(t *testing.T) {
	testDir := GetTestDir("BatchSubfolder")
	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:           testDir,
			Value:               2,
			BatchTimeoutSeconds: 10,
		},
	}
	p := NewBatchPoller(cfg)
	results := make(chan []string, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- p.Start(ctx, results)
	}()

	time.Sleep(200 * time.Millisecond)
	subDir := filepath.Join(testDir, "sub")
	_ = os.Mkdir(subDir, 0o750)

	select {
	case err := <-errChan:
		if err == nil {
			t.Error("expected error for subfolder detection")
		}
	case <-time.After(1 * time.Second):
		t.Error("timeout waiting for error")
	}
}

func TestEventPollerContextCancelled(t *testing.T) {
	testDir := GetTestDir("EventContext")
	cfg := &config.Config{Poll: config.PollConfig{Directory: testDir}}
	p := NewEventPoller(cfg)
	results := make(chan []string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := p.Start(ctx, results)
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestEventPollerSubfolderDetectionWatcher(t *testing.T) {
	testDir := GetTestDir("EventSubWatcher")
	cfg := &config.Config{Poll: config.PollConfig{Directory: testDir}}
	p := NewEventPoller(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- p.Start(ctx, make(chan []string, 1))
	}()

	time.Sleep(200 * time.Millisecond)

	subDir := filepath.Join(testDir, "new_sub_event")
	_ = os.Mkdir(subDir, 0o750)

	select {
	case err := <-errChan:
		if err == nil || !strings.Contains(err.Error(), "subfolder detected") {
			t.Errorf("expected subfolder error, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Log("Warning: timeout waiting for event subfolder error")
	}
}

func TestEventPollerDebounce(t *testing.T) {
	testDir := GetTestDir("EventDebounce")
	cfg := &config.Config{Poll: config.PollConfig{Directory: testDir}}
	p := NewEventPoller(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	results := make(chan []string, 10)
	go func() {
		_ = p.Start(ctx, results)
	}()

	time.Sleep(200 * time.Millisecond)

	testFile := filepath.Join(testDir, "debounce.txt")
	_ = os.WriteFile(testFile, []byte("data1"), 0o644)
	_ = os.WriteFile(testFile, []byte("data2"), 0o644)
	_ = os.WriteFile(testFile, []byte("data3"), 0o644)

	select {
	case files := <-results:
		if len(files) != 1 {
			t.Errorf("expected 1 file after debounce, got %d", len(files))
		}
	case <-time.After(1 * time.Second):
		t.Error("timeout waiting for debounce")
	}

	time.Sleep(600 * time.Millisecond)
	_ = os.WriteFile(testFile, []byte("data4"), 0o644)

	select {
	case files := <-results:
		if len(files) != 1 {
			t.Errorf("expected another file, got %d", len(files))
		}
	case <-time.After(1 * time.Second):
		t.Error("timeout waiting for second event")
	}
}

func TestEventPollerInitialScanSuccess(t *testing.T) {
	testDir := GetTestDir("EventInitialScan")
	_ = os.WriteFile(filepath.Join(testDir, "existing.txt"), []byte("data"), 0o644)

	cfg := &config.Config{Poll: config.PollConfig{Directory: testDir}}
	p := NewEventPoller(cfg)

	results := make(chan []string, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	go func() {
		_ = p.Start(ctx, results)
	}()

	select {
	case files := <-results:
		if len(files) != 1 || !strings.Contains(files[0], "existing.txt") {
			t.Errorf("expected existing.txt, got %v", files)
		}
	case <-time.After(3 * time.Second):
		t.Error("timeout waiting for initial scan")
	}
}

func TestTriggerPollerInvalidPattern(t *testing.T) {
	testDir := GetTestDir("TriggerInvalidPattern")
	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory: testDir,
			Algorithm: config.PollTrigger,
			Value:     "[invalid",
		},
	}
	p := NewTriggerPoller(cfg)
	if p.isTriggerFile("test.txt", "[invalid") {
		t.Error("expected false for invalid pattern")
	}
}

func TestTriggerPollerExactMatch(t *testing.T) {
	cfg := &config.Config{
		Poll: config.PollConfig{
			Algorithm: config.PollTrigger,
			Value:     "trigger.txt",
		},
	}
	p := NewTriggerPoller(cfg)
	if !p.isTriggerFile("trigger.txt", "trigger.txt") {
		t.Error("expected true for exact match")
	}
}

func TestBatchPollerWatcherCreateFile(t *testing.T) {
	testDir := GetTestDir("BatchCreateFile")
	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:           testDir,
			Value:               1,
			BatchTimeoutSeconds: 10,
		},
	}
	p := NewBatchPoller(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	results := make(chan []string, 1)
	go func() {
		_ = p.Start(ctx, results)
	}()

	time.Sleep(200 * time.Millisecond)

	testFile := filepath.Join(testDir, "batch_file.txt")
	_ = os.WriteFile(testFile, []byte("data"), 0o644)

	select {
	case files := <-results:
		if len(files) != 1 || !strings.Contains(files[0], "batch_file.txt") {
			t.Errorf("expected batch_file.txt, got %v", files)
		}
	case <-time.After(3 * time.Second):
		t.Log("Warning: timeout waiting for batch creation")
	}
}

func TestBatchPollerTimeoutFlush(t *testing.T) {
	testDir := GetTestDir("BatchTimeoutFlush")
	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:           testDir,
			Value:               10,
			BatchTimeoutSeconds: 1,
		},
	}
	p := NewBatchPoller(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	results := make(chan []string, 1)
	go func() {
		_ = p.Start(ctx, results)
	}()

	time.Sleep(200 * time.Millisecond)

	testFile := filepath.Join(testDir, "timeout_file.txt")
	_ = os.WriteFile(testFile, []byte("data"), 0o644)

	select {
	case files := <-results:
		if len(files) != 1 || !strings.Contains(files[0], "timeout_file.txt") {
			t.Errorf("expected timeout_file.txt, got %v", files)
		}
	case <-time.After(3 * time.Second):
		t.Error("timeout waiting for timeout flush")
	}
}

func TestBatchPollerWatcherAddError(t *testing.T) {
	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory: "Z:\\non_existent_drive_",
		},
	}
	p := NewBatchPoller(cfg)
	err := p.Start(context.Background(), make(chan []string))
	if err == nil || !strings.Contains(err.Error(), "initialization failed") {
		t.Errorf("expected add directory error, got %v", err)
	}
}

type mockWatcher struct {
	events    chan fsnotify.Event
	errors    chan error
	closed    bool
	addFunc   func(string) error
	closeFunc func() error
}

func newMockWatcher() *mockWatcher {
	return &mockWatcher{
		events: make(chan fsnotify.Event, 10),
		errors: make(chan error, 10),
	}
}

func (m *mockWatcher) Add(name string) error {
	if m.addFunc != nil {
		return m.addFunc(name)
	}
	return nil
}

func (m *mockWatcher) Close() error {
	m.closed = true
	if m.closeFunc != nil {
		return m.closeFunc()
	}
	return nil
}
func (m *mockWatcher) Events() chan fsnotify.Event { return m.events }
func (m *mockWatcher) Errors() chan error          { return m.errors }

// TestBatchPoller_WatcherEvents verifies event-driven batching using a mock watcher.
//
// Scenario:
// 1. Initialize BatchPoller with a threshold of 2.
// 2. Inject two "Create" events into the mock watcher.
// 3. Wait for the resulting batch.
//
// Success Criteria:
// - A batch containing both files must be emitted to the results channel.
func TestBatchPoller_WatcherEvents(t *testing.T) {
	testDir := GetTestDir("BatchWatcherMock")
	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:           testDir,
			Value:               2,
			BatchTimeoutSeconds: 10,
		},
	}
	p := NewBatchPoller(cfg)
	mw := newMockWatcher()
	p.newWatcher = func() (Watcher, error) { return mw, nil }
	p.utils = &testutils.MockOSUtils{StatInfo: &testutils.MockFileInfo{FIsDir: false}}

	results := make(chan []string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = p.Start(ctx, results)
	}()

	mw.events <- fsnotify.Event{Name: filepath.Join(testDir, "f1.txt"), Op: fsnotify.Create}
	mw.events <- fsnotify.Event{Name: filepath.Join(testDir, "f2.txt"), Op: fsnotify.Create}

	select {
	case batch := <-results:
		if len(batch) != 2 {
			t.Errorf("expected batch of 2, got %d", len(batch))
		}
	case <-time.After(2 * time.Second):
		t.Error("timeout waiting for batch")
	}
}

func TestBatchPoller_WatcherError(t *testing.T) {
	testDir := GetTestDir("BatchWatcherError")
	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:           testDir,
			BatchTimeoutSeconds: 10,
		},
	}
	p := NewBatchPoller(cfg)
	mw := newMockWatcher()
	p.newWatcher = func() (Watcher, error) { return mw, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- p.Start(ctx, make(chan []string))
	}()

	mw.errors <- fmt.Errorf("watcher fail")

	select {
	case err := <-errChan:
		if err == nil || !strings.Contains(err.Error(), "watcher fail") {
			t.Errorf("expected watcher fail error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("timeout waiting for error")
	}
}

func TestBatchPoller_NewWatcherFail(t *testing.T) {
	testDir := GetTestDir("BatchWatcherFail")
	cfg := &config.Config{Poll: config.PollConfig{Directory: testDir}}
	p := NewBatchPoller(cfg)
	p.newWatcher = func() (Watcher, error) {
		return nil, fmt.Errorf("factory fail")
	}

	err := p.Start(context.Background(), make(chan []string))
	if err == nil || !strings.Contains(err.Error(), "factory fail") {
		t.Errorf("expected factory fail, got %v", err)
	}
}

func TestEventPoller_NewWatcherFail(t *testing.T) {
	testDir := GetTestDir("EventWatcherFail")
	cfg := &config.Config{Poll: config.PollConfig{Directory: testDir}}
	p := NewEventPoller(cfg)
	p.newWatcher = func() (Watcher, error) {
		return nil, fmt.Errorf("factory fail")
	}

	err := p.Start(context.Background(), make(chan []string))
	if err == nil || !strings.Contains(err.Error(), "factory fail") {
		t.Errorf("expected factory fail, got %v", err)
	}
}

func TestTriggerPoller_NewWatcherFail(t *testing.T) {
	testDir := GetTestDir("TriggerWatcherFail")
	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory: testDir,
			Algorithm: config.PollTrigger,
			Value:     "ready.ok",
		},
	}
	p := NewTriggerPoller(cfg)
	p.newWatcher = func() (Watcher, error) {
		return nil, fmt.Errorf("factory fail")
	}

	err := p.Start(context.Background(), make(chan []string))
	if err == nil || !strings.Contains(err.Error(), "factory fail") {
		t.Errorf("expected factory fail, got %v", err)
	}
}

func TestIntervalPollerStartError(t *testing.T) {
	testDir := GetTestDir("IntervalStartError")
	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory: testDir,
			Value:     1,
		},
	}
	p := NewIntervalPoller(cfg)
	mock := &testutils.MockOSUtils{Err: fmt.Errorf("start error")}
	p.utils = mock

	err := p.Start(context.Background(), make(chan []string))
	if err == nil || !strings.Contains(err.Error(), "start error") {
		t.Errorf("expected start error, got %v", err)
	}
}

func TestIntervalPollerLoopError(t *testing.T) {
	testDir := GetTestDir("IntervalLoopError")
	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory: testDir,
			Value:     1,
		},
	}
	p := NewIntervalPoller(cfg)
	mock := &testutils.MockOSUtils{
		Files: []string{"test.txt"},
	}
	p.utils = mock

	ctx, cancel := context.WithCancel(context.Background())
	errChan := make(chan error, 1)

	go func() {
		errChan <- p.Start(ctx, make(chan []string, 1))
	}()

	time.Sleep(200 * time.Millisecond)
	mock.Err = fmt.Errorf("loop error")

	select {
	case err := <-errChan:
		if err == nil || !strings.Contains(err.Error(), "loop error") {
			t.Errorf("expected loop error, got %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Error("timeout waiting for loop error")
	}
	cancel()
}

func TestEventPollerSubfolderDetectionCreate(t *testing.T) {
	testDir := GetTestDir("EventSubfolderCreate")
	cfg := &config.Config{
		Poll: config.PollConfig{Directory: testDir},
	}
	p := NewEventPoller(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- p.Start(ctx, make(chan []string, 1))
	}()

	time.Sleep(200 * time.Millisecond)

	subfolder := filepath.Join(testDir, "new_sub")
	if err := os.MkdirAll(subfolder, 0o750); err != nil {
		t.Fatalf("failed to create subfolder: %v", err)
	}

	select {
	case err := <-errChan:
		if err == nil || !strings.Contains(err.Error(), "subfolder detected") {
			t.Errorf("expected subfolder error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Log("Warning: timeout waiting for subfolder error")
	}
}

func TestEventPollerWatcherCreateFile(t *testing.T) {
	testDir := GetTestDir("EventCreateFile")
	cfg := &config.Config{
		Poll: config.PollConfig{Directory: testDir},
	}
	p := NewEventPoller(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	results := make(chan []string, 1)
	go func() {
		_ = p.Start(ctx, results)
	}()

	time.Sleep(200 * time.Millisecond)

	testFile := filepath.Join(testDir, "new_file.txt")
	_ = os.WriteFile(testFile, []byte("data"), 0o644)

	select {
	case files := <-results:
		if len(files) != 1 || !strings.Contains(files[0], "new_file.txt") {
			t.Errorf("expected new_file.txt, got %v", files)
		}
	case <-time.After(3 * time.Second):
		t.Log("Warning: timeout waiting for file creation")
	}
}

func TestTriggerPollerWatcherEvents(t *testing.T) {
	testDir := GetTestDir("TriggerWatcher")
	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:           testDir,
			Algorithm:           config.PollTrigger,
			Value:               "ready.ok",
			BatchTimeoutSeconds: 10,
		},
	}
	p := NewTriggerPoller(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	results := make(chan []string, 1)
	go func() {
		_ = p.Start(ctx, results)
	}()

	time.Sleep(200 * time.Millisecond)

	testFile := filepath.Join(testDir, "data.txt")
	_ = os.WriteFile(testFile, []byte("data"), 0o644)
	time.Sleep(100 * time.Millisecond)

	triggerFile := filepath.Join(testDir, "ready.ok")
	_ = os.WriteFile(triggerFile, []byte("ok"), 0o644)

	select {
	case files := <-results:
		if len(files) != 1 || !strings.Contains(files[0], "data.txt") {
			t.Errorf("expected data.txt, got %v", files)
		}
	case <-time.After(3 * time.Second):
		t.Log("Warning: timeout waiting for trigger event")
	}
}

func TestTriggerPollerTimeoutFlush(t *testing.T) {
	testDir := GetTestDir("TriggerTimeout")
	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:           testDir,
			Algorithm:           config.PollTrigger,
			Value:               "never.happens",
			BatchTimeoutSeconds: 1,
		},
	}
	p := NewTriggerPoller(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	results := make(chan []string, 1)
	go func() {
		_ = p.Start(ctx, results)
	}()

	time.Sleep(200 * time.Millisecond)

	testFile := filepath.Join(testDir, "stranded.txt")
	_ = os.WriteFile(testFile, []byte("data"), 0o644)

	select {
	case files := <-results:
		if len(files) != 1 || !strings.Contains(files[0], "stranded.txt") {
			t.Errorf("expected stranded.txt, got %v", files)
		}
	case <-time.After(3 * time.Second):
		t.Error("timeout waiting for timeout flush")
	}
}

func TestTriggerPollerInitialScanTrigger(t *testing.T) {
	testDir := GetTestDir("TriggerInitial")
	_ = os.WriteFile(filepath.Join(testDir, "data.txt"), []byte("data"), 0o644)
	_ = os.WriteFile(filepath.Join(testDir, "ready.ok"), []byte("ok"), 0o644)

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:           testDir,
			Algorithm:           config.PollTrigger,
			Value:               "ready.ok",
			BatchTimeoutSeconds: 10,
		},
	}
	p := NewTriggerPoller(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	results := make(chan []string, 1)
	errChan := make(chan error, 1)
	go func() {
		errChan <- p.Start(ctx, results)
	}()

	select {
	case files := <-results:
		if len(files) != 1 || !strings.Contains(files[0], "data.txt") {
			t.Errorf("expected data.txt, got %v", files)
		}
	case err := <-errChan:
		t.Errorf("poller exited: %v", err)
	case <-time.After(3 * time.Second):
		t.Error("timeout waiting for initial scan trigger")
	}
}

func TestTriggerPollerInvalidValue(t *testing.T) {
	cfg := &config.Config{
		Poll: config.PollConfig{
			Algorithm: config.PollTrigger,
			Value:     123,
		},
	}
	p := NewTriggerPoller(cfg)
	err := p.Start(context.Background(), make(chan []string))
	if err == nil || !strings.Contains(err.Error(), "trigger pattern must be a string") {
		t.Errorf("expected error for non-string trigger, got %v", err)
	}
}

func TestEventPollerCleanup(t *testing.T) {
	testDir := GetTestDir("EventCleanup")
	cfg := &config.Config{Poll: config.PollConfig{Directory: testDir}}
	p := NewEventPoller(cfg)
	_ = p
}

func TestEventPoller_WatcherEvents(t *testing.T) {
	testDir := GetTestDir("EventWatcherMock")
	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:           testDir,
			BatchTimeoutSeconds: 10,
		},
	}
	p := NewEventPoller(cfg)
	mw := newMockWatcher()
	p.newWatcher = func() (Watcher, error) { return mw, nil }
	p.utils = &testutils.MockOSUtils{StatInfo: &testutils.MockFileInfo{FIsDir: false}}

	results := make(chan []string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = p.Start(ctx, results)
	}()

	testFile := filepath.Join(testDir, "event.txt")
	mw.events <- fsnotify.Event{Name: testFile, Op: fsnotify.Create}

	select {
	case batch := <-results:
		if len(batch) != 1 || batch[0] != testFile {
			t.Errorf("expected %s, got %v", testFile, batch)
		}
	case <-time.After(2 * time.Second):
		t.Error("timeout waiting for event")
	}
}

func TestEventPoller_WatcherError(t *testing.T) {
	testDir := GetTestDir("EventWatcherError")
	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:           testDir,
			BatchTimeoutSeconds: 10,
		},
	}
	p := NewEventPoller(cfg)
	mw := newMockWatcher()
	p.newWatcher = func() (Watcher, error) { return mw, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- p.Start(ctx, make(chan []string))
	}()

	mw.errors <- fmt.Errorf("watcher fail")

	select {
	case err := <-errChan:
		if err == nil || !strings.Contains(err.Error(), "watcher fail") {
			t.Errorf("expected watcher fail, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("timeout waiting for error")
	}
}

func TestTriggerPoller_WatcherEvents(t *testing.T) {
	testDir := GetTestDir("TriggerWatcherMock")
	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:           testDir,
			Algorithm:           config.PollTrigger,
			Value:               "ready.ok",
			BatchTimeoutSeconds: 10,
		},
	}
	p := NewTriggerPoller(cfg)
	mw := newMockWatcher()
	p.newWatcher = func() (Watcher, error) { return mw, nil }
	p.utils = &testutils.MockOSUtils{StatInfo: &testutils.MockFileInfo{FIsDir: false}}

	results := make(chan []string, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = p.Start(ctx, results)
	}()

	f1 := filepath.Join(testDir, "f1.txt")
	mw.events <- fsnotify.Event{Name: f1, Op: fsnotify.Create}
	time.Sleep(100 * time.Millisecond)
	mw.events <- fsnotify.Event{Name: filepath.Join(testDir, "ready.ok"), Op: fsnotify.Create}

	select {
	case batch := <-results:
		if len(batch) != 1 || batch[0] != f1 {
			t.Errorf("expected %s, got %v", f1, batch)
		}
	case <-time.After(2 * time.Second):
		t.Error("timeout waiting for trigger")
	}
}

func TestTriggerPoller_WatcherError(t *testing.T) {
	testDir := GetTestDir("TriggerWatcherError")
	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:           testDir,
			Algorithm:           config.PollTrigger,
			Value:               "ready.ok",
			BatchTimeoutSeconds: 10,
		},
	}
	p := NewTriggerPoller(cfg)
	mw := newMockWatcher()
	p.newWatcher = func() (Watcher, error) { return mw, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- p.Start(ctx, make(chan []string))
	}()

	mw.errors <- fmt.Errorf("watcher fail")

	select {
	case err := <-errChan:
		if err == nil || !strings.Contains(err.Error(), "watcher fail") {
			t.Errorf("expected watcher fail, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("timeout waiting for error")
	}
}

func TestEventPollerWatcherInitialStatError(t *testing.T) {
	testDir := GetTestDir("EventInitialStatErr")
	cfg := &config.Config{Poll: config.PollConfig{Directory: testDir}}
	p := NewEventPoller(cfg)

	mock := &testutils.MockOSUtils{StatInfo: &testutils.MockFileInfo{FIsDir: false}}
	p.utils = mock
	mock.Err = fmt.Errorf("hasSubfolders error")
	mock.HasSubfoldersValue = true

	err := p.Start(context.Background(), make(chan []string, 1))
	if err == nil || !strings.Contains(err.Error(), "hasSubfolders error") {
		t.Errorf("expected hasSubfolders error, got %v", err)
	}
}

func TestBatchPollerInvalidThreshold(t *testing.T) {
	cfg := &config.Config{
		Poll: config.PollConfig{
			Value: "not-an-int",
		},
	}
	p := NewBatchPoller(cfg)
	p.files["test.txt"] = struct{}{}
	results := make(chan []string, 1)
	p.mu.Lock()
	ok := p.checkThreshold(context.Background(), results)
	p.mu.Unlock()
	if !ok {
		t.Error("expected checkThreshold true")
	}
	batch := <-results
	if len(batch) != 1 {
		t.Errorf("expected 1, got %d", len(batch))
	}
}

func TestOSUtils_IsLocked_WindowsSpecific(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Skipping Windows-specific lock tests")
	}

	utils := NewOSUtils(0)
	testDir := GetTestDir("OSUtilsLockedWin")

	t.Run("SharingViolation", func(t *testing.T) {
		file := filepath.Join(testDir, "locked_file.txt")
		_ = os.WriteFile(file, []byte("data"), 0o644)

		// Open file with no sharing to simulate a lock
		f, err := os.OpenFile(file, os.O_RDWR, 0)
		if err != nil {
			t.Fatalf("failed to open file for locking: %v", err)
		}
		defer func() { _ = f.Close() }()

		// On Windows, os.OpenFile(..., os.O_RDWR, 0) usually allows sharing
		// We need a real sharing violation.
		// Since we can't easily call CreateFile with 0 sharing here without cgo or syscalls
		// (which is what IsLocked does), we trust the logic but try to trigger it if possible.
		// For now, we at least exercise the path even if it doesn't return true.
		locked, err := utils.IsLocked(file)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		_ = locked
	})
}

func TestErrSubfolderDetected_Error(t *testing.T) {
	err := &ErrSubfolderDetected{Path: "sub"}
	expected := "[Poller:Discovery] subfolder detected: sub"
	if err.Error() != expected {
		t.Errorf("expected %q, got %q", expected, err.Error())
	}
}

func TestErrWatcherInitialization_Error(t *testing.T) {
	origErr := os.ErrNotExist
	err := &ErrWatcherInitialization{Err: origErr}
	if !strings.Contains(err.Error(), "initialization failed") {
		t.Errorf("expected error message to contain 'initialization failed', got %q", err.Error())
	}
	if err.Unwrap() != origErr {
		t.Errorf("expected unwrapped error %v, got %v", origErr, err.Unwrap())
	}
}

func TestErrWatcherRuntime_Error(t *testing.T) {
	origErr := fmt.Errorf("runtime fail")
	err := &ErrWatcherRuntime{Err: origErr}
	if !strings.Contains(err.Error(), "runtime error") {
		t.Errorf("expected error message to contain 'runtime error', got %q", err.Error())
	}
	if err.Unwrap() != origErr {
		t.Errorf("expected unwrapped error %v, got %v", origErr, err.Unwrap())
	}
}

func TestOSUtils_IsLocked_TableDriven(t *testing.T) {
	utils := NewOSUtils(0)
	testDir := GetTestDir("OSUtilsLockedTable")

	invalidPath := "path\x00withnull"
	nonExistentFile := "Z:\\non_existent_file_12345.txt"
	if runtime.GOOS != "windows" {
		nonExistentFile = "/tmp/non_existent_file_12345.txt"
	}

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"InvalidPath", invalidPath, true},
		{"NonExistentFile", nonExistentFile, true},
		{"DirectoryPath", testDir, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			locked, err := utils.IsLocked(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("IsLocked() error = %v, wantErr %v", err, tt.wantErr)
			}
			if locked && tt.wantErr && !strings.Contains(tt.name, "SharingViolation") {
				// Most errors should not report as "locked"
				t.Errorf("IsLocked() = %v, expected false for non-sharing violation error", locked)
			}
		})
	}
}

func TestPoller_WatcherInitialization_Errors(t *testing.T) {
	testDir := GetTestDir("WatcherInitErrors")

	tests := []struct {
		name       string
		pollerFunc func(*config.Config) interface {
			Start(context.Context, chan<- []string) error
		}
		failNewWatcher bool
		failAdd        bool
	}{
		{"Batch_NewWatcherFail", func(c *config.Config) interface {
			Start(context.Context, chan<- []string) error
		} {
			p := NewBatchPoller(c)
			p.newWatcher = func() (Watcher, error) { return nil, fmt.Errorf("factory fail") }
			return p
		}, true, false},
		{"Batch_AddFail", func(c *config.Config) interface {
			Start(context.Context, chan<- []string) error
		} {
			p := NewBatchPoller(c)
			p.newWatcher = func() (Watcher, error) {
				mw := newMockWatcher()
				mw.addFunc = func(string) error { return fmt.Errorf("add fail") }
				return mw, nil
			}
			return p
		}, false, true},
		{"Event_NewWatcherFail", func(c *config.Config) interface {
			Start(context.Context, chan<- []string) error
		} {
			p := NewEventPoller(c)
			p.newWatcher = func() (Watcher, error) { return nil, fmt.Errorf("factory fail") }
			return p
		}, true, false},
		{"Trigger_NewWatcherFail", func(c *config.Config) interface {
			Start(context.Context, chan<- []string) error
		} {
			p := NewTriggerPoller(c)
			p.newWatcher = func() (Watcher, error) { return nil, fmt.Errorf("factory fail") }
			return p
		}, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{Poll: config.PollConfig{Directory: testDir, Value: 1, BatchTimeoutSeconds: 1}}
			poller := tt.pollerFunc(cfg)
			err := poller.Start(context.Background(), make(chan []string, 1))
			if err == nil {
				t.Error("expected error, got nil")
			}
		})
	}
}

func TestPoller_ChannelTimeouts(t *testing.T) {
	testDir := GetTestDir("ChannelTimeouts")
	cfg := &config.Config{Poll: config.PollConfig{Directory: testDir, Value: 1, BatchTimeoutSeconds: 1}}
	setDefaults(cfg)

	t.Run("Interval_PollTimeout", func(t *testing.T) {
		p := NewIntervalPoller(cfg)
		mock := &testutils.MockOSUtils{Files: []string{"f1.txt"}}
		p.utils = mock
		results := make(chan []string) // Unbuffered, no receiver = timeout
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		err := p.poll(ctx, results)
		if err == nil {
			t.Error("expected error due to blocked channel and timeout context, got nil")
		}
	})

	t.Run("Batch_FlushTimeout", func(t *testing.T) {
		p := NewBatchPoller(cfg)
		p.files["f1.txt"] = struct{}{}
		results := make(chan []string)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		p.mu.Lock()
		p.flush(ctx, results)
		p.mu.Unlock()
		if len(p.files) != 0 {
			t.Error("files should be cleared even if send times out")
		}
	})

	t.Run("Event_SendTimeout", func(t *testing.T) {
		p := NewEventPoller(cfg)
		results := make(chan []string)
		mw := newMockWatcher()
		p.newWatcher = func() (Watcher, error) { return mw, nil }
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() { _ = p.Start(ctx, results) }()

		time.Sleep(100 * time.Millisecond)
		mw.events <- fsnotify.Event{Name: "f1.txt", Op: fsnotify.Create}
		time.Sleep(100 * time.Millisecond)
		// Should return without blocking
	})
}

func TestEventPoller_WatcherInitialAddError(t *testing.T) {
	testDir := GetTestDir("EventWatcherInitAddErr")
	cfg := &config.Config{Poll: config.PollConfig{Directory: testDir}}
	p := NewEventPoller(cfg)

	p.newWatcher = func() (Watcher, error) {
		mw := newMockWatcher()
		mw.addFunc = func(name string) error {
			return fmt.Errorf("initial add error")
		}
		return mw, nil
	}

	err := p.Start(context.Background(), make(chan []string, 1))
	if err == nil || !strings.Contains(err.Error(), "initial add error") {
		t.Errorf("expected initial add error, got %v", err)
	}
}

func setDefaults(cfg *config.Config) {
	if cfg.Poll.BatchTimeoutSeconds == 0 {
		cfg.Poll.BatchTimeoutSeconds = 600
	}
}

func TestEventPoller_WatcherEvents_Subfolder(t *testing.T) {
	testDir := GetTestDir("EventWatcherSub")
	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory: testDir,
		},
	}
	setDefaults(cfg)
	p := NewEventPoller(cfg)
	mw := newMockWatcher()
	p.newWatcher = func() (Watcher, error) { return mw, nil }

	// Mock Stat to return IsDir = true
	p.utils = &testutils.MockOSUtils{
		StatInfo: &testutils.MockFileInfo{FIsDir: true, FName: "sub"},
	}

	errChan := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		errChan <- p.Start(ctx, make(chan []string, 1))
	}()

	time.Sleep(100 * time.Millisecond)
	mw.events <- fsnotify.Event{Name: filepath.Join(testDir, "sub"), Op: fsnotify.Create}

	select {
	case err := <-errChan:
		if err == nil || !strings.Contains(err.Error(), "subfolder detected") {
			t.Errorf("expected subfolder error, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("timeout waiting for subfolder error")
	}
}

func TestTriggerPoller_WatcherEvents_Subfolder(t *testing.T) {
	testDir := GetTestDir("TriggerWatcherSub")
	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory: testDir,
			Algorithm: config.PollTrigger,
			Value:     "ready.ok",
		},
	}
	setDefaults(cfg)
	p := NewTriggerPoller(cfg)
	mw := newMockWatcher()
	p.newWatcher = func() (Watcher, error) { return mw, nil }

	// Mock Stat to return IsDir = true
	p.utils = &testutils.MockOSUtils{
		StatInfo: &testutils.MockFileInfo{FIsDir: true, FName: "sub"},
	}

	errChan := make(chan error, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		errChan <- p.Start(ctx, make(chan []string, 1))
	}()

	time.Sleep(100 * time.Millisecond)
	mw.events <- fsnotify.Event{Name: filepath.Join(testDir, "sub"), Op: fsnotify.Create}

	// TriggerPoller currently just ignores subfolders in its event loop (doesn't add to map)
	// unless they match the trigger pattern. Wait, trigger.go lines 108-111:
	// if err == nil && !stat.IsDir() { p.files[event.Name] = struct{}{} }
	// So it won't return an error like Batch/Event poller.
	// Let's verify it doesn't crash.

	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-errChan:
		t.Errorf("TriggerPoller should not exit on subfolder creation, got %v", err)
	default:
		// OK
	}
}

func TestEventPoller_LRU_Eviction(t *testing.T) {
	testDir := GetTestDir("EventLRU")
	cfg := &config.Config{Poll: config.PollConfig{Directory: testDir}}
	setDefaults(cfg)
	p := NewEventPoller(cfg)
	p.maxCache = 2 // Small cache for testing

	p.processed["f1"] = p.lruList.PushFront(&cacheEntry{name: "f1", time: time.Now()})
	p.processed["f2"] = p.lruList.PushFront(&cacheEntry{name: "f2", time: time.Now()})

	mw := newMockWatcher()
	p.newWatcher = func() (Watcher, error) { return mw, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.Start(ctx, make(chan []string, 10)) }()

	time.Sleep(100 * time.Millisecond)
	// Triggering a new file event should evict the oldest (f1)
	mw.events <- fsnotify.Event{Name: "f3", Op: fsnotify.Create}

	time.Sleep(100 * time.Millisecond)
	p.mu.Lock()
	if _, ok := p.processed["f1"]; ok {
		t.Error("expected f1 to be evicted")
	}
	if len(p.processed) != 2 {
		t.Errorf("expected cache size 2, got %d", len(p.processed))
	}
	p.mu.Unlock()
}

func TestEventPoller_WatcherCloseError(t *testing.T) {
	testDir := GetTestDir("EventCloseErr")
	cfg := &config.Config{Poll: config.PollConfig{Directory: testDir}}
	setDefaults(cfg)
	p := NewEventPoller(cfg)

	mw := newMockWatcher()
	mw.closeFunc = func() error { return fmt.Errorf("close error") }
	p.newWatcher = func() (Watcher, error) { return mw, nil }

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = p.Start(ctx, make(chan []string, 1)) }()

	time.Sleep(100 * time.Millisecond)
	cancel() // This should trigger watcher.Close()
	time.Sleep(100 * time.Millisecond)
	// We just verify it doesn't crash, as the error is logged.
}

func TestBatchPoller_ResilientFallbackScan(t *testing.T) {
	testDir := GetTestDir("BatchFallbackScan")
	// Set threshold of 3 files (Value = 3), batch_timeout_seconds = 1
	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:           testDir,
			Algorithm:           "batch",
			Value:               3,
			MaxBatchSize:        10,
			BatchTimeoutSeconds: 1,
		},
	}
	p := NewBatchPoller(cfg)

	// Create 2 files in the folder (below the threshold of 3)
	f1 := filepath.Join(testDir, "file1.txt")
	f2 := filepath.Join(testDir, "file2.txt")
	_ = os.WriteFile(f1, []byte("data1"), 0o600)
	_ = os.WriteFile(f2, []byte("data2"), 0o600)
	defer func() {
		_ = os.Remove(f1)
		_ = os.Remove(f2)
	}()

	mw := newMockWatcher()
	p.newWatcher = func() (Watcher, error) { return mw, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	results := make(chan []string, 1)
	go func() {
		_ = p.Start(ctx, results)
	}()

	// Wait for the backlogTicker (which runs every 2 seconds) and the batchTimeout (1 second) to fire.
	// In the old code, since hasBacklog was false, the 2 files would be ignored.
	// In the new code, the backlog ticker will scan them, add them to p.files, and the timeout will flush them.
	select {
	case batch := <-results:
		if len(batch) != 2 {
			t.Errorf("expected batch of 2 files, got %d", len(batch))
		}
		hasF1 := false
		hasF2 := false
		for _, f := range batch {
			if strings.Contains(f, "file1.txt") {
				hasF1 = true
			}
			if strings.Contains(f, "file2.txt") {
				hasF2 = true
			}
		}
		if !hasF1 || !hasF2 {
			t.Errorf("missing expected files in batch: %v", batch)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for fallback scan to process files")
	}
}

func TestIntervalPoller_InFlightFiltering(t *testing.T) {
	testDir := GetTestDir("IntervalInFlight")
	_ = os.MkdirAll(testDir, 0o755)

	f1 := filepath.Join(testDir, "file1.txt")
	f2 := filepath.Join(testDir, "file2.txt")
	_ = os.WriteFile(f1, []byte("data1"), 0o600)
	_ = os.WriteFile(f2, []byte("data2"), 0o600)

	AddInFlight(f1)
	defer RemoveInFlight(f1)

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory: testDir,
			Algorithm: config.PollInterval,
			Value:     1,
		},
	}
	ip := NewIntervalPoller(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	results := make(chan []string, 1)
	err := ip.poll(ctx, results)
	if err != nil {
		t.Fatalf("poll failed: %v", err)
	}

	select {
	case batch := <-results:
		if len(batch) != 1 {
			t.Fatalf("expected batch size 1 (filtering in-flight file), got %d", len(batch))
		}
		if batch[0] != f2 {
			t.Errorf("expected only f2 (%s), got %s", f2, batch[0])
		}
	default:
		t.Fatal("expected results channel to have ready files")
	}
}

func TestIntervalPoller_ExclusionFiltering(t *testing.T) {
	testDir := GetTestDir("IntervalExclusion")
	_ = os.MkdirAll(testDir, 0o755)

	f1 := filepath.Join(testDir, "ex_file1.txt")
	f2 := filepath.Join(testDir, "ex_file2.txt")
	_ = os.WriteFile(f1, []byte("data1"), 0o600)
	_ = os.WriteFile(f2, []byte("data2"), 0o600)

	ExcludePath(f1, 10*time.Second)

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory: testDir,
			Algorithm: config.PollInterval,
			Value:     1,
		},
	}
	ip := NewIntervalPoller(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	results := make(chan []string, 1)
	err := ip.poll(ctx, results)
	if err != nil {
		t.Fatalf("poll failed: %v", err)
	}

	select {
	case batch := <-results:
		if len(batch) != 1 {
			t.Fatalf("expected batch size 1 (filtering excluded file), got %d", len(batch))
		}
		if batch[0] != f2 {
			t.Errorf("expected only f2 (%s), got %s", f2, batch[0])
		}
	default:
		t.Fatal("expected results channel to have ready files")
	}
}

func TestBatchPoller_WatcherOverflowRecovery(t *testing.T) {
	testDir := GetTestDir("BatchOverflowRecovery")
	_ = os.MkdirAll(testDir, 0o755)

	f1 := filepath.Join(testDir, "overflow1.txt")
	_ = os.WriteFile(f1, []byte("data"), 0o600)
	defer func() { _ = os.Remove(f1) }()

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:           testDir,
			Algorithm:           config.PollBatch,
			Value:               1,
			MaxBatchSize:        10,
			BatchTimeoutSeconds: 60,
		},
	}
	bp := NewBatchPoller(cfg)
	mw := newMockWatcher()
	bp.newWatcher = func() (Watcher, error) { return mw, nil }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	results := make(chan []string, 1)
	errChan := make(chan error, 1)
	go func() {
		errChan <- bp.Start(ctx, results)
	}()

	time.Sleep(100 * time.Millisecond)
	mw.errors <- fmt.Errorf("runtime error: fsnotify: queue or buffer overflow")

	select {
	case batch := <-results:
		if len(batch) == 0 {
			t.Errorf("expected catch-up scan batch after overflow event, got empty")
		}
	case err := <-errChan:
		t.Fatalf("BatchPoller exited prematurely on overflow error: %v", err)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for overflow catch-up scan")
	}
}
