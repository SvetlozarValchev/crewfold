package roomcli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"crewfold/internal/appdirs"
	"crewfold/internal/buildinfo"
	"crewfold/internal/room"
)

func TestConnectorSetupInstallsMachineServiceBeforeAnyRoomJoin(t *testing.T) {
	root := t.TempDir()
	fakeBin := filepath.Join(root, "fake-bin")
	if err := os.MkdirAll(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fakeBin, "systemctl"), []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	runtimeRoot, err := os.MkdirTemp("/tmp", "crewfold-connector-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeRoot) })
	t.Setenv("XDG_RUNTIME_DIR", runtimeRoot)
	t.Setenv("PATH", fakeBin)
	var stdout, stderr bytes.Buffer
	app := NewConnector(&stdout, &stderr, buildinfo.Info{Version: "test"})
	if code := app.RunConnector(context.Background(), []string{"setup"}); code != 0 {
		t.Fatalf("setup exit %d: %s", code, stderr.String())
	}
	paths, err := appdirs.Default()
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(paths.ConnectorBinaryPath); err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("installed connector = %#v, %v", info, err)
	}
	config, err := loadRemoteConfig(paths.ConnectorConfigPath)
	if err != nil || config == nil || len(config.Hubs) != 0 {
		t.Fatalf("empty connector configuration = %#v, %v", config, err)
	}
	unit, err := os.ReadFile(paths.ConnectorUnitPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(unit, []byte(paths.ConnectorBinaryPath)) || !bytes.Contains(unit, []byte("service run")) {
		t.Fatalf("connector unit = %s", unit)
	}
	updateUnit, err := os.ReadFile(filepath.Join(filepath.Dir(paths.ConnectorUnitPath), "crewfold-connector-update.service"))
	if err != nil || !bytes.Contains(updateUnit, []byte("update --automatic --config")) || !bytes.Contains(updateUnit, []byte(paths.ConnectorConfigPath)) {
		t.Fatalf("update unit = %s, %v", updateUnit, err)
	}
	timer, err := os.ReadFile(filepath.Join(filepath.Dir(paths.ConnectorUnitPath), connectorUpdateTimer))
	if err != nil || !bytes.Contains(timer, []byte("OnCalendar=hourly")) || !bytes.Contains(timer, []byte("RandomizedDelaySec=10min")) {
		t.Fatalf("update timer = %s, %v", timer, err)
	}
}

func TestRemoteConfigRoutesSameRoomSlugByAgentDirectoryAcrossHubs(t *testing.T) {
	t.Parallel()
	firstDirectory := filepath.Join(t.TempDir(), "first-agent")
	secondDirectory := filepath.Join(t.TempDir(), "second-agent")
	config := remoteConfig{Version: remoteConfigVersion, Hubs: []remoteHub{
		{
			URL: "https://first.example.ts.net", DeviceID: "device-first", Token: "token-first",
			Rooms:    []remoteRoom{{ID: "room-first", Slug: "shared"}},
			Bindings: []remoteBinding{{Room: "shared", RoomID: "room-first", ParticipantID: "participant-first", Handle: "first", Directory: firstDirectory, Provider: "none"}},
		},
		{
			URL: "https://second.example.ts.net", DeviceID: "device-second", Token: "token-second",
			Rooms:    []remoteRoom{{ID: "room-second", Slug: "shared"}},
			Bindings: []remoteBinding{{Room: "shared", RoomID: "room-second", ParticipantID: "participant-second", Handle: "second", Directory: secondDirectory, Provider: "none"}},
		},
	}}
	if err := validateRemoteConfig(config); err != nil {
		t.Fatal(err)
	}
	first, err := resolveRemoteRoute(config, "message.send", "shared", firstDirectory, room.SendInput{Room: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	if first.hub.URL != "https://first.example.ts.net" || first.binding == nil || first.binding.Handle != "first" {
		t.Fatalf("first route = %#v", first)
	}
	second, err := resolveRemoteRoute(config, "message.send", "shared", secondDirectory, room.SendInput{Room: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	if second.hub.URL != "https://second.example.ts.net" || second.binding == nil || second.binding.Handle != "second" {
		t.Fatalf("second route = %#v", second)
	}
}

func TestConnectorServiceRemainsIdleWithoutAnyJoinedRooms(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	configPath := filepath.Join(t.TempDir(), "missing.json")
	go func() {
		done <- runConnectorService(ctx, configPath, io.Discard)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("service returned %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("idle connector service did not stop")
	}
}

func TestRemoteConfigSerializesConcurrentAgentUpdates(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "connector.json")
	const agents = 8
	var wait sync.WaitGroup
	errorsFound := make(chan error, agents)
	for index := 0; index < agents; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			err := updateRemoteConfig(path, func(config *remoteConfig) error {
				config.Hubs = append(config.Hubs, remoteHub{
					URL:      fmt.Sprintf("https://hub-%d.example.ts.net", index),
					DeviceID: fmt.Sprintf("device-%d", index), Token: fmt.Sprintf("token-%d", index),
				})
				return nil
			})
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		if err != nil {
			t.Fatal(err)
		}
	}
	config, err := loadRemoteConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if config == nil || len(config.Hubs) != agents {
		t.Fatalf("config = %#v", config)
	}
}
