package tui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

const m19TransportWorkspace = "ws_11111111111111111111111111111111"
const m19TransportProject = "prj_22222222222222222222222222222222"
const m19TransportTask = "task_33333333333333333333333333333333"

type m19TransportServer struct {
	listener net.Listener
	done     chan struct{}
	errorsMu sync.Mutex
	errors   []error
	handler  func(localapi.Request) (any, *localapi.APIError)
}

func newM19TransportServer(t *testing.T, handler func(localapi.Request) (any, *localapi.APIError)) (*localapi.Client, *m19TransportServer) {
	t.Helper()
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "crewfold.sock"))
	if err != nil {
		t.Fatal(err)
	}
	server := &m19TransportServer{listener: listener, done: make(chan struct{}), handler: handler}
	go server.serve()
	t.Cleanup(func() {
		_ = listener.Close()
		<-server.done
		server.errorsMu.Lock()
		defer server.errorsMu.Unlock()
		for _, serveErr := range server.errors {
			t.Errorf("fake local API server: %v", serveErr)
		}
	})
	return localapi.NewClient(listener.Addr().String()), server
}

func (server *m19TransportServer) serve() {
	defer close(server.done)
	var connections sync.WaitGroup
	defer connections.Wait()
	for {
		connection, err := server.listener.Accept()
		if err != nil {
			if !strings.Contains(err.Error(), "use of closed network connection") {
				server.recordError(err)
			}
			return
		}
		connections.Add(1)
		go func() {
			defer connections.Done()
			defer connection.Close()
			decoder, encoder := json.NewDecoder(connection), json.NewEncoder(connection)
			var hello localapi.Request
			if err := decoder.Decode(&hello); err != nil {
				server.recordError(err)
				return
			}
			if hello.Method != localapi.MethodHello {
				server.recordError(fmt.Errorf("first method = %q, want %q", hello.Method, localapi.MethodHello))
				return
			}
			if err := encoder.Encode(localapi.MarshalResult(hello.ID, localapi.MaxProtocol, localapi.HelloResult{
				Type: "hello", SelectedProtocol: localapi.MaxProtocol,
				ServerMin: localapi.MinProtocol, ServerMax: localapi.MaxProtocol,
			})); err != nil {
				server.recordError(err)
				return
			}
			var request localapi.Request
			if err := decoder.Decode(&request); err != nil {
				server.recordError(err)
				return
			}
			result, apiErr := server.handler(request)
			response := localapi.MarshalResult(request.ID, request.Protocol, result)
			if apiErr != nil {
				response = localapi.ErrorResponse(request.ID, request.Protocol, apiErr)
			}
			if err := encoder.Encode(response); err != nil {
				server.recordError(err)
			}
		}()
	}
}

func (server *m19TransportServer) recordError(err error) {
	server.errorsMu.Lock()
	server.errors = append(server.errors, err)
	server.errorsMu.Unlock()
}

func TestM19EventContinuationFailsClosedAcrossTransportPages(t *testing.T) {
	tests := []struct {
		name    string
		pages   map[string]localapi.EventsListResult
		wantErr string
	}{
		{
			name: "changed high water",
			pages: map[string]localapi.EventsListResult{
				"":       m19EventPage(12, 2, []domain.Event{m19TransportEvent(11)}, "page-2"),
				"page-2": m19EventPage(13, 2, []domain.Event{m19TransportEvent(12)}, ""),
			},
			wantErr: "changed its frozen high-water or total",
		},
		{
			name: "changed total",
			pages: map[string]localapi.EventsListResult{
				"":       m19EventPage(12, 2, []domain.Event{m19TransportEvent(11)}, "page-2"),
				"page-2": m19EventPage(12, 3, []domain.Event{m19TransportEvent(12)}, ""),
			},
			wantErr: "changed its frozen high-water or total",
		},
		{
			name: "nonmonotonic sequence across pages",
			pages: map[string]localapi.EventsListResult{
				"":       m19EventPage(12, 2, []domain.Event{m19TransportEvent(12)}, "page-2"),
				"page-2": m19EventPage(12, 2, []domain.Event{m19TransportEvent(11)}, ""),
			},
			wantErr: "global sequence order",
		},
		{
			name: "duplicate event identity across pages",
			pages: map[string]localapi.EventsListResult{
				"": m19EventPage(12, 2, []domain.Event{m19TransportEvent(11)}, "page-2"),
				"page-2": func() localapi.EventsListResult {
					page := m19EventPage(12, 2, []domain.Event{m19TransportEvent(12)}, "")
					page.Events[0].EventID = m19TransportEvent(11).EventID
					return page
				}(),
			},
			wantErr: "repeated an event ID",
		},
		{
			name: "continuation cursor cycle",
			pages: map[string]localapi.EventsListResult{
				"":       m19EventPage(14, 4, []domain.Event{m19TransportEvent(11)}, "page-a"),
				"page-a": m19EventPage(14, 4, []domain.Event{m19TransportEvent(12)}, "page-b"),
				"page-b": m19EventPage(14, 4, []domain.Event{m19TransportEvent(13)}, "page-a"),
			},
			wantErr: "cursor cycled",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
				if request.Method != localapi.MethodEventsList {
					t.Fatalf("method = %q, want %q", request.Method, localapi.MethodEventsList)
				}
				var params localapi.EventsListParams
				if err := json.Unmarshal(request.Params, &params); err != nil {
					t.Fatalf("decode params: %v", err)
				}
				page, found := test.pages[params.Cursor]
				if !found {
					t.Fatalf("unexpected continuation cursor %q", params.Cursor)
				}
				return page, nil
			})
			message := pollEventsCmd(context.Background(), make(chan struct{}, 1), client, m19TransportWorkspace, 7, 10, false, 3)().(eventsPolledMsg)
			if message.Err == nil || !strings.Contains(message.Err.Error(), test.wantErr) {
				t.Fatalf("poll error = %v, want substring %q", message.Err, test.wantErr)
			}
			if message.Candidate != 0 || len(message.Events) != 0 {
				t.Fatalf("failed continuation leaked candidate/events: candidate=%d events=%d", message.Candidate, len(message.Events))
			}
		})
	}
}

func TestM19RawUnknownEventTypeIsDefinitiveAndInvalidatesPublishedCache(t *testing.T) {
	event := m19TransportEvent(11)
	event.Type = "future.fact"
	client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
		if request.Method != localapi.MethodEventsList {
			t.Fatalf("method = %q, want %q", request.Method, localapi.MethodEventsList)
		}
		return m19EventPage(11, 1, []domain.Event{event}, ""), nil
	})
	model := NewModel(Config{Color: ColorNever}, client)
	model.connection = ConnectionLive
	model.loadInFlight = false
	model.cursors = cursorState{Applied: 10, Candidate: 10, HighWater: 10}
	model.snapshot.Workspace = m19TransportWorkspaceValue(m19TransportWorkspace, "personal")
	model.snapshot.Tasks = []domain.TaskDetail{m19TransportTaskDetail(m19TransportTask, "cached task")}
	model.snapshot.Events = []domain.Event{m19TransportEvent(10)}
	model.selection[RouteWork] = m19TransportTask
	model.routeStack = []routeFrame{{Route: RouteWork}}
	model.pollInFlight = true
	model.pollActiveEpoch = 7

	message := pollEventsCmd(context.Background(), make(chan struct{}, 1), client, m19TransportWorkspace, model.loadGeneration, 10, false, 7)().(eventsPolledMsg)
	var apiErr *localapi.APIError
	if !errors.As(message.Err, &apiErr) || apiErr.Code != "unsupported_operator_event" || apiErr.Retryable {
		t.Fatalf("raw unknown event poll error = %v, want definitive unsupported_operator_event", message.Err)
	}
	updated, command := model.updateEventsPolled(message)
	result := updated.(Model)
	if command != nil || result.connection != ConnectionFatal || result.loadInFlight || result.pollInFlight ||
		result.snapshot.Workspace.ID != "" || len(result.snapshot.Tasks) != 0 || len(result.snapshot.Events) != 0 || result.cursors != (cursorState{}) ||
		len(result.selection) != 0 || result.currentRoute() != RouteOverview || !strings.Contains(result.lastError, "unsupported_operator_event") {
		t.Fatalf("unknown raw event did not invalidate all published truth: connection=%v load=%t poll=%t snapshot=%#v cursors=%#v selection=%#v route=%v err=%q command=%v",
			result.connection, result.loadInFlight, result.pollInFlight, result.snapshot, result.cursors, result.selection, result.currentRoute(), result.lastError, command)
	}
}

func TestM19EventCatchupAcceptsExactBoundaryAndYieldsAfterTenPages(t *testing.T) {
	t.Run("exact 1000 terminal", func(t *testing.T) {
		events := m19TransportEvents(1, eventPageSize)
		client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
			return m19EventPage(eventPageSize, eventPageSize, events, ""), nil
		})
		message := pollEventsCmd(context.Background(), make(chan struct{}, 1), client, m19TransportWorkspace, 1, 0, false, 1)().(eventsPolledMsg)
		if message.Err != nil || message.Candidate != eventPageSize || message.HighWater != eventPageSize || len(message.Events) != eventPageSize {
			t.Fatalf("exact-boundary result = candidate:%d high:%d events:%d err:%v", message.Candidate, message.HighWater, len(message.Events), message.Err)
		}
	})

	t.Run("more than 10000 yields", func(t *testing.T) {
		var calls atomic.Int64
		client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
			var params localapi.EventsListParams
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Fatalf("decode params: %v", err)
			}
			page := int(calls.Add(1))
			wantCursor := ""
			if page > 1 {
				wantCursor = fmt.Sprintf("page-%d", page)
			}
			if params.Cursor != wantCursor {
				t.Fatalf("page %d cursor = %q, want %q", page, params.Cursor, wantCursor)
			}
			start := (page-1)*eventPageSize + 1
			return m19EventPage(10001, 10001, m19TransportEvents(start, eventPageSize), fmt.Sprintf("page-%d", page+1)), nil
		})
		message := pollEventsCmd(context.Background(), make(chan struct{}, 1), client, m19TransportWorkspace, 1, 0, false, 1)().(eventsPolledMsg)
		if message.Err != nil || message.Candidate != 10000 || message.HighWater != 10001 || len(message.Events) != 10000 {
			t.Fatalf("capped catch-up = candidate:%d high:%d events:%d err:%v", message.Candidate, message.HighWater, len(message.Events), message.Err)
		}
		if got := calls.Load(); got != maxEventPages {
			t.Fatalf("event page calls = %d, want %d", got, maxEventPages)
		}
	})
}

func TestM19EventFrozenTotalFailsClosedAndZeroSelectedEventsAdvanceGlobalHighWater(t *testing.T) {
	t.Run("underreported total across continuation", func(t *testing.T) {
		pages := map[string]localapi.EventsListResult{
			"":       m19EventPage(12, 1, []domain.Event{m19TransportEvent(11)}, "page-2"),
			"page-2": m19EventPage(12, 1, []domain.Event{m19TransportEvent(12)}, ""),
		}
		client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
			var params localapi.EventsListParams
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Fatalf("decode params: %v", err)
			}
			return pages[params.Cursor], nil
		})
		message := pollEventsCmd(context.Background(), make(chan struct{}, 1), client, m19TransportWorkspace, 1, 10, false, 1)().(eventsPolledMsg)
		if message.Err == nil || !strings.Contains(message.Err.Error(), "continuation after completing its frozen total") || message.Candidate != 0 || len(message.Events) != 0 {
			t.Fatalf("underreported total result = candidate:%d events:%d err:%v", message.Candidate, len(message.Events), message.Err)
		}
	})

	t.Run("overreported terminal total", func(t *testing.T) {
		client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
			return m19EventPage(12, 2, []domain.Event{m19TransportEvent(11)}, ""), nil
		})
		message := pollEventsCmd(context.Background(), make(chan struct{}, 1), client, m19TransportWorkspace, 1, 10, false, 1)().(eventsPolledMsg)
		if message.Err == nil || !strings.Contains(message.Err.Error(), "did not complete its frozen total") || message.Candidate != 0 || len(message.Events) != 0 {
			t.Fatalf("overreported total result = candidate:%d events:%d err:%v", message.Candidate, len(message.Events), message.Err)
		}
	})

	t.Run("zero selected-workspace events still advance global high-water", func(t *testing.T) {
		client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
			return m19EventPage(99, 0, []domain.Event{}, ""), nil
		})
		message := pollEventsCmd(context.Background(), make(chan struct{}, 1), client, m19TransportWorkspace, 1, 10, false, 1)().(eventsPolledMsg)
		if message.Err != nil || message.Candidate != 99 || message.HighWater != 99 || len(message.Events) != 0 {
			t.Fatalf("empty selected-workspace page = candidate:%d high:%d events:%d err:%v", message.Candidate, message.HighWater, len(message.Events), message.Err)
		}
	})
}

func TestM19SparseCanonicalCollectionStopsAfterExactlyThreePagesWithHonestMore(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
		if request.Method != localapi.MethodTaskList {
			t.Fatalf("method = %q, want only %q", request.Method, localapi.MethodTaskList)
		}
		var params localapi.TaskListParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Fatalf("decode params: %v", err)
		}
		page := int(calls.Add(1))
		wantCursor := ""
		if page > 1 {
			wantCursor = fmt.Sprintf("task-page-%d", page)
		}
		if params.Cursor != wantCursor || params.Limit != collectionPageSize {
			t.Fatalf("task page %d params = %#v, want cursor %q limit %d", page, params, wantCursor, collectionPageSize)
		}
		return localapi.TaskListResult{
			Schema: localapi.TaskListSchema, Type: "task_list",
			Tasks:      []domain.TaskDetail{m19TransportTaskDetail(fmt.Sprintf("task_%032x", page), fmt.Sprintf("sparse task %d", page))},
			PageResult: localapi.PageResult{NextCursor: fmt.Sprintf("task-page-%d", page+1), HasMore: true, Total: 100},
		}, nil
	})

	items, total, hasMore, err := loadTasks(context.Background(), client, m19TransportWorkspace, "")
	if err != nil {
		t.Fatalf("sparse canonical task load: %v", err)
	}
	if got := calls.Load(); got != maxCollectionPages {
		t.Fatalf("sparse task calls = %d, want exactly %d", got, maxCollectionPages)
	}
	if len(items) != maxCollectionPages || total != 100 || !hasMore {
		t.Fatalf("sparse task result = items:%d total:%d more:%t, want 3/100/true", len(items), total, hasMore)
	}
	for index, item := range items {
		if want := fmt.Sprintf("task_%032x", index+1); item.Task.ID != want {
			t.Fatalf("sparse task %d ID = %q, want %q", index, item.Task.ID, want)
		}
	}
}

func TestM19CanonicalTaskPageChainsFailClosedOnCursorIdentityAndTotalViolations(t *testing.T) {
	tests := []struct {
		name    string
		pages   map[string]localapi.TaskListResult
		wantErr string
	}{
		{
			name: "nonadvancing cursor",
			pages: map[string]localapi.TaskListResult{
				"":            m19TaskPage(3, []domain.TaskDetail{m19TransportTaskDetail("task_00000000000000000000000000000001", "first")}, "task-page-a"),
				"task-page-a": m19TaskPage(3, []domain.TaskDetail{m19TransportTaskDetail("task_00000000000000000000000000000002", "second")}, "task-page-a"),
			},
			wantErr: "cursor did not advance",
		},
		{
			name: "cyclic cursor",
			pages: map[string]localapi.TaskListResult{
				"":            m19TaskPage(4, []domain.TaskDetail{m19TransportTaskDetail("task_00000000000000000000000000000001", "first")}, "task-page-a"),
				"task-page-a": m19TaskPage(4, []domain.TaskDetail{m19TransportTaskDetail("task_00000000000000000000000000000002", "second")}, "task-page-b"),
				"task-page-b": m19TaskPage(4, []domain.TaskDetail{m19TransportTaskDetail("task_00000000000000000000000000000003", "third")}, "task-page-a"),
			},
			wantErr: "cursor chain contains a cycle",
		},
		{
			name: "duplicate identity across pages",
			pages: map[string]localapi.TaskListResult{
				"":            m19TaskPage(2, []domain.TaskDetail{m19TransportTaskDetail("task_00000000000000000000000000000001", "first")}, "task-page-a"),
				"task-page-a": m19TaskPage(2, []domain.TaskDetail{m19TransportTaskDetail("task_00000000000000000000000000000001", "repeated")}, ""),
			},
			wantErr: "repeats a record identity across pages",
		},
		{
			name: "terminal page under total",
			pages: map[string]localapi.TaskListResult{
				"": m19TaskPage(2, []domain.TaskDetail{m19TransportTaskDetail("task_00000000000000000000000000000001", "only")}, ""),
			},
			wantErr: "terminal canonical collection page is incomplete",
		},
		{
			name: "terminal chain over total",
			pages: map[string]localapi.TaskListResult{
				"": m19TaskPage(2, []domain.TaskDetail{m19TransportTaskDetail("task_00000000000000000000000000000001", "first")}, "task-page-a"),
				"task-page-a": m19TaskPage(2, []domain.TaskDetail{
					m19TransportTaskDetail("task_00000000000000000000000000000002", "second"),
					m19TransportTaskDetail("task_00000000000000000000000000000003", "third"),
				}, ""),
			},
			wantErr: "contains more records than its declared total",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
				if request.Method != localapi.MethodTaskList {
					t.Fatalf("method = %q, want %q", request.Method, localapi.MethodTaskList)
				}
				var params localapi.TaskListParams
				if err := json.Unmarshal(request.Params, &params); err != nil {
					t.Fatalf("decode params: %v", err)
				}
				page, found := test.pages[params.Cursor]
				if !found {
					t.Fatalf("unexpected task continuation cursor %q", params.Cursor)
				}
				return page, nil
			})
			items, total, hasMore, err := loadTasks(context.Background(), client, m19TransportWorkspace, m19TransportProject)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("task page-chain error = %v, want substring %q", err, test.wantErr)
			}
			if items != nil || total != 0 || hasMore {
				t.Fatalf("invalid task page chain leaked items/total/more: %#v/%d/%t", items, total, hasMore)
			}
		})
	}
}

func TestM19WorkspaceChooserRejectsDuplicateIdentityAcrossPages(t *testing.T) {
	workspace := m19TransportWorkspaceValue(m19TransportWorkspace, "personal")
	client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
		var params localapi.WorkspaceListParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Fatalf("decode params: %v", err)
		}
		page := localapi.WorkspaceListResult{
			Schema: localapi.WorkspaceListSchema, Type: "workspace_list", Workspaces: []domain.Workspace{workspace},
			PageResult: localapi.PageResult{Total: 2},
		}
		if params.Cursor == "" {
			page.NextCursor, page.HasMore = "workspace-page-2", true
		} else if params.Cursor != "workspace-page-2" {
			t.Fatalf("unexpected workspace continuation cursor %q", params.Cursor)
		}
		return page, nil
	})
	message := loadScopeCmd(context.Background(), make(chan struct{}, maxConcurrentReads), make(chan struct{}, 1), client, Config{}, 1, 0)().(scopeLoadedMsg)
	if message.Err == nil || !message.Fatal || !strings.Contains(message.Err.Error(), "repeats a record identity across pages") ||
		message.Workspace.ID != "" || len(message.WorkspaceChoices) != 0 {
		t.Fatalf("duplicate workspace chooser result = fatal:%t err:%v workspace:%q choices:%d", message.Fatal, message.Err, message.Workspace.ID, len(message.WorkspaceChoices))
	}
}

func m19TaskPage(total int64, tasks []domain.TaskDetail, nextCursor string) localapi.TaskListResult {
	return localapi.TaskListResult{
		Schema: localapi.TaskListSchema, Type: "task_list", Tasks: tasks,
		PageResult: localapi.PageResult{NextCursor: nextCursor, HasMore: nextCursor != "", Total: total},
	}
}

func TestM19CanonicalReadsShareOneMaxFourSemaphore(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 8)
	var current atomic.Int64
	var maximum atomic.Int64
	client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
		if request.Method != localapi.MethodBriefingShow {
			t.Fatalf("method = %q, want %q", request.Method, localapi.MethodBriefingShow)
		}
		active := current.Add(1)
		for previous := maximum.Load(); active > previous && !maximum.CompareAndSwap(previous, active); previous = maximum.Load() {
		}
		entered <- struct{}{}
		<-release
		current.Add(-1)
		return localapi.BriefingShowResult{
			Schema: localapi.BriefingShowSchema, Type: "management_briefing",
			Briefing: m19TransportBriefing(m19TransportWorkspace),
		}, nil
	})

	ioSlots := make(chan struct{}, maxConcurrentReads)
	results := make(chan sectionLoadedMsg, 8)
	workspace := domain.Workspace{ID: m19TransportWorkspace}
	for index := 0; index < 8; index++ {
		go func(generation uint64) {
			results <- loadSectionCmd(context.Background(), ioSlots, client, generation, sectionBriefing, workspace, nil)().(sectionLoadedMsg)
		}(uint64(index + 1))
	}
	for index := 0; index < maxConcurrentReads; index++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("four canonical reads did not enter the transport")
		}
	}
	select {
	case <-entered:
		t.Fatal("a fifth canonical read bypassed the max-four semaphore")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for index := 0; index < 8; index++ {
		select {
		case result := <-results:
			if result.Err != nil {
				t.Fatalf("canonical read failed: %v", result.Err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("canonical read did not finish")
		}
	}
	if got := maximum.Load(); got > maxConcurrentReads {
		t.Fatalf("maximum concurrent canonical reads = %d, want <= %d", got, maxConcurrentReads)
	}
}

func TestM19WorkspaceChooserFailsClosedBeyondThreePages(t *testing.T) {
	t.Parallel()
	var calls atomic.Int64
	client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
		if request.Method != localapi.MethodWorkspaceList {
			t.Fatalf("method = %q, want only %q", request.Method, localapi.MethodWorkspaceList)
		}
		var params localapi.WorkspaceListParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Fatalf("decode params: %v", err)
		}
		page := int(calls.Add(1))
		wantCursor := ""
		if page > 1 {
			wantCursor = fmt.Sprintf("workspace-page-%d", page)
		}
		if params.Cursor != wantCursor || params.Limit != collectionPageSize {
			t.Fatalf("workspace page %d params = %#v, want cursor %q limit %d", page, params, wantCursor, collectionPageSize)
		}
		workspaces := []domain.Workspace{m19TransportWorkspaceValue(fmt.Sprintf("ws_%032x", page), fmt.Sprintf("workspace-%03d", page))}
		return localapi.WorkspaceListResult{
			Schema: localapi.WorkspaceListSchema, Type: "workspace_list", Workspaces: workspaces,
			PageResult: localapi.PageResult{NextCursor: fmt.Sprintf("workspace-page-%d", page+1), HasMore: true, Total: 100},
		}, nil
	})

	message := loadScopeCmd(context.Background(), make(chan struct{}, maxConcurrentReads), make(chan struct{}, 1), client, Config{}, 1, 0)().(scopeLoadedMsg)
	if message.Err == nil || !message.Fatal || !strings.Contains(message.Err.Error(), "exceeds three bounded pages") ||
		!strings.Contains(message.Err.Error(), "--workspace") || len(message.WorkspaceChoices) != 0 || message.Workspace.ID != "" {
		t.Fatalf("oversized chooser result = fatal:%t err:%v choices:%d workspace:%q", message.Fatal, message.Err, len(message.WorkspaceChoices), message.Workspace.ID)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("workspace chooser calls = %d, want exactly three bounded pages", got)
	}
}

func TestM19ImplicitSingleWorkspacePinsFrozenReviewAcrossLaterWorkspaceCreation(t *testing.T) {
	soleWorkspace := m19TransportWorkspaceValue(m19TransportWorkspace, "personal")
	secondWorkspace := m19TransportWorkspaceValue("ws_22222222222222222222222222222222", "later")
	var listCalls atomic.Int64
	var showCalls atomic.Int64
	client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
		switch request.Method {
		case localapi.MethodWorkspaceList:
			call := listCalls.Add(1)
			workspaces := []domain.Workspace{soleWorkspace}
			if call > 1 {
				// A second workspace exists by the next refresh. An unpinned
				// implicit scope would now reopen the chooser over the frozen
				// ambiguous owner action below.
				workspaces = append(workspaces, secondWorkspace)
			}
			return localapi.WorkspaceListResult{
				Schema: localapi.WorkspaceListSchema, Type: "workspace_list", Workspaces: workspaces,
				PageResult: localapi.PageResult{Total: int64(len(workspaces))},
			}, nil
		case localapi.MethodWorkspaceShow:
			showCalls.Add(1)
			var params localapi.WorkspaceShowParams
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Fatalf("decode pinned workspace show: %v", err)
			}
			if params.Identifier != soleWorkspace.ID {
				t.Fatalf("pinned workspace identifier = %q, want %q", params.Identifier, soleWorkspace.ID)
			}
			return localapi.WorkspaceShowResult{
				Schema: localapi.WorkspaceShowSchema, Type: "workspace", Workspace: soleWorkspace,
			}, nil
		case localapi.MethodEventsList:
			return m19EventPage(44, 0, []domain.Event{}, ""), nil
		default:
			t.Fatalf("unexpected scope request %q", request.Method)
			return nil, nil
		}
	})

	model := NewModel(Config{Color: ColorNever}, client)
	initialMessage, ok := model.Init()().(scopeLoadedMsg)
	if !ok || initialMessage.Err != nil || initialMessage.Workspace.ID != soleWorkspace.ID || len(initialMessage.WorkspaceChoices) != 0 {
		t.Fatalf("initial implicit scope = %#v", initialMessage)
	}
	initialGeneration := model.loadGeneration
	loadedValue, sectionCommand := model.Update(initialMessage)
	if sectionCommand == nil {
		t.Fatal("initial single-workspace scope did not begin canonical section reads")
	}
	model = loadedValue.(Model)
	if model.config.Workspace != soleWorkspace.ID {
		t.Fatalf("implicit workspace was not pinned: config workspace = %q, want %q", model.config.Workspace, soleWorkspace.ID)
	}

	// Stand in for completion of the canonical section batch, then retain an
	// exact request and idempotency key after an ambiguous response.
	model.loadInFlight = false
	model.snapshot.Workspace = soleWorkspace
	model.loadSnapshot = model.snapshot
	model.connection = ConnectionLive
	model.cursors = cursorState{Applied: 44, Candidate: 44, HighWater: 44}
	reviewContext, cancelReview := context.WithCancel(context.Background())
	defer cancelReview()
	model.modal = modalState{Kind: modalReview, Review: actionReview{
		Choice: actionChoice{
			Kind: actionStopRun, TargetType: "run", TargetID: "run_1", Revision: 7,
			Consequence: "Request a bounded graceful stop of this exact run.",
		},
		IdempotencyKey: "ui-frozen-key", Generation: model.actionGeneration,
		RequestFrozen: true, AmbiguousError: "response lost after request dispatch", cancel: cancelReview,
	}}
	model.focus = FocusModal

	refreshCommand := model.restartCanonicalLoad(model.reloadCursor())
	refreshGeneration := model.loadGeneration
	if refreshCommand == nil || refreshGeneration == initialGeneration {
		t.Fatalf("canonical refresh did not start a new generation: initial=%d refresh=%d", initialGeneration, refreshGeneration)
	}

	// An obsolete chooser result cannot replace the frozen review after the
	// refresh generation has advanced.
	staleValue, staleCommand := model.Update(scopeLoadedMsg{
		Generation: initialGeneration, WorkspaceChoices: []domain.Workspace{soleWorkspace, secondWorkspace},
	})
	if staleCommand != nil {
		t.Fatal("obsolete workspace chooser scheduled work")
	}
	model = staleValue.(Model)
	assertM19FrozenPinnedReview(t, model, soleWorkspace.ID, refreshGeneration, reviewContext)

	refreshMessage, ok := refreshCommand().(scopeLoadedMsg)
	if !ok || refreshMessage.Err != nil || len(refreshMessage.WorkspaceChoices) != 0 || refreshMessage.Workspace.ID != soleWorkspace.ID {
		t.Fatalf("pinned canonical refresh = %#v", refreshMessage)
	}
	refreshedValue, refreshedCommand := model.Update(refreshMessage)
	if refreshedCommand == nil {
		t.Fatal("pinned canonical refresh did not begin section reads")
	}
	refreshed := refreshedValue.(Model)
	assertM19FrozenPinnedReview(t, refreshed, soleWorkspace.ID, refreshGeneration, reviewContext)
	if got := listCalls.Load(); got != 1 {
		t.Fatalf("workspace list calls = %d, want only the initial implicit resolution", got)
	}
	if got := showCalls.Load(); got != 1 {
		t.Fatalf("workspace show calls = %d, want one pinned refresh resolution", got)
	}
}

func assertM19FrozenPinnedReview(t *testing.T, model Model, workspaceID string, generation uint64, reviewContext context.Context) {
	t.Helper()
	if model.config.Workspace != workspaceID || model.loadGeneration != generation {
		t.Fatalf("pinned scope changed: workspace=%q generation=%d, want %q/%d", model.config.Workspace, model.loadGeneration, workspaceID, generation)
	}
	review := model.modal.Review
	if model.modal.Kind != modalReview || model.focus != FocusModal || review.Choice.TargetID != "run_1" ||
		review.Choice.Revision != 7 || review.IdempotencyKey != "ui-frozen-key" || !review.RequestFrozen ||
		review.AmbiguousError != "response lost after request dispatch" || review.cancel == nil {
		t.Fatalf("frozen ambiguous review was replaced or mutated: focus=%v modal=%v review=%#v", model.focus, model.modal.Kind, review)
	}
	select {
	case <-reviewContext.Done():
		t.Fatal("frozen ambiguous review cancellation was triggered")
	default:
	}
}

func TestM19PollAndFenceShareOneEventRequestSlot(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{}, 2)
	var current atomic.Int64
	var maximum atomic.Int64
	client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
		if request.Method != localapi.MethodEventsList {
			t.Fatalf("method = %q, want %q", request.Method, localapi.MethodEventsList)
		}
		active := current.Add(1)
		for previous := maximum.Load(); active > previous && !maximum.CompareAndSwap(previous, active); previous = maximum.Load() {
		}
		entered <- struct{}{}
		<-release
		current.Add(-1)
		return m19EventPage(0, 0, []domain.Event{}, ""), nil
	})

	eventSlot := make(chan struct{}, 1)
	results := make(chan any, 2)
	go func() {
		results <- pollEventsCmd(context.Background(), eventSlot, client, m19TransportWorkspace, 1, 0, false, 1)()
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("poll did not enter the transport")
	}
	go func() {
		results <- fenceCanonicalCmd(context.Background(), eventSlot, client, 1, m19TransportWorkspace, 0)()
	}()
	select {
	case <-entered:
		t.Fatal("fence bypassed the occupied event request slot")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for index := 0; index < 2; index++ {
		select {
		case result := <-results:
			switch message := result.(type) {
			case eventsPolledMsg:
				if message.Err != nil {
					t.Fatalf("poll failed: %v", message.Err)
				}
			case fenceLoadedMsg:
				if message.Err != nil {
					t.Fatalf("fence failed: %v", message.Err)
				}
			default:
				t.Fatalf("result type = %T", result)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("event request did not finish")
		}
	}
	if got := maximum.Load(); got != 1 {
		t.Fatalf("maximum concurrent event requests = %d, want 1", got)
	}
}

func TestM19RealBubbleTeaProgramUsesControlledInputOutputAndWindowSize(t *testing.T) {
	client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
		page := localapi.PageResult{}
		switch request.Method {
		case localapi.MethodWorkspaceShow:
			return localapi.WorkspaceShowResult{
				Schema: localapi.WorkspaceShowSchema, Type: "workspace",
				Workspace: m19TransportWorkspaceValue(m19TransportWorkspace, "personal"),
			}, nil
		case localapi.MethodEventsList:
			return m19EventPage(0, 0, []domain.Event{}, ""), nil
		case localapi.MethodBriefingShow:
			return localapi.BriefingShowResult{
				Schema: localapi.BriefingShowSchema, Type: "management_briefing",
				Briefing: m19TransportBriefing(m19TransportWorkspace),
			}, nil
		case localapi.MethodObjectiveList:
			return localapi.ObjectiveListResult{Schema: localapi.ObjectiveListSchema, Type: "objective_list", Objectives: []domain.Objective{}, PageResult: page}, nil
		case localapi.MethodTaskList:
			return localapi.TaskListResult{
				Schema: localapi.TaskListSchema, Type: "task_list",
				Tasks:      []domain.TaskDetail{m19TransportTaskDetail(m19TransportTask, "Program smoke task")},
				PageResult: localapi.PageResult{Total: 1},
			}, nil
		case localapi.MethodRunList:
			return localapi.RunListResult{Schema: localapi.RunListSchema, Type: "run_list", Runs: []domain.RunSummary{}, PageResult: page}, nil
		case localapi.MethodAgentList:
			return localapi.AgentListResult{Schema: localapi.AgentListSchema, Type: "agent_list", Agents: []domain.AgentDefinition{}, PageResult: page}, nil
		case localapi.MethodApprovalList:
			return localapi.ApprovalListResult{Schema: localapi.ApprovalListSchema, Type: "approval_list", Approvals: []domain.ApprovalRequest{}, PageResult: page}, nil
		case localapi.MethodCheckList:
			return localapi.CheckRunListResult{Schema: localapi.CheckRunListSchema, Type: "check_run_list", Runs: []domain.CheckRunListItem{}, PageResult: page}, nil
		case localapi.MethodClaimList:
			return localapi.ClaimListResult{Schema: localapi.ClaimListSchema, Type: "claim_list", Claims: []domain.WorkClaim{}, PageResult: page}, nil
		case localapi.MethodOverlapList:
			return localapi.OverlapListResult{Schema: localapi.OverlapListSchema, Type: "overlap_list", Overlaps: []domain.WorkOverlap{}, PageResult: page}, nil
		case localapi.MethodDriftList:
			return localapi.DriftListResult{Schema: localapi.DriftListSchema, Type: "drift_list", Drifts: []domain.ClaimDrift{}, PageResult: page}, nil
		case localapi.MethodMeetingList:
			return localapi.MeetingListResult{Schema: localapi.MeetingListSchema, Type: "meeting_list", Meetings: []domain.Meeting{}, PageResult: page}, nil
		default:
			return nil, &localapi.APIError{Code: "unexpected_test_method", Message: request.Method, Retryable: false}
		}
	})

	programContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	inputReader, inputWriter := io.Pipe()
	defer inputReader.Close()
	defer inputWriter.Close()
	capture := newM19ProgramCapture()
	controllerResult := make(chan error, 1)
	go func() {
		steps := []struct {
			wait  string
			input string
		}{
			{wait: "Canonical state synchronized", input: "j"},
			{wait: "Canonical byte size: 1", input: "j"},
			{wait: "Program smoke task", input: "\t\r?"},
			{wait: "Keyboard help", input: "\x03q"},
		}
		mark := 0
		for _, step := range steps {
			if err := capture.waitAfter(mark, step.wait, 3*time.Second); err != nil {
				controllerResult <- err
				cancel()
				return
			}
			mark = capture.length()
			if _, err := io.WriteString(inputWriter, step.input); err != nil {
				controllerResult <- err
				cancel()
				return
			}
		}
		controllerResult <- nil
	}()

	model := NewModel(Config{Workspace: m19TransportWorkspace, Color: ColorNever}, client)
	model.ctx = programContext
	model.loadCancel()
	model.loadCtx, model.loadCancel = context.WithCancel(programContext)
	program := tea.NewProgram(model,
		tea.WithContext(programContext), tea.WithInput(inputReader), tea.WithOutput(capture), tea.WithWindowSize(80, 24),
	)
	finalValue, err := program.Run()
	if controllerErr := <-controllerResult; controllerErr != nil {
		t.Fatalf("program controller: %v\noutput:\n%s", controllerErr, capture.String())
	}
	if err != nil {
		t.Fatalf("Bubble Tea program: %v\noutput:\n%s", err, capture.String())
	}
	final, ok := finalValue.(Model)
	if !ok {
		t.Fatalf("final model type = %T", finalValue)
	}
	frame := final.currentFrame()
	if final.width != 80 || final.height != 24 || frame.Route != RouteWork || frame.EntityType != "task" || frame.EntityID != m19TransportTask ||
		final.focus != FocusDetail || final.modal.Kind != modalNone || final.actionGeneration == 0 {
		t.Fatalf("final controlled program state = size:%dx%d frame:%#v focus:%v modal:%v action-generation:%d", final.width, final.height, frame, final.focus, final.modal.Kind, final.actionGeneration)
	}
}

func TestM19AmbiguousApprovalReplaySendsExactFrozenRequestOverConcreteSocket(t *testing.T) {
	t.Parallel()
	captured := make(chan localapi.ApprovalDecisionParams, 1)
	client, _ := newM19TransportServer(t, func(request localapi.Request) (any, *localapi.APIError) {
		if request.Method != localapi.MethodApprovalAllow {
			t.Fatalf("replay method = %q, want %q", request.Method, localapi.MethodApprovalAllow)
		}
		var params localapi.ApprovalDecisionParams
		if err := json.Unmarshal(request.Params, &params); err != nil {
			t.Fatalf("decode approval replay: %v", err)
		}
		captured <- params
		_, _, action := approvalReviewFixture()
		approval := domain.ApprovalRequest{
			ID: params.Approval, WorkspaceID: params.Workspace, ProjectID: action.ProjectID, ActionID: action.ID,
			Status: domain.ApprovalConsumed, DecisionNote: params.DecisionNote,
			ExpectedActionRevision: action.Revision, Revision: params.ExpectedRevision + 2,
			CreatedAt: "2026-08-14T12:00:00Z", UpdatedAt: "2026-08-14T12:00:01Z",
			CreatedBy: "subsystem:supervisor", UpdatedBy: "local-owner",
		}
		action.Status = domain.SupervisorActionApplied
		action.Decision = params.DecisionNote
		action.Revision = approval.ExpectedActionRevision + 1
		action.UpdatedAt = "2026-08-14T12:00:01Z"
		action.UpdatedBy = "local-owner"
		return localapi.ApprovalMutationResult{
			Schema: localapi.ApprovalMutationSchema, Type: "approval_mutation",
			Approval: approval, Action: action, EventSequence: 61,
		}, nil
	})

	model, choice, action := approvalReviewFixture()
	model.client = client
	preparedValue, _ := model.updateActionPrepared(actionPreparedMsg{
		Generation: model.actionGeneration, CanonicalGeneration: model.loadGeneration, WorkspaceID: model.snapshot.Workspace.ID,
		Choice: choice, SupervisorAction: action, HasSupervisorAction: true, IdempotencyKey: "ui-fixed-key",
	})
	model = preparedValue.(Model)
	model.modal.Review.RequestFrozen = true
	model.modal.Review.Executing = false
	model.modal.Review.DecisionNote = "exact frozen owner note"
	model.modal.Review.AmbiguousError = "response lost after commit"
	model.snapshot.Approvals = nil
	model.modal.ReviewOffset = model.reviewMaxOffset()

	updatedValue, command := model.updateModalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl}), "ctrl+enter")
	if command == nil {
		t.Fatal("exact frozen approval replay did not return a concrete local-API command")
	}
	message, ok := command().(actionCompletedMsg)
	if !ok || message.Err != nil || message.IdempotencyKey != "ui-fixed-key" {
		t.Fatalf("approval replay result = %#v", message)
	}
	params := <-captured
	want := localapi.ApprovalDecisionParams{
		Workspace: model.snapshot.Workspace.ID, Approval: choice.TargetID, ExpectedRevision: 3,
		DecisionNote: "exact frozen owner note", IdempotencyKey: "ui-fixed-key",
	}
	if !reflect.DeepEqual(params, want) {
		t.Fatalf("concrete replay params = %#v, want exact frozen %#v", params, want)
	}
	updated := updatedValue.(Model)
	if updated.modal.Review.cancel != nil {
		updated.modal.Review.cancel()
	}
}

type m19ProgramCapture struct {
	mu      sync.Mutex
	buffer  strings.Builder
	changed chan struct{}
}

func newM19ProgramCapture() *m19ProgramCapture {
	return &m19ProgramCapture{changed: make(chan struct{}, 1)}
}

func (capture *m19ProgramCapture) Write(data []byte) (int, error) {
	capture.mu.Lock()
	count, err := capture.buffer.Write(data)
	capture.mu.Unlock()
	select {
	case capture.changed <- struct{}{}:
	default:
	}
	return count, err
}

func (capture *m19ProgramCapture) String() string {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.buffer.String()
}

func (capture *m19ProgramCapture) length() int {
	capture.mu.Lock()
	defer capture.mu.Unlock()
	return capture.buffer.Len()
}

func (capture *m19ProgramCapture) waitAfter(offset int, pattern string, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		capture.mu.Lock()
		value := capture.buffer.String()
		capture.mu.Unlock()
		if offset <= len(value) && strings.Contains(value[offset:], pattern) {
			return nil
		}
		select {
		case <-capture.changed:
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for %q after output byte %d", pattern, offset)
		}
	}
}

func m19EventPage(highWater, total int, events []domain.Event, nextCursor string) localapi.EventsListResult {
	if events == nil {
		events = []domain.Event{}
	}
	return localapi.EventsListResult{
		Schema: localapi.EventsListSchema, Type: "event_list", WorkspaceID: m19TransportWorkspace,
		HighWater: int64(highWater), Events: events,
		PageResult: localapi.PageResult{NextCursor: nextCursor, HasMore: nextCursor != "", Total: int64(total)},
	}
}

func m19TransportEvents(start, count int) []domain.Event {
	events := make([]domain.Event, count)
	for index := range events {
		events[index] = m19TransportEvent(int64(start + index))
	}
	return events
}

func m19TransportEvent(sequence int64) domain.Event {
	return domain.Event{
		EventID: fmt.Sprintf("evt_%032x", sequence), Sequence: sequence,
		Type: "task.updated", SchemaVersion: 1,
		OccurredAt: "2026-08-14T12:00:00Z", RecordedAt: "2026-08-14T12:00:00Z",
		Actor:         domain.EventActor{ActorID: "local-owner", ActorType: domain.EventActorHuman},
		WorkspaceID:   m19TransportWorkspace,
		Entity:        domain.EventEntity{Type: "task", ID: fmt.Sprintf("task_%032x", sequence), Revision: 1},
		CorrelationID: fmt.Sprintf("corr_%d", sequence), Data: json.RawMessage(`{}`),
	}
}

func m19TransportWorkspaceValue(id, name string) domain.Workspace {
	return domain.Workspace{
		ID: id, Name: name, Revision: 1,
		CreatedAt: "2026-08-14T12:00:00Z", UpdatedAt: "2026-08-14T12:00:00Z",
		CreatedBy: "local-owner", UpdatedBy: "local-owner",
	}
}

func m19TransportTaskDetail(id, title string) domain.TaskDetail {
	return domain.TaskDetail{
		Task: domain.Task{
			ID: id, WorkspaceID: m19TransportWorkspace, ProjectID: m19TransportProject,
			Title: title, Status: domain.TaskReady, Priority: 100, Revision: 1,
			CreatedAt: "2026-08-14T12:00:00Z", UpdatedAt: "2026-08-14T12:00:00Z",
			CreatedBy: "local-owner", UpdatedBy: "local-owner",
		},
		Dependencies: []domain.TaskDependency{},
		Readiness:    domain.TaskReadiness{Ready: true, Reason: "task has no incomplete dependencies and is unassigned"},
	}
}

func m19TransportBriefing(workspaceID string) domain.ManagementBriefing {
	return domain.ManagementBriefing{
		ID: "briefing_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Revision: 1,
		Scope:       domain.BriefingScope{Type: "workspace", WorkspaceID: workspaceID},
		EvaluatedAt: "2026-08-14T12:00:00Z", CaughtUp: true,
		Claims: []domain.BriefingClaim{}, Omitted: []domain.BriefingOmission{},
		ContentSHA256: strings.Repeat("a", 64), ByteSize: 1,
	}
}
