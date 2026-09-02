//go:build windows

package room

import "testing"

func TestHostedAgentExecutableUsesPowerShellSyntax(t *testing.T) {
	t.Parallel()
	path := `C:\Users\Owner's Workspace\crewfold.exe`
	if got, want := hostedAgentExecutable(path), `& 'C:\Users\Owner''s Workspace\crewfold.exe'`; got != want {
		t.Fatalf("executable = %q, want %q", got, want)
	}
}
