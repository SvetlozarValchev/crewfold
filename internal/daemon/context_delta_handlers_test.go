package daemon

import (
	"context"
	"testing"

	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func TestContextDeltaLocalHandlersAreStrictAndExposeNoOwnerAcknowledge(t *testing.T) {
	running := startTestServer(t, testConfig(t))
	client := localapi.NewClient(running.config.SocketPath)
	if _, err := client.WorkspaceInit(context.Background(), "personal", "context-delta-handler-workspace"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		method string
		params map[string]any
	}{
		{localapi.MethodContextRefresh, map[string]any{"workspace": "personal", "run": "run_0123456789abcdef0123456789abcdef", "idempotency_key": "refresh", "cursor": 4}},
		{localapi.MethodContextDeltaList, map[string]any{"workspace": "personal", "run": "run_0123456789abcdef0123456789abcdef", "after_sequence": 0, "limit": 20, "actor": "local-owner"}},
		{localapi.MethodContextDeltaShow, map[string]any{"workspace": "personal", "delta": "cdelta_0123456789abcdef0123456789abcdef", "run": "run_untrusted"}},
		{localapi.MethodContextDeltaExplain, map[string]any{"workspace": "personal", "delta": "cdelta_0123456789abcdef0123456789abcdef", "expected_sequence": 1}},
	} {
		response := rawLocalAPIRequest(t, running.config.SocketPath, test.method, test.params)
		if response.Error == nil || response.Error.Code != "invalid_request" || response.Error.Retryable {
			t.Errorf("%s injected params response = %#v", test.method, response.Error)
		}
	}
	missingCursor := rawLocalAPIRequest(t, running.config.SocketPath, localapi.MethodContextDeltaList, map[string]any{
		"workspace": "personal", "run": "run_0123456789abcdef0123456789abcdef", "limit": 20,
	})
	if missingCursor.Error == nil || missingCursor.Error.Code != "invalid_request" {
		t.Fatalf("missing after_sequence response = %#v", missingCursor.Error)
	}

	missing := rawLocalAPIRequest(t, running.config.SocketPath, localapi.MethodContextDeltaShow, map[string]any{
		"workspace": "personal", "delta": "cdelta_0123456789abcdef0123456789abcdef",
	})
	if missing.Error == nil || missing.Error.Code != store.CodeContextDeltaNotFound || missing.Error.Retryable {
		t.Fatalf("missing context delta response = %#v", missing.Error)
	}
	for _, deltaID := range []string{"cdelta_short", " cdelta_0123456789abcdef0123456789abcdef"} {
		invalid := rawLocalAPIRequest(t, running.config.SocketPath, localapi.MethodContextDeltaShow, map[string]any{
			"workspace": "personal", "delta": deltaID,
		})
		if invalid.Error == nil || invalid.Error.Code != store.CodeInvalidContextDelta || invalid.Error.Retryable {
			t.Errorf("invalid context delta %q response = %#v", deltaID, invalid.Error)
		}
	}
	ownerAck := rawLocalAPIRequest(t, running.config.SocketPath, "context.delta.acknowledge", map[string]any{})
	if ownerAck.Error == nil || ownerAck.Error.Code != "method_not_found" {
		t.Fatalf("owner acknowledgement response = %#v", ownerAck.Error)
	}
	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := running.wait(); err != nil {
		t.Fatal(err)
	}
}

func TestContextDeltaStoreErrorsKeepStableLocalCodes(t *testing.T) {
	t.Parallel()
	for _, code := range []string{store.CodeInvalidContextDelta, store.CodeContextDeltaNotFound} {
		response := storeErrorResponse(localapi.Request{ID: "delta", Protocol: localapi.MaxProtocol}, &store.Error{Code: code, Message: code})
		if response.Error == nil || response.Error.Code != code || response.Error.Retryable {
			t.Errorf("storeErrorResponse(%s) = %#v", code, response.Error)
		}
	}
}
