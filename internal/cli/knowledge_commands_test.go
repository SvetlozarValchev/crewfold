package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestKnowledgeProposeReadsStructuredMarkdownAndForwardsTypedSources(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "finding.md")
	if err := os.WriteFile(path, []byte("# Accepted contact ordering contract\n\nSort contacts by stable identifier before emission.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeDaemonClient{knowledgeMutation: localapi.KnowledgeMutationResult{
		Schema: localapi.KnowledgeMutationSchema, Type: "knowledge_mutation", EventSequence: 1,
		Revision: domain.KnowledgeRevision{ID: "krev_00000000000000000000000000000001", ItemID: "know_00000000000000000000000000000001"},
	}}
	app, stdout, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	exitCode := app.Run([]string{
		"knowledge", "propose", "--type", "finding", "--from-task", "task_00000000000000000000000000000001",
		"--supporting-task", "task_00000000000000000000000000000002", path,
		"--workspace", "personal", "--socket", "/tmp/crewfold.sock", "--output", "json",
	})
	if exitCode != ExitOK || stderr.Len() != 0 || stdout.Len() == 0 {
		t.Fatalf("Run() = %d, stdout=%q stderr=%q", exitCode, stdout.String(), stderr.String())
	}
	got := client.knowledgePropose
	if got.Title != "Accepted contact ordering contract" || got.Body != "Sort contacts by stable identifier before emission." || got.Type != domain.KnowledgeTypeFinding || got.Project != "" {
		t.Fatalf("KnowledgePropose params = %#v", got)
	}
	wantSources := []domain.KnowledgeSourceInput{
		{Type: domain.KnowledgeSourceTask, ID: "task_00000000000000000000000000000001", Role: domain.KnowledgeSourcePrimary},
		{Type: domain.KnowledgeSourceTask, ID: "task_00000000000000000000000000000002", Role: domain.KnowledgeSourceSupporting},
	}
	if !reflect.DeepEqual(got.Sources, wantSources) || got.Confidence != domain.KnowledgeConfidenceMedium || got.VerificationStatus != domain.KnowledgeVerificationSupported || got.FreshnessPolicy != domain.KnowledgeFreshUntilSuperseded {
		t.Fatalf("KnowledgePropose metadata = %#v, sources = %#v", got, got.Sources)
	}
}

func TestKnowledgeDecisionAndContextIncludePreserveOptimisticRevisionAndOrder(t *testing.T) {
	t.Parallel()
	client := &fakeDaemonClient{knowledgeMutation: localapi.KnowledgeMutationResult{
		Schema: localapi.KnowledgeMutationSchema, Type: "knowledge_mutation", EventSequence: 1,
		Revision: domain.KnowledgeRevision{ID: "krev_00000000000000000000000000000001", StateRevision: 2},
	}}
	app, _, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	if exitCode := app.Run([]string{
		"knowledge", "accept", "krev_00000000000000000000000000000001", "--expected-state-revision", "1",
		"--workspace", "personal", "--socket", "/tmp/crewfold.sock",
	}); exitCode != ExitOK {
		t.Fatalf("knowledge accept exit = %d, stderr=%q", exitCode, stderr.String())
	}
	if client.knowledgeDecision.ExpectedStateRevision != 1 || client.knowledgeDecision.KnowledgeRevision != "krev_00000000000000000000000000000001" {
		t.Fatalf("knowledge decision params = %#v", client.knowledgeDecision)
	}

	app, _, stderr = newTestApp()
	app.newClient = func(string) daemonClient { return client }
	want := []string{"krev_00000000000000000000000000000002", "krev_00000000000000000000000000000001"}
	if exitCode := app.Run([]string{
		"context", "build", "task_00000000000000000000000000000003", "--workspace", "personal", "--agent", "replacement",
		"--expected-task-revision", "2", "--include", want[0], "--include", want[1], "--socket", "/tmp/crewfold.sock",
	}); exitCode != ExitOK {
		t.Fatalf("context build exit = %d, stderr=%q", exitCode, stderr.String())
	}
	if !reflect.DeepEqual(client.contextBuildParams.KnowledgeRevisionIDs, want) {
		t.Fatalf("context includes = %v, want %v", client.contextBuildParams.KnowledgeRevisionIDs, want)
	}
}

func TestKnowledgeProposeRejectsTranscriptLikeUnstructuredInputBoundary(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "terminal.txt")
	if err := os.WriteFile(path, []byte("$ go test ./...\nPASS\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	app, _, stderr := newTestApp()
	if exitCode := app.Run([]string{
		"knowledge", "propose", "--type", "finding", "--from-task", "task_00000000000000000000000000000001",
		path, "--workspace", "personal", "--socket", "/tmp/crewfold.sock",
	}); exitCode != ExitUsage || stderr.Len() == 0 {
		t.Fatalf("Run() = %d stderr=%q, want structured Markdown usage failure", exitCode, stderr.String())
	}
}
