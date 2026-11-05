package claudequeue

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveSessionAndDeliverThroughRestrictedRelay(t *testing.T) {
	directory := t.TempDir()
	logPath := filepath.Join(directory, "relay.log")
	executable := filepath.Join(directory, "claude")
	script := `#!/bin/sh
if [ "$1" = "agents" ] && [ "$2" = "--json" ]; then
  printf '%s\n' '[{"pid":12,"cwd":"` + directory + `","kind":"interactive","sessionId":"session-12","name":"worker-12","status":"idle"}]'
  exit 0
fi
printf '%s\n' "$*" > "$CLAUDE_RELAY_LOG"
cat >> "$CLAUDE_RELAY_LOG"
printf '%s\n' CREWFOLD_DELIVERED
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_RELAY_LOG", logPath)
	client := Client{Executable: executable}
	session, err := client.ResolveSession(context.Background(), directory, "")
	if err != nil || session.SessionID != "session-12" || session.Name != "worker-12" {
		t.Fatalf("session = %#v, %v", session, err)
	}
	if err := client.Deliver(context.Background(), session.SessionID, "[CREWFOLD] receive batch"); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(content)
	for _, expected := range []string{"--no-session-persistence", "--allowedTools ToolSearch,SendMessage", `to="worker-12"`, "[CREWFOLD] receive batch"} {
		if !strings.Contains(log, expected) {
			t.Fatalf("relay log omitted %q: %s", expected, log)
		}
	}
}

func TestResolveSessionRequiresExplicitChoiceForSharedDirectory(t *testing.T) {
	directory := t.TempDir()
	executable := filepath.Join(directory, "claude")
	script := `#!/bin/sh
printf '%s\n' '[{"cwd":"` + directory + `","kind":"interactive","sessionId":"one","name":"one"},{"cwd":"` + directory + `","kind":"interactive","sessionId":"two","name":"two"}]'
`
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	client := Client{Executable: executable}
	if _, err := client.ResolveSession(context.Background(), directory, ""); err == nil || !strings.Contains(err.Error(), "multiple active Claude sessions") {
		t.Fatalf("ambiguous session error = %v", err)
	}
	selected, err := client.ResolveSession(context.Background(), directory, "two")
	if err != nil || selected.Name != "two" {
		t.Fatalf("explicit session = %#v, %v", selected, err)
	}
}

func TestDeliverRejectsMissingSessionAndUnconfirmedRelay(t *testing.T) {
	for _, test := range []struct {
		name     string
		sessions string
		response string
		want     string
	}{
		{"closed", "[]", "CREWFOLD_DELIVERED", "not currently running"},
		{"unconfirmed", `[{"sessionId":"one","name":"worker"}]`, "Could not send", "did not confirm"},
	} {
		t.Run(test.name, func(t *testing.T) {
			executable := filepath.Join(t.TempDir(), "claude")
			script := "#!/bin/sh\nif [ \"$1\" = agents ]; then\n printf '%s\\n' '" + test.sessions + "'\nelse\n printf '%s\\n' '" + test.response + "'\nfi\n"
			if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			err := (Client{Executable: executable}).Deliver(context.Background(), "one", "[CREWFOLD] receive batch")
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("delivery error = %v, want %q", err, test.want)
			}
		})
	}
}
