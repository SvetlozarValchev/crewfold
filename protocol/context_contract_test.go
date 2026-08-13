package protocol_test

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestContextSchemaConstantsMatchPublishedDocuments(t *testing.T) {
	t.Parallel()
	for path, expectedID := range map[string]string{
		"schemas/domain/v1/context-packet.schema.json":              domain.ContextPacketSchemaV1,
		"schemas/domain/v1/context-packet-v2.schema.json":           domain.ContextPacketSchemaV2,
		"schemas/domain/v1/context-packet-v3.schema.json":           domain.ContextPacketSchemaV3,
		"schemas/domain/v1/context-packet-v4.schema.json":           domain.ContextPacketSchema,
		"schemas/domain/v1/context-delta.schema.json":               domain.ContextDeltaSchema,
		"schemas/domain/v1/live-context-policy.schema.json":         domain.ContextLivePolicySchema,
		"schemas/local/v1/context-build-v4.result.schema.json":      localapi.ContextBuildSchema,
		"schemas/local/v1/context-show-v4.result.schema.json":       localapi.ContextShowSchema,
		"schemas/local/v1/context-explain-v3.result.schema.json":    localapi.ContextExplainSchema,
		"schemas/local/v1/context-refresh.result.schema.json":       localapi.ContextRefreshSchema,
		"schemas/local/v1/context-delta-list.result.schema.json":    localapi.ContextDeltaListSchema,
		"schemas/local/v1/context-delta-show.result.schema.json":    localapi.ContextDeltaShowSchema,
		"schemas/local/v1/context-delta-explain.result.schema.json": localapi.ContextDeltaExplainSchema,
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("os.ReadFile(%q) error = %v", path, err)
		}
		var header struct {
			ID string `json:"$id"`
		}
		if err := json.Unmarshal(data, &header); err != nil {
			t.Fatalf("json.Unmarshal(%q) error = %v", path, err)
		}
		if header.ID != expectedID {
			t.Errorf("schema %q ID = %q, want %q", path, header.ID, expectedID)
		}
	}
}

func TestContextV4FreezesLiveToolsAndBounds(t *testing.T) {
	t.Parallel()
	packet := readContextSchema(t, "schemas/domain/v1/context-packet-v4.schema.json")
	properties := packet["properties"].(map[string]any)
	threads := properties["participant_threads"].(map[string]any)
	dependents := properties["dependents"].(map[string]any)
	if threads["maxItems"] != float64(8) || dependents["maxItems"] != float64(32) {
		t.Fatalf("v4 packet bounds: participant_threads=%v dependents=%v", threads["maxItems"], dependents["maxItems"])
	}
	definitions := packet["$defs"].(map[string]any)
	task := definitions["task"].(map[string]any)
	if !containsContractString(contractStringSlice(task["required"]), "assignment_id") {
		t.Fatal("v4 task does not require immutable assignment identity")
	}
	livePolicy := readContextSchema(t, "schemas/domain/v1/live-context-policy.schema.json")
	live := livePolicy["properties"].(map[string]any)
	for name, value := range map[string]float64{
		"max_pending": 1, "max_relevant_events": 1000,
		"per_delta_limit_bytes": 16384, "cumulative_delta_limit_bytes": 65536,
	} {
		if live[name].(map[string]any)["const"] != value {
			t.Errorf("live context %s = %v, want %v", name, live[name], value)
		}
	}
	budget := definitions["budget"].(map[string]any)["properties"].(map[string]any)
	for name, limit := range map[string]float64{"total": 32768, "knowledge": 12288, "collaboration": 8192} {
		parts := budget[name].(map[string]any)["allOf"].([]any)
		properties := parts[1].(map[string]any)["properties"].(map[string]any)
		if properties["limit_bytes"].(map[string]any)["const"] != limit {
			t.Errorf("v4 %s budget does not freeze limit %.0f", name, limit)
		}
	}
	policy := definitions["policy"].(map[string]any)["properties"].(map[string]any)
	allowed := contractStringSlice(policy["allowed_tools"].(map[string]any)["const"])
	wantAllowed := []string{
		"crewfold_acknowledge_context_delta", "crewfold_acknowledge_message", "crewfold_get_briefing",
		"crewfold_get_context_delta", "crewfold_get_status", "crewfold_list_inbox", "crewfold_propose_knowledge",
		"crewfold_propose_completion", "crewfold_publish_artifact", "crewfold_read_message", "crewfold_report_blocked",
		"crewfold_report_contradiction", "crewfold_report_progress", "crewfold_send_message",
	}
	if !reflect.DeepEqual(allowed, wantAllowed) {
		t.Fatalf("v4 policy allowed_tools = %v, want exact immutable set %v", allowed, wantAllowed)
	}
}

func TestContextDeltaPublicContractsAreStrictAndRunScoped(t *testing.T) {
	t.Parallel()
	for _, path := range []string{
		"schemas/domain/v1/context-delta.schema.json",
		"schemas/domain/v1/context-delta-chain.schema.json",
		"schemas/domain/v1/context-refresh-result.schema.json",
		"schemas/domain/v1/context-delta-fetch-result.schema.json",
		"schemas/domain/v1/context-delta-acknowledgement.schema.json",
		"schemas/domain/v1/context-delta-list.schema.json",
		"schemas/mcp/v1/get-context-delta.output.schema.json",
		"schemas/mcp/v1/acknowledge-context-delta.output.schema.json",
		"schemas/mcp/v1/get-status.output.schema.json",
		"schemas/local/v1/context-refresh.params.schema.json",
		"schemas/local/v1/context-delta-list.params.schema.json",
		"schemas/local/v1/context-delta-query.params.schema.json",
		"schemas/local/v1/context-refresh.result.schema.json",
		"schemas/local/v1/context-delta-list.result.schema.json",
		"schemas/mcp/v1/get-context-delta.input.schema.json",
		"schemas/mcp/v1/acknowledge-context-delta.input.schema.json",
	} {
		document := readContextSchema(t, path)
		if additional, exists := document["additionalProperties"]; exists && additional != false {
			t.Errorf("%s permits additional properties", path)
		}
	}

	fetch := readContextSchema(t, "schemas/mcp/v1/get-context-delta.input.schema.json")
	if len(fetch["properties"].(map[string]any)) != 0 {
		t.Fatalf("run delta fetch accepts caller-selected scope: %v", fetch["properties"])
	}
	ack := readContextSchema(t, "schemas/mcp/v1/acknowledge-context-delta.input.schema.json")
	required := contractStringSlice(ack["required"])
	if len(required) != 3 || !containsContractString(required, "delta_id") || !containsContractString(required, "expected_sequence") || !containsContractString(required, "idempotency_key") {
		t.Fatalf("acknowledgement required fields = %v", required)
	}
	properties := ack["properties"].(map[string]any)
	for _, forbidden := range []string{"workspace", "run", "task", "agent", "context_packet_id"} {
		if _, exists := properties[forbidden]; exists {
			t.Errorf("acknowledgement exposes trusted field %q", forbidden)
		}
	}
}

func TestContextDeltaWireEnumsMatchFrozenIncrementalAuthority(t *testing.T) {
	t.Parallel()
	delta := readContextSchema(t, "schemas/domain/v1/context-delta.schema.json")
	definitions := delta["$defs"].(map[string]any)
	changeDefinition := definitions["change"].(map[string]any)
	change := changeDefinition["properties"].(map[string]any)
	kinds := contractStringSlice(change["kind"].(map[string]any)["enum"])
	wantKinds := []string{
		"message_received", "knowledge_accepted", "knowledge_withdrawn", "contradiction_opened",
		"contradiction_closed", "dependent_added", "dependent_updated", "participant_roster_updated",
	}
	if !reflect.DeepEqual(kinds, wantKinds) {
		t.Fatalf("delta kinds = %v, want exact closed vocabulary %v", kinds, wantKinds)
	}
	if containsContractString(kinds, "dependency_updated") {
		t.Fatalf("delta kind permits direct upstream contract drift: %v", kinds)
	}
	for _, wanted := range []string{"dependent_added", "dependent_updated", "participant_roster_updated"} {
		if !containsContractString(kinds, wanted) {
			t.Errorf("delta kind omits %q: %v", wanted, kinds)
		}
	}
	withdrawal := definitions["withdrawal"].(map[string]any)["properties"].(map[string]any)
	reasons := contractStringSlice(withdrawal["reason"].(map[string]any)["enum"])
	for _, wanted := range []string{"stale", "superseded", "freshness_expired", "disputed"} {
		if !containsContractString(reasons, wanted) {
			t.Errorf("withdrawal reason omits %q: %v", wanted, reasons)
		}
	}
	if maximum := withdrawal["open_contradiction_ids"].(map[string]any)["maxItems"]; maximum != float64(16) {
		t.Errorf("open contradiction ID cap = %v, want 16", maximum)
	}
	withdrawalDefinition := definitions["withdrawal"].(map[string]any)
	withdrawalConstraints := withdrawalDefinition["allOf"].([]any)
	if len(withdrawalConstraints) != 2 {
		t.Fatal("withdrawal schema does not bind replacement and contradiction evidence to exact lifecycle reasons")
	}
	disputedEvidence := withdrawalConstraints[1].(map[string]any)["then"].(map[string]any)["properties"].(map[string]any)
	if disputedEvidence["open_contradiction_ids"].(map[string]any)["minItems"] != float64(1) ||
		disputedEvidence["open_contradiction_count"].(map[string]any)["minimum"] != float64(1) {
		t.Fatal("disputed withdrawal does not require bounded positive contradiction evidence")
	}

	branches := make(map[string]map[string]any)
	for _, value := range changeDefinition["oneOf"].([]any) {
		branch := value.(map[string]any)
		properties := branch["properties"].(map[string]any)
		kind := properties["kind"].(map[string]any)
		if constant, ok := kind["const"].(string); ok {
			branches[constant] = branch
		}
		for _, enum := range contractStringSlice(kind["enum"]) {
			branches[enum] = branch
		}
	}
	wantBindings := map[string]struct{ entityType, pattern string }{
		"message_received":           {"message", `^msg_[0-9a-f]{32}$`},
		"knowledge_accepted":         {"knowledge_revision", `^krev_[0-9a-f]{32}$`},
		"knowledge_withdrawn":        {"knowledge_revision", `^krev_[0-9a-f]{32}$`},
		"contradiction_opened":       {"knowledge_contradiction", `^kcon_[0-9a-f]{32}$`},
		"contradiction_closed":       {"knowledge_contradiction", `^kcon_[0-9a-f]{32}$`},
		"dependent_added":            {"task", `^task_[0-9a-f]{32}$`},
		"dependent_updated":          {"task", `^task_[0-9a-f]{32}$`},
		"participant_roster_updated": {"thread", `^thread_[0-9a-f]{32}$`},
	}
	for kind, wanted := range wantBindings {
		branch, exists := branches[kind]
		if !exists {
			t.Errorf("delta union has no branch for %q", kind)
			continue
		}
		properties := branch["properties"].(map[string]any)
		if got := properties["entity_type"].(map[string]any)["const"]; got != wanted.entityType {
			t.Errorf("delta %s entity_type = %v, want %q", kind, got, wanted.entityType)
		}
		if got := properties["entity_id"].(map[string]any)["pattern"]; got != wanted.pattern {
			t.Errorf("delta %s entity_id pattern = %v, want %q", kind, got, wanted.pattern)
		}
		if kind != "knowledge_withdrawn" && !containsContractString(contractStringSlice(properties["cause"].(map[string]any)["required"]), "event_sequence") {
			t.Errorf("delta %s does not require its durable event cause", kind)
		}
	}
	withdrawalBranches := branches["knowledge_withdrawn"]["oneOf"].([]any)
	wantWithdrawalReasons := []string{"stale", "superseded", "freshness_expired", "disputed"}
	if len(withdrawalBranches) != len(wantWithdrawalReasons) {
		t.Fatalf("withdrawal cause union has %d branches, want %d", len(withdrawalBranches), len(wantWithdrawalReasons))
	}
	for index, rawBranch := range withdrawalBranches {
		properties := rawBranch.(map[string]any)["properties"].(map[string]any)
		cause := properties["cause"].(map[string]any)
		payload := properties["withdrawal"].(map[string]any)
		causeReason := cause["properties"].(map[string]any)["reason"].(map[string]any)["const"]
		payloadReason := payload["properties"].(map[string]any)["reason"].(map[string]any)["const"]
		if causeReason != wantWithdrawalReasons[index] || payloadReason != wantWithdrawalReasons[index] {
			t.Errorf("withdrawal branch %d binds cause/payload reasons %v/%v, want %q", index, causeReason, payloadReason, wantWithdrawalReasons[index])
		}
		requiresEvent := containsContractString(contractStringSlice(cause["required"]), "event_sequence")
		_, forbidsEvent := cause["not"]
		if index == 2 {
			if requiresEvent || !forbidsEvent {
				t.Error("time-driven freshness withdrawal does not forbid an invented event cause")
			}
		} else if !requiresEvent || forbidsEvent {
			t.Errorf("event-driven withdrawal %q does not require its positive event cause", wantWithdrawalReasons[index])
		}
	}
	accepted := branches["knowledge_accepted"]["properties"].(map[string]any)["knowledge"].(map[string]any)["allOf"].([]any)
	acceptedLifecycle := accepted[1].(map[string]any)["properties"].(map[string]any)
	for field, value := range map[string]string{"type": "decision", "review_status": "accepted", "currency_status": "current"} {
		if got := acceptedLifecycle[field].(map[string]any)["const"]; got != value {
			t.Errorf("accepted knowledge %s = %v, want %q", field, got, value)
		}
	}
	opened := branches["contradiction_opened"]["properties"].(map[string]any)["contradiction"].(map[string]any)["allOf"].([]any)
	openedSnapshot := opened[1].(map[string]any)["properties"].(map[string]any)["contradiction"].(map[string]any)["properties"].(map[string]any)
	if got := openedSnapshot["status"].(map[string]any)["const"]; got != "open" {
		t.Errorf("opened contradiction status = %v, want open", got)
	}
	closed := branches["contradiction_closed"]["properties"].(map[string]any)["contradiction"].(map[string]any)["allOf"].([]any)
	closedSnapshot := closed[1].(map[string]any)["properties"].(map[string]any)["contradiction"].(map[string]any)["properties"].(map[string]any)
	if got := contractStringSlice(closedSnapshot["status"].(map[string]any)["enum"]); !reflect.DeepEqual(got, []string{"resolved", "dismissed"}) {
		t.Errorf("closed contradiction statuses = %v, want resolved/dismissed", got)
	}
}

func TestContextDeltaResultSchemasExposeExplanationAndEventFacts(t *testing.T) {
	t.Parallel()
	explanation := readContextSchema(t, "schemas/domain/v1/context-delta-explanation.schema.json")
	required := contractStringSlice(explanation["required"])
	for _, field := range []string{"included", "excluded", "budget"} {
		if !containsContractString(required, field) {
			t.Errorf("context delta explanation does not require %q", field)
		}
	}
	explanationProperties := explanation["properties"].(map[string]any)
	changeKinds := explanationProperties["change_kinds"].(map[string]any)
	if changeKinds["maxItems"] != float64(1000) {
		t.Errorf("context delta explanation change kind cap = %v, want 1000", changeKinds["maxItems"])
	}
	wantKinds := []string{
		"message_received", "knowledge_accepted", "knowledge_withdrawn", "contradiction_opened",
		"contradiction_closed", "dependent_added", "dependent_updated", "participant_roster_updated",
	}
	if got := contractStringSlice(changeKinds["items"].(map[string]any)["enum"]); !reflect.DeepEqual(got, wantKinds) {
		t.Errorf("context delta explanation kinds = %v, want %v", got, wantKinds)
	}
	if maximum := explanationProperties["excluded"].(map[string]any)["maxItems"]; maximum != float64(0) {
		t.Errorf("context delta explanation excluded cap = %v, want 0", maximum)
	}
	refresh := readContextSchema(t, "schemas/domain/v1/context-refresh-result.schema.json")
	refreshRequired := contractStringSlice(refresh["required"])
	if !containsContractString(refreshRequired, "event_sequence") {
		t.Fatal("context refresh result does not require event_sequence")
	}
	delta := readContextSchema(t, "schemas/domain/v1/context-delta.schema.json")
	if maximum := delta["properties"].(map[string]any)["excluded"].(map[string]any)["maxItems"]; maximum != float64(0) {
		t.Errorf("context delta excluded cap = %v, want 0", maximum)
	}
	through := delta["properties"].(map[string]any)["through_event_sequence"].(map[string]any)
	if through["minimum"] != float64(0) {
		t.Fatalf("delta through_event_sequence minimum = %v, want 0", through["minimum"])
	}
	deltaBudget := delta["$defs"].(map[string]any)["budget"].(map[string]any)["properties"].(map[string]any)
	for name, limit := range map[string]float64{"total": 16384, "chain": 65536} {
		parts := deltaBudget[name].(map[string]any)["allOf"].([]any)
		properties := parts[1].(map[string]any)["properties"].(map[string]any)
		if properties["limit_bytes"].(map[string]any)["const"] != limit {
			t.Errorf("context delta %s budget does not freeze limit %.0f", name, limit)
		}
	}
	chain := readContextSchema(t, "schemas/domain/v1/context-delta-chain.schema.json")
	if len(chain["allOf"].([]any)) != 4 {
		t.Fatal("context delta chain does not correlate optional IDs with their exact sequences")
	}
	status := readContextSchema(t, "schemas/mcp/v1/get-status.output.schema.json")
	statusProperties := status["properties"].(map[string]any)
	wantRunStatuses := []string{
		domain.RunRequested, domain.RunStarting, domain.RunActive, domain.RunBlocked, domain.RunStopping,
		domain.RunStopped, domain.RunLost, domain.RunReview, domain.RunCompleted, domain.RunStartFailed, domain.RunFailed,
	}
	if got := contractStringSlice(statusProperties["run_status"].(map[string]any)["enum"]); !reflect.DeepEqual(got, wantRunStatuses) {
		t.Errorf("MCP status run states = %v, want exact domain states %v", got, wantRunStatuses)
	}
	wantTaskStatuses := []string{
		domain.TaskReady, domain.TaskAssigned, domain.TaskActive, domain.TaskBlocked, domain.TaskReview,
		domain.TaskChangesRequested, domain.TaskCompleted, domain.TaskFailed, domain.TaskCancelled,
	}
	if got := contractStringSlice(statusProperties["task_status"].(map[string]any)["enum"]); !reflect.DeepEqual(got, wantTaskStatuses) {
		t.Errorf("MCP status task states = %v, want exact domain states %v", got, wantTaskStatuses)
	}
	contextDefinition := status["$defs"].(map[string]any)["context"].(map[string]any)
	if len(contextDefinition["allOf"].([]any)) != 3 {
		t.Fatal("run status context does not close its pending/none/rebase union")
	}
	refreshDocument := readContextSchema(t, "schemas/domain/v1/context-refresh-result.schema.json")
	rebaseUnion := refreshDocument["$defs"].(map[string]any)["rebase_union"].(map[string]any)["oneOf"].([]any)
	wantRebaseReasons := []string{
		domain.ContextRebaseUnsupportedPacket, domain.ContextRebaseBaseContractChanged,
		domain.ContextRebaseDependencySetChanged, domain.ContextRebaseEventWindowExceeded,
		domain.ContextRebaseDeltaLimitExceeded, domain.ContextRebaseCumulativeLimitExceeded,
		domain.ContextRebaseUnsupportedEventType,
	}
	if len(rebaseUnion) != len(wantRebaseReasons) {
		t.Fatalf("context refresh rebase union has %d branches, want %d", len(rebaseUnion), len(wantRebaseReasons))
	}
	for index, value := range rebaseUnion {
		properties := value.(map[string]any)["properties"].(map[string]any)
		if got := properties["rebase_reason"].(map[string]any)["const"]; got != wantRebaseReasons[index] {
			t.Errorf("context refresh rebase branch %d reason = %v, want %q", index, got, wantRebaseReasons[index])
		}
		eventSequence := properties["event_sequence"].(map[string]any)
		if index == 0 {
			if eventSequence["const"] != float64(0) {
				t.Errorf("unsupported-packet refresh event sequence = %v, want 0", eventSequence)
			}
		} else if eventSequence["minimum"] != float64(1) {
			t.Errorf("durable rebase %q event sequence = %v, want minimum 1", wantRebaseReasons[index], eventSequence)
		}
	}
	for _, contract := range []struct {
		path   string
		status string
		ref    string
	}{
		{"schemas/domain/v1/context-refresh-result.schema.json", domain.ContextRefreshCreated, "#/$defs/pending_chain"},
		{"schemas/domain/v1/context-refresh-result.schema.json", domain.ContextRefreshPending, "#/$defs/pending_chain"},
		{"schemas/domain/v1/context-refresh-result.schema.json", domain.ContextRefreshUpToDate, "#/$defs/clear_chain"},
		{"schemas/domain/v1/context-refresh-result.schema.json", domain.ContextRefreshRebaseRequired, "#/$defs/rebase_union"},
		{"schemas/local/v1/context-refresh.result.schema.json", domain.ContextRefreshCreated, "../../domain/v1/context-refresh-result.schema.json#/$defs/pending_chain"},
		{"schemas/local/v1/context-refresh.result.schema.json", domain.ContextRefreshPending, "../../domain/v1/context-refresh-result.schema.json#/$defs/pending_chain"},
		{"schemas/local/v1/context-refresh.result.schema.json", domain.ContextRefreshUpToDate, "../../domain/v1/context-refresh-result.schema.json#/$defs/clear_chain"},
		{"schemas/local/v1/context-refresh.result.schema.json", domain.ContextRefreshRebaseRequired, "../../domain/v1/context-refresh-result.schema.json#/$defs/rebase_union"},
		{"schemas/domain/v1/context-delta-fetch-result.schema.json", domain.ContextDeltaPending, "context-refresh-result.schema.json#/$defs/pending_chain"},
		{"schemas/domain/v1/context-delta-fetch-result.schema.json", domain.ContextDeltaNonePending, "context-refresh-result.schema.json#/$defs/clear_chain"},
		{"schemas/domain/v1/context-delta-fetch-result.schema.json", domain.ContextDeltaRebaseRequired, "context-refresh-result.schema.json#/$defs/rebase_union"},
	} {
		document := readContextSchema(t, contract.path)
		conditional := contextStatusConditional(t, document, contract.status)
		if got := conditional["then"].(map[string]any)["$ref"]; got != contract.ref {
			t.Errorf("%s status %q chain constraint = %v, want %q", contract.path, contract.status, got, contract.ref)
		}
	}
}

func TestContextStatusChainConstraintsRejectImpossibleFixtures(t *testing.T) {
	t.Parallel()
	document := readContextSchema(t, "schemas/domain/v1/context-refresh-result.schema.json")
	definitions := document["$defs"].(map[string]any)
	deltaID := "cdelta_0123456789abcdef0123456789abcdef"

	fixtures := []struct {
		name       string
		definition string
		value      map[string]any
		valid      bool
	}{
		{"pending exact", "pending_chain", map[string]any{"chain": map[string]any{"pending_delta_id": deltaID, "pending_sequence": 1.0}}, true},
		{"pending preserves latest", "pending_chain", map[string]any{"chain": map[string]any{"pending_delta_id": deltaID, "pending_sequence": 2.0, "latest_delta_id": deltaID, "latest_sequence": 2.0}}, true},
		{"pending missing id", "pending_chain", map[string]any{"chain": map[string]any{"pending_sequence": 1.0}}, false},
		{"pending zero sequence", "pending_chain", map[string]any{"chain": map[string]any{"pending_delta_id": deltaID, "pending_sequence": 0.0}}, false},
		{"pending with rebase reason", "pending_chain", map[string]any{"chain": map[string]any{"pending_delta_id": deltaID, "pending_sequence": 1.0, "rebase_reason": domain.ContextRebaseBaseContractChanged}}, false},
		{"pending with rebase event", "pending_chain", map[string]any{"chain": map[string]any{"pending_delta_id": deltaID, "pending_sequence": 1.0, "rebase_event_sequence": 8.0}}, false},

		{"clear initial", "clear_chain", map[string]any{"chain": map[string]any{"pending_sequence": 0.0}}, true},
		{"clear after acknowledgement preserves latest", "clear_chain", map[string]any{"chain": map[string]any{"pending_sequence": 0.0, "latest_delta_id": deltaID, "latest_sequence": 1.0, "last_acknowledged_delta_id": deltaID, "last_acknowledged_sequence": 1.0}}, true},
		{"clear pending id", "clear_chain", map[string]any{"chain": map[string]any{"pending_sequence": 0.0, "pending_delta_id": deltaID}}, false},
		{"clear pending sequence", "clear_chain", map[string]any{"chain": map[string]any{"pending_sequence": 1.0}}, false},
		{"clear rebase reason", "clear_chain", map[string]any{"chain": map[string]any{"pending_sequence": 0.0, "rebase_reason": domain.ContextRebaseBaseContractChanged}}, false},
		{"clear rebase event", "clear_chain", map[string]any{"chain": map[string]any{"pending_sequence": 0.0, "rebase_event_sequence": 8.0}}, false},

		{"unsupported packet", "rebase_union", map[string]any{"rebase_reason": domain.ContextRebaseUnsupportedPacket, "event_sequence": 0.0, "chain": map[string]any{"pending_sequence": 0.0, "rebase_reason": domain.ContextRebaseUnsupportedPacket}}, true},
		{"durable exact", "rebase_union", map[string]any{"rebase_reason": domain.ContextRebaseBaseContractChanged, "event_sequence": 8.0, "chain": map[string]any{"pending_sequence": 0.0, "rebase_reason": domain.ContextRebaseBaseContractChanged, "rebase_event_sequence": 8.0}}, true},
		{"durable preserves latest", "rebase_union", map[string]any{"rebase_reason": domain.ContextRebaseDependencySetChanged, "event_sequence": 9.0, "chain": map[string]any{"pending_sequence": 0.0, "rebase_reason": domain.ContextRebaseDependencySetChanged, "rebase_event_sequence": 9.0, "latest_delta_id": deltaID, "latest_sequence": 1.0}}, true},
		{"rebase reason mismatch", "rebase_union", map[string]any{"rebase_reason": domain.ContextRebaseBaseContractChanged, "event_sequence": 8.0, "chain": map[string]any{"pending_sequence": 0.0, "rebase_reason": domain.ContextRebaseDependencySetChanged, "rebase_event_sequence": 8.0}}, false},
		{"durable zero event", "rebase_union", map[string]any{"rebase_reason": domain.ContextRebaseBaseContractChanged, "event_sequence": 0.0, "chain": map[string]any{"pending_sequence": 0.0, "rebase_reason": domain.ContextRebaseBaseContractChanged, "rebase_event_sequence": 8.0}}, false},
		{"durable missing chain event", "rebase_union", map[string]any{"rebase_reason": domain.ContextRebaseBaseContractChanged, "event_sequence": 8.0, "chain": map[string]any{"pending_sequence": 0.0, "rebase_reason": domain.ContextRebaseBaseContractChanged}}, false},
		{"durable with pending", "rebase_union", map[string]any{"rebase_reason": domain.ContextRebaseBaseContractChanged, "event_sequence": 8.0, "chain": map[string]any{"pending_delta_id": deltaID, "pending_sequence": 1.0, "rebase_reason": domain.ContextRebaseBaseContractChanged, "rebase_event_sequence": 8.0}}, false},
		{"unsupported nonzero event", "rebase_union", map[string]any{"rebase_reason": domain.ContextRebaseUnsupportedPacket, "event_sequence": 1.0, "chain": map[string]any{"pending_sequence": 0.0, "rebase_reason": domain.ContextRebaseUnsupportedPacket}}, false},
		{"unsupported with chain event", "rebase_union", map[string]any{"rebase_reason": domain.ContextRebaseUnsupportedPacket, "event_sequence": 0.0, "chain": map[string]any{"pending_sequence": 0.0, "rebase_reason": domain.ContextRebaseUnsupportedPacket, "rebase_event_sequence": 8.0}}, false},
	}
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			schema := definitions[fixture.definition].(map[string]any)
			if got := contextConstraintMatches(schema, document, fixture.value); got != fixture.valid {
				t.Errorf("constraint result = %t, want %t for %#v", got, fixture.valid, fixture.value)
			}
		})
	}

	chainDocument := readContextSchema(t, "schemas/domain/v1/context-delta-chain.schema.json")
	chainLifecycle := chainDocument["allOf"].([]any)[3].(map[string]any)
	for _, fixture := range []struct {
		name  string
		value map[string]any
		valid bool
	}{
		{"standalone pending", map[string]any{"pending_delta_id": deltaID, "pending_sequence": 1.0}, true},
		{"standalone acknowledged latest", map[string]any{"pending_sequence": 0.0, "latest_delta_id": deltaID, "latest_sequence": 1.0}, true},
		{"standalone unsupported rebase", map[string]any{"pending_sequence": 0.0, "rebase_reason": domain.ContextRebaseUnsupportedPacket}, true},
		{"standalone unsupported rebase pending", map[string]any{"pending_delta_id": deltaID, "pending_sequence": 1.0, "rebase_reason": domain.ContextRebaseUnsupportedPacket}, false},
		{"standalone durable rebase pending", map[string]any{"pending_delta_id": deltaID, "pending_sequence": 1.0, "rebase_reason": domain.ContextRebaseBaseContractChanged, "rebase_event_sequence": 8.0}, false},
	} {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			if got := contextConstraintMatches(chainLifecycle, chainDocument, fixture.value); got != fixture.valid {
				t.Errorf("chain lifecycle result = %t, want %t for %#v", got, fixture.valid, fixture.value)
			}
		})
	}
}

func TestMCPStatusSchemaRejectsUnknownLifecycleStatuses(t *testing.T) {
	t.Parallel()
	status := readContextSchema(t, "schemas/mcp/v1/get-status.output.schema.json")
	properties := status["properties"].(map[string]any)
	for field, invalid := range map[string]string{"run_status": "terminal", "task_status": "pending"} {
		allowed := contractStringSlice(properties[field].(map[string]any)["enum"])
		if len(allowed) == 0 || containsContractString(allowed, invalid) {
			t.Errorf("MCP status %s accepts invalid lifecycle value %q: %v", field, invalid, allowed)
		}
		if _, open := properties[field].(map[string]any)["type"]; open {
			t.Errorf("MCP status %s retains an open string contract", field)
		}
	}
}

func contextStatusConditional(t *testing.T, document map[string]any, status string) map[string]any {
	t.Helper()
	for _, value := range document["allOf"].([]any) {
		conditional := value.(map[string]any)
		ifSchema := conditional["if"].(map[string]any)
		properties := ifSchema["properties"].(map[string]any)
		if properties["status"].(map[string]any)["const"] == status {
			return conditional
		}
	}
	t.Fatalf("schema has no conditional for status %q", status)
	return nil
}

// contextConstraintMatches is deliberately limited to the keywords used by the
// refresh result's status/chain definitions. It lets the negative fixtures
// exercise the published constraints without adding a runtime schema library.
func contextConstraintMatches(schema, document map[string]any, value any) bool {
	if reference, exists := schema["$ref"].(string); exists {
		const prefix = "#/$defs/"
		if !strings.HasPrefix(reference, prefix) {
			return false
		}
		definitions, ok := document["$defs"].(map[string]any)
		if !ok {
			return false
		}
		target, ok := definitions[strings.TrimPrefix(reference, prefix)].(map[string]any)
		if !ok || !contextConstraintMatches(target, document, value) {
			return false
		}
	}
	if expectedType, exists := schema["type"].(string); exists {
		switch expectedType {
		case "object":
			if _, ok := value.(map[string]any); !ok {
				return false
			}
		case "integer":
			number, ok := contextContractNumber(value)
			if !ok || number != float64(int64(number)) {
				return false
			}
		}
	}
	if constant, exists := schema["const"]; exists && !contextContractEqual(value, constant) {
		return false
	}
	if enum, exists := schema["enum"].([]any); exists {
		matched := false
		for _, candidate := range enum {
			matched = matched || contextContractEqual(value, candidate)
		}
		if !matched {
			return false
		}
	}
	if minimum, exists := contextContractNumber(schema["minimum"]); exists {
		number, ok := contextContractNumber(value)
		if !ok || number < minimum {
			return false
		}
	}
	object, isObject := value.(map[string]any)
	if required, exists := schema["required"].([]any); exists {
		if !isObject {
			return false
		}
		for _, field := range contractStringSlice(required) {
			if _, present := object[field]; !present {
				return false
			}
		}
	}
	if properties, exists := schema["properties"].(map[string]any); exists && isObject {
		for field, rawConstraint := range properties {
			fieldValue, present := object[field]
			if !present {
				continue
			}
			constraint, ok := rawConstraint.(map[string]any)
			if !ok || !contextConstraintMatches(constraint, document, fieldValue) {
				return false
			}
		}
	}
	if constraints, exists := schema["allOf"].([]any); exists {
		for _, rawConstraint := range constraints {
			if !contextConstraintMatches(rawConstraint.(map[string]any), document, value) {
				return false
			}
		}
	}
	if constraints, exists := schema["anyOf"].([]any); exists {
		matched := false
		for _, rawConstraint := range constraints {
			matched = matched || contextConstraintMatches(rawConstraint.(map[string]any), document, value)
		}
		if !matched {
			return false
		}
	}
	if constraints, exists := schema["oneOf"].([]any); exists {
		matches := 0
		for _, rawConstraint := range constraints {
			if contextConstraintMatches(rawConstraint.(map[string]any), document, value) {
				matches++
			}
		}
		if matches != 1 {
			return false
		}
	}
	if rawConstraint, exists := schema["not"].(map[string]any); exists && contextConstraintMatches(rawConstraint, document, value) {
		return false
	}
	return true
}

func contextContractNumber(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case int:
		return float64(number), true
	case int64:
		return float64(number), true
	default:
		return 0, false
	}
}

func contextContractEqual(left, right any) bool {
	leftNumber, leftIsNumber := contextContractNumber(left)
	rightNumber, rightIsNumber := contextContractNumber(right)
	if leftIsNumber && rightIsNumber {
		return leftNumber == rightNumber
	}
	return reflect.DeepEqual(left, right)
}

func TestContextExplanationV3PreservesLegacyReadCompatibility(t *testing.T) {
	t.Parallel()
	schema := readContextSchema(t, "schemas/domain/v1/context-explanation-v3.schema.json")
	definitions := schema["$defs"].(map[string]any)
	budget := definitions["budget"].(map[string]any)["properties"].(map[string]any)
	collaboration := budget["collaboration"].(map[string]any)["properties"].(map[string]any)
	if collaboration["limit_bytes"].(map[string]any)["minimum"] != float64(0) {
		t.Fatal("context explanation v3 cannot represent a pre-v4 packet's absent collaboration budget")
	}
}

func readContextSchema(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func contractStringSlice(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func TestContextV3PublishesBoundedExplicitKnowledgeLinks(t *testing.T) {
	t.Parallel()
	paramsData, err := os.ReadFile("schemas/local/v1/context-build-v2.params.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var params struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			MaxItems    int  `json:"maxItems"`
			UniqueItems bool `json:"uniqueItems"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(paramsData, &params); err != nil {
		t.Fatal(err)
	}
	knowledge := params.Properties["knowledge_revision_ids"]
	if knowledge.MaxItems != 16 || !knowledge.UniqueItems || !containsContractString(params.Required, "knowledge_revision_ids") {
		t.Fatalf("context knowledge revision parameter = %#v, required = %v", knowledge, params.Required)
	}

	packetData, err := os.ReadFile("schemas/domain/v1/context-packet-v3.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var packet struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(packetData, &packet); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"requested_knowledge_revision_ids", "accepted_knowledge", "budget"} {
		if !containsContractString(packet.Required, field) {
			t.Errorf("context packet v3 does not require %q", field)
		}
	}
}

func containsContractString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func TestScopedMCPErrorVocabularyIsBounded(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("schemas/mcp/v1/tool-error.schema.json")
	if err != nil {
		t.Fatalf("os.ReadFile(tool error schema) error = %v", err)
	}
	var document struct {
		Properties map[string]struct {
			Enum []string `json:"enum"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("json.Unmarshal(tool error schema) error = %v", err)
	}
	want := []string{"invalid_input", "out_of_scope", "denied_by_policy", "temporarily_unavailable"}
	if got := document.Properties["code"].Enum; len(got) != len(want) {
		t.Fatalf("tool error codes = %v, want %v", got, want)
	} else {
		for index := range want {
			if got[index] != want[index] {
				t.Fatalf("tool error codes = %v, want %v", got, want)
			}
		}
	}
}
