// Package localapi defines the versioned protocol shared by Crewfold's local
// daemon and clients.
package localapi

import (
	"encoding/json"
	"fmt"

	"crewfold/internal/buildinfo"
)

const (
	MinProtocol = 1
	MaxProtocol = 1

	MethodHello  = "system.hello"
	MethodStatus = "system.status"
	MethodStop   = "system.stop"

	StatusSchema = "urn:crewfold:schema:local-api:status-result:v1"
	StopSchema   = "urn:crewfold:schema:local-api:stop-result:v1"
)

// Request is one newline-delimited local API request. Hello requests omit
// Protocol; all other methods use the value selected during hello.
type Request struct {
	ID       string          `json:"id"`
	Protocol int             `json:"protocol,omitempty"`
	Method   string          `json:"method"`
	Params   json.RawMessage `json:"params,omitempty"`
}

// Response is one newline-delimited local API response.
type Response struct {
	ID       string          `json:"id"`
	Protocol int             `json:"protocol,omitempty"`
	Result   json.RawMessage `json:"result,omitempty"`
	Error    *APIError       `json:"error,omitempty"`
}

// APIError is the stable error body returned by the daemon.
type APIError struct {
	Code      string         `json:"code"`
	Message   string         `json:"message"`
	Retryable bool           `json:"retryable"`
	Details   map[string]any `json:"details,omitempty"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

type HelloParams struct {
	MinProtocol int `json:"min_protocol"`
	MaxProtocol int `json:"max_protocol"`
}

type HelloResult struct {
	Type             string         `json:"type"`
	SelectedProtocol int            `json:"selected_protocol"`
	ServerMin        int            `json:"server_min_protocol"`
	ServerMax        int            `json:"server_max_protocol"`
	Version          buildinfo.Info `json:"version"`
}

type StatusResult struct {
	Schema          string         `json:"schema"`
	Type            string         `json:"type"`
	Status          string         `json:"status"`
	Protocol        int            `json:"protocol"`
	PID             int            `json:"pid"`
	StartedAt       string         `json:"started_at"`
	UptimeMillis    int64          `json:"uptime_ms"`
	ServerVersion   buildinfo.Info `json:"server_version"`
	ActiveRequests  int            `json:"active_requests"`
	ShutdownPending bool           `json:"shutdown_pending"`
}

type StopResult struct {
	Schema string `json:"schema"`
	Type   string `json:"type"`
	Status string `json:"status"`
}

// MarshalResult constructs a response without exposing server-only wire types.
func MarshalResult(id string, protocol int, result any) Response {
	data, err := json.Marshal(result)
	if err != nil {
		return ErrorResponse(id, protocol, &APIError{
			Code:      "internal_error",
			Message:   fmt.Sprintf("encode local API result: %v", err),
			Retryable: false,
		})
	}
	return Response{ID: id, Protocol: protocol, Result: data}
}

func ErrorResponse(id string, protocol int, apiError *APIError) Response {
	return Response{ID: id, Protocol: protocol, Error: apiError}
}
