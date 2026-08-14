package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

// Update is the only place where asynchronous results change dashboard state.
func (model Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		model.width, model.height = message.Width, message.Height
		return model, nil
	case scopeLoadedMsg:
		return model.updateScopeLoaded(message)
	case sectionLoadedMsg:
		return model.updateSectionLoaded(message)
	case fenceLoadedMsg:
		return model.updateFenceLoaded(message)
	case pollTickMsg:
		if message.Epoch != model.pollEpoch || model.connection != ConnectionLive || model.pollInFlight || model.loadInFlight {
			return model, nil
		}
		model.pollInFlight = true
		model.pollActiveEpoch = message.Epoch
		return model, pollEventsCmd(model.ctx, model.eventSlot, model.client, model.snapshot.Workspace.ID, model.loadGeneration, model.cursors.Applied, false, message.Epoch)
	case reconnectTickMsg:
		if message.Epoch != model.reconnectEpoch || model.connection != ConnectionReconnecting || model.loadInFlight {
			return model, nil
		}
		return model, model.restartCanonicalLoad(model.reloadCursor())
	case eventsPolledMsg:
		if message.Fence {
			return model.updateFenceEvents(message)
		}
		return model.updateEventsPolled(message)
	case actionPreparedMsg:
		return model.updateActionPrepared(message)
	case actionCompletedMsg:
		if !model.matchesActionResult(message.Generation, message.IdempotencyKey) {
			return model, nil
		}
		return model.updateActionCompleted(message)
	case attachReadyMsg:
		if !model.matchesActionResult(message.Generation, message.IdempotencyKey) {
			return model, nil
		}
		if model.modal.Review.cancel != nil {
			model.modal.Review.cancel()
			model.modal.Review.cancel = nil
		}
		if model.modal.Review.CancelRequested {
			model.modal.Review.Executing = false
			model.modal.Review.AmbiguousError = "The attach request was canceled before a child process started."
			return model, nil
		}
		if message.Err != nil {
			model.openModal(modalState{Kind: modalError, Message: sanitizeLine(message.Err.Error())})
			return model, nil
		}
		if err := validateAttachResult(message.Result, model.modal.Review.Choice.TargetID); err != nil {
			diagnosis := sanitizeLine(err.Error())
			model.connection = ConnectionFatal
			model.lastError = diagnosis
			model.openModal(modalState{Kind: modalError, Message: diagnosis})
			return model, nil
		}
		command, err := attachmentCommand(model.ctx, message.Result)
		if err != nil {
			model.openModal(modalState{Kind: modalError, Message: sanitizeLine(err.Error())})
			return model, nil
		}
		model.attachEpoch++
		model.activeAttachEpoch = model.attachEpoch
		attachEpoch := model.activeAttachEpoch
		model.closeModal()
		model.statusLine = "Attaching to run " + sanitizeLine(message.Result.RunID)
		return model, tea.ExecProcess(command, func(err error) tea.Msg { return attachFinishedMsg{Epoch: attachEpoch, Err: err} })
	case attachFinishedMsg:
		if message.Epoch == 0 || message.Epoch != model.activeAttachEpoch {
			return model, nil
		}
		model.activeAttachEpoch = 0
		if message.Err != nil {
			diagnosis := sanitizeLine(message.Err.Error())
			model.lastError = diagnosis
			model.statusLine = "Attached process exited with an error: " + diagnosis
			if model.modal.Kind == modalNone {
				model.openModal(modalState{Kind: modalError, Message: diagnosis})
			}
		} else {
			model.statusLine = "Returned from attached run"
		}
		return model, model.restartCanonicalLoad(model.cursors.Applied)
	case inboxLoadedMsg:
		frame := model.currentFrame()
		if message.Generation != model.loadGeneration || message.Epoch == 0 || message.Epoch != model.activeInboxEpoch ||
			message.WorkspaceID != model.snapshot.Workspace.ID || frame.Route != RouteCoordination ||
			frame.EntityType != "agent_inbox" || frame.EntityID != message.AgentID {
			return model, nil
		}
		model.activeInboxEpoch = 0
		if message.Err != nil {
			diagnosis := sanitizeLine(message.Err.Error())
			model.lastError = diagnosis
			model.statusLine = "Agent inbox read failed: " + diagnosis
			if model.modal.Kind == modalNone {
				model.openModal(modalState{Kind: modalError, Message: diagnosis})
			}
			return model, nil
		}
		model.inboxes[message.AgentID] = append([]domain.InboxItem(nil), message.Items...)
		model.preserveSelection(RouteCoordination, model.recordsFor(RouteCoordination))
		model.statusLine = "Agent inbox synchronized"
		return model, nil
	case briefingExplainLoadedMsg:
		return model.updateBriefingExplanation(message)
	case timelineLoadedMsg:
		return model.updateTimeline(message)
	case tea.KeyPressMsg:
		return model.updateKey(message)
	default:
		return model, nil
	}
}

func (model Model) updateActionPrepared(message actionPreparedMsg) (tea.Model, tea.Cmd) {
	if message.Generation != model.actionGeneration || message.CanonicalGeneration != model.loadGeneration ||
		message.WorkspaceID != model.snapshot.Workspace.ID || model.modal.Kind != modalActions {
		return model, nil
	}
	if message.Err != nil {
		model.openModal(modalState{Kind: modalError, Message: sanitizeLine(message.Err.Error())})
		return model, nil
	}
	if !model.actionsReady() {
		model.statusLine = "Action preparation became stale; wait for the canonical dashboard to synchronize and choose it again"
		return model, nil
	}
	review := actionReview{
		Choice: message.Choice, IdempotencyKey: message.IdempotencyKey, Generation: message.Generation,
	}
	if isApprovalAction(message.Choice.Kind) {
		approval, ok := model.approvalForChoice(message.Choice)
		if !ok {
			model.openModal(modalState{Kind: modalError, Message: "The pending approval changed while its supervisor action was being resolved. Refresh and review it again."})
			return model, nil
		}
		if !message.HasSupervisorAction {
			model.openModal(modalState{Kind: modalError, Message: "The approval review was rejected because its canonical supervisor action was not returned."})
			return model, nil
		}
		if err := model.validateApprovalAction(approval, message.SupervisorAction); err != nil {
			model.openModal(modalState{Kind: modalError, Message: sanitizeLine(err.Error())})
			return model, nil
		}
		review.Approval = approval
		review.SupervisorAction = cloneSupervisorAction(message.SupervisorAction)
		review.HasApprovalContext = true
		review.Choice.Consequence = approvalConsequence(review.Choice.Kind, review.SupervisorAction)
	}
	model.actionNote.SetValue("")
	if review.Choice.RequiresNote {
		model.actionNote.Focus()
	}
	model.openModal(modalState{Kind: modalReview, Review: review})
	return model, nil
}

func (model Model) updateBriefingExplanation(message briefingExplainLoadedMsg) (tea.Model, tea.Cmd) {
	if message.Generation != model.loadGeneration || message.Epoch == 0 || message.Epoch != model.activeBriefingExplainEpoch ||
		message.WorkspaceID != model.snapshot.Workspace.ID ||
		message.BriefingID != model.snapshot.Briefing.ID {
		return model, nil
	}
	frame := model.currentFrame()
	if frame.EntityType != "briefing_claim" || frame.EntityID != message.ClaimID {
		return model, nil
	}
	model.activeBriefingExplainEpoch = 0
	if message.Err != nil {
		model.reportBackgroundReadError("Briefing explanation read failed", message.Err)
		return model, nil
	}
	claim, ok := model.briefingClaim(message.ClaimID)
	if !ok || message.Explanation.BriefingID != message.BriefingID ||
		!reflect.DeepEqual(message.Explanation.Claim, claim) ||
		!reflect.DeepEqual(message.Explanation.Provenance, claim.Sources) {
		model.reportBackgroundReadError("Briefing explanation rejected", errors.New("the response did not exactly match the selected canonical claim and was discarded"))
		return model, nil
	}
	model.briefingExplanations[briefingExplanationKey(message.BriefingID, message.ClaimID)] = cloneBriefingExplanation(message.Explanation)
	model.statusLine = "Briefing claim explanation synchronized"
	return model, nil
}

func (model Model) updateTimeline(message timelineLoadedMsg) (tea.Model, tea.Cmd) {
	frame := model.currentFrame()
	if message.Generation != model.loadGeneration || message.Epoch == 0 || message.Epoch != model.activeTimelineEpoch ||
		message.WorkspaceID != model.snapshot.Workspace.ID || frame.EntityType != message.EntityType || frame.EntityID != message.EntityID {
		return model, nil
	}
	model.activeTimelineEpoch = 0
	if message.Rewind {
		return model.discardRewoundJournal()
	}
	if message.Err != nil {
		if invalidatesCachedTruth(message.Err) {
			return model.invalidateFatalCache(message.Err)
		}
		model.reportBackgroundReadError("Entity timeline read failed", message.Err)
		return model, nil
	}
	if message.Timeline.HighWater < model.cursors.Applied {
		return model.discardRewoundJournal()
	}
	if message.Timeline.HighWater > model.cursors.Applied {
		model.statusLine = "Entity timeline observed newer journal state; synchronizing canonical sections before publishing it"
		return model, model.restartCanonicalLoad(model.cursors.Applied)
	}
	timeline := message.Timeline
	timeline.Events = append([]domain.Event(nil), message.Timeline.Events...)
	model.entityTimelines[entityTimelineKey(message.WorkspaceID, message.EntityType, message.EntityID)] = timeline
	model.detailOffset = 0
	model.statusLine = "Entity timeline synchronized"
	return model, nil
}

func (model *Model) reportBackgroundReadError(prefix string, err error) {
	diagnosis := sanitizeLine(err.Error())
	model.lastError = diagnosis
	model.statusLine = prefix + ": " + diagnosis
	if model.modal.Kind == modalNone {
		model.openModal(modalState{Kind: modalError, Message: model.statusLine})
	}
}

func (model Model) updateScopeLoaded(message scopeLoadedMsg) (tea.Model, tea.Cmd) {
	if message.Generation != model.loadGeneration || !model.loadInFlight {
		return model, nil
	}
	if len(message.WorkspaceChoices) > 0 {
		if model.loadCancel != nil {
			model.loadCancel()
		}
		model.loadInFlight = false
		model.connection = ConnectionSyncing
		model.snapshot.Workspaces = append([]domain.Workspace(nil), message.WorkspaceChoices...)
		model.openModal(modalState{Kind: modalWorkspace, Workspaces: append([]domain.Workspace(nil), message.WorkspaceChoices...)})
		return model, nil
	}
	if message.Err != nil {
		return model.failCanonicalLoad(message.Err, message.Fatal)
	}
	if message.Rewind {
		return model.discardRewoundJournal()
	}
	if model.config.Workspace == "" {
		// An implicit single-workspace choice becomes an explicit canonical scope
		// for this UI session. Later workspace creation must not reopen the chooser
		// over a frozen or ambiguously completed owner action.
		model.config.Workspace = message.Workspace.ID
	}
	changedScope := model.snapshot.Workspace.ID != message.Workspace.ID || projectID(model.snapshot.Project) != projectID(message.Project)
	stagedScopeMatches := model.loadSnapshot.Workspace.ID == message.Workspace.ID &&
		projectID(model.loadSnapshot.Project) == projectID(message.Project)
	stagedEvents := model.loadSnapshot.Events
	stagedNotifications := model.loadSnapshot.Notifications
	if changedScope {
		model.invalidateActionInteractionForScopeChange()
	}
	model.loadScopeChanged = changedScope
	if changedScope {
		model.loadSnapshot = snapshot{Workspace: message.Workspace, Project: message.Project}
	} else {
		model.loadSnapshot = model.snapshot
		model.loadSnapshot.Workspace = message.Workspace
		model.loadSnapshot.Project = message.Project
	}
	if stagedScopeMatches {
		model.loadSnapshot.Events = appendActivity(model.loadSnapshot.Events, stagedEvents)
		model.loadSnapshot.Notifications = mergeNotifications(model.loadSnapshot.Notifications, stagedNotifications)
	}
	model.loadTarget = message.TargetCursor
	model.loadHighWater = message.HighWater
	model.loadPending = append(model.loadPending[:0], canonicalSections[:]...)
	model.loadActive = 0
	model.loadStates = make(map[canonicalSection]sectionLoadState, len(canonicalSections))
	for _, section := range canonicalSections {
		model.loadStates[section] = sectionPending
	}
	for _, route := range routes {
		model.dirty[route] = true
	}
	model.connection = ConnectionSyncing
	model.statusLine = fmt.Sprintf("Synchronizing canonical sections 0/%d", len(canonicalSections))
	return model, model.launchSectionReads()
}

func (model *Model) invalidateActionInteractionForScopeChange() {
	switch model.modal.Kind {
	case modalActions:
		model.closeModal()
		model.statusLine = "Available actions were invalidated because the canonical workspace or project changed"
	case modalReview:
		unknownOutcome := model.modal.Review.Executing || model.modal.Review.RequestFrozen
		if model.modal.Review.cancel != nil {
			model.modal.Review.cancel()
		}
		model.actionGeneration++
		model.modal = modalState{}
		diagnosis := "The canonical workspace or project changed. The prior action review was invalidated and cannot be submitted in the new scope."
		if unknownOutcome {
			diagnosis += " Its prior outcome is unknown; inspect the original scope's canonical history before starting another action."
		}
		model.openModal(modalState{Kind: modalError, Message: diagnosis})
	}
}

func (model Model) updateSectionLoaded(message sectionLoadedMsg) (tea.Model, tea.Cmd) {
	if message.Generation != model.loadGeneration || !model.loadInFlight || model.loadStates[message.Section] != sectionLoading {
		return model, nil
	}
	if model.loadActive > 0 {
		model.loadActive--
	}
	if message.Err != nil {
		model.loadStates[message.Section] = sectionFailed
		return model.failCanonicalLoad(message.Err, message.Fatal)
	}
	model.loadStates[message.Section] = sectionLoaded
	model.applySection(message)
	loaded := 0
	for _, state := range model.loadStates {
		if state == sectionLoaded {
			loaded++
		}
	}
	model.statusLine = fmt.Sprintf("Synchronizing canonical sections %d/%d", loaded, len(canonicalSections))
	if model.loadActive != 0 || len(model.loadPending) != 0 {
		return model, model.launchSectionReads()
	}
	model.statusLine = "Validating canonical event fence"
	return model, fenceCanonicalCmd(model.loadCtx, model.eventSlot, model.client, model.loadGeneration, model.loadSnapshot.Workspace.ID, model.loadHighWater)
}

func (model Model) updateFenceLoaded(message fenceLoadedMsg) (tea.Model, tea.Cmd) {
	if message.Generation != model.loadGeneration || !model.loadInFlight {
		return model, nil
	}
	if message.Err != nil {
		return model.failCanonicalLoad(message.Err, message.Fatal)
	}
	if message.HighWater < model.loadHighWater {
		return model.discardRewoundJournal()
	}
	model.loadHighWater = message.HighWater
	if model.loadTarget < model.loadHighWater {
		model.statusLine = "Catching up events that arrived during canonical reads"
		return model, pollEventsCmd(model.loadCtx, model.eventSlot, model.client, model.loadSnapshot.Workspace.ID, model.loadGeneration, model.loadTarget, true, 0)
	}
	return model.finishCanonicalLoad()
}

func (model Model) updateFenceEvents(message eventsPolledMsg) (tea.Model, tea.Cmd) {
	if message.Generation != model.loadGeneration || !model.loadInFlight || message.After != model.loadTarget {
		return model, nil
	}
	if message.Err != nil {
		return model.failCanonicalLoad(message.Err, isDefinitiveAPIError(message.Err))
	}
	if message.Rewind || message.HighWater < model.loadHighWater {
		return model.discardRewoundJournal()
	}
	// Fence events belong to the pending canonical scope. Keeping them in the
	// published snapshot loses them on first bootstrap and can mix scopes during
	// a project/workspace switch. Stage them across the next same-scope load.
	model.loadSnapshot.Events = appendActivity(model.loadSnapshot.Events, message.Events)
	model.loadSnapshot.Notifications = appendNotifications(model.loadSnapshot.Notifications, message.Events)
	if len(message.Events) != 0 {
		return model, model.restartCanonicalLoad(message.Candidate)
	}
	model.loadTarget = message.Candidate
	model.loadHighWater = message.Candidate
	model.statusLine = "Validating canonical event fence"
	return model, fenceCanonicalCmd(model.loadCtx, model.eventSlot, model.client, model.loadGeneration, model.loadSnapshot.Workspace.ID, model.loadHighWater)
}

func (model Model) finishCanonicalLoad() (tea.Model, tea.Cmd) {
	if model.loadCancel != nil {
		model.loadCancel()
		model.loadCancel = nil
	}
	activity := model.snapshot.Events
	notifications := model.snapshot.Notifications
	if model.loadScopeChanged {
		activity = nil
		notifications = nil
	}
	model.snapshot = model.loadSnapshot
	model.snapshot.Events = appendActivity(activity, model.loadSnapshot.Events)
	model.snapshot.Notifications = mergeNotifications(notifications, model.loadSnapshot.Notifications)
	model.loadSnapshot = snapshot{}
	model.loadScopeChanged = false
	model.loadInFlight = false
	model.cursors.Applied = model.loadTarget
	model.cursors.Candidate = model.loadTarget
	model.cursors.HighWater = model.loadHighWater
	model.connection = ConnectionLive
	model.reconnectTry = 0
	model.lastError = ""
	model.statusLine = "Canonical state synchronized"
	for _, route := range routes {
		model.dirty[route] = false
		model.preserveSelection(route, model.recordsFor(route))
	}
	delay := eventPollInterval
	if model.cursors.Applied < model.cursors.HighWater {
		delay = 0
	}
	commands := []tea.Cmd{model.nextPoll(delay)}
	frame := model.currentFrame()
	if frame.Route == RouteCoordination && frame.EntityType == "agent_inbox" {
		commands = append(commands, model.nextInboxLoad(frame.EntityID))
	}
	if frame.EntityType == "briefing_claim" {
		commands = append(commands, model.nextBriefingExplanation(frame.EntityID))
	}
	if isTimelineEntity(frame.EntityType, frame.EntityID) {
		commands = append(commands, model.nextTimelineLoad(frame.EntityType, frame.EntityID))
	}
	return model, tea.Batch(commands...)
}

func (model Model) discardRewoundJournal() (tea.Model, tea.Cmd) {
	hadActionReview := model.modal.Kind == modalReview
	unknownOutcome := hadActionReview && (model.modal.Review.Executing || model.modal.Review.RequestFrozen)
	if hadActionReview && model.modal.Review.cancel != nil {
		model.modal.Review.cancel()
	}
	model.actionGeneration++
	model.modal = modalState{}
	model.cursors = cursorState{}
	model.snapshot = snapshot{}
	model.loadSnapshot = snapshot{}
	model.selection = make(map[Route]string, len(routes))
	model.rowOffset = make(map[Route]int, len(routes))
	model.inboxes = make(map[string][]domain.InboxItem)
	model.briefingExplanations = make(map[string]domain.BriefingClaimExplanation)
	model.entityTimelines = make(map[string]entityTimeline)
	model.timelineEpoch++
	model.activeTimelineEpoch = 0
	model.routeStack = []routeFrame{{Route: RouteOverview}}
	model.detailOffset = 0
	model.focus = FocusNavigation
	model.modalReturnFocus = 0
	if hadActionReview {
		diagnosis := "The daemon event journal was replaced. The pending action belonged to the previous history and was invalidated; it cannot be replayed."
		if unknownOutcome {
			diagnosis += " Its prior outcome is unknown; inspect the new canonical history before starting any action."
		}
		model.openModal(modalState{Kind: modalError, Message: diagnosis})
	}
	return model, model.restartCanonicalLoad(0)
}

func (model *Model) applySection(message sectionLoadedMsg) {
	switch message.Section {
	case sectionBriefing:
		model.loadSnapshot.Briefing = message.Briefing
	case sectionObjectives:
		model.loadSnapshot.Objectives, model.loadSnapshot.ObjectiveTotal, model.loadSnapshot.ObjectiveHasMore = message.Objectives, message.Total, message.HasMore
	case sectionTasks:
		model.loadSnapshot.Tasks, model.loadSnapshot.TaskTotal, model.loadSnapshot.TaskHasMore = message.Tasks, message.Total, message.HasMore
	case sectionRuns:
		model.loadSnapshot.Runs, model.loadSnapshot.RunTotal, model.loadSnapshot.RunHasMore = message.Runs, message.Total, message.HasMore
	case sectionAgents:
		model.loadSnapshot.Agents, model.loadSnapshot.AgentTotal, model.loadSnapshot.AgentHasMore = message.Agents, message.Total, message.HasMore
	case sectionApprovals:
		model.loadSnapshot.Approvals, model.loadSnapshot.ApprovalTotal, model.loadSnapshot.ApprovalHasMore = message.Approvals, message.Total, message.HasMore
	case sectionChecks:
		model.loadSnapshot.Checks, model.loadSnapshot.CheckTotal, model.loadSnapshot.CheckHasMore = message.Checks, message.Total, message.HasMore
	case sectionClaims:
		model.loadSnapshot.Claims, model.loadSnapshot.ClaimTotal, model.loadSnapshot.ClaimHasMore = message.Claims, message.Total, message.HasMore
	case sectionOverlaps:
		model.loadSnapshot.Overlaps, model.loadSnapshot.OverlapTotal, model.loadSnapshot.OverlapHasMore = message.Overlaps, message.Total, message.HasMore
	case sectionDrifts:
		model.loadSnapshot.Drifts, model.loadSnapshot.DriftTotal, model.loadSnapshot.DriftHasMore = message.Drifts, message.Total, message.HasMore
	case sectionMeetings:
		model.loadSnapshot.Meetings, model.loadSnapshot.MeetingTotal, model.loadSnapshot.MeetingHasMore = message.Meetings, message.Total, message.HasMore
	}
}

func (model *Model) launchSectionReads() tea.Cmd {
	commands := make([]tea.Cmd, 0, maxConcurrentReads)
	for model.loadActive < maxConcurrentReads && len(model.loadPending) > 0 {
		section := model.loadPending[0]
		model.loadPending = model.loadPending[1:]
		model.loadStates[section] = sectionLoading
		model.loadActive++
		commands = append(commands, loadSectionCmd(model.loadCtx, model.ioSlots, model.client, model.loadGeneration, section, model.loadSnapshot.Workspace, model.loadSnapshot.Project))
	}
	if len(commands) == 0 {
		return nil
	}
	return tea.Batch(commands...)
}

func (model Model) failCanonicalLoad(err error, fatal bool) (tea.Model, tea.Cmd) {
	if invalidatesCachedTruth(err) {
		return model.invalidateFatalCache(err)
	}
	if model.loadCancel != nil {
		model.loadCancel()
		model.loadCancel = nil
	}
	model.loadInFlight = false
	model.loadPending = nil
	model.loadActive = 0
	model.lastError = sanitizeLine(err.Error())
	if fatal {
		model.connection = ConnectionFatal
		return model, nil
	}
	model.connection = ConnectionReconnecting
	delay := reconnectDelay(model.reconnectTry)
	model.reconnectTry++
	return model, model.nextReconnect(delay)
}

func (model *Model) restartCanonicalLoad(requestedCursor int64) tea.Cmd {
	switch model.modal.Kind {
	case modalActions:
		model.closeModal()
		model.statusLine = "Action choices were invalidated because canonical state is refreshing"
	case modalReview:
		if !model.modal.Review.Executing && !model.modal.Review.RequestFrozen {
			model.closeModal()
			model.statusLine = "Unsubmitted action review was invalidated because canonical state is refreshing"
		}
	}
	if model.loadCancel != nil {
		model.loadCancel()
	}
	model.loadCtx, model.loadCancel = context.WithCancel(model.ctx)
	model.loadGeneration++
	model.briefingExplanations = make(map[string]domain.BriefingClaimExplanation)
	model.entityTimelines = make(map[string]entityTimeline)
	model.inboxes = make(map[string][]domain.InboxItem)
	model.inboxEpoch++
	model.activeInboxEpoch = 0
	model.briefingExplainEpoch++
	model.activeBriefingExplainEpoch = 0
	model.timelineEpoch++
	model.activeTimelineEpoch = 0
	model.pollEpoch++
	model.reconnectEpoch++
	model.loadInFlight = true
	model.loadPending = nil
	model.loadActive = 0
	model.loadStates = make(map[canonicalSection]sectionLoadState, len(canonicalSections))
	model.connection = ConnectionSyncing
	for _, route := range routes {
		model.dirty[route] = true
	}
	return loadScopeCmd(model.loadCtx, model.ioSlots, model.eventSlot, model.client, model.config, model.loadGeneration, requestedCursor)
}

func (model Model) reloadCursor() int64 {
	if model.cursors.Candidate > model.cursors.Applied {
		return model.cursors.Candidate
	}
	return model.cursors.Applied
}

func (model *Model) nextPoll(delay time.Duration) tea.Cmd {
	model.pollEpoch++
	return schedulePoll(model.pollEpoch, delay)
}

func (model *Model) nextReconnect(delay time.Duration) tea.Cmd {
	model.reconnectEpoch++
	return scheduleReconnect(model.reconnectEpoch, delay)
}

func (model Model) updateEventsPolled(message eventsPolledMsg) (tea.Model, tea.Cmd) {
	if !model.pollInFlight || message.PollEpoch != model.pollActiveEpoch {
		return model, nil
	}
	model.pollInFlight = false
	model.pollActiveEpoch = 0
	if message.Generation != model.loadGeneration || message.After != model.cursors.Applied || model.loadInFlight {
		if model.connection == ConnectionLive && !model.loadInFlight {
			return model, model.nextPoll(0)
		}
		return model, nil
	}
	if message.Err != nil {
		if isDefinitiveAPIError(message.Err) {
			return model.invalidateFatalCache(message.Err)
		}
		model.lastError = sanitizeLine(message.Err.Error())
		model.connection = ConnectionReconnecting
		delay := reconnectDelay(model.reconnectTry)
		model.reconnectTry++
		return model, model.nextReconnect(delay)
	}
	if message.Rewind || message.HighWater < model.cursors.Applied {
		return model.discardRewoundJournal()
	}
	model.cursors.Candidate = message.Candidate
	model.cursors.HighWater = message.HighWater
	model.snapshot.Events = appendActivity(model.snapshot.Events, message.Events)
	model.snapshot.Notifications = appendNotifications(model.snapshot.Notifications, message.Events)
	frame := model.currentFrame()
	if frame.Route == RouteCoordination && frame.EntityType == "agent_inbox" {
		delete(model.inboxes, frame.EntityID)
	}
	if len(message.Events) == 0 {
		model.cursors.Applied = message.Candidate
		model.cursors.Candidate = message.Candidate
		model.connection = ConnectionLive
		return model, model.nextPoll(eventPollInterval)
	}
	for _, route := range routes {
		model.dirty[route] = true
	}
	return model, model.restartCanonicalLoad(message.Candidate)
}

func invalidatesCachedTruth(err error) bool {
	if errors.Is(err, localapi.ErrProtocolMismatch) {
		return true
	}
	var apiError *localapi.APIError
	return errors.As(err, &apiError) && apiError.Code == "unsupported_operator_event"
}

func (model Model) invalidateFatalCache(err error) (tea.Model, tea.Cmd) {
	if model.loadCancel != nil {
		model.loadCancel()
		model.loadCancel = nil
	}
	hadReview := model.modal.Kind == modalReview
	unknownOutcome := hadReview && (model.modal.Review.Executing || model.modal.Review.RequestFrozen)
	if hadReview && model.modal.Review.cancel != nil {
		model.modal.Review.cancel()
	}
	model.actionGeneration++
	model.modal = modalState{}
	model.modalReturnFocus = 0
	model.snapshot = snapshot{}
	model.loadSnapshot = snapshot{}
	model.cursors = cursorState{}
	model.selection = make(map[Route]string, len(routes))
	model.rowOffset = make(map[Route]int, len(routes))
	model.inboxes = make(map[string][]domain.InboxItem)
	model.briefingExplanations = make(map[string]domain.BriefingClaimExplanation)
	model.entityTimelines = make(map[string]entityTimeline)
	model.timelineEpoch++
	model.activeTimelineEpoch = 0
	model.routeStack = []routeFrame{{Route: RouteOverview}}
	model.detailOffset = 0
	model.loadInFlight = false
	model.loadPending = nil
	model.loadActive = 0
	model.connection = ConnectionFatal
	model.lastError = sanitizeLine(err.Error())
	model.focus = FocusNavigation
	if hadReview {
		diagnosis := "The canonical event history became unsafe to interpret. The pending action was invalidated and cannot be replayed."
		if unknownOutcome {
			diagnosis += " Its prior outcome is unknown; inspect repaired canonical history before starting another action."
		}
		model.openModal(modalState{Kind: modalError, Message: diagnosis})
	}
	return model, nil
}

func (model Model) updateActionCompleted(message actionCompletedMsg) (tea.Model, tea.Cmd) {
	if model.modal.Kind == modalReview && model.modal.Review.cancel != nil {
		model.modal.Review.cancel()
		model.modal.Review.cancel = nil
	}
	if message.Err != nil {
		var apiError *localapi.APIError
		if errors.As(message.Err, &apiError) && isRevisionConflict(apiError.Code) {
			model.openModal(modalState{Kind: modalError, Message: "The target changed. Canonical state was refreshed; review the action again."})
			return model, model.restartCanonicalLoad(model.cursors.Applied)
		}
		if errors.As(message.Err, &apiError) && apiError.Retryable {
			model.modal.Review.Executing = false
			model.modal.Review.AmbiguousError = sanitizeLine(message.Err.Error())
			model.connection = ConnectionReconnecting
			model.lastError = model.modal.Review.AmbiguousError
			delay := reconnectDelay(model.reconnectTry)
			model.reconnectTry++
			return model, model.nextReconnect(delay)
		}
		if errors.As(message.Err, &apiError) {
			messageText := sanitizeLine(message.Err.Error())
			if apiError.Code == "idempotency_conflict" {
				messageText = "The retained idempotency key conflicts with a different canonical request. Nothing was replayed; inspect canonical state before starting a new action."
			}
			model.openModal(modalState{Kind: modalError, Message: messageText})
			return model, nil
		}
		if model.modal.Kind == modalReview {
			model.modal.Review.Executing = false
			model.modal.Review.AmbiguousError = sanitizeLine(message.Err.Error())
			model.connection = ConnectionReconnecting
			model.lastError = model.modal.Review.AmbiguousError
			delay := reconnectDelay(model.reconnectTry)
			model.reconnectTry++
			return model, model.nextReconnect(delay)
		}
		model.openModal(modalState{Kind: modalError, Message: sanitizeLine(message.Err.Error())})
		return model, nil
	}
	model.closeModal()
	model.statusLine = message.Kind.String() + " completed"
	// Keep Applied as the lower bound. A successful mutation may emit several
	// facts, and unrelated facts may commit before its terminal event. Starting
	// after EventSequence would skip their classification and activity records.
	return model, model.restartCanonicalLoad(model.cursors.Applied)
}

func isRevisionConflict(code string) bool {
	switch code {
	case "revision_conflict", "run_conflict", "approval_conflict":
		return true
	default:
		return false
	}
}

func (model Model) matchesActionResult(generation uint64, idempotencyKey string) bool {
	return model.modal.Kind == modalReview && model.modal.Review.Executing &&
		model.modal.Review.Generation == generation && model.modal.Review.IdempotencyKey == idempotencyKey
}

func (model Model) updateKey(message tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := message.Keystroke()
	if model.width > 0 && model.height > 0 && model.layout() == layoutTooSmall {
		if model.modal.Kind != modalNone {
			if key == "ctrl+c" {
				return model.updateModalKey(message, key)
			}
			return model, nil
		}
		if key == "ctrl+c" || key == "q" {
			return model, tea.Quit
		}
		return model, nil
	}
	if model.modal.Kind != modalNone {
		return model.updateModalKey(message, key)
	}
	switch key {
	case "ctrl+c", "q":
		return model, tea.Quit
	case "tab":
		model.cycleFocus(1)
	case "shift+tab":
		model.cycleFocus(-1)
	case "up", "k":
		model.moveFocused(-1)
	case "down", "j":
		model.moveFocused(1)
	case "pgup":
		model.moveRecordPage(-1)
	case "pgdown":
		model.moveRecordPage(1)
	case "g":
		model.moveRecordToEdge(false)
	case "shift+g", "G":
		model.moveRecordToEdge(true)
	case "enter":
		return model, model.inspectSelected()
	case "esc":
		return model, model.goBack()
	case "/":
		model.filter.SetValue(model.filterText)
		model.filter.Focus()
		model.openModal(modalState{Kind: modalFilter, Message: model.filterText})
	case "?":
		model.openModal(modalState{Kind: modalHelp})
	case "a":
		if !model.actionsReady() {
			model.statusLine = "Actions are disabled until the daemon is live and synchronized"
			break
		}
		choices := model.actionsForSelection()
		if len(choices) == 0 {
			model.statusLine = "No actions are available for this record"
			break
		}
		model.openModal(modalState{Kind: modalActions, Choices: choices})
	case "r":
		if !model.loadInFlight {
			return model, model.restartCanonicalLoad(model.reloadCursor())
		}
	}
	return model, nil
}

func (model Model) updateModalKey(message tea.KeyPressMsg, key string) (tea.Model, tea.Cmd) {
	if key == "ctrl+c" {
		if model.modal.Kind == modalReview && model.modal.Review.CancelRequested {
			return model, tea.Quit
		}
		if model.modal.Kind == modalReview && model.modal.Review.Executing {
			if model.modal.Review.cancel != nil {
				model.modal.Review.cancel()
			}
			model.modal.Review.CancelRequested = true
			model.modal.Review.AmbiguousError = "Cancellation requested after submission; the outcome is unknown until the same idempotency key is replayed or canonical state confirms it."
			return model, nil
		}
		if model.modal.Kind == modalReview && model.modal.Review.RequestFrozen && model.modal.Review.IdempotencyKey != "" {
			model.modal.Review.CancelRequested = true
			model.statusLine = "The submitted mutation review and idempotency key remain retained; press Ctrl+C again to quit"
			return model, nil
		}
		if model.modal.Kind == modalFilter {
			model.filterText = model.modal.Message
			model.filter.SetValue(model.filterText)
			model.closeModal()
			model.preserveSelection(model.currentRoute(), model.recordsFor(model.currentRoute()))
			return model, nil
		}
		model.closeModal()
		return model, nil
	}
	switch model.modal.Kind {
	case modalFilter:
		switch key {
		case "esc":
			model.filterText = model.modal.Message
			model.filter.SetValue(model.filterText)
			model.filter.Blur()
			model.closeModal()
			model.preserveSelection(model.currentRoute(), model.recordsFor(model.currentRoute()))
			return model, nil
		case "enter":
			model.filterText = sanitizeLine(model.filter.Value())
			model.filter.Blur()
			model.closeModal()
			model.preserveSelection(model.currentRoute(), model.recordsFor(model.currentRoute()))
			return model, nil
		default:
			updated, command := model.filter.Update(message)
			model.filter = updated
			model.filterText = sanitizeLine(updated.Value())
			model.preserveSelection(model.currentRoute(), model.recordsFor(model.currentRoute()))
			return model, command
		}
	case modalHelp, modalError:
		if key == "esc" || key == "enter" {
			model.closeModal()
		}
	case modalWorkspace:
		switch key {
		case "esc":
			model.closeModal()
			model.connection = ConnectionFatal
			model.lastError = "workspace selection was canceled; press r to reopen the chooser or q to quit"
			return model, nil
		case "up", "k":
			if model.modal.ChoiceIndex > 0 {
				model.modal.ChoiceIndex--
			}
		case "down", "j":
			if model.modal.ChoiceIndex+1 < len(model.modal.Workspaces) {
				model.modal.ChoiceIndex++
			}
		case "enter":
			if len(model.modal.Workspaces) == 0 || model.loadInFlight {
				return model, nil
			}
			selected := model.modal.Workspaces[model.modal.ChoiceIndex]
			model.config.Workspace = selected.ID
			model.closeModal()
			return model, model.restartCanonicalLoad(0)
		}
	case modalActions:
		switch key {
		case "esc":
			model.closeModal()
		case "up", "k":
			if model.modal.ChoiceIndex > 0 {
				model.modal.ChoiceIndex--
			}
		case "down", "j":
			if model.modal.ChoiceIndex+1 < len(model.modal.Choices) {
				model.modal.ChoiceIndex++
			}
		case "enter":
			if len(model.modal.Choices) != 0 {
				if !model.actionsReady() {
					model.statusLine = "Action selection is stale; wait for a live synchronized dashboard at a safe terminal size"
					return model, nil
				}
				choice := model.modal.Choices[model.modal.ChoiceIndex]
				model.actionGeneration++
				return model, prepareActionCmd(model.ctx, model.ioSlots, model.client, model.loadGeneration, model.snapshot.Workspace.ID, choice, model.actionGeneration)
			}
		}
	case modalReview:
		if key == "esc" && !model.modal.Review.Executing {
			if model.modal.Review.RequestFrozen && model.modal.Review.IdempotencyKey != "" {
				model.statusLine = "Cannot discard a mutation with an unknown outcome; replay the retained request or press Ctrl+C twice to quit"
				return model, nil
			}
			model.closeModal()
			return model, nil
		}
		if key == "ctrl+enter" && !model.modal.Review.Executing {
			if !model.actionsReady() {
				model.statusLine = "Action not submitted: the dashboard is not live, synchronized, and large enough"
				return model, nil
			}
			if !model.reviewFullyViewed() {
				model.statusLine = "Action not submitted: scroll through the complete exact review before confirming"
				return model, nil
			}
			if !model.reviewReady() {
				model.statusLine = "Action not submitted: exact canonical approval context is unavailable or changed"
				return model, nil
			}
			if !model.modal.Review.RequestFrozen && model.modal.Review.Choice.RequiresNote {
				note := canonicalDecisionNote(model.actionNote.Value())
				if len(note) == 0 || len([]byte(note)) > 1024 {
					model.statusLine = "Action not submitted: an owner decision note from 1 to 1024 bytes is required"
					return model, nil
				}
				model.modal.Review.DecisionNote = note
			}
			model.modal.Review.RequestFrozen = true
			model.actionNote.Blur()
			model.modal.Review.Executing = true
			model.modal.Review.CancelRequested = false
			model.modal.Review.AmbiguousError = ""
			actionContext, cancel := context.WithTimeout(model.ctx, actionTimeout)
			model.modal.Review.cancel = cancel
			return model, executeActionCmd(actionContext, model.ioSlots, model.client, model.snapshot.Workspace.ID, model.modal.Review)
		}
		switch key {
		case "up", "k":
			if model.modal.ReviewOffset > 0 {
				model.modal.ReviewOffset--
			}
			return model, nil
		case "down", "j":
			if model.modal.ReviewOffset < model.reviewMaxOffset() {
				model.modal.ReviewOffset++
			}
			return model, nil
		case "pgup":
			model.modal.ReviewOffset -= model.reviewViewport()
			if model.modal.ReviewOffset < 0 {
				model.modal.ReviewOffset = 0
			}
			return model, nil
		case "pgdown":
			model.modal.ReviewOffset += model.reviewViewport()
			if maximum := model.reviewMaxOffset(); model.modal.ReviewOffset > maximum {
				model.modal.ReviewOffset = maximum
			}
			return model, nil
		case "g":
			model.modal.ReviewOffset = 0
			return model, nil
		case "shift+g", "G":
			model.modal.ReviewOffset = model.reviewMaxOffset()
			return model, nil
		}
		if !model.modal.Review.Executing && !model.modal.Review.RequestFrozen && model.modal.Review.Choice.RequiresNote {
			updated, command := model.actionNote.Update(message)
			model.actionNote = updated
			model.modal.Review.DecisionNote = canonicalDecisionNote(updated.Value())
			return model, command
		}
	}
	return model, nil
}

func (model *Model) cycleFocus(direction int) {
	order := []Focus{FocusNavigation, FocusRecords, FocusDetail}
	index := 0
	for position, focus := range order {
		if focus == model.focus {
			index = position
			break
		}
	}
	index = (index + direction + len(order)) % len(order)
	model.focus = order[index]
}

func (model *Model) moveFocused(delta int) {
	if model.focus == FocusNavigation {
		current := model.currentRoute()
		index := 0
		for position, route := range routes {
			if route == current {
				index = position
				break
			}
		}
		index += delta
		if index < 0 {
			index = 0
		}
		if index >= len(routes) {
			index = len(routes) - 1
		}
		model.leaveFrame(model.currentFrame())
		model.routeStack[len(model.routeStack)-1] = routeFrame{Route: routes[index]}
		model.preserveSelection(routes[index], model.recordsFor(routes[index]))
		return
	}
	if model.focus == FocusRecords {
		model.moveRecord(delta)
		return
	}
	if model.focus == FocusDetail {
		model.moveDetail(delta)
	}
}

func (model *Model) moveRecord(delta int) {
	route := model.currentRoute()
	records := model.recordsFor(route)
	if len(records) == 0 {
		return
	}
	index := 0
	for position, item := range records {
		if item.ID == model.selection[route] {
			index = position
			break
		}
	}
	index += delta
	if index < 0 {
		index = 0
	}
	if index >= len(records) {
		index = len(records) - 1
	}
	model.selection[route] = records[index].ID
	model.detailOffset = 0
	visible := model.height - 3
	if visible < 1 {
		visible = 1
	}
	if index < model.rowOffset[route] {
		model.rowOffset[route] = index
	}
	if index >= model.rowOffset[route]+visible {
		model.rowOffset[route] = index - visible + 1
	}
}

func (model *Model) moveRecordPage(direction int) {
	if model.focus == FocusDetail {
		model.moveDetail(direction * model.detailViewport())
		return
	}
	visible := model.height - 3
	if visible < 1 {
		visible = 1
	}
	model.moveRecord(direction * visible)
}

func (model *Model) moveRecordToEdge(last bool) {
	if model.focus == FocusDetail {
		if last {
			model.detailOffset = model.detailMaxOffset()
		} else {
			model.detailOffset = 0
		}
		return
	}
	route := model.currentRoute()
	records := model.recordsFor(route)
	if len(records) == 0 {
		return
	}
	index := 0
	if last {
		index = len(records) - 1
	}
	model.selection[route] = records[index].ID
	model.rowOffset[route] = index
	model.detailOffset = 0
}

func (model *Model) inspectSelected() tea.Cmd {
	if model.focus == FocusNavigation {
		model.focus = FocusRecords
		model.preserveSelection(model.currentRoute(), model.recordsFor(model.currentRoute()))
		return nil
	}
	if model.focus != FocusRecords {
		return nil
	}
	item, ok := model.selectedRecord()
	if !ok {
		return nil
	}
	if item.Agent != nil {
		model.pushRoute(routeFrame{Route: RouteCoordination, EntityType: "agent_inbox", EntityID: item.Agent.ID})
		model.focus = FocusRecords
		model.statusLine = "Loading agent inbox"
		return model.nextInboxLoad(item.Agent.ID)
	}
	if item.Kind == recordBriefingClaim && item.Claim != nil {
		model.pushRoute(routeFrame{Route: model.currentRoute(), EntityType: "briefing_claim", EntityID: item.Claim.ID})
		model.focus = FocusDetail
		model.statusLine = "Loading exact briefing claim explanation"
		return model.nextBriefingExplanation(item.Claim.ID)
	}
	if item.DrillRoute != 0 {
		frame := routeFrame{Route: item.DrillRoute, EntityType: "aggregate", EntityID: item.ID, Diagnosis: item.Diagnosis}
		if item.Targets != nil {
			frame.TargetIDs = make([]string, len(item.Targets))
			copy(frame.TargetIDs, item.Targets)
			// An exact aggregate drill must not inherit a text filter that was
			// applied to the producer summary rather than its target rows.
			model.filterText = ""
			model.filter.SetValue("")
		}
		model.pushRoute(frame)
		model.focus = FocusRecords
		model.preserveSelection(item.DrillRoute, model.recordsFor(item.DrillRoute))
		return nil
	}
	entityType, entityID, hasTimeline := timelineTarget(item)
	if !hasTimeline {
		entityType, entityID = recordEntityType(item.Kind), item.ID
	}
	model.pushRoute(routeFrame{Route: model.currentRoute(), EntityType: entityType, EntityID: entityID})
	model.focus = FocusDetail
	if hasTimeline {
		model.statusLine = "Loading canonical entity timeline"
		return model.nextTimelineLoad(entityType, entityID)
	}
	return nil
}

func (model *Model) goBack() tea.Cmd {
	frame := model.currentFrame()
	model.leaveFrame(frame)
	if model.popRoute() {
		model.focus = FocusRecords
		return model.loadRevealedFrame()
	}
	switch model.focus {
	case FocusDetail:
		model.focus = FocusRecords
	case FocusRecords:
		model.focus = FocusNavigation
	}
	return nil
}

func (model *Model) loadRevealedFrame() tea.Cmd {
	frame := model.currentFrame()
	if frame.Route == RouteCoordination && frame.EntityType == "agent_inbox" {
		if _, found := model.inboxes[frame.EntityID]; found {
			model.statusLine = "Agent inbox synchronized"
			return nil
		}
		model.statusLine = "Loading exact agent inbox"
		return model.nextInboxLoad(frame.EntityID)
	}
	if frame.EntityType == "briefing_claim" {
		if _, found := model.briefingExplanations[briefingExplanationKey(model.snapshot.Briefing.ID, frame.EntityID)]; found {
			model.statusLine = "Briefing claim explanation synchronized"
			return nil
		}
		model.statusLine = "Loading exact briefing claim explanation"
		return model.nextBriefingExplanation(frame.EntityID)
	}
	if isTimelineEntity(frame.EntityType, frame.EntityID) {
		if _, found := model.entityTimelines[entityTimelineKey(model.snapshot.Workspace.ID, frame.EntityType, frame.EntityID)]; found {
			model.statusLine = "Entity timeline synchronized"
			return nil
		}
		model.statusLine = "Loading canonical entity timeline"
		return model.nextTimelineLoad(frame.EntityType, frame.EntityID)
	}
	model.statusLine = "Canonical state synchronized"
	return nil
}

func (model *Model) nextInboxLoad(agentID string) tea.Cmd {
	model.inboxEpoch++
	model.activeInboxEpoch = model.inboxEpoch
	return loadInboxCmd(model.ctx, model.ioSlots, model.client, model.loadGeneration, model.activeInboxEpoch,
		model.snapshot.Workspace.ID, agentID)
}

func (model *Model) nextBriefingExplanation(claimID string) tea.Cmd {
	model.briefingExplainEpoch++
	model.activeBriefingExplainEpoch = model.briefingExplainEpoch
	return loadBriefingExplainCmd(model.ctx, model.ioSlots, model.client, model.loadGeneration, model.activeBriefingExplainEpoch,
		model.snapshot.Workspace.ID, model.snapshot.Briefing.ID, claimID)
}

func (model *Model) nextTimelineLoad(entityType, entityID string) tea.Cmd {
	model.timelineEpoch++
	model.activeTimelineEpoch = model.timelineEpoch
	return loadTimelineCmd(model.ctx, model.ioSlots, model.client, model.loadGeneration, model.activeTimelineEpoch,
		model.snapshot.Workspace.ID, entityType, entityID)
}

func (model *Model) moveDetail(delta int) {
	model.detailOffset += delta
	if model.detailOffset < 0 {
		model.detailOffset = 0
	}
	if maximum := model.detailMaxOffset(); model.detailOffset > maximum {
		model.detailOffset = maximum
	}
}

func recordEntityType(kind recordKind) string {
	switch kind {
	case recordBriefing:
		return "briefing"
	case recordBriefingClaim:
		return "briefing_claim"
	case recordObjective:
		return "objective"
	case recordTask:
		return "task"
	case recordRun:
		return "run"
	case recordAgent:
		return "agent"
	case recordInbox:
		return "message"
	case recordApproval:
		return "approval_request"
	case recordCheck:
		return "check_run"
	case recordClaim:
		return "claim"
	case recordOverlap:
		return "overlap"
	case recordDrift:
		return "claim_drift"
	case recordMeeting:
		return "meeting"
	case recordNotification:
		return "notification"
	case recordEvent:
		return "event"
	default:
		return "summary"
	}
}

func timelineTarget(item record) (string, string, bool) {
	if item.Notification != nil {
		return item.Notification.EntityType, item.Notification.EntityID,
			isTimelineEntity(item.Notification.EntityType, item.Notification.EntityID)
	}
	if item.Event != nil {
		return item.Event.Entity.Type, item.Event.Entity.ID,
			isTimelineEntity(item.Event.Entity.Type, item.Event.Entity.ID)
	}
	switch item.Kind {
	case recordObjective, recordTask, recordRun, recordInbox, recordApproval, recordCheck,
		recordClaim, recordOverlap, recordDrift, recordMeeting:
		entityType := recordEntityType(item.Kind)
		return entityType, item.ID, isTimelineEntity(entityType, item.ID)
	default:
		return "", "", false
	}
}

func isTimelineEntity(entityType, entityID string) bool {
	return entityType != "" && entityID != "" && entityType != "summary" && entityType != "aggregate" &&
		entityType != "briefing" && entityType != "briefing_claim" && entityType != "agent_inbox"
}

func entityTimelineKey(workspaceID, entityType, entityID string) string {
	return workspaceID + "\x00" + entityType + "\x00" + entityID
}

func briefingExplanationKey(briefingID, claimID string) string {
	return briefingID + "\x00" + claimID
}

func (model Model) briefingClaim(claimID string) (domain.BriefingClaim, bool) {
	for _, claim := range model.snapshot.Briefing.Claims {
		if claim.ID == claimID {
			return claim, true
		}
	}
	return domain.BriefingClaim{}, false
}

func cloneBriefingExplanation(explanation domain.BriefingClaimExplanation) domain.BriefingClaimExplanation {
	result := explanation
	result.Claim.Sources = append([]domain.BriefingClaimSource(nil), explanation.Claim.Sources...)
	result.Provenance = append([]domain.BriefingClaimSource(nil), explanation.Provenance...)
	result.Diagnoses = append([]string(nil), explanation.Diagnoses...)
	return result
}

func isApprovalAction(kind actionKind) bool {
	return kind == actionAllowApproval || kind == actionDenyApproval
}

func (model Model) approvalForChoice(choice actionChoice) (domain.ApprovalRequest, bool) {
	for _, approval := range model.snapshot.Approvals {
		if approval.ID == choice.TargetID && approval.Revision == choice.Revision && approval.Status == domain.ApprovalPending &&
			approval.WorkspaceID == model.snapshot.Workspace.ID && approval.ActionID == choice.ApprovalActionID &&
			approval.ExpectedActionRevision == choice.ExpectedActionRevision {
			return approval, true
		}
	}
	return domain.ApprovalRequest{}, false
}

func (model Model) validateApprovalAction(approval domain.ApprovalRequest, action domain.SupervisorAction) error {
	if approval.ID == "" || approval.WorkspaceID != model.snapshot.Workspace.ID || approval.Status != domain.ApprovalPending {
		return errors.New("the approval review was rejected because the canonical approval is not pending in the selected workspace")
	}
	if action.ID != approval.ActionID || action.Revision != approval.ExpectedActionRevision {
		return errors.New("the approval review was rejected because the supervisor action ID or revision does not match the canonical approval")
	}
	if action.Status != domain.SupervisorActionAwaitingApproval || action.ApprovalID != approval.ID {
		return errors.New("the approval review was rejected because the supervisor action is not awaiting this exact approval")
	}
	if action.WorkspaceID != approval.WorkspaceID {
		return errors.New("the approval review was rejected because the supervisor action is outside the selected workspace")
	}
	if model.snapshot.Project != nil && action.ProjectID != model.snapshot.Project.ID {
		return errors.New("the approval review was rejected because the supervisor action is outside the selected project")
	}
	if !knownSupervisorCondition(action.Condition) {
		return errors.New("the approval review was rejected because the supervisor condition is not a current typed condition")
	}
	if !knownSupervisorResponse(action.Response) {
		return errors.New("the approval review was rejected because the supervisor response is not a current typed response")
	}
	if !validCanonicalID(action.ID, "saction_") || !validCanonicalID(action.WorkspaceID, "ws_") ||
		!validCanonicalID(action.ProjectID, "prj_") || !validCanonicalID(action.TaskID, "task_") ||
		!validOptionalCanonicalID(action.ObjectiveID, "obj_") || !validOptionalCanonicalID(action.RunID, "run_") ||
		!validOptionalCanonicalID(action.PriorRunID, "run_") || !validOptionalCanonicalID(action.AgentID, "agent_") ||
		!validOptionalCanonicalID(action.IntentID, "sintent_") || !validOptionalCanonicalID(action.SourceProposalID, "mprop_") ||
		!validOptionalCanonicalID(action.SourceActionID, "mpact_") || !validCanonicalID(action.ApprovalID, "appr_") {
		return errors.New("the approval review was rejected because required canonical action or scope identifiers are missing or malformed")
	}
	if !validLowerHex(action.ConditionKey, 64) || !validLowerHex(action.ContentSHA256, 64) {
		return errors.New("the approval review was rejected because condition or content provenance hashes are missing or malformed")
	}
	if action.EntityRevision < 1 || action.PolicyRevision < 1 || action.AsOfEventSequence < 0 || action.Revision < 1 {
		return errors.New("the approval review was rejected because action, target, policy, or event revisions are invalid")
	}
	if len(action.Reasons) < 1 || len(action.Reasons) > 32 {
		return errors.New("the approval review was rejected because one to thirty-two exact reasons are required")
	}
	for _, reason := range action.Reasons {
		if !utf8.ValidString(reason) || strings.TrimSpace(reason) == "" || strings.ContainsRune(reason, '\x00') {
			return errors.New("the approval review was rejected because an exact reason is empty or malformed")
		}
	}
	if action.ConstraintSnapshot == nil {
		return errors.New("the approval review was rejected because the immutable constraint snapshot is missing")
	}
	if !validRFC3339(action.CreatedAt) || !validRFC3339(action.UpdatedAt) || action.CreatedBy == "" || action.UpdatedBy == "" || action.AppliedAt != "" {
		return errors.New("the approval review was rejected because immutable action time or actor provenance is missing or malformed")
	}
	switch action.Response {
	case domain.SupervisorResponseResumeRun, domain.SupervisorResponseStopRun:
		if action.RunID == "" {
			return errors.New("the approval review was rejected because the run response lacks its exact run target")
		}
	case domain.SupervisorResponseRetryTask, domain.SupervisorResponseReassignTask:
		if action.Condition != domain.SupervisorConditionManagerEscalation {
			return errors.New("the approval review was rejected because retry or reassignment is not bound to an exact manager escalation")
		}
	case domain.SupervisorResponseRequestOwner:
		// The task scope checked above is the exact owner-attention target.
	case domain.SupervisorResponseSchedule:
		return errors.New("the approval review was rejected because schedule is not an owner-approval executable response")
	}
	return nil
}

func validCanonicalID(value, prefix string) bool {
	if len(value) != len(prefix)+32 || !strings.HasPrefix(value, prefix) {
		return false
	}
	return validLowerHex(value[len(prefix):], 32)
}

func validOptionalCanonicalID(value, prefix string) bool {
	return value == "" || validCanonicalID(value, prefix)
}

func validLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validRFC3339(value string) bool {
	if value == "" {
		return false
	}
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func knownSupervisorCondition(condition string) bool {
	switch condition {
	case domain.SupervisorConditionDependencyReady, domain.SupervisorConditionBlocked, domain.SupervisorConditionStale,
		domain.SupervisorConditionFailed, domain.SupervisorConditionRepeatedFailure, domain.SupervisorConditionOverBudget,
		domain.SupervisorConditionManagerEscalation:
		return true
	default:
		return false
	}
}

func knownSupervisorResponse(response string) bool {
	switch response {
	case domain.SupervisorResponseSchedule, domain.SupervisorResponseResumeRun, domain.SupervisorResponseStopRun,
		domain.SupervisorResponseRetryTask, domain.SupervisorResponseReassignTask, domain.SupervisorResponseRequestOwner:
		return true
	default:
		return false
	}
}

func cloneSupervisorAction(action domain.SupervisorAction) domain.SupervisorAction {
	result := action
	result.Reasons = append([]string(nil), action.Reasons...)
	if action.ConstraintSnapshot != nil {
		result.ConstraintSnapshot = make(map[string]any, len(action.ConstraintSnapshot))
		for key, value := range action.ConstraintSnapshot {
			result.ConstraintSnapshot[key] = cloneJSONValue(value)
		}
	}
	return result
}

func cloneJSONValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		result := make(map[string]any, len(value))
		for key, nested := range value {
			result[key] = cloneJSONValue(nested)
		}
		return result
	case []any:
		result := make([]any, len(value))
		for index, nested := range value {
			result[index] = cloneJSONValue(nested)
		}
		return result
	default:
		return value
	}
}

func approvalConsequence(kind actionKind, action domain.SupervisorAction) string {
	if kind == actionAllowApproval {
		return fmt.Sprintf("Apply supervisor response %q for condition %q at the exact displayed action scope.", action.Response, action.Condition)
	}
	return fmt.Sprintf("Dismiss supervisor response %q for condition %q without applying it.", action.Response, action.Condition)
}

func canonicalDecisionNote(value string) string {
	return strings.TrimSpace(sanitizeLine(value))
}

func (model Model) reviewReady() bool {
	review := model.modal.Review
	if review.Choice.Kind == actionStopRun && (review.Choice.GracePeriodMillis < 1 || review.Choice.GracePeriodMillis > 30000) {
		return false
	}
	if !isApprovalAction(review.Choice.Kind) {
		return true
	}
	if !review.HasApprovalContext {
		return false
	}
	if review.RequestFrozen {
		if review.IdempotencyKey == "" || review.Choice.TargetID != review.Approval.ID ||
			review.Choice.Revision != review.Approval.Revision || review.Choice.ApprovalActionID != review.Approval.ActionID ||
			review.Choice.ExpectedActionRevision != review.Approval.ExpectedActionRevision {
			return false
		}
		if review.Choice.RequiresNote && (review.DecisionNote == "" || len([]byte(review.DecisionNote)) > 1024) {
			return false
		}
		// Once submission has an ambiguous outcome, replay must use the exact
		// frozen request and key. A successful first commit legitimately removes
		// the pending approval from the refreshed projection, so mutable lookup
		// cannot be a replay prerequisite. Scope/rewind/fatal paths invalidate the
		// review separately; this validation still binds its immutable action and
		// approval to the currently selected canonical scope.
		return model.validateApprovalAction(review.Approval, review.SupervisorAction) == nil
	}
	approval, ok := model.approvalForChoice(review.Choice)
	if !ok || !reflect.DeepEqual(approval, review.Approval) {
		return false
	}
	return model.validateApprovalAction(review.Approval, review.SupervisorAction) == nil
}

func (model Model) actionsForSelection() []actionChoice {
	item, ok := model.selectedRecord()
	if !ok {
		return nil
	}
	choices := []actionChoice{}
	if item.Run != nil {
		run := *item.Run
		if run.CanAttach {
			choices = append(choices, actionChoice{Kind: actionAttachRun, TargetType: "run", TargetID: run.ID, Consequence: "Suspend the dashboard and attach to the runtime terminal."})
		}
		if run.Status == domain.RunBlocked {
			choices = append(choices, actionChoice{Kind: actionResumeRun, TargetType: "run", TargetID: run.ID, Revision: run.Revision, Consequence: "Resume the blocked run from its durable cursor."})
		}
		if run.Status == domain.RunActive || run.Status == domain.RunBlocked {
			choices = append(choices, actionChoice{Kind: actionStopRun, TargetType: "run", TargetID: run.ID, Revision: run.Revision, Consequence: "Request a bounded graceful stop of this run.", GracePeriodMillis: stopGraceMillis})
		}
	}
	if item.Approval != nil && item.Approval.Status == domain.ApprovalPending {
		choices = append(choices,
			actionChoice{Kind: actionAllowApproval, TargetType: "approval", TargetID: item.Approval.ID, Revision: item.Approval.Revision, Consequence: "Resolve and review this pending supervisor action before allowing it.", RequiresNote: true, ApprovalActionID: item.Approval.ActionID, ExpectedActionRevision: item.Approval.ExpectedActionRevision},
			actionChoice{Kind: actionDenyApproval, TargetType: "approval", TargetID: item.Approval.ID, Revision: item.Approval.Revision, Consequence: "Resolve and review this pending supervisor action before denying it.", RequiresNote: true, ApprovalActionID: item.Approval.ActionID, ExpectedActionRevision: item.Approval.ExpectedActionRevision},
		)
	}
	sort.SliceStable(choices, func(left, right int) bool { return choices[left].Kind < choices[right].Kind })
	return choices
}

func attachmentCommand(ctx context.Context, attachment localapi.RunAttachResult) (*exec.Cmd, error) {
	if strings.TrimSpace(attachment.Executable) == "" {
		return nil, errors.New("runtime returned an empty attach executable")
	}
	if strings.ContainsRune(attachment.Executable, '\x00') {
		return nil, errors.New("runtime returned an invalid attach executable")
	}
	for _, argument := range attachment.Arguments {
		if strings.ContainsRune(argument, '\x00') {
			return nil, errors.New("runtime returned an invalid attach argument")
		}
	}
	for name, value := range attachment.Environment {
		if name == "" || strings.ContainsAny(name, "=\x00") || strings.ContainsRune(value, '\x00') {
			return nil, errors.New("runtime returned an invalid attach environment")
		}
	}
	command := exec.CommandContext(ctx, attachment.Executable, attachment.Arguments...)
	command.Env = mergeEnvironment(os.Environ(), attachment.Environment)
	return command, nil
}

func validateAttachResult(attachment localapi.RunAttachResult, reviewedRunID string) error {
	if err := localapi.ValidateRunAttachResult(attachment, reviewedRunID); err != nil {
		return fmt.Errorf("runtime attach response failed the current exact contract: %w", err)
	}
	return nil
}

func mergeEnvironment(base []string, overrides map[string]string) []string {
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	sort.Strings(names)
	result := append([]string(nil), base...)
	for _, name := range names {
		prefix := name + "="
		filtered := result[:0]
		for _, entry := range result {
			if !strings.HasPrefix(entry, prefix) {
				filtered = append(filtered, entry)
			}
		}
		result = append(filtered, prefix+overrides[name])
	}
	return result
}
