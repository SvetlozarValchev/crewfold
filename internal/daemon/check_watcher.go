package daemon

import (
	"context"
	"fmt"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/store"
)

const (
	daemonCheckWatchCandidateLimit = 100
	daemonCheckWatchScopePageLimit = 100
)

// startCheckWatcher reconciles every project through the same two-phase,
// real-Git path as the public check.watch method. An exact background no-op is
// deliberately not persisted by ApplyCheckWatch.
func (s *server) startCheckWatcher() {
	if s.config.DisableCheckWatcher {
		return
	}
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		s.runCheckWatchSweep(context.Background())
		ticker := time.NewTicker(s.config.CheckWatchScanInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-ticker.C:
				s.runCheckWatchSweep(context.Background())
			}
		}
	}()
}

func (s *server) runCheckWatchSweep(ctx context.Context) {
	page, err := s.store.ListCheckWatchScopes(ctx, store.ListCheckWatchScopesQuery{
		After: s.checkWatchScopeCursor, Limit: daemonCheckWatchScopePageLimit,
	})
	if err != nil {
		s.config.Logger.Error("check watcher could not enumerate projects", "component", "check_watcher", "error", err)
		return
	}
	if len(page.Items) == 0 && s.checkWatchScopeCursor != "" {
		// Wrap after the last project. The next bounded page revisits newly added
		// lower IDs and never turns an empty identifier into a wildcard pass.
		s.checkWatchScopeCursor = ""
		page, err = s.store.ListCheckWatchScopes(ctx, store.ListCheckWatchScopesQuery{Limit: daemonCheckWatchScopePageLimit})
		if err != nil {
			s.config.Logger.Error("check watcher could not restart project enumeration", "component", "check_watcher", "error", err)
			return
		}
	}
	s.checkWatchScopeCursor = page.NextCursor
	for _, scope := range page.Items {
		pass := s.checkWatchPass.Add(1)
		key := fmt.Sprintf("daemon-check-watch-%d-%d-%s", s.startedAt.UnixNano(), pass, scope.ProjectID)
		result, err := s.runPreparedCheckWatch(ctx, store.PrepareCheckWatchCommand{
			WorkspaceIdentifier: scope.WorkspaceID, ProjectIdentifier: scope.ProjectID,
			Limit: daemonCheckWatchCandidateLimit,
		}, key, key, false)
		if err != nil {
			s.config.Logger.Warn("check watcher pass stopped without effects", "component", "check_watcher",
				"workspace_id", scope.WorkspaceID, "project_id", scope.ProjectID, "error", err)
			continue
		}
		if result.EventSequence != 0 {
			s.config.Logger.Info("check watcher pass applied durable reconciliation", "component", "check_watcher",
				"workspace_id", scope.WorkspaceID, "project_id", scope.ProjectID,
				"freshness_appended", result.Value.FreshnessAppended,
				"notifications_created", result.Value.NotificationsCreated,
				"route_failures_created", result.Value.RouteFailuresCreated,
				"repairs_marked_stale", result.Value.RepairsMarkedStale,
				"event_sequence", result.EventSequence)
		}
	}
}

func (s *server) runPreparedCheckWatch(ctx context.Context, command store.PrepareCheckWatchCommand, idempotencyKey, correlationID string, persistNoop bool) (store.MutationResult[domain.CheckWatchReceipt], error) {
	// Serialize the read/inspect/apply snapshot within one daemon. Apply still
	// revalidates every frozen Store and repository/checkout revision.
	s.checkWatchMu.Lock()
	defer s.checkWatchMu.Unlock()

	if persistNoop {
		replayed, found, err := s.store.ReplayCheckWatch(ctx, command, idempotencyKey)
		if err != nil {
			return store.MutationResult[domain.CheckWatchReceipt]{}, err
		}
		if found {
			return replayed, nil
		}
	}
	prepared, err := s.store.PrepareCheckWatch(ctx, command)
	if err != nil {
		return store.MutationResult[domain.CheckWatchReceipt]{}, err
	}
	observations := s.observeCheckWatchCandidates(ctx, prepared.Candidates)
	return s.store.ApplyCheckWatch(ctx, store.ApplyCheckWatchCommand{
		Preparation: prepared, Observations: observations, IdempotencyKey: idempotencyKey,
		CorrelationID: correlationID, PersistNoop: persistNoop,
	})
}

func (s *server) observeCheckWatchCandidates(ctx context.Context, candidates []store.CheckWatchCandidate) []store.CheckWatchObservation {
	type checkoutSnapshot struct {
		repositoryID, repositoryFingerprint, objectFormat string
		repositoryRevision                                int64
		checkoutID, checkoutPath                          string
		checkoutRevision                                  int64
	}
	cache := make(map[checkoutSnapshot]domain.CheckGitObservation)
	observations := make([]store.CheckWatchObservation, 0, len(candidates))
	for _, candidate := range candidates {
		key := checkoutSnapshot{
			repositoryID: candidate.RepositoryID, repositoryRevision: candidate.RepositoryRevision,
			repositoryFingerprint: candidate.RepositoryFingerprint, objectFormat: candidate.ObjectFormat,
			checkoutID: candidate.CheckoutID, checkoutRevision: candidate.CheckoutRevision, checkoutPath: candidate.CheckoutPath,
		}
		observation, exists := cache[key]
		if !exists {
			observation = s.observeCheckGitCandidate(ctx, candidate)
			cache[key] = observation
		}
		observations = append(observations, store.CheckWatchObservation{
			CheckResultID: candidate.CheckResultID, FreshnessRevision: candidate.FreshnessRevision,
			Observation: observation,
		})
	}
	return observations
}
