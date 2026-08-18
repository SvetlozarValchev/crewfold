import { StrictMode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import {
  Activity, AlertCircle, Archive, Bot, Boxes, ChevronDown, ChevronRight, CircleDot, ClipboardCheck,
  Clock3, Command, FileCheck2, GitBranch, Inbox, LoaderCircle, MessageSquareText, Network, Play,
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
type DomainAgentSession = { project_id: string; agent_id: string; provider?: string; state: "unbound" | "ready" | "detached"; cwd?: string; has_conversation: boolean; revision: number; created_at?: string; updated_at?: string };
type DomainAgentSessionItem = { id: string; type: "userMessage" | "agentMessage" | "plan" | "commandExecution" | "dynamicToolCall" | "collabAgentToolCall" | "fileChange" | "reasoning"; text?: string; command?: string; status?: string };
type DomainAgentSessionTurn = { id: string; status: string; items: DomainAgentSessionItem[] };
type DomainAgentSessionResult = { schema: string; type: "domain_agent_session"; view: { session: DomainAgentSession; thread_status: string; turns: DomainAgentSessionTurn[] }; accepted_turn?: DomainAgentSessionTurn };
type DomainStaffingProfile = { provider: string; runtime: string; max_concurrency: number };
type DomainStaffingGrant = { id: string; project_id: string; manager_agent_id: string; manager_membership_revision: number; profiles: DomainStaffingProfile[]; task_classes: string[]; max_descendants: number; max_concurrency: number; budget: Budget; expires_at?: string; status: "active" | "revoked" | "expired"; revision: number; created_at: string; updated_at: string };
type Objective = { id: string; project_id: string; title: string; status: "active" | "completed" | "cancelled"; revision: number; updated_at: string };
type Task = { id: string; project_id: string; objective_id?: string; title: string; description?: string; status: string; blocked_reason?: string; priority: number; revision: number; assigned_agent_id?: string; updated_at: string };
type TaskDetail = { task: Task; dependencies: Array<{ depends_on_task_id: string }>; assignment?: { agent_id: string }; readiness: { ready: boolean; reason: string } };
type Run = { id: string; project_id: string; task_id: string; agent_id: string; runtime: string; provider: string; status: string; can_attach: boolean; revision: number; updated_at: string; result_summary?: string; blocked_question?: string; failure_code?: string; failure_message?: string };
type RunDetailView = { run: Run & { context_packet_id?: string; created_at: string; started_at?: string; finished_at?: string; placement?: { checkout_path?: string; write_mode?: string; reasons?: string[] } }; task: Task; agent: Agent; checkout: Checkout; timeline: Array<{ sequence: number; kind: string; message?: string; evidence: string[]; recorded_at: string }>; handoff?: { summary: string; evidence: string[]; created_at: string } };
type ContextExplanation = { packet_id: string; content_hash: string; byte_size: number; included: Array<{ section: string; entity_type: string; entity_id: string; revision: number; reason: string }>; excluded: Array<{ section: string; reason: string; reason_code?: string }>; budget: { total: { limit_bytes: number; used_bytes: number; remaining_bytes: number } } };
type EventRecord = { event_id: string; sequence: number; type: string; recorded_at: string; actor: { actor_type: string }; entity: { type: string; id: string; revision: number } };
type InboxItem = { message: { id: string; sender_type: string; sender_agent_name?: string; kind: string; body: string; task_id?: string; created_at: string }; delivery: { recipient_agent_id: string; recipient_name: string; status: string; wake_status: string } };
type CheckRunItem = { run: { id: string; task_id: string; status: string; revision: number; created_at: string; updated_at: string }; outcome?: string; requirement_state: string; current_freshness?: { status?: string } };
type Budget = { token_limit: number; cost_cents: number; time_seconds: number };
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
const emptyData: WorkbenchData = { workspaces: [], workspace: null, projects: [], project: null, checkouts: [], agents: [], domainAgents: [], objectives: [], tasks: [], runs: [], checks: [], highWater: 0 };

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
  const [checkouts, checks, domainAgents] = project ? await Promise.all([
    rpc<{ checkouts: Checkout[] }>(apiBase, csrf, "checkout.list", { workspace: workspace.id, project: project.id }).then((value) => value.checkouts),
    rpc<{ runs: CheckRunItem[] } & Page>(apiBase, csrf, "check.list", { workspace: workspace.id, project: project.id, limit: 200 }).then((value) => value.runs),
    rpc<{ project_id: string; agents: DomainAgent[] }>(apiBase, csrf, "domain.agent.tree", { workspace: workspace.id, project: project.id }).then((value) => value.agents),
  ]) : [[], [], []];
  const after = await rpc<{ events: EventRecord[]; high_water: number } & Page>(apiBase, csrf, "events.list", { workspace: workspace.id, after: before.high_water, limit: 1 });
  if (after.high_water !== before.high_water) {
    if (attempt >= 2) throw new Error("Canonical state kept changing during refresh; retry when the current event cut settles.");
    return loadWorkbench(apiBase, csrf, workspace.id, project?.id ?? preferredProject, attempt + 1);
  }
  return {
    workspaces: workspacePage.workspaces, workspace, projects: projectPage.projects, project, checkouts,
    agents: agentPage.agents, domainAgents, objectives: objectivePage.objectives, tasks: taskPage.tasks,
    runs: runPage.runs, checks,
    highWater: eventPage.high_water,
  };
}

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
      <p className="form-note">One replay-safe submission records the workspace, domain, checkout, first agent definition, launch profile, and hierarchy membership.</p>
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
  const row = (agent: DomainAgent, depth: number): React.ReactNode => {
    return <div key={agent.definition.id}>
      <button className={`m22-agent-row ${selected === agent.definition.id ? "selected" : ""}`} style={{ paddingLeft: `${12 + depth * 17}px` }} onClick={() => choose(agent)}>
        <span className="m22-tree-joint">{depth ? "└" : "›"}</span>
        <span><strong>{agent.definition.name}</strong><small>{agent.definition.role || "unlabeled agent"}</small></span>
        {agent.membership.preferred_entry && <em>default</em>}
      </button>
      {(children.get(agent.definition.id) ?? []).map((child) => row(child, depth + 1))}
    </div>;
  };
  const roots = active.filter((agent) => !agent.membership.parent_agent_id);
  const domainWide = roots.filter((agent) => !agent.membership.workstream_id);
  const titleCounts = activeObjectives.reduce((counts, objective) => {
    const key = objective.title.trim().toLocaleLowerCase();
    counts.set(key, (counts.get(key) ?? 0) + 1);
    return counts;
  }, new Map<string, number>());
  const scoped = activeObjectives.map((objective) => ({ objective, agents: roots.filter((agent) => agent.membership.workstream_id === objective.id) }));
  const closedScoped = closedObjectives.map((objective) => ({ objective, agents: roots.filter((agent) => agent.membership.workstream_id === objective.id) })).filter((group) => group.agents.length > 0);
  if (!roots.length && !activeObjectives.length) return <p className="m22-rail-empty">No active durable agents or workstreams are attached to this domain.</p>;
  return <div className="m22-agent-tree">{domainWide.map((agent) => row(agent, 0))}{scoped.map((group) => <section className="m22-workstream-group" key={group.objective.id}><h3>{group.objective.title}{(titleCounts.get(group.objective.title.trim().toLocaleLowerCase()) ?? 0) > 1 && <span>duplicate title · r{group.objective.revision}</span>}</h3>{group.agents.length ? group.agents.map((agent) => row(agent, 0)) : <p>no agents</p>}</section>)}{closedScoped.map((group) => <section className="m22-workstream-group closed" key={group.objective.id}><h3>{group.objective.title}<span>closed · {group.objective.status}</span></h3>{group.agents.map((agent) => row(agent, 0))}</section>)}</div>;
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

function DomainHome({ data, chooseAgent, reviewWorkstream, inspectRun, notice = "" }: { data: WorkbenchData; chooseAgent: (agent: DomainAgent) => void; reviewWorkstream: (objective: Objective) => void; inspectRun: (run: Run) => void; notice?: string }) {
  const projectObjectives = data.objectives.filter((objective) => objective.project_id === data.project?.id);
  const activeObjectives = projectObjectives.filter((objective) => objective.status === "active");
  const closedObjectives = projectObjectives.filter((objective) => objective.status !== "active");
  const activeAgents = data.domainAgents.filter((agent) => agent.membership.status === "active");
  const retiredAgents = data.domainAgents.filter((agent) => agent.membership.status === "retired");
  const projectTasks = data.tasks.filter((detail) => detail.task.project_id === data.project?.id);
  const projectRuns = data.runs.filter((run) => run.project_id === data.project?.id);
  const attention = projectTasks.filter((detail) => ["blocked", "changes_requested", "failed"].includes(detail.task.status));
  const activeRuns = projectRuns.filter((run) => ["requested", "starting", "active", "blocked", "stopping", "lost"].includes(run.status));
  const objectiveTitleCounts = activeObjectives.reduce((counts, objective) => {
    const key = objective.title.trim().toLocaleLowerCase();
    counts.set(key, (counts.get(key) ?? 0) + 1);
    return counts;
  }, new Map<string, number>());
  return <section className="m22-domain-home">
    <header>
      <p className="m22-kicker">domain</p>
      <h1>{data.project?.name}</h1>
      <p>{activeObjectives.length ? `${activeObjectives.length} active workstream${activeObjectives.length === 1 ? "" : "s"}, ${activeAgents.length} durable agent${activeAgents.length === 1 ? "" : "s"}, and ${data.checkouts.length} attached checkout${data.checkouts.length === 1 ? "" : "s"}.` : "A durable coordination and knowledge boundary. No active workstream is recorded yet."}</p>
    </header>
    {notice && <div className="m22-success"><ShieldCheck size={14} />{notice}</div>}
    {attention.length > 0 && <section className="m22-block"><h2>needs attention</h2>{attention.map((detail) => <button key={detail.task.id} className="m22-line"><span><strong>{detail.task.title}</strong><small>{detail.task.blocked_reason || detail.task.status.replaceAll("_", " ")}</small></span><StatusPill value={detail.task.status} /></button>)}</section>}
    <div className="m22-columns">
      <section className="m22-block"><h2>active workstreams</h2>{activeObjectives.length ? activeObjectives.map((objective) => <button className="m22-line" key={objective.id} onClick={() => reviewWorkstream(objective)}><span><strong>{objective.title}</strong><small>{projectTasks.filter((detail) => detail.task.objective_id === objective.id && !["completed", "failed", "cancelled"].includes(detail.task.status)).length} open tasks{(objectiveTitleCounts.get(objective.title.trim().toLocaleLowerCase()) ?? 0) > 1 ? ` · duplicate title · revision ${objective.revision}` : ""} · open lifecycle</small></span><StatusPill value={objective.status} /></button>) : <p className="m22-empty">No active workstreams.</p>}</section>
      <section className="m22-block"><h2>attached checkouts</h2>{data.checkouts.map((checkout) => <div className="m22-line static" key={checkout.id}><span><strong>{checkout.path}</strong><small>{checkout.branch || "detached"} · {checkout.write_mode}</small></span><StatusPill value={checkout.availability} /></div>)}</section>
    </div>
    <div className="m22-columns">
      <section className="m22-block"><h2>durable agents</h2>{activeAgents.length ? activeAgents.map((agent) => <button className="m22-line" key={agent.definition.id} onClick={() => chooseAgent(agent)}><span><strong>{agent.definition.name}</strong><small>{agent.definition.role} · {agent.definition.provider} through {agent.definition.runtime}</small></span><StatusPill value={latestRunForAgent(projectRuns, agent.definition.id)?.status ?? "idle"} /></button>) : <p className="m22-empty">No active durable agents.</p>}</section>
      <section className="m22-block"><h2>current runs</h2>{activeRuns.length ? activeRuns.map((run) => <button className="m22-line" key={run.id} onClick={() => inspectRun(run)}><span><strong>{projectTasks.find((detail) => detail.task.id === run.task_id)?.task.title ?? run.id}</strong><small>{data.agents.find((agent) => agent.id === run.agent_id)?.name ?? run.agent_id}</small></span><StatusPill value={run.status} /></button>) : <p className="m22-empty">No live or unresolved run.</p>}</section>
    </div>
    <section className="m22-block"><h2>domain home</h2><p className="m22-empty">No attributed domain note or pinned shared-memory item has been recorded. M22 will only render authored, revisioned pins here; it will not invent a project summary.</p></section>
    {(retiredAgents.length > 0 || closedObjectives.length > 0) && <details className="m22-history"><summary><Archive size={14} /> retired and closed history <span>{retiredAgents.length + closedObjectives.length}</span></summary><div>{retiredAgents.map((agent) => <div className="m22-line static" key={agent.definition.id}><span><strong>{agent.definition.name}</strong><small>retired agent · {agent.definition.role} · updated {displayTime(agent.membership.updated_at)}</small></span><StatusPill value="retired" /></div>)}{closedObjectives.map((objective) => <div className="m22-line static" key={objective.id}><span><strong>{objective.title}</strong><small>closed workstream · revision {objective.revision} · updated {displayTime(objective.updated_at)}</small></span><StatusPill value={objective.status} /></div>)}</div></details>}
  </section>;
}

function DomainWorkstreamCreatePanel({ data, apiBase, csrf, mutable, close, created, reload }: { data: WorkbenchData; apiBase: string; csrf: string; mutable: boolean; close: () => void; created: (title: string) => void; reload: () => Promise<void> }) {
  const [title, setTitle] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const submitting = useRef(false);
  const submit = async (event: React.FormEvent) => {
    event.preventDefault();
    if (!data.workspace || !data.project || submitting.current) return;
    const exactTitle = title.trim();
    if (data.objectives.some((objective) => objective.project_id === data.project?.id && objective.status === "active" && objective.title.trim().toLocaleLowerCase() === exactTitle.toLocaleLowerCase())) {
      setError(`An active workstream named “${exactTitle}” already exists in this domain.`);
      return;
    }
    submitting.current = true; setBusy(true); setError("");
    try {
      await rpc(apiBase, csrf, "objective.create", { workspace: data.workspace.id, project: data.project.id, title: exactTitle, budget: { token_limit: 0, cost_cents: 0, time_seconds: 0 }, idempotency_key: newKey("domain-workstream") });
      await reload(); created(exactTitle);
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Could not create the workstream."); }
    finally { submitting.current = false; setBusy(false); }
  };
  return <section className="m22-agent-create m22-workstream-create">
    <header><div><p className="m22-kicker">owner operation</p><h1>Create a workstream</h1><p>A workstream is one canonical objective inside this domain. It groups related durable agents and work without making a repository, folder, or agent the domain boundary.</p></div><button onClick={close} aria-label="Close workstream creation"><X size={15} /></button></header>
    <form onSubmit={submit}><label><span>workstream name</span><input autoFocus required maxLength={256} value={title} onChange={(event) => setTitle(event.target.value)} placeholder="Terrain consolidation" /></label>
      <div className="m22-exact-effect"><ShieldCheck size={15} /><span><strong>Exact effect</strong> Create one empty objective-backed workstream. No agent is moved, task created, run started, or authority granted.</span></div>
      <p className="m22-caveat">After creation it appears in the hierarchy even while empty. Choose it when creating or editing an agent’s placement.</p>
      {error && <p className="m22-session-error" role="alert">{error}</p>}
      <div className="m22-form-actions"><button type="button" onClick={close}>cancel</button><button className="m22-send" disabled={!mutable || busy || !title.trim()}>{busy ? <LoaderCircle className="spin" size={14} /> : <Plus size={14} />} {busy ? "creating…" : "create workstream"}</button></div>
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

function DomainWorkstreamLifecyclePanel({ data, objective, apiBase, csrf, mutable, close, cancelled, reload }: { data: WorkbenchData; objective: Objective; apiBase: string; csrf: string; mutable: boolean; close: () => void; cancelled: () => void; reload: () => Promise<void> }) {
  const [confirmed, setConfirmed] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const agents = data.domainAgents.filter((agent) => agent.membership.status === "active" && agent.membership.workstream_id === objective.id);
  const tasks = data.tasks.filter((detail) => detail.task.objective_id === objective.id && !["completed", "failed", "cancelled"].includes(detail.task.status));
  const runs = data.runs.filter((run) => tasks.some((detail) => detail.task.id === run.task_id) && ["requested", "starting", "active", "blocked", "stopping", "lost"].includes(run.status));
  const blockers = [
    ...(agents.length ? [`${agents.length} active durable agent${agents.length === 1 ? " is" : "s are"} scoped here`] : []),
    ...(tasks.length ? [`${tasks.length} nonterminal task${tasks.length === 1 ? " remains" : "s remain"}`] : []),
    ...(runs.length ? [`${runs.length} live or unresolved run${runs.length === 1 ? " remains" : "s remain"}`] : []),
  ];
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
    <header><div><p className="m22-kicker">workstream lifecycle</p><h1>{objective.title}</h1><p>Review the exact dependencies before closing this objective-backed workstream. Cancellation removes it from active organization; it never deletes tasks, events, or prior agent placement.</p></div><button onClick={close} aria-label="Close workstream lifecycle"><X size={15} /></button></header>
    <form onSubmit={submit}>
      <dl className="m22-review-facts"><div><dt>status</dt><dd>{objective.status}</dd></div><div><dt>revision</dt><dd>{objective.revision}</dd></div><div><dt>active agents</dt><dd>{agents.length}</dd></div><div><dt>open tasks</dt><dd>{tasks.length}</dd></div></dl>
      {blockers.length ? <div className="m22-lifecycle-blockers" role="alert"><strong>Cancellation is blocked</strong><ul>{blockers.map((blocker) => <li key={blocker}>{blocker}</li>)}</ul><p>Move or retire scoped agents and resolve every task/run first. Crewfold will not detach or cancel them implicitly.</p></div> : <div className="m22-exact-effect"><Archive size={15} /><span><strong>Exact effect</strong> Change this objective from active to cancelled. It moves to closed history; all contained canonical records remain available.</span></div>}
      {!blockers.length && objective.status === "active" && <label className="m22-confirm"><input type="checkbox" checked={confirmed} onChange={(event) => setConfirmed(event.target.checked)} /><span>I understand this closes the workstream without erasing its history.</span></label>}
      {error && <p className="m22-session-error" role="alert">{error}</p>}
      <div className="m22-form-actions"><button type="button" onClick={close}>back to domain</button>{objective.status === "active" && <button className="m22-danger" disabled={!mutable || busy || blockers.length > 0 || !confirmed}>{busy ? <LoaderCircle className="spin" size={14} /> : <Archive size={14} />} {busy ? "cancelling…" : "cancel workstream"}</button>}</div>
    </form>
  </section>;
}

function DurableAgentSession({ data, agent, currentRun, inspectRun, apiBase, csrf }: { data: WorkbenchData; agent: DomainAgent; currentRun: Run | null; inspectRun: (run: Run) => void; apiBase: string; csrf: string }) {
  const [result, setResult] = useState<DomainAgentSessionResult | null>(null);
  const [checkout, setCheckout] = useState(data.checkouts.length === 1 ? data.checkouts[0].id : "");
  const [input, setInput] = useState("");
  const [busy, setBusy] = useState<"loading" | "opening" | "sending" | "interrupting" | "">("loading");
  const [error, setError] = useState("");
  const scope = { workspace: data.workspace?.id ?? "", project: data.project?.id ?? "", agent: agent.definition.id };
  const load = useCallback(async (quiet = false) => {
    if (!scope.workspace || !scope.project) return;
    if (!quiet) setBusy("loading");
    try {
      const next = await rpc<DomainAgentSessionResult>(apiBase, csrf, "domain.agent.session.show", scope);
      setResult(next); setError("");
    } catch (reason) {
      if (!quiet) setError(reason instanceof Error ? reason.message : "Could not read the durable Codex session.");
    } finally { if (!quiet) setBusy(""); }
  }, [apiBase, csrf, scope.agent, scope.project, scope.workspace]);
  const accepted = result?.accepted_turn;
  const turns = result ? [...result.view.turns, ...(accepted && !result.view.turns.some((turn) => turn.id === accepted.id) ? [accepted] : [])] : [];
  const activeTurn = [...turns].reverse().find((turn) => ["inProgress", "in_progress"].includes(turn.status));
  useEffect(() => {
    setResult(null); setInput(""); setError("");
    setCheckout(data.checkouts.length === 1 ? data.checkouts[0].id : "");
    void load();
  }, [agent.definition.id, data.project?.id]);
  useEffect(() => {
    if (result?.view.session.state !== "ready" || result.view.thread_status !== "active" && !activeTurn) return;
    const timer = window.setInterval(() => void load(true), 900);
    return () => window.clearInterval(timer);
  }, [activeTurn?.id, load, result?.view.session.state, result?.view.thread_status]);
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
  const state = result?.view.session.state ?? (busy === "loading" ? "loading" : "unavailable");
  if (state === "unbound") return <div className="m22-session m22-session-empty">
    <div className="m22-session-state"><span>Codex conversation</span><StatusPill value="not started" /></div>
    <h2>Start this durable agent’s provider thread</h2>
    <p>This creates one real, non-ephemeral Codex app-server thread for <strong>{agent.definition.name}</strong>. Its name and role remain descriptive; Crewfold’s grants still govern effects.</p>
    {data.checkouts.length > 1 && <label className="m22-session-checkout"><span>attached checkout</span><select value={checkout} onChange={(event) => setCheckout(event.target.value)}><option value="">select exact checkout</option>{data.checkouts.filter((item) => item.availability === "available").map((item) => <option key={item.id} value={item.id}>{item.path}</option>)}</select></label>}
    <button className="m22-command" disabled={busy !== "" || !checkout} onClick={() => void open()}>{busy === "opening" ? <LoaderCircle className="spin" size={15} /> : <Play size={15} />} start Codex session</button>
    {error && <p className="m22-session-error">{error}</p>}
  </div>;
  return <div className="m22-session">
    <div className="m22-session-state"><span>Codex conversation · {result?.view.session.provider || agent.definition.provider}</span><StatusPill value={result?.view.thread_status ?? state} /></div>
    {busy === "loading" && !result ? <p className="m22-session-loading"><LoaderCircle className="spin" size={14} /> reading persisted provider thread…</p> : state === "detached" ? <p className="m22-session-error">This session belongs to another Crewfold node. It is visible as detached and cannot be controlled here.</p> : <>
      <div className="m22-thread" aria-live="polite">
        {turns.length === 0 ? <p className="m22-empty">The provider thread is ready. Send the first owner message below.</p> : turns.map((turn) => <section className="m22-turn" key={turn.id}>
          {turn.items.map((item) => <article className={`m22-thread-item ${item.type}`} key={item.id}>
            <span>{item.type === "userMessage" ? "you" : item.type === "agentMessage" ? agent.definition.name : item.type === "commandExecution" ? "command" : item.type.replaceAll(/([A-Z])/g, " $1").toLowerCase()}</span>
            {item.command && <code>{item.command}</code>}
            {item.text && <p>{item.text}</p>}
            {item.status && <small>{item.status.replaceAll("_", " ")}</small>}
          </article>)}
          <footer>{turn.status.replaceAll("_", " ")}</footer>
        </section>)}
      </div>
      <div className="m22-composer">
        <textarea value={input} onChange={(event) => setInput(event.target.value)} placeholder={`Message ${agent.definition.name} directly…`} maxLength={65536} onKeyDown={(event) => { if ((event.metaKey || event.ctrlKey) && event.key === "Enter") void send(); }} />
        <div><small>Ctrl/⌘ + Enter to send · conversation text is not authority</small><span>{activeTurn && <button disabled={busy !== ""} onClick={() => void interrupt()}><Square size={13} /> interrupt</button>}<button className="m22-send" disabled={busy !== "" || !input.trim() || Boolean(activeTurn)} onClick={() => void send()}>{busy === "sending" ? <LoaderCircle className="spin" size={14} /> : <Send size={14} />} send</button></span></div>
      </div>
    </>}
    {currentRun && <button className="m22-command secondary" onClick={() => inspectRun(currentRun)}><TerminalSquare size={15} /> open separate Crewfold execution run</button>}
    {error && <p className="m22-session-error">{error}</p>}
  </div>;
}

function DomainAgentCenter({ data, agent, view, setView, inspectRun, apiBase, csrf, mutable }: { data: WorkbenchData; agent: DomainAgent; view: DomainConsoleView; setView: (view: DomainConsoleView) => void; inspectRun: (run: Run) => void; apiBase: string; csrf: string; mutable: boolean }) {
  const definition = agent.definition;
  const assigned = data.tasks.filter((detail) => detail.task.assigned_agent_id === definition.id);
  const runs = data.runs.filter((run) => run.agent_id === definition.id).sort((left, right) => right.updated_at.localeCompare(left.updated_at));
  const currentRun = runs[0] ?? null;
  const workstream = data.objectives.find((objective) => objective.id === agent.membership.workstream_id);
  const tabs: Array<[DomainConsoleView, string]> = [["session", "session"], ["assignment", "assignment"], ["changes", "changes"], ["briefing", "briefing"], ["verification", "verification"], ["staffing", "staffing"]];
  return <section className="m22-agent-center">
    <header><p className="m22-kicker">durable agent</p><h1>{definition.name}</h1><p>{definition.role || "No descriptive role"} · {definition.provider} through {definition.runtime}{workstream ? ` · ${workstream.title}` : ""}</p></header>
    <nav className="m22-tabs" aria-label="Selected agent views">{tabs.map(([id, label]) => <button className={view === id ? "active" : ""} key={id} onClick={() => setView(id)}>{label}</button>)}</nav>
    {view === "session" && <DurableAgentSession data={data} agent={agent} currentRun={currentRun} inspectRun={inspectRun} apiBase={apiBase} csrf={csrf} />}
    {view === "assignment" && <div className="m22-block"><h2>assigned work</h2>{assigned.length ? assigned.map((detail) => <div className="m22-line static" key={detail.task.id}><span><strong>{detail.task.title}</strong><small>{detail.task.description || "No additional description"}</small></span><StatusPill value={detail.task.status} /></div>) : <p className="m22-empty">No task is assigned to this agent.</p>}</div>}
    {view === "changes" && <div className="m22-block"><h2>observed checkout changes</h2>{data.checkouts.flatMap((checkout) => checkout.dirty_paths ?? []).length ? data.checkouts.flatMap((checkout) => (checkout.dirty_paths ?? []).map((path) => <code className="m22-path" key={`${checkout.id}-${path}`}>{path}</code>)) : <p className="m22-empty">No changed paths are present in the bounded checkout observation.</p>}<p className="m22-caveat">These are project checkout observations, not an inferred per-agent diff.</p></div>}
    {view === "briefing" && <div className="m22-block"><h2>assigned context</h2><dl className="m22-facts"><div><dt>domain</dt><dd>{data.project?.name}</dd></div><div><dt>workstream</dt><dd>{workstream?.title ?? "not scoped"}</dd></div><div><dt>parent</dt><dd>{data.domainAgents.find((candidate) => candidate.definition.id === agent.membership.parent_agent_id)?.definition.name ?? "domain root"}</dd></div><div><dt>provider/runtime</dt><dd>{definition.provider} / {definition.runtime}</dd></div></dl><p className="m22-caveat">A run-specific context packet appears only after a real run is created.</p></div>}
    {view === "verification" && <div className="m22-block"><h2>verification owned by assigned tasks</h2>{data.checks.filter((check) => assigned.some((detail) => detail.task.id === check.run.task_id)).length ? data.checks.filter((check) => assigned.some((detail) => detail.task.id === check.run.task_id)).map((check) => <div className="m22-line static" key={check.run.id}><span><strong>{assigned.find((detail) => detail.task.id === check.run.task_id)?.task.title}</strong><small>{check.requirement_state}</small></span><StatusPill value={check.run.status} /></div>) : <p className="m22-empty">No canonical check run is attached to this agent's assigned work.</p>}</div>}
    {view === "staffing" && <StaffingPanel data={data} agent={agent} apiBase={apiBase} csrf={csrf} mutable={mutable} />}
  </section>;
}

function DomainConsole({ data, selectedAgentID, selectAgent, selectProject, inspectRun, apiBase, csrf, mutable, reload }: { data: WorkbenchData; selectedAgentID: string; selectAgent: (agent: DomainAgent | null) => void; selectProject: (id: string) => void; inspectRun: (run: Run) => void; apiBase: string; csrf: string; mutable: boolean; reload: () => Promise<void> }) {
  const [view, setView] = useState<DomainConsoleView>("domain");
  const [creating, setCreating] = useState(false);
  const [creatingWorkstream, setCreatingWorkstream] = useState(false);
  const [retiringAgentID, setRetiringAgentID] = useState("");
  const [reviewingWorkstreamID, setReviewingWorkstreamID] = useState("");
  const [workstreamNotice, setWorkstreamNotice] = useState("");
  const selected = data.domainAgents.find((agent) => agent.definition.id === selectedAgentID) ?? null;
  const retiringAgent = data.domainAgents.find((agent) => agent.definition.id === retiringAgentID) ?? null;
  const reviewingWorkstream = data.objectives.find((objective) => objective.id === reviewingWorkstreamID) ?? null;
  useEffect(() => { setView(selected ? "session" : "domain"); setCreating(false); setCreatingWorkstream(false); setRetiringAgentID(""); setReviewingWorkstreamID(""); }, [selected?.definition.id, data.project?.id]);
  useEffect(() => { setWorkstreamNotice(""); setReviewingWorkstreamID(""); setRetiringAgentID(""); }, [data.project?.id]);
  return <div className="m22-console">
    <aside className="m22-domain-rail">
      <p className="m22-rail-label">domains</p>
      {data.projects.map((project) => <button key={project.id} className={`m22-domain-row ${project.id === data.project?.id && !selected ? "selected" : ""}`} onClick={() => { if (project.id !== data.project?.id) selectProject(project.id); selectAgent(null); }}><Boxes size={14} /><span><strong>{project.name}</strong><small>{project.id === data.project?.id ? `${data.domainAgents.filter((agent) => agent.membership.status === "active").length} durable agents` : "select domain"}</small></span></button>)}
      {data.project && <><p className="m22-rail-label agents">agent hierarchy</p><DomainAgentTreeList agents={data.domainAgents} objectives={data.objectives.filter((objective) => objective.project_id === data.project?.id)} selected={selectedAgentID} choose={selectAgent} /></>}
      <div className="m22-rail-spacer" />
      {data.project && <button className="m22-add-agent" disabled={!mutable} onClick={() => { setCreating(false); setRetiringAgentID(""); setReviewingWorkstreamID(""); setWorkstreamNotice(""); setCreatingWorkstream(true); }}><Plus size={13} /> new workstream</button>}
      {data.project && <button className="m22-add-agent" disabled={!mutable} onClick={() => { setCreatingWorkstream(false); setRetiringAgentID(""); setReviewingWorkstreamID(""); setWorkstreamNotice(""); setCreating(true); }}><Plus size={13} /> add durable agent</button>}
      <div className="m22-cut">canonical through event {data.highWater}</div>
    </aside>
    <main className="m22-center">{retiringAgent ? <DomainAgentRetirementPanel data={data} agent={retiringAgent} apiBase={apiBase} csrf={csrf} mutable={mutable} close={() => setRetiringAgentID("")} retired={() => { setRetiringAgentID(""); selectAgent(null); setWorkstreamNotice(`Agent “${retiringAgent.definition.name}” was retired. Its canonical history remains under retired and closed history.`); }} reload={reload} /> : reviewingWorkstream ? <DomainWorkstreamLifecyclePanel data={data} objective={reviewingWorkstream} apiBase={apiBase} csrf={csrf} mutable={mutable} close={() => setReviewingWorkstreamID("")} cancelled={() => { setReviewingWorkstreamID(""); selectAgent(null); setWorkstreamNotice(`Workstream “${reviewingWorkstream.title}” was cancelled and moved to closed history.`); }} reload={reload} /> : creatingWorkstream ? <DomainWorkstreamCreatePanel data={data} apiBase={apiBase} csrf={csrf} mutable={mutable} close={() => setCreatingWorkstream(false)} created={(title) => { setCreatingWorkstream(false); setWorkstreamNotice(`Workstream “${title}” was created. It is empty until you place durable agents or work inside it.`); selectAgent(null); }} reload={reload} /> : creating ? <DomainAgentCreatePanel data={data} suggestedParent={selected?.definition.id ?? ""} apiBase={apiBase} csrf={csrf} mutable={mutable} close={() => setCreating(false)} created={(agent) => { setCreating(false); selectAgent(agent); }} reload={reload} /> : selected ? <DomainAgentCenter data={data} agent={selected} view={view} setView={setView} inspectRun={inspectRun} apiBase={apiBase} csrf={csrf} mutable={mutable} /> : <DomainHome data={data} chooseAgent={selectAgent} reviewWorkstream={(objective) => { setWorkstreamNotice(""); setReviewingWorkstreamID(objective.id); }} inspectRun={inspectRun} notice={workstreamNotice} />}</main>
    <aside className="m22-context">
      <p className="m22-rail-label">selected {selected ? "agent" : "domain"}</p>
      <h2>{selected?.definition.name ?? data.project?.name}</h2>
      {selected ? <><dl className="m22-facts"><div><dt>role</dt><dd>{selected.definition.role}</dd></div><div><dt>operating mode</dt><dd>{selected.membership.delegation_policy.replaceAll("_", " ")}</dd></div><div><dt>status</dt><dd>{selected.membership.status}</dd></div><div><dt>opens by default</dt><dd>{selected.membership.preferred_entry ? "yes" : "no"}</dd></div><div><dt>revision</dt><dd>{selected.membership.revision}</dd></div></dl><section><h3>operating charter</h3><p>{selected.membership.operating_charter}</p></section><DomainAgentPlacementEditor data={data} agent={selected} apiBase={apiBase} csrf={csrf} mutable={mutable} reload={reload} /><section><h3>authority boundary</h3><p>Tree placement and charter organize behavior only. Grants, assignments, claims, budgets, and capabilities authorize effects.</p></section>{selected.membership.status === "active" && <section className="m22-lifecycle-entry"><h3>lifecycle</h3><p>Retirement preserves this agent’s history and refuses while it retains active responsibility.</p><button className="m22-danger subtle" disabled={!mutable} onClick={() => { setCreating(false); setCreatingWorkstream(false); setReviewingWorkstreamID(""); setRetiringAgentID(selected.definition.id); }}><Archive size={13} /> review retirement</button></section>}</> : <><dl className="m22-facts"><div><dt>active workstreams</dt><dd>{data.objectives.filter((objective) => objective.status === "active").length}</dd></div><div><dt>active agents</dt><dd>{data.domainAgents.filter((agent) => agent.membership.status === "active").length}</dd></div><div><dt>checkouts</dt><dd>{data.checkouts.length}</dd></div><div><dt>open tasks</dt><dd>{data.tasks.filter((detail) => !["completed", "cancelled", "failed"].includes(detail.task.status)).length}</dd></div></dl><section><h3>domain boundary</h3><p>A domain coordinates related work and knowledge. It is not a repository or folder.</p></section></>}
    </aside>
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
    {currentRun ? <><div className="inspector-status"><StatusPill value={currentRun.status} /><span>{currentRun.provider} through {currentRun.runtime}</span></div><section><h3>Assigned work</h3><p>{currentTask?.title ?? "Task unavailable in this bounded page"}</p></section>{["start_failed", "failed", "lost"].includes(currentRun.status) && <section className="launch-failure" role="alert"><div className="section-title"><h3>{currentRun.status === "start_failed" ? "Launch failed" : currentRun.status === "lost" ? "Runtime outcome is unknown" : "Run failed"}</h3><AlertCircle size={16} /></div><p>{currentRun.failure_message ?? currentRun.failure_code ?? "Inspect the bounded runtime output for the exact provider diagnosis."}</p>{currentRun.status === "start_failed" && <button className="primary-button compact" disabled={!mutable || busy} onClick={() => void retry()}>{busy ? <LoaderCircle className="spin" size={14} /> : <RotateCcw size={14} />}Retry after preflight</button>}</section>}{currentRun.status === "blocked" && <section className="launch-failure review-retry" role="alert"><div className="section-title"><h3>Blocked runtime authority</h3><AlertCircle size={16} /></div><p>Resume continues this exact runtime and sandbox. It is correct only after the blocker was repaired in place.</p><p>If service, provider, runtime, or network policy changed, stop this run first. Once it is durably stopped, Crewfold can launch the retained assignment in a fresh environment.</p></section>}{canStartFresh && <section className="launch-failure review-retry"><div className="section-title"><h3>Launch a fresh environment</h3><RotateCcw size={16} /></div><p>The prior runtime is durably stopped and no longer holds authority. The task's exact assignment is retained.</p><p>This creates one fresh run using the current service, provider, runtime, and network policy.</p><button className="primary-button compact" disabled={!mutable || busy} onClick={() => void retry()}>{busy ? <LoaderCircle className="spin" size={14} /> : <RotateCcw size={14} />}Start fresh run</button></section>}{canRetryReview && <section className="launch-failure review-retry" role="alert"><div className="section-title"><h3>Changes requested</h3><ClipboardCheck size={16} /></div><p>{currentTask?.blocked_reason ?? "The completion did not satisfy the task acceptance evidence."}</p><p>The prior review remains immutable. Retrying reopens this exact assignment and creates a fresh context-bound run.</p><button className="primary-button compact" disabled={!mutable || busy} onClick={() => void retry()}>{busy ? <LoaderCircle className="spin" size={14} /> : <RotateCcw size={14} />}Retry requested changes</button></section>}<section><div className="section-title"><h3>Readable agent activity</h3><Activity size={15} /></div><RuntimeOutput logs={logs} status={currentRun.status} /></section>{terminalOpen && <LiveTerminal key={currentRun.id} apiBase={apiBase} csrf={csrf} workspace={data.workspace?.id ?? ""} run={currentRun} initialLogs={logs} close={() => setTerminalOpen(false)} mutable={mutable} />}{["active", "blocked"].includes(currentRun.status) && <section className="runtime-control"><label htmlFor="runtime-prompt">Send a visible runtime prompt</label><div><input id="runtime-prompt" value={prompt} maxLength={4096} onChange={(event) => setPrompt(event.target.value)} placeholder="Clarify the next observable step…" /><button className="secondary-button" disabled={busy || !prompt.trim()} onClick={() => void sendPrompt()}><Send size={14} />Send</button></div><div className="runtime-buttons">{currentRun.can_attach && !terminalOpen && <button className="secondary-button" disabled={busy} onClick={() => setTerminalOpen(true)}><TerminalSquare size={14} />Open live activity</button>}{currentRun.status === "blocked" && <button className="secondary-button" disabled={busy} onClick={() => void resume()}><RotateCcw size={14} />Resume same runtime</button>}<button className="secondary-button" disabled={busy} onClick={() => void interrupt()}><AlertCircle size={14} />Interrupt</button><button className="danger-button" onClick={() => void stop()} disabled={busy}>{busy ? <LoaderCircle className="spin" size={15} /> : <Square size={15} />}Stop · 5000 ms grace</button></div></section>}{notice && <div className="notice" role="status">{notice}</div>}<footer><span>Revision {currentRun.revision}</span><span>{currentRun.can_attach ? "Live activity and interactive controls available" : currentRun.status === "start_failed" ? "Retry available after diagnosis" : canRetryReview ? "Reviewed retry available" : canStartFresh ? "Fresh launch available on retained assignment" : "Bounded logs only"}</span></footer></> : agent ? <><div className="inspector-status"><StatusPill value={agentRun?.status ?? (agent.enabled ? "ready" : "disabled")} /><span>{agent.provider} through {agent.runtime}</span></div><section><h3>Authority-neutral role</h3><p>{agent.role}. Scheduling authority comes from policy, assignment, and receipts—not this label.</p></section><section><div className="section-title"><h3>Repository observation</h3><IconButton label="Refresh Git observation" onClick={() => void refreshGit()} disabled={busy}><RefreshCw className={busy ? "spin" : ""} size={14} /></IconButton></div>{git ? <><dl className="fact-list"><div><dt>Availability</dt><dd>{git.availability}</dd></div><div><dt>Branch</dt><dd>{git.branch || "detached"}</dd></div><div><dt>Working tree</dt><dd>{git.dirty ? `${git.dirty_paths?.length ?? 0}${git.omitted_paths ? `+${git.omitted_paths}` : ""} changed paths` : "clean"}</dd></div><div><dt>Write mode</dt><dd>{git.write_mode}</dd></div></dl>{git.dirty_paths && git.dirty_paths.length > 0 && <div className="changed-paths">{git.dirty_paths.slice(0, 16).map((path) => <code key={path}>{path}</code>)}{git.dirty_paths.length > 16 && <small>+{git.dirty_paths.length - 16 + (git.omitted_paths ?? 0)} paths omitted from this view</small>}</div>}</> : <p>No checkout is loaded in this bounded scope.</p>}</section>{notice && <div className="notice" role="status">{notice}</div>}{agentRun ? <button className="secondary-button" onClick={() => inspectRun(agentRun)}><TerminalSquare size={15} />Open run details</button> : <div className="quiet-line"><Clock3 size={14} />No current run</div>}<footer><span>Definition revision {agent.revision}</span><span>{agent.enabled ? "Enabled" : "Disabled"}</span></footer></> : task && <><div className="inspector-status"><StatusPill value={task.task.status} /><span>Priority {task.task.priority}</span></div><section><h3>Description</h3><p>{task.task.description || "No additional description."}</p></section><section><h3>Readiness</h3><p>{task.task.status === "completed" ? "Task completed. It is no longer awaiting scheduling." : task.readiness.ready ? "Ready for Crewfold to schedule." : readableTaskReadiness(task, data.tasks)}</p></section><section><h3>Assignment</h3><p>{data.agents.find((candidate) => candidate.id === task.task.assigned_agent_id)?.name ?? (["completed", "failed", "cancelled"].includes(task.task.status) ? "Released after the task finished." : "Unassigned")}</p></section><footer><span>Revision {task.task.revision}</span><span>Updated {displayTime(task.task.updated_at)}</span></footer></>}
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
    <DomainConsole data={data} selectedAgentID={selectedDomainAgentID} selectAgent={(agent) => setSelectedDomainAgentID(agent?.definition.id ?? "")} selectProject={(id) => void selectProject(id)} inspectRun={inspectRun} apiBase={apiBase} csrf={csrf} mutable={fresh} reload={reload} />
    <Inspector data={data} task={selectedTask} run={selectedRun} agent={selectedAgent} apiBase={apiBase} csrf={csrf} close={() => { setSelectedTask(null); setSelectedRun(null); setSelectedAgent(null); }} reload={reload} inspectRun={inspectRun} mutable={fresh} />
  </div>;
}

createRoot(document.getElementById("root")!).render(<StrictMode><App /></StrictMode>);
