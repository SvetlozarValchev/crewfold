package room

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
)

// Subscription is an attention hint, never a delivery/privacy boundary. Add the
// mentions to the same canonical event, or create one for an otherwise quiet
// metadata edit. Reassignment includes both sides; operation retries return
// before this function, so they cannot ping twice.
func notifyBoardSubscribersTx(ctx context.Context, tx *sql.Tx, item BoardItem, action, actor, name, actorKind string, participantID *string, now string) error {
	seen := map[string]bool{}
	mentions := []string{}
	for _, subscriber := range item.Subscribers {
		if subscriber != "" && subscriber != actor && !seen[subscriber] {
			seen[subscriber] = true
			mentions = append(mentions, "@"+subscriber)
		}
	}
	if len(mentions) == 0 {
		return nil
	}
	suffix := "\n\nFor " + strings.Join(mentions, " ") + " · subscribed to this discussion; shared with the room."
	var messageID sql.NullString
	if err := tx.QueryRowContext(ctx, "SELECT message_id FROM board_history WHERE item_id=? AND revision=?", item.ID, item.Version).Scan(&messageID); err != nil {
		return err
	}
	if messageID.Valid {
		_, err := tx.ExecContext(ctx, "UPDATE messages SET body=body||? WHERE id=?", suffix, messageID.String)
		return err
	}
	label := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]", "*", "\\*", "_", "\\_", "`", "\\`").Replace(item.Title)
	kind := "Task"
	if item.Kind == "question" {
		kind = "Thread"
	}
	verb := strings.TrimSpace(action)
	switch verb {
	case "create", "start":
		verb = "created"
	case "archive":
		verb = "archived"
	case "restore":
		verb = "restored"
	}
	body := "### [" + item.Key + " · " + label + "](#crewfold-record-" + item.Key + ")\n\n" + kind + " " + verb + ".\n\nStatus: " + item.Status + "." + suffix
	message, err := insertMessage(ctx, tx, item.RoomID, participantID, actor, name, actorKind, "message", body, "", now)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE board_history SET message_id=? WHERE item_id=? AND revision=?", message.ID, item.ID, item.Version)
	return err
}

// Humans may own tasks and participate, but only room agents receive pings.
// Subscriptions are append-only and snapshotted with the record for safe retries.
func addBoardSubscribersTx(ctx context.Context, tx *sql.Tx, item *BoardItem, candidates []string) error {
	if item.Subscribers == nil {
		item.Subscribers = []string{}
	}
	seen := map[string]bool{}
	for _, handle := range item.Subscribers {
		seen[handle] = true
	}
	for _, handle := range candidates {
		if handle == "" || seen[handle] {
			continue
		}
		var count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM participants WHERE room_id=? AND handle=? AND kind IN ('agent','steward')`, item.RoomID, handle).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			item.Subscribers = append(item.Subscribers, handle)
			seen[handle] = true
		}
	}
	return nil
}

// Backfill without emitting activity or altering revision numbers. Legacy
// multi-worker tasks keep the first worker as assignee; all remain subscribed.
func (s *Store) initializeBoardSubscriptions(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT data FROM board_items WHERE json_type(data,'$.subscribers') IS NULL OR json_type(data,'$.subscribers')='null'`)
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
		candidates := append([]string{item.CreatedBy}, item.Workers...)
		entries, err := boardEntriesTx(ctx, tx, item)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if e.Kind == "message" || e.Kind == "question" || e.Kind == "answer" {
				candidates = append(candidates, e.Actor)
			}
		}
		// Old assignment edits recorded handles in their action text. Recover
		// those too, including former workers no longer on the current list.
		history, err := tx.QueryContext(ctx, `SELECT action FROM board_history WHERE item_id=? ORDER BY revision`, item.ID)
		if err != nil {
			return err
		}
		for history.Next() {
			var action string
			if err = history.Scan(&action); err != nil {
				history.Close()
				return err
			}
			if _, rest, ok := strings.Cut(action, "workers: "); ok {
				for _, part := range strings.Split(rest, ", ") {
					if strings.ContainsAny(part, " :") {
						break
					}
					candidates = append(candidates, part)
				}
			}
		}
		err = history.Err()
		history.Close()
		if err != nil {
			return err
		}
		if err = addBoardSubscribersTx(ctx, tx, &item, candidates); err != nil {
			return err
		}
		if item.Kind == "task" {
			item.Assignee = item.CreatedBy
			if len(item.Workers) > 0 {
				item.Assignee = item.Workers[0]
			}
			item.Workers = []string{item.Assignee}
		} else {
			item.Assignee = ""
			item.Workers = []string{}
		}
		raw, err := json.Marshal(item)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE board_items SET data=? WHERE id=?`, string(raw), item.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
