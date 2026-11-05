package roomcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"crewfold/internal/codexapp"
	"crewfold/internal/room"
)

const remoteConfigVersion = 1

// remoteConfig is machine-level connector state. Hubs are independent: one
// connector service reconciles every Codex binding across every enrolled hub.
type remoteConfig struct {
	Version int         `json:"version"`
	Hubs    []remoteHub `json:"hubs,omitempty"`
}

type remoteHub struct {
	URL        string          `json:"url"`
	DeviceID   string          `json:"device_id"`
	DeviceName string          `json:"device_name"`
	Token      string          `json:"token"`
	Rooms      []remoteRoom    `json:"rooms,omitempty"`
	Bindings   []remoteBinding `json:"bindings,omitempty"`
}

type remoteRoom struct {
	ID    string `json:"id"`
	Slug  string `json:"slug"`
	Title string `json:"title,omitempty"`
}

type remoteBinding struct {
	Room          string `json:"room"`
	RoomID        string `json:"room_id"`
	ParticipantID string `json:"participant_id"`
	Handle        string `json:"handle"`
	Directory     string `json:"directory"`
	Provider      string `json:"provider"`
	Target        string `json:"target,omitempty"`
}

func loadRemoteConfig(path string) (*remoteConfig, error) {
	content, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var config remoteConfig
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("read connector configuration: %w", err)
	}
	if err := validateRemoteConfig(config); err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	return &config, nil
}

func validateRemoteConfig(config remoteConfig) error {
	if config.Version != remoteConfigVersion {
		return errors.New("connector configuration has an unsupported version")
	}
	seenURLs := make(map[string]struct{}, len(config.Hubs))
	for _, hub := range config.Hubs {
		normalized, err := room.NormalizeNetworkURL(hub.URL)
		if err != nil || normalized != hub.URL || hub.DeviceID == "" || hub.Token == "" {
			return errors.New("connector configuration contains an invalid hub")
		}
		if _, exists := seenURLs[hub.URL]; exists {
			return errors.New("connector configuration contains a duplicate hub")
		}
		seenURLs[hub.URL] = struct{}{}
		seenRooms := make(map[string]struct{}, len(hub.Rooms))
		for _, membership := range hub.Rooms {
			if membership.ID == "" || membership.Slug == "" {
				return errors.New("connector configuration contains an invalid room membership")
			}
			if _, exists := seenRooms[membership.ID]; exists {
				return errors.New("connector configuration contains a duplicate room membership")
			}
			seenRooms[membership.ID] = struct{}{}
		}
		seenParticipants := make(map[string]struct{}, len(hub.Bindings))
		for _, binding := range hub.Bindings {
			if binding.Room == "" || binding.RoomID == "" || binding.ParticipantID == "" || binding.Handle == "" || !filepath.IsAbs(binding.Directory) {
				return errors.New("connector configuration contains an invalid agent binding")
			}
			if binding.Provider != "codex" && binding.Provider != "claude" && binding.Provider != "none" {
				return errors.New("connector configuration contains an invalid delivery provider")
			}
			if binding.Provider == "codex" && binding.Target == "" {
				return errors.New("connector configuration contains a Codex binding without a local thread")
			}
			if _, exists := seenParticipants[binding.ParticipantID]; exists {
				return errors.New("connector configuration contains a duplicate participant binding")
			}
			seenParticipants[binding.ParticipantID] = struct{}{}
		}
	}
	return nil
}

func saveRemoteConfig(path string, config *remoteConfig) error {
	if config == nil {
		return errors.New("connector configuration is required")
	}
	config.Version = remoteConfigVersion
	if err := validateRemoteConfig(*config); err != nil {
		return err
	}
	content, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	content = append(content, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return writeAtomic(path, content)
}

// updateRemoteConfig serializes mutations from independently running agents.
// Reads remain lock-free because saveRemoteConfig publishes by atomic rename.
func updateRemoteConfig(path string, update func(*remoteConfig) error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	config, err := loadRemoteConfig(path)
	if err != nil {
		return err
	}
	if config == nil {
		config = &remoteConfig{Version: remoteConfigVersion}
	}
	if err := update(config); err != nil {
		return err
	}
	return saveRemoteConfig(path, config)
}

type routedCaller struct {
	configPath string
}

func (c *routedCaller) isRemote() bool { return c != nil }

type remoteRoute struct {
	hub     remoteHub
	binding *remoteBinding
}

func (c *routedCaller) Call(ctx context.Context, method string, params any, result any) error {
	config, err := loadRemoteConfig(c.configPath)
	if err != nil {
		return err
	}
	if method == "room.list" {
		return listRemoteRooms(ctx, config, result)
	}
	if config == nil || len(config.Hubs) == 0 {
		return errors.New("this machine has not joined any Crewfold rooms; use the room invitation command")
	}
	directory, _ := os.Getwd()
	directory = filepath.Clean(directory)
	identifier := roomIdentifierFromParams(params)
	route, err := resolveRemoteRoute(*config, method, identifier, directory, params)
	if err != nil {
		return err
	}
	participantID := ""
	if route.binding != nil {
		participantID = route.binding.ParticipantID
	}
	client := room.NetworkClient{BaseURL: route.hub.URL, Token: route.hub.Token, ParticipantID: participantID}
	remoteParams := params
	var joinInput room.JoinInput
	joining := method == "participant.join"
	leaving := method == "participant.leave"
	if joining {
		var ok bool
		joinInput, ok = params.(room.JoinInput)
		if !ok {
			return errors.New("remote join input is invalid")
		}
		if joinInput.Delivery == "codex" {
			if err := validateLocalCodexThread(ctx, joinInput.ThreadID); err != nil {
				return fmt.Errorf("bind local Codex delivery: %w", err)
			}
		}
		joinInput.ThreadID = ""
		remoteParams = joinInput
	}
	if err := client.Call(ctx, method, remoteParams, result); err != nil {
		return err
	}
	if leaving && route.binding != nil {
		participantID := route.binding.ParticipantID
		return updateRemoteConfig(c.configPath, func(latest *remoteConfig) error {
			hub := findRemoteHub(latest, route.hub.URL)
			if hub == nil {
				return errors.New("connector hub disappeared while leaving the room")
			}
			kept := hub.Bindings[:0]
			for _, candidate := range hub.Bindings {
				if candidate.ParticipantID != participantID {
					kept = append(kept, candidate)
				}
			}
			hub.Bindings = kept
			return nil
		})
	}
	if joining {
		participant, ok := result.(*room.Participant)
		if !ok || participant == nil {
			return errors.New("remote join returned no participant")
		}
		updated := remoteBinding{
			Room: joinInput.Room, RoomID: participant.RoomID, ParticipantID: participant.ID,
			Handle: participant.Handle, Directory: directory, Provider: joinInput.Delivery,
		}
		if updated.Provider == "" {
			updated.Provider = "codex"
		}
		if updated.Provider == "codex" {
			updated.Target = params.(room.JoinInput).ThreadID
		}
		return updateRemoteConfig(c.configPath, func(latest *remoteConfig) error {
			hub := findRemoteHub(latest, route.hub.URL)
			if hub == nil {
				return errors.New("connector hub disappeared while saving the agent binding")
			}
			upsertRemoteBinding(hub, updated)
			return nil
		})
	}
	return nil
}

func listRemoteRooms(ctx context.Context, config *remoteConfig, result any) error {
	target, ok := result.(*[]room.Room)
	if !ok {
		return errors.New("remote room list target is invalid")
	}
	*target = nil
	if config == nil {
		return nil
	}
	for _, hub := range config.Hubs {
		var rooms []room.Room
		if err := (room.NetworkClient{BaseURL: hub.URL, Token: hub.Token}).Call(ctx, "room.list", map[string]any{}, &rooms); err != nil {
			return fmt.Errorf("list rooms from %s: %w", hub.URL, err)
		}
		*target = append(*target, rooms...)
	}
	return nil
}

func resolveRemoteRoute(config remoteConfig, method, identifier, directory string, params any) (remoteRoute, error) {
	requireBinding := method == "participant.leave" || method == "message.send" || method == "participant.ack" || method == "document.upload" || method == "document.archive" || method == "document.restore"
	joinHandle := ""
	if input, ok := params.(room.JoinInput); ok {
		joinHandle = input.Handle
	}
	type candidate struct {
		route remoteRoute
		score int
	}
	var candidates []candidate
	for _, hub := range config.Hubs {
		for _, membership := range hub.Rooms {
			if membership.Slug != identifier && membership.ID != identifier {
				continue
			}
			var selected *remoteBinding
			score := 1
			for index := range hub.Bindings {
				binding := &hub.Bindings[index]
				if binding.RoomID != membership.ID {
					continue
				}
				if binding.Directory == directory {
					copy := *binding
					selected = &copy
					score = 3
					break
				}
				if method == "participant.join" && joinHandle != "" && binding.Handle == joinHandle {
					copy := *binding
					selected = &copy
					score = 2
				}
			}
			if requireBinding && selected == nil {
				continue
			}
			candidates = append(candidates, candidate{route: remoteRoute{hub: hub, binding: selected}, score: score})
		}
	}
	if len(candidates) == 0 {
		if requireBinding {
			return remoteRoute{}, fmt.Errorf("this agent has not joined room %q from %s", identifier, directory)
		}
		return remoteRoute{}, fmt.Errorf("room %q is not available to this machine", identifier)
	}
	best := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.score > best.score {
			best = candidate
		}
	}
	for _, candidate := range candidates {
		if candidate.score == best.score && candidate.route.hub.URL != best.route.hub.URL {
			return remoteRoute{}, fmt.Errorf("room %q exists on more than one Crewfold server; use its room ID", identifier)
		}
	}
	return best.route, nil
}

func (c *routedCaller) joinInvitation(ctx context.Context, address, invite, deviceName string, input room.JoinInput) (room.Room, room.Participant, error) {
	address, err := room.NormalizeNetworkURL(address)
	if err != nil {
		return room.Room{}, room.Participant{}, err
	}
	if strings.TrimSpace(invite) == "" {
		return room.Room{}, room.Participant{}, errors.New("room invitation secret is required")
	}
	if input.Delivery == "codex" {
		if err := validateLocalCodexThread(ctx, input.ThreadID); err != nil {
			return room.Room{}, room.Participant{}, fmt.Errorf("bind local Codex delivery: %w", err)
		}
	}
	if strings.TrimSpace(deviceName) == "" {
		deviceName, _ = os.Hostname()
		if strings.TrimSpace(deviceName) == "" {
			deviceName = "Crewfold connector"
		}
	}
	var joinedRoom room.Room
	err = updateRemoteConfig(c.configPath, func(config *remoteConfig) error {
		hub := findRemoteHub(config, address)
		if hub == nil {
			enrollment, enrollErr := (room.NetworkClient{BaseURL: address}).Enroll(ctx, room.NetworkEnrollInput{Invite: invite, DeviceName: deviceName})
			if enrollErr != nil {
				return enrollErr
			}
			config.Hubs = append(config.Hubs, remoteHub{URL: address, DeviceID: enrollment.DeviceID, DeviceName: enrollment.DeviceName, Token: enrollment.Token})
			hub = &config.Hubs[len(config.Hubs)-1]
			joinedRoom = enrollment.Room
		} else {
			client := room.NetworkClient{BaseURL: hub.URL, Token: hub.Token}
			if acceptErr := client.Call(ctx, "network.invite.accept", room.AcceptNetworkInviteInput{Invite: invite}, &joinedRoom); acceptErr != nil {
				return acceptErr
			}
		}
		upsertRemoteRoom(hub, joinedRoom)
		return nil
	})
	if err != nil {
		return room.Room{}, room.Participant{}, err
	}
	input.Room = joinedRoom.Slug
	var participant room.Participant
	if err := c.Call(ctx, "participant.join", input, &participant); err != nil {
		return joinedRoom, room.Participant{}, err
	}
	return joinedRoom, participant, nil
}

func findRemoteHub(config *remoteConfig, address string) *remoteHub {
	for index := range config.Hubs {
		if config.Hubs[index].URL == address {
			return &config.Hubs[index]
		}
	}
	return nil
}

func upsertRemoteRoom(hub *remoteHub, joined room.Room) {
	for index := range hub.Rooms {
		if hub.Rooms[index].ID == joined.ID {
			hub.Rooms[index] = remoteRoom{ID: joined.ID, Slug: joined.Slug, Title: joined.Title}
			return
		}
	}
	hub.Rooms = append(hub.Rooms, remoteRoom{ID: joined.ID, Slug: joined.Slug, Title: joined.Title})
}

func upsertRemoteBinding(hub *remoteHub, updated remoteBinding) {
	for index := range hub.Bindings {
		candidate := &hub.Bindings[index]
		if candidate.RoomID == updated.RoomID && candidate.Handle == updated.Handle {
			hub.Bindings[index] = updated
			return
		}
	}
	hub.Bindings = append(hub.Bindings, updated)
}

func validateLocalCodexThread(ctx context.Context, threadID string) error {
	if strings.TrimSpace(threadID) == "" {
		return errors.New("CODEX_THREAD_ID is unavailable")
	}
	socket, err := codexapp.DefaultSocketPath()
	if err != nil {
		return err
	}
	validationCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	thread, err := (codexapp.Client{SocketPath: socket}).Inspect(validationCtx, threadID)
	if err != nil {
		return err
	}
	if thread.Status.Type == "systemError" {
		return errors.New("Codex thread is in a system-error state")
	}
	if thread.CanAcceptDirectInput != nil && !*thread.CanAcceptDirectInput {
		return codexapp.ErrDirectInputUnavailable
	}
	return nil
}

func roomIdentifierFromParams(params any) string {
	switch input := params.(type) {
	case room.JoinInput:
		return input.Room
	case room.LeaveInput:
		return input.Room
	case room.SendInput:
		return input.Room
	case room.UploadInput:
		return input.Room
	case room.ArchiveDocumentInput:
		return input.Room
	case room.AckInput:
		return input.Room
	case room.ListMessagesInput:
		return input.Room
	case map[string]any:
		value, _ := input["room"].(string)
		return value
	}
	return ""
}

func findRemoteBinding(config *remoteConfig, roomIdentifier, directory string) (remoteHub, remoteBinding, bool, error) {
	if config == nil {
		return remoteHub{}, remoteBinding{}, false, nil
	}
	directory = filepath.Clean(directory)
	var foundHub remoteHub
	var found remoteBinding
	foundAny := false
	for _, hub := range config.Hubs {
		for _, binding := range hub.Bindings {
			if (binding.Room == roomIdentifier || binding.RoomID == roomIdentifier) && binding.Directory == directory {
				if foundAny {
					return remoteHub{}, remoteBinding{}, false, fmt.Errorf("room %q is bound on more than one Crewfold server; use its room ID", roomIdentifier)
				}
				foundHub, found, foundAny = hub, binding, true
			}
		}
	}
	return foundHub, found, foundAny, nil
}
