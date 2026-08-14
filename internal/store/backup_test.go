package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
)

type scriptedBackupStep struct {
	done bool
	err  error
}

type scriptedIncrementalBackup struct {
	steps []scriptedBackupStep
	pages []int
}

func (backup *scriptedIncrementalBackup) Step(pages int) (bool, error) {
	backup.pages = append(backup.pages, pages)
	if len(backup.steps) == 0 {
		return false, errors.New("unexpected backup step")
	}
	step := backup.steps[0]
	backup.steps = backup.steps[1:]
	return step.done, step.err
}

func TestIncrementalBackupUsesFixedBatchesAndRetriesBusy(t *testing.T) {
	t.Parallel()

	backup := &scriptedIncrementalBackup{steps: []scriptedBackupStep{
		{err: sqlite3.BUSY},
		{},
		{done: true},
	}}
	if err := stepOnlineBackup(context.Background(), backup); err != nil {
		t.Fatalf("stepOnlineBackup() error = %v", err)
	}
	if len(backup.pages) != 3 {
		t.Fatalf("backup step count = %d, want 3", len(backup.pages))
	}
	for index, pages := range backup.pages {
		if pages != backupStepPages {
			t.Fatalf("backup step %d pages = %d, want %d", index, pages, backupStepPages)
		}
	}
}

func TestIncrementalBackupBusyWaitIsContextCancelable(t *testing.T) {
	t.Parallel()

	steps := make([]scriptedBackupStep, backupBusyRetries+1)
	for index := range steps {
		steps[index].err = sqlite3.BUSY
	}
	backup := &scriptedIncrementalBackup{steps: steps}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if err := stepOnlineBackup(ctx, backup); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stepOnlineBackup(canceled busy) error = %v, want deadline exceeded", err)
	}
	if len(backup.pages) >= backupBusyRetries {
		t.Fatalf("canceled backup attempted %d steps, want prompt interruption", len(backup.pages))
	}
}

func TestBackupSnapshotDoesNotBorrowTheControlPlaneConnection(t *testing.T) {
	t.Parallel()

	storage := openTestStore(t, t.TempDir(), Options{})
	if _, err := storage.InitWorkspace(context.Background(), InitWorkspaceCommand{
		Name: "personal", IdempotencyKey: "backup-dedicated-source", CorrelationID: "backup-dedicated-source",
	}); err != nil {
		t.Fatalf("InitWorkspace() error = %v", err)
	}
	held, err := storage.db.Conn(context.Background())
	if err != nil {
		t.Fatalf("hold sole control-plane connection: %v", err)
	}
	defer held.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	destination := filepath.Join(t.TempDir(), "crewfold.db")
	metadata, err := storage.BackupSnapshot(ctx, destination)
	if err != nil {
		t.Fatalf("BackupSnapshot() with control-plane connection held error = %v", err)
	}
	if metadata.Path != destination || metadata.EventHighWater != 1 || metadata.ByteSize == 0 {
		t.Fatalf("BackupSnapshot() metadata = %#v, want complete independent snapshot", metadata)
	}
}
