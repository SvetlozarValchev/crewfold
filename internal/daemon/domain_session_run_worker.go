package daemon

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/store"
)

// processDurableAgentRun keeps accepted task work in the same provider thread
// as the durable agent's owner conversation. A run is still the exact
// authority and audit envelope for one attempt; it is no longer a second
// provider personality or a second Herdr/Codex session.
func (s *server) processDurableAgentRun(ctx context.Context, work store.RunWork) (bool, error) {
	run := work.Run
	if run.Provider != "codex" || run.Runtime != "herdr" {
		return false, nil
	}
	session, err := s.store.DomainAgentSession(ctx, run.WorkspaceID, run.ProjectID, run.AgentID)
	if err != nil {
		switch store.ErrorCode(err) {
		case store.CodeDomainAgentNotFound, store.CodeAgentNotFound:
			return false, nil
		default:
			return true, err
		}
	}
	if session.State == domain.DomainAgentSessionDetached {
		_, recordErr := s.store.LoseRun(ctx, run.ID, "durable agent provider thread is detached from this node", durableRunCorrelation(run))
		s.logRunWorkerStoreError(run.ID, "record detached durable agent run", recordErr)
		return true, nil
	}
	// Active provider reads are intentionally concurrent with owner viewing and
	// steering. Codex app-server multiplexes request IDs on one transport; a
	// read-only reconciliation request must not hide the live task or postpone
	// owner input until after that task has ended.
	if run.Status == domain.RunActive {
		return true, s.pollDurableAgentRun(ctx, work, session, durableRunCorrelation(run))
	}
	host := s.ensureDomainSessionHost()
	host.operationMu.Lock()
	defer host.operationMu.Unlock()
	// MarkRunStarting and runtime binding deliberately make the durable job
	// pending at each crash boundary. The other worker lane may therefore have
	// claimed the next revision while this launch still owns the provider
	// operation. Re-read after acquiring the operation lock and discard that
	// stale claim; the current revision already has its own durable pending job.
	current, currentErr := s.store.RunDetail(ctx, run.WorkspaceID, run.ID)
	if currentErr != nil {
		return true, currentErr
	}
	if current.Run.Revision != run.Revision || current.Run.Status != run.Status {
		return true, nil
	}

	correlationID := durableRunCorrelation(run)
	switch run.Status {
	case domain.RunRequested:
		starting, err := s.store.MarkRunStarting(ctx, run.ID, correlationID)
		if err != nil {
			return true, s.recordWorkerTransitionFailure(ctx, run, "mark durable agent run starting", err, correlationID)
		}
		return true, s.launchDurableAgentRunTurn(ctx, starting, session, correlationID)
	case domain.RunStarting:
		if run.RuntimeHandle == "" {
			starting, err := s.store.MarkRunStarting(ctx, run.ID, correlationID)
			if err != nil {
				return true, s.recordWorkerTransitionFailure(ctx, run, "revalidate durable agent run starting", err, correlationID)
			}
			return true, s.launchDurableAgentRunTurn(ctx, starting, session, correlationID)
		}
		return true, s.reconcileStartingDurableAgentRun(ctx, run, session, correlationID)
	case domain.RunStopping:
		return true, s.stopDurableAgentRun(ctx, run, session, correlationID)
	default:
		return true, nil
	}
}

func durableRunCorrelation(run domain.Run) string {
	return fmt.Sprintf("durable-agent-worker-%s-%d", run.ID, run.Revision)
}

// restoreDomainAgentRunTurn reconnects the in-memory steering route to the
// canonical run/thread binding after a daemon restart. The run worker normally
// does this while reconciling its lease, but the owner and mailbox surfaces may
// become reachable first. They must never start a parallel provider turn merely
// because the process-local route has not been rebuilt yet.
func (s *server) restoreDomainAgentRunTurn(ctx context.Context, session domain.DomainAgentSession, thread execution.CodexThread) error {
	host := s.ensureDomainSessionHost()
	if _, ok := host.activeRunTurn(session.ThreadID); ok {
		return nil
	}
	scope, err := s.store.DomainAgentSessionScopeByThread(ctx, session.ThreadID)
	if err != nil {
		return err
	}
	_, runIDs, err := s.store.DomainAgentSessionRotationBlockers(ctx, scope.Workspace.ID, scope.Project.ID, scope.Agent.ID)
	if err != nil {
		return err
	}
	for _, runID := range runIDs {
		detail, detailErr := s.store.RunDetail(ctx, scope.Workspace.ID, runID)
		if detailErr != nil {
			return detailErr
		}
		if detail.Run.Status != domain.RunStarting && detail.Run.Status != domain.RunActive && detail.Run.Status != domain.RunStopping {
			continue
		}
		threadID, threadOK := execution.ParseDomainAgentRuntimeHandle(detail.Run.RuntimeHandle)
		turnID, turnOK := execution.ParseDomainAgentTurnHandle(detail.Run.ProviderHandle)
		if !threadOK || !turnOK || threadID != session.ThreadID || !s.store.RunBindingIsCurrent(detail.Run) {
			continue
		}
		for _, turn := range thread.Turns {
			if turn.ID == turnID && !isTerminalDomainSessionTurn(turn.Status) {
				host.bindRunTurn(session.ThreadID, detail.Run.ID, turnID)
				return nil
			}
		}
	}
	return nil
}

func (s *server) launchDurableAgentRunTurn(ctx context.Context, run domain.Run, session domain.DomainAgentSession, correlationID string) error {
	if session.State == domain.DomainAgentSessionUnbound {
		var err error
		session, err = s.ensureDomainAgentSessionBound(ctx, run.WorkspaceID, run.ProjectID, run.AgentID, run.CheckoutID)
		if err != nil {
			_, recordErr := s.store.FailRunStart(ctx, run.ID, "start durable agent provider thread: "+err.Error(), correlationID)
			s.logRunWorkerStoreError(run.ID, "record durable agent thread start failure", recordErr)
			return nil
		}
	}
	if session.State != domain.DomainAgentSessionReady {
		_, recordErr := s.store.FailRunStart(ctx, run.ID, "durable agent provider thread is unavailable", correlationID)
		s.logRunWorkerStoreError(run.ID, "record unavailable durable agent thread", recordErr)
		return nil
	}
	session, thread, err := s.loadDomainAgentSession(ctx, session)
	if err != nil {
		if errors.Is(err, errDurableCodexRolloutUnavailableWithActiveRun) {
			_, recordErr := s.store.LoseRun(ctx, run.ID, err.Error(), correlationID)
			s.logRunWorkerStoreError(run.ID, "record missing durable agent rollout", recordErr)
			return nil
		}
		_, recordErr := s.store.FailRunStart(ctx, run.ID, "resume durable agent provider thread: "+err.Error(), correlationID)
		s.logRunWorkerStoreError(run.ID, "record durable agent thread resume failure", recordErr)
		return nil
	}
	if existing, ok := codexTurnForClientMessage(thread, "crewfold-run:"+run.ID); ok {
		return s.activateDurableAgentRunTurn(ctx, run, session, existing, correlationID)
	}

	runtimeHandle := execution.DomainAgentRuntimeHandle(session.ThreadID)
	bound, err := s.store.RecordRunRuntimeBinding(ctx, run.ID, runtimeHandle, correlationID)
	if err != nil {
		s.logRunWorkerStoreError(run.ID, "record durable agent runtime binding", err)
		return nil
	}
	briefing, err := s.store.AuthorizeRunCapability(ctx, run.ID)
	if err != nil {
		_, recordErr := s.store.FailRunStart(ctx, run.ID, "load accepted task briefing: "+err.Error(), correlationID)
		s.logRunWorkerStoreError(run.ID, "record durable agent briefing failure", recordErr)
		return nil
	}
	prompt := durableAgentTaskPrompt(briefing)
	host := s.ensureDomainSessionHost()
	turn, err := host.startRunTurn(ctx, session.ThreadID, run.ID, run.Placement.CheckoutPath, prompt, s.config.CodexToolNetworkAccess)
	if err != nil {
		host.clearRunTurn(session.ThreadID, run.ID)
		_, recordErr := s.store.FailRunStart(ctx, run.ID, "start accepted task turn in durable agent session: "+err.Error(), correlationID)
		s.logRunWorkerStoreError(run.ID, "record durable agent task turn failure", recordErr)
		return nil
	}
	return s.activateDurableAgentRunTurn(ctx, bound, session, turn, correlationID)
}

func (s *server) activateDurableAgentRunTurn(ctx context.Context, run domain.Run, session domain.DomainAgentSession, turn execution.CodexTurn, correlationID string) error {
	runtimeHandle := execution.DomainAgentRuntimeHandle(session.ThreadID)
	if run.RuntimeHandle == "" {
		var err error
		run, err = s.store.RecordRunRuntimeBinding(ctx, run.ID, runtimeHandle, correlationID)
		if err != nil {
			s.logRunWorkerStoreError(run.ID, "recover durable agent runtime binding", err)
			return nil
		}
	}
	if run.RuntimeHandle != runtimeHandle || !s.store.RunBindingIsCurrent(run) {
		_, recordErr := s.store.LoseRun(ctx, run.ID, "durable agent task binding does not match the current provider thread", correlationID)
		s.logRunWorkerStoreError(run.ID, "record mismatched durable agent binding", recordErr)
		return nil
	}
	s.ensureDomainSessionHost().bindRunTurn(session.ThreadID, run.ID, turn.ID)
	_, err := s.store.MarkRunStarted(ctx, run.ID, runtimeHandle, execution.DomainAgentTurnHandle(turn.ID), correlationID)
	s.logRunWorkerStoreError(run.ID, "mark durable agent run started", err)
	return nil
}

func (s *server) reconcileStartingDurableAgentRun(ctx context.Context, run domain.Run, session domain.DomainAgentSession, correlationID string) error {
	threadID, ok := execution.ParseDomainAgentRuntimeHandle(run.RuntimeHandle)
	if !ok || threadID != session.ThreadID || !s.store.RunBindingIsCurrent(run) {
		_, recordErr := s.store.LoseRun(ctx, run.ID, "durable agent task thread binding cannot be trusted", correlationID)
		s.logRunWorkerStoreError(run.ID, "record untrusted durable agent start", recordErr)
		return nil
	}
	session, thread, err := s.loadDomainAgentSession(ctx, session)
	if err != nil {
		if errors.Is(err, errDurableCodexRolloutUnavailableWithActiveRun) {
			_, recordErr := s.store.LoseRun(ctx, run.ID, err.Error(), correlationID)
			s.logRunWorkerStoreError(run.ID, "record missing durable agent rollout during start", recordErr)
			return nil
		}
		if deferErr := s.store.DeferRunJob(ctx, run.ID, runActivePollDelay); deferErr != nil {
			s.logRunWorkerStoreError(run.ID, "defer durable agent thread recovery", deferErr)
		}
		return nil
	}
	if run.ProviderHandle != "" {
		turnID, valid := execution.ParseDomainAgentTurnHandle(run.ProviderHandle)
		if !valid {
			_, recordErr := s.store.LoseRun(ctx, run.ID, "durable agent task turn binding is malformed", correlationID)
			s.logRunWorkerStoreError(run.ID, "record malformed durable agent turn", recordErr)
			return nil
		}
		turn, err := s.ensureDomainSessionHost().runTurn(ctx, session.ThreadID, run.ID, turnID)
		if err != nil {
			return s.deferDurableAgentRun(ctx, run.ID, "reconcile durable agent task turn")
		}
		return s.activateDurableAgentRunTurn(ctx, run, session, turn, correlationID)
	}
	turn, found := codexTurnForClientMessage(thread, "crewfold-run:"+run.ID)
	if !found {
		return s.launchDurableAgentRunTurn(ctx, run, session, correlationID)
	}
	return s.activateDurableAgentRunTurn(ctx, run, session, turn, correlationID)
}

func (s *server) pollDurableAgentRun(ctx context.Context, work store.RunWork, session domain.DomainAgentSession, correlationID string) error {
	run := work.Run
	threadID, threadOK := execution.ParseDomainAgentRuntimeHandle(run.RuntimeHandle)
	turnID, turnOK := execution.ParseDomainAgentTurnHandle(run.ProviderHandle)
	if !threadOK || !turnOK || threadID != session.ThreadID || !s.store.RunBindingIsCurrent(run) {
		_, recordErr := s.store.LoseRun(ctx, run.ID, "durable agent task bindings are missing, foreign, or inconsistent", correlationID)
		s.logRunWorkerStoreError(run.ID, "record invalid durable agent task binding", recordErr)
		return nil
	}
	session, _, err := s.loadDomainAgentSession(ctx, session)
	if err != nil {
		if errors.Is(err, errDurableCodexRolloutUnavailableWithActiveRun) {
			_, recordErr := s.store.LoseRun(ctx, run.ID, err.Error(), correlationID)
			s.logRunWorkerStoreError(run.ID, "record missing durable agent rollout while active", recordErr)
			return nil
		}
		return s.deferDurableAgentRun(ctx, run.ID, "resume durable agent task thread")
	}
	host := s.ensureDomainSessionHost()
	turn, err := host.runTurn(ctx, session.ThreadID, run.ID, turnID)
	if err != nil {
		return s.deferDurableAgentRun(ctx, run.ID, "read durable agent task turn")
	}
	report, queued, err := s.store.NextPendingRunReport(ctx, run.ID)
	if err != nil {
		return s.deferDurableAgentRun(ctx, run.ID, "read durable agent run report")
	}
	if queued {
		if (report.Kind == domain.ObservationCompletion || report.Kind == domain.ObservationExecutiveResponse) && !isTerminalDomainSessionTurn(turn.Status) {
			return s.deferDurableAgentRun(ctx, run.ID, "wait for durable agent completion turn")
		}
		accepted, missing := execution.AcceptancePasses(work.Scenario.Acceptance, report.Evidence)
		logsReason := "provider activity is retained in this durable agent's Codex epoch"
		if report.Kind != domain.ObservationCompletion && report.Kind != domain.ObservationExecutiveResponse {
			logsReason = ""
		}
		_, applyErr := s.store.ApplyQueuedRunReport(ctx, run.ID, report.ID, accepted, missing, nil, logsReason, correlationID)
		if applyErr == nil {
			s.signalOwnerManagerReviewWorker()
			if report.Kind != domain.ObservationProgress {
				host.clearRunTurn(session.ThreadID, run.ID)
			}
		}
		s.logRunWorkerStoreError(run.ID, "apply durable agent run report", applyErr)
		return nil
	}
	if !isTerminalDomainSessionTurn(turn.Status) {
		return s.deferDurableAgentRun(ctx, run.ID, "poll active durable agent turn")
	}
	host.clearRunTurn(session.ThreadID, run.ID)
	code, message := "provider_ended", "durable agent task turn ended without reporting completion or a blocker through Crewfold"
	if turn.Status == "failed" {
		code, message = "process_exited", "durable agent task turn failed before reporting an outcome"
	}
	_, failErr := s.store.FailRun(ctx, run.ID, code, message, nil, "provider activity is retained in this durable agent's Codex epoch", correlationID)
	s.logRunWorkerStoreError(run.ID, "record terminal durable agent turn without report", failErr)
	return nil
}

func (s *server) stopDurableAgentRun(ctx context.Context, run domain.Run, session domain.DomainAgentSession, correlationID string) error {
	threadID, threadOK := execution.ParseDomainAgentRuntimeHandle(run.RuntimeHandle)
	turnID, turnOK := execution.ParseDomainAgentTurnHandle(run.ProviderHandle)
	if !threadOK || !turnOK || threadID != session.ThreadID || !s.store.RunBindingIsCurrent(run) {
		_, recordErr := s.store.LoseRun(ctx, run.ID, "durable agent task stop cannot trust its thread binding", correlationID)
		s.logRunWorkerStoreError(run.ID, "record untrusted durable agent stop", recordErr)
		return nil
	}
	session, _, err := s.loadDomainAgentSession(ctx, session)
	if err != nil {
		if errors.Is(err, errDurableCodexRolloutUnavailableWithActiveRun) {
			_, recordErr := s.store.LoseRun(ctx, run.ID, err.Error(), correlationID)
			s.logRunWorkerStoreError(run.ID, "record missing durable agent rollout during stop", recordErr)
			return nil
		}
		return s.deferDurableAgentRun(ctx, run.ID, "resume durable agent task for stop")
	}
	if err := s.ensureDomainSessionHost().interruptTurn(ctx, session.ThreadID, turnID); err != nil {
		return s.deferDurableAgentRun(ctx, run.ID, "interrupt durable agent task turn")
	}
	s.ensureDomainSessionHost().clearRunTurn(session.ThreadID, run.ID)
	_, markErr := s.store.MarkRunStopped(ctx, run.ID, false, "durable agent task turn interrupted", nil, "provider activity is retained in this durable agent's Codex epoch", correlationID)
	s.logRunWorkerStoreError(run.ID, "mark durable agent task stopped", markErr)
	return nil
}

func (s *server) deferDurableAgentRun(ctx context.Context, runID, operation string) error {
	if err := s.store.DeferRunJob(ctx, runID, runActivePollDelay); err != nil {
		s.logRunWorkerStoreError(runID, operation, err)
	}
	return nil
}

func durableAgentTaskPrompt(briefing domain.RunBriefing) string {
	title := strings.TrimSpace(briefing.Task.Title)
	description := strings.TrimSpace(briefing.Task.Description)
	evidenceRequirement := ""
	if briefing.Task.TaskClass == "review" || briefing.Task.TaskClass == "verification" {
		evidenceRequirement = fmt.Sprintf(`

This is a %s task. Before reporting completion, publish one bounded human-readable findings/check-results artifact with crewfold_publish_artifact and include the returned artifact ID in the completion report's evidence. A prose assessment or checks list without that exact evidence intentionally blocks delivery closure.`, briefing.Task.TaskClass)
	}
	return fmt.Sprintf(`Crewfold has attached one accepted task turn to this same durable agent identity.

Task: %s
Task class: %s
Description: %s
Run: %s
Checkout: %s

This turn is the task's exact execution attempt. Work only in the supplied checkout. First call crewfold_get_briefing and follow its exact assignment, constraints, accepted context, and capability. Do not create provider-local subagents as substitutes for Crewfold durable coworkers. Record material progress with the run-scoped progress tool. Finish by reporting exactly one completion or blocker through the run-scoped Crewfold tools; a normal prose answer without that report cannot complete the task. Owner input received while you work is steering for this same turn and identity.%s`, title, briefing.Task.TaskClass, description, briefing.Run.ID, briefing.Run.Placement.CheckoutPath, evidenceRequirement)
}
