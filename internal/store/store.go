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
	"sync/atomic"
	"time"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
	"github.com/ncruces/go-sqlite3/ext/fts5"
	"github.com/ncruces/go-sqlite3/ext/hash"
)

const databaseFilename = "crewfold.db"

// sqliteApplicationID is the ASCII marker "CRFD" stored in SQLite's file header.
const sqliteApplicationID = 0x43524644

type Store struct {
	db                         *sql.DB
	path                       string
	mutationHook               func(string) error
	clock                      func() time.Time
	restoreActive              *atomic.Bool
	supervisorActionSealActive *atomic.Bool
}

func Open(ctx context.Context, dataDir string, options Options) (*Store, error) {
	path := filepath.Join(dataDir, databaseFilename)
	if err := validateDatabasePath(path); err != nil {
		return nil, err
	}

	dsn := databaseDSN(path)
	restoreActive := new(atomic.Bool)
	supervisorActionSealActive := new(atomic.Bool)
	database, err := driver.Open(dsn, func(connection *sqlite3.Conn) error {
		if err := registerSQLiteExtensions(connection); err != nil {
			return err
		}
		if err := registerSQLiteSupervisorActionSealActive(connection, supervisorActionSealActive); err != nil {
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
	})
	if err != nil {
		return nil, &Error{Code: CodeStorageFailed, Message: "open SQLite database", Cause: err}
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)

	clock := options.Clock
	if clock == nil {
		clock = time.Now
	}
	storage := &Store{db: database, path: path, mutationHook: options.MutationHook, clock: clock, restoreActive: restoreActive, supervisorActionSealActive: supervisorActionSealActive}
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, &Error{Code: CodeStorageFailed, Message: "connect to SQLite database", Cause: err}
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = database.Close()
		return nil, &Error{Code: CodeStorageFailed, Message: "set database permissions", Cause: err}
	}
	if err := storage.ensureDatabaseIdentity(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}
	if err := storage.enableWAL(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}
	if err := storage.migrate(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}
	if _, err := storage.Health(ctx); err != nil {
		_ = database.Close()
		return nil, err
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
	if err := registerSQLiteSupervisorEventKnown(connection); err != nil {
		return err
	}
	return registerSQLiteUTF8Valid(connection)
}

func (s *Store) ensureDatabaseIdentity(ctx context.Context) error {
	var applicationID, schemaVersion int
	if err := s.db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&applicationID); err != nil {
		return storageFailure("read database application id", err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schemaVersion); err != nil {
		return storageFailure("read database schema version", err)
	}
	if applicationID == sqliteApplicationID {
		return nil
	}
	if applicationID != 0 || schemaVersion != 0 {
		return &Error{Code: CodeStorageFailed, Message: "database file is not identified as a Crewfold database"}
	}

	var userTableCount int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM sqlite_schema
WHERE type = 'table' AND name NOT LIKE 'sqlite_%'`).Scan(&userTableCount); err != nil {
		return storageFailure("inspect unidentified database", err)
	}
	if userTableCount != 0 {
		return &Error{Code: CodeStorageFailed, Message: "unidentified database contains user tables and will not be adopted"}
	}
	if _, err := s.db.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id = %d", sqliteApplicationID)); err != nil {
		return storageFailure("mark Crewfold database identity", err)
	}
	return nil
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
	return s.db.Close()
}

func (s *Store) Path() string {
	return s.path
}

func (s *Store) Health(ctx context.Context) (DatabaseHealth, error) {
	health := DatabaseHealth{LatestSchemaVersion: LatestSchemaVersion}
	var foreignKeys, applicationID int
	if err := s.db.QueryRowContext(ctx, "PRAGMA application_id").Scan(&applicationID); err != nil {
		return DatabaseHealth{}, storageFailure("read database application id", err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&health.SchemaVersion); err != nil {
		return DatabaseHealth{}, storageFailure("read database schema version", err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&health.JournalMode); err != nil {
		return DatabaseHealth{}, storageFailure("read database journal mode", err)
	}
	if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return DatabaseHealth{}, storageFailure("read database foreign-key mode", err)
	}
	var err error
	health.IntegrityCheck, err = s.databaseIntegrityCheck(ctx)
	if err != nil {
		return DatabaseHealth{}, err
	}
	health.ForeignKeys = foreignKeys == 1
	health.JournalMode = strings.ToLower(health.JournalMode)
	health.Status = "ok"
	if applicationID != sqliteApplicationID || health.SchemaVersion != LatestSchemaVersion || health.JournalMode != "wal" || !health.ForeignKeys || health.IntegrityCheck != "ok" {
		health.Status = "failed"
		return health, &Error{
			Code: CodeStorageFailed,
			Message: fmt.Sprintf(
				"database health check failed (application_id=%#x schema=%d journal=%s foreign_keys=%t integrity=%s)",
				applicationID,
				health.SchemaVersion,
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

func validateDatabasePath(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return storageFailure("inspect database path", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return &Error{Code: CodeStorageFailed, Message: "database path must be a regular file and not a symbolic link"}
	}
	return nil
}

func databaseDSN(path string) string {
	location := &url.URL{Scheme: "file", Path: path}
	query := location.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(ON)")
	query.Add("_pragma", "synchronous(FULL)")
	query.Set("_txlock", "immediate")
	location.RawQuery = query.Encode()
	return location.String()
}

func storageFailure(operation string, err error) *Error {
	return &Error{Code: CodeStorageFailed, Message: operation, Cause: err}
}
