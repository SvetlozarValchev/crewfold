// Command legacy-packet rewrites one isolated acceptance-fixture packet to the
// exact pre-v4 shape. It is never linked into Crewfold and accepts only an
// explicit database path and packet ID.
package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"crewfold/internal/domain"
	_ "github.com/ncruces/go-sqlite3/driver"
)

const maximumContextBytes = 32768

func main() {
	if len(os.Args) == 4 && os.Args[1] == "verify" {
		verifyLegacyIntegrity(os.Args[2], os.Args[3])
		return
	}
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: legacy-packet DATABASE PACKET_ID | legacy-packet verify DATABASE PACKET_ID")
		os.Exit(2)
	}
	database, err := sql.Open("sqlite3", os.Args[1])
	if err != nil {
		fail(err)
	}
	defer database.Close()
	tx, err := database.Begin()
	if err != nil {
		fail(err)
	}
	defer tx.Rollback()
	var packetUpdateTrigger, deltaStateDeleteTrigger, eventUpdateTrigger string
	if err := tx.QueryRow("SELECT sql FROM sqlite_schema WHERE type = 'trigger' AND name = 'context_packet_reject_update'").Scan(&packetUpdateTrigger); err != nil {
		fail(err)
	}
	if err := tx.QueryRow("SELECT sql FROM sqlite_schema WHERE type = 'trigger' AND name = 'run_context_delta_state_reject_delete'").Scan(&deltaStateDeleteTrigger); err != nil {
		fail(err)
	}
	if err := tx.QueryRow("SELECT sql FROM sqlite_schema WHERE type = 'trigger' AND name = 'events_reject_update'").Scan(&eventUpdateTrigger); err != nil {
		fail(err)
	}
	var raw string
	if err := tx.QueryRow("SELECT packet_json FROM context_packets WHERE id = ?", os.Args[2]).Scan(&raw); err != nil {
		fail(err)
	}
	var packet domain.ContextPacket
	if err := json.Unmarshal([]byte(raw), &packet); err != nil {
		fail(err)
	}
	packet.Schema = domain.ContextPacketSchemaV3
	packet.Task.AssignmentID = ""
	packet.Dependents = nil
	packet.DependentTaskCount = 0
	packet.ParticipantThreads = nil
	packet.LiveContext = domain.ContextLivePolicy{}
	packet.AsOfEventSequence = 0
	packet.Budget.Collaboration = domain.ContextBudgetUsage{}
	filtered := make([]string, 0, len(packet.Policy.AllowedTools))
	for _, name := range packet.Policy.AllowedTools {
		if name != "crewfold_get_context_delta" && name != "crewfold_acknowledge_context_delta" {
			filtered = append(filtered, name)
		}
	}
	packet.Policy.AllowedTools = filtered
	encoded, err := finalizeLegacyPacket(&packet)
	if err != nil {
		fail(err)
	}
	if _, err := tx.Exec("DROP TRIGGER context_packet_reject_update"); err != nil {
		fail(err)
	}
	if _, err := tx.Exec("DROP TRIGGER run_context_delta_state_reject_delete"); err != nil {
		fail(err)
	}
	if _, err := tx.Exec("DROP TRIGGER events_reject_update"); err != nil {
		fail(err)
	}
	if _, err := tx.Exec("DELETE FROM run_context_delta_state WHERE context_packet_id = ?", os.Args[2]); err != nil {
		fail(err)
	}
	if result, err := tx.Exec("UPDATE context_packets SET packet_json = ?, content_hash = ?, byte_size = ? WHERE id = ?", string(encoded), packet.ContentHash, len(encoded), os.Args[2]); err != nil {
		fail(err)
	} else if affected, _ := result.RowsAffected(); affected != 1 {
		fail(errors.New("packet was not updated exactly once"))
	}
	eventData, err := json.Marshal(map[string]any{
		"task_id": packet.TaskID, "agent_id": packet.AgentID, "checkout_id": packet.CheckoutID,
		"content_hash": packet.ContentHash, "byte_size": packet.ByteSize,
	})
	if err != nil {
		fail(err)
	}
	if result, err := tx.Exec(`UPDATE events SET data_json = ?
WHERE type = 'context.packet_built' AND entity_type = 'context_packet' AND entity_id = ? AND entity_revision = 1`, string(eventData), packet.ID); err != nil {
		fail(err)
	} else if affected, _ := result.RowsAffected(); affected != 1 {
		fail(errors.New("context packet build event was not updated exactly once"))
	}
	var eventSequence int64
	if err := tx.QueryRow(`SELECT sequence FROM events
WHERE type = 'context.packet_built' AND entity_type = 'context_packet' AND entity_id = ? AND entity_revision = 1`, packet.ID).Scan(&eventSequence); err != nil {
		fail(err)
	}
	response, err := json.Marshal(struct {
		Value         domain.ContextPacket `json:"value"`
		EventSequence int64                `json:"event_sequence"`
	}{Value: packet, EventSequence: eventSequence})
	if err != nil {
		fail(err)
	}
	if result, err := tx.Exec(`UPDATE idempotency_keys SET response_json = ?
WHERE command = 'context.build' AND json_extract(response_json, '$.value.id') = ?`, string(response), packet.ID); err != nil {
		fail(err)
	} else if affected, _ := result.RowsAffected(); affected != 1 {
		fail(errors.New("context build replay was not updated exactly once"))
	}
	restoreTrigger(tx, "context_packet_reject_update", packetUpdateTrigger)
	restoreTrigger(tx, "run_context_delta_state_reject_delete", deltaStateDeleteTrigger)
	restoreTrigger(tx, "events_reject_update", eventUpdateTrigger)
	if err := tx.Commit(); err != nil {
		fail(err)
	}
}

func finalizeLegacyPacket(packet *domain.ContextPacket) ([]byte, error) {
	packet.ContentHash = "sha256:" + strings.Repeat("0", 64)
	packet.ByteSize = 0
	packet.Budget.Total = domain.ContextBudgetUsage{LimitBytes: maximumContextBytes, RemainingBytes: maximumContextBytes}
	stable := false
	for range 8 {
		encoded, err := json.Marshal(packet)
		if err != nil {
			return nil, err
		}
		usedBytes := len(encoded)
		if packet.ByteSize == usedBytes && packet.Budget.Total.UsedBytes == usedBytes && packet.Budget.Total.RemainingBytes == maximumContextBytes-usedBytes {
			stable = true
			break
		}
		packet.ByteSize = usedBytes
		packet.Budget.Total.UsedBytes = usedBytes
		packet.Budget.Total.RemainingBytes = maximumContextBytes - usedBytes
	}
	if !stable {
		return nil, errors.New("legacy packet byte accounting did not converge")
	}
	hash, err := legacyPacketSemanticHash(*packet)
	if err != nil {
		return nil, err
	}
	packet.ContentHash = hash
	encoded, err := json.Marshal(packet)
	if err != nil || len(encoded) != packet.ByteSize {
		return nil, errors.New("legacy packet final byte accounting is invalid")
	}
	return encoded, nil
}

func legacyPacketSemanticHash(packet domain.ContextPacket) (string, error) {
	semantic := packet
	semantic.ID, semantic.ContentHash, semantic.CreatedAt, semantic.CreatedBy = "", "", "", ""
	semantic.ByteSize = 0
	semantic.Budget.Total.UsedBytes = 0
	semantic.Budget.Total.RemainingBytes = maximumContextBytes
	encoded, err := json.Marshal(semantic)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("sha256:%x", digest), nil
}

func restoreTrigger(tx *sql.Tx, name, definition string) {
	if _, err := tx.Exec(definition); err != nil {
		fail(err)
	}
	var restored string
	if err := tx.QueryRow("SELECT sql FROM sqlite_schema WHERE type = 'trigger' AND name = ?", name).Scan(&restored); err != nil {
		fail(err)
	}
	if restored != definition {
		fail(fmt.Errorf("restored trigger %q differs from its original definition", name))
	}
}

func verifyLegacyIntegrity(path, packetID string) {
	database, err := sql.Open("sqlite3", path)
	if err != nil {
		fail(err)
	}
	defer database.Close()
	var count int
	if err := database.QueryRow(`SELECT COUNT(*) FROM sqlite_schema
WHERE type = 'trigger' AND name IN ('context_packet_reject_update', 'run_context_delta_state_reject_delete', 'events_reject_update')
  AND sql IS NOT NULL`).Scan(&count); err != nil {
		fail(err)
	}
	if count != 3 {
		fail(fmt.Errorf("legacy fixture left %d of 3 immutable triggers installed", count))
	}
	var raw, rowHash string
	var rowBytes int
	if err := database.QueryRow("SELECT packet_json, content_hash, byte_size FROM context_packets WHERE id = ?", packetID).Scan(&raw, &rowHash, &rowBytes); err != nil {
		fail(err)
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var packet domain.ContextPacket
	if err := decoder.Decode(&packet); err != nil {
		fail(fmt.Errorf("strictly decode legacy fixture packet: %w", err))
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		fail(errors.New("legacy fixture packet has trailing JSON content"))
	}
	if packet.Schema != domain.ContextPacketSchemaV3 {
		fail(fmt.Errorf("legacy fixture packet schema = %q", packet.Schema))
	}
	canonical, err := json.Marshal(packet)
	if err != nil || !bytes.Equal(canonical, []byte(raw)) {
		fail(errors.New("legacy fixture packet is not the exact canonical v3 wire shape"))
	}
	var wire map[string]any
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		fail(err)
	}
	for _, field := range []string{"as_of_event_sequence", "dependents", "dependent_task_count", "participant_threads", "live_context"} {
		if _, exists := wire[field]; exists {
			fail(fmt.Errorf("legacy fixture packet retained v4 field %q", field))
		}
	}
	task, ok := wire["task"].(map[string]any)
	if !ok {
		fail(errors.New("legacy fixture packet task is missing"))
	}
	if _, exists := task["assignment_id"]; exists || packet.Task.AssignmentID != "" {
		fail(errors.New("legacy fixture packet retained v4 task assignment identity"))
	}
	budget, ok := wire["budget"].(map[string]any)
	if !ok {
		fail(errors.New("legacy fixture packet budget is missing"))
	}
	if _, exists := budget["collaboration"]; exists {
		fail(errors.New("legacy fixture packet retained v4 collaboration budget"))
	}
	for _, tool := range packet.Policy.AllowedTools {
		if tool == "crewfold_get_context_delta" || tool == "crewfold_acknowledge_context_delta" {
			fail(errors.New("legacy fixture packet retained v4 live context tools"))
		}
	}
	semanticHash, err := legacyPacketSemanticHash(packet)
	if err != nil || semanticHash != packet.ContentHash || rowHash != packet.ContentHash {
		fail(errors.New("legacy fixture packet semantic hash is invalid"))
	}
	if rowBytes != len(raw) || packet.ByteSize != len(raw) || packet.Budget.Total.UsedBytes != len(raw) ||
		packet.Budget.Total.RemainingBytes != maximumContextBytes-len(raw) {
		fail(errors.New("legacy fixture packet byte accounting is invalid"))
	}
	var eventRaw, actorID, actorType string
	var eventSequence int64
	if err := database.QueryRow(`SELECT sequence, data_json, actor_id, actor_type FROM events
WHERE type = 'context.packet_built' AND entity_type = 'context_packet' AND entity_id = ? AND entity_revision = 1`, packetID).Scan(&eventSequence, &eventRaw, &actorID, &actorType); err != nil {
		fail(err)
	}
	var eventData map[string]any
	if err := json.Unmarshal([]byte(eventRaw), &eventData); err != nil {
		fail(err)
	}
	wantEvent := map[string]any{
		"task_id": packet.TaskID, "agent_id": packet.AgentID, "checkout_id": packet.CheckoutID,
		"content_hash": packet.ContentHash, "byte_size": float64(packet.ByteSize),
	}
	if actorID != "local-owner" || actorType != "human" || len(eventData) != len(wantEvent) {
		fail(errors.New("legacy fixture packet build event authority is invalid"))
	}
	for field, wanted := range wantEvent {
		if eventData[field] != wanted {
			fail(fmt.Errorf("legacy fixture packet build event %s = %v, want %v", field, eventData[field], wanted))
		}
	}
	var replayRaw string
	if err := database.QueryRow(`SELECT response_json FROM idempotency_keys
WHERE command = 'context.build' AND json_extract(response_json, '$.value.id') = ?`, packetID).Scan(&replayRaw); err != nil {
		fail(err)
	}
	var replay struct {
		Value         domain.ContextPacket `json:"value"`
		EventSequence int64                `json:"event_sequence"`
	}
	decoder = json.NewDecoder(strings.NewReader(replayRaw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&replay); err != nil || replay.EventSequence != eventSequence || replay.Value.ContentHash != packet.ContentHash {
		fail(errors.New("legacy fixture context build replay authority is invalid"))
	}
	replayPacket, err := json.Marshal(replay.Value)
	if err != nil || !bytes.Equal(replayPacket, []byte(raw)) {
		fail(errors.New("legacy fixture context build replay does not contain the exact v3 packet"))
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM run_context_delta_state WHERE context_packet_id = ?", packetID).Scan(&count); err != nil {
		fail(err)
	}
	if count != 0 {
		fail(fmt.Errorf("legacy refresh invented %d live context state rows", count))
	}
	if err := database.QueryRow("SELECT COUNT(*) FROM context_deltas WHERE context_packet_id = ?", packetID).Scan(&count); err != nil {
		fail(err)
	}
	if count != 0 {
		fail(fmt.Errorf("legacy refresh invented %d context deltas", count))
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
