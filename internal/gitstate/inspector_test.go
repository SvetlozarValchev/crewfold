package gitstate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
)

func TestInspectorSupportsAdjacentClonesAndLinkedWorktreesWithoutMutation(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}

	fixtureRoot := t.TempDir()
	createFixture(t, fixtureRoot)
	paths := []string{
		filepath.Join(fixtureRoot, "world-engine"),
		filepath.Join(fixtureRoot, "world-engine-2"),
		filepath.Join(fixtureRoot, "world-engine-5"),
		filepath.Join(fixtureRoot, "world-engine-linked"),
	}
	hook := filepath.Join(t.TempDir(), "fsmonitor-hook")
	marker := hook + ".invoked"
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf invoked >\"$0.invoked\"\n"), 0o700); err != nil {
		t.Fatalf("os.WriteFile(fsmonitor hook) error = %v", err)
	}
	configure := exec.Command("git", "-C", paths[0], "config", "core.fsmonitor", hook)
	if output, err := configure.CombinedOutput(); err != nil {
		t.Fatalf("configure hostile fsmonitor: %v\n%s", err, output)
	}
	control := exec.Command("git", "-C", paths[0], "status", "--porcelain=v2")
	if output, err := control.CombinedOutput(); err != nil {
		t.Fatalf("run fsmonitor control status: %v\n%s", err, output)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("control status did not invoke configured fsmonitor hook: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatalf("os.Remove(fsmonitor marker) error = %v", err)
	}

	before := treeDigest(t, fixtureRoot)
	runner := &recordingRunner{delegate: ExecRunner{Executable: "git"}}
	inspector := NewInspectorWithRunner(runner)
	observations := make([]string, 0, len(paths))
	for index, path := range paths {
		observation, err := inspector.Inspect(context.Background(), path)
		if err != nil {
			t.Fatalf("Inspect(%s) error = %v", path, err)
		}
		if observation.Availability != "available" || observation.Dirty {
			t.Fatalf("observation[%d] = %#v, want clean and available", index, observation)
		}
		if index < 3 && observation.CheckoutKind != "standalone" {
			t.Fatalf("observation[%d].CheckoutKind = %q, want standalone", index, observation.CheckoutKind)
		}
		if index == 3 && observation.CheckoutKind != "linked_worktree" {
			t.Fatalf("linked observation kind = %q, want linked_worktree", observation.CheckoutKind)
		}
		observations = append(observations, observation.Repository.Fingerprint)
	}
	for index, fingerprint := range observations[1:] {
		if fingerprint != observations[0] {
			t.Fatalf("checkout %d fingerprint = %q, want shared history fingerprint %q", index+1, fingerprint, observations[0])
		}
	}
	after := treeDigest(t, fixtureRoot)
	if before != after {
		t.Fatalf("fixture content/mode digest changed during read-only inspection: before %s, after %s", before, after)
	}

	allowed := map[string]bool{"rev-parse": true, "rev-list": true, "status": true}
	for _, arguments := range runner.calls {
		verbIndex := gitVerbIndex(arguments)
		if verbIndex < 0 || arguments[0] != "--no-optional-locks" {
			t.Fatalf("unsafe Git invocation: %q", arguments)
		}
		if !allowed[arguments[verbIndex]] {
			t.Fatalf("mutating or unknown Git command invoked: %q", arguments)
		}
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repository-configured fsmonitor hook was invoked: %v", err)
	}
}

func gitVerbIndex(arguments []string) int {
	for index, argument := range arguments {
		if argument == "-C" && index+2 < len(arguments) {
			return index + 2
		}
	}
	return -1
}

func TestInspectorNormalizesSubdirectoryAndDetectsDirtyState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}

	fixtureRoot := t.TempDir()
	createFixture(t, fixtureRoot)
	checkout := filepath.Join(fixtureRoot, "world-engine")
	subdirectory := filepath.Join(checkout, "nested", "source")
	if err := os.MkdirAll(subdirectory, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(subdirectory) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(subdirectory, "untracked.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile(untracked) error = %v", err)
	}

	inspector := NewInspector()
	observation, err := inspector.Inspect(context.Background(), subdirectory)
	if err != nil {
		t.Fatalf("Inspect(subdirectory) error = %v", err)
	}
	if observation.Path != checkout || !observation.Dirty {
		t.Fatalf("observation = %#v, want normalized dirty checkout %s", observation, checkout)
	}
}

func TestInspectorClassifiesUnavailableNonRepositoryAndMalformedOutput(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := NewInspector().Inspect(context.Background(), missing); ErrorCode(err) != CodeCheckoutUnavailable {
		t.Fatalf("Inspect(missing) error = %v, code = %q", err, ErrorCode(err))
	}

	nonRepository := t.TempDir()
	if _, err := NewInspector().Inspect(context.Background(), nonRepository); ErrorCode(err) != CodeNotGitRepository {
		t.Fatalf("Inspect(non-repository) error = %v, code = %q", err, ErrorCode(err))
	}

	if _, err := NewInspectorWithRunner(ExecRunner{Executable: filepath.Join(t.TempDir(), "missing-git")}).Inspect(context.Background(), nonRepository); ErrorCode(err) != CodeGitUnavailable {
		t.Fatalf("Inspect(missing Git) error = %v, code = %q", err, ErrorCode(err))
	}

	malformed := &sequenceRunner{responses: [][]byte{[]byte("true\n"), []byte(filepath.Join(t.TempDir(), "not-created") + "\n")}}
	if _, err := NewInspectorWithRunner(malformed).Inspect(context.Background(), nonRepository); ErrorCode(err) != CodeGitOutputInvalid {
		t.Fatalf("Inspect(malformed output) error = %v, code = %q", err, ErrorCode(err))
	}
}

type recordingRunner struct {
	delegate Runner
	calls    [][]string
}

func (r *recordingRunner) Run(ctx context.Context, arguments ...string) ([]byte, error) {
	r.calls = append(r.calls, append([]string(nil), arguments...))
	return r.delegate.Run(ctx, arguments...)
}

type sequenceRunner struct {
	responses [][]byte
	index     int
}

func (r *sequenceRunner) Run(context.Context, ...string) ([]byte, error) {
	if r.index >= len(r.responses) {
		return nil, errors.New("unexpected Git invocation")
	}
	response := r.responses[r.index]
	r.index++
	return response, nil
}

func createFixture(t *testing.T, root string) {
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

func treeDigest(t *testing.T, root string) string {
	t.Helper()
	var paths []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		paths = append(paths, path)
		return nil
	}); err != nil {
		t.Fatalf("filepath.WalkDir(%s) error = %v", root, err)
	}
	sort.Strings(paths)
	digest := sha256.New()
	for _, path := range paths {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("os.Lstat(%s) error = %v", path, err)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatalf("filepath.Rel(%s) error = %v", path, err)
		}
		_, _ = digest.Write([]byte(relative + "\x00" + info.Mode().String() + "\x00"))
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				t.Fatalf("os.Readlink(%s) error = %v", path, err)
			}
			_, _ = digest.Write([]byte(target))
		case info.Mode().IsRegular():
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("os.ReadFile(%s) error = %v", path, err)
			}
			_, _ = digest.Write(data)
		}
	}
	return hex.EncodeToString(digest.Sum(nil))
}
