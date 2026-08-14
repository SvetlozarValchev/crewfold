package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"crewfold/internal/buildinfo"
	"crewfold/internal/domain"
	"crewfold/internal/gitstate"
	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func TestServerStatusStopAndSocketPermissions(t *testing.T) {
	t.Parallel()

	running := startTestServer(t, testConfig(t))
	client := localapi.NewClient(running.config.SocketPath)

	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if status.Status != "ok" {
		t.Fatalf("status.Status = %q, want ok", status.Status)
	}
	if status.Protocol != localapi.MaxProtocol {
		t.Fatalf("status.Protocol = %d, want %d", status.Protocol, localapi.MaxProtocol)
	}
	if status.PID != os.Getpid() {
		t.Fatalf("status.PID = %d, want %d", status.PID, os.Getpid())
	}
	if err := status.ServerVersion.Validate(); err != nil {
		t.Fatalf("status.ServerVersion.Validate() error = %v", err)
	}

	info, err := os.Stat(running.config.SocketPath)
	if err != nil {
		t.Fatalf("os.Stat(socket) error = %v", err)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("socket permissions = %04o, want 0600", permissions)
	}
	dataInfo, err := os.Stat(running.config.DataDir)
	if err != nil {
		t.Fatalf("os.Stat(data dir) error = %v", err)
	}
	if permissions := dataInfo.Mode().Perm(); permissions != 0o700 {
		t.Fatalf("new data-directory permissions = %04o, want 0700", permissions)
	}
	lockInfo, err := os.Stat(filepath.Join(running.config.DataDir, "daemon.lock"))
	if err != nil {
		t.Fatalf("os.Stat(lock) error = %v", err)
	}
	if permissions := lockInfo.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("lock permissions = %04o, want 0600", permissions)
	}

	stopResult, err := client.Stop(context.Background())
	if err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if stopResult.Status != "stopping" {
		t.Fatalf("stopResult.Status = %q, want stopping", stopResult.Status)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Lstat(running.config.SocketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket after shutdown error = %v, want not exist", err)
	}
}

func TestSecondDaemonCannotClaimLiveDataDirOrSocket(t *testing.T) {
	t.Parallel()

	first := startTestServer(t, testConfig(t))

	sameData := first.config
	sameData.SocketPath = filepath.Join(t.TempDir(), "other.sock")
	err := Run(context.Background(), sameData)
	if ErrorCode(err) != CodeDataDirInUse {
		t.Fatalf("same data directory error = %v, code = %q, want %q", err, ErrorCode(err), CodeDataDirInUse)
	}

	sameSocket := first.config
	sameSocket.DataDir = filepath.Join(t.TempDir(), "other-data")
	err = Run(context.Background(), sameSocket)
	if ErrorCode(err) != CodeSocketInUse {
		t.Fatalf("same socket error = %v, code = %q, want %q", err, ErrorCode(err), CodeSocketInUse)
	}

	if _, err := localapi.NewClient(first.config.SocketPath).Stop(context.Background()); err != nil {
		t.Fatalf("Stop(first) error = %v", err)
	}
	if err := first.wait(); err != nil {
		t.Fatalf("Run(first) error = %v", err)
	}
}

func TestStaleSocketIsRecovered(t *testing.T) {
	t.Parallel()

	config := testConfig(t)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: config.SocketPath, Net: "unix"})
	if err != nil {
		t.Fatalf("net.ListenUnix() error = %v", err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatalf("listener.Close() error = %v", err)
	}
	if _, err := os.Lstat(config.SocketPath); err != nil {
		t.Fatalf("stale socket should exist: %v", err)
	}

	running := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	if _, err := client.Status(context.Background()); err != nil {
		t.Fatalf("Status() after stale socket recovery error = %v", err)
	}
	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestNonSocketPathIsRefusedAndPreserved(t *testing.T) {
	t.Parallel()

	config := testConfig(t)
	const contents = "belongs to the user\n"
	if err := os.WriteFile(config.SocketPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("os.WriteFile(socket path) error = %v", err)
	}

	err := Run(context.Background(), config)
	if ErrorCode(err) != CodeSocketPathOccupied {
		t.Fatalf("Run() error = %v, code = %q, want %q", err, ErrorCode(err), CodeSocketPathOccupied)
	}
	data, readErr := os.ReadFile(config.SocketPath)
	if readErr != nil {
		t.Fatalf("os.ReadFile(socket path) error = %v", readErr)
	}
	if string(data) != contents {
		t.Fatalf("socket-path file = %q, want preserved %q", data, contents)
	}
}

func TestDataDirectoryLockSymlinkIsRefusedAndTargetPreserved(t *testing.T) {
	t.Parallel()

	config := testConfig(t)
	if err := os.MkdirAll(config.DataDir, 0o700); err != nil {
		t.Fatalf("os.MkdirAll(data dir) error = %v", err)
	}
	targetPath := filepath.Join(t.TempDir(), "user-file")
	const contents = "must remain unchanged\n"
	if err := os.WriteFile(targetPath, []byte(contents), 0o600); err != nil {
		t.Fatalf("os.WriteFile(target) error = %v", err)
	}
	if err := os.Symlink(targetPath, filepath.Join(config.DataDir, "daemon.lock")); err != nil {
		t.Fatalf("os.Symlink(lock) error = %v", err)
	}

	err := Run(context.Background(), config)
	if ErrorCode(err) != CodeInvalidConfiguration {
		t.Fatalf("Run() error = %v, code = %q, want %q", err, ErrorCode(err), CodeInvalidConfiguration)
	}
	data, readErr := os.ReadFile(targetPath)
	if readErr != nil {
		t.Fatalf("os.ReadFile(target) error = %v", readErr)
	}
	if string(data) != contents {
		t.Fatalf("target contents = %q, want preserved %q", data, contents)
	}
}

func TestShutdownClosesInflightPartialRequest(t *testing.T) {
	t.Parallel()

	running := startTestServer(t, testConfig(t))
	partial, err := net.Dial("unix", running.config.SocketPath)
	if err != nil {
		t.Fatalf("net.Dial(partial) error = %v", err)
	}
	defer partial.Close()
	if _, err := partial.Write([]byte(`{"id":"partial"`)); err != nil {
		t.Fatalf("partial.Write() error = %v", err)
	}

	if _, err := localapi.NewClient(running.config.SocketPath).Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if err := partial.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatalf("partial.SetReadDeadline() error = %v", err)
	}
	buffer := make([]byte, 1)
	if _, err := partial.Read(buffer); err == nil {
		t.Fatal("partial.Read() error = nil, want connection closed during shutdown")
	}
}

func TestHelloRejectsUnsupportedProtocolRange(t *testing.T) {
	t.Parallel()

	running := startTestServer(t, testConfig(t))
	connection, err := net.Dial("unix", running.config.SocketPath)
	if err != nil {
		t.Fatalf("net.Dial() error = %v", err)
	}
	defer connection.Close()

	params, err := json.Marshal(localapi.HelloParams{MinProtocol: 2, MaxProtocol: 3})
	if err != nil {
		t.Fatalf("json.Marshal(params) error = %v", err)
	}
	request := localapi.Request{ID: "unsupported", Method: localapi.MethodHello, Params: params}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		t.Fatalf("Encode(request) error = %v", err)
	}
	line, err := bufio.NewReader(connection).ReadBytes('\n')
	if err != nil {
		t.Fatalf("ReadBytes(response) error = %v", err)
	}
	var response localapi.Response
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v", err)
	}
	if response.Error == nil || response.Error.Code != "protocol_mismatch" {
		t.Fatalf("response.Error = %#v, want protocol_mismatch", response.Error)
	}

	if _, err := localapi.NewClient(running.config.SocketPath).Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestRequestLogsCarryCorrelationFields(t *testing.T) {
	t.Parallel()

	var logBuffer lockedBuffer
	config := testConfig(t)
	config.Logger = slog.New(slog.NewJSONHandler(&logBuffer, nil))
	running := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	if _, err := client.Status(context.Background()); err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	logs := logBuffer.String()
	for _, expected := range []string{
		`"msg":"daemon started"`,
		`"msg":"request completed"`,
		`"request_id":"req-`,
		`"method":"system.status"`,
		`"msg":"daemon stopped"`,
	} {
		if !strings.Contains(logs, expected) {
			t.Errorf("logs do not contain %q:\n%s", expected, logs)
		}
	}
}

func TestWorkspaceAndEventsPersistAcrossDaemonRestart(t *testing.T) {
	t.Parallel()

	config := testConfig(t)
	first := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)

	databaseStatus, err := client.DatabaseStatus(context.Background())
	if err != nil {
		t.Fatalf("DatabaseStatus() error = %v", err)
	}
	if databaseStatus.Status != "ok" || databaseStatus.SchemaVersion != store.LatestSchemaVersion || databaseStatus.JournalMode != "wal" || !databaseStatus.ForeignKeys {
		t.Fatalf("DatabaseStatus() = %#v, want healthy current WAL database", databaseStatus)
	}

	created, err := client.WorkspaceInit(context.Background(), "personal", "init-personal")
	if err != nil {
		t.Fatalf("WorkspaceInit() error = %v", err)
	}
	replayed, err := client.WorkspaceInit(context.Background(), "personal", "init-personal")
	if err != nil {
		t.Fatalf("WorkspaceInit(replay) error = %v", err)
	}
	if !reflect.DeepEqual(created, replayed) {
		t.Fatalf("replayed result changed:\ncreated = %#v\nreplayed = %#v", created, replayed)
	}
	if _, err := client.WorkspaceInit(context.Background(), "personal", "different-key"); localAPIErrorCode(err) != store.CodeWorkspaceExists {
		t.Fatalf("duplicate workspace error = %v, code = %q", err, localAPIErrorCode(err))
	}

	shown, err := client.WorkspaceShow(context.Background(), "personal")
	if err != nil {
		t.Fatalf("WorkspaceShow() error = %v", err)
	}
	if !reflect.DeepEqual(shown.Workspace, created.Workspace) {
		t.Fatalf("WorkspaceShow() = %#v, want %#v", shown.Workspace, created.Workspace)
	}
	events, err := client.EventsList(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("EventsList() error = %v", err)
	}
	if len(events.Events) != 1 || events.Events[0].EventID != created.EventID || events.NextAfter != created.EventSequence || events.HasMore {
		t.Fatalf("EventsList() = %#v, want one creation event", events)
	}

	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(first) error = %v", err)
	}
	if err := first.wait(); err != nil {
		t.Fatalf("Run(first) error = %v", err)
	}

	second := startTestServer(t, config)
	restartedClient := localapi.NewClient(config.SocketPath)
	restored, err := restartedClient.WorkspaceShow(context.Background(), created.Workspace.ID)
	if err != nil {
		t.Fatalf("WorkspaceShow(after restart) error = %v", err)
	}
	if !reflect.DeepEqual(restored.Workspace, created.Workspace) {
		t.Fatalf("restored workspace = %#v, want %#v", restored.Workspace, created.Workspace)
	}
	restoredEvents, err := restartedClient.EventsList(context.Background(), 0, 100)
	if err != nil || len(restoredEvents.Events) != 1 {
		t.Fatalf("EventsList(after restart) = %#v, %v, want one event", restoredEvents, err)
	}
	if _, err := restartedClient.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(second) error = %v", err)
	}
	if err := second.wait(); err != nil {
		t.Fatalf("Run(second) error = %v", err)
	}
}

func TestProjectsObserveAdjacentClonesLinkedWorktreesDirtyAndMissingPathsAcrossRestart(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}

	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	config := testConfig(t)
	first := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	if _, err := client.WorkspaceInit(context.Background(), "personal", "init-personal"); err != nil {
		t.Fatalf("WorkspaceInit() error = %v", err)
	}
	project, err := client.ProjectAdd(context.Background(), "personal", "world-engine", filepath.Join(fixtureRoot, "world-engine"), domain.WriteModeExclusive, "project-world-engine")
	if err != nil {
		t.Fatalf("ProjectAdd() error = %v", err)
	}
	adjacent, err := client.CheckoutAdd(context.Background(), "personal", "world-engine", filepath.Join(fixtureRoot, "world-engine-2"), domain.WriteModeClaimed, "checkout-adjacent")
	if err != nil {
		t.Fatalf("CheckoutAdd(adjacent clone) error = %v", err)
	}
	linked, err := client.CheckoutAdd(context.Background(), "personal", "world-engine", filepath.Join(fixtureRoot, "world-engine-linked"), domain.WriteModeExclusive, "checkout-linked")
	if err != nil {
		t.Fatalf("CheckoutAdd(linked worktree) error = %v", err)
	}
	missingLater, err := client.CheckoutAdd(context.Background(), "personal", "world-engine", filepath.Join(fixtureRoot, "world-engine-5"), domain.WriteModeExclusive, "checkout-missing-later")
	if err != nil {
		t.Fatalf("CheckoutAdd(missing later) error = %v", err)
	}
	if adjacent.Repository.ID != project.Repository.ID || linked.Repository.ID != project.Repository.ID || missingLater.Repository.ID != project.Repository.ID {
		t.Fatalf("repository IDs differ: project=%s adjacent=%s linked=%s third=%s", project.Repository.ID, adjacent.Repository.ID, linked.Repository.ID, missingLater.Repository.ID)
	}
	if adjacent.Checkout.ID == project.Checkout.ID || adjacent.Checkout.CheckoutKind != domain.CheckoutStandalone {
		t.Fatalf("adjacent checkout = %#v, want distinct standalone checkout", adjacent.Checkout)
	}
	if linked.Checkout.CheckoutKind != domain.CheckoutLinkedWorktree {
		t.Fatalf("linked checkout kind = %q, want linked_worktree", linked.Checkout.CheckoutKind)
	}

	dirtyFile := filepath.Join(fixtureRoot, "world-engine-2", "untracked.txt")
	if err := os.WriteFile(dirtyFile, []byte("dirty\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile(dirty fixture file) error = %v", err)
	}
	movedPath := filepath.Join(fixtureRoot, "world-engine-5-moved")
	if err := os.Rename(filepath.Join(fixtureRoot, "world-engine-5"), movedPath); err != nil {
		t.Fatalf("os.Rename(fixture checkout) error = %v", err)
	}
	inspection, err := client.ProjectInspect(context.Background(), "personal", project.Project.ID)
	if err != nil {
		t.Fatalf("ProjectInspect() error = %v", err)
	}
	if len(inspection.Repositories) != 1 || len(inspection.Checkouts) != 4 {
		t.Fatalf("inspection has %d repositories and %d checkouts, want 1 and 4", len(inspection.Repositories), len(inspection.Checkouts))
	}
	byID := make(map[string]domain.Checkout)
	for _, checkout := range inspection.Checkouts {
		byID[checkout.ID] = checkout
	}
	if !byID[adjacent.Checkout.ID].Dirty {
		t.Fatalf("adjacent checkout = %#v, want dirty", byID[adjacent.Checkout.ID])
	}
	unavailable := byID[missingLater.Checkout.ID]
	if unavailable.Availability != domain.CheckoutUnavailable || unavailable.DiagnosticCode != gitstate.CodeCheckoutUnavailable || unavailable.ID != missingLater.Checkout.ID {
		t.Fatalf("moved checkout = %#v, want same durable ID marked unavailable", unavailable)
	}

	listed, err := client.CheckoutList(context.Background(), "personal", "world-engine")
	if err != nil || len(listed.Checkouts) != 4 {
		t.Fatalf("CheckoutList() = %#v, %v, want four stored checkouts", listed, err)
	}
	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(first) error = %v", err)
	}
	if err := first.wait(); err != nil {
		t.Fatalf("Run(first) error = %v", err)
	}

	second := startTestServer(t, config)
	restored, err := localapi.NewClient(config.SocketPath).CheckoutList(context.Background(), "personal", project.Project.ID)
	if err != nil || len(restored.Checkouts) != 4 || restored.Checkouts[0].ID == "" {
		t.Fatalf("CheckoutList(after restart) = %#v, %v", restored, err)
	}
	if _, err := localapi.NewClient(config.SocketPath).Stop(context.Background()); err != nil {
		t.Fatalf("Stop(second) error = %v", err)
	}
	if err := second.wait(); err != nil {
		t.Fatalf("Run(second) error = %v", err)
	}
}

func TestProjectRegistrationGitFailuresCreateNoPartialRecords(t *testing.T) {
	t.Parallel()

	for name, inspectErr := range map[string]error{
		"Git unavailable":  &gitstate.Error{Code: gitstate.CodeGitUnavailable, Operation: "execute Git"},
		"malformed output": &gitstate.Error{Code: gitstate.CodeGitOutputInvalid, Operation: "read repository roots"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			config := testConfig(t)
			config.GitInspector = failingInspector{err: inspectErr}
			running := startTestServer(t, config)
			client := localapi.NewClient(config.SocketPath)
			if _, err := client.WorkspaceInit(context.Background(), "personal", "init-personal"); err != nil {
				t.Fatalf("WorkspaceInit() error = %v", err)
			}
			_, err := client.ProjectAdd(context.Background(), "personal", "world-engine", "/does/not/matter", domain.WriteModeExclusive, "project-world-engine")
			if localAPIErrorCode(err) != gitstate.ErrorCode(inspectErr) {
				t.Fatalf("ProjectAdd() error = %v, code = %q", err, localAPIErrorCode(err))
			}
			if _, err := client.CheckoutList(context.Background(), "personal", "world-engine"); localAPIErrorCode(err) != store.CodeProjectNotFound {
				t.Fatalf("CheckoutList() error = %v, want project_not_found", err)
			}
			events, err := client.EventsList(context.Background(), 0, 100)
			if err != nil || len(events.Events) != 1 {
				t.Fatalf("EventsList() = %#v, %v, want only workspace event", events, err)
			}
			if _, err := client.Stop(context.Background()); err != nil {
				t.Fatalf("Stop() error = %v", err)
			}
			if err := running.wait(); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
		})
	}
}

func TestNonRepositoryRegistrationCreatesNoPartialRecords(t *testing.T) {
	t.Parallel()

	running := startTestServer(t, testConfig(t))
	client := localapi.NewClient(running.config.SocketPath)
	if _, err := client.WorkspaceInit(context.Background(), "personal", "init-personal"); err != nil {
		t.Fatalf("WorkspaceInit() error = %v", err)
	}
	_, err := client.ProjectAdd(context.Background(), "personal", "not-a-repo", t.TempDir(), domain.WriteModeExclusive, "project-not-repo")
	if localAPIErrorCode(err) != gitstate.CodeNotGitRepository {
		t.Fatalf("ProjectAdd(non-repository) error = %v, code = %q", err, localAPIErrorCode(err))
	}
	if _, err := client.CheckoutList(context.Background(), "personal", "not-a-repo"); localAPIErrorCode(err) != store.CodeProjectNotFound {
		t.Fatalf("CheckoutList() error = %v, want project_not_found", err)
	}
	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestEventPaginationUsesResumableExclusiveCursor(t *testing.T) {
	t.Parallel()

	running := startTestServer(t, testConfig(t))
	client := localapi.NewClient(running.config.SocketPath)
	for _, name := range []string{"alpha", "beta"} {
		if _, err := client.WorkspaceInit(context.Background(), name, "init-"+name); err != nil {
			t.Fatalf("WorkspaceInit(%s) error = %v", name, err)
		}
	}
	first, err := client.EventsList(context.Background(), 0, 1)
	if err != nil {
		t.Fatalf("EventsList(first) error = %v", err)
	}
	if len(first.Events) != 1 || first.NextAfter != 1 || !first.HasMore {
		t.Fatalf("first page = %#v, want sequence 1 and has_more", first)
	}
	second, err := client.EventsList(context.Background(), first.NextAfter, 1)
	if err != nil {
		t.Fatalf("EventsList(second) error = %v", err)
	}
	if len(second.Events) != 1 || second.Events[0].Sequence != 2 || second.NextAfter != 2 || second.HasMore {
		t.Fatalf("second page = %#v, want final sequence 2", second)
	}
	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if err := running.wait(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestWorkspaceMutationCrashIsAtomic(t *testing.T) {
	if os.Getenv("CREWFOLD_WORKSPACE_CRASH_HELPER") != "" {
		t.Fatal("parent crash test unexpectedly running as helper")
	}

	for _, stage := range []string{store.MutationAfterProjection, store.MutationAfterEvent} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			dataDir := filepath.Join(root, "data")
			socketPath := filepath.Join(root, "crewfold.sock")
			markerPath := filepath.Join(root, "mutation-reached")
			command := exec.Command(os.Args[0], "-test.run=^TestWorkspaceCrashHelperProcess$")
			command.Env = append(os.Environ(),
				"CREWFOLD_WORKSPACE_CRASH_HELPER=1",
				"CREWFOLD_WORKSPACE_CRASH_DATA_DIR="+dataDir,
				"CREWFOLD_WORKSPACE_CRASH_SOCKET="+socketPath,
				"CREWFOLD_WORKSPACE_CRASH_MARKER="+markerPath,
				"CREWFOLD_WORKSPACE_CRASH_STAGE="+stage,
			)
			var childOutput lockedBuffer
			command.Stdout = &childOutput
			command.Stderr = &childOutput
			if err := command.Start(); err != nil {
				t.Fatalf("start crash helper: %v", err)
			}
			childReaped := false
			t.Cleanup(func() {
				if !childReaped && command.Process != nil {
					_ = command.Process.Kill()
					_ = command.Wait()
				}
			})

			client := localapi.NewClient(socketPath)
			waitForCondition(t, 5*time.Second, func() bool {
				_, err := client.Status(context.Background())
				return err == nil
			}, "crash helper readiness; output: "+childOutput.String())

			requestDone := make(chan error, 1)
			go func() {
				_, err := client.WorkspaceInit(context.Background(), "personal", "crash-key")
				requestDone <- err
			}()
			waitForCondition(t, 5*time.Second, func() bool {
				_, err := os.Stat(markerPath)
				return err == nil
			}, "mutation barrier "+stage+"; output: "+childOutput.String())

			if err := command.Process.Kill(); err != nil {
				t.Fatalf("kill crash helper: %v", err)
			}
			if err := command.Wait(); err == nil {
				t.Fatal("crash helper exited cleanly, want forced process death")
			}
			childReaped = true
			select {
			case <-requestDone:
			case <-time.After(3 * time.Second):
				t.Fatal("workspace request did not unblock after daemon process death")
			}

			config := Config{
				DataDir:    dataDir,
				SocketPath: socketPath,
				Version:    buildinfo.Current(),
				Logger:     slog.New(slog.NewJSONHandler(io.Discard, nil)),
			}
			restarted := startTestServer(t, config)
			restartedClient := localapi.NewClient(socketPath)
			if _, err := restartedClient.WorkspaceShow(context.Background(), "personal"); localAPIErrorCode(err) != store.CodeWorkspaceNotFound {
				t.Fatalf("WorkspaceShow(after crash) error = %v, code = %q, want no partial projection", err, localAPIErrorCode(err))
			}
			events, err := restartedClient.EventsList(context.Background(), 0, 100)
			if err != nil || len(events.Events) != 0 {
				t.Fatalf("EventsList(after crash) = %#v, %v, want no partial event", events, err)
			}
			if _, err := restartedClient.WorkspaceInit(context.Background(), "personal", "crash-key"); err != nil {
				t.Fatalf("WorkspaceInit(reuse key after crash) error = %v", err)
			}
			if _, err := restartedClient.Stop(context.Background()); err != nil {
				t.Fatalf("Stop(restarted) error = %v", err)
			}
			if err := restarted.wait(); err != nil {
				t.Fatalf("Run(restarted) error = %v", err)
			}
		})
	}
}

func TestWorkspaceCrashHelperProcess(t *testing.T) {
	if os.Getenv("CREWFOLD_WORKSPACE_CRASH_HELPER") == "" {
		t.Skip("helper process")
	}
	stage := os.Getenv("CREWFOLD_WORKSPACE_CRASH_STAGE")
	marker := os.Getenv("CREWFOLD_WORKSPACE_CRASH_MARKER")
	config := Config{
		DataDir:    os.Getenv("CREWFOLD_WORKSPACE_CRASH_DATA_DIR"),
		SocketPath: os.Getenv("CREWFOLD_WORKSPACE_CRASH_SOCKET"),
		Version:    buildinfo.Current(),
		Logger:     slog.New(slog.NewJSONHandler(io.Discard, nil)),
		StoreOptions: store.Options{MutationHook: func(current string) error {
			if current != stage {
				return nil
			}
			if err := os.WriteFile(marker, []byte(current+"\n"), 0o600); err != nil {
				return err
			}
			select {}
		}},
	}
	if err := Run(context.Background(), config); err != nil {
		t.Fatalf("Run(crash helper) error = %v", err)
	}
}

func TestDatabaseStartupFailureHasStableCode(t *testing.T) {
	t.Parallel()

	config := testConfig(t)
	if err := os.MkdirAll(config.DataDir, 0o700); err != nil {
		t.Fatalf("os.MkdirAll(data dir) error = %v", err)
	}
	target := filepath.Join(t.TempDir(), "user-database")
	if err := os.WriteFile(target, []byte("preserve"), 0o600); err != nil {
		t.Fatalf("os.WriteFile(target) error = %v", err)
	}
	if err := os.Symlink(target, filepath.Join(config.DataDir, "crewfold.db")); err != nil {
		t.Fatalf("os.Symlink(database) error = %v", err)
	}
	err := Run(context.Background(), config)
	if ErrorCode(err) != CodeDatabaseUnavailable {
		t.Fatalf("Run() error = %v, code = %q, want %q", err, ErrorCode(err), CodeDatabaseUnavailable)
	}
	contents, readErr := os.ReadFile(target)
	if readErr != nil || string(contents) != "preserve" {
		t.Fatalf("target = %q, %v, want preserved", contents, readErr)
	}
}

func waitForCondition(t *testing.T, timeout time.Duration, condition func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if condition() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", description)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func localAPIErrorCode(err error) string {
	var apiError *localapi.APIError
	if errors.As(err, &apiError) {
		return apiError.Code
	}
	return ""
}

type failingInspector struct {
	err error
}

func (inspector failingInspector) Inspect(context.Context, string) (domain.CheckoutObservation, error) {
	return domain.CheckoutObservation{}, inspector.err
}

func createGitFixture(t *testing.T, root string) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "test", "fixtures", "git", "create.sh"))
	if err != nil {
		t.Fatalf("filepath.Abs(fixture script) error = %v", err)
	}
	command := exec.Command(script, root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create Git fixture: %v\n%s", err, output)
	}
}

type runningServer struct {
	config Config
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func startTestServer(t *testing.T, config Config) *runningServer {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	running := &runningServer{
		config: config,
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go func() {
		running.err = Run(ctx, config)
		close(running.done)
	}()
	t.Cleanup(func() {
		running.cancel()
		if err := running.wait(); err != nil {
			t.Errorf("Run() cleanup error = %v", err)
		}
	})

	deadline := time.Now().Add(15 * time.Second)
	client := localapi.NewClient(config.SocketPath)
	var lastStatusErr error
	for {
		probeContext, cancelProbe := context.WithTimeout(context.Background(), 250*time.Millisecond)
		_, lastStatusErr = client.Status(probeContext)
		cancelProbe()
		if lastStatusErr == nil {
			break
		}
		select {
		case <-running.done:
			t.Fatalf("server exited before readiness: %v", running.wait())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for server readiness: %v", lastStatusErr)
		}
		time.Sleep(10 * time.Millisecond)
	}

	return running
}

func (running *runningServer) wait() error {
	<-running.done
	return running.err
}

func testConfig(t *testing.T) Config {
	t.Helper()

	root := t.TempDir()
	return Config{
		DataDir:    filepath.Join(root, "data"),
		SocketPath: filepath.Join(root, "crewfold.sock"),
		Version:    buildinfo.Current(),
		Logger:     slog.New(slog.NewJSONHandler(io.Discard, nil)),
	}
}

type lockedBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (buffer *lockedBuffer) Write(data []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.Write(data)
}

func (buffer *lockedBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.buffer.String()
}

func TestErrorCodeFallsBackForUnknownErrors(t *testing.T) {
	t.Parallel()

	if code := ErrorCode(syscall.EIO); code != "daemon_failed" {
		t.Fatalf("ErrorCode(EIO) = %q, want daemon_failed", code)
	}
}
