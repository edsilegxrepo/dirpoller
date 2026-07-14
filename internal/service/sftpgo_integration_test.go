package service

import (
	"context"
	"criticalsys/secretprotector/pkg/libsecsecrets"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

	if err := os.MkdirAll(sftpHome, 0o750); err != nil {
		t.Fatalf("failed to create sftpgo home dir: %v", err)
	}
	if err := os.MkdirAll(pollDir, 0o750); err != nil {
		t.Fatalf("failed to create poll dir: %v", err)
	}
	defer func() {
		_ = fastRemoveAll(sftpHome)
		_ = fastRemoveAll(pollDir)
	}()

	// 1. Generate master key and encrypt password
	masterKeyStr, _ := libsecsecrets.GenerateKey()
	masterKey, _ := libsecsecrets.ResolveKey(context.Background(), masterKeyStr, "", "")
	encPass, _ := libsecsecrets.Encrypt(context.Background(), "password123", masterKey)
	libsecsecrets.ZeroBuffer(masterKey)

	_ = os.Setenv("SECRETPROTECTOR_KEY", masterKeyStr)
	defer func() { _ = os.Unsetenv("SECRETPROTECTOR_KEY") }()

	// 2. Start SFTPGo in portable mode in the background
	sftpgoPath := `d:\inetd\sftpgo\sftpgo.exe`
	cmd := exec.Command(sftpgoPath, "portable",
		"-d", sftpHome,
		"-u", "testuser",
		"-p", "password123",
		"-s", "2022",
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
				Port:              2022,
				Username:          "testuser",
				EncryptedPassword: encPass,
				MasterKeyEnv:      "SECRETPROTECTOR_KEY",
				RemotePath:        "/",
			},
			PostProcess: config.PostProcessConfig{
				Action:      config.PostActionDelete,
				ArchivePath: filepath.Join(tempDir, "unitests", "archive-"+timestamp),
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
	sftpgoPath := `d:\inetd\sftpgo\sftpgo.exe`
	cmd := exec.Command(sftpgoPath, "portable",
		"-d", sftpHome,
		"-u", "testuser",
		"-p", "password123",
		"-s", "2022",
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
				Port:              2022,
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
