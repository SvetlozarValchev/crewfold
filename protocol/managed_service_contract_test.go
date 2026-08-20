package protocol_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestManagedServiceSchemaConstantsMatchPublishedDocuments(t *testing.T) {
	t.Parallel()
	for path, expected := range map[string]string{
		"schemas/local/v1/managed-service-definition-mutation.result.schema.json": localapi.ManagedServiceDefinitionMutationSchema,
		"schemas/local/v1/managed-service-definition-show.result.schema.json":     localapi.ManagedServiceDefinitionShowSchema,
		"schemas/local/v1/managed-service-definition-list.result.schema.json":     localapi.ManagedServiceDefinitionListSchema,
		"schemas/local/v1/managed-service-mutation.result.schema.json":            localapi.ManagedServiceMutationSchema,
		"schemas/local/v1/managed-service-show.result.schema.json":                localapi.ManagedServiceShowSchema,
		"schemas/local/v1/managed-service-list.result.schema.json":                localapi.ManagedServiceListSchema,
		"schemas/local/v1/managed-service-logs.result.schema.json":                localapi.ManagedServiceLogsSchema,
		"schemas/local/v1/managed-service-grant-mutation.result.schema.json":      localapi.ManagedServiceGrantMutationSchema,
		"schemas/local/v1/managed-service-grant-list.result.schema.json":          localapi.ManagedServiceGrantListSchema,
		"schemas/local/v1/managed-service-request-list.result.schema.json":        localapi.ManagedServiceRequestListSchema,
		"schemas/local/v1/managed-service-request-mutation.result.schema.json":    localapi.ManagedServiceRequestMutationSchema,
		"schemas/local/v1/run-artifact-show.result.schema.json":                   localapi.RunArtifactShowSchema,
		"schemas/local/v1/workstream-delivery-show.result.schema.json":            localapi.WorkstreamDeliveryShowSchema,
		"schemas/local/v1/workstream-delivery-mutation.result.schema.json":        localapi.WorkstreamDeliveryMutationSchema,
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var header struct {
			ID string `json:"$id"`
		}
		if err := json.Unmarshal(data, &header); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		if header.ID != expected {
			t.Errorf("%s ID = %q, want %q", path, header.ID, expected)
		}
	}
}

func TestManagedServiceSchemasCoverEveryGoJSONField(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		path  string
		value any
	}{
		{"schemas/domain/v1/managed-service-environment-variable.schema.json", domain.ManagedServiceEnvironmentVariable{}},
		{"schemas/domain/v1/managed-service-health.schema.json", domain.ManagedServiceHealthCheck{}},
		{"schemas/domain/v1/managed-service-definition.schema.json", domain.ManagedServiceDefinition{}},
		{"schemas/domain/v1/managed-service-source.schema.json", domain.ManagedServiceSource{}},
		{"schemas/domain/v1/managed-service-grant.schema.json", domain.ManagedServiceGrant{}},
		{"schemas/domain/v1/managed-service-request.schema.json", domain.ManagedServiceRequest{}},
		{"schemas/domain/v1/managed-service-request-decision.schema.json", domain.ManagedServiceRequestDecision{}},
		{"schemas/domain/v1/managed-service-instance.schema.json", domain.ManagedServiceInstance{}},
		{"schemas/domain/v1/managed-service-job.schema.json", domain.ManagedServiceJob{}},
		{"schemas/domain/v1/managed-service-log-artifact.schema.json", domain.ManagedServiceLogArtifact{}},
		{"schemas/domain/v1/managed-service-detail.schema.json", domain.ManagedServiceDetail{}},
		{"schemas/domain/v1/managed-service-logs.schema.json", domain.ManagedServiceLogs{}},
		{"schemas/domain/v1/run-artifact.schema.json", domain.RunArtifact{}},
		{"schemas/domain/v1/run-artifact-content.schema.json", domain.RunArtifactContent{}},
		{"schemas/domain/v1/workstream-delivery.schema.json", domain.WorkstreamDelivery{}},
		{"schemas/local/v1/managed-service-definition-create.params.schema.json", localapi.ManagedServiceDefinitionCreateParams{}},
		{"schemas/local/v1/managed-service-definition-retire.params.schema.json", localapi.ManagedServiceDefinitionRetireParams{}},
		{"schemas/local/v1/managed-service-definition-query.params.schema.json", localapi.ManagedServiceDefinitionQueryParams{}},
		{"schemas/local/v1/managed-service-start.params.schema.json", localapi.ManagedServiceStartParams{}},
		{"schemas/local/v1/managed-service-action.params.schema.json", localapi.ManagedServiceActionParams{}},
		{"schemas/local/v1/managed-service-query.params.schema.json", localapi.ManagedServiceQueryParams{}},
		{"schemas/local/v1/managed-service-logs.params.schema.json", localapi.ManagedServiceLogsParams{}},
		{"schemas/local/v1/managed-service-grant-create.params.schema.json", localapi.ManagedServiceGrantCreateParams{}},
		{"schemas/local/v1/managed-service-grant-revoke.params.schema.json", localapi.ManagedServiceGrantRevokeParams{}},
		{"schemas/local/v1/managed-service-grant-query.params.schema.json", localapi.ManagedServiceGrantQueryParams{}},
		{"schemas/local/v1/managed-service-request-query.params.schema.json", localapi.ManagedServiceRequestQueryParams{}},
		{"schemas/local/v1/managed-service-request-decision.params.schema.json", localapi.ManagedServiceRequestDecisionParams{}},
		{"schemas/local/v1/managed-service-definition-mutation.result.schema.json", localapi.ManagedServiceDefinitionMutationResult{}},
		{"schemas/local/v1/managed-service-definition-show.result.schema.json", localapi.ManagedServiceDefinitionShowResult{}},
		{"schemas/local/v1/managed-service-definition-list.result.schema.json", localapi.ManagedServiceDefinitionListResult{}},
		{"schemas/local/v1/managed-service-mutation.result.schema.json", localapi.ManagedServiceMutationResult{}},
		{"schemas/local/v1/managed-service-show.result.schema.json", localapi.ManagedServiceShowResult{}},
		{"schemas/local/v1/managed-service-list.result.schema.json", localapi.ManagedServiceListResult{}},
		{"schemas/local/v1/managed-service-logs.result.schema.json", localapi.ManagedServiceLogsResult{}},
		{"schemas/local/v1/managed-service-grant-mutation.result.schema.json", localapi.ManagedServiceGrantMutationResult{}},
		{"schemas/local/v1/managed-service-grant-list.result.schema.json", localapi.ManagedServiceGrantListResult{}},
		{"schemas/local/v1/managed-service-request-list.result.schema.json", localapi.ManagedServiceRequestListResult{}},
		{"schemas/local/v1/managed-service-request-mutation.result.schema.json", localapi.ManagedServiceRequestMutationResult{}},
		{"schemas/local/v1/run-artifact-show.params.schema.json", localapi.RunArtifactShowParams{}},
		{"schemas/local/v1/run-artifact-show.result.schema.json", localapi.RunArtifactShowResult{}},
		{"schemas/local/v1/workstream-delivery-query.params.schema.json", localapi.WorkstreamDeliveryQueryParams{}},
		{"schemas/local/v1/workstream-delivery-decision.params.schema.json", localapi.WorkstreamDeliveryDecisionParams{}},
		{"schemas/local/v1/workstream-delivery-show.result.schema.json", localapi.WorkstreamDeliveryShowResult{}},
		{"schemas/local/v1/workstream-delivery-mutation.result.schema.json", localapi.WorkstreamDeliveryMutationResult{}},
	} {
		assertCheckSchemaFields(t, test.path, test.value)
	}
}

func TestManagedServiceSchemasRejectUnknownFieldsAtEveryObjectBoundary(t *testing.T) {
	t.Parallel()
	paths := []string{}
	for _, pattern := range []string{
		"schemas/domain/v1/managed-service-*.schema.json", "schemas/local/v1/managed-service-*.schema.json",
		"schemas/domain/v1/run-artifact*.schema.json", "schemas/local/v1/run-artifact-*.schema.json",
		"schemas/domain/v1/workstream-delivery.schema.json", "schemas/local/v1/workstream-delivery-*.schema.json",
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, matches...)
	}
	for _, path := range paths {
		assertStrictManagedServiceSchemaObjects(t, path, "$", readContextSchema(t, path))
	}
}

func assertStrictManagedServiceSchemaObjects(t *testing.T, path, location string, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		if typed["type"] == "object" && typed["additionalProperties"] != false {
			t.Errorf("%s %s does not reject unknown fields", path, location)
		}
		for name, child := range typed {
			assertStrictManagedServiceSchemaObjects(t, path, location+"."+name, child)
		}
	case []any:
		for index, child := range typed {
			assertStrictManagedServiceSchemaObjects(t, path, location+"["+strconv.Itoa(index)+"]", child)
		}
	}
}
