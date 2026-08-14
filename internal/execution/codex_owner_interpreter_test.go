package execution

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"crewfold/internal/domain"
)

func TestM21CodexOwnerInterpreterUsesTheNativeSubscriptionCLIDefault(t *testing.T) {
	codexHome := filepath.Join(t.TempDir(), "codex-home")
	t.Setenv("CODEX_HOME", codexHome)

	interpreter := NewCodexOwnerInterpreter(CodexOwnerInterpreterOptions{})
	if interpreter.codexExecutable != "codex" || interpreter.codexHome != codexHome {
		t.Fatalf("default Codex interpreter = executable %q home %q", interpreter.codexExecutable, interpreter.codexHome)
	}
}

func TestM21CodexOwnerInterpreterReplaysCompletedStructuredOutputWithoutRelaunch(t *testing.T) {
	root := t.TempDir()
	checkout := filepath.Join(root, "checkout")
	if err := os.Mkdir(checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	operationID := "run_00000000000000000000000000000021"
	operationRoot := filepath.Join(root, "state", operationID)
	if err := os.MkdirAll(operationRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := publishPrivateFileNoReplace(operationRoot, ownerInterpretationSchemaFile, ownerInterpretationSchema, nil); err != nil {
		t.Fatal(err)
	}
	output := []byte(`{"disposition":"answer","summary":"replayed","answer":"The durable review already completed.","question":"","choices":[],"objective_title":"","objective_budget":{"token_limit":0,"cost_cents":0,"time_seconds":0},"tasks":[],"citation_refs":[]}`)
	if err := publishPrivateFileNoReplace(operationRoot, ownerInterpretationOutputFile, output, nil); err != nil {
		t.Fatal(err)
	}
	runtime := NewFakeRuntime()
	interpreter := NewCodexOwnerInterpreter(CodexOwnerInterpreterOptions{
		Runtime: runtime, StateRoot: filepath.Join(root, "state"), CodexExecutable: "/bin/true",
	})
	result, err := interpreter.Interpret(context.Background(), OwnerInterpretationRequest{
		OperationID: operationID, Kind: "review", Instruction: "Review the exact worker report.", Provider: "codex",
		CheckoutPath: checkout, CanonicalContext: []byte(`{"schema":"test"}`), EventCut: 21,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != "answer" || result.Answer != "The durable review already completed." || runtime.LaunchCount() != 0 || result.ObjectiveBudget != (domain.Budget{}) {
		t.Fatalf("replayed interpretation = %#v, launches = %d", result, runtime.LaunchCount())
	}
}
