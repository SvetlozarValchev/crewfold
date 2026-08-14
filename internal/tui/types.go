// Package tui implements Crewfold's single terminal operator control panel.
package tui

import (
	"context"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

const (
	maxRouteDepth      = 16
	maxFilterRunes     = 256
	maxActivityEvents  = 200
	maxNotifications   = 100
	maxInboxItems      = 50
	maxCachedRecords   = 600
	maxCollectionPages = 3
	maxConcurrentReads = 4
	stopGraceMillis    = int64(5000)
	eventPageSize      = 1000
	maxEventPages      = 10
	eventPollInterval  = 500 * time.Millisecond
)

type canonicalSection uint8

const (
	sectionBriefing canonicalSection = iota + 1
	sectionObjectives
	sectionTasks
	sectionRuns
	sectionAgents
	sectionApprovals
	sectionChecks
	sectionClaims
	sectionOverlaps
	sectionDrifts
	sectionMeetings
)

var canonicalSections = [...]canonicalSection{
	sectionBriefing,
	sectionObjectives,
	sectionTasks,
	sectionRuns,
	sectionAgents,
	sectionApprovals,
	sectionChecks,
	sectionClaims,
	sectionOverlaps,
	sectionDrifts,
	sectionMeetings,
}

type sectionLoadState uint8

const (
	sectionPending sectionLoadState = iota + 1
	sectionLoading
	sectionLoaded
	sectionFailed
)

// ColorMode controls whether the dashboard emits terminal colors.
type ColorMode string

const (
	ColorAuto  ColorMode = "auto"
	ColorNever ColorMode = "never"
)

// Config selects the daemon and initial owner scope. An empty workspace opens
// the workspace chooser; an empty project selects the whole workspace.
type Config struct {
	SocketPath string
	Workspace  string
	Project    string
	Color      ColorMode
}

// ConnectionState is the closed operator-visible connection state machine.
type ConnectionState uint8

const (
	ConnectionConnecting ConnectionState = iota + 1
	ConnectionSyncing
	ConnectionLive
	ConnectionReconnecting
	ConnectionFatal
)

func (state ConnectionState) String() string {
	switch state {
	case ConnectionConnecting:
		return "connecting"
	case ConnectionSyncing:
		return "syncing"
	case ConnectionLive:
		return "live"
	case ConnectionReconnecting:
		return "reconnecting"
	case ConnectionFatal:
		return "fatal"
	default:
		return "fatal"
	}
}

// Focus identifies exactly one keyboard target.
type Focus uint8

const (
	FocusNavigation Focus = iota + 1
	FocusRecords
	FocusDetail
	FocusModal
)

func (focus Focus) String() string {
	switch focus {
	case FocusNavigation:
		return "navigation"
	case FocusRecords:
		return "records"
	case FocusDetail:
		return "detail"
	case FocusModal:
		return "modal"
	default:
		return "navigation"
	}
}

// Route is the closed set of top-level operator screens.
type Route uint8

const (
	RouteOverview Route = iota + 1
	RouteBriefing
	RouteWork
	RouteDecisions
	RouteChecks
	RouteCoordination
	RouteActivity
)

var routes = [...]Route{
	RouteOverview,
	RouteBriefing,
	RouteWork,
	RouteDecisions,
	RouteChecks,
	RouteCoordination,
	RouteActivity,
}

func (route Route) String() string {
	switch route {
	case RouteOverview:
		return "Overview"
	case RouteBriefing:
		return "Briefing"
	case RouteWork:
		return "Work"
	case RouteDecisions:
		return "Decisions"
	case RouteChecks:
		return "Checks"
	case RouteCoordination:
		return "Coordination"
	case RouteActivity:
		return "Activity"
	default:
		return "Overview"
	}
}

type layoutMode uint8

const (
	layoutTooSmall layoutMode = iota + 1
	layoutOnePane
	layoutTwoPane
	layoutThreePane
)

type modalKind uint8

const (
	modalNone modalKind = iota
	modalHelp
	modalFilter
	modalActions
	modalReview
	modalError
	modalWorkspace
)

type recordKind uint8

const (
	recordSummary recordKind = iota + 1
	recordBriefing
	recordBriefingClaim
	recordObjective
	recordTask
	recordRun
	recordAgent
	recordInbox
	recordApproval
	recordCheck
	recordClaim
	recordOverlap
	recordDrift
	recordMeeting
	recordNotification
	recordEvent
)

type notificationKind uint8

const (
	notificationOwnerAttention notificationKind = iota + 1
	notificationFailure
	notificationConflict
)

type notification struct {
	ID            string
	Kind          notificationKind
	EventSequence int64
	EventType     string
	EntityType    string
	EntityID      string
	OccurredAt    string
}

type actionKind uint8

const (
	actionAttachRun actionKind = iota + 1
	actionResumeRun
	actionStopRun
	actionAllowApproval
	actionDenyApproval
)

func (kind actionKind) String() string {
	switch kind {
	case actionAttachRun:
		return "Attach to run"
	case actionResumeRun:
		return "Resume run"
	case actionStopRun:
		return "Stop run"
	case actionAllowApproval:
		return "Allow approval"
	case actionDenyApproval:
		return "Deny approval"
	default:
		return "Unavailable action"
	}
}

type routeFrame struct {
	Route      Route
	EntityType string
	EntityID   string
	TargetIDs  []string
	Diagnosis  string
}

type cursorState struct {
	Applied   int64
	Candidate int64
	HighWater int64
}

type entityTimeline struct {
	HighWater int64
	Events    []domain.Event
	Total     int
	HasMore   bool
}

type snapshot struct {
	Workspace        domain.Workspace
	Workspaces       []domain.Workspace
	Project          *domain.Project
	Briefing         domain.ManagementBriefing
	Objectives       []domain.Objective
	Tasks            []domain.TaskDetail
	Runs             []domain.RunSummary
	Agents           []domain.AgentDefinition
	Approvals        []domain.ApprovalRequest
	Checks           []domain.CheckRunListItem
	Claims           []domain.WorkClaim
	Overlaps         []domain.WorkOverlap
	Drifts           []domain.ClaimDrift
	Meetings         []domain.Meeting
	Events           []domain.Event
	Notifications    []notification
	ObjectiveTotal   int
	ObjectiveHasMore bool
	TaskTotal        int
	TaskHasMore      bool
	RunTotal         int
	RunHasMore       bool
	AgentTotal       int
	AgentHasMore     bool
	ApprovalTotal    int
	ApprovalHasMore  bool
	CheckTotal       int
	CheckHasMore     bool
	ClaimTotal       int
	ClaimHasMore     bool
	OverlapTotal     int
	OverlapHasMore   bool
	DriftTotal       int
	DriftHasMore     bool
	MeetingTotal     int
	MeetingHasMore   bool
}

type record struct {
	Kind         recordKind
	ID           string
	Revision     int64
	Primary      string
	Secondary    string
	Claim        *domain.BriefingClaim
	Explanation  *domain.BriefingClaimExplanation
	Objective    *domain.Objective
	Task         *domain.TaskDetail
	Run          *domain.RunSummary
	Agent        *domain.AgentDefinition
	Inbox        *domain.InboxItem
	Approval     *domain.ApprovalRequest
	Check        *domain.CheckRunListItem
	WorkClaim    *domain.WorkClaim
	Overlap      *domain.WorkOverlap
	Drift        *domain.ClaimDrift
	Meeting      *domain.Meeting
	Event        *domain.Event
	Notification *notification
	Briefing     *domain.ManagementBriefing
	Targets      []string
	DrillRoute   Route
	Diagnosis    string
}

type actionChoice struct {
	Kind                   actionKind
	TargetType             string
	TargetID               string
	Revision               int64
	Consequence            string
	RequiresNote           bool
	ApprovalActionID       string
	ExpectedActionRevision int64
	GracePeriodMillis      int64
}

type actionReview struct {
	Choice             actionChoice
	Approval           domain.ApprovalRequest
	SupervisorAction   domain.SupervisorAction
	HasApprovalContext bool
	IdempotencyKey     string
	Generation         uint64
	Executing          bool
	RequestFrozen      bool
	AmbiguousError     string
	DecisionNote       string
	CancelRequested    bool
	cancel             context.CancelFunc
}

type modalState struct {
	Kind         modalKind
	Choices      []actionChoice
	ChoiceIndex  int
	ReviewOffset int
	Review       actionReview
	Message      string
	Workspaces   []domain.Workspace
}

// Model is Crewfold's one Bubble Tea state machine. All asynchronous work
// returns messages; no command mutates this value from a goroutine.
type Model struct {
	config    Config
	client    *localapi.Client
	ctx       context.Context
	ioSlots   chan struct{}
	eventSlot chan struct{}

	connection ConnectionState
	cursors    cursorState
	snapshot   snapshot

	routeStack   []routeFrame
	focus        Focus
	selection    map[Route]string
	rowOffset    map[Route]int
	detailOffset int

	width  int
	height int

	filter           textinput.Model
	actionNote       textinput.Model
	filterText       string
	modal            modalState
	modalReturnFocus Focus

	loadGeneration             uint64
	loadInFlight               bool
	loadCtx                    context.Context
	loadCancel                 context.CancelFunc
	loadPending                []canonicalSection
	loadActive                 int
	loadStates                 map[canonicalSection]sectionLoadState
	loadSnapshot               snapshot
	loadScopeChanged           bool
	loadTarget                 int64
	loadHighWater              int64
	pollInFlight               bool
	pollEpoch                  uint64
	pollActiveEpoch            uint64
	reconnectEpoch             uint64
	actionGeneration           uint64
	attachEpoch                uint64
	activeAttachEpoch          uint64
	inboxEpoch                 uint64
	activeInboxEpoch           uint64
	briefingExplainEpoch       uint64
	activeBriefingExplainEpoch uint64
	timelineEpoch              uint64
	activeTimelineEpoch        uint64
	reconnectTry               int
	lastError                  string
	statusLine                 string
	colorEnabled               bool

	dirty                map[Route]bool
	inboxes              map[string][]domain.InboxItem
	briefingExplanations map[string]domain.BriefingClaimExplanation
	entityTimelines      map[string]entityTimeline
}

var _ tea.Model = Model{}
