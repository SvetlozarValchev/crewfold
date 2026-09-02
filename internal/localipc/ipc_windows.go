//go:build windows

// Package localipc provides owner-local daemon transport.
package localipc

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

const pipePrefix = `\\.\pipe\crewfold-`

// Endpoint returns a stable named-pipe path derived from runtimeDir. The hash
// avoids collisions between owners without exposing the owner path in the pipe
// namespace.
func Endpoint(runtimeDir string) string {
	identity := sha256.Sum256([]byte(strings.ToLower(runtimeDir)))
	return fmt.Sprintf(`%s%x`, pipePrefix, identity[:8])
}

// Normalize validates a Windows named-pipe endpoint.
func Normalize(endpoint string) (string, error) {
	if !strings.HasPrefix(strings.ToLower(endpoint), strings.ToLower(pipePrefix)) {
		return "", errors.New(`Crewfold endpoint must be a \\.\pipe\crewfold-* named pipe`)
	}
	return endpoint, nil
}

// Listen creates a byte-stream named pipe restricted to the current owner.
func Listen(endpoint string) (net.Listener, error) {
	if _, err := Normalize(endpoint); err != nil {
		return nil, err
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, fmt.Errorf("resolve current Windows user: %w", err)
	}
	securityDescriptor := "D:P(A;;GA;;;" + user.User.Sid.String() + ")"
	listener, err := winio.ListenPipe(endpoint, &winio.PipeConfig{
		SecurityDescriptor: securityDescriptor,
		InputBufferSize:    64 * 1024,
		OutputBufferSize:   64 * 1024,
	})
	if err != nil {
		return nil, fmt.Errorf("listen on owner pipe: %w", err)
	}
	return listener, nil
}

// DialContext connects to an owner-local named pipe.
func DialContext(ctx context.Context, endpoint string) (net.Conn, error) {
	return winio.DialPipeContext(ctx, endpoint)
}

// Remove is a no-op because Windows removes a named pipe when its listener
// closes.
func Remove(string) error { return nil }
