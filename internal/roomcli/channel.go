package roomcli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"crewfold/internal/appdirs"
	"crewfold/internal/room"
)

type localClaudeBinding struct {
	Socket        string `json:"socket"`
	Room          string `json:"room"`
	RoomID        string `json:"room_id"`
	ParticipantID string `json:"participant_id"`
	Handle        string `json:"handle"`
	Directory     string `json:"directory"`
	Cursor        int64  `json:"cursor"`
}

type claudeChannelSetup struct {
	Config        string `json:"config"`
	Command       string `json:"command"`
	ResumeCommand string `json:"resume_command"`
	Room          string `json:"room"`
	ParticipantID string `json:"participant_id"`
	Handle        string `json:"handle"`
}

func (a *App) channel(ctx context.Context, local room.Client, args []string, paths appdirs.Paths, jsonMode bool) int {
	if len(args) < 2 || (args[0] != "setup" && args[0] != "run") {
		return a.fail(errors.New("usage: crewfold channel setup|run ROOM [--config PATH] [--binding PATH]"))
	}
	action, identifier := args[0], args[1]
	configPath, rest, err := pullOption(args[2:], "config")
	if err != nil {
		return a.fail(err)
	}
	bindingPath, rest, err := pullOption(rest, "binding")
	if err != nil {
		return a.fail(err)
	}
	if len(rest) != 0 {
		return a.fail(errors.New("usage: crewfold channel setup|run ROOM [--config PATH] [--binding PATH]"))
	}
	if a.program == "crewfold-connector" && configPath == "" && bindingPath == "" {
		configPath = paths.ConnectorConfigPath
	}
	if action == "run" {
		if jsonMode {
			return a.fail(errors.New("channel run is an MCP stdio command and cannot use --output json"))
		}
		backend, err := loadClaudeChannelBackend(configPath, bindingPath, paths, identifier)
		if err != nil {
			return a.fail(err)
		}
		if err := runClaudeChannel(ctx, backend, a.stdin, a.stdout, a.stderr); err != nil && !errors.Is(err, context.Canceled) {
			return a.fail(err)
		}
		return 0
	}
	directory, _ := os.Getwd()
	directory = filepath.Clean(directory)
	setup, err := setupClaudeChannel(ctx, local, paths, configPath, identifier, directory)
	if err != nil {
		return a.fail(err)
	}
	return a.print(setup, jsonMode, func() { printClaudeChannelSetup(a.stdout, setup) })
}

func setupClaudeChannel(ctx context.Context, local room.Client, paths appdirs.Paths, configPath, identifier, directory string) (claudeChannelSetup, error) {
	configPath, bindingPath, binding, err := prepareClaudeChannel(ctx, local, paths, configPath, identifier, directory)
	if err != nil {
		return claudeChannelSetup{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return claudeChannelSetup{}, err
	}
	if configPath != "" {
		if info, statErr := os.Stat(paths.ConnectorBinaryPath); statErr == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
			executable = paths.ConnectorBinaryPath
		}
	}
	channelDirectory := filepath.Join(paths.ConfigDir, "channels")
	if err := os.MkdirAll(channelDirectory, 0o700); err != nil {
		return claudeChannelSetup{}, err
	}
	mcpPath := filepath.Join(channelDirectory, binding.ParticipantID+".json")
	runArgs := []string{"channel", "run", binding.Room}
	if bindingPath != "" {
		runArgs = append(runArgs, "--binding", bindingPath)
	} else {
		runArgs = append(runArgs, "--config", configPath)
	}
	mcpConfig := map[string]any{"mcpServers": map[string]any{"crewfold": map[string]any{
		"command": executable,
		"args":    runArgs,
	}}}
	content, err := json.MarshalIndent(mcpConfig, "", "  ")
	if err != nil {
		return claudeChannelSetup{}, err
	}
	if err := writeAtomic(mcpPath, append(content, '\n')); err != nil {
		return claudeChannelSetup{}, err
	}
	command := "claude --mcp-config " + strconv.Quote(mcpPath) + " --dangerously-load-development-channels server:crewfold"
	return claudeChannelSetup{Config: mcpPath, Command: command, ResumeCommand: "claude --continue" + strings.TrimPrefix(command, "claude"), Room: binding.Room, ParticipantID: binding.ParticipantID, Handle: binding.Handle}, nil
}

func printClaudeChannelSetup(output io.Writer, setup claudeChannelSetup) {
	fmt.Fprintf(output, "Claude Channel ready for @%s. Exit the current Claude process, then resume it with:\n%s\n", setup.Handle, setup.ResumeCommand)
	fmt.Fprintln(output, "Accept Claude's one-time development-channel confirmation when it starts.")
}

func prepareClaudeChannel(ctx context.Context, local room.Client, paths appdirs.Paths, configPath, identifier, directory string) (string, string, remoteBinding, error) {
	if configPath != "" {
		config, err := loadRemoteConfig(configPath)
		if err != nil {
			return "", "", remoteBinding{}, err
		}
		if _, binding, exists, findErr := findRemoteBinding(config, identifier, directory); findErr != nil {
			return "", "", remoteBinding{}, findErr
		} else if exists {
			if binding.Provider != "claude" {
				return "", "", remoteBinding{}, errors.New("this participant did not join with --delivery claude")
			}
			return configPath, "", binding, nil
		}
		return "", "", remoteBinding{}, errors.New("this directory has no Claude delivery binding for that room")
	}
	var snapshot room.Snapshot
	if err := local.Call(ctx, "room.snapshot", room.ListMessagesInput{Room: identifier, Limit: 1}, &snapshot); err != nil {
		return "", "", remoteBinding{}, err
	}
	var participant *room.Participant
	for index := range snapshot.Participants {
		candidate := &snapshot.Participants[index]
		if candidate.Status == "joined" && filepath.Clean(candidate.WorkingDirectory) == directory {
			participant = candidate
			break
		}
	}
	if participant == nil {
		return "", "", remoteBinding{}, errors.New("this directory has not joined that local room; run crewfold room join ROOM --handle HANDLE --delivery claude first")
	}
	channelDirectory := filepath.Join(paths.ConfigDir, "channels")
	if err := os.MkdirAll(channelDirectory, 0o700); err != nil {
		return "", "", remoteBinding{}, err
	}
	bindingPath := filepath.Join(channelDirectory, participant.ID+".binding.json")
	cursor := snapshot.Room.LastSequence
	if existing, loadErr := loadLocalClaudeBinding(bindingPath); loadErr == nil && existing.ParticipantID == participant.ID {
		cursor = existing.Cursor
	} else if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
		return "", "", remoteBinding{}, loadErr
	}
	localBinding := localClaudeBinding{Socket: local.SocketPath, Room: snapshot.Room.Slug, RoomID: snapshot.Room.ID, ParticipantID: participant.ID, Handle: participant.Handle, Directory: directory, Cursor: cursor}
	if err := saveLocalClaudeBinding(bindingPath, localBinding); err != nil {
		return "", "", remoteBinding{}, err
	}
	return "", bindingPath, remoteBinding{Room: snapshot.Room.Slug, RoomID: snapshot.Room.ID, ParticipantID: participant.ID, Handle: participant.Handle, Directory: directory, Provider: "claude"}, nil
}

func loadLocalClaudeBinding(path string) (localClaudeBinding, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return localClaudeBinding{}, err
	}
	var binding localClaudeBinding
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&binding); err != nil {
		return localClaudeBinding{}, fmt.Errorf("read local Claude binding: %w", err)
	}
	if !filepath.IsAbs(binding.Socket) || !filepath.IsAbs(binding.Directory) || binding.Room == "" || binding.RoomID == "" || binding.ParticipantID == "" || binding.Handle == "" || binding.Cursor < 0 {
		return localClaudeBinding{}, errors.New("local Claude binding is invalid")
	}
	return binding, nil
}

func saveLocalClaudeBinding(path string, binding localClaudeBinding) error {
	content, err := json.MarshalIndent(binding, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeAtomic(path, append(content, '\n'))
}

type claudeChannelBackend interface {
	binding() remoteBinding
	callTool(context.Context, json.RawMessage) (any, error)
	deliveryLoop(context.Context, <-chan struct{}, *mcpStdio, io.Writer)
}

type remoteClaudeBackend struct {
	hub    remoteHub
	member remoteBinding
}

func (b remoteClaudeBackend) binding() remoteBinding { return b.member }
func (b remoteClaudeBackend) callTool(ctx context.Context, raw json.RawMessage) (any, error) {
	client := room.NetworkClient{BaseURL: b.hub.URL, Token: b.hub.Token, ParticipantID: b.member.ParticipantID}
	return callClaudeChannelTool(ctx, client, b.member, raw)
}
func (b remoteClaudeBackend) deliveryLoop(ctx context.Context, ready <-chan struct{}, transport *mcpStdio, stderr io.Writer) {
	claudeChannelDeliveryLoop(ctx, b.hub, b.member, ready, transport, stderr)
}

type localClaudeBackend struct {
	path   string
	member localClaudeBinding
}

func (b localClaudeBackend) binding() remoteBinding {
	return remoteBinding{Room: b.member.Room, RoomID: b.member.RoomID, ParticipantID: b.member.ParticipantID, Handle: b.member.Handle, Directory: b.member.Directory, Provider: "claude"}
}
func (b localClaudeBackend) callTool(ctx context.Context, raw json.RawMessage) (any, error) {
	client := room.Client{SocketPath: b.member.Socket}
	if err := ensureLocalClaudeParticipant(ctx, client, b.member); err != nil {
		return nil, err
	}
	return callClaudeChannelTool(ctx, client, b.binding(), raw)
}
func (b localClaudeBackend) deliveryLoop(ctx context.Context, ready <-chan struct{}, transport *mcpStdio, stderr io.Writer) {
	localClaudeChannelDeliveryLoop(ctx, b.path, b.member, ready, transport, stderr)
}

func loadClaudeChannelBackend(configPath, bindingPath string, paths appdirs.Paths, identifier string) (claudeChannelBackend, error) {
	if bindingPath != "" {
		binding, err := loadLocalClaudeBinding(bindingPath)
		if err != nil {
			return nil, err
		}
		if identifier != binding.Room && identifier != binding.RoomID {
			return nil, errors.New("local Claude binding belongs to another room")
		}
		return localClaudeBackend{path: bindingPath, member: binding}, nil
	}
	if configPath == "" {
		return nil, errors.New("Claude channel binding is missing")
	}
	config, err := loadRemoteConfig(configPath)
	if err != nil || config == nil {
		if err == nil {
			err = errors.New("Claude channel binding is missing; run crewfold channel setup ROOM")
		}
		return nil, err
	}
	directory, _ := os.Getwd()
	hub, binding, exists, err := findRemoteBinding(config, identifier, directory)
	if err != nil {
		return nil, err
	}
	if !exists || binding.Provider != "claude" {
		return nil, errors.New("this directory has no Claude delivery binding for that room")
	}
	return remoteClaudeBackend{hub: hub, member: binding}, nil
}

type mcpStdio struct {
	mu      sync.Mutex
	encoder *json.Encoder
}

func (m *mcpStdio) write(value any) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.encoder.Encode(value)
}

type mcpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func runClaudeChannel(ctx context.Context, backend claudeChannelBackend, stdin io.Reader, stdout, stderr io.Writer) error {
	channelCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	binding := backend.binding()
	transport := &mcpStdio{encoder: json.NewEncoder(stdout)}
	ready := make(chan struct{})
	var readyOnce sync.Once
	go backend.deliveryLoop(channelCtx, ready, transport, stderr)
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 4096), 2*1024*1024)
	for scanner.Scan() {
		var request mcpRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			continue
		}
		hasID := len(request.ID) > 0 && string(request.ID) != "null"
		respond := func(result any) error {
			if !hasID {
				return nil
			}
			return transport.write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(request.ID), "result": result})
		}
		respondError := func(code int, message string) error {
			if !hasID {
				return nil
			}
			return transport.write(map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(request.ID), "error": map[string]any{"code": code, "message": message}})
		}
		switch request.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string `json:"protocolVersion"`
			}
			_ = json.Unmarshal(request.Params, &params)
			if params.ProtocolVersion == "" {
				params.ProtocolVersion = "2025-06-18"
			}
			if err := respond(map[string]any{
				"protocolVersion": params.ProtocolVersion,
				"capabilities": map[string]any{
					"experimental": map[string]any{"claude/channel": map[string]any{}},
					"tools":        map[string]any{"listChanged": false},
				},
				"serverInfo":   map[string]any{"name": "crewfold", "version": "0.1.0"},
				"instructions": fmt.Sprintf("Events are shared-room activity for @%s in Crewfold room %s. Respond or act when directly addressed, collectively addressed, or required by your room role; otherwise stay silent. Use reply to publish a useful room response and publish_context to replace your visible current status. Do not narrate routine acknowledgements.", binding.Handle, binding.Room),
			}); err != nil {
				return err
			}
		case "notifications/initialized":
			readyOnce.Do(func() { close(ready) })
		case "ping":
			if err := respond(map[string]any{}); err != nil {
				return err
			}
		case "tools/list":
			if err := respond(map[string]any{"tools": claudeChannelTools()}); err != nil {
				return err
			}
		case "tools/call":
			result, err := backend.callTool(channelCtx, request.Params)
			if err != nil {
				if writeErr := respond(map[string]any{"content": []map[string]any{{"type": "text", "text": err.Error()}}, "isError": true}); writeErr != nil {
					return writeErr
				}
				continue
			}
			if err := respond(result); err != nil {
				return err
			}
		case "notifications/cancelled":
		case "notifications/roots/list_changed":
		default:
			if err := respondError(-32601, "method not found"); err != nil {
				return err
			}
		}
	}
	cancel()
	if err := scanner.Err(); err != nil {
		return err
	}
	return nil
}

func claudeChannelTools() []map[string]any {
	return []map[string]any{
		{
			"name": "reply", "description": "Publish a concise reply to the current Crewfold room",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}, "additionalProperties": false},
		},
		{
			"name": "publish_context", "description": "Replace this participant's visible current Crewfold context",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}, "required": []string{"text"}, "additionalProperties": false},
		},
		{
			"name": "read_room", "description": "Read the current canonical Crewfold room state and recent messages",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
		},
	}
}

func callClaudeChannelTool(ctx context.Context, client room.Caller, binding remoteBinding, raw json.RawMessage) (any, error) {
	var call struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(raw, &call); err != nil {
		return nil, errors.New("invalid tool call")
	}
	textResult := func(text string) map[string]any {
		return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}}
	}
	switch call.Name {
	case "reply", "publish_context":
		var arguments struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(call.Arguments, &arguments); err != nil || strings.TrimSpace(arguments.Text) == "" {
			return nil, errors.New("text is required")
		}
		kind := "message"
		if call.Name == "publish_context" {
			kind = "context"
		}
		var message room.Message
		if err := client.Call(ctx, "message.send", room.SendInput{Room: binding.RoomID, WorkingDirectory: binding.Directory, Handle: binding.Handle, Kind: kind, Body: arguments.Text}, &message); err != nil {
			return nil, err
		}
		return textResult(fmt.Sprintf("published Crewfold #%d", message.Sequence)), nil
	case "read_room":
		var snapshot room.Snapshot
		if err := client.Call(ctx, "room.snapshot", room.ListMessagesInput{Room: binding.RoomID, Limit: 50}, &snapshot); err != nil {
			return nil, err
		}
		var output strings.Builder
		fmt.Fprintf(&output, "# %s\n\n%s\n", snapshot.Room.Title, snapshot.Room.Topic)
		for _, message := range snapshot.Messages {
			fmt.Fprintf(&output, "\n## #%d · @%s · %s\n\n%s\n", message.Sequence, message.SenderHandle, room.MessageAttribution(message, snapshot.Participants), message.Body)
		}
		return textResult(output.String()), nil
	default:
		return nil, errors.New("unknown Crewfold channel tool")
	}
}

func ensureLocalClaudeParticipant(ctx context.Context, client room.Client, binding localClaudeBinding) error {
	var snapshot room.Snapshot
	if err := client.Call(ctx, "room.snapshot", room.ListMessagesInput{Room: binding.RoomID, Limit: 1}, &snapshot); err != nil {
		return err
	}
	for _, participant := range snapshot.Participants {
		if participant.ID == binding.ParticipantID && participant.Status == "joined" && filepath.Clean(participant.WorkingDirectory) == filepath.Clean(binding.Directory) {
			return nil
		}
	}
	return errors.New("local Claude participant has left this room")
}

func localClaudeChannelDeliveryLoop(ctx context.Context, bindingPath string, binding localClaudeBinding, ready <-chan struct{}, transport *mcpStdio, stderr io.Writer) {
	select {
	case <-ctx.Done():
		return
	case <-ready:
	}
	client := room.Client{SocketPath: binding.Socket}
	for ctx.Err() == nil {
		if err := ensureLocalClaudeParticipant(ctx, client, binding); err != nil {
			if ctx.Err() == nil {
				fmt.Fprintf(stderr, "crewfold channel: %v\n", err)
			}
			return
		}
		var snapshot room.Snapshot
		if err := client.Call(ctx, "room.snapshot", room.ListMessagesInput{Room: binding.RoomID, After: binding.Cursor, Limit: 100}, &snapshot); err != nil {
			if ctx.Err() != nil {
				return
			}
			fmt.Fprintf(stderr, "crewfold channel poll: %v\n", err)
			waitContext(ctx, 2*time.Second)
			continue
		}
		through := binding.Cursor
		messages := make([]room.Message, 0, len(snapshot.Messages))
		for _, message := range snapshot.Messages {
			if message.Sequence > through {
				through = message.Sequence
			}
			if message.SenderKind == "system" || message.ParticipantID == binding.ParticipantID {
				continue
			}
			messages = append(messages, message)
		}
		if through > binding.Cursor {
			emittedThrough := through
			if len(messages) > 0 {
				batch := room.NetworkDeliveryBatch{Room: snapshot.Room, Participant: room.Participant{ID: binding.ParticipantID, Handle: binding.Handle, HumanHandle: "owner"}, Participants: snapshot.Participants, Messages: messages, Through: through}
				content, includedThrough := networkDeliveryPrompt(batch)
				if err := transport.write(map[string]any{
					"jsonrpc": "2.0", "method": "notifications/claude/channel",
					"params": map[string]any{"content": content, "meta": map[string]string{
						"room": snapshot.Room.Slug, "participant": binding.Handle, "through": strconv.FormatInt(includedThrough, 10),
					}},
				}); err != nil {
					fmt.Fprintf(stderr, "crewfold channel notify: %v\n", err)
					return
				}
				emittedThrough = includedThrough
			}
			binding.Cursor = emittedThrough
			if err := saveLocalClaudeBinding(bindingPath, binding); err != nil {
				fmt.Fprintf(stderr, "crewfold channel cursor: %v\n", err)
				return
			}
		}
		waitContext(ctx, time.Second)
	}
}

func claudeChannelDeliveryLoop(ctx context.Context, hub remoteHub, binding remoteBinding, ready <-chan struct{}, transport *mcpStdio, stderr io.Writer) {
	select {
	case <-ctx.Done():
		return
	case <-ready:
	}
	client := room.NetworkClient{BaseURL: hub.URL, Token: hub.Token, ParticipantID: binding.ParticipantID}
	for ctx.Err() == nil {
		var batch room.NetworkDeliveryBatch
		if err := client.Call(ctx, "delivery.poll", room.NetworkDeliveryPollInput{ParticipantID: binding.ParticipantID, WaitSeconds: 25}, &batch); err != nil {
			if ctx.Err() != nil {
				return
			}
			if strings.Contains(err.Error(), "automatic delivery is not active") || strings.Contains(err.Error(), "binding not found") {
				return
			}
			fmt.Fprintf(stderr, "crewfold channel poll: %v\n", err)
			waitContext(ctx, 3*time.Second)
			continue
		}
		if batch.Through == 0 {
			continue
		}
		if len(batch.Messages) > 0 {
			content, deliveredThrough := networkDeliveryPrompt(batch)
			err := transport.write(map[string]any{
				"jsonrpc": "2.0", "method": "notifications/claude/channel",
				"params": map[string]any{"content": content, "meta": map[string]string{
					"room": batch.Room.Slug, "participant": batch.Participant.Handle, "through": strconv.FormatInt(deliveredThrough, 10),
				}},
			})
			if err != nil {
				fmt.Fprintf(stderr, "crewfold channel notify: %v\n", err)
				return
			}
			batch.Through = deliveredThrough
		}
		var ignored room.NetworkBinding
		if err := client.Call(ctx, "delivery.report", room.NetworkDeliveryResultInput{ParticipantID: binding.ParticipantID, Through: batch.Through, Status: "delivered"}, &ignored); err != nil {
			fmt.Fprintf(stderr, "crewfold channel report: %v\n", err)
			waitContext(ctx, 3*time.Second)
		}
	}
}
