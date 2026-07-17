// Package poller defines the interface and implementations for directory scanning strategies.
//
// Objective:
// Provide flexible, robust file discovery mechanisms while enforcing strict
// non-recursive directory monitoring constraints. It supports both legacy
// interval-based scanning and modern real-time event-driven detection.
//
// Core Components:
// - Poller Interface: Universal API for detection strategies (Interval, Batch, Event, Trigger).
// - OSUtils: Platform-native abstractions for lock detection and recursive safety.
// - Watcher: Interface for OS-native file system event monitoring (fsnotify).
//
// Data Flow:
// 1. Monitor: A Poller implementation (e.g., EventPoller) watches the source directory.
// 2. Discovery: Files are identified via initial scan or OS-native events.
// 3. Validation: OSUtils.HasSubfolders ensures no nested directories exist.
// 4. Batching: Files are collected until a threshold (Batch) or timeout (BatchTimeout) is met.
// 5. Hand-off: The resulting batch of file paths is sent to the results channel.
package poller

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

var (
	exclusionMu  sync.RWMutex
	exclusionMap = make(map[string]time.Time)

	inFlightMu  sync.RWMutex
	inFlightMap = make(map[string]struct{})
)

// AddInFlight marks a file path as currently being processed.
func AddInFlight(path string) {
	inFlightMu.Lock()
	defer inFlightMu.Unlock()
	inFlightMap[path] = struct{}{}
}

// RemoveInFlight unmarks a file path after processing.
func RemoveInFlight(path string) {
	inFlightMu.Lock()
	defer inFlightMu.Unlock()
	delete(inFlightMap, path)
}

// IsInFlight checks if a file path is currently being processed.
func IsInFlight(path string) bool {
	inFlightMu.RLock()
	defer inFlightMu.RUnlock()
	_, ok := inFlightMap[path]
	return ok
}

// ExcludePath adds a file path to the temporary exclusion cache with a TTL.
func ExcludePath(path string, ttl time.Duration) {
	exclusionMu.Lock()
	defer exclusionMu.Unlock()
	exclusionMap[path] = time.Now().Add(ttl)
}

// IsExcluded checks if a path is currently in the exclusion cache and not expired.
func IsExcluded(path string) bool {
	exclusionMu.RLock()
	defer exclusionMu.RUnlock()
	t, ok := exclusionMap[path]
	if !ok {
		return false
	}
	if time.Now().After(t) {
		return false
	}
	return true
}

// ClearExpiredExclusions purges all expired paths from the exclusion cache.
func ClearExpiredExclusions() {
	exclusionMu.Lock()
	defer exclusionMu.Unlock()
	now := time.Now()
	for k, v := range exclusionMap {
		if now.After(v) {
			delete(exclusionMap, k)
		}
	}
}

// Watcher defines the interface for file system event monitoring.
type Watcher interface {
	Add(name string) error
	Close() error
	Events() chan fsnotify.Event
	Errors() chan error
}

type realWatcher struct {
	*fsnotify.Watcher
}

func (w *realWatcher) Events() chan fsnotify.Event { return w.Watcher.Events }
func (w *realWatcher) Errors() chan error          { return w.Watcher.Errors }

func newRealWatcher() (Watcher, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &realWatcher{w}, nil
}

// Poller defines the interface for different polling algorithms.
//
// Objective:
// Standardize how different discovery strategies interact with the Engine.
// Each implementation is responsible for its own lifecycle management,
// error handling, and batching logic.
//
// Data Flow:
// 1. Start(): Begins the discovery loop (blocking).
// 2. Discovery: Identifies candidate files.
// 3. Filtering: Validates files for non-recursive safety.
// 4. Batching: Aggregates files based on algorithm-specific triggers.
// 5. Results: Dispatches slices of file paths to the provided channel.
type Poller interface {
	// Start begins the polling process and sends discovered files to the channel.
	// It should block until the context is cancelled or a fatal error occurs.
	//
	// Data Flow:
	// 1. Initial Scan: Get all files currently in the directory.
	// 2. Watcher: Subscribe to OS-native events (via fsnotify).
	// 3. Collection: Add new/changed files to an internal map.
	// 4. Threshold Check: If map size >= Batch count, call flush().
	// 5. Timeout: If BatchTimeoutSeconds passes, call flush().
	// 6. Flush: Send all map keys to the results channel and clear the map.
	Start(ctx context.Context, results chan<- []string) error
}

// OSUtils defines the interface for operating system specific file operations.
//
// Objective: Abstract platform-native file logic to maintain a cross-platform
// core while leveraging native high-performance APIs (e.g., Windows CreateFile).
//
// Methods:
// - IsLocked: Robust write-lock detection (using FILE_SHARE_NONE on Windows).
// - HasSubfolders: Enforces the non-recursive directory constraint.
// - GetFiles: Flat directory listing with recursive safety checks.
type OSUtils interface {
	// IsLocked returns true if the file is currently locked by another process.
	// On Windows, this typically uses CreateFile with FILE_SHARE_NONE.
	IsLocked(path string) (bool, error)

	// HasSubfolders returns true if any subdirectories exist within the given path.
	// This is used to enforce the non-recursive requirement specified in Section 1.
	HasSubfolders(path string) (bool, error)

	// GetFiles retrieves a list of all files in the directory.
	// It should return an error if a subfolder is detected to ensure non-recursive behavior.
	GetFiles(dir string) ([]string, error)

	// Stat returns os.FileInfo for the given path.
	Stat(path string) (os.FileInfo, error)
}

// NewOSUtils creates an OSUtils implementation for the current platform.
// The actual implementation is determined by build tags (e.g., poller_windows.go).
//
// Logic:
// 1. Windows: Uses native Win32 APIs (CreateFile) for robust lock detection.
// 2. Linux: Uses standard POSIX file operations.
func NewOSUtils(limit int) OSUtils {
	return newOSUtils(limit)
}
