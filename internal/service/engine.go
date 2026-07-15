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
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"criticalsys.net/dirpoller/internal/action"
	"criticalsys.net/dirpoller/internal/archive"
	"criticalsys.net/dirpoller/internal/config"
	"criticalsys.net/dirpoller/internal/integrity"
	"criticalsys.net/dirpoller/internal/poller"
)

var (
	// goos is used for platform-specific logic and can be overridden in tests.
	// [Refinement]: removed runtime.GOOS direct assignment here to rely on platform files where possible.
	// However, engine.go is shared and needs a default for non-service mode or testing.
	goos = ""

	// osStat allows mocking file metadata queries during tests to avoid filesystem latency.
	osStat = os.Stat
)

// FileVerifier defines the interface for ensuring files are fully committed.
type FileVerifier interface {
	Verify(ctx context.Context, path string) (bool, error)
	CalculateHash(path string) (string, error)
}

// PostArchiver defines the interface for post-processing successfully handled files.
type PostArchiver interface {
	Process(ctx context.Context, files []string) error
}

// ActivityLogger defines the interface for dual-track file-based logging.
type ActivityLogger interface {
	LogProcess(msg string) error
	LogExecution(summary ExecutionSummary) error
}

// EngineLogger defines the interface for engine-level event logging.
type EngineLogger interface {
	Error(id uint32, msg string) error
	Info(id uint32, msg string) error
	Warn(msg string)
	Close() error
}

// Engine orchestrates the polling, verification, and action execution lifecycle.
// It acts as the central controller, routing events between the poller, verifier, action handler, and loggers.
type Engine struct {
	cfg       *config.Config       // Application configuration
	poller    poller.Poller        // The selected polling algorithm (Interval, Batch, Event, or Trigger)
	verifier  FileVerifier         // Logic for ensuring files are fully committed
	handler   action.ActionHandler // The primary action (SFTP or local Script)
	archiver  PostArchiver         // Post-processing logic (Delete, Move, or Compress)
	isService bool                 // Indicates if running as a native Windows Service
	logger    EngineLogger         // System event logger (Windows Event Log or Console)
	customLog ActivityLogger       // File-based dual-track logger (if enabled)

	// testBackoff allows overriding the restart delay for unit tests
	testBackoff time.Duration

	// Internal factories for testing injection
	platLoggerFunc func(name string, isService bool) (Logger, error)

	// testLastCleanupDay allows overriding the daily task tracker for unit tests
	testLastCleanupDay int

	consecutivePollerFailures int // Counter for consecutive poller startup failures
}

// NewEngine initializes the core processing engine with configured components.
// It maps configuration directives to specific interface implementations and sets up
// the verification and action pipeline.
//
// Data Flow:
// 1. Secret Resolution: Decrypts SFTP passwords if configured.
// 2. Poller Selection: Maps the config algorithm to a specific Poller implementation.
// 3. Action Setup: Initializes the SFTP or Script handler.
// 4. Logging Setup: Configures system and activity loggers.
// 5. Cleanup Scheduling: Initiates the appropriate RemoteCleanup strategy (CLI vs Service).
func NewEngine(cfg *config.Config, isService bool) (*Engine, error) {
	e := &Engine{
		cfg:            cfg,
		isService:      isService,
		platLoggerFunc: NewPlatformLogger,
	}

	// Apply Go runtime GC/Memory limit tuning if configured
	if cfg.GCPercent != 0 {
		debug.SetGCPercent(cfg.GCPercent)
	}
	if cfg.MemoryLimitMB > 0 {
		debug.SetMemoryLimit(int64(cfg.MemoryLimitMB) * 1024 * 1024)
	}

	// 0. Handle Secret Decryption (SFTP Password)
	// [Refinement]: Removed startup decryption logic.
	// SFTPHandler.Execute now handles decryption per-batch to ensure
	// sensitive credentials only exist in memory during active execution.

	// 1. Initialize Polling Algorithm
	var p poller.Poller
	switch cfg.Poll.Algorithm {
	case config.PollInterval:
		p = poller.NewIntervalPoller(cfg)
	case config.PollBatch:
		p = poller.NewBatchPoller(cfg)
	case config.PollEvent:
		p = poller.NewEventPoller(cfg)
	case config.PollTrigger:
		p = poller.NewTriggerPoller(cfg)
	default:
		return nil, fmt.Errorf("unsupported poller algorithm: %s", cfg.Poll.Algorithm)
	}
	e.poller = p

	// 2. Initialize Primary Action Handler
	var handler action.ActionHandler
	switch cfg.Action.Type {
	case config.ActionSFTP:
		handler = action.NewSFTPHandler(cfg)
	case config.ActionScript:
		handler = action.NewScriptHandler(cfg)
	default:
		return nil, fmt.Errorf("unsupported action type: %s", cfg.Action.Type)
	}
	e.handler = handler

	// 3. Initialize System Logger (EventLog for Services, CLI for manual runs)
	if isService {
		// "DirPoller" is the registered event source name
		platLogger, err := e.platLoggerFunc("DirPoller", true)
		if err != nil {
			return nil, fmt.Errorf("failed to open platform logger: %w", err)
		}
		e.logger = &engineLoggerWrapper{Logger: platLogger}
	} else {
		e.logger = &cliLogger{}
	}

	// 4. Initialize Optional File-based Logger
	if len(cfg.Logging) > 0 {
		e.customLog = NewCustomLogger(cfg.Logging[0].LogName, cfg.Logging[0].LogRetention)
	}

	e.verifier = integrity.NewVerifier(cfg)
	e.archiver = archive.NewArchiver(cfg)

	// 5. Run RemoteCleanup (CLI only at startup, Service schedules it daily)
	if !isService {
		go func() {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
			defer cancel()
			if err := handler.RemoteCleanup(cleanupCtx); err != nil {
				e.logError(fmt.Sprintf("SFTP RemoteCleanup failed: %v", err))
			}
		}()
	}

	return e, nil
}

// engineLoggerWrapper wraps a Logger to implement EngineLogger.
type engineLoggerWrapper struct {
	Logger
}

func (w *engineLoggerWrapper) Warn(msg string) {
	_ = w.Info(2, "Warning: "+msg)
}

// NewEngineWithPlatLogger initializes the core processing engine with a custom platform logger factory.
// This is primarily used for testing error paths in platform logger initialization.
func NewEngineWithPlatLogger(cfg *config.Config, isService bool, platLoggerFunc func(name string, isService bool) (Logger, error)) (*Engine, error) {
	e, err := NewEngine(cfg, isService)
	if err != nil {
		return nil, err
	}
	e.platLoggerFunc = platLoggerFunc

	// If it's a service, we need to re-initialize the logger with the custom factory
	if isService {
		platLogger, err := e.platLoggerFunc("DirPoller", true)
		if err != nil {
			return nil, fmt.Errorf("failed to open platform logger: %w", err)
		}
		e.logger = &engineLoggerWrapper{Logger: platLogger}
	}

	return e, nil
}

// Run starts the infinite polling and processing loop.
//
// Objective:
// Maintain the continuous operation of the file processing pipeline. It manages
// the coordination between the asynchronous poller and the synchronous
// processing stages, while also handling scheduled system maintenance.
//
// Data Flow:
// 1. Poller Loop: Runs in a background goroutine, emitting batches to 'results'.
// 2. Signal Handling: Monitors for context cancellation or system errors.
// 3. Maintenance: Checks for day-change to trigger daily cleanup tasks.
// 4. Batch Processing: Invokes 'processFiles' for every batch received from the poller.
// 5. Recovery: Implements exponential backoff for poller restarts in service mode.
func (e *Engine) Run(ctx context.Context) error {
	e.logProcess("[Engine:Startup] Engine starting...")
	results := make(chan []string)
	errChan := make(chan error, 1)

	// Start the poller in its own goroutine
	go func() {
		if err := e.poller.Start(ctx, results); err != nil {
			errChan <- err
		}
	}()

	// Scheduled Tasks (Daily at 00:00:00)
	lastCleanupDay := -1
	if e.isService {
		lastCleanupDay = time.Now().YearDay()
	}
	if e.testLastCleanupDay != 0 {
		lastCleanupDay = e.testLastCleanupDay
	}

	for {
		// Check for daily 00:00:00 tasks in service mode
		if e.isService {
			now := time.Now()
			if now.YearDay() != lastCleanupDay {
				e.logProcess("[Engine:Schedule] Executing scheduled daily tasks (00:00:00)...")
				lastCleanupDay = now.YearDay()

				// 1. Remote SFTP Cleanup
				go func(ctx context.Context) {
					cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
					defer cancel()
					if err := e.handler.RemoteCleanup(cleanupCtx); err != nil {
						e.logError(fmt.Sprintf("[Engine:Schedule] Scheduled SFTP RemoteCleanup failed: %v", err))
					}
				}(ctx)
			}
		}

		select {
		case <-ctx.Done():
			e.logProcess("Engine stopping (context canceled)...")
			return ctx.Err()
		case err := <-errChan:
			var errMsg string
			var isPermanent bool

			// Leverage structured error types for recovery decisions
			switch e := err.(type) {
			case *poller.ErrSubfolderDetected:
				errMsg = fmt.Sprintf("[Engine:Poller] Critical: %v. Polling halted for this directory until resolved.", e)
				isPermanent = true
			case *poller.ErrWatcherInitialization:
				errMsg = fmt.Sprintf("[Engine:Poller] Watcher failed to start: %v", e)
			case *poller.ErrWatcherRuntime:
				errMsg = fmt.Sprintf("[Engine:Poller] Watcher encountered runtime error: %v", e)
			default:
				errMsg = fmt.Sprintf("[Engine:Poller] Poller error: %v", err)
			}

			e.logError(errMsg)

			if e.isService && err != context.Canceled && !isPermanent {
				// SECTION: Service Resilience
				// As per specs.txt, the service should log errors and continue.
				// We restart the poller to attempt recovery in the next cycle.
				// [Recommendation Impl]: Implement exponential backoff for restarts.
				e.consecutivePollerFailures++

				backoff := 5 * time.Second
				// Apply exponential factor with overflow protection and cap
				for i := 1; i < e.consecutivePollerFailures; i++ {
					nextBackoff := backoff * 2
					if nextBackoff > 1*time.Hour || nextBackoff < backoff {
						backoff = 1 * time.Hour
						break
					}
					backoff = nextBackoff
				}

				if strings.Contains(err.Error(), "permission denied") || strings.Contains(err.Error(), "access is denied") {
					if backoff < 30*time.Second {
						backoff = 30 * time.Second
					}
				}

				if e.testBackoff > 0 {
					backoff = e.testBackoff
				}
				e.logProcess(fmt.Sprintf("[Engine:Poller] Restarting poller in %v after error (failure #%d)...", backoff, e.consecutivePollerFailures))

				go func(delay time.Duration) {
					select {
					case <-ctx.Done():
						return
					case <-time.After(delay):
						if err := e.poller.Start(ctx, results); err != nil {
							errChan <- err
						}
					}
				}(backoff)
				continue
			}

			if isPermanent {
				e.logProcess("[Engine:Poller] Engine entering idle state due to permanent poller error.")
				// We don't return here in service mode to keep the service process alive,
				// but we stop the poller loop by not 'continue'-ing.
				// However, the Engine loop is infinite. To truly halt, we might need a signal.
				// For now, we'll just stop restarting the poller.
				continue
			}

			return err
		case files := <-results:
			// Reset failures on successful result receipt
			e.consecutivePollerFailures = 0
			// Pipeline step: Hand off discovered files for verification and execution
			e.processFiles(ctx, files)
		}
	}
}

// processFiles coordinates the concurrent verification and sequential execution of a file batch.
//
// Objective: Transition a batch of discovered files through the integrity and action pipeline.
//
// Pipeline Steps:
// 1. Concurrent Verification: Fan-out verification across N attempts (Lock check, Size, Hash).
// 2. Action Execution: Hand off verified files to the ActionHandler (SFTP or Script).
// 3. Post-Processing: Archive, Delete, or Compress files that were successfully handled.
// 4. Activity Reporting: Log the results of the entire batch execution to the activity log.
func (e *Engine) processFiles(ctx context.Context, files []string) {
	summary := ExecutionSummary{
		StartTime: time.Now(),
	}
	var verifiedFiles []string
	var wg sync.WaitGroup
	var mu sync.Mutex

	// STEP 1: CONCURRENT INTEGRITY VERIFICATION
	// Verification sleep is platform-independent and runs concurrently for all files.
	// Disk/Network operations are safely throttled inside the Verifier's semaphore.
	for _, f := range files {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()

			ok, err := e.verifier.Verify(ctx, path)
			if err != nil {
				poller.ExcludePath(path, 30*time.Second)
				// Individual file errors go to the Activity Log, not the System Event Log
				// We use a mutex to safely collect results from multiple goroutines.
				mu.Lock()
				summary.Errors = append(summary.Errors, FileProcessInfo{
					Path:  path,
					Error: err.Error(),
				})
				mu.Unlock()
				return
			}
			if ok {
				mu.Lock()
				verifiedFiles = append(verifiedFiles, path)
				mu.Unlock()
			} else {
				poller.ExcludePath(path, 30*time.Second)
			}
		}(f)
	}

	wg.Wait()

	// STEP 2: ACTION EXECUTION (SFTP OR SCRIPT)
	// Only files that passed the integrity check are processed.
	if len(verifiedFiles) > 0 {
		// The handler (SFTP or Script) manages its own internal concurrency pool.
		processedFiles, err := e.handler.Execute(ctx, verifiedFiles)
		if err != nil {
			// System-level action failures (e.g., SFTP connection down) are logged to the System Event Log/EventViewer.
			e.logError(fmt.Sprintf("[Engine:Action] Action execution failed: %v", err))
		}

		// Update activity summary with results from the handler (only if custom log is configured)
		if e.customLog != nil {
			successMap := make(map[string]bool)
			for _, f := range processedFiles {
				successMap[f] = true
				info := e.getFileInfo(f, "")
				summary.Processed = append(summary.Processed, info)
			}

			for _, f := range verifiedFiles {
				if !successMap[f] {
					info := e.getFileInfo(f, "action execution failed")
					summary.Errors = append(summary.Errors, info)
				}
			}
		}

		// STEP 3: POST-PROCESSING (ARCHIVE/DELETE)
		// Only performed on files successfully handled in Step 2 to ensure no data loss.
		if len(processedFiles) > 0 {
			if err := e.archiver.Process(ctx, processedFiles); err != nil {
				e.logError(fmt.Sprintf("[Engine:Archive] Post-processing failed: %v", err))
			}
		}
	}

	// STEP 4: ACTIVITY LOGGING
	// Writes the per-execution report to the file system (if configured).
	if e.customLog != nil {
		if err := e.customLog.LogExecution(summary); err != nil {
			e.logError(fmt.Sprintf("[Engine:Logging] Failed to write activity log: %v", err))
		}
	}

	// Clear the verifier cache at the end of the cycle to prevent memory leaks
	if v, ok := e.verifier.(*integrity.Verifier); ok {
		v.ClearCache()
	}
	poller.ClearExpiredExclusions()
}

func (e *Engine) getFileInfo(path string, errMsg string) FileProcessInfo {
	info := FileProcessInfo{
		Path:  path,
		Error: errMsg,
	}
	// Use absolute path for consistency if possible, otherwise keep original
	if filepath.IsAbs(path) {
		info.Path = path
	} else {
		absPath, err := filepath.Abs(path)
		if err == nil {
			info.Path = absPath
		}
	}

	// 1. Retrieve size from cache to avoid disk reads
	var size int64
	var sizeCached bool
	if v, ok := e.verifier.(*integrity.Verifier); ok {
		size, sizeCached = v.GetCachedSize(path)
	}
	if sizeCached {
		info.Size = size
	} else {
		stat, err := osStat(path)
		if err == nil {
			info.Size = stat.Size()
		}
	}

	// 2. Only compute hash if integrity hashing is enabled to avoid high I/O overhead.
	// If e.cfg is nil (e.g. manually constructed in tests), we calculate it by default.
	if e.cfg == nil || e.cfg.Integrity.Algorithm == config.IntegrityHash {
		hash, err := e.verifier.CalculateHash(path)
		if err == nil {
			info.Hash = hash
		}
	} else {
		info.Hash = ""
	}

	return info
}

func (e *Engine) logError(msg string) {
	if e != nil {
		e.logProcess("ERROR: " + msg)
		if e.logger != nil {
			if err := e.logger.Error(1, msg); err != nil {
				log.Printf("Failed to log error: %v (message: %s)", err, msg)
			}
		} else {
			log.Println(msg)
		}
	}
}

func (e *Engine) logProcess(msg string) {
	if e != nil && e.customLog != nil {
		if err := e.customLog.LogProcess(msg); err != nil {
			log.Printf("Failed to write process log: %v (message: %s)", err, msg)
		}
	}
}

type cliLogger struct{}

func (l *cliLogger) Error(id uint32, msg string) error {
	log.Println("ERROR:", msg)
	return nil
}

func (l *cliLogger) Info(id uint32, msg string) error {
	log.Println("INFO:", msg)
	return nil
}

func (l *cliLogger) Warn(msg string) {
	log.Println("WARN:", msg)
}

func (l *cliLogger) Close() error {
	return nil
}

func (e *Engine) Close() {
	if e.logger != nil {
		if err := e.logger.Close(); err != nil {
			log.Printf("Warning: failed to close system logger: %v", err)
		}
	}
	if e.handler != nil {
		if err := e.handler.Close(); err != nil {
			e.logError(fmt.Sprintf("Warning: failed to close action handler: %v", err))
		}
	}
}
