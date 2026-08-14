package store

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func TestAcceptedDeliveryBriefingPreservesSelfReportTrustAndOwnerAcceptanceBasis(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, true)
	commitment := fixture.createCommitment(t, "briefing-self-report-trust")
	handoffID := fixture.completeRun(t)
	accepted := acceptSupportedOutcomeForBriefingTrust(t, storage, fixture.workspace.ID, fixture.task.Task.ID,
		commitment.Commitment.ID, domain.OutcomeEvidenceHandoff, handoffID, "briefing-self-report-trust")
	if len(accepted.Detail.Evidence) != 1 || accepted.Detail.Evidence[0].Class != domain.EvidenceAgentSelfReport {
		t.Fatalf("accepted self-report evidence = %#v", accepted.Detail.Evidence)
	}
	assertAcceptedDeliveryBriefingTrust(t, storage, fixture.workspace.ID, fixture.task.Task.ID, handoffID,
		domain.OutcomeEvidenceHandoff, domain.EvidenceAgentSelfReport, domain.CheckEvidenceSupports,
		domain.OutcomeEvidenceFresh, domain.OutcomeEvidenceFresh)
}

func TestAcceptedDeliveryBriefingPreservesMechanicalTrustAndFreshness(t *testing.T) {
	storage, fixture := newOutcomeAdversarialFixture(t, true)
	commitment := fixture.createCommitment(t, "briefing-mechanical-trust")
	if err := os.MkdirAll(fixture.checkout.Path, 0o700); err != nil {
		t.Fatalf("MkdirAll(checkout) = %v", err)
	}
	definition, err := storage.CreateCheckDefinition(context.Background(), CreateCheckDefinitionCommand{
		WorkspaceIdentifier: fixture.workspace.ID,
		ProjectIdentifier:   fixture.project.ID,
		Name:                "briefing-mechanical-trust",
		Executable:          "/bin/true",
		Arguments:           []string{},
		WorkingDirectory:    ".",
		TimeoutMillis:       1000,
		OutputByteLimit:     1024,
		IdempotencyKey:      "briefing-mechanical-trust-definition",
		CorrelationID:       "request-briefing-mechanical-trust-definition",
	})
	if err != nil {
		t.Fatalf("CreateCheckDefinition() = %v", err)
	}
	requirement, err := storage.CreateTaskCheckRequirement(context.Background(), CreateTaskCheckRequirementCommand{
		WorkspaceIdentifier:       fixture.workspace.ID,
		TaskID:                    fixture.task.Task.ID,
		CriterionKey:              "briefing-mechanical-trust",
		Statement:                 "the exact owner-run check passes",
		CheckDefinitionID:         definition.Value.ID,
		DefinitionContentRevision: definition.Value.ContentRevision,
		ExpectedTaskRevision:      fixture.task.Task.Revision,
		IdempotencyKey:            "briefing-mechanical-trust-requirement",
		CorrelationID:             "request-briefing-mechanical-trust-requirement",
	})
	if err != nil {
		t.Fatalf("CreateTaskCheckRequirement() = %v", err)
	}
	requested, err := storage.RequestCheckRun(context.Background(), RequestCheckRunCommand{
		WorkspaceIdentifier:               fixture.workspace.ID,
		TaskID:                            fixture.task.Task.ID,
		RequirementID:                     requirement.Value.ID,
		CheckDefinitionIdentifier:         definition.Value.ID,
		CheckoutIdentifier:                fixture.checkout.ID,
		ExpectedRequirementRevision:       requirement.Value.Revision,
		ExpectedDefinitionContentRevision: definition.Value.ContentRevision,
		ExpectedCheckoutRevision:          fixture.checkout.Revision,
		IdempotencyKey:                    "briefing-mechanical-trust-run",
		CorrelationID:                     "request-briefing-mechanical-trust-run",
	})
	if err != nil {
		t.Fatalf("RequestCheckRun() = %v", err)
	}
	work, found, err := storage.ClaimCheckJob(context.Background(), 30*time.Second)
	if err != nil || !found || work.Run.ID != requested.Value.ID {
		t.Fatalf("ClaimCheckJob() = %#v, %t, %v", work, found, err)
	}
	observation := domain.CheckGitObservation{
		Available:    true,
		RepositoryID: work.Run.RepositoryID,
		ObjectFormat: work.Run.RepositoryObjectFormat,
		CheckoutID:   work.Run.CheckoutID,
		Branch:       fixture.checkout.Branch,
		HeadCommit:   fixture.checkout.HeadCommit,
		DirtyPaths:   []string{},
		ObservedAt:   storage.nowText(),
	}
	started, err := storage.MarkCheckStarting(context.Background(), MarkCheckStartingCommand{
		CheckRunID:                work.Run.ID,
		OperationID:               work.Run.ID,
		EffectiveSpecSHA256:       strings.Repeat("a", 64),
		EffectiveWorkingDirectory: fixture.checkout.Path,
		Launchable:                true,
		Observation:               observation,
		CorrelationID:             "briefing-mechanical-trust-starting",
	})
	if err != nil || started.LaunchReceipt == nil || !started.LaunchReceipt.Launchable {
		t.Fatalf("MarkCheckStarting() = %#v, %v", started, err)
	}
	if _, err := storage.RecordCheckRuntimeBinding(context.Background(), work.Run.ID, "direct:"+work.Run.ID, "briefing-mechanical-trust-binding"); err != nil {
		t.Fatalf("RecordCheckRuntimeBinding() = %v", err)
	}
	if _, err := storage.MarkCheckRunning(context.Background(), work.Run.ID, "briefing-mechanical-trust-running"); err != nil {
		t.Fatalf("MarkCheckRunning() = %v", err)
	}
	exitCode := 0
	terminal := started.LaunchReceipt.Observation
	terminal.ObservedAt = storage.nowText()
	finished, err := storage.FinishCheckRun(context.Background(), FinishCheckRunCommand{
		CheckRunID: work.Run.ID, Outcome: domain.CheckOutcomePassed, ExitCode: &exitCode,
		TerminalObservation: terminal, CorrelationID: "briefing-mechanical-trust-finished",
	})
	if err != nil || finished.Result == nil || finished.CurrentFreshness == nil ||
		finished.CurrentFreshness.Status != domain.CheckFreshnessFresh || len(finished.Evidence.MechanicalCheck) != 1 {
		t.Fatalf("FinishCheckRun() = %#v, %v", finished, err)
	}
	evidenceID := finished.Evidence.MechanicalCheck[0].ID
	accepted := acceptSupportedOutcomeForBriefingTrust(t, storage, fixture.workspace.ID, fixture.task.Task.ID,
		commitment.Commitment.ID, domain.OutcomeEvidenceCheckRequirementEvidence, evidenceID, "briefing-mechanical-trust")
	if len(accepted.Detail.Evidence) != 1 || accepted.Detail.Evidence[0].Class != domain.EvidenceMechanicalCheck {
		t.Fatalf("accepted mechanical evidence = %#v", accepted.Detail.Evidence)
	}
	assertAcceptedDeliveryBriefingTrust(t, storage, fixture.workspace.ID, fixture.task.Task.ID, evidenceID,
		domain.OutcomeEvidenceCheckRequirementEvidence, domain.EvidenceMechanicalCheck, domain.CheckEvidenceSupports,
		domain.OutcomeEvidenceFresh, domain.OutcomeEvidenceFresh)
}

func TestAcceptedDeliveryBriefingPreservesIndependentReviewTrust(t *testing.T) {
	const key = "briefing-independent-trust"
	fixture := newAssignmentReviewValidationFixture(t, key)
	storage := fixture.storage
	if _, err := storage.db.Exec(`UPDATE checkouts SET write_mode='shared' WHERE project_id=?`, fixture.base.project.ID); err != nil {
		t.Fatalf("configure shared fixture checkout = %v", err)
	}
	target := fixture.createTask(t, key+"-target")
	commitment, err := storage.CreateDeliverableCommitment(context.Background(), CreateDeliverableCommitmentCommand{
		WorkspaceIdentifier: fixture.base.workspace.ID,
		TaskID:              target.Task.ID,
		Key:                 key,
		Title:               "Independently reviewed deliverable",
		AcceptanceCriteria:  []string{"an independent review handoff supports the exact target"},
		IdempotencyKey:      key + "-commitment",
		CorrelationID:       "request-" + key + "-commitment",
	})
	if err != nil {
		t.Fatalf("CreateDeliverableCommitment() = %v", err)
	}
	assigned, err := storage.AssignTask(context.Background(), AssignTaskCommand{
		WorkspaceIdentifier: fixture.base.workspace.ID,
		TaskID:              target.Task.ID,
		AgentIdentifier:     fixture.base.target.AgentID,
		LeaseSeconds:        900,
		ExpectedRevision:    target.Task.Revision,
		IdempotencyKey:      key + "-target-assignment",
		CorrelationID:       "request-" + key + "-target-assignment",
	})
	if err != nil {
		t.Fatalf("AssignTask(target) = %v", err)
	}
	var checkoutID string
	if err := storage.db.QueryRow(`SELECT id FROM checkouts WHERE project_id=? ORDER BY id LIMIT 1`, fixture.base.project.ID).Scan(&checkoutID); err != nil {
		t.Fatalf("read target checkout = %v", err)
	}
	created, err := storage.CreateRun(context.Background(), CreateRunCommand{
		WorkspaceIdentifier: fixture.base.workspace.ID,
		TaskID:              target.Task.ID,
		CheckoutIdentifier:  checkoutID,
		Runtime:             "fake",
		Provider:            "fake",
		Scenario: domain.FakeScenario{
			Schema: execution.FakeScenarioSchema,
			Name:   key + "-target",
			Steps:  []domain.FakeStep{{Kind: domain.ObservationCompletion, Message: "target completed"}},
		},
		ExpectedTaskRevision: assigned.Detail.Task.Revision,
		IdempotencyKey:       key + "-target-run",
		CorrelationID:        "request-" + key + "-target-run",
	})
	if err != nil {
		t.Fatalf("CreateRun(target) = %v", err)
	}
	targetRun := completeOutcomeTrustRun(t, storage, created.Detail.Run.ID, key+"-target")
	if targetRun.Handoff == nil {
		t.Fatal("completed target did not retain its self-report handoff")
	}
	completedTarget, err := storage.TaskDetail(context.Background(), fixture.base.workspace.ID, target.Task.ID)
	if err != nil || completedTarget.Task.Status != domain.TaskCompleted {
		t.Fatalf("TaskDetail(completed target) = %#v, %v", completedTarget, err)
	}

	submitted := fixture.submitReview(t, completedTarget.Task, fixture.reviewerProfile.ID, key)
	acceptedReview := fixture.acceptProposal(t, submitted, key)
	var reviewTaskID, reviewIntentID string
	for _, effect := range acceptedReview.Effects {
		if effect.EntityType == "task" && effect.EffectType == "created" {
			reviewTaskID = effect.EntityID
		}
		if effect.EntityType == "scheduling_intent" {
			reviewIntentID = effect.EntityID
		}
	}
	if reviewTaskID == "" || reviewIntentID == "" {
		t.Fatalf("accepted review effects = %#v", acceptedReview.Effects)
	}
	configureSupervisorForContention(t, storage, fixture.base.workspace.ID, domain.SupervisorLimits{
		MaxActiveRuns: 8, MaxStartingRuns: 8, DefaultProjectConcurrency: 8, DefaultProviderConcurrency: 8,
	}, key)
	scheduled, err := storage.RunSupervisor(context.Background(), RunSupervisorCommand{
		WorkspaceIdentifier: fixture.base.workspace.ID,
		Limit:               100,
		IdempotencyKey:      key + "-review-schedule",
		CorrelationID:       "request-" + key + "-review-schedule",
	})
	if err != nil || len(scheduled.ScheduledRunIDs) != 1 {
		t.Fatalf("RunSupervisor(review) = %#v, %v", scheduled, err)
	}
	var scheduledTaskID string
	if err := storage.db.QueryRow(`SELECT task_id FROM runs WHERE id=?`, scheduled.ScheduledRunIDs[0]).Scan(&scheduledTaskID); err != nil || scheduledTaskID != reviewTaskID {
		t.Fatalf("scheduled review task = %q, %v; want %q", scheduledTaskID, err, reviewTaskID)
	}
	reviewRun := completeOutcomeTrustRun(t, storage, scheduled.ScheduledRunIDs[0], key+"-review")
	if reviewRun.Handoff == nil {
		t.Fatal("completed independent review did not retain a handoff")
	}
	var satisfiedIntent int
	if err := storage.db.QueryRow(`SELECT COUNT(*) FROM scheduling_intents WHERE id=? AND status='satisfied'`, reviewIntentID).Scan(&satisfiedIntent); err != nil || satisfiedIntent != 1 {
		t.Fatalf("review scheduling intent satisfied count = %d, %v", satisfiedIntent, err)
	}
	assessment := acceptSupportedOutcomeForBriefingTrust(t, storage, fixture.base.workspace.ID, target.Task.ID,
		commitment.Commitment.ID, domain.OutcomeEvidenceHandoff, reviewRun.Handoff.ID, key)
	if len(assessment.Detail.Evidence) != 1 || assessment.Detail.Evidence[0].Class != domain.EvidenceIndependentReview {
		t.Fatalf("accepted independent-review evidence = %#v", assessment.Detail.Evidence)
	}
	assertAcceptedDeliveryBriefingTrust(t, storage, fixture.base.workspace.ID, target.Task.ID, reviewRun.Handoff.ID,
		domain.OutcomeEvidenceHandoff, domain.EvidenceIndependentReview, domain.CheckEvidenceSupports,
		domain.OutcomeEvidenceFresh, domain.OutcomeEvidenceFresh)
}

func completeOutcomeTrustRun(t *testing.T, storage *Store, runID, key string) domain.RunDetail {
	t.Helper()
	starting, err := storage.MarkRunStarting(context.Background(), runID, "request-"+key+"-starting")
	if err != nil {
		t.Fatalf("MarkRunStarting(%s) = %v", key, err)
	}
	if _, err := storage.MarkRunStarted(context.Background(), starting.ID, key+"-runtime", key+"-provider", "request-"+key+"-started"); err != nil {
		t.Fatalf("MarkRunStarted(%s) = %v", key, err)
	}
	completed, err := storage.ApplyRunObservation(context.Background(), starting.ID, domain.RunObservation{
		Kind: domain.ObservationCompletion, Message: key + " completed exact work",
		Evidence: []string{key + " exact evidence"}, Handoff: key + " exact handoff", LogArchive: prepareTestRunLogArchive(t, storage, starting.ID),
	}, true, nil, "request-"+key+"-completed")
	if err != nil {
		t.Fatalf("ApplyRunObservation(%s) = %v", key, err)
	}
	return completed
}

func acceptSupportedOutcomeForBriefingTrust(t *testing.T, storage *Store, workspaceID, taskID, commitmentID, sourceType, sourceID, key string) OutcomeAssessmentMutationResult {
	t.Helper()
	proposed, err := storage.ProposeOutcomeAssessment(context.Background(), ProposeOutcomeAssessmentCommand{
		WorkspaceIdentifier: workspaceID,
		TaskID:              taskID,
		CommitmentID:        commitmentID,
		Input: domain.OutcomeAssessmentInput{
			Conclusion:          domain.OutcomeAchieved,
			DeliveredScope:      []string{"the promised deliverable"},
			UnmetScope:          []string{},
			DecisionRevisionIDs: []string{},
			Evidence:            []domain.OutcomeEvidenceInput{{SourceType: sourceType, SourceID: sourceID}},
			Effects:             []domain.OutcomeEffectInput{},
			Deviations:          []domain.OutcomeDeviationInput{},
			Risks:               []domain.OutcomeRiskInput{},
			Unknowns:            []domain.OutcomeUnknownInput{},
			FollowUpTaskIDs:     []string{},
			OwnerAttention:      []domain.OutcomeOwnerAttentionInput{},
		},
		IdempotencyKey: key + "-outcome-propose",
		CorrelationID:  "request-" + key + "-outcome-propose",
	})
	if err != nil {
		t.Fatalf("ProposeOutcomeAssessment(%s) = %v", key, err)
	}
	accepted, err := storage.AcceptOutcomeAssessment(context.Background(), DecideOutcomeAssessmentCommand{
		WorkspaceIdentifier:   workspaceID,
		AssessmentID:          proposed.Detail.Assessment.ID,
		ExpectedStateRevision: proposed.Detail.Assessment.StateRevision,
		DecisionNote:          "owner accepts exact evidence without upgrading its trust class",
		IdempotencyKey:        key + "-outcome-accept",
		CorrelationID:         "request-" + key + "-outcome-accept",
	})
	if err != nil {
		t.Fatalf("AcceptOutcomeAssessment(%s) = %v", key, err)
	}
	return accepted
}

func assertAcceptedDeliveryBriefingTrust(t *testing.T, storage *Store, workspaceID, taskID, evidenceID, evidenceType, evidenceClass, evidenceEffect, pinnedFreshness, currentFreshness string) {
	t.Helper()
	briefing, err := storage.ShowManagementBriefing(context.Background(), ShowManagementBriefingQuery{
		WorkspaceIdentifier: workspaceID,
		ScopeType:           domain.OwnerCheckpointTask,
		ScopeIdentifier:     taskID,
	})
	if err != nil {
		t.Fatalf("ShowManagementBriefing() = %v", err)
	}
	var delivery *domain.BriefingClaim
	var evidenceSource, acceptanceSource *domain.BriefingClaimSource
	for index := range briefing.Claims {
		claim := &briefing.Claims[index]
		if claim.Kind != domain.BriefingClaimAcceptedDelivery {
			continue
		}
		for sourceIndex := range claim.Sources {
			source := &claim.Sources[sourceIndex]
			if source.EntityType == evidenceType && source.EntityID == evidenceID {
				delivery, evidenceSource = claim, source
			}
			if source.EntityType == "outcome_assessment_acceptance_basis" {
				acceptanceSource = source
			}
		}
	}
	if delivery == nil || evidenceSource == nil {
		t.Fatalf("accepted-delivery claims = %#v; want exact evidence %s/%s", briefing.Claims, evidenceType, evidenceID)
	}
	want := domain.BriefingClaimSource{
		EntityType: evidenceType, EntityID: evidenceID, Revision: evidenceSource.Revision,
		ContentSHA256: evidenceSource.ContentSHA256, EventSequence: evidenceSource.EventSequence,
		EvidenceClass: evidenceClass, EvidenceEffect: evidenceEffect,
		PinnedFreshness: pinnedFreshness, CurrentFreshness: currentFreshness,
	}
	if evidenceSource.Revision < 1 || evidenceSource.ContentSHA256 == "" || evidenceSource.EventSequence < 1 || !reflect.DeepEqual(*evidenceSource, want) {
		t.Fatalf("briefing evidence source = %#v, want exact derived %#v", *evidenceSource, want)
	}
	if acceptanceSource == nil || acceptanceSource.EntityID == "" || acceptanceSource.ContentSHA256 == "" ||
		acceptanceSource.EventSequence < 1 || acceptanceSource.EvidenceClass != "" || acceptanceSource.EvidenceEffect != "" ||
		acceptanceSource.PinnedFreshness != "" || acceptanceSource.CurrentFreshness != "" {
		t.Fatalf("briefing owner acceptance basis = %#v", acceptanceSource)
	}
	explanation, err := storage.ExplainManagementBriefingClaim(context.Background(), ExplainManagementBriefingClaimQuery{
		WorkspaceIdentifier: workspaceID,
		BriefingID:          briefing.ID,
		ClaimID:             delivery.ID,
	})
	if err != nil {
		t.Fatalf("ExplainManagementBriefingClaim() = %v", err)
	}
	if !reflect.DeepEqual(explanation.Claim, *delivery) || !reflect.DeepEqual(explanation.Provenance, delivery.Sources) {
		t.Fatalf("briefing explanation lost exact trust provenance: %#v", explanation)
	}
	foundEvidenceDiagnosis, foundAcceptanceDiagnosis := false, false
	for _, diagnosis := range explanation.Diagnoses {
		foundEvidenceDiagnosis = foundEvidenceDiagnosis || strings.Contains(diagnosis, "evidence") || strings.Contains(diagnosis, "handoff")
		foundAcceptanceDiagnosis = foundAcceptanceDiagnosis || strings.Contains(diagnosis, "owner acceptance basis")
	}
	if !foundEvidenceDiagnosis || !foundAcceptanceDiagnosis {
		t.Fatalf("briefing diagnoses = %v; want evidence and owner acceptance basis", explanation.Diagnoses)
	}
}
