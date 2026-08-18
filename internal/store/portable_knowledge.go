package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"
)

const (
	maximumKnowledgeBundleFileBytes        = 64 * 1024 * 1024
	maximumPortableKnowledgeItems          = 4096
	maximumPortableKnowledgeRevisions      = 16384
	maximumPortableKnowledgeContradictions = 8192
	maximumPortableKnowledgeJSONDepth      = 64
	knowledgeImportedEvent                 = "knowledge.imported"
	contradictionImportedEvent             = "contradiction.imported"
	knowledgeImportCompletedEvent          = "knowledge.import_completed"
)

var errPortableKnowledgeSizeLimit = errors.New("portable knowledge representation exceeds its byte limit")

var errPortableKnowledgeStructuralLimit = errors.New("portable knowledge manifest exceeds structural limits")

type portableKnowledgeStructuralLimits struct {
	TaskScopeAnchors int
	Items            int
	Revisions        int
	Contradictions   int
	Sources          int
	JSONDepth        int
}

var portableKnowledgeManifestLimits = portableKnowledgeStructuralLimits{
	TaskScopeAnchors: maximumPortableKnowledgeItems,
	Items:            maximumPortableKnowledgeItems,
	Revisions:        maximumPortableKnowledgeRevisions,
	Contradictions:   maximumPortableKnowledgeContradictions,
	Sources:          maximumKnowledgeSources,
	JSONDepth:        maximumPortableKnowledgeJSONDepth,
}

type portableBoundedBuffer struct {
	buffer bytes.Buffer
	limit  int
	err    error
}

func newPortableBoundedBuffer(limit int) *portableBoundedBuffer {
	return &portableBoundedBuffer{limit: limit}
}

func (buffer *portableBoundedBuffer) Write(value []byte) (int, error) {
	if buffer.err != nil {
		return 0, buffer.err
	}
	if len(value) > buffer.limit-buffer.buffer.Len() {
		buffer.err = errPortableKnowledgeSizeLimit
		return 0, buffer.err
	}
	return buffer.buffer.Write(value)
}

func (buffer *portableBoundedBuffer) WriteString(value string) {
	if buffer.err == nil {
		_, buffer.err = buffer.Write([]byte(value))
	}
}

func (buffer *portableBoundedBuffer) Bytes() []byte { return buffer.buffer.Bytes() }
func (buffer *portableBoundedBuffer) Err() error    { return buffer.err }

type portableCanonicalHashWriter struct {
	digest     hash.Hash
	limit      int
	written    int
	pending    byte
	hasPending bool
	err        error
}

type portableMarkdownWriter interface {
	WriteString(string)
	Err() error
}

type portableMarkdownComparisonWriter struct {
	expected []byte
	offset   int
	err      error
}

func (writer *portableMarkdownComparisonWriter) WriteString(value string) {
	if writer.err != nil {
		return
	}
	end := writer.offset + len(value)
	if end > len(writer.expected) || !bytes.Equal(writer.expected[writer.offset:end], []byte(value)) {
		writer.err = errors.New("portable knowledge Markdown differs")
		return
	}
	writer.offset = end
}

func (writer *portableMarkdownComparisonWriter) Err() error { return writer.err }

func (writer *portableCanonicalHashWriter) Write(value []byte) (int, error) {
	if writer.err != nil {
		return 0, writer.err
	}
	if len(value) > writer.limit+1-writer.written {
		writer.err = errPortableKnowledgeSizeLimit
		return 0, writer.err
	}
	if len(value) == 0 {
		return 0, nil
	}
	if writer.hasPending {
		_, _ = writer.digest.Write([]byte{writer.pending})
	}
	if len(value) > 1 {
		_, _ = writer.digest.Write(value[:len(value)-1])
	}
	writer.pending = value[len(value)-1]
	writer.hasPending = true
	writer.written += len(value)
	return len(value), nil
}

func canonicalJSONSHA256(value any) (string, error) {
	digest := sha256.New()
	writer := &portableCanonicalHashWriter{digest: digest, limit: maximumKnowledgeBundleFileBytes}
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	if writer.err != nil {
		return "", writer.err
	}
	if !writer.hasPending || writer.pending != '\n' || writer.written-1 > writer.limit {
		return "", errPortableKnowledgeSizeLimit
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func (s *Store) ExportKnowledgeBundle(ctx context.Context, query ExportKnowledgeBundleQuery) (KnowledgeBundleExportResult, error) {
	query.WorkspaceIdentifier = strings.TrimSpace(query.WorkspaceIdentifier)
	query.ProjectIdentifier = strings.TrimSpace(query.ProjectIdentifier)
	if query.WorkspaceIdentifier == "" || query.ProjectIdentifier == "" {
		return KnowledgeBundleExportResult{}, invalidKnowledgeBundle("knowledge export requires workspace and project")
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return KnowledgeBundleExportResult{}, storageFailure("begin portable knowledge export", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, query.WorkspaceIdentifier)
	if err != nil {
		return KnowledgeBundleExportResult{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, query.ProjectIdentifier)
	if err != nil {
		return KnowledgeBundleExportResult{}, err
	}
	snapshot, err := s.portableKnowledgeSnapshotInTransaction(ctx, tx, workspace, project)
	if err != nil {
		return KnowledgeBundleExportResult{}, err
	}
	asOf, err := dbgen.New(tx).PortableKnowledgeEventHighWater(ctx)
	if err != nil {
		return KnowledgeBundleExportResult{}, storageFailure("read portable knowledge event cursor", err)
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeBundleExportResult{}, storageFailure("commit portable knowledge snapshot read", err)
	}
	if err := validatePortableKnowledgeSnapshot(snapshot); err != nil {
		return KnowledgeBundleExportResult{}, err
	}
	result, err := renderPortableKnowledgeBundle(snapshot)
	if err != nil {
		return KnowledgeBundleExportResult{}, err
	}
	result.AsOfEventSequence = asOf
	return result, nil
}

func (s *Store) portableKnowledgeSnapshotInTransaction(ctx context.Context, tx *sql.Tx, workspace Workspace, project domain.Project) (domain.PortableKnowledgeSnapshot, error) {
	queries := dbgen.New(tx)
	preflight, err := queries.PortableKnowledgeSnapshotPreflight(ctx, dbgen.PortableKnowledgeSnapshotPreflightParams{
		WorkspaceID: workspace.ID, ProjectID: project.ID,
	})
	if err != nil {
		return domain.PortableKnowledgeSnapshot{}, storageFailure("preflight portable knowledge snapshot", err)
	}
	if preflight.ItemCount > maximumPortableKnowledgeItems || preflight.RevisionCount > maximumPortableKnowledgeRevisions ||
		preflight.ContradictionCount > maximumPortableKnowledgeContradictions {
		return domain.PortableKnowledgeSnapshot{}, invalidKnowledgeBundle("project exceeds portable knowledge record limits")
	}
	if preflight.PayloadByteFloor > maximumKnowledgeBundleFileBytes {
		return domain.PortableKnowledgeSnapshot{}, invalidKnowledgeBundle("portable knowledge snapshot exceeds the 64 MiB representation limit")
	}
	itemRows, err := queries.ListPortableKnowledgeItems(ctx, dbgen.ListPortableKnowledgeItemsParams{WorkspaceID: workspace.ID, ProjectID: project.ID})
	if err != nil {
		return domain.PortableKnowledgeSnapshot{}, storageFailure("list portable knowledge items", err)
	}
	if len(itemRows) > maximumPortableKnowledgeItems {
		return domain.PortableKnowledgeSnapshot{}, invalidKnowledgeBundle("project exceeds portable knowledge item limit")
	}
	anchorRows, err := queries.ListPortableKnowledgeTaskScopeAnchors(ctx, dbgen.ListPortableKnowledgeTaskScopeAnchorsParams{WorkspaceID: workspace.ID, ProjectID: project.ID})
	if err != nil {
		return domain.PortableKnowledgeSnapshot{}, storageFailure("list portable knowledge task-scope anchors", err)
	}
	contradictionRows, err := queries.ListPortableKnowledgeContradictions(ctx, dbgen.ListPortableKnowledgeContradictionsParams{WorkspaceID: workspace.ID, ProjectID: project.ID})
	if err != nil {
		return domain.PortableKnowledgeSnapshot{}, storageFailure("list portable knowledge contradictions", err)
	}
	if len(contradictionRows) > maximumPortableKnowledgeContradictions {
		return domain.PortableKnowledgeSnapshot{}, invalidKnowledgeBundle("project exceeds portable contradiction limit")
	}
	snapshot := domain.PortableKnowledgeSnapshot{
		Scope:            domain.PortableKnowledgeScope{WorkspaceID: workspace.ID, WorkspaceName: workspace.Name, ProjectID: project.ID, ProjectName: project.Name},
		TaskScopeAnchors: make([]domain.PortableKnowledgeTaskScopeAnchor, 0, len(anchorRows)),
		Items:            make([]domain.PortableKnowledgeItem, 0, len(itemRows)),
		Contradictions:   make([]domain.PortableKnowledgeContradiction, 0, len(contradictionRows)),
	}
	for _, row := range anchorRows {
		snapshot.TaskScopeAnchors = append(snapshot.TaskScopeAnchors, domain.PortableKnowledgeTaskScopeAnchor{
			TaskID: row.TaskID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, CreatedAt: row.CreatedAt, CreatedBy: row.CreatedBy,
		})
	}
	var snapshotPayloadBytes int64
	for _, row := range itemRows {
		ids, err := queries.ListPortableKnowledgeRevisionIDsForItem(ctx, row.ID)
		if err != nil {
			return domain.PortableKnowledgeSnapshot{}, storageFailure("list portable knowledge revisions", err)
		}
		item := domain.PortableKnowledgeItem{Item: domain.KnowledgeItem{
			ID: row.ID, WorkspaceID: row.WorkspaceID, ProjectID: row.ProjectID, TaskScopeID: row.TaskScopeID,
			Type: row.Type, CreatedAt: row.CreatedAt, CreatedBy: row.CreatedBy, CreatedByType: row.CreatedByType,
		}, Revisions: make([]domain.KnowledgeRevision, 0, len(ids))}
		for _, id := range ids {
			revision, err := s.KnowledgeRevisionInTransaction(ctx, tx, workspace.ID, id)
			if err != nil {
				return domain.PortableKnowledgeSnapshot{}, err
			}
			if err := addPortableKnowledgePayloadBytes(&snapshotPayloadBytes, revision); err != nil {
				return domain.PortableKnowledgeSnapshot{}, err
			}
			item.Revisions = append(item.Revisions, revision)
		}
		snapshot.Counts.Revisions += int64(len(item.Revisions))
		if snapshot.Counts.Revisions > maximumPortableKnowledgeRevisions {
			return domain.PortableKnowledgeSnapshot{}, invalidKnowledgeBundle("project exceeds portable knowledge revision limit")
		}
		snapshot.Items = append(snapshot.Items, item)
	}
	for _, row := range contradictionRows {
		snapshot.Contradictions = append(snapshot.Contradictions, portableContradiction(knowledgeContradictionFromRow(row)))
	}
	snapshot.Counts.TaskScopeAnchors = int64(len(snapshot.TaskScopeAnchors))
	snapshot.Counts.Items = int64(len(snapshot.Items))
	snapshot.Counts.Contradictions = int64(len(snapshot.Contradictions))
	return snapshot, nil
}

func addPortableKnowledgePayloadBytes(total *int64, revision domain.KnowledgeRevision) error {
	// Title/body/source strings are an unavoidable lower bound for both the
	// canonical JSON and human rendering. Refuse a project once that lower bound
	// crosses the public file cap; bounded encoders below account for escaping
	// and all structural overhead without ever growing beyond the cap.
	added := int64(len(revision.Title) + len(revision.Body) + len(revision.DecisionNote) + len(revision.StaleReason))
	for _, source := range revision.Sources {
		added += int64(len(source.Type) + len(source.ID) + len(source.Role))
	}
	if added > maximumKnowledgeBundleFileBytes-*total {
		return invalidKnowledgeBundle("portable knowledge snapshot exceeds the 64 MiB representation limit")
	}
	*total += added
	return nil
}

func portableContradiction(value domain.KnowledgeContradiction) domain.PortableKnowledgeContradiction {
	return domain.PortableKnowledgeContradiction{
		ID: value.ID, WorkspaceID: value.WorkspaceID, ProjectID: value.ProjectID,
		LeftRevisionID: value.LeftRevisionID, RightRevisionID: value.RightRevisionID,
		Status: value.Status, StateRevision: value.StateRevision, ReportNote: value.ReportNote,
		ReportedAt: value.ReportedAt, ReportedBy: value.ReportedBy, ReportedByType: value.ReportedByType,
		ConfirmedAt: value.ConfirmedAt, ConfirmedBy: value.ConfirmedBy, ConfirmedByType: value.ConfirmedByType, ConfirmNote: value.ConfirmNote,
		DismissedAt: value.DismissedAt, DismissedBy: value.DismissedBy, DismissedByType: value.DismissedByType, DismissNote: value.DismissNote,
		ResolutionReason: value.ResolutionReason, ResolvedAt: value.ResolvedAt, ResolvedBy: value.ResolvedBy,
		ResolvedByType: value.ResolvedByType, ResolutionNote: value.ResolutionNote,
	}
}

func renderPortableKnowledgeBundle(snapshot domain.PortableKnowledgeSnapshot) (KnowledgeBundleExportResult, error) {
	contentHash, err := canonicalJSONSHA256(snapshot)
	if err != nil {
		if errors.Is(err, errPortableKnowledgeSizeLimit) {
			return KnowledgeBundleExportResult{}, invalidKnowledgeBundle("portable knowledge snapshot exceeds the 64 MiB representation limit")
		}
		return KnowledgeBundleExportResult{}, storageFailure("encode portable knowledge snapshot", err)
	}
	markdown, err := renderPortableKnowledgeMarkdown(snapshot)
	if err != nil {
		if errors.Is(err, errPortableKnowledgeSizeLimit) {
			return KnowledgeBundleExportResult{}, invalidKnowledgeBundle("portable knowledge Markdown exceeds the 64 MiB file limit")
		}
		return KnowledgeBundleExportResult{}, storageFailure("render portable knowledge Markdown", err)
	}
	markdownDigest := sha256.Sum256(markdown)
	manifest := domain.PortableKnowledgeBundleManifest{
		Schema: domain.PortableKnowledgeBundleManifestSchema, Type: domain.PortableKnowledgeBundleType,
		BundleID: "kbun_" + contentHash[:32], ContentSHA256: contentHash, Snapshot: snapshot,
		Rendering: domain.PortableKnowledgeRendering{Path: domain.PortableKnowledgeRenderingPath,
			MediaType: domain.PortableKnowledgeRenderingMediaType, ByteSize: int64(len(markdown)), SHA256: hex.EncodeToString(markdownDigest[:])},
	}
	manifestJSON, err := canonicalJSONLine(manifest)
	if err != nil {
		if errors.Is(err, errPortableKnowledgeSizeLimit) {
			return KnowledgeBundleExportResult{}, invalidKnowledgeBundle("portable knowledge manifest exceeds the 64 MiB file limit")
		}
		return KnowledgeBundleExportResult{}, storageFailure("encode portable knowledge manifest", err)
	}
	if len(manifestJSON) > maximumKnowledgeBundleFileBytes || len(markdown) > maximumKnowledgeBundleFileBytes {
		return KnowledgeBundleExportResult{}, invalidKnowledgeBundle("portable knowledge bundle exceeds the 64 MiB file limit")
	}
	return KnowledgeBundleExportResult{Manifest: manifest, ManifestJSON: manifestJSON, Markdown: markdown}, nil
}

func canonicalJSON(value any) ([]byte, error) {
	return canonicalJSONWithLimit(value, maximumKnowledgeBundleFileBytes, false)
}

func canonicalJSONWithLimit(value any, limit int, line bool) ([]byte, error) {
	encoderLimit := limit
	if !line {
		encoderLimit++ // Encoder always emits one LF, removed below.
	}
	buffer := newPortableBoundedBuffer(encoderLimit)
	encoder := json.NewEncoder(buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	if buffer.Err() != nil {
		return nil, buffer.Err()
	}
	encoded := buffer.Bytes()
	if line {
		return encoded, nil
	}
	return bytes.TrimSuffix(encoded, []byte{'\n'}), nil
}

func canonicalJSONLine(value any) ([]byte, error) {
	return canonicalJSONWithLimit(value, maximumKnowledgeBundleFileBytes, true)
}

func renderPortableKnowledgeMarkdown(snapshot domain.PortableKnowledgeSnapshot) ([]byte, error) {
	return renderPortableKnowledgeMarkdownWithLimit(snapshot, maximumKnowledgeBundleFileBytes)
}

func renderPortableKnowledgeMarkdownWithLimit(snapshot domain.PortableKnowledgeSnapshot, limit int) ([]byte, error) {
	output := newPortableBoundedBuffer(limit)
	writePortableKnowledgeMarkdown(snapshot, output)
	if output.Err() != nil {
		return nil, output.Err()
	}
	return output.Bytes(), nil
}

func writePortableKnowledgeMarkdown(snapshot domain.PortableKnowledgeSnapshot, output portableMarkdownWriter) {
	output.WriteString("# Crewfold portable knowledge\n\n")
	output.WriteString("Workspace: " + markdownInline(snapshot.Scope.WorkspaceName) + " (`" + snapshot.Scope.WorkspaceID + "`)  \n")
	output.WriteString("Project: " + markdownInline(snapshot.Scope.ProjectName) + " (`" + snapshot.Scope.ProjectID + "`)\n\n")
	output.WriteString("Items: " + strconv.FormatInt(snapshot.Counts.Items, 10) +
		"; revisions: " + strconv.FormatInt(snapshot.Counts.Revisions, 10) +
		"; contradictions: " + strconv.FormatInt(snapshot.Counts.Contradictions, 10) + ".\n")
	for _, item := range snapshot.Items {
		output.WriteString("\n## " + markdownHeading(item.Item.ID) + "\n")
		output.WriteString("Type: `" + item.Item.Type + "`")
		if item.Item.TaskScopeID != "" {
			output.WriteString("; task scope: `" + item.Item.TaskScopeID + "`")
		}
		output.WriteString("\n")
		for _, revision := range item.Revisions {
			output.WriteString("\n### " + markdownHeading(revision.ID) + " — " + markdownHeading(revision.Title) + "\n\n")
			output.WriteString("State: `" + revision.ReviewStatus + "` / `" + revision.CurrencyStatus + "`; revision " + strconv.FormatInt(revision.RevisionNumber, 10) + ".\n\n")
			output.WriteString(markdownBody(revision.Body) + "\n")
		}
	}
	if len(snapshot.Contradictions) != 0 {
		output.WriteString("\n## Contradictions\n")
		for _, contradiction := range snapshot.Contradictions {
			output.WriteString("\n- `" + contradiction.ID + "` (`" + contradiction.Status + "`): `" + contradiction.LeftRevisionID + "` ↔ `" + contradiction.RightRevisionID + "` — " + markdownInline(contradiction.ReportNote) + "\n")
		}
	}
}

func comparePortableKnowledgeMarkdown(snapshot domain.PortableKnowledgeSnapshot, expected []byte) error {
	output := &portableMarkdownComparisonWriter{expected: expected}
	writePortableKnowledgeMarkdown(snapshot, output)
	if output.Err() != nil {
		return output.Err()
	}
	if output.offset != len(expected) {
		return errors.New("portable knowledge Markdown has trailing bytes")
	}
	return nil
}

func markdownInline(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "\r", " ")
	return markdownText(value)
}

func markdownHeading(value string) string {
	return markdownInline(value)
}

func markdownBody(value string) string {
	lines := strings.Split(strings.ReplaceAll(value, "\r\n", "\n"), "\n")
	for index := range lines {
		lines[index] = markdownText(lines[index])
	}
	return strings.Join(lines, "\n")
}

func markdownText(value string) string {
	var escaped strings.Builder
	for _, character := range value {
		if unicode.IsControl(character) || unicode.In(character, unicode.Cf) {
			escaped.WriteString(`\u{`)
			escaped.WriteString(strconv.FormatInt(int64(character), 16))
			escaped.WriteByte('}')
			continue
		}
		switch character {
		case '&':
			escaped.WriteString("&amp;")
		case '<':
			escaped.WriteString("&lt;")
		case '>':
			escaped.WriteString("&gt;")
		case '\\', '`', '*', '_', '{', '}', '[', ']', '(', ')', '#', '+', '-', '.', '!', '|':
			escaped.WriteByte('\\')
			escaped.WriteRune(character)
		default:
			escaped.WriteRune(character)
		}
	}
	return escaped.String()
}

func (s *Store) ImportKnowledgeBundle(ctx context.Context, command ImportKnowledgeBundleCommand) (KnowledgeBundleImportResult, error) {
	normalizeImportKnowledgeCommand(&command)
	if command.WorkspaceIdentifier == "" || command.ProjectIdentifier == "" || !knowledgeActorIsOwner(command.Actor) {
		if validKnowledgeActor(command.Actor) && !knowledgeActorIsOwner(command.Actor) {
			return KnowledgeBundleImportResult{}, &Error{Code: CodeKnowledgeImportDenied, Message: "only the local owner may import portable knowledge"}
		}
		return KnowledgeBundleImportResult{}, invalidKnowledgeBundle("knowledge import requires target workspace, project, and trusted owner")
	}
	if !validLowerHex(command.ExpectedContentSHA256, 64) {
		return KnowledgeBundleImportResult{}, bundleDigestMismatch("expected content digest must be exactly 64 lowercase hexadecimal characters")
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidKnowledgeBundle); err != nil {
		return KnowledgeBundleImportResult{}, err
	}
	manifest, err := validatePortableKnowledgeBundle(command.ManifestJSON, command.Markdown, command.ExpectedContentSHA256)
	if err != nil {
		return KnowledgeBundleImportResult{}, err
	}
	if !identifierMatches(command.WorkspaceIdentifier, manifest.Snapshot.Scope.WorkspaceID, manifest.Snapshot.Scope.WorkspaceName) ||
		!identifierMatches(command.ProjectIdentifier, manifest.Snapshot.Scope.ProjectID, manifest.Snapshot.Scope.ProjectName) {
		return KnowledgeBundleImportResult{}, importScopeConflict("target workspace/project guards do not match the bundle scope")
	}
	requestHash, err := hashCommand("knowledge.import", map[string]any{
		"workspace": manifest.Snapshot.Scope.WorkspaceID, "project": manifest.Snapshot.Scope.ProjectID,
		"manifest_sha256": bytesSHA256(command.ManifestJSON), "markdown_sha256": bytesSHA256(command.Markdown),
		"expected_content_sha256": command.ExpectedContentSHA256, "create_scope": command.CreateScope, "actor": command.Actor,
	})
	if err != nil {
		return KnowledgeBundleImportResult{}, storageFailure("hash portable knowledge import", err)
	}
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return KnowledgeBundleImportResult{}, storageFailure("begin portable knowledge import", err)
	}
	defer tx.Rollback()
	if replay, found, err := lookupPortableKnowledgeImportReplay(ctx, tx, command, manifest, requestHash); err != nil {
		return KnowledgeBundleImportResult{}, err
	} else if found {
		return replay, nil
	}
	workspace, project, created, err := ensurePortableKnowledgeImportScope(ctx, tx, manifest.Snapshot.Scope, command.CreateScope)
	if err != nil {
		return KnowledgeBundleImportResult{}, err
	}
	if workspace.ID != manifest.Snapshot.Scope.WorkspaceID || workspace.Name != manifest.Snapshot.Scope.WorkspaceName ||
		project.ID != manifest.Snapshot.Scope.ProjectID || project.Name != manifest.Snapshot.Scope.ProjectName {
		return KnowledgeBundleImportResult{}, importScopeConflict("existing target scope does not exactly match bundle IDs and names")
	}
	if err := requireEmptyKnowledgeProject(ctx, tx, workspace.ID, project.ID); err != nil {
		return KnowledgeBundleImportResult{}, err
	}
	if err := preflightPortableKnowledgeIdentities(ctx, tx, manifest.Snapshot); err != nil {
		return KnowledgeBundleImportResult{}, err
	}
	created.TaskScopeAnchors, err = ensurePortableKnowledgeAnchors(ctx, tx, manifest.Snapshot.Scope, manifest.Snapshot.TaskScopeAnchors, command.CreateScope)
	if err != nil {
		return KnowledgeBundleImportResult{}, err
	}
	now := s.nowText()
	importID, err := randomID("kimp_")
	if err != nil {
		return KnowledgeBundleImportResult{}, storageFailure("generate knowledge import id", err)
	}
	s.restoreActive.Store(true)
	defer s.restoreActive.Store(false)
	lastSequence, err := s.insertPortableKnowledgeSnapshot(ctx, tx, importID, manifest.Snapshot, command, now)
	if err != nil {
		return KnowledgeBundleImportResult{}, err
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return KnowledgeBundleImportResult{}, err
	}
	completedSequence, err := appendEventForActor(ctx, tx, workspace.ID, "knowledge_import", importID, 1,
		knowledgeImportCompletedEvent, command.CorrelationID, now, command.Actor.ID, command.Actor.Type,
		map[string]any{"bundle_id": manifest.BundleID, "project_id": project.ID, "content_sha256": manifest.ContentSHA256,
			"item_count": manifest.Snapshot.Counts.Items, "revision_count": manifest.Snapshot.Counts.Revisions,
			"contradiction_count": manifest.Snapshot.Counts.Contradictions})
	if err != nil {
		return KnowledgeBundleImportResult{}, err
	}
	if completedSequence > lastSequence {
		lastSequence = completedSequence
	}
	if err := s.runMutationHook(MutationAfterEvent); err != nil {
		return KnowledgeBundleImportResult{}, err
	}
	receipt := domain.KnowledgeImportReceipt{ID: importID, BundleID: manifest.BundleID, WorkspaceID: workspace.ID,
		ProjectID: project.ID, ContentSHA256: manifest.ContentSHA256, RenderingSHA256: manifest.Rendering.SHA256,
		ImportedAt: now, ImportedBy: command.Actor.ID, ImportedByType: command.Actor.Type, CompletedEventSequence: completedSequence}
	if err := dbgen.New(tx).InsertPortableKnowledgeImportReceipt(ctx, dbgen.InsertPortableKnowledgeImportReceiptParams{
		ID: receipt.ID, BundleID: receipt.BundleID, WorkspaceID: receipt.WorkspaceID, ProjectID: receipt.ProjectID,
		ContentSha256: receipt.ContentSHA256, RenderingSha256: receipt.RenderingSHA256,
		ManifestJson: command.ManifestJSON, Markdown: command.Markdown, IdempotencyKey: command.IdempotencyKey,
		RequestHash: requestHash, ImportedAt: receipt.ImportedAt, ImportedBy: receipt.ImportedBy,
		ImportedByType: receipt.ImportedByType, CreatedWorkspace: boolInteger(created.Workspace),
		CreatedProject: boolInteger(created.Project), CreatedTaskScopeAnchors: created.TaskScopeAnchors,
		CompletedEventSequence: receipt.CompletedEventSequence,
	}); err != nil {
		return KnowledgeBundleImportResult{}, storageFailure("record knowledge import receipt", err)
	}
	result := KnowledgeBundleImportResult{Receipt: receipt, Counts: manifest.Snapshot.Counts, Created: created, EventSequence: lastSequence}
	s.restoreActive.Store(false)
	if err := tx.Commit(); err != nil {
		return KnowledgeBundleImportResult{}, storageFailure("commit portable knowledge import", err)
	}
	s.refreshKnowledgeIndexAfterCanonicalMutation(ctx)
	return result, nil
}

func normalizeImportKnowledgeCommand(command *ImportKnowledgeBundleCommand) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.ProjectIdentifier = strings.TrimSpace(command.ProjectIdentifier)
	command.ExpectedContentSHA256 = strings.TrimSpace(command.ExpectedContentSHA256)
	command.Actor.ID = strings.TrimSpace(command.Actor.ID)
	command.Actor.Type = strings.TrimSpace(command.Actor.Type)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
}

func validatePortableKnowledgeBundle(manifestBytes, markdown []byte, expectedContentHash string) (domain.PortableKnowledgeBundleManifest, error) {
	if len(manifestBytes) == 0 || len(manifestBytes) > maximumKnowledgeBundleFileBytes || len(markdown) > maximumKnowledgeBundleFileBytes ||
		!utf8.Valid(manifestBytes) || !utf8.Valid(markdown) || bytes.IndexByte(manifestBytes, 0) >= 0 || bytes.IndexByte(markdown, 0) >= 0 {
		return domain.PortableKnowledgeBundleManifest{}, invalidKnowledgeBundle("bundle files must be bounded valid UTF-8 without NUL")
	}
	if err := preflightPortableKnowledgeManifestStructure(manifestBytes, portableKnowledgeManifestLimits); err != nil {
		return domain.PortableKnowledgeBundleManifest{}, err
	}
	var manifest domain.PortableKnowledgeBundleManifest
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return manifest, invalidKnowledgeBundle("manifest is not strict portable knowledge JSON")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return manifest, invalidKnowledgeBundle("manifest contains trailing JSON")
	}
	canonical, err := canonicalJSONLine(manifest)
	if err != nil || !bytes.Equal(canonical, manifestBytes) {
		return manifest, invalidKnowledgeBundle("manifest is not canonical JSON v1")
	}
	if manifest.Schema != domain.PortableKnowledgeBundleManifestSchema || manifest.Type != domain.PortableKnowledgeBundleType ||
		manifest.Rendering.Path != domain.PortableKnowledgeRenderingPath || manifest.Rendering.MediaType != domain.PortableKnowledgeRenderingMediaType {
		return manifest, invalidKnowledgeBundle("manifest schema, type, or rendering metadata is unsupported")
	}
	if expectedContentHash != "" && expectedContentHash != manifest.ContentSHA256 {
		return manifest, bundleDigestMismatch("expected content digest does not match manifest")
	}
	if err := validatePortableKnowledgeSnapshot(manifest.Snapshot); err != nil {
		return manifest, err
	}
	contentHash, err := canonicalJSONSHA256(manifest.Snapshot)
	if err != nil {
		return manifest, invalidKnowledgeBundle("snapshot canonical representation exceeds the portable file limit")
	}
	if manifest.ContentSHA256 != contentHash || manifest.BundleID != "kbun_"+contentHash[:32] {
		return manifest, bundleDigestMismatch("snapshot content digest or bundle ID does not match canonical bytes")
	}
	if manifest.Rendering.ByteSize != int64(len(markdown)) || manifest.Rendering.SHA256 != bytesSHA256(markdown) {
		return manifest, bundleDigestMismatch("knowledge.md digest or byte size does not match manifest")
	}
	if err := comparePortableKnowledgeMarkdown(manifest.Snapshot, markdown); err != nil {
		return manifest, bundleDigestMismatch("knowledge.md is not the deterministic rendering of the snapshot")
	}
	if err := validatePortableKnowledgeBundleSize(manifest); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func preflightPortableKnowledgeManifestStructure(data []byte, limits portableKnowledgeStructuralLimits) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanPortableKnowledgeManifest(decoder, limits); err != nil {
		if errors.Is(err, errPortableKnowledgeStructuralLimit) {
			return invalidKnowledgeBundle("manifest exceeds portable knowledge structural limits")
		}
		return invalidKnowledgeBundle("manifest JSON structure cannot be safely decoded")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return invalidKnowledgeBundle("manifest contains trailing JSON")
	}
	return nil
}

func scanPortableKnowledgeManifest(decoder *json.Decoder, limits portableKnowledgeStructuralLimits) error {
	if err := consumePortableJSONOpening(decoder, '{'); err != nil {
		return err
	}
	seenSnapshot := false
	for decoder.More() {
		key, err := consumePortableJSONKey(decoder)
		if err != nil {
			return err
		}
		if key != "snapshot" {
			if err := skipPortableJSONValue(decoder, limits.JSONDepth); err != nil {
				return err
			}
			continue
		}
		if seenSnapshot {
			return errPortableKnowledgeStructuralLimit
		}
		seenSnapshot = true
		if err := scanPortableKnowledgeSnapshot(decoder, limits); err != nil {
			return err
		}
	}
	return consumePortableJSONClosing(decoder, '}')
}

func scanPortableKnowledgeSnapshot(decoder *json.Decoder, limits portableKnowledgeStructuralLimits) error {
	if err := consumePortableJSONOpening(decoder, '{'); err != nil {
		return err
	}
	seenAnchors, seenItems, seenContradictions := false, false, false
	revisions := 0
	for decoder.More() {
		key, err := consumePortableJSONKey(decoder)
		if err != nil {
			return err
		}
		switch key {
		case "task_scope_anchors":
			if seenAnchors {
				return errPortableKnowledgeStructuralLimit
			}
			seenAnchors = true
			if err := scanPortableJSONArray(decoder, limits.TaskScopeAnchors, func() error {
				return skipPortableJSONValue(decoder, limits.JSONDepth)
			}); err != nil {
				return err
			}
		case "items":
			if seenItems {
				return errPortableKnowledgeStructuralLimit
			}
			seenItems = true
			if err := scanPortableJSONArray(decoder, limits.Items, func() error {
				return scanPortableKnowledgeItem(decoder, limits, &revisions)
			}); err != nil {
				return err
			}
		case "contradictions":
			if seenContradictions {
				return errPortableKnowledgeStructuralLimit
			}
			seenContradictions = true
			if err := scanPortableJSONArray(decoder, limits.Contradictions, func() error {
				return skipPortableJSONValue(decoder, limits.JSONDepth)
			}); err != nil {
				return err
			}
		default:
			if err := skipPortableJSONValue(decoder, limits.JSONDepth); err != nil {
				return err
			}
		}
	}
	return consumePortableJSONClosing(decoder, '}')
}

func scanPortableKnowledgeItem(decoder *json.Decoder, limits portableKnowledgeStructuralLimits, revisions *int) error {
	if err := consumePortableJSONOpening(decoder, '{'); err != nil {
		return err
	}
	seenRevisions := false
	for decoder.More() {
		key, err := consumePortableJSONKey(decoder)
		if err != nil {
			return err
		}
		if key != "revisions" {
			if err := skipPortableJSONValue(decoder, limits.JSONDepth); err != nil {
				return err
			}
			continue
		}
		if seenRevisions {
			return errPortableKnowledgeStructuralLimit
		}
		seenRevisions = true
		if err := consumePortableJSONOpening(decoder, '['); err != nil {
			return err
		}
		for decoder.More() {
			(*revisions)++
			if *revisions > limits.Revisions {
				return errPortableKnowledgeStructuralLimit
			}
			if err := scanPortableKnowledgeRevision(decoder, limits); err != nil {
				return err
			}
		}
		if err := consumePortableJSONClosing(decoder, ']'); err != nil {
			return err
		}
	}
	return consumePortableJSONClosing(decoder, '}')
}

func scanPortableKnowledgeRevision(decoder *json.Decoder, limits portableKnowledgeStructuralLimits) error {
	if err := consumePortableJSONOpening(decoder, '{'); err != nil {
		return err
	}
	seenSources := false
	for decoder.More() {
		key, err := consumePortableJSONKey(decoder)
		if err != nil {
			return err
		}
		if key != "sources" {
			if err := skipPortableJSONValue(decoder, limits.JSONDepth); err != nil {
				return err
			}
			continue
		}
		if seenSources {
			return errPortableKnowledgeStructuralLimit
		}
		seenSources = true
		if err := scanPortableJSONArray(decoder, limits.Sources, func() error {
			return skipPortableJSONValue(decoder, limits.JSONDepth)
		}); err != nil {
			return err
		}
	}
	return consumePortableJSONClosing(decoder, '}')
}

func scanPortableJSONArray(decoder *json.Decoder, limit int, scanElement func() error) error {
	if err := consumePortableJSONOpening(decoder, '['); err != nil {
		return err
	}
	count := 0
	for decoder.More() {
		count++
		if count > limit {
			return errPortableKnowledgeStructuralLimit
		}
		if err := scanElement(); err != nil {
			return err
		}
	}
	return consumePortableJSONClosing(decoder, ']')
}

func consumePortableJSONKey(decoder *json.Decoder) (string, error) {
	token, err := decoder.Token()
	if err != nil {
		return "", err
	}
	key, ok := token.(string)
	if !ok {
		return "", errPortableKnowledgeStructuralLimit
	}
	return key, nil
}

func consumePortableJSONOpening(decoder *json.Decoder, expected json.Delim) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != expected {
		return errPortableKnowledgeStructuralLimit
	}
	return nil
}

func consumePortableJSONClosing(decoder *json.Decoder, expected json.Delim) error {
	return consumePortableJSONOpening(decoder, expected)
}

func skipPortableJSONValue(decoder *json.Decoder, maximumDepth int) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, nested := token.(json.Delim)
	if !nested {
		return nil
	}
	if delimiter != '{' && delimiter != '[' {
		return errPortableKnowledgeStructuralLimit
	}
	depth := 1
	for depth > 0 {
		token, err = decoder.Token()
		if err != nil {
			return err
		}
		delimiter, nested = token.(json.Delim)
		if !nested {
			continue
		}
		switch delimiter {
		case '{', '[':
			depth++
			if depth > maximumDepth {
				return errPortableKnowledgeStructuralLimit
			}
		case '}', ']':
			depth--
		}
	}
	return nil
}

func validatePortableKnowledgeBundleSize(manifest domain.PortableKnowledgeBundleManifest) error {
	manifestJSON, err := canonicalJSONLine(manifest)
	if err != nil {
		return invalidKnowledgeBundle("manifest cannot be canonically encoded")
	}
	if len(manifestJSON) > maximumKnowledgeBundleFileBytes || manifest.Rendering.ByteSize > maximumKnowledgeBundleFileBytes {
		return invalidKnowledgeBundle("canonical bundle files exceed the 64 MiB limit")
	}
	return nil
}

func validatePortableKnowledgeSnapshot(snapshot domain.PortableKnowledgeSnapshot) error {
	if !validPortableID(snapshot.Scope.WorkspaceID, "ws_", 35) || !validPortableID(snapshot.Scope.ProjectID, "prj_", 36) ||
		!workspaceNamePattern.MatchString(snapshot.Scope.WorkspaceName) || !workspaceNamePattern.MatchString(snapshot.Scope.ProjectName) {
		return invalidKnowledgeBundle("snapshot scope IDs or names are invalid")
	}
	if snapshot.TaskScopeAnchors == nil || snapshot.Items == nil || snapshot.Contradictions == nil {
		return invalidKnowledgeBundle("snapshot collections must be JSON arrays")
	}
	if len(snapshot.TaskScopeAnchors) > maximumPortableKnowledgeItems || len(snapshot.Items) > maximumPortableKnowledgeItems || len(snapshot.Contradictions) > maximumPortableKnowledgeContradictions {
		return invalidKnowledgeBundle("snapshot exceeds item or contradiction limits")
	}
	if snapshot.Counts.TaskScopeAnchors != int64(len(snapshot.TaskScopeAnchors)) || snapshot.Counts.Items != int64(len(snapshot.Items)) ||
		snapshot.Counts.Contradictions != int64(len(snapshot.Contradictions)) {
		return invalidKnowledgeBundle("snapshot counts do not match arrays")
	}
	anchors := make(map[string]domain.PortableKnowledgeTaskScopeAnchor, len(snapshot.TaskScopeAnchors))
	usedAnchors := make(map[string]struct{}, len(snapshot.TaskScopeAnchors))
	last := ""
	for _, anchor := range snapshot.TaskScopeAnchors {
		if anchor.TaskID <= last || !validPortableID(anchor.TaskID, "task_", 37) || anchor.WorkspaceID != snapshot.Scope.WorkspaceID ||
			anchor.ProjectID != snapshot.Scope.ProjectID || !canonicalTimestamp(anchor.CreatedAt) || !validKnowledgeText(anchor.CreatedBy, 128) {
			return invalidKnowledgeBundle("task-scope anchors are not canonical or valid")
		}
		last, anchors[anchor.TaskID] = anchor.TaskID, anchor
	}
	revisions := make(map[string]domain.KnowledgeRevision)
	items := make(map[string]domain.KnowledgeItem)
	revisionCount := 0
	last = ""
	for _, portableItem := range snapshot.Items {
		item := portableItem.Item
		if item.ID <= last || !validPortableID(item.ID, "know_", 37) || item.WorkspaceID != snapshot.Scope.WorkspaceID || item.ProjectID != snapshot.Scope.ProjectID ||
			!domain.ValidKnowledgeType(item.Type) || !canonicalTimestamp(item.CreatedAt) || !validKnowledgeText(item.CreatedBy, 128) || !domain.ValidKnowledgeActorType(item.CreatedByType) {
			return invalidKnowledgeBundle("knowledge items are not canonical or valid")
		}
		if item.TaskScopeID != "" {
			if _, ok := anchors[item.TaskScopeID]; !ok {
				return invalidKnowledgeBundle("task-scoped item has no exact anchor")
			}
			usedAnchors[item.TaskScopeID] = struct{}{}
		}
		if len(portableItem.Revisions) == 0 {
			return invalidKnowledgeBundle("knowledge item has no revisions")
		}
		firstRevision := portableItem.Revisions[0]
		if item.CreatedAt != firstRevision.ProposedAt || item.CreatedBy != firstRevision.ProposedBy || item.CreatedByType != firstRevision.ProposedByType {
			return invalidKnowledgeBundle("knowledge item creation must equal its first proposal")
		}
		last = item.ID
		items[item.ID] = item
		for index, revision := range portableItem.Revisions {
			revisionCount++
			if revisionCount > maximumPortableKnowledgeRevisions || revision.ItemID != item.ID || revision.WorkspaceID != item.WorkspaceID ||
				revision.ProjectID != item.ProjectID || revision.TaskScopeID != item.TaskScopeID || revision.Type != item.Type || revision.RevisionNumber != int64(index+1) ||
				!validatePortableKnowledgeRevision(revision) {
				return invalidKnowledgeBundle("knowledge revisions are not canonical or valid")
			}
			if _, duplicate := revisions[revision.ID]; duplicate {
				return invalidKnowledgeBundle("knowledge revision IDs must be globally distinct")
			}
			if !validPortableCuratorShape(item, revision) {
				return invalidKnowledgeBundle("curator-described knowledge revision has an unreachable canonical shape")
			}
			revisions[revision.ID] = revision
		}
		if err := validatePortableKnowledgeItemGraph(portableItem.Revisions); err != nil {
			return err
		}
	}
	if snapshot.Counts.Revisions != int64(revisionCount) {
		return invalidKnowledgeBundle("snapshot revision count does not match arrays")
	}
	if len(usedAnchors) != len(anchors) {
		return invalidKnowledgeBundle("task-scope anchors must exactly equal the item applicability scopes")
	}
	last = ""
	pairs := make(map[string]struct{}, len(snapshot.Contradictions))
	for _, contradiction := range snapshot.Contradictions {
		if contradiction.ID <= last || !validatePortableContradiction(contradiction, snapshot.Scope, revisions, items) {
			return invalidKnowledgeBundle("portable contradictions are not canonical or valid")
		}
		pair := contradiction.LeftRevisionID + "\x00" + contradiction.RightRevisionID
		if _, duplicate := pairs[pair]; duplicate {
			return invalidKnowledgeBundle("contradiction revision pairs must be unique")
		}
		pairs[pair] = struct{}{}
		last = contradiction.ID
	}
	return nil
}

func validPortableCuratorShape(item domain.KnowledgeItem, revision domain.KnowledgeRevision) bool {
	isCuratorProposal := revision.ProposedBy == domain.CuratorActorID && revision.ProposedByType == domain.KnowledgeActorSubsystem
	isCuratorAcceptance := revision.AcceptedBy == domain.CuratorActorID && revision.AcceptedByType == domain.KnowledgeActorSubsystem
	if !isCuratorProposal && !isCuratorAcceptance {
		return true
	}
	if !isCuratorProposal || item.TaskScopeID != "" || item.Type != domain.KnowledgeTypeDecision || revision.RevisionNumber != 1 ||
		revision.SupersedesRevisionID != "" || revision.Confidence != domain.KnowledgeConfidenceMedium ||
		revision.VerificationStatus != domain.KnowledgeVerificationSupported || revision.FreshnessPolicy != domain.KnowledgeFreshUntilSuperseded ||
		revision.FreshUntil != "" || !validCuratorCopyText(revision.Title, maximumKnowledgeTitleBytes) ||
		!validCuratorCopyText(revision.Body, maximumCuratorSummaryBytes) || len(revision.Sources) != 1 ||
		isCuratorAcceptance && revision.DecisionNote != curatorAutoAcceptanceNote {
		return false
	}
	source := revision.Sources[0]
	return source.Ordinal == 0 && source.Type == domain.KnowledgeSourceMeetingProposal && source.Revision == curatorSourceRevision &&
		source.Role == domain.KnowledgeSourcePrimary
}

func validatePortableKnowledgeRevision(revision domain.KnowledgeRevision) bool {
	if !validPortableID(revision.ID, "krev_", 37) || !validPortableTrimmedText(revision.Title, maximumKnowledgeTitleBytes) ||
		!validPortableTrimmedText(revision.Body, maximumKnowledgeBodyBytes) || revision.ContentHash != knowledgeContentHash(revision.Title, revision.Body) ||
		!domain.ValidKnowledgeReviewStatus(revision.ReviewStatus) || !domain.ValidKnowledgeCurrencyStatus(revision.CurrencyStatus) ||
		!domain.ValidKnowledgeConfidence(revision.Confidence) || !domain.ValidKnowledgeVerification(revision.VerificationStatus) ||
		!domain.ValidKnowledgeFreshnessPolicy(revision.FreshnessPolicy) || !canonicalTimestamp(revision.ProposedAt) ||
		!validKnowledgeText(revision.ProposedBy, 128) || !validPortableProposalActor(revision.ProposedBy, revision.ProposedByType) {
		return false
	}
	if revision.FreshnessPolicy == domain.KnowledgeFreshUntilSuperseded && revision.FreshUntil != "" ||
		revision.FreshnessPolicy == domain.KnowledgeFreshExpiresAt && !validPortableFreshUntil(revision.FreshUntil) {
		return false
	}
	if revision.FreshnessPolicy == domain.KnowledgeFreshExpiresAt && !timestampAfter(revision.FreshUntil, revision.ProposedAt) {
		return false
	}
	if revision.Sources == nil || len(revision.Sources) == 0 || len(revision.Sources) > maximumKnowledgeSources {
		return false
	}
	primary := 0
	seen := make(map[string]struct{}, len(revision.Sources))
	for index, source := range revision.Sources {
		if source.Ordinal != int64(index) || !domain.ValidKnowledgeSourceType(source.Type) || !domain.ValidKnowledgeSourceRole(source.Role) ||
			!validPortableKnowledgeSourceID(source.Type, source.ID) || source.Revision < 1 ||
			source.Type == domain.KnowledgeSourceMeetingProposal && source.Revision != curatorSourceRevision {
			return false
		}
		key := source.Type + "\x00" + source.ID
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
		if source.Role == domain.KnowledgeSourcePrimary {
			primary++
		}
	}
	if primary != 1 {
		return false
	}
	return validPortableRevisionLifecycle(revision)
}

func validPortableRevisionLifecycle(r domain.KnowledgeRevision) bool {
	actorFields := func(at, by, typ string) bool {
		return canonicalTimestamp(at) && validKnowledgeText(by, 128) && domain.ValidKnowledgeActorType(typ)
	}
	noneAccepted := r.AcceptedAt == "" && r.AcceptedBy == "" && r.AcceptedByType == ""
	noneRejected := r.RejectedAt == "" && r.RejectedBy == "" && r.RejectedByType == ""
	noneStale := r.StaleAt == "" && r.StaleBy == "" && r.StaleByType == "" && r.StaleReason == ""
	switch {
	case r.ReviewStatus == domain.KnowledgeReviewProposed && r.CurrencyStatus == domain.KnowledgeCurrencyPending:
		return r.StateRevision == 1 && noneAccepted && noneRejected && noneStale && r.DecisionNote == ""
	case r.ReviewStatus == domain.KnowledgeReviewRejected && r.CurrencyStatus == domain.KnowledgeCurrencyPending:
		return r.StateRevision == 2 && noneAccepted && noneStale && actorFields(r.RejectedAt, r.RejectedBy, r.RejectedByType) &&
			ownerActor(r.RejectedBy, r.RejectedByType) && timestampNotBefore(r.RejectedAt, r.ProposedAt) && validOptionalKnowledgeNote(r.DecisionNote)
	case r.ReviewStatus == domain.KnowledgeReviewAccepted && r.CurrencyStatus == domain.KnowledgeCurrencyCurrent:
		return r.StateRevision == 2 && actorFields(r.AcceptedAt, r.AcceptedBy, r.AcceptedByType) && timestampNotBefore(r.AcceptedAt, r.ProposedAt) &&
			validPortableAcceptanceActor(r.AcceptedBy, r.AcceptedByType) && noneRejected && noneStale && validOptionalKnowledgeNote(r.DecisionNote)
	case r.ReviewStatus == domain.KnowledgeReviewAccepted && (r.CurrencyStatus == domain.KnowledgeCurrencyStale || r.CurrencyStatus == domain.KnowledgeCurrencySuperseded):
		if r.StateRevision != 3 || !actorFields(r.AcceptedAt, r.AcceptedBy, r.AcceptedByType) || !timestampNotBefore(r.AcceptedAt, r.ProposedAt) ||
			!validPortableAcceptanceActor(r.AcceptedBy, r.AcceptedByType) || !noneRejected || !validOptionalKnowledgeNote(r.DecisionNote) {
			return false
		}
		if r.CurrencyStatus == domain.KnowledgeCurrencyStale {
			return actorFields(r.StaleAt, r.StaleBy, r.StaleByType) && ownerActor(r.StaleBy, r.StaleByType) && timestampNotBefore(r.StaleAt, r.AcceptedAt) && validPortableTrimmedText(r.StaleReason, maximumKnowledgeDecisionBytes)
		}
		return noneStale
	}
	return false
}

func validatePortableKnowledgeItemGraph(revisions []domain.KnowledgeRevision) error {
	byID := make(map[string]domain.KnowledgeRevision, len(revisions))
	liveSuccessors := make(map[string]int, len(revisions))
	liveProposals, liveCurrent := 0, 0
	for _, revision := range revisions {
		byID[revision.ID] = revision
		if revision.ReviewStatus == domain.KnowledgeReviewProposed {
			liveProposals++
		}
		if revision.ReviewStatus == domain.KnowledgeReviewAccepted && revision.CurrencyStatus == domain.KnowledgeCurrencyCurrent {
			liveCurrent++
		}
		if revision.SupersedesRevisionID != "" && (revision.ReviewStatus == domain.KnowledgeReviewProposed || revision.ReviewStatus == domain.KnowledgeReviewAccepted) {
			liveSuccessors[revision.SupersedesRevisionID]++
		}
	}
	if liveProposals > 1 || liveCurrent > 1 {
		return invalidKnowledgeBundle("knowledge item has multiple live revisions")
	}
	for _, count := range liveSuccessors {
		if count > 1 {
			return invalidKnowledgeBundle("knowledge item has multiple live successors")
		}
	}
	for index, revision := range revisions {
		if revision.ReviewStatus == domain.KnowledgeReviewProposed && index != len(revisions)-1 {
			return invalidKnowledgeBundle("a proposed knowledge revision must be the item's latest proposal")
		}
		if index > 0 {
			previous := revisions[index-1]
			if !timestampNotBefore(revision.ProposedAt, previous.ProposedAt) ||
				(previous.ReviewStatus == domain.KnowledgeReviewRejected && !timestampNotBefore(revision.ProposedAt, previous.RejectedAt)) {
				return invalidKnowledgeBundle("knowledge revision creation order is not chronologically reachable")
			}
		}
		if index == 0 && revision.SupersedesRevisionID != "" || index > 0 && revision.SupersedesRevisionID == "" {
			return invalidKnowledgeBundle("knowledge supersession chain is not contiguous")
		}
		if revision.SupersedesRevisionID != "" {
			predecessor, ok := byID[revision.SupersedesRevisionID]
			if !ok || predecessor.RevisionNumber >= revision.RevisionNumber || predecessor.ReviewStatus != domain.KnowledgeReviewAccepted ||
				!timestampNotBefore(revision.ProposedAt, predecessor.AcceptedAt) || !portableRevisionCurrentAt(predecessor, revision.ProposedAt, byID) {
				return invalidKnowledgeBundle("knowledge supersession leaves its item or cycles")
			}
		}
	}
	acceptedSuccessors := make(map[string][]domain.KnowledgeRevision)
	for _, revision := range revisions {
		if revision.SupersedesRevisionID != "" && revision.ReviewStatus == domain.KnowledgeReviewAccepted {
			acceptedSuccessors[revision.SupersedesRevisionID] = append(acceptedSuccessors[revision.SupersedesRevisionID], revision)
		}
	}
	for _, revision := range revisions {
		successors := acceptedSuccessors[revision.ID]
		if revision.CurrencyStatus == domain.KnowledgeCurrencySuperseded && len(successors) != 1 {
			return invalidKnowledgeBundle("superseded knowledge revision requires one accepted successor")
		}
		if revision.CurrencyStatus != domain.KnowledgeCurrencySuperseded && len(successors) != 0 {
			return invalidKnowledgeBundle("accepted successor requires a superseded predecessor")
		}
		if revision.SupersedesRevisionID != "" && revision.ReviewStatus == domain.KnowledgeReviewRejected &&
			byID[revision.SupersedesRevisionID].ReviewStatus != domain.KnowledgeReviewAccepted {
			return invalidKnowledgeBundle("rejected successor requires an accepted predecessor")
		}
		if revision.SupersedesRevisionID != "" && revision.ReviewStatus == domain.KnowledgeReviewAccepted {
			predecessor := byID[revision.SupersedesRevisionID]
			if !ownerActor(revision.AcceptedBy, revision.AcceptedByType) || !timestampNotBefore(revision.AcceptedAt, predecessor.AcceptedAt) {
				return invalidKnowledgeBundle("accepted successor cannot predate its predecessor acceptance")
			}
		}
		if revision.SupersedesRevisionID != "" {
			for _, acceptedSuccessor := range acceptedSuccessors[revision.SupersedesRevisionID] {
				if revision.RevisionNumber > acceptedSuccessor.RevisionNumber && revision.ID != acceptedSuccessor.ID {
					return invalidKnowledgeBundle("knowledge proposal targets a predecessor after its accepted successor")
				}
			}
		}
	}
	return nil
}

func validatePortableContradiction(c domain.PortableKnowledgeContradiction, scope domain.PortableKnowledgeScope,
	revisions map[string]domain.KnowledgeRevision, items map[string]domain.KnowledgeItem,
) bool {
	left, lok := revisions[c.LeftRevisionID]
	right, rok := revisions[c.RightRevisionID]
	if !validPortableID(c.ID, "kcon_", 37) || c.WorkspaceID != scope.WorkspaceID || c.ProjectID != scope.ProjectID ||
		!lok || !rok || c.LeftRevisionID >= c.RightRevisionID || left.ItemID == right.ItemID ||
		left.ReviewStatus != domain.KnowledgeReviewAccepted || right.ReviewStatus != domain.KnowledgeReviewAccepted ||
		!timestampNotBefore(c.ReportedAt, left.AcceptedAt) || !timestampNotBefore(c.ReportedAt, right.AcceptedAt) ||
		!portableRevisionCurrentAt(left, c.ReportedAt, revisions) || !portableRevisionCurrentAt(right, c.ReportedAt, revisions) ||
		!validPortableTrimmedText(c.ReportNote, maximumContradictionNoteBytes) || !canonicalTimestamp(c.ReportedAt) ||
		!validKnowledgeText(c.ReportedBy, 128) || (c.ReportedByType != domain.KnowledgeActorHuman && c.ReportedByType != domain.KnowledgeActorAgentRun) {
		return false
	}
	leftScope, rightScope := items[left.ItemID].TaskScopeID, items[right.ItemID].TaskScopeID
	if leftScope != "" && rightScope != "" && leftScope != rightScope {
		return false
	}
	if c.ReportedByType == domain.KnowledgeActorHuman && c.ReportedBy != localOwnerActorID ||
		c.ReportedByType == domain.KnowledgeActorAgentRun && !validPortableID(c.ReportedBy, "run_", 36) {
		return false
	}
	emptyConfirmed := c.ConfirmedAt == "" && c.ConfirmedBy == "" && c.ConfirmedByType == "" && c.ConfirmNote == ""
	emptyDismissed := c.DismissedAt == "" && c.DismissedBy == "" && c.DismissedByType == "" && c.DismissNote == ""
	emptyResolved := c.ResolutionReason == "" && c.ResolvedAt == "" && c.ResolvedBy == "" && c.ResolvedByType == "" && c.ResolutionNote == ""
	confirmed := canonicalTimestamp(c.ConfirmedAt) && validKnowledgeText(c.ConfirmedBy, 128) && c.ConfirmedByType == domain.KnowledgeActorHuman && validOptionalContradictionNote(c.ConfirmNote)
	dismissed := canonicalTimestamp(c.DismissedAt) && validKnowledgeText(c.DismissedBy, 128) && c.DismissedByType == domain.KnowledgeActorHuman && validOptionalContradictionNote(c.DismissNote)
	resolved := canonicalTimestamp(c.ResolvedAt) && validKnowledgeText(c.ResolvedBy, 128) && c.ResolvedByType == domain.KnowledgeActorHuman &&
		validKnowledgeText(c.ResolutionNote, maximumContradictionNoteBytes) && (c.ResolutionReason == ContradictionResolutionParticipantStale || c.ResolutionReason == ContradictionResolutionParticipantSuperseded)
	switch c.Status {
	case domain.KnowledgeContradictionProposed:
		return c.StateRevision == 1 && emptyConfirmed && emptyDismissed && emptyResolved
	case domain.KnowledgeContradictionOpen:
		return c.StateRevision == 2 && confirmed && c.ConfirmedBy == localOwnerActorID && timestampNotBefore(c.ConfirmedAt, c.ReportedAt) && emptyDismissed && emptyResolved &&
			left.ReviewStatus == domain.KnowledgeReviewAccepted && left.CurrencyStatus == domain.KnowledgeCurrencyCurrent &&
			right.ReviewStatus == domain.KnowledgeReviewAccepted && right.CurrencyStatus == domain.KnowledgeCurrencyCurrent
	case domain.KnowledgeContradictionDismissed:
		return (c.StateRevision == 2 || c.StateRevision == 3) && dismissed && c.DismissedBy == localOwnerActorID && timestampNotBefore(c.DismissedAt, c.ReportedAt) && emptyResolved &&
			((c.StateRevision == 2 && emptyConfirmed) || (c.StateRevision == 3 && confirmed && c.ConfirmedBy == localOwnerActorID &&
				timestampNotBefore(c.ConfirmedAt, c.ReportedAt) && timestampNotBefore(c.DismissedAt, c.ConfirmedAt) &&
				portableRevisionCurrentAt(left, c.ConfirmedAt, revisions) && portableRevisionCurrentAt(right, c.ConfirmedAt, revisions) &&
				portableRevisionCurrentAt(left, c.DismissedAt, revisions) && portableRevisionCurrentAt(right, c.DismissedAt, revisions)))
	case domain.KnowledgeContradictionResolved:
		return c.StateRevision == 3 && confirmed && c.ConfirmedBy == localOwnerActorID && emptyDismissed && resolved && c.ResolvedBy == localOwnerActorID &&
			timestampNotBefore(c.ConfirmedAt, c.ReportedAt) && timestampNotBefore(c.ResolvedAt, c.ConfirmedAt) &&
			portableRevisionCurrentAt(left, c.ConfirmedAt, revisions) && portableRevisionCurrentAt(right, c.ConfirmedAt, revisions) &&
			portableRevisionCurrentAt(left, c.ResolvedAt, revisions) && portableRevisionCurrentAt(right, c.ResolvedAt, revisions) &&
			portableResolutionMatchesParticipant(c, revisions)
	}
	return false
}

func portableRevisionCurrentAt(revision domain.KnowledgeRevision, at string, revisions map[string]domain.KnowledgeRevision) bool {
	switch revision.CurrencyStatus {
	case domain.KnowledgeCurrencyCurrent:
		return true
	case domain.KnowledgeCurrencyStale:
		return !timestampAfter(at, revision.StaleAt)
	case domain.KnowledgeCurrencySuperseded:
		for _, successor := range revisions {
			if successor.SupersedesRevisionID == revision.ID && successor.ReviewStatus == domain.KnowledgeReviewAccepted {
				return !timestampAfter(at, successor.AcceptedAt)
			}
		}
	}
	return false
}

func validPortableFreshUntil(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func portableResolutionMatchesParticipant(c domain.PortableKnowledgeContradiction, revisions map[string]domain.KnowledgeRevision) bool {
	participants := []domain.KnowledgeRevision{revisions[c.LeftRevisionID], revisions[c.RightRevisionID]}
	for _, participant := range participants {
		expectedNote := "knowledge revision " + participant.ID + " became "
		if c.ResolutionReason == ContradictionResolutionParticipantStale && participant.ReviewStatus == domain.KnowledgeReviewAccepted &&
			participant.CurrencyStatus == domain.KnowledgeCurrencyStale && participant.StaleAt == c.ResolvedAt &&
			participant.StaleBy == c.ResolvedBy && participant.StaleByType == c.ResolvedByType && c.ResolutionNote == expectedNote+"stale" {
			return true
		}
		if c.ResolutionReason == ContradictionResolutionParticipantSuperseded && participant.ReviewStatus == domain.KnowledgeReviewAccepted &&
			participant.CurrencyStatus == domain.KnowledgeCurrencySuperseded && c.ResolutionNote == expectedNote+"superseded" {
			for _, successor := range revisions {
				if successor.SupersedesRevisionID == participant.ID && successor.ReviewStatus == domain.KnowledgeReviewAccepted &&
					successor.AcceptedAt == c.ResolvedAt && successor.AcceptedBy == c.ResolvedBy && successor.AcceptedByType == c.ResolvedByType {
					return true
				}
			}
		}
	}
	return false
}

func validOptionalKnowledgeNote(value string) bool {
	return value == "" || validPortableTrimmedText(value, maximumKnowledgeDecisionBytes)
}
func validOptionalContradictionNote(value string) bool {
	return value == "" || validPortableTrimmedText(value, maximumContradictionNoteBytes)
}

func validPortableTrimmedText(value string, maximum int) bool {
	return value == strings.TrimSpace(value) && validKnowledgeText(value, maximum)
}

func canonicalTimestamp(value string) bool {
	if value == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Location() == time.UTC && parsed.Format(time.RFC3339Nano) == value
}

func timestampAfter(left, right string) bool {
	leftTime, leftErr := time.Parse(time.RFC3339Nano, left)
	rightTime, rightErr := time.Parse(time.RFC3339Nano, right)
	return leftErr == nil && rightErr == nil && leftTime.After(rightTime)
}

func timestampNotBefore(left, right string) bool {
	leftTime, leftErr := time.Parse(time.RFC3339Nano, left)
	rightTime, rightErr := time.Parse(time.RFC3339Nano, right)
	return leftErr == nil && rightErr == nil && !leftTime.Before(rightTime)
}

func validPortableKnowledgeSourceID(sourceType, id string) bool {
	switch sourceType {
	case domain.KnowledgeSourceTask:
		return validPortableID(id, "task_", 37)
	case domain.KnowledgeSourceMeeting:
		return validPortableID(id, "meet_", 37)
	case domain.KnowledgeSourceMeetingProposal:
		return validPortableID(id, "proposal_", 41)
	case domain.KnowledgeSourceDomainAgent:
		return validPortableID(id, "agent_", 38)
	default:
		return false
	}
}

func validPortableAcceptanceActor(id, actorType string) bool {
	return ownerActor(id, actorType) || id == "subsystem:curator" && actorType == domain.KnowledgeActorSubsystem
}

func validPortableProposalActor(id, actorType string) bool {
	switch actorType {
	case domain.KnowledgeActorHuman:
		return id == localOwnerActorID
	case domain.KnowledgeActorAgentRun:
		return validPortableID(id, "run_", 36)
	case domain.KnowledgeActorIntegration:
		return validPortableID(id, "agent_", 38)
	case domain.KnowledgeActorSubsystem:
		return id == "subsystem:curator"
	default:
		return false
	}
}

func ownerActor(id, actorType string) bool {
	return id == localOwnerActorID && actorType == domain.KnowledgeActorHuman
}

func validLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func validPortableID(value, prefix string, length int) bool {
	if len(value) != length || !strings.HasPrefix(value, prefix) {
		return false
	}
	for _, character := range value[len(prefix):] {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func bytesSHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func identifierMatches(identifier, id, name string) bool {
	return identifier == id || identifier == name
}

func invalidKnowledgeBundle(message string) error {
	return &Error{Code: CodeInvalidKnowledgeBundle, Message: message}
}
func bundleDigestMismatch(message string) error {
	return &Error{Code: CodeKnowledgeBundleDigestMismatch, Message: message}
}
func importScopeConflict(message string) error {
	return &Error{Code: CodeKnowledgeImportScopeConflict, Message: message}
}
func importConflict(message string) error {
	return &Error{Code: CodeKnowledgeImportConflict, Message: message}
}

func lookupPortableKnowledgeImportReplay(ctx context.Context, tx *sql.Tx, command ImportKnowledgeBundleCommand,
	manifest domain.PortableKnowledgeBundleManifest, requestHash string,
) (KnowledgeBundleImportResult, bool, error) {
	row, err := dbgen.New(tx).GetPortableKnowledgeImportReceipt(ctx, dbgen.GetPortableKnowledgeImportReceiptParams{
		WorkspaceID: manifest.Snapshot.Scope.WorkspaceID, ProjectID: manifest.Snapshot.Scope.ProjectID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return KnowledgeBundleImportResult{}, false, nil
	}
	if err != nil {
		return KnowledgeBundleImportResult{}, false, storageFailure("read portable knowledge import receipt", err)
	}
	receipt := domain.KnowledgeImportReceipt{ID: row.ID, BundleID: row.BundleID, WorkspaceID: row.WorkspaceID,
		ProjectID: row.ProjectID, ContentSHA256: row.ContentSha256, RenderingSHA256: row.RenderingSha256,
		ImportedAt: row.ImportedAt, ImportedBy: row.ImportedBy, ImportedByType: row.ImportedByType,
		CompletedEventSequence: row.CompletedEventSequence}
	if row.IdempotencyKey == command.IdempotencyKey && row.RequestHash != requestHash {
		return KnowledgeBundleImportResult{}, false, &Error{Code: CodeIdempotencyConflict, Message: "idempotency key was already used for a different portable knowledge import payload"}
	}
	if !bytes.Equal(row.ManifestJson, command.ManifestJSON) || !bytes.Equal(row.Markdown, command.Markdown) ||
		receipt.BundleID != manifest.BundleID || receipt.ContentSHA256 != manifest.ContentSHA256 {
		return KnowledgeBundleImportResult{}, false, importConflict("target project already contains a different portable knowledge import")
	}
	return KnowledgeBundleImportResult{Receipt: receipt, Counts: manifest.Snapshot.Counts, EventSequence: receipt.CompletedEventSequence, Replayed: true}, true, nil
}

func ensurePortableKnowledgeImportScope(ctx context.Context, tx *sql.Tx, scope domain.PortableKnowledgeScope, create bool) (Workspace, domain.Project, KnowledgeBundleImportCreated, error) {
	created := KnowledgeBundleImportCreated{}
	queries := dbgen.New(tx)
	workspace, err := workspaceInTransaction(ctx, tx, scope.WorkspaceID)
	if err == nil {
		// Exact name is checked by caller.
	} else if ErrorCode(err) == CodeWorkspaceNotFound && create {
		now := scopeCreationTimestamp()
		workspace = Workspace{ID: scope.WorkspaceID, Name: scope.WorkspaceName, Revision: 1, CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID}
		if insertErr := queries.InsertPortableWorkspace(ctx, dbgen.InsertPortableWorkspaceParams{
			ID: workspace.ID, Name: workspace.Name, Revision: workspace.Revision, CreatedAt: workspace.CreatedAt,
			UpdatedAt: workspace.UpdatedAt, CreatedBy: workspace.CreatedBy, UpdatedBy: workspace.UpdatedBy,
		}); insertErr != nil {
			return Workspace{}, domain.Project{}, created, importScopeConflict("cannot create exact bundle workspace")
		}
		created.Workspace = true
	} else if err != nil {
		return Workspace{}, domain.Project{}, created, importScopeConflict("bundle workspace does not exist; use create-scope")
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, scope.ProjectID)
	if err == nil {
		return workspace, project, created, nil
	}
	if ErrorCode(err) != CodeProjectNotFound || !create {
		return Workspace{}, domain.Project{}, created, importScopeConflict("bundle project does not exist; use create-scope")
	}
	now := scopeCreationTimestamp()
	project = domain.Project{ID: scope.ProjectID, WorkspaceID: workspace.ID, Name: scope.ProjectName, Revision: 1,
		CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID}
	if err := queries.InsertPortableProject(ctx, dbgen.InsertPortableProjectParams{
		ID: project.ID, WorkspaceID: project.WorkspaceID, Name: project.Name, Revision: project.Revision,
		CreatedAt: project.CreatedAt, UpdatedAt: project.UpdatedAt, CreatedBy: project.CreatedBy, UpdatedBy: project.UpdatedBy,
	}); err != nil {
		return Workspace{}, domain.Project{}, created, importScopeConflict("cannot create exact bundle project")
	}
	created.Project = true
	return workspace, project, created, nil
}

func scopeCreationTimestamp() string { return "1970-01-01T00:00:00Z" }

func requireEmptyKnowledgeProject(ctx context.Context, tx *sql.Tx, workspaceID, projectID string) error {
	counts, err := dbgen.New(tx).CountPortableKnowledgeProjectState(ctx, dbgen.CountPortableKnowledgeProjectStateParams{
		TargetWorkspaceID: workspaceID, TargetProjectID: projectID,
	})
	if err != nil {
		return storageFailure("inspect portable knowledge import target", err)
	}
	if counts.ItemCount != 0 || counts.ContradictionCount != 0 || counts.ImportCount != 0 {
		return importConflict("portable knowledge import requires an empty exact project")
	}
	return nil
}

func preflightPortableKnowledgeIdentities(ctx context.Context, tx *sql.Tx, snapshot domain.PortableKnowledgeSnapshot) error {
	queries := dbgen.New(tx)
	for _, portableItem := range snapshot.Items {
		count, err := queries.CountPortableKnowledgeItemIdentity(ctx, portableItem.Item.ID)
		if err != nil {
			return storageFailure("preflight portable knowledge item identity", err)
		}
		if count != 0 {
			return importConflict("knowledge item identity already exists outside the empty target")
		}
		for _, revision := range portableItem.Revisions {
			count, err = queries.CountPortableKnowledgeRevisionIdentity(ctx, revision.ID)
			if err != nil {
				return storageFailure("preflight portable knowledge revision identity", err)
			}
			if count != 0 {
				return importConflict("knowledge revision identity already exists outside the empty target")
			}
		}
	}
	for _, contradiction := range snapshot.Contradictions {
		count, err := queries.CountPortableKnowledgeContradictionIdentity(ctx, dbgen.CountPortableKnowledgeContradictionIdentityParams{
			ID: contradiction.ID, WorkspaceID: snapshot.Scope.WorkspaceID,
			LeftRevisionID: contradiction.LeftRevisionID, RightRevisionID: contradiction.RightRevisionID,
		})
		if err != nil {
			return storageFailure("preflight portable contradiction identity", err)
		}
		if count != 0 {
			return importConflict("knowledge contradiction identity or pair already exists outside the empty target")
		}
	}
	return nil
}

func ensurePortableKnowledgeAnchors(ctx context.Context, tx *sql.Tx, scope domain.PortableKnowledgeScope, anchors []domain.PortableKnowledgeTaskScopeAnchor, create bool) (int64, error) {
	required := make(map[string]domain.PortableKnowledgeTaskScopeAnchor, len(anchors))
	for _, anchor := range anchors {
		required[anchor.TaskID] = anchor
	}
	queries := dbgen.New(tx)
	rows, err := queries.ListPortableKnowledgeTargetAnchors(ctx, dbgen.ListPortableKnowledgeTargetAnchorsParams{
		WorkspaceID: scope.WorkspaceID, ProjectID: scope.ProjectID,
	})
	if err != nil {
		return 0, storageFailure("list target task-scope anchors", err)
	}
	for _, row := range rows {
		existing := domain.PortableKnowledgeTaskScopeAnchor{TaskID: row.TaskID, WorkspaceID: row.WorkspaceID,
			ProjectID: row.ProjectID, CreatedAt: row.CreatedAt, CreatedBy: row.CreatedBy}
		expected, ok := required[existing.TaskID]
		if !ok || existing != expected {
			return 0, importScopeConflict("target project has a task-scope anchor outside the exact bundle set")
		}
	}
	var created int64
	for _, anchor := range anchors {
		task, taskErr := queries.GetPortableTaskIdentity(ctx, anchor.TaskID)
		if taskErr == nil && (task.WorkspaceID != anchor.WorkspaceID || task.ProjectID != anchor.ProjectID || task.CreatedAt != anchor.CreatedAt || task.CreatedBy != anchor.CreatedBy) {
			return 0, importScopeConflict("live task with bundle anchor ID has different identity metadata")
		}
		if taskErr != nil && !errors.Is(taskErr, sql.ErrNoRows) {
			return 0, storageFailure("read live task for portable anchor", taskErr)
		}
		existing, err := queries.GetPortableKnowledgeAnchorIdentity(ctx, anchor.TaskID)
		if err == nil {
			if existing.WorkspaceID != anchor.WorkspaceID || existing.ProjectID != anchor.ProjectID || existing.CreatedAt != anchor.CreatedAt || existing.CreatedBy != anchor.CreatedBy {
				return 0, importScopeConflict("existing task-scope anchor differs from bundle")
			}
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, storageFailure("read portable task-scope anchor", err)
		}
		if !create {
			return 0, importScopeConflict("bundle task-scope anchor is missing; use create-scope")
		}
		if err := queries.InsertPortableKnowledgeAnchor(ctx, dbgen.InsertPortableKnowledgeAnchorParams{
			TaskID: anchor.TaskID, WorkspaceID: anchor.WorkspaceID, ProjectID: anchor.ProjectID,
			CreatedAt: anchor.CreatedAt, CreatedBy: anchor.CreatedBy,
		}); err != nil {
			return 0, storageFailure("insert portable task-scope anchor", err)
		}
		created++
	}
	return created, nil
}

func (s *Store) insertPortableKnowledgeSnapshot(ctx context.Context, tx *sql.Tx, importID string,
	snapshot domain.PortableKnowledgeSnapshot, command ImportKnowledgeBundleCommand, now string,
) (int64, error) {
	lastSequence := int64(0)
	queries := dbgen.New(tx)
	for _, anchor := range snapshot.TaskScopeAnchors {
		if err := queries.InsertPortableKnowledgeImportEntity(ctx, dbgen.InsertPortableKnowledgeImportEntityParams{
			ImportID: importID, EntityType: "task_scope_anchor", EntityID: anchor.TaskID, ImportedAt: now,
		}); err != nil {
			return 0, storageFailure("audit imported task-scope anchor", err)
		}
	}
	for _, portableItem := range snapshot.Items {
		item := portableItem.Item
		if err := queries.InsertKnowledgeItem(ctx, dbgen.InsertKnowledgeItemParams{
			ID: item.ID, WorkspaceID: item.WorkspaceID, ProjectID: item.ProjectID, Type: item.Type,
			CreatedAt: item.CreatedAt, CreatedBy: item.CreatedBy, CreatedByType: item.CreatedByType,
		}); err != nil {
			return 0, storageFailure("insert imported knowledge item", err)
		}
		if item.TaskScopeID != "" {
			if err := queries.InsertKnowledgeItemTaskScope(ctx, dbgen.InsertKnowledgeItemTaskScopeParams{ItemID: item.ID, TaskID: item.TaskScopeID}); err != nil {
				return 0, storageFailure("bind imported knowledge task scope", err)
			}
		}
		if err := queries.InsertPortableKnowledgeImportEntity(ctx, dbgen.InsertPortableKnowledgeImportEntityParams{
			ImportID: importID, EntityType: "knowledge_item", EntityID: item.ID, ImportedAt: now,
		}); err != nil {
			return 0, storageFailure("audit imported knowledge item", err)
		}
		for _, revision := range portableItem.Revisions {
			if err := insertPortableKnowledgeRevision(ctx, queries, revision); err != nil {
				return 0, err
			}
			sequence, err := appendEventForActor(ctx, tx, snapshot.Scope.WorkspaceID, "knowledge_revision", revision.ID, revision.StateRevision,
				knowledgeImportedEvent, command.CorrelationID, now, command.Actor.ID, command.Actor.Type,
				map[string]any{"bundle_import_id": importID, "project_id": snapshot.Scope.ProjectID, "item_id": item.ID,
					"revision_number": revision.RevisionNumber, "review_status": revision.ReviewStatus, "currency_status": revision.CurrencyStatus})
			if err != nil {
				return 0, err
			}
			lastSequence = sequence
			if err := queries.InsertPortableKnowledgeImportEntity(ctx, dbgen.InsertPortableKnowledgeImportEntityParams{
				ImportID: importID, EntityType: "knowledge_revision", EntityID: revision.ID, EventSequence: &sequence, ImportedAt: now,
			}); err != nil {
				return 0, storageFailure("audit imported knowledge revision", err)
			}
		}
	}
	for _, contradiction := range snapshot.Contradictions {
		sequence, err := appendEventForActor(ctx, tx, snapshot.Scope.WorkspaceID, "knowledge_contradiction", contradiction.ID, contradiction.StateRevision,
			contradictionImportedEvent, command.CorrelationID, now, command.Actor.ID, command.Actor.Type,
			map[string]any{"bundle_import_id": importID, "project_id": snapshot.Scope.ProjectID,
				"left_revision_id": contradiction.LeftRevisionID, "right_revision_id": contradiction.RightRevisionID,
				"status": contradiction.Status, "state_revision": contradiction.StateRevision})
		if err != nil {
			return 0, err
		}
		lastSequence = sequence
		if err := insertPortableKnowledgeContradiction(ctx, queries, contradiction, sequence); err != nil {
			return 0, err
		}
		if err := queries.InsertPortableKnowledgeImportEntity(ctx, dbgen.InsertPortableKnowledgeImportEntityParams{
			ImportID: importID, EntityType: "knowledge_contradiction", EntityID: contradiction.ID, EventSequence: &sequence, ImportedAt: now,
		}); err != nil {
			return 0, storageFailure("audit imported knowledge contradiction", err)
		}
	}
	return lastSequence, nil
}

func insertPortableKnowledgeRevision(ctx context.Context, queries *dbgen.Queries, revision domain.KnowledgeRevision) error {
	if err := queries.InsertPortableKnowledgeRevision(ctx, dbgen.InsertPortableKnowledgeRevisionParams{
		ID: revision.ID, ItemID: revision.ItemID, RevisionNumber: revision.RevisionNumber, StateRevision: revision.StateRevision,
		Title: revision.Title, Body: revision.Body, ContentHash: revision.ContentHash, ReviewStatus: revision.ReviewStatus,
		CurrencyStatus: revision.CurrencyStatus, Confidence: revision.Confidence, VerificationStatus: revision.VerificationStatus,
		FreshnessPolicy: revision.FreshnessPolicy, FreshUntil: optionalStringPointer(revision.FreshUntil),
		SupersedesRevisionID: optionalStringPointer(revision.SupersedesRevisionID), ProposedAt: revision.ProposedAt,
		ProposedBy: revision.ProposedBy, ProposedByType: revision.ProposedByType, AcceptedAt: optionalStringPointer(revision.AcceptedAt),
		AcceptedBy: optionalStringPointer(revision.AcceptedBy), AcceptedByType: optionalStringPointer(revision.AcceptedByType),
		RejectedAt: optionalStringPointer(revision.RejectedAt), RejectedBy: optionalStringPointer(revision.RejectedBy),
		RejectedByType: optionalStringPointer(revision.RejectedByType), StaleAt: optionalStringPointer(revision.StaleAt),
		StaleBy: optionalStringPointer(revision.StaleBy), StaleByType: optionalStringPointer(revision.StaleByType),
		DecisionNote: optionalStringPointer(revision.DecisionNote), StaleReason: optionalStringPointer(revision.StaleReason),
	}); err != nil {
		return storageFailure("insert imported knowledge revision", err)
	}
	for _, source := range revision.Sources {
		if err := queries.InsertKnowledgeSource(ctx, dbgen.InsertKnowledgeSourceParams{
			RevisionID: revision.ID, Ordinal: source.Ordinal, SourceType: source.Type, SourceID: source.ID,
			SourceRevision: source.Revision, Role: source.Role,
		}); err != nil {
			return storageFailure("insert imported knowledge source", err)
		}
	}
	return nil
}

func insertPortableKnowledgeContradiction(ctx context.Context, queries *dbgen.Queries, c domain.PortableKnowledgeContradiction, sequence int64) error {
	// A single local contradiction.imported event supplies required linkage for
	// every populated lifecycle sequence column. The portable snapshot never
	// exposes these local numeric values.
	var confirmSequence, dismissSequence, resolutionSequence, causeSequence *int64
	if c.ConfirmedAt != "" {
		confirmSequence = &sequence
	}
	if c.DismissedAt != "" {
		dismissSequence = &sequence
	}
	if c.ResolvedAt != "" {
		resolutionSequence, causeSequence = &sequence, &sequence
	}
	if err := queries.InsertPortableKnowledgeContradiction(ctx, dbgen.InsertPortableKnowledgeContradictionParams{
		ID: c.ID, WorkspaceID: c.WorkspaceID, ProjectID: c.ProjectID, LeftRevisionID: c.LeftRevisionID,
		RightRevisionID: c.RightRevisionID, Status: c.Status, StateRevision: c.StateRevision, ReportNote: c.ReportNote,
		ReportedAt: c.ReportedAt, ReportedBy: c.ReportedBy, ReportedByType: c.ReportedByType, DetectedEventSequence: sequence,
		ConfirmedAt: optionalStringPointer(c.ConfirmedAt), ConfirmedBy: optionalStringPointer(c.ConfirmedBy),
		ConfirmedByType: optionalStringPointer(c.ConfirmedByType), ConfirmNote: optionalStringPointer(c.ConfirmNote),
		ConfirmEventSequence: confirmSequence, DismissedAt: optionalStringPointer(c.DismissedAt),
		DismissedBy: optionalStringPointer(c.DismissedBy), DismissedByType: optionalStringPointer(c.DismissedByType),
		DismissNote: optionalStringPointer(c.DismissNote), DismissEventSequence: dismissSequence,
		ResolutionReason: optionalStringPointer(c.ResolutionReason), ResolvedAt: optionalStringPointer(c.ResolvedAt),
		ResolvedBy: optionalStringPointer(c.ResolvedBy), ResolvedByType: optionalStringPointer(c.ResolvedByType),
		ResolutionNote: optionalStringPointer(c.ResolutionNote), ResolutionEventSequence: resolutionSequence,
		ResolutionCauseEventSequence: causeSequence,
	}); err != nil {
		return storageFailure("insert imported knowledge contradiction", err)
	}
	return nil
}
