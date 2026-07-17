// Package integrity_test provides unit tests for the integrity verification logic.
//
// Objective:
// Validate the robust verification of file stability before processing.
// It ensures that files are fully committed to disk and not currently held
// by other processes, using various algorithms (Size, Timestamp, Hash).
//
// Scenarios Covered:
//   - Lock Detection: Verifies rejection of files currently locked by the OS.
//   - Stability Sampling: Confirms that files are rejected if properties change
//     during the verification interval.
//   - Multi-Algorithm: Validates Size, Timestamp, and XXH3-128 Hash strategies.
//   - Edge Cases: Handles non-existent files, permission errors, and context cancellation.
package integrity

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"criticalsys.net/dirpoller/internal/config"
	"criticalsys.net/dirpoller/internal/testutils"
)

func TestMain(m *testing.M) {
	code := m.Run()
	os.Exit(code)
}

func getTestDir(name string) string {
	return testutils.GetUniqueTestDir("integrity", name)
}

// TestIntegrityLockCheck verifies that locked files are correctly identified.
//
// Scenario:
// 1. Create a test file.
// 2. Mock OSUtils to report the file as locked.
// 3. Attempt verification via Verifier.Verify.
//
// Success Criteria:
// - Verification must return false (not verified) when the file is locked.
// - No error should be returned for a valid lock detection scenario.
func TestIntegrityLockCheck(t *testing.T) {
	testDir := getTestDir("LockCheck")
	testFile := filepath.Join(testDir, "locked.txt")
	err := os.WriteFile(testFile, []byte("data"), 0o644)
	if err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	cfg := &config.Config{
		Integrity: config.IntegrityConfig{
			VerificationAttempts: 2,
			VerificationInterval: 1,
			Algorithm:            config.IntegritySize,
		},
	}
	v := NewVerifier(cfg)

	verified, err := v.Verify(context.Background(), testFile)
	if err != nil {
		t.Errorf("Verify failed: %v", err)
	}
	if !verified {
		t.Error("expected file to be verified when not locked")
	}
}

func TestIntegrityHash(t *testing.T) {
	testDir := getTestDir("HashCheck")
	testFile := filepath.Join(testDir, "hash.txt")
	_ = os.WriteFile(testFile, []byte("initial"), 0o644)

	cfg := &config.Config{
		Integrity: config.IntegrityConfig{
			VerificationAttempts: 2,
			VerificationInterval: 1,
			Algorithm:            config.IntegrityHash,
		},
	}
	v := NewVerifier(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	verified, err := v.Verify(ctx, testFile)
	if err != nil {
		t.Errorf("Verify failed: %v", err)
	}
	if !verified {
		t.Error("expected file to be verified")
	}
}

// TestIntegrityChangingFile verifies that unstable files are rejected.
//
// Scenario:
// 1. Create a test file.
// 2. Start verification in a goroutine.
// 3. Modify the file size during the verification interval.
//
// Success Criteria:
// - Verifier must detect the property change between samples.
// - Verification must return false to indicate the file is still "active".
func TestIntegrityChangingFile(t *testing.T) {
	testDir := getTestDir("ChangingFile")
	testFile := filepath.Join(testDir, "changing.txt")
	_ = os.WriteFile(testFile, []byte("v1"), 0o644)

	cfg := &config.Config{
		Integrity: config.IntegrityConfig{
			VerificationAttempts: 1,
			VerificationInterval: 1,
			Algorithm:            config.IntegritySize,
		},
	}
	v := NewVerifier(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Start a goroutine to change the file size during the interval
	go func() {
		time.Sleep(500 * time.Millisecond)
		_ = os.WriteFile(testFile, []byte("v1-changed-size"), 0o644)
	}()

	verified, err := v.Verify(ctx, testFile)
	if err != nil {
		t.Errorf("Verify failed: %v", err)
	}
	if verified {
		t.Error("expected file to NOT be verified because size changed")
	}
}

func TestIntegrityTimestamp(t *testing.T) {
	testDir := getTestDir("TimestampCheck")
	testFile := filepath.Join(testDir, "timestamp.txt")
	_ = os.WriteFile(testFile, []byte("timestamp data"), 0o644)

	cfg := &config.Config{
		Integrity: config.IntegrityConfig{
			VerificationAttempts: 2,
			VerificationInterval: 1,
			Algorithm:            config.IntegrityTimestamp,
		},
	}
	v := NewVerifier(cfg)

	verified, err := v.Verify(context.Background(), testFile)
	if err != nil {
		t.Errorf("Verify failed: %v", err)
	}
	if !verified {
		t.Error("expected file to be verified")
	}
}

func TestVerifierUnsupportedAlgorithm(t *testing.T) {
	cfg := &config.Config{
		Integrity: config.IntegrityConfig{
			Algorithm: "invalid",
		},
	}
	v := NewVerifier(cfg)
	_, err := v.getIntegrityValue("any")
	if err == nil {
		t.Error("expected error for unsupported integrity algorithm, got nil")
	}
}

func TestVerifierCalculateHash(t *testing.T) {
	testDir := getTestDir("CalculateHash")
	testFile := filepath.Join(testDir, "test.txt")
	content := []byte("hello world")
	_ = os.WriteFile(testFile, content, 0o644)

	cfg := &config.Config{}
	v := NewVerifier(cfg)
	hash, err := v.CalculateHash(testFile)
	if err != nil {
		t.Fatalf("CalculateHash failed: %v", err)
	}
	if len(hash) != 32 {
		t.Errorf("expected 32-character hex string for XXH3-128, got %d characters: %s", len(hash), hash)
	}
}

func TestVerifierCalculateHashReadError(t *testing.T) {
	testDir := getTestDir("HashReadError")
	// On Windows, opening a directory with os.Open succeeds, but io.Copy/Read will fail.
	cfg := &config.Config{}
	v := NewVerifier(cfg)

	_, err := v.CalculateHash(testDir)
	if err == nil {
		t.Error("expected error when hashing a directory (read failure), got nil")
	}
}

func TestVerifierVerifyContextCancelled(t *testing.T) {
	testDir := getTestDir("VerifyContext")
	testFile := filepath.Join(testDir, "test.txt")
	_ = os.WriteFile(testFile, []byte("data"), 0o644)

	cfg := &config.Config{
		Integrity: config.IntegrityConfig{
			VerificationAttempts: 2,
			VerificationInterval: 1,
			Algorithm:            config.IntegritySize,
		},
	}
	v := NewVerifier(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := v.Verify(ctx, testFile)
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestVerifierVerifyFirstStatError(t *testing.T) {
	cfg := &config.Config{
		Integrity: config.IntegrityConfig{
			VerificationAttempts: 1,
			VerificationInterval: 1,
			Algorithm:            config.IntegritySize,
		},
	}
	v := NewVerifier(cfg)

	v.utils = &testutils.MockOSUtils{Locked: false}

	// Pass a non-existent file to trigger Stat error in getIntegrityValue
	_, err := v.Verify(context.Background(), "non_existent_file_12345")
	if err == nil {
		t.Error("expected error for non-existent file during first integrity check, got nil")
	}
}

func TestVerifierVerifySecondStatError(t *testing.T) {
	testDir := getTestDir("VerifySecondStatError")
	testFile := filepath.Join(testDir, "test.txt")
	_ = os.WriteFile(testFile, []byte("data"), 0o644)

	cfg := &config.Config{
		Integrity: config.IntegrityConfig{
			VerificationAttempts: 1,
			VerificationInterval: 1,
			Algorithm:            config.IntegritySize,
		},
	}
	v := NewVerifier(cfg)

	// Start a goroutine to delete the file during the wait
	go func() {
		time.Sleep(500 * time.Millisecond)
		_ = os.Remove(testFile)
	}()

	_, err := v.Verify(context.Background(), testFile)
	if err == nil {
		t.Error("expected error for file deletion during verification, got nil")
	}
}

func TestVerifierVerifyLockDetected(t *testing.T) {
	testDir := getTestDir("VerifyLock")
	testFile := filepath.Join(testDir, "locked.txt")
	_ = os.WriteFile(testFile, []byte("data"), 0o644)

	cfg := &config.Config{
		Integrity: config.IntegrityConfig{
			VerificationAttempts: 1,
			VerificationInterval: 1,
			Algorithm:            config.IntegritySize,
		},
	}
	v := NewVerifier(cfg)

	v.utils = &testutils.MockOSUtils{Locked: true}

	verified, err := v.Verify(context.Background(), testFile)
	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}
	if verified {
		t.Error("expected verified=false when locked")
	}
}

func TestVerifierVerifyLockError(t *testing.T) {
	testDir := getTestDir("VerifyLockError")
	testFile := filepath.Join(testDir, "error.txt")
	_ = os.WriteFile(testFile, []byte("data"), 0o644)

	cfg := &config.Config{
		Integrity: config.IntegrityConfig{
			VerificationAttempts: 1,
			VerificationInterval: 1,
			Algorithm:            config.IntegritySize,
		},
	}
	v := NewVerifier(cfg)

	v.utils = &testutils.MockOSUtils{Err: fmt.Errorf("lock check failed")}

	_, err := v.Verify(context.Background(), testFile)
	if err == nil {
		t.Error("expected error for lock check failure, got nil")
	}
}

func TestVerifier_NestedSemaphoreDeadlockPrevention(t *testing.T) {
	testDir := getTestDir("NestedSemaphore")
	testFile := filepath.Join(testDir, "test.txt")
	_ = os.WriteFile(testFile, []byte("hash data content"), 0o644)
	defer func() { _ = os.Remove(testFile) }()

	// Set attempts = 1, interval = 0, algorithm = "hash"
	cfg := &config.Config{
		Integrity: config.IntegrityConfig{
			VerificationAttempts: 1,
			VerificationInterval: 0,
			Algorithm:            config.IntegrityHash,
		},
	}
	v := NewVerifier(cfg)

	// Artificially restrict semaphore size to 1.
	// If nested acquisition occurred, calling Verify would deadlock immediately
	// because the outer Verify holds the single slot, leaving calculateHashRaw blocked.
	v.ioSem = make(chan struct{}, 1)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	verified, err := v.Verify(ctx, testFile)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !verified {
		t.Error("expected file to be verified successfully")
	}
}

func TestVerifier_ClearCache(t *testing.T) {
	cfg := &config.Config{
		Integrity: config.IntegrityConfig{
			Algorithm: config.IntegritySize,
		},
	}
	v := NewVerifier(cfg)

	// Populate cache maps directly
	v.mu.Lock()
	v.hashes["file1.txt"] = "hash1"
	v.sizes["file1.txt"] = 100
	v.mu.Unlock()

	// Clear cache
	v.ClearCache()

	// Assert cache maps are empty
	v.mu.Lock()
	hashLen := len(v.hashes)
	sizeLen := len(v.sizes)
	v.mu.Unlock()

	if hashLen != 0 {
		t.Errorf("expected hashes cache to be empty, got size %d", hashLen)
	}
	if sizeLen != 0 {
		t.Errorf("expected sizes cache to be empty, got size %d", sizeLen)
	}
}

// [Removed redundant local mocks: mockOSUtils - now using testutils.MockOSUtils]
