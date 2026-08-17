package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crewfold/internal/appdirs"
)

func TestInstallWritesPrivateExactUnitAndStartsIt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runtimeRoot, err := os.MkdirTemp("", "cf-svc-")
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
		t.Fatalf("Resolve() error = %v", err)
	}
	type call struct {
		name      string
		arguments []string
	}
	var calls []call
	manager := Manager{
		Paths: paths, Executable: filepath.Join(root, "Crewfold 100%", "crewfold"),
		EnvironmentPath: "/opt/owner-tools:/usr/bin", CodexToolNetworkAccess: true,
		Run: func(_ context.Context, name string, arguments ...string) ([]byte, error) {
			calls = append(calls, call{name: name, arguments: append([]string(nil), arguments...)})
			return nil, nil
		},
	}
	result, err := manager.Install(context.Background())
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	if result.Status != "active" || result.Socket != paths.SocketPath {
		t.Fatalf("Install() result = %#v", result)
	}
	contents, err := os.ReadFile(paths.UnitPath)
	if err != nil {
		t.Fatalf("os.ReadFile(unit) error = %v", err)
	}
	unit := string(contents)
	for _, required := range []string{
		`Environment="PATH=/opt/owner-tools:/usr/bin"`,
		`ExecStart="` + filepath.Join(root, `Crewfold 100%%`, `crewfold`) + `" daemon run`,
		`--data-dir "` + paths.DataDir + `"`,
		`--socket "` + paths.SocketPath + `"`,
		`--web-address 127.0.0.1:0`,
		`--codex-tool-network-access true`,
		`UMask=0077`,
		`NoNewPrivileges=true`,
		`RuntimeDirectory=crewfold`,
		`RuntimeDirectoryMode=0700`,
	} {
		if !strings.Contains(unit, required) {
			t.Fatalf("unit missing %q:\n%s", required, unit)
		}
	}
	info, err := os.Stat(paths.UnitPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("unit mode = %v error = %v, want 0600", info, err)
	}
	if len(calls) != 3 || strings.Join(calls[0].arguments, " ") != "--user daemon-reload" || strings.Join(calls[1].arguments, " ") != "--user enable crewfold.service" || strings.Join(calls[2].arguments, " ") != "--user restart crewfold.service" {
		t.Fatalf("systemctl calls = %#v", calls)
	}
}

func TestInstallRejectsRelativeOrUnsafeServicePath(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"relative:/usr/bin", ":/usr/bin", "/usr/bin/../bin", "/usr/bin\n/tmp"} {
		manager := Manager{Paths: appdirs.Paths{DataDir: "/data", SocketPath: "/run/crewfold.sock", UnitPath: "/config/crewfold.service"}, Executable: "/usr/bin/crewfold", EnvironmentPath: value, Run: func(context.Context, string, ...string) ([]byte, error) { return nil, nil }}
		if err := manager.validate(); err == nil {
			t.Errorf("validate(%q) error = nil", value)
		}
	}
}

func TestInstallMakesInstalledHerdrTheCompanionRuntimeService(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runtimeRoot, err := os.MkdirTemp("", "cf-svc-herdr-")
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
	var calls []string
	manager := Manager{
		Paths: paths, Executable: filepath.Join(root, "bin", "crewfold"), HerdrExecutable: filepath.Join(root, "bin", "herdr"),
		Run: func(_ context.Context, name string, arguments ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(arguments, " "))
			return nil, nil
		},
	}
	if _, err := manager.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	crewfoldUnit, err := os.ReadFile(paths.UnitPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(crewfoldUnit), "Wants="+HerdrUnitName) || !strings.Contains(string(crewfoldUnit), "After="+HerdrUnitName) {
		t.Fatalf("Crewfold unit lacks Herdr dependency:\n%s", crewfoldUnit)
	}
	herdrPath := filepath.Join(filepath.Dir(paths.UnitPath), HerdrUnitName)
	herdrUnit, err := os.ReadFile(herdrPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(herdrUnit), `ExecStart="`+filepath.Join(root, "bin", "herdr")+`" server`) {
		t.Fatalf("Herdr unit = %s", herdrUnit)
	}
	if !strings.Contains(string(herdrUnit), "Before="+UnitName) || !strings.Contains(string(herdrUnit), "PartOf="+UnitName) {
		t.Fatalf("Herdr unit is not lifecycle-bound to Crewfold:\n%s", herdrUnit)
	}
	if len(calls) != 5 || !strings.Contains(calls[1], "enable "+HerdrUnitName) || !strings.Contains(calls[2], "restart "+HerdrUnitName) || !strings.Contains(calls[3], "enable "+UnitName) || !strings.Contains(calls[4], "restart "+UnitName) {
		t.Fatalf("systemctl calls = %#v", calls)
	}
}

func TestInstallRefusesSymlinkedUnitWithoutChangingTarget(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	runtimeRoot, err := os.MkdirTemp("", "cf-svc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeRoot) })
	paths, err := appdirs.Resolve(filepath.Join(root, "home"), func(name string) string {
		if name == "XDG_RUNTIME_DIR" {
			return runtimeRoot
		}
		if name == "XDG_CONFIG_HOME" {
			return filepath.Join(root, "config")
		}
		return filepath.Join(root, strings.ToLower(strings.TrimPrefix(name, "XDG_")))
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.UnitPath), 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "owner-file")
	if err := os.WriteFile(target, []byte("preserve\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, paths.UnitPath); err != nil {
		t.Fatal(err)
	}
	manager := Manager{Paths: paths, Executable: "/opt/crewfold", Run: func(context.Context, string, ...string) ([]byte, error) { return nil, nil }}
	if _, err := manager.Install(context.Background()); err == nil {
		t.Fatal("Install() error = nil, want refusal")
	}
	contents, err := os.ReadFile(target)
	if err != nil || string(contents) != "preserve\n" {
		t.Fatalf("target contents = %q error = %v", contents, err)
	}
}

func TestStatusReturnsExactSystemdState(t *testing.T) {
	t.Parallel()

	manager := Manager{
		Paths:      appdirs.Paths{DataDir: "/data", SocketPath: "/run/crewfold.sock", UnitPath: "/config/crewfold.service"},
		Executable: "/usr/bin/crewfold",
		Run: func(_ context.Context, name string, arguments ...string) ([]byte, error) {
			if name != "systemctl" || strings.Join(arguments, " ") != "--user show --property=ActiveState --value crewfold.service" {
				t.Fatalf("command = %s %#v", name, arguments)
			}
			return []byte("activating\n"), nil
		},
	}
	result, err := manager.Status(context.Background())
	if err != nil || result.Status != "activating" {
		t.Fatalf("Status() = %#v, %v", result, err)
	}
}

func TestStatusRejectsUnsafeOrSchemaInvalidState(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"Active", "active now", "active\x1b[31m", "active\nfailed"} {
		manager := Manager{
			Paths:      appdirs.Paths{DataDir: "/data", SocketPath: "/run/crewfold.sock", UnitPath: "/config/crewfold.service"},
			Executable: "/usr/bin/crewfold",
			Run:        func(context.Context, string, ...string) ([]byte, error) { return []byte(state), nil },
		}
		if _, err := manager.Status(context.Background()); err == nil {
			t.Errorf("Status(%q) error = nil, want refusal", state)
		}
	}
}

func TestSystemdFailureIsBoundedAndVisible(t *testing.T) {
	t.Parallel()

	manager := Manager{
		Paths:      appdirs.Paths{DataDir: "/data", SocketPath: "/run/crewfold.sock", UnitPath: "/config/crewfold.service"},
		Executable: "/usr/bin/crewfold",
		Run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("\x1b[31m" + strings.Repeat("x", 4096)), errors.New("exit 1")
		},
	}
	if _, err := manager.Start(context.Background()); err == nil || len(err.Error()) > 2200 || strings.ContainsRune(err.Error(), '\x1b') {
		t.Fatalf("Start() error = %v", err)
	}
}
