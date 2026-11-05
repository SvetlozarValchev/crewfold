package room

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestBoardAutomaticSubscribers(t *testing.T) {
	for _, kind := range []string{"task", "thread"} {
		t.Run(kind, func(t *testing.T) {
			s, a, b := boardTestStore(t)
			ctx := context.Background()
			d := boardDo(t, s, kind+".create", BoardInput{ParticipantID: a.ID, Title: boardString("Discussion")})
			if !reflect.DeepEqual(d.Item.Subscribers, []string{a.Handle}) {
				t.Fatalf("OP not subscribed: %+v", d.Item)
			}
			if kind == "task" && d.Item.Assignee != a.Handle {
				t.Fatal("creator not assigned")
			}
			if kind == "thread" && (d.Item.Assignee != "" || len(d.Item.Workers) != 0) {
				t.Fatal("thread assigned")
			}
			d = boardDo(t, s, kind+".reply", BoardInput{ParticipantID: b.ID, Key: d.Item.Key, Body: "I can help."})
			if !reflect.DeepEqual(d.Item.Subscribers, []string{a.Handle, b.Handle}) {
				t.Fatal("participant not subscribed")
			}
			if !strings.Contains(d.History[0].Body, "For @alpha") || strings.Contains(d.History[0].Body, "@beta") {
				t.Fatal("wrong subscribers pinged or self ping")
			}
			check := func(d BoardDetail) {
				t.Helper()
				body := d.History[0].Body
				if !strings.Contains(body, "For @alpha @beta") || strings.Contains(body, "@owner") {
					t.Fatalf("bad pings: %s", body)
				}
				if len(d.Item.Subscribers) != 2 {
					t.Fatal("human subscribed")
				}
			}
			d = boardDo(t, s, kind+".update", BoardInput{Owner: true, Key: d.Item.Key, Version: d.Item.Version, Title: boardString("Renamed")})
			check(d)
			d = boardDo(t, s, kind+".ask", BoardInput{Owner: true, Key: d.Item.Key, Body: "Ready?"})
			check(d)
			post := d.Entries[len(d.Entries)-1].Number
			d = boardDo(t, s, kind+".edit-post", BoardInput{Owner: true, Key: d.Item.Key, Version: d.Item.Version, Post: post, Body: "Ready to test?"})
			check(d)
			input := BoardInput{Owner: true, Key: d.Item.Key, ReplyTo: post, Body: "Ready", OperationID: "answer-once"}
			before, _ := s.Snapshot(ctx, "shared", 0, 100)
			d = boardDo(t, s, kind+".answer", input)
			check(d)
			boardDo(t, s, kind+".answer", input)
			after, _ := s.Snapshot(ctx, "shared", 0, 100)
			if len(after.Messages) != len(before.Messages)+1 {
				t.Fatal("duplicate notification on answer/retry")
			}
			if kind == "task" {
				d = boardDo(t, s, "task.update", BoardInput{Owner: true, Key: d.Item.Key, Version: d.Item.Version, Assignee: boardString(b.Handle)})
				check(d)
				if d.Item.Assignee != b.Handle {
					t.Fatal("reassignment failed")
				}
				d = boardDo(t, s, "task.update", BoardInput{Owner: true, Key: d.Item.Key, Version: d.Item.Version, Status: boardString("done")})
				check(d)
				for _, action := range []string{"archive", "restore"} {
					d = boardDo(t, s, "task."+action, BoardInput{Owner: true, Key: d.Item.Key, Version: d.Item.Version})
					check(d)
				}
			} else {
				for _, action := range []string{"resolve", "reopen"} {
					d = boardDo(t, s, "thread."+action, BoardInput{Owner: true, Key: d.Item.Key, Version: d.Item.Version})
					check(d)
				}
			}
		})
	}
}

func TestBoardReassignmentRetainsSubscriberAndRejectsInvalidAssignments(t *testing.T) {
	s, a, b := boardTestStore(t)
	d := boardDo(t, s, "task.create", BoardInput{ParticipantID: a.ID, Title: boardString("Task")})
	d = boardDo(t, s, "task.update", BoardInput{Owner: true, Key: d.Item.Key, Version: d.Item.Version, Assignee: boardString(b.Handle)})
	d = boardDo(t, s, "task.reply", BoardInput{Owner: true, Key: d.Item.Key, Body: "Both still following?"})
	if !strings.Contains(d.History[0].Body, "For @alpha @beta") {
		t.Fatal("former assignee lost subscription")
	}
	for _, workers := range [][]string{{a.Handle, b.Handle}, {}, {"unknown"}} {
		_, err := s.Board(context.Background(), "task.update", BoardInput{Room: "shared", Owner: true, Key: d.Item.Key, Version: d.Item.Version, Workers: &workers, OperationID: "bad"})
		if err == nil {
			t.Fatal("invalid assignment accepted")
		}
	}
	_, err := s.Board(context.Background(), "thread.create", BoardInput{Room: "shared", Owner: true, Title: boardString("Thread"), Workers: &[]string{a.Handle}, OperationID: "thread-bad"})
	if err == nil {
		t.Fatal("assignable thread")
	}
}

func TestBoardQuestionAuthorsAndAnswerersSubscribe(t *testing.T) {
	s, a, b := boardTestStore(t)
	ctx := context.Background()
	d := boardDo(t, s, "question.ask", BoardInput{Owner: true, Title: boardString("Owner question")})
	if len(d.Item.Subscribers) != 0 {
		t.Fatal("human OP subscribed")
	}
	d = boardDo(t, s, "question.answer", BoardInput{Owner: true, Key: d.Item.Key, Body: "First choice."})
	// Legacy agents can still ask a follow-up through question.update.
	d = boardDo(t, s, "question.update", BoardInput{ParticipantID: a.ID, Key: d.Item.Key, Version: d.Item.Version, HumanInput: boardBool(true), Notes: boardString("Follow-up?")})
	if !reflect.DeepEqual(d.Item.Subscribers, []string{a.Handle}) {
		t.Fatal("legacy question author not subscribed")
	}
	in := BoardInput{ParticipantID: b.ID, Key: d.Item.Key, Body: "I suggest yes.", OperationID: "subscriber-retry"}
	d = boardDo(t, s, "question.answer", in)
	if !reflect.DeepEqual(d.Item.Subscribers, []string{a.Handle, b.Handle}) {
		t.Fatal("answerer not subscribed")
	}
	if !strings.Contains(d.History[0].Body, "For @alpha") || strings.Contains(d.History[0].Body, "@beta") {
		t.Fatal("answerer pinged itself")
	}
	d = boardDo(t, s, "question.answer", BoardInput{Owner: true, Key: d.Item.Key, Body: "Yes."})
	if !strings.Contains(d.History[0].Body, "For @alpha @beta") {
		t.Fatal("answer omitted subscribers")
	}
	before, _ := s.Snapshot(ctx, "shared", 0, 100)
	replay := boardDo(t, s, "question.answer", in)
	after, _ := s.Snapshot(ctx, "shared", 0, 100)
	if replay.Item.Version != d.Item.Version-1 || before.Room.LastSequence != after.Room.LastSequence {
		t.Fatal("retry changed state or republished")
	}
}

func TestBoardSubscriptionReceiptDoesNotPickUpLaterAssignees(t *testing.T) {
	s, a, b := boardTestStore(t)
	in := BoardInput{ParticipantID: a.ID, Title: boardString("Task"), OperationID: "create-once"}
	d := boardDo(t, s, "task.create", in)
	boardDo(t, s, "task.update", BoardInput{Owner: true, Key: d.Item.Key, Version: d.Item.Version, Assignee: boardString(b.Handle)})
	replay := boardDo(t, s, "task.create", in)
	if replay.Item.Assignee != a.Handle || !reflect.DeepEqual(replay.Item.Subscribers, []string{a.Handle}) {
		t.Fatal("retry acquired later assignee/subscriber state")
	}
}

func TestBoardSubscriptionMigration(t *testing.T) {
	s, a, b := boardTestStore(t)
	ctx := context.Background()
	for _, kind := range []string{"task", "thread"} {
		d := boardDo(t, s, kind+".create", BoardInput{ParticipantID: a.ID, Title: boardString("Old record")})
		d = boardDo(t, s, kind+".reply", BoardInput{ParticipantID: b.ID, Key: d.Item.Key, Body: "Old participant."})
		d = boardDo(t, s, kind+".reply", BoardInput{Owner: true, Key: d.Item.Key, Body: "Human participation."})
		d.Item.Subscribers = nil
		d.Item.Assignee = ""
		d.Item.Workers = []string{b.Handle, a.Handle}
		if kind == "thread" {
			d.Item.Workers = []string{}
		}
		raw, _ := json.Marshal(d.Item)
		if _, err := s.db.ExecContext(ctx, "UPDATE board_items SET data=? WHERE id=?", string(raw), d.Item.ID); err != nil {
			t.Fatal(err)
		}
		before, _ := s.Snapshot(ctx, "shared", 0, 100)
		for range 2 {
			if err := s.initializeBoardSubscriptions(ctx); err != nil {
				t.Fatal(err)
			}
		}
		result, err := s.Board(ctx, kind+".show", BoardInput{Room: "shared", Key: d.Item.Key})
		if err != nil {
			t.Fatal(err)
		}
		item := result.(BoardDetail).Item
		if !reflect.DeepEqual(item.Subscribers, []string{a.Handle, b.Handle}) || item.Version != d.Item.Version {
			t.Fatalf("bad migration: %+v", item)
		}
		if kind == "task" && (item.Assignee != b.Handle || len(item.Workers) != 1) {
			t.Fatal("legacy task not normalized")
		}
		if kind == "thread" && len(item.Workers) != 0 {
			t.Fatal("legacy thread still assignable")
		}
		after, _ := s.Snapshot(ctx, "shared", 0, 100)
		if len(before.Messages) != len(after.Messages) {
			t.Fatal("migration emitted pings")
		}
	}
}
