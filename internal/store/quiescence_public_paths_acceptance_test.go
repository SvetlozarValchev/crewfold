package store

import (
	"context"
	"testing"

	"crewfold/internal/domain"
)

func TestM20EveryRemainingActionableQueueIsPubliclyCreatedAndBlocksQuiescence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	fixture := newEscalationAcceptanceFixture(t, "m20-bkp02-public-queues")

	openTarget := fixture.createTarget(t, domain.ProposalResponseReassignTask, "m20-bkp02-open-approval")
	_, openAction, openApproval := fixture.acceptEscalation(t, openTarget.request, "m20-bkp02-open-approval")

	intentTarget := fixture.createTarget(t, domain.ProposalResponseReassignTask, "m20-bkp02-open-intent")
	_, _, intentApproval := fixture.acceptEscalation(t, intentTarget.request, "m20-bkp02-open-intent")
	if _, err := fixture.storage.AllowApproval(ctx, DecideApprovalCommand{
		WorkspaceIdentifier: fixture.base.workspace.ID,
		ApprovalRequestID:   intentApproval.ID,
		ExpectedRevision:    intentApproval.Revision,
		DecisionNote:        "Create the exact approved scheduling intent fixture.",
		IdempotencyKey:      "m20-bkp02-open-intent-allow",
		CorrelationID:       "request-m20-bkp02-open-intent-allow",
	}); err != nil {
		t.Fatalf("AllowApproval(open scheduling intent) = %v", err)
	}

	message, err := fixture.storage.SendMessage(ctx, SendMessageCommand{
		WorkspaceIdentifier: fixture.base.workspace.ID,
		RecipientAgent:      fixture.base.manager.ID,
		Kind:                domain.MessageInform,
		Subject:             "Public wake fixture",
		Body:                "Wake the exact starting manager run through the durable queue.",
		IdempotencyKey:      "m20-bkp02-open-wake",
		CorrelationID:       "request-m20-bkp02-open-wake",
	})
	if err != nil || message.Value.Recipient.WakeStatus != domain.WakePending {
		t.Fatalf("SendMessage(open wake) = %#v, %v; want one pending durable wake", message, err)
	}

	before, err := fixture.storage.ListEvents(ctx, ListEventsQuery{
		WorkspaceIdentifier: fixture.base.workspace.ID,
		Limit:               1,
	})
	if err != nil {
		t.Fatalf("ListEvents(before quiescence reads) = %v", err)
	}
	cut, err := fixture.storage.CheckQuiescentCut(ctx)
	if err != nil {
		t.Fatalf("CheckQuiescentCut(public queues) = %v", err)
	}
	if cut.Quiescent || cut.EventHighWater != before.HighWater ||
		cut.Counts.OpenWakeJobs != 1 || cut.Counts.OpenSchedulingIntents != 1 ||
		cut.Counts.OpenSupervisorActions != 1 || cut.Counts.OpenApprovals != 1 {
		t.Fatalf("public queue quiescence cut = %#v; want exact 1/1/1/1 actionable blockers at event %d", cut, before.HighWater)
	}

	report, err := fixture.storage.VerifyCanonical(ctx, CanonicalVerifyOptions{Full: true})
	if err != nil || !report.Complete || report.Status != "ok" || report.EventHighWater != before.HighWater {
		t.Fatalf("VerifyCanonical(public queues) = %#v, %v", report, err)
	}
	wantQueues := map[string]struct {
		rows     int64
		open     int64
		terminal int64
	}{
		"message_wake":      {rows: 1, open: 1},
		"scheduling_intent": {rows: 1, open: 1},
		"supervisor_action": {rows: 2, open: 1, terminal: 1},
		"approval":          {rows: 2, open: 1, terminal: 1},
	}
	for _, queue := range report.DurableQueues {
		want, ok := wantQueues[queue.Name]
		if !ok {
			continue
		}
		if queue.RowCount != want.rows || queue.OpenCount != want.open || queue.TerminalCount != want.terminal || queue.ViolationCount != 0 || queue.Status != "ok" {
			t.Fatalf("durable queue %q = %#v; want rows/open/terminal %d/%d/%d and exact valid partition", queue.Name, queue, want.rows, want.open, want.terminal)
		}
		delete(wantQueues, queue.Name)
	}
	if len(wantQueues) != 0 {
		t.Fatalf("canonical durable queue report omitted public fixtures: %#v", wantQueues)
	}

	kindCounts := map[string]int{}
	knownBlockers := map[string]string{}
	for _, blocker := range report.QuiescenceBlockers {
		kindCounts[blocker.Kind]++
		knownBlockers[blocker.Kind] = blocker.EntityID
	}
	if len(report.QuiescenceBlockers) > maximumQuiescenceBlockerSamples ||
		kindCounts["open_wake_job"] != 1 || kindCounts["open_scheduling_intent"] != 1 ||
		kindCounts["open_supervisor_action"] != 1 || kindCounts["open_approval"] != 1 ||
		knownBlockers["open_supervisor_action"] != openAction.ID || knownBlockers["open_approval"] != openApproval.ID {
		t.Fatalf("bounded public queue blockers = %#v", report.QuiescenceBlockers)
	}

	after, err := fixture.storage.ListEvents(ctx, ListEventsQuery{
		WorkspaceIdentifier: fixture.base.workspace.ID,
		Limit:               1,
	})
	if err != nil || after.HighWater != before.HighWater {
		t.Fatalf("quiescence/full verification changed event high-water from %d to %d: %v", before.HighWater, after.HighWater, err)
	}
}
