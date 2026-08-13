package execution

import (
	"context"
	"encoding/json"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/mcp"
)

type fixtureContextDeltaToolClient struct {
	testing                *testing.T
	deltas                 []domain.ContextDelta
	index                  int
	ackCalls               int
	lastAckRaw             []byte
	fetchRunOverride       string
	fetchPacketOverride    string
	fetchChainRunOverride  string
	deltaWorkspaceOverride string
	ackRunOverride         string
	ackPacketOverride      string
	ackByOverride          string
	zeroAckEvent           bool
	noPendingState         *domain.ContextDeltaFetchResult
	deniedAcknowledge      *mcp.ToolError
}

func (client *fixtureContextDeltaToolClient) CallTool(_ context.Context, name string, arguments any) (mcp.ToolCallResult, error) {
	client.testing.Helper()
	fields, ok := arguments.(map[string]any)
	if !ok {
		client.testing.Fatalf("arguments type = %T, want map[string]any", arguments)
	}
	for _, forbidden := range []string{"run", "workspace", "project", "task", "agent", "cursor"} {
		if _, exists := fields[forbidden]; exists {
			client.testing.Fatalf("context delta fixture supplied forbidden authority field %q", forbidden)
		}
	}
	switch name {
	case "crewfold_get_context_delta":
		if len(fields) != 0 {
			client.testing.Fatalf("fetch arguments = %#v, want empty", fields)
		}
		if client.index >= len(client.deltas) {
			state := domain.ContextDeltaFetchResult{Status: domain.ContextDeltaNonePending}
			if client.noPendingState != nil {
				state = *client.noPendingState
			}
			encoded, _ := json.Marshal(state)
			return mcp.ToolCallResult{StructuredContent: encoded}, nil
		}
		delta := client.deltas[client.index]
		if client.deltaWorkspaceOverride != "" {
			delta.WorkspaceID = client.deltaWorkspaceOverride
		}
		stateRunID := delta.RunID
		if client.fetchRunOverride != "" {
			stateRunID = client.fetchRunOverride
		}
		statePacketID := delta.ContextPacketID
		if client.fetchPacketOverride != "" {
			statePacketID = client.fetchPacketOverride
		}
		chainRunID := delta.RunID
		if client.fetchChainRunOverride != "" {
			chainRunID = client.fetchChainRunOverride
		}
		encoded, _ := json.Marshal(domain.ContextDeltaFetchResult{
			Status: domain.ContextDeltaPending, RunID: stateRunID, ContextPacketID: statePacketID,
			StateRevision: 1, Delta: &delta, Chain: domain.ContextDeltaChain{
				RunID: chainRunID, ContextPacketID: delta.ContextPacketID, LatestDeltaID: delta.ID,
				LatestSequence: delta.Sequence, PendingDeltaID: delta.ID, PendingSequence: delta.Sequence,
			},
		})
		return mcp.ToolCallResult{StructuredContent: encoded}, nil
	case "crewfold_acknowledge_context_delta":
		client.ackCalls++
		if client.index >= len(client.deltas) && client.deniedAcknowledge != nil {
			encoded, _ := json.Marshal(client.deniedAcknowledge)
			return mcp.ToolCallResult{StructuredContent: encoded, IsError: true}, nil
		}
		delta := client.deltas[client.index]
		if fields["delta_id"] != delta.ID || fields["expected_sequence"] != delta.Sequence {
			client.testing.Fatalf("acknowledgement arguments = %#v, want delta %s sequence %d", fields, delta.ID, delta.Sequence)
		}
		if client.ackCalls%2 == 1 {
			receiptRunID := delta.RunID
			if client.ackRunOverride != "" {
				receiptRunID = client.ackRunOverride
			}
			receiptPacketID := delta.ContextPacketID
			if client.ackPacketOverride != "" {
				receiptPacketID = client.ackPacketOverride
			}
			acknowledgedBy := delta.RunID
			if client.ackByOverride != "" {
				acknowledgedBy = client.ackByOverride
			}
			eventSequence := int64(9)
			if client.zeroAckEvent {
				eventSequence = 0
			}
			receipt := domain.ContextDeltaAcknowledgement{
				ID: "cdack_00000000000000000000000000000001", RunID: receiptRunID,
				ContextPacketID: receiptPacketID, DeltaID: delta.ID, Sequence: delta.Sequence,
				AcknowledgedAt: "2026-08-13T12:00:00Z", AcknowledgedBy: acknowledgedBy, EventSequence: eventSequence,
			}
			client.lastAckRaw, _ = json.Marshal(receipt)
			return mcp.ToolCallResult{StructuredContent: client.lastAckRaw}, nil
		}
		client.index++
		return mcp.ToolCallResult{StructuredContent: client.lastAckRaw}, nil
	default:
		client.testing.Fatalf("unexpected tool %q", name)
		return mcp.ToolCallResult{}, nil
	}
}

func TestFixtureContextDeltaRejectsCrossScopeFetchAndForgedAcknowledgement(t *testing.T) {
	t.Parallel()
	delta := domain.ContextDelta{
		ID: "cdelta_00000000000000000000000000000001", RunID: "run_00000000000000000000000000000001",
		ContextPacketID: "ctx_00000000000000000000000000000001",
		WorkspaceID:     "ws_00000000000000000000000000000001", ProjectID: "prj_00000000000000000000000000000001",
		TaskID: "task_00000000000000000000000000000001", AgentID: "agent_00000000000000000000000000000001", Sequence: 1,
		Changes: []domain.ContextDeltaChange{{Kind: domain.ContextDeltaMessageReceived, Message: &domain.InboxSummaryItem{BodyPreview: "preview"}}},
	}
	briefing := domain.RunBriefing{
		Run: domain.Run{ID: delta.RunID, ContextPacketID: delta.ContextPacketID, WorkspaceID: delta.WorkspaceID,
			ProjectID: delta.ProjectID, TaskID: delta.TaskID, AgentID: delta.AgentID},
		Packet: domain.ContextPacket{ID: delta.ContextPacketID},
	}
	plan := domain.FixtureContextDelta{Expectations: []domain.FixtureContextDeltaExpectation{{RequiredKinds: []string{domain.ContextDeltaMessageReceived}}}}
	for name, configure := range map[string]func(*fixtureContextDeltaToolClient){
		"foreign fetch run": func(client *fixtureContextDeltaToolClient) {
			client.fetchRunOverride = "run_ffffffffffffffffffffffffffffffff"
		},
		"foreign fetch packet": func(client *fixtureContextDeltaToolClient) {
			client.fetchPacketOverride = "ctx_ffffffffffffffffffffffffffffffff"
		},
		"foreign fetch chain": func(client *fixtureContextDeltaToolClient) {
			client.fetchChainRunOverride = "run_ffffffffffffffffffffffffffffffff"
		},
		"foreign delta workspace": func(client *fixtureContextDeltaToolClient) {
			client.deltaWorkspaceOverride = "ws_ffffffffffffffffffffffffffffffff"
		},
		"foreign acknowledgement run": func(client *fixtureContextDeltaToolClient) {
			client.ackRunOverride = "run_ffffffffffffffffffffffffffffffff"
		},
		"foreign acknowledgement packet": func(client *fixtureContextDeltaToolClient) {
			client.ackPacketOverride = "ctx_ffffffffffffffffffffffffffffffff"
		},
		"foreign acknowledgement actor": func(client *fixtureContextDeltaToolClient) {
			client.ackByOverride = "run_ffffffffffffffffffffffffffffffff"
		},
		"missing acknowledgement event": func(client *fixtureContextDeltaToolClient) {
			client.zeroAckEvent = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := &fixtureContextDeltaToolClient{testing: t, deltas: []domain.ContextDelta{delta}}
			configure(client)
			if err := runFixtureContextDelta(context.Background(), client, plan, briefing); err == nil {
				t.Fatal("runFixtureContextDelta() error = nil, want exact-scope rejection")
			}
		})
	}
}

func TestFixtureContextDeltaRequiresStableNonRetryableCrossRunDenial(t *testing.T) {
	t.Parallel()
	briefing := domain.RunBriefing{
		Run:    domain.Run{ID: "run_00000000000000000000000000000001", ContextPacketID: "ctx_00000000000000000000000000000001"},
		Packet: domain.ContextPacket{ID: "ctx_00000000000000000000000000000001"},
	}
	state := domain.ContextDeltaFetchResult{Status: domain.ContextDeltaNonePending, RunID: briefing.Run.ID,
		ContextPacketID: briefing.Packet.ID, Chain: domain.ContextDeltaChain{RunID: briefing.Run.ID, ContextPacketID: briefing.Packet.ID}}
	plan := domain.FixtureContextDelta{ExpectNoPending: true, DeniedDeltaID: "cdelta_ffffffffffffffffffffffffffffffff", DeniedExpectedSequence: 1}
	for name, body := range map[string]mcp.ToolError{
		"wrong code": {Code: "out_of_scope"},
		"retryable":  {Code: "invalid_input", Retryable: true},
	} {
		t.Run(name, func(t *testing.T) {
			client := &fixtureContextDeltaToolClient{testing: t, noPendingState: &state, deniedAcknowledge: &body}
			if err := runFixtureContextDelta(context.Background(), client, plan, briefing); err == nil {
				t.Fatal("runFixtureContextDelta() error = nil, want stable denial rejection")
			}
		})
	}
	valid := mcp.ToolError{Code: "invalid_input"}
	client := &fixtureContextDeltaToolClient{testing: t, noPendingState: &state, deniedAcknowledge: &valid}
	if err := runFixtureContextDelta(context.Background(), client, plan, briefing); err != nil {
		t.Fatalf("runFixtureContextDelta(valid denial) error = %v", err)
	}
}

func (*fixtureContextDeltaToolClient) ReadResource(context.Context, string) ([]mcp.ResourceContents, error) {
	return nil, nil
}

func TestFixtureBriefingRequiresExactScope(t *testing.T) {
	t.Parallel()
	briefing := domain.RunBriefing{
		Run: domain.Run{
			ID: "run_00000000000000000000000000000001", WorkspaceID: "ws_00000000000000000000000000000001",
			ProjectID: "prj_00000000000000000000000000000001", TaskID: "task_00000000000000000000000000000001",
			AgentID: "agent_00000000000000000000000000000001", CheckoutID: "co_00000000000000000000000000000001",
			ContextPacketID: "ctx_00000000000000000000000000000001",
		},
		Task: domain.Task{
			ID: "task_00000000000000000000000000000001", WorkspaceID: "ws_00000000000000000000000000000001",
			ProjectID: "prj_00000000000000000000000000000001",
		},
		Packet: domain.ContextPacket{
			ID: "ctx_00000000000000000000000000000001", WorkspaceID: "ws_00000000000000000000000000000001",
			ProjectID: "prj_00000000000000000000000000000001", TaskID: "task_00000000000000000000000000000001",
			AgentID: "agent_00000000000000000000000000000001", CheckoutID: "co_00000000000000000000000000000001",
			Role:     domain.ContextRole{AgentID: "agent_00000000000000000000000000000001"},
			Task:     domain.ContextTask{TaskID: "task_00000000000000000000000000000001"},
			Checkout: domain.ContextCheckout{CheckoutID: "co_00000000000000000000000000000001", ProjectID: "prj_00000000000000000000000000000001"},
		},
	}
	if !fixtureBriefingHasExactScope(briefing) {
		t.Fatal("exact fixture briefing was rejected")
	}
	for name, mutate := range map[string]func(*domain.RunBriefing){
		"packet workspace": func(value *domain.RunBriefing) { value.Packet.WorkspaceID = "ws_ffffffffffffffffffffffffffffffff" },
		"task project":     func(value *domain.RunBriefing) { value.Task.ProjectID = "prj_ffffffffffffffffffffffffffffffff" },
		"packet role":      func(value *domain.RunBriefing) { value.Packet.Role.AgentID = "agent_ffffffffffffffffffffffffffffffff" },
		"packet checkout": func(value *domain.RunBriefing) {
			value.Packet.Checkout.CheckoutID = "co_ffffffffffffffffffffffffffffffff"
		},
	} {
		t.Run(name, func(t *testing.T) {
			forged := briefing
			mutate(&forged)
			if fixtureBriefingHasExactScope(forged) {
				t.Fatal("forged fixture briefing was accepted")
			}
		})
	}
}

func TestFixtureContextDeltaFetchesTypedChangesAndAcknowledgesExactRun(t *testing.T) {
	t.Parallel()
	threadID := "thread_00000000000000000000000000000001"
	revisionID := "krev_00000000000000000000000000000001"
	withdrawalID := "krev_00000000000000000000000000000002"
	delta := domain.ContextDelta{
		ID: "cdelta_00000000000000000000000000000001", RunID: "run_00000000000000000000000000000001",
		ContextPacketID: "ctx_00000000000000000000000000000001",
		WorkspaceID:     "ws_00000000000000000000000000000001", ProjectID: "prj_00000000000000000000000000000001",
		TaskID: "task_00000000000000000000000000000001", AgentID: "agent_00000000000000000000000000000001", Sequence: 1,
		Changes: []domain.ContextDeltaChange{
			{Kind: domain.ContextDeltaMessageReceived, Message: &domain.InboxSummaryItem{BodyPreview: "bounded preview"}},
			{Kind: domain.ContextDeltaKnowledgeAccepted, Knowledge: &domain.KnowledgeRevision{ID: revisionID}},
			{Kind: domain.ContextDeltaKnowledgeWithdrawn, Withdrawal: &domain.ContextKnowledgeWithdrawal{RevisionID: withdrawalID}},
			{Kind: domain.ContextDeltaParticipantRosterUpdated, ParticipantThread: &domain.ParticipantThread{Thread: domain.MessageThread{ID: threadID}}},
		},
	}
	client := &fixtureContextDeltaToolClient{testing: t, deltas: []domain.ContextDelta{delta}}
	briefing := domain.RunBriefing{
		Run: domain.Run{ID: delta.RunID, ContextPacketID: delta.ContextPacketID, WorkspaceID: delta.WorkspaceID,
			ProjectID: delta.ProjectID, TaskID: delta.TaskID, AgentID: delta.AgentID},
		Packet: domain.ContextPacket{ID: delta.ContextPacketID},
	}
	plan := domain.FixtureContextDelta{
		DuplicateAcknowledge: true,
		Expectations: []domain.FixtureContextDeltaExpectation{{
			RequiredKinds:  []string{domain.ContextDeltaMessageReceived, domain.ContextDeltaKnowledgeAccepted, domain.ContextDeltaKnowledgeWithdrawn, domain.ContextDeltaParticipantRosterUpdated},
			MessagePreview: "bounded preview", ParticipantThreadID: threadID, KnowledgeRevisionIDs: []string{revisionID},
			WithdrawalRevisionIDs: []string{withdrawalID},
		}},
	}
	if err := runFixtureContextDelta(context.Background(), client, plan, briefing); err != nil {
		t.Fatalf("runFixtureContextDelta() error = %v", err)
	}
	if client.ackCalls != 2 || client.index != 1 {
		t.Fatalf("ack calls=%d index=%d, want idempotent replay then one consumed delta", client.ackCalls, client.index)
	}
}

func TestValidateFixtureContextDeltaRejectsAmbiguousOrUnboundedPlans(t *testing.T) {
	t.Parallel()
	for name, plan := range map[string]domain.FixtureContextDelta{
		"ambiguous modes": {ExpectNoPending: true, ExpectToolsDenied: true},
		"delay":           {ExpectNoPending: true, InitialDelayMillis: 30001},
		"duplicate kind": {Expectations: []domain.FixtureContextDeltaExpectation{{
			RequiredKinds: []string{domain.ContextDeltaMessageReceived, domain.ContextDeltaMessageReceived},
		}}},
		"forged cursor": {ExpectNoPending: true, DeniedDeltaID: "cdelta_00000000000000000000000000000001"},
		"duplicate knowledge": {Expectations: []domain.FixtureContextDeltaExpectation{{
			RequiredKinds: []string{domain.ContextDeltaKnowledgeAccepted},
			KnowledgeRevisionIDs: []string{
				"krev_00000000000000000000000000000001",
				"krev_00000000000000000000000000000001",
			},
		}}},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := validateFixtureContextDelta(plan); err == nil {
				t.Fatal("validateFixtureContextDelta() error = nil, want rejection")
			}
		})
	}
}
