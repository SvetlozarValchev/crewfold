// Package service installs and controls Crewfold's owner-local Linux service.
package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"crewfold/internal/appdirs"
)

const (
	UnitName      = "crewfold.service"
	HerdrUnitName = "crewfold-herdr.service"
)

// Runner invokes one process and returns its bounded combined output.
type Runner func(context.Context, string, ...string) ([]byte, error)

// Manager owns one exact systemd user-service definition.
type Manager struct {
	Paths           appdirs.Paths
	Executable      string
	HerdrExecutable string
	Run             Runner
}

// Result is the stable human-facing service lifecycle result.
type Result struct {
	Action   string `json:"action"`
	Status   string `json:"status"`
	UnitPath string `json:"unit_path"`
	DataDir  string `json:"data_dir"`
	Socket   string `json:"socket"`
}

// Install writes the exact current unit, reloads systemd, and enables and
// starts the service. It never embeds credentials or provider configuration.
func (m Manager) Install(ctx context.Context) (Result, error) {
	if err := m.validate(); err != nil {
		return Result{}, err
	}
	for _, directory := range []string{m.Paths.StateDir, m.Paths.ConfigDir, m.Paths.RuntimeDir, filepath.Dir(m.Paths.UnitPath)} {
		if err := ensurePrivateDirectory(directory); err != nil {
			return Result{}, fmt.Errorf("prepare service directory %s: %w", directory, err)
		}
	}
	if err := writeUnitAtomic(m.Paths.UnitPath, []byte(m.unit())); err != nil {
		return Result{}, fmt.Errorf("publish Crewfold user service: %w", err)
	}
	if m.HerdrExecutable != "" {
		if err := writeUnitAtomic(m.herdrUnitPath(), []byte(m.herdrUnit())); err != nil {
			return Result{}, fmt.Errorf("publish Crewfold Herdr runtime service: %w", err)
		}
	}
	if err := m.run(ctx, "daemon-reload"); err != nil {
		return Result{}, err
	}
	if m.HerdrExecutable != "" {
		if err := m.run(ctx, "enable", HerdrUnitName); err != nil {
			return Result{}, err
		}
		if err := m.run(ctx, "restart", HerdrUnitName); err != nil {
			return Result{}, err
		}
	}
	if err := m.run(ctx, "enable", UnitName); err != nil {
		return Result{}, err
	}
	if err := m.run(ctx, "restart", UnitName); err != nil {
		return Result{}, err
	}
	return m.result("install", "active"), nil
}

func (m Manager) Start(ctx context.Context) (Result, error) {
	if err := m.validate(); err != nil {
		return Result{}, err
	}
	if err := m.run(ctx, "start", UnitName); err != nil {
		return Result{}, err
	}
	return m.result("start", "active"), nil
}

func (m Manager) Stop(ctx context.Context) (Result, error) {
	if err := m.validate(); err != nil {
		return Result{}, err
	}
	if err := m.run(ctx, "stop", UnitName); err != nil {
		return Result{}, err
	}
	return m.result("stop", "inactive"), nil
}

func (m Manager) Status(ctx context.Context) (Result, error) {
	if err := m.validate(); err != nil {
		return Result{}, err
	}
	output, err := m.Run(ctx, "systemctl", "--user", "show", "--property=ActiveState", "--value", UnitName)
	if err != nil {
		return Result{}, commandError("inspect", output, err)
	}
	state := strings.TrimSpace(string(output))
	if !validServiceState(state) {
		return Result{}, errors.New("systemd returned an invalid Crewfold service state")
	}
	return m.result("status", state), nil
}

func validServiceState(value string) bool {
	if len(value) == 0 || len(value) > 64 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for index := 1; index < len(value); index++ {
		current := value[index]
		if (current < 'a' || current > 'z') && current != '_' && current != '-' {
			return false
		}
	}
	return true
}

func (m Manager) result(action, status string) Result {
	return Result{Action: action, Status: status, UnitPath: m.Paths.UnitPath, DataDir: m.Paths.DataDir, Socket: m.Paths.SocketPath}
}

func (m Manager) validate() error {
	if m.Run == nil {
		return errors.New("service command runner is unavailable")
	}
	if !filepath.IsAbs(m.Executable) || filepath.Clean(m.Executable) != m.Executable || containsUnsafeServiceText(m.Executable) {
		return errors.New("service executable must be a canonical absolute path")
	}
	if m.HerdrExecutable != "" && (!filepath.IsAbs(m.HerdrExecutable) || filepath.Clean(m.HerdrExecutable) != m.HerdrExecutable || containsUnsafeServiceText(m.HerdrExecutable)) {
		return errors.New("Herdr service executable must be a canonical absolute path")
	}
	for name, value := range map[string]string{
		"data directory": m.Paths.DataDir,
		"socket path":    m.Paths.SocketPath,
		"unit path":      m.Paths.UnitPath,
	} {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value || containsUnsafeServiceText(value) {
			return fmt.Errorf("service %s must be a canonical absolute path", name)
		}
	}
	return nil
}

func containsUnsafeServiceText(value string) bool {
	for _, current := range value {
		if current < 0x20 || current >= 0x7f && current <= 0x9f || current == '\u2028' || current == '\u2029' ||
			current >= '\u202a' && current <= '\u202e' || current >= '\u2066' && current <= '\u2069' {
			return true
		}
	}
	return false
}

func (m Manager) run(ctx context.Context, arguments ...string) error {
	output, err := m.Run(ctx, "systemctl", append([]string{"--user"}, arguments...)...)
	if err != nil {
		return commandError(arguments[0], output, err)
	}
	return nil
}

func commandError(action string, output []byte, err error) error {
	detail := safeCommandDetail(string(output), 2048)
	if detail == "" {
		return fmt.Errorf("systemd could not %s Crewfold service: %w", action, err)
	}
	return fmt.Errorf("systemd could not %s Crewfold service: %s: %w", action, detail, err)
}

func safeCommandDetail(value string, maximum int) string {
	var builder strings.Builder
	for _, current := range strings.TrimSpace(strings.ToValidUTF8(value, "�")) {
		unsafe := current < 0x20 || current >= 0x7f && current <= 0x9f || current == '\u2028' || current == '\u2029' ||
			current >= '\u202a' && current <= '\u202e' || current >= '\u2066' && current <= '\u2069'
		if unsafe {
			current = '�'
		}
		if builder.Len()+utf8.RuneLen(current) > maximum {
			break
		}
		builder.WriteRune(current)
	}
	return builder.String()
}

func (m Manager) unit() string {
	dependency := ""
	if m.HerdrExecutable != "" {
		dependency = "Wants=" + HerdrUnitName + "\nAfter=" + HerdrUnitName + "\n"
	}
	return `[Unit]
Description=Crewfold local agent control plane
After=default.target
` + dependency + `

[Service]
Type=simple
ExecStart=` + systemdQuote(m.Executable) + ` daemon run --data-dir ` + systemdQuote(m.Paths.DataDir) + ` --socket ` + systemdQuote(m.Paths.SocketPath) + ` --web-address 127.0.0.1:0
Restart=on-failure
RestartSec=2s
UMask=0077
NoNewPrivileges=true
RuntimeDirectory=crewfold
RuntimeDirectoryMode=0700

[Install]
WantedBy=default.target
`
}

func (m Manager) herdrUnitPath() string {
	return filepath.Join(filepath.Dir(m.Paths.UnitPath), HerdrUnitName)
}

func (m Manager) herdrUnit() string {
	return `[Unit]
Description=Herdr interactive runtime host for Crewfold
After=default.target
Before=` + UnitName + `
PartOf=` + UnitName + `

[Service]
Type=simple
ExecStart=` + systemdQuote(m.HerdrExecutable) + ` server
Restart=on-failure
RestartSec=2s
UMask=0077
NoNewPrivileges=true

[Install]
WantedBy=default.target
`
}

func systemdQuote(value string) string {
	// A percent remains a systemd specifier inside quotes and therefore must be
	// doubled. strconv.Quote supplies a deterministic C-style quoted word.
	return strings.ReplaceAll(strconv.Quote(value), "%", "%%")
}

func ensurePrivateDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("directory path must be canonical and absolute")
	}
	volume := filepath.VolumeName(path)
	current := volume + string(filepath.Separator)
	remainder := strings.TrimPrefix(path, current)
	for _, component := range strings.Split(remainder, string(filepath.Separator)) {
		if component == "" {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return errors.New("directory path contains a non-directory or symbolic link")
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("directory is not owned by the current user")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func writeUnitAtomic(path string, contents []byte) error {
	parent := filepath.Dir(path)
	if info, err := os.Lstat(path); err == nil {
		if err := validatePrivateRegular(info); err != nil {
			return fmt.Errorf("existing service unit is unsafe: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	stage := filepath.Join(parent, ".crewfold.service.stage")
	if info, err := os.Lstat(stage); err == nil {
		if err := validatePrivateRegular(info); err != nil {
			return fmt.Errorf("interrupted service stage is unsafe: %w", err)
		}
		if err := os.Remove(stage); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	file, err := os.OpenFile(stage, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	removeStage := true
	defer func() {
		_ = file.Close()
		if removeStage {
			_ = os.Remove(stage)
		}
	}()
	if _, err := file.Write(contents); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(stage, path); err != nil {
		return err
	}
	removeStage = false
	return syncDirectory(parent)
}

func validatePrivateRegular(info os.FileInfo) error {
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return errors.New("file must be a private regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 {
		return errors.New("file must be singly linked and owned by the current user")
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		return err
	}
	return nil
}
