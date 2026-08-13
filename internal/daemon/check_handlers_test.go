package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"crewfold/internal/localapi"
)

func TestCheckDispatchCoversEveryOwnerMethod(t *testing.T) {
	t.Parallel()
	methods := []string{
		localapi.MethodCheckDefinitionCreate, localapi.MethodCheckDefinitionRetire, localapi.MethodCheckDefinitionShow, localapi.MethodCheckDefinitionList,
		localapi.MethodCheckRequirementCreate, localapi.MethodCheckRequirementRetire, localapi.MethodCheckRequirementList,
		localapi.MethodCheckGrantCreate, localapi.MethodCheckGrantRevoke, localapi.MethodCheckGrantShow, localapi.MethodCheckGrantList,
		localapi.MethodCheckRouteCreate, localapi.MethodCheckRouteRetire, localapi.MethodCheckRouteList,
		localapi.MethodCheckPolicyShow, localapi.MethodCheckPolicyConfigure,
		localapi.MethodCheckRun, localapi.MethodCheckList, localapi.MethodCheckInspect, localapi.MethodCheckLogs, localapi.MethodCheckWatch,
		localapi.MethodCheckRepairList, localapi.MethodCheckRepairInspect, localapi.MethodCheckRepairAccept, localapi.MethodCheckRepairReject,
	}
	if len(methods) != 25 {
		t.Fatalf("owner check method count = %d, want 25", len(methods))
	}
	server := &server{}
	for _, method := range methods {
		method := method
		t.Run(method, func(t *testing.T) {
			response, stop := server.handleRequest(localapi.Request{ID: "req-check", Protocol: 1, Method: method, Params: json.RawMessage(`{"unexpected":true}`)})
			if stop {
				t.Fatal("handleRequest requested server stop")
			}
			if response.Error == nil || response.Error.Code == "method_not_found" {
				t.Fatalf("dispatch error = %#v, want method-specific strict-params rejection", response.Error)
			}
		})
	}
}

func TestCheckRunRejectsCheckoutRevisionWithoutCheckoutBeforeStore(t *testing.T) {
	t.Parallel()
	server := &server{}
	params := localapi.CheckRunParams{Workspace: "personal", Task: "task_exact", Definition: "unit", ExpectedCheckoutRevision: 3, IdempotencyKey: "idem-exact"}
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	response := server.handleCheckRun(localapi.Request{ID: "req-check", Protocol: 1, Params: data})
	if response.Error == nil || response.Error.Code != "invalid_request" || !strings.Contains(response.Error.Message, "checkout revision requires checkout") {
		t.Fatalf("response = %#v", response)
	}
}

func TestCheckParamsRejectDuplicateFieldsAtEveryDepth(t *testing.T) {
	t.Parallel()
	for _, data := range []string{
		`{"workspace":"personal","workspace":"other"}`,
		`{"workspace":"personal","definitions":[{"definition":"unit","definition":"lint","content_revision":1}]}`,
	} {
		var params localapi.CheckGrantCreateParams
		if err := decodeCheckParams(json.RawMessage(data), &params); err == nil || !strings.Contains(err.Error(), "duplicate JSON field") {
			t.Errorf("decodeCheckParams(%s) error = %v", data, err)
		}
	}
}

func TestRunStartRejectsCheckWatchGrantWithoutRevisionAndWithContext(t *testing.T) {
	t.Parallel()
	server := &server{}
	for _, params := range []localapi.RunStartParams{
		{CheckWatchGrant: "checkgrant_exact"},
		{ExpectedCheckWatchGrantRevision: 2},
		{Context: "ctx_exact", CheckWatchGrant: "checkgrant_exact", ExpectedCheckWatchGrantRevision: 2},
	} {
		data, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		response := server.handleRunStart(localapi.Request{ID: "req-check", Protocol: 1, Params: data})
		if response.Error == nil || response.Error.Code != "invalid_request" {
			t.Fatalf("params = %#v, response = %#v", params, response)
		}
	}
}

func TestCheckPolicyConfigureRejectsIncoherentProfileAndBoundsBeforeStore(t *testing.T) {
	t.Parallel()
	server := &server{}
	for name, params := range map[string]localapi.CheckPolicyConfigureParams{
		"enabled without profile": {
			Workspace: "personal", Project: "demo", RepairProposalsEnabled: true,
			MaxOpenRepairs: 4, ExpectedRevision: 1, IdempotencyKey: "idem-enabled",
		},
		"disabled with profile": {
			Workspace: "personal", Project: "demo", RepairProfile: "lprof_0123456789abcdef0123456789abcdef",
			RepairProfileRevision: 1, MaxOpenRepairs: 4, ExpectedRevision: 1, IdempotencyKey: "idem-disabled",
		},
		"zero limit": {
			Workspace: "personal", Project: "demo", MaxOpenRepairs: 0,
			ExpectedRevision: 1, IdempotencyKey: "idem-zero",
		},
		"excess limit": {
			Workspace: "personal", Project: "demo", MaxOpenRepairs: 33,
			ExpectedRevision: 1, IdempotencyKey: "idem-excess",
		},
	} {
		name, params := name, params
		t.Run(name, func(t *testing.T) {
			data, err := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			response := server.handleCheckPolicyConfigure(localapi.Request{ID: "req-policy", Protocol: 1, Params: data})
			if response.Error == nil || response.Error.Code != "invalid_request" {
				t.Fatalf("response = %#v, want invalid_request", response)
			}
		})
	}
}
