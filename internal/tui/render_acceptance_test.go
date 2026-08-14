package tui

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/lipgloss/v2"

	"crewfold/internal/domain"
)

func TestM19ConsentControlsRemainVisibleAtMinimumSizesWithLongStatus(t *testing.T) {
	t.Parallel()
	for _, size := range []struct{ width, height int }{{60, 18}, {80, 24}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			model, choice, action := approvalReviewFixture()
			model.width, model.height = size.width, size.height
			updated, _ := model.updateActionPrepared(actionPreparedMsg{
				Generation: model.actionGeneration, CanonicalGeneration: model.loadGeneration, WorkspaceID: model.snapshot.Workspace.ID,
				Choice: choice, SupervisorAction: action, HasSupervisorAction: true, IdempotencyKey: "ui-fixed-key",
			})
			review := updated.(Model)
			review.statusLine = strings.Repeat("hostile provider status must not displace consent controls ", 100)

			lockedFooter := lastRenderLine(review.render())
			if !strings.Contains(lockedFooter, "Scroll") || !strings.Contains(lockedFooter, "confirmation locked") {
				t.Fatalf("%dx%d locked footer lost review controls: %q", size.width, size.height, lockedFooter)
			}

			review.modal.ReviewOffset = review.reviewMaxOffset()
			confirmFooter := lastRenderLine(review.render())
			if !strings.Contains(confirmFooter, "Esc cancel") || !strings.Contains(confirmFooter, "Ctrl+Enter confirm") {
				t.Fatalf("%dx%d confirm footer lost consent controls: %q", size.width, size.height, confirmFooter)
			}

			review.modal.Review.Executing = true
			executingFooter := lastRenderLine(review.render())
			if !strings.Contains(executingFooter, "Ctrl+C") || !strings.Contains(executingFooter, "second Ctrl+C quits") {
				t.Fatalf("%dx%d executing footer lost cancel/quit controls: %q", size.width, size.height, executingFooter)
			}
		})
	}
}

func TestM19SyncingCacheIsAlwaysLabeledStale(t *testing.T) {
	t.Parallel()
	model := NewModel(Config{Color: ColorNever}, nil)
	model.width, model.height = 80, 24
	model.connection = ConnectionSyncing
	model.snapshot.Workspace = domain.Workspace{ID: "ws_1", Name: "personal"}
	header := strings.Split(model.render(), "\n")[0]
	if !strings.Contains(header, "syncing") || !strings.Contains(header, "cached state is stale") {
		t.Fatalf("syncing cached header lacks honest stale label: %q", header)
	}
}

func TestM19HostileMegabyteRenderIsStrictlyWindowBounded(t *testing.T) {
	t.Parallel()
	hostile := strings.Repeat("wide界🙂\x1b]2;owned\x07\u202esecret\xff\n", 32768)
	model := NewModel(Config{Color: ColorNever}, nil)
	model.width, model.height = 80, 24
	model.connection = ConnectionLive
	model.loadInFlight = false
	model.snapshot.Workspace = domain.Workspace{ID: "ws_1", Name: hostile}
	model.snapshot.Tasks = []domain.TaskDetail{{Task: domain.Task{ID: "task_1", Title: hostile, Description: hostile, Status: domain.TaskReady}}}
	model.routeStack = []routeFrame{{Route: RouteWork}, {Route: RouteWork, EntityType: "task", EntityID: "task_1"}}
	model.selection[RouteWork] = "task_1"
	model.focus = FocusDetail
	model.statusLine = hostile

	frame := model.render()
	if !utf8.ValidString(frame) {
		t.Fatal("bounded frame is not valid UTF-8")
	}
	lines := strings.Split(frame, "\n")
	if len(lines) != model.height {
		t.Fatalf("render lines = %d, want exact window height %d", len(lines), model.height)
	}
	for index, line := range lines {
		if width := lipgloss.Width(line); width != model.width {
			t.Fatalf("line %d display width = %d, want %d", index, width, model.width)
		}
		for _, current := range line {
			if isUnsafeControl(current) {
				t.Fatalf("line %d retained unsafe U+%04X", index, current)
			}
		}
	}
	maximumBytes := model.width*model.height*4 + model.height
	if len(frame) > maximumBytes {
		t.Fatalf("render bytes = %d, exceeds strict window-derived bound %d", len(frame), maximumBytes)
	}
}

func lastRenderLine(frame string) string {
	lines := strings.Split(frame, "\n")
	return lines[len(lines)-1]
}
