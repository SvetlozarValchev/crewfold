package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Caller is implemented by both the owner-local Unix client and the
// authenticated tailnet client.
type Caller interface {
	Call(context.Context, string, any, any) error
}

type NetworkClient struct {
	BaseURL       string
	Token         string
	ParticipantID string
	HTTP          *http.Client
}

func NormalizeNetworkURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Crewfold URL is invalid")
	}
	if parsed.Scheme != "https" {
		host := parsed.Hostname()
		if parsed.Scheme != "http" || (host != "127.0.0.1" && host != "localhost") {
			return "", errors.New("Crewfold URL must use HTTPS")
		}
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("Crewfold URL cannot contain a path")
	}
	parsed.Path = ""
	return strings.TrimSuffix(parsed.String(), "/"), nil
}

func (c NetworkClient) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 45 * time.Second}
}

func (c NetworkClient) Call(ctx context.Context, method string, params any, result any) error {
	baseURL, err := NormalizeNetworkURL(c.BaseURL)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	requestID, err := randomID("req_")
	if err != nil {
		return err
	}
	body, err := json.Marshal(Request{ID: requestID, Method: method, Params: raw})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/network/rpc", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(c.Token))
	if c.ParticipantID != "" {
		request.Header.Set("X-Crewfold-Participant", c.ParticipantID)
	}
	response, err := c.httpClient().Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		message := strings.TrimSpace(string(detail))
		if message == "" {
			message = response.Status
		}
		return errors.New(message)
	}
	var envelope struct {
		ID     string          `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8*1024*1024)).Decode(&envelope); err != nil {
		return err
	}
	if envelope.ID != requestID {
		return errors.New("Crewfold hub returned a mismatched response")
	}
	if envelope.Error != "" {
		return errors.New(envelope.Error)
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return fmt.Errorf("decode Crewfold hub response: %w", err)
	}
	return nil
}

func (c NetworkClient) Enroll(ctx context.Context, input NetworkEnrollInput) (NetworkEnrollment, error) {
	baseURL, err := NormalizeNetworkURL(c.BaseURL)
	if err != nil {
		return NetworkEnrollment{}, err
	}
	body, err := json.Marshal(input)
	if err != nil {
		return NetworkEnrollment{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/network/enroll", bytes.NewReader(body))
	if err != nil {
		return NetworkEnrollment{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient().Do(request)
	if err != nil {
		return NetworkEnrollment{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		message := strings.TrimSpace(string(detail))
		if message == "" {
			message = response.Status
		}
		return NetworkEnrollment{}, errors.New(message)
	}
	var enrollment NetworkEnrollment
	if err := json.NewDecoder(io.LimitReader(response.Body, 1024*1024)).Decode(&enrollment); err != nil {
		return NetworkEnrollment{}, err
	}
	return enrollment, nil
}
