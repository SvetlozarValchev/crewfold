package roomcli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"crewfold/internal/appdirs"
	"crewfold/internal/buildinfo"
)

const connectorReleaseAPI = "https://api.github.com/repos/SvetlozarValchev/crewfold/releases/latest"
const connectorAsset = "crewfold-connector-linux-amd64.tar.gz"
const connectorUpdateTimer = "crewfold-connector-update.timer"
const maxConnectorDownload = 64 << 20

var stableVersion = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

func compareVersions(a, b string) (int, error) {
	x, y := stableVersion.FindStringSubmatch(a), stableVersion.FindStringSubmatch(b)
	if x == nil || y == nil {
		return 0, errors.New("updates require stable vMAJOR.MINOR.PATCH versions; development builds are not replaced automatically")
	}
	for i := 1; i <= 3; i++ {
		// Numeric comparison without integer overflow.
		if len(x[i]) > len(y[i]) {
			return 1, nil
		}
		if len(x[i]) < len(y[i]) {
			return -1, nil
		}
		if x[i] > y[i] {
			return 1, nil
		}
		if x[i] < y[i] {
			return -1, nil
		}
	}
	return 0, nil
}

// Also used by delivery workers: an updater takes the exclusive lock only after
// downloads/preflight, and keeps it across restart/rollback. Polling is not locked.
func connectorLock(ctx context.Context, path string, exclusive bool) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	op := syscall.LOCK_SH
	if exclusive {
		op = syscall.LOCK_EX
	}
	for {
		if err := ctx.Err(); err != nil {
			f.Close()
			return nil, err
		}
		err = syscall.Flock(int(f.Fd()), op|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			f.Close()
			return nil, err
		}
		waitContext(ctx, 100*time.Millisecond)
	}
}

func connectorSystemctl(ctx context.Context, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("systemctl %s: %s: %w", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return strings.TrimSpace(string(out)), nil
}

func configureConnectorUpdates(ctx context.Context, paths appdirs.Paths, enabled bool) error {
	if !enabled {
		_, err := connectorSystemctl(ctx, "disable", "--now", connectorUpdateTimer)
		return err
	}
	if info, err := os.Stat(paths.ConnectorBinaryPath); err != nil || !info.Mode().IsRegular() {
		return errors.New("run crewfold-connector setup first")
	}
	dir := filepath.Dir(paths.ConnectorUnitPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	service := `[Unit]
Description=Update Crewfold connector from stable GitHub releases
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=` + systemdQuote(paths.ConnectorBinaryPath) + ` update --automatic --config ` + systemdQuote(paths.ConnectorConfigPath) + `
TimeoutStartSec=10min
UMask=0077
NoNewPrivileges=true
`
	timer := `[Unit]
Description=Check for Crewfold connector updates hourly

[Timer]
OnCalendar=hourly
RandomizedDelaySec=10min
Persistent=true

[Install]
WantedBy=timers.target
`
	if err := writeAtomic(filepath.Join(dir, "crewfold-connector-update.service"), []byte(service)); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dir, connectorUpdateTimer), []byte(timer)); err != nil {
		return err
	}
	if _, err := connectorSystemctl(ctx, "daemon-reload"); err != nil {
		return err
	}
	_, err := connectorSystemctl(ctx, "enable", "--now", connectorUpdateTimer)
	return err
}

func (a *App) connectorAutoUpdate(ctx context.Context, args []string, paths appdirs.Paths, jsonMode bool) int {
	if len(args) != 1 || (args[0] != "enable" && args[0] != "disable" && args[0] != "status") {
		return a.fail(errors.New("usage: crewfold-connector auto-update enable|disable|status"))
	}
	if args[0] != "status" {
		if err := configureConnectorUpdates(ctx, paths, args[0] == "enable"); err != nil {
			return a.fail(err)
		}
	}
	status, err := connectorSystemctl(ctx, "show", "--property=ActiveState", "--value", connectorUpdateTimer)
	if err != nil {
		return a.fail(err)
	}
	return a.print(map[string]string{"status": status, "timer": connectorUpdateTimer}, jsonMode, func() { fmt.Fprintf(a.stdout, "Connector automatic updates: %s (hourly stable releases).\n", status) })
}

type connectorRelease struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

type connectorUpdater struct {
	client    *http.Client
	latestURL string
	inspect   func(context.Context, string) (buildinfo.Info, error)
	preflight func(context.Context, string, string) error
	service   func(context.Context, ...string) (string, error)
	health    func(context.Context, string) error
}

func newConnectorUpdater() connectorUpdater {
	return connectorUpdater{
		client: &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 6 {
				return errors.New("too many release redirects")
			}
			host := req.URL.Hostname()
			if req.URL.Scheme != "https" || (host != "github.com" && host != "api.github.com" && host != "release-assets.githubusercontent.com" && host != "objects.githubusercontent.com") {
				return errors.New("untrusted release redirect")
			}
			return nil
		}}, latestURL: connectorReleaseAPI, inspect: inspectConnectorBinary, service: connectorSystemctl, health: connectorUpdateHealth,
		preflight: func(ctx context.Context, binary, config string) error {
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := exec.CommandContext(ctx, binary, "service", "check-config", "--config", config).Run(); err != nil {
				return fmt.Errorf("candidate cannot read existing configuration: %w", err)
			}
			return nil
		},
	}
}

func inspectConnectorBinary(ctx context.Context, binary string) (buildinfo.Info, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var info buildinfo.Info
	out, err := exec.CommandContext(ctx, binary, "--output", "json", "version").Output()
	if err != nil {
		return info, err
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return info, err
	}
	return info, info.Validate()
}

func connectorUpdateHealth(ctx context.Context, binary string) error {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	want, err := os.Stat(binary)
	if err != nil {
		return err
	}
	previousPID := ""
	for ctx.Err() == nil {
		state, stateErr := connectorSystemctl(ctx, "show", "--property=ActiveState", "--value", "crewfold-connector.service")
		pid, pidErr := connectorSystemctl(ctx, "show", "--property=MainPID", "--value", "crewfold-connector.service")
		if n, err := strconv.Atoi(pid); err == nil && n > 0 && stateErr == nil && pidErr == nil && state == "active" {
			got, err := os.Stat("/proc/" + pid + "/exe")
			if err == nil && os.SameFile(want, got) {
				if pid == previousPID {
					return nil
				}
				previousPID = pid
			} else {
				previousPID = ""
			}
		} else {
			previousPID = ""
		}
		waitContext(ctx, time.Second)
	}
	return errors.New("connector did not remain active on the installed binary")
}

func (u connectorUpdater) download(ctx context.Context, address string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "crewfold-connector-updater")
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("release download returned HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errors.New("release download exceeds size limit")
	}
	return b, nil
}

func releaseAssetURL(release connectorRelease, name string) (string, error) {
	expected := "https://github.com/SvetlozarValchev/crewfold/releases/download/" + release.Tag + "/" + name
	found := ""
	for _, asset := range release.Assets {
		if asset.Name != name {
			continue
		}
		parsed, err := url.Parse(asset.URL)
		if err != nil || asset.URL != expected || parsed.User != nil || found != "" {
			return "", errors.New("release has invalid or duplicate asset URL")
		}
		found = asset.URL
	}
	if found == "" {
		return "", fmt.Errorf("release is missing %s", name)
	}
	return found, nil
}

func extractConnector(archive, checksum []byte, destination string) error {
	fields := strings.Fields(string(checksum))
	if len(fields) != 2 || fields[1] != connectorAsset {
		return errors.New("invalid release checksum file")
	}
	digest := sha256.Sum256(archive)
	if !strings.EqualFold(fields[0], hex.EncodeToString(digest[:])) {
		return errors.New("release checksum mismatch")
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(io.LimitReader(gz, maxConnectorDownload+1))
	seen := map[string]bool{}
	found := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if seen[h.Name] {
			return errors.New("duplicate archive entry")
		}
		seen[h.Name] = true
		if h.Name == "crewfold-connector-linux-amd64/" && h.Typeflag == tar.TypeDir {
			continue
		}
		if (h.Name != "crewfold-connector-linux-amd64/crewfold-connector" && h.Name != "crewfold-connector-linux-amd64/README.md") || h.Typeflag != tar.TypeReg || h.Size < 0 || h.Size > maxConnectorDownload {
			return errors.New("unexpected release archive entry")
		}
		if strings.HasSuffix(h.Name, "/README.md") {
			continue
		}
		f, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
		if err != nil {
			return err
		}
		_, err = io.CopyN(f, tr, h.Size)
		syncErr := f.Sync()
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if syncErr != nil {
			return syncErr
		}
		if closeErr != nil {
			return closeErr
		}
		found = true
	}
	if !found {
		return errors.New("release archive has no connector")
	}
	return nil
}

func (u connectorUpdater) update(ctx context.Context, paths appdirs.Paths, check bool) (map[string]string, error) {
	if runtime.GOOS != "linux" || runtime.GOARCH != "amd64" {
		return nil, errors.New("automatic releases currently support linux/amd64 only")
	}
	lockCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	lock, err := connectorLock(lockCtx, paths.ConnectorConfigPath+".update.lock", true)
	if err != nil {
		return nil, fmt.Errorf("another update or setup is running: %w", err)
	}
	defer lock.Close()
	installed, err := u.inspect(ctx, paths.ConnectorBinaryPath)
	if err != nil {
		return nil, fmt.Errorf("inspect installed connector (run setup first): %w", err)
	}
	data, err := u.download(ctx, u.latestURL, 2<<20)
	if err != nil {
		return nil, err
	}
	var release connectorRelease
	if err := json.Unmarshal(data, &release); err != nil {
		return nil, err
	}
	if release.Draft || release.Prerelease {
		return nil, errors.New("refusing non-stable release")
	}
	comparison, err := compareVersions(release.Tag, installed.Version)
	if err != nil {
		return nil, err
	}
	result := map[string]string{"installed": installed.Version, "latest": release.Tag, "status": "up-to-date"}
	if comparison <= 0 {
		return result, nil
	}
	result["status"] = "available"
	if check {
		return result, nil
	}
	archiveURL, err := releaseAssetURL(release, connectorAsset)
	if err != nil {
		return nil, err
	}
	checksumURL, err := releaseAssetURL(release, connectorAsset+".sha256")
	if err != nil {
		return nil, err
	}
	checksum, err := u.download(ctx, checksumURL, 4096)
	if err != nil {
		return nil, err
	}
	archive, err := u.download(ctx, archiveURL, maxConnectorDownload)
	if err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(filepath.Dir(paths.ConnectorBinaryPath), ".connector-update-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	candidate := filepath.Join(stage, "crewfold-connector")
	if err := extractConnector(archive, checksum, candidate); err != nil {
		return nil, err
	}
	info, err := u.inspect(ctx, candidate)
	if err != nil {
		return nil, fmt.Errorf("candidate version check: %w", err)
	}
	if info.Version != release.Tag || info.Platform != "linux/amd64" {
		return nil, errors.New("candidate does not match the published release/platform")
	}
	if err := u.preflight(ctx, candidate, paths.ConnectorConfigPath); err != nil {
		return nil, err
	}
	drainCtx, drainCancel := context.WithTimeout(ctx, 2*time.Minute)
	defer drainCancel()
	deliveryLock, err := connectorLock(drainCtx, paths.ConnectorConfigPath+".delivery.lock", true)
	if err != nil {
		return nil, fmt.Errorf("waiting for active deliveries (nothing changed): %w", err)
	}
	defer deliveryLock.Close()
	state, err := u.service(ctx, "show", "--property=ActiveState", "--value", "crewfold-connector.service")
	if err != nil {
		return nil, err
	}
	if state != "active" && state != "inactive" && state != "failed" {
		return nil, fmt.Errorf("connector is %s; try again after service transition", state)
	}
	backup := paths.ConnectorBinaryPath + ".previous"
	if err := installConnectorBinary(paths.ConnectorBinaryPath, backup); err != nil {
		return nil, err
	}
	if err := installConnectorBinary(candidate, paths.ConnectorBinaryPath); err != nil {
		return nil, err
	}
	if state == "active" {
		// Independent short context lets rollback complete even if the caller is interrupted.
		restartCtx, restartCancel := context.WithTimeout(context.Background(), time.Minute)
		defer restartCancel()
		_, err = u.service(restartCtx, "restart", "crewfold-connector.service")
		if err == nil {
			err = u.health(restartCtx, paths.ConnectorBinaryPath)
		}
		if err != nil {
			restoreErr := installConnectorBinary(backup, paths.ConnectorBinaryPath)
			if restoreErr != nil {
				return nil, fmt.Errorf("update failed: %v; rollback failed: %w; previous binary: %s", err, restoreErr, backup)
			}
			_, restartErr := u.service(restartCtx, "restart", "crewfold-connector.service")
			if restartErr == nil {
				restartErr = u.health(restartCtx, paths.ConnectorBinaryPath)
			}
			return nil, fmt.Errorf("update failed: %v; restored previous binary (recovery status: %v)", err, restartErr)
		}
	}
	result["status"], result["installed"] = "updated", release.Tag
	return result, nil
}

func (a *App) connectorUpdate(ctx context.Context, args []string, paths appdirs.Paths, jsonMode bool) int {
	config, args, err := pullOption(args, "config")
	if err != nil {
		return a.fail(err)
	}
	if config != "" {
		paths.ConnectorConfigPath = config
	}
	check := false
	for _, arg := range args {
		switch arg {
		case "--check":
			check = true
		case "--automatic":
		default:
			return a.fail(errors.New("usage: crewfold-connector update [--check]"))
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	result, err := newConnectorUpdater().update(ctx, paths, check)
	if err != nil {
		return a.fail(err)
	}
	return a.print(result, jsonMode, func() {
		fmt.Fprintf(a.stdout, "Connector %s: installed %s, latest %s.\n", result["status"], result["installed"], result["latest"])
	})
}
