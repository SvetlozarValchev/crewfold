package execution

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestLiveHerdrHostsOneStructuredCodexAppServerEpoch(t *testing.T) {
	if os.Getenv("CREWFOLD_TEST_LIVE_HERDR") != "1" {
		t.Skip("set CREWFOLD_TEST_LIVE_HERDR=1 for the installed Herdr/Codex integration")
	}
	if _, err := exec.LookPath("herdr"); err != nil {
		t.Skipf("Herdr is unavailable: %v", err)
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skipf("Codex is unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	stateRoot, err := os.MkdirTemp(filepath.Join(home, ".local", "state", "crewfold"), ".live-herdr-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(stateRoot) })
	transport, err := StartCodexAppServerInHerdr(ctx, HerdrCodexAppServerOptions{StateRoot: stateRoot})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transport.Close() })
	client, err := NewCodexAppServerClient(transport)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	checkout := filepath.Join(stateRoot, "checkout")
	if err := os.Mkdir(checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	thread, err := client.StartThread(ctx, CodexThreadStartParams{
		CWD: checkout, Ephemeral: false, ApprovalPolicy: "never", Sandbox: CodexSandboxReadOnly,
	})
	if err != nil {
		host := transport.(*herdrCodexAppServerTransport)
		t.Fatalf("%v\nHerdr pane:\n%s", err, host.diagnostic(context.Background()))
	}
	if err := client.SetThreadName(ctx, thread.ID, "Crewfold live Herdr host test"); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteThread(ctx, thread.ID); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
}
