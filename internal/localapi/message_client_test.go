package localapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"testing"

	"crewfold/internal/domain"
)

func TestInboxResultPreservesHonestCheckSubsystemSenderWithoutAgentIdentity(t *testing.T) {
	t.Parallel()
	payload := []byte(`{"schema":"urn:crewfold:schema:local-api:inbox-list-result:v1","type":"inbox","agent":"change-agent","items":[{"message":{"id":"msg_00000000000000000000000000000000","workspace_id":"ws_00000000000000000000000000000000","thread_id":"thread_00000000000000000000000000000000","project_id":"prj_00000000000000000000000000000000","task_id":"task_00000000000000000000000000000000","sender_type":"subsystem","sender_id":"crewfold-check-worker","kind":"inform","body":"The exact local check failed.","artifact_ids":[],"created_at":"2026-08-13T20:00:00Z"},"delivery":{"message_id":"msg_00000000000000000000000000000000","recipient_agent_id":"agent_00000000000000000000000000000000","recipient_name":"change-agent","status":"queued","queued_at":"2026-08-13T20:00:00Z","wake_status":"not_requested"}}]}`)
	var result InboxListResult
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Items) != 1 {
		t.Fatalf("inbox items = %d", len(result.Items))
	}
	message := result.Items[0].Message
	if message.SenderType != "subsystem" || message.SenderID != "crewfold-check-worker" ||
		message.SenderAgentID != "" || message.SenderAgentName != "" || message.SenderRunID != "" ||
		message.Kind != domain.MessageInform {
		t.Fatalf("subsystem message = %#v", message)
	}
	roundTrip, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(roundTrip, &object); err != nil {
		t.Fatal(err)
	}
	items := object["items"].([]any)
	wireMessage := items[0].(map[string]any)["message"].(map[string]any)
	for _, absent := range []string{"sender_agent_id", "sender_agent_name", "sender_run_id"} {
		if _, exists := wireMessage[absent]; exists {
			t.Errorf("subsystem message exposes %s", absent)
		}
	}
}

func TestParticipantThreadClientsDefaultStableKeysAndPreserveBindings(t *testing.T) {
	t.Parallel()

	create := captureThreadClientRequest(t, MethodThreadCreate, func(client *Client) error {
		_, err := client.ThreadCreate(context.Background(), ThreadCreateParams{
			Workspace: "personal", Subject: "Align engine contract",
			Participants: []ThreadParticipantParams{{Agent: "consumer", Task: "task_consumer"}, {Agent: "engine", Task: "task_engine"}},
		})
		return err
	}, ParticipantThreadMutationResult{Schema: ParticipantThreadMutationSchema, Type: "participant_thread_mutation"})
	var createParams ThreadCreateParams
	if err := json.Unmarshal(create.Params, &createParams); err != nil {
		t.Fatal(err)
	}
	if createParams.IdempotencyKey == "" || len(createParams.Participants) != 2 || createParams.Participants[1].Agent != "engine" {
		t.Fatalf("thread.create params = %#v", createParams)
	}
	assertOwnerIdentityAbsent(t, create.Params)

	invite := captureThreadClientRequest(t, MethodThreadInvite, func(client *Client) error {
		_, err := client.ThreadInvite(context.Background(), ThreadInviteParams{
			Workspace: "personal", Thread: "thread_00000000000000000000000000000001",
			Participant:                 ThreadParticipantParams{Agent: "reviewer", Task: "task_reviewer"},
			ExpectedParticipantRevision: 2,
		})
		return err
	}, ParticipantThreadMutationResult{Schema: ParticipantThreadMutationSchema, Type: "participant_thread_mutation"})
	var inviteParams ThreadInviteParams
	if err := json.Unmarshal(invite.Params, &inviteParams); err != nil {
		t.Fatal(err)
	}
	if inviteParams.IdempotencyKey == "" || inviteParams.ExpectedParticipantRevision != 2 || inviteParams.Participant.Task != "task_reviewer" {
		t.Fatalf("thread.invite params = %#v", inviteParams)
	}
	assertOwnerIdentityAbsent(t, invite.Params)
}

func TestThreadParticipantsClientUsesReadOnlyQueryShape(t *testing.T) {
	t.Parallel()
	request := captureThreadClientRequest(t, MethodThreadParticipants, func(client *Client) error {
		_, err := client.ThreadParticipants(context.Background(), "personal", "thread_00000000000000000000000000000001")
		return err
	}, ParticipantThreadResult{Schema: ParticipantThreadSchema, Type: "participant_thread"})
	var params map[string]any
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatal(err)
	}
	if len(params) != 2 || params["workspace"] != "personal" || params["thread"] == "" {
		t.Fatalf("thread.participants.list params = %#v", params)
	}
	if _, exists := params["idempotency_key"]; exists {
		t.Fatal("read-only participant query contains idempotency_key")
	}
}

func TestThreadListClientUsesBoundedProjectQuery(t *testing.T) {
	t.Parallel()
	request := captureThreadClientRequest(t, MethodThreadList, func(client *Client) error {
		_, err := client.ThreadList(context.Background(), "ws_00000000000000000000000000000001", "prj_00000000000000000000000000000001", 25)
		return err
	}, ThreadListResult{Schema: ThreadListSchema, Type: "thread_list", Threads: []domain.ThreadSummary{}})
	var params map[string]any
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatal(err)
	}
	if len(params) != 3 || params["workspace"] != "ws_00000000000000000000000000000001" || params["project"] != "prj_00000000000000000000000000000001" || params["limit"] != float64(25) {
		t.Fatalf("thread.list params = %#v", params)
	}
}

func captureThreadClientRequest(t *testing.T, method string, invoke func(*Client) error, response any) Request {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "local-api.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	captured := make(chan Request, 1)
	serverResult := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverResult <- err
			return
		}
		defer connection.Close()
		decoder, encoder := json.NewDecoder(connection), json.NewEncoder(connection)
		var hello Request
		if err := decoder.Decode(&hello); err != nil {
			serverResult <- err
			return
		}
		if hello.Method != MethodHello {
			serverResult <- fmt.Errorf("first method = %q, want %q", hello.Method, MethodHello)
			return
		}
		if err := encoder.Encode(MarshalResult(hello.ID, MaxProtocol, HelloResult{Type: "hello", SelectedProtocol: MaxProtocol, ServerMin: MinProtocol, ServerMax: MaxProtocol})); err != nil {
			serverResult <- err
			return
		}
		var request Request
		if err := decoder.Decode(&request); err != nil {
			serverResult <- err
			return
		}
		if request.Method != method {
			serverResult <- fmt.Errorf("second method = %q, want %q", request.Method, method)
			return
		}
		captured <- request
		serverResult <- encoder.Encode(MarshalResult(request.ID, request.Protocol, response))
	}()

	if err := invoke(NewClient(socketPath)); err != nil {
		t.Fatalf("client call error = %v", err)
	}
	if err := <-serverResult; err != nil {
		t.Fatalf("fake local API server error = %v", err)
	}
	return <-captured
}

func assertOwnerIdentityAbsent(t *testing.T, data []byte) {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"owner", "actor", "created_by", "invited_by"} {
		if _, exists := object[forbidden]; exists {
			t.Errorf("client params expose caller-selectable authority field %q", forbidden)
		}
	}
}
