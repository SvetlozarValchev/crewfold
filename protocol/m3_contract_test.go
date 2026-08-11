package protocol_test

import (
	"encoding/json"
	"os"
	"testing"

	"crewfold/internal/localapi"
)

func TestM3ResultSchemaConstantsMatchPublishedDocuments(t *testing.T) {
	t.Parallel()

	for path, expectedID := range map[string]string{
		"schemas/local/v1/project-add.result.schema.json":     localapi.ProjectAddSchema,
		"schemas/local/v1/project-inspect.result.schema.json": localapi.ProjectInspectSchema,
		"schemas/local/v1/checkout-add.result.schema.json":    localapi.CheckoutAddSchema,
		"schemas/local/v1/checkout-list.result.schema.json":   localapi.CheckoutListSchema,
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
