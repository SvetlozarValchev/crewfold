package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/mcp"
)

type recordingFixtureToolClient struct {
	name      string
	arguments map[string]any
}

type wakeSettlementFixtureClient struct {
	inboxCalls int
	terminal   string
}

func (client *wakeSettlementFixtureClient) CallTool(_ context.Context, name string, _ any) (mcp.ToolCallResult, error) {
	switch name {
	case "crewfold_list_inbox":
		client.inboxCalls++
		status := domain.WakePending
		if client.inboxCalls > 1 {
			status = client.terminal
		}
		encoded, _ := json.Marshal([]domain.InboxItem{{
			Message:  domain.Message{ID: "msg_0123456789abcdef", Kind: domain.MessageInform},
			Delivery: domain.MessageDelivery{MessageID: "msg_0123456789abcdef", WakeStatus: status},
		}})
		return mcp.ToolCallResult{StructuredContent: encoded}, nil
	case "crewfold_read_message", "crewfold_acknowledge_message":
		return mcp.ToolCallResult{StructuredContent: json.RawMessage(`{}`)}, nil
	default:
		return mcp.ToolCallResult{}, fmt.Errorf("unexpected tool %s", name)
	}
}

func (*wakeSettlementFixtureClient) ReadResource(context.Context, string) ([]mcp.ResourceContents, error) {
	return nil, nil
}

func (client *recordingFixtureToolClient) CallTool(_ context.Context, name string, arguments any) (mcp.ToolCallResult, error) {
	client.name = name
	client.arguments, _ = arguments.(map[string]any)
	encoded, _ := json.Marshal(domain.MessageMutation{
		Thread:  domain.MessageThread{ID: "thread_0123456789abcdef"},
		Message: domain.Message{ID: "msg_0123456789abcdef"},
	})
	return mcp.ToolCallResult{StructuredContent: encoded}, nil
}

func (*recordingFixtureToolClient) ReadResource(context.Context, string) ([]mcp.ResourceContents, error) {
	return nil, nil
}

func TestFixtureMailboxCanSendIntoOwnerCreatedThread(t *testing.T) {
	t.Parallel()
	client := &recordingFixtureToolClient{}
	plan := domain.FixtureMailbox{Send: &domain.FixtureMailboxMessage{
		RecipientAgent: "plug-agent",
		ThreadID:       "thread_0123456789abcdef",
		Kind:           domain.MessageQuestion,
		Body:           "Which integration contract does plugandrev require?",
	}}
	if err := runFixtureMailbox(context.Background(), client, plan); err != nil {
		t.Fatalf("runFixtureMailbox() error = %v", err)
	}
	if client.name != "crewfold_send_message" {
		t.Fatalf("tool = %q, want crewfold_send_message", client.name)
	}
	if client.arguments["thread_id"] != plan.Send.ThreadID {
		t.Fatalf("thread_id = %#v, want %q", client.arguments["thread_id"], plan.Send.ThreadID)
	}
	if client.arguments["recipient_agent"] != "plug-agent" {
		t.Fatalf("recipient_agent = %#v, want plug-agent", client.arguments["recipient_agent"])
	}
}

func TestFixtureMailboxWaitsForTerminalWakeSettlement(t *testing.T) {
	t.Parallel()
	for _, terminal := range []string{domain.WakeSucceeded, domain.WakeFailed} {
		t.Run(terminal, func(t *testing.T) {
			t.Parallel()
			client := &wakeSettlementFixtureClient{terminal: terminal}
			plan := domain.FixtureMailbox{
				WaitForKind:         domain.MessageInform,
				AcknowledgeReceived: true,
				WaitTimeoutMillis:   1000,
			}
			if err := runFixtureMailbox(context.Background(), client, plan); err != nil {
				t.Fatalf("runFixtureMailbox() error = %v", err)
			}
			if client.inboxCalls != 2 {
				t.Fatalf("inbox calls = %d, want initial read plus one settlement poll", client.inboxCalls)
			}
		})
	}
}

func TestFixtureMailboxWakeSettlementWaitHonorsCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitForFixtureWakeSettlement(ctx, &wakeSettlementFixtureClient{terminal: domain.WakePending}, domain.InboxItem{
		Message:  domain.Message{ID: "msg_0123456789abcdef"},
		Delivery: domain.MessageDelivery{WakeStatus: domain.WakePending},
	}, time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForFixtureWakeSettlement() error = %v, want context canceled", err)
	}
}
