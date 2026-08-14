package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"crewfold/internal/domain"
)

// These tests intentionally use direct SQL. The process-local owner is trusted,
// but schema-17 authority must still reject partial/substituted rows written by a
// buggy command path before they become durable capabilities.
func TestManagerGrantNormalizedAuthorityRejectsPostSealMutation(t *testing.T) {
	t.Parallel()
	storage, fixture := createManagerGrantAdversarialFixture(t)

	tests := []struct {
		name      string
		statement string
		arguments []any
	}{
		{
			name:      "proposal kind append",
			statement: "INSERT INTO manager_grant_proposal_kinds(grant_id,ordinal,kind) VALUES(?,1,'review')",
			arguments: []any{fixture.grant.ID},
		},
		{
			name:      "claim kind append",
			statement: "INSERT INTO manager_grant_claim_kinds(grant_id,ordinal,kind) VALUES(?,1,'path')",
			arguments: []any{fixture.grant.ID},
		},
		{
			name:      "target profile append",
			statement: "INSERT INTO manager_grant_launch_profiles(grant_id,ordinal,launch_profile_id,launch_profile_revision,agent_id,agent_revision) VALUES(?,1,?,?,?,?)",
			arguments: []any{fixture.grant.ID, fixture.target.ID, fixture.target.Revision, fixture.target.AgentID, fixture.target.AgentRevision},
		},
		{
			name:      "proposal kind update",
			statement: "UPDATE manager_grant_proposal_kinds SET kind='review' WHERE grant_id=? AND ordinal=0",
			arguments: []any{fixture.grant.ID},
		},
		{
			name:      "claim kind delete",
			statement: "DELETE FROM manager_grant_claim_kinds WHERE grant_id=? AND ordinal=0",
			arguments: []any{fixture.grant.ID},
		},
		{
			name:      "target profile delete",
			statement: "DELETE FROM manager_grant_launch_profiles WHERE grant_id=? AND ordinal=0",
			arguments: []any{fixture.grant.ID},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := storage.db.Exec(test.statement, test.arguments...); err == nil {
				t.Fatalf("direct SQL %s unexpectedly changed sealed manager authority", test.name)
			}
		})
	}

	stored, err := storage.ManagerGrant(context.Background(), fixture.workspace.ID, fixture.grant.ID)
	if err != nil {
		t.Fatalf("ManagerGrant() after rejected SQL = %v", err)
	}
	if len(stored.ProposalKinds) != 1 || stored.ProposalKinds[0] != domain.ManagerProposalTaskDecomposition ||
		len(stored.AllowedClaimKinds) != 1 || stored.AllowedClaimKinds[0] != domain.ClaimKindComponent ||
		len(stored.LaunchProfiles) != 1 || stored.LaunchProfiles[0].LaunchProfileID != fixture.target.ID {
		t.Fatalf("sealed manager authority changed after rejected SQL: %#v", stored)
	}
}

func TestManagerGrantNormalizedAuthorityRequiresCanonicalChildrenBeforeParent(t *testing.T) {
	t.Parallel()
	storage, fixture := createManagerGrantAdversarialFixture(t)

	grantID := "mgrgrant_ffffffffffffffffffffffffffffffff"
	contentJSON := fmt.Sprintf(`{"workspace_id":%q,"project_id":%q,"objective_id":%q,"task_id":%q,"task_revision":%d,"agent_id":%q,"agent_revision":%d,"proposal_kinds":["task_decomposition"],"launch_profiles":[{"launch_profile_id":%q,"revision":%d,"agent_id":%q,"agent_revision":%d}],"allowed_claim_kinds":["component"],"limits":{"max_open_proposals":2,"max_actions":8,"max_tasks":4,"max_dependencies":4,"max_claim_requirements":2,"budget":{"token_limit":1000,"cost_cents":100,"time_seconds":600}}}`,
		fixture.workspace.ID, fixture.project.ID, fixture.objective.ID, fixture.planning.Task.ID, fixture.planning.Task.Revision,
		fixture.manager.ID, fixture.manager.Revision, fixture.target.ID, fixture.target.Revision, fixture.target.AgentID,
		fixture.target.AgentRevision)
	statement := `INSERT INTO manager_grants(
id,workspace_id,project_id,objective_id,task_id,task_revision,agent_id,agent_revision,
proposal_kinds_json,launch_profiles_json,allowed_claim_kinds_json,max_open_proposals,max_actions,max_tasks,
max_dependencies,max_claim_requirements,budget_tokens,budget_cost_cents,budget_time_seconds,content_json,content_sha256,
expires_at,status,revision,created_at,updated_at,created_by,updated_by)
VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,lower(hex(sha256(CAST(? AS BLOB)))),NULL,'active',1,?,?,?,?)`
	if _, err := storage.db.Exec(statement,
		grantID, fixture.workspace.ID, fixture.project.ID, fixture.objective.ID, fixture.planning.Task.ID, fixture.planning.Task.Revision,
		fixture.manager.ID, fixture.manager.Revision, `["task_decomposition"]`, fmt.Sprintf(`[{"launch_profile_id":%q,"revision":%d,"agent_id":%q,"agent_revision":%d}]`, fixture.target.ID, fixture.target.Revision, fixture.target.AgentID, fixture.target.AgentRevision),
		`["component"]`, 2, 8, 4, 4, 2, 1000, 100, 600, contentJSON, contentJSON,
		fixture.grant.CreatedAt, fixture.grant.CreatedAt, "local-owner", "local-owner"); err == nil {
		t.Fatal("direct SQL parent without normalized authority children unexpectedly succeeded")
	}
	var count int
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM manager_grants WHERE id=?", grantID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial manager grant count = %d, %v; want zero", count, err)
	}
}

func TestSupervisorPolicyNormalizedLimitsRejectPostSealMutation(t *testing.T) {
	t.Parallel()
	storage, fixture := createManagerGrantAdversarialFixture(t)
	configured, err := storage.ConfigureSupervisorPolicy(context.Background(), ConfigureSupervisorPolicyCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Enabled: true, AutoSchedule: true,
		Limits: domain.SupervisorLimits{
			MaxActiveRuns: 4, MaxStartingRuns: 2, DefaultProjectConcurrency: 2, DefaultProviderConcurrency: 2,
			ProjectConcurrency: map[string]int{fixture.project.ID: 1}, ProviderConcurrency: map[string]int{"fake": 1},
		},
		AutoRetryLimit: 1, RetryCooldownSeconds: 30, ExpectedRevision: 1,
		IdempotencyKey: "adversarial-supervisor-policy", CorrelationID: "request-adversarial-supervisor-policy",
	})
	if err != nil {
		t.Fatalf("ConfigureSupervisorPolicy() = %v", err)
	}
	if configured.Value.Revision != 2 {
		t.Fatalf("configured policy revision = %d, want 2", configured.Value.Revision)
	}

	tests := []struct {
		name      string
		statement string
		arguments []any
	}{
		{
			name:      "project limit append",
			statement: "INSERT INTO supervisor_policy_project_limits(workspace_id,policy_revision,project_id,max_concurrency) VALUES(?,?,?,2)",
			arguments: []any{fixture.workspace.ID, configured.Value.Revision, fixture.project.ID},
		},
		{
			name:      "provider limit append",
			statement: "INSERT INTO supervisor_policy_provider_limits(workspace_id,policy_revision,provider,max_concurrency) VALUES(?,?,'other-provider',2)",
			arguments: []any{fixture.workspace.ID, configured.Value.Revision},
		},
		{
			name:      "project limit update",
			statement: "UPDATE supervisor_policy_project_limits SET max_concurrency=2 WHERE workspace_id=? AND policy_revision=? AND project_id=?",
			arguments: []any{fixture.workspace.ID, configured.Value.Revision, fixture.project.ID},
		},
		{
			name:      "provider limit delete",
			statement: "DELETE FROM supervisor_policy_provider_limits WHERE workspace_id=? AND policy_revision=? AND provider='fake'",
			arguments: []any{fixture.workspace.ID, configured.Value.Revision},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := storage.db.Exec(test.statement, test.arguments...); err == nil {
				t.Fatalf("direct SQL %s unexpectedly changed sealed supervisor limits", test.name)
			}
		})
	}

	stored, err := storage.SupervisorPolicy(context.Background(), fixture.workspace.ID)
	if err != nil {
		t.Fatalf("SupervisorPolicy() after rejected SQL = %v", err)
	}
	if stored.Revision != 2 || stored.Limits.ProjectConcurrency[fixture.project.ID] != 1 || stored.Limits.ProviderConcurrency["fake"] != 1 {
		t.Fatalf("sealed supervisor limits changed after rejected SQL: %#v", stored)
	}
}

func TestSupervisorStateRejectsRawCursorJumpAcrossUnclassifiedEvent(t *testing.T) {
	storage, fixture := createManagerGrantAdversarialFixture(t)
	configureCursorTestPolicy(t, storage, fixture.workspace.ID, "raw-cursor-jump")
	runCursorTestSupervisor(t, storage, fixture.workspace.ID, "raw-cursor-baseline", false)
	before := supervisorCursorForTest(t, storage, fixture.workspace.ID)

	tx, err := storage.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin raw cursor unknown-event fixture = %v", err)
	}
	unknownSequence, err := appendEvent(context.Background(), tx, fixture.workspace.ID, "task", fixture.planning.Task.ID,
		fixture.planning.Task.Revision, "task.unclassified_authority_changed", "raw-cursor-unknown", storage.nowText(), map[string]any{"version": 1})
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("append raw cursor unknown event = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit raw cursor unknown event = %v", err)
	}

	var previousUpdatedAt string
	if err := storage.db.QueryRow(`SELECT updated_at FROM supervisor_state WHERE workspace_id=?`, fixture.workspace.ID).Scan(&previousUpdatedAt); err != nil {
		t.Fatalf("read raw cursor timestamp = %v", err)
	}
	previousTime, err := time.Parse(time.RFC3339Nano, previousUpdatedAt)
	if err != nil {
		t.Fatalf("parse raw cursor timestamp = %v", err)
	}
	if _, err := storage.db.Exec(`UPDATE supervisor_state
SET last_event_sequence=?,revision=revision+1,updated_at=? WHERE workspace_id=?`,
		unknownSequence, previousTime.Add(time.Nanosecond).UTC().Format(time.RFC3339Nano), fixture.workspace.ID); err == nil {
		t.Fatal("direct SQL cursor jump across an unclassified event unexpectedly succeeded")
	}
	if after := supervisorCursorForTest(t, storage, fixture.workspace.ID); after != before {
		t.Fatalf("cursor after rejected raw jump = %d, want unchanged %d", after, before)
	}
	_, err = storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "raw-cursor-unknown-scan", CorrelationID: "request-raw-cursor-unknown-scan", PersistNoop: true,
	})
	if ErrorCode(err) != CodeUnsupportedSupervisorEvent {
		t.Fatalf("RunSupervisor(after rejected raw jump) = %v, code %q; want %q", err, ErrorCode(err), CodeUnsupportedSupervisorEvent)
	}
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM supervisor_actions`)
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM idempotency_keys WHERE key='raw-cursor-unknown-scan'`)
}

func TestManagerGrantExpiryIsCanonicalAndRecheckedLive(t *testing.T) {
	t.Parallel()
	storage, fixture := createManagerGrantAdversarialFixture(t)
	ctx := context.Background()
	observed, err := time.Parse(time.RFC3339Nano, fixture.grant.CreatedAt)
	if err != nil {
		t.Fatalf("parse fixture grant creation = %v", err)
	}
	observed = observed.Add(time.Second)
	storage.clock = func() time.Time { return observed }

	base := CreateManagerGrantCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID,
		ObjectiveID: fixture.objective.ID, TaskID: fixture.planning.Task.ID, AgentIdentifier: fixture.manager.ID,
		ExpectedTaskRevision: fixture.planning.Task.Revision, ExpectedAgentRevision: fixture.manager.Revision,
		ProposalKinds: []string{domain.ManagerProposalTaskDecomposition}, LaunchProfileIDs: []string{fixture.target.ID},
		AllowedClaimKinds: []string{domain.ClaimKindComponent},
		Limits: domain.ManagerProposalLimits{MaxOpenProposals: 2, MaxActions: 8, MaxTasks: 4, MaxDependencies: 4,
			MaxClaimRequirements: 2, Budget: domain.Budget{TokenLimit: 1000, CostCents: 100, TimeSeconds: 600}},
	}
	nonCanonical := base
	nonCanonical.ExpiresAt = observed.Add(time.Hour).In(time.FixedZone("offset", 2*60*60)).Format(time.RFC3339Nano)
	nonCanonical.IdempotencyKey, nonCanonical.CorrelationID = "offset-grant", "request-offset-grant"
	if _, err := storage.CreateManagerGrant(ctx, nonCanonical); ErrorCode(err) != CodeInvalidManagerGrant {
		t.Fatalf("CreateManagerGrant(noncanonical offset) = %v, code %q; want %q", err, ErrorCode(err), CodeInvalidManagerGrant)
	}

	past := base
	past.ExpiresAt = observed.Add(-time.Nanosecond).Format(time.RFC3339Nano)
	past.IdempotencyKey, past.CorrelationID = "past-grant", "request-past-grant"
	if _, err := storage.CreateManagerGrant(ctx, past); ErrorCode(err) != CodeInvalidManagerGrant {
		t.Fatalf("CreateManagerGrant(past expiry) = %v, code %q; want %q", err, ErrorCode(err), CodeInvalidManagerGrant)
	}

	// This is the raw-SQL ordering primitive used by schema-17 lifecycle guards.
	// Equivalent RFC3339 offsets must compare as the same instant even though
	// owner commands require the canonical UTC spelling.
	var equivalent int
	if err := storage.db.QueryRow(`SELECT crewfold_timestamp_key('2035-01-01T02:00:00+02:00') = crewfold_timestamp_key('2035-01-01T00:00:00Z')`).Scan(&equivalent); err != nil || equivalent != 1 {
		t.Fatalf("timestamp-key equivalent offsets = %d, %v; want 1", equivalent, err)
	}

	expiresAt := observed.Add(2 * time.Minute)
	current := base
	current.ExpiresAt = expiresAt.Format(time.RFC3339Nano)
	current.IdempotencyKey, current.CorrelationID = "expiring-grant", "request-expiring-grant"
	created, err := storage.CreateManagerGrant(ctx, current)
	if err != nil {
		t.Fatalf("CreateManagerGrant(expiring) = %v", err)
	}

	observed = observed.Add(time.Nanosecond)
	profile, err := storage.CreateLaunchProfile(ctx, CreateLaunchProfileCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID,
		AgentIdentifier: fixture.manager.ID, ExpectedAgentRevision: fixture.manager.Revision,
		Purpose: "expiring exact management authority", Runtime: "fake", Provider: "fake",
		Scenario: domain.FakeScenario{
			Schema: "urn:crewfold:schema:fixture:fake-run-scenario:v1", Name: "expiring-manager",
			Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "expiry probe"}},
		},
		AssignmentLeaseSeconds: 900, CapabilityTTLSeconds: 900, ManagerGrantID: created.Value.ID,
		IdempotencyKey: "expiring-manager-profile", CorrelationID: "request-expiring-manager-profile",
	})
	if err != nil {
		t.Fatalf("CreateLaunchProfile(expiring manager) = %v", err)
	}

	observed = observed.Add(time.Nanosecond)
	invoked, err := storage.InvokeManager(ctx, InvokeManagerCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ObjectiveID: fixture.objective.ID,
		TaskID: fixture.planning.Task.ID, ManagerGrantID: created.Value.ID, LaunchProfileID: profile.Value.ID,
		ExpectedTaskRevision: fixture.planning.Task.Revision, ExpectedGrantRevision: created.Value.Revision,
		ExpectedProfileRevision: profile.Value.Revision,
		IdempotencyKey:          "expiring-manager-invoke", CorrelationID: "request-expiring-manager-invoke",
	})
	if err != nil {
		t.Fatalf("InvokeManager(expiring) = %v", err)
	}
	observed = observed.Add(time.Nanosecond)
	if _, err := storage.MarkRunStarting(ctx, invoked.Detail.Run.ID, "request-expiring-manager-starting"); err != nil {
		t.Fatalf("MarkRunStarting(expiring) = %v", err)
	}
	var packetCursor int64
	if err := storage.db.QueryRow(`SELECT json_extract(packet.packet_json,'$.as_of_event_sequence') FROM run_context_bindings binding JOIN context_packets packet ON packet.id=binding.context_packet_id WHERE binding.run_id=?`, invoked.Detail.Run.ID).Scan(&packetCursor); err != nil {
		t.Fatalf("read expiring manager packet cursor = %v", err)
	}

	observed = expiresAt
	_, err = storage.SubmitManagerProposal(ctx, SubmitManagerProposalCommand{
		RunID: invoked.Detail.Run.ID, ManagerGrantID: created.Value.ID, ExpectedGrantRevision: created.Value.Revision,
		Kind: domain.ManagerProposalTaskDecomposition, Summary: "Expired live authority must not remain usable.",
		AsOfEventSequence: packetCursor,
		Actions: []domain.ManagerProposalAction{{Type: domain.ProposalActionCreateTask, CreateTask: &domain.ProposalCreateTaskAction{
			TaskKey: "expired", LaunchProfileID: fixture.target.ID, Title: "Must not exist",
			Budget: domain.Budget{TokenLimit: 10, CostCents: 1, TimeSeconds: 10},
		}}},
		IdempotencyKey: "expired-live-proposal", CorrelationID: "request-expired-live-proposal",
	})
	if ErrorCode(err) != CodeManagerProposalDenied {
		t.Fatalf("SubmitManagerProposal(exact expiry) = %v, code %q; want %q", err, ErrorCode(err), CodeManagerProposalDenied)
	}
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM manager_proposals WHERE source_run_id=?`, invoked.Detail.Run.ID)

	if _, err := storage.CreateLaunchProfile(ctx, CreateLaunchProfileCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID,
		AgentIdentifier: fixture.manager.ID, ExpectedAgentRevision: fixture.manager.Revision,
		Purpose: "expired grant must not bind", Runtime: "fake", Provider: "fake",
		Scenario: domain.FakeScenario{
			Schema: "urn:crewfold:schema:fixture:fake-run-scenario:v1", Name: "expired-profile",
			Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "must not launch"}},
		},
		AssignmentLeaseSeconds: 900, CapabilityTTLSeconds: 900, ManagerGrantID: created.Value.ID,
		IdempotencyKey: "expired-manager-profile", CorrelationID: "request-expired-manager-profile",
	}); ErrorCode(err) != CodeInvalidLaunchProfile {
		t.Fatalf("CreateLaunchProfile(expired grant) = %v, code %q; want %q", err, ErrorCode(err), CodeInvalidLaunchProfile)
	}
}

func TestManagerGrantMaxOpenIncludesInvalidProposals(t *testing.T) {
	t.Parallel()
	storage, fixture := createManagerGrantAdversarialFixture(t)
	runID, packetCursor := invokeAdversarialManager(t, storage, fixture, "invalid-open-bound")
	ctx := context.Background()
	invalidAction := func(taskKey string) []domain.ManagerProposalAction {
		return []domain.ManagerProposalAction{{Type: domain.ProposalActionCreateTask, CreateTask: &domain.ProposalCreateTaskAction{
			TaskKey: taskKey, LaunchProfileID: "lprof_ffffffffffffffffffffffffffffffff", Title: "Invalid bounded proposal",
			Budget: domain.Budget{TokenLimit: 10, CostCents: 1, TimeSeconds: 10},
		}}}
	}
	invalidIDs := make([]string, 0, fixture.grant.Limits.MaxOpenProposals)
	for index := 0; index < fixture.grant.Limits.MaxOpenProposals; index++ {
		key := fmt.Sprintf("invalid-open-%d", index)
		submitted, err := storage.SubmitManagerProposal(ctx, SubmitManagerProposalCommand{
			RunID: runID, ManagerGrantID: fixture.grant.ID, ExpectedGrantRevision: fixture.grant.Revision,
			Kind: domain.ManagerProposalTaskDecomposition, Summary: "Invalid submissions still consume the owner-authored open bound.",
			AsOfEventSequence: packetCursor, Actions: invalidAction(fmt.Sprintf("invalid-%d", index)),
			IdempotencyKey: key, CorrelationID: "request-" + key,
		})
		if err != nil || submitted.Proposal.Status != domain.ManagerProposalInvalid {
			t.Fatalf("SubmitManagerProposal(invalid %d) = %#v, %v; want durable invalid", index, submitted.Proposal, err)
		}
		invalidIDs = append(invalidIDs, submitted.Proposal.ID)
	}
	_, err := storage.SubmitManagerProposal(ctx, SubmitManagerProposalCommand{
		RunID: runID, ManagerGrantID: fixture.grant.ID, ExpectedGrantRevision: fixture.grant.Revision,
		Kind: domain.ManagerProposalTaskDecomposition, Summary: "This invalid proposal must exceed the same open bound.",
		AsOfEventSequence: packetCursor, Actions: invalidAction("invalid-overflow"),
		IdempotencyKey: "invalid-open-overflow", CorrelationID: "request-invalid-open-overflow",
	})
	if ErrorCode(err) != CodeManagerProposalDenied {
		t.Fatalf("SubmitManagerProposal(invalid overflow) = %v, code %q; want %q", err, ErrorCode(err), CodeManagerProposalDenied)
	}
	assertManagementRowCount(t, storage, fixture.grant.Limits.MaxOpenProposals,
		`SELECT COUNT(*) FROM manager_proposals WHERE grant_id=? AND status='invalid'`, fixture.grant.ID)

	rejected, err := storage.RejectManagerProposal(ctx, RejectManagerProposalCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ManagerProposalID: invalidIDs[0], ExpectedRevision: 1,
		DecisionNote:   "Reject this invalid submission to free one bounded slot.",
		IdempotencyKey: "invalid-open-reject", CorrelationID: "request-invalid-open-reject",
	})
	if err != nil || rejected.Proposal.Status != domain.ManagerProposalRejected || len(rejected.Effects) != 0 {
		t.Fatalf("RejectManagerProposal(invalid) = %#v, %v; want rejected with no effects", rejected, err)
	}
	replacement, err := storage.SubmitManagerProposal(ctx, SubmitManagerProposalCommand{
		RunID: runID, ManagerGrantID: fixture.grant.ID, ExpectedGrantRevision: fixture.grant.Revision,
		Kind: domain.ManagerProposalTaskDecomposition, Summary: "One owner rejection frees exactly one open slot.",
		AsOfEventSequence: packetCursor, Actions: invalidAction("invalid-replacement"),
		IdempotencyKey: "invalid-open-replacement", CorrelationID: "request-invalid-open-replacement",
	})
	if err != nil || replacement.Proposal.Status != domain.ManagerProposalInvalid {
		t.Fatalf("SubmitManagerProposal(after invalid rejection) = %#v, %v; want one replacement", replacement.Proposal, err)
	}
	assertManagementRowCount(t, storage, fixture.grant.Limits.MaxOpenProposals,
		`SELECT COUNT(*) FROM manager_proposals WHERE grant_id=? AND status IN ('pending','invalid')`, fixture.grant.ID)
}

func TestProposalDiagnosticBoundCannotHideLaterValidationError(t *testing.T) {
	storage, fixture := createManagerGrantAdversarialFixture(t)
	ctx := context.Background()

	// Thirty-three existing overlaps make each of the first two proposed claim
	// checks emit warnings. The duplicate error arrives only after more than the
	// 64 retained diagnostic slots have been considered.
	for index := 0; index < 33; index++ {
		key := fmt.Sprintf("diagnostic-overlap-%02d", index)
		task := createAdversarialValidationTask(t, storage, fixture, key, domain.Budget{TokenLimit: 1, CostCents: 1, TimeSeconds: 1})
		if _, err := storage.AddClaim(ctx, AddClaimCommand{
			WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID, TaskID: task.Task.ID,
			Kind: domain.ClaimKindComponent, Target: "diagnostic-shared-component", Mode: domain.ClaimModeExclusive,
			ConflictPolicy: domain.ClaimPolicyNotify, LeaseDuration: 24 * time.Hour,
			IdempotencyKey: key + "-claim", CorrelationID: "request-" + key + "-claim",
		}); err != nil {
			t.Fatalf("AddClaim(%d) = %v", index, err)
		}
	}
	runID, packetCursor := invokeAdversarialManager(t, storage, fixture, "diagnostic-cap")
	actions := []domain.ManagerProposalAction{
		{Type: domain.ProposalActionCreateTask, CreateTask: &domain.ProposalCreateTaskAction{
			TaskKey: "diagnostic-target", LaunchProfileID: fixture.target.ID, Title: "Diagnostic cap target",
			Budget: domain.Budget{TokenLimit: 10, CostCents: 1, TimeSeconds: 10},
		}},
		{Type: domain.ProposalActionDeclareClaimRequirement, DeclareClaimRequirement: &domain.ProposalDeclareClaimRequirementAction{
			Task: domain.ProposalTaskRef{ProposalTaskKey: "diagnostic-target"}, Kind: domain.ClaimKindComponent,
			Target: "diagnostic-shared-component", Mode: domain.ClaimModeExclusive, ConflictPolicy: domain.ClaimPolicyNotify,
		}},
		{Type: domain.ProposalActionDeclareClaimRequirement, DeclareClaimRequirement: &domain.ProposalDeclareClaimRequirementAction{
			Task: domain.ProposalTaskRef{ProposalTaskKey: "diagnostic-target"}, Kind: domain.ClaimKindComponent,
			Target: "diagnostic-shared-component", Mode: domain.ClaimModeExclusive, ConflictPolicy: domain.ClaimPolicyNotify,
		}},
	}
	submitted, err := storage.SubmitManagerProposal(ctx, SubmitManagerProposalCommand{
		RunID: runID, ManagerGrantID: fixture.grant.ID, ExpectedGrantRevision: fixture.grant.Revision,
		Kind: domain.ManagerProposalTaskDecomposition, Summary: "Warnings must never displace a later authority error.",
		AsOfEventSequence: packetCursor, Actions: actions,
		IdempotencyKey: "diagnostic-cap-submit", CorrelationID: "request-diagnostic-cap-submit",
	})
	if err != nil {
		t.Fatalf("SubmitManagerProposal(diagnostic cap) = %v", err)
	}
	if submitted.Proposal.Status != domain.ManagerProposalInvalid {
		t.Fatalf("diagnostic-cap proposal status = %q, issues=%#v; want invalid", submitted.Proposal.Status, submitted.Proposal.ValidationIssues)
	}
	if len(submitted.Proposal.ValidationIssues) > maximumManagerValidationIssues {
		t.Fatalf("diagnostic-cap retained %d issues, want <= %d", len(submitted.Proposal.ValidationIssues), maximumManagerValidationIssues)
	}
	assertProposalIssue(t, submitted.Proposal.ValidationIssues, "duplicate_claim_requirement", domain.ProposalIssueError)
	if _, err := storage.AcceptManagerProposal(ctx, AcceptManagerProposalCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ManagerProposalID: submitted.Proposal.ID, ExpectedRevision: submitted.Proposal.Revision,
		DecisionNote:   "An error hidden by warning volume must not be accepted.",
		IdempotencyKey: "diagnostic-cap-accept", CorrelationID: "request-diagnostic-cap-accept",
	}); ErrorCode(err) != CodeManagerProposalConflict {
		t.Fatalf("AcceptManagerProposal(diagnostic cap) = %v, code %q; want %q", err, ErrorCode(err), CodeManagerProposalConflict)
	}
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM manager_proposal_effects WHERE proposal_id=?`, submitted.Proposal.ID)
}

func TestManagerProposalAcceptanceRejectsTargetAgentRevisionDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		update func(*testing.T, *Store, managerGrantAdversarialFixture)
	}{
		{name: "disabled", update: func(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture) {
			disabled := false
			if _, err := storage.UpdateAgent(context.Background(), UpdateAgentCommand{
				WorkspaceIdentifier: fixture.workspace.ID, AgentIdentifier: fixture.target.AgentID,
				Enabled: &disabled, ExpectedRevision: fixture.target.AgentRevision,
				IdempotencyKey: "disable-target-before-accept", CorrelationID: "request-disable-target-before-accept",
			}); err != nil {
				t.Fatalf("UpdateAgent(disable target) = %v", err)
			}
		}},
		{name: "revised", update: func(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture) {
			role := "revised owner-defined target label"
			if _, err := storage.UpdateAgent(context.Background(), UpdateAgentCommand{
				WorkspaceIdentifier: fixture.workspace.ID, AgentIdentifier: fixture.target.AgentID,
				Role: &role, ExpectedRevision: fixture.target.AgentRevision,
				IdempotencyKey: "revise-target-before-accept", CorrelationID: "request-revise-target-before-accept",
			}); err != nil {
				t.Fatalf("UpdateAgent(revise target) = %v", err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			storage, fixture := createManagerGrantAdversarialFixture(t)
			runID, packetCursor := invokeAdversarialManager(t, storage, fixture, "target-drift-"+test.name)
			submitted, err := storage.SubmitManagerProposal(context.Background(), SubmitManagerProposalCommand{
				RunID: runID, ManagerGrantID: fixture.grant.ID, ExpectedGrantRevision: fixture.grant.Revision,
				Kind: domain.ManagerProposalTaskDecomposition, Summary: "Target authority must still be current at acceptance.",
				AsOfEventSequence: packetCursor,
				Actions: []domain.ManagerProposalAction{{Type: domain.ProposalActionCreateTask, CreateTask: &domain.ProposalCreateTaskAction{
					TaskKey: "target-drift", LaunchProfileID: fixture.target.ID, Title: "Target drift probe",
					Budget: domain.Budget{TokenLimit: 10, CostCents: 1, TimeSeconds: 10},
				}}},
				IdempotencyKey: "target-drift-submit-" + test.name, CorrelationID: "request-target-drift-submit-" + test.name,
			})
			if err != nil || submitted.Proposal.Status != domain.ManagerProposalPending {
				t.Fatalf("SubmitManagerProposal(target drift) = %#v, %v", submitted.Proposal, err)
			}
			test.update(t, storage, fixture)
			accepted, err := storage.AcceptManagerProposal(context.Background(), AcceptManagerProposalCommand{
				WorkspaceIdentifier: fixture.workspace.ID, ManagerProposalID: submitted.Proposal.ID, ExpectedRevision: submitted.Proposal.Revision,
				DecisionNote:   "Reject target authority drift at the decision boundary.",
				IdempotencyKey: "target-drift-accept-" + test.name, CorrelationID: "request-target-drift-accept-" + test.name,
			})
			if err != nil {
				t.Fatalf("AcceptManagerProposal(target drift) = %v", err)
			}
			if accepted.Proposal.Status != domain.ManagerProposalStale || len(accepted.Effects) != 0 {
				t.Fatalf("target-drift decision = %#v; want stale with no effects", accepted)
			}
			assertProposalIssue(t, accepted.Proposal.ValidationIssues, "profile_agent_stale", domain.ProposalIssueError)
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM manager_proposal_effects WHERE proposal_id=?`, submitted.Proposal.ID)
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM scheduling_intents WHERE source_proposal_id=?`, submitted.Proposal.ID)
		})
	}
}

func TestManagerProposalAcceptanceRevalidatesFrozenSourceAndObjective(t *testing.T) {
	for _, test := range []struct {
		name   string
		update func(*testing.T, *Store, managerGrantAdversarialFixture)
	}{
		{name: "source agent disabled", update: func(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture) {
			disabled := false
			if _, err := storage.UpdateAgent(context.Background(), UpdateAgentCommand{
				WorkspaceIdentifier: fixture.workspace.ID, AgentIdentifier: fixture.manager.ID,
				Enabled: &disabled, ExpectedRevision: fixture.manager.Revision,
				IdempotencyKey: "disable-source-before-accept", CorrelationID: "request-disable-source-before-accept",
			}); err != nil {
				t.Fatalf("UpdateAgent(disable proposal source) = %v", err)
			}
		}},
		{name: "source agent revised", update: func(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture) {
			role := "revised source label without new authority"
			if _, err := storage.UpdateAgent(context.Background(), UpdateAgentCommand{
				WorkspaceIdentifier: fixture.workspace.ID, AgentIdentifier: fixture.manager.ID,
				Role: &role, ExpectedRevision: fixture.manager.Revision,
				IdempotencyKey: "revise-source-before-accept", CorrelationID: "request-revise-source-before-accept",
			}); err != nil {
				t.Fatalf("UpdateAgent(revise proposal source) = %v", err)
			}
		}},
		{name: "objective revised", update: func(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture) {
			title := "Revised objective requires a new explicit grant"
			if _, err := storage.UpdateObjective(context.Background(), UpdateObjectiveCommand{
				WorkspaceIdentifier: fixture.workspace.ID, ObjectiveID: fixture.objective.ID, Title: &title,
				ExpectedRevision: fixture.objective.Revision,
				IdempotencyKey:   "revise-objective-before-accept", CorrelationID: "request-revise-objective-before-accept",
			}); err != nil {
				t.Fatalf("UpdateObjective(revise before proposal acceptance) = %v", err)
			}
		}},
		{name: "objective inactive", update: func(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture) {
			status := domain.ObjectiveCancelled
			if _, err := storage.UpdateObjective(context.Background(), UpdateObjectiveCommand{
				WorkspaceIdentifier: fixture.workspace.ID, ObjectiveID: fixture.objective.ID, Status: &status,
				ExpectedRevision: fixture.objective.Revision,
				IdempotencyKey:   "pause-objective-before-accept", CorrelationID: "request-pause-objective-before-accept",
			}); err != nil {
				t.Fatalf("UpdateObjective(pause before proposal acceptance) = %v", err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			storage, fixture := createManagerGrantAdversarialFixture(t)
			key := "frozen-source-objective-" + strings.ReplaceAll(test.name, " ", "-")
			runID, packetCursor := invokeAdversarialManager(t, storage, fixture, key)
			submitted, err := storage.SubmitManagerProposal(context.Background(), SubmitManagerProposalCommand{
				RunID: runID, ManagerGrantID: fixture.grant.ID, ExpectedGrantRevision: fixture.grant.Revision,
				Kind: domain.ManagerProposalTaskDecomposition, Summary: "Acceptance must revalidate frozen source and objective authority.",
				AsOfEventSequence: packetCursor,
				Actions: []domain.ManagerProposalAction{{Type: domain.ProposalActionCreateTask, CreateTask: &domain.ProposalCreateTaskAction{
					TaskKey: "frozen-authority", LaunchProfileID: fixture.target.ID, Title: "Frozen authority probe",
					Budget: domain.Budget{TokenLimit: 10, CostCents: 1, TimeSeconds: 10},
				}}},
				IdempotencyKey: "frozen-source-objective-submit-" + test.name,
				CorrelationID:  "request-frozen-source-objective-submit-" + test.name,
			})
			if err != nil || submitted.Proposal.Status != domain.ManagerProposalPending ||
				submitted.Proposal.ObjectiveRevision != fixture.objective.Revision {
				t.Fatalf("SubmitManagerProposal(frozen source/objective) = %#v, %v", submitted.Proposal, err)
			}
			test.update(t, storage, fixture)
			decided, err := storage.AcceptManagerProposal(context.Background(), AcceptManagerProposalCommand{
				WorkspaceIdentifier: fixture.workspace.ID, ManagerProposalID: submitted.Proposal.ID,
				ExpectedRevision: submitted.Proposal.Revision,
				DecisionNote:     "Do not apply a proposal whose frozen source authority changed.",
				IdempotencyKey:   "frozen-source-objective-accept-" + test.name,
				CorrelationID:    "request-frozen-source-objective-accept-" + test.name,
			})
			if err != nil {
				t.Fatalf("AcceptManagerProposal(frozen source/objective) = %v", err)
			}
			if decided.Proposal.Status != domain.ManagerProposalStale || len(decided.Effects) != 0 {
				t.Fatalf("frozen source/objective decision = %#v; want stale with no effects", decided)
			}
			assertProposalIssue(t, decided.Proposal.ValidationIssues, "grant_stale", domain.ProposalIssueError)
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM manager_proposal_effects WHERE proposal_id=?`, submitted.Proposal.ID)
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM scheduling_intents WHERE source_proposal_id=?`, submitted.Proposal.ID)
		})
	}
}

func TestIdenticalArbitraryRoleWithoutExactGrantCannotPropose(t *testing.T) {
	t.Parallel()
	storage, fixture := createManagerGrantAdversarialFixture(t)
	ctx := context.Background()
	task, err := storage.CreateTask(ctx, CreateTaskCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID, ObjectiveID: fixture.objective.ID,
		Title: "Same-role authority probe", Budget: domain.Budget{TokenLimit: 100, CostCents: 10, TimeSeconds: 60},
		IdempotencyKey: "same-role-probe-task", CorrelationID: "request-same-role-probe-task",
	})
	if err != nil {
		t.Fatalf("CreateTask(same role probe) = %v", err)
	}
	assigned, err := storage.AssignTask(ctx, AssignTaskCommand{
		WorkspaceIdentifier: fixture.workspace.ID, TaskID: task.Detail.Task.ID, AgentIdentifier: fixture.target.AgentID,
		LeaseSeconds: 900, ExpectedRevision: task.Detail.Task.Revision,
		IdempotencyKey: "same-role-probe-assignment", CorrelationID: "request-same-role-probe-assignment",
	})
	if err != nil {
		t.Fatalf("AssignTask(same role probe) = %v", err)
	}
	run, err := storage.CreateRun(ctx, CreateRunCommand{
		WorkspaceIdentifier: fixture.workspace.ID, TaskID: assigned.Detail.Task.ID, Runtime: "fake", Provider: "fake",
		Scenario: domain.FakeScenario{
			Schema: "urn:crewfold:schema:fixture:fake-run-scenario:v1", Name: "same-role-ungranted",
			Acceptance: domain.AcceptanceRule{}, Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "no authority"}},
		},
		ExpectedTaskRevision: assigned.Detail.Task.Revision,
		IdempotencyKey:       "same-role-probe-run", CorrelationID: "request-same-role-probe-run",
	})
	if err != nil {
		t.Fatalf("CreateRun(same role probe) = %v", err)
	}
	if _, err := storage.MarkRunStarting(ctx, run.Detail.Run.ID, "request-same-role-probe-starting"); err != nil {
		t.Fatalf("MarkRunStarting(same role probe) = %v", err)
	}
	_, err = storage.SubmitManagerProposal(ctx, SubmitManagerProposalCommand{
		RunID: run.Detail.Run.ID, ManagerGrantID: fixture.grant.ID, ExpectedGrantRevision: fixture.grant.Revision,
		Kind: domain.ManagerProposalTaskDecomposition, Summary: "A role label must not grant this proposal.",
		Actions: []domain.ManagerProposalAction{{Type: domain.ProposalActionCreateTask, CreateTask: &domain.ProposalCreateTaskAction{
			TaskKey: "unauthorized", LaunchProfileID: fixture.target.ID, Title: "Must not exist",
			Budget: domain.Budget{TokenLimit: 10, CostCents: 1, TimeSeconds: 10},
		}}},
		IdempotencyKey: "same-role-probe-proposal", CorrelationID: "request-same-role-probe-proposal",
	})
	if ErrorCode(err) != CodeManagerProposalDenied {
		t.Fatalf("SubmitManagerProposal(same arbitrary role, ungranted) = %v, code %q; want %q", err, ErrorCode(err), CodeManagerProposalDenied)
	}
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM manager_proposals WHERE source_run_id=?`, run.Detail.Run.ID)
}

func TestManagerProposalAggregateValidationMatrix(t *testing.T) {
	type validationCase struct {
		name          string
		issueCode     string
		issueSeverity string
		status        string
		arrange       func(*testing.T, *Store, managerGrantAdversarialFixture) []domain.ManagerProposalAction
	}
	create := func(key, profileID string, budget domain.Budget) domain.ManagerProposalAction {
		return domain.ManagerProposalAction{Type: domain.ProposalActionCreateTask, CreateTask: &domain.ProposalCreateTaskAction{
			TaskKey: key, LaunchProfileID: profileID, Title: "Aggregate validation " + key, Budget: budget,
		}}
	}
	ref := func(key string) domain.ProposalTaskRef { return domain.ProposalTaskRef{ProposalTaskKey: key} }
	claim := func(key, policy string) domain.ManagerProposalAction {
		return domain.ManagerProposalAction{Type: domain.ProposalActionDeclareClaimRequirement, DeclareClaimRequirement: &domain.ProposalDeclareClaimRequirementAction{
			Task: ref(key), Kind: domain.ClaimKindComponent, Target: "shared-renderer", Mode: domain.ClaimModeExclusive, ConflictPolicy: policy,
		}}
	}
	bounded := domain.Budget{TokenLimit: 10, CostCents: 1, TimeSeconds: 10}
	tests := []validationCase{
		{
			name: "proposed dependency cycle", issueCode: "dependency_cycle", issueSeverity: domain.ProposalIssueError, status: domain.ManagerProposalInvalid,
			arrange: func(_ *testing.T, _ *Store, fixture managerGrantAdversarialFixture) []domain.ManagerProposalAction {
				return []domain.ManagerProposalAction{
					create("a", fixture.target.ID, bounded), create("b", fixture.target.ID, bounded),
					{Type: domain.ProposalActionAddDependency, AddDependency: &domain.ProposalAddDependencyAction{Task: ref("a"), DependsOn: ref("b")}},
					{Type: domain.ProposalActionAddDependency, AddDependency: &domain.ProposalAddDependencyAction{Task: ref("b"), DependsOn: ref("a")}},
				}
			},
		},
		{
			name: "existing plus proposed dependency cycle", issueCode: "dependency_cycle", issueSeverity: domain.ProposalIssueError, status: domain.ManagerProposalInvalid,
			arrange: func(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture) []domain.ManagerProposalAction {
				first := createAdversarialValidationTask(t, storage, fixture, "existing-cycle-first", bounded)
				second := createAdversarialValidationTask(t, storage, fixture, "existing-cycle-second", bounded)
				linked, err := storage.AddTaskDependency(context.Background(), AddTaskDependencyCommand{
					WorkspaceIdentifier: fixture.workspace.ID, TaskID: second.Task.ID, DependsOnTaskID: first.Task.ID,
					ExpectedRevision: second.Task.Revision, IdempotencyKey: "existing-cycle-edge", CorrelationID: "request-existing-cycle-edge",
				})
				if err != nil {
					t.Fatalf("AddTaskDependency(existing cycle edge) = %v", err)
				}
				return []domain.ManagerProposalAction{
					create("cycle-filler", fixture.target.ID, bounded),
					{Type: domain.ProposalActionAddDependency, AddDependency: &domain.ProposalAddDependencyAction{
						Task:      domain.ProposalTaskRef{TaskID: first.Task.ID, ExpectedTaskRevision: first.Task.Revision},
						DependsOn: domain.ProposalTaskRef{TaskID: linked.Detail.Task.ID, ExpectedTaskRevision: linked.Detail.Task.Revision},
					}},
				}
			},
		},
		{
			name: "duplicate dependency", issueCode: "duplicate_dependency", issueSeverity: domain.ProposalIssueError, status: domain.ManagerProposalInvalid,
			arrange: func(_ *testing.T, _ *Store, fixture managerGrantAdversarialFixture) []domain.ManagerProposalAction {
				edge := domain.ManagerProposalAction{Type: domain.ProposalActionAddDependency, AddDependency: &domain.ProposalAddDependencyAction{Task: ref("b"), DependsOn: ref("a")}}
				return []domain.ManagerProposalAction{create("a", fixture.target.ID, bounded), create("b", fixture.target.ID, bounded), edge, edge}
			},
		},
		{
			name: "task outside grant objective", issueCode: "task_scope_or_revision", issueSeverity: domain.ProposalIssueError, status: domain.ManagerProposalInvalid,
			arrange: func(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture) []domain.ManagerProposalAction {
				objective, err := storage.CreateObjective(context.Background(), CreateObjectiveCommand{
					WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID, Title: "Outside exact grant objective",
					Budget:         domain.Budget{TokenLimit: 100, CostCents: 10, TimeSeconds: 60},
					IdempotencyKey: "outside-objective", CorrelationID: "request-outside-objective",
				})
				if err != nil {
					t.Fatalf("CreateObjective(outside grant) = %v", err)
				}
				task, err := storage.CreateTask(context.Background(), CreateTaskCommand{
					WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID, ObjectiveID: objective.Value.ID,
					Title: "Outside exact grant task", Budget: bounded,
					IdempotencyKey: "outside-task", CorrelationID: "request-outside-task",
				})
				if err != nil {
					t.Fatalf("CreateTask(outside grant) = %v", err)
				}
				return []domain.ManagerProposalAction{
					create("scope-filler", fixture.target.ID, bounded),
					{Type: domain.ProposalActionAddDependency, AddDependency: &domain.ProposalAddDependencyAction{
						Task: ref("scope-filler"), DependsOn: domain.ProposalTaskRef{TaskID: task.Detail.Task.ID, ExpectedTaskRevision: task.Detail.Task.Revision},
					}},
				}
			},
		},
		{
			name: "profile outside exact grant", issueCode: "profile_not_allowed", issueSeverity: domain.ProposalIssueError, status: domain.ManagerProposalInvalid,
			arrange: func(_ *testing.T, _ *Store, _ managerGrantAdversarialFixture) []domain.ManagerProposalAction {
				return []domain.ManagerProposalAction{create("wrong-profile", "lprof_ffffffffffffffffffffffffffffffff", bounded)}
			},
		},
		{
			name: "retired frozen profile", issueCode: "profile_stale", issueSeverity: domain.ProposalIssueError, status: domain.ManagerProposalInvalid,
			arrange: func(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture) []domain.ManagerProposalAction {
				if _, err := storage.RetireLaunchProfile(context.Background(), RetireLaunchProfileCommand{
					WorkspaceIdentifier: fixture.workspace.ID, LaunchProfileID: fixture.target.ID, ExpectedRevision: fixture.target.Revision,
					Reason: "exercise frozen target retirement", IdempotencyKey: "retire-validation-target", CorrelationID: "request-retire-validation-target",
				}); err != nil {
					t.Fatalf("RetireLaunchProfile() = %v", err)
				}
				return []domain.ManagerProposalAction{create("retired-profile", fixture.target.ID, bounded)}
			},
		},
		{
			name: "grant task count", issueCode: "proposal_limit_exceeded", issueSeverity: domain.ProposalIssueError, status: domain.ManagerProposalInvalid,
			arrange: func(_ *testing.T, _ *Store, fixture managerGrantAdversarialFixture) []domain.ManagerProposalAction {
				return []domain.ManagerProposalAction{
					create("one", fixture.target.ID, bounded), create("two", fixture.target.ID, bounded), create("three", fixture.target.ID, bounded),
					create("four", fixture.target.ID, bounded), create("five", fixture.target.ID, bounded),
				}
			},
		},
		{
			name: "grant budget", issueCode: "grant_budget_exceeded", issueSeverity: domain.ProposalIssueError, status: domain.ManagerProposalInvalid,
			arrange: func(_ *testing.T, _ *Store, fixture managerGrantAdversarialFixture) []domain.ManagerProposalAction {
				return []domain.ManagerProposalAction{create("grant-budget", fixture.target.ID, domain.Budget{TokenLimit: 1001, CostCents: 1, TimeSeconds: 1})}
			},
		},
		{
			name: "objective finite envelope", issueCode: "objective_budget_exceeded", issueSeverity: domain.ProposalIssueError, status: domain.ManagerProposalInvalid,
			arrange: func(_ *testing.T, _ *Store, fixture managerGrantAdversarialFixture) []domain.ManagerProposalAction {
				return []domain.ManagerProposalAction{create("objective-budget", fixture.target.ID, domain.Budget{TokenLimit: 9600, CostCents: 1, TimeSeconds: 1})}
			},
		},
		{
			name: "unlimited allocation under finite envelope", issueCode: "unlimited_budget_under_finite_envelope", issueSeverity: domain.ProposalIssueError, status: domain.ManagerProposalInvalid,
			arrange: func(_ *testing.T, _ *Store, fixture managerGrantAdversarialFixture) []domain.ManagerProposalAction {
				return []domain.ManagerProposalAction{create("unlimited-budget", fixture.target.ID, domain.Budget{})}
			},
		},
		{
			name: "existing unlimited allocation", issueCode: "objective_budget_overcommitted", issueSeverity: domain.ProposalIssueError, status: domain.ManagerProposalInvalid,
			arrange: func(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture) []domain.ManagerProposalAction {
				createAdversarialValidationTask(t, storage, fixture, "existing-unlimited", domain.Budget{})
				return []domain.ManagerProposalAction{create("bounded-after-unlimited", fixture.target.ID, bounded)}
			},
		},
		{
			name: "duplicate claim", issueCode: "duplicate_claim_requirement", issueSeverity: domain.ProposalIssueError, status: domain.ManagerProposalInvalid,
			arrange: func(_ *testing.T, _ *Store, fixture managerGrantAdversarialFixture) []domain.ManagerProposalAction {
				return []domain.ManagerProposalAction{create("claim-a", fixture.target.ID, bounded), claim("claim-a", domain.ClaimPolicyNotify), claim("claim-a", domain.ClaimPolicyNotify)}
			},
		},
		{
			name: "claim deny conflict", issueCode: "claim_conflict", issueSeverity: domain.ProposalIssueError, status: domain.ManagerProposalInvalid,
			arrange: func(_ *testing.T, _ *Store, fixture managerGrantAdversarialFixture) []domain.ManagerProposalAction {
				return []domain.ManagerProposalAction{create("claim-a", fixture.target.ID, bounded), create("claim-b", fixture.target.ID, bounded), claim("claim-a", domain.ClaimPolicyDenyNew), claim("claim-b", domain.ClaimPolicyNotify)}
			},
		},
		{
			name: "warning only claim overlap", issueCode: "claim_overlap_notify", issueSeverity: domain.ProposalIssueWarning, status: domain.ManagerProposalPending,
			arrange: func(_ *testing.T, _ *Store, fixture managerGrantAdversarialFixture) []domain.ManagerProposalAction {
				return []domain.ManagerProposalAction{create("claim-a", fixture.target.ID, bounded), create("claim-b", fixture.target.ID, bounded), claim("claim-a", domain.ClaimPolicyNotify), claim("claim-b", domain.ClaimPolicyNotify)}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			storage, fixture := createManagerGrantAdversarialFixture(t)
			actions := test.arrange(t, storage, fixture)
			runID, asOfEventSequence := invokeAdversarialManager(t, storage, fixture, "aggregate-"+strings.ReplaceAll(test.name, " ", "-"))
			result, err := storage.SubmitManagerProposal(context.Background(), SubmitManagerProposalCommand{
				RunID: runID, ManagerGrantID: fixture.grant.ID, ExpectedGrantRevision: fixture.grant.Revision,
				Kind: domain.ManagerProposalTaskDecomposition, Summary: "Exercise " + test.name,
				AsOfEventSequence: asOfEventSequence, Actions: actions,
				IdempotencyKey: "aggregate-submit-" + strings.ReplaceAll(test.name, " ", "-"), CorrelationID: "request-aggregate-submit-" + strings.ReplaceAll(test.name, " ", "-"),
			})
			if err != nil {
				t.Fatalf("SubmitManagerProposal(%s) = %v", test.name, err)
			}
			if result.Proposal.Status != test.status || len(result.Effects) != 0 {
				t.Fatalf("proposal %s = status %q effects %#v; want %q and inert", test.name, result.Proposal.Status, result.Effects, test.status)
			}
			assertProposalIssue(t, result.Proposal.ValidationIssues, test.issueCode, test.issueSeverity)
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM manager_proposal_effects WHERE proposal_id=?`, result.Proposal.ID)
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM scheduling_intents WHERE source_proposal_id=?`, result.Proposal.ID)
			if test.status == domain.ManagerProposalInvalid {
				_, err := storage.AcceptManagerProposal(context.Background(), AcceptManagerProposalCommand{
					WorkspaceIdentifier: fixture.workspace.ID, ManagerProposalID: result.Proposal.ID, ExpectedRevision: result.Proposal.Revision,
					DecisionNote: "Invalid proposal must remain inert.", IdempotencyKey: "aggregate-invalid-accept-" + strings.ReplaceAll(test.name, " ", "-"),
					CorrelationID: "request-aggregate-invalid-accept-" + strings.ReplaceAll(test.name, " ", "-"),
				})
				if ErrorCode(err) != CodeManagerProposalConflict {
					t.Fatalf("AcceptManagerProposal(invalid %s) = %v, code %q; want %q", test.name, err, ErrorCode(err), CodeManagerProposalConflict)
				}
			}
		})
	}
}

func TestManagerProposalAcceptanceIsInertAtomicAndIdempotent(t *testing.T) {
	t.Parallel()
	storage, fixture := createManagerGrantAdversarialFixture(t)
	ctx := context.Background()

	planningProfile, err := storage.CreateLaunchProfile(ctx, CreateLaunchProfileCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID,
		AgentIdentifier: fixture.manager.ID, ExpectedAgentRevision: fixture.manager.Revision,
		Purpose: "planning metadata without role authority", Runtime: "fake", Provider: "fake",
		Scenario: domain.FakeScenario{
			Schema: "urn:crewfold:schema:fixture:fake-run-scenario:v1", Name: "adversarial-manager-proposal",
			Acceptance: domain.AcceptanceRule{},
			Steps:      []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "bounded planning run"}},
		},
		AssignmentLeaseSeconds: 900, CapabilityTTLSeconds: 900, ManagerGrantID: fixture.grant.ID,
		IdempotencyKey: "adversarial-planning-profile", CorrelationID: "request-adversarial-planning-profile",
	})
	if err != nil {
		t.Fatalf("CreateLaunchProfile(planning) = %v", err)
	}
	invoked, err := storage.InvokeManager(ctx, InvokeManagerCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ObjectiveID: fixture.objective.ID,
		TaskID: fixture.planning.Task.ID, ManagerGrantID: fixture.grant.ID, LaunchProfileID: planningProfile.Value.ID,
		ExpectedTaskRevision: fixture.planning.Task.Revision, ExpectedGrantRevision: fixture.grant.Revision,
		ExpectedProfileRevision: planningProfile.Value.Revision,
		IdempotencyKey:          "adversarial-manager-invoke", CorrelationID: "request-adversarial-manager-invoke",
	})
	if err != nil {
		t.Fatalf("InvokeManager() = %v", err)
	}
	if _, err := storage.MarkRunStarting(ctx, invoked.Detail.Run.ID, "request-adversarial-manager-starting"); err != nil {
		t.Fatalf("MarkRunStarting() = %v", err)
	}
	var asOfEventSequence int64
	if err := storage.db.QueryRowContext(ctx, `
SELECT json_extract(packet.packet_json,'$.as_of_event_sequence')
FROM run_context_bindings binding
JOIN context_packets packet ON packet.id=binding.context_packet_id
WHERE binding.run_id=?`, invoked.Detail.Run.ID).Scan(&asOfEventSequence); err != nil {
		t.Fatalf("read manager packet cursor = %v", err)
	}
	actions := []domain.ManagerProposalAction{
		{Type: domain.ProposalActionCreateTask, CreateTask: &domain.ProposalCreateTaskAction{
			TaskKey: "a", LaunchProfileID: fixture.target.ID, Title: "Adversarial A", Priority: 30,
			Budget: domain.Budget{TokenLimit: 100, CostCents: 10, TimeSeconds: 60},
		}},
		{Type: domain.ProposalActionCreateTask, CreateTask: &domain.ProposalCreateTaskAction{
			TaskKey: "b", LaunchProfileID: fixture.target.ID, Title: "Adversarial B", Priority: 20,
			Budget: domain.Budget{TokenLimit: 100, CostCents: 10, TimeSeconds: 60},
		}},
		{Type: domain.ProposalActionCreateTask, CreateTask: &domain.ProposalCreateTaskAction{
			TaskKey: "review", LaunchProfileID: fixture.target.ID, Title: "Adversarial independent review", Priority: 10,
			Budget: domain.Budget{TokenLimit: 100, CostCents: 10, TimeSeconds: 60},
		}},
		{Type: domain.ProposalActionAddDependency, AddDependency: &domain.ProposalAddDependencyAction{
			Task: domain.ProposalTaskRef{ProposalTaskKey: "b"}, DependsOn: domain.ProposalTaskRef{ProposalTaskKey: "a"},
		}},
		{Type: domain.ProposalActionAddDependency, AddDependency: &domain.ProposalAddDependencyAction{
			Task: domain.ProposalTaskRef{ProposalTaskKey: "review"}, DependsOn: domain.ProposalTaskRef{ProposalTaskKey: "a"},
		}},
		{Type: domain.ProposalActionAddDependency, AddDependency: &domain.ProposalAddDependencyAction{
			Task: domain.ProposalTaskRef{ProposalTaskKey: "review"}, DependsOn: domain.ProposalTaskRef{ProposalTaskKey: "b"},
		}},
	}
	submitted, err := storage.SubmitManagerProposal(ctx, SubmitManagerProposalCommand{
		RunID: invoked.Detail.Run.ID, ManagerGrantID: fixture.grant.ID, ExpectedGrantRevision: fixture.grant.Revision,
		Kind: domain.ManagerProposalTaskDecomposition, Summary: "Apply A then B and independently review both",
		AsOfEventSequence: asOfEventSequence, Actions: actions,
		IdempotencyKey: "adversarial-proposal-submit", CorrelationID: "request-adversarial-proposal-submit",
	})
	if err != nil {
		t.Fatalf("SubmitManagerProposal() = %v", err)
	}
	if submitted.Proposal.Status != domain.ManagerProposalPending || len(submitted.Effects) != 0 {
		t.Fatalf("submitted proposal = %#v; want pending and inert", submitted)
	}
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM tasks WHERE objective_id=? AND title LIKE 'Adversarial %'`, fixture.objective.ID)
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM manager_proposal_effects WHERE proposal_id=?`, submitted.Proposal.ID)
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM scheduling_intents WHERE source_proposal_id=?`, submitted.Proposal.ID)

	accepted, err := storage.AcceptManagerProposal(ctx, AcceptManagerProposalCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ManagerProposalID: submitted.Proposal.ID, ExpectedRevision: 1,
		DecisionNote: "Accept this exact bounded graph.", IdempotencyKey: "adversarial-proposal-accept",
		CorrelationID: "request-adversarial-proposal-accept",
	})
	if err != nil {
		t.Fatalf("AcceptManagerProposal() = %v", err)
	}
	replayed, err := storage.AcceptManagerProposal(ctx, AcceptManagerProposalCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ManagerProposalID: submitted.Proposal.ID, ExpectedRevision: 1,
		DecisionNote: "Accept this exact bounded graph.", IdempotencyKey: "adversarial-proposal-accept",
		CorrelationID: "request-adversarial-proposal-accept",
	})
	if err != nil {
		t.Fatalf("AcceptManagerProposal(replay) = %v", err)
	}
	if accepted.Proposal.Status != domain.ManagerProposalAccepted || replayed.EventSequence != accepted.EventSequence ||
		replayed.Proposal.ID != accepted.Proposal.ID || len(replayed.Effects) != len(accepted.Effects) {
		t.Fatalf("accept/replay differ: accepted=%#v replayed=%#v", accepted, replayed)
	}
	if len(accepted.Effects) != 9 {
		t.Fatalf("accepted effects = %d, want 9 exact task/intent/dependency effects", len(accepted.Effects))
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM manager_proposal_decisions WHERE proposal_id=?`, submitted.Proposal.ID)
	assertManagementRowCount(t, storage, 9, `SELECT COUNT(*) FROM manager_proposal_effects WHERE proposal_id=?`, submitted.Proposal.ID)
	assertManagementRowCount(t, storage, 3, `SELECT COUNT(*) FROM scheduling_intents WHERE source_proposal_id=?`, submitted.Proposal.ID)
	assertManagementRowCount(t, storage, 3, `SELECT COUNT(*) FROM task_dependencies WHERE task_id IN (SELECT entity_id FROM manager_proposal_effects WHERE proposal_id=? AND effect_type='created' AND entity_type='task')`, submitted.Proposal.ID)

	for title, revision := range map[string]int64{"Adversarial A": 1, "Adversarial B": 2, "Adversarial independent review": 3} {
		var got int64
		if err := storage.db.QueryRowContext(ctx, `SELECT revision FROM tasks WHERE objective_id=? AND title=?`, fixture.objective.ID, title).Scan(&got); err != nil {
			t.Fatalf("read accepted task %q = %v", title, err)
		}
		if got != revision {
			t.Fatalf("accepted task %q revision = %d, want %d", title, got, revision)
		}
	}

	for _, mutation := range []struct {
		name      string
		statement string
		arguments []any
	}{
		{name: "action update", statement: `UPDATE manager_proposal_actions SET type=type WHERE proposal_id=?`, arguments: []any{submitted.Proposal.ID}},
		{name: "effect delete", statement: `DELETE FROM manager_proposal_effects WHERE proposal_id=?`, arguments: []any{submitted.Proposal.ID}},
		{name: "decision delete", statement: `DELETE FROM manager_proposal_decisions WHERE proposal_id=?`, arguments: []any{submitted.Proposal.ID}},
		{name: "intent target substitution", statement: `UPDATE scheduling_intents SET agent_id=? WHERE source_proposal_id=?`, arguments: []any{fixture.manager.ID, submitted.Proposal.ID}},
		{name: "proposal delete", statement: `DELETE FROM manager_proposals WHERE id=?`, arguments: []any{submitted.Proposal.ID}},
	} {
		t.Run("sealed "+mutation.name, func(t *testing.T) {
			if _, err := storage.db.Exec(mutation.statement, mutation.arguments...); err == nil {
				t.Fatalf("direct SQL %s unexpectedly changed the accepted graph", mutation.name)
			}
		})
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM manager_proposal_decisions WHERE proposal_id=?`, submitted.Proposal.ID)
	assertManagementRowCount(t, storage, 9, `SELECT COUNT(*) FROM manager_proposal_effects WHERE proposal_id=?`, submitted.Proposal.ID)
	assertManagementRowCount(t, storage, 3, `SELECT COUNT(*) FROM scheduling_intents WHERE source_proposal_id=?`, submitted.Proposal.ID)
	if _, err := storage.MarkRunStarted(ctx, invoked.Detail.Run.ID, "fake-manager-runtime", "fake-manager-provider", "request-adversarial-manager-started"); err != nil {
		t.Fatalf("MarkRunStarted(manager) = %v", err)
	}
	if _, err := storage.ApplyRunObservation(ctx, invoked.Detail.Run.ID, domain.RunObservation{
		Kind: domain.ObservationCompletion, Message: "Submitted exact bounded proposal.", Handoff: "Owner accepted the proposal.", LogArchive: prepareTestRunLogArchive(t, storage, invoked.Detail.Run.ID),
	}, true, nil, "request-adversarial-manager-completed"); err != nil {
		t.Fatalf("ApplyRunObservation(manager completion) = %v", err)
	}

	configureResult, err := storage.ConfigureSupervisorPolicy(ctx, ConfigureSupervisorPolicyCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Enabled: true, AutoSchedule: true,
		Limits:         domain.SupervisorLimits{MaxActiveRuns: 4, MaxStartingRuns: 2, DefaultProjectConcurrency: 4, DefaultProviderConcurrency: 4},
		AutoRetryLimit: 0, RetryCooldownSeconds: 0, ExpectedRevision: 1,
		IdempotencyKey: "adversarial-scheduling-policy", CorrelationID: "request-adversarial-scheduling-policy",
	})
	if err != nil || !configureResult.Value.AutoSchedule {
		t.Fatalf("ConfigureSupervisorPolicy(scheduling) = %#v, %v", configureResult, err)
	}
	firstScan, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "adversarial-scheduling-scan", CorrelationID: "request-adversarial-scheduling-scan-one",
	})
	if err != nil {
		t.Fatalf("RunSupervisor(first dependency-ready scan) = %v", err)
	}
	if len(firstScan.ScheduledRunIDs) != 1 || len(firstScan.Actions) != 1 ||
		firstScan.Actions[0].Condition != domain.SupervisorConditionDependencyReady || firstScan.Actions[0].Status != domain.SupervisorActionApplied {
		t.Fatalf("first dependency-ready scan = %#v; want exact one applied schedule", firstScan)
	}
	// Idempotency is semantic across fresh transport request IDs.
	replayedScan, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "adversarial-scheduling-scan", CorrelationID: "request-adversarial-scheduling-scan-two",
	})
	if err != nil || replayedScan.EventSequence != firstScan.EventSequence ||
		len(replayedScan.ScheduledRunIDs) != 1 || replayedScan.ScheduledRunIDs[0] != firstScan.ScheduledRunIDs[0] {
		t.Fatalf("RunSupervisor(replay) = %#v, %v; want exact receipt %#v", replayedScan, err, firstScan)
	}

	var taskAID string
	if err := storage.db.QueryRowContext(ctx, `SELECT id FROM tasks WHERE objective_id=? AND title='Adversarial A'`, fixture.objective.ID).Scan(&taskAID); err != nil {
		t.Fatalf("read scheduled task A = %v", err)
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM runs WHERE task_id=?`, taskAID)
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM run_jobs job JOIN runs run ON run.id=job.run_id WHERE run.task_id=? AND job.status='pending' AND job.origin='supervisor'`, taskAID)
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM run_scheduling_receipts receipt JOIN runs run ON run.id=receipt.run_id WHERE run.task_id=?`, taskAID)

	// Simulate a daemon crash after the durable intent/action/run/job receipt but
	// before any worker claims the job. Reopening must not schedule a second run.
	dataDir := filepath.Dir(storage.Path())
	if err := storage.Close(); err != nil {
		t.Fatalf("Close(before supervisor restart) = %v", err)
	}
	reopened, err := Open(ctx, dataDir, Options{})
	if err != nil {
		t.Fatalf("Open(after supervisor restart) = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	afterRestart, err := reopened.RunSupervisor(ctx, RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "adversarial-after-restart-scan", CorrelationID: "request-adversarial-after-restart-scan",
	})
	if err != nil {
		t.Fatalf("RunSupervisor(after restart) = %v", err)
	}
	if len(afterRestart.ScheduledRunIDs) != 0 {
		t.Fatalf("restart scheduled duplicate runs: %#v", afterRestart.ScheduledRunIDs)
	}
	assertManagementRowCount(t, reopened, 1, `SELECT COUNT(*) FROM runs WHERE task_id=?`, taskAID)
	assertManagementRowCount(t, reopened, 1, `SELECT COUNT(*) FROM run_scheduling_receipts receipt JOIN runs run ON run.id=receipt.run_id WHERE run.task_id=?`, taskAID)

	for _, mutation := range []struct {
		name      string
		statement string
		arguments []any
	}{
		{name: "scheduling receipt update", statement: `UPDATE run_scheduling_receipts SET task_revision=task_revision WHERE run_id=?`, arguments: []any{firstScan.ScheduledRunIDs[0]}},
		{name: "scheduling receipt delete", statement: `DELETE FROM run_scheduling_receipts WHERE run_id=?`, arguments: []any{firstScan.ScheduledRunIDs[0]}},
		{name: "applied action rewrite", statement: `UPDATE supervisor_actions SET constraint_snapshot_json=constraint_snapshot_json WHERE id=?`, arguments: []any{firstScan.Actions[0].ID}},
	} {
		if _, err := reopened.db.Exec(mutation.statement, mutation.arguments...); err == nil {
			t.Fatalf("direct SQL %s unexpectedly changed sealed scheduling authority", mutation.name)
		}
	}
}

func TestSupervisorConcurrentScansRespectEveryCapacityDimension(t *testing.T) {
	tests := []struct {
		name              string
		targetConcurrency int
		limits            domain.SupervisorLimits
	}{
		{name: "global", targetConcurrency: 8, limits: domain.SupervisorLimits{MaxActiveRuns: 1, MaxStartingRuns: 1, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8}},
		{name: "project", targetConcurrency: 8, limits: domain.SupervisorLimits{MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 1, DefaultProviderConcurrency: 8}},
		{name: "provider", targetConcurrency: 8, limits: domain.SupervisorLimits{MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 1}},
		{name: "agent", targetConcurrency: 1, limits: domain.SupervisorLimits{MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8}},
		{name: "checkout", targetConcurrency: 8, limits: domain.SupervisorLimits{MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			storage, fixture := createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{
				TargetMaxConcurrency: test.targetConcurrency,
				SharedTargetCheckout: test.name != "checkout",
			})
			acceptAdversarialSchedulingPair(t, storage, fixture, "capacity-"+test.name, "")
			configureSupervisorForContention(t, storage, fixture.workspace.ID, test.limits, "capacity-"+test.name)

			const scanners = 8
			start := make(chan struct{})
			type scanOutcome struct {
				result SupervisorRunResult
				err    error
			}
			outcomes := make(chan scanOutcome, scanners)
			var workers sync.WaitGroup
			for index := 0; index < scanners; index++ {
				workers.Add(1)
				go func(index int) {
					defer workers.Done()
					<-start
					result, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
						WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
						IdempotencyKey: fmt.Sprintf("capacity-%s-scan-%d", test.name, index),
						CorrelationID:  fmt.Sprintf("request-capacity-%s-scan-%d", test.name, index),
					})
					outcomes <- scanOutcome{result: result, err: err}
				}(index)
			}
			close(start)
			workers.Wait()
			close(outcomes)
			scheduled := 0
			for outcome := range outcomes {
				if outcome.err != nil {
					t.Fatalf("RunSupervisor(concurrent %s) = %v", test.name, outcome.err)
				}
				scheduled += len(outcome.result.ScheduledRunIDs)
			}
			if scheduled != 1 {
				t.Fatalf("concurrent %s scans scheduled %d runs, want exactly one", test.name, scheduled)
			}
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM runs WHERE created_by='subsystem:supervisor'`)
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE status='run_requested'`)
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE status='deferred' AND reason IS NOT NULL`)
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM run_scheduling_receipts`)
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM supervisor_actions WHERE condition='dependency_ready' AND response='schedule' AND status='deferred'`)
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM events WHERE type='supervisor.action_recorded' AND entity_id IN (SELECT id FROM supervisor_actions WHERE status='deferred')`)

			repeated, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
				WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
				IdempotencyKey: "capacity-" + test.name + "-repeat", CorrelationID: "request-capacity-" + test.name + "-repeat",
			})
			if err != nil || len(repeated.ScheduledRunIDs) != 0 {
				t.Fatalf("RunSupervisor(%s repeat) = %#v, %v; want no duplicate", test.name, repeated, err)
			}
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM runs WHERE created_by='subsystem:supervisor'`)
		})
	}
}

func TestSupervisorClaimConflictDefersWithoutPartialLease(t *testing.T) {
	storage, fixture := createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{TargetMaxConcurrency: 8, SharedTargetCheckout: true})
	acceptAdversarialSchedulingPair(t, storage, fixture, "claim-contention", domain.ClaimPolicyPauseScheduling)
	configureSupervisorForContention(t, storage, fixture.workspace.ID, domain.SupervisorLimits{
		MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8,
	}, "claim-contention")
	result, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "claim-contention-scan", CorrelationID: "request-claim-contention-scan",
	})
	if err != nil || len(result.ScheduledRunIDs) != 1 {
		t.Fatalf("RunSupervisor(claim contention) = %#v, %v; want one scheduled", result, err)
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM work_claims WHERE status='active' AND target='shared-renderer'`)
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE status='deferred' AND reason LIKE '%claim%'`)
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM task_assignments WHERE created_by='subsystem:supervisor'`)
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM run_scheduling_receipts`)
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM supervisor_actions WHERE condition='dependency_ready' AND response='schedule' AND status='deferred'`)
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM events WHERE type='supervisor.action_recorded' AND entity_id IN (SELECT id FROM supervisor_actions WHERE status='deferred')`)
}

func TestSupervisorStartFailureRetryCreatesFreshRunCooledDownAndBounded(t *testing.T) {
	observed := time.Date(2032, 3, 4, 5, 6, 7, 0, time.UTC)
	storage, fixture := createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{
		TargetMaxConcurrency: 8, SharedTargetCheckout: true, Clock: func() time.Time {
			observed = observed.Add(time.Microsecond)
			return observed
		},
	})
	acceptAdversarialSchedulingPair(t, storage, fixture, "bounded-start-retry", "")
	initialLimits := domain.SupervisorLimits{MaxActiveRuns: 1, MaxStartingRuns: 1, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8}
	configureSupervisorForContention(t, storage, fixture.workspace.ID, initialLimits, "bounded-start-retry-initial")
	initial, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "bounded-start-retry-schedule", CorrelationID: "request-bounded-start-retry-schedule",
	})
	if err != nil || len(initial.ScheduledRunIDs) != 1 {
		t.Fatalf("RunSupervisor(initial retry fixture) = %#v, %v", initial, err)
	}
	priorRunID := initial.ScheduledRunIDs[0]
	var assignmentID, profileID string
	var profileRevision int64
	if err := storage.db.QueryRow(`SELECT assignment_id,launch_profile_id,launch_profile_revision FROM run_scheduling_receipts WHERE run_id=?`, priorRunID).Scan(&assignmentID, &profileID, &profileRevision); err != nil {
		t.Fatalf("read retry receipt = %v", err)
	}
	if _, err := storage.ConfigureSupervisorPolicy(context.Background(), ConfigureSupervisorPolicyCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Enabled: true, AutoSchedule: false, Limits: initialLimits,
		AutoRetryLimit: 1, RetryCooldownSeconds: 60, ExpectedRevision: 2,
		IdempotencyKey: "bounded-start-retry-policy", CorrelationID: "request-bounded-start-retry-policy",
	}); err != nil {
		t.Fatalf("ConfigureSupervisorPolicy(retry) = %v", err)
	}
	if _, err := storage.FailRunStart(context.Background(), priorRunID, "definite fixture start failure", "request-bounded-start-retry-first-failure"); err != nil {
		t.Fatalf("FailRunStart(first) = %v", err)
	}

	beforeCooldown, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "bounded-start-retry-before-cooldown", CorrelationID: "request-bounded-start-retry-before-cooldown",
	})
	if err != nil || len(beforeCooldown.Actions) != 0 || len(beforeCooldown.ScheduledRunIDs) != 0 {
		t.Fatalf("RunSupervisor(before retry cooldown) = %#v, %v; want inert", beforeCooldown, err)
	}
	observed = observed.Add(59 * time.Second)
	stillCooling, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "bounded-start-retry-still-cooling", CorrelationID: "request-bounded-start-retry-still-cooling",
	})
	if err != nil || len(stillCooling.Actions) != 0 {
		t.Fatalf("RunSupervisor(still cooling) = %#v, %v; want inert", stillCooling, err)
	}
	observed = observed.Add(time.Second)
	retried, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "bounded-start-retry-eligible", CorrelationID: "request-bounded-start-retry-eligible",
	})
	if err != nil || len(retried.Actions) != 1 || retried.Actions[0].Response != domain.SupervisorResponseRetryTask ||
		retried.Actions[0].Status != domain.SupervisorActionApplied || retried.Actions[0].PriorRunID != priorRunID ||
		len(retried.ScheduledRunIDs) != 1 || retried.Actions[0].RunID != retried.ScheduledRunIDs[0] || retried.Actions[0].RunID == priorRunID {
		t.Fatalf("RunSupervisor(eligible retry) = %#v, %v; want one fresh-run retry action", retried, err)
	}
	retryRunID := retried.ScheduledRunIDs[0]
	var status, currentAssignment string
	if err := storage.db.QueryRow(`SELECT status,assignment_id FROM runs WHERE id=?`, retryRunID).Scan(&status, &currentAssignment); err != nil {
		t.Fatalf("read retried run = %v", err)
	}
	if status != domain.RunRequested || currentAssignment != assignmentID {
		t.Fatalf("retried run status/assignment = %q/%q; want requested/%q", status, currentAssignment, assignmentID)
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM runs WHERE id=? AND status='start_failed'`, priorRunID)
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM runs WHERE id=?`, retryRunID)
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM run_scheduling_receipts WHERE run_id=? AND launch_profile_id=? AND launch_profile_revision=?`, priorRunID, profileID, profileRevision)
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM run_retry_receipts WHERE run_id=? AND prior_run_id=? AND assignment_id=? AND launch_profile_id=? AND launch_profile_revision=? AND attempt=1`, retryRunID, priorRunID, assignmentID, profileID, profileRevision)
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM supervisor_actions WHERE run_id=? AND prior_run_id=? AND response='retry_task' AND status='applied'`, retryRunID, priorRunID)

	if _, err := storage.FailRunStart(context.Background(), retryRunID, "second definite fixture start failure", "request-bounded-start-retry-second-failure"); err != nil {
		t.Fatalf("FailRunStart(second) = %v", err)
	}
	observed = observed.Add(60 * time.Second)
	exhausted, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
		IdempotencyKey: "bounded-start-retry-exhausted", CorrelationID: "request-bounded-start-retry-exhausted",
	})
	if err != nil {
		t.Fatalf("RunSupervisor(exhausted retry) = %v", err)
	}
	retryActions, ownerActions := 0, 0
	for _, action := range exhausted.Actions {
		if action.Response == domain.SupervisorResponseRetryTask {
			retryActions++
		}
		if action.Condition == domain.SupervisorConditionRepeatedFailure && action.Response == domain.SupervisorResponseRequestOwner && action.Status == domain.SupervisorActionAwaitingApproval {
			ownerActions++
		}
	}
	if retryActions != 0 || ownerActions != 1 {
		t.Fatalf("exhausted retry actions = %#v; want no retry and one owner approval", exhausted.Actions)
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM supervisor_actions WHERE prior_run_id=? AND run_id=? AND response='retry_task'`, priorRunID, retryRunID)
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM approval_requests WHERE action_id IN (SELECT id FROM supervisor_actions WHERE run_id=? AND condition='repeated_failure' AND response='request_owner')`, retryRunID)
}

func TestSupervisorRetryRequiresUnexpiredAuthorityAndFreeExclusiveCheckout(t *testing.T) {
	t.Run("expired assignment is not revived", func(t *testing.T) {
		observed := time.Date(2033, 4, 5, 6, 7, 8, 0, time.UTC)
		storage, fixture := createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{
			TargetMaxConcurrency: 8, SharedTargetCheckout: true, Clock: func() time.Time {
				observed = observed.Add(time.Microsecond)
				return observed
			},
		})
		acceptAdversarialSchedulingPair(t, storage, fixture, "expired-retry-authority", "")
		limits := domain.SupervisorLimits{MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8}
		configureSupervisorForContention(t, storage, fixture.workspace.ID, limits, "expired-retry-authority-initial")
		initial, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
			WorkspaceIdentifier: fixture.workspace.ID, Limit: 1,
			IdempotencyKey: "expired-retry-authority-schedule", CorrelationID: "request-expired-retry-authority-schedule",
		})
		if err != nil || len(initial.ScheduledRunIDs) != 1 {
			t.Fatalf("RunSupervisor(expired retry fixture) = %#v, %v", initial, err)
		}
		priorRunID := initial.ScheduledRunIDs[0]
		var taskID, assignmentID, leaseExpiry string
		if err := storage.db.QueryRow(`SELECT run.task_id,run.assignment_id,assignment.lease_expires_at FROM runs run JOIN task_assignments assignment ON assignment.id=run.assignment_id WHERE run.id=?`, priorRunID).Scan(&taskID, &assignmentID, &leaseExpiry); err != nil {
			t.Fatalf("read expiring retry authority = %v", err)
		}
		if _, err := storage.FailRunStart(context.Background(), priorRunID, "definite failure before authority expiry", "request-expired-retry-authority-fail"); err != nil {
			t.Fatalf("FailRunStart(expired authority) = %v", err)
		}
		if _, err := storage.ConfigureSupervisorPolicy(context.Background(), ConfigureSupervisorPolicyCommand{
			WorkspaceIdentifier: fixture.workspace.ID, Enabled: true, AutoSchedule: false, Limits: limits,
			AutoRetryLimit: 1, RetryCooldownSeconds: 60, ExpectedRevision: 2,
			IdempotencyKey: "expired-retry-authority-policy", CorrelationID: "request-expired-retry-authority-policy",
		}); err != nil {
			t.Fatalf("ConfigureSupervisorPolicy(expired retry) = %v", err)
		}
		expires, err := time.Parse(time.RFC3339Nano, leaseExpiry)
		if err != nil {
			t.Fatalf("parse retry assignment expiry = %v", err)
		}
		observed = expires
		result, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
			WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
			IdempotencyKey: "expired-retry-authority-scan", CorrelationID: "request-expired-retry-authority-scan",
		})
		if err != nil {
			t.Fatalf("RunSupervisor(expired retry authority) = %v", err)
		}
		for _, action := range result.Actions {
			if action.Response == domain.SupervisorResponseRetryTask {
				t.Fatalf("expired assignment authorized retry action %#v", action)
			}
		}
		assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM runs WHERE task_id=?`, taskID)
		assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM runs WHERE id=? AND status='start_failed' AND assignment_id=?`, priorRunID, assignmentID)
		assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM supervisor_actions WHERE run_id=? AND response='retry_task'`, priorRunID)
	})

	t.Run("exclusive checkout occupant blocks retry", func(t *testing.T) {
		observed := time.Date(2034, 5, 6, 7, 8, 9, 0, time.UTC)
		storage, fixture := createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{
			TargetMaxConcurrency: 8, SharedTargetCheckout: false, Clock: func() time.Time {
				observed = observed.Add(time.Microsecond)
				return observed
			},
		})
		acceptAdversarialSchedulingPair(t, storage, fixture, "exclusive-retry-capacity", "")
		limits := domain.SupervisorLimits{MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8}
		configureSupervisorForContention(t, storage, fixture.workspace.ID, limits, "exclusive-retry-capacity-initial")
		initial, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
			WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
			IdempotencyKey: "exclusive-retry-capacity-schedule", CorrelationID: "request-exclusive-retry-capacity-schedule",
		})
		if err != nil || len(initial.ScheduledRunIDs) != 1 {
			t.Fatalf("RunSupervisor(exclusive retry fixture) = %#v, %v", initial, err)
		}
		priorRunID := initial.ScheduledRunIDs[0]
		var taskID, checkoutID, writeMode string
		if err := storage.db.QueryRow(`SELECT run.task_id,run.checkout_id,checkout.write_mode FROM runs run JOIN checkouts checkout ON checkout.id=run.checkout_id WHERE run.id=?`, priorRunID).Scan(&taskID, &checkoutID, &writeMode); err != nil {
			t.Fatalf("read exclusive retry placement = %v", err)
		}
		if writeMode == domain.WriteModeShared || writeMode == domain.WriteModeReadOnly {
			t.Fatalf("retry checkout mode = %q; want exclusive/claimed", writeMode)
		}
		if _, err := storage.FailRunStart(context.Background(), priorRunID, "definite failure before exclusive contention", "request-exclusive-retry-capacity-fail"); err != nil {
			t.Fatalf("FailRunStart(exclusive retry) = %v", err)
		}
		if _, err := storage.ConfigureSupervisorPolicy(context.Background(), ConfigureSupervisorPolicyCommand{
			WorkspaceIdentifier: fixture.workspace.ID, Enabled: true, AutoSchedule: true, Limits: limits,
			AutoRetryLimit: 1, RetryCooldownSeconds: 60, ExpectedRevision: 2,
			IdempotencyKey: "exclusive-retry-capacity-policy", CorrelationID: "request-exclusive-retry-capacity-policy",
		}); err != nil {
			t.Fatalf("ConfigureSupervisorPolicy(exclusive retry) = %v", err)
		}
		occupant, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
			WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
			IdempotencyKey: "exclusive-retry-capacity-occupant", CorrelationID: "request-exclusive-retry-capacity-occupant",
		})
		if err != nil || len(occupant.ScheduledRunIDs) != 1 {
			t.Fatalf("RunSupervisor(exclusive occupant) = %#v, %v", occupant, err)
		}
		occupantRunID := occupant.ScheduledRunIDs[0]
		assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM runs WHERE id=? AND checkout_id=? AND status='requested'`, occupantRunID, checkoutID)

		observed = observed.Add(60 * time.Second)
		blocked, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
			WorkspaceIdentifier: fixture.workspace.ID, Limit: 100,
			IdempotencyKey: "exclusive-retry-capacity-blocked", CorrelationID: "request-exclusive-retry-capacity-blocked",
		})
		if err != nil {
			t.Fatalf("RunSupervisor(exclusive retry blocked) = %v", err)
		}
		for _, action := range blocked.Actions {
			if action.Response == domain.SupervisorResponseRetryTask {
				t.Fatalf("exclusive checkout occupant authorized retry action %#v", action)
			}
		}
		assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM runs WHERE task_id=?`, taskID)
		assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM runs WHERE id=? AND status='start_failed'`, priorRunID)
		assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM supervisor_actions WHERE run_id=? AND response='retry_task'`, priorRunID)
	})
}

func TestSupervisorBlockedApprovalIsSingularAndOwnerDecided(t *testing.T) {
	for _, test := range []struct {
		name  string
		allow bool
	}{
		{name: "allow resumes and consumes", allow: true},
		{name: "deny dismisses without resuming", allow: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			storage, workspace, blocked, action, approval := createBlockedSupervisorApprovalFixture(t)
			ctx := context.Background()

			// A later scan observes the same exact run revision but cannot create a
			// second decision path for the already materialized condition key.
			repeated, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
				WorkspaceIdentifier: workspace.ID, Limit: 100,
				IdempotencyKey: "blocked-supervisor-repeat", CorrelationID: "request-blocked-supervisor-repeat",
			})
			if err != nil {
				t.Fatalf("RunSupervisor(repeated blocked scan) = %v", err)
			}
			if len(repeated.Actions) != 0 {
				t.Fatalf("repeated blocked scan actions = %#v, want none", repeated.Actions)
			}
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM supervisor_actions WHERE condition='blocked' AND run_id=?`, blocked.Run.ID)
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM approval_requests WHERE action_id=?`, action.ID)

			for _, mutation := range []struct {
				name      string
				statement string
				arguments []any
			}{
				{name: "approval link substitution", statement: `UPDATE approval_requests SET action_id=action_id WHERE id=?`, arguments: []any{approval.ID}},
				{name: "approval delete", statement: `DELETE FROM approval_requests WHERE id=?`, arguments: []any{approval.ID}},
				{name: "action response substitution", statement: `UPDATE supervisor_actions SET response='stop_run' WHERE id=?`, arguments: []any{action.ID}},
				{name: "action delete", statement: `DELETE FROM supervisor_actions WHERE id=?`, arguments: []any{action.ID}},
			} {
				if _, err := storage.db.Exec(mutation.statement, mutation.arguments...); err == nil {
					t.Fatalf("direct SQL %s unexpectedly changed sealed approval authority", mutation.name)
				}
			}

			command := DecideApprovalCommand{
				WorkspaceIdentifier: workspace.ID, ApprovalRequestID: approval.ID, ExpectedRevision: approval.Revision,
				DecisionNote:   "Owner resolves this exact blocked revision.",
				IdempotencyKey: "blocked-approval-decision-" + test.name, CorrelationID: "request-blocked-approval-decision-" + test.name,
			}
			var decided ApprovalMutationResult
			if test.allow {
				decided, err = storage.AllowApproval(ctx, command)
			} else {
				decided, err = storage.DenyApproval(ctx, command)
			}
			if err != nil {
				t.Fatalf("decide blocked approval = %v", err)
			}
			var replayed ApprovalMutationResult
			if test.allow {
				replayed, err = storage.AllowApproval(ctx, command)
			} else {
				replayed, err = storage.DenyApproval(ctx, command)
			}
			if err != nil || replayed.EventSequence != decided.EventSequence || replayed.Approval.ID != approval.ID || replayed.Action.ID != action.ID {
				t.Fatalf("approval replay = %#v, %v; want exact event %d", replayed, err, decided.EventSequence)
			}
			storedRun, err := storage.RunDetail(ctx, workspace.ID, blocked.Run.ID)
			if err != nil {
				t.Fatalf("Run(after approval) = %v", err)
			}
			if test.allow {
				if decided.Approval.Status != domain.ApprovalConsumed || decided.Action.Status != domain.SupervisorActionApplied || storedRun.Run.Status != domain.RunActive {
					t.Fatalf("allowed approval = %#v, run=%#v; want consumed/applied/active", decided, storedRun.Run)
				}
				assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM events WHERE entity_id=? AND type='approval.granted'`, approval.ID)
				assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM events WHERE entity_id=? AND type='approval.consumed'`, approval.ID)
			} else {
				if decided.Approval.Status != domain.ApprovalDenied || decided.Action.Status != domain.SupervisorActionDismissed || storedRun.Run.Status != domain.RunBlocked {
					t.Fatalf("denied approval = %#v, run=%#v; want denied/dismissed/blocked", decided, storedRun.Run)
				}
				assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM events WHERE entity_id=? AND type='approval.denied'`, approval.ID)
			}
		})
	}
}

func TestSupervisorAndApprovalFaultBarriersRollbackWholeDecision(t *testing.T) {
	t.Run("supervisor scan", func(t *testing.T) {
		storage, workspace, blocked := createBlockedRunWithSupervisorPolicy(t)
		injected := errors.New("injected supervisor scan interruption")
		storage.mutationHook = func(stage string) error {
			if stage == MutationAfterProjection {
				return injected
			}
			return nil
		}
		_, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
			WorkspaceIdentifier: workspace.ID, Limit: 100,
			IdempotencyKey: "faulted-blocked-supervisor", CorrelationID: "request-faulted-blocked-supervisor",
		})
		if !errors.Is(err, injected) {
			t.Fatalf("RunSupervisor(faulted) = %v, want injected failure", err)
		}
		assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM supervisor_actions WHERE run_id=?`, blocked.Run.ID)
		assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM approval_requests`)
		assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM events WHERE type IN ('supervisor.action_recorded','approval.requested','supervisor.scan_completed')`)
		storage.mutationHook = nil
		result, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
			WorkspaceIdentifier: workspace.ID, Limit: 100,
			IdempotencyKey: "faulted-blocked-supervisor", CorrelationID: "request-faulted-blocked-supervisor",
		})
		if err != nil || len(result.Actions) != 1 || result.Actions[0].Status != domain.SupervisorActionAwaitingApproval {
			t.Fatalf("RunSupervisor(after rollback) = %#v, %v", result, err)
		}
	})

	t.Run("approval decision", func(t *testing.T) {
		storage, workspace, blocked, action, approval := createBlockedSupervisorApprovalFixture(t)
		injected := errors.New("injected approval interruption")
		storage.mutationHook = func(stage string) error {
			if stage == MutationAfterEvent {
				return injected
			}
			return nil
		}
		command := DecideApprovalCommand{
			WorkspaceIdentifier: workspace.ID, ApprovalRequestID: approval.ID, ExpectedRevision: 1,
			DecisionNote:   "Resume only this exact blocked revision.",
			IdempotencyKey: "faulted-blocked-approval", CorrelationID: "request-faulted-blocked-approval",
		}
		if _, err := storage.AllowApproval(context.Background(), command); !errors.Is(err, injected) {
			t.Fatalf("AllowApproval(faulted) = %v, want injected failure", err)
		}
		storedApproval, err := storage.ApprovalRequest(context.Background(), workspace.ID, approval.ID)
		if err != nil || storedApproval.Status != domain.ApprovalPending || storedApproval.Revision != 1 {
			t.Fatalf("approval after rollback = %#v, %v", storedApproval, err)
		}
		storedAction, err := storage.SupervisorAction(context.Background(), workspace.ID, action.ID)
		if err != nil || storedAction.Status != domain.SupervisorActionAwaitingApproval || storedAction.Revision != 1 {
			t.Fatalf("action after rollback = %#v, %v", storedAction, err)
		}
		storedRun, err := storage.RunDetail(context.Background(), workspace.ID, blocked.Run.ID)
		if err != nil || storedRun.Run.Status != domain.RunBlocked {
			t.Fatalf("run after rollback = %#v, %v", storedRun.Run, err)
		}
		assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM events WHERE entity_id=? AND type IN ('approval.granted','approval.consumed')`, approval.ID)
		storage.mutationHook = nil
		result, err := storage.AllowApproval(context.Background(), command)
		if err != nil || result.Approval.Status != domain.ApprovalConsumed || result.Action.Status != domain.SupervisorActionApplied {
			t.Fatalf("AllowApproval(after rollback) = %#v, %v", result, err)
		}
	})
}

func TestSupervisorMaterializesEveryNamedNonAutomaticCondition(t *testing.T) {
	t.Run("stale", func(t *testing.T) {
		observed := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
		storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return observed }})
		workspace, _, _, _, assigned := initializeRunTest(t, storage, "M16 stale condition")
		created := createRunTest(t, storage, workspace.ID, assigned, managementProgressScenario("M16-stale"), "M16-stale-run")
		if _, err := storage.MarkRunStarting(context.Background(), created.Run.ID, "request-M16-stale-starting"); err != nil {
			t.Fatalf("MarkRunStarting(stale) = %v", err)
		}
		lost, err := storage.LoseRun(context.Background(), created.Run.ID, "runtime outcome is unknown", "request-M16-stale-lost")
		if err != nil {
			t.Fatalf("LoseRun(stale) = %v", err)
		}
		observed = observed.Add(24 * time.Hour)
		expired, err := storage.ReconcileExpiredAssignments(context.Background(), workspace.ID, "request-M16-stale-expiry")
		if err != nil || expired != 0 {
			t.Fatalf("ReconcileExpiredAssignments(lost) = %d, %v; want no release", expired, err)
		}
		if _, err := storage.AssignTask(context.Background(), AssignTaskCommand{
			WorkspaceIdentifier: workspace.ID, TaskID: lost.Task.ID, AgentIdentifier: lost.Agent.ID,
			LeaseSeconds: 60, ExpectedRevision: lost.Task.Revision,
			IdempotencyKey: "M16-stale-reassign", CorrelationID: "request-M16-stale-reassign",
		}); ErrorCode(err) != CodeAssignmentConflict {
			t.Fatalf("AssignTask(lost retained assignment) = %v, code %q; want %q", err, ErrorCode(err), CodeAssignmentConflict)
		}
		assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM task_assignments WHERE id=? AND status='active'`, lost.Task.AssignmentID)
		configureAdversarialSupervisor(t, storage, workspace.ID, "M16-stale-policy")
		assertOnePendingSupervisorCondition(t, storage, workspace.ID, domain.SupervisorConditionStale, "M16-stale-scan")
	})

	t.Run("failed", func(t *testing.T) {
		storage := openTestStore(t, t.TempDir(), Options{})
		workspace, _, _, _, assigned := initializeRunTest(t, storage, "M16 failed condition")
		active := startAdversarialRun(t, storage, workspace.ID, assigned, "M16-failed")
		if _, err := storage.FailRun(context.Background(), active.Run.ID, "provider_failed", "definite provider failure", prepareTestRunLogArchive(t, storage, active.Run.ID), "", "request-M16-failed"); err != nil {
			t.Fatalf("FailRun() = %v", err)
		}
		configureAdversarialSupervisor(t, storage, workspace.ID, "M16-failed-policy")
		assertOnePendingSupervisorCondition(t, storage, workspace.ID, domain.SupervisorConditionFailed, "M16-failed-scan")
	})

	t.Run("repeated failure", func(t *testing.T) {
		storage := openTestStore(t, t.TempDir(), Options{})
		workspace, project, agent, checkout, assigned := initializeRunTest(t, storage, "M16 repeated failure")
		// Seed one historical terminal run through the narrow projection seam;
		// the second failure uses the public worker state machine below.
		const historicalRun = "run_ffffffffffffffffffffffffffffffff"
		if _, err := storage.db.Exec(`INSERT INTO runs(
id,workspace_id,project_id,task_id,agent_id,checkout_id,runtime,provider,scenario_name,scenario_json,
placement_reasons_json,status,step_cursor,failure_code,failure_message,revision,created_at,updated_at,started_at,finished_at,
created_by,updated_by,assignment_id)
VALUES(?,?,?,?,?,?,'fake','fake','historical-failure','{}','[]','failed',0,'provider_failed','first definite failure',3,
'2000-01-01T00:00:00Z','2000-01-01T00:00:02Z','2000-01-01T00:00:01Z','2000-01-01T00:00:02Z','local-owner','local-owner',?)`,
			historicalRun, workspace.ID, project.ID, assigned.Task.ID, agent.ID, checkout.ID, assigned.Assignment.ID); err != nil {
			t.Fatalf("seed historical failed run = %v", err)
		}
		active := startAdversarialRun(t, storage, workspace.ID, assigned, "M16-repeated-current")
		if _, err := storage.FailRun(context.Background(), active.Run.ID, "provider_failed", "second definite provider failure", prepareTestRunLogArchive(t, storage, active.Run.ID), "", "request-M16-repeated-failed"); err != nil {
			t.Fatalf("FailRun(second) = %v", err)
		}
		configureAdversarialSupervisor(t, storage, workspace.ID, "M16-repeated-policy")
		assertOnePendingSupervisorCondition(t, storage, workspace.ID, domain.SupervisorConditionRepeatedFailure, "M16-repeated-scan")
	})

	t.Run("wall time over budget", func(t *testing.T) {
		observed := time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC)
		storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time {
			observed = observed.Add(time.Second)
			return observed
		}})
		workspace, _, _, _, assigned := initializeRunTest(t, storage, "M16 over budget")
		budget := domain.Budget{TokenLimit: 100, CostCents: 10, TimeSeconds: 1}
		updated, err := storage.UpdateTask(context.Background(), UpdateTaskCommand{
			WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, Budget: &budget,
			ExpectedRevision: assigned.Task.Revision, IdempotencyKey: "M16-over-budget-task", CorrelationID: "request-M16-over-budget-task",
		})
		if err != nil {
			t.Fatalf("UpdateTask(over budget fixture) = %v", err)
		}
		startAdversarialRun(t, storage, workspace.ID, updated.Detail, "M16-over-budget")
		observed = observed.Add(time.Minute)
		configureAdversarialSupervisor(t, storage, workspace.ID, "M16-over-budget-policy")
		assertOnePendingSupervisorCondition(t, storage, workspace.ID, domain.SupervisorConditionOverBudget, "M16-over-budget-scan")
	})
}

func TestSupervisorWallTimeBudgetUsesExactSecondBoundary(t *testing.T) {
	observed := time.Date(2035, 6, 7, 8, 9, 10, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time {
		observed = observed.Add(time.Microsecond)
		return observed
	}})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "M16 exact wall-time boundary")
	budget := domain.Budget{TokenLimit: 100, CostCents: 10, TimeSeconds: 60}
	updated, err := storage.UpdateTask(context.Background(), UpdateTaskCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, Budget: &budget,
		ExpectedRevision: assigned.Task.Revision, IdempotencyKey: "exact-wall-time-task", CorrelationID: "request-exact-wall-time-task",
	})
	if err != nil {
		t.Fatalf("UpdateTask(exact wall-time) = %v", err)
	}
	active := startAdversarialRun(t, storage, workspace.ID, updated.Detail, "exact-wall-time")
	configureAdversarialSupervisor(t, storage, workspace.ID, "exact-wall-time-policy")
	startedAt, err := time.Parse(time.RFC3339Nano, active.Run.StartedAt)
	if err != nil {
		t.Fatalf("parse exact wall-time start = %v", err)
	}
	storage.clock = func() time.Time { return observed }
	observed = startedAt.Add(60 * time.Second)
	atBoundary, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: workspace.ID, Limit: 100,
		IdempotencyKey: "exact-wall-time-boundary", CorrelationID: "request-exact-wall-time-boundary",
	})
	if err != nil {
		t.Fatalf("RunSupervisor(exact wall-time boundary) = %v", err)
	}
	for _, action := range atBoundary.Actions {
		if action.Condition == domain.SupervisorConditionOverBudget {
			t.Fatalf("exact wall-time boundary emitted over-budget action %#v", action)
		}
	}
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM supervisor_actions WHERE condition='over_budget' AND run_id=?`, active.Run.ID)

	observed = observed.Add(time.Second)
	assertOnePendingSupervisorCondition(t, storage, workspace.ID, domain.SupervisorConditionOverBudget, "exact-wall-time-exceeded")
}

func managementProgressScenario(name string) domain.FakeScenario {
	return domain.FakeScenario{
		Schema: "urn:crewfold:schema:fixture:fake-run-scenario:v1", Name: name,
		Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "bounded work"}},
	}
}

func startAdversarialRun(t *testing.T, storage *Store, workspaceID string, assigned domain.TaskDetail, key string) domain.RunDetail {
	t.Helper()
	created := createRunTest(t, storage, workspaceID, assigned, managementProgressScenario(key), key+"-run")
	if _, err := storage.MarkRunStarting(context.Background(), created.Run.ID, "request-"+key+"-starting"); err != nil {
		t.Fatalf("MarkRunStarting(%s) = %v", key, err)
	}
	active, err := storage.MarkRunStarted(context.Background(), created.Run.ID, "fake-runtime", "fake-provider", "request-"+key+"-started")
	if err != nil {
		t.Fatalf("MarkRunStarted(%s) = %v", key, err)
	}
	return active
}

func configureAdversarialSupervisor(t *testing.T, storage *Store, workspaceID, key string) {
	t.Helper()
	if _, err := storage.ConfigureSupervisorPolicy(context.Background(), ConfigureSupervisorPolicyCommand{
		WorkspaceIdentifier: workspaceID, Enabled: true, AutoSchedule: false,
		Limits:         domain.SupervisorLimits{MaxActiveRuns: 8, MaxStartingRuns: 2, DefaultProjectConcurrency: 4, DefaultProviderConcurrency: 4},
		AutoRetryLimit: 0, RetryCooldownSeconds: 0, ExpectedRevision: 1,
		IdempotencyKey: key, CorrelationID: "request-" + key,
	}); err != nil {
		t.Fatalf("ConfigureSupervisorPolicy(%s) = %v", key, err)
	}
}

func assertOnePendingSupervisorCondition(t *testing.T, storage *Store, workspaceID, condition, key string) {
	t.Helper()
	result, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: workspaceID, Limit: 100, IdempotencyKey: key, CorrelationID: "request-" + key,
	})
	if err != nil {
		t.Fatalf("RunSupervisor(%s) = %v", condition, err)
	}
	found := 0
	for _, action := range result.Actions {
		if action.Condition != condition {
			continue
		}
		found++
		if action.Status != domain.SupervisorActionAwaitingApproval || action.ApprovalID == "" || action.Response != domain.SupervisorResponseRequestOwner {
			t.Fatalf("supervisor %s action = %#v; want inert approval-backed owner request", condition, action)
		}
		approval, err := storage.ApprovalRequest(context.Background(), workspaceID, action.ApprovalID)
		if err != nil || approval.Status != domain.ApprovalPending || approval.ActionID != action.ID {
			t.Fatalf("supervisor %s approval = %#v, %v", condition, approval, err)
		}
	}
	if found != 1 {
		t.Fatalf("supervisor %s actions = %d in %#v; want exactly one", condition, found, result.Actions)
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM supervisor_actions WHERE condition=?`, condition)
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM approval_requests WHERE action_id IN (SELECT id FROM supervisor_actions WHERE condition=?)`, condition)
}

func createBlockedSupervisorApprovalFixture(t *testing.T) (*Store, domain.Workspace, domain.RunDetail, domain.SupervisorAction, domain.ApprovalRequest) {
	t.Helper()
	storage, workspace, blocked := createBlockedRunWithSupervisorPolicy(t)
	result, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: workspace.ID, Limit: 100,
		IdempotencyKey: "blocked-supervisor-scan", CorrelationID: "request-blocked-supervisor-scan",
	})
	if err != nil {
		t.Fatalf("RunSupervisor(blocked) = %v", err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Condition != domain.SupervisorConditionBlocked ||
		result.Actions[0].Status != domain.SupervisorActionAwaitingApproval || result.Actions[0].ApprovalID == "" {
		t.Fatalf("blocked supervisor result = %#v", result)
	}
	approval, err := storage.ApprovalRequest(context.Background(), workspace.ID, result.Actions[0].ApprovalID)
	if err != nil || approval.Status != domain.ApprovalPending || approval.ActionID != result.Actions[0].ID {
		t.Fatalf("ApprovalRequest(blocked) = %#v, %v", approval, err)
	}
	return storage, workspace, blocked, result.Actions[0], approval
}

func createBlockedRunWithSupervisorPolicy(t *testing.T) (*Store, domain.Workspace, domain.RunDetail) {
	t.Helper()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "M16 blocked approval")
	scenario := domain.FakeScenario{
		Schema: "urn:crewfold:schema:fixture:fake-run-scenario:v1", Name: "M16-blocked-approval",
		Steps: []domain.FakeStep{{Kind: domain.ObservationBlocked, Message: "Which exact owner decision?"}},
	}
	created := createRunTest(t, storage, workspace.ID, assigned, scenario, "M16-blocked-approval-run")
	if _, err := storage.MarkRunStarting(context.Background(), created.Run.ID, "request-M16-blocked-starting"); err != nil {
		t.Fatalf("MarkRunStarting(blocked) = %v", err)
	}
	if _, err := storage.MarkRunStarted(context.Background(), created.Run.ID, "fake-runtime", "fake-provider", "request-M16-blocked-started"); err != nil {
		t.Fatalf("MarkRunStarted(blocked) = %v", err)
	}
	blocked, err := storage.ApplyRunObservation(context.Background(), created.Run.ID, domain.RunObservation{
		Kind: domain.ObservationBlocked, Message: "Which exact owner decision?",
	}, true, nil, "request-M16-blocked-observation")
	if err != nil {
		t.Fatalf("ApplyRunObservation(blocked) = %v", err)
	}
	configureAdversarialSupervisor(t, storage, workspace.ID, "blocked-supervisor-policy")
	return storage, workspace, blocked
}

func createAdversarialValidationTask(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture, key string, budget domain.Budget) domain.TaskDetail {
	t.Helper()
	result, err := storage.CreateTask(context.Background(), CreateTaskCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID, ObjectiveID: fixture.objective.ID,
		Title: "Aggregate fixture " + key, Budget: budget,
		IdempotencyKey: key, CorrelationID: "request-" + key,
	})
	if err != nil {
		t.Fatalf("CreateTask(%s) = %v", key, err)
	}
	return result.Detail
}

func invokeAdversarialManager(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture, key string) (string, int64) {
	t.Helper()
	profile, err := storage.CreateLaunchProfile(context.Background(), CreateLaunchProfileCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID,
		AgentIdentifier: fixture.manager.ID, ExpectedAgentRevision: fixture.manager.Revision,
		Purpose: "exact aggregate validation run", Runtime: "fake", Provider: "fake",
		Scenario: domain.FakeScenario{
			Schema: "urn:crewfold:schema:fixture:fake-run-scenario:v1", Name: key,
			Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "validate a bounded proposal"}},
		},
		AssignmentLeaseSeconds: 900, CapabilityTTLSeconds: 900, ManagerGrantID: fixture.grant.ID,
		IdempotencyKey: key + "-profile", CorrelationID: "request-" + key + "-profile",
	})
	if err != nil {
		t.Fatalf("CreateLaunchProfile(%s) = %v", key, err)
	}
	invoked, err := storage.InvokeManager(context.Background(), InvokeManagerCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ObjectiveID: fixture.objective.ID,
		TaskID: fixture.planning.Task.ID, ManagerGrantID: fixture.grant.ID, LaunchProfileID: profile.Value.ID,
		ExpectedTaskRevision: fixture.planning.Task.Revision, ExpectedGrantRevision: fixture.grant.Revision,
		ExpectedProfileRevision: profile.Value.Revision,
		IdempotencyKey:          key + "-invoke", CorrelationID: "request-" + key + "-invoke",
	})
	if err != nil {
		t.Fatalf("InvokeManager(%s) = %v", key, err)
	}
	if _, err := storage.MarkRunStarting(context.Background(), invoked.Detail.Run.ID, "request-"+key+"-starting"); err != nil {
		t.Fatalf("MarkRunStarting(%s) = %v", key, err)
	}
	var asOfEventSequence int64
	if err := storage.db.QueryRow(`
SELECT json_extract(packet.packet_json,'$.as_of_event_sequence')
FROM run_context_bindings binding
JOIN context_packets packet ON packet.id=binding.context_packet_id
WHERE binding.run_id=?`, invoked.Detail.Run.ID).Scan(&asOfEventSequence); err != nil {
		t.Fatalf("read manager packet cursor for %s = %v", key, err)
	}
	return invoked.Detail.Run.ID, asOfEventSequence
}

func acceptAdversarialSchedulingPair(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture, key, claimPolicy string) {
	t.Helper()
	runID, asOfEventSequence := invokeAdversarialManager(t, storage, fixture, key)
	budget := domain.Budget{TokenLimit: 10, CostCents: 1, TimeSeconds: 10}
	actions := []domain.ManagerProposalAction{
		{Type: domain.ProposalActionCreateTask, CreateTask: &domain.ProposalCreateTaskAction{
			TaskKey: "first", LaunchProfileID: fixture.target.ID, Title: key + " first", Priority: 20, Budget: budget,
		}},
		{Type: domain.ProposalActionCreateTask, CreateTask: &domain.ProposalCreateTaskAction{
			TaskKey: "second", LaunchProfileID: fixture.target.ID, Title: key + " second", Priority: 10, Budget: budget,
		}},
	}
	if claimPolicy != "" {
		for _, taskKey := range []string{"first", "second"} {
			actions = append(actions, domain.ManagerProposalAction{
				Type: domain.ProposalActionDeclareClaimRequirement,
				DeclareClaimRequirement: &domain.ProposalDeclareClaimRequirementAction{
					Task: domain.ProposalTaskRef{ProposalTaskKey: taskKey}, Kind: domain.ClaimKindComponent,
					Target: "shared-renderer", Mode: domain.ClaimModeExclusive, ConflictPolicy: claimPolicy,
				},
			})
		}
	}
	submitted, err := storage.SubmitManagerProposal(context.Background(), SubmitManagerProposalCommand{
		RunID: runID, ManagerGrantID: fixture.grant.ID, ExpectedGrantRevision: fixture.grant.Revision,
		Kind: domain.ManagerProposalTaskDecomposition, Summary: "Create two simultaneously ready exact-profile tasks.",
		AsOfEventSequence: asOfEventSequence, Actions: actions,
		IdempotencyKey: key + "-submit", CorrelationID: "request-" + key + "-submit",
	})
	if err != nil || submitted.Proposal.Status != domain.ManagerProposalPending {
		t.Fatalf("SubmitManagerProposal(%s pair) = %#v, %v; want pending", key, submitted.Proposal, err)
	}
	accepted, err := storage.AcceptManagerProposal(context.Background(), AcceptManagerProposalCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ManagerProposalID: submitted.Proposal.ID, ExpectedRevision: submitted.Proposal.Revision,
		DecisionNote: "Accept exact contention fixture.", IdempotencyKey: key + "-accept", CorrelationID: "request-" + key + "-accept",
	})
	if err != nil || accepted.Proposal.Status != domain.ManagerProposalAccepted {
		t.Fatalf("AcceptManagerProposal(%s pair) = %#v, %v", key, accepted.Proposal, err)
	}
	if _, err := storage.MarkRunStarted(context.Background(), runID, key+"-runtime", key+"-provider", "request-"+key+"-started"); err != nil {
		t.Fatalf("MarkRunStarted(%s manager) = %v", key, err)
	}
	if _, err := storage.ApplyRunObservation(context.Background(), runID, domain.RunObservation{
		Kind: domain.ObservationCompletion, Message: "Submitted exact contention fixture.", Handoff: "Owner accepted both ready tasks.", LogArchive: prepareTestRunLogArchive(t, storage, runID),
	}, true, nil, "request-"+key+"-completed"); err != nil {
		t.Fatalf("ApplyRunObservation(%s manager completion) = %v", key, err)
	}
}

func configureSupervisorForContention(t *testing.T, storage *Store, workspaceID string, limits domain.SupervisorLimits, key string) {
	t.Helper()
	if _, err := storage.ConfigureSupervisorPolicy(context.Background(), ConfigureSupervisorPolicyCommand{
		WorkspaceIdentifier: workspaceID, Enabled: true, AutoSchedule: true, Limits: limits,
		AutoRetryLimit: 0, RetryCooldownSeconds: 0, ExpectedRevision: 1,
		IdempotencyKey: key + "-policy", CorrelationID: "request-" + key + "-policy",
	}); err != nil {
		t.Fatalf("ConfigureSupervisorPolicy(%s) = %v", key, err)
	}
}

func assertProposalIssue(t *testing.T, issues []domain.ProposalValidationIssue, code, severity string) {
	t.Helper()
	found := false
	for _, issue := range issues {
		if severity == domain.ProposalIssueWarning && issue.Severity == domain.ProposalIssueError {
			t.Fatalf("warning-only proposal also contained error issue %#v", issue)
		}
		if issue.Code == code && issue.Severity == severity {
			found = true
		}
	}
	if !found {
		t.Fatalf("proposal issues = %#v; want %s %s", issues, severity, code)
	}
}

func assertManagementRowCount(t *testing.T, storage *Store, wanted int, statement string, arguments ...any) {
	t.Helper()
	var got int
	if err := storage.db.QueryRow(statement, arguments...).Scan(&got); err != nil {
		t.Fatalf("count management rows = %v", err)
	}
	if got != wanted {
		t.Fatalf("management row count = %d, want %d for %s", got, wanted, statement)
	}
}

type managerGrantAdversarialFixture struct {
	workspace domain.Workspace
	project   domain.Project
	objective domain.Objective
	manager   domain.AgentDefinition
	target    domain.LaunchProfile
	planning  domain.TaskDetail
	grant     domain.ManagerGrant
}

type managerGrantAdversarialFixtureOptions struct {
	TargetMaxConcurrency int
	SharedTargetCheckout bool
	Clock                func() time.Time
}

func createManagerGrantAdversarialFixture(t *testing.T) (*Store, managerGrantAdversarialFixture) {
	return createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{})
}

func createManagerGrantAdversarialFixtureWithOptions(t *testing.T, options managerGrantAdversarialFixtureOptions) (*Store, managerGrantAdversarialFixture) {
	t.Helper()
	storage := openTestStore(t, t.TempDir(), Options{Clock: options.Clock})
	workspace, project := initializeWorkTestProject(t, storage)
	if options.SharedTargetCheckout {
		var checkoutID string
		if err := storage.db.QueryRow(`SELECT id FROM checkouts WHERE project_id=? ORDER BY id LIMIT 1`, project.ID).Scan(&checkoutID); err != nil {
			t.Fatalf("read target checkout = %v", err)
		}
		if _, err := storage.db.Exec(`UPDATE checkouts SET write_mode='shared' WHERE id=?`, checkoutID); err != nil {
			t.Fatalf("make adversarial checkout shared = %v", err)
		}
	}
	targetConcurrency := options.TargetMaxConcurrency
	if targetConcurrency == 0 {
		targetConcurrency = 1
	}
	ctx := context.Background()
	managerResult, err := storage.CreateAgent(ctx, CreateAgentCommand{
		WorkspaceIdentifier: workspace.ID, Name: "arbitrary-manager", Role: "constellation cartographer",
		Provider: "fake", Runtime: "fake", MaxConcurrency: 1,
		IdempotencyKey: "adversarial-manager", CorrelationID: "request-adversarial-manager",
	})
	if err != nil {
		t.Fatalf("CreateAgent(manager) = %v", err)
	}
	targetResult, err := storage.CreateAgent(ctx, CreateAgentCommand{
		WorkspaceIdentifier: workspace.ID, Name: "arbitrary-target", Role: "constellation cartographer",
		Provider: "fake", Runtime: "fake", MaxConcurrency: targetConcurrency,
		IdempotencyKey: "adversarial-target", CorrelationID: "request-adversarial-target",
	})
	if err != nil {
		t.Fatalf("CreateAgent(target) = %v", err)
	}
	objectiveResult, err := storage.CreateObjective(ctx, CreateObjectiveCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Title: "Adversarial management",
		Budget:         domain.Budget{TokenLimit: 10000, CostCents: 1000, TimeSeconds: 3600},
		IdempotencyKey: "adversarial-objective", CorrelationID: "request-adversarial-objective",
	})
	if err != nil {
		t.Fatalf("CreateObjective() = %v", err)
	}
	planningResult, err := storage.CreateTask(ctx, CreateTaskCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ObjectiveID: objectiveResult.Value.ID,
		Title: "Plan exact work", Budget: domain.Budget{TokenLimit: 500, CostCents: 50, TimeSeconds: 300},
		IdempotencyKey: "adversarial-planning", CorrelationID: "request-adversarial-planning",
	})
	if err != nil {
		t.Fatalf("CreateTask() = %v", err)
	}
	assigned, err := storage.AssignTask(ctx, AssignTaskCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: planningResult.Detail.Task.ID, AgentIdentifier: managerResult.Value.ID,
		LeaseSeconds: 900, ExpectedRevision: planningResult.Detail.Task.Revision,
		IdempotencyKey: "adversarial-assignment", CorrelationID: "request-adversarial-assignment",
	})
	if err != nil {
		t.Fatalf("AssignTask() = %v", err)
	}
	scenario := domain.FakeScenario{
		Schema: "urn:crewfold:schema:fixture:fake-run-scenario:v1", Name: "adversarial-target", Acceptance: domain.AcceptanceRule{},
		Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "bounded target"}},
	}
	targetProfile, err := storage.CreateLaunchProfile(ctx, CreateLaunchProfileCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: targetResult.Value.ID,
		ExpectedAgentRevision: targetResult.Value.Revision, Runtime: "fake", Provider: "fake", Scenario: scenario,
		AssignmentLeaseSeconds: 900, CapabilityTTLSeconds: 900,
		IdempotencyKey: "adversarial-target-profile", CorrelationID: "request-adversarial-target-profile",
	})
	if err != nil {
		t.Fatalf("CreateLaunchProfile() = %v", err)
	}
	grantResult, err := storage.CreateManagerGrant(ctx, CreateManagerGrantCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ObjectiveID: objectiveResult.Value.ID,
		TaskID: assigned.Detail.Task.ID, AgentIdentifier: managerResult.Value.ID,
		ExpectedTaskRevision: assigned.Detail.Task.Revision, ExpectedAgentRevision: managerResult.Value.Revision,
		ProposalKinds: []string{domain.ManagerProposalTaskDecomposition}, LaunchProfileIDs: []string{targetProfile.Value.ID},
		AllowedClaimKinds: []string{domain.ClaimKindComponent},
		Limits:            domain.ManagerProposalLimits{MaxOpenProposals: 2, MaxActions: 8, MaxTasks: 4, MaxDependencies: 4, MaxClaimRequirements: 2, Budget: domain.Budget{TokenLimit: 1000, CostCents: 100, TimeSeconds: 600}},
		IdempotencyKey:    "adversarial-grant", CorrelationID: "request-adversarial-grant",
	})
	if err != nil {
		t.Fatalf("CreateManagerGrant() = %v", err)
	}
	if managerResult.Value.Role != targetResult.Value.Role || !strings.Contains(managerResult.Value.Role, " ") {
		t.Fatalf("fixture roles = %q/%q; want identical arbitrary labels", managerResult.Value.Role, targetResult.Value.Role)
	}
	return storage, managerGrantAdversarialFixture{
		workspace: workspace, project: project, objective: objectiveResult.Value, manager: managerResult.Value,
		target: targetProfile.Value, planning: assigned.Detail, grant: grantResult.Value,
	}
}
