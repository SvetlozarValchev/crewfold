package protocol_test

import (
	"encoding/json"
	"strings"
	"testing"

	"crewfold/internal/buildinfo"
	"crewfold/internal/localapi"
	protocolschema "crewfold/protocol"
)

func TestM21WebSchemasAreClosedAndExecutable(t *testing.T) {
	t.Parallel()
	bootstrap, _ := json.Marshal(localapi.WebBootstrapResult{
		Schema: localapi.WebBootstrapSchema, Type: "web_bootstrap",
		URL: "http://127.0.0.1:43121/#bootstrap=" + strings.Repeat("a", 64), ExpiresAt: "2026-08-14T20:00:00Z",
	})
	if err := protocolschema.ValidateJSON("local/v1/web-bootstrap.result.schema.json", bootstrap); err != nil {
		t.Fatalf("bootstrap schema error = %v", err)
	}
	status, _ := json.Marshal(map[string]any{
		"schema": "urn:crewfold:schema:web:workbench-status:v1", "type": "workbench_status", "status": "ok",
		"protocol": 1, "pid": 123, "started_at": "2026-08-14T20:00:00Z", "uptime_ms": 42,
		"codex_tool_network_access": true,
		"server_version":            buildinfo.Current(),
	})
	if err := protocolschema.ValidateJSON("web/v1/status.response.schema.json", status); err != nil {
		t.Fatalf("status schema error = %v", err)
	}

	var unknown map[string]any
	if err := json.Unmarshal(status, &unknown); err != nil {
		t.Fatal(err)
	}
	unknown["future"] = true
	unknownRaw, _ := json.Marshal(unknown)
	if err := protocolschema.ValidateJSON("web/v1/status.response.schema.json", unknownRaw); err == nil {
		t.Fatal("status schema accepted an unknown field")
	}
	errorRaw := []byte(`{"schema":"urn:crewfold:schema:web:error:v1","type":"error","error":{"code":"unauthorized","message":"owner session is missing or expired"}}`)
	if err := protocolschema.ValidateJSON("web/v1/error.response.schema.json", errorRaw); err != nil {
		t.Fatalf("error response schema error = %v", err)
	}
}
