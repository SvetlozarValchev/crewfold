package roomcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"crewfold/internal/claudequeue"
	"crewfold/internal/room"
)

type sessionIdentity struct{ Provider, Target string }

// Cache only for this CLI invocation. Different rooms always resolve their own
// membership; reads never rewrite any binding. Parent PID matching works after cd.
type callerSession struct {
	loaded   bool
	identity sessionIdentity
	err      error
}

func (s *callerSession) get(ctx context.Context) (sessionIdentity, error) {
	if s.loaded {
		return s.identity, s.err
	}
	s.loaded = true
	ancestors, claude := processAncestors(os.Getppid())
	if claude || os.Getenv("CLAUDECODE") != "" {
		lookup, cancel := context.WithTimeout(ctx, 4*time.Second)
		defer cancel()
		var current claudequeue.Session
		current, s.err = (claudequeue.Client{}).CallerSession(lookup, ancestors)
		if s.err == nil {
			s.identity = sessionIdentity{"claude", current.SessionID}
		}
	} else if target := strings.TrimSpace(os.Getenv("CODEX_THREAD_ID")); target != "" {
		s.identity = sessionIdentity{"codex", target}
	}
	return s.identity, s.err
}

func processAncestors(pid int) ([]int, bool) {
	var result []int
	claude := false
	seen := map[int]bool{}
	for pid > 1 && !seen[pid] && len(result) < 64 {
		seen[pid] = true
		result = append(result, pid)
		raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			break
		}
		text := string(raw)
		end := strings.LastIndex(text, ")")
		start := strings.Index(text, "(")
		if end < 0 || start < 0 {
			break
		}
		name := strings.ToLower(text[start+1 : end])
		if name == "claude" || strings.HasPrefix(name, "claude-") {
			claude = true
		}
		fields := strings.Fields(text[end+1:])
		if len(fields) < 2 {
			break
		}
		pid, err = strconv.Atoi(fields[1])
		if err != nil {
			break
		}
	}
	return result, claude
}

func sessionParticipant(identity sessionIdentity, participants []room.Participant, directories ...string) (room.Participant, error) {
	var found room.Participant
	for _, p := range participants {
		if p.Status != "joined" || p.Delivery == nil || p.Delivery.Kind != identity.Provider || p.Delivery.Target != identity.Target {
			continue
		}
		if found.ID != "" {
			return room.Participant{}, errors.New("this session is bound to multiple participants in this room; resolve the duplicate bindings")
		}
		found = p
	}
	if found.ID == "" {
		// Explicit --delivery none members have no provider identity. Preserve
		// their manual directory-based use, but never borrow another session.
		for _, p := range participants {
			if len(directories) > 0 && p.Status == "joined" && p.Delivery == nil && p.WorkingDirectory == directories[0] {
				return p, nil
			}
		}
		return found, errors.New("this agent session is not bound to this room; join once from the intended session (changing folders does not require rejoining)")
	}
	return found, nil
}

type sessionCaller struct {
	client  room.Caller
	session callerSession
}

func (c *routedCaller) recipient(ctx context.Context, identifier, cwd string, participants []room.Participant) (room.Participant, error) {
	identity, err := c.session.get(ctx)
	if err != nil {
		return room.Participant{}, err
	}
	config, err := loadRemoteConfig(c.configPath)
	if err != nil {
		return room.Participant{}, err
	}
	if config == nil {
		return room.Participant{}, errors.New("connector is not configured")
	}
	route, err := resolveRemoteRoute(*config, "participant.ack", identifier, cwd, room.AckInput{Room: identifier}, identity)
	if err != nil {
		return room.Participant{}, err
	}
	for _, p := range participants {
		if route.binding != nil && p.ID == route.binding.ParticipantID && p.Status == "joined" {
			return p, nil
		}
	}
	return room.Participant{}, errors.New("session participant is no longer joined")
}

func (c *sessionCaller) recipient(ctx context.Context, identifier, cwd string, participants []room.Participant) (room.Participant, error) {
	identity, err := c.session.get(ctx)
	if err != nil {
		return room.Participant{}, err
	}
	if identity.Target != "" {
		return sessionParticipant(identity, participants, cwd)
	}
	return deliveryParticipant(c.client, identifier, cwd, participants), nil
}

func (c *sessionCaller) Call(ctx context.Context, method string, params any, result any) error {
	// Owner calls and read-only operations do not need an agent identity.
	needs := false
	directory := ""
	switch in := params.(type) {
	case room.SendInput:
		needs = !in.Owner
		directory = in.WorkingDirectory
	case room.UploadInput:
		needs = !in.Owner
		directory = in.WorkingDirectory
	case room.ArchiveDocumentInput:
		needs = !in.Owner
		directory = in.WorkingDirectory
	case room.AckInput:
		needs = true
		directory = in.WorkingDirectory
	case room.LeaveInput:
		needs = true
		directory = in.WorkingDirectory
	case room.BoardInput:
		needs = !in.Owner && !strings.HasSuffix(method, ".show") && !strings.HasSuffix(method, ".list")
		directory = in.WorkingDirectory
	}
	if needs {
		identity, err := c.session.get(ctx)
		if err != nil {
			return err
		}
		if identity.Target != "" {
			var participants []room.Participant
			if err := c.client.Call(ctx, "participant.list", map[string]string{"room": roomIdentifierFromParams(params)}, &participants); err != nil {
				return err
			}
			p, err := sessionParticipant(identity, participants, directory)
			if err != nil {
				return err
			}
			// Existing wire fields preserve compatibility with room authorization.
			switch in := params.(type) {
			case room.SendInput:
				in.WorkingDirectory = p.WorkingDirectory
				in.Handle = p.Handle
				params = in
			case room.UploadInput:
				in.WorkingDirectory = p.WorkingDirectory
				in.Handle = p.Handle
				params = in
			case room.ArchiveDocumentInput:
				in.WorkingDirectory = p.WorkingDirectory
				in.Handle = p.Handle
				params = in
			case room.AckInput:
				in.WorkingDirectory = p.WorkingDirectory
				in.Handle = p.Handle
				params = in
			case room.LeaveInput:
				in.WorkingDirectory = p.WorkingDirectory
				in.Handle = p.Handle
				params = in
			case room.BoardInput:
				in.WorkingDirectory = p.WorkingDirectory
				params = in
			}
		}
	}
	return c.client.Call(ctx, method, params, result)
}
