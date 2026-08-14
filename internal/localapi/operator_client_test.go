package localapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"crewfold/internal/buildinfo"
	"crewfold/internal/domain"
)

func TestProtocolMismatchHasTypedErrorsIsSeam(t *testing.T) {
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	go func() {
		var request Request
		if err := json.NewDecoder(server).Decode(&request); err != nil {
			return
		}
		_ = json.NewEncoder(server).Encode(MarshalResult(request.ID, MaxProtocol+1, HelloResult{
			Type: "hello", SelectedProtocol: MaxProtocol + 1, ServerMin: MaxProtocol + 1,
			ServerMax: MaxProtocol + 1, Version: buildinfo.Current(),
		}))
	}()
	_, err := negotiate(client)
	var mismatch *ProtocolMismatchError
	if !errors.Is(err, ErrProtocolMismatch) || !errors.As(err, &mismatch) {
		t.Fatalf("negotiate mismatch = %v, want typed ProtocolMismatchError", err)
	}
}

func TestEventValidationRejectsUnresolvedWorkspaceNameScope(t *testing.T) {
	result := EventsListResult{
		WorkspaceID: "ws_11111111111111111111111111111111",
		HighWater:   1,
		Events: []domain.Event{
			m19CanonicalEvent(1, m19CanonicalEventID(1), "ws_11111111111111111111111111111111"),
		},
		PageResult: PageResult{Total: 1},
	}
	if err := validateForwardEvents(EventsListParams{Workspace: "personal", After: 0}, result); err == nil {
		t.Fatal("event validation accepted an unresolved workspace name")
	}
}

func TestEventValidationBindsCanonicalWorkspaceIDScope(t *testing.T) {
	requested := "ws_11111111111111111111111111111111"
	other := "ws_22222222222222222222222222222222"
	event := m19CanonicalEvent(1, m19CanonicalEventID(1), other)

	forward := EventsListResult{
		WorkspaceID: other, HighWater: 1, Events: []domain.Event{event},
		PageResult: PageResult{Total: 1},
	}
	if err := validateForwardEvents(EventsListParams{Workspace: requested}, forward); err == nil {
		t.Fatal("forward event page accepted a different canonical workspace")
	}

	reverse := EventsTimelineResult{
		WorkspaceID: other, HighWater: 1, Events: []domain.Event{event},
		PageResult: PageResult{Total: 1},
	}
	if err := validateReverseTimeline(EventsTimelineParams{
		Workspace: requested, EntityType: event.Entity.Type, EntityID: event.Entity.ID,
	}, reverse); err == nil {
		t.Fatal("reverse event timeline accepted a different canonical workspace")
	}
}

func TestStrictRoundTripRejectsNestedUnknownAndDuplicateFields(t *testing.T) {
	for _, test := range []struct {
		name     string
		response string
	}{
		{
			name:     "nested unknown",
			response: `{"id":"strict","protocol":1,"result":{"schema":"` + EventsListSchema + `","type":"event_list","workspace_id":"ws_11111111111111111111111111111111","high_water":1,"events":[{"event_id":"evt_00000000000000000000000000000001","sequence":1,"type":"task.updated","schema_version":1,"occurred_at":"2026-08-14T12:00:00Z","recorded_at":"2026-08-14T12:00:00Z","actor":{"actor_id":"local-owner","actor_type":"human","compat_role":"owner"},"workspace_id":"ws_11111111111111111111111111111111","entity":{"type":"task","id":"task_1","revision":1},"correlation_id":"corr_1","data":{}}],"next_cursor":"","has_more":false,"total":1}}` + "\n",
		},
		{
			name:     "duplicate envelope id",
			response: `{"id":"strict","id":"other","protocol":1,"result":{}}` + "\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()
			go func() {
				var request Request
				if err := json.NewDecoder(server).Decode(&request); err == nil {
					_, _ = server.Write([]byte(test.response))
				}
			}()
			var result EventsListResult
			err := roundTripStrict(client, Request{ID: "strict", Protocol: MinProtocol, Method: MethodEventsList}, &result)
			if err == nil || (!strings.Contains(err.Error(), "unknown field") && !strings.Contains(err.Error(), "duplicate JSON field")) {
				t.Fatalf("strict decode error = %v", err)
			}
		})
	}
}

func TestM19PostNegotiationEnvelopeRequiresExactProtocol(t *testing.T) {
	t.Parallel()
	for _, protocol := range []int{0, MaxProtocol + 1} {
		protocol := protocol
		t.Run(fmt.Sprintf("protocol-%d", protocol), func(t *testing.T) {
			err := m19RawEnvelopeError(t, fmt.Sprintf(`{"id":"strict-envelope","protocol":%d,"result":{}}`+"\n", protocol))
			if !errors.Is(err, ErrProtocolMismatch) || !strings.Contains(err.Error(), "does not match negotiated protocol") {
				t.Fatalf("post-negotiation protocol %d error = %v, want typed exact-protocol rejection", protocol, err)
			}
		})
	}
}

func TestM19ResponseEnvelopeRejectsSimultaneousResultAndError(t *testing.T) {
	t.Parallel()
	err := m19RawEnvelopeError(t, `{"id":"strict-envelope","protocol":1,"result":{},"error":{"code":"conflict","message":"must not mask result","retryable":false}}`+"\n")
	if err == nil || !strings.Contains(err.Error(), "exactly one of result or error") {
		t.Fatalf("simultaneous result/error envelope = %v, want fail-closed XOR rejection", err)
	}
}

func m19RawEnvelopeError(t *testing.T, response string) error {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var request Request
		if err := json.NewDecoder(server).Decode(&request); err == nil {
			_, _ = server.Write([]byte(response))
		}
	}()
	var result map[string]any
	err := roundTripStrict(client, Request{ID: "strict-envelope", Protocol: MaxProtocol, Method: MethodStatus}, &result)
	<-done
	return err
}
