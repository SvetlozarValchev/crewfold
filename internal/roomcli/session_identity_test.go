package roomcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"crewfold/internal/room"
)

func TestSessionCommandsStayRoomScopedAfterChangingDirectory(t *testing.T) {
	identity := sessionIdentity{"codex", "same-session"}
	members := map[string]room.Participant{
		"one": {ID: "p1", Handle: "first", Status: "joined", WorkingDirectory: "/old/one", Delivery: &room.ParticipantDelivery{Kind: "codex", Target: "same-session"}},
		"two": {ID: "p2", Handle: "second", Status: "joined", WorkingDirectory: "/old/two", Delivery: &room.ParticipantDelivery{Kind: "codex", Target: "same-session"}},
	}
	var forwarded int
	base := callerFunc(func(_ context.Context, method string, in, out any) error {
		if method == "participant.list" {
			key := in.(map[string]string)["room"]
			*out.(*[]room.Participant) = []room.Participant{members[key], {ID: "other", Status: "joined", WorkingDirectory: "/new", Delivery: &room.ParticipantDelivery{Kind: "codex", Target: "someone-else"}}}
			return nil
		}
		key := roomIdentifierFromParams(in)
		var cwd, handle string
		switch v := in.(type) {
		case room.SendInput:
			cwd, handle = v.WorkingDirectory, v.Handle
		case room.AckInput:
			cwd, handle = v.WorkingDirectory, v.Handle
		case room.LeaveInput:
			cwd, handle = v.WorkingDirectory, v.Handle
		case room.UploadInput:
			cwd, handle = v.WorkingDirectory, v.Handle
		case room.ArchiveDocumentInput:
			cwd, handle = v.WorkingDirectory, v.Handle
		case room.BoardInput:
			cwd, handle = v.WorkingDirectory, members[key].Handle
		default:
			t.Fatalf("unexpected input %T", in)
		}
		if cwd != members[key].WorkingDirectory || handle != members[key].Handle {
			t.Fatalf("wrong room actor: %s, %#v", key, in)
		}
		forwarded++
		return nil
	})
	caller := &sessionCaller{client: base, session: callerSession{loaded: true, identity: identity}}
	for _, key := range []string{"one", "two", "one"} {
		for _, action := range []struct {
			method string
			input  any
		}{
			{"message.send", room.SendInput{Room: key, WorkingDirectory: "/new"}},
			{"participant.ack", room.AckInput{Room: key, WorkingDirectory: "/new"}},
			{"participant.leave", room.LeaveInput{Room: key, WorkingDirectory: "/new"}},
			{"document.upload", room.UploadInput{Room: key, WorkingDirectory: "/new"}},
			{"document.archive", room.ArchiveDocumentInput{Room: key, WorkingDirectory: "/new"}},
			{"task.update", room.BoardInput{Room: key, WorkingDirectory: "/new"}},
		} {
			if err := caller.Call(context.Background(), action.method, action.input, nil); err != nil {
				t.Fatal(err)
			}
		}
		p, err := caller.recipient(context.Background(), key, "/new", []room.Participant{members[key]})
		if err != nil || p.ID != members[key].ID {
			t.Fatalf("receive identity = %#v, %v", p, err)
		}
	}
	if forwarded != 18 {
		t.Fatal(forwarded)
	}
	if err := caller.Call(context.Background(), "message.send", room.SendInput{Room: "unjoined", WorkingDirectory: "/new"}, nil); err == nil {
		t.Fatal("impersonated cwd member")
	}
}

func TestSessionParticipantRejectsAmbiguousAndLeft(t *testing.T) {
	p := room.Participant{ID: "p", Status: "joined", Delivery: &room.ParticipantDelivery{Kind: "claude", Target: "s"}}
	identity := sessionIdentity{"claude", "s"}
	if _, err := sessionParticipant(identity, []room.Participant{p, p}); err == nil {
		t.Fatal("accepted duplicates")
	}
	p.Status = "left"
	if _, err := sessionParticipant(identity, []room.Participant{p}); err == nil {
		t.Fatal("accepted left participant")
	}
}

func TestManualMembersRetainDirectoryFallback(t *testing.T) {
	p := room.Participant{ID: "manual", Status: "joined", WorkingDirectory: "/manual"}
	identity := sessionIdentity{"codex", "current"}
	if got, err := sessionParticipant(identity, []room.Participant{p}, "/manual"); err != nil || got.ID != p.ID {
		t.Fatalf("manual fallback: %#v, %v", got, err)
	}
	p.Delivery = &room.ParticipantDelivery{Kind: "codex", Target: "someone-else"}
	if _, err := sessionParticipant(identity, []room.Participant{p}, "/manual"); err == nil {
		t.Fatal("borrowed another session")
	}
}

func TestClaudeCallerUsesProcessNotFolderOrInheritedCodexID(t *testing.T) {
	directory := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '[{\"pid\":%d,\"kind\":\"interactive\",\"sessionId\":\"actual-claude\",\"cwd\":\"/original-folder\"}]'\n", os.Getppid())
	if err := os.WriteFile(filepath.Join(directory, "claude"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CODEX_THREAD_ID", "inherited-outer-session")
	identity, err := (&callerSession{}).get(context.Background())
	if err != nil || identity.Provider != "claude" || identity.Target != "actual-claude" {
		t.Fatalf("caller: %#v, %v", identity, err)
	}
}

func TestRemoteSessionRoutingDoesNotReplaceOtherRoomBinding(t *testing.T) {
	config := remoteConfig{Version: 1, Hubs: []remoteHub{{URL: "https://example.test", Rooms: []remoteRoom{{ID: "r1", Slug: "one"}, {ID: "r2", Slug: "two"}}, Bindings: []remoteBinding{
		{RoomID: "r1", ParticipantID: "p1", Directory: "/old", Provider: "claude", Target: "s"},
		{RoomID: "r2", ParticipantID: "p2", Directory: "/other-old", Provider: "claude", Target: "s"},
		{RoomID: "r1", ParticipantID: "wrong", Directory: "/new", Provider: "claude", Target: "other"},
	}}}}
	for _, key := range []string{"one", "two", "one"} {
		route, err := resolveRemoteRoute(config, "message.send", key, "/new", nil, sessionIdentity{"claude", "s"})
		if err != nil || route.binding == nil || route.binding.Target != "s" {
			t.Fatalf("route %#v, %v", route, err)
		}
		if key == "one" && route.binding.ParticipantID != "p1" || key == "two" && route.binding.ParticipantID != "p2" {
			t.Fatal("cross-room identity")
		}
	}
	if _, err := resolveRemoteRoute(config, "message.send", "one", "/new", nil, sessionIdentity{"claude", "missing"}); err == nil {
		t.Fatal("fell back to another session by directory")
	}
}

func TestSessionDetectionErrorDoesNotFallBack(t *testing.T) {
	called := false
	c := &sessionCaller{session: callerSession{loaded: true, err: errors.New("cannot identify caller")}, client: callerFunc(func(context.Context, string, any, any) error { called = true; return nil })}
	if err := c.Call(context.Background(), "message.send", room.SendInput{Room: "one"}, nil); err == nil || called {
		t.Fatal("unsafe fallback")
	}
}
