//go:build windows

package room

import "testing"

func TestAgentShellExecutableUsesRuntimeNativeSyntax(t *testing.T) {
	t.Parallel()
	path := `C:\Users\Owner's Workspace\crewfold.exe`
	if got, want := agentShellExecutable("pi", path), `'/c/Users/Owner'"'"'s Workspace/crewfold.exe'`; got != want {
		t.Fatalf("Pi executable = %q, want %q", got, want)
	}
	if got, want := agentShellExecutable("codex", path), `& 'C:\Users\Owner''s Workspace\crewfold.exe'`; got != want {
		t.Fatalf("Codex executable = %q, want %q", got, want)
	}
}
