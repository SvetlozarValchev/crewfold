package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// This opt-in test crosses the installed Herdr/Codex boundary with the exact
// production durable-agent instructions and dynamic-tool catalog. The lower
// execution-package transport test intentionally cannot validate that schema.
func TestLiveHerdrStartsTheProductionDurableAgentThread(t *testing.T) {
	if os.Getenv("CREWFOLD_TEST_LIVE_HERDR") != "1" {
		t.Skip("set CREWFOLD_TEST_LIVE_HERDR=1 for the installed Herdr/Codex integration")
	}
	if _, err := exec.LookPath("herdr"); err != nil {
		t.Skipf("Herdr is unavailable: %v", err)
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skipf("Codex is unavailable: %v", err)
	}
	root := t.TempDir()
	hostRoot, err := os.MkdirTemp("/tmp", "cf-live-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(hostRoot) })
	checkout := filepath.Join(root, "checkout")
	if err := os.Mkdir(checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	host := newDomainSessionHost(Config{DataDir: root, SocketPath: filepath.Join(hostRoot, "s")}, nil)
	t.Cleanup(func() { _ = host.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	thread, err := host.startThread(ctx, checkout, "Crewfold live durable agent", "You own one bounded live integration fixture.")
	if err != nil {
		t.Fatal(err)
	}
	if thread.ID == "" {
		t.Fatal("production durable-agent thread has no identity")
	}
	if err := host.deleteThread(ctx, thread.ID); err != nil {
		t.Fatal(err)
	}
}
