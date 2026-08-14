// Package store implements Crewfold's SQLite event journal and projections.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"
	"github.com/ncruces/go-sqlite3/ext/hash"
	"golang.org/x/sys/unix"
)

const databaseFilename = "crewfold.db"

const (
	// SQLite permits one writer at a time. Keeping that writer on its own
	// connection serializes mutation admission without allowing a busy writer to
	// consume every connection needed by WAL readers and control-plane status.
	sqliteWriterConnections = 1
	sqliteReaderConnections = 4
	sqliteBusyTimeout       = 5 * time.Second
)

const databaseCreationStageName = ".crewfold.db.creating-v1"

// sqliteApplicationID is the ASCII marker "CRFD" stored in SQLite's file header.
const sqliteApplicationID = 0x43524644

type Store struct {
	db                         *sql.DB
	writeDB                    *sql.DB
	path                       string
	runtimeNodeID              string
	runtimeNodeFingerprint     string
	mutationHook               func(string) error
	clock                      func() time.Time
	restoreActive              *atomic.Bool
	runLossResolutionActive    *atomic.Bool
	supervisorActionSealActive *atomic.Bool
	checkMutationSealActive    *atomic.Bool
	outcomeMutationSealActive  *atomic.Bool
	outcomeMutationMu          sync.Mutex
}

func Open(ctx context.Context, dataDir string, options Options) (*Store, error) {
	path := filepath.Join(dataDir, databaseFilename)
	exists, err := inspectDatabasePath(path)
	if err != nil {
		return nil, err
	}
	if !exists {
		if err := createAndPublishCurrentDatabase(ctx, path, options); err != nil {
			return nil, err
		}
		if _, err := inspectDatabasePath(path); err != nil {
			return nil, err
		}
	}

	storage, err := openStoreFile(ctx, path, options)
	if err != nil {
		return nil, err
	}
	fail := func(openErr error) (*Store, error) {
		_ = storage.Close()
		return nil, openErr
	}
	if _, err := verifyBaselineIdentity(ctx, storage.db); err != nil {
		return fail(err)
	}
	if err := storage.enableWAL(ctx); err != nil {
		return fail(err)
	}
	if _, err := storage.Health(ctx); err != nil {
		return fail(err)
	}
	return storage, nil
}

func openStoreFile(ctx context.Context, path string, options Options) (*Store, error) {
	dsn := databaseDSN(path)
	restoreActive := new(atomic.Bool)
	runLossResolutionActive := new(atomic.Bool)
	supervisorActionSealActive := new(atomic.Bool)
	checkMutationSealActive := new(atomic.Bool)
	outcomeMutationSealActive := new(atomic.Bool)
	initializeConnection := func(connection *sqlite3.Conn) error {
		if err := registerSQLiteExtensions(connection); err != nil {
			return err
		}
		if err := registerSQLiteSupervisorActionSealActive(connection, supervisorActionSealActive); err != nil {
			return err
		}
		if err := connection.CreateFunction("crewfold_check_mutation_seal_active", 0, sqlite3.INNOCUOUS,
			func(functionContext sqlite3.Context, _ ...sqlite3.Value) {
				if checkMutationSealActive.Load() {
					functionContext.ResultInt(1)
					return
				}
				functionContext.ResultInt(0)
			}); err != nil {
			return err
		}
		if err := connection.CreateFunction("crewfold_outcome_mutation_seal_active", 0, sqlite3.INNOCUOUS,
			func(functionContext sqlite3.Context, _ ...sqlite3.Value) {
				if outcomeMutationSealActive.Load() {
					functionContext.ResultInt(1)
					return
				}
				functionContext.ResultInt(0)
			}); err != nil {
			return err
		}
		if err := connection.CreateFunction("crewfold_run_loss_resolution_active", 0, sqlite3.INNOCUOUS,
			func(functionContext sqlite3.Context, _ ...sqlite3.Value) {
				if runLossResolutionActive.Load() {
					functionContext.ResultInt(1)
					return
				}
				functionContext.ResultInt(0)
			}); err != nil {
			return err
		}
		if err := connection.CreateFunction("crewfold_outcome_event_known", 1, sqlite3.DETERMINISTIC|sqlite3.INNOCUOUS,
			func(functionContext sqlite3.Context, arguments ...sqlite3.Value) {
				if len(arguments) == 1 && arguments[0].Type() == sqlite3.TEXT && knownOutcomeProjectorEvent(arguments[0].Text()) {
					functionContext.ResultInt(1)
					return
				}
				functionContext.ResultInt(0)
			}); err != nil {
			return err
		}
		if err := connection.CreateFunction("crewfold_outcome_claim_id", 4, sqlite3.DETERMINISTIC|sqlite3.INNOCUOUS,
			func(functionContext sqlite3.Context, arguments ...sqlite3.Value) {
				if len(arguments) != 4 || arguments[0].Type() != sqlite3.TEXT || arguments[1].Type() != sqlite3.TEXT || arguments[2].Type() != sqlite3.TEXT || arguments[3].Type() != sqlite3.TEXT {
					functionContext.ResultNull()
					return
				}
				functionContext.ResultText(outcomeBriefingClaimID(arguments[0].Text(), arguments[1].Text(), arguments[2].Text(), arguments[3].Text()))
			}); err != nil {
			return err
		}
		if err := connection.CreateFunction("crewfold_outcome_acceptance_basis_sha", 4, sqlite3.DETERMINISTIC|sqlite3.INNOCUOUS,
			func(functionContext sqlite3.Context, arguments ...sqlite3.Value) {
				if len(arguments) != 4 || arguments[0].Type() != sqlite3.TEXT || arguments[1].Type() != sqlite3.TEXT || arguments[2].Type() != sqlite3.INTEGER || arguments[3].Type() != sqlite3.INTEGER {
					functionContext.ResultNull()
					return
				}
				value, err := hashCommand("outcome.policy_acceptance", map[string]any{
					"assessment_id": arguments[0].Text(), "content_sha256": arguments[1].Text(),
					"event_sequence": arguments[2].Int64(), "state_revision": arguments[3].Int64(),
				})
				if err != nil {
					functionContext.ResultNull()
					return
				}
				functionContext.ResultText(value)
			}); err != nil {
			return err
		}
		return connection.CreateFunction("crewfold_restore_active", 0, sqlite3.INNOCUOUS,
			func(functionContext sqlite3.Context, _ ...sqlite3.Value) {
				if restoreActive.Load() {
					functionContext.ResultInt(1)
					return
				}
				functionContext.ResultInt(0)
			})
	}
	readDatabase, err := driver.Open(dsn, initializeConnection)
	if err != nil {
		return nil, &Error{Code: CodeStorageFailed, Message: "open SQLite database", Cause: err}
	}
	readDatabase.SetMaxOpenConns(sqliteReaderConnections)
	readDatabase.SetMaxIdleConns(sqliteReaderConnections)
	writeDatabase, err := driver.Open(dsn, initializeConnection)
	if err != nil {
		_ = readDatabase.Close()
		return nil, &Error{Code: CodeStorageFailed, Message: "open SQLite writer", Cause: err}
	}
	writeDatabase.SetMaxOpenConns(sqliteWriterConnections)
	writeDatabase.SetMaxIdleConns(sqliteWriterConnections)

	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	storage := &Store{db: readDatabase, writeDB: writeDatabase, path: path, mutationHook: options.MutationHook, clock: clock,
		runtimeNodeID: strings.TrimSpace(options.RuntimeNodeID), runtimeNodeFingerprint: strings.TrimSpace(options.RuntimeNodeFingerprint),
		restoreActive: restoreActive, runLossResolutionActive: runLossResolutionActive, supervisorActionSealActive: supervisorActionSealActive, checkMutationSealActive: checkMutationSealActive, outcomeMutationSealActive: outcomeMutationSealActive}
	if err := readDatabase.PingContext(ctx); err != nil {
		_ = storage.Close()
		return nil, &Error{Code: CodeStorageFailed, Message: "connect to SQLite database", Cause: err}
	}
	if err := writeDatabase.PingContext(ctx); err != nil {
		_ = storage.Close()
		return nil, &Error{Code: CodeStorageFailed, Message: "connect to SQLite writer", Cause: err}
	}
	return storage, nil
}

func registerSQLiteExtensions(connection *sqlite3.Conn) error {
	if err := fts5.Register(connection); err != nil {
		return err
	}
	if err := hash.Register(connection); err != nil {
		return err
	}
	if err := registerSQLiteTimestampKey(connection); err != nil {
		return err
	}
	if err := registerSQLiteEventClassifiers(connection); err != nil {
		return err
	}
	return registerSQLiteUTF8Valid(connection)
}

func (s *Store) enableWAL(ctx context.Context) error {
	var journalMode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		return storageFailure("enable SQLite WAL mode", err)
	}
	if strings.ToLower(journalMode) != "wal" {
		return &Error{Code: CodeStorageFailed, Message: fmt.Sprintf("SQLite returned journal mode %q instead of wal", journalMode)}
	}
	return nil
}

func (s *Store) Close() error {
	var writeErr error
	if s.writeDB != nil {
		writeErr = s.writeDB.Close()
	}
	readErr := s.db.Close()
	return errors.Join(writeErr, readErr)
}

// beginTx routes all mutation transactions through the single writer pool while
// leaving read-only snapshots on the bounded reader pool. The split is what lets
// WAL reads remain responsive when another SQLite writer holds the database.
func (s *Store) beginTx(ctx context.Context, options *sql.TxOptions) (*sql.Tx, error) {
	if options != nil && options.ReadOnly {
		return s.db.BeginTx(ctx, options)
	}
	return s.writeDB.BeginTx(ctx, options)
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Health(ctx context.Context) (DatabaseHealth, error) {
	health := DatabaseHealth{}
	identity, err := verifyBaselineIdentity(ctx, s.db)
	if err != nil {
		return health, err
	}
	health.SchemaVersion, health.SourceSHA256, health.CatalogSHA256 = identity.SchemaVersion, identity.SourceSHA256, identity.CatalogSHA256
	var foreignKeys int
	if err := s.db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&health.SQLiteVersion); err != nil {
		return DatabaseHealth{}, storageFailure("read SQLite runtime version", err)
	}
	if strings.TrimSpace(health.SQLiteVersion) == "" {
		return DatabaseHealth{}, storageFailure("read SQLite runtime version", errors.New("SQLite returned an empty version"))
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&health.JournalMode); err != nil {
		return DatabaseHealth{}, storageFailure("read database journal mode", err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return DatabaseHealth{}, storageFailure("read database foreign-key mode", err)
	}
	health.IntegrityCheck, err = s.databaseIntegrityCheck(ctx)
	if err != nil {
		return DatabaseHealth{}, err
	}
	health.ForeignKeys = foreignKeys == 1
	health.JournalMode = strings.ToLower(health.JournalMode)
	health.Status = "ok"
	if health.JournalMode != "wal" || !health.ForeignKeys || health.IntegrityCheck != "ok" {
		health.Status = "failed"
		return health, &Error{
			Code: CodeStorageFailed,
			Message: fmt.Sprintf(
				"database health check failed (schema=%d source_sha256=%s catalog_sha256=%s journal=%s foreign_keys=%t integrity=%s)",
				health.SchemaVersion,
				health.SourceSHA256,
				health.CatalogSHA256,
				health.JournalMode,
				health.ForeignKeys,
				health.IntegrityCheck,
			),
		}
	}
	return health, nil
}

func (s *Store) databaseIntegrityCheck(ctx context.Context) (string, error) {
	fileResult, err := s.databaseIntegrityCheckWithoutRetrieval(ctx)
	if err != nil {
		return "", storageFailure("run database integrity check", err)
	}
	return fileResult, nil
}

func (s *Store) databaseIntegrityCheckWithoutRetrieval(ctx context.Context) (string, error) {
	// The base registered driver does not install FTS5. SQLite therefore runs one
	// global page-allocation, freelist, and ordinary B-tree check without invoking
	// the disposable virtual table's xIntegrity hook. Any failure here is file-wide
	// or structural and must block startup; semantic FTS health is checked by the
	// retrieval subsystem itself.
	database, err := sql.Open("sqlite3", databaseDSN(s.path))
	if err != nil {
		return "", err
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(0)
	defer database.Close()
	if err := database.PingContext(ctx); err != nil {
		return "", err
	}
	return runSQLiteQuickCheck(ctx, database)
}

func runSQLiteQuickCheck(ctx context.Context, database *sql.DB) (string, error) {
	rows, err := database.QueryContext(ctx, "PRAGMA quick_check(1)")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	result := ""
	for rows.Next() {
		var current string
		if err := rows.Scan(&current); err != nil {
			return "", err
		}
		if current != "ok" {
			return current, nil
		}
		result = current
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if result == "" {
		return "", errors.New("SQLite quick_check returned no result")
	}
	return result, nil
}

func inspectDatabasePath(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, storageFailure("inspect database path", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, &Error{Code: CodeStorageFailed, Message: "database path must be a regular file and not a symbolic link"}
	}
	if info.Mode().Perm() != 0o600 {
		return false, &Error{Code: CodeStorageFailed, Message: "database file permissions must be exactly 0600"}
	}
	if info.Sys() != nil {
		if stat, ok := info.Sys().(*unix.Stat_t); ok && stat.Nlink != 1 {
			return false, &Error{Code: CodeStorageFailed, Message: "database file must not have hard-link aliases"}
		}
	}
	return true, nil
}

func createAndPublishCurrentDatabase(ctx context.Context, path string, options Options) (err error) {
	parent := filepath.Dir(path)
	parentDirectory, err := os.Open(parent)
	if err != nil {
		return storageFailure("open database parent for staged creation", err)
	}
	defer parentDirectory.Close()
	if err := unix.Flock(int(parentDirectory.Fd()), unix.LOCK_EX); err != nil {
		return storageFailure("lock database parent for staged creation", err)
	}
	defer unix.Flock(int(parentDirectory.Fd()), unix.LOCK_UN)
	if exists, inspectErr := inspectDatabasePath(path); inspectErr != nil {
		return inspectErr
	} else if exists {
		return nil
	}
	stagingPath := filepath.Join(parent, databaseCreationStageName)
	if err := cleanupInterruptedDatabaseCreation(parentDirectory, stagingPath); err != nil {
		return storageFailure("reconcile interrupted staged database", err)
	}
	stagingFD, err := unix.Open(stagingPath, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return storageFailure("create staged database file", err)
	}
	stagingFile := os.NewFile(uintptr(stagingFD), stagingPath)
	published := false
	defer func() {
		if published {
			return
		}
		_ = cleanupInterruptedDatabaseCreation(parentDirectory, stagingPath)
	}()
	if err := stagingFile.Chmod(0o600); err != nil {
		_ = stagingFile.Close()
		return storageFailure("set staged database permissions", err)
	}
	if err := stagingFile.Close(); err != nil {
		return storageFailure("close staged database file", err)
	}

	staged, err := openStoreFile(ctx, stagingPath, options)
	if err != nil {
		return err
	}
	if err := staged.initializeCurrentBaseline(ctx); err != nil {
		_ = staged.Close()
		return err
	}
	if err := staged.Close(); err != nil {
		return storageFailure("close staged current database", err)
	}
	for _, suffix := range []string{"-journal", "-wal", "-shm"} {
		if _, err := os.Lstat(stagingPath + suffix); err == nil {
			return &Error{Code: CodeStorageFailed, Message: "staged current database retained a SQLite sidecar before publication"}
		} else if !errors.Is(err, os.ErrNotExist) {
			return storageFailure("inspect staged SQLite sidecar", err)
		}
	}
	if err := syncStagedDatabaseFile(stagingPath); err != nil {
		return storageFailure("sync staged current database", err)
	}
	if err := staged.runMutationHook(MutationBeforeBaselinePublish); err != nil {
		return err
	}
	if err := unix.Renameat2(unix.AT_FDCWD, stagingPath, unix.AT_FDCWD, path, unix.RENAME_NOREPLACE); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return nil
		}
		return storageFailure("publish current database baseline", err)
	}
	published = true
	if err := syncDirectory(parent); err != nil {
		return storageFailure("sync database directory after baseline publication", err)
	}
	if err := staged.runMutationHook(MutationAfterBaselinePublish); err != nil {
		return err
	}
	return nil
}

func cleanupInterruptedDatabaseCreation(parentDirectory *os.File, stagingPath string) error {
	removed := false
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		candidate := stagingPath + suffix
		var stat unix.Stat_t
		if err := unix.Lstat(candidate, &stat); errors.Is(err, unix.ENOENT) {
			continue
		} else if err != nil {
			return err
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Uid != uint32(os.Geteuid()) || stat.Nlink != 1 || stat.Mode&0o777 != 0o600 {
			return errors.New("interrupted staged database is not exact private single-link storage")
		}
		if err := unix.Unlink(candidate); err != nil {
			return err
		}
		removed = true
	}
	if removed {
		return parentDirectory.Sync()
	}
	return nil
}

func syncStagedDatabaseFile(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return err
	}
	return directory.Close()
}

func databaseDSN(path string) string {
	location := &url.URL{Scheme: "file", Path: path}
	query := location.Query()
	query.Set("mode", "rw")
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", sqliteBusyTimeout.Milliseconds()))
	query.Add("_pragma", "foreign_keys(ON)")
	query.Add("_pragma", "synchronous(FULL)")
	query.Set("_txlock", "immediate")
	location.RawQuery = query.Encode()
	return location.String()
}

func storageFailure(operation string, err error) *Error {
	if errors.Is(err, sqlite3.BUSY) {
		return &Error{Code: CodeDatabaseBusy, Message: operation, Cause: err}
	}
	return &Error{Code: CodeStorageFailed, Message: operation, Cause: err}
}
