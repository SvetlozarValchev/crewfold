package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func TestM21ExecutiveContextDistinguishesStoredTaskStatusFromRunnableReadiness(t *testing.T) {
	storage, fixture, _ := createManagerInvocationFixture(t)
	ctx := context.Background()
	dependency, err := storage.CreateTask(ctx, CreateTaskCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID, ObjectiveID: fixture.objective.ID,
		Title: "Required foundation", Budget: domain.Budget{}, IdempotencyKey: "executive-readiness-foundation", CorrelationID: "executive-readiness-foundation",
	})
	if err != nil {
		t.Fatal(err)
	}
	dependent, err := storage.CreateTask(ctx, CreateTaskCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID, ObjectiveID: fixture.objective.ID,
		Title: "Dependent implementation", Budget: domain.Budget{}, IdempotencyKey: "executive-readiness-dependent", CorrelationID: "executive-readiness-dependent",
	})
	if err != nil {
		t.Fatal(err)
	}
	dependent, err = storage.AddTaskDependency(ctx, AddTaskDependencyCommand{
		WorkspaceIdentifier: fixture.workspace.ID, TaskID: dependent.Detail.Task.ID, DependsOnTaskID: dependency.Detail.Task.ID,
		ExpectedRevision: dependent.Detail.Task.Revision, IdempotencyKey: "executive-readiness-link", CorrelationID: "executive-readiness-link",
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := storage.BuildOwnerInterpretationSnapshot(ctx, fixture.workspace.ID, fixture.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	var contextValue struct {
		Tasks []struct {
			ID        string               `json:"id"`
			Status    string               `json:"status"`
			Readiness domain.TaskReadiness `json:"readiness"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(snapshot.CanonicalContext, &contextValue); err != nil {
		t.Fatal(err)
	}
	for _, task := range contextValue.Tasks {
		if task.ID != dependent.Detail.Task.ID {
			continue
		}
		if task.Status != domain.TaskReady || task.Readiness.Ready || !strings.Contains(task.Readiness.Reason, dependency.Detail.Task.ID) {
			t.Fatalf("dependent executive task context = %#v; want stored ready but derived waiting for %s", task, dependency.Detail.Task.ID)
		}
		return
	}
	t.Fatalf("executive context omitted dependent task %s", dependent.Detail.Task.ID)
}

func TestInvokeManagerDerivesAuthorityFromExactGrant(t *testing.T) {
	t.Parallel()
	storage, fixture, profile := createManagerInvocationFixture(t)
	command := InvokeManagerCommand{
		WorkspaceIdentifier:     fixture.workspace.ID,
		ObjectiveID:             fixture.objective.ID,
		TaskID:                  fixture.planning.Task.ID,
		ManagerGrantID:          fixture.grant.ID,
		LaunchProfileID:         profile.ID,
		ExpectedTaskRevision:    fixture.planning.Task.Revision,
		ExpectedGrantRevision:   fixture.grant.Revision,
		ExpectedProfileRevision: profile.Revision,
		IdempotencyKey:          "invoke-arbitrary-label-manager",
		CorrelationID:           "request-invoke-arbitrary-label-manager",
	}
	created, err := storage.InvokeManager(context.Background(), command)
	if err != nil {
		t.Fatalf("InvokeManager() = %v", err)
	}
	replayed, err := storage.InvokeManager(context.Background(), command)
	if err != nil || !reflect.DeepEqual(replayed, created) {
		t.Fatalf("InvokeManager(replay) = %#v, %v; want %#v", replayed, err, created)
	}
	if created.Detail.Run.Status != domain.RunRequested || created.Detail.Run.AgentID != fixture.manager.ID ||
		created.Detail.Run.AssignmentID != fixture.planning.Task.AssignmentID || created.Detail.Run.ContextPacketID == "" {
		t.Fatalf("manager run = %#v", created.Detail.Run)
	}
	packet, err := storage.ContextPacket(context.Background(), fixture.workspace.ID, created.Detail.Run.ContextPacketID)
	if err != nil {
		t.Fatalf("ContextPacket() = %v", err)
	}
	if packet.Schema != domain.ContextPacketSchema || packet.ManagementGrant == nil ||
		packet.ManagementGrant.GrantID != fixture.grant.ID || packet.ManagementGrant.ManagerTaskID != fixture.planning.Task.ID ||
		packet.ManagementGrant.ManagerAgentID != fixture.manager.ID || packet.ManagementGrant.OwnerExecutive ||
		packet.ManagementGrant.InvocationProfileID != profile.ID || packet.ManagementGrant.InvocationProfileRev != profile.Revision {
		t.Fatalf("manager packet authority = %#v", packet)
	}
	if fixture.manager.Role != "constellation cartographer" {
		t.Fatalf("authority test accidentally depends on a special role: %q", fixture.manager.Role)
	}
	wantTools := append(append([]string(nil), runScopedTools...), "crewfold_propose_assignment")
	if !reflect.DeepEqual(packet.Policy.AllowedTools, wantTools) {
		t.Fatalf("allowed tools = %#v, want %#v", packet.Policy.AllowedTools, wantTools)
	}
	var runCount, jobCount, bindingCount int
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM runs WHERE assignment_id=?", fixture.planning.Task.AssignmentID).Scan(&runCount); err != nil || runCount != 1 {
		t.Fatalf("manager run count = %d, %v; want one", runCount, err)
	}
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM run_jobs WHERE run_id=? AND status='pending' AND origin='owner'", created.Detail.Run.ID).Scan(&jobCount); err != nil || jobCount != 1 {
		t.Fatalf("manager job count = %d, %v; want one owner-origin job", jobCount, err)
	}
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM run_context_bindings WHERE run_id=? AND context_packet_id=?", created.Detail.Run.ID, packet.ID).Scan(&bindingCount); err != nil || bindingCount != 1 {
		t.Fatalf("manager binding count = %d, %v; want one", bindingCount, err)
	}

	changed := command
	changed.ExpectedProfileRevision++
	if _, err := storage.InvokeManager(context.Background(), changed); ErrorCode(err) != CodeIdempotencyConflict {
		t.Fatalf("InvokeManager(changed replay) = %v, code %q; want idempotency conflict", err, ErrorCode(err))
	}
}

func TestM21FailedOwnerExecutiveExchangeDoesNotCreateMeaninglessApproval(t *testing.T) {
	storage, fixture, profile := createManagerInvocationFixture(t)
	ctx := context.Background()
	binding, err := storage.CreateOwnerExecutiveBinding(ctx, CreateOwnerExecutiveBindingCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		ProjectIdentifier:   fixture.project.ID,
		ObjectiveID:         fixture.objective.ID,
		PlanningTaskID:      fixture.planning.Task.ID,
		AgentID:             fixture.manager.ID,
		ManagerGrantID:      fixture.grant.ID,
		LaunchProfileID:     profile.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := storage.BuildOwnerInterpretationSnapshot(ctx, fixture.workspace.ID, fixture.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	turn, err := storage.RequestOwnerExecutiveTurn(ctx, RequestOwnerExecutiveTurnCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		ProjectIdentifier:   fixture.project.ID,
		Instruction:         "Summarize the project state",
		Kind:                "instruction",
		IdempotencyKey:      "failed-executive-turn",
		Snapshot:            snapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	exchange, found, err := storage.ClaimOwnerExecutiveExchange(ctx)
	if err != nil || !found || exchange.ID != turn.Exchange.ID || exchange.BindingID != binding.ID {
		t.Fatalf("ClaimOwnerExecutiveExchange() = %#v, %t, %v", exchange, found, err)
	}
	created, err := storage.InvokeManager(ctx, InvokeManagerCommand{
		WorkspaceIdentifier:     fixture.workspace.ID,
		ObjectiveID:             fixture.objective.ID,
		TaskID:                  fixture.planning.Task.ID,
		ManagerGrantID:          fixture.grant.ID,
		LaunchProfileID:         profile.ID,
		ExpectedTaskRevision:    fixture.planning.Task.Revision,
		ExpectedGrantRevision:   fixture.grant.Revision,
		ExpectedProfileRevision: profile.Revision,
		IdempotencyKey:          "failed-executive-run",
		CorrelationID:           "failed-executive-run",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.DispatchOwnerExecutiveExchange(ctx, exchange.ID, created.Detail.Run.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.MarkRunStarting(ctx, created.Detail.Run.ID, "failed-executive-starting"); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.MarkRunStarted(ctx, created.Detail.Run.ID, "runtime", "provider", "failed-executive-started"); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.FailRun(ctx, created.Detail.Run.ID, "provider_ended", "provider exited before responding", nil, "provider logs unavailable", "failed-executive-terminal"); err != nil {
		t.Fatal(err)
	}
	configureAdversarialSupervisor(t, storage, fixture.workspace.ID, "failed-executive-policy")
	scan, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		Limit:               100,
		IdempotencyKey:      "failed-executive-scan",
		CorrelationID:       "failed-executive-scan",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range scan.Actions {
		if action.RunID == created.Detail.Run.ID {
			t.Fatalf("failed executive exchange created owner decision: %#v", action)
		}
	}
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM supervisor_actions WHERE run_id=?`, created.Detail.Run.ID)
	assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM approval_requests WHERE action_id IN (SELECT id FROM supervisor_actions WHERE run_id=?)`, created.Detail.Run.ID)
	storedTurn, err := storage.OwnerExecutiveTurn(ctx, fixture.workspace.ID, turn.Detail.Turn.ID)
	if err != nil || storedTurn.Detail.Turn.Status != "failed" || storedTurn.Exchange.Status != "failed" {
		t.Fatalf("failed executive remains visible on its conversation = %#v, %v", storedTurn, err)
	}
}

func TestM21ProactiveExecutiveReviewCoalescesBehindEarlierExchange(t *testing.T) {
	storage, fixture, profile := createManagerInvocationFixture(t)
	ctx := context.Background()
	if _, err := storage.CreateOwnerExecutiveBinding(ctx, CreateOwnerExecutiveBindingCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		ProjectIdentifier:   fixture.project.ID,
		ObjectiveID:         fixture.objective.ID,
		PlanningTaskID:      fixture.planning.Task.ID,
		AgentID:             fixture.manager.ID,
		ManagerGrantID:      fixture.grant.ID,
		LaunchProfileID:     profile.ID,
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := storage.BuildOwnerInterpretationSnapshot(ctx, fixture.workspace.ID, fixture.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	first, err := storage.RequestOwnerExecutiveTurn(ctx, RequestOwnerExecutiveTurnCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		ProjectIdentifier:   fixture.project.ID,
		Instruction:         "Establish the current project direction",
		Kind:                "instruction",
		IdempotencyKey:      "coalesced-executive-owner-turn",
		Snapshot:            snapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	reviewSnapshot, err := storage.BuildOwnerInterpretationSnapshot(ctx, fixture.workspace.ID, fixture.project.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = storage.RequestOwnerExecutiveTurn(ctx, RequestOwnerExecutiveTurnCommand{
		WorkspaceIdentifier:  fixture.workspace.ID,
		ProjectIdentifier:    fixture.project.ID,
		ConversationID:       first.Detail.Conversation.ID,
		Instruction:          "Review the newest worker facts",
		Kind:                 "review",
		InitiatedBy:          "executive",
		TriggerEventSequence: reviewSnapshot.EventSequence,
		IdempotencyKey:       "coalesced-executive-review",
		Snapshot:             reviewSnapshot,
	})
	if !OwnerExecutiveReviewTemporarilyBusy(err) {
		t.Fatalf("RequestOwnerExecutiveTurn(concurrent review) = %v, code %q; want temporary coalescing conflict", err, ErrorCode(err))
	}
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM owner_turns WHERE conversation_id=?`, first.Detail.Conversation.ID)
	assertManagementRowCount(t, storage, 1, `SELECT COUNT(*) FROM owner_executive_exchanges WHERE binding_id=?`, first.Exchange.BindingID)
}

func TestM21WorkbenchWorkerExceptionsReachExecutiveWithoutGenericApproval(t *testing.T) {
	for _, terminal := range []string{"blocked", "failed"} {
		terminal := terminal
		t.Run(terminal, func(t *testing.T) {
			storage, fixture, executiveProfile := createManagerInvocationFixture(t)
			ctx := context.Background()
			if _, err := storage.CreateOwnerExecutiveBinding(ctx, CreateOwnerExecutiveBindingCommand{
				WorkspaceIdentifier: fixture.workspace.ID,
				ProjectIdentifier:   fixture.project.ID,
				ObjectiveID:         fixture.objective.ID,
				PlanningTaskID:      fixture.planning.Task.ID,
				AgentID:             fixture.manager.ID,
				ManagerGrantID:      fixture.grant.ID,
				LaunchProfileID:     executiveProfile.ID,
			}); err != nil {
				t.Fatal(err)
			}
			snapshot, err := storage.BuildOwnerInterpretationSnapshot(ctx, fixture.workspace.ID, fixture.project.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := storage.RequestOwnerExecutiveTurn(ctx, RequestOwnerExecutiveTurnCommand{
				WorkspaceIdentifier: fixture.workspace.ID,
				ProjectIdentifier:   fixture.project.ID,
				Instruction:         "Coordinate worker exceptions for this project",
				Kind:                "instruction",
				IdempotencyKey:      "worker-exception-owner-turn-" + terminal,
				Snapshot:            snapshot,
			}); err != nil {
				t.Fatal(err)
			}
			workerTask, err := storage.CreateTask(ctx, CreateTaskCommand{
				WorkspaceIdentifier: fixture.workspace.ID,
				ProjectIdentifier:   fixture.project.ID,
				ObjectiveID:         fixture.objective.ID,
				Title:               "Exercise " + terminal + " executive routing",
				Budget:              domain.Budget{TokenLimit: 200, CostCents: 20, TimeSeconds: 300},
				IdempotencyKey:      "worker-exception-task-" + terminal,
				CorrelationID:       "worker-exception-task-" + terminal,
			})
			if err != nil {
				t.Fatal(err)
			}
			assigned, err := storage.AssignTask(ctx, AssignTaskCommand{
				WorkspaceIdentifier: fixture.workspace.ID,
				TaskID:              workerTask.Detail.Task.ID,
				AgentIdentifier:     fixture.target.AgentID,
				LeaseSeconds:        900,
				ExpectedRevision:    workerTask.Detail.Task.Revision,
				IdempotencyKey:      "worker-exception-assignment-" + terminal,
				CorrelationID:       "worker-exception-assignment-" + terminal,
			})
			if err != nil {
				t.Fatal(err)
			}
			active := startAdversarialRun(t, storage, fixture.workspace.ID, assigned.Detail, "worker-exception-"+terminal)
			switch terminal {
			case "blocked":
				if _, err := storage.ApplyRunObservation(ctx, active.Run.ID, domain.RunObservation{
					Kind: domain.ObservationBlocked, Message: "The package source is unavailable and no repair has been proved.",
				}, true, nil, "worker-exception-blocked-applied"); err != nil {
					t.Fatal(err)
				}
			case "failed":
				if _, err := storage.FailRun(ctx, active.Run.ID, "process_exited", "The worker process exited before completing its task.", nil, "terminal logs were unavailable", "worker-exception-failed-applied"); err != nil {
					t.Fatal(err)
				}
			}
			configureAdversarialSupervisor(t, storage, fixture.workspace.ID, "worker-exception-policy-"+terminal)
			scan, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
				WorkspaceIdentifier: fixture.workspace.ID,
				Limit:               100,
				IdempotencyKey:      "worker-exception-scan-" + terminal,
				CorrelationID:       "worker-exception-scan-" + terminal,
			})
			if err != nil {
				t.Fatal(err)
			}
			var routed int
			for _, action := range scan.Actions {
				if action.RunID != active.Run.ID {
					continue
				}
				if action.Status != domain.SupervisorActionDeferred || action.Response != domain.SupervisorResponseRequestOwner || action.ApprovalID != "" {
					t.Fatalf("workbench %s created an owner effect instead of a routed attention receipt: %#v", terminal, action)
				}
				routed++
			}
			if terminal == "failed" && routed != 1 {
				t.Fatalf("workbench failed routed attention actions = %d, want one exact run-revision receipt", routed)
			}
			if terminal == "blocked" && routed != 0 {
				t.Fatalf("workbench blocker created %d redundant supervisor attention actions", routed)
			}
			assertManagementRowCount(t, storage, 0, `SELECT COUNT(*) FROM approval_requests WHERE action_id IN (SELECT id FROM supervisor_actions WHERE run_id=?)`, active.Run.ID)
			review, found, err := storage.OwnerManagerReview(ctx, fixture.workspace.ID, fixture.project.ID)
			if err != nil || !found || review.Status != "pending" || review.RequestedEventSequence < 1 {
				t.Fatalf("workbench %s executive attention = %#v, %t, %v", terminal, review, found, err)
			}
			second, err := storage.RunSupervisor(ctx, RunSupervisorCommand{
				WorkspaceIdentifier: fixture.workspace.ID,
				Limit:               100,
				IdempotencyKey:      "worker-exception-second-scan-" + terminal,
				CorrelationID:       "worker-exception-second-scan-" + terminal,
			})
			if err != nil {
				t.Fatal(err)
			}
			for _, action := range second.Actions {
				if action.RunID == active.Run.ID {
					t.Fatalf("unchanged workbench %s exception was routed twice: %#v", terminal, action)
				}
			}
			after, found, err := storage.OwnerManagerReview(ctx, fixture.workspace.ID, fixture.project.ID)
			if err != nil || !found || after.RequestedEventSequence != review.RequestedEventSequence {
				t.Fatalf("unchanged workbench %s review cursor = %#v, %t, %v; want unchanged %#v", terminal, after, found, err, review)
			}
		})
	}
}

func TestInvokeManagerRollsBackPacketRunAndJob(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{MutationAfterProjection, MutationAfterEvent} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			storage, fixture, profile := createManagerInvocationFixture(t)
			storage.mutationHook = func(current string) error {
				if current == stage {
					return errors.New("injected manager invocation failure")
				}
				return nil
			}
			_, err := storage.InvokeManager(context.Background(), InvokeManagerCommand{
				WorkspaceIdentifier: fixture.workspace.ID, ObjectiveID: fixture.objective.ID,
				TaskID: fixture.planning.Task.ID, ManagerGrantID: fixture.grant.ID, LaunchProfileID: profile.ID,
				ExpectedTaskRevision: fixture.planning.Task.Revision, ExpectedGrantRevision: fixture.grant.Revision,
				ExpectedProfileRevision: profile.Revision, IdempotencyKey: "invoke-rollback-" + stage,
				CorrelationID: "request-invoke-rollback-" + stage,
			})
			if ErrorCode(err) != CodeStorageFailed {
				t.Fatalf("InvokeManager(%s) = %v, code %q; want storage_failed", stage, err, ErrorCode(err))
			}
			storage.mutationHook = nil
			for name, query := range map[string]string{
				"runs":    "SELECT COUNT(*) FROM runs WHERE assignment_id=?",
				"packets": "SELECT COUNT(*) FROM context_packets WHERE task_id=? AND json_extract(packet_json,'$.schema')='urn:crewfold:schema:domain:context-packet:v1'",
				"jobs":    "SELECT COUNT(*) FROM run_jobs WHERE run_id IN (SELECT id FROM runs WHERE assignment_id=?)",
			} {
				var count int
				if scanErr := storage.db.QueryRow(query, fixture.planning.Task.AssignmentID).Scan(&count); scanErr != nil || count != 0 {
					t.Fatalf("%s after rollback = %d, %v; want zero", name, count, scanErr)
				}
			}
		})
	}
}

func createManagerInvocationFixture(t *testing.T) (*Store, managerGrantAdversarialFixture, domain.LaunchProfile) {
	t.Helper()
	storage, fixture := createManagerGrantAdversarialFixture(t)
	// Exercise the valid empty claim-kind authority set. This previously became
	// JSON null while building manager authority and was rejected by the SQL authority
	// seal, even though the immutable grant correctly stored canonical JSON [].
	grantResult, err := storage.CreateManagerGrant(context.Background(), CreateManagerGrantCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID,
		ObjectiveID: fixture.objective.ID, TaskID: fixture.planning.Task.ID,
		AgentIdentifier: fixture.manager.ID, ExpectedTaskRevision: fixture.planning.Task.Revision,
		ExpectedAgentRevision: fixture.manager.Revision,
		ProposalKinds:         []string{domain.ManagerProposalAssignment},
		LaunchProfileIDs:      []string{fixture.target.ID},
		AllowedClaimKinds:     []string{},
		Limits: domain.ManagerProposalLimits{
			MaxOpenProposals: 2, MaxActions: 2, MaxTasks: 1, MaxDependencies: 1,
			MaxClaimRequirements: 1, Budget: domain.Budget{TokenLimit: 1000, CostCents: 100, TimeSeconds: 600},
		},
		IdempotencyKey: "manager-invocation-empty-claims-grant", CorrelationID: "request-manager-invocation-empty-claims-grant",
	})
	if err != nil {
		t.Fatalf("CreateManagerGrant(empty claims) = %v", err)
	}
	fixture.grant = grantResult.Value
	scenario := domain.FakeScenario{
		Schema: execution.FakeScenarioSchema,
		Name:   "arbitrary-label-manager-plan",
		Steps:  []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "propose bounded work"}},
	}
	profile, err := storage.CreateLaunchProfile(context.Background(), CreateLaunchProfileCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID,
		AgentIdentifier: fixture.manager.ID, ExpectedAgentRevision: fixture.manager.Revision,
		Runtime: fixture.manager.Runtime, Provider: fixture.manager.Provider, Scenario: scenario,
		AssignmentLeaseSeconds: 900, CapabilityTTLSeconds: 900, ManagerGrantID: fixture.grant.ID,
		IdempotencyKey: "manager-invocation-profile", CorrelationID: "request-manager-invocation-profile",
	})
	if err != nil {
		t.Fatalf("CreateLaunchProfile(manager) = %v", err)
	}
	return storage, fixture, profile.Value
}
