package store_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/recovery"
	"crewfold/internal/store"
)

func TestM20LostRunBlocksBackupUntilExactOwnerResolution(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	if err := os.Chmod(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	storage, err := store.Open(ctx, dataDir, store.Options{
		RuntimeNodeID:          strings.Repeat("1", 32),
		RuntimeNodeFingerprint: strings.Repeat("2", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })

	workspace, err := storage.InitWorkspace(ctx, store.InitWorkspaceCommand{
		Name: "lost-backup", IdempotencyKey: "lost-backup-workspace", CorrelationID: "lost-backup-workspace",
	})
	if err != nil {
		t.Fatal(err)
	}
	checkoutRoot := filepath.Join(t.TempDir(), "checkout")
	project, err := storage.RegisterProject(ctx, store.RegisterProjectCommand{
		WorkspaceIdentifier: workspace.Workspace.ID,
		Name:                "lost-backup",
		WriteMode:           domain.WriteModeShared,
		IdempotencyKey:      "lost-backup-project",
		CorrelationID:       "lost-backup-project",
		Observation: domain.CheckoutObservation{
			Path: checkoutRoot, Availability: domain.CheckoutAvailable, CheckoutKind: domain.CheckoutStandalone,
			Branch: "main", HeadCommit: strings.Repeat("2", 40), GitDir: filepath.Join(checkoutRoot, ".git"), GitCommonDir: filepath.Join(checkoutRoot, ".git"),
			Repository: domain.RepositoryObservation{
				Fingerprint: "git_" + strings.Repeat("1", 64), ObjectFormat: "sha1", RootCommits: []string{strings.Repeat("0", 40)},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := storage.CreateAgent(ctx, store.CreateAgentCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, Name: "lost-runner", Role: "descriptive only",
		Runtime: "fake", Provider: "fake", IdempotencyKey: "lost-backup-agent", CorrelationID: "lost-backup-agent",
	})
	if err != nil {
		t.Fatal(err)
	}
	createdTask, err := storage.CreateTask(ctx, store.CreateTaskCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, ProjectIdentifier: project.Project.ID,
		Title: "retire uncertain runtime", Priority: 100,
		IdempotencyKey: "lost-backup-task", CorrelationID: "lost-backup-task",
	})
	if err != nil {
		t.Fatal(err)
	}
	assigned, err := storage.AssignTask(ctx, store.AssignTaskCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, TaskID: createdTask.Detail.Task.ID, AgentIdentifier: agent.Value.ID,
		LeaseSeconds: 300, ExpectedRevision: createdTask.Detail.Task.Revision,
		IdempotencyKey: "lost-backup-assignment", CorrelationID: "lost-backup-assignment",
	})
	if err != nil {
		t.Fatal(err)
	}
	createdRun, err := storage.CreateRun(ctx, store.CreateRunCommand{
		WorkspaceIdentifier: workspace.Workspace.ID, TaskID: assigned.Detail.Task.ID, CheckoutIdentifier: project.Checkout.ID,
		Runtime: "fake", Provider: "fake",
		Scenario: domain.FakeScenario{
			Schema: execution.FakeScenarioSchema, Name: "lost-backup",
			Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "outcome will become unknown"}},
		},
		ExpectedTaskRevision: assigned.Detail.Task.Revision,
		IdempotencyKey:       "lost-backup-run", CorrelationID: "lost-backup-run",
	})
	if err != nil {
		t.Fatal(err)
	}
	starting, err := storage.MarkRunStarting(ctx, createdRun.Detail.Run.ID, "lost-backup-starting")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.RecordRunRuntimeBinding(ctx, starting.ID, "opaque-node-bound-handle", "lost-backup-bound"); err != nil {
		t.Fatal(err)
	}
	lost, err := storage.LoseRun(ctx, starting.ID, "runtime identity and outcome are unknown", "lost-backup-lost")
	if err != nil {
		t.Fatal(err)
	}
	if lost.Run.Status != domain.RunLost || lost.Task.Status != domain.TaskBlocked || lost.Run.RuntimeHandle == "" {
		t.Fatalf("lost run did not retain uncertain authority: %#v", lost)
	}

	beforeRefusal := lostBackupEventHighWater(t, storage, workspace.Workspace.ID)
	refusedTarget := filepath.Join(t.TempDir(), "must-not-publish")
	_, err = recovery.CreateBundle(ctx, storage, dataDir, refusedTarget, "lost-backup-refusal")
	if recovery.ErrorCode(err) != recovery.CodeBackupNotQuiescent {
		t.Fatalf("CreateBundle(lost) error = %v, code = %q", err, recovery.ErrorCode(err))
	}
	var recoveryError *recovery.Error
	if !errors.As(err, &recoveryError) || recoveryError.Quiescence == nil ||
		recoveryError.Quiescence.Counts.NonterminalRuns != 1 || recoveryError.Quiescence.Counts.RuntimeBindings != 1 {
		t.Fatalf("CreateBundle(lost) quiescence details = %#v", recoveryError)
	}
	if _, statErr := os.Lstat(refusedTarget); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("lost-run refusal published a target: %v", statErr)
	}
	if after := lostBackupEventHighWater(t, storage, workspace.Workspace.ID); after != beforeRefusal {
		t.Fatalf("lost-run backup refusal changed event high-water from %d to %d", beforeRefusal, after)
	}

	resolved, err := storage.ResolveLostRun(ctx, store.ResolveLostRunCommand{
		WorkspaceIdentifier:     workspace.Workspace.ID,
		RunID:                   lost.Run.ID,
		ExpectedRevision:        lost.Run.Revision,
		Note:                    "owner retired the runtime through its native control surface",
		RuntimeRetiredConfirmed: true,
		IdempotencyKey:          "lost-backup-resolve",
		CorrelationID:           "lost-backup-resolve",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Detail.Run.Status != domain.RunFailed || resolved.Detail.Run.FailureCode != "runtime_retired_by_owner" ||
		resolved.Detail.Run.RuntimeHandle != "" || resolved.Detail.Task.Status != domain.TaskBlocked || resolved.Detail.Task.AssignmentID != "" {
		t.Fatalf("resolved lost run retained authority or changed retry policy: %#v", resolved.Detail)
	}
	if resolved.EventSequence != beforeRefusal+1 {
		t.Fatalf("lost-run resolution event sequence = %d, want exactly one event after %d", resolved.EventSequence, beforeRefusal)
	}

	cutHighWater := lostBackupEventHighWater(t, storage, workspace.Workspace.ID)
	if cutHighWater != resolved.EventSequence {
		t.Fatalf("resolved event high-water = %d, resolution sequence = %d", cutHighWater, resolved.EventSequence)
	}
	publishedTarget := filepath.Join(t.TempDir(), "resolved-cut")
	created, err := recovery.CreateBundle(ctx, storage, dataDir, publishedTarget, "lost-backup-success")
	if err != nil {
		t.Fatalf("CreateBundle(after exact owner resolution) = %v", err)
	}
	if created.Manifest.EventHighWater != cutHighWater || !created.Integrity.Quiescence.Quiescent || created.Integrity.Quiescence.Counts.RuntimeBindings != 0 {
		t.Fatalf("resolved backup cut = %#v", created)
	}
	verified, err := recovery.VerifyBundle(ctx, publishedTarget)
	if err != nil || verified.ManifestSHA256 != created.ManifestSHA256 || verified.Manifest.EventHighWater != cutHighWater {
		t.Fatalf("VerifyBundle(resolved cut) = %#v, %v", verified, err)
	}
	if after := lostBackupEventHighWater(t, storage, workspace.Workspace.ID); after != cutHighWater {
		t.Fatalf("successful maintenance cut changed event high-water from %d to %d", cutHighWater, after)
	}
}

func lostBackupEventHighWater(t *testing.T, storage *store.Store, workspaceID string) int64 {
	t.Helper()
	page, err := storage.ListEvents(context.Background(), store.ListEventsQuery{WorkspaceIdentifier: workspaceID, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	return page.HighWater
}
