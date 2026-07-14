package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"criticalsys.net/dirpoller/internal/config"
)

// mockActionHandler implements action.ActionHandler for scale testing without external dependencies.
type mockScaleActionHandler struct {
	mu        sync.Mutex
	processed []string
}

func (m *mockScaleActionHandler) Execute(ctx context.Context, files []string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.processed = append(m.processed, files...)
	// Simulate minor processing delay per file
	time.Sleep(10 * time.Microsecond * time.Duration(len(files)))
	return files, nil
}

func (m *mockScaleActionHandler) RemoteCleanup(ctx context.Context) error {
	return nil
}

func (m *mockScaleActionHandler) Close() error {
	return nil
}

type mockScaleVerifier struct{}

func (m *mockScaleVerifier) Verify(ctx context.Context, path string) (bool, error) {
	return true, nil
}

func (m *mockScaleVerifier) CalculateHash(path string) (string, error) {
	return "mock-hash", nil
}

type mockScaleArchiver struct{}

func (m *mockScaleArchiver) Process(ctx context.Context, files []string) error {
	return nil
}

// TestEngineScalePerformance measures the memory, CPU, and goroutine characteristics of the Engine.
// Run this test with:
//
//	$env:TEST_SCALE="true"; $env:SCALE_LIMIT="10000"; go test -v -run=TestEngineScalePerformance ./internal/service
func TestEngineScalePerformance(t *testing.T) {
	if os.Getenv("TEST_SCALE") != "true" {
		t.Skip("Skipping scale performance test. Set TEST_SCALE=true to run.")
	}

	// Override osStat to bypass slow disk metadata reads during scale tests
	oldOsStat := osStat
	osStat = func(name string) (os.FileInfo, error) {
		return &mockFileInfo{name: name}, nil
	}
	defer func() { osStat = oldOsStat }()

	scaleLimit := 10000
	if limitStr := os.Getenv("SCALE_LIMIT"); limitStr != "" {
		_, _ = fmt.Sscanf(limitStr, "%d", &scaleLimit)
	}

	if scaleLimit >= 1000000 {
		fmt.Printf("[ScaleTest] Running in-memory mode for %d files to bypass disk bottlenecks...\n", scaleLimit)

		files := make([]string, scaleLimit)
		for i := 0; i < scaleLimit; i++ {
			files[i] = fmt.Sprintf("C:\\mock_polling_dir\\scale_file_%d.txt", i)
		}

		cfg := &config.Config{
			Poll: config.PollConfig{
				Algorithm:              config.PollInterval,
				MaxVerificationWorkers: 64,
			},
			Action: config.ActionConfig{
				Type:                  config.ActionScript,
				ConcurrentConnections: 64,
			},
		}

		engine, err := NewEngine(cfg, false)
		if err != nil {
			t.Fatalf("failed to create engine: %v", err)
		}
		defer engine.Close()

		engine.verifier = &mockScaleVerifier{}
		engine.archiver = &mockScaleArchiver{}
		mockAction := &mockScaleActionHandler{}
		engine.handler = mockAction

		runtime.GC()
		var msBaseline runtime.MemStats
		runtime.ReadMemStats(&msBaseline)
		goroutinesBaseline := runtime.NumGoroutine()

		fmt.Printf("\n--- Baseline Metrics (In-Memory) ---\n")
		fmt.Printf("Heap Alloc: %d MB\n", msBaseline.Alloc/1024/1024)
		fmt.Printf("Heap Sys:   %d MB\n", msBaseline.HeapSys/1024/1024)
		fmt.Printf("Goroutines: %d\n", goroutinesBaseline)
		fmt.Printf("------------------------------------\n\n")

		startTime := time.Now()

		engine.processFiles(context.Background(), files)

		duration := time.Since(startTime)

		runtime.GC()
		var msFinal runtime.MemStats
		runtime.ReadMemStats(&msFinal)

		fmt.Printf("\n--- Scale Execution Metrics (%d files - In-Memory) ---\n", scaleLimit)
		fmt.Printf("Duration:         %v\n", duration)
		fmt.Printf("Processed Count:  %d\n", len(mockAction.processed))
		fmt.Printf("Max Heap Alloc:   %d MB (Baseline: %d MB)\n", msFinal.Alloc/1024/1024, msBaseline.Alloc/1024/1024)
		fmt.Printf("Final Heap Sys:   %d MB\n", msFinal.HeapSys/1024/1024)
		fmt.Printf("------------------------------------------------------\n\n")
		return
	}

	// 1. Setup structured temp directory: %TEMP%\unitests\dirpoller-YYYYMMDDhhmmss
	tempDir := os.Getenv("TEMP")
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	timestamp := time.Now().Format("20060102150405")
	testDir := filepath.Join(tempDir, "unitests", fmt.Sprintf("dirpoller-%s-%d", timestamp, scaleLimit))

	if err := os.MkdirAll(testDir, 0o750); err != nil {
		t.Fatalf("failed to create temp test directory %s: %v", testDir, err)
	}
	archiveDir := filepath.Join(filepath.Dir(testDir), "archive")
	defer func() {
		_ = os.RemoveAll(testDir)
		_ = os.RemoveAll(archiveDir)
	}()

	fmt.Printf("[ScaleTest] Generating %d files inside: %s\n", scaleLimit, testDir)

	// Create empty files concurrently
	const numWorkers = 64
	jobs := make(chan int, scaleLimit)
	var genWg sync.WaitGroup

	for w := 0; w < numWorkers; w++ {
		genWg.Add(1)
		go func() {
			defer genWg.Done()
			for j := range jobs {
				path := filepath.Join(testDir, fmt.Sprintf("scale_file_%d.txt", j))
				f, err := os.Create(path)
				if err == nil {
					_ = f.Close()
				}
			}
		}()
	}

	for i := 0; i < scaleLimit; i++ {
		jobs <- i
	}
	close(jobs)
	genWg.Wait()
	fmt.Printf("[ScaleTest] Generated %d files successfully.\n", scaleLimit)

	// Configure engine to run
	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:              testDir,
			Algorithm:              config.PollInterval,
			Value:                  1, // 1 second interval
			MaxBatchSize:           scaleLimit,
			MaxVerificationWorkers: 64,
		},
		Integrity: config.IntegrityConfig{
			Algorithm:            config.IntegritySize,
			VerificationAttempts: 0,
			VerificationInterval: 0,
		},
		Action: config.ActionConfig{
			Type:                  config.ActionScript, // Will be overridden
			ConcurrentConnections: 64,
			PostProcess: config.PostProcessConfig{
				Action:      config.PostActionDelete, // Delete files after execution
				ArchivePath: filepath.Join(filepath.Dir(testDir), "archive"),
			},
		},
	}

	engine, err := NewEngine(cfg, false)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	// Inject the mock action handler
	mockAction := &mockScaleActionHandler{}
	engine.handler = mockAction

	// Perform GC and capture baseline stats
	runtime.GC()
	var msBaseline runtime.MemStats
	runtime.ReadMemStats(&msBaseline)
	goroutinesBaseline := runtime.NumGoroutine()

	fmt.Printf("\n--- Baseline Metrics ---\n")
	fmt.Printf("Heap Alloc: %d MB\n", msBaseline.Alloc/1024/1024)
	fmt.Printf("Heap Sys:   %d MB\n", msBaseline.HeapSys/1024/1024)
	fmt.Printf("Goroutines: %d\n", goroutinesBaseline)
	fmt.Printf("------------------------\n\n")

	// Start engine in background
	ctx, cancel := context.WithCancel(context.Background())
	errChan := make(chan error, 1)

	startTime := time.Now()
	go func() {
		errChan <- engine.Run(ctx)
	}()

	// Wait for the files to be picked up, verified, and processed.
	// Since verification attempts = 1, interval = 1s, it should process quickly.
	// Monitor goroutines and memory during processing.
	var maxGoroutines int
	var maxAlloc uint64
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	// We expect the files to be deleted once processed.
	// We wait until the files directory is empty.
	timeout := time.After(180 * time.Second)
	completed := false

OuterLoop:
	for {
		select {
		case <-timeout:
			t.Error("timeout waiting for files to be processed")
			break OuterLoop
		case <-ticker.C:
			g := runtime.NumGoroutine()
			if g > maxGoroutines {
				maxGoroutines = g
			}

			var ms runtime.MemStats
			runtime.ReadMemStats(&ms)
			if ms.Alloc > maxAlloc {
				maxAlloc = ms.Alloc
			}

			// Check if files are all processed and deleted
			files, err := os.ReadDir(testDir)
			if err == nil && len(files) == 0 {
				completed = true
				break OuterLoop
			}
		}
	}

	cancel()
	<-errChan

	// Final GC and report stats
	runtime.GC()
	var msFinal runtime.MemStats
	runtime.ReadMemStats(&msFinal)

	duration := time.Since(startTime)

	fmt.Printf("\n--- Scale Execution Metrics (%d files) ---\n", scaleLimit)
	fmt.Printf("Duration:         %v\n", duration)
	fmt.Printf("Completed OK:     %t\n", completed)
	fmt.Printf("Processed Count:  %d\n", len(mockAction.processed))
	fmt.Printf("Max Goroutines:   %d (Baseline: %d)\n", maxGoroutines, goroutinesBaseline)
	fmt.Printf("Max Heap Alloc:   %d MB (Baseline: %d MB)\n", maxAlloc/1024/1024, msBaseline.Alloc/1024/1024)
	fmt.Printf("Final Heap Alloc: %d MB\n", msFinal.Alloc/1024/1024)
	fmt.Printf("Final Heap Sys:   %d MB\n", msFinal.HeapSys/1024/1024)
	fmt.Printf("-------------------------------------------\n\n")
}

type mockFileInfo struct {
	name string
}

func (m *mockFileInfo) Name() string       { return m.name }
func (m *mockFileInfo) Size() int64        { return 0 }
func (m *mockFileInfo) Mode() os.FileMode  { return 0 }
func (m *mockFileInfo) ModTime() time.Time { return time.Now() }
func (m *mockFileInfo) IsDir() bool        { return false }
func (m *mockFileInfo) Sys() interface{}   { return nil }
