package protocol

import (
	"io/fs"
	"strings"
	"testing"
)

func TestEmbeddedCurrentSchemaCatalogCompiles(t *testing.T) {
	t.Parallel()
	err := fs.WalkDir(embeddedSchemas, "schemas", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		schemaPath := strings.TrimPrefix(path, "schemas/")
		if _, err := compiledSchema(schemaPath); err != nil {
			t.Errorf("compiledSchema(%q) = %v", schemaPath, err)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk embedded schema catalog: %v", err)
	}
}
