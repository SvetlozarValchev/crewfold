package store

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestM19EventPagesFreezeHighWaterAndBindEveryContinuation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 13, 0, 0, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return now }})
	created, err := storage.InitWorkspace(ctx, InitWorkspaceCommand{
		Name: "events", IdempotencyKey: "m19-events-workspace", CorrelationID: "m19-events-workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := created.Workspace.ID
	baseline, err := storage.ListEvents(ctx, ListEventsQuery{WorkspaceIdentifier: workspaceID, Limit: MaximumEventPageLimit})
	if err != nil {
		t.Fatal(err)
	}
	appendM19FixtureEvents(t, ctx, storage, workspaceID, 1001, 1)

	defaultPage, err := storage.ListEvents(ctx, ListEventsQuery{WorkspaceIdentifier: workspaceID, After: baseline.HighWater})
	if err != nil || len(defaultPage.Events) != DefaultReadPageLimit || !defaultPage.HasMore {
		t.Fatalf("default event page = len %d more %t, %v; want %d and more", len(defaultPage.Events), defaultPage.HasMore, err, DefaultReadPageLimit)
	}
	first, err := storage.ListEvents(ctx, ListEventsQuery{
		WorkspaceIdentifier: workspaceID, After: baseline.HighWater, Limit: MaximumEventPageLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.WorkspaceID != workspaceID || len(first.Events) != 1000 || !first.HasMore || first.NextCursor == "" || first.Total != 1001 {
		t.Fatalf("first 1,000-event page = %#v", first)
	}
	capturedHighWater := first.HighWater
	newSequence := appendM19FixtureEvents(t, ctx, storage, workspaceID, 1, 2000)
	second, err := storage.ListEvents(ctx, ListEventsQuery{
		WorkspaceIdentifier: workspaceID, After: baseline.HighWater, Cursor: first.NextCursor, Limit: MaximumEventPageLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.HighWater != capturedHighWater || second.Total != first.Total || len(second.Events) != 1 || second.Events[0].Sequence != capturedHighWater || second.HasMore || second.NextCursor != "" {
		t.Fatalf("frozen continuation = %#v, want one final event through H=%d", second, capturedHighWater)
	}
	newPage, err := storage.ListEvents(ctx, ListEventsQuery{WorkspaceIdentifier: workspaceID, After: capturedHighWater, Limit: 1})
	if err != nil || len(newPage.Events) != 1 || newPage.Events[0].Sequence != newSequence || newPage.HighWater != newSequence {
		t.Fatalf("post-fence page = %#v, %v, want new sequence %d", newPage, err, newSequence)
	}

	for name, query := range map[string]ListEventsQuery{
		"after mismatch":  {WorkspaceIdentifier: workspaceID, After: baseline.HighWater + 1, Cursor: first.NextCursor, Limit: MaximumEventPageLimit},
		"oversize cursor": {WorkspaceIdentifier: workspaceID, After: baseline.HighWater, Cursor: string(make([]byte, MaximumReadCursorBytes+1)), Limit: MaximumEventPageLimit},
	} {
		if _, err := storage.ListEvents(ctx, query); ErrorCode(err) != CodeInvalidCursor {
			t.Fatalf("%s error = %v, code %q; want invalid_cursor", name, err, ErrorCode(err))
		}
	}
	if _, err := storage.ListEvents(ctx, ListEventsQuery{WorkspaceIdentifier: workspaceID, Limit: MaximumEventPageLimit + 1}); ErrorCode(err) != CodeInvalidCursor {
		t.Fatalf("event limit overflow error = %v, code %q; want invalid_cursor", err, ErrorCode(err))
	}

	other, err := storage.InitWorkspace(ctx, InitWorkspaceCommand{
		Name: "other", IdempotencyKey: "m19-events-other", CorrelationID: "m19-events-other",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.ListEvents(ctx, ListEventsQuery{
		WorkspaceIdentifier: other.Workspace.ID, After: baseline.HighWater, Cursor: first.NextCursor, Limit: MaximumEventPageLimit,
	}); ErrorCode(err) != CodeInvalidCursor {
		t.Fatalf("cross-workspace event cursor error = %v, code %q; want invalid_cursor", err, ErrorCode(err))
	}

	timeline, err := storage.EventTimeline(ctx, EventTimelineQuery{
		WorkspaceIdentifier: workspaceID, EntityType: "task", EntityID: "task_m19_fixture", Limit: MaximumReadPageLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if timeline.WorkspaceID != workspaceID || len(timeline.Events) != MaximumReadPageLimit || !timeline.HasMore || timeline.NextCursor == "" || timeline.Total != 1002 || timeline.Events[0].Sequence != newSequence {
		t.Fatalf("reverse timeline first page = %#v", timeline)
	}
	timelineHighWater := timeline.HighWater
	latestSequence := appendM19FixtureEvents(t, ctx, storage, workspaceID, 1, 3000)
	timelineNext, err := storage.EventTimeline(ctx, EventTimelineQuery{
		WorkspaceIdentifier: workspaceID, EntityType: "task", EntityID: "task_m19_fixture",
		Cursor: timeline.NextCursor, Limit: MaximumReadPageLimit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if timelineNext.HighWater != timelineHighWater || len(timelineNext.Events) == 0 || timelineNext.Events[0].Sequence >= timeline.Events[len(timeline.Events)-1].Sequence {
		t.Fatalf("reverse timeline continuation escaped frozen order: first=%#v next=%#v", timeline, timelineNext)
	}
	if latestSequence <= timelineNext.HighWater {
		t.Fatalf("fixture did not create an event beyond frozen timeline H: latest=%d H=%d", latestSequence, timelineNext.HighWater)
	}
}

func TestM19EventHeadCannotAdvanceAcrossAnUnreturnedUnknownKind(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	created, err := storage.InitWorkspace(ctx, InitWorkspaceCommand{
		Name: "unknown-head", IdempotencyKey: "m19-unknown-head-workspace", CorrelationID: "m19-unknown-head-workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := created.Workspace.ID
	baseline, err := storage.ListEvents(ctx, ListEventsQuery{WorkspaceIdentifier: workspaceID, Limit: MaximumEventPageLimit})
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := storage.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	for index, eventType := range []string{"task.updated", "task.future_operator_fact", "task.updated"} {
		if _, err := appendEvent(ctx, transaction, workspaceID, "task", "task_m19_unknown_head", 1,
			eventType, fmt.Sprintf("m19-unknown-head-%d", index), storage.nowText(), map[string]any{"ordinal": index}); err != nil {
			t.Fatal(err)
		}
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}

	// The TUI's bounded head probe asks for one row but consumes the atomically
	// captured high-water. It must not receive a known first row and a high-water
	// that silently crosses an unknown row hidden later in the same frozen cut.
	page, err := storage.ListEvents(ctx, ListEventsQuery{
		WorkspaceIdentifier: workspaceID, After: baseline.HighWater, Limit: 1,
	})
	if ErrorCode(err) != CodeUnsupportedOperatorEvent || !reflect.DeepEqual(page, EventPage{}) {
		t.Fatalf("one-row event head = %#v, %v; want zero page and %s", page, err, CodeUnsupportedOperatorEvent)
	}
}

func appendM19FixtureEvents(t *testing.T, ctx context.Context, storage *Store, workspaceID string, count, ordinalBase int) int64 {
	t.Helper()
	transaction, err := storage.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	var sequence int64
	for index := 0; index < count; index++ {
		ordinal := ordinalBase + index
		sequence, err = appendEvent(ctx, transaction, workspaceID, "task", "task_m19_fixture", 1,
			"task.updated", fmt.Sprintf("m19-fixture-%d", ordinal), "2026-08-14T13:00:00Z", map[string]any{"ordinal": ordinal})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	return sequence
}

func TestM19OperatorReadsNeverReconcileElapsedLeasesOrAppendEvents(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return now }})
	workspace, project := initializeWorkTestProject(t, storage)
	checkout := claimTestCheckout(t, storage, workspace.ID, project.ID)
	agent, err := storage.CreateAgent(ctx, CreateAgentCommand{
		WorkspaceIdentifier: workspace.ID, Name: "operator-fixture", Role: "manager-owner-approver",
		Provider: "fake", IdempotencyKey: "m19-pure-agent", CorrelationID: "m19-pure-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	firstTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "elapsed assignment", "m19-pure-first-task")
	secondTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "elapsed claim", "m19-pure-second-task")
	if _, err := storage.AssignTask(ctx, AssignTaskCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: firstTask.Task.ID, AgentIdentifier: agent.Value.ID,
		LeaseSeconds: 60, ExpectedRevision: firstTask.Task.Revision,
		IdempotencyKey: "m19-pure-assignment", CorrelationID: "m19-pure-assignment",
	}); err != nil {
		t.Fatal(err)
	}
	firstClaim := addClaimTest(t, storage, AddClaimCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, TaskID: firstTask.Task.ID,
		CheckoutIdentifier: checkout.ID, Kind: domain.ClaimKindPath, Target: "src/operator/**",
		Mode: domain.ClaimModeExclusive, ConflictPolicy: domain.ClaimPolicyNotify, LeaseDuration: time.Minute,
		IdempotencyKey: "m19-pure-first-claim", CorrelationID: "m19-pure-first-claim",
	})
	secondClaim := addClaimTest(t, storage, AddClaimCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, TaskID: secondTask.Task.ID,
		CheckoutIdentifier: checkout.ID, Kind: domain.ClaimKindPath, Target: "src/operator/file.go",
		Mode: domain.ClaimModeExclusive, ConflictPolicy: domain.ClaimPolicyNotify, LeaseDuration: time.Minute,
		IdempotencyKey: "m19-pure-second-claim", CorrelationID: "m19-pure-second-claim",
	})
	if len(secondClaim.Overlaps) != 1 {
		t.Fatalf("fixture overlap count = %d, want 1", len(secondClaim.Overlaps))
	}

	// The records are elapsed according to the Store clock but deliberately not
	// reconciled. Every owner read must return the last committed projection and
	// leave the time-driven mutation to the daemon worker.
	now = now.Add(2 * time.Minute)
	before, err := storage.ListEvents(ctx, ListEventsQuery{WorkspaceIdentifier: workspace.ID, After: 0, Limit: MaximumEventPageLimit})
	if err != nil {
		t.Fatal(err)
	}

	workspaces, err := storage.ListWorkspaces(ctx, ListWorkspacesQuery{})
	if err != nil || workspaces.Total != 1 || len(workspaces.Workspaces) != 1 {
		t.Fatalf("ListWorkspaces() = %#v, %v", workspaces, err)
	}
	projects, err := storage.ListProjects(ctx, ListProjectsQuery{WorkspaceIdentifier: workspace.ID})
	if err != nil || projects.Total != 1 || len(projects.Projects) != 1 {
		t.Fatalf("ListProjects() = %#v, %v", projects, err)
	}
	shownProject, err := storage.Project(ctx, workspace.ID, project.ID)
	if err != nil || !reflect.DeepEqual(shownProject, project) {
		t.Fatalf("Project() = %#v, %v, want pure canonical project %#v", shownProject, err, project)
	}
	agents, err := storage.ListAgents(ctx, ListAgentsQuery{WorkspaceIdentifier: workspace.ID})
	if err != nil || agents.Total != 1 || len(agents.Agents) != 1 {
		t.Fatalf("ListAgents() = %#v, %v", agents, err)
	}
	if _, err := storage.ListObjectives(ctx, ListObjectivesQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID}); err != nil {
		t.Fatal(err)
	}
	tasks, err := storage.ListTasks(ctx, ListTasksQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID})
	if err != nil || tasks.Total != 2 || len(tasks.Tasks) != 2 {
		t.Fatalf("ListTasks() = %#v, %v", tasks, err)
	}
	assignedFound := false
	for _, task := range tasks.Tasks {
		if task.Task.ID == firstTask.Task.ID {
			assignedFound = task.Task.Status == domain.TaskAssigned && task.Assignment != nil
		}
	}
	if !assignedFound {
		t.Fatalf("elapsed assignment was mutated or hidden by a read: %#v", tasks.Tasks)
	}
	if detail, err := storage.TaskDetail(ctx, workspace.ID, firstTask.Task.ID); err != nil || detail.Task.Status != domain.TaskAssigned || detail.Assignment == nil {
		t.Fatalf("TaskDetail(elapsed) = %#v, %v", detail, err)
	}
	if status, err := storage.CoordinationStatus(ctx, workspace.ID); err != nil || status.TasksAssigned != 1 {
		t.Fatalf("CoordinationStatus(elapsed) = %#v, %v", status, err)
	}
	if _, err := storage.ListRuns(ctx, ListRunsQuery{WorkspaceIdentifier: workspace.ID}); err != nil {
		t.Fatal(err)
	}
	claims, err := storage.ListClaims(ctx, ListClaimsQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Status: domain.ClaimActive})
	if err != nil || claims.Total != 2 || len(claims.Claims) != 2 {
		t.Fatalf("ListClaims(active elapsed) = %#v, %v", claims, err)
	}
	if claims.Claims[0].Status != domain.ClaimActive || claims.Claims[1].Status != domain.ClaimActive {
		t.Fatalf("elapsed claims changed during read: %#v", claims.Claims)
	}
	overlaps, err := storage.ListOverlaps(ctx, ListOverlapsQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Status: domain.OverlapOpen})
	if err != nil || overlaps.Total != 1 || len(overlaps.Overlaps) != 1 {
		t.Fatalf("ListOverlaps(open elapsed) = %#v, %v", overlaps, err)
	}
	if overlap, err := storage.Overlap(ctx, workspace.ID, overlaps.Overlaps[0].ID); err != nil || overlap.Status != domain.OverlapOpen {
		t.Fatalf("Overlap(elapsed) = %#v, %v", overlap, err)
	}
	if _, err := storage.ListClaimDrifts(ctx, ListClaimDriftsQuery{WorkspaceIdentifier: workspace.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.ListMeetings(ctx, ListMeetingsQuery{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.EventTimeline(ctx, EventTimelineQuery{WorkspaceIdentifier: workspace.ID, EntityType: "task", EntityID: firstTask.Task.ID}); err != nil {
		t.Fatal(err)
	}

	after, err := storage.ListEvents(ctx, ListEventsQuery{WorkspaceIdentifier: workspace.ID, After: 0, Limit: MaximumEventPageLimit})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("operator reads changed the event journal:\nbefore %#v\nafter  %#v", before, after)
	}
	if firstClaim.Claim.Status != domain.ClaimActive || secondClaim.Claim.Status != domain.ClaimActive {
		t.Fatalf("fixture claim unexpectedly nonactive before worker: %#v %#v", firstClaim.Claim, secondClaim.Claim)
	}
}
