package daemon

import (
	"context"
	"strings"
	"testing"

	"crewfold/internal/execution"
)

type unavailableCodexSandboxRunner struct {
	calls []string
}

func (runner *unavailableCodexSandboxRunner) Run(_ context.Context, _ string, arguments []string, _ map[string]string) (execution.ProviderCommandResult, error) {
	call := strings.Join(arguments, " ")
	runner.calls = append(runner.calls, call)
	switch call {
	case "--version":
		return execution.ProviderCommandResult{Stdout: []byte("codex-cli 1.2.3\n")}, nil
	case "exec --help":
		return execution.ProviderCommandResult{Stdout: []byte("--config --ephemeral --ignore-user-config --json --sandbox\n")}, nil
	case "sandbox -- /bin/sh -c exit 0":
		return execution.ProviderCommandResult{Stderr: []byte("bwrap: loopback: Failed RTM_NEWADDR: Operation not permitted\n"), ExitCode: 1}, nil
	default:
		return execution.ProviderCommandResult{}, nil
	}
}

func TestM21WorkbenchProviderDiagnosisRejectsBrokenCodexSandboxBeforeAuthentication(t *testing.T) {
	t.Parallel()

	runner := &unavailableCodexSandboxRunner{}
	provider := execution.NewCodexProvider(execution.CodexProviderOptions{CodexExecutable: "/opt/codex", CodexHome: "/private/codex", ProbeRunner: runner})
	_, err := diagnoseWorkbenchProvider(context.Background(), provider)
	if err == nil || !strings.Contains(err.Error(), "workspace sandbox is unavailable") || !strings.Contains(err.Error(), "RTM_NEWADDR") {
		t.Fatalf("diagnoseWorkbenchProvider() error = %v", err)
	}
	for _, call := range runner.calls {
		if call == "login status" {
			t.Fatalf("authentication was probed after a deterministic sandbox failure: %#v", runner.calls)
		}
	}
}
