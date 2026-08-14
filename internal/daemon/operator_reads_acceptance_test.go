package daemon

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestM19ProjectShowAndListDoNotObserveGitOrAppendEvents(t *testing.T) {
	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	config := testConfig(t)
	config.DisableRunWorker = true
	config.DisableCheckWorker = true
	config.DisableCheckWatcher = true
	config.DisableClaimWatcher = true
	config.DisableSupervisor = true
	config.DisableLeaseReconciler = true

	running := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	ctx := context.Background()
	workspace, err := client.WorkspaceInit(ctx, "personal", "m19-project-read-workspace")
	if err != nil {
		t.Fatal(err)
	}
	project, err := client.ProjectAdd(ctx, workspace.Workspace.ID, "world-engine", filepath.Join(fixtureRoot, "world-engine"), domain.WriteModeExclusive, "m19-project-read-project")
	if err != nil {
		t.Fatal(err)
	}
	before, err := client.EventsList(ctx, localapi.EventsListParams{
		Workspace:  workspace.Workspace.ID,
		PageParams: localapi.PageParams{Limit: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}

	// A Git-refreshing inspection would now observe this new checkout state and
	// append an event. Project resolution for the UI must remain a projection read.
	if err := os.WriteFile(filepath.Join(fixtureRoot, "world-engine", "untracked-m19.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, identifier := range []string{project.Project.ID, project.Project.Name} {
		shown, showErr := client.ProjectShow(ctx, workspace.Workspace.ID, identifier)
		if showErr != nil || !reflect.DeepEqual(shown.Project, project.Project) {
			t.Fatalf("ProjectShow(%q) = %#v, %v; want unchanged canonical project %#v", identifier, shown, showErr, project.Project)
		}
	}
	listed, err := client.ProjectList(ctx, localapi.ProjectListParams{
		Workspace:  workspace.Workspace.ID,
		PageParams: localapi.PageParams{Limit: 50},
	})
	if err != nil || listed.Total != 1 || len(listed.Projects) != 1 || !reflect.DeepEqual(listed.Projects[0], project.Project) {
		t.Fatalf("ProjectList() = %#v, %v; want one unchanged canonical project", listed, err)
	}
	after, err := client.EventsList(ctx, localapi.EventsListParams{
		Workspace:  workspace.Workspace.ID,
		PageParams: localapi.PageParams{Limit: 1000},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("pure project resolution changed the event journal:\nbefore %#v\nafter  %#v", before, after)
	}

	if _, err := client.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := running.wait(); err != nil {
		t.Fatal(err)
	}
}
