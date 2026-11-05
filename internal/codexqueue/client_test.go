package codexqueue

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClientValidatesAndQueuesThroughCodexCLI(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "arguments.log")
	executable := filepath.Join(root, "codex")
	script := `#!/bin/sh
if [ "$1" = "queue" ] && [ "$2" = "--help" ]; then
  exit 0
fi
printf '%s\n' "$@" > "$CREWFOLD_CODEX_QUEUE_TEST_LOG"
printf 'Queued message test for thread %s.\n' "$3"
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREWFOLD_CODEX_QUEUE_TEST_LOG", logPath)
	client := Client{Executable: executable}
	resolved, err := client.ResolveExecutable()
	if err != nil || resolved != executable {
		t.Fatalf("resolved executable = %q, %v", resolved, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Validate(ctx, "thread-test"); err != nil {
		t.Fatal(err)
	}
	if err := client.Deliver(ctx, "thread-test", "room message #7"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "queue\n--thread\nthread-test\n--message\nroom message #7\n"
	if string(content) != want {
		t.Fatalf("arguments = %q, want %q", content, want)
	}
}

func TestRealCodexQueueDelivery(t *testing.T) {
	threadID := strings.TrimSpace(os.Getenv("CREWFOLD_REAL_CODEX_THREAD_ID"))
	if threadID == "" {
		t.Skip("set CREWFOLD_REAL_CODEX_THREAD_ID to a disposable open Codex session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := Client{}
	if err := client.Validate(ctx, threadID); err != nil {
		t.Fatal(err)
	}
	if err := client.Deliver(ctx, threadID, "[CREWFOLD DELIVERY TEST]\nReply exactly: delivery accepted. Do not use tools."); err != nil {
		t.Fatal(err)
	}
}

func TestClientReportsQueueFailure(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "codex")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf 'session is unavailable\\n' >&2\nexit 7\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	err := (Client{Executable: executable}).Deliver(context.Background(), "thread-test", "message")
	if err == nil || !strings.Contains(err.Error(), "session is unavailable") {
		t.Fatalf("delivery error = %v", err)
	}
}
