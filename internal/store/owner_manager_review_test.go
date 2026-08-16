package store

import (
	"context"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
)

func TestM21WorkerReportsAndMessagesCoalesceIntoDurableManagerReviews(t *testing.T) {
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project, agent, checkout, assigned := initializeRunTest(t, storage, "manager-review-loop")
	profile, err := storage.CreateLaunchProfile(context.Background(), CreateLaunchProfileCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, AgentIdentifier: agent.ID,
		ExpectedAgentRevision: agent.Revision, Purpose: "implementation", Runtime: agent.Runtime, Provider: agent.Provider,
		CheckoutIdentifier: checkout.ID, Scenario: managementProgressScenario("manager-review-loop"),
		AssignmentLeaseSeconds: 300, CapabilityTTLSeconds: 300,
		IdempotencyKey: "manager-review-profile", CorrelationID: "manager-review-profile",
	})
	if err != nil {
		t.Fatal(err)
	}
	events := testWorkspaceEvents(t, storage, workspace.ID, 0, 100)
	conversation, err := storage.PrepareOwnerTurn(context.Background(), PrepareOwnerTurnCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, Instruction: "Coordinate this project", Kind: "query",
		IdempotencyKey: "manager-review-owner-turn", ExpectedEventSequence: events[len(events)-1].Sequence,
		Interpretation: domain.OwnerInterpretation{Disposition: "answer", Summary: "Ready", Answer: "I will review worker updates.", ObjectiveBudget: domain.Budget{}, Tasks: []domain.OwnerPlanTask{}, Choices: []domain.OwnerChoice{}, CitationRefs: []string{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	run := createRunTest(t, storage, workspace.ID, assigned, domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "manager-review-loop", Steps: []domain.FakeStep{{Kind: domain.ObservationProgress, Message: "working"}}}, "manager-review-run")
	starting, err := storage.MarkRunStarting(context.Background(), run.Run.ID, "manager-review-starting")
	if err != nil {
		t.Fatal(err)
	}
	report, err := storage.SubmitRunReport(context.Background(), CreateRunReportCommand{
		RunID: starting.ID, Kind: domain.ObservationProgress, Message: "The implementation boundary is understood.",
		Payload: map[string]any{"next": "wire the first slice"}, IdempotencyKey: "manager-review-progress",
	})
	if err != nil {
		t.Fatal(err)
	}
	job, found, err := storage.OwnerManagerReview(context.Background(), workspace.ID, project.ID)
	if err != nil || !found || job.Status != "pending" || job.RequestedEventSequence <= conversation.Turn.AsOfEventSequence || job.ConversationID != conversation.Conversation.ID {
		t.Fatalf("OwnerManagerReview(after report) = %#v, %t, %v", job, found, err)
	}
	replayedReport, err := storage.SubmitRunReport(context.Background(), CreateRunReportCommand{
		RunID: starting.ID, Kind: domain.ObservationProgress, Message: "The implementation boundary is understood.",
		Payload: map[string]any{"next": "wire the first slice"}, IdempotencyKey: "manager-review-progress",
	})
	if err != nil || replayedReport.ID != report.ID {
		t.Fatalf("SubmitRunReport(replay) = %#v, %v", replayedReport, err)
	}
	replayedJob, _, err := storage.OwnerManagerReview(context.Background(), workspace.ID, project.ID)
	if err != nil || replayedJob.RequestedEventSequence != job.RequestedEventSequence {
		t.Fatalf("review cursor advanced on report replay: %#v, %v", replayedJob, err)
	}
	claimed, found, err := storage.ClaimOwnerManagerReview(context.Background(), time.Minute)
	if err != nil || !found || claimed.Status != "leased" || claimed.ProjectID != project.ID {
		t.Fatalf("ClaimOwnerManagerReview() = %#v, %t, %v", claimed, found, err)
	}
	snapshot, err := storage.BuildOwnerInterpretationSnapshot(context.Background(), workspace.ID, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := snapshot.Citations["report:"+report.ID]; !exists {
		t.Fatalf("manager snapshot omitted triggering report %s", report.ID)
	}
	if err := storage.AdvanceOwnerManagerReviewCut(context.Background(), project.ID, snapshot.EventSequence); err != nil {
		t.Fatal(err)
	}
	review, err := storage.PrepareOwnerTurn(context.Background(), PrepareOwnerTurnCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ConversationID: conversation.Conversation.ID,
		Instruction: "Review worker activity", Kind: "review", InitiatedBy: "executive",
		TriggerEventSequence: snapshot.EventSequence, ExpectedEventSequence: snapshot.EventSequence,
		IdempotencyKey: "manager-review:" + project.ID + ":first",
		Citations:      []domain.OwnerCitation{snapshot.Citations["report:"+report.ID]},
		Interpretation: domain.OwnerInterpretation{Disposition: "clarify", Summary: "One architecture choice needs the owner.", Question: "Should the first slice keep the existing boundary?", Choices: []domain.OwnerChoice{{Key: "keep", Label: "Keep it", Description: "Retain the current boundary.", Recommended: true}, {Key: "change", Label: "Change it", Description: "Rework the boundary first."}}, ObjectiveBudget: domain.Budget{}, Tasks: []domain.OwnerPlanTask{}, CitationRefs: []string{"report:" + report.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if review.Turn.InitiatedBy != "executive" || review.Turn.TriggerEventSequence != snapshot.EventSequence || review.Turn.Interpretation.Disposition != "clarify" || len(review.Operations) != 0 {
		t.Fatalf("proactive review turn = %#v", review)
	}
	replayedReview, found, err := storage.OwnerTurnReplay(context.Background(), PrepareOwnerTurnCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ConversationID: conversation.Conversation.ID,
		Instruction: "Review worker activity", Kind: "review", InitiatedBy: "executive",
		TriggerEventSequence: snapshot.EventSequence,
		IdempotencyKey:       "manager-review:" + project.ID + ":first",
	})
	if err != nil || !found || replayedReview.Turn.ID != review.Turn.ID {
		t.Fatalf("OwnerTurnReplay(executive crash boundary) = %#v, %t, %v", replayedReview, found, err)
	}
	lateReport, err := storage.SubmitRunReport(context.Background(), CreateRunReportCommand{
		RunID: starting.ID, Kind: domain.ObservationProgress, Message: "A newer worker update arrived after the frozen review cut.",
		Payload: map[string]any{"next": "include it in one follow-up pass"}, IdempotencyKey: "manager-review-late-progress",
	})
	if err != nil {
		t.Fatal(err)
	}
	lateQueued, found, err := storage.OwnerManagerReview(context.Background(), workspace.ID, project.ID)
	if err != nil || !found || lateQueued.Status != "leased" || lateQueued.RequestedEventSequence <= snapshot.EventSequence {
		t.Fatalf("OwnerManagerReview(late worker event) = %#v, %t, %v", lateQueued, found, err)
	}
	lateEventSequence := lateQueued.RequestedEventSequence
	if err := storage.CompleteOwnerManagerReview(context.Background(), project.ID, snapshot.EventSequence, review.Turn.ID); err != nil {
		t.Fatal(err)
	}
	pendingFollowup, found, err := storage.OwnerManagerReview(context.Background(), workspace.ID, project.ID)
	if err != nil || !found || pendingFollowup.Status != "pending" || pendingFollowup.ReviewedEventSequence != snapshot.EventSequence || pendingFollowup.RequestedEventSequence != lateEventSequence {
		t.Fatalf("OwnerManagerReview(coalesced follow-up) = %#v, %t, %v", pendingFollowup, found, err)
	}
	followupClaim, found, err := storage.ClaimOwnerManagerReview(context.Background(), time.Minute)
	if err != nil || !found || followupClaim.RequestedEventSequence != lateEventSequence {
		t.Fatalf("ClaimOwnerManagerReview(follow-up) = %#v, %t, %v", followupClaim, found, err)
	}
	followupSnapshot, err := storage.BuildOwnerInterpretationSnapshot(context.Background(), workspace.ID, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.AdvanceOwnerManagerReviewCut(context.Background(), project.ID, followupSnapshot.EventSequence); err != nil {
		t.Fatal(err)
	}
	followupReview, err := storage.PrepareOwnerTurn(context.Background(), PrepareOwnerTurnCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ConversationID: conversation.Conversation.ID,
		Instruction: "Review newer worker activity", Kind: "review", InitiatedBy: "executive",
		TriggerEventSequence: followupSnapshot.EventSequence, ExpectedEventSequence: followupSnapshot.EventSequence,
		IdempotencyKey: "manager-review:" + project.ID + ":follow-up",
		Citations:      []domain.OwnerCitation{followupSnapshot.Citations["report:"+lateReport.ID]},
		Interpretation: domain.OwnerInterpretation{Disposition: "answer", Summary: "Follow-up reviewed.", Answer: "The newer worker update is accounted for.", ObjectiveBudget: domain.Budget{}, Tasks: []domain.OwnerPlanTask{}, Choices: []domain.OwnerChoice{}, CitationRefs: []string{"report:" + lateReport.ID}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.CompleteOwnerManagerReview(context.Background(), project.ID, followupSnapshot.EventSequence, followupReview.Turn.ID); err != nil {
		t.Fatal(err)
	}
	settled, found, err := storage.OwnerManagerReview(context.Background(), workspace.ID, project.ID)
	if err != nil || !found || settled.Status != "idle" || settled.ReviewedEventSequence != followupSnapshot.EventSequence || settled.LastTurnID != followupReview.Turn.ID {
		t.Fatalf("OwnerManagerReview(settled) = %#v, %t, %v", settled, found, err)
	}
	page, err := storage.ListOwnerConversation(context.Background(), workspace.ID, project.ID, conversation.Conversation.ID)
	if err != nil || page.Review == nil || len(page.Turns) != 3 || page.Turns[1].Turn.ID != review.Turn.ID || page.Turns[2].Turn.ID != followupReview.Turn.ID {
		t.Fatalf("ListOwnerConversation(after proactive review) = %#v, %v", page, err)
	}
	reviewer, err := storage.CreateAgent(context.Background(), CreateAgentCommand{
		WorkspaceIdentifier: workspace.ID, Name: "reviewer", Role: "review", Provider: "fake", Runtime: "fake", MaxConcurrency: 1,
		IdempotencyKey: "manager-review-recipient", CorrelationID: "manager-review-recipient",
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := storage.SendMessage(context.Background(), SendMessageCommand{
		WorkspaceIdentifier: workspace.ID, SenderRunID: starting.ID, RecipientAgent: reviewer.Value.ID,
		Kind: domain.MessageQuestion, Subject: "Boundary review", Body: "Please verify the implementation boundary.",
		IdempotencyKey: "manager-review-worker-message", CorrelationID: "manager-review-worker-message",
	})
	if err != nil {
		t.Fatal(err)
	}
	messageJob, found, err := storage.OwnerManagerReview(context.Background(), workspace.ID, project.ID)
	if err != nil || !found || messageJob.Status != "pending" || messageJob.RequestedEventSequence != message.EventSequence || messageJob.ReviewedEventSequence != settled.ReviewedEventSequence {
		t.Fatalf("OwnerManagerReview(after agent message) = %#v, %t, %v", messageJob, found, err)
	}
	messageReplay, err := storage.SendMessage(context.Background(), SendMessageCommand{
		WorkspaceIdentifier: workspace.ID, SenderRunID: starting.ID, RecipientAgent: reviewer.Value.ID,
		Kind: domain.MessageQuestion, Subject: "Boundary review", Body: "Please verify the implementation boundary.",
		IdempotencyKey: "manager-review-worker-message", CorrelationID: "manager-review-worker-message",
	})
	if err != nil || messageReplay.Value.Message.ID != message.Value.Message.ID {
		t.Fatalf("SendMessage(replay) = %#v, %v", messageReplay, err)
	}
	messageClaim, found, err := storage.ClaimOwnerManagerReview(context.Background(), time.Minute)
	if err != nil || !found || messageClaim.RequestedEventSequence != message.EventSequence {
		t.Fatalf("ClaimOwnerManagerReview(message) = %#v, %t, %v", messageClaim, found, err)
	}
	if err := storage.RecoverOwnerManagerReviewLeases(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered, found, err := storage.OwnerManagerReview(context.Background(), workspace.ID, project.ID)
	if err != nil || !found || recovered.Status != "pending" || recovered.LeaseExpiresAt != "" || recovered.Attempts != messageClaim.Attempts {
		t.Fatalf("RecoverOwnerManagerReviewLeases() = %#v, %t, %v", recovered, found, err)
	}
	messageClaim, found, err = storage.ClaimOwnerManagerReview(context.Background(), time.Minute)
	if err != nil || !found || messageClaim.Status != "leased" || messageClaim.Attempts != recovered.Attempts+1 {
		t.Fatalf("ClaimOwnerManagerReview(recovered) = %#v, %t, %v", messageClaim, found, err)
	}
	if err := storage.FailOwnerManagerReview(context.Background(), project.ID, messageClaim.RequestedEventSequence, context.DeadlineExceeded); err != nil {
		t.Fatal(err)
	}
	failed, found, err := storage.OwnerManagerReview(context.Background(), workspace.ID, project.ID)
	if err != nil || !found || failed.Status != "failed" || failed.LastError == "" {
		t.Fatalf("OwnerManagerReview(failed) = %#v, %t, %v", failed, found, err)
	}
	if profile.Value.ID == "" {
		t.Fatal("launch profile fixture was not created")
	}
	integrity, err := storage.VerifyCanonical(context.Background(), CanonicalVerifyOptions{Full: true})
	if err != nil || integrity.Status != "ok" {
		t.Fatalf("VerifyCanonical() = %#v, %v", integrity, err)
	}
}
