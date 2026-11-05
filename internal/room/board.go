package room

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"
)

// BoardInput is shared by the CLI and web. Browser identity is supplied only by
// the authenticated server, never by JSON sent by a collaborator.
type BoardInput struct {
	Room             string    `json:"room"`
	Key              string    `json:"key,omitempty"`
	WorkingDirectory string    `json:"working_directory,omitempty"`
	ParticipantID    string    `json:"participant_id,omitempty"`
	Owner            bool      `json:"owner,omitempty"`
	BrowserDevice    string    `json:"-"`
	OperationID      string    `json:"operation_id,omitempty"`
	Version          int64     `json:"version,omitempty"`
	Title            *string   `json:"title,omitempty"`
	Notes            *string   `json:"notes,omitempty"`
	Status           *string   `json:"status,omitempty"`
	Workers          *[]string `json:"workers,omitempty"`
	Assignee         *string   `json:"assignee,omitempty"`
	AddWorkers       []string  `json:"add_workers,omitempty"`
	RemoveWorkers    []string  `json:"remove_workers,omitempty"`
	Blocker          *string   `json:"blocker,omitempty"`
	HumanInput       *bool     `json:"human_input,omitempty"`
	Options          *[]string `json:"options,omitempty"`
	Documents        *[]string `json:"documents,omitempty"`
	LinkDocuments    []string  `json:"link_documents,omitempty"`
	Body             string    `json:"body,omitempty"`
	Resolve          bool      `json:"resolve,omitempty"`
	Archived         bool      `json:"archived,omitempty"`
	FilterStatus     string    `json:"filter_status,omitempty"`
	Worker           string    `json:"worker,omitempty"`
	Search           string    `json:"search,omitempty"`
	After            int64     `json:"after,omitempty"`
	Limit            int       `json:"limit,omitempty"`
	HistoryBefore    int64     `json:"history_before,omitempty"`
	EntriesBefore    int64     `json:"entries_before,omitempty"`
	ReplyTo          int64     `json:"reply_to,omitempty"`
	Post             int64     `json:"post,omitempty"`
}

type BoardItem struct {
	ID                 string     `json:"id"`
	RoomID             string     `json:"room_id"`
	Key                string     `json:"key"`
	Number             int64      `json:"number"`
	Kind               string     `json:"kind"`
	Title              string     `json:"title"`
	Notes              string     `json:"notes"`
	Status             string     `json:"status"`
	Workers            []string   `json:"workers"`
	Assignee           string     `json:"assignee,omitempty"`
	Subscribers        []string   `json:"subscribers"`
	Blocker            string     `json:"blocker"`
	HumanInput         bool       `json:"human_input"`
	HumanInputRevision int64      `json:"human_input_revision,omitempty"`
	AnswerCount        int64      `json:"answer_count"`
	LastAnswerBy       string     `json:"last_answer_by,omitempty"`
	Options            []string   `json:"options"`
	Documents          []Document `json:"documents"`
	Archived           bool       `json:"archived"`
	Version            int64      `json:"version"`
	CreatedBy          string     `json:"created_by"`
	UpdatedBy          string     `json:"updated_by"`
	CreatedAt          string     `json:"created_at"`
	UpdatedAt          string     `json:"updated_at"`
	EntryCount         int        `json:"entry_count"`
	PendingQuestions   int        `json:"pending_questions"`
	Preview            string     `json:"preview,omitempty"`
}

type BoardHistory struct {
	Revision  int64  `json:"revision"`
	Actor     string `json:"actor"`
	Action    string `json:"action"`
	CreatedAt string `json:"created_at"`
	Sequence  int64  `json:"sequence,omitempty"`
	Body      string `json:"body,omitempty"`
}
type BoardDetail struct {
	Item           BoardItem      `json:"item"`
	LatestAnswer   *BoardHistory  `json:"latest_answer,omitempty"`
	History        []BoardHistory `json:"history"`
	NextHistory    int64          `json:"next_history,omitempty"`
	OperationID    string         `json:"operation_id,omitempty"`
	Entries        []BoardEntry   `json:"entries"`
	NextEntries    int64          `json:"next_entries,omitempty"`
	PendingEntries []BoardEntry   `json:"pending_entries"`
}
type BoardPage struct {
	Items []BoardItem `json:"items"`
	Next  int64       `json:"next,omitempty"`
	Total int         `json:"total"`
}

func (s *Store) initializeBoard(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS board_items (
 id TEXT PRIMARY KEY, room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE,
 kind TEXT NOT NULL CHECK(kind IN ('task','question')), number INTEGER NOT NULL,
 status TEXT NOT NULL, archived INTEGER NOT NULL CHECK(archived IN (0,1)),
 version INTEGER NOT NULL, data TEXT NOT NULL, UNIQUE(room_id,kind,number)
) STRICT;
CREATE INDEX IF NOT EXISTS board_room_view ON board_items(room_id,kind,archived,status,number);
CREATE TABLE IF NOT EXISTS board_history (
 item_id TEXT NOT NULL REFERENCES board_items(id) ON DELETE CASCADE,
 revision INTEGER NOT NULL, actor TEXT NOT NULL, action TEXT NOT NULL, created_at TEXT NOT NULL,
 message_id TEXT REFERENCES messages(id), PRIMARY KEY(item_id,revision)
) STRICT;
CREATE TABLE IF NOT EXISTS board_operations (
 room_id TEXT NOT NULL REFERENCES rooms(id) ON DELETE CASCADE, actor_id TEXT NOT NULL,
 operation_id TEXT NOT NULL, request_hash TEXT NOT NULL, result TEXT NOT NULL,
 PRIMARY KEY(room_id,actor_id,operation_id)
) STRICT;`)
	if err != nil {
		return err
	}
	if err := s.initializeBoardEntries(ctx); err != nil {
		return err
	}
	return s.initializeBoardSubscriptions(ctx)
}

func boardMethod(method string) (string, string, bool) {
	parts := strings.Split(method, ".")
	if len(parts) == 2 && parts[0] == "thread" {
		parts[0] = "question"
		if parts[1] == "create" {
			return "question", "start", true
		}
		if parts[1] == "ask" {
			return "question", "ask-post", true
		}
	}
	if len(parts) != 2 || (parts[0] != "task" && parts[0] != "question") {
		return "", "", false
	}
	switch parts[1] {
	case "reply", "edit-post":
		return parts[0], parts[1], true
	case "ask-post":
		return parts[0], parts[1], true
	case "list", "show", "update":
		return parts[0], parts[1], true
	case "create", "archive", "restore":
		return parts[0], parts[1], parts[0] == "task"
	case "ask", "answer", "resolve", "reopen":
		if parts[0] == "task" && parts[1] == "ask" {
			return "task", "ask-post", true
		}
		if parts[0] == "task" && parts[1] == "answer" {
			return "task", "answer", true
		}
		return parts[0], parts[1], parts[0] == "question"
	}
	return "", "", false
}

func boardNumber(key, kind string) (int64, error) {
	prefix := "T-"
	if kind == "question" {
		prefix = "Q-"
		key = strings.Replace(key, "TH-", "Q-", 1)
	}
	if !strings.HasPrefix(key, prefix) {
		return 0, fmt.Errorf("expected a %s record, for example %s1", kind, prefix)
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(key, prefix), 10, 64)
	if err != nil || n < 1 {
		return 0, errors.New("invalid board record number")
	}
	return n, nil
}

func boardItemTx(ctx context.Context, tx *sql.Tx, roomID, kind, key string) (BoardItem, error) {
	var item BoardItem
	n, err := boardNumber(key, kind)
	if err != nil {
		return item, err
	}
	var data string
	err = tx.QueryRowContext(ctx, `SELECT data FROM board_items WHERE room_id=? AND kind=? AND number=?`, roomID, kind, n).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return item, errors.New("board record not found in this room")
	}
	if err != nil {
		return item, err
	}
	err = json.Unmarshal([]byte(data), &item)
	if item.Options == nil {
		item.Options = []string{}
	}
	if err == nil {
		err = boardQuestionStateTx(ctx, tx, &item)
	}
	return item, err
}

// Derive answer metadata from canonical history, including records created before
// human-input requests became a separate, repeatable state. Respect the revision
// boundary so operation receipt replays cannot pick up later answers.
func boardQuestionStateTx(ctx context.Context, tx *sql.Tx, item *BoardItem) error {
	return boardEntryStateTx(ctx, tx, item)
}

func legacyBoardQuestionStateTx(ctx context.Context, tx *sql.Tx, item *BoardItem) error {
	if item.Kind != "question" {
		return nil
	}
	var humanAnswer, legacyRequest int64
	err := tx.QueryRowContext(ctx, `SELECT
 COUNT(CASE WHEN h.action IN ('answer','answered and resolved') THEN 1 END),
 COALESCE(MAX(CASE WHEN h.action IN ('answer','answered and resolved') AND m.sender_kind IN ('owner','member') THEN h.revision END),0),
 COALESCE(MAX(CASE WHEN h.action LIKE '%human-input hint%' THEN h.revision END),0)
 FROM board_history h LEFT JOIN messages m ON m.id=h.message_id WHERE h.item_id=? AND h.revision<=?`, item.ID, item.Version).Scan(&item.AnswerCount, &humanAnswer, &legacyRequest)
	if err != nil {
		return err
	}
	request := item.HumanInputRevision
	if request == 0 {
		request = legacyRequest
	}
	if item.Status == "resolved" || (humanAnswer > 0 && humanAnswer >= request) {
		item.HumanInput = false
	}
	item.LastAnswerBy = ""
	if item.AnswerCount > 0 {
		return tx.QueryRowContext(ctx, `SELECT actor FROM board_history WHERE item_id=? AND revision<=? AND action IN ('answer','answered and resolved') ORDER BY revision DESC LIMIT 1`, item.ID, item.Version).Scan(&item.LastAnswerBy)
	}
	return nil
}

func boardDetailTx(ctx context.Context, tx *sql.Tx, item BoardItem, before int64, entriesBefore ...int64) (BoardDetail, error) {
	if err := boardQuestionStateTx(ctx, tx, &item); err != nil {
		return BoardDetail{}, err
	}
	detail := BoardDetail{Item: item, History: []BoardHistory{}}
	entries, err := boardEntriesTx(ctx, tx, item)
	if err != nil {
		return detail, err
	}
	detail.PendingEntries = []BoardEntry{}
	cutoff := int64(0)
	if len(entriesBefore) > 0 {
		cutoff = entriesBefore[0]
	}
	detail.Entries = []BoardEntry{}
	for _, entry := range entries {
		if cutoff == 0 || entry.Number < cutoff {
			detail.Entries = append(detail.Entries, entry)
		}
	}
	if len(detail.Entries) > 50 {
		detail.Entries = detail.Entries[len(detail.Entries)-50:]
		detail.NextEntries = detail.Entries[0].Number
	}
	// Keep unanswered questions accessible even when their post is on an older page.
	if item.Status != "resolved" && len(detail.Entries) > 0 {
		for _, entry := range entries {
			if entry.Kind == "question" && !entry.Answered && entry.Number < detail.Entries[0].Number {
				detail.PendingEntries = append(detail.PendingEntries, entry)
			}
		}
	}
	if item.AnswerCount > 0 {
		var answer BoardHistory
		err := tx.QueryRowContext(ctx, `SELECT h.revision,h.actor,h.action,h.created_at,m.sequence,m.body FROM board_history h JOIN messages m ON m.id=h.message_id WHERE h.item_id=? AND h.revision<=? AND h.action IN ('answer','answered and resolved') ORDER BY h.revision DESC LIMIT 1`, item.ID, item.Version).Scan(&answer.Revision, &answer.Actor, &answer.Action, &answer.CreatedAt, &answer.Sequence, &answer.Body)
		if err != nil {
			return detail, err
		}
		// The canonical room event starts with a generated question heading;
		// the detail already displays that title, so show only the answer here.
		answer.Body = eventBody(answer.Body)
		detail.LatestAnswer = &answer
	}
	rows, err := tx.QueryContext(ctx, `SELECT h.revision,h.actor,h.action,h.created_at,COALESCE(m.sequence,0),COALESCE(m.body,'') FROM board_history h LEFT JOIN messages m ON m.id=h.message_id WHERE h.item_id=? AND h.revision<=? AND (?=0 OR h.revision<?) ORDER BY h.revision DESC LIMIT 51`, item.ID, item.Version, before, before)
	if err != nil {
		return detail, err
	}
	defer rows.Close()
	for rows.Next() {
		var h BoardHistory
		if err := rows.Scan(&h.Revision, &h.Actor, &h.Action, &h.CreatedAt, &h.Sequence, &h.Body); err != nil {
			return detail, err
		}
		detail.History = append(detail.History, h)
	}
	if len(detail.History) > 50 {
		detail.History = detail.History[:50]
		detail.NextHistory = detail.History[49].Revision
	}
	return detail, rows.Err()
}

// Board reads and mutates room-local records. Discussion posts and their
// history links commit atomically; metadata edits notify other subscribers.
func (s *Store) Board(ctx context.Context, method string, in BoardInput) (any, error) {
	kind, action, ok := boardMethod(method)
	if !ok {
		return nil, errors.New("unknown board method")
	}
	if err := validateBoardInput(kind, action, in); err != nil {
		return nil, err
	}
	if method == "thread.update" && (in.Notes != nil || in.Options != nil || in.HumanInput != nil) {
		return nil, errors.New("thread update edits metadata; use reply, ask, or edit-post for discussion")
	}
	if (method == "thread.answer" || method == "task.answer") && in.ReplyTo == 0 {
		return nil, errors.New("answer requires reply_to (--to P-N) from show")
	}
	room, err := s.resolveRoom(ctx, in.Room)
	if err != nil {
		return nil, err
	}
	if in.After < 0 || in.HistoryBefore < 0 || in.EntriesBefore < 0 {
		return nil, errors.New("cursor cannot be negative")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if action == "list" {
		limit := in.Limit
		if limit <= 0 {
			limit = 50
		}
		if limit > 100 {
			limit = 100
		}
		filter := "room_id=? AND kind=? AND archived=? AND (?='' OR status=?) AND (?='' OR instr(lower(json_extract(data,'$.title') || ' ' || json_extract(data,'$.notes')),lower(?))>0) AND (?='' OR EXISTS(SELECT 1 FROM json_each(json_extract(data,'$.workers')) WHERE value=?))"
		args := []any{room.ID, kind, in.Archived, in.FilterStatus, in.FilterStatus, in.Search, in.Search, strings.TrimPrefix(in.Worker, "@"), strings.TrimPrefix(in.Worker, "@")}
		cursor, order := "number>?", "number"
		cursorArgs := []any{in.After}
		if kind == "task" && in.FilterStatus == "done" {
			// Show recently changed completed work first, with a stable tie-breaker.
			cursor = "(?=0 OR (json_extract(data,'$.updated_at'),number)<(COALESCE((SELECT json_extract(c.data,'$.updated_at') FROM board_items c WHERE c.room_id=? AND c.kind=? AND c.number=?),''),?))"
			cursorArgs = []any{in.After, room.ID, kind, in.After, in.After}
			order = "json_extract(data,'$.updated_at') DESC, number DESC"
		}
		queryArgs := append(append(append([]any{}, args...), cursorArgs...), limit+1)
		rows, err := tx.QueryContext(ctx, "SELECT data FROM board_items WHERE "+filter+" AND "+cursor+" ORDER BY "+order+" LIMIT ?", queryArgs...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		page := BoardPage{Items: []BoardItem{}}
		for rows.Next() {
			var data string
			var item BoardItem
			if err = rows.Scan(&data); err != nil {
				return nil, err
			}
			if err = json.Unmarshal([]byte(data), &item); err != nil {
				return nil, err
			}
			if item.Options == nil {
				item.Options = []string{}
			}
			page.Items = append(page.Items, item)
		}
		if len(page.Items) > limit {
			page.Items = page.Items[:limit]
			page.Next = page.Items[limit-1].Number
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM board_items WHERE "+filter, args...).Scan(&page.Total); err != nil {
			return nil, err
		}
		for i := range page.Items {
			if err := boardQuestionStateTx(ctx, tx, &page.Items[i]); err != nil {
				return nil, err
			}
		}
		return page, nil
	}
	if action == "show" {
		item, err := boardItemTx(ctx, tx, room.ID, kind, in.Key)
		if err != nil {
			return nil, err
		}
		return boardDetailTx(ctx, tx, item, in.HistoryBefore, in.EntriesBefore)
	}
	if err := tx.QueryRowContext(ctx, `SELECT status FROM rooms WHERE id=?`, room.ID).Scan(&room.Status); err != nil {
		return nil, err
	}
	if room.Status != "open" {
		return nil, errors.New("room is archived")
	}
	if len(in.OperationID) < 1 || len(in.OperationID) > 128 {
		return nil, errors.New("a bounded operation_id is required for retry-safe board mutations")
	}
	var participantID *string
	handle, name, senderKind, actorID := "owner", "You", "owner", "owner"
	if in.BrowserDevice != "" {
		err = tx.QueryRowContext(ctx, `SELECT d.browser_handle FROM network_devices d JOIN network_room_memberships m ON m.device_id=d.id WHERE d.id=? AND m.room_id=?`, in.BrowserDevice, room.ID).Scan(&handle)
		if err != nil || handle == "" {
			return nil, errors.New("configure your collaborator browser identity before editing the board")
		}
		name, senderKind, actorID = handle, "member", in.BrowserDevice
	} else if !in.Owner {
		p, err := resolveParticipantActorTx(ctx, tx, room.ID, in.ParticipantID, in.WorkingDirectory, "")
		if err != nil {
			return nil, err
		}
		participantID = &p.ID
		handle, name, senderKind, actorID = p.Handle, p.DisplayName, p.Kind, p.ID
	}
	// Normalize transport-only identity before hashing so a retry through the
	// same identity can use a room slug or ID without changing its operation.
	fingerprint := in
	fingerprint.Room = room.ID
	fingerprint.WorkingDirectory = ""
	fingerprint.ParticipantID = ""
	fingerprint.Owner = false
	fingerprint.OperationID = ""
	raw, _ := json.Marshal(fingerprint)
	digest := fmt.Sprintf("%x", sha256.Sum256(append([]byte(method+"\n"), raw...)))
	var oldHash, oldResult string
	err = tx.QueryRowContext(ctx, `SELECT request_hash,result FROM board_operations WHERE room_id=? AND actor_id=? AND operation_id=?`, room.ID, actorID, in.OperationID).Scan(&oldHash, &oldResult)
	if err == nil {
		if oldHash != digest {
			return nil, errors.New("operation_id was already used for a different request")
		}
		var detail BoardDetail
		err = json.Unmarshal([]byte(oldResult), &detail)
		if err != nil {
			return nil, err
		}
		replayed, err := boardDetailTx(ctx, tx, detail.Item, 0)
		replayed.OperationID = detail.OperationID
		return replayed, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	now := s.now().UTC().Format(time.RFC3339Nano)
	creating := action == "create" || action == "ask" || action == "start"
	item := BoardItem{RoomID: room.ID, Kind: kind, Status: "backlog", Workers: []string{}, Options: []string{}, Documents: []Document{}, CreatedBy: handle, CreatedAt: now}
	if creating {
		if kind == "question" {
			item.Status = "open"
		}
		if in.Title == nil {
			return nil, errors.New("title is required")
		}
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(number),0)+1 FROM board_items WHERE room_id=? AND kind=?`, room.ID, kind).Scan(&item.Number); err != nil {
			return nil, err
		}
		prefix := "T-"
		if kind == "question" {
			prefix = "Q-"
			if action == "start" {
				prefix = "TH-"
			}
		}
		item.Key = prefix + strconv.FormatInt(item.Number, 10)
		item.ID, err = randomID("board_")
		if err != nil {
			return nil, err
		}
	} else {
		item, err = boardItemTx(ctx, tx, room.ID, kind, in.Key)
		if err != nil {
			return nil, err
		}
		if kind == "question" && item.Status == "resolved" && action != "reopen" && action != "resolve" {
			return nil, errors.New("question is resolved and read-only; reopen it before editing or answering")
		}
		// Independent answers append safely without a stale-field overwrite. An
		// answer that also resolves must check the version like every other edit.
		if ((action != "answer" && action != "reply" && action != "ask-post") || in.Resolve || in.Version > 0) && (in.Version <= 0 || in.Version != item.Version) {
			return nil, fmt.Errorf("version conflict: %s is revision %d; read it again before editing", item.Key, item.Version)
		}
	}
	before := item
	if in.Assignee != nil {
		if in.Workers != nil || len(in.AddWorkers)+len(in.RemoveWorkers) > 0 {
			return nil, errors.New("use assignee or legacy worker fields, not both")
		}
		in.Workers = &[]string{*in.Assignee}
	}
	if kind == "question" && (in.Workers != nil || len(in.AddWorkers)+len(in.RemoveWorkers) > 0) {
		return nil, errors.New("threads have an OP and automatic subscribers, not assignees")
	}
	if creating && kind == "task" {
		item.Workers = []string{handle}
	}
	if action == "update" || creating {
		if in.Title != nil {
			item.Title, err = boundedText("title", *in.Title, 1, 240)
			if err != nil {
				return nil, err
			}
			if strings.ContainsAny(item.Title, "\r\n") {
				return nil, errors.New("title must be one line")
			}
		}
		if in.Notes != nil {
			item.Notes, err = boundedText("notes", *in.Notes, 0, 16384)
			if err != nil {
				return nil, err
			}
		}
		if in.Status != nil {
			item.Status = *in.Status
		}
		if in.Blocker != nil {
			item.Blocker, err = boundedText("blocker", *in.Blocker, 0, 1000)
			if err != nil {
				return nil, err
			}
		}
		if kind == "task" && (in.HumanInput != nil || in.Resolve) {
			return nil, errors.New("human input and resolution are question fields")
		}
		if kind == "question" && in.Blocker != nil {
			return nil, errors.New("blocker is a task field")
		}
		if in.HumanInput != nil {
			item.HumanInput = *in.HumanInput
			if item.HumanInput {
				item.HumanInputRevision = item.Version + 1
			}
		}
		if in.Options != nil {
			if len(*in.Options) > 8 {
				return nil, errors.New("questions support at most 8 answer options")
			}
			item.Options = []string{}
			seen := map[string]bool{}
			for _, option := range *in.Options {
				option, err = boundedText("answer option", option, 1, 240)
				if err != nil {
					return nil, err
				}
				if strings.ContainsAny(option, "\r\n") {
					return nil, errors.New("answer options must be single lines")
				}
				key := strings.ToLower(option)
				if seen[key] {
					return nil, errors.New("answer options must be distinct")
				}
				seen[key] = true
				item.Options = append(item.Options, option)
			}
		}
		if len(in.AddWorkers)+len(in.RemoveWorkers) > 0 {
			workers := append([]string{}, item.Workers...)
			if in.Workers != nil {
				workers = append([]string{}, (*in.Workers)...)
			}
			workers = append(workers, in.AddWorkers...)
			remove := map[string]bool{}
			for _, w := range in.RemoveWorkers {
				remove[strings.TrimPrefix(strings.TrimSpace(w), "@")] = true
			}
			filtered := []string{}
			for _, w := range workers {
				if !remove[strings.TrimPrefix(strings.TrimSpace(w), "@")] {
					filtered = append(filtered, w)
				}
			}
			in.Workers = &filtered
		}
		if in.Workers != nil {
			if len(*in.Workers) > 32 {
				return nil, errors.New("at most 32 workers may be listed")
			}
			item.Workers = []string{}
			seen := map[string]bool{}
			for _, worker := range *in.Workers {
				worker = strings.TrimPrefix(strings.TrimSpace(worker), "@")
				if worker == "" || seen[worker] {
					continue
				}
				seen[worker] = true
				if worker != "owner" {
					var count int
					err = tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM participants WHERE room_id=? AND handle=?)+(SELECT COUNT(*) FROM network_devices d JOIN network_room_memberships m ON m.device_id=d.id WHERE m.room_id=? AND d.browser_handle=?)`, room.ID, worker, room.ID, worker).Scan(&count)
					if err != nil {
						return nil, err
					}
					if count == 0 {
						return nil, fmt.Errorf("worker @%s is not in this room", worker)
					}
				}
				item.Workers = append(item.Workers, worker)
			}
		}
		if kind == "task" {
			if len(item.Workers) != 1 {
				return nil, errors.New("a task must have exactly one assignee; use --assignee HANDLE to reassign")
			}
			item.Assignee = item.Workers[0]
		}
		if len(in.LinkDocuments) > 0 {
			refs := []string{}
			for _, d := range item.Documents {
				refs = append(refs, d.ID)
			}
			if in.Documents != nil {
				refs = append([]string{}, (*in.Documents)...)
			}
			refs = append(refs, in.LinkDocuments...)
			in.Documents = &refs
		}
		if in.Documents != nil {
			if len(*in.Documents) > 16 {
				return nil, errors.New("at most 16 document links are allowed")
			}
			item.Documents = []Document{}
			seen := map[string]bool{}
			for _, ref := range *in.Documents {
				d, err := resolveDocumentTx(ctx, tx, room.ID, ref)
				if err != nil {
					return nil, err
				}
				if !seen[d.ID] {
					item.Documents = append(item.Documents, d)
					seen[d.ID] = true
				}
			}
		}
	}
	switch action {
	case "archive":
		item.Archived = true
	case "restore":
		item.Archived = false
	case "resolve":
		item.Status = "resolved"
	case "reopen":
		item.Status = "open"
	case "answer":
		if senderKind == "owner" || senderKind == "member" {
			item.HumanInput = false
		}
		if in.Resolve {
			item.Status = "resolved"
		}
	}
	if item.Status == "resolved" {
		item.HumanInput = false
	}
	if kind == "task" && item.Status != "backlog" && item.Status != "in-progress" && item.Status != "done" {
		return nil, errors.New("task status must be backlog, in-progress, or done")
	}
	if kind == "question" && item.Status != "open" && item.Status != "resolved" {
		return nil, errors.New("question status must be open or resolved")
	}
	if action == "answer" || action == "reply" || action == "ask-post" || action == "edit-post" {
		in.Body, err = boundedText("answer", in.Body, 1, 16384)
		if err != nil {
			return nil, err
		}
		if senderKind == "agent" || senderKind == "steward" {
			if err = ValidateSharedMessage(in.Body); err != nil {
				return nil, err
			}
		}
	}
	if action == "start" && (senderKind == "agent" || senderKind == "steward") {
		if err = ValidateSharedMessage(item.Notes); err != nil && item.Notes != "" {
			return nil, err
		}
	}
	item.Version++
	item.UpdatedAt = now
	item.UpdatedBy = handle
	candidates := append([]string{item.CreatedBy}, item.Workers...)
	if err := addBoardSubscribersTx(ctx, tx, &item, candidates); err != nil {
		return nil, err
	}
	data, err := json.Marshal(item)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO board_items(id,room_id,kind,number,status,archived,version,data) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET status=excluded.status,archived=excluded.archived,version=excluded.version,data=excluded.data`, item.ID, room.ID, kind, item.Number, item.Status, item.Archived, item.Version, string(data))
	if err != nil {
		return nil, err
	}
	var messageID any
	historyAction := action
	if action == "update" {
		fields := []string{}
		if before.Title != item.Title {
			fields = append(fields, "title")
		}
		if before.Notes != item.Notes {
			fields = append(fields, "notes")
		}
		if before.Status != item.Status {
			fields = append(fields, "status: "+before.Status+" → "+item.Status)
		}
		if !reflect.DeepEqual(before.Workers, item.Workers) {
			fields = append(fields, "assignee: "+item.Assignee)
		}
		if before.Blocker != item.Blocker {
			fields = append(fields, "blocker: "+item.Blocker)
		}
		if before.HumanInput != item.HumanInput {
			fields = append(fields, "human-input hint")
		}
		if !reflect.DeepEqual(before.Options, item.Options) {
			fields = append(fields, "answer options")
		}
		if !reflect.DeepEqual(before.Documents, item.Documents) {
			fields = append(fields, "document links")
		}
		historyAction = "updated " + strings.Join(fields, ", ")
	}
	requestedHuman := action == "update" && !before.HumanInput && item.HumanInput
	if kind == "question" && (action == "ask" || (action == "answer" && in.ReplyTo == 0) || action == "resolve" || action == "reopen" || (action == "update" && before.Status != item.Status) || requestedHuman) {
		label := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_", "`", "\\`").Replace(item.Title)
		body := "### [" + item.Key + " · " + label + "](#crewfold-question-" + item.Key + ")\n\n"
		switch action {
		case "ask":
			body += "Question raised."
			if item.HumanInput {
				body += " Human input requested; anyone may answer."
			}
			if item.Notes != "" {
				body += "\n\n" + item.Notes
			}
			if len(item.Options) > 0 {
				body += "\n\nSuggested answers (a custom answer is also welcome):"
				for _, option := range item.Options {
					body += "\n- " + option
				}
			}
		case "answer":
			body += in.Body
			if in.Resolve {
				body += "\n\nQuestion marked resolved."
				historyAction = "answered and resolved"
			}
		default:
			if requestedHuman {
				body += "Human input requested again; anyone may answer.\n\n" + item.Notes
			} else {
				body += "Question marked " + item.Status + "."
			}
		}
		message, err := insertMessage(ctx, tx, room.ID, participantID, handle, name, senderKind, "message", body, "", now)
		if err != nil {
			return nil, err
		}
		messageID = message.ID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO board_history(item_id,revision,actor,action,created_at,message_id) VALUES(?,?,?,?,?,?)`, item.ID, item.Version, handle, historyAction, now, messageID)
	if err != nil {
		return nil, err
	}
	if err := writeBoardEntriesTx(ctx, tx, before, item, action, in, handle, name, senderKind, participantID, now, messageID); err != nil {
		return nil, err
	}
	// Subscribe actual discussion contributors, including older clients that
	// create a follow-up question via question.update. Metadata edits alone
	// are not participation; editing a question post is.
	var posted bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM board_entries WHERE item_id=? AND revision=? AND json_extract(data,'$.kind') IN ('message','question','answer'))`, item.ID, item.Version).Scan(&posted); err != nil {
		return nil, err
	}
	if posted {
		if err := addBoardSubscribersTx(ctx, tx, &item, []string{handle}); err != nil {
			return nil, err
		}
		data, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE board_items SET data=? WHERE id=?`, string(data), item.ID); err != nil {
			return nil, err
		}
	}
	if err := notifyBoardSubscribersTx(ctx, tx, item, historyAction, handle, name, senderKind, participantID, now); err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE rooms SET updated_at=? WHERE id=?`, now, room.ID)
	if err != nil {
		return nil, err
	}
	detail, err := boardDetailTx(ctx, tx, item, 0)
	if err != nil {
		return nil, err
	}
	detail.OperationID = in.OperationID
	receipt := detail
	receipt.History = nil
	receipt.LatestAnswer = nil
	receipt.Entries = nil
	receipt.NextHistory = 0
	result, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO board_operations(room_id,actor_id,operation_id,request_hash,result) VALUES(?,?,?,?,?)`, room.ID, actorID, in.OperationID, digest, string(result))
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return detail, nil
}

func validateBoardInput(kind, action string, in BoardInput) error {
	if in.Assignee != nil && (kind != "task" || (action != "create" && action != "update")) {
		return errors.New("assignee is only accepted by task.create and task.update")
	}
	if action != "show" && in.EntriesBefore != 0 {
		return errors.New("entries_before is only accepted by show")
	}
	if in.ReplyTo < 0 || in.Post < 0 {
		return errors.New("post numbers cannot be negative")
	}
	if in.ReplyTo != 0 && action != "answer" && action != "reply" {
		return errors.New("reply_to requires reply or answer")
	}
	if in.Post != 0 && action != "edit-post" {
		return errors.New("post requires edit-post")
	}
	if kind == "task" && in.Resolve {
		return errors.New("resolve is only available on threads; update task status separately")
	}
	if action == "reply" || action == "ask-post" || action == "edit-post" {
		if in.Title != nil || in.Notes != nil || in.Status != nil || in.Workers != nil || in.Blocker != nil || in.Documents != nil || len(in.AddWorkers)+len(in.RemoveWorkers)+len(in.LinkDocuments) > 0 || in.Search != "" || in.FilterStatus != "" || in.Worker != "" || in.Archived || in.After != 0 || in.Limit != 0 || in.HistoryBefore != 0 || in.Resolve {
			return errors.New("post operations accept body, reply target, and question options only")
		}
		if action == "reply" && (in.Options != nil || in.HumanInput != nil) {
			return errors.New("use ask for a question with choices or requested human input")
		}
		if action == "edit-post" && in.Post == 0 {
			return errors.New("edit-post requires post number")
		}
		return nil
	}
	if in.Options != nil && (kind != "question" || (action != "ask" && action != "update")) {
		return errors.New("options are only accepted by question.ask and question.update")
	}
	if len(in.AddWorkers) > 32 || len(in.RemoveWorkers) > 32 || len(in.LinkDocuments) > 16 {
		return errors.New("too many worker or document changes")
	}
	if action != "create" && action != "start" && action != "ask" && action != "update" && len(in.AddWorkers)+len(in.RemoveWorkers)+len(in.LinkDocuments) > 0 {
		return errors.New("worker and document changes require create, ask, or update")
	}
	editable := action == "create" || action == "start" || action == "ask" || action == "update"
	if !editable && (in.Title != nil || in.Notes != nil || in.Status != nil || in.Workers != nil || in.Blocker != nil || in.HumanInput != nil || in.Documents != nil) {
		return errors.New("record fields are only accepted by create, ask, or update")
	}
	if action != "answer" && (in.Body != "" || in.Resolve) {
		return errors.New("body and resolve are only accepted by question.answer")
	}
	if kind == "task" && in.HumanInput != nil {
		return errors.New("human_input is a question field")
	}
	if kind == "question" && (in.Blocker != nil || in.Archived) {
		return errors.New("blocker and archive are task fields")
	}
	if in.FilterStatus != "" {
		valid := (kind == "task" && (in.FilterStatus == "backlog" || in.FilterStatus == "in-progress" || in.FilterStatus == "done")) || (kind == "question" && (in.FilterStatus == "open" || in.FilterStatus == "resolved"))
		if !valid {
			return errors.New("invalid status filter for this record type")
		}
	}
	if action != "list" && (in.FilterStatus != "" || in.Worker != "" || in.Search != "" || in.Archived || in.After != 0 || in.Limit != 0) {
		return errors.New("list filters are only accepted by list")
	}
	if action != "show" && in.HistoryBefore != 0 {
		return errors.New("history_before is only accepted by show")
	}
	if (action == "list" || action == "show") && (in.OperationID != "" || in.Version != 0) {
		return errors.New("operation and version are only accepted by mutations")
	}
	if len(in.Search) > 1000 {
		return errors.New("search is too long")
	}
	return nil
}
