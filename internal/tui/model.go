package tui

import (
	"context"
	"os"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

// NewModel constructs the dashboard around the concrete current local API
// client. A nil client is replaced with one for Config.SocketPath.
func NewModel(config Config, client *localapi.Client) Model {
	if config.Color == "" {
		config.Color = ColorAuto
	}
	if client == nil {
		client = localapi.NewClient(config.SocketPath)
	}
	filter := newFilter()
	actionNote := newActionNote()
	baseContext := context.Background()
	loadContext, loadCancel := context.WithCancel(baseContext)
	_, noColor := os.LookupEnv("NO_COLOR")
	colorEnabled := config.Color != ColorNever && !noColor
	return Model{
		config:               config,
		client:               client,
		ctx:                  baseContext,
		ioSlots:              make(chan struct{}, maxConcurrentReads),
		eventSlot:            make(chan struct{}, 1),
		connection:           ConnectionConnecting,
		focus:                FocusNavigation,
		routeStack:           []routeFrame{{Route: RouteOverview}},
		selection:            make(map[Route]string, len(routes)),
		rowOffset:            make(map[Route]int, len(routes)),
		filter:               filter,
		actionNote:           actionNote,
		loadGeneration:       1,
		loadInFlight:         true,
		loadCtx:              loadContext,
		loadCancel:           loadCancel,
		loadStates:           make(map[canonicalSection]sectionLoadState, len(canonicalSections)),
		dirty:                make(map[Route]bool, len(routes)),
		inboxes:              make(map[string][]domain.InboxItem),
		briefingExplanations: make(map[string]domain.BriefingClaimExplanation),
		entityTimelines:      make(map[string]entityTimeline),
		colorEnabled:         colorEnabled,
	}
}

func newActionNote() textinput.Model {
	note := textinput.New()
	note.Prompt = "Owner note: "
	note.Placeholder = "why this decision is appropriate"
	note.CharLimit = 1024
	note.SetWidth(256)
	return note
}

func (model Model) actionsReady() bool {
	if model.connection != ConnectionLive || model.loadInFlight || model.layout() == layoutTooSmall {
		return false
	}
	if model.cursors.Applied != model.cursors.Candidate || model.cursors.Applied != model.cursors.HighWater {
		return false
	}
	for _, dirty := range model.dirty {
		if dirty {
			return false
		}
	}
	return true
}

func (model *Model) openModal(modal modalState) {
	if model.modal.Kind == modalNone && model.focus != FocusModal {
		model.modalReturnFocus = model.focus
	}
	model.modal = modal
	model.focus = FocusModal
}

func (model *Model) closeModal() {
	model.filter.Blur()
	model.actionNote.Blur()
	model.modal = modalState{}
	model.actionGeneration++
	switch model.modalReturnFocus {
	case FocusNavigation, FocusRecords, FocusDetail:
		model.focus = model.modalReturnFocus
	default:
		model.focus = FocusRecords
	}
	model.modalReturnFocus = 0
}

func newFilter() textinput.Model {
	filter := textinput.New()
	filter.Prompt = "/ "
	filter.Placeholder = "filter visible records"
	filter.CharLimit = maxFilterRunes
	filter.SetWidth(maxFilterRunes)
	return filter
}

// Init starts the first canonical load. It never starts or stops the daemon.
func (model Model) Init() tea.Cmd {
	return loadScopeCmd(model.loadCtx, model.ioSlots, model.eventSlot, model.client, model.config, model.loadGeneration, 0)
}

func (model Model) currentRoute() Route {
	if len(model.routeStack) == 0 {
		return RouteOverview
	}
	return model.routeStack[len(model.routeStack)-1].Route
}

func (model Model) currentFrame() routeFrame {
	if len(model.routeStack) == 0 {
		return routeFrame{Route: RouteOverview}
	}
	return model.routeStack[len(model.routeStack)-1]
}

func (model Model) layout() layoutMode {
	if model.width < 60 || model.height < 18 {
		return layoutTooSmall
	}
	if model.width >= 120 && model.height >= 32 {
		return layoutThreePane
	}
	if model.width >= 80 && model.height >= 24 {
		return layoutTwoPane
	}
	return layoutOnePane
}

func (model Model) colorsEnabled() bool {
	return model.colorEnabled
}

func (model *Model) pushRoute(frame routeFrame) {
	if len(model.routeStack) >= maxRouteDepth {
		model.evictFrameCache(model.routeStack[0])
		copy(model.routeStack, model.routeStack[1:])
		model.routeStack = model.routeStack[:maxRouteDepth-1]
	}
	model.routeStack = append(model.routeStack, frame)
	model.detailOffset = 0
}

func (model *Model) evictFrameCache(frame routeFrame) {
	owners := 0
	for _, candidate := range model.routeStack {
		if framesShareAsyncCache(frame, candidate) {
			owners++
		}
	}
	if owners > 1 {
		return
	}
	if frame.Route == RouteCoordination && frame.EntityType == "agent_inbox" {
		delete(model.inboxes, frame.EntityID)
	}
	if frame.EntityType == "briefing_claim" {
		delete(model.briefingExplanations, briefingExplanationKey(model.snapshot.Briefing.ID, frame.EntityID))
	}
	if isTimelineEntity(frame.EntityType, frame.EntityID) {
		delete(model.entityTimelines, entityTimelineKey(model.snapshot.Workspace.ID, frame.EntityType, frame.EntityID))
	}
}

func framesShareAsyncCache(left, right routeFrame) bool {
	if left.Route == RouteCoordination && left.EntityType == "agent_inbox" {
		return right.Route == RouteCoordination && right.EntityType == left.EntityType && right.EntityID == left.EntityID
	}
	if left.EntityType == "briefing_claim" || isTimelineEntity(left.EntityType, left.EntityID) {
		return right.EntityType == left.EntityType && right.EntityID == left.EntityID
	}
	return false
}

func (model *Model) leaveFrame(frame routeFrame) {
	if frame.Route == RouteCoordination && frame.EntityType == "agent_inbox" {
		model.inboxEpoch++
		model.activeInboxEpoch = 0
	}
	if frame.EntityType == "briefing_claim" {
		model.briefingExplainEpoch++
		model.activeBriefingExplainEpoch = 0
	}
	if isTimelineEntity(frame.EntityType, frame.EntityID) {
		model.timelineEpoch++
		model.activeTimelineEpoch = 0
	}
	model.evictFrameCache(frame)
}

func (model *Model) popRoute() bool {
	if len(model.routeStack) <= 1 {
		return false
	}
	model.routeStack = model.routeStack[:len(model.routeStack)-1]
	model.detailOffset = 0
	return true
}

func (model *Model) preserveSelection(route Route, records []record) {
	selected := model.selection[route]
	for _, item := range records {
		if item.ID == selected {
			return
		}
	}
	if len(records) == 0 {
		delete(model.selection, route)
		model.rowOffset[route] = 0
		return
	}
	model.selection[route] = records[0].ID
	model.rowOffset[route] = 0
}

func matchesFilter(item record, filter string) bool {
	if filter == "" {
		return true
	}
	haystack := strings.ToLower(item.ID + " " + item.Primary + " " + item.Secondary)
	return strings.Contains(haystack, strings.ToLower(filter))
}
