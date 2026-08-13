package localapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func TestCuratorQueueClientSendsBoundedDefaultWithoutAuthorityFields(t *testing.T) {
	t.Parallel()
	request := captureCuratorRequest(t, MethodCuratorQueue, func(client *Client) error {
		_, err := client.CuratorQueue(context.Background(), CuratorQueueParams{Workspace: "personal", Project: "engine"})
		return err
	}, CuratorQueueResult{Schema: CuratorQueueSchema, Type: "curator_queue"})
	var params CuratorQueueParams
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Limit == nil || *params.Limit != 50 || params.Workspace != "personal" || params.Project != "engine" {
		t.Fatalf("curator queue params = %#v", params)
	}
	if strings.Contains(string(request.Params), "actor") {
		t.Fatalf("owner-only request exposed caller authority: %s", request.Params)
	}
}

func TestCuratorMutationClientsPreserveExplicitPolicyAndGenerateIdempotency(t *testing.T) {
	t.Parallel()
	enabled := false
	ruleRequest := captureCuratorRequest(t, MethodCuratorRuleConfigure, func(client *Client) error {
		_, err := client.CuratorRuleConfigure(context.Background(), CuratorRuleConfigureParams{
			Workspace: "personal", Rule: "accepted_meeting_resolution_copy/v1", Enabled: &enabled, ExpectedRevision: 3,
		})
		return err
	}, CuratorRuleMutationResult{Schema: CuratorRuleMutationSchema, Type: "curator_rule_mutation"})
	var rule CuratorRuleConfigureParams
	if err := json.Unmarshal(ruleRequest.Params, &rule); err != nil {
		t.Fatal(err)
	}
	if rule.Enabled == nil || *rule.Enabled || rule.ExpectedRevision != 3 || strings.TrimSpace(rule.IdempotencyKey) == "" {
		t.Fatalf("curator rule params = %#v", rule)
	}

	processRequest := captureCuratorRequest(t, MethodCuratorProcess, func(client *Client) error {
		_, err := client.CuratorProcess(context.Background(), CuratorProcessParams{
			Workspace: "personal", Project: "engine", ApplySafe: true,
		})
		return err
	}, CuratorProcessResult{Schema: CuratorProcessSchema, Type: "curator_process"})
	var process CuratorProcessParams
	if err := json.Unmarshal(processRequest.Params, &process); err != nil {
		t.Fatal(err)
	}
	if !process.ApplySafe || strings.TrimSpace(process.IdempotencyKey) == "" {
		t.Fatalf("curator process params = %#v", process)
	}
	deriveOnlyRequest := captureCuratorRequest(t, MethodCuratorProcess, func(client *Client) error {
		_, err := client.CuratorProcess(context.Background(), CuratorProcessParams{
			Workspace: "personal", Project: "engine",
		})
		return err
	}, CuratorProcessResult{Schema: CuratorProcessSchema, Type: "curator_process"})
	var deriveOnly CuratorProcessParams
	if err := json.Unmarshal(deriveOnlyRequest.Params, &deriveOnly); err != nil {
		t.Fatal(err)
	}
	if deriveOnly.ApplySafe || strings.TrimSpace(deriveOnly.IdempotencyKey) == "" || strings.Contains(string(deriveOnlyRequest.Params), `"apply_safe"`) {
		t.Fatalf("derive-only curator process params = %#v, JSON=%s", deriveOnly, deriveOnlyRequest.Params)
	}
	for _, request := range []Request{ruleRequest, processRequest, deriveOnlyRequest} {
		if strings.Contains(string(request.Params), "actor") {
			t.Fatalf("owner-only request exposed caller authority: %s", request.Params)
		}
	}
}

func captureCuratorRequest(t *testing.T, method string, call func(*Client) error, result any) Request {
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
		decoder := json.NewDecoder(connection)
		encoder := json.NewEncoder(connection)
		var hello Request
		if err := decoder.Decode(&hello); err != nil {
			serverResult <- err
			return
		}
		if hello.Method != MethodHello {
			serverResult <- fmt.Errorf("first method = %q, want %q", hello.Method, MethodHello)
			return
		}
		if err := encoder.Encode(MarshalResult(hello.ID, MaxProtocol, HelloResult{
			Type: "hello", SelectedProtocol: MaxProtocol, ServerMin: MinProtocol, ServerMax: MaxProtocol,
		})); err != nil {
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
		serverResult <- encoder.Encode(MarshalResult(request.ID, request.Protocol, result))
	}()
	if err := call(NewClient(socketPath)); err != nil {
		t.Fatalf("%s client call error = %v", method, err)
	}
	if err := <-serverResult; err != nil {
		t.Fatalf("fake local API server error = %v", err)
	}
	return <-captured
}
