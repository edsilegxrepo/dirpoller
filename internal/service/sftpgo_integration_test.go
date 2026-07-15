package service

import (
	"context"
	"criticalsys/secretprotector/pkg/libsecsecrets"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"criticalsys.net/dirpoller/internal/config"
)

// TestLiveSFTPGoIntegration verifies end-to-end SFTP uploading against a real running SFTPGo instance.
// Run this test with:
//
//	$env:TEST_LIVE_SFTP="true"; go test -v -run=TestLiveSFTPGoIntegration ./internal/service
func TestLiveSFTPGoIntegration(t *testing.T) {
	if os.Getenv("TEST_LIVE_SFTP") != "true" {
		t.Skip("Skipping live SFTP test. Set TEST_LIVE_SFTP=true to run.")
	}

	tempDir := os.Getenv("TEMP")
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	timestamp := time.Now().Format("20060102150405")
	sftpHome := filepath.Join(tempDir, "unitests", "sftpgo_home-"+timestamp)
	pollDir := filepath.Join(tempDir, "unitests", "poll_dir-"+timestamp)
	archiveDir := filepath.Join(tempDir, "unitests", "archive-"+timestamp)

	if err := os.MkdirAll(sftpHome, 0o750); err != nil {
		t.Fatalf("failed to create sftpgo home dir: %v", err)
	}
	if err := os.MkdirAll(pollDir, 0o750); err != nil {
		t.Fatalf("failed to create poll dir: %v", err)
	}
	defer func() {
		_ = fastRemoveAll(sftpHome)
		_ = fastRemoveAll(pollDir)
		_ = fastRemoveAll(archiveDir)
	}()

	// 1. Generate master key and encrypt password
	masterKeyStr, _ := libsecsecrets.GenerateKey()
	masterKey, _ := libsecsecrets.ResolveKey(context.Background(), masterKeyStr, "", "")
	encPass, _ := libsecsecrets.Encrypt(context.Background(), "password123", masterKey)
	libsecsecrets.ZeroBuffer(masterKey)

	_ = os.Setenv("SECRETPROTECTOR_KEY", masterKeyStr)
	defer func() { _ = os.Unsetenv("SECRETPROTECTOR_KEY") }()

	// 2. Start SFTPGo in portable mode in the background
	sftpgoPath := getSFTPGoPath()
	port := getSFTPGoPort()
	cmd := exec.Command(sftpgoPath, "portable",
		"-d", sftpHome,
		"-u", "testuser",
		"-p", "password123",
		"-s", strconv.Itoa(port),
		"-g", "*",
	)

	// Redirect output to avoid cluttering test output
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Dir = sftpHome

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start SFTPGo binary at %s: %v", sftpgoPath, err)
	}
	defer func() {
		_ = cmd.Process.Kill()
	}()

	// Wait 2 seconds for SFTPGo server to bind and start listening
	time.Sleep(2 * time.Second)

	// 3. Prepare config pointing to local SFTPGo instance
	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:              pollDir,
			Algorithm:              config.PollInterval,
			Value:                  1,
			MaxBatchSize:           10,
			MaxVerificationWorkers: 2,
		},
		Integrity: config.IntegrityConfig{
			Algorithm:            config.IntegritySize,
			VerificationAttempts: 0,
			VerificationInterval: 0,
		},
		Action: config.ActionConfig{
			Type:                  config.ActionSFTP,
			ConcurrentConnections: 2,
			SFTP: config.SFTPConfig{
				Host:              "127.0.0.1",
				Port:              port,
				Username:          "testuser",
				EncryptedPassword: encPass,
				MasterKeyEnv:      "SECRETPROTECTOR_KEY",
				RemotePath:        "/",
			},
			PostProcess: config.PostProcessConfig{
				Action:      config.PostActionDelete,
				ArchivePath: archiveDir,
			},
		},
	}

	engine, err := NewEngine(cfg, false)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	// 3. Start Engine in background
	ctx, cancel := context.WithCancel(context.Background())
	errChan := make(chan error, 1)
	go func() {
		errChan <- engine.Run(ctx)
	}()

	// 4. Create a test file to poll/upload
	testFile := filepath.Join(pollDir, "test_sftpgo_upload.txt")
	testData := []byte("hello sftpgo live integration test data")
	if err := os.WriteFile(testFile, testData, 0o600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// 5. Poll and wait for upload success
	timeout := time.After(10 * time.Second)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	success := false
	for {
		select {
		case <-timeout:
			t.Error("timeout waiting for file to upload to SFTPGo")
			goto ExitLoop
		case <-ticker.C:
			// Check if file arrived at SFTPGo directory
			uploadedFile := filepath.Join(sftpHome, "test_sftpgo_upload.txt")
			if _, err := os.Stat(uploadedFile); err == nil {
				// Verify local file is cleaned up
				if _, err := os.Stat(testFile); os.IsNotExist(err) {
					success = true
					goto ExitLoop
				}
			}
		}
	}

ExitLoop:
	cancel()
	<-errChan

	if !success {
		t.Fatal("SFTPGo live integration test failed")
	}
	t.Log("SFTPGo live integration test passed successfully!")
}

// TestLiveSFTPGo5KIntegration performs a realistic production load simulation of 5,000 files,
// testing 100% functionality including file lock detection, retry recovery, and timing metrics.
// Run this test with:
//
//	$env:TEST_LIVE_SFTP_5K="true"; go test -v -run=TestLiveSFTPGo5KIntegration ./internal/service
func TestLiveSFTPGo5KIntegration(t *testing.T) {
	if os.Getenv("TEST_LIVE_SFTP_5K") != "true" {
		t.Skip("Skipping live 5K SFTP test. Set TEST_LIVE_SFTP_5K=true to run.")
	}

	var totalArchivedFiles int
	var sftpFileCount int
	var archiveEntries []os.DirEntry
	var sftpEntries []os.DirEntry
	var totalTime time.Duration
	var phase1Time time.Duration
	var phase2Time time.Duration
	var phase2StartTime time.Time
	var phase2Done bool

	tempDir := os.Getenv("TEMP")
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	timestamp := time.Now().Format("20060102150405")
	sftpHome := filepath.Join(tempDir, "unitests", "sftpgo_home_5k-"+timestamp)
	pollDir := filepath.Join(tempDir, "unitests", "poll_dir_5k-"+timestamp)
	archiveDir := filepath.Join(tempDir, "unitests", "archive_5k-"+timestamp)

	if err := os.MkdirAll(sftpHome, 0o750); err != nil {
		t.Fatalf("failed to create sftpgo home dir: %v", err)
	}
	if err := os.MkdirAll(pollDir, 0o750); err != nil {
		t.Fatalf("failed to create poll dir: %v", err)
	}
	defer func() {
		_ = fastRemoveAll(sftpHome)
		_ = fastRemoveAll(pollDir)
		_ = fastRemoveAll(archiveDir)
	}()

	fmt.Printf("[Live5K] Generating 5,000 test files (4,900 unlocked, 100 locked exclusively)...\n")
	dummyContent := make([]byte, 10240) // 10KB dummy content
	for i := range dummyContent {
		dummyContent[i] = 'A'
	}

	// 1. Generate 4,900 unlocked files
	for i := 0; i < 4900; i++ {
		path := filepath.Join(pollDir, fmt.Sprintf("unlocked_invoice_%d.txt", i))
		if err := os.WriteFile(path, dummyContent, 0o600); err != nil {
			t.Fatalf("failed to create unlocked test file %d: %v", i, err)
		}
	}

	// 2. Generate 100 locked files and hold lock handles
	lockClosers := make([]io.Closer, 0, 100)
	for i := 0; i < 100; i++ {
		path := filepath.Join(pollDir, fmt.Sprintf("locked_invoice_%d.txt", i))
		if err := os.WriteFile(path, dummyContent, 0o600); err != nil {
			t.Fatalf("failed to create locked test file %d: %v", i, err)
		}
		closer, err := lockFileExclusively(path)
		if err != nil {
			t.Fatalf("failed to lock file %d: %v", i, err)
		}
		lockClosers = append(lockClosers, closer)
	}
	defer func() {
		for _, c := range lockClosers {
			_ = c.Close()
		}
	}()

	// 3. Generate master key and encrypt password
	masterKeyStr, _ := libsecsecrets.GenerateKey()
	masterKey, _ := libsecsecrets.ResolveKey(context.Background(), masterKeyStr, "", "")
	encPass, _ := libsecsecrets.Encrypt(context.Background(), "password123", masterKey)
	libsecsecrets.ZeroBuffer(masterKey)

	_ = os.Setenv("SECRETPROTECTOR_KEY", masterKeyStr)
	defer func() { _ = os.Unsetenv("SECRETPROTECTOR_KEY") }()

	// 4. Start SFTPGo portable server
	sftpgoPath := getSFTPGoPath()
	port := getSFTPGoPort()
	cmd := exec.Command(sftpgoPath, "portable",
		"-d", sftpHome,
		"-u", "testuser",
		"-p", "password123",
		"-s", strconv.Itoa(port),
		"-g", "*",
	)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Dir = sftpHome

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start SFTPGo: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
	}()

	// Wait 2 seconds for SFTPGo server to start listening
	time.Sleep(2 * time.Second)

	// 5. Build configuration mimicking production load
	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:              pollDir,
			Algorithm:              config.PollInterval,
			Value:                  1,
			MaxBatchSize:           5000, // Batch contains all 5000 files
			MaxVerificationWorkers: 64,   // This defaults to concurrent sleeps now
		},
		Integrity: config.IntegrityConfig{
			Algorithm:            config.IntegritySize,
			VerificationAttempts: 1, // Mimicking user configuration
			VerificationInterval: 1,
		},
		Action: config.ActionConfig{
			Type:                  config.ActionSFTP,
			ConcurrentConnections: 16, // Mimicking user sftp concurrent conns
			SFTP: config.SFTPConfig{
				Host:              "127.0.0.1",
				Port:              port,
				Username:          "testuser",
				EncryptedPassword: encPass,
				MasterKeyEnv:      "SECRETPROTECTOR_KEY",
				RemotePath:        "/",
			},
			PostProcess: config.PostProcessConfig{
				Action:      config.PostActionMoveArchive,
				ArchivePath: archiveDir,
			},
		},
	}

	engine, err := NewEngine(cfg, false)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	// 6. Start Engine in background
	ctx, cancel := context.WithCancel(context.Background())
	errChan := make(chan error, 1)
	go func() {
		errChan <- engine.Run(ctx)
	}()

	startTime := time.Now()

	// 7. Verify Phase 1: Check that all 4,900 unlocked files are processed, and 100 locked files are skipped.
	fmt.Printf("[Live5K] Waiting for Phase 1 (unlocked files processed, locked files skipped)...\n")
	timeout := time.After(180 * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	phase1Done := false
	for {
		select {
		case <-timeout:
			t.Error("timeout waiting for Phase 1 to complete")
			goto CleanupAndExit
		case <-ticker.C:
			// Read remaining files in polling directory
			entries, err := os.ReadDir(pollDir)
			if err != nil {
				t.Fatalf("failed to read poll dir: %v", err)
			}
			// Once only 100 locked files remain in the pollDir, Phase 1 is done
			if len(entries) == 100 {
				phase1Done = true
				goto Phase1Complete
			}
		}
	}

Phase1Complete:
	phase1Time = time.Since(startTime)
	if !phase1Done {
		t.Fatalf("Phase 1 verification failed")
	}
	fmt.Printf("[Live5K] Phase 1 completed successfully in %v (4,900 files uploaded & archived!).\n", phase1Time.Round(time.Millisecond))

	// STRICT PERFORMANCE SLA ASSERTION:
	// Verification sleep takes 1s. Uploads (4900 loopback files on 16 conns) should take <60s.
	// Total budget is 90s. Exceeding this indicates a sequential sleep or uploader regression.
	if phase1Time > 90*time.Second {
		t.Errorf("PERFORMANCE REGRESSION: Phase 1 took %v, which exceeds SLA budget of 90 seconds", phase1Time)
	}

	// Verify that the 100 locked files are indeed locked and still present
	for i := 0; i < 100; i++ {
		path := filepath.Join(pollDir, fmt.Sprintf("locked_invoice_%d.txt", i))
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Fatalf("locked file %s disappeared prematurely", path)
		}
	}

	// 8. Unlock the 100 files
	fmt.Printf("[Live5K] Unlocking the remaining 100 files to simulate completion...\n")
	for _, c := range lockClosers {
		_ = c.Close()
	}
	lockClosers = nil // Clear references

	// 9. Verify Phase 2: Check that all 100 formerly-locked files are processed.
	phase2StartTime = time.Now()
	timeout = time.After(60 * time.Second)

	phase2Done = false
	for {
		select {
		case <-timeout:
			t.Error("timeout waiting for Phase 2 to complete")
			goto CleanupAndExit
		case <-ticker.C:
			entries, err := os.ReadDir(pollDir)
			if err != nil {
				t.Fatalf("failed to read poll dir: %v", err)
			}
			// Directory is empty once the 100 files are processed
			if len(entries) == 0 {
				phase2Done = true
				goto Phase2Complete
			}
		}
	}

Phase2Complete:
	phase2Time = time.Since(phase2StartTime)
	if !phase2Done {
		t.Fatalf("Phase 2 verification failed")
	}
	fmt.Printf("[Live5K] Phase 2 completed successfully in %v (last 100 files uploaded & archived!).\n", phase2Time.Round(time.Millisecond))

	// Allow 1 second for the final transaction commit rename on disk to complete before checking
	time.Sleep(1 * time.Second)

	// STRICT PERFORMANCE SLA ASSERTION:
	// Rescan, verify sleep (1s), and upload of 100 files should easily take <20s.
	if phase2Time > 20*time.Second {
		t.Errorf("PERFORMANCE REGRESSION: Phase 2 took %v, which exceeds SLA budget of 20 seconds", phase2Time)
	}

	totalTime = time.Since(startTime)
	if totalTime > 110*time.Second {
		t.Errorf("PERFORMANCE REGRESSION: Total run took %v, which exceeds SLA budget of 110 seconds", totalTime)
	}

	// 10. Verify Post-Processing (Archives)
	// Both batches should be cleanly moved to datestamped subdirectories inside the archiveDir
	archiveEntries, err = os.ReadDir(archiveDir)
	if err != nil {
		t.Fatalf("failed to read archive directory: %v", err)
	}
	// We expect 2 separate folders (one for the 4900 files, and one for the 100 files)
	totalArchivedFiles = 0
	for _, entry := range archiveEntries {
		if entry.IsDir() && entry.Name() != ".staging" {
			subFiles, err := os.ReadDir(filepath.Join(archiveDir, entry.Name()))
			if err == nil {
				for _, sf := range subFiles {
					if strings.HasPrefix(sf.Name(), "unlocked_invoice_") || strings.HasPrefix(sf.Name(), "locked_invoice_") {
						totalArchivedFiles++
					}
				}
			}
		}
	}
	if totalArchivedFiles != 5000 {
		t.Errorf("expected 5000 archived files, got %d", totalArchivedFiles)
	}

	// Verify all 5,000 files arrived on SFTPGo server (filter out SFTPGo config/db files)
	sftpEntries, err = os.ReadDir(sftpHome)
	if err != nil {
		t.Fatalf("failed to read SFTP home dir: %v", err)
	}
	sftpFileCount = 0
	for _, entry := range sftpEntries {
		if !entry.IsDir() && (strings.HasPrefix(entry.Name(), "unlocked_invoice_") || strings.HasPrefix(entry.Name(), "locked_invoice_")) {
			sftpFileCount++
		}
	}
	if sftpFileCount != 5000 {
		t.Errorf("expected 5000 uploaded files on SFTPGo server, got %d", sftpFileCount)
	}

	fmt.Printf("\n=======================================================\n")
	fmt.Printf("   LIVE 5K FILES INTEGRATION TEST PERFORMANCE SUMMARY\n")
	fmt.Printf("=======================================================\n")
	fmt.Printf(" - Phase 1 (4,900 files concurrent verification + upload): %v\n", phase1Time.Round(time.Second))
	fmt.Printf(" - Phase 2 (100 files verification retry + upload):        %v\n", phase2Time.Round(time.Second))
	fmt.Printf(" - Total Execution Time:                                   %v\n", time.Since(startTime).Round(time.Second))
	fmt.Printf("=======================================================\n\n")

CleanupAndExit:
	cancel()
	<-errChan
}

// TestLiveSFTPGo1MIntegration performs a full-scale 1M files live end-to-end integration test against a local SFTPGo instance.
// Run this test with:
//
//	$env:TEST_LIVE_SFTP_1M="true"; go test -v -timeout=40m -run=TestLiveSFTPGo1MIntegration ./internal/service
func TestLiveSFTPGo1MIntegration(t *testing.T) {
	if os.Getenv("TEST_LIVE_SFTP_1M") != "true" {
		t.Skip("Skipping live 1M SFTP test. Set TEST_LIVE_SFTP_1M=true to run.")
	}

	tempDir := os.Getenv("TEMP")
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	timestamp := time.Now().Format("20060102150405")
	sftpHome := filepath.Join(tempDir, "unitests", "sftpgo_home_1m-"+timestamp)
	pollDir := filepath.Join(tempDir, "unitests", "poll_dir_1m-"+timestamp)
	archiveDir := filepath.Join(tempDir, "unitests", "archive_1m-"+timestamp)

	_ = os.MkdirAll(sftpHome, 0o750)
	_ = os.MkdirAll(pollDir, 0o750)
	defer func() {
		_ = fastRemoveAll(sftpHome)
		_ = fastRemoveAll(pollDir)
		_ = fastRemoveAll(archiveDir)
	}()

	fmt.Printf("[Live1M] Generating 1,000,000 dummy files in %s...\n", pollDir)

	// Create empty files concurrently
	const numGenWorkers = 16
	jobs := make(chan int, 1000000)
	var genWg sync.WaitGroup

	for w := 0; w < numGenWorkers; w++ {
		genWg.Add(1)
		go func() {
			defer genWg.Done()
			for j := range jobs {
				path := filepath.Join(pollDir, fmt.Sprintf("file_%d.txt", j))
				f, err := os.Create(path)
				if err == nil {
					_ = f.Close()
				}
			}
		}()
	}

	for i := 0; i < 1000000; i++ {
		jobs <- i
	}
	close(jobs)
	genWg.Wait()
	fmt.Printf("[Live1M] Generated 1,000,000 files successfully.\n")

	// Start SFTPGo in portable mode in the background
	sftpgoPath := getSFTPGoPath()
	port := getSFTPGoPort()
	cmd := exec.Command(sftpgoPath, "portable",
		"-d", sftpHome,
		"-u", "testuser",
		"-p", "password123",
		"-s", strconv.Itoa(port),
		"-g", "*",
	)
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Dir = sftpHome

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start SFTPGo: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
	}()

	// Wait 2 seconds for SFTPGo server to start listening
	time.Sleep(2 * time.Second)

	// Generate master key and encrypt password
	masterKeyStr, _ := libsecsecrets.GenerateKey()
	masterKey, _ := libsecsecrets.ResolveKey(context.Background(), masterKeyStr, "", "")
	encPass, _ := libsecsecrets.Encrypt(context.Background(), "password123", masterKey)
	libsecsecrets.ZeroBuffer(masterKey)

	_ = os.Setenv("SECRETPROTECTOR_KEY", masterKeyStr)
	defer func() { _ = os.Unsetenv("SECRETPROTECTOR_KEY") }()

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:              pollDir,
			Algorithm:              config.PollInterval,
			Value:                  1,
			MaxBatchSize:           10000,
			MaxVerificationWorkers: 64,
		},
		Integrity: config.IntegrityConfig{
			Algorithm:            config.IntegritySize,
			VerificationAttempts: 0,
			VerificationInterval: 0,
		},
		Action: config.ActionConfig{
			Type:                  config.ActionSFTP,
			ConcurrentConnections: 64,
			SFTP: config.SFTPConfig{
				Host:              "127.0.0.1",
				Port:              port,
				Username:          "testuser",
				EncryptedPassword: encPass,
				MasterKeyEnv:      "SECRETPROTECTOR_KEY",
				RemotePath:        "/",
			},
			PostProcess: config.PostProcessConfig{
				Action:      config.PostActionDelete,
				ArchivePath: filepath.Join(tempDir, "unitests", "archive_1m-"+timestamp),
			},
		},
	}

	engine, err := NewEngine(cfg, false)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	// Start Engine in background
	ctx, cancel := context.WithCancel(context.Background())
	errChan := make(chan error, 1)
	go func() {
		errChan <- engine.Run(ctx)
	}()

	// Wait for upload to complete (all files deleted locally)
	timeout := time.After(4 * time.Hour)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	startTime := time.Now()
	success := false

	for {
		select {
		case <-timeout:
			t.Error("timeout waiting for 1M files to upload")
			goto ExitLoop
		case <-ticker.C:
			// Read directory entries in chunks to count remaining files without OOM/GC issues
			f, err := os.Open(pollDir)
			if err != nil {
				goto ExitLoop
			}
			_, err = f.ReadDir(1)
			_ = f.Close()
			if err != nil {
				// Directory is empty (or we got EOF)
				success = true
				goto ExitLoop
			}

			// Count files remaining (just print that we are not empty yet)
			fmt.Printf("[Live1M] Processing files... (Elapsed: %v)\n", time.Since(startTime).Round(time.Second))
		}
	}

ExitLoop:
	cancel()
	<-errChan

	if !success {
		t.Fatal("1M live SFTP integration test failed")
	}
	t.Logf("1M live SFTP integration test passed in %v!", time.Since(startTime))
}

// fastRemoveAll removes a directory and its contents as quickly as possible
// using platform-optimized native shell tools, with a fallback to os.RemoveAll.
func fastRemoveAll(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}

	if runtime.GOOS == "windows" {
		// Windows: Use native rmdir /s /q which is significantly faster than os.RemoveAll
		// because it runs at the kernel level without Go's file-by-file sequential recursion.
		cmd := exec.Command("cmd", "/c", "rmdir", "/s", "/q", dir)
		if err := cmd.Run(); err == nil {
			return nil
		}
		// Fallback to standard Go recursion if cmd fails
		return os.RemoveAll(dir)
	}

	// Linux / Unix: Use native rm -rf which leverages filesystem-level optimizations
	cmd := exec.Command("rm", "-rf", dir)
	if err := cmd.Run(); err == nil {
		return nil
	}

	// Fallback to standard Go recursion if rm fails
	return os.RemoveAll(dir)
}

func getSFTPGoPath() string {
	if path := os.Getenv("SFTPGO_PATH"); path != "" {
		return path
	}
	if path, err := exec.LookPath("sftpgo"); err == nil {
		return path
	}
	if runtime.GOOS == "windows" {
		return `d:\inetd\sftpgo\sftpgo.exe`
	}
	return "sftpgo"
}

func getSFTPGoPort() int {
	portStr := os.Getenv("SFTPGO_PORT")
	if portStr == "" {
		portStr = "2022"
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 2022
	}
	return port
}
