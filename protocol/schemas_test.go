package protocol_test

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"crewfold/internal/buildinfo"
)

type stringProperty struct {
	Type      string `json:"type"`
	Const     string `json:"const"`
	MinLength int    `json:"minLength"`
	Pattern   string `json:"pattern"`
}

type objectSchema struct {
	ID                   string                    `json:"$id"`
	Type                 string                    `json:"type"`
	AdditionalProperties bool                      `json:"additionalProperties"`
	Required             []string                  `json:"required"`
	Properties           map[string]stringProperty `json:"properties"`
}

func TestSchemasAreValidJSONWithUniqueIDs(t *testing.T) {
	t.Parallel()

	var paths []string
	err := filepath.WalkDir("schemas", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && filepath.Ext(path) == ".json" {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("filepath.WalkDir() error = %v", err)
	}
	if len(paths) < 3 {
		t.Fatalf("schema count = %d, want at least 3; paths = %v", len(paths), paths)
	}

	ids := make(map[string]string, len(paths))
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("os.ReadFile(%q) error = %v", path, err)
		}
		if !json.Valid(data) {
			t.Fatalf("schema %q is not valid JSON", path)
		}

		var header struct {
			Schema string `json:"$schema"`
			ID     string `json:"$id"`
		}
		if err := json.Unmarshal(data, &header); err != nil {
			t.Fatalf("json.Unmarshal(%q) error = %v", path, err)
		}
		if header.Schema != "https://json-schema.org/draft/2020-12/schema" {
			t.Fatalf("schema %q draft = %q, want 2020-12", path, header.Schema)
		}
		if header.ID == "" {
			t.Fatalf("schema %q has empty $id", path)
		}
		if previous, exists := ids[header.ID]; exists {
			t.Fatalf("schemas %q and %q share $id %q", previous, path, header.ID)
		}
		ids[header.ID] = path
		validateLocalReferences(t, path, data)
	}
}

func validateLocalReferences(t *testing.T, schemaPath string, data []byte) {
	t.Helper()

	var document any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatalf("json.Unmarshal(%q) error = %v", schemaPath, err)
	}
	walkJSON(document, func(reference string) {
		if reference == "" || reference[0] == '#' || regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*:`).MatchString(reference) {
			return
		}
		target := reference
		if index := len(target); index > 0 {
			if match := regexp.MustCompile(`#.*$`).FindStringIndex(target); match != nil {
				target = target[:match[0]]
			}
		}
		resolved := filepath.Clean(filepath.Join(filepath.Dir(schemaPath), filepath.FromSlash(target)))
		if _, err := os.Stat(resolved); err != nil {
			t.Errorf("schema %q reference %q does not resolve: %v", schemaPath, reference, err)
		}
	})
}

func walkJSON(value any, visitReference func(string)) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "$ref" {
				if reference, ok := child.(string); ok {
					visitReference(reference)
				}
				continue
			}
			walkJSON(child, visitReference)
		}
	case []any:
		for _, child := range typed {
			walkJSON(child, visitReference)
		}
	}
}

func TestVersionResponseValidatesAgainstPublishedSchema(t *testing.T) {
	t.Parallel()

	schemaData, err := os.ReadFile("schemas/cli/v1/version.response.schema.json")
	if err != nil {
		t.Fatalf("os.ReadFile(schema) error = %v", err)
	}
	var schema objectSchema
	if err := json.Unmarshal(schemaData, &schema); err != nil {
		t.Fatalf("json.Unmarshal(schema) error = %v", err)
	}

	responseData, err := json.Marshal(buildinfo.Current())
	if err != nil {
		t.Fatalf("json.Marshal(Current()) error = %v", err)
	}
	validateStringObject(t, responseData, schema)
}

func validateStringObject(t *testing.T, data []byte, schema objectSchema) {
	t.Helper()

	if schema.Type != "object" {
		t.Fatalf("schema.Type = %q, want object", schema.Type)
	}
	if schema.AdditionalProperties {
		t.Fatal("schema.AdditionalProperties = true, want false")
	}

	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatalf("json.Unmarshal(response) error = %v", err)
	}

	for _, name := range schema.Required {
		if _, exists := object[name]; !exists {
			t.Errorf("required property %q is absent", name)
		}
	}
	for name := range object {
		if _, declared := schema.Properties[name]; !declared {
			t.Errorf("response contains undeclared property %q", name)
		}
	}

	for name, property := range schema.Properties {
		value, exists := object[name]
		if !exists {
			continue
		}
		if property.Type != "string" && property.Const == "" {
			t.Errorf("test validator cannot handle %q type %q", name, property.Type)
			continue
		}
		stringValue, ok := value.(string)
		if !ok {
			t.Errorf("property %q has type %T, want string", name, value)
			continue
		}
		if property.MinLength > 0 && len(stringValue) < property.MinLength {
			t.Errorf("property %q length = %d, want >= %d", name, len(stringValue), property.MinLength)
		}
		if property.Const != "" && stringValue != property.Const {
			t.Errorf("property %q = %q, want const %q", name, stringValue, property.Const)
		}
		if property.Pattern != "" {
			matched, err := regexp.MatchString(property.Pattern, stringValue)
			if err != nil {
				t.Errorf("property %q pattern %q is invalid: %v", name, property.Pattern, err)
			} else if !matched {
				t.Errorf("property %q value %q does not match %q", name, stringValue, property.Pattern)
			}
		}
	}
}
