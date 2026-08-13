package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestKnowledgeExportCLIForwardsExactAbsolutePathAndWritesText(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "export")
	client := &fakeDaemonClient{knowledgeExport: localapi.KnowledgeExportResult{
		Schema: localapi.KnowledgeExportSchema, Type: "knowledge_export", Directory: directory,
		BundleID: "kbun_11111111111111111111111111111111", ContentSHA256: strings.Repeat("a", 64),
		ManifestSHA256: strings.Repeat("b", 64), ManifestBytes: 123,
		MarkdownSHA256: strings.Repeat("c", 64), MarkdownBytes: 456, AsOfEventSequence: 18,
		Counts: domain.PortableKnowledgeCounts{Items: 2, Revisions: 3, Contradictions: 1, TaskScopeAnchors: 1},
	}}
	app, stdout, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	exit := app.Run([]string{"knowledge", "export", directory, "--workspace", "personal", "--project", "engine", "--socket", "/tmp/crewfold.sock"})
	if exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}
	if client.knowledgeExportParams != (localapi.KnowledgeExportParams{Workspace: "personal", Project: "engine", Directory: directory}) {
		t.Fatalf("params=%#v", client.knowledgeExportParams)
	}
	for _, expected := range []string{directory, "kbun_", "as-of event sequence: 18", "revisions: 3"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("stdout=%q missing %q", stdout.String(), expected)
		}
	}
}

func TestKnowledgeImportCLIForwardsDigestCreateScopeAndDefaultIdempotency(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "bundle")
	digest := strings.Repeat("a", 64)
	client := &fakeDaemonClient{knowledgeImport: localapi.KnowledgeImportResult{
		Schema: localapi.KnowledgeImportSchema, Type: "knowledge_import", Directory: directory,
		BundleID: "kbun_11111111111111111111111111111111", ContentSHA256: digest, Status: "imported",
		Receipt: domain.KnowledgeImportReceipt{ID: "kimp_11111111111111111111111111111111"},
	}}
	app, stdout, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	exit := app.Run([]string{"knowledge", "import", directory, "--workspace", "personal", "--project", "engine", "--expected-content-sha256", digest, "--create-scope", "--socket", "/tmp/crewfold.sock", "--output", "json"})
	if exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}
	if client.knowledgeImportParams.Workspace != "personal" || client.knowledgeImportParams.Project != "engine" || client.knowledgeImportParams.Directory != directory || client.knowledgeImportParams.ExpectedContentSHA256 != digest || !client.knowledgeImportParams.CreateScope {
		t.Fatalf("params=%#v", client.knowledgeImportParams)
	}
	var result localapi.KnowledgeImportResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Schema != localapi.KnowledgeImportSchema || result.Status != "imported" {
		t.Fatalf("JSON=%#v error=%v stdout=%q", result, err, stdout.String())
	}
}

func TestPortableKnowledgeCLIRejectsUnsafePathsAndDigestBeforeCallingDaemon(t *testing.T) {
	for name, args := range map[string][]string{
		"uppercase digest":   {"knowledge", "import", "/tmp/bundle", "--workspace", "personal", "--project", "engine", "--expected-content-sha256", strings.Repeat("A", 64), "--socket", "/tmp/sock"},
		"valued create flag": {"knowledge", "import", "/tmp/bundle", "--workspace", "personal", "--project", "engine", "--expected-content-sha256", strings.Repeat("a", 64), "--create-scope=true", "--socket", "/tmp/sock"},
		"root export":        {"knowledge", "export", "/", "--workspace", "personal", "--project", "engine", "--socket", "/tmp/sock"},
	} {
		t.Run(name, func(t *testing.T) {
			client := &fakeDaemonClient{}
			app, _, stderr := newTestApp()
			app.newClient = func(string) daemonClient { return client }
			if exit := app.Run(args); exit != ExitUsage || stderr.Len() == 0 {
				t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
			}
			if client.knowledgeExportParams.Directory != "" || client.knowledgeImportParams.Directory != "" {
				t.Fatalf("daemon called with export=%#v import=%#v", client.knowledgeExportParams, client.knowledgeImportParams)
			}
		})
	}
}

func TestPortableKnowledgeCLIResolvesAndCleansPathsWithoutFollowingLinks(t *testing.T) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(workingDirectory, "bundle")
	for name, input := range map[string]string{
		"relative":       "bundle",
		"dot relative":   "./bundle",
		"parent segment": filepath.Join("subdir", "..", "bundle"),
		"trailing slash": "bundle/",
	} {
		t.Run(name, func(t *testing.T) {
			client := &fakeDaemonClient{knowledgeExport: localapi.KnowledgeExportResult{Schema: localapi.KnowledgeExportSchema, Type: "knowledge_export", Directory: want}}
			app, _, stderr := newTestApp()
			app.newClient = func(string) daemonClient { return client }
			if exit := app.Run([]string{"knowledge", "export", input, "--workspace", "personal", "--project", "engine", "--socket", "/tmp/sock"}); exit != ExitOK || stderr.Len() != 0 {
				t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
			}
			if client.knowledgeExportParams.Directory != want {
				t.Fatalf("resolved directory=%q, want %q", client.knowledgeExportParams.Directory, want)
			}
		})
	}
}

func TestKnowledgeHelpPublishesPortableCommands(t *testing.T) {
	app, stdout, stderr := newTestApp()
	if exit := app.Run([]string{"help", "knowledge"}); exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}
	for _, expected := range []string{"knowledge export <directory>", "knowledge import <directory>", "--expected-content-sha256", "--create-scope", "never overwrites"} {
		if !strings.Contains(stdout.String(), expected) {
			t.Errorf("help missing %q: %s", expected, stdout.String())
		}
	}
}
