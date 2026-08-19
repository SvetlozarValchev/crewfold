package store

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func prepareTestRunLogArchive(t *testing.T, storage *Store, runID string) *domain.RunLogArchive {
	t.Helper()
	archive, err := storage.PrepareRunLogArchive(context.Background(), runID, domain.RunLogs{RunID: runID, State: "exited"})
	if err != nil {
		t.Fatalf("PrepareRunLogArchive() error = %v", err)
	}
	return &archive
}

func TestRunLifecyclePersistsPlacementTimelineAcceptanceAndHandoff(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project, agent, adjacent, assigned := initializeRunTest(t, storage, "successful run")
	scenario := domain.FakeScenario{
		Schema: execution.FakeScenarioSchema, Name: "successful-run",
		Acceptance: domain.AcceptanceRule{RequiredEvidence: []string{"tests_passed"}},
		Steps: []domain.FakeStep{
			{Kind: domain.ObservationProgress, Message: "working"},
			{Kind: domain.ObservationCompletion, Message: "done", Evidence: []string{"tests_passed"}, Handoff: "review the completed work"},
		},
	}
	readOnly, err := storage.AddCheckout(context.Background(), AddCheckoutCommand{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, WriteMode: domain.WriteModeReadOnly, IdempotencyKey: "read-only-checkout", CorrelationID: "request-read-only-checkout", Observation: sourceTestObservation(filepath.Join(t.TempDir(), "read-only-clone"), "read-only")})
	if err != nil {
		t.Fatalf("AddCheckout(read only) error = %v", err)
	}
	if _, err := storage.CreateRun(context.Background(), CreateRunCommand{WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, CheckoutIdentifier: readOnly.Checkout.ID, Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: assigned.Task.Revision, IdempotencyKey: "reject-read-only-run", CorrelationID: "request-reject-read-only-run"}); ErrorCode(err) != CodePlacementUnavailable {
		t.Fatalf("CreateRun(read only) error = %v, code = %q", err, ErrorCode(err))
	}
	command := CreateRunCommand{WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, CheckoutIdentifier: adjacent.ID, Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: assigned.Task.Revision, IdempotencyKey: "start-successful-run", CorrelationID: "request-start-successful-run"}
	created, err := storage.CreateRun(context.Background(), command)
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	replayCommand := command
	replayCommand.CorrelationID = "request-start-successful-run-retry"
	replayed, err := storage.CreateRun(context.Background(), replayCommand)
	if err != nil || !reflect.DeepEqual(replayed, created) {
		t.Fatalf("CreateRun(replay) = %#v, %v; want %#v", replayed, err, created)
	}
	changedCommand := command
	changedCommand.Scenario.Name = "changed-scenario"
	if _, err := storage.CreateRun(context.Background(), changedCommand); ErrorCode(err) != CodeIdempotencyConflict {
		t.Fatalf("CreateRun(changed replay) error = %v, code = %q", err, ErrorCode(err))
	}
	secondCommand := command
	secondCommand.IdempotencyKey = "second-live-run"
	if _, err := storage.CreateRun(context.Background(), secondCommand); ErrorCode(err) != CodeRunConflict {
		t.Fatalf("CreateRun(second live run) error = %v, code = %q", err, ErrorCode(err))
	}
	if created.Detail.Run.Status != domain.RunRequested || created.Detail.Run.AgentID != agent.ID || created.Detail.Run.Placement.CheckoutPath == "" {
		t.Fatalf("created run = %#v", created.Detail.Run)
	}
	if created.Detail.Checkout.ID != adjacent.ID || created.Detail.Checkout.CheckoutKind != domain.CheckoutStandalone {
		t.Fatalf("placement checkout = %#v, want explicit adjacent standalone clone %#v", created.Detail.Checkout, adjacent)
	}

	starting, err := storage.MarkRunStarting(context.Background(), created.Detail.Run.ID, "worker-starting")
	if err != nil || starting.Status != domain.RunStarting {
		t.Fatalf("MarkRunStarting() = %#v, %v", starting, err)
	}
	const runtimeSentinel = "hostile-runtime-handle-MUST-NOT-LEAK"
	const providerSentinel = "hostile-provider-handle-MUST-NOT-LEAK"
	active, err := storage.MarkRunStarted(context.Background(), starting.ID, runtimeSentinel, providerSentinel, "worker-started")
	if err != nil || active.Run.Status != domain.RunActive || active.Task.Status != domain.TaskActive {
		t.Fatalf("MarkRunStarted() = %#v, %v", active, err)
	}
	publicJSON, err := json.Marshal(active)
	if err != nil {
		t.Fatalf("marshal active run: %v", err)
	}
	if strings.Contains(string(publicJSON), runtimeSentinel) || strings.Contains(string(publicJSON), providerSentinel) ||
		strings.Contains(string(publicJSON), "runtime_handle") || strings.Contains(string(publicJSON), "provider_handle") {
		t.Fatalf("public run JSON leaked an internal binding: %s", publicJSON)
	}
	var startedEventJSON string
	if err := storage.db.QueryRow("SELECT data_json FROM events WHERE entity_id=? AND type='run.started' ORDER BY sequence DESC LIMIT 1", starting.ID).Scan(&startedEventJSON); err != nil {
		t.Fatalf("query run.started event: %v", err)
	}
	if strings.Contains(startedEventJSON, runtimeSentinel) || strings.Contains(startedEventJSON, providerSentinel) ||
		strings.Contains(startedEventJSON, "runtime_handle") || strings.Contains(startedEventJSON, "provider_handle") {
		t.Fatalf("run.started journal payload leaked an internal binding: %s", startedEventJSON)
	}
	progress, err := storage.ApplyRunObservation(context.Background(), starting.ID, domain.RunObservation{Kind: domain.ObservationProgress, Message: "working"}, true, nil, "worker-progress")
	if err != nil || progress.Run.StepCursor != 1 {
		t.Fatalf("ApplyRunObservation(progress) = %#v, %v", progress, err)
	}
	if _, err := storage.ApplyRunObservation(context.Background(), starting.ID, domain.RunObservation{Kind: domain.ObservationCompletion, Message: "done", Evidence: []string{"tests_passed"}, LogArchive: prepareTestRunLogArchive(t, storage, starting.ID)}, true, nil, "worker-invalid-complete"); ErrorCode(err) != CodeInvalidRun {
		t.Fatalf("ApplyRunObservation(completion without handoff) error = %v, code = %q", err, ErrorCode(err))
	}
	completed, err := storage.ApplyRunObservation(context.Background(), starting.ID, domain.RunObservation{Kind: domain.ObservationCompletion, Message: "done", Evidence: []string{"tests_passed"}, Handoff: "review the completed work", LogArchive: prepareTestRunLogArchive(t, storage, starting.ID)}, true, nil, "worker-complete")
	if err != nil || completed.Run.Status != domain.RunCompleted || completed.Task.Status != domain.TaskCompleted || completed.Handoff == nil {
		t.Fatalf("ApplyRunObservation(completion) = %#v, %v", completed, err)
	}
	if completed.Handoff.Summary != "review the completed work" || len(completed.Timeline) != 7 {
		t.Fatalf("completed detail = %#v", completed)
	}
	if completed.Task.AssignmentID != "" {
		t.Fatalf("completed task retained assignment: %#v", completed.Task)
	}

	timeline, err := storage.TaskTimeline(context.Background(), workspace.ID, assigned.Task.ID)
	if err != nil || len(timeline.Runs) != 1 || len(timeline.Entries) != 7 {
		t.Fatalf("TaskTimeline() = %#v, %v", timeline, err)
	}
	listedPage, err := storage.ListRuns(context.Background(), ListRunsQuery{WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, Status: domain.RunCompleted})
	listed := listedPage.Runs
	if err != nil || len(listed) != 1 || listed[0].ID != starting.ID {
		t.Fatalf("Runs() = %#v, %v", listed, err)
	}
}

func TestM23RunStartCannotEscapeItsWorkstreamPrimaryCheckout(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	inspection, err := storage.InspectProject(ctx, workspace.ID, project.ID)
	if err != nil || len(inspection.Checkouts) != 1 {
		t.Fatalf("InspectProject() = %#v, %v", inspection, err)
	}
	primary := inspection.Checkouts[0]
	adjacent, err := storage.AddCheckout(ctx, AddCheckoutCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, WriteMode: domain.WriteModeExclusive,
		IdempotencyKey: "m23-adjacent-checkout", CorrelationID: "m23-adjacent-checkout",
		Observation: sourceTestObservation(filepath.Join(t.TempDir(), "adjacent"), "adjacent"),
	})
	if err != nil {
		t.Fatal(err)
	}
	objective, err := storage.CreateObjective(ctx, CreateObjectiveCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, PrimaryCheckoutID: primary.ID,
		Title: "Bound workstream", IdempotencyKey: "m23-bound-workstream", CorrelationID: "m23-bound-workstream",
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := storage.CreateAgent(ctx, CreateAgentCommand{
		WorkspaceIdentifier: workspace.ID, Name: "bound-worker", Role: "implementer", Provider: "fake", Runtime: "fake",
		IdempotencyKey: "m23-bound-worker", CorrelationID: "m23-bound-worker",
	})
	if err != nil {
		t.Fatal(err)
	}
	task, err := storage.CreateTask(ctx, CreateTaskCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ObjectiveID: objective.Value.ID,
		Title: "Work in the persistent checkout", Priority: 100,
		IdempotencyKey: "m23-bound-task", CorrelationID: "m23-bound-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	assigned, err := storage.AssignTask(ctx, AssignTaskCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: task.Detail.Task.ID, AgentIdentifier: agent.Value.ID,
		LeaseSeconds: 300, ExpectedRevision: task.Detail.Task.Revision,
		IdempotencyKey: "m23-bound-assignment", CorrelationID: "m23-bound-assignment",
	})
	if err != nil {
		t.Fatal(err)
	}
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "m23-bound-run", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "working"}}}
	wrong := CreateRunCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: assigned.Detail.Task.ID, CheckoutIdentifier: adjacent.Checkout.ID,
		Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: assigned.Detail.Task.Revision,
		IdempotencyKey: "m23-wrong-checkout", CorrelationID: "m23-wrong-checkout",
	}
	if _, err := storage.CreateRun(ctx, wrong); ErrorCode(err) != CodePlacementUnavailable {
		t.Fatalf("CreateRun(adjacent) error = %v, code = %q", err, ErrorCode(err))
	}
	correct := wrong
	correct.CheckoutIdentifier = primary.ID
	correct.IdempotencyKey = "m23-primary-checkout"
	correct.CorrelationID = "m23-primary-checkout"
	created, err := storage.CreateRun(ctx, correct)
	if err != nil {
		t.Fatal(err)
	}
	if created.Detail.Run.CheckoutID != primary.ID || created.Detail.Checkout.ID != primary.ID {
		t.Fatalf("CreateRun(primary) = %#v, want checkout %s", created.Detail, primary.ID)
	}
}

func TestRunPlacementEnforcesAgentConcurrency(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project, agent, _, firstTask := initializeRunTest(t, storage, "first concurrent run")
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "concurrency", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "holding capacity", WaitForResume: true}}}
	first := createRunTest(t, storage, workspace.ID, firstTask, scenario, "start-first-concurrent-run")
	if first.Run.Status != domain.RunRequested {
		t.Fatalf("first run = %#v", first.Run)
	}
	secondTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "second concurrent run", "second-concurrent-task")
	assigned, err := storage.AssignTask(context.Background(), AssignTaskCommand{WorkspaceIdentifier: workspace.ID, TaskID: secondTask.Task.ID, AgentIdentifier: agent.ID, LeaseSeconds: 300, ExpectedRevision: secondTask.Task.Revision, IdempotencyKey: "assign-second-concurrent-task", CorrelationID: "request-assign-second-concurrent-task"})
	if err != nil {
		t.Fatalf("AssignTask(second) error = %v", err)
	}
	_, err = storage.CreateRun(context.Background(), CreateRunCommand{WorkspaceIdentifier: workspace.ID, TaskID: assigned.Detail.Task.ID, Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: assigned.Detail.Task.Revision, IdempotencyKey: "start-second-concurrent-run", CorrelationID: "request-start-second-concurrent-run"})
	if ErrorCode(err) != CodePlacementUnavailable {
		t.Fatalf("CreateRun(over concurrency) error = %v, code = %q", err, ErrorCode(err))
	}
	if _, err := storage.ListRuns(context.Background(), ListRunsQuery{WorkspaceIdentifier: workspace.ID, Status: "unknown"}); ErrorCode(err) != CodeInvalidRun {
		t.Fatalf("Runs(unknown status) error = %v, code = %q", err, ErrorCode(err))
	}
}

func TestRunJobLanesSeparateLaunchFromLiveControl(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project, agent, _, firstTask := initializeRunTest(t, storage, "lane control run")
	maximum := 2
	if _, err := storage.UpdateAgent(context.Background(), UpdateAgentCommand{
		WorkspaceIdentifier: workspace.ID,
		AgentIdentifier:     agent.ID,
		MaxConcurrency:      &maximum,
		ExpectedRevision:    agent.Revision,
		IdempotencyKey:      "raise-run-lane-capacity",
		CorrelationID:       "request-raise-run-lane-capacity",
	}); err != nil {
		t.Fatalf("UpdateAgent(max concurrency) error = %v", err)
	}
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "run-job-lanes", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "working"}}}
	controlRun := createRunTest(t, storage, workspace.ID, firstTask, scenario, "start-run-lane-control")
	starting, err := storage.MarkRunStarting(context.Background(), controlRun.Run.ID, "run-lane-starting")
	if err != nil {
		t.Fatalf("MarkRunStarting(control) error = %v", err)
	}
	if _, err := storage.MarkRunStarted(context.Background(), starting.ID, "run-lane-runtime", "run-lane-provider", "run-lane-started"); err != nil {
		t.Fatalf("MarkRunStarted(control) error = %v", err)
	}

	launchCheckout, err := storage.AddCheckout(context.Background(), AddCheckoutCommand{
		WorkspaceIdentifier: workspace.ID,
		ProjectIdentifier:   project.ID,
		WriteMode:           domain.WriteModeExclusive,
		IdempotencyKey:      "run-lane-launch-checkout",
		CorrelationID:       "request-run-lane-launch-checkout",
		Observation:         sourceTestObservation(filepath.Join(t.TempDir(), "run-lane-launch"), "run-lane-launch"),
	})
	if err != nil {
		t.Fatalf("AddCheckout(launch) error = %v", err)
	}
	launchTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "lane launch run", "run-lane-launch-task")
	assigned, err := storage.AssignTask(context.Background(), AssignTaskCommand{
		WorkspaceIdentifier: workspace.ID,
		TaskID:              launchTask.Task.ID,
		AgentIdentifier:     agent.ID,
		LeaseSeconds:        300,
		ExpectedRevision:    launchTask.Task.Revision,
		IdempotencyKey:      "assign-run-lane-launch",
		CorrelationID:       "request-assign-run-lane-launch",
	})
	if err != nil {
		t.Fatalf("AssignTask(launch) error = %v", err)
	}
	launchRun, err := storage.CreateRun(context.Background(), CreateRunCommand{
		WorkspaceIdentifier:  workspace.ID,
		TaskID:               assigned.Detail.Task.ID,
		CheckoutIdentifier:   launchCheckout.Checkout.ID,
		Runtime:              "fake",
		Provider:             "fake",
		Scenario:             scenario,
		ExpectedTaskRevision: assigned.Detail.Task.Revision,
		IdempotencyKey:       "start-run-lane-launch",
		CorrelationID:        "request-start-run-lane-launch",
	})
	if err != nil {
		t.Fatalf("CreateRun(launch) error = %v", err)
	}

	launchWork, found, err := storage.ClaimRunLaunchJob(context.Background(), time.Second)
	if err != nil || !found || launchWork.Run.ID != launchRun.Detail.Run.ID || launchWork.Run.Status != domain.RunRequested {
		t.Fatalf("ClaimRunLaunchJob() = %#v, %t, %v", launchWork, found, err)
	}
	controlWork, found, err := storage.ClaimRunControlJob(context.Background(), time.Second)
	if err != nil || !found || controlWork.Run.ID != controlRun.Run.ID || controlWork.Run.Status != domain.RunActive {
		t.Fatalf("ClaimRunControlJob() = %#v, %t, %v", controlWork, found, err)
	}
}

func TestRunIntentFailuresRollBackProjectionQueueTimelineEventAndIdempotency(t *testing.T) {
	t.Parallel()

	for _, stage := range []string{MutationAfterProjection, MutationAfterEvent} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			storage := openTestStore(t, t.TempDir(), Options{})
			workspace, _, _, _, assigned := initializeRunTest(t, storage, "atomic run "+stage)
			var eventsBefore, idempotencyBefore int
			if err := storage.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&eventsBefore); err != nil {
				t.Fatalf("count events before: %v", err)
			}
			if err := storage.db.QueryRow("SELECT COUNT(*) FROM idempotency_keys").Scan(&idempotencyBefore); err != nil {
				t.Fatalf("count idempotency before: %v", err)
			}
			injected := errors.New("injected run intent interruption")
			storage.mutationHook = func(current string) error {
				if current == stage {
					return injected
				}
				return nil
			}
			scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "atomic-run", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "not committed"}}}
			_, err := storage.CreateRun(context.Background(), CreateRunCommand{WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: assigned.Task.Revision, IdempotencyKey: "atomic-run-" + stage, CorrelationID: "request-atomic-run"})
			if !errors.Is(err, injected) {
				t.Fatalf("CreateRun() error = %v, want injected failure", err)
			}
			for _, table := range []string{"runs", "run_jobs", "run_timeline", "run_handoffs"} {
				var count int
				if err := storage.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("%s count = %d, %v, want 0", table, count, err)
				}
			}
			var eventsAfter, idempotencyAfter int
			_ = storage.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&eventsAfter)
			_ = storage.db.QueryRow("SELECT COUNT(*) FROM idempotency_keys").Scan(&idempotencyAfter)
			if eventsAfter != eventsBefore || idempotencyAfter != idempotencyBefore {
				t.Fatalf("counts after rollback events=%d idempotency=%d; want %d and %d", eventsAfter, idempotencyAfter, eventsBefore, idempotencyBefore)
			}
		})
	}
}

func TestBlockedRunResumesFromPersistedCursor(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "blocked run")
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "blocked-run", Steps: []domain.FakeStep{{Kind: domain.ObservationBlocked, Message: "Which behavior?"}, {Kind: domain.ObservationCompletion, Message: "done", Handoff: "review the chosen behavior"}}}
	created := createRunTest(t, storage, workspace.ID, assigned, scenario, "start-blocked-run")
	_, _ = storage.MarkRunStarting(context.Background(), created.Run.ID, "worker-starting")
	_, _ = storage.MarkRunStarted(context.Background(), created.Run.ID, "runtime", "provider", "worker-started")
	blocked, err := storage.ApplyRunObservation(context.Background(), created.Run.ID, domain.RunObservation{Kind: domain.ObservationBlocked, Message: "Which behavior?"}, true, nil, "worker-blocked")
	if err != nil || blocked.Run.Status != domain.RunBlocked || blocked.Task.Status != domain.TaskBlocked || blocked.Run.StepCursor != 1 {
		t.Fatalf("blocked observation = %#v, %v", blocked, err)
	}
	resumeCommand := ResumeRunCommand{WorkspaceIdentifier: workspace.ID, RunID: created.Run.ID, ExpectedRevision: blocked.Run.Revision, IdempotencyKey: "resume-blocked-run", CorrelationID: "request-resume-blocked-run"}
	resumed, err := storage.ResumeRun(context.Background(), resumeCommand)
	if err != nil || resumed.Detail.Run.Status != domain.RunActive || resumed.Detail.Run.StepCursor != 1 || resumed.Detail.Task.Status != domain.TaskActive {
		t.Fatalf("ResumeRun() = %#v, %v", resumed, err)
	}
	resumeCommand.CorrelationID = "request-resume-blocked-run-retry"
	resumeReplay, err := storage.ResumeRun(context.Background(), resumeCommand)
	if err != nil || !reflect.DeepEqual(resumeReplay, resumed) {
		t.Fatalf("ResumeRun(replay with new correlation) = %#v, %v; want %#v", resumeReplay, err, resumed)
	}
	work, found, err := storage.ClaimRunControlJob(context.Background(), time.Second)
	if err != nil || !found || work.Run.StepCursor != 1 || work.Scenario.Name != scenario.Name {
		t.Fatalf("ClaimRunControlJob() = %#v, %t, %v", work, found, err)
	}
}

func TestM23RunDetailCarriesTheExactStructuredBlocker(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "structured blocker")
	created := createRunTest(t, storage, workspace.ID, assigned, domain.FakeScenario{
		Schema: execution.FakeScenarioSchema,
		Name:   "structured-blocker",
		Steps:  []domain.FakeStep{{Kind: domain.ObservationBlocked, Message: "The predecessor review output is missing."}},
	}, "start-structured-blocker")
	if _, err := storage.MarkRunStarting(ctx, created.Run.ID, "structured-blocker-starting"); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.MarkRunStarted(ctx, created.Run.ID, "runtime", "provider", "structured-blocker-started"); err != nil {
		t.Fatal(err)
	}
	report, err := storage.SubmitRunReport(ctx, CreateRunReportCommand{
		RunID: created.Run.ID, Kind: domain.ObservationBlocked,
		Message:  "The predecessor review output is missing.",
		Evidence: []string{"task_review", "run_review"},
		Payload: map[string]any{
			"reason":   "The predecessor review output is missing.",
			"needs":    []string{"accepted review handoff", "review evidence references"},
			"severity": "blocking", "related_ids": []string{"task_review", "run_review"},
			"idempotency_key": "structured-blocker-report",
		},
		IdempotencyKey: "structured-blocker-report",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.ApplyQueuedRunReport(ctx, created.Run.ID, report.ID, true, nil, nil, "", "apply-structured-blocker"); err != nil {
		t.Fatal(err)
	}
	detail, err := storage.RunDetail(ctx, workspace.ID, created.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Blocker == nil || detail.Blocker.Reason != "The predecessor review output is missing." ||
		detail.Blocker.Severity != "blocking" ||
		strings.Join(detail.Blocker.Needs, ",") != "accepted review handoff,review evidence references" ||
		strings.Join(detail.Blocker.RelatedIDs, ",") != "task_review,run_review" {
		t.Fatalf("RunDetail().Blocker = %#v", detail.Blocker)
	}
}

func TestRejectedCompletionRequestsChangesAndRetainsAssignment(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "review run")
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "review-run", Acceptance: domain.AcceptanceRule{RequiredEvidence: []string{"tests_passed", "reviewed"}}, Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "proposed", Evidence: []string{"tests_passed"}, Handoff: "review remains"}}}
	created := createRunTest(t, storage, workspace.ID, assigned, scenario, "start-review-run")
	_, _ = storage.MarkRunStarting(context.Background(), created.Run.ID, "worker-starting")
	_, _ = storage.MarkRunStarted(context.Background(), created.Run.ID, "runtime", "provider", "worker-started")
	detail, err := storage.ApplyRunObservation(context.Background(), created.Run.ID, domain.RunObservation{Kind: domain.ObservationCompletion, Message: "proposed", Evidence: []string{"tests_passed"}, Handoff: "review remains", LogArchive: prepareTestRunLogArchive(t, storage, created.Run.ID)}, false, []string{"reviewed"}, "worker-review")
	if err != nil || detail.Run.Status != domain.RunReview || detail.Task.Status != domain.TaskChangesRequested || detail.Task.AssignmentID == "" || detail.Handoff != nil {
		t.Fatalf("rejected completion = %#v, %v", detail, err)
	}
	retryCommand := RetryReviewedRunCommand{
		WorkspaceIdentifier: workspace.ID, PriorRunID: detail.Run.ID,
		ExpectedRunRevision: detail.Run.Revision, ExpectedTaskRevision: detail.Task.Revision,
		Scenario:       domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "review-retry", Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "retry"}}},
		IdempotencyKey: "retry-reviewed-run", CorrelationID: "request-retry-reviewed-run",
	}
	retried, err := storage.RetryReviewedRun(context.Background(), retryCommand)
	if err != nil || retried.Detail.Run.ID == detail.Run.ID || retried.Detail.Run.Status != domain.RunRequested || retried.Detail.Task.Status != domain.TaskAssigned || retried.Detail.Task.AssignmentID != detail.Task.AssignmentID {
		t.Fatalf("RetryReviewedRun() = %#v, %v", retried, err)
	}
	retryCommand.CorrelationID = "request-retry-reviewed-run-replay"
	replay, err := storage.RetryReviewedRun(context.Background(), retryCommand)
	if err != nil || !reflect.DeepEqual(replay, retried) {
		t.Fatalf("RetryReviewedRun(replay) = %#v, %v; want %#v", replay, err, retried)
	}
	var retriedEvents int
	if err := storage.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM events WHERE workspace_id=? AND ((entity_type='task' AND entity_id=? AND entity_revision=? AND type='task.assigned') OR (entity_type='run' AND entity_id=? AND type='run.requested'))`, workspace.ID, detail.Task.ID, retried.Detail.Task.Revision, retried.Detail.Run.ID).Scan(&retriedEvents); err != nil || retriedEvents != 2 {
		t.Fatalf("review retry events = %d, %v; want task assignment plus run request", retriedEvents, err)
	}
	var eventCountBefore int
	if err := storage.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM events WHERE workspace_id=?`, workspace.ID).Scan(&eventCountBefore); err != nil {
		t.Fatal(err)
	}
	conflictCommand := retryCommand
	conflictCommand.IdempotencyKey = "retry-reviewed-run-again"
	conflictCommand.ExpectedTaskRevision = retried.Detail.Task.Revision
	if _, err := storage.RetryReviewedRun(context.Background(), conflictCommand); ErrorCode(err) != CodeRunConflict {
		t.Fatalf("RetryReviewedRun(superseded review) error = %v, code %q; want %q", err, ErrorCode(err), CodeRunConflict)
	}
	var eventCountAfter int
	if err := storage.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM events WHERE workspace_id=?`, workspace.ID).Scan(&eventCountAfter); err != nil || eventCountAfter != eventCountBefore {
		t.Fatalf("superseded retry event count = %d, %v; want unchanged %d", eventCountAfter, err, eventCountBefore)
	}
}

func TestRunStopRetainsAssignmentAndRecordsForcedFallback(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "stopped run")
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "stopped-run", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "waiting"}}}
	created := createRunTest(t, storage, workspace.ID, assigned, scenario, "start-stopped-run")
	_, _ = storage.MarkRunStarting(context.Background(), created.Run.ID, "worker-starting")
	active, err := storage.MarkRunStarted(context.Background(), created.Run.ID, "runtime", "provider", "worker-started")
	if err != nil {
		t.Fatalf("MarkRunStarted() error = %v", err)
	}
	command := StopRunCommand{
		WorkspaceIdentifier: workspace.ID,
		RunID:               active.Run.ID,
		ExpectedRevision:    active.Run.Revision,
		GracePeriodMillis:   250,
		IdempotencyKey:      "stop-active-run",
		CorrelationID:       "request-stop-active-run",
	}
	requested, err := storage.RequestRunStop(context.Background(), command)
	if err != nil || requested.Detail.Run.Status != domain.RunStopping || requested.Detail.Run.StopGraceMillis != 250 {
		t.Fatalf("RequestRunStop() = %#v, %v", requested, err)
	}
	command.CorrelationID = "request-stop-active-run-retry"
	replayed, err := storage.RequestRunStop(context.Background(), command)
	if err != nil || !reflect.DeepEqual(requested, replayed) {
		t.Fatalf("RequestRunStop(replay) = %#v, %v; want %#v", replayed, err, requested)
	}
	stopped, err := storage.MarkRunStopped(context.Background(), active.Run.ID, true, "process ignored graceful stop and was force-killed", prepareTestRunLogArchive(t, storage, active.Run.ID), "", "worker-stopped")
	if err != nil {
		t.Fatalf("MarkRunStopped() error = %v", err)
	}
	if stopped.Run.Status != domain.RunStopped || !stopped.Run.StopForced || stopped.Run.StopGraceMillis != 0 || stopped.Task.Status != domain.TaskAssigned || stopped.Task.AssignmentID == "" {
		t.Fatalf("stopped detail = %#v", stopped)
	}
	if _, err := storage.CreateRun(context.Background(), CreateRunCommand{WorkspaceIdentifier: workspace.ID, TaskID: stopped.Task.ID, Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: stopped.Task.Revision, IdempotencyKey: "replacement-after-stop", CorrelationID: "request-replacement-after-stop"}); err != nil {
		t.Fatalf("CreateRun(replacement) error = %v", err)
	}
}

func TestLostRunRetainsAssignmentAndCheckoutCapacity(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "lost run")
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "lost-run", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "unknown"}}}
	created := createRunTest(t, storage, workspace.ID, assigned, scenario, "start-lost-run")
	if _, err := storage.MarkRunStarting(context.Background(), created.Run.ID, "worker-starting"); err != nil {
		t.Fatalf("MarkRunStarting() error = %v", err)
	}
	lost, err := storage.LoseRun(context.Background(), created.Run.ID, "supervisor disappeared before final state", "worker-lost")
	if err != nil {
		t.Fatalf("LoseRun() error = %v", err)
	}
	if lost.Run.Status != domain.RunLost || lost.Task.Status != domain.TaskBlocked || lost.Task.AssignmentID == "" || lost.Run.FinishedAt != "" {
		t.Fatalf("lost detail = %#v", lost)
	}
	if _, err := storage.CreateRun(context.Background(), CreateRunCommand{WorkspaceIdentifier: workspace.ID, TaskID: lost.Task.ID, Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: lost.Task.Revision, IdempotencyKey: "unsafe-replacement", CorrelationID: "request-unsafe-replacement"}); ErrorCode(err) != CodeRunConflict {
		t.Fatalf("CreateRun(after lost runtime) error = %v, code = %q", err, ErrorCode(err))
	}
}

func TestLostRunBoundsCanonicalProjectionAndTimelineDiagnostics(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "bounded lost run")
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "bounded-lost-run", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "unknown"}}}
	created := createRunTest(t, storage, workspace.ID, assigned, scenario, "start-bounded-lost-run")
	if _, err := storage.MarkRunStarting(context.Background(), created.Run.ID, "bounded-lost-starting"); err != nil {
		t.Fatal(err)
	}
	diagnostic := strings.Repeat("provider context mentions authentication and an unrelated command failed — ", 200)
	lost, err := storage.LoseRun(context.Background(), created.Run.ID, diagnostic, "bounded-lost")
	if err != nil {
		t.Fatal(err)
	}
	if length := len(lost.Run.FailureMessage); length == 0 || length > 1024 {
		t.Fatalf("lost failure message bytes = %d, want 1..1024", length)
	}
	if !strings.HasPrefix(diagnostic, lost.Run.FailureMessage) {
		t.Fatalf("lost failure message is not a bounded prefix: %q", lost.Run.FailureMessage)
	}
	for _, entry := range lost.Timeline {
		if len(entry.Message) > 4096 {
			t.Fatalf("timeline %q message bytes = %d, want <=4096", entry.Kind, len(entry.Message))
		}
	}
}

func TestResolveLostRunReleasesExecutionAuthorityButLeavesTaskBlocked(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project, _, checkout, assigned := initializeRunTest(t, storage, "resolved lost run")
	claim := addClaimTest(t, storage, AddClaimCommand{
		WorkspaceIdentifier: workspace.ID,
		ProjectIdentifier:   project.ID,
		TaskID:              assigned.Task.ID,
		CheckoutIdentifier:  checkout.ID,
		Kind:                domain.ClaimKindComponent,
		Target:              "world-engine",
		Mode:                domain.ClaimModeExclusive,
		ConflictPolicy:      domain.ClaimPolicyNotify,
		LeaseDuration:       time.Hour,
		IdempotencyKey:      "claim-resolved-lost-run",
		CorrelationID:       "request-claim-resolved-lost-run",
	})
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "resolved-lost-run", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "unknown"}}}
	created := createRunTest(t, storage, workspace.ID, assigned, scenario, "start-resolved-lost-run")
	starting, err := storage.MarkRunStarting(context.Background(), created.Run.ID, "resolved-lost-starting")
	if err != nil {
		t.Fatalf("MarkRunStarting() error = %v", err)
	}
	if _, err := storage.RecordRunRuntimeBinding(context.Background(), starting.ID, "node-bound-runtime-handle", "resolved-lost-binding"); err != nil {
		t.Fatalf("RecordRunRuntimeBinding() error = %v", err)
	}
	lost, err := storage.LoseRun(context.Background(), starting.ID, "runtime result is unknown", "resolved-lost")
	if err != nil {
		t.Fatalf("LoseRun() error = %v", err)
	}

	command := ResolveLostRunCommand{
		WorkspaceIdentifier:     workspace.ID,
		RunID:                   lost.Run.ID,
		ExpectedRevision:        lost.Run.Revision,
		Note:                    "owner verified the runtime was retired outside Crewfold",
		RuntimeRetiredConfirmed: true,
		IdempotencyKey:          "resolve-lost-run",
		CorrelationID:           "request-resolve-lost-run",
	}
	resolved, err := storage.ResolveLostRun(context.Background(), command)
	if err != nil {
		t.Fatalf("ResolveLostRun() error = %v", err)
	}
	if resolved.Detail.Run.Status != domain.RunFailed || resolved.Detail.Run.FailureCode != "runtime_retired_by_owner" ||
		resolved.Detail.Run.FinishedAt == "" || resolved.Detail.Run.RuntimeHandle != "" || resolved.Detail.Run.ProviderHandle != "" {
		t.Fatalf("resolved run = %#v", resolved.Detail.Run)
	}
	if resolved.Detail.Task.Status != domain.TaskBlocked || resolved.Detail.Task.AssignmentID != "" || resolved.Detail.Task.AssignedAgentID != "" {
		t.Fatalf("resolved task = %#v", resolved.Detail.Task)
	}
	if resolved.Resolution.RunID != lost.Run.ID || resolved.Resolution.LostRevision != lost.Run.Revision ||
		resolved.Resolution.Resolution != runLossResolutionOwnerConfirmed || resolved.Resolution.Note != command.Note ||
		resolved.Resolution.ResolvedBy != localOwnerActorID || resolved.Resolution.EventSequence != resolved.EventSequence ||
		resolved.Resolution.ResolvedAt != resolved.Detail.Run.FinishedAt {
		t.Fatalf("resolution receipt = %#v; result sequence=%d", resolved.Resolution, resolved.EventSequence)
	}

	var assignmentStatus, claimStatus, jobStatus string
	if err := storage.db.QueryRow("SELECT status FROM task_assignments WHERE id=?", lost.Run.AssignmentID).Scan(&assignmentStatus); err != nil {
		t.Fatalf("query assignment status: %v", err)
	}
	if err := storage.db.QueryRow("SELECT status FROM work_claims WHERE id=?", claim.Claim.ID).Scan(&claimStatus); err != nil {
		t.Fatalf("query claim status: %v", err)
	}
	if err := storage.db.QueryRow("SELECT status FROM run_jobs WHERE run_id=?", lost.Run.ID).Scan(&jobStatus); err != nil {
		t.Fatalf("query run job status: %v", err)
	}
	if assignmentStatus != "released" || claimStatus != domain.ClaimReleased || jobStatus != "complete" {
		t.Fatalf("released authority = assignment %q, claim %q, job %q", assignmentStatus, claimStatus, jobStatus)
	}
	var eventCount, taskFailedCount int
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM events WHERE entity_id=? AND type='run.lost_resolved'", lost.Run.ID).Scan(&eventCount); err != nil {
		t.Fatalf("count resolution events: %v", err)
	}
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM events WHERE entity_id=? AND type='task.failed'", lost.Task.ID).Scan(&taskFailedCount); err != nil {
		t.Fatalf("count task failure events: %v", err)
	}
	if eventCount != 1 || taskFailedCount != 0 {
		t.Fatalf("terminal events = run.lost_resolved %d, task.failed %d", eventCount, taskFailedCount)
	}

	command.CorrelationID = "request-resolve-lost-run-retry"
	replayed, err := storage.ResolveLostRun(context.Background(), command)
	if err != nil || !reflect.DeepEqual(replayed, resolved) {
		t.Fatalf("ResolveLostRun(replay) = %#v, %v; want %#v", replayed, err, resolved)
	}
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM events WHERE entity_id=? AND type='run.lost_resolved'", lost.Run.ID).Scan(&eventCount); err != nil || eventCount != 1 {
		t.Fatalf("resolution replay event count = %d, %v", eventCount, err)
	}
	conflict := command
	conflict.Note = "a semantically different retirement assertion"
	if _, err := storage.ResolveLostRun(context.Background(), conflict); ErrorCode(err) != CodeIdempotencyConflict {
		t.Fatalf("ResolveLostRun(changed semantic replay) error = %v, code = %q", err, ErrorCode(err))
	}
}

func TestResolveLostRunRequiresExactOwnerConfirmation(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, _, assigned := initializeRunTest(t, storage, "unconfirmed lost run")
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "unconfirmed-lost-run", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "unknown"}}}
	created := createRunTest(t, storage, workspace.ID, assigned, scenario, "start-unconfirmed-lost-run")
	if _, err := storage.MarkRunStarting(context.Background(), created.Run.ID, "unconfirmed-lost-starting"); err != nil {
		t.Fatalf("MarkRunStarting() error = %v", err)
	}
	lost, err := storage.LoseRun(context.Background(), created.Run.ID, "runtime result is unknown", "unconfirmed-lost")
	if err != nil {
		t.Fatalf("LoseRun() error = %v", err)
	}
	base := ResolveLostRunCommand{
		WorkspaceIdentifier: workspace.ID, RunID: lost.Run.ID, ExpectedRevision: lost.Run.Revision,
		Note: "runtime was retired", IdempotencyKey: "resolve-unconfirmed-lost", CorrelationID: "request-resolve-unconfirmed-lost",
	}
	if _, err := storage.ResolveLostRun(context.Background(), base); ErrorCode(err) != CodeInvalidRun {
		t.Fatalf("ResolveLostRun(without confirmation) error = %v, code = %q", err, ErrorCode(err))
	}
	base.RuntimeRetiredConfirmed = true
	base.ExpectedRevision--
	base.IdempotencyKey = "resolve-stale-lost"
	if _, err := storage.ResolveLostRun(context.Background(), base); ErrorCode(err) != CodeRevisionConflict {
		t.Fatalf("ResolveLostRun(stale revision) error = %v, code = %q", err, ErrorCode(err))
	}
	current, err := storage.RunDetail(context.Background(), workspace.ID, lost.Run.ID)
	if err != nil || current.Run.Status != domain.RunLost || current.Task.Status != domain.TaskBlocked || current.Task.AssignmentID == "" {
		t.Fatalf("RunDetail(after rejected resolutions) = %#v, %v", current, err)
	}
}

func initializeRunTest(t *testing.T, storage *Store, title string) (domain.Workspace, domain.Project, domain.AgentDefinition, domain.Checkout, domain.TaskDetail) {
	t.Helper()
	workspace, project := initializeWorkTestProject(t, storage)
	adjacent, err := storage.AddCheckout(context.Background(), AddCheckoutCommand{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, WriteMode: domain.WriteModeExclusive, IdempotencyKey: "adjacent-checkout-" + title, CorrelationID: "request-adjacent-checkout", Observation: sourceTestObservation(filepath.Join(t.TempDir(), "world-engine-2"), "agent-two")})
	if err != nil {
		t.Fatalf("AddCheckout(adjacent clone) error = %v", err)
	}
	agentResult, err := storage.CreateAgent(context.Background(), CreateAgentCommand{WorkspaceIdentifier: workspace.ID, Name: "implementer", Role: "implementer", Provider: "fake", Runtime: "fake", IdempotencyKey: "agent-" + title, CorrelationID: "request-agent"})
	if err != nil {
		t.Fatalf("CreateAgent() error = %v", err)
	}
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, title, "task-"+title)
	assigned, err := storage.AssignTask(context.Background(), AssignTaskCommand{WorkspaceIdentifier: workspace.ID, TaskID: task.Task.ID, AgentIdentifier: agentResult.Value.ID, LeaseSeconds: 300, ExpectedRevision: task.Task.Revision, IdempotencyKey: "assign-" + title, CorrelationID: "request-assign"})
	if err != nil {
		t.Fatalf("AssignTask() error = %v", err)
	}
	return workspace, project, agentResult.Value, adjacent.Checkout, assigned.Detail
}

func createRunTest(t *testing.T, storage *Store, workspaceID string, task domain.TaskDetail, scenario domain.FakeScenario, key string) domain.RunDetail {
	t.Helper()
	result, err := storage.CreateRun(context.Background(), CreateRunCommand{WorkspaceIdentifier: workspaceID, TaskID: task.Task.ID, Runtime: "fake", Provider: "fake", Scenario: scenario, ExpectedTaskRevision: task.Task.Revision, IdempotencyKey: key, CorrelationID: "request-" + key})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	return result.Detail
}
