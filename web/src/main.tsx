import { StrictMode, type ReactNode, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import {
  Activity, AlertCircle, Archive, ArrowRight, Bot, Boxes, ChevronDown, ChevronRight, CircleDot, ClipboardCheck,
  Clock3, Command, FileCheck2, FileText, GitBranch, Inbox, LoaderCircle, MessageSquareText, Network, Play,
  Plus, RefreshCw, RotateCcw, Send, ShieldCheck, Sparkles, Square, TerminalSquare, Users, X,
} from "lucide-react";
import "@xterm/xterm/css/xterm.css";
import "./styles.css";

type ConnectionState = "connecting" | "connected" | "unauthorized" | "failed";

type APIError = { code: string; message: string; retryable: boolean; details?: Record<string, unknown> };
type RPCEnvelope<T> = { id: string; protocol: number; result?: T; error?: APIError };
type Page = { next_cursor: string; has_more: boolean; total: number };
type Workspace = { id: string; name: string; revision: number; updated_at: string };
type Project = { id: string; workspace_id: string; name: string; revision: number; updated_at: string };
type Checkout = { id: string; project_id: string; path: string; write_mode: string; availability: string; branch?: string; head_commit?: string; dirty?: boolean; dirty_paths?: string[]; omitted_paths?: number; truncated?: boolean; diagnostic?: string };
type Agent = { id: string; workspace_id: string; name: string; role: string; provider: string; runtime: string; enabled: boolean; max_concurrency: number; revision: number };
type DelegationPolicy = "hands_on" | "adaptive" | "delegation_first";
type DomainAgentMembership = { project_id: string; agent_id: string; parent_agent_id?: string; workstream_id?: string; operating_charter: string; delegation_policy: DelegationPolicy; preferred_entry: boolean; status: "active" | "retired"; revision: number; created_at: string; updated_at: string; created_by: string; updated_by: string };
type DomainAgent = { definition: Agent; membership: DomainAgentMembership };
type DomainAgentSpecDraft = { name: string; role: string; operating_charter: string; delegation_policy: DelegationPolicy; rationale: string };

const agentOwnershipTemplates = [
  { key: "domain-coordinator", label: "Domain coordinator", intent: "Coordinate the shared domain, maintain cross-workstream context, route material dependencies to the affected agents, delegate continuing specialist responsibilities when staffing authority permits, and escalate only genuine owner decisions." },
  { key: "workstream-coordinator", label: "Workstream coordinator", intent: "Own one bounded workstream outcome, maintain its dependency picture, coordinate implementers and reviewers, keep adjacent workstreams informed of interface changes, and escalate unresolved cross-workstream conflicts." },
  { key: "implementer", label: "Implementation specialist", intent: "Implement bounded assigned work in the authorized checkout, preserve unrelated changes, publish exact progress and evidence through Crewfold, communicate interface impacts early, and stop when required authority or context is missing." },
  { key: "independent-reviewer", label: "Independent reviewer", intent: "Independently review assigned changes against their stated contract, inspect evidence and regressions, challenge unsafe assumptions, report concrete findings without taking over implementation, and clearly separate verified defects from suggestions." },
  { key: "verification-qa", label: "Verification / scenario QA", intent: "Verify assigned outcomes using authorized checks and realistic scenarios, preserve reproducible evidence, distinguish product defects from environment failures, report coverage gaps, and avoid claiming completion without the required proof." },
  { key: "knowledge-maintainer", label: "Knowledge maintainer", intent: "Maintain shared domain knowledge from accepted evidence, connect decisions and interface constraints across workstreams, identify stale or contradictory guidance, and request owner review before treating disputed conclusions as current." },
  { key: "integration-release", label: "Integration / release coordinator", intent: "Coordinate cross-repository interfaces, upstream changes, dependency compatibility, and release readiness; route required work to the owning agents, track verification gaps, and never publish or deploy without explicit authority." },
] as const;
const staffingTaskClasses = [
  { value: "implementation", label: "Implementation", description: "Build or modify bounded product/code work." },
  { value: "review", label: "Independent review", description: "Inspect changes and report findings without owning implementation." },
  { value: "verification", label: "Verification / QA", description: "Run checks or realistic scenarios and preserve evidence." },
  { value: "coordination", label: "Coordination", description: "Lead one bounded workstream or cross-agent dependency." },
  { value: "knowledge", label: "Knowledge maintenance", description: "Curate shared domain context from accepted evidence." },
  { value: "integration", label: "Integration / release", description: "Coordinate repository interfaces and release readiness." },
] as const;
const staffingTaskClassLabel = (value: string) => staffingTaskClasses.find((item) => item.value === value)?.label ?? value;
const staffingBudgetLabel = (value: number, finiteUnit: string) => value === 0 ? `unlimited ${finiteUnit}` : `${value.toLocaleString()} ${finiteUnit}`;
type DomainAgentSession = { project_id: string; agent_id: string; provider?: string; state: "unbound" | "ready" | "detached" | "archived"; cwd?: string; has_conversation: boolean; epoch: number; revision: number; created_at?: string; updated_at?: string };
type DomainAgentSessionEpoch = { epoch: number; status: "current" | "archived"; rotation_reason?: string; created_at: string; rotated_at?: string };
type DomainAgentSessionCommandAction = { type: "read" | "listFiles" | "search" | "unknown"; command?: string; name?: string; path?: string; query?: string };
type DomainAgentSessionFileChange = { path: string; kind: "add" | "delete" | "update"; diff?: string };
type DomainAgentSessionItem = { id: string; type: "userMessage" | "agentMessage" | "plan" | "commandExecution" | "mcpToolCall" | "dynamicToolCall" | "collabAgentToolCall" | "fileChange" | "webSearch" | "subAgentActivity" | "reasoning" | "error"; origin?: "owner" | "crewfold_task" | "crewfold_delivery"; text?: string; command?: string; status?: string; cwd?: string; process_id?: string; exit_code?: number; duration_ms?: number; command_actions?: DomainAgentSessionCommandAction[]; changes?: DomainAgentSessionFileChange[] };
type DomainAgentSessionTurn = { id: string; status: string; items: DomainAgentSessionItem[] };
type DomainAgentSessionResult = { schema: string; type: "domain_agent_session"; view: { session: DomainAgentSession; epochs: DomainAgentSessionEpoch[]; thread_status: string; turns: DomainAgentSessionTurn[] }; accepted_turn?: DomainAgentSessionTurn };
type DomainStaffingProfile = { provider: string; runtime: string; max_concurrency: number };
type DomainStaffingGrant = { id: string; project_id: string; manager_agent_id: string; manager_membership_revision: number; profiles: DomainStaffingProfile[]; task_classes: string[]; max_descendants: number; max_concurrency: number; budget: Budget; expires_at?: string; status: "active" | "revoked" | "expired"; revision: number; created_at: string; updated_at: string };
type LaunchProfile = { id: string; project_id: string; agent_id: string; runtime: string; provider: string; checkout_id?: string; status: string; revision: number };
type DomainWorkProposalTask = { key: string; title: string; description: string; task_class: string; priority: number; budget: Budget; assignee_key: string; depends_on: string[]; dependency_delivery: Record<string, "completion" | "handoff" | "handoff_with_evidence"> };
type DomainWorkProposalAgent = { key: string; existing_agent_id?: string; existing_membership_revision?: number; existing_launch_profile_id?: string; name?: string; role?: string; parent_key?: string; operating_charter?: string; delegation_policy?: string; provider?: string; runtime?: string; max_concurrency?: number; task_class?: string; budget: Budget };
type DomainWorkProposal = { id: string; workspace_id: string; project_id: string; source_agent_id: string; source_thread_id: string; staffing_grant_id: string; staffing_grant_revision: number; summary: string; as_of_event_sequence: number; content: { objective_title: string; objective_budget: Budget; primary_checkout_id: string; primary_checkout_revision: number; reference_checkout_ids: string[]; agents: DomainWorkProposalAgent[]; tasks: DomainWorkProposalTask[] }; content_sha256: string; status: "pending" | "accepted" | "rejected" | "stale"; decision_note?: string; revision: number; created_at: string; updated_at: string; decided_at?: string };
type KnowledgeRevision = { id: string; item_id: string; project_id: string; task_scope_id?: string; type: "decision" | "finding"; revision_number: number; state_revision: number; title: string; body: string; review_status: "proposed" | "accepted" | "rejected"; currency_status: "pending" | "current" | "stale" | "superseded"; confidence: string; verification_status: string; proposed_at: string; proposed_by: string; proposed_by_type: string; accepted_at?: string; sources: Array<{ type: string; id: string; revision: number; role: string; ordinal: number }> };
type MessageThread = { id: string; workspace_id: string; project_id?: string; task_id?: string; subject: string; status: "open" | "closed"; revision: number; created_at: string; updated_at: string; created_by: string; updated_by: string };
type ThreadSummary = { thread: MessageThread; message_count: number; agent_ids: string[] };
type ThreadMessage = { id: string; thread_id: string; sender_type: string; sender_agent_name?: string; kind: string; body: string; created_at: string };
type ThreadDetail = { thread: MessageThread; messages: ThreadMessage[]; recipients: Array<{ message_id: string; recipient_name: string; status: string; wake_status: string }> };
type Objective = { id: string; project_id: string; primary_checkout_id?: string; title: string; status: "active" | "completed" | "cancelled"; revision: number; updated_at: string };
type Task = { id: string; project_id: string; objective_id?: string; title: string; description?: string; task_class: string; status: string; blocked_reason?: string; priority: number; revision: number; assigned_agent_id?: string; updated_at: string };
type TaskDetail = { task: Task; dependencies: Array<{ depends_on_task_id: string; delivery_requirement?: "completion" | "handoff" | "handoff_with_evidence" }>; assignment?: { agent_id: string }; readiness: { ready: boolean; reason: string } };
type Run = { id: string; project_id: string; task_id: string; agent_id: string; checkout_id?: string; runtime: string; provider: string; status: string; assessment?: "pass" | "block" | "changes_requested"; can_attach: boolean; revision: number; updated_at: string; result_summary?: string; blocked_question?: string; failure_code?: string; failure_message?: string };
type RunDetailView = { run: Run & { context_packet_id?: string; created_at: string; started_at?: string; finished_at?: string; placement?: { checkout_path?: string; write_mode?: string; reasons?: string[] } }; task: Task; agent: Agent; checkout: Checkout; timeline: Array<{ sequence: number; kind: string; message?: string; evidence: string[]; recorded_at: string }>; blocker?: { reason: string; needs: string[]; severity: string; related_ids: string[] }; handoff?: { summary: string; evidence: string[]; created_at: string }; assessment?: "pass" | "block" | "changes_requested" };
type RunArtifactContent = { artifact: { id: string; run_id: string; name: string; media_type: string; content_hash: string; byte_size: number; created_at: string }; workspace_id: string; project_id: string; task_id: string; agent_id: string; content: string };
type ContextExplanation = { packet_id: string; content_hash: string; byte_size: number; included: Array<{ section: string; entity_type: string; entity_id: string; revision: number; reason: string }>; excluded: Array<{ section: string; reason: string; reason_code?: string }>; budget: { total: { limit_bytes: number; used_bytes: number; remaining_bytes: number } } };
type EventRecord = { event_id: string; sequence: number; type: string; recorded_at: string; actor: { actor_type: string }; entity: { type: string; id: string; revision: number } };
type InboxItem = { message: { id: string; sender_type: string; sender_agent_name?: string; kind: string; body: string; task_id?: string; created_at: string }; delivery: { recipient_agent_id: string; recipient_name: string; status: string; wake_status: string } };
type CheckRunItem = { run: { id: string; task_id: string; status: string; revision: number; created_at: string; updated_at: string }; outcome?: string; requirement_state: string; current_freshness?: { status?: string } };
type Budget = { token_limit: number; cost_cents: number; time_seconds: number };
type ManagedProcessHealth = { type: "process" | "tcp" | "http"; host?: string; port?: number; path?: string; interval_millis: number; timeout_millis: number };
type ManagedProcessDefinition = { id: string; workspace_id: string; project_id: string; workstream_id?: string; checkout_id: string; name: string; description: string; executable: string; arguments: string[]; working_directory: string; environment: Array<{ name: string; value: string }>; profile: string; profile_revision: number; network_mode: "none" | "loopback" | "local"; health: ManagedProcessHealth; restart_policy: "never" | "on_failure" | "on_daemon_restart"; maximum_restarts: number; restart_cooldown_millis: number; stop_signal: "term"; stop_grace_millis: number; output_byte_limit: number; capacity_class: "local_development"; status: "active" | "retired"; revision: number; updated_at: string };
type ManagedProcessInstance = { id: string; workspace_id: string; project_id: string; workstream_id?: string; checkout_id: string; definition_id: string; definition_revision: number; source: { type: "owner" | "agent" | "agent_request"; actor_id: string; agent_id?: string; agent_revision?: number; thread_id?: string; request_id?: string; grant_id?: string; grant_revision?: number }; status: "requested" | "starting" | "healthy" | "degraded" | "stopping" | "stopped" | "failed" | "unknown"; desired_state: "running" | "stopped"; health_status: "pending" | "healthy" | "unhealthy" | "unknown"; restart_count: number; exit_code?: number; diagnostic_code?: string; diagnostic?: string; revision: number; created_at: string; updated_at: string; started_at?: string; healthy_at?: string; finished_at?: string };
type ManagedProcessLogs = { instance_id: string; state: "live" | "terminal"; stdout: { text: string; captured_bytes: number; omitted_bytes: number; truncated: boolean }; stderr: { text: string; captured_bytes: number; omitted_bytes: number; truncated: boolean } };
type ManagedProcessGrant = { id: string; workspace_id: string; project_id: string; definition_id: string; definition_revision: number; manager_agent_id: string; manager_membership_revision: number; parent_grant_id?: string; actions: Array<"inspect" | "logs" | "start" | "stop" | "restart" | "delegate">; maximum_instances: number; expires_at?: string; status: "active" | "revoked"; revision: number; created_at: string; updated_at: string };
type ManagedProcessRequest = { id: string; workspace_id: string; project_id: string; definition_id: string; definition_revision: number; agent_id: string; agent_membership_revision: number; thread_id: string; summary: string; status: "pending" | "accepted" | "rejected"; revision: number; created_at: string; updated_at: string; decided_at?: string; decision_reason?: string };
type ManagedProcessJob = { id: string; instance_id: string; action: "start" | "stop" | "restart" | "probe"; status: "pending" | "leased" | "complete" | "failed_unknown"; available_at: string; lease_expires_at?: string; attempts: number; diagnostic?: string; created_at: string; updated_at: string };
type ManagedProcessDetail = { definition: ManagedProcessDefinition; instance: ManagedProcessInstance; jobs: ManagedProcessJob[]; logs: Array<{ id: string; instance_id: string; kind: "stdout" | "stderr"; content_sha256: string; captured_bytes: number; omitted_bytes: number; truncated: boolean; created_at: string }> };
type WorkstreamDelivery = { objective_id: string; objective_revision: number; state: "in_progress" | "blocked" | "verified_awaiting_owner_acceptance" | "accepted" | "rejected"; sha256: string; task_count: number; completed_tasks: number; verification_tasks: number; passing_verifications: number; evidence: string[]; blockers: string[]; decision_reason?: string; decision_at?: string; decision_event_sequence?: number };
type WorkbenchData = {
  workspaces: Workspace[];
  workspace: Workspace | null;
  projects: Project[];
  project: Project | null;
  checkouts: Checkout[];
  agents: Agent[];
  domainAgents: DomainAgent[];
  objectives: Objective[];
  tasks: TaskDetail[];
  runs: Run[];
  checks: CheckRunItem[];
  knowledge: KnowledgeRevision[];
  threads: ThreadSummary[];
  launchProfiles: LaunchProfile[];
  workProposals: DomainWorkProposal[];
  processDefinitions: ManagedProcessDefinition[];
  processInstances: ManagedProcessInstance[];
  processGrants: ManagedProcessGrant[];
  processRequests: ManagedProcessRequest[];
  highWater: number;
};

type DaemonStatus = {
  schema: string;
  type: "workbench_status";
  status: "ok";
  protocol: number;
  pid: number;
  started_at: string;
  uptime_ms: number;
  codex_tool_network_access: boolean;
  server_version: { version: string };
};

type SessionResponse = {
  schema: string;
  type: "workbench_session";
  status: "authenticated";
  api_base: string;
  csrf_token: string;
  expires_at: string;
};

const expectedStatusSchema = "urn:crewfold:schema:web:workbench-status:v1";
const expectedSessionSchema = "urn:crewfold:schema:web:workbench-session:v1";
const emptyData: WorkbenchData = { workspaces: [], workspace: null, projects: [], project: null, checkouts: [], agents: [], domainAgents: [], objectives: [], tasks: [], runs: [], checks: [], knowledge: [], threads: [], launchProfiles: [], workProposals: [], processDefinitions: [], processInstances: [], processGrants: [], processRequests: [], highWater: 0 };

class RPCFailure extends Error {
  readonly apiError: APIError;
  constructor(error: APIError) { super(error.message); this.name = "RPCFailure"; this.apiError = error; }
}

function newKey(scope: string) { return `web-${scope}-${crypto.randomUUID()}`; }
function displayTime(value?: string) {
  if (!value) return "—";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit", month: "short", day: "numeric" }).format(date);
}
function latestRunForAgent(runs: Run[], agentID: string) {
  return runs.filter((run) => run.agent_id === agentID).sort((left, right) => right.updated_at.localeCompare(left.updated_at) || right.id.localeCompare(left.id))[0] ?? null;
}
function statusTone(status: string) {
  if (["completed", "granted", "consumed", "available", "ready", "healthy", "stopped", "pass", "accepted"].includes(status)) return "good";
  if (["failed", "start_failed", "lost", "denied", "unknown", "rejected"].includes(status)) return "bad";
  if (["active", "starting", "requested", "stopping", "pending"].includes(status)) return "live";
  if (["blocked", "review", "changes_requested", "degraded", "unhealthy", "verified_awaiting_owner_acceptance"].includes(status)) return "warn";
  return "quiet";
}

function runOutcome(run: Run, task?: Task) {
  if (run.assessment === "pass") return { label: task?.task_class === "verification" ? "verification passed" : "review passed", tone: "pass" };
  if (run.assessment === "block") return { label: task?.status === "completed" ? "blocking findings delivered" : "review blocked delivery", tone: "block" };
  if (run.assessment === "changes_requested") return { label: task?.status === "completed" ? "findings delivered" : "changes requested", tone: "changes_requested" };
  return { label: run.status.replaceAll("_", " "), tone: run.status };
}

type RuntimeActivity = { key: string; kind: string; text: string; tone: "quiet" | "live" | "good" | "bad" };

function readableCommand(value: unknown) {
  let command = String(value ?? "Local command").trim();
  for (const prefix of ["/bin/bash -lc ", "/bin/sh -lc "]) {
    if (!command.startsWith(prefix)) continue;
    command = command.slice(prefix.length).trim();
    if (command.length >= 2 && ((command.startsWith("'") && command.endsWith("'")) || (command.startsWith('"') && command.endsWith('"')))) {
      command = command.slice(1, -1);
    }
    command = command.replaceAll("'\"'\"'", "'");
    break;
  }
  return command.replace(/\s+/g, " ").trim();
}

function readableRuntimeActivity(raw: string): RuntimeActivity[] {
  const clean = raw
    .replace(/\u001b\][^\u0007]*(?:\u0007|\u001b\\)/g, "")
    .replace(/\u001b\[[0-?]*[ -/]*[@-~]/g, "")
    .replace(/\r\n/g, "\n")
    .replace(/\r/g, "\n");
  const entries: RuntimeActivity[] = [];
  const lifecycleEntries = new Map<string, number>();
  const message = (value: unknown): unknown => {
    if (!value || typeof value !== "object") return value;
    const candidate = value as Record<string, unknown>;
    return candidate.message ?? candidate.error ?? JSON.stringify(candidate);
  };
  const add = (kind: string, text: unknown, tone: RuntimeActivity["tone"] = "quiet") => {
    if (typeof text !== "string" || !text.trim()) return;
    const value = text.trim();
    if (entries.at(-1)?.kind === kind && entries.at(-1)?.text === value) return;
    entries.push({ key: `${entries.length}-${kind}`, kind, text: value, tone });
  };
  const upsert = (identity: string, kind: string, text: unknown, tone: RuntimeActivity["tone"]) => {
    if (typeof text !== "string" || !text.trim()) return;
    const value = text.trim();
    const prior = lifecycleEntries.get(identity);
    if (prior !== undefined) {
      entries[prior] = { key: identity, kind, text: value, tone };
      return;
    }
    lifecycleEntries.set(identity, entries.length);
    entries.push({ key: identity, kind, text: value, tone });
  };
  for (const original of clean.split("\n")) {
    const line = original.trim();
    if (!line || line.startsWith("exec '") || line.includes("__herdr-pane-supervisor")) continue;
    const start = line.indexOf('{"type"');
    if (start >= 0) {
      try {
        const value = JSON.parse(line.slice(start)) as Record<string, unknown>;
        const type = typeof value.type === "string" ? value.type : "event";
        const item = value.item && typeof value.item === "object" ? value.item as Record<string, unknown> : null;
        if (type === "thread.started") { add("Session", "Codex session started", "live"); continue; }
        if (type === "turn.started") { add("Turn", "Agent reasoning started", "live"); continue; }
        if (type === "turn.completed") { add("Turn", "Agent turn completed", "good"); continue; }
        if (type === "turn.failed" || type === "error") { add("Error", message(value.error ?? value.message ?? line), "bad"); continue; }
        if ((type === "item.started" || type === "item.updated") && item) {
          const itemType = typeof item.type === "string" ? item.type : "item";
          const itemID = typeof item.id === "string" ? item.id : `${itemType}-${entries.length}`;
          if (itemType === "mcp_tool_call") { upsert(itemID, "Crewfold tool", `${String(item.tool ?? "tool").replaceAll("crewfold_", "").replaceAll("_", " ")} · running`, "live"); continue; }
          if (itemType === "command_execution") { upsert(itemID, "Command · running", readableCommand(item.command ?? item.text), "live"); continue; }
        }
        if (type === "item.completed" && item) {
          const itemType = typeof item.type === "string" ? item.type : "item";
          if (itemType === "agent_message") { add("Agent", item.text, "good"); continue; }
          const itemID = typeof item.id === "string" ? item.id : `${itemType}-${entries.length}`;
          if (itemType === "mcp_tool_call") { upsert(itemID, "Crewfold tool", `${String(item.tool ?? "tool").replaceAll("crewfold_", "").replaceAll("_", " ")} · ${String(item.status ?? "completed")}`, item.status === "failed" ? "bad" : "good"); continue; }
          if (itemType === "command_execution") {
            const failed = item.status === "failed" || typeof item.exit_code === "number" && item.exit_code !== 0;
            const command = readableCommand(item.command ?? item.text);
            const output = typeof item.aggregated_output === "string" ? item.aggregated_output.trim() : "";
            upsert(itemID, failed ? "Command · failed" : "Command · completed", failed && output ? `${command}\n\n${output.slice(-1600)}` : command, failed ? "bad" : "quiet");
            continue;
          }
        }
        continue;
      } catch {
        // A partial live JSON record is represented by its useful diagnostic below.
      }
    }
    // Bounded logs can begin in the middle of a large JSON-RPC result. Such a
    // fragment is protocol data, not a human-facing failure (it often contains
    // the harmless field `"error": null`). Complete Codex events are handled
    // above; discard structural fragments and retain only plain diagnostics.
    if (/"[A-Za-z0-9_]+"\s*:/.test(line)) continue;
    if (/bwrap:|failed to|error:|not permitted|usage limit|command not found/i.test(line)) add("Runtime", line, "bad");
  }
  const unique: RuntimeActivity[] = [];
  for (const entry of entries) {
    const prior = unique.at(-1);
    if (prior?.kind === entry.kind && prior.text === entry.text && prior.tone === entry.tone) continue;
    unique.push({ ...entry, key: `${unique.length}-${entry.kind}` });
  }
  return unique;
}

function RuntimeActivityFeed({ logs, empty, limit = 40 }: { logs: string; empty: string; limit?: number }) {
  const allActivity = useMemo(() => readableRuntimeActivity(logs), [logs]);
  const activity = limit > 0 ? allActivity.slice(-limit) : allActivity;
  if (activity.length === 0) return <div className="activity-empty"><Activity size={16} /><span>{empty}</span></div>;
  return <div className="runtime-activity">{activity.map((entry) => <article className={entry.tone} key={entry.key}><span>{entry.kind}</span><p>{entry.text}</p></article>)}</div>;
}

function RuntimeOutput({ logs, status }: { logs: string; status: string }) {
  const empty = logs
    ? "This bounded tail starts inside protocol data. The advanced runtime can expose the current raw stream."
    : ["requested", "starting"].includes(status)
      ? "The provider session is launching; no readable event has arrived yet."
      : ["active", "blocked", "stopping"].includes(status)
        ? "The provider is running, but no readable event is present in the retained tail yet."
        : "No human-readable provider event was retained for this completed session.";
  return <><RuntimeActivityFeed logs={logs} empty={empty} />{logs && <details className="raw-runtime-output"><summary>Show raw bounded provider output</summary><pre>{logs}</pre></details>}</>;
}

async function exchangeBootstrap(token: string): Promise<SessionResponse> {
  const response = await fetch("/api/v1/session", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ bootstrap: token }) });
  if (!response.ok) throw new Error(`bootstrap exchange failed (${response.status})`);
  const result = (await response.json()) as SessionResponse;
  if (result.schema !== expectedSessionSchema || result.type !== "workbench_session" || result.status !== "authenticated") throw new Error("bootstrap exchange returned an unknown contract");
  return result;
}

async function loadStatus(apiBase: string): Promise<DaemonStatus> {
  const response = await fetch(`${apiBase}/status`, { credentials: "same-origin" });
  if (response.status === 401) throw new Error("unauthorized");
  if (!response.ok) throw new Error(`status request failed (${response.status})`);
  const result = (await response.json()) as DaemonStatus;
  if (result.schema !== expectedStatusSchema || result.type !== "workbench_status" || result.status !== "ok") throw new Error("status response returned an unknown contract");
  return result;
}

async function rpc<T>(apiBase: string, csrf: string, method: string, params: Record<string, unknown>): Promise<T> {
  const id = `web-${crypto.randomUUID()}`;
  const response = await fetch(`${apiBase}/rpc`, {
    method: "POST", credentials: "same-origin",
    headers: { "Content-Type": "application/json", "X-Crewfold-CSRF": csrf },
    body: JSON.stringify({ id, method, params }),
  });
  if (response.status === 401) throw new Error("unauthorized");
  if (!response.ok) throw new Error(`workbench request failed (${response.status})`);
  const envelope = (await response.json()) as RPCEnvelope<T>;
  if (envelope.id !== id || envelope.protocol !== 1) throw new Error("workbench response did not bind the request");
  if (envelope.error) throw new RPCFailure(envelope.error);
  if (!envelope.result) throw new Error("workbench response omitted its result");
  return envelope.result;
}

async function submitOnboarding(apiBase: string, csrf: string, body: Record<string, unknown>): Promise<void> {
  const response = await fetch(`${apiBase}/onboarding`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-Crewfold-CSRF": csrf }, body: JSON.stringify(body) });
  if (response.status === 401) throw new Error("unauthorized");
  const value = (await response.json()) as { status?: string; error?: { message: string } };
  if (!response.ok || value.status !== "completed") throw new Error(value.error?.message ?? `onboarding failed (${response.status})`);
}

async function retryWorkbenchRun(apiBase: string, csrf: string, workspace: string, run: Run, task: Task): Promise<Run> {
  const response = await fetch(`${apiBase}/retry-run`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-Crewfold-CSRF": csrf }, body: JSON.stringify({ workspace, run: run.id, expected_run_revision: run.revision, expected_task_revision: task.revision, idempotency_key: newKey("run-retry") }) });
  const value = (await response.json()) as { detail?: { run: Run }; error?: { message: string } };
  if (!response.ok || !value.detail?.run) throw new Error(value.error?.message ?? `run retry failed (${response.status})`);
  return value.detail.run;
}

function bootstrapFromFragment(): string {
  const fragment = new URLSearchParams(window.location.hash.replace(/^#/, ""));
  const token = fragment.get("bootstrap") ?? "";
  if (token) history.replaceState(null, "", `${window.location.pathname}${window.location.search}`);
  return token;
}

async function loadWorkbench(apiBase: string, csrf: string, preferredWorkspace = "", preferredProject = "", attempt = 0): Promise<WorkbenchData> {
  const workspacePage = await rpc<{ workspaces: Workspace[] } & Page>(apiBase, csrf, "workspace.list", { limit: 200 });
  const workspace = workspacePage.workspaces.find((item) => item.id === preferredWorkspace) ?? workspacePage.workspaces[0] ?? null;
  if (!workspace) return { ...emptyData, workspaces: workspacePage.workspaces };
  const before = await rpc<{ events: EventRecord[]; high_water: number } & Page>(apiBase, csrf, "events.list", { workspace: workspace.id, after: 0, limit: 1 });
  const eventAfter = Math.max(0, before.high_water - 200);
  const [projectPage, agentPage, objectivePage, taskPage, runPage, eventPage] = await Promise.all([
    rpc<{ projects: Project[] } & Page>(apiBase, csrf, "project.list", { workspace: workspace.id, limit: 200 }),
    rpc<{ agents: Agent[] } & Page>(apiBase, csrf, "agent.list", { workspace: workspace.id, limit: 200 }),
    rpc<{ objectives: Objective[] } & Page>(apiBase, csrf, "objective.list", { workspace: workspace.id, limit: 200 }),
    rpc<{ tasks: TaskDetail[] } & Page>(apiBase, csrf, "task.list", { workspace: workspace.id, limit: 200 }),
    rpc<{ runs: Run[] } & Page>(apiBase, csrf, "run.list", { workspace: workspace.id, limit: 200 }),
    rpc<{ events: EventRecord[]; high_water: number } & Page>(apiBase, csrf, "events.list", { workspace: workspace.id, after: eventAfter, limit: 200 }),
  ]);
  const project = projectPage.projects.find((item) => item.id === preferredProject) ?? projectPage.projects[0] ?? null;
  const [checkouts, checks, domainAgents, knowledge, threads, launchProfiles, workProposals, processDefinitions, processInstances, processGrants, processRequests] = project ? await Promise.all([
    rpc<{ checkouts: Checkout[] }>(apiBase, csrf, "checkout.list", { workspace: workspace.id, project: project.id }).then((value) => value.checkouts),
    rpc<{ runs: CheckRunItem[] } & Page>(apiBase, csrf, "check.list", { workspace: workspace.id, project: project.id, limit: 200 }).then((value) => value.runs),
    rpc<{ project_id: string; agents: DomainAgent[] }>(apiBase, csrf, "domain.agent.tree", { workspace: workspace.id, project: project.id }).then((value) => value.agents),
    rpc<{ list: { revisions: KnowledgeRevision[] } }>(apiBase, csrf, "knowledge.list", { workspace: workspace.id, project: project.id }).then((value) => value.list.revisions),
    rpc<{ threads: ThreadSummary[] }>(apiBase, csrf, "thread.list", { workspace: workspace.id, project: project.id, limit: 50 }).then((value) => value.threads),
    rpc<{ profiles: LaunchProfile[] }>(apiBase, csrf, "launch_profile.list", { workspace: workspace.id, project: project.id, limit: 100 }).then((value) => value.profiles),
    rpc<{ proposals: DomainWorkProposal[] }>(apiBase, csrf, "domain.work_proposal.list", { workspace: workspace.id, project: project.id }).then((value) => value.proposals),
    rpc<{ definitions: ManagedProcessDefinition[] }>(apiBase, csrf, "managed_service.definition.list", { workspace: workspace.id, project: project.id, limit: 200 }).then((value) => value.definitions),
    rpc<{ instances: ManagedProcessInstance[] }>(apiBase, csrf, "managed_service.list", { workspace: workspace.id, project: project.id, limit: 200 }).then((value) => value.instances),
    rpc<{ grants: ManagedProcessGrant[] }>(apiBase, csrf, "managed_service.grant.list", { workspace: workspace.id, project: project.id, limit: 200 }).then((value) => value.grants),
    rpc<{ requests: ManagedProcessRequest[] }>(apiBase, csrf, "managed_service.request.list", { workspace: workspace.id, project: project.id, limit: 200 }).then((value) => value.requests),
  ]) : [[], [], [], [], [], [], [], [], [], [], []];
  const after = await rpc<{ events: EventRecord[]; high_water: number } & Page>(apiBase, csrf, "events.list", { workspace: workspace.id, after: before.high_water, limit: 1 });
  if (after.high_water !== before.high_water) {
    if (attempt >= 2) throw new Error("Canonical state kept changing during refresh; retry when the current event cut settles.");
    return loadWorkbench(apiBase, csrf, workspace.id, project?.id ?? preferredProject, attempt + 1);
  }
  return {
    workspaces: workspacePage.workspaces, workspace, projects: projectPage.projects, project, checkouts,
    agents: agentPage.agents, domainAgents, objectives: objectivePage.objectives, tasks: taskPage.tasks,
    runs: runPage.runs, checks, knowledge, threads, launchProfiles, workProposals, processDefinitions, processInstances, processGrants, processRequests,
    highWater: eventPage.high_water,
  };
}

function IconButton({ label, children, onClick, disabled = false }: { label: string; children: React.ReactNode; onClick?: () => void; disabled?: boolean }) {
  return <button className="icon-button" aria-label={label} title={label} onClick={onClick} disabled={disabled}>{children}</button>;
}

function StatusPill({ value, tone = value }: { value: string; tone?: string }) {
  return <span className={`status-pill ${statusTone(tone)}`}><CircleDot size={12} aria-hidden="true" />{value.replaceAll("_", " ")}</span>;
}

function readableTaskReadiness(detail: TaskDetail, tasks: TaskDetail[]) {
  if (detail.task.status === "changes_requested") return detail.task.blocked_reason || "Completion needs a reviewed retry.";
  const prerequisites = detail.dependencies.map((dependency) => tasks.find((candidate) => candidate.task.id === dependency.depends_on_task_id)?.task).filter((task): task is Task => Boolean(task && task.status !== "completed"));
  if (prerequisites.length > 0) return `Waiting for ${prerequisites.map((task) => `“${task.title}”`).join(", ")}`;
  return detail.readiness.reason || "Waiting for Crewfold to make this task runnable.";
}

function taskSuccessors(detail: TaskDetail, tasks: TaskDetail[]) {
  return tasks.filter((candidate) => candidate.dependencies.some((dependency) => dependency.depends_on_task_id === detail.task.id));
}

function taskOwnerAgentID(detail: TaskDetail, runs: Run[]) {
  if (detail.task.assigned_agent_id) return detail.task.assigned_agent_id;
  return runs.filter((candidate) => candidate.task_id === detail.task.id).sort((left, right) => right.updated_at.localeCompare(left.updated_at))[0]?.agent_id ?? "";
}

function taskProgressExplanation(detail: TaskDetail, tasks: TaskDetail[], runs: Run[], agents: Agent[]) {
  const run = runs.filter((candidate) => candidate.task_id === detail.task.id).sort((left, right) => right.updated_at.localeCompare(left.updated_at))[0];
  const successors = taskSuccessors(detail, tasks);
  if ((run?.assessment === "changes_requested" || run?.assessment === "block") && detail.task.status === "completed") {
    if (!successors.length) return "Review finished with findings, but the accepted graph has no remediation successor.";
    const routed = successors.map((successor) => {
      const owner = agents.find((agent) => agent.id === taskOwnerAgentID(successor, runs))?.name;
      return `“${successor.task.title}”${owner ? ` owned by ${owner}` : ""}`;
    });
    return `Review finished with ${run.assessment === "block" ? "blocking findings" : "requested changes"}; its handoff and evidence now gate ${routed.join(", ")}.`;
  }
  if (detail.task.status === "completed") return "Completed; downstream tasks may consume its exact handoff and evidence.";
  if (detail.readiness.ready) {
    const owner = agents.find((agent) => agent.id === taskOwnerAgentID(detail, runs))?.name;
    return owner ? `Next runnable step · assigned to ${owner} · Crewfold may launch it now.` : "Next runnable step · ready for assignment.";
  }
  return readableTaskReadiness(detail, tasks);
}

function dependencyOrderedTasks(tasks: TaskDetail[]) {
  const byID = new Map(tasks.map((detail) => [detail.task.id, detail]));
  const ordered: TaskDetail[] = [];
  const visited = new Set<string>();
  const visiting = new Set<string>();
  const visit = (detail: TaskDetail) => {
    if (visited.has(detail.task.id)) return;
    if (visiting.has(detail.task.id)) return;
    visiting.add(detail.task.id);
    for (const dependency of detail.dependencies) {
      const predecessor = byID.get(dependency.depends_on_task_id);
      if (predecessor) visit(predecessor);
    }
    visiting.delete(detail.task.id);
    visited.add(detail.task.id);
    ordered.push(detail);
  };
  [...tasks].sort((left, right) => left.task.priority - right.task.priority || left.task.title.localeCompare(right.task.title)).forEach(visit);
  return ordered;
}

function AgentOwnershipTemplatePicker({ intent, setIntent }: { intent: string; setIntent: (value: string) => void }) {
  const selected = agentOwnershipTemplates.find((template) => template.intent === intent)?.key ?? "";
  return <label className="m22-agent-template"><span>Start from a role template</span><select value={selected} onChange={(event) => {
    const template = agentOwnershipTemplates.find((candidate) => candidate.key === event.target.value);
    setIntent(template?.intent ?? "");
  }}><option value="">Custom — write your own</option>{agentOwnershipTemplates.map((template) => <option key={template.key} value={template.key}>{template.label}</option>)}</select><small>Optional starting point. It only prefills the editable ownership brief and grants no authority.</small></label>;
}

function Onboarding({ apiBase, csrf, status, onComplete }: { apiBase: string; csrf: string; status: DaemonStatus | null; onComplete: () => Promise<void> }) {
  const [workspace, setWorkspace] = useState("personal");
  const [project, setProject] = useState("");
  const [path, setPath] = useState("");
  const [agent, setAgent] = useState("");
  const [role, setRole] = useState("");
  const [ownerIntent, setOwnerIntent] = useState("");
  const [operatingCharter, setOperatingCharter] = useState("");
  const [delegationPolicy, setDelegationPolicy] = useState<DelegationPolicy>("adaptive");
  const [draftRationale, setDraftRationale] = useState("");
  const [draftBusy, setDraftBusy] = useState(false);
  const [provider, setProvider] = useState("codex");
  const [runtime, setRuntime] = useState("herdr");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const updatePath = (value: string) => {
    setPath(value);
    if (!project) {
      const candidate = value.replace(/\/$/, "").split("/").pop()?.toLowerCase().replace(/[^a-z0-9-]+/g, "-").replace(/^[^a-z]+/, "") ?? "";
      if (/^[a-z][a-z0-9-]{0,62}$/.test(candidate)) setProject(candidate);
    }
  };
  const submit = async (event: React.FormEvent) => {
    event.preventDefault(); setBusy(true); setError("");
    try {
      await submitOnboarding(apiBase, csrf, { repository_path: path, workspace, project, agent, role, operating_charter: operatingCharter, delegation_policy: delegationPolicy, provider, runtime, write_mode: "shared" });
      await onComplete();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Onboarding failed without a diagnosis.");
    } finally { setBusy(false); }
  };
  const draft = async () => {
    setDraftBusy(true); setError("");
    try {
      const result = await rpc<{ schema: string; type: "domain_agent_spec_draft"; draft: DomainAgentSpecDraft }>(apiBase, csrf, "domain.agent.spec.draft", { repository_path: path, domain_name: project, owner_intent: ownerIntent });
      setAgent(result.draft.name); setRole(result.draft.role); setOperatingCharter(result.draft.operating_charter); setDelegationPolicy(result.draft.delegation_policy); setDraftRationale(result.draft.rationale);
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Codex could not draft the agent specification."); }
    finally { setDraftBusy(false); }
  };
  return <main className="onboarding-main">
    <section className="onboarding-copy">
      <div className="eyebrow"><Sparkles size={14} />First local workspace</div>
      <h1>Bring your repository into the workbench.</h1>
      <p>Crewfold records one domain, attaches this checkout, and creates the first durable agent in its owner-visible hierarchy. Its name and descriptive role are yours; neither grants authority. Provider credentials stay in their native CLI home and never enter the browser.</p>
      <div className="onboarding-trust">
        <div><ShieldCheck size={19} /><span><strong>Local authority</strong>Loopback web, Unix socket, private database</span></div>
        <div><GitBranch size={19} /><span><strong>Existing repository</strong>No clone, move, or rewrite</span></div>
        <div><Bot size={19} /><span><strong>Subscription login</strong>Codex or Claude CLI, no API key form</span></div>
      </div>
    </section>
    <form className="onboarding-form" onSubmit={submit}>
      <div className="form-heading"><div className="step-mark">1</div><div><h2>Set up your first domain</h2><p>Attach a checkout and define the first durable agent. You can grow any hierarchy later.</p></div></div>
      <label><span>Repository path</span><input required value={path} onChange={(event) => updatePath(event.target.value)} placeholder="~/depot/dev/world-engine-2" autoComplete="off" /></label>
      <div className="field-grid">
        <label><span>Workspace</span><input required pattern="[a-z][-a-z0-9]{0,62}" value={workspace} onChange={(event) => setWorkspace(event.target.value)} /></label>
        <label><span>Domain</span><input required pattern="[a-z][-a-z0-9]{0,62}" value={project} onChange={(event) => setProject(event.target.value)} placeholder="world-engine" /></label>
      </div>
      <div className="m22-spec-drafter"><AgentOwnershipTemplatePicker intent={ownerIntent} setIntent={setOwnerIntent} /><label><span>What should the first agent own?</span><textarea required maxLength={4096} value={ownerIntent} onChange={(event) => setOwnerIntent(event.target.value)} placeholder="Choose a template above or write a custom ownership brief." /></label><button type="button" disabled={draftBusy || !path || !project || !ownerIntent.trim()} onClick={() => void draft()}>{draftBusy ? <LoaderCircle className="spin" size={15} /> : <Sparkles size={15} />} {draftBusy ? "Codex is drafting…" : "Draft reviewed specification"}</button><small>Read-only, short-lived Codex session. It records no Crewfold state and grants no authority.</small></div>
      <div className="field-grid">
        <label><span>First durable agent</span><input required pattern="[a-z][-a-z0-9]{0,62}" value={agent} onChange={(event) => setAgent(event.target.value)} /><small className="field-help">An arbitrary owner-chosen name—not a hardcoded steward or executive.</small></label>
        <label><span>Descriptive role</span><input required maxLength={256} value={role} onChange={(event) => setRole(event.target.value)} /><small className="field-help">A label for humans. Grants and assignments authorize effects.</small></label>
      </div>
      <label><span>Owner-reviewed operating charter</span><textarea required maxLength={8192} value={operatingCharter} onChange={(event) => setOperatingCharter(event.target.value)} placeholder="Describe what this agent owns, how it coordinates, when it delegates, and what it must escalate." /><small className="field-help">This behavior is injected into the durable Codex conversation. It still cannot expand grants or assignments.</small></label>
      <label><span>Operating mode</span><select value={delegationPolicy} onChange={(event) => setDelegationPolicy(event.target.value as DelegationPolicy)}><option value="hands_on">Hands-on by default</option><option value="adaptive">Choose direct work or delegation</option><option value="delegation_first">Delegate durable responsibilities first</option></select></label>
      {draftRationale && <div className="m22-draft-rationale"><strong>Why Codex suggested this</strong>{draftRationale}</div>}
      <label><span>Provider</span><select value={provider} onChange={(event) => setProvider(event.target.value)}><option value="codex">Codex subscription</option><option value="claude">Claude subscription</option><option value="fixture-mcp">Local fixture</option></select></label>
      {provider === "codex" && <div className="runtime-primary"><Network size={19} /><div><strong>Dependency and documentation network {status?.codex_tool_network_access ? "enabled" : "disabled"}</strong><span>{status?.codex_tool_network_access ? "Codex may retrieve packages and documentation inside the workspace sandbox. Publishing, deployment, credentials, paid services, and external side effects remain outside this grant." : "This service cannot retrieve uncached packages. Reinstall with --codex-tool-network-access true before starting implementation work."}</span></div><StatusPill value={status?.codex_tool_network_access ? "enabled" : "blocked"} /></div>}
      <div className="runtime-primary"><TerminalSquare size={19} /><div><strong>Herdr interactive runtime</strong><span>Persistent agent terminal hosted beside Crewfold's canonical state.</span></div><StatusPill value={runtime === "herdr" ? "recommended" : "fallback"} /></div>
      <details className="advanced-runtime"><summary>Advanced runtime fallback</summary><label><span>Execution runtime</span><select value={runtime} onChange={(event) => setRuntime(event.target.value)}><option value="herdr">Herdr interactive · recommended</option><option value="direct">Direct headless · CI and automation</option></select></label><p>Direct has bounded logs but no persistent interactive terminal.</p></details>
      {error && <div className="form-error" role="alert"><AlertCircle size={17} />{error}</div>}
      <button className="primary-button" disabled={busy || draftBusy || !operatingCharter.trim()}>{busy ? <LoaderCircle className="spin" size={17} /> : <Command size={17} />} {busy ? "Inspecting and recording…" : "Create domain"}</button>
      <div className="role-preview"><div><Boxes size={16} /><span><strong>{project || "Domain"}</strong>Shared knowledge and coordination boundary.</span></div><ChevronRight size={15} /><div><Bot size={16} /><span><strong>{agent || "Durable agent"}</strong>{role || "Owner-defined role"} through {runtime === "herdr" ? "Herdr" : "Direct"}.</span></div></div>
      <p className="form-note">One replay-safe submission records the workspace, domain, checkout, first agent definition, launch profile, hierarchy membership, and a visible starter staffing grant for up to 12 durable descendants across coordination, implementation, integration, knowledge, review, and verification. Its cumulative token, time, and cost dimensions start unlimited; you can narrow or revoke it from Staffing.</p>
    </form>
  </main>;
}

type DomainConsoleView = "domain" | "session" | "assignment" | "changes" | "briefing" | "verification" | "staffing";

function DomainAgentTreeList({ agents, objectives, selected, choose }: { agents: DomainAgent[]; objectives: Objective[]; selected: string; choose: (agent: DomainAgent) => void }) {
  const active = agents.filter((agent) => agent.membership.status === "active");
  const activeObjectives = objectives.filter((objective) => objective.status === "active");
  const closedObjectives = objectives.filter((objective) => objective.status !== "active");
  const children = new Map<string, DomainAgent[]>();
  for (const agent of active) {
    const parent = agent.membership.parent_agent_id ?? "";
    children.set(parent, [...(children.get(parent) ?? []), agent]);
  }
  for (const values of children.values()) values.sort((left, right) => left.definition.name.localeCompare(right.definition.name));
  const row = (agent: DomainAgent, depth: number, scopeID: string): React.ReactNode => {
    return <div key={agent.definition.id}>
      <button className={`m22-agent-row ${selected === agent.definition.id ? "selected" : ""}`} style={{ paddingLeft: `${12 + depth * 17}px` }} onClick={() => choose(agent)}>
        <span className="m22-tree-joint">{depth ? "└" : "›"}</span>
        <span><strong>{agent.definition.name}</strong><small>{agent.definition.role || "unlabeled agent"}</small></span>
        {agent.membership.preferred_entry && <em>default</em>}
      </button>
      {(children.get(agent.definition.id) ?? []).filter((child) => (child.membership.workstream_id ?? "") === scopeID).map((child) => row(child, depth + 1, scopeID))}
    </div>;
  };
  const rootsForScope = (scopeID: string) => {
    const members = active.filter((agent) => (agent.membership.workstream_id ?? "") === scopeID);
    const memberIDs = new Set(members.map((agent) => agent.definition.id));
    return members.filter((agent) => !agent.membership.parent_agent_id || !memberIDs.has(agent.membership.parent_agent_id));
  };
  const domainWide = rootsForScope("");
  const titleCounts = activeObjectives.reduce((counts, objective) => {
    const key = objective.title.trim().toLocaleLowerCase();
    counts.set(key, (counts.get(key) ?? 0) + 1);
    return counts;
  }, new Map<string, number>());
  const scoped = activeObjectives.map((objective) => ({ objective, agents: rootsForScope(objective.id) }));
  const closedScoped = closedObjectives.map((objective) => ({ objective, agents: rootsForScope(objective.id) })).filter((group) => group.agents.length > 0);
  if (!active.length && !activeObjectives.length) return <p className="m22-rail-empty">No active durable agents or workstreams are attached to this domain.</p>;
  return <div className="m22-agent-tree">{domainWide.map((agent) => row(agent, 0, ""))}{scoped.map((group) => <section className="m22-workstream-group" key={group.objective.id}><h3>{group.objective.title}{(titleCounts.get(group.objective.title.trim().toLocaleLowerCase()) ?? 0) > 1 && <span>duplicate title · r{group.objective.revision}</span>}</h3>{group.agents.length ? group.agents.map((agent) => row(agent, 0, group.objective.id)) : <p>no agents</p>}</section>)}{closedScoped.map((group) => <section className="m22-workstream-group closed" key={group.objective.id}><h3>{group.objective.title}<span>closed · {group.objective.status}</span></h3>{group.agents.map((agent) => row(agent, 0, group.objective.id))}</section>)}</div>;
}

function DomainAgentCreatePanel({ data, suggestedParent, apiBase, csrf, mutable, close, created, reload }: { data: WorkbenchData; suggestedParent: string; apiBase: string; csrf: string; mutable: boolean; close: () => void; created: (agent: DomainAgent) => void; reload: () => Promise<void> }) {
  const [name, setName] = useState("");
  const [role, setRole] = useState("");
  const [ownerIntent, setOwnerIntent] = useState("");
  const [operatingCharter, setOperatingCharter] = useState("");
  const [delegationPolicy, setDelegationPolicy] = useState<DelegationPolicy>("adaptive");
  const [draftRationale, setDraftRationale] = useState("");
  const [draftBusy, setDraftBusy] = useState(false);
  const [provider, setProvider] = useState("codex");
  const [runtime, setRuntime] = useState("herdr");
  const [maxConcurrency, setMaxConcurrency] = useState(1);
  const [parent, setParent] = useState(suggestedParent);
  const [workstream, setWorkstream] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!data.workspace || !data.project) return;
    setBusy(true); setError("");
    try {
      const result = await rpc<{ schema: string; type: "domain_agent_create"; agent: DomainAgent; event_sequences: number[] }>(apiBase, csrf, "domain.agent.create", {
        workspace: data.workspace.id, project: data.project.id, name: name.trim(), role: role.trim(), provider, runtime,
        max_concurrency: maxConcurrency, operating_charter: operatingCharter.trim(), delegation_policy: delegationPolicy,
        ...(parent ? { parent_agent: parent } : {}), ...(workstream ? { workstream } : {}),
        idempotency_key: newKey("domain-agent-create"),
      });
      await reload();
      created(result.agent);
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Could not create the durable agent."); }
    finally { setBusy(false); }
  };
  const draft = async () => {
    if (!data.workspace || !data.project) return;
    setDraftBusy(true); setError("");
    try {
      const result = await rpc<{ schema: string; type: "domain_agent_spec_draft"; draft: DomainAgentSpecDraft }>(apiBase, csrf, "domain.agent.spec.draft", {
        workspace: data.workspace.id, project: data.project.id, ...(data.checkouts.length === 1 ? { checkout: data.checkouts[0].id } : {}), owner_intent: ownerIntent,
      });
      setName(result.draft.name); setRole(result.draft.role); setOperatingCharter(result.draft.operating_charter); setDelegationPolicy(result.draft.delegation_policy); setDraftRationale(result.draft.rationale);
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Codex could not draft the agent specification."); }
    finally { setDraftBusy(false); }
  };
  return <section className="m22-agent-create">
    <header><div><p className="m22-kicker">owner operation</p><h1>Create a durable agent</h1><p>This records one real agent definition and its exact place in the domain tree atomically. Parentage routes attention; it grants no staffing, task, checkout, or runtime authority.</p></div><button onClick={close} aria-label="Close agent creation"><X size={15} /></button></header>
    <form onSubmit={submit}>
      <div className="m22-spec-drafter"><AgentOwnershipTemplatePicker intent={ownerIntent} setIntent={setOwnerIntent} /><label><span>What should this agent own?</span><textarea required maxLength={4096} value={ownerIntent} onChange={(event) => setOwnerIntent(event.target.value)} placeholder="Choose a template above or write a custom ownership brief." /></label><button type="button" disabled={draftBusy || !ownerIntent.trim()} onClick={() => void draft()}>{draftBusy ? <LoaderCircle className="spin" size={15} /> : <Sparkles size={15} />} {draftBusy ? "Codex is drafting…" : "Draft agent specification"}</button><small>Optional read-only Codex assistance. The draft is temporary until you review and create it.</small></div>
      <label><span>agent name</span><input required pattern="[a-z][-a-z0-9]{0,62}" value={name} onChange={(event) => setName(event.target.value)} placeholder="terrain-reviewer" /></label>
      <label><span>descriptive role</span><input required maxLength={128} value={role} onChange={(event) => setRole(event.target.value)} placeholder="independent terrain review" /></label>
      <label><span>owner-reviewed operating charter</span><textarea required maxLength={8192} value={operatingCharter} onChange={(event) => setOperatingCharter(event.target.value)} placeholder="Describe durable ownership, communication, delegation, reporting, and escalation behavior." /></label>
      <label><span>operating mode</span><select value={delegationPolicy} onChange={(event) => setDelegationPolicy(event.target.value as DelegationPolicy)}><option value="hands_on">Hands-on by default</option><option value="adaptive">Choose direct work or delegation</option><option value="delegation_first">Delegate durable responsibilities first</option></select></label>
      {draftRationale && <div className="m22-draft-rationale"><strong>Why Codex suggested this</strong>{draftRationale}</div>}
      <div className="m22-form-grid"><label><span>parent in attention tree</span><select value={parent} onChange={(event) => setParent(event.target.value)}><option value="">domain root</option>{data.domainAgents.filter((agent) => agent.membership.status === "active").map((agent) => <option key={agent.definition.id} value={agent.definition.id}>{agent.definition.name}</option>)}</select></label><label><span>workstream scope</span><select value={workstream} onChange={(event) => setWorkstream(event.target.value)}><option value="">domain-wide</option>{data.objectives.filter((objective) => objective.project_id === data.project?.id && objective.status === "active").map((objective) => <option key={objective.id} value={objective.id}>{objective.title}</option>)}</select></label></div>
      <div className="m22-form-grid"><label><span>provider</span><select value={provider} onChange={(event) => setProvider(event.target.value)}><option value="codex">Codex subscription</option><option value="claude">Claude subscription</option><option value="fixture-mcp">Local fixture</option></select></label><label><span>runtime host</span><select value={runtime} onChange={(event) => setRuntime(event.target.value)}><option value="herdr">Herdr interactive</option><option value="direct">Direct headless</option></select></label></div>
      <label><span>maximum concurrent runs</span><input type="number" min={1} max={100} value={maxConcurrency} onChange={(event) => setMaxConcurrency(Number(event.target.value))} /></label>
      <div className="m22-exact-effect"><ShieldCheck size={15} /><span><strong>Exact effect</strong> Create the definition and hierarchy membership only. No session, task, run, grant, or child is started.</span></div>
      {error && <p className="m22-session-error">{error}</p>}
      <div className="m22-form-actions"><button type="button" onClick={close}>cancel</button><button className="m22-send" disabled={!mutable || busy || draftBusy || !name.trim() || !role.trim() || !operatingCharter.trim()}>{busy ? <LoaderCircle className="spin" size={14} /> : <Plus size={14} />} create reviewed agent</button></div>
    </form>
  </section>;
}

function StaffingPanel({ data, agent, apiBase, csrf, mutable }: { data: WorkbenchData; agent: DomainAgent; apiBase: string; csrf: string; mutable: boolean }) {
  const [grants, setGrants] = useState<DomainStaffingGrant[]>([]);
  const [editing, setEditing] = useState(false);
  const [provider, setProvider] = useState(agent.definition.provider);
  const [runtime, setRuntime] = useState(agent.definition.runtime);
  const [childConcurrency, setChildConcurrency] = useState(1);
  const [maxDescendants, setMaxDescendants] = useState(4);
  const [maxConcurrency, setMaxConcurrency] = useState(4);
  const [taskClasses, setTaskClasses] = useState<string[]>(["implementation", "review", "verification"]);
  const [customTaskClass, setCustomTaskClass] = useState("");
  const [limitTokens, setLimitTokens] = useState(true);
  const [limitCost, setLimitCost] = useState(false);
  const [limitTime, setLimitTime] = useState(true);
  const [tokenLimit, setTokenLimit] = useState(250000);
  const [costCents, setCostCents] = useState(0);
  const [timeSeconds, setTimeSeconds] = useState(14400);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const scope = { workspace: data.workspace?.id ?? "", project: data.project?.id ?? "", manager_agent: agent.definition.id };
  const load = useCallback(async () => {
    if (!scope.workspace || !scope.project) return;
    try {
      const result = await rpc<{ schema: string; type: "domain_staffing_grant_list"; grants: DomainStaffingGrant[] }>(apiBase, csrf, "domain.agent.staffing_grant.list", scope);
      setGrants(result.grants); setError("");
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Could not read staffing grants."); }
  }, [apiBase, csrf, scope.manager_agent, scope.project, scope.workspace]);
  useEffect(() => { setEditing(false); setProvider(agent.definition.provider); setRuntime(agent.definition.runtime); void load(); }, [agent.definition.id, load]);
  const create = async (event: React.FormEvent) => {
    event.preventDefault();
    const custom = customTaskClass.trim();
    const classes = [...new Set([...taskClasses, ...(custom ? [custom] : [])])];
    setBusy(true); setError("");
    try {
      await rpc(apiBase, csrf, "domain.agent.staffing_grant.create", {
        ...scope, expected_membership_revision: agent.membership.revision,
        profiles: [{ provider, runtime, max_concurrency: childConcurrency }], task_classes: classes,
        max_descendants: maxDescendants, max_concurrency: maxConcurrency,
        budget: { token_limit: limitTokens ? tokenLimit : 0, cost_cents: limitCost ? costCents : 0, time_seconds: limitTime ? timeSeconds : 0 },
        idempotency_key: newKey("domain-staffing-grant"),
      });
      setEditing(false); await load();
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Could not create the staffing grant."); }
    finally { setBusy(false); }
  };
  const revoke = async (grant: DomainStaffingGrant) => {
    setBusy(true); setError("");
    try {
      await rpc(apiBase, csrf, "domain.agent.staffing_grant.revoke", { workspace: scope.workspace, grant_id: grant.id, expected_revision: grant.revision, idempotency_key: newKey("domain-staffing-revoke") });
      await load();
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Could not revoke the staffing grant."); }
    finally { setBusy(false); }
  };
  return <div className="m22-block m22-staffing"><header><div><h2>owner staffing grants</h2><p>Hierarchy is not authority. These exact grants let this agent create bounded durable descendants through Crewfold’s structured tool.</p></div><button className="m22-command" disabled={!mutable || busy} onClick={() => setEditing(!editing)}><Plus size={13} /> grant staffing</button></header>
    {grants.length ? grants.map((grant) => <article className="m22-grant" key={grant.id}><div><strong>{grant.status} · up to {grant.max_descendants} descendants / {grant.max_concurrency} concurrent</strong><small>{grant.profiles.map((profile) => `${profile.provider}/${profile.runtime} ≤${profile.max_concurrency}`).join(", ")} · {grant.task_classes.map(staffingTaskClassLabel).join(", ")}</small><small>{staffingBudgetLabel(grant.budget.token_limit, "tokens")} · {staffingBudgetLabel(grant.budget.time_seconds, "seconds")} · {staffingBudgetLabel(grant.budget.cost_cents, "cost")}</small></div>{grant.status === "active" && <button disabled={!mutable || busy} onClick={() => void revoke(grant)}>revoke</button>}</article>) : <p className="m22-empty">This agent cannot create durable children. You can still create and attach agents directly as owner.</p>}
    {editing && <form className="m22-staffing-form" onSubmit={create}><div className="m22-form-grid"><label><span>child provider</span><select value={provider} onChange={(event) => setProvider(event.target.value)}><option value="codex">Codex subscription</option><option value="claude">Claude subscription</option><option value="fixture-mcp">Local fixture</option></select></label><label><span>child runtime</span><select value={runtime} onChange={(event) => setRuntime(event.target.value)}><option value="herdr">Herdr interactive</option><option value="direct">Direct headless</option></select></label></div><div className="m22-form-grid"><label><span>maximum descendants</span><input type="number" min={1} max={1000} value={maxDescendants} onChange={(event) => setMaxDescendants(Number(event.target.value))} /></label><label><span>total concurrent capacity</span><input type="number" min={1} max={100} value={maxConcurrency} onChange={(event) => setMaxConcurrency(Number(event.target.value))} /></label></div><label><span>maximum concurrency per child</span><input type="number" min={1} max={100} value={childConcurrency} onChange={(event) => setChildConcurrency(Number(event.target.value))} /></label><fieldset className="m22-task-classes"><legend>work the agent may staff</legend><p>Choose familiar work types below. The exact class is matched when this agent later requests a child; role names alone never grant authority.</p>{staffingTaskClasses.map(({ value, label, description }) => <label key={value}><input type="checkbox" checked={taskClasses.includes(value)} onChange={(event) => setTaskClasses((current) => event.target.checked ? [...current, value] : current.filter((item) => item !== value))} /><span><strong>{label}</strong><small>{description}</small></span></label>)}<label className="m22-custom-class"><span>Advanced custom class</span><input pattern="[a-z][-a-z0-9]{0,62}" value={customTaskClass} onChange={(event) => setCustomTaskClass(event.target.value)} placeholder="for example: scenario-validation" /></label></fieldset><fieldset className="m22-budget"><legend>cumulative child budget</legend><p>Turn a limit off to allow that dimension without a ceiling. Crewfold records this using its canonical unlimited value, <code>0</code>.</p><div className="m22-form-grid three"><label><span><input type="checkbox" checked={limitTokens} onChange={(event) => setLimitTokens(event.target.checked)} /> limit tokens</span>{limitTokens ? <input type="number" min={1} value={tokenLimit} onChange={(event) => setTokenLimit(Number(event.target.value))} /> : <strong className="m22-unlimited">unlimited</strong>}</label><label><span><input type="checkbox" checked={limitCost} onChange={(event) => setLimitCost(event.target.checked)} /> limit cost (cents)</span>{limitCost ? <input type="number" min={1} value={costCents || 1} onChange={(event) => setCostCents(Number(event.target.value))} /> : <strong className="m22-unlimited">unlimited</strong>}</label><label><span><input type="checkbox" checked={limitTime} onChange={(event) => setLimitTime(event.target.checked)} /> limit time (seconds)</span>{limitTime ? <input type="number" min={1} value={timeSeconds} onChange={(event) => setTimeSeconds(Number(event.target.value))} /> : <strong className="m22-unlimited">unlimited</strong>}</label></div></fieldset><div className="m22-exact-effect"><ShieldCheck size={15} /><span><strong>Exact effect</strong> This records authority only. The agent must later request each child through the typed Crewfold tool; every request is checked against this revision and budget.</span></div><div className="m22-form-actions"><button type="button" onClick={() => setEditing(false)}>cancel</button><button className="m22-send" disabled={!mutable || busy || taskClasses.length === 0 && !customTaskClass.trim()}>{busy ? <LoaderCircle className="spin" size={14} /> : <ShieldCheck size={14} />} create exact grant</button></div></form>}
    {error && <p className="m22-session-error">{error}</p>}
  </div>;
}

function CheckoutAttach({ data, apiBase, csrf, mutable, reload }: { data: WorkbenchData; apiBase: string; csrf: string; mutable: boolean; reload: () => Promise<void> }) {
  const [open, setOpen] = useState(false);
  const [path, setPath] = useState("");
  const [writeMode, setWriteMode] = useState("shared");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const attach = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!data.workspace || !data.project || busy || !path.trim()) return;
    setBusy(true); setError("");
    try {
      await rpc(apiBase, csrf, "checkout.add", {
        workspace: data.workspace.id, project: data.project.id, repository_path: path.trim(), write_mode: writeMode,
        idempotency_key: newKey("checkout-attach"),
      });
      setPath(""); setOpen(false); await reload();
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Could not attach the checkout."); }
    finally { setBusy(false); }
  };
  return <div className="m22-checkout-attach">
    <button className="m22-command" disabled={!mutable || busy} onClick={() => { setOpen((value) => !value); setError(""); }}><Plus size={13} /> attach checkout</button>
    {open && <form onSubmit={attach}>
      <label><span>existing local Git checkout</span><input value={path} onChange={(event) => setPath(event.target.value)} placeholder="/home/you/depot/dev/world-engine-5" autoFocus /></label>
      <label><span>write policy</span><select value={writeMode} onChange={(event) => setWriteMode(event.target.value)}><option value="shared">Shared · coordinate claims before writes</option><option value="claimed">Claimed · writes require declared claims</option><option value="exclusive">Exclusive · one active writer</option><option value="read_only">Read only · observation only</option></select></label>
      <p>The directory must already be an inspectable Git checkout. Attaching records it as a resource of this domain; it does not move files, create work, or grant an agent write authority.</p>
      <div className="m22-form-actions"><button type="button" onClick={() => setOpen(false)}>cancel</button><button className="m22-send" disabled={!path.trim() || busy}>{busy ? <LoaderCircle className="spin" size={14} /> : <Plus size={14} />} attach exact checkout</button></div>
    </form>}
    {error && <p className="m22-session-error">{error}</p>}
  </div>;
}

function DomainWorkProposalReview({ data, proposal, apiBase, csrf, mutable, reload }: { data: WorkbenchData; proposal: DomainWorkProposal; apiBase: string; csrf: string; mutable: boolean; reload: () => Promise<void> }) {
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState<"accept" | "reject" | "">("");
  const [error, setError] = useState("");
  const source = data.agents.find((agent) => agent.id === proposal.source_agent_id);
  const checkout = data.checkouts.find((candidate) => candidate.id === proposal.content.primary_checkout_id);
  const taskByKey = new Map(proposal.content.tasks.map((task) => [task.key, task]));
  const agentByKey = new Map(proposal.content.agents.map((agent) => [agent.key, agent]));
  const newAgentCount = proposal.content.agents.filter((agent) => !agent.existing_agent_id).length;
  const dependencyCount = proposal.content.tasks.reduce((count, task) => count + (task.depends_on ?? []).length, 0);
  const isFlatTeam = proposal.content.agents.length > 1 && proposal.content.agents.every((agent) => !agent.parent_key);
  const proposalAgentName = (agent: DomainWorkProposalAgent) => agent.name ?? data.agents.find((candidate) => candidate.id === agent.existing_agent_id)?.name ?? agent.key;
  const proposalAgentTree = (parentKey = "", depth = 0): React.ReactNode => proposal.content.agents.filter((agent) => (agent.parent_key ?? "") === parentKey).map((agent) => <li key={agent.key} style={{ "--proposal-depth": depth } as React.CSSProperties}>
    <strong>{proposalAgentName(agent)}</strong><span>{agent.role ?? "existing durable agent"}</span>{proposalAgentTree(agent.key, depth + 1)}
  </li>);
  const summaryLead = proposal.summary.match(/^[\s\S]*?[.!?](?:\s|$)/)?.[0]?.trim() || proposal.summary;
  const decide = async (accept: boolean) => {
    if (!data.workspace || proposal.status !== "pending") return;
    setBusy(accept ? "accept" : "reject"); setError("");
    try {
      await rpc(apiBase, csrf, accept ? "domain.work_proposal.accept" : "domain.work_proposal.reject", {
        workspace: data.workspace.id, proposal_id: proposal.id, expected_revision: proposal.revision,
        decision_note: note.trim() || (accept ? "Owner reviewed and accepted the exact proposed work graph." : "Owner rejected the proposed work graph."),
        idempotency_key: newKey(accept ? "domain-work-accept" : "domain-work-reject"),
      });
      await reload();
    } catch (reason) { setError(reason instanceof Error ? reason.message : "The proposal decision failed without a diagnosis."); }
    finally { setBusy(""); }
  };
  return <article className={`m22-work-proposal ${proposal.status}`}>
    <header><div><p className="m22-kicker">review proposed work</p><h3>{proposal.content.objective_title}</h3><p className="m22-proposal-summary">{summaryLead}</p>{summaryLead !== proposal.summary && <details className="m22-proposal-brief"><summary>Read the complete proposal brief</summary><p>{proposal.summary}</p></details>}</div><StatusPill value={proposal.status} /></header>
    <div className="m22-proposal-impact" aria-label="Proposal effect summary"><span><strong>1</strong> workstream</span><span><strong>{newAgentCount}</strong> new agent{newAgentCount === 1 ? "" : "s"}</span><span><strong>{proposal.content.tasks.length}</strong> task{proposal.content.tasks.length === 1 ? "" : "s"}</span><span><strong>{dependencyCount}</strong> dependenc{dependencyCount === 1 ? "y" : "ies"}</span></div>
    <div className="m22-proposal-overview">
      <section className="m22-proposal-team"><h4>Who reports to whom</h4><p>Canonical attention tree after acceptance. The proposing coordinator is the root.</p><div className="m22-proposal-root"><strong>{source?.name ?? "coordinator"}</strong><span>proposing coordinator</span></div><ul>{proposalAgentTree()}</ul>{isFlatTeam && <p className="m22-proposal-topology-warning"><strong>Flat team:</strong> every proposed agent reports directly to the coordinator; none manages another.</p>}</section>
      <section className="m22-proposal-plan"><h4>Work plan</h4><p>Tasks run in this dependency order.</p><ol className="m22-proposal-tasks">{proposal.content.tasks.map((task, index) => {
        const proposedAssignee = agentByKey.get(task.assignee_key);
        const assigneeName = proposedAssignee?.name ?? data.agents.find((candidate) => candidate.id === proposedAssignee?.existing_agent_id)?.name ?? task.assignee_key;
        const dependencies = task.depends_on ?? [];
        const delivery = task.dependency_delivery ?? {};
        return <li key={task.key}><span className="m22-proposal-step">{index + 1}</span><div><strong>{task.title}</strong><small>{task.task_class.replaceAll("-", " ")} · {assigneeName}</small>{dependencies.length > 0 && <span>After {dependencies.map((key) => taskByKey.get(key)?.title ?? key).join(", ")}</span>}<details><summary>View exact task contract</summary><p>{task.description}</p><footer>priority {task.priority} · {task.key}{dependencies.length > 0 ? ` · delivery: ${dependencies.map((key) => (delivery[key] ?? "completion").replaceAll("_", " ")).join(", ")}` : ""}</footer></details></div></li>;
      })}</ol></section>
    </div>
    <details className="m22-proposal-metadata"><summary>Proposal source, checkout, and budget</summary><dl className="m22-proposal-facts"><div><dt>proposed by</dt><dd>{source?.name ?? proposal.source_agent_id}</dd></div><div><dt>primary checkout</dt><dd>{checkout?.path ?? proposal.content.primary_checkout_id} · r{proposal.content.primary_checkout_revision}</dd></div><div><dt>frozen state</dt><dd>event {proposal.as_of_event_sequence} · proposal r{proposal.revision}</dd></div><div><dt>objective budget</dt><dd>{staffingBudgetLabel(proposal.content.objective_budget.token_limit, "tokens")} · {staffingBudgetLabel(proposal.content.objective_budget.time_seconds, "seconds")}</dd></div></dl></details>
    <div className="m22-exact-effect"><ShieldCheck size={15} /><span><strong>What accepting does</strong> Creates this team and workstream, binds them to the checkout, then schedules {proposal.content.tasks.length} task{proposal.content.tasks.length === 1 ? "" : "s"} in the order shown. Nothing above exists or runs before acceptance.</span></div>
    {proposal.status === "pending" ? <><label className="m22-proposal-note"><span>owner decision note</span><input value={note} maxLength={2048} onChange={(event) => setNote(event.target.value)} placeholder="Optional rationale recorded with the exact decision" /></label><div className="m22-proposal-actions"><button disabled={!mutable || Boolean(busy)} onClick={() => void decide(false)}>{busy === "reject" ? <LoaderCircle className="spin" size={14} /> : <X size={14} />} reject</button><button className="m22-send" disabled={!mutable || Boolean(busy)} onClick={() => void decide(true)}>{busy === "accept" ? <LoaderCircle className="spin" size={14} /> : <Play size={14} />} accept exact graph</button></div></> : <footer>decided {displayTime(proposal.decided_at)} · {proposal.decision_note || "No decision note recorded."}</footer>}
    {error && <p className="m22-session-error" role="alert">{error}</p>}
  </article>;
}

function KnowledgeProposalReview({ data, revision, apiBase, csrf, mutable, reload }: { data: WorkbenchData; revision: KnowledgeRevision; apiBase: string; csrf: string; mutable: boolean; reload: () => Promise<void> }) {
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState<"accept" | "reject" | "">("");
  const [error, setError] = useState("");
  const primary = revision.sources.find((source) => source.role === "primary");
  const sourceAgent = primary?.type === "domain_agent" ? data.agents.find((agent) => agent.id === primary.id) : null;
  const decide = async (accept: boolean) => {
    if (!data.workspace || revision.review_status !== "proposed") return;
    setBusy(accept ? "accept" : "reject"); setError("");
    try {
      await rpc(apiBase, csrf, accept ? "knowledge.accept" : "knowledge.reject", {
        workspace: data.workspace.id, knowledge_revision: revision.id, expected_state_revision: revision.state_revision,
        decision_note: note.trim() || (accept ? "Owner reviewed and accepted this exact sourced domain knowledge revision." : "Owner rejected this domain knowledge proposal."),
        idempotency_key: newKey(accept ? "knowledge-accept" : "knowledge-reject"),
      });
      await reload();
    } catch (reason) { setError(reason instanceof Error ? reason.message : "The knowledge decision failed without a diagnosis."); }
    finally { setBusy(""); }
  };
  return <article className="m22-knowledge-review">
    <header><div><p className="m22-kicker">knowledge proposal · no current authority yet</p><h3>{revision.title}</h3></div><StatusPill value="proposed" /></header>
    <p className="m22-knowledge-body">{revision.body}</p>
    <dl className="m22-proposal-facts"><div><dt>proposed by</dt><dd>{sourceAgent?.name ?? revision.proposed_by}</dd></div><div><dt>quality claim</dt><dd>{revision.confidence} confidence · {revision.verification_status}</dd></div><div><dt>provenance</dt><dd>{primary ? `${primary.type.replaceAll("_", " ")} ${sourceAgent?.name ?? primary.id} at revision ${primary.revision}` : "missing primary source"} · {revision.sources.length - (primary ? 1 : 0)} supporting</dd></div></dl>
    <div className="m22-exact-effect"><ShieldCheck size={15} /><span><strong>Exact effect</strong> Accepting makes only this immutable sourced revision current domain knowledge. It does not edit a checkout, start an agent, or authorize implementation.</span></div>
    <label className="m22-proposal-note"><span>owner decision note</span><input value={note} maxLength={1024} onChange={(event) => setNote(event.target.value)} placeholder="Optional acceptance rationale; rejection records the default reason" /></label>
    <div className="m22-proposal-actions"><button disabled={!mutable || Boolean(busy)} onClick={() => void decide(false)}>{busy === "reject" ? <LoaderCircle className="spin" size={14} /> : <X size={14} />} reject</button><button className="m22-send" disabled={!mutable || Boolean(busy)} onClick={() => void decide(true)}>{busy === "accept" ? <LoaderCircle className="spin" size={14} /> : <ShieldCheck size={14} />} accept as current knowledge</button></div>
    {error && <p className="m22-session-error" role="alert">{error}</p>}
  </article>;
}

function managedProcessCommand(definition: ManagedProcessDefinition) {
  return [definition.executable, ...definition.arguments].map((value) => /\s|["']/.test(value) ? JSON.stringify(value) : value).join(" ");
}

function managedProcessURL(definition: ManagedProcessDefinition) {
  if (definition.health.type !== "http" || !definition.health.host || !definition.health.port) return "";
  const host = definition.health.host === "0.0.0.0" || definition.health.host === "::" ? "127.0.0.1" : definition.health.host;
  if (!["127.0.0.1", "localhost", "::1"].includes(host)) return "";
  const displayHost = host.includes(":") ? `[${host}]` : host;
  return `http://${displayHost}:${definition.health.port}${definition.health.path || "/"}`;
}

function ManagedProcesses({ data, apiBase, csrf, mutable, reload }: { data: WorkbenchData; apiBase: string; csrf: string; mutable: boolean; reload: () => Promise<void> }) {
  const [creating, setCreating] = useState(false);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [checkout, setCheckout] = useState(data.checkouts.find((item) => item.availability === "available")?.id ?? "");
  const [workstream, setWorkstream] = useState("");
  const [executable, setExecutable] = useState("");
  const [argumentsText, setArgumentsText] = useState("");
  const [environmentText, setEnvironmentText] = useState("");
  const [workingDirectory, setWorkingDirectory] = useState(".");
  const [networkMode, setNetworkMode] = useState<ManagedProcessDefinition["network_mode"]>("none");
  const [healthType, setHealthType] = useState<ManagedProcessHealth["type"]>("process");
  const [healthHost, setHealthHost] = useState("127.0.0.1");
  const [healthPort, setHealthPort] = useState("");
  const [healthPath, setHealthPath] = useState("/");
  const [restartPolicy, setRestartPolicy] = useState<ManagedProcessDefinition["restart_policy"]>("never");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [logs, setLogs] = useState<ManagedProcessLogs | null>(null);
  const [granting, setGranting] = useState("");
  const [grantAgent, setGrantAgent] = useState("");
  const [grantActions, setGrantActions] = useState<ManagedProcessGrant["actions"]>(["inspect", "logs", "start", "stop", "restart"]);
  const [resolvingUnknown, setResolvingUnknown] = useState("");
  const [unknownReason, setUnknownReason] = useState("");
  const [runtimeRetired, setRuntimeRetired] = useState(false);

  const latest = (definitionID: string) => data.processInstances.filter((instance) => instance.definition_id === definitionID).sort((left, right) => right.updated_at.localeCompare(left.updated_at) || right.id.localeCompare(left.id))[0] ?? null;
  const mutate = async (key: string, method: string, params: Record<string, unknown>) => {
    setBusy(key); setError("");
    try { await rpc(apiBase, csrf, method, params); await reload(); return true; }
    catch (reason) { setError(reason instanceof Error ? reason.message : "Managed process operation failed."); return false; }
    finally { setBusy(""); }
  };
  const create = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!data.workspace || !data.project) return;
    const environment: Array<{ name: string; value: string }> = [];
    for (const line of environmentText.split("\n").map((value) => value.trim()).filter(Boolean)) {
      const separator = line.indexOf("=");
      if (separator < 1) { setError("Each environment override must be one NAME=VALUE line."); return; }
      environment.push({ name: line.slice(0, separator), value: line.slice(separator + 1) });
    }
    const port = Number.parseInt(healthPort, 10);
    const health: ManagedProcessHealth = healthType === "process"
      ? { type: "process", interval_millis: 1000, timeout_millis: 500 }
      : { type: healthType, host: healthHost, port: Number.isFinite(port) ? port : 0, ...(healthType === "http" ? { path: healthPath } : {}), interval_millis: 1000, timeout_millis: 500 };
    setBusy("create"); setError("");
    try {
      await rpc(apiBase, csrf, "managed_service.definition.create", {
        workspace: data.workspace.id, project: data.project.id, ...(workstream ? { workstream } : {}), checkout,
        name: name.trim(), description: description.trim() || name.trim(), executable: executable.trim(),
        arguments: argumentsText.split("\n").map((value) => value.trim()).filter(Boolean), working_directory: workingDirectory.trim(), environment,
        profile: "local-process", profile_revision: 1, network_mode: networkMode, health,
        restart_policy: restartPolicy, maximum_restarts: restartPolicy === "never" ? 0 : 3, restart_cooldown_millis: 500,
        stop_signal: "term", stop_grace_millis: 5000, output_byte_limit: 262144, capacity_class: "local_development",
        idempotency_key: newKey("process-define"),
      });
      setCreating(false); setName(""); setDescription(""); setExecutable(""); setArgumentsText(""); setEnvironmentText(""); await reload();
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Could not define the managed process."); }
    finally { setBusy(""); }
  };
  const readLogs = async (instance: ManagedProcessInstance) => {
    setBusy(`logs-${instance.id}`); setError("");
    try { const result = await rpc<{ logs: ManagedProcessLogs }>(apiBase, csrf, "managed_service.logs", { workspace: data.workspace?.id, instance: instance.id }); setLogs(result.logs); }
    catch (reason) { setError(reason instanceof Error ? reason.message : "Could not read managed process logs."); }
    finally { setBusy(""); }
  };
  const decideRequest = async (request: ManagedProcessRequest, accept: boolean) => {
    await mutate(`request-${request.id}`, accept ? "managed_service.request.accept" : "managed_service.request.reject", {
      workspace: data.workspace?.id, request: request.id, expected_revision: request.revision,
      reason: accept ? "Owner approved this exact managed process start request." : "Owner declined this managed process start request.",
      idempotency_key: newKey(accept ? "process-request-accept" : "process-request-reject"),
    });
  };
  const createGrant = async (definition: ManagedProcessDefinition) => {
    const selected = data.domainAgents.find((candidate) => candidate.definition.id === grantAgent && candidate.membership.status === "active");
    if (!selected) { setError("Select one current durable agent."); return; }
    if (!grantActions.length) { setError("Select at least one exact process action."); return; }
    await mutate(`grant-${definition.id}`, "managed_service.grant.create", {
      workspace: data.workspace?.id, definition: definition.id, expected_definition_revision: definition.revision,
      manager_agent: selected.definition.id, expected_membership_revision: selected.membership.revision,
      actions: grantActions, maximum_instances: 1, idempotency_key: newKey("process-grant"),
    });
    setGranting(""); setGrantAgent("");
  };
  const toggleGrantAction = (action: ManagedProcessGrant["actions"][number]) => setGrantActions((current) => current.includes(action) ? current.filter((candidate) => candidate !== action) : [...current, action]);

  const pendingRequests = data.processRequests.filter((request) => request.status === "pending");

  return <section className="m22-block m24-processes">
    <div className="m22-block-heading"><div><h2>managed local processes</h2><p>Durable non-interactive commands attached to exact checkouts: development servers, watchers, local APIs, cookers, mocks, and similar tools.</p></div><button className="m22-command" disabled={!mutable} onClick={() => setCreating((value) => !value)}>{creating ? <X size={13} /> : <Plus size={13} />}{creating ? "close" : "define process"}</button></div>
    {error && <p className="m22-session-error" role="alert">{error}</p>}
    {pendingRequests.length > 0 && <div className="m24-process-requests"><h3>agent requests · no effect yet</h3>{pendingRequests.map((request) => {
      const definition = data.processDefinitions.find((candidate) => candidate.id === request.definition_id);
      const agent = data.agents.find((candidate) => candidate.id === request.agent_id);
      return <article key={request.id}><div><strong>{agent?.name ?? request.agent_id} requests start of {definition?.name ?? request.definition_id}</strong><p>{request.summary}</p><small>Accepting creates one exact instance from definition revision {request.definition_revision}; rejecting starts nothing.</small></div><div><button disabled={!mutable || !!busy} onClick={() => void decideRequest(request, false)}>reject</button><button className="primary" disabled={!mutable || !!busy} onClick={() => void decideRequest(request, true)}>{busy === `request-${request.id}` ? "applying…" : "accept and start"}</button></div></article>;
    })}</div>}
    {creating && <form className="m24-process-form" onSubmit={create}>
      <div className="m22-form-grid"><label><span>name</span><input required maxLength={128} value={name} onChange={(event) => setName(event.target.value)} placeholder="signal-garden-dev" /></label><label><span>description</span><input maxLength={1024} value={description} onChange={(event) => setDescription(event.target.value)} placeholder="Local development server" /></label></div>
      <div className="m22-form-grid"><label><span>checkout</span><select required value={checkout} onChange={(event) => setCheckout(event.target.value)}><option value="">select exact checkout</option>{data.checkouts.filter((item) => item.availability === "available").map((item) => <option value={item.id} key={item.id}>{item.path}</option>)}</select></label><label><span>workstream</span><select value={workstream} onChange={(event) => setWorkstream(event.target.value)}><option value="">domain-wide process</option>{data.objectives.filter((item) => item.status === "active").map((item) => <option value={item.id} key={item.id}>{item.title}</option>)}</select></label></div>
      <div className="m22-form-grid"><label><span>executable</span><input required value={executable} onChange={(event) => setExecutable(event.target.value)} placeholder="npm" /></label><label><span>working directory inside checkout</span><input required value={workingDirectory} onChange={(event) => setWorkingDirectory(event.target.value)} placeholder="." /></label></div>
      <div className="m22-form-grid"><label><span>arguments · one exact argument per line</span><textarea value={argumentsText} onChange={(event) => setArgumentsText(event.target.value)} placeholder={"run\ndev\n--\n--host\n127.0.0.1"} /></label><label><span>environment · one NAME=VALUE per line</span><textarea value={environmentText} onChange={(event) => setEnvironmentText(event.target.value)} placeholder="PORT=4312" /></label></div>
      <div className="m22-form-grid three"><label><span>network exposure</span><select value={networkMode} onChange={(event) => setNetworkMode(event.target.value as ManagedProcessDefinition["network_mode"])}><option value="none">no endpoint</option><option value="loopback">loopback only</option></select></label><label><span>health check</span><select value={healthType} onChange={(event) => setHealthType(event.target.value as ManagedProcessHealth["type"])}><option value="process">process remains alive</option><option value="tcp">TCP endpoint</option><option value="http">HTTP endpoint</option></select></label><label><span>restart policy</span><select value={restartPolicy} onChange={(event) => setRestartPolicy(event.target.value as ManagedProcessDefinition["restart_policy"])}><option value="never">never</option><option value="on_failure">on failure · max 3</option><option value="on_daemon_restart">after daemon restart · max 3</option></select></label></div>
      {healthType !== "process" && <div className="m22-form-grid three"><label><span>health host</span><input required value={healthHost} onChange={(event) => setHealthHost(event.target.value)} /></label><label><span>health port</span><input required type="number" min="1" max="65535" value={healthPort} onChange={(event) => setHealthPort(event.target.value)} /></label>{healthType === "http" && <label><span>HTTP path</span><input required value={healthPath} onChange={(event) => setHealthPath(event.target.value)} /></label>}</div>}
      <div className="m22-exact-effect"><ShieldCheck size={15} /><span><strong>Exact effect</strong> Record one reusable process definition. This does not start it. The daemon will later launch the exact executable and argv in this checkout; no shell interpolation is added.</span></div>
      <div className="m22-form-actions"><button type="button" onClick={() => setCreating(false)}>cancel</button><button className="m22-send" disabled={!mutable || busy === "create" || !checkout || !name.trim() || !executable.trim()}>{busy === "create" ? <LoaderCircle className="spin" size={14} /> : <Plus size={14} />}{busy === "create" ? "defining…" : "define process"}</button></div>
    </form>}
    {data.processDefinitions.filter((definition) => definition.status === "active").length ? <div className="m24-process-list">{data.processDefinitions.filter((definition) => definition.status === "active").map((definition) => {
      const instance = latest(definition.id); const live = instance && ["requested", "starting", "healthy", "degraded", "stopping", "unknown"].includes(instance.status); const url = managedProcessURL(definition); const objective = data.objectives.find((item) => item.id === definition.workstream_id); const exactCheckout = data.checkouts.find((item) => item.id === definition.checkout_id);
      const grants = data.processGrants.filter((grant) => grant.definition_id === definition.id && grant.status === "active");
      return <article key={definition.id}>
        <header><div><strong>{definition.name}</strong><small>{definition.description}</small></div><StatusPill value={instance?.status ?? "not started"} /></header>
        <code>{managedProcessCommand(definition)}</code>
        <dl><div><dt>scope</dt><dd>{objective?.title ?? "domain-wide"}</dd></div><div><dt>checkout</dt><dd>{exactCheckout?.path ?? definition.checkout_id}</dd></div><div><dt>health</dt><dd>{instance ? live ? instance.health_status : "not running" : definition.health.type}</dd></div><div><dt>restart</dt><dd>{definition.restart_policy.replaceAll("_", " ")}</dd></div></dl>
        {grants.length > 0 && <div className="m24-process-grant-summary"><strong>agent authority</strong>{grants.map((grant) => <span key={grant.id}>{data.agents.find((agent) => agent.id === grant.manager_agent_id)?.name ?? grant.manager_agent_id} · {grant.actions.join(", ")} · max {grant.maximum_instances}</span>)}</div>}
        {instance?.diagnostic && <p className="m24-process-diagnostic">{instance.diagnostic}</p>}
        {instance?.status === "unknown" && <div className="m24-process-unknown"><strong>Runtime ownership is unknown</strong><p>Crewfold will not restart, stop, or signal this process. First verify outside Crewfold that the old process has ended, then retire only this stale binding.</p>{resolvingUnknown === instance.id ? <><label><span>owner resolution reason</span><input maxLength={2048} value={unknownReason} onChange={(event) => setUnknownReason(event.target.value)} placeholder="How was the old process confirmed ended?" /></label><label className="m22-confirm"><input type="checkbox" checked={runtimeRetired} onChange={(event) => setRuntimeRetired(event.target.checked)} /><span>I confirm the unknown external process has ended. Crewfold did not stop it.</span></label><div><button onClick={() => { setResolvingUnknown(""); setUnknownReason(""); setRuntimeRetired(false); }}>cancel</button><button className="danger" disabled={!mutable || !!busy || !runtimeRetired || !unknownReason.trim()} onClick={() => void mutate(`resolve-${instance.id}`, "managed_service.resolve_unknown", { workspace: data.workspace?.id, instance: instance.id, expected_revision: instance.revision, runtime_retired_confirmed: true, reason: unknownReason.trim(), idempotency_key: newKey("process-resolve-unknown") }).then((resolved) => { if (resolved) { setResolvingUnknown(""); setUnknownReason(""); setRuntimeRetired(false); } })}>{busy === `resolve-${instance.id}` ? "recording…" : "retire stale runtime binding"}</button></div></> : <button disabled={!mutable || !!busy} onClick={() => setResolvingUnknown(instance.id)}>resolve unknown runtime…</button>}</div>}
        {granting === definition.id && <div className="m24-process-grant-form"><label><span>durable agent</span><select value={grantAgent} onChange={(event) => setGrantAgent(event.target.value)}><option value="">select agent</option>{data.domainAgents.filter((agent) => agent.membership.status === "active").map((agent) => <option value={agent.definition.id} key={agent.definition.id}>{agent.definition.name}</option>)}</select></label><fieldset><legend>exact allowed actions</legend>{(["inspect", "logs", "start", "stop", "restart", "delegate"] as ManagedProcessGrant["actions"]).map((action) => <label key={action}><input type="checkbox" checked={grantActions.includes(action)} onChange={() => toggleGrantAction(action)} />{action}</label>)}</fieldset><p>This authority is definition-specific, revision-bound, limited to one concurrent instance, and may be narrowed when delegated. It does not authorize a shell or a different command.</p><div><button onClick={() => setGranting("")}>cancel</button><button className="primary" disabled={!mutable || !!busy || !grantAgent || !grantActions.length} onClick={() => void createGrant(definition)}>{busy === `grant-${definition.id}` ? "granting…" : "grant exact authority"}</button></div></div>}
        <footer>{url && instance?.health_status === "healthy" && <a href={url} target="_blank" rel="noreferrer">open {url}</a>}<button disabled={!mutable || !!busy} onClick={() => { setGranting((current) => current === definition.id ? "" : definition.id); setGrantAgent(""); }}>agent authority</button><span />{instance && <button disabled={!!busy} onClick={() => void readLogs(instance)}>{busy === `logs-${instance.id}` ? "reading…" : "logs"}</button>}{live && instance && ["healthy", "degraded"].includes(instance.status) && <><button disabled={!!busy || !mutable} onClick={() => void mutate(`restart-${instance.id}`, "managed_service.restart", { workspace: data.workspace?.id, instance: instance.id, expected_revision: instance.revision, idempotency_key: newKey("process-restart") })}>{busy === `restart-${instance.id}` ? "restarting…" : "restart"}</button><button className="danger" disabled={!!busy || !mutable} onClick={() => void mutate(`stop-${instance.id}`, "managed_service.stop", { workspace: data.workspace?.id, instance: instance.id, expected_revision: instance.revision, idempotency_key: newKey("process-stop") })}>{busy === `stop-${instance.id}` ? "stopping…" : "stop"}</button></>}{!live && <button className="primary" disabled={!!busy || !mutable} onClick={() => void mutate(`start-${definition.id}`, "managed_service.start", { workspace: data.workspace?.id, definition: definition.id, expected_revision: definition.revision, idempotency_key: newKey("process-start") })}>{busy === `start-${definition.id}` ? "starting…" : instance ? "start new instance" : "start"}</button>}</footer>
      </article>;
    })}</div> : !creating && <p className="m22-empty">No managed process is defined. Define the exact command once, then start, inspect, stop, or restart it here.</p>}
    {logs && <div className="m24-process-logs"><header><strong>{logs.state} logs</strong><button onClick={() => setLogs(null)} aria-label="Close managed process logs"><X size={13} /></button></header><section><h3>stdout {logs.stdout.truncated ? `· ${logs.stdout.omitted_bytes} bytes omitted` : ""}</h3><pre>{logs.stdout.text || "(empty)"}</pre></section><section><h3>stderr {logs.stderr.truncated ? `· ${logs.stderr.omitted_bytes} bytes omitted` : ""}</h3><pre>{logs.stderr.text || "(empty)"}</pre></section></div>}
  </section>;
}

function DomainHome({ data, chooseAgent, reviewWorkstream, inspectTask, inspectRun, notice = "", apiBase, csrf, mutable, reload }: { data: WorkbenchData; chooseAgent: (agent: DomainAgent) => void; reviewWorkstream: (objective: Objective) => void; inspectTask: (task: TaskDetail) => void; inspectRun: (run: Run) => void; notice?: string; apiBase: string; csrf: string; mutable: boolean; reload: () => Promise<void> }) {
  const projectObjectives = data.objectives.filter((objective) => objective.project_id === data.project?.id);
  const activeObjectives = projectObjectives.filter((objective) => objective.status === "active");
  const closedObjectives = projectObjectives.filter((objective) => objective.status !== "active");
  const activeAgents = data.domainAgents.filter((agent) => agent.membership.status === "active");
  const retiredAgents = data.domainAgents.filter((agent) => agent.membership.status === "retired");
  const projectTasks = data.tasks.filter((detail) => detail.task.project_id === data.project?.id);
  const projectRuns = data.runs.filter((run) => run.project_id === data.project?.id);
  const attention = projectTasks.filter((detail) => ["blocked", "changes_requested", "failed"].includes(detail.task.status));
  const activeRuns = projectRuns.filter((run) => ["requested", "starting", "active", "blocked", "stopping", "lost"].includes(run.status));
  const currentKnowledge = data.knowledge.filter((revision) => revision.review_status === "accepted" && revision.currency_status === "current");
  const proposedKnowledge = data.knowledge.filter((revision) => revision.review_status === "proposed");
  const pendingWork = data.workProposals.filter((proposal) => proposal.status === "pending");
  const decidedWork = data.workProposals.filter((proposal) => proposal.status !== "pending");
  const objectiveTitleCounts = activeObjectives.reduce((counts, objective) => {
    const key = objective.title.trim().toLocaleLowerCase();
    counts.set(key, (counts.get(key) ?? 0) + 1);
    return counts;
  }, new Map<string, number>());
  const checkoutUseCounts = activeObjectives.reduce((counts, objective) => {
    if (objective.primary_checkout_id) counts.set(objective.primary_checkout_id, (counts.get(objective.primary_checkout_id) ?? 0) + 1);
    return counts;
  }, new Map<string, number>());
  return <section className="m22-domain-home">
    <header>
      <p className="m22-kicker">domain</p>
      <h1>{data.project?.name}</h1>
      <p>{activeObjectives.length ? `${activeObjectives.length} active workstream${activeObjectives.length === 1 ? "" : "s"}, ${activeAgents.length} durable agent${activeAgents.length === 1 ? "" : "s"}, and ${data.checkouts.length} attached checkout${data.checkouts.length === 1 ? "" : "s"}.` : "A durable coordination and knowledge boundary. No active workstream is recorded yet."}</p>
    </header>
    {notice && <div className="m22-success"><ShieldCheck size={14} />{notice}</div>}
    {pendingWork.length > 0 && <section className="m22-block m22-work-proposals"><header><div><h2>needs your review</h2><p>{pendingWork.length} typed coordinator proposal{pendingWork.length === 1 ? "" : "s"}. Conversation alone has changed nothing.</p></div><span>{pendingWork.length}</span></header>{pendingWork.map((proposal) => <DomainWorkProposalReview key={proposal.id} data={data} proposal={proposal} apiBase={apiBase} csrf={csrf} mutable={mutable} reload={reload} />)}</section>}
    {attention.length > 0 && <section className="m22-block"><h2>needs attention</h2>{attention.map((detail) => {
      const run = projectRuns.filter((candidate) => candidate.task_id === detail.task.id).sort((left, right) => right.updated_at.localeCompare(left.updated_at))[0];
      return <button key={detail.task.id} className="m22-line" onClick={() => run ? inspectRun(run) : inspectTask(detail)}><span><strong>{detail.task.title}</strong><small>{detail.task.blocked_reason || detail.task.status.replaceAll("_", " ")}{run ? " · open exact run" : " · inspect task gate"}</small></span><StatusPill value={detail.task.status} /></button>;
    })}</section>}
    <div className="m22-columns">
      <section className="m22-block"><h2>active workstreams</h2>{activeObjectives.length ? activeObjectives.map((objective) => {
        const checkout = data.checkouts.find((candidate) => candidate.id === objective.primary_checkout_id);
        const shared = objective.primary_checkout_id && (checkoutUseCounts.get(objective.primary_checkout_id) ?? 0) > 1;
        return <button className={`m22-line ${shared ? "warning" : ""}`} key={objective.id} onClick={() => reviewWorkstream(objective)}><span><strong>{objective.title}</strong><small>{checkout?.path ?? "No primary checkout bound"}</small><small>{projectTasks.filter((detail) => detail.task.objective_id === objective.id && !["completed", "failed", "cancelled"].includes(detail.task.status)).length} open tasks{shared ? " · shared checkout—coordinate overlapping writes" : ""}{(objectiveTitleCounts.get(objective.title.trim().toLocaleLowerCase()) ?? 0) > 1 ? ` · duplicate title · revision ${objective.revision}` : ""}</small></span><StatusPill value={objective.status} /></button>;
      }) : <p className="m22-empty">No active workstreams.</p>}</section>
      <section className="m22-block"><div className="m22-block-heading"><h2>attached checkouts</h2><CheckoutAttach data={data} apiBase={apiBase} csrf={csrf} mutable={mutable} reload={reload} /></div>{data.checkouts.length ? data.checkouts.map((checkout) => <div className="m22-line static" key={checkout.id}><span><strong>{checkout.path}</strong><small>{checkout.branch || "detached"} · {checkout.write_mode}{checkout.diagnostic ? ` · ${checkout.diagnostic}` : ""}</small></span><StatusPill value={checkout.availability} /></div>) : <p className="m22-empty">No checkout is attached. Attach an existing local Git checkout to give sessions and assigned runs an exact resource.</p>}</section>
    </div>
    <div className={`m22-columns ${activeRuns.length ? "" : "single"}`}>
      <section className="m22-block"><h2>durable agents</h2>{activeAgents.length ? activeAgents.map((agent) => <button className="m22-line" key={agent.definition.id} onClick={() => chooseAgent(agent)}><span><strong>{agent.definition.name}</strong><small>{agent.definition.role} · {agent.definition.provider} through {agent.definition.runtime}</small></span><StatusPill value={latestRunForAgent(projectRuns, agent.definition.id)?.status ?? "idle"} /></button>) : <p className="m22-empty">No active durable agents.</p>}</section>
      {activeRuns.length > 0 && <section className="m22-block"><h2>current runs</h2>{activeRuns.map((run) => <button className="m22-line" key={run.id} onClick={() => inspectRun(run)}><span><strong>{projectTasks.find((detail) => detail.task.id === run.task_id)?.task.title ?? run.id}</strong><small>{data.agents.find((agent) => agent.id === run.agent_id)?.name ?? run.agent_id}</small></span><StatusPill value={run.status} /></button>)}</section>}
    </div>
    <ManagedProcesses data={data} apiBase={apiBase} csrf={csrf} mutable={mutable} reload={reload} />
    {(currentKnowledge.length > 0 || proposedKnowledge.length > 0) && <section className="m22-block"><h2>shared domain knowledge</h2>
      {proposedKnowledge.length > 0 && <div className="m22-knowledge-proposals"><p><strong>{proposedKnowledge.length} sourced proposal{proposedKnowledge.length === 1 ? "" : "s"} need owner review.</strong> Agent conversation and coordination threads do not become knowledge by themselves.</p>{proposedKnowledge.map((revision) => <KnowledgeProposalReview key={revision.id} data={data} revision={revision} apiBase={apiBase} csrf={csrf} mutable={mutable} reload={reload} />)}</div>}
      {currentKnowledge.map((revision) => <details className="m22-knowledge" key={revision.id}><summary><span><strong>{revision.title}</strong><small>{revision.type} · {revision.verification_status} · revision {revision.revision_number} · accepted {displayTime(revision.accepted_at)}</small></span><StatusPill value="current" /></summary><p>{revision.body}</p><footer>proposed by {revision.proposed_by_type} {revision.proposed_by} · {revision.sources.length} exact source{revision.sources.length === 1 ? "" : "s"}</footer></details>)}
    </section>}
    {data.threads.length > 0 && <CoordinationThreads data={data} apiBase={apiBase} csrf={csrf} />}
    {decidedWork.length > 0 && <details className="m22-history"><summary><ClipboardCheck size={14} /> coordinator proposal history <span>{decidedWork.length}</span></summary><div className="m22-proposal-history">{decidedWork.map((proposal) => <DomainWorkProposalReview key={proposal.id} data={data} proposal={proposal} apiBase={apiBase} csrf={csrf} mutable={mutable} reload={reload} />)}</div></details>}
    {(retiredAgents.length > 0 || closedObjectives.length > 0) && <details className="m22-history"><summary><Archive size={14} /> retired and closed history <span>{retiredAgents.length + closedObjectives.length}</span></summary><div>{retiredAgents.map((agent) => <div className="m22-line static" key={agent.definition.id}><span><strong>{agent.definition.name}</strong><small>retired agent · {agent.definition.role} · updated {displayTime(agent.membership.updated_at)}</small></span><StatusPill value="retired" /></div>)}{closedObjectives.map((objective) => <div className="m22-line static" key={objective.id}><span><strong>{objective.title}</strong><small>closed workstream · revision {objective.revision} · updated {displayTime(objective.updated_at)}</small></span><StatusPill value={objective.status} /></div>)}</div></details>}
  </section>;
}

function CoordinationThreads({ data, apiBase, csrf, threads = data.threads, heading = "coordination threads" }: { data: WorkbenchData; apiBase: string; csrf: string; threads?: ThreadSummary[]; heading?: string }) {
  const [selected, setSelected] = useState<ThreadDetail | null>(null);
  const [loading, setLoading] = useState("");
  const [error, setError] = useState("");
  const open = async (summary: ThreadSummary) => {
    if (!data.workspace) return;
    setLoading(summary.thread.id); setError("");
    try {
      const result = await rpc<{ detail: ThreadDetail }>(apiBase, csrf, "thread.show", { workspace: data.workspace.id, thread: summary.thread.id });
      setSelected(result.detail);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Could not read the coordination thread.");
    } finally {
      setLoading("");
    }
  };
  return <section className="m22-block m22-threads"><h2>{heading}</h2><p className="m22-caveat">Durable messages exchanged by owners and agents. Accepted domain knowledge is kept separately.</p>
    {threads.length ? <details className="m22-thread-index"><summary><span>{threads.length} thread{threads.length === 1 ? "" : "s"}</span><small>latest {displayTime(threads[0]?.thread.updated_at)}</small></summary><div>{threads.map((summary) => <button className="m22-line" key={summary.thread.id} onClick={() => void open(summary)}><span><strong>{summary.thread.subject}</strong><small>{summary.message_count} message{summary.message_count === 1 ? "" : "s"} · updated {displayTime(summary.thread.updated_at)}</small></span>{loading === summary.thread.id ? <LoaderCircle className="spin" size={14} /> : <StatusPill value={summary.thread.status} />}</button>)}</div></details> : <p className="m22-empty">No durable coordination thread is recorded in this scope.</p>}
    {error && <p className="m22-session-error" role="alert">{error}</p>}
    {selected && <article className="m22-thread-detail"><header><div><p className="m22-kicker">coordination thread</p><h3>{selected.thread.subject}</h3><small>{selected.messages.length} message{selected.messages.length === 1 ? "" : "s"} · audit ID {selected.thread.id}</small></div><button onClick={() => setSelected(null)} aria-label="Close coordination thread"><X size={14} /></button></header>{selected.messages.map((message) => {
      const delivery = selected.recipients.filter((recipient) => recipient.message_id === message.id);
      return <div className="m22-thread-message" key={message.id}><p><strong>{message.sender_agent_name || (message.sender_type === "owner" ? "You" : message.sender_type)}</strong><span>{message.kind.replaceAll("_", " ")} · {displayTime(message.created_at)}</span></p><div>{message.body}</div>{delivery.length > 0 && <small>to {delivery.map((recipient) => `${recipient.recipient_name} · ${recipient.status}${recipient.wake_status === "not_requested" ? "" : ` · wake ${recipient.wake_status}`}`).join("; ")}</small>}</div>;
    })}</article>}
  </section>;
}

function DomainWorkstreamCreatePanel({ data, apiBase, csrf, mutable, close, created, reload }: { data: WorkbenchData; apiBase: string; csrf: string; mutable: boolean; close: () => void; created: (title: string) => void; reload: () => Promise<void> }) {
  const [title, setTitle] = useState("");
  const eligibleCheckouts = data.checkouts.filter((checkout) => checkout.availability === "available" && checkout.write_mode !== "read_only");
  const [checkoutID, setCheckoutID] = useState(eligibleCheckouts.length === 1 ? eligibleCheckouts[0].id : "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submitting = useRef(false);
  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!data.workspace || !data.project || !checkoutID || submitting.current) return;
    const exactTitle = title.trim();
    if (data.objectives.some((objective) => objective.project_id === data.project?.id && objective.status === "active" && objective.title.trim().toLocaleLowerCase() === exactTitle.toLocaleLowerCase())) {
      setError(`An active workstream named “${exactTitle}” already exists in this domain.`);
      return;
    }
    submitting.current = true; setBusy(true); setError("");
    try {
      await rpc(apiBase, csrf, "objective.create", { workspace: data.workspace.id, project: data.project.id, primary_checkout: checkoutID, reference_checkouts: [], title: exactTitle, budget: { token_limit: 0, cost_cents: 0, time_seconds: 0 }, idempotency_key: newKey("domain-workstream") });
      await reload(); created(exactTitle);
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Could not create the workstream."); }
    finally { submitting.current = false; setBusy(false); }
  };
  return <section className="m22-agent-create m22-workstream-create">
    <header><div><p className="m22-kicker">owner operation</p><h1>Create a workstream</h1><p>A workstream is one bounded outcome in this domain, anchored to the persistent checkout where its agents will do the work.</p></div><button onClick={close} aria-label="Close workstream creation"><X size={15} /></button></header>
    <form onSubmit={submit}><label><span>workstream name</span><input autoFocus required maxLength={256} value={title} onChange={(event) => setTitle(event.target.value)} placeholder="Terrain consolidation" /></label>
      <label><span>primary persistent checkout</span><select required value={checkoutID} onChange={(event) => setCheckoutID(event.target.value)}><option value="">select exact checkout</option>{eligibleCheckouts.map((checkout) => <option key={checkout.id} value={checkout.id}>{checkout.path} · {checkout.write_mode}</option>)}</select></label>
      {!eligibleCheckouts.length && <p className="m22-session-error">Attach one available writable Git checkout before creating a workstream.</p>}
      {checkoutID && data.objectives.some((objective) => objective.status === "active" && objective.primary_checkout_id === checkoutID) && <p className="m22-session-warning">Another active workstream already uses this checkout. That is allowed, but both workstreams must coordinate claims and overlapping changes explicitly.</p>}
      <div className="m22-exact-effect"><ShieldCheck size={15} /><span><strong>Exact effect</strong> Create one empty objective-backed workstream bound to this exact existing checkout. No clone, dependency bootstrap, agent move, task, run, or authority grant occurs.</span></div>
      <p className="m22-caveat">After creation it appears in the hierarchy even while empty. Coordinator proposals can place durable agents and publish its executable task graph atomically.</p>
      {error && <p className="m22-session-error" role="alert">{error}</p>}
      <div className="m22-form-actions"><button type="button" onClick={close}>cancel</button><button className="m22-send" disabled={!mutable || busy || !title.trim() || !checkoutID}>{busy ? <LoaderCircle className="spin" size={14} /> : <Plus size={14} />} {busy ? "creating…" : "create workstream"}</button></div>
    </form>
  </section>;
}

function DomainAgentPlacementEditor({ data, agent, apiBase, csrf, mutable, reload }: { data: WorkbenchData; agent: DomainAgent; apiBase: string; csrf: string; mutable: boolean; reload: () => Promise<void> }) {
  const [editing, setEditing] = useState(false);
  const [parent, setParent] = useState(agent.membership.parent_agent_id ?? "");
  const [workstream, setWorkstream] = useState(agent.membership.workstream_id ?? "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  useEffect(() => { setParent(agent.membership.parent_agent_id ?? ""); setWorkstream(agent.membership.workstream_id ?? ""); }, [agent.definition.id, agent.membership.revision]);
  useEffect(() => { setEditing(false); setError(""); setNotice(""); }, [agent.definition.id]);
  const submit = async (event: React.FormEvent) => {
    event.preventDefault(); setBusy(true); setError(""); setNotice("");
    try {
      await rpc(apiBase, csrf, "domain.agent.update", { workspace: data.workspace?.id, project: data.project?.id, agent: agent.definition.id, parent_agent: parent, workstream, expected_revision: agent.membership.revision, idempotency_key: newKey("domain-agent-placement") });
      await reload(); setNotice("Placement updated in the canonical hierarchy."); setEditing(false);
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Could not update agent placement."); }
    finally { setBusy(false); }
  };
  return <section className="m22-placement"><h3>hierarchy placement</h3>{editing ? <form onSubmit={submit}><label><span>parent</span><select value={parent} onChange={(event) => setParent(event.target.value)}><option value="">domain root</option>{data.domainAgents.filter((candidate) => candidate.membership.status === "active" && candidate.definition.id !== agent.definition.id).map((candidate) => <option key={candidate.definition.id} value={candidate.definition.id}>{candidate.definition.name}</option>)}</select></label><label><span>workstream</span><select value={workstream} onChange={(event) => setWorkstream(event.target.value)}><option value="">domain-wide</option>{data.objectives.filter((objective) => objective.project_id === data.project?.id && objective.status === "active").map((objective) => <option key={objective.id} value={objective.id}>{objective.title}</option>)}</select></label><small>Placement routes owner attention only. It grants no authority.</small>{error && <p className="m22-session-error" role="alert">{error}</p>}<div><button type="button" onClick={() => setEditing(false)}>cancel</button><button disabled={!mutable || busy}>{busy ? "saving…" : "save placement"}</button></div></form> : <button className="m22-command" disabled={!mutable} onClick={() => setEditing(true)}>edit placement</button>}{notice && <p className="m22-placement-notice">{notice}</p>}</section>;
}

function DomainAgentRetirementPanel({ data, agent, apiBase, csrf, mutable, close, retired, reload }: { data: WorkbenchData; agent: DomainAgent; apiBase: string; csrf: string; mutable: boolean; close: () => void; retired: () => void; reload: () => Promise<void> }) {
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const children = data.domainAgents.filter((candidate) => candidate.membership.status === "active" && candidate.membership.parent_agent_id === agent.definition.id);
  const assignments = data.tasks.filter((detail) => detail.task.assigned_agent_id === agent.definition.id && !["completed", "failed", "cancelled"].includes(detail.task.status));
  const runs = data.runs.filter((run) => run.agent_id === agent.definition.id && ["requested", "starting", "active", "blocked", "stopping", "lost"].includes(run.status));
  const blockers = [
    ...(children.length ? [`${children.length} active child agent${children.length === 1 ? "" : "s"} must be moved or retired first`] : []),
    ...(assignments.length ? [`${assignments.length} nonterminal assignment${assignments.length === 1 ? "" : "s"} must be completed, cancelled, or reassigned first`] : []),
    ...(runs.length ? [`${runs.length} live or unresolved run${runs.length === 1 ? "" : "s"} must be settled first`] : []),
  ];
  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!data.workspace || !data.project || blockers.length || !confirmed) return;
    setBusy(true); setError("");
    try {
      await rpc(apiBase, csrf, "domain.agent.update", { workspace: data.workspace.id, project: data.project.id, agent: agent.definition.id, status: "retired", expected_revision: agent.membership.revision, idempotency_key: newKey("domain-agent-retire") });
      await reload(); retired();
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Could not retire the durable agent."); }
    finally { setBusy(false); }
  };
  return <section className="m22-agent-create m22-lifecycle-review">
    <header><div><p className="m22-kicker">review lifecycle change</p><h1>Retire {agent.definition.name}</h1><p>Retirement removes this agent from the active hierarchy and prevents its durable session from exercising Crewfold tool authority. Its definition, conversation, receipts, assignments, runs, and events remain inspectable history.</p></div><button onClick={close} aria-label="Close retirement review"><X size={15} /></button></header>
    <form onSubmit={submit}>
      <dl className="m22-review-facts"><div><dt>agent</dt><dd>{agent.definition.name}</dd></div><div><dt>role</dt><dd>{agent.definition.role}</dd></div><div><dt>membership revision</dt><dd>{agent.membership.revision}</dd></div><div><dt>historical record</dt><dd>preserved</dd></div></dl>
      {blockers.length ? <div className="m22-lifecycle-blockers" role="alert"><strong>Retirement is blocked</strong><ul>{blockers.map((blocker) => <li key={blocker}>{blocker}</li>)}</ul><p>Tree placement, task state, and runtime state must be resolved explicitly; retirement will not rewrite them.</p></div> : <div className="m22-exact-effect"><Archive size={15} /><span><strong>Exact effect</strong> Set this domain membership to retired and clear its default-entry flag. The underlying agent definition and all canonical history remain.</span></div>}
      {!blockers.length && <label className="m22-confirm"><input type="checkbox" checked={confirmed} onChange={(event) => setConfirmed(event.target.checked)} /><span>I understand this retires the membership; it does not erase history or stop unrelated work.</span></label>}
      <p className="m22-caveat">The Store rechecks active children, assignments, unresolved runs, and staffing grants in the same transaction. A concurrent change fails without a partial effect.</p>
      {error && <p className="m22-session-error" role="alert">{error}</p>}
      <div className="m22-form-actions"><button type="button" onClick={close}>keep active</button><button className="m22-danger" disabled={!mutable || busy || blockers.length > 0 || !confirmed}>{busy ? <LoaderCircle className="spin" size={14} /> : <Archive size={14} />} {busy ? "retiring…" : "retire agent"}</button></div>
    </form>
  </section>;
}

function DomainWorkstreamLifecyclePanel({ data, objective, inspectTask, inspectRun, apiBase, csrf, mutable, close, cancelled, reload }: { data: WorkbenchData; objective: Objective; inspectTask: (task: TaskDetail) => void; inspectRun: (run: Run) => void; apiBase: string; csrf: string; mutable: boolean; close: () => void; cancelled: () => void; reload: () => Promise<void> }) {
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [delivery, setDelivery] = useState<WorkstreamDelivery | null>(null);
  const [deliveryReason, setDeliveryReason] = useState("");
  const agents = data.domainAgents.filter((agent) => agent.membership.status === "active" && agent.membership.workstream_id === objective.id);
  const allTasks = dependencyOrderedTasks(data.tasks.filter((detail) => detail.task.objective_id === objective.id));
  const tasks = allTasks.filter((detail) => !["completed", "failed", "cancelled"].includes(detail.task.status));
  const runs = data.runs.filter((run) => allTasks.some((detail) => detail.task.id === run.task_id) && ["requested", "starting", "active", "blocked", "stopping", "lost"].includes(run.status));
  const checkout = data.checkouts.find((candidate) => candidate.id === objective.primary_checkout_id);
  const sharing = data.objectives.filter((candidate) => candidate.status === "active" && candidate.primary_checkout_id && candidate.primary_checkout_id === objective.primary_checkout_id);
  const blockers = [
    ...(agents.length ? [`${agents.length} active durable agent${agents.length === 1 ? " is" : "s are"} scoped here`] : []),
    ...(tasks.length ? [`${tasks.length} nonterminal task${tasks.length === 1 ? " remains" : "s remain"}`] : []),
    ...(runs.length ? [`${runs.length} live or unresolved run${runs.length === 1 ? " remains" : "s remain"}`] : []),
  ];
  useEffect(() => {
    setDelivery(null);
    if (!data.workspace) return;
    void rpc<{ delivery: WorkstreamDelivery }>(apiBase, csrf, "workstream.delivery.show", { workspace: data.workspace.id, objective: objective.id })
      .then((result) => { setDelivery(result.delivery); setError(""); })
      .catch((reason) => setError(reason instanceof Error ? reason.message : "Could not derive the exact delivery state."));
  }, [apiBase, csrf, data.highWater, data.workspace?.id, objective.id]);
  const decideDelivery = async (accept: boolean) => {
    if (!data.workspace || !delivery || busy || !mutable) return;
    setBusy(true); setError("");
    try {
      const result = await rpc<{ delivery: WorkstreamDelivery }>(apiBase, csrf, accept ? "workstream.delivery.accept" : "workstream.delivery.reject", {
        workspace: data.workspace.id, objective: objective.id,
        expected_objective_revision: delivery.objective_revision, expected_sha256: delivery.sha256,
        ...(accept ? {} : { reason: deliveryReason.trim() }), idempotency_key: newKey(accept ? "delivery-accept" : "delivery-reject"),
      });
      setDelivery(result.delivery); setDeliveryReason(""); await reload();
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Could not record the exact delivery decision."); }
    finally { setBusy(false); }
  };
  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!data.workspace || blockers.length || !confirmed || objective.status !== "active") return;
    setBusy(true); setError("");
    try {
      await rpc(apiBase, csrf, "objective.update", { workspace: data.workspace.id, objective: objective.id, status: "cancelled", expected_revision: objective.revision, idempotency_key: newKey("domain-workstream-cancel") });
      await reload(); cancelled();
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Could not cancel the workstream."); }
    finally { setBusy(false); }
  };
  return <section className="m22-agent-create m22-lifecycle-review">
    <header><div><p className="m22-kicker">workstream</p><h1>{objective.title}</h1><p>One outcome, one persistent primary checkout, its durable team, and the exact dependency/output gates that control progress.</p></div><button onClick={close} aria-label="Close workstream lifecycle"><X size={15} /></button></header>
    <form onSubmit={submit}>
      <dl className="m22-review-facts"><div><dt>workstream</dt><dd>{objective.status}</dd></div><div><dt>delivery</dt><dd>{delivery?.state.replaceAll("_", " ") ?? "deriving exact state…"}</dd></div><div><dt>primary checkout</dt><dd>{checkout?.path ?? "not bound"}</dd></div><div><dt>durable team</dt><dd>{agents.length ? agents.map((agent) => agent.definition.name).join(", ") : "no agents placed"}</dd></div><div><dt>open tasks</dt><dd>{tasks.length}</dd></div></dl>
      {sharing.length > 1 && <div className="m22-lifecycle-blockers"><strong>Shared checkout</strong><p>{sharing.map((candidate) => candidate.title).join(" and ")} use this same persistent checkout. This is allowed, but concurrent work must use non-overlapping claims or explicit coordination.</p></div>}
      <section className="m22-workstream-graph"><h2>execution chain</h2>{allTasks.length ? allTasks.map((detail) => {
        const assignment = data.agents.find((candidate) => candidate.id === detail.task.assigned_agent_id)?.name;
        const run = data.runs.filter((candidate) => candidate.task_id === detail.task.id).sort((left, right) => right.updated_at.localeCompare(left.updated_at))[0];
        const dependencyText = detail.dependencies.length ? `after ${detail.dependencies.map((dependency) => {
          const source = allTasks.find((candidate) => candidate.task.id === dependency.depends_on_task_id)?.task.title ?? dependency.depends_on_task_id;
          return `${source} → ${(dependency.delivery_requirement ?? "completion").replaceAll("_", " ")}`;
        }).join(", ")}` : "entry task";
        const outcome = run ? runOutcome(run, detail.task) : null;
        return <button type="button" className="m22-line" key={detail.task.id} onClick={() => run ? inspectRun(run) : inspectTask(detail)}><span><strong>{detail.task.title}</strong><small>{detail.task.task_class} · {dependencyText}</small><small>{taskProgressExplanation(detail, allTasks, data.runs, data.agents)}{assignment && !detail.readiness.ready && detail.task.status !== "completed" ? ` · assigned to ${assignment}` : ""}</small></span><StatusPill value={outcome?.label ?? detail.task.status} tone={outcome?.tone ?? detail.task.status} /></button>;
      }) : <p className="m22-empty">No task graph has been accepted for this workstream.</p>}</section>
      {delivery && <section className={`m24-delivery ${statusTone(delivery.state)}`} aria-label="Exact workstream delivery"><header><div><p className="m22-kicker">exact delivery revision</p><h2>{delivery.state === "verified_awaiting_owner_acceptance" ? "Verified — awaiting your acceptance" : delivery.state.replaceAll("_", " ")}</h2></div><StatusPill value={`${delivery.completed_tasks}/${delivery.task_count} tasks`} tone={delivery.state} /></header><p>{delivery.verification_tasks ? `${delivery.passing_verifications} of ${delivery.verification_tasks} structured verification tasks passed.` : "No structured verification task is present."} This state is derived from canonical task, assessment, handoff, and evidence records—not provider prose.</p>{delivery.blockers.length > 0 && <ul>{delivery.blockers.map((blocker) => <li key={blocker}>{blocker}</li>)}</ul>}{delivery.evidence.length > 0 && <p><strong>Evidence:</strong> {delivery.evidence.length} exact reference{delivery.evidence.length === 1 ? "" : "s"}</p>}<small>delivery {delivery.sha256.slice(0, 16)}… · objective revision {delivery.objective_revision}</small>{(delivery.state === "verified_awaiting_owner_acceptance" || delivery.state === "rejected") && <div className="m24-delivery-actions"><label><span>Reason if rejecting this exact delivery</span><input value={deliveryReason} maxLength={2048} onChange={(event) => setDeliveryReason(event.target.value)} placeholder="What must change before acceptance?" /></label><button type="button" disabled={busy || !mutable || !deliveryReason.trim()} onClick={() => void decideDelivery(false)}>reject delivery</button><button type="button" className="m22-command" disabled={busy || !mutable} onClick={() => void decideDelivery(true)}><ClipboardCheck size={14} /> accept exact delivery</button></div>}{delivery.state === "accepted" && <p className="m22-exact-effect"><ClipboardCheck size={14} /><span><strong>Accepted by the local owner.</strong> This closed the workstream only; it did not commit, push, publish, deploy, or start a process.</span></p>}</section>}
      {blockers.length ? <div className="m22-lifecycle-blockers" role="alert"><strong>Cancellation is blocked</strong><ul>{blockers.map((blocker) => <li key={blocker}>{blocker}</li>)}</ul><p>Move or retire scoped agents and resolve every task/run first. Crewfold will not detach or cancel them implicitly.</p></div> : <div className="m22-exact-effect"><Archive size={15} /><span><strong>Exact effect</strong> Change this objective from active to cancelled. It moves to closed history; all contained canonical records remain available.</span></div>}
      {!blockers.length && objective.status === "active" && <label className="m22-confirm"><input type="checkbox" checked={confirmed} onChange={(event) => setConfirmed(event.target.checked)} /><span>I understand this closes the workstream without erasing its history.</span></label>}
      {error && <p className="m22-session-error" role="alert">{error}</p>}
      <div className="m22-form-actions"><button type="button" onClick={close}>back to domain</button>{objective.status === "active" && <button className="m22-danger" disabled={!mutable || busy || blockers.length > 0 || !confirmed}>{busy ? <LoaderCircle className="spin" size={14} /> : <Archive size={14} />} {busy ? "cancelling…" : "cancel workstream"}</button>}</div>
    </form>
  </section>;
}

function sessionItemLabel(item: DomainAgentSessionItem, agentName: string) {
  if (item.type === "userMessage") return item.origin === "crewfold_task" ? "crewfold task" : item.origin === "crewfold_delivery" ? "crewfold delivery" : "you";
  if (item.type === "agentMessage") return agentName;
  if (item.type === "commandExecution") return "command";
  if (item.type === "dynamicToolCall") return item.command?.startsWith("crewfold_") ? "crewfold tool" : "provider tool";
  if (item.type === "mcpToolCall") return "mcp tool";
  if (item.type === "collabAgentToolCall") return "temporary provider helper";
  if (item.type === "subAgentActivity") return "temporary provider helper";
  if (item.type === "fileChange") return "files";
  if (item.type === "webSearch") return "web search";
  return item.type.replaceAll(/([A-Z])/g, " $1").toLowerCase();
}

function MarkdownInline({ text }: { text: string }) {
  const tokens = text.split(/(`[^`\n]+`|\*\*[^*\n]+\*\*|\*[^*\n]+\*|\[[^\]\n]+\]\(https?:\/\/[^)\s]+\))/g).filter(Boolean);
  return <>{tokens.map((token, index) => {
    if (token.startsWith("`") && token.endsWith("`")) return <code key={index}>{token.slice(1, -1)}</code>;
    if (token.startsWith("**") && token.endsWith("**")) return <strong key={index}>{token.slice(2, -2)}</strong>;
    if (token.startsWith("*") && token.endsWith("*")) return <em key={index}>{token.slice(1, -1)}</em>;
    const link = token.match(/^\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)$/);
    if (link) return <a key={index} href={link[2]} target="_blank" rel="noreferrer">{link[1]}</a>;
    return token;
  })}</>;
}

function MarkdownText({ text }: { text: string }) {
  const lines = text.replaceAll("\r\n", "\n").split("\n");
  const blocks: ReactNode[] = [];
  let index = 0;
  while (index < lines.length) {
    if (!lines[index].trim()) { index++; continue; }
    if (lines[index].trimStart().startsWith("```")) {
      const language = lines[index].trim().slice(3).trim();
      const body: string[] = [];
      index++;
      while (index < lines.length && !lines[index].trimStart().startsWith("```")) body.push(lines[index++]);
      if (index < lines.length) index++;
      blocks.push(<pre className="m22-markdown-code" key={`code-${index}`}><code data-language={language || undefined}>{body.join("\n")}</code></pre>);
      continue;
    }
    const heading = lines[index].match(/^(#{1,4})\s+(.+)$/);
    if (heading) {
      const level = heading[1].length;
      const content = <MarkdownInline text={heading[2]} />;
      blocks.push(level <= 2 ? <h3 key={`heading-${index}`}>{content}</h3> : <h4 key={`heading-${index}`}>{content}</h4>);
      index++;
      continue;
    }
    const unordered = lines[index].match(/^\s*[-*]\s+(.+)$/);
    if (unordered) {
      const items: string[] = [];
      while (index < lines.length) {
        const match = lines[index].match(/^\s*[-*]\s+(.+)$/);
        if (!match) break;
        items.push(match[1]); index++;
      }
      blocks.push(<ul key={`list-${index}`}>{items.map((item, itemIndex) => <li key={itemIndex}><MarkdownInline text={item} /></li>)}</ul>);
      continue;
    }
    const ordered = lines[index].match(/^\s*\d+[.)]\s+(.+)$/);
    if (ordered) {
      const items: string[] = [];
      while (index < lines.length) {
        const match = lines[index].match(/^\s*\d+[.)]\s+(.+)$/);
        if (!match) break;
        items.push(match[1]); index++;
      }
      blocks.push(<ol key={`ordered-${index}`}>{items.map((item, itemIndex) => <li key={itemIndex}><MarkdownInline text={item} /></li>)}</ol>);
      continue;
    }
    const paragraph: string[] = [];
    while (index < lines.length && lines[index].trim() && !/^(#{1,4})\s+/.test(lines[index]) && !/^\s*(?:[-*]|\d+[.)])\s+/.test(lines[index]) && !lines[index].trimStart().startsWith("```")) paragraph.push(lines[index++].trim());
    blocks.push(<p key={`paragraph-${index}`}><MarkdownInline text={paragraph.join(" ")} /></p>);
  }
  return <div className="m22-markdown">{blocks}</div>;
}

function sessionItemCommand(item: DomainAgentSessionItem) {
  if (item.type !== "dynamicToolCall") return item.command;
  return ({
    crewfold_get_domain_context: "read domain context",
    crewfold_send_message: "send durable message",
    crewfold_create_durable_child: "create durable child",
    crewfold_delegate_staffing_grant: "delegate staffing grant",
    crewfold_propose_work: "submit work proposal",
    crewfold_propose_knowledge: "submit knowledge proposal",
  } as Record<string, string>)[item.command ?? ""] ?? item.command;
}

function formatActivityDuration(milliseconds?: number) {
  if (!milliseconds || milliseconds < 1) return "";
  if (milliseconds < 1000) return `${milliseconds}ms`;
  const seconds = Math.floor(milliseconds / 1000);
  if (seconds < 60) return `${seconds}s`;
  return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
}

function isActiveSessionItem(item: DomainAgentSessionItem) {
  return ["inProgress", "in_progress", "running"].includes(item.status ?? "");
}

function isExplorationCommand(item: DomainAgentSessionItem) {
  return item.type === "commandExecution" && Boolean(item.command_actions?.length) && item.command_actions!.every((action) => action.type !== "unknown");
}

function explorationActionText(action: DomainAgentSessionCommandAction) {
  if (action.type === "read") return `Read ${action.name || action.path || action.command || "file"}`;
  if (action.type === "listFiles") return `List ${action.path || action.command || "files"}`;
  if (action.type === "search") return `Search ${action.query || action.command || "repository"}${action.path ? ` in ${action.path}` : ""}`;
  return action.command || "Inspect repository";
}

function outputPreview(value: string) {
  const lines = value.trimEnd().split("\n");
  if (lines.length <= 9) return { text: lines.join("\n"), omitted: 0 };
  return { text: [...lines.slice(0, 5), ...lines.slice(-3)].join("\n"), omitted: lines.length - 8 };
}

function diffStats(change: DomainAgentSessionFileChange) {
  if (change.kind === "add" && change.diff) return { added: change.diff.split("\n").length, removed: 0 };
  if (change.kind === "delete" && change.diff) return { added: 0, removed: change.diff.split("\n").length };
  let added = 0; let removed = 0;
  for (const line of (change.diff ?? "").split("\n")) {
    if (line.startsWith("+") && !line.startsWith("+++")) added++;
    if (line.startsWith("-") && !line.startsWith("---")) removed++;
  }
  return { added, removed };
}

function DiffLines({ change }: { change: DomainAgentSessionFileChange }) {
  const lines = (change.diff ?? "").split("\n");
  return <pre className="m22-diff">{lines.map((line, index) => {
    const tone = change.kind === "add" || line.startsWith("+") && !line.startsWith("+++") ? "add" : change.kind === "delete" || line.startsWith("-") && !line.startsWith("---") ? "del" : line.startsWith("@@") ? "hunk" : "context";
    const content = change.kind === "update" && (tone === "add" || tone === "del") ? line.slice(1) : line;
    return <span className={tone} key={`${index}-${line.slice(0, 16)}`}><i>{index + 1}</i><b>{tone === "add" ? "+" : tone === "del" ? "-" : " "}</b>{content}{"\n"}</span>;
  })}</pre>;
}

function SessionExplorationGroup({ items }: { items: DomainAgentSessionItem[] }) {
  const active = items.some(isActiveSessionItem);
  const actions = items.flatMap((item) => item.command_actions ?? []);
  return <article className="m22-activity-group exploration">
    <header><strong>{active ? "Exploring" : "Explored"}</strong>{active && <LoaderCircle className="spin" size={13} />}</header>
    <ul>{actions.map((action, index) => <li key={`${index}-${action.type}-${action.name ?? action.path ?? action.query ?? action.command}`}>{explorationActionText(action)}</li>)}</ul>
  </article>;
}

function SessionThreadItem({ item, agentName }: { item: DomainAgentSessionItem; agentName: string }) {
  const command = sessionItemCommand(item);
  if (item.type === "reasoning" && !item.text?.trim() && !command) return null;
  if (item.type === "commandExecution") {
    const active = isActiveSessionItem(item);
    const failed = item.status === "failed" || item.exit_code !== undefined && item.exit_code !== 0;
    const preview = item.text ? outputPreview(item.text) : null;
    return <article className={`m22-activity-group command ${failed ? "failed" : ""}`}>
      <header><strong>{active ? item.process_id ? "Running background terminal" : "Running" : failed ? "Command failed" : "Ran"}</strong>{active && <LoaderCircle className="spin" size={13} />}{item.duration_ms ? <small>{formatActivityDuration(item.duration_ms)}</small> : null}</header>
      <code>{command}</code>
      {item.cwd && <p className="m22-activity-meta">in {item.cwd}</p>}
      {preview && <pre className="m22-output-preview">{preview.text}</pre>}
      {preview && (preview.omitted > 0 || item.text!.split("\n").length > 9) && <details><summary>+{preview.omitted} lines · view full command transcript</summary><pre>{item.text}</pre></details>}
      {failed && item.exit_code !== undefined && <p className="m22-command-result">exit {item.exit_code}</p>}
    </article>;
  }
  if (item.type === "fileChange") {
    const changes = item.changes ?? [];
    const stats = changes.reduce((total, change) => { const next = diffStats(change); return { added: total.added + next.added, removed: total.removed + next.removed }; }, { added: 0, removed: 0 });
    return <article className="m22-activity-group files">
      <header><strong>{isActiveSessionItem(item) ? "Editing" : `Edited ${changes.length} ${changes.length === 1 ? "file" : "files"}`}</strong>{isActiveSessionItem(item) && <LoaderCircle className="spin" size={13} />}<small><em>+{stats.added}</em> <del>-{stats.removed}</del></small></header>
      {changes.map((change) => { const changeStats = diffStats(change); return <details className="m22-file-change" key={`${change.kind}-${change.path}`}><summary><span>{change.path}</span><small><em>+{changeStats.added}</em> <del>-{changeStats.removed}</del></small></summary>{change.diff ? <DiffLines change={change} /> : <p>Patch content has not arrived yet.</p>}</details>; })}
    </article>;
  }
  return <article className={`m22-thread-item ${item.type} ${item.origin ?? ""} ${item.status === "failed" ? "failed" : ""}`}>
    <span>{sessionItemLabel(item, agentName)}</span>
    {command && <code>{command}</code>}
    {item.text && (item.type === "agentMessage" || item.type === "plan" ? <MarkdownText text={item.text} /> : <p>{item.text}</p>)}
    {(item.status || item.duration_ms) && <small>{[item.status?.replaceAll("_", " "), formatActivityDuration(item.duration_ms)].filter(Boolean).join(" · ")}</small>}
  </article>;
}

function SessionTurnItems({ items, agentName }: { items: DomainAgentSessionItem[]; agentName: string }) {
  const rendered: ReactNode[] = [];
  for (let index = 0; index < items.length;) {
    if (isExplorationCommand(items[index])) {
      const group: DomainAgentSessionItem[] = [];
      while (index < items.length && isExplorationCommand(items[index])) group.push(items[index++]);
      rendered.push(<SessionExplorationGroup items={group} key={`exploration-${group[0].id}`} />);
      continue;
    }
    const item = items[index++];
    rendered.push(<SessionThreadItem item={item} agentName={agentName} key={item.id} />);
  }
  return <>{rendered}</>;
}

function ActiveTurnProgress({ turn }: { turn: DomainAgentSessionTurn }) {
  const [startedAt] = useState(() => Date.now());
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => { const timer = window.setInterval(() => setNow(Date.now()), 1000); return () => window.clearInterval(timer); }, []);
  const activeItem = [...turn.items].reverse().find(isActiveSessionItem);
  const elapsed = formatActivityDuration(now - startedAt);
  const label = activeItem?.type === "commandExecution" ? isExplorationCommand(activeItem) ? "Exploring repository" : activeItem.process_id ? "Waiting for background terminal" : "Running command" : activeItem?.type === "fileChange" ? "Applying file changes" : activeItem?.type === "mcpToolCall" || activeItem?.type === "dynamicToolCall" ? "Waiting for tool" : "Working";
  const detail = activeItem?.type === "commandExecution" ? activeItem.command : activeItem?.command;
  return <div className="m22-turn-progress"><LoaderCircle className="spin" size={14} /><strong>{label}</strong><span>{elapsed}</span>{detail && <code>{detail}</code>}</div>;
}

type AgentCanonicalActivityItem = {
  id: string;
  occurredAt: string;
  kind: "task" | "process" | "message";
  title: string;
  detail: string;
  state: string;
  run?: Run;
  providerLogs?: string;
};

type AgentAttachedActivity = {
  runs: Record<string, { detail?: RunDetailView; logs?: string; error?: string }>;
  services: Record<string, ManagedProcessDetail>;
  threads: Record<string, ThreadDetail>;
};

const emptyAgentAttachedActivity: AgentAttachedActivity = { runs: {}, services: {}, threads: {} };

function runTimelineState(kind: string, detail: RunDetailView) {
  if (kind === "task.changes_requested") return detail.assessment === "block" ? "review blocked delivery" : "changes requested";
  if (kind === "task.handoff_recorded") return "handoff recorded";
  if (kind === "run.completion_proposed") return "awaiting review";
  if (kind === "run.progress_reported") return "progress";
  if (kind === "run.start_failed") return "start failed";
  if (kind === "run.stop_requested") return "stopping";
  return kind.split(".").at(-1)?.replaceAll("_", " ") || detail.run.status.replaceAll("_", " ");
}

function AgentCanonicalActivity({ data, agent, runs, attached, inspectRun }: { data: WorkbenchData; agent: DomainAgent; runs: Run[]; attached: AgentAttachedActivity; inspectRun: (run: Run) => void }) {
  const items: AgentCanonicalActivityItem[] = [];
  for (const run of runs) {
    const task = data.tasks.find((detail) => detail.task.id === run.task_id)?.task;
    const activity = attached.runs[run.id];
    const timeline = activity?.detail?.timeline ?? [];
    if (timeline.length) {
      timeline.forEach((entry, index) => items.push({
        id: `run-${run.id}-${entry.sequence}`,
        occurredAt: entry.recorded_at,
        kind: "task",
        title: task?.title ?? activity?.detail?.task.title ?? "Bounded assigned work",
        detail: [entry.kind.replaceAll("_", " "), entry.message, entry.evidence.length ? `${entry.evidence.length} evidence reference${entry.evidence.length === 1 ? "" : "s"}` : ""].filter(Boolean).join(" · "),
        state: runTimelineState(entry.kind, activity.detail!),
        run,
        ...(index === timeline.length - 1 && activity.logs ? { providerLogs: activity.logs } : {}),
      }));
      continue;
    }
    const outcome = runOutcome(run, task);
    items.push({ id: `run-${run.id}`, occurredAt: run.updated_at, kind: "task", title: task?.title ?? "Bounded assigned work", detail: [task?.task_class, run.blocked_question || run.failure_message || run.result_summary || activity?.error || `${run.provider} through ${run.runtime}`].filter(Boolean).join(" · "), state: outcome.label, run, providerLogs: activity?.logs });
  }
  for (const instance of data.processInstances.filter((candidate) => candidate.source?.agent_id === agent.definition.id)) {
    const definition = data.processDefinitions.find((candidate) => candidate.id === instance.definition_id);
    const detail = attached.services[instance.id];
    for (const job of detail?.jobs ?? []) items.push({ id: `process-${instance.id}-${job.id}`, occurredAt: job.updated_at, kind: "process", title: definition?.name ?? detail.definition.name, detail: [job.action, job.diagnostic, `attempt ${job.attempts}`].filter(Boolean).join(" · "), state: job.status });
    items.push({ id: `process-${instance.id}`, occurredAt: instance.updated_at, kind: "process", title: definition?.name ?? "Managed local process", detail: instance.diagnostic || `${definition ? managedProcessCommand(definition) : "Exact process definition"} · health ${instance.health_status}`, state: instance.status });
  }
  for (const thread of data.threads.filter((candidate) => candidate.agent_ids.includes(agent.definition.id))) {
    const detail = attached.threads[thread.thread.id];
    if (detail?.messages.length) {
      for (const message of detail.messages) items.push({ id: `thread-${thread.thread.id}-${message.id}`, occurredAt: message.created_at, kind: "message", title: thread.thread.subject, detail: `${message.sender_agent_name || message.sender_type}: ${message.body}`, state: message.kind.replaceAll("_", " ") });
    } else items.push({ id: `thread-${thread.thread.id}`, occurredAt: thread.thread.updated_at, kind: "message", title: thread.thread.subject, detail: `${thread.message_count} durable message${thread.message_count === 1 ? "" : "s"} · ${thread.thread.status}`, state: thread.thread.status });
  }
  items.sort((left, right) => left.occurredAt.localeCompare(right.occurredAt) || left.id.localeCompare(right.id));
  const latest = items.at(-1);
  const latestCopy = latest?.run ? <button type="button" onClick={() => inspectRun(latest.run!)}><strong>{latest.title}</strong><small>{latest.detail}</small></button> : latest ? <span className="m24-agent-record-copy"><strong>{latest.title}</strong><small>{latest.detail}</small></span> : null;
  return <section className="m24-agent-records" aria-label="Exact Crewfold records for this durable agent">
    <header><div><strong>Current assignment and status</strong><small>the exact Crewfold authority attached to this same Codex session</small></div><span>{items.length ? `${items.length} record${items.length === 1 ? "" : "s"}` : "none yet"}</span></header>
    {latest ? <div className={`m24-agent-latest ${statusTone(latest.state)}`}>
      <time dateTime={latest.occurredAt}>{displayTime(latest.occurredAt)}</time>
      {latestCopy}
      <StatusPill value={latest.state} tone={latest.state} />
    </div> : <p className="m22-empty">No task attempt, managed process, or durable coordination record exists yet.</p>}
    {items.length > 0 && <details className="m24-agent-history">
      <summary><span>Show lifecycle receipts</span><small>{items.length} canonical record{items.length === 1 ? "" : "s"}</small></summary>
      <p>These are the durable task, process, message, and outcome receipts produced around the Codex turns below. They are audit history for this agent—not separate sessions.</p>
      <ol>{items.map((item) => <li className={statusTone(item.state)} key={item.id}>
        <time dateTime={item.occurredAt}>{displayTime(item.occurredAt)}</time>
        <span className="m24-agent-record-kind">{item.kind}</span>
        {item.run ? <button type="button" onClick={() => inspectRun(item.run!)}><strong>{item.title}</strong><small>{item.detail}</small></button> : <span className="m24-agent-record-copy"><strong>{item.title}</strong><small>{item.detail}</small></span>}
        <StatusPill value={item.state} tone={item.state} />
        {item.providerLogs && <details className="m24-attached-provider-activity"><summary>captured provider activity for this attempt</summary><RuntimeActivityFeed logs={item.providerLogs} empty="No readable provider event was retained for this attempt." limit={0} /></details>}
      </li>)}</ol>
    </details>}
  </section>;
}

function DurableAgentSession({ data, agent, runs, inspectRun, apiBase, csrf }: { data: WorkbenchData; agent: DomainAgent; runs: Run[]; inspectRun: (run: Run) => void; apiBase: string; csrf: string }) {
	const workstream = data.objectives.find((objective) => objective.id === agent.membership.workstream_id);
	const defaultCheckout = workstream?.primary_checkout_id ?? (data.checkouts.length === 1 ? data.checkouts[0].id : "");
	const [result, setResult] = useState<DomainAgentSessionResult | null>(null);
	const [selectedEpoch, setSelectedEpoch] = useState(0);
	const [checkout, setCheckout] = useState(defaultCheckout);
  const [attachedActivity, setAttachedActivity] = useState<AgentAttachedActivity>(emptyAgentAttachedActivity);
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState<"loading" | "opening" | "sending" | "interrupting" | "compacting" | "rotating" | "">("loading");
  const [error, setError] = useState("");
  const composerRef = useRef<HTMLTextAreaElement>(null);
  const threadRef = useRef<HTMLDivElement>(null);
  const followThreadRef = useRef(true);
  const scope = { workspace: data.workspace?.id ?? "", project: data.project?.id ?? "", agent: agent.definition.id };
  const attachedSignature = `${runs.map((run) => `${run.id}:${run.revision}`).join(",")}|${data.processInstances.filter((instance) => instance.source?.agent_id === agent.definition.id).map((instance) => `${instance.id}:${instance.revision}`).join(",")}|${data.threads.filter((thread) => thread.agent_ids.includes(agent.definition.id)).map((thread) => `${thread.thread.id}:${thread.thread.revision}:${thread.message_count}`).join(",")}`;
  const load = useCallback(async (quiet = false, epoch = selectedEpoch) => {
    if (!scope.workspace || !scope.project) return;
    if (!quiet) setBusy("loading");
    try {
      const next = await rpc<DomainAgentSessionResult>(apiBase, csrf, "domain.agent.session.show", epoch > 0 ? { ...scope, epoch } : scope);
      setResult(next); setError("");
    } catch (reason) {
      if (!quiet) setError(reason instanceof Error ? reason.message : "Could not read the durable Codex session.");
    } finally { if (!quiet) setBusy(""); }
  }, [apiBase, csrf, scope.agent, scope.project, scope.workspace, selectedEpoch]);
  const accepted = result?.accepted_turn;
  const providerTurns = result?.view.turns ?? [];
  const turns = result ? [...providerTurns, ...(accepted && !providerTurns.some((turn) => turn.id === accepted.id) ? [accepted] : [])].map((turn) => ({ ...turn, items: turn.items ?? [] })) : [];
  const activeTurn = [...turns].reverse().find((turn) => ["inProgress", "in_progress"].includes(turn.status));
  const activityKey = `${turns.map((turn) => `${turn.id}:${turn.status}:${turn.items.map((item) => `${item.id}:${item.status ?? ""}:${item.text?.length ?? 0}:${item.duration_ms ?? 0}:${item.changes?.reduce((size, change) => size + (change.diff?.length ?? 0), 0) ?? 0}`).join(",")}`).join("|")}|${runs.map((run) => `${run.id}:${run.revision}`).join(",")}|${data.processInstances.filter((instance) => instance.source?.agent_id === agent.definition.id).map((instance) => `${instance.id}:${instance.revision}`).join(",")}`;
  useEffect(() => {
    if (!scope.workspace) return;
    let current = true;
    let loading = false;
    const refresh = async () => {
      if (loading) return;
      loading = true;
      const next: AgentAttachedActivity = { runs: {}, services: {}, threads: {} };
      await Promise.all(runs.map(async (run) => {
        try {
          const shown = await rpc<{ detail: RunDetailView }>(apiBase, csrf, "run.show", { workspace: scope.workspace, run: run.id });
          next.runs[run.id] = { detail: shown.detail };
        } catch (reason) {
          next.runs[run.id] = { error: reason instanceof Error ? reason.message : "Run detail is unavailable." };
        }
        try {
          const result = await rpc<{ logs: { stdout: { text: string; truncated: boolean; omitted_bytes: number }; stderr: { text: string; truncated: boolean; omitted_bytes: number } } }>(apiBase, csrf, "run.logs", { workspace: scope.workspace, run: run.id, tail: 10000 });
          next.runs[run.id] = { ...next.runs[run.id], logs: [result.logs.stdout.text, result.logs.stderr.text, result.logs.stdout.truncated || result.logs.stderr.truncated ? "[bounded log output; earlier bytes omitted]" : ""].filter(Boolean).join("\n") };
        } catch {
          // Lost or not-yet-bound runs can have exact canonical state without trustworthy logs.
        }
      }));
      await Promise.all(data.processInstances.filter((instance) => instance.source?.agent_id === agent.definition.id).map(async (instance) => {
        try { next.services[instance.id] = (await rpc<{ detail: ManagedProcessDetail }>(apiBase, csrf, "managed_service.show", { workspace: scope.workspace, instance: instance.id })).detail; } catch { /* summary remains visible */ }
      }));
      await Promise.all(data.threads.filter((thread) => thread.agent_ids.includes(agent.definition.id)).map(async (thread) => {
        try { next.threads[thread.thread.id] = (await rpc<{ detail: ThreadDetail }>(apiBase, csrf, "thread.show", { workspace: scope.workspace, thread: thread.thread.id })).detail; } catch { /* summary remains visible */ }
      }));
      if (current) setAttachedActivity(next);
      loading = false;
    };
    setAttachedActivity(emptyAgentAttachedActivity);
    void refresh();
    const live = runs.some((run) => ["requested", "starting", "active", "blocked", "stopping"].includes(run.status)) || data.processInstances.some((instance) => instance.source?.agent_id === agent.definition.id && ["requested", "starting", "healthy", "degraded", "stopping"].includes(instance.status));
    const timer = live ? window.setInterval(() => void refresh(), 1500) : undefined;
    return () => { current = false; if (timer !== undefined) window.clearInterval(timer); };
  }, [apiBase, csrf, scope.workspace, scope.agent, attachedSignature]);
  useEffect(() => {
    setResult(null); setSelectedEpoch(0); setInput(""); setError("");
		setCheckout(defaultCheckout);
		void load(false, 0);
	}, [agent.definition.id, data.project?.id, defaultCheckout]);
  useEffect(() => {
    // The selected agent is a live console. Its accepted task may be bound
    // after the last domain snapshot, so an idle or even unbound view cannot
    // be used as a reason to stop reading it. Archived epochs are immutable;
    // the current epoch follows its provider thread continuously.
    if (!result || selectedEpoch > 0 || ["archived", "detached"].includes(result.view.session.state)) return;
    const timer = window.setInterval(() => void load(true), 900);
    return () => window.clearInterval(timer);
  }, [load, result?.view.session.state, selectedEpoch]);
  useLayoutEffect(() => {
    const composer = composerRef.current;
    if (!composer) return;
    composer.style.height = "auto";
    composer.style.height = `${Math.min(composer.scrollHeight, 160)}px`;
  }, [input]);
  useLayoutEffect(() => {
    const thread = threadRef.current;
    if (!thread || !followThreadRef.current) return;
    thread.scrollTop = thread.scrollHeight;
  }, [activityKey]);
  const open = async () => {
    setBusy("opening"); setError("");
    try {
      setResult(await rpc<DomainAgentSessionResult>(apiBase, csrf, "domain.agent.session.open", { ...scope, checkout, idempotency_key: newKey("domain-session-open") }));
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Could not start the durable Codex session."); }
    finally { setBusy(""); }
  };
  const send = async () => {
    const text = input.trim();
    if (!text) return;
    setBusy("sending"); setError("");
    try {
      setResult(await rpc<DomainAgentSessionResult>(apiBase, csrf, "domain.agent.session.send", { ...scope, text, idempotency_key: newKey("domain-session-turn") }));
      setInput("");
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Could not send the owner message."); }
    finally { setBusy(""); }
  };
  const interrupt = async () => {
    if (!activeTurn) return;
    setBusy("interrupting"); setError("");
    try {
      setResult(await rpc<DomainAgentSessionResult>(apiBase, csrf, "domain.agent.session.interrupt", { ...scope, turn_id: activeTurn.id }));
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Could not interrupt the current Codex turn."); }
    finally { setBusy(""); }
  };
  const compact = async () => {
    if (!result || activeTurn) return;
    setBusy("compacting"); setError("");
    try {
      setResult(await rpc<DomainAgentSessionResult>(apiBase, csrf, "domain.agent.session.compact", { ...scope, expected_epoch: result.view.session.epoch })); setSelectedEpoch(0);
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Could not compact and recycle the Codex session."); }
    finally { setBusy(""); }
  };
  const rotate = async () => {
    if (!result || activeTurn) return;
    setBusy("rotating"); setError("");
    try {
      setResult(await rpc<DomainAgentSessionResult>(apiBase, csrf, "domain.agent.session.rotate", { ...scope, expected_epoch: result.view.session.epoch, reason: "owner_requested" })); setSelectedEpoch(0);
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Could not hand off to a fresh Codex epoch."); }
    finally { setBusy(""); }
  };
  const showEpoch = async (epoch: number) => {
    setBusy("loading"); setError("");
    try { setResult(await rpc<DomainAgentSessionResult>(apiBase, csrf, "domain.agent.session.show", epoch > 0 ? { ...scope, epoch } : scope)); setSelectedEpoch(epoch); }
    catch (reason) { setError(reason instanceof Error ? reason.message : "Could not read this durable Codex epoch."); }
    finally { setBusy(""); }
  };
  const state = result?.view.session.state ?? (busy === "loading" ? "loading" : "unavailable");
  if (state === "unbound") return <div className="m22-session m22-session-empty">
    <div className="m22-session-state"><span>Codex conversation</span><StatusPill value="not started" /></div>
		<AgentCanonicalActivity data={data} agent={agent} runs={runs} attached={attachedActivity} inspectRun={inspectRun} />
		<div className="m22-thread m24-agent-timeline"><div className="m24-provider-boundary"><strong>Conversation</strong><span>not started</span></div><p className="m22-empty">No provider turn exists yet.</p></div>
		<h2>Start this durable agent’s provider thread</h2>
		<p>This starts the real, resumable Codex conversation for <strong>{agent.definition.name}</strong>. Crewfold activity above already belongs to this same durable coworker; a provider thread is its conversational memory, not a second identity.</p>
		{workstream ? <p className="m22-session-bound-checkout"><strong>Workstream checkout:</strong> {data.checkouts.find((item) => item.id === workstream.primary_checkout_id)?.path ?? workstream.primary_checkout_id}</p> : data.checkouts.length > 1 && <label className="m22-session-checkout"><span>domain checkout for this conversation</span><select value={checkout} onChange={(event) => setCheckout(event.target.value)}><option value="">select exact checkout</option>{data.checkouts.filter((item) => item.availability === "available").map((item) => <option key={item.id} value={item.id}>{item.path}</option>)}</select></label>}
    <button className="m22-command" disabled={busy !== "" || !checkout} onClick={() => void open()}>{busy === "opening" ? <LoaderCircle className="spin" size={15} /> : <Play size={15} />} start Codex session</button>
    {error && <p className="m22-session-error">{error}</p>}
  </div>;
  const unresolvedExecution = runs.find((run) => ["requested", "starting", "active", "blocked", "stopping", "lost"].includes(run.status));
  const archived = state === "archived";
  return <div className="m22-session">
    <div className="m22-session-state"><span>Codex conversation · epoch {result?.view.session.epoch || 1} · {result?.view.session.provider || agent.definition.provider}</span><StatusPill value={result?.view.thread_status ?? state} /></div>
    {busy === "loading" && !result ? <p className="m22-session-loading"><LoaderCircle className="spin" size={14} /> reading persisted provider thread…</p> : state === "detached" ? <p className="m22-session-error">This session belongs to another Crewfold node. It is visible as detached and cannot be controlled here.</p> : <>
      {!archived && <AgentCanonicalActivity data={data} agent={agent} runs={runs} attached={attachedActivity} inspectRun={inspectRun} />}
      <div className="m22-thread m24-agent-timeline" aria-live="polite" ref={threadRef} onScroll={(event) => {
        const thread = event.currentTarget;
        followThreadRef.current = thread.scrollHeight - thread.scrollTop - thread.clientHeight < 48;
      }}>
        <div className="m24-provider-boundary"><strong>Codex session</strong><span>epoch {result?.view.session.epoch || 1} · owner and accepted task turns</span></div>
        {turns.length === 0 ? <p className="m22-empty">The provider thread is ready. Send the first owner message below.</p> : turns.map((turn) => <section className="m22-turn" key={turn.id}>
          <SessionTurnItems items={turn.items} agentName={agent.definition.name} />
          {["inProgress", "in_progress"].includes(turn.status) && <ActiveTurnProgress turn={turn} />}
          {!(["inProgress", "in_progress"].includes(turn.status)) && <footer>{turn.status.replaceAll("_", " ")}</footer>}
        </section>)}
      </div>
      {archived ? <div className="m22-archive-banner"><span>This epoch is immutable history. It cannot receive owner input or Crewfold tool authority.</span><button onClick={() => void showEpoch(0)}>return to current epoch</button></div> : <div className="m22-composer">
        <div className="m22-composer-line">
          <textarea ref={composerRef} rows={1} value={input} onChange={(event) => setInput(event.target.value)} placeholder={`Message ${agent.definition.name} directly…`} maxLength={65536} onKeyDown={(event) => {
            if (event.key === "Enter" && !event.shiftKey && !event.nativeEvent.isComposing) { event.preventDefault(); void send(); }
          }} />
          <span>{activeTurn && <button disabled={busy !== ""} onClick={() => void interrupt()}><Square size={13} /> interrupt</button>}<button className="m22-send" disabled={busy !== "" || !input.trim()} onClick={() => void send()}>{busy === "sending" ? <LoaderCircle className="spin" size={14} /> : <Send size={14} />} {activeTurn ? "steer" : "send"}</button></span>
        </div>
        <small>Enter to send · Shift+Enter for a new line · conversation text is not authority</small>
      </div>}
      <details className="m22-session-lifecycle">
        <summary><RotateCcw size={13} /> session memory and handoff <span>{result?.view.epochs.length ?? 1} epoch{(result?.view.epochs.length ?? 1) === 1 ? "" : "s"}</span></summary>
        <p>The durable agent, assignments, receipts, and hierarchy survive provider cleanup. Compaction keeps this epoch and restarts only its disposable Codex host. A handoff archives it and starts a fresh epoch from bounded canonical continuity.</p>
        {unresolvedExecution && <p className="m22-session-lifecycle-warning">Resolve the attached {unresolvedExecution.status} execution before compacting or handing off; Crewfold will not strand uncertain task authority.</p>}
        <div className="m22-session-epochs">{(result?.view.epochs ?? []).map((epoch) => <button className={(selectedEpoch || result?.view.session.epoch) === epoch.epoch ? "selected" : ""} key={epoch.epoch} disabled={busy !== ""} onClick={() => void showEpoch(epoch.status === "current" ? 0 : epoch.epoch)}><strong>epoch {epoch.epoch}</strong> {epoch.status}{epoch.rotation_reason ? ` · ${epoch.rotation_reason.replaceAll("_", " ")}` : ""}</button>)}</div>
        {!archived && <div className="m22-session-lifecycle-actions"><button disabled={busy !== "" || Boolean(activeTurn) || Boolean(unresolvedExecution)} onClick={() => void compact()}>{busy === "compacting" ? <LoaderCircle className="spin" size={13} /> : <RotateCcw size={13} />} compact and recycle host</button><button disabled={busy !== "" || Boolean(activeTurn) || Boolean(unresolvedExecution)} onClick={() => void rotate()}>{busy === "rotating" ? <LoaderCircle className="spin" size={13} /> : <Archive size={13} />} hand off to fresh epoch</button></div>}
      </details>
    </>}
    {error && <p className="m22-session-error">{error}</p>}
  </div>;
}

function DomainAgentCenter({ data, agent, view, setView, inspectRun, apiBase, csrf, mutable, detailsOpen, toggleDetails }: { data: WorkbenchData; agent: DomainAgent; view: DomainConsoleView; setView: (view: DomainConsoleView) => void; inspectRun: (run: Run) => void; apiBase: string; csrf: string; mutable: boolean; detailsOpen: boolean; toggleDetails: () => void }) {
  const definition = agent.definition;
  const assigned = data.tasks.filter((detail) => detail.task.assigned_agent_id === definition.id);
  const runs = data.runs.filter((run) => run.agent_id === definition.id).sort((left, right) => right.updated_at.localeCompare(left.updated_at));
  const workstream = data.objectives.find((objective) => objective.id === agent.membership.workstream_id);
  const consequentialRun = runs.find((run) => ["lost", "blocked", "stopping", "active", "starting", "requested"].includes(run.status)) ?? runs[0];
  const consequentialTask = consequentialRun ? data.tasks.find((detail) => detail.task.id === consequentialRun.task_id)?.task : undefined;
  const consequentialOutcome = consequentialRun ? runOutcome(consequentialRun, consequentialTask) : null;
  const checkout = workstream ? data.checkouts.find((candidate) => candidate.id === workstream.primary_checkout_id) : undefined;
  const changedPaths = data.checkouts.flatMap((candidate) => candidate.dirty_paths ?? []);
  const ownedChecks = data.checks.filter((check) => assigned.some((detail) => detail.task.id === check.run.task_id));
  const agentThreads = data.threads.filter((thread) => thread.agent_ids.includes(definition.id));
  const tabs: Array<[DomainConsoleView, string]> = [["session", "session"]];
  if (assigned.length > 0) tabs.push(["assignment", "assignment"]);
  if (changedPaths.length > 0) tabs.push(["changes", "changes"]);
  tabs.push(["briefing", "briefing"]);
  if (ownedChecks.length > 0) tabs.push(["verification", "verification"]);
  tabs.push(["staffing", "staffing"]);
  useEffect(() => {
    if (!tabs.some(([id]) => id === view)) setView("session");
  }, [view, setView, assigned.length, changedPaths.length, ownedChecks.length]);
  return <section className="m22-agent-center">
    <header><button className={`m22-detail-toggle ${detailsOpen ? "active" : ""}`} type="button" aria-expanded={detailsOpen} onClick={toggleDetails}>{detailsOpen ? "hide details" : "agent details"}</button><p className="m22-kicker">durable agent</p><div className="m22-agent-title"><h1>{definition.name}</h1><StatusPill value={consequentialOutcome?.label ?? "idle"} tone={consequentialOutcome?.tone ?? "idle"} /></div><p>{definition.role || "No descriptive role"} · {definition.provider} through {definition.runtime}{workstream ? ` · ${workstream.title}` : ""}</p>{workstream && <small className="m22-agent-checkout">task work uses {checkout?.path ?? "an unavailable primary checkout"}</small>}</header>
    <nav className="m22-tabs" aria-label="Selected agent views">{tabs.map(([id, label]) => <button className={view === id ? "active" : ""} key={id} onClick={() => setView(id)}>{label}</button>)}</nav>
    {view === "session" && <><DurableAgentSession data={data} agent={agent} runs={runs} inspectRun={inspectRun} apiBase={apiBase} csrf={csrf} />{agentThreads.length > 0 && <CoordinationThreads data={data} apiBase={apiBase} csrf={csrf} threads={agentThreads} heading={`${definition.name} coordination`} />}</>}
    {view === "assignment" && <div className="m22-block"><h2>assigned work</h2>{assigned.length ? assigned.map((detail) => <div className="m22-line static" key={detail.task.id}><span><strong>{detail.task.title}</strong><small>{detail.task.description || "No additional description"}</small></span><StatusPill value={detail.task.status} /></div>) : <><p className="m22-empty">No canonical task is assigned to this agent.</p><p className="m22-caveat">Conversation and coordination messages are not assignments. This view fills only after a task is created and explicitly assigned.</p></>}</div>}
    {view === "changes" && <div className="m22-block"><h2>observed checkout changes</h2>{data.checkouts.flatMap((candidate) => (candidate.dirty_paths ?? []).map((path) => <code className="m22-path" key={`${candidate.id}-${path}`}>{path}</code>))}<p className="m22-caveat">These are project checkout observations, not an inferred per-agent diff.</p></div>}
    {view === "briefing" && <div className="m22-block"><h2>scope and available context</h2><dl className="m22-facts"><div><dt>domain</dt><dd>{data.project?.name}</dd></div><div><dt>workstream</dt><dd>{workstream?.title ?? "domain-wide"}</dd></div><div><dt>parent</dt><dd>{data.domainAgents.find((candidate) => candidate.definition.id === agent.membership.parent_agent_id)?.definition.name ?? "domain root"}</dd></div><div><dt>attached checkouts</dt><dd>{data.checkouts.length}</dd></div><div><dt>accepted knowledge</dt><dd>{data.knowledge.filter((revision) => revision.review_status === "accepted" && revision.currency_status === "current").length}</dd></div><div><dt>coordination threads</dt><dd>{data.threads.filter((thread) => thread.agent_ids.includes(definition.id)).length}</dd></div></dl><p className="m22-caveat">This is the durable domain scope available to the conversation. A task-specific frozen context packet exists only for an assigned execution run.</p></div>}
    {view === "verification" && <div className="m22-block"><h2>verification owned by assigned tasks</h2>{ownedChecks.map((check) => <div className="m22-line static" key={check.run.id}><span><strong>{assigned.find((detail) => detail.task.id === check.run.task_id)?.task.title}</strong><small>{check.requirement_state}</small></span><StatusPill value={check.run.status} /></div>)}</div>}
    {view === "staffing" && <StaffingPanel data={data} agent={agent} apiBase={apiBase} csrf={csrf} mutable={mutable} />}
  </section>;
}

function DomainConsole({ data, selectedAgentID, selectAgent, selectProject, inspectTask, inspectRun, apiBase, csrf, mutable, reload }: { data: WorkbenchData; selectedAgentID: string; selectAgent: (agent: DomainAgent | null) => void; selectProject: (id: string) => void; inspectTask: (task: TaskDetail) => void; inspectRun: (run: Run) => void; apiBase: string; csrf: string; mutable: boolean; reload: () => Promise<void> }) {
  const [view, setView] = useState<DomainConsoleView>("domain");
  const [creating, setCreating] = useState(false);
  const [creatingWorkstream, setCreatingWorkstream] = useState(false);
  const [retiringAgentID, setRetiringAgentID] = useState("");
  const [reviewingWorkstreamID, setReviewingWorkstreamID] = useState("");
  const [workstreamNotice, setWorkstreamNotice] = useState("");
  const [contextOpen, setContextOpen] = useState(false);
  const selected = data.domainAgents.find((agent) => agent.definition.id === selectedAgentID) ?? null;
  const retiringAgent = data.domainAgents.find((agent) => agent.definition.id === retiringAgentID) ?? null;
  const reviewingWorkstream = data.objectives.find((objective) => objective.id === reviewingWorkstreamID) ?? null;
  useEffect(() => { setView(selected ? "session" : "domain"); setCreating(false); setCreatingWorkstream(false); setRetiringAgentID(""); setReviewingWorkstreamID(""); setContextOpen(false); }, [selected?.definition.id, data.project?.id]);
  useEffect(() => { setWorkstreamNotice(""); setReviewingWorkstreamID(""); setRetiringAgentID(""); }, [data.project?.id]);
  return <div className={`m22-console ${contextOpen && selected ? "context-open" : ""}`}>
    <aside className="m22-domain-rail">
      <p className="m22-rail-label">domains</p>
      {data.projects.map((project) => <button key={project.id} className={`m22-domain-row ${project.id === data.project?.id && !selected ? "selected" : ""}`} onClick={() => { if (project.id !== data.project?.id) selectProject(project.id); selectAgent(null); }}><Boxes size={14} /><span><strong>{project.name}</strong><small>{project.id === data.project?.id ? `${data.domainAgents.filter((agent) => agent.membership.status === "active").length} durable agents` : "select domain"}</small></span></button>)}
      {data.project && <><p className="m22-rail-label agents">agent hierarchy</p><DomainAgentTreeList agents={data.domainAgents} objectives={data.objectives.filter((objective) => objective.project_id === data.project?.id)} selected={selectedAgentID} choose={selectAgent} /></>}
      <div className="m22-rail-spacer" />
      {data.project && <button className="m22-add-agent" disabled={!mutable} onClick={() => { setCreating(false); setRetiringAgentID(""); setReviewingWorkstreamID(""); setWorkstreamNotice(""); setCreatingWorkstream(true); }}><Plus size={13} /> new workstream</button>}
      {data.project && <button className="m22-add-agent" disabled={!mutable} onClick={() => { setCreatingWorkstream(false); setRetiringAgentID(""); setReviewingWorkstreamID(""); setWorkstreamNotice(""); setCreating(true); }}><Plus size={13} /> add durable agent</button>}
      <div className="m22-cut">canonical through event {data.highWater}</div>
    </aside>
    <main className="m22-center">{retiringAgent ? <DomainAgentRetirementPanel data={data} agent={retiringAgent} apiBase={apiBase} csrf={csrf} mutable={mutable} close={() => setRetiringAgentID("")} retired={() => { setRetiringAgentID(""); selectAgent(null); setWorkstreamNotice(`Agent “${retiringAgent.definition.name}” was retired. Its canonical history remains under retired and closed history.`); }} reload={reload} /> : reviewingWorkstream ? <DomainWorkstreamLifecyclePanel data={data} objective={reviewingWorkstream} inspectTask={inspectTask} inspectRun={inspectRun} apiBase={apiBase} csrf={csrf} mutable={mutable} close={() => setReviewingWorkstreamID("")} cancelled={() => { setReviewingWorkstreamID(""); selectAgent(null); setWorkstreamNotice(`Workstream “${reviewingWorkstream.title}” was cancelled and moved to closed history.`); }} reload={reload} /> : creatingWorkstream ? <DomainWorkstreamCreatePanel data={data} apiBase={apiBase} csrf={csrf} mutable={mutable} close={() => setCreatingWorkstream(false)} created={(title) => { setCreatingWorkstream(false); setWorkstreamNotice(`Workstream “${title}” was created. It is empty until you place durable agents or work inside it.`); selectAgent(null); }} reload={reload} /> : creating ? <DomainAgentCreatePanel data={data} suggestedParent={selected?.definition.id ?? ""} apiBase={apiBase} csrf={csrf} mutable={mutable} close={() => setCreating(false)} created={(agent) => { setCreating(false); selectAgent(agent); }} reload={reload} /> : selected ? <DomainAgentCenter data={data} agent={selected} view={view} setView={setView} inspectRun={inspectRun} apiBase={apiBase} csrf={csrf} mutable={mutable} detailsOpen={contextOpen} toggleDetails={() => setContextOpen((value) => !value)} /> : <DomainHome data={data} chooseAgent={selectAgent} reviewWorkstream={(objective) => { setWorkstreamNotice(""); setReviewingWorkstreamID(objective.id); }} inspectTask={inspectTask} inspectRun={inspectRun} notice={workstreamNotice} apiBase={apiBase} csrf={csrf} mutable={mutable} reload={reload} />}</main>
    {contextOpen && <aside className="m22-context">
      <p className="m22-rail-label">selected {selected ? "agent" : "domain"}</p>
      <h2>{selected?.definition.name ?? data.project?.name}</h2>
      {selected ? <><dl className="m22-facts"><div><dt>role</dt><dd>{selected.definition.role}</dd></div><div><dt>mode</dt><dd>{selected.membership.delegation_policy.replaceAll("_", " ")}</dd></div><div><dt>status</dt><dd>{selected.membership.status}</dd></div><div><dt>revision</dt><dd>{selected.membership.revision}</dd></div></dl><details className="m22-context-disclosure"><summary>operating charter</summary><p>{selected.membership.operating_charter}</p></details><DomainAgentPlacementEditor data={data} agent={selected} apiBase={apiBase} csrf={csrf} mutable={mutable} reload={reload} /><details className="m22-context-disclosure"><summary>how authority works</summary><p>Tree placement and charter organize behavior only. Grants, assignments, claims, budgets, and capabilities authorize effects.</p></details>{selected.membership.status === "active" && <section className="m22-lifecycle-entry"><h3>lifecycle</h3><p>Retirement preserves this agent’s history and refuses while it retains active responsibility.</p><button className="m22-danger subtle" disabled={!mutable} onClick={() => { setCreating(false); setCreatingWorkstream(false); setReviewingWorkstreamID(""); setRetiringAgentID(selected.definition.id); }}><Archive size={13} /> review retirement</button></section>}</> : <><dl className="m22-facts"><div><dt>active workstreams</dt><dd>{data.objectives.filter((objective) => objective.status === "active").length}</dd></div><div><dt>active agents</dt><dd>{data.domainAgents.filter((agent) => agent.membership.status === "active").length}</dd></div><div><dt>checkouts</dt><dd>{data.checkouts.length}</dd></div><div><dt>open tasks</dt><dd>{data.tasks.filter((detail) => !["completed", "cancelled", "failed"].includes(detail.task.status)).length}</dd></div></dl><section><h3>domain boundary</h3><p>A domain coordinates related work and knowledge. It is not a repository or folder.</p></section></>}
    </aside>}
  </div>;
}

function LiveTerminal({ apiBase, csrf, workspace, run, initialLogs, close, mutable }: { apiBase: string; csrf: string; workspace: string; run: Run; initialLogs: string; close: () => void; mutable: boolean }) {
  const [state, setState] = useState("connecting");
  const [input, setInput] = useState("");
  const [streamRaw, setStreamRaw] = useState("");
  const [protocolOpen, setProtocolOpen] = useState(false);
  const [followOutput, setFollowOutput] = useState(true);
  const [unseenOutput, setUnseenOutput] = useState(false);
  const host = useRef<HTMLDivElement | null>(null);
  const socket = useRef<WebSocket | null>(null);
  const terminal = useRef<Terminal | null>(null);
  const fit = useRef<FitAddon | null>(null);
  const follow = useRef(true);
  const controlsEnabled = useRef(mutable);
  const decoder = useRef(new TextDecoder());
  const rawBuffer = useRef("");
  // The terminal websocket often begins with the same bounded tail returned by
  // run.logs. Prefer the live stream once it exists so the readable surface
  // never renders the same provider events twice.
  const readableLogs = useMemo(() => streamRaw || initialLogs, [initialLogs, streamRaw]);

  useEffect(() => { controlsEnabled.current = mutable; }, [mutable]);

  const sendInput = useCallback((value: string) => {
    if (!controlsEnabled.current || socket.current?.readyState !== WebSocket.OPEN || !value) return;
    socket.current.send(JSON.stringify({ type: "input", data: value }));
  }, []);
  const sendSize = useCallback(() => {
    if (socket.current?.readyState !== WebSocket.OPEN || !terminal.current) return;
    socket.current.send(JSON.stringify({ type: "resize", cols: terminal.current.cols, rows: terminal.current.rows }));
  }, []);

  useEffect(() => {
    if (!protocolOpen || !host.current) return;
    const nextTerminal = new Terminal({
      allowProposedApi: false,
      cursorBlink: true,
      cursorStyle: "bar",
      fontFamily: "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace",
      fontSize: 11,
      lineHeight: 1.2,
      minimumContrastRatio: 4.5,
      scrollback: 2000,
      scrollOnUserInput: true,
      theme: { background: "#070a0b", foreground: "#d5dcda", cursor: "#79d9d0", cursorAccent: "#070a0b", selectionBackground: "#28433f", black: "#070a0b", brightBlack: "#677472", red: "#f07d72", brightRed: "#ff9a91", green: "#a5c76b", brightGreen: "#c2e48a", yellow: "#d7b86e", brightYellow: "#efd08a", blue: "#79a8d9", brightBlue: "#9bc4ee", magenta: "#b99ad9", brightMagenta: "#d3b4ef", cyan: "#79d9d0", brightCyan: "#a0eee6", white: "#d5dcda", brightWhite: "#f4f7f6" },
    });
    const fitAddon = new FitAddon();
    nextTerminal.loadAddon(fitAddon);
    nextTerminal.open(host.current);
    terminal.current = nextTerminal;
    fit.current = fitAddon;
    const fitToPanel = () => {
      if (!host.current || host.current.clientWidth === 0 || host.current.clientHeight === 0) return;
      try { fitAddon.fit(); sendSize(); } catch { /* the inspector is closing */ }
    };
    fitToPanel();
    if (rawBuffer.current) nextTerminal.write(rawBuffer.current, () => nextTerminal.scrollToBottom());
    const resizeObserver = new ResizeObserver(fitToPanel);
    resizeObserver.observe(host.current);
    const inputSubscription = nextTerminal.onData(sendInput);
    const scrollSubscription = nextTerminal.onScroll((position) => {
      const atBottom = position >= nextTerminal.buffer.active.baseY;
      follow.current = atBottom;
      setFollowOutput(atBottom);
      if (atBottom) setUnseenOutput(false);
    });
    return () => {
      resizeObserver.disconnect();
      inputSubscription.dispose();
      scrollSubscription.dispose();
      terminal.current = null;
      fit.current = null;
      nextTerminal.dispose();
    };
  }, [protocolOpen, sendInput, sendSize]);

  useEffect(() => {
    let active = true;
    const connect = async () => {
      try {
        const response = await fetch(`${apiBase}/terminal-grant`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-Crewfold-CSRF": csrf }, body: JSON.stringify({ workspace, run: run.id }) });
        const grant = (await response.json()) as { websocket_path?: string; protocol?: string; error?: { message: string } };
        if (!response.ok || !grant.protocol || grant.websocket_path !== "terminal") throw new Error(grant.error?.message ?? "Terminal grant failed");
        const address = `${window.location.origin.replace(/^http/, "ws")}${apiBase}/${grant.websocket_path}`;
        const next = new WebSocket(address, grant.protocol);
        next.binaryType = "arraybuffer";
        socket.current = next;
        next.onopen = () => { if (!active) return; setState("connected"); try { fit.current?.fit(); } catch { /* panel closed */ } sendSize(); };
        next.onmessage = (event) => {
          if (!active) return;
          const raw = event.data instanceof ArrayBuffer ? decoder.current.decode(event.data, { stream: true }) : String(event.data);
          rawBuffer.current = `${rawBuffer.current}${raw}`.slice(-262144);
          setStreamRaw(rawBuffer.current);
          if (!follow.current) setUnseenOutput(true);
          terminal.current?.write(raw, () => { if (follow.current) terminal.current?.scrollToBottom(); });
        };
        next.onerror = () => { if (active) setState("unavailable"); };
        next.onclose = () => { if (active) setState("closed"); };
      } catch (reason) {
        setState(reason instanceof Error ? reason.message : "unavailable");
      }
    };
    void connect();
    return () => { active = false; socket.current?.close(1000, "inspector closed"); socket.current = null; decoder.current = new TextDecoder(); };
  }, [apiBase, csrf, run.id, sendSize, workspace]);

  const toggleFollow = () => {
    const next = !follow.current;
    follow.current = next;
    setFollowOutput(next);
    if (next) { terminal.current?.scrollToBottom(); setUnseenOutput(false); }
  };
  return <section className="live-terminal" aria-label="Live agent activity">
    <div className="section-title"><h3>Advanced live runtime</h3><span><CircleDot size={11} />{state}</span></div>
    <p>This is the current Herdr stream and optional raw terminal—not a second agent session. The durable agent’s readable conversation and task receipts remain in its main session.</p>
    {!mutable && <div className="terminal-paused">Canonical state is refreshing; output remains connected while controls are temporarily disabled.</div>}
    <div className="live-activity-scroll"><RuntimeActivityFeed logs={readableLogs} empty={state === "connected" ? "Connected. No readable provider event has been emitted yet." : "Connecting to the current Herdr run…"} /></div>
    <details className="protocol-console" onToggle={(event) => setProtocolOpen(event.currentTarget.open)}>
      <summary><span><TerminalSquare size={13} />Advanced protocol console</span><small>Exact PTY bytes and direct terminal input</small></summary>
      {protocolOpen && <div className="protocol-console-body">
        <div className="terminal-toolbar"><span>Diagnostic surface · raw Codex JSONL may be noisy</span><button type="button" className={followOutput ? "follow-active" : ""} onClick={toggleFollow}><ChevronDown size={13} />{unseenOutput ? "New output" : followOutput ? "Following" : "Follow output"}</button></div>
        <div ref={host} className="terminal-canvas" role="application" aria-label="Advanced raw Herdr protocol terminal" />
        <form onSubmit={(event) => { event.preventDefault(); sendInput(input + "\r"); setInput(""); }}><input aria-label="Raw terminal input" value={input} maxLength={4095} onChange={(event) => setInput(event.target.value)} placeholder="Send raw terminal input…" disabled={!mutable} /><button className="secondary-button" disabled={!mutable || state !== "connected" || !input}><Send size={14} />Send</button><button type="button" className="secondary-button" disabled={!mutable || state !== "connected"} onClick={() => sendInput("\u0003")}><AlertCircle size={14} />Ctrl-C</button></form>
      </div>}
    </details>
    <button type="button" className="secondary-button close-live-activity" onClick={close}><X size={14} />Close advanced runtime</button>
  </section>;
}

function Inspector({ data, task, run, agent, apiBase, csrf, close, reload, inspectRun, mutable }: { data: WorkbenchData; task: TaskDetail | null; run: Run | null; agent: Agent | null; apiBase: string; csrf: string; close: () => void; reload: () => Promise<void>; inspectRun: (run: Run) => void; mutable: boolean }) {
  const [logs, setLogs] = useState("");
  const [git, setGit] = useState<Checkout | null>(null);
  const [tab, setTab] = useState<"live" | "context" | "changes" | "history">("live");
  const [runDetail, setRunDetail] = useState<RunDetailView | null>(null);
  const [contextExplanation, setContextExplanation] = useState<ContextExplanation | null>(null);
  const [runMessages, setRunMessages] = useState<InboxItem[]>([]);
  const [prompt, setPrompt] = useState("");
  const [notice, setNotice] = useState("");
  const [terminalOpen, setTerminalOpen] = useState(false);
  const [artifact, setArtifact] = useState<RunArtifactContent | null>(null);
  const [artifactError, setArtifactError] = useState("");
  const [busy, setBusy] = useState(false);
  const currentRun = run ? data.runs.find((candidate) => candidate.id === run.id) ?? run : null;
  const currentTaskDetail = currentRun ? data.tasks.find((item) => item.task.id === currentRun.task_id) ?? null : null;
  const currentTask = currentTaskDetail?.task ?? null;
  const remediationTasks = currentTaskDetail ? taskSuccessors(currentTaskDetail, data.tasks).map((detail) => ({ ...detail, task: { ...detail.task, assigned_agent_id: taskOwnerAgentID(detail, data.runs) } })) : [];
  const currentOutcome = currentRun ? runOutcome(currentRun, currentTask ?? undefined) : null;
  const canRetryReview = currentRun?.status === "review" && currentTask?.status === "changes_requested";
  const canStartFresh = currentRun?.status === "stopped" && currentTask?.status === "assigned";
	const canRetryFailure = currentRun?.status === "failed" && currentTask?.status === "failed";
  useEffect(() => {
    setLogs("");
    if (!currentRun) return;
    let active = true;
    let loading = false;
    const loadLogs = async () => {
      if (loading) return;
      loading = true;
      try {
        const result = await rpc<{ logs: { stdout: { text: string; truncated: boolean; omitted_bytes: number }; stderr: { text: string; truncated: boolean; omitted_bytes: number }; state: string } }>(apiBase, csrf, "run.logs", { workspace: data.workspace?.id, run: currentRun.id, tail: 160 });
        if (active) setLogs([result.logs.stdout.text, result.logs.stderr.text, result.logs.stdout.truncated || result.logs.stderr.truncated ? "[bounded log output; earlier bytes omitted]" : ""].filter(Boolean).join("\n"));
      } catch (error) {
        if (active) setLogs(error instanceof Error ? error.message : "Logs unavailable");
      } finally { loading = false; }
    };
    void loadLogs();
    const live = ["requested", "starting", "active", "blocked", "stopping"].includes(currentRun.status);
    const timer = live ? window.setInterval(() => void loadLogs(), 1000) : undefined;
    return () => { active = false; if (timer !== undefined) window.clearInterval(timer); };
  }, [apiBase, csrf, data.workspace?.id, currentRun?.id, currentRun?.status, data.highWater]);
  useEffect(() => { setGit(data.checkouts[0] ?? null); }, [data.checkouts]);
  useEffect(() => {
    setRunDetail(null); setContextExplanation(null); setRunMessages([]); setTab("live");
    if (!currentRun || !data.workspace) return;
    void rpc<{ detail: RunDetailView }>(apiBase, csrf, "run.show", { workspace: data.workspace.id, run: currentRun.id }).then((result) => {
      setRunDetail(result.detail);
      if (result.detail.run.context_packet_id) void rpc<{ explanation: ContextExplanation }>(apiBase, csrf, "context.explain", { workspace: data.workspace?.id, context: result.detail.run.context_packet_id }).then((value) => setContextExplanation(value.explanation)).catch(() => setContextExplanation(null));
    }).catch(() => setRunDetail(null));
    void rpc<{ items: InboxItem[] }>(apiBase, csrf, "inbox.list", { workspace: data.workspace.id, agent: currentRun.agent_id, limit: 50 }).then((value) => setRunMessages(value.items.filter((item) => !item.message.task_id || item.message.task_id === currentRun.task_id))).catch(() => setRunMessages([]));
  }, [apiBase, csrf, currentRun?.id, data.highWater, data.workspace?.id]);
  if (!task && !run && !agent) return null;
  const stop = async () => { if (!mutable || !currentRun || !data.workspace) return; setBusy(true); setNotice(""); try { await rpc(apiBase, csrf, "run.stop", { workspace: data.workspace.id, run: currentRun.id, expected_revision: currentRun.revision, grace_period_millis: 5000, idempotency_key: newKey("stop") }); setNotice("Graceful stop requested with the displayed 5000 ms grace."); await reload(); } catch (reason) { setNotice(reason instanceof Error ? reason.message : "Stop failed."); } finally { setBusy(false); } };
  const resume = async () => { if (!mutable || !currentRun || !data.workspace) return; setBusy(true); setNotice(""); try { await rpc(apiBase, csrf, "run.resume", { workspace: data.workspace.id, run: currentRun.id, expected_revision: currentRun.revision, idempotency_key: newKey("resume") }); setNotice("Run resumed from its exact current revision."); await reload(); } catch (reason) { setNotice(reason instanceof Error ? reason.message : "Resume failed."); } finally { setBusy(false); } };
  const interrupt = async () => { if (!mutable || !currentRun || !data.workspace) return; setBusy(true); setNotice(""); try { await rpc(apiBase, csrf, "run.interrupt", { workspace: data.workspace.id, run: currentRun.id }); setNotice("Interrupt delivered to the current-node runtime binding."); } catch (reason) { setNotice(reason instanceof Error ? reason.message : "Interrupt failed."); } finally { setBusy(false); } };
  const sendPrompt = async () => { if (!mutable || !currentRun || !data.workspace || !prompt.trim()) return; setBusy(true); setNotice(""); try { await rpc(apiBase, csrf, "run.prompt", { workspace: data.workspace.id, run: currentRun.id, text: prompt.trim() }); setPrompt(""); setNotice("Prompt delivered to the current live runtime."); } catch (reason) { setNotice(reason instanceof Error ? reason.message : "Prompt failed."); } finally { setBusy(false); } };
  const openArtifact = async (artifactID: string) => { if (!data.workspace) return; setArtifact(null); setArtifactError(""); try { const result = await rpc<{ artifact: RunArtifactContent }>(apiBase, csrf, "run.artifact.show", { workspace: data.workspace.id, artifact: artifactID }); setArtifact(result.artifact); } catch (reason) { setArtifactError(reason instanceof Error ? reason.message : "Evidence is unavailable."); } };
  const retry = async () => { if (!mutable || !currentRun || !currentTask || !data.workspace || currentRun.status !== "start_failed" && !canRetryReview && !canStartFresh && !canRetryFailure) return; setBusy(true); setNotice(""); try { const freshRun = await retryWorkbenchRun(apiBase, csrf, data.workspace.id, currentRun, currentTask); setNotice(canRetryFailure ? "The failed attempt remains in history. A fresh attempt was assigned to the same durable agent using the exact retained checkout and launch profile." : canRetryReview ? "Requested changes were reopened on the retained assignment and a fresh run was queued." : canStartFresh ? "A fresh run was queued under the current service, provider, runtime, and network policy." : "A fresh run was requested after the runtime and provider preflight passed."); await reload(); inspectRun(freshRun); } catch (reason) { setNotice(reason instanceof Error ? reason.message : "Retry failed."); } finally { setBusy(false); } };
  const refreshGit = async () => { if (!data.workspace || !data.project) return; setBusy(true); setNotice(""); try { const response = await fetch(`${apiBase}/git?workspace=${encodeURIComponent(data.workspace.id)}&project=${encodeURIComponent(data.project.id)}`, { credentials: "same-origin" }); const result = (await response.json()) as { observations?: Array<Omit<Checkout, "id" | "project_id" | "path" | "write_mode"> & { checkout_id: string }>; error?: { message: string } }; if (!response.ok || !result.observations) throw new Error(result.error?.message ?? "Git observation failed"); const observation = result.observations[0]; const canonical = data.checkouts.find((checkout) => checkout.id === observation?.checkout_id); setGit(observation && canonical ? { ...canonical, ...observation, id: observation.checkout_id } : null); setNotice("Repository status refreshed without persisting source or diff content."); } catch (reason) { setNotice(reason instanceof Error ? reason.message : "Git observation failed."); } finally { setBusy(false); } };
  const agentRun = agent ? latestRunForAgent(data.runs, agent.id) : null;
  return <div className="drawer-scrim" onMouseDown={(event) => { if (event.target === event.currentTarget) close(); }}><aside className={`inspector${terminalOpen ? " live-inspector" : ""}`} aria-label="Canonical inspector"><header><div><div className="eyebrow">Exact inspector</div><h2>{run ? data.agents.find((candidate) => candidate.id === run.agent_id)?.name ?? "Agent run" : agent?.name ?? task?.task.title}</h2></div><IconButton label="Close inspector" onClick={close}><X size={18} /></IconButton></header>
    {currentRun ? <><div className="inspector-status"><StatusPill value={currentOutcome?.label ?? currentRun.status} tone={currentOutcome?.tone ?? currentRun.status} /><span>{currentRun.provider} through {currentRun.runtime}</span></div><section><h3>Assigned work</h3><p>{currentTask?.title ?? "Task unavailable in this bounded page"}</p><small>{currentTask?.task_class ? `${currentTask.task_class} task` : "task class unavailable"}</small></section>{runDetail?.assessment && <section className={`m24-assessment ${statusTone(runDetail.assessment)}`}><div className="section-title"><h3>Structured assessment</h3><ClipboardCheck size={16} /></div><p><strong>{runDetail.assessment === "pass" ? "PASS" : runDetail.assessment === "block" ? "BLOCK" : "CHANGES REQUESTED"}</strong></p><p>{currentRun.result_summary || runDetail.handoff?.summary || "The assessment was recorded without an additional summary."}</p><small>{runDetail.assessment === "changes_requested" && currentTask?.status === "completed" ? "This review is complete. Its immutable handoff and evidence now belong to the graph's remediation step; this reviewer should not retry the implementation." : "This is the reviewer or verifier's canonical assessment. Process completion alone does not mean delivery acceptance."}</small></section>}{runDetail?.assessment === "changes_requested" && currentTask?.status === "completed" && <section className="launch-failure review-retry"><div className="section-title"><h3>Next owner</h3><ArrowRight size={16} /></div>{remediationTasks.length ? remediationTasks.map((next) => <p key={next.task.id}><strong>{data.agents.find((candidate) => candidate.id === next.task.assigned_agent_id)?.name ?? "Unassigned"}</strong> owns “{next.task.title}” · {next.readiness.ready ? "ready for Crewfold to launch" : readableTaskReadiness(next, data.tasks)}</p>) : <p>The accepted graph has no remediation successor. Review the workstream graph before claiming that these findings are resolved.</p>}</section>}{["start_failed", "failed", "lost"].includes(currentRun.status) && <section className="launch-failure" role="alert"><div className="section-title"><h3>{currentRun.status === "start_failed" ? "Launch failed" : currentRun.status === "lost" ? "Runtime outcome is unknown" : "Run failed"}</h3><AlertCircle size={16} /></div><p>{currentRun.failure_message ?? currentRun.failure_code ?? "Inspect the bounded runtime output for the exact provider diagnosis."}</p>{currentRun.status === "start_failed" && <button className="primary-button compact" disabled={!mutable || busy} onClick={() => void retry()}>{busy ? <LoaderCircle className="spin" size={14} /> : <RotateCcw size={14} />}Retry after preflight</button>}{canRetryFailure && <><p>The failed attempt remains immutable. Retry creates a fresh attempt for the same durable agent, checkout, and accepted launch profile after checking the current account.</p><button className="primary-button compact" disabled={!mutable || busy} onClick={() => void retry()}>{busy ? <LoaderCircle className="spin" size={14} /> : <RotateCcw size={14} />}Retry now</button></>}</section>}{currentRun.status === "blocked" && <section className="launch-failure review-retry" role="alert"><div className="section-title"><h3>Why this run stopped</h3><AlertCircle size={16} /></div><p>{runDetail?.blocker?.reason ?? currentRun.blocked_question ?? currentTask?.blocked_reason ?? "The agent reported an unresolved blocker."}</p>{runDetail?.blocker?.needs?.length ? <><h4>What must be supplied or repaired</h4><ul>{runDetail.blocker.needs.map((need) => <li key={need}>{need}</li>)}</ul></> : <p>No structured needs were included in the blocked report. Inspect the activity and ask the owning agent for the exact missing input before resuming.</p>}<h4>Choose the correct recovery</h4><p><strong>Resume same runtime</strong> only after those needs were delivered into this run or its existing checkout. If the checkout, service, provider, runtime, or network policy must change, stop this run first and then launch the retained assignment in a fresh environment.</p>{runDetail?.blocker?.related_ids?.length ? <small>Related canonical records: {runDetail.blocker.related_ids.join(", ")}</small> : null}</section>}{canStartFresh && <section className="launch-failure review-retry"><div className="section-title"><h3>Launch a fresh environment</h3><RotateCcw size={16} /></div><p>The prior runtime is durably stopped and no longer holds authority. The task's exact assignment is retained.</p><p>This creates one fresh run using the current service, provider, runtime, and network policy.</p><button className="primary-button compact" disabled={!mutable || busy} onClick={() => void retry()}>{busy ? <LoaderCircle className="spin" size={14} /> : <RotateCcw size={14} />}Start fresh run</button></section>}{canRetryReview && <section className="launch-failure review-retry" role="alert"><div className="section-title"><h3>Completion evidence was rejected</h3><ClipboardCheck size={16} /></div><p>{currentTask?.blocked_reason ?? "The completion did not satisfy the task acceptance evidence."}</p><p>This is not a completed independent review. Retrying reopens this exact retained assignment with a fresh context-bound run.</p><button className="primary-button compact" disabled={!mutable || busy} onClick={() => void retry()}>{busy ? <LoaderCircle className="spin" size={14} /> : <RotateCcw size={14} />}Retry rejected completion</button></section>}{terminalOpen ? <LiveTerminal key={currentRun.id} apiBase={apiBase} csrf={csrf} workspace={data.workspace?.id ?? ""} run={currentRun} initialLogs={logs} close={() => setTerminalOpen(false)} mutable={mutable} /> : <section><div className="section-title"><h3>Captured agent activity</h3><Activity size={15} /></div><RuntimeOutput logs={logs} status={currentRun.status} /></section>}{["active", "blocked"].includes(currentRun.status) && <section className="runtime-control"><label htmlFor="runtime-prompt">Send a visible runtime prompt</label><div><input id="runtime-prompt" value={prompt} maxLength={4095} onChange={(event) => setPrompt(event.target.value)} placeholder="Clarify the next observable step…" /><button className="secondary-button" disabled={busy || !prompt.trim()} onClick={() => void sendPrompt()}><Send size={14} />Send</button></div><div className="runtime-buttons">{currentRun.can_attach && !terminalOpen && <button className="secondary-button" disabled={busy} onClick={() => setTerminalOpen(true)}><TerminalSquare size={14} />Open live activity</button>}{currentRun.status === "blocked" && <button className="secondary-button" disabled={busy} onClick={() => void resume()}><RotateCcw size={14} />Resume after repair</button>}<button className="secondary-button" disabled={busy} onClick={() => void interrupt()}><AlertCircle size={14} />Interrupt</button><button className="danger-button" onClick={() => void stop()} disabled={busy}>{busy ? <LoaderCircle className="spin" size={15} /> : <Square size={15} />}Stop · 5000 ms grace</button></div></section>}{notice && <div className="notice" role="status">{notice}</div>}<footer><span>Revision {currentRun.revision}</span><span>{currentRun.can_attach ? "Live activity and interactive controls available" : canRetryFailure ? "Fresh attempt available with the repaired account" : currentRun.status === "start_failed" ? "Retry available after diagnosis" : canRetryReview ? "Rejected completion retry available" : canStartFresh ? "Fresh launch available on retained assignment" : "Bounded logs only"}</span></footer></> : agent ? <><div className="inspector-status"><StatusPill value={agentRun?.status ?? (agent.enabled ? "ready" : "disabled")} /><span>{agent.provider} through {agent.runtime}</span></div><section><h3>Authority-neutral role</h3><p>{agent.role}. Scheduling authority comes from policy, assignment, and receipts—not this label.</p></section><section><div className="section-title"><h3>Repository observation</h3><IconButton label="Refresh Git observation" onClick={() => void refreshGit()} disabled={busy}><RefreshCw className={busy ? "spin" : ""} size={14} /></IconButton></div>{git ? <><dl className="fact-list"><div><dt>Availability</dt><dd>{git.availability}</dd></div><div><dt>Branch</dt><dd>{git.branch || "detached"}</dd></div><div><dt>Working tree</dt><dd>{git.dirty ? `${git.dirty_paths?.length ?? 0}${git.omitted_paths ? `+${git.omitted_paths}` : ""} changed paths` : "clean"}</dd></div><div><dt>Write mode</dt><dd>{git.write_mode}</dd></div></dl>{git.dirty_paths && git.dirty_paths.length > 0 && <div className="changed-paths">{git.dirty_paths.slice(0, 16).map((path) => <code key={path}>{path}</code>)}{git.dirty_paths.length > 16 && <small>+{git.dirty_paths.length - 16 + (git.omitted_paths ?? 0)} paths omitted from this view</small>}</div>}</> : <p>No checkout is loaded in this bounded scope.</p>}</section>{notice && <div className="notice" role="status">{notice}</div>}{agentRun ? <button className="secondary-button" onClick={() => inspectRun(agentRun)}><TerminalSquare size={15} />Open run details</button> : <div className="quiet-line"><Clock3 size={14} />No current run</div>}<footer><span>Definition revision {agent.revision}</span><span>{agent.enabled ? "Enabled" : "Disabled"}</span></footer></> : task && <><div className="inspector-status"><StatusPill value={task.task.status} /><span>Priority {task.task.priority}</span></div><section><h3>Description</h3><p>{task.task.description || "No additional description."}</p></section><section><h3>Readiness</h3><p>{taskProgressExplanation(task, data.tasks, data.runs, data.agents)}</p></section><section><h3>Assignment</h3><p>{data.agents.find((candidate) => candidate.id === task.task.assigned_agent_id)?.name ?? (["completed", "failed", "cancelled"].includes(task.task.status) ? "Released after the task finished." : "Unassigned")}</p></section><footer><span>Revision {task.task.revision}</span><span>Updated {displayTime(task.task.updated_at)}</span></footer></>}
    {currentRun && runDetail && <section className="m24-evidence"><div className="section-title"><h3>Evidence and handoff</h3><FileText size={15} /></div>{runDetail.handoff?.summary && <p>{runDetail.handoff.summary}</p>}<div className="m24-evidence-links">{Array.from(new Set([...(runDetail.handoff?.evidence ?? []), ...runDetail.timeline.flatMap((entry) => entry.evidence ?? [])])).filter((id) => id.startsWith("artifact_")).map((id) => <button type="button" className="secondary-button compact" key={id} onClick={() => void openArtifact(id)}><FileText size={13} />Read {id.slice(0, 17)}…</button>)}</div>{artifact && <article className="m24-evidence-content"><header><strong>{artifact.artifact.name}</strong><small>{artifact.artifact.media_type} · {artifact.artifact.byte_size} bytes</small></header><pre>{artifact.content}</pre><footer><code>{artifact.artifact.content_hash}</code></footer></article>}{artifactError && <p className="error-text" role="alert">{artifactError}</p>}</section>}
  </aside></div>;
}

function App() {
  const [connection, setConnection] = useState<ConnectionState>("connecting");
  const [status, setStatus] = useState<DaemonStatus | null>(null);
  const [apiBase, setAPIBase] = useState("");
  const [csrf, setCSRF] = useState("");
  const [data, setData] = useState<WorkbenchData>(emptyData);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [selectedTask, setSelectedTask] = useState<TaskDetail | null>(null);
  const [selectedRun, setSelectedRun] = useState<Run | null>(null);
  const [selectedAgent, setSelectedAgent] = useState<Agent | null>(null);
  const [selectedDomainAgentID, setSelectedDomainAgentID] = useState("");
  const [fresh, setFresh] = useState(false);
  const refreshing = useRef(false);

  const reload = useCallback(async () => {
    if (!apiBase || !csrf || refreshing.current) return;
    refreshing.current = true;
    try {
      const current = await loadWorkbench(apiBase, csrf, data.workspace?.id ?? sessionStorage.getItem("crewfold_workspace") ?? "", data.project?.id ?? sessionStorage.getItem("crewfold_project") ?? "");
      setData(current); setError(""); setFresh(true);
      if (current.workspace) sessionStorage.setItem("crewfold_workspace", current.workspace.id);
      if (current.project) sessionStorage.setItem("crewfold_project", current.project.id);
    } catch (reason) { setFresh(false); setError(reason instanceof Error ? reason.message : "Could not load canonical workbench state."); }
    finally { refreshing.current = false; setLoading(false); }
  }, [apiBase, csrf, data.project?.id, data.workspace?.id]);

  useEffect(() => {
    let active = true;
    const connect = async () => {
      try {
        const bootstrap = bootstrapFromFragment();
        let nextAPIBase = sessionStorage.getItem("crewfold_api_base") ?? "";
        let nextCSRF = sessionStorage.getItem("crewfold_csrf") ?? "";
        if (bootstrap) {
          const session = await exchangeBootstrap(bootstrap);
          nextAPIBase = session.api_base; nextCSRF = session.csrf_token;
          sessionStorage.setItem("crewfold_api_base", nextAPIBase); sessionStorage.setItem("crewfold_csrf", nextCSRF);
        }
        if (!/^\/api\/v1\/session\/[0-9a-f]{64}$/.test(nextAPIBase) || !/^[0-9a-f]{64}$/.test(nextCSRF)) throw new Error("unauthorized");
        const current = await loadStatus(nextAPIBase);
        if (!active) return;
        setStatus(current); setAPIBase(nextAPIBase); setCSRF(nextCSRF); setConnection("connected");
      } catch (reason) {
        if (!active) return;
        const message = reason instanceof Error ? reason.message : "connection failed";
        if (message === "unauthorized") { sessionStorage.removeItem("crewfold_api_base"); sessionStorage.removeItem("crewfold_csrf"); }
        setConnection(message === "unauthorized" ? "unauthorized" : "failed"); setError(message === "unauthorized" ? "Run crewfold open to obtain a fresh one-time grant." : message); setLoading(false);
      }
    };
    void connect();
    return () => { active = false; };
  }, []);

  useEffect(() => { if (apiBase && csrf) void reload(); }, [apiBase, csrf]);
  useEffect(() => {
    if (!apiBase || !data.workspace) return;
    let timer = 0;
    const stream = new EventSource(`${apiBase}/events?workspace=${encodeURIComponent(data.workspace.id)}`, { withCredentials: true });
    stream.addEventListener("invalidated", () => { setFresh(false); window.clearTimeout(timer); timer = window.setTimeout(() => void reload(), 120); });
    stream.addEventListener("open", () => { if (!refreshing.current) setFresh(true); });
    stream.addEventListener("error", () => { setFresh(false); setError("Live canonical updates disconnected; state-changing controls are disabled until refresh succeeds."); });
    return () => { window.clearTimeout(timer); stream.close(); };
  }, [apiBase, data.workspace?.id, reload]);

  const selectWorkspace = async (id: string) => {
    const workspace = data.workspaces.find((item) => item.id === id) ?? null;
    setLoading(true);
    setData({ ...data, workspace, project: null });
    sessionStorage.setItem("crewfold_workspace", id);
    sessionStorage.removeItem("crewfold_project");
    refreshing.current = true;
    try {
      const current = await loadWorkbench(apiBase, csrf, id, "");
      setData(current);
      if (current.project) sessionStorage.setItem("crewfold_project", current.project.id);
      setError(""); setFresh(true);
    } catch (reason) {
      setFresh(false); setError(reason instanceof Error ? reason.message : "Could not change workspace.");
    } finally {
      refreshing.current = false;
      setLoading(false);
    }
  };
  const selectProject = async (id: string) => {
    const project = data.projects.find((item) => item.id === id) ?? null;
    setLoading(true);
    setData({ ...data, project });
    sessionStorage.setItem("crewfold_project", id);
    refreshing.current = true;
    try {
      const current = await loadWorkbench(apiBase, csrf, data.workspace?.id ?? "", id);
      setData(current);
      setError(""); setFresh(true);
    } catch (reason) {
      setFresh(false); setError(reason instanceof Error ? reason.message : "Could not change project.");
    } finally {
      refreshing.current = false;
      setLoading(false);
    }
  };
  const inspectTask = (task: TaskDetail) => { setSelectedRun(null); setSelectedAgent(null); setSelectedTask(task); };
  const inspectRun = (run: Run) => { setSelectedTask(null); setSelectedAgent(null); setSelectedRun(run); };
  const inspectAgent = (agent: Agent) => { setSelectedTask(null); setSelectedRun(null); setSelectedAgent(agent); };

  if (connection !== "connected") return <div className="connection-screen"><div className={`connection-orb ${connection}`}>{connection === "connecting" ? <LoaderCircle className="spin" /> : <AlertCircle />}</div><div className="eyebrow">Owner-local workbench</div><h1>{connection === "connecting" ? "Connecting to Crewfold" : "Workbench access required"}</h1><p>{error || "Exchanging the one-time owner grant…"}</p></div>;

  if (loading) return <main className="loading-main m22-loading"><LoaderCircle className="spin" size={22} /><p>loading canonical state…</p></main>;
  if (data.workspaces.length === 0 || data.projects.length === 0 || data.agents.length === 0) return <Onboarding apiBase={apiBase} csrf={csrf} status={status} onComplete={reload} />;

  return <div className={`m22-root ${fresh ? "" : "stale"}`}>
    <header className="m22-top">
      <div className="m22-brand"><span>CF</span><strong>Crewfold</strong><small>local</small></div>
      <div className="m22-domain-tabs">{data.projects.map((project) => <button key={project.id} className={project.id === data.project?.id ? "active" : ""} onClick={() => void selectProject(project.id)}>{project.name}</button>)}<button className="add" aria-label="Add domain" disabled>+</button></div>
      <div className={`m22-live ${fresh ? "connected" : ""}`}><i />{fresh ? `event ${data.highWater}` : "refreshing"}</div>
    </header>
    {error && <div className="m22-error"><AlertCircle size={15} /><span>{error}</span><button onClick={() => void reload()}>retry</button></div>}
    <DomainConsole data={data} selectedAgentID={selectedDomainAgentID} selectAgent={(agent) => setSelectedDomainAgentID(agent?.definition.id ?? "")} selectProject={(id) => void selectProject(id)} inspectTask={inspectTask} inspectRun={inspectRun} apiBase={apiBase} csrf={csrf} mutable={fresh} reload={reload} />
    <Inspector data={data} task={selectedTask} run={selectedRun} agent={selectedAgent} apiBase={apiBase} csrf={csrf} close={() => { setSelectedTask(null); setSelectedRun(null); setSelectedAgent(null); }} reload={reload} inspectRun={inspectRun} mutable={fresh} />
  </div>;
}

createRoot(document.getElementById("root")!).render(<StrictMode><App /></StrictMode>);
