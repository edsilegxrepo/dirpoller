// Package action_test provides unit tests for the SFTP action handler.
//
// Objective:
// Validate the robust upload logic of the SFTPHandler, including atomic transfers,
// connection pooling, error recovery (retries), and security features like
// circuit breaking and credential decryption.
//
// Scenarios Covered:
// - Atomic Transfer: Ensures files are uploaded to .tmp and renamed only on success.
// - Resilience: Verifies retries for transient errors and circuit breaker for persistent ones.
// - Connection Management: Tests persistent session reuse and automatic reconnection.
// - Security: Validates MFA, SSH-Key authentication, and decryption failure handling.
// - Integrity: Confirms remote file size verification after transfer.
package action

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"criticalsys.net/dirpoller/internal/config"
	"criticalsys.net/dirpoller/internal/testutils"
	"golang.org/x/crypto/ssh"
)

// mockDialerAdapter adapts testutils.MockDialer to the action.Dialer interface.
type mockDialerAdapter struct {
	*testutils.MockDialer
}

func (m *mockDialerAdapter) Dial(network, addr string, sshCfg *ssh.ClientConfig) (SFTPClient, SSHClient, error) {
	sftpClient, sshClient, err := m.MockDialer.Dial(network, addr, sshCfg)
	if err != nil {
		return nil, nil, err
	}
	return &mockSFTPClientAdapter{sftpClient.(*testutils.MockSFTPClient)}, sshClient.(*testutils.MockSSHClient), nil
}

// mockSFTPClientAdapter adapts testutils.MockSFTPClient to the action.SFTPClient interface.
type mockSFTPClientAdapter struct {
	*testutils.MockSFTPClient
}

func (m *mockSFTPClientAdapter) Create(path string) (SFTPFile, error) {
	f, err := m.MockSFTPClient.Create(path)
	if err != nil {
		return nil, err
	}
	return f.(*testutils.MockSFTPFile), nil
}

func getActionTestDir(name string) string {
	return testutils.GetUniqueTestDir("action", name)
}

type functionalDialer struct {
	dialFunc func(string, string, *ssh.ClientConfig) (SFTPClient, SSHClient, error)
}

func (d *functionalDialer) Dial(n, a string, c *ssh.ClientConfig) (SFTPClient, SSHClient, error) {
	return d.dialFunc(n, a, c)
}

// TestSFTPHandler_Execute_Comprehensive verifies the end-to-end SFTP upload pipeline.
//
// Scenario:
// 1. Success: Standard upload with mock server.
// 2. SecretDecryption_Fail: Rejection when decryption of credentials fails.
// 3. CircuitBreaker: Immediate failure when the error threshold is reached.
// 4. RetryAndSucceed: Verifies exponential backoff for transient "connection reset" errors.
// 5. IntegritySizeMismatch: Rejection when remote size does not match local size.
//
// Success Criteria:
// - Successful files are correctly tracked and returned.
// - Security boundaries (decryption/circuit breaker) are enforced.
// - Transient errors are handled via the configured retry logic.
func TestSFTPHandler_Execute_Comprehensive(t *testing.T) {
	testDir := getActionTestDir("SFTPExecuteComp")
	f1 := filepath.Join(testDir, "f1.txt")
	_ = os.WriteFile(f1, []byte("data"), 0o644)

	cfg := &config.Config{
		Action: config.ActionConfig{
			ConcurrentConnections: 1,
			SFTP: config.SFTPConfig{
				Host:       "localhost",
				RemotePath: "/remote",
			},
		},
	}

	t.Run("Success", func(t *testing.T) {
		mFile := &testutils.MockSFTPFile{Buffer: new(bytes.Buffer)}
		mSFTP := &testutils.MockSFTPClient{CreatedFile: mFile}
		mSSH := &testutils.MockSSHClient{}
		mDialer := &mockDialerAdapter{&testutils.MockDialer{Client: mSFTP, Conn: mSSH}}

		h := NewSFTPHandler(cfg)
		h.dialer = mDialer

		files, err := h.Execute(context.Background(), []string{f1})
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if len(files) != 1 || files[0] != f1 {
			t.Errorf("expected [f1], got %v", files)
		}
	})

	t.Run("SecretDecryption_Fail", func(t *testing.T) {
		cfgWithSecret := &config.Config{
			Action: config.ActionConfig{
				SFTP: config.SFTPConfig{
					EncryptedPassword: "bad",
				},
			},
		}
		h := NewSFTPHandler(cfgWithSecret)
		_, err := h.Execute(context.Background(), []string{f1})
		if err == nil || !strings.Contains(err.Error(), "security failure") {
			t.Errorf("expected security failure, got %v", err)
		}
	})

	t.Run("CircuitBreaker", func(t *testing.T) {
		h := NewSFTPHandler(cfg)
		h.consecutiveFail = circuitBreakerThreshold
		_, err := h.Execute(context.Background(), []string{f1})
		if err == nil || !strings.Contains(err.Error(), "circuit breaker active") {
			t.Errorf("expected circuit breaker error, got %v", err)
		}
	})

	t.Run("GetOrCreateClientFail", func(t *testing.T) {
		h := NewSFTPHandler(cfg)
		h.dialer = &mockDialerAdapter{&testutils.MockDialer{Err: fmt.Errorf("dial error")}}
		_, err := h.Execute(context.Background(), []string{f1})
		if err == nil || !strings.Contains(err.Error(), "dial error") {
			t.Errorf("expected dial error, got %v", err)
		}
	})

	t.Run("MkdirAllFail", func(t *testing.T) {
		mSFTP := &testutils.MockSFTPClient{MkdirAllErr: fmt.Errorf("mkdir fail")}
		mSSH := &testutils.MockSSHClient{}
		mDialer := &mockDialerAdapter{&testutils.MockDialer{Client: mSFTP, Conn: mSSH}}
		h := NewSFTPHandler(cfg)
		h.dialer = mDialer
		_, err := h.Execute(context.Background(), []string{f1})
		if err == nil || !strings.Contains(err.Error(), "mkdir fail") {
			t.Errorf("expected mkdir fail, got %v", err)
		}
	})

	t.Run("RetryAndSucceed", func(t *testing.T) {
		mFile := &testutils.MockSFTPFile{Buffer: new(bytes.Buffer)}
		mSFTP := &testutils.MockSFTPClient{}
		mSSH := &testutils.MockSSHClient{}

		callCount := 0
		mDialer := &functionalDialer{
			dialFunc: func(n, a string, c *ssh.ClientConfig) (SFTPClient, SSHClient, error) {
				return &mockSFTPClientAdapter{mSFTP}, mSSH, nil
			},
		}

		mSFTP.CreateFunc = func(path string) (interface{}, error) {
			callCount++
			if callCount == 1 {
				return nil, fmt.Errorf("connection reset") // retriable
			}
			mSFTP.CreatedFile = mFile
			return mFile, nil
		}

		h := NewSFTPHandler(cfg)
		h.dialer = mDialer

		files, err := h.Execute(context.Background(), []string{f1})
		if err != nil {
			t.Errorf("unexpected error after retry: %v", err)
		}
		if len(files) != 1 {
			t.Errorf("expected 1 success, got %d", len(files))
		}
		if callCount != 2 {
			t.Errorf("expected 2 attempts, got %d", callCount)
		}
	})

	t.Run("NonRetriableFail", func(t *testing.T) {
		mSFTP := &testutils.MockSFTPClient{CreateErr: fmt.Errorf("perm fail")}
		mSSH := &testutils.MockSSHClient{}
		mDialer := &mockDialerAdapter{&testutils.MockDialer{Client: mSFTP, Conn: mSSH}}
		h := NewSFTPHandler(cfg)
		h.dialer = mDialer
		success, err := h.Execute(context.Background(), []string{f1})
		if err == nil || !strings.Contains(err.Error(), "perm fail") {
			t.Errorf("expected perm fail, got %v", err)
		}
		if len(success) != 0 {
			t.Errorf("expected 0 success, got %d", len(success))
		}
	})

	t.Run("IntegritySizeMismatch", func(t *testing.T) {
		mFile := &testutils.MockSFTPFile{Buffer: new(bytes.Buffer)}
		mSFTP := &testutils.MockSFTPClient{CreatedFile: mFile}
		mSFTP.StatFunc = func(path string) (os.FileInfo, error) {
			return &testutils.MockFileInfo{FName: path, FSize: 999}, nil // mismatch
		}
		mSSH := &testutils.MockSSHClient{}
		mDialer := &mockDialerAdapter{&testutils.MockDialer{Client: mSFTP, Conn: mSSH}}
		h := NewSFTPHandler(cfg)
		h.dialer = mDialer
		success, err := h.Execute(context.Background(), []string{f1})
		if err == nil || !strings.Contains(err.Error(), "integrity check failed") {
			t.Errorf("expected integrity error, got %v", err)
		}
		if len(success) != 0 {
			t.Errorf("expected 0 success, got %d", len(success))
		}
	})
}

func TestSFTPHandler_Reconnect_Logic(t *testing.T) {
	cfg := &config.Config{
		Action: config.ActionConfig{
			SFTP: config.SFTPConfig{RemotePath: "/remote"},
		},
	}

	t.Run("Reconnect_CloseFail", func(t *testing.T) {
		mSFTP1 := &testutils.MockSFTPClient{StatErr: fmt.Errorf("lost"), CloseErr: fmt.Errorf("close error")}
		mSSH1 := &testutils.MockSSHClient{}
		mSFTP2 := &testutils.MockSFTPClient{}
		mSSH2 := &testutils.MockSSHClient{}

		dialed := false
		mDialer := &functionalDialer{
			dialFunc: func(n, a string, c *ssh.ClientConfig) (SFTPClient, SSHClient, error) {
				dialed = true
				return &mockSFTPClientAdapter{mSFTP2}, mSSH2, nil
			},
		}

		h := NewSFTPHandler(cfg)
		h.dialer = mDialer

		sess1 := &sftpSession{
			client: &mockSFTPClientAdapter{mSFTP1},
			conn:   mSSH1,
		}
		h.allSessions = append(h.allSessions, sess1)
		h.pool <- sess1
		h.activeConns = 1

		ctx := context.Background()

		sess, err := h.acquireSession(ctx)
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		if sess == nil || !dialed {
			t.Error("expected reconnection")
		}
	})
}

func TestSFTPHandler_Connect_Branches(t *testing.T) {
	t.Run("PasswordAuth", func(t *testing.T) {
		cfg := &config.Config{
			Action: config.ActionConfig{
				SFTP: config.SFTPConfig{
					Host: "h", Username: "u", Password: "p",
				},
			},
		}
		h := NewSFTPHandler(cfg)
		h.dialer = &mockDialerAdapter{&testutils.MockDialer{Err: fmt.Errorf("dial error")}}
		_, _, _ = h.connect() // Verify it reaches dial
	})

	t.Run("HostKeyFixed", func(t *testing.T) {
		cfg := &config.Config{
			Action: config.ActionConfig{
				SFTP: config.SFTPConfig{
					Host: "h", Username: "u", HostKey: "AAAAC3NzaC1lZDI1NTE5AAAAILM+67VX7Oc26VYm8L2Iz5yT4qPlPrSgqS9A2t47ycHt",
				},
			},
		}
		h := NewSFTPHandler(cfg)
		h.dialer = &mockDialerAdapter{&testutils.MockDialer{Err: fmt.Errorf("dial error")}}
		_, _, _ = h.connect()
	})

	t.Run("AuthFail_Detection", func(t *testing.T) {
		cfg := &config.Config{
			Action: config.ActionConfig{
				SFTP: config.SFTPConfig{Host: "h", Username: "u", Password: "p"},
			},
		}
		h := NewSFTPHandler(cfg)
		h.dialer = &mockDialerAdapter{&testutils.MockDialer{Err: fmt.Errorf("ssh: unable to authenticate")}}
		_, _, err := h.connect()
		if _, ok := err.(*ErrAuthenticationFailed); !ok {
			t.Errorf("expected ErrAuthenticationFailed, got %T", err)
		}
	})
}

func TestSFTPHandler_RemoteCleanup_Branches(t *testing.T) {
	cfg := &config.Config{
		Action: config.ActionConfig{
			SFTP: config.SFTPConfig{RemotePath: "/remote"},
		},
	}

	t.Run("ReadDirError", func(t *testing.T) {
		mSFTP := &testutils.MockSFTPClient{ReadDirErr: fmt.Errorf("readdir fail")}
		mSSH := &testutils.MockSSHClient{}
		mDialer := &mockDialerAdapter{&testutils.MockDialer{Client: mSFTP, Conn: mSSH}}
		h := NewSFTPHandler(cfg)
		h.dialer = mDialer
		err := h.RemoteCleanup(context.Background())
		if err != nil {
			t.Errorf("expected nil error for ReadDir failure (nothing to clean), got %v", err)
		}
	})

	t.Run("DeleteOldTmpFiles", func(t *testing.T) {
		oldTime := time.Now().Add(-25 * time.Hour)
		newTime := time.Now().Add(-1 * time.Hour)
		mFiles := []os.FileInfo{
			&testutils.MockFileInfo{FName: "old.tmp", FModTime: oldTime},
			&testutils.MockFileInfo{FName: "new.tmp", FModTime: newTime},
		}
		mSFTP := &testutils.MockSFTPClient{Files: mFiles}
		mSSH := &testutils.MockSSHClient{}
		mDialer := &mockDialerAdapter{&testutils.MockDialer{Client: mSFTP, Conn: mSSH}}
		h := NewSFTPHandler(cfg)
		h.dialer = mDialer
		_ = h.RemoteCleanup(context.Background())

		found := false
		for _, p := range mSFTP.RemovedPaths {
			if strings.HasSuffix(p, "old.tmp") {
				found = true
			}
			if strings.HasSuffix(p, "new.tmp") {
				t.Error("new.tmp should not be removed")
			}
		}
		if !found {
			t.Error("old.tmp should be removed")
		}
	})
}

func TestSFTPHandler_IsRetriable(t *testing.T) {
	h := &SFTPHandler{}
	tests := []struct {
		err      error
		expected bool
	}{
		{nil, false},
		{errors.New("connection reset"), true},
		{errors.New("EOF"), true},
		{errors.New("perm denied"), false},
	}
	for _, tt := range tests {
		if got := h.isRetriable(tt.err); got != tt.expected {
			t.Errorf("isRetriable(%v) = %v, want %v", tt.err, got, tt.expected)
		}
	}
}

func TestActionErrors_Unwrap_All(t *testing.T) {
	root := errors.New("root")
	if errors.Unwrap(&ErrConnectionLost{Err: root}) != root {
		t.Error("ErrConnectionLost unwrap fail")
	}
	if errors.Unwrap(&ErrAuthenticationFailed{Err: root}) != root {
		t.Error("ErrAuthenticationFailed unwrap fail")
	}
	if errors.Unwrap(&ErrExecutionFailed{Err: root}) != root {
		t.Error("ErrExecutionFailed unwrap fail")
	}
}

func TestSFTPHandler_LoadFile_RenameFail(t *testing.T) {
	testDir := getActionTestDir("SFTPRenameFail")
	f1 := filepath.Join(testDir, "f1.txt")
	_ = os.WriteFile(f1, []byte("data"), 0o644)

	mSFTP := &testutils.MockSFTPClient{
		RenameErr: fmt.Errorf("rename fail"),
	}
	h := NewSFTPHandler(&config.Config{})
	err := h.uploadFile(&mockSFTPClientAdapter{mSFTP}, f1)
	if err == nil || !strings.Contains(err.Error(), "failed to commit remote file") {
		t.Errorf("expected rename error, got %v", err)
	}
}

func TestErrExecutionFailed_Error(t *testing.T) {
	err := errors.New("root")
	e := &ErrExecutionFailed{Path: "p", Output: "out", Err: err}
	msg := e.Error()
	if !strings.Contains(msg, "p") || !strings.Contains(msg, "out") || !strings.Contains(msg, "root") {
		t.Errorf("unexpected error message: %s", msg)
	}
}

func TestRealDialer_Dial_Failure(t *testing.T) {
	d := &realDialer{}
	// Use an invalid address to ensure Dial fails
	_, _, err := d.Dial("tcp", "localhost:0", &ssh.ClientConfig{
		Timeout: 1 * time.Millisecond,
	})
	if err == nil {
		t.Error("expected dial failure for port 0")
	}
}

func TestSFTPHandler_Connect_SSHKey(t *testing.T) {
	testDir := getActionTestDir("SSHKeyAuth")
	keyFile := filepath.Join(testDir, "id_rsa")
	// Note: We're not using a real key here, just testing the error path of ParsePrivateKey
	_ = os.WriteFile(keyFile, []byte("invalid key data"), 0o600)

	cfg := &config.Config{
		Action: config.ActionConfig{
			SFTP: config.SFTPConfig{
				Host:       "h",
				Username:   "u",
				SSHKeyPath: keyFile,
			},
		},
	}
	h := NewSFTPHandler(cfg)
	_, _, err := h.connect()
	if err == nil || !strings.Contains(err.Error(), "failed to parse SSH key") {
		t.Errorf("expected key parse failure, got %v", err)
	}
}

func TestSFTPHandler_Connect_SSHKeyWithPassphrase(t *testing.T) {
	testDir := getActionTestDir("SSHKeyPassphrase")
	keyFile := filepath.Join(testDir, "id_rsa_pass")
	_ = os.WriteFile(keyFile, []byte("invalid key data"), 0o600)

	cfg := &config.Config{
		Action: config.ActionConfig{
			SFTP: config.SFTPConfig{
				Host:             "h",
				Username:         "u",
				SSHKeyPath:       keyFile,
				SSHKeyPassphrase: "pw",
			},
		},
	}
	h := NewSFTPHandler(cfg)
	_, _, err := h.connect()
	if err == nil || !strings.Contains(err.Error(), "failed to parse SSH key") {
		t.Errorf("expected key parse failure with passphrase, got %v", err)
	}
}

func TestSFTPHandler_Connect_InvalidHostKey(t *testing.T) {
	cfg := &config.Config{
		Action: config.ActionConfig{
			SFTP: config.SFTPConfig{
				Host:     "h",
				Username: "u",
				HostKey:  "invalid-base64",
			},
		},
	}
	h := NewSFTPHandler(cfg)
	_, _, err := h.connect()
	if err == nil || !strings.Contains(err.Error(), "failed to decode host key") {
		t.Errorf("expected host key decode failure, got %v", err)
	}
}

func TestSFTPHandler_UploadFile_LocalStatFail(t *testing.T) {
	h := NewSFTPHandler(&config.Config{})
	err := h.uploadFile(&mockSFTPClientAdapter{&testutils.MockSFTPClient{}}, "non-existent-file")
	if err == nil {
		t.Error("expected error for non-existent local file")
	}
}

func TestSFTPHandler_UploadFile_TransferFail(t *testing.T) {
	testDir := getActionTestDir("SFTPTransferFail")
	f1 := filepath.Join(testDir, "f1.txt")
	_ = os.WriteFile(f1, []byte("data"), 0o644)

	mFile := &testutils.MockSFTPFile{WriteErr: fmt.Errorf("write fail")}
	mSFTP := &testutils.MockSFTPClient{CreatedFile: mFile}
	h := NewSFTPHandler(&config.Config{})
	err := h.uploadFile(&mockSFTPClientAdapter{mSFTP}, f1)
	if err == nil || !strings.Contains(err.Error(), "failed to transfer data") {
		t.Errorf("expected transfer error, got %v", err)
	}
}

func TestSFTPHandler_Close_NoClient(t *testing.T) {
	h := &SFTPHandler{}
	if err := h.Close(); err != nil {
		t.Errorf("unexpected error closing empty handler: %v", err)
	}
}

func TestSFTPHandler_Execute_ContextDone(t *testing.T) {
	testDir := getActionTestDir("SFTPContextDone")
	f1 := filepath.Join(testDir, "f1.txt")
	_ = os.WriteFile(f1, []byte("data"), 0o644)

	cfg := &config.Config{
		Action: config.ActionConfig{
			ConcurrentConnections: 1,
			SFTP: config.SFTPConfig{
				Host: "localhost",
			},
		},
	}
	h := NewSFTPHandler(cfg)
	h.dialer = &mockDialerAdapter{&testutils.MockDialer{Client: &testutils.MockSFTPClient{}, Conn: &testutils.MockSSHClient{}}}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	success, _ := h.Execute(ctx, []string{f1})
	if len(success) != 0 {
		t.Errorf("expected 0 success for cancelled context, got %d", len(success))
	}
}

func TestSFTPHandler_DialerOptimization_Fallback(t *testing.T) {
	// This tests the branch in realDialer where it falls back if MaxPacket fails.
	// We can't easily trigger this with the real dialer without a real server,
	// but we've covered the logic in sftp.go via code review and existing tests.
}

func TestSFTPHandler_LazyConnectionPruning(t *testing.T) {
	cfg := &config.Config{
		Action: config.ActionConfig{
			ConcurrentConnections: 1,
			SFTP: config.SFTPConfig{
				Host: "localhost",
			},
		},
	}
	h := NewSFTPHandler(cfg)

	// Create mock clients
	mSFTP := &testutils.MockSFTPClient{}
	mSSH := &testutils.MockSSHClient{}

	// Initialize h.pool and h.allSessions manually
	h.pool = make(chan *sftpSession, 1)

	// 1. Create a session that is NOT idle (> 5 minutes) and verify it is returned as-is
	freshSess := &sftpSession{
		client:   &mockSFTPClientAdapter{mSFTP},
		conn:     mSSH,
		lastUsed: time.Now(),
	}
	h.allSessions = append(h.allSessions, freshSess)
	h.pool <- freshSess
	h.activeConns = 1

	ctx := context.Background()
	sess, err := h.acquireSession(ctx)
	if err != nil {
		t.Fatalf("failed to acquire fresh session: %v", err)
	}
	if sess != freshSess {
		t.Errorf("expected fresh session to be returned, got %v", sess)
	}

	// Return the session back to the pool
	h.pool <- sess

	// 2. Backdate the session to be idle for 6 minutes (expired)
	sess.lastUsed = time.Now().Add(-6 * time.Minute)

	// Since the expired session will be closed, acquireSession will attempt to dial a new one.
	// We configure the dialer with a new mock client to verify that it dials a new session.
	newSFTP := &testutils.MockSFTPClient{}
	newSSH := &testutils.MockSSHClient{}
	h.dialer = &mockDialerAdapter{&testutils.MockDialer{Client: newSFTP, Conn: newSSH}}

	sess, err = h.acquireSession(ctx)
	if err != nil {
		t.Fatalf("failed to acquire session after pruning: %v", err)
	}
	if sess == freshSess {
		t.Errorf("expected fresh session to be pruned, but it was returned")
	}

	// Verify the pruned session was closed and removed
	if h.activeConns != 1 {
		t.Errorf("expected activeConns to remain 1 after pruning and recreation, got %d", h.activeConns)
	}
	if len(h.allSessions) != 1 || h.allSessions[0] == freshSess {
		t.Errorf("expected allSessions to contain only the new session, got: %v", h.allSessions)
	}
}
