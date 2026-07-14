// Package poller (Event) implements the real-time event-driven detection strategy.
//
// Objective:
// Minimize detection latency by leveraging platform-native file system
// notification APIs (ReadDirectoryChangesW on Windows, inotify on Linux).
// It is the most efficient strategy for local disks with high performance requirements.
//
// Data Flow:
// 1. Initial Scan: Emits existing files to ensure no data is missed on startup.
// 2. Event Loop: Listens for OS-native file creation and modification events.
// 3. Debouncing: Uses an internal LRU cache and timer to suppress duplicate events.
// 4. Dispatch: Sends discovered paths immediately to the Engine for verification.
package poller

import (
	"container/list"
	"context"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"criticalsys.net/dirpoller/internal/config"
	"github.com/fsnotify/fsnotify"
)

// EventPoller uses Windows-native ReadDirectoryChangesW (via fsnotify) for real-time detection.
// It is optimized for high-traffic local disks where minimal latency is required.
type EventPoller struct {
	cfg        *config.Config
	utils      OSUtils
	mu         sync.Mutex
	newWatcher func() (Watcher, error)
	processed  map[string]*list.Element
	lruList    *list.List
	maxCache   int
}

// NewEventPoller initializes a new EventPoller with real-time detection capabilities.
func NewEventPoller(cfg *config.Config) *EventPoller {
	return &EventPoller{
		cfg:   cfg,
		utils: NewOSUtils(cfg.Poll.MaxBatchSize),
		newWatcher: func() (Watcher, error) {
			return newRealWatcher()
		},
		processed: make(map[string]*list.Element),
		lruList:   list.New(),
		maxCache:  10000, // Bound the cache to 10k entries to prevent memory leaks
	}
}

type cacheEntry struct {
	name string
	time time.Time
}

// Start begins the event polling process.
//
// Data Flow:
// 1. Initial Scan: Emits existing files in the directory.
// 2. OS Events: Listens for CREATE, WRITE, and RENAME events via fsnotify.
// 3. Debounce: Uses a 500ms timer to prevent multiple triggers for a single multi-stage write.
// 4. Verification Hand-off: Sends paths to the Engine's verification pipeline.
func (p *EventPoller) Start(ctx context.Context, results chan<- []string) error {
	watcher, err := p.newWatcher()
	if err != nil {
		return &ErrWatcherInitialization{Err: err}
	}
	defer func() {
		if closeErr := watcher.Close(); closeErr != nil {
			log.Printf("Warning: failed to close watcher: %v\n", closeErr)
		}
	}()

	const debounceInterval = 500 * time.Millisecond
	const cleanupInterval = 5 * time.Minute
	cleanupTicker := time.NewTicker(cleanupInterval)
	defer cleanupTicker.Stop()

	// Add the directory to the watcher
	if err := watcher.Add(p.cfg.Poll.Directory); err != nil {
		return &ErrWatcherInitialization{Err: err}
	}

	// Process existing files first (GetFiles also checks for subfolders)
	files, err := p.utils.GetFiles(p.cfg.Poll.Directory)
	if err != nil {
		return err
	}
	if len(files) > 0 {
		results <- files
	}

	// Micro-batching/coalescing variables
	var pendingBatch []string
	var flushTimer *time.Timer
	var flushChan <-chan time.Time

	// Safe timer shutdown helper
	stopTimer := func() {
		if flushTimer != nil {
			if !flushTimer.Stop() {
				select {
				case <-flushTimer.C:
				default:
				}
			}
			flushTimer = nil
			flushChan = nil
		}
	}
	defer stopTimer()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-cleanupTicker.C:
			p.mu.Lock()
			for name, element := range p.processed {
				entry := element.Value.(*cacheEntry)
				if time.Since(entry.time) > debounceInterval*2 {
					p.lruList.Remove(element)
					delete(p.processed, name)
				}
			}
			p.mu.Unlock()

		case <-flushChan:
			if len(pendingBatch) > 0 {
				go func(b []string) {
					select {
					case results <- b:
					case <-ctx.Done():
					}
				}(pendingBatch)
				pendingBatch = nil
			}
			flushTimer = nil
			flushChan = nil

		case event, ok := <-watcher.Events():
			if !ok {
				return nil
			}

			// If a new directory is created, we must abort
			if event.Op&fsnotify.Create == fsnotify.Create {
				info, err := filepath.Abs(event.Name)
				if err == nil {
					stat, err := p.utils.Stat(info)
					if err == nil && stat.IsDir() {
						return &ErrSubfolderDetected{Path: event.Name}
					}
				}
			}

			// When a file is written or created, we send it for integrity check
			if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				p.mu.Lock()
				var lastTime time.Time
				if element, ok := p.processed[event.Name]; ok {
					lastTime = element.Value.(*cacheEntry).time
					p.lruList.MoveToFront(element)
					element.Value.(*cacheEntry).time = time.Now()
				} else {
					// Add to LRU cache
					if p.lruList.Len() >= p.maxCache {
						oldest := p.lruList.Back()
						if oldest != nil {
							p.lruList.Remove(oldest)
							delete(p.processed, oldest.Value.(*cacheEntry).name)
						}
					}
					entry := &cacheEntry{name: event.Name, time: time.Now()}
					element := p.lruList.PushFront(entry)
					p.processed[event.Name] = element
				}

				var shouldSend bool
				if lastTime.IsZero() || time.Since(lastTime) > debounceInterval {
					shouldSend = true
				}
				p.mu.Unlock()

				if shouldSend {
					pendingBatch = append(pendingBatch, event.Name)
					if len(pendingBatch) >= p.cfg.Poll.MaxBatchSize {
						go func(b []string) {
							select {
							case results <- b:
							case <-ctx.Done():
							}
						}(pendingBatch)
						pendingBatch = nil
						stopTimer()
					} else if flushTimer == nil {
						flushTimer = time.NewTimer(50 * time.Millisecond)
						flushChan = flushTimer.C
					}
				}
			}

		case err, ok := <-watcher.Errors():
			if !ok {
				return nil
			}
			errMsg := strings.ToLower(err.Error())
			isOverflow := strings.Contains(errMsg, "overflow") ||
				strings.Contains(errMsg, "short buffer") ||
				strings.Contains(errMsg, "buffer limit") ||
				strings.Contains(errMsg, "too many open files")

			if isOverflow {
				log.Printf("Warning: Directory watcher encountered overflow runtime error: %v. Initiating catch-up directory scan...\n", err)
				files, scanErr := p.utils.GetFiles(p.cfg.Poll.Directory)
				if scanErr == nil && len(files) > 0 {
					select {
					case results <- files:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
			} else {
				return &ErrWatcherRuntime{Err: err}
			}
		}
	}
}
