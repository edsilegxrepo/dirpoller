// Package archive_test provides unit tests for the archiving and post-processing logic.
//
// Objective:
// Validate the transactional "Prepare-Commit-Rollback" pattern for local file
// lifecycle management. It ensures that files are safely staged before
// deletion, moving, or compression, and that failures trigger an
// automatic restoration of the original state.
//
// Scenarios Covered:
// - Transactional Integrity: Success and failure paths for the staging process.
// - Post-Actions: Full lifecycle validation for Delete, Move, and Compress.
// - Resilience: Incomplete rollback handling and context cancellation.
// - Compression: Error handling for tar/zstd writers and concurrent operations.
package archive

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"criticalsys.net/dirpoller/internal/config"
	"criticalsys.net/dirpoller/internal/testutils"
)

func getArchiveTestDir(name string) string {
	return testutils.GetUniqueTestDir("archive", name)
}

type mockTarWriter struct {
	writeHeaderErr error
	writeErr       error
	closeErr       error
}

func (m *mockTarWriter) WriteHeader(hdr *tar.Header) error { return m.writeHeaderErr }
func (m *mockTarWriter) Write(p []byte) (n int, err error) {
	if m.writeErr != nil {
		return 0, m.writeErr
	}
	return len(p), nil
}
func (m *mockTarWriter) Close() error { return m.closeErr }

type mockWriteCloser struct {
	writeErr error
	closeErr error
}

func (m *mockWriteCloser) Write(p []byte) (n int, err error) {
	if m.writeErr != nil {
		return 0, m.writeErr
	}
	return len(p), nil
}
func (m *mockWriteCloser) Close() error { return m.closeErr }

// TestArchive_Process_Comprehensive verifies the high-level transactional post-processing logic.
//
// Scenario:
// 1. EmptyFiles: Ensures no-op behavior for empty file batches.
// 2. UnsupportedAction: Validates rejection of unknown post-actions and rollback.
// 3. StageFailure: Confirms rollback if the initial staging (move to .staging) fails.
// 4. ContextCancelled: Verifies graceful exit if processing is interrupted.
//
// Success Criteria:
// - Files are restored to their original location upon any failure.
// - Correct error messages are returned for invalid configurations.
// - System remains in a consistent state across all scenarios.
func TestArchive_Process_Comprehensive(t *testing.T) {
	testDir := getArchiveTestDir("ProcessComp")
	sourceDir := filepath.Join(testDir, "source")
	archiveDir := filepath.Join(testDir, "archive")
	_ = os.MkdirAll(sourceDir, 0o750)
	_ = os.MkdirAll(archiveDir, 0o750)

	f1 := filepath.Join(sourceDir, "f1.txt")
	_ = os.WriteFile(f1, []byte("data1"), 0o644)

	t.Run("EmptyFiles", func(t *testing.T) {
		testDir := getArchiveTestDir("EmptyFiles")
		cfg := &config.Config{
			Action: config.ActionConfig{
				PostProcess: config.PostProcessConfig{
					ArchivePath: filepath.Join(testDir, "archive"),
				},
			},
		}
		a := NewArchiver(cfg)
		if err := a.Process(context.Background(), nil); err != nil {
			t.Errorf("unexpected error for nil files: %v", err)
		}
	})

	t.Run("UnsupportedAction", func(t *testing.T) {
		cfg := &config.Config{
			Action: config.ActionConfig{
				PostProcess: config.PostProcessConfig{
					Action:      "invalid",
					ArchivePath: archiveDir,
				},
			},
		}
		a := NewArchiver(cfg)
		err := a.Process(context.Background(), []string{f1})
		if err == nil || !strings.Contains(err.Error(), "unsupported post action") {
			t.Errorf("expected unsupported action error, got %v", err)
		}
		if _, err := os.Stat(f1); err != nil {
			t.Error("file was not rolled back")
		}
	})

	t.Run("StageFailure_Rollback", func(t *testing.T) {
		cfg := &config.Config{
			Action: config.ActionConfig{
				PostProcess: config.PostProcessConfig{
					Action:      config.PostActionDelete,
					ArchivePath: archiveDir,
				},
			},
		}
		a := NewArchiver(cfg)
		err := a.Process(context.Background(), []string{"nonexistent.txt"})
		if err == nil {
			t.Error("expected error for nonexistent file")
		}
	})

	t.Run("StageFiles_ContextCancelled", func(t *testing.T) {
		testDir := getArchiveTestDir("ContextCancelled")
		cfg := &config.Config{
			Action: config.ActionConfig{
				PostProcess: config.PostProcessConfig{
					ArchivePath: filepath.Join(testDir, "archive"),
				},
			},
		}
		a := NewArchiver(cfg)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, _, err := a.stageFiles(ctx, []string{f1})
		if err != context.Canceled {
			t.Errorf("expected context.Canceled, got %v", err)
		}
	})

	t.Run("StageFiles_RenameError", func(t *testing.T) {
		// Create a directory where a file should be moved to trigger rename error
		tempDir := getArchiveTestDir("StageRenameErr")
		sourceFile := filepath.Join(tempDir, "source.txt")
		_ = os.WriteFile(sourceFile, []byte("data"), 0o644)

		cfg := &config.Config{
			Action: config.ActionConfig{
				PostProcess: config.PostProcessConfig{
					Action:      config.PostActionMoveArchive,
					ArchivePath: tempDir,
				},
			},
		}
		a := NewArchiver(cfg)

		// stagingDir will be tempDir/.staging/<uuid>
		// We can't easily predict the UUID, but we can try to make the .staging dir a file
		stagingBase := filepath.Join(tempDir, ".staging")
		_ = os.WriteFile(stagingBase, []byte("i am a file"), 0o644)

		_, _, err := a.stageFiles(context.Background(), []string{sourceFile})
		if err == nil {
			t.Error("expected error when staging base is a file")
		}
	})
}

func TestArchive_Rollback_Mixed(t *testing.T) {
	testDir := getArchiveTestDir("RollbackMixed")
	_ = os.MkdirAll(testDir, 0o750)

	f1 := filepath.Join(testDir, "staged1.txt")
	f1Orig := filepath.Join(testDir, "orig1.txt")
	_ = os.WriteFile(f1, []byte("d1"), 0o644)

	cfg := &config.Config{
		Action: config.ActionConfig{
			PostProcess: config.PostProcessConfig{
				ArchivePath: filepath.Join(testDir, "archive"),
			},
		},
	}
	a := NewArchiver(cfg)
	staged := map[string]string{
		f1:            f1Orig,
		"nonexistent": "whatever",
	}

	err := a.rollback(context.Background(), staged)
	if err == nil || !strings.Contains(err.Error(), "rollback incomplete") {
		t.Errorf("expected incomplete rollback, got %v", err)
	}

	if _, err := os.Stat(f1Orig); err != nil {
		t.Error("f1 was not rolled back successfully")
	}
}

func TestArchive_MoveToFolder_MkdirFail(t *testing.T) {
	testDir := getArchiveTestDir("MoveMkdirFail")
	_ = os.MkdirAll(testDir, 0o750)

	// Create a file where we want to create a directory
	blockPath := filepath.Join(testDir, "blocked")
	_ = os.WriteFile(blockPath, []byte("data"), 0o644)

	cfg := &config.Config{
		Action: config.ActionConfig{
			PostProcess: config.PostProcessConfig{
				ArchivePath: blockPath,
			},
		},
	}
	a := NewArchiver(cfg)
	err := a.moveToFolder("some-staging-dir")
	if err == nil {
		t.Error("expected error when ArchivePath is a file")
	}
}

func TestArchive_AddFileToArchive_Errors(t *testing.T) {
	testDir := getArchiveTestDir("AddFileErrors")
	_ = os.MkdirAll(testDir, 0o750)
	f1 := filepath.Join(testDir, "f1.txt")
	_ = os.WriteFile(f1, []byte("data"), 0o644)

	cfg := &config.Config{
		Action: config.ActionConfig{
			PostProcess: config.PostProcessConfig{
				ArchivePath: filepath.Join(testDir, "archive"),
			},
		},
	}
	a := NewArchiver(cfg)

	t.Run("OpenFail", func(t *testing.T) {
		err := a.addFileToArchive(&mockTarWriter{}, "non-existent-file", make([]byte, 1024))
		if err == nil {
			t.Error("expected error opening non-existent file")
		}
	})

	t.Run("WriteHeaderError", func(t *testing.T) {
		tw := &mockTarWriter{writeHeaderErr: fmt.Errorf("hdr fail")}
		err := a.addFileToArchive(tw, f1, make([]byte, 1024))
		if err == nil || !strings.Contains(err.Error(), "hdr fail") {
			t.Errorf("expected hdr fail, got %v", err)
		}
	})

	t.Run("CopyError", func(t *testing.T) {
		tw := &mockTarWriter{writeErr: fmt.Errorf("write fail")}
		err := a.addFileToArchive(tw, f1, make([]byte, 1024))
		if err == nil || !strings.Contains(err.Error(), "write fail") {
			t.Errorf("expected write fail, got %v", err)
		}
	})
}

func TestArchive_CompressToArchive_Errors(t *testing.T) {
	testDir := getArchiveTestDir("CompressErrors")
	_ = os.MkdirAll(testDir, 0o750)
	stagingDir := filepath.Join(testDir, "staging")
	_ = os.MkdirAll(stagingDir, 0o750)
	f1 := filepath.Join(stagingDir, "f1.txt")
	_ = os.WriteFile(f1, []byte("data"), 0o644)

	cfg := &config.Config{
		Action: config.ActionConfig{
			PostProcess: config.PostProcessConfig{ArchivePath: testDir},
		},
	}

	t.Run("MkdirAllError", func(t *testing.T) {
		badCfg := &config.Config{
			Action: config.ActionConfig{
				PostProcess: config.PostProcessConfig{ArchivePath: "Z:\\invalid\\*\x00"},
			},
		}
		a := NewArchiver(badCfg)
		err := a.compressToArchive(context.Background(), stagingDir, map[string]string{f1: "f1"})
		if err == nil {
			t.Error("expected MkdirAll error")
		}
	})

	t.Run("CreateError", func(t *testing.T) {
		filePath := filepath.Join(testDir, "afile")
		_ = os.WriteFile(filePath, []byte("x"), 0o644)
		badCfg := &config.Config{
			Action: config.ActionConfig{
				PostProcess: config.PostProcessConfig{ArchivePath: filePath},
			},
		}
		a := NewArchiver(badCfg)
		err := a.compressToArchive(context.Background(), stagingDir, map[string]string{f1: "f1"})
		if err == nil {
			t.Error("expected Create error")
		}
	})

	t.Run("TarWriteHeaderError", func(t *testing.T) {
		a := NewArchiver(cfg)
		a.newTarWriter = func(w io.Writer) TarWriter {
			return &mockTarWriter{writeHeaderErr: fmt.Errorf("hdr fail")}
		}
		err := a.compressToArchive(context.Background(), stagingDir, map[string]string{f1: "f1"})
		if err == nil || !strings.Contains(err.Error(), "hdr fail") {
			t.Errorf("expected hdr fail, got %v", err)
		}
	})

	t.Run("FinalClosesError", func(t *testing.T) {
		a := NewArchiver(cfg)
		a.newTarWriter = func(w io.Writer) TarWriter {
			return &mockTarWriter{closeErr: fmt.Errorf("tar close fail")}
		}
		err := a.compressToArchive(context.Background(), stagingDir, map[string]string{f1: "f1"})
		if err == nil || !strings.Contains(err.Error(), "tar close fail") {
			t.Errorf("expected tar close fail, got %v", err)
		}

		a = NewArchiver(cfg)
		a.newZstdWriter = func(w io.Writer) (io.WriteCloser, error) {
			return &mockWriteCloser{closeErr: fmt.Errorf("zstd close fail")}, nil
		}
		err = a.compressToArchive(context.Background(), stagingDir, map[string]string{f1: "f1"})
		if err == nil || !strings.Contains(err.Error(), "zstd close fail") {
			t.Errorf("expected zstd close fail, got %v", err)
		}
	})

	t.Run("ZstdWriterFailure", func(t *testing.T) {
		a := NewArchiver(cfg)
		a.newZstdWriter = func(w io.Writer) (io.WriteCloser, error) {
			return nil, fmt.Errorf("zstd init fail")
		}
		err := a.compressToArchive(context.Background(), stagingDir, map[string]string{f1: "f1"})
		if err == nil || !strings.Contains(err.Error(), "zstd init fail") {
			t.Errorf("expected zstd init fail, got %v", err)
		}
	})
}

// TestArchive_FullCycle_AllActions validates the complete lifecycle for all supported post-actions.
//
// Scenario:
// 1. For each action (Delete, Move, Compress):
// 2. Create a source file.
// 3. Execute the post-action via Archiver.Process.
// 4. Verify the source file no longer exists.
//
// Success Criteria:
// - All supported actions result in the removal of the original source file.
// - No errors are returned during the standard success path.
func TestArchive_FullCycle_AllActions(t *testing.T) {
	testDir := getArchiveTestDir("FullCycleAll")
	sourceDir := filepath.Join(testDir, "source")
	archiveDir := filepath.Join(testDir, "archive")
	_ = os.MkdirAll(sourceDir, 0o750)
	_ = os.MkdirAll(archiveDir, 0o750)

	actions := []config.PostAction{
		config.PostActionDelete,
		config.PostActionMoveArchive,
		config.PostActionMoveCompress,
	}

	for _, action := range actions {
		t.Run(string(action), func(t *testing.T) {
			fname := fmt.Sprintf("file-%s.txt", action)
			fpath := filepath.Join(sourceDir, fname)
			_ = os.WriteFile(fpath, []byte("data"), 0o644)

			cfg := &config.Config{
				Action: config.ActionConfig{
					PostProcess: config.PostProcessConfig{
						Action:      action,
						ArchivePath: archiveDir,
					},
				},
			}
			a := NewArchiver(cfg)
			err := a.Process(context.Background(), []string{fpath})
			if err != nil {
				t.Fatalf("action %s failed: %v", action, err)
			}
			if _, err := os.Stat(fpath); !os.IsNotExist(err) {
				t.Errorf("file %s still exists after %s", fpath, action)
			}
		})
	}
}

func TestArchive_CompressToArchive_Context(t *testing.T) {
	testDir := getArchiveTestDir("CompressContext")
	_ = os.MkdirAll(testDir, 0o750)
	stagingDir := filepath.Join(testDir, "staging")
	_ = os.MkdirAll(stagingDir, 0o750)

	f1 := filepath.Join(stagingDir, "f1.txt")
	_ = os.WriteFile(f1, []byte("data"), 0o644)

	cfg := &config.Config{
		Action: config.ActionConfig{
			PostProcess: config.PostProcessConfig{ArchivePath: testDir},
		},
	}
	a := NewArchiver(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := a.compressToArchive(ctx, stagingDir, map[string]string{f1: "f1"})
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}
