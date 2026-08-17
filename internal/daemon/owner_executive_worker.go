package daemon

import (
	"context"
	"time"

	"crewfold/internal/store"
)

const (
	ownerExecutivePassLimit = 2
	ownerExecutiveIdleWait  = 250 * time.Millisecond
)

func (s *server) startOwnerExecutiveWorker() {
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		for s.leaseReconcileCtx.Err() == nil {
			processed := 0
			for processed < ownerExecutivePassLimit && s.leaseReconcileCtx.Err() == nil {
				exchange, found, err := s.store.ClaimOwnerExecutiveExchange(s.leaseReconcileCtx)
				if err != nil {
					s.config.Logger.Error("owner executive exchange claim failed", "component", "owner_executive", "error", err)
					break
				}
				if !found {
					break
				}
				processed++
				if err := s.dispatchOwnerExecutiveExchange(s.leaseReconcileCtx, exchange.ID); err != nil {
					if store.ManagerInvocationTemporarilyBusy(err) {
						s.config.Logger.Info("owner executive exchange waits for the prior session or execution capacity", "component", "owner_executive", "exchange_id", exchange.ID)
						_ = s.store.DeferOwnerExecutiveExchange(s.leaseReconcileCtx, exchange.ID, err)
					} else if exchange.Attempts >= 3 {
						s.config.Logger.Error("owner executive exchange dispatch failed", "component", "owner_executive", "exchange_id", exchange.ID, "error", err)
						_ = s.store.FailOwnerExecutiveExchange(s.leaseReconcileCtx, exchange.ID, err)
					} else {
						s.config.Logger.Error("owner executive exchange dispatch failed", "component", "owner_executive", "exchange_id", exchange.ID, "error", err)
						_ = s.store.RetryOwnerExecutiveExchange(s.leaseReconcileCtx, exchange.ID, err)
					}
				}
			}
			wait := ownerExecutiveIdleWait
			if processed == ownerExecutivePassLimit {
				wait = time.Millisecond
			}
			if !waitForMessageWake(s.leaseReconcileCtx, s.ownerExecutiveSignal, wait) {
				return
			}
		}
	}()
}

func (s *server) signalOwnerExecutiveWorker() {
	if s.ownerExecutiveSignal == nil {
		return
	}
	select {
	case s.ownerExecutiveSignal <- struct{}{}:
	default:
	}
}

func (s *server) dispatchOwnerExecutiveExchange(ctx context.Context, exchangeID string) error {
	binding, err := s.store.OwnerExecutiveBindingForExchange(ctx, exchangeID)
	if err != nil {
		return err
	}
	task, err := s.store.TaskDetail(ctx, binding.WorkspaceID, binding.PlanningTaskID)
	if err != nil {
		return err
	}
	grant, err := s.store.ManagerGrant(ctx, binding.WorkspaceID, binding.ManagerGrantID)
	if err != nil {
		return err
	}
	profile, err := s.store.LaunchProfile(ctx, binding.WorkspaceID, binding.LaunchProfileID)
	if err != nil {
		return err
	}
	result, err := s.store.InvokeManager(ctx, store.InvokeManagerCommand{
		WorkspaceIdentifier: binding.WorkspaceID, ObjectiveID: binding.ObjectiveID,
		TaskID: binding.PlanningTaskID, ManagerGrantID: binding.ManagerGrantID, LaunchProfileID: binding.LaunchProfileID,
		ExpectedTaskRevision: task.Task.Revision, ExpectedGrantRevision: grant.Revision, ExpectedProfileRevision: profile.Revision,
		IdempotencyKey: "owner-executive:" + exchangeID, CorrelationID: "owner-executive:" + exchangeID,
	})
	if err != nil {
		return err
	}
	return s.store.DispatchOwnerExecutiveExchange(ctx, exchangeID, result.Detail.Run.ID)
}
