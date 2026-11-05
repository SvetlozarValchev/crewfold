package roomcli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"crewfold/internal/room"
)

const boardHelp = `Optional shared room board (same commands in crewfold-connector):
  crewfold room thread list ROOM [--open|--resolved]
  crewfold room thread show ROOM TH-N [--entries-before N] [--history-before N]
  crewfold room thread create ROOM TITLE [--stdin]
  crewfold room thread update ROOM TH-N [--title TEXT] [--link-document FILE]
  crewfold room thread resolve|reopen ROOM TH-N
  crewfold room thread reply ROOM TH-N [TEXT|--stdin]
  crewfold room thread ask ROOM TH-N [TEXT|--stdin] [--human-input] [--option TEXT ...]
  crewfold room thread answer ROOM TH-N [TEXT|--stdin] --to P-N
  crewfold room thread edit-post ROOM TH-N --post P-N --stdin [--option TEXT ...|--clear-options]
  crewfold room task reply|ask|answer|edit-post ROOM T-N ... (same post options)

Legacy question commands and Q-N links remain supported. TH-N identifies new threads.
Each question owns its options. Follow-ups start with no inherited choices.
Activity mentions subscribed agents except its author; everyone can still see and contribute.
Threads have an OP, not an assignee. Creator and agent posters subscribe automatically.
Tasks have one assignee (creator by default); reassignment retains previous subscribers.

  crewfold room task list ROOM [--status backlog|in-progress|done] [--archived] [--worker HANDLE] [--search TEXT] [--after N] [--limit N]
  crewfold room task create ROOM TITLE [--stdin] [--assignee HANDLE] [--link-document FILE]
  crewfold room task show ROOM T-N [--history-before N]
  crewfold room task update ROOM T-N [--title TEXT] [--stdin] [--status STATUS] [--assignee HANDLE] [--blocked TEXT|--clear-blocked] [--link-document FILE]
  crewfold room task archive|restore ROOM T-N
  crewfold room question list ROOM [--open|--resolved] [--after N] [--limit N]
  crewfold room question ask ROOM TITLE [--stdin] [--human-input] [--option TEXT ...]
  crewfold room question show ROOM Q-N [--history-before N]
  crewfold room question update ROOM Q-N [--title TEXT] [--stdin] [--human-input|--no-human-input] [--option TEXT ...|--clear-options] [--link-document FILE]
  crewfold room question answer ROOM Q-N [TEXT|--stdin] [--resolve]
  crewfold room question resolve|reopen ROOM Q-N

Any joined participant can maintain any record. Assignment does not grant exclusive editing rights.
Repeat --option to offer suggested answers; a custom answer is always allowed. Options replace the whole list.
Record activity mentions subscribed agents, except its author. Participation subscribes automatically. An answer need not resolve it.
Human answers clear pending human input; ask --human-input requests it on a fresh question post.
Agent replies do not clear human requests. Legacy question update --human-input remains supported.
Resolved questions are read-only. Explicitly reopen before editing or answering.
--stdin supplies Markdown notes or an answer. --assignee HANDLE reassigns a task; --documents ID,ID replaces document links (empty clears).
Edits read the current revision first; --version N explicitly checks a known revision.
Mutations print an operation ID; --operation-id ID with identical arguments/version safely retries a request.
Use --output json (or --json here) for structured results. Local humans may use --owner.
`

func (a *App) board(ctx context.Context, client room.Caller, kind string, args []string, jsonMode bool) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" {
		fmt.Fprint(a.stdout, boardHelp)
		return 0
	}
	action := args[0]
	posting := action == "reply" || action == "answer" || action == "edit-post" || (action == "ask" && kind != "question")
	allowed := map[string]bool{"list": true, "show": true, "update": true, "reply": true, "edit-post": true, "answer": true, "ask": true}
	if kind == "thread" {
		allowed["create"] = true
	}
	if kind == "task" {
		for _, s := range []string{"create", "archive", "restore"} {
			allowed[s] = true
		}
	} else {
		for _, s := range []string{"ask", "answer", "resolve", "reopen"} {
			allowed[s] = true
		}
	}
	if !allowed[action] {
		return a.fail(fmt.Errorf("unknown %s command %q", kind, action))
	}
	values := map[string][]string{}
	flags := map[string]bool{}
	pos := []string{}
	valueOptions := map[string]bool{"title": true, "status": true, "blocked": true, "add-worker": true, "remove-worker": true, "workers": true, "assignee": true, "link-document": true, "documents": true, "worker": true, "search": true, "after": true, "limit": true, "history-before": true, "entries-before": true, "version": true, "operation-id": true, "option": true, "to": true, "post": true}
	flagOptions := map[string]bool{"stdin": true, "owner": true, "json": true, "archived": true, "open": true, "resolved": true, "resolve": true, "human-input": true, "no-human-input": true, "clear-blocked": true, "clear-options": true}
	for i := 1; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "--") {
			pos = append(pos, arg)
			continue
		}
		key := strings.TrimPrefix(arg, "--")
		if flagOptions[key] {
			flags[key] = true
			continue
		}
		if !valueOptions[key] || i+1 == len(args) {
			return a.fail(fmt.Errorf("unknown option or missing value: %s", arg))
		}
		i++
		values[key] = append(values[key], args[i])
	}
	if len(pos) < 1 {
		return a.fail(errors.New("room is required"))
	}
	if flags["json"] {
		jsonMode = true
	}
	cwd, _ := os.Getwd()
	in := room.BoardInput{Room: pos[0], WorkingDirectory: cwd, Owner: flags["owner"]}
	if a.program == "crewfold-connector" && in.Owner {
		return a.fail(errors.New("--owner is unavailable in the connector"))
	}
	value := func(key string) string {
		v := values[key]
		if len(v) == 0 {
			return ""
		}
		return v[len(v)-1]
	}
	// Reject options that otherwise appear accepted but have no effect.
	permitted := map[string]bool{"owner": true, "json": true}
	if action == "list" {
		for _, k := range []string{"archived", "open", "resolved", "worker", "search", "after", "limit", "status"} {
			permitted[k] = true
		}
	} else if action == "show" {
		permitted["history-before"], permitted["entries-before"] = true, true
	} else {
		permitted["operation-id"], permitted["version"] = true, true
		if action == "create" || (action == "ask" && kind == "question") || action == "update" {
			for _, k := range []string{"stdin", "title", "status", "link-document", "documents"} {
				permitted[k] = true
			}
			if kind == "task" {
				for _, k := range []string{"assignee", "workers", "add-worker", "remove-worker"} {
					permitted[k] = true
				}
			}
			if kind == "task" {
				for _, k := range []string{"blocked", "clear-blocked"} {
					permitted[k] = true
				}
			} else {
				permitted["human-input"], permitted["no-human-input"] = true, true
				permitted["option"], permitted["clear-options"] = true, true
			}
		}
		if posting {
			permitted["stdin"] = true
			if action == "reply" || action == "answer" {
				permitted["to"] = true
			}
			if action == "answer" && kind != "task" {
				permitted["resolve"] = true
			}
			if action == "ask" || action == "edit-post" {
				permitted["human-input"], permitted["no-human-input"], permitted["option"], permitted["clear-options"] = true, true, true, true
			}
			if action == "edit-post" {
				permitted["post"] = true
			}
		}
	}
	for k := range flags {
		if !permitted[k] {
			return a.fail(fmt.Errorf("--%s is not supported for %s %s", k, kind, action))
		}
	}
	for k := range values {
		if !permitted[k] {
			return a.fail(fmt.Errorf("--%s is not supported for %s %s", k, kind, action))
		}
	}
	for key, target := range map[string]*int64{"version": &in.Version, "after": &in.After, "history-before": &in.HistoryBefore, "entries-before": &in.EntriesBefore} {
		if _, ok := values[key]; ok {
			n, err := strconv.ParseInt(value(key), 10, 64)
			if err != nil || n < 0 {
				return a.fail(fmt.Errorf("--%s must be a non-negative integer", key))
			}
			*target = n
		}
	}
	if _, ok := values["limit"]; ok {
		n, err := strconv.Atoi(value("limit"))
		if err != nil || n < 1 || n > 100 {
			return a.fail(errors.New("--limit must be between 1 and 100"))
		}
		in.Limit = n
	}
	if action == "list" {
		if len(pos) != 1 {
			return a.fail(errors.New("unexpected list arguments"))
		}
		if flags["open"] && flags["resolved"] {
			return a.fail(errors.New("choose --open or --resolved"))
		}
		in.Archived = flags["archived"]
		in.FilterStatus = value("status")
		in.Worker = value("worker")
		in.Search = value("search")
		if flags["open"] {
			in.FilterStatus = "open"
		}
		if flags["resolved"] {
			in.FilterStatus = "resolved"
		}
		var page room.BoardPage
		if err := client.Call(ctx, kind+".list", in, &page); err != nil {
			return a.fail(err)
		}
		return a.print(page, jsonMode, func() {
			for _, t := range page.Items {
				fmt.Fprintf(a.stdout, "%s\tr%d\t%s\t%s\t%s\n", t.Key, t.Version, t.Status, t.Title, strings.Join(t.Workers, ", "))
			}
			if len(page.Items) == 0 {
				fmt.Fprintln(a.stdout, "no matching records")
			}
			if page.Next > 0 {
				fmt.Fprintf(a.stdout, "more records: --after %d\n", page.Next)
			}
		})
	}
	creating := action == "create" || (action == "ask" && kind == "question")
	if len(pos) < 2 {
		return a.fail(errors.New("record key or title is required"))
	}
	if creating {
		title := strings.Join(pos[1:], " ")
		in.Title = &title
	} else {
		in.Key = pos[1]
		if !posting && len(pos) > 2 {
			return a.fail(errors.New("unexpected arguments"))
		}
	}
	if action == "show" {
		var d room.BoardDetail
		if err := client.Call(ctx, kind+".show", in, &d); err != nil {
			return a.fail(err)
		}
		return a.print(d, jsonMode, func() { printBoardDetail(a.stdout, d) })
	}
	if flags["human-input"] && flags["no-human-input"] {
		return a.fail(errors.New("choose --human-input or --no-human-input"))
	}
	if _, ok := values["blocked"]; ok && flags["clear-blocked"] {
		return a.fail(errors.New("choose --blocked or --clear-blocked"))
	}
	if flags["human-input"] || flags["no-human-input"] {
		v := flags["human-input"]
		in.HumanInput = &v
	}
	for key, target := range map[string]**string{"title": &in.Title, "status": &in.Status, "blocked": &in.Blocker} {
		if _, ok := values[key]; ok {
			v := value(key)
			*target = &v
		}
	}
	if flags["clear-blocked"] {
		v := ""
		in.Blocker = &v
	}
	if flags["stdin"] {
		if posting && len(pos) > 2 {
			return a.fail(errors.New("answer text and --stdin cannot be combined"))
		}
		data, err := io.ReadAll(io.LimitReader(a.stdin, 16385))
		if err != nil {
			return a.fail(err)
		}
		if len(data) > 16384 {
			return a.fail(errors.New("stdin exceeds 16384 bytes"))
		}
		body := string(data)
		if posting {
			in.Body = body
		} else {
			in.Notes = &body
		}
	} else if posting {
		in.Body = strings.Join(pos[2:], " ")
	}
	if flags["clear-options"] && len(values["option"]) > 0 {
		return a.fail(errors.New("choose --option or --clear-options"))
	}
	if flags["clear-options"] || len(values["option"]) > 0 {
		options := append([]string{}, values["option"]...)
		in.Options = &options
	}
	for flag, target := range map[string]*int64{"to": &in.ReplyTo, "post": &in.Post} {
		if v := value(flag); v != "" {
			n, err := strconv.ParseInt(strings.TrimPrefix(v, "P-"), 10, 64)
			if err != nil || n < 1 {
				return a.fail(fmt.Errorf("--%s must be a positive post number, such as P-1", flag))
			}
			*target = n
		}
	}
	if kind != "question" && action == "answer" && in.ReplyTo == 0 {
		return a.fail(errors.New("answer requires --to P-N from show"))
	}
	in.Resolve = flags["resolve"]
	var current room.BoardDetail
	if !creating && (!posting || action == "edit-post" || in.Resolve || in.Version > 0) {
		if err := client.Call(ctx, kind+".show", room.BoardInput{Room: in.Room, Key: in.Key}, &current); err != nil {
			return a.fail(err)
		}
		if in.Version == 0 {
			in.Version = current.Item.Version
		}
	}
	if _, ok := values["assignee"]; ok {
		v := value("assignee")
		in.Assignee = &v
	}
	if _, ok := values["workers"]; ok {
		workers := []string{}
		for _, w := range strings.Split(value("workers"), ",") {
			if strings.TrimSpace(w) != "" {
				workers = append(workers, strings.TrimPrefix(strings.TrimSpace(w), "@"))
			}
		}
		in.Workers = &workers
	}
	in.AddWorkers = values["add-worker"]
	in.RemoveWorkers = values["remove-worker"]
	if _, ok := values["documents"]; ok {
		refs := []string{}
		for _, ref := range strings.Split(value("documents"), ",") {
			if strings.TrimSpace(ref) != "" {
				refs = append(refs, strings.TrimSpace(ref))
			}
		}
		in.Documents = &refs
	}
	in.LinkDocuments = values["link-document"]
	in.OperationID = value("operation-id")
	if in.OperationID == "" {
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return a.fail(err)
		}
		in.OperationID = hex.EncodeToString(b[:])
	}
	var result room.BoardDetail
	if err := client.Call(ctx, kind+"."+action, in, &result); err != nil {
		return a.fail(fmt.Errorf("%w (operation %s; expected revision %d)", err, in.OperationID, in.Version))
	}
	return a.print(result, jsonMode, func() {
		fmt.Fprintf(a.stdout, "%s · revision %d · %s · %s\noperation %s\n", result.Item.Key, result.Item.Version, result.Item.Status, result.Item.Title, result.OperationID)
		if len(result.History) > 0 && result.History[0].Revision == result.Item.Version && result.History[0].Sequence > 0 {
			fmt.Fprintf(a.stdout, "room event #%d posted\n", result.History[0].Sequence)
		} else {
			fmt.Fprintln(a.stdout, "record updated; no room message posted")
		}
	})
}

func printBoardDetail(w io.Writer, d room.BoardDetail) {
	t := d.Item
	fmt.Fprintf(w, "%s · revision %d · %s\n%s\n", t.Key, t.Version, t.Status, t.Title)
	if t.Kind == "task" || len(d.Entries) == 0 {
		fmt.Fprintln(w, t.Notes)
	}
	if t.Kind == "task" {
		fmt.Fprintf(w, "assignee: @%s\n", t.Assignee)
	} else {
		fmt.Fprintf(w, "OP: @%s\n", t.CreatedBy)
	}
	if len(t.Subscribers) > 0 {
		fmt.Fprintf(w, "subscribers (automatic): %s\n", strings.Join(t.Subscribers, ", "))
	}
	if t.Blocker != "" {
		fmt.Fprintf(w, "blocked: %s\n", t.Blocker)
	}
	if t.HumanInput {
		fmt.Fprintln(w, "awaiting human input; anyone may answer")
	}
	if t.Kind == "question" && t.Status == "resolved" {
		fmt.Fprintln(w, "resolved · read-only; reopen before editing or answering")
	}
	if t.Archived {
		fmt.Fprintln(w, "archived")
	}
	for _, doc := range t.Documents {
		fmt.Fprintf(w, "document: %s (%s)\n", doc.Name, doc.ID)
	}
	entries := append(append([]room.BoardEntry{}, d.PendingEntries...), d.Entries...)
	for _, e := range entries {
		fmt.Fprintf(w, "\nP-%d · @%s · %s", e.Number, e.Actor, e.Kind)
		if e.ReplyTo > 0 {
			fmt.Fprintf(w, " · reply to P-%d", e.ReplyTo)
		}
		if e.Answered {
			fmt.Fprint(w, " · answered")
		}
		fmt.Fprintf(w, "\n%s\n%s\n", e.Title, e.Body)
		if e.Kind == "question" && !e.Answered && t.Status != "resolved" {
			for _, option := range e.Options {
				fmt.Fprintf(w, "- %s\n", option)
			}
			fmt.Fprintf(w, "Answer this question with --to P-%d (choices or a custom answer).\n", e.Number)
		}
	}
	for _, h := range d.History {
		fmt.Fprintf(w, "\nr%d @%s · %s\n", h.Revision, h.Actor, h.Action)
		if h.Body != "" && len(d.Entries) == 0 {
			fmt.Fprintln(w, h.Body)
		}
	}
	if d.NextEntries > 0 {
		fmt.Fprintf(w, "earlier posts: --entries-before %d\n", d.NextEntries)
	}
	if d.NextHistory > 0 {
		fmt.Fprintf(w, "more history: --history-before %d\n", d.NextHistory)
	}
}
