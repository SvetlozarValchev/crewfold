package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"golang.org/x/sys/unix"
)

const (
	backupStepPages      = 256
	backupBusyRetries    = 50
	backupBusyRetryDelay = 10 * time.Millisecond
)

var errOnlineBackupBusy = errors.New("online SQLite backup remained busy")

func (s *Store) BackupSnapshot(ctx context.Context, destination string) (SnapshotMetadata, error) {
	destination, err := filepath.Abs(destination)
	if err != nil || filepath.Clean(destination) != destination || destination == s.path {
		return SnapshotMetadata{}, &Error{Code: CodeStorageFailed, Message: "backup snapshot destination is invalid", Cause: err}
	}
	parent, err := os.Lstat(filepath.Dir(destination))
	if err != nil || !parent.IsDir() || parent.Mode()&os.ModeSymlink != 0 {
		return SnapshotMetadata{}, &Error{Code: CodeStorageFailed, Message: "backup snapshot parent must be an existing non-symlink directory", Cause: err}
	}
	placeholder, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return SnapshotMetadata{}, storageFailure("create backup snapshot destination", err)
	}
	if err := placeholder.Close(); err != nil {
		_ = os.Remove(destination)
		return SnapshotMetadata{}, storageFailure("close backup snapshot placeholder", err)
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		_ = os.Remove(destination)
		_ = os.Remove(destination + "-wal")
		_ = os.Remove(destination + "-shm")
	}()

	source, err := driver.Open(liveBackupSourceDSN(s.path), registerSQLiteExtensions)
	if err != nil {
		return SnapshotMetadata{}, storageFailure("open dedicated SQLite backup source", err)
	}
	source.SetMaxOpenConns(1)
	source.SetMaxIdleConns(0)
	defer source.Close()
	if err := source.PingContext(ctx); err != nil {
		return SnapshotMetadata{}, storageFailure("connect dedicated SQLite backup source", err)
	}
	connection, err := source.Conn(ctx)
	if err != nil {
		return SnapshotMetadata{}, storageFailure("acquire dedicated SQLite backup connection", err)
	}
	err = connection.Raw(func(raw any) (backupErr error) {
		sqliteConnection, ok := raw.(driver.Conn)
		if !ok {
			return fmt.Errorf("SQLite driver connection does not expose online backup")
		}
		rawConnection := sqliteConnection.Raw()
		oldInterrupt := rawConnection.SetInterrupt(ctx)
		defer rawConnection.SetInterrupt(oldInterrupt)
		backup, err := rawConnection.BackupInit("main", sqliteFileURI(destination))
		if err != nil {
			return err
		}
		defer func() {
			backupErr = errors.Join(backupErr, backup.Close())
		}()
		return stepOnlineBackup(ctx, backup)
	})
	closeErr := connection.Close()
	if err != nil {
		if errors.Is(err, errOnlineBackupBusy) {
			return SnapshotMetadata{}, &Error{Code: CodeDatabaseBusy, Message: "SQLite backup source remained busy; retry the snapshot", Cause: err}
		}
		return SnapshotMetadata{}, storageFailure("create online SQLite backup", err)
	}
	if closeErr != nil {
		return SnapshotMetadata{}, storageFailure("release SQLite backup connection", closeErr)
	}
	if err := syncRegularFile(destination); err != nil {
		return SnapshotMetadata{}, storageFailure("sync SQLite backup snapshot", err)
	}

	identity, eventHighWater, err := inspectSnapshotIdentity(ctx, destination)
	if err != nil {
		return SnapshotMetadata{}, err
	}
	byteSize, digest, err := regularFileSHA256(ctx, destination)
	if err != nil {
		return SnapshotMetadata{}, storageFailure("hash SQLite backup snapshot", err)
	}
	committed = true
	return SnapshotMetadata{
		Path: destination, ByteSize: byteSize, SHA256: digest,
		Baseline: identity, EventHighWater: eventHighWater,
	}, nil
}

type incrementalBackup interface {
	Step(int) (bool, error)
}

func stepOnlineBackup(ctx context.Context, backup incrementalBackup) error {
	busyRetries := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		done, err := backup.Step(backupStepPages)
		if err == nil {
			busyRetries = 0
			if done {
				return nil
			}
			runtime.Gosched()
			continue
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if !errors.Is(err, sqlite3.BUSY) && !errors.Is(err, sqlite3.LOCKED) {
			return err
		}
		busyRetries++
		if busyRetries > backupBusyRetries {
			return errors.Join(errOnlineBackupBusy, err)
		}
		timer := time.NewTimer(backupBusyRetryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// VerifyDatabaseSnapshot opens an exact SQLite snapshot read-only and immutable.
// It never enables WAL, creates schema, or mutates the snapshot or its directory.
func VerifyDatabaseSnapshot(ctx context.Context, path string, options CanonicalVerifyOptions) (CanonicalIntegrityReport, error) {
	path, err := filepath.Abs(path)
	if err != nil || filepath.Clean(path) != path {
		return CanonicalIntegrityReport{}, &Error{Code: CodeStorageFailed, Message: "database snapshot path is invalid", Cause: err}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return CanonicalIntegrityReport{}, &Error{Code: CodeStorageFailed, Message: "database snapshot must be an exact 0600 regular file", Cause: err}
	}
	return verifyDatabaseSnapshotLocation(ctx, path, options)
}

// VerifyDatabaseSnapshotFile verifies the already-open snapshot inode. The
// descriptor lets recovery code hold a no-follow, beneath-opened file across
// hashing and SQLite verification without reopening a replaceable pathname.
func VerifyDatabaseSnapshotFile(ctx context.Context, file *os.File, options CanonicalVerifyOptions) (CanonicalIntegrityReport, error) {
	if file == nil {
		return CanonicalIntegrityReport{}, &Error{Code: CodeStorageFailed, Message: "database snapshot descriptor is missing"}
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o777 != 0o600 {
		return CanonicalIntegrityReport{}, &Error{Code: CodeStorageFailed, Message: "database snapshot descriptor must be an exact 0600 regular file", Cause: err}
	}
	duplicate, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return CanonicalIntegrityReport{}, storageFailure("duplicate database snapshot descriptor", err)
	}
	duplicateFile := os.NewFile(uintptr(duplicate), "crewfold recovery snapshot")
	defer duplicateFile.Close()
	return verifyDatabaseSnapshotLocation(ctx, fmt.Sprintf("/proc/self/fd/%d", duplicate), options)
}

// VerifyDatabaseRecoveryCopy opens a private disposable DB/WAL/SHM copy in
// read-write mode so SQLite may perform recovery on that copy before the same
// canonical verifier used by doctor and backup runs. Callers must never pass a
// selected live/source database path to this function.
func VerifyDatabaseRecoveryCopy(ctx context.Context, path string, options CanonicalVerifyOptions) (CanonicalIntegrityReport, error) {
	path, err := filepath.Abs(path)
	if err != nil || filepath.Clean(path) != path {
		return CanonicalIntegrityReport{}, &Error{Code: CodeStorageFailed, Message: "database recovery-copy path is invalid", Cause: err}
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 {
		return CanonicalIntegrityReport{}, &Error{Code: CodeStorageFailed, Message: "database recovery copy must be an exact 0600 regular file", Cause: err}
	}
	storage, err := openStoreFile(ctx, path, Options{})
	if err != nil {
		return CanonicalIntegrityReport{}, err
	}
	report, verifyErr := storage.VerifyCanonical(ctx, options)
	closeErr := storage.Close()
	if verifyErr != nil {
		return report, verifyErr
	}
	if closeErr != nil {
		return report, storageFailure("close database recovery copy", closeErr)
	}
	return report, nil
}

func verifyDatabaseSnapshotLocation(ctx context.Context, path string, options CanonicalVerifyOptions) (CanonicalIntegrityReport, error) {
	database, err := driver.Open(readOnlyDatabaseDSN(path), registerSQLiteExtensions)
	if err != nil {
		return CanonicalIntegrityReport{}, storageFailure("open database snapshot read-only", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	defer database.Close()
	if err := database.PingContext(ctx); err != nil {
		return CanonicalIntegrityReport{}, storageFailure("connect to database snapshot read-only", err)
	}
	storage := &Store{db: database, path: path, clock: time.Now}
	return storage.VerifyCanonical(ctx, options)
}

func inspectSnapshotIdentity(ctx context.Context, path string) (BaselineIdentity, int64, error) {
	database, err := sql.Open("sqlite3", readOnlyDatabaseDSN(path))
	if err != nil {
		return BaselineIdentity{}, 0, storageFailure("open SQLite backup for identity verification", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(0)
	defer database.Close()
	if err := database.PingContext(ctx); err != nil {
		return BaselineIdentity{}, 0, storageFailure("connect to SQLite backup for identity verification", err)
	}
	identity, err := verifyBaselineIdentity(ctx, database)
	if err != nil {
		return BaselineIdentity{}, 0, err
	}
	result, err := runSQLiteQuickCheck(ctx, database)
	if err != nil {
		return BaselineIdentity{}, 0, storageFailure("run backup snapshot quick check", err)
	}
	if result != "ok" {
		return BaselineIdentity{}, 0, &Error{Code: CodeStorageFailed, Message: "backup snapshot failed SQLite quick_check: " + result}
	}
	var eventHighWater int64
	if err := database.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM events").Scan(&eventHighWater); err != nil {
		return BaselineIdentity{}, 0, storageFailure("read backup snapshot event high-water", err)
	}
	return identity, eventHighWater, nil
}

func sqliteFileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func readOnlyDatabaseDSN(path string) string {
	location := &url.URL{Scheme: "file", Path: path}
	query := location.Query()
	query.Set("mode", "ro")
	query.Set("immutable", "1")
	location.RawQuery = query.Encode()
	return location.String()
}

func liveBackupSourceDSN(path string) string {
	location := &url.URL{Scheme: "file", Path: path}
	query := location.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "busy_timeout(0)")
	query.Add("_pragma", "query_only(ON)")
	location.RawQuery = query.Encode()
	return location.String()
}

func syncRegularFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		if err != nil {
			return err
		}
		return errors.New("snapshot is not a regular file")
	}
	return file.Sync()
}

func regularFileSHA256(ctx context.Context, path string) (int64, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		if err != nil {
			return 0, "", err
		}
		return 0, "", errors.New("path is not a regular file")
	}
	digest := sha256.New()
	buffer := make([]byte, 128<<10)
	for {
		if err := ctx.Err(); err != nil {
			return 0, "", err
		}
		read, readErr := file.Read(buffer)
		if read > 0 {
			_, _ = digest.Write(buffer[:read])
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return 0, "", readErr
		}
	}
	return info.Size(), hex.EncodeToString(digest.Sum(nil)), nil
}
