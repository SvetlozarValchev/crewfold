package execution

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"time"

	"crewfold/internal/mcp"
)

const mcpBridgeMaximumMessageBytes = 64 * 1024

// RunMCPStdioBridge adapts Codex's local STDIO MCP transport to Crewfold's
// owner-only Unix socket. The run token is read from its private file and added
// only to requests sent over the local socket; it is never written to stdio.
func RunMCPStdioBridge(input io.Reader, output, diagnostics io.Writer) int {
	socketPath := strings.TrimSpace(os.Getenv("CREWFOLD_MCP_SOCKET"))
	capabilityPath := strings.TrimSpace(os.Getenv("CREWFOLD_MCP_CAPABILITY_FILE"))
	if socketPath == "" || capabilityPath == "" {
		fmt.Fprintln(diagnostics, "Crewfold MCP bridge configuration is incomplete: socket and capability file are required")
		return 1
	}
	token, err := readBridgeCapability(capabilityPath)
	if err != nil {
		fmt.Fprintf(diagnostics, "Crewfold MCP bridge capability failed: %v\n", err)
		return 1
	}
	connection, err := dialBridgeSocket(socketPath)
	if err != nil {
		fmt.Fprintf(diagnostics, "Crewfold MCP bridge socket failed: %v\n", err)
		return 1
	}
	defer connection.Close()

	requests := bufio.NewScanner(input)
	requests.Buffer(make([]byte, 4096), mcpBridgeMaximumMessageBytes)
	responses := bufio.NewScanner(connection)
	responses.Buffer(make([]byte, 4096), mcpBridgeMaximumMessageBytes)
	encoder := json.NewEncoder(connection)
	writer := json.NewEncoder(output)
	for requests.Scan() {
		line := bytes.TrimSpace(requests.Bytes())
		if len(line) == 0 {
			continue
		}
		request, notification, err := prepareBridgeRequest(line, token)
		if err != nil {
			_ = writer.Encode(mcp.Failure(nil, -32600, err.Error(), nil))
			continue
		}
		if notification {
			continue
		}
		if err := encoder.Encode(request); err != nil {
			fmt.Fprintf(diagnostics, "Crewfold MCP bridge write failed: %v\n", err)
			return 1
		}
		if !responses.Scan() {
			if err := responses.Err(); err != nil {
				fmt.Fprintf(diagnostics, "Crewfold MCP bridge response failed: %v\n", err)
			} else {
				fmt.Fprintln(diagnostics, "Crewfold MCP bridge response failed: daemon closed the connection")
			}
			return 1
		}
		if _, err := output.Write(append(append([]byte(nil), responses.Bytes()...), '\n')); err != nil {
			fmt.Fprintf(diagnostics, "Crewfold MCP bridge stdout failed: %v\n", err)
			return 1
		}
	}
	if err := requests.Err(); err != nil {
		fmt.Fprintf(diagnostics, "Crewfold MCP bridge request failed: %v\n", err)
		return 1
	}
	return 0
}

func readBridgeCapability(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("capability file must be a private, non-symlink regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if len(data) == 0 || len(data) > 1024 || strings.TrimSpace(string(data)) == "" {
		return "", errors.New("capability file is empty or oversized")
	}
	return strings.TrimSpace(string(data)), nil
}

func dialBridgeSocket(path string) (net.Conn, error) {
	return net.DialTimeout("unix", path, 5*time.Second)
}

func prepareBridgeRequest(data []byte, token string) (map[string]any, bool, error) {
	var request map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&request); err != nil {
		return nil, false, errors.New("invalid JSON-RPC request")
	}
	if request["jsonrpc"] != mcp.JSONRPCVersion {
		return nil, false, errors.New("invalid JSON-RPC version")
	}
	method, ok := request["method"].(string)
	if !ok || strings.TrimSpace(method) == "" {
		return nil, false, errors.New("invalid JSON-RPC method")
	}
	if _, hasID := request["id"]; !hasID {
		return nil, true, nil
	}
	params, ok := request["params"].(map[string]any)
	if !ok {
		params = make(map[string]any)
	}
	meta, ok := params["_meta"].(map[string]any)
	if !ok {
		meta = make(map[string]any)
	}
	meta[mcp.CapabilityMeta] = token
	params["_meta"] = meta
	request["params"] = params
	return request, false, nil
}
