package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"crewfold/internal/domain"
)

func TestCuratorApplySafeDerivesAndAcceptsNewSafeCandidatesInSamePass(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	insertRawCuratorMeetingSources(t, storage, workspace.ID, project.ID, maximumCuratorAcceptances+1, false)
	if _, err := storage.ConfigureCuratorRule(context.Background(), ConfigureCuratorRuleCommand{
		WorkspaceIdentifier: workspace.ID, RuleName: domain.CuratorRuleAcceptedMeetingResolutionCopy,
		Enabled: true, ExpectedRevision: 1, IdempotencyKey: "same-pass-enable", CorrelationID: "request-same-pass-enable",
	}); err != nil {
		t.Fatalf("ConfigureCuratorRule() error = %v", err)
	}
	command := ProcessCuratorCommand{WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ApplySafe: true,
		IdempotencyKey: "same-pass-process", CorrelationID: "request-same-pass-process"}
	result, err := storage.ProcessCurator(context.Background(), command)
	if err != nil || result.Process.CandidatesScanned != maximumCuratorAcceptances+1 ||
		len(result.Process.Derived) != maximumCuratorAcceptances+1 || len(result.Process.Accepted) != maximumCuratorAcceptances {
		t.Fatalf("ProcessCurator(same pass) = %#v, %v", result, err)
	}
	for index, accepted := range result.Process.Accepted {
		if accepted.KnowledgeRevisionID != result.Process.Derived[index].KnowledgeRevisionID ||
			accepted.DerivationID != result.Process.Derived[index].ID {
			t.Fatalf("accepted[%d] = %#v, derived = %#v", index, accepted, result.Process.Derived[index])
		}
	}
	replayed, err := storage.ProcessCurator(context.Background(), command)
	if err != nil || !reflect.DeepEqual(replayed, result) {
		t.Fatalf("ProcessCurator(replay) = %#v, %v; want %#v", replayed, err, result)
	}
	remaining, err := storage.ProcessCurator(context.Background(), ProcessCuratorCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ApplySafe: true,
		IdempotencyKey: "same-pass-process-remaining", CorrelationID: "request-same-pass-process-remaining",
	})
	if err != nil || remaining.Process.CandidatesScanned != 1 || len(remaining.Process.Derived) != 0 ||
		len(remaining.Process.Accepted) != 1 ||
		remaining.Process.Accepted[0].KnowledgeRevisionID != result.Process.Derived[maximumCuratorAcceptances].KnowledgeRevisionID {
		t.Fatalf("ProcessCurator(remaining) = %#v, %v", remaining, err)
	}
}

func TestCuratorDerivesWhileDisabledThenAcceptsSameRevisionAfterEnable(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	setup, proposal := createAcceptedCuratorMeeting(t, storage)

	deriveCommand := ProcessCuratorCommand{WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID,
		IdempotencyKey: "curator-derive-disabled", CorrelationID: "request-curator-derive-disabled"}
	derived, err := storage.ProcessCurator(context.Background(), deriveCommand)
	if err != nil || derived.Process.CandidatesScanned != 1 || len(derived.Process.Derived) != 1 || len(derived.Process.Accepted) != 0 {
		t.Fatalf("ProcessCurator(disabled) = %#v, %v", derived, err)
	}
	derivation := derived.Process.Derived[0]
	if derivation.SourceID != proposal.ID || derivation.SourceRevision != proposal.Revision || derivation.RuleRevision != 1 {
		t.Fatalf("derivation = %#v, want exact accepted proposal under default rule", derivation)
	}
	queued, err := storage.QueueCuratorRevisions(context.Background(), CuratorQueueQuery{
		WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID,
	})
	if err != nil || len(queued.Entries) != 1 || queued.Entries[0].Revision.ID != derivation.KnowledgeRevisionID ||
		queued.Entries[0].Eligibility != domain.CuratorEligibilityManual ||
		queued.Entries[0].EligibilityReason != domain.CuratorEligibilityReasonRuleDisabled {
		t.Fatalf("QueueCuratorRevisions(disabled) = %#v, %v", queued, err)
	}
	proposalRevisionID := queued.Entries[0].Revision.ID

	enabled, err := storage.ConfigureCuratorRule(context.Background(), ConfigureCuratorRuleCommand{
		WorkspaceIdentifier: setup.workspace.ID, RuleName: domain.CuratorRuleAcceptedMeetingResolutionCopy,
		Enabled: true, ExpectedRevision: 1, IdempotencyKey: "enable-curator-rule", CorrelationID: "request-enable-curator-rule",
	})
	if err != nil || !enabled.Rule.Enabled || enabled.Rule.Revision != 2 {
		t.Fatalf("ConfigureCuratorRule(enable) = %#v, %v", enabled, err)
	}
	queued, err = storage.QueueCuratorRevisions(context.Background(), CuratorQueueQuery{
		WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID,
	})
	if err != nil || len(queued.Entries) != 1 || queued.Entries[0].Eligibility != domain.CuratorEligibilitySafe {
		t.Fatalf("QueueCuratorRevisions(enabled) = %#v, %v", queued, err)
	}

	accepted, err := storage.ProcessCurator(context.Background(), ProcessCuratorCommand{
		WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID, ApplySafe: true,
		IdempotencyKey: "curator-accept-safe", CorrelationID: "request-curator-accept-safe",
	})
	if err != nil || len(accepted.Process.Derived) != 0 || len(accepted.Process.Accepted) != 1 ||
		accepted.Process.Accepted[0].KnowledgeRevisionID != proposalRevisionID ||
		accepted.Process.Accepted[0].RuleRevision != 2 || accepted.Process.Accepted[0].DerivationID != derivation.ID {
		t.Fatalf("ProcessCurator(enabled) = %#v, %v", accepted, err)
	}
	revision, err := storage.KnowledgeRevision(context.Background(), setup.workspace.ID, proposalRevisionID)
	if err != nil || revision.ReviewStatus != domain.KnowledgeReviewAccepted || revision.CurrencyStatus != domain.KnowledgeCurrencyCurrent ||
		revision.Title != acceptedMeetingAgenda(t, storage, setup.workspace.ID, proposal.MeetingID) || revision.Body != proposal.Summary ||
		revision.Confidence != domain.KnowledgeConfidenceMedium || revision.VerificationStatus != domain.KnowledgeVerificationSupported ||
		revision.FreshnessPolicy != domain.KnowledgeFreshUntilSuperseded || revision.TaskScopeID != "" || len(revision.Sources) != 1 ||
		revision.Sources[0].Type != domain.KnowledgeSourceMeetingProposal || revision.Sources[0].ID != proposal.ID ||
		revision.Sources[0].Revision != proposal.Revision || revision.Sources[0].Role != domain.KnowledgeSourcePrimary {
		t.Fatalf("accepted curator revision = %#v, %v", revision, err)
	}
	checks, err := storage.ListKnowledgeAuthorityChecks(context.Background(), setup.workspace.ID, proposalRevisionID)
	if err != nil || len(checks) != 1 || checks[0].Actor != curatorActor || checks[0].Outcome != domain.KnowledgeAuthorityAllowed ||
		checks[0].Reason != domain.KnowledgeAuthorityReasonStatePolicy || checks[0].EventSequence != accepted.Process.Accepted[0].KnowledgeEventSequence {
		t.Fatalf("curator authority checks = %#v, %v", checks, err)
	}
	queued, err = storage.QueueCuratorRevisions(context.Background(), CuratorQueueQuery{
		WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID,
	})
	if err != nil || len(queued.Entries) != 0 {
		t.Fatalf("QueueCuratorRevisions(after acceptance) = %#v, %v", queued, err)
	}
}

func TestCuratorApplySafeFalseNeverAcceptsAndHashDistinguishesApply(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	setup, _ := createAcceptedCuratorMeeting(t, storage)
	if _, err := storage.ConfigureCuratorRule(context.Background(), ConfigureCuratorRuleCommand{
		WorkspaceIdentifier: setup.workspace.ID, RuleName: domain.CuratorRuleAcceptedMeetingResolutionCopy,
		Enabled: true, ExpectedRevision: 1, IdempotencyKey: "enable-derive-only", CorrelationID: "request-enable-derive-only",
	}); err != nil {
		t.Fatalf("ConfigureCuratorRule() error = %v", err)
	}
	command := ProcessCuratorCommand{WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID,
		ApplySafe: false, IdempotencyKey: "derive-only-key", CorrelationID: "request-derive-only"}
	result, err := storage.ProcessCurator(context.Background(), command)
	if err != nil || len(result.Process.Derived) != 1 || len(result.Process.Accepted) != 0 {
		t.Fatalf("ProcessCurator(apply_safe=false) = %#v, %v", result, err)
	}
	command.ApplySafe = true
	if _, err := storage.ProcessCurator(context.Background(), command); ErrorCode(err) != CodeIdempotencyConflict {
		t.Fatalf("ProcessCurator(changed apply_safe) error = %v, code = %q", err, ErrorCode(err))
	}
}

func TestCuratorSkipsUnsafeExactCopiesWithoutTruncation(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	setup, proposal := createAcceptedCuratorMeeting(t, storage)
	if _, err := storage.db.Exec("UPDATE meeting_proposals SET summary = ? WHERE id = ?", strings.Repeat("x", maximumCuratorSummaryBytes+1), proposal.ID); err != nil {
		t.Fatalf("make accepted proposal unsafe: %v", err)
	}
	result, err := storage.ProcessCurator(context.Background(), ProcessCuratorCommand{
		WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID,
		IdempotencyKey: "skip-unsafe-curator", CorrelationID: "request-skip-unsafe-curator",
	})
	if err != nil || result.Process.CandidatesScanned != 1 || len(result.Process.Derived) != 0 || len(result.Process.Accepted) != 0 ||
		len(result.Process.Skipped) != 1 || result.Process.Skipped[0].SourceID != proposal.ID ||
		result.Process.Skipped[0].Reason != domain.CuratorSkipSummaryInvalid {
		t.Fatalf("ProcessCurator(unsafe) = %#v, %v", result, err)
	}
	var derivations, knowledge int
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM curator_derivations").Scan(&derivations); err != nil {
		t.Fatalf("count derivations: %v", err)
	}
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM knowledge_revisions WHERE proposed_by = ?", curatorActor.ID).Scan(&knowledge); err != nil {
		t.Fatalf("count curator knowledge: %v", err)
	}
	if derivations != 0 || knowledge != 0 {
		t.Fatalf("unsafe source persisted derivations=%d knowledge=%d", derivations, knowledge)
	}
}

func TestCuratorExactCopyByteBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		agenda      string
		summary     string
		wantDerived bool
		wantSkip    string
	}{
		{name: "agenda 160 ascii", agenda: strings.Repeat("a", 160), summary: "valid summary", wantDerived: true},
		{name: "agenda 161 ascii", agenda: strings.Repeat("a", 161), summary: "valid summary", wantSkip: domain.CuratorSkipAgendaInvalid},
		{name: "agenda 160 multibyte bytes", agenda: strings.Repeat("é", 80), summary: "valid summary", wantDerived: true},
		{name: "agenda 162 multibyte bytes", agenda: strings.Repeat("é", 81), summary: "valid summary", wantSkip: domain.CuratorSkipAgendaInvalid},
		{name: "summary 2048 ascii", agenda: "valid agenda", summary: strings.Repeat("s", 2048), wantDerived: true},
		{name: "summary 2049 ascii", agenda: "valid agenda", summary: strings.Repeat("s", 2049), wantSkip: domain.CuratorSkipSummaryInvalid},
		{name: "summary 2048 multibyte bytes", agenda: "valid agenda", summary: strings.Repeat("界", 682) + "aa", wantDerived: true},
		{name: "summary 2051 multibyte bytes", agenda: "valid agenda", summary: strings.Repeat("界", 683) + "aa", wantSkip: domain.CuratorSkipSummaryInvalid},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			storage := openTestStore(t, t.TempDir(), Options{})
			setup, proposal := createAcceptedCuratorMeeting(t, storage)
			if _, err := storage.db.Exec("UPDATE meetings SET agenda = ? WHERE id = ?", test.agenda, proposal.MeetingID); err != nil {
				t.Fatalf("set agenda: %v", err)
			}
			if _, err := storage.db.Exec("UPDATE meeting_proposals SET summary = ? WHERE id = ?", test.summary, proposal.ID); err != nil {
				t.Fatalf("set summary: %v", err)
			}
			result, err := storage.ProcessCurator(context.Background(), ProcessCuratorCommand{
				WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID,
				IdempotencyKey: "copy-boundary", CorrelationID: "request-copy-boundary",
			})
			if err != nil {
				t.Fatalf("ProcessCurator() error = %v", err)
			}
			if test.wantDerived {
				if len(result.Process.Derived) != 1 || len(result.Process.Skipped) != 0 {
					t.Fatalf("ProcessCurator() = %#v, want exact derivation", result)
				}
				revision, err := storage.KnowledgeRevision(context.Background(), setup.workspace.ID, result.Process.Derived[0].KnowledgeRevisionID)
				if err != nil || revision.Title != test.agenda || revision.Body != test.summary {
					t.Fatalf("exact boundary copy = %#v, %v", revision, err)
				}
			} else if len(result.Process.Derived) != 0 || len(result.Process.Skipped) != 1 || result.Process.Skipped[0].Reason != test.wantSkip {
				t.Fatalf("ProcessCurator() = %#v, want skip %q", result, test.wantSkip)
			}
		})
	}
}

func TestCuratorExactCopyRejectsEmptyNullAndInvalidUTF8(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		agenda  string
		summary string
		want    string
	}{
		{name: "empty agenda", agenda: "", summary: "valid", want: domain.CuratorSkipAgendaInvalid},
		{name: "null agenda", agenda: "agenda\x00suffix", summary: "valid", want: domain.CuratorSkipAgendaInvalid},
		{name: "invalid agenda utf8", agenda: string([]byte{0xff}), summary: "valid", want: domain.CuratorSkipAgendaInvalid},
		{name: "empty summary", agenda: "valid", summary: "", want: domain.CuratorSkipSummaryInvalid},
		{name: "null summary", agenda: "valid", summary: "summary\x00suffix", want: domain.CuratorSkipSummaryInvalid},
		{name: "invalid summary utf8", agenda: "valid", summary: string([]byte{0xff}), want: domain.CuratorSkipSummaryInvalid},
	}
	for _, test := range tests {
		if got := curatorSourceSkipReason(test.agenda, test.summary); got != test.want {
			t.Errorf("%s: curatorSourceSkipReason() = %q, want %q", test.name, got, test.want)
		}
	}
}

func TestOrdinaryKnowledgeAcceptanceDoesNotGrantCuratorSubsystemAuthority(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	task := createWorkTestTask(t, storage, workspace.ID, project.ID, "curator authority boundary", "curator-authority-task")
	proposal := proposeTaskKnowledge(t, storage, workspace.ID, task.Task.ID, "curator-authority-proposal",
		"Owner boundary", "The ordinary knowledge path cannot grant a subsystem authority", "")
	result, err := storage.AcceptKnowledge(context.Background(), AcceptKnowledgeCommand{
		WorkspaceIdentifier: workspace.ID, RevisionID: proposal.Revision.ID,
		ExpectedStateRevision: proposal.Revision.StateRevision, Actor: curatorActor,
		IdempotencyKey: "ordinary-curator-accept", CorrelationID: "request-ordinary-curator-accept",
	})
	if ErrorCode(err) != CodeKnowledgeDenied || result.AuthorityCheck == nil ||
		result.AuthorityCheck.Outcome != domain.KnowledgeAuthorityDenied || result.AuthorityCheck.Reason != domain.KnowledgeAuthorityReasonNotOwner {
		t.Fatalf("AcceptKnowledge(curator actor) = %#v, %v", result, err)
	}
	current, err := storage.KnowledgeRevision(context.Background(), workspace.ID, proposal.Revision.ID)
	if err != nil || current.ReviewStatus != domain.KnowledgeReviewProposed || current.StateRevision != proposal.Revision.StateRevision {
		t.Fatalf("proposal after denied ordinary acceptance = %#v, %v", current, err)
	}
}

func TestCuratorProcessRollbackAndRetryIsAtomic(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{MutationAfterProjection, MutationAfterEvent} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			storage := openTestStore(t, t.TempDir(), Options{})
			setup, _ := createAcceptedCuratorMeeting(t, storage)
			if _, err := storage.ConfigureCuratorRule(context.Background(), ConfigureCuratorRuleCommand{
				WorkspaceIdentifier: setup.workspace.ID, RuleName: domain.CuratorRuleAcceptedMeetingResolutionCopy,
				Enabled: true, ExpectedRevision: 1, IdempotencyKey: "curator-rollback-enable-" + stage,
				CorrelationID: "request-curator-rollback-enable-" + stage,
			}); err != nil {
				t.Fatalf("ConfigureCuratorRule() error = %v", err)
			}
			injected := errors.New("curator interruption")
			storage.mutationHook = func(current string) error {
				if current == stage {
					return injected
				}
				return nil
			}
			command := ProcessCuratorCommand{WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID,
				ApplySafe: true, IdempotencyKey: "curator-rollback-" + stage, CorrelationID: "request-curator-rollback-" + stage}
			if _, err := storage.ProcessCurator(context.Background(), command); !errors.Is(err, injected) {
				t.Fatalf("ProcessCurator(injected) error = %v", err)
			}
			storage.mutationHook = nil
			for _, table := range []string{"curator_derivations", "curator_auto_acceptances"} {
				var count int
				if err := storage.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("%s count after rollback = %d, %v", table, count, err)
				}
			}
			var knowledge, idempotency int
			if err := storage.db.QueryRow("SELECT COUNT(*) FROM knowledge_revisions WHERE proposed_by = ?", curatorActor.ID).Scan(&knowledge); err != nil || knowledge != 0 {
				t.Fatalf("curator knowledge after rollback = %d, %v", knowledge, err)
			}
			if err := storage.db.QueryRow("SELECT COUNT(*) FROM idempotency_keys WHERE key = ?", command.IdempotencyKey).Scan(&idempotency); err != nil || idempotency != 0 {
				t.Fatalf("curator idempotency after rollback = %d, %v", idempotency, err)
			}
			result, err := storage.ProcessCurator(context.Background(), command)
			if err != nil || len(result.Process.Derived) != 1 || len(result.Process.Accepted) != 1 ||
				result.Process.Accepted[0].KnowledgeRevisionID != result.Process.Derived[0].KnowledgeRevisionID {
				t.Fatalf("ProcessCurator(retry) = %#v, %v", result, err)
			}
		})
	}
}

func TestCuratorAutoAcceptanceRollbackAndRetryIsAtomic(t *testing.T) {
	t.Parallel()
	for _, stage := range []string{MutationAfterProjection, MutationAfterEvent} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			storage := openTestStore(t, t.TempDir(), Options{})
			setup, _ := createAcceptedCuratorMeeting(t, storage)
			derived, err := storage.ProcessCurator(context.Background(), ProcessCuratorCommand{
				WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID,
				IdempotencyKey: "auto-rollback-derive-" + stage, CorrelationID: "request-auto-rollback-derive-" + stage,
			})
			if err != nil || len(derived.Process.Derived) != 1 {
				t.Fatalf("ProcessCurator(derive) = %#v, %v", derived, err)
			}
			revisionID := derived.Process.Derived[0].KnowledgeRevisionID
			if _, err := storage.ConfigureCuratorRule(context.Background(), ConfigureCuratorRuleCommand{
				WorkspaceIdentifier: setup.workspace.ID, RuleName: domain.CuratorRuleAcceptedMeetingResolutionCopy,
				Enabled: true, ExpectedRevision: 1, IdempotencyKey: "auto-rollback-enable-" + stage,
				CorrelationID: "request-auto-rollback-enable-" + stage,
			}); err != nil {
				t.Fatalf("ConfigureCuratorRule() error = %v", err)
			}
			var eventsBefore, checksBefore int
			if err := storage.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&eventsBefore); err != nil {
				t.Fatalf("count events: %v", err)
			}
			if err := storage.db.QueryRow("SELECT COUNT(*) FROM knowledge_authority_checks").Scan(&checksBefore); err != nil {
				t.Fatalf("count authority checks: %v", err)
			}
			injected := errors.New("curator auto-accept interruption")
			storage.mutationHook = func(current string) error {
				if current == stage {
					return injected
				}
				return nil
			}
			command := ProcessCuratorCommand{WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID,
				ApplySafe: true, IdempotencyKey: "auto-rollback-process-" + stage,
				CorrelationID: "request-auto-rollback-process-" + stage}
			if _, err := storage.ProcessCurator(context.Background(), command); !errors.Is(err, injected) {
				t.Fatalf("ProcessCurator(auto injected) error = %v", err)
			}
			storage.mutationHook = nil
			revision, err := storage.KnowledgeRevision(context.Background(), setup.workspace.ID, revisionID)
			if err != nil || revision.ReviewStatus != domain.KnowledgeReviewProposed || revision.StateRevision != 1 {
				t.Fatalf("revision after auto rollback = %#v, %v", revision, err)
			}
			var eventsAfter, checksAfter, autoAfter, idempotencyAfter int
			if err := storage.db.QueryRow("SELECT COUNT(*) FROM events").Scan(&eventsAfter); err != nil {
				t.Fatalf("count events after: %v", err)
			}
			if err := storage.db.QueryRow("SELECT COUNT(*) FROM knowledge_authority_checks").Scan(&checksAfter); err != nil {
				t.Fatalf("count authority checks after: %v", err)
			}
			if err := storage.db.QueryRow("SELECT COUNT(*) FROM curator_auto_acceptances").Scan(&autoAfter); err != nil {
				t.Fatalf("count auto acceptances after: %v", err)
			}
			if err := storage.db.QueryRow("SELECT COUNT(*) FROM idempotency_keys WHERE key = ?", command.IdempotencyKey).Scan(&idempotencyAfter); err != nil {
				t.Fatalf("count idempotency after: %v", err)
			}
			if eventsAfter != eventsBefore || checksAfter != checksBefore || autoAfter != 0 || idempotencyAfter != 0 {
				t.Fatalf("auto rollback leaked events=%d/%d checks=%d/%d auto=%d idempotency=%d", eventsAfter, eventsBefore, checksAfter, checksBefore, autoAfter, idempotencyAfter)
			}
			accepted, err := storage.ProcessCurator(context.Background(), command)
			if err != nil || len(accepted.Process.Accepted) != 1 || accepted.Process.Accepted[0].KnowledgeRevisionID != revisionID {
				t.Fatalf("ProcessCurator(auto retry) = %#v, %v", accepted, err)
			}
		})
	}
}

func TestCuratorDefaultsAndQueueCursorAreWorkspaceScoped(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	first, project := initializeWorkTestProject(t, storage)
	secondResult, err := storage.InitWorkspace(context.Background(), InitWorkspaceCommand{
		Name: "second-curator-workspace", IdempotencyKey: "second-curator-workspace", CorrelationID: "request-second-curator-workspace",
	})
	if err != nil {
		t.Fatalf("InitWorkspace(second) error = %v", err)
	}
	for _, workspaceID := range []string{first.ID, secondResult.Workspace.ID} {
		var revision int64
		var enabled int
		if err := storage.db.QueryRow("SELECT revision, enabled FROM curator_rules WHERE workspace_id = ?", workspaceID).Scan(&revision, &enabled); err != nil || revision != 1 || enabled != 0 {
			t.Fatalf("default curator rule for %s = revision %d enabled %d, %v", workspaceID, revision, enabled, err)
		}
	}

	workspace, _ := initializeCuratorQueueProposals(t, storage, first.ID, project.ID, 2)
	page, err := storage.QueueCuratorRevisions(context.Background(), CuratorQueueQuery{
		WorkspaceIdentifier: first.ID, ProjectIdentifier: project.ID, Limit: 1,
	})
	if err != nil || len(page.Entries) != 1 || page.NextCursor == "" {
		t.Fatalf("QueueCuratorRevisions(first page) = %#v, %v", page, err)
	}
	secondPage, err := storage.QueueCuratorRevisions(context.Background(), CuratorQueueQuery{
		WorkspaceIdentifier: first.ID, ProjectIdentifier: project.ID, Limit: 1, After: page.NextCursor,
	})
	if err != nil || len(secondPage.Entries) != 1 || secondPage.Entries[0].Revision.ID == page.Entries[0].Revision.ID || secondPage.NextCursor != "" {
		t.Fatalf("QueueCuratorRevisions(second page) = %#v, %v", secondPage, err)
	}
	if page.Rule.Revision != 1 || page.Rule.Enabled || page.Rule.ID == "" || secondPage.Rule != page.Rule {
		t.Fatalf("queue rule snapshot = %#v then %#v", page.Rule, secondPage.Rule)
	}
	cursor, err := decodeCuratorQueueCursor(page.NextCursor, first.ID, project.ID)
	if err != nil {
		t.Fatalf("decode queue cursor: %v", err)
	}
	cursor.ProjectID = "prj_000000000000000000000000000000ff"
	wrongScope, err := encodeCuratorQueueCursor(cursor)
	if err != nil {
		t.Fatalf("encode wrong-scope cursor: %v", err)
	}
	if _, err := storage.QueueCuratorRevisions(context.Background(), CuratorQueueQuery{
		WorkspaceIdentifier: first.ID, ProjectIdentifier: project.ID, Limit: 1, After: wrongScope,
	}); ErrorCode(err) != CodeInvalidKnowledge {
		t.Fatalf("cross-project cursor error = %v, code = %q", err, ErrorCode(err))
	}
	if _, err := storage.QueueCuratorRevisions(context.Background(), CuratorQueueQuery{
		WorkspaceIdentifier: first.ID, ProjectIdentifier: project.ID, Limit: 1, After: page.NextCursor + "=",
	}); ErrorCode(err) != CodeInvalidKnowledge {
		t.Fatalf("noncanonical cursor error = %v, code = %q", err, ErrorCode(err))
	}
	if _, err := storage.QueueCuratorRevisions(context.Background(), CuratorQueueQuery{
		WorkspaceIdentifier: secondResult.Workspace.ID, ProjectIdentifier: project.ID, Limit: 1, After: page.NextCursor,
	}); ErrorCode(err) == "" {
		t.Fatal("cross-workspace cursor unexpectedly succeeded")
	}
	_ = workspace
}

func TestCuratorSafeDerivationIsNotStarvedByOlderManualProposals(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	setup := initializeMeetingTest(t, storage, 2)
	initializeCuratorQueueProposals(t, storage, setup.workspace.ID, setup.project.ID, 101)
	source := acceptCuratorMeetingForSetup(t, storage, setup, "starvation")
	if _, err := storage.ConfigureCuratorRule(context.Background(), ConfigureCuratorRuleCommand{
		WorkspaceIdentifier: setup.workspace.ID, RuleName: domain.CuratorRuleAcceptedMeetingResolutionCopy,
		Enabled: true, ExpectedRevision: 1, IdempotencyKey: "starvation-enable", CorrelationID: "request-starvation-enable",
	}); err != nil {
		t.Fatalf("ConfigureCuratorRule() error = %v", err)
	}
	result, err := storage.ProcessCurator(context.Background(), ProcessCuratorCommand{
		WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID, ApplySafe: true,
		IdempotencyKey: "starvation-process", CorrelationID: "request-starvation-process",
	})
	if err != nil || result.Process.CandidatesScanned > maximumCuratorCandidates || len(result.Process.Derived) != 1 ||
		len(result.Process.Accepted) != 1 || result.Process.Derived[0].SourceID != source.ID ||
		result.Process.Accepted[0].KnowledgeRevisionID != result.Process.Derived[0].KnowledgeRevisionID {
		t.Fatalf("ProcessCurator(manual starvation) = %#v, %v; source=%s", result, err, source.ID)
	}
	queue, err := storage.QueueCuratorRevisions(context.Background(), CuratorQueueQuery{
		WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID, Limit: 200,
	})
	if err != nil || len(queue.Entries) != 101 {
		t.Fatalf("manual queue after safe acceptance = %d entries, %v", len(queue.Entries), err)
	}
}

func TestCuratorProcessDerivesAtMostOneHundredStructuredCandidates(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	now := storage.nowText()
	agentID := "agent_000000000000000000000000000000aa"
	taskLowID := "task_00000000000000000000000000000001"
	taskHighID := "task_00000000000000000000000000000002"
	if _, err := storage.db.Exec(`INSERT INTO agents(id,workspace_id,name,role,provider,runtime,enabled,max_concurrency,revision,created_at,updated_at,created_by,updated_by)
VALUES(?,?,?,?,?,?,1,1,1,?,?,?,?)`, agentID, workspace.ID, "curator-bounds-agent", "fixture", "fake", "fake", now, now, localOwnerActorID, localOwnerActorID); err != nil {
		t.Fatalf("insert bounds agent: %v", err)
	}
	for _, taskID := range []string{taskLowID, taskHighID} {
		if _, err := storage.db.Exec(`INSERT INTO tasks(id,workspace_id,project_id,objective_id,title,description,status,blocked_reason,priority,budget_tokens,budget_cost_cents,budget_time_seconds,revision,created_at,updated_at,created_by,updated_by)
VALUES(?,?,?,NULL,?,NULL,'ready',NULL,100,0,0,0,1,?,?,?,?)`, taskID, workspace.ID, project.ID, taskID, now, now, localOwnerActorID, localOwnerActorID); err != nil {
			t.Fatalf("insert bounds task: %v", err)
		}
	}
	for index := 0; index < 101; index++ {
		suffix := fmt.Sprintf("%032x", index+1)
		claimLowID := "claim_" + fmt.Sprintf("%032x", index*2+1)
		claimHighID := "claim_" + fmt.Sprintf("%032x", index*2+2)
		for claimIndex, claimID := range []string{claimLowID, claimHighID} {
			taskID := taskLowID
			if claimIndex == 1 {
				taskID = taskHighID
			}
			if _, err := storage.db.Exec(`INSERT INTO work_claims(id,workspace_id,project_id,task_id,checkout_id,kind,target,mode,conflict_policy,status,baseline_paths_json,lease_expires_at,revision,created_at,updated_at,created_by,updated_by)
VALUES(?,?,?,?,NULL,'path',?,'exclusive','request_resolution','active','[]',?,1,?,?,?,?)`, claimID, workspace.ID, project.ID, taskID, "bounds/"+claimID, "2099-01-01T00:00:00Z", now, now, localOwnerActorID, localOwnerActorID); err != nil {
				t.Fatalf("insert bounds claim: %v", err)
			}
		}
		overlapID := "overlap_" + suffix
		meetingID := "meet_" + suffix
		proposalID := "proposal_" + suffix
		if _, err := storage.db.Exec(`INSERT INTO work_overlaps(id,workspace_id,project_id,claim_low_id,claim_high_id,task_low_id,task_high_id,kind,witness,severity,policy_response,scheduling_paused,resolution_required,status,explanation_json,detected_at,resolved_at,resolution_reason,revision)
VALUES(?,?,?,?,?,?,?,'path','bounds','medium','request_resolution',0,1,'resolved','{}',?,?,?,1)`, overlapID, workspace.ID, project.ID, claimLowID, claimHighID, taskLowID, taskHighID, now, now, "fixture"); err != nil {
			t.Fatalf("insert bounds overlap %d: %v", index, err)
		}
		if _, err := storage.db.Exec(`INSERT INTO meetings(id,workspace_id,project_id,overlap_id,agenda,facilitator_agent_id,policy,reviewer_agent_id,allowed_actions_json,status,frozen_input_json,frozen_input_hash,deadline_at,stalled_reason,revision,created_at,updated_at,created_by,updated_by)
VALUES(?,?,?,?,?,?,'owner_decision',NULL,'[]','concluded','{}',?,? ,NULL,1,?,?,?,?)`, meetingID, workspace.ID, project.ID, overlapID, "Bounds agenda "+suffix, agentID, strings.Repeat("0", 64), "2099-01-01T00:00:00Z", now, now, localOwnerActorID, localOwnerActorID); err != nil {
			t.Fatalf("insert bounds meeting %d: %v", index, err)
		}
		if _, err := storage.db.Exec(`INSERT INTO meeting_proposals(id,meeting_id,proposed_by,summary,status,revision,proposed_at,decided_at,decision_note)
VALUES(?,?,? ,?,'accepted',2,?,?,?)`, proposalID, meetingID, agentID, "Bounds summary "+suffix, now, now, "fixture accepted"); err != nil {
			t.Fatalf("insert bounds proposal %d: %v", index, err)
		}
	}
	first, err := storage.ProcessCurator(context.Background(), ProcessCuratorCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID,
		IdempotencyKey: "structured-bound-first", CorrelationID: "request-structured-bound-first",
	})
	if err != nil || first.Process.CandidatesScanned != 100 || len(first.Process.Derived) != 100 {
		t.Fatalf("ProcessCurator(first bound) scanned=%d derived=%d, %v", first.Process.CandidatesScanned, len(first.Process.Derived), err)
	}
	second, err := storage.ProcessCurator(context.Background(), ProcessCuratorCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID,
		IdempotencyKey: "structured-bound-second", CorrelationID: "request-structured-bound-second",
	})
	if err != nil || second.Process.CandidatesScanned != 1 || len(second.Process.Derived) != 1 {
		t.Fatalf("ProcessCurator(second bound) scanned=%d derived=%d, %v", second.Process.CandidatesScanned, len(second.Process.Derived), err)
	}
}

func TestCuratorValidSourceIsNotStarvedByOneHundredUnsafeSources(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	insertRawCuratorMeetingSources(t, storage, workspace.ID, project.ID, 100, true)
	insertRawCuratorMeetingSources(t, storage, workspace.ID, project.ID, 1, false)
	result, err := storage.ProcessCurator(context.Background(), ProcessCuratorCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID,
		IdempotencyKey: "unsafe-starvation", CorrelationID: "request-unsafe-starvation",
	})
	if err != nil || result.Process.CandidatesScanned != maximumCuratorCandidates || len(result.Process.Derived) != 1 ||
		len(result.Process.Skipped) != maximumCuratorCandidates-1 {
		t.Fatalf("ProcessCurator(unsafe starvation) scanned=%d derived=%d skipped=%d, %v",
			result.Process.CandidatesScanned, len(result.Process.Derived), len(result.Process.Skipped), err)
	}
}

func TestCuratorValidSourceIsNotStarvedByOneHundredInvalidUTF8Sources(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	insertRawCuratorMeetingSources(t, storage, workspace.ID, project.ID, maximumCuratorCandidates, false)
	result, err := storage.db.Exec("UPDATE meeting_proposals SET summary = ?", string([]byte{0xff}))
	if err != nil {
		t.Fatalf("make curator sources invalid UTF-8: %v", err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed != maximumCuratorCandidates {
		t.Fatalf("invalid UTF-8 source rows = %d, %v", changed, err)
	}
	insertRawCuratorMeetingSources(t, storage, workspace.ID, project.ID, 1, false)
	if _, err := storage.ConfigureCuratorRule(context.Background(), ConfigureCuratorRuleCommand{
		WorkspaceIdentifier: workspace.ID, RuleName: domain.CuratorRuleAcceptedMeetingResolutionCopy,
		Enabled: true, ExpectedRevision: 1, IdempotencyKey: "invalid-utf8-enable", CorrelationID: "request-invalid-utf8-enable",
	}); err != nil {
		t.Fatalf("ConfigureCuratorRule() error = %v", err)
	}
	processed, err := storage.ProcessCurator(context.Background(), ProcessCuratorCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ApplySafe: true,
		IdempotencyKey: "invalid-utf8-starvation", CorrelationID: "request-invalid-utf8-starvation",
	})
	if err != nil || processed.Process.CandidatesScanned != maximumCuratorCandidates || len(processed.Process.Derived) != 1 ||
		len(processed.Process.Accepted) != 1 || len(processed.Process.Skipped) != maximumCuratorCandidates-1 ||
		processed.Process.Accepted[0].KnowledgeRevisionID != processed.Process.Derived[0].KnowledgeRevisionID {
		t.Fatalf("ProcessCurator(invalid UTF-8 starvation) = %#v, %v", processed, err)
	}
}

func TestCuratorIntactPrederivedRevisionIsNotStarvedByOlderMismatches(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	workspace, project := initializeWorkTestProject(t, storage)
	insertRawCuratorMeetingSources(t, storage, workspace.ID, project.ID, maximumCuratorAcceptances+1, false)
	derived, err := storage.ProcessCurator(context.Background(), ProcessCuratorCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID,
		IdempotencyKey: "mismatch-starvation-derive", CorrelationID: "request-mismatch-starvation-derive",
	})
	if err != nil || len(derived.Process.Derived) != maximumCuratorAcceptances+1 {
		t.Fatalf("ProcessCurator(derive mismatch sources) = %#v, %v", derived, err)
	}
	rows, err := storage.db.Query(`
SELECT d.knowledge_revision_id, d.source_id
FROM curator_derivations d
JOIN knowledge_revisions kr ON kr.id = d.knowledge_revision_id
WHERE d.workspace_id = ? AND d.project_id = ? AND d.rule_name = ?
ORDER BY crewfold_timestamp_key(kr.proposed_at), kr.id`,
		workspace.ID, project.ID, domain.CuratorRuleAcceptedMeetingResolutionCopy)
	if err != nil {
		t.Fatalf("list ordered curator derivations: %v", err)
	}
	revisionIDs := make([]string, 0, maximumCuratorAcceptances+1)
	sourceIDs := make([]string, 0, maximumCuratorAcceptances+1)
	for rows.Next() {
		var revisionID, sourceID string
		if err := rows.Scan(&revisionID, &sourceID); err != nil {
			rows.Close()
			t.Fatalf("scan ordered curator derivation: %v", err)
		}
		revisionIDs = append(revisionIDs, revisionID)
		sourceIDs = append(sourceIDs, sourceID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("iterate ordered curator derivations: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("close ordered curator derivations: %v", err)
	}
	if len(revisionIDs) != maximumCuratorAcceptances+1 {
		t.Fatalf("ordered curator derivations = %d, want %d", len(revisionIDs), maximumCuratorAcceptances+1)
	}
	for index := 0; index < maximumCuratorAcceptances; index++ {
		if _, err := storage.db.Exec("UPDATE meeting_proposals SET summary = summary || ? WHERE id = ?", " changed", sourceIDs[index]); err != nil {
			t.Fatalf("mismatch curator source %d: %v", index, err)
		}
	}
	if _, err := storage.ConfigureCuratorRule(context.Background(), ConfigureCuratorRuleCommand{
		WorkspaceIdentifier: workspace.ID, RuleName: domain.CuratorRuleAcceptedMeetingResolutionCopy,
		Enabled: true, ExpectedRevision: 1, IdempotencyKey: "mismatch-starvation-enable", CorrelationID: "request-mismatch-starvation-enable",
	}); err != nil {
		t.Fatalf("ConfigureCuratorRule() error = %v", err)
	}
	processed, err := storage.ProcessCurator(context.Background(), ProcessCuratorCommand{
		WorkspaceIdentifier: workspace.ID, ProjectIdentifier: project.ID, ApplySafe: true,
		IdempotencyKey: "mismatch-starvation-process", CorrelationID: "request-mismatch-starvation-process",
	})
	if err != nil || processed.Process.CandidatesScanned != maximumCuratorAcceptances+1 ||
		processed.Process.CandidatesScanned > maximumCuratorCandidates || len(processed.Process.Derived) != 0 ||
		len(processed.Process.Accepted) != 1 || processed.Process.Accepted[0].KnowledgeRevisionID != revisionIDs[maximumCuratorAcceptances] {
		t.Fatalf("ProcessCurator(mismatch starvation) = %#v, %v", processed, err)
	}
}

func TestCuratorPrederivedSafeRevisionPrecedesPersistentUnsafeSources(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	setup, _ := createAcceptedCuratorMeeting(t, storage)
	derived, err := storage.ProcessCurator(context.Background(), ProcessCuratorCommand{
		WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID,
		IdempotencyKey: "prederived-before-unsafe", CorrelationID: "request-prederived-before-unsafe",
	})
	if err != nil || len(derived.Process.Derived) != 1 {
		t.Fatalf("ProcessCurator(prederive) = %#v, %v", derived, err)
	}
	revisionID := derived.Process.Derived[0].KnowledgeRevisionID
	insertRawCuratorMeetingSources(t, storage, setup.workspace.ID, setup.project.ID, 100, true)
	if _, err := storage.ConfigureCuratorRule(context.Background(), ConfigureCuratorRuleCommand{
		WorkspaceIdentifier: setup.workspace.ID, RuleName: domain.CuratorRuleAcceptedMeetingResolutionCopy,
		Enabled: true, ExpectedRevision: 1, IdempotencyKey: "prederived-enable", CorrelationID: "request-prederived-enable",
	}); err != nil {
		t.Fatalf("ConfigureCuratorRule() error = %v", err)
	}
	result, err := storage.ProcessCurator(context.Background(), ProcessCuratorCommand{
		WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID, ApplySafe: true,
		IdempotencyKey: "prederived-safe-before-unsafe", CorrelationID: "request-prederived-safe-before-unsafe",
	})
	if err != nil || result.Process.CandidatesScanned != maximumCuratorCandidates || len(result.Process.Accepted) != 1 ||
		result.Process.Accepted[0].KnowledgeRevisionID != revisionID || len(result.Process.Skipped) != maximumCuratorCandidates-1 {
		t.Fatalf("ProcessCurator(prederived plus unsafe) scanned=%d accepted=%#v skipped=%d, %v",
			result.Process.CandidatesScanned, result.Process.Accepted, len(result.Process.Skipped), err)
	}
}

func TestCuratorDatabaseRejectsForgedAuthorityRecordsAndMutation(t *testing.T) {
	t.Parallel()
	storage := openTestStore(t, t.TempDir(), Options{})
	setup, _ := createAcceptedCuratorMeeting(t, storage)
	derived, err := storage.ProcessCurator(context.Background(), ProcessCuratorCommand{
		WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID,
		IdempotencyKey: "db-boundary-derive", CorrelationID: "request-db-boundary-derive",
	})
	if err != nil || len(derived.Process.Derived) != 1 {
		t.Fatalf("ProcessCurator(derive) = %#v, %v", derived, err)
	}
	derivation := derived.Process.Derived[0]
	if _, err := storage.ConfigureCuratorRule(context.Background(), ConfigureCuratorRuleCommand{
		WorkspaceIdentifier: setup.workspace.ID, RuleName: domain.CuratorRuleAcceptedMeetingResolutionCopy,
		Enabled: true, ExpectedRevision: 1, IdempotencyKey: "db-boundary-enable", CorrelationID: "request-db-boundary-enable",
	}); err != nil {
		t.Fatalf("ConfigureCuratorRule(enable) error = %v", err)
	}
	accepted, err := storage.ProcessCurator(context.Background(), ProcessCuratorCommand{
		WorkspaceIdentifier: setup.workspace.ID, ProjectIdentifier: setup.project.ID, ApplySafe: true,
		IdempotencyKey: "db-boundary-accept", CorrelationID: "request-db-boundary-accept",
	})
	if err != nil || len(accepted.Process.Accepted) != 1 {
		t.Fatalf("ProcessCurator(accept) = %#v, %v", accepted, err)
	}
	auto := accepted.Process.Accepted[0]

	assertSQLRejected := func(name, statement string, arguments ...any) {
		t.Helper()
		if _, err := storage.db.Exec(statement, arguments...); err == nil {
			t.Fatalf("%s unexpectedly succeeded", name)
		}
	}
	assertSQLRejected("noncontiguous owner rule", `INSERT INTO curator_rules(id,workspace_id,name,revision,enabled,created_at,created_by,event_sequence)
VALUES(?,?,?,99,1,?,'local-owner',?)`, "crule_ffffffffffffffffffffffffffffffff", setup.workspace.ID,
		domain.CuratorRuleAcceptedMeetingResolutionCopy, storage.nowText(), accepted.EventSequence)
	assertSQLRejected("forged subsystem config", `INSERT INTO curator_rules(id,workspace_id,name,revision,enabled,created_at,created_by,event_sequence)
VALUES(?,?,?,3,1,?,'subsystem:curator',0)`, "crule_eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", setup.workspace.ID,
		domain.CuratorRuleAcceptedMeetingResolutionCopy, storage.nowText())
	assertSQLRejected("mutate derivation", "UPDATE curator_derivations SET source_content_hash = ? WHERE id = ?", strings.Repeat("0", 64), derivation.ID)
	assertSQLRejected("delete derivation", "DELETE FROM curator_derivations WHERE id = ?", derivation.ID)
	assertSQLRejected("mutate auto acceptance", "UPDATE curator_auto_acceptances SET rule_revision = rule_revision + 1 WHERE id = ?", auto.ID)
	assertSQLRejected("delete auto acceptance", "DELETE FROM curator_auto_acceptances WHERE id = ?", auto.ID)

	if _, err := storage.ConfigureCuratorRule(context.Background(), ConfigureCuratorRuleCommand{
		WorkspaceIdentifier: setup.workspace.ID, RuleName: domain.CuratorRuleAcceptedMeetingResolutionCopy,
		Enabled: false, ExpectedRevision: 2, IdempotencyKey: "db-boundary-disable", CorrelationID: "request-db-boundary-disable",
	}); err != nil {
		t.Fatalf("ConfigureCuratorRule(disable) error = %v", err)
	}
	assertSQLRejected("duplicate auto acceptance remains rejected after effective disable", `INSERT INTO curator_auto_acceptances(
id,workspace_id,project_id,rule_id,rule_name,rule_revision,derivation_id,knowledge_revision_id,
authority_check_id,knowledge_event_sequence,event_sequence,created_at,actor_id,actor_type)
SELECT ?,workspace_id,project_id,rule_id,rule_name,rule_revision,derivation_id,knowledge_revision_id,
authority_check_id,knowledge_event_sequence,event_sequence,created_at,actor_id,actor_type
FROM curator_auto_acceptances WHERE id=?`, "cauto_ffffffffffffffffffffffffffffffff", auto.ID)
}

func TestCuratorDatabaseRejectsInvalidUTF8AndForgedOutputHashes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		invalidSource bool
		forgeHash     bool
	}{
		{name: "invalid UTF-8 exact copy", invalidSource: true},
		{name: "forged matching stored output hashes", forgeHash: true},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			storage := openTestStore(t, t.TempDir(), Options{})
			setup, proposal := createAcceptedCuratorMeeting(t, storage)
			if test.invalidSource {
				invalidSummary := "invalid-" + string([]byte{0xff})
				if _, err := storage.db.Exec("UPDATE meeting_proposals SET summary = ? WHERE id = ?", invalidSummary, proposal.ID); err != nil {
					t.Fatalf("make curator source invalid UTF-8: %v", err)
				}
			}
			if err := insertDirectCuratorDerivation(t, storage, setup.workspace.ID, setup.project.ID, proposal.ID, test.forgeHash); err == nil ||
				!strings.Contains(err.Error(), "invalid curator derivation") {
				t.Fatalf("direct curator derivation error = %v, want trigger rejection", err)
			}
		})
	}
}

func insertDirectCuratorDerivation(t *testing.T, storage *Store, workspaceID, projectID, proposalID string, forgeOutputHash bool) error {
	t.Helper()
	ctx := context.Background()
	tx, err := storage.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin direct curator derivation: %v", err)
	}
	defer tx.Rollback()

	var ruleID string
	var ruleRevision int64
	if err := tx.QueryRowContext(ctx, `
SELECT id, revision FROM curator_rules
WHERE workspace_id = ? AND name = ?
ORDER BY revision DESC LIMIT 1`, workspaceID, domain.CuratorRuleAcceptedMeetingResolutionCopy).Scan(&ruleID, &ruleRevision); err != nil {
		t.Fatalf("read direct curator rule: %v", err)
	}
	var meetingID, agenda, summary, proposalStatus, meetingStatus, outputHash, sourceHash string
	var proposalRevision int64
	if err := tx.QueryRowContext(ctx, `
SELECT m.id, m.agenda, mp.summary, mp.revision, mp.status, m.status,
       lower(hex(sha256(m.agenda || char(10) || mp.summary))),
       lower(hex(sha256(
           m.id || char(10) || mp.id || char(10) || CAST(mp.revision AS TEXT) || char(10) ||
           m.agenda || char(10) || mp.summary || char(10) || mp.status || char(10) || m.status
       )))
FROM meeting_proposals mp JOIN meetings m ON m.id = mp.meeting_id
WHERE mp.id = ?`, proposalID).Scan(&meetingID, &agenda, &summary, &proposalRevision, &proposalStatus, &meetingStatus, &outputHash, &sourceHash); err != nil {
		t.Fatalf("read direct curator source: %v", err)
	}
	if proposalStatus != domain.MeetingProposalAccepted || meetingStatus != domain.MeetingConcluded {
		t.Fatalf("direct curator source status = %s/%s", proposalStatus, meetingStatus)
	}
	if forgeOutputHash {
		outputHash = strings.Repeat("f", 64)
	}
	itemID, err := randomID("know_")
	if err != nil {
		t.Fatalf("generate direct curator item ID: %v", err)
	}
	revisionID, err := randomID("krev_")
	if err != nil {
		t.Fatalf("generate direct curator revision ID: %v", err)
	}
	derivationID, err := randomID("cder_")
	if err != nil {
		t.Fatalf("generate direct curator derivation ID: %v", err)
	}
	now := storage.nowText()
	if _, err := tx.ExecContext(ctx, `
INSERT INTO knowledge_items(id,workspace_id,project_id,task_scope_id,type,created_at,created_by,created_by_type)
VALUES(?,?,?,NULL,'decision',?,'subsystem:curator','subsystem')`, itemID, workspaceID, projectID, now); err != nil {
		t.Fatalf("insert direct curator item: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO knowledge_revisions(
    id,item_id,revision_number,state_revision,title,body,content_hash,review_status,currency_status,
    confidence,verification_status,freshness_policy,proposed_at,proposed_by,proposed_by_type
) VALUES(?,?,1,1,?,?,?,'proposed','pending','medium','supported','until_superseded',?,'subsystem:curator','subsystem')`,
		revisionID, itemID, agenda, summary, outputHash, now); err != nil {
		t.Fatalf("insert direct curator revision: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO knowledge_sources(revision_id,ordinal,source_type,source_id,source_revision,role)
VALUES(?,0,'meeting_proposal',?,?,'primary')`, revisionID, proposalID, proposalRevision); err != nil {
		t.Fatalf("insert direct curator source: %v", err)
	}
	eventSequence, err := appendEventForActor(ctx, tx, workspaceID, "curator_derivation", derivationID, 1,
		curatorDerivedEvent, "request-direct-curator-derivation", now, domain.CuratorActorID, domain.KnowledgeActorSubsystem,
		map[string]any{"knowledge_revision_id": revisionID, "meeting_id": meetingID})
	if err != nil {
		t.Fatalf("append direct curator event: %v", err)
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO curator_derivations(
    id,workspace_id,project_id,rule_id,rule_name,rule_revision,source_type,source_id,source_revision,
    source_content_hash,knowledge_revision_id,output_content_hash,created_at,created_by,event_sequence
) VALUES(?,?,?,?,?,?,'meeting_proposal',?,?,?,?,?,?,'subsystem:curator',?)`,
		derivationID, workspaceID, projectID, ruleID, domain.CuratorRuleAcceptedMeetingResolutionCopy, ruleRevision,
		proposalID, proposalRevision, sourceHash, revisionID, outputHash, now, eventSequence)
	return err
}

func insertRawCuratorMeetingSources(t *testing.T, storage *Store, workspaceID, projectID string, count int, unsafe bool) {
	t.Helper()
	now := storage.nowText()
	var existing int
	if err := storage.db.QueryRow("SELECT COUNT(*) FROM meeting_proposals").Scan(&existing); err != nil {
		t.Fatalf("count raw curator sources: %v", err)
	}
	agentID := "agent_000000000000000000000000000000bb"
	taskLowID := "task_00000000000000000000000000000011"
	taskHighID := "task_00000000000000000000000000000012"
	if _, err := storage.db.Exec(`INSERT OR IGNORE INTO agents(id,workspace_id,name,role,provider,runtime,enabled,max_concurrency,revision,created_at,updated_at,created_by,updated_by)
VALUES(?,?,?,?,?,?,1,1,1,?,?,?,?)`, agentID, workspaceID, "curator-raw-agent", "fixture", "fake", "fake", now, now, localOwnerActorID, localOwnerActorID); err != nil {
		t.Fatalf("insert raw agent: %v", err)
	}
	for _, taskID := range []string{taskLowID, taskHighID} {
		if _, err := storage.db.Exec(`INSERT OR IGNORE INTO tasks(id,workspace_id,project_id,objective_id,title,description,status,blocked_reason,priority,budget_tokens,budget_cost_cents,budget_time_seconds,revision,created_at,updated_at,created_by,updated_by)
VALUES(?,?,?,NULL,?,NULL,'ready',NULL,100,0,0,0,1,?,?,?,?)`, taskID, workspaceID, projectID, taskID, now, now, localOwnerActorID, localOwnerActorID); err != nil {
			t.Fatalf("insert raw task: %v", err)
		}
	}
	for offset := 0; offset < count; offset++ {
		index := existing + offset + 1
		suffix := fmt.Sprintf("%032x", index)
		claimLowID := "claim_" + fmt.Sprintf("%032x", 10000+index*2)
		claimHighID := "claim_" + fmt.Sprintf("%032x", 10000+index*2+1)
		for claimIndex, claimID := range []string{claimLowID, claimHighID} {
			taskID := taskLowID
			if claimIndex == 1 {
				taskID = taskHighID
			}
			if _, err := storage.db.Exec(`INSERT INTO work_claims(id,workspace_id,project_id,task_id,checkout_id,kind,target,mode,conflict_policy,status,baseline_paths_json,lease_expires_at,revision,created_at,updated_at,created_by,updated_by)
VALUES(?,?,?,?,NULL,'path',?,'exclusive','request_resolution','active','[]',?,1,?,?,?,?)`, claimID, workspaceID, projectID, taskID, "raw/"+claimID, "2099-01-01T00:00:00Z", now, now, localOwnerActorID, localOwnerActorID); err != nil {
				t.Fatalf("insert raw claim: %v", err)
			}
		}
		overlapID := "overlap_" + suffix
		meetingID := "meet_" + suffix
		proposalID := "proposal_" + suffix
		if _, err := storage.db.Exec(`INSERT INTO work_overlaps(id,workspace_id,project_id,claim_low_id,claim_high_id,task_low_id,task_high_id,kind,witness,severity,policy_response,scheduling_paused,resolution_required,status,explanation_json,detected_at,resolved_at,resolution_reason,revision)
VALUES(?,?,?,?,?,?,?,'path','raw','medium','request_resolution',0,1,'resolved','{}',?,?,?,1)`, overlapID, workspaceID, projectID, claimLowID, claimHighID, taskLowID, taskHighID, now, now, "fixture"); err != nil {
			t.Fatalf("insert raw overlap: %v", err)
		}
		if _, err := storage.db.Exec(`INSERT INTO meetings(id,workspace_id,project_id,overlap_id,agenda,facilitator_agent_id,policy,reviewer_agent_id,allowed_actions_json,status,frozen_input_json,frozen_input_hash,deadline_at,stalled_reason,revision,created_at,updated_at,created_by,updated_by)
VALUES(?,?,?,?,?,?,'owner_decision',NULL,'[]','concluded','{}',?,? ,NULL,1,?,?,?,?)`, meetingID, workspaceID, projectID, overlapID, "Raw agenda "+suffix, agentID, strings.Repeat("0", 64), "2099-01-01T00:00:00Z", now, now, localOwnerActorID, localOwnerActorID); err != nil {
			t.Fatalf("insert raw meeting: %v", err)
		}
		summary := "Raw summary " + suffix
		if unsafe {
			summary = strings.Repeat("x", maximumCuratorSummaryBytes+1)
		}
		if _, err := storage.db.Exec(`INSERT INTO meeting_proposals(id,meeting_id,proposed_by,summary,status,revision,proposed_at,decided_at,decision_note)
VALUES(?,?,?,?,'accepted',2,?,?,?)`, proposalID, meetingID, agentID, summary, now, now, "fixture accepted"); err != nil {
			t.Fatalf("insert raw proposal: %v", err)
		}
	}
}

func createAcceptedCuratorMeeting(t *testing.T, storage *Store) (meetingTestSetup, domain.MeetingProposal) {
	t.Helper()
	setup := initializeMeetingTest(t, storage, 2)
	return setup, acceptCuratorMeetingForSetup(t, storage, setup, "curator")
}

func acceptCuratorMeetingForSetup(t *testing.T, storage *Store, setup meetingTestSetup, key string) domain.MeetingProposal {
	t.Helper()
	created := createMeetingTest(t, storage, setup, domain.MeetingPolicyOwnerDecision, nil, "")
	proposed, err := storage.RunMeeting(context.Background(), RunMeetingCommand{
		WorkspaceIdentifier: setup.workspace.ID, MeetingID: created.Detail.Meeting.ID,
		ExpectedRevision: created.Detail.Meeting.Revision, Fixture: sequenceMeetingFixture(setup),
		IdempotencyKey: key + "-run-meeting", CorrelationID: "request-" + key + "-run-meeting",
	})
	if err != nil || proposed.Detail.Proposal == nil {
		t.Fatalf("RunMeeting(curator source) = %#v, %v", proposed, err)
	}
	accepted, err := storage.AcceptMeeting(context.Background(), AcceptMeetingCommand{
		WorkspaceIdentifier: setup.workspace.ID, MeetingID: created.Detail.Meeting.ID,
		ExpectedRevision: proposed.Detail.Meeting.Revision, DecisionNote: "accepted curator source",
		IdempotencyKey: key + "-accept-meeting", CorrelationID: "request-" + key + "-accept-meeting",
	})
	if err != nil || accepted.Detail.Proposal == nil || accepted.Detail.Proposal.Status != domain.MeetingProposalAccepted ||
		accepted.Detail.Meeting.Status != domain.MeetingConcluded {
		t.Fatalf("AcceptMeeting(curator source) = %#v, %v", accepted, err)
	}
	return *accepted.Detail.Proposal
}

func acceptedMeetingAgenda(t *testing.T, storage *Store, workspaceID, meetingID string) string {
	t.Helper()
	detail, err := storage.Meeting(context.Background(), workspaceID, meetingID)
	if err != nil {
		t.Fatalf("Meeting(%s) error = %v", meetingID, err)
	}
	return detail.Meeting.Agenda
}

func initializeCuratorQueueProposals(t *testing.T, storage *Store, workspaceID, projectID string, count int) (Workspace, []domain.KnowledgeRevision) {
	t.Helper()
	workspace, err := storage.Workspace(context.Background(), workspaceID)
	if err != nil {
		t.Fatalf("Workspace(%s) error = %v", workspaceID, err)
	}
	revisions := make([]domain.KnowledgeRevision, 0, count)
	for index := 0; index < count; index++ {
		task := createWorkTestTask(t, storage, workspaceID, projectID, fmt.Sprintf("queue task %d", index), fmt.Sprintf("queue-task-%d", index))
		proposal := proposeTaskKnowledge(t, storage, workspaceID, task.Task.ID, fmt.Sprintf("queue-proposal-%d", index),
			fmt.Sprintf("Queue title %d", index), fmt.Sprintf("Queue body %d", index), "")
		revisions = append(revisions, proposal.Revision)
	}
	return workspace, revisions
}
