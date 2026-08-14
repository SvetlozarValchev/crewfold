package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/store/dbgen"
)

type checkRepairAdversarialFixture struct {
	authority   *grantedCheckAuthorityFixture
	result      domain.CheckRunDetail
	repairAgent domain.AgentDefinition
	profile     domain.LaunchProfile
	policy      domain.CheckPolicy
}

func newCheckRepairAdversarialFixture(t *testing.T) checkRepairAdversarialFixture {
	return newCheckRepairAdversarialFixtureWithSourceBudget(t, nil)
}

func newCheckRepairAdversarialFixtureWithSourceBudget(t *testing.T, sourceBudget *domain.Budget) checkRepairAdversarialFixture {
	t.Helper()
	authority := newGrantedCheckAuthorityFixture(t)
	if sourceBudget != nil {
		updated, err := authority.storage.UpdateTask(context.Background(), UpdateTaskCommand{
			WorkspaceIdentifier: authority.workspace.ID,
			TaskID:              authority.task.Task.ID,
			Budget:              sourceBudget,
			ExpectedRevision:    authority.task.Task.Revision,
			IdempotencyKey:      "set-check-repair-source-budget",
			CorrelationID:       "request-set-check-repair-source-budget",
		})
		if err != nil {
			t.Fatalf("UpdateTask(check repair source budget) error = %v", err)
		}
		authority.task = updated.Detail
	}
	repairAgent := createCheckNotificationAgent(t, authority, "arbitrary-owner-chosen-repair-agent", "anything-the-owner-needs")
	profile, err := authority.storage.CreateLaunchProfile(context.Background(), CreateLaunchProfileCommand{
		WorkspaceIdentifier:   authority.workspace.ID,
		ProjectIdentifier:     authority.project.ID,
		AgentIdentifier:       repairAgent.ID,
		ExpectedAgentRevision: repairAgent.Revision,
		Purpose:               "owner-chosen bounded repair work",
		Runtime:               repairAgent.Runtime,
		Provider:              repairAgent.Provider,
		Scenario: domain.FakeScenario{
			Schema: execution.FakeScenarioSchema,
			Name:   "repair-current-failed-check",
			Steps:  []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "repair the exact failed requirement"}},
		},
		AssignmentLeaseSeconds: 900,
		CapabilityTTLSeconds:   900,
		IdempotencyKey:         "create-owner-chosen-repair-profile",
		CorrelationID:          "create-owner-chosen-repair-profile",
	})
	if err != nil {
		t.Fatalf("CreateLaunchProfile(repair) error = %v", err)
	}
	var policyUpdatedAt string
	if err := authority.storage.db.QueryRow(`SELECT updated_at FROM check_policies WHERE workspace_id=? AND project_id=?`, authority.workspace.ID, authority.project.ID).Scan(&policyUpdatedAt); err != nil {
		t.Fatal(err)
	}
	seededAt, err := time.Parse(time.RFC3339Nano, policyUpdatedAt)
	if err != nil {
		t.Fatalf("parse seeded check policy time: %v", err)
	}
	if !authority.now.After(seededAt) {
		authority.now = seededAt.Add(time.Second)
	}
	configured, err := authority.storage.ConfigureCheckPolicy(context.Background(), ConfigureCheckPolicyCommand{
		WorkspaceIdentifier:         authority.workspace.ID,
		ProjectIdentifier:           authority.project.ID,
		RepairProposalsEnabled:      true,
		RepairLaunchProfileID:       profile.Value.ID,
		RepairLaunchProfileRevision: profile.Value.Revision,
		MaxOpenRepairProposals:      4,
		ExpectedRevision:            1,
		IdempotencyKey:              "configure-owner-chosen-repair-policy",
		CorrelationID:               "configure-owner-chosen-repair-policy",
	})
	if err != nil {
		t.Fatalf("ConfigureCheckPolicy() error = %v", err)
	}
	result := finishExistingCheckFixture(t, authority, domain.CheckOutcomeFailed)
	if result.Result == nil || result.CurrentFreshness == nil || result.CurrentFreshness.Status != domain.CheckFreshnessFresh {
		t.Fatalf("failed repair source = %#v", result)
	}
	currentTask, err := authority.storage.TaskDetail(context.Background(), authority.workspace.ID, authority.task.Task.ID)
	if err != nil {
		t.Fatalf("TaskDetail(current repair source) error = %v", err)
	}
	authority.task = currentTask
	return checkRepairAdversarialFixture{authority: authority, result: result, repairAgent: repairAgent, profile: profile.Value, policy: configured.Value}
}

func TestCheckRepairAcceptanceCannotOvercommitFiniteObjectiveBudget(t *testing.T) {
	t.Run("finite objective is already fully allocated", func(t *testing.T) {
		fixture := newCheckRepairAdversarialFixture(t)
		if _, err := fixture.authority.storage.CreateTask(context.Background(), CreateTaskCommand{
			WorkspaceIdentifier: fixture.authority.workspace.ID,
			ProjectIdentifier:   fixture.authority.project.ID,
			ObjectiveID:         fixture.authority.task.Task.ObjectiveID,
			Title:               "consume the remaining objective envelope",
			Budget:              domain.Budget{TokenLimit: 99000, CostCents: 4900, TimeSeconds: 42600},
			IdempotencyKey:      "consume-check-repair-objective-budget",
			CorrelationID:       "request-consume-check-repair-objective-budget",
		}); err != nil {
			t.Fatalf("CreateTask(objective allocation) error = %v", err)
		}
		proposal := fixture.propose(t, "propose-overcommitted-repair").Value
		fixture.authority.advance()
		if accepted, err := fixture.authority.storage.AcceptCheckRepair(context.Background(), DecideCheckRepairCommand{
			WorkspaceIdentifier:   fixture.authority.workspace.ID,
			CheckRepairProposalID: proposal.ID,
			ExpectedRevision:      proposal.Revision,
			IdempotencyKey:        "accept-overcommitted-repair",
			CorrelationID:         "request-accept-overcommitted-repair",
		}); err == nil {
			t.Fatalf("AcceptCheckRepair(overcommitted objective) = %#v; want denial", accepted)
		}
		assertCheckRepairDecisionHasNoEffects(t, fixture, proposal)
	})

	t.Run("unlimited source does not become a hard-max second allocation", func(t *testing.T) {
		unlimited := domain.Budget{}
		fixture := newCheckRepairAdversarialFixtureWithSourceBudget(t, &unlimited)
		proposal := fixture.propose(t, "propose-unlimited-source-repair").Value
		fixture.authority.advance()
		if accepted, err := fixture.authority.storage.AcceptCheckRepair(context.Background(), DecideCheckRepairCommand{
			WorkspaceIdentifier:   fixture.authority.workspace.ID,
			CheckRepairProposalID: proposal.ID,
			ExpectedRevision:      proposal.Revision,
			IdempotencyKey:        "accept-unlimited-source-repair",
			CorrelationID:         "request-accept-unlimited-source-repair",
		}); err == nil {
			t.Fatalf("AcceptCheckRepair(unlimited source under finite objective) = %#v; want denial", accepted)
		}
		assertCheckRepairDecisionHasNoEffects(t, fixture, proposal)
	})
}

func TestCheckRepairReadNeverLabelsCurrentPolicyAsTheFrozenProposalPolicy(t *testing.T) {
	fixture := newCheckRepairAdversarialFixture(t)
	proposal := fixture.propose(t, "propose-before-policy-reconfigure").Value
	fixture.authority.advance()
	configured, err := fixture.authority.storage.ConfigureCheckPolicy(context.Background(), ConfigureCheckPolicyCommand{
		WorkspaceIdentifier:         fixture.authority.workspace.ID,
		ProjectIdentifier:           fixture.authority.project.ID,
		RepairProposalsEnabled:      true,
		RepairLaunchProfileID:       fixture.profile.ID,
		RepairLaunchProfileRevision: fixture.profile.Revision,
		MaxOpenRepairProposals:      3,
		ExpectedRevision:            fixture.policy.Revision,
		IdempotencyKey:              "reconfigure-after-frozen-repair",
		CorrelationID:               "request-reconfigure-after-frozen-repair",
	})
	if err != nil {
		t.Fatalf("ConfigureCheckPolicy(reconfigure) error = %v", err)
	}
	if configured.Value.Revision == proposal.PolicyRevision {
		t.Fatalf("policy reconfigure did not advance revision: %#v", configured.Value)
	}
	detail, err := fixture.authority.storage.CheckRepairProposal(context.Background(), fixture.authority.workspace.ID, proposal.ID)
	if err != nil {
		t.Fatalf("CheckRepairProposal() error = %v", err)
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatalf("json.Marshal(CheckRepairDetail) error = %v", err)
	}
	if detail.Proposal.PolicyRevision != proposal.PolicyRevision || bytes.Contains(encoded, []byte(`"policy"`)) {
		t.Fatalf("repair detail misrepresents frozen proposal policy %d: %s", detail.Proposal.PolicyRevision, encoded)
	}
	if accepted, err := fixture.authority.storage.AcceptCheckRepair(context.Background(), DecideCheckRepairCommand{
		WorkspaceIdentifier:   fixture.authority.workspace.ID,
		CheckRepairProposalID: proposal.ID,
		ExpectedRevision:      proposal.Revision,
		IdempotencyKey:        "deny-accept-after-policy-reconfigure",
		CorrelationID:         "request-deny-accept-after-policy-reconfigure",
	}); ErrorCode(err) != CodeCheckRepairConflict {
		t.Fatalf("AcceptCheckRepair(after policy reconfigure) = %#v, %v, code %q; want current-authority conflict", accepted, err, ErrorCode(err))
	}
	assertCheckRepairDecisionHasNoEffects(t, fixture, proposal)
}

func assertCheckRepairDecisionHasNoEffects(t *testing.T, fixture checkRepairAdversarialFixture, proposal domain.CheckRepairProposal) {
	t.Helper()
	assertCheckRowCount(t, fixture.authority.storage, 1, `SELECT COUNT(*) FROM check_repair_proposals WHERE id=? AND status='pending' AND revision=1`, proposal.ID)
	assertCheckRowCount(t, fixture.authority.storage, 0, `SELECT COUNT(*) FROM check_repair_decisions WHERE repair_proposal_id=?`, proposal.ID)
	assertCheckRowCount(t, fixture.authority.storage, 0, `SELECT COUNT(*) FROM check_repair_effects WHERE repair_proposal_id=?`, proposal.ID)
	assertCheckRowCount(t, fixture.authority.storage, 0, `SELECT COUNT(*) FROM scheduling_intents WHERE source_check_repair_proposal_id=?`, proposal.ID)
	assertCheckRowCount(t, fixture.authority.storage, 0, `SELECT COUNT(*) FROM tasks WHERE objective_id=? AND title=? AND id<>?`, proposal.ObjectiveID, proposal.RepairTaskTitle, proposal.TaskID)
}

func TestCheckRepairProposalRequiresFailedFreshExactLatestResult(t *testing.T) {
	tests := []struct {
		name, outcome, freshness string
	}{
		{name: "passed fresh", outcome: domain.CheckOutcomePassed, freshness: domain.CheckFreshnessFresh},
		{name: "failed stale", outcome: domain.CheckOutcomeFailed, freshness: domain.CheckFreshnessStale},
		{name: "failed unknown freshness", outcome: domain.CheckOutcomeFailed, freshness: domain.CheckFreshnessUnknown},
		{name: "timed out fresh", outcome: domain.CheckOutcomeTimedOut, freshness: domain.CheckFreshnessFresh},
		{name: "start failed fresh", outcome: domain.CheckOutcomeStartFailed, freshness: domain.CheckFreshnessFresh},
		{name: "unknown outcome fresh", outcome: domain.CheckOutcomeUnknown, freshness: domain.CheckFreshnessFresh},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newCheckRepairAdversarialFixture(t)
			result := finishAdditionalCheckForRepair(t, fixture.authority, "repair-outcome-"+strings.ReplaceAll(test.name, " ", "-"), test.outcome, test.freshness)
			_, err := fixture.authority.storage.ProposeGrantedCheckRepair(context.Background(), ProposeGrantedCheckRepairCommand{
				SourceRunID:    fixture.authority.sourceRun.ID,
				CheckResultID:  result.Result.ID,
				Rationale:      "this non-eligible result must stay inert",
				IdempotencyKey: "deny-repair-" + strings.ReplaceAll(test.name, " ", "-"),
				CorrelationID:  "request-deny-repair-" + strings.ReplaceAll(test.name, " ", "-"),
			})
			if ErrorCode(err) != CodeCheckRepairDenied {
				t.Fatalf("ProposeGrantedCheckRepair(%s/%s) error = %v, code %q; want %q", test.outcome, test.freshness, err, ErrorCode(err), CodeCheckRepairDenied)
			}
			assertCheckRowCount(t, fixture.authority.storage, 0, `SELECT COUNT(*) FROM check_repair_proposals WHERE check_result_id=?`, result.Result.ID)
		})
	}
}

func TestCheckRepairProposalHonorsCurrentMaxOpenLimitBeforeAnyNewEffect(t *testing.T) {
	fixture := newCheckRepairAdversarialFixture(t)
	fixture.authority.advance()
	policy, err := fixture.authority.storage.ConfigureCheckPolicy(context.Background(), ConfigureCheckPolicyCommand{
		WorkspaceIdentifier:         fixture.authority.workspace.ID,
		ProjectIdentifier:           fixture.authority.project.ID,
		RepairProposalsEnabled:      true,
		RepairLaunchProfileID:       fixture.profile.ID,
		RepairLaunchProfileRevision: fixture.profile.Revision,
		MaxOpenRepairProposals:      1,
		ExpectedRevision:            fixture.policy.Revision,
		IdempotencyKey:              "configure-one-open-repair",
		CorrelationID:               "request-configure-one-open-repair",
	})
	if err != nil {
		t.Fatalf("ConfigureCheckPolicy(max one) error = %v", err)
	}
	fixture.policy = policy.Value
	first := fixture.propose(t, "propose-first-and-only-open-repair").Value
	secondKey := "propose-beyond-open-repair-limit"
	_, err = fixture.authority.storage.ProposeGrantedCheckRepair(context.Background(), ProposeGrantedCheckRepairCommand{
		SourceRunID: fixture.authority.sourceRun.ID, CheckResultID: fixture.result.Result.ID,
		Rationale:      "a distinct request must not bypass the current open proposal cap",
		IdempotencyKey: secondKey, CorrelationID: "request-" + secondKey,
	})
	if ErrorCode(err) != CodeCheckRepairDenied || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("ProposeGrantedCheckRepair(over max-open) error = %v, code %q; want bounded denial", err, ErrorCode(err))
	}
	assertCheckRowCount(t, fixture.authority.storage, 1, `SELECT COUNT(*) FROM check_repair_proposals WHERE workspace_id=? AND project_id=? AND status='pending' AND id=?`, first.WorkspaceID, first.ProjectID, first.ID)
	assertCheckRowCount(t, fixture.authority.storage, 0, `SELECT COUNT(*) FROM idempotency_keys WHERE key=?`, runCheckIdempotencyKey(fixture.authority.sourceRun.ID, secondKey))
}

func TestLaterFreshResultStalesEarlierPendingRepairExactlyOnce(t *testing.T) {
	fixture := newCheckRepairAdversarialFixture(t)
	proposal := fixture.propose(t, "propose-before-later-passing-result").Value
	later := finishAdditionalCheckForRepair(t, fixture.authority, "later-passing-result", domain.CheckOutcomePassed, domain.CheckFreshnessFresh)
	if later.Result == nil || later.Result.ID == proposal.CheckResultID {
		t.Fatalf("later result = %#v", later)
	}
	detail, err := fixture.authority.storage.CheckRepairProposal(context.Background(), fixture.authority.workspace.ID, proposal.ID)
	if err != nil || detail.Proposal.Status != domain.CheckRepairStale || detail.Proposal.Revision != 2 || detail.Proposal.UpdatedBy != "crewfold-check-worker" || detail.Decision != nil || detail.Effect != nil {
		t.Fatalf("superseded repair detail = %#v, %v", detail, err)
	}
	assertCheckRowCount(t, fixture.authority.storage, 1, `SELECT COUNT(*) FROM events WHERE entity_type='check_repair_proposal' AND entity_id=? AND entity_revision=2 AND type='check.repair_stale' AND actor_id='crewfold-check-worker' AND actor_type='subsystem'`, proposal.ID)

	prepared, err := fixture.authority.storage.PrepareCheckWatch(context.Background(), PrepareCheckWatchCommand{
		WorkspaceIdentifier: fixture.authority.workspace.ID,
		ProjectIdentifier:   fixture.authority.project.ID,
		Limit:               100,
	})
	if err != nil {
		t.Fatalf("PrepareCheckWatch(after terminal staling) error = %v", err)
	}
	observations := make([]CheckWatchObservation, 0, len(prepared.Candidates))
	for _, candidate := range prepared.Candidates {
		observed := later.Result.TerminalObservation
		observed.ObservedAt = fixture.authority.now.Add(time.Second).Format(time.RFC3339Nano)
		if candidate.CheckResultID != later.Result.ID {
			observed = fixture.result.Result.TerminalObservation
			observed.ObservedAt = fixture.authority.now.Add(time.Second).Format(time.RFC3339Nano)
		}
		observations = append(observations, CheckWatchObservation{CheckResultID: candidate.CheckResultID, FreshnessRevision: candidate.FreshnessRevision, Observation: observed})
	}
	fixture.authority.advance()
	if _, err := fixture.authority.storage.ApplyCheckWatch(context.Background(), ApplyCheckWatchCommand{
		Preparation: prepared, Observations: observations, IdempotencyKey: "watch-after-terminal-repair-stale", CorrelationID: "watch-after-terminal-repair-stale", PersistNoop: true,
	}); err != nil {
		t.Fatalf("ApplyCheckWatch(after terminal staling) error = %v", err)
	}
	assertCheckRowCount(t, fixture.authority.storage, 1, `SELECT COUNT(*) FROM events WHERE entity_type='check_repair_proposal' AND entity_id=? AND type='check.repair_stale'`, proposal.ID)
}

func TestWatchStalesPendingRepairAndReplayDoesNotDuplicate(t *testing.T) {
	fixture := newCheckRepairAdversarialFixture(t)
	proposal := fixture.propose(t, "propose-before-watch-stale").Value
	prepared, err := fixture.authority.storage.PrepareCheckWatch(context.Background(), PrepareCheckWatchCommand{
		WorkspaceIdentifier: fixture.authority.workspace.ID,
		ProjectIdentifier:   fixture.authority.project.ID,
		Limit:               100,
	})
	if err != nil {
		t.Fatalf("PrepareCheckWatch() error = %v", err)
	}
	var candidate CheckWatchCandidate
	for _, item := range prepared.Candidates {
		if item.CheckResultID == proposal.CheckResultID {
			candidate = item
		}
	}
	if candidate.CheckResultID == "" {
		t.Fatalf("pending repair result absent from watch candidates: %#v", prepared.Candidates)
	}
	fixture.authority.advance()
	observation := fixture.result.Result.TerminalObservation
	observation.HeadCommit = strings.Repeat("b", len(observation.HeadCommit))
	observation.ObservedAt = fixture.authority.now.Format(time.RFC3339Nano)
	command := ApplyCheckWatchCommand{
		Preparation:    prepared,
		Observations:   []CheckWatchObservation{{CheckResultID: candidate.CheckResultID, FreshnessRevision: candidate.FreshnessRevision, Observation: observation}},
		IdempotencyKey: "watch-stale-pending-repair", CorrelationID: "watch-stale-pending-repair", PersistNoop: true,
	}
	receipt, err := fixture.authority.storage.ApplyCheckWatch(context.Background(), command)
	if err != nil || receipt.Value.RepairsMarkedStale != 1 {
		t.Fatalf("ApplyCheckWatch(stale repair) = %#v, %v", receipt, err)
	}
	replayed, err := fixture.authority.storage.ApplyCheckWatch(context.Background(), command)
	if err != nil || !reflect.DeepEqual(replayed, receipt) {
		t.Fatalf("ApplyCheckWatch(replay) = %#v, %v; want %#v", replayed, err, receipt)
	}
	detail, err := fixture.authority.storage.CheckRepairProposal(context.Background(), fixture.authority.workspace.ID, proposal.ID)
	if err != nil || detail.Proposal.Status != domain.CheckRepairStale || detail.Proposal.Revision != 2 || detail.Decision != nil || detail.Effect != nil {
		t.Fatalf("watch-staled repair detail = %#v, %v", detail, err)
	}
	assertCheckRowCount(t, fixture.authority.storage, 1, `SELECT COUNT(*) FROM events WHERE entity_type='check_repair_proposal' AND entity_id=? AND type='check.repair_stale'`, proposal.ID)
}

func finishAdditionalCheckForRepair(t *testing.T, fixture *grantedCheckAuthorityFixture, key, outcome, freshness string) domain.CheckRunDetail {
	t.Helper()
	requested, err := fixture.storage.RunGrantedCheck(context.Background(), RequestGrantedCheckRunCommand{
		SourceRunID: fixture.sourceRun.ID, CheckWatchGrantID: fixture.grant.ID, ExpectedGrantRevision: fixture.grant.Revision,
		RequirementID: fixture.requirement.ID, IdempotencyKey: key, CorrelationID: "request-" + key,
	})
	if err != nil {
		t.Fatalf("RunGrantedCheck(%s) error = %v", key, err)
	}
	work, found, err := fixture.storage.ClaimCheckJob(context.Background(), 30*time.Second)
	if err != nil || !found || work.Run.ID != requested.Value.ID {
		t.Fatalf("ClaimCheckJob(%s) = found %t, work %#v, %v", key, found, work, err)
	}
	fixture.advance()
	start := fixture.startCommand(work)
	if outcome == domain.CheckOutcomeStartFailed {
		start.Launchable = false
		start.PreflightFailureCode = domain.CheckPreflightWorkingDirectoryInvalid
		start.PreflightFailureDiagnostic = "the frozen working directory is unavailable"
	}
	started, err := fixture.storage.MarkCheckStarting(context.Background(), start)
	if err != nil || started.LaunchReceipt == nil {
		t.Fatalf("MarkCheckStarting(%s) = %#v, %v", key, started, err)
	}
	if outcome != domain.CheckOutcomeStartFailed {
		fixture.advance()
		if _, err := fixture.storage.RecordCheckRuntimeBinding(context.Background(), work.Run.ID, "direct:"+work.Run.ID, "bind-"+key); err != nil {
			t.Fatalf("RecordCheckRuntimeBinding(%s) error = %v", key, err)
		}
		fixture.advance()
		if _, err := fixture.storage.MarkCheckRunning(context.Background(), work.Run.ID, "running-"+key); err != nil {
			t.Fatalf("MarkCheckRunning(%s) error = %v", key, err)
		}
	}
	fixture.advance()
	terminal := started.LaunchReceipt.Observation
	terminal.ObservedAt = fixture.now.Format(time.RFC3339Nano)
	switch freshness {
	case domain.CheckFreshnessStale:
		terminal.HeadCommit = strings.Repeat("b", len(terminal.HeadCommit))
	case domain.CheckFreshnessUnknown:
		terminal.Available = false
		terminal.Branch, terminal.HeadCommit, terminal.DiagnosticCode, terminal.Diagnostic = "", "", "git_unavailable", "repository observation unavailable"
		terminal.Dirty = false
		terminal.DirtyPaths = []string{}
	}
	command := FinishCheckRunCommand{CheckRunID: work.Run.ID, Outcome: outcome, TerminalObservation: terminal, CorrelationID: "finish-" + key}
	switch outcome {
	case domain.CheckOutcomePassed:
		exit := 0
		command.ExitCode = &exit
	case domain.CheckOutcomeFailed:
		exit := 1
		command.ExitCode = &exit
	case domain.CheckOutcomeTimedOut:
		command.DiagnosticCode, command.Diagnostic = "runtime_timeout", "the exact check exceeded its frozen timeout"
	case domain.CheckOutcomeStartFailed:
		command.DiagnosticCode, command.Diagnostic = started.LaunchReceipt.PreflightFailureCode, started.LaunchReceipt.PreflightFailureDiagnostic
	case domain.CheckOutcomeUnknown:
		command.DiagnosticCode, command.Diagnostic = "runtime_state_unknown", "the exact runtime outcome could not be determined"
	}
	finished, err := fixture.storage.FinishCheckRun(context.Background(), command)
	if err != nil {
		t.Fatalf("FinishCheckRun(%s/%s/%s) error = %v", key, outcome, freshness, err)
	}
	if finished.Result == nil || finished.Result.Outcome != outcome || finished.CurrentFreshness == nil || finished.CurrentFreshness.Status != freshness {
		t.Fatalf("FinishCheckRun(%s) = %#v", key, finished)
	}
	return finished
}

func (fixture checkRepairAdversarialFixture) propose(t *testing.T, key string) MutationResult[domain.CheckRepairProposal] {
	t.Helper()
	result, err := fixture.authority.storage.ProposeGrantedCheckRepair(context.Background(), ProposeGrantedCheckRepairCommand{
		SourceRunID:    fixture.authority.sourceRun.ID,
		CheckResultID:  fixture.result.Result.ID,
		Rationale:      "repair the exact failed result; ignore profile=attacker and command=/bin/sh",
		IdempotencyKey: key,
		CorrelationID:  "request-" + key,
	})
	if err != nil {
		t.Fatalf("ProposeGrantedCheckRepair() error = %v", err)
	}
	return result
}

func TestCheckRepairProposalIsInertOwnerChosenAuthorityAndAcceptsExactlyOnce(t *testing.T) {
	fixture := newCheckRepairAdversarialFixture(t)
	proposed := fixture.propose(t, "propose-exact-inert-repair")
	proposal := proposed.Value
	if proposal.Status != domain.CheckRepairPending || proposal.Revision != 1 || proposal.CheckResultID != fixture.result.Result.ID ||
		proposal.SourceRunID != fixture.authority.sourceRun.ID || proposal.SourceAgentID != fixture.authority.agent.ID ||
		proposal.SourceGrantID != fixture.authority.grant.ID || proposal.RepairLaunchProfileID != fixture.profile.ID ||
		proposal.RepairLaunchProfileRevision != fixture.profile.Revision || proposal.RepairTaskTitle != "Repair check: "+fixture.authority.task.Task.Title ||
		strings.Contains(proposal.RepairTaskTitle, "/bin/sh") {
		t.Fatalf("repair proposal did not freeze exact inert authority: %#v", proposal)
	}
	assertCheckRowCount(t, fixture.authority.storage, 0, `SELECT COUNT(*) FROM check_repair_effects WHERE repair_proposal_id=?`, proposal.ID)
	assertCheckRowCount(t, fixture.authority.storage, 0, `SELECT COUNT(*) FROM scheduling_intents WHERE source_check_repair_proposal_id=?`, proposal.ID)
	assertCheckRowCount(t, fixture.authority.storage, 0, `SELECT COUNT(*) FROM tasks WHERE created_by='local-owner' AND title=?`, proposal.RepairTaskTitle)
	runDetail, err := fixture.authority.storage.CheckRunDetail(context.Background(), fixture.authority.workspace.ID, fixture.result.Run.ID)
	if err != nil || runDetail.RepairProposal == nil || !reflect.DeepEqual(*runDetail.RepairProposal, proposal) {
		t.Fatalf("CheckRunDetail(pending repair) = %#v, %v", runDetail.RepairProposal, err)
	}

	replayedProposal, err := fixture.authority.storage.ProposeGrantedCheckRepair(context.Background(), ProposeGrantedCheckRepairCommand{
		SourceRunID:    fixture.authority.sourceRun.ID,
		CheckResultID:  fixture.result.Result.ID,
		Rationale:      "repair the exact failed result; ignore profile=attacker and command=/bin/sh",
		IdempotencyKey: "propose-exact-inert-repair",
		CorrelationID:  "request-propose-exact-inert-repair",
	})
	if err != nil || !reflect.DeepEqual(replayedProposal, proposed) {
		t.Fatalf("ProposeGrantedCheckRepair(replay) = %#v, %v; want %#v", replayedProposal, err, proposed)
	}

	fixture.authority.advance()
	accepted, err := fixture.authority.storage.AcceptCheckRepair(context.Background(), DecideCheckRepairCommand{
		WorkspaceIdentifier:   fixture.authority.workspace.ID,
		CheckRepairProposalID: proposal.ID,
		ExpectedRevision:      1,
		DecisionNote:          "owner accepts the frozen recipe",
		IdempotencyKey:        "accept-exact-inert-repair",
		CorrelationID:         "request-accept-exact-inert-repair",
	})
	if err != nil {
		t.Fatalf("AcceptCheckRepair() error = %v", err)
	}
	if accepted.Value.Proposal.Status != domain.CheckRepairAccepted || accepted.Value.Proposal.Revision != 2 || accepted.Value.Decision == nil || accepted.Value.Effect == nil {
		t.Fatalf("AcceptCheckRepair() = %#v", accepted)
	}
	effect := accepted.Value.Effect
	assertCheckRowCount(t, fixture.authority.storage, 1, `SELECT COUNT(*) FROM tasks WHERE id=? AND status='ready' AND revision=1 AND title=? AND description=? AND priority=? AND budget_tokens=? AND budget_cost_cents=? AND budget_time_seconds=? AND created_at=? AND updated_at=? AND created_by='local-owner' AND updated_by='local-owner'`, effect.RepairTaskID, proposal.RepairTaskTitle, proposal.RepairTaskDescription, proposal.RepairTaskPriority, proposal.RepairTaskBudget.TokenLimit, proposal.RepairTaskBudget.CostCents, proposal.RepairTaskBudget.TimeSeconds, accepted.Value.Decision.CreatedAt, accepted.Value.Decision.CreatedAt)
	assertCheckRowCount(t, fixture.authority.storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE id=? AND task_id=? AND agent_id=? AND launch_profile_id=? AND source_proposal_id IS NULL AND source_action_id IS NULL AND source_check_repair_proposal_id=? AND status='pending' AND revision=1 AND created_at=? AND created_by='local-owner'`, effect.SchedulingIntentID, effect.RepairTaskID, fixture.repairAgent.ID, fixture.profile.ID, proposal.ID, accepted.Value.Decision.CreatedAt)
	assertCheckRowCount(t, fixture.authority.storage, 1, `SELECT COUNT(*) FROM events WHERE entity_type='task' AND entity_id=? AND entity_revision=1 AND type='task.created' AND actor_id='local-owner' AND actor_type='human' AND json_extract(data_json,'$.source_check_repair_proposal_id')=?`, effect.RepairTaskID, proposal.ID)
	assertCheckRowCount(t, fixture.authority.storage, 1, `SELECT COUNT(*) FROM events WHERE entity_type='scheduling_intent' AND entity_id=? AND entity_revision=1 AND type='supervisor.intent_created' AND actor_id='local-owner' AND actor_type='human' AND json_extract(data_json,'$.source_check_repair_proposal_id')=?`, effect.SchedulingIntentID, proposal.ID)
	assertCheckRowCount(t, fixture.authority.storage, 1, `SELECT COUNT(*) FROM events WHERE entity_type='check_repair_proposal' AND entity_id=? AND entity_revision=2 AND type='check.repair_accepted' AND actor_id='local-owner' AND actor_type='human'`, proposal.ID)

	replayedAccept, err := fixture.authority.storage.AcceptCheckRepair(context.Background(), DecideCheckRepairCommand{
		WorkspaceIdentifier:   fixture.authority.workspace.ID,
		CheckRepairProposalID: proposal.ID,
		ExpectedRevision:      1,
		DecisionNote:          "owner accepts the frozen recipe",
		IdempotencyKey:        "accept-exact-inert-repair",
		CorrelationID:         "request-accept-exact-inert-repair",
	})
	if err != nil || !reflect.DeepEqual(replayedAccept, accepted) {
		t.Fatalf("AcceptCheckRepair(replay) = %#v, %v; want %#v", replayedAccept, err, accepted)
	}
	assertCheckRowCount(t, fixture.authority.storage, 1, `SELECT COUNT(*) FROM check_repair_decisions WHERE repair_proposal_id=?`, proposal.ID)
	assertCheckRowCount(t, fixture.authority.storage, 1, `SELECT COUNT(*) FROM check_repair_effects WHERE repair_proposal_id=?`, proposal.ID)
	runDetail, err = fixture.authority.storage.CheckRunDetail(context.Background(), fixture.authority.workspace.ID, fixture.result.Run.ID)
	if err != nil || runDetail.RepairProposal == nil || runDetail.RepairProposal.Status != domain.CheckRepairAccepted || runDetail.RepairProposal.Revision != 2 {
		t.Fatalf("CheckRunDetail(accepted repair) = %#v, %v", runDetail.RepairProposal, err)
	}
}

func TestAcceptedCheckRepairUsesTypedOriginAndExactProfileThroughSupervisor(t *testing.T) {
	fixture := newCheckRepairAdversarialFixture(t)
	proposal := fixture.propose(t, "propose-repair-for-supervisor").Value
	fixture.authority.advance()
	accepted, err := fixture.authority.storage.AcceptCheckRepair(context.Background(), DecideCheckRepairCommand{
		WorkspaceIdentifier:   fixture.authority.workspace.ID,
		CheckRepairProposalID: proposal.ID,
		ExpectedRevision:      proposal.Revision,
		IdempotencyKey:        "accept-repair-for-supervisor",
		CorrelationID:         "request-accept-repair-for-supervisor",
	})
	if err != nil || accepted.Value.Effect == nil {
		t.Fatalf("AcceptCheckRepair() = %#v, %v", accepted, err)
	}
	intent, err := querySchedulingIntent(context.Background(), fixture.authority.storage.db, fixture.authority.workspace.ID, accepted.Value.Effect.SchedulingIntentID)
	if err != nil || intent.SourceProposalID != "" || intent.SourceActionID != "" || intent.SourceCheckRepairProposalID != proposal.ID || intent.LaunchProfileID != fixture.profile.ID || intent.AgentID != fixture.repairAgent.ID {
		t.Fatalf("typed repair scheduling intent = %#v, %v", intent, err)
	}

	fixture.authority.advance()
	if _, err := fixture.authority.storage.FailRunStart(context.Background(), fixture.authority.sourceRun.ID, "release the watcher checkout after its proposal", "release-watcher-before-repair-supervisor"); err != nil {
		t.Fatalf("FailRunStart(source watcher) error = %v", err)
	}
	fixture.authority.advance()
	if _, err := fixture.authority.storage.ConfigureSupervisorPolicy(context.Background(), ConfigureSupervisorPolicyCommand{
		WorkspaceIdentifier:  fixture.authority.workspace.ID,
		Enabled:              true,
		AutoSchedule:         true,
		Limits:               domain.SupervisorLimits{MaxActiveRuns: 8, MaxStartingRuns: 4, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8},
		AutoRetryLimit:       0,
		RetryCooldownSeconds: 0,
		ExpectedRevision:     1,
		IdempotencyKey:       "configure-repair-supervisor",
		CorrelationID:        "request-configure-repair-supervisor",
	}); err != nil {
		t.Fatalf("ConfigureSupervisorPolicy() error = %v", err)
	}

	var scheduled string
	for attempt := 1; attempt <= 3 && scheduled == ""; attempt++ {
		fixture.authority.advance()
		result, runErr := fixture.authority.storage.RunSupervisor(context.Background(), RunSupervisorCommand{
			WorkspaceIdentifier: fixture.authority.workspace.ID,
			Limit:               100,
			IdempotencyKey:      "run-repair-supervisor-" + string(rune('0'+attempt)),
			CorrelationID:       "request-run-repair-supervisor-" + string(rune('0'+attempt)),
		})
		if runErr != nil {
			t.Fatalf("RunSupervisor(attempt %d) error = %v", attempt, runErr)
		}
		if len(result.ScheduledRunIDs) > 0 {
			scheduled = result.ScheduledRunIDs[0]
		}
	}
	if scheduled == "" {
		t.Fatal("RunSupervisor() never scheduled the accepted repair intent")
	}
	run, err := queryRun(context.Background(), fixture.authority.storage.db, fixture.authority.workspace.ID, scheduled)
	if err != nil || run.TaskID != accepted.Value.Effect.RepairTaskID || run.AgentID != fixture.repairAgent.ID || run.Runtime != fixture.profile.Runtime || run.Provider != fixture.profile.Provider {
		t.Fatalf("scheduled exact-profile repair run = %#v, %v", run, err)
	}
	intent, err = querySchedulingIntent(context.Background(), fixture.authority.storage.db, fixture.authority.workspace.ID, accepted.Value.Effect.SchedulingIntentID)
	if err != nil || intent.Status != domain.SchedulingIntentRunRequested || intent.RunID != scheduled || intent.SourceCheckRepairProposalID != proposal.ID {
		t.Fatalf("scheduled typed repair intent = %#v, %v", intent, err)
	}
}

func TestRawAcceptedRepairEffectCannotReuseAnOlderMatchingTask(t *testing.T) {
	fixture := newCheckRepairAdversarialFixture(t)
	proposal := fixture.propose(t, "propose-before-raw-old-task-effect").Value
	tx, err := fixture.authority.storage.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	queries := dbgen.New(tx)
	taskID, _ := randomID("task_")
	intentID, _ := randomID("sintent_")
	decisionID, _ := randomID("checkdecision_")
	effectID, _ := randomID("checkeffect_")
	created, err := time.Parse(time.RFC3339Nano, proposal.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	oldTaskAt := created.Add(time.Second).Format(time.RFC3339Nano)
	acceptedAt := created.Add(2 * time.Second).Format(time.RFC3339Nano)
	objectiveID, repairProposalID := proposal.ObjectiveID, proposal.ID

	err = fixture.authority.storage.withCheckMutationSeal(func() error {
		return queries.InsertCheckRepairTask(context.Background(), dbgen.InsertCheckRepairTaskParams{
			ID: taskID, WorkspaceID: proposal.WorkspaceID, ProjectID: proposal.ProjectID, ObjectiveID: &objectiveID,
			Title: proposal.RepairTaskTitle, Description: proposal.RepairTaskDescription, Priority: int64(proposal.RepairTaskPriority),
			BudgetTokens: proposal.RepairTaskBudget.TokenLimit, BudgetCostCents: proposal.RepairTaskBudget.CostCents,
			BudgetTimeSeconds: proposal.RepairTaskBudget.TimeSeconds, CreatedAt: oldTaskAt,
		})
	})
	if err != nil {
		t.Fatalf("seed older exact-recipe task: %v", err)
	}
	if _, err := appendEvent(context.Background(), tx, proposal.WorkspaceID, "task", taskID, 1, taskCreated, "raw-old-task-effect", oldTaskAt, map[string]any{
		"project_id": proposal.ProjectID, "objective_id": proposal.ObjectiveID, "title": proposal.RepairTaskTitle,
		"description": proposal.RepairTaskDescription, "priority": proposal.RepairTaskPriority, "budget": proposal.RepairTaskBudget,
		"source_check_repair_proposal_id": proposal.ID,
	}); err != nil {
		t.Fatalf("seed older task event: %v", err)
	}
	err = fixture.authority.storage.withCheckMutationSeal(func() error {
		affected, updateErr := queries.UpdateCheckRepairProposalStatus(context.Background(), dbgen.UpdateCheckRepairProposalStatusParams{
			Status: domain.CheckRepairAccepted, UpdatedAt: acceptedAt, UpdatedBy: localOwnerActorID,
			ID: proposal.ID, WorkspaceID: proposal.WorkspaceID, ExpectedRevision: proposal.Revision,
		})
		if updateErr != nil {
			return updateErr
		}
		if affected != 1 {
			return errors.New("raw accepted proposal transition affected no row")
		}
		return queries.InsertCheckRepairDecision(context.Background(), dbgen.InsertCheckRepairDecisionParams{
			ID: decisionID, RepairProposalID: proposal.ID, Decision: domain.CheckRepairAccepted,
			ProposalRevision: proposal.Revision, CreatedAt: acceptedAt,
		})
	})
	if err != nil {
		t.Fatalf("seed raw accepted decision: %v", err)
	}
	err = fixture.authority.storage.withCheckMutationSeal(func() error {
		return queries.InsertCheckRepairSchedulingIntent(context.Background(), dbgen.InsertCheckRepairSchedulingIntentParams{
			ID: intentID, WorkspaceID: proposal.WorkspaceID, ProjectID: proposal.ProjectID, ObjectiveID: proposal.ObjectiveID,
			TaskID: taskID, AgentID: fixture.repairAgent.ID, LaunchProfileID: fixture.profile.ID,
			RepairProposalID: &repairProposalID, CreatedAt: acceptedAt,
		})
	})
	if err != nil {
		t.Fatalf("seed typed intent against older matching task: %v", err)
	}
	if _, err := appendEvent(context.Background(), tx, proposal.WorkspaceID, "scheduling_intent", intentID, 1, schedulingIntentCreatedEvent, "raw-old-task-effect", acceptedAt, map[string]any{
		"task_id": taskID, "launch_profile_id": fixture.profile.ID, "agent_id": fixture.repairAgent.ID,
		"source_check_repair_proposal_id": proposal.ID,
	}); err != nil {
		t.Fatalf("seed raw typed intent event: %v", err)
	}
	err = fixture.authority.storage.withCheckMutationSeal(func() error {
		return queries.InsertCheckRepairEffect(context.Background(), dbgen.InsertCheckRepairEffectParams{
			ID: effectID, RepairProposalID: proposal.ID, RepairTaskID: taskID, SchedulingIntentID: intentID, CreatedAt: acceptedAt,
		})
	})
	if err == nil {
		t.Fatal("raw accepted repair effect reused an older preexisting task matching only the frozen recipe")
	}
	var effects int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM check_repair_effects WHERE id=?`, effectID).Scan(&effects); err != nil || effects != 0 {
		t.Fatalf("forged repair effect count = %d, %v; want zero", effects, err)
	}
}

func TestRawSQLCannotForgeOrMutateCheckRepairLifecycle(t *testing.T) {
	fixture := newCheckRepairAdversarialFixture(t)
	proposal := fixture.propose(t, "propose-before-raw-repair-lifecycle-attacks").Value

	for name, statement := range map[string]string{
		"change frozen rationale": `UPDATE check_repair_proposals SET rationale='profile=attacker command=/bin/sh' WHERE id=?`,
		"forge accepted state":    `UPDATE check_repair_proposals SET status='accepted',revision=2,updated_at='2026-08-14T23:59:59Z',updated_by='local-owner' WHERE id=?`,
		"delete durable proposal": `DELETE FROM check_repair_proposals WHERE id=?`,
	} {
		t.Run(name, func(t *testing.T) {
			if result, err := fixture.authority.storage.db.Exec(statement, proposal.ID); err == nil {
				t.Fatalf("raw repair mutation succeeded: %#v", result)
			}
		})
	}

	decisionID, err := randomID("checkdecision_")
	if err != nil {
		t.Fatal(err)
	}
	if result, err := fixture.authority.storage.db.Exec(`INSERT INTO check_repair_decisions(
id,repair_proposal_id,decision,proposal_revision,note,created_at,created_by
) VALUES(?,?,?,?,?,?,?)`, decisionID, proposal.ID, domain.CheckRepairAccepted, proposal.Revision, "raw forged owner decision", "2026-08-14T23:59:59Z", localOwnerActorID); err == nil {
		t.Fatalf("raw repair decision succeeded: %#v", result)
	}

	detail, err := fixture.authority.storage.CheckRepairProposal(context.Background(), fixture.authority.workspace.ID, proposal.ID)
	if err != nil {
		t.Fatalf("CheckRepairProposal(after raw attacks) error = %v", err)
	}
	if !reflect.DeepEqual(detail.Proposal, proposal) || detail.Decision != nil || detail.Effect != nil {
		t.Fatalf("raw attacks changed inert repair lifecycle: %#v; want proposal %#v", detail, proposal)
	}
}

func TestCheckRepairFaultBarriersRollbackWithoutOrphansAndReplay(t *testing.T) {
	for _, stage := range []string{
		MutationAfterCheckRepairDecision,
		MutationAfterCheckRepairTask,
		MutationAfterCheckRepairIntent,
		MutationAfterCheckRepairEffect,
		MutationAfterCheckRepairEvent,
		MutationAfterCheckRepairIdempotency,
	} {
		t.Run(stage, func(t *testing.T) {
			fixture := newCheckRepairAdversarialFixture(t)
			proposal := fixture.propose(t, "propose-repair-fault-"+stage).Value
			command := DecideCheckRepairCommand{
				WorkspaceIdentifier:   fixture.authority.workspace.ID,
				CheckRepairProposalID: proposal.ID,
				ExpectedRevision:      proposal.Revision,
				IdempotencyKey:        "accept-repair-fault-" + stage,
				CorrelationID:         "request-accept-repair-fault-" + stage,
			}
			fixture.authority.advance()
			fixture.authority.storage.mutationHook = failCheckStage(stage)
			if _, err := fixture.authority.storage.AcceptCheckRepair(context.Background(), command); !errors.Is(err, errInjectedCheckBarrier) {
				t.Fatalf("AcceptCheckRepair(%s) error = %v; want injected fault", stage, err)
			}
			fixture.authority.storage.mutationHook = nil
			assertCheckRowCount(t, fixture.authority.storage, 1, `SELECT COUNT(*) FROM check_repair_proposals WHERE id=? AND status='pending' AND revision=1`, proposal.ID)
			assertCheckRowCount(t, fixture.authority.storage, 0, `SELECT COUNT(*) FROM check_repair_decisions WHERE repair_proposal_id=?`, proposal.ID)
			assertCheckRowCount(t, fixture.authority.storage, 0, `SELECT COUNT(*) FROM check_repair_effects WHERE repair_proposal_id=?`, proposal.ID)
			assertCheckRowCount(t, fixture.authority.storage, 0, `SELECT COUNT(*) FROM scheduling_intents WHERE source_check_repair_proposal_id=?`, proposal.ID)
			assertCheckRowCount(t, fixture.authority.storage, 0, `SELECT COUNT(*) FROM tasks WHERE objective_id=? AND title=? AND id<>?`, proposal.ObjectiveID, proposal.RepairTaskTitle, proposal.TaskID)
			assertCheckRowCount(t, fixture.authority.storage, 0, `SELECT COUNT(*) FROM events WHERE entity_type='check_repair_proposal' AND entity_id=? AND type='check.repair_accepted'`, proposal.ID)
			assertCheckRowCount(t, fixture.authority.storage, 0, `SELECT COUNT(*) FROM idempotency_keys WHERE key=?`, ownerCheckIdempotencyKey(command.IdempotencyKey))

			accepted, err := fixture.authority.storage.AcceptCheckRepair(context.Background(), command)
			if err != nil || accepted.Value.Effect == nil {
				t.Fatalf("AcceptCheckRepair(%s retry) = %#v, %v", stage, accepted, err)
			}
			replayed, err := fixture.authority.storage.AcceptCheckRepair(context.Background(), command)
			if err != nil || !reflect.DeepEqual(replayed, accepted) {
				t.Fatalf("AcceptCheckRepair(%s replay) = %#v, %v; want %#v", stage, replayed, err, accepted)
			}
		})
	}
}

func TestCheckRepairProposalFaultBarriersRollbackAndReplay(t *testing.T) {
	for _, stage := range []string{
		MutationAfterCheckRepairProposalProjection,
		MutationAfterCheckRepairProposalEvent,
		MutationAfterCheckRepairProposalIdempotency,
	} {
		t.Run(stage, func(t *testing.T) {
			fixture := newCheckRepairAdversarialFixture(t)
			command := ProposeGrantedCheckRepairCommand{
				SourceRunID:    fixture.authority.sourceRun.ID,
				CheckResultID:  fixture.result.Result.ID,
				Rationale:      "bounded proposal fault injection",
				IdempotencyKey: "propose-repair-fault-" + stage,
				CorrelationID:  "request-propose-repair-fault-" + stage,
			}
			fixture.authority.storage.mutationHook = failCheckStage(stage)
			if _, err := fixture.authority.storage.ProposeGrantedCheckRepair(context.Background(), command); !errors.Is(err, errInjectedCheckBarrier) {
				t.Fatalf("ProposeGrantedCheckRepair(%s) error = %v; want injected fault", stage, err)
			}
			fixture.authority.storage.mutationHook = nil
			assertCheckRowCount(t, fixture.authority.storage, 0, `SELECT COUNT(*) FROM check_repair_proposals WHERE check_result_id=?`, fixture.result.Result.ID)
			assertCheckRowCount(t, fixture.authority.storage, 0, `SELECT COUNT(*) FROM events WHERE entity_type='check_repair_proposal' AND type='check.repair_proposed' AND json_extract(data_json,'$.check_result_id')=?`, fixture.result.Result.ID)
			assertCheckRowCount(t, fixture.authority.storage, 0, `SELECT COUNT(*) FROM idempotency_keys WHERE key=?`, runCheckIdempotencyKey(fixture.authority.sourceRun.ID, command.IdempotencyKey))

			proposed, err := fixture.authority.storage.ProposeGrantedCheckRepair(context.Background(), command)
			if err != nil || proposed.Value.Status != domain.CheckRepairPending {
				t.Fatalf("ProposeGrantedCheckRepair(%s retry) = %#v, %v", stage, proposed, err)
			}
			replayed, err := fixture.authority.storage.ProposeGrantedCheckRepair(context.Background(), command)
			if err != nil || !reflect.DeepEqual(replayed, proposed) {
				t.Fatalf("ProposeGrantedCheckRepair(%s replay) = %#v, %v; want %#v", stage, replayed, err, proposed)
			}
			assertCheckRowCount(t, fixture.authority.storage, 1, `SELECT COUNT(*) FROM check_repair_proposals WHERE id=?`, proposed.Value.ID)
			assertCheckRowCount(t, fixture.authority.storage, 1, `SELECT COUNT(*) FROM events WHERE entity_type='check_repair_proposal' AND entity_id=? AND type='check.repair_proposed'`, proposed.Value.ID)
		})
	}
}
