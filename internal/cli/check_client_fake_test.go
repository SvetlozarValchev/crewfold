package cli

import (
	"context"

	"crewfold/internal/localapi"
)

// The shared CLI fake implements the complete current daemon surface. Focused
// check tests override these methods on fakeCheckDaemonClient when they need to
// capture inputs or return a particular result.
func (*fakeDaemonClient) CheckDefinitionCreate(context.Context, localapi.CheckDefinitionCreateParams) (localapi.CheckDefinitionMutationResult, error) {
	return localapi.CheckDefinitionMutationResult{}, nil
}

func (*fakeDaemonClient) CheckDefinitionRetire(context.Context, localapi.CheckDefinitionRetireParams) (localapi.CheckDefinitionMutationResult, error) {
	return localapi.CheckDefinitionMutationResult{}, nil
}

func (*fakeDaemonClient) CheckDefinitionShow(context.Context, string, string) (localapi.CheckDefinitionShowResult, error) {
	return localapi.CheckDefinitionShowResult{}, nil
}

func (*fakeDaemonClient) CheckDefinitionList(context.Context, localapi.CheckDefinitionQueryParams) (localapi.CheckDefinitionListResult, error) {
	return localapi.CheckDefinitionListResult{}, nil
}

func (*fakeDaemonClient) CheckRequirementCreate(context.Context, localapi.CheckRequirementCreateParams) (localapi.CheckRequirementMutationResult, error) {
	return localapi.CheckRequirementMutationResult{}, nil
}

func (*fakeDaemonClient) CheckRequirementRetire(context.Context, localapi.CheckRequirementRetireParams) (localapi.CheckRequirementMutationResult, error) {
	return localapi.CheckRequirementMutationResult{}, nil
}

func (*fakeDaemonClient) CheckRequirementList(context.Context, localapi.CheckRequirementQueryParams) (localapi.CheckRequirementListResult, error) {
	return localapi.CheckRequirementListResult{}, nil
}

func (*fakeDaemonClient) CheckGrantCreate(context.Context, localapi.CheckGrantCreateParams) (localapi.CheckGrantMutationResult, error) {
	return localapi.CheckGrantMutationResult{}, nil
}

func (*fakeDaemonClient) CheckGrantRevoke(context.Context, localapi.CheckGrantRevokeParams) (localapi.CheckGrantMutationResult, error) {
	return localapi.CheckGrantMutationResult{}, nil
}

func (*fakeDaemonClient) CheckGrantShow(context.Context, string, string) (localapi.CheckGrantShowResult, error) {
	return localapi.CheckGrantShowResult{}, nil
}

func (*fakeDaemonClient) CheckGrantList(context.Context, localapi.CheckGrantQueryParams) (localapi.CheckGrantListResult, error) {
	return localapi.CheckGrantListResult{}, nil
}

func (*fakeDaemonClient) CheckRouteCreate(context.Context, localapi.CheckRouteCreateParams) (localapi.CheckRouteMutationResult, error) {
	return localapi.CheckRouteMutationResult{}, nil
}

func (*fakeDaemonClient) CheckRouteRetire(context.Context, localapi.CheckRouteRetireParams) (localapi.CheckRouteMutationResult, error) {
	return localapi.CheckRouteMutationResult{}, nil
}

func (*fakeDaemonClient) CheckRouteList(context.Context, localapi.CheckRouteQueryParams) (localapi.CheckRouteListResult, error) {
	return localapi.CheckRouteListResult{}, nil
}

func (*fakeDaemonClient) CheckPolicyShow(context.Context, string, string) (localapi.CheckPolicyShowResult, error) {
	return localapi.CheckPolicyShowResult{}, nil
}

func (*fakeDaemonClient) CheckPolicyConfigure(context.Context, localapi.CheckPolicyConfigureParams) (localapi.CheckPolicyMutationResult, error) {
	return localapi.CheckPolicyMutationResult{}, nil
}

func (*fakeDaemonClient) CheckRun(context.Context, localapi.CheckRunParams) (localapi.CheckRunMutationResult, error) {
	return localapi.CheckRunMutationResult{}, nil
}

func (*fakeDaemonClient) CheckList(context.Context, localapi.CheckQueryParams) (localapi.CheckRunListResult, error) {
	return localapi.CheckRunListResult{}, nil
}

func (*fakeDaemonClient) CheckInspect(context.Context, string, string) (localapi.CheckInspectResult, error) {
	return localapi.CheckInspectResult{}, nil
}

func (*fakeDaemonClient) CheckLogs(context.Context, string, string) (localapi.CheckLogsResult, error) {
	return localapi.CheckLogsResult{}, nil
}

func (*fakeDaemonClient) CheckWatch(context.Context, localapi.CheckWatchParams) (localapi.CheckWatchResult, error) {
	return localapi.CheckWatchResult{}, nil
}

func (*fakeDaemonClient) CheckRepairList(context.Context, localapi.CheckRepairQueryParams) (localapi.CheckRepairListResult, error) {
	return localapi.CheckRepairListResult{}, nil
}

func (*fakeDaemonClient) CheckRepairInspect(context.Context, string, string) (localapi.CheckRepairShowResult, error) {
	return localapi.CheckRepairShowResult{}, nil
}

func (*fakeDaemonClient) CheckRepairAccept(context.Context, localapi.CheckRepairDecisionParams) (localapi.CheckRepairMutationResult, error) {
	return localapi.CheckRepairMutationResult{}, nil
}

func (*fakeDaemonClient) CheckRepairReject(context.Context, localapi.CheckRepairDecisionParams) (localapi.CheckRepairMutationResult, error) {
	return localapi.CheckRepairMutationResult{}, nil
}
