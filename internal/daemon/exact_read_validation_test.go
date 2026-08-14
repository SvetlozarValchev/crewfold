package daemon

import (
	"encoding/json"
	"testing"

	"crewfold/internal/localapi"
)

func TestExactReadHandlersRejectMissingOrMalformedSelectorsBeforeStore(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		method string
		params string
	}{
		{name: "agent omitted", method: localapi.MethodAgentShow, params: `{"workspace":"personal"}`},
		{name: "agent blank", method: localapi.MethodAgentShow, params: `{"workspace":"personal","agent":" "}`},
		{name: "objective omitted", method: localapi.MethodObjectiveShow, params: `{"workspace":"personal"}`},
		{name: "objective malformed", method: localapi.MethodObjectiveShow, params: `{"workspace":"personal","objective":"obj_bad"}`},
		{name: "task omitted", method: localapi.MethodTaskShow, params: `{"workspace":"personal"}`},
		{name: "task malformed", method: localapi.MethodTaskShow, params: `{"workspace":"personal","task":"task_bad"}`},
		{name: "run omitted", method: localapi.MethodRunShow, params: `{"workspace":"personal"}`},
		{name: "run malformed", method: localapi.MethodRunShow, params: `{"workspace":"personal","run":"run_bad"}`},
		{name: "approval omitted", method: localapi.MethodApprovalInspect, params: `{"workspace":"personal"}`},
		{name: "approval malformed", method: localapi.MethodApprovalInspect, params: `{"workspace":"personal","approval":"appr_bad"}`},
		{name: "check omitted", method: localapi.MethodCheckInspect, params: `{"workspace":"personal"}`},
		{name: "check malformed", method: localapi.MethodCheckInspect, params: `{"workspace":"personal","check_run":"checkrun_bad"}`},
		{name: "overlap omitted", method: localapi.MethodOverlapInspect, params: `{"workspace":"personal"}`},
		{name: "overlap malformed", method: localapi.MethodOverlapInspect, params: `{"workspace":"personal","overlap":"overlap_bad"}`},
	}
	server := &server{}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := localapi.Request{ID: "exact-read", Protocol: localapi.MaxProtocol, Method: test.method, Params: json.RawMessage(test.params)}
			var response localapi.Response
			switch test.method {
			case localapi.MethodAgentShow:
				response = server.handleAgentShow(request)
			case localapi.MethodObjectiveShow:
				response = server.handleObjectiveShow(request)
			case localapi.MethodTaskShow:
				response = server.handleTaskShow(request)
			case localapi.MethodRunShow:
				response = server.handleRunShow(request)
			case localapi.MethodApprovalInspect:
				response = server.handleApprovalInspect(request)
			case localapi.MethodCheckInspect:
				response = server.handleCheckInspect(request)
			case localapi.MethodOverlapInspect:
				response = server.handleOverlapInspect(request)
			}
			if response.Error == nil || response.Error.Code != "invalid_request" || response.Error.Retryable {
				t.Fatalf("response = %#v, want non-retryable invalid_request before Store access", response)
			}
		})
	}
}

func TestOverlapHandlersRejectEachOthersParametersBeforeStore(t *testing.T) {
	t.Parallel()
	server := &server{}
	tests := []struct {
		name    string
		handler func(localapi.Request) localapi.Response
		params  string
	}{
		{name: "inspect rejects scan filter", handler: server.handleOverlapInspect, params: `{"workspace":"personal","project":"world-engine"}`},
		{name: "scan rejects inspect selector", handler: server.handleOverlapScan, params: `{"workspace":"personal","overlap":"overlap_00000000000000000000000000000001"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := test.handler(localapi.Request{ID: "overlap-contract", Protocol: localapi.MaxProtocol, Params: json.RawMessage(test.params)})
			if response.Error == nil || response.Error.Code != "invalid_request" || response.Error.Retryable {
				t.Fatalf("response = %#v, want non-retryable invalid_request before Store access", response)
			}
		})
	}
}
