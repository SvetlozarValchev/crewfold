package room

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestBoardThreadQuestionRoundsAndTaskDiscussion(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"thread", "task"} {
		t.Run(kind, func(t *testing.T) {
			s, a, b := boardTestStore(t)
			d := boardDo(t, s, kind+".create", BoardInput{ParticipantID: a.ID, Title: boardString("Try the lean planner"), Notes: boardString("Keep the separate description.")})
			initial := len(d.Entries)
			if (kind == "task" && initial != 0) || (kind == "thread" && initial != 1) {
				t.Fatal("initial post/description mismatch")
			}
			options := []string{"Existing pair", "New areas"}
			d = boardDo(t, s, kind+".ask", BoardInput{ParticipantID: b.ID, Key: d.Item.Key, Body: "Which areas?", Options: &options, HumanInput: boardBool(true)})
			question := d.Entries[len(d.Entries)-1]
			if !d.Item.HumanInput || d.Item.PendingQuestions != 1 || question.Kind != "question" {
				t.Fatalf("missing pending question: %+v", d)
			}
			opts2 := []string{"Use existing pair", "Try coastal"}
			d = boardDo(t, s, kind+".edit-post", BoardInput{ParticipantID: a.ID, Key: d.Item.Key, Post: question.Number, Version: d.Item.Version, Body: "Which two areas?", Options: &opts2})
			if len(d.Entries) != initial+1 || d.Entries[initial].Body != "Which two areas?" {
				t.Fatal("edit duplicated the question")
			}
			d = boardDo(t, s, kind+".answer", BoardInput{ParticipantID: a.ID, Key: d.Item.Key, ReplyTo: question.Number, Body: "I suggest the existing pair."})
			if !d.Item.HumanInput {
				t.Fatal("agent suggestion cleared human request")
			}
			answer := BoardInput{Owner: true, Key: d.Item.Key, ReplyTo: question.Number, Body: "Use existing pair", OperationID: "answer-once"}
			d = boardDo(t, s, kind+".answer", answer)
			if d.Item.HumanInput || d.Item.PendingQuestions != 0 || !d.Entries[initial].Answered {
				t.Fatal("human answer did not settle this question")
			}
			revision := d.Item.Version
			d = boardDo(t, s, kind+".reply", BoardInput{ParticipantID: b.ID, Key: d.Item.Key, Body: "Trial is running."})
			if d.Entries[len(d.Entries)-1].Kind != "message" || d.Item.PendingQuestions != 0 {
				t.Fatal("update became a question")
			}
			d = boardDo(t, s, kind+".ask", BoardInput{ParticipantID: a.ID, Key: d.Item.Key, Body: "What next?"})
			next := d.Entries[len(d.Entries)-1]
			if len(next.Options) != 0 || next.HumanInput || d.Item.PendingQuestions != 1 {
				t.Fatal("new round inherited previous choices/hint")
			}
			replay := boardDo(t, s, kind+".answer", answer)
			if replay.Item.Version != revision || replay.Item.PendingQuestions != 0 {
				t.Fatal("retry receipt included future rounds")
			}
			if kind == "task" && d.Item.Notes != "Keep the separate description." {
				t.Fatal("discussion overwrote task description")
			}
			for _, test := range []BoardInput{
				{Owner: true, Post: question.Number, Body: "Rewrite an answered prompt", Version: d.Item.Version},
				{Owner: true, Post: 999, Body: "Wrong post", Version: d.Item.Version},
			} {
				test.Room, test.Key, test.OperationID = "shared", d.Item.Key, "bad-"+fmt.Sprint(test.Post)
				if _, err := s.Board(context.Background(), kind+".edit-post", test); err == nil {
					t.Fatal("edited answered/absent post")
				}
			}
			_, err := s.Board(context.Background(), kind+".answer", BoardInput{Room: "shared", Owner: true, Key: d.Item.Key, Body: "Wrong target", ReplyTo: 999, OperationID: "bad-target"})
			if err == nil {
				t.Fatal("accepted unrelated target")
			}
			snapshot, _ := s.Snapshot(context.Background(), "shared", 0, 100)
			var count int
			for _, m := range snapshot.Messages {
				if strings.Contains(m.Body, "In reply to P-") && strings.HasSuffix(eventBody(m.Body), "Use existing pair") {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("answer published %d times", count)
			}
			if kind == "thread" {
				d = boardDo(t, s, "thread.answer", BoardInput{Owner: true, Key: d.Item.Key, ReplyTo: next.Number, Body: "Finish", Resolve: true, Version: d.Item.Version})
				if d.Entries[len(d.Entries)-1].Kind != "resolved" || d.Item.PendingQuestions != 0 {
					t.Fatal("missing resolution event")
				}
				_, err = s.Board(context.Background(), "thread.reply", BoardInput{Room: "shared", Owner: true, Key: d.Item.Key, Body: "Late", OperationID: "late"})
				if err == nil {
					t.Fatal("resolved thread accepted reply")
				}
				d = boardDo(t, s, "thread.reopen", BoardInput{Owner: true, Key: d.Item.Key, Version: d.Item.Version})
				if d.Entries[len(d.Entries)-1].Kind != "reopened" {
					t.Fatal("missing reopen")
				}
			}
		})
	}
}

func TestBoardEntriesPaginationAndPendingQuestions(t *testing.T) {
	t.Parallel()
	s, a, _ := boardTestStore(t)
	d := boardDo(t, s, "thread.create", BoardInput{ParticipantID: a.ID, Title: boardString("Long discussion")})
	d = boardDo(t, s, "thread.ask", BoardInput{ParticipantID: a.ID, Key: d.Item.Key, Body: "Decision?", HumanInput: boardBool(true)})
	for i := 0; i < 55; i++ {
		d = boardDo(t, s, "thread.reply", BoardInput{ParticipantID: a.ID, Key: d.Item.Key, Body: fmt.Sprintf("Update %d", i)})
	}
	if len(d.Entries) != 50 || d.NextEntries == 0 || len(d.PendingEntries) != 1 || d.PendingEntries[0].Number != 2 {
		t.Fatal("pagination lost pending prompt")
	}
	v, err := s.Board(context.Background(), "thread.show", BoardInput{Room: "shared", Key: d.Item.Key, EntriesBefore: d.NextEntries})
	if err != nil {
		t.Fatal(err)
	}
	older := v.(BoardDetail)
	if len(older.Entries) != 7 || older.NextEntries != 0 || older.Entries[0].Number != 1 {
		t.Fatal("older page mismatch")
	}
	page, err := s.Board(context.Background(), "thread.list", BoardInput{Room: "shared", Limit: 1})
	if err != nil || page.(BoardPage).Total != 1 {
		t.Fatal("missing accurate total")
	}
}

func TestBoardDoneUsesRecentOrderAndScopedCursor(t *testing.T) {
	t.Parallel()
	s, a, _ := boardTestStore(t)
	ctx := context.Background()
	first := boardDo(t, s, "task.create", BoardInput{ParticipantID: a.ID, Title: boardString("First"), Status: boardString("done")})
	second := boardDo(t, s, "task.create", BoardInput{ParticipantID: a.ID, Title: boardString("Second"), Status: boardString("done")})
	first = boardDo(t, s, "task.update", BoardInput{ParticipantID: a.ID, Key: first.Item.Key, Version: first.Item.Version, Notes: boardString("Recently verified")})
	got, err := s.Board(ctx, "task.list", BoardInput{Room: "shared", FilterStatus: "done", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	page := got.(BoardPage)
	if page.Total != 2 || len(page.Items) != 1 || page.Items[0].ID != first.Item.ID || page.Next != first.Item.Number {
		t.Fatalf("wrong recent page: %+v", page)
	}
	got, err = s.Board(ctx, "task.list", BoardInput{Room: "shared", FilterStatus: "done", Limit: 1, After: page.Next})
	if err != nil {
		t.Fatal(err)
	}
	page = got.(BoardPage)
	if page.Total != 2 || page.Next != 0 || len(page.Items) != 1 || page.Items[0].ID != second.Item.ID {
		t.Fatalf("wrong cursor page: %+v", page)
	}
}

func TestBoardLegacyMigrationPreservesHistoryAndDoesNotPublish(t *testing.T) {
	t.Parallel()
	s, a, _ := boardTestStore(t)
	ctx := context.Background()
	options := []string{"A", "B"}
	answered := boardDo(t, s, "question.ask", BoardInput{ParticipantID: a.ID, Title: boardString("Old choice"), Notes: boardString("Original prompt."), Options: &options, HumanInput: boardBool(true)})
	answered = boardDo(t, s, "question.answer", BoardInput{Owner: true, Key: answered.Item.Key, Body: "A"})
	pending := boardDo(t, s, "question.ask", BoardInput{ParticipantID: a.ID, Title: boardString("Pending choice"), Options: &options, HumanInput: boardBool(true)})
	task := boardDo(t, s, "task.create", BoardInput{ParticipantID: a.ID, Title: boardString("Old task"), Notes: boardString("Description survives")})
	// Reproduce the pre-thread database: canonical events + current records only.
	if _, err := s.db.ExecContext(ctx, "DELETE FROM board_entries; DELETE FROM board_entry_migrations"); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Snapshot(ctx, "shared", 0, 100)
	for i := 0; i < 2; i++ {
		if err := s.initializeBoardEntries(ctx); err != nil {
			t.Fatal(err)
		}
	}
	after, _ := s.Snapshot(ctx, "shared", 0, 100)
	if len(before.Messages) != len(after.Messages) {
		t.Fatal("migration published messages")
	}
	for _, key := range []string{answered.Item.Key, pending.Item.Key, task.Item.Key} {
		method := "thread.show"
		if strings.HasPrefix(key, "T-") {
			method = "task.show"
		}
		value, err := s.Board(ctx, method, BoardInput{Room: "shared", Key: key})
		if err != nil {
			t.Fatal(err)
		}
		got := value.(BoardDetail)
		switch key {
		case answered.Item.Key:
			if got.Item.HumanInput || len(got.Entries) != 2 || got.Entries[1].ReplyTo != 1 || got.Entries[1].Body != "A" || len(got.Entries[0].Options) != 0 {
				t.Fatalf("migration rewrote answered choices: %+v", got)
			}
		case pending.Item.Key:
			if !got.Item.HumanInput || len(got.Entries) != 1 || len(got.Entries[0].Options) != 2 {
				t.Fatal("pending choices lost")
			}
		case task.Item.Key:
			if len(got.Entries) != 0 || got.Item.Notes != "Description survives" {
				t.Fatal("task description became comment")
			}
		}
	}
}
