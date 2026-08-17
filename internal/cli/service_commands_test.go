package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"crewfold/internal/appdirs"
	"crewfold/internal/localapi"
	protocolschema "crewfold/protocol"
)

func TestM21ServiceInstallUsesPrivateDefaultsAndMachineContract(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runtimeRoot, err := os.MkdirTemp("", "cf-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeRoot) })
	paths, err := appdirs.Resolve(filepath.Join(root, "home"), func(name string) string {
		switch name {
		case "XDG_STATE_HOME":
			return filepath.Join(root, "state")
		case "XDG_CONFIG_HOME":
			return filepath.Join(root, "config")
		case "XDG_RUNTIME_DIR":
			return runtimeRoot
		default:
			return ""
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	app, stdout, stderr := newTestApp()
	app.resolveAppDirs = func() (appdirs.Paths, error) { return paths, nil }
	app.executablePath = func() (string, error) { return filepath.Join(root, "bin", "crewfold"), nil }
	app.lookPath = func(string) (string, error) { return "", errors.New("not installed") }
	var commands []string
	app.runService = func(_ context.Context, name string, arguments ...string) ([]byte, error) {
		commands = append(commands, name+" "+strings.Join(arguments, " "))
		return nil, nil
	}
	if exit := app.Run([]string{"service", "install", "--output=json"}); exit != ExitOK {
		t.Fatalf("service install exit = %d stderr = %q", exit, stderr.String())
	}
	if err := protocolschema.ValidateJSON("cli/v1/service.response.schema.json", stdout.Bytes()); err != nil {
		t.Fatalf("service response schema error = %v; response = %s", err, stdout.String())
	}
	var response serviceResponse
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != "active" || response.DataDir != paths.DataDir || response.Socket != paths.SocketPath {
		t.Fatalf("service response = %#v", response)
	}
	if response.CodexToolNetworkAccess == nil || !*response.CodexToolNetworkAccess {
		t.Fatalf("service response network policy = %#v", response.CodexToolNetworkAccess)
	}
	unit, err := os.ReadFile(paths.UnitPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), "--codex-tool-network-access true") {
		t.Fatalf("installed service did not enable the ordinary dependency network policy:\n%s", unit)
	}
	if len(commands) != 3 || !strings.Contains(commands[1], "enable crewfold.service") || !strings.Contains(commands[2], "restart crewfold.service") {
		t.Fatalf("systemctl commands = %#v", commands)
	}
}

func TestM21ServiceInstallCanExplicitlyDisableCodexToolNetwork(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	paths := appdirs.Paths{
		StateDir: filepath.Join(root, "state"), ConfigDir: filepath.Join(root, "config"), RuntimeDir: filepath.Join(root, "run"),
		DataDir: filepath.Join(root, "state", "crewfold"), SocketPath: filepath.Join(root, "run", "crewfold.sock"), UnitPath: filepath.Join(root, "config", "crewfold.service"),
	}
	app, stdout, stderr := newTestApp()
	app.resolveAppDirs = func() (appdirs.Paths, error) { return paths, nil }
	app.executablePath = func() (string, error) { return filepath.Join(root, "bin", "crewfold"), nil }
	app.lookPath = func(string) (string, error) { return "", errors.New("not installed") }
	app.runService = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	if exit := app.Run([]string{"service", "install", "--codex-tool-network-access", "false"}); exit != ExitOK {
		t.Fatalf("service install exit = %d stderr = %q", exit, stderr.String())
	}
	unit, err := os.ReadFile(paths.UnitPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(unit), "--codex-tool-network-access false") || !strings.Contains(stdout.String(), "network: disabled") {
		t.Fatalf("unit=%s stdout=%q", unit, stdout.String())
	}
}

func TestM21OpenUsesDefaultSocketAndNeverPrintsBootstrap(t *testing.T) {
	t.Parallel()

	paths := appdirs.Paths{SocketPath: "/run/user/1000/crewfold/crewfold.sock"}
	token := strings.Repeat("a", 64)
	client := &fakeDaemonClient{webBootstrap: localapi.WebBootstrapResult{
		Schema: localapi.WebBootstrapSchema, Type: "web_bootstrap",
		URL: "http://127.0.0.1:43121/#bootstrap=" + token, ExpiresAt: "2026-08-14T20:00:00Z",
	}}
	app, stdout, stderr := newTestApp()
	app.resolveAppDirs = func() (appdirs.Paths, error) { return paths, nil }
	app.newClient = func(socket string) daemonClient {
		if socket != paths.SocketPath {
			t.Fatalf("socket = %q, want %q", socket, paths.SocketPath)
		}
		return client
	}
	var opened string
	app.openURL = func(_ context.Context, value string) error { opened = value; return nil }
	if exit := app.Run([]string{"open", "--output=json"}); exit != ExitOK {
		t.Fatalf("open exit = %d stderr = %q", exit, stderr.String())
	}
	if opened != client.webBootstrap.URL {
		t.Fatalf("opened URL = %q", opened)
	}
	if strings.Contains(stdout.String(), token) || strings.Contains(stderr.String(), token) {
		t.Fatalf("bootstrap leaked to output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if err := protocolschema.ValidateJSON("cli/v1/workbench-open.response.schema.json", stdout.Bytes()); err != nil {
		t.Fatalf("open response schema error = %v; response = %s", err, stdout.String())
	}
}

func TestM21OpenWaitsForServiceSocketWithoutRetryingContractFailures(t *testing.T) {
	t.Parallel()

	bootstrap := localapi.WebBootstrapResult{
		Schema: localapi.WebBootstrapSchema, Type: "web_bootstrap",
		URL: "http://127.0.0.1:43121/#bootstrap=" + strings.Repeat("c", 64), ExpiresAt: "2026-08-14T20:00:00Z",
	}
	t.Run("startup connection", func(t *testing.T) {
		client := &fakeDaemonClient{
			webBootstrap: bootstrap,
			webBootstrapErrors: []error{
				&net.OpError{Op: "dial", Net: "unix", Err: syscall.ENOENT},
				&net.OpError{Op: "dial", Net: "unix", Err: syscall.ECONNREFUSED},
			},
		}
		app, _, stderr := newTestApp()
		app.newClient = func(string) daemonClient { return client }
		app.openURL = func(context.Context, string) error { return nil }
		if exit := app.Run([]string{"open", "--socket", "/tmp/crewfold.sock"}); exit != ExitOK {
			t.Fatalf("open exit=%d stderr=%q", exit, stderr.String())
		}
		if client.webBootstrapCalls != 3 {
			t.Fatalf("bootstrap calls = %d, want 3", client.webBootstrapCalls)
		}
	})

	t.Run("contract failure", func(t *testing.T) {
		client := &fakeDaemonClient{webBootstrapErr: &localapi.APIError{Code: "invalid_request", Message: "refused"}}
		app, _, _ := newTestApp()
		app.newClient = func(string) daemonClient { return client }
		if exit := app.Run([]string{"open", "--socket", "/tmp/crewfold.sock"}); exit != ExitFailure {
			t.Fatalf("open exit=%d, want failure", exit)
		}
		if client.webBootstrapCalls != 1 {
			t.Fatalf("bootstrap calls = %d, want 1", client.webBootstrapCalls)
		}
	})
}

func TestM21OpenRejectsInvalidDaemonURLBeforeBrowser(t *testing.T) {
	t.Parallel()

	client := &fakeDaemonClient{webBootstrap: localapi.WebBootstrapResult{
		Schema: localapi.WebBootstrapSchema, Type: "web_bootstrap",
		URL: "http://attacker.invalid/#bootstrap=" + strings.Repeat("a", 64), ExpiresAt: "2026-08-14T20:00:00Z",
	}}
	app, _, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	opened := false
	app.openURL = func(context.Context, string) error { opened = true; return nil }
	if exit := app.Run([]string{"open", "--socket", "/tmp/crewfold.sock"}); exit != ExitFailure {
		t.Fatalf("open exit = %d, want failure", exit)
	}
	if opened || !strings.Contains(stderr.String(), "invalid owner-local URL") {
		t.Fatalf("opened=%t stderr=%q", opened, stderr.String())
	}
}

func TestM21ServiceAndOpenFailuresAreVisible(t *testing.T) {
	t.Parallel()

	t.Run("paths", func(t *testing.T) {
		app, _, stderr := newTestApp()
		app.resolveAppDirs = func() (appdirs.Paths, error) { return appdirs.Paths{}, errors.New("bad XDG root") }
		if exit := app.Run([]string{"service", "status"}); exit != ExitFailure || !strings.Contains(stderr.String(), "bad XDG root") {
			t.Fatalf("service status exit=%d stderr=%q", exit, stderr.String())
		}
	})
	t.Run("browser", func(t *testing.T) {
		app, _, stderr := newTestApp()
		app.newClient = func(string) daemonClient {
			return &fakeDaemonClient{webBootstrap: localapi.WebBootstrapResult{
				Schema: localapi.WebBootstrapSchema, Type: "web_bootstrap",
				URL: "http://127.0.0.1:43121/#bootstrap=" + strings.Repeat("b", 64), ExpiresAt: "2026-08-14T20:00:00Z",
			}}
		}
		app.openURL = func(context.Context, string) error { return errors.New("desktop unavailable") }
		if exit := app.Run([]string{"open", "--socket", "/tmp/crewfold.sock"}); exit != ExitFailure || !strings.Contains(stderr.String(), "browser") {
			t.Fatalf("open exit=%d stderr=%q", exit, stderr.String())
		}
	})
}
