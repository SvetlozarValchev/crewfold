package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/store/dbgen"
)

func TestKnowledgeContradictionLifecycleDisputeAndCanonicalReplay(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "contradiction source", "contradiction-source")
	left := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "left", "conflictprobe", "left claim", "")
	right := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "right", "conflictprobe", "right claim", "")

	reportCommand := ReportKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, LeftRevisionID: right.Revision.ID, RightRevisionID: left.Revision.ID,
		ReportNote: "The accepted claims conflict", Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "report-conflict", CorrelationID: "request-report-conflict",
	}
	reported, err := storage.ReportKnowledgeContradiction(context.Background(), reportCommand)
	if err != nil {
		t.Fatal(err)
	}
	contradiction := reported.Detail.Contradiction
	if contradiction.Status != domain.KnowledgeContradictionProposed || contradiction.StateRevision != 1 ||
		contradiction.LeftRevisionID >= contradiction.RightRevisionID || reported.Detail.AuthorityCheckCount != 0 {
		t.Fatalf("reported contradiction = %#v", reported)
	}
	reportCommand.LeftRevisionID, reportCommand.RightRevisionID = left.Revision.ID, right.Revision.ID
	replayed, err := storage.ReportKnowledgeContradiction(context.Background(), reportCommand)
	if err != nil || replayed.Detail.Contradiction.ID != contradiction.ID || replayed.EventSequence != reported.EventSequence {
		t.Fatalf("canonical replay = %#v, %v", replayed, err)
	}
	proposedDispute, err := storage.KnowledgeRevisionDispute(context.Background(), workspace.ID, left.Revision.ID)
	if err != nil || proposedDispute.Disputed || proposedDispute.OpenContradictionCount != 0 || len(proposedDispute.OpenContradictionIDs) != 0 {
		t.Fatalf("proposed dispute = %#v, %v", proposedDispute, err)
	}

	confirmed, err := storage.ConfirmKnowledgeContradiction(context.Background(), DecideKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, ContradictionID: contradiction.ID, ExpectedStateRevision: 1,
		Note: "Owner confirms exact conflict", Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "confirm-conflict", CorrelationID: "request-confirm-conflict",
	})
	if err != nil {
		t.Fatal(err)
	}
	if confirmed.Detail.Contradiction.Status != domain.KnowledgeContradictionOpen ||
		confirmed.Detail.Contradiction.ConfirmEventSequence != confirmed.EventSequence ||
		confirmed.Detail.AuthorityCheckCount != 1 || len(confirmed.Detail.AuthorityChecks) != 1 ||
		confirmed.Detail.AuthorityChecks[0].EventSequence != confirmed.EventSequence {
		t.Fatalf("confirmed contradiction = %#v", confirmed)
	}
	dispute, err := storage.KnowledgeRevisionDispute(context.Background(), workspace.ID, left.Revision.ID)
	if err != nil || !dispute.Disputed || dispute.OpenContradictionCount != 1 || len(dispute.OpenContradictionIDs) != 1 || dispute.OpenContradictionIDs[0] != contradiction.ID {
		t.Fatalf("open dispute = %#v, %v", dispute, err)
	}

	indexBefore, err := storage.KnowledgeIndexStatus(context.Background(), workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	search, err := storage.SearchKnowledge(context.Background(), SearchKnowledgeQuery{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Query: "conflictprobe", Limit: 1,
	})
	if err != nil || len(search.Matches) != 0 {
		t.Fatalf("search disputed revisions = %#v, %v", search, err)
	}
	indexAfter, err := storage.KnowledgeIndexStatus(context.Background(), workspace.ID)
	if err != nil || indexAfter.Generation != indexBefore.Generation || indexAfter.SourceCount != indexBefore.SourceCount || indexAfter.SourceDigest != indexBefore.SourceDigest {
		t.Fatalf("index mutated by dispute: before=%#v after=%#v err=%v", indexBefore, indexAfter, err)
	}

	dismissed, err := storage.DismissKnowledgeContradiction(context.Background(), DecideKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, ContradictionID: contradiction.ID, ExpectedStateRevision: 2,
		Note: "False positive after owner review", Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "dismiss-conflict", CorrelationID: "request-dismiss-conflict",
	})
	if err != nil || dismissed.Detail.Contradiction.Status != domain.KnowledgeContradictionDismissed ||
		dismissed.Detail.Contradiction.DismissEventSequence != dismissed.EventSequence || dismissed.Detail.AuthorityCheckCount != 2 {
		t.Fatalf("dismissed contradiction = %#v, %v", dismissed, err)
	}
	dispute, err = storage.KnowledgeRevisionDispute(context.Background(), workspace.ID, left.Revision.ID)
	if err != nil || dispute.Disputed || dispute.OpenContradictionCount != 0 {
		t.Fatalf("closed dispute = %#v, %v", dispute, err)
	}
	list, err := storage.ListKnowledgeContradictionDetails(context.Background(), ListKnowledgeContradictionsQuery{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID,
	})
	if err != nil || len(list) != 0 {
		t.Fatalf("active list = %#v, %v", list, err)
	}
	list, err = storage.ListKnowledgeContradictionDetails(context.Background(), ListKnowledgeContradictionsQuery{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID,
		Status: domain.KnowledgeContradictionDismissed,
	})
	if err != nil || len(list) != 1 || list[0].Contradiction.ID != contradiction.ID {
		t.Fatalf("dismissed list = %#v, %v", list, err)
	}
}

func TestKnowledgeContradictionReportScopesAndBounds(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	firstTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "first scope", "contradiction-first-scope")
	secondTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "second scope", "contradiction-second-scope")
	broad := acceptedContradictionKnowledge(t, storage, workspace.ID, firstTask.Task.ID, "scope-broad", "broad", "broad", "")
	first := acceptedContradictionKnowledge(t, storage, workspace.ID, firstTask.Task.ID, "scope-first", "first", "first", firstTask.Task.ID)
	second := acceptedContradictionKnowledge(t, storage, workspace.ID, secondTask.Task.ID, "scope-second", "second", "second", secondTask.Task.ID)

	if _, err := storage.ReportKnowledgeContradiction(context.Background(), ReportKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, LeftRevisionID: broad.Revision.ID, RightRevisionID: first.Revision.ID,
		ReportNote: strings.Repeat("é", 1024), Actor: OwnerKnowledgeActor(), IdempotencyKey: "scope-intersect", CorrelationID: "request-scope-intersect",
	}); err != nil {
		t.Fatalf("broad+task report error = %v", err)
	}
	if _, err := storage.ReportKnowledgeContradiction(context.Background(), ReportKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, LeftRevisionID: first.Revision.ID, RightRevisionID: second.Revision.ID,
		ReportNote: "disjoint", Actor: OwnerKnowledgeActor(), IdempotencyKey: "scope-disjoint", CorrelationID: "request-scope-disjoint",
	}); ErrorCode(err) != CodeKnowledgeConflict {
		t.Fatalf("disjoint report error = %v, code %q", err, ErrorCode(err))
	}
	if _, err := storage.ReportKnowledgeContradiction(context.Background(), ReportKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, LeftRevisionID: broad.Revision.ID, RightRevisionID: second.Revision.ID,
		ReportNote: strings.Repeat("x", 2049), Actor: OwnerKnowledgeActor(), IdempotencyKey: "oversized-note", CorrelationID: "request-oversized-note",
	}); ErrorCode(err) != CodeInvalidKnowledge {
		t.Fatalf("oversized report error = %v, code %q", err, ErrorCode(err))
	}
	if _, err := storage.ReportKnowledgeContradiction(context.Background(), ReportKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, LeftRevisionID: broad.Revision.ID, RightRevisionID: second.Revision.ID,
		ReportNote: "bad\x00note", Actor: OwnerKnowledgeActor(), IdempotencyKey: "nul-note", CorrelationID: "request-nul-note",
	}); ErrorCode(err) != CodeInvalidKnowledge {
		t.Fatalf("NUL report error = %v, code %q", err, ErrorCode(err))
	}
	if _, err := storage.ReportKnowledgeContradiction(context.Background(), ReportKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, LeftRevisionID: broad.Revision.ID, RightRevisionID: second.Revision.ID,
		ReportNote: string([]byte{0xff}), Actor: OwnerKnowledgeActor(), IdempotencyKey: "invalid-utf8-note",
		CorrelationID: "request-invalid-utf8-note",
	}); ErrorCode(err) != CodeInvalidKnowledge {
		t.Fatalf("invalid UTF-8 report error = %v, code %q", err, ErrorCode(err))
	}
}

func TestKnowledgeContradictionParticipantEligibilityMatrix(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	firstTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "matrix first", "contradiction-matrix-first")
	secondTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "matrix second", "contradiction-matrix-second")

	allowed := []struct {
		name       string
		leftScope  string
		rightScope string
	}{
		{name: "broad-broad"},
		{name: "broad-task", rightScope: firstTask.Task.ID},
		{name: "same-task", leftScope: firstTask.Task.ID, rightScope: firstTask.Task.ID},
	}
	for index, test := range allowed {
		left := acceptedContradictionKnowledge(t, storage, workspace.ID, firstTask.Task.ID,
			fmt.Sprintf("matrix-allowed-left-%d", index), "left", "left", test.leftScope)
		right := acceptedContradictionKnowledge(t, storage, workspace.ID, firstTask.Task.ID,
			fmt.Sprintf("matrix-allowed-right-%d", index), "right", "right", test.rightScope)
		if _, err := storage.ReportKnowledgeContradiction(context.Background(), ReportKnowledgeContradictionCommand{
			WorkspaceIdentifier: workspace.ID, LeftRevisionID: left.Revision.ID, RightRevisionID: right.Revision.ID,
			ReportNote: test.name, Actor: OwnerKnowledgeActor(), IdempotencyKey: "matrix-allowed-" + test.name,
			CorrelationID: "request-matrix-allowed-" + test.name,
		}); err != nil {
			t.Fatalf("allowed %s report error = %v", test.name, err)
		}
	}

	current := acceptedContradictionKnowledge(t, storage, workspace.ID, firstTask.Task.ID, "matrix-current", "current", "current", "")
	disjoint := acceptedContradictionKnowledge(t, storage, workspace.ID, secondTask.Task.ID, "matrix-disjoint", "disjoint", "disjoint", secondTask.Task.ID)
	firstScoped := acceptedContradictionKnowledge(t, storage, workspace.ID, firstTask.Task.ID, "matrix-first-scoped", "first", "first", firstTask.Task.ID)
	assertReportCode(t, storage, workspace.ID, firstScoped.Revision.ID, disjoint.Revision.ID, "matrix-disjoint", CodeKnowledgeConflict)

	proposed := proposeCustomSearchKnowledge(t, storage, ProposeKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, Type: domain.KnowledgeTypeFinding, Title: "proposed", Body: "proposed",
		Confidence: domain.KnowledgeConfidenceHigh, VerificationStatus: domain.KnowledgeVerificationVerified,
		FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded,
		Sources:         []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: firstTask.Task.ID, Role: domain.KnowledgeSourcePrimary}},
	}, "matrix-proposed")
	assertReportCode(t, storage, workspace.ID, current.Revision.ID, proposed.Revision.ID, "matrix-proposed", CodeKnowledgeConflict)

	rejectedProposal := proposeCustomSearchKnowledge(t, storage, ProposeKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, Type: domain.KnowledgeTypeFinding, Title: "rejected", Body: "rejected",
		Confidence: domain.KnowledgeConfidenceHigh, VerificationStatus: domain.KnowledgeVerificationVerified,
		FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded,
		Sources:         []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: firstTask.Task.ID, Role: domain.KnowledgeSourcePrimary}},
	}, "matrix-rejected")
	rejected, err := storage.RejectKnowledge(context.Background(), RejectKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: rejectedProposal.Revision.ID,
		ExpectedStateRevision: rejectedProposal.Revision.StateRevision, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "matrix-reject", CorrelationID: "request-matrix-reject",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertReportCode(t, storage, workspace.ID, current.Revision.ID, rejected.Revision.ID, "matrix-rejected", CodeKnowledgeConflict)

	stale := acceptedContradictionKnowledge(t, storage, workspace.ID, firstTask.Task.ID, "matrix-stale", "stale", "stale", "")
	stale, err = storage.MarkKnowledgeStale(context.Background(), MarkKnowledgeStaleCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: stale.Revision.ID, ExpectedStateRevision: stale.Revision.StateRevision,
		Reason: "stale", Actor: OwnerKnowledgeActor(), IdempotencyKey: "matrix-stale-mark", CorrelationID: "request-matrix-stale-mark",
	})
	if err != nil {
		t.Fatal(err)
	}
	assertReportCode(t, storage, workspace.ID, current.Revision.ID, stale.Revision.ID, "matrix-stale", CodeKnowledgeConflict)

	predecessor := acceptedContradictionKnowledge(t, storage, workspace.ID, firstTask.Task.ID, "matrix-predecessor", "old", "old", "")
	successorProposal := proposeCustomSearchKnowledge(t, storage, ProposeKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, Type: predecessor.Revision.Type, Title: "replacement", Body: "replacement",
		Confidence: predecessor.Revision.Confidence, VerificationStatus: predecessor.Revision.VerificationStatus,
		FreshnessPolicy: predecessor.Revision.FreshnessPolicy, SupersedesRevisionID: predecessor.Revision.ID,
		Sources: []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: firstTask.Task.ID, Role: domain.KnowledgeSourcePrimary}},
	}, "matrix-successor")
	successor := acceptSearchKnowledge(t, storage, workspace.ID, successorProposal, "matrix-successor")
	assertReportCode(t, storage, workspace.ID, current.Revision.ID, predecessor.Revision.ID, "matrix-superseded", CodeKnowledgeConflict)
	assertReportCode(t, storage, workspace.ID, predecessor.Revision.ID, successor.Revision.ID, "matrix-same-item", CodeKnowledgeConflict)

	otherProject, err := storage.RegisterProject(context.Background(), RegisterProjectCommand{
		WorkspaceIdentifier: workspace.ID, Name: "other-project", IdempotencyKey: "matrix-other-project",
		CorrelationID: "request-matrix-other-project", Observation: sourceTestObservation(t.TempDir()+"/other-project", "main"),
	})
	if err != nil {
		t.Fatal(err)
	}
	otherProjectTask := createWorkTestTask(t, storage, workspace.ID, otherProject.Project.ID, "other project", "matrix-other-project-task")
	otherProjectKnowledge := acceptedContradictionKnowledge(t, storage, workspace.ID, otherProjectTask.Task.ID, "matrix-other-project-knowledge", "other", "other", "")
	assertReportCode(t, storage, workspace.ID, current.Revision.ID, otherProjectKnowledge.Revision.ID, "matrix-cross-project", CodeKnowledgeConflict)

	otherWorkspace, err := storage.InitWorkspace(context.Background(), InitWorkspaceCommand{Name: "other-workspace", IdempotencyKey: "matrix-other-workspace", CorrelationID: "request-matrix-other-workspace"})
	if err != nil {
		t.Fatal(err)
	}
	otherWorkspaceProject, err := storage.RegisterProject(context.Background(), RegisterProjectCommand{
		WorkspaceIdentifier: otherWorkspace.Workspace.ID, Name: "other-workspace-project", IdempotencyKey: "matrix-other-workspace-project",
		CorrelationID: "request-matrix-other-workspace-project", Observation: sourceTestObservation(t.TempDir()+"/other-workspace", "main"),
	})
	if err != nil {
		t.Fatal(err)
	}
	otherWorkspaceTask := createWorkTestTask(t, storage, otherWorkspace.Workspace.ID, otherWorkspaceProject.Project.ID, "other workspace", "matrix-other-workspace-task")
	otherWorkspaceKnowledge := acceptedContradictionKnowledge(t, storage, otherWorkspace.Workspace.ID, otherWorkspaceTask.Task.ID, "matrix-other-workspace-knowledge", "other", "other", "")
	assertReportCode(t, storage, workspace.ID, current.Revision.ID, otherWorkspaceKnowledge.Revision.ID, "matrix-cross-workspace", CodeKnowledgeNotFound)
}

func TestKnowledgeContradictionConfirmRevalidatesParticipantsWithoutWrites(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "confirm revalidation", "contradiction-confirm-revalidation")
	left := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "revalidate-left", "left", "left", "")
	right := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "revalidate-right", "right", "right", "")
	reported, err := storage.ReportKnowledgeContradiction(context.Background(), ReportKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, LeftRevisionID: left.Revision.ID, RightRevisionID: right.Revision.ID,
		ReportNote: "candidate", Actor: OwnerKnowledgeActor(), IdempotencyKey: "revalidate-report", CorrelationID: "request-revalidate-report",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.MarkKnowledgeStale(context.Background(), MarkKnowledgeStaleCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: left.Revision.ID, ExpectedStateRevision: left.Revision.StateRevision,
		Reason: "changed before confirmation", Actor: OwnerKnowledgeActor(), IdempotencyKey: "revalidate-stale", CorrelationID: "request-revalidate-stale",
	}); err != nil {
		t.Fatal(err)
	}
	var eventsBefore, checksBefore, idempotencyBefore int
	_ = storage.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&eventsBefore)
	_ = storage.db.QueryRow("SELECT COUNT(*) FROM knowledge_contradiction_authority_checks").Scan(&checksBefore)
	_ = storage.db.QueryRow("SELECT COUNT(*) FROM idempotency_keys").Scan(&idempotencyBefore)
	if _, err := storage.ConfirmKnowledgeContradiction(context.Background(), DecideKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, ContradictionID: reported.Detail.Contradiction.ID, ExpectedStateRevision: 1,
		Actor: OwnerKnowledgeActor(), IdempotencyKey: "revalidate-confirm", CorrelationID: "request-revalidate-confirm",
	}); ErrorCode(err) != CodeContradictionConflict {
		t.Fatalf("confirm after stale error = %v, code %q", err, ErrorCode(err))
	}
	detail, err := storage.KnowledgeContradictionDetail(context.Background(), workspace.ID, reported.Detail.Contradiction.ID)
	if err != nil || detail.Contradiction.Status != domain.KnowledgeContradictionProposed || detail.Contradiction.StateRevision != 1 {
		t.Fatalf("candidate after failed confirm = %#v, %v", detail, err)
	}
	var eventsAfter, checksAfter, idempotencyAfter int
	_ = storage.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&eventsAfter)
	_ = storage.db.QueryRow("SELECT COUNT(*) FROM knowledge_contradiction_authority_checks").Scan(&checksAfter)
	_ = storage.db.QueryRow("SELECT COUNT(*) FROM idempotency_keys").Scan(&idempotencyAfter)
	if eventsAfter != eventsBefore || checksAfter != checksBefore || idempotencyAfter != idempotencyBefore {
		t.Fatalf("failed confirm wrote state events=%d/%d checks=%d/%d idempotency=%d/%d", eventsAfter, eventsBefore, checksAfter, checksBefore, idempotencyAfter, idempotencyBefore)
	}
}

func TestKnowledgeContradictionReportIdempotencyConflictsAreWriteFree(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "idempotency report", "contradiction-idempotency-report")
	left := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "idempotency-left", "left", "left", "")
	right := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "idempotency-right", "right", "right", "")
	command := ReportKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, LeftRevisionID: left.Revision.ID, RightRevisionID: right.Revision.ID,
		ReportNote: "original", Actor: OwnerKnowledgeActor(), IdempotencyKey: "idempotency-report-key", CorrelationID: "request-idempotency-report",
	}
	if _, err := storage.ReportKnowledgeContradiction(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	var eventsBefore int
	_ = storage.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&eventsBefore)
	changed := command
	changed.ReportNote = "changed"
	if _, err := storage.ReportKnowledgeContradiction(context.Background(), changed); ErrorCode(err) != CodeIdempotencyConflict {
		t.Fatalf("changed replay error = %v, code %q", err, ErrorCode(err))
	}
	for index, reverse := range []bool{false, true} {
		duplicate := command
		duplicate.IdempotencyKey = fmt.Sprintf("duplicate-pair-%d", index)
		duplicate.CorrelationID = fmt.Sprintf("request-duplicate-pair-%d", index)
		if reverse {
			duplicate.LeftRevisionID, duplicate.RightRevisionID = duplicate.RightRevisionID, duplicate.LeftRevisionID
		}
		if _, err := storage.ReportKnowledgeContradiction(context.Background(), duplicate); ErrorCode(err) != CodeContradictionConflict {
			t.Fatalf("duplicate pair reverse=%t error = %v, code %q", reverse, err, ErrorCode(err))
		}
	}
	var eventsAfter int
	_ = storage.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&eventsAfter)
	if eventsAfter != eventsBefore {
		t.Fatalf("conflicting reports appended events %d -> %d", eventsBefore, eventsAfter)
	}
}

func TestSearchExcludesDisputedBeforeLimitAndQuarantinesBroadParticipant(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	firstTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "search contradiction task", "search-contradiction-task")
	otherTask := createWorkTestTask(t, storage, workspace.ID, project.ID, "other search task", "search-contradiction-other")
	disputedBroad := acceptedContradictionKnowledge(t, storage, workspace.ID, firstTask.Task.ID, "search-broad", "quarantineprobe", "broad strongest", "")
	disputedScoped := acceptedContradictionKnowledge(t, storage, workspace.ID, firstTask.Task.ID, "search-scoped", "quarantineprobe", "scoped conflict", firstTask.Task.ID)
	safe := acceptedContradictionKnowledge(t, storage, workspace.ID, firstTask.Task.ID, "search-safe", "quarantineprobe", "safe fallback", "")
	openContradiction(t, storage, workspace.ID, disputedBroad.Revision.ID, disputedScoped.Revision.ID, "search-open")

	for name, taskID := range map[string]string{"project": "", "exact": firstTask.Task.ID, "unrelated": otherTask.Task.ID} {
		t.Run(name, func(t *testing.T) {
			result, err := storage.SearchKnowledge(context.Background(), SearchKnowledgeQuery{
				WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, TaskIdentifier: taskID,
				Query: "quarantineprobe", Limit: 1,
			})
			if err != nil || len(result.Matches) != 1 || result.Matches[0].Revision.ID != safe.Revision.ID {
				t.Fatalf("search(%s) = %#v, %v", name, result, err)
			}
		})
	}
}

func TestKnowledgeContradictionDenialIgnoresExpectedRevisionAndReplays(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, _, _, task := initializeRunTest(t, storage, "contradiction denial scope")
	left := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "denial-left", "left", "left", "")
	right := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "denial-right", "right", "right", "")
	reported, err := storage.ReportKnowledgeContradiction(context.Background(), ReportKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, LeftRevisionID: left.Revision.ID, RightRevisionID: right.Revision.ID,
		ReportNote: "reported", Actor: OwnerKnowledgeActor(), IdempotencyKey: "denial-report", CorrelationID: "request-denial-report",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := contradictionReporterRun(t, storage, workspace.ID, task)
	command := DecideKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, ContradictionID: reported.Detail.Contradiction.ID,
		ExpectedStateRevision: 999, Note: "unauthorized", Actor: domain.KnowledgeActor{ID: run.ID, Type: domain.KnowledgeActorAgentRun},
		IdempotencyKey: "agent-confirm-denial", CorrelationID: "request-agent-confirm-denial",
	}
	denied, err := storage.ConfirmKnowledgeContradiction(context.Background(), command)
	if ErrorCode(err) != CodeContradictionDenied || denied.AuthorityCheck == nil || denied.AuthorityCheck.Outcome != domain.KnowledgeAuthorityDenied ||
		denied.Detail.Contradiction.Status != domain.KnowledgeContradictionProposed || denied.Detail.AuthorityCheckCount != 1 {
		t.Fatalf("denied decision = %#v, %v", denied, err)
	}
	replayed, replayErr := storage.ConfirmKnowledgeContradiction(context.Background(), command)
	if ErrorCode(replayErr) != CodeContradictionDenied || replayed.AuthorityCheck == nil || replayed.AuthorityCheck.ID != denied.AuthorityCheck.ID {
		t.Fatalf("denial replay = %#v, %v", replayed, replayErr)
	}
	if _, err := storage.DismissKnowledgeContradiction(context.Background(), DecideKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, ContradictionID: reported.Detail.Contradiction.ID,
		ExpectedStateRevision: reported.Detail.Contradiction.StateRevision, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "owner-terminal-dismiss", CorrelationID: "request-owner-terminal-dismiss",
	}); err != nil {
		t.Fatal(err)
	}
	terminalCommand := command
	terminalCommand.IdempotencyKey = "agent-terminal-denial"
	terminalCommand.CorrelationID = "request-agent-terminal-denial"
	terminalDenied, terminalErr := storage.DismissKnowledgeContradiction(context.Background(), terminalCommand)
	if ErrorCode(terminalErr) != CodeContradictionDenied || terminalDenied.AuthorityCheck == nil ||
		terminalDenied.AuthorityCheck.Outcome != domain.KnowledgeAuthorityDenied ||
		terminalDenied.Detail.Contradiction.Status != domain.KnowledgeContradictionDismissed {
		t.Fatalf("terminal denial = %#v, %v", terminalDenied, terminalErr)
	}
}

func TestConcurrentKnowledgeContradictionConfirmationHasOneWinner(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "concurrent contradiction", "concurrent-contradiction")
	left := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "concurrent-left", "left", "left", "")
	right := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "concurrent-right", "right", "right", "")
	reported, err := storage.ReportKnowledgeContradiction(context.Background(), ReportKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, LeftRevisionID: left.Revision.ID, RightRevisionID: right.Revision.ID,
		ReportNote: "candidate", Actor: OwnerKnowledgeActor(), IdempotencyKey: "concurrent-report",
		CorrelationID: "request-concurrent-report",
	})
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errorsByCall := make([]error, 2)
	var wait sync.WaitGroup
	for index := range errorsByCall {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, errorsByCall[index] = storage.ConfirmKnowledgeContradiction(context.Background(), DecideKnowledgeContradictionCommand{
				WorkspaceIdentifier: workspace.ID, ContradictionID: reported.Detail.Contradiction.ID,
				ExpectedStateRevision: reported.Detail.Contradiction.StateRevision, Actor: OwnerKnowledgeActor(),
				IdempotencyKey: fmt.Sprintf("concurrent-confirm-%d", index),
				CorrelationID:  fmt.Sprintf("request-concurrent-confirm-%d", index),
			})
		}(index)
	}
	close(start)
	wait.Wait()
	successes, conflicts := 0, 0
	for _, callErr := range errorsByCall {
		if callErr == nil {
			successes++
		} else if ErrorCode(callErr) == CodeRevisionConflict {
			conflicts++
		} else {
			t.Fatalf("concurrent confirm error = %v, code %q", callErr, ErrorCode(callErr))
		}
	}
	detail, err := storage.KnowledgeContradictionDetail(context.Background(), workspace.ID, reported.Detail.Contradiction.ID)
	if successes != 1 || conflicts != 1 || err != nil || detail.Contradiction.Status != domain.KnowledgeContradictionOpen ||
		detail.Contradiction.StateRevision != 2 || detail.AuthorityCheckCount != 1 {
		t.Fatalf("concurrent confirms successes=%d conflicts=%d detail=%#v err=%v calls=%v",
			successes, conflicts, detail, err, errorsByCall)
	}
}

func TestConcurrentKnowledgeContradictionReportsHaveOneCanonicalWinner(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "concurrent reports", "concurrent-reports")
	left := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "concurrent-report-left", "left", "left", "")
	right := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "concurrent-report-right", "right", "right", "")

	start := make(chan struct{})
	errorsByCall := make([]error, 2)
	results := make([]KnowledgeContradictionMutationResult, 2)
	var wait sync.WaitGroup
	for index := range errorsByCall {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			leftID, rightID := left.Revision.ID, right.Revision.ID
			if index == 1 {
				leftID, rightID = rightID, leftID
			}
			results[index], errorsByCall[index] = storage.ReportKnowledgeContradiction(context.Background(), ReportKnowledgeContradictionCommand{
				WorkspaceIdentifier: workspace.ID, LeftRevisionID: leftID, RightRevisionID: rightID,
				ReportNote: "same canonical conflict", Actor: OwnerKnowledgeActor(),
				IdempotencyKey: fmt.Sprintf("concurrent-report-%d", index),
				CorrelationID:  fmt.Sprintf("request-concurrent-report-%d", index),
			})
		}(index)
	}
	close(start)
	wait.Wait()
	successes, conflicts := 0, 0
	for index, callErr := range errorsByCall {
		if callErr == nil {
			successes++
			if results[index].Detail.Contradiction.LeftRevisionID >= results[index].Detail.Contradiction.RightRevisionID {
				t.Fatalf("winning pair is not canonical: %#v", results[index].Detail.Contradiction)
			}
		} else if ErrorCode(callErr) == CodeContradictionConflict {
			conflicts++
		} else {
			t.Fatalf("concurrent report error = %v, code %q", callErr, ErrorCode(callErr))
		}
	}
	var contradictionCount, detectedEventCount int
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM knowledge_contradictions").Scan(&contradictionCount); err != nil {
		t.Fatal(err)
	}
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM events WHERE type=?", contradictionDetectedEvent).Scan(&detectedEventCount); err != nil {
		t.Fatal(err)
	}
	if successes != 1 || conflicts != 1 || contradictionCount != 1 || detectedEventCount != 1 {
		t.Fatalf("concurrent reports successes=%d conflicts=%d rows=%d events=%d calls=%v",
			successes, conflicts, contradictionCount, detectedEventCount, errorsByCall)
	}
}

func TestKnowledgeRevisionRemainsDisputedUntilLastOpenContradictionCloses(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "incremental close", "incremental-close")
	center := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "incremental-center", "center", "center", "")
	first := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "incremental-first", "first", "first", "")
	second := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "incremental-second", "second", "second", "")
	firstConflict := openContradiction(t, storage, workspace.ID, center.Revision.ID, first.Revision.ID, "incremental-first")
	secondConflict := openContradiction(t, storage, workspace.ID, center.Revision.ID, second.Revision.ID, "incremental-second")

	if _, err := storage.DismissKnowledgeContradiction(context.Background(), DecideKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, ContradictionID: firstConflict.ID,
		ExpectedStateRevision: firstConflict.StateRevision, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "dismiss-incremental-first", CorrelationID: "request-dismiss-incremental-first",
	}); err != nil {
		t.Fatal(err)
	}
	dispute, err := storage.KnowledgeRevisionDispute(context.Background(), workspace.ID, center.Revision.ID)
	if err != nil || !dispute.Disputed || dispute.OpenContradictionCount != 1 ||
		len(dispute.OpenContradictionIDs) != 1 || dispute.OpenContradictionIDs[0] != secondConflict.ID {
		t.Fatalf("dispute after first close = %#v, %v", dispute, err)
	}
	if _, err := storage.DismissKnowledgeContradiction(context.Background(), DecideKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, ContradictionID: secondConflict.ID,
		ExpectedStateRevision: secondConflict.StateRevision, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "dismiss-incremental-second", CorrelationID: "request-dismiss-incremental-second",
	}); err != nil {
		t.Fatal(err)
	}
	dispute, err = storage.KnowledgeRevisionDispute(context.Background(), workspace.ID, center.Revision.ID)
	if err != nil || dispute.Disputed || dispute.OpenContradictionCount != 0 || len(dispute.OpenContradictionIDs) != 0 {
		t.Fatalf("dispute after final close = %#v, %v", dispute, err)
	}
}

func TestKnowledgeTerminalTransitionResolvesAllOpenContradictionsAtomically(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{"commit", MutationAfterProjection, MutationAfterEvent} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			storage := openTestStore(t, t.TempDir(), Options{})
			workspace, project := initializeWorkTestProject(t, storage)
			task := createWorkTestTask(t, storage, workspace.ID, project.ID, "fanout source", "contradiction-fanout-source")
			center := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "fanout-center", "center", "center", "")
			first := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "fanout-first", "first", "first", "")
			second := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "fanout-second", "second", "second", "")
			firstConflict := openContradiction(t, storage, workspace.ID, center.Revision.ID, first.Revision.ID, "fanout-one")
			secondConflict := openContradiction(t, storage, workspace.ID, center.Revision.ID, second.Revision.ID, "fanout-two")

			if stage != "commit" {
				injected := errors.New("injected terminal contradiction interruption")
				storage.mutationHook = func(current string) error {
					if current == stage {
						return injected
					}
					return nil
				}
				_, err := storage.MarkKnowledgeStale(context.Background(), MarkKnowledgeStaleCommand{
					WorkspaceIdentifier: workspace.ID, RevisionID: center.Revision.ID,
					ExpectedStateRevision: center.Revision.StateRevision, Reason: "invalidated",
					Actor: OwnerKnowledgeActor(), IdempotencyKey: "stale-fanout-" + stage, CorrelationID: "request-stale-fanout-" + stage,
				})
				if !errors.Is(err, injected) {
					t.Fatalf("MarkKnowledgeStale error = %v, want injected", err)
				}
				storage.mutationHook = nil
				current, _ := storage.KnowledgeRevision(context.Background(), workspace.ID, center.Revision.ID)
				one, _ := storage.KnowledgeContradictionDetail(context.Background(), workspace.ID, firstConflict.ID)
				two, _ := storage.KnowledgeContradictionDetail(context.Background(), workspace.ID, secondConflict.ID)
				if current.CurrencyStatus != domain.KnowledgeCurrencyCurrent || one.Contradiction.Status != domain.KnowledgeContradictionOpen || two.Contradiction.Status != domain.KnowledgeContradictionOpen {
					t.Fatalf("rollback state: knowledge=%#v one=%#v two=%#v", current, one.Contradiction, two.Contradiction)
				}
				return
			}
			result, err := storage.MarkKnowledgeStale(context.Background(), MarkKnowledgeStaleCommand{
				WorkspaceIdentifier: workspace.ID, RevisionID: center.Revision.ID,
				ExpectedStateRevision: center.Revision.StateRevision, Reason: "invalidated",
				Actor: OwnerKnowledgeActor(), IdempotencyKey: "stale-fanout", CorrelationID: "request-stale-fanout",
			})
			if err != nil {
				t.Fatal(err)
			}
			one, _ := storage.KnowledgeContradictionDetail(context.Background(), workspace.ID, firstConflict.ID)
			two, _ := storage.KnowledgeContradictionDetail(context.Background(), workspace.ID, secondConflict.ID)
			for _, detail := range []domain.KnowledgeContradictionDetail{one, two} {
				if detail.Contradiction.Status != domain.KnowledgeContradictionResolved ||
					detail.Contradiction.ResolutionReason != ContradictionResolutionParticipantStale ||
					detail.Contradiction.ResolutionCauseEventSequence == 0 || detail.Contradiction.ResolutionEventSequence == 0 {
					t.Fatalf("resolved contradiction = %#v", detail.Contradiction)
				}
			}
			if result.EventSequence != two.Contradiction.ResolutionEventSequence && result.EventSequence != one.Contradiction.ResolutionEventSequence {
				t.Fatalf("result high-water = %d, resolutions %d/%d", result.EventSequence, one.Contradiction.ResolutionEventSequence, two.Contradiction.ResolutionEventSequence)
			}
			dispute, err := storage.KnowledgeRevisionDispute(context.Background(), workspace.ID, center.Revision.ID)
			if err != nil || dispute.Disputed || dispute.OpenContradictionCount != 0 {
				t.Fatalf("resolved dispute = %#v, %v", dispute, err)
			}
		})
	}
}

func TestKnowledgeSupersessionOrdersResolutionBeforeSuccessorAcceptance(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "supersession source", "contradiction-supersession-source")
	predecessor := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "supersession-predecessor", "old", "old", "")
	other := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "supersession-other", "other", "other", "")
	conflict := openContradiction(t, storage, workspace.ID, predecessor.Revision.ID, other.Revision.ID, "supersession-open")
	successor := proposeCustomSearchKnowledge(t, storage, ProposeKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, Type: domain.KnowledgeTypeFinding, Title: "new", Body: "new",
		Confidence: domain.KnowledgeConfidenceHigh, VerificationStatus: domain.KnowledgeVerificationVerified,
		FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded, SupersedesRevisionID: predecessor.Revision.ID,
		Sources: []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: task.Task.ID, Role: domain.KnowledgeSourcePrimary}},
	}, "contradiction-successor")
	accepted, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: successor.Revision.ID,
		ExpectedStateRevision: successor.Revision.StateRevision, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "accept-contradiction-successor", CorrelationID: "request-accept-contradiction-successor",
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := storage.KnowledgeContradictionDetail(context.Background(), workspace.ID, conflict.ID)
	if err != nil || detail.Contradiction.Status != domain.KnowledgeContradictionResolved ||
		detail.Contradiction.ResolutionReason != ContradictionResolutionParticipantSuperseded {
		t.Fatalf("superseded contradiction = %#v, %v", detail.Contradiction, err)
	}
	events := testWorkspaceEvents(t, storage, workspace.ID, 0, 1000)
	var supersededSequence, resolutionSequence, acceptedSequence int64
	for _, event := range events {
		switch {
		case event.Type == knowledgeSupersededEvent && event.Entity.ID == predecessor.Revision.ID:
			supersededSequence = event.Sequence
		case event.Type == contradictionResolvedEvent && event.Entity.ID == conflict.ID:
			resolutionSequence = event.Sequence
		case event.Type == knowledgeAcceptedEvent && event.Entity.ID == successor.Revision.ID:
			acceptedSequence = event.Sequence
		}
	}
	if supersededSequence == 0 || resolutionSequence == 0 || acceptedSequence == 0 ||
		!(supersededSequence < resolutionSequence && resolutionSequence < acceptedSequence) ||
		detail.Contradiction.ResolutionCauseEventSequence != supersededSequence ||
		detail.Contradiction.ResolutionEventSequence != resolutionSequence || accepted.EventSequence != acceptedSequence {
		t.Fatalf("event order superseded=%d resolution=%d accepted=%d detail=%#v result=%d",
			supersededSequence, resolutionSequence, acceptedSequence, detail.Contradiction, accepted.EventSequence)
	}
}

func TestKnowledgeContradictionEventPayloadCarriesDeltaSnapshot(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "event source", "contradiction-event-source")
	left := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "event-left", "left", "left", task.Task.ID)
	right := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "event-right", "right", "right", "")
	opened := openContradiction(t, storage, workspace.ID, left.Revision.ID, right.Revision.ID, "event-payload")

	events := testWorkspaceEvents(t, storage, workspace.ID, 0, 1000)
	for _, event := range events {
		if event.Entity.ID != opened.ID || (event.Type != contradictionDetectedEvent && event.Type != contradictionConfirmedEvent) {
			continue
		}
		var data map[string]any
		if err := json.Unmarshal(event.Data, &data); err != nil {
			t.Fatal(err)
		}
		for _, key := range []string{"project_id", "left_revision_id", "right_revision_id", "left_task_scope_id", "right_task_scope_id", "status", "state_revision"} {
			if _, exists := data[key]; !exists {
				t.Fatalf("event %s missing %s: %s", event.Type, key, event.Data)
			}
		}
	}
}

func TestDisputedEligibleContextFailsWithoutStateAndCanSucceedAfterClose(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, agent, adjacent, assigned := initializeRunTest(t, storage, "disputed context")
	left := acceptedContradictionKnowledge(t, storage, workspace.ID, assigned.Task.ID, "context-left", "left", "left", assigned.Task.ID)
	right := acceptedContradictionKnowledge(t, storage, workspace.ID, assigned.Task.ID, "context-right", "right", "right", "")
	conflict := openContradiction(t, storage, workspace.ID, left.Revision.ID, right.Revision.ID, "context-open")
	command := BuildContextCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, AgentIdentifier: agent.ID,
		CheckoutIdentifier: adjacent.ID, KnowledgeRevisionIDs: []string{left.Revision.ID},
		ExpectedTaskRevision: assigned.Task.Revision, IdempotencyKey: "disputed-context-key", CorrelationID: "request-disputed-context",
	}
	var packetsBefore, idempotencyBefore int
	_ = storage.db.QueryRow("SELECT COUNT(*) FROM context_packets").Scan(&packetsBefore)
	_ = storage.db.QueryRow("SELECT COUNT(*) FROM idempotency_keys").Scan(&idempotencyBefore)
	if _, err := storage.BuildContextPacket(context.Background(), command); ErrorCode(err) != CodeKnowledgeConflict || !strings.Contains(err.Error(), conflict.ID) {
		t.Fatalf("disputed context error = %v, code %q", err, ErrorCode(err))
	}
	var packetsAfter, idempotencyAfter int
	_ = storage.db.QueryRow("SELECT COUNT(*) FROM context_packets").Scan(&packetsAfter)
	_ = storage.db.QueryRow("SELECT COUNT(*) FROM idempotency_keys").Scan(&idempotencyAfter)
	if packetsAfter != packetsBefore || idempotencyAfter != idempotencyBefore {
		t.Fatalf("failed context persisted state packets=%d/%d idempotency=%d/%d", packetsAfter, packetsBefore, idempotencyAfter, idempotencyBefore)
	}
	if _, err := storage.DismissKnowledgeContradiction(context.Background(), DecideKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, ContradictionID: conflict.ID, ExpectedStateRevision: conflict.StateRevision,
		Actor: OwnerKnowledgeActor(), IdempotencyKey: "dismiss-context-open", CorrelationID: "request-dismiss-context-open",
	}); err != nil {
		t.Fatal(err)
	}
	if built, err := storage.BuildContextPacket(context.Background(), command); err != nil || len(built.Value.AcceptedKnowledge) != 1 || built.Value.AcceptedKnowledge[0].ID != left.Revision.ID {
		t.Fatalf("context after close = %#v, %v", built, err)
	}
}

func TestContextIneligibilityPrecedesDisputeIdentity(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project, agent, adjacent, assigned := initializeRunTest(t, storage, "dispute precedence")
	other := createWorkTestTask(t, storage, workspace.ID, project.ID, "other dispute task", "other-dispute-task")
	left := acceptedContradictionKnowledge(t, storage, workspace.ID, other.Task.ID, "precedence-left", "left", "left", other.Task.ID)
	right := acceptedContradictionKnowledge(t, storage, workspace.ID, other.Task.ID, "precedence-right", "right", "right", "")
	conflict := openContradiction(t, storage, workspace.ID, left.Revision.ID, right.Revision.ID, "precedence-open")
	command := BuildContextCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, AgentIdentifier: agent.ID,
		CheckoutIdentifier: adjacent.ID, KnowledgeRevisionIDs: []string{left.Revision.ID},
		ExpectedTaskRevision: assigned.Task.Revision, IdempotencyKey: "precedence-context", CorrelationID: "request-precedence-context",
	}
	built, err := storage.BuildContextPacket(context.Background(), command)
	if err != nil {
		t.Fatalf("out-of-scope disputed context leaked hard conflict %s: %v", conflict.ID, err)
	}
	if len(built.Value.AcceptedKnowledge) != 0 {
		t.Fatalf("out-of-scope disputed context accepted = %#v", built.Value.AcceptedKnowledge)
	}
	encoded, _ := json.Marshal(built.Value)
	if strings.Contains(string(encoded), conflict.ID) {
		t.Fatalf("out-of-scope packet leaked contradiction ID: %s", encoded)
	}
}

func TestContextLifecycleIneligibilityPrecedesDisputeIdentity(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, agent, adjacent, assigned := initializeRunTest(t, storage, "dispute lifecycle precedence")

	type candidate struct {
		name       string
		revisionID string
		conflictID string
		wantReason string
	}
	candidates := make([]candidate, 0, 4)
	for _, status := range []string{
		domain.KnowledgeReviewProposed,
		domain.KnowledgeReviewRejected,
		domain.KnowledgeCurrencyStale,
		domain.KnowledgeCurrencySuperseded,
	} {
		left := acceptedContradictionKnowledge(t, storage, workspace.ID, assigned.Task.ID,
			fmt.Sprintf("precedence-%s-left", status), status, status, "")
		right := acceptedContradictionKnowledge(t, storage, workspace.ID, assigned.Task.ID,
			fmt.Sprintf("precedence-%s-right", status), "counterclaim", "counterclaim", "")
		conflict := openContradiction(t, storage, workspace.ID, left.Revision.ID, right.Revision.ID,
			fmt.Sprintf("precedence-%s", status))
		candidates = append(candidates, candidate{
			name: status, revisionID: left.Revision.ID, conflictID: conflict.ID,
			wantReason: map[string]string{
				domain.KnowledgeReviewProposed:     "proposed",
				domain.KnowledgeReviewRejected:     "rejected",
				domain.KnowledgeCurrencyStale:      "stale",
				domain.KnowledgeCurrencySuperseded: "superseded",
			}[status],
		})
	}

	// These states cannot coexist through supported governance because stale and
	// superseded revisions atomically close incident contradictions. This seam
	// models an upgraded/tampered database so the context authority precedence is
	// independently executable rather than relying only on classifier unit tests.
	if _, err := storage.db.Exec("DROP TRIGGER knowledge_revision_reject_illegal_governance_update"); err != nil {
		t.Fatalf("drop governance trigger for precedence fixture: %v", err)
	}
	now := storage.nowText()
	for index, value := range candidates {
		var err error
		switch value.name {
		case domain.KnowledgeReviewProposed:
			_, err = storage.db.Exec(`UPDATE knowledge_revisions
SET review_status='proposed', currency_status='pending', state_revision=state_revision+1,
    accepted_at=NULL, accepted_by=NULL, accepted_by_type=NULL, decision_note=NULL
WHERE id=?`, value.revisionID)
		case domain.KnowledgeReviewRejected:
			_, err = storage.db.Exec(`UPDATE knowledge_revisions
SET review_status='rejected', currency_status='pending', state_revision=state_revision+1,
    accepted_at=NULL, accepted_by=NULL, accepted_by_type=NULL,
    rejected_at=?, rejected_by='local-owner', rejected_by_type='human', decision_note=NULL
WHERE id=?`, now, value.revisionID)
		case domain.KnowledgeCurrencyStale:
			_, err = storage.db.Exec(`UPDATE knowledge_revisions
SET currency_status='stale', state_revision=state_revision+1,
    stale_at=?, stale_by='local-owner', stale_by_type='human', stale_reason='precedence fixture'
WHERE id=?`, now, value.revisionID)
		case domain.KnowledgeCurrencySuperseded:
			_, err = storage.db.Exec(`UPDATE knowledge_revisions
SET currency_status='superseded', state_revision=state_revision+1 WHERE id=?`, value.revisionID)
			if err == nil {
				insertContextKnowledgeSuccessorFixture(t, storage, 9100+index, value.revisionID, assigned.Task.ID)
			}
		}
		if err != nil {
			t.Fatalf("force %s precedence fixture: %v", value.name, err)
		}
	}

	requested := make([]string, 0, len(candidates))
	for _, value := range candidates {
		requested = append(requested, value.revisionID)
	}
	built, err := storage.BuildContextPacket(context.Background(), BuildContextCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, AgentIdentifier: agent.ID,
		CheckoutIdentifier: adjacent.ID, KnowledgeRevisionIDs: requested, ExpectedTaskRevision: assigned.Task.Revision,
		IdempotencyKey: "lifecycle-precedence-context", CorrelationID: "request-lifecycle-precedence-context",
	})
	if err != nil {
		t.Fatalf("ineligible disputed context leaked hard conflict: %v", err)
	}
	if len(built.Value.AcceptedKnowledge) != 0 {
		t.Fatalf("ineligible disputed context accepted = %#v", built.Value.AcceptedKnowledge)
	}
	encoded, _ := json.Marshal(built.Value)
	for _, value := range candidates {
		if !hasContextKnowledgeExclusion(built.Value.Excluded, value.revisionID, value.wantReason) {
			t.Errorf("missing %s exclusion for %s: %#v", value.wantReason, value.revisionID, built.Value.Excluded)
		}
		if strings.Contains(string(encoded), value.conflictID) {
			t.Errorf("%s exclusion leaked contradiction ID %s: %s", value.name, value.conflictID, encoded)
		}
	}
}

func TestContextExpiredKnowledgePrecedesDisputeIdentity(t *testing.T) {
	t.Parallel()
	now := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	storage := openTestStore(t, t.TempDir(), Options{Clock: func() time.Time { return now }})
	workspace, _, agent, adjacent, assigned := initializeRunTest(t, storage, "expired dispute precedence")
	expiringProposal := proposeExpiringSearchKnowledge(t, storage, workspace.ID, assigned.Task.ID,
		"expired-precedence", now.Add(time.Hour).Format(time.RFC3339Nano))
	expiring := acceptSearchKnowledge(t, storage, workspace.ID, expiringProposal, "expired-precedence")
	right := acceptedContradictionKnowledge(t, storage, workspace.ID, assigned.Task.ID,
		"expired-precedence-right", "counterclaim", "counterclaim", "")
	conflict := openContradiction(t, storage, workspace.ID, expiring.Revision.ID, right.Revision.ID,
		"expired-precedence")
	now = now.Add(2 * time.Hour)

	built, err := storage.BuildContextPacket(context.Background(), BuildContextCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, AgentIdentifier: agent.ID,
		CheckoutIdentifier: adjacent.ID, KnowledgeRevisionIDs: []string{expiring.Revision.ID},
		ExpectedTaskRevision: assigned.Task.Revision, IdempotencyKey: "expired-precedence-context",
		CorrelationID: "request-expired-precedence-context",
	})
	if err != nil || !hasContextKnowledgeExclusion(built.Value.Excluded, expiring.Revision.ID, "stale") {
		t.Fatalf("expired disputed context = %#v, %v", built, err)
	}
	encoded, _ := json.Marshal(built.Value)
	if strings.Contains(string(encoded), conflict.ID) {
		t.Fatalf("expired context leaked contradiction ID %s: %s", conflict.ID, encoded)
	}
}

func TestContextDisputePrecedesKnowledgeByteBudget(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, agent, adjacent, assigned := initializeRunTest(t, storage, "dispute before byte budget")
	oversized := acceptedContradictionKnowledge(t, storage, workspace.ID, assigned.Task.ID,
		"budget-precedence-left", "large eligible claim", strings.Repeat("b", 13*1024), "")
	right := acceptedContradictionKnowledge(t, storage, workspace.ID, assigned.Task.ID,
		"budget-precedence-right", "counterclaim", "counterclaim", "")
	conflict := openContradiction(t, storage, workspace.ID, oversized.Revision.ID, right.Revision.ID,
		"budget-precedence")

	_, err := storage.BuildContextPacket(context.Background(), BuildContextCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, AgentIdentifier: agent.ID,
		CheckoutIdentifier: adjacent.ID, KnowledgeRevisionIDs: []string{oversized.Revision.ID},
		ExpectedTaskRevision: assigned.Task.Revision, IdempotencyKey: "budget-precedence-context",
		CorrelationID: "request-budget-precedence-context",
	})
	if ErrorCode(err) != CodeKnowledgeConflict || !strings.Contains(err.Error(), conflict.ID) || strings.Contains(err.Error(), "budget") {
		t.Fatalf("over-budget disputed context error = %v, code %q", err, ErrorCode(err))
	}
}

func TestContextConflictErrorIsSortedBoundedAndCountsUniqueContradictions(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _, agent, adjacent, assigned := initializeRunTest(t, storage, "bounded context conflicts")
	center := acceptedContradictionKnowledge(t, storage, workspace.ID, assigned.Task.ID,
		"bounded-context-center", "center", "center", "")
	conflictIDs := make([]string, 0, 17)
	participantIDs := make([]string, 0, 17)
	for index := 0; index < 17; index++ {
		participant := acceptedContradictionKnowledge(t, storage, workspace.ID, assigned.Task.ID,
			fmt.Sprintf("bounded-context-participant-%02d", index), "participant", fmt.Sprintf("participant %d", index), "")
		participantIDs = append(participantIDs, participant.Revision.ID)
		conflict := openContradiction(t, storage, workspace.ID, center.Revision.ID, participant.Revision.ID,
			fmt.Sprintf("bounded-context-%02d", index))
		conflictIDs = append(conflictIDs, conflict.ID)
	}
	sort.Strings(conflictIDs)

	_, err := storage.BuildContextPacket(context.Background(), BuildContextCommand{
		WorkspaceIdentifier: workspace.ID, TaskID: assigned.Task.ID, AgentIdentifier: agent.ID,
		CheckoutIdentifier: adjacent.ID,
		// The first conflict is incident to both requested revisions. It must be
		// counted once rather than once per pin.
		KnowledgeRevisionIDs: []string{center.Revision.ID, participantIDs[0]},
		ExpectedTaskRevision: assigned.Task.Revision, IdempotencyKey: "bounded-context-conflict",
		CorrelationID: "request-bounded-context-conflict",
	})
	want := "requested knowledge is disputed by open contradictions " + strings.Join(conflictIDs[:16], ", ") + " (+1 more)"
	if ErrorCode(err) != CodeKnowledgeConflict || err.Error() != want {
		t.Fatalf("bounded context conflict error = %v, code %q; want %q", err, ErrorCode(err), want)
	}
}

func TestKnowledgeContradictionDatabaseRejectsForgedLifecycle(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "forgery source", "contradiction-forgery-source")
	left := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "forgery-left", "left", "left", "")
	right := acceptedContradictionKnowledge(t, storage, workspace.ID, task.Task.ID, "forgery-right", "right", "right", "")
	rawTx, rawErr := storage.db.BeginTx(context.Background(), nil)
	if rawErr != nil {
		t.Fatal(rawErr)
	}
	rawID := "kcon_ffffffffffffffffffffffffffffffff"
	rawNow := storage.nowText()
	rawSequence, rawErr := appendEventForActor(context.Background(), rawTx, workspace.ID,
		"knowledge_contradiction", rawID, 1, contradictionDetectedEvent, "raw-invalid-utf8-report",
		rawNow, localOwnerActorID, localActorType, map[string]any{})
	if rawErr != nil {
		t.Fatal(rawErr)
	}
	rawLeft, rawRight := canonicalContradictionPair(left.Revision.ID, right.Revision.ID)
	if _, rawErr := rawTx.Exec(`INSERT INTO knowledge_contradictions(
id, workspace_id, project_id, left_revision_id, right_revision_id, status, state_revision,
report_note, reported_at, reported_by, reported_by_type, detected_event_sequence)
VALUES (?, ?, ?, ?, ?, 'proposed', 1, ?, ?, 'local-owner', 'human', ?)`,
		rawID, workspace.ID, project.ID, rawLeft, rawRight, string([]byte{0xff}), rawNow, rawSequence); rawErr == nil {
		t.Fatal("direct invalid UTF-8 contradiction report unexpectedly succeeded")
	}
	_ = rawTx.Rollback()

	reported, err := storage.ReportKnowledgeContradiction(context.Background(), ReportKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, LeftRevisionID: left.Revision.ID, RightRevisionID: right.Revision.ID,
		ReportNote: "reported", Actor: OwnerKnowledgeActor(), IdempotencyKey: "forgery-report", CorrelationID: "request-forgery-report",
	})
	if err != nil {
		t.Fatal(err)
	}
	contradiction := reported.Detail.Contradiction
	now := storage.nowText()
	if _, err := storage.db.Exec(`UPDATE knowledge_contradictions
SET id='kcon_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee' WHERE id=?`, contradiction.ID); err == nil {
		t.Fatal("direct contradiction identity rewrite unexpectedly succeeded")
	}
	if detail, err := storage.KnowledgeContradictionDetail(context.Background(), workspace.ID, contradiction.ID); err != nil ||
		detail.Contradiction.ID != contradiction.ID {
		t.Fatalf("contradiction after rejected identity rewrite = %#v, %v", detail, err)
	}
	if _, err := storage.db.Exec(`UPDATE knowledge_contradictions
SET status='open', state_revision=2, confirmed_at=?, confirmed_by='local-owner',
confirmed_by_type='human', confirm_event_sequence=detected_event_sequence WHERE id=?`, now, contradiction.ID); err == nil {
		t.Fatal("event-only direct contradiction confirmation unexpectedly succeeded")
	}

	tx, err := storage.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	sequence, err := appendEventForActor(context.Background(), tx, workspace.ID, "knowledge_contradiction", contradiction.ID,
		2, contradictionConfirmedEvent, "raw-confirm", now, localOwnerActorID, localActorType, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	queries := dbgen.New(tx)
	if _, err := insertKnowledgeContradictionAuthorityCheck(context.Background(), queries, workspace.ID, contradiction.ID,
		domain.KnowledgeContradictionAuthorityConfirm, OwnerKnowledgeActor(), domain.KnowledgeAuthorityAllowed,
		domain.KnowledgeAuthorityReasonOwner, string([]byte{0xff}), "raw-confirm-invalid-utf8",
		strings.Repeat("d", 64), sequence, now); err == nil {
		t.Fatal("direct invalid UTF-8 contradiction authority note unexpectedly succeeded")
	}
	if _, err := insertKnowledgeContradictionAuthorityCheck(context.Background(), queries, workspace.ID, contradiction.ID,
		domain.KnowledgeContradictionAuthorityConfirm, OwnerKnowledgeActor(), domain.KnowledgeAuthorityAllowed,
		domain.KnowledgeAuthorityReasonOwner, "ledger note", "raw-confirm", strings.Repeat("a", 64), sequence, now); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE knowledge_contradictions
SET status='open', state_revision=2, confirmed_at=?, confirmed_by='local-owner',
confirmed_by_type='human', confirm_note='different note', confirm_event_sequence=? WHERE id=?`, now, sequence, contradiction.ID); err == nil {
		t.Fatal("confirmation with mismatched authority note unexpectedly succeeded")
	}
	_ = tx.Rollback()

	confirmed, err := storage.ConfirmKnowledgeContradiction(context.Background(), DecideKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspace.ID, ContradictionID: contradiction.ID, ExpectedStateRevision: 1,
		Actor: OwnerKnowledgeActor(), IdempotencyKey: "forgery-confirm", CorrelationID: "request-forgery-confirm",
	})
	if err != nil {
		t.Fatal(err)
	}
	opened := confirmed.Detail.Contradiction
	tx, err = storage.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	queries = dbgen.New(tx)
	rows, err := queries.MarkKnowledgeRevisionStale(context.Background(), dbgen.MarkKnowledgeRevisionStaleParams{
		StaleAt: &now, StaleBy: ptr(localOwnerActorID), StaleByType: ptr(localActorType), StaleReason: ptr("raw invalidation"),
		ID: left.Revision.ID, ExpectedStateRevision: left.Revision.StateRevision,
	})
	if err != nil || rows != 1 {
		t.Fatalf("raw stale projection = %d, %v", rows, err)
	}
	causeSequence, err := appendEventForActor(context.Background(), tx, workspace.ID, "knowledge_revision", left.Revision.ID,
		left.Revision.StateRevision+1, knowledgeStaleEvent, "raw-stale", now, localOwnerActorID, localActorType, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insertKnowledgeAuthorityCheck(context.Background(), queries, workspace.ID, left.Revision.ID,
		domain.KnowledgeAuthorityMarkStale, OwnerKnowledgeActor(), domain.KnowledgeAuthorityAllowed,
		domain.KnowledgeAuthorityReasonOwner, "raw invalidation", "raw-stale", strings.Repeat("b", 64), causeSequence, now); err != nil {
		t.Fatal(err)
	}
	wrongActor := domain.KnowledgeActor{ID: "forged-owner", Type: domain.KnowledgeActorHuman}
	resolutionSequence, err := appendEventForActor(context.Background(), tx, workspace.ID, "knowledge_contradiction", opened.ID,
		opened.StateRevision+1, contradictionResolvedEvent, "raw-resolution", now, wrongActor.ID, wrongActor.Type, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	reason := ContradictionResolutionParticipantStale
	note := "knowledge revision " + left.Revision.ID + " became stale"
	if rows, err := queries.ResolveKnowledgeContradiction(context.Background(), dbgen.ResolveKnowledgeContradictionParams{
		ResolutionReason: &reason, ResolvedAt: &now, ResolvedBy: &wrongActor.ID, ResolvedByType: &wrongActor.Type,
		ResolutionNote: &note, ResolutionEventSequence: &resolutionSequence,
		ResolutionCauseEventSequence: &causeSequence, ID: opened.ID, ExpectedStateRevision: opened.StateRevision,
	}); err == nil || rows != 0 {
		t.Fatalf("wrong-actor raw resolution = rows %d, error %v; want trigger rejection", rows, err)
	}

	// A causal event/check for the wrong participant must not borrow the other
	// participant's terminal state. This was a subtle way to forge an apparently
	// causal resolution while leaving the named participant current.
	fakeCauseSequence, err := appendEventForActor(context.Background(), tx, workspace.ID, "knowledge_revision", right.Revision.ID,
		right.Revision.StateRevision, knowledgeStaleEvent, "raw-wrong-participant-cause", now,
		localOwnerActorID, localActorType, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insertKnowledgeAuthorityCheck(context.Background(), queries, workspace.ID, right.Revision.ID,
		domain.KnowledgeAuthorityMarkStale, OwnerKnowledgeActor(), domain.KnowledgeAuthorityAllowed,
		domain.KnowledgeAuthorityReasonOwner, "forged wrong participant cause", "raw-wrong-participant-cause",
		strings.Repeat("c", 64), fakeCauseSequence, now); err != nil {
		t.Fatal(err)
	}
	fakeResolutionSequence, err := appendEventForActor(context.Background(), tx, workspace.ID, "knowledge_contradiction", opened.ID,
		opened.StateRevision+1, contradictionResolvedEvent, "raw-wrong-participant-resolution", now,
		localOwnerActorID, localActorType, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	fakeNote := "knowledge revision " + right.Revision.ID + " became stale"
	if rows, err := queries.ResolveKnowledgeContradiction(context.Background(), dbgen.ResolveKnowledgeContradictionParams{
		ResolutionReason: &reason, ResolvedAt: &now, ResolvedBy: ptr(localOwnerActorID), ResolvedByType: ptr(localActorType),
		ResolutionNote: &fakeNote, ResolutionEventSequence: &fakeResolutionSequence,
		ResolutionCauseEventSequence: &fakeCauseSequence, ID: opened.ID, ExpectedStateRevision: opened.StateRevision,
	}); err == nil || rows != 0 {
		t.Fatalf("wrong-participant raw resolution = rows %d, error %v; want trigger rejection", rows, err)
	}

	// Even a self-consistent forged human across the participant projection,
	// causal event, authority row, and resolution event is not the local owner.
	forgedRows, err := queries.MarkKnowledgeRevisionStale(context.Background(), dbgen.MarkKnowledgeRevisionStaleParams{
		StaleAt: &now, StaleBy: &wrongActor.ID, StaleByType: &wrongActor.Type,
		StaleReason: ptr("forged non-owner invalidation"), ID: right.Revision.ID,
		ExpectedStateRevision: right.Revision.StateRevision,
	})
	if err != nil || forgedRows != 1 {
		t.Fatalf("forged stale projection = %d, %v", forgedRows, err)
	}
	forgedCauseSequence, err := appendEventForActor(context.Background(), tx, workspace.ID, "knowledge_revision", right.Revision.ID,
		right.Revision.StateRevision+1, knowledgeStaleEvent, "raw-forged-owner-cause", now,
		wrongActor.ID, wrongActor.Type, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := insertKnowledgeAuthorityCheck(context.Background(), queries, workspace.ID, right.Revision.ID,
		domain.KnowledgeAuthorityMarkStale, wrongActor, domain.KnowledgeAuthorityAllowed,
		domain.KnowledgeAuthorityReasonOwner, "forged non-owner invalidation", "raw-forged-owner-cause",
		strings.Repeat("e", 64), forgedCauseSequence, now); err != nil {
		t.Fatal(err)
	}
	forgedResolutionSequence, err := appendEventForActor(context.Background(), tx, workspace.ID, "knowledge_contradiction", opened.ID,
		opened.StateRevision+1, contradictionResolvedEvent, "raw-forged-owner-resolution", now,
		wrongActor.ID, wrongActor.Type, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	forgedNote := "knowledge revision " + right.Revision.ID + " became stale"
	if rows, err := queries.ResolveKnowledgeContradiction(context.Background(), dbgen.ResolveKnowledgeContradictionParams{
		ResolutionReason: &reason, ResolvedAt: &now, ResolvedBy: &wrongActor.ID, ResolvedByType: &wrongActor.Type,
		ResolutionNote: &forgedNote, ResolutionEventSequence: &forgedResolutionSequence,
		ResolutionCauseEventSequence: &forgedCauseSequence, ID: opened.ID, ExpectedStateRevision: opened.StateRevision,
	}); err == nil || rows != 0 {
		t.Fatalf("consistent forged-owner raw resolution = rows %d, error %v; want trigger rejection", rows, err)
	}
	_ = tx.Rollback()
}

func TestKnowledgeContradictionReadBoundsDiscloseExactCounts(t *testing.T) {
	t.Run("dispute IDs", func(t *testing.T) {
		storage := openTestStore(t, t.TempDir(), Options{})
		workspace, project := initializeWorkTestProject(t, storage)
		task := createWorkTestTask(t, storage, workspace.ID, project.ID, "bounded dispute source", "bounded-dispute-source")
		center := insertContextKnowledgeFixture(t, storage, contextKnowledgeFixture{
			ordinal: 1000, workspaceID: workspace.ID, projectID: project.ID, sourceTaskID: task.Task.ID,
			reviewStatus: domain.KnowledgeReviewAccepted, currencyStatus: domain.KnowledgeCurrencyCurrent, body: "center",
		})
		allIDs := make([]string, 0, 201)
		for index := 0; index < 201; index++ {
			other := insertContextKnowledgeFixture(t, storage, contextKnowledgeFixture{
				ordinal: 1001 + index, workspaceID: workspace.ID, projectID: project.ID, sourceTaskID: task.Task.ID,
				reviewStatus: domain.KnowledgeReviewAccepted, currencyStatus: domain.KnowledgeCurrencyCurrent,
				body: fmt.Sprintf("participant %d", index),
			})
			opened := openContradiction(t, storage, workspace.ID, center, other, fmt.Sprintf("bounded-%03d", index))
			allIDs = append(allIDs, opened.ID)
		}
		sort.Strings(allIDs)
		dispute, err := storage.KnowledgeRevisionDispute(context.Background(), workspace.ID, center)
		if err != nil || !dispute.Disputed || dispute.OpenContradictionCount != 201 || len(dispute.OpenContradictionIDs) != 200 {
			t.Fatalf("bounded dispute = %#v, %v", dispute, err)
		}
		for index := range dispute.OpenContradictionIDs {
			if dispute.OpenContradictionIDs[index] != allIDs[index] {
				t.Fatalf("bounded dispute IDs differ at %d: %s, want %s", index, dispute.OpenContradictionIDs[index], allIDs[index])
			}
		}
	})

	t.Run("authority checks", func(t *testing.T) {
		storage := openTestStore(t, t.TempDir(), Options{})
		workspace, _, _, _, assigned := initializeRunTest(t, storage, "bounded contradiction authority")
		run := contradictionReporterRun(t, storage, workspace.ID, assigned)
		left := acceptedContradictionKnowledge(t, storage, workspace.ID, assigned.Task.ID, "bounded-auth-left", "left", "left", "")
		right := acceptedContradictionKnowledge(t, storage, workspace.ID, assigned.Task.ID, "bounded-auth-right", "right", "right", "")
		reported, err := storage.ReportKnowledgeContradiction(context.Background(), ReportKnowledgeContradictionCommand{
			WorkspaceIdentifier: workspace.ID, LeftRevisionID: left.Revision.ID, RightRevisionID: right.Revision.ID,
			ReportNote: "reported", Actor: OwnerKnowledgeActor(), IdempotencyKey: "bounded-auth-report", CorrelationID: "request-bounded-auth-report",
		})
		if err != nil {
			t.Fatal(err)
		}
		for index := 0; index < 201; index++ {
			_, err := storage.ConfirmKnowledgeContradiction(context.Background(), DecideKnowledgeContradictionCommand{
				WorkspaceIdentifier: workspace.ID, ContradictionID: reported.Detail.Contradiction.ID,
				ExpectedStateRevision: 999, Actor: domain.KnowledgeActor{ID: run.ID, Type: domain.KnowledgeActorAgentRun},
				IdempotencyKey: fmt.Sprintf("bounded-auth-denial-%03d", index), CorrelationID: fmt.Sprintf("request-bounded-auth-denial-%03d", index),
			})
			if ErrorCode(err) != CodeContradictionDenied {
				t.Fatalf("denial %d error = %v, code %q", index, err, ErrorCode(err))
			}
		}
		detail, err := storage.KnowledgeContradictionDetail(context.Background(), workspace.ID, reported.Detail.Contradiction.ID)
		if err != nil || detail.AuthorityCheckCount != 201 || len(detail.AuthorityChecks) != 200 {
			t.Fatalf("bounded authority detail = %#v, %v", detail, err)
		}
		for index := 1; index < len(detail.AuthorityChecks); index++ {
			if detail.AuthorityChecks[index-1].EventSequence <= detail.AuthorityChecks[index].EventSequence {
				t.Fatalf("authority checks not newest first at %d: %d <= %d", index,
					detail.AuthorityChecks[index-1].EventSequence, detail.AuthorityChecks[index].EventSequence)
			}
		}
	})
}

func ptr[T any](value T) *T { return &value }

func assertReportCode(t *testing.T, storage *Store, workspaceID, leftRevisionID, rightRevisionID, key, wantCode string) {
	t.Helper()
	_, err := storage.ReportKnowledgeContradiction(context.Background(), ReportKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspaceID,
		LeftRevisionID:      leftRevisionID,
		RightRevisionID:     rightRevisionID,
		ReportNote:          key,
		Actor:               OwnerKnowledgeActor(),
		IdempotencyKey:      key,
		CorrelationID:       "request-" + key,
	})
	if ErrorCode(err) != wantCode {
		t.Fatalf("report %s error = %v, code %q; want %q", key, err, ErrorCode(err), wantCode)
	}
}

func acceptedContradictionKnowledge(t *testing.T, storage *Store, workspaceID, sourceTaskID, key, title, body, taskScopeID string) KnowledgeMutationResult {
	t.Helper()
	proposed := proposeCustomSearchKnowledge(t, storage, ProposeKnowledgeCommand{
		WorkspaceIdentifier: workspaceID, TaskScopeID: taskScopeID, Type: domain.KnowledgeTypeFinding,
		Title: title, Body: body, Confidence: domain.KnowledgeConfidenceHigh,
		VerificationStatus: domain.KnowledgeVerificationVerified, FreshnessPolicy: domain.KnowledgeFreshUntilSuperseded,
		Sources: []domain.KnowledgeSourceInput{{Type: domain.KnowledgeSourceTask, ID: sourceTaskID, Role: domain.KnowledgeSourcePrimary}},
	}, "contradiction-"+key)
	return acceptSearchKnowledge(t, storage, workspaceID, proposed, "contradiction-"+key)
}

func openContradiction(t *testing.T, storage *Store, workspaceID, leftID, rightID, key string) domain.KnowledgeContradiction {
	t.Helper()
	reported, err := storage.ReportKnowledgeContradiction(context.Background(), ReportKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspaceID, LeftRevisionID: leftID, RightRevisionID: rightID,
		ReportNote: "exact claims conflict", Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "report-" + key, CorrelationID: "request-report-" + key,
	})
	if err != nil {
		t.Fatal(err)
	}
	confirmed, err := storage.ConfirmKnowledgeContradiction(context.Background(), DecideKnowledgeContradictionCommand{
		WorkspaceIdentifier: workspaceID, ContradictionID: reported.Detail.Contradiction.ID,
		ExpectedStateRevision: reported.Detail.Contradiction.StateRevision, Actor: OwnerKnowledgeActor(),
		IdempotencyKey: "confirm-" + key, CorrelationID: "request-confirm-" + key,
	})
	if err != nil {
		t.Fatal(err)
	}
	return confirmed.Detail.Contradiction
}

func contradictionReporterRun(t *testing.T, storage *Store, workspaceID string, task domain.TaskDetail) domain.Run {
	t.Helper()
	assigned := createRunTest(t, storage, workspaceID, task, domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "contradiction-reporter", Steps: []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "done"}}}, "contradiction-reporter")
	return assigned.Run
}
