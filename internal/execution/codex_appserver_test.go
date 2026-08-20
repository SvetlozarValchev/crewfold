package execution

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"
)

func TestM22CodexAppServerUsesDurableThreadLifecycleAndFaithfulNotifications(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	client, err := NewCodexAppServerClient(clientSide)
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		defer serverSide.Close()
		scanner := bufio.NewScanner(serverSide)
		encoder := json.NewEncoder(serverSide)
		methods := []string{"initialize", "initialized", "thread/start", "thread/name/set", "thread/read", "thread/turns/list", "turn/start", "turn/interrupt", "thread/compact/start", "thread/resume"}
		for _, want := range methods {
			if !scanner.Scan() {
				t.Errorf("missing %s: %v", want, scanner.Err())
				return
			}
			var request map[string]any
			if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
				t.Errorf("decode %s: %v", want, err)
				return
			}
			if request["method"] != want {
				t.Errorf("method = %v, want %s", request["method"], want)
				return
			}
			id, hasID := request["id"]
			if want == "initialized" {
				if hasID {
					t.Errorf("initialized carried id %v", id)
					return
				}
				continue
			}
			var result any
			switch want {
			case "initialize":
				result = map[string]any{"codexHome": "/private/codex", "platformFamily": "unix", "platformOs": "linux", "userAgent": "fixture"}
			case "thread/start", "thread/read", "thread/resume":
				params := request["params"].(map[string]any)
				if want == "thread/start" && params["ephemeral"] != false {
					t.Errorf("thread/start ephemeral = %#v", params["ephemeral"])
					return
				}
				result = map[string]any{"thread": map[string]any{"id": "019-thread", "cwd": "/work", "ephemeral": false, "modelProvider": "openai", "status": map[string]any{"type": "idle"}, "turns": []any{}}}
			case "thread/turns/list":
				params := request["params"].(map[string]any)
				if params["threadId"] != "019-thread" || params["itemsView"] != "full" || params["sortDirection"] != "desc" || params["limit"] != float64(100) {
					t.Errorf("thread/turns/list params = %#v", params)
					return
				}
				result = map[string]any{"data": []any{
					map[string]any{"id": "turn-newest", "status": "completed", "items": []any{}},
					map[string]any{"id": "turn-oldest", "status": "completed", "items": []any{}},
				}}
			case "thread/name/set":
				params := request["params"].(map[string]any)
				if params["threadId"] != "019-thread" || params["name"] != "Crewfold: orchid" {
					t.Errorf("thread/name/set params = %#v", params)
					return
				}
				result = map[string]any{}
			case "turn/start":
				params := request["params"].(map[string]any)
				if params["clientUserMessageId"] != "run-message" || params["cwd"] != "/work/task" || params["approvalPolicy"] != "never" {
					t.Errorf("turn/start authority params = %#v", params)
					return
				}
				policy, ok := params["sandboxPolicy"].(map[string]any)
				if !ok || policy["type"] != "workspaceWrite" || policy["networkAccess"] != true {
					t.Errorf("turn/start sandbox policy = %#v", params["sandboxPolicy"])
					return
				}
				result = map[string]any{"turn": map[string]any{"id": "turn-1", "status": "inProgress", "items": []any{}}}
			case "turn/interrupt", "thread/compact/start":
				result = map[string]any{}
			}
			if err := encoder.Encode(map[string]any{"id": id, "result": result}); err != nil {
				t.Errorf("respond %s: %v", want, err)
				return
			}
			if want == "thread/compact/start" {
				_ = encoder.Encode(map[string]any{"method": "item/completed", "params": map[string]any{
					"threadId": "019-thread",
					"turnId":   "turn-compact",
					"item":     map[string]any{"id": "item-compact", "type": "contextCompaction"},
				}})
			}
			if want == "turn/start" {
				_ = encoder.Encode(map[string]any{"method": "item/agentMessage/delta", "params": map[string]any{"threadId": "019-thread", "turnId": "turn-1", "itemId": "item-1", "delta": "exact provider text"}})
				_ = encoder.Encode(map[string]any{"id": 77, "method": "item/commandExecution/requestApproval", "params": map[string]any{"threadId": "019-thread", "turnId": "turn-1"}})
				if !scanner.Scan() {
					t.Errorf("missing explicit approval response: %v", scanner.Err())
					return
				}
				var response map[string]any
				if err := json.Unmarshal(scanner.Bytes(), &response); err != nil || response["id"] != float64(77) || response["error"] == nil {
					t.Errorf("approval response = %#v, %v", response, err)
					return
				}
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	thread, err := client.StartThread(ctx, CodexThreadStartParams{CWD: "/work", Ephemeral: false})
	if err != nil || thread.ID != "019-thread" {
		t.Fatalf("StartThread() = %#v, %v", thread, err)
	}
	if err := client.SetThreadName(ctx, thread.ID, "Crewfold: orchid"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ReadThread(ctx, thread.ID); err != nil {
		t.Fatal(err)
	}
	turns, err := client.ListThreadTurns(ctx, thread.ID, 100)
	if err != nil || len(turns) != 2 || turns[0].ID != "turn-oldest" || turns[1].ID != "turn-newest" {
		t.Fatalf("ListThreadTurns() = %#v, %v", turns, err)
	}
	turn, err := client.StartTurnWithOptions(ctx, thread.ID, "continue exactly", CodexTurnStartOptions{
		ClientMessageID: "run-message", CWD: "/work/task", ApprovalPolicy: "never",
		SandboxPolicy: map[string]any{"type": "workspaceWrite", "networkAccess": true, "writableRoots": []string{}},
	})
	if err != nil || turn.ID != "turn-1" {
		t.Fatalf("StartTurn() = %#v, %v", turn, err)
	}
	select {
	case notification := <-client.Notifications():
		if notification.Method != "item/agentMessage/delta" || string(notification.Params) == "" {
			t.Fatalf("notification = %#v", notification)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case request := <-client.Requests():
		if string(request.ID) != "77" || request.Method != "item/commandExecution/requestApproval" {
			t.Fatalf("request = %#v", request)
		}
		if err := client.Respond(request.ID, nil, context.Canceled); err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := client.InterruptTurn(ctx, thread.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	if err := client.CompactThread(ctx, thread.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case notification := <-client.Notifications():
		if notification.Method != "item/completed" {
			t.Fatalf("compaction notification = %#v", notification)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if _, err := client.ResumeThread(ctx, thread.ID); err != nil {
		t.Fatal(err)
	}
	_ = clientSide.Close()
	<-serverDone
}

func TestM22CodexAppServerRejectsEphemeralAndIncompleteThreads(t *testing.T) {
	for _, test := range []struct {
		name   string
		thread CodexThread
	}{
		{name: "ephemeral", thread: CodexThread{ID: "thread", CWD: "/work", ModelProvider: "openai", Status: CodexThreadStatus{Type: "idle"}, Ephemeral: true}},
		{name: "incomplete", thread: CodexThread{ID: "thread", CWD: "/work", Status: CodexThreadStatus{Type: "idle"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := validateCodexThread(test.thread, false); err == nil {
				t.Fatalf("validateCodexThread(%#v) succeeded", test.thread)
			}
		})
	}
}
