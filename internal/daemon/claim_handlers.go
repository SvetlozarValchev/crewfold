package daemon

import (
	"context"
	"strings"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
	"crewfold/internal/store"
)

func (s *server) handleClaimAdd(request localapi.Request) localapi.Response {
	var params localapi.ClaimAddParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "claim.add requires workspace, project, task, kind, target, lease_seconds, and idempotency_key")
	}
	if params.LeaseSeconds < 1 || params.LeaseSeconds > int64((30*24*time.Hour)/time.Second) {
		return invalidParamsResponse(request, "claim.add lease_seconds must be between 1 and 2592000")
	}
	command := store.AddClaimCommand{
		WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, TaskID: params.Task,
		CheckoutIdentifier: params.Checkout, Kind: params.Kind, Target: params.Target, Mode: params.Mode,
		ConflictPolicy: params.ConflictPolicy, LeaseDuration: time.Duration(params.LeaseSeconds) * time.Second,
		IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	}
	// Serialize the replay check with Git observation and claim commit. The data
	// directory admits only one daemon, so this closes both lost-response and
	// concurrent duplicate-request windows without broadening Store authority.
	s.claimAddMu.Lock()
	defer s.claimAddMu.Unlock()
	if replay, found, err := s.store.ReplayAddClaim(context.Background(), command); err != nil {
		return storeErrorResponse(request, err)
	} else if found {
		return claimMutationResponse(request, replay)
	}
	var baselineObservation *domain.CheckoutObservation
	if params.Kind == domain.ClaimKindPath {
		inspection, err := s.store.InspectProject(context.Background(), params.Workspace, params.Project)
		if err != nil {
			return storeErrorResponse(request, err)
		}
		var selected *domain.Checkout
		for index := range inspection.Checkouts {
			checkout := &inspection.Checkouts[index]
			if checkout.Availability != domain.CheckoutAvailable || checkout.WriteMode == domain.WriteModeReadOnly {
				continue
			}
			if params.Checkout != "" && params.Checkout != checkout.ID && params.Checkout != checkout.Path {
				continue
			}
			if selected != nil && params.Checkout == "" {
				selected = nil
				break
			}
			selected = checkout
		}
		if selected != nil {
			observation, err := s.gitInspector.Inspect(context.Background(), selected.Path)
			if err != nil {
				return gitErrorResponse(request, err)
			}
			if _, err := s.store.ApplyCheckoutObservations(context.Background(), params.Workspace, params.Project, boundedCorrelation(request.ID, "baseline"), map[string]domain.CheckoutObservation{selected.ID: observation}); err != nil {
				return storeErrorResponse(request, err)
			}
			baselineObservation = &observation
		}
	}
	result, err := s.store.AddClaim(context.Background(), command)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	if baselineObservation != nil && !result.Replayed {
		if _, err := s.store.RecordCheckoutClaimScan(context.Background(), store.RecordCheckoutClaimScanCommand{
			CheckoutID: result.Claim.CheckoutID, WatcherID: s.claimWatcherID, HeadCommit: baselineObservation.HeadCommit,
			DirtyPaths: baselineObservation.DirtyPaths, CorrelationID: boundedCorrelation(request.ID, "initial-scan"),
		}); err != nil {
			s.config.Logger.Warn("initial claim watcher scan failed", "component", "claim_watcher", "claim_id", result.Claim.ID, "error", err)
		}
	}
	return claimMutationResponse(request, result)
}

func claimMutationResponse(request localapi.Request, result store.ClaimMutationResult) localapi.Response {
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ClaimMutationResult{
		Schema: localapi.ClaimMutationSchema, Type: "claim_mutation", Claim: result.Claim,
		Overlaps: result.Overlaps, Warnings: result.Warnings, EventSequence: result.EventSequence,
	})
}

func boundedCorrelation(base, suffix string) string {
	value := base + "-" + suffix
	if len(value) <= 128 {
		return value
	}
	return base[:128-len(suffix)-1] + "-" + suffix
}

func (s *server) handleClaimList(request localapi.Request) localapi.Response {
	var params localapi.ClaimListParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "claim.list requires workspace and optional project/status")
	}
	page, err := s.store.ListClaims(context.Background(), store.ListClaimsQuery{WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, Status: params.Status, Cursor: params.Cursor, Limit: params.Limit})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ClaimListResult{Schema: localapi.ClaimListSchema, Type: "claim_list", Claims: page.Claims, PageResult: localapi.PageResult{NextCursor: page.NextCursor, HasMore: page.HasMore, Total: page.Total}})
}

func (s *server) handleClaimRelease(request localapi.Request) localapi.Response {
	var params localapi.ClaimReleaseParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "claim.release requires workspace, claim, expected_revision, and idempotency_key")
	}
	result, err := s.store.ReleaseClaim(context.Background(), store.ReleaseClaimCommand{
		WorkspaceIdentifier: params.Workspace, ClaimID: params.Claim, ExpectedRevision: params.ExpectedRevision,
		IdempotencyKey: params.IdempotencyKey, CorrelationID: request.ID,
	})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.ClaimMutationResult{
		Schema: localapi.ClaimMutationSchema, Type: "claim_mutation", Claim: result.Claim,
		Overlaps: result.Overlaps, Warnings: result.Warnings, EventSequence: result.EventSequence,
	})
}

func (s *server) handleOverlapList(request localapi.Request) localapi.Response {
	var params localapi.OverlapListParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "overlap.list requires workspace and optional project/status")
	}
	page, err := s.store.ListOverlaps(context.Background(), store.ListOverlapsQuery{WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, Status: params.Status, Cursor: params.Cursor, Limit: params.Limit})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.OverlapListResult{Schema: localapi.OverlapListSchema, Type: "overlap_list", Overlaps: page.Overlaps, PageResult: localapi.PageResult{NextCursor: page.NextCursor, HasMore: page.HasMore, Total: page.Total}})
}

func (s *server) handleOverlapInspect(request localapi.Request) localapi.Response {
	var params localapi.OverlapInspectParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || !validCanonicalEntityID(params.Overlap, "overlap_") {
		return invalidParamsResponse(request, "overlap.inspect requires workspace and overlap")
	}
	overlap, err := s.store.Overlap(context.Background(), params.Workspace, params.Overlap)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.OverlapInspectResult{Schema: localapi.OverlapInspectSchema, Type: "overlap", Overlap: overlap})
}

func (s *server) handleOverlapScan(request localapi.Request) localapi.Response {
	var params localapi.OverlapScanParams
	if err := decodeParams(request.Params, &params); err != nil || strings.TrimSpace(params.Workspace) == "" || params.Project != "" && strings.TrimSpace(params.Project) == "" {
		return invalidParamsResponse(request, "overlap.scan requires workspace and optional project")
	}
	workspace, err := s.store.Workspace(context.Background(), params.Workspace)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	projectID := ""
	if params.Project != "" {
		inspection, err := s.store.InspectProject(context.Background(), workspace.ID, params.Project)
		if err != nil {
			return storeErrorResponse(request, err)
		}
		projectID = inspection.Project.ID
	}
	scans, issues := s.runClaimScan(context.Background(), workspace.ID, projectID, request.ID)
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.OverlapScanResult{Schema: localapi.OverlapScanSchema, Type: "overlap_scan", Scans: scans, Issues: issues})
}

func (s *server) handleDriftList(request localapi.Request) localapi.Response {
	var params localapi.DriftListParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "drift.list requires workspace and optional status")
	}
	page, err := s.store.ListClaimDrifts(context.Background(), store.ListClaimDriftsQuery{WorkspaceIdentifier: params.Workspace, ProjectIdentifier: params.Project, Status: params.Status, Cursor: params.Cursor, Limit: params.Limit})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.DriftListResult{Schema: localapi.DriftListSchema, Type: "drift_list", Drifts: page.Drifts, PageResult: localapi.PageResult{NextCursor: page.NextCursor, HasMore: page.HasMore, Total: page.Total}})
}
