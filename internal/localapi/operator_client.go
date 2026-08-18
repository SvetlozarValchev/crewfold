package localapi

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"crewfold/internal/domain"
	protocolschema "crewfold/protocol"
)

var canonicalWorkspaceIDPattern = regexp.MustCompile(`^ws_[0-9a-f]{32}$`)
var canonicalProjectIDPattern = regexp.MustCompile(`^prj_[0-9a-f]{32}$`)
var canonicalObjectiveIDPattern = regexp.MustCompile(`^obj_[0-9a-f]{32}$`)
var canonicalTaskIDPattern = regexp.MustCompile(`^task_[0-9a-f]{32}$`)
var canonicalAgentIDPattern = regexp.MustCompile(`^agent_[0-9a-f]{32}$`)
var canonicalScopeNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

const (
	defaultOperatorPageLimit = 50
	maximumOperatorPageLimit = 200
	maximumEventPageLimit    = 1000
	maximumCursorBytes       = 256
)

var operatorReadResultDiscriminators = map[string][2]string{
	MethodWorkspaceShow:          {WorkspaceShowSchema, "workspace"},
	MethodProjectShow:            {ProjectShowSchema, "project"},
	MethodAgentShow:              {AgentShowSchema, "agent"},
	MethodWorkspaceList:          {WorkspaceListSchema, "workspace_list"},
	MethodProjectList:            {ProjectListSchema, "project_list"},
	MethodAgentList:              {AgentListSchema, "agent_list"},
	MethodDomainAgentTree:        {DomainAgentTreeSchema, "domain_agent_tree"},
	MethodDomainAgentSessionShow: {DomainAgentSessionSchema, "domain_agent_session"},
	MethodDomainWorkProposalList: {DomainWorkProposalListSchema, "domain_work_proposal_list"},
	MethodObjectiveList:          {ObjectiveListSchema, "objective_list"},
	MethodTaskList:               {TaskListSchema, "task_list"},
	MethodRunList:                {RunListSchema, "run_list"},
	MethodClaimList:              {ClaimListSchema, "claim_list"},
	MethodOverlapList:            {OverlapListSchema, "overlap_list"},
	MethodDriftList:              {DriftListSchema, "drift_list"},
	MethodMeetingList:            {MeetingListSchema, "meeting_list"},
	MethodEventsList:             {EventsListSchema, "event_list"},
	MethodEventsTimeline:         {EventsTimelineSchema, "event_timeline"},
}

// operatorResultSchemaPaths is the executable current-contract registry for
// every result consumed by the TUI or another security-bearing owner surface.
// Validation happens on raw JSON before Go zero values can erase omitted-vs-null
// or optional-field presence.
type operatorResultContract struct {
	path, schema, kind string
}

var operatorResultContracts = map[string]operatorResultContract{
	MethodWorkspaceShow:               {"local/v1/workspace-show.result.schema.json", WorkspaceShowSchema, "workspace"},
	MethodProjectShow:                 {"local/v1/project-show.result.schema.json", ProjectShowSchema, "project"},
	MethodAgentShow:                   {"local/v1/agent-show.result.schema.json", AgentShowSchema, "agent"},
	MethodWorkspaceList:               {"local/v1/workspace-list.result.schema.json", WorkspaceListSchema, "workspace_list"},
	MethodProjectList:                 {"local/v1/project-list.result.schema.json", ProjectListSchema, "project_list"},
	MethodAgentList:                   {"local/v1/agent-list.result.schema.json", AgentListSchema, "agent_list"},
	MethodDomainAgentCreate:           {"local/v1/domain-agent-create.result.schema.json", DomainAgentCreateSchema, "domain_agent_create"},
	MethodDomainAgentSpecDraft:        {"local/v1/domain-agent-spec-draft.result.schema.json", DomainAgentSpecDraftSchema, "domain_agent_spec_draft"},
	MethodDomainAgentAttach:           {"local/v1/domain-agent-mutation.result.schema.json", DomainAgentMutationSchema, "domain_agent_mutation"},
	MethodDomainAgentUpdate:           {"local/v1/domain-agent-mutation.result.schema.json", DomainAgentMutationSchema, "domain_agent_mutation"},
	MethodDomainAgentTree:             {"local/v1/domain-agent-tree.result.schema.json", DomainAgentTreeSchema, "domain_agent_tree"},
	MethodDomainAgentSessionOpen:      {"local/v1/domain-agent-session.result.schema.json", DomainAgentSessionSchema, "domain_agent_session"},
	MethodDomainAgentSessionShow:      {"local/v1/domain-agent-session.result.schema.json", DomainAgentSessionSchema, "domain_agent_session"},
	MethodDomainAgentSessionSend:      {"local/v1/domain-agent-session.result.schema.json", DomainAgentSessionSchema, "domain_agent_session"},
	MethodDomainAgentSessionInterrupt: {"local/v1/domain-agent-session.result.schema.json", DomainAgentSessionSchema, "domain_agent_session"},
	MethodDomainStaffingGrantCreate:   {"local/v1/domain-staffing-grant-mutation.result.schema.json", DomainStaffingGrantMutationSchema, "domain_staffing_grant_mutation"},
	MethodDomainStaffingGrantList:     {"local/v1/domain-staffing-grant-list.result.schema.json", DomainStaffingGrantListSchema, "domain_staffing_grant_list"},
	MethodDomainStaffingGrantRevoke:   {"local/v1/domain-staffing-grant-mutation.result.schema.json", DomainStaffingGrantMutationSchema, "domain_staffing_grant_mutation"},
	MethodDomainWorkProposalList:      {"local/v1/domain-work-proposal-list.result.schema.json", DomainWorkProposalListSchema, "domain_work_proposal_list"},
	MethodDomainWorkProposalAccept:    {"local/v1/domain-work-proposal-decision.result.schema.json", DomainWorkProposalDecisionSchema, "domain_work_proposal_decision"},
	MethodDomainWorkProposalReject:    {"local/v1/domain-work-proposal-decision.result.schema.json", DomainWorkProposalDecisionSchema, "domain_work_proposal_decision"},
	MethodObjectiveList:               {"local/v1/objective-list.result.schema.json", ObjectiveListSchema, "objective_list"},
	MethodTaskList:                    {"local/v1/task-list.result.schema.json", TaskListSchema, "task_list"},
	MethodRunList:                     {"local/v1/run-list.result.schema.json", RunListSchema, "run_list"},
	MethodClaimList:                   {"local/v1/claim-list.result.schema.json", ClaimListSchema, "claim_list"},
	MethodOverlapList:                 {"local/v1/overlap-list.result.schema.json", OverlapListSchema, "overlap_list"},
	MethodDriftList:                   {"local/v1/drift-list.result.schema.json", DriftListSchema, "drift_list"},
	MethodMeetingList:                 {"local/v1/meeting-list.result.schema.json", MeetingListSchema, "meeting_list"},
	MethodApprovalList:                {"local/v1/approval-list.result.schema.json", ApprovalListSchema, "approval_list"},
	MethodCheckList:                   {"local/v1/check-run-list.result.schema.json", CheckRunListSchema, "check_run_list"},
	MethodInboxList:                   {"local/v1/inbox-list.result.schema.json", InboxListSchema, "inbox"},
	MethodThreadList:                  {"local/v1/thread-list.result.schema.json", ThreadListSchema, "thread_list"},
	MethodEventsList:                  {"local/v1/events-list.result.schema.json", EventsListSchema, "event_list"},
	MethodEventsTimeline:              {"local/v1/events-timeline.result.schema.json", EventsTimelineSchema, "event_timeline"},
	MethodBriefingShow:                {"local/v1/briefing-show.result.schema.json", BriefingShowSchema, "management_briefing"},
	MethodBriefingExplain:             {"local/v1/briefing-explain.result.schema.json", BriefingExplainSchema, "briefing_claim_explanation"},
	MethodSupervisorActionShow:        {"local/v1/supervisor-action-show.result.schema.json", SupervisorActionShowSchema, "supervisor_action"},
	MethodRunAttach:                   {"local/v1/run-attach.result.schema.json", RunAttachSchema, "run_attach"},
	MethodRunResume:                   {"local/v1/run-mutation.result.schema.json", RunMutationSchema, "run_mutation"},
	MethodRunStop:                     {"local/v1/run-mutation.result.schema.json", RunMutationSchema, "run_mutation"},
	MethodRunLostResolve:              {"local/v1/run-loss-resolution.result.schema.json", RunLossResolutionSchema, "run_loss_resolution"},
	MethodApprovalAllow:               {"local/v1/approval-mutation.result.schema.json", ApprovalMutationSchema, "approval_mutation"},
	MethodApprovalDeny:                {"local/v1/approval-mutation.result.schema.json", ApprovalMutationSchema, "approval_mutation"},
	MethodSystemDoctorFull:            {"local/v1/full-doctor.result.schema.json", FullDoctorSchema, "full_doctor"},
	MethodBackupCreate:                {"local/v1/backup-create.result.schema.json", BackupCreateSchema, "backup"},
	MethodWebBootstrap:                {"local/v1/web-bootstrap.result.schema.json", WebBootstrapSchema, "web_bootstrap"},
}

func validateStrictOperatorResultWire(method string, raw []byte) error {
	contract, ok := operatorResultContracts[method]
	if !ok {
		return nil
	}
	var discriminator struct {
		Schema string `json:"schema"`
		Type   string `json:"type"`
	}
	if err := json.Unmarshal(raw, &discriminator); err != nil || discriminator.Schema != contract.schema || discriminator.Type != contract.kind {
		return fmt.Errorf("result discriminator is %q/%q, want %q/%q", discriminator.Schema, discriminator.Type, contract.schema, contract.kind)
	}
	return protocolschema.ValidateJSON(contract.path, raw)
}

func validateOperatorReadResult(method string, paramsValue, result any) error {
	if expected, ok := operatorReadResultDiscriminators[method]; ok {
		if err := validateOperatorDiscriminator(method, result, expected); err != nil {
			return err
		}
	}

	switch value := result.(type) {
	case *WorkspaceShowResult:
		params := paramsValue.(WorkspaceShowParams)
		if !canonicalWorkspaceIDPattern.MatchString(value.Workspace.ID) || !canonicalScopeNamePattern.MatchString(value.Workspace.Name) ||
			!scopeIdentifierMatches(params.Identifier, value.Workspace.ID, value.Workspace.Name, canonicalWorkspaceIDPattern) {
			return fmt.Errorf("decode local API result %s: workspace does not match the requested scope", method)
		}
		return nil
	case *ProjectShowResult:
		params := paramsValue.(ProjectShowParams)
		if !canonicalProjectIDPattern.MatchString(value.Project.ID) || !canonicalWorkspaceIDPattern.MatchString(value.Project.WorkspaceID) ||
			!canonicalScopeNamePattern.MatchString(value.Project.Name) ||
			value.Project.WorkspaceID != params.Workspace ||
			!scopeIdentifierMatches(params.Project, value.Project.ID, value.Project.Name, canonicalProjectIDPattern) {
			return fmt.Errorf("decode local API result %s: project does not match the requested scope", method)
		}
		return nil
	case *AgentShowResult:
		params := paramsValue.(AgentQueryParams)
		if !canonicalAgentIDPattern.MatchString(value.Agent.ID) || value.Agent.WorkspaceID != params.Workspace ||
			!scopeIdentifierMatches(params.Agent, value.Agent.ID, value.Agent.Name, canonicalAgentIDPattern) {
			return fmt.Errorf("decode local API result %s: agent does not match the requested scope", method)
		}
		return nil
	case *WorkspaceListResult:
		return validatePage(method, value.PageResult, len(value.Workspaces), requestedPageLimit(paramsValue.(WorkspaceListParams).Limit, maximumOperatorPageLimit))
	case *ProjectListResult:
		params := paramsValue.(ProjectListParams)
		for _, item := range value.Projects {
			if err := validateOperatorScope(method, params.Workspace, "", item.WorkspaceID, ""); err != nil {
				return err
			}
		}
		return validatePage(method, value.PageResult, len(value.Projects), requestedPageLimit(paramsValue.(ProjectListParams).Limit, maximumOperatorPageLimit))
	case *AgentListResult:
		params := paramsValue.(AgentListParams)
		for _, item := range value.Agents {
			if err := validateOperatorScope(method, params.Workspace, "", item.WorkspaceID, ""); err != nil {
				return err
			}
		}
		return validatePage(method, value.PageResult, len(value.Agents), requestedPageLimit(paramsValue.(AgentListParams).Limit, maximumOperatorPageLimit))
	case *DomainAgentTreeResult:
		params := paramsValue.(DomainAgentTreeParams)
		if value.ProjectID != params.Project || len(value.Agents) > 1000 {
			return fmt.Errorf("decode local API result %s: domain tree scope or bound mismatch", method)
		}
		seen := make(map[string]domain.DomainAgentMembership, len(value.Agents))
		preferred := 0
		for _, item := range value.Agents {
			if item.Definition.WorkspaceID != params.Workspace || item.Membership.ProjectID != params.Project ||
				item.Definition.ID != item.Membership.AgentID || seen[item.Membership.AgentID].AgentID != "" {
				return fmt.Errorf("decode local API result %s: domain agent scope or identity mismatch", method)
			}
			seen[item.Membership.AgentID] = item.Membership
			if item.Membership.Status == domain.DomainAgentActive && item.Membership.PreferredEntry {
				preferred++
			}
		}
		if preferred > 1 || !validReturnedDomainAttentionTree(seen) {
			return fmt.Errorf("decode local API result %s: domain attention tree is invalid", method)
		}
		return nil
	case *DomainWorkProposalListResult:
		params := paramsValue.(DomainWorkProposalListParams)
		for _, proposal := range value.Proposals {
			if proposal.WorkspaceID != params.Workspace || proposal.ProjectID != params.Project {
				return fmt.Errorf("decode local API result %s: domain work proposal violates requested scope", method)
			}
		}
		return nil
	case *ObjectiveListResult:
		params := paramsValue.(ObjectiveListParams)
		for _, item := range value.Objectives {
			if err := validateOperatorScope(method, params.Workspace, params.Project, item.WorkspaceID, item.ProjectID); err != nil {
				return err
			}
		}
		return validatePage(method, value.PageResult, len(value.Objectives), requestedPageLimit(paramsValue.(ObjectiveListParams).Limit, maximumOperatorPageLimit))
	case *TaskListResult:
		params := paramsValue.(TaskListParams)
		for _, item := range value.Tasks {
			if err := validateOperatorScope(method, params.Workspace, params.Project, item.Task.WorkspaceID, item.Task.ProjectID); err != nil {
				return err
			}
			if params.ReadyOnly && !item.Readiness.Ready {
				return fmt.Errorf("decode local API result %s: non-ready task violates ready-only scope", method)
			}
		}
		return validatePage(method, value.PageResult, len(value.Tasks), requestedPageLimit(paramsValue.(TaskListParams).Limit, maximumOperatorPageLimit))
	case *RunListResult:
		params := paramsValue.(RunListParams)
		for _, item := range value.Runs {
			if err := validateOperatorScope(method, params.Workspace, params.Project, item.WorkspaceID, item.ProjectID); err != nil {
				return err
			}
			if (params.Task != "" && item.TaskID != params.Task) || (params.Status != "" && item.Status != params.Status) {
				return fmt.Errorf("decode local API result %s: run violates requested task or status scope", method)
			}
		}
		return validatePage(method, value.PageResult, len(value.Runs), requestedPageLimit(paramsValue.(RunListParams).Limit, maximumOperatorPageLimit))
	case *ClaimListResult:
		params := paramsValue.(ClaimListParams)
		for _, item := range value.Claims {
			if err := validateOperatorScope(method, params.Workspace, params.Project, item.WorkspaceID, item.ProjectID); err != nil {
				return err
			}
			if params.Status != "" && item.Status != params.Status {
				return fmt.Errorf("decode local API result %s: claim violates requested status scope", method)
			}
		}
		return validatePage(method, value.PageResult, len(value.Claims), requestedPageLimit(paramsValue.(ClaimListParams).Limit, maximumOperatorPageLimit))
	case *OverlapListResult:
		params := paramsValue.(OverlapListParams)
		for _, item := range value.Overlaps {
			if err := validateOperatorScope(method, params.Workspace, params.Project, item.WorkspaceID, item.ProjectID); err != nil {
				return err
			}
			if params.Status != "" && item.Status != params.Status {
				return fmt.Errorf("decode local API result %s: overlap violates requested status scope", method)
			}
		}
		return validatePage(method, value.PageResult, len(value.Overlaps), requestedPageLimit(paramsValue.(OverlapListParams).Limit, maximumOperatorPageLimit))
	case *DriftListResult:
		params := paramsValue.(DriftListParams)
		for _, item := range value.Drifts {
			if err := validateOperatorScope(method, params.Workspace, params.Project, item.WorkspaceID, item.ProjectID); err != nil {
				return err
			}
			if params.Status != "" && item.Status != params.Status {
				return fmt.Errorf("decode local API result %s: claim drift violates requested status scope", method)
			}
		}
		return validatePage(method, value.PageResult, len(value.Drifts), requestedPageLimit(paramsValue.(DriftListParams).Limit, maximumOperatorPageLimit))
	case *MeetingListResult:
		params := paramsValue.(MeetingListParams)
		for _, item := range value.Meetings {
			if err := validateOperatorScope(method, params.Workspace, params.Project, item.WorkspaceID, item.ProjectID); err != nil {
				return err
			}
			if params.Status != "" && item.Status != params.Status {
				return fmt.Errorf("decode local API result %s: meeting violates requested status scope", method)
			}
		}
		return validatePage(method, value.PageResult, len(value.Meetings), requestedPageLimit(paramsValue.(MeetingListParams).Limit, maximumOperatorPageLimit))
	case *ApprovalListResult:
		params := paramsValue.(ApprovalListParams)
		for _, item := range value.Approvals {
			if err := validateOperatorScope(method, params.Workspace, params.Project, item.WorkspaceID, item.ProjectID); err != nil {
				return err
			}
			if (params.Status != "" && item.Status != params.Status) || (params.Action != "" && item.ActionID != params.Action) {
				return fmt.Errorf("decode local API result %s: approval violates requested action or status scope", method)
			}
		}
		return validatePage(method, value.PageResult, len(value.Approvals), requestedPageLimit(paramsValue.(ApprovalListParams).Limit, maximumOperatorPageLimit))
	case *CheckRunListResult:
		params := paramsValue.(CheckListParams)
		for _, item := range value.Runs {
			run := item.Run
			if err := validateOperatorScope(method, params.Workspace, params.Project, run.WorkspaceID, run.ProjectID); err != nil {
				return err
			}
			if (params.Task != "" && run.TaskID != params.Task) || (params.Requirement != "" && run.RequirementID != params.Requirement) ||
				(params.Definition != "" && run.DefinitionID != params.Definition) || (params.Status != "" && run.Status != params.Status) ||
				(params.Outcome != "" && item.Outcome != params.Outcome) {
				return fmt.Errorf("decode local API result %s: check run violates requested scope", method)
			}
		}
		return validatePage(method, value.PageResult, len(value.Runs), requestedPageLimit(paramsValue.(CheckListParams).Limit, maximumOperatorPageLimit))
	case *EventsListResult:
		return validateForwardEvents(paramsValue.(EventsListParams), *value)
	case *EventsTimelineResult:
		return validateReverseTimeline(paramsValue.(EventsTimelineParams), *value)
	case *SupervisorActionShowResult:
		params := paramsValue.(SupervisorActionQueryParams)
		if value.Action.ID != params.Action || value.Action.WorkspaceID != params.Workspace {
			return fmt.Errorf("decode local API result %s: supervisor action does not match the requested scope", method)
		}
	case *RunMutationResult:
		var workspace, run string
		var expectedRevision int64
		switch params := paramsValue.(type) {
		case RunResumeParams:
			workspace, run, expectedRevision = params.Workspace, params.Run, params.ExpectedRevision
			if value.Detail.Run.Status != domain.RunActive {
				return fmt.Errorf("decode local API result %s: resume did not return an active run", method)
			}
		case RunStopParams:
			workspace, run, expectedRevision = params.Workspace, params.Run, params.ExpectedRevision
			if value.Detail.Run.Status != domain.RunStopping || value.Detail.Run.StopGraceMillis != params.GracePeriodMillis {
				return fmt.Errorf("decode local API result %s: stop did not return the requested stopping state", method)
			}
		}
		if value.Detail.Run.ID != run || value.Detail.Run.Revision != expectedRevision+1 || value.Detail.Run.WorkspaceID != workspace {
			return fmt.Errorf("decode local API result %s: run mutation does not match the requested target", method)
		}
	case *DomainAgentMutationResult:
		var workspace, project, agent string
		var expectedRevision int64
		switch params := paramsValue.(type) {
		case DomainAgentAttachParams:
			workspace, project, agent, expectedRevision = params.Workspace, params.Project, params.Agent, 0
		case DomainAgentUpdateParams:
			workspace, project, agent, expectedRevision = params.Workspace, params.Project, params.Agent, params.ExpectedRevision
		}
		if !canonicalWorkspaceIDPattern.MatchString(workspace) || value.Membership.ProjectID != project ||
			value.Membership.AgentID != agent || value.Membership.Revision != expectedRevision+1 {
			return fmt.Errorf("decode local API result %s: domain agent mutation does not match the requested target", method)
		}
	case *DomainWorkProposalDecisionResult:
		params := paramsValue.(DomainWorkProposalDecisionParams)
		proposal := value.Decision.Proposal
		if proposal.WorkspaceID != params.Workspace || proposal.ID != params.ProposalID || proposal.Revision != params.ExpectedRevision+1 || proposal.DecisionNote != params.DecisionNote {
			return fmt.Errorf("decode local API result %s: domain work proposal decision does not match the requested target", method)
		}
		if method == MethodDomainWorkProposalAccept && proposal.Status != domain.DomainWorkProposalAccepted || method == MethodDomainWorkProposalReject && proposal.Status != domain.DomainWorkProposalRejected {
			return fmt.Errorf("decode local API result %s: domain work proposal returned the wrong terminal decision", method)
		}
	case *RunLossResolutionResult:
		params := paramsValue.(RunLostResolveParams)
		if value.Detail.Run.ID != params.Run || value.Detail.Run.WorkspaceID != params.Workspace ||
			value.Detail.Run.Revision != params.ExpectedRevision+1 || value.Detail.Run.Status != domain.RunFailed ||
			value.Detail.Run.FailureCode != "runtime_retired_by_owner" || value.Detail.Task.Status != domain.TaskBlocked ||
			value.Resolution.RunID != params.Run || value.Resolution.LostRevision != params.ExpectedRevision ||
			value.Resolution.Resolution != "owner_confirmed_effects_ended" || value.Resolution.Note != params.Note ||
			value.Resolution.EventSequence != value.EventSequence || value.Resolution.ResolvedBy != "local-owner" {
			return fmt.Errorf("decode local API result %s: lost-run resolution does not match the exact retired target", method)
		}
	case *FullDoctorResult:
		checkOrder := FullDoctorCheckOrder()
		if len(value.Checks) != len(checkOrder) {
			return fmt.Errorf("decode local API result %s: full doctor check registry is incomplete", method)
		}
		failed, warning := false, false
		for index, code := range checkOrder {
			check := value.Checks[index]
			if check.Code != code || check.IssueCount > check.CheckedCount {
				return fmt.Errorf("decode local API result %s: full doctor check %d is inconsistent", method, index)
			}
			failed = failed || check.Status == "failed"
			warning = warning || check.Status == "warning"
			if check.Status == "ok" && check.IssueCount != 0 {
				return fmt.Errorf("decode local API result %s: successful full doctor check reports issues", method)
			}
		}
		if (value.Status == "failed") != failed || (value.Status == "degraded") != (!failed && warning) ||
			(value.Status == "ok") != (!failed && !warning) {
			return fmt.Errorf("decode local API result %s: full doctor status disagrees with its checks", method)
		}
	case *BackupCreateResult:
		params := paramsValue.(BackupCreateParams)
		if value.Backup.Path != params.TargetPath || value.Backup.TotalBytes < 1 {
			return fmt.Errorf("decode local API result %s: backup does not match the requested target", method)
		}
	case *ApprovalMutationResult:
		params := paramsValue.(ApprovalDecisionParams)
		if value.Approval.ID != params.Approval || value.Action.ID != value.Approval.ActionID || value.Approval.ProjectID != value.Action.ProjectID ||
			value.Approval.WorkspaceID != params.Workspace || value.Action.WorkspaceID != params.Workspace {
			return fmt.Errorf("decode local API result %s: approval mutation does not match the requested target", method)
		}
		if value.Approval.DecisionNote != params.DecisionNote || value.Action.Decision != params.DecisionNote || value.Action.ApprovalID != value.Approval.ID ||
			value.Action.Revision != value.Approval.ExpectedActionRevision+1 {
			return fmt.Errorf("decode local API result %s: approval mutation decision linkage differs", method)
		}
		switch method {
		case MethodApprovalAllow:
			if value.Approval.Status != domain.ApprovalConsumed || value.Action.Status != domain.SupervisorActionApplied || value.Approval.Revision != params.ExpectedRevision+2 {
				return fmt.Errorf("decode local API result %s: allow returned the wrong governed outcome", method)
			}
		case MethodApprovalDeny:
			if value.Approval.Status != domain.ApprovalDenied || value.Action.Status != domain.SupervisorActionDismissed || value.Approval.Revision != params.ExpectedRevision+1 {
				return fmt.Errorf("decode local API result %s: deny returned the wrong governed outcome", method)
			}
		}
	case *BriefingShowResult:
		params := paramsValue.(BriefingShowParams)
		scope := value.Briefing.Scope
		if scope.WorkspaceID != params.Workspace || !briefingScopeMatches(params, scope) {
			return fmt.Errorf("decode local API result %s: briefing does not match the requested scope", method)
		}
	case *BriefingExplainResult:
		params := paramsValue.(BriefingExplainParams)
		if value.Explanation.BriefingID != params.Briefing || value.Explanation.Claim.ID != params.Claim {
			return fmt.Errorf("decode local API result %s: explanation does not match the requested briefing claim", method)
		}
	}
	return nil
}

func briefingScopeMatches(params BriefingShowParams, scope domain.BriefingScope) bool {
	if scope.Type != params.ScopeType {
		return false
	}
	switch params.ScopeType {
	case "workspace":
		return scope.WorkspaceID == params.ScopeIdentifier
	case "project":
		return scope.ProjectID == params.ScopeIdentifier
	case "objective":
		return scope.ObjectiveID == params.ScopeIdentifier
	case "task":
		return scope.TaskID == params.ScopeIdentifier
	default:
		return false
	}
}

func validReturnedDomainAttentionTree(values map[string]domain.DomainAgentMembership) bool {
	for agentID, membership := range values {
		if membership.Status != domain.DomainAgentActive || membership.ParentAgentID == "" {
			continue
		}
		parent, ok := values[membership.ParentAgentID]
		if !ok || parent.Status != domain.DomainAgentActive || membership.ParentAgentID == agentID {
			return false
		}
		seen := map[string]bool{agentID: true}
		current := membership.ParentAgentID
		for current != "" {
			if seen[current] {
				return false
			}
			seen[current] = true
			ancestor, ok := values[current]
			if !ok || ancestor.Status != domain.DomainAgentActive {
				return false
			}
			current = ancestor.ParentAgentID
		}
	}
	return true
}

func validateOperatorScope(method, requestedWorkspace, requestedProject, actualWorkspace, actualProject string) error {
	if actualWorkspace != requestedWorkspace {
		return fmt.Errorf("decode local API result %s: record workspace scope mismatch", method)
	}
	if requestedProject != "" && actualProject != requestedProject {
		return fmt.Errorf("decode local API result %s: record project scope mismatch", method)
	}
	return nil
}

func validateOperatorDiscriminator(method string, result any, expected [2]string) error {
	var schema, kind string
	switch value := result.(type) {
	case *WorkspaceShowResult:
		schema, kind = value.Schema, value.Type
	case *ProjectShowResult:
		schema, kind = value.Schema, value.Type
	case *AgentShowResult:
		schema, kind = value.Schema, value.Type
	case *WorkspaceListResult:
		schema, kind = value.Schema, value.Type
	case *ProjectListResult:
		schema, kind = value.Schema, value.Type
	case *AgentListResult:
		schema, kind = value.Schema, value.Type
	case *DomainAgentTreeResult:
		schema, kind = value.Schema, value.Type
	case *DomainAgentSessionResult:
		schema, kind = value.Schema, value.Type
	case *ObjectiveListResult:
		schema, kind = value.Schema, value.Type
	case *TaskListResult:
		schema, kind = value.Schema, value.Type
	case *RunListResult:
		schema, kind = value.Schema, value.Type
	case *ClaimListResult:
		schema, kind = value.Schema, value.Type
	case *OverlapListResult:
		schema, kind = value.Schema, value.Type
	case *DriftListResult:
		schema, kind = value.Schema, value.Type
	case *MeetingListResult:
		schema, kind = value.Schema, value.Type
	case *EventsListResult:
		schema, kind = value.Schema, value.Type
	case *EventsTimelineResult:
		schema, kind = value.Schema, value.Type
	default:
		return fmt.Errorf("decode local API result %s: result has no discriminator", method)
	}
	if schema != expected[0] || kind != expected[1] {
		return fmt.Errorf("decode local API result %s: discriminator is %q/%q, want %q/%q", method, schema, kind, expected[0], expected[1])
	}
	return nil
}

func scopeIdentifierMatches(identifier, canonicalID, name string, idPattern *regexp.Regexp) bool {
	if idPattern.MatchString(identifier) {
		return canonicalID == identifier
	}
	return name == identifier
}

func (c *Client) resolveOperatorWorkspace(ctx context.Context, identifier string) (string, error) {
	if canonicalWorkspaceIDPattern.MatchString(identifier) {
		return identifier, nil
	}
	result, err := c.WorkspaceShow(ctx, identifier)
	if err != nil {
		return "", err
	}
	return result.Workspace.ID, nil
}

func (c *Client) resolveOperatorProject(ctx context.Context, workspaceID, identifier string) (string, error) {
	if identifier == "" || canonicalProjectIDPattern.MatchString(identifier) {
		return identifier, nil
	}
	result, err := c.ProjectShow(ctx, workspaceID, identifier)
	if err != nil {
		return "", err
	}
	return result.Project.ID, nil
}

func (c *Client) resolveOperatorAgent(ctx context.Context, workspaceID, identifier string) (string, error) {
	if canonicalAgentIDPattern.MatchString(identifier) {
		return identifier, nil
	}
	result, err := c.AgentShow(ctx, workspaceID, identifier)
	if err != nil {
		return "", err
	}
	return result.Agent.ID, nil
}

func (c *Client) resolveOperatorScope(ctx context.Context, workspace, project string) (string, string, error) {
	workspaceID, err := c.resolveOperatorWorkspace(ctx, workspace)
	if err != nil {
		return "", "", err
	}
	projectID, err := c.resolveOperatorProject(ctx, workspaceID, project)
	if err != nil {
		return "", "", err
	}
	return workspaceID, projectID, nil
}

func requestedPageLimit(requested, maximum int) int {
	if requested == 0 {
		return defaultOperatorPageLimit
	}
	if requested < 0 || requested > maximum {
		return maximum
	}
	return requested
}

func validatePage(method string, page PageResult, itemCount, limit int) error {
	if len(page.NextCursor) > maximumCursorBytes {
		return fmt.Errorf("decode local API result %s: next cursor exceeds %d bytes", method, maximumCursorBytes)
	}
	if page.HasMore != (page.NextCursor != "") {
		return fmt.Errorf("decode local API result %s: has_more and next_cursor disagree", method)
	}
	if page.Total < int64(itemCount) || itemCount > limit || (itemCount == 0 && page.Total != 0) || (page.HasMore && itemCount == 0) {
		return fmt.Errorf("decode local API result %s: invalid page bounds", method)
	}
	return nil
}

func validateForwardEvents(params EventsListParams, result EventsListResult) error {
	if err := validatePage(MethodEventsList, result.PageResult, len(result.Events), requestedPageLimit(params.Limit, maximumEventPageLimit)); err != nil {
		return err
	}
	if result.HighWater < 0 {
		return fmt.Errorf("decode local API result %s: negative high-water", MethodEventsList)
	}
	if !canonicalWorkspaceIDPattern.MatchString(result.WorkspaceID) {
		return fmt.Errorf("decode local API result %s: workspace scope mismatch", MethodEventsList)
	}
	if result.WorkspaceID != params.Workspace {
		return fmt.Errorf("decode local API result %s: workspace scope mismatch", MethodEventsList)
	}
	if result.HighWater < params.After && (len(result.Events) != 0 || result.Total != 0 || result.HasMore) {
		return fmt.Errorf("decode local API result %s: rewind response contains events", MethodEventsList)
	}
	previous := params.After
	eventIDs := make(map[string]struct{}, len(result.Events))
	for index, event := range result.Events {
		if err := validateCanonicalEvent(MethodEventsList, event); err != nil {
			return err
		}
		if event.WorkspaceID != result.WorkspaceID || event.Sequence <= previous || event.Sequence > result.HighWater {
			return fmt.Errorf("decode local API result %s: event %d has nonmonotonic sequence %d", MethodEventsList, index, event.Sequence)
		}
		if _, duplicate := eventIDs[event.EventID]; duplicate {
			return fmt.Errorf("decode local API result %s: duplicate event id %q", MethodEventsList, event.EventID)
		}
		eventIDs[event.EventID] = struct{}{}
		previous = event.Sequence
	}
	return nil
}

func validateReverseTimeline(params EventsTimelineParams, result EventsTimelineResult) error {
	if err := validatePage(MethodEventsTimeline, result.PageResult, len(result.Events), requestedPageLimit(params.Limit, maximumOperatorPageLimit)); err != nil {
		return err
	}
	if result.HighWater < 0 {
		return fmt.Errorf("decode local API result %s: negative high-water", MethodEventsTimeline)
	}
	if !canonicalWorkspaceIDPattern.MatchString(result.WorkspaceID) {
		return fmt.Errorf("decode local API result %s: workspace scope mismatch", MethodEventsTimeline)
	}
	if result.WorkspaceID != params.Workspace {
		return fmt.Errorf("decode local API result %s: workspace scope mismatch", MethodEventsTimeline)
	}
	var previous int64
	eventIDs := make(map[string]struct{}, len(result.Events))
	for index, event := range result.Events {
		if err := validateCanonicalEvent(MethodEventsTimeline, event); err != nil {
			return err
		}
		if event.WorkspaceID != result.WorkspaceID || (index > 0 && event.Sequence >= previous) || event.Sequence > result.HighWater || event.Entity.Type != params.EntityType || event.Entity.ID != params.EntityID {
			return fmt.Errorf("decode local API result %s: timeline event %d violates scope or reverse order", MethodEventsTimeline, index)
		}
		if _, duplicate := eventIDs[event.EventID]; duplicate {
			return fmt.Errorf("decode local API result %s: duplicate event id %q", MethodEventsTimeline, event.EventID)
		}
		eventIDs[event.EventID] = struct{}{}
		previous = event.Sequence
	}
	return nil
}

func validateCanonicalEvent(method string, event domain.Event) error {
	if !domain.KnownEventType(event.Type) {
		return &APIError{
			Code:      "unsupported_operator_event",
			Message:   fmt.Sprintf("%s returned unsupported event type %q at sequence %d", method, event.Type, event.Sequence),
			Retryable: false,
		}
	}
	if !domain.ValidEvent(event) {
		return fmt.Errorf("decode local API result %s: malformed canonical event", method)
	}
	return nil
}
