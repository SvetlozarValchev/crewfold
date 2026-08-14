package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	maximumReportedForeignKeyViolations = 100
	maximumQuiescenceBlockerSamples     = 20
)

type semanticFamilyDefinition struct {
	name   string
	tables []string
}

type logicalColumn struct {
	name       string
	notNull    bool
	primaryKey int
}

var semanticFamilyRegistry = []semanticFamilyDefinition{
	{name: "core", tables: []string{
		"workspaces", "events", "idempotency_keys",
	}},
	{name: "project", tables: []string{
		"projects", "repositories", "project_repositories", "checkouts", "agents",
		"objectives", "tasks", "task_dependencies", "task_assignments",
	}},
	{name: "run", tables: []string{
		"runs", "run_runtime_bindings", "immutable_artifacts", "run_log_artifacts", "run_loss_resolutions",
		"run_jobs", "run_timeline", "run_handoffs", "context_packets",
		"run_context_bindings", "run_capabilities", "run_reports", "run_artifacts",
		"run_tool_calls",
	}},
	{name: "coordination", tables: []string{
		"work_claims", "work_overlaps", "task_coordination_holds", "claim_drifts",
		"checkout_claim_scans",
	}},
	{name: "meeting", tables: []string{
		"meetings", "meeting_participants", "meeting_contributions", "meeting_proposals",
		"meeting_actions", "task_roles",
	}},
	{name: "knowledge", tables: []string{
		"knowledge_items", "knowledge_revisions", "knowledge_sources",
		"knowledge_authority_checks", "thread_participants",
		"curator_rules", "curator_derivations", "curator_auto_acceptances",
		"knowledge_contradictions", "knowledge_contradiction_authority_checks",
		"knowledge_task_scope_anchors", "knowledge_item_task_scopes", "knowledge_imports",
		"knowledge_import_entities",
	}},
	{name: "context", tables: []string{
		"run_context_delta_state", "context_deltas", "context_delta_acknowledgements",
	}},
	{name: "management", tables: []string{
		"manager_grants", "launch_profiles", "manager_grant_proposal_kinds",
		"manager_grant_launch_profiles", "manager_grant_claim_kinds", "manager_proposals",
		"manager_proposal_effects", "manager_proposal_actions", "manager_proposal_submissions",
		"manager_proposal_decisions", "task_claim_requirements", "supervisor_policies",
		"supervisor_policy_project_limits", "supervisor_policy_provider_limits",
		"supervisor_actions", "supervisor_action_receipts", "approval_requests",
		"supervisor_state", "run_scheduling_receipts", "run_retry_receipts",
		"scheduling_intents",
	}},
	{name: "messaging", tables: []string{
		"message_threads", "messages", "message_recipients", "message_wake_jobs",
	}},
	{name: "checks", tables: []string{
		"check_definitions", "check_definition_arguments", "task_check_requirements",
		"check_watch_grants", "check_watch_grant_operations", "check_watch_grant_definitions",
		"check_policies", "check_routes", "check_runs", "check_runtime_bindings", "check_jobs",
		"check_launch_receipts", "check_results", "check_artifacts",
		"check_result_freshness", "check_requirement_evidence",
		"check_notification_receipts", "check_route_failures", "check_repair_proposals",
		"check_repair_decisions", "check_repair_effects", "check_watch_state",
		"check_watch_receipts",
	}},
	{name: "outcomes", tables: []string{
		"deliverable_commitments", "outcome_commitment_receipts", "outcome_assessments",
		"outcome_assessment_decision_refs", "outcome_assessment_evidence_refs",
		"outcome_assessment_effects", "outcome_assessment_deviations",
		"outcome_assessment_risks", "outcome_assessment_unknowns",
		"outcome_assessment_follow_up_tasks", "outcome_assessment_owner_attention",
		"outcome_assessment_submissions", "outcome_assessment_governance",
		"outcome_assessment_acceptance_basis", "owner_checkpoints",
		"outcome_projector_state", "management_briefings", "management_briefing_claims",
		"management_briefing_claim_sources", "management_briefing_receipts",
	}},
}

var derivedKnowledgeTables = map[string]bool{
	"knowledge_search":          true,
	"knowledge_search_data":     true,
	"knowledge_search_idx":      true,
	"knowledge_search_content":  true,
	"knowledge_search_docsize":  true,
	"knowledge_search_config":   true,
	"knowledge_search_metadata": true,
}

func (s *Store) VerifyCanonical(ctx context.Context, options CanonicalVerifyOptions) (CanonicalIntegrityReport, error) {
	report := CanonicalIntegrityReport{
		Status:               "failed",
		ForeignKeyViolations: []ForeignKeyViolation{},
		SemanticFamilies:     []SemanticIntegrityFamily{},
		DurableQueues:        []DurableQueueIntegrity{},
		DerivedProjections:   []DerivedProjectionIntegrity{},
		ArtifactReferences:   []ImmutableArtifactReference{},
		QuiescenceBlockers:   []QuiescenceBlocker{},
		Failures:             []CanonicalIntegrityIssue{},
	}
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return report, storageFailure("begin canonical integrity verification", err)
	}
	defer tx.Rollback()

	report.Baseline, err = verifyBaselineIdentity(ctx, tx)
	if err != nil {
		report.Failures = append(report.Failures, CanonicalIntegrityIssue{Check: "current_baseline", Detail: err.Error()})
		return report, nil
	}

	pragma := "PRAGMA quick_check(1)"
	if options.Full {
		pragma = "PRAGMA integrity_check"
	}
	report.PhysicalIntegrity, err = runIntegrityPragma(ctx, tx, pragma)
	if err != nil {
		return report, storageFailure("run canonical physical integrity check", err)
	}
	if report.PhysicalIntegrity != "ok" {
		report.Failures = append(report.Failures, CanonicalIntegrityIssue{Check: "physical_integrity", Detail: report.PhysicalIntegrity})
	}

	report.ForeignKeyViolations, report.ForeignKeyViolationCount, err = collectForeignKeyViolations(ctx, tx)
	if err != nil {
		return report, storageFailure("run canonical foreign-key verification", err)
	}
	if report.ForeignKeyViolationCount != 0 {
		report.Failures = append(report.Failures, CanonicalIntegrityIssue{
			Check: "foreign_keys", Detail: fmt.Sprintf("%d foreign-key violations", report.ForeignKeyViolationCount),
		})
	}

	tables, err := applicationTableNames(ctx, tx)
	if err != nil {
		return report, storageFailure("enumerate current application tables", err)
	}
	report.ApplicationTableCount = len(tables)
	classificationFailures, err := tableClassificationFailures(ctx, tx)
	if err != nil {
		return report, storageFailure("validate exact integrity table registry", err)
	}
	for _, failure := range classificationFailures {
		report.Failures = append(report.Failures, CanonicalIntegrityIssue{Check: "table_registry", Detail: failure})
	}
	if len(classificationFailures) != 0 {
		return report, nil
	}
	report.DurableQueues, err = inspectDurableQueues(ctx, tx)
	if err != nil {
		return report, storageFailure("verify durable queue partitions", err)
	}
	for _, queue := range report.DurableQueues {
		if queue.ViolationCount != 0 {
			report.Failures = append(report.Failures, CanonicalIntegrityIssue{
				Check:  "durable_queue_" + queue.Name,
				Detail: fmt.Sprintf("%d rows are outside the exact open/terminal partition", queue.ViolationCount),
			})
		}
	}
	if ownershipFailures := semanticOwnershipFailures(tables); len(ownershipFailures) != 0 {
		for _, failure := range ownershipFailures {
			report.Failures = append(report.Failures, CanonicalIntegrityIssue{Check: "semantic_ownership", Detail: failure})
		}
		return report, nil
	}

	report.SemanticFamilies, report.LogicalSHA256, err = streamSemanticFamilies(ctx, tx, s.runtimeNodeID, s.runtimeNodeFingerprint)
	if err != nil {
		return report, storageFailure("stream canonical semantic families", err)
	}
	for _, family := range report.SemanticFamilies {
		if family.Status != "ok" {
			report.Failures = append(report.Failures, CanonicalIntegrityIssue{Check: "semantic_" + family.Name, Detail: family.Detail})
		}
	}
	derivedStatus, derivedDiagnosis, err := validateKnowledgeFTSReadOnly(ctx, tx)
	if err != nil {
		return report, storageFailure("verify rebuildable knowledge projection", err)
	}
	report.DerivedProjections = append(report.DerivedProjections, DerivedProjectionIntegrity{
		Name: "knowledge_search", Status: derivedStatus, Diagnosis: derivedDiagnosis,
	})
	report.ArtifactReferences, err = immutableArtifactReferences(ctx, tx)
	if err != nil {
		return report, storageFailure("verify immutable artifact references", err)
	}
	var invalidArtifactLinks int64
	if err := tx.QueryRowContext(ctx, `
SELECT
 (SELECT COUNT(*) FROM immutable_artifacts artifact
  WHERE NOT EXISTS(SELECT 1 FROM check_artifacts checked WHERE checked.content_sha256=artifact.content_sha256)
    AND NOT EXISTS(SELECT 1 FROM run_log_artifacts run_log WHERE run_log.content_sha256=artifact.content_sha256))
 +
 (SELECT COUNT(*) FROM check_artifacts checked
  LEFT JOIN immutable_artifacts artifact ON artifact.content_sha256=checked.content_sha256
  WHERE artifact.content_sha256 IS NULL OR artifact.byte_size<>checked.captured_bytes)
 +
 (SELECT COUNT(*) FROM run_log_artifacts run_log
  LEFT JOIN immutable_artifacts artifact ON artifact.content_sha256=run_log.content_sha256
  WHERE artifact.content_sha256 IS NULL OR artifact.byte_size<>run_log.captured_bytes)
`).Scan(&invalidArtifactLinks); err != nil {
		return report, storageFailure("verify immutable artifact closure", err)
	}
	if invalidArtifactLinks != 0 {
		report.Failures = append(report.Failures, CanonicalIntegrityIssue{
			Check: "immutable_artifacts", Detail: fmt.Sprintf("%d orphaned or size-inconsistent artifact links", invalidArtifactLinks),
		})
	}
	if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(sequence),0) FROM events").Scan(&report.EventHighWater); err != nil {
		return report, storageFailure("read canonical event high-water", err)
	}
	report.Quiescence, err = quiescentCutInTransaction(ctx, tx)
	if err != nil {
		return report, err
	}
	if !report.Quiescence.Quiescent {
		report.QuiescenceBlockers, err = quiescenceBlockerSamples(ctx, tx)
		if err != nil {
			return report, storageFailure("read quiescence blocker samples", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return report, storageFailure("finish canonical integrity verification", err)
	}
	report.Complete = true
	if len(report.Failures) == 0 {
		report.Status = "ok"
	}
	return report, nil
}

func immutableArtifactReferences(ctx context.Context, query baselineQuery) ([]ImmutableArtifactReference, error) {
	rows, err := query.QueryContext(ctx, `
SELECT referenced.content_sha256,referenced.byte_size,referenced.kind
FROM (
  SELECT artifact.content_sha256 AS content_sha256,
         artifact.byte_size AS byte_size,
         'check_artifact' AS kind
  FROM immutable_artifacts artifact
  WHERE EXISTS(
    SELECT 1 FROM check_artifacts checked
    WHERE checked.content_sha256=artifact.content_sha256
  )
  UNION ALL
  SELECT artifact.content_sha256 AS content_sha256,
         artifact.byte_size AS byte_size,
         'run_log_artifact' AS kind
  FROM immutable_artifacts artifact
  WHERE EXISTS(
    SELECT 1 FROM run_log_artifacts run_log
    WHERE run_log.content_sha256=artifact.content_sha256
  )
) referenced
ORDER BY referenced.kind,referenced.content_sha256`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]ImmutableArtifactReference, 0)
	for rows.Next() {
		var reference ImmutableArtifactReference
		if err := rows.Scan(&reference.ContentSHA256, &reference.ByteSize, &reference.Kind); err != nil {
			return nil, err
		}
		result = append(result, reference)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func runIntegrityPragma(ctx context.Context, query baselineQuery, pragma string) (string, error) {
	rows, err := query.QueryContext(ctx, pragma)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	result := ""
	issueCount := 0
	for rows.Next() {
		var current string
		if err := rows.Scan(&current); err != nil {
			return "", err
		}
		if current == "ok" && issueCount == 0 {
			result = "ok"
			continue
		}
		issueCount++
		if issueCount == 1 {
			result = current
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if result == "" {
		return "", fmt.Errorf("%s returned no result", pragma)
	}
	if issueCount > 1 {
		result = fmt.Sprintf("%s (and %d more failures)", result, issueCount-1)
	}
	return result, nil
}

func collectForeignKeyViolations(ctx context.Context, query baselineQuery) ([]ForeignKeyViolation, int64, error) {
	rows, err := query.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	violations := make([]ForeignKeyViolation, 0)
	var count int64
	for rows.Next() {
		var table, parent string
		var rowID sql.NullInt64
		var foreignKey int64
		if err := rows.Scan(&table, &rowID, &parent, &foreignKey); err != nil {
			return nil, 0, err
		}
		count++
		if len(violations) < maximumReportedForeignKeyViolations {
			violation := ForeignKeyViolation{Table: table, ParentTable: parent, ForeignKey: foreignKey}
			if rowID.Valid {
				value := rowID.Int64
				violation.RowID = &value
			}
			violations = append(violations, violation)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return violations, count, nil
}

func applicationTableNames(ctx context.Context, query baselineQuery) ([]string, error) {
	rows, err := query.QueryContext(ctx, `
SELECT name FROM sqlite_schema
WHERE type='table' AND name NOT LIKE 'sqlite_%'
ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		if name == "schema_baseline" || derivedKnowledgeTables[name] {
			continue
		}
		result = append(result, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func semanticOwnershipFailures(actualTables []string) []string {
	actual := make(map[string]bool, len(actualTables))
	for _, table := range actualTables {
		actual[table] = true
	}
	owners := make(map[string][]string, len(actualTables))
	for _, family := range semanticFamilyRegistry {
		for _, table := range family.tables {
			owners[table] = append(owners[table], family.name)
		}
	}
	failures := make([]string, 0)
	for _, table := range actualTables {
		if len(owners[table]) != 1 {
			failures = append(failures, fmt.Sprintf("application table %q has %d semantic owners", table, len(owners[table])))
		}
	}
	for table, tableOwners := range owners {
		if !actual[table] {
			failures = append(failures, fmt.Sprintf("semantic family %q owns missing application table %q", strings.Join(tableOwners, ","), table))
		}
		if len(tableOwners) > 1 {
			failures = append(failures, fmt.Sprintf("application table %q is duplicated across semantic families %q", table, strings.Join(tableOwners, ",")))
		}
	}
	sort.Strings(failures)
	return failures
}

func streamSemanticFamilies(ctx context.Context, tx *sql.Tx, runtimeNodeID, runtimeNodeFingerprint string) ([]SemanticIntegrityFamily, string, error) {
	overall := sha256.New()
	result := make([]SemanticIntegrityFamily, 0, len(semanticFamilyRegistry))
	for _, definition := range semanticFamilyRegistry {
		family := SemanticIntegrityFamily{
			Name: definition.name, Tables: append([]string(nil), definition.tables...),
			Status: "ok", Violations: []SemanticIntegrityViolation{},
		}
		sort.Strings(family.Tables)
		familyDigest := sha256.New()
		writer := io.MultiWriter(overall, familyDigest)
		if err := writeCatalogField(writer, true, family.Name); err != nil {
			return nil, "", err
		}
		for _, table := range family.Tables {
			rows, err := streamLogicalTable(ctx, tx, writer, table)
			if err != nil {
				family.Status = "failed"
				family.Detail = err.Error()
				result = append(result, family)
				return result, "", err
			}
			family.RowsStreamed += rows
		}
		family.LogicalSHA256 = hex.EncodeToString(familyDigest.Sum(nil))
		violations, validationErr := validateSemanticFamily(ctx, tx, family.Name, runtimeNodeID, runtimeNodeFingerprint)
		if validationErr != nil {
			return result, "", fmt.Errorf("validate semantic family %s: %w", family.Name, validationErr)
		}
		family.Violations = violations
		for _, violation := range family.Violations {
			family.ViolationCount += violation.Count
		}
		if family.ViolationCount != 0 {
			family.Status = "failed"
			family.Detail = fmt.Sprintf("%d semantic violations across %d checks", family.ViolationCount, len(family.Violations))
		}
		result = append(result, family)
	}
	return result, hex.EncodeToString(overall.Sum(nil)), nil
}

func streamLogicalTable(ctx context.Context, tx *sql.Tx, writer io.Writer, table string) (int64, error) {
	columns, err := tableColumns(ctx, tx, table)
	if err != nil {
		return 0, err
	}
	if len(columns) == 0 {
		return 0, fmt.Errorf("application table %q has no visible columns", table)
	}
	if err := writeCatalogField(writer, true, table); err != nil {
		return 0, err
	}
	quoted := make([]string, len(columns))
	for index, column := range columns {
		if err := writeCatalogField(writer, true, column.name); err != nil {
			return 0, err
		}
		quoted[index] = quoteSQLiteIdentifier(column.name)
	}
	orderColumns, err := logicalTableOrderColumns(ctx, tx, table, columns)
	if err != nil {
		return 0, err
	}
	orderBy := make([]string, len(orderColumns))
	for index, column := range orderColumns {
		orderBy[index] = quoteSQLiteIdentifier(column)
	}
	statement := fmt.Sprintf("SELECT %s FROM %s ORDER BY %s", strings.Join(quoted, ","), quoteSQLiteIdentifier(table), strings.Join(orderBy, ","))
	rows, err := tx.QueryContext(ctx, statement)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var count int64
	for rows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := rows.Scan(destinations...); err != nil {
			return 0, err
		}
		for _, value := range values {
			if err := writeLogicalValue(writer, value); err != nil {
				return 0, err
			}
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	return count, nil
}

func tableColumns(ctx context.Context, tx *sql.Tx, table string) ([]logicalColumn, error) {
	rows, err := tx.QueryContext(ctx, "PRAGMA table_xinfo("+quoteSQLiteIdentifier(table)+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns := make([]logicalColumn, 0)
	for rows.Next() {
		var cid, notNull, primaryKey, hidden int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey, &hidden); err != nil {
			return nil, err
		}
		if hidden == 0 {
			columns = append(columns, logicalColumn{name: name, notNull: notNull == 1, primaryKey: primaryKey})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return columns, nil
}

func logicalTableOrderColumns(ctx context.Context, tx *sql.Tx, table string, columns []logicalColumn) ([]string, error) {
	primaryKey := make([]logicalColumn, 0)
	for _, column := range columns {
		if column.primaryKey > 0 {
			primaryKey = append(primaryKey, column)
		}
	}
	if len(primaryKey) != 0 {
		sort.Slice(primaryKey, func(left, right int) bool { return primaryKey[left].primaryKey < primaryKey[right].primaryKey })
		result := make([]string, len(primaryKey))
		for index, column := range primaryKey {
			result[index] = column.name
		}
		return result, nil
	}

	notNull := make(map[string]bool, len(columns))
	for _, column := range columns {
		notNull[column.name] = column.notNull
	}
	rows, err := tx.QueryContext(ctx, "PRAGMA index_list("+quoteSQLiteIdentifier(table)+")")
	if err != nil {
		return nil, err
	}
	type uniqueIndex struct {
		sequence int
		name     string
	}
	indexes := make([]uniqueIndex, 0)
	for rows.Next() {
		var sequence, unique, partial int
		var name, origin string
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if unique == 1 && partial == 0 {
			indexes = append(indexes, uniqueIndex{sequence: sequence, name: name})
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	sort.Slice(indexes, func(left, right int) bool {
		if indexes[left].sequence != indexes[right].sequence {
			return indexes[left].sequence < indexes[right].sequence
		}
		return indexes[left].name < indexes[right].name
	})
	for _, index := range indexes {
		indexRows, err := tx.QueryContext(ctx, "PRAGMA index_xinfo("+quoteSQLiteIdentifier(index.name)+")")
		if err != nil {
			return nil, err
		}
		key := make([]string, 0)
		valid := true
		for indexRows.Next() {
			var sequence, cid, descending, keyColumn int
			var name sql.NullString
			var collation string
			if err := indexRows.Scan(&sequence, &cid, &name, &descending, &collation, &keyColumn); err != nil {
				_ = indexRows.Close()
				return nil, err
			}
			if keyColumn != 1 {
				continue
			}
			if cid < 0 || !name.Valid || !notNull[name.String] {
				valid = false
				continue
			}
			key = append(key, name.String)
		}
		if err := indexRows.Close(); err != nil {
			return nil, err
		}
		if valid && len(key) != 0 {
			return key, nil
		}
	}

	// Truly keyless tables are rare and bounded by their family validator. Only
	// they pay the cost of an all-column deterministic sort.
	fallback := make([]string, len(columns))
	for index, column := range columns {
		fallback[index] = column.name
	}
	return fallback, nil
}

func quoteSQLiteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func writeLogicalValue(writer io.Writer, value any) error {
	switch value := value.(type) {
	case nil:
		_, err := writer.Write([]byte{0})
		return err
	case int64:
		return writeFixedLogicalValue(writer, 1, uint64(value))
	case float64:
		return writeFixedLogicalValue(writer, 2, math.Float64bits(value))
	case string:
		return writeVariableLogicalValue(writer, 3, []byte(value))
	case []byte:
		return writeVariableLogicalValue(writer, 4, value)
	case bool:
		integer := uint64(0)
		if value {
			integer = 1
		}
		return writeFixedLogicalValue(writer, 5, integer)
	case time.Time:
		return writeVariableLogicalValue(writer, 6, []byte(value.UTC().Format(time.RFC3339Nano)))
	default:
		return fmt.Errorf("unsupported SQLite logical value type %T", value)
	}
}

func writeFixedLogicalValue(writer io.Writer, kind byte, value uint64) error {
	var encoded [9]byte
	encoded[0] = kind
	binary.BigEndian.PutUint64(encoded[1:], value)
	_, err := writer.Write(encoded[:])
	return err
}

func writeVariableLogicalValue(writer io.Writer, kind byte, value []byte) error {
	if _, err := writer.Write([]byte{kind}); err != nil {
		return err
	}
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	if _, err := writer.Write(size[:]); err != nil {
		return err
	}
	_, err := writer.Write(value)
	return err
}

func (s *Store) CheckQuiescentCut(ctx context.Context) (QuiescentCut, error) {
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return QuiescentCut{}, storageFailure("begin quiescence check", err)
	}
	defer tx.Rollback()
	cut, err := quiescentCutInTransaction(ctx, tx)
	if err != nil {
		return QuiescentCut{}, err
	}
	if err := tx.Commit(); err != nil {
		return QuiescentCut{}, storageFailure("finish quiescence check", err)
	}
	return cut, nil
}

func quiescentCutInTransaction(ctx context.Context, tx *sql.Tx) (QuiescentCut, error) {
	var cut QuiescentCut
	err := tx.QueryRowContext(ctx, `
SELECT
 (SELECT COALESCE(MAX(sequence),0) FROM events),
 (SELECT COUNT(*) FROM runs WHERE status IN ('requested','starting','active','blocked','stopping','lost')),
 ((SELECT COUNT(*) FROM run_runtime_bindings) + (SELECT COUNT(*) FROM check_runtime_bindings)),
 (SELECT COUNT(*) FROM check_runs WHERE status<>'finished')
`).Scan(
		&cut.EventHighWater,
		&cut.Counts.NonterminalRuns,
		&cut.Counts.RuntimeBindings,
		&cut.Counts.UnfinishedCheckRuns,
	)
	if err != nil {
		return QuiescentCut{}, storageFailure("read quiescence cut", err)
	}
	for _, definition := range durableQueueRegistry {
		count, err := queueOpenCount(ctx, tx, definition)
		if err != nil {
			return QuiescentCut{}, storageFailure("read "+definition.name+" quiescence", err)
		}
		switch definition.name {
		case "run_job":
			cut.Counts.UnsettledRunJobs = count
		case "check_job":
			cut.Counts.UnsettledCheckJobs = count
		case "message_wake":
			cut.Counts.OpenWakeJobs = count
		case "scheduling_intent":
			cut.Counts.OpenSchedulingIntents = count
		case "supervisor_action":
			cut.Counts.OpenSupervisorActions = count
		case "approval":
			cut.Counts.OpenApprovals = count
		default:
			return QuiescentCut{}, storageFailure("map durable queue quiescence", fmt.Errorf("unmapped durable queue %q", definition.name))
		}
	}
	cut.Quiescent = quiescenceCountsZero(cut.Counts)
	proof, err := json.Marshal(struct {
		EventHighWater int64            `json:"event_high_water"`
		Counts         QuiescenceCounts `json:"counts"`
	}{EventHighWater: cut.EventHighWater, Counts: cut.Counts})
	if err != nil {
		return QuiescentCut{}, storageFailure("encode quiescence proof", err)
	}
	digest := sha256.Sum256(proof)
	cut.ProofSHA256 = hex.EncodeToString(digest[:])
	return cut, nil
}

func quiescenceCountsZero(counts QuiescenceCounts) bool {
	return counts.NonterminalRuns == 0 &&
		counts.UnsettledRunJobs == 0 &&
		counts.RuntimeBindings == 0 &&
		counts.UnfinishedCheckRuns == 0 &&
		counts.UnsettledCheckJobs == 0 &&
		counts.OpenWakeJobs == 0 &&
		counts.OpenSchedulingIntents == 0 &&
		counts.OpenSupervisorActions == 0 &&
		counts.OpenApprovals == 0
}

func quiescenceBlockerSamples(ctx context.Context, tx *sql.Tx) ([]QuiescenceBlocker, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT blocker.kind,blocker.entity_id FROM (
 SELECT 'nonterminal_run' AS kind,id AS entity_id FROM runs WHERE status IN ('requested','starting','active','blocked','stopping','lost')
 UNION ALL SELECT 'run_runtime_binding',run_id FROM run_runtime_bindings
 UNION ALL SELECT 'check_runtime_binding',check_run_id FROM check_runtime_bindings
 UNION ALL SELECT 'unfinished_check_run',id FROM check_runs WHERE status<>'finished'
) blocker ORDER BY blocker.kind,blocker.entity_id LIMIT ?`, maximumQuiescenceBlockerSamples)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]QuiescenceBlocker, 0, maximumQuiescenceBlockerSamples)
	for rows.Next() {
		var blocker QuiescenceBlocker
		if err := rows.Scan(&blocker.Kind, &blocker.EntityID); err != nil {
			return nil, err
		}
		result = append(result, blocker)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, definition := range durableQueueRegistry {
		blockers, err := queueBlockers(ctx, tx, definition, maximumQuiescenceBlockerSamples)
		if err != nil {
			return nil, err
		}
		result = append(result, blockers...)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		return result[i].EntityID < result[j].EntityID
	})
	if len(result) > maximumQuiescenceBlockerSamples {
		result = result[:maximumQuiescenceBlockerSamples]
	}
	return result, nil
}
