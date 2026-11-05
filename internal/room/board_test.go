package room

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

func boardString(s string) *string { return &s }
func boardBool(v bool) *bool       { return &v }

func TestBoardHumanInputCyclesAndResolvedReadOnly(t *testing.T) {
	t.Parallel()
	s, a, b := boardTestStore(t)
	ctx := context.Background()
	q := boardDo(t, s, "question.ask", BoardInput{ParticipantID: a.ID, Title: boardString("Which approach?"), HumanInput: boardBool(true)})
	q = boardDo(t, s, "question.answer", BoardInput{ParticipantID: b.ID, Key: q.Item.Key, Body: "I suggest option A."})
	if !q.Item.HumanInput || q.Item.AnswerCount != 1 {
		t.Fatal("agent answer cleared human request")
	}
	answerInput := BoardInput{Owner: true, Key: q.Item.Key, Body: "Use A.", OperationID: "human-answer"}
	q = boardDo(t, s, "question.answer", answerInput)
	if q.Item.HumanInput || q.Item.Status != "open" || q.Item.AnswerCount != 2 || q.LatestAnswer == nil || q.LatestAnswer.Body != "Use A." {
		t.Fatalf("human answer state: %+v", q)
	}
	q = boardDo(t, s, "question.update", BoardInput{ParticipantID: a.ID, Key: q.Item.Key, Version: q.Item.Version, Notes: boardString("Follow-up evidence.")})
	if q.Item.HumanInput {
		t.Fatal("ordinary edit requested input")
	}
	q = boardDo(t, s, "question.update", BoardInput{ParticipantID: a.ID, Key: q.Item.Key, Version: q.Item.Version, HumanInput: boardBool(true), Notes: boardString("Does A also apply on mobile?")})
	if !q.Item.HumanInput || q.Item.HumanInputRevision != q.Item.Version || q.History[0].Sequence == 0 || !strings.Contains(q.History[0].Body, "Human input requested again") {
		t.Fatalf("follow-up not requested/notified: %+v", q)
	}
	replay := boardDo(t, s, "question.answer", answerInput)
	if replay.Item.HumanInput || replay.Item.AnswerCount != 2 || replay.Item.Version != 3 {
		t.Fatalf("receipt picked up future state: %+v", replay)
	}
	page, err := s.Board(ctx, "question.list", BoardInput{Room: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	listed := page.(BoardPage).Items[0]
	if !listed.HumanInput || listed.AnswerCount != 2 || listed.LastAnswerBy != "owner" {
		t.Fatalf("list state: %+v", listed)
	}
	q = boardDo(t, s, "question.answer", BoardInput{Owner: true, Key: q.Item.Key, Body: "Yes, mobile too."})
	if q.Item.HumanInput {
		t.Fatal("second human answer did not clear request")
	}
	q = boardDo(t, s, "question.resolve", BoardInput{ParticipantID: a.ID, Key: q.Item.Key, Version: q.Item.Version})
	for _, in := range []BoardInput{
		{Owner: true, Body: "Late answer"},
		{ParticipantID: b.ID, Body: "Late agent answer"},
		{Owner: true, Notes: boardString("Late edit")},
	} {
		method := "question.answer"
		if in.Notes != nil {
			method = "question.update"
		}
		in.Room, in.Key, in.Version, in.OperationID = "shared", q.Item.Key, q.Item.Version, method
		if _, err := s.Board(ctx, method, in); err == nil || !strings.Contains(err.Error(), "read-only") {
			t.Fatalf("resolved mutation: %v", err)
		}
	}
	replay = boardDo(t, s, "question.answer", answerInput)
	if replay.Item.Version != 3 {
		t.Fatal("valid retry rejected after resolution")
	}
	q = boardDo(t, s, "question.reopen", BoardInput{ParticipantID: b.ID, Key: q.Item.Key, Version: q.Item.Version})
	if q.Item.HumanInput || q.Item.Status != "open" {
		t.Fatal("reopen implicitly requested human input")
	}
	q = boardDo(t, s, "question.answer", BoardInput{ParticipantID: b.ID, Key: q.Item.Key, Body: "New evidence."})
	if q.Item.AnswerCount != 4 {
		t.Fatal("thread lost previous answers")
	}
}

func TestBoardLegacyHumanAnswersClearPendingHint(t *testing.T) {
	t.Parallel()
	s, a, _ := boardTestStore(t)
	ctx := context.Background()
	q := boardDo(t, s, "question.ask", BoardInput{ParticipantID: a.ID, Title: boardString("Legacy question"), HumanInput: boardBool(true)})
	q = boardDo(t, s, "question.answer", BoardInput{Owner: true, Key: q.Item.Key, Body: "Already answered."})
	// Reproduce old storage: the hint stayed true after a human answer and
	// there was no request revision. Reads must fix presentation without writes.
	if _, err := s.db.ExecContext(ctx, `UPDATE board_items SET data=json_remove(json_set(data,'$.human_input',json('true')),'$.human_input_revision') WHERE id=?`, q.Item.ID); err != nil {
		t.Fatal(err)
	}
	value, err := s.Board(ctx, "question.show", BoardInput{Room: "shared", Key: q.Item.Key})
	if err != nil {
		t.Fatal(err)
	}
	q = value.(BoardDetail)
	if q.Item.HumanInput || q.Item.AnswerCount != 1 || q.LatestAnswer.Body != "Already answered." {
		t.Fatalf("legacy answer not recognized: %+v", q)
	}
	q = boardDo(t, s, "question.update", BoardInput{ParticipantID: a.ID, Key: q.Item.Key, Version: q.Item.Version, HumanInput: boardBool(true)})
	if !q.Item.HumanInput {
		t.Fatal("old answer suppressed new request")
	}
}
func boardTestStore(t *testing.T) (*Store, Participant, Participant) {
	t.Helper()
	ctx := context.Background()
	s, err := Open(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err = s.CreateRoom(ctx, CreateRoomInput{Slug: "shared", Title: "Shared"}); err != nil {
		t.Fatal(err)
	}
	a, err := s.Join(ctx, JoinInput{Room: "shared", Handle: "alpha", WorkingDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Join(ctx, JoinInput{Room: "shared", Handle: "beta", WorkingDirectory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return s, a, b
}
func boardDo(t *testing.T, s *Store, method string, in BoardInput) BoardDetail {
	t.Helper()
	if in.Room == "" {
		in.Room = "shared"
	}
	if in.OperationID == "" {
		in.OperationID, _ = randomID("test_")
	}
	v, err := s.Board(context.Background(), method, in)
	if err != nil {
		t.Fatal(err)
	}
	return v.(BoardDetail)
}

func TestBoardSharedEditsQuietTasksAndConflicts(t *testing.T) {
	t.Parallel()
	s, a, b := boardTestStore(t)
	ctx := context.Background()
	before, _ := s.Snapshot(ctx, "shared", 0, 50)
	created := boardDo(t, s, "task.create", BoardInput{ParticipantID: a.ID, Title: boardString("Check a release")})
	workers := []string{"@beta", "beta"}
	updated := boardDo(t, s, "task.update", BoardInput{ParticipantID: b.ID, Key: created.Item.Key, Version: created.Item.Version, Workers: &workers, Status: boardString("done")})
	if updated.Item.CreatedBy != "alpha" || updated.Item.UpdatedBy != "beta" || len(updated.Item.Workers) != 1 || updated.Item.Status != "done" {
		t.Fatalf("shared editing lost attribution: %+v", updated)
	}
	_, err := s.Board(ctx, "task.update", BoardInput{Room: "shared", ParticipantID: a.ID, Key: created.Item.Key, Version: 1, OperationID: "stale", Title: boardString("Old edit")})
	if err == nil || !strings.Contains(err.Error(), "version conflict") {
		t.Fatalf("stale edit = %v", err)
	}
	archived := boardDo(t, s, "task.archive", BoardInput{ParticipantID: b.ID, Key: updated.Item.Key, Version: 2})
	if !archived.Item.Archived {
		t.Fatal("not archived")
	}
	page, err := s.Board(ctx, "task.list", BoardInput{Room: "shared"})
	if err != nil || len(page.(BoardPage).Items) != 0 {
		t.Fatalf("active list: %v %v", page, err)
	}
	page, err = s.Board(ctx, "task.list", BoardInput{Room: "shared", Archived: true})
	if err != nil || len(page.(BoardPage).Items) != 1 {
		t.Fatalf("archive list: %v %v", page, err)
	}
	restored := boardDo(t, s, "task.restore", BoardInput{ParticipantID: a.ID, Key: updated.Item.Key, Version: 3})
	if restored.Item.Archived {
		t.Fatal("not restored")
	}
	after, _ := s.Snapshot(ctx, "shared", 0, 50)
	if len(after.Messages) != len(before.Messages)+3 {
		t.Fatal("assigned task update/archive/restore should each notify once")
	}
	for _, m := range after.Messages[len(before.Messages):] {
		if !strings.Contains(m.Body, "For @") {
			t.Fatal("task change omitted subscribers")
		}
	}
	if _, err = s.Leave(ctx, LeaveInput{Room: "shared", ParticipantID: a.ID}); err != nil {
		t.Fatal(err)
	}
	boardDo(t, s, "task.update", BoardInput{ParticipantID: b.ID, Key: updated.Item.Key, Version: 4, Notes: boardString("Creator left; others can still edit.")})
	_, err = s.Board(ctx, "task.update", BoardInput{Room: "shared", ParticipantID: a.ID, Key: updated.Item.Key, Version: 5, OperationID: "left", Notes: boardString("Should fail")})
	if err == nil {
		t.Fatal("departed participant edited")
	}
}

func TestBoardAnswersAreCanonicalAtomicAndRetrySafe(t *testing.T) {
	t.Parallel()
	s, a, b := boardTestStore(t)
	ctx := context.Background()
	q := boardDo(t, s, "question.ask", BoardInput{ParticipantID: a.ID, Title: boardString("Ship this?"), HumanInput: boardBool(true)})
	before, _ := s.Snapshot(ctx, "shared", 0, 50)
	in := BoardInput{Room: "shared", ParticipantID: b.ID, Key: q.Item.Key, OperationID: "reply-one", Body: "The smoke tests pass."}
	v, err := s.Board(ctx, "question.answer", in)
	if err != nil {
		t.Fatal(err)
	}
	answer := v.(BoardDetail)
	if answer.Item.Status != "open" || len(answer.History) != 2 {
		t.Fatalf("answer wrongly resolved: %+v", answer)
	}
	after, _ := s.Snapshot(ctx, "shared", 0, 50)
	if len(after.Messages) != len(before.Messages)+1 {
		t.Fatal("answer was not exactly one room event")
	}
	last := after.Messages[len(after.Messages)-1]
	if last.ParticipantID != b.ID || last.SenderHandle != "beta" || !strings.Contains(last.Body, "#crewfold-question-Q-1") {
		t.Fatalf("wrong attribution/link: %+v", last)
	}
	if answer.History[0].Body != last.Body || answer.History[0].Sequence != last.Sequence {
		t.Fatal("answer history diverged from canonical feed")
	}
	repeated, err := s.Board(ctx, "question.answer", in)
	if err != nil || repeated.(BoardDetail).Item.Version != 2 {
		t.Fatalf("retry failed: %v", err)
	}
	current, _ := s.Snapshot(ctx, "shared", 0, 50)
	if current.Room.LastSequence != after.Room.LastSequence {
		t.Fatal("retry duplicated answer")
	}
	in.Body = "Different body"
	if _, err = s.Board(ctx, "question.answer", in); err == nil {
		t.Fatal("operation id reused for different request")
	}
	closed := boardDo(t, s, "question.answer", BoardInput{ParticipantID: b.ID, Key: q.Item.Key, Version: 2, Body: "Ship it.", Resolve: true})
	if closed.Item.Status != "resolved" || len(closed.History) != 3 {
		t.Fatal("answer and resolve failed")
	}
	boardDo(t, s, "question.reopen", BoardInput{ParticipantID: a.ID, Key: q.Item.Key, Version: 3})
	// Two independent answers must append without a lost update or an exclusive
	// ownership gate, even when both agents read the same prior state.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i, p := range []Participant{a, b} {
		wg.Add(1)
		go func(i int, p Participant) {
			defer wg.Done()
			_, err := s.Board(ctx, "question.answer", BoardInput{Room: "shared", ParticipantID: p.ID, Key: q.Item.Key, OperationID: fmt.Sprintf("parallel-%d", i), Body: "Additional evidence."})
			errs <- err
		}(i, p)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	result, err := s.Board(ctx, "question.show", BoardInput{Room: "shared", Key: q.Item.Key})
	if err != nil || result.(BoardDetail).Item.Version != 6 {
		t.Fatalf("concurrent answers lost: %v %v", result, err)
	}
}

func TestBoardRoomScopingReferencesAndPagination(t *testing.T) {
	t.Parallel()
	s, a, _ := boardTestStore(t)
	ctx := context.Background()
	if _, err := s.CreateRoom(ctx, CreateRoomInput{Slug: "private", Title: "Private"}); err != nil {
		t.Fatal(err)
	}
	private := boardDo(t, s, "task.create", BoardInput{Room: "private", Owner: true, Title: boardString("Private")})
	if _, err := s.Board(ctx, "task.update", BoardInput{Room: "private", ParticipantID: a.ID, Key: private.Item.Key, Version: 1, OperationID: "unauthorized", Title: boardString("spoof")}); err == nil {
		t.Fatal("cross-room actor accepted")
	}
	doc, err := s.Upload(ctx, UploadInput{Room: "private", Owner: true, Name: "evidence.md", ContentBase64: base64.StdEncoding.EncodeToString([]byte("evidence"))})
	if err != nil {
		t.Fatal(err)
	}
	refs := []string{doc.Document.ID}
	if _, err = s.Board(ctx, "task.create", BoardInput{Room: "shared", ParticipantID: a.ID, OperationID: "bad-doc", Title: boardString("Shared"), Documents: &refs}); err == nil {
		t.Fatal("cross-room document linked")
	}
	workers := []string{"not-in-room"}
	if _, err = s.Board(ctx, "task.create", BoardInput{Room: "shared", ParticipantID: a.ID, OperationID: "bad-worker", Title: boardString("Shared"), Workers: &workers}); err == nil {
		t.Fatal("unknown worker linked")
	}
	for i := 0; i < 6; i++ {
		boardDo(t, s, "task.create", BoardInput{ParticipantID: a.ID, Title: boardString(fmt.Sprintf("Task %d", i))})
	}
	page, err := s.Board(ctx, "task.list", BoardInput{Room: "shared", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	p := page.(BoardPage)
	if len(p.Items) != 3 || p.Next != 3 {
		t.Fatalf("first page: %+v", p)
	}
	page, err = s.Board(ctx, "task.list", BoardInput{Room: "shared", Limit: 3, After: p.Next})
	if err != nil {
		t.Fatal(err)
	}
	p = page.(BoardPage)
	if len(p.Items) != 3 || p.Items[0].Key != "T-4" || p.Next != 0 {
		t.Fatalf("second page: %+v", p)
	}
	for i := 1; i <= 51; i++ {
		boardDo(t, s, "task.update", BoardInput{ParticipantID: a.ID, Key: "T-1", Version: int64(i), Notes: boardString(fmt.Sprintf("Revision %d", i))})
	}
	d, err := s.Board(ctx, "task.show", BoardInput{Room: "shared", Key: "T-1"})
	if err != nil {
		t.Fatal(err)
	}
	detail := d.(BoardDetail)
	if len(detail.History) != 50 || detail.NextHistory != 3 {
		t.Fatal("history not bounded")
	}
	d, err = s.Board(ctx, "task.show", BoardInput{Room: "shared", Key: "T-1", HistoryBefore: 3})
	if err != nil || len(d.(BoardDetail).History) != 2 {
		t.Fatalf("history continuation %v %v", d, err)
	}
	if _, err = s.Archive(ctx, "shared"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Board(ctx, "task.create", BoardInput{Room: "shared", Owner: true, OperationID: "closed", Title: boardString("No")}); err == nil {
		t.Fatal("archived room mutated")
	}
}

func TestBoardDeltaRetriesAndPinnedDocumentsSurviveReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	s, err := Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if _, err = s.CreateRoom(ctx, CreateRoomInput{Slug: "shared", Title: "Shared"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"alpha", "beta"} {
		if _, err = s.Join(ctx, JoinInput{Room: "shared", Handle: name, WorkingDirectory: t.TempDir()}); err != nil {
			t.Fatal(err)
		}
	}
	doc, err := s.Upload(ctx, UploadInput{Room: "shared", Owner: true, Name: "evidence.md", ContentBase64: base64.StdEncoding.EncodeToString([]byte("original evidence"))})
	if err != nil {
		t.Fatal(err)
	}
	created := boardDo(t, s, "task.create", BoardInput{Owner: true, Title: boardString("Pinned evidence")})
	in := BoardInput{Room: "shared", Owner: true, Key: created.Item.Key, Version: 1, OperationID: "delta-retry", AddWorkers: []string{"@alpha"}, RemoveWorkers: []string{"owner"}, LinkDocuments: []string{"evidence.md"}}
	value, err := s.Board(ctx, "task.update", in)
	if err != nil {
		t.Fatal(err)
	}
	updated := value.(BoardDetail)
	if len(updated.Item.Workers) != 1 || len(updated.Item.Documents) != 1 || updated.Item.Documents[0].ID != doc.Document.ID {
		t.Fatal("deltas lost")
	}
	if _, err = s.Upload(ctx, UploadInput{Room: "shared", Owner: true, Name: "evidence.md", ContentBase64: base64.StdEncoding.EncodeToString([]byte("later evidence"))}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := s.Board(ctx, "task.update", in)
	if err != nil {
		t.Fatal(err)
	}
	got := retry.(BoardDetail)
	if got.Item.Version != 2 || got.Item.Documents[0].ID != doc.Document.ID || len(got.History) != 2 {
		t.Fatalf("retry moved pinned evidence or revision: %+v", got)
	}
	var stored string
	if err = s.db.QueryRowContext(ctx, `SELECT result FROM board_operations WHERE operation_id='delta-retry'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "original evidence") {
		t.Fatal("operation journal duplicated document bytes")
	}
	for _, in := range []BoardInput{{Room: "shared", Owner: true, OperationID: "ignored", Key: "T-1", Body: "ignored", Version: 2}, {Room: "shared", FilterStatus: "bogus"}} {
		method := "task.update"
		if in.FilterStatus != "" {
			method = "task.list"
		}
		if _, err = s.Board(ctx, method, in); err == nil {
			t.Fatal("accepted a meaningless field")
		}
	}
}

func TestQuestionOptionsCanBeAddedWithoutReplacingAnswers(t *testing.T) {
	t.Parallel()
	s, a, b := boardTestStore(t)
	ctx := context.Background()
	q := boardDo(t, s, "question.ask", BoardInput{ParticipantID: a.ID, Title: boardString("Release decision?")})
	boardDo(t, s, "question.answer", BoardInput{ParticipantID: b.ID, Key: q.Item.Key, Body: "The tests pass."})
	options := []string{" Ship now ", "Wait for fixes"}
	updated := boardDo(t, s, "question.update", BoardInput{ParticipantID: b.ID, Key: q.Item.Key, Version: 2, Options: &options})
	if updated.Item.Key != q.Item.Key || updated.Item.Options[0] != "Ship now" || len(updated.History) != 3 || !strings.Contains(updated.History[1].Body, "The tests pass.") {
		t.Fatalf("update lost question/answers: %+v", updated)
	}
	before, _ := s.Snapshot(ctx, "shared", 0, 50)
	if !strings.Contains(before.Messages[len(before.Messages)-1].Body, "For @alpha") {
		t.Fatal("option edit did not notify the OP")
	}
	selected := boardDo(t, s, "question.answer", BoardInput{ParticipantID: a.ID, Key: q.Item.Key, Body: "Ship now"})
	if !strings.Contains(selected.History[0].Body, "Ship now") {
		t.Fatal("selected answer not published")
	}
	custom := boardDo(t, s, "question.answer", BoardInput{ParticipantID: b.ID, Key: q.Item.Key, Body: "Ship only the client today."})
	if custom.Item.Status != "open" {
		t.Fatal("custom answer resolved question")
	}
	preserved := boardDo(t, s, "question.update", BoardInput{ParticipantID: a.ID, Key: q.Item.Key, Version: 5, Notes: boardString("More context")})
	if len(preserved.Item.Options) != 2 {
		t.Fatal("omitting options cleared them")
	}
	empty := []string{}
	cleared := boardDo(t, s, "question.update", BoardInput{ParticipantID: b.ID, Key: q.Item.Key, Version: 6, Options: &empty})
	if len(cleared.Item.Options) != 0 || len(cleared.History) != 7 {
		t.Fatal("clearing options lost history")
	}
	asked := boardDo(t, s, "question.ask", BoardInput{ParticipantID: a.ID, Title: boardString("With choices?"), Options: &options})
	if !strings.Contains(asked.History[0].Body, "- Ship now") || !strings.Contains(asked.History[0].Body, "custom answer") {
		t.Fatal("raised question omitted options from room event")
	}
	for _, bad := range [][]string{{""}, {"yes", " YES "}, {"one\ntwo"}, {strings.Repeat("x", 241)}, {"1", "2", "3", "4", "5", "6", "7", "8", "9"}} {
		_, err := s.Board(ctx, "question.update", BoardInput{Room: "shared", ParticipantID: a.ID, Key: q.Item.Key, Version: 7, OperationID: "invalid-options", Options: &bad})
		if err == nil {
			t.Fatalf("accepted invalid options: %#v", bad)
		}
	}
	if _, err := s.Board(ctx, "task.create", BoardInput{Room: "shared", ParticipantID: a.ID, OperationID: "task-options", Title: boardString("Not a question"), Options: &options}); err == nil {
		t.Fatal("accepted options on a task")
	}
}

func TestBoardCollaboratorIdentityAndAccess(t *testing.T) {
	t.Parallel()
	store, local, _ := boardTestStore(t)
	ctx := context.Background()
	if _, err := store.CreateRoom(ctx, CreateRoomInput{Slug: "private", Title: "Private"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConfigureNetwork(ctx, ConfigureNetworkInput{Enabled: true, PublicOrigin: "https://example.ts.net"}); err != nil {
		t.Fatal(err)
	}
	invite, err := store.CreateNetworkInvite(ctx, CreateNetworkInviteInput{Room: "shared"})
	if err != nil {
		t.Fatal(err)
	}
	enrollment, err := store.EnrollNetworkDevice(ctx, NetworkEnrollInput{Invite: invite.Secret, DeviceName: "remote"})
	if err != nil {
		t.Fatal(err)
	}
	device := NetworkDevice{ID: enrollment.DeviceID, Name: enrollment.DeviceName}
	if _, err = store.SetNetworkBrowserIdentity(ctx, device.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	p, err := store.JoinNetworkParticipant(ctx, device, JoinInput{Room: "shared", Handle: "remote-agent", WorkingDirectory: "/remote/work", Delivery: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	manager := NewDeliveryManager(ctx, store, nil, nil)
	defer manager.Close()
	server := &Server{store: store, deliveries: manager}
	raw := func(in BoardInput) json.RawMessage { b, _ := json.Marshal(in); return b }
	q := boardDo(t, store, "question.ask", BoardInput{ParticipantID: local.ID, Title: boardString("Need a decision?"), HumanInput: boardBool(true)})
	result, err := server.networkBrowserCall(ctx, device, "question.answer", raw(BoardInput{Room: "shared", Key: q.Item.Key, Owner: true, ParticipantID: local.ID, OperationID: "human", Body: "A human answer."}))
	if err != nil {
		t.Fatal(err)
	}
	d := result.(BoardDetail)
	if d.Item.UpdatedBy != "alice" {
		t.Fatalf("human impersonated owner or agent: %+v", d.Item)
	}
	if d.Item.HumanInput {
		t.Fatal("collaborator human answer did not clear request")
	}
	d = boardDo(t, store, "question.update", BoardInput{ParticipantID: local.ID, Key: q.Item.Key, Version: d.Item.Version, HumanInput: boardBool(true)})
	result, err = server.networkCall(ctx, device, p.ID, "question.answer", raw(BoardInput{Room: "shared", Key: q.Item.Key, Owner: true, ParticipantID: local.ID, OperationID: "agent", Body: "An agent answer."}))
	if err != nil {
		t.Fatal(err)
	}
	if result.(BoardDetail).Item.UpdatedBy != "remote-agent" {
		t.Fatal("remote identity spoofed")
	}
	if !result.(BoardDetail).Item.HumanInput {
		t.Fatal("remote agent answer cleared human request")
	}
	for _, method := range []string{"task.list", "question.show", "task.create"} {
		in := BoardInput{Room: "private", Key: "Q-1", Title: boardString("No access"), Owner: true, OperationID: method}
		if _, err = server.networkBrowserCall(ctx, device, method, raw(in)); err == nil {
			t.Fatalf("browser leaked %s", method)
		}
		if _, err = server.networkCall(ctx, device, p.ID, method, raw(in)); err == nil {
			t.Fatalf("connector leaked %s", method)
		}
	}
	if _, err = server.networkCall(ctx, device, "", "task.create", raw(BoardInput{Room: "shared", Title: boardString("No binding"), Owner: true, OperationID: "no-binding"})); err == nil {
		t.Fatal("unbound device mutated")
	}
	batch, err := store.NetworkDeliveryBatch(ctx, device.ID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Same canonical message stream is used, and self-authored answers are
	// excluded by the existing delivery implementation.
	for _, m := range batch.Messages {
		if m.ParticipantID == p.ID {
			t.Fatal("self answer delivered back")
		}
	}
}
