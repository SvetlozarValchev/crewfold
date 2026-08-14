package loadtest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"crewfold/internal/buildinfo"
	"crewfold/internal/daemon"
	"crewfold/internal/domain"
	"crewfold/internal/localapi"
	"crewfold/internal/recovery"
	"crewfold/internal/store"
)

type personalClock struct {
	nanoseconds atomic.Int64
}

func newPersonalClock(value time.Time) *personalClock {
	clock := &personalClock{}
	clock.nanoseconds.Store(value.UTC().UnixNano())
	return clock
}

func (clock *personalClock) Now() time.Time {
	return time.Unix(0, clock.nanoseconds.Load()).UTC()
}

func (clock *personalClock) Advance(duration time.Duration) {
	clock.nanoseconds.Add(int64(duration))
}

type runningPersonalDaemon struct {
	client    *localapi.Client
	cancel    context.CancelFunc
	done      chan error
	socketDir string
	stopOnce  sync.Once
	stopErr   error
}

func startPersonalDaemon(ctx context.Context, root string, clock *personalClock, reconcile bool, wake func(context.Context, domain.MessageWakeJob) error) (*runningPersonalDaemon, time.Duration, error) {
	// Unix socket addresses are much shorter than ordinary filesystem paths and
	// t.TempDir roots can exceed that limit. Own a second private, short-lived
	// directory solely for the socket and remove it with the daemon.
	socketDir, err := os.MkdirTemp("", "crewfold-load-sock-")
	if err != nil {
		return nil, 0, fmt.Errorf("create personal-100 socket directory: %w", err)
	}
	if err := os.Chmod(socketDir, 0o700); err != nil {
		_ = os.RemoveAll(socketDir)
		return nil, 0, fmt.Errorf("make personal-100 socket directory private: %w", err)
	}
	cleanupSocket := func() { _ = os.RemoveAll(socketDir) }
	socketPath := filepath.Join(socketDir, "daemon.sock")
	config := daemon.Config{
		DataDir: root, SocketPath: socketPath, Version: buildinfo.Current(),
		Logger:                 slog.New(slog.NewJSONHandler(io.Discard, nil)),
		StoreOptions:           store.Options{Clock: clock.Now},
		DisableRunWorker:       true,
		DisableCheckWorker:     true,
		DisableCheckWatcher:    true,
		DisableClaimWatcher:    true,
		DisableSupervisor:      true,
		DisableLeaseReconciler: !reconcile,
		LeaseReconcileInterval: 20 * time.Millisecond,
		MessageWake:            wake,
	}
	daemonContext, cancel := context.WithCancel(ctx)
	running := &runningPersonalDaemon{cancel: cancel, done: make(chan error, 1), socketDir: socketDir}
	started := time.Now()
	go func() {
		running.done <- daemon.Run(daemonContext, config)
		close(running.done)
	}()
	probeClient := localapi.NewClient(socketPath).WithTimeout(250 * time.Millisecond)
	deadline := time.Now().Add(10 * time.Second)
	for {
		probeContext, cancelProbe := context.WithTimeout(ctx, 250*time.Millisecond)
		status, statusErr := probeClient.Status(probeContext)
		cancelProbe()
		if statusErr == nil && status.Status == "ok" {
			running.client = localapi.NewClient(socketPath).WithTimeout(65 * time.Second)
			return running, time.Since(started), nil
		}
		select {
		case runErr := <-running.done:
			cancel()
			cleanupSocket()
			return nil, time.Since(started), fmt.Errorf("personal-100 daemon exited before readiness: %w", runErr)
		default:
		}
		if err := ctx.Err(); err != nil {
			cancel()
			cleanupSocket()
			return nil, time.Since(started), err
		}
		if time.Now().After(deadline) {
			cancel()
			cleanupSocket()
			return nil, time.Since(started), fmt.Errorf("personal-100 daemon did not become ready: %w", statusErr)
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			cancel()
			cleanupSocket()
			return nil, time.Since(started), ctx.Err()
		case <-timer.C:
		}
	}
}

func (running *runningPersonalDaemon) stop() error {
	if running == nil {
		return nil
	}
	running.stopOnce.Do(func() {
		if running.client != nil {
			stopContext, cancelStop := context.WithTimeout(context.Background(), 5*time.Second)
			_, running.stopErr = running.client.WithTimeout(5 * time.Second).Stop(stopContext)
			cancelStop()
		}
		running.cancel()
		select {
		case runErr := <-running.done:
			if running.stopErr == nil {
				running.stopErr = runErr
			}
		case <-time.After(10 * time.Second):
			if running.stopErr == nil {
				running.stopErr = context.DeadlineExceeded
			}
		}
		if removeErr := os.RemoveAll(running.socketDir); running.stopErr == nil && removeErr != nil {
			running.stopErr = removeErr
		}
	})
	return running.stopErr
}

func exerciseSaturatedControl(ctx context.Context, storage *store.Store, root string, state *fixtureState, metrics *measurements, clock *personalClock, proof *capacityProof) (returnedErr error) {
	releaseWake := make(chan struct{})
	wakeStarted := make(chan struct{})
	var releaseOnce sync.Once
	var startOnce sync.Once
	var wakeStartedCount atomic.Int64
	var wakeCompletedCount atomic.Int64
	release := func() { releaseOnce.Do(func() { close(releaseWake) }) }
	wake := func(effectContext context.Context, _ domain.MessageWakeJob) error {
		wakeStartedCount.Add(1)
		startOnce.Do(func() { close(wakeStarted) })
		select {
		case <-releaseWake:
			wakeCompletedCount.Add(1)
			return nil
		case <-effectContext.Done():
			return effectContext.Err()
		}
	}
	running, _, err := startPersonalDaemon(ctx, root, clock, true, wake)
	if err != nil {
		return err
	}
	defer func() {
		release()
		if stopErr := running.stop(); returnedErr == nil && stopErr != nil {
			returnedErr = fmt.Errorf("stop saturated personal-100 daemon: %w", stopErr)
		}
	}()

	beforeControl, err := eventHighWater(ctx, storage, &metrics.eventPages)
	if err != nil {
		return err
	}
	threadID := ""
	logicalProject := "project/" + state.projects[4].project.Name
	for index := 0; index < personalStatusOperations; index++ {
		statusStarted := time.Now()
		status, statusErr := running.client.Status(ctx)
		statusElapsed := time.Since(statusStarted)
		metrics.statusOperations = append(metrics.statusOperations, statusElapsed)
		metrics.controlOperations = append(metrics.controlOperations, statusElapsed)
		if statusErr != nil || status.Status != "ok" || status.ShutdownPending {
			return fmt.Errorf("read saturated personal-100 status %d: result=%#v error=%w", index, status, statusErr)
		}

		messageStarted := time.Now()
		message, messageErr := running.client.MessageSend(ctx, localapi.MessageSendParams{
			Workspace: state.workspace.ID, RecipientAgent: state.phaseAgents[4].ID, Thread: threadID,
			Project: state.projects[4].project.ID, Kind: domain.MessageInform,
			Subject: "Personal-100 saturated control", Body: fmt.Sprintf("personal-100 saturated control message %03d", index),
			ArtifactIDs: []string{}, IdempotencyKey: fmt.Sprintf("load-saturated-message-%03d", index),
		})
		messageElapsed := time.Since(messageStarted)
		metrics.messageOperations = append(metrics.messageOperations, messageElapsed)
		metrics.controlOperations = append(metrics.controlOperations, messageElapsed)
		if messageErr != nil || message.Mutation.Recipient.WakeStatus != domain.WakePending || message.EventSequence <= beforeControl {
			return fmt.Errorf("send saturated personal-100 message %d: result=%#v error=%w", index, message, messageErr)
		}
		if index == 0 {
			threadID = message.Mutation.Thread.ID
			state.controlThreadID = threadID
			state.entityKeys[entityMapKey("thread", threadID)] = logicalProject + "/control-thread"
			state.projectOwners[entityMapKey("thread", threadID)] = logicalProject
			select {
			case <-wakeStarted:
			case <-time.After(2 * time.Second):
				return fmt.Errorf("personal-100 asynchronous wake effect did not start")
			}
		} else if message.Mutation.Thread.ID != threadID {
			return fmt.Errorf("personal-100 saturated message %d escaped its fixed thread", index)
		}
		messageID := message.Mutation.Message.ID
		state.controlMessages[messageID] = index
		state.entityKeys[entityMapKey("message", messageID)] = fmt.Sprintf("%s/control-message/%03d", logicalProject, index)
		state.projectOwners[entityMapKey("message", messageID)] = logicalProject
	}
	if len(metrics.statusOperations) != personalStatusOperations || len(metrics.messageOperations) != personalMessageOperations ||
		len(metrics.controlOperations) != personalControlOperations || wakeStartedCount.Load() != 1 {
		return fmt.Errorf("personal-100 saturated control topology differs: status=%d message=%d combined=%d wake_started=%d",
			len(metrics.statusOperations), len(metrics.messageOperations), len(metrics.controlOperations), wakeStartedCount.Load())
	}
	proof.asyncWakeBlocked = 1
	release()
	if err := waitPersonalCondition(ctx, 5*time.Second, func() (bool, error) {
		detail, readErr := storage.Thread(ctx, state.workspace.ID, threadID)
		if readErr != nil {
			return false, readErr
		}
		if len(detail.Messages) != personalMessageOperations || len(detail.Recipients) != personalMessageOperations || wakeCompletedCount.Load() != personalMessageOperations {
			return false, nil
		}
		for _, delivery := range detail.Recipients {
			if delivery.Status != domain.DeliveryDelivered || delivery.WakeStatus != domain.WakeSucceeded || delivery.DeliveredRunID == "" {
				return false, nil
			}
		}
		return true, nil
	}); err != nil {
		return fmt.Errorf("drain personal-100 asynchronous message wakes: %w", err)
	}
	afterControl, err := eventHighWater(ctx, storage, &metrics.eventPages)
	if err != nil {
		return err
	}
	proof.controlEventDelta = afterControl - beforeControl
	if proof.controlEventDelta != personalControlEventDelta {
		return fmt.Errorf("personal-100 control event delta=%d, want %d", proof.controlEventDelta, personalControlEventDelta)
	}
	health, err := storage.ExecutionHealth(ctx)
	if err != nil {
		return err
	}
	if health.Node.Unresolved != 8 || health.Node.Starting != 2 || executionProviderUnresolved(health, "fixture-a") != 4 || executionProviderUnresolved(health, "fixture-b") != 4 {
		return fmt.Errorf("personal-100 control traffic changed saturated execution health: node=%#v providers=%#v", health.Node, health.Providers)
	}

	beforeReconciliation := afterControl
	clock.Advance(2 * time.Minute)
	reconciliationStarted := time.Now()
	err = waitPersonalCondition(ctx, 5*time.Second, func() (bool, error) {
		detail, readErr := storage.TaskDetail(ctx, state.workspace.ID, state.projects[9].phaseTask.ID)
		if readErr != nil {
			return false, readErr
		}
		return detail.Task.Status == domain.TaskReady && detail.Assignment == nil, nil
	})
	metrics.reconciliation = append(metrics.reconciliation, time.Since(reconciliationStarted))
	if err != nil {
		return fmt.Errorf("reconcile controlled personal-100 assignment lease: %w", err)
	}
	afterReconciliation, err := eventHighWater(ctx, storage, &metrics.eventPages)
	if err != nil {
		return err
	}
	proof.reconciliationEventDelta = afterReconciliation - beforeReconciliation
	proof.reconciliationSettled = 1
	if proof.reconciliationEventDelta != 1 {
		return fmt.Errorf("personal-100 lease reconciliation event delta=%d, want 1", proof.reconciliationEventDelta)
	}
	return nil
}

func measurePersonalRecovery(ctx context.Context, root string, clock *personalClock, canonical store.CanonicalIntegrityReport, metrics *measurements) (returnedErr error) {
	recoveryRoot := root + "-recovery"
	if err := os.Mkdir(recoveryRoot, 0o700); err != nil {
		return fmt.Errorf("create personal-100 recovery sibling: %w", err)
	}
	defer os.RemoveAll(recoveryRoot)
	running, startupElapsed, err := startPersonalDaemon(ctx, root, clock, false, nil)
	metrics.warmStartup = append(metrics.warmStartup, startupElapsed)
	if err != nil {
		return fmt.Errorf("start warm personal-100 daemon: %w", err)
	}
	defer func() {
		if stopErr := running.stop(); returnedErr == nil && stopErr != nil {
			returnedErr = fmt.Errorf("stop warm personal-100 daemon: %w", stopErr)
		}
	}()

	doctor, err := measured(&metrics.doctor, func() (localapi.FullDoctorResult, error) {
		return running.client.SystemDoctorFull(ctx)
	})
	if err != nil {
		return fmt.Errorf("run personal-100 full doctor: %w", err)
	}
	if doctor.Status != "ok" || doctor.EventSequence != personalEventCount || len(doctor.Checks) != len(localapi.FullDoctorCheckOrder()) || doctor.Resources.RSSBytes <= 0 {
		return fmt.Errorf("personal-100 full doctor result differs: status=%s event=%d checks=%d rss=%d", doctor.Status, doctor.EventSequence, len(doctor.Checks), doctor.Resources.RSSBytes)
	}
	bundlePath := filepath.Join(recoveryRoot, "personal-load-backup")
	created, err := measured(&metrics.backupCreate, func() (localapi.BackupCreateResult, error) {
		return running.client.BackupCreate(ctx, localapi.BackupCreateParams{TargetPath: bundlePath, IdempotencyKey: "personal-load-backup"})
	})
	if err != nil {
		return fmt.Errorf("create personal-100 backup: %w", err)
	}
	if created.Backup.Path != bundlePath || created.Backup.EventSequence != personalEventCount ||
		created.Backup.LogicalStateSHA256 != canonical.LogicalSHA256 || created.Backup.BaselineSHA256 != canonical.Baseline.SourceSHA256 ||
		created.Backup.ArtifactCount != int64(len(canonical.ArtifactReferences)) {
		return fmt.Errorf("personal-100 backup result differs: %#v", created.Backup)
	}
	if err := running.stop(); err != nil {
		return fmt.Errorf("stop source daemon before offline recovery operations: %w", err)
	}

	verified, err := measured(&metrics.backupVerify, func() (recovery.VerifiedBundle, error) {
		return recovery.VerifyBundle(ctx, bundlePath)
	})
	if err != nil {
		return fmt.Errorf("verify personal-100 backup offline: %w", err)
	}
	if verified.Manifest.BackupID != created.Backup.ID || verified.ManifestSHA256 != created.Backup.ManifestSHA256 ||
		verified.Manifest.EventHighWater != personalEventCount || verified.Manifest.LogicalSHA256 != canonical.LogicalSHA256 {
		return fmt.Errorf("personal-100 verified bundle differs from its online result")
	}
	restorePath := filepath.Join(recoveryRoot, "personal-load-restore")
	restored, err := measured(&metrics.backupRestore, func() (recovery.PendingRestore, error) {
		return recovery.RestorePending(ctx, bundlePath, restorePath)
	})
	if err != nil {
		return fmt.Errorf("restore personal-100 backup offline: %w", err)
	}
	if restored.Path != restorePath || restored.BackupID != verified.Manifest.BackupID ||
		restored.ManifestSHA256 != verified.ManifestSHA256 || restored.EventHighWater != personalEventCount ||
		restored.LogicalSHA256 != canonical.LogicalSHA256 {
		return fmt.Errorf("personal-100 pending restore differs from its verified bundle")
	}
	return nil
}

func waitPersonalCondition(ctx context.Context, maximum time.Duration, condition func() (bool, error)) error {
	deadline := time.Now().Add(maximum)
	var lastErr error
	for {
		if ok, err := condition(); ok {
			return nil
		} else if err != nil {
			lastErr = err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return lastErr
			}
			return context.DeadlineExceeded
		}
		timer := time.NewTimer(5 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func executionProviderUnresolved(health domain.ExecutionHealth, name string) int64 {
	for _, provider := range health.Providers {
		if provider.Provider == name {
			return provider.Unresolved
		}
	}
	return 0
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
