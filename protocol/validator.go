// Package protocol exposes validation against Crewfold's one current embedded
// JSON Schema catalog. Keeping the executable validator beside the published
// schemas prevents the local client and the wire contract from drifting.
package protocol

import (
	"bytes"
	"embed"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const embeddedSchemaBase = "https://schemas.crewfold.local/"

//go:embed schemas
var embeddedSchemas embed.FS

type embeddedSchemaLoader struct{}

func (embeddedSchemaLoader) Load(location string) (any, error) {
	parsed, err := url.Parse(location)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "schemas.crewfold.local" {
		return nil, fmt.Errorf("unsupported Crewfold schema location %q", location)
	}
	name := "schemas/" + strings.TrimPrefix(parsed.Path, "/")
	data, err := embeddedSchemas.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("read embedded Crewfold schema %q: %w", name, err)
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode embedded Crewfold schema %q: %w", name, err)
	}
	// The public URN remains the wire identity. Compilation uses the embedded
	// HTTPS location as its retrieval base so relative $refs resolve within the
	// catalog rather than against a pathless URN.
	if object, ok := document.(map[string]any); ok {
		delete(object, "$id")
	}
	return document, nil
}

var compiledSchemas = struct {
	sync.Mutex
	values map[string]*jsonschema.Schema
}{values: make(map[string]*jsonschema.Schema)}

// ValidateJSON validates one JSON value against a schema path relative to the
// embedded protocol/schemas directory. Format assertions are enabled because
// timestamps and other formatted strings are authority-bearing wire fields.
func ValidateJSON(schemaPath string, raw []byte) error {
	schema, err := compiledSchema(schemaPath)
	if err != nil {
		return err
	}
	instance, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("decode Crewfold schema instance: %w", err)
	}
	if err := schema.Validate(instance); err != nil {
		return fmt.Errorf("Crewfold schema %s rejected the value: %w", schemaPath, err)
	}
	return nil
}

func compiledSchema(schemaPath string) (*jsonschema.Schema, error) {
	clean := strings.TrimPrefix(schemaPath, "/")
	if clean == "" || strings.Contains(clean, "..") {
		return nil, fmt.Errorf("invalid Crewfold schema path %q", schemaPath)
	}
	compiledSchemas.Lock()
	defer compiledSchemas.Unlock()
	if schema := compiledSchemas.values[clean]; schema != nil {
		return schema, nil
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(embeddedSchemaLoader{})
	compiler.AssertFormat()
	schema, err := compiler.Compile(embeddedSchemaBase + clean)
	if err != nil {
		return nil, fmt.Errorf("compile Crewfold schema %s: %w", clean, err)
	}
	compiledSchemas.values[clean] = schema
	return schema, nil
}
