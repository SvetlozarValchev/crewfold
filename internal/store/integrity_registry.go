package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

type integrityTableClass string

const (
	integrityTableDomain  integrityTableClass = "canonical_domain"
	integrityTableControl integrityTableClass = "durable_control_receipt_queue"
	integrityTableDerived integrityTableClass = "rebuildable_derived"
)

// durableControlReceiptTables are durable non-domain rows which are not
// independently scheduled external-effect queues. Queue tables are declared
// separately below together with their exact open-state rule.
var durableControlReceiptTables = map[string]bool{
	"schema_baseline": true, "idempotency_keys": true,
	"run_runtime_bindings": true, "run_loss_resolutions": true,
	"run_context_bindings": true, "run_capabilities": true, "run_reports": true,
	"knowledge_authority_checks": true, "knowledge_contradiction_authority_checks": true,
	"checkout_claim_scans": true, "context_delta_acknowledgements": true,
	"manager_proposal_submissions": true, "manager_proposal_decisions": true,
	"supervisor_action_receipts": true, "supervisor_state": true,
	"run_scheduling_receipts": true, "run_retry_receipts": true,
	"check_runtime_bindings": true, "check_launch_receipts": true,
	"check_notification_receipts": true, "check_repair_decisions": true,
	"check_repair_effects": true, "check_watch_state": true, "check_watch_receipts": true,
	"outcome_commitment_receipts": true, "outcome_assessment_submissions": true,
	"outcome_assessment_governance": true, "outcome_assessment_acceptance_basis": true,
	"outcome_projector_state": true, "management_briefing_receipts": true,
}

type durableQueueDefinition struct {
	name          string
	healthName    string
	table         string
	idColumn      string
	openPredicate string
	terminalRule  string
	blockerKind   string
	statuses      []string
}

// durableQueueRegistry is the single source of truth for externally scheduled
// durable work. Each rule names all nonterminal states; both doctor/backup
// quiescence counts and bounded blocker samples are evaluated from this list.
var durableQueueRegistry = []durableQueueDefinition{
	{name: "run_job", healthName: "run", table: "run_jobs", idColumn: "run_id", openPredicate: "status IN ('pending','leased')", terminalRule: "status='complete'", blockerKind: "unsettled_run_job", statuses: []string{"pending", "leased", "complete"}},
	{name: "check_job", healthName: "check", table: "check_jobs", idColumn: "check_run_id", openPredicate: "status IN ('pending','leased')", terminalRule: "status='complete'", blockerKind: "unsettled_check_job", statuses: []string{"pending", "leased", "complete"}},
	{name: "message_wake", healthName: "message_wake", table: "message_wake_jobs", idColumn: "id", openPredicate: "status IN ('pending','leased')", terminalRule: "status IN ('succeeded','failed','failed_unknown')", blockerKind: "open_wake_job", statuses: []string{"pending", "leased", "succeeded", "failed", "failed_unknown"}},
	{name: "scheduling_intent", healthName: "scheduling_intent", table: "scheduling_intents", idColumn: "id", openPredicate: "status IN ('pending','deferred','awaiting_approval','run_requested')", terminalRule: "status IN ('satisfied','failed','cancelled')", blockerKind: "open_scheduling_intent", statuses: []string{"pending", "deferred", "awaiting_approval", "run_requested", "satisfied", "failed", "cancelled"}},
	{name: "supervisor_action", healthName: "supervisor_action", table: "supervisor_actions", idColumn: "id", openPredicate: "status IN ('proposed','awaiting_approval','deferred')", terminalRule: "status IN ('applied','dismissed','failed')", blockerKind: "open_supervisor_action", statuses: []string{"proposed", "awaiting_approval", "deferred", "applied", "dismissed", "failed"}},
	{name: "approval", healthName: "approval", table: "approval_requests", idColumn: "id", openPredicate: "status IN ('pending','granted')", terminalRule: "status IN ('denied','expired','consumed')", blockerKind: "open_approval", statuses: []string{"pending", "granted", "denied", "expired", "consumed"}},
	{name: "owner_manager_review", healthName: "owner_manager_review", table: "owner_manager_review_jobs", idColumn: "project_id", openPredicate: "status IN ('pending','leased')", terminalRule: "status IN ('idle','failed')", blockerKind: "open_owner_manager_review", statuses: []string{"idle", "pending", "leased", "failed"}},
}

func queueDefinition(name string) (durableQueueDefinition, bool) {
	for _, definition := range durableQueueRegistry {
		if definition.name == name {
			return definition, true
		}
	}
	return durableQueueDefinition{}, false
}

func queueTableNames() map[string]bool {
	result := make(map[string]bool, len(durableQueueRegistry))
	for _, definition := range durableQueueRegistry {
		result[definition.table] = true
	}
	return result
}

func tableClassificationFailures(ctx context.Context, query baselineQuery) ([]string, error) {
	rows, err := query.QueryContext(ctx, `SELECT name FROM sqlite_schema WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	actual := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		actual[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	semanticOwners := map[string]int{}
	for _, family := range semanticFamilyRegistry {
		for _, table := range family.tables {
			semanticOwners[table]++
		}
	}
	queues := queueTableNames()
	failures := []string{}
	for table := range actual {
		owners := 0
		if derivedKnowledgeTables[table] {
			owners++
		}
		if durableControlReceiptTables[table] {
			owners++
		}
		if queues[table] {
			owners++
		}
		if semanticOwners[table] == 1 && !derivedKnowledgeTables[table] && !durableControlReceiptTables[table] && !queues[table] {
			owners++
		}
		if owners != 1 {
			failures = append(failures, fmt.Sprintf("baseline table %q has %d integrity classifications", table, owners))
		}
	}
	for table := range durableControlReceiptTables {
		if !actual[table] {
			failures = append(failures, fmt.Sprintf("durable control registry names missing table %q", table))
		}
	}
	seenNames, seenTables := map[string]bool{}, map[string]bool{}
	for _, definition := range durableQueueRegistry {
		if definition.name == "" || definition.healthName == "" || definition.table == "" || definition.idColumn == "" || definition.openPredicate == "" || definition.terminalRule == "" || definition.blockerKind == "" || len(definition.statuses) == 0 {
			failures = append(failures, "durable queue registry contains an incomplete rule")
		}
		if seenNames[definition.name] || seenTables[definition.table] {
			failures = append(failures, fmt.Sprintf("durable queue registry duplicates %q/%q", definition.name, definition.table))
		}
		seenNames[definition.name], seenTables[definition.table] = true, true
		if !actual[definition.table] {
			failures = append(failures, fmt.Sprintf("durable queue %q names missing table %q", definition.name, definition.table))
		}
		seenStatuses := map[string]bool{}
		for _, status := range definition.statuses {
			if status == "" || seenStatuses[status] {
				failures = append(failures, fmt.Sprintf("durable queue %q has an empty or duplicate declared status %q", definition.name, status))
				continue
			}
			seenStatuses[status] = true
			var partitions int
			statement := fmt.Sprintf("SELECT (CASE WHEN %s THEN 1 ELSE 0 END) + (CASE WHEN %s THEN 1 ELSE 0 END) FROM (SELECT ? AS status)", definition.openPredicate, definition.terminalRule)
			if err := query.QueryRowContext(ctx, statement, status).Scan(&partitions); err != nil {
				return nil, err
			}
			if partitions != 1 {
				failures = append(failures, fmt.Sprintf("durable queue %q status %q belongs to %d open/terminal partitions", definition.name, status, partitions))
			}
		}
	}
	for table := range derivedKnowledgeTables {
		if !actual[table] {
			failures = append(failures, fmt.Sprintf("derived registry names missing table %q", table))
		}
	}
	sort.Strings(failures)
	return failures, nil
}

func inspectDurableQueues(ctx context.Context, query baselineQuery) ([]DurableQueueIntegrity, error) {
	result := make([]DurableQueueIntegrity, 0, len(durableQueueRegistry))
	for _, definition := range durableQueueRegistry {
		item := DurableQueueIntegrity{Name: definition.name, Table: definition.table, Status: "ok", Samples: []string{}}
		statement := fmt.Sprintf(`SELECT COUNT(*),
 COALESCE(SUM(CASE WHEN %s THEN 1 ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN %s THEN 1 ELSE 0 END),0),
 COALESCE(SUM(CASE WHEN NOT ((%s) OR (%s)) OR ((%s) AND (%s)) THEN 1 ELSE 0 END),0)
FROM %s`, definition.openPredicate, definition.terminalRule,
			definition.openPredicate, definition.terminalRule, definition.openPredicate, definition.terminalRule,
			quoteSQLiteIdentifier(definition.table))
		if err := query.QueryRowContext(ctx, statement).Scan(&item.RowCount, &item.OpenCount, &item.TerminalCount, &item.ViolationCount); err != nil {
			return nil, err
		}
		if item.ViolationCount != 0 {
			item.Status = "failed"
			sampleStatement := fmt.Sprintf(`SELECT %s FROM %s
WHERE NOT ((%s) OR (%s)) OR ((%s) AND (%s))
ORDER BY %s LIMIT ?`, quoteSQLiteIdentifier(definition.idColumn), quoteSQLiteIdentifier(definition.table),
				definition.openPredicate, definition.terminalRule, definition.openPredicate, definition.terminalRule,
				quoteSQLiteIdentifier(definition.idColumn))
			rows, err := query.QueryContext(ctx, sampleStatement, maximumSemanticViolationSamples)
			if err != nil {
				return nil, err
			}
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					rows.Close()
					return nil, err
				}
				item.Samples = append(item.Samples, id)
			}
			if err := rows.Close(); err != nil {
				return nil, err
			}
			if err := rows.Err(); err != nil {
				return nil, err
			}
		}
		result = append(result, item)
	}
	return result, nil
}

func integrityTableClassFor(table string) (integrityTableClass, bool) {
	if derivedKnowledgeTables[table] {
		return integrityTableDerived, true
	}
	if durableControlReceiptTables[table] || queueTableNames()[table] {
		return integrityTableControl, true
	}
	for _, family := range semanticFamilyRegistry {
		for _, owned := range family.tables {
			if owned == table {
				return integrityTableDomain, true
			}
		}
	}
	return "", false
}

func queueOpenCount(ctx context.Context, query baselineQuery, definition durableQueueDefinition) (int64, error) {
	statement := fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s", quoteSQLiteIdentifier(definition.table), definition.openPredicate)
	var count int64
	if err := query.QueryRowContext(ctx, statement).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func queueBlockers(ctx context.Context, query baselineQuery, definition durableQueueDefinition, limit int) ([]QuiescenceBlocker, error) {
	if limit <= 0 {
		return []QuiescenceBlocker{}, nil
	}
	statement := fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY %s LIMIT ?",
		quoteSQLiteIdentifier(definition.idColumn), quoteSQLiteIdentifier(definition.table), definition.openPredicate, quoteSQLiteIdentifier(definition.idColumn))
	rows, err := query.QueryContext(ctx, statement, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []QuiescenceBlocker{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, QuiescenceBlocker{Kind: strings.TrimSpace(definition.blockerKind), EntityID: id})
	}
	return result, rows.Err()
}
