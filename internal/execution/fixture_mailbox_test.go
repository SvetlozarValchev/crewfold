package execution

import (
	"context"
	"encoding/json"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/mcp"
)

type recordingFixtureToolClient struct {
	name      string
	arguments map[string]any
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
