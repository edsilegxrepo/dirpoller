// Package poller (Trigger) implements the signal-file detection strategy.
//
// Objective:
// Coordinate file processing based on the appearance of a specific "trigger"
// or "signal" file. This allows upstream systems to signal that a set of
// files is ready for processing.
//
// Data Flow:
// 1. Collection: Monitors the directory for any new file arrivals.
// 2. Pattern Matching: Checks every new file against the configured trigger pattern.
// 3. Trigger Detection: If the trigger file appears, all currently pending files are flushed.
// 4. Timeout Fallback: Forces a flush if the trigger never arrives within the timeout period.
package poller

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"criticalsys.net/dirpoller/internal/config"
	"github.com/fsnotify/fsnotify"
)

// TriggerPoller waits for a specific "trigger file" (exact name or wildcard) to appear.
// Once detected, it processes all other files currently pending in the directory.
// It also includes a timeout fallback to process files if the trigger never arrives.
type TriggerPoller struct {
	cfg          *config.Config
	utils        OSUtils
	mu           sync.Mutex
	files        map[string]struct{}
	newWatcher   func() (Watcher, error)
	hasBacklog   bool
	backlogUtils OSUtils
}

// NewTriggerPoller initializes a new TriggerPoller with pattern-based detection.
func NewTriggerPoller(cfg *config.Config) *TriggerPoller {
	return &TriggerPoller{
		cfg:          cfg,
		utils:        NewOSUtils(cfg.Poll.MaxBatchSize),
		backlogUtils: NewOSUtils(0), // Unlimited for backlog scans
		files:        make(map[string]struct{}),
		newWatcher: func() (Watcher, error) {
			return newRealWatcher()
		},
	}
}

// Start begins the trigger polling process.
//
// Objective: Process files only when a specific "ready" or "done" file pattern appears.
//
// Data Flow:
// 1. Initial Scan: Uses OSUtils to find existing files and checks for illegal subfolders.
// 2. Watcher: Initializes fsnotify to monitor for new (Create) or modified (Write) files.
// 3. Pattern Match: If a new file matches the trigger pattern (e.g., "ready.ok" or "*.done"), flush() is called.
// 4. Collection: Non-trigger files are stored in an internal map to ensure uniqueness.
// 5. Timeout Fallback: A ticker (cfg.Poll.BatchTimeoutSeconds) forces a flush if the trigger never arrives.
// 6. Flush: All collected file paths (excluding the trigger) are sent to the results channel.
func (p *TriggerPoller) Start(ctx context.Context, results chan<- []string) error {
	pattern, ok := p.cfg.Poll.Value.(string)
	if !ok {
		return &ErrWatcherInitialization{Err: fmt.Errorf("trigger pattern must be a string")}
	}

	watcher, err := p.newWatcher()
	if err != nil {
		return &ErrWatcherInitialization{Err: err}
	}
	defer func() {
		_ = watcher.Close()
	}()

	if err := watcher.Add(p.cfg.Poll.Directory); err != nil {
		return &ErrWatcherInitialization{Err: err}
	}

	// Initial scan (GetFiles also detects subfolders)
	initialFiles, err := p.utils.GetFiles(p.cfg.Poll.Directory)
	if err != nil {
		return err
	}
	p.mu.Lock()
	hasTrigger := false
	for _, f := range initialFiles {
		if p.isTriggerFile(f, pattern) {
			hasTrigger = true
		} else {
			p.files[f] = struct{}{}
		}
	}
	if len(initialFiles) >= p.cfg.Poll.MaxBatchSize {
		p.hasBacklog = true
	}
	if hasTrigger {
		p.flush(ctx, results)
	}
	p.mu.Unlock()

	timeoutDuration := time.Duration(p.cfg.Poll.BatchTimeoutSeconds) * time.Second
	if timeoutDuration <= 0 {
		timeoutDuration = 600 * time.Second
	}
	timeoutTicker := time.NewTicker(timeoutDuration)
	defer timeoutTicker.Stop()

	backlogTicker := time.NewTicker(2 * time.Second)
	defer backlogTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeoutTicker.C:
			p.mu.Lock()
			if len(p.files) > 0 {
				p.flush(ctx, results)
			}
			p.mu.Unlock()
		case <-backlogTicker.C:
			p.mu.Lock()
			if len(p.files) == 0 {
				backlog := p.scanBacklog()
				if len(backlog) > 0 {
					hasTrigger := false
					for _, f := range backlog {
						if p.isTriggerFile(f, pattern) {
							hasTrigger = true
						} else {
							p.files[f] = struct{}{}
						}
					}
					if hasTrigger && len(p.files) > 0 {
						p.flush(ctx, results)
					}
					if len(backlog) >= p.cfg.Poll.MaxBatchSize {
						p.hasBacklog = true
					} else {
						p.hasBacklog = false
					}
				} else {
					p.hasBacklog = false
				}
			}
			p.mu.Unlock()
		case event, ok := <-watcher.Events():
			if !ok {
				return nil
			}
			if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				p.mu.Lock()
				if p.isTriggerFile(event.Name, pattern) {
					p.flush(ctx, results)
					timeoutTicker.Reset(timeoutDuration)
				} else {
					// Check if it's a directory before adding to files
					stat, err := p.utils.Stat(event.Name)
					if err == nil && !stat.IsDir() {
						p.files[event.Name] = struct{}{}
					}
				}
				p.mu.Unlock()
			}
		case err, ok := <-watcher.Errors():
			if !ok {
				return nil
			}
			return &ErrWatcherRuntime{Err: err}
		}
	}
}

func (p *TriggerPoller) scanBacklog() []string {
	files, err := p.backlogUtils.GetFiles(p.cfg.Poll.Directory)
	if err != nil {
		return nil
	}
	var backlog []string
	for _, f := range files {
		if !IsInFlight(f) {
			backlog = append(backlog, f)
			if len(backlog) >= p.cfg.Poll.MaxBatchSize {
				break
			}
		}
	}
	return backlog
}

func (p *TriggerPoller) isTriggerFile(path, pattern string) bool {
	name := filepath.Base(path)
	match, err := filepath.Match(pattern, name)
	if err != nil {
		// If pattern is invalid, fallback to exact match or contains
		return name == pattern || strings.Contains(name, pattern)
	}
	return match
}

func (p *TriggerPoller) flush(ctx context.Context, results chan<- []string) {
	if len(p.files) == 0 {
		return
	}
	batch := make([]string, 0, len(p.files))
	for f := range p.files {
		batch = append(batch, f)
	}
	if len(batch) >= p.cfg.Poll.MaxBatchSize {
		p.hasBacklog = true
	}
	p.files = make(map[string]struct{})

	p.mu.Unlock()
	select {
	case results <- batch:
	case <-ctx.Done():
	}
	p.mu.Lock()
}
