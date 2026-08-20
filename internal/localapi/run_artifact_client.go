package localapi

import "context"

func (c *Client) RunArtifactShow(ctx context.Context, workspace, artifact string) (RunArtifactShowResult, error) {
	workspaceID, err := c.resolveOperatorWorkspace(ctx, workspace)
	if err != nil {
		return RunArtifactShowResult{}, err
	}
	var result RunArtifactShowResult
	params := RunArtifactShowParams{Workspace: workspaceID, Artifact: artifact}
	if err := c.callParamsStrict(ctx, MethodRunArtifactShow, params, &result); err != nil {
		return RunArtifactShowResult{}, err
	}
	return result, nil
}
