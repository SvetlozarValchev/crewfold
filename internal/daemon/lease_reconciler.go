package daemon

import (
	"context"
	"fmt"
	"time"

	"crewfold/internal/store"
)

const daemonLeaseWorkspacePageLimit = 100

// startLeaseReconciler advances elapsed assignment and claim leases without
// coupling fact mutation to an operator read. Each workspace mutation remains
// transactional and idempotent: already-expired records cannot emit again.
func (s *server) startLeaseReconciler() {
	if s.config.DisableLeaseReconciler {
		return
	}
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		s.runLeaseReconciliationSweep(s.leaseReconcileCtx)
		ticker := time.NewTicker(s.config.LeaseReconcileInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.leaseReconcileCtx.Done():
				return
			case <-ticker.C:
				s.runLeaseReconciliationSweep(s.leaseReconcileCtx)
			}
		}
	}()
}

func (s *server) runLeaseReconciliationSweep(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	page, err := s.store.ListWorkspaces(ctx, store.ListWorkspacesQuery{
		Cursor: s.leaseReconcileCursor, Limit: daemonLeaseWorkspacePageLimit,
	})
	if err != nil {
		s.config.Logger.Error("lease reconciler could not enumerate workspaces", "component", "lease_reconciler", "error", err)
		return
	}
	if page.HasMore {
		s.leaseReconcileCursor = page.NextCursor
	} else {
		s.leaseReconcileCursor = ""
	}
	for _, workspace := range page.Workspaces {
		if ctx.Err() != nil {
			return
		}
		pass := s.leaseReconcilePass.Add(1)
		correlationID := fmt.Sprintf("daemon-lease-%d-%d-%s", s.startedAt.UnixNano(), pass, workspace.ID)
		assignments, assignmentErr := s.store.ReconcileExpiredAssignments(ctx, workspace.ID, correlationID+"-assignments")
		if assignmentErr != nil {
			s.config.Logger.Error("assignment lease reconciliation failed", "component", "lease_reconciler", "workspace_id", workspace.ID, "error", assignmentErr)
			continue
		}
		claims, claimErr := s.store.ReconcileExpiredClaims(ctx, workspace.ID, correlationID+"-claims")
		if claimErr != nil {
			s.config.Logger.Error("claim lease reconciliation failed", "component", "lease_reconciler", "workspace_id", workspace.ID, "error", claimErr)
			continue
		}
		if assignments != 0 || claims != 0 {
			s.config.Logger.Info("elapsed leases reconciled", "component", "lease_reconciler", "workspace_id", workspace.ID,
				"assignments", assignments, "claims", claims)
		}
	}
}
