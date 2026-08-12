package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

const maximumMessageBytes = 64 * 1024

type Client struct {
	connection net.Conn
	scanner    *bufio.Scanner
	encoder    *json.Encoder
	token      string
	nextID     int64
}

func Dial(ctx context.Context, socketPath, token string) (*Client, error) {
	dialer := net.Dialer{}
	connection, err := dialer.DialContext(ctx, "unix", strings.TrimSpace(socketPath))
	if err != nil {
		return nil, fmt.Errorf("dial Crewfold MCP socket: %w", err)
	}
	scanner := bufio.NewScanner(connection)
	scanner.Buffer(make([]byte, 4096), maximumMessageBytes)
	return &Client{connection: connection, scanner: scanner, encoder: json.NewEncoder(connection), token: strings.TrimSpace(token)}, nil
}

func (c *Client) Close() error { return c.connection.Close() }

func (c *Client) Initialize(ctx context.Context) error {
	var result struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	err := c.Call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "crewfold-run", "version": "1"},
	}, &result)
	if err != nil {
		return err
	}
	if result.ProtocolVersion != ProtocolVersion {
		return errors.New("Crewfold MCP server selected an unsupported protocol")
	}
	return nil
}

func (c *Client) CallTool(ctx context.Context, name string, arguments any) (ToolCallResult, error) {
	var result ToolCallResult
	err := c.Call(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments}, &result)
	return result, err
}

func (c *Client) ReadResource(ctx context.Context, uri string) ([]ResourceContents, error) {
	var result struct {
		Contents []ResourceContents `json:"contents"`
	}
	err := c.Call(ctx, "resources/read", map[string]any{"uri": uri}, &result)
	return result.Contents, err
}

func (c *Client) Call(ctx context.Context, method string, params map[string]any, result any) error {
	if params == nil {
		params = make(map[string]any)
	}
	params["_meta"] = map[string]string{CapabilityMeta: c.token}
	c.nextID++
	request := map[string]any{"jsonrpc": JSONRPCVersion, "id": c.nextID, "method": method, "params": params}
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.connection.SetDeadline(deadline)
		defer c.connection.SetDeadline(time.Time{})
	}
	if err := c.encoder.Encode(request); err != nil {
		return fmt.Errorf("write MCP request: %w", err)
	}
	if !c.scanner.Scan() {
		if err := c.scanner.Err(); err != nil {
			return fmt.Errorf("read MCP response: %w", err)
		}
		return errors.New("MCP server closed without a response")
	}
	var response struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *RPCError       `json:"error"`
	}
	if err := json.Unmarshal(c.scanner.Bytes(), &response); err != nil {
		return fmt.Errorf("decode MCP response: %w", err)
	}
	if response.JSONRPC != JSONRPCVersion {
		return errors.New("MCP response has an invalid JSON-RPC version")
	}
	if response.Error != nil {
		return response.Error
	}
	if result != nil && len(response.Result) != 0 {
		if err := json.Unmarshal(response.Result, result); err != nil {
			return fmt.Errorf("decode MCP result: %w", err)
		}
	}
	return nil
}
