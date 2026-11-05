package room

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

func (s *Server) wakeNetwork() {
	s.networkMu.Lock()
	if s.networkWake != nil {
		close(s.networkWake)
	}
	s.networkWake = make(chan struct{})
	s.networkMu.Unlock()
}

func (s *Server) currentNetworkWake() <-chan struct{} {
	s.networkMu.Lock()
	defer s.networkMu.Unlock()
	return s.networkWake
}

func (s *Server) networkEnabled(ctx context.Context) (bool, error) {
	settings, err := s.store.NetworkSettings(ctx)
	return settings.Enabled, err
}

func (s *Server) handleNetworkEnroll(response http.ResponseWriter, request *http.Request) {
	s.secureWeb(response)
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	enabled, err := s.networkEnabled(request.Context())
	if err != nil || !enabled {
		http.Error(response, "Crewfold networking is disabled", http.StatusServiceUnavailable)
		return
	}
	var input NetworkEnrollInput
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 16*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	enrollment, err := s.store.EnrollNetworkDevice(request.Context(), input)
	if err != nil {
		http.Error(response, err.Error(), http.StatusBadRequest)
		return
	}
	s.writeWebJSON(response, enrollment)
}

func (s *Server) handleNetworkRPC(response http.ResponseWriter, request *http.Request) {
	s.secureWeb(response)
	if request.Method != http.MethodPost {
		http.Error(response, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	enabled, err := s.networkEnabled(request.Context())
	if err != nil || !enabled {
		http.Error(response, "Crewfold networking is disabled", http.StatusServiceUnavailable)
		return
	}
	token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
	device, err := s.store.AuthenticateNetworkDevice(request.Context(), token)
	if err != nil {
		http.Error(response, "unauthorized", http.StatusUnauthorized)
		return
	}
	var rpc Request
	decoder := json.NewDecoder(http.MaxBytesReader(response, request.Body, 6*1024*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rpc); err != nil {
		http.Error(response, "invalid request", http.StatusBadRequest)
		return
	}
	result, err := s.networkCall(request.Context(), device, request.Header.Get("X-Crewfold-Participant"), rpc.Method, rpc.Params)
	if err != nil {
		s.writeWebJSON(response, Response{ID: rpc.ID, Error: err.Error()})
		return
	}
	s.writeWebJSON(response, Response{ID: rpc.ID, Result: result})
}

func (s *Server) networkCall(ctx context.Context, device NetworkDevice, participantID, method string, raw json.RawMessage) (any, error) {
	decode := func(target any) error {
		if len(raw) == 0 {
			raw = json.RawMessage("{}")
		}
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		return decoder.Decode(target)
	}
	requireBinding := func(roomIdentifier string) (NetworkBinding, Participant, Room, error) {
		if strings.TrimSpace(participantID) == "" {
			return NetworkBinding{}, Participant{}, Room{}, errors.New("this command requires a joined remote participant")
		}
		binding, participant, room, err := s.store.NetworkBinding(ctx, device.ID, participantID)
		if err != nil {
			return binding, participant, room, err
		}
		if binding.Status == "left" || participant.Status != "joined" {
			return binding, participant, room, errors.New("remote participant has left this room")
		}
		if roomIdentifier != "" && roomIdentifier != room.ID && roomIdentifier != room.Slug {
			return binding, participant, room, errors.New("participant is not bound to this room")
		}
		return binding, participant, room, nil
	}
	switch method {
	case "status":
		rooms, err := s.store.NetworkRooms(ctx, device.ID)
		return map[string]any{"status": "ok", "rooms": len(rooms), "device": device}, err
	case "network.invite.accept":
		var input AcceptNetworkInviteInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		return s.store.AcceptNetworkInvite(ctx, device.ID, input)
	case "room.list":
		return s.store.NetworkRooms(ctx, device.ID)
	case "room.snapshot":
		var input ListMessagesInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		room, err := s.store.NetworkRoom(ctx, device.ID, input.Room)
		if err != nil {
			return nil, err
		}
		if input.After > 0 && input.Before > 0 {
			return nil, errors.New("after and before cannot be used together")
		}
		if input.Before > 0 {
			return s.store.SnapshotBefore(ctx, room.ID, input.Before, input.Limit)
		}
		return s.store.Snapshot(ctx, room.ID, input.After, input.Limit)
	case "participant.join":
		var input JoinInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		input.ThreadID = ""
		participant, err := s.store.JoinNetworkParticipant(ctx, device, input)
		if err == nil {
			s.deliveries.Wake()
			s.wakeNetwork()
		}
		return participant, err
	case "participant.leave":
		var input LeaveInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		_, _, room, err := requireBinding(input.Room)
		if err != nil {
			return nil, err
		}
		input.Room, input.ParticipantID = room.ID, participantID
		participant, err := s.store.Leave(ctx, input)
		if err == nil {
			s.deliveries.Wake()
			s.wakeNetwork()
		}
		return participant, err
	case "message.send":
		var input SendInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		_, _, room, err := requireBinding(input.Room)
		if err != nil {
			return nil, err
		}
		input.Room, input.ParticipantID, input.Owner = room.ID, participantID, false
		message, err := s.store.Send(ctx, input)
		if err == nil {
			s.deliveries.Wake()
			s.wakeNetwork()
		}
		return message, err
	case "participant.ack":
		var input AckInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		_, _, room, err := requireBinding(input.Room)
		if err != nil {
			return nil, err
		}
		input.Room, input.ParticipantID = room.ID, participantID
		return s.store.Ack(ctx, input)
	case "document.upload":
		var input UploadInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		_, _, room, err := requireBinding(input.Room)
		if err != nil {
			return nil, err
		}
		input.Room, input.ParticipantID, input.Owner = room.ID, participantID, false
		message, err := s.store.Upload(ctx, input)
		if err == nil {
			s.deliveries.Wake()
			s.wakeNetwork()
		}
		return message, err
	case "document.read":
		var input struct {
			Room     string `json:"room"`
			Document string `json:"document"`
		}
		if err := decode(&input); err != nil {
			return nil, err
		}
		room, err := s.store.NetworkRoom(ctx, device.ID, input.Room)
		if err != nil {
			return nil, err
		}
		document, content, err := s.store.ReadDocument(ctx, room.ID, input.Document)
		if err != nil {
			return nil, err
		}
		return map[string]any{"document": document, "content_base64": base64.StdEncoding.EncodeToString(content)}, nil
	case "document.archive", "document.restore":
		var input ArchiveDocumentInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		_, _, room, err := requireBinding(input.Room)
		if err != nil {
			return nil, err
		}
		input.Room, input.ParticipantID, input.Owner = room.ID, participantID, false
		return s.store.SetDocumentArchived(ctx, input, method == "document.archive")
	case "delivery.poll":
		var input NetworkDeliveryPollInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		if input.ParticipantID == "" {
			input.ParticipantID = participantID
		}
		if input.ParticipantID != participantID {
			return nil, errors.New("delivery participant does not match authenticated binding")
		}
		return s.pollNetworkDelivery(ctx, device.ID, input)
	case "delivery.report":
		var input NetworkDeliveryResultInput
		if err := decode(&input); err != nil {
			return nil, err
		}
		if input.ParticipantID == "" {
			input.ParticipantID = participantID
		}
		if input.ParticipantID != participantID {
			return nil, errors.New("delivery participant does not match authenticated binding")
		}
		return s.store.RecordNetworkDelivery(ctx, device.ID, input)
	default:
		return nil, errors.New("method is not available to remote participants")
	}
}

func (s *Server) pollNetworkDelivery(ctx context.Context, deviceID string, input NetworkDeliveryPollInput) (NetworkDeliveryBatch, error) {
	wait := time.Duration(input.WaitSeconds) * time.Second
	if wait < 0 || wait > 25*time.Second {
		return NetworkDeliveryBatch{}, errors.New("delivery wait must be between 0 and 25 seconds")
	}
	deadline := time.Now().Add(wait)
	for {
		batch, err := s.store.NetworkDeliveryBatch(ctx, deviceID, input.ParticipantID)
		if err != nil {
			return NetworkDeliveryBatch{}, err
		}
		if batch.Through > 0 && len(batch.Messages) == 0 {
			binding, _, _, bindErr := s.store.NetworkBinding(ctx, deviceID, input.ParticipantID)
			if bindErr != nil || batch.Through > binding.LastDeliveredSequence {
				return batch, bindErr
			}
		}
		if len(batch.Messages) > 0 {
			direct := false
			for _, message := range batch.Messages {
				if messageNeedsImmediateDelivery(message.Body, batch.Participant.Handle) {
					direct = true
					break
				}
			}
			if direct || wait == 0 {
				return batch, nil
			}
			lastAt, _ := time.Parse(time.RFC3339Nano, batch.Messages[len(batch.Messages)-1].CreatedAt)
			quietUntil := lastAt.Add(5 * time.Second)
			if !time.Now().Before(quietUntil) || !time.Now().Before(deadline) {
				return batch, nil
			}
		}
		if wait == 0 || !time.Now().Before(deadline) {
			return batch, nil
		}
		wake := s.currentNetworkWake()
		delay := time.Until(deadline)
		if len(batch.Messages) > 0 {
			lastAt, _ := time.Parse(time.RFC3339Nano, batch.Messages[len(batch.Messages)-1].CreatedAt)
			if quiet := time.Until(lastAt.Add(5 * time.Second)); quiet > 0 && quiet < delay {
				delay = quiet
			}
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return NetworkDeliveryBatch{}, ctx.Err()
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}
