package store

import (
	"context"
	"testing"
	"time"

	"crewfold/internal/domain"
)

func TestManagedServiceEmptyCollectionsAreJSONArrays(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, _ := initializeWorkTestProject(t, storage)

	definitions, err := storage.ManagedServiceDefinitions(ctx, ListManagedServiceDefinitionsQuery{WorkspaceIdentifier: workspace.ID})
	if err != nil || definitions == nil || len(definitions) != 0 {
		t.Fatalf("ManagedServiceDefinitions(empty) = %#v, %v", definitions, err)
	}
	instances, err := storage.ManagedServiceInstances(ctx, ListManagedServiceInstancesQuery{WorkspaceIdentifier: workspace.ID})
	if err != nil || instances == nil || len(instances) != 0 {
		t.Fatalf("ManagedServiceInstances(empty) = %#v, %v", instances, err)
	}
	grants, err := storage.ManagedServiceGrants(ctx, ListManagedServiceGrantsQuery{WorkspaceIdentifier: workspace.ID})
	if err != nil || grants == nil || len(grants) != 0 {
		t.Fatalf("ManagedServiceGrants(empty) = %#v, %v", grants, err)
	}
	requests, err := storage.ManagedServiceRequests(ctx, ListManagedServiceRequestsQuery{WorkspaceIdentifier: workspace.ID})
	if err != nil || requests == nil || len(requests) != 0 {
		t.Fatalf("ManagedServiceRequests(empty) = %#v, %v", requests, err)
	}
}

func TestManagedServiceAgentNeedsExactGrantOrCreatesInertOwnerRequest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	inspection, err := storage.InspectProject(ctx, workspace.ID, project.ID)
	if err != nil || len(inspection.Checkouts) != 1 {
		t.Fatalf("InspectProject() = %#v, %v", inspection, err)
	}
	agent := createDomainTestAgent(t, storage, workspace.ID, "service-owner", "arbitrary durable agent")
	attached, err := storage.AttachDomainAgent(ctx, AttachDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.Value.ID,
		OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentAdaptive,
		IdempotencyKey: "attach-service-owner", CorrelationID: "attach-service-owner",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.BindDomainAgentSession(ctx, BindDomainAgentSessionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.Value.ID,
		Provider: "codex", ThreadID: "service-owner-thread", CWD: inspection.Checkouts[0].Path,
	}); err != nil {
		t.Fatal(err)
	}
	definition, err := storage.CreateManagedServiceDefinition(ctx, CreateManagedServiceDefinitionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, CheckoutID: inspection.Checkouts[0].ID,
		Name: "preview", Description: "generic local process", Executable: "/usr/bin/sleep", Arguments: []string{"60"}, WorkingDirectory: ".",
		Profile: "local-process", ProfileRevision: 1, NetworkMode: domain.ManagedServiceNetworkNone,
		Health:        domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthProcess, IntervalMillis: 100, TimeoutMillis: 50},
		RestartPolicy: domain.ManagedServiceRestartNever, StopSignal: domain.ManagedServiceStopSignalTerm, StopGraceMillis: 100,
		OutputByteLimit: 4096, CapacityClass: domain.ManagedServiceCapacityLocalDevelop,
		IdempotencyKey: "define-agent-service", CorrelationID: "define-agent-service",
	})
	if err != nil {
		t.Fatal(err)
	}
	eventsBefore := len(testWorkspaceEvents(t, storage, workspace.ID, 0, 1000))
	request, err := storage.SubmitManagedServiceRequest(ctx, SubmitManagedServiceRequestCommand{
		ThreadID: "service-owner-thread", DefinitionID: definition.Value.ID, ExpectedRevision: definition.Value.Revision,
		Summary: "Start the reviewed local preview for owner inspection.", IdempotencyKey: "request-preview", CorrelationID: "request-preview",
	})
	if err != nil || request.Value.Status != domain.ManagedServiceRequestPending {
		t.Fatalf("SubmitManagedServiceRequest() = %#v, %v", request, err)
	}
	if instances, err := storage.ManagedServiceInstances(ctx, ListManagedServiceInstancesQuery{WorkspaceIdentifier: workspace.ID}); err != nil || len(instances) != 0 {
		t.Fatalf("inert request instances = %#v, %v", instances, err)
	}
	if got := len(testWorkspaceEvents(t, storage, workspace.ID, 0, 1000)); got != eventsBefore+1 {
		t.Fatalf("request event count = %d, want %d", got, eventsBefore+1)
	}
	rejected, err := storage.DecideManagedServiceRequest(ctx, DecideManagedServiceRequestCommand{
		WorkspaceIdentifier: workspace.ID, RequestID: request.Value.ID, ExpectedRevision: request.Value.Revision,
		Reason: "Use a direct bounded grant for this recurring process.", IdempotencyKey: "reject-preview-request", CorrelationID: "reject-preview-request",
	})
	if err != nil || rejected.Value.Request.Status != domain.ManagedServiceRequestRejected || rejected.Value.Instance != nil {
		t.Fatalf("DecideManagedServiceRequest(reject) = %#v, %v", rejected, err)
	}
	expiresAt := storage.clock().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	grant, err := storage.CreateManagedServiceGrant(ctx, CreateManagedServiceGrantCommand{
		WorkspaceIdentifier: workspace.ID, DefinitionID: definition.Value.ID, ExpectedDefinitionRevision: definition.Value.Revision,
		ManagerAgentIdentifier: agent.Value.ID, ExpectedMembershipRevision: attached.Value.Revision,
		Actions: []string{domain.ManagedServiceActionInspect, domain.ManagedServiceActionLogs, domain.ManagedServiceActionStart, domain.ManagedServiceActionStop}, MaximumInstances: 1, ExpiresAt: expiresAt,
		IdempotencyKey: "grant-preview", CorrelationID: "grant-preview",
	})
	if err != nil || grant.Value.ManagerAgentID != agent.Value.ID {
		t.Fatalf("CreateManagedServiceGrant() = %#v, %v", grant, err)
	}
	started, err := storage.StartManagedServiceAsAgent(ctx, "service-owner-thread", grant.Value.ID, grant.Value.Revision, definition.Value.ID, definition.Value.Revision, "agent-start-preview", "agent-start-preview")
	if err != nil || started.Value.Source.Type != domain.ManagedServiceSourceAgent || started.Value.Source.GrantID != grant.Value.ID || started.Value.Source.ThreadID != "service-owner-thread" {
		t.Fatalf("StartManagedServiceAsAgent() = %#v, %v", started, err)
	}
	inspected, err := storage.ManagedServiceDetailAsAgent(ctx, "service-owner-thread", grant.Value.ID, grant.Value.Revision, started.Value.ID, started.Value.Revision, domain.ManagedServiceActionInspect)
	if err != nil || inspected.Instance.ID != started.Value.ID || inspected.Definition.ID != definition.Value.ID || len(inspected.Jobs) != 1 {
		t.Fatalf("ManagedServiceDetailAsAgent(inspect) = %#v, %v", inspected, err)
	}
	logged, err := storage.ManagedServiceDetailAsAgent(ctx, "service-owner-thread", grant.Value.ID, grant.Value.Revision, started.Value.ID, started.Value.Revision, domain.ManagedServiceActionLogs)
	if err != nil || logged.Instance.ID != started.Value.ID {
		t.Fatalf("ManagedServiceDetailAsAgent(logs) = %#v, %v", logged, err)
	}
	if _, err := storage.ManagedServiceDetailAsAgent(ctx, "service-owner-thread", grant.Value.ID, grant.Value.Revision, started.Value.ID, started.Value.Revision, domain.ManagedServiceActionRestart); ErrorCode(err) != CodeInvalidManagedService {
		t.Fatalf("ungranted agent detail error = %v code=%q", err, ErrorCode(err))
	}
	if _, err := storage.StartManagedServiceAsAgent(ctx, "service-owner-thread", "svcgrant_00000000000000000000000000000000", 1, definition.Value.ID, definition.Value.Revision, "ungrafted-start", "ungrafted-start"); ErrorCode(err) != CodeManagedServiceGrantNotFound {
		t.Fatalf("ungranted agent start error = %v code=%q", err, ErrorCode(err))
	}
	listed, err := storage.ManagedServiceGrants(ctx, ListManagedServiceGrantsQuery{WorkspaceIdentifier: workspace.ID, ManagerIdentifier: agent.Value.ID})
	if err != nil || len(listed) != 1 || listed[0].ID != grant.Value.ID {
		t.Fatalf("ManagedServiceGrants() = %#v, %v", listed, err)
	}
	requests, err := storage.ManagedServiceRequests(ctx, ListManagedServiceRequestsQuery{WorkspaceIdentifier: workspace.ID, Status: domain.ManagedServiceRequestRejected})
	if err != nil || len(requests) != 1 || requests[0].ID != request.Value.ID {
		t.Fatalf("ManagedServiceRequests() = %#v, %v", requests, err)
	}
}
