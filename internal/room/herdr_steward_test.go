package room

import (
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

func TestHasPendingCodexPasteUsesOnlyRecentTerminal(t *testing.T) {
	t.Parallel()
	if !hasPendingCodexPaste("ready\n› task [Pasted Content 1200 chars]") {
		t.Fatal("pending paste was not detected")
	}
	if hasPendingCodexPaste("[Pasted Content 1200 chars]" + string(make([]byte, 5000))) {
		t.Fatal("stale paste marker was detected")
	}
}
