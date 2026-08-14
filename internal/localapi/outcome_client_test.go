package localapi

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"crewfold/internal/domain"
)

func TestOutcomeClientsRejectWrongResultDiscriminators(t *testing.T) {
	t.Parallel()
	calls := map[string]struct {
		call   func(*Client) error
		schema string
		kind   string
	}{
		MethodOutcomeCommitmentCreate: {func(client *Client) error {
			_, err := client.OutcomeCommitmentCreate(context.Background(), OutcomeCommitmentCreateParams{})
			return err
		}, OutcomeCommitmentMutationSchema, "outcome_commitment_mutation"},
		MethodOutcomeCommitmentShow: {func(client *Client) error {
			_, err := client.OutcomeCommitmentShow(context.Background(), "personal", "outcommit_exact")
			return err
		}, OutcomeCommitmentShowSchema, "outcome_commitment"},
		MethodOutcomeCommitmentList: {func(client *Client) error {
			_, err := client.OutcomeCommitmentList(context.Background(), OutcomeCommitmentQueryParams{})
			return err
		}, OutcomeCommitmentListSchema, "outcome_commitment_list"},
		MethodOutcomePropose: {func(client *Client) error {
			_, err := client.OutcomePropose(context.Background(), OutcomeProposeParams{})
			return err
		}, OutcomeMutationSchema, "outcome_mutation"},
		MethodOutcomeShow: {func(client *Client) error {
			_, err := client.OutcomeShow(context.Background(), "personal", "outassess_exact")
			return err
		}, OutcomeShowSchema, "outcome"},
		MethodOutcomeList: {func(client *Client) error {
			_, err := client.OutcomeList(context.Background(), OutcomeQueryParams{})
			return err
		}, OutcomeListSchema, "outcome_list"},
		MethodOutcomeAccept: {func(client *Client) error {
			_, err := client.OutcomeAccept(context.Background(), OutcomeDecisionParams{})
			return err
		}, OutcomeMutationSchema, "outcome_mutation"},
		MethodOutcomeReject: {func(client *Client) error {
			_, err := client.OutcomeReject(context.Background(), OutcomeDecisionParams{})
			return err
		}, OutcomeMutationSchema, "outcome_mutation"},
		MethodCheckpointCreate: {func(client *Client) error {
			_, err := client.CheckpointCreate(context.Background(), CheckpointCreateParams{})
			return err
		}, CheckpointMutationSchema, "checkpoint_mutation"},
		MethodCheckpointShow: {func(client *Client) error {
			_, err := client.CheckpointShow(context.Background(), "personal", "outcpnt_exact")
			return err
		}, CheckpointShowSchema, "checkpoint"},
		MethodCheckpointList: {func(client *Client) error {
			_, err := client.CheckpointList(context.Background(), CheckpointQueryParams{})
			return err
		}, CheckpointListSchema, "checkpoint_list"},
		MethodBriefingShow: {func(client *Client) error {
			_, err := client.BriefingShow(context.Background(), BriefingShowParams{
				Workspace: "ws_00000000000000000000000000000000", ScopeType: domain.OwnerCheckpointWorkspace,
				ScopeIdentifier: "ws_00000000000000000000000000000000",
			})
			return err
		}, BriefingShowSchema, "management_briefing"},
		MethodBriefingExplain: {func(client *Client) error {
			_, err := client.BriefingExplain(context.Background(), BriefingExplainParams{Workspace: "ws_00000000000000000000000000000000"})
			return err
		}, BriefingExplainSchema, "briefing_claim_explanation"},
	}
	if len(calls) != 13 {
		t.Fatalf("outcome client count=%d", len(calls))
	}
	for method, test := range calls {
		method, test := method, test
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			for name, result := range map[string]map[string]any{
				"schema": {"schema": "urn:wrong", "type": test.kind},
				"type":   {"schema": test.schema, "type": "wrong"},
			} {
				err := captureCuratorRequestResultError(t, method, test.call, result)
				if err == nil || !strings.Contains(err.Error(), "discriminator") {
					t.Errorf("%s error=%v", name, err)
				}
			}
		})
	}
}

func TestOutcomeProposeClientNormalizesArraysAndExposesNoAuthorityLabels(t *testing.T) {
	t.Parallel()
	request := captureCuratorRequest(t, MethodOutcomePropose, func(client *Client) error {
		_, err := client.OutcomePropose(context.Background(), OutcomeProposeParams{
			Workspace: "personal", Task: "task_exact", Commitment: "outcommit_exact",
			Assessment: domain.OutcomeAssessmentInput{Conclusion: domain.OutcomeUnknown},
		})
		return err
	}, OutcomeMutationResult{Schema: OutcomeMutationSchema, Type: "outcome_mutation"})
	var params map[string]any
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatal(err)
	}
	assessment, ok := params["assessment"].(map[string]any)
	if !ok {
		t.Fatalf("assessment=%#v", params["assessment"])
	}
	for _, field := range []string{"delivered_scope", "unmet_scope", "decision_revision_ids", "evidence", "effects", "deviations", "risks", "unknowns", "follow_up_task_ids", "owner_attention"} {
		value, exists := assessment[field]
		if !exists {
			t.Errorf("missing %s: %s", field, request.Params)
			continue
		}
		if values, ok := value.([]any); !ok || len(values) != 0 {
			t.Errorf("%s=%#v, want empty array", field, value)
		}
	}
	for _, forbidden := range []string{"actor", "authority", "agent", "role", "purpose", "class", "freshness", "strength", "policy_acceptance"} {
		if strings.Contains(string(request.Params), `"`+forbidden+`"`) {
			t.Errorf("request exposed caller-selected %q: %s", forbidden, request.Params)
		}
	}
	if key, ok := params["idempotency_key"].(string); !ok || strings.TrimSpace(key) == "" {
		t.Fatalf("idempotency_key=%#v", params["idempotency_key"])
	}
}

func TestBriefingShowClientHasNoCallerSelectedHistoricCursor(t *testing.T) {
	t.Parallel()
	request := captureCuratorRequest(t, MethodBriefingShow, func(client *Client) error {
		_, err := client.BriefingShow(context.Background(), BriefingShowParams{
			Workspace: "ws_" + strings.Repeat("b", 32), ScopeType: domain.OwnerCheckpointProject,
			ScopeIdentifier: "prj_" + strings.Repeat("c", 32), SinceCheckpoint: "outcpnt_exact",
		})
		return err
	}, BriefingShowResult{Schema: BriefingShowSchema, Type: "management_briefing", Briefing: domain.ManagementBriefing{
		ID: "briefing_" + strings.Repeat("a", 32), Revision: 1,
		Scope: domain.BriefingScope{
			Type: domain.OwnerCheckpointProject, WorkspaceID: "ws_" + strings.Repeat("b", 32), ProjectID: "prj_" + strings.Repeat("c", 32),
		},
		EvaluatedAt: "2026-08-14T12:00:00Z", CaughtUp: true,
		Claims: []domain.BriefingClaim{}, Omitted: []domain.BriefingOmission{},
		ContentSHA256: strings.Repeat("d", 64), ByteSize: 1,
	}})
	encoded := string(request.Params)
	if !strings.Contains(encoded, `"since_checkpoint":"outcpnt_exact"`) {
		t.Fatalf("request=%s", encoded)
	}
	for _, forbidden := range []string{"at_event_sequence", "event_cursor", "as_of", "historic_cursor", "renderer", "narrative"} {
		if strings.Contains(encoded, `"`+forbidden+`"`) {
			t.Errorf("briefing request exposed %q: %s", forbidden, encoded)
		}
	}
}

func TestOutcomeClientRejectsUnknownResultFields(t *testing.T) {
	t.Parallel()
	err := captureCuratorRequestResultError(t, MethodCheckpointShow, func(client *Client) error {
		_, err := client.CheckpointShow(context.Background(), "personal", "outcpnt_exact")
		return err
	}, map[string]any{
		"schema": CheckpointShowSchema, "type": "checkpoint", "checkpoint": map[string]any{},
		"archive_status": "deprecated",
	})
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown result error=%v", err)
	}
}
