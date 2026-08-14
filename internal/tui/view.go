package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type palette struct {
	header lipgloss.Style
	footer lipgloss.Style
}

func makePalette(enabled bool) palette {
	if !enabled {
		plain := lipgloss.NewStyle()
		return palette{header: plain, footer: plain}
	}
	return palette{
		header: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("230")).Background(lipgloss.Color("24")),
		footer: lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Background(lipgloss.Color("236")),
	}
}

// View renders only current model state and never performs I/O.
func (model Model) View() tea.View {
	view := tea.NewView(model.render())
	view.AltScreen = true
	view.MouseMode = tea.MouseModeNone
	return view
}

func (model Model) render() string {
	if model.width <= 0 || model.height <= 0 {
		return "Crewfold operator UI\nWaiting for terminal dimensions…"
	}
	styles := makePalette(model.colorsEnabled())
	header := styles.header.Render(fitLine(model.headerText(), model.width))
	footer := styles.footer.Render(fitLine(model.footerText(), model.width))
	bodyHeight := model.height - 2
	if bodyHeight < 0 {
		bodyHeight = 0
	}

	var body []string
	if model.layout() == layoutTooSmall {
		body = model.renderTooSmall(model.width, bodyHeight, styles)
	} else if model.modal.Kind != modalNone {
		body = model.renderModal(model.width, bodyHeight, styles)
	} else if model.snapshot.Workspace.ID == "" && model.connection != ConnectionConnecting && model.connection != ConnectionSyncing {
		body = model.renderSetup(model.width, bodyHeight, styles)
	} else {
		switch model.layout() {
		case layoutThreePane:
			body = model.renderThreePane(model.width, bodyHeight, styles)
		case layoutTwoPane:
			body = model.renderTwoPane(model.width, bodyHeight, styles)
		default:
			body = model.renderOnePane(model.width, bodyHeight, styles)
		}
	}
	body = normalizeLines(body, model.width, bodyHeight)
	lines := make([]string, 0, model.height)
	lines = append(lines, header)
	lines = append(lines, body...)
	if model.height > 1 {
		lines = append(lines, footer)
	}
	return strings.Join(lines, "\n")
}

func (model Model) headerText() string {
	scope := "choose workspace"
	if model.snapshot.Workspace.ID != "" {
		scope = model.snapshot.Workspace.Name
		if scope == "" {
			scope = model.snapshot.Workspace.ID
		}
	}
	if model.snapshot.Project != nil {
		project := model.snapshot.Project.Name
		if project == "" {
			project = model.snapshot.Project.ID
		}
		scope += " / " + project
	}
	state := model.connection.String()
	if model.connection != ConnectionLive && model.snapshot.Workspace.ID != "" {
		state += " (cached state is stale)"
	}
	focus := model.focus.String()
	return fmt.Sprintf(" Crewfold | %s | %s | focus %s | cursor %d/%d/%d ", scope, state, focus, model.cursors.Applied, model.cursors.Candidate, model.cursors.HighWater)
}

func (model Model) footerText() string {
	status := ""
	statusSuffix := ""
	if model.statusLine != "" {
		status = " " + sanitizeLine(model.statusLine) + " |"
		statusSuffix = " | " + sanitizeLine(model.statusLine)
	}
	if model.width > 0 && model.height > 0 && model.layout() == layoutTooSmall {
		if model.modal.Kind != modalNone {
			return " Resize | Ctrl+C cancel; submitted: second Ctrl+C quits" + statusSuffix + " "
		}
		return " Resize terminal | q quit" + statusSuffix + " "
	}
	if model.modal.Kind == modalFilter {
		return " Esc cancel | Enter apply | type to filter" + statusSuffix + " "
	}
	if model.modal.Kind == modalReview {
		if model.modal.Review.Executing {
			return " Ctrl+C cancel request | second Ctrl+C quits" + statusSuffix + " "
		}
		if !model.reviewFullyViewed() {
			return " Scroll PgUp/PgDn | confirmation locked until final line" + statusSuffix + " "
		}
		if model.modal.Review.RequestFrozen && model.modal.Review.IdempotencyKey != "" {
			return " Ctrl+Enter replay retained request | Ctrl+C then quit" + statusSuffix + " "
		}
		return " Esc cancel | Ctrl+Enter confirm" + statusSuffix + " "
	}
	if model.modal.Kind != modalNone {
		return " arrows/j/k move | Enter choose | Esc close" + statusSuffix + " "
	}
	if model.focus == FocusDetail {
		return status + " arrows/j/k scroll detail | PgUp/PgDn | g/G edges | Esc back | Tab focus | q quit "
	}
	return status + " Tab focus | arrows/j/k move | Enter inspect | / filter | a actions | r refresh | ? help | q quit "
}

func (model Model) renderThreePane(width, height int, styles palette) []string {
	navigationWidth := 22
	recordsWidth := (width - navigationWidth) * 45 / 100
	detailWidth := width - navigationWidth - recordsWidth
	navigation := model.renderNavigation(navigationWidth, height, styles)
	records := model.renderRecords(recordsWidth, height, styles)
	detail := model.renderDetail(detailWidth, height, styles)
	return joinColumns(height, navigation, records, detail)
}

func (model Model) renderTwoPane(width, height int, styles palette) []string {
	leftWidth := width * 42 / 100
	rightWidth := width - leftWidth
	var left []string
	if model.focus == FocusNavigation {
		left = model.renderNavigation(leftWidth, height, styles)
	} else {
		left = model.renderRecords(leftWidth, height, styles)
	}
	return joinColumns(height, left, model.renderDetail(rightWidth, height, styles))
}

func (model Model) renderOnePane(width, height int, styles palette) []string {
	switch model.focus {
	case FocusNavigation:
		return model.renderNavigation(width, height, styles)
	case FocusDetail:
		return model.renderDetail(width, height, styles)
	default:
		return model.renderRecords(width, height, styles)
	}
}

func (model Model) renderNavigation(width, height int, styles palette) []string {
	title := "Screens"
	if model.focus == FocusNavigation {
		title += " [focus]"
	}
	lines := []string{paneTitle(title, width)}
	current := model.currentRoute()
	for _, route := range routes {
		marker := "  "
		if route == current {
			marker = "> "
		}
		line := fitLine(marker+route.String(), width)
		lines = append(lines, line)
	}
	return normalizeLines(lines, width, height)
}

func (model Model) renderRecords(width, height int, styles palette) []string {
	route := model.currentRoute()
	title := route.String()
	if model.filterText != "" {
		title += " / " + model.filterText
	}
	if model.focus == FocusRecords {
		title += " [focus]"
	}
	lines := []string{paneTitle(title, width)}
	records := model.recordsFor(route)
	if len(records) == 0 {
		lines = append(lines, fitLine("No records in this scope.", width))
		return normalizeLines(lines, width, height)
	}
	visible := height - 1
	if visible < 1 {
		visible = 1
	}
	start := model.rowOffset[route]
	if start < 0 {
		start = 0
	}
	if start >= len(records) {
		start = len(records) - 1
	}
	end := start + visible
	if end > len(records) {
		end = len(records)
	}
	selected := model.selection[route]
	for _, item := range records[start:end] {
		marker := "  "
		if item.ID == selected {
			marker = "> "
		}
		line := fitLine(marker+item.Primary+" | "+item.Secondary, width)
		lines = append(lines, line)
	}
	return normalizeLines(lines, width, height)
}

func (model Model) renderDetail(width, height int, styles palette) []string {
	title := "Details"
	if model.focus == FocusDetail {
		title += " [focus]"
	}
	lines := []string{paneTitle(title, width)}
	item, ok := model.selectedRecord()
	if !ok {
		lines = append(lines, fitLine("Select a record to inspect it.", width))
		return normalizeLines(lines, width, height)
	}
	details := model.detailContentLines(item)
	start := model.detailOffset
	maximum := model.detailMaxOffset()
	if start > maximum {
		start = maximum
	}
	if start < 0 {
		start = 0
	}
	end := start + model.detailViewport()
	if end > len(details) {
		end = len(details)
	}
	for _, line := range details[start:end] {
		lines = append(lines, fitLine(line, width))
	}
	return normalizeLines(lines, width, height)
}

func (model Model) detailViewport() int {
	viewport := model.height - 3
	if viewport < 1 {
		viewport = 1
	}
	return viewport
}

func (model Model) detailMaxOffset() int {
	item, ok := model.selectedRecord()
	if !ok {
		return 0
	}
	maximum := len(model.detailContentLines(item)) - model.detailViewport()
	if maximum < 0 {
		return 0
	}
	return maximum
}

func (model Model) detailContentLines(item record) []string {
	lines := detailLines(item)
	frame := model.currentFrame()
	if !isTimelineEntity(frame.EntityType, frame.EntityID) {
		return lines
	}
	lines = append(lines, "Entity timeline:")
	timeline, found := model.entityTimelines[entityTimelineKey(model.snapshot.Workspace.ID, frame.EntityType, frame.EntityID)]
	if !found {
		return append(lines, "Loading exact canonical history…")
	}
	lines = append(lines, fmt.Sprintf("%d of %d events cached at high-water %d", len(timeline.Events), timeline.Total, timeline.HighWater))
	if timeline.HasMore {
		lines = append(lines, fmt.Sprintf("Older history omitted after %d bounded pages (%d events not cached)", maxCollectionPages, timeline.Total-len(timeline.Events)))
	}
	if len(timeline.Events) == 0 {
		return append(lines, "No canonical events exist for this entity.")
	}
	for _, event := range timeline.Events {
		lines = append(lines, sanitizeLine(fmt.Sprintf("#%d %s · r%d · %s · %s", event.Sequence, event.Type,
			event.Entity.Revision, event.OccurredAt, event.Actor.ActorType)))
	}
	return lines
}

func detailLines(item record) []string {
	lines := []string{"ID: " + item.ID}
	if item.Revision > 0 {
		lines = append(lines, fmt.Sprintf("Revision: %d", item.Revision))
	}
	switch item.Kind {
	case recordBriefing:
		briefing := item.Briefing
		lines = append(lines,
			fmt.Sprintf("Event cursor: %d", briefing.EventCursor),
			fmt.Sprintf("Cutoff event sequence: %d", briefing.CutoffEventSequence),
			fmt.Sprintf("Since event sequence: %d", briefing.SinceEventSequence),
			"Checkpoint: "+briefing.CheckpointID,
			"Evaluated: "+briefing.EvaluatedAt,
			fmt.Sprintf("Caught up: %t", briefing.CaughtUp),
			"Content SHA-256: "+briefing.ContentSHA256,
			fmt.Sprintf("Canonical byte size: %d", briefing.ByteSize),
			fmt.Sprintf("Claims: %d", len(briefing.Claims)),
			fmt.Sprintf("Omissions: %d", len(briefing.Omitted)),
		)
		if briefing.UnknownEventType != "" {
			lines = append(lines, fmt.Sprintf("Unknown event: %s at %d", briefing.UnknownEventType, briefing.UnknownEventSequence))
		}
	case recordBriefingClaim:
		claim := item.Claim
		lines = append(lines,
			"Kind: "+claim.Kind,
			"Urgency: "+claim.Urgency,
			"Status: "+claim.Status,
			"Summary: "+claim.Summary,
			fmt.Sprintf("Source cursor: %d", claim.SourceEventSequence),
			"Provenance:",
		)
		for _, source := range claim.Sources {
			lines = append(lines,
				fmt.Sprintf("- %s/%s r%d event %d", source.EntityType, source.EntityID, source.Revision, source.EventSequence),
			)
			if source.EvidenceClass != "" || source.EvidenceEffect != "" || source.CurrentFreshness != "" {
				lines = append(lines, fmt.Sprintf("  class=%s effect=%s pinned=%s current=%s", source.EvidenceClass, source.EvidenceEffect, source.PinnedFreshness, source.CurrentFreshness))
			}
		}
		if item.Explanation == nil {
			lines = append(lines, "Explanation diagnoses: loading on first inspection")
			break
		}
		explanation := item.Explanation
		lines = append(lines,
			"Explanation evaluated: "+explanation.EvaluatedAt,
			"Explanation provenance:",
		)
		for _, source := range explanation.Provenance {
			lines = append(lines,
				fmt.Sprintf("- %s/%s r%d event %d", source.EntityType, source.EntityID, source.Revision, source.EventSequence),
				"  content-sha256="+source.ContentSHA256,
				fmt.Sprintf("  class=%s effect=%s pinned=%s current=%s", source.EvidenceClass, source.EvidenceEffect, source.PinnedFreshness, source.CurrentFreshness),
			)
		}
		lines = append(lines, "Explanation diagnoses:")
		if len(explanation.Diagnoses) == 0 {
			lines = append(lines, "- none")
		}
		for _, diagnosis := range explanation.Diagnoses {
			lines = append(lines, "- "+diagnosis)
		}
	case recordObjective:
		objective := item.Objective
		lines = append(lines,
			"Title: "+objective.Title,
			"Status: "+objective.Status,
			"Project: "+objective.ProjectID,
			fmt.Sprintf("Budget: %d tokens · %d cents · %d seconds", objective.Budget.TokenLimit, objective.Budget.CostCents, objective.Budget.TimeSeconds),
			"Created: "+objective.CreatedAt+" by "+objective.CreatedBy,
			"Updated: "+objective.UpdatedAt+" by "+objective.UpdatedBy,
		)
	case recordTask:
		task := item.Task.Task
		lines = append(lines,
			"Title: "+task.Title,
			"Status: "+task.Status,
			fmt.Sprintf("Priority: %d", task.Priority),
			"Readiness: "+item.Task.Readiness.Reason,
		)
		if task.BlockedReason != "" {
			lines = append(lines, "Blocked: "+task.BlockedReason)
		}
		if task.AssignedAgentID != "" {
			lines = append(lines, "Assigned agent: "+task.AssignedAgentID)
		}
		if task.Description != "" {
			lines = append(lines, "Description: "+task.Description)
		}
		if len(item.Task.Dependencies) != 0 {
			lines = append(lines, "Dependencies:")
			for _, dependency := range item.Task.Dependencies {
				lines = append(lines, "- "+dependency.DependsOnTaskID)
			}
		}
	case recordRun:
		run := item.Run
		lines = append(lines,
			"Status: "+run.Status,
			"Task: "+run.TaskID,
			"Agent: "+run.AgentID,
			"Runtime/provider: "+run.Runtime+" / "+run.Provider,
		)
		if run.BlockedQuestion != "" {
			lines = append(lines, "Blocked question: "+run.BlockedQuestion)
		}
		if run.ResultSummary != "" {
			lines = append(lines, "Result: "+run.ResultSummary)
		}
	case recordAgent:
		agent := item.Agent
		lines = append(lines,
			"Name: "+agent.Name,
			"Descriptive role: "+agent.Role,
			"Provider/runtime: "+agent.Provider+" / "+agent.Runtime,
			fmt.Sprintf("Enabled: %t", agent.Enabled),
			fmt.Sprintf("Maximum concurrency: %d", agent.MaxConcurrency),
			"Created: "+agent.CreatedAt+" by "+agent.CreatedBy,
			"Updated: "+agent.UpdatedAt+" by "+agent.UpdatedBy,
			"Enter opens this agent inbox; role text does not grant authority.",
		)
	case recordInbox:
		inbox := item.Inbox
		message := inbox.Message
		delivery := inbox.Delivery
		lines = append(lines,
			"Agent inbox message",
			"Kind: "+message.Kind,
			"Body: "+message.Body,
			"Thread: "+message.ThreadID,
			"Sender: "+message.SenderType+" / "+message.SenderID,
			"Created: "+message.CreatedAt,
			"Delivery: "+delivery.Status,
			"Recipient: "+delivery.RecipientName+" / "+delivery.RecipientAgentID,
			"Wake: "+delivery.WakeStatus,
		)
		if message.SenderAgentID != "" {
			lines = append(lines, "Sender agent: "+message.SenderAgentName+" / "+message.SenderAgentID)
		}
		if message.SenderRunID != "" {
			lines = append(lines, "Sender run: "+message.SenderRunID)
		}
		if message.ProjectID != "" || message.TaskID != "" {
			lines = append(lines, "Project/task: "+message.ProjectID+" / "+message.TaskID)
		}
		if message.ReplyToMessageID != "" {
			lines = append(lines, "Reply to: "+message.ReplyToMessageID)
		}
		if len(message.ArtifactIDs) != 0 {
			lines = append(lines, "Artifacts: "+strings.Join(message.ArtifactIDs, ", "))
		}
		if delivery.WakeDiagnostic != "" {
			lines = append(lines, "Wake diagnostic: "+delivery.WakeDiagnostic)
		}
	case recordApproval:
		approval := item.Approval
		lines = append(lines,
			"Status: "+approval.Status,
			"Action: "+approval.ActionID,
			fmt.Sprintf("Expected action revision: %d", approval.ExpectedActionRevision),
		)
		if approval.ExpiresAt != "" {
			lines = append(lines, "Expires: "+approval.ExpiresAt)
		}
	case recordCheck:
		check := item.Check
		lines = append(lines,
			"Status: "+check.Run.Status,
			"Task: "+check.Run.TaskID,
			"Requirement: "+check.Run.RequirementID,
			"Outcome: "+check.Outcome,
			"Requirement state: "+check.RequirementState,
		)
		if check.CurrentFreshness != nil {
			lines = append(lines, "Freshness: "+check.CurrentFreshness.Status+" / "+check.CurrentFreshness.Reason)
		}
	case recordClaim:
		claim := item.WorkClaim
		lines = append(lines,
			"Kind/target: "+claim.Kind+" / "+claim.Target,
			"Mode/policy: "+claim.Mode+" / "+claim.ConflictPolicy,
			"Status: "+claim.Status,
			"Project/task: "+claim.ProjectID+" / "+claim.TaskID,
			"Checkout: "+claim.CheckoutID,
			"Lease expires: "+claim.LeaseExpiresAt,
			"Created: "+claim.CreatedAt+" by "+claim.CreatedBy,
			"Updated: "+claim.UpdatedAt+" by "+claim.UpdatedBy,
		)
		if len(claim.BaselinePaths) != 0 {
			lines = append(lines, "Baseline paths: "+strings.Join(claim.BaselinePaths, ", "))
		}
	case recordOverlap:
		overlap := item.Overlap
		lines = append(lines,
			"Kind/witness: "+overlap.Kind+" / "+overlap.Witness,
			"Severity/status: "+overlap.Severity+" / "+overlap.Status,
			"Policy response: "+overlap.PolicyResponse,
			fmt.Sprintf("Scheduling paused: %t", overlap.SchedulingPaused),
			fmt.Sprintf("Resolution required: %t", overlap.ResolutionRequired),
			"Claims: "+strings.Join(overlap.ClaimIDs, ", "),
			"Tasks: "+strings.Join(overlap.TaskIDs, ", "),
			"Detected: "+overlap.DetectedAt,
		)
		for _, explanation := range overlap.Explanation {
			lines = append(lines, "Explanation: "+explanation)
		}
		if overlap.ResolutionReason != "" {
			lines = append(lines, "Resolution: "+overlap.ResolutionReason+" at "+overlap.ResolvedAt)
		}
	case recordDrift:
		drift := item.Drift
		lines = append(lines,
			"Path: "+drift.Path,
			"Status: "+drift.Status,
			"Claim/task: "+drift.ClaimID+" / "+drift.TaskID,
			"Project/checkout: "+drift.ProjectID+" / "+drift.CheckoutID,
			"Head commit: "+drift.HeadCommit,
			fmt.Sprintf("Observation gap: %t", drift.ObservationGap),
			"First observed: "+drift.FirstObservedAt,
			"Last observed: "+drift.LastObservedAt,
		)
		if drift.ResolvedAt != "" {
			lines = append(lines, "Resolved: "+drift.ResolvedAt)
		}
	case recordMeeting:
		meeting := item.Meeting
		lines = append(lines,
			"Agenda: "+meeting.Agenda,
			"Status/policy: "+meeting.Status+" / "+meeting.Policy,
			"Project/overlap: "+meeting.ProjectID+" / "+meeting.OverlapID,
			"Facilitator: "+meeting.FacilitatorAgentID,
			"Reviewer: "+meeting.ReviewerAgentID,
			"Allowed actions: "+strings.Join(meeting.AllowedActions, ", "),
			"Frozen input hash: "+meeting.FrozenInputHash,
			"Deadline: "+meeting.DeadlineAt,
			"Created: "+meeting.CreatedAt+" by "+meeting.CreatedBy,
			"Updated: "+meeting.UpdatedAt+" by "+meeting.UpdatedBy,
		)
		if meeting.StalledReason != "" {
			lines = append(lines, "Stalled: "+meeting.StalledReason)
		}
	case recordNotification:
		notification := item.Notification
		lines = append(lines,
			"Classification: "+notificationLabel(notification.Kind),
			fmt.Sprintf("Event sequence: %d", notification.EventSequence),
			"Event type: "+notification.EventType,
			"Entity: "+notification.EntityType+" / "+notification.EntityID,
			"Occurred: "+notification.OccurredAt,
		)
	case recordEvent:
		event := item.Event
		lines = append(lines,
			fmt.Sprintf("Sequence: %d", event.Sequence),
			"Type: "+event.Type,
			"Entity: "+event.Entity.Type+" / "+event.Entity.ID,
			"Occurred: "+event.OccurredAt,
			"Actor type: "+event.Actor.ActorType,
		)
	default:
		lines = append(lines, item.Primary, item.Secondary)
		if item.Diagnosis != "" {
			lines = append(lines, "Diagnosis: "+item.Diagnosis)
		}
		if item.Targets != nil {
			lines = append(lines, fmt.Sprintf("Contributing records: %d", len(item.Targets)))
			for _, target := range item.Targets {
				lines = append(lines, "- "+target)
			}
		}
	}
	for index := range lines {
		lines[index] = sanitizeLine(lines[index])
	}
	return lines
}

func (model Model) renderTooSmall(width, height int, styles palette) []string {
	return normalizeLines([]string{
		fitLine("Terminal too small for Crewfold UI.", width),
		fitLine("Minimum stable size: 60 columns x 18 rows.", width),
		fitLine(fmt.Sprintf("Current size: %d x %d.", model.width, model.height), width),
		fitLine("Resize the terminal. With no modal, q quits.", width),
		fitLine("With a hidden modal, Ctrl+C cancels; press it twice after submission to quit.", width),
	}, width, height)
}

func (model Model) renderSetup(width, height int, styles palette) []string {
	socket := model.config.SocketPath
	if strings.TrimSpace(socket) == "" {
		socket = "<not configured>"
	}
	lines := []string{}
	if model.connection == ConnectionFatal {
		lines = append(lines,
			fitLine("Crewfold canonical scope unavailable", width),
			fitLine("The daemon was reached, but this workspace history cannot be operated safely.", width),
			fitLine("Initialize the missing scope or repair the reported protocol/event history.", width),
			fitLine("Then press r to resolve the scope and rebuild every canonical section.", width),
		)
	} else {
		lines = append(lines,
			fitLine("Crewfold daemon unavailable", width),
			fitLine("No daemon is reachable at "+socket+".", width),
			fitLine("Start it in another terminal:", width),
			fitLine("crewfold daemon run --data-dir <path> --socket <path>", width),
			fitLine("Then relaunch or press r to retry:", width),
			fitLine("crewfold ui --socket <path> --workspace <name-or-id>", width),
		)
	}
	if model.lastError != "" {
		lines = append(lines, fitLine("Diagnostic: "+model.lastError, width))
	}
	return normalizeLines(lines, width, height)
}

func (model Model) renderModal(width, height int, styles palette) []string {
	lines := []string{}
	switch model.modal.Kind {
	case modalHelp:
		lines = []string{
			"Keyboard help",
			"Tab / Shift+Tab   move focus",
			"arrows or j/k     move selection",
			"PgUp/PgDn, g/G    page, first, last",
			"Enter             inspect only",
			"Esc               back or cancel",
			"/                 filter records",
			"a                 available actions",
			"r                 refresh canonical reads",
			"q                 quit outside input/modal",
			"Ctrl+C            cancel, then quit",
		}
	case modalFilter:
		lines = []string{"Filter visible records", "/ " + model.filter.Value(), "Enter applies; Esc restores the previous filter."}
	case modalActions:
		lines = []string{"Available actions"}
		for index, choice := range model.modal.Choices {
			marker := "  "
			if index == model.modal.ChoiceIndex {
				marker = "> "
			}
			line := fitLine(marker+choice.Kind.String()+" — "+choice.Consequence, width)
			lines = append(lines, line)
		}
	case modalReview:
		return model.renderReviewModal(width, height)
	case modalError:
		lines = []string{"Action could not be completed", model.modal.Message, "Press Esc or Enter to close."}
	case modalWorkspace:
		lines = []string{"Choose a workspace"}
		for index, workspace := range model.modal.Workspaces {
			marker := "  "
			if index == model.modal.ChoiceIndex {
				marker = "> "
			}
			lines = append(lines, marker+workspace.Name+" | "+workspace.ID)
		}
	}
	for index := range lines {
		lines[index] = fitLine(lines[index], width)
	}
	return normalizeLines(lines, width, height)
}

func (model Model) renderReviewModal(width, height int) []string {
	content := model.reviewContentLines(width)
	viewport := height - 2
	if viewport < 1 {
		viewport = 1
	}
	maxOffset := len(content) - viewport
	if maxOffset < 0 {
		maxOffset = 0
	}
	offset := model.modal.ReviewOffset
	if offset < 0 {
		offset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}
	end := offset + viewport
	if end > len(content) {
		end = len(content)
	}
	lines := []string{"Review action"}
	lines = append(lines, content[offset:end]...)
	lines = append(lines, fmt.Sprintf("Review lines %d-%d of %d · arrows/PgUp/PgDn scroll", visibleStart(offset, len(content)), end, len(content)))
	for index := range lines {
		lines[index] = fitLine(lines[index], width)
	}
	return normalizeLines(lines, width, height)
}

func (model Model) reviewViewport() int {
	viewport := model.height - 4
	if viewport < 1 {
		return 1
	}
	return viewport
}

func (model Model) reviewMaxOffset() int {
	maximum := len(model.reviewContentLines(model.width)) - model.reviewViewport()
	if maximum < 0 {
		return 0
	}
	return maximum
}

func (model Model) reviewFullyViewed() bool {
	return model.modal.ReviewOffset >= model.reviewMaxOffset()
}

func (model Model) reviewContentLines(width int) []string {
	review := model.modal.Review
	logical := []string{
		"Action: " + review.Choice.Kind.String(),
		"Target: " + review.Choice.TargetType + " / " + review.Choice.TargetID,
	}
	if review.HasApprovalContext {
		action := review.SupervisorAction
		logical = append(logical,
			fmt.Sprintf("Approval revision: %d", review.Approval.Revision),
			"Supervisor action: "+action.ID,
			fmt.Sprintf("Action revision: %d", action.Revision),
			fmt.Sprintf("Expected action revision: %d", review.Approval.ExpectedActionRevision),
			"Condition: "+action.Condition,
			"Response: "+action.Response,
		)
	} else if review.Choice.Revision > 0 {
		logical = append(logical, fmt.Sprintf("Expected revision: %d", review.Choice.Revision))
	}
	if review.Choice.Kind == actionStopRun {
		logical = append(logical, fmt.Sprintf("Grace period: %d ms", review.Choice.GracePeriodMillis))
	}
	logical = append(logical, "Consequence: "+review.Choice.Consequence)
	if review.IdempotencyKey != "" {
		logical = append(logical, "Idempotency key: "+review.IdempotencyKey)
	}
	if review.Choice.RequiresNote {
		note := canonicalDecisionNote(model.actionNote.Value())
		if review.RequestFrozen {
			note = review.DecisionNote
		}
		logical = append(logical, "Owner decision note: "+note)
		if review.RequestFrozen {
			logical = append(logical, "The submitted note is frozen for exact idempotent replay.")
		}
	}
	if review.HasApprovalContext {
		action := review.SupervisorAction
		logical = append(logical,
			"Condition key: "+action.ConditionKey,
			"Workspace ID: "+displayID(action.WorkspaceID),
			"Project ID: "+displayID(action.ProjectID),
			"Objective ID: "+displayID(action.ObjectiveID),
			"Task ID: "+displayID(action.TaskID),
			"Run ID: "+displayID(action.RunID),
			"Prior run ID: "+displayID(action.PriorRunID),
			"Agent ID: "+displayID(action.AgentID),
			"Intent ID: "+displayID(action.IntentID),
			"Source proposal ID: "+displayID(action.SourceProposalID),
			"Source action ID: "+displayID(action.SourceActionID),
			fmt.Sprintf("Target entity revision: %d", action.EntityRevision),
			fmt.Sprintf("Policy revision: %d", action.PolicyRevision),
			fmt.Sprintf("As-of event sequence: %d", action.AsOfEventSequence),
			"Action content SHA-256: "+action.ContentSHA256,
			"Reasons:",
		)
		if len(action.Reasons) == 0 {
			logical = append(logical, "- none")
		}
		for _, reason := range action.Reasons {
			logical = append(logical, "- "+reason)
		}
	}
	if review.Executing {
		logical = append(logical, "Submitting through the canonical local API…")
	}
	if review.AmbiguousError != "" {
		logical = append(logical,
			"Outcome unknown: "+review.AmbiguousError,
			"The original idempotency key is retained. Ctrl+Enter replays that exact request once the dashboard is live.",
		)
	}
	wrapped := make([]string, 0, len(logical))
	for _, line := range logical {
		line = sanitizeLine(line)
		for _, visual := range strings.Split(lipgloss.Wrap(line, width, ""), "\n") {
			wrapped = append(wrapped, visual)
		}
	}
	return wrapped
}

func visibleStart(offset, total int) int {
	if total == 0 {
		return 0
	}
	return offset + 1
}

func displayID(value string) string {
	if value == "" {
		return "<none>"
	}
	return value
}

func paneTitle(title string, width int) string {
	return fitLine("-- "+title+" "+strings.Repeat("-", width), width)
}

func normalizeLines(lines []string, width, height int) []string {
	if height <= 0 {
		return nil
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	result := make([]string, 0, height)
	for _, line := range lines {
		result = append(result, fitLine(line, width))
	}
	for len(result) < height {
		result = append(result, strings.Repeat(" ", width))
	}
	return result
}

func joinColumns(height int, columns ...[]string) []string {
	result := make([]string, height)
	for row := 0; row < height; row++ {
		var builder strings.Builder
		for _, column := range columns {
			if row < len(column) {
				builder.WriteString(column[row])
			}
		}
		result[row] = builder.String()
	}
	return result
}
