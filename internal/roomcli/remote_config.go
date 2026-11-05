package roomcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"crewfold/internal/codexapp"
	"crewfold/internal/room"
)

type remoteConfig struct {
	URL        string          `json:"url"`
	DeviceID   string          `json:"device_id"`
	DeviceName string          `json:"device_name"`
	Token      string          `json:"token"`
	Bindings   []remoteBinding `json:"bindings,omitempty"`
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
		return nil, fmt.Errorf("read remote configuration: %w", err)
	}
	normalized, err := room.NormalizeNetworkURL(config.URL)
	if err != nil || normalized != config.URL || config.DeviceID == "" || config.Token == "" {
		return nil, errors.New("remote configuration is invalid")
	}
	_ = os.Chmod(path, 0o600)
	return &config, nil
}

func saveRemoteConfig(path string, config *remoteConfig) error {
	if config == nil {
		return errors.New("remote configuration is required")
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

type routedCaller struct {
	local      room.Caller
	configPath string
	mu         sync.Mutex
	config     *remoteConfig
}

func (c *routedCaller) isRemote() bool { return c != nil && c.config != nil }

func (c *routedCaller) Call(ctx context.Context, method string, params any, result any) error {
	if !c.isRemote() {
		return c.local.Call(ctx, method, params, result)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	directory, _ := os.Getwd()
	directory = filepath.Clean(directory)
	roomIdentifier := roomIdentifierFromParams(params)
	var binding *remoteBinding
	for index := range c.config.Bindings {
		candidate := &c.config.Bindings[index]
		if (candidate.Room == roomIdentifier || candidate.RoomID == roomIdentifier) && candidate.Directory == directory {
			binding = candidate
			break
		}
	}
	participantID := ""
	if binding != nil {
		participantID = binding.ParticipantID
	}
	client := room.NetworkClient{BaseURL: c.config.URL, Token: c.config.Token, ParticipantID: participantID}
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
	if leaving && binding != nil {
		leavingParticipantID := binding.ParticipantID
		kept := c.config.Bindings[:0]
		for _, candidate := range c.config.Bindings {
			if candidate.ParticipantID != leavingParticipantID {
				kept = append(kept, candidate)
			}
		}
		c.config.Bindings = kept
		if err := saveRemoteConfig(c.configPath, c.config); err != nil {
			return fmt.Errorf("remove remote delivery binding: %w", err)
		}
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
			// The provider target deliberately remains on this machine.
			original := params.(room.JoinInput)
			updated.Target = original.ThreadID
		}
		replaced := false
		for index := range c.config.Bindings {
			candidate := &c.config.Bindings[index]
			if candidate.RoomID == updated.RoomID && candidate.Handle == updated.Handle {
				c.config.Bindings[index] = updated
				replaced = true
				break
			}
		}
		if !replaced {
			c.config.Bindings = append(c.config.Bindings, updated)
		}
		if err := saveRemoteConfig(c.configPath, c.config); err != nil {
			return fmt.Errorf("save remote delivery binding: %w", err)
		}
	}
	return nil
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

func (c *routedCaller) binding(roomIdentifier, directory string) (remoteBinding, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	directory = filepath.Clean(directory)
	for _, binding := range c.config.Bindings {
		if (binding.Room == roomIdentifier || binding.RoomID == roomIdentifier) && binding.Directory == directory {
			return binding, true
		}
	}
	return remoteBinding{}, false
}
