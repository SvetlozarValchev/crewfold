package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func TestOwnerTaskTransitionsRejectReservedRunAuthority(t *testing.T) {
	for _, runStatus := range []string{domain.RunRequested, domain.RunStarting} {
		t.Run(runStatus, func(t *testing.T) {
			storage := openTestStore(t, t.TempDir(), Options{})
			workspace, _, _, _, assigned := initializeRunTest(t, storage, "owner transition "+runStatus)
			created := createRunTest(t, storage, workspace.ID, assigned, domain.FakeScenario{
				Schema: execution.FakeScenarioSchema,
				Name:   "owner-transition-" + runStatus,
				Steps:  []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "reserved"}},
			}, "owner-transition-run-"+runStatus)
			if runStatus == domain.RunStarting {
				if _, err := storage.MarkRunStarting(context.Background(), created.Run.ID, "worker-owner-transition-starting"); err != nil {
					t.Fatalf("MarkRunStarting() = %v", err)
				}
			}

			for _, action := range []string{"start", "block", "cancel"} {
				_, err := storage.TransitionTask(context.Background(), TransitionTaskCommand{
					WorkspaceIdentifier: workspace.ID,
					TaskID:              assigned.Task.ID,
					Action:              action,
					Reason:              "owner transition must not split reserved run authority",
					ExpectedRevision:    assigned.Task.Revision,
					IdempotencyKey:      fmt.Sprintf("owner-%s-%s", action, runStatus),
					CorrelationID:       fmt.Sprintf("request-owner-%s-%s", action, runStatus),
				})
				if ErrorCode(err) != CodeRunConflict {
					t.Fatalf("TransitionTask(%s with %s run) error=%v code=%q, want %q", action, runStatus, err, ErrorCode(err), CodeRunConflict)
				}
			}

			detail, err := storage.RunDetail(context.Background(), workspace.ID, created.Run.ID)
			if err != nil || detail.Run.Status != runStatus || detail.Task.Status != domain.TaskAssigned || detail.Task.Revision != assigned.Task.Revision {
				t.Fatalf("reserved state after rejected owner transitions = %#v, %v", detail, err)
			}
		})
	}

	t.Run("blocked run requires approved resume", func(t *testing.T) {
		storage := openTestStore(t, t.TempDir(), Options{})
		workspace, _, _, _, assigned := initializeRunTest(t, storage, "owner unblock reserved run")
		created := createRunTest(t, storage, workspace.ID, assigned, domain.FakeScenario{
			Schema: execution.FakeScenarioSchema,
			Name:   "owner-unblock-reserved-run",
			Steps:  []domain.FakeStep{{Kind: domain.ObservationBlocked, Message: "agent needs a decision"}},
		}, "owner-unblock-reserved-run")
		if _, err := storage.MarkRunStarting(context.Background(), created.Run.ID, "worker-owner-unblock-starting"); err != nil {
			t.Fatalf("MarkRunStarting() = %v", err)
		}
		if _, err := storage.MarkRunStarted(context.Background(), created.Run.ID, "runtime-owner-unblock", "provider-owner-unblock", "worker-owner-unblock-started"); err != nil {
			t.Fatalf("MarkRunStarted() = %v", err)
		}
		blocked, err := storage.ApplyRunObservation(context.Background(), created.Run.ID, domain.RunObservation{
			Kind: domain.ObservationBlocked, Message: "agent needs a decision",
		}, true, nil, "worker-owner-unblock-blocked")
		if err != nil || blocked.Run.Status != domain.RunBlocked || blocked.Task.Status != domain.TaskBlocked {
			t.Fatalf("ApplyRunObservation(blocked) = %#v, %v", blocked, err)
		}

		_, err = storage.TransitionTask(context.Background(), TransitionTaskCommand{
			WorkspaceIdentifier: workspace.ID,
			TaskID:              assigned.Task.ID,
			Action:              "unblock",
			ExpectedRevision:    blocked.Task.Revision,
			IdempotencyKey:      "owner-unblock-reserved",
			CorrelationID:       "request-owner-unblock-reserved",
		})
		if ErrorCode(err) != CodeRunConflict {
			t.Fatalf("TransitionTask(unblock reserved run) error=%v code=%q, want %q", err, ErrorCode(err), CodeRunConflict)
		}
		resumed, err := storage.ResumeRun(context.Background(), ResumeRunCommand{
			WorkspaceIdentifier: workspace.ID,
			RunID:               created.Run.ID,
			ExpectedRevision:    blocked.Run.Revision,
			IdempotencyKey:      "approved-resume-after-owner-unblock-rejection",
			CorrelationID:       "request-approved-resume-after-owner-unblock-rejection",
		})
		if err != nil || resumed.Detail.Run.Status != domain.RunActive || resumed.Detail.Task.Status != domain.TaskActive {
			t.Fatalf("ResumeRun() = %#v, %v", resumed, err)
		}
	})

	t.Run("unreserved task still blocks and unblocks", func(t *testing.T) {
		storage := openTestStore(t, t.TempDir(), Options{})
		workspace, _, _, _, assigned := initializeRunTest(t, storage, "owner unblock without run")
		blocked, err := storage.TransitionTask(context.Background(), TransitionTaskCommand{
			WorkspaceIdentifier: workspace.ID,
			TaskID:              assigned.Task.ID,
			Action:              "block",
			Reason:              "ordinary owner decision",
			ExpectedRevision:    assigned.Task.Revision,
			IdempotencyKey:      "owner-block-without-run",
			CorrelationID:       "request-owner-block-without-run",
		})
		if err != nil || blocked.Detail.Task.Status != domain.TaskBlocked {
			t.Fatalf("TransitionTask(block without run) = %#v, %v", blocked, err)
		}
		unblocked, err := storage.TransitionTask(context.Background(), TransitionTaskCommand{
			WorkspaceIdentifier: workspace.ID,
			TaskID:              assigned.Task.ID,
			Action:              "unblock",
			ExpectedRevision:    blocked.Detail.Task.Revision,
			IdempotencyKey:      "owner-unblock-without-run",
			CorrelationID:       "request-owner-unblock-without-run",
		})
		if err != nil || unblocked.Detail.Task.Status != domain.TaskAssigned {
			t.Fatalf("TransitionTask(unblock without run) = %#v, %v", unblocked, err)
		}
	})
}

func TestMarkRunStartingRevalidatesCurrentTaskAndExactAssignment(t *testing.T) {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "launch gate current task")
	created := createRunTest(t, storage, workspace.ID, assigned, domain.FakeScenario{
		Schema: execution.FakeScenarioSchema,
		Name:   "launch-gate-current-task",
		Steps:  []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "must not launch"}},
	}, "launch-gate-current-task")
	// The reservation triggers make replacing/releasing the exact assignment
	// impossible through supported writes. Tamper only the independently mutable
	// task projection to prove the worker still rechecks it at launch time.
	if _, err := storage.db.Exec(`UPDATE tasks SET status='blocked',blocked_reason='owner decision won the race' WHERE id=?`, assigned.Task.ID); err != nil {
		t.Fatalf("tamper task projection before launch = %v", err)
	}

	_, err := storage.MarkRunStarting(context.Background(), created.Run.ID, "worker-revalidate-launch-gate")
	if ErrorCode(err) != CodeRunConflict {
		t.Fatalf("MarkRunStarting() error=%v code=%q, want %q", err, ErrorCode(err), CodeRunConflict)
	}
	var status string
	if queryErr := storage.db.QueryRow(`SELECT status FROM runs WHERE id=?`, created.Run.ID).Scan(&status); queryErr != nil || status != domain.RunRequested {
		t.Fatalf("run after rejected launch gate status=%q query error=%v", status, queryErr)
	}
}

func TestClaimRunJobUsesFrozenSupervisorAuthorityAfterCommit(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		retry  bool
		mutate func(*testing.T, *Store, managerGrantAdversarialFixture)
	}{
		{
			name: "initial profile retired",
			mutate: func(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture) {
				t.Helper()
				if _, err := storage.RetireLaunchProfile(context.Background(), RetireLaunchProfileCommand{
					WorkspaceIdentifier: fixture.workspace.ID,
					LaunchProfileID:     fixture.target.ID,
					ExpectedRevision:    fixture.target.Revision,
					Reason:              "retire only after committed scheduling receipt",
					IdempotencyKey:      "retire-profile-after-commit",
					CorrelationID:       "request-retire-profile-after-commit",
				}); err != nil {
					t.Fatalf("RetireLaunchProfile() = %v", err)
				}
			},
		},
		{
			name:   "initial agent disabled",
			mutate: disableCommittedRunAgent,
		},
		{
			name:  "retry profile retired",
			retry: true,
			mutate: func(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture) {
				t.Helper()
				if _, err := storage.RetireLaunchProfile(context.Background(), RetireLaunchProfileCommand{
					WorkspaceIdentifier: fixture.workspace.ID,
					LaunchProfileID:     fixture.target.ID,
					ExpectedRevision:    fixture.target.Revision,
					Reason:              "retire only after committed retry receipt",
					IdempotencyKey:      "retire-profile-after-retry-commit",
					CorrelationID:       "request-retire-profile-after-retry-commit",
				}); err != nil {
					t.Fatalf("RetireLaunchProfile(retry) = %v", err)
				}
			},
		},
		{
			name:   "retry agent disabled",
			retry:  true,
			mutate: disableCommittedRunAgent,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			key := strings.ReplaceAll(testCase.name, " ", "-")
			storage, fixture, runID := committedSupervisorRunForWorkerTest(t, key, testCase.retry)
			testCase.mutate(t, storage, fixture)

			work, found, err := storage.ClaimRunLaunchJob(context.Background(), 30*time.Second)
			if err != nil || !found || work.Run.ID != runID || work.Run.Status != domain.RunRequested {
				t.Fatalf("ClaimRunLaunchJob() = %#v, found=%t, error=%v; want committed run %s", work, found, err, runID)
			}
			starting, err := storage.MarkRunStarting(context.Background(), runID, "worker-start-frozen-authority")
			if err != nil || starting.Status != domain.RunStarting {
				t.Fatalf("MarkRunStarting(committed authority) = %#v, %v", starting, err)
			}
		})
	}
}

func TestCommittedSupervisorRunStartsAfterAssignmentLeaseDeadline(t *testing.T) {
	storage, fixture, runID := committedSupervisorRunForWorkerTest(t, "committed-run-expired-lease", false)
	var leaseText string
	if err := storage.db.QueryRow(`SELECT assignment.lease_expires_at
FROM runs run JOIN task_assignments assignment ON assignment.id=run.assignment_id
WHERE run.id=?`, runID).Scan(&leaseText); err != nil {
		t.Fatalf("read committed assignment deadline = %v", err)
	}
	leaseDeadline, err := time.Parse(time.RFC3339Nano, leaseText)
	if err != nil {
		t.Fatalf("parse committed assignment deadline = %v", err)
	}
	storage.clock = func() time.Time { return leaseDeadline.Add(time.Nanosecond) }

	work, found, err := storage.ClaimRunLaunchJob(context.Background(), 30*time.Second)
	if err != nil || !found || work.Run.ID != runID {
		t.Fatalf("ClaimRunLaunchJob(after assignment deadline) = %#v, found=%t, error=%v", work, found, err)
	}
	starting, err := storage.MarkRunStarting(context.Background(), runID, "worker-start-committed-after-lease")
	if err != nil || starting.Status != domain.RunStarting || starting.WorkspaceID != fixture.workspace.ID {
		t.Fatalf("MarkRunStarting(after assignment deadline) = %#v, %v", starting, err)
	}
	// The replayed gate is the crash-recovery path and must remain a no-op rather
	// than rejecting the already committed run because wall time advanced.
	replayed, err := storage.MarkRunStarting(context.Background(), runID, "worker-replay-start-committed-after-lease")
	if err != nil || replayed.Revision != starting.Revision || replayed.Status != domain.RunStarting {
		t.Fatalf("MarkRunStarting(replay after assignment deadline) = %#v, %v; want revision %d", replayed, err, starting.Revision)
	}
}

func committedSupervisorRunForWorkerTest(t *testing.T, key string, retry bool) (*Store, managerGrantAdversarialFixture, string) {
	t.Helper()
	storage, fixture, proposalID, intentID := acceptedSingleSchedulingIntent(t, key)
	ctx := context.Background()
	var managerRunID string
	if err := storage.db.QueryRow(`SELECT source_run_id FROM manager_proposals WHERE id=?`, proposalID).Scan(&managerRunID); err != nil {
		t.Fatalf("read manager run = %v", err)
	}
	if _, err := storage.MarkRunStarted(ctx, managerRunID, "manager-runtime-"+key, "manager-provider-"+key, "request-manager-started-"+key); err != nil {
		t.Fatalf("MarkRunStarted(manager) = %v", err)
	}
	if _, err := storage.ApplyRunObservation(ctx, managerRunID, domain.RunObservation{
		Kind: domain.ObservationCompletion, Message: "proposal committed", Handoff: "owner accepted exact task",
	}, true, nil, "request-manager-completed-"+key); err != nil {
		t.Fatalf("ApplyRunObservation(manager completion) = %v", err)
	}
	if retry {
		policy, err := storage.SupervisorPolicy(ctx, fixture.workspace.ID)
		if err != nil {
			t.Fatalf("SupervisorPolicy() = %v", err)
		}
		if _, err := storage.ConfigureSupervisorPolicy(ctx, ConfigureSupervisorPolicyCommand{
			WorkspaceIdentifier:  fixture.workspace.ID,
			Enabled:              true,
			AutoSchedule:         true,
			Limits:               policy.Limits,
			AutoRetryLimit:       1,
			RetryCooldownSeconds: 1,
			ExpectedRevision:     policy.Revision,
			IdempotencyKey:       key + "-retry-policy",
			CorrelationID:        "request-" + key + "-retry-policy",
		}); err != nil {
			t.Fatalf("ConfigureSupervisorPolicy(retry) = %v", err)
		}
	}

	runID := scheduleSingleAcceptedIntent(t, storage, fixture.workspace.ID, intentID, key)
	if !retry {
		return storage, fixture, runID
	}
	if _, err := storage.MarkRunStarting(ctx, runID, "request-initial-retry-source-starting-"+key); err != nil {
		t.Fatalf("MarkRunStarting(retry source) = %v", err)
	}
	if _, err := storage.FailRunStart(ctx, runID, "definite failure before committed retry", "request-initial-retry-source-failed-"+key); err != nil {
		t.Fatalf("FailRunStart(retry source) = %v", err)
	}
	// The production cooldown is part of retry authority; move the controlled
	// store clock across its exact one-second boundary before asking for retry.
	priorClock := storage.clock
	base := priorClock().UTC()
	storage.clock = func() time.Time { return base.Add(time.Second) }
	retried, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		Limit:               100,
		IdempotencyKey:      key + "-retry-scan",
		CorrelationID:       "request-" + key + "-retry-scan",
	})
	if err != nil || len(retried.ScheduledRunIDs) != 1 || len(retried.Actions) != 1 || retried.Actions[0].Response != domain.SupervisorResponseRetryTask {
		t.Fatalf("RunSupervisor(retry) = %#v, %v", retried, err)
	}
	return storage, fixture, retried.ScheduledRunIDs[0]
}

func disableCommittedRunAgent(t *testing.T, storage *Store, fixture managerGrantAdversarialFixture) {
	t.Helper()
	disabled := false
	agent, err := storage.Agent(context.Background(), fixture.workspace.ID, fixture.target.AgentID)
	if err != nil {
		t.Fatalf("Agent(target) = %v", err)
	}
	if _, err := storage.UpdateAgent(context.Background(), UpdateAgentCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		AgentIdentifier:     agent.ID,
		Enabled:             &disabled,
		ExpectedRevision:    agent.Revision,
		IdempotencyKey:      "disable-agent-after-commit-" + fmt.Sprint(agent.Revision),
		CorrelationID:       "request-disable-agent-after-commit",
	}); err != nil {
		t.Fatalf("UpdateAgent(disable after commit) = %v", err)
	}
}
