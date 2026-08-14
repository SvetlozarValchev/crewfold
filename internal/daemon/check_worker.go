package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/gitstate"
	"crewfold/internal/store"
)

const (
	checkJobLease                         = 30 * time.Second
	checkIdlePollDelay                    = 250 * time.Millisecond
	checkActivePollDelay                  = 250 * time.Millisecond
	checkAdapterCallTimeout               = 5 * time.Second
	checkLaunchCallTimeout                = 10 * time.Second
	maximumCheckObservationDirtyPaths     = 256
	maximumCheckObservationPathBytes      = 1024
	maximumCheckObservationPathBytesTotal = 256 * 1024
)

func (s *server) startCheckWorker() {
	if s.config.DisableCheckWorker {
		return
	}
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		ctx := s.leaseReconcileCtx
		for ctx.Err() == nil {
			work, found, err := s.store.ClaimCheckJob(ctx, checkJobLease)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				s.config.Logger.Error("check worker could not claim work", "component", "check_worker", "error", err)
				if !s.waitForCheckWork(ctx) {
					return
				}
				continue
			}
			if !found {
				if !s.waitForCheckWork(ctx) {
					return
				}
				continue
			}
			if err := s.processCheckWork(ctx, work); err != nil {
				if ctx.Err() != nil {
					return
				}
				s.config.Logger.Error("check worker stopped after a fault barrier", "component", "check_worker", "check_run_id", work.Run.ID, "error", err)
				s.requestStop("check worker fault barrier")
				return
			}
		}
	}()
}

func (s *server) waitForCheckWork(ctx context.Context) bool {
	timer := time.NewTimer(checkIdlePollDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *server) processCheckWork(ctx context.Context, work store.CheckWork) error {
	correlationID := fmt.Sprintf("check-worker-%s-%d", work.Run.ID, work.Run.Revision)
	preparer := s.checkRuntime.(execution.RuntimeLaunchPreparer)
	statusInspector := s.checkRuntime.(execution.RuntimeStatusInspector)

	if work.Run.Status == domain.CheckRunRequested {
		workingDirectory, pathErr := resolveCheckWorkingDirectory(work.Checkout.Path, work.Definition.WorkingDirectory)
		if pathErr != nil {
			// Preparation remains deterministic for a definite pre-child path
			// failure. The Store seals this exact no-effect attempt before the
			// result is terminalized.
			workingDirectory = unresolvedCheckWorkingDirectory(work.Checkout.Path, work.Definition.WorkingDirectory)
		}
		placement, launch := checkLaunchSpec(work, workingDirectory)
		prepareContext, cancelPrepare := context.WithTimeout(ctx, checkAdapterCallTimeout)
		prepared, prepareErr := preparer.PrepareLaunch(prepareContext, work.Run.ID, placement, launch)
		cancelPrepare()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if prepareErr != nil {
			// Preparation is side-effect free and every stored definition has already
			// passed validation. Failure here is an adapter/configuration fault; do not
			// invent a terminal result without a launch receipt.
			return prepareErr
		}
		launchObservation := s.observeCheckGit(ctx, work)
		launchable := pathErr == nil
		preflightFailureCode, preflightFailureDiagnostic := "", ""
		if pathErr != nil {
			preflightFailureCode = "working_directory_invalid"
			preflightFailureDiagnostic = boundedCheckDiagnostic(pathErr.Error())
		}
		detail, err := s.store.MarkCheckStarting(ctx, store.MarkCheckStartingCommand{
			CheckRunID: work.Run.ID, OperationID: work.Run.ID, EffectiveSpecSHA256: prepared.SpecSHA256,
			EffectiveWorkingDirectory: workingDirectory, Launchable: launchable,
			PreflightFailureCode: preflightFailureCode, PreflightFailureDiagnostic: preflightFailureDiagnostic,
			Observation: launchObservation, CorrelationID: correlationID,
		})
		if err != nil {
			if store.ErrorCode(err) == store.CodeCheckCapacityDeferred {
				return s.store.DeferCheckJob(ctx, work.Run.ID, checkActivePollDelay)
			}
			return err
		}
		work.Run, work.Job, work.LaunchReceipt = detail.Run, detail.Job, detail.LaunchReceipt
		if err := s.checkWorkerBarrier("after_check_launch_receipt", work.Run); err != nil {
			return err
		}
		if work.LaunchReceipt == nil {
			return errors.New("check start committed without an immutable launch receipt")
		}
		if !work.LaunchReceipt.Launchable {
			return s.finishCheck(ctx, work, domain.CheckOutcomeStartFailed, nil, false,
				work.LaunchReceipt.PreflightFailureCode, work.LaunchReceipt.PreflightFailureDiagnostic,
				domain.RunLogs{}, correlationID)
		}
	}

	if work.Run.Status == domain.CheckRunStarting {
		if work.LaunchReceipt == nil {
			return s.finishCheck(ctx, work, domain.CheckOutcomeUnknown, nil, false, "launch_receipt_missing", "starting check has no immutable launch receipt", domain.RunLogs{}, correlationID)
		}
		if !work.LaunchReceipt.Launchable {
			return s.finishCheck(ctx, work, domain.CheckOutcomeStartFailed, nil, false,
				work.LaunchReceipt.PreflightFailureCode, work.LaunchReceipt.PreflightFailureDiagnostic,
				domain.RunLogs{}, correlationID)
		}
		if work.Run.RuntimeHandle == "" {
			// After the pre-effect receipt commits, replay uses its exact
			// canonical working directory. Requiring the checkout path to remain
			// resolvable here could strand an already-running child after a crash.
			placement, launch := checkLaunchSpec(work, work.LaunchReceipt.EffectiveWorkingDirectory)
			prepareContext, cancelPrepare := context.WithTimeout(ctx, checkAdapterCallTimeout)
			prepared, err := preparer.PrepareLaunch(prepareContext, work.Run.ID, placement, launch)
			cancelPrepare()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil || prepared.SpecSHA256 != work.LaunchReceipt.EffectiveSpecSHA256 {
				diagnostic := "runtime could not reproduce the receipted launch specification"
				if err != nil {
					diagnostic += ": " + err.Error()
				}
				return s.finishCheck(ctx, work, domain.CheckOutcomeUnknown, nil, false, "launch_spec_mismatch", diagnostic, domain.RunLogs{}, correlationID)
			}
			launchContext, cancelLaunch := context.WithTimeout(ctx, checkLaunchCallTimeout)
			binding, err := s.checkRuntime.Launch(launchContext, work.Run.ID, placement, launch)
			cancelLaunch()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				var unavailable *execution.RuntimeUnavailableError
				if errors.As(err, &unavailable) {
					return s.store.DeferCheckJob(ctx, work.Run.ID, checkActivePollDelay)
				}
				var definite *execution.StartError
				if errors.As(err, &definite) {
					return s.finishCheck(ctx, work, domain.CheckOutcomeStartFailed, nil, false, "runtime_start_failed", err.Error(), domain.RunLogs{}, correlationID)
				}
				return s.finishCheck(ctx, work, domain.CheckOutcomeUnknown, nil, false, "runtime_launch_unknown", err.Error(), domain.RunLogs{}, correlationID)
			}
			if err := s.checkWorkerBarrier("after_check_runtime_launch", work.Run); err != nil {
				return err
			}
			updated, err := s.store.RecordCheckRuntimeBinding(ctx, work.Run.ID, binding.RuntimeHandle, correlationID)
			if err != nil {
				return err
			}
			work.Run = updated
			if err := s.checkWorkerBarrier("after_check_runtime_binding", work.Run); err != nil {
				return err
			}
		} else {
			reconcileContext, cancelReconcile := context.WithTimeout(ctx, checkAdapterCallTimeout)
			_, err := s.checkRuntime.Reconcile(reconcileContext, work.Run.ID, work.Run.RuntimeHandle)
			cancelReconcile()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				var unavailable *execution.RuntimeUnavailableError
				if errors.As(err, &unavailable) {
					return s.store.DeferCheckJob(ctx, work.Run.ID, checkActivePollDelay)
				}
				return s.finishCheck(ctx, work, domain.CheckOutcomeUnknown, nil, false, "runtime_reconcile_unknown", err.Error(), domain.RunLogs{}, correlationID)
			}
		}
		detail, err := s.store.MarkCheckRunning(ctx, work.Run.ID, correlationID)
		if err != nil {
			return err
		}
		work.Run, work.Job = detail.Run, detail.Job
	}

	if work.Run.Status != domain.CheckRunRunning {
		return nil
	}
	inspectContext, cancelInspect := context.WithTimeout(ctx, checkAdapterCallTimeout)
	status, err := statusInspector.InspectStatus(inspectContext, work.Run.ID, work.Run.RuntimeHandle)
	cancelInspect()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		var unavailable *execution.RuntimeUnavailableError
		if errors.As(err, &unavailable) {
			return s.store.DeferCheckJob(ctx, work.Run.ID, checkActivePollDelay)
		}
		return s.finishCheck(ctx, work, domain.CheckOutcomeUnknown, nil, false, "runtime_inspection_unknown", err.Error(), domain.RunLogs{}, correlationID)
	}
	switch status.State {
	case execution.RuntimeStateStarting, execution.RuntimeStateRunning:
		return s.store.DeferCheckJob(ctx, work.Run.ID, checkActivePollDelay)
	case execution.RuntimeStateExited:
		if !status.ExitKnown {
			return s.finishCheckWithRuntimeLogs(ctx, work, domain.CheckOutcomeUnknown, nil, status.Forced, "exit_status_unknown", status.Diagnostic, correlationID)
		}
		outcome := domain.CheckOutcomeFailed
		if status.ExitCode == 0 {
			outcome = domain.CheckOutcomePassed
		}
		exitCode := status.ExitCode
		return s.finishCheckWithRuntimeLogs(ctx, work, outcome, &exitCode, status.Forced, "", status.Diagnostic, correlationID)
	case execution.RuntimeStateTimedOut:
		return s.finishCheckWithRuntimeLogs(ctx, work, domain.CheckOutcomeTimedOut, checkExitCode(status), status.Forced, "runtime_timeout", status.Diagnostic, correlationID)
	case execution.RuntimeStateStopped, execution.RuntimeStateUnknown:
		return s.finishCheckWithRuntimeLogs(ctx, work, domain.CheckOutcomeUnknown, checkExitCode(status), status.Forced, "runtime_outcome_unknown", status.Diagnostic, correlationID)
	default:
		return s.finishCheck(ctx, work, domain.CheckOutcomeUnknown, nil, status.Forced, "runtime_state_invalid", "runtime returned an unsupported lifecycle state", domain.RunLogs{}, correlationID)
	}
}

func checkExitCode(status execution.RuntimeStatus) *int {
	if !status.ExitKnown {
		return nil
	}
	value := status.ExitCode
	return &value
}

func checkLaunchSpec(work store.CheckWork, workingDirectory string) (domain.RunPlacement, execution.LaunchSpec) {
	placement := domain.RunPlacement{
		TaskID: work.Run.TaskID, CheckoutID: work.Run.CheckoutID, CheckoutPath: workingDirectory,
		WriteMode: work.Run.CheckoutWriteMode, Runtime: "direct", Provider: "check",
		Reasons: []string{"owner-allowlisted mechanical check"},
	}
	launch := execution.LaunchSpec{Command: &execution.CommandSpec{
		Executable: work.Definition.Executable, Arguments: append([]string(nil), work.Definition.Arguments...),
		Environment: map[string]string{}, Timeout: time.Duration(work.Definition.TimeoutMillis) * time.Millisecond,
		OutputByteLimit: work.Definition.OutputByteLimit,
	}}
	return placement, launch
}

func unresolvedCheckWorkingDirectory(checkoutPath, relative string) string {
	return filepath.Clean(filepath.Join(checkoutPath, filepath.FromSlash(relative)))
}

func resolveCheckWorkingDirectory(checkoutPath, relative string) (string, error) {
	if filepath.IsAbs(relative) || relative == "" || strings.ContainsRune(relative, '\x00') {
		return "", errors.New("check working directory must be a checkout-relative path")
	}
	root, err := filepath.EvalSymlinks(checkoutPath)
	if err != nil {
		return "", fmt.Errorf("resolve check checkout: %w", err)
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("normalize check checkout: %w", err)
	}
	candidate, err := filepath.EvalSymlinks(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return "", fmt.Errorf("resolve check working directory: %w", err)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("normalize check working directory: %w", err)
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("check working directory resolves outside its checkout")
	}
	return candidate, nil
}

func (s *server) observeCheckGit(ctx context.Context, work store.CheckWork) domain.CheckGitObservation {
	return s.observeCheckGitTarget(ctx, work.Checkout.Path, work.Repository.ID, work.Repository.Fingerprint, work.Repository.ObjectFormat, work.Checkout.ID)
}

func (s *server) observeCheckGitCandidate(ctx context.Context, candidate store.CheckWatchCandidate) domain.CheckGitObservation {
	return s.observeCheckGitTarget(ctx, candidate.CheckoutPath, candidate.RepositoryID, candidate.RepositoryFingerprint, candidate.ObjectFormat, candidate.CheckoutID)
}

func (s *server) observeCheckGitTarget(ctx context.Context, checkoutPath, repositoryID, repositoryFingerprint, objectFormat, checkoutID string) domain.CheckGitObservation {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	observation, err := s.gitInspector.Inspect(ctx, checkoutPath)
	if err != nil {
		return unavailableCheckGitObservation(repositoryID, objectFormat, checkoutID, now, gitstate.ErrorCode(err), err.Error())
	}
	if observation.Repository.Fingerprint != repositoryFingerprint || observation.Repository.ObjectFormat != objectFormat {
		return unavailableCheckGitObservation(repositoryID, objectFormat, checkoutID, now, "repository_identity_changed", "checkout repository identity changed")
	}
	if !utf8.ValidString(observation.Branch) || strings.ContainsRune(observation.Branch, '\x00') || len([]byte(observation.Branch)) > 1024 {
		return unavailableCheckGitObservation(repositoryID, objectFormat, checkoutID, now, "branch_invalid", "checkout observation contained a malformed branch name")
	}
	wantCommitBytes := 40
	if objectFormat == "sha256" {
		wantCommitBytes = 64
	}
	if len(observation.HeadCommit) != wantCommitBytes || strings.IndexFunc(observation.HeadCommit, func(value rune) bool {
		return value < '0' || value > '9' && value < 'a' || value > 'f'
	}) >= 0 {
		return unavailableCheckGitObservation(repositoryID, objectFormat, checkoutID, now, "head_commit_invalid", "checkout observation contained a malformed HEAD commit")
	}
	// Keep the canonical empty collection as [] rather than null. The receipt
	// schema and SQL mirror intentionally distinguish a typed empty path set
	// from an absent observation field.
	dirtyPaths := append([]string{}, observation.DirtyPaths...)
	if len(dirtyPaths) > maximumCheckObservationDirtyPaths {
		return unavailableCheckGitObservation(repositoryID, objectFormat, checkoutID, now, "dirty_path_bound_exceeded", "checkout observation exceeded the retained dirty-path count")
	}
	sort.Strings(dirtyPaths)
	totalBytes := 0
	for index, path := range dirtyPaths {
		clean := filepath.ToSlash(filepath.Clean(path))
		if path == "" || path == "." || path == ".." || filepath.IsAbs(path) || clean != path || strings.HasPrefix(path, "../") || strings.ContainsRune(path, '\\') || strings.ContainsRune(path, '\x00') || !utf8.ValidString(path) || len([]byte(path)) > maximumCheckObservationPathBytes || (index > 0 && dirtyPaths[index-1] == path) {
			return unavailableCheckGitObservation(repositoryID, objectFormat, checkoutID, now, "dirty_path_invalid", "checkout observation contained a non-canonical dirty path")
		}
		totalBytes += len(path)
		if totalBytes > maximumCheckObservationPathBytesTotal {
			return unavailableCheckGitObservation(repositoryID, objectFormat, checkoutID, now, "dirty_path_bound_exceeded", "checkout observation exceeded the retained dirty-path byte limit")
		}
	}
	if observation.Dirty != (len(dirtyPaths) != 0) {
		return unavailableCheckGitObservation(repositoryID, objectFormat, checkoutID, now, "dirty_state_invalid", "checkout observation dirty state disagreed with its retained path set")
	}
	return domain.CheckGitObservation{
		Available: true, RepositoryID: repositoryID, ObjectFormat: objectFormat,
		CheckoutID: checkoutID, Branch: observation.Branch, HeadCommit: observation.HeadCommit,
		Dirty: observation.Dirty, DirtyPaths: dirtyPaths, ObservedAt: now,
	}
}

func unavailableCheckGitObservation(repositoryID, objectFormat, checkoutID, observedAt, code, diagnostic string) domain.CheckGitObservation {
	return domain.CheckGitObservation{
		RepositoryID: repositoryID, ObjectFormat: objectFormat, CheckoutID: checkoutID,
		ObservedAt: observedAt, DiagnosticCode: code, Diagnostic: boundedCheckDiagnostic(diagnostic), DirtyPaths: []string{},
	}
}

func boundedCheckDiagnostic(value string) string {
	const maximum = 4096
	value = strings.ToValidUTF8(value, "?")
	if len(value) > maximum {
		value = value[:maximum]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

func (s *server) checkLogs(ctx context.Context, run domain.CheckRun) (domain.RunLogs, error) {
	if run.RuntimeHandle == "" {
		return domain.RunLogs{}, nil
	}
	logsContext, cancelLogs := context.WithTimeout(ctx, checkAdapterCallTimeout)
	logs, err := s.checkRuntime.Logs(logsContext, run.ID, run.RuntimeHandle, 0)
	cancelLogs()
	if err != nil {
		return domain.RunLogs{}, err
	}
	return logs, nil
}

func (s *server) finishCheckWithRuntimeLogs(ctx context.Context, work store.CheckWork, outcome string, exitCode *int, forced bool, diagnosticCode, diagnostic, correlationID string) error {
	logs, err := s.checkLogs(ctx, work.Run)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return s.finishCheck(ctx, work, domain.CheckOutcomeUnknown, nil, forced, "runtime_output_unknown", err.Error(), domain.RunLogs{}, correlationID)
	}
	return s.finishCheck(ctx, work, outcome, exitCode, forced, diagnosticCode, diagnostic, logs, correlationID)
}

func (s *server) finishCheck(ctx context.Context, work store.CheckWork, outcome string, exitCode *int, forced bool, diagnosticCode, diagnostic string, logs domain.RunLogs, correlationID string) error {
	artifacts := make([]store.PreparedCheckArtifact, 0, 3)
	for _, item := range []struct {
		kind string
		log  domain.CapturedLog
	}{
		{kind: domain.CheckArtifactStdout, log: logs.Stdout},
		{kind: domain.CheckArtifactStderr, log: logs.Stderr},
	} {
		if item.log.Text == "" && item.log.OmittedBytes == 0 {
			continue
		}
		artifact, err := s.store.PrepareCheckArtifact(ctx, item.kind, []byte(item.log.Text), item.log.OmittedBytes)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, artifact)
	}
	if diagnostic = boundedCheckDiagnostic(diagnostic); diagnostic != "" {
		artifact, err := s.store.PrepareCheckArtifact(ctx, domain.CheckArtifactDiagnostic, []byte(diagnostic), 0)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, artifact)
	}
	_, err := s.store.FinishCheckRun(ctx, store.FinishCheckRunCommand{
		CheckRunID: work.Run.ID, Outcome: outcome, ExitCode: exitCode, Forced: forced,
		DiagnosticCode: diagnosticCode, Diagnostic: diagnostic, TerminalObservation: s.observeCheckGit(ctx, work),
		Artifacts: artifacts, CorrelationID: correlationID,
	})
	return err
}

func (s *server) checkWorkerBarrier(stage string, run domain.CheckRun) error {
	if s.config.CheckWorkerHook == nil {
		return nil
	}
	return s.config.CheckWorkerHook(stage, run)
}
