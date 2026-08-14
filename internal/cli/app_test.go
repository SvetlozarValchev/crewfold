package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"crewfold/internal/buildinfo"
	"crewfold/internal/daemon"
	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/herdr"
	"crewfold/internal/localapi"
)

func TestNoArgumentsShowsRootHelp(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	if exitCode := app.Run(nil); exitCode != ExitOK {
		t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitOK)
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Fatalf("stdout = %q, want root usage", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestVersionText(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	if exitCode := app.Run([]string{"version"}); exitCode != ExitOK {
		t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitOK)
	}
	if !strings.Contains(stdout.String(), "crewfold dev\n") {
		t.Fatalf("stdout = %q, want development version", stdout.String())
	}
	if !strings.Contains(stdout.String(), "platform: ") {
		t.Fatalf("stdout = %q, want platform", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestVersionJSONAcceptsGlobalOptionBeforeOrAfterCommand(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"before": {"--output", "json", "version"},
		"after":  {"version", "--output=json"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			app, stdout, stderr := newTestApp()
			if exitCode := app.Run(args); exitCode != ExitOK {
				t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitOK)
			}

			var response buildinfo.Info
			if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
				t.Fatalf("json.Unmarshal(stdout) error = %v; stdout = %q", err, stdout.String())
			}
			if response.Schema != buildinfo.VersionSchema {
				t.Fatalf("response.Schema = %q, want %q", response.Schema, buildinfo.VersionSchema)
			}
			if err := response.Validate(); err != nil {
				t.Fatalf("response.Validate() error = %v", err)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestDoctorSelfJSON(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	if exitCode := app.Run([]string{"doctor", "--self", "--output", "json"}); exitCode != ExitOK {
		t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitOK)
	}

	var response doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal(stdout) error = %v; stdout = %q", err, stdout.String())
	}
	if response.Schema != doctorSchema {
		t.Fatalf("response.Schema = %q, want %q", response.Schema, doctorSchema)
	}
	if response.Status != "ok" {
		t.Fatalf("response.Status = %q, want ok; checks = %#v", response.Status, response.Checks)
	}
	if len(response.Checks) != 3 {
		t.Fatalf("len(response.Checks) = %d, want 3", len(response.Checks))
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestDoctorSelfFailureIsVisibleAndNonZero(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	app.executablePath = func() (string, error) {
		return "", errors.New("injected executable lookup failure")
	}

	if exitCode := app.Run([]string{"doctor", "--self", "--output=json"}); exitCode != ExitFailure {
		t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitFailure)
	}

	var response doctorReport
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal(stdout) error = %v; stdout = %q", err, stdout.String())
	}
	if response.Status != "failed" {
		t.Fatalf("response.Status = %q, want failed", response.Status)
	}
	if !strings.Contains(stdout.String(), "injected executable lookup failure") {
		t.Fatalf("stdout = %q, want injected failure detail", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
}

func TestDoctorRetrievalReportsStructuredDegradationAndFails(t *testing.T) {
	t.Parallel()
	client := &fakeDaemonClient{knowledgeIndexStatus: localapi.KnowledgeIndexStatusResult{
		Schema: localapi.KnowledgeIndexStatusSchema, Type: "knowledge_index_status",
		Index: domain.KnowledgeIndexStatus{Status: domain.KnowledgeIndexDegraded, Diagnosis: "missing"},
	}}
	app, stdout, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	if exit := app.Run([]string{"doctor", "--retrieval", "--workspace", "personal", "--socket", "/tmp/crewfold.sock", "--output", "json"}); exit != ExitFailure {
		t.Fatalf("retrieval doctor exit=%d, want failure; stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	var result localapi.KnowledgeIndexStatusResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Schema != localapi.KnowledgeIndexStatusSchema || result.Index.Diagnosis != "missing" {
		t.Fatalf("retrieval doctor JSON=%#v error=%v", result, err)
	}
	if stderr.Len() != 0 || client.knowledgeIndexWorkspace != "personal" {
		t.Fatalf("retrieval doctor stderr=%q workspace=%q", stderr.String(), client.knowledgeIndexWorkspace)
	}
}

func TestUnknownCommandHasStableTextErrorAndUsageExit(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	if exitCode := app.Run([]string{"definitely-not-a-command"}); exitCode != ExitUsage {
		t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitUsage)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), `error: unknown command "definitely-not-a-command"`) {
		t.Fatalf("stderr = %q, want unknown-command error", stderr.String())
	}
	if !strings.Contains(stderr.String(), "crewfold help") {
		t.Fatalf("stderr = %q, want help hint", stderr.String())
	}
	if strings.Contains(strings.ToLower(stderr.String()), "goroutine") {
		t.Fatalf("stderr = %q, must not contain a stack trace", stderr.String())
	}
}

func TestUnknownCommandHasStructuredJSONError(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	if exitCode := app.Run([]string{"--output=json", "nope"}); exitCode != ExitUsage {
		t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitUsage)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}

	var response errorEnvelope
	if err := json.Unmarshal(stderr.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal(stderr) error = %v; stderr = %q", err, stderr.String())
	}
	if response.Schema != errorSchema {
		t.Fatalf("response.Schema = %q, want %q", response.Schema, errorSchema)
	}
	if response.Error.Code != "unknown_command" {
		t.Fatalf("response.Error.Code = %q, want unknown_command", response.Error.Code)
	}
}

func TestInvalidOutputAndCommandArgumentsAreUsageErrors(t *testing.T) {
	t.Parallel()

	tests := map[string][]string{
		"invalid output":      {"version", "--output", "yaml"},
		"duplicate output":    {"--output", "json", "version", "--output=json"},
		"version argument":    {"version", "extra"},
		"doctor missing self": {"doctor"},
		"too much help":       {"help", "version", "doctor"},
	}

	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			app, _, stderr := newTestApp()
			if exitCode := app.Run(args); exitCode != ExitUsage {
				t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitUsage)
			}
			if stderr.Len() == 0 {
				t.Fatal("stderr is empty, want usage diagnosis")
			}
		})
	}
}

func TestHelpTopics(t *testing.T) {
	t.Parallel()

	for _, topic := range []string{"version", "doctor", "daemon", "service", "open", "status", "workspace", "project", "checkout", "agent", "objective", "task", "context", "message", "inbox", "thread", "run", "events", "help"} {
		t.Run(topic, func(t *testing.T) {
			t.Parallel()

			app, stdout, stderr := newTestApp()
			if exitCode := app.Run([]string{"help", topic}); exitCode != ExitOK {
				t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitOK)
			}
			if !strings.Contains(stdout.String(), "Usage:") {
				t.Fatalf("stdout = %q, want usage", stdout.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestMessageSendPassesBoundedRecipientAndThreadInputs(t *testing.T) {
	t.Parallel()
	client := &fakeDaemonClient{messageSend: localapi.MessageSendResult{Schema: localapi.MessageSendSchema, Type: "message_send", Mutation: domain.MessageMutation{Message: domain.Message{ID: "msg_00000000000000000000000000000001"}}}}
	app, stdout, stderr := newTestApp()
	app.newClient = func(socketPath string) daemonClient {
		if socketPath != "/tmp/messages.sock" {
			t.Fatalf("socket path = %q", socketPath)
		}
		return client
	}
	args := []string{"message", "send", "reviewer", "--workspace", "personal", "--kind", "question", "--body", "Check the contract", "--subject", "Contract review", "--thread", "thread_00000000000000000000000000000001", "--artifact-ids", "artifact_one,artifact_two", "--reply-to", "msg_00000000000000000000000000000002", "--socket", "/tmp/messages.sock", "--idempotency-key", "message-key", "--output", "json"}
	if exitCode := app.Run(args); exitCode != ExitOK || stderr.Len() != 0 {
		t.Fatalf("Run(message send) exit = %d, stderr = %q", exitCode, stderr.String())
	}
	params := client.messageSendParams
	if params.Workspace != "personal" || params.RecipientAgent != "reviewer" || params.Kind != "question" || params.Thread == "" || params.ReplyToMessage == "" || len(params.ArtifactIDs) != 2 || params.IdempotencyKey != "message-key" {
		t.Fatalf("MessageSend params = %#v", params)
	}
	if !strings.Contains(stdout.String(), localapi.MessageSendSchema) {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestThreadCreateAcceptsRepeatedTaskBoundParticipants(t *testing.T) {
	t.Parallel()
	collaboration := domain.ParticipantThread{
		Thread: domain.MessageThread{ID: "thread_00000000000000000000000000000001", Subject: "Align engine contract"},
		Kind:   domain.ThreadKindParticipantBound, ParticipantRevision: 1,
	}
	client := &fakeDaemonClient{participantMutation: localapi.ParticipantThreadMutationResult{
		Schema: localapi.ParticipantThreadMutationSchema, Type: "participant_thread_mutation", Collaboration: collaboration,
	}}
	app, stdout, stderr := newTestApp()
	app.newClient = func(socketPath string) daemonClient {
		if socketPath != "/tmp/collaboration.sock" {
			t.Fatalf("socket path = %q", socketPath)
		}
		return client
	}
	exitCode := app.Run([]string{
		"thread", "create", "--workspace", "personal", "--subject", "Align engine contract",
		"--participant", "consumer=task_00000000000000000000000000000001",
		"--participant=engine=task_00000000000000000000000000000002",
		"--socket", "/tmp/collaboration.sock", "--output", "json",
	})
	if exitCode != ExitOK || stderr.Len() != 0 {
		t.Fatalf("Run(thread create) exit = %d, stderr = %q", exitCode, stderr.String())
	}
	params := client.threadCreateParams
	if params.Workspace != "personal" || params.Subject != "Align engine contract" || len(params.Participants) != 2 || params.Participants[0].Agent != "consumer" || params.Participants[1].Task == "" || params.IdempotencyKey != "" {
		t.Fatalf("ThreadCreate params = %#v", params)
	}
	if !strings.Contains(stdout.String(), localapi.ParticipantThreadMutationSchema) {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestThreadInviteAndParticipantsPassConcurrencyInputs(t *testing.T) {
	t.Parallel()
	threadID := "thread_00000000000000000000000000000001"
	collaboration := domain.ParticipantThread{
		Thread: domain.MessageThread{ID: threadID, Subject: "Align engine contract"},
		Kind:   domain.ThreadKindParticipantBound, ParticipantRevision: 3,
	}
	client := &fakeDaemonClient{
		participantMutation: localapi.ParticipantThreadMutationResult{Schema: localapi.ParticipantThreadMutationSchema, Type: "participant_thread_mutation", Collaboration: collaboration},
		participantThread:   localapi.ParticipantThreadResult{Schema: localapi.ParticipantThreadSchema, Type: "participant_thread", Collaboration: collaboration},
	}
	app, stdout, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	if exitCode := app.Run([]string{
		"thread", "invite", threadID, "--workspace", "personal", "--agent", "reviewer",
		"--task", "task_00000000000000000000000000000003", "--expected-participant-revision", "2",
		"--socket", "/tmp/collaboration.sock", "--idempotency-key", "invite-reviewer",
	}); exitCode != ExitOK {
		t.Fatalf("Run(thread invite) exit = %d, stderr = %q", exitCode, stderr.String())
	}
	params := client.threadInviteParams
	if params.Thread != threadID || params.Participant.Agent != "reviewer" || params.ExpectedParticipantRevision != 2 || params.IdempotencyKey != "invite-reviewer" {
		t.Fatalf("ThreadInvite params = %#v", params)
	}
	stdout.Reset()
	if exitCode := app.Run([]string{"thread", "participants", threadID, "--workspace", "personal", "--socket", "/tmp/collaboration.sock", "--output", "json"}); exitCode != ExitOK {
		t.Fatalf("Run(thread participants) exit = %d, stderr = %q", exitCode, stderr.String())
	}
	if !reflect.DeepEqual(client.threadParticipantsArgs, []string{"personal", threadID}) || !strings.Contains(stdout.String(), localapi.ParticipantThreadSchema) {
		t.Fatalf("participants args/output = %#v / %q", client.threadParticipantsArgs, stdout.String())
	}
}

func TestThreadCommandsRejectMalformedBoundsBeforeCallingDaemon(t *testing.T) {
	t.Parallel()
	for name, args := range map[string][]string{
		"one participant": {
			"thread", "create", "--workspace", "personal", "--subject", "too small",
			"--participant", "one=task_00000000000000000000000000000001", "--socket", "/tmp/collaboration.sock",
		},
		"malformed binding": {
			"thread", "create", "--workspace", "personal", "--subject", "bad binding",
			"--participant", "one", "--participant", "two=task_00000000000000000000000000000002", "--socket", "/tmp/collaboration.sock",
		},
		"zero participant revision": {
			"thread", "invite", "thread_00000000000000000000000000000001", "--workspace", "personal",
			"--agent", "three", "--task", "task_00000000000000000000000000000003",
			"--expected-participant-revision", "0", "--socket", "/tmp/collaboration.sock",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			app, _, stderr := newTestApp()
			if exitCode := app.Run(args); exitCode != ExitUsage {
				t.Fatalf("Run() exit = %d, want %d; stderr = %q", exitCode, ExitUsage, stderr.String())
			}
		})
	}
}

func TestRunStartLoadsValidatedScenarioAndPassesPlacementInputs(t *testing.T) {
	t.Parallel()

	scenarioPath := filepath.Join(t.TempDir(), "successful-handoff.json")
	scenario := `{"schema":"urn:crewfold:schema:fixture:fake-run-scenario:v1","name":"successful-handoff","acceptance":{"required_evidence":["tests_passed"]},"steps":[{"kind":"completion","message":"done","evidence":["tests_passed"],"handoff":"review the work"}]}`
	if err := os.WriteFile(scenarioPath, []byte(scenario), 0o600); err != nil {
		t.Fatalf("os.WriteFile(scenario) error = %v", err)
	}
	app, stdout, stderr := newTestApp()
	client := &fakeDaemonClient{runMutation: localapi.RunMutationResult{Schema: localapi.RunMutationSchema, Type: "run_mutation", Detail: domain.RunDetail{Run: domain.Run{ID: "run_00000000000000000000000000000001", Status: domain.RunRequested}}}}
	app.newClient = func(socketPath string) daemonClient {
		if socketPath != "/tmp/crewfold.sock" {
			t.Fatalf("socket path = %q", socketPath)
		}
		return client
	}
	exitCode := app.Run([]string{"run", "start", "task_00000000000000000000000000000001", "--workspace", "personal", "--checkout", "co_00000000000000000000000000000001", "--runtime", "fake", "--provider", "fake", "--scenario", scenarioPath, "--expected-task-revision", "2", "--socket", "/tmp/crewfold.sock", "--idempotency-key", "start-run", "--output", "json"})
	if exitCode != ExitOK || stderr.Len() != 0 {
		t.Fatalf("Run() exit = %d, stderr = %q", exitCode, stderr.String())
	}
	if client.runStartParams.Workspace != "personal" || client.runStartParams.Checkout != "co_00000000000000000000000000000001" || client.runStartParams.Scenario.Name != "successful-handoff" || client.runStartParams.ExpectedTaskRevision != 2 {
		t.Fatalf("RunStart params = %#v", client.runStartParams)
	}
	if !strings.Contains(stdout.String(), localapi.RunMutationSchema) {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunStopAndLogsPassBoundedRuntimeOptions(t *testing.T) {
	t.Parallel()

	client := &fakeDaemonClient{
		runMutation: localapi.RunMutationResult{Schema: localapi.RunMutationSchema, Type: "run_mutation"},
		runLogs: localapi.RunLogsResult{
			Schema: localapi.RunLogsSchema,
			Type:   "run_logs",
			Logs:   domain.RunLogs{RunID: "run_00000000000000000000000000000001", State: execution.RuntimeStateRunning},
		},
	}
	for _, args := range [][]string{
		{"run", "stop", "run_00000000000000000000000000000001", "--graceful", "--workspace", "personal", "--expected-revision", "7", "--grace-millis", "250", "--socket", "/tmp/crewfold.sock", "--idempotency-key", "stop-run", "--output", "json"},
		{"run", "logs", "run_00000000000000000000000000000001", "--workspace", "personal", "--tail", "25", "--socket", "/tmp/crewfold.sock", "--output", "json"},
	} {
		app, stdout, stderr := newTestApp()
		app.newClient = func(string) daemonClient { return client }
		if exitCode := app.Run(args); exitCode != ExitOK {
			t.Fatalf("Run(%q) exit code = %d; stderr = %q", args, exitCode, stderr.String())
		}
		if stdout.Len() == 0 || stderr.Len() != 0 {
			t.Fatalf("Run(%q) stdout = %q, stderr = %q", args, stdout.String(), stderr.String())
		}
	}
	if params := client.runStopParams; params.Workspace != "personal" || params.ExpectedRevision != 7 || params.GracePeriodMillis != 250 || params.IdempotencyKey != "stop-run" {
		t.Fatalf("RunStop params = %#v", params)
	}
	if client.runLogsWorkspace != "personal" || client.runLogsRun != "run_00000000000000000000000000000001" || client.runLogsTail != 25 {
		t.Fatalf("RunLogs args = %q, %q, %d", client.runLogsWorkspace, client.runLogsRun, client.runLogsTail)
	}
}

func TestRunResolveLostRequiresAndForwardsExactOwnerAttestation(t *testing.T) {
	t.Parallel()

	runID := "run_00000000000000000000000000000001"
	client := &fakeDaemonClient{runLossResolution: localapi.RunLossResolutionResult{
		Schema: localapi.RunLossResolutionSchema,
		Type:   "run_loss_resolution",
		Detail: domain.RunDetail{
			Run:  domain.Run{ID: runID, Status: domain.RunFailed, Revision: 8},
			Task: domain.Task{Status: domain.TaskBlocked},
		},
		Resolution: domain.RunLossResolution{
			RunID: runID, LostRevision: 7, Resolution: "owner_confirmed_effects_ended",
			Note: "runtime was retired independently", EventSequence: 91,
		},
		EventSequence: 91,
	}}
	app, stdout, stderr := newTestApp()
	app.newClient = func(socketPath string) daemonClient {
		if socketPath != "/tmp/crewfold.sock" {
			t.Fatalf("socket path = %q", socketPath)
		}
		return client
	}
	exit := app.Run([]string{
		"run", "resolve-lost", runID,
		"--workspace", "personal",
		"--expected-revision", "7",
		"--note", "runtime was retired independently",
		"--confirm-runtime-retired",
		"--idempotency-key", "resolve-lost-run",
		"--socket", "/tmp/crewfold.sock",
		"--output", "json",
	})
	if exit != ExitOK || stderr.Len() != 0 || !strings.Contains(stdout.String(), localapi.RunLossResolutionSchema) {
		t.Fatalf("Run(resolve-lost) exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
	params := client.runLostResolveParams
	if params.Workspace != "personal" || params.Run != runID || params.ExpectedRevision != 7 ||
		params.Note != "runtime was retired independently" || !params.RuntimeRetiredConfirmed || params.IdempotencyKey != "resolve-lost-run" {
		t.Fatalf("RunLostResolve params = %#v", params)
	}

	for name, args := range map[string][]string{
		"missing confirmation": {
			"run", "resolve-lost", runID, "--workspace", "personal", "--expected-revision", "7",
			"--note", "runtime was retired independently", "--socket", "/tmp/crewfold.sock",
		},
		"duplicate confirmation": {
			"run", "resolve-lost", runID, "--workspace", "personal", "--expected-revision", "7",
			"--note", "runtime was retired independently", "--confirm-runtime-retired", "--confirm-runtime-retired",
			"--socket", "/tmp/crewfold.sock",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			app, _, stderr := newTestApp()
			if exit := app.Run(args); exit != ExitUsage || !strings.Contains(stderr.String(), "confirm-runtime-retired") {
				t.Fatalf("Run(%s) exit=%d stderr=%q", name, exit, stderr.String())
			}
		})
	}
}

func TestRunStopAndLogsRejectUnsafeOptions(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"stop without graceful marker": {"run", "stop", "run_1234", "--workspace", "personal", "--expected-revision", "1", "--socket", "/tmp/socket"},
		"stop grace too large":         {"run", "stop", "run_1234", "--graceful", "--workspace", "personal", "--expected-revision", "1", "--grace-millis", "30001", "--socket", "/tmp/socket"},
		"negative log tail":            {"run", "logs", "run_1234", "--workspace", "personal", "--tail", "-1", "--socket", "/tmp/socket"},
	} {
		t.Run(name, func(t *testing.T) {
			app, _, stderr := newTestApp()
			if exitCode := app.Run(args); exitCode != ExitUsage || stderr.Len() == 0 {
				t.Fatalf("Run() exit code = %d, stderr = %q", exitCode, stderr.String())
			}
		})
	}
}

func TestRunInteractiveControlsUseProviderNeutralRuntimeAPI(t *testing.T) {
	t.Parallel()

	client := &fakeDaemonClient{
		runControl: localapi.RunControlResult{Schema: localapi.RunControlSchema, Type: "run_control", RunID: "run_00000000000000000000000000000001", Runtime: "herdr", Status: "delivered"},
		runAttach:  localapi.RunAttachResult{Schema: localapi.RunAttachSchema, Type: "run_attach", RunID: "run_00000000000000000000000000000001", Runtime: "herdr", Executable: "/opt/herdr", Arguments: []string{"terminal", "attach", "term-1"}},
	}
	for _, test := range []struct {
		arguments []string
		action    string
	}{
		{[]string{"run", "prompt", client.runAttach.RunID, "--workspace", "personal", "--text", "inspect inbox", "--socket", "/tmp/crewfold.sock", "--output", "json"}, "prompt"},
		{[]string{"run", "interrupt", client.runAttach.RunID, "--workspace", "personal", "--socket", "/tmp/crewfold.sock", "--output", "json"}, "interrupt"},
	} {
		client.runControl.Action = test.action
		app, stdout, stderr := newTestApp()
		app.newClient = func(string) daemonClient { return client }
		if exit := app.Run(test.arguments); exit != ExitOK || stderr.Len() != 0 || !strings.Contains(stdout.String(), localapi.RunControlSchema) {
			t.Fatalf("Run(%s) exit=%d stdout=%q stderr=%q", test.action, exit, stdout.String(), stderr.String())
		}
	}
	if client.runPromptText != "inspect inbox" || client.runControlWorkspace != "personal" || client.runControlRun != client.runAttach.RunID {
		t.Fatalf("interactive control args = workspace:%q run:%q prompt:%q", client.runControlWorkspace, client.runControlRun, client.runPromptText)
	}

	app, _, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	var attached localapi.RunAttachResult
	app.runInteractive = func(_ context.Context, value localapi.RunAttachResult) error {
		attached = value
		return nil
	}
	if exit := app.Run([]string{"run", "attach", client.runAttach.RunID, "--workspace", "personal", "--socket", "/tmp/crewfold.sock"}); exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("Run(attach) exit=%d stderr=%q", exit, stderr.String())
	}
	if attached.Executable != "/opt/herdr" || client.runControlWorkspace != "personal" || client.runControlRun != client.runAttach.RunID {
		t.Fatalf("attach result=%#v workspace=%q run=%q", attached, client.runControlWorkspace, client.runControlRun)
	}
}

func TestRunAttachRejectsRemovedTakeoverOption(t *testing.T) {
	t.Parallel()
	app, _, stderr := newTestApp()
	exit := app.Run([]string{
		"run", "attach", "run_00000000000000000000000000000001", "--takeover",
		"--workspace", "personal", "--socket", "/tmp/crewfold.sock",
	})
	if exit != ExitUsage || !strings.Contains(stderr.String(), "unknown option --takeover") {
		t.Fatalf("Run(removed attach takeover) exit=%d stderr=%q", exit, stderr.String())
	}
}

func TestHerdrDoctorUsesExplicitBinaryAndSession(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	app.probeHerdr = func(_ context.Context, executable, session string) herdr.ProbeReport {
		if executable != "/opt/herdr" || session != "crewfold-test" {
			t.Fatalf("probe args = %q, %q", executable, session)
		}
		return herdr.ProbeReport{Schema: herdr.ProbeSchema, Runtime: "herdr", Status: "ok", Binary: executable, Session: session, SchemaVersion: 1, Protocol: 19, Checks: []herdr.ProbeCheck{{Name: "schema", Status: "ok"}}}
	}
	if exit := app.Run([]string{"doctor", "--runtime", "herdr", "--herdr-binary", "/opt/herdr", "--herdr-session", "crewfold-test", "--output", "json"}); exit != ExitOK || stderr.Len() != 0 || !strings.Contains(stdout.String(), herdr.ProbeSchema) {
		t.Fatalf("Run(doctor runtime) exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
}

func TestCodexDoctorUsesExplicitBinaryAndHome(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	app.probeCodex = func(_ context.Context, executable, codexHome string) execution.CodexProbeReport {
		if executable != "/opt/codex" || codexHome != "/private/codex" {
			t.Fatalf("probe args = %q, %q", executable, codexHome)
		}
		return execution.CodexProbeReport{
			Schema: execution.CodexProbeSchema, Provider: "codex", Status: "ok", Binary: executable,
			Capabilities: []string{"headless_execution", "mcp_client", "structured_events"},
			Checks:       []execution.CodexProbeCheck{{Name: "authentication", Status: "ok"}},
		}
	}
	if exit := app.Run([]string{"doctor", "--provider", "codex", "--codex-binary", "/opt/codex", "--codex-home", "/private/codex", "--output", "json"}); exit != ExitOK || stderr.Len() != 0 || !strings.Contains(stdout.String(), execution.CodexProbeSchema) {
		t.Fatalf("Run(doctor provider) exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
}

func TestClaudeDoctorUsesExplicitBinaryAndConfigDirectory(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	app.probeClaude = func(_ context.Context, executable, configDir string) execution.ClaudeProbeReport {
		if executable != "/opt/claude" || configDir != "/private/claude" {
			t.Fatalf("probe args = %q, %q", executable, configDir)
		}
		return execution.ClaudeProbeReport{
			Schema: execution.ClaudeProbeSchema, Provider: "claude", Status: "ok", Binary: executable,
			Version: "2.1.220 (Claude Code)", Capabilities: []string{"headless_execution", "mcp_client", "structured_events"},
			Checks: []execution.ClaudeProbeCheck{{Name: "authentication", Status: "ok"}},
		}
	}
	if exit := app.Run([]string{"doctor", "--provider", "claude", "--claude-binary", "/opt/claude", "--claude-config-dir", "/private/claude", "--output", "json"}); exit != ExitOK || stderr.Len() != 0 || !strings.Contains(stdout.String(), execution.ClaudeProbeSchema) {
		t.Fatalf("exit/output = %d, %q, %q", exit, stdout.String(), stderr.String())
	}
}

func TestProjectAndCheckoutCommandsUseExplicitScopeAndStructuredResults(t *testing.T) {
	t.Parallel()

	project := domain.Project{ID: "prj_1234", WorkspaceID: "ws_1234", Name: "world-engine", Revision: 1}
	repository := domain.Repository{ID: "repo_1234", WorkspaceID: "ws_1234", Fingerprint: "git_1234", Revision: 1}
	checkout := domain.Checkout{ID: "co_1234", ProjectID: project.ID, RepositoryID: repository.ID, Path: "/work/world-engine", WriteMode: "exclusive", CheckoutKind: "standalone", Availability: "available", Revision: 1}
	for name, test := range map[string]struct {
		args       []string
		wantSchema string
		configure  func(*fakeDaemonClient)
		assert     func(*testing.T, *fakeDaemonClient)
	}{
		"project add": {
			args:       []string{"project", "add", "world-engine", "--workspace", "personal", "--repo", "/work/world-engine", "--mode", "claimed", "--socket", "/tmp/sources.sock", "--idempotency-key", "project-key", "--output=json"},
			wantSchema: localapi.ProjectAddSchema,
			configure: func(client *fakeDaemonClient) {
				client.projectAdd = localapi.ProjectAddResult{Schema: localapi.ProjectAddSchema, Type: "project_registered", Project: project, Repository: repository, Checkout: checkout}
			},
			assert: func(t *testing.T, client *fakeDaemonClient) {
				want := []string{"personal", "world-engine", "/work/world-engine", "claimed", "project-key"}
				if strings.Join(client.projectArgs, "|") != strings.Join(want, "|") {
					t.Fatalf("ProjectAdd args = %q, want %q", client.projectArgs, want)
				}
			},
		},
		"project inspect": {
			args:       []string{"project", "inspect", "world-engine", "--workspace", "personal", "--socket", "/tmp/sources.sock", "--output=json"},
			wantSchema: localapi.ProjectInspectSchema,
			configure: func(client *fakeDaemonClient) {
				client.projectInspect = localapi.ProjectInspectResult{Schema: localapi.ProjectInspectSchema, Type: "project_inspection", Project: project, Repositories: []domain.Repository{repository}, Checkouts: []domain.Checkout{checkout}}
			},
			assert: func(t *testing.T, client *fakeDaemonClient) {
				if strings.Join(client.projectArgs, "|") != "personal|world-engine" {
					t.Fatalf("ProjectInspect args = %q", client.projectArgs)
				}
			},
		},
		"checkout add adjacent clone": {
			args:       []string{"checkout", "add", "world-engine", "/work/world-engine-2", "--workspace", "personal", "--mode", "exclusive", "--socket", "/tmp/sources.sock", "--idempotency-key", "checkout-key", "--output=json"},
			wantSchema: localapi.CheckoutAddSchema,
			configure: func(client *fakeDaemonClient) {
				client.checkoutAdd = localapi.CheckoutAddResult{Schema: localapi.CheckoutAddSchema, Type: "checkout_registered", Repository: repository, Checkout: checkout}
			},
			assert: func(t *testing.T, client *fakeDaemonClient) {
				want := []string{"personal", "world-engine", "/work/world-engine-2", "exclusive", "checkout-key"}
				if strings.Join(client.checkoutArgs, "|") != strings.Join(want, "|") {
					t.Fatalf("CheckoutAdd args = %q, want %q", client.checkoutArgs, want)
				}
			},
		},
		"checkout list": {
			args:       []string{"checkout", "list", "world-engine", "--workspace", "personal", "--socket", "/tmp/sources.sock", "--output=json"},
			wantSchema: localapi.CheckoutListSchema,
			configure: func(client *fakeDaemonClient) {
				client.checkoutList = localapi.CheckoutListResult{Schema: localapi.CheckoutListSchema, Type: "checkout_list", Project: project, Checkouts: []domain.Checkout{checkout}}
			},
			assert: func(t *testing.T, client *fakeDaemonClient) {
				if strings.Join(client.checkoutArgs, "|") != "personal|world-engine" {
					t.Fatalf("CheckoutList args = %q", client.checkoutArgs)
				}
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := &fakeDaemonClient{}
			test.configure(client)
			app, stdout, stderr := newTestApp()
			app.newClient = func(socketPath string) daemonClient {
				if socketPath != "/tmp/sources.sock" {
					t.Fatalf("socketPath = %q, want /tmp/sources.sock", socketPath)
				}
				return client
			}
			if exitCode := app.Run(test.args); exitCode != ExitOK {
				t.Fatalf("Run() exit code = %d, stderr = %q", exitCode, stderr.String())
			}
			var envelope struct {
				Schema string `json:"schema"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
				t.Fatalf("json.Unmarshal(stdout) error = %v; stdout = %q", err, stdout.String())
			}
			if envelope.Schema != test.wantSchema {
				t.Fatalf("schema = %q, want %q", envelope.Schema, test.wantSchema)
			}
			test.assert(t, client)
		})
	}
}

func TestProjectAndCheckoutCommandsRejectMissingScopeAndPaths(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"project subcommand":      {"project"},
		"project repository":      {"project", "add", "demo", "--workspace", "personal", "--socket", "/tmp/socket"},
		"project workspace":       {"project", "inspect", "demo", "--socket", "/tmp/socket"},
		"checkout subcommand":     {"checkout"},
		"checkout path":           {"checkout", "add", "demo", "--workspace", "personal", "--socket", "/tmp/socket"},
		"checkout list workspace": {"checkout", "list", "demo", "--socket", "/tmp/socket"},
	} {
		t.Run(name, func(t *testing.T) {
			app, _, stderr := newTestApp()
			if exitCode := app.Run(args); exitCode != ExitUsage {
				t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitUsage)
			}
			if stderr.Len() == 0 {
				t.Fatal("stderr is empty, want usage diagnosis")
			}
		})
	}
}

func TestCoordinationCommandsPreserveScopeBudgetsLeasesAndRevisions(t *testing.T) {
	t.Parallel()

	client := &fakeDaemonClient{
		agentMutation:     localapi.AgentMutationResult{Schema: localapi.AgentMutationSchema, Type: "agent_mutation"},
		objectiveMutation: localapi.ObjectiveMutationResult{Schema: localapi.ObjectiveMutationSchema, Type: "objective_mutation"},
		taskMutation:      localapi.TaskMutationResult{Schema: localapi.TaskMutationSchema, Type: "task_mutation"},
		coordination:      localapi.CoordinationStatusResult{Schema: localapi.CoordinationStatusSchema, Type: "coordination_status", Workspace: "personal"},
	}
	run := func(args []string, wantSchema string) {
		t.Helper()
		app, stdout, stderr := newTestApp()
		app.newClient = func(socketPath string) daemonClient {
			if socketPath != "/tmp/coordination.sock" {
				t.Fatalf("socketPath = %q, want /tmp/coordination.sock", socketPath)
			}
			return client
		}
		if exitCode := app.Run(args); exitCode != ExitOK {
			t.Fatalf("Run(%q) exit code = %d; stderr = %q", args, exitCode, stderr.String())
		}
		var envelope struct {
			Schema string `json:"schema"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
			t.Fatalf("json.Unmarshal(%q) error = %v; stdout = %q", args, err, stdout.String())
		}
		if envelope.Schema != wantSchema {
			t.Fatalf("Run(%q) schema = %q, want %q", args, envelope.Schema, wantSchema)
		}
	}

	run([]string{"agent", "create", "implementer", "--workspace", "personal", "--role", "implementer", "--provider", "codex", "--runtime", "herdr", "--max-concurrency", "2", "--socket", "/tmp/coordination.sock", "--idempotency-key", "agent-key", "--output=json"}, localapi.AgentMutationSchema)
	if params := client.agentCreateParams; params.Workspace != "personal" || params.Name != "implementer" || params.Provider != "codex" || params.Runtime != "herdr" || params.MaxConcurrency != 2 || params.IdempotencyKey != "agent-key" {
		t.Fatalf("AgentCreate params = %#v", params)
	}

	run([]string{"objective", "create", "Ship greeting", "--workspace", "personal", "--project", "demo", "--budget-tokens", "20000", "--budget-cents", "500", "--budget-seconds", "3600", "--socket", "/tmp/coordination.sock", "--idempotency-key", "objective-key", "--output=json"}, localapi.ObjectiveMutationSchema)
	if params := client.objectiveCreateParams; params.Workspace != "personal" || params.Project != "demo" || params.Budget != (domain.Budget{TokenLimit: 20000, CostCents: 500, TimeSeconds: 3600}) {
		t.Fatalf("ObjectiveCreate params = %#v", params)
	}

	run([]string{"task", "create", "--workspace", "personal", "--project", "demo", "--objective", "obj_1234", "--title", "Implement greeting", "--description", "deterministic", "--priority", "200", "--budget-tokens", "5000", "--socket", "/tmp/coordination.sock", "--idempotency-key", "task-key", "--output=json"}, localapi.TaskMutationSchema)
	if params := client.taskCreateParams; params.Workspace != "personal" || params.Project != "demo" || params.Objective != "obj_1234" || params.Priority != 200 || params.Budget.TokenLimit != 5000 {
		t.Fatalf("TaskCreate params = %#v", params)
	}

	run([]string{"task", "assign", "task_1234", "implementer", "--workspace", "personal", "--lease-seconds", "300", "--expected-revision", "7", "--socket", "/tmp/coordination.sock", "--idempotency-key", "assign-key", "--output=json"}, localapi.TaskMutationSchema)
	if params := client.taskAssignParams; params.Task != "task_1234" || params.Agent != "implementer" || params.LeaseSeconds != 300 || params.ExpectedRevision != 7 {
		t.Fatalf("TaskAssign params = %#v", params)
	}

	run([]string{"status", "--workspace", "personal", "--socket", "/tmp/coordination.sock", "--output=json"}, localapi.CoordinationStatusSchema)
	if client.coordinationWorkspace != "personal" {
		t.Fatalf("CoordinationStatus workspace = %q, want personal", client.coordinationWorkspace)
	}
}

func TestCoordinationCommandsRejectAmbiguousBudgetReplacementAndInvalidConcurrency(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"zero concurrency":         {"agent", "create", "implementer", "--workspace", "personal", "--role", "implementer", "--provider", "fake", "--max-concurrency", "0", "--socket", "/tmp/socket"},
		"partial objective budget": {"objective", "update", "obj_1234", "--workspace", "personal", "--budget-tokens", "10", "--expected-revision", "1", "--socket", "/tmp/socket"},
		"partial task budget":      {"task", "update", "task_1234", "--workspace", "personal", "--budget-cents", "10", "--expected-revision", "1", "--socket", "/tmp/socket"},
	} {
		t.Run(name, func(t *testing.T) {
			app, _, stderr := newTestApp()
			if exitCode := app.Run(args); exitCode != ExitUsage {
				t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitUsage)
			}
			if stderr.Len() == 0 {
				t.Fatal("stderr is empty, want usage diagnosis")
			}
		})
	}
}

func TestWorkspaceCommandsUseExplicitSocketAndStructuredResults(t *testing.T) {
	t.Parallel()

	workspace := domain.Workspace{
		ID:        "ws_1234",
		Name:      "personal",
		Revision:  1,
		CreatedAt: "2026-08-12T01:00:00Z",
		UpdatedAt: "2026-08-12T01:00:00Z",
		CreatedBy: "local-owner",
		UpdatedBy: "local-owner",
	}
	for name, test := range map[string]struct {
		args       []string
		wantSchema string
		configure  func(*fakeDaemonClient)
		assert     func(*testing.T, *fakeDaemonClient)
	}{
		"database doctor": {
			args:       []string{"doctor", "--database", "--socket", "/tmp/workspace.sock", "--output=json"},
			wantSchema: localapi.DatabaseStatusSchema,
			configure: func(client *fakeDaemonClient) {
				client.databaseStatus = localapi.DatabaseStatusResult{
					Schema:         localapi.DatabaseStatusSchema,
					Type:           "database_status",
					Status:         "ok",
					SchemaVersion:  1,
					BaselineSHA256: strings.Repeat("a", 64),
					CatalogSHA256:  strings.Repeat("b", 64),
					JournalMode:    "wal",
					ForeignKeys:    true,
					IntegrityCheck: "ok",
				}
			},
		},
		"workspace init": {
			args:       []string{"workspace", "init", "personal", "--socket", "/tmp/workspace.sock", "--idempotency-key", "init-personal", "--output=json"},
			wantSchema: localapi.WorkspaceInitSchema,
			configure: func(client *fakeDaemonClient) {
				client.workspaceInit = localapi.WorkspaceInitResult{
					Schema:        localapi.WorkspaceInitSchema,
					Type:          "workspace_initialized",
					Workspace:     workspace,
					EventID:       "evt_1234",
					EventSequence: 1,
				}
			},
			assert: func(t *testing.T, client *fakeDaemonClient) {
				if client.initName != "personal" || client.initKey != "init-personal" {
					t.Fatalf("WorkspaceInit args = %q, %q", client.initName, client.initKey)
				}
			},
		},
		"workspace show": {
			args:       []string{"workspace", "show", "personal", "--socket=/tmp/workspace.sock", "--output=json"},
			wantSchema: localapi.WorkspaceShowSchema,
			configure: func(client *fakeDaemonClient) {
				client.workspaceShow = localapi.WorkspaceShowResult{
					Schema:    localapi.WorkspaceShowSchema,
					Type:      "workspace",
					Workspace: workspace,
				}
			},
			assert: func(t *testing.T, client *fakeDaemonClient) {
				if client.showIdentifier != "personal" {
					t.Fatalf("WorkspaceShow identifier = %q", client.showIdentifier)
				}
			},
		},
		"events list": {
			args:       []string{"events", "list", "--workspace", "personal", "--socket", "/tmp/workspace.sock", "--after", "7", "--limit", "25", "--output=json"},
			wantSchema: localapi.EventsListSchema,
			configure: func(client *fakeDaemonClient) {
				client.eventsList = localapi.EventsListResult{
					Schema:    localapi.EventsListSchema,
					Type:      "event_list",
					HighWater: 7,
					Events:    []domain.Event{},
					PageResult: localapi.PageResult{
						Total: 0,
					},
				}
			},
			assert: func(t *testing.T, client *fakeDaemonClient) {
				if client.eventsAfter != 7 || client.eventsLimit != 25 {
					t.Fatalf("EventsList args = %d, %d", client.eventsAfter, client.eventsLimit)
				}
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := &fakeDaemonClient{}
			if test.configure != nil {
				test.configure(client)
			}
			app, stdout, stderr := newTestApp()
			app.newClient = func(socketPath string) daemonClient {
				if socketPath != "/tmp/workspace.sock" {
					t.Fatalf("socketPath = %q, want /tmp/workspace.sock", socketPath)
				}
				return client
			}
			if exitCode := app.Run(test.args); exitCode != ExitOK {
				t.Fatalf("Run() exit code = %d, stderr = %q", exitCode, stderr.String())
			}
			var response struct {
				Schema string `json:"schema"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
				t.Fatalf("json.Unmarshal(stdout) error = %v; stdout = %q", err, stdout.String())
			}
			if response.Schema != test.wantSchema {
				t.Fatalf("response.Schema = %q, want %q", response.Schema, test.wantSchema)
			}
			if test.assert != nil {
				test.assert(t, client)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestWorkspaceCommandsRejectInvalidArguments(t *testing.T) {
	t.Parallel()

	for name, args := range map[string][]string{
		"workspace subcommand":  {"workspace"},
		"workspace name":        {"workspace", "init", "--socket", "/tmp/socket"},
		"workspace socket":      {"workspace", "show", "personal"},
		"events subcommand":     {"events"},
		"events after missing":  {"events", "list", "--socket", "/tmp/socket"},
		"events after negative": {"events", "list", "--socket", "/tmp/socket", "--after", "-1"},
		"events limit zero":     {"events", "list", "--socket", "/tmp/socket", "--after", "0", "--limit", "0"},
		"database socket":       {"doctor", "--database"},
	} {
		t.Run(name, func(t *testing.T) {
			app, _, stderr := newTestApp()
			if exitCode := app.Run(args); exitCode != ExitUsage {
				t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitUsage)
			}
			if stderr.Len() == 0 {
				t.Fatal("stderr is empty, want usage diagnosis")
			}
		})
	}
}

func TestDaemonRunPassesRequiredConfiguration(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	var received daemon.Config
	app.runDaemon = func(_ context.Context, config daemon.Config) error {
		received = config
		return nil
	}

	exitCode := app.Run([]string{
		"daemon", "run",
		"--data-dir", "/tmp/crewfold-test-data",
		"--socket=/tmp/crewfold-test.sock",
		"--web-address", "127.0.0.1:43121",
		"--codex-sandbox", "danger-full-access",
		"--codex-external-sandbox", "true",
		"--codex-tool-network-access", "true",
		"--claude-binary", "/opt/claude",
		"--claude-config-dir", "/private/claude",
		"--claude-max-budget-usd", "2.5",
		"--claude-external-sandbox", "true",
	})
	if exitCode != ExitOK {
		t.Fatalf("Run() exit code = %d, want %d; stderr = %q", exitCode, ExitOK, stderr.String())
	}
	if received.DataDir != "/tmp/crewfold-test-data" {
		t.Fatalf("received.DataDir = %q", received.DataDir)
	}
	if received.SocketPath != "/tmp/crewfold-test.sock" {
		t.Fatalf("received.SocketPath = %q", received.SocketPath)
	}
	if received.WebAddress != "127.0.0.1:43121" {
		t.Fatalf("received.WebAddress = %q", received.WebAddress)
	}
	if received.Logger == nil {
		t.Fatal("received.Logger = nil")
	}
	if !received.CodexToolNetworkAccess {
		t.Fatal("received.CodexToolNetworkAccess = false")
	}
	if received.CodexSandboxMode != execution.CodexSandboxDangerFullAccess {
		t.Fatalf("received.CodexSandboxMode = %q", received.CodexSandboxMode)
	}
	if !received.CodexExternallySandboxed {
		t.Fatal("received.CodexExternallySandboxed = false")
	}
	if received.ClaudeExecutable != "/opt/claude" || received.ClaudeConfigDir != "/private/claude" || received.ClaudeMaxBudgetUSD != "2.50" || !received.ClaudeExternallySandboxed {
		t.Fatalf("received Claude config = %#v", received)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("stdout = %q, stderr = %q, want empty", stdout.String(), stderr.String())
	}
}

func TestDaemonRunRejectsInvalidCodexToolNetworkAccess(t *testing.T) {
	t.Parallel()

	app, _, stderr := newTestApp()
	exitCode := app.Run([]string{
		"daemon", "run", "--data-dir", "/tmp/data", "--socket", "/tmp/socket",
		"--codex-tool-network-access", "sometimes",
	})
	if exitCode != ExitUsage || !strings.Contains(stderr.String(), "must be true or false") {
		t.Fatalf("Run() exit=%d stderr=%q", exitCode, stderr.String())
	}
}

func TestDaemonRunRejectsInvalidCodexSandbox(t *testing.T) {
	t.Parallel()

	app, _, stderr := newTestApp()
	exitCode := app.Run([]string{
		"daemon", "run", "--data-dir", "/tmp/data", "--socket", "/tmp/socket",
		"--codex-sandbox", "unbounded",
	})
	if exitCode != ExitUsage || !strings.Contains(stderr.String(), "Codex sandbox must be") {
		t.Fatalf("Run() exit=%d stderr=%q", exitCode, stderr.String())
	}
}

func TestDaemonRunRefusesFullAccessWithoutExternalSandbox(t *testing.T) {
	t.Parallel()

	app, _, stderr := newTestApp()
	exitCode := app.Run([]string{
		"daemon", "run", "--data-dir", "/tmp/data", "--socket", "/tmp/socket",
		"--codex-sandbox", "danger-full-access",
	})
	if exitCode != ExitUsage || !strings.Contains(stderr.String(), "requires --codex-external-sandbox true") {
		t.Fatalf("Run() exit=%d stderr=%q", exitCode, stderr.String())
	}
}

func TestDaemonRunRejectsInvalidClaudeBudget(t *testing.T) {
	t.Parallel()

	app, _, stderr := newTestApp()
	exitCode := app.Run([]string{
		"daemon", "run", "--data-dir", "/tmp/data", "--socket", "/tmp/socket",
		"--claude-max-budget-usd", "unbounded",
	})
	if exitCode != ExitUsage || !strings.Contains(stderr.String(), "Claude maximum budget") {
		t.Fatalf("Run() exit=%d stderr=%q", exitCode, stderr.String())
	}
}

func TestDaemonRunReportsStableStartupError(t *testing.T) {
	t.Parallel()

	app, stdout, stderr := newTestApp()
	app.runDaemon = func(context.Context, daemon.Config) error {
		return &daemon.StartupError{
			Code:    daemon.CodeSocketInUse,
			Message: "socket is served by a live process",
		}
	}

	exitCode := app.Run([]string{
		"daemon", "run", "--data-dir", "/tmp/data", "--socket", "/tmp/socket", "--output=json",
	})
	if exitCode != ExitFailure {
		t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitFailure)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	var response errorEnvelope
	if err := json.Unmarshal(stderr.Bytes(), &response); err != nil {
		t.Fatalf("json.Unmarshal(stderr) error = %v; stderr = %q", err, stderr.String())
	}
	if response.Error.Code != daemon.CodeSocketInUse {
		t.Fatalf("response.Error.Code = %q, want %q", response.Error.Code, daemon.CodeSocketInUse)
	}
}

func TestStatusAndStopUseExplicitSocket(t *testing.T) {
	t.Parallel()

	status := localapi.StatusResult{
		Schema:        localapi.StatusSchema,
		Type:          "system_status",
		Status:        "ok",
		Protocol:      1,
		PID:           42,
		StartedAt:     time.Unix(1, 0).UTC().Format(time.RFC3339),
		UptimeMillis:  12,
		ServerVersion: buildinfo.Current(),
	}
	client := &fakeDaemonClient{
		status: status,
		stop: localapi.StopResult{
			Schema: localapi.StopSchema,
			Type:   "stop_acknowledgement",
			Status: "stopping",
		},
	}

	for name, test := range map[string]struct {
		args       []string
		wantSchema string
	}{
		"status": {[]string{"status", "--socket", "/tmp/test.sock", "--output=json"}, localapi.StatusSchema},
		"stop":   {[]string{"daemon", "stop", "--socket=/tmp/test.sock", "--output", "json"}, localapi.StopSchema},
	} {
		t.Run(name, func(t *testing.T) {
			app, stdout, stderr := newTestApp()
			app.newClient = func(socketPath string) daemonClient {
				if socketPath != "/tmp/test.sock" {
					t.Fatalf("socketPath = %q, want /tmp/test.sock", socketPath)
				}
				return client
			}

			if exitCode := app.Run(test.args); exitCode != ExitOK {
				t.Fatalf("Run() exit code = %d, want %d; stderr = %q", exitCode, ExitOK, stderr.String())
			}
			var response struct {
				Schema string `json:"schema"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
				t.Fatalf("json.Unmarshal(stdout) error = %v; stdout = %q", err, stdout.String())
			}
			if response.Schema != test.wantSchema {
				t.Fatalf("response.Schema = %q, want %q", response.Schema, test.wantSchema)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestDaemonCommandsRejectMissingAndUnknownOptions(t *testing.T) {
	t.Parallel()

	tests := map[string][]string{
		"daemon subcommand": {"daemon"},
		"run data dir":      {"daemon", "run", "--socket", "/tmp/socket"},
		"run socket":        {"daemon", "run", "--data-dir", "/tmp/data"},
		"status socket":     {"status"},
		"stop socket":       {"daemon", "stop"},
		"unknown option":    {"status", "--bogus", "value"},
		"duplicate option":  {"status", "--socket", "one", "--socket", "two"},
	}

	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			app, _, stderr := newTestApp()
			if exitCode := app.Run(args); exitCode != ExitUsage {
				t.Fatalf("Run() exit code = %d, want %d", exitCode, ExitUsage)
			}
			if stderr.Len() == 0 {
				t.Fatal("stderr is empty, want usage diagnosis")
			}
		})
	}
}

type fakeDaemonClient struct {
	status                    localapi.StatusResult
	statusErr                 error
	stop                      localapi.StopResult
	stopErr                   error
	databaseStatus            localapi.DatabaseStatusResult
	databaseStatusErr         error
	fullDoctor                localapi.FullDoctorResult
	backupCreate              localapi.BackupCreateResult
	backupCreateParams        localapi.BackupCreateParams
	webBootstrap              localapi.WebBootstrapResult
	webBootstrapErr           error
	webBootstrapErrors        []error
	webBootstrapCalls         int
	knowledgeIndexStatus      localapi.KnowledgeIndexStatusResult
	knowledgeIndexRebuild     localapi.KnowledgeIndexRebuildResult
	knowledgeSearch           localapi.KnowledgeSearchResult
	knowledgeSearchParams     localapi.KnowledgeSearchParams
	knowledgeRebuildParams    localapi.KnowledgeIndexRebuildParams
	knowledgeIndexWorkspace   string
	workspaceInit             localapi.WorkspaceInitResult
	workspaceInitErr          error
	workspaceShow             localapi.WorkspaceShowResult
	workspaceShowErr          error
	projectAdd                localapi.ProjectAddResult
	projectAddErr             error
	projectInspect            localapi.ProjectInspectResult
	projectInspectErr         error
	checkoutAdd               localapi.CheckoutAddResult
	checkoutAddErr            error
	checkoutList              localapi.CheckoutListResult
	checkoutListErr           error
	eventsList                localapi.EventsListResult
	eventsListErr             error
	agentMutation             localapi.AgentMutationResult
	agentShow                 localapi.AgentShowResult
	agentList                 localapi.AgentListResult
	objectiveMutation         localapi.ObjectiveMutationResult
	objectiveShow             localapi.ObjectiveShowResult
	objectiveList             localapi.ObjectiveListResult
	taskMutation              localapi.TaskMutationResult
	taskShow                  localapi.TaskShowResult
	taskList                  localapi.TaskListResult
	taskTimeline              localapi.TaskTimelineResult
	runMutation               localapi.RunMutationResult
	runLossResolution         localapi.RunLossResolutionResult
	runShow                   localapi.RunShowResult
	runList                   localapi.RunListResult
	runLogs                   localapi.RunLogsResult
	runControl                localapi.RunControlResult
	runAttach                 localapi.RunAttachResult
	runStartParams            localapi.RunStartParams
	runResumeParams           localapi.RunResumeParams
	runStopParams             localapi.RunStopParams
	runLostResolveParams      localapi.RunLostResolveParams
	runLogsWorkspace          string
	runLogsRun                string
	runLogsTail               int
	runControlWorkspace       string
	runControlRun             string
	runPromptText             string
	coordination              localapi.CoordinationStatusResult
	messageSend               localapi.MessageSendResult
	inboxList                 localapi.InboxListResult
	participantMutation       localapi.ParticipantThreadMutationResult
	participantThread         localapi.ParticipantThreadResult
	threadShow                localapi.ThreadShowResult
	messageSendParams         localapi.MessageSendParams
	threadCreateParams        localapi.ThreadCreateParams
	threadInviteParams        localapi.ThreadInviteParams
	threadParticipantsArgs    []string
	agentCreateParams         localapi.AgentCreateParams
	objectiveCreateParams     localapi.ObjectiveCreateParams
	taskCreateParams          localapi.TaskCreateParams
	taskAssignParams          localapi.TaskAssignParams
	contextBuildParams        localapi.ContextBuildParams
	contextRefresh            localapi.ContextRefreshResult
	contextRefreshParams      localapi.ContextRefreshParams
	contextDeltaList          localapi.ContextDeltaListResult
	contextDeltaListParams    localapi.ContextDeltaListParams
	contextDeltaShow          localapi.ContextDeltaShowResult
	contextDeltaExplain       localapi.ContextDeltaExplainResult
	contextDeltaQueryArgs     []string
	knowledgeMutation         localapi.KnowledgeMutationResult
	knowledgeShow             localapi.KnowledgeShowResult
	knowledgeList             localapi.KnowledgeListResult
	knowledgePropose          localapi.KnowledgeProposeParams
	knowledgeDecision         localapi.KnowledgeDecisionParams
	knowledgeStale            localapi.KnowledgeMarkStaleParams
	knowledgeDispute          localapi.KnowledgeDisputeResult
	knowledgeDisputeArgs      []string
	knowledgeExport           localapi.KnowledgeExportResult
	knowledgeImport           localapi.KnowledgeImportResult
	knowledgeExportParams     localapi.KnowledgeExportParams
	knowledgeImportParams     localapi.KnowledgeImportParams
	curatorQueue              localapi.CuratorQueueResult
	curatorRuleMutation       localapi.CuratorRuleMutationResult
	curatorProcess            localapi.CuratorProcessResult
	curatorQueueParams        localapi.CuratorQueueParams
	curatorRuleParams         localapi.CuratorRuleConfigureParams
	curatorProcessParams      localapi.CuratorProcessParams
	conMutation               localapi.ContradictionMutationResult
	conShow                   localapi.ContradictionShowResult
	conList                   localapi.ContradictionListResult
	conReportParams           localapi.ContradictionReportParams
	conListParams             localapi.ContradictionListParams
	conDecisionParams         localapi.ContradictionDecisionParams
	conAction                 string
	coordinationWorkspace     string
	initName                  string
	initKey                   string
	showIdentifier            string
	projectArgs               []string
	checkoutArgs              []string
	eventsAfter               int64
	eventsLimit               int
	outcomeCommitmentMutation localapi.OutcomeCommitmentMutationResult
	outcomeCommitmentShow     localapi.OutcomeCommitmentShowResult
	outcomeCommitmentList     localapi.OutcomeCommitmentListResult
	outcomeMutation           localapi.OutcomeMutationResult
	outcomeShow               localapi.OutcomeShowResult
	outcomeList               localapi.OutcomeListResult
	checkpointMutation        localapi.CheckpointMutationResult
	checkpointShow            localapi.CheckpointShowResult
	checkpointList            localapi.CheckpointListResult
	briefingShow              localapi.BriefingShowResult
	briefingExplain           localapi.BriefingExplainResult
	outcomeCommitmentParams   localapi.OutcomeCommitmentCreateParams
	outcomeCommitmentQuery    localapi.OutcomeCommitmentQueryParams
	outcomeProposeParams      localapi.OutcomeProposeParams
	outcomeQueryParams        localapi.OutcomeQueryParams
	outcomeDecisionParams     localapi.OutcomeDecisionParams
	outcomeDecisionAction     string
	checkpointCreateParams    localapi.CheckpointCreateParams
	checkpointQueryParams     localapi.CheckpointQueryParams
	briefingShowParams        localapi.BriefingShowParams
	briefingExplainParams     localapi.BriefingExplainParams
}

func (client *fakeDaemonClient) Status(context.Context) (localapi.StatusResult, error) {
	return client.status, client.statusErr
}

func (client *fakeDaemonClient) Stop(context.Context) (localapi.StopResult, error) {
	return client.stop, client.stopErr
}

func (client *fakeDaemonClient) DatabaseStatus(context.Context) (localapi.DatabaseStatusResult, error) {
	return client.databaseStatus, client.databaseStatusErr
}

func (client *fakeDaemonClient) SystemDoctorFull(context.Context) (localapi.FullDoctorResult, error) {
	return client.fullDoctor, nil
}

func (client *fakeDaemonClient) BackupCreate(_ context.Context, params localapi.BackupCreateParams) (localapi.BackupCreateResult, error) {
	client.backupCreateParams = params
	return client.backupCreate, nil
}

func (client *fakeDaemonClient) WebBootstrap(context.Context) (localapi.WebBootstrapResult, error) {
	client.webBootstrapCalls++
	if len(client.webBootstrapErrors) > 0 {
		err := client.webBootstrapErrors[0]
		client.webBootstrapErrors = client.webBootstrapErrors[1:]
		return localapi.WebBootstrapResult{}, err
	}
	return client.webBootstrap, client.webBootstrapErr
}

func (client *fakeDaemonClient) KnowledgeIndexStatus(_ context.Context, workspace string) (localapi.KnowledgeIndexStatusResult, error) {
	client.knowledgeIndexWorkspace = workspace
	return client.knowledgeIndexStatus, nil
}

func (client *fakeDaemonClient) WorkspaceInit(_ context.Context, name, key string) (localapi.WorkspaceInitResult, error) {
	client.initName = name
	client.initKey = key
	return client.workspaceInit, client.workspaceInitErr
}

func (client *fakeDaemonClient) WorkspaceShow(_ context.Context, identifier string) (localapi.WorkspaceShowResult, error) {
	client.showIdentifier = identifier
	return client.workspaceShow, client.workspaceShowErr
}

func (client *fakeDaemonClient) ProjectAdd(_ context.Context, workspace, name, path, mode, key string) (localapi.ProjectAddResult, error) {
	client.projectArgs = []string{workspace, name, path, mode, key}
	return client.projectAdd, client.projectAddErr
}

func (client *fakeDaemonClient) ProjectInspect(_ context.Context, workspace, project string) (localapi.ProjectInspectResult, error) {
	client.projectArgs = []string{workspace, project}
	return client.projectInspect, client.projectInspectErr
}

func (client *fakeDaemonClient) CheckoutAdd(_ context.Context, workspace, project, path, mode, key string) (localapi.CheckoutAddResult, error) {
	client.checkoutArgs = []string{workspace, project, path, mode, key}
	return client.checkoutAdd, client.checkoutAddErr
}

func (client *fakeDaemonClient) CheckoutList(_ context.Context, workspace, project string) (localapi.CheckoutListResult, error) {
	client.checkoutArgs = []string{workspace, project}
	return client.checkoutList, client.checkoutListErr
}

func (client *fakeDaemonClient) AgentCreate(_ context.Context, params localapi.AgentCreateParams) (localapi.AgentMutationResult, error) {
	client.agentCreateParams = params
	return client.agentMutation, nil
}

func (client *fakeDaemonClient) AgentUpdate(context.Context, localapi.AgentUpdateParams) (localapi.AgentMutationResult, error) {
	return client.agentMutation, nil
}

func (client *fakeDaemonClient) AgentShow(context.Context, string, string) (localapi.AgentShowResult, error) {
	return client.agentShow, nil
}

func (client *fakeDaemonClient) AgentList(context.Context, localapi.AgentListParams) (localapi.AgentListResult, error) {
	return client.agentList, nil
}

func (client *fakeDaemonClient) ObjectiveCreate(_ context.Context, params localapi.ObjectiveCreateParams) (localapi.ObjectiveMutationResult, error) {
	client.objectiveCreateParams = params
	return client.objectiveMutation, nil
}

func (client *fakeDaemonClient) ObjectiveUpdate(context.Context, localapi.ObjectiveUpdateParams) (localapi.ObjectiveMutationResult, error) {
	return client.objectiveMutation, nil
}

func (client *fakeDaemonClient) ObjectiveShow(context.Context, string, string) (localapi.ObjectiveShowResult, error) {
	return client.objectiveShow, nil
}

func (client *fakeDaemonClient) ObjectiveList(context.Context, localapi.ObjectiveListParams) (localapi.ObjectiveListResult, error) {
	return client.objectiveList, nil
}

func (client *fakeDaemonClient) TaskCreate(_ context.Context, params localapi.TaskCreateParams) (localapi.TaskMutationResult, error) {
	client.taskCreateParams = params
	return client.taskMutation, nil
}

func (client *fakeDaemonClient) TaskUpdate(context.Context, localapi.TaskUpdateParams) (localapi.TaskMutationResult, error) {
	return client.taskMutation, nil
}

func (client *fakeDaemonClient) TaskShow(context.Context, string, string) (localapi.TaskShowResult, error) {
	return client.taskShow, nil
}

func (client *fakeDaemonClient) TaskList(context.Context, localapi.TaskListParams) (localapi.TaskListResult, error) {
	return client.taskList, nil
}

func (client *fakeDaemonClient) TaskDepend(context.Context, localapi.TaskDependencyParams) (localapi.TaskMutationResult, error) {
	return client.taskMutation, nil
}

func (client *fakeDaemonClient) TaskAssign(_ context.Context, params localapi.TaskAssignParams) (localapi.TaskMutationResult, error) {
	client.taskAssignParams = params
	return client.taskMutation, nil
}

func (client *fakeDaemonClient) TaskTransition(context.Context, localapi.TaskTransitionParams) (localapi.TaskMutationResult, error) {
	return client.taskMutation, nil
}

func (client *fakeDaemonClient) TaskTimeline(context.Context, string, string) (localapi.TaskTimelineResult, error) {
	return client.taskTimeline, nil
}

func (client *fakeDaemonClient) ContextBuild(_ context.Context, params localapi.ContextBuildParams) (localapi.ContextBuildResult, error) {
	client.contextBuildParams = params
	return localapi.ContextBuildResult{}, nil
}

func (client *fakeDaemonClient) ContextShow(context.Context, string, string) (localapi.ContextShowResult, error) {
	return localapi.ContextShowResult{}, nil
}

func (client *fakeDaemonClient) ContextExplain(context.Context, string, string) (localapi.ContextExplainResult, error) {
	return localapi.ContextExplainResult{}, nil
}

func (client *fakeDaemonClient) ContextRefresh(_ context.Context, params localapi.ContextRefreshParams) (localapi.ContextRefreshResult, error) {
	client.contextRefreshParams = params
	return client.contextRefresh, nil
}

func (client *fakeDaemonClient) ContextDeltaList(_ context.Context, params localapi.ContextDeltaListParams) (localapi.ContextDeltaListResult, error) {
	client.contextDeltaListParams = params
	return client.contextDeltaList, nil
}

func (client *fakeDaemonClient) ContextDeltaShow(_ context.Context, workspace, delta string) (localapi.ContextDeltaShowResult, error) {
	client.contextDeltaQueryArgs = []string{workspace, delta}
	return client.contextDeltaShow, nil
}

func (client *fakeDaemonClient) ContextDeltaExplain(_ context.Context, workspace, delta string) (localapi.ContextDeltaExplainResult, error) {
	client.contextDeltaQueryArgs = []string{workspace, delta}
	return client.contextDeltaExplain, nil
}

func (client *fakeDaemonClient) KnowledgePropose(_ context.Context, params localapi.KnowledgeProposeParams) (localapi.KnowledgeMutationResult, error) {
	client.knowledgePropose = params
	return client.knowledgeMutation, nil
}

func (client *fakeDaemonClient) KnowledgeShow(context.Context, string, string) (localapi.KnowledgeShowResult, error) {
	return client.knowledgeShow, nil
}

func (client *fakeDaemonClient) KnowledgeList(context.Context, localapi.KnowledgeListParams) (localapi.KnowledgeListResult, error) {
	return client.knowledgeList, nil
}

func (client *fakeDaemonClient) KnowledgeSearch(_ context.Context, params localapi.KnowledgeSearchParams) (localapi.KnowledgeSearchResult, error) {
	client.knowledgeSearchParams = params
	return client.knowledgeSearch, nil
}

func (client *fakeDaemonClient) KnowledgeIndexRebuild(_ context.Context, params localapi.KnowledgeIndexRebuildParams) (localapi.KnowledgeIndexRebuildResult, error) {
	client.knowledgeRebuildParams = params
	return client.knowledgeIndexRebuild, nil
}

func (client *fakeDaemonClient) KnowledgeAccept(_ context.Context, params localapi.KnowledgeDecisionParams) (localapi.KnowledgeMutationResult, error) {
	client.knowledgeDecision = params
	return client.knowledgeMutation, nil
}

func (client *fakeDaemonClient) KnowledgeReject(_ context.Context, params localapi.KnowledgeDecisionParams) (localapi.KnowledgeMutationResult, error) {
	client.knowledgeDecision = params
	return client.knowledgeMutation, nil
}

func (client *fakeDaemonClient) KnowledgeMarkStale(_ context.Context, params localapi.KnowledgeMarkStaleParams) (localapi.KnowledgeMutationResult, error) {
	client.knowledgeStale = params
	return client.knowledgeMutation, nil
}

func (client *fakeDaemonClient) KnowledgeDispute(_ context.Context, workspace, revision string) (localapi.KnowledgeDisputeResult, error) {
	client.knowledgeDisputeArgs = []string{workspace, revision}
	return client.knowledgeDispute, nil
}

func (client *fakeDaemonClient) KnowledgeExport(_ context.Context, params localapi.KnowledgeExportParams) (localapi.KnowledgeExportResult, error) {
	client.knowledgeExportParams = params
	return client.knowledgeExport, nil
}

func (client *fakeDaemonClient) KnowledgeImport(_ context.Context, params localapi.KnowledgeImportParams) (localapi.KnowledgeImportResult, error) {
	client.knowledgeImportParams = params
	return client.knowledgeImport, nil
}

func (client *fakeDaemonClient) CuratorQueue(_ context.Context, params localapi.CuratorQueueParams) (localapi.CuratorQueueResult, error) {
	client.curatorQueueParams = params
	return client.curatorQueue, nil
}

func (client *fakeDaemonClient) CuratorRuleConfigure(_ context.Context, params localapi.CuratorRuleConfigureParams) (localapi.CuratorRuleMutationResult, error) {
	client.curatorRuleParams = params
	return client.curatorRuleMutation, nil
}

func (client *fakeDaemonClient) CuratorProcess(_ context.Context, params localapi.CuratorProcessParams) (localapi.CuratorProcessResult, error) {
	client.curatorProcessParams = params
	return client.curatorProcess, nil
}

func (client *fakeDaemonClient) ContradictionReport(_ context.Context, params localapi.ContradictionReportParams) (localapi.ContradictionMutationResult, error) {
	client.conReportParams = params
	return client.conMutation, nil
}

func (client *fakeDaemonClient) ContradictionShow(context.Context, string, string) (localapi.ContradictionShowResult, error) {
	return client.conShow, nil
}

func (client *fakeDaemonClient) ContradictionList(_ context.Context, params localapi.ContradictionListParams) (localapi.ContradictionListResult, error) {
	client.conListParams = params
	return client.conList, nil
}

func (client *fakeDaemonClient) ContradictionConfirm(_ context.Context, params localapi.ContradictionDecisionParams) (localapi.ContradictionMutationResult, error) {
	client.conDecisionParams = params
	client.conAction = "confirm"
	return client.conMutation, nil
}

func (client *fakeDaemonClient) ContradictionDismiss(_ context.Context, params localapi.ContradictionDecisionParams) (localapi.ContradictionMutationResult, error) {
	client.conDecisionParams = params
	client.conAction = "dismiss"
	return client.conMutation, nil
}

func (client *fakeDaemonClient) MessageSend(_ context.Context, params localapi.MessageSendParams) (localapi.MessageSendResult, error) {
	client.messageSendParams = params
	return client.messageSend, nil
}

func (client *fakeDaemonClient) InboxList(context.Context, string, string, int) (localapi.InboxListResult, error) {
	return client.inboxList, nil
}

func (client *fakeDaemonClient) ThreadCreate(_ context.Context, params localapi.ThreadCreateParams) (localapi.ParticipantThreadMutationResult, error) {
	client.threadCreateParams = params
	return client.participantMutation, nil
}

func (client *fakeDaemonClient) ThreadInvite(_ context.Context, params localapi.ThreadInviteParams) (localapi.ParticipantThreadMutationResult, error) {
	client.threadInviteParams = params
	return client.participantMutation, nil
}

func (client *fakeDaemonClient) ThreadParticipants(_ context.Context, workspace, thread string) (localapi.ParticipantThreadResult, error) {
	client.threadParticipantsArgs = []string{workspace, thread}
	return client.participantThread, nil
}

func (client *fakeDaemonClient) ThreadShow(context.Context, string, string) (localapi.ThreadShowResult, error) {
	return client.threadShow, nil
}

func (client *fakeDaemonClient) RunStart(_ context.Context, params localapi.RunStartParams) (localapi.RunMutationResult, error) {
	client.runStartParams = params
	return client.runMutation, nil
}

func (client *fakeDaemonClient) RunShow(context.Context, string, string) (localapi.RunShowResult, error) {
	return client.runShow, nil
}

func (client *fakeDaemonClient) RunList(context.Context, localapi.RunListParams) (localapi.RunListResult, error) {
	return client.runList, nil
}

func (client *fakeDaemonClient) RunResume(_ context.Context, params localapi.RunResumeParams) (localapi.RunMutationResult, error) {
	client.runResumeParams = params
	return client.runMutation, nil
}

func (client *fakeDaemonClient) RunStop(_ context.Context, params localapi.RunStopParams) (localapi.RunMutationResult, error) {
	client.runStopParams = params
	return client.runMutation, nil
}

func (client *fakeDaemonClient) RunLostResolve(_ context.Context, params localapi.RunLostResolveParams) (localapi.RunLossResolutionResult, error) {
	client.runLostResolveParams = params
	return client.runLossResolution, nil
}

func (client *fakeDaemonClient) RunLogs(_ context.Context, workspace, run string, tail int) (localapi.RunLogsResult, error) {
	client.runLogsWorkspace = workspace
	client.runLogsRun = run
	client.runLogsTail = tail
	return client.runLogs, nil
}

func (client *fakeDaemonClient) RunPrompt(_ context.Context, workspace, run, text string) (localapi.RunControlResult, error) {
	client.runControlWorkspace, client.runControlRun, client.runPromptText = workspace, run, text
	return client.runControl, nil
}

func (client *fakeDaemonClient) RunInterrupt(_ context.Context, workspace, run string) (localapi.RunControlResult, error) {
	client.runControlWorkspace, client.runControlRun = workspace, run
	return client.runControl, nil
}

func (client *fakeDaemonClient) RunAttach(_ context.Context, workspace, run string) (localapi.RunAttachResult, error) {
	client.runControlWorkspace, client.runControlRun = workspace, run
	return client.runAttach, nil
}

func (client *fakeDaemonClient) CoordinationStatus(_ context.Context, workspace string) (localapi.CoordinationStatusResult, error) {
	client.coordinationWorkspace = workspace
	return client.coordination, nil
}

func (client *fakeDaemonClient) ClaimAdd(context.Context, localapi.ClaimAddParams) (localapi.ClaimMutationResult, error) {
	return localapi.ClaimMutationResult{}, nil
}

func (client *fakeDaemonClient) ClaimList(context.Context, localapi.ClaimListParams) (localapi.ClaimListResult, error) {
	return localapi.ClaimListResult{}, nil
}

func (client *fakeDaemonClient) ClaimRelease(context.Context, localapi.ClaimReleaseParams) (localapi.ClaimMutationResult, error) {
	return localapi.ClaimMutationResult{}, nil
}

func (client *fakeDaemonClient) OverlapList(context.Context, localapi.OverlapListParams) (localapi.OverlapListResult, error) {
	return localapi.OverlapListResult{}, nil
}

func (client *fakeDaemonClient) OverlapInspect(context.Context, string, string) (localapi.OverlapInspectResult, error) {
	return localapi.OverlapInspectResult{}, nil
}

func (client *fakeDaemonClient) OverlapScan(context.Context, string, string) (localapi.OverlapScanResult, error) {
	return localapi.OverlapScanResult{}, nil
}

func (client *fakeDaemonClient) DriftList(context.Context, localapi.DriftListParams) (localapi.DriftListResult, error) {
	return localapi.DriftListResult{}, nil
}

func (client *fakeDaemonClient) MeetingCreate(context.Context, localapi.MeetingCreateParams) (localapi.MeetingMutationResult, error) {
	return localapi.MeetingMutationResult{}, nil
}

func (client *fakeDaemonClient) MeetingRun(context.Context, localapi.MeetingRunParams) (localapi.MeetingMutationResult, error) {
	return localapi.MeetingMutationResult{}, nil
}

func (client *fakeDaemonClient) MeetingInspect(context.Context, string, string) (localapi.MeetingInspectResult, error) {
	return localapi.MeetingInspectResult{}, nil
}

func (client *fakeDaemonClient) MeetingAccept(context.Context, localapi.MeetingAcceptParams) (localapi.MeetingMutationResult, error) {
	return localapi.MeetingMutationResult{}, nil
}

func (client *fakeDaemonClient) MeetingTakeover(context.Context, localapi.MeetingTakeoverParams) (localapi.MeetingMutationResult, error) {
	return localapi.MeetingMutationResult{}, nil
}

func (client *fakeDaemonClient) ManagerGrantCreate(context.Context, localapi.ManagerGrantCreateParams) (localapi.ManagerGrantMutationResult, error) {
	return localapi.ManagerGrantMutationResult{}, nil
}

func (client *fakeDaemonClient) ManagerGrantRevoke(context.Context, localapi.ManagerGrantRevokeParams) (localapi.ManagerGrantMutationResult, error) {
	return localapi.ManagerGrantMutationResult{}, nil
}

func (client *fakeDaemonClient) ManagerGrantShow(context.Context, string, string) (localapi.ManagerGrantShowResult, error) {
	return localapi.ManagerGrantShowResult{}, nil
}

func (client *fakeDaemonClient) ManagerGrantList(context.Context, localapi.ManagerGrantQueryParams) (localapi.ManagerGrantListResult, error) {
	return localapi.ManagerGrantListResult{}, nil
}

func (client *fakeDaemonClient) LaunchProfileCreate(context.Context, localapi.LaunchProfileCreateParams) (localapi.LaunchProfileMutationResult, error) {
	return localapi.LaunchProfileMutationResult{}, nil
}

func (client *fakeDaemonClient) LaunchProfileRetire(context.Context, localapi.LaunchProfileRetireParams) (localapi.LaunchProfileMutationResult, error) {
	return localapi.LaunchProfileMutationResult{}, nil
}

func (client *fakeDaemonClient) LaunchProfileShow(context.Context, string, string) (localapi.LaunchProfileShowResult, error) {
	return localapi.LaunchProfileShowResult{}, nil
}

func (client *fakeDaemonClient) LaunchProfileList(context.Context, localapi.LaunchProfileQueryParams) (localapi.LaunchProfileListResult, error) {
	return localapi.LaunchProfileListResult{}, nil
}

func (client *fakeDaemonClient) ManagerInvoke(context.Context, localapi.ManagerInvokeParams) (localapi.ManagerInvocationResult, error) {
	return localapi.ManagerInvocationResult{}, nil
}

func (client *fakeDaemonClient) ProposalList(context.Context, localapi.ProposalQueryParams) (localapi.ProposalListResult, error) {
	return localapi.ProposalListResult{}, nil
}

func (client *fakeDaemonClient) ProposalInspect(context.Context, string, string) (localapi.ProposalShowResult, error) {
	return localapi.ProposalShowResult{}, nil
}

func (client *fakeDaemonClient) ProposalAccept(context.Context, localapi.ProposalDecisionParams) (localapi.ProposalMutationResult, error) {
	return localapi.ProposalMutationResult{}, nil
}

func (client *fakeDaemonClient) ProposalReject(context.Context, localapi.ProposalDecisionParams) (localapi.ProposalMutationResult, error) {
	return localapi.ProposalMutationResult{}, nil
}

func (client *fakeDaemonClient) SupervisorPolicyShow(context.Context, string) (localapi.SupervisorPolicyShowResult, error) {
	return localapi.SupervisorPolicyShowResult{}, nil
}

func (client *fakeDaemonClient) SupervisorPolicyConfigure(context.Context, localapi.SupervisorPolicyConfigureParams) (localapi.SupervisorPolicyMutationResult, error) {
	return localapi.SupervisorPolicyMutationResult{}, nil
}

func (client *fakeDaemonClient) SupervisorRun(context.Context, localapi.SupervisorRunParams) (localapi.SupervisorRunResult, error) {
	return localapi.SupervisorRunResult{}, nil
}

func (client *fakeDaemonClient) SupervisorActionList(context.Context, localapi.SupervisorActionQueryParams) (localapi.SupervisorActionListResult, error) {
	return localapi.SupervisorActionListResult{}, nil
}

func (client *fakeDaemonClient) SupervisorActionShow(context.Context, string, string) (localapi.SupervisorActionShowResult, error) {
	return localapi.SupervisorActionShowResult{}, nil
}

func (client *fakeDaemonClient) SupervisorExplain(context.Context, localapi.SupervisorExplainParams) (localapi.SupervisorExplanationResult, error) {
	return localapi.SupervisorExplanationResult{}, nil
}

func (client *fakeDaemonClient) ApprovalList(context.Context, localapi.ApprovalListParams) (localapi.ApprovalListResult, error) {
	return localapi.ApprovalListResult{}, nil
}

func (client *fakeDaemonClient) ApprovalInspect(context.Context, string, string) (localapi.ApprovalShowResult, error) {
	return localapi.ApprovalShowResult{}, nil
}

func (client *fakeDaemonClient) ApprovalAllow(context.Context, localapi.ApprovalDecisionParams) (localapi.ApprovalMutationResult, error) {
	return localapi.ApprovalMutationResult{}, nil
}

func (client *fakeDaemonClient) ApprovalDeny(context.Context, localapi.ApprovalDecisionParams) (localapi.ApprovalMutationResult, error) {
	return localapi.ApprovalMutationResult{}, nil
}

func (client *fakeDaemonClient) EventsList(_ context.Context, params localapi.EventsListParams) (localapi.EventsListResult, error) {
	client.eventsAfter = params.After
	client.eventsLimit = params.Limit
	return client.eventsList, client.eventsListErr
}

func (client *fakeDaemonClient) OutcomeCommitmentCreate(_ context.Context, params localapi.OutcomeCommitmentCreateParams) (localapi.OutcomeCommitmentMutationResult, error) {
	client.outcomeCommitmentParams = params
	return client.outcomeCommitmentMutation, nil
}

func (client *fakeDaemonClient) OutcomeCommitmentShow(_ context.Context, workspace, commitment string) (localapi.OutcomeCommitmentShowResult, error) {
	client.outcomeCommitmentQuery = localapi.OutcomeCommitmentQueryParams{Workspace: workspace, Commitment: commitment}
	return client.outcomeCommitmentShow, nil
}

func (client *fakeDaemonClient) OutcomeCommitmentList(_ context.Context, params localapi.OutcomeCommitmentQueryParams) (localapi.OutcomeCommitmentListResult, error) {
	client.outcomeCommitmentQuery = params
	return client.outcomeCommitmentList, nil
}

func (client *fakeDaemonClient) OutcomePropose(_ context.Context, params localapi.OutcomeProposeParams) (localapi.OutcomeMutationResult, error) {
	client.outcomeProposeParams = params
	return client.outcomeMutation, nil
}

func (client *fakeDaemonClient) OutcomeShow(_ context.Context, workspace, outcome string) (localapi.OutcomeShowResult, error) {
	client.outcomeQueryParams = localapi.OutcomeQueryParams{Workspace: workspace, Outcome: outcome}
	return client.outcomeShow, nil
}

func (client *fakeDaemonClient) OutcomeList(_ context.Context, params localapi.OutcomeQueryParams) (localapi.OutcomeListResult, error) {
	client.outcomeQueryParams = params
	return client.outcomeList, nil
}

func (client *fakeDaemonClient) OutcomeAccept(_ context.Context, params localapi.OutcomeDecisionParams) (localapi.OutcomeMutationResult, error) {
	client.outcomeDecisionParams = params
	client.outcomeDecisionAction = "accept"
	return client.outcomeMutation, nil
}

func (client *fakeDaemonClient) OutcomeReject(_ context.Context, params localapi.OutcomeDecisionParams) (localapi.OutcomeMutationResult, error) {
	client.outcomeDecisionParams = params
	client.outcomeDecisionAction = "reject"
	return client.outcomeMutation, nil
}

func (client *fakeDaemonClient) CheckpointCreate(_ context.Context, params localapi.CheckpointCreateParams) (localapi.CheckpointMutationResult, error) {
	client.checkpointCreateParams = params
	return client.checkpointMutation, nil
}

func (client *fakeDaemonClient) CheckpointShow(_ context.Context, workspace, checkpoint string) (localapi.CheckpointShowResult, error) {
	client.checkpointQueryParams = localapi.CheckpointQueryParams{Workspace: workspace, Checkpoint: checkpoint}
	return client.checkpointShow, nil
}

func (client *fakeDaemonClient) CheckpointList(_ context.Context, params localapi.CheckpointQueryParams) (localapi.CheckpointListResult, error) {
	client.checkpointQueryParams = params
	return client.checkpointList, nil
}

func (client *fakeDaemonClient) BriefingShow(_ context.Context, params localapi.BriefingShowParams) (localapi.BriefingShowResult, error) {
	client.briefingShowParams = params
	return client.briefingShow, nil
}

func (client *fakeDaemonClient) BriefingExplain(_ context.Context, params localapi.BriefingExplainParams) (localapi.BriefingExplainResult, error) {
	client.briefingExplainParams = params
	return client.briefingExplain, nil
}

func newTestApp() (*App, *bytes.Buffer, *bytes.Buffer) {
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	return New(stdout, stderr, buildinfo.Current()), stdout, stderr
}
