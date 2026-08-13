package localapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestContradictionReportClientGeneratesIdempotencyWithoutAuthorityFields(t *testing.T) {
	t.Parallel()
	request := captureCuratorRequest(t, MethodContradictionReport, func(client *Client) error {
		_, err := client.ContradictionReport(context.Background(), ContradictionReportParams{
			Workspace: "personal", LeftRevision: "krev_left", RightRevision: "krev_right", Reason: "conflicting exact values",
		})
		return err
	}, ContradictionMutationResult{Schema: ContradictionMutationSchema, Type: "contradiction_mutation"})
	var params ContradictionReportParams
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Workspace != "personal" || params.LeftRevision != "krev_left" || params.RightRevision != "krev_right" ||
		params.Reason != "conflicting exact values" || strings.TrimSpace(params.IdempotencyKey) == "" {
		t.Fatalf("contradiction report params = %#v", params)
	}
	for _, forbidden := range []string{"actor", "project", "status", "reported_by", "left_revision_id"} {
		if strings.Contains(string(request.Params), `"`+forbidden+`"`) {
			t.Errorf("contradiction report request exposed %q: %s", forbidden, request.Params)
		}
	}
}

func TestContradictionListClientDefaultsToBoundedFiftyAndPreservesExplicitZero(t *testing.T) {
	t.Parallel()
	request := captureCuratorRequest(t, MethodContradictionList, func(client *Client) error {
		_, err := client.ContradictionList(context.Background(), ContradictionListParams{Workspace: "personal", Project: "engine"})
		return err
	}, ContradictionListResult{Schema: ContradictionListSchema, Type: "knowledge_contradiction_list"})
	var params ContradictionListParams
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Project != "engine" || params.Limit == nil || *params.Limit != 50 {
		t.Fatalf("default contradiction list params = %#v", params)
	}

	zero := 0
	zeroRequest := captureCuratorRequest(t, MethodContradictionList, func(client *Client) error {
		_, err := client.ContradictionList(context.Background(), ContradictionListParams{Workspace: "personal", Project: "engine", Limit: &zero})
		return err
	}, ContradictionListResult{Schema: ContradictionListSchema, Type: "knowledge_contradiction_list"})
	if !strings.Contains(string(zeroRequest.Params), `"limit":0`) {
		t.Fatalf("explicit zero list request = %s", zeroRequest.Params)
	}
}

func TestContradictionDecisionClientsGenerateIdempotencyAndNeverExposeActor(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		method string
		call   func(*Client, ContradictionDecisionParams) error
	}{
		{method: MethodContradictionConfirm, call: func(client *Client, params ContradictionDecisionParams) error {
			_, err := client.ContradictionConfirm(context.Background(), params)
			return err
		}},
		{method: MethodContradictionDismiss, call: func(client *Client, params ContradictionDecisionParams) error {
			_, err := client.ContradictionDismiss(context.Background(), params)
			return err
		}},
	} {
		t.Run(test.method, func(t *testing.T) {
			t.Parallel()
			request := captureCuratorRequest(t, test.method, func(client *Client) error {
				return test.call(client, ContradictionDecisionParams{
					Workspace: "personal", Contradiction: "kcon_exact", ExpectedStateRevision: 3, Note: "owner review",
				})
			}, ContradictionMutationResult{Schema: ContradictionMutationSchema, Type: "contradiction_mutation"})
			var params ContradictionDecisionParams
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Fatal(err)
			}
			if params.ExpectedStateRevision != 3 || params.Note != "owner review" || strings.TrimSpace(params.IdempotencyKey) == "" {
				t.Fatalf("%s params = %#v", test.method, params)
			}
			if strings.Contains(string(request.Params), `"actor"`) {
				t.Fatalf("owner governance exposed caller authority: %s", request.Params)
			}
		})
	}
}
