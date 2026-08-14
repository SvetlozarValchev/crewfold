package tui

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestM19ExactLayoutBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		width, height int
		want          layoutMode
	}{
		{name: "narrow below minimum", width: 59, height: 18, want: layoutTooSmall},
		{name: "short below minimum", width: 60, height: 17, want: layoutTooSmall},
		{name: "minimum", width: 60, height: 18, want: layoutOnePane},
		{name: "largest one pane", width: 79, height: 23, want: layoutOnePane},
		{name: "minimum two pane", width: 80, height: 24, want: layoutTwoPane},
		{name: "largest two pane", width: 119, height: 31, want: layoutTwoPane},
		{name: "minimum three pane", width: 120, height: 32, want: layoutThreePane},
		{name: "large", width: 240, height: 80, want: layoutThreePane},
		{name: "wide but short is one pane", width: 240, height: 23, want: layoutOnePane},
		{name: "tall but narrow is one pane", width: 79, height: 80, want: layoutOnePane},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := NewModel(Config{}, nil)
			model.width, model.height = test.width, test.height
			if got := model.layout(); got != test.want {
				t.Fatalf("layout(%dx%d) = %d, want %d", test.width, test.height, got, test.want)
			}
		})
	}
}

func TestM19TooSmallStateCannotHideOrSubmitAnActionReview(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{Color: ColorNever}, nil)
	model.width, model.height = 59, 18
	model.connection = ConnectionLive
	model.snapshot.Workspace = domain.Workspace{ID: "ws_1", Name: "personal"}
	model.modal = modalState{Kind: modalReview, Review: actionReview{
		Choice: actionChoice{
			Kind: actionStopRun, TargetType: "run", TargetID: "run_1", Revision: 4,
			Consequence: "Request a bounded graceful stop of this run.",
		},
		IdempotencyKey: "ui-fixed-key",
	}}

	view := model.render()
	if !strings.Contains(view, "Terminal too small for Crewfold UI") || strings.Contains(view, "Review action") ||
		strings.Contains(view, "Ctrl+Enter") || strings.Contains(strings.ToLower(view), "confirm") || strings.Contains(strings.ToLower(view), "choose") {
		t.Fatalf("too-small review rendered an unsafe confirmation surface:\n%s", view)
	}
	updated, command := model.updateModalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl}), "ctrl+enter")
	if command != nil {
		t.Fatal("Ctrl+Enter submitted an action while the review was below the minimum safe size")
	}
	result, ok := updated.(Model)
	if !ok {
		t.Fatalf("updateModalKey returned %T, want Model", updated)
	}
	if result.modal.Kind != modalReview || result.modal.Review.Executing {
		t.Fatalf("too-small confirmation did not preserve the unsubmitted review: %#v", result.modal)
	}
}

func TestM19ModalFocusIsExplicitAndRestoresPriorFocus(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{}, nil)
	model.focus = FocusDetail
	opened, command := model.updateKey(tea.KeyPressMsg(tea.Key{Code: '?', Text: "?"}))
	if command != nil {
		t.Fatal("opening static help unexpectedly started a command")
	}
	openModel, ok := opened.(Model)
	if !ok {
		t.Fatalf("updateKey returned %T, want Model", opened)
	}
	if openModel.modal.Kind != modalHelp || openModel.focus != FocusModal {
		t.Fatalf("help modal state = kind %v focus %v, want help/modal", openModel.modal.Kind, openModel.focus)
	}
	closed, command := openModel.updateKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	if command != nil {
		t.Fatal("closing static help unexpectedly started a command")
	}
	closedModel, ok := closed.(Model)
	if !ok {
		t.Fatalf("updateKey returned %T, want Model", closed)
	}
	if closedModel.modal.Kind != modalNone || closedModel.focus != FocusDetail {
		t.Fatalf("closed help state = kind %v focus %v, want none/detail", closedModel.modal.Kind, closedModel.focus)
	}
}

func TestM19CrossWorkspaceHighWaterAdvancesWithoutInvalidatingCanonicalSections(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{}, nil)
	model.connection = ConnectionLive
	model.loadInFlight = false
	model.cursors = cursorState{Applied: 10, Candidate: 10, HighWater: 10}
	model.snapshot.Workspace = domain.Workspace{ID: "ws_selected", Name: "selected"}
	model.pollInFlight = true
	model.pollActiveEpoch = 1
	updated, command := model.updateEventsPolled(eventsPolledMsg{
		Generation: model.loadGeneration,
		After:      10,
		PollEpoch:  1,
		Candidate:  20,
		HighWater:  20,
	})
	if command == nil {
		t.Fatal("completed global high-water coverage did not schedule the next bounded poll")
	}
	result, ok := updated.(Model)
	if !ok {
		t.Fatalf("updateEventsPolled returned %T, want Model", updated)
	}
	if result.connection != ConnectionLive || result.loadInFlight {
		t.Fatalf("unrelated-workspace high-water triggered canonical refresh: connection=%v load=%t", result.connection, result.loadInFlight)
	}
	if result.cursors != (cursorState{Applied: 20, Candidate: 20, HighWater: 20}) {
		t.Fatalf("global high-water coverage cursors = %#v, want all 20", result.cursors)
	}
	for route, dirty := range result.dirty {
		if dirty {
			t.Fatalf("unrelated-workspace high-water dirtied route %v", route)
		}
	}
}

func TestM19ActivityAndNotificationsRetainExactNewestBoundsWithoutDuplicates(t *testing.T) {
	t.Parallel()
	events := make([]domain.Event, 250)
	for index := range events {
		sequence := int64(index + 1)
		events[index] = domain.Event{
			EventID: fmt.Sprintf("evt_%d", sequence), Sequence: sequence, Type: "task.blocked",
			OccurredAt: fmt.Sprintf("2026-08-14T12:%02d:00Z", index%60),
			Entity:     domain.EventEntity{Type: "task", ID: fmt.Sprintf("task_%d", sequence)},
		}
	}

	activity := appendActivity(nil, events)
	if len(activity) != maxActivityEvents || activity[0].Sequence != 250 || activity[len(activity)-1].Sequence != 51 {
		t.Fatalf("activity retention = len:%d first:%d last:%d, want 200/250/51", len(activity), activity[0].Sequence, activity[len(activity)-1].Sequence)
	}
	activity = appendActivity(activity, events[149:])
	if len(activity) != maxActivityEvents || activity[0].Sequence != 250 || activity[len(activity)-1].Sequence != 51 {
		t.Fatalf("duplicate activity changed exact retention: len:%d first:%d last:%d", len(activity), activity[0].Sequence, activity[len(activity)-1].Sequence)
	}

	notifications := appendNotifications(nil, events)
	if len(notifications) != maxNotifications || notifications[0].EventSequence != 250 || notifications[len(notifications)-1].EventSequence != 151 {
		t.Fatalf("notification retention = len:%d first:%d last:%d, want 100/250/151", len(notifications), notifications[0].EventSequence, notifications[len(notifications)-1].EventSequence)
	}
	notifications = appendNotifications(notifications, events[199:])
	if len(notifications) != maxNotifications || notifications[0].EventSequence != 250 || notifications[len(notifications)-1].EventSequence != 151 {
		t.Fatalf("duplicate notification changed exact retention: len:%d first:%d last:%d", len(notifications), notifications[0].EventSequence, notifications[len(notifications)-1].EventSequence)
	}
}

func TestM19FinalFenceRejectsEventDuringCanonicalSectionReads(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{}, nil)
	model.connection = ConnectionSyncing
	model.cursors = cursorState{Applied: 9, Candidate: 9, HighWater: 10}
	model.snapshot.Workspace = domain.Workspace{ID: "ws_selected", Name: "selected"}
	model.snapshot.Tasks = []domain.TaskDetail{{Task: domain.Task{ID: "task_old", Title: "old applied state"}}}
	model.loadSnapshot = model.snapshot
	model.loadSnapshot.Tasks = []domain.TaskDetail{{Task: domain.Task{ID: "task_mixed", Title: "read after concurrent mutation"}}}
	model.loadTarget = 10
	model.loadHighWater = 10
	model.loadInFlight = true

	fenced, command := model.updateFenceLoaded(fenceLoadedMsg{Generation: model.loadGeneration, HighWater: 11})
	if command == nil {
		t.Fatal("advanced final high-water did not start bounded event validation")
	}
	fencedModel := fenced.(Model)
	if fencedModel.connection != ConnectionSyncing || !fencedModel.loadInFlight || fencedModel.cursors.Applied != 9 {
		t.Fatalf("advanced fence was applied or marked live: connection=%v load=%t cursors=%#v", fencedModel.connection, fencedModel.loadInFlight, fencedModel.cursors)
	}
	if got := fencedModel.snapshot.Tasks[0].Task.ID; got != "task_old" {
		t.Fatalf("mixed staged task escaped before event validation: %q", got)
	}

	event := domain.Event{EventID: "evt_11", Sequence: 11, Type: "task.updated", WorkspaceID: "ws_selected", Entity: domain.EventEntity{Type: "task", ID: "task_mixed", Revision: 2}}
	restarted, command := fencedModel.updateFenceEvents(eventsPolledMsg{
		Generation: fencedModel.loadGeneration,
		After:      10,
		Fence:      true,
		Events:     []domain.Event{event},
		Candidate:  11,
		HighWater:  11,
	})
	if command == nil {
		t.Fatal("selected-workspace event during reads did not restart the full canonical load")
	}
	restartedModel := restarted.(Model)
	if restartedModel.loadGeneration != fencedModel.loadGeneration+1 || restartedModel.connection != ConnectionSyncing || !restartedModel.loadInFlight || restartedModel.cursors.Applied != 9 {
		t.Fatalf("mixed batch restart state = generation %d connection %v load %t cursors %#v", restartedModel.loadGeneration, restartedModel.connection, restartedModel.loadInFlight, restartedModel.cursors)
	}
	if got := restartedModel.snapshot.Tasks[0].Task.ID; got != "task_old" {
		t.Fatalf("mixed staged task became canonical after restart: %q", got)
	}

	late, command := restartedModel.updateSectionLoaded(sectionLoadedMsg{
		Generation: fencedModel.loadGeneration,
		Section:    sectionTasks,
		Tasks:      []domain.TaskDetail{{Task: domain.Task{ID: "task_late"}}},
	})
	if command != nil {
		t.Fatal("stale section result started work in the newer generation")
	}
	lateModel := late.(Model)
	if got := lateModel.snapshot.Tasks[0].Task.ID; got != "task_old" {
		t.Fatalf("stale section result overwrote applied snapshot: %q", got)
	}
}

func TestM19AppliedCursorWaitsForEveryCanonicalSectionAndFinalFence(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{}, nil)
	generation := model.loadGeneration
	loaded, command := model.updateScopeLoaded(scopeLoadedMsg{
		Generation:   generation,
		Workspace:    domain.Workspace{ID: "ws_selected", Name: "selected"},
		TargetCursor: 10,
		HighWater:    10,
	})
	if command == nil {
		t.Fatal("scope load did not launch the bounded canonical section batch")
	}
	model = loaded.(Model)
	if model.loadActive != maxConcurrentReads {
		t.Fatalf("initial active section reads = %d, want %d", model.loadActive, maxConcurrentReads)
	}

	for index, section := range canonicalSections {
		message := sectionLoadedMsg{Generation: generation, Section: section}
		if section == sectionTasks {
			message.Tasks = []domain.TaskDetail{{Task: domain.Task{ID: "task_new", Title: "staged canonical task"}}}
		}
		updated, next := model.updateSectionLoaded(message)
		model = updated.(Model)
		if model.loadActive > maxConcurrentReads {
			t.Fatalf("section %d raised active reads to %d", section, model.loadActive)
		}
		if model.cursors.Applied != 0 || model.connection == ConnectionLive || len(model.snapshot.Tasks) != 0 {
			t.Fatalf("section %d/%d published before the complete fence: connection=%v cursor=%#v tasks=%#v", index+1, len(canonicalSections), model.connection, model.cursors, model.snapshot.Tasks)
		}
		if index == len(canonicalSections)-1 && next == nil {
			t.Fatal("last canonical section did not request the final event fence")
		}
	}

	finished, command := model.updateFenceLoaded(fenceLoadedMsg{Generation: generation, HighWater: 10})
	if command == nil {
		t.Fatal("successful final fence did not schedule the next event poll")
	}
	model = finished.(Model)
	if model.connection != ConnectionLive || model.loadInFlight || model.cursors != (cursorState{Applied: 10, Candidate: 10, HighWater: 10}) {
		t.Fatalf("fenced canonical load = connection %v load %t cursors %#v", model.connection, model.loadInFlight, model.cursors)
	}
	if len(model.snapshot.Tasks) != 1 || model.snapshot.Tasks[0].Task.ID != "task_new" {
		t.Fatalf("fenced staged section was not atomically published: %#v", model.snapshot.Tasks)
	}
}

func TestM19UnsupportedEventKindIsFatalAndInvalidatesEveryCachedFact(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{}, nil)
	model.connection = ConnectionLive
	model.loadInFlight = false
	model.cursors = cursorState{Applied: 10, Candidate: 10, HighWater: 10}
	model.snapshot.Workspace = domain.Workspace{ID: "ws_selected", Name: "selected"}
	model.snapshot.Tasks = []domain.TaskDetail{{Task: domain.Task{ID: "task_cached"}}}
	model.snapshot.Events = []domain.Event{{EventID: "evt_cached", Sequence: 10, Type: "task.updated"}}
	model.selection[RouteWork] = "task_cached"
	model.routeStack = []routeFrame{{Route: RouteWork}}
	model.pollInFlight = true
	model.pollActiveEpoch = 7

	updated, command := model.updateEventsPolled(eventsPolledMsg{
		Generation: model.loadGeneration,
		After:      10,
		PollEpoch:  7,
		Err: &localapi.APIError{
			Code: "unsupported_operator_event", Message: "future fact at sequence 11", Retryable: false,
		},
	})
	if command != nil {
		t.Fatal("unsupported event kind scheduled a retry instead of stopping at a fatal version boundary")
	}
	result := updated.(Model)
	if result.connection != ConnectionFatal || result.loadInFlight || result.pollInFlight {
		t.Fatalf("unsupported event state = connection %v load %t poll %t", result.connection, result.loadInFlight, result.pollInFlight)
	}
	if result.snapshot.Workspace.ID != "" || len(result.snapshot.Tasks) != 0 || len(result.snapshot.Events) != 0 || result.cursors != (cursorState{}) {
		t.Fatalf("unsupported event retained cached facts: snapshot=%#v cursors=%#v", result.snapshot, result.cursors)
	}
	if len(result.selection) != 0 || len(result.routeStack) != 1 || result.currentRoute() != RouteOverview || !strings.Contains(result.lastError, "unsupported_operator_event") {
		t.Fatalf("unsupported event fatal diagnosis/navigation = selection %#v route %#v error %q", result.selection, result.routeStack, result.lastError)
	}
}

func TestM19AmbiguousActionErrorsRetainExactReplayKey(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{name: "transport loss", err: errors.New("connection reset after submission")},
		{name: "retryable API error", err: &localapi.APIError{Code: "storage_failed", Message: "commit result unavailable", Retryable: true}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := NewModel(Config{}, nil)
			model.connection = ConnectionLive
			model.snapshot.Workspace = domain.Workspace{ID: "ws_1", Name: "personal"}
			model.modal = modalState{Kind: modalReview, Review: actionReview{
				Choice: actionChoice{
					Kind: actionStopRun, TargetType: "run", TargetID: "run_1", Revision: 9,
					Consequence: "Request a bounded graceful stop of this run.",
				},
				IdempotencyKey: "ui-stable-replay-key",
				Executing:      true,
			}}
			updated, command := model.updateActionCompleted(actionCompletedMsg{Kind: actionStopRun, Err: test.err})
			if command == nil {
				t.Fatal("ambiguous action result did not schedule canonical reconnect")
			}
			result, ok := updated.(Model)
			if !ok {
				t.Fatalf("updateActionCompleted returned %T, want Model", updated)
			}
			if result.modal.Kind != modalReview || result.modal.Review.IdempotencyKey != "ui-stable-replay-key" || result.modal.Review.Executing || result.modal.Review.AmbiguousError == "" {
				t.Fatalf("ambiguous result lost replay state: %#v", result.modal)
			}
			if result.connection != ConnectionReconnecting {
				t.Fatalf("ambiguous result connection = %v, want reconnecting", result.connection)
			}
		})
	}
}

func TestM19SanitizesInvalidUTF8TerminalControlsAndBidi(t *testing.T) {
	t.Parallel()
	unsafe := string([]byte{'a', 0xff, 'b', 0x1b, ']', '2', ';', 'x', 0x07, 'c', 0xc2, 0x80}) +
		"\u061c\u200e\u200f\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069\u206a\u206b\u206c\u206d\u206e\u206f" +
		"\u2028\u2029\r\n\tend"
	got := sanitizeLine(unsafe)
	if !utf8.ValidString(got) {
		t.Fatalf("sanitizeLine returned invalid UTF-8: %q", got)
	}
	for _, current := range got {
		if isUnsafeControl(current) {
			t.Fatalf("sanitizeLine retained unsafe rune U+%04X in %q", current, got)
		}
	}
	if strings.ContainsAny(got, "\r\n\t") {
		t.Fatalf("sanitizeLine retained an external line/control separator: %q", got)
	}
	if strings.ContainsAny(got, "\u2028\u2029") {
		t.Fatalf("sanitizeLine retained an external Unicode line/paragraph separator: %q", got)
	}
	if strings.ContainsAny(got, "\u061c\u200e\u200f\u202a\u202b\u202c\u202d\u202e\u2066\u2067\u2068\u2069\u206a\u206b\u206c\u206d\u206e\u206f") {
		t.Fatalf("sanitizeLine retained a Unicode bidirectional formatting control: %q", got)
	}
	if !strings.Contains(got, "ab]2;xc end") {
		t.Fatalf("sanitizeLine removed safe visible content or failed to collapse whitespace: %q", got)
	}
}

func TestM19FitLineUsesWholeGraphemeDisplayWidth(t *testing.T) {
	t.Parallel()
	got := fitLine("👩‍💻", 4)
	if width := lipgloss.Width(got); width != 4 {
		t.Fatalf("fitLine grapheme display width = %d, want 4: %q", width, got)
	}
	hangulSyllable := "\u1100\u1161"
	got = fitLine(hangulSyllable, 3)
	if !strings.Contains(got, hangulSyllable) || strings.Contains(got, "…") || lipgloss.Width(got) != 3 {
		t.Fatalf("fitLine split a Hangul extended grapheme cluster: %q", got)
	}
}

func TestM19FitLineBoundsZeroWidthInput(t *testing.T) {
	t.Parallel()
	got := fitLine(strings.Repeat("\u0301", 100_000)+"x", 8)
	if width := acceptanceDisplayWidth(got); width != 8 {
		t.Fatalf("fitLine display width = %d, want 8", width)
	}
	if len(got) > 1024 {
		t.Fatalf("fitLine emitted %d bytes for an eight-cell line; zero-width input is not bounded", len(got))
	}
}

func TestM19ModalExternalTextCannotBypassSanitization(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		build func(Model) Model
	}{
		{
			name: "API error",
			build: func(model Model) Model {
				model.modal = modalState{Kind: modalError, Message: "failed\x1b]2;owned\x07\u202eexe.live"}
				return model
			},
		},
		{
			name: "filter",
			build: func(model Model) Model {
				model.modal = modalState{Kind: modalFilter}
				model.filter.SetValue("needle\x1b[2J\u2066hidden")
				return model
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := NewModel(Config{Color: ColorNever}, nil)
			model.width, model.height = 80, 24
			model = test.build(model)
			got := model.render()
			if !utf8.ValidString(got) {
				t.Fatalf("render returned invalid UTF-8: %q", got)
			}
			for _, current := range got {
				if current == '\n' {
					continue
				}
				if isUnsafeControl(current) {
					t.Fatalf("render retained unsafe rune U+%04X: %q", current, got)
				}
			}
		})
	}
}

func TestM19SelectionUsesStableIDsAcrossReorderAndRemoval(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{}, nil)
	model.selection[RouteWork] = "task_b"
	model.rowOffset[RouteWork] = 7

	model.preserveSelection(RouteWork, []record{{ID: "task_c"}, {ID: "task_b"}, {ID: "task_a"}})
	if got := model.selection[RouteWork]; got != "task_b" {
		t.Fatalf("selection after reorder = %q, want stable ID task_b", got)
	}
	if got := model.rowOffset[RouteWork]; got != 7 {
		t.Fatalf("row offset after retained selection = %d, want 7", got)
	}

	model.preserveSelection(RouteWork, []record{{ID: "task_c"}, {ID: "task_a"}})
	if got := model.selection[RouteWork]; got != "task_c" {
		t.Fatalf("selection after removal = %q, want deterministic first survivor task_c", got)
	}
	if got := model.rowOffset[RouteWork]; got != 0 {
		t.Fatalf("row offset after removal = %d, want 0", got)
	}

	model.preserveSelection(RouteWork, nil)
	if _, exists := model.selection[RouteWork]; exists {
		t.Fatal("selection survived an empty record set")
	}
}

func TestM19RouteStackAndConnectionStatesAreClosed(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{}, nil)
	for index := 0; index < maxRouteDepth*3; index++ {
		model.pushRoute(routeFrame{Route: RouteActivity, EntityID: string(rune(index + 1))})
	}
	if got := len(model.routeStack); got != maxRouteDepth {
		t.Fatalf("route depth = %d, want %d", got, maxRouteDepth)
	}
	if got := model.currentRoute(); got != RouteActivity {
		t.Fatalf("current route = %v, want Activity", got)
	}

	wantStates := map[ConnectionState]string{
		ConnectionConnecting:   "connecting",
		ConnectionSyncing:      "syncing",
		ConnectionLive:         "live",
		ConnectionReconnecting: "reconnecting",
		ConnectionFatal:        "fatal",
	}
	for state, want := range wantStates {
		if got := state.String(); got != want {
			t.Fatalf("connection state %d = %q, want %q", state, got, want)
		}
	}
	if got := ConnectionState(255).String(); got != "fatal" {
		t.Fatalf("unknown connection state = %q, want fail-closed fatal", got)
	}
}

func TestM19NOColorAndExplicitNeverDisableColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	model := NewModel(Config{Color: ColorAuto}, nil)
	if model.colorsEnabled() {
		t.Fatal("NO_COLOR did not disable styling")
	}

	t.Setenv("NO_COLOR", "")
	model = NewModel(Config{Color: ColorNever}, nil)
	if model.colorsEnabled() {
		t.Fatal("ColorNever did not disable styling")
	}
}

func TestM19ViewDependsOnlyOnConstructedModelState(t *testing.T) {
	previous, existed := os.LookupEnv("NO_COLOR")
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv("NO_COLOR", previous)
		} else {
			_ = os.Unsetenv("NO_COLOR")
		}
	})

	model := NewModel(Config{Color: ColorAuto}, nil)
	model.width, model.height = 80, 24
	before := model.render()
	if err := os.Setenv("NO_COLOR", "1"); err != nil {
		t.Fatal(err)
	}
	after := model.render()
	if after != before {
		t.Fatal("the same model rendered differently after process environment changed; View performs ambient I/O")
	}
}

func TestM19MonochromeFrameRetainsTextualConnectionAndFocus(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{Color: ColorNever}, nil)
	model.width, model.height = 80, 24
	model.connection = ConnectionReconnecting
	model.focus = FocusDetail
	model.snapshot.Workspace = domain.Workspace{ID: "ws_1", Name: "personal"}
	got := model.render()
	if strings.ContainsRune(got, '\x1b') {
		t.Fatalf("monochrome frame contains an ANSI escape: %q", got)
	}
	for _, want := range []string{"reconnecting", "cached state is stale", "focus detail", "Details [focus]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("monochrome frame lacks textual label %q:\n%s", want, got)
		}
	}
}

func TestM19BriefingMetadataAndEvidenceRemainExact(t *testing.T) {
	t.Parallel()
	briefing := domain.ManagementBriefing{
		ID: "brief_1", Revision: 1,
		Scope:               domain.BriefingScope{Type: "project", WorkspaceID: "ws_1", ProjectID: "prj_1"},
		EventCursor:         91,
		CutoffEventSequence: 89,
		CheckpointID:        "checkpoint_1",
		SinceEventSequence:  40,
		EvaluatedAt:         "2026-08-14T12:00:00Z",
		CaughtUp:            true,
		Claims: []domain.BriefingClaim{{
			ID: "bclaim_1", Kind: domain.BriefingClaimRisk, Urgency: "high", Summary: "Risk remains", Status: "open", SourceEventSequence: 88,
			Sources: []domain.BriefingClaimSource{{
				EntityType: "check_requirement_evidence", EntityID: "evidence_1", Revision: 3,
				ContentSHA256: strings.Repeat("a", 64), EventSequence: 88,
				EvidenceClass: "mechanical", EvidenceEffect: "supports", PinnedFreshness: "fresh", CurrentFreshness: "stale",
			}},
		}},
		Omitted:       []domain.BriefingOmission{{Section: "risks_unknowns", Reason: "claim_limit", Count: 2}},
		ContentSHA256: strings.Repeat("b", 64),
		ByteSize:      4096,
	}
	model := NewModel(Config{}, nil)
	model.snapshot.Briefing = briefing
	model.routeStack = []routeFrame{{Route: RouteBriefing}}
	records := model.recordsFor(RouteBriefing)
	if len(records) != 3 {
		t.Fatalf("briefing records = %d, want metadata + claim + omission", len(records))
	}
	if records[0].Briefing == nil || !reflect.DeepEqual(*records[0].Briefing, briefing) {
		t.Fatalf("briefing metadata record changed canonical briefing:\ngot  %#v\nwant %#v", records[0].Briefing, briefing)
	}
	if records[1].Claim == nil || !reflect.DeepEqual(*records[1].Claim, briefing.Claims[0]) {
		t.Fatalf("claim record changed canonical claim:\ngot  %#v\nwant %#v", records[1].Claim, briefing.Claims[0])
	}
	detail := strings.Join(detailLines(records[0]), "\n") + "\n" + strings.Join(detailLines(records[1]), "\n")
	for _, want := range []string{
		"Event cursor: 91", "Cutoff event sequence: 89", "Since event sequence: 40",
		"Content SHA-256: " + briefing.ContentSHA256,
		"class=mechanical effect=supports pinned=fresh current=stale",
	} {
		if !strings.Contains(detail, want) {
			t.Fatalf("briefing detail lacks exact %q:\n%s", want, detail)
		}
	}
}

func TestM19OverviewAggregatesCarryExactDrilldownIDs(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{}, nil)
	model.snapshot.Briefing.Claims = []domain.BriefingClaim{
		{ID: "decision_1", Kind: domain.BriefingClaimRequiredDecision},
		{ID: "risk_1", Kind: domain.BriefingClaimRisk},
		{ID: "risk_2", Kind: domain.BriefingClaimRisk},
		{ID: "unknown_1", Kind: domain.BriefingClaimUnknown},
	}
	model.snapshot.Tasks = []domain.TaskDetail{
		{Task: domain.Task{ID: "task_ready", Status: domain.TaskReady}},
		{Task: domain.Task{ID: "task_blocked", Status: domain.TaskBlocked}},
		{Task: domain.Task{ID: "task_done", Status: domain.TaskCompleted}},
	}
	model.snapshot.Runs = []domain.RunSummary{
		{ID: "run_active", Status: domain.RunActive},
		{ID: "run_done", Status: domain.RunCompleted},
	}
	model.snapshot.Approvals = []domain.ApprovalRequest{
		{ID: "approval_pending", Status: domain.ApprovalPending},
		{ID: "approval_allowed", Status: domain.ApprovalGranted},
	}

	records := model.overviewRecords()
	got := make(map[string][]string, len(records))
	for _, item := range records {
		got[item.ID] = item.Targets
	}
	want := map[string][]string{
		"summary:decisions": {"decision_1"},
		"summary:risks":     {"risk_1", "risk_2"},
		"summary:unknowns":  {"unknown_1"},
		"summary:tasks":     {"task_ready", "task_blocked"},
		"summary:runs":      {"run_active"},
		"summary:approvals": {"approval_pending"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("aggregate target IDs = %#v, want %#v", got, want)
	}
}

func TestM19CombinedSourcesNeverSilentlyDropCachedOrCanonicalRecords(t *testing.T) {
	t.Parallel()

	t.Run("complete task and run sources receive a fair bounded allocation", func(t *testing.T) {
		model := NewModel(Config{}, nil)
		model.routeStack = []routeFrame{{Route: RouteWork}}
		for index := 0; index < 400; index++ {
			model.snapshot.Tasks = append(model.snapshot.Tasks, domain.TaskDetail{Task: domain.Task{ID: fmt.Sprintf("task_%03d", index)}})
			model.snapshot.Runs = append(model.snapshot.Runs, domain.RunSummary{ID: fmt.Sprintf("run_%03d", index)})
		}
		model.snapshot.TaskTotal = len(model.snapshot.Tasks)
		model.snapshot.RunTotal = len(model.snapshot.Runs)

		records := model.recordsFor(RouteWork)
		if len(records) != maxCachedRecords {
			t.Fatalf("combined Work records = %d, want exact screen bound %d", len(records), maxCachedRecords)
		}
		summaries := map[string]record{}
		taskCount, runCount := 0, 0
		for _, item := range records {
			switch item.Kind {
			case recordSummary:
				summaries[item.ID] = item
			case recordTask:
				taskCount++
			case recordRun:
				runCount++
			}
		}
		if taskCount != 299 || runCount != 299 {
			t.Fatalf("bounded source allocation = %d tasks/%d runs, want 299/299", taskCount, runCount)
		}
		for _, id := range []string{"section:work:tasks", "section:work:runs"} {
			summary, ok := summaries[id]
			if !ok {
				t.Fatalf("missing explicit combined-source summary %q in %#v", id, summaries)
			}
			if summary.Secondary != "400 cached · 400 canonical" || summary.Diagnosis != "101 cached records omitted by the 600-record work screen bound; 0 additional canonical records were not cached." {
				t.Fatalf("summary %s = %#v", id, summary)
			}
		}
	})

	t.Run("API and screen omissions remain separate", func(t *testing.T) {
		model := NewModel(Config{}, nil)
		model.routeStack = []routeFrame{{Route: RouteWork}}
		for index := 0; index < maxCachedRecords; index++ {
			model.snapshot.Tasks = append(model.snapshot.Tasks, domain.TaskDetail{Task: domain.Task{ID: fmt.Sprintf("task_%03d", index)}})
		}
		model.snapshot.TaskTotal = 800
		model.snapshot.TaskHasMore = true

		records := model.recordsFor(RouteWork)
		if len(records) != maxCachedRecords || records[0].ID != "section:work:tasks" {
			t.Fatalf("bounded incomplete Work records = %#v", records)
		}
		summary := records[0]
		if summary.Primary != "Tasks — 599 shown" || summary.Secondary != "600 cached · 800 canonical" || summary.Diagnosis != "1 cached records omitted by the 600-record work screen bound; 200 additional canonical records were not cached." {
			t.Fatalf("incomplete task summary = %#v", summary)
		}
	})
}

func TestM19RunSummaryExcludesRolePurposeAuthorityInputs(t *testing.T) {
	t.Parallel()
	summaryType := reflect.TypeOf(domain.RunSummary{})
	for _, forbidden := range []string{"Role", "Purpose"} {
		if _, exists := summaryType.FieldByName(forbidden); exists {
			t.Fatalf("RunSummary exposes authority-looking descriptive field %s", forbidden)
		}
	}

	base := NewModel(Config{}, nil)
	base.routeStack = []routeFrame{{Route: RouteCoordination}}
	base.focus = FocusRecords
	base.snapshot.Runs = []domain.RunSummary{
		{ID: "run_b", Status: domain.RunBlocked, CanAttach: true, Revision: 7},
		{ID: "run_a", Status: domain.RunActive, Revision: 3},
	}
	base.selection[RouteCoordination] = "run_b"
	records := base.recordsFor(RouteCoordination)
	actions := base.actionsForSelection()
	if len(records) != 2 || records[0].ID != "run_b" || records[1].ID != "run_a" {
		t.Fatalf("run summary order = %#v", records)
	}
	if len(actions) != 3 {
		t.Fatalf("blocked run actions = %#v, want attach/resume/stop from canonical status", actions)
	}
	base.selection[RouteCoordination] = "run_a"
	for _, action := range base.actionsForSelection() {
		if action.Kind == actionAttachRun {
			t.Fatalf("run with can_attach=false exposed attach: %#v", action)
		}
	}
}

func acceptanceDisplayWidth(value string) int {
	width := 0
	for _, current := range value {
		if current >= 0x300 && current <= 0x36f {
			continue
		}
		width++
	}
	return width
}
