//go:build !windows

// Package localipc provides owner-local daemon transport.
package localipc

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"time"
)

// Endpoint returns the Unix-domain socket beneath runtimeDir.
func Endpoint(runtimeDir string) string {
	return filepath.Join(runtimeDir, "crewfold.sock")
}

// Normalize resolves an endpoint to an absolute Unix-domain socket path.
func Normalize(endpoint string) (string, error) {
	return filepath.Abs(endpoint)
}

// Listen creates an owner-only Unix-domain socket listener.
func Listen(endpoint string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(endpoint), 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(endpoint), 0o700); err != nil {
		return nil, err
	}
	if err := prepare(endpoint); err != nil {
		return nil, err
	}
	listener, err := net.Listen("unix", endpoint)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(endpoint, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(endpoint)
		return nil, err
	}
	return listener, nil
}

// DialContext connects to an owner-local Unix-domain socket.
func DialContext(ctx context.Context, endpoint string) (net.Conn, error) {
	dialer := net.Dialer{Timeout: 2 * time.Second}
	return dialer.DialContext(ctx, "unix", endpoint)
}

// Remove cleans up a Unix-domain socket path.
func Remove(endpoint string) error {
	err := os.Remove(endpoint)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func prepare(endpoint string) error {
	info, err := os.Lstat(endpoint)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return errors.New("socket path is occupied by a non-socket file")
	}
	connection, dialErr := net.DialTimeout("unix", endpoint, 200*time.Millisecond)
	if dialErr == nil {
		_ = connection.Close()
		return errors.New("Crewfold daemon is already running")
	}
	return os.Remove(endpoint)
}
