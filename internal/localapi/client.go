package localapi

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

const defaultTimeout = 2 * time.Second

type Client struct {
	socketPath string
	timeout    time.Duration
}

func NewClient(socketPath string) *Client {
	return &Client{socketPath: socketPath, timeout: defaultTimeout}
}

func (c *Client) Hello(ctx context.Context) (HelloResult, error) {
	connection, cancel, err := c.dial(ctx)
	if err != nil {
		return HelloResult{}, err
	}
	defer cancel()
	defer connection.Close()

	return negotiate(connection)
}

func (c *Client) Status(ctx context.Context) (StatusResult, error) {
	connection, cancel, err := c.dial(ctx)
	if err != nil {
		return StatusResult{}, err
	}
	defer cancel()
	defer connection.Close()

	hello, err := negotiate(connection)
	if err != nil {
		return StatusResult{}, err
	}

	var result StatusResult
	if err := roundTrip(connection, Request{
		ID:       requestID(),
		Protocol: hello.SelectedProtocol,
		Method:   MethodStatus,
	}, &result); err != nil {
		return StatusResult{}, err
	}
	return result, nil
}

func (c *Client) Stop(ctx context.Context) (StopResult, error) {
	connection, cancel, err := c.dial(ctx)
	if err != nil {
		return StopResult{}, err
	}
	defer cancel()
	defer connection.Close()

	hello, err := negotiate(connection)
	if err != nil {
		return StopResult{}, err
	}

	var result StopResult
	if err := roundTrip(connection, Request{
		ID:       requestID(),
		Protocol: hello.SelectedProtocol,
		Method:   MethodStop,
	}, &result); err != nil {
		return StopResult{}, err
	}
	return result, nil
}

func (c *Client) dial(ctx context.Context) (net.Conn, context.CancelFunc, error) {
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		connection, err := (&net.Dialer{}).DialContext(ctx, "unix", c.socketPath)
		if err != nil {
			cancel()
			return nil, func() {}, fmt.Errorf("connect to Crewfold daemon at %s: %w", c.socketPath, err)
		}
		if deadline, ok := ctx.Deadline(); ok {
			_ = connection.SetDeadline(deadline)
		}
		return connection, cancel, nil
	}

	connection, err := (&net.Dialer{}).DialContext(ctx, "unix", c.socketPath)
	if err != nil {
		return nil, func() {}, fmt.Errorf("connect to Crewfold daemon at %s: %w", c.socketPath, err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = connection.SetDeadline(deadline)
	}
	return connection, func() {}, nil
}

func negotiate(connection net.Conn) (HelloResult, error) {
	params, err := json.Marshal(HelloParams{MinProtocol: MinProtocol, MaxProtocol: MaxProtocol})
	if err != nil {
		return HelloResult{}, fmt.Errorf("marshal protocol negotiation: %w", err)
	}

	var result HelloResult
	if err := roundTrip(connection, Request{
		ID:     requestID(),
		Method: MethodHello,
		Params: params,
	}, &result); err != nil {
		return HelloResult{}, err
	}
	if result.SelectedProtocol < MinProtocol || result.SelectedProtocol > MaxProtocol {
		return HelloResult{}, fmt.Errorf("daemon selected unsupported protocol %d", result.SelectedProtocol)
	}
	return result, nil
}

func roundTrip(connection net.Conn, request Request, result any) error {
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return fmt.Errorf("write local API request %s: %w", request.ID, err)
	}

	reader := bufio.NewReader(connection)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read local API response %s: %w", request.ID, err)
	}

	var response Response
	if err := json.Unmarshal(line, &response); err != nil {
		return fmt.Errorf("decode local API response %s: %w", request.ID, err)
	}
	if response.ID != request.ID {
		return fmt.Errorf("local API response id %q does not match request %q", response.ID, request.ID)
	}
	if response.Error != nil {
		return response.Error
	}
	if len(response.Result) == 0 {
		return errors.New("local API response has neither result nor error")
	}
	if err := json.Unmarshal(response.Result, result); err != nil {
		return fmt.Errorf("decode local API result %s: %w", request.ID, err)
	}
	return nil
}

func requestID() string {
	var random [8]byte
	if _, err := rand.Read(random[:]); err != nil {
		return fmt.Sprintf("req-%d", time.Now().UnixNano())
	}
	return "req-" + hex.EncodeToString(random[:])
}
