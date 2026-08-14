package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestM20OrdinaryRoundTripOutlivesDialDeadline(t *testing.T) {
	t.Parallel()
	socketPath, requestSeen, unblock, serverDone := startBlockedRecoveryServer(t, MethodDatabaseStatus, DatabaseStatusResult{})
	defer unblock()
	callDone := make(chan error, 1)
	go func() {
		_, err := NewClient(socketPath).DatabaseStatus(context.Background())
		callDone <- err
	}()
	waitForRecoveryTestSignal(t, requestSeen, "ordinary database-status request", 2*time.Second)
	timer := time.NewTimer(defaultDialTimeout + 250*time.Millisecond)
	defer timer.Stop()
	select {
	case err := <-callDone:
		t.Fatalf("ordinary response returned inside the %s dial deadline: %v", defaultDialTimeout, err)
	case <-timer.C:
	}
	unblock()
	select {
	case err := <-callDone:
		if err != nil {
			t.Fatalf("delayed ordinary response error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ordinary client did not consume the delayed response")
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("delayed ordinary fake server error = %v", err)
	}
}

func TestM20OrdinaryRoundTripPreservesShorterCallerWindows(t *testing.T) {
	t.Parallel()
	t.Run("context cancellation", func(t *testing.T) {
		t.Parallel()
		socketPath, requestSeen, unblock, serverDone := startBlockedRecoveryServer(t, MethodDatabaseStatus, DatabaseStatusResult{})
		defer unblock()
		ctx, cancel := context.WithCancel(context.Background())
		callDone := make(chan error, 1)
		go func() {
			_, err := NewClient(socketPath).DatabaseStatus(ctx)
			callDone <- err
		}()
		waitForRecoveryTestSignal(t, requestSeen, "ordinary cancellable request", 2*time.Second)
		cancel()
		select {
		case err := <-callDone:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("ordinary cancellation error = %v, want context.Canceled", err)
			}
		case <-time.After(time.Second):
			t.Fatal("ordinary round trip ignored caller cancellation")
		}
		unblock()
		<-serverDone
	})
	t.Run("client override", func(t *testing.T) {
		t.Parallel()
		socketPath, requestSeen, unblock, serverDone := startBlockedRecoveryServer(t, MethodDatabaseStatus, DatabaseStatusResult{})
		defer unblock()
		callDone := make(chan error, 1)
		go func() {
			_, err := NewClient(socketPath).WithTimeout(500 * time.Millisecond).DatabaseStatus(context.Background())
			callDone <- err
		}()
		waitForRecoveryTestSignal(t, requestSeen, "ordinary overridden request", 2*time.Second)
		select {
		case err := <-callDone:
			if err == nil {
				t.Fatal("ordinary client override unexpectedly accepted a blocked response")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("ordinary round trip ignored the shorter client timeout")
		}
		unblock()
		<-serverDone
	})
}

func TestM20RecoveryMaintenanceCallsOutliveDialDeadline(t *testing.T) {
	t.Parallel()
	target := "/private/backups/crewfold-cut"
	backup := BackupCreateResult{
		Schema: BackupCreateSchema,
		Type:   "backup",
		Backup: BackupSummary{
			ID: "backup_" + strings.Repeat("a", 32), Path: target, CreatedAt: "2026-08-14T12:00:00Z",
			BaselineSHA256: strings.Repeat("b", 64), EventSequence: 91,
			LogicalStateSHA256: strings.Repeat("c", 64), DatabaseSHA256: strings.Repeat("d", 64),
			ManifestSHA256: strings.Repeat("e", 64), ArtifactCount: 2, TotalBytes: 4096,
		},
	}
	tests := []struct {
		name   string
		method string
		result any
		call   func(context.Context, *Client) error
	}{
		{name: "full doctor", method: MethodSystemDoctorFull, result: m20FullDoctorResult(), call: func(ctx context.Context, client *Client) error {
			_, err := client.SystemDoctorFull(ctx)
			return err
		}},
		{name: "backup create", method: MethodBackupCreate, result: backup, call: func(ctx context.Context, client *Client) error {
			_, err := client.BackupCreate(ctx, BackupCreateParams{TargetPath: target, IdempotencyKey: "maintenance-timeout"})
			return err
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			socketPath, requestSeen, unblock, serverDone := startBlockedRecoveryServer(t, test.method, test.result)
			defer unblock()
			callDone := make(chan error, 1)
			started := time.Now()
			go func() { callDone <- test.call(context.Background(), NewClient(socketPath)) }()
			waitForRecoveryTestSignal(t, requestSeen, "maintenance request", 2*time.Second)

			timer := time.NewTimer(defaultDialTimeout + 250*time.Millisecond)
			defer timer.Stop()
			select {
			case err := <-callDone:
				t.Fatalf("%s returned inside the ordinary dial %s deadline: %v", test.method, defaultDialTimeout, err)
			case <-timer.C:
			}
			unblock()
			select {
			case err := <-callDone:
				if err != nil {
					t.Fatalf("%s delayed maintenance response error = %v", test.method, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("%s did not consume the delayed maintenance response", test.method)
			}
			if elapsed := time.Since(started); elapsed <= defaultDialTimeout {
				t.Fatalf("%s elapsed = %s, want beyond dial deadline %s", test.method, elapsed, defaultDialTimeout)
			}
			if err := <-serverDone; err != nil {
				t.Fatalf("%s delayed fake server error = %v", test.method, err)
			}
		})
	}
}

func TestM20RecoveryMaintenanceCallerCancellationWins(t *testing.T) {
	t.Parallel()
	target := "/private/backups/crewfold-cut"
	tests := []struct {
		name   string
		method string
		result any
		call   func(context.Context, *Client) error
	}{
		{name: "full doctor", method: MethodSystemDoctorFull, result: m20FullDoctorResult(), call: func(ctx context.Context, client *Client) error {
			_, err := client.SystemDoctorFull(ctx)
			return err
		}},
		{name: "backup create", method: MethodBackupCreate, result: BackupCreateResult{}, call: func(ctx context.Context, client *Client) error {
			_, err := client.BackupCreate(ctx, BackupCreateParams{TargetPath: target, IdempotencyKey: "cancel-maintenance"})
			return err
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			socketPath, requestSeen, unblock, serverDone := startBlockedRecoveryServer(t, test.method, test.result)
			defer unblock()
			ctx, cancel := context.WithCancel(context.Background())
			callDone := make(chan error, 1)
			go func() { callDone <- test.call(ctx, NewClient(socketPath)) }()
			waitForRecoveryTestSignal(t, requestSeen, "cancellable maintenance request", 2*time.Second)
			cancel()
			select {
			case err := <-callDone:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("%s cancellation error = %v, want context.Canceled", test.method, err)
				}
			case <-time.After(time.Second):
				t.Fatalf("%s ignored caller cancellation after its request was accepted", test.method)
			}
			unblock()
			<-serverDone
		})
	}
}

func TestM20RecoveryMaintenanceClientOverrideWins(t *testing.T) {
	t.Parallel()
	target := "/private/backups/crewfold-cut"
	tests := []struct {
		name   string
		method string
		result any
		call   func(context.Context, *Client) error
	}{
		{name: "full doctor", method: MethodSystemDoctorFull, result: m20FullDoctorResult(), call: func(ctx context.Context, client *Client) error {
			_, err := client.SystemDoctorFull(ctx)
			return err
		}},
		{name: "backup create", method: MethodBackupCreate, result: BackupCreateResult{}, call: func(ctx context.Context, client *Client) error {
			_, err := client.BackupCreate(ctx, BackupCreateParams{TargetPath: target, IdempotencyKey: "override-maintenance"})
			return err
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			socketPath, requestSeen, unblock, serverDone := startBlockedRecoveryServer(t, test.method, test.result)
			defer unblock()
			callDone := make(chan error, 1)
			go func() {
				callDone <- test.call(context.Background(), NewClient(socketPath).WithTimeout(500*time.Millisecond))
			}()
			waitForRecoveryTestSignal(t, requestSeen, "overridden maintenance request", 2*time.Second)
			select {
			case err := <-callDone:
				if err == nil {
					t.Fatalf("%s client override unexpectedly accepted a blocked response", test.method)
				}
			case <-time.After(2 * time.Second):
				t.Fatalf("%s ignored the shorter client timeout override", test.method)
			}
			unblock()
			<-serverDone
		})
	}
}

func startBlockedRecoveryServer(t *testing.T, method string, result any) (string, <-chan struct{}, func(), <-chan error) {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "local-api.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	requestSeen := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	serverDone := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer connection.Close()
		decoder, encoder := json.NewDecoder(connection), json.NewEncoder(connection)
		var hello Request
		if err := decoder.Decode(&hello); err != nil {
			serverDone <- err
			return
		}
		if hello.Method != MethodHello {
			serverDone <- fmt.Errorf("first method = %q, want %q", hello.Method, MethodHello)
			return
		}
		if err := encoder.Encode(MarshalResult(hello.ID, MaxProtocol, HelloResult{
			Type: "hello", SelectedProtocol: MaxProtocol, ServerMin: MinProtocol, ServerMax: MaxProtocol,
		})); err != nil {
			serverDone <- err
			return
		}
		var request Request
		if err := decoder.Decode(&request); err != nil {
			serverDone <- err
			return
		}
		if request.Method != method {
			serverDone <- fmt.Errorf("second method = %q, want %q", request.Method, method)
			return
		}
		close(requestSeen)
		<-release
		serverDone <- encoder.Encode(MarshalResult(request.ID, request.Protocol, result))
	}()
	return socketPath, requestSeen, unblock, serverDone
}

func waitForRecoveryTestSignal(t *testing.T, signal <-chan struct{}, description string, timeout time.Duration) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(timeout):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func TestM20FullDoctorClientRequiresExactOrderedCurrentCheckRegistry(t *testing.T) {
	t.Parallel()
	exact := m20FullDoctorResult()
	for _, test := range []struct {
		name    string
		result  FullDoctorResult
		wantErr bool
	}{
		{name: "exact", result: exact},
		{name: "wrong order", result: func() FullDoctorResult {
			result := m20FullDoctorResult()
			result.Checks[0], result.Checks[1] = result.Checks[1], result.Checks[0]
			return result
		}(), wantErr: true},
		{name: "ok check with issue", result: func() FullDoctorResult {
			result := m20FullDoctorResult()
			result.Checks[3].IssueCount = 1
			return result
		}(), wantErr: true},
		{name: "summary status mismatch", result: func() FullDoctorResult {
			result := m20FullDoctorResult()
			result.Status = "degraded"
			return result
		}(), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := capturePortableResultError(t, MethodSystemDoctorFull, func(client *Client) error {
				_, callErr := client.SystemDoctorFull(context.Background())
				return callErr
			}, test.result)
			if test.wantErr && err == nil {
				t.Fatalf("SystemDoctorFull(%s) accepted inconsistent result", test.name)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("SystemDoctorFull(%s) error = %v", test.name, err)
			}
		})
	}
}

func TestM20BackupCreateClientBindsTargetAndDefaultsIdempotency(t *testing.T) {
	t.Parallel()
	target := "/private/backups/crewfold-cut"
	exact := BackupCreateResult{
		Schema: BackupCreateSchema,
		Type:   "backup",
		Backup: BackupSummary{
			ID: "backup_" + strings.Repeat("a", 32), Path: target, CreatedAt: "2026-08-14T12:00:00Z",
			BaselineSHA256: strings.Repeat("b", 64), EventSequence: 91,
			LogicalStateSHA256: strings.Repeat("c", 64), DatabaseSHA256: strings.Repeat("d", 64),
			ManifestSHA256: strings.Repeat("e", 64), ArtifactCount: 2, TotalBytes: 4096,
		},
	}
	request := captureCuratorRequest(t, MethodBackupCreate, func(client *Client) error {
		_, err := client.BackupCreate(context.Background(), BackupCreateParams{TargetPath: target})
		return err
	}, exact)
	var params BackupCreateParams
	if err := json.Unmarshal(request.Params, &params); err != nil {
		t.Fatal(err)
	}
	if params.TargetPath != target || strings.TrimSpace(params.IdempotencyKey) == "" {
		t.Fatalf("backup.create params = %#v", params)
	}

	wrong := exact
	wrong.Backup.Path = "/private/backups/other-cut"
	if err := capturePortableResultError(t, MethodBackupCreate, func(client *Client) error {
		_, callErr := client.BackupCreate(context.Background(), BackupCreateParams{TargetPath: target, IdempotencyKey: "fixed"})
		return callErr
	}, wrong); err == nil {
		t.Fatal("BackupCreate accepted a result for a different target")
	}
}

func m20FullDoctorResult() FullDoctorResult {
	result := FullDoctorResult{
		Schema: FullDoctorSchema, Type: "full_doctor", Status: "ok", EventSequence: 91,
		Baseline: FullDoctorBaseline{SHA256: strings.Repeat("a", 64), InstalledSchemaSHA256: strings.Repeat("b", 64)},
		Resources: FullDoctorResources{
			DatabaseBytes: 4096, ReferencedArtifactBytes: 1024, RSSBytes: 8192,
			Goroutines: 12, OpenFDs: 9, FilesystemFreeBytes: 1 << 30,
		},
		Limits: FullDoctorLimits{BriefingClaims: 128, BriefingBytes: 65536, NodeUnresolvedRuns: 20},
		Checks: make([]FullDoctorCheck, len(FullDoctorCheckOrder())),
	}
	for index, code := range FullDoctorCheckOrder() {
		result.Checks[index] = FullDoctorCheck{
			Code: code, Status: "ok", CheckedCount: 1, Summary: code + " passed",
			Samples: []FullDoctorSample{}, Remediation: FullDoctorRemediation{Kind: "none", Command: []string{}},
		}
	}
	return result
}
