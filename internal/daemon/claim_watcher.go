package daemon

import (
	"context"
	"fmt"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/store"
)

func (s *server) startClaimWatcher() {
	if s.config.DisableClaimWatcher {
		return
	}
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		_, issues := s.runClaimScan(context.Background(), "", "", "claim-watcher-startup")
		s.logClaimScanIssues(issues)
		ticker := time.NewTicker(s.config.ClaimScanInterval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-ticker.C:
				_, issues := s.runClaimScan(context.Background(), "", "", "claim-watcher-periodic")
				s.logClaimScanIssues(issues)
			}
		}
	}()
}

func (s *server) logClaimScanIssues(issues []string) {
	for _, issue := range issues {
		s.config.Logger.Warn("claim watcher scan failed", "component", "claim_watcher", "issue", issue)
	}
}

func (s *server) runClaimScan(ctx context.Context, workspaceID, projectID, correlationPrefix string) ([]domain.CheckoutClaimScan, []string) {
	targets, err := s.store.ClaimWatchTargets(ctx)
	if err != nil {
		return []domain.CheckoutClaimScan{}, []string{err.Error()}
	}
	scans := make([]domain.CheckoutClaimScan, 0, len(targets))
	issues := make([]string, 0)
	for index, target := range targets {
		if workspaceID != "" && target.WorkspaceID != workspaceID || projectID != "" && target.ProjectID != projectID {
			continue
		}
		observation, err := s.gitInspector.Inspect(ctx, target.Path)
		if err != nil {
			issues = append(issues, fmt.Sprintf("checkout %s: %v", target.CheckoutID, err))
			continue
		}
		scan, err := s.store.RecordCheckoutClaimScan(ctx, store.RecordCheckoutClaimScanCommand{
			CheckoutID: target.CheckoutID, WatcherID: s.claimWatcherID, HeadCommit: observation.HeadCommit,
			DirtyPaths: observation.DirtyPaths, CorrelationID: boundedCorrelation(correlationPrefix, fmt.Sprintf("%d", index)),
		})
		if err != nil {
			issues = append(issues, fmt.Sprintf("checkout %s: %v", target.CheckoutID, err))
			continue
		}
		scans = append(scans, scan)
	}
	return scans, issues
}
