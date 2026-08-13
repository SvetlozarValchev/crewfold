package localapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func TestPortableKnowledgeClientsSendOnlyFilesystemDescriptorAndScope(t *testing.T) {
	t.Parallel()
	exportRequest := captureCuratorRequest(t, MethodKnowledgeExport, func(client *Client) error {
		_, err := client.KnowledgeExport(context.Background(), KnowledgeExportParams{
			Workspace: "personal", Project: "engine", Directory: "/tmp/export",
		})
		return err
	}, KnowledgeExportResult{Schema: KnowledgeExportSchema, Type: "knowledge_export"})
	var exportParams KnowledgeExportParams
	if err := json.Unmarshal(exportRequest.Params, &exportParams); err != nil || exportParams.Directory != "/tmp/export" {
		t.Fatalf("export params=%#v error=%v", exportParams, err)
	}

	digest := strings.Repeat("a", 64)
	importRequest := captureCuratorRequest(t, MethodKnowledgeImport, func(client *Client) error {
		_, err := client.KnowledgeImport(context.Background(), KnowledgeImportParams{
			Workspace: "personal", Project: "engine", Directory: "/tmp/import",
			ExpectedContentSHA256: digest, CreateScope: true,
		})
		return err
	}, KnowledgeImportResult{Schema: KnowledgeImportSchema, Type: "knowledge_import"})
	var importParams KnowledgeImportParams
	if err := json.Unmarshal(importRequest.Params, &importParams); err != nil {
		t.Fatal(err)
	}
	if importParams.ExpectedContentSHA256 != digest || !importParams.CreateScope || strings.TrimSpace(importParams.IdempotencyKey) == "" {
		t.Fatalf("import params=%#v", importParams)
	}
	falseScopeRequest := captureCuratorRequest(t, MethodKnowledgeImport, func(client *Client) error {
		_, err := client.KnowledgeImport(context.Background(), KnowledgeImportParams{
			Workspace: "personal", Project: "engine", Directory: "/tmp/import", ExpectedContentSHA256: digest,
		})
		return err
	}, KnowledgeImportResult{Schema: KnowledgeImportSchema, Type: "knowledge_import"})
	if !strings.Contains(string(falseScopeRequest.Params), `"create_scope":false`) {
		t.Fatalf("false create_scope was not explicit: %s", falseScopeRequest.Params)
	}
	for _, request := range []Request{exportRequest, importRequest, falseScopeRequest} {
		for _, forbidden := range []string{`"actor"`, `"provider"`, `"manifest"`, `"markdown"`, `"bundle_bytes"`} {
			if strings.Contains(string(request.Params), forbidden) {
				t.Errorf("request exposed %s: %s", forbidden, request.Params)
			}
		}
		if len(request.Params) >= 64*1024 {
			t.Errorf("portable request carried %d bytes", len(request.Params))
		}
	}
}

func TestPortableKnowledgeClientRejectsUnknownResultFields(t *testing.T) {
	t.Parallel()
	request := captureCuratorRequestResultError(t, MethodKnowledgeExport, func(client *Client) error {
		_, err := client.KnowledgeExport(context.Background(), KnowledgeExportParams{
			Workspace: "personal", Project: "engine", Directory: "/tmp/export",
		})
		return err
	}, map[string]any{"schema": KnowledgeExportSchema, "type": "knowledge_export", "unexpected": true})
	if request == nil || !strings.Contains(request.Error(), "unknown field") {
		t.Fatalf("KnowledgeExport unknown result error=%v", request)
	}
}

func captureCuratorRequestResultError(t *testing.T, method string, call func(*Client) error, result any) error {
	t.Helper()
	return capturePortableResultError(t, method, call, result)
}

func capturePortableResultError(t *testing.T, method string, call func(*Client) error, result any) error {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "local-api.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverResult := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverResult <- err
			return
		}
		defer connection.Close()
		decoder, encoder := json.NewDecoder(connection), json.NewEncoder(connection)
		var hello Request
		if err := decoder.Decode(&hello); err != nil {
			serverResult <- err
			return
		}
		if err := encoder.Encode(MarshalResult(hello.ID, MaxProtocol, HelloResult{
			Type: "hello", SelectedProtocol: MaxProtocol, ServerMin: MinProtocol, ServerMax: MaxProtocol,
		})); err != nil {
			serverResult <- err
			return
		}
		var request Request
		if err := decoder.Decode(&request); err != nil {
			serverResult <- err
			return
		}
		if request.Method != method {
			serverResult <- fmt.Errorf("method=%s want=%s", request.Method, method)
			return
		}
		serverResult <- encoder.Encode(MarshalResult(request.ID, request.Protocol, result))
	}()
	clientError := call(NewClient(socketPath))
	if err := <-serverResult; err != nil {
		t.Fatalf("fake server error=%v", err)
	}
	return clientError
}
