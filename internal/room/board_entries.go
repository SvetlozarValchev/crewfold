package room

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Entries are versioned snapshots. Answers reference a stable per-record post
// number, never the latest question or the record's mutable description.
type BoardEntry struct {
	Number     int64    `json:"number"`
	Revision   int64    `json:"revision"`
	Kind       string   `json:"kind"`
	Actor      string   `json:"actor"`
	ActorKind  string   `json:"actor_kind"`
	CreatedAt  string   `json:"created_at"`
	Title      string   `json:"title,omitempty"`
	Body       string   `json:"body"`
	Options    []string `json:"options"`
	HumanInput bool     `json:"human_input"`
	ReplyTo    int64    `json:"reply_to,omitempty"`
	Sequence   int64    `json:"sequence,omitempty"`
	Answered   bool     `json:"answered"`
	Legacy     bool     `json:"legacy,omitempty"`
}

func boardEntriesTx(ctx context.Context, tx *sql.Tx, item BoardItem) ([]BoardEntry, error) {
	rows, err := tx.QueryContext(ctx, `SELECT data FROM board_entries e WHERE item_id=? AND revision<=? AND revision=(SELECT MAX(revision) FROM board_entries v WHERE v.item_id=e.item_id AND v.number=e.number AND v.revision<=?) ORDER BY number`, item.ID, item.Version, item.Version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	entries := []BoardEntry{}
	for rows.Next() {
		var raw string
		var e BoardEntry
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &e); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	for i := range entries {
		if entries[i].Kind != "question" {
			continue
		}
		for _, a := range entries {
			if a.Kind == "answer" && a.ReplyTo == entries[i].Number && (!entries[i].HumanInput || a.ActorKind == "owner" || a.ActorKind == "member") {
				entries[i].Answered = true
			}
		}
	}
	return entries, nil
}

func saveBoardEntryTx(ctx context.Context, tx *sql.Tx, itemID string, e BoardEntry) error {
	e.Answered = false
	if e.Options == nil {
		e.Options = []string{}
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO board_entries(item_id,number,revision,data) VALUES(?,?,?,?) ON CONFLICT(item_id,number,revision) DO UPDATE SET data=excluded.data`, itemID, e.Number, e.Revision, string(raw))
	return err
}

func boardEntryStateTx(ctx context.Context, tx *sql.Tx, item *BoardItem) error {
	var migrated int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM board_entry_migrations WHERE item_id=?`, item.ID).Scan(&migrated); err != nil {
		return err
	}
	if migrated == 0 {
		return legacyBoardQuestionStateTx(ctx, tx, item)
	}
	entries, err := boardEntriesTx(ctx, tx, *item)
	if err != nil {
		return err
	}
	item.HumanInput = false
	item.AnswerCount = 0
	item.LastAnswerBy = ""
	item.PendingQuestions = 0
	item.EntryCount = len(entries)
	item.Preview = ""
	for _, e := range entries {
		if e.Kind == "answer" {
			item.AnswerCount++
			item.LastAnswerBy = e.Actor
		}
		if e.Kind == "question" && !e.Answered && item.Status != "resolved" {
			item.PendingQuestions++
			if e.HumanInput {
				item.HumanInput = true
			}
		}
		if e.Kind != "resolved" && e.Kind != "reopened" {
			item.Preview = e.Body
			if item.Preview == "" {
				item.Preview = e.Title
			}
		}
	}
	if len([]rune(item.Preview)) > 160 {
		item.Preview = string([]rune(item.Preview)[:160]) + "…"
	}
	return nil
}

func (s *Store) initializeBoardEntries(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS board_entries(item_id TEXT NOT NULL REFERENCES board_items(id) ON DELETE CASCADE,number INTEGER NOT NULL,revision INTEGER NOT NULL,data TEXT NOT NULL,PRIMARY KEY(item_id,number,revision)) STRICT;
 CREATE TABLE IF NOT EXISTS board_entry_migrations(item_id TEXT PRIMARY KEY REFERENCES board_items(id) ON DELETE CASCADE) STRICT;`)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT data FROM board_items WHERE id NOT IN (SELECT item_id FROM board_entry_migrations)`)
	if err != nil {
		return err
	}
	items := []BoardItem{}
	for rows.Next() {
		var raw string
		var item BoardItem
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal([]byte(raw), &item); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		if err = migrateBoardEntriesTx(ctx, tx, item); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func eventBody(body string) string {
	if at := strings.LastIndex(body, "\n\nFor @"); at >= 0 && (strings.HasSuffix(body, " · subscribed to this discussion; shared with the room.") || strings.HasSuffix(body, " · task/thread activity; shared with the room.")) {
		body = body[:at]
	}
	if strings.HasPrefix(body, "### [") {
		if _, rest, ok := strings.Cut(body, "\n\n"); ok {
			return rest
		}
	}
	return body
}

// Preserve actual historical text. Old option edits were not snapshotted, so
// never present today's choices as if they were the choices of an old answer.
func migrateBoardEntriesTx(ctx context.Context, tx *sql.Tx, item BoardItem) error {
	if item.Kind == "question" {
		rows, err := tx.QueryContext(ctx, `SELECT h.revision,h.actor,h.action,h.created_at,COALESCE(m.sequence,0),COALESCE(m.body,''),COALESCE(m.sender_kind,'agent') FROM board_history h LEFT JOIN messages m ON m.id=h.message_id WHERE h.item_id=? ORDER BY h.revision`, item.ID)
		if err != nil {
			return err
		}
		entries := []BoardEntry{}
		var question int64
		for rows.Next() {
			var revision, seq int64
			var actor, action, date, body, sender string
			if err = rows.Scan(&revision, &actor, &action, &date, &seq, &body, &sender); err != nil {
				rows.Close()
				return err
			}
			if body == "" {
				continue
			}
			e := BoardEntry{Number: int64(len(entries) + 1), Revision: revision, Actor: actor, ActorKind: sender, CreatedAt: date, Body: eventBody(body), Sequence: seq, Options: []string{}, Legacy: true}
			switch {
			case action == "ask" || strings.Contains(e.Body, "Human input requested again;"):
				e.Kind = "question"
				question = e.Number
				e.Title = item.Title
			case action == "answer" || action == "answered and resolved":
				e.Kind = "answer"
				e.ReplyTo = question
			case action == "resolve":
				e.Kind = "resolved"
			case action == "reopen":
				e.Kind = "reopened"
			default:
				e.Kind = "message"
			}
			entries = append(entries, e)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err = saveBoardEntryTx(ctx, tx, item.ID, e); err != nil {
				return err
			}
		}
		effective := item
		if err = legacyBoardQuestionStateTx(ctx, tx, &effective); err != nil {
			return err
		}
		// Only the currently unanswered prompt may use the current editable fields.
		for i := len(entries) - 1; i >= 0; i-- {
			e := entries[i]
			if e.Kind != "question" {
				continue
			}
			answered := false
			for _, a := range entries {
				if a.Kind == "answer" && a.ReplyTo == e.Number {
					answered = true
				}
			}
			if !answered || effective.HumanInput {
				e.Revision = item.Version
				e.Title = item.Title
				e.Body = item.Notes
				e.Options = item.Options
				e.HumanInput = effective.HumanInput
				if err = saveBoardEntryTx(ctx, tx, item.ID, e); err != nil {
					return err
				}
			}
			break
		}
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO board_entry_migrations(item_id) VALUES(?)`, item.ID)
	return err
}

func checkedOptions(options []string) ([]string, error) {
	if len(options) > 8 {
		return nil, errors.New("questions support at most 8 answer options")
	}
	out := []string{}
	seen := map[string]bool{}
	for _, option := range options {
		v, err := boundedText("answer option", option, 1, 240)
		if err != nil {
			return nil, err
		}
		if strings.ContainsAny(v, "\r\n") {
			return nil, errors.New("answer options must be single lines")
		}
		key := strings.ToLower(v)
		if seen[key] {
			return nil, errors.New("answer options must be distinct")
		}
		seen[key] = true
		out = append(out, v)
	}
	return out, nil
}

func writeBoardEntriesTx(ctx context.Context, tx *sql.Tx, before, item BoardItem, action string, in BoardInput, actor, name, actorKind string, participantID *string, now string, existingMessage any) error {
	entries, err := boardEntriesTx(ctx, tx, before)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO board_entry_migrations(item_id) VALUES(?)`, item.ID)
	if err != nil {
		return err
	}
	number := int64(1)
	if len(entries) > 0 {
		number = entries[len(entries)-1].Number + 1
	}
	e := BoardEntry{Number: number, Revision: item.Version, Actor: actor, ActorKind: actorKind, CreatedAt: now, Options: []string{}}
	var pending []BoardEntry
	for _, p := range entries {
		if p.Kind == "question" && !p.Answered {
			pending = append(pending, p)
		}
	}
	emit := false
	switch action {
	case "create":
		return nil
	case "start":
		e.Kind = "message"
		e.Body = item.Notes
		if e.Body == "" {
			e.Body = item.Title
		}
		emit = true
	case "ask":
		e.Kind = "question"
		e.Title = item.Title
		e.Body = item.Notes
		e.Options = item.Options
		e.HumanInput = item.HumanInput
	case "ask-post":
		e.Kind = "question"
		e.Body = in.Body
		if in.Options != nil {
			e.Options = *in.Options
		}
		e.HumanInput = in.HumanInput != nil && *in.HumanInput
		emit = true
	case "reply", "answer":
		e.Kind = "message"
		e.Body = in.Body
		e.ReplyTo = in.ReplyTo
		emit = true
		if action == "answer" && e.ReplyTo == 0 {
			if len(pending) > 1 {
				return errors.New("multiple pending questions; specify --to P-N")
			}
			if len(pending) == 1 {
				e.ReplyTo = pending[0].Number
			} else {
				for i := len(entries) - 1; i >= 0; i-- {
					if entries[i].Kind == "question" {
						e.ReplyTo = entries[i].Number
						break
					}
				}
			}
		}
		if e.ReplyTo != 0 {
			found := false
			for _, p := range entries {
				if p.Number == e.ReplyTo && p.Kind == "question" {
					found = true
				}
			}
			if !found {
				return errors.New("reply target is not a question in this record")
			}
			e.Kind = "answer"
		}
	case "edit-post":
		found := false
		for _, p := range entries {
			if p.Number == in.Post {
				e = p
				found = true
				break
			}
		}
		if !found {
			return errors.New("post not found in this record")
		}
		for _, p := range entries {
			if p.ReplyTo == e.Number {
				return errors.New("answered questions are read-only; ask a follow-up with fresh options")
			}
		}
		if e.Kind != "question" {
			return errors.New("only unanswered question posts may be edited")
		}
		e.Revision = item.Version
		e.Body = in.Body
		if in.Options != nil {
			e.Options = *in.Options
		}
		if in.HumanInput != nil {
			e.HumanInput = *in.HumanInput
		}
		emit = true
	case "resolve", "reopen":
		if action == "resolve" {
			e.Kind = "resolved"
		} else {
			e.Kind = "reopened"
		}
		e.Body = "Thread " + e.Kind + "."
	case "update":
		if item.Kind != "question" {
			return nil
		}
		if in.Status != nil && before.Status != item.Status {
			if item.Status == "resolved" {
				e.Kind = "resolved"
			} else {
				e.Kind = "reopened"
			}
			e.Body = "Thread " + e.Kind + "."
			break
		}
		// Compatibility for older agents: update edits the current unanswered
		// question, or starts a fresh question when input/options are requested.
		if in.Notes == nil && in.Options == nil && in.HumanInput == nil {
			return nil
		}
		if len(pending) > 1 {
			return errors.New("multiple pending questions; use edit-post --post P-N")
		}
		if len(pending) == 1 {
			e = pending[0]
			for _, answer := range entries {
				if answer.ReplyTo == e.Number && (in.Notes != nil || in.Options != nil) {
					return errors.New("answered questions are read-only; ask a follow-up with fresh options")
				}
			}
			e.Revision = item.Version
			if in.Title != nil {
				e.Title = *in.Title
			}
			if in.Notes != nil {
				e.Body = *in.Notes
			}
			if in.Options != nil {
				e.Options = *in.Options
			}
			if in.HumanInput != nil {
				e.HumanInput = *in.HumanInput
			}
		}
		if len(pending) == 0 {
			if in.Options == nil && (in.HumanInput == nil || !*in.HumanInput) {
				return nil
			}
			e.Kind = "question"
			e.Title = item.Title
			e.Body = item.Notes
			if in.Options != nil {
				e.Options = *in.Options
			}
			e.HumanInput = in.HumanInput != nil && *in.HumanInput
		}
	default:
		return nil
	}
	if e.Kind == "question" {
		e.Options, err = checkedOptions(e.Options)
		if err != nil {
			return err
		}
	}
	if existingMessage != nil {
		if err = tx.QueryRowContext(ctx, `SELECT sequence FROM messages WHERE id=?`, existingMessage).Scan(&e.Sequence); err != nil {
			return err
		}
	}
	if emit && existingMessage == nil {
		label := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_", "`", "\\`").Replace(item.Title)
		body := "### [" + item.Key + " · " + label + "](#crewfold-record-" + item.Key + ")\n\n" + e.Body
		if e.ReplyTo != 0 {
			body = "### [" + item.Key + " · " + label + "](#crewfold-record-" + item.Key + ")\n\nIn reply to P-" + fmt.Sprint(e.ReplyTo) + ":\n\n" + e.Body
		}
		if e.Kind == "question" {
			if e.HumanInput {
				body += "\n\nHuman input requested; anyone may answer."
			}
			if len(e.Options) > 0 {
				body += "\n\nSuggested answers (a custom answer is also welcome):"
				for _, o := range e.Options {
					body += "\n- " + o
				}
			}
		}
		if action == "edit-post" {
			body += "\n\nUpdated question P-" + fmt.Sprint(e.Number) + "."
		}
		message, err := insertMessage(ctx, tx, item.RoomID, participantID, actor, name, actorKind, "message", body, "", now)
		if err != nil {
			return err
		}
		e.Sequence = message.Sequence
		historyAction := action
		if e.Kind == "answer" {
			historyAction = "answer"
		}
		if _, err = tx.ExecContext(ctx, `UPDATE board_history SET message_id=?,action=? WHERE item_id=? AND revision=?`, message.ID, historyAction, item.ID, item.Version); err != nil {
			return err
		}
	}
	if err := saveBoardEntryTx(ctx, tx, item.ID, e); err != nil {
		return err
	}
	if action == "answer" && in.Resolve {
		e.Number++
		e.Kind, e.Body, e.ReplyTo, e.Options = "resolved", "Thread resolved.", 0, []string{}
		return saveBoardEntryTx(ctx, tx, item.ID, e)
	}
	return nil
}
