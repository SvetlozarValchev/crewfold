package daemon

import (
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"

	"crewfold/internal/localapi"
)

func TestM19DaemonRequestWireRejectsUnknownDuplicateAndSchemaInvalidInput(t *testing.T) {
	config := testConfig(t)
	config.DisableRunWorker = true
	config.DisableCheckWorker = true
	config.DisableCheckWatcher = true
	config.DisableClaimWatcher = true
	config.DisableSupervisor = true
	config.DisableLeaseReconciler = true
	running := startTestServer(t, config)
	workspaceID := "ws_" + strings.Repeat("2", 32)
	runID := "run_" + strings.Repeat("1", 32)
	longWorkspace := strings.Repeat("w", 129)
	tests := []struct {
		name string
		wire string
	}{
		{
			name: "unknown envelope member",
			wire: `{"id":"unknown-envelope","protocol":1,"method":"workspace.list","params":{},"compat":true}`,
		},
		{
			name: "duplicate envelope member",
			wire: `{"id":"duplicate-envelope-a","id":"duplicate-envelope-b","protocol":1,"method":"workspace.list","params":{}}`,
		},
		{
			name: "unknown hello parameter",
			wire: `{"id":"unknown-hello","method":"system.hello","params":{"min_protocol":1,"max_protocol":1,"compat":true}}`,
		},
		{
			name: "duplicate workspace parameter",
			wire: fmt.Sprintf(`{"id":"duplicate-workspace","protocol":1,"method":"run.resume","params":{"workspace":%q,"workspace":%q,"run":%q,"expected_revision":1,"idempotency_key":"exact"}}`, workspaceID, workspaceID, runID),
		},
		{
			name: "duplicate run parameter",
			wire: fmt.Sprintf(`{"id":"duplicate-run","protocol":1,"method":"run.resume","params":{"workspace":%q,"run":%q,"run":%q,"expected_revision":1,"idempotency_key":"exact"}}`, workspaceID, runID, runID),
		},
		{
			name: "duplicate idempotency parameter",
			wire: fmt.Sprintf(`{"id":"duplicate-key","protocol":1,"method":"run.resume","params":{"workspace":%q,"run":%q,"expected_revision":1,"idempotency_key":"exact","idempotency_key":"changed"}}`, workspaceID, runID),
		},
		{
			name: "explicit zero list limit",
			wire: `{"id":"zero-limit","protocol":1,"method":"workspace.list","params":{"limit":0}}`,
		},
		{
			name: "malformed attach run",
			wire: fmt.Sprintf(`{"id":"bad-attach-run","protocol":1,"method":"run.attach","params":{"workspace":%q,"run":"run_bad"}}`, workspaceID),
		},
		{
			name: "malformed resume run",
			wire: fmt.Sprintf(`{"id":"bad-resume-run","protocol":1,"method":"run.resume","params":{"workspace":%q,"run":"run_bad","expected_revision":1,"idempotency_key":"exact"}}`, workspaceID),
		},
		{
			name: "malformed stop run",
			wire: fmt.Sprintf(`{"id":"bad-stop-run","protocol":1,"method":"run.stop","params":{"workspace":%q,"run":"run_bad","expected_revision":1,"grace_period_millis":250,"idempotency_key":"exact"}}`, workspaceID),
		},
		{
			name: "invalid approval status filter",
			wire: fmt.Sprintf(`{"id":"bad-status","protocol":1,"method":"approval.list","params":{"workspace":%q,"status":"compat_pending"}}`, workspaceID),
		},
		{
			name: "invalid approval action filter",
			wire: fmt.Sprintf(`{"id":"bad-filter","protocol":1,"method":"approval.list","params":{"workspace":%q,"action":"saction_bad"}}`, workspaceID),
		},
		{
			name: "workspace name forbidden on operator wire",
			wire: fmt.Sprintf(`{"id":"workspace-name-wire","protocol":1,"method":"run.attach","params":{"workspace":"personal","run":%q}}`, runID),
		},
		{
			name: "project name forbidden on operator wire",
			wire: fmt.Sprintf(`{"id":"project-name-wire","protocol":1,"method":"approval.list","params":{"workspace":%q,"project":"demo"}}`, workspaceID),
		},
		{
			name: "agent name forbidden on operator wire",
			wire: fmt.Sprintf(`{"id":"agent-name-wire","protocol":1,"method":"inbox.list","params":{"workspace":%q,"agent":"inbox-reader","limit":20}}`, workspaceID),
		},
		{
			name: "oversized workspace scope",
			wire: fmt.Sprintf(`{"id":"oversized-scope","protocol":1,"method":"run.attach","params":{"workspace":%q,"run":%q}}`, longWorkspace, runID),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := m19RawDaemonRequest(t, config.SocketPath, test.wire)
			if response.Error == nil || response.Error.Code != "invalid_request" || response.Error.Retryable || len(response.Result) != 0 {
				t.Fatalf("raw invalid request response = %#v, want definitive invalid_request with no result", response)
			}
		})
	}

	if _, err := localapi.NewClient(config.SocketPath).Stop(t.Context()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func m19RawDaemonRequest(t *testing.T, socketPath, wire string) localapi.Response {
	t.Helper()
	connection, err := net.Dial("unix", socketPath)
	if err != nil {
		t.Fatalf("dial daemon socket: %v", err)
	}
	defer connection.Close()
	if _, err := fmt.Fprintln(connection, wire); err != nil {
		t.Fatalf("write raw request: %v", err)
	}
	var response localapi.Response
	if err := json.NewDecoder(connection).Decode(&response); err != nil {
		t.Fatalf("decode raw response: %v", err)
	}
	return response
}
