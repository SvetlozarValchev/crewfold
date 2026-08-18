package daemon

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func TestParticipantThreadLocalAPIMapsCompleteResultsAndStaleRevision(t *testing.T) {
	t.Parallel()
	config := testConfig(t)
	config.GitInspector = participantSurfaceInspector{}
	running := startTestServer(t, config)
	client := localapi.NewClient(running.config.SocketPath)
	if _, err := client.WorkspaceInit(context.Background(), "personal", "participant-surface-workspace"); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	consumerProject, err := client.ProjectAdd(context.Background(), "personal", "consumer", filepath.Join(root, "consumer"), domain.WriteModeShared, "participant-consumer-project")
	if err != nil {
		t.Fatal(err)
	}
	engineProject, err := client.ProjectAdd(context.Background(), "personal", "engine", filepath.Join(root, "engine"), domain.WriteModeShared, "participant-engine-project")
	if err != nil {
		t.Fatal(err)
	}
	createBinding := func(name, projectID string) (string, string) {
		t.Helper()
		agent, createErr := client.AgentCreate(context.Background(), localapi.AgentCreateParams{
			Workspace: "personal", Name: name, Role: "implementer", Provider: "fake", Runtime: "fake", MaxConcurrency: 2, IdempotencyKey: "participant-agent-" + name,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		task, createErr := client.TaskCreate(context.Background(), localapi.TaskCreateParams{
			Workspace: "personal", Project: projectID, Title: name + " task", Priority: 100, IdempotencyKey: "participant-task-" + name,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		assigned, createErr := client.TaskAssign(context.Background(), localapi.TaskAssignParams{
			Workspace: "personal", Task: task.Detail.Task.ID, Agent: agent.Agent.ID, LeaseSeconds: 300,
			ExpectedRevision: task.Detail.Task.Revision, IdempotencyKey: "participant-assignment-" + name,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		return agent.Agent.ID, assigned.Detail.Task.ID
	}
	consumerAgent, consumerTask := createBinding("consumer-agent", consumerProject.Project.ID)
	engineAgent, engineTask := createBinding("engine-agent", engineProject.Project.ID)
	reviewerAgent, reviewerTask := createBinding("reviewer-agent", consumerProject.Project.ID)

	created, err := client.ThreadCreate(context.Background(), localapi.ThreadCreateParams{
		Workspace: "personal", Subject: "Align engine contract",
		Participants:   []localapi.ThreadParticipantParams{{Agent: consumerAgent, Task: consumerTask}, {Agent: engineAgent, Task: engineTask}},
		IdempotencyKey: "participant-thread-create",
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Schema != localapi.ParticipantThreadMutationSchema || created.Type != "participant_thread_mutation" || created.EventSequence < 1 || created.Collaboration.Kind != domain.ThreadKindParticipantBound || created.Collaboration.ParticipantRevision != 1 || len(created.Collaboration.Participants) != 2 {
		t.Fatalf("ThreadCreate() = %#v", created)
	}
	threads, err := client.ThreadList(context.Background(), "personal", consumerProject.Project.ID, 50)
	if err != nil || threads.Schema != localapi.ThreadListSchema || threads.Type != "thread_list" || len(threads.Threads) != 1 || threads.Threads[0].Thread.ID != created.Collaboration.Thread.ID || threads.Threads[0].MessageCount != 0 {
		t.Fatalf("ThreadList() = %#v, %v", threads, err)
	}
	for _, participant := range created.Collaboration.Participants {
		if !strings.HasPrefix(participant.ID, "participant_") || participant.InvitedBy != "local-owner" {
			t.Errorf("participant binding = %#v", participant)
		}
	}
	listed, err := client.ThreadParticipants(context.Background(), "personal", created.Collaboration.Thread.ID)
	if err != nil || listed.Schema != localapi.ParticipantThreadSchema || listed.Type != "participant_thread" || !reflect.DeepEqual(listed.Collaboration, created.Collaboration) {
		t.Fatalf("ThreadParticipants() = %#v, %v; want %#v", listed, err, created.Collaboration)
	}
	invited, err := client.ThreadInvite(context.Background(), localapi.ThreadInviteParams{
		Workspace: "personal", Thread: created.Collaboration.Thread.ID,
		Participant:                 localapi.ThreadParticipantParams{Agent: reviewerAgent, Task: reviewerTask},
		ExpectedParticipantRevision: 1, IdempotencyKey: "participant-thread-invite",
	})
	if err != nil || invited.Collaboration.ParticipantRevision != 2 || len(invited.Collaboration.Participants) != 3 || invited.EventSequence <= created.EventSequence {
		t.Fatalf("ThreadInvite() = %#v, %v", invited, err)
	}
	_, err = client.ThreadInvite(context.Background(), localapi.ThreadInviteParams{
		Workspace: "personal", Thread: created.Collaboration.Thread.ID,
		Participant:                 localapi.ThreadParticipantParams{Agent: reviewerAgent, Task: reviewerTask},
		ExpectedParticipantRevision: 1, IdempotencyKey: "participant-thread-stale-invite",
	})
	if localAPIErrorCode(err) != store.CodeRevisionConflict {
		t.Fatalf("ThreadInvite(stale) error = %v, code = %q", err, localAPIErrorCode(err))
	}
}

type participantSurfaceInspector struct{}

func (participantSurfaceInspector) Inspect(_ context.Context, path string) (domain.CheckoutObservation, error) {
	digit := "1"
	if filepath.Base(path) == "engine" {
		digit = "2"
	}
	return domain.CheckoutObservation{
		Path: path, Availability: domain.CheckoutAvailable, CheckoutKind: domain.CheckoutStandalone,
		Branch: "main", HeadCommit: strings.Repeat(digit, 40), DirtyPaths: []string{},
		GitDir: filepath.Join(path, ".git"), GitCommonDir: filepath.Join(path, ".git"),
		Repository: domain.RepositoryObservation{
			Fingerprint: "git_" + strings.Repeat(digit, 64), ObjectFormat: "sha1", RootCommits: []string{strings.Repeat(digit, 40)},
		},
	}, nil
}

func TestParticipantThreadHandlersRejectUnknownAndCallerAuthorityFields(t *testing.T) {
	t.Parallel()
	running := startTestServer(t, testConfig(t))
	for _, test := range []struct {
		method string
		params map[string]any
	}{
		{localapi.MethodThreadCreate, map[string]any{
			"workspace": "personal", "subject": "alignment", "participants": []any{
				map[string]any{"agent": "one", "task": "task_one"}, map[string]any{"agent": "two", "task": "task_two"},
			}, "idempotency_key": "create", "owner": "attacker",
		}},
		{localapi.MethodThreadInvite, map[string]any{
			"workspace": "personal", "thread": "thread_00000000000000000000000000000001",
			"participant":                   map[string]any{"agent": "three", "task": "task_three", "invited_by": "attacker"},
			"expected_participant_revision": 1, "idempotency_key": "invite",
		}},
		{localapi.MethodThreadParticipants, map[string]any{
			"workspace": "personal", "thread": "thread_00000000000000000000000000000001", "unexpected": true,
		}},
	} {
		response := rawLocalAPIRequest(t, running.config.SocketPath, test.method, test.params)
		if response.Error == nil || response.Error.Code != "invalid_request" || response.Error.Retryable {
			t.Errorf("%s response error = %#v, want non-retryable invalid_request", test.method, response.Error)
		}
	}
}

func TestParticipantThreadCreateBoundsCrossLocalAPIAsStableErrors(t *testing.T) {
	t.Parallel()
	running := startTestServer(t, testConfig(t))
	client := localapi.NewClient(running.config.SocketPath)
	if _, err := client.WorkspaceInit(context.Background(), "personal", "participant-handler-workspace"); err != nil {
		t.Fatal(err)
	}

	one := []any{map[string]any{"agent": "one", "task": "task_one"}}
	nine := make([]any, 9)
	for index := range nine {
		nine[index] = map[string]any{"agent": "agent", "task": "task"}
	}
	for name, participants := range map[string][]any{"one": one, "nine": nine} {
		response := rawLocalAPIRequest(t, running.config.SocketPath, localapi.MethodThreadCreate, map[string]any{
			"workspace": "personal", "subject": "bounded", "participants": participants, "idempotency_key": "bounds-" + name,
		})
		if response.Error == nil || response.Error.Code != store.CodeInvalidMessage || response.Error.Retryable {
			t.Errorf("thread.create(%s participants) error = %#v, want non-retryable %s", name, response.Error, store.CodeInvalidMessage)
		}
	}
}

func TestParticipantRevisionConflictIsStableAndNonRetryable(t *testing.T) {
	t.Parallel()
	response := storeErrorResponse(localapi.Request{ID: "stale-participants", Protocol: localapi.MaxProtocol}, &store.Error{
		Code: store.CodeRevisionConflict, Message: "participant revision changed",
	})
	if response.Error == nil || response.Error.Code != store.CodeRevisionConflict || response.Error.Retryable {
		t.Fatalf("storeErrorResponse(revision conflict) = %#v", response.Error)
	}
}
