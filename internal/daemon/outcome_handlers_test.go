package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"crewfold/internal/localapi"
)

func TestOutcomeDispatchCoversEveryOwnerMethod(t *testing.T) {
	t.Parallel()
	methods := []string{
		localapi.MethodOutcomeCommitmentCreate,
		localapi.MethodOutcomeCommitmentShow,
		localapi.MethodOutcomeCommitmentList,
		localapi.MethodOutcomePropose,
		localapi.MethodOutcomeShow,
		localapi.MethodOutcomeList,
		localapi.MethodOutcomeAccept,
		localapi.MethodOutcomeReject,
		localapi.MethodCheckpointCreate,
		localapi.MethodCheckpointShow,
		localapi.MethodCheckpointList,
		localapi.MethodBriefingShow,
		localapi.MethodBriefingExplain,
	}
	if len(methods) != 13 {
		t.Fatalf("owner outcome method count = %d, want 13", len(methods))
	}
	server := &server{}
	for _, method := range methods {
		method := method
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			response, stop := server.handleRequest(localapi.Request{
				ID: "req-outcome", Protocol: 1, Method: method,
				Params: json.RawMessage(`{"unexpected":true}`),
			})
			if stop {
				t.Fatal("handleRequest requested server stop")
			}
			if response.Error == nil || response.Error.Code == "method_not_found" {
				t.Fatalf("dispatch error = %#v, want method-specific strict-params rejection", response.Error)
			}
		})
	}
}

func TestOutcomeParamsRejectCallerAuthorityAndHistoricBriefingCursor(t *testing.T) {
	t.Parallel()
	server := &server{}
	for name, test := range map[string]struct {
		method string
		params string
	}{
		"caller evidence class": {
			method: localapi.MethodOutcomePropose,
			params: `{"workspace":"personal","task":"task_exact","commitment":"outcommit_exact","assessment":{"conclusion":"unknown","delivered_scope":[],"unmet_scope":[],"decision_revision_ids":[],"evidence":[{"source_type":"handoff","source_id":"handoff_exact","class":"independent_review"}],"effects":[],"deviations":[],"risks":[],"unknowns":[],"follow_up_task_ids":[],"owner_attention":[]},"idempotency_key":"idem"}`,
		},
		"missing required assessment array": {
			method: localapi.MethodOutcomePropose,
			params: `{"workspace":"personal","task":"task_exact","commitment":"outcommit_exact","assessment":{"conclusion":"unknown","delivered_scope":[],"unmet_scope":[],"decision_revision_ids":[],"evidence":[],"effects":[],"deviations":[],"risks":[],"unknowns":[],"follow_up_task_ids":[]},"idempotency_key":"idem"}`,
		},
		"duplicate nested assessment field": {
			method: localapi.MethodOutcomePropose,
			params: `{"workspace":"personal","task":"task_exact","commitment":"outcommit_exact","assessment":{"conclusion":"unknown","conclusion":"achieved","delivered_scope":[],"unmet_scope":[],"decision_revision_ids":[],"evidence":[],"effects":[],"deviations":[],"risks":[],"unknowns":[],"follow_up_task_ids":[],"owner_attention":[]},"idempotency_key":"idem"}`,
		},
		"historic cursor": {
			method: localapi.MethodBriefingShow,
			params: `{"workspace":"personal","scope_type":"workspace","scope_identifier":"personal","at_event_sequence":42}`,
		},
	} {
		name, test := name, test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			response, stop := server.handleRequest(localapi.Request{ID: "req-closed", Protocol: 1, Method: test.method, Params: json.RawMessage(test.params)})
			if stop {
				t.Fatal("handleRequest requested server stop")
			}
			if response.Error == nil || response.Error.Code != "invalid_request" || !strings.Contains(response.Error.Message, "requires") {
				t.Fatalf("response = %#v, want strict invalid_request", response)
			}
		})
	}
}

func TestOutcomeDispatchHasNoShortAssessmentMethodAliases(t *testing.T) {
	t.Parallel()
	server := &server{}
	for _, method := range []string{"outcome.propose", "outcome.show", "outcome.list", "outcome.accept", "outcome.reject"} {
		response, stop := server.handleRequest(localapi.Request{ID: "req-no-alias", Protocol: 1, Method: method, Params: json.RawMessage(`{}`)})
		if stop {
			t.Fatal("handleRequest requested server stop")
		}
		if response.Error == nil || response.Error.Code != "method_not_found" {
			t.Errorf("%s response = %#v, want method_not_found", method, response)
		}
	}
}
