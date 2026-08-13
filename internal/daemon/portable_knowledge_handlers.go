package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

var portableSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

func (s *server) handleKnowledgeExport(request localapi.Request) localapi.Response {
	var params localapi.KnowledgeExportParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || strings.TrimSpace(params.Project) == "" || params.Directory == "" {
		return invalidParamsResponse(request, "knowledge.export requires workspace, project, and an absolute clean directory")
	}
	if err := validatePortableDirectoryPath(params.Directory); err != nil {
		return portableFileErrorResponse(request, err)
	}
	if err := ensurePortableExportPathAbsent(params.Directory); err != nil {
		return portableFileErrorResponse(request, err)
	}
	exported, err := s.store.ExportKnowledgeBundle(context.Background(), store.ExportKnowledgeBundleQuery{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	if err := createPortableBundle(params.Directory, exported.ManifestJSON, exported.Markdown); err != nil {
		return portableFileErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.KnowledgeExportResult{
		Schema: localapi.KnowledgeExportSchema, Type: "knowledge_export", Directory: params.Directory,
		BundleID: exported.Manifest.BundleID, ContentSHA256: exported.Manifest.ContentSHA256,
		ManifestSHA256: portableFileSHA256(exported.ManifestJSON), ManifestBytes: int64(len(exported.ManifestJSON)),
		MarkdownSHA256: portableFileSHA256(exported.Markdown), MarkdownBytes: int64(len(exported.Markdown)),
		AsOfEventSequence: exported.AsOfEventSequence, Counts: exported.Manifest.Snapshot.Counts,
	})
}

func (s *server) handleKnowledgeImport(request localapi.Request) localapi.Response {
	var wire struct {
		Workspace             string `json:"workspace"`
		Project               string `json:"project"`
		Directory             string `json:"directory"`
		ExpectedContentSHA256 string `json:"expected_content_sha256"`
		CreateScope           *bool  `json:"create_scope"`
		IdempotencyKey        string `json:"idempotency_key"`
	}
	if err := decodeParams(request.Params, &wire); err != nil || strings.TrimSpace(wire.Workspace) == "" || strings.TrimSpace(wire.Project) == "" || wire.Directory == "" || !portableSHA256Pattern.MatchString(wire.ExpectedContentSHA256) || wire.CreateScope == nil || strings.TrimSpace(wire.IdempotencyKey) == "" {
		return invalidParamsResponse(request, "knowledge.import requires workspace, project, directory, expected_content_sha256, create_scope, and idempotency_key")
	}
	params := localapi.KnowledgeImportParams{
		Workspace: wire.Workspace, Project: wire.Project, Directory: wire.Directory,
		ExpectedContentSHA256: wire.ExpectedContentSHA256, CreateScope: *wire.CreateScope, IdempotencyKey: wire.IdempotencyKey,
	}
	if err := validatePortableDirectoryPath(params.Directory); err != nil {
		return portableFileErrorResponse(request, err)
	}
	files, err := readPortableBundle(params.Directory)
	if err != nil {
		return portableFileErrorResponse(request, err)
	}
	imported, err := s.store.ImportKnowledgeBundle(context.Background(), store.ImportKnowledgeBundleCommand{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project,
		ManifestJSON: files.Manifest, Markdown: files.Markdown,
		ExpectedContentSHA256: params.ExpectedContentSHA256, CreateScope: params.CreateScope,
		Actor: store.OwnerKnowledgeActor(), IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	status := "imported"
	if imported.Replayed {
		status = "already_present"
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.KnowledgeImportResult{
		Schema: localapi.KnowledgeImportSchema, Type: "knowledge_import", Directory: params.Directory,
		BundleID: imported.Receipt.BundleID, ContentSHA256: imported.Receipt.ContentSHA256,
		ManifestSHA256: portableFileSHA256(files.Manifest), ManifestBytes: int64(len(files.Manifest)),
		MarkdownSHA256: portableFileSHA256(files.Markdown), MarkdownBytes: int64(len(files.Markdown)),
		Counts: imported.Counts, Receipt: imported.Receipt, Status: status,
		Created: localapi.PortableKnowledgeCreatedCounts{
			Workspaces: boolCount(imported.Created.Workspace), Projects: boolCount(imported.Created.Project),
			TaskScopeAnchors: imported.Created.TaskScopeAnchors,
		},
		EventSequence: imported.EventSequence,
	})
}

func portableFileSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func boolCount(value bool) int64 {
	if value {
		return 1
	}
	return 0
}

func portableFileErrorResponse(request localapi.Request, err error) localapi.Response {
	code := portableFileErrorCode(err)
	return localapi.ErrorResponse(request.ID, request.Protocol, &localapi.APIError{
		Code: code, Message: err.Error(), Retryable: code == store.CodeStorageFailed,
	})
}
