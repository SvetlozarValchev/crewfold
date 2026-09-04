package room

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"time"

	"crewfold/internal/localipc"
)

type Client struct{ SocketPath string }

func (c Client) Call(ctx context.Context, method string, params any, result any) error {
	dialContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	connection, err := localipc.DialContext(dialContext, c.SocketPath)
	cancel()
	if err != nil {
		return err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(2 * time.Minute))
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	requestID, err := randomID("req_")
	if err != nil {
		return err
	}
	if err := json.NewEncoder(connection).Encode(Request{ID: requestID, Method: method, Params: raw}); err != nil {
		return err
	}
	var response struct {
		ID     string          `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.NewDecoder(bufio.NewReader(connection)).Decode(&response); err != nil {
		return err
	}
	if response.ID != requestID {
		return errors.New("daemon returned a mismatched response")
	}
	if response.Error != "" {
		return errors.New(response.Error)
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(response.Result, result)
}
