package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
)

func TestOutcomeCommitmentAddForwardsExactOwnerInput(t *testing.T) {
	client := &fakeDaemonClient{outcomeCommitmentMutation: localapi.OutcomeCommitmentMutationResult{
		Schema: localapi.OutcomeCommitmentMutationSchema, Type: "outcome_commitment_mutation",
		Commitment: domain.DeliverableCommitment{ID: "outcommit_11111111111111111111111111111111", Key: "release", TaskID: "task_11111111111111111111111111111111", AcceptanceCriteria: []string{"API works", "Docs match"}},
	}}
	app, stdout, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	exit := app.Run([]string{"outcome", "commitment", "add", "release", "--task", "task_11111111111111111111111111111111", "--title", "Ship the release", "--description", "Exact promised scope", "--criterion", "API works", "--criterion", "Docs match", "--workspace", "personal", "--socket", "/tmp/crewfold.sock", "--idempotency-key", "commit-key"})
	if exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}
	wantCriteria := []string{"API works", "Docs match"}
	params := client.outcomeCommitmentParams
	if params.Workspace != "personal" || params.Task != "task_11111111111111111111111111111111" || params.Key != "release" || params.Title != "Ship the release" || params.Description != "Exact promised scope" || params.IdempotencyKey != "commit-key" || !equalStrings(params.AcceptanceCriteria, wantCriteria) {
		t.Fatalf("params=%#v", params)
	}
	if !strings.Contains(stdout.String(), "commitment: outcommit_") || !strings.Contains(stdout.String(), "criteria: 2") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestOutcomeProposeReadsOneStrictBoundedStructuredDocument(t *testing.T) {
	path := filepath.Join(t.TempDir(), "assessment.json")
	input := domain.OutcomeAssessmentInput{
		Conclusion:     domain.OutcomePartial,
		DeliveredScope: []string{"bounded API"}, UnmetScope: []string{"operator UI"},
		DecisionRevisionIDs: []string{"krev_11111111111111111111111111111111"},
		Evidence:            []domain.OutcomeEvidenceInput{{SourceType: domain.OutcomeEvidenceHandoff, SourceID: "handoff_11111111111111111111111111111111"}},
		Effects:             []domain.OutcomeEffectInput{{Kind: domain.OutcomeEffectCompatibility, Direction: domain.OutcomeEffectNeutral, Summary: "No compatibility layer"}},
		Deviations:          []domain.OutcomeDeviationInput{{Kind: domain.OutcomeDeviationScopeChange, Summary: "UI remains M19"}},
		Risks:               []domain.OutcomeRiskInput{{Severity: domain.OutcomeRiskMedium, Summary: "Owner load", Mitigation: "Use briefing"}},
		Unknowns:            []domain.OutcomeUnknownInput{{Summary: "Long-term scale"}},
		FollowUpTaskIDs:     []string{"task_22222222222222222222222222222222"},
		OwnerAttention:      []domain.OutcomeOwnerAttentionInput{{Urgency: domain.OutcomeAttentionNext, Action: "Review", Reason: "Partial delivery"}},
	}
	data, err := json.Marshal(outcomeProposalDocument{Commitment: "outcommit_11111111111111111111111111111111", Assessment: input})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeDaemonClient{outcomeMutation: localapi.OutcomeMutationResult{Schema: localapi.OutcomeMutationSchema, Type: "outcome_mutation", Detail: domain.OutcomeAssessmentDetail{Assessment: domain.OutcomeAssessment{ID: "outassess_11111111111111111111111111111111", CommitmentID: "outcommit_11111111111111111111111111111111", ReviewState: domain.OutcomeAssessmentProposed, Conclusion: domain.OutcomePartial, Revision: 1, StateRevision: 1}}}}
	app, stdout, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	exit := app.Run([]string{"outcome", "propose", "--task", "task_11111111111111111111111111111111", path, "--workspace", "personal", "--supersedes", "outassess_00000000000000000000000000000000", "--socket", "/tmp/crewfold.sock", "--idempotency-key", "outcome-key"})
	if exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}
	params := client.outcomeProposeParams
	if params.Workspace != "personal" || params.Task != "task_11111111111111111111111111111111" || params.Commitment != "outcommit_11111111111111111111111111111111" || params.SupersedesOutcome != "outassess_00000000000000000000000000000000" || params.IdempotencyKey != "outcome-key" || params.Assessment.Conclusion != domain.OutcomePartial || len(params.Assessment.Evidence) != 1 || len(params.Assessment.OwnerAttention) != 1 {
		t.Fatalf("params=%#v", params)
	}
	if !strings.Contains(stdout.String(), "review state: proposed") || !strings.Contains(stdout.String(), "conclusion: partial") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestOutcomeProposeReadsStrictYAMLWithExactWrapper(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outcome.yaml")
	document := `commitment: outcommit_11111111111111111111111111111111
assessment:
  conclusion: unknown
  delivered_scope: []
  unmet_scope:
    - Operator review remains
  decision_revision_ids: []
  evidence: []
  effects: []
  deviations: []
  risks: []
  unknowns:
    - summary: Final owner judgment
  follow_up_task_ids: []
  owner_attention:
    - urgency: now
      action: Review the evidence
      reason: Outcome is unknown
`
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	client := &fakeDaemonClient{outcomeMutation: localapi.OutcomeMutationResult{Schema: localapi.OutcomeMutationSchema, Type: "outcome_mutation", Detail: domain.OutcomeAssessmentDetail{Assessment: domain.OutcomeAssessment{ID: "outassess_11111111111111111111111111111111", ReviewState: domain.OutcomeAssessmentProposed, Conclusion: domain.OutcomeUnknown, Revision: 1, StateRevision: 1}}}}
	app, _, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	exit := app.Run([]string{"outcome", "propose", "--task", "task_11111111111111111111111111111111", path, "--workspace", "personal", "--socket", "/tmp/crewfold.sock"})
	if exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}
	params := client.outcomeProposeParams
	if params.Commitment != "outcommit_11111111111111111111111111111111" || params.Assessment.Conclusion != domain.OutcomeUnknown || len(params.Assessment.Unknowns) != 1 || len(params.Assessment.OwnerAttention) != 1 {
		t.Fatalf("params=%#v", params)
	}
}

func TestOutcomeProposeRejectsUnknownAndOversizeDocumentsBeforeDaemon(t *testing.T) {
	for name, contents := range map[string]string{
		"unknown field":       `{"commitment":"outcommit_11111111111111111111111111111111","assessment":{"conclusion":"achieved","delivered_scope":[],"unmet_scope":[],"decision_revision_ids":[],"evidence":[],"effects":[],"deviations":[],"risks":[],"unknowns":[],"follow_up_task_ids":[],"owner_attention":[],"authority":"agent"}}`,
		"duplicate field":     "commitment: outcommit_11111111111111111111111111111111\ncommitment: outcommit_22222222222222222222222222222222\nassessment: {}\n",
		"null field":          "commitment: outcommit_11111111111111111111111111111111\nassessment: null\n",
		"more than one value": `{} {}`,
		"oversize":            strings.Repeat("x", maximumOutcomeInputBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "assessment.json")
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatal(err)
			}
			client := &fakeDaemonClient{}
			app, _, stderr := newTestApp()
			app.newClient = func(string) daemonClient { return client }
			exit := app.Run([]string{"outcome", "propose", "--task", "task_11111111111111111111111111111111", path, "--workspace", "personal", "--socket", "/tmp/crewfold.sock"})
			if exit != ExitUsage || stderr.Len() == 0 {
				t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
			}
			if client.outcomeProposeParams.Commitment != "" {
				t.Fatalf("daemon called: %#v", client.outcomeProposeParams)
			}
		})
	}
}

func TestOutcomeDecisionsForwardExpectedStateRevision(t *testing.T) {
	for _, action := range []string{"accept", "reject"} {
		t.Run(action, func(t *testing.T) {
			client := &fakeDaemonClient{outcomeMutation: localapi.OutcomeMutationResult{Schema: localapi.OutcomeMutationSchema, Type: "outcome_mutation", Detail: domain.OutcomeAssessmentDetail{Assessment: domain.OutcomeAssessment{ID: "outassess_11111111111111111111111111111111", ReviewState: map[bool]string{true: domain.OutcomeAssessmentAccepted, false: domain.OutcomeAssessmentRejected}[action == "accept"], StateRevision: 2}}}}
			app, _, stderr := newTestApp()
			app.newClient = func(string) daemonClient { return client }
			exit := app.Run([]string{"outcome", action, "outassess_11111111111111111111111111111111", "--workspace", "personal", "--expected-state-revision", "1", "--decision-note", "Owner judgment", "--socket", "/tmp/crewfold.sock", "--idempotency-key", action + "-key"})
			if exit != ExitOK || stderr.Len() != 0 {
				t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
			}
			if client.outcomeDecisionAction != action || client.outcomeDecisionParams.ExpectedStateRevision != 1 || client.outcomeDecisionParams.DecisionNote != "Owner judgment" || client.outcomeDecisionParams.IdempotencyKey != action+"-key" {
				t.Fatalf("action=%s params=%#v", client.outcomeDecisionAction, client.outcomeDecisionParams)
			}
		})
	}
}

func TestCheckpointAndBriefingUseOneExactScope(t *testing.T) {
	client := &fakeDaemonClient{
		checkpointMutation: localapi.CheckpointMutationResult{Schema: localapi.CheckpointMutationSchema, Type: "checkpoint_mutation", Checkpoint: domain.OwnerCheckpoint{ID: "outcpnt_11111111111111111111111111111111", ScopeType: domain.OwnerCheckpointProject, ScopeID: "prj_11111111111111111111111111111111", EventSequence: 42}},
		briefingShow: localapi.BriefingShowResult{Schema: localapi.BriefingShowSchema, Type: "management_briefing", Briefing: domain.ManagementBriefing{
			ID: "briefing_11111111111111111111111111111111", Scope: domain.BriefingScope{Type: domain.OwnerCheckpointProject}, EventCursor: 52,
			Claims: []domain.BriefingClaim{}, Omitted: []domain.BriefingOmission{{Section: domain.BriefingSectionRisksUnknowns, Reason: domain.BriefingOmittedByteLimit, Count: 3}}, ByteSize: 512,
		}},
	}
	app, stdout, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	project := "prj_11111111111111111111111111111111"
	if exit := app.Run([]string{"checkpoint", "create", "--workspace", "personal", "--project", project, "--socket", "/tmp/crewfold.sock"}); exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("checkpoint exit=%d stderr=%q", exit, stderr.String())
	}
	if client.checkpointCreateParams.ScopeType != domain.OwnerCheckpointProject || client.checkpointCreateParams.ScopeIdentifier != project {
		t.Fatalf("checkpoint params=%#v", client.checkpointCreateParams)
	}
	if exit := app.Run([]string{"briefing", "show", "--workspace", "personal", "--project", project, "--since", "outcpnt_11111111111111111111111111111111", "--socket", "/tmp/crewfold.sock"}); exit != ExitOK || stderr.Len() != 0 {
		t.Fatalf("briefing exit=%d stderr=%q", exit, stderr.String())
	}
	if client.briefingShowParams.ScopeType != domain.OwnerCheckpointProject || client.briefingShowParams.ScopeIdentifier != project || client.briefingShowParams.SinceCheckpoint != "outcpnt_11111111111111111111111111111111" {
		t.Fatalf("briefing params=%#v", client.briefingShowParams)
	}
	if !strings.Contains(stdout.String(), "omitted\trisks_unknowns\tbyte_limit\t3\n") {
		t.Fatalf("briefing output loses section-specific omission accounting: %q", stdout.String())
	}
}

func TestCheckpointRejectsAmbiguousScopeBeforeDaemon(t *testing.T) {
	client := &fakeDaemonClient{}
	app, _, stderr := newTestApp()
	app.newClient = func(string) daemonClient { return client }
	exit := app.Run([]string{"checkpoint", "create", "--workspace", "personal", "--project", "demo", "--task", "task_11111111111111111111111111111111", "--socket", "/tmp/crewfold.sock"})
	if exit != ExitUsage || stderr.Len() == 0 {
		t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
	}
	if client.checkpointCreateParams.ScopeType != "" {
		t.Fatalf("daemon called: %#v", client.checkpointCreateParams)
	}
}

func TestOutcomeCheckpointBriefingHelpIsCurrentOnly(t *testing.T) {
	for _, topic := range []string{"outcome", "checkpoint", "briefing"} {
		app, stdout, stderr := newTestApp()
		if exit := app.Run([]string{"help", topic}); exit != ExitOK || stderr.Len() != 0 {
			t.Fatalf("topic=%s exit=%d stderr=%q", topic, exit, stderr.String())
		}
		text := strings.ToLower(stdout.String())
		for _, forbidden := range []string{"deprecated", "legacy", "archive checkpoint", "narrative renderer", "agent proposes"} {
			if strings.Contains(text, forbidden) {
				t.Errorf("%s help contains %q: %s", topic, forbidden, text)
			}
		}
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
