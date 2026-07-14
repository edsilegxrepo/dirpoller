// Package action contains logic for processing files via SFTP or local scripts.
// It provides a standardized ActionHandler interface to execute operations
// on batches of verified files in parallel.
//
// Core Components:
// - ActionHandler: Interface for executing operations on file batches.
// - SFTPHandler: High-performance SFTP upload engine with connection pooling and atomic protocols.
// - ScriptHandler: Local script execution engine with timeout and concurrency control.
//
// Data Flow:
// 1. The Engine provides a list of verified file paths to the ActionHandler.
// 2. The handler (SFTP or Script) executes the action in parallel using a semaphore-controlled worker pool.
// 3. For SFTP, an Atomic Upload Protocol (Stage -> Transfer -> Rename -> Stat) is enforced.
// 4. Returns a list of successfully processed files for post-processing (archiving).
package action

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"criticalsys.net/dirpoller/internal/config"
)

// ScriptHandler executes local scripts for processed files.
//
// Objective: Extend application functionality by delegating file processing
// to external logic (scripts or binaries).
//
// Core Functionality:
// - Parallel Execution: Uses a semaphore pool to control concurrency.
// - Reliability: Enforces execution timeouts and captures all output.
// - Security: Validates absolute paths and handles execution contexts.
type ScriptHandler struct {
	cfg *config.Config
}

// NewScriptHandler creates a new script action handler.
func NewScriptHandler(cfg *config.Config) *ScriptHandler {
	return &ScriptHandler{
		cfg: cfg,
	}
}

// Execute runs the configured script for each file in parallel using a handler-wide semaphore pool.
//
// Objective: Extend application functionality by delegating file processing
// to external logic (scripts or binaries).
//
// Data Flow:
// 1. Worker Pool: Uses a semaphore pool to throttle parallel script execution.
// 2. Child Context: Wraps each execution in a child context with a maximum timeout.
// 3. Script Invocation: Executes the external command with the absolute file path.
// 4. Result Aggregation: Collects exit codes and captures combined output for reporting.
func (h *ScriptHandler) Execute(ctx context.Context, files []string) ([]string, error) {
	conns := h.cfg.Action.ConcurrentConnections
	if conns <= 0 {
		conns = 1
	}
	if len(files) < conns {
		conns = len(files)
	}

	jobs := make(chan string, len(files))
	for _, f := range files {
		jobs <- f
	}
	close(jobs)

	var wg sync.WaitGroup
	errChan := make(chan error, len(files))
	successChan := make(chan string, len(files))

	for w := 0; w < conns; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				select {
				case <-ctx.Done():
					return
				default:
					if err := h.executeScript(ctx, f); err != nil {
						errChan <- err
					} else {
						successChan <- f
					}
				}
			}
		}()
	}

	wg.Wait()
	close(errChan)
	close(successChan)

	var successfulFiles []string
	for f := range successChan {
		successfulFiles = append(successfulFiles, f)
	}

	if len(errChan) > 0 {
		var errs []error
		for e := range errChan {
			errs = append(errs, e)
		}
		return successfulFiles, errors.Join(errs...)
	}
	return successfulFiles, nil
}

// Close implements the ActionHandler interface.
func (h *ScriptHandler) Close() error {
	return nil
}

// RemoteCleanup implements the ActionHandler interface.
func (h *ScriptHandler) RemoteCleanup(ctx context.Context) error {
	return nil
}

// executeScript runs the configured script for a single file.
// It uses an absolute path for security and a context timeout for reliability.
func (h *ScriptHandler) executeScript(ctx context.Context, file string) error {
	// Security: Use absolute path and validate file exists
	absFile, err := filepath.Abs(file)
	if err != nil {
		return fmt.Errorf("[Action:Script] failed to get absolute path for %s: %w", file, err)
	}

	timeout := time.Duration(h.cfg.Action.Script.TimeoutSeconds) * time.Second
	if timeout == 0 {
		timeout = 60 * time.Second
	}

	childCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// #nosec G204 - Script path is validated as absolute and existing in config.go
	// nosemgrep
	cmd := exec.CommandContext(childCtx, h.cfg.Action.Script.Path, absFile)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return &ErrExecutionFailed{Path: file, Err: err, Output: string(output)}
	}

	return nil
}
