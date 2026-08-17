import { StrictMode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import {
  Activity, AlertCircle, AlertTriangle, BookOpenText, Bot, Boxes, Check, CheckCircle2, ChevronDown, ChevronRight,
  CircleDot, ClipboardCheck, Clock3, Code2, Command, Database, FileCheck2, GitBranch,
  GitCommitHorizontal, HeartPulse, Inbox, LayoutDashboard, ListChecks, LoaderCircle,
  Menu, MessageCircle, MessageSquareText, Network, Play, Plus, RefreshCw, RotateCcw,
  Search, Send, Settings, ShieldCheck, Sparkles, Square, Stethoscope, TerminalSquare,
  Users, Workflow, X, XCircle,
} from "lucide-react";
import "@xterm/xterm/css/xterm.css";
import "./styles.css";

type ConnectionState = "connecting" | "connected" | "unauthorized" | "failed";
type View = "workbench" | "graph" | "crew" | "inbox" | "decisions" | "evidence" | "briefing" | "activity" | "health";

type APIError = { code: string; message: string; retryable: boolean; details?: Record<string, unknown> };
type RPCEnvelope<T> = { id: string; protocol: number; result?: T; error?: APIError };
type Page = { next_cursor: string; has_more: boolean; total: number };
type Workspace = { id: string; name: string; revision: number; updated_at: string };
type Project = { id: string; workspace_id: string; name: string; revision: number; updated_at: string };
type Checkout = { id: string; project_id: string; path: string; write_mode: string; availability: string; branch?: string; head_commit?: string; dirty?: boolean; dirty_paths?: string[]; omitted_paths?: number; truncated?: boolean; diagnostic?: string };
type Agent = { id: string; workspace_id: string; name: string; role: string; provider: string; runtime: string; enabled: boolean; max_concurrency: number; revision: number };
type Objective = { id: string; project_id: string; title: string; status: string; revision: number; updated_at: string };
type Task = { id: string; project_id: string; objective_id?: string; title: string; description?: string; status: string; blocked_reason?: string; priority: number; revision: number; assigned_agent_id?: string; updated_at: string };
type TaskDetail = { task: Task; dependencies: Array<{ depends_on_task_id: string }>; assignment?: { agent_id: string }; readiness: { ready: boolean; reason: string } };
type Run = { id: string; project_id: string; task_id: string; agent_id: string; runtime: string; provider: string; status: string; can_attach: boolean; revision: number; updated_at: string; result_summary?: string; blocked_question?: string; failure_code?: string; failure_message?: string };
type RunDetailView = { run: Run & { context_packet_id?: string; created_at: string; started_at?: string; finished_at?: string; placement?: { checkout_path?: string; write_mode?: string; reasons?: string[] } }; task: Task; agent: Agent; checkout: Checkout; timeline: Array<{ sequence: number; kind: string; message?: string; evidence: string[]; recorded_at: string }>; handoff?: { summary: string; evidence: string[]; created_at: string } };
type ContextExplanation = { packet_id: string; content_hash: string; byte_size: number; included: Array<{ section: string; entity_type: string; entity_id: string; revision: number; reason: string }>; excluded: Array<{ section: string; reason: string; reason_code?: string }>; budget: { total: { limit_bytes: number; used_bytes: number; remaining_bytes: number } } };
type EventRecord = { event_id: string; sequence: number; type: string; recorded_at: string; actor: { actor_type: string }; entity: { type: string; id: string; revision: number } };
type Approval = { id: string; project_id?: string; action_id: string; status: string; decision_note?: string; revision: number; expires_at?: string; created_at: string; updated_at: string; decided_at?: string };
type SupervisorAction = { id: string; project_id?: string; objective_id?: string; task_id?: string; run_id?: string; agent_id?: string; condition: string; response: string; status: string; decision?: string; reasons: string[]; constraint_snapshot: Record<string, unknown>; revision: number; created_at: string; updated_at: string };
type InboxItem = { message: { id: string; sender_type: string; sender_agent_name?: string; kind: string; body: string; task_id?: string; created_at: string }; delivery: { recipient_agent_id: string; recipient_name: string; status: string; wake_status: string } };
type CheckRunItem = { run: { id: string; task_id: string; status: string; revision: number; created_at: string; updated_at: string }; outcome?: string; requirement_state: string; current_freshness?: { status?: string } };
type FullDoctor = { status: string; event_sequence: number; resources: { database_bytes: number; referenced_artifact_bytes: number; filesystem_free_bytes: number }; checks: Array<{ code: string; status: string; issue_count: number; summary: string }> };
type Briefing = { id: string; revision: number; event_cursor: number; caught_up: boolean; evaluated_at: string; claims: Array<{ id: string; kind: string; urgency: string; summary: string; status: string }>; omitted: Array<{ section: string; reason: string; count: number }>; byte_size: number };
type Budget = { token_limit: number; cost_cents: number; time_seconds: number };
type LaunchProfile = { id: string; project_id: string; agent_id: string; purpose?: string; runtime: string; provider: string; status: string; revision: number };
type OwnerChoice = { key: string; label: string; description: string; recommended: boolean };
type OwnerPlanTask = { key: string; title: string; description: string; priority: number; budget: Budget; launch_profile_id: string; depends_on: string[] };
type OwnerInterpretation = { disposition: "pending" | "answer" | "ready" | "clarify" | "refuse"; summary: string; answer: string; question: string; choices: OwnerChoice[]; objective_title: string; objective_budget: Budget; tasks: OwnerPlanTask[]; citation_refs: string[] };
type OwnerOperation = { id: string; ordinal: number; type: string; payload: Record<string, unknown>; policy_result: string; status: string; result_entity_type?: string; event_sequence?: number };
type OwnerTurnDetail = { conversation: { id: string; title: string }; turn: { id: string; ordinal: number; kind: "query" | "plan" | "act" | "instruction" | "review"; initiated_by: "owner" | "executive"; trigger_event_sequence?: number; instruction: string; status: string; answer?: string; as_of_event_sequence: number; completed_event_sequence?: number; revision: number; interpretation: OwnerInterpretation; citations: Array<{ ref: string; entity_type: string; entity_id: string; entity_revision: number; as_of_event_sequence: number; label: string }> }; operations: OwnerOperation[]; receipts: Array<{ operation_id: string; method: string; event_sequence?: number }> };
type OwnerManagerReview = { workspace_id: string; project_id: string; conversation_id: string; status: "idle" | "pending" | "leased" | "failed"; requested_event_sequence: number; reviewed_event_sequence: number; attempts: number; last_turn_id?: string; last_error?: string; updated_at: string };
type OwnerExecutiveExchange = { id: string; turn_id: string; binding_id: string; run_id?: string; event_sequence: number; status: "pending" | "leased" | "running" | "responded" | "failed"; attempts: number; proposal_ids: string[]; last_error?: string; updated_at: string };
type OwnerExecutiveBinding = { id: string; agent_id: string; objective_id: string; planning_task_id: string; manager_grant_id: string; launch_profile_id: string; status: string; revision: number };
type ManagerProposalAction = {
  id?: string;
  ordinal: number;
  type: string;
  create_task?: { task_key: string; launch_profile_id: string; title: string; description?: string; priority: number; budget: Budget };
  add_dependency?: { task: { task_id?: string; proposal_task_key?: string }; depends_on: { task_id?: string; proposal_task_key?: string } };
  declare_claim_requirement?: { task: { task_id?: string; proposal_task_key?: string }; kind: string; target: string; mode: string; conflict_policy: string };
  assign_task?: { task: { task_id?: string; proposal_task_key?: string }; launch_profile_id: string };
  request_review?: { task: { task_id?: string; proposal_task_key?: string }; launch_profile_id: string; title: string; description?: string; priority: number; budget: Budget };
  request_action?: { response: string; target_run_id?: string; target_task_id?: string; launch_profile_id?: string; reason: string; expected_revision: number };
};
type ManagerProposal = { id: string; project_id: string; objective_id: string; source_run_id: string; source_agent_id: string; kind: string; summary: string; status: string; as_of_event_sequence: number; actions: ManagerProposalAction[]; validation_issues: Array<{ code: string; path: string; message: string; severity: string }>; decision_note?: string; revision: number; created_at: string; updated_at: string; decided_at?: string };

type WorkbenchData = {
  workspaces: Workspace[];
  workspace: Workspace | null;
  projects: Project[];
  project: Project | null;
  checkouts: Checkout[];
  agents: Agent[];
  launchProfiles: LaunchProfile[];
  objectives: Objective[];
  tasks: TaskDetail[];
  runs: Run[];
  approvals: Approval[];
  proposals: ManagerProposal[];
  supervisorActions: SupervisorAction[];
  checks: CheckRunItem[];
  events: EventRecord[];
  executive: OwnerExecutiveBinding | null;
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
const emptyData: WorkbenchData = { workspaces: [], workspace: null, projects: [], project: null, checkouts: [], agents: [], launchProfiles: [], objectives: [], tasks: [], runs: [], approvals: [], proposals: [], supervisorActions: [], checks: [], events: [], executive: null, highWater: 0 };

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
function pendingConsequentialDecisions(data: WorkbenchData) {
  const actions = new Map(data.supervisorActions.map((action) => [action.id, action]));
  const approvals = data.approvals.filter((approval) => approval.status === "pending" && actions.get(approval.action_id)?.response !== "request_owner");
  const lostRuns = data.runs.filter((run) => run.status === "lost");
  return data.proposals.filter((proposal) => proposal.status === "pending").length + approvals.length + lostRuns.length;
}
function statusTone(status: string) {
  if (["completed", "granted", "consumed", "available", "ready"].includes(status)) return "good";
  if (["failed", "start_failed", "lost", "denied"].includes(status)) return "bad";
  if (["active", "starting", "requested", "stopping", "pending"].includes(status)) return "live";
  if (["blocked", "review", "changes_requested"].includes(status)) return "warn";
  return "quiet";
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
  return unique.slice(-40);
}

function RuntimeActivityFeed({ logs, empty }: { logs: string; empty: string }) {
  const activity = useMemo(() => readableRuntimeActivity(logs), [logs]);
  if (activity.length === 0) return <div className="activity-empty"><Activity size={16} /><span>{empty}</span></div>;
  return <div className="runtime-activity">{activity.map((entry) => <article className={entry.tone} key={entry.key}><span>{entry.kind}</span><p>{entry.text}</p></article>)}</div>;
}

function RuntimeOutput({ logs, status }: { logs: string; status: string }) {
  const empty = logs
    ? "This bounded tail starts inside protocol data. Open live activity to combine it with the current stream."
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

async function submitOwnerIntent(apiBase: string, csrf: string, body: Record<string, unknown>): Promise<{ detail: OwnerTurnDetail; exchange: OwnerExecutiveExchange }> {
  const response = await fetch(`${apiBase}/intent`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-Crewfold-CSRF": csrf }, body: JSON.stringify(body) });
  if (response.status === 401) throw new Error("unauthorized");
  const value = (await response.json()) as { detail?: OwnerTurnDetail; exchange?: OwnerExecutiveExchange; error?: { message: string } };
  if (!response.ok || !value.detail || !value.exchange) throw new Error(value.error?.message ?? `owner instruction failed (${response.status})`);
  return { detail: value.detail, exchange: value.exchange };
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

async function loadOwnerConversation(apiBase: string, workspace: string, project: string): Promise<{ turns: OwnerTurnDetail[]; exchanges: OwnerExecutiveExchange[]; executive: OwnerExecutiveBinding | null; review: OwnerManagerReview | null }> {
  const response = await fetch(`${apiBase}/conversation?workspace=${encodeURIComponent(workspace)}&project=${encodeURIComponent(project)}`, { credentials: "same-origin" });
  if (!response.ok) throw new Error(`conversation read failed (${response.status})`);
  const value = (await response.json()) as { turns?: OwnerTurnDetail[]; exchanges?: OwnerExecutiveExchange[]; executive?: OwnerExecutiveBinding | null; review?: OwnerManagerReview | null };
  return { turns: value.turns ?? [], exchanges: value.exchanges ?? [], executive: value.executive ?? null, review: value.review ?? null };
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
  const [projectPage, agentPage, objectivePage, taskPage, runPage, approvalPage, supervisorActionPage, eventPage] = await Promise.all([
    rpc<{ projects: Project[] } & Page>(apiBase, csrf, "project.list", { workspace: workspace.id, limit: 200 }),
    rpc<{ agents: Agent[] } & Page>(apiBase, csrf, "agent.list", { workspace: workspace.id, limit: 200 }),
    rpc<{ objectives: Objective[] } & Page>(apiBase, csrf, "objective.list", { workspace: workspace.id, limit: 200 }),
    rpc<{ tasks: TaskDetail[] } & Page>(apiBase, csrf, "task.list", { workspace: workspace.id, limit: 200 }),
    rpc<{ runs: Run[] } & Page>(apiBase, csrf, "run.list", { workspace: workspace.id, limit: 200 }),
    rpc<{ approvals: Approval[] } & Page>(apiBase, csrf, "approval.list", { workspace: workspace.id, limit: 200 }),
    rpc<{ actions: SupervisorAction[] }>(apiBase, csrf, "supervisor.action.list", { workspace: workspace.id, limit: 100 }),
    rpc<{ events: EventRecord[]; high_water: number } & Page>(apiBase, csrf, "events.list", { workspace: workspace.id, after: eventAfter, limit: 200 }),
  ]);
  const project = projectPage.projects.find((item) => item.id === preferredProject) ?? projectPage.projects[0] ?? null;
  const [checkouts, checks, launchProfiles, proposals, executive] = project ? await Promise.all([
    rpc<{ checkouts: Checkout[] }>(apiBase, csrf, "checkout.list", { workspace: workspace.id, project: project.id }).then((value) => value.checkouts),
    rpc<{ runs: CheckRunItem[] } & Page>(apiBase, csrf, "check.list", { workspace: workspace.id, project: project.id, limit: 200 }).then((value) => value.runs),
    rpc<{ profiles: LaunchProfile[] }>(apiBase, csrf, "launch_profile.list", { workspace: workspace.id, project: project.id, status: "active", limit: 100 }).then((value) => value.profiles),
    rpc<{ proposals: ManagerProposal[] }>(apiBase, csrf, "proposal.list", { workspace: workspace.id, project: project.id, limit: 100 }).then((value) => value.proposals),
    loadOwnerConversation(apiBase, workspace.id, project.id).then((value) => value.executive),
  ]) : [[], [], [], [], null];
  const after = await rpc<{ events: EventRecord[]; high_water: number } & Page>(apiBase, csrf, "events.list", { workspace: workspace.id, after: before.high_water, limit: 1 });
  if (after.high_water !== before.high_water) {
    if (attempt >= 2) throw new Error("Canonical state kept changing during refresh; retry when the current event cut settles.");
    return loadWorkbench(apiBase, csrf, workspace.id, project?.id ?? preferredProject, attempt + 1);
  }
  return {
    workspaces: workspacePage.workspaces, workspace, projects: projectPage.projects, project, checkouts,
    agents: agentPage.agents, launchProfiles, objectives: objectivePage.objectives, tasks: taskPage.tasks,
    runs: runPage.runs, approvals: approvalPage.approvals, proposals, supervisorActions: supervisorActionPage.actions, checks, events: eventPage.events, executive,
    highWater: eventPage.high_water,
  };
}

const navItems: Array<{ id: View; label: string; icon: typeof LayoutDashboard }> = [
  { id: "workbench", label: "Workbench", icon: MessageSquareText },
  { id: "graph", label: "Work graph", icon: Network },
  { id: "crew", label: "Crew", icon: Users },
  { id: "inbox", label: "Inbox", icon: Inbox },
  { id: "decisions", label: "Decisions", icon: ClipboardCheck },
  { id: "evidence", label: "Evidence", icon: FileCheck2 },
  { id: "briefing", label: "Briefing", icon: BookOpenText },
  { id: "activity", label: "Activity", icon: Activity },
  { id: "health", label: "Health", icon: Stethoscope },
];

function IconButton({ label, children, onClick, disabled = false }: { label: string; children: React.ReactNode; onClick?: () => void; disabled?: boolean }) {
  return <button className="icon-button" aria-label={label} title={label} onClick={onClick} disabled={disabled}>{children}</button>;
}

function StatusPill({ value }: { value: string }) {
  return <span className={`status-pill ${statusTone(value)}`}><CircleDot size={12} aria-hidden="true" />{value.replaceAll("_", " ")}</span>;
}

function readableTaskReadiness(detail: TaskDetail, tasks: TaskDetail[]) {
  if (detail.task.status === "changes_requested") return detail.task.blocked_reason || "Completion needs a reviewed retry.";
  const prerequisites = detail.dependencies.map((dependency) => tasks.find((candidate) => candidate.task.id === dependency.depends_on_task_id)?.task).filter((task): task is Task => Boolean(task && task.status !== "completed"));
  if (prerequisites.length > 0) return `Waiting for ${prerequisites.map((task) => `“${task.title}”`).join(", ")}`;
  return detail.readiness.reason || "Waiting for Crewfold to make this task runnable.";
}

function EmptyState({ icon: Icon, title, detail }: { icon: typeof Inbox; title: string; detail: string }) {
  return <div className="empty-state"><Icon size={28} aria-hidden="true" /><h3>{title}</h3><p>{detail}</p></div>;
}

function Onboarding({ apiBase, csrf, status, onComplete }: { apiBase: string; csrf: string; status: DaemonStatus | null; onComplete: () => Promise<void> }) {
  const [workspace, setWorkspace] = useState("personal");
  const [project, setProject] = useState("");
  const [path, setPath] = useState("");
  const [agent, setAgent] = useState("builder");
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
      await submitOnboarding(apiBase, csrf, { repository_path: path, workspace, project, agent, provider, runtime, write_mode: "shared" });
      await onComplete();
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "Onboarding failed without a diagnosis.");
    } finally { setBusy(false); }
  };
  return <main className="onboarding-main">
    <section className="onboarding-copy">
      <div className="eyebrow"><Sparkles size={14} />First local workspace</div>
      <h1>Bring your repository into the workbench.</h1>
      <p>Crewfold records the repository and creates two clear roles: a project executive for direction and an implementation worker for accepted work. Provider credentials stay in their native CLI home and never enter the browser.</p>
      <div className="onboarding-trust">
        <div><ShieldCheck size={19} /><span><strong>Local authority</strong>Loopback web, Unix socket, private database</span></div>
        <div><GitBranch size={19} /><span><strong>Existing repository</strong>No clone, move, or rewrite</span></div>
        <div><Bot size={19} /><span><strong>Subscription login</strong>Codex or Claude CLI, no API key form</span></div>
      </div>
    </section>
    <form className="onboarding-form" onSubmit={submit}>
      <div className="form-heading"><div className="step-mark">1</div><div><h2>Set up your workbench</h2><p>Review the initial worker and runtime before creating canonical state.</p></div></div>
      <label><span>Repository path</span><input required value={path} onChange={(event) => updatePath(event.target.value)} placeholder="~/depot/dev/world-engine-2" autoComplete="off" /></label>
      <div className="field-grid">
        <label><span>Workspace</span><input required pattern="[a-z][a-z0-9-]{0,62}" value={workspace} onChange={(event) => setWorkspace(event.target.value)} /></label>
        <label><span>Project</span><input required pattern="[a-z][a-z0-9-]{0,62}" value={project} onChange={(event) => setProject(event.target.value)} placeholder="world-engine-2" /></label>
      </div>
      <label><span>Implementation worker</span><input required pattern="[a-z][a-z0-9-]{0,62}" value={agent} onChange={(event) => setAgent(event.target.value)} /><small className="field-help">Executes implementation tasks only after you accept a proposal. Crewfold also creates a separate project executive for conversation and planning.</small></label>
      <label><span>Provider</span><select value={provider} onChange={(event) => setProvider(event.target.value)}><option value="codex">Codex subscription</option><option value="claude">Claude subscription</option><option value="fixture-mcp">Local fixture</option></select></label>
      {provider === "codex" && <div className="runtime-primary"><Network size={19} /><div><strong>Dependency and documentation network {status?.codex_tool_network_access ? "enabled" : "disabled"}</strong><span>{status?.codex_tool_network_access ? "Codex may retrieve packages and documentation inside the workspace sandbox. Publishing, deployment, credentials, paid services, and external side effects remain outside this grant." : "This service cannot retrieve uncached packages. Reinstall with --codex-tool-network-access true before starting implementation work."}</span></div><StatusPill value={status?.codex_tool_network_access ? "enabled" : "blocked"} /></div>}
      <div className="runtime-primary"><TerminalSquare size={19} /><div><strong>Herdr interactive runtime</strong><span>Persistent agent terminal hosted beside Crewfold's canonical state.</span></div><StatusPill value={runtime === "herdr" ? "recommended" : "fallback"} /></div>
      <details className="advanced-runtime"><summary>Advanced runtime fallback</summary><label><span>Execution runtime</span><select value={runtime} onChange={(event) => setRuntime(event.target.value)}><option value="herdr">Herdr interactive · recommended</option><option value="direct">Direct headless · CI and automation</option></select></label><p>Direct has bounded logs but no persistent interactive terminal.</p></details>
      {error && <div className="form-error" role="alert"><AlertCircle size={17} />{error}</div>}
      <button className="primary-button" disabled={busy}>{busy ? <LoaderCircle className="spin" size={17} /> : <Command size={17} />} {busy ? "Inspecting and recording…" : "Create workbench"}</button>
      <div className="role-preview"><div><Sparkles size={16} /><span><strong>Project executive</strong>Talks with you, reads canonical context, and proposes work.</span></div><ChevronRight size={15} /><div><Code2 size={16} /><span><strong>{agent || "Implementation worker"}</strong>Executes only accepted tasks through {runtime === "herdr" ? "Herdr" : "Direct"}.</span></div></div>
      <p className="form-note">One submission records the workspace, project checkout, executive, and worker with replay-safe idempotency.</p>
    </form>
  </main>;
}

function ManagerDecisionCard({ detail, actionable, busy, mutable, choose }: { detail: OwnerTurnDetail; actionable: boolean; busy: boolean; mutable: boolean; choose: (choice: OwnerChoice) => void }) {
  return <section className={`manager-decision-card ${actionable ? "" : "resolved"}`}><header><span className="decision-icon">?</span><div><strong>{actionable ? "Executive needs your decision" : "Earlier decision"}</strong><small>{actionable ? "Your selection is sent back as a visible owner instruction. It does not silently accept proposals or execute effects." : "Later conversation activity superseded this question; its choices are now inert."}</small></div></header><h3>{detail.turn.interpretation.question}</h3><div className="manager-choices">{detail.turn.interpretation.choices.map((choice) => <button key={choice.key} disabled={!actionable || !mutable || busy} onClick={() => choose(choice)}><span><strong>{choice.label}</strong>{choice.recommended && <em>Recommended</em>}</span><small>{choice.description}</small><span className="choose-label">{actionable ? "Send this answer" : "Superseded"}</span></button>)}</div></section>;
}

function WorkbenchView({ data, apiBase, csrf, reload, selectTask, selectRun, openDecisions, openCrew, mutable }: { data: WorkbenchData; apiBase: string; csrf: string; reload: () => Promise<void>; selectTask: (task: TaskDetail) => void; selectRun: (run: Run) => void; openDecisions: () => void; openCrew: () => void; mutable: boolean }) {
  const [instruction, setInstruction] = useState("");
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [turns, setTurns] = useState<OwnerTurnDetail[]>([]);
  const [exchanges, setExchanges] = useState<OwnerExecutiveExchange[]>([]);
  const [executive, setExecutive] = useState<OwnerExecutiveBinding | null>(data.executive);
  const [managerReview, setManagerReview] = useState<OwnerManagerReview | null>(null);
  const [trackedExchangeID, setTrackedExchangeID] = useState("");
  const recovering = useRef(false);
  const pendingKey = data.workspace && data.project ? `crewfold_pending_intent_${data.workspace.id}_${data.project.id}` : "";
  useEffect(() => { setExecutive(data.executive); }, [data.executive?.id, data.executive?.revision]);
  useEffect(() => {
    if (!data.workspace || !data.project) return;
    let active = true;
    let timer = 0;
    const poll = async () => {
      try {
        const page = await loadOwnerConversation(apiBase, data.workspace!.id, data.project!.id);
        if (!active) return;
        setTurns(page.turns); setExchanges(page.exchanges); setExecutive(page.executive); setManagerReview(page.review);
        const working = page.exchanges.some((exchange) => ["pending", "leased", "running"].includes(exchange.status));
        timer = window.setTimeout(() => void poll(), working || page.review && ["pending", "leased"].includes(page.review.status) ? 750 : 4000);
      } catch {
        if (!active) return;
        timer = window.setTimeout(() => void poll(), 4000);
      }
    };
    void poll();
    return () => { active = false; window.clearTimeout(timer); };
  }, [apiBase, data.project?.id, data.workspace?.id]);
  useEffect(() => {
    if (!trackedExchangeID) return;
    const exchange = exchanges.find((item) => item.id === trackedExchangeID);
    if (!exchange || !["responded", "failed"].includes(exchange.status)) return;
    setNotice(exchange.status === "responded" ? "Executive response recorded. The durable conversation remains here; its short-lived provider session is not project authority." : exchange.last_error ?? "The executive session ended before recording a response.");
    setTrackedExchangeID("");
    void reload();
  }, [exchanges, reload, trackedExchangeID]);
  useEffect(() => {
    if (!pendingKey || recovering.current) return;
    const raw = sessionStorage.getItem(pendingKey);
    if (!raw) return;
    recovering.current = true; setBusy(true); setNotice("Recovering the exact interrupted owner turn…");
    try {
      const body = JSON.parse(raw) as Record<string, unknown>;
      void submitOwnerIntent(apiBase, csrf, body).then(async ({ detail, exchange }) => {
        sessionStorage.removeItem(pendingKey);
        setTurns((current) => [...current.filter((item) => item.turn.id !== detail.turn.id), detail]);
        setExchanges((current) => [...current.filter((item) => item.id !== exchange.id), exchange]);
        setTrackedExchangeID(exchange.id);
        setNotice("Recovered the exact durable executive exchange without creating a duplicate run.");
        await reload();
      }).catch((reason) => setNotice(reason instanceof Error ? reason.message : "Interrupted turn recovery failed.")).finally(() => { recovering.current = false; setBusy(false); });
    } catch {
      sessionStorage.removeItem(pendingKey); recovering.current = false; setBusy(false); setNotice("Discarded an invalid local recovery marker; no daemon effect was requested.");
    }
  }, [apiBase, csrf, pendingKey]);
  const createWork = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!mutable || !data.workspace || !data.project || !instruction.trim()) return;
    setBusy(true); setNotice("");
    try {
      const currentConversation = turns.at(-1)?.conversation.id;
      const body = { workspace: data.workspace.id, project: data.project.id, ...(currentConversation ? { conversation_id: currentConversation } : {}), instruction: instruction.trim(), idempotency_key: newKey("executive-turn") };
      sessionStorage.setItem(pendingKey, JSON.stringify(body));
      const { detail, exchange } = await submitOwnerIntent(apiBase, csrf, body);
      sessionStorage.removeItem(pendingKey);
      setInstruction(""); setNotice("Instruction recorded. The project executive is working from the frozen canonical context.");
      setTurns((current) => [...current, detail]);
      setExchanges((current) => [...current, exchange]);
      setTrackedExchangeID(exchange.id);
      await reload();
    } catch (reason) { setNotice(reason instanceof Error ? reason.message : "The instruction could not be committed."); }
    finally { setBusy(false); }
  };
  const answerManagerChoice = async (detail: OwnerTurnDetail, choice: OwnerChoice) => {
    if (!mutable || !data.workspace || !data.project) return;
    setBusy(true); setNotice("");
    try {
      const body = { workspace: data.workspace.id, project: data.project.id, conversation_id: detail.conversation.id, instruction: `My decision for "${detail.turn.interpretation.question}": ${choice.label}. ${choice.description}`.trim(), idempotency_key: newKey(`executive-choice-${choice.key}`) };
      const next = await submitOwnerIntent(apiBase, csrf, body);
      setTurns((current) => [...current, next.detail]);
      setExchanges((current) => [...current, next.exchange]);
      setNotice("Your decision was recorded and sent to the project executive as a new durable turn.");
      await reload();
    } catch (reason) { setNotice(reason instanceof Error ? reason.message : "The manager decision could not be processed."); }
    finally { setBusy(false); }
  };
  const projectTasks = data.tasks.filter(({ task }) => task.id !== executive?.planning_task_id);
  const projectRuns = data.runs.filter((run) => run.task_id !== executive?.planning_task_id);
  const activeRuns = projectRuns.filter((run) => ["requested", "starting", "active", "blocked", "stopping"].includes(run.status));
  const attentionRuns = projectRuns.filter((run) => ["requested", "starting", "active", "blocked", "stopping", "start_failed", "failed", "lost"].includes(run.status) || run.status === "review" && data.tasks.some((detail) => detail.task.id === run.task_id && detail.task.status === "changes_requested")).sort((left, right) => right.updated_at.localeCompare(left.updated_at) || right.id.localeCompare(left.id));
  const openTasks = projectTasks.filter(({ task }) => !["completed", "failed", "cancelled"].includes(task.status));
  const exchangeByTurn = new Map(exchanges.map((exchange) => [exchange.turn_id, exchange]));
  const pendingProposalIDs = new Set(data.proposals.filter((proposal) => proposal.status === "pending").map((proposal) => proposal.id));
  const executiveAgent = executive ? data.agents.find((agent) => agent.id === executive.agent_id) : null;
  const implementationAgents = data.agents.filter((agent) => agent.id !== executiveAgent?.id && agent.enabled);
  const busyWorkerIDs = new Set(activeRuns.map((run) => run.agent_id));
  const idleWorkers = implementationAgents.filter((agent) => !busyWorkerIDs.has(agent.id));
  const readyTasks = openTasks.filter((detail) => detail.readiness.ready);
  const dependencyWaiting = openTasks.filter((detail) => detail.dependencies.some((dependency) => {
    const prerequisite = data.tasks.find((candidate) => candidate.task.id === dependency.depends_on_task_id)?.task;
    return prerequisite && prerequisite.status !== "completed";
  }));
  const exchangeMessage = (detail: OwnerTurnDetail, exchange?: OwnerExecutiveExchange) => {
    if (!exchange) return detail.turn.answer ?? detail.turn.interpretation.summary ?? "This historical turn predates the project executive exchange contract.";
    if (exchange.status === "pending") return exchange.last_error?.startsWith("planning assignment already has live run ") ? "This review is durably queued behind the executive session that is still closing. It will start automatically when that short-lived session releases the planning assignment." : "Your instruction is durably queued for the project executive.";
    if (exchange.status === "leased") return "Crewfold is freezing the executive run and its exact authority context.";
    if (exchange.status === "running") return "The project executive is working in a short-lived Herdr session. Its answer and any typed proposals will appear here.";
    if (exchange.status === "failed") return exchange.last_error ?? "The executive session ended before recording a response.";
    return detail.turn.answer ?? detail.turn.interpretation.summary ?? "The executive response was recorded.";
  };
  return <div className="view-grid workbench-view">
    <section className="conversation-panel panel">
      <div className="panel-heading"><div><div className="eyebrow"><Sparkles size={13} />Project direction</div><h1>Talk to your project executive</h1></div><StatusPill value="local" /></div>
      <div className="conversation-intro">
        <div className="assistant-avatar"><Bot size={22} /></div>
        <div><strong>{executiveAgent?.name ?? "Project executive"}</strong><p>I’m a durable Crewfold agent, not a one-shot form interpreter. The conversation persists, while each provider session is short-lived and closes after recording its response. I can answer, ask a decision, or create typed proposals; Crewfold remains the authority that validates and commits accepted effects.</p></div>
      </div>
      {managerReview && <div className={`manager-review-state ${managerReview.status}`}><Bot size={15} /><span><strong>{managerReview.status === "leased" ? "Executive is reviewing worker activity" : managerReview.status === "pending" ? "Worker activity queued for executive review" : managerReview.status === "failed" ? "Executive review needs attention" : "Executive is caught up"}</strong><small>{managerReview.status === "failed" ? managerReview.last_error : `reviewed #${managerReview.reviewed_event_sequence} · requested #${managerReview.requested_event_sequence}`}</small></span></div>}
      {turns.length > 0 && <div className="conversation-history">{turns.slice(-6).map((detail) => {
        const exchange = exchangeByTurn.get(detail.turn.id);
        const executiveRun = exchange?.run_id ? data.runs.find((run) => run.id === exchange.run_id) : undefined;
        return <div className="turn" key={detail.turn.id}>
        {detail.turn.initiated_by === "owner" ? <div className="owner-message"><strong>You</strong><p>{detail.turn.instruction}</p><span>instruction</span></div> : <div className="manager-trigger"><Activity size={13} /><span>Proactive executive review of worker activity through event #{detail.turn.trigger_event_sequence}</span></div>}
        <div className="crew-message"><span className="assistant-avatar"><Bot size={16} /></span><div><strong>{executiveAgent?.name ?? "Project executive"}</strong><p>{exchangeMessage(detail, exchange)}</p>
          <div className="receipt-line"><ShieldCheck size={13} />{exchange?.status ?? detail.turn.status} · frozen event cut #{detail.turn.as_of_event_sequence}</div>
          {detail.turn.citations.length > 0 && <div className="citation-list" aria-label="Canonical answer citations">{detail.turn.citations.map((citation) => <span key={citation.ref}>{citation.label} · r{citation.entity_revision}</span>)}</div>}
          {detail.turn.interpretation.disposition === "clarify" && <ManagerDecisionCard detail={detail} actionable={turns.at(-1)?.turn.id === detail.turn.id} busy={busy} mutable={mutable} choose={(choice) => void answerManagerChoice(detail, choice)} />}
          {exchange && exchange.proposal_ids.some((id) => pendingProposalIDs.has(id)) && (() => { const count = exchange.proposal_ids.filter((id) => pendingProposalIDs.has(id)).length; return <button className="proposal-ready" onClick={openDecisions}><ClipboardCheck size={15} /><span>{count} proposal{count === 1 ? " is" : "s are"} ready. Review exactly what will change.</span><ChevronRight size={16} /></button>; })()}
          {executiveRun && ["requested", "starting", "active", "blocked", "stopping"].includes(executiveRun.status) && <button className="secondary-button executive-run-button" onClick={() => selectRun(executiveRun)}><TerminalSquare size={14} />Inspect live executive session</button>}
        </div></div>
      </div>;
      })}</div>}
      <form className="composer" onSubmit={createWork}>
        <textarea value={instruction} onChange={(event) => setInstruction(event.target.value)} maxLength={4096} placeholder="Ask for status, give direction, or request a change…" aria-label="Message to project executive" />
        <div className="composer-footer"><span className="executive-boundary"><ShieldCheck size={13} />Proposals require explicit acceptance</span><span>{instruction.length}/4096</span><button className="submit-intent" disabled={!mutable || busy || !instruction.trim() || !data.project}>{busy ? <LoaderCircle className="spin" size={17} /> : <Send size={17} />}Send</button></div>
      </form>
      {notice && <div className="notice" role="status"><CheckCircle2 size={17} />{notice}</div>}
      <div className="effect-note"><ShieldCheck size={16} /><span><strong>Conversation is not authority.</strong> The executive may use read-only Crewfold tools and submit typed proposals. Only explicit proposal acceptance or an existing deterministic policy can commit work.</span></div>
    </section>
    <aside className="right-stack">
      <section className="panel metric-panel"><div className="panel-label"><Workflow size={16} />Current implementation</div><div className="metric-row"><div><strong>{openTasks.length}</strong><span>open tasks</span></div><div><strong>{activeRuns.length}</strong><span>worker runs</span></div><button onClick={openDecisions} aria-label="Open decisions"><strong>{pendingConsequentialDecisions(data)}</strong><span>decisions</span><ChevronRight size={14} /></button></div></section>
      <section className="panel capacity-panel"><div className="panel-label"><Users size={16} />Execution capacity</div><div className="capacity-summary"><strong>{implementationAgents.length} implementation {implementationAgents.length === 1 ? "agent" : "agents"} configured</strong><span>{activeRuns.length} active {activeRuns.length === 1 ? "run" : "runs"} · {busyWorkerIDs.size} busy {busyWorkerIDs.size === 1 ? "agent" : "agents"} · {idleWorkers.length} idle · {readyTasks.length} tasks ready now</span></div>{dependencyWaiting.length > 0 && <p><GitBranch size={14} />{dependencyWaiting.length} task{dependencyWaiting.length === 1 ? " is" : "s are"} waiting for prerequisite work, so another agent would not make {dependencyWaiting.length === 1 ? "it" : "them"} runnable yet.</p>}<button className="secondary-button compact" onClick={openCrew}><Users size={14} />Inspect configured crew</button></section>
      <section className="panel compact-list attention-list"><div className="panel-title"><h2>Agents and launch attention</h2><button onClick={() => void reload()} aria-label="Refresh workbench"><RefreshCw size={15} /></button></div>{attentionRuns.length === 0 ? <EmptyState icon={Bot} title="No worker run needs attention" detail="There is no live, failed, unresolved, or changes-requested implementation run in this project." /> : attentionRuns.slice(0, 5).map((run) => { const task = data.tasks.find((detail) => detail.task.id === run.task_id)?.task; const agent = data.agents.find((candidate) => candidate.id === run.agent_id); return <button className="list-row" key={run.id} onClick={() => selectRun(run)}><span className="row-icon"><Bot size={16} /></span><span><strong>{task?.title ?? "Assigned work"}</strong><small>{agent?.name ?? "Agent"} · {run.status === "review" && task?.status === "changes_requested" ? task.blocked_reason || "completion needs a reviewed retry" : run.status === "start_failed" ? run.failure_message ?? "launch failed; inspect and retry" : run.status === "failed" ? "inspect failure output" : run.status === "lost" ? "runtime outcome is unknown; owner resolution is required" : run.status.replaceAll("_", " ")}</small></span><StatusPill value={run.status === "review" && task?.status === "changes_requested" ? "changes requested" : run.status} /></button>; })}</section>
    </aside>
    <section className="panel task-strip full-span"><div className="panel-title"><div><h2>Implementation work</h2><p>{openTasks.length ? "Open tasks in their current canonical state." : projectTasks.length ? "All accepted tasks are currently terminal." : "Accepted executive proposals will create implementation work here."}</p></div><span>{openTasks.length} open · {projectTasks.length} total</span></div>{projectTasks.length === 0 ? <EmptyState icon={ListChecks} title="No implementation work accepted yet" detail="Ask the executive for a bounded plan or change, then review the exact proposal in Decisions." /> : <div className="task-cards">{(openTasks.length ? openTasks : projectTasks).slice(0, 6).map((detail) => { const waiting = detail.task.status === "ready" && !detail.readiness.ready; return <button key={detail.task.id} className="task-card" onClick={() => selectTask(detail)}><div><StatusPill value={waiting ? "waiting" : detail.task.status} /><span className="priority">P{detail.task.priority}</span></div><strong>{detail.task.title}</strong><small>{waiting ? readableTaskReadiness(detail, projectTasks) : `${data.agents.find((agent) => agent.id === detail.task.assigned_agent_id)?.name ?? (["completed", "failed", "cancelled"].includes(detail.task.status) ? "Assignment released" : "Unassigned")} · updated ${displayTime(detail.task.updated_at)}`}</small><ChevronRight size={16} /></button>; })}</div>}</section>
  </div>;
}

function GraphView({ data, selectTask }: { data: WorkbenchData; selectTask: (task: TaskDetail) => void }) {
  const implementationTasks = data.tasks.filter(({ task }) => task.id !== data.executive?.planning_task_id);
  const counts = {
    ready: implementationTasks.filter(({ task }) => ["ready", "assigned"].includes(task.status)).length,
    active: implementationTasks.filter(({ task }) => ["active", "review"].includes(task.status)).length,
    blocked: implementationTasks.filter(({ task }) => task.status === "blocked").length,
    completed: implementationTasks.filter(({ task }) => task.status === "completed").length,
  };
  const stableTaskOrder = (left: TaskDetail, right: TaskDetail) => left.task.priority - right.task.priority || left.task.title.localeCompare(right.task.title) || left.task.id.localeCompare(right.task.id);
  const taskIDs = new Set(implementationTasks.map(({ task }) => task.id));
  const remainingDependencies = new Map(implementationTasks.map((detail) => [detail.task.id, detail.dependencies.filter((dependency) => taskIDs.has(dependency.depends_on_task_id)).length]));
  const dependents = new Map<string, TaskDetail[]>();
  for (const detail of implementationTasks) for (const dependency of detail.dependencies) if (taskIDs.has(dependency.depends_on_task_id)) dependents.set(dependency.depends_on_task_id, [...(dependents.get(dependency.depends_on_task_id) ?? []), detail]);
  const available = implementationTasks.filter((detail) => remainingDependencies.get(detail.task.id) === 0).sort(stableTaskOrder);
  const ordered: TaskDetail[] = [];
  while (available.length) {
    const next = available.shift()!;
    ordered.push(next);
    for (const dependent of dependents.get(next.task.id) ?? []) {
      const remaining = (remainingDependencies.get(dependent.task.id) ?? 1) - 1;
      remainingDependencies.set(dependent.task.id, remaining);
      if (remaining === 0) { available.push(dependent); available.sort(stableTaskOrder); }
    }
  }
  for (const detail of implementationTasks.sort(stableTaskOrder)) if (!ordered.some((candidate) => candidate.task.id === detail.task.id)) ordered.push(detail);
  return <section className="panel page-panel"><div className="page-heading"><div><div className="eyebrow"><Network size={13} />Canonical implementation graph</div><h1>Work graph</h1><p>Accepted tasks in prerequisite order, with assignments and the exact reason each item can or cannot progress.</p></div><span>{implementationTasks.length} {implementationTasks.length === 1 ? "task" : "tasks"}</span></div>
    {implementationTasks.length === 0 ? <EmptyState icon={Workflow} title="No implementation graph yet" detail="Ask the project executive for a plan, then accept its exact proposal in Decisions." /> : <><div className="work-overview" aria-label="Project work summary"><div><span>Ready</span><strong>{counts.ready}</strong><small>can be scheduled</small></div><div><span>In progress</span><strong>{counts.active}</strong><small>working or review</small></div><div className={counts.blocked ? "warn" : ""}><span>Blocked</span><strong>{counts.blocked}</strong><small>needs a prerequisite or decision</small></div><div><span>Done</span><strong>{counts.completed}</strong><small>completed tasks</small></div></div><div className="work-list">{ordered.map((detail, index) => {
      const dependencyTasks = detail.dependencies.map((dependency) => implementationTasks.find(({ task }) => task.id === dependency.depends_on_task_id)?.task).filter((task): task is Task => Boolean(task));
      const agent = data.agents.find((candidate) => candidate.id === detail.task.assigned_agent_id);
      const run = data.runs.filter((candidate) => candidate.task_id === detail.task.id).sort((left, right) => right.updated_at.localeCompare(left.updated_at))[0];
      const progress = detail.task.status === "completed" ? "Completed" : detail.task.status === "blocked" ? detail.task.blocked_reason || readableTaskReadiness(detail, implementationTasks) : detail.readiness.ready ? "Ready for Crewfold to schedule" : readableTaskReadiness(detail, implementationTasks);
      return <button className="work-item" key={detail.task.id} onClick={() => selectTask(detail)}><span className="work-order">{index + 1}</span><div className="work-item-main"><div><strong>{detail.task.title}</strong><StatusPill value={detail.task.status} /></div>{detail.task.description && <p>{detail.task.description}</p>}<div className="work-progress"><span className={detail.task.status === "blocked" ? "warn" : ""}>{progress}</span>{agent && <span><Bot size={13} />{agent.name}</span>}{run && <span><Activity size={13} />run {run.status.replaceAll("_", " ")}</span>}</div>{dependencyTasks.length > 0 && <div className="dependency-line"><GitBranch size={14} /><span>After {dependencyTasks.map((task) => `“${task.title}”`).join(", ")}</span></div>}</div><ChevronRight size={18} /></button>;
    })}</div></>}
  </section>;
}

function CrewView({ data, apiBase, csrf, reload, selectAgent, mutable }: { data: WorkbenchData; apiBase: string; csrf: string; reload: () => Promise<void>; selectAgent: (agent: Agent) => void; mutable: boolean }) {
  const executiveAgent = data.executive ? data.agents.find((agent) => agent.id === data.executive?.agent_id) : undefined;
  const workers = data.agents.filter((agent) => agent.id !== executiveAgent?.id);
  const activeWorkers = workers.filter((agent) => agent.enabled);
  const defaultWorker = activeWorkers[0] ?? executiveAgent;
  const [adding, setAdding] = useState(false);
  const [name, setName] = useState("");
  const [provider, setProvider] = useState(defaultWorker?.provider ?? "codex");
  const [runtime, setRuntime] = useState(defaultWorker?.runtime ?? "herdr");
  const [maxConcurrency, setMaxConcurrency] = useState(2);
  const [disableID, setDisableID] = useState("");
  const [busy, setBusy] = useState(false);
  const [feedback, setFeedback] = useState("");
  const configure = async (payload: Record<string, unknown>) => {
    if (!data.workspace || !data.project || !data.executive) return;
    setBusy(true); setFeedback("");
    try {
      await rpc(apiBase, csrf, "owner.crew.configure", { workspace: data.workspace.id, project: data.project.id, expected_binding_revision: data.executive.revision, idempotency_key: newKey("crew"), ...payload });
      setFeedback(payload.action === "add" ? `${String(payload.name)} is now an authorized implementation worker.` : "The worker was disabled and removed from the executive’s launch authority.");
      setAdding(false); setDisableID(""); setName("");
      await reload();
    } catch (reason) { setFeedback(reason instanceof Error ? reason.message : "Crew configuration failed before its authority change committed."); }
    finally { setBusy(false); }
  };
  const card = (agent: Agent, kind: "executive" | "worker") => {
    const run = latestRunForAgent(data.runs, agent.id);
    const profile = data.launchProfiles.find((candidate) => candidate.agent_id === agent.id && candidate.status === "active" && !candidate.purpose?.includes("executive"));
    return <article className={`crew-card ${kind}${agent.enabled ? "" : " disabled"}`} key={agent.id}><div className="crew-card-top"><span className="agent-avatar">{kind === "executive" ? <Sparkles size={20} /> : <Code2 size={20} />}</span><StatusPill value={run?.status ?? (agent.enabled ? "ready" : "disabled")} /></div><span className="crew-kind">{kind === "executive" ? "Project direction" : agent.enabled ? "Authorized implementation" : "Disabled implementation"}</span><h2>{agent.name}</h2><p>{kind === "executive" ? "Talks with you, reviews canonical project state, asks consequential questions, and submits typed proposals. It cannot accept its own proposals or edit the project." : agent.enabled ? "Executes tasks only after accepted graph changes and deterministic scheduling make them ready." : "Retained as canonical history. It cannot receive new work or be selected by the executive."}</p><dl className="fact-list"><div><dt>Provider</dt><dd>{agent.provider}</dd></div><div><dt>Runtime</dt><dd>{agent.runtime}</dd></div><div><dt>Concurrency</dt><dd>{agent.max_concurrency}</dd></div><div><dt>{kind === "worker" ? "Launch authority" : "Latest session"}</dt><dd>{kind === "worker" ? profile ? "active" : "none" : run?.status.replaceAll("_", " ") ?? "none"}</dd></div></dl><div className="crew-card-actions"><button className="secondary-button" onClick={() => selectAgent(agent)}><Search size={15} />Inspect {kind === "executive" ? "executive" : "worker"}{run ? " session" : ""}</button>{kind === "worker" && agent.enabled && <button className="danger-button" disabled={!mutable || busy || activeWorkers.length <= 1} title={activeWorkers.length <= 1 ? "Add a replacement before disabling the final worker." : "Disable this worker after its accepted work is complete."} onClick={() => setDisableID(agent.id)}><XCircle size={15} />Disable worker</button>}</div>{disableID === agent.id && <div className="crew-confirm" role="alert"><strong>Remove {agent.name} from future work?</strong><p>Crewfold will first prove this worker owns no accepted or live work, then replace the executive’s exact grant and disable this worker. It will not stop or reassign work implicitly.</p><div><button className="secondary-button" onClick={() => setDisableID("")}>Cancel</button><button className="danger-button" disabled={busy} onClick={() => void configure({ action: "disable", agent: agent.id })}>{busy ? <LoaderCircle className="spin" size={14} /> : <XCircle size={14} />}Disable exactly</button></div></div>}</article>;
  };
  return <section className="panel page-panel"><div className="page-heading"><div><div className="eyebrow"><Users size={13} />Who does what</div><h1>Crew</h1><p>The executive coordinates and proposes. Workers implement accepted tasks. Crewfold—not either agent—is the authority that records state and applies permitted effects.</p></div><span>{data.agents.length} agents</span></div>
    <div className="configuration-gap" role="note"><Settings size={17} /><div><strong>You own the implementation crew</strong><p>Adding or disabling a worker replaces the executive’s exact launch grant as one reviewed configuration change. It does not create tasks, start agents, bypass dependencies, or silently move accepted work.</p></div><button className="primary-button compact" disabled={!mutable || busy || !data.executive} onClick={() => setAdding(!adding)}><Plus size={15} />Add worker</button></div>
    {adding && <form className="crew-editor" onSubmit={(event) => { event.preventDefault(); void configure({ action: "add", name, provider, runtime, max_concurrency: maxConcurrency }); }}><div><div className="eyebrow">New implementation authority</div><h2>Add a worker</h2><p>This creates one enabled agent, one immutable project launch profile, and a replacement executive grant that may assign accepted work to it.</p></div><label><span>Worker name</span><input required pattern="[a-z][a-z0-9-]{0,62}" value={name} onChange={(event) => setName(event.target.value)} placeholder="reviewer" /></label><label><span>Provider</span><select value={provider} onChange={(event) => setProvider(event.target.value)}><option value="codex">Codex subscription</option><option value="claude">Claude</option></select></label><label><span>Runtime</span><select value={runtime} onChange={(event) => setRuntime(event.target.value)}><option value="herdr">Herdr interactive</option><option value="direct">Direct headless</option></select></label><label><span>Maximum concurrent runs</span><input type="number" min={1} max={100} value={maxConcurrency} onChange={(event) => setMaxConcurrency(Number(event.target.value))} /></label><div className="crew-editor-effect"><ShieldCheck size={16} /><span><strong>Exact effect</strong> No implementation starts. The project executive may use this worker only in a future typed proposal that you accept.</span></div><div className="row-actions"><button type="button" className="secondary-button" onClick={() => setAdding(false)}>Cancel</button><button className="primary-button" disabled={busy || !name}>{busy ? <LoaderCircle className="spin" size={15} /> : <Plus size={15} />}Authorize worker</button></div></form>}
    {feedback && <div className={feedback.includes("now an authorized") || feedback.includes("was disabled") ? "inline-success" : "global-error"}>{feedback.includes("now an authorized") || feedback.includes("was disabled") ? <CheckCircle2 size={16} /> : <AlertCircle size={16} />}<span>{feedback}</span></div>}
    {data.agents.length === 0 ? <EmptyState icon={Bot} title="No agents configured" detail="Onboarding creates a project executive and the first implementation worker." /> : <><div className="crew-section-label"><Sparkles size={15} /><div><strong>Project executive</strong><small>Your persistent conversation, backed by short-lived provider sessions.</small></div></div><div className="crew-grid focused">{executiveAgent ? card(executiveAgent, "executive") : <div className="role-missing"><AlertCircle size={18} /><span>The exact executive binding is unavailable. Refresh before directing work.</span></div>}</div><div className="crew-section-label"><Code2 size={15} /><div><strong>Implementation workers</strong><small>Agents that execute accepted and ready work.</small></div></div>{workers.length ? <div className="crew-grid">{workers.map((agent) => card(agent, "worker"))}</div> : <EmptyState icon={Code2} title="No implementation worker" detail="Add a worker definition before accepting executable work." />}</>}
  </section>;
}

function supervisorResponseLabel(action?: SupervisorAction, executive = false) {
  if (!action) return "Supervisor action";
  if (executive && action.response === "request_owner" && action.condition === "failed") return "Executive review session failed";
  if (action.response === "request_owner") return action.condition === "failed" ? "Acknowledge a failed run" : "Owner acknowledgement required";
  if (action.response === "resume_run") return "Resume a blocked run";
  return action.response.replaceAll("_", " ");
}

function supervisorDecisionLabels(action?: SupervisorAction, executive = false) {
  if (action?.response === "resume_run") return { allow: "Resume this run", deny: "Keep blocked" };
  if (executive && action?.response === "request_owner") return { allow: "Mark reviewed", deny: "Leave unresolved" };
  if (action?.response === "request_owner") return { allow: "Mark seen", deny: "Leave open" };
  return { allow: "Allow", deny: "Deny" };
}

function supervisorConsequence(action?: SupervisorAction, executive = false) {
  if (action?.response === "resume_run") return "Resuming only releases this exact blocked run to continue in its existing runtime. It does not repair Herdr, replace the execution profile, or create a fresh sandbox.";
  if (executive && action?.response === "request_owner") return "Marking this reviewed only records that you saw the failed executive session. It does not change implementation work, retry the review, or resume a worker.";
  if (action?.response === "request_owner") return "Acknowledging records that you reviewed this terminal failure. It does not retry the run or change the failed task.";
  return "Allowing commits only the exact requested supervisor response shown above.";
}

function snapshotText(action: SupervisorAction, name: string) {
  const value = action.constraint_snapshot[name];
  return typeof value === "string" && value.trim() ? value : "";
}

function proposalTaskRef(value?: { task_id?: string; proposal_task_key?: string }) {
  return value?.proposal_task_key ? `new task “${value.proposal_task_key}”` : value?.task_id ? `task ${value.task_id}` : "an unspecified task";
}

function proposalActionDescription(action: ManagerProposalAction) {
  if (action.create_task) return `Create task “${action.create_task.title}” at priority ${action.create_task.priority} using the frozen launch profile.`;
  if (action.add_dependency) return `Make ${proposalTaskRef(action.add_dependency.task)} depend on ${proposalTaskRef(action.add_dependency.depends_on)}.`;
  if (action.declare_claim_requirement) return `Require ${proposalTaskRef(action.declare_claim_requirement.task)} to hold the ${action.declare_claim_requirement.kind} claim on “${action.declare_claim_requirement.target}” in ${action.declare_claim_requirement.mode} mode.`;
  if (action.assign_task) return `Assign ${proposalTaskRef(action.assign_task.task)} through the frozen launch profile.`;
  if (action.request_review) return `Create review “${action.request_review.title}” for ${proposalTaskRef(action.request_review.task)}.`;
  if (action.request_action) return `Request ${action.request_action.response.replaceAll("_", " ")} because: ${action.request_action.reason}`;
  return action.type.replaceAll("_", " ");
}

function ProposalCard({ proposal, busy, mutable, decide, requestChanges }: { proposal: ManagerProposal; busy: boolean; mutable: boolean; decide: (decision: "accept" | "reject") => void; requestChanges?: (instruction: string) => void }) {
  const [editing, setEditing] = useState(false);
  const [changeRequest, setChangeRequest] = useState("");
  const tasks = proposal.actions.filter((action) => action.create_task).map((action) => action.create_task!);
  const dependencies = proposal.actions.filter((action) => action.add_dependency).map((action) => action.add_dependency!);
  const taskTitles = new Map(tasks.map((task) => [task.task_key, task.title]));
  const readableTaskRef = (value?: { task_id?: string; proposal_task_key?: string }) => value?.proposal_task_key ? `“${taskTitles.get(value.proposal_task_key) ?? value.proposal_task_key}”` : value?.task_id ? `existing task ${value.task_id}` : "an unspecified task";
  const invalid = proposal.status === "invalid";
  const pending = proposal.status === "pending";
  return <article className={`decision-card proposal-card ${invalid ? "invalid" : ""}`}>
    <header><span className="row-icon">{invalid ? <AlertCircle size={18} /> : <Workflow size={18} />}</span><div><strong>{proposal.summary}</strong><small>{invalid ? "Rejected by Crewfold validation; no owner action is required" : `Task decomposition · frozen at event #${proposal.as_of_event_sequence}`}</small></div><StatusPill value={proposal.status} /></header>
    {!invalid && <div className="proposal-impact"><strong>If accepted</strong><span>Create {tasks.length} implementation task{tasks.length === 1 ? "" : "s"}, add {dependencies.length} prerequisite link{dependencies.length === 1 ? "" : "s"}, then let Crewfold schedule only ready work through the frozen worker profile.</span></div>}
    {tasks.length > 0 && <div className="proposal-task-list"><h3>{tasks.length} proposed implementation task{tasks.length === 1 ? "" : "s"}</h3>{tasks.map((task, index) => <div key={task.task_key}><span>{index + 1}</span><div><strong>{task.title}</strong>{task.description && <p>{task.description}</p>}<small>Priority {task.priority} · accepted worker profile</small></div></div>)}</div>}
    {!invalid && dependencies.length > 0 && <div className="proposal-dependencies"><h3>Execution order</h3>{dependencies.map((dependency, index) => <div key={`${proposal.id}-dependency-${index}`}><span>{readableTaskRef(dependency.depends_on)}</span><ChevronRight size={15} /><strong>{readableTaskRef(dependency.task)}</strong></div>)}</div>}
    {invalid && <div className="invalid-proposal-summary"><AlertCircle size={17} /><div><strong>This draft never reached owner review.</strong><p>{proposal.validation_issues.some((issue) => issue.code === "invalid_claim_requirement") ? "The executive requested coordination claims that were not present in its frozen grant. Crewfold rejected the draft and changed no project state." : "Crewfold rejected this draft because one or more typed operations were outside its exact contract. No project state changed."}</p></div></div>}
    <details className="technical-details"><summary>{invalid ? "Show validation details" : `Show ${proposal.actions.length} exact typed operations`}</summary>{!invalid && <ol>{proposal.actions.map((action) => <li key={action.id ?? `${proposal.id}-${action.ordinal}`}><span>{action.ordinal + 1}</span>{proposalActionDescription(action)}</li>)}</ol>}{proposal.validation_issues.length > 0 && <ul>{proposal.validation_issues.map((issue, index) => <li key={`${issue.path}-${issue.code}-${index}`}>{issue.message}</li>)}</ul>}</details>
    {!invalid && <p className="decision-consequence"><strong>Authority boundary:</strong> Accepting applies exactly this frozen revision transactionally. It does not let the executive edit files or launch arbitrary work.</p>}
    {pending && editing && <div className="proposal-revision"><label><strong>What should change?</strong><span>Describe the task, dependency, worker, priority, or budget changes. The current draft stays inert; your executive must submit a new typed revision for review.</span><textarea value={changeRequest} onChange={(event) => setChangeRequest(event.target.value)} maxLength={2048} placeholder="For example: split verification from implementation, make it depend on the UI task, and assign it to reviewer." /></label><div className="row-actions"><button className="secondary-button" disabled={busy} onClick={() => { setEditing(false); setChangeRequest(""); }}>Cancel</button><button className="primary-button compact" disabled={!mutable || busy || !changeRequest.trim()} onClick={() => requestChanges?.(changeRequest.trim())}>{busy ? <LoaderCircle className="spin" size={15} /> : <MessageSquareText size={15} />}Send revision request</button></div></div>}
    {pending && !editing ? <div className="row-actions"><button className="secondary-button" disabled={!mutable || busy} onClick={() => decide("reject")}><XCircle size={15} />Reject proposal</button>{requestChanges && <button className="secondary-button" disabled={!mutable || busy} onClick={() => setEditing(true)}><MessageSquareText size={15} />Request changes</button>}<button className="primary-button compact" disabled={!mutable || busy || proposal.validation_issues.some((issue) => issue.severity === "error")} onClick={() => decide("accept")}>{busy ? <LoaderCircle className="spin" size={15} /> : <Check size={15} />}Accept these tasks</button></div> : !pending && !invalid && <div className="decision-record"><CheckCircle2 size={16} /><span><strong>Recorded owner decision</strong>{proposal.decision_note ?? proposal.status.replaceAll("_", " ")} · {displayTime(proposal.decided_at ?? proposal.updated_at)}</span></div>}
  </article>;
}

function DecisionsView({ data, apiBase, csrf, reload, mutable }: { data: WorkbenchData; apiBase: string; csrf: string; reload: () => Promise<void>; mutable: boolean }) {
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const [retiredRuns, setRetiredRuns] = useState<Set<string>>(new Set());
  const approvals = data.approvals.filter((item) => !data.project || item.project_id === data.project.id);
  const proposals = data.proposals.filter((item) => !data.project || item.project_id === data.project.id);
  const decide = async (approval: Approval, action: SupervisorAction | undefined, decision: "allow" | "deny") => {
    if (!mutable || !data.workspace) return;
    const taskTitle = data.tasks.find((item) => item.task.id === action?.task_id)?.task.title ?? "the governed action";
    const acknowledgement = action?.response === "request_owner";
    const decisionNote = decision === "allow" ? acknowledgement ? `Acknowledged ${action?.condition.replaceAll("_", " ") ?? "supervisor condition"} for ${taskTitle}` : `Allowed ${action?.response.replaceAll("_", " ") ?? "supervisor action"} for ${taskTitle}` : `Dismissed ${action?.response.replaceAll("_", " ") ?? "supervisor action"} for ${taskTitle}`;
    setBusy(approval.id); setError("");
    try {
      await rpc(apiBase, csrf, `approval.${decision}`, { workspace: data.workspace.id, approval: approval.id, expected_revision: approval.revision, decision_note: decisionNote, idempotency_key: newKey(`approval-${decision}`) });
      await reload();
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Decision could not be committed."); }
    finally { setBusy(""); }
  };
  const decideProposal = async (proposal: ManagerProposal, decision: "accept" | "reject") => {
    if (!mutable || !data.workspace) return;
    setBusy(proposal.id); setError("");
    try {
      await rpc(apiBase, csrf, `proposal.${decision}`, { workspace: data.workspace.id, proposal: proposal.id, expected_revision: proposal.revision, decision_note: decision === "accept" ? "Accepted the exact reviewed executive proposal." : "Rejected the exact reviewed executive proposal.", idempotency_key: newKey(`proposal-${decision}`) });
      await reload();
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Executive proposal decision could not be committed."); }
    finally { setBusy(""); }
  };
  const requestProposalChanges = async (proposal: ManagerProposal, instruction: string) => {
    if (!mutable || !data.workspace || !data.project) return;
    setBusy(proposal.id); setError("");
    try {
      const conversation = await loadOwnerConversation(apiBase, data.workspace.id, data.project.id);
      const conversationID = conversation.turns.at(-1)?.conversation.id;
      if (!conversationID) throw new Error("The durable project-executive conversation is unavailable; refresh before requesting a revision.");
      await rpc(apiBase, csrf, "proposal.reject", { workspace: data.workspace.id, proposal: proposal.id, expected_revision: proposal.revision, decision_note: "Owner requested changes through the durable project executive.", idempotency_key: newKey("proposal-revise-reject") });
      await submitOwnerIntent(apiBase, csrf, { workspace: data.workspace.id, project: data.project.id, conversation_id: conversationID, instruction: `Revise the rejected proposal “${proposal.summary}”. Owner changes: ${instruction}. Preserve only the still-valid parts, do not execute anything, and submit a new exact typed proposal for owner review.`, idempotency_key: newKey("proposal-revise-turn") });
      await reload();
    } catch (reason) { setError(reason instanceof Error ? reason.message : "The proposal revision request could not be recorded."); }
    finally { setBusy(""); }
  };
  const pendingProposals = proposals.filter((item) => item.status === "pending");
  const proposalHistory = proposals.filter((item) => item.status !== "pending");
  const pendingApprovals = approvals.filter((item) => item.status === "pending");
  const approvalHistory = approvals.filter((item) => item.status !== "pending");
  const actionForApproval = (approval: Approval) => data.supervisorActions.find((item) => item.id === approval.action_id);
  const pendingNotices = pendingApprovals.filter((approval) => actionForApproval(approval)?.response === "request_owner");
  const pendingEffectApprovals = pendingApprovals.filter((approval) => actionForApproval(approval)?.response !== "request_owner");
  const lostRuns = data.runs.filter((run) => run.status === "lost");
  const pendingDecisions = pendingEffectApprovals.length + pendingProposals.length + lostRuns.length;
  const pendingReview = pendingDecisions + pendingNotices.length;
  const resolveLost = async (run: Run) => {
    if (!mutable || !data.workspace || !retiredRuns.has(run.id)) return;
    setBusy(run.id); setError("");
    try {
      await rpc(apiBase, csrf, "run.lost.resolve", { workspace: data.workspace.id, run: run.id, expected_revision: run.revision, note: "Owner confirmed the native runtime has ended before releasing Crewfold capacity.", runtime_retired_confirmed: true, idempotency_key: newKey("resolve-lost") });
      setRetiredRuns((current) => { const next = new Set(current); next.delete(run.id); return next; });
      await reload();
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Lost runtime could not be resolved."); }
    finally { setBusy(""); }
  };
  const lostRunCard = (run: Run) => {
    const task = data.tasks.find((item) => item.task.id === run.task_id)?.task;
    const agent = data.agents.find((item) => item.id === run.agent_id);
    const executive = run.task_id === data.executive?.planning_task_id;
    const confirmed = retiredRuns.has(run.id);
    return <article className="decision-card lost-runtime-card" key={run.id}>
      <header><span className="row-icon"><AlertTriangle size={18} /></span><div><strong>{executive ? "Confirm the interrupted executive session has ended" : "Confirm the lost worker runtime has ended"}</strong><small>{executive ? "Project direction session" : task?.title ?? "Exact lost implementation run"}</small></div><StatusPill value="owner confirmation required" /></header>
      <div className="approval-effect"><strong>What you are deciding</strong><p>{executive ? "Crewfold no longer trusts this short-lived executive session’s native runtime. Confirming releases only its retained binding and capacity so the durable conversation and queued review can continue. It does not change project tasks or accept any proposal, and it does not stop the native process." : "Crewfold no longer trusts this worker runtime’s identity or outcome. Confirming transitions the lost run to failed, releases its retained binding and capacity, and leaves the task blocked for an explicit recovery plan. It does not stop the native process."}</p></div>
      <dl className="decision-facts"><div><dt>Agent</dt><dd>{agent?.name ?? (executive ? "Project executive" : "Implementation worker")}</dd></div><div><dt>Current run state</dt><dd>Lost · capacity retained</dd></div><div><dt>Recorded diagnosis</dt><dd>{run.failure_message ?? run.failure_code ?? "Runtime identity or outcome is unknown"}</dd></div></dl>
      <label className="retirement-confirmation"><input type="checkbox" checked={confirmed} onChange={(event) => setRetiredRuns((current) => { const next = new Set(current); if (event.target.checked) next.add(run.id); else next.delete(run.id); return next; })} /><span><strong>I independently confirmed the Herdr pane or native process has ended.</strong><small>Do not confirm this while the process may still be writing.</small></span></label>
      <div className="row-actions"><button className="primary-button compact" disabled={!mutable || busy === run.id || !confirmed} onClick={() => void resolveLost(run)}>{busy === run.id ? <LoaderCircle className="spin" size={15} /> : <Check size={15} />}Release the lost runtime</button></div>
    </article>;
  };
  const approvalCard = (approval: Approval) => {
    const action = actionForApproval(approval);
    const task = data.tasks.find((item) => item.task.id === action?.task_id)?.task;
    const run = data.runs.find((item) => item.id === action?.run_id);
    const agent = data.agents.find((item) => item.id === action?.agent_id);
    const executiveTarget = Boolean(run && run.task_id === data.executive?.planning_task_id);
    const acknowledgement = action?.response === "request_owner";
    const labels = supervisorDecisionLabels(action, executiveTarget);
    const blockedQuestion = action ? snapshotText(action, "blocked_question") || snapshotText(action, "question") : "";
    return <article className={`decision-card ${acknowledgement ? "notice-card" : ""}`} key={approval.id}>
      <header><span className="row-icon"><ShieldCheck size={18} /></span><div><strong>{supervisorResponseLabel(action, executiveTarget)}</strong><small>{executiveTarget ? "Project direction review; implementation work is unchanged" : task?.title ?? "Exact governed target unavailable in this bounded page"}</small></div><StatusPill value={approval.status} /></header>
      {action ? <><div className="approval-effect"><strong>{acknowledgement ? "What this records" : "What you are deciding"}</strong><p>{supervisorConsequence(action, executiveTarget)}</p></div><dl className="decision-facts"><div><dt>Trigger</dt><dd>{action.condition.replaceAll("_", " ")}</dd></div><div><dt>{acknowledgement ? "Record" : "Requested action"}</dt><dd>{acknowledgement ? "Owner reviewed the failure" : action.response.replaceAll("_", " ")}</dd></div>{agent && <div><dt>Agent</dt><dd>{agent.name}</dd></div>}{run && <div><dt>Current run state</dt><dd>{run.status.replaceAll("_", " ")}</dd></div>}</dl><details className="technical-details"><summary>{acknowledgement ? "Why this notice exists" : "Why Crewfold paused this effect"}</summary><ul>{action.reasons.map((reason) => <li key={reason}>{reason}</li>)}</ul>{blockedQuestion && <><strong>Agent’s exact blocker</strong><p>{blockedQuestion}</p></>}</details></> : <p className="decision-unavailable">The action detail is outside this bounded page; refresh before deciding.</p>}
      {approval.status === "pending" ? <div className="row-actions"><button className="secondary-button" disabled={!mutable || busy === approval.id || !action} onClick={() => void decide(approval, action, "deny")}><XCircle size={15} />{labels.deny}</button><button className="primary-button compact" disabled={!mutable || busy === approval.id || !action} onClick={() => void decide(approval, action, "allow")}>{busy === approval.id ? <LoaderCircle className="spin" size={15} /> : <Check size={15} />}{labels.allow}</button></div> : <div className="decision-record"><CheckCircle2 size={16} /><span><strong>Recorded owner decision</strong>{action?.decision ?? approval.decision_note ?? approval.status.replaceAll("_", " ")} · {displayTime(approval.decided_at ?? approval.updated_at)}</span></div>}
    </article>;
  };
  return <section className="panel page-panel">
    <div className="page-heading"><div><div className="eyebrow"><ClipboardCheck size={14} />Owner authority boundary</div><h1>Decisions</h1><p>Consequential proposals and policy-gated effects are decisions. Failure acknowledgements that change no project work are separated as notices.</p></div><span>{pendingReview} need{pendingReview === 1 ? "s" : ""} review</span></div>
    {error && <div className="form-error" role="alert"><AlertCircle size={16} />{error}</div>}
    {pendingReview === 0 && <EmptyState icon={ShieldCheck} title="Nothing needs your decision" detail="New executive proposals and policy-gated runtime effects will appear here with a plain-language effect before any action." />}
    {pendingDecisions > 0 && <><div className="section-heading actionable"><div><ClipboardCheck size={18} /><span><strong>Decisions that change work</strong><small>Each item states the exact effect that acceptance applies.</small></span></div><span>{pendingDecisions}</span></div><div className="decision-list">{lostRuns.map(lostRunCard)}{pendingProposals.map((proposal) => <ProposalCard key={proposal.id} proposal={proposal} busy={busy === proposal.id} mutable={mutable} decide={(decision) => void decideProposal(proposal, decision)} requestChanges={(instruction) => void requestProposalChanges(proposal, instruction)} />)}{pendingEffectApprovals.map(approvalCard)}</div></>}
    {pendingNotices.length > 0 && <><div className="section-heading notice"><div><AlertCircle size={18} /><span><strong>Attention, not a project decision</strong><small>These records explain terminal failures. Marking one seen does not retry, resume, reassign, or edit work.</small></span></div><span>{pendingNotices.length}</span></div><div className="decision-list">{pendingNotices.map(approvalCard)}</div></>}
    {(proposalHistory.length > 0 || approvalHistory.length > 0) && <details className="decision-history"><summary><Clock3 size={16} /><span><strong>Earlier decisions and rejected drafts</strong><small>{proposalHistory.length + approvalHistory.length} historical item{proposalHistory.length + approvalHistory.length === 1 ? "" : "s"}; nothing here is awaiting action.</small></span><ChevronDown size={16} /></summary><div className="decision-list">{proposalHistory.map((proposal) => <ProposalCard key={proposal.id} proposal={proposal} busy={false} mutable={false} decide={() => undefined} />)}{approvalHistory.map(approvalCard)}</div></details>}
  </section>;
}

function ActivityView({ data }: { data: WorkbenchData }) {
  return <section className="panel page-panel"><div className="page-heading"><div><div className="eyebrow"><Activity size={13} />Immutable journal</div><h1>Activity</h1><p>Current known event types through sequence {data.highWater}.</p></div><span>{data.events.length} loaded</span></div>{data.events.length === 0 ? <EmptyState icon={GitCommitHorizontal} title="No activity recorded" detail="Committed effects will appear here." /> : <div className="timeline">{[...data.events].reverse().map((event) => <div key={event.event_id}><span className="timeline-dot" /><div><strong>{event.type.replaceAll("_", " ").replaceAll(".", " · ")}</strong><small>{event.entity.type} revision {event.entity.revision} · {displayTime(event.recorded_at)}</small></div><span>#{event.sequence}</span></div>)}</div>}</section>;
}

function InboxView({ data, apiBase, csrf }: { data: WorkbenchData; apiBase: string; csrf: string }) {
  const [agentID, setAgentID] = useState(data.agents[0]?.id ?? "");
  const [items, setItems] = useState<InboxItem[]>([]);
  const [loading, setLoading] = useState(false);
  useEffect(() => { if (!agentID && data.agents[0]) setAgentID(data.agents[0].id); }, [agentID, data.agents]);
  useEffect(() => {
    if (!data.workspace || !agentID) { setItems([]); return; }
    setLoading(true);
    void rpc<{ items: InboxItem[] }>(apiBase, csrf, "inbox.list", { workspace: data.workspace.id, agent: agentID, limit: 50 }).then((result) => setItems(result.items)).catch(() => setItems([])).finally(() => setLoading(false));
  }, [agentID, apiBase, csrf, data.highWater, data.workspace?.id]);
  return <section className="panel page-panel"><div className="page-heading"><div><div className="eyebrow"><Inbox size={13} />Agent communication</div><h1>Inbox</h1><p>Durable questions, handoffs, and messages by exact recipient.</p></div>{data.agents.length > 0 && <select className="inline-select" aria-label="Inbox agent" value={agentID} onChange={(event) => setAgentID(event.target.value)}>{data.agents.map((agent) => <option key={agent.id} value={agent.id}>{agent.name}</option>)}</select>}</div>{loading ? <div className="quiet-line"><LoaderCircle className="spin" size={15} />Loading bounded inbox…</div> : items.length === 0 ? <EmptyState icon={Inbox} title="No messages for this agent" detail="Questions, risks, handoffs, and decision notices will appear here." /> : <div className="message-list">{items.map((item) => <article key={item.message.id}><span className="row-icon"><MessageCircle size={16} /></span><div><header><strong>{item.message.sender_agent_name ?? (item.message.sender_type === "owner" ? "You" : "Crewfold")}</strong><StatusPill value={item.message.kind} /></header><p>{item.message.body}</p><small>{displayTime(item.message.created_at)} · {item.delivery.status} · wake {item.delivery.wake_status}</small></div></article>)}</div>}</section>;
}

function EvidenceView({ data }: { data: WorkbenchData }) {
  return <section className="panel page-panel"><div className="page-heading"><div><div className="eyebrow"><FileCheck2 size={13} />Completion evidence</div><h1>Evidence</h1><p>Canonical checks and freshness tied to current tasks.</p></div><span>{data.checks.length} check runs</span></div>{data.checks.length === 0 ? <EmptyState icon={FileCheck2} title="No evidence collected yet" detail="Check results appear when a configured requirement runs." /> : <div className="table-list">{data.checks.map((item) => <div className="table-row" key={item.run.id}><span className="row-icon"><FileCheck2 size={17} /></span><span><strong>{data.tasks.find((task) => task.task.id === item.run.task_id)?.task.title ?? "Task check"}</strong><small>Updated {displayTime(item.run.updated_at)} · revision {item.run.revision}</small></span><StatusPill value={item.outcome ?? item.requirement_state} /><span>{item.current_freshness?.status ?? item.requirement_state}</span></div>)}</div>}</section>;
}

function BriefingView({ data, apiBase, csrf }: { data: WorkbenchData; apiBase: string; csrf: string }) {
  const [briefing, setBriefing] = useState<Briefing | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const load = async () => {
    if (!data.workspace || !data.project) return;
    setBusy(true); setError("");
    try {
      const result = await rpc<{ briefing: Briefing }>(apiBase, csrf, "briefing.show", { workspace: data.workspace.id, scope_type: "project", scope_identifier: data.project.id });
      setBriefing(result.briefing);
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Briefing could not be generated."); }
    finally { setBusy(false); }
  };
  return <section className="panel page-panel"><div className="page-heading"><div><div className="eyebrow"><BookOpenText size={13} />Bounded understanding</div><h1>Project briefing</h1><p>Decisions, risks, verification gaps, accepted delivery, rationale, and change at one canonical cut.</p></div><button className="primary-button compact" onClick={() => void load()} disabled={busy || !data.project}>{busy ? <LoaderCircle className="spin" size={14} /> : <BookOpenText size={14} />}{briefing ? "Refresh briefing" : "Generate briefing"}</button></div>{error && <div className="form-error" role="alert"><AlertCircle size={16} />{error}</div>}{!briefing ? <EmptyState icon={BookOpenText} title="Briefing is ready on demand" detail="Generate one bounded, provenance-linked summary for the selected project." /> : <><div className="briefing-summary"><StatusPill value={briefing.caught_up ? "current" : "partial"} /><span>Event cut #{briefing.event_cursor}</span><span>{briefing.claims.length} claims · {briefing.byte_size} bytes</span><span>Evaluated {displayTime(briefing.evaluated_at)}</span></div>{briefing.claims.length === 0 ? <EmptyState icon={CheckCircle2} title="No briefing claims" detail="There are no current decisions, risks, gaps, or assessed deliveries in this scope." /> : <div className="briefing-claims">{briefing.claims.map((claim) => <article key={claim.id}><div><StatusPill value={claim.urgency} /><StatusPill value={claim.status} /></div><h2>{claim.kind.replaceAll("_", " ")}</h2><p>{claim.summary}</p></article>)}</div>}{briefing.omitted.length > 0 && <div className="effect-note"><AlertCircle size={16} /><span>{briefing.omitted.map((item) => `${item.count} ${item.section.replaceAll("_", " ")} omitted by ${item.reason.replaceAll("_", " ")}`).join(" · ")}</span></div>}</>}</section>;
}

function HealthView({ apiBase, csrf, status }: { apiBase: string; csrf: string; status: DaemonStatus | null }) {
  const [doctor, setDoctor] = useState<FullDoctor | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const inspect = async () => { setBusy(true); setError(""); try { setDoctor(await rpc<FullDoctor>(apiBase, csrf, "system.doctor.full", {})); } catch (reason) { setError(reason instanceof Error ? reason.message : "Full diagnosis failed."); } finally { setBusy(false); } };
  return <section className="panel page-panel"><div className="page-heading"><div><div className="eyebrow"><Stethoscope size={13} />Local service</div><h1>Health and recovery</h1><p>Read-only physical, canonical, queue, artifact, and resource diagnosis.</p></div><button className="primary-button compact" onClick={() => void inspect()} disabled={busy}>{busy ? <LoaderCircle className="spin" size={14} /> : <HeartPulse size={14} />}Run full doctor</button></div><div className="health-overview"><div><strong>{status?.pid ?? "—"}</strong><span>daemon PID</span></div><div><strong>{Math.floor((status?.uptime_ms ?? 0) / 1000)}s</strong><span>uptime</span></div><div><strong>{status?.codex_tool_network_access ? "enabled" : "disabled"}</strong><span>Codex dependency network</span></div><div><strong>{doctor?.event_sequence ?? "—"}</strong><span>event high-water</span></div><div><strong>{doctor?.status ?? "not run"}</strong><span>full diagnosis</span></div></div><div className="effect-note"><Network size={16} /><span>This service policy permits Codex package and documentation retrieval only inside its workspace-write sandbox. It does not authorize publishing, deployment, credentials, paid services, or external side effects.</span></div>{error && <div className="form-error"><AlertCircle size={16} />{error}</div>}{doctor && <div className="doctor-grid">{doctor.checks.map((check) => <article key={check.code}><StatusPill value={check.status} /><strong>{check.code.replaceAll("_", " ")}</strong><p>{check.summary}</p><small>{check.issue_count} issues</small></article>)}</div>}</section>;
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
  const readableLogs = useMemo(() => [initialLogs, streamRaw].filter(Boolean).join("\n"), [initialLogs, streamRaw]);

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
    <div className="section-title"><h3>Live provider activity</h3><span><CircleDot size={11} />{state}</span></div>
    <p>Codex protocol events are rendered into a readable live stream. Canonical Crewfold state and receipts remain above.</p>
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
    <button type="button" className="secondary-button close-live-activity" onClick={close}><X size={14} />Hide live activity</button>
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
  const [busy, setBusy] = useState(false);
  const currentRun = run ? data.runs.find((candidate) => candidate.id === run.id) ?? run : null;
  const currentTask = currentRun ? data.tasks.find((item) => item.task.id === currentRun.task_id)?.task ?? null : null;
  const executiveRun = Boolean(currentRun && currentRun.task_id === data.executive?.planning_task_id);
  const canRetryReview = currentRun?.status === "review" && currentTask?.status === "changes_requested";
  const canStartFresh = currentRun?.status === "stopped" && currentTask?.status === "assigned";
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
  const retry = async () => { if (!mutable || !currentRun || !currentTask || !data.workspace || currentRun.status !== "start_failed" && !canRetryReview && !canStartFresh) return; setBusy(true); setNotice(""); try { const freshRun = await retryWorkbenchRun(apiBase, csrf, data.workspace.id, currentRun, currentTask); setNotice(canRetryReview ? "Requested changes were reopened on the retained assignment and a fresh run was queued." : canStartFresh ? "A fresh run was queued under the current service, provider, runtime, and network policy." : "A fresh run was requested after the runtime and provider preflight passed."); await reload(); inspectRun(freshRun); } catch (reason) { setNotice(reason instanceof Error ? reason.message : "Retry failed."); } finally { setBusy(false); } };
  const refreshGit = async () => { if (!data.workspace || !data.project) return; setBusy(true); setNotice(""); try { const response = await fetch(`${apiBase}/git?workspace=${encodeURIComponent(data.workspace.id)}&project=${encodeURIComponent(data.project.id)}`, { credentials: "same-origin" }); const result = (await response.json()) as { observations?: Array<Omit<Checkout, "id" | "project_id" | "path" | "write_mode"> & { checkout_id: string }>; error?: { message: string } }; if (!response.ok || !result.observations) throw new Error(result.error?.message ?? "Git observation failed"); const observation = result.observations[0]; const canonical = data.checkouts.find((checkout) => checkout.id === observation?.checkout_id); setGit(observation && canonical ? { ...canonical, ...observation, id: observation.checkout_id } : null); setNotice("Repository status refreshed without persisting source or diff content."); } catch (reason) { setNotice(reason instanceof Error ? reason.message : "Git observation failed."); } finally { setBusy(false); } };
  const agentRun = agent ? latestRunForAgent(data.runs, agent.id) : null;
  return <div className="drawer-scrim" onMouseDown={(event) => { if (event.target === event.currentTarget) close(); }}><aside className={`inspector${terminalOpen ? " live-inspector" : ""}`} aria-label="Canonical inspector"><header><div><div className="eyebrow">Exact inspector</div><h2>{run ? data.agents.find((candidate) => candidate.id === run.agent_id)?.name ?? "Agent run" : agent?.name ?? task?.task.title}</h2></div><IconButton label="Close inspector" onClick={close}><X size={18} /></IconButton></header>
    {currentRun ? <><div className="inspector-status"><StatusPill value={currentRun.status} /><span>{currentRun.provider} through {currentRun.runtime}</span></div><section><h3>{executiveRun ? "Session purpose" : "Assigned work"}</h3><p>{executiveRun ? "Review project direction and new worker activity" : currentTask?.title ?? "Task unavailable in this bounded page"}</p>{executiveRun && <div className="session-explanation"><strong>Why this session appears</strong><span>Crewfold starts one short-lived project-executive session when you send a message or new worker activity needs review. It closes after recording one durable response.</span></div>}</section>{["start_failed", "failed", "lost"].includes(currentRun.status) && <section className="launch-failure" role="alert"><div className="section-title"><h3>{currentRun.status === "start_failed" ? "Launch failed" : currentRun.status === "lost" ? "Runtime outcome is unknown" : "Run failed"}</h3><AlertCircle size={16} /></div><p>{currentRun.failure_message ?? currentRun.failure_code ?? "Inspect the bounded runtime output for the exact provider diagnosis."}</p>{currentRun.status === "start_failed" && <button className="primary-button compact" disabled={!mutable || busy} onClick={() => void retry()}>{busy ? <LoaderCircle className="spin" size={14} /> : <RotateCcw size={14} />}Retry after preflight</button>}</section>}{currentRun.status === "blocked" && <section className="launch-failure review-retry" role="alert"><div className="section-title"><h3>Blocked runtime authority</h3><AlertCircle size={16} /></div><p>Resume continues this exact runtime and sandbox. It is correct only after the blocker was repaired in place.</p><p>If service, provider, runtime, or network policy changed, stop this run first. Once it is durably stopped, Crewfold can launch the retained assignment in a fresh environment.</p></section>}{canStartFresh && <section className="launch-failure review-retry"><div className="section-title"><h3>Launch a fresh environment</h3><RotateCcw size={16} /></div><p>The prior runtime is durably stopped and no longer holds authority. The task's exact assignment is retained.</p><p>This creates one fresh run using the current service, provider, runtime, and network policy.</p><button className="primary-button compact" disabled={!mutable || busy} onClick={() => void retry()}>{busy ? <LoaderCircle className="spin" size={14} /> : <RotateCcw size={14} />}Start fresh run</button></section>}{canRetryReview && <section className="launch-failure review-retry" role="alert"><div className="section-title"><h3>Changes requested</h3><ClipboardCheck size={16} /></div><p>{currentTask?.blocked_reason ?? "The completion did not satisfy the task acceptance evidence."}</p><p>The prior review remains immutable. Retrying reopens this exact assignment and creates a fresh context-bound run.</p><button className="primary-button compact" disabled={!mutable || busy} onClick={() => void retry()}>{busy ? <LoaderCircle className="spin" size={14} /> : <RotateCcw size={14} />}Retry requested changes</button></section>}<section><div className="section-title"><h3>Readable agent activity</h3><Activity size={15} /></div><RuntimeOutput logs={logs} status={currentRun.status} /></section>{terminalOpen && <LiveTerminal key={currentRun.id} apiBase={apiBase} csrf={csrf} workspace={data.workspace?.id ?? ""} run={currentRun} initialLogs={logs} close={() => setTerminalOpen(false)} mutable={mutable} />}{["active", "blocked"].includes(currentRun.status) && <section className="runtime-control"><label htmlFor="runtime-prompt">Send a visible runtime prompt</label><div><input id="runtime-prompt" value={prompt} maxLength={4096} onChange={(event) => setPrompt(event.target.value)} placeholder="Clarify the next observable step…" /><button className="secondary-button" disabled={busy || !prompt.trim()} onClick={() => void sendPrompt()}><Send size={14} />Send</button></div><div className="runtime-buttons">{currentRun.can_attach && !terminalOpen && <button className="secondary-button" disabled={busy} onClick={() => setTerminalOpen(true)}><TerminalSquare size={14} />Open live activity</button>}{currentRun.status === "blocked" && <button className="secondary-button" disabled={busy} onClick={() => void resume()}><RotateCcw size={14} />Resume same runtime</button>}<button className="secondary-button" disabled={busy} onClick={() => void interrupt()}><AlertCircle size={14} />Interrupt</button><button className="danger-button" onClick={() => void stop()} disabled={busy}>{busy ? <LoaderCircle className="spin" size={15} /> : <Square size={15} />}Stop · 5000 ms grace</button></div></section>}{notice && <div className="notice" role="status">{notice}</div>}<footer><span>Revision {currentRun.revision}</span><span>{currentRun.can_attach ? "Live activity and interactive controls available" : currentRun.status === "start_failed" ? "Retry available after diagnosis" : canRetryReview ? "Reviewed retry available" : canStartFresh ? "Fresh launch available on retained assignment" : "Bounded logs only"}</span></footer></> : agent ? <><div className="inspector-status"><StatusPill value={agentRun?.status ?? (agent.enabled ? "ready" : "disabled")} /><span>{agent.provider} through {agent.runtime}</span></div><section><h3>Authority-neutral role</h3><p>{agent.role}. Scheduling authority comes from policy, assignment, and receipts—not this label.</p></section><section><div className="section-title"><h3>Repository observation</h3><IconButton label="Refresh Git observation" onClick={() => void refreshGit()} disabled={busy}><RefreshCw className={busy ? "spin" : ""} size={14} /></IconButton></div>{git ? <><dl className="fact-list"><div><dt>Availability</dt><dd>{git.availability}</dd></div><div><dt>Branch</dt><dd>{git.branch || "detached"}</dd></div><div><dt>Working tree</dt><dd>{git.dirty ? `${git.dirty_paths?.length ?? 0}${git.omitted_paths ? `+${git.omitted_paths}` : ""} changed paths` : "clean"}</dd></div><div><dt>Write mode</dt><dd>{git.write_mode}</dd></div></dl>{git.dirty_paths && git.dirty_paths.length > 0 && <div className="changed-paths">{git.dirty_paths.slice(0, 16).map((path) => <code key={path}>{path}</code>)}{git.dirty_paths.length > 16 && <small>+{git.dirty_paths.length - 16 + (git.omitted_paths ?? 0)} paths omitted from this view</small>}</div>}</> : <p>No checkout is loaded in this bounded scope.</p>}</section>{notice && <div className="notice" role="status">{notice}</div>}{agentRun ? <button className="secondary-button" onClick={() => inspectRun(agentRun)}><TerminalSquare size={15} />Open run details</button> : <div className="quiet-line"><Clock3 size={14} />No current run</div>}<footer><span>Definition revision {agent.revision}</span><span>{agent.enabled ? "Enabled" : "Disabled"}</span></footer></> : task && <><div className="inspector-status"><StatusPill value={task.task.status} /><span>Priority {task.task.priority}</span></div><section><h3>Description</h3><p>{task.task.description || "No additional description."}</p></section><section><h3>Readiness</h3><p>{task.task.status === "completed" ? "Task completed. It is no longer awaiting scheduling." : task.readiness.ready ? "Ready for Crewfold to schedule." : readableTaskReadiness(task, data.tasks)}</p></section><section><h3>Assignment</h3><p>{data.agents.find((candidate) => candidate.id === task.task.assigned_agent_id)?.name ?? (["completed", "failed", "cancelled"].includes(task.task.status) ? "Released after the task finished." : "Unassigned")}</p></section><footer><span>Revision {task.task.revision}</span><span>Updated {displayTime(task.task.updated_at)}</span></footer></>}
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
  const [view, setView] = useState<View>("workbench");
  const [mobileNav, setMobileNav] = useState(false);
  const [selectedTask, setSelectedTask] = useState<TaskDetail | null>(null);
  const [selectedRun, setSelectedRun] = useState<Run | null>(null);
  const [selectedAgent, setSelectedAgent] = useState<Agent | null>(null);
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

  const title = useMemo(() => navItems.find((item) => item.id === view)?.label ?? "Workbench", [view]);
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

  return <div className={`app-shell ${fresh ? "" : "stale"}`}>
    <header className="topbar">
      <button className="mobile-menu" aria-label="Open navigation" onClick={() => setMobileNav(!mobileNav)}><Menu size={19} /></button>
      <div className="brand"><span className="mark">CF</span><span>Crewfold <small>local</small></span></div>
      <div className="crumbs"><span>{data.workspace?.name ?? "personal workbench"}</span><ChevronRight size={14} /><strong>{title}</strong></div>
      <div className={`service-state ${fresh ? "connected" : "connecting"}`}><i /><span>{fresh ? "Current · owner-local UI" : "Refreshing exact state…"}</span></div>
    </header>
    <aside className={`sidebar ${mobileNav ? "open" : ""}`}>
      <div className="workspace-card"><div className="workspace-glyph"><Boxes size={18} /></div><div><strong>{data.workspace?.name ?? "Your workbench"}</strong><span>{data.project?.name ?? "Exact local state"}</span></div>{data.workspaces.length > 1 && <ChevronDown size={15} />}</div>
      {data.workspaces.length > 1 && <select className="scope-select" value={data.workspace?.id ?? ""} onChange={(event) => void selectWorkspace(event.target.value)} aria-label="Workspace">{data.workspaces.map((workspace) => <option value={workspace.id} key={workspace.id}>{workspace.name}</option>)}</select>}
      {data.projects.length > 1 && <select className="scope-select" value={data.project?.id ?? ""} onChange={(event) => void selectProject(event.target.value)} aria-label="Project">{data.projects.map((project) => <option value={project.id} key={project.id}>{project.name}</option>)}</select>}
      <nav aria-label="Primary navigation">{navItems.map(({ id, label, icon: Icon }) => { const pendingDecisions = pendingConsequentialDecisions(data); return <button key={id} className={view === id ? "active" : ""} onClick={() => { setView(id); setMobileNav(false); }}><Icon size={17} strokeWidth={1.8} aria-hidden="true" /><span>{label}</span>{id === "decisions" && pendingDecisions > 0 && <b>{pendingDecisions}</b>}</button>; })}</nav>
      <div className="sidebar-spacer" />
      <div className="health-card"><HeartPulse size={16} /><span><strong>Canonical state</strong><small>Journal #{data.highWater}</small></span><Check size={14} /></div>
      <button className="settings-button" onClick={() => setView("health")}><Settings size={16} />Service settings</button>
    </aside>
    {loading ? <main className="loading-main"><LoaderCircle className="spin" size={26} /><p>Loading exact local state…</p></main> : data.workspaces.length === 0 || data.projects.length === 0 || data.agents.length === 0 ? <Onboarding apiBase={apiBase} csrf={csrf} status={status} onComplete={reload} /> : <main className="content-main">
      {error && <div className="global-error"><AlertCircle size={17} /><span>{error}</span><button onClick={() => void reload()}><RefreshCw size={15} />Retry</button></div>}
      {view === "workbench" && <WorkbenchView data={data} apiBase={apiBase} csrf={csrf} reload={reload} selectTask={inspectTask} selectRun={inspectRun} openDecisions={() => setView("decisions")} openCrew={() => setView("crew")} mutable={fresh} />}
      {view === "graph" && <GraphView data={data} selectTask={inspectTask} />}
      {view === "crew" && <CrewView data={data} apiBase={apiBase} csrf={csrf} reload={reload} selectAgent={inspectAgent} mutable={fresh} />}
      {view === "decisions" && <DecisionsView data={data} apiBase={apiBase} csrf={csrf} reload={reload} mutable={fresh} />}
      {view === "activity" && <ActivityView data={data} />}
      {view === "inbox" && <InboxView data={data} apiBase={apiBase} csrf={csrf} />}
      {view === "evidence" && <EvidenceView data={data} />}
      {view === "briefing" && <BriefingView data={data} apiBase={apiBase} csrf={csrf} />}
      {view === "health" && <HealthView apiBase={apiBase} csrf={csrf} status={status} />}
    </main>}
    <Inspector data={data} task={selectedTask} run={selectedRun} agent={selectedAgent} apiBase={apiBase} csrf={csrf} close={() => { setSelectedTask(null); setSelectedRun(null); setSelectedAgent(null); }} reload={reload} inspectRun={inspectRun} mutable={fresh} />
  </div>;
}

createRoot(document.getElementById("root")!).render(<StrictMode><App /></StrictMode>);
