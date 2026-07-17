// Package integrity provides logic for verifying that files are fully written and consistent.
//
// Objective:
// Ensure that files discovered by the poller are "stable" and fully committed to
// disk before processing. This prevents partial transfers of files that are still
// being written or are locked by other processes.
//
// Core Components:
// - Verifier: Orchestrates stability checks across multiple attempts.
// - OSUtils: Platform-specific logic for robust lock detection.
//
// Data Flow:
// 1. Discovery: The Engine receives a batch of files from the Poller.
// 2. Hand-off: The Engine passes file paths to the Verifier.
// 3. Lock Check: Verifier uses OSUtils.IsLocked to check for native file locks.
// 4. Stability Sampling: Verifier records a file property (size, timestamp, or hash).
// 5. Verification: After an interval, the property is sampled again and compared.
// 6. Approval: Returns true only if the file remains unchanged across N attempts.
package integrity

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"criticalsys.net/dirpoller/internal/config"
	"criticalsys.net/dirpoller/internal/poller"
	"github.com/zeebo/xxh3"
)

var bufPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, 1*1024*1024)
		return &b
	},
}

// Verifier orchestrates multiple attempts to ensure file integrity.
// A file is considered "stable" only if its property (size, timestamp, or hash)
// remains unchanged across N consecutive attempts at specific intervals.
type Verifier struct {
	cfg    *config.Config
	utils  poller.OSUtils
	mu     sync.Mutex
	hashes map[string]string
	sizes  map[string]int64
	ioSem  chan struct{} // Semaphore to throttle concurrent disk/network I/O
}

// NewVerifier creates a new integrity verifier instance.
func NewVerifier(cfg *config.Config) *Verifier {
	return &Verifier{
		cfg:    cfg,
		utils:  poller.NewOSUtils(0), // No limit needed for lock/stat checks on individual paths
		hashes: make(map[string]string),
		sizes:  make(map[string]int64),
		ioSem:  make(chan struct{}, 64), // Limit concurrent file descriptor/disk operations to 64
	}
}

// Verify checks file consistency across multiple attempts.
//
// Logic:
//  1. Lock Check: Uses platform-native APIs (e.g., Windows CreateFile with FILE_SHARE_NONE)
//     to see if the file is currently held by another process.
//  2. Initial Sample: Captures the configured integrity property (Size, Timestamp, or XXH3-128 Hash).
//  3. Interval Wait: Pauses execution for the configured VerificationInterval.
//  4. Comparison: Re-samples the property and compares with the previous value.
//  5. Success: Returns true if the property is stable across all configured VerificationAttempts.
func (v *Verifier) Verify(ctx context.Context, path string) (bool, error) {
	for i := 0; i < v.cfg.Integrity.VerificationAttempts; i++ {
		// Acquire I/O slot (with context cancellation check)
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case v.ioSem <- struct{}{}:
		}
		locked, err := v.utils.IsLocked(path)
		<-v.ioSem
		if err != nil {
			return false, fmt.Errorf("[Integrity:Verify] failed to check lock for %s: %w", path, err)
		}
		if locked {
			return false, nil // File is locked, retry later
		}

		// Acquire I/O slot
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case v.ioSem <- struct{}{}:
		}
		currentValue, err := v.getIntegrityValue(path)
		<-v.ioSem
		if err != nil {
			return false, err
		}

		// Wait for the configured interval (sleep is executed outside the I/O semaphore)
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(time.Duration(v.cfg.Integrity.VerificationInterval) * time.Second):
		}

		// Acquire I/O slot
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case v.ioSem <- struct{}{}:
		}
		newValue, err := v.getIntegrityValue(path)
		<-v.ioSem
		if err != nil {
			return false, err
		}

		if currentValue != newValue {
			return false, nil // File is still being modified
		}
	}

	return true, nil
}

func (v *Verifier) getIntegrityValue(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}

	v.mu.Lock()
	v.sizes[path] = info.Size()
	v.mu.Unlock()

	switch v.cfg.Integrity.Algorithm {
	case config.IntegritySize:
		return fmt.Sprintf("%d", info.Size()), nil
	case config.IntegrityTimestamp:
		return info.ModTime().String(), nil
	case config.IntegrityHash:
		return v.calculateHashRaw(path)
	default:
		return "", fmt.Errorf("[Integrity:Algorithm] unsupported integrity algorithm: %s", v.cfg.Integrity.Algorithm)
	}
}

// GetCachedSize returns the cached size of the file if available.
func (v *Verifier) GetCachedSize(path string) (int64, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	size, ok := v.sizes[path]
	return size, ok
}

// CalculateHash calculates the XXH3-128 of a file.
// This is used for both the stability check algorithm and for logging in the activity report.
func (v *Verifier) CalculateHash(path string) (string, error) {
	// Acquire I/O slot for public API throttling (not called inside Verify loop)
	v.ioSem <- struct{}{}
	defer func() { <-v.ioSem }()
	return v.calculateHashRaw(path)
}

func (v *Verifier) calculateHashRaw(path string) (string, error) {
	v.mu.Lock()
	if val, ok := v.hashes[path]; ok {
		v.mu.Unlock()
		return val, nil
	}
	v.mu.Unlock()

	f, err := os.Open(filepath.Clean(path)) // #nosec G304
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			log.Printf("Warning: failed to close file %s: %v\n", path, closeErr)
		}
	}()

	h := xxh3.New128()
	bufPtr := bufPool.Get().(*[]byte)
	defer bufPool.Put(bufPtr)
	buf := *bufPtr

	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return "", err
	}

	hash := hex.EncodeToString(h.Sum(nil))

	v.mu.Lock()
	v.hashes[path] = hash
	v.mu.Unlock()

	return hash, nil
}

// ClearCache flushes all cached hashes and sizes from memory.
func (v *Verifier) ClearCache() {
	v.mu.Lock()
	v.hashes = make(map[string]string)
	v.sizes = make(map[string]int64)
	v.mu.Unlock()
}
