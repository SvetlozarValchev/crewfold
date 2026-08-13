package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func TestInvokeManagerDerivesPacketV5AuthorityFromExactGrant(t *testing.T) {
	t.Parallel()
	storage, fixture, profile := createManagerInvocationFixture(t)
	command := InvokeManagerCommand{
		WorkspaceIdentifier:     fixture.workspace.ID,
		ObjectiveID:             fixture.objective.ID,
		TaskID:                  fixture.planning.Task.ID,
		ManagerGrantID:          fixture.grant.ID,
		LaunchProfileID:         profile.ID,
		ExpectedTaskRevision:    fixture.planning.Task.Revision,
		ExpectedGrantRevision:   fixture.grant.Revision,
		ExpectedProfileRevision: profile.Revision,
		IdempotencyKey:          "invoke-arbitrary-label-manager",
		CorrelationID:           "request-invoke-arbitrary-label-manager",
	}
	created, err := storage.InvokeManager(context.Background(), command)
	if err != nil {
		t.Fatalf("InvokeManager() = %v", err)
	}
	replayed, err := storage.InvokeManager(context.Background(), command)
	if err != nil || !reflect.DeepEqual(replayed, created) {
		t.Fatalf("InvokeManager(replay) = %#v, %v; want %#v", replayed, err, created)
	}
	if created.Detail.Run.Status != domain.RunRequested || created.Detail.Run.AgentID != fixture.manager.ID ||
		created.Detail.Run.AssignmentID != fixture.planning.Task.AssignmentID || created.Detail.Run.ContextPacketID == "" {
		t.Fatalf("manager run = %#v", created.Detail.Run)
	}
	packet, err := storage.ContextPacket(context.Background(), fixture.workspace.ID, created.Detail.Run.ContextPacketID)
	if err != nil {
		t.Fatalf("ContextPacket() = %v", err)
	}
	if packet.Schema != domain.ContextPacketSchemaV5 || packet.ManagementGrant == nil ||
		packet.ManagementGrant.GrantID != fixture.grant.ID || packet.ManagementGrant.ManagerTaskID != fixture.planning.Task.ID ||
		packet.ManagementGrant.ManagerAgentID != fixture.manager.ID {
		t.Fatalf("manager packet authority = %#v", packet)
	}
	if fixture.manager.Role != "constellation cartographer" {
		t.Fatalf("authority test accidentally depends on a special role: %q", fixture.manager.Role)
	}
	wantTools := append(append([]string(nil), runScopedTools...), "crewfold_propose_assignment")
	if !reflect.DeepEqual(packet.Policy.AllowedTools, wantTools) {
		t.Fatalf("allowed tools = %#v, want %#v", packet.Policy.AllowedTools, wantTools)
	}
	var runCount, jobCount, bindingCount int
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM runs WHERE assignment_id=?", fixture.planning.Task.AssignmentID).Scan(&runCount); err != nil || runCount != 1 {
		t.Fatalf("manager run count = %d, %v; want one", runCount, err)
	}
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM run_jobs WHERE run_id=? AND status='pending' AND origin='owner'", created.Detail.Run.ID).Scan(&jobCount); err != nil || jobCount != 1 {
		t.Fatalf("manager job count = %d, %v; want one owner-origin job", jobCount, err)
	}
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM run_context_bindings WHERE run_id=? AND context_packet_id=?", created.Detail.Run.ID, packet.ID).Scan(&bindingCount); err != nil || bindingCount != 1 {
		t.Fatalf("manager binding count = %d, %v; want one", bindingCount, err)
	}

	changed := command
	changed.ExpectedProfileRevision++
	if _, err := storage.InvokeManager(context.Background(), changed); ErrorCode(err) != CodeIdempotencyConflict {
		t.Fatalf("InvokeManager(changed replay) = %v, code %q; want idempotency conflict", err, ErrorCode(err))
	}
}

func TestInvokeManagerRollsBackPacketRunAndJob(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{MutationAfterProjection, MutationAfterEvent} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			storage, fixture, profile := createManagerInvocationFixture(t)
			storage.mutationHook = func(current string) error {
				if current == stage {
					return errors.New("injected manager invocation failure")
				}
				return nil
			}
			_, err := storage.InvokeManager(context.Background(), InvokeManagerCommand{
				WorkspaceIdentifier: fixture.workspace.ID, ObjectiveID: fixture.objective.ID,
				TaskID: fixture.planning.Task.ID, ManagerGrantID: fixture.grant.ID, LaunchProfileID: profile.ID,
				ExpectedTaskRevision: fixture.planning.Task.Revision, ExpectedGrantRevision: fixture.grant.Revision,
				ExpectedProfileRevision: profile.Revision, IdempotencyKey: "invoke-rollback-" + stage,
				CorrelationID: "request-invoke-rollback-" + stage,
			})
			if ErrorCode(err) != CodeStorageFailed {
				t.Fatalf("InvokeManager(%s) = %v, code %q; want storage_failed", stage, err, ErrorCode(err))
			}
			storage.mutationHook = nil
			for name, query := range map[string]string{
				"runs":    "SELECT COUNT(*) FROM runs WHERE assignment_id=?",
				"packets": "SELECT COUNT(*) FROM context_packets WHERE task_id=? AND json_extract(packet_json,'$.schema')='urn:crewfold:schema:domain:context-packet:v5'",
				"jobs":    "SELECT COUNT(*) FROM run_jobs WHERE run_id IN (SELECT id FROM runs WHERE assignment_id=?)",
			} {
				var count int
				if scanErr := storage.db.QueryRow(query, fixture.planning.Task.AssignmentID).Scan(&count); scanErr != nil || count != 0 {
					t.Fatalf("%s after rollback = %d, %v; want zero", name, count, scanErr)
				}
			}
		})
	}
}

func createManagerInvocationFixture(t *testing.T) (*Store, managerGrantAdversarialFixture, domain.LaunchProfile) {
	t.Helper()
	storage, fixture := createManagerGrantAdversarialFixture(t)
	// Exercise the valid empty claim-kind authority set. This previously became
	// JSON null while building packet v5 and was rejected by the SQL authority
	// seal, even though the immutable grant correctly stored canonical JSON [].
	grantResult, err := storage.CreateManagerGrant(context.Background(), CreateManagerGrantCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID,
		ObjectiveID: fixture.objective.ID, TaskID: fixture.planning.Task.ID,
		AgentIdentifier: fixture.manager.ID, ExpectedTaskRevision: fixture.planning.Task.Revision,
		ExpectedAgentRevision: fixture.manager.Revision,
		ProposalKinds:         []string{domain.ManagerProposalAssignment},
		LaunchProfileIDs:      []string{fixture.target.ID},
		AllowedClaimKinds:     []string{},
		Limits: domain.ManagerProposalLimits{
			MaxOpenProposals: 2, MaxActions: 2, MaxTasks: 1, MaxDependencies: 1,
			MaxClaimRequirements: 1, Budget: domain.Budget{TokenLimit: 1000, CostCents: 100, TimeSeconds: 600},
		},
		IdempotencyKey: "manager-invocation-empty-claims-grant", CorrelationID: "request-manager-invocation-empty-claims-grant",
	})
	if err != nil {
		t.Fatalf("CreateManagerGrant(empty claims) = %v", err)
	}
	fixture.grant = grantResult.Value
	scenario := domain.FakeScenario{
		Schema: execution.FakeScenarioSchema,
		Name:   "arbitrary-label-manager-plan",
		Steps:  []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "propose bounded work"}},
	}
	profile, err := storage.CreateLaunchProfile(context.Background(), CreateLaunchProfileCommand{
		WorkspaceIdentifier: fixture.workspace.ID, ProjectIdentifier: fixture.project.ID,
		AgentIdentifier: fixture.manager.ID, ExpectedAgentRevision: fixture.manager.Revision,
		Runtime: fixture.manager.Runtime, Provider: fixture.manager.Provider, Scenario: scenario,
		AssignmentLeaseSeconds: 900, CapabilityTTLSeconds: 900, ManagerGrantID: fixture.grant.ID,
		IdempotencyKey: "manager-invocation-profile", CorrelationID: "request-manager-invocation-profile",
	})
	if err != nil {
		t.Fatalf("CreateLaunchProfile(manager) = %v", err)
	}
	return storage, fixture, profile.Value
}
