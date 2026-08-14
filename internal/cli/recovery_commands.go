package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"crewfold/internal/localapi"
	"crewfold/internal/recovery"
)

const (
	backupVerifySchema    = "urn:crewfold:schema:cli:backup-verify-response:v1"
	backupRestoreSchema   = "urn:crewfold:schema:cli:backup-restore-response:v1"
	backupActivateSchema  = "urn:crewfold:schema:cli:backup-activate-response:v1"
	repairInspectSchema   = "urn:crewfold:schema:cli:repair-inspect-response:v1"
	recoveryPathMaxBytes  = 4096
	recoveryTextMaxBytes  = 4096
	repairFindingMaxBytes = 2048
)

type offlineRecoveryCheck struct {
	Code    string `json:"code"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

type offlineRecoveryError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type backupVerifyResult struct {
	Schema string                  `json:"schema"`
	Type   string                  `json:"type"`
	Status string                  `json:"status"`
	Path   string                  `json:"path"`
	Backup *localapi.BackupSummary `json:"backup,omitempty"`
	Checks []offlineRecoveryCheck  `json:"checks"`
	Error  *offlineRecoveryError   `json:"error,omitempty"`
}

type backupRestoreResult struct {
	Schema            string `json:"schema"`
	Type              string `json:"type"`
	Status            string `json:"status"`
	BackupID          string `json:"backup_id"`
	ManifestSHA256    string `json:"manifest_sha256"`
	TargetPath        string `json:"target_path"`
	EventSequence     int64  `json:"event_sequence"`
	LogicalSHA256     string `json:"logical_sha256"`
	PendingActivation bool   `json:"pending_activation"`
}

type backupActivateResult struct {
	Schema           string `json:"schema"`
	Type             string `json:"type"`
	Status           string `json:"status"`
	BackupID         string `json:"backup_id"`
	ManifestSHA256   string `json:"manifest_sha256"`
	TargetPath       string `json:"target_path"`
	EventSequence    int64  `json:"event_sequence"`
	LogicalSHA256    string `json:"logical_sha256"`
	NodeFingerprint  string `json:"node_fingerprint"`
	ActivationSHA256 string `json:"activation_sha256"`
	ActivatedAt      string `json:"activated_at"`
	SourceRetired    bool   `json:"source_retired"`
}

type repairBaseline struct {
	SchemaVersion int    `json:"schema_version"`
	SourceSHA256  string `json:"source_sha256"`
	CatalogSHA256 string `json:"catalog_sha256"`
}

type repairFinding struct {
	Code    string `json:"code"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
	Action  string `json:"action"`
}

type repairInspectResult struct {
	Schema         string                     `json:"schema"`
	Type           string                     `json:"type"`
	Status         string                     `json:"status"`
	Path           string                     `json:"path"`
	Baseline       *repairBaseline            `json:"baseline,omitempty"`
	EventSequence  int64                      `json:"event_sequence"`
	LogicalSHA256  string                     `json:"logical_sha256,omitempty"`
	Copied         recovery.RepairCopiedFiles `json:"copied"`
	ArtifactStatus string                     `json:"artifact_status"`
	Findings       []repairFinding            `json:"findings"`
	Truncated      bool                       `json:"truncated"`
	Error          *offlineRecoveryError      `json:"error,omitempty"`
}

func (a *App) runFullDoctor(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	socketPath, failure := requiredOption(options, "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socketPath).SystemDoctorFull(ctx)
	if err != nil {
		return a.writeClientFailure(mode, "run full doctor", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write full doctor output", err))
		}
	} else {
		fmt.Fprintln(a.stdout, "Crewfold full doctor")
		for _, check := range result.Checks {
			fmt.Fprintf(a.stdout, "  %-28s %s", check.Code+":", check.Status)
			if check.Summary != "" {
				fmt.Fprintf(a.stdout, " — %s", check.Summary)
			}
			fmt.Fprintln(a.stdout)
		}
		fmt.Fprintf(a.stdout, "event_sequence: %d\nstatus: %s\n", result.EventSequence, result.Status)
	}
	if result.Status != "ok" {
		return ExitFailure
	}
	return ExitOK
}

func (a *App) runBackup(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("backup requires a subcommand", "run 'crewfold help backup' for usage"))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, backupHelp)
		return ExitOK
	}
	switch args[0] {
	case "create":
		return a.runBackupCreate(ctx, mode, args[1:])
	case "verify":
		return a.runBackupVerify(ctx, mode, args[1:])
	case "restore":
		return a.runBackupRestore(ctx, mode, args[1:])
	case "activate":
		return a.runBackupActivate(ctx, mode, args[1:])
	default:
		return a.writeFailure(mode, commandFailure{
			exitCode: ExitUsage,
			code:     "unknown_backup_command",
			message:  fmt.Sprintf("unknown backup command %q", args[0]),
			hint:     "run 'crewfold help backup' for usage",
		})
	}
}

func (a *App) runBackupActivate(ctx context.Context, mode outputMode, args []string) int {
	target, remaining, failure := requiredLeadingArgument(args, "restored data directory")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	confirmed := false
	for _, argument := range remaining {
		if argument != "--confirm-source-retired" {
			return a.writeFailure(mode, usageFailure(fmt.Sprintf("unknown option %s", argument), "run 'crewfold help backup' for usage"))
		}
		if confirmed {
			return a.writeFailure(mode, usageFailure("--confirm-source-retired may be specified only once", "remove the duplicate confirmation"))
		}
		confirmed = true
	}
	if !confirmed {
		return a.writeFailure(mode, usageFailure("backup activate requires --confirm-source-retired", "retire the source installation, then provide the explicit confirmation"))
	}
	target, failure = canonicalCLIPath(target, "restore target")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	activated, err := a.activateBackup(ctx, target, true)
	if err != nil {
		return a.writeRecoveryFailure(mode, "activate restored data directory", err)
	}
	result := backupActivateResult{
		Schema: backupActivateSchema, Type: "backup_activate", Status: "activated",
		BackupID: activated.BackupID, ManifestSHA256: activated.ManifestSHA256, TargetPath: activated.Path,
		EventSequence: activated.EventHighWater, LogicalSHA256: activated.LogicalSHA256,
		NodeFingerprint: activated.NodeFingerprint, ActivationSHA256: activated.ActivationSHA256,
		ActivatedAt: activated.ActivatedAt, SourceRetired: activated.SourceRetired,
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write backup activation output", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "backup: %s\ntarget: %s\nevent_sequence: %d\nnode_fingerprint: %s\nstatus: activated\n",
			result.BackupID, result.TargetPath, result.EventSequence, result.NodeFingerprint)
	}
	return ExitOK
}

func (a *App) runRepair(ctx context.Context, mode outputMode, args []string) int {
	if len(args) == 0 {
		return a.writeFailure(mode, usageFailure("repair requires a subcommand", "run 'crewfold help repair' for usage"))
	}
	if len(args) == 1 && isHelp(args[0]) {
		fmt.Fprint(a.stdout, repairHelp)
		return ExitOK
	}
	if args[0] != "inspect" {
		return a.writeFailure(mode, commandFailure{exitCode: ExitUsage, code: "unknown_repair_command", message: fmt.Sprintf("unknown repair command %q", args[0]), hint: "run 'crewfold help repair' for usage"})
	}
	return a.runRepairInspect(ctx, mode, args[1:])
}

func (a *App) runRepairInspect(ctx context.Context, mode outputMode, args []string) int {
	target, remaining, failure := requiredLeadingArgument(args, "data directory")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	if _, failure := parseOptions(remaining); failure != nil {
		return a.writeFailure(mode, *failure)
	}
	target, failure = canonicalCLIPath(target, "repair target")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	inspection, err := a.inspectRepair(ctx, target)
	if err != nil {
		result := uninspectableRepairResult(target, err)
		if mode == outputJSON {
			if writeErr := writeJSON(a.stdout, result); writeErr != nil {
				return a.writeFailure(outputText, internalFailure("write failed repair inspection output", writeErr))
			}
		} else {
			fmt.Fprintf(a.stdout, "path: %s\nstatus: uninspectable\ndiagnosis: %s: %s\n", result.Path, result.Error.Code, result.Error.Message)
		}
		return a.writeRecoveryFailure(mode, "inspect offline data directory", err)
	}
	result := repairInspectionResult(inspection)
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write repair inspection output", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "path: %s\nstatus: %s\n", result.Path, result.Status)
		for _, finding := range result.Findings {
			fmt.Fprintf(a.stdout, "  %s: %s — %s (action: %s)\n", finding.Code, finding.Status, finding.Summary, finding.Action)
		}
	}
	if result.Status != "ok" {
		return ExitFailure
	}
	return ExitOK
}

func repairInspectionResult(inspection recovery.RepairInspection) repairInspectResult {
	status := "ok"
	if inspection.Status != "ok" {
		status = "guidance_required"
	}
	result := repairInspectResult{
		Schema: repairInspectSchema, Type: "repair_inspect", Status: status, Path: inspection.Path,
		EventSequence: inspection.Integrity.EventHighWater, LogicalSHA256: inspection.Integrity.LogicalSHA256,
		Copied: inspection.Copied, ArtifactStatus: inspection.Artifacts.Status,
		Findings: make([]repairFinding, 0, len(inspection.Findings)), Truncated: false,
	}
	baseline := inspection.Integrity.Baseline
	if baseline.SchemaVersion != 0 || baseline.SourceSHA256 != "" || baseline.CatalogSHA256 != "" {
		result.Baseline = &repairBaseline{SchemaVersion: baseline.SchemaVersion, SourceSHA256: baseline.SourceSHA256, CatalogSHA256: baseline.CatalogSHA256}
	}
	for _, finding := range inspection.Findings {
		result.Findings = append(result.Findings, repairFinding{
			Code: finding.Code, Status: finding.Status,
			Summary: boundedRecoveryTextTo(finding.Summary, repairFindingMaxBytes), Action: finding.Remediation,
		})
	}
	return result
}

func uninspectableRepairResult(path string, err error) repairInspectResult {
	code := recovery.ErrorCode(err)
	if code == "" {
		code = "recovery_failed"
	}
	return repairInspectResult{
		Schema: repairInspectSchema, Type: "repair_inspect", Status: "uninspectable", Path: path,
		Copied: recovery.RepairCopiedFiles{}, ArtifactStatus: "not_run", Findings: []repairFinding{}, Truncated: false,
		Error: &offlineRecoveryError{Code: code, Message: boundedRecoveryErrorMessage(err)},
	}
}

func (a *App) runBackupVerify(ctx context.Context, mode outputMode, args []string) int {
	bundlePath, remaining, failure := requiredLeadingArgument(args, "backup bundle directory")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	if _, failure := parseOptions(remaining); failure != nil {
		return a.writeFailure(mode, *failure)
	}
	bundlePath, failure = canonicalCLIPath(bundlePath, "backup bundle")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	verified, err := a.verifyBackup(ctx, bundlePath)
	if err != nil {
		result := failedBackupVerifyResult(bundlePath, err)
		if mode == outputJSON {
			if writeErr := writeJSON(a.stdout, result); writeErr != nil {
				return a.writeFailure(outputText, internalFailure("write failed backup verification output", writeErr))
			}
		} else {
			fmt.Fprintf(a.stdout, "path: %s\nstatus: failed\n", result.Path)
			for _, check := range result.Checks {
				fmt.Fprintf(a.stdout, "check_%s: %s - %s\n", check.Code, check.Status, check.Summary)
			}
			fmt.Fprintf(a.stdout, "diagnosis: %s: %s\n", result.Error.Code, result.Error.Message)
		}
		return a.writeRecoveryFailure(mode, "verify backup", err)
	}
	summary := backupSummary(verified)
	result := backupVerifyResult{
		Schema: backupVerifySchema,
		Type:   "backup_verify",
		Status: "ok",
		Path:   verified.Root,
		Backup: &summary,
		Checks: []offlineRecoveryCheck{
			{Code: "manifest", Status: "ok", Summary: "canonical manifest and digest verified"},
			{Code: "file_closure", Status: "ok", Summary: "exact private database and artifact closure verified"},
			{Code: "canonical_integrity", Status: "ok", Summary: "current baseline and canonical database integrity verified"},
			{Code: "quiescence", Status: "ok", Summary: "captured external-effect queues and bindings are quiescent"},
		},
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write backup verification output", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "backup: %s\npath: %s\nevent_sequence: %d\nmanifest_sha256: %s\nstatus: ok\n",
			result.Backup.ID, result.Backup.Path, result.Backup.EventSequence, result.Backup.ManifestSHA256)
	}
	return ExitOK
}

func failedBackupVerifyResult(path string, err error) backupVerifyResult {
	code := recovery.ErrorCode(err)
	if code == "" {
		code = "recovery_failed"
	}
	checks := []offlineRecoveryCheck{
		{Code: "manifest", Status: "failed", Summary: "manifest or bundle envelope could not be verified"},
		{Code: "file_closure", Status: "not_run", Summary: "file closure was not trusted after the earlier failure"},
		{Code: "canonical_integrity", Status: "not_run", Summary: "canonical database integrity was not trusted after the earlier failure"},
		{Code: "quiescence", Status: "not_run", Summary: "quiescence was not trusted after the earlier failure"},
	}
	phase := recovery.VerificationFailurePhase(err)
	switch {
	case phase == recovery.VerificationPhaseFileClosure:
		checks[0].Status = "ok"
		checks[0].Summary = "canonical manifest and digest verified"
		checks[1].Status = "failed"
		checks[1].Summary = "exact private database and artifact closure verification failed"
	case phase == recovery.VerificationPhaseCanonicalIntegrity || phase == "" && (code == recovery.CodeCurrentBaselineMismatch || code == recovery.CodeCanonicalIntegrityFailed):
		checks[0].Status = "ok"
		checks[0].Summary = "canonical manifest and digest verified"
		checks[1].Status = "ok"
		checks[1].Summary = "declared private file closure passed byte verification"
		checks[2].Status = "failed"
		checks[2].Summary = "database current-baseline or canonical integrity verification failed"
	case phase == recovery.VerificationPhaseQuiescence || phase == "" && code == recovery.CodeRestoreUnsafeNonterminal:
		checks[0].Status = "ok"
		checks[0].Summary = "canonical manifest and digest verified"
		checks[1].Status = "ok"
		checks[1].Summary = "exact private database and artifact closure verified"
		checks[2].Status = "ok"
		checks[2].Summary = "current baseline and canonical database integrity verified"
		checks[3].Status = "failed"
		checks[3].Summary = "captured state is not a quiescent recovery cut"
	}
	return backupVerifyResult{
		Schema: backupVerifySchema, Type: "backup_verify", Status: "failed", Path: path, Checks: checks,
		Error: &offlineRecoveryError{Code: code, Message: boundedRecoveryErrorMessage(err)},
	}
}

func (a *App) runBackupRestore(ctx context.Context, mode outputMode, args []string) int {
	bundlePath, remaining, failure := requiredLeadingArgument(args, "backup bundle directory")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	options, failure := parseOptions(remaining, "to")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	targetPath, failure := requiredOption(options, "to")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	bundlePath, failure = canonicalCLIPath(bundlePath, "backup bundle")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	targetPath, failure = canonicalCLIPath(targetPath, "restore target")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	pending, err := a.restoreBackup(ctx, bundlePath, targetPath)
	if err != nil {
		return a.writeRecoveryFailure(mode, "restore backup", err)
	}
	result := backupRestoreResult{
		Schema: backupRestoreSchema, Type: "backup_restore", Status: "pending_activation",
		BackupID: pending.BackupID, ManifestSHA256: pending.ManifestSHA256, TargetPath: pending.Path,
		EventSequence: pending.EventHighWater, LogicalSHA256: pending.LogicalSHA256, PendingActivation: true,
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write backup restore output", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "backup: %s\ntarget: %s\nevent_sequence: %d\nstatus: pending_activation\n",
			result.BackupID, result.TargetPath, result.EventSequence)
		fmt.Fprintln(a.stdout, "next: retire the source installation, then run 'crewfold backup activate <target> --confirm-source-retired'")
	}
	return ExitOK
}

func backupSummary(verified recovery.VerifiedBundle) localapi.BackupSummary {
	manifest := verified.Manifest
	return localapi.BackupSummary{
		ID: manifest.BackupID, Path: verified.Root, CreatedAt: manifest.CreatedAt,
		BaselineSHA256: manifest.BaselineSHA256, EventSequence: manifest.EventHighWater,
		LogicalStateSHA256: manifest.LogicalSHA256, DatabaseSHA256: manifest.Database.SHA256,
		ManifestSHA256: verified.ManifestSHA256, ArtifactCount: int64(len(manifest.Entries)), TotalBytes: manifest.TotalBytes,
	}
}

func canonicalCLIPath(path, description string) (string, *commandFailure) {
	if !utf8.ValidString(path) || strings.IndexFunc(path, recoveryTerminalUnsafeRune) >= 0 {
		failure := usageFailure(description+" path must be valid UTF-8 without terminal control or bidirectional formatting characters", "choose a terminal-safe filesystem path")
		return "", &failure
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		failure := usageFailure(description+" path cannot be resolved", "choose a valid path")
		return "", &failure
	}
	absolute = filepath.Clean(absolute)
	if len(absolute) > recoveryPathMaxBytes || !utf8.ValidString(absolute) || strings.IndexFunc(absolute, recoveryTerminalUnsafeRune) >= 0 {
		failure := usageFailure(description+" path must resolve to terminal-safe valid UTF-8 of at most 4096 bytes", "choose a shorter terminal-safe filesystem path")
		return "", &failure
	}
	if absolute == string(filepath.Separator) {
		failure := usageFailure(description+" path may not resolve to the filesystem root", "choose a dedicated recovery directory")
		return "", &failure
	}
	return absolute, nil
}

func boundedRecoveryErrorMessage(err error) string {
	if err == nil {
		return "recovery operation failed"
	}
	return boundedRecoveryText(err.Error())
}

func boundedRecoveryText(value string) string {
	return boundedRecoveryTextTo(value, recoveryTextMaxBytes)
}

func boundedRecoveryTextTo(value string, maximum int) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	value = strings.Map(func(character rune) rune {
		if recoveryTerminalUnsafeRune(character) {
			return ' '
		}
		return character
	}, value)
	value = strings.TrimSpace(value)
	if value == "" {
		return "recovery operation failed"
	}
	if len(value) <= maximum {
		return value
	}
	end := maximum
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	if end == 0 {
		return "recovery operation failed"
	}
	return value[:end]
}

func recoveryTerminalUnsafeRune(character rune) bool {
	if character <= 0x1f || character == 0x7f || character >= 0x80 && character <= 0x9f {
		return true
	}
	switch character {
	case 0x061c, 0x200e, 0x200f, 0x2028, 0x2029, 0x202a, 0x202b, 0x202c, 0x202d, 0x202e,
		0x2066, 0x2067, 0x2068, 0x2069, 0x206a, 0x206b, 0x206c, 0x206d, 0x206e, 0x206f:
		return true
	default:
		return false
	}
}

func (a *App) writeRecoveryFailure(mode outputMode, operation string, err error) int {
	code := recovery.ErrorCode(err)
	if code == "" {
		code = "recovery_failed"
	}
	hint := "inspect the recovery finding and retry without modifying the source or target"
	switch code {
	case recovery.CodeCurrentBaselineMismatch:
		hint = "use a bundle created by this exact current Crewfold contract"
	case recovery.CodeBackupIntegrityFailed, recovery.CodeCanonicalIntegrityFailed:
		hint = "verify another private bundle; Crewfold will not repair or import this one"
	case recovery.CodeBackupTargetExists, recovery.CodeRestoreTargetExists:
		hint = "choose a new nonexistent target directory"
	case recovery.CodeRestoreNotActivated:
		hint = "retire the source installation, then explicitly activate this pending restore"
	}
	return a.writeFailure(mode, commandFailure{exitCode: ExitFailure, code: code, message: boundedRecoveryText(fmt.Sprintf("%s: %v", operation, err)), hint: hint})
}

func (a *App) runBackupCreate(ctx context.Context, mode outputMode, args []string) int {
	options, failure := parseOptions(args, "socket", "to", "idempotency-key")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	socketPath, failure := requiredOption(options, "socket")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	target, failure := requiredOption(options, "to")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	target, failure = canonicalCLIPath(target, "backup target")
	if failure != nil {
		return a.writeFailure(mode, *failure)
	}
	result, err := a.newClient(socketPath).BackupCreate(ctx, localapi.BackupCreateParams{
		TargetPath: target, IdempotencyKey: options["idempotency-key"],
	})
	if err != nil {
		return a.writeClientFailure(mode, "create backup", err)
	}
	if mode == outputJSON {
		if err := writeJSON(a.stdout, result); err != nil {
			return a.writeFailure(outputText, internalFailure("write backup creation output", err))
		}
	} else {
		fmt.Fprintf(a.stdout, "backup: %s\npath: %s\nevent_sequence: %d\nmanifest_sha256: %s\ntotal_bytes: %d\n",
			result.Backup.ID, result.Backup.Path, result.Backup.EventSequence, result.Backup.ManifestSHA256, result.Backup.TotalBytes)
	}
	return ExitOK
}

const backupHelp = `Usage:
  crewfold backup create --socket <path> --to <new-bundle-dir> [--idempotency-key <key>]
  crewfold backup verify <bundle-dir>
  crewfold backup restore <bundle-dir> --to <new-data-dir>
  crewfold backup activate <new-data-dir> --confirm-source-retired
`

const repairHelp = `Usage:
  crewfold repair inspect <data-dir>

Inspect an offline data directory through a private nofollow-safe database copy.
The command never edits, migrates, rebuilds, salvages, or deletes selected data.
`
