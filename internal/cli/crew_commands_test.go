package cli

import (
	"encoding/json"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestM21CrewCommandsExposeExactSecondaryConfigurationSurface(t *testing.T) {
	client := &fakeDaemonClient{ownerCrewMutation: localapi.OwnerCrewMutationResult{
		Schema: localapi.OwnerCrewMutationSchema, Type: "owner_crew_mutation", Action: "add",
		Binding: domain.OwnerExecutiveBinding{Revision: 4}, Agent: domain.AgentDefinition{ID: "agent_11111111111111111111111111111111", Name: "reviewer", Enabled: true},
		WorkerProfiles: []domain.LaunchProfile{{ID: "lprof_11111111111111111111111111111111"}}, EventSequence: 42,
	}, workspaceShow: localapi.WorkspaceShowResult{Workspace: domain.Workspace{ID: "ws_11111111111111111111111111111111", Name: "personal"}},
		projectInspect: localapi.ProjectInspectResult{Project: domain.Project{ID: "prj_11111111111111111111111111111111", WorkspaceID: "ws_11111111111111111111111111111111", Name: "demo"}}}
	run := func(args []string) localapi.OwnerCrewMutationResult {
		t.Helper()
		app, stdout, stderr := newTestApp()
		app.newClient = func(socket string) daemonClient {
			if socket != "/tmp/crew.sock" {
				t.Fatalf("socket = %q", socket)
			}
			return client
		}
		if code := app.Run(args); code != ExitOK {
			t.Fatalf("Run(%q) = %d; stderr=%q", args, code, stderr.String())
		}
		var result localapi.OwnerCrewMutationResult
		if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
			t.Fatalf("decode output = %v; output=%q", err, stdout.String())
		}
		return result
	}

	added := run([]string{"crew", "add", "reviewer", "--workspace", "personal", "--project", "demo", "--provider", "codex", "--runtime", "herdr", "--max-concurrency", "2", "--expected-binding-revision", "3", "--idempotency-key", "crew-add", "--socket", "/tmp/crew.sock", "--output=json"})
	if added.Schema != localapi.OwnerCrewMutationSchema || client.ownerCrewParams != (localapi.OwnerCrewConfigureParams{Workspace: "ws_11111111111111111111111111111111", Project: "prj_11111111111111111111111111111111", Action: "add", ExpectedBindingRevision: 3, Name: "reviewer", Provider: "codex", Runtime: "herdr", MaxConcurrency: 2, IdempotencyKey: "crew-add"}) {
		t.Fatalf("add result/params = %#v / %#v", added, client.ownerCrewParams)
	}

	client.ownerCrewMutation.Action = "disable"
	disabled := run([]string{"crew", "disable", "agent_11111111111111111111111111111111", "--workspace", "personal", "--project", "demo", "--expected-binding-revision", "4", "--idempotency-key", "crew-disable", "--socket", "/tmp/crew.sock", "--output=json"})
	if disabled.Action != "disable" || client.ownerCrewParams != (localapi.OwnerCrewConfigureParams{Workspace: "ws_11111111111111111111111111111111", Project: "prj_11111111111111111111111111111111", Action: "disable", ExpectedBindingRevision: 4, Agent: "agent_11111111111111111111111111111111", IdempotencyKey: "crew-disable"}) {
		t.Fatalf("disable result/params = %#v / %#v", disabled, client.ownerCrewParams)
	}
}
