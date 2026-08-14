package daemon

import (
	"context"
	"database/sql"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/execution"
	"crewfold/internal/localapi"
)

func TestMessageWakePassIsBoundedSequentialAndYields(t *testing.T) {
	jobs := make([]domain.MessageWakeJob, messageWakePassLimit+1)
	for index := range jobs {
		jobs[index] = domain.MessageWakeJob{ID: "wake-" + string(rune('a'+index))}
	}
	claimCursor := 0
	claims := func(context.Context, time.Duration) (domain.MessageWakeJob, bool, error) {
		if claimCursor == len(jobs) {
			return domain.MessageWakeJob{}, false, nil
		}
		job := jobs[claimCursor]
		claimCursor++
		return job, true, nil
	}
	var activeEffects, maximumActiveEffects atomic.Int32
	deliveries := 0
	deliver := func(_ context.Context, _ domain.MessageWakeJob, timeout time.Duration) error {
		if timeout != messageWakeTimeout {
			t.Fatalf("delivery timeout = %s, want %s", timeout, messageWakeTimeout)
		}
		active := activeEffects.Add(1)
		for {
			maximum := maximumActiveEffects.Load()
			if active <= maximum || maximumActiveEffects.CompareAndSwap(maximum, active) {
				break
			}
		}
		deliveries++
		activeEffects.Add(-1)
		return nil
	}
	settlements := 0
	settle := func(_ context.Context, _ string, outcome, diagnostic string) error {
		if outcome != domain.WakeSucceeded || diagnostic != "" {
			t.Fatalf("settlement outcome = %q, diagnostic = %q", outcome, diagnostic)
		}
		settlements++
		return nil
	}

	processed, err := processMessageWakePass(context.Background(), claims, deliver, settle)
	if err != nil || processed != messageWakePassLimit || claimCursor != messageWakePassLimit || deliveries != messageWakePassLimit || settlements != messageWakePassLimit {
		t.Fatalf("first pass processed=%d claims=%d deliveries=%d settlements=%d error=%v", processed, claimCursor, deliveries, settlements, err)
	}
	if maximumActiveEffects.Load() != 1 {
		t.Fatalf("maximum concurrent external effects = %d, want 1", maximumActiveEffects.Load())
	}
	if wait := messageWakeWaitDuration(processed); wait != messageWakeYieldWait {
		t.Fatalf("full-pass wait = %s, want yield %s", wait, messageWakeYieldWait)
	}

	processed, err = processMessageWakePass(context.Background(), claims, deliver, settle)
	if err != nil || processed != 1 || claimCursor != len(jobs) || deliveries != len(jobs) || settlements != len(jobs) {
		t.Fatalf("second pass processed=%d claims=%d deliveries=%d settlements=%d error=%v", processed, claimCursor, deliveries, settlements, err)
	}
	if wait := messageWakeWaitDuration(processed); wait != messageWakeIdleWait {
		t.Fatalf("partial-pass wait = %s, want idle %s", wait, messageWakeIdleWait)
	}
}

func TestMessageWakeDeliveryHasDeadlineAndPassCancellationDoesNotSettle(t *testing.T) {
	var observedDeadline time.Time
	s := &server{config: Config{MessageWake: func(ctx context.Context, _ domain.MessageWakeJob) error {
		var ok bool
		observedDeadline, ok = ctx.Deadline()
		if !ok {
			t.Fatal("message wake delivery context has no deadline")
		}
		return nil
	}}}
	started := time.Now()
	if err := s.deliverMessageWake(context.Background(), domain.MessageWakeJob{ID: "deadline"}, messageWakeTimeout); err != nil {
		t.Fatalf("deliverMessageWake() error = %v", err)
	}
	if remaining := observedDeadline.Sub(started); remaining <= 4*time.Second || remaining > messageWakeTimeout+100*time.Millisecond {
		t.Fatalf("delivery deadline offset = %s, want a five-second cap", remaining)
	}

	ctx, cancel := context.WithCancel(context.Background())
	deliveryStarted := make(chan struct{})
	done := make(chan error, 1)
	var settled atomic.Bool
	go func() {
		_, err := processMessageWakePass(
			ctx,
			func(context.Context, time.Duration) (domain.MessageWakeJob, bool, error) {
				return domain.MessageWakeJob{ID: "cancelled"}, true, nil
			},
			func(ctx context.Context, _ domain.MessageWakeJob, _ time.Duration) error {
				close(deliveryStarted)
				<-ctx.Done()
				return ctx.Err()
			},
			func(context.Context, string, string, string) error {
				settled.Store(true)
				return nil
			},
		)
		done <- err
	}()
	<-deliveryStarted
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled pass error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled message wake pass did not stop")
	}
	if settled.Load() {
		t.Fatal("shutdown-cancelled external effect was durably settled")
	}
}

func TestMessageWakeSignalIsNonblockingCoalescedAndInterruptsIdleWait(t *testing.T) {
	signal := make(chan struct{}, 1)
	s := &server{messageWakeSignal: signal}
	s.signalMessageWakeWorker()
	s.signalMessageWakeWorker()
	if len(signal) != 1 {
		t.Fatalf("coalesced signal count = %d, want 1", len(signal))
	}

	started := time.Now()
	if !waitForMessageWake(context.Background(), signal, time.Second) {
		t.Fatal("signaled wait stopped")
	}
	if elapsed := time.Since(started); elapsed >= 100*time.Millisecond {
		t.Fatalf("signaled wait took %s, want prompt wake-up", elapsed)
	}

	nilServer := &server{}
	nilServer.signalMessageWakeWorker()
}

func TestMessageWakeHandlersAndStartupRemainResponsiveDuringExternalEffect(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("Git is unavailable: %v", err)
	}

	fixtureRoot := t.TempDir()
	createGitFixture(t, fixtureRoot)
	config := testConfig(t)
	firstStarted, firstCanceled := make(chan struct{}), make(chan struct{})
	var firstStartOnce, firstCancelOnce sync.Once
	config.MessageWake = func(ctx context.Context, _ domain.MessageWakeJob) error {
		firstStartOnce.Do(func() { close(firstStarted) })
		<-ctx.Done()
		firstCancelOnce.Do(func() { close(firstCanceled) })
		return ctx.Err()
	}

	first := startTestServer(t, config)
	client := localapi.NewClient(config.SocketPath)
	project, agent := initializeRunWorkerAPI(t, client, fixtureRoot)
	task := createAssignedRunWorkerTask(t, client, project.Project.ID, agent.Agent.ID, "message wake responsiveness")
	scenario := domain.FakeScenario{Schema: execution.FakeScenarioSchema, Name: "message-wake-responsiveness", Steps: []domain.FakeStep{{Kind: domain.ObservationBlocked, Message: "waiting for mailbox"}}}
	startedRun, err := client.RunStart(context.Background(), localapi.RunStartParams{
		Workspace: "personal", Task: task.Detail.Task.ID, Runtime: "fake", Provider: "fake", Scenario: scenario,
		ExpectedTaskRevision: task.Detail.Task.Revision, IdempotencyKey: "start-message-wake-responsiveness",
	})
	if err != nil {
		t.Fatalf("RunStart() error = %v", err)
	}
	waitForRunStatus(t, client, startedRun.Detail.Run.ID, domain.RunBlocked)

	sendContext, cancelSend := context.WithTimeout(context.Background(), time.Second)
	firstMessage, err := client.MessageSend(sendContext, localapi.MessageSendParams{
		Workspace: "personal", RecipientAgent: agent.Agent.ID, Project: project.Project.ID,
		Kind: domain.MessageInform, Body: "first durable wake", ArtifactIDs: []string{}, IdempotencyKey: "first-durable-wake",
	})
	cancelSend()
	if err != nil || firstMessage.Mutation.Recipient.WakeStatus != domain.WakePending {
		t.Fatalf("MessageSend(first) = %#v, %v", firstMessage, err)
	}
	select {
	case <-firstStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("message wake worker did not start external effect")
	}
	statusContext, cancelStatus := context.WithTimeout(context.Background(), 250*time.Millisecond)
	if _, err := client.Status(statusContext); err != nil {
		t.Fatalf("Status() while wake effect blocked error = %v", err)
	}
	cancelStatus()

	// The first effect keeps the only delivery lane occupied, so this second
	// request can only enqueue durable work; it cannot invoke an inline effect.
	secondContext, cancelSecond := context.WithTimeout(context.Background(), time.Second)
	secondMessage, err := client.MessageSend(secondContext, localapi.MessageSendParams{
		Workspace: "personal", RecipientAgent: agent.Agent.ID, Project: project.Project.ID,
		Kind: domain.MessageInform, Body: "pending across restart", ArtifactIDs: []string{}, IdempotencyKey: "second-durable-wake",
	})
	cancelSecond()
	if err != nil || secondMessage.Mutation.Recipient.WakeStatus != domain.WakePending {
		t.Fatalf("MessageSend(second) = %#v, %v", secondMessage, err)
	}
	if _, err := client.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(first) error = %v", err)
	}
	select {
	case <-firstCanceled:
	case <-time.After(time.Second):
		t.Fatal("daemon shutdown did not cancel the external wake effect")
	}
	select {
	case <-first.done:
		if first.err != nil {
			t.Fatalf("Run(first) error = %v", first.err)
		}
	case <-time.After(time.Second):
		t.Fatal("daemon shutdown waited on the cancelled wake effect")
	}
	database, err := sql.Open("sqlite3", filepath.Join(config.DataDir, "crewfold.db"))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := database.Exec(`UPDATE message_wake_jobs
SET lease_expires_at = '2000-01-01T00:00:00Z'
WHERE status = 'leased'`); err != nil {
		database.Close()
		t.Fatalf("expire interrupted wake lease error = %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("database.Close() error = %v", err)
	}

	secondStarted, secondCanceled := make(chan struct{}), make(chan struct{})
	var secondStartOnce, secondCancelOnce sync.Once
	config.MessageWake = func(ctx context.Context, _ domain.MessageWakeJob) error {
		secondStartOnce.Do(func() { close(secondStarted) })
		<-ctx.Done()
		secondCancelOnce.Do(func() { close(secondCanceled) })
		return ctx.Err()
	}
	restartBegan := time.Now()
	second := startTestServer(t, config)
	if elapsed := time.Since(restartBegan); elapsed >= time.Second {
		t.Fatalf("listener readiness waited %s for pending wake effect", elapsed)
	}
	restarted := localapi.NewClient(config.SocketPath)
	select {
	case <-secondStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("pending wake was not picked up after restart")
	}
	inbox, err := restarted.InboxList(context.Background(), "personal", agent.Agent.ID, 20)
	if err != nil {
		t.Fatalf("InboxList(after wake recovery) error = %v", err)
	}
	foundFailedUnknown := false
	for _, item := range inbox.Items {
		if item.Message.ID != firstMessage.Mutation.Message.ID {
			continue
		}
		foundFailedUnknown = item.Delivery.WakeStatus == domain.WakeFailedUnknown && strings.Contains(item.Delivery.WakeDiagnostic, "outcome is unknown")
	}
	if !foundFailedUnknown {
		t.Fatalf("InboxList() did not publish failed_unknown diagnosis for interrupted wake: %#v", inbox.Items)
	}
	if _, err := restarted.Status(context.Background()); err != nil {
		t.Fatalf("Status(after restart with blocked wake) error = %v", err)
	}
	if _, err := restarted.Stop(context.Background()); err != nil {
		t.Fatalf("Stop(second) error = %v", err)
	}
	select {
	case <-secondCanceled:
	case <-time.After(time.Second):
		t.Fatal("second shutdown did not cancel the external wake effect")
	}
	if err := second.wait(); err != nil {
		t.Fatalf("Run(second) error = %v", err)
	}
}
