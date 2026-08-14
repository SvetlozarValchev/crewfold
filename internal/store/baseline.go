package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
)

const CurrentSchemaVersion = 1

//go:embed baseline/current.sql
var baselineFiles embed.FS

var (
	currentBaselineSQL          = mustReadCurrentBaseline()
	currentBaselineSourceSHA256 = sha256Text(currentBaselineSQL)
)

type baselineQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func mustReadCurrentBaseline() string {
	data, err := baselineFiles.ReadFile("baseline/current.sql")
	if err != nil {
		panic(fmt.Sprintf("read embedded current baseline: %v", err))
	}
	if len(data) == 0 {
		panic("embedded current baseline is empty")
	}
	return string(data)
}

func sha256Text(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func (s *Store) initializeCurrentBaseline(ctx context.Context) error {
	tx, err := s.beginTx(ctx, nil)
	if err != nil {
		return storageFailure("begin current baseline creation", err)
	}
	defer tx.Rollback()

	var applicationID, schemaVersion, userObjects int
	if err := tx.QueryRowContext(ctx, "PRAGMA application_id").Scan(&applicationID); err != nil {
		return storageFailure("read fresh database application id", err)
	}
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&schemaVersion); err != nil {
		return storageFailure("read fresh database schema version", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%'`).Scan(&userObjects); err != nil {
		return storageFailure("inspect fresh database catalog", err)
	}
	if applicationID != 0 || schemaVersion != 0 || userObjects != 0 {
		return &Error{Code: CodeStorageFailed, Message: "fresh database is not empty and cannot receive the current baseline"}
	}

	if _, err := tx.ExecContext(ctx, currentBaselineSQL); err != nil {
		return storageFailure("create current database baseline", err)
	}
	if err := s.runMutationHook(MutationAfterBaselineCatalog); err != nil {
		return err
	}
	catalogSHA256, err := databaseCatalogSHA256(ctx, tx)
	if err != nil {
		return storageFailure("hash current database catalog", err)
	}
	if _, err := tx.ExecContext(ctx,
		"INSERT INTO schema_baseline(singleton,source_sha256,catalog_sha256) VALUES (1,?,?)",
		currentBaselineSourceSHA256,
		catalogSHA256,
	); err != nil {
		return storageFailure("record current baseline identity", err)
	}
	if err := s.runMutationHook(MutationAfterBaselineIdentity); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA application_id = %d", sqliteApplicationID)); err != nil {
		return storageFailure("set current database application id", err)
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", CurrentSchemaVersion)); err != nil {
		return storageFailure("set current database schema version", err)
	}
	if _, err := verifyBaselineIdentity(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return storageFailure("commit current database baseline", err)
	}
	return nil
}

func (s *Store) BaselineIdentity(ctx context.Context) (BaselineIdentity, error) {
	return verifyBaselineIdentity(ctx, s.db)
}

func verifyBaselineIdentity(ctx context.Context, query baselineQuery) (BaselineIdentity, error) {
	var identity BaselineIdentity
	var applicationID int
	if err := query.QueryRowContext(ctx, "PRAGMA application_id").Scan(&applicationID); err != nil {
		return BaselineIdentity{}, storageFailure("read database application id", err)
	}
	if err := query.QueryRowContext(ctx, "PRAGMA user_version").Scan(&identity.SchemaVersion); err != nil {
		return BaselineIdentity{}, storageFailure("read database schema version", err)
	}
	if applicationID != sqliteApplicationID || identity.SchemaVersion != CurrentSchemaVersion {
		return BaselineIdentity{}, &Error{
			Code: CodeCurrentBaselineMismatch,
			Message: fmt.Sprintf(
				"database identity does not match the current baseline (application_id=%#x schema_version=%d)",
				applicationID,
				identity.SchemaVersion,
			),
		}
	}

	var identityCount int
	if err := query.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_baseline`).Scan(&identityCount); err != nil {
		return BaselineIdentity{}, currentBaselineMismatch("database does not contain the exact current baseline identity", err)
	}
	if identityCount != 1 {
		return BaselineIdentity{}, currentBaselineMismatch("database must contain exactly one current baseline identity", nil)
	}
	if err := query.QueryRowContext(ctx,
		`SELECT source_sha256,catalog_sha256 FROM schema_baseline WHERE singleton=1`,
	).Scan(&identity.SourceSHA256, &identity.CatalogSHA256); err != nil {
		return BaselineIdentity{}, currentBaselineMismatch("database does not contain the current baseline singleton", err)
	}
	if identity.SourceSHA256 != currentBaselineSourceSHA256 {
		return BaselineIdentity{}, currentBaselineMismatch("database baseline source SHA-256 does not match this binary", nil)
	}
	actualCatalogSHA256, err := databaseCatalogSHA256(ctx, query)
	if err != nil {
		return BaselineIdentity{}, storageFailure("hash installed database catalog", err)
	}
	if identity.CatalogSHA256 != actualCatalogSHA256 {
		return BaselineIdentity{}, currentBaselineMismatch("installed database catalog SHA-256 does not match its exact baseline identity", nil)
	}
	return identity, nil
}

func currentBaselineMismatch(message string, cause error) *Error {
	return &Error{Code: CodeCurrentBaselineMismatch, Message: message, Cause: cause}
}

func databaseCatalogSHA256(ctx context.Context, query baselineQuery) (string, error) {
	rows, err := query.QueryContext(ctx, `
SELECT type,name,tbl_name,sql
FROM sqlite_schema
ORDER BY type,name,tbl_name`)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	digest := sha256.New()
	for rows.Next() {
		var objectType, name, tableName string
		var statement sql.NullString
		if err := rows.Scan(&objectType, &name, &tableName, &statement); err != nil {
			return "", err
		}
		if derivedKnowledgeCatalogObject(objectType, name, tableName) {
			continue
		}
		for _, field := range []string{objectType, name, tableName} {
			if err := writeCatalogField(digest, true, field); err != nil {
				return "", err
			}
		}
		if err := writeCatalogField(digest, statement.Valid, statement.String); err != nil {
			return "", err
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// The FTS projection is deliberately rebuildable and is verified separately
// from canonical state. SQLite's FTS5 module may rewrite the virtual-table SQL
// into an equivalent whitespace form after an ordinary content mutation, and a
// missing projection must leave explicit index rebuild reachable. Exclude only
// the exact derived tables and SQLite-owned indexes on their shadow tables; any
// user-defined trigger or other catalog object remains part of the baseline.
func derivedKnowledgeCatalogObject(objectType, name, tableName string) bool {
	derivedTables := map[string]bool{
		"knowledge_search":          true,
		"knowledge_search_config":   true,
		"knowledge_search_content":  true,
		"knowledge_search_data":     true,
		"knowledge_search_docsize":  true,
		"knowledge_search_idx":      true,
		"knowledge_search_metadata": true,
	}
	if objectType == "table" {
		return derivedTables[name]
	}
	return objectType == "index" && derivedTables[tableName] &&
		len(name) > len("sqlite_autoindex_") && name[:len("sqlite_autoindex_")] == "sqlite_autoindex_"
}

func writeCatalogField(writer io.Writer, present bool, value string) error {
	presence := byte(0)
	if present {
		presence = 1
	}
	if _, err := writer.Write([]byte{presence}); err != nil {
		return err
	}
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	if _, err := writer.Write(size[:]); err != nil {
		return err
	}
	_, err := io.WriteString(writer, value)
	return err
}
