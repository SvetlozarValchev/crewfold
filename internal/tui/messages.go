package tui

import (
	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

type scopeLoadedMsg struct {
	Generation       uint64
	Workspace        domain.Workspace
	Project          *domain.Project
	TargetCursor     int64
	HighWater        int64
	Rewind           bool
	Fatal            bool
	Err              error
	WorkspaceChoices []domain.Workspace
}

type sectionLoadedMsg struct {
	Generation uint64
	Section    canonicalSection
	Briefing   domain.ManagementBriefing
	Objectives []domain.Objective
	Tasks      []domain.TaskDetail
	Runs       []domain.RunSummary
	Agents     []domain.AgentDefinition
	Approvals  []domain.ApprovalRequest
	Checks     []domain.CheckRunListItem
	Claims     []domain.WorkClaim
	Overlaps   []domain.WorkOverlap
	Drifts     []domain.ClaimDrift
	Meetings   []domain.Meeting
	Total      int
	HasMore    bool
	Fatal      bool
	Err        error
}

type fenceLoadedMsg struct {
	Generation uint64
	HighWater  int64
	Fatal      bool
	Err        error
}

type pollTickMsg struct{ Epoch uint64 }

type reconnectTickMsg struct{ Epoch uint64 }

type eventsPolledMsg struct {
	Generation uint64
	After      int64
	Fence      bool
	PollEpoch  uint64
	Events     []domain.Event
	Candidate  int64
	HighWater  int64
	Rewind     bool
	Err        error
}

type actionPreparedMsg struct {
	Generation          uint64
	CanonicalGeneration uint64
	WorkspaceID         string
	Choice              actionChoice
	SupervisorAction    domain.SupervisorAction
	HasSupervisorAction bool
	IdempotencyKey      string
	Err                 error
}

type actionCompletedMsg struct {
	Generation     uint64
	IdempotencyKey string
	Kind           actionKind
	Err            error
}

type attachReadyMsg struct {
	Generation     uint64
	IdempotencyKey string
	Result         localapi.RunAttachResult
	Err            error
}

type attachFinishedMsg struct {
	Epoch uint64
	Err   error
}

type inboxLoadedMsg struct {
	Generation  uint64
	Epoch       uint64
	WorkspaceID string
	AgentID     string
	Items       []domain.InboxItem
	Err         error
}

type briefingExplainLoadedMsg struct {
	Generation  uint64
	Epoch       uint64
	WorkspaceID string
	BriefingID  string
	ClaimID     string
	Explanation domain.BriefingClaimExplanation
	Err         error
}

type timelineLoadedMsg struct {
	Generation  uint64
	Epoch       uint64
	WorkspaceID string
	EntityType  string
	EntityID    string
	Timeline    entityTimeline
	Rewind      bool
	Err         error
}
