package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"time"

	tea "charm.land/bubbletea/v2"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

const (
	canonicalReadTimeout = 5 * time.Second
	briefingReadTimeout  = 15 * time.Second
	actionTimeout        = 15 * time.Second
	collectionPageSize   = 200
)

func loadScopeCmd(base context.Context, ioSlots, eventSlot chan struct{}, client *localapi.Client, config Config, generation uint64, requestedCursor int64) tea.Cmd {
	return func() tea.Msg {
		if err := acquireIOSlot(base, ioSlots); err != nil {
			return scopeLoadedMsg{Generation: generation, Err: err}
		}
		defer releaseIOSlot(ioSlots)
		workspace, choices, err := resolveWorkspace(base, client.WithTimeout(canonicalReadTimeout), config.Workspace)
		if err != nil {
			return scopeLoadedMsg{Generation: generation, Err: err, Fatal: isDefinitiveAPIError(err)}
		}
		if len(choices) > 0 {
			return scopeLoadedMsg{Generation: generation, WorkspaceChoices: choices}
		}

		var project *domain.Project
		if config.Project != "" {
			ctx, cancel := context.WithTimeout(base, canonicalReadTimeout)
			result, resolveErr := client.WithTimeout(canonicalReadTimeout).ProjectShow(ctx, workspace.ID, config.Project)
			cancel()
			if resolveErr != nil {
				return scopeLoadedMsg{Generation: generation, Err: resolveErr, Fatal: isDefinitiveAPIError(resolveErr)}
			}
			project = &result.Project
		}

		highWater, err := eventHead(base, eventSlot, client, workspace.ID, requestedCursor)
		if err != nil {
			return scopeLoadedMsg{Generation: generation, Err: err, Fatal: isDefinitiveAPIError(err)}
		}
		targetCursor := requestedCursor
		rewind := highWater < requestedCursor
		if targetCursor == 0 || rewind {
			targetCursor = highWater
		}
		return scopeLoadedMsg{
			Generation: generation, Workspace: workspace, Project: project,
			TargetCursor: targetCursor, HighWater: highWater, Rewind: rewind,
		}
	}
}

func loadSectionCmd(base context.Context, ioSlots chan struct{}, client *localapi.Client, generation uint64, section canonicalSection, workspace domain.Workspace, project *domain.Project) tea.Cmd {
	return func() tea.Msg {
		message := sectionLoadedMsg{Generation: generation, Section: section}
		if err := acquireIOSlot(base, ioSlots); err != nil {
			message.Err = err
			return message
		}
		defer releaseIOSlot(ioSlots)
		var err error
		switch section {
		case sectionBriefing:
			message.Briefing, err = loadBriefing(base, client.WithTimeout(briefingReadTimeout), workspace, project)
		case sectionObjectives:
			message.Objectives, message.Total, message.HasMore, err = loadObjectives(base, client.WithTimeout(canonicalReadTimeout), workspace.ID, projectID(project))
		case sectionTasks:
			message.Tasks, message.Total, message.HasMore, err = loadTasks(base, client.WithTimeout(canonicalReadTimeout), workspace.ID, projectID(project))
		case sectionRuns:
			message.Runs, message.Total, message.HasMore, err = loadRuns(base, client.WithTimeout(canonicalReadTimeout), workspace.ID, projectID(project))
		case sectionAgents:
			message.Agents, message.Total, message.HasMore, err = loadAgents(base, client.WithTimeout(canonicalReadTimeout), workspace.ID)
		case sectionApprovals:
			message.Approvals, message.Total, message.HasMore, err = loadApprovals(base, client.WithTimeout(canonicalReadTimeout), workspace.ID, projectID(project))
		case sectionChecks:
			message.Checks, message.Total, message.HasMore, err = loadChecks(base, client.WithTimeout(canonicalReadTimeout), workspace.ID, projectID(project))
		case sectionClaims:
			message.Claims, message.Total, message.HasMore, err = loadClaims(base, client.WithTimeout(canonicalReadTimeout), workspace.ID, projectID(project))
		case sectionOverlaps:
			message.Overlaps, message.Total, message.HasMore, err = loadOverlaps(base, client.WithTimeout(canonicalReadTimeout), workspace.ID, projectID(project))
		case sectionDrifts:
			message.Drifts, message.Total, message.HasMore, err = loadDrifts(base, client.WithTimeout(canonicalReadTimeout), workspace.ID, projectID(project))
		case sectionMeetings:
			message.Meetings, message.Total, message.HasMore, err = loadMeetings(base, client.WithTimeout(canonicalReadTimeout), workspace.ID, projectID(project))
		default:
			err = errors.New("unknown canonical UI section")
		}
		message.Err = err
		message.Fatal = isDefinitiveAPIError(err)
		return message
	}
}

func resolveWorkspace(base context.Context, client *localapi.Client, identifier string) (domain.Workspace, []domain.Workspace, error) {
	if identifier != "" {
		ctx, cancel := context.WithTimeout(base, canonicalReadTimeout)
		defer cancel()
		result, err := client.WorkspaceShow(ctx, identifier)
		return result.Workspace, nil, err
	}
	workspaces := []domain.Workspace{}
	cursor := ""
	guard := newCollectionPageGuard()
	for page := 0; page < maxCollectionPages; page++ {
		ctx, cancel := context.WithTimeout(base, canonicalReadTimeout)
		result, err := client.WorkspaceList(ctx, localapi.WorkspaceListParams{PageParams: localapi.PageParams{Cursor: cursor, Limit: collectionPageSize}})
		cancel()
		if err != nil {
			return domain.Workspace{}, nil, err
		}
		identities := make([]string, len(result.Workspaces))
		for index := range result.Workspaces {
			identities[index] = result.Workspaces[index].ID
		}
		if err := guard.accept(cursor, result.PageResult, identities); err != nil {
			return domain.Workspace{}, nil, err
		}
		workspaces = append(workspaces, result.Workspaces...)
		if result.HasMore && page+1 == maxCollectionPages {
			return domain.Workspace{}, nil, &scopeError{message: "workspace chooser exceeds three bounded pages; relaunch with --workspace <stable-id-or-exact-name> so no valid scope is silently omitted"}
		}
		if !result.HasMore || result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor
	}
	if len(workspaces) == 0 {
		return domain.Workspace{}, nil, &scopeError{message: "no Crewfold workspaces exist; initialize one with 'crewfold workspace init <name> --socket <path>'"}
	}
	if len(workspaces) == 1 {
		return workspaces[0], nil, nil
	}
	return domain.Workspace{}, workspaces, nil
}

type scopeError struct{ message string }

func (err *scopeError) Error() string { return err.message }

type collectionReadError struct{ message string }

func (err *collectionReadError) Error() string { return err.message }

type collectionPageGuard struct {
	initialized bool
	total       int64
	loaded      int64
	cursors     map[string]struct{}
	identities  map[string]struct{}
}

func newCollectionPageGuard() *collectionPageGuard {
	return &collectionPageGuard{
		cursors:    map[string]struct{}{"": {}},
		identities: make(map[string]struct{}),
	}
}

func (guard *collectionPageGuard) accept(currentCursor string, page localapi.PageResult, identities []string) error {
	if !guard.initialized {
		guard.initialized, guard.total = true, page.Total
	} else if page.Total != guard.total {
		return &collectionReadError{message: "canonical collection total changed across one bounded page chain"}
	}
	pageIdentities := make(map[string]struct{}, len(identities))
	for _, identity := range identities {
		if identity == "" {
			return &collectionReadError{message: "canonical collection page contains an empty record identity"}
		}
		if _, duplicate := guard.identities[identity]; duplicate {
			return &collectionReadError{message: "canonical collection repeats a record identity across pages"}
		}
		if _, duplicate := pageIdentities[identity]; duplicate {
			return &collectionReadError{message: "canonical collection repeats a record identity within one page"}
		}
		pageIdentities[identity] = struct{}{}
	}
	nextLoaded := guard.loaded + int64(len(identities))
	if nextLoaded > guard.total {
		return &collectionReadError{message: "canonical collection contains more records than its declared total"}
	}
	if page.HasMore {
		if page.NextCursor == "" || page.NextCursor == currentCursor {
			return &collectionReadError{message: "canonical collection cursor did not advance"}
		}
		if _, seen := guard.cursors[page.NextCursor]; seen {
			return &collectionReadError{message: "canonical collection cursor chain contains a cycle"}
		}
		if nextLoaded >= guard.total {
			return &collectionReadError{message: "canonical collection continued after reaching its declared total"}
		}
	} else if page.NextCursor != "" || nextLoaded != guard.total {
		return &collectionReadError{message: "terminal canonical collection page is incomplete"}
	}
	for identity := range pageIdentities {
		guard.identities[identity] = struct{}{}
	}
	if page.HasMore {
		guard.cursors[page.NextCursor] = struct{}{}
	}
	guard.loaded = nextLoaded
	return nil
}

func (guard *collectionPageGuard) boundedTotal() int {
	return boundedTotal(guard.total)
}

func loadObjectives(base context.Context, client *localapi.Client, workspace, project string) ([]domain.Objective, int, bool, error) {
	items := []domain.Objective{}
	cursor := ""
	guard := newCollectionPageGuard()
	for page := 0; page < maxCollectionPages; page++ {
		ctx, cancel := context.WithTimeout(base, canonicalReadTimeout)
		result, err := client.ObjectiveList(ctx, localapi.ObjectiveListParams{Workspace: workspace, Project: project, PageParams: localapi.PageParams{Cursor: cursor, Limit: collectionPageSize}})
		cancel()
		if err != nil {
			return nil, 0, false, err
		}
		identities := make([]string, len(result.Objectives))
		for index := range result.Objectives {
			identities[index] = result.Objectives[index].ID
		}
		if err := guard.accept(cursor, result.PageResult, identities); err != nil {
			return nil, 0, false, err
		}
		items = append(items, result.Objectives...)
		if !result.HasMore || result.NextCursor == "" {
			return items, guard.boundedTotal(), false, nil
		}
		cursor = result.NextCursor
	}
	return items, guard.boundedTotal(), true, nil
}

func loadAgents(base context.Context, client *localapi.Client, workspace string) ([]domain.AgentDefinition, int, bool, error) {
	items := []domain.AgentDefinition{}
	cursor := ""
	guard := newCollectionPageGuard()
	for page := 0; page < maxCollectionPages; page++ {
		ctx, cancel := context.WithTimeout(base, canonicalReadTimeout)
		result, err := client.AgentList(ctx, localapi.AgentListParams{Workspace: workspace, PageParams: localapi.PageParams{Cursor: cursor, Limit: collectionPageSize}})
		cancel()
		if err != nil {
			return nil, 0, false, err
		}
		identities := make([]string, len(result.Agents))
		for index := range result.Agents {
			identities[index] = result.Agents[index].ID
		}
		if err := guard.accept(cursor, result.PageResult, identities); err != nil {
			return nil, 0, false, err
		}
		items = append(items, result.Agents...)
		if !result.HasMore || result.NextCursor == "" {
			return items, guard.boundedTotal(), false, nil
		}
		cursor = result.NextCursor
	}
	return items, guard.boundedTotal(), true, nil
}

func eventHead(base context.Context, eventSlot chan struct{}, client *localapi.Client, workspace string, requestedCursor int64) (int64, error) {
	if err := acquireIOSlot(base, eventSlot); err != nil {
		return 0, err
	}
	defer releaseIOSlot(eventSlot)
	ctx, cancel := context.WithTimeout(base, 2*time.Second)
	head, err := client.EventsList(ctx, localapi.EventsListParams{
		Workspace:  workspace,
		After:      requestedCursor,
		PageParams: localapi.PageParams{Limit: 1},
	})
	cancel()
	if err != nil {
		return 0, err
	}
	if head.WorkspaceID != workspace {
		return 0, errors.New("event head returned a different canonical workspace")
	}
	// Activity begins at this UI session's captured high-water. Global event
	// sequence arithmetic cannot identify a workspace's latest N records.
	return head.HighWater, nil
}

func fenceCanonicalCmd(base context.Context, eventSlot chan struct{}, client *localapi.Client, generation uint64, workspace string, after int64) tea.Cmd {
	return func() tea.Msg {
		highWater, err := eventHead(base, eventSlot, client, workspace, after)
		return fenceLoadedMsg{Generation: generation, HighWater: highWater, Fatal: isDefinitiveAPIError(err), Err: err}
	}
}

func loadBriefing(base context.Context, client *localapi.Client, workspace domain.Workspace, project *domain.Project) (domain.ManagementBriefing, error) {
	scopeType := domain.OwnerCheckpointWorkspace
	scopeID := workspace.ID
	if project != nil {
		scopeType = domain.OwnerCheckpointProject
		scopeID = project.ID
	}
	ctx, cancel := context.WithTimeout(base, briefingReadTimeout)
	defer cancel()
	result, err := client.BriefingShow(ctx, localapi.BriefingShowParams{
		Workspace: workspace.ID, ScopeType: scopeType, ScopeIdentifier: scopeID,
	})
	return result.Briefing, err
}

func loadTasks(base context.Context, client *localapi.Client, workspace, project string) ([]domain.TaskDetail, int, bool, error) {
	items := []domain.TaskDetail{}
	cursor := ""
	guard := newCollectionPageGuard()
	for page := 0; page < maxCollectionPages; page++ {
		ctx, cancel := context.WithTimeout(base, canonicalReadTimeout)
		result, err := client.TaskList(ctx, localapi.TaskListParams{
			Workspace: workspace, Project: project,
			PageParams: localapi.PageParams{Cursor: cursor, Limit: collectionPageSize},
		})
		cancel()
		if err != nil {
			return nil, 0, false, err
		}
		identities := make([]string, len(result.Tasks))
		for index := range result.Tasks {
			identities[index] = result.Tasks[index].Task.ID
		}
		if err := guard.accept(cursor, result.PageResult, identities); err != nil {
			return nil, 0, false, err
		}
		items = append(items, result.Tasks...)
		if !result.HasMore || result.NextCursor == "" {
			return items, guard.boundedTotal(), false, nil
		}
		cursor = result.NextCursor
	}
	return items, guard.boundedTotal(), true, nil
}

func loadRuns(base context.Context, client *localapi.Client, workspace, project string) ([]domain.RunSummary, int, bool, error) {
	items := []domain.RunSummary{}
	cursor := ""
	guard := newCollectionPageGuard()
	for page := 0; page < maxCollectionPages; page++ {
		ctx, cancel := context.WithTimeout(base, canonicalReadTimeout)
		result, err := client.RunList(ctx, localapi.RunListParams{
			Workspace:  workspace,
			Project:    project,
			PageParams: localapi.PageParams{Cursor: cursor, Limit: collectionPageSize},
		})
		cancel()
		if err != nil {
			return nil, 0, false, err
		}
		identities := make([]string, len(result.Runs))
		for index := range result.Runs {
			identities[index] = result.Runs[index].ID
		}
		if err := guard.accept(cursor, result.PageResult, identities); err != nil {
			return nil, 0, false, err
		}
		items = append(items, result.Runs...)
		if !result.HasMore || result.NextCursor == "" {
			return items, guard.boundedTotal(), false, nil
		}
		cursor = result.NextCursor
	}
	return items, guard.boundedTotal(), true, nil
}

func loadApprovals(base context.Context, client *localapi.Client, workspace, project string) ([]domain.ApprovalRequest, int, bool, error) {
	items := []domain.ApprovalRequest{}
	cursor := ""
	guard := newCollectionPageGuard()
	for page := 0; page < maxCollectionPages; page++ {
		ctx, cancel := context.WithTimeout(base, canonicalReadTimeout)
		result, err := client.ApprovalList(ctx, localapi.ApprovalListParams{
			Workspace:  workspace,
			Project:    project,
			PageParams: localapi.PageParams{Cursor: cursor, Limit: collectionPageSize},
		})
		cancel()
		if err != nil {
			return nil, 0, false, err
		}
		identities := make([]string, len(result.Approvals))
		for index := range result.Approvals {
			identities[index] = result.Approvals[index].ID
		}
		if err := guard.accept(cursor, result.PageResult, identities); err != nil {
			return nil, 0, false, err
		}
		items = append(items, result.Approvals...)
		if !result.HasMore || result.NextCursor == "" {
			return items, guard.boundedTotal(), false, nil
		}
		cursor = result.NextCursor
	}
	return items, guard.boundedTotal(), true, nil
}

func loadChecks(base context.Context, client *localapi.Client, workspace, project string) ([]domain.CheckRunListItem, int, bool, error) {
	items := []domain.CheckRunListItem{}
	cursor := ""
	guard := newCollectionPageGuard()
	for page := 0; page < maxCollectionPages; page++ {
		ctx, cancel := context.WithTimeout(base, canonicalReadTimeout)
		result, err := client.CheckList(ctx, localapi.CheckListParams{
			Workspace: workspace, Project: project,
			PageParams: localapi.PageParams{Cursor: cursor, Limit: collectionPageSize},
		})
		cancel()
		if err != nil {
			return nil, 0, false, err
		}
		identities := make([]string, len(result.Runs))
		for index := range result.Runs {
			identities[index] = result.Runs[index].Run.ID
		}
		if err := guard.accept(cursor, result.PageResult, identities); err != nil {
			return nil, 0, false, err
		}
		items = append(items, result.Runs...)
		if !result.HasMore || result.NextCursor == "" {
			return items, guard.boundedTotal(), false, nil
		}
		cursor = result.NextCursor
	}
	return items, guard.boundedTotal(), true, nil
}

func loadClaims(base context.Context, client *localapi.Client, workspace, project string) ([]domain.WorkClaim, int, bool, error) {
	items := []domain.WorkClaim{}
	cursor := ""
	guard := newCollectionPageGuard()
	for page := 0; page < maxCollectionPages; page++ {
		ctx, cancel := context.WithTimeout(base, canonicalReadTimeout)
		result, err := client.ClaimList(ctx, localapi.ClaimListParams{Workspace: workspace, Project: project, PageParams: localapi.PageParams{Cursor: cursor, Limit: collectionPageSize}})
		cancel()
		if err != nil {
			return nil, 0, false, err
		}
		identities := make([]string, len(result.Claims))
		for index := range result.Claims {
			identities[index] = result.Claims[index].ID
		}
		if err := guard.accept(cursor, result.PageResult, identities); err != nil {
			return nil, 0, false, err
		}
		items = append(items, result.Claims...)
		if !result.HasMore || result.NextCursor == "" {
			return items, guard.boundedTotal(), false, nil
		}
		cursor = result.NextCursor
	}
	return items, guard.boundedTotal(), true, nil
}

func loadOverlaps(base context.Context, client *localapi.Client, workspace, project string) ([]domain.WorkOverlap, int, bool, error) {
	items := []domain.WorkOverlap{}
	cursor := ""
	guard := newCollectionPageGuard()
	for page := 0; page < maxCollectionPages; page++ {
		ctx, cancel := context.WithTimeout(base, canonicalReadTimeout)
		result, err := client.OverlapList(ctx, localapi.OverlapListParams{Workspace: workspace, Project: project, PageParams: localapi.PageParams{Cursor: cursor, Limit: collectionPageSize}})
		cancel()
		if err != nil {
			return nil, 0, false, err
		}
		identities := make([]string, len(result.Overlaps))
		for index := range result.Overlaps {
			identities[index] = result.Overlaps[index].ID
		}
		if err := guard.accept(cursor, result.PageResult, identities); err != nil {
			return nil, 0, false, err
		}
		items = append(items, result.Overlaps...)
		if !result.HasMore || result.NextCursor == "" {
			return items, guard.boundedTotal(), false, nil
		}
		cursor = result.NextCursor
	}
	return items, guard.boundedTotal(), true, nil
}

func loadDrifts(base context.Context, client *localapi.Client, workspace, project string) ([]domain.ClaimDrift, int, bool, error) {
	items := []domain.ClaimDrift{}
	cursor := ""
	guard := newCollectionPageGuard()
	for page := 0; page < maxCollectionPages; page++ {
		ctx, cancel := context.WithTimeout(base, canonicalReadTimeout)
		result, err := client.DriftList(ctx, localapi.DriftListParams{Workspace: workspace, Project: project, PageParams: localapi.PageParams{Cursor: cursor, Limit: collectionPageSize}})
		cancel()
		if err != nil {
			return nil, 0, false, err
		}
		identities := make([]string, len(result.Drifts))
		for index := range result.Drifts {
			identities[index] = result.Drifts[index].ID
		}
		if err := guard.accept(cursor, result.PageResult, identities); err != nil {
			return nil, 0, false, err
		}
		items = append(items, result.Drifts...)
		if !result.HasMore || result.NextCursor == "" {
			return items, guard.boundedTotal(), false, nil
		}
		cursor = result.NextCursor
	}
	return items, guard.boundedTotal(), true, nil
}

func loadMeetings(base context.Context, client *localapi.Client, workspace, project string) ([]domain.Meeting, int, bool, error) {
	items := []domain.Meeting{}
	cursor := ""
	guard := newCollectionPageGuard()
	for page := 0; page < maxCollectionPages; page++ {
		ctx, cancel := context.WithTimeout(base, canonicalReadTimeout)
		result, err := client.MeetingList(ctx, localapi.MeetingListParams{Workspace: workspace, Project: project, PageParams: localapi.PageParams{Cursor: cursor, Limit: collectionPageSize}})
		cancel()
		if err != nil {
			return nil, 0, false, err
		}
		identities := make([]string, len(result.Meetings))
		for index := range result.Meetings {
			identities[index] = result.Meetings[index].ID
		}
		if err := guard.accept(cursor, result.PageResult, identities); err != nil {
			return nil, 0, false, err
		}
		items = append(items, result.Meetings...)
		if !result.HasMore || result.NextCursor == "" {
			return items, guard.boundedTotal(), false, nil
		}
		cursor = result.NextCursor
	}
	return items, guard.boundedTotal(), true, nil
}

func loadInboxCmd(base context.Context, ioSlots chan struct{}, client *localapi.Client, generation, epoch uint64, workspace, agent string) tea.Cmd {
	return func() tea.Msg {
		if err := acquireIOSlot(base, ioSlots); err != nil {
			return inboxLoadedMsg{Generation: generation, Epoch: epoch, WorkspaceID: workspace, AgentID: agent, Err: err}
		}
		defer releaseIOSlot(ioSlots)
		ctx, cancel := context.WithTimeout(base, canonicalReadTimeout)
		defer cancel()
		result, err := client.WithTimeout(canonicalReadTimeout).InboxList(ctx, workspace, agent, maxInboxItems)
		return inboxLoadedMsg{Generation: generation, Epoch: epoch, WorkspaceID: workspace, AgentID: agent, Items: result.Items, Err: err}
	}
}

func loadTimelineCmd(base context.Context, ioSlots chan struct{}, client *localapi.Client, generation, epoch uint64, workspace, entityType, entityID string) tea.Cmd {
	return func() tea.Msg {
		message := timelineLoadedMsg{
			Generation: generation, Epoch: epoch, WorkspaceID: workspace,
			EntityType: entityType, EntityID: entityID,
		}
		if workspace == "" || entityType == "" || entityID == "" {
			message.Err = errors.New("entity timeline requires an exact workspace and entity")
			return message
		}
		if err := acquireIOSlot(base, ioSlots); err != nil {
			message.Err = err
			return message
		}
		defer releaseIOSlot(ioSlots)

		cursor := ""
		var highWater, total int64 = -1, -1
		events := make([]domain.Event, 0)
		seenCursors := make(map[string]struct{})
		seenEventIDs := make(map[string]struct{})
		seenSequences := make(map[int64]struct{})
		hasMore := false
		var previous int64
		for page := 0; page < maxCollectionPages; page++ {
			ctx, cancel := context.WithTimeout(base, canonicalReadTimeout)
			result, err := client.WithTimeout(canonicalReadTimeout).EventsTimeline(ctx, localapi.EventsTimelineParams{
				Workspace: workspace, EntityType: entityType, EntityID: entityID,
				PageParams: localapi.PageParams{Cursor: cursor, Limit: collectionPageSize},
			})
			cancel()
			if err != nil {
				if cursor != "" && isAPIErrorCode(err, "invalid_cursor") {
					message.Rewind = true
					return message
				}
				message.Err = err
				return message
			}
			if result.WorkspaceID != workspace {
				message.Err = errors.New("entity timeline returned a different canonical workspace")
				return message
			}
			if page == 0 {
				highWater, total = result.HighWater, result.Total
				if total < 0 {
					message.Err = errors.New("entity timeline returned a negative frozen total")
					return message
				}
			} else if result.HighWater != highWater || result.Total != total {
				message.Err = errors.New("entity timeline continuation changed its frozen high-water or total")
				return message
			}
			for _, event := range result.Events {
				if event.WorkspaceID != workspace || event.Entity.Type != entityType || event.Entity.ID != entityID ||
					event.Sequence > highWater || (len(events) != 0 && event.Sequence >= previous) {
					message.Err = errors.New("entity timeline continuation violated exact scope or reverse sequence order")
					return message
				}
				if _, duplicate := seenEventIDs[event.EventID]; duplicate {
					message.Err = errors.New("entity timeline continuation repeated an event ID")
					return message
				}
				if _, duplicate := seenSequences[event.Sequence]; duplicate {
					message.Err = errors.New("entity timeline continuation repeated a sequence")
					return message
				}
				seenEventIDs[event.EventID] = struct{}{}
				seenSequences[event.Sequence] = struct{}{}
				previous = event.Sequence
				// Event payloads are validated by the strict client but the timeline
				// renders only canonical envelope metadata. Do not retain opaque data.
				event.Data = nil
				events = append(events, event)
			}
			if int64(len(events)) > total {
				message.Err = errors.New("entity timeline pages exceeded their frozen total")
				return message
			}
			hasMore = result.HasMore
			if result.HasMore != (result.NextCursor != "") {
				message.Err = errors.New("entity timeline continuation cursor and has-more state disagree")
				return message
			}
			if !result.HasMore {
				if int64(len(events)) != total {
					message.Err = errors.New("terminal entity timeline page did not complete its frozen total")
					return message
				}
				break
			}
			if int64(len(events)) >= total {
				message.Err = errors.New("entity timeline claimed a continuation after completing its frozen total")
				return message
			}
			if result.NextCursor == cursor {
				message.Err = errors.New("entity timeline continuation cursor did not advance")
				return message
			}
			if _, repeated := seenCursors[result.NextCursor]; repeated {
				message.Err = errors.New("entity timeline continuation cursor cycled")
				return message
			}
			seenCursors[result.NextCursor] = struct{}{}
			cursor = result.NextCursor
		}
		message.Timeline = entityTimeline{
			HighWater: highWater, Events: events, Total: boundedTotal(total), HasMore: hasMore,
		}
		return message
	}
}

func projectID(project *domain.Project) string {
	if project == nil {
		return ""
	}
	return project.ID
}

func boundedTotal(total int64) int {
	if total <= 0 {
		return 0
	}
	if total > int64(math.MaxInt) {
		return math.MaxInt
	}
	return int(total)
}

func pollEventsCmd(base context.Context, eventSlot chan struct{}, client *localapi.Client, workspace string, generation uint64, after int64, fence bool, pollEpoch uint64) tea.Cmd {
	return func() tea.Msg {
		if workspace == "" {
			return eventsPolledMsg{Generation: generation, After: after, Fence: fence, PollEpoch: pollEpoch, Err: errors.New("event polling requires a selected workspace")}
		}
		if err := acquireIOSlot(base, eventSlot); err != nil {
			return eventsPolledMsg{Generation: generation, After: after, Fence: fence, PollEpoch: pollEpoch, Err: err}
		}
		defer releaseIOSlot(eventSlot)
		cursor := ""
		candidate := after
		highWater := after
		total := int64(-1)
		events := []domain.Event{}
		hasMore := false
		seenCursors := map[string]struct{}{}
		seenEventIDs := make(map[string]struct{})
		seenSequences := make(map[int64]struct{})
		for page := 0; page < maxEventPages; page++ {
			ctx, cancel := context.WithTimeout(base, 2*time.Second)
			result, err := client.EventsList(ctx, localapi.EventsListParams{
				Workspace: workspace, After: after,
				PageParams: localapi.PageParams{Cursor: cursor, Limit: eventPageSize},
			})
			cancel()
			if err != nil {
				if cursor != "" && isAPIErrorCode(err, "invalid_cursor") {
					return eventsPolledMsg{Generation: generation, After: after, Fence: fence, PollEpoch: pollEpoch, Rewind: true}
				}
				return eventsPolledMsg{Generation: generation, After: after, Fence: fence, PollEpoch: pollEpoch, Err: err}
			}
			if result.WorkspaceID != workspace {
				return eventsPolledMsg{Generation: generation, After: after, Fence: fence, PollEpoch: pollEpoch, Err: errors.New("event page returned a different canonical workspace")}
			}
			if page == 0 {
				highWater = result.HighWater
				total = result.Total
				if total < 0 {
					return eventsPolledMsg{Generation: generation, After: after, Fence: fence, PollEpoch: pollEpoch, Err: errors.New("event page returned a negative frozen total")}
				}
				if highWater < after {
					return eventsPolledMsg{Generation: generation, After: after, Fence: fence, PollEpoch: pollEpoch, HighWater: highWater, Rewind: true}
				}
			} else if result.HighWater != highWater || result.Total != total {
				return eventsPolledMsg{Generation: generation, After: after, Fence: fence, PollEpoch: pollEpoch, Err: errors.New("event continuation changed its frozen high-water or total")}
			}
			for _, event := range result.Events {
				if event.WorkspaceID != workspace || event.Sequence <= candidate || event.Sequence > highWater {
					return eventsPolledMsg{Generation: generation, After: after, Fence: fence, PollEpoch: pollEpoch, Err: errors.New("event continuation violated workspace scope or global sequence order")}
				}
				if _, duplicate := seenEventIDs[event.EventID]; duplicate {
					return eventsPolledMsg{Generation: generation, After: after, Fence: fence, PollEpoch: pollEpoch, Err: errors.New("event continuation repeated an event ID")}
				}
				if _, duplicate := seenSequences[event.Sequence]; duplicate {
					return eventsPolledMsg{Generation: generation, After: after, Fence: fence, PollEpoch: pollEpoch, Err: errors.New("event continuation repeated a sequence")}
				}
				seenEventIDs[event.EventID] = struct{}{}
				seenSequences[event.Sequence] = struct{}{}
				candidate = event.Sequence
				events = append(events, event)
			}
			if int64(len(events)) > total {
				return eventsPolledMsg{Generation: generation, After: after, Fence: fence, PollEpoch: pollEpoch, Err: errors.New("event pages exceeded their frozen total")}
			}
			hasMore = result.HasMore
			if result.HasMore != (result.NextCursor != "") {
				return eventsPolledMsg{Generation: generation, After: after, Fence: fence, PollEpoch: pollEpoch, Err: errors.New("event continuation cursor and has-more state disagree")}
			}
			if !result.HasMore {
				if int64(len(events)) != total {
					return eventsPolledMsg{Generation: generation, After: after, Fence: fence, PollEpoch: pollEpoch, Err: errors.New("terminal event page did not complete its frozen total")}
				}
				break
			}
			if int64(len(events)) >= total {
				return eventsPolledMsg{Generation: generation, After: after, Fence: fence, PollEpoch: pollEpoch, Err: errors.New("event page claimed a continuation after completing its frozen total")}
			}
			if result.NextCursor == cursor {
				return eventsPolledMsg{Generation: generation, After: after, Fence: fence, PollEpoch: pollEpoch, Err: errors.New("event continuation cursor did not advance")}
			}
			if _, repeated := seenCursors[result.NextCursor]; repeated {
				return eventsPolledMsg{Generation: generation, After: after, Fence: fence, PollEpoch: pollEpoch, Err: errors.New("event continuation cursor cycled")}
			}
			seenCursors[result.NextCursor] = struct{}{}
			cursor = result.NextCursor
		}
		if !hasMore {
			candidate = highWater
		}
		return eventsPolledMsg{Generation: generation, After: after, Fence: fence, PollEpoch: pollEpoch, Events: events, Candidate: candidate, HighWater: highWater}
	}
}

func schedulePoll(epoch uint64, delay time.Duration) tea.Cmd {
	return tea.Tick(delay, func(time.Time) tea.Msg { return pollTickMsg{Epoch: epoch} })
}

func scheduleReconnect(epoch uint64, delay time.Duration) tea.Cmd {
	return tea.Tick(delay, func(time.Time) tea.Msg { return reconnectTickMsg{Epoch: epoch} })
}

func reconnectDelay(attempt int) time.Duration {
	delays := [...]time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second}
	if attempt < 0 {
		attempt = 0
	}
	if attempt < len(delays) {
		return delays[attempt]
	}
	return 5 * time.Second
}

func prepareActionCmd(base context.Context, ioSlots chan struct{}, client *localapi.Client, canonicalGeneration uint64, workspace string, choice actionChoice, generation uint64) tea.Cmd {
	return func() tea.Msg {
		message := actionPreparedMsg{
			Generation: generation, CanonicalGeneration: canonicalGeneration, WorkspaceID: workspace, Choice: choice,
		}
		if choice.Kind == actionAttachRun {
			return message
		}
		if isApprovalAction(choice.Kind) {
			ctx, cancel := context.WithTimeout(base, canonicalReadTimeout)
			defer cancel()
			if err := acquireIOSlot(ctx, ioSlots); err != nil {
				message.Err = err
				return message
			}
			result, err := client.WithTimeout(canonicalReadTimeout).SupervisorActionShow(ctx, workspace, choice.ApprovalActionID)
			releaseIOSlot(ioSlots)
			if err != nil {
				message.Err = err
				return message
			}
			message.SupervisorAction = result.Action
			message.HasSupervisorAction = true
		}
		bytes := make([]byte, 16)
		if _, err := rand.Read(bytes); err != nil {
			message.Err = fmt.Errorf("create action idempotency key: %w", err)
			return message
		}
		message.IdempotencyKey = "ui-" + hex.EncodeToString(bytes)
		return message
	}
}

func loadBriefingExplainCmd(base context.Context, ioSlots chan struct{}, client *localapi.Client, generation, epoch uint64, workspace, briefingID, claimID string) tea.Cmd {
	return func() tea.Msg {
		message := briefingExplainLoadedMsg{
			Generation: generation, Epoch: epoch, WorkspaceID: workspace, BriefingID: briefingID, ClaimID: claimID,
		}
		ctx, cancel := context.WithTimeout(base, briefingReadTimeout)
		defer cancel()
		if err := acquireIOSlot(ctx, ioSlots); err != nil {
			message.Err = err
			return message
		}
		result, err := client.WithTimeout(briefingReadTimeout).BriefingExplain(ctx, localapi.BriefingExplainParams{
			Workspace: workspace, Briefing: briefingID, Claim: claimID,
		})
		releaseIOSlot(ioSlots)
		message.Explanation = result.Explanation
		message.Err = err
		return message
	}
}

func executeActionCmd(ctx context.Context, ioSlots chan struct{}, client *localapi.Client, workspace string, review actionReview) tea.Cmd {
	return func() tea.Msg {
		if err := acquireIOSlot(ctx, ioSlots); err != nil {
			if review.Choice.Kind == actionAttachRun {
				return attachReadyMsg{Generation: review.Generation, Err: err}
			}
			return actionCompletedMsg{Generation: review.Generation, IdempotencyKey: review.IdempotencyKey, Kind: review.Choice.Kind, Err: err}
		}
		defer releaseIOSlot(ioSlots)
		client = client.WithTimeout(actionTimeout)
		switch review.Choice.Kind {
		case actionAttachRun:
			result, err := client.RunAttach(ctx, workspace, review.Choice.TargetID)
			return attachReadyMsg{Generation: review.Generation, Result: result, Err: err}
		case actionResumeRun:
			_, err := client.RunResume(ctx, localapi.RunResumeParams{
				Workspace: workspace, Run: review.Choice.TargetID, ExpectedRevision: review.Choice.Revision, IdempotencyKey: review.IdempotencyKey,
			})
			return actionCompletedMsg{Generation: review.Generation, IdempotencyKey: review.IdempotencyKey, Kind: review.Choice.Kind, Err: err}
		case actionStopRun:
			_, err := client.RunStop(ctx, localapi.RunStopParams{
				Workspace: workspace, Run: review.Choice.TargetID, ExpectedRevision: review.Choice.Revision,
				GracePeriodMillis: review.Choice.GracePeriodMillis, IdempotencyKey: review.IdempotencyKey,
			})
			return actionCompletedMsg{Generation: review.Generation, IdempotencyKey: review.IdempotencyKey, Kind: review.Choice.Kind, Err: err}
		case actionAllowApproval:
			_, err := client.ApprovalAllow(ctx, localapi.ApprovalDecisionParams{
				Workspace: workspace, Approval: review.Approval.ID, ExpectedRevision: review.Approval.Revision, DecisionNote: review.DecisionNote, IdempotencyKey: review.IdempotencyKey,
			})
			return actionCompletedMsg{Generation: review.Generation, IdempotencyKey: review.IdempotencyKey, Kind: review.Choice.Kind, Err: err}
		case actionDenyApproval:
			_, err := client.ApprovalDeny(ctx, localapi.ApprovalDecisionParams{
				Workspace: workspace, Approval: review.Approval.ID, ExpectedRevision: review.Approval.Revision, DecisionNote: review.DecisionNote, IdempotencyKey: review.IdempotencyKey,
			})
			return actionCompletedMsg{Generation: review.Generation, IdempotencyKey: review.IdempotencyKey, Kind: review.Choice.Kind, Err: err}
		default:
			return actionCompletedMsg{Generation: review.Generation, IdempotencyKey: review.IdempotencyKey, Kind: review.Choice.Kind, Err: errors.New("unsupported typed UI action")}
		}
	}
}

func acquireIOSlot(ctx context.Context, slots chan struct{}) error {
	select {
	case slots <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func releaseIOSlot(slots chan struct{}) {
	<-slots
}

func isDefinitiveAPIError(err error) bool {
	var apiError *localapi.APIError
	var selectedScope *scopeError
	var invalidCollection *collectionReadError
	return errors.As(err, &selectedScope) || errors.As(err, &invalidCollection) || errors.Is(err, localapi.ErrProtocolMismatch) || errors.As(err, &apiError) && !apiError.Retryable
}

func isAPIErrorCode(err error, code string) bool {
	var apiError *localapi.APIError
	return errors.As(err, &apiError) && apiError.Code == code
}
