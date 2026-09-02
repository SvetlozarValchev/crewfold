//go:build windows

package codexapp

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMissingWindowsControlSocketExplainsCodexLimitation(t *testing.T) {
	t.Parallel()
	_, err := (Client{SocketPath: `\\.\pipe\missing-codex-control`, Timeout: 10 * time.Millisecond}).Inspect(context.Background(), "thread-test")
	if !errors.Is(err, ErrDaemonLifecycleUnsupported) {
		t.Fatalf("Inspect() error = %v", err)
	}
}
