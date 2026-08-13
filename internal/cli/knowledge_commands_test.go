package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestKnowledgeSearchForwardsScopeDefaultsAndExplainsTextAndJSON(t *testing.T) {
	t.Parallel()
	search := domain.KnowledgeSearchResult{
		NormalizedQuery: "contact ordering", EvaluatedAt: "2026-08-13T12:00:00Z",
		CanonicalEventSequence: 42, RankPolicy: domain.KnowledgeSearchRankPolicy,
		Index: domain.KnowledgeIndexStatus{Status: domain.KnowledgeIndexOK, Generation: 2, BuiltAt: "2026-08-13T11:59:00Z", SourceEventSequence: 42, SourceCount: 1, SourceDigest: strings.Repeat("a", 64)},
		Matches: []domain.KnowledgeSearchMatch{{Ordinal: 1, Revision: domain.KnowledgeRevision{
			ID: "krev_00000000000000000000000000000001", Title: "Contact ordering",
		}, Explanation: domain.KnowledgeSearchExplanation{
			Scope:      domain.KnowledgeSearchScopeExplanation{Reason: "exact task"},
			Authority:  domain.KnowledgeSearchAuthorityExplanation{Reason: "accepted current"},
			Freshness:  domain.KnowledgeSearchFreshnessExplanation{Reason: "until superseded"},
			Provenance: domain.KnowledgeSearchProvenanceExplanation{Reason: "primary task"},
			Text:       domain.KnowledgeSearchTextExplanation{BM25: -2.5},
		}}},
	}
	client := &fakeDaemonClient{knowledgeSearch: localapi.KnowledgeSearchResult{
		Schema: localapi.KnowledgeSearchSchema, Type: "knowledge_search", Search: search,
	}}
	app, stdout, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	if exit := app.Run([]string{"knowledge", "search", "contact ordering", "--workspace", "personal", "--project", "demo", "--task", "task_00000000000000000000000000000001", "--type", "finding", "--socket", "/tmp/crewfold.sock"}); exit != ExitOK {
		t.Fatalf("knowledge search exit=%d stderr=%q", exit, stderr.String())
	}
	if got := client.knowledgeSearchParams; got.Query != "contact ordering" || got.Workspace != "personal" || got.Project != "demo" || got.Task == "" || got.Type != "finding" || got.Limit == nil || *got.Limit != 20 {
		t.Fatalf("KnowledgeSearch params = %#v", got)
	}
	for _, wanted := range []string{"rank policy: knowledge_search_v1", "krev_00000000000000000000000000000001", "scope: exact task", "BM25"} {
		if !strings.Contains(strings.ToLower(stdout.String()), strings.ToLower(wanted)) {
			t.Errorf("text search output %q lacks %q", stdout.String(), wanted)
		}
	}

	app, stdout, stderr = newTestApp()
	app.newClient = func(string) daemonClient { return client }
	if exit := app.Run([]string{"knowledge", "search", "contact ordering", "--workspace", "personal", "--project", "demo", "--limit", "100", "--socket", "/tmp/crewfold.sock", "--output", "json"}); exit != ExitOK {
		t.Fatalf("JSON knowledge search exit=%d stderr=%q", exit, stderr.String())
	}
	if client.knowledgeSearchParams.Limit == nil || *client.knowledgeSearchParams.Limit != 100 {
		t.Fatalf("explicit search limit = %v, want 100", client.knowledgeSearchParams.Limit)
	}
	var decoded localapi.KnowledgeSearchResult
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil || decoded.Schema != localapi.KnowledgeSearchSchema || len(decoded.Search.Matches) != 1 {
		t.Fatalf("search JSON = %#v, error=%v", decoded, err)
	}
}

func TestKnowledgeSearchSeparatorAllowsOptionLookingLiteralQuery(t *testing.T) {
	t.Parallel()
	client := &fakeDaemonClient{knowledgeSearch: localapi.KnowledgeSearchResult{
		Schema: localapi.KnowledgeSearchSchema, Type: "knowledge_search",
		Search: domain.KnowledgeSearchResult{Matches: []domain.KnowledgeSearchMatch{}},
	}}
	app, _, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	if exit := app.Run([]string{
		"knowledge", "search", "--", "--literal-flag", "--workspace", "personal", "--project", "demo", "--socket", "/tmp/crewfold.sock",
	}); exit != ExitOK {
		t.Fatalf("option-looking query exit=%d stderr=%q", exit, stderr.String())
	}
	if got := client.knowledgeSearchParams; got.Query != "--literal-flag" || got.Limit == nil || *got.Limit != 20 {
		t.Fatalf("option-looking search params = %#v", got)
	}

	app, _, stderr = newTestApp()
	if exit := app.Run([]string{"knowledge", "search", "--"}); exit != ExitUsage || stderr.Len() == 0 {
		t.Fatalf("separator without a distinct query exit=%d stderr=%q", exit, stderr.String())
	}
}

func TestKnowledgeSearchRejectsOutOfBoundsLimit(t *testing.T) {
	t.Parallel()
	for _, limit := range []string{"0", "101"} {
		app, _, stderr := newTestApp()
		if exit := app.Run([]string{"knowledge", "search", "query", "--workspace", "personal", "--project", "demo", "--limit", limit, "--socket", "/tmp/crewfold.sock"}); exit != ExitUsage || stderr.Len() == 0 {
			t.Fatalf("limit %s exit=%d stderr=%q", limit, exit, stderr.String())
		}
	}
}

func TestKnowledgeAndDoctorHelpPublishRetrievalCommands(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	if exit := app.Run([]string{"help", "knowledge"}); exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("knowledge help exit=%d stderr=%q", exit, stderr.String())
	}
	for _, command := range []string{"knowledge search <query>", "knowledge search -- <query-starting-with-->", "knowledge index status", "knowledge index rebuild"} {
		if !strings.Contains(stdout.String(), command) {
			t.Errorf("knowledge help %q lacks %q", stdout.String(), command)
		}
	}

	app, stdout, stderr = newTestApp()
	if exit := app.Run([]string{"help", "doctor"}); exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("doctor help exit=%d stderr=%q", exit, stderr.String())
	}
	if !strings.Contains(stdout.String(), "doctor --retrieval --workspace <scope> --socket <path>") {
		t.Errorf("doctor help %q lacks retrieval diagnosis usage", stdout.String())
	}
}

func TestKnowledgeSearchJSONPreservesCompleteRevisionAndRankingTuple(t *testing.T) {
	t.Parallel()
	revision := domain.KnowledgeRevision{
		ID: "krev_00000000000000000000000000000001", ItemID: "know_00000000000000000000000000000001",
		WorkspaceID: "ws_00000000000000000000000000000001", ProjectID: "prj_00000000000000000000000000000001",
		Type: domain.KnowledgeTypeDecision, RevisionNumber: 1, StateRevision: 2, Title: "Contact ordering", Body: "Sort by stable ID.",
		ContentHash: strings.Repeat("b", 64), ReviewStatus: domain.KnowledgeReviewAccepted, CurrencyStatus: domain.KnowledgeCurrencyCurrent,
		Confidence: domain.KnowledgeConfidenceHigh, VerificationStatus: domain.KnowledgeVerificationVerified,
		FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded, ProposedAt: "2026-08-13T10:00:00Z", ProposedBy: "local-owner", ProposedByType: domain.KnowledgeActorHuman,
		AcceptedAt: "2026-08-13T11:00:00Z", AcceptedBy: "local-owner", AcceptedByType: domain.KnowledgeActorHuman,
		Sources: []domain.KnowledgeSource{{Type: domain.KnowledgeSourceTask, ID: "task_00000000000000000000000000000001", Revision: 2, Role: domain.KnowledgeSourcePrimary, Ordinal: 0}},
	}
	search := domain.KnowledgeSearchResult{
		NormalizedQuery: "contact ordering", EvaluatedAt: "2026-08-13T12:00:00Z", CanonicalEventSequence: 9,
		RankPolicy: domain.KnowledgeSearchRankPolicy, Index: domain.KnowledgeIndexStatus{Status: domain.KnowledgeIndexOK, Generation: 1, BuiltAt: "2026-08-13T12:00:00Z", SourceEventSequence: 9, SourceCount: 1, SourceDigest: strings.Repeat("c", 64)},
		Matches: []domain.KnowledgeSearchMatch{{Ordinal: 1, Revision: revision, Explanation: domain.KnowledgeSearchExplanation{
			Scope:      domain.KnowledgeSearchScopeExplanation{Rank: 0, Reason: "project-wide"},
			Authority:  domain.KnowledgeSearchAuthorityExplanation{ReviewStatus: domain.KnowledgeReviewAccepted, CurrencyStatus: domain.KnowledgeCurrencyCurrent, AcceptedByType: domain.KnowledgeActorHuman, Reason: "accepted current"},
			Freshness:  domain.KnowledgeSearchFreshnessExplanation{Class: 0, EvaluatedAt: "2026-08-13T12:00:00Z", Reason: "until superseded"},
			Provenance: domain.KnowledgeSearchProvenanceExplanation{Rank: 0, Reason: "neutral", MatchedSourceIDs: []string{}},
			Quality:    domain.KnowledgeSearchQualityExplanation{Confidence: domain.KnowledgeConfidenceHigh, ConfidenceRank: 0, VerificationStatus: domain.KnowledgeVerificationVerified, VerificationRank: 0},
			Text:       domain.KnowledgeSearchTextExplanation{BM25: -1, TitleWeight: 8, BodyWeight: 1},
			TieBreaker: domain.KnowledgeSearchTieBreaker{AcceptedAt: revision.AcceptedAt, RevisionID: revision.ID},
		}}},
	}
	client := &fakeDaemonClient{knowledgeSearch: localapi.KnowledgeSearchResult{Schema: localapi.KnowledgeSearchSchema, Type: "knowledge_search", Search: search}}
	app, stdout, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	if exit := app.Run([]string{"knowledge", "search", "contact ordering", "--workspace", "personal", "--project", "demo", "--socket", "/tmp/crewfold.sock", "--output", "json"}); exit != ExitOK {
		t.Fatalf("search JSON exit=%d stderr=%q", exit, stderr.String())
	}
	var decoded localapi.KnowledgeSearchResult
	if err := json.Unmarshal(stdout.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	got := decoded.Search.Matches[0]
	if got.Revision.Body != revision.Body || len(got.Revision.Sources) != 1 || got.Explanation.Text.TitleWeight != 8 || got.Explanation.TieBreaker.RevisionID != revision.ID {
		t.Fatalf("search result lost revision/ranking detail: %#v", got)
	}
}

func TestKnowledgeIndexStatusAndRebuildUseExplicitWorkspace(t *testing.T) {
	t.Parallel()
	index := domain.KnowledgeIndexStatus{Status: domain.KnowledgeIndexOK, Generation: 3, BuiltAt: "2026-08-13T12:00:00Z", SourceEventSequence: 44, SourceCount: 7, SourceDigest: strings.Repeat("a", 64)}
	client := &fakeDaemonClient{
		knowledgeIndexStatus:  localapi.KnowledgeIndexStatusResult{Schema: localapi.KnowledgeIndexStatusSchema, Type: "knowledge_index_status", Index: index},
		knowledgeIndexRebuild: localapi.KnowledgeIndexRebuildResult{Schema: localapi.KnowledgeIndexRebuildSchema, Type: "knowledge_index_rebuild", Index: index},
	}
	app, stdout, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	if exit := app.Run([]string{"knowledge", "index", "status", "--workspace", "personal", "--socket", "/tmp/crewfold.sock"}); exit != ExitOK || !strings.Contains(stdout.String(), "generation: 3") {
		t.Fatalf("index status exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	if client.knowledgeIndexWorkspace != "personal" {
		t.Fatalf("index status workspace = %q", client.knowledgeIndexWorkspace)
	}
	app, stdout, stderr = newTestApp()
	app.newClient = func(string) daemonClient { return client }
	if exit := app.Run([]string{"knowledge", "index", "rebuild", "--workspace", "personal", "--idempotency-key", "rebuild-1", "--socket", "/tmp/crewfold.sock", "--output", "json"}); exit != ExitOK {
		t.Fatalf("index rebuild exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	if client.knowledgeRebuildParams.Workspace != "personal" || client.knowledgeRebuildParams.IdempotencyKey != "rebuild-1" {
		t.Fatalf("rebuild params = %#v", client.knowledgeRebuildParams)
	}
}

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
