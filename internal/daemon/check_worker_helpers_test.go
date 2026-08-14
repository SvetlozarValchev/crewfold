package daemon

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/store"
)

type fixedCheckGitInspector struct {
	observation domain.CheckoutObservation
	err         error
}

func (i fixedCheckGitInspector) Inspect(context.Context, string) (domain.CheckoutObservation, error) {
	return i.observation, i.err
}

func TestCheckRuntimeEnvironmentExcludesProviderAndCapabilityAuthority(t *testing.T) {
	t.Parallel()

	environment := checkRuntimeEnvironment([]string{
		"PATH=/bin", "LANG=C.UTF-8", "LC_MESSAGES=C", "TMPDIR=/tmp", "TZ=UTC",
		"CLAUDE_CONFIG_DIR=/secret/claude", "CODEX_HOME=/secret/codex",
		"CREWFOLD_MCP_SOCKET=/secret/socket", "CREWFOLD_MCP_CAPABILITY_FILE=/secret/token",
		"AWS_SECRET_ACCESS_KEY=secret", "HOME=/secret/home", "MALFORMED",
	})
	sort.Strings(environment)
	want := []string{"LANG=C.UTF-8", "LC_MESSAGES=C", "PATH=/bin", "TMPDIR=/tmp", "TZ=UTC"}
	sort.Strings(want)
	if !reflect.DeepEqual(environment, want) {
		t.Fatalf("checkRuntimeEnvironment() = %#v; want %#v", environment, want)
	}
}

func TestResolveCheckWorkingDirectoryRejectsSymlinkEscape(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink containment uses Unix checkout paths")
	}

	parent := t.TempDir()
	checkout := filepath.Join(parent, "checkout")
	inside := filepath.Join(checkout, "nested", "work")
	outside := filepath.Join(parent, "outside")
	for _, path := range []string{inside, outside} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
	}

	resolved, err := resolveCheckWorkingDirectory(checkout, "nested/work")
	if err != nil || resolved != inside {
		t.Fatalf("resolveCheckWorkingDirectory(in-tree) = %q, %v; want %q", resolved, err, inside)
	}
	if err := os.Symlink(inside, filepath.Join(checkout, "inside-link")); err != nil {
		t.Fatalf("Symlink(inside) error = %v", err)
	}
	resolved, err = resolveCheckWorkingDirectory(checkout, "inside-link")
	if err != nil || resolved != inside {
		t.Fatalf("resolveCheckWorkingDirectory(in-tree symlink) = %q, %v; want %q", resolved, err, inside)
	}
	if err := os.Symlink(outside, filepath.Join(checkout, "escape")); err != nil {
		t.Fatalf("Symlink(outside) error = %v", err)
	}
	if resolved, err := resolveCheckWorkingDirectory(checkout, "escape"); err == nil || resolved != "" {
		t.Fatalf("resolveCheckWorkingDirectory(escape) = %q, %v; want rejection", resolved, err)
	}
}

func TestResolveCheckWorkingDirectoryCanonicalizesCheckoutRootSymlink(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlink containment uses Unix checkout paths")
	}

	parent := t.TempDir()
	realCheckout := filepath.Join(parent, "real-checkout")
	working := filepath.Join(realCheckout, "work")
	if err := os.MkdirAll(working, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	checkoutLink := filepath.Join(parent, "checkout-link")
	if err := os.Symlink(realCheckout, checkoutLink); err != nil {
		t.Fatalf("Symlink(checkout root) error = %v", err)
	}
	resolved, err := resolveCheckWorkingDirectory(checkoutLink, "work")
	if err != nil || resolved != working {
		t.Fatalf("resolveCheckWorkingDirectory(linked root) = %q, %v; want %q", resolved, err, working)
	}
}

func TestObserveCheckGitTurnsOversizedDirtySetIntoUnavailableObservation(t *testing.T) {
	t.Parallel()
	paths := make([]string, maximumCheckObservationDirtyPaths+1)
	for index := range paths {
		paths[index] = "file-" + strings.Repeat("0", 4) + string(rune('a'+index%26))
	}
	server := &server{gitInspector: fixedCheckGitInspector{observation: domain.CheckoutObservation{
		Repository: domain.RepositoryObservation{Fingerprint: "fingerprint", ObjectFormat: "sha1"},
		HeadCommit: strings.Repeat("a", 40), Dirty: true, DirtyPaths: paths,
	}}}
	work := storeCheckWorkForObservationTest()
	observation := server.observeCheckGit(context.Background(), work)
	if observation.Available || observation.DiagnosticCode != "dirty_path_bound_exceeded" || len(observation.DirtyPaths) != 0 {
		t.Fatalf("oversized observation = %#v; want bounded unavailable diagnosis", observation)
	}
}

func TestObserveCheckGitCanonicalizesDirtyPathOrder(t *testing.T) {
	t.Parallel()
	server := &server{gitInspector: fixedCheckGitInspector{observation: domain.CheckoutObservation{
		Repository: domain.RepositoryObservation{Fingerprint: "fingerprint", ObjectFormat: "sha1"},
		HeadCommit: strings.Repeat("a", 40), Dirty: true, DirtyPaths: []string{"z.go", "a.go"},
	}}}
	observation := server.observeCheckGit(context.Background(), storeCheckWorkForObservationTest())
	if !observation.Available || !reflect.DeepEqual(observation.DirtyPaths, []string{"a.go", "z.go"}) {
		t.Fatalf("canonical observation = %#v", observation)
	}
}

func TestObserveCheckGitCandidateUsesFrozenIdentityForUnavailableObservation(t *testing.T) {
	t.Parallel()
	server := &server{gitInspector: fixedCheckGitInspector{observation: domain.CheckoutObservation{
		Repository: domain.RepositoryObservation{Fingerprint: "different", ObjectFormat: "sha1"},
		Branch:     "main", HeadCommit: strings.Repeat("a", 40), DirtyPaths: []string{},
	}}}
	candidate := store.CheckWatchCandidate{
		CheckResultID: "checkresult_00000000000000000000000000000000", FreshnessRevision: 3,
		RepositoryID: "repo_00000000000000000000000000000000", RepositoryFingerprint: "frozen",
		ObjectFormat: "sha1", CheckoutID: "co_00000000000000000000000000000000", CheckoutPath: "/checkout",
	}
	observation := server.observeCheckGitCandidate(context.Background(), candidate)
	if observation.Available || observation.RepositoryID != candidate.RepositoryID || observation.ObjectFormat != candidate.ObjectFormat ||
		observation.CheckoutID != candidate.CheckoutID || observation.DiagnosticCode != "repository_identity_changed" || observation.DirtyPaths == nil {
		t.Fatalf("candidate unavailable observation = %#v", observation)
	}
}

func TestObserveCheckGitTurnsMalformedTextIntoUnavailableObservation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		branch     string
		dirtyPaths []string
		diagnostic string
	}{
		{name: "invalid UTF-8 branch", branch: string([]byte{0xff}), diagnostic: "branch_invalid"},
		{name: "NUL branch", branch: "main\x00forged", diagnostic: "branch_invalid"},
		{name: "oversized branch", branch: strings.Repeat("b", 1025), diagnostic: "branch_invalid"},
		{name: "dot dirty path", dirtyPaths: []string{"."}, diagnostic: "dirty_path_invalid"},
		{name: "parent dirty path", dirtyPaths: []string{"../outside"}, diagnostic: "dirty_path_invalid"},
		{name: "unclean dirty path", dirtyPaths: []string{"dir/../file"}, diagnostic: "dirty_path_invalid"},
		{name: "absolute dirty path", dirtyPaths: []string{"/outside"}, diagnostic: "dirty_path_invalid"},
		{name: "backslash dirty path", dirtyPaths: []string{`dir\file`}, diagnostic: "dirty_path_invalid"},
		{name: "NUL dirty path", dirtyPaths: []string{"file\x00forged"}, diagnostic: "dirty_path_invalid"},
		{name: "invalid UTF-8 dirty path", dirtyPaths: []string{string([]byte{0xff})}, diagnostic: "dirty_path_invalid"},
		{name: "oversized dirty path", dirtyPaths: []string{strings.Repeat("p", 1025)}, diagnostic: "dirty_path_invalid"},
		{name: "duplicate dirty path", dirtyPaths: []string{"same", "same"}, diagnostic: "dirty_path_invalid"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := &server{gitInspector: fixedCheckGitInspector{observation: domain.CheckoutObservation{
				Repository: domain.RepositoryObservation{Fingerprint: "fingerprint", ObjectFormat: "sha1"},
				Branch:     test.branch, HeadCommit: strings.Repeat("a", 40), Dirty: len(test.dirtyPaths) != 0,
				DirtyPaths: append([]string{}, test.dirtyPaths...),
			}}}
			observation := server.observeCheckGit(context.Background(), storeCheckWorkForObservationTest())
			if observation.Available || observation.DiagnosticCode != test.diagnostic || len(observation.DirtyPaths) != 0 {
				t.Fatalf("malformed observation = %#v; want unavailable %q", observation, test.diagnostic)
			}
		})
	}
}

func TestObserveCheckGitTurnsMalformedHeadAndDirtyStateIntoUnavailableObservation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		headCommit string
		dirty      bool
		dirtyPaths []string
		diagnostic string
	}{
		{name: "short HEAD", headCommit: strings.Repeat("a", 39), diagnostic: "head_commit_invalid"},
		{name: "uppercase HEAD", headCommit: strings.Repeat("A", 40), diagnostic: "head_commit_invalid"},
		{name: "nonhex HEAD", headCommit: strings.Repeat("g", 40), diagnostic: "head_commit_invalid"},
		{name: "clean with paths", headCommit: strings.Repeat("a", 40), dirtyPaths: []string{"file"}, diagnostic: "dirty_state_invalid"},
		{name: "dirty without paths", headCommit: strings.Repeat("a", 40), dirty: true, diagnostic: "dirty_state_invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := &server{gitInspector: fixedCheckGitInspector{observation: domain.CheckoutObservation{
				Repository: domain.RepositoryObservation{Fingerprint: "fingerprint", ObjectFormat: "sha1"},
				Branch:     "main", HeadCommit: test.headCommit, Dirty: test.dirty, DirtyPaths: append([]string{}, test.dirtyPaths...),
			}}}
			observation := server.observeCheckGit(context.Background(), storeCheckWorkForObservationTest())
			if observation.Available || observation.DiagnosticCode != test.diagnostic || len(observation.DirtyPaths) != 0 {
				t.Fatalf("malformed observation = %#v; want unavailable %q", observation, test.diagnostic)
			}
		})
	}
}

func TestBoundedCheckDiagnosticPreservesUTF8AtByteBoundary(t *testing.T) {
	t.Parallel()
	value := boundedCheckDiagnostic(strings.Repeat("a", 4095) + "€")
	if len(value) > 4096 || !utf8.ValidString(value) {
		t.Fatalf("bounded diagnostic length=%d valid=%v", len(value), utf8.ValidString(value))
	}
}

func storeCheckWorkForObservationTest() store.CheckWork {
	return store.CheckWork{
		Checkout:   domain.Checkout{ID: "co_00000000000000000000000000000000", Path: "/checkout"},
		Repository: domain.Repository{ID: "repo_00000000000000000000000000000000", Fingerprint: "fingerprint", ObjectFormat: "sha1"},
	}
}
