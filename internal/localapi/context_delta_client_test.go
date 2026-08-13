package localapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"crewfold/internal/domain"
)

func TestContextRefreshClientGeneratesKeyWithoutCallerSelectedAuthority(t *testing.T) {
	t.Parallel()
	request := captureCuratorRequest(t, MethodContextRefresh, func(client *Client) error {
		_, err := client.ContextRefresh(context.Background(), ContextRefreshParams{Workspace: "personal", Run: "run_exact"})
		return err
	}, ContextRefreshResult{Schema: ContextRefreshSchema, Type: "context_refresh", ContextRefreshResult: domain.ContextRefreshResult{
		Status: domain.ContextRefreshUpToDate, RunID: "run_exact", ContextPacketID: "ctx_exact",
	}})
	var params ContextRefreshParams
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Workspace != "personal" || params.Run != "run_exact" || strings.TrimSpace(params.IdempotencyKey) == "" {
		t.Fatalf("context refresh params = %#v", params)
	}
	for _, forbidden := range []string{"cursor", "event_sequence", "task", "agent", "context_packet_id", "actor"} {
		if strings.Contains(string(request.Params), `"`+forbidden+`"`) {
			t.Errorf("context refresh exposed caller-selected %q: %s", forbidden, request.Params)
		}
	}
}

func TestContextDeltaInspectionClientsUseOwnerReadOnlyScope(t *testing.T) {
	t.Parallel()
	after := int64(4)
	listRequest := captureCuratorRequest(t, MethodContextDeltaList, func(client *Client) error {
		_, err := client.ContextDeltaList(context.Background(), ContextDeltaListParams{
			Workspace: "personal", Run: "run_exact", AfterSequence: &after, Limit: 7,
		})
		return err
	}, ContextDeltaListResult{Schema: ContextDeltaListSchema, Type: "context_delta_list", ContextDeltaList: domain.ContextDeltaList{
		Deltas: []domain.ContextDelta{},
	}})
	var list ContextDeltaListParams
	if err := json.Unmarshal(listRequest.Params, &list); err != nil || list.AfterSequence == nil || *list.AfterSequence != 4 || list.Limit != 7 {
		t.Fatalf("context delta list params = %#v, error = %v", list, err)
	}
	if strings.Contains(string(listRequest.Params), "idempotency_key") {
		t.Fatalf("read-only context delta list contains an idempotency key: %s", listRequest.Params)
	}

	queryRequest := captureCuratorRequest(t, MethodContextDeltaShow, func(client *Client) error {
		_, err := client.ContextDeltaShow(context.Background(), "personal", "cdelta_exact")
		return err
	}, ContextDeltaShowResult{Schema: ContextDeltaShowSchema, Type: "context_delta"})
	var query ContextDeltaQueryParams
	if err := json.Unmarshal(queryRequest.Params, &query); err != nil || query.Workspace != "personal" || query.Delta != "cdelta_exact" {
		t.Fatalf("context delta query params = %#v, error = %v", query, err)
	}
}

func TestContextDeltaClientRejectsUnknownResultFields(t *testing.T) {
	t.Parallel()
	err := captureCuratorRequestResultError(t, MethodContextRefresh, func(client *Client) error {
		_, err := client.ContextRefresh(context.Background(), ContextRefreshParams{Workspace: "personal", Run: "run_exact"})
		return err
	}, map[string]any{
		"schema": ContextRefreshSchema, "type": "context_refresh", "status": domain.ContextRefreshUpToDate,
		"run_id": "run_exact", "context_packet_id": "ctx_exact", "state_revision": 1,
		"scanned_from_event_sequence": 1, "scanned_through_event_sequence": 1,
		"chain": map[string]any{}, "unexpected": true,
	})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("context refresh unknown result error = %v", err)
	}
}

func TestContextDeltaClientsRejectWrongResultDiscriminators(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		method string
		call   func(*Client) error
		result any
	}{
		{
			name: "refresh schema", method: MethodContextRefresh,
			call: func(client *Client) error {
				_, err := client.ContextRefresh(context.Background(), ContextRefreshParams{Workspace: "personal", Run: "run_exact"})
				return err
			},
			result: ContextRefreshResult{Schema: "urn:wrong", Type: "context_refresh"},
		},
		{
			name: "refresh type", method: MethodContextRefresh,
			call: func(client *Client) error {
				_, err := client.ContextRefresh(context.Background(), ContextRefreshParams{Workspace: "personal", Run: "run_exact"})
				return err
			},
			result: ContextRefreshResult{Schema: ContextRefreshSchema, Type: "wrong"},
		},
		{
			name: "list schema", method: MethodContextDeltaList,
			call: func(client *Client) error {
				after := int64(0)
				_, err := client.ContextDeltaList(context.Background(), ContextDeltaListParams{Workspace: "personal", Run: "run_exact", AfterSequence: &after, Limit: 20})
				return err
			},
			result: ContextDeltaListResult{Schema: "urn:wrong", Type: "context_delta_list"},
		},
		{
			name: "show type", method: MethodContextDeltaShow,
			call: func(client *Client) error {
				_, err := client.ContextDeltaShow(context.Background(), "personal", "cdelta_exact")
				return err
			},
			result: ContextDeltaShowResult{Schema: ContextDeltaShowSchema, Type: "wrong"},
		},
		{
			name: "explain schema", method: MethodContextDeltaExplain,
			call: func(client *Client) error {
				_, err := client.ContextDeltaExplain(context.Background(), "personal", "cdelta_exact")
				return err
			},
			result: ContextDeltaExplainResult{Schema: "urn:wrong", Type: "context_delta_explanation"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := captureCuratorRequestResultError(t, test.method, test.call, test.result)
			if err == nil || !strings.Contains(err.Error(), "unexpected result schema") {
				t.Fatalf("wrong discriminator error = %v", err)
			}
		})
	}
}
