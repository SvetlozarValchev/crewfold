package store

import (
	"encoding/json"
	"strings"
)

const checkOwnerIdempotencyActor = "owner"

// checkSemanticHash deliberately excludes transport metadata. Correlation IDs
// identify an individual delivery, and the raw idempotency key selects the
// replay slot; neither changes the semantic mutation being requested.
func checkSemanticHash(operation string, command any) (string, error) {
	encoded, err := json.Marshal(command)
	if err != nil {
		return "", err
	}
	var semantic map[string]any
	if err := json.Unmarshal(encoded, &semantic); err != nil {
		return "", err
	}
	delete(semantic, "IdempotencyKey")
	delete(semantic, "CorrelationID")
	return hashCommand(operation, semantic)
}

func ownerCheckIdempotencyKey(raw string) string {
	return "check:" + checkOwnerIdempotencyActor + ":" + strings.TrimSpace(raw)
}

func runCheckIdempotencyKey(sourceRunID, raw string) string {
	return "check:run:" + strings.TrimSpace(sourceRunID) + ":" + strings.TrimSpace(raw)
}
