package tui

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"crewfold/internal/domain"
)

func TestM19RouteEvictionAndReplacementReleaseOwnedAsyncCaches(t *testing.T) {
	workspaceID := m19TransportWorkspace
	t.Run("depth eviction releases timeline", func(t *testing.T) {
		model := NewModel(Config{Color: ColorNever}, nil)
		model.snapshot.Workspace = m19TransportWorkspaceValue(workspaceID, "personal")
		taskID := "task_00000000000000000000000000000001"
		old := routeFrame{Route: RouteWork, EntityType: "task", EntityID: taskID}
		model.routeStack = []routeFrame{old}
		for index := 1; index < maxRouteDepth; index++ {
			model.routeStack = append(model.routeStack, routeFrame{Route: RouteWork, EntityType: "aggregate", EntityID: fmt.Sprintf("aggregate-%d", index)})
		}
		key := entityTimelineKey(workspaceID, old.EntityType, old.EntityID)
		model.entityTimelines[key] = entityTimeline{HighWater: 10}
		model.pushRoute(routeFrame{Route: RouteActivity})
		if _, retained := model.entityTimelines[key]; retained || len(model.routeStack) != maxRouteDepth {
			t.Fatalf("depth eviction retained timeline cache: routes=%d timelines=%#v", len(model.routeStack), model.entityTimelines)
		}
	})

	t.Run("depth eviction releases inbox", func(t *testing.T) {
		model := NewModel(Config{Color: ColorNever}, nil)
		agentID := "agent_00000000000000000000000000000001"
		model.routeStack = []routeFrame{{Route: RouteCoordination, EntityType: "agent_inbox", EntityID: agentID}}
		for index := 1; index < maxRouteDepth; index++ {
			model.routeStack = append(model.routeStack, routeFrame{Route: RouteOverview, EntityType: "aggregate", EntityID: fmt.Sprintf("aggregate-%d", index)})
		}
		model.inboxes[agentID] = []domain.InboxItem{{Message: domain.Message{ID: "message_old"}}}
		model.pushRoute(routeFrame{Route: RouteActivity})
		if _, retained := model.inboxes[agentID]; retained || len(model.routeStack) != maxRouteDepth {
			t.Fatalf("depth eviction retained inbox cache: routes=%d inboxes=%#v", len(model.routeStack), model.inboxes)
		}
	})

	for _, test := range []struct {
		name  string
		frame routeFrame
		seed  func(*Model)
		check func(Model) bool
	}{
		{
			name:  "navigation replacement releases inbox",
			frame: routeFrame{Route: RouteCoordination, EntityType: "agent_inbox", EntityID: "agent_old"},
			seed: func(model *Model) {
				model.inboxes["agent_old"] = []domain.InboxItem{{Message: domain.Message{ID: "message_old"}}}
			},
			check: func(model Model) bool { _, retained := model.inboxes["agent_old"]; return retained },
		},
		{
			name:  "navigation replacement releases timeline",
			frame: routeFrame{Route: RouteWork, EntityType: "task", EntityID: "task_old"},
			seed: func(model *Model) {
				model.entityTimelines[entityTimelineKey(workspaceID, "task", "task_old")] = entityTimeline{HighWater: 10}
			},
			check: func(model Model) bool {
				_, retained := model.entityTimelines[entityTimelineKey(workspaceID, "task", "task_old")]
				return retained
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := NewModel(Config{Color: ColorNever}, nil)
			model.snapshot.Workspace = m19TransportWorkspaceValue(workspaceID, "personal")
			model.routeStack = []routeFrame{test.frame}
			model.focus = FocusNavigation
			test.seed(&model)
			model.moveFocused(1)
			if test.check(model) {
				t.Fatalf("direct route replacement retained cache: frame=%#v inboxes=%#v timelines=%#v", test.frame, model.inboxes, model.entityTimelines)
			}
		})
	}
}

func TestM19SameScopeCanonicalRestartHidesPriorGenerationInbox(t *testing.T) {
	model := NewModel(Config{Color: ColorNever}, nil)
	model.connection = ConnectionLive
	model.loadInFlight = false
	model.snapshot.Workspace = m19TransportWorkspaceValue(m19TransportWorkspace, "personal")
	agentID := "agent_00000000000000000000000000000001"
	model.routeStack = []routeFrame{{Route: RouteCoordination}, {Route: RouteCoordination, EntityType: "agent_inbox", EntityID: agentID}}
	model.inboxes[agentID] = []domain.InboxItem{{
		Message:  domain.Message{ID: "message_old", Body: "prior-generation body"},
		Delivery: domain.MessageDelivery{MessageID: "message_old", RecipientAgentID: agentID},
	}}
	if records := model.recordsFor(RouteCoordination); len(records) != 1 {
		t.Fatalf("precondition inbox records = %#v", records)
	}
	command := model.restartCanonicalLoad(10)
	if command == nil || !model.loadInFlight || model.connection != ConnectionSyncing {
		t.Fatalf("same-scope restart state = command:%v load:%t connection:%v", command, model.loadInFlight, model.connection)
	}
	if records := model.recordsFor(RouteCoordination); len(records) != 0 || len(model.inboxes) != 0 {
		t.Fatalf("same-scope syncing rendered prior-generation inbox: records=%#v inboxes=%#v", records, model.inboxes)
	}
}

func TestM19AsyncRouteCachesRemainBoundedThroughAgentChurn(t *testing.T) {
	model := NewModel(Config{Color: ColorNever}, nil)
	model.snapshot.Workspace = m19TransportWorkspaceValue(m19TransportWorkspace, "personal")
	for index := 0; index < maxRouteDepth*4; index++ {
		agentID := fmt.Sprintf("agent_%032x", index+1)
		taskID := fmt.Sprintf("task_%032x", index+1)
		model.pushRoute(routeFrame{Route: RouteCoordination, EntityType: "agent_inbox", EntityID: agentID})
		model.inboxes[agentID] = []domain.InboxItem{{Message: domain.Message{ID: fmt.Sprintf("message_%d", index)}}}
		model.pushRoute(routeFrame{Route: RouteWork, EntityType: "task", EntityID: taskID})
		model.entityTimelines[entityTimelineKey(m19TransportWorkspace, "task", taskID)] = entityTimeline{HighWater: int64(index + 1)}
		if len(model.inboxes)+len(model.entityTimelines) > maxRouteDepth {
			t.Fatalf("async route cache grew beyond route ownership bound at iteration %d: inboxes=%d timelines=%d routes=%d",
				index, len(model.inboxes), len(model.entityTimelines), len(model.routeStack))
		}
	}
}

func TestM19DuplicateAsyncRouteOwnerSurvivesEscAndLateCompletion(t *testing.T) {
	t.Run("task timeline", func(t *testing.T) {
		const taskID = "task_11111111111111111111111111111111"
		model := NewModel(Config{Color: ColorNever}, nil)
		model.loadInFlight = false
		model.connection = ConnectionLive
		model.snapshot.Workspace = m19TransportWorkspaceValue(m19TransportWorkspace, "personal")
		model.snapshot.Tasks = []domain.TaskDetail{{Task: domain.Task{
			ID: taskID, WorkspaceID: m19TransportWorkspace, Title: "retained task", Status: domain.TaskReady, Revision: 1,
		}}}
		model.cursors = cursorState{Applied: 10, Candidate: 10, HighWater: 10}
		model.routeStack = []routeFrame{{Route: RouteWork}}
		model.selection[RouteWork] = taskID
		model.focus = FocusRecords

		if command := model.inspectSelected(); command == nil {
			t.Fatal("first task inspection did not start a timeline read")
		}
		firstEpoch := model.activeTimelineEpoch
		first := entityTimeline{HighWater: 10, Events: []domain.Event{{EventID: "evt_first"}}, Total: 1}
		updated, command := model.Update(timelineLoadedMsg{
			Generation: model.loadGeneration, Epoch: firstEpoch, WorkspaceID: m19TransportWorkspace,
			EntityType: "task", EntityID: taskID, Timeline: first,
		})
		if command != nil {
			t.Fatal("first exact-fence timeline unexpectedly scheduled more work")
		}
		model = updated.(Model)

		model.focus = FocusRecords
		if command := model.inspectSelected(); command == nil || len(model.routeStack) != 3 {
			t.Fatalf("duplicate task inspection = command:%v routes:%#v", command, model.routeStack)
		}
		secondEpoch := model.activeTimelineEpoch
		updated, command = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
		if command != nil {
			t.Fatal("Esc from duplicate task detail unexpectedly scheduled work")
		}
		model = updated.(Model)
		key := entityTimelineKey(m19TransportWorkspace, "task", taskID)
		if len(model.routeStack) != 2 || !reflect.DeepEqual(model.entityTimelines[key], first) {
			t.Fatalf("Esc did not reveal the prior task frame with its cache: routes=%#v timeline=%#v", model.routeStack, model.entityTimelines[key])
		}
		if strings.Contains(strings.ToLower(model.statusLine), "loading") {
			t.Fatalf("revealed cached task detail remained stuck in loading state: %q", model.statusLine)
		}

		late := entityTimeline{HighWater: 10, Events: []domain.Event{{EventID: "evt_late"}}, Total: 1}
		updated, command = model.Update(timelineLoadedMsg{
			Generation: model.loadGeneration, Epoch: secondEpoch, WorkspaceID: m19TransportWorkspace,
			EntityType: "task", EntityID: taskID, Timeline: late,
		})
		model = updated.(Model)
		if command != nil || !reflect.DeepEqual(model.entityTimelines[key], first) {
			t.Fatalf("late duplicate task completion clobbered retained cache: command=%v timeline=%#v", command, model.entityTimelines[key])
		}
	})

	t.Run("briefing claim explanation", func(t *testing.T) {
		claim := domain.BriefingClaim{
			ID: "bclaim_1", Kind: domain.BriefingClaimRisk, Urgency: "high", Status: "open", Summary: "retained claim",
			Sources:             []domain.BriefingClaimSource{{EntityType: "task", EntityID: "task_1", Revision: 1, EventSequence: 10}},
			SourceEventSequence: 10,
		}
		model := NewModel(Config{Color: ColorNever}, nil)
		model.loadInFlight = false
		model.connection = ConnectionLive
		model.snapshot.Workspace = m19TransportWorkspaceValue(m19TransportWorkspace, "personal")
		model.snapshot.Briefing = domain.ManagementBriefing{ID: "briefing_1", Claims: []domain.BriefingClaim{claim}}
		model.routeStack = []routeFrame{{Route: RouteBriefing}}
		model.selection[RouteBriefing] = claim.ID
		model.focus = FocusRecords

		if command := model.inspectSelected(); command == nil {
			t.Fatal("first claim inspection did not start an explanation read")
		}
		firstEpoch := model.activeBriefingExplainEpoch
		first := domain.BriefingClaimExplanation{
			BriefingID: model.snapshot.Briefing.ID, Claim: claim, EvaluatedAt: "2026-08-14T12:00:00Z",
			Provenance: append([]domain.BriefingClaimSource(nil), claim.Sources...), Diagnoses: []string{"first diagnosis"},
		}
		updated, command := model.Update(briefingExplainLoadedMsg{
			Generation: model.loadGeneration, Epoch: firstEpoch, WorkspaceID: m19TransportWorkspace,
			BriefingID: model.snapshot.Briefing.ID, ClaimID: claim.ID, Explanation: first,
		})
		if command != nil {
			t.Fatal("first exact explanation unexpectedly scheduled more work")
		}
		model = updated.(Model)

		model.focus = FocusRecords
		if command := model.inspectSelected(); command == nil || len(model.routeStack) != 3 {
			t.Fatalf("duplicate claim inspection = command:%v routes:%#v", command, model.routeStack)
		}
		secondEpoch := model.activeBriefingExplainEpoch
		updated, command = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
		if command != nil {
			t.Fatal("Esc from duplicate claim detail unexpectedly scheduled work")
		}
		model = updated.(Model)
		key := briefingExplanationKey(model.snapshot.Briefing.ID, claim.ID)
		if len(model.routeStack) != 2 || !reflect.DeepEqual(model.briefingExplanations[key], first) {
			t.Fatalf("Esc did not reveal the prior claim frame with its cache: routes=%#v explanation=%#v", model.routeStack, model.briefingExplanations[key])
		}
		if strings.Contains(strings.ToLower(model.statusLine), "loading") {
			t.Fatalf("revealed cached claim detail remained stuck in loading state: %q", model.statusLine)
		}

		late := cloneBriefingExplanation(first)
		late.Diagnoses = []string{"late diagnosis"}
		updated, command = model.Update(briefingExplainLoadedMsg{
			Generation: model.loadGeneration, Epoch: secondEpoch, WorkspaceID: m19TransportWorkspace,
			BriefingID: model.snapshot.Briefing.ID, ClaimID: claim.ID, Explanation: late,
		})
		model = updated.(Model)
		if command != nil || !reflect.DeepEqual(model.briefingExplanations[key], first) {
			t.Fatalf("late duplicate claim completion clobbered retained cache: command=%v explanation=%#v", command, model.briefingExplanations[key])
		}
	})
}

func TestM19DuplicateAsyncRouteWithoutCacheNeverStrandsRevealedFrame(t *testing.T) {
	t.Run("task timeline", func(t *testing.T) {
		const taskID = "task_22222222222222222222222222222222"
		model := NewModel(Config{Color: ColorNever}, nil)
		model.loadInFlight = false
		model.connection = ConnectionLive
		model.snapshot.Workspace = m19TransportWorkspaceValue(m19TransportWorkspace, "personal")
		model.snapshot.Tasks = []domain.TaskDetail{{Task: domain.Task{
			ID: taskID, WorkspaceID: m19TransportWorkspace, Title: "in-flight task", Status: domain.TaskReady, Revision: 1,
		}}}
		model.cursors = cursorState{Applied: 10, Candidate: 10, HighWater: 10}
		model.routeStack = []routeFrame{{Route: RouteWork}}
		model.selection[RouteWork] = taskID
		model.focus = FocusRecords

		if command := model.inspectSelected(); command == nil {
			t.Fatal("first task inspection did not start a timeline read")
		}
		model.focus = FocusRecords
		if command := model.inspectSelected(); command == nil {
			t.Fatal("second task inspection neither reused nor restarted the timeline read")
		}
		secondEpoch := model.activeTimelineEpoch
		duplicated := len(model.routeStack) == 3
		updated, escapeCommand := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
		model = updated.(Model)
		if duplicated {
			if escapeCommand == nil || model.activeTimelineEpoch == 0 || len(model.routeStack) != 2 ||
				model.currentFrame().EntityType != "task" || model.currentFrame().EntityID != taskID {
				t.Fatalf("Esc stranded uncached prior task detail: command=%v active=%d routes=%#v", escapeCommand, model.activeTimelineEpoch, model.routeStack)
			}
			reloadEpoch := model.activeTimelineEpoch
			updated, command := model.Update(timelineLoadedMsg{
				Generation: model.loadGeneration, Epoch: secondEpoch, WorkspaceID: m19TransportWorkspace,
				EntityType: "task", EntityID: taskID, Timeline: entityTimeline{HighWater: 10, Total: 1},
			})
			model = updated.(Model)
			if command != nil || model.activeTimelineEpoch != reloadEpoch || len(model.entityTimelines) != 0 {
				t.Fatalf("late abandoned task result displaced revealed-frame reload: command=%v active=%d caches=%#v", command, model.activeTimelineEpoch, model.entityTimelines)
			}
			updated, command = model.Update(timelineLoadedMsg{
				Generation: model.loadGeneration, Epoch: reloadEpoch, WorkspaceID: m19TransportWorkspace,
				EntityType: "task", EntityID: taskID, Timeline: entityTimeline{HighWater: 10, Total: 0},
			})
			model = updated.(Model)
			if command != nil || len(model.entityTimelines) != 1 || strings.Contains(strings.ToLower(model.statusLine), "loading") {
				t.Fatalf("revealed task reload did not finish: command=%v status=%q caches=%#v", command, model.statusLine, model.entityTimelines)
			}
			return
		}
		if len(model.routeStack) != 1 {
			t.Fatalf("duplicate-prevention path left unexpected task routes: %#v", model.routeStack)
		}
	})

	t.Run("briefing claim explanation", func(t *testing.T) {
		claim := domain.BriefingClaim{
			ID: "bclaim_2", Kind: domain.BriefingClaimRisk, Urgency: "high", Status: "open", Summary: "in-flight claim",
			Sources:             []domain.BriefingClaimSource{{EntityType: "task", EntityID: "task_2", Revision: 1, EventSequence: 10}},
			SourceEventSequence: 10,
		}
		model := NewModel(Config{Color: ColorNever}, nil)
		model.loadInFlight = false
		model.connection = ConnectionLive
		model.snapshot.Workspace = m19TransportWorkspaceValue(m19TransportWorkspace, "personal")
		model.snapshot.Briefing = domain.ManagementBriefing{ID: "briefing_2", Claims: []domain.BriefingClaim{claim}}
		model.routeStack = []routeFrame{{Route: RouteBriefing}}
		model.selection[RouteBriefing] = claim.ID
		model.focus = FocusRecords

		if command := model.inspectSelected(); command == nil {
			t.Fatal("first claim inspection did not start an explanation read")
		}
		model.focus = FocusRecords
		if command := model.inspectSelected(); command == nil {
			t.Fatal("second claim inspection neither reused nor restarted the explanation read")
		}
		secondEpoch := model.activeBriefingExplainEpoch
		duplicated := len(model.routeStack) == 3
		updated, escapeCommand := model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
		model = updated.(Model)
		if duplicated {
			if escapeCommand == nil || model.activeBriefingExplainEpoch == 0 || len(model.routeStack) != 2 ||
				model.currentFrame().EntityType != "briefing_claim" || model.currentFrame().EntityID != claim.ID {
				t.Fatalf("Esc stranded uncached prior claim detail: command=%v active=%d routes=%#v", escapeCommand, model.activeBriefingExplainEpoch, model.routeStack)
			}
			reloadEpoch := model.activeBriefingExplainEpoch
			explanation := domain.BriefingClaimExplanation{
				BriefingID: model.snapshot.Briefing.ID, Claim: claim, EvaluatedAt: "2026-08-14T12:00:00Z",
				Provenance: append([]domain.BriefingClaimSource(nil), claim.Sources...), Diagnoses: []string{"reloaded"},
			}
			updated, command := model.Update(briefingExplainLoadedMsg{
				Generation: model.loadGeneration, Epoch: secondEpoch, WorkspaceID: m19TransportWorkspace,
				BriefingID: model.snapshot.Briefing.ID, ClaimID: claim.ID, Explanation: explanation,
			})
			model = updated.(Model)
			if command != nil || model.activeBriefingExplainEpoch != reloadEpoch || len(model.briefingExplanations) != 0 {
				t.Fatalf("late abandoned claim result displaced revealed-frame reload: command=%v active=%d caches=%#v", command, model.activeBriefingExplainEpoch, model.briefingExplanations)
			}
			updated, command = model.Update(briefingExplainLoadedMsg{
				Generation: model.loadGeneration, Epoch: reloadEpoch, WorkspaceID: m19TransportWorkspace,
				BriefingID: model.snapshot.Briefing.ID, ClaimID: claim.ID, Explanation: explanation,
			})
			model = updated.(Model)
			if command != nil || len(model.briefingExplanations) != 1 || strings.Contains(strings.ToLower(model.statusLine), "loading") {
				t.Fatalf("revealed claim reload did not finish: command=%v status=%q caches=%#v", command, model.statusLine, model.briefingExplanations)
			}
			return
		}
		if len(model.routeStack) != 1 {
			t.Fatalf("duplicate-prevention path left unexpected claim routes: %#v", model.routeStack)
		}
	})
}
