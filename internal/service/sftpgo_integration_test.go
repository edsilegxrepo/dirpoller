package service

import (
	"context"
	"criticalsys.net/secretprotector/pkg/libsecsecrets"
	"fmt"
	"io"
	"math/rand"
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

// TestLiveSFTPGoBatchIntegration verifies end-to-end SFTP uploading using the Batch Poller algorithm.
// Run this test with:
//
//	$env:TEST_LIVE_SFTP="true"; go test -v -run=TestLiveSFTPGoBatchIntegration ./internal/service
func TestLiveSFTPGoBatchIntegration(t *testing.T) {
	if os.Getenv("TEST_LIVE_SFTP") != "true" {
		t.Skip("Skipping live SFTP test. Set TEST_LIVE_SFTP=true to run.")
	}

	tempDir := os.Getenv("TEMP")
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	timestamp := time.Now().Format("20060102150405")
	sftpHome := filepath.Join(tempDir, "unitests", "sftpgo_home_batch-"+timestamp)
	pollDir := filepath.Join(tempDir, "unitests", "poll_dir_batch-"+timestamp)
	archiveDir := filepath.Join(tempDir, "unitests", "archive_batch-"+timestamp)

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

	// 2. Start SFTPGo portable server
	sftpgoPath := getSFTPGoPath()
	port := getSFTPGoPort() + 1
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

	// Wait 2 seconds for SFTPGo server to bind
	time.Sleep(2 * time.Second)

	// 3. Write config to a JSON file to test JSON unmarshaling of numeric Value (float64)
	jsonConfigPath := filepath.Join(tempDir, "unitests", "config_batch-"+timestamp+".json")
	jsonConfigData := fmt.Sprintf(`{
		"poll": {
			"directory": "%s",
			"algorithm": "batch",
			"value": 3,
			"max_batch_size": 100,
			"max_verification_workers": 128
		},
		"integrity": {
			"algorithm": "size",
			"attempts": 1,
			"interval": 1
		},
		"action": {
			"type": "sftp",
			"concurrent_connections": 32,
			"post_process": {
				"action": "delete",
				"archive_path": "%s"
			},
			"sftp": {
				"host": "127.0.0.1",
				"port": %d,
				"username": "testuser",
				"encrypted_password": "%s",
				"master_key_env": "SECRETPROTECTOR_KEY",
				"remote_path": "/"
			}
		}
	}`, filepath.ToSlash(pollDir), filepath.ToSlash(archiveDir), port, encPass)

	if err := os.WriteFile(jsonConfigPath, []byte(jsonConfigData), 0o600); err != nil {
		t.Fatalf("failed to write json config file: %v", err)
	}
	defer func() { _ = os.Remove(jsonConfigPath) }()

	// 4. Load config using config.LoadConfig to exercise the JSON unmarshaler and type safety checks
	cfg, _, err := config.LoadConfig(jsonConfigPath)
	if err != nil {
		t.Fatalf("failed to load config from json: %v", err)
	}

	// 5. Create 105 test files to verify BACKLOG DRAINING (since 105 > MaxBatchSize 100)
	var localFiles []string
	for i := 1; i <= 105; i++ {
		filename := fmt.Sprintf("batch_test_%d.txt", i)
		path := filepath.Join(pollDir, filename)
		if err := os.WriteFile(path, []byte(fmt.Sprintf("file %d data", i)), 0o600); err != nil {
			t.Fatalf("failed to write test file %d: %v", i, err)
		}
		localFiles = append(localFiles, path)
	}

	// Give Windows OS a moment to release handles/locks after writing 105 files in a tight loop
	time.Sleep(1 * time.Second)

	engine, err := NewEngine(cfg, false)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	// Start Engine
	ctx, cancel := context.WithCancel(context.Background())
	errChan := make(chan error, 1)
	go func() {
		errChan <- engine.Run(ctx)
	}()

	// 6. Poll and wait for upload success of ALL 105 files
	timeout := time.After(45 * time.Second)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	success := false
	for {
		select {
		case <-timeout:
			var missing []int
			for i := 1; i <= 105; i++ {
				upFile := filepath.Join(sftpHome, fmt.Sprintf("batch_test_%d.txt", i))
				if _, err := os.Stat(upFile); err != nil {
					missing = append(missing, i)
				}
			}
			var remainingLocal []string
			for _, f := range localFiles {
				if _, err := os.Stat(f); err == nil {
					remainingLocal = append(remainingLocal, filepath.Base(f))
				}
			}
			t.Errorf("timeout waiting for files to upload to SFTPGo. Missing on SFTPGo (%d files): %v. Remaining local (%d files): %v", len(missing), missing, len(remainingLocal), remainingLocal)
			goto ExitLoop
		case <-ticker.C:
			// Check if all 105 files arrived at SFTPGo directory and local ones are cleaned up
			allUploaded := true
			for i := 1; i <= 105; i++ {
				upFile := filepath.Join(sftpHome, fmt.Sprintf("batch_test_%d.txt", i))
				if _, err := os.Stat(upFile); err != nil {
					allUploaded = false
					break
				}
			}
			if allUploaded {
				allLocalDeleted := true
				for _, f := range localFiles {
					if _, err := os.Stat(f); !os.IsNotExist(err) {
						allLocalDeleted = false
						break
					}
				}
				if allLocalDeleted {
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
		t.Fatal("SFTPGo live batch integration test failed")
	}
	t.Log("SFTPGo live batch integration test passed successfully!")
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
	// Rescan, verify sleep (1s), and upload of 100 files should easily take <40s (including 30s lock exclusion TTL).
	if phase2Time > 40*time.Second {
		t.Errorf("PERFORMANCE REGRESSION: Phase 2 took %v, which exceeds SLA budget of 40 seconds", phase2Time)
	}

	totalTime = time.Since(startTime)
	if totalTime > 130*time.Second {
		t.Errorf("PERFORMANCE REGRESSION: Total run took %v, which exceeds SLA budget of 130 seconds", totalTime)
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

// TestLiveSFTPGoMatrixIntegration runs a matrix of realistic and live test cases against
// the portable SFTPGo server. It tests multiple polling algorithms, post-processing
// lifecycle actions, and configurations loaded from JSON files.
// Run this test with:
//
//	$env:TEST_LIVE_SFTP="true"; go test -v -run=TestLiveSFTPGoMatrixIntegration ./internal/service
func TestLiveSFTPGoMatrixIntegration(t *testing.T) {
	if os.Getenv("TEST_LIVE_SFTP") != "true" {
		t.Skip("Skipping live SFTP matrix test. Set TEST_LIVE_SFTP=true to run.")
	}

	tempDir := os.Getenv("TEMP")
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	timestamp := time.Now().Format("20060102150405")

	// Start SFTPGo once for all matrix cases
	sftpHome := filepath.Join(tempDir, "unitests", "sftpgo_home_matrix-"+timestamp)
	_ = os.MkdirAll(sftpHome, 0o750)
	defer func() { _ = fastRemoveAll(sftpHome) }()

	masterKeyStr, _ := libsecsecrets.GenerateKey()
	masterKey, _ := libsecsecrets.ResolveKey(context.Background(), masterKeyStr, "", "")
	encPass, _ := libsecsecrets.Encrypt(context.Background(), "password123", masterKey)
	libsecsecrets.ZeroBuffer(masterKey)

	_ = os.Setenv("SECRETPROTECTOR_KEY", masterKeyStr)
	defer func() { _ = os.Unsetenv("SECRETPROTECTOR_KEY") }()

	sftpgoPath := getSFTPGoPath()
	port := getSFTPGoPort() + 3
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
	defer func() { _ = cmd.Process.Kill() }()

	time.Sleep(2 * time.Second) // wait for server bind

	// Sub-test cases
	t.Run("TriggerPoller_MoveArchive_ArchiveRetention", func(t *testing.T) {
		caseTime := time.Now().Format("150405")
		pollDir := filepath.Join(tempDir, "unitests", "poll_matrix_trigger-"+caseTime)
		archiveDir := filepath.Join(tempDir, "unitests", "archive_matrix_trigger-"+caseTime)
		_ = os.MkdirAll(pollDir, 0o750)
		_ = os.MkdirAll(archiveDir, 0o750)
		defer func() {
			_ = fastRemoveAll(pollDir)
			_ = fastRemoveAll(archiveDir)
		}()

		// 1. Create a backdated archived file (older than 3 days) to verify that daily archive retention cleanup executes.
		backdatedFile := filepath.Join(archiveDir, "old_archived_file.txt")
		_ = os.WriteFile(backdatedFile, []byte("old archive"), 0o600)
		threeDaysAgo := time.Now().AddDate(0, 0, -4)
		_ = os.Chtimes(backdatedFile, threeDaysAgo, threeDaysAgo)

		// 2. Write JSON config with Trigger Algorithm, Move Archive post-process, and 3 days retention
		configPath := filepath.Join(tempDir, "unitests", "config_matrix_trigger-"+caseTime+".json")
		configData := fmt.Sprintf(`{
			"poll": {
				"directory": "%s",
				"algorithm": "trigger",
				"value": "ready.txt",
				"max_batch_size": 100,
				"max_verification_workers": 16
			},
			"integrity": {
				"algorithm": "size",
				"attempts": 1,
				"interval": 1
			},
			"action": {
				"type": "sftp",
				"concurrent_connections": 4,
				"post_process": {
					"action": "move_archive",
					"archive_path": "%s",
					"archive_retention": 3
				},
				"sftp": {
					"host": "127.0.0.1",
					"port": %d,
					"username": "testuser",
					"encrypted_password": "%s",
					"master_key_env": "SECRETPROTECTOR_KEY",
					"remote_path": "/"
				}
			}
		}`, filepath.ToSlash(pollDir), filepath.ToSlash(archiveDir), port, encPass)
		_ = os.WriteFile(configPath, []byte(configData), 0o600)
		defer func() { _ = os.Remove(configPath) }()

		cfg, _, err := config.LoadConfig(configPath)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		engine, err := NewEngine(cfg, false)
		if err != nil {
			t.Fatalf("failed to create engine: %v", err)
		}
		defer engine.Close()

		// 3. Write 3 data files. Verify they are NOT uploaded immediately because the trigger file is missing.
		for i := 1; i <= 3; i++ {
			_ = os.WriteFile(filepath.Join(pollDir, fmt.Sprintf("data_%d.txt", i)), []byte("invoice data"), 0o600)
		}

		ctx, cancel := context.WithCancel(context.Background())
		errChan := make(chan error, 1)
		go func() {
			errChan <- engine.Run(ctx)
		}()

		// Wait 1.5 seconds and assert no files were uploaded
		time.Sleep(1500 * time.Millisecond)
		for i := 1; i <= 3; i++ {
			if _, err := os.Stat(filepath.Join(sftpHome, fmt.Sprintf("data_%d.txt", i))); err == nil {
				cancel()
				t.Fatalf("data_%d.txt should not have been uploaded yet (no trigger)", i)
			}
		}

		// 4. Write trigger.ok file to trigger processing
		triggerFile := filepath.Join(pollDir, "ready.txt")
		_ = os.WriteFile(triggerFile, []byte("ready"), 0o600)
		time.Sleep(1 * time.Second) // wait for handles release

		// 5. Poll and verify all 3 data files are processed, uploaded, archived, and backdated archive gets cleaned up!
		timeout := time.After(15 * time.Second)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		success := false
		for {
			select {
			case <-timeout:
				t.Error("timeout waiting for trigger processing")
				goto ExitSubTest1
			case <-ticker.C:
				allUploaded := true
				for i := 1; i <= 3; i++ {
					if _, err := os.Stat(filepath.Join(sftpHome, fmt.Sprintf("data_%d.txt", i))); err != nil {
						allUploaded = false
						break
					}
				}
				if allUploaded {
					// Verify files are archived locally in datestamped subfolder
					allArchived := true
					for i := 1; i <= 3; i++ {
						found := false
						_ = filepath.Walk(archiveDir, func(path string, info os.FileInfo, err error) error {
							if err == nil && !info.IsDir() && filepath.Base(path) == fmt.Sprintf("data_%d.txt", i) {
								found = true
							}
							return nil
						})
						if !found {
							allArchived = false
							break
						}
					}
					// Verify backdated archive is deleted
					_, oldErr := os.Stat(backdatedFile)
					if allArchived && os.IsNotExist(oldErr) {
						success = true
						goto ExitSubTest1
					}
				}
			}
		}

	ExitSubTest1:
		cancel()
		<-errChan
		if !success {
			t.Fatal("TriggerPoller + MoveArchive + ArchiveRetention sub-test failed")
		}
	})

	t.Run("EventPoller_MoveCompress", func(t *testing.T) {
		caseTime := time.Now().Format("150405")
		pollDir := filepath.Join(tempDir, "unitests", "poll_matrix_event-"+caseTime)
		archiveDir := filepath.Join(tempDir, "unitests", "archive_matrix_event-"+caseTime)
		_ = os.MkdirAll(pollDir, 0o750)
		_ = os.MkdirAll(archiveDir, 0o750)
		defer func() {
			_ = fastRemoveAll(pollDir)
			_ = fastRemoveAll(archiveDir)
		}()

		// Write config
		configPath := filepath.Join(tempDir, "unitests", "config_matrix_event-"+caseTime+".json")
		configData := fmt.Sprintf(`{
			"poll": {
				"directory": "%s",
				"algorithm": "event",
				"max_batch_size": 100,
				"max_verification_workers": 16
			},
			"integrity": {
				"algorithm": "size",
				"attempts": 1,
				"interval": 1
			},
			"action": {
				"type": "sftp",
				"concurrent_connections": 4,
				"post_process": {
					"action": "move_compress",
					"archive_path": "%s"
				},
				"sftp": {
					"host": "127.0.0.1",
					"port": %d,
					"username": "testuser",
					"encrypted_password": "%s",
					"master_key_env": "SECRETPROTECTOR_KEY",
					"remote_path": "/"
				}
			}
		}`, filepath.ToSlash(pollDir), filepath.ToSlash(archiveDir), port, encPass)
		_ = os.WriteFile(configPath, []byte(configData), 0o600)
		defer func() { _ = os.Remove(configPath) }()

		cfg, _, err := config.LoadConfig(configPath)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		engine, err := NewEngine(cfg, false)
		if err != nil {
			t.Fatalf("failed to create engine: %v", err)
		}
		defer engine.Close()

		ctx, cancel := context.WithCancel(context.Background())
		errChan := make(chan error, 1)
		go func() {
			errChan <- engine.Run(ctx)
		}()

		// Write 4 files to pollDir
		var localFiles []string
		for i := 1; i <= 4; i++ {
			path := filepath.Join(pollDir, fmt.Sprintf("event_data_%d.txt", i))
			_ = os.WriteFile(path, []byte("event file data"), 0o600)
			localFiles = append(localFiles, path)
		}
		time.Sleep(1 * time.Second) // handles release

		// Verify files are uploaded to SFTPGo and compressed in archiveDir
		timeout := time.After(15 * time.Second)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		success := false
		for {
			select {
			case <-timeout:
				t.Error("timeout waiting for event processing")
				goto ExitSubTest2
			case <-ticker.C:
				allUploaded := true
				for i := 1; i <= 4; i++ {
					if _, err := os.Stat(filepath.Join(sftpHome, fmt.Sprintf("event_data_%d.txt", i))); err != nil {
						allUploaded = false
						break
					}
				}
				if allUploaded {
					// Verify local files are deleted
					allDeleted := true
					for _, f := range localFiles {
						if _, err := os.Stat(f); !os.IsNotExist(err) {
							allDeleted = false
							break
						}
					}
					// Verify a compressed archive (.zst) exists in archiveDir
					var hasArchive bool
					entries, err := os.ReadDir(archiveDir)
					if err == nil {
						for _, entry := range entries {
							if strings.HasSuffix(entry.Name(), ".zst") {
								hasArchive = true
								break
							}
						}
					}
					if allDeleted && hasArchive {
						success = true
						goto ExitSubTest2
					}
				}
			}
		}

	ExitSubTest2:
		cancel()
		<-errChan
		if !success {
			t.Fatal("EventPoller + MoveCompress sub-test failed")
		}
	})

	t.Run("IntegrityDisabled_AlgorithmNone", func(t *testing.T) {
		caseTime := time.Now().Format("150405")
		pollDir := filepath.Join(tempDir, "unitests", "poll_matrix_disabled-"+caseTime)
		archiveDir := filepath.Join(tempDir, "unitests", "archive_matrix_disabled-"+caseTime)
		_ = os.MkdirAll(pollDir, 0o750)
		_ = os.MkdirAll(archiveDir, 0o750)
		defer func() {
			_ = fastRemoveAll(pollDir)
			_ = fastRemoveAll(archiveDir)
		}()

		configPath := filepath.Join(tempDir, "unitests", "config_matrix_disabled-"+caseTime+".json")
		configData := fmt.Sprintf(`{
			"poll": {
				"directory": "%s",
				"algorithm": "interval",
				"value": 1,
				"max_batch_size": 100
			},
			"integrity": {
				"algorithm": "none",
				"attempts": 0,
				"interval": 0
			},
			"action": {
				"type": "sftp",
				"concurrent_connections": 2,
				"sftp": {
					"host": "127.0.0.1",
					"port": %d,
					"username": "testuser",
					"encrypted_password": "%s",
					"remote_path": "/"
				},
				"post_process": {
					"action": "delete",
					"archive_path": "%s"
				}
			}
		}`, filepath.ToSlash(pollDir), port, encPass, filepath.ToSlash(archiveDir))

		_ = os.WriteFile(configPath, []byte(configData), 0o600)
		defer func() { _ = os.Remove(configPath) }()

		cfg, _, err := config.LoadConfig(configPath)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		engine, err := NewEngine(cfg, false)
		if err != nil {
			t.Fatalf("failed to create engine: %v", err)
		}
		defer engine.Close()

		ctx, cancel := context.WithCancel(context.Background())
		errChan := make(chan error, 1)
		go func() {
			errChan <- engine.Run(ctx)
		}()

		dataPath := filepath.Join(pollDir, "disabled_integrity_file.txt")
		_ = os.WriteFile(dataPath, []byte("disabled integrity test data"), 0o600)

		timeout := time.After(10 * time.Second)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		success := false
		for {
			select {
			case <-timeout:
				t.Error("timeout waiting for disabled integrity file upload")
				goto ExitSubTest3
			case <-ticker.C:
				if _, err := os.Stat(filepath.Join(sftpHome, "disabled_integrity_file.txt")); err == nil {
					if _, err := os.Stat(dataPath); os.IsNotExist(err) {
						success = true
						goto ExitSubTest3
					}
				}
			}
		}

	ExitSubTest3:
		cancel()
		<-errChan
		if !success {
			t.Fatal("IntegrityDisabled_AlgorithmNone integration test failed")
		}
	})

	t.Run("Integrity_AlgorithmHash", func(t *testing.T) {
		caseTime := time.Now().Format("150405")
		pollDir := filepath.Join(tempDir, "unitests", "poll_matrix_hash-"+caseTime)
		archiveDir := filepath.Join(tempDir, "unitests", "archive_matrix_hash-"+caseTime)
		_ = os.MkdirAll(pollDir, 0o750)
		_ = os.MkdirAll(archiveDir, 0o750)
		defer func() {
			_ = fastRemoveAll(pollDir)
			_ = fastRemoveAll(archiveDir)
		}()

		configPath := filepath.Join(tempDir, "unitests", "config_matrix_hash-"+caseTime+".json")
		configData := fmt.Sprintf(`{
			"poll": {
				"directory": "%s",
				"algorithm": "interval",
				"value": 1,
				"max_batch_size": 100
			},
			"integrity": {
				"algorithm": "hash",
				"attempts": 1,
				"interval": 1
			},
			"action": {
				"type": "sftp",
				"concurrent_connections": 2,
				"sftp": {
					"host": "127.0.0.1",
					"port": %d,
					"username": "testuser",
					"encrypted_password": "%s",
					"remote_path": "/"
				},
				"post_process": {
					"action": "delete",
					"archive_path": "%s"
				}
			}
		}`, filepath.ToSlash(pollDir), port, encPass, filepath.ToSlash(archiveDir))

		_ = os.WriteFile(configPath, []byte(configData), 0o600)
		defer func() { _ = os.Remove(configPath) }()

		cfg, _, err := config.LoadConfig(configPath)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}

		engine, err := NewEngine(cfg, false)
		if err != nil {
			t.Fatalf("failed to create engine: %v", err)
		}
		defer engine.Close()

		ctx, cancel := context.WithCancel(context.Background())
		errChan := make(chan error, 1)
		go func() {
			errChan <- engine.Run(ctx)
		}()

		dataPath := filepath.Join(pollDir, "hash_integrity_file.txt")
		_ = os.WriteFile(dataPath, []byte("hash integrity xxh3-128 test content"), 0o600)

		timeout := time.After(10 * time.Second)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		success := false
		for {
			select {
			case <-timeout:
				t.Error("timeout waiting for hash integrity file upload")
				goto ExitSubTest4
			case <-ticker.C:
				if _, err := os.Stat(filepath.Join(sftpHome, "hash_integrity_file.txt")); err == nil {
					if _, err := os.Stat(dataPath); os.IsNotExist(err) {
						success = true
						goto ExitSubTest4
					}
				}
			}
		}

	ExitSubTest4:
		cancel()
		<-errChan
		if !success {
			t.Fatal("Integrity_AlgorithmHash integration test failed")
		}
	})
}

func startSFTPGo(port int, sftpHome string) (*exec.Cmd, error) {
	sftpgoPath := getSFTPGoPath()
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
		return nil, err
	}
	return cmd, nil
}

// TestLiveSFTPGoLongevityAndGoroutineLeak validates that the Engine does not leak goroutines
// or memory over multiple hours/cycles of execution.
func TestLiveSFTPGoLongevityAndGoroutineLeak(t *testing.T) {
	if os.Getenv("TEST_LIVE_SFTP") != "true" {
		t.Skip("Skipping live SFTP longevity test. Set TEST_LIVE_SFTP=true to run.")
	}

	tempDir := os.Getenv("TEMP")
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	timestamp := time.Now().Format("20060102150405")
	sftpHome := filepath.Join(tempDir, "unitests", "sftpgo_home_leak-"+timestamp)
	_ = os.MkdirAll(sftpHome, 0o750)
	defer func() { _ = fastRemoveAll(sftpHome) }()

	port := getSFTPGoPort() + 4
	cmd, err := startSFTPGo(port, sftpHome)
	if err != nil {
		t.Fatalf("failed to start SFTPGo: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	time.Sleep(2 * time.Second)

	pollDir := filepath.Join(tempDir, "unitests", "poll_leak-"+timestamp)
	archiveDir := filepath.Join(tempDir, "unitests", "archive_leak-"+timestamp)
	_ = os.MkdirAll(pollDir, 0o750)
	_ = os.MkdirAll(archiveDir, 0o750)
	defer func() {
		_ = fastRemoveAll(pollDir)
		_ = fastRemoveAll(archiveDir)
	}()

	masterKeyStr, _ := libsecsecrets.GenerateKey()
	masterKey, _ := libsecsecrets.ResolveKey(context.Background(), masterKeyStr, "", "")
	encPass, _ := libsecsecrets.Encrypt(context.Background(), "password123", masterKey)
	libsecsecrets.ZeroBuffer(masterKey)

	_ = os.Setenv("SECRETPROTECTOR_KEY", masterKeyStr)
	defer func() { _ = os.Unsetenv("SECRETPROTECTOR_KEY") }()

	configPath := filepath.Join(tempDir, "unitests", "config_leak-"+timestamp+".json")
	configData := fmt.Sprintf(`{
		"poll": {
			"directory": "%s",
			"algorithm": "interval",
			"value": 2,
			"max_batch_size": 100,
			"max_verification_workers": 16
		},
		"integrity": {
			"algorithm": "size",
			"attempts": 1,
			"interval": 1
		},
		"action": {
			"type": "sftp",
			"concurrent_connections": 4,
			"post_process": {
				"action": "delete",
				"archive_path": "%s"
			},
			"sftp": {
				"host": "127.0.0.1",
				"port": %d,
				"username": "testuser",
				"encrypted_password": "%s",
				"master_key_env": "SECRETPROTECTOR_KEY",
				"remote_path": "/"
			}
		}
	}`, filepath.ToSlash(pollDir), filepath.ToSlash(archiveDir), port, encPass)
	_ = os.WriteFile(configPath, []byte(configData), 0o600)
	defer func() { _ = os.Remove(configPath) }()

	cfg, _, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	// 1. Capture baseline goroutines
	runtime.GC()
	time.Sleep(500 * time.Millisecond)
	baselineGoroutines := runtime.NumGoroutine()

	engine, err := NewEngine(cfg, false)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errChan := make(chan error, 1)
	go func() {
		errChan <- engine.Run(ctx)
	}()

	// 2. Perform 10 bursts of file writes over 15 seconds
	for b := 1; b <= 10; b++ {
		for i := 1; i <= 5; i++ {
			_ = os.WriteFile(filepath.Join(pollDir, fmt.Sprintf("leak_file_%d_%d.txt", b, i)), []byte("leak test"), 0o600)
		}
		time.Sleep(1200 * time.Millisecond)
	}

	// Verify all 50 files uploaded (with a timeout)
	timeout := time.After(15 * time.Second)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	success := false
	for {
		select {
		case <-timeout:
			cancel()
			t.Fatal("timeout waiting for all longevity burst files to upload")
		case <-ticker.C:
			allUploaded := true
			for b := 1; b <= 10; b++ {
				for i := 1; i <= 5; i++ {
					upFile := filepath.Join(sftpHome, fmt.Sprintf("leak_file_%d_%d.txt", b, i))
					if _, err := os.Stat(upFile); err != nil {
						allUploaded = false
						break
					}
				}
				if !allUploaded {
					break
				}
			}
			if allUploaded {
				success = true
				goto ExitLeakVerify
			}
		}
	}

ExitLeakVerify:
	if !success {
		cancel()
		t.Fatal("file verification failed")
	}

	// 3. Close the Engine and cancel context
	engine.Close()
	cancel()
	<-errChan

	// 4. Force GC and verify goroutine count returns back to baseline
	runtime.GC()
	time.Sleep(1500 * time.Millisecond) // Give runtime time to clean up completed goroutines
	runtime.GC()

	finalGoroutines := runtime.NumGoroutine()
	// Allow a small headroom of 6 goroutines for normal system variance
	headroom := 6
	if finalGoroutines > baselineGoroutines+headroom {
		t.Errorf("Goroutine leak detected: baseline=%d, final=%d (headroom=%d)", baselineGoroutines, finalGoroutines, headroom)
	} else {
		t.Logf("Goroutine leak test passed. Baseline: %d, Final: %d", baselineGoroutines, finalGoroutines)
	}
}

// TestLiveSFTPGoConcurrencyAndBackpressure validates thread-safety and lack of race conditions
// when multiple concurrent writers stress the Engine while worker connections are limited.
func TestLiveSFTPGoConcurrencyAndBackpressure(t *testing.T) {
	if os.Getenv("TEST_LIVE_SFTP") != "true" {
		t.Skip("Skipping live SFTP concurrency test. Set TEST_LIVE_SFTP=true to run.")
	}

	tempDir := os.Getenv("TEMP")
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	timestamp := time.Now().Format("20060102150405")
	sftpHome := filepath.Join(tempDir, "unitests", "sftpgo_home_concur-"+timestamp)
	_ = os.MkdirAll(sftpHome, 0o750)
	defer func() { _ = fastRemoveAll(sftpHome) }()

	port := getSFTPGoPort() + 5
	cmd, err := startSFTPGo(port, sftpHome)
	if err != nil {
		t.Fatalf("failed to start SFTPGo: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	time.Sleep(2 * time.Second)

	pollDir := filepath.Join(tempDir, "unitests", "poll_concur-"+timestamp)
	archiveDir := filepath.Join(tempDir, "unitests", "archive_concur-"+timestamp)
	_ = os.MkdirAll(pollDir, 0o750)
	_ = os.MkdirAll(archiveDir, 0o750)
	defer func() {
		_ = fastRemoveAll(pollDir)
		_ = fastRemoveAll(archiveDir)
	}()

	masterKeyStr, _ := libsecsecrets.GenerateKey()
	masterKey, _ := libsecsecrets.ResolveKey(context.Background(), masterKeyStr, "", "")
	encPass, _ := libsecsecrets.Encrypt(context.Background(), "password123", masterKey)
	libsecsecrets.ZeroBuffer(masterKey)

	_ = os.Setenv("SECRETPROTECTOR_KEY", masterKeyStr)
	defer func() { _ = os.Unsetenv("SECRETPROTECTOR_KEY") }()

	configPath := filepath.Join(tempDir, "unitests", "config_concur-"+timestamp+".json")
	// Low MaxBatchSize (100) and ConcurrentConnections (2) to force backpressure/queuing
	configData := fmt.Sprintf(`{
		"poll": {
			"directory": "%s",
			"algorithm": "batch",
			"value": 3,
			"max_batch_size": 100,
			"max_verification_workers": 16
		},
		"integrity": {
			"algorithm": "size",
			"attempts": 3,
			"interval": 1
		},
		"action": {
			"type": "sftp",
			"concurrent_connections": 2,
			"post_process": {
				"action": "delete",
				"archive_path": "%s"
			},
			"sftp": {
				"host": "127.0.0.1",
				"port": %d,
				"username": "testuser",
				"encrypted_password": "%s",
				"master_key_env": "SECRETPROTECTOR_KEY",
				"remote_path": "/"
			}
		}
	}`, filepath.ToSlash(pollDir), filepath.ToSlash(archiveDir), port, encPass)
	_ = os.WriteFile(configPath, []byte(configData), 0o600)
	defer func() { _ = os.Remove(configPath) }()

	cfg, _, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	engine, err := NewEngine(cfg, false)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	ctx, cancel := context.WithCancel(context.Background())
	errChan := make(chan error, 1)
	go func() {
		errChan <- engine.Run(ctx)
	}()

	// Spawn 4 concurrent writers writing 50 files each (total 200 files) at random intervals
	var wg sync.WaitGroup
	totalFiles := 200
	filesPerWriter := 50
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(writerId int) {
			defer wg.Done()
			for i := 1; i <= filesPerWriter; i++ {
				filename := fmt.Sprintf("concur_file_%d_%d.txt", writerId, i)
				filePath := filepath.Join(pollDir, filename)
				_ = os.WriteFile(filePath, []byte("concurrency stress test data"), 0o600)
				time.Sleep(time.Duration(10+rand.Intn(40)) * time.Millisecond)
			}
		}(w)
	}
	wg.Wait()

	// Wait for all uploads to complete
	timeout := time.After(45 * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	success := false
	for {
		select {
		case <-timeout:
			t.Error("timeout waiting for concurrency stress test to complete")
			goto ExitConcur
		case <-ticker.C:
			uploadedCount := 0
			entries, err := os.ReadDir(sftpHome)
			if err == nil {
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), "concur_file_") {
						uploadedCount++
					}
				}
			}
			if uploadedCount == totalFiles {
				success = true
				goto ExitConcur
			}
		}
	}

ExitConcur:
	cancel()
	<-errChan
	if !success {
		t.Fatal("Concurrency and backpressure integration test failed")
	}
}

// TestLiveSFTPGoMidTransferDisconnectAndRecovery validates that if connection is lost
// mid-transfer, the engine retries with backoff and resumes cleanly when SFTP recovers.
func TestLiveSFTPGoMidTransferDisconnectAndRecovery(t *testing.T) {
	if os.Getenv("TEST_LIVE_SFTP") != "true" {
		t.Skip("Skipping live SFTP disconnect/recovery test. Set TEST_LIVE_SFTP=true to run.")
	}

	tempDir := os.Getenv("TEMP")
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	timestamp := time.Now().Format("20060102150405")
	sftpHome := filepath.Join(tempDir, "unitests", "sftpgo_home_recov-"+timestamp)
	_ = os.MkdirAll(sftpHome, 0o750)
	defer func() { _ = fastRemoveAll(sftpHome) }()

	port := getSFTPGoPort() + 6
	cmd, err := startSFTPGo(port, sftpHome)
	if err != nil {
		t.Fatalf("failed to start SFTPGo: %v", err)
	}
	time.Sleep(2 * time.Second)

	pollDir := filepath.Join(tempDir, "unitests", "poll_recov-"+timestamp)
	archiveDir := filepath.Join(tempDir, "unitests", "archive_recov-"+timestamp)
	_ = os.MkdirAll(pollDir, 0o750)
	_ = os.MkdirAll(archiveDir, 0o750)
	defer func() {
		_ = fastRemoveAll(pollDir)
		_ = fastRemoveAll(archiveDir)
	}()

	masterKeyStr, _ := libsecsecrets.GenerateKey()
	masterKey, _ := libsecsecrets.ResolveKey(context.Background(), masterKeyStr, "", "")
	encPass, _ := libsecsecrets.Encrypt(context.Background(), "password123", masterKey)
	libsecsecrets.ZeroBuffer(masterKey)

	_ = os.Setenv("SECRETPROTECTOR_KEY", masterKeyStr)
	defer func() { _ = os.Unsetenv("SECRETPROTECTOR_KEY") }()

	configPath := filepath.Join(tempDir, "unitests", "config_recov-"+timestamp+".json")
	configData := fmt.Sprintf(`{
		"poll": {
			"directory": "%s",
			"algorithm": "interval",
			"value": 1,
			"max_batch_size": 100,
			"max_verification_workers": 16
		},
		"integrity": {
			"algorithm": "size",
			"attempts": 1,
			"interval": 1
		},
		"action": {
			"type": "sftp",
			"concurrent_connections": 2,
			"post_process": {
				"action": "delete",
				"archive_path": "%s"
			},
			"sftp": {
				"host": "127.0.0.1",
				"port": %d,
				"username": "testuser",
				"encrypted_password": "%s",
				"master_key_env": "SECRETPROTECTOR_KEY",
				"remote_path": "/"
			}
		}
	}`, filepath.ToSlash(pollDir), filepath.ToSlash(archiveDir), port, encPass)
	_ = os.WriteFile(configPath, []byte(configData), 0o600)
	defer func() { _ = os.Remove(configPath) }()

	cfg, _, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	engine, err := NewEngine(cfg, false)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	defer engine.Close()

	// Write 5 files to transfer
	for i := 1; i <= 5; i++ {
		_ = os.WriteFile(filepath.Join(pollDir, fmt.Sprintf("recov_file_%d.txt", i)), []byte("recovery test data"), 0o600)
	}
	time.Sleep(1 * time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	errChan := make(chan error, 1)
	go func() {
		errChan <- engine.Run(ctx)
	}()

	// 1. Let the engine start processing and kill the SFTPGo server process mid-transfer
	time.Sleep(300 * time.Millisecond)
	_ = cmd.Process.Kill()
	t.Log("SFTPGo killed mid-transfer. Waiting for engine backoff cycle...")

	// 2. Wait 3 seconds to let the engine hit connection errors and trigger backoffs
	time.Sleep(3 * time.Second)

	// 3. Restart SFTPGo on the same port
	cmd, err = startSFTPGo(port, sftpHome)
	if err != nil {
		cancel()
		t.Fatalf("failed to restart SFTPGo: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	t.Log("SFTPGo restarted. Verifying auto-recovery...")

	// 4. Poll and verify all 5 files are successfully uploaded and cleaned up
	timeout := time.After(25 * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	success := false
	for {
		select {
		case <-timeout:
			t.Error("timeout waiting for auto-recovery upload completion")
			goto ExitRecov
		case <-ticker.C:
			uploadedCount := 0
			entries, err := os.ReadDir(sftpHome)
			if err == nil {
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), "recov_file_") {
						uploadedCount++
					}
				}
			}
			if uploadedCount == 5 {
				success = true
				goto ExitRecov
			}
		}
	}

ExitRecov:
	cancel()
	<-errChan
	if !success {
		t.Fatal("Mid-transfer connection drop and recovery integration test failed")
	}
}

// TestLiveSFTPGoConstantInfluxAndHandleLeak streams 500 files in a continuous, high-volume influx
// to the poller, and verifies that no file descriptors or handles are leaked during the transfers.
// Run this test with:
//
//	$env:TEST_LIVE_SFTP="true"; go test -v -run=TestLiveSFTPGoConstantInfluxAndHandleLeak ./internal/service
func TestLiveSFTPGoConstantInfluxAndHandleLeak(t *testing.T) {
	if os.Getenv("TEST_LIVE_SFTP") != "true" {
		t.Skip("Skipping live SFTP influx leak test. Set TEST_LIVE_SFTP=true to run.")
	}

	tempDir := os.Getenv("TEMP")
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	timestamp := time.Now().Format("20060102150405")
	sftpHome := filepath.Join(tempDir, "unitests", "sftpgo_home_influx-"+timestamp)
	_ = os.MkdirAll(sftpHome, 0o750)
	defer func() { _ = fastRemoveAll(sftpHome) }()

	port := getSFTPGoPort() + 7
	cmd, err := startSFTPGo(port, sftpHome)
	if err != nil {
		t.Fatalf("failed to start SFTPGo: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	time.Sleep(2 * time.Second)

	pollDir := filepath.Join(tempDir, "unitests", "poll_influx-"+timestamp)
	archiveDir := filepath.Join(tempDir, "unitests", "archive_influx-"+timestamp)
	_ = os.MkdirAll(pollDir, 0o750)
	_ = os.MkdirAll(archiveDir, 0o750)
	defer func() {
		_ = fastRemoveAll(pollDir)
		_ = fastRemoveAll(archiveDir)
	}()

	masterKeyStr, _ := libsecsecrets.GenerateKey()
	masterKey, _ := libsecsecrets.ResolveKey(context.Background(), masterKeyStr, "", "")
	encPass, _ := libsecsecrets.Encrypt(context.Background(), "password123", masterKey)
	libsecsecrets.ZeroBuffer(masterKey)

	_ = os.Setenv("SECRETPROTECTOR_KEY", masterKeyStr)
	defer func() { _ = os.Unsetenv("SECRETPROTECTOR_KEY") }()

	configPath := filepath.Join(tempDir, "unitests", "config_influx-"+timestamp+".json")
	configData := fmt.Sprintf(`{
		"poll": {
			"directory": "%s",
			"algorithm": "interval",
			"value": 1,
			"max_batch_size": 100,
			"max_verification_workers": 32
		},
		"integrity": {
			"algorithm": "size",
			"attempts": 3,
			"interval": 1
		},
		"action": {
			"type": "sftp",
			"concurrent_connections": 8,
			"post_process": {
				"action": "delete",
				"archive_path": "%s"
			},
			"sftp": {
				"host": "127.0.0.1",
				"port": %d,
				"username": "testuser",
				"encrypted_password": "%s",
				"master_key_env": "SECRETPROTECTOR_KEY",
				"remote_path": "/"
			}
		}
	}`, filepath.ToSlash(pollDir), filepath.ToSlash(archiveDir), port, encPass)
	_ = os.WriteFile(configPath, []byte(configData), 0o600)
	defer func() { _ = os.Remove(configPath) }()

	cfg, _, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}

	engine, err := NewEngine(cfg, false)
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errChan := make(chan error, 1)
	go func() {
		errChan <- engine.Run(ctx)
	}()

	// 1. Capture initial open handles
	runtime.GC()
	time.Sleep(500 * time.Millisecond)
	initialHandles, err := getOpenHandles()
	if err != nil {
		t.Fatalf("failed to get open handles count: %v", err)
	}
	t.Logf("Initial open process handles: %d", initialHandles)

	// 2. Continuous influx: write 500 files, 20 files every 100ms
	totalFiles := 500
	groupSize := 20
	groups := totalFiles / groupSize

	for g := 0; g < groups; g++ {
		for i := 0; i < groupSize; i++ {
			filename := fmt.Sprintf("influx_file_%d_%d.txt", g, i)
			_ = os.WriteFile(filepath.Join(pollDir, filename), []byte("influx test data"), 0o600)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// 3. Wait for all 500 files to be uploaded with a timeout
	timeout := time.After(35 * time.Second)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	success := false
	for {
		select {
		case <-timeout:
			engine.Close()
			cancel()
			t.Fatal("timeout waiting for all influx files to upload and delete")
		case <-ticker.C:
			uploadedCount := 0
			entries, err := os.ReadDir(sftpHome)
			if err == nil {
				for _, entry := range entries {
					if strings.HasPrefix(entry.Name(), "influx_file_") {
						uploadedCount++
					}
				}
			}

			localCount := 0
			localEntries, err := os.ReadDir(pollDir)
			if err == nil {
				for _, entry := range localEntries {
					if strings.HasPrefix(entry.Name(), "influx_file_") {
						localCount++
					}
				}
			}

			if uploadedCount == totalFiles && localCount == 0 {
				success = true
				goto ExitInflux
			}
		}
	}

ExitInflux:
	cancel()
	<-errChan
	engine.Close()

	// Forcibly kill SFTPGo child process and reap it to release handles
	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	// Clean up temporary directories on disk to release OS and antivirus handle locks on the 1000 files
	_ = fastRemoveAll(pollDir)
	_ = fastRemoveAll(archiveDir)
	_ = fastRemoveAll(sftpHome)

	if !success {
		t.Fatal("Influx upload failed")
	}

	// 4. Force GC and assert that final handle count did not leak
	runtime.GC()
	time.Sleep(1500 * time.Millisecond)
	runtime.GC()

	finalHandles, err := getOpenHandles()
	if err != nil {
		t.Fatalf("failed to get final open handles: %v", err)
	}
	t.Logf("Final open process handles: %d", finalHandles)

	// Allow a reasonable headroom for background Go network/dialer cached sockets and OS TCP TIME_WAIT states
	headroom := 120
	if finalHandles > initialHandles+headroom {
		t.Errorf("Handle leak detected: initial=%d, final=%d", initialHandles, finalHandles)
	} else {
		t.Log("Influx handle leak test passed successfully.")
	}
}

// TestLiveSFTPGo5KBatchIntegration tests 1,000 files under PollBatch mode with MaxBatchSize: 250
// against a real live SFTPGo instance, verifying real-time fsnotify event handling and backlog draining.
func TestLiveSFTPGo5KBatchIntegration(t *testing.T) {
	if os.Getenv("TEST_LIVE_SFTP_5K") != "true" && os.Getenv("TEST_LIVE_SFTP") != "true" {
		t.Skip("Skipping 5K live batch SFTP test. Set TEST_LIVE_SFTP_5K=true to run.")
	}

	tempDir := os.Getenv("TEMP")
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	uID := fmt.Sprintf("%d", time.Now().UnixNano())
	sftpHome := filepath.Join(tempDir, "sftp_home_5k_batch_"+uID)
	pollDir := filepath.Join(tempDir, "poll_5k_batch_"+uID)
	archiveDir := filepath.Join(tempDir, "archive_5k_batch_"+uID)

	_ = os.MkdirAll(sftpHome, 0o755)
	_ = os.MkdirAll(pollDir, 0o755)
	_ = os.MkdirAll(archiveDir, 0o755)

	defer func() {
		_ = os.RemoveAll(sftpHome)
		_ = os.RemoveAll(pollDir)
		_ = os.RemoveAll(archiveDir)
	}()

	totalFiles := 1000
	for i := 0; i < totalFiles; i++ {
		p := filepath.Join(pollDir, fmt.Sprintf("batch_scale_%d.txt", i))
		_ = os.WriteFile(p, []byte("batch scale test data"), 0o600)
	}

	masterKeyStr, _ := libsecsecrets.GenerateKey()
	masterKey, _ := libsecsecrets.ResolveKey(context.Background(), masterKeyStr, "", "")
	encPass, _ := libsecsecrets.Encrypt(context.Background(), "password123", masterKey)
	libsecsecrets.ZeroBuffer(masterKey)

	_ = os.Setenv("SECRETPROTECTOR_KEY", masterKeyStr)
	defer func() { _ = os.Unsetenv("SECRETPROTECTOR_KEY") }()

	sftpgoPath := getSFTPGoPath()
	port := getSFTPGoPort()
	cmd := exec.Command(sftpgoPath, "portable",
		"-d", sftpHome,
		"-u", "testuser",
		"-p", "password123",
		"-s", strconv.Itoa(port),
		"-g", "*",
	)
	cmd.Dir = sftpHome

	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start SFTPGo: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()

	time.Sleep(2 * time.Second)

	cfg := &config.Config{
		Poll: config.PollConfig{
			Directory:           pollDir,
			Algorithm:           config.PollBatch,
			Value:               250,
			MaxBatchSize:        250,
			BatchTimeoutSeconds: 5,
		},
		Integrity: config.IntegrityConfig{
			Algorithm:            config.IntegrityNone,
			VerificationAttempts: 0,
			VerificationInterval: 0,
		},
		Action: config.ActionConfig{
			Type:                  config.ActionSFTP,
			ConcurrentConnections: 16,
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

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	errChan := make(chan error, 1)
	go func() { errChan <- engine.Run(ctx) }()

	success := false
	for start := time.Now(); time.Since(start) < 25*time.Second; {
		entries, err := os.ReadDir(pollDir)
		if err == nil && len(entries) == 0 {
			success = true
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	cancel()
	<-errChan

	if !success {
		entries, _ := os.ReadDir(pollDir)
		t.Fatalf("5K Batch Live Test timed out: %d files remaining in pollDir", len(entries))
	}
}
