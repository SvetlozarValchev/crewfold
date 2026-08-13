package cli

import (
	"reflect"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

const (
	testContextDeltaRun    = "run_0123456789abcdef0123456789abcdef"
	testContextDeltaPacket = "ctx_0123456789abcdef0123456789abcdef"
	testContextDeltaID     = "cdelta_0123456789abcdef0123456789abcdef"
)

func TestContextRefreshCLIUsesOwnerLocalSurface(t *testing.T) {
	t.Parallel()
	delta := domain.ContextDelta{ID: testContextDeltaID, RunID: testContextDeltaRun, ContextPacketID: testContextDeltaPacket, Sequence: 1, FromEventSequence: 9, ThroughEventSequence: 12, ByteSize: 412}
	client := &fakeDaemonClient{contextRefresh: localapi.ContextRefreshResult{
		Schema: localapi.ContextRefreshSchema, Type: "context_refresh",
		ContextRefreshResult: domain.ContextRefreshResult{Status: domain.ContextRefreshCreated, RunID: testContextDeltaRun, ContextPacketID: testContextDeltaPacket, ScannedFromEventSequence: 9, ScannedThroughEventSequence: 12, Delta: &delta, EventSequence: 21},
	}}
	app, stdout, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	if exit := app.Run([]string{"context", "refresh", testContextDeltaRun, "--workspace", "personal", "--socket", "/tmp/crewfold.sock", "--idempotency-key", "refresh-one"}); exit != ExitOK {
		t.Fatalf("context refresh exit=%d stderr=%q", exit, stderr.String())
	}
	if client.contextRefreshParams.Workspace != "personal" || client.contextRefreshParams.Run != testContextDeltaRun || client.contextRefreshParams.IdempotencyKey != "refresh-one" {
		t.Fatalf("refresh params = %#v", client.contextRefreshParams)
	}
	for _, wanted := range []string{"status: created", "delta: " + testContextDeltaID, "events: (9, 12]", "bytes: 412", "event sequence: 21"} {
		if !strings.Contains(stdout.String(), wanted) {
			t.Fatalf("refresh stdout=%q, want %q", stdout.String(), wanted)
		}
	}
}

func TestContextDeltaCLIListsAndQueriesOwnerInspection(t *testing.T) {
	t.Parallel()
	delta := domain.ContextDelta{ID: testContextDeltaID, RunID: testContextDeltaRun, ContextPacketID: testContextDeltaPacket, Sequence: 2, FromEventSequence: 12, ThroughEventSequence: 18, ByteSize: 512}
	client := &fakeDaemonClient{
		contextDeltaList: localapi.ContextDeltaListResult{Schema: localapi.ContextDeltaListSchema, Type: "context_delta_list", ContextDeltaList: domain.ContextDeltaList{
			Chain:         domain.ContextDeltaChain{RunID: testContextDeltaRun, ContextPacketID: testContextDeltaPacket},
			AfterSequence: 1, NextSequence: 2, Deltas: []domain.ContextDelta{delta},
		}},
		contextDeltaShow:    localapi.ContextDeltaShowResult{Schema: localapi.ContextDeltaShowSchema, Type: "context_delta", Delta: delta},
		contextDeltaExplain: localapi.ContextDeltaExplainResult{Schema: localapi.ContextDeltaExplainSchema, Type: "context_delta_explanation", Explanation: domain.ContextDeltaExplanation{DeltaID: testContextDeltaID, RunID: testContextDeltaRun, ContextPacketID: testContextDeltaPacket, Sequence: 2, FromEventSequence: 12, ThroughEventSequence: 18, ChangeKinds: []string{domain.ContextDeltaMessageReceived}, ByteSize: 512}},
	}

	app, stdout, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	if exit := app.Run([]string{"context", "delta", "list", testContextDeltaRun, "--workspace", "personal", "--after-sequence", "1", "--limit", "7", "--socket", "/tmp/crewfold.sock"}); exit != ExitOK {
		t.Fatalf("delta list exit=%d stderr=%q", exit, stderr.String())
	}
	if client.contextDeltaListParams.AfterSequence == nil || *client.contextDeltaListParams.AfterSequence != 1 || client.contextDeltaListParams.Limit != 7 || !strings.Contains(stdout.String(), testContextDeltaID) {
		t.Fatalf("list params=%#v stdout=%q", client.contextDeltaListParams, stdout.String())
	}

	app, stdout, stderr = newTestApp()
	app.newClient = func(string) daemonClient { return client }
	if exit := app.Run([]string{"context", "delta", "show", testContextDeltaID, "--workspace", "personal", "--socket", "/tmp/crewfold.sock"}); exit != ExitOK {
		t.Fatalf("delta show exit=%d stderr=%q", exit, stderr.String())
	}
	if !reflect.DeepEqual(client.contextDeltaQueryArgs, []string{"personal", testContextDeltaID}) || !strings.Contains(stdout.String(), "sequence: 2") {
		t.Fatalf("show args=%v stdout=%q", client.contextDeltaQueryArgs, stdout.String())
	}
}

func TestContextRefreshCLIExplainsRebaseWithoutTreatingItAsFailure(t *testing.T) {
	t.Parallel()
	client := &fakeDaemonClient{contextRefresh: localapi.ContextRefreshResult{
		Schema: localapi.ContextRefreshSchema, Type: "context_refresh",
		ContextRefreshResult: domain.ContextRefreshResult{Status: domain.ContextRefreshRebaseRequired, RunID: testContextDeltaRun, ContextPacketID: testContextDeltaPacket, RebaseReason: domain.ContextRebaseUnsupportedPacket},
	}}
	app, stdout, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	if exit := app.Run([]string{"context", "refresh", testContextDeltaRun, "--workspace", "personal", "--socket", "/tmp/crewfold.sock"}); exit != ExitOK {
		t.Fatalf("rebase refresh exit=%d stderr=%q", exit, stderr.String())
	}
	if !strings.Contains(stdout.String(), "status: rebase_required") || !strings.Contains(stdout.String(), "start a replacement run") {
		t.Fatalf("rebase stdout=%q", stdout.String())
	}
}

func TestContextDeltaCLIHelpListsEveryOwnerInspectionCommand(t *testing.T) {
	t.Parallel()
	app, stdout, stderr := newTestApp()
	if exit := app.Run([]string{"context", "delta", "--help"}); exit != ExitOK {
		t.Fatalf("context delta help exit=%d stderr=%q", exit, stderr.String())
	}
	for _, command := range []string{"context refresh", "context delta list", "context delta show", "context delta explain"} {
		if !strings.Contains(stdout.String(), command) {
			t.Errorf("context delta help omits %q: %s", command, stdout.String())
		}
	}
}
