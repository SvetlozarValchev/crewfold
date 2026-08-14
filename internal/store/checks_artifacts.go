package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"crewfold/internal/domain"
	"crewfold/internal/store/dbgen"

	"golang.org/x/sys/unix"
)

func (s *Store) PrepareCheckArtifact(_ context.Context, kind string, content []byte, omittedBytes int64) (PreparedCheckArtifact, error) {
	kind = strings.TrimSpace(kind)
	if !validCheckArtifactKind(kind) || omittedBytes < 0 || !utf8.Valid(content) || int64(len(content)) > maximumCheckArtifactBytes(kind) {
		return PreparedCheckArtifact{}, checkError(CodeCheckArtifactUnavailable, "check artifact is invalid")
	}
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	shardFD, err := s.openCheckArtifactShard(hash, true)
	if err != nil {
		return PreparedCheckArtifact{}, err
	}
	defer unix.Close(shardFD)
	if existing, err := readCheckArtifactAt(shardFD, hash, maximumCheckArtifactBytes(kind)); err == nil {
		sum := sha256.Sum256(existing)
		if len(existing) != len(content) || hex.EncodeToString(sum[:]) != hash {
			return PreparedCheckArtifact{}, checkError(CodeCheckArtifactUnavailable, "existing check artifact is corrupt")
		}
		return PreparedCheckArtifact{Kind: kind, ContentSHA256: hash, CapturedBytes: int64(len(content)), OmittedBytes: omittedBytes, Truncated: omittedBytes > 0}, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return PreparedCheckArtifact{}, storageFailure("inspect check artifact", err)
	}
	temporaryName, err := randomID(".check-artifact-")
	if err != nil {
		return PreparedCheckArtifact{}, storageFailure("name check artifact staging file", err)
	}
	temporaryFD, err := unix.Openat(shardFD, temporaryName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return PreparedCheckArtifact{}, storageFailure("prepare check artifact", err)
	}
	defer unix.Unlinkat(shardFD, temporaryName, 0)
	temporary := os.NewFile(uintptr(temporaryFD), temporaryName)
	written, writeErr := temporary.Write(content)
	err = writeErr
	if err == nil && written != len(content) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = temporary.Sync()
	}
	closeErr := temporary.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return PreparedCheckArtifact{}, storageFailure("write check artifact", err)
	}
	if err := unix.Linkat(shardFD, temporaryName, shardFD, hash, 0); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return PreparedCheckArtifact{}, storageFailure("commit check artifact", err)
		}
		existing, readErr := readCheckArtifactAt(shardFD, hash, maximumCheckArtifactBytes(kind))
		if readErr != nil {
			return PreparedCheckArtifact{}, checkError(CodeCheckArtifactUnavailable, "raced check artifact is not a bounded regular file")
		}
		existingDigest := sha256.Sum256(existing)
		if len(existing) != len(content) || hex.EncodeToString(existingDigest[:]) != hash {
			return PreparedCheckArtifact{}, checkError(CodeCheckArtifactUnavailable, "raced check artifact is corrupt")
		}
	} else if err := unix.Fsync(shardFD); err != nil {
		return PreparedCheckArtifact{}, storageFailure("sync check artifact directory", err)
	}
	return PreparedCheckArtifact{Kind: kind, ContentSHA256: hash, CapturedBytes: int64(len(content)), OmittedBytes: omittedBytes, Truncated: omittedBytes > 0}, nil
}

func (s *Store) openCheckArtifactShard(hash string, create bool) (int, error) {
	if !validLowerSHA256(hash) {
		return -1, checkError(CodeCheckArtifactUnavailable, "check artifact digest is invalid")
	}
	dataDirectory := filepath.Dir(s.path)
	if !filepath.IsAbs(dataDirectory) || filepath.Clean(dataDirectory) != dataDirectory {
		return -1, checkError(CodeCheckArtifactUnavailable, "check artifact data directory is invalid")
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return -1, storageFailure("open filesystem root for check artifacts", err)
	}
	for _, component := range strings.Split(strings.Trim(dataDirectory, string(filepath.Separator)), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return -1, storageFailure("open check artifact data directory without following links", openErr)
		}
		fd = next
	}
	for _, component := range []string{"check-artifacts", hash[:2]} {
		if create {
			if mkdirErr := unix.Mkdirat(fd, component, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, os.ErrExist) {
				_ = unix.Close(fd)
				return -1, storageFailure("create private check artifact directory", mkdirErr)
			}
		}
		next, openErr := unix.Openat(fd, component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return -1, storageFailure("open private check artifact directory without following links", openErr)
		}
		var stat unix.Stat_t
		if statErr := unix.Fstat(next, &stat); statErr != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Uid != uint32(os.Geteuid()) || stat.Mode&0o077 != 0 {
			_ = unix.Close(next)
			if statErr != nil {
				return -1, storageFailure("inspect private check artifact directory", statErr)
			}
			return -1, checkError(CodeCheckArtifactUnavailable, "check artifact directory is not private and owner-controlled")
		}
		fd = next
	}
	return fd, nil
}
func validCheckArtifactKind(value string) bool {
	return value == domain.CheckArtifactStdout || value == domain.CheckArtifactStderr || value == domain.CheckArtifactDiagnostic
}

func maximumCheckArtifactBytes(kind string) int64 {
	if kind == domain.CheckArtifactDiagnostic {
		return 4096
	}
	return 1 << 20
}

func readCheckArtifactAt(directoryFD int, name string, limit int64) ([]byte, error) {
	fd, err := unix.Openat(directoryFD, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > limit {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("check artifact must be a bounded regular file")
	}
	content, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(content)) > limit {
		if err != nil {
			return nil, err
		}
		return nil, errors.New("check artifact exceeds its bound")
	}
	return content, nil
}

func (s *Store) FinishCheckRun(ctx context.Context, command FinishCheckRunCommand) (domain.CheckRunDetail, error) {
	command.CheckRunID, command.Outcome, command.DiagnosticCode, command.Diagnostic, command.CorrelationID = strings.TrimSpace(command.CheckRunID), strings.TrimSpace(command.Outcome), strings.TrimSpace(command.DiagnosticCode), strings.TrimSpace(command.Diagnostic), strings.TrimSpace(command.CorrelationID)
	if !validCheckOutcome(command.Outcome, command.ExitCode, command.Forced) ||
		!validCheckObservation(command.TerminalObservation) ||
		len(command.Artifacts) > 3 ||
		(command.DiagnosticCode != "" && !validCheckText(command.DiagnosticCode, 128)) ||
		(command.Diagnostic != "" && !validCheckText(command.Diagnostic, 4096)) {
		return domain.CheckRunDetail{}, checkError(CodeInvalidRun, "terminal check bundle is invalid")
	}
	seen := map[string]bool{}
	sort.Slice(command.Artifacts, func(i, j int) bool { return command.Artifacts[i].Kind < command.Artifacts[j].Kind })
	for _, artifact := range command.Artifacts {
		if !validCheckArtifactKind(artifact.Kind) || seen[artifact.Kind] || !validLowerSHA256(artifact.ContentSHA256) || artifact.CapturedBytes < 0 || artifact.OmittedBytes < 0 || artifact.Truncated != (artifact.OmittedBytes > 0) {
			return domain.CheckRunDetail{}, checkError(CodeInvalidRun, "terminal check artifacts are invalid")
		}
		seen[artifact.Kind] = true
		if err := s.verifyPreparedCheckArtifact(artifact); err != nil {
			return domain.CheckRunDetail{}, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.CheckRunDetail{}, err
	}
	defer tx.Rollback()
	work, err := checkWorkInTransaction(ctx, tx, command.CheckRunID)
	if err != nil {
		return domain.CheckRunDetail{}, err
	}
	if work.Run.Status == domain.CheckRunFinished {
		detail, err := checkRunDetailInTransaction(ctx, tx, work.Run.ID)
		if err == nil && !finishCheckReplayMatches(detail, command) {
			return domain.CheckRunDetail{}, checkError(CodeCheckRunConflict, "terminal check bundle replay differs")
		}
		_ = tx.Commit()
		return detail, err
	}
	if (work.Run.Status != domain.CheckRunStarting && work.Run.Status != domain.CheckRunRunning) || work.Job.Status != domain.CheckJobLeased || work.LaunchReceipt == nil {
		return domain.CheckRunDetail{}, checkError(CodeCheckRunConflict, "check finish requires leased started operation and receipt")
	}
	if !checkJobLeaseCurrent(work.Job, s.clock().UTC()) || command.TerminalObservation.RepositoryID != work.Run.RepositoryID || command.TerminalObservation.ObjectFormat != work.Run.RepositoryObjectFormat || command.TerminalObservation.CheckoutID != work.Run.CheckoutID || !validTerminalCheckOutcome(work, command) {
		return domain.CheckRunDetail{}, checkError(CodeCheckRunConflict, "terminal check bundle differs from the receipted operation")
	}
	for _, artifact := range command.Artifacts {
		limit := maximumCheckArtifactBytes(artifact.Kind)
		if artifact.Kind != domain.CheckArtifactDiagnostic && work.Definition.OutputByteLimit < limit {
			limit = work.Definition.OutputByteLimit
		}
		if artifact.CapturedBytes > limit {
			return domain.CheckRunDetail{}, checkError(CodeInvalidRun, "terminal check artifact exceeds the frozen definition bound")
		}
	}
	now := s.nowText()
	resultID, _ := randomID("checkresult_")
	freshID, _ := randomID("checkfresh_")
	evidenceID, _ := randomID("checkevidence_")
	dirtyJSON, _ := json.Marshal(command.TerminalObservation.DirtyPaths)
	freshness, reason, eligible := initialCheckFreshness(work.LaunchReceipt.Observation, command.TerminalObservation)
	effect := domain.CheckEvidenceInconclusive
	if command.Outcome == domain.CheckOutcomePassed && freshness == domain.CheckFreshnessFresh {
		effect = domain.CheckEvidenceSupports
	} else if command.Outcome == domain.CheckOutcomeFailed && freshness == domain.CheckFreshnessFresh {
		effect = domain.CheckEvidenceContradicts
	}
	queries := dbgen.New(tx)
	err = s.withCheckMutationSeal(func() error {
		if err := queries.FinishTerminalCheckRun(ctx, dbgen.FinishTerminalCheckRunParams{FinishedAt: &now, CheckRunID: work.Run.ID}); err != nil {
			return err
		}
		if err := queries.CompleteTerminalCheckJob(ctx, dbgen.CompleteTerminalCheckJobParams{UpdatedAt: now, CheckRunID: work.Run.ID}); err != nil {
			return err
		}
		if err := queries.InsertTerminalCheckResult(ctx, dbgen.InsertTerminalCheckResultParams{
			ID: resultID, CheckRunID: work.Run.ID, RequirementID: work.Run.RequirementID, RequirementRevision: work.Run.RequirementRevision,
			DefinitionID: work.Run.DefinitionID, DefinitionContentRevision: work.Run.DefinitionContentRevision,
			Outcome: command.Outcome, ExitCode: optionalCheckExitCode(command.ExitCode), Forced: boolInteger(command.Forced),
			DiagnosticCode: optionalStringPointer(command.DiagnosticCode), Diagnostic: optionalStringPointer(command.Diagnostic),
			ObservationAvailable: boolInteger(command.TerminalObservation.Available), RepositoryID: command.TerminalObservation.RepositoryID,
			ObjectFormat: command.TerminalObservation.ObjectFormat, CheckoutID: command.TerminalObservation.CheckoutID,
			Branch: optionalStringPointer(command.TerminalObservation.Branch), HeadCommit: optionalStringPointer(command.TerminalObservation.HeadCommit),
			Dirty: boolInteger(command.TerminalObservation.Dirty), DirtyPathsJson: string(dirtyJSON), ObservedAt: command.TerminalObservation.ObservedAt,
			ObservationDiagnosticCode: optionalStringPointer(command.TerminalObservation.DiagnosticCode),
			ObservationDiagnostic:     optionalStringPointer(command.TerminalObservation.Diagnostic), CreatedAt: now,
		}); err != nil {
			return err
		}
		for _, artifact := range command.Artifacts {
			if err := queries.InsertImmutableArtifact(ctx, dbgen.InsertImmutableArtifactParams{
				ContentSha256: artifact.ContentSHA256,
				ByteSize:      artifact.CapturedBytes,
				CreatedAt:     now,
				CreatedBy:     "crewfold-check-worker",
			}); err != nil {
				return err
			}
			id, _ := randomID("checkartifact_")
			if err := queries.InsertTerminalCheckArtifact(ctx, dbgen.InsertTerminalCheckArtifactParams{
				ID: id, CheckResultID: resultID, Kind: artifact.Kind, ContentSha256: artifact.ContentSHA256,
				CapturedBytes: artifact.CapturedBytes, OmittedBytes: artifact.OmittedBytes, Truncated: boolInteger(artifact.Truncated), CreatedAt: now,
			}); err != nil {
				return err
			}
		}
		if err := queries.InsertInitialCheckResultFreshness(ctx, dbgen.InsertInitialCheckResultFreshnessParams{
			ID: freshID, CheckResultID: resultID, Status: freshness, Reason: reason, InitiallyEligible: boolInteger(eligible),
			EverStale: boolInteger(freshness == domain.CheckFreshnessStale), ObservationAvailable: boolInteger(command.TerminalObservation.Available),
			RepositoryID: command.TerminalObservation.RepositoryID, ObjectFormat: command.TerminalObservation.ObjectFormat,
			CheckoutID: command.TerminalObservation.CheckoutID, Branch: optionalStringPointer(command.TerminalObservation.Branch),
			HeadCommit: optionalStringPointer(command.TerminalObservation.HeadCommit), Dirty: boolInteger(command.TerminalObservation.Dirty),
			DirtyPathsJson: string(dirtyJSON), ObservedAt: command.TerminalObservation.ObservedAt,
			DiagnosticCode: optionalStringPointer(command.TerminalObservation.DiagnosticCode),
			Diagnostic:     optionalStringPointer(command.TerminalObservation.Diagnostic), CreatedAt: now,
		}); err != nil {
			return err
		}
		return queries.InsertInitialCheckRequirementEvidence(ctx, dbgen.InsertInitialCheckRequirementEvidenceParams{
			ID: evidenceID, RequirementID: work.Run.RequirementID, RequirementRevision: work.Run.RequirementRevision,
			CheckResultID: resultID, Effect: effect, CreatedAt: now,
		})
	})
	if err != nil {
		return domain.CheckRunDetail{}, checkConstraint("record terminal check bundle", CodeCheckRunConflict, err)
	}
	if err := s.runMutationHook(MutationAfterCheckResult); err != nil {
		return domain.CheckRunDetail{}, err
	}
	if len(command.Artifacts) > 0 {
		if err := s.runMutationHook(MutationAfterCheckArtifact); err != nil {
			return domain.CheckRunDetail{}, err
		}
	}
	if err := s.runMutationHook(MutationAfterCheckFreshness); err != nil {
		return domain.CheckRunDetail{}, err
	}
	if err := s.runMutationHook(MutationAfterCheckEvidence); err != nil {
		return domain.CheckRunDetail{}, err
	}
	if _, _, err := s.materializeCheckNotifications(ctx, tx, work, resultID, command.Outcome, freshness, 1, now, command.CorrelationID); err != nil {
		return domain.CheckRunDetail{}, err
	}
	work.Run.Revision++
	if _, err := appendEventForActor(ctx, tx, work.Run.WorkspaceID, "check_run", work.Run.ID, work.Run.Revision, checkRunFinishedEvent, command.CorrelationID, now, "crewfold-check-worker", "subsystem", map[string]any{"result_id": resultID, "outcome": command.Outcome}); err != nil {
		return domain.CheckRunDetail{}, err
	}
	if _, err := appendEventForActor(ctx, tx, work.Run.WorkspaceID, "check_result", resultID, 1, checkResultRecordedEvent, command.CorrelationID, now, "crewfold-check-worker", "subsystem", map[string]any{"check_run_id": work.Run.ID, "freshness": freshness, "evidence_class": domain.EvidenceMechanicalCheck}); err != nil {
		return domain.CheckRunDetail{}, err
	}
	// A later result supersedes every still-pending repair for the same exact
	// requirement revision. Even an initially unknown result makes the older
	// source non-latest, so leaving its proposal pending would strand policy
	// capacity behind an operation which can no longer be accepted.
	if _, err := s.markCheckRepairsStale(ctx, tx, work.Run.WorkspaceID, resultID, work.Run.RequirementID, work.Run.RequirementRevision, false, now, command.CorrelationID); err != nil {
		return domain.CheckRunDetail{}, err
	}
	if err := s.runMutationHook(MutationAfterCheckResultEvent); err != nil {
		return domain.CheckRunDetail{}, err
	}
	detail, err := checkRunDetailInTransaction(ctx, tx, work.Run.ID)
	if err != nil {
		return domain.CheckRunDetail{}, err
	}
	if err := tx.Commit(); err != nil {
		return domain.CheckRunDetail{}, err
	}
	return detail, nil
}

func (s *Store) verifyPreparedCheckArtifact(artifact PreparedCheckArtifact) error {
	shardFD, err := s.openCheckArtifactShard(artifact.ContentSHA256, false)
	if err != nil {
		return checkError(CodeCheckArtifactUnavailable, "prepared check artifact directory is unavailable")
	}
	defer unix.Close(shardFD)
	content, err := readCheckArtifactAt(shardFD, artifact.ContentSHA256, maximumCheckArtifactBytes(artifact.Kind))
	if err != nil {
		return checkError(CodeCheckArtifactUnavailable, "prepared check artifact is missing")
	}
	sum := sha256.Sum256(content)
	if int64(len(content)) != artifact.CapturedBytes || hex.EncodeToString(sum[:]) != artifact.ContentSHA256 {
		return checkError(CodeCheckArtifactUnavailable, "prepared check artifact hash or size differs")
	}
	return nil
}
func optionalCheckExitCode(value *int) *int64 {
	if value == nil {
		return nil
	}
	converted := int64(*value)
	return &converted
}
func validCheckOutcome(outcome string, exit *int, forced bool) bool {
	switch outcome {
	case domain.CheckOutcomePassed:
		return exit != nil && *exit == 0 && !forced
	case domain.CheckOutcomeFailed:
		return exit != nil && *exit != 0
	case domain.CheckOutcomeTimedOut, domain.CheckOutcomeUnknown:
		return true
	case domain.CheckOutcomeStartFailed:
		return exit == nil && !forced
	}
	return false
}
func initialCheckFreshness(launch, terminal domain.CheckGitObservation) (string, string, bool) {
	if !launch.Available || !terminal.Available || launch.Dirty || terminal.Dirty {
		return domain.CheckFreshnessUnknown, "source observations are dirty, unavailable, or incomplete", false
	}
	if launch.Available && terminal.Available && launch.RepositoryID == terminal.RepositoryID && launch.CheckoutID == terminal.CheckoutID && launch.HeadCommit != "" && launch.HeadCommit == terminal.HeadCommit && !launch.Dirty && !terminal.Dirty {
		return domain.CheckFreshnessFresh, "launch and terminal observations have the same clean HEAD", true
	}
	if launch.Available && terminal.Available && launch.HeadCommit != "" && terminal.HeadCommit != "" && launch.HeadCommit != terminal.HeadCommit {
		return domain.CheckFreshnessStale, "HEAD changed during check", false
	}
	return domain.CheckFreshnessUnknown, "source observations are dirty, unavailable, or incomplete", false
}

func validTerminalCheckOutcome(work CheckWork, command FinishCheckRunCommand) bool {
	switch command.Outcome {
	case domain.CheckOutcomePassed, domain.CheckOutcomeFailed:
		return work.Run.RuntimeHandle != ""
	case domain.CheckOutcomeTimedOut:
		return work.Run.RuntimeHandle != "" && command.DiagnosticCode == "runtime_timeout"
	case domain.CheckOutcomeStartFailed:
		return work.Run.RuntimeHandle == "" && command.ExitCode == nil && !command.Forced
	case domain.CheckOutcomeUnknown:
		return true
	default:
		return false
	}
}

func finishCheckReplayMatches(detail domain.CheckRunDetail, command FinishCheckRunCommand) bool {
	if detail.Result == nil || detail.Result.Outcome != command.Outcome || detail.Result.Forced != command.Forced || detail.Result.DiagnosticCode != command.DiagnosticCode || detail.Result.Diagnostic != command.Diagnostic || !equalCheckObservation(detail.Result.TerminalObservation, command.TerminalObservation) || !equalOptionalExitCode(detail.Result.ExitCode, command.ExitCode) || len(detail.Artifacts) != len(command.Artifacts) {
		return false
	}
	for index, artifact := range detail.Artifacts {
		prepared := command.Artifacts[index]
		if artifact.Kind != prepared.Kind || artifact.ContentSHA256 != prepared.ContentSHA256 || artifact.CapturedBytes != prepared.CapturedBytes || artifact.OmittedBytes != prepared.OmittedBytes || artifact.Truncated != prepared.Truncated {
			return false
		}
	}
	return true
}

func equalOptionalExitCode(left, right *int) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func (s *Store) CheckRunLogs(ctx context.Context, workspaceIdentifier, runID string) (domain.CheckRunLogs, error) {
	detail, err := s.CheckRunDetail(ctx, workspaceIdentifier, runID)
	if err != nil {
		return domain.CheckRunLogs{}, err
	}
	logs := domain.CheckRunLogs{CheckRunID: detail.Run.ID}
	if detail.Result == nil {
		return logs, nil
	}
	rows, err := dbgen.New(s.db).ListCheckArtifactLogMetadata(ctx, detail.Result.ID)
	if err != nil {
		return logs, err
	}
	for _, row := range rows {
		kind, hash := row.Kind, row.ContentSha256
		captured, omitted, truncated := row.CapturedBytes, row.OmittedBytes, row.Truncated
		limit := maximumCheckArtifactBytes(kind)
		if kind != domain.CheckArtifactDiagnostic && detail.Definition.OutputByteLimit < limit {
			limit = detail.Definition.OutputByteLimit
		}
		if !validCheckArtifactKind(kind) || !validLowerSHA256(hash) || captured < 0 || captured > limit || omitted < 0 || (truncated != 0) != (omitted > 0) {
			return domain.CheckRunLogs{}, checkError(CodeCheckArtifactUnavailable, "committed check artifact metadata is invalid")
		}
		shardFD, pathErr := s.openCheckArtifactShard(hash, false)
		if pathErr != nil {
			return domain.CheckRunLogs{}, checkError(CodeCheckArtifactUnavailable, "committed check artifact directory is unavailable")
		}
		content, err := readCheckArtifactAt(shardFD, hash, limit)
		_ = unix.Close(shardFD)
		if err != nil {
			return domain.CheckRunLogs{}, checkError(CodeCheckArtifactUnavailable, "committed check artifact is missing")
		}
		sum := sha256.Sum256(content)
		if int64(len(content)) != captured || hex.EncodeToString(sum[:]) != hash || !utf8.Valid(content) {
			return domain.CheckRunLogs{}, checkError(CodeCheckArtifactUnavailable, "committed check artifact failed verification")
		}
		entry := &domain.CheckCapturedLog{Kind: kind, Content: string(content), CapturedBytes: captured, OmittedBytes: omitted, Truncated: truncated != 0, ContentSHA256: hash}
		switch kind {
		case domain.CheckArtifactStdout:
			logs.Stdout = entry
		case domain.CheckArtifactStderr:
			logs.Stderr = entry
		case domain.CheckArtifactDiagnostic:
			logs.Diagnostic = entry
		}
	}
	return logs, nil
}
