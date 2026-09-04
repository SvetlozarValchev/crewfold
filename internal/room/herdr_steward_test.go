package room

import (
	"reflect"
	"slices"
	"testing"
)

func TestStewardCodexArgumentsResumeInitializedThread(t *testing.T) {
	fresh := stewardCodexArguments(HostedSteward{ManagedWorkingDirectory: true})
	if slices.Contains(fresh, "resume") || slices.Contains(fresh, "--last") {
		t.Fatalf("fresh steward unexpectedly resumes: %#v", fresh)
	}
	if !slices.Contains(fresh, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("managed steward lost its sandbox argument: %#v", fresh)
	}

	resuming := stewardCodexArguments(HostedSteward{ManagedWorkingDirectory: true, InitializedAt: "2026-09-04T00:00:00Z"})
	if len(resuming) < 2 || resuming[len(resuming)-2] != "resume" || resuming[len(resuming)-1] != "--last" {
		t.Fatalf("initialized steward does not resume its prior thread: %#v", resuming)
	}
}

func TestStewardAgentStartArguments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		steward HostedSteward
		want    []string
	}{
		{
			name:    "codex existing directory",
			steward: HostedSteward{AgentName: "steward", AgentKind: "codex"},
			want:    []string{"agent", "start", "steward", "--kind", "codex", "--pane", "w1:p1", "--timeout", "60000", "--", "--no-alt-screen", "-c", "shell_environment_policy.inherit=all", "-c", "check_for_update_on_startup=false"},
		},
		{
			name:    "codex managed directory",
			steward: HostedSteward{AgentName: "steward", AgentKind: "codex", ManagedWorkingDirectory: true},
			want:    []string{"agent", "start", "steward", "--kind", "codex", "--pane", "w1:p1", "--timeout", "60000", "--", "--no-alt-screen", "-c", "shell_environment_policy.inherit=all", "-c", "check_for_update_on_startup=false", "--dangerously-bypass-approvals-and-sandbox", "--dangerously-bypass-hook-trust"},
		},
		{
			name:    "pi existing directory",
			steward: HostedSteward{AgentName: "steward", AgentKind: "pi"},
			want:    []string{"agent", "start", "steward", "--kind", "pi", "--pane", "w1:p1", "--timeout", "60000"},
		},
		{
			name:    "pi managed directory",
			steward: HostedSteward{AgentName: "steward", AgentKind: "pi", ManagedWorkingDirectory: true},
			want:    []string{"agent", "start", "steward", "--kind", "pi", "--pane", "w1:p1", "--timeout", "60000", "--", "--approve"},
		},
		{
			name:    "pi initialized session",
			steward: HostedSteward{AgentName: "steward", AgentKind: "pi", ManagedWorkingDirectory: true, InitializedAt: "2026-09-04T00:00:00Z"},
			want:    []string{"agent", "start", "steward", "--kind", "pi", "--pane", "w1:p1", "--timeout", "60000", "--", "--approve", "--continue"},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := stewardAgentStartArguments(test.steward, "w1:p1")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("arguments = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestHasPendingCodexPasteUsesOnlyRecentTerminal(t *testing.T) {
	t.Parallel()
	if !hasPendingCodexPaste("ready\n› task [Pasted Content 1200 chars]") {
		t.Fatal("pending paste was not detected")
	}
	if hasPendingCodexPaste("[Pasted Content 1200 chars]" + string(make([]byte, 5000))) {
		t.Fatal("stale paste marker was detected")
	}
}

func TestStewardAgentStartArgumentsRejectsUnknownRuntime(t *testing.T) {
	t.Parallel()
	if _, err := stewardAgentStartArguments(HostedSteward{AgentName: "steward", AgentKind: "unknown"}, "w1:p1"); err == nil {
		t.Fatal("expected unsupported runtime error")
	}
}
