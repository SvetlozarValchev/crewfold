package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestKnowledgeSearchIsScopedRankedAndReadOnly(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	consumer := createWorkTestTask(t, storage, workspace.ID, project.ID, "consumer", "search-consumer")
	dependency := createWorkTestTask(t, storage, workspace.ID, project.ID, "dependency", "search-dependency")
	if _, err := storage.AddTaskDependency(context.Background(), AddTaskDependencyCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: consumer.Task.ID, DependsOnTaskID: dependency.Task.ID,
		ExpectedRevision: consumer.Task.Revision, IdempotencyKey: "search-dependency-edge", CorrelationID: "request-search-dependency-edge",
	}); err != nil {
		t.Fatal(err)
	}
	exact := proposeSearchKnowledge(t, storage, workspace.ID, dependency.Task.ID, consumer.Task.ID, "exact", "Contact ordering exact", "contact ordering", domain.KnowledgeConfidenceLow, domain.KnowledgeVerificationUnverified)
	projectWide := proposeSearchKnowledge(t, storage, workspace.ID, dependency.Task.ID, "", "project", "Contact ordering project", "contact ordering", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationVerified)
	for index, revision := range []KnowledgeMutationResult{exact, projectWide} {
		if _, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{
			WorkspaceIdentifier: workspace.ID, RevisionID: revision.Revision.ID, ExpectedStateRevision: 1,
			Actor: OwnerKnowledgeActor(), IdempotencyKey: "search-accept-" + string(rune('a'+index)), CorrelationID: "request-search-accept",
		}); err != nil {
			t.Fatal(err)
		}
	}
	var eventsBefore, idempotencyBefore int
	_ = storage.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&eventsBefore)
	_ = storage.db.QueryRow("SELECT COUNT(*) FROM idempotency_keys").Scan(&idempotencyBefore)
	result, err := storage.SearchKnowledge(context.Background(), SearchKnowledgeQuery{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, TaskIdentifier: consumer.Task.ID,
		Query: "contact ordering", Limit: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.NormalizedQuery != "contact ordering" || len(result.Matches) != 2 || result.Matches[0].Revision.ID != exact.Revision.ID || result.Matches[0].Explanation.Scope.Rank != 0 || result.Matches[1].Explanation.Scope.Rank != 1 {
		t.Fatalf("task search = %#v", result)
	}
	projectResult, err := storage.SearchKnowledge(context.Background(), SearchKnowledgeQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Query: "contact ordering"})
	if err != nil || len(projectResult.Matches) != 1 || projectResult.Matches[0].Revision.ID != projectWide.Revision.ID || projectResult.Matches[0].Explanation.Scope.Rank != 0 {
		t.Fatalf("project search = %#v, %v", projectResult, err)
	}
	var eventsAfter, idempotencyAfter int
	_ = storage.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&eventsAfter)
	_ = storage.db.QueryRow("SELECT COUNT(*) FROM idempotency_keys").Scan(&idempotencyAfter)
	if eventsAfter != eventsBefore || idempotencyAfter != idempotencyBefore {
		t.Fatalf("search mutated events/idempotency: %d/%d %d/%d", eventsBefore, eventsAfter, idempotencyBefore, idempotencyAfter)
	}
}

func TestKnowledgeSearchWeightsTitleAndTreatsQueryAsLiteral(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	source := createWorkTestTask(t, storage, workspace.ID, project.ID, "source", "search-weight-source")
	titleHit := proposeSearchKnowledge(t, storage, workspace.ID, source.Task.ID, "", "title-hit", "weightprobe", "ordinary text", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationVerified)
	bodyHit := proposeSearchKnowledge(t, storage, workspace.ID, source.Task.ID, "", "body-hit", "ordinary text", "weightprobe", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationVerified)
	literal := proposeSearchKnowledge(t, storage, workspace.ID, source.Task.ID, "", "literal", "OR quoted", "literal operator and quote", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationVerified)
	acceptSearchKnowledge(t, storage, workspace.ID, titleHit, "title-hit")
	acceptSearchKnowledge(t, storage, workspace.ID, bodyHit, "body-hit")
	acceptSearchKnowledge(t, storage, workspace.ID, literal, "literal")

	weighted, err := storage.SearchKnowledge(context.Background(), SearchKnowledgeQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Query: "weightprobe"})
	if err != nil || len(weighted.Matches) != 2 || weighted.Matches[0].Revision.ID != titleHit.Revision.ID || weighted.Matches[0].Explanation.Text.BM25 >= weighted.Matches[1].Explanation.Text.BM25 {
		t.Fatalf("weighted search = %#v, %v", weighted, err)
	}
	literalResult, err := storage.SearchKnowledge(context.Background(), SearchKnowledgeQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Query: "OR \"quoted\""})
	if err != nil || literalResult.NormalizedQuery != "OR \"quoted\"" || len(literalResult.Matches) != 1 || literalResult.Matches[0].Revision.ID != literal.Revision.ID {
		t.Fatalf("literal search = %#v, %v", literalResult, err)
	}
}

func TestKnowledgeSearchProvenanceRanks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		wantRank    int
		buildSource func(consumer, dependency, unrelated string) []domain.KnowledgeSourceInput
		matched     func(consumer, dependency string) string
	}{
		{
			name: "supporting exact task", wantRank: 1,
			buildSource: func(consumer, _, unrelated string) []domain.KnowledgeSourceInput {
				return []domain.KnowledgeSourceInput{
					{Type: domain.KnowledgeSourceTask, ID: unrelated, Role: domain.KnowledgeSourcePrimary},
					{Type: domain.KnowledgeSourceTask, ID: consumer, Role: domain.KnowledgeSourceSupporting},
				}
			},
			matched: func(consumer, _ string) string { return consumer },
		},
		{
			name: "primary direct dependency", wantRank: 2,
			buildSource: func(_, dependency, _ string) []domain.KnowledgeSourceInput {
				return []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: dependency, Role: domain.KnowledgeSourcePrimary}}
			},
			matched: func(_, dependency string) string { return dependency },
		},
		{
			name: "supporting direct dependency", wantRank: 3,
			buildSource: func(_, dependency, unrelated string) []domain.KnowledgeSourceInput {
				return []domain.KnowledgeSourceInput{
					{Type: domain.KnowledgeSourceTask, ID: unrelated, Role: domain.KnowledgeSourcePrimary},
					{Type: domain.KnowledgeSourceTask, ID: dependency, Role: domain.KnowledgeSourceSupporting},
				}
			},
			matched: func(_, dependency string) string { return dependency },
		},
	}
	for index, test := range tests {
		index, test := index, test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			storage := openTestStore(t, t.TempDir(), Options{})
			workspace, project := initializeWorkTestProject(t, storage)
			consumer := createWorkTestTask(t, storage, workspace.ID, project.ID, "consumer", "provenance-consumer-"+string(rune('a'+index)))
			dependency := createWorkTestTask(t, storage, workspace.ID, project.ID, "dependency", "provenance-dependency-"+string(rune('a'+index)))
			unrelated := createWorkTestTask(t, storage, workspace.ID, project.ID, "unrelated", "provenance-unrelated-"+string(rune('a'+index)))
			if _, err := storage.AddTaskDependency(context.Background(), AddTaskDependencyCommand{
				WorkspaceIdentifier: workspace.ID, TaskID: consumer.Task.ID, DependsOnTaskID: dependency.Task.ID,
				ExpectedRevision: consumer.Task.Revision, IdempotencyKey: "provenance-edge-" + string(rune('a'+index)), CorrelationID: "request-provenance-edge",
			}); err != nil {
				t.Fatal(err)
			}
			proposed := proposeCustomSearchKnowledge(t, storage, ProposeKnowledgeCommand{
				WorkspaceIdentifier: workspace.ID, Type: domain.KnowledgeTypeFinding,
				Title: "provenancerankprobe", Body: "identical body", Confidence: domain.KnowledgeConfidenceHigh,
				VerificationStatus: domain.KnowledgeVerificationVerified, FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded,
				Sources: test.buildSource(consumer.Task.ID, dependency.Task.ID, unrelated.Task.ID),
			}, "provenance-"+string(rune('a'+index)))
			accepted := acceptSearchKnowledge(t, storage, workspace.ID, proposed, "provenance-"+string(rune('a'+index)))
			result, err := storage.SearchKnowledge(context.Background(), SearchKnowledgeQuery{
				WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, TaskIdentifier: consumer.Task.ID, Query: "provenancerankprobe",
			})
			wantMatched := test.matched(consumer.Task.ID, dependency.Task.ID)
			if err != nil || len(result.Matches) != 1 || result.Matches[0].Revision.ID != accepted.Revision.ID ||
				result.Matches[0].Explanation.Provenance.Rank != test.wantRank ||
				!reflect.DeepEqual(result.Matches[0].Explanation.Provenance.MatchedSourceIDs, []string{wantMatched}) {
				t.Fatalf("provenance rank result = %#v, %v; want rank %d matched %q", result, err, test.wantRank, wantMatched)
			}
		})
	}
}

func TestKnowledgeSearchFreshnessUsesChronologicalInstantsAndStrictBoundary(t *testing.T) {
	t.Parallel()
	clock := time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return clock }})
	workspace, project := initializeWorkTestProject(t, storage)
	source := createWorkTestTask(t, storage, workspace.ID, project.ID, "source", "search-freshness-source")
	evaluatedAt := time.Date(2030, 1, 2, 3, 4, 5, 123456700, time.UTC)
	offset := time.FixedZone("test-offset", 60*60)
	boundary := proposeExpiringSearchKnowledge(t, storage, workspace.ID, source.Task.ID, "boundary", evaluatedAt.In(offset).Format(time.RFC3339Nano))
	near := proposeExpiringSearchKnowledge(t, storage, workspace.ID, source.Task.ID, "near", evaluatedAt.Add(time.Nanosecond).In(offset).Format(time.RFC3339Nano))
	far := proposeExpiringSearchKnowledge(t, storage, workspace.ID, source.Task.ID, "far", evaluatedAt.Add(2*time.Nanosecond).Format(time.RFC3339Nano))
	untilSuperseded := proposeCustomSearchKnowledge(t, storage, ProposeKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, Type: domain.KnowledgeTypeFinding,
		Title: "chronoprobe", Body: "identical freshness candidate", Confidence: domain.KnowledgeConfidenceHigh,
		VerificationStatus: domain.KnowledgeVerificationVerified, FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded,
		Sources: []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: source.Task.ID, Role: domain.KnowledgeSourcePrimary}},
	}, "until-superseded")
	acceptSearchKnowledge(t, storage, workspace.ID, boundary, "boundary")
	acceptSearchKnowledge(t, storage, workspace.ID, near, "near")
	acceptSearchKnowledge(t, storage, workspace.ID, far, "far")
	acceptSearchKnowledge(t, storage, workspace.ID, untilSuperseded, "until-superseded")
	clock = evaluatedAt

	result, err := storage.SearchKnowledge(context.Background(), SearchKnowledgeQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Query: "chronoprobe"})
	if err != nil || len(result.Matches) != 3 || result.Matches[0].Revision.ID != untilSuperseded.Revision.ID ||
		result.Matches[0].Explanation.Freshness.Class != 0 || result.Matches[1].Revision.ID != far.Revision.ID ||
		result.Matches[1].Explanation.Freshness.Class != 1 || result.Matches[2].Revision.ID != near.Revision.ID {
		t.Fatalf("chronological freshness search = %#v, %v", result, err)
	}
}

func TestKnowledgeSearchTypeFilterIsHardEligibility(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	source := createWorkTestTask(t, storage, workspace.ID, project.ID, "source", "type-filter-source")
	base := ProposeKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, Title: "typefilterprobe", Body: "identical body",
		Confidence: domain.KnowledgeConfidenceHigh, VerificationStatus: domain.KnowledgeVerificationVerified,
		FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded,
		Sources:         []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: source.Task.ID, Role: domain.KnowledgeSourcePrimary}},
	}
	findingCommand := base
	findingCommand.Type = domain.KnowledgeTypeFinding
	decisionCommand := base
	decisionCommand.Type = domain.KnowledgeTypeDecision
	finding := proposeCustomSearchKnowledge(t, storage, findingCommand, "type-finding")
	decision := proposeCustomSearchKnowledge(t, storage, decisionCommand, "type-decision")
	acceptSearchKnowledge(t, storage, workspace.ID, finding, "type-finding")
	acceptSearchKnowledge(t, storage, workspace.ID, decision, "type-decision")
	result, err := storage.SearchKnowledge(context.Background(), SearchKnowledgeQuery{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Type: domain.KnowledgeTypeDecision, Query: "typefilterprobe",
	})
	if err != nil || len(result.Matches) != 1 || result.Matches[0].Revision.ID != decision.Revision.ID || result.Matches[0].Revision.Type != domain.KnowledgeTypeDecision {
		t.Fatalf("type-filtered search = %#v, %v", result, err)
	}
}

func TestKnowledgeSearchFinalTieBreakUsesRevisionID(t *testing.T) {
	t.Parallel()
	clock := time.Date(2030, 1, 2, 3, 4, 5, 678901234, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return clock }})
	workspace, project := initializeWorkTestProject(t, storage)
	source := createWorkTestTask(t, storage, workspace.ID, project.ID, "source", "revision-tie-source")
	first := proposeSearchKnowledge(t, storage, workspace.ID, source.Task.ID, "", "revision-tie-first", "revisiontieprobe", "identical body", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationVerified)
	second := proposeSearchKnowledge(t, storage, workspace.ID, source.Task.ID, "", "revision-tie-second", "revisiontieprobe", "identical body", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationVerified)
	first = acceptSearchKnowledge(t, storage, workspace.ID, first, "revision-tie-first")
	second = acceptSearchKnowledge(t, storage, workspace.ID, second, "revision-tie-second")
	wantFirst, wantSecond := first.Revision.ID, second.Revision.ID
	if wantSecond < wantFirst {
		wantFirst, wantSecond = wantSecond, wantFirst
	}
	result, err := storage.SearchKnowledge(context.Background(), SearchKnowledgeQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Query: "revisiontieprobe"})
	if err != nil || len(result.Matches) != 2 || result.Matches[0].Revision.ID != wantFirst || result.Matches[1].Revision.ID != wantSecond {
		t.Fatalf("revision ID tie-break = %#v, %v; want %s then %s", result, err, wantFirst, wantSecond)
	}
	if result.Matches[0].Explanation.TieBreaker.AcceptedAt != result.Matches[1].Explanation.TieBreaker.AcceptedAt ||
		result.Matches[0].Explanation.Text.BM25 != result.Matches[1].Explanation.Text.BM25 {
		t.Fatalf("tie-break candidates differed on an earlier axis: %#v", result.Matches)
	}
}

func TestKnowledgeSearchAcceptanceTimeUsesExactNanoseconds(t *testing.T) {
	t.Parallel()
	clock := time.Date(2030, 1, 2, 3, 4, 4, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return clock }})
	workspace, project := initializeWorkTestProject(t, storage)
	source := createWorkTestTask(t, storage, workspace.ID, project.ID, "source", "search-accepted-time-source")
	wholeSecond := proposeSearchKnowledge(t, storage, workspace.ID, source.Task.ID, "", "accepted-whole", "acceptedtimeprobe", "identical body", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationVerified)
	fractional := proposeSearchKnowledge(t, storage, workspace.ID, source.Task.ID, "", "accepted-fractional", "acceptedtimeprobe", "identical body", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationVerified)
	clock = time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	acceptSearchKnowledge(t, storage, workspace.ID, wholeSecond, "accepted-whole")
	clock = clock.Add(time.Nanosecond)
	acceptSearchKnowledge(t, storage, workspace.ID, fractional, "accepted-fractional")

	result, err := storage.SearchKnowledge(context.Background(), SearchKnowledgeQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Query: "acceptedtimeprobe"})
	if err != nil || len(result.Matches) != 2 || result.Matches[0].Revision.ID != fractional.Revision.ID || result.Matches[1].Revision.ID != wholeSecond.Revision.ID {
		t.Fatalf("acceptance timestamp search = %#v, %v", result, err)
	}
	if result.Matches[0].Revision.AcceptedAt != "2030-01-02T03:04:05.000000001Z" || result.Matches[1].Revision.AcceptedAt != "2030-01-02T03:04:05Z" {
		t.Fatalf("acceptance timestamps = %q, %q", result.Matches[0].Revision.AcceptedAt, result.Matches[1].Revision.AcceptedAt)
	}
}

func TestKnowledgeSearchInvalidPersistedTimestampDegrades(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	source := createWorkTestTask(t, storage, workspace.ID, project.ID, "source", "search-invalid-time-source")
	proposed := proposeSearchKnowledge(t, storage, workspace.ID, source.Task.ID, "", "invalid-time", "invalidtimeprobe", "canonical body", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationVerified)
	acceptSearchKnowledge(t, storage, workspace.ID, proposed, "invalid-time")
	if _, err := storage.db.Exec("DROP TRIGGER knowledge_revision_reject_illegal_governance_update"); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.db.Exec("UPDATE knowledge_revisions SET accepted_at = 'not-a-timestamp' WHERE id = ?", proposed.Revision.ID); err != nil {
		t.Fatal(err)
	}
	_, err := storage.SearchKnowledge(context.Background(), SearchKnowledgeQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Query: "invalidtimeprobe"})
	if ErrorCode(err) != CodeRetrievalDegraded || !strings.Contains(err.Error(), domain.KnowledgeIndexCorrupt) {
		t.Fatalf("invalid persisted timestamp error = %v, code %q", err, ErrorCode(err))
	}
}

func TestKnowledgeIndexDamageDegradesWithoutAffectingCanonicalReadsAndRebuildRepairs(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "source", "retrieval-damage-source")
	proposed := proposeSearchKnowledge(t, storage, workspace.ID, task.Task.ID, "", "damage", "Contact damage", "contact damage", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationVerified)
	var currentMetadataDDL string
	if err := storage.db.QueryRow("SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'knowledge_search_metadata'").Scan(&currentMetadataDDL); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.db.Exec("DELETE FROM knowledge_search WHERE revision_id = ?", proposed.Revision.ID); err != nil {
		t.Fatal(err)
	}
	status, err := storage.KnowledgeIndexStatus(context.Background(), workspace.ID)
	if err != nil || status.Status != domain.KnowledgeIndexDegraded || status.Diagnosis != domain.KnowledgeIndexContentMismatch {
		t.Fatalf("damaged status = %#v, %v", status, err)
	}
	if _, err := storage.KnowledgeRevision(context.Background(), workspace.ID, proposed.Revision.ID); err != nil {
		t.Fatalf("canonical read after index damage: %v", err)
	}
	if _, err := storage.SearchKnowledge(context.Background(), SearchKnowledgeQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Query: "contact"}); ErrorCode(err) != CodeRetrievalDegraded {
		t.Fatalf("search damaged error = %v", err)
	}
	rebuilt, err := storage.RebuildKnowledgeIndex(context.Background(), RebuildKnowledgeIndexCommand{WorkspaceIdentifier: workspace.ID, IdempotencyKey: "repair-index", CorrelationID: "request-repair-index"})
	if err != nil || rebuilt.Index.Status != domain.KnowledgeIndexOK || rebuilt.Index.Generation != 2 {
		t.Fatalf("rebuild = %#v, %v", rebuilt, err)
	}
	replayed, err := storage.RebuildKnowledgeIndex(context.Background(), RebuildKnowledgeIndexCommand{WorkspaceIdentifier: workspace.ID, IdempotencyKey: "repair-index", CorrelationID: "request-repair-index-replay"})
	if err != nil || !reflect.DeepEqual(replayed, rebuilt) {
		t.Fatalf("rebuild replay = %#v, %v", replayed, err)
	}
	if _, err := storage.db.Exec("DROP TABLE knowledge_search"); err != nil {
		t.Fatal(err)
	}
	status, err = storage.KnowledgeIndexStatus(context.Background(), workspace.ID)
	if err != nil || status.Diagnosis != domain.KnowledgeIndexMissing {
		t.Fatalf("missing status = %#v, %v", status, err)
	}
	if _, err := storage.RebuildKnowledgeIndex(context.Background(), RebuildKnowledgeIndexCommand{WorkspaceIdentifier: workspace.ID, IdempotencyKey: "recreate-index", CorrelationID: "request-recreate-index"}); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.db.Exec("DROP TABLE knowledge_search_metadata"); err != nil {
		t.Fatal(err)
	}
	status, err = storage.KnowledgeIndexStatus(context.Background(), workspace.ID)
	if err != nil || status.Diagnosis != domain.KnowledgeIndexMissing {
		t.Fatalf("missing metadata status = %#v, %v", status, err)
	}
	metadataRepair, err := storage.RebuildKnowledgeIndex(context.Background(), RebuildKnowledgeIndexCommand{WorkspaceIdentifier: workspace.ID, IdempotencyKey: "recreate-index-metadata", CorrelationID: "request-recreate-index-metadata"})
	if err != nil || metadataRepair.Index.Status != domain.KnowledgeIndexOK || metadataRepair.Index.Generation != 1 {
		t.Fatalf("metadata rebuild = %#v, %v", metadataRepair, err)
	}
	var metadataDDL string
	if err := storage.db.QueryRow("SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'knowledge_search_metadata'").Scan(&metadataDDL); err != nil {
		t.Fatal(err)
	}
	var strict int
	if err := storage.db.QueryRow("SELECT strict FROM pragma_table_list WHERE name = 'knowledge_search_metadata'").Scan(&strict); err != nil {
		t.Fatal(err)
	}
	if metadataDDL != currentMetadataDDL || strict != 1 || !strings.Contains(metadataDDL, "source_digest NOT GLOB '*[^0-9a-f]*'") {
		t.Fatalf("repaired metadata schema differs from current baseline: strict=%d current=%q repaired=%q", strict, currentMetadataDDL, metadataDDL)
	}
	transaction, err := storage.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.Exec("UPDATE knowledge_search_metadata SET source_digest = ? WHERE singleton = 1", strings.Repeat("A", 64)); err == nil {
		_ = transaction.Rollback()
		t.Fatal("repaired metadata schema accepted an uppercase source digest")
	}
	_ = transaction.Rollback()
}

func TestKnowledgeProposalSurvivesMissingDerivedIndex(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "source", "missing-index-source")
	if _, err := storage.db.Exec("DROP TABLE knowledge_search"); err != nil {
		t.Fatal(err)
	}
	proposed := proposeSearchKnowledge(t, storage, workspace.ID, task.Task.ID, "", "missing", "Canonical survives", "derived search is optional", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationVerified)
	if _, err := storage.KnowledgeRevision(context.Background(), workspace.ID, proposed.Revision.ID); err != nil {
		t.Fatalf("canonical proposal after missing index = %v", err)
	}
	status, err := storage.KnowledgeIndexStatus(context.Background(), workspace.ID)
	if err != nil || status.Diagnosis != domain.KnowledgeIndexMissing {
		t.Fatalf("missing status = %#v, %v", status, err)
	}
}

func TestKnowledgeProposalCatchUpDoesNotRepairContentMismatch(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "source", "mismatched-index-source")
	first := proposeSearchKnowledge(t, storage, workspace.ID, task.Task.ID, "", "mismatch-first", "First canonical", "first canonical body", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationVerified)
	if _, err := storage.db.Exec("UPDATE knowledge_search SET body = 'tampered derived body' WHERE revision_id = ?", first.Revision.ID); err != nil {
		t.Fatal(err)
	}
	second := proposeSearchKnowledge(t, storage, workspace.ID, task.Task.ID, "", "mismatch-second", "Second canonical", "second canonical body", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationVerified)
	if _, err := storage.KnowledgeRevision(context.Background(), workspace.ID, second.Revision.ID); err != nil {
		t.Fatalf("canonical proposal after content mismatch = %v", err)
	}
	var indexedSecond int64
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM knowledge_search WHERE revision_id = ?", second.Revision.ID).Scan(&indexedSecond); err != nil {
		t.Fatal(err)
	}
	var firstIndexedBody string
	if err := storage.db.QueryRow("SELECT body FROM knowledge_search WHERE revision_id = ?", first.Revision.ID).Scan(&firstIndexedBody); err != nil {
		t.Fatal(err)
	}
	if indexedSecond != 0 || firstIndexedBody != "tampered derived body" {
		t.Fatalf("incremental catch-up repaired a damaged projection: new rows=%d old body=%q", indexedSecond, firstIndexedBody)
	}
	status, err := storage.KnowledgeIndexStatus(context.Background(), workspace.ID)
	if err != nil || status.Status != domain.KnowledgeIndexDegraded {
		t.Fatalf("damaged projection status = %#v, %v", status, err)
	}
}

func TestKnowledgeIndexNullContentIsMismatchAndCatchUpDoesNotRepair(t *testing.T) {
	t.Parallel()
	for _, column := range []string{"workspace_id", "title", "body"} {
		column := column
		t.Run(column, func(t *testing.T) {
			t.Parallel()
			storage := openTestStore(t, t.TempDir(), Options{})
			workspace, project := initializeWorkTestProject(t, storage)
			task := createWorkTestTask(t, storage, workspace.ID, project.ID, "source", "null-index-source-"+column)
			first := proposeSearchKnowledge(t, storage, workspace.ID, task.Task.ID, "", "null-first-"+column, "Null projection", "first canonical body", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationVerified)
			statement := "UPDATE knowledge_search SET " + column + " = NULL WHERE revision_id = ?"
			if _, err := storage.db.Exec(statement, first.Revision.ID); err != nil {
				t.Fatal(err)
			}
			status, err := storage.KnowledgeIndexStatus(context.Background(), workspace.ID)
			if err != nil || status.Status != domain.KnowledgeIndexDegraded || status.Diagnosis != domain.KnowledgeIndexContentMismatch {
				t.Fatalf("NULL %s status = %#v, %v", column, status, err)
			}
			second := proposeSearchKnowledge(t, storage, workspace.ID, task.Task.ID, "", "null-second-"+column, "Second canonical", "second canonical body", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationVerified)
			if _, err := storage.KnowledgeRevision(context.Background(), workspace.ID, second.Revision.ID); err != nil {
				t.Fatalf("canonical proposal after NULL %s = %v", column, err)
			}
			var indexedSecond int64
			if err := storage.db.QueryRow("SELECT COUNT(*) FROM knowledge_search WHERE revision_id = ?", second.Revision.ID).Scan(&indexedSecond); err != nil {
				t.Fatal(err)
			}
			var damagedStillNull int
			if err := storage.db.QueryRow("SELECT "+column+" IS NULL FROM knowledge_search WHERE revision_id = ?", first.Revision.ID).Scan(&damagedStillNull); err != nil {
				t.Fatal(err)
			}
			if indexedSecond != 0 || damagedStillNull != 1 {
				t.Fatalf("catch-up repaired NULL %s projection: new rows=%d null=%d", column, indexedSecond, damagedStillNull)
			}
			status, err = storage.KnowledgeIndexStatus(context.Background(), workspace.ID)
			if err != nil || status.Status != domain.KnowledgeIndexDegraded {
				t.Fatalf("NULL %s status after proposal = %#v, %v", column, status, err)
			}
		})
	}
}

func TestKnowledgeIndexSchemaTamperingDegradesAndRebuildRepairs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		ddl  string
	}{
		{
			name: "swapped title and body columns",
			ddl: `CREATE VIRTUAL TABLE knowledge_search USING fts5(
revision_id UNINDEXED, workspace_id UNINDEXED, body, title,
tokenize = 'unicode61 remove_diacritics 2')`,
		},
		{
			name: "different tokenizer configuration",
			ddl: `CREATE VIRTUAL TABLE knowledge_search USING fts5(
revision_id UNINDEXED, workspace_id UNINDEXED, title, body,
tokenize = 'unicode61 remove_diacritics 0')`,
		},
	}
	for index, test := range tests {
		index, test := index, test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			storage := openTestStore(t, t.TempDir(), Options{})
			workspace, project := initializeWorkTestProject(t, storage)
			task := createWorkTestTask(t, storage, workspace.ID, project.ID, "source", "schema-tamper-source-"+string(rune('a'+index)))
			first := proposeSearchKnowledge(t, storage, workspace.ID, task.Task.ID, "", "schema-first-"+string(rune('a'+index)), "schemaprobe", "canonical body", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationVerified)
			acceptSearchKnowledge(t, storage, workspace.ID, first, "schema-first-"+string(rune('a'+index)))
			if _, err := storage.db.Exec("DROP TABLE knowledge_search"); err != nil {
				t.Fatal(err)
			}
			if _, err := storage.db.Exec(test.ddl); err != nil {
				t.Fatal(err)
			}
			if _, err := storage.db.Exec(`INSERT INTO knowledge_search(revision_id,workspace_id,title,body)
SELECT kr.id,ki.workspace_id,kr.title,kr.body FROM knowledge_revisions kr JOIN knowledge_items ki ON ki.id=kr.item_id ORDER BY kr.id`); err != nil {
				t.Fatal(err)
			}
			status, err := storage.KnowledgeIndexStatus(context.Background(), workspace.ID)
			if err != nil || status.Status != domain.KnowledgeIndexDegraded || status.Diagnosis != domain.KnowledgeIndexCorrupt {
				t.Fatalf("tampered schema status = %#v, %v", status, err)
			}
			if _, err := storage.SearchKnowledge(context.Background(), SearchKnowledgeQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Query: "schemaprobe"}); ErrorCode(err) != CodeRetrievalDegraded {
				t.Fatalf("search with tampered schema error = %v, code %q", err, ErrorCode(err))
			}
			second := proposeSearchKnowledge(t, storage, workspace.ID, task.Task.ID, "", "schema-second-"+string(rune('a'+index)), "Second canonical", "second canonical body", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationVerified)
			var indexedSecond int64
			if err := storage.db.QueryRow("SELECT COUNT(*) FROM knowledge_search WHERE revision_id = ?", second.Revision.ID).Scan(&indexedSecond); err != nil {
				t.Fatal(err)
			}
			if indexedSecond != 0 {
				t.Fatalf("catch-up appended to tampered schema: rows=%d", indexedSecond)
			}
			rebuilt, err := storage.RebuildKnowledgeIndex(context.Background(), RebuildKnowledgeIndexCommand{
				WorkspaceIdentifier: workspace.ID, IdempotencyKey: "repair-schema-" + string(rune('a'+index)), CorrelationID: "request-repair-schema",
			})
			if err != nil || rebuilt.Index.Status != domain.KnowledgeIndexOK {
				t.Fatalf("schema rebuild = %#v, %v", rebuilt, err)
			}
			var repairedSQL string
			if err := storage.db.QueryRow("SELECT sql FROM sqlite_schema WHERE type='table' AND name='knowledge_search'").Scan(&repairedSQL); err != nil {
				t.Fatal(err)
			}
			if normalizeSQLiteSchemaSQL(repairedSQL) != normalizeSQLiteSchemaSQL(knowledgeSearchTableDDL) {
				t.Fatalf("repaired schema = %q", repairedSQL)
			}
			status, err = storage.KnowledgeIndexStatus(context.Background(), workspace.ID)
			if err != nil || status.Status != domain.KnowledgeIndexOK {
				t.Fatalf("repaired schema status = %#v, %v", status, err)
			}
		})
	}
}

func TestKnowledgeIndexShadowSchemaTamperingIsDiagnosedSeparatelyFromCanonicalCatalog(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		table string
		ddl   string
	}{
		{name: "config", table: "knowledge_search_config", ddl: `CREATE TABLE knowledge_search_config(k PRIMARY KEY,v,rogue TEXT UNIQUE) WITHOUT ROWID`},
		{name: "content", table: "knowledge_search_content", ddl: `CREATE TABLE knowledge_search_content(id INTEGER PRIMARY KEY,c0,c1,c2,c3,rogue TEXT)`},
		{name: "data", table: "knowledge_search_data", ddl: `CREATE TABLE knowledge_search_data(id INTEGER PRIMARY KEY,block BLOB,rogue TEXT)`},
		{name: "docsize", table: "knowledge_search_docsize", ddl: `CREATE TABLE knowledge_search_docsize(id INTEGER PRIMARY KEY,sz BLOB,rogue TEXT)`},
		{name: "idx", table: "knowledge_search_idx", ddl: `CREATE TABLE knowledge_search_idx(segid,term,pgno,rogue TEXT,PRIMARY KEY(segid,term)) WITHOUT ROWID`},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			dataDir := t.TempDir()
			storage := openTestStore(t, dataDir, Options{})
			workspace, err := storage.InitWorkspace(context.Background(), InitWorkspaceCommand{
				Name: "personal", IdempotencyKey: "shadow-workspace-" + test.name, CorrelationID: "shadow-workspace-request-" + test.name,
			})
			if err != nil {
				t.Fatalf("InitWorkspace() error = %v", err)
			}
			if _, err := storage.db.Exec("DROP TABLE " + test.table); err != nil {
				t.Fatalf("drop %s: %v", test.table, err)
			}
			if _, err := storage.db.Exec(test.ddl); err != nil {
				t.Fatalf("replace %s: %v", test.table, err)
			}
			if _, err := storage.BaselineIdentity(context.Background()); err != nil {
				t.Fatalf("derived shadow replacement changed canonical baseline identity: %v", err)
			}
			if err := storage.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}

			reopened, err := Open(context.Background(), dataDir, Options{})
			if err != nil {
				t.Fatalf("Open(with corrupt derived shadow) error = %v", err)
			}
			defer reopened.Close()
			status, err := reopened.KnowledgeIndexStatus(context.Background(), workspace.Workspace.ID)
			if err != nil || status.Status != domain.KnowledgeIndexDegraded || status.Diagnosis != domain.KnowledgeIndexCorrupt {
				t.Fatalf("KnowledgeIndexStatus(%s) = %#v, %v", test.table, status, err)
			}
		})
	}
}

func TestKnowledgeIndexMetadataSchemaTamperingDegradesAndRebuildRepairs(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _ := initializeWorkTestProject(t, storage)
	if _, err := storage.db.Exec("DROP TABLE knowledge_search_metadata"); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.db.Exec(`CREATE TABLE knowledge_search_metadata (
singleton INTEGER,
generation INTEGER,
built_at TEXT,
source_event_sequence INTEGER,
source_count INTEGER,
source_digest TEXT
)`); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("0", 64)
	builtAt := "2030-01-02T03:04:05Z"
	if _, err := storage.db.Exec(`INSERT INTO knowledge_search_metadata(
singleton,generation,built_at,source_event_sequence,source_count,source_digest
) VALUES (1,1,?,0,0,?),(1,2,?,0,0,?)`,
		builtAt, digest, builtAt, digest); err != nil {
		t.Fatal(err)
	}
	status, err := storage.KnowledgeIndexStatus(context.Background(), workspace.ID)
	if err != nil || status.Status != domain.KnowledgeIndexDegraded || status.Diagnosis != domain.KnowledgeIndexCorrupt {
		t.Fatalf("lax duplicate metadata status = %#v, %v", status, err)
	}
	rebuilt, err := storage.RebuildKnowledgeIndex(context.Background(), RebuildKnowledgeIndexCommand{
		WorkspaceIdentifier: workspace.ID, IdempotencyKey: "repair-metadata-schema", CorrelationID: "request-repair-metadata-schema",
	})
	if err != nil || rebuilt.Index.Status != domain.KnowledgeIndexOK {
		t.Fatalf("metadata schema rebuild = %#v, %v", rebuilt, err)
	}
	var repairedSQL string
	if err := storage.db.QueryRow("SELECT sql FROM sqlite_schema WHERE type='table' AND name='knowledge_search_metadata'").Scan(&repairedSQL); err != nil {
		t.Fatal(err)
	}
	if normalizeSQLiteSchemaSQL(repairedSQL) != normalizeSQLiteSchemaSQL(knowledgeSearchMetadataTableDDL) {
		t.Fatalf("repaired metadata schema = %q", repairedSQL)
	}
	status, err = storage.KnowledgeIndexStatus(context.Background(), workspace.ID)
	if err != nil || status.Status != domain.KnowledgeIndexOK {
		t.Fatalf("repaired metadata status = %#v, %v", status, err)
	}
}

func TestKnowledgeIndexMetadataTemporalValuesMustBeValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		statement string
		argument  any
	}{
		{name: "invalid built at", statement: "UPDATE knowledge_search_metadata SET built_at=? WHERE singleton=1", argument: "not-a-timestamp"},
		{name: "future source cursor", statement: "UPDATE knowledge_search_metadata SET source_event_sequence=? WHERE singleton=1", argument: int64(1 << 50)},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			storage := openTestStore(t, t.TempDir(), Options{})
			workspace, _ := initializeWorkTestProject(t, storage)
			if _, err := storage.db.Exec(test.statement, test.argument); err != nil {
				t.Fatal(err)
			}
			status, err := storage.KnowledgeIndexStatus(context.Background(), workspace.ID)
			if err != nil || status.Status != domain.KnowledgeIndexDegraded || status.Diagnosis != domain.KnowledgeIndexCorrupt {
				t.Fatalf("invalid metadata value status = %#v, %v", status, err)
			}
		})
	}
}

func TestKnowledgeFTSSegmentCorruptionDoesNotBlockCanonicalRestartAndRebuild(t *testing.T) {
	t.Parallel()
	dataDirectory := t.TempDir()
	storage := openTestStore(t, dataDirectory, Options{})
	workspace, project, agent, checkout, assigned := initializeRunTest(t, storage, "retrieval segment restart")
	proposed := proposeSearchKnowledge(t, storage, workspace.ID, assigned.Task.ID, assigned.Task.ID, "segment-restart", "segmentrestartprobe", "canonical context survives derived index corruption", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationVerified)
	accepted := acceptSearchKnowledge(t, storage, workspace.ID, proposed, "segment-restart")
	packet, err := storage.BuildContextPacket(context.Background(), BuildContextCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, AgentIdentifier: agent.ID,
		CheckoutIdentifier: checkout.ID, KnowledgeRevisionIDs: []string{accepted.Revision.ID},
		ExpectedTaskRevision: assigned.Task.Revision, IdempotencyKey: "segment-restart-context", CorrelationID: "request-segment-restart-context",
	})
	if err != nil {
		t.Fatal(err)
	}
	var segmentID int64
	if err := storage.db.QueryRow("SELECT MAX(id) FROM knowledge_search_data WHERE id > 10").Scan(&segmentID); err != nil || segmentID == 0 {
		t.Fatalf("find FTS segment = %d, %v", segmentID, err)
	}
	if _, err := storage.db.Exec("UPDATE knowledge_search_data SET block = x'00' WHERE id = ?", segmentID); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(context.Background(), dataDirectory, Options{})
	if err != nil {
		t.Fatalf("restart with corrupt derived FTS index = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	loadedRevision, err := reopened.KnowledgeRevision(context.Background(), workspace.ID, accepted.Revision.ID)
	if err != nil || loadedRevision.ID != accepted.Revision.ID || loadedRevision.Body != accepted.Revision.Body {
		t.Fatalf("canonical revision after restart = %#v, %v", loadedRevision, err)
	}
	loadedPacket, err := reopened.ContextPacket(context.Background(), workspace.ID, packet.Value.ID)
	if err != nil || loadedPacket.ID != packet.Value.ID || len(loadedPacket.AcceptedKnowledge) != 1 || loadedPacket.AcceptedKnowledge[0].ID != accepted.Revision.ID {
		t.Fatalf("canonical context after restart = %#v, %v", loadedPacket, err)
	}
	status, err := reopened.KnowledgeIndexStatus(context.Background(), workspace.ID)
	if err != nil || status.Status != domain.KnowledgeIndexDegraded || status.Diagnosis != domain.KnowledgeIndexCorrupt {
		t.Fatalf("corrupt FTS status after restart = %#v, %v", status, err)
	}
	if _, err := reopened.SearchKnowledge(context.Background(), SearchKnowledgeQuery{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, TaskIdentifier: assigned.Task.ID, Query: "segmentrestartprobe",
	}); ErrorCode(err) != CodeRetrievalDegraded || !strings.Contains(err.Error(), domain.KnowledgeIndexCorrupt) {
		t.Fatalf("search with corrupt FTS error = %v, code %q", err, ErrorCode(err))
	}
	if _, err := reopened.RebuildKnowledgeIndex(context.Background(), RebuildKnowledgeIndexCommand{
		WorkspaceIdentifier: workspace.ID, IdempotencyKey: "repair-segment-restart", CorrelationID: "request-repair-segment-restart",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := reopened.SearchKnowledge(context.Background(), SearchKnowledgeQuery{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, TaskIdentifier: assigned.Task.ID, Query: "segmentrestartprobe",
	})
	if err != nil || len(result.Matches) != 1 || result.Matches[0].Revision.ID != accepted.Revision.ID {
		t.Fatalf("search after FTS rebuild = %#v, %v", result, err)
	}
}

func TestCanonicalPageCorruptionStillBlocksRestart(t *testing.T) {
	t.Parallel()
	dataDirectory := t.TempDir()
	storage := openTestStore(t, dataDirectory, Options{})
	_, _ = initializeWorkTestProject(t, storage)
	var rootPage, pageSize int64
	if err := storage.db.QueryRow("SELECT rootpage FROM sqlite_schema WHERE type='table' AND name='workspaces'").Scan(&rootPage); err != nil {
		t.Fatal(err)
	}
	if err := storage.db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	if rootPage < 2 || pageSize < 512 {
		t.Fatalf("unexpected canonical page coordinates: root=%d size=%d", rootPage, pageSize)
	}
	if _, err := storage.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := os.OpenFile(filepath.Join(dataDirectory, databaseFilename), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.WriteAt([]byte{0}, (rootPage-1)*pageSize); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(context.Background(), dataDirectory, Options{}); err == nil || ErrorCode(err) != CodeStorageFailed {
		if reopened != nil {
			_ = reopened.Close()
		}
		t.Fatalf("Open(canonical corruption) error = %v, code %q", err, ErrorCode(err))
	}
}

func TestSimultaneousFTSAndCanonicalCorruptionStillBlocksRestart(t *testing.T) {
	t.Parallel()
	dataDirectory := t.TempDir()
	storage := openTestStore(t, dataDirectory, Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "source", "simultaneous-corruption-source")
	_ = proposeSearchKnowledge(t, storage, workspace.ID, task.Task.ID, "", "simultaneous-corruption", "simultaneousprobe", "indexed body", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationVerified)
	var segmentID, rootPage, pageSize int64
	if err := storage.db.QueryRow("SELECT MAX(id) FROM knowledge_search_data WHERE id > 10").Scan(&segmentID); err != nil || segmentID == 0 {
		t.Fatalf("find FTS segment = %d, %v", segmentID, err)
	}
	if err := storage.db.QueryRow("SELECT rootpage FROM sqlite_schema WHERE type='table' AND name='workspaces'").Scan(&rootPage); err != nil {
		t.Fatal(err)
	}
	if err := storage.db.QueryRow("PRAGMA page_size").Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.db.Exec("UPDATE knowledge_search_data SET block=x'00' WHERE id=?", segmentID); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	if err := storage.Close(); err != nil {
		t.Fatal(err)
	}
	database, err := os.OpenFile(filepath.Join(dataDirectory, databaseFilename), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.WriteAt([]byte{0}, (rootPage-1)*pageSize); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := Open(context.Background(), dataDirectory, Options{}); err == nil || ErrorCode(err) != CodeStorageFailed {
		if reopened != nil {
			_ = reopened.Close()
		}
		t.Fatalf("Open(simultaneous corruption) error = %v, code %q", err, ErrorCode(err))
	}
}

func TestNormalizeSQLiteSchemaSQLIgnoresOnlyFormattingWhitespace(t *testing.T) {
	t.Parallel()
	formatted := `CREATE   VIRTUAL TABLE knowledge_search USING fts5 (
revision_id UNINDEXED,
workspace_id UNINDEXED,
title,
body,
tokenize = 'unicode61 remove_diacritics 2'
)`
	if normalizeSQLiteSchemaSQL(formatted) != normalizeSQLiteSchemaSQL(knowledgeSearchTableDDL) {
		t.Fatalf("format-only schema difference was not normalized: %q != %q", normalizeSQLiteSchemaSQL(formatted), normalizeSQLiteSchemaSQL(knowledgeSearchTableDDL))
	}
	missingSeparator := strings.Replace(knowledgeSearchTableDDL, "VIRTUAL TABLE", "VIRTUALTABLE", 1)
	if normalizeSQLiteSchemaSQL(missingSeparator) == normalizeSQLiteSchemaSQL(knowledgeSearchTableDDL) {
		t.Fatal("schema normalizer erased significant token separation")
	}
}

func TestKnowledgeIndexRebuildPublicationFailureRollsBack(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	source := createWorkTestTask(t, storage, workspace.ID, project.ID, "source", "rebuild-rollback-source")
	proposed := proposeSearchKnowledge(t, storage, workspace.ID, source.Task.ID, "", "rebuild-rollback", "rollback probe", "canonical content", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationVerified)
	acceptSearchKnowledge(t, storage, workspace.ID, proposed, "rebuild-rollback")
	before, err := storage.KnowledgeIndexStatus(context.Background(), workspace.ID)
	if err != nil || before.Status != domain.KnowledgeIndexOK {
		t.Fatalf("status before rebuild = %#v, %v", before, err)
	}
	var eventsBefore int64
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&eventsBefore); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected index publication interruption")
	storage.mutationHook = func(stage string) error {
		if stage == MutationAfterProjection {
			return injected
		}
		return nil
	}
	_, err = storage.RebuildKnowledgeIndex(context.Background(), RebuildKnowledgeIndexCommand{
		WorkspaceIdentifier: workspace.ID, IdempotencyKey: "rebuild-rollback", CorrelationID: "request-rebuild-rollback",
	})
	storage.mutationHook = nil
	if !errors.Is(err, injected) {
		t.Fatalf("rebuild error = %v, want injected failure", err)
	}
	after, err := storage.KnowledgeIndexStatus(context.Background(), workspace.ID)
	if err != nil || after.Status != domain.KnowledgeIndexOK || after.Generation != before.Generation || after.SourceDigest != before.SourceDigest {
		t.Fatalf("status after failed publication = %#v, %v; before %#v", after, err, before)
	}
	var eventsAfter, idempotencyRows int64
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&eventsAfter); err != nil {
		t.Fatal(err)
	}
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM idempotency_keys WHERE key = 'rebuild-rollback'").Scan(&idempotencyRows); err != nil {
		t.Fatal(err)
	}
	if eventsAfter != eventsBefore || idempotencyRows != 0 {
		t.Fatalf("failed rebuild mutated canonical state: events %d -> %d, idempotency rows %d", eventsBefore, eventsAfter, idempotencyRows)
	}
}

func TestKnowledgeIndexRebuildOldReplayConflictsWithHealthyNewState(t *testing.T) {
	t.Parallel()
	t.Run("degraded current snapshot", func(t *testing.T) {
		storage := openTestStore(t, t.TempDir(), Options{})
		workspace, _ := initializeWorkTestProject(t, storage)
		if _, err := storage.RebuildKnowledgeIndex(context.Background(), RebuildKnowledgeIndexCommand{
			WorkspaceIdentifier: workspace.ID, IdempotencyKey: "before-degradation", CorrelationID: "request-before-degradation",
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := storage.db.Exec("DROP TABLE knowledge_search"); err != nil {
			t.Fatal(err)
		}
		_, err := storage.RebuildKnowledgeIndex(context.Background(), RebuildKnowledgeIndexCommand{
			WorkspaceIdentifier: workspace.ID, IdempotencyKey: "before-degradation", CorrelationID: "request-before-degradation-replay",
		})
		if ErrorCode(err) != CodeRetrievalDegraded || !strings.Contains(err.Error(), domain.KnowledgeIndexMissing) {
			t.Fatalf("degraded replay error = %v, code %q", err, ErrorCode(err))
		}
	})

	t.Run("later rebuild generation", func(t *testing.T) {
		storage := openTestStore(t, t.TempDir(), Options{})
		workspace, _ := initializeWorkTestProject(t, storage)
		first, err := storage.RebuildKnowledgeIndex(context.Background(), RebuildKnowledgeIndexCommand{
			WorkspaceIdentifier: workspace.ID, IdempotencyKey: "old-generation", CorrelationID: "request-old-generation",
		})
		if err != nil {
			t.Fatal(err)
		}
		second, err := storage.RebuildKnowledgeIndex(context.Background(), RebuildKnowledgeIndexCommand{
			WorkspaceIdentifier: workspace.ID, IdempotencyKey: "new-generation", CorrelationID: "request-new-generation",
		})
		if err != nil || second.Index.Status != domain.KnowledgeIndexOK || second.Index.Generation <= first.Index.Generation {
			t.Fatalf("later rebuild = %#v, %v; first %#v", second, err, first)
		}
		_, err = storage.RebuildKnowledgeIndex(context.Background(), RebuildKnowledgeIndexCommand{
			WorkspaceIdentifier: workspace.ID, IdempotencyKey: "old-generation", CorrelationID: "request-old-generation-replay",
		})
		if ErrorCode(err) != CodeIdempotencyConflict {
			t.Fatalf("old generation replay error = %v, code %q", err, ErrorCode(err))
		}
		current, err := storage.KnowledgeIndexStatus(context.Background(), workspace.ID)
		if err != nil || current.Status != domain.KnowledgeIndexOK || current.Generation != second.Index.Generation {
			t.Fatalf("current status = %#v, %v", current, err)
		}
	})

	t.Run("canonical refresh digest", func(t *testing.T) {
		storage := openTestStore(t, t.TempDir(), Options{})
		workspace, project := initializeWorkTestProject(t, storage)
		source := createWorkTestTask(t, storage, workspace.ID, project.ID, "source", "old-replay-refresh-source")
		first, err := storage.RebuildKnowledgeIndex(context.Background(), RebuildKnowledgeIndexCommand{
			WorkspaceIdentifier: workspace.ID, IdempotencyKey: "before-refresh", CorrelationID: "request-before-refresh",
		})
		if err != nil {
			t.Fatal(err)
		}
		_ = proposeSearchKnowledge(t, storage, workspace.ID, source.Task.ID, "", "refresh-replay", "refresh digest", "new canonical revision", domain.KnowledgeConfidenceHigh, domain.KnowledgeVerificationVerified)
		current, err := storage.KnowledgeIndexStatus(context.Background(), workspace.ID)
		if err != nil || current.Status != domain.KnowledgeIndexOK || current.Generation != first.Index.Generation || current.SourceDigest == first.Index.SourceDigest {
			t.Fatalf("refreshed status = %#v, %v; first %#v", current, err, first)
		}
		_, err = storage.RebuildKnowledgeIndex(context.Background(), RebuildKnowledgeIndexCommand{
			WorkspaceIdentifier: workspace.ID, IdempotencyKey: "before-refresh", CorrelationID: "request-before-refresh-replay",
		})
		if ErrorCode(err) != CodeIdempotencyConflict {
			t.Fatalf("pre-refresh replay error = %v, code %q", err, ErrorCode(err))
		}
	})
}

func proposeSearchKnowledge(t *testing.T, storage *Store, workspaceID, sourceTaskID, taskScopeID, key, title, body, confidence, verification string) KnowledgeMutationResult {
	t.Helper()
	return proposeCustomSearchKnowledge(t, storage, ProposeKnowledgeCommand{
		WorkspaceIdentifier: workspaceID, TaskScopeID: taskScopeID, Type: domain.KnowledgeTypeFinding,
		Title: title, Body: body, Confidence: confidence, VerificationStatus: verification,
		FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded,
		Sources:         []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: sourceTaskID, Role: domain.KnowledgeSourcePrimary}},
	}, key)
}

func proposeCustomSearchKnowledge(t *testing.T, storage *Store, command ProposeKnowledgeCommand, key string) KnowledgeMutationResult {
	t.Helper()
	command.Actor = OwnerKnowledgeActor()
	command.IdempotencyKey = "search-propose-" + key
	command.CorrelationID = "request-search-propose-" + key
	result, err := storage.ProposeKnowledge(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func proposeExpiringSearchKnowledge(t *testing.T, storage *Store, workspaceID, sourceTaskID, key, freshUntil string) KnowledgeMutationResult {
	t.Helper()
	result, err := storage.ProposeKnowledge(context.Background(), ProposeKnowledgeCommand{
		WorkspaceIdentifier: workspaceID, Type: domain.KnowledgeTypeFinding,
		Title: "chronoprobe", Body: "identical freshness candidate", Confidence: domain.KnowledgeConfidenceHigh,
		VerificationStatus: domain.KnowledgeVerificationVerified, FreshnessPolicy: domain.KnowledgeFreshExpiresAt,
		FreshUntil: freshUntil,
		Sources:    []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: sourceTaskID, Role: domain.KnowledgeSourcePrimary}},
		Actor:      OwnerKnowledgeActor(), IdempotencyKey: "search-propose-" + key, CorrelationID: "request-search-propose-" + key,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func acceptSearchKnowledge(t *testing.T, storage *Store, workspaceID string, proposed KnowledgeMutationResult, key string) KnowledgeMutationResult {
	t.Helper()
	result, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{
		WorkspaceIdentifier: workspaceID, RevisionID: proposed.Revision.ID, ExpectedStateRevision: proposed.Revision.StateRevision,
		Actor: OwnerKnowledgeActor(), IdempotencyKey: "search-accept-" + key, CorrelationID: "request-search-accept-" + key,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
