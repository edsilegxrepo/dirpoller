// Package action_test provides integration tests for the SFTP action handler using a real mock SSH/SFTP server.
//
// Objective:
// Validate the SFTPHandler's ability to interact with a functional SSH/SFTP
// server, covering all supported authentication methods and security
// features like host key verification.
//
// Scenarios Covered:
// - Authentication: Password, RSA Key, and Multi-Factor Authentication (MFA).
// - Failure Paths: Incorrect passwords, unauthorized keys, and MFA partial failures.
// - Security: Strict host key verification and rejection of mismatched keys.
// - Connectivity: Proper handling of network addresses and ports.
package action

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"criticalsys.net/dirpoller/internal/config"
	"criticalsys.net/dirpoller/internal/testutils"
	"golang.org/x/crypto/ssh"
)

// TestSFTPHandler_RealMockServer coordinates integration tests against a mock SSH/SFTP server.
//
// Scenario:
// 1. PasswordAuth: Connect using standard username/password.
// 2. KeyAuth: Connect using a generated RSA private key.
// 3. MFA_Auth: Connect using both an RSA key and a password.
// 4. HostKeyVerification: Verify connection success with correct key and failure with incorrect key.
//
// Success Criteria:
// - All valid authentication attempts result in successful file transfers.
// - All invalid authentication or security checks result in appropriate errors.
// - Temporary test artifacts (keys, directories) are cleaned up.
func TestSFTPHandler_RealMockServer(t *testing.T) {
	// Create a temporary directory for local files
	tempDir := testutils.GetUniqueTestDir("action", "sftp_integration")
	localFile := filepath.Join(tempDir, "test.txt")
	content := []byte("hello sftp")
	err := os.WriteFile(localFile, content, 0o644)
	if err != nil {
		t.Fatalf("failed to write local file: %v", err)
	}

	t.Run("PasswordAuth", func(t *testing.T) {
		server := NewMockServer(t, map[string]string{"password": "testpass"})
		defer server.Close()

		addr := server.Addr
		host, portStr, _ := splitAddr(addr)

		cfg := &config.Config{
			Action: config.ActionConfig{
				SFTP: config.SFTPConfig{
					Host:     host,
					Port:     mustAtoi(portStr),
					Username: "testuser",
					Password: "testpass",
				},
			},
		}

		h := NewSFTPHandler(cfg)
		defer func() { _ = h.Close() }()

		success, err := h.Execute(context.Background(), []string{localFile})
		if err != nil {
			t.Fatalf("Execute failed: %v", err)
		}
		if len(success) != 1 || success[0] != localFile {
			t.Errorf("expected 1 success file, got %v", success)
		}
	})

	t.Run("KeyAuth", func(t *testing.T) {
		// Generate a client key
		clientKey, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatalf("failed to generate client key: %v", err)
		}
		clientKeyPath := filepath.Join(tempDir, "client_id_rsa")
		keyPEM := pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(clientKey),
		})
		_ = os.WriteFile(clientKeyPath, keyPEM, 0o600)

		server := NewMockServer(t, nil)
		defer server.Close()

		addr := server.Addr
		host, portStr, _ := splitAddr(addr)

		cfg := &config.Config{
			Action: config.ActionConfig{
				SFTP: config.SFTPConfig{
					Host:       host,
					Port:       mustAtoi(portStr),
					Username:   "keyuser",
					SSHKeyPath: clientKeyPath,
				},
			},
		}

		h := NewSFTPHandler(cfg)
		defer func() { _ = h.Close() }()

		success, err := h.Execute(context.Background(), []string{localFile})
		if err != nil {
			t.Fatalf("Execute failed: %v", err)
		}
		if len(success) != 1 {
			t.Errorf("expected 1 success file, got %v", success)
		}
	})

	t.Run("MFA_Auth", func(t *testing.T) {
		// Generate a client key for MFA
		clientKey, _ := rsa.GenerateKey(rand.Reader, 2048)
		clientKeyPath := filepath.Join(tempDir, "mfa_id_rsa")
		keyPEM := pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(clientKey),
		})
		_ = os.WriteFile(clientKeyPath, keyPEM, 0o600)

		server := NewMockServer(t, nil)
		defer server.Close()

		addr := server.Addr
		host, portStr, _ := splitAddr(addr)

		cfg := &config.Config{
			Action: config.ActionConfig{
				SFTP: config.SFTPConfig{
					Host:       host,
					Port:       mustAtoi(portStr),
					Username:   "mfauser",
					Password:   "mfapass",
					SSHKeyPath: clientKeyPath,
				},
			},
		}

		h := NewSFTPHandler(cfg)
		defer func() { _ = h.Close() }()

		success, err := h.Execute(context.Background(), []string{localFile})
		if err != nil {
			t.Fatalf("Execute failed: %v", err)
		}
		if len(success) != 1 {
			t.Errorf("expected 1 success file, got %v", success)
		}
	})
	t.Run("PasswordAuth_Failure", func(t *testing.T) {
		server := NewMockServer(t, map[string]string{"password": "testpass"})
		defer server.Close()

		cfg := &config.Config{
			Action: config.ActionConfig{
				SFTP: config.SFTPConfig{
					Host:     "127.0.0.1",
					Port:     mustAtoi(strings.Split(server.Addr, ":")[1]),
					Username: "testuser",
					Password: "wrongpassword",
				},
			},
		}

		h := NewSFTPHandler(cfg)
		defer func() { _ = h.Close() }()

		_, err := h.Execute(context.Background(), []string{localFile})
		if err == nil {
			t.Fatal("expected error with wrong password, got nil")
		}
		if !strings.Contains(err.Error(), "ssh: unable to authenticate") {
			t.Errorf("expected ssh authentication error, got: %v", err)
		}
	})

	t.Run("KeyAuth_Failure", func(t *testing.T) {
		// Generate a DIFFERENT client key that the server won't recognize
		wrongKey, _ := rsa.GenerateKey(rand.Reader, 2048)
		wrongKeyPath := filepath.Join(tempDir, "wrong_id_rsa")
		keyPEM := pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(wrongKey),
		})
		err := os.WriteFile(wrongKeyPath, keyPEM, 0o600)
		if err != nil {
			t.Fatalf("failed to write wrong client key file: %v", err)
		}

		server := NewMockServer(t, nil)
		defer server.Close()

		cfg := &config.Config{
			Action: config.ActionConfig{
				SFTP: config.SFTPConfig{
					Host:       "127.0.0.1",
					Port:       mustAtoi(strings.Split(server.Addr, ":")[1]),
					Username:   "wronguser",
					SSHKeyPath: wrongKeyPath,
				},
			},
		}

		h := NewSFTPHandler(cfg)
		defer func() { _ = h.Close() }()

		_, err = h.Execute(context.Background(), []string{localFile})
		if err == nil {
			t.Fatal("expected error with wrong key, got nil")
		}
	})

	t.Run("MFA_Auth_PartialFailure", func(t *testing.T) {
		clientKey, _ := rsa.GenerateKey(rand.Reader, 2048)
		clientKeyPath := filepath.Join(tempDir, "mfa_fail_id_rsa")
		keyPEM := pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(clientKey),
		})
		_ = os.WriteFile(clientKeyPath, keyPEM, 0o600)

		server := NewMockServer(t, nil)
		defer server.Close()

		cfg := &config.Config{
			Action: config.ActionConfig{
				SFTP: config.SFTPConfig{
					Host:       "127.0.0.1",
					Port:       mustAtoi(strings.Split(server.Addr, ":")[1]),
					Username:   "mfauser",
					Password:   "wrongmfapass",
					SSHKeyPath: clientKeyPath,
				},
			},
		}

		h := NewSFTPHandler(cfg)
		defer func() { _ = h.Close() }()

		_, err := h.Execute(context.Background(), []string{localFile})
		if err == nil {
			t.Fatal("expected error with wrong MFA password, got nil")
		}
	})

	t.Run("HostKeyVerification_Success", func(t *testing.T) {
		server := NewMockServer(t, map[string]string{"password": "testpass"})
		defer server.Close()

		hostKeyBase64 := base64.StdEncoding.EncodeToString(server.HostKey.Marshal())

		cfg := &config.Config{
			Action: config.ActionConfig{
				SFTP: config.SFTPConfig{
					Host:     "127.0.0.1",
					Port:     mustAtoi(strings.Split(server.Addr, ":")[1]),
					Username: "testuser",
					Password: "testpass",
					HostKey:  hostKeyBase64,
				},
			},
		}

		h := NewSFTPHandler(cfg)
		defer func() { _ = h.Close() }()

		success, err := h.Execute(context.Background(), []string{localFile})
		if err != nil {
			t.Fatalf("Execute failed with correct host key: %v", err)
		}
		if len(success) != 1 {
			t.Errorf("expected 1 success file, got %v", success)
		}
	})

	t.Run("HostKeyVerification_Failure", func(t *testing.T) {
		server := NewMockServer(t, map[string]string{"password": "testpass"})
		defer server.Close()

		// Generate a DIFFERENT host key to simulate a mismatch
		wrongKey, _ := rsa.GenerateKey(rand.Reader, 2048)
		wrongSigner, _ := ssh.NewSignerFromKey(wrongKey)
		wrongHostKeyBase64 := base64.StdEncoding.EncodeToString(wrongSigner.PublicKey().Marshal())

		cfg := &config.Config{
			Action: config.ActionConfig{
				SFTP: config.SFTPConfig{
					Host:     "127.0.0.1",
					Port:     mustAtoi(strings.Split(server.Addr, ":")[1]),
					Username: "testuser",
					Password: "testpass",
					HostKey:  wrongHostKeyBase64,
				},
			},
		}

		h := NewSFTPHandler(cfg)
		defer func() { _ = h.Close() }()

		_, err := h.Execute(context.Background(), []string{localFile})
		if err == nil {
			t.Fatal("expected error with mismatched host key, got nil")
		}
		if !strings.Contains(err.Error(), "ssh: handshake failed: ssh: host key mismatch") {
			t.Errorf("expected host key mismatch error, got: %v", err)
		}
	})
}

func splitAddr(addr string) (string, string, error) {
	host, port, err := net.SplitHostPort(addr)
	return host, port, err
}

func mustAtoi(s string) int {
	var i int
	_, _ = fmt.Sscanf(s, "%d", &i)
	return i
}
