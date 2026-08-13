package localapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"crewfold/internal/domain"
)

func TestKnowledgeSearchClientPreservesLimitPresenceAndSendsDefault(t *testing.T) {
	t.Parallel()

	zero := 0
	for _, test := range []struct {
		name  string
		limit *int
		want  int
	}{
		{name: "omitted gets default", want: 20},
		{name: "explicit zero remains explicit", limit: &zero, want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			captured := captureKnowledgeSearchRequest(t, KnowledgeSearchParams{
				Workspace: "personal", Project: "demo", Query: "term", Limit: test.limit,
			})
			if captured.Limit == nil || *captured.Limit != test.want {
				t.Fatalf("captured limit = %v, want pointer to %d", captured.Limit, test.want)
			}
		})
	}
}

func TestKnowledgeDisputeClientSendsExactReadWithoutAuthorityFields(t *testing.T) {
	t.Parallel()
	request := captureCuratorRequest(t, MethodKnowledgeDispute, func(client *Client) error {
		_, err := client.KnowledgeDispute(context.Background(), "personal", "krev_exact")
		return err
	}, KnowledgeDisputeResult{Schema: KnowledgeDisputeSchema, Type: "knowledge_revision_dispute"})
	var params KnowledgeQueryParams
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.Workspace != "personal" || params.KnowledgeRevision != "krev_exact" {
		t.Fatalf("knowledge dispute params=%#v", params)
	}
	for _, forbidden := range []string{`"actor"`, `"currency_status"`, `"contradiction"`} {
		if strings.Contains(string(request.Params), forbidden) {
			t.Errorf("knowledge dispute read exposed mutation input %s: %s", forbidden, request.Params)
		}
	}
}

func captureKnowledgeSearchRequest(t *testing.T, params KnowledgeSearchParams) KnowledgeSearchParams {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "local-api.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	captured := make(chan KnowledgeSearchParams, 1)
	serverResult := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverResult <- err
			return
		}
		defer connection.Close()
		decoder := json.NewDecoder(connection)
		encoder := json.NewEncoder(connection)
		var hello Request
		if err := decoder.Decode(&hello); err != nil {
			serverResult <- err
			return
		}
		if hello.Method != MethodHello {
			serverResult <- fmt.Errorf("first method = %q, want %q", hello.Method, MethodHello)
			return
		}
		if err := encoder.Encode(MarshalResult(hello.ID, MaxProtocol, HelloResult{
			Type: "hello", SelectedProtocol: MaxProtocol, ServerMin: MinProtocol, ServerMax: MaxProtocol,
		})); err != nil {
			serverResult <- err
			return
		}
		var search Request
		if err := decoder.Decode(&search); err != nil {
			serverResult <- err
			return
		}
		if search.Method != MethodKnowledgeSearch {
			serverResult <- fmt.Errorf("second method = %q, want %q", search.Method, MethodKnowledgeSearch)
			return
		}
		var decoded KnowledgeSearchParams
		if err := json.Unmarshal(search.Params, &decoded); err != nil {
			serverResult <- err
			return
		}
		captured <- decoded
		response := KnowledgeSearchResult{
			Schema: KnowledgeSearchSchema, Type: "knowledge_search",
			Search: domain.KnowledgeSearchResult{Matches: []domain.KnowledgeSearchMatch{}},
		}
		serverResult <- encoder.Encode(MarshalResult(search.ID, search.Protocol, response))
	}()

	if _, err := NewClient(socketPath).KnowledgeSearch(context.Background(), params); err != nil {
		t.Fatalf("KnowledgeSearch() error = %v", err)
	}
	if err := <-serverResult; err != nil {
		t.Fatalf("fake local API server error = %v", err)
	}
	return <-captured
}
