package store

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"crewfold/internal/domain"
)

func TestProjectAndAdjacentCheckoutRegistrationShareRepositoryButNotCheckout(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	workspace := initializeSourceTestWorkspace(t, storage)
	root := t.TempDir()
	firstObservation := sourceTestObservation(filepath.Join(root, "world-engine"), "main")
	first, err := storage.RegisterProject(context.Background(), RegisterProjectCommand{
		WorkspaceIdentifier: workspace.Workspace.ID,
		Name:                "world-engine",
		WriteMode:           domain.WriteModeExclusive,
		IdempotencyKey:      "project-world-engine",
		CorrelationID:       "request-project",
		Observation:         firstObservation,
	})
	if err != nil {
		t.Fatalf("RegisterProject() error = %v", err)
	}
	replayed, err := storage.RegisterProject(context.Background(), RegisterProjectCommand{
		WorkspaceIdentifier: workspace.Workspace.ID,
		Name:                "world-engine",
		WriteMode:           domain.WriteModeExclusive,
		IdempotencyKey:      "project-world-engine",
		CorrelationID:       "request-project",
		Observation:         firstObservation,
	})
	if err != nil || !reflect.DeepEqual(replayed, first) {
		t.Fatalf("RegisterProject(replay) = %#v, %v, want %#v", replayed, err, first)
	}

	adjacentObservation := sourceTestObservation(filepath.Join(root, "world-engine-2"), "agent-two")
	adjacent, err := storage.AddCheckout(context.Background(), AddCheckoutCommand{
		WorkspaceIdentifier: workspace.Workspace.Name,
		ProjectIdentifier:   first.Project.Name,
		WriteMode:           domain.WriteModeClaimed,
		IdempotencyKey:      "checkout-world-engine-2",
		CorrelationID:       "request-checkout",
		Observation:         adjacentObservation,
	})
	if err != nil {
		t.Fatalf("AddCheckout(adjacent clone) error = %v", err)
	}
	if adjacent.RepositoryCreated || adjacent.Repository.ID != first.Repository.ID {
		t.Fatalf("adjacent repository = %#v, created=%t; want existing %s", adjacent.Repository, adjacent.RepositoryCreated, first.Repository.ID)
	}
	if adjacent.Checkout.ID == first.Checkout.ID || adjacent.Checkout.Path == first.Checkout.Path {
		t.Fatalf("adjacent checkout = %#v, want a distinct checkout identity", adjacent.Checkout)
	}
	if adjacent.Checkout.CheckoutKind != domain.CheckoutStandalone {
		t.Fatalf("adjacent checkout kind = %q, want standalone", adjacent.Checkout.CheckoutKind)
	}

	inspection, err := storage.InspectProject(context.Background(), workspace.Workspace.Name, first.Project.ID)
	if err != nil {
		t.Fatalf("InspectProject() error = %v", err)
	}
	if len(inspection.Repositories) != 1 || len(inspection.Checkouts) != 2 {
		t.Fatalf("inspection = %#v, want one repository and two checkouts", inspection)
	}

	_, err = storage.AddCheckout(context.Background(), AddCheckoutCommand{
		WorkspaceIdentifier: workspace.Workspace.ID,
		ProjectIdentifier:   first.Project.ID,
		IdempotencyKey:      "duplicate-path",
		CorrelationID:       "request-duplicate",
		Observation:         adjacentObservation,
	})
	if ErrorCode(err) != CodeCheckoutExists {
		t.Fatalf("AddCheckout(duplicate path) error = %v, code = %q", err, ErrorCode(err))
	}
	events, err := storage.Events(context.Background(), 0, 100)
	if err != nil || len(events) != 5 {
		t.Fatalf("events after duplicate = %d, %v, want 5", len(events), err)
	}
}

func TestUnavailableObservationPreservesCheckoutIdentityAndLastKnownGitState(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	workspace := initializeSourceTestWorkspace(t, storage)
	registered, err := storage.RegisterProject(context.Background(), RegisterProjectCommand{
		WorkspaceIdentifier: workspace.Workspace.ID,
		Name:                "world-engine",
		IdempotencyKey:      "project-world-engine",
		CorrelationID:       "request-project",
		Observation:         sourceTestObservation(filepath.Join(t.TempDir(), "world-engine"), "main"),
	})
	if err != nil {
		t.Fatalf("RegisterProject() error = %v", err)
	}
	unavailable := domain.CheckoutObservation{
		Path:           registered.Checkout.Path,
		Availability:   domain.CheckoutUnavailable,
		CheckoutKind:   domain.CheckoutKindUnknown,
		DiagnosticCode: "checkout_unavailable",
		Diagnostic:     "checkout path no longer exists",
	}
	inspection, err := storage.ApplyCheckoutObservations(context.Background(), workspace.Workspace.ID, registered.Project.ID, "request-inspect", map[string]domain.CheckoutObservation{registered.Checkout.ID: unavailable})
	if err != nil {
		t.Fatalf("ApplyCheckoutObservations() error = %v", err)
	}
	if len(inspection.Checkouts) != 1 {
		t.Fatalf("checkouts = %#v, want one", inspection.Checkouts)
	}
	checkout := inspection.Checkouts[0]
	if checkout.ID != registered.Checkout.ID || checkout.RepositoryID != registered.Repository.ID || checkout.Path != registered.Checkout.Path {
		t.Fatalf("unavailable checkout identity changed: %#v", checkout)
	}
	if checkout.Availability != domain.CheckoutUnavailable || checkout.Revision != 2 || checkout.DiagnosticCode != "checkout_unavailable" {
		t.Fatalf("unavailable checkout = %#v, want revision 2 with diagnosis", checkout)
	}
	if checkout.HeadCommit != registered.Checkout.HeadCommit || checkout.Branch != registered.Checkout.Branch || checkout.CheckoutKind != registered.Checkout.CheckoutKind {
		t.Fatalf("unavailable checkout lost last-known Git state: %#v", checkout)
	}

	again, err := storage.ApplyCheckoutObservations(context.Background(), workspace.Workspace.ID, registered.Project.ID, "request-inspect-again", map[string]domain.CheckoutObservation{registered.Checkout.ID: unavailable})
	if err != nil {
		t.Fatalf("ApplyCheckoutObservations(unchanged) error = %v", err)
	}
	if again.Checkouts[0].Revision != checkout.Revision {
		t.Fatalf("unchanged observation revision = %d, want %d", again.Checkouts[0].Revision, checkout.Revision)
	}
	events, err := storage.Events(context.Background(), 0, 100)
	if err != nil || len(events) != 5 {
		t.Fatalf("events after idempotent observation = %d, %v, want 5", len(events), err)
	}
}

func TestProjectRegistrationFailureRollsBackAllSourceRecords(t *testing.T) {
	t.Parallel()

	injected := errors.New("injected source transaction failure")
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace := initializeSourceTestWorkspace(t, storage)
	storage.mutationHook = func(stage string) error {
		if stage == MutationAfterProjection {
			return injected
		}
		return nil
	}
	_, err := storage.RegisterProject(context.Background(), RegisterProjectCommand{
		WorkspaceIdentifier: workspace.Workspace.ID,
		Name:                "world-engine",
		IdempotencyKey:      "project-world-engine",
		CorrelationID:       "request-project",
		Observation:         sourceTestObservation(filepath.Join(t.TempDir(), "world-engine"), "main"),
	})
	if !errors.Is(err, injected) {
		t.Fatalf("RegisterProject() error = %v, want injected failure", err)
	}
	for _, table := range []string{"projects", "repositories", "project_repositories", "checkouts"} {
		var count int
		if err := storage.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("%s count = %d, want no partial source records", table, count)
		}
	}
	assertCounts(t, storage, 1, 1, 1)
}

func initializeSourceTestWorkspace(t *testing.T, storage *Store) WorkspaceInitResult {
	t.Helper()
	result, err := storage.InitWorkspace(context.Background(), InitWorkspaceCommand{Name: "personal", IdempotencyKey: "workspace-personal", CorrelationID: "request-workspace"})
	if err != nil {
		t.Fatalf("InitWorkspace() error = %v", err)
	}
	return result
}

func sourceTestObservation(path, branch string) domain.CheckoutObservation {
	return domain.CheckoutObservation{
		Path:         path,
		Availability: domain.CheckoutAvailable,
		CheckoutKind: domain.CheckoutStandalone,
		Branch:       branch,
		HeadCommit:   "2222222222222222222222222222222222222222",
		GitDir:       filepath.Join(path, ".git"),
		GitCommonDir: filepath.Join(path, ".git"),
		Repository: domain.RepositoryObservation{
			Fingerprint:  "git_1111111111111111111111111111111111111111111111111111111111111111",
			ObjectFormat: "sha1",
			RootCommits:  []string{"0000000000000000000000000000000000000000"},
		},
	}
}
