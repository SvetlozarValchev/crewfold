package daemon

import (
	"encoding/json"
	"strings"
	"testing"

	"crewfold/internal/localapi"
)

func TestM19RunAttachRejectsRemovedTakeoverWireField(t *testing.T) {
	t.Parallel()
	request := localapi.Request{
		ID: "m19-attach-no-takeover", Protocol: localapi.MaxProtocol, Method: localapi.MethodRunAttach,
		Params: json.RawMessage(`{"workspace":"personal","run":"run_00000000000000000000000000000001","takeover":true}`),
	}
	response := (&server{}).handleRunAttach(request)
	if response.Error == nil || response.Error.Code != "invalid_request" || response.Error.Retryable || !strings.Contains(response.Error.Message, "workspace and run") {
		t.Fatalf("run.attach removed takeover response = %#v, want non-retryable invalid_request before Store/runtime access", response)
	}
}
