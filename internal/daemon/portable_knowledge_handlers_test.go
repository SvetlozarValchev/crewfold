package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func TestPortableKnowledgeHandlersRejectUnknownFieldsAndUnsafePaths(t *testing.T) {
	running := startTestServer(t, testConfig(t))
	for _, test := range []struct {
		name   string
		method string
		params map[string]any
		code   string
	}{
		{name: "export unknown", method: localapi.MethodKnowledgeExport, params: map[string]any{"workspace": "personal", "project": "engine", "directory": "/tmp/export", "actor": "owner"}, code: "invalid_request"},
		{name: "import unknown", method: localapi.MethodKnowledgeImport, params: map[string]any{"workspace": "personal", "project": "engine", "directory": "/tmp/import", "expected_content_sha256": strings.Repeat("a", 64), "create_scope": false, "idempotency_key": "x", "markdown": "bytes"}, code: "invalid_request"},
		{name: "import missing create scope", method: localapi.MethodKnowledgeImport, params: map[string]any{"workspace": "personal", "project": "engine", "directory": "/tmp/import", "expected_content_sha256": strings.Repeat("a", 64), "idempotency_key": "x"}, code: "invalid_request"},
		{name: "export relative", method: localapi.MethodKnowledgeExport, params: map[string]any{"workspace": "personal", "project": "engine", "directory": "relative"}, code: codeInvalidKnowledgeBundlePath},
		{name: "import unclean", method: localapi.MethodKnowledgeImport, params: map[string]any{"workspace": "personal", "project": "engine", "directory": "/tmp/../tmp/import", "expected_content_sha256": strings.Repeat("a", 64), "create_scope": false, "idempotency_key": "x"}, code: codeInvalidKnowledgeBundlePath},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := rawLocalAPIRequest(t, running.config.SocketPath, test.method, test.params)
			if response.Error == nil || response.Error.Code != test.code || response.Error.Retryable {
				t.Fatalf("error=%#v, want nonretryable %s", response.Error, test.code)
			}
		})
	}
}

func TestPortableKnowledgeContractErrorsAreStableAndNonretryable(t *testing.T) {
	request := localapi.Request{ID: "portable-error", Protocol: localapi.MaxProtocol}
	for _, code := range []string{
		store.CodeKnowledgeExportPathExists,
		store.CodeInvalidKnowledgeBundlePath,
		store.CodeInvalidKnowledgeBundle,
		store.CodeKnowledgeBundleDigestMismatch,
		store.CodeKnowledgeImportScopeConflict,
		store.CodeKnowledgeImportConflict,
	} {
		var response localapi.Response
		if code == store.CodeKnowledgeExportPathExists || code == store.CodeInvalidKnowledgeBundlePath {
			response = portableFileErrorResponse(request, newPortableFileError(code, "portable failure", nil))
		} else {
			response = storeErrorResponse(request, &store.Error{Code: code, Message: "portable failure"})
		}
		if response.Error == nil || response.Error.Code != code || response.Error.Retryable {
			t.Errorf("code %s response=%#v", code, response.Error)
		}
	}
}

func TestPortableKnowledgeLocalAPIFilesystemRoundTrip(t *testing.T) {
	running := startTestServer(t, testConfig(t))
	client := localapi.NewClient(running.config.SocketPath)
	workspace, err := client.WorkspaceInit(context.Background(), "portable-source", "portable-source-init")
	if err != nil {
		t.Fatal(err)
	}
	repositoryRoot := t.TempDir()
	createGitFixture(t, repositoryRoot)
	project, err := client.ProjectAdd(context.Background(), workspace.Workspace.ID, "empty-project", filepath.Join(repositoryRoot, "world-engine"), domain.WriteModeShared, "portable-project")
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "bundle")
	exported, err := client.KnowledgeExport(context.Background(), localapi.KnowledgeExportParams{
		Workspace: workspace.Workspace.ID, Project: project.Project.ID, Directory: directory,
	})
	if err != nil {
		t.Fatalf("KnowledgeExport() error=%v", err)
	}
	if exported.Schema != localapi.KnowledgeExportSchema || exported.Directory != directory || exported.BundleID == "" || exported.ContentSHA256 == "" || exported.ManifestBytes <= 0 || exported.MarkdownBytes <= 0 {
		t.Fatalf("KnowledgeExport()=%#v", exported)
	}
	for _, name := range []string{portableManifestName, portableMarkdownName} {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s info=%v error=%v", name, info, err)
		}
	}
	if _, err := client.KnowledgeExport(context.Background(), localapi.KnowledgeExportParams{
		Workspace: workspace.Workspace.ID, Project: project.Project.ID, Directory: directory,
	}); localAPIErrorCode(err) != codeKnowledgeExportPathExists {
		t.Fatalf("second export error=%v code=%s", err, localAPIErrorCode(err))
	}

	imported, err := client.KnowledgeImport(context.Background(), localapi.KnowledgeImportParams{
		Workspace: workspace.Workspace.ID, Project: project.Project.ID, Directory: directory,
		ExpectedContentSHA256: exported.ContentSHA256, IdempotencyKey: "portable-import",
	})
	if err != nil {
		t.Fatalf("KnowledgeImport() error=%v", err)
	}
	if imported.Schema != localapi.KnowledgeImportSchema || imported.Status != "imported" || imported.BundleID != exported.BundleID || imported.ContentSHA256 != exported.ContentSHA256 || imported.EventSequence < 1 {
		t.Fatalf("KnowledgeImport()=%#v", imported)
	}
	if imported.ManifestSHA256 != exported.ManifestSHA256 || imported.MarkdownSHA256 != exported.MarkdownSHA256 || imported.ManifestBytes != exported.ManifestBytes || imported.MarkdownBytes != exported.MarkdownBytes {
		t.Fatalf("file summaries changed: export=%#v import=%#v", exported, imported)
	}
	replayed, err := client.KnowledgeImport(context.Background(), localapi.KnowledgeImportParams{
		Workspace: workspace.Workspace.ID, Project: project.Project.ID, Directory: directory,
		ExpectedContentSHA256: exported.ContentSHA256, IdempotencyKey: "portable-import",
	})
	if err != nil || replayed.Status != "already_present" || replayed.Receipt.ID != imported.Receipt.ID || replayed.EventSequence != imported.EventSequence {
		t.Fatalf("KnowledgeImport(replay)=%#v error=%v; first=%#v", replayed, err, imported)
	}
	if _, err := client.KnowledgeImport(context.Background(), localapi.KnowledgeImportParams{
		Workspace: workspace.Workspace.ID, Project: project.Project.ID, Directory: directory,
		ExpectedContentSHA256: strings.Repeat("0", 64), IdempotencyKey: "portable-bad-digest",
	}); localAPIErrorCode(err) != store.CodeKnowledgeBundleDigestMismatch {
		t.Fatalf("bad digest error=%v code=%s", err, localAPIErrorCode(err))
	}
}
