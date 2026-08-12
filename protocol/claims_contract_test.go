package protocol_test

import (
	"encoding/json"
	"os"
	"testing"

	"crewfold/internal/localapi"
)

func TestClaimResultSchemaConstantsMatchPublishedDocuments(t *testing.T) {
	t.Parallel()
	for path, expectedID := range map[string]string{
		"schemas/local/v1/claim-mutation.result.schema.json":  localapi.ClaimMutationSchema,
		"schemas/local/v1/claim-list.result.schema.json":      localapi.ClaimListSchema,
		"schemas/local/v1/overlap-list.result.schema.json":    localapi.OverlapListSchema,
		"schemas/local/v1/overlap-inspect.result.schema.json": localapi.OverlapInspectSchema,
		"schemas/local/v1/overlap-scan.result.schema.json":    localapi.OverlapScanSchema,
		"schemas/local/v1/drift-list.result.schema.json":      localapi.DriftListSchema,
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("os.ReadFile(%q) error = %v", path, err)
		}
		var header struct {
			ID string `json:"$id"`
		}
		if err := json.Unmarshal(data, &header); err != nil {
			t.Fatalf("json.Unmarshal(%q) error = %v", path, err)
		}
		if header.ID != expectedID {
			t.Errorf("schema %q ID = %q, want %q", path, header.ID, expectedID)
		}
	}
}
