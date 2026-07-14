// Package poller (Batch) implements the volume-based batching strategy.
//
// Objective:
// Aggregates discovered files into batches of a specific size before
// triggering processing. This is optimized for high-volume scenarios
// where individual file processing would be inefficient.
//
// Data Flow:
// 1. Monitoring: Combines initial directory scan with real-time fsnotify events.
// 2. Collection: Stores unique file paths in an internal map.
// 3. Threshold Check: Triggers a flush when the map size reaches the configured limit.
// 4. Timeout Fallback: Ensures no files are stranded by flushing after a period of inactivity.
package poller

import (
	"context"
	"sync"
	"time"

	"criticalsys.net/dirpoller/internal/config"
	"github.com/fsnotify/fsnotify"
)

// BatchPoller collects files as they arrive but waits until a specific volume (file count)
// is reached before executing actions. It uses file system notifications for low-latency detection
// and a timeout fallback to ensure files are not stranded.
type BatchPoller struct {
	cfg        *config.Config
	utils      OSUtils
	mu         sync.Mutex
	files      map[string]struct{}
	newWatcher func() (Watcher, error)
}

// NewBatchPoller initializes a new BatchPoller with native OS utilities.
func NewBatchPoller(cfg *config.Config) *BatchPoller {
	return &BatchPoller{
		cfg:   cfg,
		utils: NewOSUtils(cfg.Poll.MaxBatchSize),
		files: make(map[string]struct{}),
		newWatcher: func() (Watcher, error) {
			return newRealWatcher()
		},
	}
}

// Start begins the batch polling process.
//
// Data Flow:
// 1. Initial Scan: Populate internal list with current files.
// 2. Event Watcher: Background goroutine listens for new file creations.
// 3. Batch Logic: If file count >= threshold, call flush().
// 4. Fallback: If BatchTimeoutSeconds passes without reaching threshold, call flush().
func (p *BatchPoller) Start(ctx context.Context, results chan<- []string) error {
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
	for _, f := range initialFiles {
		p.files[f] = struct{}{}
	}
	p.checkThreshold(results)
	p.mu.Unlock()

	timeoutTicker := time.NewTicker(time.Duration(p.cfg.Poll.BatchTimeoutSeconds) * time.Second)
	defer timeoutTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeoutTicker.C:
			p.mu.Lock()
			if len(p.files) > 0 {
				p.flush(results)
			}
			p.mu.Unlock()
		case event, ok := <-watcher.Events():
			if !ok {
				return nil
			}
			if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				p.mu.Lock()
				// Check if it's a directory before adding to files
				stat, err := p.utils.Stat(event.Name)
				if err == nil && stat.IsDir() {
					p.mu.Unlock()
					return &ErrSubfolderDetected{Path: event.Name}
				}
				p.files[event.Name] = struct{}{}
				if p.checkThreshold(results) {
					timeoutTicker.Reset(time.Duration(p.cfg.Poll.BatchTimeoutSeconds) * time.Second)
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

func (p *BatchPoller) checkThreshold(results chan<- []string) bool {
	threshold, ok := p.cfg.Poll.Value.(int)
	if !ok {
		// Default to 1 if not an int
		threshold = 1
	}
	if len(p.files) >= threshold {
		p.flush(results)
		return true
	}
	return false
}

// flush sends all currently collected files as a single batch and clears the internal map.
func (p *BatchPoller) flush(results chan<- []string) {
	if len(p.files) == 0 {
		return
	}
	batch := make([]string, 0, len(p.files))
	for f := range p.files {
		batch = append(batch, f)
	}
	// Security: Dispatch in a goroutine to prevent blocking the poller loop
	// when the results channel consumer is slow.
	go func(b []string) {
		results <- b
	}(batch)
	p.files = make(map[string]struct{})
}
