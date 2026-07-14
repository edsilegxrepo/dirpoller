// Package archive handles post-processing tasks like deletion, moving, or zstd compression.
//
// Objective:
// Manage the local file lifecycle reliably after successful processing. It ensures
// that files are either safely stored (Archive/Compress) or removed, while
// guaranteeing that no file is lost or processed twice due to partial failures.
//
// Core Components:
// - Archiver: Orchestrates the post-processing transaction.
// - Tar/Zstd Writers: Interfaces for high-performance concurrent compression.
//
// Data Flow:
// 1. Transaction Start: Archiver receives a list of successfully processed files.
// 2. Prepare: Files are moved to a hidden .staging directory to prevent interference.
// 3. Commit: The configured action (Delete, Move, or Compress) is performed from staging.
// 4. Finalize: Staging directory is cleaned up upon success.
// 5. Rollback: On failure, files are moved back to their original locations from staging.
package archive

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"criticalsys.net/dirpoller/internal/config"
	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
)

// TarWriter defines the interface for tar archive operations.
type TarWriter interface {
	WriteHeader(hdr *tar.Header) error
	io.WriteCloser
}

// Archiver manages the cleanup and archiving of successfully processed files.
// It supports permanent deletion, moving to datestamped folders, or consolidation into .tar.zst archives.
type Archiver struct {
	cfg           *config.Config
	newTarWriter  func(w io.Writer) TarWriter
	newZstdWriter func(w io.Writer) (io.WriteCloser, error)
}

// NewArchiver creates a new archiver instance.
func NewArchiver(cfg *config.Config) *Archiver {
	return &Archiver{
		cfg: cfg,
		newTarWriter: func(w io.Writer) TarWriter {
			return tar.NewWriter(w)
		},
		newZstdWriter: func(w io.Writer) (io.WriteCloser, error) {
			return zstd.NewWriter(w, zstd.WithEncoderConcurrency(0))
		},
	}
}

// Process executes the configured post-action using a transactional pattern.
//
// Objective:
// Guarantee atomicity of the post-processing phase. If the system crashes
// or an error occurs during archiving, the files remain in their original
// state or in a recoverable staging area.
//
// Logic:
// 1. stageFiles: Moves candidates to .staging with unique UUID subfolders.
// 2. commit: Performs the final operation (Delete, MoveArchive, or Compress).
// 3. rollback: Automatically invoked on failure to restore original file paths.
func (a *Archiver) Process(ctx context.Context, files []string) error {
	if len(files) == 0 {
		return nil
	}

	// 1. PREPARE: Move files to staging
	stagingDir, stagedFiles, err := a.stageFiles(ctx, files)
	if err != nil {
		// Attempt rollback for any already staged files
		_ = a.rollback(ctx, stagedFiles)
		if stagingDir != "" {
			_ = concurrentRemoveAll(stagingDir)
		}
		return fmt.Errorf("failed to stage files for archiving: %w", err)
	}

	// 2. COMMIT: Execute the actual post-action from staging
	var commitErr error
	switch a.cfg.Action.PostProcess.Action {
	case config.PostActionDelete:
		commitErr = concurrentRemoveAll(stagingDir)
	case config.PostActionMoveArchive:
		commitErr = a.moveToFolder(stagingDir)
	case config.PostActionMoveCompress:
		commitErr = a.compressToArchive(ctx, stagingDir, stagedFiles)
	default:
		commitErr = fmt.Errorf("unsupported post action: %s", a.cfg.Action.PostProcess.Action)
	}

	if commitErr != nil {
		// ROLLBACK: Move files back to source
		_ = a.rollback(ctx, stagedFiles)
		_ = concurrentRemoveAll(stagingDir)
		return fmt.Errorf("failed to commit archiving transaction: %w", commitErr)
	}

	// Cleanup staging dir if it wasn't removed by the action
	if _, err := os.Stat(stagingDir); err == nil {
		_ = concurrentRemoveAll(stagingDir)
	}

	return nil
}

func (a *Archiver) stageFiles(ctx context.Context, files []string) (string, map[string]string, error) {
	batchID := uuid.NewString()

	// 1. Check for absolute paths for archive staging
	archivePath := a.cfg.Action.PostProcess.ArchivePath
	if archivePath == "" || !filepath.IsAbs(archivePath) {
		return "", nil, fmt.Errorf("absolute archive_path required: %s", archivePath)
	}

	// 2. Construct the stagingBase as archivepath + .staging + a unique UUID
	stagingDir := filepath.Join(archivePath, ".staging", batchID)

	// If path cannot be created, throw an error
	if err := os.MkdirAll(stagingDir, 0o750); err != nil {
		return "", nil, fmt.Errorf("failed to create staging directory %s: %w", stagingDir, err)
	}

	staged := make(map[string]string) // stagedPath -> originalPath
	var mu sync.Mutex

	// Bounded worker pool to execute renames concurrently
	numWorkers := 16
	if len(files) < numWorkers {
		numWorkers = len(files)
	}

	jobs := make(chan string, len(files))
	for _, f := range files {
		jobs <- f
	}
	close(jobs)

	var wg sync.WaitGroup
	errChan := make(chan error, numWorkers)
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				select {
				case <-workerCtx.Done():
					return
				default:
					fClean := filepath.Clean(f)
					dest := filepath.Clean(filepath.Join(stagingDir, filepath.Base(fClean)))
					if err := os.Rename(fClean, dest); err != nil {
						select {
						case errChan <- err:
							cancel() // abort other workers on first error
						default:
						}
						return
					}
					mu.Lock()
					staged[dest] = fClean
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	select {
	case err := <-errChan:
		return stagingDir, staged, err
	default:
	}

	if workerCtx.Err() != nil {
		return stagingDir, staged, workerCtx.Err()
	}

	return stagingDir, staged, nil
}

func (a *Archiver) rollback(ctx context.Context, staged map[string]string) error {
	if len(staged) == 0 {
		return nil
	}

	// Bounded worker pool to execute rollbacks concurrently
	numWorkers := 16
	if len(staged) < numWorkers {
		numWorkers = len(staged)
	}

	type renameJob struct {
		stagedPath   string
		originalPath string
	}
	jobs := make(chan renameJob, len(staged))
	for k, v := range staged {
		jobs <- renameJob{stagedPath: k, originalPath: v}
	}
	close(jobs)

	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				select {
				case <-ctx.Done():
					return // Abort rollback immediately if context is canceled
				default:
				}
				if err := os.Rename(job.stagedPath, job.originalPath); err != nil {
					mu.Lock()
					errs = append(errs, fmt.Errorf("rollback failed for %s -> %s: %v", job.stagedPath, job.originalPath, err))
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	if len(errs) > 0 {
		log.Printf("Warning: archiving rollback encountered errors: %v\n", errs)
		return fmt.Errorf("rollback incomplete")
	}
	return nil
}

func (a *Archiver) moveToFolder(stagingDir string) error {
	datestamp := time.Now().Format("20060102-150405.000000")
	destDir := filepath.Join(a.cfg.Action.PostProcess.ArchivePath, datestamp)

	// Create parent dir if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(destDir), 0o750); err != nil {
		return err
	}

	return os.Rename(stagingDir, destDir)
}

func (a *Archiver) compressToArchive(ctx context.Context, stagingDir string, staged map[string]string) error {
	datestamp := time.Now().Format("20060102-150405.000000")
	archiveName := fmt.Sprintf("batch-%s.zst", datestamp)
	archivePath := filepath.Clean(filepath.Join(a.cfg.Action.PostProcess.ArchivePath, archiveName))

	if err := os.MkdirAll(a.cfg.Action.PostProcess.ArchivePath, 0o750); err != nil {
		return err
	}

	// Atomic Archive Creation: Write to a temp file first, then rename.
	// This ensures that a partial or corrupted archive is never visible at the final path.
	tmpArchivePath := archivePath + "." + uuid.NewString() + ".tmp"
	f, err := os.Create(filepath.Clean(tmpArchivePath)) // #nosec G304
	if err != nil {
		return err
	}

	cleanupDone := false
	defer func() {
		_ = f.Close()
		if !cleanupDone {
			_ = os.Remove(tmpArchivePath)
		}
	}()

	enc, err := a.newZstdWriter(f)
	if err != nil {
		return err
	}
	defer func() { _ = enc.Close() }()

	tw := a.newTarWriter(enc)
	defer func() { _ = tw.Close() }()

	buf := make([]byte, 1*1024*1024)
	for stagedPath := range staged {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			if err := a.addFileToArchive(tw, stagedPath, buf); err != nil {
				return err
			}
		}
	}

	// Explicitly close writers to ensure all data is flushed before rename.
	if err := tw.Close(); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	if err := os.Rename(tmpArchivePath, archivePath); err != nil {
		return err
	}
	cleanupDone = true
	return nil
}

func (a *Archiver) addFileToArchive(tw TarWriter, path string, buf []byte) error {
	f, err := os.Open(filepath.Clean(path)) // #nosec G304
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			log.Printf("Warning: failed to close file %s: %v\n", path, closeErr)
		}
	}()

	info, err := f.Stat()
	if err != nil {
		return err
	}

	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = filepath.Base(path)

	if err := tw.WriteHeader(header); err != nil {
		return err
	}

	_, err = io.CopyBuffer(tw, f, buf)
	return err
}

// concurrentRemoveAll removes a directory and its contents concurrently
// using a bounded pool of Go workers.
func concurrentRemoveAll(dir string) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}

	// 1. Open the directory to read in chunks
	f, err := os.Open(filepath.Clean(dir))
	if err != nil {
		return os.RemoveAll(dir) // Fallback
	}
	defer func() { _ = f.Close() }()

	// 2. Setup job channel and worker group
	const numWorkers = 16
	jobs := make(chan string, 1000)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error

	// 3. Spawn workers to delete files concurrently
	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			}
		}()
	}

	// 4. Stream file paths to workers in chunks of 1000 (constant low memory)
	for {
		entries, readErr := f.ReadDir(1000)
		for _, entry := range entries {
			if !entry.IsDir() {
				jobs <- filepath.Join(dir, entry.Name())
			}
		}
		if readErr != nil {
			break
		}
	}
	close(jobs)
	_ = f.Close() // Explicitly close the folder handle on Windows to release the lock before deleting it
	wg.Wait()

	// 5. Delete the parent folder itself
	if err := os.Remove(dir); err != nil && !os.IsNotExist(err) {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
