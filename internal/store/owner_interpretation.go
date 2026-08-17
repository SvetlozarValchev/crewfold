package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"crewfold/internal/domain"
)

// OwnerInterpretationSnapshot is the exact read-only input to one provider
// manager turn. CanonicalContext is bounded, canonically encoded, and tied to
// EventSequence; Citations is the only namespace provider citation refs may
// select from.
type OwnerInterpretationSnapshot struct {
	WorkspaceID      string
	ProjectID        string
	CheckoutPath     string
	Provider         string
	EventSequence    int64
	CanonicalContext []byte
	Citations        map[string]domain.OwnerCitation
}

// BuildOwnerInterpretationSnapshot captures all manager-visible records in
// one SQLite read snapshot. It deliberately carries no executable, runtime
// handle, credentials, policy mutation, or raw authority token.
func (s *Store) BuildOwnerInterpretationSnapshot(ctx context.Context, workspaceIdentifier, projectIdentifier string) (OwnerInterpretationSnapshot, error) {
	tx, err := s.beginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return OwnerInterpretationSnapshot{}, storageFailure("begin owner interpretation snapshot", err)
	}
	defer tx.Rollback()
	workspace, err := workspaceInTransaction(ctx, tx, strings.TrimSpace(workspaceIdentifier))
	if err != nil {
		return OwnerInterpretationSnapshot{}, err
	}
	project, err := queryProject(ctx, tx, workspace.ID, strings.TrimSpace(projectIdentifier))
	if err != nil {
		return OwnerInterpretationSnapshot{}, err
	}
	var highWater int64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(sequence),0) FROM events WHERE workspace_id=?`, workspace.ID).Scan(&highWater); err != nil {
		return OwnerInterpretationSnapshot{}, storageFailure("read owner interpretation event cut", err)
	}
	var checkoutID, checkoutPath, checkoutWriteMode, checkoutBranch string
	if err := tx.QueryRowContext(ctx, `SELECT id,path,write_mode,COALESCE(branch,'') FROM checkouts WHERE project_id=? AND availability='available' ORDER BY created_at,id LIMIT 1`, project.ID).Scan(&checkoutID, &checkoutPath, &checkoutWriteMode, &checkoutBranch); errors.Is(err, sql.ErrNoRows) {
		return OwnerInterpretationSnapshot{}, &Error{Code: CodePlacementUnavailable, Message: "owner manager requires one available project checkout"}
	} else if err != nil {
		return OwnerInterpretationSnapshot{}, storageFailure("read owner interpretation checkout", err)
	}

	citations := make(map[string]domain.OwnerCitation)
	facts := make([]map[string]any, 0, 128)
	addFact := func(ref, entityType, entityID string, revision int64, label string, value map[string]any) {
		value["ref"] = ref
		facts = append(facts, value)
		citations[ref] = domain.OwnerCitation{Ref: ref, EntityType: entityType, EntityID: entityID, EntityRevision: revision, AsOfEventSequence: highWater, Label: label}
	}
	addFact("project:current", "project", project.ID, project.Revision, project.Name, map[string]any{"kind": "project", "id": project.ID, "name": project.Name, "revision": project.Revision})
	addFact("checkout:current", "checkout", checkoutID, 0, checkoutPath, map[string]any{"kind": "checkout", "id": checkoutID, "path": checkoutPath, "write_mode": checkoutWriteMode, "branch": checkoutBranch})

	agents := make([]map[string]any, 0, 32)
	agentRows, err := tx.QueryContext(ctx, `SELECT id,name,role,provider,runtime,enabled,max_concurrency,revision FROM agents WHERE workspace_id=? ORDER BY name,id LIMIT 32`, workspace.ID)
	if err != nil {
		return OwnerInterpretationSnapshot{}, storageFailure("read owner interpretation agents", err)
	}
	for agentRows.Next() {
		var id, name, role, provider, runtime string
		var enabled bool
		var maximum int
		var revision int64
		if err := agentRows.Scan(&id, &name, &role, &provider, &runtime, &enabled, &maximum, &revision); err != nil {
			agentRows.Close()
			return OwnerInterpretationSnapshot{}, storageFailure("scan owner interpretation agent", err)
		}
		value := map[string]any{"id": id, "name": name, "role": role, "provider": provider, "runtime": runtime, "enabled": enabled, "max_concurrency": maximum, "revision": revision}
		agents = append(agents, value)
		addFact("agent:"+id, "agent", id, revision, name, map[string]any{"kind": "agent", "id": id, "name": name, "provider": provider, "runtime": runtime, "enabled": enabled, "revision": revision})
	}
	if err := agentRows.Close(); err != nil {
		return OwnerInterpretationSnapshot{}, storageFailure("close owner interpretation agents", err)
	}

	profiles := make([]map[string]any, 0, 32)
	profileRows, err := tx.QueryContext(ctx, launchProfileSelect+` WHERE workspace_id=? AND project_id=? AND status='active' AND manager_grant_id IS NULL ORDER BY created_at,id LIMIT 32`, workspace.ID, project.ID)
	if err != nil {
		return OwnerInterpretationSnapshot{}, storageFailure("read owner interpretation launch profiles", err)
	}
	provider := ""
	for profileRows.Next() {
		profile, err := scanLaunchProfile(profileRows)
		if err != nil {
			profileRows.Close()
			return OwnerInterpretationSnapshot{}, err
		}
		profiles = append(profiles, map[string]any{"id": profile.ID, "agent_id": profile.AgentID, "agent_revision": profile.AgentRevision, "purpose": profile.Purpose, "runtime": profile.Runtime, "provider": profile.Provider, "checkout_id": profile.CheckoutID, "revision": profile.Revision})
		addFact("profile:"+profile.ID, "launch_profile", profile.ID, profile.Revision, profile.Purpose, map[string]any{"kind": "launch_profile", "id": profile.ID, "agent_id": profile.AgentID, "purpose": profile.Purpose, "runtime": profile.Runtime, "provider": profile.Provider, "revision": profile.Revision})
		if provider == "" {
			provider = profile.Provider
		}
	}
	if err := profileRows.Close(); err != nil {
		return OwnerInterpretationSnapshot{}, storageFailure("close owner interpretation launch profiles", err)
	}
	if len(profiles) == 0 {
		return OwnerInterpretationSnapshot{}, &Error{Code: CodeInvalidLaunchProfile, Message: "owner manager requires at least one active project launch profile"}
	}

	objectives := make([]map[string]any, 0, 32)
	objectiveRows, err := tx.QueryContext(ctx, `SELECT id,title,status,budget_tokens,budget_cost_cents,budget_time_seconds,revision FROM objectives WHERE workspace_id=? AND project_id=? ORDER BY created_at,id LIMIT 32`, workspace.ID, project.ID)
	if err != nil {
		return OwnerInterpretationSnapshot{}, storageFailure("read owner interpretation objectives", err)
	}
	for objectiveRows.Next() {
		var id, title, status string
		var tokens, cost, seconds int64
		var revision int64
		if err := objectiveRows.Scan(&id, &title, &status, &tokens, &cost, &seconds, &revision); err != nil {
			objectiveRows.Close()
			return OwnerInterpretationSnapshot{}, storageFailure("scan owner interpretation objective", err)
		}
		objectives = append(objectives, map[string]any{"id": id, "title": title, "status": status, "budget": domain.Budget{TokenLimit: tokens, CostCents: cost, TimeSeconds: seconds}, "revision": revision})
		addFact("objective:"+id, "objective", id, revision, title, map[string]any{"kind": "objective", "id": id, "title": title, "status": status, "revision": revision})
	}
	objectiveRows.Close()

	tasks := make([]map[string]any, 0, 100)
	taskRows, err := tx.QueryContext(ctx, `SELECT id,COALESCE(objective_id,''),title,COALESCE(description,''),status,COALESCE(blocked_reason,''),priority,budget_tokens,budget_cost_cents,budget_time_seconds,revision FROM tasks WHERE workspace_id=? AND project_id=? ORDER BY created_at,id LIMIT 100`, workspace.ID, project.ID)
	if err != nil {
		return OwnerInterpretationSnapshot{}, storageFailure("read owner interpretation tasks", err)
	}
	for taskRows.Next() {
		var id, objectiveID, title, description, status, blocked string
		var priority int
		var tokens, cost, seconds int64
		var revision int64
		if err := taskRows.Scan(&id, &objectiveID, &title, &description, &status, &blocked, &priority, &tokens, &cost, &seconds, &revision); err != nil {
			taskRows.Close()
			return OwnerInterpretationSnapshot{}, storageFailure("scan owner interpretation task", err)
		}
		var dependencies []string
		dependencyRows, dependencyErr := tx.QueryContext(ctx, `SELECT depends_on_task_id FROM task_dependencies WHERE task_id=? ORDER BY depends_on_task_id`, id)
		if dependencyErr != nil {
			taskRows.Close()
			return OwnerInterpretationSnapshot{}, storageFailure("read owner interpretation dependencies", dependencyErr)
		}
		for dependencyRows.Next() {
			var dependency string
			if err := dependencyRows.Scan(&dependency); err != nil {
				dependencyRows.Close()
				taskRows.Close()
				return OwnerInterpretationSnapshot{}, storageFailure("scan owner interpretation dependency", err)
			}
			dependencies = append(dependencies, dependency)
		}
		dependencyRows.Close()
		readiness, err := taskReadiness(ctx, tx, domain.Task{ID: id, Status: status})
		if err != nil {
			taskRows.Close()
			return OwnerInterpretationSnapshot{}, err
		}
		tasks = append(tasks, map[string]any{"id": id, "objective_id": objectiveID, "title": title, "description": description, "status": status, "readiness": readiness, "blocked_reason": blocked, "priority": priority, "budget": domain.Budget{TokenLimit: tokens, CostCents: cost, TimeSeconds: seconds}, "revision": revision, "depends_on": dependencies})
		addFact("task:"+id, "task", id, revision, title, map[string]any{"kind": "task", "id": id, "title": title, "status": status, "readiness": readiness, "blocked_reason": blocked, "revision": revision})
	}
	taskRows.Close()

	runs := make([]map[string]any, 0, 100)
	runRows, err := tx.QueryContext(ctx, `SELECT id,task_id,agent_id,runtime,provider,status,COALESCE(blocked_question,''),COALESCE(result_summary,''),COALESCE(failure_code,''),revision,updated_at FROM runs WHERE workspace_id=? AND project_id=? ORDER BY created_at DESC,id DESC LIMIT 100`, workspace.ID, project.ID)
	if err != nil {
		return OwnerInterpretationSnapshot{}, storageFailure("read owner interpretation runs", err)
	}
	for runRows.Next() {
		var id, taskID, agentID, runtime, runProvider, status, question, result, failure, updatedAt string
		var revision int64
		if err := runRows.Scan(&id, &taskID, &agentID, &runtime, &runProvider, &status, &question, &result, &failure, &revision, &updatedAt); err != nil {
			runRows.Close()
			return OwnerInterpretationSnapshot{}, storageFailure("scan owner interpretation run", err)
		}
		runs = append(runs, map[string]any{"id": id, "task_id": taskID, "agent_id": agentID, "runtime": runtime, "provider": runProvider, "status": status, "blocked_question": question, "result_summary": result, "failure_code": failure, "revision": revision, "updated_at": updatedAt})
		addFact("run:"+id, "run", id, revision, id, map[string]any{"kind": "run", "id": id, "task_id": taskID, "status": status, "blocked_question": question, "result_summary": result, "failure_code": failure, "revision": revision})
	}
	runRows.Close()

	reports := make([]map[string]any, 0, 50)
	reportRows, err := tx.QueryContext(ctx, `SELECT report.id,report.run_id,run.task_id,report.kind,report.message,COALESCE(report.handoff,''),report.evidence_json,report.status,report.created_at
FROM run_reports report JOIN runs run ON run.id=report.run_id
WHERE run.workspace_id=? AND run.project_id=? ORDER BY report.sequence DESC LIMIT 50`, workspace.ID, project.ID)
	if err != nil {
		return OwnerInterpretationSnapshot{}, storageFailure("read owner interpretation reports", err)
	}
	for reportRows.Next() {
		var id, runID, taskID, kind, message, handoff, evidenceJSON, status, createdAt string
		if err := reportRows.Scan(&id, &runID, &taskID, &kind, &message, &handoff, &evidenceJSON, &status, &createdAt); err != nil {
			reportRows.Close()
			return OwnerInterpretationSnapshot{}, storageFailure("scan owner interpretation report", err)
		}
		var evidence []string
		if err := json.Unmarshal([]byte(evidenceJSON), &evidence); err != nil {
			reportRows.Close()
			return OwnerInterpretationSnapshot{}, storageFailure("decode owner interpretation report evidence", err)
		}
		reports = append(reports, map[string]any{"id": id, "run_id": runID, "task_id": taskID, "kind": kind, "message": message, "handoff": handoff, "evidence_ids": evidence, "status": status, "created_at": createdAt})
		addFact("report:"+id, "run_report", id, 1, kind+": "+message, map[string]any{"kind": "run_report", "id": id, "run_id": runID, "task_id": taskID, "report_kind": kind, "message": message, "handoff": handoff, "status": status, "created_at": createdAt})
	}
	if err := reportRows.Close(); err != nil {
		return OwnerInterpretationSnapshot{}, storageFailure("close owner interpretation reports", err)
	}

	messages := make([]map[string]any, 0, 50)
	messageRows, err := tx.QueryContext(ctx, `SELECT id,COALESCE(task_id,''),sender_type,sender_id,kind,body,created_at FROM messages WHERE workspace_id=? AND project_id=? ORDER BY created_at DESC,id DESC LIMIT 50`, workspace.ID, project.ID)
	if err != nil {
		return OwnerInterpretationSnapshot{}, storageFailure("read owner interpretation messages", err)
	}
	for messageRows.Next() {
		var id, taskID, senderType, senderID, kind, body, createdAt string
		if err := messageRows.Scan(&id, &taskID, &senderType, &senderID, &kind, &body, &createdAt); err != nil {
			messageRows.Close()
			return OwnerInterpretationSnapshot{}, storageFailure("scan owner interpretation message", err)
		}
		messages = append(messages, map[string]any{"id": id, "task_id": taskID, "sender_type": senderType, "sender_id": senderID, "kind": kind, "body": body, "created_at": createdAt})
		addFact("message:"+id, "message", id, 1, kind, map[string]any{"kind": "message", "id": id, "task_id": taskID, "sender_type": senderType, "sender_id": senderID, "message_kind": kind, "body": body, "created_at": createdAt})
	}
	messageRows.Close()

	contextValue := map[string]any{
		"schema": "urn:crewfold:owner-manager-context:v1", "workspace": map[string]any{"id": workspace.ID, "name": workspace.Name},
		"project": map[string]any{"id": project.ID, "name": project.Name, "revision": project.Revision}, "event_cut": highWater,
		"repository": map[string]any{"checkout_id": checkoutID, "path": checkoutPath, "write_mode": checkoutWriteMode, "branch": checkoutBranch},
		"agents":     agents, "launch_profiles": profiles, "objectives": objectives, "tasks": tasks, "runs": runs,
		"reports": reports, "messages": messages, "facts": facts,
	}
	contextJSON, _, err := canonicalContent(contextValue)
	if err != nil {
		return OwnerInterpretationSnapshot{}, storageFailure("encode owner interpretation snapshot", err)
	}
	if len(contextJSON) > 256*1024 {
		return OwnerInterpretationSnapshot{}, &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager context exceeds its bounded input"}
	}
	return OwnerInterpretationSnapshot{WorkspaceID: workspace.ID, ProjectID: project.ID, CheckoutPath: checkoutPath, Provider: provider, EventSequence: highWater, CanonicalContext: contextJSON, Citations: citations}, nil
}

func ResolveOwnerCitations(snapshot OwnerInterpretationSnapshot, refs []string) ([]domain.OwnerCitation, error) {
	seen := make(map[string]struct{}, len(refs))
	result := make([]domain.OwnerCitation, 0, len(refs))
	for _, ref := range refs {
		if _, duplicate := seen[ref]; duplicate {
			return nil, &Error{Code: CodeInvalidOwnerConversation, Message: "owner manager repeated a citation reference"}
		}
		value, exists := snapshot.Citations[ref]
		if !exists {
			return nil, &Error{Code: CodeInvalidOwnerConversation, Message: fmt.Sprintf("owner manager cited unknown canonical ref %q", ref)}
		}
		seen[ref] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func decodeOwnerInterpretation(raw []byte) (domain.OwnerInterpretation, error) {
	var value domain.OwnerInterpretation
	if err := json.Unmarshal(raw, &value); err != nil {
		return domain.OwnerInterpretation{}, err
	}
	return value, nil
}
