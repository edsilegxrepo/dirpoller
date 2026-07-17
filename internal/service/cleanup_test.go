package service

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"criticalsys.net/dirpoller/internal/config"
	"criticalsys.net/dirpoller/internal/testutils"
)

func TestPurgeOldEntries(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("service", "purge_entries_test")
	_ = os.MkdirAll(testDir, 0o755)
	defer func() { _ = os.RemoveAll(testDir) }()

	now := time.Now()

	// 1. Create a recent file
	recentFile := filepath.Join(testDir, "recent.txt")
	if err := os.WriteFile(recentFile, []byte("recent"), 0o644); err != nil {
		t.Fatalf("failed to write recent file: %v", err)
	}

	// 2. Create an old file (backdated 10 days)
	oldFile := filepath.Join(testDir, "old.txt")
	if err := os.WriteFile(oldFile, []byte("old"), 0o644); err != nil {
		t.Fatalf("failed to write old file: %v", err)
	}
	tenDaysAgo := now.AddDate(0, 0, -10)
	if err := os.Chtimes(oldFile, tenDaysAgo, tenDaysAgo); err != nil {
		t.Fatalf("failed to change times for old file: %v", err)
	}

	// 3. Create an old subdirectory (backdated 10 days)
	oldSubDir := filepath.Join(testDir, "old_subdir")
	if err := os.MkdirAll(oldSubDir, 0o755); err != nil {
		t.Fatalf("failed to create old subdir: %v", err)
	}
	if err := os.Chtimes(oldSubDir, tenDaysAgo, tenDaysAgo); err != nil {
		t.Fatalf("failed to change times for old subdir: %v", err)
	}

	// Cutoff at 5 days ago
	cutoff := now.AddDate(0, 0, -5)

	// Run with filter returning true for everything (files and directories)
	filterAll := func(entry os.DirEntry) bool { return true }
	err := purgeOldEntries(testDir, cutoff, filterAll)
	if err != nil {
		t.Fatalf("purgeOldEntries failed: %v", err)
	}

	// Recent file should exist
	if _, err := os.Stat(recentFile); err != nil {
		t.Errorf("recent file should still exist: %v", err)
	}

	// Old file should be deleted
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Errorf("old file should be deleted, got: %v", err)
	}

	// Old subdirectory should be deleted
	if _, err := os.Stat(oldSubDir); !os.IsNotExist(err) {
		t.Errorf("old subdir should be deleted, got: %v", err)
	}
}

func TestPurgeOldEntries_InvalidDir(t *testing.T) {
	// Should fail gracefully on non-existent directory
	err := purgeOldEntries("non_existent_dir_12345", time.Now(), func(entry os.DirEntry) bool { return true })
	if err == nil {
		t.Error("expected error for non-existent directory, got nil")
	}
}

func TestEngine_checkAndPurgeArchives(t *testing.T) {
	testDir := testutils.GetUniqueTestDir("service", "engine_archive_cleanup_test")
	archiveDir := filepath.Join(testDir, "archive")
	_ = os.MkdirAll(archiveDir, 0o755)
	defer func() { _ = os.RemoveAll(testDir) }()

	retentionDays := 3
	cfg := &config.Config{
		Action: config.ActionConfig{
			PostProcess: config.PostProcessConfig{
				ArchivePath:      archiveDir,
				ArchiveRetention: &retentionDays,
			},
		},
	}

	e := &Engine{
		cfg: cfg,
	}

	// 1. Create a recent file in the archive
	recentFile := filepath.Join(archiveDir, "recent.txt")
	_ = os.WriteFile(recentFile, []byte("recent"), 0o644)

	// 2. Create an old file in the archive (backdated 5 days)
	oldFile := filepath.Join(archiveDir, "old.txt")
	_ = os.WriteFile(oldFile, []byte("old"), 0o644)
	fiveDaysAgo := time.Now().AddDate(0, 0, -5)
	_ = os.Chtimes(oldFile, fiveDaysAgo, fiveDaysAgo)

	// 3. Create an old .staging folder in the archive (backdated 5 days)
	stagingFolder := filepath.Join(archiveDir, ".staging")
	_ = os.MkdirAll(stagingFolder, 0o755)
	_ = os.Chtimes(stagingFolder, fiveDaysAgo, fiveDaysAgo)

	// Run cleanup
	e.checkAndPurgeArchives()

	// Assert recent file is kept
	if _, err := os.Stat(recentFile); err != nil {
		t.Errorf("recent file should exist: %v", err)
	}

	// Assert old file is purged
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Errorf("old file should be purged, got: %v", err)
	}

	// Assert .staging folder is NOT purged (excluded by filter)
	if _, err := os.Stat(stagingFolder); err != nil {
		t.Errorf(".staging folder should be preserved: %v", err)
	}

	// Assert lastArchiveCleanupDate is set to today
	today := time.Now().Format("20060102")
	if e.lastArchiveCleanupDate != today {
		t.Errorf("expected lastArchiveCleanupDate %s, got %s", today, e.lastArchiveCleanupDate)
	}

	// If we write a new old file, and run it again today, it should NOT be purged because lastArchiveCleanupDate == today gates it!
	oldFile2 := filepath.Join(archiveDir, "old2.txt")
	_ = os.WriteFile(oldFile2, []byte("old2"), 0o644)
	_ = os.Chtimes(oldFile2, fiveDaysAgo, fiveDaysAgo)

	e.checkAndPurgeArchives()

	if _, err := os.Stat(oldFile2); err != nil {
		t.Errorf("old2 file should NOT be purged because of calendar day gating: %v", err)
	}
}
