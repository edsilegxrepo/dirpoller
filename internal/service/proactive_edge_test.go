package service

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"criticalsys.net/dirpoller/internal/config"
	"criticalsys.net/dirpoller/internal/integrity"
	"criticalsys.net/dirpoller/internal/testutils"
)

// TestProactive_UnicodeAndSpecialFilenames verifies that files containing spaces,
// hash signs, parentheses, UTF-8 symbols, and non-ASCII characters are safely verified and processed.
func TestProactive_UnicodeAndSpecialFilenames(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("service", "unicode_special_names")
	_ = os.MkdirAll(testDir, 0o755)
	defer func() { _ = os.RemoveAll(testDir) }()

	specialNames := []string{
		"INV_#1&2 (2026).pdf",
		"file with spaces and #hash.txt",
		"report_ñ_测试_📊.csv",
		".hidden_leading_dot_file.dat",
	}

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory: testDir,
			Algorithm: config.PollInterval,
		},
		Integrity: config.IntegrityConfig{
			Algorithm:            config.IntegritySize,
			VerificationAttempts: 1,
			VerificationInterval: 1,
		},
		Action: config.ActionConfig{
			Type: config.ActionScript,
		},
	}

	v := integrity.NewVerifier(cfg)
	ctx := context.Background()

	for _, name := range specialNames {
		path := filepath.Join(testDir, name)
		if err := os.WriteFile(path, []byte("proactive edge test content"), 0o600); err != nil {
			t.Fatalf("failed to write file %s: %v", name, err)
		}

		verified, err := v.Verify(ctx, path)
		if err != nil {
			t.Errorf("expected no error verifying special filename %s, got %v", name, err)
		}
		if !verified {
			t.Errorf("expected file %s to be verified successfully", name)
		}
	}
}

// TestProactive_ZeroByteFiles verifies that empty 0-byte files pass integrity verification
// and cached size lookups without panics or division-by-zero errors.
func TestProactive_ZeroByteFiles(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("service", "zero_byte_files")
	_ = os.MkdirAll(testDir, 0o755)
	defer func() { _ = os.RemoveAll(testDir) }()

	emptyPath := filepath.Join(testDir, "empty_file.bin")
	if err := os.WriteFile(emptyPath, []byte{}, 0o600); err != nil {
		t.Fatalf("failed to write 0-byte file: %v", err)
	}

	cfg := &config.Config{
		Integrity: config.IntegrityConfig{
			Algorithm:            config.IntegritySize,
			VerificationAttempts: 1,
			VerificationInterval: 1,
		},
	}

	v := integrity.NewVerifier(cfg)
	verified, err := v.Verify(context.Background(), emptyPath)
	if err != nil {
		t.Fatalf("unexpected error verifying 0-byte file: %v", err)
	}
	if !verified {
		t.Errorf("expected 0-byte file to be verified, got false")
	}

	cachedSize, ok := v.GetCachedSize(emptyPath)
	if !ok || cachedSize != 0 {
		t.Errorf("expected cached size 0, got size=%d, ok=%v", cachedSize, ok)
	}
}

// TestProactive_SlowWriterLockDetection verifies that a file being actively appended to
// over time is correctly rejected during integrity verification while changing.
func TestProactive_SlowWriterLockDetection(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("service", "slow_writer")
	_ = os.MkdirAll(testDir, 0o755)
	defer func() { _ = os.RemoveAll(testDir) }()

	filePath := filepath.Join(testDir, "growing_file.log")
	if err := os.WriteFile(filePath, []byte("initial"), 0o600); err != nil {
		t.Fatalf("failed to write initial file: %v", err)
	}

	cfg := &config.Config{
		Integrity: config.IntegrityConfig{
			Algorithm:            config.IntegritySize,
			VerificationAttempts: 2,
			VerificationInterval: 1,
		},
	}

	v := integrity.NewVerifier(cfg)

	// Append data mid-verification
	stopAppender := make(chan struct{})
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stopAppender:
				return
			case <-ticker.C:
				f, err := os.OpenFile(filePath, os.O_APPEND|os.O_WRONLY, 0o600)
				if err == nil {
					_, _ = f.WriteString("chunk\n")
					_ = f.Close()
				}
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	verified, err := v.Verify(ctx, filePath)
	close(stopAppender)

	if err != nil && ctx.Err() == nil {
		t.Fatalf("unexpected error during slow writer test: %v", err)
	}
	if verified {
		t.Errorf("expected file being appended to to fail stability check, but got verified=true")
	}
}

// TestProactive_ResourceLeakCheck verifies that spawning and closing an Engine
// recovers goroutines back to baseline without leaking worker subroutines.
func TestProactive_ResourceLeakCheck(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("service", "resource_leak_check")
	_ = os.MkdirAll(testDir, 0o755)
	defer func() { _ = os.RemoveAll(testDir) }()

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory: testDir,
			Algorithm: config.PollInterval,
			Value:     1,
		},
		Integrity: config.IntegrityConfig{
			Algorithm:            config.IntegrityNone,
			VerificationAttempts: 0,
		},
		Action: config.ActionConfig{
			Type: config.ActionScript,
			Script: config.ScriptConfig{
				Path:           filepath.Join(testDir, "dummy.sh"),
				TimeoutSeconds: 5,
			},
			PostProcess: config.PostProcessConfig{
				Action:      config.PostActionDelete,
				ArchivePath: testDir,
			},
		},
	}

	// Create dummy script
	scriptPath := filepath.Join(testDir, "dummy.sh")
	if runtime.GOOS == "windows" {
		scriptPath = filepath.Join(testDir, "dummy.bat")
		cfg.Action.Script.Path = scriptPath
		_ = os.WriteFile(scriptPath, []byte("@echo off\nexit /b 0\n"), 0o755)
	} else {
		_ = os.WriteFile(scriptPath, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	}

	runtime.GC()
	initialGoroutines := runtime.NumGoroutine()

	engine, err := NewEngine(cfg, false)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errChan := make(chan error, 1)
	go func() {
		errChan <- engine.Run(ctx)
	}()

	time.Sleep(200 * time.Millisecond)
	cancel()
	engine.Close()
	<-errChan

	time.Sleep(300 * time.Millisecond)
	runtime.GC()
	finalGoroutines := runtime.NumGoroutine()

	// Allow a small delta (<= 2) for runtime/testing framework background routines
	if finalGoroutines > initialGoroutines+3 {
		t.Errorf("goroutine leak detected: initial=%d, final=%d", initialGoroutines, finalGoroutines)
	}
}
