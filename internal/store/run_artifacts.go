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
	"crewfold/internal/store/dbgen"

	"golang.org/x/sys/unix"
)

const maximumRunLogArtifactBytes int64 = 64 * 1024

var terminalLogRunIDPattern = regexp.MustCompile(`^run_[0-9a-f]{32}$`)

// PrepareRunLogArchive publishes a bounded immutable stdout/stderr pair before
// the terminal SQLite transaction. A crash can leave unreferenced files; only
// rows committed with the terminal projection enter backup closure.
func (s *Store) PrepareRunLogArchive(_ context.Context, expectedRunID string, logs domain.RunLogs) (domain.RunLogArchive, error) {
	if !terminalLogRunIDPattern.MatchString(expectedRunID) || logs.RunID != expectedRunID || !validRuntimeLogState(logs.State) {
		return domain.RunLogArchive{}, runLogsUnavailable("runtime log capture does not match the expected run", nil)
	}
	stdout, err := s.prepareArchivedRunLog(domain.CheckArtifactStdout, logs.Stdout)
	if err != nil {
		return domain.RunLogArchive{}, err
	}
	stderr, err := s.prepareArchivedRunLog(domain.CheckArtifactStderr, logs.Stderr)
	if err != nil {
		return domain.RunLogArchive{}, err
	}
	return domain.RunLogArchive{RunID: expectedRunID, State: logs.State, Stdout: stdout, Stderr: stderr}, nil
}

func (s *Store) prepareArchivedRunLog(kind string, captured domain.CapturedLog) (domain.ArchivedRunLog, error) {
	if captured.CapturedBytes < 0 || captured.OmittedBytes < 0 || captured.Truncated != (captured.OmittedBytes > 0) {
		return domain.ArchivedRunLog{}, runLogsUnavailable("runtime log capture metadata is invalid", nil)
	}
	text := strings.ToValidUTF8(captured.Text, "\uFFFD")
	omitted := captured.OmittedBytes
	if int64(len(text)) > maximumRunLogArtifactBytes {
		limit := int(maximumRunLogArtifactBytes)
		for limit > 0 && !utf8.ValidString(text[:limit]) {
			limit--
		}
		omitted = saturatingAddInt64(omitted, int64(len(text)-limit))
		text = text[:limit]
	}
	content := []byte(text)
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	if err := s.publishImmutableArtifact(runArtifactNamespace, ".run-artifact-", hash, content, maximumRunLogArtifactBytes); err != nil {
		return domain.ArchivedRunLog{}, runLogsUnavailable("publish terminal run log artifact", err)
	}
	return domain.ArchivedRunLog{
		Kind: kind, ContentSHA256: hash, CapturedBytes: int64(len(content)),
		OmittedBytes: omitted, Truncated: omitted > 0,
	}, nil
}

func saturatingAddInt64(left, right int64) int64 {
	const maximumInt64 = int64(^uint64(0) >> 1)
	if left > maximumInt64-right {
		return maximumInt64
	}
	return left + right
}

func (s *Store) validateRunLogArchive(runID string, archive *domain.RunLogArchive) error {
	if archive == nil {
		return runLogsUnavailable("trusted terminal transition requires a prepared stdout/stderr archive", nil)
	}
	if !terminalLogRunIDPattern.MatchString(runID) || archive.RunID != runID || !validRuntimeLogState(archive.State) {
		return runLogsUnavailable("prepared terminal run log archive does not match the run", nil)
	}
	for _, artifact := range []domain.ArchivedRunLog{archive.Stdout, archive.Stderr} {
		if (artifact.Kind != domain.CheckArtifactStdout && artifact.Kind != domain.CheckArtifactStderr) ||
			!validLowerSHA256(artifact.ContentSHA256) || artifact.CapturedBytes < 0 || artifact.CapturedBytes > maximumRunLogArtifactBytes ||
			artifact.OmittedBytes < 0 || artifact.Truncated != (artifact.OmittedBytes > 0) {
			return runLogsUnavailable("prepared terminal run log metadata is invalid", nil)
		}
		if err := s.verifyArchivedRunLog(artifact); err != nil {
			return err
		}
	}
	if archive.Stdout.Kind != domain.CheckArtifactStdout || archive.Stderr.Kind != domain.CheckArtifactStderr {
		return runLogsUnavailable("prepared terminal run log streams are incomplete", nil)
	}
	return nil
}

func (s *Store) validateTerminalLogOutcome(runID string, archive *domain.RunLogArchive, unavailableReason string) error {
	unavailableReason = strings.TrimSpace(unavailableReason)
	if archive != nil && unavailableReason != "" {
		return &Error{Code: CodeInvalidRun, Message: "terminal run logs cannot be both archived and unavailable"}
	}
	if archive != nil {
		return s.validateRunLogArchive(runID, archive)
	}
	if !validMessageText(unavailableReason, 2048) {
		return runLogsUnavailable("terminal run log unavailability requires a bounded diagnosis", nil)
	}
	return nil
}

func terminalLogEventData(archive *domain.RunLogArchive, unavailableReason string) map[string]any {
	if archive == nil {
		return map[string]any{"logs_available": false, "logs_unavailable_reason": strings.TrimSpace(unavailableReason)}
	}
	return map[string]any{
		"logs_available": true,
		"stdout_sha256":  archive.Stdout.ContentSHA256,
		"stderr_sha256":  archive.Stderr.ContentSHA256,
	}
}

func mergeEventData(base, addition map[string]any) map[string]any {
	for key, value := range addition {
		base[key] = value
	}
	return base
}

func (s *Store) verifyArchivedRunLog(artifact domain.ArchivedRunLog) error {
	shardFD, err := s.openImmutableArtifactShard(runArtifactNamespace, artifact.ContentSHA256, false)
	if err != nil {
		return runLogsUnavailable("prepared terminal run log directory is unavailable", err)
	}
	defer unix.Close(shardFD)
	content, err := readImmutableArtifactAt(shardFD, artifact.ContentSHA256, maximumRunLogArtifactBytes)
	if err != nil {
		return runLogsUnavailable("prepared terminal run log artifact is unavailable", err)
	}
	digest := sha256.Sum256(content)
	if int64(len(content)) != artifact.CapturedBytes || hex.EncodeToString(digest[:]) != artifact.ContentSHA256 || !utf8.Valid(content) {
		return runLogsUnavailable("prepared terminal run log artifact failed verification", nil)
	}
	return nil
}

func (s *Store) insertRunLogArchive(ctx context.Context, tx *sql.Tx, runID, now string, archive *domain.RunLogArchive) error {
	if err := s.validateRunLogArchive(runID, archive); err != nil {
		return err
	}
	queries := dbgen.New(tx)
	for _, artifact := range []domain.ArchivedRunLog{archive.Stdout, archive.Stderr} {
		if err := queries.InsertImmutableArtifact(ctx, dbgen.InsertImmutableArtifactParams{
			ContentSha256: artifact.ContentSHA256, ByteSize: artifact.CapturedBytes,
			CreatedAt: now, CreatedBy: runWorkerActorID,
		}); err != nil {
			return storageFailure("catalog terminal run log artifact", err)
		}
		// The content catalog is shared by the typed check/run namespaces. A
		// matching digest may therefore predate this run archive, but its sealed
		// byte size must still agree with the file we just verified.
		var catalogedBytes int64
		if err := tx.QueryRowContext(ctx, `SELECT byte_size FROM immutable_artifacts WHERE content_sha256=?`, artifact.ContentSHA256).Scan(&catalogedBytes); err != nil {
			return storageFailure("verify terminal run log artifact catalog", err)
		}
		if catalogedBytes != artifact.CapturedBytes {
			return storageFailure("verify terminal run log artifact catalog", errors.New("content digest has conflicting catalog metadata"))
		}
		id, err := randomID("runartifact_")
		if err != nil {
			return storageFailure("generate terminal run log artifact id", err)
		}
		if err := queries.InsertRunLogArtifact(ctx, dbgen.InsertRunLogArtifactParams{
			ID: id, RunID: runID, Kind: artifact.Kind, ContentSha256: artifact.ContentSHA256,
			CapturedBytes: artifact.CapturedBytes, OmittedBytes: artifact.OmittedBytes,
			Truncated: boolInteger(artifact.Truncated), CreatedAt: now,
		}); err != nil {
			return storageFailure("record terminal run log artifact", err)
		}
	}
	return nil
}

func (s *Store) terminalRunLogOutcomeReplayMatches(ctx context.Context, tx *sql.Tx, run domain.Run, archive *domain.RunLogArchive, unavailableReason string) (bool, error) {
	receipt, err := terminalRunLogReceiptFrom(ctx, tx, run)
	if err != nil {
		return false, err
	}
	rows, err := dbgen.New(tx).ListRunLogArtifacts(ctx, run.ID)
	if err != nil {
		return false, storageFailure("read terminal run log archive", err)
	}
	if archive == nil {
		return !receipt.available && len(rows) == 0 && receipt.unavailableReason == strings.TrimSpace(unavailableReason), nil
	}
	if err := s.validateRunLogArchive(run.ID, archive); err != nil {
		return false, err
	}
	if !receipt.available || receipt.stdoutSHA256 != archive.Stdout.ContentSHA256 || receipt.stderrSHA256 != archive.Stderr.ContentSHA256 {
		return false, nil
	}
	if len(rows) != 2 {
		return false, nil
	}
	want := map[string]domain.ArchivedRunLog{archive.Stdout.Kind: archive.Stdout, archive.Stderr.Kind: archive.Stderr}
	for _, row := range rows {
		artifact, exists := want[row.Kind]
		if !exists || artifact.ContentSHA256 != row.ContentSha256 || artifact.CapturedBytes != row.CapturedBytes || artifact.OmittedBytes != row.OmittedBytes || boolInteger(artifact.Truncated) != row.Truncated {
			return false, nil
		}
	}
	return true, nil
}

// RunTerminalLogs returns only a verified immutable terminal archive. Missing,
// incomplete, or corrupt capture is an explicit unavailable error.
func (s *Store) RunTerminalLogs(ctx context.Context, workspaceIdentifier, runID string, tail int) (domain.RunLogs, error) {
	if tail < 0 || tail > 10000 {
		return domain.RunLogs{}, &Error{Code: CodeInvalidRun, Message: "terminal run log tail is invalid"}
	}
	detail, err := s.RunDetail(ctx, workspaceIdentifier, runID)
	if err != nil {
		return domain.RunLogs{}, err
	}
	if !terminalRunStatus(detail.Run.Status) || detail.Run.Status == domain.RunLost {
		return domain.RunLogs{}, runLogsUnavailable("run has no trustworthy immutable terminal capture", nil)
	}
	rows, err := dbgen.New(s.db).ListRunLogArtifacts(ctx, detail.Run.ID)
	if err != nil {
		return domain.RunLogs{}, storageFailure("read terminal run log metadata", err)
	}
	receipt, err := s.terminalRunLogReceipt(ctx, detail.Run)
	if err != nil {
		return domain.RunLogs{}, err
	}
	if !receipt.available {
		if len(rows) != 0 {
			return domain.RunLogs{}, runLogsUnavailable("terminal run log receipt conflicts with immutable artifact rows", nil)
		}
		return domain.RunLogs{}, runLogsUnavailable(receipt.unavailableReason, nil)
	}
	if len(rows) != 2 {
		return domain.RunLogs{}, runLogsUnavailable("terminal run log archive is unavailable", nil)
	}
	logs := domain.RunLogs{RunID: detail.Run.ID, State: terminalRuntimeState(detail.Run)}
	seen := make(map[string]bool, 2)
	for _, row := range rows {
		if seen[row.Kind] || (row.Kind != domain.CheckArtifactStdout && row.Kind != domain.CheckArtifactStderr) || !validLowerSHA256(row.ContentSha256) ||
			row.CapturedBytes < 0 || row.CapturedBytes > maximumRunLogArtifactBytes || row.OmittedBytes < 0 || (row.Truncated != 0) != (row.OmittedBytes > 0) {
			return domain.RunLogs{}, runLogsUnavailable("terminal run log metadata is invalid", nil)
		}
		seen[row.Kind] = true
		expectedHash := receipt.stdoutSHA256
		if row.Kind == domain.CheckArtifactStderr {
			expectedHash = receipt.stderrSHA256
		}
		if row.ContentSha256 != expectedHash {
			return domain.RunLogs{}, runLogsUnavailable("terminal run log receipt digest differs from artifact metadata", nil)
		}
		shardFD, openErr := s.openImmutableArtifactShard(runArtifactNamespace, row.ContentSha256, false)
		if openErr != nil {
			return domain.RunLogs{}, runLogsUnavailable("terminal run log directory is unavailable", openErr)
		}
		content, readErr := readImmutableArtifactAt(shardFD, row.ContentSha256, maximumRunLogArtifactBytes)
		_ = unix.Close(shardFD)
		if readErr != nil {
			return domain.RunLogs{}, runLogsUnavailable("terminal run log artifact is unavailable", readErr)
		}
		digest := sha256.Sum256(content)
		if int64(len(content)) != row.CapturedBytes || hex.EncodeToString(digest[:]) != row.ContentSha256 || !utf8.Valid(content) {
			return domain.RunLogs{}, runLogsUnavailable("terminal run log artifact failed verification", nil)
		}
		captured := domain.CapturedLog{Text: tailRunLogText(string(content), tail), CapturedBytes: row.CapturedBytes, OmittedBytes: row.OmittedBytes, Truncated: row.Truncated != 0}
		if row.Kind == domain.CheckArtifactStdout {
			logs.Stdout = captured
		} else {
			logs.Stderr = captured
		}
	}
	if !seen[domain.CheckArtifactStdout] || !seen[domain.CheckArtifactStderr] {
		return domain.RunLogs{}, runLogsUnavailable("terminal run log streams are incomplete", nil)
	}
	return logs, nil
}

type terminalLogReceipt struct {
	available         bool
	unavailableReason string
	stdoutSHA256      string
	stderrSHA256      string
}

func (s *Store) terminalRunLogReceipt(ctx context.Context, run domain.Run) (terminalLogReceipt, error) {
	return terminalRunLogReceiptFrom(ctx, s.db, run)
}

type terminalLogReceiptQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func terminalRunLogReceiptFrom(ctx context.Context, query terminalLogReceiptQuery, run domain.Run) (terminalLogReceipt, error) {
	eventType := ""
	switch run.Status {
	case domain.RunStopped:
		eventType = runStoppedEvent
	case domain.RunReview:
		eventType = runCompletionProposedEvent
	case domain.RunCompleted:
		eventType = runCompletedEvent
	case domain.RunStartFailed:
		eventType = runStartFailedEvent
	case domain.RunFailed:
		eventType = runFailedEvent
		if run.FailureCode == "runtime_retired_by_owner" {
			eventType = runLostResolvedEvent
		}
	default:
		return terminalLogReceipt{}, runLogsUnavailable("run has no terminal log receipt", nil)
	}
	var booleanType, unavailableReason, stdoutSHA256, stderrSHA256 string
	err := query.QueryRowContext(ctx, `SELECT
  COALESCE(json_type(data_json,'$.logs_available'),''),
  COALESCE(json_extract(data_json,'$.logs_unavailable_reason'),''),
  COALESCE(json_extract(data_json,'$.stdout_sha256'),''),
  COALESCE(json_extract(data_json,'$.stderr_sha256'),'')
FROM events WHERE entity_type='run' AND entity_id=? AND type=?
ORDER BY sequence DESC LIMIT 1`, run.ID, eventType).Scan(&booleanType, &unavailableReason, &stdoutSHA256, &stderrSHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return terminalLogReceipt{}, runLogsUnavailable("terminal run log receipt is missing", nil)
	}
	if err != nil {
		return terminalLogReceipt{}, storageFailure("read terminal run log receipt", err)
	}
	switch booleanType {
	case "true":
		if !validLowerSHA256(stdoutSHA256) || !validLowerSHA256(stderrSHA256) || unavailableReason != "" {
			return terminalLogReceipt{}, runLogsUnavailable("available terminal run log receipt is invalid", nil)
		}
		return terminalLogReceipt{available: true, stdoutSHA256: stdoutSHA256, stderrSHA256: stderrSHA256}, nil
	case "false":
		if !validMessageText(unavailableReason, 2048) || stdoutSHA256 != "" || stderrSHA256 != "" {
			return terminalLogReceipt{}, runLogsUnavailable("unavailable terminal run log receipt is invalid", nil)
		}
		return terminalLogReceipt{unavailableReason: unavailableReason}, nil
	default:
		return terminalLogReceipt{}, runLogsUnavailable("terminal run log receipt availability is invalid", nil)
	}
}

func terminalRunStatus(status string) bool {
	switch status {
	case domain.RunStopped, domain.RunReview, domain.RunCompleted, domain.RunStartFailed, domain.RunFailed:
		return true
	default:
		return false
	}
}

func terminalRuntimeState(run domain.Run) string {
	switch {
	case run.Status == domain.RunStopped:
		return "stopped"
	case run.FailureCode == "runtime_timeout":
		return "timed_out"
	default:
		return "exited"
	}
}

func validRuntimeLogState(state string) bool {
	switch state {
	case "starting", "running", "exited", "stopped", "timed_out", "unknown":
		return true
	default:
		return false
	}
}

func tailRunLogText(value string, lines int) string {
	if lines <= 0 {
		return value
	}
	parts := strings.Split(value, "\n")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "\n") + "\n"
}

func runLogsUnavailable(message string, cause error) *Error {
	return &Error{Code: CodeRunLogsUnavailable, Message: message, Cause: cause}
}
