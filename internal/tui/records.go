package tui

import (
	"fmt"
	"sort"
	"strings"

	"crewfold/internal/domain"
)

func (model Model) recordsFor(route Route) []record {
	frame := model.currentFrame()
	if route == RouteCoordination && frame.EntityType == "agent_inbox" {
		items := model.inboxes[frame.EntityID]
		records := make([]record, 0, len(items)+1)
		if len(items) == maxInboxItems {
			records = append(records, record{
				Kind: recordSummary, ID: "inbox-bound:" + frame.EntityID,
				Primary:   fmt.Sprintf("Agent inbox — %d items shown", len(items)),
				Secondary: "The inbox read is bounded at 50 items; the API does not expose a total.",
				Diagnosis: "Older inbox messages may exist. This view does not present the bounded result as a complete inbox.",
			})
		}
		for index := range items {
			item := &items[index]
			records = append(records, inboxRecord(item))
		}
		return records
	}
	var records []record
	drillAppliedBeforeBound := false
	switch route {
	case RouteOverview:
		records = model.overviewRecords()
	case RouteBriefing:
		briefing := &model.snapshot.Briefing
		if briefing.ID != "" {
			records = append(records, record{
				Kind:      recordBriefing,
				ID:        briefing.ID,
				Revision:  briefing.Revision,
				Primary:   fmt.Sprintf("Briefing at event %d", briefing.EventCursor),
				Secondary: fmt.Sprintf("cutoff %d · since %d · %d claims", briefing.CutoffEventSequence, briefing.SinceEventSequence, len(briefing.Claims)),
				Briefing:  briefing,
			})
		}
		for index := range model.snapshot.Briefing.Claims {
			claim := &model.snapshot.Briefing.Claims[index]
			item := record{
				Kind:      recordBriefingClaim,
				ID:        claim.ID,
				Primary:   claim.Summary,
				Secondary: strings.Join([]string{claim.Kind, claim.Urgency, claim.Status}, " · "),
				Claim:     claim,
			}
			if explanation, ok := model.briefingExplanations[briefingExplanationKey(briefing.ID, claim.ID)]; ok {
				explanationCopy := explanation
				item.Explanation = &explanationCopy
			}
			records = append(records, item)
		}
		for _, omission := range model.snapshot.Briefing.Omitted {
			records = append(records, record{
				Kind:      recordSummary,
				ID:        "omission:" + omission.Section + ":" + omission.Reason,
				Primary:   fmt.Sprintf("%d omitted", omission.Count),
				Secondary: omission.Section + " · " + omission.Reason,
			})
		}
	case RouteWork:
		objectives := make([]record, 0, len(model.snapshot.Objectives))
		for index := range model.snapshot.Objectives {
			objective := &model.snapshot.Objectives[index]
			objectives = append(objectives, objectiveRecord(objective))
		}
		tasks := make([]record, 0, len(model.snapshot.Tasks))
		for index := range model.snapshot.Tasks {
			detail := &model.snapshot.Tasks[index]
			tasks = append(tasks, taskRecord(detail))
		}
		runs := make([]record, 0, len(model.snapshot.Runs))
		for index := range model.snapshot.Runs {
			run := &model.snapshot.Runs[index]
			runs = append(runs, runRecord(run))
		}
		sections, targeted := targetRecordSections(frame, route, []recordSection{
			{ID: "objectives", Label: "Objectives", Records: objectives, Total: model.snapshot.ObjectiveTotal, HasMore: model.snapshot.ObjectiveHasMore},
			{ID: "tasks", Label: "Tasks", Records: tasks, Total: model.snapshot.TaskTotal, HasMore: model.snapshot.TaskHasMore},
			{ID: "runs", Label: "Runs", Records: runs, Total: model.snapshot.RunTotal, HasMore: model.snapshot.RunHasMore},
		})
		drillAppliedBeforeBound = targeted
		records = combineRecordSections("work", sections)
	case RouteDecisions:
		briefingDecisions := []record{}
		for index := range model.snapshot.Briefing.Claims {
			claim := &model.snapshot.Briefing.Claims[index]
			if claim.Kind != domain.BriefingClaimRequiredDecision {
				continue
			}
			briefingDecisions = append(briefingDecisions, record{
				Kind:      recordBriefingClaim,
				ID:        claim.ID,
				Primary:   claim.Summary,
				Secondary: claim.Urgency + " · " + claim.Status,
				Claim:     claim,
			})
			if explanation, ok := model.briefingExplanations[briefingExplanationKey(model.snapshot.Briefing.ID, claim.ID)]; ok {
				explanationCopy := explanation
				briefingDecisions[len(briefingDecisions)-1].Explanation = &explanationCopy
			}
		}
		if omitted := model.omittedIn(domain.BriefingSectionRequiredDecisions); omitted > 0 {
			briefingDecisions = append(briefingDecisions, record{
				Kind: recordSummary, ID: "decisions:briefing-omitted",
				Primary:   fmt.Sprintf("%d additional required decisions omitted", omitted),
				Secondary: "Canonical briefing bound; omitted claims have no drillable IDs.",
				Diagnosis: "This Decisions view is incomplete by the exact omission count reported by the M18 briefing.",
			})
		}
		approvals := make([]record, 0, len(model.snapshot.Approvals))
		for index := range model.snapshot.Approvals {
			approval := &model.snapshot.Approvals[index]
			approvals = append(approvals, approvalRecord(approval))
		}
		meetings := []record{}
		for index := range model.snapshot.Meetings {
			meeting := &model.snapshot.Meetings[index]
			meetings = append(meetings, meetingRecord(meeting))
		}
		sections, targeted := targetRecordSections(frame, route, []recordSection{
			{ID: "briefing", Label: "Briefing decisions", Records: briefingDecisions, Total: len(briefingDecisions)},
			{ID: "approvals", Label: "Approvals", Records: approvals, Total: model.snapshot.ApprovalTotal, HasMore: model.snapshot.ApprovalHasMore},
			{ID: "meetings", Label: "Meetings", Records: meetings, Total: model.snapshot.MeetingTotal, HasMore: model.snapshot.MeetingHasMore},
		})
		drillAppliedBeforeBound = targeted
		records = combineRecordSections("decisions", sections)
	case RouteChecks:
		checks := make([]record, 0, len(model.snapshot.Checks))
		for index := range model.snapshot.Checks {
			item := &model.snapshot.Checks[index]
			checks = append(checks, checkRecord(item))
		}
		sections, targeted := targetRecordSections(frame, route, []recordSection{{ID: "checks", Label: "Checks", Records: checks, Total: model.snapshot.CheckTotal, HasMore: model.snapshot.CheckHasMore}})
		drillAppliedBeforeBound = targeted
		records = combineRecordSections("checks", sections)
	case RouteCoordination:
		agents := make([]record, 0, len(model.snapshot.Agents))
		for index := range model.snapshot.Agents {
			agent := &model.snapshot.Agents[index]
			agents = append(agents, agentRecord(agent))
		}
		runs := make([]record, 0, len(model.snapshot.Runs))
		for index := range model.snapshot.Runs {
			run := &model.snapshot.Runs[index]
			runs = append(runs, runRecord(run))
		}
		claims := make([]record, 0, len(model.snapshot.Claims))
		for index := range model.snapshot.Claims {
			claim := &model.snapshot.Claims[index]
			claims = append(claims, workClaimRecord(claim))
		}
		overlaps := make([]record, 0, len(model.snapshot.Overlaps))
		for index := range model.snapshot.Overlaps {
			overlap := &model.snapshot.Overlaps[index]
			overlaps = append(overlaps, overlapRecord(overlap))
		}
		drifts := make([]record, 0, len(model.snapshot.Drifts))
		for index := range model.snapshot.Drifts {
			drift := &model.snapshot.Drifts[index]
			drifts = append(drifts, driftRecord(drift))
		}
		meetings := make([]record, 0, len(model.snapshot.Meetings))
		for index := range model.snapshot.Meetings {
			meeting := &model.snapshot.Meetings[index]
			meetings = append(meetings, meetingRecord(meeting))
		}
		sections, targeted := targetRecordSections(frame, route, []recordSection{
			{ID: "agents", Label: "Agent inboxes", Records: agents, Total: model.snapshot.AgentTotal, HasMore: model.snapshot.AgentHasMore},
			{ID: "runs", Label: "Runs", Records: runs, Total: model.snapshot.RunTotal, HasMore: model.snapshot.RunHasMore},
			{ID: "claims", Label: "Claims", Records: claims, Total: model.snapshot.ClaimTotal, HasMore: model.snapshot.ClaimHasMore},
			{ID: "overlaps", Label: "Overlaps", Records: overlaps, Total: model.snapshot.OverlapTotal, HasMore: model.snapshot.OverlapHasMore},
			{ID: "drifts", Label: "Drifts", Records: drifts, Total: model.snapshot.DriftTotal, HasMore: model.snapshot.DriftHasMore},
			{ID: "meetings", Label: "Meetings", Records: meetings, Total: model.snapshot.MeetingTotal, HasMore: model.snapshot.MeetingHasMore},
		})
		drillAppliedBeforeBound = targeted
		records = combineRecordSections("coordination", sections)
	case RouteActivity:
		for index := range model.snapshot.Notifications {
			item := &model.snapshot.Notifications[index]
			records = append(records, notificationRecord(item))
		}
		for index := range model.snapshot.Events {
			event := &model.snapshot.Events[index]
			records = append(records, eventRecord(event))
		}
	default:
		return nil
	}

	frame = model.currentFrame()
	if !drillAppliedBeforeBound && frame.Route == route && frame.TargetIDs != nil {
		targets := make(map[string]struct{}, len(frame.TargetIDs))
		for _, id := range frame.TargetIDs {
			targets[id] = struct{}{}
		}
		drilled := records[:0]
		for _, item := range records {
			if _, ok := targets[item.ID]; ok {
				drilled = append(drilled, item)
			}
		}
		records = drilled
	}
	if model.filterText == "" {
		return boundScreenRecords(route.String(), records)
	}
	filtered := make([]record, 0, len(records))
	for _, item := range records {
		if matchesFilter(item, model.filterText) {
			filtered = append(filtered, item)
		}
	}
	return boundScreenRecords(route.String(), filtered)
}

func targetRecordSections(frame routeFrame, route Route, sections []recordSection) ([]recordSection, bool) {
	if frame.Route != route || frame.TargetIDs == nil {
		return sections, false
	}
	targets := make(map[string]struct{}, len(frame.TargetIDs))
	for _, id := range frame.TargetIDs {
		targets[id] = struct{}{}
	}
	result := make([]recordSection, len(sections))
	for index, section := range sections {
		result[index] = section
		result[index].Records = make([]record, 0, len(section.Records))
		for _, item := range section.Records {
			if _, ok := targets[item.ID]; ok {
				result[index].Records = append(result[index].Records, item)
			}
		}
		// Target IDs come from this same canonical snapshot. The drill is a
		// bounded exact subset, not the source collection's independent page.
		result[index].Total = len(result[index].Records)
		result[index].HasMore = false
	}
	return result, true
}

func objectiveRecord(objective *domain.Objective) record {
	return record{Kind: recordObjective, ID: objective.ID, Revision: objective.Revision, Primary: objective.Title, Secondary: "objective · " + objective.Status, Objective: objective}
}

type recordSection struct {
	ID      string
	Label   string
	Records []record
	Total   int
	HasMore bool
}

func combineRecordSections(screen string, sections []recordSection) []record {
	itemCount, summaryCount := 0, 0
	for _, section := range sections {
		itemCount += len(section.Records)
		if section.HasMore {
			summaryCount++
		}
	}
	screenBound := itemCount+summaryCount > maxCachedRecords
	if !screenBound && summaryCount == 0 {
		result := make([]record, 0, itemCount)
		for _, section := range sections {
			result = append(result, section.Records...)
		}
		return result
	}
	showSummary := make([]bool, len(sections))
	summaryCount = 0
	for index, section := range sections {
		showSummary[index] = section.HasMore || screenBound && len(section.Records) > 0
		if showSummary[index] {
			summaryCount++
		}
	}
	capacity := maxCachedRecords - summaryCount
	if capacity < 0 {
		capacity = 0
	}
	shown := make([]int, len(sections))
	items := make([]record, 0, capacity)
	for len(items) < capacity {
		advanced := false
		for index := range sections {
			if shown[index] >= len(sections[index].Records) || len(items) >= capacity {
				continue
			}
			items = append(items, sections[index].Records[shown[index]])
			shown[index]++
			advanced = true
		}
		if !advanced {
			break
		}
	}
	result := make([]record, 0, summaryCount+len(items))
	for index, section := range sections {
		if !showSummary[index] {
			continue
		}
		canonicalTotal := section.Total
		if canonicalTotal < len(section.Records) {
			canonicalTotal = len(section.Records)
		}
		cachedOmitted := len(section.Records) - shown[index]
		apiOmitted := canonicalTotal - len(section.Records)
		if apiOmitted < 0 {
			apiOmitted = 0
		}
		diagnosis := fmt.Sprintf("%d cached records omitted by the %d-record %s screen bound; %d additional canonical records were not cached.", cachedOmitted, maxCachedRecords, screen, apiOmitted)
		result = append(result, record{
			Kind: recordSummary, ID: "section:" + screen + ":" + section.ID,
			Primary:   fmt.Sprintf("%s — %d shown", section.Label, shown[index]),
			Secondary: fmt.Sprintf("%d cached · %d canonical", len(section.Records), canonicalTotal),
			Diagnosis: diagnosis,
		})
	}
	return append(result, items...)
}

func boundScreenRecords(screen string, records []record) []record {
	if len(records) <= maxCachedRecords {
		return records
	}
	shown := maxCachedRecords - 1
	diagnostic := record{
		Kind: recordSummary, ID: "screen-bound:" + screen,
		Primary:   fmt.Sprintf("%s view incomplete — %d shown", screen, shown),
		Secondary: fmt.Sprintf("%d loaded records · %d omitted by the screen bound", len(records), len(records)-shown),
		Diagnosis: "The omitted loaded records are not represented as a complete drill-down set.",
	}
	result := make([]record, 0, maxCachedRecords)
	result = append(result, diagnostic)
	return append(result, records[:shown]...)
}

func (model Model) overviewRecords() []record {
	claimCounts := make(map[string]int)
	claimIDs := make(map[string][]string)
	for _, claim := range model.snapshot.Briefing.Claims {
		claimCounts[claim.Kind]++
		claimIDs[claim.Kind] = append(claimIDs[claim.Kind], claim.ID)
	}
	activeRuns := 0
	activeRunIDs := []string{}
	for _, run := range model.snapshot.Runs {
		if isLiveRun(run.Status) {
			activeRuns++
			activeRunIDs = append(activeRunIDs, run.ID)
		}
	}
	blockedTasks := 0
	readyTasks := 0
	workIDs := []string{}
	for _, detail := range model.snapshot.Tasks {
		switch detail.Task.Status {
		case domain.TaskBlocked:
			blockedTasks++
			workIDs = append(workIDs, detail.Task.ID)
		case domain.TaskReady:
			readyTasks++
			workIDs = append(workIDs, detail.Task.ID)
		}
	}
	pendingApprovals := 0
	pendingApprovalIDs := []string{}
	for _, approval := range model.snapshot.Approvals {
		if approval.Status == domain.ApprovalPending {
			pendingApprovals++
			pendingApprovalIDs = append(pendingApprovalIDs, approval.ID)
		}
	}
	decisionOmitted := model.omittedIn(domain.BriefingSectionRequiredDecisions)
	riskUnknownOmitted := model.omittedIn(domain.BriefingSectionRisksUnknowns)
	records := []record{
		visibleClaimSummary("summary:decisions", "required decisions", claimCounts[domain.BriefingClaimRequiredDecision], decisionOmitted, claimIDs[domain.BriefingClaimRequiredDecision], RouteDecisions, "Briefing owner attention"),
		visibleClaimSummary("summary:risks", "risks", claimCounts[domain.BriefingClaimRisk], riskUnknownOmitted, claimIDs[domain.BriefingClaimRisk], RouteBriefing, "Current evidence-backed risks"),
		visibleClaimSummary("summary:unknowns", "unknowns", claimCounts[domain.BriefingClaimUnknown], riskUnknownOmitted, claimIDs[domain.BriefingClaimUnknown], RouteBriefing, "Current explicit unknowns"),
	}
	if model.snapshot.TaskHasMore {
		records = append(records, incompleteSummary("summary:tasks", "Task overview incomplete", len(model.snapshot.Tasks), model.snapshot.TaskTotal, RouteWork))
	} else {
		records = append(records, record{Kind: recordSummary, ID: "summary:tasks", Primary: fmt.Sprintf("%d ready · %d blocked tasks", readyTasks, blockedTasks), Secondary: "Canonical task state", Targets: workIDs, DrillRoute: RouteWork})
	}
	if model.snapshot.RunHasMore {
		records = append(records, incompleteSummary("summary:runs", "Run overview incomplete", len(model.snapshot.Runs), model.snapshot.RunTotal, RouteCoordination))
	} else {
		records = append(records, record{Kind: recordSummary, ID: "summary:runs", Primary: fmt.Sprintf("%d active runs", activeRuns), Secondary: "Canonical runtime state", Targets: activeRunIDs, DrillRoute: RouteCoordination})
	}
	if model.snapshot.ApprovalHasMore {
		records = append(records, incompleteSummary("summary:approvals", "Approval overview incomplete", len(model.snapshot.Approvals), model.snapshot.ApprovalTotal, RouteDecisions))
	} else {
		records = append(records, record{Kind: recordSummary, ID: "summary:approvals", Primary: fmt.Sprintf("%d pending approvals", pendingApprovals), Secondary: "Owner authority required", Targets: pendingApprovalIDs, DrillRoute: RouteDecisions})
	}
	return records
}

func (model Model) omittedIn(section string) int {
	total := 0
	for _, omission := range model.snapshot.Briefing.Omitted {
		if omission.Section == section {
			total += omission.Count
		}
	}
	return total
}

func visibleClaimSummary(id, noun string, visible, omitted int, ids []string, route Route, secondary string) record {
	primary := fmt.Sprintf("%d %s", visible, noun)
	diagnosis := ""
	if omitted > 0 {
		primary = fmt.Sprintf("%d visible %s", visible, noun)
		diagnosis = fmt.Sprintf("The canonical briefing omitted %d additional claims in this bounded section; only visible claim IDs are drillable.", omitted)
		secondary += fmt.Sprintf(" · %d section claims omitted", omitted)
	}
	return record{Kind: recordSummary, ID: id, Primary: primary, Secondary: secondary, Targets: nonNilIDs(ids), DrillRoute: route, Diagnosis: diagnosis}
}

func incompleteSummary(id, title string, loaded, total int, route Route) record {
	return record{
		Kind:       recordSummary,
		ID:         id,
		Primary:    title,
		Secondary:  fmt.Sprintf("%d of %d records cached; open the canonical list", loaded, total),
		DrillRoute: route,
		Diagnosis:  "No aggregate count is shown because the full producer set is not loaded.",
	}
}

func nonNilIDs(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func taskRecord(detail *domain.TaskDetail) record {
	return record{
		Kind:      recordTask,
		ID:        detail.Task.ID,
		Revision:  detail.Task.Revision,
		Primary:   detail.Task.Title,
		Secondary: fmt.Sprintf("task · %s · priority %d", detail.Task.Status, detail.Task.Priority),
		Task:      detail,
	}
}

func runRecord(run *domain.RunSummary) record {
	primary := "Run " + run.ID
	return record{
		Kind:      recordRun,
		ID:        run.ID,
		Revision:  run.Revision,
		Primary:   primary,
		Secondary: fmt.Sprintf("run · %s · %s/%s", run.Status, run.Runtime, run.Provider),
		Run:       run,
	}
}

func agentRecord(agent *domain.AgentDefinition) record {
	enabled := "disabled"
	if agent.Enabled {
		enabled = "enabled"
	}
	return record{
		Kind: recordAgent, ID: agent.ID, Revision: agent.Revision, Primary: "Agent inbox — " + agent.Name,
		Secondary: enabled + " · descriptive role: " + agent.Role, Agent: agent, DrillRoute: RouteCoordination,
	}
}

func inboxRecord(item *domain.InboxItem) record {
	primary := "Agent inbox message · " + item.Message.Kind
	if item.Message.SenderAgentName != "" {
		primary += " from " + item.Message.SenderAgentName
	}
	return record{Kind: recordInbox, ID: item.Message.ID, Primary: primary, Secondary: item.Delivery.Status + " · " + item.Message.CreatedAt, Inbox: item}
}

func workClaimRecord(claim *domain.WorkClaim) record {
	return record{Kind: recordClaim, ID: claim.ID, Revision: claim.Revision, Primary: "Claim " + claim.Target, Secondary: claim.Kind + " · " + claim.Mode + " · " + claim.Status, WorkClaim: claim}
}

func overlapRecord(overlap *domain.WorkOverlap) record {
	return record{Kind: recordOverlap, ID: overlap.ID, Revision: overlap.Revision, Primary: "Overlap " + overlap.Witness, Secondary: overlap.Severity + " · " + overlap.Status, Overlap: overlap}
}

func driftRecord(drift *domain.ClaimDrift) record {
	return record{Kind: recordDrift, ID: drift.ID, Revision: drift.Revision, Primary: "Drift " + drift.Path, Secondary: drift.Status, Drift: drift}
}

func meetingRecord(meeting *domain.Meeting) record {
	return record{Kind: recordMeeting, ID: meeting.ID, Revision: meeting.Revision, Primary: "Meeting " + meeting.Agenda, Secondary: meeting.Status + " · " + meeting.Policy, Meeting: meeting}
}

func approvalRecord(approval *domain.ApprovalRequest) record {
	return record{
		Kind:      recordApproval,
		ID:        approval.ID,
		Revision:  approval.Revision,
		Primary:   "Approval " + approval.ID,
		Secondary: approval.Status + " · action " + approval.ActionID,
		Approval:  approval,
	}
}

func checkRecord(item *domain.CheckRunListItem) record {
	secondary := "check · " + item.Run.Status
	if item.Outcome != "" {
		secondary += " · " + item.Outcome
	}
	if item.CurrentFreshness != nil {
		secondary += " · " + item.CurrentFreshness.Status
	}
	return record{
		Kind:      recordCheck,
		ID:        item.Run.ID,
		Revision:  item.Run.Revision,
		Primary:   "Check " + item.Run.ID,
		Secondary: secondary,
		Check:     item,
	}
}

func eventRecord(event *domain.Event) record {
	return record{
		Kind:      recordEvent,
		ID:        event.EventID,
		Revision:  event.Entity.Revision,
		Primary:   event.Type,
		Secondary: fmt.Sprintf("#%d · %s/%s", event.Sequence, event.Entity.Type, event.Entity.ID),
		Event:     event,
	}
}

func notificationRecord(item *notification) record {
	return record{
		Kind:         recordNotification,
		ID:           "notification:" + item.ID,
		Primary:      notificationLabel(item.Kind) + ": " + item.EventType,
		Secondary:    fmt.Sprintf("#%d · %s/%s", item.EventSequence, item.EntityType, item.EntityID),
		Notification: item,
	}
}

func notificationLabel(kind notificationKind) string {
	switch kind {
	case notificationFailure:
		return "Failure"
	case notificationConflict:
		return "Conflict"
	default:
		return "Owner attention"
	}
}

func isLiveRun(status string) bool {
	switch status {
	case domain.RunRequested, domain.RunStarting, domain.RunActive, domain.RunBlocked, domain.RunStopping, domain.RunReview:
		return true
	default:
		return false
	}
}

func appendActivity(current []domain.Event, incoming []domain.Event) []domain.Event {
	bySequence := make(map[int64]domain.Event, len(current)+len(incoming))
	for _, event := range current {
		event.Data = nil
		bySequence[event.Sequence] = event
	}
	for _, event := range incoming {
		event.Data = nil
		bySequence[event.Sequence] = event
	}
	sequences := make([]int64, 0, len(bySequence))
	for sequence := range bySequence {
		sequences = append(sequences, sequence)
	}
	sort.Slice(sequences, func(left, right int) bool { return sequences[left] > sequences[right] })
	if len(sequences) > maxActivityEvents {
		sequences = sequences[:maxActivityEvents]
	}
	result := make([]domain.Event, 0, len(sequences))
	for _, sequence := range sequences {
		result = append(result, bySequence[sequence])
	}
	return result
}

var notificationEventKinds = map[string]notificationKind{
	"approval.requested":            notificationOwnerAttention,
	"task.blocked":                  notificationOwnerAttention,
	"task.failed":                   notificationFailure,
	"run.blocked":                   notificationOwnerAttention,
	"run.failed":                    notificationFailure,
	"run.lost":                      notificationFailure,
	"run.start_failed":              notificationFailure,
	"overlap.opened":                notificationConflict,
	"meeting.stalled":               notificationConflict,
	"contradiction.detected":        notificationConflict,
	"check.notification_unroutable": notificationFailure,
	"check.freshness_stale":         notificationOwnerAttention,
	"context_delta.rebase_required": notificationOwnerAttention,
}

func appendNotifications(current []notification, events []domain.Event) []notification {
	incoming := make([]notification, 0, len(events))
	for _, event := range events {
		kind, tracked := notificationEventKinds[event.Type]
		if !tracked {
			continue
		}
		id := event.EventID
		if id == "" {
			id = fmt.Sprintf("event-%d", event.Sequence)
		}
		incoming = append(incoming, notification{
			ID: id, Kind: kind, EventSequence: event.Sequence, EventType: event.Type,
			EntityType: event.Entity.Type, EntityID: event.Entity.ID, OccurredAt: event.OccurredAt,
		})
	}
	return mergeNotifications(current, incoming)
}

func mergeNotifications(current, incoming []notification) []notification {
	byID := make(map[string]notification, len(current)+len(incoming))
	for _, item := range current {
		byID[item.ID] = item
	}
	for _, item := range incoming {
		byID[item.ID] = item
	}
	result := make([]notification, 0, len(byID))
	for _, item := range byID {
		result = append(result, item)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].EventSequence != result[right].EventSequence {
			return result[left].EventSequence > result[right].EventSequence
		}
		return result[left].ID < result[right].ID
	})
	if len(result) > maxNotifications {
		result = result[:maxNotifications]
	}
	return result
}

func (model Model) selectedRecord() (record, bool) {
	route := model.currentRoute()
	selected := model.selection[route]
	for _, item := range model.recordsFor(route) {
		if item.ID == selected {
			return item, true
		}
	}
	return record{}, false
}
