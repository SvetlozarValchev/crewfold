//go:build linux

package daemon

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestM24TwentyManagedServiceCyclesLeaveNoProcessRuntimeOrResourceLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("requires real process lifecycle boundaries")
	}
	running := startTestServer(t, testConfig(t))
	fixtureRoot := filepath.Join(t.TempDir(), "endurance-checkouts")
	createGitFixture(t, fixtureRoot)
	checkoutPath := filepath.Join(fixtureRoot, "world-engine")
	fixture := filepath.Join(checkoutPath, "endurance-service")
	pidPath := filepath.Join(checkoutPath, "service.pid")
	if err := os.WriteFile(fixture, []byte("#!/bin/sh\necho $$ > service.pid\necho generation-ready\nexec /bin/sleep 30\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	client := localapi.NewClient(running.config.SocketPath)
	workspace, err := client.WorkspaceInit(context.Background(), "m24-endurance", "m24-endurance-workspace")
	if err != nil {
		t.Fatal(err)
	}
	project, err := client.ProjectAdd(context.Background(), workspace.Workspace.ID, "m24-endurance-project", checkoutPath, domain.WriteModeShared, "m24-endurance-project")
	if err != nil {
		t.Fatal(err)
	}
	definition, err := client.ManagedServiceDefinitionCreate(context.Background(), localapi.ManagedServiceDefinitionCreateParams{
		Workspace: workspace.Workspace.ID, Project: project.Project.ID, Checkout: project.Checkout.ID,
		Name: "endurance-service", Description: "twenty-cycle resource fixture", Executable: fixture, Arguments: []string{}, WorkingDirectory: ".", Environment: []domain.ManagedServiceEnvironmentVariable{},
		Profile: "local-process", ProfileRevision: 1, NetworkMode: domain.ManagedServiceNetworkNone,
		Health:        domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthProcess, IntervalMillis: 100, TimeoutMillis: 50},
		RestartPolicy: domain.ManagedServiceRestartNever, StopSignal: domain.ManagedServiceStopSignalTerm, StopGraceMillis: 100,
		OutputByteLimit: 4096, CapacityClass: domain.ManagedServiceCapacityLocalDevelop, IdempotencyKey: "endurance-definition",
	})
	if err != nil {
		t.Fatal(err)
	}
	cycle := func(index int) {
		t.Helper()
		_ = os.Remove(pidPath)
		started, startErr := client.ManagedServiceStart(context.Background(), localapi.ManagedServiceStartParams{
			Workspace: workspace.Workspace.ID, Definition: definition.Definition.ID, ExpectedRevision: definition.Definition.Revision,
			IdempotencyKey: "endurance-start-" + time.Unix(int64(index+1), 0).UTC().Format("150405"),
		})
		if startErr != nil {
			t.Fatalf("cycle %d start: %v", index, startErr)
		}
		healthy := waitManagedServicePublicState(t, client, workspace.Workspace.ID, started.Instance.ID, domain.ManagedServiceHealthy)
		pidBytes, readErr := os.ReadFile(pidPath)
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
		if readErr != nil || parseErr != nil || pid <= 1 {
			t.Fatalf("cycle %d public fixture PID = %q, %v, %v", index, pidBytes, readErr, parseErr)
		}
		if _, stopErr := client.ManagedServiceStop(context.Background(), localapi.ManagedServiceActionParams{
			Workspace: workspace.Workspace.ID, Instance: healthy.ID, ExpectedRevision: healthy.Revision,
			IdempotencyKey: "endurance-stop-" + time.Unix(int64(index+1), 0).UTC().Format("150405"),
		}); stopErr != nil {
			t.Fatalf("cycle %d stop: %v", index, stopErr)
		}
		waitManagedServicePublicState(t, client, workspace.Workspace.ID, healthy.ID, domain.ManagedServiceStopped)
		if err := syscall.Kill(pid, 0); err == nil {
			t.Fatalf("cycle %d process %d remains", index, pid)
		}
		logs, logsErr := client.ManagedServiceLogs(context.Background(), workspace.Workspace.ID, healthy.ID)
		if logsErr != nil || logs.Logs.State != "terminal" || !strings.Contains(logs.Logs.Stdout.Text, "generation-ready") {
			t.Fatalf("cycle %d terminal logs = %#v, %v", index, logs, logsErr)
		}
	}

	// Prime lazy Go, SQLite, HTTP-client, and race-detector resources before the
	// frozen relative baseline, then retain the exact 20 measured cycles.
	cycle(-1)
	runtime.GC()
	_, baselineFDs, resourceErr := daemonProcessResources()
	if resourceErr != nil {
		t.Fatal(resourceErr)
	}
	baselineGoroutines := runtime.NumGoroutine()
	for index := 0; index < 20; index++ {
		cycle(index)
	}
	runtime.GC()
	deadline := time.Now().Add(2 * time.Second)
	var finalFDs int64
	var runtimeEntries []os.DirEntry
	for {
		_, finalFDs, resourceErr = daemonProcessResources()
		if resourceErr != nil {
			t.Fatal(resourceErr)
		}
		runtimeEntries, err = os.ReadDir(filepath.Join(running.config.DataDir, "service-runtime"))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		if finalFDs <= baselineFDs+3 && runtime.NumGoroutine() <= baselineGoroutines+5 && len(runtimeEntries) == 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if finalFDs > baselineFDs+3 || runtime.NumGoroutine() > baselineGoroutines+5 {
		t.Fatalf("managed-service resource drift: fds %d -> %d, goroutines %d -> %d", baselineFDs, finalFDs, baselineGoroutines, runtime.NumGoroutine())
	}
	if len(runtimeEntries) != 0 {
		t.Fatalf("terminal service-runtime entries remain: %#v", runtimeEntries)
	}
}

func waitManagedServicePublicState(t *testing.T, client *localapi.Client, workspaceID, instanceID, status string) domain.ManagedServiceInstance {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		result, err := client.ManagedServiceShow(context.Background(), workspaceID, instanceID)
		if err == nil && result.Detail.Instance.Status == status {
			return result.Detail.Instance
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("managed service %s did not reach %s", instanceID, status)
	return domain.ManagedServiceInstance{}
}
