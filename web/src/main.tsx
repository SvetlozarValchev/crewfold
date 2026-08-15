import { StrictMode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { FitAddon } from "@xterm/addon-fit";
import { Terminal } from "@xterm/xterm";
import {
  Activity, AlertCircle, BookOpenText, Bot, Boxes, Check, CheckCircle2, ChevronDown, ChevronRight,
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
type Agent = { id: string; workspace_id: string; name: string; role: string; provider: string; runtime: string; enabled: boolean; revision: number };
type Objective = { id: string; project_id: string; title: string; status: string; revision: number; updated_at: string };
type Task = { id: string; project_id: string; objective_id?: string; title: string; description?: string; status: string; priority: number; revision: number; assigned_agent_id?: string; updated_at: string };
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
type OwnerInterpretation = { disposition: "answer" | "ready" | "clarify" | "refuse"; summary: string; answer: string; question: string; choices: OwnerChoice[]; objective_title: string; objective_budget: Budget; tasks: OwnerPlanTask[]; citation_refs: string[] };
type OwnerOperation = { id: string; ordinal: number; type: string; payload: Record<string, unknown>; policy_result: string; status: string; result_entity_type?: string; event_sequence?: number };
type OwnerTurnDetail = { conversation: { id: string; title: string }; turn: { id: string; ordinal: number; kind: "query" | "plan" | "act" | "review"; initiated_by: "owner" | "manager"; trigger_event_sequence?: number; instruction: string; status: string; answer?: string; as_of_event_sequence: number; completed_event_sequence?: number; revision: number; interpretation: OwnerInterpretation; citations: Array<{ ref: string; entity_type: string; entity_id: string; entity_revision: number; as_of_event_sequence: number; label: string }> }; operations: OwnerOperation[]; receipts: Array<{ operation_id: string; method: string; event_sequence?: number }> };
type OwnerManagerReview = { workspace_id: string; project_id: string; conversation_id: string; status: "idle" | "pending" | "leased" | "failed"; requested_event_sequence: number; reviewed_event_sequence: number; attempts: number; last_turn_id?: string; last_error?: string; updated_at: string };

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
  supervisorActions: SupervisorAction[];
  checks: CheckRunItem[];
  events: EventRecord[];
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
const emptyData: WorkbenchData = { workspaces: [], workspace: null, projects: [], project: null, checkouts: [], agents: [], launchProfiles: [], objectives: [], tasks: [], runs: [], approvals: [], supervisorActions: [], checks: [], events: [], highWater: 0 };

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

function readableRuntimeActivity(raw: string): RuntimeActivity[] {
  const clean = raw
    .replace(/\u001b\][^\u0007]*(?:\u0007|\u001b\\)/g, "")
    .replace(/\u001b\[[0-?]*[ -/]*[@-~]/g, "")
    .replace(/\r\n/g, "\n")
    .replace(/\r/g, "\n");
  const entries: RuntimeActivity[] = [];
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
        if (type === "item.completed" && item) {
          const itemType = typeof item.type === "string" ? item.type : "item";
          if (itemType === "agent_message") { add("Agent", item.text, "good"); continue; }
          if (itemType === "mcp_tool_call") { add("Crewfold tool", `${String(item.tool ?? "tool").replaceAll("crewfold_", "").replaceAll("_", " ")} · ${String(item.status ?? "completed")}`, item.status === "failed" ? "bad" : "live"); continue; }
          if (itemType === "command_execution") { add("Command", item.command ?? item.text ?? "Local command completed", item.status === "failed" ? "bad" : "quiet"); continue; }
        }
        continue;
      } catch {
        // A partial live JSON record is represented by its useful diagnostic below.
      }
    }
    if (/bwrap:|failed|error|not permitted|usage limit/i.test(line)) add("Runtime", line, "bad");
  }
  return entries.slice(-40);
}

function RuntimeOutput({ logs }: { logs: string }) {
  const activity = useMemo(() => readableRuntimeActivity(logs), [logs]);
  if (activity.length === 0) return <pre>{logs || "No runtime output was captured."}</pre>;
  return <><div className="runtime-activity">{activity.map((entry) => <article className={entry.tone} key={entry.key}><span>{entry.kind}</span><p>{entry.text}</p></article>)}</div><details className="raw-runtime-output"><summary>Show raw bounded provider output</summary><pre>{logs}</pre></details></>;
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

async function submitOwnerIntent(apiBase: string, csrf: string, body: Record<string, unknown>): Promise<OwnerTurnDetail> {
  const response = await fetch(`${apiBase}/intent`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-Crewfold-CSRF": csrf }, body: JSON.stringify(body) });
  if (response.status === 401) throw new Error("unauthorized");
  const value = (await response.json()) as { detail?: OwnerTurnDetail; error?: { message: string } };
  if (!response.ok || !value.detail) throw new Error(value.error?.message ?? `owner intent failed (${response.status})`);
  return value.detail;
}

async function executeOwnerPlan(apiBase: string, csrf: string, workspace: string, turnID: string): Promise<OwnerTurnDetail> {
  const response = await fetch(`${apiBase}/execute`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-Crewfold-CSRF": csrf }, body: JSON.stringify({ workspace, turn_id: turnID }) });
  const value = (await response.json()) as { detail?: OwnerTurnDetail; error?: { message: string } };
  if (!response.ok || !value.detail) throw new Error(value.error?.message ?? `plan execution failed (${response.status})`);
  return value.detail;
}

async function editOwnerPlan(apiBase: string, csrf: string, body: Record<string, unknown>): Promise<OwnerTurnDetail> {
  const response = await fetch(`${apiBase}/plan`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-Crewfold-CSRF": csrf }, body: JSON.stringify(body) });
  const value = (await response.json()) as { detail?: OwnerTurnDetail; error?: { message: string } };
  if (!response.ok || !value.detail) throw new Error(value.error?.message ?? `plan edit failed (${response.status})`);
  return value.detail;
}

async function submitOnboarding(apiBase: string, csrf: string, body: Record<string, unknown>): Promise<void> {
  const response = await fetch(`${apiBase}/onboarding`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-Crewfold-CSRF": csrf }, body: JSON.stringify(body) });
  if (response.status === 401) throw new Error("unauthorized");
  const value = (await response.json()) as { status?: string; error?: { message: string } };
  if (!response.ok || value.status !== "completed") throw new Error(value.error?.message ?? `onboarding failed (${response.status})`);
}

async function retryWorkbenchRun(apiBase: string, csrf: string, workspace: string, run: string): Promise<Run> {
  const response = await fetch(`${apiBase}/retry-run`, { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json", "X-Crewfold-CSRF": csrf }, body: JSON.stringify({ workspace, run, idempotency_key: newKey("run-retry") }) });
  const value = (await response.json()) as { detail?: { run: Run }; error?: { message: string } };
  if (!response.ok || !value.detail?.run) throw new Error(value.error?.message ?? `run retry failed (${response.status})`);
  return value.detail.run;
}

async function loadOwnerConversation(apiBase: string, workspace: string, project: string): Promise<{ turns: OwnerTurnDetail[]; review: OwnerManagerReview | null }> {
  const response = await fetch(`${apiBase}/conversation?workspace=${encodeURIComponent(workspace)}&project=${encodeURIComponent(project)}`, { credentials: "same-origin" });
  if (!response.ok) throw new Error(`conversation read failed (${response.status})`);
  const value = (await response.json()) as { turns?: OwnerTurnDetail[]; review?: OwnerManagerReview | null };
  return { turns: value.turns ?? [], review: value.review ?? null };
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
  const [checkouts, checks, launchProfiles] = project ? await Promise.all([
    rpc<{ checkouts: Checkout[] }>(apiBase, csrf, "checkout.list", { workspace: workspace.id, project: project.id }).then((value) => value.checkouts),
    rpc<{ runs: CheckRunItem[] } & Page>(apiBase, csrf, "check.list", { workspace: workspace.id, project: project.id, limit: 200 }).then((value) => value.runs),
    rpc<{ profiles: LaunchProfile[] }>(apiBase, csrf, "launch_profile.list", { workspace: workspace.id, project: project.id, status: "active", limit: 100 }).then((value) => value.profiles),
  ]) : [[], [], []];
  const after = await rpc<{ events: EventRecord[]; high_water: number } & Page>(apiBase, csrf, "events.list", { workspace: workspace.id, after: before.high_water, limit: 1 });
  if (after.high_water !== before.high_water) {
    if (attempt >= 2) throw new Error("Canonical state kept changing during refresh; retry when the current event cut settles.");
    return loadWorkbench(apiBase, csrf, workspace.id, project?.id ?? preferredProject, attempt + 1);
  }
  return {
    workspaces: workspacePage.workspaces, workspace, projects: projectPage.projects, project, checkouts,
    agents: agentPage.agents, launchProfiles, objectives: objectivePage.objectives, tasks: taskPage.tasks,
    runs: runPage.runs, approvals: approvalPage.approvals, supervisorActions: supervisorActionPage.actions, checks, events: eventPage.events,
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

function EmptyState({ icon: Icon, title, detail }: { icon: typeof Inbox; title: string; detail: string }) {
  return <div className="empty-state"><Icon size={28} aria-hidden="true" /><h3>{title}</h3><p>{detail}</p></div>;
}

function Onboarding({ apiBase, csrf, onComplete }: { apiBase: string; csrf: string; onComplete: () => Promise<void> }) {
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
      <p>Crewfold inspects the Git repository through the daemon, records one exact checkout, and creates your first agent definition. Provider credentials stay in their native CLI home and never enter the browser.</p>
      <div className="onboarding-trust">
        <div><ShieldCheck size={19} /><span><strong>Local authority</strong>Loopback web, Unix socket, private database</span></div>
        <div><GitBranch size={19} /><span><strong>Existing repository</strong>No clone, move, or rewrite</span></div>
        <div><Bot size={19} /><span><strong>Subscription login</strong>Codex or Claude CLI, no API key form</span></div>
      </div>
    </section>
    <form className="onboarding-form" onSubmit={submit}>
      <div className="form-heading"><div className="step-mark">1</div><div><h2>Set up your workbench</h2><p>You can change agent policy later.</p></div></div>
      <label><span>Repository path</span><input required value={path} onChange={(event) => updatePath(event.target.value)} placeholder="~/depot/dev/world-engine-2" autoComplete="off" /></label>
      <div className="field-grid">
        <label><span>Workspace</span><input required pattern="[a-z][a-z0-9-]{0,62}" value={workspace} onChange={(event) => setWorkspace(event.target.value)} /></label>
        <label><span>Project</span><input required pattern="[a-z][a-z0-9-]{0,62}" value={project} onChange={(event) => setProject(event.target.value)} placeholder="world-engine-2" /></label>
      </div>
      <label><span>First agent</span><input required pattern="[a-z][a-z0-9-]{0,62}" value={agent} onChange={(event) => setAgent(event.target.value)} /></label>
      <label><span>Provider</span><select value={provider} onChange={(event) => setProvider(event.target.value)}><option value="codex">Codex subscription</option><option value="claude">Claude subscription</option><option value="fixture-mcp">Local fixture</option></select></label>
      <div className="runtime-primary"><TerminalSquare size={19} /><div><strong>Herdr interactive runtime</strong><span>Persistent agent terminal hosted beside Crewfold's canonical state.</span></div><StatusPill value={runtime === "herdr" ? "recommended" : "fallback"} /></div>
      <details className="advanced-runtime"><summary>Advanced runtime fallback</summary><label><span>Execution runtime</span><select value={runtime} onChange={(event) => setRuntime(event.target.value)}><option value="herdr">Herdr interactive · recommended</option><option value="direct">Direct headless · CI and automation</option></select></label><p>Direct has bounded logs but no persistent interactive terminal.</p></details>
      {error && <div className="form-error" role="alert"><AlertCircle size={17} />{error}</div>}
      <button className="primary-button" disabled={busy}>{busy ? <LoaderCircle className="spin" size={17} /> : <Command size={17} />} {busy ? "Inspecting and recording…" : "Create workbench"}</button>
      <p className="form-note">One submission records the workspace, project checkout, and agent with replay-safe idempotency.</p>
    </form>
  </main>;
}

function PlanEditor({ detail, agents, profiles, disabled, save }: { detail: OwnerTurnDetail; agents: Agent[]; profiles: LaunchProfile[]; disabled: boolean; save: (body: Record<string, unknown>) => Promise<void> }) {
  const interpretation = detail.turn.interpretation;
  const [open, setOpen] = useState(false);
  const [objectiveTitle, setObjectiveTitle] = useState(interpretation.objective_title);
  const [objectiveBudget, setObjectiveBudget] = useState<Budget>(interpretation.objective_budget);
  const [tasks, setTasks] = useState<OwnerPlanTask[]>(interpretation.tasks.map((task) => ({ ...task, depends_on: task.depends_on ?? [] })));
  const [busy, setBusy] = useState(false);
  const updateTask = (index: number, patch: Partial<OwnerPlanTask>) => setTasks((current) => current.map((task, taskIndex) => taskIndex === index ? { ...task, ...patch } : task));
  const submit = async (event: React.FormEvent) => {
    event.preventDefault(); setBusy(true);
    try {
      await save({ turn_id: detail.turn.id, expected_revision: detail.turn.revision, objective_title: objectiveTitle.trim(), objective_budget: objectiveBudget, tasks: tasks.map((task) => ({ ...task, title: task.title.trim(), description: task.description.trim() })) });
      setOpen(false);
    } catch {
      // The parent renders the exact server diagnosis beside the plan.
    } finally { setBusy(false); }
  };
  if (!open) return <button className="edit-plan" disabled={disabled} onClick={() => setOpen(true)}><Settings size={13} />Edit objective, graph, profiles, and budgets</button>;
  return <form className="plan-editor multi-plan-editor" onSubmit={submit}>
    <label><span>Objective</span><input required maxLength={512} value={objectiveTitle} onChange={(event) => setObjectiveTitle(event.target.value)} /></label>
    <div className="plan-editor-grid budget-grid"><label><span>Objective token limit</span><input type="number" min="0" max="1000000" value={objectiveBudget.token_limit} onChange={(event) => setObjectiveBudget({ ...objectiveBudget, token_limit: Number(event.target.value) })} /></label><label><span>Paid cost cents</span><input type="number" value={0} disabled /></label><label><span>Objective time seconds</span><input type="number" min="0" max="86400" value={objectiveBudget.time_seconds} onChange={(event) => setObjectiveBudget({ ...objectiveBudget, time_seconds: Number(event.target.value) })} /></label></div>
    <div className="plan-task-list">{tasks.map((task, index) => { const profile = profiles.find((candidate) => candidate.id === task.launch_profile_id); const agent = agents.find((candidate) => candidate.id === profile?.agent_id); return <fieldset key={task.key}><legend>{index + 1}. {task.key}</legend><label><span>Task title</span><input required maxLength={512} value={task.title} onChange={(event) => updateTask(index, { title: event.target.value })} /></label><label><span>Description</span><textarea maxLength={4096} value={task.description} onChange={(event) => updateTask(index, { description: event.target.value })} /></label><div className="plan-editor-grid"><label><span>Launch profile</span><select value={task.launch_profile_id} onChange={(event) => updateTask(index, { launch_profile_id: event.target.value })}>{profiles.map((candidate) => <option key={candidate.id} value={candidate.id}>{agents.find((item) => item.id === candidate.agent_id)?.name ?? "Agent"} · {candidate.purpose || "work"} · {candidate.provider}/{candidate.runtime}</option>)}</select></label><label><span>Priority</span><input type="number" min="0" max="1000" value={task.priority} onChange={(event) => updateTask(index, { priority: Number(event.target.value) })} /></label></div><label><span>Depends on task keys</span><input value={task.depends_on.join(", ")} onChange={(event) => updateTask(index, { depends_on: event.target.value.split(",").map((value) => value.trim()).filter(Boolean) })} placeholder="foundation, api" /></label><small>{agent ? `Runs as ${agent.name}; role labels do not grant authority.` : "Select a current launch profile."}</small></fieldset>; })}</div>
    <div className="plan-editor-actions"><button type="button" className="secondary-button" onClick={() => setOpen(false)}>Cancel</button><button className="primary-button compact" disabled={busy || disabled || tasks.length === 0 || tasks.some((task) => !task.launch_profile_id)}>{busy ? <LoaderCircle className="spin" size={13} /> : <Check size={13} />}Seal reviewed graph revision</button></div>
  </form>;
}

function PlanReview({ detail, agents, profiles }: { detail: OwnerTurnDetail; agents: Agent[]; profiles: LaunchProfile[] }) {
  const tasks = detail.turn.interpretation.tasks;
  if (tasks.length === 0) return null;
  return <section className="plan-review" aria-label={`${tasks.length}-task reviewed plan`}><header><div><strong>{tasks.length} actual tasks</strong><small>{detail.turn.interpretation.objective_title}</small></div><StatusPill value={detail.turn.status} /></header><ol>{tasks.map((task, index) => { const profile = profiles.find((candidate) => candidate.id === task.launch_profile_id); const agent = agents.find((candidate) => candidate.id === profile?.agent_id); const dependencies = task.depends_on ?? []; return <li key={task.key}><span>{index + 1}</span><div><strong>{task.title}</strong><small>{agent?.name ?? "Current launch profile"} · {dependencies.length > 0 ? `after ${dependencies.join(", ")}` : "starts first"}</small></div></li>; })}</ol></section>;
}

function ManagerDecisionCard({ detail, actionable, busy, mutable, choose }: { detail: OwnerTurnDetail; actionable: boolean; busy: boolean; mutable: boolean; choose: (choice: OwnerChoice) => void }) {
  return <section className={`manager-decision-card ${actionable ? "" : "resolved"}`}><header><span className="decision-icon">?</span><div><strong>{actionable ? "Decision needed" : "Earlier decision"}</strong><small>{actionable ? "Selecting an answer creates a reviewed plan. Nothing executes until you approve that plan." : "Later conversation activity superseded this question; its choices are now inert."}</small></div></header><h3>{detail.turn.interpretation.question}</h3><div className="manager-choices">{detail.turn.interpretation.choices.map((choice) => <button key={choice.key} disabled={!actionable || !mutable || busy} onClick={() => choose(choice)}><span><strong>{choice.label}</strong>{choice.recommended && <em>Recommended</em>}</span><small>{choice.description}</small><span className="choose-label">{actionable ? "Review this response" : "Superseded"}</span></button>)}</div></section>;
}

function WorkbenchView({ data, apiBase, csrf, reload, selectTask, selectRun, mutable }: { data: WorkbenchData; apiBase: string; csrf: string; reload: () => Promise<void>; selectTask: (task: TaskDetail) => void; selectRun: (run: Run) => void; mutable: boolean }) {
  const [instruction, setInstruction] = useState("");
  const [mode, setMode] = useState<"query" | "plan" | "act">("act");
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [turns, setTurns] = useState<OwnerTurnDetail[]>([]);
  const [managerReview, setManagerReview] = useState<OwnerManagerReview | null>(null);
  const recovering = useRef(false);
  const pendingKey = data.workspace && data.project ? `crewfold_pending_intent_${data.workspace.id}_${data.project.id}` : "";
  useEffect(() => {
    if (!data.workspace || !data.project) return;
    let active = true;
    let timer = 0;
    const poll = async () => {
      try {
        const page = await loadOwnerConversation(apiBase, data.workspace!.id, data.project!.id);
        if (!active) return;
        setTurns(page.turns); setManagerReview(page.review);
        timer = window.setTimeout(() => void poll(), page.review && ["pending", "leased"].includes(page.review.status) ? 750 : 4000);
      } catch {
        if (!active) return;
        timer = window.setTimeout(() => void poll(), 4000);
      }
    };
    void poll();
    return () => { active = false; window.clearTimeout(timer); };
  }, [apiBase, data.project?.id, data.workspace?.id]);
  useEffect(() => {
    if (!pendingKey || recovering.current) return;
    const raw = sessionStorage.getItem(pendingKey);
    if (!raw) return;
    recovering.current = true; setBusy(true); setNotice("Recovering the exact interrupted owner turn…");
    try {
      const body = JSON.parse(raw) as Record<string, unknown>;
      void submitOwnerIntent(apiBase, csrf, body).then(async (detail) => {
        sessionStorage.removeItem(pendingKey);
        setTurns((current) => [...current.filter((item) => item.turn.id !== detail.turn.id), detail]);
        setNotice("Recovered the exact durable turn without reinterpretation or duplicate effects.");
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
      const body = { workspace: data.workspace.id, project: data.project.id, ...(currentConversation ? { conversation_id: currentConversation } : {}), instruction: instruction.trim(), mode, idempotency_key: newKey("intent") };
      sessionStorage.setItem(pendingKey, JSON.stringify(body));
      const detail = await submitOwnerIntent(apiBase, csrf, body);
      sessionStorage.removeItem(pendingKey);
      setInstruction(""); setNotice("The manager returned a typed result bound to the current canonical event cut.");
      setTurns((current) => [...current, detail]);
      if (mode === "query") setNotice("Answered from the frozen canonical event cut without creating a domain event.");
      if (mode === "plan") setNotice(`Frozen a dependency-aware ${detail.turn.interpretation.tasks.length}-task plan. No effect has executed.`);
      if (mode === "act") setNotice(detail.turn.interpretation.disposition === "clarify" ? "The manager paused before every effect and raised a bounded decision." : detail.turn.interpretation.disposition === "refuse" ? "The manager refused an operation outside the local action grammar; no effect ran." : `Committed ${detail.receipts.length} exact graph receipts; the supervisor now owns scheduling.`);
      await reload();
    } catch (reason) { setNotice(reason instanceof Error ? reason.message : "The instruction could not be committed."); }
    finally { setBusy(false); }
  };
  const executePlan = async (turnID: string) => {
    if (!mutable || !data.workspace) return;
    setBusy(true); setNotice("");
    try {
      const executed = await executeOwnerPlan(apiBase, csrf, data.workspace.id, turnID);
      setTurns((current) => current.map((turn) => turn.turn.id === turnID ? executed : turn));
      setNotice("Committed the frozen graph exactly; the supervisor now schedules dependency-ready work through its launch profiles.");
      await reload();
    } catch (reason) { setNotice(reason instanceof Error ? reason.message : "The frozen plan could not execute."); }
    finally { setBusy(false); }
  };
  const answerManagerChoice = async (detail: OwnerTurnDetail, choice: OwnerChoice) => {
    if (!mutable || !data.workspace || !data.project) return;
    setBusy(true); setNotice("");
    try {
      const body = { workspace: data.workspace.id, project: data.project.id, conversation_id: detail.conversation.id, instruction: `Selected answer for "${detail.turn.interpretation.question}": ${choice.label}. ${choice.description} Produce a reviewed plan only; do not execute it.`.trim(), mode: "plan", idempotency_key: newKey(`manager-choice-${choice.key}`) };
      const next = await submitOwnerIntent(apiBase, csrf, body);
      setTurns((current) => [...current, next]);
      setNotice(next.turn.interpretation.disposition === "clarify" ? "The manager needs one more bounded decision." : next.turn.status === "planned" ? "The selected answer produced a reviewed plan. Nothing has executed; inspect the graph before choosing Execute." : "The selected answer was recorded without creating execution effects.");
      await reload();
    } catch (reason) { setNotice(reason instanceof Error ? reason.message : "The manager decision could not be processed."); }
    finally { setBusy(false); }
  };
  const savePlan = async (detail: OwnerTurnDetail, draft: Record<string, unknown>) => {
    if (!mutable || !data.workspace) return;
    setNotice("");
    try {
      const edited = await editOwnerPlan(apiBase, csrf, { workspace: data.workspace.id, ...draft });
      setTurns((current) => current.map((turn) => turn.turn.id === detail.turn.id ? edited : turn));
      setNotice(`Sealed reviewed plan revision ${edited.turn.revision}; no domain effect has executed.`);
    } catch (reason) {
      setNotice(reason instanceof Error ? reason.message : "The plan edit could not be sealed.");
      throw reason;
    }
  };
  const activeRuns = data.runs.filter((run) => ["requested", "starting", "active", "blocked", "stopping"].includes(run.status));
  const attentionRuns = data.runs.filter((run) => ["requested", "starting", "active", "blocked", "stopping", "start_failed", "failed", "lost"].includes(run.status)).sort((left, right) => right.updated_at.localeCompare(left.updated_at) || right.id.localeCompare(left.id));
  const openTasks = data.tasks.filter(({ task }) => !["completed", "failed", "cancelled"].includes(task.status));
  return <div className="view-grid workbench-view">
    <section className="conversation-panel panel">
      <div className="panel-heading"><div><div className="eyebrow"><Sparkles size={13} />Owner instruction</div><h1>What should the crew accomplish?</h1></div><StatusPill value="local" /></div>
      <div className="conversation-intro">
        <div className="assistant-avatar"><Bot size={22} /></div>
        <div><strong>Crewfold</strong><p>I’ll answer from canonical state, freeze an editable plan, or execute a clearly authorized local goal. Destructive, publication, external, credential, budget, and authority changes stop before their first effect.</p></div>
      </div>
      {managerReview && <div className={`manager-review-state ${managerReview.status}`}><Bot size={15} /><span><strong>{managerReview.status === "leased" ? "Manager is reviewing worker activity" : managerReview.status === "pending" ? "Worker activity queued for manager review" : managerReview.status === "failed" ? "Manager review needs attention" : "Manager is caught up"}</strong><small>{managerReview.status === "failed" ? managerReview.last_error : `reviewed #${managerReview.reviewed_event_sequence} · requested #${managerReview.requested_event_sequence}`}</small></span></div>}
      {turns.length > 0 && <div className="conversation-history">{turns.slice(-6).map((detail) => <div className="turn" key={detail.turn.id}>
        {detail.turn.initiated_by === "owner" ? <div className="owner-message"><strong>You</strong><p>{detail.turn.instruction}</p><span>{detail.turn.kind}</span></div> : <div className="manager-trigger"><Activity size={13} /><span>Proactive review of worker activity through event #{detail.turn.trigger_event_sequence}</span></div>}
        <div className="crew-message"><span className="assistant-avatar"><Bot size={16} /></span><div><strong>Crewfold manager</strong><p>{detail.turn.answer ?? detail.turn.interpretation.summary ?? (detail.turn.status === "planned" ? `Frozen ${detail.operations.length} operations; no effects executed.` : `${detail.receipts.length} exact effects committed.`)}</p>
          <div className="receipt-line"><ShieldCheck size={13} />{detail.turn.interpretation.disposition} · {detail.turn.status} · event cut #{detail.turn.completed_event_sequence ?? detail.turn.as_of_event_sequence}</div>
          {detail.turn.citations.length > 0 && <div className="citation-list" aria-label="Canonical answer citations">{detail.turn.citations.map((citation) => <span key={citation.ref}>{citation.label} · r{citation.entity_revision}</span>)}</div>}
          {detail.turn.interpretation.disposition === "clarify" && <ManagerDecisionCard detail={detail} actionable={turns.at(-1)?.turn.id === detail.turn.id} busy={busy} mutable={mutable} choose={(choice) => void answerManagerChoice(detail, choice)} />}
          <PlanReview detail={detail} agents={data.agents} profiles={data.launchProfiles} />
          {detail.operations.length > 0 && <details className="operation-details"><summary>{detail.operations.length} typed execution operations</summary><p>Internal objective, task, dependency, and scheduling receipts. These are not additional tasks.</p><ol className="operation-list" aria-label="Frozen typed execution operations">{detail.operations.map((operation) => <li key={operation.id}><span>{operation.ordinal}</span><strong>{operation.type.replaceAll("_", " ")}</strong><StatusPill value={operation.status} /></li>)}</ol></details>}
          {detail.turn.status === "planned" && <PlanEditor key={detail.turn.revision} detail={detail} agents={data.agents} profiles={data.launchProfiles} disabled={!mutable || busy} save={(draft) => savePlan(detail, draft)} />}
          {detail.turn.status === "planned" && <button className="execute-plan" disabled={!mutable || busy} onClick={() => void executePlan(detail.turn.id)}><Play size={13} />Execute reviewed graph</button>}
        </div></div>
      </div>)}</div>}
      <form className="composer" onSubmit={createWork}>
        <textarea value={instruction} onChange={(event) => setInstruction(event.target.value)} maxLength={4096} placeholder="Build the first playable world loop and organize the work…" aria-label="Owner instruction" />
        <div className="composer-footer"><div className="intent-mode" aria-label="Instruction mode">{(["query", "plan", "act"] as const).map((value) => <button type="button" key={value} className={mode === value ? "selected" : ""} onClick={() => setMode(value)}>{value}</button>)}</div><span>{instruction.length}/4096</span><button className="submit-intent" disabled={!mutable || busy || !instruction.trim() || !data.project}>{busy ? <LoaderCircle className="spin" size={17} /> : <Send size={17} />}{mode === "act" ? "Do it" : mode === "plan" ? "Plan" : "Ask"}</button></div>
      </form>
      {notice && <div className="notice" role="status"><CheckCircle2 size={17} />{notice}</div>}
      <div className="effect-note"><ShieldCheck size={16} /><span><strong>No hidden effects.</strong> Query is read-only, Plan freezes operations, and Act returns exact receipts only after permitted effects commit.</span></div>
    </section>
    <aside className="right-stack">
      <section className="panel metric-panel"><div className="panel-label"><Workflow size={16} />Current work</div><div className="metric-row"><div><strong>{openTasks.length}</strong><span>open tasks</span></div><div><strong>{activeRuns.length}</strong><span>live runs</span></div><div><strong>{data.approvals.filter((item) => item.status === "pending").length}</strong><span>decisions</span></div></div></section>
      <section className="panel compact-list"><div className="panel-title"><h2>Agents and launch attention</h2><button onClick={() => void reload()} aria-label="Refresh workbench"><RefreshCw size={15} /></button></div>{attentionRuns.length === 0 ? <EmptyState icon={Bot} title="No run needs attention" detail="There is no live, failed, or unresolved agent run in this project." /> : attentionRuns.slice(0, 5).map((run) => <button className="list-row" key={run.id} onClick={() => selectRun(run)}><span className="row-icon"><Bot size={16} /></span><span><strong>{data.agents.find((agent) => agent.id === run.agent_id)?.name ?? "Agent"}</strong><small>{run.status === "start_failed" ? run.failure_message ?? "Launch failed; inspect and retry." : run.status === "failed" ? `${data.tasks.find((task) => task.task.id === run.task_id)?.task.title ?? "Assigned work"} · inspect failure output` : run.status === "lost" ? "Runtime outcome is unknown; owner resolution is required." : data.tasks.find((task) => task.task.id === run.task_id)?.task.title ?? "Assigned work"}</small></span><StatusPill value={run.status} /></button>)}</section>
    </aside>
    <section className="panel task-strip full-span"><div className="panel-title"><div><h2>Next work</h2><p>Canonical task state, ordered by Crewfold.</p></div><span>{data.tasks.length} total</span></div>{openTasks.length === 0 ? <EmptyState icon={ListChecks} title="No work recorded yet" detail="Describe the outcome above to create the first objective and task." /> : <div className="task-cards">{openTasks.slice(0, 6).map((detail) => <button key={detail.task.id} className="task-card" onClick={() => selectTask(detail)}><div><StatusPill value={detail.task.status} /><span className="priority">P{detail.task.priority}</span></div><strong>{detail.task.title}</strong><small>{data.agents.find((agent) => agent.id === detail.task.assigned_agent_id)?.name ?? "Unassigned"} · updated {displayTime(detail.task.updated_at)}</small><ChevronRight size={16} /></button>)}</div>}</section>
  </div>;
}

function GraphView({ data, selectTask }: { data: WorkbenchData; selectTask: (task: TaskDetail) => void }) {
  const columns = ["ready", "assigned", "active", "blocked", "review", "completed"];
  return <section className="panel page-panel"><div className="page-heading"><div><div className="eyebrow"><Network size={13} />Canonical graph</div><h1>Work graph</h1><p>Objectives, dependencies, assignments, and current task state.</p></div><span>{data.tasks.length} tasks</span></div>
    {data.tasks.length === 0 ? <EmptyState icon={Workflow} title="The graph is empty" detail="Create work from the Workbench to populate it." /> : <div className="kanban">{columns.map((column) => <div className="kanban-column" key={column}><header><StatusPill value={column} /><span>{data.tasks.filter(({ task }) => task.status === column).length}</span></header>{data.tasks.filter(({ task }) => task.status === column).map((detail) => <button key={detail.task.id} onClick={() => selectTask(detail)}><strong>{detail.task.title}</strong><small>{detail.dependencies.length ? `${detail.dependencies.length} dependencies · ${detail.readiness.reason}` : detail.readiness.ready ? "Ready to proceed" : detail.readiness.reason || "Not ready"}</small></button>)}</div>)}</div>}
  </section>;
}

function CrewView({ data, selectAgent }: { data: WorkbenchData; selectAgent: (agent: Agent) => void }) {
  return <section className="panel page-panel"><div className="page-heading"><div><div className="eyebrow"><Users size={13} />Runtime roster</div><h1>Crew</h1><p>Definitions and their latest canonical run—not terminal folklore.</p></div><span>{data.agents.length} agents</span></div>
    {data.agents.length === 0 ? <EmptyState icon={Bot} title="No agents configured" detail="Add the first provider/runtime definition during onboarding." /> : <div className="crew-grid">{data.agents.map((agent) => { const run = latestRunForAgent(data.runs, agent.id); return <article className="crew-card" key={agent.id}><div className="crew-card-top"><span className="agent-avatar"><Bot size={20} /></span><StatusPill value={run?.status ?? (agent.enabled ? "ready" : "disabled")} /></div><h2>{agent.name}</h2><p>{agent.role}</p><dl className="fact-list"><div><dt>Provider</dt><dd>{agent.provider}</dd></div><div><dt>Runtime</dt><dd>{agent.runtime}</dd></div><div><dt>Concurrency</dt><dd>policy bound</dd></div></dl><button className="secondary-button" onClick={() => selectAgent(agent)}><Search size={15} />Inspect agent{run ? " and run" : ""}</button></article>; })}</div>}
  </section>;
}

function supervisorResponseLabel(action?: SupervisorAction) {
  if (!action) return "Supervisor action";
  if (action.response === "request_owner") return action.condition === "failed" ? "Acknowledge a failed run" : "Owner acknowledgement required";
  if (action.response === "resume_run") return "Resume a blocked run";
  return action.response.replaceAll("_", " ");
}

function supervisorDecisionLabels(action?: SupervisorAction) {
  if (action?.response === "resume_run") return { allow: "Resume this run", deny: "Keep blocked" };
  if (action?.response === "request_owner") return { allow: "Acknowledge", deny: "Dismiss" };
  return { allow: "Allow", deny: "Deny" };
}

function supervisorConsequence(action?: SupervisorAction) {
  if (action?.response === "resume_run") return "Resuming only releases this exact blocked run to continue in its existing runtime. It does not repair Herdr, replace the execution profile, or create a fresh sandbox.";
  if (action?.response === "request_owner") return "Acknowledging records that you reviewed this terminal failure. It does not retry the run or change the failed task.";
  return "Allowing commits only the exact requested supervisor response shown above.";
}

function snapshotText(action: SupervisorAction, name: string) {
  const value = action.constraint_snapshot[name];
  return typeof value === "string" && value.trim() ? value : "";
}

function DecisionsView({ data, apiBase, csrf, reload, mutable }: { data: WorkbenchData; apiBase: string; csrf: string; reload: () => Promise<void>; mutable: boolean }) {
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const approvals = data.approvals.filter((item) => !data.project || item.project_id === data.project.id);
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
  return <section className="panel page-panel">
    <div className="page-heading"><div><div className="eyebrow"><ClipboardCheck size={13} />Policy gates</div><h1>Decisions</h1><p>Exact conditions, requested responses, affected work, and recorded owner choices.</p></div><span>{approvals.filter((item) => item.status === "pending").length} pending</span></div>
    {error && <div className="form-error" role="alert"><AlertCircle size={16} />{error}</div>}
    {approvals.length === 0 ? <EmptyState icon={ShieldCheck} title="Nothing needs approval" detail="Gated operations will appear here with their exact requested effect." /> : <div className="decision-list">{approvals.map((approval) => {
      const action = data.supervisorActions.find((item) => item.id === approval.action_id);
      const task = data.tasks.find((item) => item.task.id === action?.task_id)?.task;
      const run = data.runs.find((item) => item.id === action?.run_id);
      const agent = data.agents.find((item) => item.id === action?.agent_id);
      const labels = supervisorDecisionLabels(action);
      const blockedQuestion = action ? snapshotText(action, "blocked_question") || snapshotText(action, "question") : "";
      return <article className="decision-card" key={approval.id}>
        <header><span className="row-icon"><ShieldCheck size={17} /></span><div><strong>{supervisorResponseLabel(action)}</strong><small>{task?.title ?? "Exact governed target unavailable in this bounded page"}</small></div><StatusPill value={approval.status} /></header>
        {action ? <>
          <dl className="decision-facts"><div><dt>Condition</dt><dd>{action.condition.replaceAll("_", " ")}</dd></div><div><dt>Requested response</dt><dd>{action.response.replaceAll("_", " ")}</dd></div>{agent && <div><dt>Agent</dt><dd>{agent.name}</dd></div>}{run && <div><dt>Current run state</dt><dd>{run.status.replaceAll("_", " ")}</dd></div>}</dl>
          <div className="decision-reasons"><strong>Why Crewfold paused</strong><ul>{action.reasons.map((reason) => <li key={reason}>{reason}</li>)}</ul></div>
          {blockedQuestion && <div className="decision-question"><strong>Agent’s exact blocker</strong><p>{blockedQuestion}</p></div>}
          <p className="decision-consequence"><strong>Exact effect:</strong> {supervisorConsequence(action)}</p>
        </> : <p className="decision-unavailable">The action detail is outside this bounded page; refresh before deciding.</p>}
        {approval.status === "pending" ? <div className="row-actions"><button className="secondary-button" disabled={!mutable || busy === approval.id || !action} onClick={() => void decide(approval, action, "deny")}><XCircle size={14} />{labels.deny}</button><button className="primary-button compact" disabled={!mutable || busy === approval.id || !action} onClick={() => void decide(approval, action, "allow")}>{busy === approval.id ? <LoaderCircle className="spin" size={14} /> : <Check size={14} />}{labels.allow}</button></div> : <div className="decision-record"><CheckCircle2 size={15} /><span><strong>Recorded owner decision</strong>{action?.decision ?? approval.decision_note ?? approval.status.replaceAll("_", " ")} · {displayTime(approval.decided_at ?? approval.updated_at)}</span></div>}
      </article>;
    })}</div>}
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
  return <section className="panel page-panel"><div className="page-heading"><div><div className="eyebrow"><Stethoscope size={13} />Local service</div><h1>Health and recovery</h1><p>Read-only physical, canonical, queue, artifact, and resource diagnosis.</p></div><button className="primary-button compact" onClick={() => void inspect()} disabled={busy}>{busy ? <LoaderCircle className="spin" size={14} /> : <HeartPulse size={14} />}Run full doctor</button></div><div className="health-overview"><div><strong>{status?.pid ?? "—"}</strong><span>daemon PID</span></div><div><strong>{Math.floor((status?.uptime_ms ?? 0) / 1000)}s</strong><span>uptime</span></div><div><strong>{doctor?.event_sequence ?? "—"}</strong><span>event high-water</span></div><div><strong>{doctor?.status ?? "not run"}</strong><span>full diagnosis</span></div></div>{error && <div className="form-error"><AlertCircle size={16} />{error}</div>}{doctor && <div className="doctor-grid">{doctor.checks.map((check) => <article key={check.code}><StatusPill value={check.status} /><strong>{check.code.replaceAll("_", " ")}</strong><p>{check.summary}</p><small>{check.issue_count} issues</small></article>)}</div>}</section>;
}

function LiveTerminal({ apiBase, csrf, workspace, run, close, mutable }: { apiBase: string; csrf: string; workspace: string; run: Run; close: () => void; mutable: boolean }) {
  const [state, setState] = useState("connecting");
  const [input, setInput] = useState("");
  const [followOutput, setFollowOutput] = useState(true);
  const [unseenOutput, setUnseenOutput] = useState(false);
  const host = useRef<HTMLDivElement | null>(null);
  const socket = useRef<WebSocket | null>(null);
  const terminal = useRef<Terminal | null>(null);
  const fit = useRef<FitAddon | null>(null);
  const follow = useRef(true);
  const controlsEnabled = useRef(mutable);
  const decoder = useRef(new TextDecoder());

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
    if (!host.current) return;
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
  }, [sendInput, sendSize]);

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
  return <section className="live-terminal" aria-label="Live Herdr terminal"><div className="section-title"><h3>Raw interactive terminal</h3><span><CircleDot size={11} />{state}</span></div><p>The readable activity is above. This raw Herdr terminal is retained for direct inspection and input.</p>{!mutable && <div className="terminal-paused">Canonical state is refreshing; output remains connected while input is temporarily disabled.</div>}<div className="terminal-toolbar"><span>Click the terminal to type · wheel to inspect scrollback</span><button type="button" className={followOutput ? "follow-active" : ""} onClick={toggleFollow}><ChevronDown size={13} />{unseenOutput ? "New output" : followOutput ? "Following" : "Follow output"}</button></div><div ref={host} className="terminal-canvas" role="application" aria-label="Interactive Herdr terminal output" /><form onSubmit={(event) => { event.preventDefault(); sendInput(input + "\r"); setInput(""); }}><input aria-label="Terminal input" value={input} maxLength={4095} onChange={(event) => setInput(event.target.value)} placeholder="Paste or send terminal input…" disabled={!mutable} /><button className="secondary-button" disabled={!mutable || state !== "connected" || !input}><Send size={14} />Send</button><button type="button" className="secondary-button" disabled={!mutable || state !== "connected"} onClick={() => sendInput("\u0003")}><AlertCircle size={14} />Ctrl-C</button><button type="button" className="secondary-button" onClick={close}><X size={14} />Close</button></form></section>;
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
  useEffect(() => { setLogs(""); if (!run) return; void rpc<{ logs: { stdout: { text: string; truncated: boolean; omitted_bytes: number }; stderr: { text: string; truncated: boolean; omitted_bytes: number }; state: string } }>(apiBase, csrf, "run.logs", { workspace: data.workspace?.id, run: run.id, tail: 160 }).then((result) => setLogs([result.logs.stdout.text, result.logs.stderr.text, result.logs.stdout.truncated || result.logs.stderr.truncated ? "[bounded log output; earlier bytes omitted]" : ""].filter(Boolean).join("\n"))).catch((error) => setLogs(error instanceof Error ? error.message : "Logs unavailable")); }, [apiBase, csrf, data.workspace?.id, run?.id, data.highWater]);
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
  const retry = async () => { if (!mutable || !currentRun || !data.workspace || currentRun.status !== "start_failed") return; setBusy(true); setNotice(""); try { const freshRun = await retryWorkbenchRun(apiBase, csrf, data.workspace.id, currentRun.id); setNotice("A fresh run was requested after the runtime and provider preflight passed."); await reload(); inspectRun(freshRun); } catch (reason) { setNotice(reason instanceof Error ? reason.message : "Retry failed."); } finally { setBusy(false); } };
  const refreshGit = async () => { if (!data.workspace || !data.project) return; setBusy(true); setNotice(""); try { const response = await fetch(`${apiBase}/git?workspace=${encodeURIComponent(data.workspace.id)}&project=${encodeURIComponent(data.project.id)}`, { credentials: "same-origin" }); const result = (await response.json()) as { observations?: Array<Omit<Checkout, "id" | "project_id" | "path" | "write_mode"> & { checkout_id: string }>; error?: { message: string } }; if (!response.ok || !result.observations) throw new Error(result.error?.message ?? "Git observation failed"); const observation = result.observations[0]; const canonical = data.checkouts.find((checkout) => checkout.id === observation?.checkout_id); setGit(observation && canonical ? { ...canonical, ...observation, id: observation.checkout_id } : null); setNotice("Repository status refreshed without persisting source or diff content."); } catch (reason) { setNotice(reason instanceof Error ? reason.message : "Git observation failed."); } finally { setBusy(false); } };
  const agentRun = agent ? latestRunForAgent(data.runs, agent.id) : null;
  return <div className="drawer-scrim" onMouseDown={(event) => { if (event.target === event.currentTarget) close(); }}><aside className="inspector" aria-label="Canonical inspector"><header><div><div className="eyebrow">Exact inspector</div><h2>{run ? data.agents.find((candidate) => candidate.id === run.agent_id)?.name ?? "Agent run" : agent?.name ?? task?.task.title}</h2></div><IconButton label="Close inspector" onClick={close}><X size={18} /></IconButton></header>
    {currentRun ? <><div className="inspector-status"><StatusPill value={currentRun.status} /><span>{currentRun.provider} through {currentRun.runtime}</span></div><section><h3>Assigned work</h3><p>{data.tasks.find((item) => item.task.id === currentRun.task_id)?.task.title ?? "Task unavailable in this bounded page"}</p></section>{["start_failed", "failed", "lost"].includes(currentRun.status) && <section className="launch-failure" role="alert"><div className="section-title"><h3>{currentRun.status === "start_failed" ? "Launch failed" : currentRun.status === "lost" ? "Runtime outcome is unknown" : "Run failed"}</h3><AlertCircle size={16} /></div><p>{currentRun.failure_message ?? currentRun.failure_code ?? "Inspect the bounded runtime output for the exact provider diagnosis."}</p>{currentRun.status === "start_failed" && <button className="primary-button compact" disabled={!mutable || busy} onClick={() => void retry()}>{busy ? <LoaderCircle className="spin" size={14} /> : <RotateCcw size={14} />}Retry after preflight</button>}</section>}<section><div className="section-title"><h3>Agent activity</h3><Activity size={15} /></div><RuntimeOutput logs={logs} /></section>{terminalOpen && <LiveTerminal apiBase={apiBase} csrf={csrf} workspace={data.workspace?.id ?? ""} run={currentRun} close={() => setTerminalOpen(false)} mutable={mutable} />}{["active", "blocked"].includes(currentRun.status) && <section className="runtime-control"><label htmlFor="runtime-prompt">Send a visible runtime prompt</label><div><input id="runtime-prompt" value={prompt} maxLength={4096} onChange={(event) => setPrompt(event.target.value)} placeholder="Clarify the next observable step…" /><button className="secondary-button" disabled={busy || !prompt.trim()} onClick={() => void sendPrompt()}><Send size={14} />Send</button></div><div className="runtime-buttons">{currentRun.can_attach && !terminalOpen && <button className="secondary-button" disabled={busy} onClick={() => setTerminalOpen(true)}><TerminalSquare size={14} />Open raw terminal</button>}{currentRun.status === "blocked" && <button className="secondary-button" disabled={busy} onClick={() => void resume()}><RotateCcw size={14} />Resume</button>}<button className="secondary-button" disabled={busy} onClick={() => void interrupt()}><AlertCircle size={14} />Interrupt</button><button className="danger-button" onClick={() => void stop()} disabled={busy}>{busy ? <LoaderCircle className="spin" size={15} /> : <Square size={15} />}Stop · 5000 ms grace</button></div></section>}{notice && <div className="notice" role="status">{notice}</div>}<footer><span>Revision {currentRun.revision}</span><span>{currentRun.can_attach ? "Interactive runtime available" : currentRun.status === "start_failed" ? "Retry available after diagnosis" : "Bounded logs only"}</span></footer></> : agent ? <><div className="inspector-status"><StatusPill value={agentRun?.status ?? (agent.enabled ? "ready" : "disabled")} /><span>{agent.provider} through {agent.runtime}</span></div><section><h3>Authority-neutral role</h3><p>{agent.role}. Scheduling authority comes from policy, assignment, and receipts—not this label.</p></section><section><div className="section-title"><h3>Repository observation</h3><IconButton label="Refresh Git observation" onClick={() => void refreshGit()} disabled={busy}><RefreshCw className={busy ? "spin" : ""} size={14} /></IconButton></div>{git ? <><dl className="fact-list"><div><dt>Availability</dt><dd>{git.availability}</dd></div><div><dt>Branch</dt><dd>{git.branch || "detached"}</dd></div><div><dt>Working tree</dt><dd>{git.dirty ? `${git.dirty_paths?.length ?? 0}${git.omitted_paths ? `+${git.omitted_paths}` : ""} changed paths` : "clean"}</dd></div><div><dt>Write mode</dt><dd>{git.write_mode}</dd></div></dl>{git.dirty_paths && git.dirty_paths.length > 0 && <div className="changed-paths">{git.dirty_paths.slice(0, 16).map((path) => <code key={path}>{path}</code>)}{git.dirty_paths.length > 16 && <small>+{git.dirty_paths.length - 16 + (git.omitted_paths ?? 0)} paths omitted from this view</small>}</div>}</> : <p>No checkout is loaded in this bounded scope.</p>}</section>{notice && <div className="notice" role="status">{notice}</div>}{agentRun ? <button className="secondary-button" onClick={() => inspectRun(agentRun)}><TerminalSquare size={15} />Open run details</button> : <div className="quiet-line"><Clock3 size={14} />No current run</div>}<footer><span>Definition revision {agent.revision}</span><span>{agent.enabled ? "Enabled" : "Disabled"}</span></footer></> : task && <><div className="inspector-status"><StatusPill value={task.task.status} /><span>Priority {task.task.priority}</span></div><section><h3>Description</h3><p>{task.task.description || "No additional description."}</p></section><section><h3>Readiness</h3><p>{task.readiness.ready ? "Ready to run." : task.readiness.reason || "Not ready."}</p></section><section><h3>Assignment</h3><p>{data.agents.find((candidate) => candidate.id === task.task.assigned_agent_id)?.name ?? "Unassigned"}</p></section><footer><span>Revision {task.task.revision}</span><span>Updated {displayTime(task.task.updated_at)}</span></footer></>}
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
      <div className={`service-state ${fresh ? "connected" : "connecting"}`}><i /><span>{fresh ? "Current · local only" : "Refreshing exact state…"}</span></div>
    </header>
    <aside className={`sidebar ${mobileNav ? "open" : ""}`}>
      <div className="workspace-card"><div className="workspace-glyph"><Boxes size={18} /></div><div><strong>{data.workspace?.name ?? "Your workbench"}</strong><span>{data.project?.name ?? "Exact local state"}</span></div>{data.workspaces.length > 1 && <ChevronDown size={15} />}</div>
      {data.workspaces.length > 1 && <select className="scope-select" value={data.workspace?.id ?? ""} onChange={(event) => void selectWorkspace(event.target.value)} aria-label="Workspace">{data.workspaces.map((workspace) => <option value={workspace.id} key={workspace.id}>{workspace.name}</option>)}</select>}
      {data.projects.length > 1 && <select className="scope-select" value={data.project?.id ?? ""} onChange={(event) => void selectProject(event.target.value)} aria-label="Project">{data.projects.map((project) => <option value={project.id} key={project.id}>{project.name}</option>)}</select>}
      <nav aria-label="Primary navigation">{navItems.map(({ id, label, icon: Icon }) => <button key={id} className={view === id ? "active" : ""} onClick={() => { setView(id); setMobileNav(false); }}><Icon size={17} strokeWidth={1.8} aria-hidden="true" /><span>{label}</span>{id === "decisions" && data.approvals.some((item) => item.status === "pending") && <b>{data.approvals.filter((item) => item.status === "pending").length}</b>}</button>)}</nav>
      <div className="sidebar-spacer" />
      <div className="health-card"><HeartPulse size={16} /><span><strong>Canonical state</strong><small>Journal #{data.highWater}</small></span><Check size={14} /></div>
      <button className="settings-button" onClick={() => setView("health")}><Settings size={16} />Service settings</button>
    </aside>
    {loading ? <main className="loading-main"><LoaderCircle className="spin" size={26} /><p>Loading exact local state…</p></main> : data.workspaces.length === 0 || data.projects.length === 0 || data.agents.length === 0 ? <Onboarding apiBase={apiBase} csrf={csrf} onComplete={reload} /> : <main className="content-main">
      {error && <div className="global-error"><AlertCircle size={17} /><span>{error}</span><button onClick={() => void reload()}><RefreshCw size={15} />Retry</button></div>}
      {view === "workbench" && <WorkbenchView data={data} apiBase={apiBase} csrf={csrf} reload={reload} selectTask={inspectTask} selectRun={inspectRun} mutable={fresh} />}
      {view === "graph" && <GraphView data={data} selectTask={inspectTask} />}
      {view === "crew" && <CrewView data={data} selectAgent={inspectAgent} />}
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
