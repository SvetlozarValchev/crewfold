package store

import (
	"context"
	"fmt"
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

func TestAgentAuthoredManagedServiceProposalIsInertUntilOneOwnerDecision(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	inspection, err := storage.InspectProject(ctx, workspace.ID, project.ID)
	if err != nil || len(inspection.Checkouts) != 1 {
		t.Fatalf("InspectProject() = %#v, %v", inspection, err)
	}
	agent := createDomainTestAgent(t, storage, workspace.ID, "process-coordinator", "durable process coordinator")
	attached, err := storage.AttachDomainAgent(ctx, AttachDomainAgentCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.Value.ID,
		OperatingCharter: testDomainAgentCharter, DelegationPolicy: domain.DomainAgentAdaptive,
		IdempotencyKey: "attach-process-coordinator", CorrelationID: "attach-process-coordinator",
	})
	if err != nil {
		t.Fatal(err)
	}
	const threadID = "process-coordinator-thread"
	if _, err := storage.BindDomainAgentSession(ctx, BindDomainAgentSessionCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.Value.ID,
		Provider: "codex", ThreadID: threadID, CWD: inspection.Checkouts[0].Path,
	}); err != nil {
		t.Fatal(err)
	}
	create := func(name, key string) MutationResult[domain.ManagedServiceDefinition] {
		result, createErr := storage.CreateManagedServiceDefinitionAsAgent(ctx, threadID, CreateManagedServiceDefinitionCommand{
			WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, CheckoutID: inspection.Checkouts[0].ID,
			Name: name, Description: "agent-inspected local preview", Executable: "/usr/bin/sleep", Arguments: []string{"60"}, WorkingDirectory: ".",
			Profile: "local-process", ProfileRevision: 1, NetworkMode: domain.ManagedServiceNetworkNone,
			Health:        domain.ManagedServiceHealthCheck{Type: domain.ManagedServiceHealthProcess, IntervalMillis: 100, TimeoutMillis: 50},
			RestartPolicy: domain.ManagedServiceRestartNever, StopSignal: domain.ManagedServiceStopSignalTerm, StopGraceMillis: 100,
			OutputByteLimit: 4096, CapacityClass: domain.ManagedServiceCapacityLocalDevelop,
			IdempotencyKey: key, CorrelationID: key,
		})
		if createErr != nil {
			t.Fatal(createErr)
		}
		if result.Value.CreatedBy != agent.Value.ID || result.Value.UpdatedBy != agent.Value.ID {
			t.Fatalf("agent-authored definition actors = %q/%q", result.Value.CreatedBy, result.Value.UpdatedBy)
		}
		return result
	}

	definition := create("agent-preview", "agent-preview-definition")
	request, err := storage.SubmitManagedServiceRequest(ctx, SubmitManagedServiceRequestCommand{
		ThreadID: threadID, DefinitionID: definition.Value.ID, ExpectedRevision: definition.Value.Revision,
		Summary: "Run the exact inspected preview so the owner can test it.", IdempotencyKey: "agent-preview-request", CorrelationID: "agent-preview-request",
	})
	if err != nil {
		t.Fatal(err)
	}
	if instances, listErr := storage.ManagedServiceInstances(ctx, ListManagedServiceInstancesQuery{WorkspaceIdentifier: workspace.ID}); listErr != nil || len(instances) != 0 {
		t.Fatalf("proposal started before review: %#v, %v", instances, listErr)
	}
	accepted, err := storage.DecideManagedServiceRequest(ctx, DecideManagedServiceRequestCommand{
		WorkspaceIdentifier: workspace.ID, RequestID: request.Value.ID, ExpectedRevision: request.Value.Revision, Accept: true,
		Reason: "Owner reviewed the exact process contract.", IdempotencyKey: "accept-agent-preview", CorrelationID: "accept-agent-preview",
	})
	if err != nil {
		t.Fatal(err)
	}
	if accepted.Value.Instance == nil || accepted.Value.Grant == nil {
		t.Fatalf("accepted proposal = %#v, want instance and grant", accepted.Value)
	}
	if accepted.Value.Grant.ManagerAgentID != agent.Value.ID || accepted.Value.Grant.ManagerMembershipRevision != attached.Value.Revision || accepted.Value.Grant.MaximumInstances != 1 {
		t.Fatalf("accepted grant = %#v", accepted.Value.Grant)
	}
	wantActions := []string{"inspect", "logs", "restart", "start", "stop"}
	if fmt.Sprint(accepted.Value.Grant.Actions) != fmt.Sprint(wantActions) {
		t.Fatalf("accepted grant actions = %v, want %v", accepted.Value.Grant.Actions, wantActions)
	}
	if accepted.Value.Instance.Source.Type != domain.ManagedServiceSourceRequest || accepted.Value.Instance.Source.RequestID != request.Value.ID {
		t.Fatalf("accepted instance source = %#v", accepted.Value.Instance.Source)
	}

	rejectedDefinition := create("rejected-preview", "rejected-preview-definition")
	rejectedRequest, err := storage.SubmitManagedServiceRequest(ctx, SubmitManagedServiceRequestCommand{
		ThreadID: threadID, DefinitionID: rejectedDefinition.Value.ID, ExpectedRevision: rejectedDefinition.Value.Revision,
		Summary: "A second exact preview candidate.", IdempotencyKey: "rejected-preview-request", CorrelationID: "rejected-preview-request",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.DecideManagedServiceRequest(ctx, DecideManagedServiceRequestCommand{
		WorkspaceIdentifier: workspace.ID, RequestID: rejectedRequest.Value.ID, ExpectedRevision: rejectedRequest.Value.Revision,
		Reason: "Owner does not want this process definition.", IdempotencyKey: "reject-agent-preview", CorrelationID: "reject-agent-preview",
	}); err != nil {
		t.Fatal(err)
	}
	retired, err := storage.ManagedServiceDefinition(ctx, workspace.ID, rejectedDefinition.Value.ID)
	if err != nil || retired.Status != domain.ManagedServiceDefinitionRetired || retired.Revision != 2 || retired.UpdatedBy != localOwnerActorID {
		t.Fatalf("rejected definition = %#v, %v", retired, err)
	}
	integrity, err := storage.VerifyCanonical(ctx, CanonicalVerifyOptions{Full: true})
	if err != nil || !integrity.Complete || integrity.Status != "ok" {
		t.Fatalf("agent-authored service integrity = %#v, %v", integrity, err)
	}
}
