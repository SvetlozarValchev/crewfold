package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"crewfold/internal/domain"
)

const knowledgeSearchTableDDL = `CREATE VIRTUAL TABLE knowledge_search USING fts5(
revision_id UNINDEXED, workspace_id UNINDEXED, title, body,
tokenize = 'unicode61 remove_diacritics 2')`

const knowledgeSearchMetadataTableDDL = `CREATE TABLE knowledge_search_metadata (
    singleton INTEGER PRIMARY KEY CHECK (singleton = 1),
    generation INTEGER NOT NULL CHECK (generation > 0),
    built_at TEXT NOT NULL,
    source_event_sequence INTEGER NOT NULL CHECK (source_event_sequence >= 0),
    source_count INTEGER NOT NULL CHECK (source_count >= 0),
    source_digest TEXT NOT NULL CHECK (
        length(source_digest) = 64 AND source_digest NOT GLOB '*[^0-9a-f]*'
    )
) STRICT`

type knowledgeIndexSource struct {
	Count         int64
	Digest        string
	EventSequence int64
}

type knowledgeSearchCandidate struct {
	RevisionID       string
	ScopeRank        int
	ProvenanceRank   int
	MatchedSourceIDs []string
	FreshnessClass   int
	FreshUntil       string
	ConfidenceRank   int
	VerificationRank int
	BM25             float64
	AcceptedAt       string
}

func (s *Store) SearchKnowledge(ctx context.Context, query SearchKnowledgeQuery) (domain.KnowledgeSearchResult, error) {
	query.WorkspaceIdentifier = strings.TrimSpace(query.WorkspaceIdentifier)
	query.ProjectIdentifier = strings.TrimSpace(query.ProjectIdentifier)
	query.TaskIdentifier = strings.TrimSpace(query.TaskIdentifier)
	query.Type = strings.TrimSpace(query.Type)
	query.Query = strings.TrimSpace(query.Query)
	if query.Limit == 0 {
		query.Limit = 20
	}
	if query.WorkspaceIdentifier == "" || query.ProjectIdentifier == "" || query.Limit < 1 || query.Limit > 100 || (query.Type != "" && !domain.ValidKnowledgeType(query.Type)) {
		return domain.KnowledgeSearchResult{}, &Error{Code: CodeInvalidKnowledge, Message: "knowledge search requires workspace, project, a valid optional type, and limit from 1 to 100"}
	}
	normalizedQuery, err := normalizeKnowledgeSearchQuery(query.Query)
	if err != nil {
		return domain.KnowledgeSearchResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.KnowledgeSearchResult{}, storageFailure("begin knowledge search", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, query.WorkspaceIdentifier)
	if err != nil {
		return domain.KnowledgeSearchResult{}, err
	}
	project, err := projectInTransaction(ctx, tx, workspace.ID, query.ProjectIdentifier)
	if err != nil {
		return domain.KnowledgeSearchResult{}, err
	}
	var task domain.Task
	if query.TaskIdentifier != "" {
		task, err = queryTask(ctx, tx, workspace.ID, query.TaskIdentifier)
		if err != nil {
			return domain.KnowledgeSearchResult{}, err
		}
		if task.ProjectID != project.ID {
			return domain.KnowledgeSearchResult{}, &Error{Code: CodeInvalidKnowledge, Message: "knowledge search task must belong to the selected project"}
		}
	}
	status := s.knowledgeIndexStatusInTransaction(ctx, tx)
	if status.Status != domain.KnowledgeIndexOK {
		return domain.KnowledgeSearchResult{}, retrievalDegraded(status.Diagnosis)
	}
	evaluatedAt := s.nowText()
	candidates, err := searchKnowledgeCandidates(ctx, tx, workspace.ID, project.ID, task.ID, query.Type, normalizedQuery, evaluatedAt, query.Limit)
	if err != nil {
		return domain.KnowledgeSearchResult{}, err
	}
	result := domain.KnowledgeSearchResult{
		NormalizedQuery: normalizedQuery, EvaluatedAt: evaluatedAt,
		RankPolicy: domain.KnowledgeSearchRankPolicy,
		Index:      status, Matches: make([]domain.KnowledgeSearchMatch, 0, len(candidates)),
	}
	if err := tx.QueryRowContext(ctx, "SELECT CAST(COALESCE(MAX(sequence),0) AS INTEGER) FROM events").Scan(&result.CanonicalEventSequence); err != nil {
		return domain.KnowledgeSearchResult{}, storageFailure("read knowledge search event cursor", err)
	}
	for index, candidate := range candidates {
		revision, err := s.KnowledgeRevisionInTransaction(ctx, tx, workspace.ID, candidate.RevisionID)
		if err != nil {
			return domain.KnowledgeSearchResult{}, err
		}
		result.Matches = append(result.Matches, domain.KnowledgeSearchMatch{
			Ordinal: int64(index + 1), Revision: revision,
			Explanation: knowledgeSearchExplanation(revision, candidate, task.ID, evaluatedAt),
		})
	}
	return result, nil
}

func normalizeKnowledgeSearchQuery(value string) (string, error) {
	if value == "" || !utf8.ValidString(value) || len(value) > 256 || strings.ContainsRune(value, '\x00') {
		return "", &Error{Code: CodeInvalidKnowledge, Message: "knowledge search query must contain 1 to 256 UTF-8 bytes"}
	}
	chunks := strings.Fields(value)
	if len(chunks) < 1 || len(chunks) > 16 {
		return "", &Error{Code: CodeInvalidKnowledge, Message: "knowledge search query must contain 1 to 16 whitespace-delimited terms"}
	}
	return strings.Join(chunks, " "), nil
}

func compileKnowledgeSearchQuery(normalized string) string {
	chunks := strings.Fields(normalized)
	quoted := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		quoted = append(quoted, `"`+strings.ReplaceAll(chunk, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " AND ")
}

func searchKnowledgeCandidates(ctx context.Context, tx *sql.Tx, workspaceID, projectID, taskID, knowledgeType, normalizedQuery, evaluatedAt string, limit int) ([]knowledgeSearchCandidate, error) {
	// FTS5 MATCH, bm25, and virtual-table integrity are intentionally isolated
	// here; sqlc cannot model FTS5 MATCH/rank expressions. All authority, scope,
	// provenance, and freshness filters still join canonical relational records.
	rows, err := tx.QueryContext(ctx, `WITH dependency_ids(id) AS (
SELECT depends_on_task_id FROM task_dependencies WHERE task_id = ?
), ranked AS (
SELECT kr.id,
CASE WHEN ? = '' THEN 0 WHEN ki.task_scope_id = ? THEN 0 ELSE 1 END AS scope_rank,
CASE WHEN ? = '' THEN 0 ELSE COALESCE((
  SELECT MIN(CASE
    WHEN ks.source_type='task' AND ks.source_id=? AND ks.role='primary' THEN 0
    WHEN ks.source_type='task' AND ks.source_id=? AND ks.role='supporting' THEN 1
    WHEN ks.source_type='task' AND ks.source_id IN (SELECT id FROM dependency_ids) AND ks.role='primary' THEN 2
    WHEN ks.source_type='task' AND ks.source_id IN (SELECT id FROM dependency_ids) AND ks.role='supporting' THEN 3
    ELSE 4 END)
  FROM knowledge_sources ks WHERE ks.revision_id=kr.id
),4) END AS provenance_rank,
CASE kr.freshness_policy WHEN 'until_superseded' THEN 0 ELSE 1 END AS freshness_class,
COALESCE(kr.fresh_until,'') AS fresh_until,
CASE kr.confidence WHEN 'high' THEN 0 WHEN 'medium' THEN 1 ELSE 2 END AS confidence_rank,
CASE kr.verification_status WHEN 'verified' THEN 0 WHEN 'supported' THEN 1 ELSE 2 END AS verification_rank,
bm25(knowledge_search,0.0,0.0,8.0,1.0) AS text_rank, kr.accepted_at,
crewfold_timestamp_key(kr.accepted_at) AS accepted_at_key
FROM knowledge_search
JOIN knowledge_revisions kr ON kr.id=knowledge_search.revision_id
JOIN knowledge_items ki ON ki.id=kr.item_id
WHERE knowledge_search MATCH ? AND ki.workspace_id=? AND ki.project_id=?
  AND kr.review_status='accepted' AND kr.currency_status='current'
	  AND (?='' OR ki.type=?)
	  AND ((?='' AND ki.task_scope_id IS NULL) OR (?!='' AND (ki.task_scope_id IS NULL OR ki.task_scope_id=?)))
	  AND EXISTS (SELECT 1 FROM knowledge_sources primary_source WHERE primary_source.revision_id=kr.id AND primary_source.role='primary')
  AND (kr.freshness_policy='until_superseded' OR crewfold_timestamp_key(kr.fresh_until)>crewfold_timestamp_key(?))
)
SELECT id,scope_rank,provenance_rank,freshness_class,fresh_until,confidence_rank,verification_rank,text_rank,accepted_at
FROM ranked ORDER BY scope_rank,provenance_rank,freshness_class,
CASE WHEN freshness_class=1 THEN crewfold_timestamp_key(ranked."fresh_until") END DESC,
confidence_rank,verification_rank,text_rank,accepted_at_key DESC,id ASC LIMIT ?`,
		taskID, taskID, taskID, taskID, taskID, taskID, compileKnowledgeSearchQuery(normalizedQuery), workspaceID, projectID,
		knowledgeType, knowledgeType, taskID, taskID, taskID, evaluatedAt, limit)
	if err != nil {
		if retrievalMissingError(err) {
			return nil, retrievalDegraded(domain.KnowledgeIndexMissing)
		}
		return nil, retrievalDegraded(domain.KnowledgeIndexCorrupt)
	}
	defer rows.Close()
	result := make([]knowledgeSearchCandidate, 0)
	for rows.Next() {
		var candidate knowledgeSearchCandidate
		if err := rows.Scan(&candidate.RevisionID, &candidate.ScopeRank, &candidate.ProvenanceRank, &candidate.FreshnessClass,
			&candidate.FreshUntil, &candidate.ConfidenceRank, &candidate.VerificationRank, &candidate.BM25, &candidate.AcceptedAt); err != nil {
			return nil, storageFailure("scan knowledge search result", err)
		}
		matched, err := matchedKnowledgeSourceIDs(ctx, tx, candidate.RevisionID, taskID, candidate.ProvenanceRank)
		if err != nil {
			return nil, err
		}
		candidate.MatchedSourceIDs = matched
		result = append(result, candidate)
	}
	if err := rows.Err(); err != nil {
		return nil, retrievalDegraded(domain.KnowledgeIndexCorrupt)
	}
	return result, nil
}

func matchedKnowledgeSourceIDs(ctx context.Context, tx *sql.Tx, revisionID, taskID string, rank int) ([]string, error) {
	if taskID == "" || rank == 4 {
		return []string{}, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT source_id FROM knowledge_sources WHERE revision_id=? AND source_type='task' AND (
(?=0 AND source_id=? AND role='primary') OR (?=1 AND source_id=? AND role='supporting') OR
(?=2 AND role='primary' AND source_id IN (SELECT depends_on_task_id FROM task_dependencies WHERE task_id=?)) OR
(?=3 AND role='supporting' AND source_id IN (SELECT depends_on_task_id FROM task_dependencies WHERE task_id=?))
) ORDER BY source_id`, revisionID, rank, taskID, rank, taskID, rank, taskID, rank, taskID)
	if err != nil {
		return nil, storageFailure("query matched knowledge provenance", err)
	}
	defer rows.Close()
	result := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, storageFailure("scan matched knowledge provenance", err)
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func knowledgeSearchExplanation(revision domain.KnowledgeRevision, candidate knowledgeSearchCandidate, taskID, evaluatedAt string) domain.KnowledgeSearchExplanation {
	scopeReason := "project-wide knowledge is eligible for a project-only search"
	if taskID != "" {
		if candidate.ScopeRank == 0 {
			scopeReason = "knowledge is scoped to the exact search task"
		} else {
			scopeReason = "project-wide knowledge applies to the search task"
		}
	}
	provenanceReasons := []string{"exact task primary source", "exact task supporting source", "direct dependency primary source", "direct dependency supporting source", "no task provenance affinity"}
	provenanceReason := "task provenance affinity was not requested"
	if taskID != "" {
		provenanceReason = provenanceReasons[candidate.ProvenanceRank]
	}
	freshnessReason := "current until explicitly superseded or marked stale"
	if candidate.FreshnessClass == 1 {
		freshnessReason = "explicit freshness deadline remains after evaluation time"
	}
	return domain.KnowledgeSearchExplanation{
		Scope:      domain.KnowledgeSearchScopeExplanation{Rank: candidate.ScopeRank, Reason: scopeReason},
		Authority:  domain.KnowledgeSearchAuthorityExplanation{ReviewStatus: revision.ReviewStatus, CurrencyStatus: revision.CurrencyStatus, AcceptedByType: revision.AcceptedByType, Reason: "owner-accepted current canonical revision"},
		Freshness:  domain.KnowledgeSearchFreshnessExplanation{Class: candidate.FreshnessClass, FreshUntil: candidate.FreshUntil, EvaluatedAt: evaluatedAt, Reason: freshnessReason},
		Provenance: domain.KnowledgeSearchProvenanceExplanation{Rank: candidate.ProvenanceRank, Reason: provenanceReason, MatchedSourceIDs: candidate.MatchedSourceIDs},
		Quality:    domain.KnowledgeSearchQualityExplanation{Confidence: revision.Confidence, ConfidenceRank: candidate.ConfidenceRank, VerificationStatus: revision.VerificationStatus, VerificationRank: candidate.VerificationRank},
		Text:       domain.KnowledgeSearchTextExplanation{BM25: candidate.BM25, TitleWeight: 8, BodyWeight: 1},
		TieBreaker: domain.KnowledgeSearchTieBreaker{AcceptedAt: candidate.AcceptedAt, RevisionID: revision.ID},
	}
}

func (s *Store) KnowledgeIndexStatus(ctx context.Context, workspaceIdentifier string) (domain.KnowledgeIndexStatus, error) {
	if _, err := s.Workspace(ctx, strings.TrimSpace(workspaceIdentifier)); err != nil {
		return domain.KnowledgeIndexStatus{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.KnowledgeIndexStatus{}, storageFailure("begin knowledge index inspection", err)
	}
	defer tx.Rollback()
	status := s.knowledgeIndexStatusInTransaction(ctx, tx)
	return status, nil
}

func (s *Store) RebuildKnowledgeIndex(ctx context.Context, command RebuildKnowledgeIndexCommand) (KnowledgeIndexRebuildResult, error) {
	command.WorkspaceIdentifier = strings.TrimSpace(command.WorkspaceIdentifier)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.CorrelationID = strings.TrimSpace(command.CorrelationID)
	if command.WorkspaceIdentifier == "" {
		return KnowledgeIndexRebuildResult{}, &Error{Code: CodeInvalidKnowledge, Message: "knowledge index rebuild requires workspace"}
	}
	if err := validateMutationMetadata(command.IdempotencyKey, command.CorrelationID, CodeInvalidKnowledge); err != nil {
		return KnowledgeIndexRebuildResult{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return KnowledgeIndexRebuildResult{}, storageFailure("begin knowledge index rebuild", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, command.WorkspaceIdentifier)
	if err != nil {
		return KnowledgeIndexRebuildResult{}, err
	}
	requestHash, err := hashCommand("knowledge.index.rebuild", map[string]string{"workspace": workspace.ID})
	if err != nil {
		return KnowledgeIndexRebuildResult{}, storageFailure("hash knowledge index rebuild", err)
	}
	var replay KnowledgeIndexRebuildResult
	if found, err := lookupIdempotency(ctx, tx, command.IdempotencyKey, "knowledge.index.rebuild", requestHash, &replay); err != nil {
		return KnowledgeIndexRebuildResult{}, err
	} else if found {
		current := s.knowledgeIndexStatusInTransaction(ctx, tx)
		if current.Status != domain.KnowledgeIndexOK {
			return KnowledgeIndexRebuildResult{}, retrievalDegraded(current.Diagnosis)
		}
		if current.Generation != replay.Index.Generation || current.SourceDigest != replay.Index.SourceDigest {
			return KnowledgeIndexRebuildResult{}, &Error{Code: CodeIdempotencyConflict, Message: "knowledge index rebuild idempotency result is no longer current"}
		}
		return replay, nil
	}
	var generation int64
	_ = tx.QueryRowContext(ctx, "SELECT generation FROM knowledge_search_metadata WHERE singleton = 1").Scan(&generation)
	if err := recreateKnowledgeSearch(ctx, tx); err != nil {
		return KnowledgeIndexRebuildResult{}, err
	}
	source, err := knowledgeIndexCanonicalSource(ctx, tx)
	if err != nil {
		return KnowledgeIndexRebuildResult{}, err
	}
	if diagnosis := knowledgeIndexSchemaDiagnosis(ctx, tx); diagnosis != "" {
		return KnowledgeIndexRebuildResult{}, retrievalDegraded(diagnosis)
	}
	if diagnosis := knowledgeSearchIntegrityDiagnosis(ctx, tx); diagnosis != "" {
		return KnowledgeIndexRebuildResult{}, retrievalDegraded(diagnosis)
	}
	mismatch, err := knowledgeSearchContentMismatch(ctx, tx, source.Count)
	if err != nil {
		return KnowledgeIndexRebuildResult{}, retrievalDegraded(domain.KnowledgeIndexCorrupt)
	}
	if mismatch {
		return KnowledgeIndexRebuildResult{}, retrievalDegraded(domain.KnowledgeIndexContentMismatch)
	}
	if err := s.runMutationHook(MutationAfterProjection); err != nil {
		return KnowledgeIndexRebuildResult{}, err
	}
	now := s.nowText()
	status := domain.KnowledgeIndexStatus{Status: domain.KnowledgeIndexOK, Generation: generation + 1, BuiltAt: now, SourceEventSequence: source.EventSequence, SourceCount: source.Count, SourceDigest: source.Digest}
	if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_search_metadata(singleton,generation,built_at,source_event_sequence,source_count,source_digest)
VALUES (1,?,?,?,?,?) ON CONFLICT(singleton) DO UPDATE SET generation=excluded.generation,built_at=excluded.built_at,
source_event_sequence=excluded.source_event_sequence,source_count=excluded.source_count,source_digest=excluded.source_digest`,
		status.Generation, status.BuiltAt, status.SourceEventSequence, status.SourceCount, status.SourceDigest); err != nil {
		return KnowledgeIndexRebuildResult{}, storageFailure("record knowledge index generation", err)
	}
	result := KnowledgeIndexRebuildResult{Index: status}
	if err := recordIdempotency(ctx, tx, command.IdempotencyKey, "knowledge.index.rebuild", requestHash, result, now); err != nil {
		return KnowledgeIndexRebuildResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return KnowledgeIndexRebuildResult{}, storageFailure("commit knowledge index rebuild", err)
	}
	return result, nil
}

func recreateKnowledgeSearch(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS knowledge_search"); err != nil {
		return storageFailure("drop derived knowledge index", err)
	}
	if _, err := tx.ExecContext(ctx, "DROP TABLE IF EXISTS knowledge_search_metadata"); err != nil {
		return storageFailure("drop derived knowledge index metadata", err)
	}
	if _, err := tx.ExecContext(ctx, knowledgeSearchTableDDL); err != nil {
		return storageFailure("create derived knowledge index", err)
	}
	if _, err := tx.ExecContext(ctx, knowledgeSearchMetadataTableDDL); err != nil {
		return storageFailure("create derived knowledge index metadata", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_search(revision_id,workspace_id,title,body)
SELECT kr.id,ki.workspace_id,kr.title,kr.body FROM knowledge_revisions kr JOIN knowledge_items ki ON ki.id=kr.item_id ORDER BY kr.id`); err != nil {
		return storageFailure("populate derived knowledge index", err)
	}
	return nil
}

// refreshKnowledgeIndexAfterCanonicalMutation is deliberately best-effort. The
// FTS projection is derived and may never make a canonical knowledge mutation
// fail after that mutation committed. A later status call exposes degradation
// and an explicit rebuild repairs the projection atomically.
func (s *Store) refreshKnowledgeIndexAfterCanonicalMutation(ctx context.Context) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	status := s.knowledgeIndexStatusInTransaction(ctx, tx)
	if status.Status != domain.KnowledgeIndexDegraded || status.Diagnosis != domain.KnowledgeIndexOutOfDate {
		return
	}
	projection, err := knowledgeIndexProjectionSource(ctx, tx)
	if err != nil || projection.Count != status.SourceCount || projection.Digest != status.SourceDigest {
		return
	}
	mismatch, err := knowledgeSearchProjectionContentMismatch(ctx, tx)
	if err != nil || mismatch {
		return
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_search(revision_id,workspace_id,title,body)
SELECT kr.id,ki.workspace_id,kr.title,kr.body FROM knowledge_revisions kr JOIN knowledge_items ki ON ki.id=kr.item_id
WHERE NOT EXISTS (SELECT 1 FROM knowledge_search ks WHERE ks.revision_id=kr.id) ORDER BY kr.id`); err != nil {
		return
	}
	source, err := knowledgeIndexCanonicalSource(ctx, tx)
	if err != nil {
		return
	}
	if diagnosis := knowledgeSearchIntegrityDiagnosis(ctx, tx); diagnosis != "" {
		return
	}
	mismatch, err = knowledgeSearchContentMismatch(ctx, tx, source.Count)
	if err != nil || mismatch {
		return
	}
	if _, err := tx.ExecContext(ctx, `UPDATE knowledge_search_metadata
SET built_at=?,source_event_sequence=?,source_count=?,source_digest=? WHERE singleton=1`,
		s.nowText(), source.EventSequence, source.Count, source.Digest); err != nil {
		return
	}
	_ = tx.Commit()
}

func (s *Store) knowledgeIndexStatusInTransaction(ctx context.Context, tx *sql.Tx) domain.KnowledgeIndexStatus {
	degraded := func(diagnosis string) domain.KnowledgeIndexStatus {
		return domain.KnowledgeIndexStatus{Status: domain.KnowledgeIndexDegraded, Diagnosis: diagnosis}
	}
	var status domain.KnowledgeIndexStatus
	if err := tx.QueryRowContext(ctx, `SELECT generation,built_at,source_event_sequence,source_count,source_digest
FROM knowledge_search_metadata WHERE singleton=1`).Scan(&status.Generation, &status.BuiltAt, &status.SourceEventSequence, &status.SourceCount, &status.SourceDigest); err != nil {
		if retrievalMissingError(err) {
			return degraded(domain.KnowledgeIndexMissing)
		}
		return degraded(domain.KnowledgeIndexCorrupt)
	}
	if diagnosis := knowledgeIndexSchemaDiagnosis(ctx, tx); diagnosis != "" {
		status.Status, status.Diagnosis = domain.KnowledgeIndexDegraded, diagnosis
		return status
	}
	if _, err := time.Parse(time.RFC3339Nano, status.BuiltAt); err != nil {
		status.Status, status.Diagnosis = domain.KnowledgeIndexDegraded, domain.KnowledgeIndexCorrupt
		return status
	}
	if diagnosis := knowledgeSearchIntegrityDiagnosis(ctx, tx); diagnosis != "" {
		status.Status, status.Diagnosis = domain.KnowledgeIndexDegraded, diagnosis
		return status
	}
	source, err := knowledgeIndexCanonicalSource(ctx, tx)
	if err != nil {
		status.Status, status.Diagnosis = domain.KnowledgeIndexDegraded, domain.KnowledgeIndexCorrupt
		return status
	}
	if status.SourceEventSequence > source.EventSequence {
		status.Status, status.Diagnosis = domain.KnowledgeIndexDegraded, domain.KnowledgeIndexCorrupt
		return status
	}
	if status.SourceCount != source.Count || status.SourceDigest != source.Digest {
		status.Status, status.Diagnosis = domain.KnowledgeIndexDegraded, domain.KnowledgeIndexOutOfDate
		return status
	}
	mismatch, err := knowledgeSearchContentMismatch(ctx, tx, source.Count)
	if err != nil {
		status.Status, status.Diagnosis = domain.KnowledgeIndexDegraded, domain.KnowledgeIndexCorrupt
		return status
	}
	if mismatch {
		status.Status, status.Diagnosis = domain.KnowledgeIndexDegraded, domain.KnowledgeIndexContentMismatch
		return status
	}
	status.Status = domain.KnowledgeIndexOK
	return status
}

func knowledgeIndexSchemaDiagnosis(ctx context.Context, tx *sql.Tx) string {
	expected := []struct {
		name string
		ddl  string
	}{
		{name: "knowledge_search", ddl: knowledgeSearchTableDDL},
		{name: "knowledge_search_metadata", ddl: knowledgeSearchMetadataTableDDL},
	}
	for _, table := range expected {
		var schemaSQL string
		if err := tx.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema
WHERE type='table' AND name=?`, table.name).Scan(&schemaSQL); err != nil {
			if retrievalMissingError(err) {
				return domain.KnowledgeIndexMissing
			}
			return domain.KnowledgeIndexCorrupt
		}
		if normalizeSQLiteSchemaSQL(schemaSQL) != normalizeSQLiteSchemaSQL(table.ddl) {
			return domain.KnowledgeIndexCorrupt
		}
	}
	return ""
}

func normalizeSQLiteSchemaSQL(value string) string {
	type schemaToken struct {
		text string
		word bool
	}
	characters := []rune(value)
	tokens := make([]schemaToken, 0, len(characters)/2)
	for index := 0; index < len(characters); {
		if unicode.IsSpace(characters[index]) {
			index++
			continue
		}
		if strings.ContainsRune("(),=", characters[index]) {
			tokens = append(tokens, schemaToken{text: string(characters[index])})
			index++
			continue
		}
		if strings.ContainsRune("'\"`[", characters[index]) {
			start, opener := index, characters[index]
			closer := opener
			if opener == '[' {
				closer = ']'
			}
			index++
			for index < len(characters) {
				if characters[index] != closer {
					index++
					continue
				}
				index++
				if index < len(characters) && characters[index] == closer {
					index++
					continue
				}
				break
			}
			tokens = append(tokens, schemaToken{text: string(characters[start:index]), word: true})
			continue
		}
		start := index
		for index < len(characters) && !unicode.IsSpace(characters[index]) &&
			!strings.ContainsRune("(),='\"`[", characters[index]) {
			index++
		}
		tokens = append(tokens, schemaToken{text: string(characters[start:index]), word: true})
	}
	var normalized strings.Builder
	normalized.Grow(len(value))
	for index, token := range tokens {
		if index > 0 && token.word && tokens[index-1].word {
			normalized.WriteByte(' ')
		}
		normalized.WriteString(token.text)
	}
	return normalized.String()
}

func knowledgeSearchIntegrityDiagnosis(ctx context.Context, tx *sql.Tx) string {
	if _, err := tx.ExecContext(ctx, "INSERT INTO knowledge_search(knowledge_search) VALUES('integrity-check')"); err != nil {
		if retrievalMissingError(err) {
			return domain.KnowledgeIndexMissing
		}
		return domain.KnowledgeIndexCorrupt
	}
	return ""
}

func knowledgeSearchContentMismatch(ctx context.Context, tx *sql.Tx, sourceCount int64) (bool, error) {
	var indexedRows int64
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM knowledge_search").Scan(&indexedRows); err != nil {
		return false, err
	}
	var mismatch int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
SELECT kr.id FROM knowledge_revisions kr JOIN knowledge_items ki ON ki.id=kr.item_id
LEFT JOIN knowledge_search ks ON ks.revision_id=kr.id
WHERE ks.rowid IS NULL OR ks.workspace_id IS NOT ki.workspace_id OR ks.title IS NOT kr.title OR ks.body IS NOT kr.body
UNION ALL
SELECT ks.revision_id FROM knowledge_search ks LEFT JOIN knowledge_revisions kr ON kr.id=ks.revision_id WHERE kr.id IS NULL
)`).Scan(&mismatch); err != nil {
		return false, err
	}
	return mismatch != 0 || indexedRows != sourceCount, nil
}

func knowledgeSearchProjectionContentMismatch(ctx context.Context, tx *sql.Tx) (bool, error) {
	var mismatch int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
FROM knowledge_search ks
LEFT JOIN knowledge_revisions kr ON kr.id=ks.revision_id
LEFT JOIN knowledge_items ki ON ki.id=kr.item_id
WHERE kr.id IS NULL OR ki.id IS NULL OR ks.workspace_id IS NOT ki.workspace_id OR ks.title IS NOT kr.title OR ks.body IS NOT kr.body`).Scan(&mismatch); err != nil {
		return false, err
	}
	return mismatch != 0, nil
}

func knowledgeIndexCanonicalSource(ctx context.Context, query queryRower) (knowledgeIndexSource, error) {
	rows, err := queryRows(ctx, query, `SELECT kr.id,kr.content_hash FROM knowledge_revisions kr ORDER BY kr.id`)
	if err != nil {
		return knowledgeIndexSource{}, storageFailure("read canonical knowledge index source", err)
	}
	defer rows.Close()
	digest := sha256.New()
	var source knowledgeIndexSource
	for rows.Next() {
		var id, contentHash string
		if err := rows.Scan(&id, &contentHash); err != nil {
			return knowledgeIndexSource{}, storageFailure("scan canonical knowledge index source", err)
		}
		_, _ = digest.Write([]byte(id + "\x00" + contentHash + "\n"))
		source.Count++
	}
	if err := rows.Err(); err != nil {
		return knowledgeIndexSource{}, storageFailure("iterate canonical knowledge index source", err)
	}
	if err := query.QueryRowContext(ctx, "SELECT CAST(COALESCE(MAX(sequence),0) AS INTEGER) FROM events").Scan(&source.EventSequence); err != nil {
		return knowledgeIndexSource{}, storageFailure("read canonical event cursor", err)
	}
	source.Digest = hex.EncodeToString(digest.Sum(nil))
	return source, nil
}

func knowledgeIndexProjectionSource(ctx context.Context, query queryRower) (knowledgeIndexSource, error) {
	rows, err := queryRows(ctx, query, `SELECT ks.revision_id,kr.content_hash
FROM knowledge_search ks
JOIN knowledge_revisions kr ON kr.id=ks.revision_id
ORDER BY ks.revision_id`)
	if err != nil {
		return knowledgeIndexSource{}, storageFailure("read knowledge index projection source", err)
	}
	defer rows.Close()
	digest := sha256.New()
	var source knowledgeIndexSource
	for rows.Next() {
		var id, contentHash string
		if err := rows.Scan(&id, &contentHash); err != nil {
			return knowledgeIndexSource{}, storageFailure("scan knowledge index projection source", err)
		}
		_, _ = digest.Write([]byte(id + "\x00" + contentHash + "\n"))
		source.Count++
	}
	if err := rows.Err(); err != nil {
		return knowledgeIndexSource{}, storageFailure("iterate knowledge index projection source", err)
	}
	source.Digest = hex.EncodeToString(digest.Sum(nil))
	return source, nil
}

type queryRowsContext interface {
	queryRower
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func queryRows(ctx context.Context, query queryRower, statement string, args ...any) (*sql.Rows, error) {
	rowsQuery, ok := query.(queryRowsContext)
	if !ok {
		return nil, errors.New("query context does not support rows")
	}
	return rowsQuery.QueryContext(ctx, statement, args...)
}

func retrievalMissingError(err error) bool {
	return errors.Is(err, sql.ErrNoRows) || strings.Contains(strings.ToLower(fmt.Sprint(err)), "no such table")
}

func retrievalDegraded(diagnosis string) error {
	if diagnosis == "" {
		diagnosis = domain.KnowledgeIndexCorrupt
	}
	return &Error{Code: CodeRetrievalDegraded, Message: "knowledge retrieval is degraded: " + diagnosis}
}
