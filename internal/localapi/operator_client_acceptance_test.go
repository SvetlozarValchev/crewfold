package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"crewfold/internal/domain"
)

func TestM19RunAttachStrictlyBindsTheRequestedCanonicalRun(t *testing.T) {
	t.Parallel()
	workspaceID := "ws_" + strings.Repeat("c", 32)
	runID := "run_" + strings.Repeat("a", 32)
	otherRunID := "run_" + strings.Repeat("b", 32)
	valid := RunAttachResult{
		Schema: RunAttachSchema, Type: "run_attach", RunID: runID,
		Runtime: "herdr", Executable: "/usr/bin/herdr", Arguments: []string{"terminal", "attach", "term-A"},
		Environment: map[string]string{"HERDR_SESSION": "crewfold"},
	}
	tests := []struct {
		name    string
		result  any
		wantErr string
	}{
		{name: "exact", result: valid},
		{name: "wrong run", result: func() RunAttachResult { result := valid; result.RunID = otherRunID; return result }(), wantErr: "unexpected result"},
		{name: "wrong discriminator", result: func() RunAttachResult { result := valid; result.Type = "run_detail"; return result }(), wantErr: "result discriminator"},
		{name: "unknown nested field", result: map[string]any{
			"schema": RunAttachSchema, "type": "run_attach", "run_id": runID, "runtime": "herdr",
			"executable": "/usr/bin/herdr", "arguments": []string{"terminal", "attach", "term-A"},
			"environment": map[string]any{"HERDR_SESSION": "crewfold"}, "opaque_handle": "must-not-pass",
		}, wantErr: "unknown field"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := capturePortableResultError(t, MethodRunAttach, func(client *Client) error {
				_, callErr := client.RunAttach(context.Background(), workspaceID, runID)
				return callErr
			}, test.result)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("exact attach result rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("attach error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestM19InboxListStrictlyBindsScopeBoundsAndShape(t *testing.T) {
	t.Parallel()
	const agentID = "agent_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const workspaceID = "ws_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	exact := InboxListResult{Schema: InboxListSchema, Type: "inbox", Agent: agentID, Items: []domain.InboxItem{}}
	tests := []struct {
		name    string
		limit   int
		result  any
		wantErr string
	}{
		{name: "exact", result: exact},
		{name: "wrong agent", result: func() InboxListResult {
			result := exact
			result.Agent = "agent_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			return result
		}(), wantErr: "scope or bound"},
		{name: "more than requested maximum", limit: 50, result: InboxListResult{
			Schema: InboxListSchema, Type: "inbox", Agent: agentID, Items: m19InboxItems(51, workspaceID, agentID),
		}, wantErr: "maxItems"},
		{name: "message from wrong workspace", result: InboxListResult{
			Schema: InboxListSchema, Type: "inbox", Agent: agentID,
			Items: m19InboxItems(1, "ws_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", agentID),
		}, wantErr: "message, recipient, or workspace scope"},
		{name: "delivery to wrong recipient", result: func() InboxListResult {
			items := m19InboxItems(1, workspaceID, agentID)
			items[0].Delivery.RecipientAgentID = "agent_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			return InboxListResult{Schema: InboxListSchema, Type: "inbox", Agent: agentID, Items: items}
		}(), wantErr: "message, recipient, or workspace scope"},
		{name: "delivery names a different message", result: func() InboxListResult {
			items := m19InboxItems(1, workspaceID, agentID)
			items[0].Delivery.MessageID = "msg_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			return InboxListResult{Schema: InboxListSchema, Type: "inbox", Agent: agentID, Items: items}
		}(), wantErr: "message, recipient, or workspace scope"},
		{name: "wrong schema", result: func() InboxListResult {
			result := exact
			result.Schema = "urn:crewfold:schema:local-api:inbox-list-result:v2"
			return result
		}(), wantErr: "result discriminator"},
		{name: "wrong type", result: func() InboxListResult { result := exact; result.Type = "inbox_compat"; return result }(), wantErr: "result discriminator"},
		{name: "unknown top field", result: map[string]any{
			"schema": InboxListSchema, "type": "inbox", "agent": agentID, "items": []any{}, "unexpected": true,
		}, wantErr: "unknown field"},
		{name: "unknown nested field", result: map[string]any{
			"schema": InboxListSchema, "type": "inbox", "agent": agentID,
			"items": []any{map[string]any{"message": map[string]any{}, "delivery": map[string]any{}, "unexpected": true}},
		}, wantErr: "unknown field"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := capturePortableResultError(t, MethodInboxList, func(client *Client) error {
				_, callErr := client.InboxList(context.Background(), workspaceID, agentID, test.limit)
				return callErr
			}, test.result)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("exact inbox result rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("InboxList(%s) error = %v, want substring %q", test.name, err, test.wantErr)
			}
		})
	}
}

func TestM19BriefingShowStrictlyBindsRequestedScope(t *testing.T) {
	t.Parallel()
	workspaceID := "ws_" + strings.Repeat("a", 32)
	otherWorkspaceID := "ws_" + strings.Repeat("b", 32)
	projectID := "prj_" + strings.Repeat("c", 32)
	otherProjectID := "prj_" + strings.Repeat("d", 32)
	tests := []struct {
		name   string
		params BriefingShowParams
		scope  domain.BriefingScope
		valid  bool
	}{
		{
			name: "exact workspace scope",
			params: BriefingShowParams{
				Workspace: workspaceID, ScopeType: domain.OwnerCheckpointWorkspace, ScopeIdentifier: workspaceID,
			},
			scope: domain.BriefingScope{Type: domain.OwnerCheckpointWorkspace, WorkspaceID: workspaceID},
			valid: true,
		},
		{
			name: "wrong workspace",
			params: BriefingShowParams{
				Workspace: workspaceID, ScopeType: domain.OwnerCheckpointWorkspace, ScopeIdentifier: workspaceID,
			},
			scope: domain.BriefingScope{Type: domain.OwnerCheckpointWorkspace, WorkspaceID: otherWorkspaceID},
		},
		{
			name: "exact project scope",
			params: BriefingShowParams{
				Workspace: workspaceID, ScopeType: domain.OwnerCheckpointProject, ScopeIdentifier: projectID,
			},
			scope: domain.BriefingScope{Type: domain.OwnerCheckpointProject, WorkspaceID: workspaceID, ProjectID: projectID},
			valid: true,
		},
		{
			name: "wrong project",
			params: BriefingShowParams{
				Workspace: workspaceID, ScopeType: domain.OwnerCheckpointProject, ScopeIdentifier: projectID,
			},
			scope: domain.BriefingScope{Type: domain.OwnerCheckpointProject, WorkspaceID: workspaceID, ProjectID: otherProjectID},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := BriefingShowResult{
				Schema: BriefingShowSchema, Type: "management_briefing",
				Briefing: m19OperatorBriefing(test.scope),
			}
			err := capturePortableResultError(t, MethodBriefingShow, func(client *Client) error {
				_, callErr := client.BriefingShow(context.Background(), test.params)
				return callErr
			}, result)
			if test.valid && err != nil {
				t.Fatalf("BriefingShow rejected exact scope: %v", err)
			}
			if !test.valid && err == nil {
				t.Fatalf("BriefingShow accepted mismatched scope %#v for request %#v", test.scope, test.params)
			}
		})
	}
}

func m19OperatorBriefing(scope domain.BriefingScope) domain.ManagementBriefing {
	return domain.ManagementBriefing{
		ID: "briefing_" + strings.Repeat("e", 32), Revision: 1, Scope: scope,
		EvaluatedAt: "2026-08-14T12:00:00Z", CaughtUp: true,
		Claims: []domain.BriefingClaim{}, Omitted: []domain.BriefingOmission{},
		ContentSHA256: strings.Repeat("f", 64), ByteSize: 1,
	}
}

func TestM19ApprovalListStrictlyBindsRequestedProject(t *testing.T) {
	t.Parallel()
	workspaceID := "ws_" + strings.Repeat("1", 32)
	projectID := "prj_" + strings.Repeat("2", 32)
	otherProjectID := "prj_" + strings.Repeat("3", 32)
	approval := domain.ApprovalRequest{
		ID: "appr_" + strings.Repeat("4", 32), WorkspaceID: workspaceID, ProjectID: projectID,
		ActionID: "saction_" + strings.Repeat("5", 32), Status: "pending",
		ExpectedActionRevision: 1, Revision: 1,
		CreatedAt: "2026-08-14T12:00:00Z", UpdatedAt: "2026-08-14T12:00:00Z",
		CreatedBy: "local-owner", UpdatedBy: "local-owner",
	}
	call := func(client *Client) error {
		_, err := client.ApprovalList(context.Background(), ApprovalListParams{
			Workspace: workspaceID, Project: projectID,
		})
		return err
	}
	page := ApprovalListResult{
		Schema: ApprovalListSchema, Type: "approval_list", Approvals: []domain.ApprovalRequest{approval},
		PageResult: PageResult{Total: 1},
	}
	if err := capturePortableResultError(t, MethodApprovalList, call, page); err != nil {
		t.Fatalf("approval.list rejected exact project-bound result: %v", err)
	}
	page.Approvals[0].ProjectID = otherProjectID
	if err := capturePortableResultError(t, MethodApprovalList, call, page); err == nil {
		t.Fatalf("approval.list accepted approval from project %q for requested project %q", otherProjectID, projectID)
	}
}

func m19InboxItems(count int, workspaceID, agentID string) []domain.InboxItem {
	items := make([]domain.InboxItem, count)
	for index := range items {
		messageID := fmt.Sprintf("msg_%032x", index+1)
		items[index] = domain.InboxItem{
			Message: domain.Message{
				ID: messageID, WorkspaceID: workspaceID, ThreadID: "thread_" + strings.Repeat("c", 32),
				SenderType: "owner", SenderID: "local-owner", Kind: "inform", Body: "bounded inbox item",
				ArtifactIDs: []string{}, CreatedAt: "2026-08-14T12:00:00Z",
			},
			Delivery: domain.MessageDelivery{
				MessageID: messageID, RecipientAgentID: agentID, RecipientName: "operator-agent",
				Status: "queued", QueuedAt: "2026-08-14T12:00:00Z", WakeStatus: "not_requested",
			},
		}
	}
	return items
}

func TestM19WorkspaceShowStrictlyBindsRequestedIdentity(t *testing.T) {
	t.Parallel()
	workspace := m19OperatorWorkspace("a", "personal")
	other := m19OperatorWorkspace("b", "other")
	exact := WorkspaceShowResult{Schema: WorkspaceShowSchema, Type: "workspace", Workspace: workspace}
	tests := []struct {
		name       string
		identifier string
		result     any
		wantErr    bool
	}{
		{name: "exact canonical id", identifier: workspace.ID, result: exact},
		{name: "exact name", identifier: workspace.Name, result: exact},
		{name: "wrong canonical id", identifier: workspace.ID, result: WorkspaceShowResult{Schema: WorkspaceShowSchema, Type: "workspace", Workspace: other}, wantErr: true},
		{name: "wrong name", identifier: workspace.Name, result: WorkspaceShowResult{Schema: WorkspaceShowSchema, Type: "workspace", Workspace: other}, wantErr: true},
		{name: "wrong schema", identifier: workspace.ID, result: func() WorkspaceShowResult {
			result := exact
			result.Schema = "urn:crewfold:schema:local-api:workspace-show-result:v2"
			return result
		}(), wantErr: true},
		{name: "wrong type", identifier: workspace.ID, result: func() WorkspaceShowResult { result := exact; result.Type = "workspace_compat"; return result }(), wantErr: true},
		{name: "unknown top field", identifier: workspace.ID, result: map[string]any{
			"schema": WorkspaceShowSchema, "type": "workspace", "workspace": workspace, "unexpected": true,
		}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := capturePortableResultError(t, MethodWorkspaceShow, func(client *Client) error {
				_, callErr := client.WorkspaceShow(context.Background(), test.identifier)
				return callErr
			}, test.result)
			if test.wantErr && err == nil {
				t.Fatalf("WorkspaceShow(%s) accepted mismatched result %#v", test.name, test.result)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("WorkspaceShow(%s) rejected exact result: %v", test.name, err)
			}
		})
	}
}

func TestM19ProjectShowStrictlyBindsRequestedScopeAndIdentity(t *testing.T) {
	t.Parallel()
	workspace := m19OperatorWorkspace("c", "personal")
	project := m19OperatorProject("d", workspace.ID, "engine")
	otherProject := m19OperatorProject("e", workspace.ID, "other")
	exact := ProjectShowResult{Schema: ProjectShowSchema, Type: "project", Project: project}
	tests := []struct {
		name      string
		workspace string
		project   string
		result    any
		wantErr   bool
	}{
		{name: "exact canonical ids", workspace: workspace.ID, project: project.ID, result: exact},
		{name: "exact project name", workspace: workspace.ID, project: project.Name, result: exact},
		{name: "wrong workspace", workspace: workspace.ID, project: project.ID, result: func() ProjectShowResult {
			result := exact
			result.Project.WorkspaceID = "ws_" + strings.Repeat("f", 32)
			return result
		}(), wantErr: true},
		{name: "wrong canonical project id", workspace: workspace.ID, project: project.ID, result: ProjectShowResult{Schema: ProjectShowSchema, Type: "project", Project: otherProject}, wantErr: true},
		{name: "wrong project name", workspace: workspace.ID, project: project.Name, result: ProjectShowResult{Schema: ProjectShowSchema, Type: "project", Project: otherProject}, wantErr: true},
		{name: "wrong schema", workspace: workspace.ID, project: project.ID, result: func() ProjectShowResult {
			result := exact
			result.Schema = "urn:crewfold:schema:local-api:project-show-result:v2"
			return result
		}(), wantErr: true},
		{name: "wrong type", workspace: workspace.ID, project: project.ID, result: func() ProjectShowResult { result := exact; result.Type = "project_compat"; return result }(), wantErr: true},
		{name: "unknown top field", workspace: workspace.ID, project: project.ID, result: map[string]any{
			"schema": ProjectShowSchema, "type": "project", "project": project, "unexpected": true,
		}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := capturePortableResultError(t, MethodProjectShow, func(client *Client) error {
				_, callErr := client.ProjectShow(context.Background(), test.workspace, test.project)
				return callErr
			}, test.result)
			if test.wantErr && err == nil {
				t.Fatalf("ProjectShow(%s) accepted mismatched result %#v", test.name, test.result)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("ProjectShow(%s) rejected exact result: %v", test.name, err)
			}
		})
	}
}

func m19OperatorWorkspace(hexDigit, name string) domain.Workspace {
	return domain.Workspace{
		ID: "ws_" + strings.Repeat(hexDigit, 32), Name: name, Revision: 1,
		CreatedAt: "2026-08-14T12:00:00Z", UpdatedAt: "2026-08-14T12:00:00Z",
		CreatedBy: "local-owner", UpdatedBy: "local-owner",
	}
}

func m19OperatorProject(hexDigit, workspaceID, name string) domain.Project {
	return domain.Project{
		ID: "prj_" + strings.Repeat(hexDigit, 32), WorkspaceID: workspaceID, Name: name, Revision: 1,
		CreatedAt: "2026-08-14T12:00:00Z", UpdatedAt: "2026-08-14T12:00:00Z",
		CreatedBy: "local-owner", UpdatedBy: "local-owner",
	}
}

func TestM19LocalClientRejectsResponseBeyondSixteenMiB(t *testing.T) {
	t.Parallel()
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var request Request
		if err := json.NewDecoder(server).Decode(&request); err != nil {
			return
		}
		_, _ = server.Write([]byte(strings.Repeat("x", maximumResponseBytes+1)))
	}()

	err := roundTrip(client, Request{ID: "m19-oversize", Protocol: MinProtocol, Method: MethodEventsList}, &EventsListResult{})
	if err == nil || !strings.Contains(err.Error(), "response exceeds 16777216 bytes") {
		t.Fatalf("roundTrip(oversize) error = %v, want explicit 16 MiB rejection", err)
	}
	<-done
}

func TestM19OperatorClientRejectsImpossibleOrWrongScopeEventPages(t *testing.T) {
	t.Parallel()
	expectedWorkspace := "ws_" + strings.Repeat("a", 32)
	otherWorkspace := "ws_" + strings.Repeat("b", 32)
	duplicateID := m19CanonicalEventID(11)
	first := m19CanonicalEvent(11, duplicateID, expectedWorkspace)
	second := m19CanonicalEvent(12, duplicateID, expectedWorkspace)
	tests := []struct {
		name   string
		params EventsListParams
		result EventsListResult
	}{
		{
			name:   "wrong workspace",
			params: EventsListParams{Workspace: expectedWorkspace, After: 10},
			result: EventsListResult{WorkspaceID: expectedWorkspace, HighWater: 11, Events: []domain.Event{m19CanonicalEvent(11, m19CanonicalEventID(11), otherWorkspace)}, PageResult: PageResult{Total: 1}},
		},
		{
			name:   "duplicate event identity",
			params: EventsListParams{Workspace: expectedWorkspace, After: 10},
			result: EventsListResult{WorkspaceID: expectedWorkspace, HighWater: 12, Events: []domain.Event{first, second}, PageResult: PageResult{Total: 2}},
		},
		{
			name:   "positive total with empty terminal page",
			params: EventsListParams{Workspace: expectedWorkspace, After: 10},
			result: EventsListResult{WorkspaceID: expectedWorkspace, HighWater: 12, PageResult: PageResult{Total: 1}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateForwardEvents(test.params, test.result); err == nil {
				t.Fatalf("validateForwardEvents(%s) accepted impossible page %#v", test.name, test.result)
			}
		})
	}

	invalidActor := m19CanonicalEvent(11, m19CanonicalEventID(11), expectedWorkspace)
	invalidActor.Actor.ActorType = "agent"
	if err := validateForwardEvents(EventsListParams{Workspace: expectedWorkspace, After: 10}, EventsListResult{
		WorkspaceID: expectedWorkspace, HighWater: 11, Events: []domain.Event{invalidActor}, PageResult: PageResult{Total: 1},
	}); err == nil {
		t.Fatal("validateForwardEvents accepted an actor type outside the published event union")
	}

	timeline := EventsTimelineResult{
		WorkspaceID: expectedWorkspace,
		HighWater:   11,
		Events:      []domain.Event{m19CanonicalEvent(11, m19CanonicalEventID(11), otherWorkspace)},
		PageResult: PageResult{
			Total: 1,
		},
	}
	if err := validateReverseTimeline(EventsTimelineParams{
		Workspace: expectedWorkspace, EntityType: "task", EntityID: "task_1",
	}, timeline); err == nil {
		t.Fatalf("validateReverseTimeline accepted wrong-workspace event %#v", timeline.Events[0])
	}
}

func TestM19OperatorClientRejectsCanonicalEventSchemaViolations(t *testing.T) {
	t.Parallel()
	workspaceID := "ws_" + strings.Repeat("c", 32)
	valid := m19CanonicalEvent(11, m19CanonicalEventID(11), workspaceID)
	tests := []struct {
		name   string
		mutate func(*domain.Event)
	}{
		{name: "zero sequence", mutate: func(event *domain.Event) { event.Sequence = 0 }},
		{name: "noncanonical event id", mutate: func(event *domain.Event) { event.EventID = "evt_11" }},
		{name: "empty type", mutate: func(event *domain.Event) { event.Type = "" }},
		{name: "zero schema version", mutate: func(event *domain.Event) { event.SchemaVersion = 0 }},
		{name: "noncanonical workspace id", mutate: func(event *domain.Event) { event.WorkspaceID = "workspace-not-canonical" }},
		{name: "invalid occurred timestamp", mutate: func(event *domain.Event) { event.OccurredAt = "not-a-date-time" }},
		{name: "invalid recorded timestamp", mutate: func(event *domain.Event) { event.RecordedAt = "2026-99-99T99:99:99Z" }},
		{name: "empty actor id", mutate: func(event *domain.Event) { event.Actor.ActorID = "" }},
		{name: "unknown actor type", mutate: func(event *domain.Event) { event.Actor.ActorType = "agent" }},
		{name: "empty entity type", mutate: func(event *domain.Event) { event.Entity.Type = "" }},
		{name: "empty entity id", mutate: func(event *domain.Event) { event.Entity.ID = "" }},
		{name: "zero entity revision", mutate: func(event *domain.Event) { event.Entity.Revision = 0 }},
		{name: "empty correlation id", mutate: func(event *domain.Event) { event.CorrelationID = "" }},
		{name: "overlong correlation id", mutate: func(event *domain.Event) { event.CorrelationID = strings.Repeat("c", 129) }},
		{name: "overlong causation id", mutate: func(event *domain.Event) { event.CausationID = strings.Repeat("c", 129) }},
		{name: "empty data", mutate: func(event *domain.Event) { event.Data = nil }},
		{name: "invalid data", mutate: func(event *domain.Event) { event.Data = json.RawMessage(`{`) }},
		{name: "array data", mutate: func(event *domain.Event) { event.Data = json.RawMessage(`[]`) }},
		{name: "scalar data", mutate: func(event *domain.Event) { event.Data = json.RawMessage(`"scalar"`) }},
		{name: "null data", mutate: func(event *domain.Event) { event.Data = json.RawMessage(`null`) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := valid
			test.mutate(&event)
			result := EventsListResult{
				WorkspaceID: event.WorkspaceID,
				HighWater:   event.Sequence,
				Events:      []domain.Event{event},
				PageResult:  PageResult{Total: 1},
			}
			if result.HighWater < 11 {
				result.HighWater = 11
			}
			if err := validateForwardEvents(EventsListParams{Workspace: workspaceID, After: 10}, result); err == nil {
				t.Fatalf("validateForwardEvents accepted %s: %#v", test.name, event)
			}
		})
	}
}

func m19CanonicalEvent(sequence int64, eventID, workspaceID string) domain.Event {
	return domain.Event{
		EventID: eventID, Sequence: sequence, Type: "task.updated", SchemaVersion: 1,
		OccurredAt: "2026-08-14T12:00:00Z", RecordedAt: "2026-08-14T12:00:00Z",
		Actor:         domain.EventActor{ActorID: "local-owner", ActorType: domain.EventActorHuman},
		WorkspaceID:   workspaceID,
		Entity:        domain.EventEntity{Type: "task", ID: "task_1", Revision: sequence},
		CorrelationID: "corr_1",
		Data:          json.RawMessage(`{"status":"ready"}`),
	}
}

func m19CanonicalEventID(sequence int64) string {
	return fmt.Sprintf("evt_%032x", sequence)
}

func TestM19OperatorResultDecodingRejectsUnknownFields(t *testing.T) {
	t.Parallel()
	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		var request Request
		if err := json.NewDecoder(server).Decode(&request); err != nil {
			return
		}
		response := `{"id":"m19-strict","protocol":1,"result":{"schema":"` + EventsListSchema + `","type":"event_list","workspace_id":"ws_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","high_water":0,"events":[],"next_cursor":"","has_more":false,"total":0,"compat_events":[]}}` + "\n"
		_, _ = server.Write([]byte(response))
	}()

	err := roundTripStrict(client, Request{ID: "m19-strict", Protocol: MinProtocol, Method: MethodEventsList}, &EventsListResult{})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("roundTripStrict(unknown field) error = %v, want fail-closed schema rejection", err)
	}
	<-done
}

func TestM19EventClientsRejectExplicitEmptyCausationID(t *testing.T) {
	t.Parallel()
	workspaceID := "ws_" + strings.Repeat("d", 32)
	event := m19CanonicalEventWire(1, workspaceID)
	event["causation_id"] = ""
	tests := []struct {
		name   string
		method string
		call   func(*Client) error
		result map[string]any
	}{
		{
			name: "forward", method: MethodEventsList,
			call: func(client *Client) error {
				_, err := client.EventsList(context.Background(), EventsListParams{Workspace: workspaceID})
				return err
			},
			result: m19EventPageWire(EventsListSchema, "event_list", workspaceID, 1, []any{event}),
		},
		{
			name: "timeline", method: MethodEventsTimeline,
			call: func(client *Client) error {
				_, err := client.EventsTimeline(context.Background(), EventsTimelineParams{Workspace: workspaceID, EntityType: "task", EntityID: "task_1"})
				return err
			},
			result: m19EventPageWire(EventsTimelineSchema, "event_timeline", workspaceID, 1, []any{event}),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := capturePortableResultError(t, test.method, test.call, test.result)
			if err == nil || !strings.Contains(err.Error(), "causation_id") {
				t.Fatalf("%s accepted explicitly empty causation_id: %v", test.method, err)
			}
		})
	}
}

func TestM19EventClientsRejectUnknownCanonicalTypeWithDefinitiveCode(t *testing.T) {
	t.Parallel()
	workspaceID := "ws_" + strings.Repeat("d", 32)
	for _, test := range []struct {
		name   string
		method string
		kind   string
		call   func(*Client) error
	}{
		{
			name: "forward", method: MethodEventsList, kind: "event_list",
			call: func(client *Client) error {
				_, err := client.EventsList(context.Background(), EventsListParams{Workspace: workspaceID})
				return err
			},
		},
		{
			name: "timeline", method: MethodEventsTimeline, kind: "event_timeline",
			call: func(client *Client) error {
				_, err := client.EventsTimeline(context.Background(), EventsTimelineParams{
					Workspace: workspaceID, EntityType: "task", EntityID: "task_1",
				})
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			event := m19CanonicalEventWire(1, workspaceID)
			event["type"] = "future.fact"
			result := m19EventPageWire(testSchemaForEventMethod(test.method), test.kind, workspaceID, 1, []any{event})
			err := capturePortableResultError(t, test.method, test.call, result)
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.Code != "unsupported_operator_event" || apiErr.Retryable {
				t.Fatalf("%s unknown event error = %v, want definitive unsupported_operator_event", test.method, err)
			}
		})
	}
}

func testSchemaForEventMethod(method string) string {
	if method == MethodEventsTimeline {
		return EventsTimelineSchema
	}
	return EventsListSchema
}

func TestM19EventClientsRejectOmittedOrNullRequiredResultMembers(t *testing.T) {
	t.Parallel()
	workspaceID := "ws_" + strings.Repeat("e", 32)
	tests := []struct {
		name   string
		method string
		call   func(*Client) error
		result map[string]any
	}{
		{
			name: "forward", method: MethodEventsList,
			call: func(client *Client) error {
				_, err := client.EventsList(context.Background(), EventsListParams{Workspace: workspaceID})
				return err
			},
			result: m19EventPageWire(EventsListSchema, "event_list", workspaceID, 0, []any{}),
		},
		{
			name: "timeline", method: MethodEventsTimeline,
			call: func(client *Client) error {
				_, err := client.EventsTimeline(context.Background(), EventsTimelineParams{Workspace: workspaceID, EntityType: "task", EntityID: "task_1"})
				return err
			},
			result: m19EventPageWire(EventsTimelineSchema, "event_timeline", workspaceID, 0, []any{}),
		},
	}
	required := []string{"schema", "type", "workspace_id", "high_water", "events", "next_cursor", "has_more", "total"}
	for _, test := range tests {
		t.Run(test.name+"/exact", func(t *testing.T) {
			if err := capturePortableResultError(t, test.method, test.call, test.result); err != nil {
				t.Fatalf("exact %s result rejected: %v", test.method, err)
			}
		})
		for _, field := range required {
			field := field
			t.Run(test.name+"/omitted-"+field, func(t *testing.T) {
				result := m19CloneWireObject(test.result)
				delete(result, field)
				if err := capturePortableResultError(t, test.method, test.call, result); err == nil {
					t.Fatalf("%s accepted omitted required field %q", test.method, field)
				}
			})
			t.Run(test.name+"/null-"+field, func(t *testing.T) {
				result := m19CloneWireObject(test.result)
				result[field] = nil
				if err := capturePortableResultError(t, test.method, test.call, result); err == nil {
					t.Fatalf("%s accepted null required field %q", test.method, field)
				}
			})
		}
	}
}

func TestM19OrdinaryOperatorPagesRejectOmittedOrNullCollections(t *testing.T) {
	t.Parallel()
	workspaceID := "ws_" + strings.Repeat("f", 32)
	tests := []struct {
		name       string
		method     string
		schema     string
		kind       string
		collection string
		call       func(*Client) error
	}{
		{name: "workspaces", method: MethodWorkspaceList, schema: WorkspaceListSchema, kind: "workspace_list", collection: "workspaces", call: func(client *Client) error {
			_, err := client.WorkspaceList(context.Background(), WorkspaceListParams{})
			return err
		}},
		{name: "projects", method: MethodProjectList, schema: ProjectListSchema, kind: "project_list", collection: "projects", call: func(client *Client) error {
			_, err := client.ProjectList(context.Background(), ProjectListParams{Workspace: workspaceID})
			return err
		}},
		{name: "agents", method: MethodAgentList, schema: AgentListSchema, kind: "agent_list", collection: "agents", call: func(client *Client) error {
			_, err := client.AgentList(context.Background(), AgentListParams{Workspace: workspaceID})
			return err
		}},
		{name: "objectives", method: MethodObjectiveList, schema: ObjectiveListSchema, kind: "objective_list", collection: "objectives", call: func(client *Client) error {
			_, err := client.ObjectiveList(context.Background(), ObjectiveListParams{Workspace: workspaceID})
			return err
		}},
		{name: "tasks", method: MethodTaskList, schema: TaskListSchema, kind: "task_list", collection: "tasks", call: func(client *Client) error {
			_, err := client.TaskList(context.Background(), TaskListParams{Workspace: workspaceID})
			return err
		}},
		{name: "runs", method: MethodRunList, schema: RunListSchema, kind: "run_list", collection: "runs", call: func(client *Client) error {
			_, err := client.RunList(context.Background(), RunListParams{Workspace: workspaceID})
			return err
		}},
		{name: "claims", method: MethodClaimList, schema: ClaimListSchema, kind: "claim_list", collection: "claims", call: func(client *Client) error {
			_, err := client.ClaimList(context.Background(), ClaimListParams{Workspace: workspaceID})
			return err
		}},
		{name: "overlaps", method: MethodOverlapList, schema: OverlapListSchema, kind: "overlap_list", collection: "overlaps", call: func(client *Client) error {
			_, err := client.OverlapList(context.Background(), OverlapListParams{Workspace: workspaceID})
			return err
		}},
		{name: "drifts", method: MethodDriftList, schema: DriftListSchema, kind: "drift_list", collection: "drifts", call: func(client *Client) error {
			_, err := client.DriftList(context.Background(), DriftListParams{Workspace: workspaceID})
			return err
		}},
		{name: "meetings", method: MethodMeetingList, schema: MeetingListSchema, kind: "meeting_list", collection: "meetings", call: func(client *Client) error {
			_, err := client.MeetingList(context.Background(), MeetingListParams{Workspace: workspaceID})
			return err
		}},
		{name: "approvals", method: MethodApprovalList, schema: ApprovalListSchema, kind: "approval_list", collection: "approvals", call: func(client *Client) error {
			_, err := client.ApprovalList(context.Background(), ApprovalListParams{Workspace: workspaceID})
			return err
		}},
		{name: "checks", method: MethodCheckList, schema: CheckRunListSchema, kind: "check_run_list", collection: "runs", call: func(client *Client) error {
			_, err := client.CheckList(context.Background(), CheckListParams{Workspace: workspaceID})
			return err
		}},
	}
	for _, test := range tests {
		exact := map[string]any{
			"schema": test.schema, "type": test.kind, test.collection: []any{},
			"next_cursor": "", "has_more": false, "total": int64(0),
		}
		t.Run(test.name+"/exact", func(t *testing.T) {
			if err := capturePortableResultError(t, test.method, test.call, exact); err != nil {
				t.Fatalf("exact %s page rejected: %v", test.method, err)
			}
		})
		t.Run(test.name+"/omitted-collection", func(t *testing.T) {
			result := m19CloneWireObject(exact)
			delete(result, test.collection)
			if err := capturePortableResultError(t, test.method, test.call, result); err == nil {
				t.Fatalf("%s accepted omitted %q collection", test.method, test.collection)
			}
		})
		t.Run(test.name+"/null-collection", func(t *testing.T) {
			result := m19CloneWireObject(exact)
			result[test.collection] = nil
			if err := capturePortableResultError(t, test.method, test.call, result); err == nil {
				t.Fatalf("%s accepted null %q collection", test.method, test.collection)
			}
		})
	}
}

func TestM19OrdinaryOperatorPageRejectsOmittedOrNullPaginationMembers(t *testing.T) {
	t.Parallel()
	exact := map[string]any{
		"schema": WorkspaceListSchema, "type": "workspace_list", "workspaces": []any{},
		"next_cursor": "", "has_more": false, "total": int64(0),
	}
	call := func(client *Client) error {
		_, err := client.WorkspaceList(context.Background(), WorkspaceListParams{})
		return err
	}
	for _, field := range []string{"next_cursor", "has_more", "total"} {
		field := field
		t.Run("omitted-"+field, func(t *testing.T) {
			result := m19CloneWireObject(exact)
			delete(result, field)
			if err := capturePortableResultError(t, MethodWorkspaceList, call, result); err == nil {
				t.Fatalf("workspace.list accepted omitted required pagination field %q", field)
			}
		})
		t.Run("null-"+field, func(t *testing.T) {
			result := m19CloneWireObject(exact)
			result[field] = nil
			if err := capturePortableResultError(t, MethodWorkspaceList, call, result); err == nil {
				t.Fatalf("workspace.list accepted null required pagination field %q", field)
			}
		})
	}
}

func TestM19RunListRejectsMalformedOrWrongScopeNestedSummary(t *testing.T) {
	t.Parallel()
	workspaceID := "ws_" + strings.Repeat("1", 32)
	exactRun := map[string]any{
		"id": "run_" + strings.Repeat("2", 32), "workspace_id": workspaceID,
		"project_id": "prj_" + strings.Repeat("3", 32), "task_id": "task_" + strings.Repeat("4", 32),
		"agent_id": "agent_" + strings.Repeat("5", 32), "runtime": "herdr", "provider": "codex",
		"status": "blocked", "can_attach": true, "blocked_question": "Which loop?", "revision": int64(1),
		"created_at": "2026-08-14T12:00:00Z", "updated_at": "2026-08-14T12:00:01Z",
	}
	exactPage := map[string]any{
		"schema": RunListSchema, "type": "run_list", "runs": []any{exactRun},
		"next_cursor": "", "has_more": false, "total": int64(1),
	}
	call := func(client *Client) error {
		_, err := client.RunList(context.Background(), RunListParams{Workspace: workspaceID})
		return err
	}
	if err := capturePortableResultError(t, MethodRunList, call, exactPage); err != nil {
		t.Fatalf("exact run.list nested summary rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "omitted revision", mutate: func(run map[string]any) { delete(run, "revision") }},
		{name: "null updated timestamp", mutate: func(run map[string]any) { run["updated_at"] = nil }},
		{name: "wrong workspace", mutate: func(run map[string]any) { run["workspace_id"] = "ws_" + strings.Repeat("9", 32) }},
		{name: "unknown status", mutate: func(run map[string]any) { run["status"] = "compat_blocked" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := m19CloneWireObject(exactRun)
			test.mutate(run)
			page := m19CloneWireObject(exactPage)
			page["runs"] = []any{run}
			if err := capturePortableResultError(t, MethodRunList, call, page); err == nil {
				t.Fatalf("run.list accepted malformed nested summary for %s", test.name)
			}
		})
	}
}

func TestM19OperatorDetailClientsRejectOmittedOrNullRequiredPayload(t *testing.T) {
	t.Parallel()
	workspaceID := "ws_" + strings.Repeat("6", 32)
	tests := []struct {
		name    string
		method  string
		schema  string
		kind    string
		payload string
		call    func(*Client) error
	}{
		{name: "briefing show", method: MethodBriefingShow, schema: BriefingShowSchema, kind: "management_briefing", payload: "briefing", call: func(client *Client) error {
			_, err := client.BriefingShow(context.Background(), BriefingShowParams{Workspace: workspaceID, ScopeType: "workspace", ScopeIdentifier: workspaceID})
			return err
		}},
		{name: "briefing explain", method: MethodBriefingExplain, schema: BriefingExplainSchema, kind: "briefing_claim_explanation", payload: "explanation", call: func(client *Client) error {
			_, err := client.BriefingExplain(context.Background(), BriefingExplainParams{Workspace: workspaceID, Briefing: "briefing_1", Claim: "briefing_claim_1"})
			return err
		}},
		{name: "supervisor action show", method: MethodSupervisorActionShow, schema: SupervisorActionShowSchema, kind: "supervisor_action", payload: "action", call: func(client *Client) error {
			_, err := client.SupervisorActionShow(context.Background(), workspaceID, "supact_1")
			return err
		}},
	}
	for _, test := range tests {
		for _, variant := range []string{"omitted", "null"} {
			variant := variant
			t.Run(test.name+"/"+variant, func(t *testing.T) {
				result := map[string]any{"schema": test.schema, "type": test.kind}
				if variant == "null" {
					result[test.payload] = nil
				}
				if err := capturePortableResultError(t, test.method, test.call, result); err == nil {
					t.Fatalf("%s accepted %s required payload %q", test.method, variant, test.payload)
				}
			})
		}
	}
}

func m19EventPageWire(schema, kind, workspaceID string, highWater int64, events []any) map[string]any {
	return map[string]any{
		"schema": schema, "type": kind, "workspace_id": workspaceID, "high_water": highWater,
		"events": events, "next_cursor": "", "has_more": false, "total": int64(len(events)),
	}
}

func m19CanonicalEventWire(sequence int64, workspaceID string) map[string]any {
	return map[string]any{
		"event_id": m19CanonicalEventID(sequence), "sequence": sequence, "type": "task.updated", "schema_version": 1,
		"occurred_at": "2026-08-14T12:00:00Z", "recorded_at": "2026-08-14T12:00:00Z",
		"actor":          map[string]any{"actor_id": "local-owner", "actor_type": domain.EventActorHuman},
		"workspace_id":   workspaceID,
		"entity":         map[string]any{"type": "task", "id": "task_1", "revision": sequence},
		"correlation_id": "corr_1", "data": map[string]any{"status": "ready"},
	}
}

func m19CloneWireObject(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}
