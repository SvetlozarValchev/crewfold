package daemon

import (
	"context"
	"strings"

	"crewfold/internal/localapi"
)

func (s *server) handleRunArtifactShow(request localapi.Request) localapi.Response {
	var params localapi.RunArtifactShowParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || !validCanonicalEntityID(params.Artifact, "artifact_") {
		return invalidParamsResponse(request, "run.artifact.show requires workspace and artifact")
	}
	artifact, err := s.store.RunArtifactContent(context.Background(), params.Workspace, params.Artifact)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.RunArtifactShowResult{
		Schema: localapi.RunArtifactShowSchema, Type: "run_artifact", Artifact: artifact,
	})
}
