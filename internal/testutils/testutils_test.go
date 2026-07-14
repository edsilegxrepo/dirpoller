// Package testutils_test provides unit tests for the shared testing utilities.
//
// Objective:
// Validate the core mocking infrastructure and helper functions used throughout
// the DirPoller test suite. It ensures that mocks for file systems, SFTP
// clients, and SSH connections behave predictably, allowing other tests
// to rely on them for high-fidelity simulations.
//
// Scenarios Covered:
// - Mock Metadata: Verification of MockFileInfo property retrieval.
// - SFTP Simulation: Testing of MockSFTPClient and MockSFTPFile operations.
// - Networking: Verification of MockDialer and connection establishment logic.
// - Path Management: Validation of unique test directory generation and cleanup.
// - Verifier/Archiver Mocks: Ensuring specialized component mocks return expected values.
package testutils

import (
	"bytes"
	"context"
	"os"
	"testing"
)

func TestMockFileInfo(t *testing.T) {
	info, _ := os.Stdin.Stat()
	m := &MockFileInfo{
		FName:    "test.txt",
		FSize:    100,
		FMode:    0o644,
		FModTime: info.ModTime(),
		FIsDir:   false,
	}

	if m.Name() != "test.txt" {
		t.Errorf("expected test.txt, got %s", m.Name())
	}
	if m.Size() != 100 {
		t.Errorf("expected 100, got %d", m.Size())
	}
	if m.Mode() != 0o644 {
		t.Errorf("expected 0644, got %v", m.Mode())
	}
	if m.IsDir() {
		t.Error("expected not a directory")
	}
	if m.Sys() != nil {
		t.Error("expected nil Sys()")
	}
}

func TestMockSFTPFile(t *testing.T) {
	m := &MockSFTPFile{Buffer: new(bytes.Buffer)}
	data := []byte("hello")
	n, err := m.Write(data)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if n != len(data) {
		t.Errorf("expected %d bytes, got %d", len(data), n)
	}

	if m.Close() != nil {
		t.Error("expected nil Close error")
	}

	m.WriteErr = os.ErrPermission
	_, err = m.Write(data)
	if err != os.ErrPermission {
		t.Errorf("expected permission error, got %v", err)
	}
}

// TestMockSFTPClient verifies the simulated SFTP client behavior.
//
// Scenario:
// 1. Create a MockSFTPClient.
// 2. Perform common SFTP operations (MkdirAll, Create, Stat, Rename, Remove, ReadDir).
//
// Success Criteria:
// - Operations must succeed by default.
// - Metadata (renamed paths, removed paths) must be recorded correctly.
// - MockFileInfo must be returned for Stat/ReadDir calls.
func TestMockSFTPClient(t *testing.T) {
	m := &MockSFTPClient{}

	if err := m.MkdirAll("/test"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	f, err := m.Create("/test/file.txt")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if f == nil {
		t.Fatal("expected file, got nil")
	}

	info, err := m.Stat("/test/file.txt")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if info.Name() != "/test/file.txt" {
		t.Errorf("expected /test/file.txt, got %s", info.Name())
	}

	if err := m.Rename("old", "new"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(m.RenamedPaths) != 1 || m.RenamedPaths[0][0] != "old" || m.RenamedPaths[0][1] != "new" {
		t.Error("rename paths not recorded correctly")
	}

	if err := m.Remove("file"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(m.RemovedPaths) != 1 || m.RemovedPaths[0] != "file" {
		t.Error("remove paths not recorded correctly")
	}

	m.Files = []os.FileInfo{&MockFileInfo{FName: "f1"}}
	files, err := m.ReadDir("/test")
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(files) != 1 || files[0].Name() != "f1" {
		t.Error("ReadDir failed")
	}

	if err := m.Close(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestMockDialer(t *testing.T) {
	mClient := &MockSFTPClient{}
	mConn := &MockSSHClient{}
	d := &MockDialer{Client: mClient, Conn: mConn}

	c, conn, err := d.Dial("tcp", "addr", nil)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if c != mClient || conn != mConn {
		t.Error("Dial returned wrong values")
	}
}

func TestMockFileVerifier(t *testing.T) {
	m := &MockFileVerifier{VerifyOk: true, Hash: "hash123"}
	ok, err := m.Verify(context.Background(), "path")
	if err != nil || !ok {
		t.Error("Verify failed")
	}
	h, err := m.CalculateHash("path")
	if err != nil || h != "hash123" {
		t.Error("CalculateHash failed")
	}
}

func TestMockPostArchiver(t *testing.T) {
	m := &MockPostArchiver{}
	if err := m.Process(context.Background(), []string{"f1"}); err != nil {
		t.Error("Process failed")
	}
}

func TestMockOSUtils(t *testing.T) {
	m := &MockOSUtils{Locked: true, HasSubfoldersValue: true, Files: []string{"f1"}}

	locked, err := m.IsLocked("p")
	if err != nil || !locked {
		t.Error("IsLocked failed")
	}

	has, err := m.HasSubfolders("p")
	if err != nil || !has {
		t.Error("HasSubfolders failed")
	}

	files, err := m.GetFiles("p")
	if err != nil || len(files) != 1 || files[0] != "f1" {
		t.Error("GetFiles failed")
	}

	info, err := m.Stat("p")
	if err != nil || info.Name() != "p" {
		t.Error("Stat failed")
	}
}

func TestMockLogger(t *testing.T) {
	m := &MockLogger{}
	if err := m.Info(1, "msg"); err != nil {
		t.Error("Info failed")
	}
	if err := m.Error(1, "msg"); err != nil {
		t.Error("Error failed")
	}
	m.Warn("test warning")

	m.CloseErr = os.ErrClosed
	if err := m.Close(); err != os.ErrClosed {
		t.Errorf("expected os.ErrClosed, got %v", err)
	}
}

// TestMockSFTPClient_Errors verifies error injection in the mock SFTP client.
//
// Scenario:
// 1. Configure the mock client to return specific errors for each method.
// 2. Invoke the methods and verify the errors are propagated.
// 3. Test custom function overrides (CreateFunc, StatFunc).
//
// Success Criteria:
// - The client must return the configured errors exactly.
// - Function overrides must take precedence over static error fields.
func TestMockSFTPClient_Errors(t *testing.T) {
	m := &MockSFTPClient{
		CreateErr:  os.ErrPermission,
		StatErr:    os.ErrNotExist,
		RenameErr:  os.ErrInvalid,
		RemoveErr:  os.ErrPermission,
		ReadDirErr: os.ErrNotExist,
		CloseErr:   os.ErrClosed,
	}

	if _, err := m.Create("path"); err != os.ErrPermission {
		t.Errorf("expected permission error, got %v", err)
	}
	if _, err := m.Stat("path"); err != os.ErrNotExist {
		t.Errorf("expected not exist error, got %v", err)
	}
	if err := m.Rename("a", "b"); err != os.ErrInvalid {
		t.Errorf("expected invalid error, got %v", err)
	}
	if err := m.Remove("path"); err != os.ErrPermission {
		t.Errorf("expected permission error, got %v", err)
	}
	if _, err := m.ReadDir("path"); err != os.ErrNotExist {
		t.Errorf("expected not exist error, got %v", err)
	}
	if err := m.Close(); err != os.ErrClosed {
		t.Errorf("expected closed error, got %v", err)
	}

	// Test custom funcs
	m.CreateFunc = func(path string) (interface{}, error) {
		return nil, os.ErrExist
	}
	if _, err := m.Create("path"); err != os.ErrExist {
		t.Error("CreateFunc not called")
	}

	m.StatFunc = func(path string) (os.FileInfo, error) {
		return nil, os.ErrExist
	}
	if _, err := m.Stat("path"); err != os.ErrExist {
		t.Error("StatFunc not called")
	}
}

func TestMockOSUtils_More(t *testing.T) {
	m := &MockOSUtils{
		IsLockedErr: os.ErrPermission,
		StatErr:     os.ErrNotExist,
	}
	if _, err := m.IsLocked("p"); err != os.ErrPermission {
		t.Error("IsLockedErr not handled")
	}
	if _, err := m.Stat("p"); err != os.ErrNotExist {
		t.Error("StatErr not handled")
	}

	m.StatErr = nil
	m.StatInfo = &MockFileInfo{FName: "custom"}
	info, err := m.Stat("p")
	if err != nil || info.Name() != "custom" {
		t.Errorf("StatInfo not returned correctly: %v", err)
	}
}

func TestUtils_Env(t *testing.T) {
	oldTemp := os.Getenv("TEMP")
	defer func() {
		_ = os.Setenv("TEMP", oldTemp)
	}()

	_ = os.Setenv("TEMP", "")
	base := GetTestBaseDir()
	if base == "" {
		t.Error("GetTestBaseDir failed with empty TEMP")
	}
}

func TestUtils_Directories(t *testing.T) {
	pkgDir := GetPackageTestDir("testpkg")
	if _, err := os.Stat(pkgDir); os.IsNotExist(err) {
		t.Errorf("GetPackageTestDir failed to create directory: %s", pkgDir)
	}

	uniqueDir := GetUniqueTestDir("testpkg", "TestFunc")
	if _, err := os.Stat(uniqueDir); os.IsNotExist(err) {
		t.Errorf("GetUniqueTestDir failed to create directory: %s", uniqueDir)
	}

	// Test IsWindows
	_ = IsWindows()
}
