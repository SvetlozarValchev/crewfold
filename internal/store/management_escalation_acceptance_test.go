package store

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func TestManagerEscalationAcceptanceAndApprovalMatrix(t *testing.T) {
	for _, response := range []string{
		domain.ProposalResponseResumeRun,
		domain.ProposalResponseStopRun,
		domain.ProposalResponseRetryTask,
		domain.ProposalResponseReassignTask,
	} {
		response := response
		t.Run(response+" allow and replay", func(t *testing.T) {
			t.Parallel()
			fixture := newEscalationAcceptanceFixture(t, response+"-allow")
			target := fixture.createTarget(t, response, response+"-allow")
			_, action, approval := fixture.acceptEscalation(t, target.request, response+"-allow")

			command := DecideApprovalCommand{
				WorkspaceIdentifier: fixture.base.workspace.ID,
				ApprovalRequestID:   approval.ID,
				ExpectedRevision:    approval.Revision,
				DecisionNote:        "Allow this exact frozen manager escalation.",
				IdempotencyKey:      response + "-allow-decision",
				CorrelationID:       "request-" + response + "-allow-decision",
			}
			decided, err := fixture.storage.AllowApproval(context.Background(), command)
			if err != nil {
				t.Fatalf("AllowApproval(%s) = %v", response, err)
			}
			replayed, err := fixture.storage.AllowApproval(context.Background(), command)
			if err != nil || replayed.EventSequence != decided.EventSequence || replayed.Approval.ID != decided.Approval.ID || replayed.Action.ID != decided.Action.ID {
				t.Fatalf("AllowApproval(%s replay) = %#v, %v; want event %d for %s/%s", response, replayed, err, decided.EventSequence, approval.ID, action.ID)
			}
			if decided.Approval.Status != domain.ApprovalConsumed || decided.Approval.Revision != 3 ||
				decided.Action.Status != domain.SupervisorActionApplied || decided.Action.Revision != 2 {
				t.Fatalf("AllowApproval(%s) = %#v; want consumed approval and applied action", response, decided)
			}
			assertManagementRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM events WHERE entity_id=? AND type='approval.granted'`, approval.ID)
			assertManagementRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM events WHERE entity_id=? AND type='approval.consumed'`, approval.ID)
			assertManagementRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM events WHERE entity_id=? AND type='supervisor.action_applied'`, action.ID)
			fixture.assertAllowedTarget(t, target, action)
		})

		t.Run(response+" stale allow denied and replayed", func(t *testing.T) {
			t.Parallel()
			fixture := newEscalationAcceptanceFixture(t, response+"-stale")
			target := fixture.createTarget(t, response, response+"-stale")
			_, action, approval := fixture.acceptEscalation(t, target.request, response+"-stale")
			fixture.makeTargetStale(t, target, response+"-stale")
			staleFingerprint := fixture.targetFingerprint(t, target)

			allow := DecideApprovalCommand{
				WorkspaceIdentifier: fixture.base.workspace.ID,
				ApprovalRequestID:   approval.ID,
				ExpectedRevision:    approval.Revision,
				DecisionNote:        "Attempt only the frozen target revision.",
				IdempotencyKey:      response + "-stale-allow",
				CorrelationID:       "request-" + response + "-stale-allow",
			}
			if _, err := fixture.storage.AllowApproval(context.Background(), allow); ErrorCode(err) != CodeApprovalConflict {
				t.Fatalf("AllowApproval(%s stale) = %v, code %q; want %q", response, err, ErrorCode(err), CodeApprovalConflict)
			}
			storedApproval, err := fixture.storage.ApprovalRequest(context.Background(), fixture.base.workspace.ID, approval.ID)
			if err != nil || storedApproval.Status != domain.ApprovalPending || storedApproval.Revision != 1 {
				t.Fatalf("approval after stale %s allow = %#v, %v; want pending revision 1", response, storedApproval, err)
			}
			storedAction, err := fixture.storage.SupervisorAction(context.Background(), fixture.base.workspace.ID, action.ID)
			if err != nil || storedAction.Status != domain.SupervisorActionAwaitingApproval || storedAction.Revision != 1 {
				t.Fatalf("action after stale %s allow = %#v, %v; want awaiting approval revision 1", response, storedAction, err)
			}
			assertManagementRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM events WHERE entity_id=? AND type IN ('approval.granted','approval.consumed','supervisor.action_applied')`, approval.ID)

			deny := allow
			deny.DecisionNote = "Deny the now-stale target without applying it."
			deny.IdempotencyKey = response + "-stale-deny"
			deny.CorrelationID = "request-" + response + "-stale-deny"
			denied, err := fixture.storage.DenyApproval(context.Background(), deny)
			if err != nil {
				t.Fatalf("DenyApproval(%s stale) = %v", response, err)
			}
			replayed, err := fixture.storage.DenyApproval(context.Background(), deny)
			if err != nil || replayed.EventSequence != denied.EventSequence || replayed.Approval.ID != denied.Approval.ID || replayed.Action.ID != denied.Action.ID {
				t.Fatalf("DenyApproval(%s stale replay) = %#v, %v; want event %d", response, replayed, err, denied.EventSequence)
			}
			if denied.Approval.Status != domain.ApprovalDenied || denied.Action.Status != domain.SupervisorActionDismissed {
				t.Fatalf("DenyApproval(%s stale) = %#v; want denied/dismissed", response, denied)
			}
			if got := fixture.targetFingerprint(t, target); got != staleFingerprint {
				t.Fatalf("denying stale %s changed target: got %s, want %s", response, got, staleFingerprint)
			}
			assertManagementRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM events WHERE entity_id=? AND type='approval.denied'`, approval.ID)
			assertManagementRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM events WHERE entity_id=? AND type IN ('approval.granted','approval.consumed')`, approval.ID)
		})
	}
}

func TestApprovedRunControlsRejectUnavailableBindingsWithoutConsumingApproval(t *testing.T) {
	for _, response := range []string{domain.ProposalResponseResumeRun, domain.ProposalResponseStopRun} {
		response := response
		for _, binding := range []string{"foreign", "missing"} {
			binding := binding
			t.Run(response+" "+binding, func(t *testing.T) {
				key := strings.ReplaceAll(response+"-"+binding, "_", "-")
				fixture := newEscalationAcceptanceFixture(t, "binding-refusal-"+key)
				target := fixture.createTarget(t, response, "binding-refusal-"+key)
				_, action, approval := fixture.acceptEscalation(t, target.request, "binding-refusal-"+key)

				controlStore := fixture.storage
				if binding == "foreign" {
					foreign, err := Open(context.Background(), filepath.Dir(fixture.storage.path), Options{
						RuntimeNodeID: foreignRuntimeNodeID, RuntimeNodeFingerprint: foreignRuntimeNodeFingerprint,
					})
					if err != nil {
						t.Fatalf("Open(foreign approval store) error = %v", err)
					}
					defer foreign.Close()
					controlStore = foreign
				} else if _, err := fixture.storage.writeDB.ExecContext(context.Background(), `DELETE FROM run_runtime_bindings WHERE run_id=?`, target.runID); err != nil {
					t.Fatalf("delete approved-control runtime binding: %v", err)
				}

				command := DecideApprovalCommand{
					WorkspaceIdentifier: fixture.base.workspace.ID,
					ApprovalRequestID:   approval.ID,
					ExpectedRevision:    approval.Revision,
					DecisionNote:        "Apply only with the exact live runtime authority.",
					IdempotencyKey:      "binding-refusal-decision-" + key,
					CorrelationID:       "request-binding-refusal-decision-" + key,
				}
				before := approvedRunControlSnapshotForTest(t, controlStore, fixture.base.workspace.ID, target.runID, approval.ID, action.ID, command.IdempotencyKey)
				if _, err := controlStore.AllowApproval(context.Background(), command); ErrorCode(err) != CodeRuntimeBindingUnavailable {
					t.Fatalf("AllowApproval(%s %s binding) error = %v, code = %q; want %q", response, binding, err, ErrorCode(err), CodeRuntimeBindingUnavailable)
				}
				after := approvedRunControlSnapshotForTest(t, controlStore, fixture.base.workspace.ID, target.runID, approval.ID, action.ID, command.IdempotencyKey)
				if !reflect.DeepEqual(after, before) {
					t.Fatalf("approved %s with %s binding changed state: before=%#v after=%#v", response, binding, before, after)
				}
				if after.Approval.Status != domain.ApprovalPending || after.Action.Status != domain.SupervisorActionAwaitingApproval || after.Control.IdempotencyCount != 0 {
					t.Fatalf("approved %s refusal consumed authority or receipt: %#v", response, after)
				}

				// A foreign-node refusal leaves the exact owner free to retry the
				// same semantic approval and idempotency key successfully.
				if binding == "foreign" {
					decided, err := fixture.storage.AllowApproval(context.Background(), command)
					if err != nil || decided.Approval.Status != domain.ApprovalConsumed || decided.Action.Status != domain.SupervisorActionApplied {
						t.Fatalf("AllowApproval(%s owner retry) = %#v, %v", response, decided, err)
					}
				}
			})
		}
	}
}

type approvedRunControlSnapshot struct {
	Control  runtimeBindingControlSnapshot
	Approval domain.ApprovalRequest
	Action   domain.SupervisorAction
}

func approvedRunControlSnapshotForTest(t *testing.T, storage *Store, workspaceID, runID, approvalID, actionID, idempotencyKey string) approvedRunControlSnapshot {
	t.Helper()
	approval, err := storage.ApprovalRequest(context.Background(), workspaceID, approvalID)
	if err != nil {
		t.Fatalf("ApprovalRequest(control snapshot) error = %v", err)
	}
	action, err := storage.SupervisorAction(context.Background(), workspaceID, actionID)
	if err != nil {
		t.Fatalf("SupervisorAction(control snapshot) error = %v", err)
	}
	return approvedRunControlSnapshot{
		Control:  runtimeBindingControlSnapshotForTest(t, storage, runID, idempotencyKey),
		Approval: approval,
		Action:   action,
	}
}

func TestSupervisorRequestOwnerApprovalAllowDenyAndReplay(t *testing.T) {
	for _, allow := range []bool{true, false} {
		allow := allow
		name := "deny"
		if allow {
			name = "allow"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			storage := openTestStore(t, t.TempDir(), Options{})
			workspace, _, _, _, assigned := initializeRunTest(t, storage, "request owner "+name)
			active := startAdversarialRun(t, storage, workspace.ID, assigned, "request-owner-"+name)
			failed, err := storage.FailRun(context.Background(), active.Run.ID, "provider_failed", "definite owner-visible failure", prepareTestRunLogArchive(t, storage, active.Run.ID), "", "request-request-owner-"+name+"-failure")
			if err != nil {
				t.Fatalf("FailRun(request_owner %s) = %v", name, err)
			}
			configureAdversarialSupervisor(t, storage, workspace.ID, "request-owner-"+name+"-policy")
			scan, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
				WorkspaceIdentifier: workspace.ID,
				Limit:               100,
				IdempotencyKey:      "request-owner-" + name + "-scan",
				CorrelationID:       "request-request-owner-" + name + "-scan",
			})
			if err != nil {
				t.Fatalf("RunSupervisor(request_owner %s) = %v", name, err)
			}
			var action domain.SupervisorAction
			for _, candidate := range scan.Actions {
				if candidate.Condition == domain.SupervisorConditionFailed {
					action = candidate
				}
			}
			if action.ID == "" || action.Response != domain.SupervisorResponseRequestOwner || action.Status != domain.SupervisorActionAwaitingApproval || action.ApprovalID == "" {
				t.Fatalf("request_owner %s action = %#v; want failed/request_owner/awaiting_approval", name, action)
			}
			approval, err := storage.ApprovalRequest(context.Background(), workspace.ID, action.ApprovalID)
			if err != nil || approval.Status != domain.ApprovalPending {
				t.Fatalf("ApprovalRequest(request_owner %s) = %#v, %v", name, approval, err)
			}
			before := runFingerprint(t, storage, failed.Run.ID)
			command := DecideApprovalCommand{
				WorkspaceIdentifier: workspace.ID,
				ApprovalRequestID:   approval.ID,
				ExpectedRevision:    approval.Revision,
				DecisionNote:        "Acknowledge this exact owner request.",
				IdempotencyKey:      "request-owner-" + name + "-decision",
				CorrelationID:       "request-request-owner-" + name + "-decision",
			}
			var decided, replayed ApprovalMutationResult
			if allow {
				decided, err = storage.AllowApproval(context.Background(), command)
				if err == nil {
					replayed, err = storage.AllowApproval(context.Background(), command)
				}
			} else {
				decided, err = storage.DenyApproval(context.Background(), command)
				if err == nil {
					replayed, err = storage.DenyApproval(context.Background(), command)
				}
			}
			if err != nil || replayed.EventSequence != decided.EventSequence || replayed.Action.ID != action.ID {
				t.Fatalf("request_owner %s decision/replay = %#v / %#v, %v", name, decided, replayed, err)
			}
			if allow && (decided.Approval.Status != domain.ApprovalConsumed || decided.Action.Status != domain.SupervisorActionApplied) {
				t.Fatalf("allowed request_owner = %#v; want consumed/applied", decided)
			}
			if !allow && (decided.Approval.Status != domain.ApprovalDenied || decided.Action.Status != domain.SupervisorActionDismissed) {
				t.Fatalf("denied request_owner = %#v; want denied/dismissed", decided)
			}
			if after := runFingerprint(t, storage, failed.Run.ID); after != before {
				t.Fatalf("request_owner %s mutated governed run: got %s, want %s", name, after, before)
			}
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM supervisor_actions WHERE id=? AND response='request_owner'`, action.ID)
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM approval_requests WHERE id=? AND action_id=?`, approval.ID, action.ID)
		})
	}
}

func TestManagerEscalationMaterializesApprovalWhileSupervisorPolicyDisabled(t *testing.T) {
	t.Parallel()
	fixture := newEscalationAcceptanceFixture(t, "disabled-policy")
	policy, err := fixture.storage.SupervisorPolicy(context.Background(), fixture.base.workspace.ID)
	if err != nil || policy.Enabled {
		t.Fatalf("SupervisorPolicy(disabled escalation fixture) = %#v, %v; want disabled", policy, err)
	}
	target := fixture.createTarget(t, domain.ProposalResponseReassignTask, "disabled-policy")
	accepted, action, approval := fixture.acceptEscalation(t, target.request, "disabled-policy")
	if len(accepted.Effects) != 2 || action.PolicyRevision != policy.Revision ||
		action.Status != domain.SupervisorActionAwaitingApproval || approval.Status != domain.ApprovalPending {
		t.Fatalf("disabled-policy escalation = effects %#v, action %#v, approval %#v", accepted.Effects, action, approval)
	}
	assertManagementRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM scheduling_intents WHERE task_id=?`, target.taskID)
}

func TestAcceptedSchedulingIntentTerminalizesWithDefinitiveRunOutcome(t *testing.T) {
	for _, outcome := range []string{domain.RunCompleted, domain.RunFailed, domain.RunStopped} {
		outcome := outcome
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()
			key := "intent-terminal-" + strings.ReplaceAll(outcome, "_", "-")
			storage, fixture, _, intentID := acceptedSingleSchedulingIntent(t, key)
			scheduled, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
				WorkspaceIdentifier: fixture.workspace.ID,
				Limit:               100,
				IdempotencyKey:      key + "-schedule",
				CorrelationID:       "request-" + key + "-schedule",
			})
			if err != nil || len(scheduled.ScheduledRunIDs) != 1 {
				t.Fatalf("RunSupervisor(%s) = %#v, %v; want one scheduled run", outcome, scheduled, err)
			}
			runID := scheduled.ScheduledRunIDs[0]
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE id=? AND run_id=? AND status='run_requested'`, intentID, runID)
			if _, err := storage.MarkRunStarting(context.Background(), runID, "request-"+key+"-starting"); err != nil {
				t.Fatalf("MarkRunStarting(%s) = %v", outcome, err)
			}
			active, err := storage.MarkRunStarted(context.Background(), runID, key+"-runtime", key+"-provider", "request-"+key+"-started")
			if err != nil {
				t.Fatalf("MarkRunStarted(%s) = %v", outcome, err)
			}

			wantIntentStatus, wantEvent := "", ""
			switch outcome {
			case domain.RunCompleted:
				wantIntentStatus, wantEvent = domain.SchedulingIntentSatisfied, schedulingIntentSatisfiedEvent
				if _, err := storage.ApplyRunObservation(context.Background(), runID, domain.RunObservation{
					Kind: domain.ObservationCompletion, Message: "accepted scheduled work is complete", Handoff: "inspect the exact completed work", LogArchive: prepareTestRunLogArchive(t, storage, runID),
				}, true, nil, "request-"+key+"-completed"); err != nil {
					t.Fatalf("ApplyRunObservation(completed) = %v", err)
				}
			case domain.RunFailed:
				wantIntentStatus, wantEvent = domain.SchedulingIntentFailed, schedulingIntentFailedEvent
				if _, err := storage.FailRun(context.Background(), runID, "provider_failed", "definite scheduled runtime failure", prepareTestRunLogArchive(t, storage, runID), "", "request-"+key+"-failed"); err != nil {
					t.Fatalf("FailRun(scheduled) = %v", err)
				}
			case domain.RunStopped:
				wantIntentStatus, wantEvent = domain.SchedulingIntentCancelled, schedulingIntentCancelledEvent
				stopping, err := storage.RequestRunStop(context.Background(), StopRunCommand{
					WorkspaceIdentifier: fixture.workspace.ID,
					RunID:               runID,
					ExpectedRevision:    active.Run.Revision,
					GracePeriodMillis:   1000,
					IdempotencyKey:      key + "-stop",
					CorrelationID:       "request-" + key + "-stop",
				})
				if err != nil {
					t.Fatalf("RequestRunStop(scheduled) = %v", err)
				}
				if _, err := storage.MarkRunStopped(context.Background(), stopping.Detail.Run.ID, false, "scheduled run stopped", prepareTestRunLogArchive(t, storage, stopping.Detail.Run.ID), "", "request-"+key+"-stopped"); err != nil {
					t.Fatalf("MarkRunStopped(scheduled) = %v", err)
				}
			}
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE id=? AND run_id=? AND status=? AND revision>2`, intentID, runID, wantIntentStatus)
			assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM events WHERE entity_id=? AND type=?`, intentID, wantEvent)
		})
	}
}

func TestAcceptedSchedulingIntentClosesOnRejectedCompletionAndDisabledStartFailure(t *testing.T) {
	t.Run("rejected completion", func(t *testing.T) {
		t.Parallel()
		const key = "intent-rejected-completion"
		storage, fixture, _, intentID := acceptedSingleSchedulingIntent(t, key)
		runID := scheduleSingleAcceptedIntent(t, storage, fixture.workspace.ID, intentID, key)
		if _, err := storage.MarkRunStarting(context.Background(), runID, "request-"+key+"-starting"); err != nil {
			t.Fatalf("MarkRunStarting(rejected completion) = %v", err)
		}
		if _, err := storage.MarkRunStarted(context.Background(), runID, key+"-runtime", key+"-provider", "request-"+key+"-started"); err != nil {
			t.Fatalf("MarkRunStarted(rejected completion) = %v", err)
		}
		rejected, err := storage.ApplyRunObservation(context.Background(), runID, domain.RunObservation{
			Kind: domain.ObservationCompletion, Message: "completion lacks required owner evidence", Evidence: []string{"partial-check"}, LogArchive: prepareTestRunLogArchive(t, storage, runID),
		}, false, []string{"required-check"}, "request-"+key+"-rejected")
		if err != nil {
			t.Fatalf("ApplyRunObservation(rejected completion) = %v", err)
		}
		if rejected.Run.Status != domain.RunReview || rejected.Task.Status != domain.TaskChangesRequested {
			t.Fatalf("rejected completion = run %q task %q; want review/changes_requested", rejected.Run.Status, rejected.Task.Status)
		}
		assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE id=? AND run_id=? AND status='failed' AND revision>2`, intentID, runID)
		assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM events WHERE entity_id=? AND type=? AND json_extract(data_json,'$.run_status')='review'`, intentID, schedulingIntentFailedEvent)
	})

	t.Run("start failed while policy disabled", func(t *testing.T) {
		t.Parallel()
		const key = "intent-disabled-start-failure"
		storage, fixture, _, intentID := acceptedSingleSchedulingIntent(t, key)
		runID := scheduleSingleAcceptedIntent(t, storage, fixture.workspace.ID, intentID, key)
		policy, err := storage.SupervisorPolicy(context.Background(), fixture.workspace.ID)
		if err != nil || !policy.Enabled {
			t.Fatalf("SupervisorPolicy(before disable) = %#v, %v; want enabled", policy, err)
		}
		disabled, err := storage.ConfigureSupervisorPolicy(context.Background(), ConfigureSupervisorPolicyCommand{
			WorkspaceIdentifier:  fixture.workspace.ID,
			Enabled:              false,
			AutoSchedule:         false,
			Limits:               policy.Limits,
			AutoRetryLimit:       0,
			RetryCooldownSeconds: 0,
			ExpectedRevision:     policy.Revision,
			IdempotencyKey:       key + "-disable-policy",
			CorrelationID:        "request-" + key + "-disable-policy",
		})
		if err != nil || disabled.Value.Enabled {
			t.Fatalf("ConfigureSupervisorPolicy(disable before start failure) = %#v, %v", disabled.Value, err)
		}
		if _, err := storage.MarkRunStarting(context.Background(), runID, "request-"+key+"-starting"); err != nil {
			t.Fatalf("MarkRunStarting(disabled start failure) = %v", err)
		}
		failed, err := storage.FailRunStart(context.Background(), runID, "definite start failure under disabled policy", "request-"+key+"-failed")
		if err != nil {
			t.Fatalf("FailRunStart(disabled policy) = %v", err)
		}
		if failed.Run.Status != domain.RunStartFailed || failed.Task.Status != domain.TaskAssigned {
			t.Fatalf("disabled-policy start failure = run %q task %q; want start_failed/assigned", failed.Run.Status, failed.Task.Status)
		}
		assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE id=? AND run_id=? AND status='failed' AND revision>2`, intentID, runID)
		assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM events WHERE entity_id=? AND type=? AND json_extract(data_json,'$.run_status')='start_failed'`, intentID, schedulingIntentFailedEvent)
	})
}

func scheduleSingleAcceptedIntent(t *testing.T, storage *Store, workspaceID, intentID, key string) string {
	t.Helper()
	scheduled, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: workspaceID,
		Limit:               100,
		IdempotencyKey:      key + "-schedule",
		CorrelationID:       "request-" + key + "-schedule",
	})
	if err != nil || len(scheduled.ScheduledRunIDs) != 1 {
		t.Fatalf("RunSupervisor(%s) = %#v, %v; want one scheduled run", key, scheduled, err)
	}
	runID := scheduled.ScheduledRunIDs[0]
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE id=? AND run_id=? AND status='run_requested'`, intentID, runID)
	return runID
}

type escalationAcceptanceFixture struct {
	storage        *Store
	base           managerGrantAdversarialFixture
	grant          domain.ManagerGrant
	managerRunID   string
	packetSequence int64
}

type escalationAcceptanceTarget struct {
	request domain.ProposalRequestAction
	taskID  string
	runID   string
}

func newEscalationAcceptanceFixture(t *testing.T, key string) escalationAcceptanceFixture {
	t.Helper()
	storage, base := createManagerGrantAdversarialFixtureWithOptions(t, managerGrantAdversarialFixtureOptions{
		TargetMaxConcurrency: 4,
		SharedTargetCheckout: true,
	})
	ctx := context.Background()
	grantResult, err := storage.CreateManagerGrant(ctx, CreateManagerGrantCommand{
		WorkspaceIdentifier:   base.workspace.ID,
		ProjectIdentifier:     base.project.ID,
		ObjectiveID:           base.objective.ID,
		TaskID:                base.planning.Task.ID,
		AgentIdentifier:       base.manager.ID,
		ExpectedTaskRevision:  base.planning.Task.Revision,
		ExpectedAgentRevision: base.manager.Revision,
		ProposalKinds:         []string{domain.ManagerProposalEscalation, domain.ManagerProposalTaskDecomposition},
		LaunchProfileIDs:      []string{base.target.ID},
		AllowedClaimKinds:     []string{domain.ClaimKindComponent},
		Limits: domain.ManagerProposalLimits{
			MaxOpenProposals: 32, MaxActions: 16, MaxTasks: 16, MaxDependencies: 16, MaxClaimRequirements: 16,
			Budget: domain.Budget{TokenLimit: 1000, CostCents: 100, TimeSeconds: 600},
		},
		IdempotencyKey: "escalation-acceptance-grant-" + key,
		CorrelationID:  "request-escalation-acceptance-grant-" + key,
	})
	if err != nil {
		t.Fatalf("CreateManagerGrant(escalation %s) = %v", key, err)
	}
	planningProfile, err := storage.CreateLaunchProfile(ctx, CreateLaunchProfileCommand{
		WorkspaceIdentifier:   base.workspace.ID,
		ProjectIdentifier:     base.project.ID,
		AgentIdentifier:       base.manager.ID,
		ExpectedAgentRevision: base.manager.Revision,
		Purpose:               "ornamental planning metadata with no authority semantics",
		Runtime:               base.manager.Runtime,
		Provider:              base.manager.Provider,
		Scenario: domain.FakeScenario{
			Schema: execution.FakeScenarioSchema,
			Name:   "escalation-acceptance-" + strings.ReplaceAll(key, "_", "-"),
			Steps:  []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "submit one typed escalation"}},
		},
		AssignmentLeaseSeconds: 900,
		CapabilityTTLSeconds:   900,
		ManagerGrantID:         grantResult.Value.ID,
		IdempotencyKey:         "escalation-acceptance-profile-" + key,
		CorrelationID:          "request-escalation-acceptance-profile-" + key,
	})
	if err != nil {
		t.Fatalf("CreateLaunchProfile(escalation %s) = %v", key, err)
	}
	invoked, err := storage.InvokeManager(ctx, InvokeManagerCommand{
		WorkspaceIdentifier:     base.workspace.ID,
		ObjectiveID:             base.objective.ID,
		TaskID:                  base.planning.Task.ID,
		ManagerGrantID:          grantResult.Value.ID,
		LaunchProfileID:         planningProfile.Value.ID,
		ExpectedTaskRevision:    base.planning.Task.Revision,
		ExpectedGrantRevision:   grantResult.Value.Revision,
		ExpectedProfileRevision: planningProfile.Value.Revision,
		IdempotencyKey:          "escalation-acceptance-invoke-" + key,
		CorrelationID:           "request-escalation-acceptance-invoke-" + key,
	})
	if err != nil {
		t.Fatalf("InvokeManager(escalation %s) = %v", key, err)
	}
	if _, err := storage.MarkRunStarting(ctx, invoked.Detail.Run.ID, "request-escalation-acceptance-starting-"+key); err != nil {
		t.Fatalf("MarkRunStarting(escalation %s) = %v", key, err)
	}
	var packetSequence int64
	if err := storage.db.QueryRow(`
SELECT json_extract(packet.packet_json,'$.as_of_event_sequence')
FROM run_context_bindings binding
JOIN context_packets packet ON packet.id=binding.context_packet_id
WHERE binding.run_id=?`, invoked.Detail.Run.ID).Scan(&packetSequence); err != nil {
		t.Fatalf("read escalation packet cursor %s = %v", key, err)
	}
	if base.manager.Role != "constellation cartographer" || planningProfile.Value.Purpose == base.manager.Role {
		t.Fatalf("escalation fixture accidentally derives authority from role/purpose metadata: role=%q purpose=%q", base.manager.Role, planningProfile.Value.Purpose)
	}
	return escalationAcceptanceFixture{
		storage: storage, base: base, grant: grantResult.Value,
		managerRunID: invoked.Detail.Run.ID, packetSequence: packetSequence,
	}
}

func (fixture escalationAcceptanceFixture) createTarget(t *testing.T, response, key string) escalationAcceptanceTarget {
	t.Helper()
	switch response {
	case domain.ProposalResponseResumeRun, domain.ProposalResponseStopRun:
		created, err := fixture.storage.CreateTask(context.Background(), CreateTaskCommand{
			WorkspaceIdentifier: fixture.base.workspace.ID,
			ProjectIdentifier:   fixture.base.project.ID,
			ObjectiveID:         fixture.base.objective.ID,
			Title:               "Escalation target " + response,
			Budget:              domain.Budget{TokenLimit: 10, CostCents: 1, TimeSeconds: 10},
			IdempotencyKey:      "escalation-target-task-" + key,
			CorrelationID:       "request-escalation-target-task-" + key,
		})
		if err != nil {
			t.Fatalf("CreateTask(%s target) = %v", response, err)
		}
		assigned, err := fixture.storage.AssignTask(context.Background(), AssignTaskCommand{
			WorkspaceIdentifier: fixture.base.workspace.ID,
			TaskID:              created.Detail.Task.ID,
			AgentIdentifier:     fixture.base.target.AgentID,
			LeaseSeconds:        900,
			ExpectedRevision:    created.Detail.Task.Revision,
			IdempotencyKey:      "escalation-target-assign-" + key,
			CorrelationID:       "request-escalation-target-assign-" + key,
		})
		if err != nil {
			t.Fatalf("AssignTask(%s target) = %v", response, err)
		}
		scenario := managementProgressScenario("escalation-target-" + strings.ReplaceAll(key, "_", "-"))
		if response == domain.ProposalResponseResumeRun {
			scenario.Steps = []domain.FakeStep{{Kind: domain.ObservationBlocked, Message: "exact owner input required"}}
		}
		run := createRunTest(t, fixture.storage, fixture.base.workspace.ID, assigned.Detail, scenario, "escalation-target-run-"+key)
		if _, err := fixture.storage.MarkRunStarting(context.Background(), run.Run.ID, "request-escalation-target-starting-"+key); err != nil {
			t.Fatalf("MarkRunStarting(%s target) = %v", response, err)
		}
		active, err := fixture.storage.MarkRunStarted(context.Background(), run.Run.ID, "fake-runtime-"+key, "fake-provider-"+key, "request-escalation-target-started-"+key)
		if err != nil {
			t.Fatalf("MarkRunStarted(%s target) = %v", response, err)
		}
		current := active
		if response == domain.ProposalResponseResumeRun {
			current, err = fixture.storage.ApplyRunObservation(context.Background(), active.Run.ID, domain.RunObservation{
				Kind: domain.ObservationBlocked, Message: "exact owner input required",
			}, true, nil, "request-escalation-target-blocked-"+key)
			if err != nil {
				t.Fatalf("ApplyRunObservation(%s target) = %v", response, err)
			}
		}
		return escalationAcceptanceTarget{
			taskID: current.Task.ID,
			runID:  current.Run.ID,
			request: domain.ProposalRequestAction{
				Response: response, TargetRunID: current.Run.ID,
				Reason: "Apply only to this exact run revision.", ExpectedRevision: current.Run.Revision,
			},
		}
	case domain.ProposalResponseRetryTask:
		taskID, runID, revision := fixture.createDefiniteStartFailure(t, key)
		return escalationAcceptanceTarget{
			taskID: taskID,
			runID:  runID,
			request: domain.ProposalRequestAction{
				Response: response, TargetTaskID: taskID,
				Reason: "Retry the exact retained assignment after definite start failure.", ExpectedRevision: revision,
			},
		}
	case domain.ProposalResponseReassignTask:
		created, err := fixture.storage.CreateTask(context.Background(), CreateTaskCommand{
			WorkspaceIdentifier: fixture.base.workspace.ID,
			ProjectIdentifier:   fixture.base.project.ID,
			ObjectiveID:         fixture.base.objective.ID,
			Title:               "Exact ready reassignment target",
			Budget:              domain.Budget{TokenLimit: 10, CostCents: 1, TimeSeconds: 10},
			IdempotencyKey:      "escalation-reassign-target-" + key,
			CorrelationID:       "request-escalation-reassign-target-" + key,
		})
		if err != nil {
			t.Fatalf("CreateTask(reassign target) = %v", err)
		}
		return escalationAcceptanceTarget{
			taskID: created.Detail.Task.ID,
			request: domain.ProposalRequestAction{
				Response: response, TargetTaskID: created.Detail.Task.ID, LaunchProfileID: fixture.base.target.ID,
				Reason: "Reassign only the exact ready task revision.", ExpectedRevision: created.Detail.Task.Revision,
			},
		}
	default:
		t.Fatalf("unsupported escalation response %q", response)
		return escalationAcceptanceTarget{}
	}
}

func (fixture escalationAcceptanceFixture) createDefiniteStartFailure(t *testing.T, key string) (string, string, int64) {
	t.Helper()
	ctx := context.Background()
	submitted, err := fixture.storage.SubmitManagerProposal(ctx, SubmitManagerProposalCommand{
		RunID:                 fixture.managerRunID,
		ManagerGrantID:        fixture.grant.ID,
		ExpectedGrantRevision: fixture.grant.Revision,
		Kind:                  domain.ManagerProposalTaskDecomposition,
		Summary:               "Create one exact retry acceptance target.",
		AsOfEventSequence:     fixture.packetSequence,
		Actions: []domain.ManagerProposalAction{{
			Type: domain.ProposalActionCreateTask,
			CreateTask: &domain.ProposalCreateTaskAction{
				TaskKey: "retry-target", LaunchProfileID: fixture.base.target.ID, Title: "Retry acceptance target", Priority: 10,
				Budget: domain.Budget{TokenLimit: 10, CostCents: 1, TimeSeconds: 10},
			},
		}},
		IdempotencyKey: "escalation-retry-create-" + key,
		CorrelationID:  "request-escalation-retry-create-" + key,
	})
	if err != nil || submitted.Proposal.Status != domain.ManagerProposalPending {
		t.Fatalf("SubmitManagerProposal(retry target) = %#v, %v", submitted.Proposal, err)
	}
	accepted, err := fixture.storage.AcceptManagerProposal(ctx, AcceptManagerProposalCommand{
		WorkspaceIdentifier: fixture.base.workspace.ID,
		ManagerProposalID:   submitted.Proposal.ID,
		ExpectedRevision:    submitted.Proposal.Revision,
		DecisionNote:        "Create the exact retry acceptance target.",
		IdempotencyKey:      "escalation-retry-create-accept-" + key,
		CorrelationID:       "request-escalation-retry-create-accept-" + key,
	})
	if err != nil || accepted.Proposal.Status != domain.ManagerProposalAccepted {
		t.Fatalf("AcceptManagerProposal(retry target) = %#v, %v", accepted.Proposal, err)
	}
	var taskID string
	for _, effect := range accepted.Effects {
		if effect.EntityType == "task" {
			taskID = effect.EntityID
		}
	}
	if taskID == "" {
		t.Fatalf("retry target acceptance effects = %#v; want created task", accepted.Effects)
	}
	if _, err := fixture.storage.ConfigureSupervisorPolicy(ctx, ConfigureSupervisorPolicyCommand{
		WorkspaceIdentifier: fixture.base.workspace.ID,
		Enabled:             true,
		AutoSchedule:        true,
		Limits: domain.SupervisorLimits{
			MaxActiveRuns: 8, MaxStartingRuns: 4, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8,
		},
		AutoRetryLimit:       0,
		RetryCooldownSeconds: 0,
		ExpectedRevision:     1,
		IdempotencyKey:       "escalation-retry-policy-" + key,
		CorrelationID:        "request-escalation-retry-policy-" + key,
	}); err != nil {
		t.Fatalf("ConfigureSupervisorPolicy(retry target) = %v", err)
	}
	scheduled, err := fixture.storage.RunSupervisor(ctx, RunSupervisorCommand{
		WorkspaceIdentifier: fixture.base.workspace.ID,
		Limit:               100,
		IdempotencyKey:      "escalation-retry-schedule-" + key,
		CorrelationID:       "request-escalation-retry-schedule-" + key,
	})
	if err != nil || len(scheduled.ScheduledRunIDs) != 1 {
		t.Fatalf("RunSupervisor(retry target) = %#v, %v; want one run", scheduled, err)
	}
	runID := scheduled.ScheduledRunIDs[0]
	if _, err := fixture.storage.MarkRunStarting(ctx, runID, "request-escalation-retry-starting-"+key); err != nil {
		t.Fatalf("MarkRunStarting(retry target) = %v", err)
	}
	failed, err := fixture.storage.FailRunStart(ctx, runID, "definite retry acceptance start failure", "request-escalation-retry-failed-"+key)
	if err != nil || failed.Run.Status != domain.RunStartFailed || failed.Task.Status != domain.TaskAssigned {
		t.Fatalf("FailRunStart(retry target) = %#v, %v", failed, err)
	}
	return taskID, runID, failed.Task.Revision
}

func (fixture escalationAcceptanceFixture) acceptEscalation(t *testing.T, request domain.ProposalRequestAction, key string) (ManagerProposalMutationResult, domain.SupervisorAction, domain.ApprovalRequest) {
	t.Helper()
	ctx := context.Background()
	submitted, err := fixture.storage.SubmitManagerProposal(ctx, SubmitManagerProposalCommand{
		RunID:                 fixture.managerRunID,
		ManagerGrantID:        fixture.grant.ID,
		ExpectedGrantRevision: fixture.grant.Revision,
		Kind:                  domain.ManagerProposalEscalation,
		Summary:               "Escalate one exact typed owner action.",
		AsOfEventSequence:     fixture.packetSequence,
		Actions: []domain.ManagerProposalAction{{
			Type: domain.ProposalActionRequestAction, RequestAction: &request,
		}},
		IdempotencyKey: "manager-escalation-submit-" + key,
		CorrelationID:  "request-manager-escalation-submit-" + key,
	})
	if err != nil || submitted.Proposal.Status != domain.ManagerProposalPending || len(submitted.Effects) != 0 ||
		len(submitted.Proposal.Actions) != 1 || submitted.Proposal.Actions[0].Type != domain.ProposalActionRequestAction ||
		submitted.Proposal.Actions[0].RequestAction == nil || submitted.Proposal.Actions[0].Ordinal != 0 {
		t.Fatalf("SubmitManagerProposal(escalation %s) = %#v, %v; want one pending typed action and no effects", key, submitted, err)
	}
	command := AcceptManagerProposalCommand{
		WorkspaceIdentifier: fixture.base.workspace.ID,
		ManagerProposalID:   submitted.Proposal.ID,
		ExpectedRevision:    submitted.Proposal.Revision,
		DecisionNote:        "Accept only this inert escalation for a separate owner decision.",
		IdempotencyKey:      "manager-escalation-accept-" + key,
		CorrelationID:       "request-manager-escalation-accept-" + key,
	}
	accepted, err := fixture.storage.AcceptManagerProposal(ctx, command)
	if err != nil || accepted.Proposal.Status != domain.ManagerProposalAccepted || len(accepted.Effects) != 2 {
		t.Fatalf("AcceptManagerProposal(escalation %s) = %#v, %v; want accepted with two effects", key, accepted, err)
	}
	replayed, err := fixture.storage.AcceptManagerProposal(ctx, command)
	if err != nil || replayed.EventSequence != accepted.EventSequence || replayed.Proposal.ID != accepted.Proposal.ID || len(replayed.Effects) != 2 {
		t.Fatalf("AcceptManagerProposal(escalation %s replay) = %#v, %v; want event %d and two original effects", key, replayed, err, accepted.EventSequence)
	}

	effectIDs := make(map[string]string, 2)
	for _, effect := range accepted.Effects {
		if effect.ActionID != submitted.Proposal.Actions[0].ID || effect.ProposalID != submitted.Proposal.ID || effect.EffectType != "created" {
			t.Fatalf("escalation %s effect = %#v; want created effect linked to exact proposal/action", key, effect)
		}
		effectIDs[effect.EntityType] = effect.EntityID
	}
	if effectIDs["supervisor_action"] == "" || effectIDs["approval_request"] == "" || len(effectIDs) != 2 {
		t.Fatalf("escalation %s effects = %#v; want one supervisor_action and one approval_request", key, accepted.Effects)
	}
	for _, effect := range replayed.Effects {
		if effectIDs[effect.EntityType] != effect.EntityID {
			t.Fatalf("escalation %s replay effect = %#v; want original IDs %#v", key, effect, effectIDs)
		}
	}
	action, err := fixture.storage.SupervisorAction(ctx, fixture.base.workspace.ID, effectIDs["supervisor_action"])
	if err != nil {
		t.Fatalf("SupervisorAction(escalation %s) = %v", key, err)
	}
	approval, err := fixture.storage.ApprovalRequest(ctx, fixture.base.workspace.ID, effectIDs["approval_request"])
	if err != nil {
		t.Fatalf("ApprovalRequest(escalation %s) = %v", key, err)
	}
	if action.Condition != domain.SupervisorConditionManagerEscalation || action.Response != request.Response ||
		action.Status != domain.SupervisorActionAwaitingApproval || action.SourceProposalID != submitted.Proposal.ID ||
		action.SourceActionID != submitted.Proposal.Actions[0].ID || action.ApprovalID != approval.ID ||
		approval.Status != domain.ApprovalPending || approval.ActionID != action.ID || approval.ExpectedActionRevision != action.Revision {
		t.Fatalf("accepted escalation %s action/approval = %#v / %#v; source proposal=%s action=%s", key, action, approval, submitted.Proposal.ID, submitted.Proposal.Actions[0].ID)
	}
	assertManagementRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM manager_proposal_actions WHERE proposal_id=? AND type='request_action'`, submitted.Proposal.ID)
	assertManagementRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM supervisor_actions WHERE source_proposal_id=? AND source_action_id=? AND condition='manager_escalation'`, submitted.Proposal.ID, submitted.Proposal.Actions[0].ID)
	assertManagementRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM approval_requests WHERE id=? AND action_id=?`, approval.ID, action.ID)
	assertManagementRowCount(t, fixture.storage, 2, `SELECT COUNT(*) FROM manager_proposal_effects WHERE proposal_id=? AND action_id=?`, submitted.Proposal.ID, submitted.Proposal.Actions[0].ID)
	return accepted, action, approval
}

func (fixture escalationAcceptanceFixture) assertAllowedTarget(t *testing.T, target escalationAcceptanceTarget, action domain.SupervisorAction) {
	t.Helper()
	switch target.request.Response {
	case domain.ProposalResponseResumeRun:
		detail, err := fixture.storage.RunDetail(context.Background(), fixture.base.workspace.ID, target.runID)
		if err != nil || detail.Run.Status != domain.RunActive || detail.Run.Revision != target.request.ExpectedRevision+1 || detail.Task.Status != domain.TaskActive {
			t.Fatalf("allowed resume target = %#v, %v", detail, err)
		}
		assertManagementRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM events WHERE entity_id=? AND type='run.resumed' AND json_extract(data_json,'$.supervisor_action_id')=?`, target.runID, action.ID)
	case domain.ProposalResponseStopRun:
		detail, err := fixture.storage.RunDetail(context.Background(), fixture.base.workspace.ID, target.runID)
		if err != nil || detail.Run.Status != domain.RunStopping || detail.Run.Revision != target.request.ExpectedRevision+1 || detail.Run.StopGraceMillis != 30000 {
			t.Fatalf("allowed stop target = %#v, %v", detail, err)
		}
		assertManagementRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM events WHERE entity_id=? AND type='run.stop_requested' AND json_extract(data_json,'$.supervisor_action_id')=?`, target.runID, action.ID)
	case domain.ProposalResponseRetryTask, domain.ProposalResponseReassignTask:
		detail, err := fixture.storage.TaskDetail(context.Background(), fixture.base.workspace.ID, target.taskID)
		if err != nil || detail.Task.Status != domain.TaskReady || detail.Task.AssignmentID != "" || detail.Task.AssignedAgentID != "" || detail.Task.Revision != target.request.ExpectedRevision+1 {
			t.Fatalf("allowed %s target = %#v, %v", target.request.Response, detail, err)
		}
		assertManagementRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM scheduling_intents WHERE task_id=? AND source_proposal_id=? AND source_action_id=? AND launch_profile_id=? AND status='pending'`, target.taskID, action.SourceProposalID, action.SourceActionID, fixture.base.target.ID)
		assertManagementRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM events WHERE entity_id=? AND type='task.readied' AND json_extract(data_json,'$.supervisor_action_id')=? AND json_extract(data_json,'$.status')='ready'`, target.taskID, action.ID)
		if target.request.Response == domain.ProposalResponseRetryTask {
			assertManagementRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM runs WHERE id=? AND status='start_failed'`, target.runID)
		}
	}
}

func (fixture escalationAcceptanceFixture) makeTargetStale(t *testing.T, target escalationAcceptanceTarget, key string) {
	t.Helper()
	switch target.request.Response {
	case domain.ProposalResponseResumeRun:
		if _, err := fixture.storage.ResumeRun(context.Background(), ResumeRunCommand{
			WorkspaceIdentifier: fixture.base.workspace.ID,
			RunID:               target.runID,
			ExpectedRevision:    target.request.ExpectedRevision,
			IdempotencyKey:      "stale-owner-resume-" + key,
			CorrelationID:       "request-stale-owner-resume-" + key,
		}); err != nil {
			t.Fatalf("ResumeRun(make manager target stale) = %v", err)
		}
	case domain.ProposalResponseStopRun:
		if _, err := fixture.storage.RequestRunStop(context.Background(), StopRunCommand{
			WorkspaceIdentifier: fixture.base.workspace.ID,
			RunID:               target.runID,
			ExpectedRevision:    target.request.ExpectedRevision,
			GracePeriodMillis:   1000,
			IdempotencyKey:      "stale-owner-stop-" + key,
			CorrelationID:       "request-stale-owner-stop-" + key,
		}); err != nil {
			t.Fatalf("RequestRunStop(make manager target stale) = %v", err)
		}
	case domain.ProposalResponseRetryTask, domain.ProposalResponseReassignTask:
		priority := 101
		if _, err := fixture.storage.UpdateTask(context.Background(), UpdateTaskCommand{
			WorkspaceIdentifier: fixture.base.workspace.ID,
			TaskID:              target.taskID,
			Priority:            &priority,
			ExpectedRevision:    target.request.ExpectedRevision,
			IdempotencyKey:      "stale-owner-task-update-" + key,
			CorrelationID:       "request-stale-owner-task-update-" + key,
		}); err != nil {
			t.Fatalf("UpdateTask(make %s target stale) = %v", target.request.Response, err)
		}
	}
}

func (fixture escalationAcceptanceFixture) targetFingerprint(t *testing.T, target escalationAcceptanceTarget) string {
	t.Helper()
	if target.runID != "" && target.request.Response != domain.ProposalResponseRetryTask {
		return runFingerprint(t, fixture.storage, target.runID)
	}
	detail, err := fixture.storage.TaskDetail(context.Background(), fixture.base.workspace.ID, target.taskID)
	if err != nil {
		t.Fatalf("fingerprint escalation task %s = %v", target.taskID, err)
	}
	var openIntents int
	if err := fixture.storage.db.QueryRow(`SELECT COUNT(*) FROM scheduling_intents WHERE task_id=? AND status IN ('pending','deferred','awaiting_approval','run_requested')`, target.taskID).Scan(&openIntents); err != nil {
		t.Fatalf("fingerprint escalation task intents %s = %v", target.taskID, err)
	}
	return fmt.Sprintf("%s/%d/%s/%s/%d", detail.Task.Status, detail.Task.Revision, detail.Task.AssignmentID, detail.Task.AssignedAgentID, openIntents)
}

func runFingerprint(t *testing.T, storage *Store, runID string) string {
	t.Helper()
	var status, jobStatus string
	var revision int64
	if err := storage.db.QueryRow(`SELECT run.status,run.revision,job.status FROM runs run JOIN run_jobs job ON job.run_id=run.id WHERE run.id=?`, runID).Scan(&status, &revision, &jobStatus); err != nil {
		t.Fatalf("fingerprint run %s = %v", runID, err)
	}
	return fmt.Sprintf("%s/%d/%s", status, revision, jobStatus)
}

func TestManagerProposalFullCanonicalContentBoundary(t *testing.T) {
	t.Parallel()
	fixture := newEscalationAcceptanceFixture(t, "canonical-boundary")
	const actionCount = 12
	const placeholderActionID = "mpact_00000000000000000000000000000000"

	actions := make([]domain.ManagerProposalAction, actionCount)
	for index := range actions {
		actions[index] = domain.ManagerProposalAction{
			ID: placeholderActionID, Ordinal: index, Type: domain.ProposalActionCreateTask,
			CreateTask: &domain.ProposalCreateTaskAction{
				TaskKey: fmt.Sprintf("boundary-%02d", index), LaunchProfileID: fixture.base.target.ID,
				Title: fmt.Sprintf("Canonical boundary task %02d", index), Description: "x", Priority: index,
				Budget: domain.Budget{TokenLimit: 1, CostCents: 1, TimeSeconds: 1},
			},
		}
	}
	content := func(values []domain.ManagerProposalAction) []byte {
		t.Helper()
		data, _, err := canonicalContent(managerProposalContent{
			WorkspaceID: fixture.base.workspace.ID, ProjectID: fixture.base.project.ID,
			ObjectiveID: fixture.base.objective.ID, ObjectiveRevision: fixture.base.objective.Revision,
			SourceRunID: fixture.managerRunID, SourceAgentID: fixture.base.manager.ID,
			GrantID: fixture.grant.ID, GrantRevision: fixture.grant.Revision,
			Kind: domain.ManagerProposalTaskDecomposition, Summary: "Exact full canonical proposal boundary.",
			AsOfEventSequence: fixture.packetSequence, Actions: values,
		})
		if err != nil {
			t.Fatalf("canonicalContent(boundary proposal) = %v", err)
		}
		return data
	}
	baseLength := len(content(actions))
	remaining := maximumManagerProposalBytes - baseLength
	if remaining < 0 || remaining > actionCount*4095 {
		t.Fatalf("canonical boundary fixture base=%d remaining=%d cannot reach %d within description bounds", baseLength, remaining, maximumManagerProposalBytes)
	}
	for index := range actions {
		add := remaining
		if add > 4095 {
			add = 4095
		}
		actions[index].CreateTask.Description += strings.Repeat("x", add)
		remaining -= add
	}
	if got := len(content(actions)); got != maximumManagerProposalBytes {
		t.Fatalf("canonical proposal bytes = %d, want exact boundary %d", got, maximumManagerProposalBytes)
	}

	commandActions := proposalActionsWithoutAssignedIdentity(actions)
	submitted, err := fixture.storage.SubmitManagerProposal(context.Background(), SubmitManagerProposalCommand{
		RunID:                 fixture.managerRunID,
		ManagerGrantID:        fixture.grant.ID,
		ExpectedGrantRevision: fixture.grant.Revision,
		Kind:                  domain.ManagerProposalTaskDecomposition,
		Summary:               "Exact full canonical proposal boundary.",
		AsOfEventSequence:     fixture.packetSequence,
		Actions:               commandActions,
		IdempotencyKey:        "canonical-proposal-exact-boundary",
		CorrelationID:         "request-canonical-proposal-exact-boundary",
	})
	if err != nil || submitted.Proposal.Status != domain.ManagerProposalPending {
		t.Fatalf("SubmitManagerProposal(exact %d-byte canonical content) = %#v, %v", maximumManagerProposalBytes, submitted.Proposal, err)
	}
	var storedBytes int
	if err := fixture.storage.db.QueryRow(`SELECT length(CAST(content_json AS BLOB)) FROM manager_proposals WHERE id=?`, submitted.Proposal.ID).Scan(&storedBytes); err != nil || storedBytes != maximumManagerProposalBytes {
		t.Fatalf("stored canonical proposal bytes = %d, %v; want %d", storedBytes, err, maximumManagerProposalBytes)
	}

	over := proposalActionsWithoutAssignedIdentity(actions)
	for index := len(over) - 1; index >= 0; index-- {
		if len(over[index].CreateTask.Description) < 4096 {
			over[index].CreateTask.Description += "x"
			break
		}
	}
	_, err = fixture.storage.SubmitManagerProposal(context.Background(), SubmitManagerProposalCommand{
		RunID:                 fixture.managerRunID,
		ManagerGrantID:        fixture.grant.ID,
		ExpectedGrantRevision: fixture.grant.Revision,
		Kind:                  domain.ManagerProposalTaskDecomposition,
		Summary:               "Exact full canonical proposal boundary.",
		AsOfEventSequence:     fixture.packetSequence,
		Actions:               over,
		IdempotencyKey:        "canonical-proposal-over-boundary",
		CorrelationID:         "request-canonical-proposal-over-boundary",
	})
	if ErrorCode(err) != CodeInvalidManagerProposal || !strings.Contains(err.Error(), "canonical proposal exceeds") {
		t.Fatalf("SubmitManagerProposal(%d-byte canonical content) = %v, code %q; want %q", maximumManagerProposalBytes+1, err, ErrorCode(err), CodeInvalidManagerProposal)
	}
	assertManagementRowCount(t, fixture.storage, 1, `SELECT COUNT(*) FROM manager_proposals WHERE grant_id=?`, fixture.grant.ID)
	assertManagementRowCount(t, fixture.storage, 0, `SELECT COUNT(*) FROM idempotency_keys WHERE key='canonical-proposal-over-boundary'`)
}

func proposalActionsWithoutAssignedIdentity(values []domain.ManagerProposalAction) []domain.ManagerProposalAction {
	result := make([]domain.ManagerProposalAction, len(values))
	for index, value := range values {
		copy := value
		copy.ID, copy.Ordinal = "", 0
		if value.CreateTask != nil {
			payload := *value.CreateTask
			copy.CreateTask = &payload
		}
		result[index] = copy
	}
	return result
}
