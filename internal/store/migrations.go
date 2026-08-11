package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func (s *Store) migrate(ctx context.Context) error {
	var current int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return storageFailure("read current schema version", err)
	}
	if current > LatestSchemaVersion {
		return &Error{
			Code:    CodeStorageFailed,
			Message: fmt.Sprintf("database schema version %d is newer than supported version %d", current, LatestSchemaVersion),
		}
	}

	if _, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    name TEXT NOT NULL,
    applied_at TEXT NOT NULL
) STRICT;
`); err != nil {
		return storageFailure("create migration metadata", err)
	}

	migrations, err := loadMigrations()
	if err != nil {
		return err
	}
	if err := s.validateAppliedMigrations(ctx, current, migrations); err != nil {
		return err
	}
	for _, migration := range migrations {
		if migration.version <= current {
			continue
		}
		transaction, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return storageFailure(fmt.Sprintf("begin migration %d", migration.version), err)
		}
		if _, err := transaction.ExecContext(ctx, migration.sql); err != nil {
			_ = transaction.Rollback()
			return storageFailure(fmt.Sprintf("apply migration %d", migration.version), err)
		}
		if _, err := transaction.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, name, applied_at) VALUES (?, ?, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))",
			migration.version,
			migration.name,
		); err != nil {
			_ = transaction.Rollback()
			return storageFailure(fmt.Sprintf("record migration %d", migration.version), err)
		}
		if _, err := transaction.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", migration.version)); err != nil {
			_ = transaction.Rollback()
			return storageFailure(fmt.Sprintf("set migration version %d", migration.version), err)
		}
		if err := transaction.Commit(); err != nil {
			return storageFailure(fmt.Sprintf("commit migration %d", migration.version), err)
		}
		current = migration.version
	}
	if current != LatestSchemaVersion {
		return &Error{
			Code:    CodeStorageFailed,
			Message: fmt.Sprintf("database reached schema version %d, expected %d", current, LatestSchemaVersion),
		}
	}
	return nil
}

func (s *Store) validateAppliedMigrations(ctx context.Context, current int, migrations []migration) error {
	rows, err := s.db.QueryContext(ctx, "SELECT version, name FROM schema_migrations ORDER BY version")
	if err != nil {
		return storageFailure("read migration metadata", err)
	}
	defer rows.Close()

	index := 0
	for rows.Next() {
		var version int
		var name string
		if err := rows.Scan(&version, &name); err != nil {
			return storageFailure("scan migration metadata", err)
		}
		if index >= current || index >= len(migrations) || version != migrations[index].version || name != migrations[index].name {
			return &Error{Code: CodeStorageFailed, Message: "database migration metadata does not match embedded migrations"}
		}
		index++
	}
	if err := rows.Err(); err != nil {
		return storageFailure("iterate migration metadata", err)
	}
	if index != current {
		return &Error{
			Code:    CodeStorageFailed,
			Message: fmt.Sprintf("database schema version is %d but has %d migration records", current, index),
		}
	}
	return nil
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, storageFailure("read embedded migrations", err)
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].Name() < entries[right].Name() })

	result := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		if !ok {
			return nil, &Error{Code: CodeStorageFailed, Message: fmt.Sprintf("invalid migration filename %q", entry.Name())}
		}
		version, err := strconv.Atoi(prefix)
		if err != nil || version != len(result)+1 {
			return nil, &Error{Code: CodeStorageFailed, Message: fmt.Sprintf("migration %q is not the next contiguous version", entry.Name())}
		}
		data, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, storageFailure("read embedded migration "+entry.Name(), err)
		}
		result = append(result, migration{version: version, name: entry.Name(), sql: string(data)})
	}
	if len(result) != LatestSchemaVersion {
		return nil, &Error{
			Code:    CodeStorageFailed,
			Message: fmt.Sprintf("embedded migration count %d does not match latest schema version %d", len(result), LatestSchemaVersion),
		}
	}
	return result, nil
}
