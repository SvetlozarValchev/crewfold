package localapi

import "crewfold/internal/domain"

const (
	MethodRunArtifactShow = "run.artifact.show"
	RunArtifactShowSchema = "urn:crewfold:schema:local-api:run-artifact-show-result:v1"
)

type RunArtifactShowParams struct {
	Workspace string `json:"workspace"`
	Artifact  string `json:"artifact"`
}

type RunArtifactShowResult struct {
	Schema   string                    `json:"schema"`
	Type     string                    `json:"type"`
	Artifact domain.RunArtifactContent `json:"artifact"`
}
