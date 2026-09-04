//go:build !windows

package localipc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListenRejectsOccupiedNonSocketPath(t *testing.T) {
	endpoint := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(endpoint, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Listen(endpoint)
	if err == nil || !strings.Contains(err.Error(), "non-socket") {
		t.Fatalf("Listen() error = %v", err)
	}
}
