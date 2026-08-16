package daemon

import (
	"os"
	"testing"

	"crewfold/internal/execution"
)

// The production binary owns this private subprocess entrypoint. Daemon
// integration tests run under the Go test binary, so mirror that dispatch to
// exercise the real MCP bridge/runtime path instead of replacing it with a
// fake provider observation.
func TestMain(testingMain *testing.M) {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "__direct-supervisor":
			os.Exit(execution.RunDirectSupervisor(os.Args[2:]))
		case "__fixture-mcp-provider":
			os.Exit(execution.RunFixtureMCPProvider(os.Stdin, os.Stdout, os.Stderr))
		}
	}
	os.Exit(testingMain.Run())
}
