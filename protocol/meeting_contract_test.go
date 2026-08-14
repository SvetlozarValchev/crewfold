package protocol_test

import (
	"encoding/json"
	"os"
	"testing"

	"crewfold/internal/localapi"
)

func TestMeetingResultSchemaConstantsMatchPublishedDocuments(t *testing.T) {
	t.Parallel()
	for path, expectedID := range map[string]string{
		"schemas/local/v1/meeting-mutation.result.schema.json": localapi.MeetingMutationSchema,
		"schemas/local/v1/meeting-inspect.result.schema.json":  localapi.MeetingInspectSchema,
		"schemas/local/v1/meeting-list.result.schema.json":     localapi.MeetingListSchema,
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
