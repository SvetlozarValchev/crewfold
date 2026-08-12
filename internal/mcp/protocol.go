// Package mcp defines Crewfold's small, run-scoped MCP transport surface.
package mcp

import "encoding/json"

const (
	JSONRPCVersion  = "2.0"
	ProtocolVersion = "2025-06-18"
	CapabilityMeta  = "crewfold/capability"
)

type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int        `json:"code"`
	Message string     `json:"message"`
	Data    *ToolError `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return e.Message }

type ToolError struct {
	Code       string   `json:"code"`
	Message    string   `json:"message"`
	Retryable  bool     `json:"retryable"`
	RelatedIDs []string `json:"related_ids,omitempty"`
}

type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type ToolCallResult struct {
	Content           []Content       `json:"content"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
	IsError           bool            `json:"isError,omitempty"`
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MIMEType    string `json:"mimeType,omitempty"`
}

type ResourceContents struct {
	URI      string `json:"uri"`
	MIMEType string `json:"mimeType"`
	Text     string `json:"text"`
}

func Success(id json.RawMessage, result any) Response {
	return Response{JSONRPC: JSONRPCVersion, ID: id, Result: result}
}

func Failure(id json.RawMessage, code int, message string, data *ToolError) Response {
	return Response{JSONRPC: JSONRPCVersion, ID: id, Error: &RPCError{Code: code, Message: message, Data: data}}
}
