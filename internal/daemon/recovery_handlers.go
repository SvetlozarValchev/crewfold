package daemon

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"

	"crewfold/internal/domain"
	"crewfold/internal/localapi"
	"crewfold/internal/recovery"
	"crewfold/internal/store"
)

const (
	fullDoctorTimeout       = 60 * time.Second
	maximumDoctorSamples    = 20
	maximumDoctorRSSBytes   = int64(512 << 20)
	maximumDoctorStateBytes = int64(1 << 30)
)

type databaseFileProbe struct {
	ByteSize int64
	Code     string
	Detail   string
}

func (s *server) handleSystemDoctorFull(request localapi.Request) localapi.Response {
	var params localapi.SystemDoctorFullParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "system.doctor.full requires empty parameters")
	}
	ctx, cancel := context.WithTimeout(s.leaseReconcileCtx, fullDoctorTimeout)
	defer cancel()
	integrity, err := s.store.VerifyCanonical(ctx, store.CanonicalVerifyOptions{Full: true})
	if err != nil {
		return storeErrorResponse(request, err)
	}
	artifacts, err := recovery.VerifyLiveArtifacts(ctx, s.config.DataDir, integrity.ArtifactReferences)
	if err != nil {
		return recoveryErrorResponse(request, err)
	}
	execution, err := s.store.ExecutionHealth(ctx)
	if err != nil {
		return storeErrorResponse(request, err)
	}
	databaseProbe := probeLiveDatabase(s.store.Path())
	databaseBytes := databaseProbe.ByteSize
	artifactBytes := int64(0)
	for _, reference := range integrity.ArtifactReferences {
		artifactBytes += reference.ByteSize
	}
	rssBytes, openFDs, resourceErr := daemonProcessResources()

	checks := buildFullDoctorChecks(integrity, artifacts, databaseProbe, execution.Node.Unresolved, execution.Services, databaseBytes, artifactBytes, rssBytes, resourceErr)
	status := "ok"
	for _, check := range checks {
		if check.Status == "failed" {
			status = "failed"
			break
		}
		if check.Status == "warning" {
			status = "degraded"
		}
	}
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.FullDoctorResult{
		Schema: localapi.FullDoctorSchema, Type: "full_doctor", Status: status,
		EventSequence: integrity.EventHighWater,
		Baseline: localapi.FullDoctorBaseline{
			SHA256: integrity.Baseline.SourceSHA256, InstalledSchemaSHA256: integrity.Baseline.CatalogSHA256,
		},
		Resources: localapi.FullDoctorResources{
			DatabaseBytes: databaseBytes, ReferencedArtifactBytes: artifactBytes, RSSBytes: rssBytes,
			Goroutines: int64(runtime.NumGoroutine()), OpenFDs: openFDs, FilesystemFreeBytes: int64(artifacts.FreeBytes),
		},
		Limits: localapi.FullDoctorLimits{BriefingClaims: 128, BriefingBytes: 65536, NodeUnresolvedRuns: store.NodeExecutionCapacityLimit},
		Checks: checks,
	})
}

func (s *server) handleBackupCreate(request localapi.Request) localapi.Response {
	var params localapi.BackupCreateParams
	if err := decodeParams(request.Params, &params); err != nil {
		return invalidParamsResponse(request, "backup.create requires target_path and idempotency_key")
	}
	ctx, cancel := context.WithTimeout(s.leaseReconcileCtx, fullDoctorTimeout)
	defer cancel()
	verified, err := recovery.CreateBundle(ctx, s.store, s.config.DataDir, params.TargetPath, params.IdempotencyKey)
	if err != nil {
		return recoveryErrorResponse(request, err)
	}
	manifest := verified.Manifest
	return localapi.MarshalResult(request.ID, request.Protocol, localapi.BackupCreateResult{
		Schema: localapi.BackupCreateSchema, Type: "backup",
		Backup: localapi.BackupSummary{
			ID: manifest.BackupID, Path: verified.Root, CreatedAt: manifest.CreatedAt,
			BaselineSHA256: manifest.BaselineSHA256, EventSequence: manifest.EventHighWater,
			LogicalStateSHA256: manifest.LogicalSHA256, DatabaseSHA256: manifest.Database.SHA256,
			ManifestSHA256: verified.ManifestSHA256, ArtifactCount: int64(len(manifest.Entries)), TotalBytes: manifest.TotalBytes,
		},
	})
}

func buildFullDoctorChecks(
	integrity store.CanonicalIntegrityReport,
	artifacts recovery.ArtifactFilesystemReport,
	databaseProbe databaseFileProbe,
	unresolved int64,
	services domain.ManagedServiceExecutionHealth,
	databaseBytes, artifactBytes, rssBytes int64,
	resourceErr error,
) []localapi.FullDoctorCheck {
	checks := make([]localapi.FullDoctorCheck, 0, len(localapi.FullDoctorCheckOrder()))
	baselineIssues := int64(0)
	physicalIssues := int64(0)
	canonicalIssues := int64(0)
	eventIssues := int64(0)
	projectionIssues := int64(0)
	for _, failure := range integrity.Failures {
		switch failure.Check {
		case "current_baseline":
			baselineIssues++
		case "physical_integrity":
			physicalIssues++
		default:
			canonicalIssues++
		}
	}
	rowsChecked := int64(0)
	for _, family := range integrity.SemanticFamilies {
		rowsChecked += family.RowsStreamed
		for _, violation := range family.Violations {
			switch violation.Check {
			case "known_canonical_event_envelope":
				eventIssues += violation.Count
			case "current_node_runtime_binding", "runtime_binding_parity":
				// Runtime-binding violations have their own exact doctor check.
				// Keeping them out of projection parity makes issue counts
				// unambiguous and gives the owner one remediation surface.
			default:
				projectionIssues += violation.Count
			}
		}
	}
	baselineCheck := doctorCheck("current_baseline", 1, baselineIssues, "exact embedded baseline and installed sqlite_schema agree", "restore_verified_backup")
	physicalCheck := doctorCheck("sqlite_integrity_check", 1, physicalIssues, "full SQLite integrity_check completed", "restore_verified_backup")
	foreignKeyCheck := doctorCheck("foreign_keys", maxDoctorCount(1, integrity.ForeignKeyViolationCount), integrity.ForeignKeyViolationCount, "all foreign-key relationships were scanned", "restore_verified_backup")
	canonicalCheck := doctorCheck("canonical_integrity", maxDoctorCount(1, rowsChecked), canonicalIssues, "every registered canonical table belongs to one verified semantic family", "restore_verified_backup")
	eventCheck := doctorCheck("event_contract", maxDoctorCount(1, integrity.EventHighWater), eventIssues, "known contiguous canonical event envelopes were verified", "report_defect")
	projectionCheck := doctorCheck("projection_receipt_parity", maxDoctorCount(1, rowsChecked), projectionIssues, "projection, receipt, authority, and lifecycle parity was verified", "restore_verified_backup")
	for _, violation := range integrity.ForeignKeyViolations {
		if len(foreignKeyCheck.Samples) == maximumDoctorSamples {
			break
		}
		entityID := violation.Table
		if violation.RowID != nil {
			entityID += ":" + strconv.FormatInt(*violation.RowID, 10)
		}
		foreignKeyCheck.Samples = append(foreignKeyCheck.Samples, localapi.FullDoctorSample{
			EntityType: "database_row", EntityID: boundedDoctorText(entityID, 128), Code: "foreign_key_violation",
			Detail: boundedDoctorText(fmt.Sprintf("%s references missing parent %s through foreign key %d", violation.Table, violation.ParentTable, violation.ForeignKey), 2048),
		})
	}
	for _, failure := range integrity.Failures {
		if failure.Check == "current_baseline" || failure.Check == "physical_integrity" || len(canonicalCheck.Samples) == maximumDoctorSamples {
			continue
		}
		canonicalCheck.Samples = append(canonicalCheck.Samples, localapi.FullDoctorSample{
			EntityType: "integrity_check", EntityID: boundedDoctorText(failure.Check, 128), Code: "canonical_integrity_failure",
			Detail: boundedDoctorText(failure.Detail, 2048),
		})
	}
	for _, family := range integrity.SemanticFamilies {
		for _, violation := range family.Violations {
			target := &projectionCheck
			if violation.Check == "known_canonical_event_envelope" {
				target = &eventCheck
			} else if violation.Check == "current_node_runtime_binding" || violation.Check == "runtime_binding_parity" {
				continue
			}
			for _, sample := range violation.Samples {
				if len(target.Samples) == maximumDoctorSamples {
					break
				}
				target.Samples = append(target.Samples, localapi.FullDoctorSample{
					EntityType: "canonical_row", EntityID: boundedDoctorText(sample, 128), Code: boundedDoctorCode(violation.Check),
					Detail: boundedDoctorText(family.Name+" semantic integrity violation", 2048),
				})
			}
		}
	}
	checks = append(checks, baselineCheck, physicalCheck, foreignKeyCheck, canonicalCheck, eventCheck, projectionCheck)
	artifactCheck := doctorCheck("artifact_integrity", maxDoctorCount(1, artifacts.CheckedCount), artifacts.IssueCount, "referenced immutable artifact hashes and private paths were verified", "restore_verified_backup")
	if artifacts.IssueCount == 0 && artifacts.WarningCount != 0 {
		artifactCheck.Status = "warning"
		artifactCheck.IssueCount = artifacts.WarningCount
		artifactCheck.CheckedCount = maxDoctorCount(artifactCheck.CheckedCount, artifacts.WarningCount)
		artifactCheck.Summary = fmt.Sprintf("artifact closure is valid; %d unreferenced private entries will be excluded from backup", artifacts.WarningCount)
		artifactCheck.Remediation = localapi.FullDoctorRemediation{Kind: "none", Command: []string{}}
	}
	for _, issue := range append(append([]recovery.ArtifactFilesystemIssue{}, artifacts.Issues...), artifacts.Warnings...) {
		if len(artifactCheck.Samples) == maximumDoctorSamples {
			break
		}
		artifactCheck.Samples = append(artifactCheck.Samples, localapi.FullDoctorSample{
			EntityType: "artifact", EntityID: boundedDoctorText(issue.Path, 128), Code: boundedDoctorCode(issue.Code), Detail: boundedDoctorText(issue.Detail, 2048),
		})
	}
	checks = append(checks, artifactCheck)

	derivedIssues := int64(0)
	derivedSummary := "rebuildable knowledge search projection agrees with canonical sources"
	for _, projection := range integrity.DerivedProjections {
		if projection.Status != "ok" {
			derivedIssues++
			derivedSummary = projection.Diagnosis
		}
	}
	derivedCheck := doctorCheck("derived_knowledge_index", maxDoctorCount(1, int64(len(integrity.DerivedProjections))), derivedIssues, derivedSummary, "rebuild_knowledge_index")
	if derivedIssues != 0 {
		derivedCheck.Status = "warning"
	}
	checks = append(checks, derivedCheck)

	runtimeIssues := int64(0)
	runtimeSamples := []localapi.FullDoctorSample{}
	for _, family := range integrity.SemanticFamilies {
		if family.Name != "run" && family.Name != "checks" {
			continue
		}
		for _, violation := range family.Violations {
			if violation.Check != "current_node_runtime_binding" && violation.Check != "runtime_binding_parity" {
				continue
			}
			runtimeIssues += violation.Count
			code := "runtime_binding_parity"
			detail := "live operation is missing its required runtime binding or the binding lifecycle is invalid"
			if violation.Check == "current_node_runtime_binding" {
				code = "foreign_node_binding"
				detail = "binding node ID or key fingerprint differs from this daemon"
			}
			for _, sample := range violation.Samples {
				if len(runtimeSamples) == maximumDoctorSamples {
					break
				}
				runtimeSamples = append(runtimeSamples, localapi.FullDoctorSample{
					EntityType: "runtime_binding", EntityID: boundedDoctorText(sample, 128), Code: code,
					Detail: detail,
				})
			}
		}
	}
	runtimeCheck := doctorCheck("runtime_bindings", maxDoctorCount(1, integrity.Quiescence.Counts.RuntimeBindings), runtimeIssues, "runtime bindings belong to the current node and exact nonterminal operation", "resolve_lost_runtime")
	runtimeCheck.Samples = runtimeSamples
	checks = append(checks, runtimeCheck)
	serviceSummary := "latest managed process state for every active definition is operable or cleanly stopped"
	serviceCheck := doctorCheck("managed_services", maxDoctorCount(1, services.DefinitionCount+services.InstanceCount), services.IssueCount, serviceSummary, "retry")
	for _, issue := range services.Issues {
		detail := "inspect the exact process logs and repair its structural definition or local environment before restarting"
		if issue.Status == domain.ManagedServiceUnknown {
			detail = "the prior process owner is unknown; confirm that its process group ended, then resolve the unknown instance before restarting"
		} else if issue.Status == domain.ManagedServiceDegraded {
			detail = "the process is running but its exact health probe is failing; inspect logs and repair the service or probe before restart"
		}
		if issue.Diagnostic != "" {
			detail += ": " + issue.Diagnostic
		}
		code := issue.DiagnosticCode
		if code == "" {
			code = "managed_service_" + issue.Status
		}
		serviceCheck.Samples = append(serviceCheck.Samples, localapi.FullDoctorSample{
			EntityType: "managed_service", EntityID: boundedDoctorText(issue.InstanceID, 128), Code: boundedDoctorCode(code), Detail: boundedDoctorText(detail, 2048),
		})
	}
	checks = append(checks, serviceCheck)
	queueChecked, queueIssues := int64(len(integrity.DurableQueues)), int64(0)
	for _, queue := range integrity.DurableQueues {
		queueChecked += queue.RowCount
		queueIssues += queue.ViolationCount
	}
	queueCheck := doctorCheck("durable_queues", maxDoctorCount(1, queueChecked), queueIssues, "all registered durable queue rows belong to exactly one open or terminal state set", "retry")
	for _, queue := range integrity.DurableQueues {
		for _, sample := range queue.Samples {
			if len(queueCheck.Samples) == maximumDoctorSamples {
				break
			}
			queueCheck.Samples = append(queueCheck.Samples, localapi.FullDoctorSample{
				EntityType: "durable_queue", EntityID: boundedDoctorText(sample, 128), Code: "queue_partition_violation",
				Detail: boundedDoctorText(queue.Name+"/"+queue.Table+" row is outside the registered open/terminal state partition", 2048),
			})
		}
	}
	databaseIssues := int64(0)
	if databaseProbe.Code != "" {
		databaseIssues = 1
	}
	filesystemCheck := doctorCheck("filesystem_permissions", maxDoctorCount(1, artifacts.CheckedCount+1), int64(artifacts.UnsafeCount)+databaseIssues, "data, live database, and referenced artifact paths are private and nofollow-safe", "restore_verified_backup")
	if databaseProbe.Code != "" {
		filesystemCheck.Samples = append(filesystemCheck.Samples, localapi.FullDoctorSample{
			EntityType: "database", EntityID: "crewfold.db", Code: boundedDoctorCode(databaseProbe.Code), Detail: boundedDoctorText(databaseProbe.Detail, 2048),
		})
	}
	checks = append(checks, queueCheck, filesystemCheck)
	resourceIssues := int64(0)
	resourceStatus := "resource usage is within the personal-scale hard limits"
	if resourceErr != nil || databaseProbe.Code != "" || rssBytes > maximumDoctorRSSBytes || databaseBytes+artifactBytes > maximumDoctorStateBytes || unresolved > store.NodeExecutionCapacityLimit {
		resourceIssues = 1
		resourceStatus = "one or more resource probes or hard personal-scale limits require attention"
	}
	resourceCheck := doctorCheck("resource_budget", 4, resourceIssues, resourceStatus, "free_space")
	if databaseProbe.Code != "" {
		resourceCheck.Remediation = localapi.FullDoctorRemediation{
			Kind:    "restore_verified_backup",
			Command: doctorRemediationCommand("restore_verified_backup"),
		}
		resourceCheck.Samples = append(resourceCheck.Samples, localapi.FullDoctorSample{
			EntityType: "database", EntityID: "crewfold.db", Code: boundedDoctorCode(databaseProbe.Code), Detail: boundedDoctorText(databaseProbe.Detail, 2048),
		})
	}
	checks = append(checks, resourceCheck, doctorCheck("restore_activation", 1, 0, "this running daemon passed pending-restore and activation startup gates", "activate_restore"))
	markIncompleteDoctorChecks(checks, integrity)
	return checks
}

func markIncompleteDoctorChecks(checks []localapi.FullDoctorCheck, integrity store.CanonicalIntegrityReport) {
	if integrity.Complete {
		return
	}
	missing := map[string]bool{
		"current_baseline":          integrity.Baseline.SourceSHA256 == "",
		"sqlite_integrity_check":    integrity.PhysicalIntegrity == "",
		"foreign_keys":              integrity.PhysicalIntegrity == "",
		"canonical_integrity":       integrity.ApplicationTableCount == 0,
		"event_contract":            len(integrity.SemanticFamilies) == 0,
		"projection_receipt_parity": len(integrity.SemanticFamilies) == 0,
		"artifact_integrity":        len(integrity.DerivedProjections) == 0,
		"derived_knowledge_index":   len(integrity.DerivedProjections) == 0,
		"runtime_bindings":          integrity.Quiescence.ProofSHA256 == "",
		"managed_services":          len(integrity.SemanticFamilies) == 0,
		"durable_queues":            len(integrity.DurableQueues) == 0,
		"filesystem_permissions":    len(integrity.DerivedProjections) == 0,
	}
	for index := range checks {
		check := &checks[index]
		if !missing[check.Code] || check.Status == "failed" {
			continue
		}
		check.Status = "failed"
		check.IssueCount++
		check.CheckedCount = maxDoctorCount(check.CheckedCount, check.IssueCount)
		check.Remediation = localapi.FullDoctorRemediation{Kind: "restore_verified_backup", Command: doctorRemediationCommand("restore_verified_backup")}
		if len(check.Samples) < maximumDoctorSamples {
			check.Samples = append(check.Samples, localapi.FullDoctorSample{
				EntityType: "integrity_check", EntityID: check.Code, Code: "canonical_scan_incomplete",
				Detail: "canonical verification stopped before this check produced a complete result",
			})
		}
	}
}

func doctorCheck(code string, checked, issues int64, summary, failureRemediation string) localapi.FullDoctorCheck {
	status := "ok"
	remediation := localapi.FullDoctorRemediation{Kind: "none", Command: []string{}}
	if issues != 0 {
		status = "failed"
		remediation.Kind = failureRemediation
		remediation.Command = doctorRemediationCommand(failureRemediation)
	}
	return localapi.FullDoctorCheck{
		Code: code, Status: status, CheckedCount: maxDoctorCount(checked, issues), IssueCount: issues,
		Summary: boundedDoctorText(summary, 2048), Samples: []localapi.FullDoctorSample{}, Remediation: remediation,
	}
}

func doctorRemediationCommand(kind string) []string {
	switch kind {
	case "rebuild_knowledge_index":
		return []string{"crewfold", "knowledge", "index", "rebuild", "--workspace", "<workspace>", "--socket", "<socket>"}
	case "resolve_lost_runtime":
		return []string{"crewfold", "run", "resolve-lost", "<run-id>", "--confirm-runtime-retired", "--socket", "<socket>"}
	case "free_space":
		return []string{"crewfold", "doctor", "--full", "--socket", "<socket>"}
	case "activate_restore":
		return []string{"crewfold", "backup", "activate", "<data-dir>", "--confirm-source-retired"}
	case "retry":
		return []string{"crewfold", "doctor", "--full", "--socket", "<socket>"}
	case "restore_verified_backup":
		return []string{"crewfold", "backup", "verify", "<bundle-dir>"}
	case "report_defect":
		return []string{"crewfold", "doctor", "--full", "--socket", "<socket>"}
	default:
		return []string{}
	}
}

func maxDoctorCount(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func boundedDoctorText(value string, maximum int) string {
	value = strings.Map(func(char rune) rune {
		if char == 0 || char < 0x20 && char != '\t' {
			return -1
		}
		return char
	}, value)
	if len(value) > maximum {
		return value[:maximum]
	}
	return value
}

func boundedDoctorCode(value string) string {
	value = boundedDoctorText(value, 64)
	if value == "" {
		return "integrity_issue"
	}
	return value
}

func recoveryErrorResponse(request localapi.Request, err error) localapi.Response {
	code := recovery.ErrorCode(err)
	if code == "" {
		code = recovery.CodeBackupIntegrityFailed
	}
	details := map[string]any(nil)
	var recoveryError *recovery.Error
	if errors.As(err, &recoveryError) && recoveryError.Quiescence != nil {
		details = map[string]any{"counts": recoveryError.Quiescence.Counts, "samples": recoveryError.Quiescence.Samples}
	}
	return localapi.ErrorResponse(request.ID, request.Protocol, &localapi.APIError{
		Code: code, Message: err.Error(), Retryable: code == recovery.CodeDatabaseBusy || code == recovery.CodeBackupNotQuiescent || code == recovery.CodeOperationCancelled,
		Details: details,
	})
}
