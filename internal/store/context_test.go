package store

import (
	"context"
	"crypto/sha256"
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

func TestManagerToolsAreDerivedInCanonicalOrder(t *testing.T) {
	t.Parallel()
	allKinds := []string{
		domain.ManagerProposalAssignment,
		domain.ManagerProposalEscalation,
		domain.ManagerProposalReview,
		domain.ManagerProposalTaskDecomposition,
	}
	wantSuffix := []string{
		"crewfold_propose_assignment",
		"crewfold_propose_escalation",
		"crewfold_propose_review",
		"crewfold_propose_tasks",
	}
	tools := managerAllowedTools(allKinds)
	if !reflect.DeepEqual(tools[:len(runScopedTools)], runScopedTools) || !reflect.DeepEqual(tools[len(runScopedTools):], wantSuffix) {
		t.Fatalf("manager tools = %v, want run-scoped base plus %v", tools, wantSuffix)
	}
	for index, kind := range allKinds {
		tools := managerAllowedTools([]string{kind})
		if len(tools) != len(runScopedTools)+1 || tools[len(runScopedTools)] != wantSuffix[index] {
			t.Errorf("manager tools for %q = %v", kind, tools)
		}
	}
	if !reflect.DeepEqual(managerAllowedTools(nil), runScopedTools) {
		t.Fatal("empty manager kinds changed the run-scoped tool base")
	}
}

func TestCheckWatchToolsAreDerivedInCanonicalOrder(t *testing.T) {
	t.Parallel()
	operations := []string{domain.CheckWatchOperationRun, domain.CheckWatchOperationInspect, domain.CheckWatchOperationProposeRepair}
	wantSuffix := []string{"crewfold_run_check", "crewfold_list_check_results", "crewfold_inspect_check_result", "crewfold_propose_check_repair"}
	tools := checkWatchAllowedTools(operations)
	if !reflect.DeepEqual(tools[:len(runScopedTools)], runScopedTools) || !reflect.DeepEqual(tools[len(runScopedTools):], wantSuffix) {
		t.Fatalf("check-watch tools = %v, want run-scoped base plus %v", tools, wantSuffix)
	}
	for index, operation := range operations {
		tools := checkWatchAllowedTools([]string{operation})
		want := checkWatchOperationTools[index].tools
		if !reflect.DeepEqual(tools[:len(runScopedTools)], runScopedTools) || !reflect.DeepEqual(tools[len(runScopedTools):], want) {
			t.Errorf("check-watch tools for %q = %v, want run-scoped base plus %v", operation, tools, want)
		}
	}
	if !reflect.DeepEqual(checkWatchAllowedTools(nil), runScopedTools) {
		t.Fatal("empty check-watch operations changed the run-scoped tool base")
	}
}

func TestCheckWatchGrantValidationIsExactAndRoleAgnostic(t *testing.T) {
	t.Parallel()
	packet := domain.ContextPacket{
		Schema: domain.ContextPacketSchema, WorkspaceID: "ws_exact", ProjectID: "prj_exact", AgentID: "agent_exact",
		Role:     domain.ContextRole{AgentID: "agent_exact", Revision: 7, Role: "arbitrary evidence poet"},
		Included: []domain.ContextSelection{{Section: "check_watch_grant", EntityType: "check_watch_grant", EntityID: "checkgrant_exact", Revision: 3}},
		CheckWatchGrant: &domain.ContextCheckWatchGrant{
			Schema: domain.ContextCheckWatchGrantSchema, GrantID: "checkgrant_exact", GrantRevision: 3,
			WorkspaceID: "ws_exact", ProjectID: "prj_exact", WatcherAgentID: "agent_exact", WatcherAgentRevision: 7,
			Operations: []string{domain.CheckWatchOperationRun, domain.CheckWatchOperationInspect, domain.CheckWatchOperationProposeRepair},
			Definitions: []domain.CheckWatchGrantDefinition{
				{DefinitionID: "checkdef_a", ContentRevision: 2, DefinitionSHA256: strings.Repeat("a", 64)},
				{DefinitionID: "checkdef_b", ContentRevision: 4, DefinitionSHA256: strings.Repeat("b", 64)},
			},
			MaxPending: 8, MaxInFlight: 2, ContentSHA256: strings.Repeat("c", 64),
		},
	}
	if err := validateContextCheckWatchGrant(packet); err != nil {
		t.Fatalf("valid check-watch grant rejected: %v", err)
	}
	packet.Role.Role = "another same arbitrary role"
	if err := validateContextCheckWatchGrant(packet); err != nil {
		t.Fatalf("role label affected check-watch authority: %v", err)
	}
	for name, mutate := range map[string]func(*domain.ContextCheckWatchGrant){
		"wrong agent revision": func(grant *domain.ContextCheckWatchGrant) { grant.WatcherAgentRevision++ },
		"unsorted operations": func(grant *domain.ContextCheckWatchGrant) {
			grant.Operations = []string{domain.CheckWatchOperationInspect, domain.CheckWatchOperationRun}
		},
		"duplicate definition": func(grant *domain.ContextCheckWatchGrant) {
			grant.Definitions = append(grant.Definitions, grant.Definitions[len(grant.Definitions)-1])
		},
		"forged definition hash": func(grant *domain.ContextCheckWatchGrant) {
			grant.Definitions[0].DefinitionSHA256 = strings.Repeat("g", 64)
		},
		"in-flight above pending": func(grant *domain.ContextCheckWatchGrant) { grant.MaxInFlight = grant.MaxPending + 1 },
		"mixed authority":         func(grant *domain.ContextCheckWatchGrant) {},
	} {
		candidate := packet
		grant := *packet.CheckWatchGrant
		grant.Operations = append([]string(nil), grant.Operations...)
		grant.Definitions = append([]domain.CheckWatchGrantDefinition(nil), grant.Definitions...)
		mutate(&grant)
		candidate.CheckWatchGrant = &grant
		if name == "mixed authority" {
			candidate.ManagementGrant = &domain.ContextManagerGrant{}
			if err := validateLiveContextPacket(candidate); err == nil {
				t.Errorf("mixed delegated authority unexpectedly validated")
			}
			continue
		}
		if err := validateContextCheckWatchGrant(candidate); err == nil {
			t.Errorf("%s check-watch grant unexpectedly validated", name)
		}
	}
}

func TestCreateRunBuildsCheckWatchAuthorityOnlyFromExplicitExactGrant(t *testing.T) {
	current := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return current }})
	workspace, project, agent, checkout, assigned := initializeRunTest(t, storage, "explicit check authority")
	grant := insertContextCheckWatchGrantFixture(t, storage, workspace.ID, project.ID, agent, current.Add(6*time.Hour), []string{
		domain.CheckWatchOperationRun, domain.CheckWatchOperationInspect, domain.CheckWatchOperationProposeRepair,
	})
	scenario := domain.FakeScenario{
		Schema: "urn:crewfold:schema:fixture:fake-run-scenario:v1", Name: "explicit-check-authority",
		Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "exercise exact watcher authority"}},
	}
	created, err := storage.CreateRun(context.Background(), CreateRunCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, CheckoutIdentifier: checkout.ID,
		Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: assigned.Task.Revision,
		CheckWatchGrantID: grant.ID, ExpectedCheckWatchGrantRevision: grant.Revision,
		CapabilityTTL: 12 * time.Hour, IdempotencyKey: "explicit-check-authority", CorrelationID: "request-explicit-check-authority",
	})
	if err != nil {
		t.Fatalf("CreateRun(explicit check authority) = %v", err)
	}
	packet, err := storage.ContextPacket(context.Background(), workspace.ID, created.Detail.Run.ContextPacketID)
	if err != nil {
		t.Fatalf("ContextPacket(check authority) = %v", err)
	}
	if packet.Schema != domain.ContextPacketSchema || packet.ManagementGrant != nil || packet.CheckWatchGrant == nil ||
		packet.CheckWatchGrant.GrantID != grant.ID || packet.CheckWatchGrant.GrantRevision != grant.Revision ||
		!reflect.DeepEqual(packet.Policy.AllowedTools, checkWatchAllowedTools(grant.Operations)) {
		t.Fatalf("explicit run did not freeze exact check authority: %#v", packet)
	}
	if _, err := storage.CreateRun(context.Background(), CreateRunCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, Runtime: "fake", Provider: "fake", Scenario: scenario,
		ExpectedTaskRevision: assigned.Task.Revision, CheckWatchGrantID: grant.ID,
		IdempotencyKey: "missing-grant-revision", CorrelationID: "request-missing-grant-revision",
	}); ErrorCode(err) != CodeInvalidRun {
		t.Fatalf("CreateRun(unpaired grant) error = %v, code = %q", err, ErrorCode(err))
	}
	changed := CreateRunCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, CheckoutIdentifier: checkout.ID,
		Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: assigned.Task.Revision,
		CheckWatchGrantID: grant.ID, ExpectedCheckWatchGrantRevision: grant.Revision + 1,
		CapabilityTTL: 12 * time.Hour, IdempotencyKey: "explicit-check-authority", CorrelationID: "request-explicit-check-authority",
	}
	if _, err := storage.CreateRun(context.Background(), changed); ErrorCode(err) != CodeIdempotencyConflict {
		t.Fatalf("CreateRun(changed exact grant under replay key) error = %v, code = %q", err, ErrorCode(err))
	}
}

func TestCreateRunRejectsCallerSuppliedCheckWatchAuthorityWithoutExplicitGrant(t *testing.T) {
	current := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return current }})
	workspace, project, agent, checkout, assigned := initializeRunTest(t, storage, "supplied check authority")
	grant := insertContextCheckWatchGrantFixture(t, storage, workspace.ID, project.ID, agent, current.Add(6*time.Hour), []string{domain.CheckWatchOperationRun})
	tx, err := storage.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	packet, _, err := storage.buildCheckWatchContextPacketInTransaction(context.Background(), tx, workspace.ID, assigned.Task, agent, checkout, grant, "build-unbound-check-authority", storage.nowText())
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("buildCheckWatchContextPacketInTransaction() = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit unbound check-authority packet: %v", err)
	}
	scenario := domain.FakeScenario{
		Schema: "urn:crewfold:schema:fixture:fake-run-scenario:v1", Name: "supplied-check-authority",
		Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "must not ambiently inherit authority"}},
	}
	if _, err := storage.CreateRun(context.Background(), CreateRunCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, ContextPacketID: packet.ID,
		Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: assigned.Task.Revision,
		IdempotencyKey: "supply-check-authority", CorrelationID: "request-supply-check-authority",
	}); ErrorCode(err) != CodeInvalidContext {
		t.Fatalf("CreateRun(supplied check authority without grant) error = %v, code = %q", err, ErrorCode(err))
	}
}

func TestCheckWatchLiveAuthorityRevalidationMatrix(t *testing.T) {
	for _, test := range []struct {
		name       string
		operations []string
		operation  string
		mutate     func(*testing.T, *Store, *time.Time, domain.CheckWatchGrant, domain.AgentDefinition)
	}{
		{
			name: "revoked exact grant", operations: []string{domain.CheckWatchOperationRun}, operation: domain.CheckWatchOperationRun,
			mutate: func(t *testing.T, storage *Store, current *time.Time, grant domain.CheckWatchGrant, _ domain.AgentDefinition) {
				*current = current.Add(time.Second)
				storage.checkMutationSealActive.Store(true)
				defer storage.checkMutationSealActive.Store(false)
				if _, err := storage.db.Exec(`UPDATE check_watch_grants SET status='revoked', revision=revision+1,
updated_at=?, updated_by='local-owner' WHERE id=?`, current.Format(time.RFC3339Nano), grant.ID); err != nil {
					t.Fatalf("revoke check-watch grant fixture: %v", err)
				}
			},
		},
		{
			name: "expired exact grant", operations: []string{domain.CheckWatchOperationRun}, operation: domain.CheckWatchOperationRun,
			mutate: func(_ *testing.T, _ *Store, current *time.Time, _ domain.CheckWatchGrant, _ domain.AgentDefinition) {
				*current = current.Add(2 * time.Hour)
			},
		},
		{
			name: "same role but newer agent revision", operations: []string{domain.CheckWatchOperationRun}, operation: domain.CheckWatchOperationRun,
			mutate: func(t *testing.T, storage *Store, current *time.Time, _ domain.CheckWatchGrant, agent domain.AgentDefinition) {
				*current = current.Add(time.Second)
				sameRole := agent.Role
				if _, err := storage.UpdateAgent(context.Background(), UpdateAgentCommand{
					WorkspaceIdentifier: agent.WorkspaceID, AgentIdentifier: agent.ID, Role: &sameRole,
					ExpectedRevision: agent.Revision, IdempotencyKey: "same-role-new-revision", CorrelationID: "request-same-role-new-revision",
				}); err != nil {
					t.Fatalf("UpdateAgent(same role) = %v", err)
				}
			},
		},
		{
			name: "operation absent from exact grant", operations: []string{domain.CheckWatchOperationRun}, operation: domain.CheckWatchOperationInspect,
			mutate: func(_ *testing.T, _ *Store, _ *time.Time, _ domain.CheckWatchGrant, _ domain.AgentDefinition) {},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
			storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return current }})
			workspace, project, agent, checkout, assigned := initializeRunTest(t, storage, "check authority revalidation "+test.name)
			expiresAt := current.Add(time.Hour)
			grant := insertContextCheckWatchGrantFixture(t, storage, workspace.ID, project.ID, agent, expiresAt, test.operations)
			scenario := domain.FakeScenario{
				Schema: "urn:crewfold:schema:fixture:fake-run-scenario:v1", Name: "check-live-revalidation",
				Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "authority is exact and current"}},
			}
			created, err := storage.CreateRun(context.Background(), CreateRunCommand{
				WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, CheckoutIdentifier: checkout.ID,
				Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: assigned.Task.Revision,
				CheckWatchGrantID: grant.ID, ExpectedCheckWatchGrantRevision: grant.Revision,
				CapabilityTTL: 12 * time.Hour, IdempotencyKey: "check-live-revalidation", CorrelationID: "request-check-live-revalidation",
			})
			if err != nil {
				t.Fatalf("CreateRun(check authority) = %v", err)
			}
			if _, err := storage.MarkRunStarting(context.Background(), created.Detail.Run.ID, "check-live-revalidation-starting"); err != nil {
				t.Fatalf("MarkRunStarting(check authority) = %v", err)
			}
			if _, err := storage.AuthorizeRunCheckWatchGrant(context.Background(), created.Detail.Run.ID, domain.CheckWatchOperationRun); err != nil {
				t.Fatalf("initial exact check-watch authorization = %v", err)
			}
			test.mutate(t, storage, &current, grant, agent)
			if _, err := storage.ContextPacket(context.Background(), workspace.ID, created.Detail.Run.ContextPacketID); err != nil {
				t.Fatalf("frozen packet became unreadable after authority change: %v", err)
			}
			if _, err := storage.AuthorizeRunCapability(context.Background(), created.Detail.Run.ID); err != nil {
				t.Fatalf("ordinary bound run became invalid after authority change: %v", err)
			}
			if _, err := storage.AuthorizeRunCheckWatchGrant(context.Background(), created.Detail.Run.ID, test.operation); ErrorCode(err) != CodeCheckWatchGrantDenied {
				t.Fatalf("AuthorizeRunCheckWatchGrant(%s) error = %v, code = %q", test.operation, err, ErrorCode(err))
			}
		})
	}
}

func TestSameRoleDoesNotConferCheckWatchAuthority(t *testing.T) {
	current := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return current }})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "same role no grant")
	scenario := domain.FakeScenario{
		Schema: "urn:crewfold:schema:fixture:fake-run-scenario:v1", Name: "same-role-no-grant",
		Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "role is metadata"}},
	}
	created, err := storage.CreateRun(context.Background(), CreateRunCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, Runtime: "fake", Provider: "fake", Scenario: scenario,
		ExpectedTaskRevision: assigned.Task.Revision, IdempotencyKey: "same-role-no-grant", CorrelationID: "request-same-role-no-grant",
	})
	if err != nil {
		t.Fatalf("CreateRun(ungranted) = %v", err)
	}
	if _, err := storage.MarkRunStarting(context.Background(), created.Detail.Run.ID, "same-role-no-grant-starting"); err != nil {
		t.Fatalf("MarkRunStarting(ungranted) = %v", err)
	}
	if _, err := storage.AuthorizeRunCheckWatchGrant(context.Background(), created.Detail.Run.ID, domain.CheckWatchOperationRun); ErrorCode(err) != CodeCheckWatchGrantDenied {
		t.Fatalf("same-role ungranted run check-watch authorization error = %v, code = %q", err, ErrorCode(err))
	}
}

func insertContextCheckWatchGrantFixture(t *testing.T, storage *Store, workspaceID, projectID string, agent domain.AgentDefinition, expiresAt time.Time, operations []string) domain.CheckWatchGrant {
	t.Helper()
	definitionID := "checkdef_00000000000000000000000000000001"
	grantID := "checkgrant_00000000000000000000000000000001"
	now := storage.nowText()
	definitionContent, err := json.Marshal(struct {
		WorkspaceID      string   `json:"workspace_id"`
		ProjectID        string   `json:"project_id"`
		Name             string   `json:"name"`
		Executable       string   `json:"executable"`
		Arguments        []string `json:"arguments"`
		WorkingDirectory string   `json:"working_directory"`
		TimeoutMillis    int64    `json:"timeout_millis"`
		OutputByteLimit  int64    `json:"output_byte_limit"`
	}{
		WorkspaceID: workspaceID, ProjectID: projectID, Name: "context watcher fixture", Executable: "/bin/true",
		Arguments: []string{}, WorkingDirectory: ".", TimeoutMillis: 1000, OutputByteLimit: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	definitionDigest := sha256.Sum256(definitionContent)
	definitionSHA := fmt.Sprintf("%x", definitionDigest)
	definitions := []domain.CheckWatchGrantDefinition{{DefinitionID: definitionID, ContentRevision: 1, DefinitionSHA256: definitionSHA}}
	operationsJSON, err := json.Marshal(operations)
	if err != nil {
		t.Fatal(err)
	}
	definitionsJSON, err := json.Marshal(definitions)
	if err != nil {
		t.Fatal(err)
	}
	expiresAtText := expiresAt.UTC().Format(time.RFC3339Nano)
	grantContent, err := json.Marshal(struct {
		WorkspaceID   string                             `json:"workspace_id"`
		ProjectID     string                             `json:"project_id"`
		AgentID       string                             `json:"agent_id"`
		AgentRevision int64                              `json:"agent_revision"`
		Operations    []string                           `json:"operations"`
		Definitions   []domain.CheckWatchGrantDefinition `json:"definitions"`
		MaxPending    int                                `json:"max_pending"`
		MaxInFlight   int                                `json:"max_in_flight"`
		ExpiresAt     string                             `json:"expires_at"`
	}{
		WorkspaceID: workspaceID, ProjectID: projectID, AgentID: agent.ID, AgentRevision: agent.Revision,
		Operations: operations, Definitions: definitions, MaxPending: 8, MaxInFlight: 2, ExpiresAt: expiresAtText,
	})
	if err != nil {
		t.Fatal(err)
	}
	grantDigest := sha256.Sum256(grantContent)
	grantSHA := fmt.Sprintf("%x", grantDigest)
	storage.checkMutationSealActive.Store(true)
	defer storage.checkMutationSealActive.Store(false)
	tx, err := storage.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO check_definitions(
id,workspace_id,project_id,name,executable,working_directory,timeout_millis,output_byte_limit,arguments_json,content_json,
content_revision,content_sha256,status,revision,created_at,updated_at,created_by,updated_by)
VALUES (?,?,?,?,?,?,?,?,?,?,1,?,'active',1,?,?,'local-owner','local-owner')`,
		definitionID, workspaceID, projectID, "context watcher fixture", "/bin/true", ".", 1000, 1024, "[]", string(definitionContent), definitionSHA, now, now); err != nil {
		t.Fatalf("insert check definition fixture: %v", err)
	}
	for ordinal, operation := range operations {
		if _, err := tx.Exec("INSERT INTO check_watch_grant_operations(grant_id,ordinal,operation) VALUES (?,?,?)", grantID, ordinal, operation); err != nil {
			t.Fatalf("insert check-watch operation fixture: %v", err)
		}
	}
	if _, err := tx.Exec(`INSERT INTO check_watch_grant_definitions(
grant_id,ordinal,definition_id,definition_content_revision,definition_sha256) VALUES (?,0,?,1,?)`, grantID, definitionID, definitionSHA); err != nil {
		t.Fatalf("insert check-watch definition fixture: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO check_watch_grants(
id,workspace_id,project_id,agent_id,agent_revision,operations_json,definitions_json,max_pending,max_in_flight,expires_at,
content_json,content_sha256,status,revision,created_at,updated_at,created_by,updated_by)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,'active',1,?,?,'local-owner','local-owner')`,
		grantID, workspaceID, projectID, agent.ID, agent.Revision, string(operationsJSON), string(definitionsJSON), 8, 2,
		expiresAtText, string(grantContent), grantSHA, now, now); err != nil {
		t.Fatalf("insert check-watch grant fixture: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit check-watch grant fixture: %v", err)
	}
	return domain.CheckWatchGrant{
		ID: grantID, WorkspaceID: workspaceID, ProjectID: projectID, AgentID: agent.ID, AgentRevision: agent.Revision,
		Operations: append([]string(nil), operations...), Definitions: definitions, MaxPending: 8, MaxInFlight: 2,
		ExpiresAt: expiresAtText, ContentSHA256: grantSHA, Status: domain.CheckWatchGrantActive, Revision: 1,
		CreatedAt: now, UpdatedAt: now, CreatedBy: localOwnerActorID, UpdatedBy: localOwnerActorID,
	}
}

func TestOrdinaryPacketMarshalDoesNotExposeDelegatedAuthority(t *testing.T) {
	t.Parallel()
	packet := domain.ContextPacket{
		Schema:       domain.ContextPacketSchema,
		Dependencies: []domain.ContextDependency{}, Dependents: []domain.ContextDependency{}, ParticipantThreads: []domain.ParticipantThread{},
		RequestedKnowledgeRevisionIDs: []string{}, AcceptedKnowledge: []domain.KnowledgeRevision{}, Included: []domain.ContextSelection{}, Excluded: []domain.ContextExclusion{},
	}
	encoded, err := json.Marshal(packet)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["management_grant"]; exists {
		t.Fatalf("ordinary packet wire shape gained manager authority: %s", encoded)
	}
	for _, required := range []string{"dependencies", "dependents", "participant_threads", "requested_knowledge_revision_ids", "accepted_knowledge"} {
		if string(fields[required]) != "[]" {
			t.Errorf("packet explicit collection %s = %s", required, fields[required])
		}
	}
}

func TestManagerGrantValidationIsExactAndRoleAgnostic(t *testing.T) {
	t.Parallel()
	packet := domain.ContextPacket{
		Schema: domain.ContextPacketSchema, WorkspaceID: "ws_exact", ProjectID: "prj_exact", TaskID: "task_exact", AgentID: "agent_exact",
		Role:     domain.ContextRole{AgentID: "agent_exact", Revision: 7, Role: "constellation cartographer"},
		Task:     domain.ContextTask{TaskID: "task_exact", ObjectiveID: "obj_exact", Revision: 11},
		Included: []domain.ContextSelection{{Section: "management_grant", EntityType: "manager_grant", EntityID: "mgrgrant_exact", Revision: 3}},
		ManagementGrant: &domain.ContextManagerGrant{
			Schema: domain.ContextManagerGrantSchema, GrantID: "mgrgrant_exact", GrantRevision: 3,
			WorkspaceID: "ws_exact", ProjectID: "prj_exact", ObjectiveID: "obj_exact", ObjectiveRevision: 2,
			ManagerAgentID: "agent_exact", ManagerAgentRevision: 7, ManagerTaskID: "task_exact", ManagerTaskRevision: 11,
			AllowedProposalKinds: []string{domain.ManagerProposalAssignment, domain.ManagerProposalTaskDecomposition},
			LaunchProfiles:       []domain.ContextManagerLaunchProfile{{LaunchProfileID: "lprof_exact", Revision: 2, AgentID: "agent_target", AgentRevision: 4}},
			AllowedClaimKinds:    []string{domain.ClaimKindComponent, domain.ClaimKindOperation, domain.ClaimKindPath},
			MaxOpenProposals:     2, MaxActions: 8, MaxTasks: 4, MaxDependencies: 8, MaxClaimRequirements: 8,
		},
	}
	if err := validateContextManagerGrant(packet); err != nil {
		t.Fatalf("valid manager grant rejected: %v", err)
	}
	packet.Role.Role = "a second arbitrary owner label"
	if err := validateContextManagerGrant(packet); err != nil {
		t.Fatalf("role label affected manager authority: %v", err)
	}
	for name, mutate := range map[string]func(*domain.ContextManagerGrant){
		"wrong task revision": func(grant *domain.ContextManagerGrant) { grant.ManagerTaskRevision++ },
		"unsorted kinds": func(grant *domain.ContextManagerGrant) {
			grant.AllowedProposalKinds = []string{domain.ManagerProposalTaskDecomposition, domain.ManagerProposalAssignment}
		},
		"duplicate profile": func(grant *domain.ContextManagerGrant) {
			grant.LaunchProfiles = append(grant.LaunchProfiles, grant.LaunchProfiles[0])
		},
		"zero limit": func(grant *domain.ContextManagerGrant) { grant.MaxTasks = 0 },
	} {
		candidate := packet
		grant := *packet.ManagementGrant
		grant.AllowedProposalKinds = append([]string(nil), grant.AllowedProposalKinds...)
		grant.LaunchProfiles = append([]domain.ContextManagerLaunchProfile(nil), grant.LaunchProfiles...)
		mutate(&grant)
		candidate.ManagementGrant = &grant
		if err := validateContextManagerGrant(candidate); err == nil {
			t.Errorf("%s manager grant unexpectedly validated", name)
		}
	}
}

func TestManagerInvocationBuildsAuthorityFromExactGrantAndProfile(t *testing.T) {
	t.Parallel()
	storage, fixture := createManagerGrantAdversarialFixture(t)
	ctx := context.Background()

	ordinary, err := storage.BuildContextPacket(ctx, BuildContextCommand{
		WorkspaceIdentifier: fixture.workspace.ID, TaskID: fixture.planning.Task.ID, AgentIdentifier: fixture.manager.ID,
		ExpectedTaskRevision: fixture.planning.Task.Revision, IdempotencyKey: "ordinary-planning-context", CorrelationID: "request-ordinary-planning-context",
	})
	if err != nil {
		t.Fatalf("BuildContextPacket(ordinary) = %v", err)
	}
	if ordinary.Value.Schema != domain.ContextPacketSchema || ordinary.Value.ManagementGrant != nil || !reflect.DeepEqual(ordinary.Value.Policy.AllowedTools, runScopedTools) {
		t.Fatalf("ordinary context unexpectedly gained manager authority: %#v", ordinary.Value)
	}

	planningScenario := domain.FakeScenario{
		Schema: "urn:crewfold:schema:fixture:fake-run-scenario:v1", Name: "exact-manager-planning",
		Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "propose bounded work"}},
	}
	planningProfile, err := storage.CreateLaunchProfile(ctx, CreateLaunchProfileCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID, AgentIdentifier: fixture.manager.ID,
		ExpectedAgentRevision: fixture.manager.Revision, Runtime: "fake", Provider: "fake", Scenario: planningScenario,
		AssignmentLeaseSeconds: 900, CapabilityTTLSeconds: 900, ManagerGrantID: fixture.grant.ID,
		IdempotencyKey: "exact-manager-planning-profile", CorrelationID: "request-exact-manager-planning-profile",
	})
	if err != nil {
		t.Fatalf("CreateLaunchProfile(planning) = %v", err)
	}
	invoked, err := storage.InvokeManager(ctx, InvokeManagerCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ObjectiveID: fixture.objective.ID, TaskID: fixture.planning.Task.ID,
		ManagerGrantID: fixture.grant.ID, LaunchProfileID: planningProfile.Value.ID,
		ExpectedTaskRevision: fixture.planning.Task.Revision, ExpectedGrantRevision: fixture.grant.Revision,
		ExpectedProfileRevision: planningProfile.Value.Revision,
		IdempotencyKey:          "invoke-exact-manager", CorrelationID: "request-invoke-exact-manager",
	})
	if err != nil {
		t.Fatalf("InvokeManager() = %v", err)
	}
	packet, err := storage.ContextPacket(ctx, fixture.workspace.ID, invoked.Detail.Run.ContextPacketID)
	if err != nil {
		t.Fatalf("ContextPacket(manager) = %v", err)
	}
	grant := packet.ManagementGrant
	if packet.Schema != domain.ContextPacketSchema || grant == nil || grant.GrantID != fixture.grant.ID || grant.GrantRevision != fixture.grant.Revision ||
		grant.ManagerTaskID != fixture.planning.Task.ID || grant.ManagerTaskRevision != fixture.planning.Task.Revision ||
		grant.ManagerAgentID != fixture.manager.ID || grant.ManagerAgentRevision != fixture.manager.Revision ||
		len(grant.LaunchProfiles) != 1 || grant.LaunchProfiles[0].LaunchProfileID != fixture.target.ID ||
		!reflect.DeepEqual(packet.Policy.AllowedTools, managerAllowedTools(fixture.grant.ProposalKinds)) {
		t.Fatalf("manager packet did not freeze exact grant/profile authority: %#v", packet)
	}
	if packet.Role.Role != "constellation cartographer" {
		t.Fatalf("arbitrary role label changed in packet: %q", packet.Role.Role)
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
