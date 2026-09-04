package roomcli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"crewfold/internal/appdirs"
	"crewfold/internal/buildinfo"
	"crewfold/internal/room"
)

type App struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
	info   buildinfo.Info
}

func New(stdout, stderr io.Writer, info buildinfo.Info) *App {
	return &App{stdin: os.Stdin, stdout: stdout, stderr: stderr, info: info}
}

func (a *App) Run(ctx context.Context, args []string) int {
	jsonOutput, args, err := pullOption(args, "output")
	if err != nil {
		return a.fail(err)
	}
	jsonMode := jsonOutput == "json"
	if jsonOutput != "" && !jsonMode {
		return a.fail(errors.New("--output must be json"))
	}
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprint(a.stdout, rootHelp)
		return 0
	}
	paths, pathErr := appdirs.Default()
	if pathErr != nil {
		return a.fail(pathErr)
	}
	socket, remaining, err := pullOption(args[1:], "socket")
	if err != nil {
		return a.fail(err)
	}
	if socket == "" {
		socket = strings.TrimSpace(os.Getenv("CREWFOLD_SOCKET"))
	}
	if socket == "" {
		socket = paths.SocketPath
	}
	client := room.Client{SocketPath: socket}
	switch args[0] {
	case "version":
		return a.print(a.info, jsonMode, func() { fmt.Fprintf(a.stdout, "Crewfold %s (%s)\n", a.info.Version, a.info.Commit) })
	case "daemon":
		return a.daemon(ctx, append([]string{}, args[1:]...), paths)
	case "service":
		return a.service(ctx, remaining, paths, jsonMode)
	case "open":
		return a.open(ctx, client, jsonMode)
	case "status":
		var result struct {
			Status    string         `json:"status"`
			Rooms     int            `json:"rooms"`
			PID       int            `json:"pid"`
			StartedAt string         `json:"started_at"`
			Version   buildinfo.Info `json:"version"`
		}
		if err := client.Call(ctx, "status", map[string]any{}, &result); err != nil {
			return a.fail(err)
		}
		return a.print(result, jsonMode, func() {
			fmt.Fprintf(a.stdout, "Crewfold is online · %d rooms · pid %d\n", result.Rooms, result.PID)
		})
	case "room":
		return a.room(ctx, client, remaining, jsonMode)
	default:
		return a.fail(fmt.Errorf("unknown command %q; run crewfold help", args[0]))
	}
}

func (a *App) daemon(ctx context.Context, args []string, paths appdirs.Paths) int {
	if len(args) == 0 || args[0] != "run" {
		return a.fail(errors.New("usage: crewfold daemon run [--data-dir PATH] [--socket PATH] [--web-address 127.0.0.1:PORT]"))
	}
	dataDir, args, err := pullOption(args[1:], "data-dir")
	if err != nil {
		return a.fail(err)
	}
	socket, args, err := pullOption(args, "socket")
	if err != nil {
		return a.fail(err)
	}
	webAddress, args, err := pullOption(args, "web-address")
	if err != nil {
		return a.fail(err)
	}
	if len(args) != 0 {
		return a.fail(fmt.Errorf("unknown daemon options: %s", strings.Join(args, " ")))
	}
	if dataDir == "" {
		dataDir = paths.DataDir
	}
	if socket == "" {
		socket = paths.SocketPath
	}
	if webAddress == "" {
		webAddress = "127.0.0.1:0"
	}
	if err := room.RunServer(ctx, room.ServerConfig{DataDir: dataDir, SocketPath: socket, WebAddress: webAddress, Version: a.info}); err != nil {
		return a.fail(err)
	}
	return 0
}

func (a *App) service(ctx context.Context, args []string, paths appdirs.Paths, jsonMode bool) int {
	if len(args) != 1 {
		return a.fail(errors.New("usage: crewfold service install|start|stop|status"))
	}
	action := args[0]
	unit := "crewfold.service"
	if action == "install" {
		executable, err := os.Executable()
		if err != nil {
			return a.fail(err)
		}
		for _, directory := range []string{paths.StateDir, paths.RuntimeDir, filepath.Dir(paths.UnitPath)} {
			if err := os.MkdirAll(directory, 0o700); err != nil {
				return a.fail(err)
			}
			if err := os.Chmod(directory, 0o700); err != nil {
				return a.fail(err)
			}
		}
		content := `[Unit]
Description=Crewfold shared AI rooms
After=default.target

[Service]
Type=simple
ExecStart=` + systemdQuote(executable) + ` daemon run --data-dir ` + systemdQuote(paths.DataDir) + ` --socket ` + systemdQuote(paths.SocketPath) + ` --web-address 127.0.0.1:0
Restart=on-failure
RestartSec=2s
UMask=0077
NoNewPrivileges=true
RuntimeDirectory=crewfold
RuntimeDirectoryMode=0700

[Install]
WantedBy=default.target
`
		if err := writeAtomic(paths.UnitPath, []byte(content)); err != nil {
			return a.fail(err)
		}
		for _, command := range [][]string{{"daemon-reload"}, {"enable", unit}, {"restart", unit}} {
			if output, err := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, command...)...).CombinedOutput(); err != nil {
				return a.fail(fmt.Errorf("systemctl %s: %s: %w", strings.Join(command, " "), strings.TrimSpace(string(output)), err))
			}
		}
	} else if action == "start" || action == "stop" {
		if output, err := exec.CommandContext(ctx, "systemctl", "--user", action, unit).CombinedOutput(); err != nil {
			return a.fail(fmt.Errorf("systemctl %s: %s: %w", action, strings.TrimSpace(string(output)), err))
		}
	} else if action != "status" {
		return a.fail(errors.New("usage: crewfold service install|start|stop|status"))
	}
	output, err := exec.CommandContext(ctx, "systemctl", "--user", "show", "--property=ActiveState", "--value", unit).CombinedOutput()
	if err != nil {
		return a.fail(fmt.Errorf("inspect service: %s: %w", strings.TrimSpace(string(output)), err))
	}
	result := map[string]any{"action": action, "status": strings.TrimSpace(string(output)), "data_dir": paths.DataDir, "socket": paths.SocketPath, "unit": paths.UnitPath}
	return a.print(result, jsonMode, func() {
		fmt.Fprintf(a.stdout, "Crewfold service: %s\ndata: %s\nsocket: %s\n", result["status"], paths.DataDir, paths.SocketPath)
	})
}

func (a *App) open(ctx context.Context, client room.Client, jsonMode bool) int {
	var bootstrap struct {
		URL       string `json:"url"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := client.Call(ctx, "web.bootstrap", map[string]any{}, &bootstrap); err != nil {
		return a.fail(err)
	}
	parsed, err := url.Parse(bootstrap.URL)
	if err != nil || parsed.Hostname() != "127.0.0.1" || !strings.HasPrefix(parsed.Fragment, "bootstrap=") {
		return a.fail(errors.New("daemon returned an invalid local web URL"))
	}
	if !jsonMode {
		command := exec.CommandContext(ctx, "xdg-open", bootstrap.URL)
		command.Stdout, command.Stderr = io.Discard, io.Discard
		if err := command.Run(); err != nil {
			return a.fail(err)
		}
	}
	return a.print(bootstrap, jsonMode, func() { parsed.Fragment = ""; fmt.Fprintf(a.stdout, "Crewfold opened at %s\n", parsed.String()) })
}

func (a *App) room(ctx context.Context, client room.Client, args []string, jsonMode bool) int {
	if len(args) == 0 {
		fmt.Fprint(a.stdout, roomHelp)
		return 0
	}
	switch args[0] {
	case "create":
		if len(args) < 2 {
			return a.fail(errors.New("usage: crewfold room create SLUG [--title TITLE] [--topic TOPIC]"))
		}
		title, rest, err := pullOption(args[2:], "title")
		if err != nil {
			return a.fail(err)
		}
		topic, rest, err := pullOption(rest, "topic")
		if err != nil {
			return a.fail(err)
		}
		if len(rest) != 0 {
			return a.fail(fmt.Errorf("unknown options: %s", strings.Join(rest, " ")))
		}
		if title == "" {
			title = strings.ReplaceAll(args[1], "-", " ")
		}
		var snapshot room.Snapshot
		if err := client.Call(ctx, "room.create", room.CreateRoomInput{Slug: args[1], Title: title, Topic: topic}, &snapshot); err != nil {
			return a.fail(err)
		}
		return a.print(snapshot, jsonMode, func() {
			fmt.Fprintf(a.stdout, "room %s created\n", snapshot.Room.Slug)
		})
	case "steward":
		return a.roomSteward(ctx, client, args[1:], jsonMode)
	case "list":
		var rooms []room.Room
		if err := client.Call(ctx, "room.list", map[string]any{}, &rooms); err != nil {
			return a.fail(err)
		}
		return a.print(rooms, jsonMode, func() {
			if len(rooms) == 0 {
				fmt.Fprintln(a.stdout, "no rooms yet")
				return
			}
			for _, item := range rooms {
				fmt.Fprintf(a.stdout, "%s\t%s\t%s\t%d messages\n", item.Slug, item.Status, item.Title, item.LastSequence)
			}
		})
	case "show", "status":
		identifier, rest, err := leading(args[1:], "room")
		if err != nil {
			return a.fail(err)
		}
		if len(rest) != 0 {
			return a.fail(errors.New("unexpected arguments"))
		}
		var snapshot room.Snapshot
		if err := client.Call(ctx, "room.snapshot", room.ListMessagesInput{Room: identifier, Limit: 50}, &snapshot); err != nil {
			return a.fail(err)
		}
		return a.print(snapshot, jsonMode, func() { printSnapshot(a.stdout, snapshot, false) })
	case "join":
		identifier, rest, err := leading(args[1:], "room")
		if err != nil {
			return a.fail(err)
		}
		handle, rest, err := pullOption(rest, "handle")
		if err != nil {
			return a.fail(err)
		}
		name, rest, err := pullOption(rest, "name")
		if err != nil {
			return a.fail(err)
		}
		kind, rest, err := pullOption(rest, "kind")
		if err != nil {
			return a.fail(err)
		}
		cwd, rest, err := pullOption(rest, "cwd")
		if err != nil {
			return a.fail(err)
		}
		delivery, rest, err := pullOption(rest, "delivery")
		if err != nil {
			return a.fail(err)
		}
		if len(rest) != 0 || handle == "" {
			return a.fail(errors.New("usage: crewfold room join ROOM --handle HANDLE [--name NAME] [--kind agent|steward] [--cwd PATH] [--delivery codex|none]"))
		}
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		if delivery == "" {
			delivery = "codex"
		}
		threadID := ""
		if delivery == "codex" {
			threadID = strings.TrimSpace(os.Getenv("CODEX_THREAD_ID"))
			if threadID == "" {
				return a.fail(errors.New("Codex delivery is the default, but CODEX_THREAD_ID is unavailable; run join from inside the Codex session or use --delivery none"))
			}
		} else if delivery != "none" {
			return a.fail(errors.New("--delivery must be codex or none"))
		}
		var participant room.Participant
		if err := client.Call(ctx, "participant.join", room.JoinInput{Room: identifier, Handle: handle, DisplayName: name, WorkingDirectory: cwd, Kind: kind, Delivery: delivery, ThreadID: threadID}, &participant); err != nil {
			return a.fail(err)
		}
		return a.print(participant, jsonMode, func() {
			fmt.Fprintf(a.stdout, "joined %s as @%s from %s · delivery %s\n", identifier, participant.Handle, participant.WorkingDirectory, delivery)
			fmt.Fprintf(a.stdout, "room messages render GitHub-flavored Markdown; use `crewfold room send %s --stdin` for readable multiline posts\n", identifier)
		})
	case "send", "context":
		identifier, rest, err := leading(args[1:], "room")
		if err != nil {
			return a.fail(err)
		}
		fromStdin, rest, err := pullFlag(rest, "stdin")
		if err != nil {
			return a.fail(err)
		}
		if fromStdin && len(rest) != 0 {
			return a.fail(errors.New("message text and --stdin cannot be used together"))
		}
		body := strings.Join(rest, " ")
		if fromStdin {
			content, readErr := io.ReadAll(io.LimitReader(a.stdin, 16385))
			if readErr != nil {
				return a.fail(fmt.Errorf("read message from stdin: %w", readErr))
			}
			if len(content) > 16384 {
				return a.fail(errors.New("message from stdin exceeds 16384 bytes"))
			}
			body = string(content)
		}
		if strings.TrimSpace(body) == "" {
			return a.fail(errors.New("message text is required"))
		}
		if args[0] == "send" {
			if err := validateMessageReadability(identifier, body, fromStdin); err != nil {
				return a.fail(err)
			}
		}
		cwd, _ := os.Getwd()
		kind := "message"
		if args[0] == "context" {
			kind = "context"
		}
		var message room.Message
		if err := client.Call(ctx, "message.send", room.SendInput{Room: identifier, WorkingDirectory: cwd, Kind: kind, Body: body}, &message); err != nil {
			return a.fail(err)
		}
		return a.print(message, jsonMode, func() { fmt.Fprintf(a.stdout, "#%d @%s: %s\n", message.Sequence, message.SenderHandle, message.Body) })
	case "read":
		identifier, rest, err := leading(args[1:], "room")
		if err != nil {
			return a.fail(err)
		}
		afterText, rest, err := pullOption(rest, "after")
		if err != nil {
			return a.fail(err)
		}
		if len(rest) != 0 {
			return a.fail(errors.New("unexpected arguments"))
		}
		after := int64(0)
		if afterText != "" {
			after, err = strconv.ParseInt(afterText, 10, 64)
			if err != nil {
				return a.fail(errors.New("--after must be an integer"))
			}
		}
		var snapshot room.Snapshot
		if err := client.Call(ctx, "room.snapshot", room.ListMessagesInput{Room: identifier, After: after, Limit: 500}, &snapshot); err != nil {
			return a.fail(err)
		}
		if !jsonMode {
			for _, message := range snapshot.Messages {
				printMessage(a.stdout, message)
			}
		}
		if cwd, cwdErr := os.Getwd(); cwdErr == nil && snapshot.Room.LastSequence > 0 {
			var ignored room.Participant
			_ = client.Call(ctx, "participant.ack", room.AckInput{Room: identifier, WorkingDirectory: cwd, Through: snapshot.Room.LastSequence}, &ignored)
		}
		if jsonMode {
			return a.print(snapshot, true, func() {})
		}
		return 0
	case "watch":
		identifier, rest, err := leading(args[1:], "room")
		if err != nil {
			return a.fail(err)
		}
		afterText, rest, err := pullOption(rest, "after")
		if err != nil {
			return a.fail(err)
		}
		if len(rest) != 0 {
			return a.fail(errors.New("unexpected arguments"))
		}
		after := int64(0)
		if afterText != "" {
			after, err = strconv.ParseInt(afterText, 10, 64)
			if err != nil {
				return a.fail(err)
			}
		}
		return a.watch(ctx, client, identifier, after, jsonMode)
	case "ack":
		identifier, rest, err := leading(args[1:], "room")
		if err != nil {
			return a.fail(err)
		}
		throughText, rest, err := pullOption(rest, "through")
		if err != nil {
			return a.fail(err)
		}
		if len(rest) != 0 {
			return a.fail(errors.New("unexpected arguments"))
		}
		through := int64(0)
		if throughText != "" {
			through, err = strconv.ParseInt(throughText, 10, 64)
			if err != nil {
				return a.fail(err)
			}
		}
		cwd, _ := os.Getwd()
		var participant room.Participant
		if err := client.Call(ctx, "participant.ack", room.AckInput{Room: identifier, WorkingDirectory: cwd, Through: through}, &participant); err != nil {
			return a.fail(err)
		}
		return a.print(participant, jsonMode, func() {
			fmt.Fprintf(a.stdout, "@%s acknowledged through #%d\n", participant.Handle, participant.LastReadSequence)
		})
	case "upload":
		if len(args) < 3 {
			return a.fail(errors.New("usage: crewfold room upload ROOM FILE [--caption TEXT]"))
		}
		identifier, path := args[1], args[2]
		caption, rest, err := pullOption(args[3:], "caption")
		if err != nil {
			return a.fail(err)
		}
		if len(rest) != 0 {
			return a.fail(errors.New("unexpected arguments"))
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return a.fail(err)
		}
		mediaType := mime.TypeByExtension(filepath.Ext(path))
		cwd, _ := os.Getwd()
		var message room.Message
		if err := client.Call(ctx, "document.upload", room.UploadInput{Room: identifier, WorkingDirectory: cwd, Name: filepath.Base(path), MediaType: mediaType, ContentBase64: base64.StdEncoding.EncodeToString(content), Caption: caption}, &message); err != nil {
			return a.fail(err)
		}
		return a.print(message, jsonMode, func() { fmt.Fprintf(a.stdout, "uploaded %s as %s\n", message.Document.Name, message.Document.ID) })
	case "document":
		if len(args) < 3 {
			return a.fail(errors.New("usage: crewfold room document ROOM DOCUMENT [--to PATH]"))
		}
		to, rest, err := pullOption(args[3:], "to")
		if err != nil {
			return a.fail(err)
		}
		if len(rest) != 0 {
			return a.fail(errors.New("unexpected arguments"))
		}
		var result struct {
			Document      room.Document `json:"document"`
			ContentBase64 string        `json:"content_base64"`
		}
		if err := client.Call(ctx, "document.read", map[string]any{"room": args[1], "document": args[2]}, &result); err != nil {
			return a.fail(err)
		}
		if jsonMode {
			return a.print(result, true, func() {})
		}
		content, err := base64.StdEncoding.DecodeString(result.ContentBase64)
		if err != nil {
			return a.fail(err)
		}
		if to != "" {
			if err := os.WriteFile(to, content, 0o600); err != nil {
				return a.fail(err)
			}
			fmt.Fprintf(a.stdout, "wrote %s\n", to)
		} else {
			_, _ = a.stdout.Write(content)
			if len(content) == 0 || content[len(content)-1] != '\n' {
				fmt.Fprintln(a.stdout)
			}
		}
		return 0
	case "archive":
		identifier, rest, err := leading(args[1:], "room")
		if err != nil {
			return a.fail(err)
		}
		if len(rest) != 0 {
			return a.fail(errors.New("unexpected arguments"))
		}
		var result room.Room
		if err := client.Call(ctx, "room.archive", map[string]any{"room": identifier}, &result); err != nil {
			return a.fail(err)
		}
		return a.print(result, jsonMode, func() { fmt.Fprintf(a.stdout, "room %s archived\n", result.Slug) })
	default:
		return a.fail(fmt.Errorf("unknown room command %q", args[0]))
	}
}

func (a *App) roomSteward(ctx context.Context, client room.Client, args []string, jsonMode bool) int {
	if len(args) == 0 {
		return a.fail(errors.New("usage: crewfold room steward start|status|prompt|key|stop|restart ROOM"))
	}
	action := args[0]
	identifier, rest, err := leading(args[1:], "room")
	if err != nil {
		return a.fail(err)
	}
	switch action {
	case "start":
		handle, rest, err := pullOption(rest, "handle")
		if err != nil {
			return a.fail(err)
		}
		name, rest, err := pullOption(rest, "name")
		if err != nil {
			return a.fail(err)
		}
		role, rest, err := pullOption(rest, "role")
		if err != nil {
			return a.fail(err)
		}
		cwd, rest, err := pullOption(rest, "cwd")
		if err != nil {
			return a.fail(err)
		}
		if len(rest) != 0 || handle == "" {
			return a.fail(errors.New("usage: crewfold room steward start ROOM --handle HANDLE [--name NAME] [--role ROLE] [--cwd PATH]"))
		}
		var steward room.HostedSteward
		if err := client.Call(ctx, "steward.start", room.StartStewardInput{Room: identifier, Handle: handle, DisplayName: name, Role: role, WorkingDirectory: cwd}, &steward); err != nil {
			return a.fail(err)
		}
		return a.print(steward, jsonMode, func() {
			fmt.Fprintf(a.stdout, "@%s is starting in Herdr session %s\n", steward.Handle, steward.HerdrSession)
		})
	case "status":
		if len(rest) != 0 {
			return a.fail(errors.New("unexpected arguments"))
		}
		var console *room.StewardConsole
		if err := client.Call(ctx, "steward.status", map[string]any{"room": identifier}, &console); err != nil {
			return a.fail(err)
		}
		if console == nil {
			return a.print(console, jsonMode, func() { fmt.Fprintln(a.stdout, "no hosted steward") })
		}
		return a.print(console, jsonMode, func() {
			fmt.Fprintf(a.stdout, "@%s\t%s\t%s\t%s\n", console.Steward.Handle, console.Steward.Status, console.Steward.AgentStatus, console.Steward.HerdrSession)
		})
	case "prompt":
		if len(rest) == 0 {
			return a.fail(errors.New("prompt text is required"))
		}
		var accepted map[string]any
		if err := client.Call(ctx, "steward.prompt", room.PromptStewardInput{Room: identifier, Text: strings.Join(rest, " ")}, &accepted); err != nil {
			return a.fail(err)
		}
		return a.print(accepted, jsonMode, func() { fmt.Fprintln(a.stdout, "prompt sent to hosted steward") })
	case "key":
		if len(rest) != 1 {
			return a.fail(errors.New("usage: crewfold room steward key ROOM enter|esc|ctrl+c"))
		}
		var accepted map[string]any
		if err := client.Call(ctx, "steward.key", room.StewardKeyInput{Room: identifier, Key: rest[0]}, &accepted); err != nil {
			return a.fail(err)
		}
		return a.print(accepted, jsonMode, func() { fmt.Fprintln(a.stdout, "key sent to hosted steward") })
	case "stop", "restart":
		if len(rest) != 0 {
			return a.fail(errors.New("unexpected arguments"))
		}
		var steward room.HostedSteward
		if err := client.Call(ctx, "steward."+action, map[string]any{"room": identifier}, &steward); err != nil {
			return a.fail(err)
		}
		return a.print(steward, jsonMode, func() { fmt.Fprintf(a.stdout, "hosted steward %s\n", steward.Status) })
	default:
		return a.fail(fmt.Errorf("unknown steward command %q", action))
	}
}

func (a *App) watch(ctx context.Context, client room.Client, identifier string, after int64, jsonMode bool) int {
	if jsonMode {
		return a.fail(errors.New("room watch is a streaming text command; use room read --output json for automation"))
	}
	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()
	cwd, _ := os.Getwd()
	for {
		var snapshot room.Snapshot
		if err := client.Call(ctx, "room.snapshot", room.ListMessagesInput{Room: identifier, After: after, Limit: 500}, &snapshot); err != nil {
			return a.fail(err)
		}
		for _, message := range snapshot.Messages {
			printMessage(a.stdout, message)
			if message.Sequence > after {
				after = message.Sequence
			}
		}
		if after > 0 {
			var ignored room.Participant
			_ = client.Call(ctx, "participant.ack", room.AckInput{Room: identifier, WorkingDirectory: cwd, Through: after}, &ignored)
		}
		select {
		case <-ctx.Done():
			return 0
		case <-ticker.C:
		}
	}
}

func printSnapshot(output io.Writer, snapshot room.Snapshot, messages bool) {
	fmt.Fprintf(output, "%s · %s\n%s\n\nparticipants\n", snapshot.Room.Title, snapshot.Room.Status, snapshot.Room.Topic)
	for _, participant := range snapshot.Participants {
		context := ""
		if participant.Context != "" {
			context = " · " + participant.Context
		}
		fmt.Fprintf(output, "@%s\t%s\t%s\t%s\t%d unread%s\n", participant.Handle, participant.Kind, participant.Status, participant.WorkingDirectory, participant.UnreadCount, context)
	}
	documentNames := make(map[string]struct{}, len(snapshot.Documents))
	for _, document := range snapshot.Documents {
		documentNames[document.Name] = struct{}{}
	}
	fmt.Fprintf(output, "\n%d documents · %d revisions · %d messages\n", len(documentNames), len(snapshot.Documents), snapshot.Room.LastSequence)
	if messages {
		for _, message := range snapshot.Messages {
			printMessage(output, message)
		}
	}
}

func printMessage(output io.Writer, message room.Message) {
	marker := ""
	if message.Kind == "context" {
		marker = " [context]"
	}
	if message.Kind == "document" && message.Document != nil {
		marker = " [document: " + message.Document.Name + "]"
	}
	fmt.Fprintf(output, "#%d %s @%s%s\n%s\n", message.Sequence, message.CreatedAt, message.SenderHandle, marker, message.Body)
}

func (a *App) print(value any, jsonMode bool, text func()) int {
	if jsonMode {
		encoder := json.NewEncoder(a.stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(value); err != nil {
			return a.fail(err)
		}
	} else {
		text()
	}
	return 0
}
func (a *App) fail(err error) int { fmt.Fprintf(a.stderr, "crewfold: %v\n", err); return 1 }

func pullOption(args []string, name string) (string, []string, error) {
	result := ""
	remaining := []string{}
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--"+name {
			if result != "" || index+1 >= len(args) {
				return "", nil, fmt.Errorf("--%s requires one value", name)
			}
			result = args[index+1]
			index++
			continue
		}
		if strings.HasPrefix(argument, "--"+name+"=") {
			if result != "" {
				return "", nil, fmt.Errorf("--%s was provided more than once", name)
			}
			result = strings.TrimPrefix(argument, "--"+name+"=")
			continue
		}
		remaining = append(remaining, argument)
	}
	return result, remaining, nil
}
func pullFlag(args []string, name string) (bool, []string, error) {
	found := false
	remaining := []string{}
	for _, argument := range args {
		if argument == "--"+name {
			if found {
				return false, nil, fmt.Errorf("--%s was provided more than once", name)
			}
			found = true
			continue
		}
		if strings.HasPrefix(argument, "--"+name+"=") {
			return false, nil, fmt.Errorf("--%s does not take a value", name)
		}
		remaining = append(remaining, argument)
	}
	return found, remaining, nil
}

func validateMessageReadability(roomIdentifier, body string, fromStdin bool) error {
	body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	count := utf8.RuneCountInString(body)
	if count > room.MaximumInlineMessageRunes && !fromStdin {
		return fmt.Errorf("message is %d characters; substantial room posts must use `crewfold room send %s --stdin` with short Markdown paragraphs or bullets", count, roomIdentifier)
	}
	if err := room.ValidateSharedMessage(body); err != nil {
		return fmt.Errorf("%w; use `crewfold room send %s --stdin`", err, roomIdentifier)
	}
	return nil
}

func leading(args []string, name string) (string, []string, error) {
	if len(args) == 0 || strings.HasPrefix(args[0], "--") {
		return "", nil, fmt.Errorf("%s is required", name)
	}
	return args[0], args[1:], nil
}

func writeAtomic(path string, content []byte) error {
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, content, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
func systemdQuote(value string) string { return strings.ReplaceAll(strconv.Quote(value), "%", "%%") }

const rootHelp = `Crewfold is a shared room for independently run AI sessions.

Usage:
  crewfold service install|start|stop|status
  crewfold open
  crewfold status
  crewfold room COMMAND
  crewfold daemon run

Run 'crewfold room' for room commands.
`

const roomHelp = `Room commands:
  crewfold room create SLUG [--title TITLE] [--topic TOPIC]
  crewfold room list
  crewfold room show ROOM
  crewfold room join ROOM --handle HANDLE [--name NAME] [--kind agent|steward] [--delivery codex|none]
  crewfold room send ROOM MESSAGE... | --stdin
  crewfold room context ROOM CURRENT-CONTEXT... | --stdin
  crewfold room read ROOM [--after SEQUENCE]
  crewfold room watch ROOM [--after SEQUENCE]
  crewfold room ack ROOM [--through SEQUENCE]
  crewfold room upload ROOM FILE [--caption TEXT]
  crewfold room document ROOM DOCUMENT [--to PATH]
  crewfold room steward start ROOM --handle HANDLE [--name NAME] [--role ROLE] [--cwd PATH]
  crewfold room steward status ROOM
  crewfold room steward prompt ROOM MESSAGE...
  crewfold room steward key ROOM enter|esc|ctrl+c
  crewfold room steward stop ROOM
  crewfold room steward restart ROOM
  crewfold room archive ROOM

Codex delivery is the join default and binds the current CODEX_THREAD_ID. Use
--delivery none only for a participant that cannot receive Codex prompts.
Participant commands identify the current session by its joined working directory.
`
