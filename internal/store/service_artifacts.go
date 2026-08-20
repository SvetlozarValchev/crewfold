package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"crewfold/internal/domain"

	"golang.org/x/sys/unix"
)

const maximumManagedServiceLogArtifactBytes int64 = 1024 * 1024

var managedServiceInstanceIDPattern = regexp.MustCompile(`^svcinst_[0-9a-f]{32}$`)

// PrepareManagedServiceLogArchive publishes a bounded immutable stdout/stderr
// pair. Unreferenced files after a crash are harmless and excluded from backup.
func (s *Store) PrepareManagedServiceLogArchive(_ context.Context, expectedInstanceID string, logs domain.ManagedServiceLogs, limit int64) (domain.ManagedServiceLogArchive, error) {
	if !managedServiceInstanceIDPattern.MatchString(expectedInstanceID) || logs.InstanceID != expectedInstanceID {
		return domain.ManagedServiceLogArchive{}, managedServiceLogsUnavailable("managed-service log capture does not match the expected instance", nil)
	}
	if limit < 4096 || limit > maximumManagedServiceLogArtifactBytes {
		return domain.ManagedServiceLogArchive{}, managedServiceLogsUnavailable("managed-service log capture limit is invalid", nil)
	}
	stdout, err := s.prepareManagedServiceArchivedLog(domain.ManagedServiceLogStdout, logs.Stdout, limit)
	if err != nil {
		return domain.ManagedServiceLogArchive{}, err
	}
	stderr, err := s.prepareManagedServiceArchivedLog(domain.ManagedServiceLogStderr, logs.Stderr, limit)
	if err != nil {
		return domain.ManagedServiceLogArchive{}, err
	}
	return domain.ManagedServiceLogArchive{InstanceID: expectedInstanceID, Stdout: stdout, Stderr: stderr}, nil
}

func (s *Store) prepareManagedServiceArchivedLog(kind string, captured domain.CapturedLog, limit int64) (domain.ArchivedRunLog, error) {
	if captured.CapturedBytes < 0 || captured.OmittedBytes < 0 || captured.Truncated != (captured.OmittedBytes > 0) {
		return domain.ArchivedRunLog{}, managedServiceLogsUnavailable("managed-service log capture metadata is invalid", nil)
	}
	text := strings.ToValidUTF8(captured.Text, "\uFFFD")
	omitted := captured.OmittedBytes
	if int64(len(text)) > limit {
		cut := int(limit)
		for cut > 0 && !utf8.ValidString(text[:cut]) {
			cut--
		}
		omitted = saturatingAddInt64(omitted, int64(len(text)-cut))
		text = text[:cut]
	}
	content := []byte(text)
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	if err := s.publishImmutableArtifact(serviceArtifactNamespace, ".service-artifact-", hash, content, limit); err != nil {
		return domain.ArchivedRunLog{}, managedServiceLogsUnavailable("publish managed-service terminal log artifact", err)
	}
	return domain.ArchivedRunLog{Kind: kind, ContentSHA256: hash, CapturedBytes: int64(len(content)), OmittedBytes: omitted, Truncated: omitted > 0}, nil
}

func (s *Store) validateManagedServiceLogArchive(instanceID string, limit int64, archive *domain.ManagedServiceLogArchive) error {
	if archive == nil || archive.InstanceID != instanceID || !managedServiceInstanceIDPattern.MatchString(instanceID) {
		return managedServiceLogsUnavailable("prepared managed-service terminal logs do not match the instance", nil)
	}
	for _, artifact := range []domain.ArchivedRunLog{archive.Stdout, archive.Stderr} {
		if (artifact.Kind != domain.ManagedServiceLogStdout && artifact.Kind != domain.ManagedServiceLogStderr) || !validLowerSHA256(artifact.ContentSHA256) ||
			artifact.CapturedBytes < 0 || artifact.CapturedBytes > limit || artifact.OmittedBytes < 0 || artifact.Truncated != (artifact.OmittedBytes > 0) {
			return managedServiceLogsUnavailable("prepared managed-service terminal log metadata is invalid", nil)
		}
		if err := s.verifyManagedServiceArchivedLog(artifact, limit); err != nil {
			return err
		}
	}
	if archive.Stdout.Kind != domain.ManagedServiceLogStdout || archive.Stderr.Kind != domain.ManagedServiceLogStderr {
		return managedServiceLogsUnavailable("prepared managed-service terminal log streams are incomplete", nil)
	}
	return nil
}

func (s *Store) verifyManagedServiceArchivedLog(artifact domain.ArchivedRunLog, limit int64) error {
	shardFD, err := s.openImmutableArtifactShard(serviceArtifactNamespace, artifact.ContentSHA256, false)
	if err != nil {
		return managedServiceLogsUnavailable("managed-service terminal log directory is unavailable", err)
	}
	defer unix.Close(shardFD)
	content, err := readImmutableArtifactAt(shardFD, artifact.ContentSHA256, limit)
	if err != nil {
		return managedServiceLogsUnavailable("managed-service terminal log artifact is unavailable", err)
	}
	digest := sha256.Sum256(content)
	if int64(len(content)) != artifact.CapturedBytes || hex.EncodeToString(digest[:]) != artifact.ContentSHA256 || !utf8.Valid(content) {
		return managedServiceLogsUnavailable("managed-service terminal log artifact failed verification", nil)
	}
	return nil
}

func (s *Store) insertManagedServiceLogArchive(ctx context.Context, tx *sql.Tx, instanceID, now string, limit int64, archive *domain.ManagedServiceLogArchive) error {
	if err := s.validateManagedServiceLogArchive(instanceID, limit, archive); err != nil {
		return err
	}
	for _, artifact := range []domain.ArchivedRunLog{archive.Stdout, archive.Stderr} {
		if _, err := tx.ExecContext(ctx, `INSERT INTO immutable_artifacts(content_sha256,byte_size,created_at,created_by) VALUES(?,?,?,'subsystem:service-worker') ON CONFLICT(content_sha256) DO NOTHING`, artifact.ContentSHA256, artifact.CapturedBytes, now); err != nil {
			return storageFailure("catalog managed-service terminal log artifact", err)
		}
		var catalogedBytes int64
		if err := tx.QueryRowContext(ctx, `SELECT byte_size FROM immutable_artifacts WHERE content_sha256=?`, artifact.ContentSHA256).Scan(&catalogedBytes); err != nil {
			return storageFailure("verify managed-service terminal log catalog", err)
		}
		if catalogedBytes != artifact.CapturedBytes {
			return storageFailure("verify managed-service terminal log catalog", errors.New("content digest has conflicting catalog metadata"))
		}
		id, err := randomID("svclog_")
		if err != nil {
			return storageFailure("generate managed-service terminal log id", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO managed_service_log_artifacts(id,instance_id,kind,content_sha256,captured_bytes,omitted_bytes,truncated,created_at,created_by) VALUES(?,?,?,?,?,?,?,?, 'subsystem:service-worker')`, id, instanceID, artifact.Kind, artifact.ContentSHA256, artifact.CapturedBytes, artifact.OmittedBytes, boolInteger(artifact.Truncated), now); err != nil {
			return managedServiceConstraint("record managed-service terminal log artifact", err)
		}
	}
	return nil
}

// ManagedServiceTerminalLogs returns only a verified immutable terminal pair.
func (s *Store) ManagedServiceTerminalLogs(ctx context.Context, workspaceIdentifier, instanceID string) (domain.ManagedServiceLogs, error) {
	detail, err := s.ManagedServiceDetail(ctx, workspaceIdentifier, instanceID)
	if err != nil {
		return domain.ManagedServiceLogs{}, err
	}
	if detail.Instance.Status != domain.ManagedServiceStopped && detail.Instance.Status != domain.ManagedServiceFailed {
		return domain.ManagedServiceLogs{}, managedServiceLogsUnavailable("managed service has no immutable terminal log capture", nil)
	}
	if len(detail.Logs) != 2 {
		return domain.ManagedServiceLogs{}, managedServiceLogsUnavailable("managed-service terminal log archive is unavailable", nil)
	}
	result := domain.ManagedServiceLogs{InstanceID: detail.Instance.ID, State: "terminal"}
	seen := map[string]bool{}
	for _, row := range detail.Logs {
		artifact := domain.ArchivedRunLog{Kind: row.Kind, ContentSHA256: row.ContentSHA256, CapturedBytes: row.CapturedBytes, OmittedBytes: row.OmittedBytes, Truncated: row.Truncated}
		if seen[row.Kind] || (row.Kind != domain.ManagedServiceLogStdout && row.Kind != domain.ManagedServiceLogStderr) {
			return domain.ManagedServiceLogs{}, managedServiceLogsUnavailable("managed-service terminal log metadata is invalid", nil)
		}
		seen[row.Kind] = true
		if err := s.verifyManagedServiceArchivedLog(artifact, detail.Definition.OutputByteLimit); err != nil {
			return domain.ManagedServiceLogs{}, err
		}
		shardFD, err := s.openImmutableArtifactShard(serviceArtifactNamespace, row.ContentSHA256, false)
		if err != nil {
			return domain.ManagedServiceLogs{}, managedServiceLogsUnavailable("managed-service terminal log directory is unavailable", err)
		}
		content, readErr := readImmutableArtifactAt(shardFD, row.ContentSHA256, detail.Definition.OutputByteLimit)
		_ = unix.Close(shardFD)
		if readErr != nil {
			return domain.ManagedServiceLogs{}, managedServiceLogsUnavailable("managed-service terminal log artifact is unavailable", readErr)
		}
		captured := domain.CapturedLog{Text: string(content), CapturedBytes: row.CapturedBytes, OmittedBytes: row.OmittedBytes, Truncated: row.Truncated}
		if row.Kind == domain.ManagedServiceLogStdout {
			result.Stdout = captured
		} else {
			result.Stderr = captured
		}
	}
	if !seen[domain.ManagedServiceLogStdout] || !seen[domain.ManagedServiceLogStderr] {
		return domain.ManagedServiceLogs{}, managedServiceLogsUnavailable("managed-service terminal log streams are incomplete", nil)
	}
	return result, nil
}

func managedServiceLogsUnavailable(message string, cause error) error {
	return &Error{Code: CodeManagedServiceLogsUnavailable, Message: message, Cause: cause}
}
