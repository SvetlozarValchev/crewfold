package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"crewfold/internal/localapi"
)

func TestOperatorListHandlersRejectOutOfContractClosedFilters(t *testing.T) {
	server := &server{}
	tests := []struct {
		name   string
		method string
		params any
	}{
		{name: "approval status", method: localapi.MethodApprovalList, params: localapi.ApprovalListParams{Workspace: "personal", Status: "future"}},
		{name: "approval action", method: localapi.MethodApprovalList, params: localapi.ApprovalListParams{Workspace: "personal", Action: "saction_bad"}},
		{name: "check status", method: localapi.MethodCheckList, params: localapi.CheckListParams{Workspace: "personal", Status: "future"}},
		{name: "check outcome", method: localapi.MethodCheckList, params: localapi.CheckListParams{Workspace: "personal", Outcome: "future"}},
		{name: "check task", method: localapi.MethodCheckList, params: localapi.CheckListParams{Workspace: "personal", Task: "task_bad"}},
		{name: "check requirement", method: localapi.MethodCheckList, params: localapi.CheckListParams{Workspace: "personal", Requirement: "checkreq_bad"}},
		{name: "check definition", method: localapi.MethodCheckList, params: localapi.CheckListParams{Workspace: "personal", Definition: "checkdef_bad"}},
		{name: "run task", method: localapi.MethodRunList, params: localapi.RunListParams{Workspace: "personal", Task: "task_bad"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			params, err := json.Marshal(test.params)
			if err != nil {
				t.Fatal(err)
			}
			request := localapi.Request{ID: "validation", Protocol: localapi.MaxProtocol, Method: test.method, Params: params}
			var response localapi.Response
			switch test.method {
			case localapi.MethodApprovalList:
				response = server.handleApprovalList(request)
			case localapi.MethodCheckList:
				response = server.handleCheckList(request)
			case localapi.MethodRunList:
				response = server.handleRunList(request)
			}
			if response.Error == nil || response.Error.Code != "invalid_request" {
				t.Fatalf("response = %#v, want invalid_request", response)
			}
		})
	}

	validHex := strings.Repeat("a", 32)
	if !validApprovalListStatus("pending") || !validCheckListStatus("running") || !validCheckListOutcome("timed_out") ||
		!validCanonicalEntityID("saction_"+validHex, "saction_") {
		t.Fatal("current published filters were rejected")
	}
}
