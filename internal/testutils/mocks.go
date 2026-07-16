// Package testutils (Mocks) provides shared mock implementations for internal interfaces.
//
// Objective:
// Enable unit testing of core logic by providing controllable, predictable
// implementations of platform-native and external service interfaces.
//
// Mocks Provided:
// - MockFileInfo: Simulates os.FileInfo.
// - MockSFTPClient/File/Dialer: Simulates the SFTP/SSH transport layer.
// - MockFileVerifier: Simulates file integrity checks.
// - MockOSUtils: Simulates platform-specific file operations.
// - MockLogger: Simulates system event logging.
package testutils

import (
	"bytes"
	"context"
	"io"
	"os"
	"time"
)

// MockFileInfo implements os.FileInfo for testing.
type MockFileInfo struct {
	FName    string
	FSize    int64
	FMode    os.FileMode
	FModTime time.Time
	FIsDir   bool
}

func (m *MockFileInfo) Name() string       { return m.FName }
func (m *MockFileInfo) Size() int64        { return m.FSize }
func (m *MockFileInfo) Mode() os.FileMode  { return m.FMode }
func (m *MockFileInfo) ModTime() time.Time { return m.FModTime }
func (m *MockFileInfo) IsDir() bool        { return m.FIsDir }
func (m *MockFileInfo) Sys() interface{}   { return nil }

// MockSFTPFile implements the SFTPFile interface for testing.
type MockSFTPFile struct {
	Buffer         *bytes.Buffer
	WriteErr       error
	CloseErr       error
	ReadFromCalled bool
}

func (m *MockSFTPFile) Write(p []byte) (n int, err error) {
	if m.WriteErr != nil {
		return 0, m.WriteErr
	}
	return m.Buffer.Write(p)
}

func (m *MockSFTPFile) ReadFrom(r io.Reader) (n int64, err error) {
	m.ReadFromCalled = true
	if m.WriteErr != nil {
		return 0, m.WriteErr
	}
	return m.Buffer.ReadFrom(r)
}

func (m *MockSFTPFile) Close() error {
	return m.CloseErr
}

// MockSFTPClient implements the SFTPClient interface for testing.
type MockSFTPClient struct {
	MkdirAllErr  error
	CreateErr    error
	StatErr      error
	RenameErr    error
	RemoveErr    error
	ReadDirErr   error
	CloseErr     error
	CreatedFile  *MockSFTPFile
	Files        []os.FileInfo
	RemovedPaths []string
	RenamedPaths [][2]string
	StatFunc     func(path string) (os.FileInfo, error)
	CreateFunc   func(path string) (interface{}, error)
}

func (m *MockSFTPClient) MkdirAll(path string) error { return m.MkdirAllErr }

func (m *MockSFTPClient) Create(path string) (interface{}, error) {
	if m.CreateFunc != nil {
		return m.CreateFunc(path)
	}
	if m.CreateErr != nil {
		return nil, m.CreateErr
	}
	if m.CreatedFile != nil {
		return m.CreatedFile, nil
	}
	m.CreatedFile = &MockSFTPFile{Buffer: new(bytes.Buffer)}
	return m.CreatedFile, nil
}

func (m *MockSFTPClient) Stat(path string) (os.FileInfo, error) {
	if m.StatFunc != nil {
		return m.StatFunc(path)
	}
	if m.StatErr != nil {
		return nil, m.StatErr
	}
	size := int64(0)
	if m.CreatedFile != nil {
		size = int64(m.CreatedFile.Buffer.Len())
	}
	return &MockFileInfo{FName: path, FSize: size}, nil
}

func (m *MockSFTPClient) Rename(oldpath, newpath string) error {
	m.RenamedPaths = append(m.RenamedPaths, [2]string{oldpath, newpath})
	return m.RenameErr
}

func (m *MockSFTPClient) Remove(path string) error {
	m.RemovedPaths = append(m.RemovedPaths, path)
	return m.RemoveErr
}

func (m *MockSFTPClient) ReadDir(path string) ([]os.FileInfo, error) {
	if m.ReadDirErr != nil {
		return nil, m.ReadDirErr
	}
	return m.Files, nil
}

func (m *MockSFTPClient) Close() error { return m.CloseErr }

// MockSSHClient implements the SSHClient interface (io.Closer)
type MockSSHClient struct {
	CloseErr error
}

func (m *MockSSHClient) Close() error {
	return m.CloseErr
}

// MockDialer implements the Dialer interface.
type MockDialer struct {
	Client *MockSFTPClient
	Conn   *MockSSHClient
	Err    error
}

func (m *MockDialer) Dial(network, addr string, config interface{}) (interface{}, interface{}, error) {
	return m.Client, m.Conn, m.Err
}

// MockFileVerifier provides a shared mock for file verification logic.
type MockFileVerifier struct {
	VerifyOk  bool
	VerifyErr error
	Hash      string
	HashErr   error
}

func (m *MockFileVerifier) Verify(ctx context.Context, path string) (bool, error) {
	return m.VerifyOk, m.VerifyErr
}

func (m *MockFileVerifier) CalculateHash(path string) (string, error) {
	return m.Hash, m.HashErr
}

// MockPostArchiver provides a shared mock for post-processing logic.
type MockPostArchiver struct {
	Err error
}

func (m *MockPostArchiver) Process(ctx context.Context, files []string) error {
	return m.Err
}

// MockOSUtils provides a shared mock for platform-specific file operations.
type MockOSUtils struct {
	Locked             bool
	HasSubfoldersValue bool
	Files              []string
	Err                error
	StatErr            error
	StatInfo           os.FileInfo
	IsLockedErr        error
}

func (m *MockOSUtils) IsLocked(path string) (bool, error) {
	if m.IsLockedErr != nil {
		return false, m.IsLockedErr
	}
	return m.Locked, m.Err
}

func (m *MockOSUtils) HasSubfolders(path string) (bool, error) {
	return m.HasSubfoldersValue, m.Err
}

func (m *MockOSUtils) GetFiles(dir string) ([]string, error) {
	return m.Files, m.Err
}

func (m *MockOSUtils) Stat(path string) (os.FileInfo, error) {
	if m.StatErr != nil {
		return nil, m.StatErr
	}
	if m.StatInfo != nil {
		return m.StatInfo, nil
	}
	return &MockFileInfo{FName: path}, nil
}

// MockLogger provides a shared mock for system logging.
type MockLogger struct {
	Err      error
	CloseErr error
}

func (m *MockLogger) Error(id uint32, msg string) error {
	return m.Err
}

func (m *MockLogger) Info(id uint32, msg string) error {
	return m.Err
}

func (m *MockLogger) Warn(msg string) {}

func (m *MockLogger) Close() error {
	return m.CloseErr
}
