package daemon

import (
	"context"
	"fmt"
	"time"

	"crewfold/internal/store"
)

const daemonSupervisorPassLimit = 100
const daemonSupervisorWorkspacePageLimit = 100

// startSupervisor reconciles enabled workspace policies without requiring an
// owner to poll the supervisor API. Every pass calls the same bounded,
// transactional Store operation exposed by supervisor.run; durable policy,
// intent, action, approval, and scheduling state therefore survive restarts.
func (s *server) startSupervisor() {
	if s.config.DisableSupervisor {
		return
	}
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		s.runSupervisorSweep(context.Background())
		ticker := time.NewTicker(s.config.SupervisorScanInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-ticker.C:
				s.runSupervisorSweep(context.Background())
			}
		}
	}()
}

func (s *server) runSupervisorSweep(ctx context.Context) {
	workspaceIDs, err := s.store.SupervisorWorkspaceIDs(ctx, s.supervisorWorkspaceCursor, daemonSupervisorWorkspacePageLimit)
	if err != nil {
		s.config.Logger.Error("supervisor could not enumerate enabled policies", "component", "supervisor", "error", err)
		return
	}
	if len(workspaceIDs) == 0 && s.supervisorWorkspaceCursor != "" {
		// Wrap after the last key so newly enabled lower IDs and the beginning of
		// the deterministic order are revisited without an unbounded full sweep.
		s.supervisorWorkspaceCursor = ""
		workspaceIDs, err = s.store.SupervisorWorkspaceIDs(ctx, "", daemonSupervisorWorkspacePageLimit)
		if err != nil {
			s.config.Logger.Error("supervisor could not restart enabled policy enumeration", "component", "supervisor", "error", err)
			return
		}
	}
	if len(workspaceIDs) != 0 {
		s.supervisorWorkspaceCursor = workspaceIDs[len(workspaceIDs)-1]
	}
	if len(workspaceIDs) < daemonSupervisorWorkspacePageLimit {
		// This page reached the end; the next tick begins a fresh ordered sweep.
		s.supervisorWorkspaceCursor = ""
	}
	for _, workspaceID := range workspaceIDs {
		pass := s.supervisorPass.Add(1)
		key := fmt.Sprintf("daemon-supervisor-%d-%d-%s", s.startedAt.UnixNano(), pass, workspaceID)
		result, err := s.store.RunSupervisor(ctx, store.RunSupervisorCommand{
			WorkspaceIdentifier: workspaceID,
			Limit:               daemonSupervisorPassLimit,
			IdempotencyKey:      key,
			CorrelationID:       key,
		})
		if err != nil {
			s.config.Logger.Error("supervisor pass failed", "component", "supervisor", "workspace_id", workspaceID, "error", err)
			continue
		}
		if len(result.Actions) != 0 || len(result.ScheduledRunIDs) != 0 {
			s.config.Logger.Info("supervisor pass applied durable decisions", "component", "supervisor", "workspace_id", workspaceID,
				"actions", len(result.Actions), "scheduled_runs", len(result.ScheduledRunIDs), "event_sequence", result.EventSequence)
		}
	}
}
