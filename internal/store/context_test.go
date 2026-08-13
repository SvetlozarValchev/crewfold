package store

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func TestContextKnowledgeRevisionIDsAreOrderedBoundedAndUnique(t *testing.T) {
	t.Parallel()
	first := "krev_00000000000000000000000000000001"
	second := "krev_00000000000000000000000000000002"
	got, err := normalizeContextKnowledgeRevisionIDs([]string{" " + first + " ", second})
	if err != nil || !reflect.DeepEqual(got, []string{first, second}) {
		t.Fatalf("normalizeContextKnowledgeRevisionIDs() = %#v, %v", got, err)
	}
	for name, values := range map[string][]string{
		"duplicate": {first, first},
		"malformed": {"knowledge_revision"},
	} {
		if _, err := normalizeContextKnowledgeRevisionIDs(values); ErrorCode(err) != CodeInvalidContext {
			t.Errorf("normalizeContextKnowledgeRevisionIDs(%s) error = %v, code = %q", name, err, ErrorCode(err))
		}
	}
	tooMany := make([]string, maximumContextKnowledgeItems+1)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("krev_%032x", index)
	}
	if _, err := normalizeContextKnowledgeRevisionIDs(tooMany); ErrorCode(err) != CodeInvalidContext {
		t.Fatalf("normalizeContextKnowledgeRevisionIDs(too many) error = %v, code = %q", err, ErrorCode(err))
	}
}

func TestContextKnowledgeEligibilityReasonsAreExplicit(t *testing.T) {
	t.Parallel()
	const (
		workspaceID = "ws_00000000000000000000000000000001"
		projectID   = "prj_00000000000000000000000000000001"
		taskID      = "task_00000000000000000000000000000001"
		now         = "2026-08-13T12:00:00Z"
	)
	task := domain.Task{ID: taskID, ProjectID: projectID}
	eligible := domain.KnowledgeRevision{
		WorkspaceID: workspaceID, ProjectID: projectID,
		ReviewStatus: domain.KnowledgeReviewAccepted, CurrencyStatus: domain.KnowledgeCurrencyCurrent,
		FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded,
	}
	if code, reason, err := contextKnowledgeIneligibility(eligible, workspaceID, task, now); err != nil || code != "" || reason != "" {
		t.Fatalf("eligible revision classification = %q, %q, %v", code, reason, err)
	}
	cases := []struct {
		name string
		edit func(*domain.KnowledgeRevision)
		want string
	}{
		{name: "proposed", edit: func(value *domain.KnowledgeRevision) {
			value.ReviewStatus, value.CurrencyStatus = domain.KnowledgeReviewProposed, domain.KnowledgeCurrencyPending
		}, want: "proposed"},
		{name: "rejected", edit: func(value *domain.KnowledgeRevision) {
			value.ReviewStatus, value.CurrencyStatus = domain.KnowledgeReviewRejected, domain.KnowledgeCurrencyPending
		}, want: "rejected"},
		{name: "stale", edit: func(value *domain.KnowledgeRevision) { value.CurrencyStatus = domain.KnowledgeCurrencyStale }, want: "stale"},
		{name: "superseded", edit: func(value *domain.KnowledgeRevision) { value.CurrencyStatus = domain.KnowledgeCurrencySuperseded }, want: "superseded"},
		{name: "project scope", edit: func(value *domain.KnowledgeRevision) { value.ProjectID = "prj_00000000000000000000000000000002" }, want: "out_of_scope"},
		{name: "task scope", edit: func(value *domain.KnowledgeRevision) { value.TaskScopeID = "task_00000000000000000000000000000002" }, want: "out_of_scope"},
		{name: "expired", edit: func(value *domain.KnowledgeRevision) {
			value.FreshnessPolicy, value.FreshUntil = domain.KnowledgeFreshExpiresAt, now
		}, want: "stale"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			candidate := eligible
			test.edit(&candidate)
			code, reason, err := contextKnowledgeIneligibility(candidate, workspaceID, task, now)
			if err != nil || code != test.want || reason == "" {
				t.Fatalf("contextKnowledgeIneligibility() = %q, %q, %v; want %q", code, reason, err, test.want)
			}
		})
	}
}

func TestContextPacketPinsEligibleKnowledgeAndExplainsEveryExclusion(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project, agent, adjacent, assigned := initializeRunTest(t, storage, "explicit context knowledge")
	otherTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "other knowledge scope", "other-knowledge-scope")

	eligible := insertContextKnowledgeFixture(t, storage, contextKnowledgeFixture{
		ordinal: 1, workspaceID: workspace.ID, projectID: project.ID, sourceTaskID: assigned.Task.ID,
		reviewStatus: domain.KnowledgeReviewAccepted, currencyStatus: domain.KnowledgeCurrencyCurrent,
		body: "Accepted replacement-agent finding.",
	})
	proposed := insertContextKnowledgeFixture(t, storage, contextKnowledgeFixture{
		ordinal: 2, workspaceID: workspace.ID, projectID: project.ID, sourceTaskID: assigned.Task.ID,
		reviewStatus: domain.KnowledgeReviewProposed, currencyStatus: domain.KnowledgeCurrencyPending, body: "Unaccepted proposal.",
	})
	rejected := insertContextKnowledgeFixture(t, storage, contextKnowledgeFixture{
		ordinal: 3, workspaceID: workspace.ID, projectID: project.ID, sourceTaskID: assigned.Task.ID,
		reviewStatus: domain.KnowledgeReviewRejected, currencyStatus: domain.KnowledgeCurrencyPending, body: "Rejected proposal.",
	})
	stale := insertContextKnowledgeFixture(t, storage, contextKnowledgeFixture{
		ordinal: 4, workspaceID: workspace.ID, projectID: project.ID, sourceTaskID: assigned.Task.ID,
		reviewStatus: domain.KnowledgeReviewAccepted, currencyStatus: domain.KnowledgeCurrencyStale, body: "Stale finding.",
	})
	superseded := insertContextKnowledgeFixture(t, storage, contextKnowledgeFixture{
		ordinal: 5, workspaceID: workspace.ID, projectID: project.ID, sourceTaskID: assigned.Task.ID,
		reviewStatus: domain.KnowledgeReviewAccepted, currencyStatus: domain.KnowledgeCurrencySuperseded, body: "Superseded finding.",
	})
	replacement := insertContextKnowledgeSuccessorFixture(t, storage, 50, superseded, assigned.Task.ID)
	outOfScope := insertContextKnowledgeFixture(t, storage, contextKnowledgeFixture{
		ordinal: 6, workspaceID: workspace.ID, projectID: project.ID, taskScopeID: otherTask.Task.ID, sourceTaskID: otherTask.Task.ID,
		reviewStatus: domain.KnowledgeReviewAccepted, currencyStatus: domain.KnowledgeCurrencyCurrent, body: "Other task only.",
	})
	overBudget := insertContextKnowledgeFixture(t, storage, contextKnowledgeFixture{
		ordinal: 7, workspaceID: workspace.ID, projectID: project.ID, sourceTaskID: assigned.Task.ID,
		reviewStatus: domain.KnowledgeReviewAccepted, currencyStatus: domain.KnowledgeCurrencyCurrent,
		body: strings.Repeat("large accepted knowledge ", 650),
	})

	requested := []string{eligible, proposed, rejected, stale, superseded, outOfScope, overBudget}
	command := BuildContextCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, AgentIdentifier: agent.ID,
		CheckoutIdentifier: adjacent.ID, KnowledgeRevisionIDs: requested, ExpectedTaskRevision: assigned.Task.Revision,
		IdempotencyKey: "build-explicit-knowledge", CorrelationID: "request-build-explicit-knowledge",
	}
	built, err := storage.BuildContextPacket(context.Background(), command)
	if err != nil {
		t.Fatalf("BuildContextPacket() error = %v", err)
	}
	packet := built.Value
	if !reflect.DeepEqual(packet.RequestedKnowledgeRevisionIDs, requested) {
		t.Fatalf("requested context knowledge = %v, want %v", packet.RequestedKnowledgeRevisionIDs, requested)
	}
	if len(packet.AcceptedKnowledge) != 1 || packet.AcceptedKnowledge[0].ID != eligible || packet.AcceptedKnowledge[0].Body != "Accepted replacement-agent finding." || len(packet.AcceptedKnowledge[0].Sources) != 1 {
		t.Fatalf("accepted context knowledge = %#v", packet.AcceptedKnowledge)
	}
	wantReasons := map[string]string{proposed: "proposed", rejected: "rejected", stale: "stale", superseded: "superseded", outOfScope: "out_of_scope", overBudget: "over_budget"}
	for _, exclusion := range packet.Excluded {
		if exclusion.RequestedRevisionID == "" {
			continue
		}
		if want := wantReasons[exclusion.RequestedRevisionID]; exclusion.ReasonCode != want || exclusion.ByteSize <= 0 {
			t.Errorf("knowledge exclusion = %#v, want reason %q", exclusion, want)
		}
		delete(wantReasons, exclusion.RequestedRevisionID)
		if exclusion.RequestedRevisionID == superseded && exclusion.ReplacementRevisionID != replacement {
			t.Errorf("superseded exclusion replacement = %q, want %q", exclusion.ReplacementRevisionID, replacement)
		}
	}
	if len(wantReasons) != 0 {
		t.Fatalf("missing knowledge exclusions: %v", wantReasons)
	}
	if packet.Budget.Knowledge.UsedBytes <= 0 || packet.Budget.Knowledge.UsedBytes > maximumContextKnowledgeBytes || packet.Budget.Total.UsedBytes != packet.ByteSize {
		t.Fatalf("context budget = %#v, bytes = %d", packet.Budget, packet.ByteSize)
	}

	if _, err := storage.db.Exec(`UPDATE knowledge_revisions
SET currency_status = 'stale', state_revision = state_revision + 1,
    stale_at = ?, stale_by = 'owner', stale_by_type = 'human', stale_reason = 'fixture changed after packet build'
WHERE id = ?`, storage.nowText(), eligible); err != nil {
		t.Fatalf("mark fixture stale: %v", err)
	}
	replayed, err := storage.BuildContextPacket(context.Background(), command)
	if err != nil || !reflect.DeepEqual(replayed, built) {
		t.Fatalf("BuildContextPacket(replay after knowledge change) = %#v, %v; want frozen %#v", replayed, err, built)
	}
	changed := command
	changed.IdempotencyKey = "build-explicit-knowledge-after-stale"
	changed.CorrelationID = "request-build-explicit-knowledge-after-stale"
	changed.KnowledgeRevisionIDs = []string{eligible}
	newPacket, err := storage.BuildContextPacket(context.Background(), changed)
	if err != nil || len(newPacket.Value.AcceptedKnowledge) != 0 || !hasContextKnowledgeExclusion(newPacket.Value.Excluded, eligible, "stale") {
		t.Fatalf("BuildContextPacket(new after stale) = %#v, %v", newPacket, err)
	}

	conflict := command
	conflict.KnowledgeRevisionIDs = []string{proposed}
	if _, err := storage.BuildContextPacket(context.Background(), conflict); ErrorCode(err) != CodeIdempotencyConflict {
		t.Fatalf("BuildContextPacket(changed idempotent request) error = %v, code = %q", err, ErrorCode(err))
	}
	unknown := command
	unknown.IdempotencyKey = "build-unknown-knowledge"
	unknown.CorrelationID = "request-build-unknown-knowledge"
	unknown.KnowledgeRevisionIDs = []string{"krev_ffffffffffffffffffffffffffffffff"}
	if _, err := storage.BuildContextPacket(context.Background(), unknown); ErrorCode(err) != CodeKnowledgeNotFound {
		t.Fatalf("BuildContextPacket(unknown knowledge) error = %v, code = %q", err, ErrorCode(err))
	}
}

type contextKnowledgeFixture struct {
	ordinal        int
	workspaceID    string
	projectID      string
	taskScopeID    string
	sourceTaskID   string
	reviewStatus   string
	currencyStatus string
	body           string
}

func insertContextKnowledgeFixture(t *testing.T, storage *Store, fixture contextKnowledgeFixture) string {
	t.Helper()
	itemID := fmt.Sprintf("know_%032x", fixture.ordinal)
	revisionID := fmt.Sprintf("krev_%032x", fixture.ordinal)
	now := storage.nowText()
	if _, err := storage.db.Exec(`INSERT INTO knowledge_items(
id, workspace_id, project_id, task_scope_id, type, created_at, created_by, created_by_type)
VALUES (?, ?, ?, NULLIF(?, ''), 'finding', ?, 'owner', 'human')`, itemID, fixture.workspaceID, fixture.projectID, fixture.taskScopeID, now); err != nil {
		t.Fatalf("insert knowledge item: %v", err)
	}
	acceptedAt, acceptedBy, acceptedByType := any(nil), any(nil), any(nil)
	rejectedAt, rejectedBy, rejectedByType := any(nil), any(nil), any(nil)
	staleAt, staleBy, staleByType, staleReason := any(nil), any(nil), any(nil), any(nil)
	if fixture.reviewStatus == domain.KnowledgeReviewAccepted {
		acceptedAt, acceptedBy, acceptedByType = now, "owner", domain.KnowledgeActorHuman
	}
	if fixture.reviewStatus == domain.KnowledgeReviewRejected {
		rejectedAt, rejectedBy, rejectedByType = now, "owner", domain.KnowledgeActorHuman
	}
	if fixture.currencyStatus == domain.KnowledgeCurrencyStale {
		staleAt, staleBy, staleByType, staleReason = now, "owner", domain.KnowledgeActorHuman, "fixture stale"
	}
	if _, err := storage.db.Exec(`INSERT INTO knowledge_revisions(
id, item_id, revision_number, state_revision, title, body, content_hash,
review_status, currency_status, confidence, verification_status, freshness_policy,
proposed_at, proposed_by, proposed_by_type,
accepted_at, accepted_by, accepted_by_type, rejected_at, rejected_by, rejected_by_type,
stale_at, stale_by, stale_by_type, stale_reason)
VALUES (?, ?, 1, 1, 'Context fixture', ?, ?, ?, ?, 'high', 'verified', 'until_superseded',
?, 'owner', 'human', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		revisionID, itemID, fixture.body, strings.Repeat("a", 64), fixture.reviewStatus, fixture.currencyStatus,
		now, acceptedAt, acceptedBy, acceptedByType, rejectedAt, rejectedBy, rejectedByType, staleAt, staleBy, staleByType, staleReason); err != nil {
		t.Fatalf("insert knowledge revision: %v", err)
	}
	var sourceRevision int64
	if err := storage.db.QueryRow("SELECT revision FROM tasks WHERE id = ?", fixture.sourceTaskID).Scan(&sourceRevision); err != nil {
		t.Fatalf("read source task revision: %v", err)
	}
	if _, err := storage.db.Exec(`INSERT INTO knowledge_sources(revision_id, ordinal, source_type, source_id, source_revision, role)
VALUES (?, 0, 'task', ?, ?, 'primary')`, revisionID, fixture.sourceTaskID, sourceRevision); err != nil {
		t.Fatalf("insert knowledge source: %v", err)
	}
	return revisionID
}

func insertContextKnowledgeSuccessorFixture(t *testing.T, storage *Store, ordinal int, predecessorID, sourceTaskID string) string {
	t.Helper()
	revisionID := fmt.Sprintf("krev_%032x", ordinal)
	now := storage.nowText()
	var itemID string
	if err := storage.db.QueryRow("SELECT item_id FROM knowledge_revisions WHERE id = ?", predecessorID).Scan(&itemID); err != nil {
		t.Fatalf("read predecessor knowledge item: %v", err)
	}
	if _, err := storage.db.Exec(`INSERT INTO knowledge_revisions(
id, item_id, revision_number, state_revision, title, body, content_hash,
review_status, currency_status, confidence, verification_status, freshness_policy,
supersedes_revision_id, proposed_at, proposed_by, proposed_by_type,
accepted_at, accepted_by, accepted_by_type)
VALUES (?, ?, 2, 2, 'Context replacement fixture', 'Current replacement finding.', ?,
'accepted', 'current', 'high', 'verified', 'until_superseded', ?, ?, 'owner', 'human', ?, 'owner', 'human')`,
		revisionID, itemID, strings.Repeat("b", 64), predecessorID, now, now); err != nil {
		t.Fatalf("insert successor knowledge revision: %v", err)
	}
	var sourceRevision int64
	if err := storage.db.QueryRow("SELECT revision FROM tasks WHERE id = ?", sourceTaskID).Scan(&sourceRevision); err != nil {
		t.Fatalf("read successor source task revision: %v", err)
	}
	if _, err := storage.db.Exec(`INSERT INTO knowledge_sources(revision_id, ordinal, source_type, source_id, source_revision, role)
VALUES (?, 0, 'task', ?, ?, 'primary')`, revisionID, sourceTaskID, sourceRevision); err != nil {
		t.Fatalf("insert successor knowledge source: %v", err)
	}
	return revisionID
}

func hasContextKnowledgeExclusion(exclusions []domain.ContextExclusion, revisionID, reasonCode string) bool {
	for _, exclusion := range exclusions {
		if exclusion.RequestedRevisionID == revisionID && exclusion.ReasonCode == reasonCode {
			return true
		}
	}
	return false
}

func TestContextPacketBindingReportsArtifactsAndScopeAreDurable(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project, agent, adjacent, assigned := initializeRunTest(t, storage, "scoped context")
	command := BuildContextCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, AgentIdentifier: agent.ID,
		CheckoutIdentifier: adjacent.ID, ExpectedTaskRevision: assigned.Task.Revision,
		IdempotencyKey: "build-scoped-context", CorrelationID: "request-build-scoped-context",
	}
	built, err := storage.BuildContextPacket(context.Background(), command)
	if err != nil {
		t.Fatalf("BuildContextPacket() error = %v", err)
	}
	replayed, err := storage.BuildContextPacket(context.Background(), command)
	if err != nil || !reflect.DeepEqual(replayed, built) {
		t.Fatalf("BuildContextPacket(replay) = %#v, %v; want %#v", replayed, err, built)
	}
	packet := built.Value
	if packet.Schema != domain.ContextPacketSchema || packet.ContentHash == "" || packet.ByteSize <= 0 || packet.ByteSize > maximumContextBytes || packet.Task.Revision != assigned.Task.Revision || packet.Role.Revision != agent.Revision || packet.Checkout.Revision != adjacent.Revision || packet.RequestedKnowledgeRevisionIDs == nil || len(packet.RequestedKnowledgeRevisionIDs) != 0 || packet.AcceptedKnowledge == nil || len(packet.AcceptedKnowledge) != 0 {
		t.Fatalf("context packet = %#v", packet)
	}
	if packet.Budget.Total.LimitBytes != maximumContextBytes || packet.Budget.Total.UsedBytes != packet.ByteSize || packet.Budget.Total.RemainingBytes != maximumContextBytes-packet.ByteSize || packet.Budget.Knowledge.LimitBytes != maximumContextKnowledgeBytes || packet.Budget.Knowledge.UsedBytes != 0 || packet.Budget.Knowledge.RemainingBytes != maximumContextKnowledgeBytes {
		t.Fatalf("context packet budget = %#v", packet.Budget)
	}
	if _, err := storage.db.ExecContext(context.Background(), "UPDATE context_packets SET packet_json = packet_json WHERE id = ?", packet.ID); err == nil {
		t.Fatal("updating immutable context packet storage unexpectedly succeeded")
	}
	if _, err := storage.db.ExecContext(context.Background(), "DELETE FROM context_packets WHERE id = ?", packet.ID); err == nil {
		t.Fatal("deleting immutable context packet storage unexpectedly succeeded")
	}
	wantedExclusions := map[string]bool{"accepted_knowledge": false, "messages": false, "claims": false, "transcripts": false}
	for _, exclusion := range packet.Excluded {
		if _, exists := wantedExclusions[exclusion.Section]; exists {
			wantedExclusions[exclusion.Section] = true
		}
	}
	for section, found := range wantedExclusions {
		if !found {
			t.Errorf("context exclusion %q is absent", section)
		}
	}
	secondCommand := command
	secondCommand.IdempotencyKey = "build-equivalent-scoped-context"
	secondCommand.CorrelationID = "request-build-equivalent-scoped-context"
	equivalent, err := storage.BuildContextPacket(context.Background(), secondCommand)
	if err != nil || equivalent.Value.ID == packet.ID || equivalent.Value.AsOfEventSequence <= packet.AsOfEventSequence || !reflect.DeepEqual(equivalent.Value.Included, packet.Included) || !reflect.DeepEqual(equivalent.Value.Excluded, packet.Excluded) {
		t.Fatalf("BuildContextPacket(equivalent) = %#v, %v; want different identity, later cursor, and stable selection", equivalent, err)
	}
	explanation, err := storage.ExplainContextPacket(context.Background(), workspace.ID, packet.ID)
	if err != nil || explanation.ContentHash != packet.ContentHash || explanation.ByteSize != packet.ByteSize || !reflect.DeepEqual(explanation.Included, packet.Included) || !reflect.DeepEqual(explanation.Excluded, packet.Excluded) || !reflect.DeepEqual(explanation.Budget, packet.Budget) {
		t.Fatalf("ExplainContextPacket() = %#v, %v", explanation, err)
	}
	legacy := packet
	legacy.Schema, legacy.ID = domain.ContextPacketSchemaV2, "ctx_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	legacy.RequestedKnowledgeRevisionIDs, legacy.AcceptedKnowledge, legacy.Budget = nil, nil, domain.ContextBudget{}
	legacy.ByteSize = 0
	var legacyJSON []byte
	for range 8 {
		legacyJSON, err = json.Marshal(legacy)
		if err != nil {
			t.Fatalf("marshal legacy context packet: %v", err)
		}
		if len(legacyJSON) == legacy.ByteSize {
			break
		}
		legacy.ByteSize = len(legacyJSON)
	}
	legacyJSON, err = json.Marshal(legacy)
	if err != nil || len(legacyJSON) != legacy.ByteSize {
		t.Fatalf("legacy context byte accounting = %d, %d, %v", len(legacyJSON), legacy.ByteSize, err)
	}
	if _, err := storage.db.Exec(`INSERT INTO context_packets(
id, workspace_id, project_id, task_id, agent_id, checkout_id, packet_json,
content_hash, byte_size, created_at, created_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		legacy.ID, legacy.WorkspaceID, legacy.ProjectID, legacy.TaskID, legacy.AgentID, legacy.CheckoutID,
		string(legacyJSON), legacy.ContentHash, legacy.ByteSize, legacy.CreatedAt, legacy.CreatedBy); err != nil {
		t.Fatalf("insert legacy context packet: %v", err)
	}
	loadedLegacy, err := storage.ContextPacket(context.Background(), workspace.ID, legacy.ID)
	if err != nil || loadedLegacy.Schema != domain.ContextPacketSchemaV2 || loadedLegacy.Budget.Total.LimitBytes != 0 {
		t.Fatalf("ContextPacket(legacy) = %#v, %v", loadedLegacy, err)
	}
	legacyExplanation, err := storage.ExplainContextPacket(context.Background(), workspace.ID, legacy.ID)
	if err != nil || legacyExplanation.Budget.Total.UsedBytes != legacy.ByteSize || legacyExplanation.Budget.Total.LimitBytes != maximumContextBytes || legacyExplanation.Budget.Knowledge.LimitBytes != maximumContextKnowledgeBytes {
		t.Fatalf("ExplainContextPacket(legacy) = %#v, %v", legacyExplanation, err)
	}

	otherTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "other scoped task", "other-scoped-task")
	otherAssigned, err := storage.AssignTask(context.Background(), AssignTaskCommand{WorkspaceIdentifier: workspace.ID, TaskID: otherTask.Task.ID, AgentIdentifier: agent.ID, LeaseSeconds: 300, ExpectedRevision: otherTask.Task.Revision, IdempotencyKey: "assign-other-scoped-task", CorrelationID: "request-assign-other-scoped-task"})
	if err != nil {
		t.Fatalf("AssignTask(other) error = %v", err)
	}
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "scoped-context", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "working"}}}
	if _, err := storage.CreateRun(context.Background(), CreateRunCommand{WorkspaceIdentifier: workspace.ID, TaskID: otherAssigned.Detail.Task.ID, ContextPacketID: packet.ID, Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: otherAssigned.Detail.Task.Revision, IdempotencyKey: "wrong-context-run", CorrelationID: "request-wrong-context-run"}); ErrorCode(err) != CodeInvalidContext {
		t.Fatalf("CreateRun(wrong context) error = %v, code = %q", err, ErrorCode(err))
	}

	created, err := storage.CreateRun(context.Background(), CreateRunCommand{WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, ContextPacketID: packet.ID, Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: assigned.Task.Revision, IdempotencyKey: "scoped-context-run", CorrelationID: "request-scoped-context-run"})
	if err != nil || created.Detail.Run.ContextPacketID != packet.ID {
		t.Fatalf("CreateRun(scoped) = %#v, %v", created, err)
	}
	if _, err := storage.AuthorizeRunCapability(context.Background(), created.Detail.Run.ID); ErrorCode(err) != CodeCapabilityInactive {
		t.Fatalf("AuthorizeRunCapability(requested) error = %v, code = %q", err, ErrorCode(err))
	}
	starting, err := storage.MarkRunStarting(context.Background(), created.Detail.Run.ID, "worker-starting-scoped")
	if err != nil {
		t.Fatalf("MarkRunStarting() error = %v", err)
	}
	briefing, err := storage.AuthorizeRunCapability(context.Background(), starting.ID)
	if err != nil || briefing.Packet.ID != packet.ID || briefing.Run.ID != starting.ID || briefing.Resource != "crewfold://runs/"+starting.ID+"/briefing" {
		t.Fatalf("AuthorizeRunCapability() = %#v, %v", briefing, err)
	}
	reportCommand := CreateRunReportCommand{RunID: starting.ID, Kind: domain.ObservationProgress, Message: "implemented slice", Evidence: []string{"tests_passed"}, Payload: map[string]any{"next": []string{}}, IdempotencyKey: "scoped-progress"}
	report, err := storage.SubmitRunReport(context.Background(), reportCommand)
	if err != nil {
		t.Fatalf("SubmitRunReport() error = %v", err)
	}
	reportReplay, err := storage.SubmitRunReport(context.Background(), reportCommand)
	if err != nil || reportReplay.ID != report.ID {
		t.Fatalf("SubmitRunReport(replay) = %#v, %v; want ID %s", reportReplay, err, report.ID)
	}
	changedReport := reportCommand
	changedReport.Message = "different content"
	if _, err := storage.SubmitRunReport(context.Background(), changedReport); ErrorCode(err) != CodeIdempotencyConflict {
		t.Fatalf("SubmitRunReport(conflict) error = %v, code = %q", err, ErrorCode(err))
	}
	artifactCommand := PublishRunArtifactCommand{RunID: starting.ID, Name: "test evidence", MediaType: "text/plain", Content: "bounded evidence", IdempotencyKey: "scoped-artifact"}
	artifact, err := storage.PublishRunArtifact(context.Background(), artifactCommand)
	artifactReplay, replayErr := storage.PublishRunArtifact(context.Background(), artifactCommand)
	if err != nil || replayErr != nil || artifact.ID == "" || artifactReplay.ID != artifact.ID || artifact.ContentHash == "" {
		t.Fatalf("PublishRunArtifact() = %#v, %v; replay = %#v, %v", artifact, err, artifactReplay, replayErr)
	}
	active, err := storage.MarkRunStarted(context.Background(), starting.ID, "runtime", "provider", "worker-started-scoped")
	if err != nil || active.Run.Status != domain.RunActive {
		t.Fatalf("MarkRunStarted() = %#v, %v", active, err)
	}
	pending, found, err := storage.NextPendingRunReport(context.Background(), active.Run.ID)
	if err != nil || !found || pending.ID != report.ID {
		t.Fatalf("NextPendingRunReport() = %#v, %t, %v", pending, found, err)
	}
	applied, err := storage.ApplyQueuedRunReport(context.Background(), active.Run.ID, pending.ID, true, nil, "worker-apply-scoped")
	if err != nil || applied.Run.StepCursor != 1 {
		t.Fatalf("ApplyQueuedRunReport() = %#v, %v", applied, err)
	}
	reportAfterApply, err := storage.SubmitRunReport(context.Background(), reportCommand)
	if err != nil || reportAfterApply.ID != report.ID || reportAfterApply.Status != "applied" {
		t.Fatalf("SubmitRunReport(replay after apply) = %#v, %v", reportAfterApply, err)
	}
	if _, found, err := storage.NextPendingRunReport(context.Background(), active.Run.ID); err != nil || found {
		t.Fatalf("NextPendingRunReport(after apply) found = %t, error = %v", found, err)
	}
}

func TestRunCapabilityExpiresAndBecomesInactiveAfterStop(t *testing.T) {
	base := time.Date(2026, time.August, 12, 12, 0, 0, 0, time.UTC)
	now := base
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return now }})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "expiring capability")
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "expiring-capability", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "waiting"}}}
	created, err := storage.CreateRun(context.Background(), CreateRunCommand{WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: assigned.Task.Revision, CapabilityTTL: time.Second, IdempotencyKey: "expiring-capability-run", CorrelationID: "request-expiring-capability-run"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	starting, _ := storage.MarkRunStarting(context.Background(), created.Detail.Run.ID, "worker-starting-expiry")
	if _, err := storage.AuthorizeRunCapability(context.Background(), starting.ID); err != nil {
		t.Fatalf("AuthorizeRunCapability(before expiry) error = %v", err)
	}
	now = base.Add(time.Second)
	if _, err := storage.AuthorizeRunCapability(context.Background(), starting.ID); ErrorCode(err) != CodeCapabilityExpired {
		t.Fatalf("AuthorizeRunCapability(expired) error = %v, code = %q", err, ErrorCode(err))
	}

	now = base
	secondStorage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return now }})
	secondWorkspace, _, _, _, secondAssigned := initializeRunTest(t, secondStorage, "stopped capability")
	second, err := secondStorage.CreateRun(context.Background(), CreateRunCommand{WorkspaceIdentifier: secondWorkspace.ID, TaskID: secondAssigned.Task.ID, Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: secondAssigned.Task.Revision, IdempotencyKey: "stopped-capability-run", CorrelationID: "request-stopped-capability-run"})
	if err != nil {
		t.Fatalf("CreateRun(second) error = %v", err)
	}
	secondStarting, _ := secondStorage.MarkRunStarting(context.Background(), second.Detail.Run.ID, "worker-starting-stop")
	active, _ := secondStorage.MarkRunStarted(context.Background(), secondStarting.ID, "runtime", "provider", "worker-started-stop")
	stopping, err := secondStorage.RequestRunStop(context.Background(), StopRunCommand{WorkspaceIdentifier: secondWorkspace.ID, RunID: active.Run.ID, ExpectedRevision: active.Run.Revision, GracePeriodMillis: 100, IdempotencyKey: "stop-scoped-run", CorrelationID: "request-stop-scoped-run"})
	if err != nil {
		t.Fatalf("RequestRunStop() error = %v", err)
	}
	if _, err := secondStorage.MarkRunStopped(context.Background(), stopping.Detail.Run.ID, false, "stopped", "worker-stopped-scoped"); err != nil {
		t.Fatalf("MarkRunStopped() error = %v", err)
	}
	if _, err := secondStorage.AuthorizeRunCapability(context.Background(), second.Detail.Run.ID); ErrorCode(err) != CodeCapabilityInactive {
		t.Fatalf("AuthorizeRunCapability(stopped) error = %v, code = %q", err, ErrorCode(err))
	}
}
