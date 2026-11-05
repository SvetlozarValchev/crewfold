package room

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crewfold/internal/buildinfo"
)

func TestNetworkHTTPWorkflowUsesRoomScopedDeviceIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dataDir := filepath.Join(root, "state")
	socket := filepath.Join(root, "runtime", "crewfold.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- RunServer(ctx, ServerConfig{DataDir: dataDir, SocketPath: socket, WebAddress: "127.0.0.1:0", Version: buildinfo.Info{Version: "test"}})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("server shutdown: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("server did not stop")
		}
	})
	waitForTestPath(t, socket)
	waitForTestPath(t, filepath.Join(dataDir, "web-address"))
	addressBytes, err := os.ReadFile(filepath.Join(dataDir, "web-address"))
	if err != nil {
		t.Fatal(err)
	}
	baseURL := "http://" + strings.TrimSpace(string(addressBytes))
	local := Client{SocketPath: socket}
	var created Snapshot
	if err := local.Call(ctx, "room.create", CreateRoomInput{Slug: "tailnet-test", Title: "Tailnet test"}, &created); err != nil {
		t.Fatal(err)
	}
	var settings NetworkSettings
	if err := local.Call(ctx, "network.configure", ConfigureNetworkInput{Enabled: true, PublicOrigin: "https://crewfold.example.ts.net"}, &settings); err != nil {
		t.Fatal(err)
	}
	var invite NetworkInvite
	if err := local.Call(ctx, "network.invite.create", CreateNetworkInviteInput{Room: created.Room.ID}, &invite); err != nil {
		t.Fatal(err)
	}
	network := NetworkClient{BaseURL: baseURL}
	enrollment, err := network.Enroll(ctx, NetworkEnrollInput{Invite: invite.Secret, DeviceName: "remote-test"})
	if err != nil {
		t.Fatal(err)
	}
	network.Token = enrollment.Token
	var webBootstrap NetworkBrowserBootstrap
	if err := network.Call(ctx, "browser.bootstrap", map[string]any{}, &webBootstrap); err != nil {
		t.Fatal(err)
	}
	if webBootstrap.Rooms != 1 {
		t.Fatalf("browser bootstrap rooms = %d", webBootstrap.Rooms)
	}
	bootstrapURL, err := url.Parse(webBootstrap.URL)
	if err != nil {
		t.Fatal(err)
	}
	bootstrap := strings.TrimPrefix(bootstrapURL.Fragment, "network-bootstrap=")
	if bootstrap == "" {
		t.Fatalf("browser bootstrap URL = %q", webBootstrap.URL)
	}
	browserToken := redeemNetworkWebSession(t, baseURL, bootstrapURL.Host, "https://"+bootstrapURL.Host, bootstrap)
	if status := redeemNetworkWebSessionStatus(t, baseURL, bootstrapURL.Host, "https://"+bootstrapURL.Host, bootstrap); status != http.StatusUnauthorized {
		t.Fatalf("reused browser bootstrap status = %d", status)
	}
	var browserRooms []Room
	networkWebCall(t, baseURL, bootstrapURL.Host, "https://"+bootstrapURL.Host, browserToken, "room.list", map[string]any{}, &browserRooms)
	if len(browserRooms) != 1 || browserRooms[0].ID != created.Room.ID {
		t.Fatalf("browser rooms = %#v", browserRooms)
	}
	var identity NetworkBrowserIdentity
	networkWebCall(t, baseURL, bootstrapURL.Host, "https://"+bootstrapURL.Host, browserToken, "browser.identity", map[string]any{}, &identity)
	if identity.Handle != "" || identity.SuggestedHandle == "" {
		t.Fatalf("initial browser identity = %#v", identity)
	}
	networkWebCall(t, baseURL, bootstrapURL.Host, "https://"+bootstrapURL.Host, browserToken, "browser.identity.set", map[string]any{"handle": "remote-human"}, &identity)
	if identity.Handle != "remote-human" {
		t.Fatalf("configured browser identity = %#v", identity)
	}
	var memberMessage Message
	networkWebCall(t, baseURL, bootstrapURL.Host, "https://"+bootstrapURL.Host, browserToken, "message.send", SendInput{Room: created.Room.ID, Body: "Hello from the collaborator browser."}, &memberMessage)
	if memberMessage.SenderKind != "member" || memberMessage.SenderHandle != "remote-human" || memberMessage.ParticipantID != "" {
		t.Fatalf("browser member message = %#v", memberMessage)
	}
	var ownerOnly any
	if err := networkWebCallError(baseURL, bootstrapURL.Host, "https://"+bootstrapURL.Host, browserToken, "room.create", CreateRoomInput{Slug: "forbidden", Title: "Forbidden"}, &ownerOnly); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("browser owner operation = %v", err)
	}
	var participant Participant
	if err := network.Call(ctx, "participant.join", JoinInput{Room: created.Room.Slug, Handle: "remote-agent", WorkingDirectory: "/remote/project", Delivery: "codex"}, &participant); err != nil {
		t.Fatal(err)
	}
	if participant.HumanHandle != "remote-human" {
		t.Fatalf("remote participant human = %#v", participant)
	}
	network.ParticipantID = participant.ID
	var ownerMessage Message
	if err := local.Call(ctx, "message.send", SendInput{Room: created.Room.ID, Owner: true, Body: "@remote-agent inspect the network path."}, &ownerMessage); err != nil {
		t.Fatal(err)
	}
	var batch NetworkDeliveryBatch
	if err := network.Call(ctx, "delivery.poll", NetworkDeliveryPollInput{ParticipantID: participant.ID}, &batch); err != nil {
		t.Fatal(err)
	}
	if len(batch.Messages) != 1 || batch.Messages[0].ID != ownerMessage.ID {
		t.Fatalf("network batch = %#v", batch)
	}
	var binding NetworkBinding
	if err := network.Call(ctx, "delivery.report", NetworkDeliveryResultInput{ParticipantID: participant.ID, Through: batch.Through, Status: "delivered"}, &binding); err != nil {
		t.Fatal(err)
	}
	var reply Message
	if err := network.Call(ctx, "message.send", SendInput{Room: created.Room.ID, Body: "The remote path works."}, &reply); err != nil {
		t.Fatal(err)
	}
	if reply.SenderHandle != "remote-agent" {
		t.Fatalf("reply sender = %#v", reply)
	}
	var left Participant
	if err := network.Call(ctx, "participant.leave", LeaveInput{Room: created.Room.ID}, &left); err != nil {
		t.Fatal(err)
	}
	if err := network.Call(ctx, "message.send", SendInput{Room: created.Room.ID, Body: "This must not be published."}, &reply); err == nil || !strings.Contains(err.Error(), "has left") {
		t.Fatalf("left participant could still publish: %v", err)
	}
	var forbidden any
	if err := network.Call(ctx, "room.create", CreateRoomInput{Slug: "forbidden", Title: "Forbidden"}, &forbidden); err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("remote owner operation = %v", err)
	}
	unauthorized := NetworkClient{BaseURL: baseURL, Token: strings.Repeat("0", 64)}
	if err := unauthorized.Call(ctx, "room.list", map[string]any{}, &forbidden); err == nil || !strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("invalid credential = %v", err)
	}
}

func redeemNetworkWebSession(t *testing.T, baseURL, host, origin, bootstrap string) string {
	t.Helper()
	body, err := json.Marshal(map[string]string{"bootstrap": bootstrap})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, baseURL+"/api/network/session", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Host = host
	request.Header.Set("Origin", origin)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(response.Body)
		t.Fatalf("redeem browser session: %s: %s", response.Status, strings.TrimSpace(string(detail)))
	}
	var session struct {
		Token string `json:"token"`
		Mode  string `json:"mode"`
	}
	if err := json.NewDecoder(response.Body).Decode(&session); err != nil {
		t.Fatal(err)
	}
	if session.Token == "" || session.Mode != "collaborator" {
		t.Fatalf("browser session = %#v", session)
	}
	return session.Token
}

func redeemNetworkWebSessionStatus(t *testing.T, baseURL, host, origin, bootstrap string) int {
	t.Helper()
	body, err := json.Marshal(map[string]string{"bootstrap": bootstrap})
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, baseURL+"/api/network/session", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Host = host
	request.Header.Set("Origin", origin)
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	return response.StatusCode
}

func networkWebCall(t *testing.T, baseURL, host, origin, token, method string, params, result any) {
	t.Helper()
	if err := networkWebCallError(baseURL, host, origin, token, method, params, result); err != nil {
		t.Fatal(err)
	}
}

func networkWebCallError(baseURL, host, origin, token, method string, params, result any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	body, err := json.Marshal(Request{ID: "test-request", Method: method, Params: raw})
	if err != nil {
		return err
	}
	request, err := http.NewRequest(http.MethodPost, baseURL+"/api/network/web/rpc", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Host = host
	request.Header.Set("Origin", origin)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return errors.New(strings.TrimSpace(string(detail)))
	}
	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  string          `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return err
	}
	if envelope.Error != "" {
		return errors.New(envelope.Error)
	}
	if result == nil {
		return nil
	}
	return json.Unmarshal(envelope.Result, result)
}

func waitForTestPath(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("path was not created: %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
