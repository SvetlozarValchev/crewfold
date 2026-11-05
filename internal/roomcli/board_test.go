package roomcli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"crewfold/internal/buildinfo"
	"crewfold/internal/room"
)

func TestBoardCLISharedCommandsAndExpectedRevision(t *testing.T) {
	ctx := context.Background()
	var out, stderr bytes.Buffer
	app := New(&out, &stderr, buildinfo.Info{})
	var calls []string
	var mutation room.BoardInput
	client := callerFunc(func(_ context.Context, method string, input, output any) error {
		calls = append(calls, method)
		in := input.(room.BoardInput)
		if roomIdentifierFromParams(in) != "shared" {
			t.Fatal("connector lost room routing")
		}
		if method == "task.show" {
			*output.(*room.BoardDetail) = room.BoardDetail{Item: room.BoardItem{Key: "T-2", Version: 4, Workers: []string{"alpha"}, Documents: []room.Document{{ID: "doc-old"}}}}
			return nil
		}
		mutation = in
		*output.(*room.BoardDetail) = room.BoardDetail{Item: room.BoardItem{Key: "T-2", Version: 5, Status: "in-progress"}, OperationID: in.OperationID}
		return nil
	})
	app.stdin = strings.NewReader("## Outcome\n\nShort shared notes.\n")
	code := app.board(ctx, client, "task", []string{"update", "shared", "T-2", "--stdin", "--status", "in-progress", "--add-worker", "@beta", "--remove-worker", "alpha", "--link-document", "report.md"}, false)
	if code != 0 {
		t.Fatal(stderr.String())
	}
	if strings.Join(calls, ",") != "task.show,task.update" || mutation.Version != 4 || mutation.OperationID == "" || mutation.Owner || mutation.WorkingDirectory == "" {
		t.Fatalf("bad mutation: %+v calls %v", mutation, calls)
	}
	if mutation.Workers != nil || len(mutation.AddWorkers) != 1 || mutation.AddWorkers[0] != "@beta" || len(mutation.RemoveWorkers) != 1 {
		t.Fatalf("worker deltas: %+v", mutation)
	}
	if mutation.Documents != nil || len(mutation.LinkDocuments) != 1 || mutation.LinkDocuments[0] != "report.md" || mutation.Notes == nil {
		t.Fatal("lost documents or stdin notes")
	}
	if !strings.Contains(out.String(), "no room message posted") {
		t.Fatal("missing quiet task receipt")
	}
}

func TestBoardCLIAnswerDoesNotRequireVersionOrResolve(t *testing.T) {
	var out, stderr bytes.Buffer
	app := NewConnector(&out, &stderr, buildinfo.Info{})
	app.stdin = strings.NewReader("Tests pass.")
	client := callerFunc(func(_ context.Context, method string, input, output any) error {
		in := input.(room.BoardInput)
		if method != "question.answer" || in.Resolve || in.Version != 0 || in.Body != "Tests pass." {
			t.Fatalf("wrong answer: %s %+v", method, in)
		}
		*output.(*room.BoardDetail) = room.BoardDetail{Item: room.BoardItem{Key: "Q-1", Status: "open", Version: 2}, OperationID: in.OperationID}
		return nil
	})
	if code := app.board(context.Background(), client, "question", []string{"answer", "shared", "Q-1", "--stdin", "--json"}, false); code != 0 {
		t.Fatal(stderr.String())
	}
	if !strings.Contains(out.String(), `"status": "open"`) {
		t.Fatalf("not JSON: %s", out.String())
	}
}

func TestQuestionCLIUpdatesAndClearsOptions(t *testing.T) {
	for _, clear := range []bool{false, true} {
		var out, stderr bytes.Buffer
		app := New(&out, &stderr, buildinfo.Info{})
		client := callerFunc(func(_ context.Context, method string, input, output any) error {
			in := input.(room.BoardInput)
			if method == "question.show" {
				*output.(*room.BoardDetail) = room.BoardDetail{Item: room.BoardItem{Key: "Q-1", Version: 3}}
				return nil
			}
			if method != "question.update" || in.Key != "Q-1" || in.Version != 3 || in.Options == nil {
				t.Fatalf("invalid update: %s %+v", method, in)
			}
			if clear {
				if len(*in.Options) != 0 {
					t.Fatal("clear options failed")
				}
			} else if strings.Join(*in.Options, "|") != "Ship now|Wait for fixes" {
				t.Fatal("repeated option flags lost")
			}
			*output.(*room.BoardDetail) = room.BoardDetail{Item: room.BoardItem{Key: "Q-1", Version: 4}}
			return nil
		})
		args := []string{"update", "shared", "Q-1", "--option", "Ship now", "--option", "Wait for fixes"}
		if clear {
			args = []string{"update", "shared", "Q-1", "--clear-options"}
		}
		if app.board(context.Background(), client, "question", args, false) != 0 {
			t.Fatal(stderr.String())
		}
	}
}

func TestBoardCLIRejectsMeaninglessOptions(t *testing.T) {
	client := callerFunc(func(context.Context, string, any, any) error { t.Fatal("invalid command reached server"); return nil })
	for _, args := range [][]string{{"answer", "shared", "Q-1", "answer", "--blocked", "x"}, {"ask", "shared", "Question?", "--human-input", "--no-human-input"}, {"list", "shared", "--open", "--resolved"}, {"show", "shared", "Q-1", "--status", "resolved"}, {"answer", "shared", "Q-1", "text", "--stdin"}} {
		var out, err bytes.Buffer
		app := New(&out, &err, buildinfo.Info{})
		if app.board(context.Background(), client, "question", args, false) == 0 {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestThreadCLIAndTaskDiscussion(t *testing.T) {
	for _, kind := range []string{"task", "thread"} {
		for _, action := range []string{"reply", "ask", "answer", "edit-post"} {
			t.Run(kind+"/"+action, func(t *testing.T) {
				var out, stderr bytes.Buffer
				app := NewConnector(&out, &stderr, buildinfo.Info{})
				app.stdin = strings.NewReader("The new message.")
				key := "TH-1"
				if kind == "task" {
					key = "T-1"
				}
				args := []string{action, "shared", key, "--stdin"}
				if action == "answer" {
					args = append(args, "--to", "P-2")
				}
				if action == "ask" {
					args = append(args, "--human-input", "--option", "Yes", "--option", "No")
				}
				if action == "edit-post" {
					args = append(args, "--post", "P-2", "--clear-options")
				}
				client := callerFunc(func(_ context.Context, method string, input, output any) error {
					in := input.(room.BoardInput)
					if method == kind+".show" {
						*output.(*room.BoardDetail) = room.BoardDetail{Item: room.BoardItem{Version: 4}}
						return nil
					}
					if method != kind+"."+action || in.Body != "The new message." || in.Key != key || in.Owner {
						t.Fatalf("bad post: %s %+v", method, in)
					}
					if action == "answer" && in.ReplyTo != 2 {
						t.Fatal("answer lost target")
					}
					if action == "ask" && (in.Options == nil || len(*in.Options) != 2 || in.HumanInput == nil || !*in.HumanInput) {
						t.Fatal("lost question options")
					}
					if action == "edit-post" && (in.Post != 2 || in.Version != 4 || in.Options == nil || len(*in.Options) != 0) {
						t.Fatal("lost edit fields")
					}
					*output.(*room.BoardDetail) = room.BoardDetail{Item: room.BoardItem{Key: key, Version: 5}, History: []room.BoardHistory{{Revision: 5, Sequence: 20}}, OperationID: in.OperationID}
					return nil
				})
				if app.board(context.Background(), client, kind, args, false) != 0 {
					t.Fatal(stderr.String())
				}
				if !strings.Contains(out.String(), "#20") {
					t.Fatal("post did not report room notification")
				}
			})
		}
	}
}

func TestTaskCLIAssignee(t *testing.T) {
	var out, stderr bytes.Buffer
	app := NewConnector(&out, &stderr, buildinfo.Info{})
	client := callerFunc(func(_ context.Context, method string, input, output any) error {
		in := input.(room.BoardInput)
		if method == "task.show" {
			*output.(*room.BoardDetail) = room.BoardDetail{Item: room.BoardItem{Key: "TH-1", Version: 3}}
			return nil
		}
		if method != "task.update" || in.Version != 3 || in.Assignee == nil || *in.Assignee != "beta" {
			t.Fatalf("bad assignment: %s %+v", method, in)
		}
		*output.(*room.BoardDetail) = room.BoardDetail{Item: room.BoardItem{Key: "TH-1", Version: 4}, History: []room.BoardHistory{{Revision: 4, Sequence: 10}}}
		return nil
	})
	if app.board(context.Background(), client, "task", []string{"update", "shared", "T-1", "--assignee", "beta"}, false) != 0 {
		t.Fatal(stderr.String())
	}
	if !strings.Contains(out.String(), "#10") {
		t.Fatal("missing assignment notification receipt")
	}
}
