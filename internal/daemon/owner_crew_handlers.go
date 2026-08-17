package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func (s *server) handleOwnerCrewConfigure(request localapi.Request) localapi.Response {
	var params localapi.OwnerCrewConfigureParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "owner.crew.configure requires exact project, action, binding revision, worker fields, and idempotency_key")
	}
	ctx := context.Background()
	configurationJSON, err := json.Marshal(struct {
		Workspace               string `json:"workspace"`
		Project                 string `json:"project"`
		Action                  string `json:"action"`
		ExpectedBindingRevision int64  `json:"expected_binding_revision"`
		Name                    string `json:"name,omitempty"`
		Agent                   string `json:"agent,omitempty"`
		Provider                string `json:"provider,omitempty"`
		Runtime                 string `json:"runtime,omitempty"`
		MaxConcurrency          int    `json:"max_concurrency,omitempty"`
	}{params.Workspace, params.Project, params.Action, params.ExpectedBindingRevision, params.Name, params.Agent, params.Provider, params.Runtime, params.MaxConcurrency})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	digest := sha256.Sum256(configurationJSON)
	configurationHash := hex.EncodeToString(digest[:])
	binding, err := s.store.OwnerExecutiveBinding(ctx, params.Workspace, params.Project)
	if err != nil {
		return storeErrorResponse(request, err)
	}

	// A response-loss replay arrives after the binding revision advanced. The
	// exact configuration hash is the idempotency semantic; current IDs are only
	// supplied so the Store can resolve the already-committed receipt before any
	// new mutation is attempted.
	if binding.Revision != params.ExpectedBindingRevision {
		replayed, replayErr := s.store.ReconfigureOwnerExecutive(ctx, store.ReconfigureOwnerExecutiveCommand{
			WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project,
			ExpectedRevision: params.ExpectedBindingRevision, ManagerGrantID: binding.ManagerGrantID,
			LaunchProfileID: binding.LaunchProfileID, ConfigurationHash: configurationHash,
			Reason: "owner changed the implementation crew", IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
		})
		if replayErr != nil {
			return storeErrorResponse(request, replayErr)
		}
		if params.Action == "disable" {
			return s.completeDisabledWorker(request, params, replayed.Value, replayed.EventSequence)
		}
		return s.ownerCrewMutationResponse(request, params, replayed.Value, replayed.EventSequence)
	}

	grant, err := s.store.ManagerGrant(ctx, params.Workspace, binding.ManagerGrantID)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	executive, err := s.store.Agent(ctx, params.Workspace, binding.AgentID)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	executiveProfile, err := s.store.LaunchProfile(ctx, params.Workspace, binding.LaunchProfileID)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	profiles, err := s.activeImplementationProfiles(ctx, params.Workspace, params.Project, binding.AgentID)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	var target domain.AgentDefinition
	if params.Action == "add" {
		provider, ok := s.providers[params.Provider]
		if !ok {
			return storeErrorResponse(request, &store.Error{Code: store.CodeAdapterUnavailable, Message: "selected provider is not registered"})
		}
		runtimeDriver, ok := s.runtimes[params.Runtime]
		if !ok {
			return storeErrorResponse(request, &store.Error{Code: store.CodeAdapterUnavailable, Message: "selected runtime is not registered"})
		}
		if err := preflightWorkbenchRuntime(ctx, params.Runtime, runtimeDriver); err != nil {
			return storeErrorResponse(request, err)
		}
		probeContext, cancel := context.WithTimeout(ctx, 15*time.Second)
		_, err = diagnoseWorkbenchProvider(probeContext, provider)
		cancel()
		if err != nil {
			return storeErrorResponse(request, &store.Error{Code: store.CodeAdapterUnavailable, Message: err.Error()})
		}
		created, createErr := s.store.CreateAgent(ctx, store.CreateAgentCommand{
			WorkspaceIdentifier: params.Workspace, Name: params.Name, Role: "implementation",
			Provider: params.Provider, Runtime: params.Runtime, MaxConcurrency: params.MaxConcurrency,
			IdempotencyKey: params.IdempotencyKey + "-agent", CorrelationID: request.ID,
		})
		if createErr != nil {
			return storeErrorResponse(request, createErr)
		}
		target = created.Value
		createdProfile, profileErr := s.store.CreateLaunchProfile(ctx, store.CreateLaunchProfileCommand{
			WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project,
			AgentIdentifier: target.ID, ExpectedAgentRevision: target.Revision, Purpose: "implementation",
			Runtime: target.Runtime, Provider: target.Provider, CheckoutIdentifier: executiveProfile.CheckoutID,
			Scenario: ownerWorkbenchScenario(), AssignmentLeaseSeconds: executiveProfile.AssignmentLeaseSeconds,
			CapabilityTTLSeconds: executiveProfile.CapabilityTTLSeconds,
			IdempotencyKey:       params.IdempotencyKey + "-worker-profile", CorrelationID: request.ID,
		})
		if profileErr != nil {
			return storeErrorResponse(request, profileErr)
		}
		profiles = append(profiles, createdProfile.Value)
	} else {
		target, err = s.store.Agent(ctx, params.Workspace, params.Agent)
		if err != nil {
			return storeErrorResponse(request, err)
		}
		if err := s.store.EnsureImplementationWorkerCanDisable(ctx, params.Workspace, params.Project, target.ID); err != nil {
			return storeErrorResponse(request, err)
		}
		remaining := profiles[:0]
		for _, profile := range profiles {
			if profile.AgentID != target.ID {
				remaining = append(remaining, profile)
			}
		}
		profiles = remaining
		if len(profiles) == 0 {
			return storeErrorResponse(request, &store.Error{Code: store.CodeInvalidAgent, Message: "the final implementation worker cannot be disabled; add its replacement first"})
		}
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].ID < profiles[j].ID })
	profileIDs := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		profileIDs = append(profileIDs, profile.ID)
	}
	newGrant, err := s.store.CreateManagerGrant(ctx, store.CreateManagerGrantCommand{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project,
		ObjectiveID: binding.ObjectiveID, TaskID: binding.PlanningTaskID, AgentIdentifier: binding.AgentID,
		ExpectedTaskRevision: grant.TaskRevision, ExpectedAgentRevision: executive.Revision,
		ProposalKinds: grant.ProposalKinds, LaunchProfileIDs: profileIDs, AllowedClaimKinds: grant.AllowedClaimKinds,
		Limits: grant.Limits, ExpiresAt: grant.ExpiresAt,
		IdempotencyKey: params.IdempotencyKey + "-grant", CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	newExecutiveProfile, err := s.store.CreateLaunchProfile(ctx, store.CreateLaunchProfileCommand{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project,
		AgentIdentifier: binding.AgentID, ExpectedAgentRevision: executive.Revision, Purpose: executiveProfile.Purpose,
		Runtime: executiveProfile.Runtime, Provider: executiveProfile.Provider, CheckoutIdentifier: executiveProfile.CheckoutID,
		Scenario: executiveProfile.Scenario, AssignmentLeaseSeconds: executiveProfile.AssignmentLeaseSeconds,
		CapabilityTTLSeconds: executiveProfile.CapabilityTTLSeconds, ManagerGrantID: newGrant.Value.ID,
		IdempotencyKey: params.IdempotencyKey + "-executive-profile", CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	reconfigured, err := s.store.ReconfigureOwnerExecutive(ctx, store.ReconfigureOwnerExecutiveCommand{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project,
		ExpectedRevision: params.ExpectedBindingRevision, ManagerGrantID: newGrant.Value.ID,
		LaunchProfileID: newExecutiveProfile.Value.ID, ConfigurationHash: configurationHash,
		Reason: "owner changed the implementation crew", IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	eventSequence := reconfigured.EventSequence
	if params.Action == "disable" {
		return s.completeDisabledWorker(request, params, reconfigured.Value, eventSequence)
	}
	return s.ownerCrewMutationResponse(request, params, reconfigured.Value, eventSequence)
}

func (s *server) completeDisabledWorker(request localapi.Request, params localapi.OwnerCrewConfigureParams, binding domain.OwnerExecutiveBinding, eventSequence int64) localapi.Response {
	ctx := context.Background()
	target, err := s.store.Agent(ctx, params.Workspace, params.Agent)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	disabled := false
	expectedRevision := target.Revision
	if !target.Enabled && expectedRevision > 1 {
		expectedRevision--
	}
	updated, updateErr := s.store.UpdateAgent(ctx, store.UpdateAgentCommand{
		WorkspaceIdentifier: params.Workspace, AgentIdentifier: target.ID, Enabled: &disabled,
		ExpectedRevision: expectedRevision, IdempotencyKey: params.IdempotencyKey + "-disable-agent", CorrelationID: request.ID,
	})
	if updateErr != nil {
		return storeErrorResponse(request, updateErr)
	}
	if updated.EventSequence > eventSequence {
		eventSequence = updated.EventSequence
	}
	allTargetProfiles, listErr := s.store.LaunchProfiles(ctx, store.ListLaunchProfilesQuery{WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, AgentIdentifier: target.ID, Status: domain.LaunchProfileActive, Limit: 100})
	if listErr != nil {
		return storeErrorResponse(request, listErr)
	}
	for index, profile := range allTargetProfiles {
		retired, retireErr := s.store.RetireLaunchProfile(ctx, store.RetireLaunchProfileCommand{
			WorkspaceIdentifier: params.Workspace, LaunchProfileID: profile.ID, ExpectedRevision: profile.Revision,
			Reason: "owner disabled the implementation worker", IdempotencyKey: params.IdempotencyKey + "-retire-" + strings.TrimPrefix(profile.ID, "lprof_")[:8] + "-" + string(rune('a'+index)), CorrelationID: request.ID,
		})
		if retireErr != nil {
			return storeErrorResponse(request, retireErr)
		}
		if retired.EventSequence > eventSequence {
			eventSequence = retired.EventSequence
		}
	}
	return s.ownerCrewMutationResponse(request, params, binding, eventSequence)
}

func (s *server) activeImplementationProfiles(ctx context.Context, workspace, project, executiveAgentID string) ([]domain.LaunchProfile, error) {
	profiles, err := s.store.LaunchProfiles(ctx, store.ListLaunchProfilesQuery{WorkspaceIdentifier: workspace, ProjectIdentifier: project, Status: domain.LaunchProfileActive, Limit: 100})
	if err != nil {
		return nil, err
	}
	result := make([]domain.LaunchProfile, 0, len(profiles))
	for _, profile := range profiles {
		if profile.AgentID != executiveAgentID && profile.ManagerGrantID == "" {
			result = append(result, profile)
		}
	}
	return result, nil
}

func (s *server) ownerCrewMutationResponse(request localapi.Request, params localapi.OwnerCrewConfigureParams, binding domain.OwnerExecutiveBinding, eventSequence int64) localapi.Response {
	identifier := params.Agent
	if params.Action == "add" {
		identifier = params.Name
	}
	agent, err := s.store.Agent(context.Background(), params.Workspace, identifier)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	profiles, err := s.activeImplementationProfiles(context.Background(), params.Workspace, params.Project, binding.AgentID)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.OwnerCrewMutationResult{
		Schema: localapi.OwnerCrewMutationSchema, Type: "owner_crew_mutation", Action: params.Action,
		Binding: binding, Agent: agent, WorkerProfiles: profiles, EventSequence: eventSequence,
	})
}
