package roomcli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"crewfold/internal/appdirs"
	"crewfold/internal/buildinfo"
)

type updaterTransport func(*http.Request) (*http.Response, error)

func (f updaterTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func updateArchive(t *testing.T, name string, kind byte) ([]byte, []byte) {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tr := tar.NewWriter(gz)
	content := []byte("new-binary")
	h := &tar.Header{Name: name, Typeflag: kind, Mode: 0755, Size: int64(len(content))}
	if kind == tar.TypeSymlink {
		h.Size = 0
		h.Linkname = "/tmp/escape"
	}
	if err := tr.WriteHeader(h); err != nil {
		t.Fatal(err)
	}
	if h.Size > 0 {
		if _, err := tr.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(b.Bytes())
	return b.Bytes(), []byte(fmt.Sprintf("%x  %s\n", digest, connectorAsset))
}

func TestStableVersionOrdering(t *testing.T) {
	for _, tc := range []struct {
		a, b    string
		want    int
		invalid bool
	}{
		{"v0.10.0", "v0.9.9", 1, false}, {"v1.0.0", "v2.0.0", -1, false}, {"1.2.3", "v1.2.3", 0, false},
		{"v1.2.4-rc1", "v1.2.3", 0, true}, {"v1.2.4", "dev", 0, true}, {"v01.2.3", "v1.2.3", 0, true},
	} {
		got, err := compareVersions(tc.a, tc.b)
		if got != tc.want || (err != nil) != tc.invalid {
			t.Fatalf("%s %s: %d %v", tc.a, tc.b, got, err)
		}
	}
}

func TestReleaseExtractionRejectsUntrustedContent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    byte
		corrupt bool
	}{
		{"../../escape", tar.TypeReg, false}, {"crewfold-connector-linux-amd64/crewfold-connector", tar.TypeSymlink, false},
		{"crewfold-connector-linux-amd64/crewfold-connector", tar.TypeReg, true},
	} {
		archive, checksum := updateArchive(t, tc.name, tc.kind)
		if tc.corrupt {
			checksum[0] = 'x'
		}
		if err := extractConnector(archive, checksum, filepath.Join(t.TempDir(), "candidate")); err == nil {
			t.Fatalf("accepted %#v", tc)
		}
	}
	archive, checksum := updateArchive(t, "crewfold-connector-linux-amd64/crewfold-connector", tar.TypeReg)
	path := filepath.Join(t.TempDir(), "candidate")
	if err := extractConnector(archive, checksum, path); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "new-binary" {
		t.Fatal(string(got))
	}
}

func TestConnectorUpdateAndRollback(t *testing.T) {
	for _, scenario := range []string{"success", "inactive", "check", "older", "bad-checksum", "bad-config", "bad-version", "bad-url", "restart-failure", "health-failure"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			paths := appdirs.Paths{ConnectorBinaryPath: filepath.Join(root, "crewfold-connector"), ConnectorConfigPath: filepath.Join(root, "connector.json")}
			if err := os.WriteFile(paths.ConnectorBinaryPath, []byte("old-binary"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(paths.ConnectorConfigPath, []byte("unchanged-config"), 0600); err != nil {
				t.Fatal(err)
			}
			archive, checksum := updateArchive(t, "crewfold-connector-linux-amd64/crewfold-connector", tar.TypeReg)
			tag := "v0.6.2"
			if scenario == "older" {
				tag = "v0.6.0"
			}
			base := "https://github.com/SvetlozarValchev/crewfold/releases/download/" + tag + "/"
			assetURL := base + connectorAsset
			if scenario == "bad-url" {
				assetURL = "https://evil.example/" + connectorAsset
			}
			release, _ := json.Marshal(map[string]any{"tag_name": tag, "assets": []map[string]string{{"name": connectorAsset, "browser_download_url": assetURL}, {"name": connectorAsset + ".sha256", "browser_download_url": base + connectorAsset + ".sha256"}}})
			if scenario == "bad-checksum" {
				checksum[0] = 'x'
			}
			restarts := 0
			healthChecks := 0
			u := connectorUpdater{latestURL: connectorReleaseAPI,
				client: &http.Client{Transport: updaterTransport(func(req *http.Request) (*http.Response, error) {
					var content []byte
					switch req.URL.String() {
					case connectorReleaseAPI:
						content = release
					case base + connectorAsset:
						content = archive
					case base + connectorAsset + ".sha256":
						content = checksum
					default:
						t.Fatalf("unexpected request %s", req.URL)
					}
					return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(content))}, nil
				})},
				inspect: func(_ context.Context, path string) (buildinfo.Info, error) {
					v := "v0.6.1"
					if path != paths.ConnectorBinaryPath {
						v = tag
						if scenario == "bad-version" {
							v = "v0.6.99"
						}
					}
					return buildinfo.Info{Version: v, Platform: "linux/amd64"}, nil
				},
				preflight: func(context.Context, string, string) error {
					if scenario == "bad-config" {
						return errors.New("bad config")
					}
					return nil
				},
				service: func(_ context.Context, args ...string) (string, error) {
					if args[0] == "show" {
						if scenario == "inactive" {
							return "inactive", nil
						}
						return "active", nil
					}
					if strings.Join(args, " ") != "restart crewfold-connector.service" {
						t.Fatalf("unexpected service operation %v", args)
					}
					restarts++
					if scenario == "restart-failure" && restarts == 1 {
						return "", errors.New("restart failed")
					}
					return "", nil
				},
				health: func(context.Context, string) error {
					healthChecks++
					if scenario == "health-failure" && healthChecks == 1 {
						return errors.New("unhealthy")
					}
					return nil
				},
			}
			result, err := u.update(context.Background(), paths, scenario == "check")
			wantErr := strings.HasPrefix(scenario, "bad-") || strings.HasSuffix(scenario, "failure")
			if (err != nil) != wantErr {
				t.Fatalf("result=%v err=%v", result, err)
			}
			got, _ := os.ReadFile(paths.ConnectorBinaryPath)
			want := "old-binary"
			if scenario == "success" || scenario == "inactive" {
				want = "new-binary"
			}
			if string(got) != want {
				t.Fatalf("installed %s, want %s", got, want)
			}
			config, _ := os.ReadFile(paths.ConnectorConfigPath)
			if string(config) != "unchanged-config" {
				t.Fatal("modified credentials/bindings")
			}
			wantRestarts := 0
			if scenario == "success" {
				wantRestarts = 1
			}
			if strings.HasSuffix(scenario, "failure") {
				wantRestarts = 2
			}
			if restarts != wantRestarts {
				t.Fatalf("restarts %d want %d", restarts, wantRestarts)
			}
		})
	}
}

func TestDeliveryLockDrainsBeforeUpdate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delivery.lock")
	reader, err := connectorLock(context.Background(), path, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := connectorLock(context.Background(), path, false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if f, err := connectorLock(ctx, path, true); err == nil {
		f.Close()
		t.Fatal("updater interrupted deliveries")
	}
	reader.Close()
	second.Close()
	writer, err := connectorLock(context.Background(), path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel2()
	if f, err := connectorLock(ctx2, path, false); err == nil {
		f.Close()
		t.Fatal("delivery started during replacement")
	}
}

func TestUpdaterRejectsOversizedAndFailedResponses(t *testing.T) {
	for _, status := range []int{200, 403} {
		u := connectorUpdater{client: &http.Client{Transport: updaterTransport(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("too long"))}, nil
		})}}
		if _, err := u.download(context.Background(), "https://example.test", 2); err == nil {
			t.Fatal("accepted response")
		}
	}
}
