import { StrictMode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import {
  Activity, AlertCircle, BookOpenText, Bot, Boxes, Check, CheckCircle2, ChevronDown, ChevronRight,
  CircleDot, ClipboardCheck, Clock3, Code2, Command, Database, FileCheck2, GitBranch,
  GitCommitHorizontal, HeartPulse, Inbox, LayoutDashboard, ListChecks, LoaderCircle,
  Menu, MessageCircle, MessageSquareText, Network, Play, Plus, RefreshCw, RotateCcw,
  Search, Send, Settings, ShieldCheck, Sparkles, Square, Stethoscope, TerminalSquare,
  Users, Workflow, X, XCircle,
} from "lucide-react";
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
type TaskDetail = { task: Task; dependencies: Array<{ dependency_task_id: string }>; assignment?: { agent_id: string }; readiness: { ready: boolean; reasons: string[] } };
type Run = { id: string; project_id: string; task_id: string; agent_id: string; runtime: string; provider: string; status: string; can_attach: boolean; revision: number; updated_at: string; result_summary?: string; blocked_question?: string; failure_code?: string };
type EventRecord = { event_id: string; sequence: number; type: string; recorded_at: string; actor: { actor_type: string }; entity: { type: string; id: string; revision: number } };
type Approval = { id: string; project_id?: string; action_id: string; status: string; revision: number; expires_at?: string; created_at: string };
type InboxItem = { message: { id: string; sender_type: string; sender_agent_name?: string; kind: string; body: string; task_id?: string; created_at: string }; delivery: { recipient_agent_id: string; recipient_name: string; status: string; wake_status: string } };
type CheckRunItem = { run: { id: string; task_id: string; status: string; revision: number; created_at: string; updated_at: string }; outcome?: string; requirement_state: string; current_freshness?: { status?: string } };
type FullDoctor = { status: string; event_sequence: number; resources: { database_bytes: number; referenced_artifact_bytes: number; filesystem_free_bytes: number }; checks: Array<{ code: string; status: string; issue_count: number; summary: string }> };
type Briefing = { id: string; revision: number; event_cursor: number; caught_up: boolean; evaluated_at: string; claims: Array<{ id: string; kind: string; urgency: string; summary: string; status: string }>; omitted: Array<{ section: string; reason: string; count: number }>; byte_size: number };
type OwnerOperation = { id: string; ordinal: number; type: string; payload: Record<string, unknown>; policy_result: string; status: string; result_entity_type?: string; event_sequence?: number };
type OwnerTurnDetail = { conversation: { id: string; title: string }; turn: { id: string; kind: "query" | "plan" | "act"; instruction: string; status: string; answer?: string; as_of_event_sequence: number; completed_event_sequence?: number; revision: number }; operations: OwnerOperation[]; receipts: Array<{ operation_id: string; method: string; event_sequence?: number }> };

type WorkbenchData = {
  workspaces: Workspace[];
  workspace: Workspace | null;
  projects: Project[];
  project: Project | null;
  checkouts: Checkout[];
  agents: Agent[];
  objectives: Objective[];
  tasks: TaskDetail[];
  runs: Run[];
  approvals: Approval[];
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
const emptyData: WorkbenchData = { workspaces: [], workspace: null, projects: [], project: null, checkouts: [], agents: [], objectives: [], tasks: [], runs: [], approvals: [], checks: [], events: [], highWater: 0 };

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
function statusTone(status: string) {
  if (["completed", "granted", "consumed", "available", "ready"].includes(status)) return "good";
  if (["failed", "start_failed", "lost", "denied"].includes(status)) return "bad";
  if (["active", "starting", "requested", "stopping", "pending"].includes(status)) return "live";
  if (["blocked", "review", "changes_requested"].includes(status)) return "warn";
  return "quiet";
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

async function loadOwnerConversation(apiBase: string, workspace: string, project: string): Promise<OwnerTurnDetail[]> {
  const response = await fetch(`${apiBase}/conversation?workspace=${encodeURIComponent(workspace)}&project=${encodeURIComponent(project)}`, { credentials: "same-origin" });
  if (!response.ok) throw new Error(`conversation read failed (${response.status})`);
  const value = (await response.json()) as { turns?: OwnerTurnDetail[] };
  return value.turns ?? [];
}

function safeTerminalText(value: string): string {
  return [...value].map((char) => {
    const code = char.codePointAt(0) ?? 0;
    if (char === "\n" || char === "\t") return char;
    if (code < 0x20 || code >= 0x7f && code <= 0x9f || code === 0x2028 || code === 0x2029 || code >= 0x202a && code <= 0x202e || code >= 0x2066 && code <= 0x2069) return "�";
    return char;
  }).join("");
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
  const [projectPage, agentPage, objectivePage, taskPage, runPage, approvalPage, eventPage] = await Promise.all([
    rpc<{ projects: Project[] } & Page>(apiBase, csrf, "project.list", { workspace: workspace.id, limit: 200 }),
    rpc<{ agents: Agent[] } & Page>(apiBase, csrf, "agent.list", { workspace: workspace.id, limit: 200 }),
    rpc<{ objectives: Objective[] } & Page>(apiBase, csrf, "objective.list", { workspace: workspace.id, limit: 200 }),
    rpc<{ tasks: TaskDetail[] } & Page>(apiBase, csrf, "task.list", { workspace: workspace.id, limit: 200 }),
    rpc<{ runs: Run[] } & Page>(apiBase, csrf, "run.list", { workspace: workspace.id, limit: 200 }),
    rpc<{ approvals: Approval[] } & Page>(apiBase, csrf, "approval.list", { workspace: workspace.id, limit: 200 }),
    rpc<{ events: EventRecord[]; high_water: number } & Page>(apiBase, csrf, "events.list", { workspace: workspace.id, after: eventAfter, limit: 200 }),
  ]);
  const project = projectPage.projects.find((item) => item.id === preferredProject) ?? projectPage.projects[0] ?? null;
  const [checkouts, checks] = project ? await Promise.all([
    rpc<{ checkouts: Checkout[] }>(apiBase, csrf, "checkout.list", { workspace: workspace.id, project: project.id }).then((value) => value.checkouts),
    rpc<{ runs: CheckRunItem[] } & Page>(apiBase, csrf, "check.list", { workspace: workspace.id, project: project.id, limit: 200 }).then((value) => value.runs),
  ]) : [[], []];
  const after = await rpc<{ events: EventRecord[]; high_water: number } & Page>(apiBase, csrf, "events.list", { workspace: workspace.id, after: before.high_water, limit: 1 });
  if (after.high_water !== before.high_water) {
    if (attempt >= 2) throw new Error("Canonical state kept changing during refresh; retry when the current event cut settles.");
    return loadWorkbench(apiBase, csrf, workspace.id, project?.id ?? preferredProject, attempt + 1);
  }
  return {
    workspaces: workspacePage.workspaces, workspace, projects: projectPage.projects, project, checkouts,
    agents: agentPage.agents, objectives: objectivePage.objectives, tasks: taskPage.tasks,
    runs: runPage.runs, approvals: approvalPage.approvals, checks, events: eventPage.events,
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
  const [runtime, setRuntime] = useState("direct");
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
      <div className="field-grid">
        <label><span>Provider</span><select value={provider} onChange={(event) => setProvider(event.target.value)}><option value="codex">Codex subscription</option><option value="claude">Claude subscription</option><option value="fixture-mcp">Local fixture</option></select></label>
        <label><span>Runtime</span><select value={runtime} onChange={(event) => setRuntime(event.target.value)}><option value="direct">Direct headless</option><option value="herdr">Herdr interactive (optional)</option></select></label>
      </div>
      {error && <div className="form-error" role="alert"><AlertCircle size={17} />{error}</div>}
      <button className="primary-button" disabled={busy}>{busy ? <LoaderCircle className="spin" size={17} /> : <Command size={17} />} {busy ? "Inspecting and recording…" : "Create workbench"}</button>
      <p className="form-note">One submission records the workspace, project checkout, and agent with replay-safe idempotency.</p>
    </form>
  </main>;
}

function PlanEditor({ detail, agents, disabled, save }: { detail: OwnerTurnDetail; agents: Agent[]; disabled: boolean; save: (body: Record<string, unknown>) => Promise<void> }) {
  const task = detail.operations.find((operation) => operation.type === "create_task")?.payload ?? {};
  const assignment = detail.operations.find((operation) => operation.type === "assign_task")?.payload ?? {};
  const budget = task.budget && typeof task.budget === "object" ? task.budget as Record<string, unknown> : {};
  const [open, setOpen] = useState(false);
  const [title, setTitle] = useState(String(task.title ?? detail.turn.instruction));
  const [description, setDescription] = useState(String(task.description ?? detail.turn.instruction));
  const [priority, setPriority] = useState(Number(task.priority ?? 500));
  const [agent, setAgent] = useState(String(assignment.agent_id ?? agents.find((candidate) => candidate.enabled)?.id ?? ""));
  const [tokens, setTokens] = useState(Number(budget.token_limit ?? 0));
  const [cost, setCost] = useState(Number(budget.cost_cents ?? 0));
  const [seconds, setSeconds] = useState(Number(budget.time_seconds ?? 0));
  const [busy, setBusy] = useState(false);
  const submit = async (event: React.FormEvent) => {
    event.preventDefault(); setBusy(true);
    try {
      await save({ turn_id: detail.turn.id, expected_revision: detail.turn.revision, title: title.trim(), description: description.trim(), priority, agent, budget: { token_limit: tokens, cost_cents: cost, time_seconds: seconds } });
      setOpen(false);
    } catch {
      // The parent renders the exact server diagnosis beside the plan.
    } finally { setBusy(false); }
  };
  if (!open) return <button className="edit-plan" disabled={disabled} onClick={() => setOpen(true)}><Settings size={13} />Edit task, agent, and budget</button>;
  return <form className="plan-editor" onSubmit={submit}><label><span>Task and objective title</span><input required maxLength={256} value={title} onChange={(event) => setTitle(event.target.value)} /></label><label><span>Description</span><textarea required maxLength={4096} value={description} onChange={(event) => setDescription(event.target.value)} /></label><div className="plan-editor-grid"><label><span>Agent</span><select value={agent} onChange={(event) => setAgent(event.target.value)}>{agents.filter((candidate) => candidate.enabled).map((candidate) => <option key={candidate.id} value={candidate.id}>{candidate.name} · {candidate.provider}/{candidate.runtime}</option>)}</select></label><label><span>Priority (0–1000)</span><input type="number" min="0" max="1000" value={priority} onChange={(event) => setPriority(Number(event.target.value))} /></label></div><div className="plan-editor-grid budget-grid"><label><span>Token limit</span><input type="number" min="0" max="1000000" value={tokens} onChange={(event) => setTokens(Number(event.target.value))} /></label><label><span>Paid cost cents (not granted)</span><input type="number" min="0" max="0" value={cost} disabled onChange={(event) => setCost(Number(event.target.value))} /></label><label><span>Time seconds</span><input type="number" min="0" max="86400" value={seconds} onChange={(event) => setSeconds(Number(event.target.value))} /></label></div><div className="plan-editor-actions"><button type="button" className="secondary-button" onClick={() => setOpen(false)}>Cancel</button><button className="primary-button compact" disabled={busy || disabled || !agent}>{busy ? <LoaderCircle className="spin" size={13} /> : <Check size={13} />}Seal reviewed revision</button></div></form>;
}

function WorkbenchView({ data, apiBase, csrf, reload, selectTask, selectRun, mutable }: { data: WorkbenchData; apiBase: string; csrf: string; reload: () => Promise<void>; selectTask: (task: TaskDetail) => void; selectRun: (run: Run) => void; mutable: boolean }) {
  const [instruction, setInstruction] = useState("");
  const [mode, setMode] = useState<"query" | "plan" | "act">("act");
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const [turns, setTurns] = useState<OwnerTurnDetail[]>([]);
  const recovering = useRef(false);
  const pendingKey = data.workspace && data.project ? `crewfold_pending_intent_${data.workspace.id}_${data.project.id}` : "";
  useEffect(() => { if (!data.workspace || !data.project) return; void loadOwnerConversation(apiBase, data.workspace.id, data.project.id).then(setTurns).catch(() => setTurns([])); }, [apiBase, data.project?.id, data.workspace?.id]);
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
      setInstruction(""); setNotice("Committed an objective and assigned first task. Review it below, then start the agent when ready.");
      setTurns((current) => [...current, detail]);
      if (mode === "query") setNotice("Answered from the frozen canonical event cut without creating a domain event.");
      if (mode === "plan") setNotice("Frozen a typed four-step plan. No effect has executed.");
      if (mode === "act") setNotice(detail.turn.status === "awaiting_approval" ? "Paused before every effect because the instruction crosses an owner-review boundary." : "Committed four receipted effects and started the selected agent.");
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
      setNotice("Committed the frozen plan exactly and started the selected agent.");
      await reload();
    } catch (reason) { setNotice(reason instanceof Error ? reason.message : "The frozen plan could not execute."); }
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
  const openTasks = data.tasks.filter(({ task }) => !["completed", "failed", "cancelled"].includes(task.status));
  return <div className="view-grid workbench-view">
    <section className="conversation-panel panel">
      <div className="panel-heading"><div><div className="eyebrow"><Sparkles size={13} />Owner instruction</div><h1>What should the crew accomplish?</h1></div><StatusPill value="local" /></div>
      <div className="conversation-intro">
        <div className="assistant-avatar"><Bot size={22} /></div>
        <div><strong>Crewfold</strong><p>I’ll answer from canonical state, freeze an editable plan, or execute a clearly authorized local goal. Destructive, publication, external, credential, budget, and authority changes stop before their first effect.</p></div>
      </div>
      {turns.length > 0 && <div className="conversation-history">{turns.slice(-4).map((detail) => <div className="turn" key={detail.turn.id}><div className="owner-message"><strong>You</strong><p>{detail.turn.instruction}</p><span>{detail.turn.kind}</span></div><div className="crew-message"><span className="assistant-avatar"><Bot size={16} /></span><div><strong>Crewfold</strong><p>{detail.turn.answer ?? (detail.turn.status === "planned" ? `Frozen ${detail.operations.length} operations; no effects executed.` : `${detail.receipts.length} exact effects committed.`)}</p><div className="receipt-line"><ShieldCheck size={13} />{detail.turn.status} · event cut #{detail.turn.completed_event_sequence ?? detail.turn.as_of_event_sequence}</div>{detail.operations.length > 0 && <ol className="operation-list" aria-label="Frozen typed operations">{detail.operations.map((operation) => <li key={operation.id}><span>{operation.ordinal}</span><strong>{operation.type.replaceAll("_", " ")}</strong><StatusPill value={operation.status} /></li>)}</ol>}{detail.turn.status === "planned" && <PlanEditor detail={detail} agents={data.agents} disabled={!mutable || busy} save={(draft) => savePlan(detail, draft)} />}{detail.turn.status === "planned" && <button className="execute-plan" disabled={!mutable || busy} onClick={() => void executePlan(detail.turn.id)}><Play size={13} />Execute reviewed plan</button>}</div></div></div>)}</div>}
      <form className="composer" onSubmit={createWork}>
        <textarea value={instruction} onChange={(event) => setInstruction(event.target.value)} maxLength={256} placeholder="Build the first playable world loop and organize the work…" aria-label="Owner instruction" />
        <div className="composer-footer"><div className="intent-mode" aria-label="Instruction mode">{(["query", "plan", "act"] as const).map((value) => <button type="button" key={value} className={mode === value ? "selected" : ""} onClick={() => setMode(value)}>{value}</button>)}</div><span>{instruction.length}/256</span><button className="submit-intent" disabled={!mutable || busy || !instruction.trim() || !data.project}>{busy ? <LoaderCircle className="spin" size={17} /> : <Send size={17} />}{mode === "act" ? "Do it" : mode === "plan" ? "Plan" : "Ask"}</button></div>
      </form>
      {notice && <div className="notice" role="status"><CheckCircle2 size={17} />{notice}</div>}
      <div className="effect-note"><ShieldCheck size={16} /><span><strong>No hidden effects.</strong> Query is read-only, Plan freezes operations, and Act returns exact receipts only after permitted effects commit.</span></div>
    </section>
    <aside className="right-stack">
      <section className="panel metric-panel"><div className="panel-label"><Workflow size={16} />Current work</div><div className="metric-row"><div><strong>{openTasks.length}</strong><span>open tasks</span></div><div><strong>{activeRuns.length}</strong><span>live runs</span></div><div><strong>{data.approvals.filter((item) => item.status === "pending").length}</strong><span>decisions</span></div></div></section>
      <section className="panel compact-list"><div className="panel-title"><h2>Agents in motion</h2><button onClick={() => void reload()} aria-label="Refresh workbench"><RefreshCw size={15} /></button></div>{activeRuns.length === 0 ? <EmptyState icon={Bot} title="No agent is running" detail="Assigned work is ready for you to inspect and start." /> : activeRuns.slice(0, 5).map((run) => <button className="list-row" key={run.id} onClick={() => selectRun(run)}><span className="row-icon"><Bot size={16} /></span><span><strong>{data.agents.find((agent) => agent.id === run.agent_id)?.name ?? "Agent"}</strong><small>{data.tasks.find((task) => task.task.id === run.task_id)?.task.title ?? "Assigned work"}</small></span><StatusPill value={run.status} /></button>)}</section>
    </aside>
    <section className="panel task-strip full-span"><div className="panel-title"><div><h2>Next work</h2><p>Canonical task state, ordered by Crewfold.</p></div><span>{data.tasks.length} total</span></div>{openTasks.length === 0 ? <EmptyState icon={ListChecks} title="No work recorded yet" detail="Describe the outcome above to create the first objective and task." /> : <div className="task-cards">{openTasks.slice(0, 6).map((detail) => <button key={detail.task.id} className="task-card" onClick={() => selectTask(detail)}><div><StatusPill value={detail.task.status} /><span className="priority">P{detail.task.priority}</span></div><strong>{detail.task.title}</strong><small>{data.agents.find((agent) => agent.id === detail.task.assigned_agent_id)?.name ?? "Unassigned"} · updated {displayTime(detail.task.updated_at)}</small><ChevronRight size={16} /></button>)}</div>}</section>
  </div>;
}

function GraphView({ data, selectTask }: { data: WorkbenchData; selectTask: (task: TaskDetail) => void }) {
  const columns = ["ready", "assigned", "active", "blocked", "review", "completed"];
  return <section className="panel page-panel"><div className="page-heading"><div><div className="eyebrow"><Network size={13} />Canonical graph</div><h1>Work graph</h1><p>Objectives, dependencies, assignments, and current task state.</p></div><span>{data.tasks.length} tasks</span></div>
    {data.tasks.length === 0 ? <EmptyState icon={Workflow} title="The graph is empty" detail="Create work from the Workbench to populate it." /> : <div className="kanban">{columns.map((column) => <div className="kanban-column" key={column}><header><StatusPill value={column} /><span>{data.tasks.filter(({ task }) => task.status === column).length}</span></header>{data.tasks.filter(({ task }) => task.status === column).map((detail) => <button key={detail.task.id} onClick={() => selectTask(detail)}><strong>{detail.task.title}</strong><small>{detail.dependencies.length ? `${detail.dependencies.length} dependencies` : detail.readiness.ready ? "Ready to proceed" : detail.readiness.reasons[0]}</small></button>)}</div>)}</div>}
  </section>;
}

function CrewView({ data, selectAgent }: { data: WorkbenchData; selectAgent: (agent: Agent) => void }) {
  return <section className="panel page-panel"><div className="page-heading"><div><div className="eyebrow"><Users size={13} />Runtime roster</div><h1>Crew</h1><p>Definitions and their latest canonical run—not terminal folklore.</p></div><span>{data.agents.length} agents</span></div>
    {data.agents.length === 0 ? <EmptyState icon={Bot} title="No agents configured" detail="Add the first provider/runtime definition during onboarding." /> : <div className="crew-grid">{data.agents.map((agent) => { const run = data.runs.find((candidate) => candidate.agent_id === agent.id); return <article className="crew-card" key={agent.id}><div className="crew-card-top"><span className="agent-avatar"><Bot size={20} /></span><StatusPill value={run?.status ?? (agent.enabled ? "ready" : "disabled")} /></div><h2>{agent.name}</h2><p>{agent.role}</p><dl className="fact-list"><div><dt>Provider</dt><dd>{agent.provider}</dd></div><div><dt>Runtime</dt><dd>{agent.runtime}</dd></div><div><dt>Concurrency</dt><dd>policy bound</dd></div></dl><button className="secondary-button" onClick={() => selectAgent(agent)}><Search size={15} />Inspect agent{run ? " and run" : ""}</button></article>; })}</div>}
  </section>;
}

function DecisionsView({ data, apiBase, csrf, reload, mutable }: { data: WorkbenchData; apiBase: string; csrf: string; reload: () => Promise<void>; mutable: boolean }) {
  const [busy, setBusy] = useState("");
  const [error, setError] = useState("");
  const decide = async (approval: Approval, decision: "allow" | "deny") => {
    if (!mutable || !data.workspace) return;
    setBusy(approval.id); setError("");
    try {
      await rpc(apiBase, csrf, `approval.${decision}`, { workspace: data.workspace.id, approval: approval.id, expected_revision: approval.revision, decision_note: decision === "allow" ? "Allowed in local workbench" : "Denied in local workbench", idempotency_key: newKey(`approval-${decision}`) });
      await reload();
    } catch (reason) { setError(reason instanceof Error ? reason.message : "Decision could not be committed."); }
    finally { setBusy(""); }
  };
  return <section className="panel page-panel"><div className="page-heading"><div><div className="eyebrow"><ClipboardCheck size={13} />Policy gates</div><h1>Decisions</h1><p>Actions Crewfold paused for explicit owner authority.</p></div><span>{data.approvals.filter((item) => item.status === "pending").length} pending</span></div>{error && <div className="form-error" role="alert"><AlertCircle size={16} />{error}</div>}{data.approvals.length === 0 ? <EmptyState icon={ShieldCheck} title="Nothing needs approval" detail="Gated operations will appear here with their exact requested effect." /> : <div className="table-list">{data.approvals.map((approval) => <div className="table-row decision-row" key={approval.id}><span className="row-icon"><ShieldCheck size={17} /></span><span><strong>Supervisor action</strong><small>Requested {displayTime(approval.created_at)} · revision {approval.revision}</small></span><StatusPill value={approval.status} />{approval.status === "pending" ? <span className="row-actions"><button className="secondary-button" disabled={!mutable || busy === approval.id} onClick={() => void decide(approval, "deny")}><XCircle size={14} />Deny</button><button className="primary-button compact" disabled={!mutable || busy === approval.id} onClick={() => void decide(approval, "allow")}>{busy === approval.id ? <LoaderCircle className="spin" size={14} /> : <Check size={14} />}Allow</button></span> : <ChevronRight size={16} />}</div>)}</div>}</section>;
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

function LiveTerminal({ apiBase, csrf, workspace, run, close }: { apiBase: string; csrf: string; workspace: string; run: Run; close: () => void }) {
  const [state, setState] = useState("connecting");
  const [output, setOutput] = useState("");
  const [input, setInput] = useState("");
  const socket = useRef<WebSocket | null>(null);
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
        next.onopen = () => { if (!active) return; setState("connected"); next.send(JSON.stringify({ type: "resize", cols: 100, rows: 32 })); };
        next.onmessage = (event) => {
          if (!active) return;
          const raw = event.data instanceof ArrayBuffer ? new TextDecoder().decode(event.data) : String(event.data);
          const safe = safeTerminalText(raw);
          setOutput((current) => {
            const combined = current + safe;
            return combined.length > 65536 ? "[earlier terminal display omitted]\n" + combined.slice(-65536) : combined;
          });
        };
        next.onerror = () => { if (active) setState("unavailable"); };
        next.onclose = () => { if (active) setState("closed"); };
      } catch (reason) {
        setState(reason instanceof Error ? reason.message : "unavailable");
      }
    };
    void connect();
    return () => { active = false; socket.current?.close(1000, "inspector closed"); socket.current = null; };
  }, [apiBase, csrf, run.id, workspace]);
  const send = (value: string) => { if (socket.current?.readyState !== WebSocket.OPEN || !value) return; socket.current.send(JSON.stringify({ type: "input", data: value })); };
  return <section className="live-terminal" aria-label="Live Herdr terminal"><div className="section-title"><h3>Live Herdr terminal</h3><span><CircleDot size={11} />{state}</span></div><p>Operational bytes are untrusted and display-sanitized. Canonical state remains in the inspector.</p><pre aria-live="polite">{output || "Waiting for terminal output…"}</pre><form onSubmit={(event) => { event.preventDefault(); send(input + "\n"); setInput(""); }}><input aria-label="Terminal input" value={input} maxLength={4095} onChange={(event) => setInput(event.target.value)} placeholder="Send terminal input…" /><button className="secondary-button" disabled={state !== "connected" || !input}><Send size={14} />Send</button><button type="button" className="secondary-button" disabled={state !== "connected"} onClick={() => send("\u0003")}><AlertCircle size={14} />Ctrl-C</button><button type="button" className="secondary-button" onClick={close}><X size={14} />Close</button></form></section>;
}

function Inspector({ data, task, run, agent, apiBase, csrf, close, reload, inspectRun, mutable }: { data: WorkbenchData; task: TaskDetail | null; run: Run | null; agent: Agent | null; apiBase: string; csrf: string; close: () => void; reload: () => Promise<void>; inspectRun: (run: Run) => void; mutable: boolean }) {
  const [logs, setLogs] = useState("");
  const [git, setGit] = useState<Checkout | null>(null);
  const [prompt, setPrompt] = useState("");
  const [notice, setNotice] = useState("");
  const [terminalOpen, setTerminalOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  useEffect(() => { setLogs(""); if (!run) return; void rpc<{ logs: { stdout: { text: string; truncated: boolean; omitted_bytes: number }; stderr: { text: string; truncated: boolean; omitted_bytes: number }; state: string } }>(apiBase, csrf, "run.logs", { workspace: data.workspace?.id, run: run.id, tail: 160 }).then((result) => setLogs([result.logs.stdout.text, result.logs.stderr.text, result.logs.stdout.truncated || result.logs.stderr.truncated ? "[bounded log output; earlier bytes omitted]" : ""].filter(Boolean).join("\n"))).catch((error) => setLogs(error instanceof Error ? error.message : "Logs unavailable")); }, [apiBase, csrf, data.workspace?.id, run?.id, data.highWater]);
  useEffect(() => { setGit(data.checkouts[0] ?? null); }, [data.checkouts]);
  useEffect(() => { if (!mutable) setTerminalOpen(false); }, [mutable]);
  if (!task && !run && !agent) return null;
  const currentRun = run ? data.runs.find((candidate) => candidate.id === run.id) ?? run : null;
  const stop = async () => { if (!mutable || !currentRun || !data.workspace) return; setBusy(true); setNotice(""); try { await rpc(apiBase, csrf, "run.stop", { workspace: data.workspace.id, run: currentRun.id, expected_revision: currentRun.revision, grace_period_millis: 5000, idempotency_key: newKey("stop") }); setNotice("Graceful stop requested with the displayed 5000 ms grace."); await reload(); } catch (reason) { setNotice(reason instanceof Error ? reason.message : "Stop failed."); } finally { setBusy(false); } };
  const resume = async () => { if (!mutable || !currentRun || !data.workspace) return; setBusy(true); setNotice(""); try { await rpc(apiBase, csrf, "run.resume", { workspace: data.workspace.id, run: currentRun.id, expected_revision: currentRun.revision, idempotency_key: newKey("resume") }); setNotice("Run resumed from its exact current revision."); await reload(); } catch (reason) { setNotice(reason instanceof Error ? reason.message : "Resume failed."); } finally { setBusy(false); } };
  const interrupt = async () => { if (!mutable || !currentRun || !data.workspace) return; setBusy(true); setNotice(""); try { await rpc(apiBase, csrf, "run.interrupt", { workspace: data.workspace.id, run: currentRun.id }); setNotice("Interrupt delivered to the current-node runtime binding."); } catch (reason) { setNotice(reason instanceof Error ? reason.message : "Interrupt failed."); } finally { setBusy(false); } };
  const sendPrompt = async () => { if (!mutable || !currentRun || !data.workspace || !prompt.trim()) return; setBusy(true); setNotice(""); try { await rpc(apiBase, csrf, "run.prompt", { workspace: data.workspace.id, run: currentRun.id, text: prompt.trim() }); setPrompt(""); setNotice("Prompt delivered to the current live runtime."); } catch (reason) { setNotice(reason instanceof Error ? reason.message : "Prompt failed."); } finally { setBusy(false); } };
  const refreshGit = async () => { if (!data.workspace || !data.project) return; setBusy(true); setNotice(""); try { const response = await fetch(`${apiBase}/git?workspace=${encodeURIComponent(data.workspace.id)}&project=${encodeURIComponent(data.project.id)}`, { credentials: "same-origin" }); const result = (await response.json()) as { observations?: Array<Omit<Checkout, "id" | "project_id" | "path" | "write_mode"> & { checkout_id: string }>; error?: { message: string } }; if (!response.ok || !result.observations) throw new Error(result.error?.message ?? "Git observation failed"); const observation = result.observations[0]; const canonical = data.checkouts.find((checkout) => checkout.id === observation?.checkout_id); setGit(observation && canonical ? { ...canonical, ...observation, id: observation.checkout_id } : null); setNotice("Repository status refreshed without persisting source or diff content."); } catch (reason) { setNotice(reason instanceof Error ? reason.message : "Git observation failed."); } finally { setBusy(false); } };
  const agentRun = agent ? data.runs.find((candidate) => candidate.agent_id === agent.id) : null;
  return <div className="drawer-scrim" onMouseDown={(event) => { if (event.target === event.currentTarget) close(); }}><aside className="inspector" aria-label="Canonical inspector"><header><div><div className="eyebrow">Exact inspector</div><h2>{run ? data.agents.find((candidate) => candidate.id === run.agent_id)?.name ?? "Agent run" : agent?.name ?? task?.task.title}</h2></div><IconButton label="Close inspector" onClick={close}><X size={18} /></IconButton></header>
    {currentRun ? <><div className="inspector-status"><StatusPill value={currentRun.status} /><span>{currentRun.provider} through {currentRun.runtime}</span></div><section><h3>Assigned work</h3><p>{data.tasks.find((item) => item.task.id === currentRun.task_id)?.task.title ?? "Task unavailable in this bounded page"}</p></section><section><div className="section-title"><h3>Bounded runtime output</h3><TerminalSquare size={15} /></div><pre>{logs || "Loading bounded logs…"}</pre></section>{terminalOpen && <LiveTerminal apiBase={apiBase} csrf={csrf} workspace={data.workspace?.id ?? ""} run={currentRun} close={() => setTerminalOpen(false)} />}{["active", "blocked"].includes(currentRun.status) && <section className="runtime-control"><label htmlFor="runtime-prompt">Send a visible runtime prompt</label><div><input id="runtime-prompt" value={prompt} maxLength={4096} onChange={(event) => setPrompt(event.target.value)} placeholder="Clarify the next observable step…" /><button className="secondary-button" disabled={busy || !prompt.trim()} onClick={() => void sendPrompt()}><Send size={14} />Send</button></div><div className="runtime-buttons">{currentRun.can_attach && !terminalOpen && <button className="secondary-button" disabled={busy} onClick={() => setTerminalOpen(true)}><TerminalSquare size={14} />Open live terminal</button>}{currentRun.status === "blocked" && <button className="secondary-button" disabled={busy} onClick={() => void resume()}><RotateCcw size={14} />Resume</button>}<button className="secondary-button" disabled={busy} onClick={() => void interrupt()}><AlertCircle size={14} />Interrupt</button><button className="danger-button" onClick={() => void stop()} disabled={busy}>{busy ? <LoaderCircle className="spin" size={15} /> : <Square size={15} />}Stop · 5000 ms grace</button></div></section>}{notice && <div className="notice" role="status">{notice}</div>}<footer><span>Revision {currentRun.revision}</span><span>{currentRun.can_attach ? "Interactive runtime available" : "Bounded logs only"}</span></footer></> : agent ? <><div className="inspector-status"><StatusPill value={agentRun?.status ?? (agent.enabled ? "ready" : "disabled")} /><span>{agent.provider} through {agent.runtime}</span></div><section><h3>Authority-neutral role</h3><p>{agent.role}. Scheduling authority comes from policy, assignment, and receipts—not this label.</p></section><section><div className="section-title"><h3>Repository observation</h3><IconButton label="Refresh Git observation" onClick={() => void refreshGit()} disabled={busy}><RefreshCw className={busy ? "spin" : ""} size={14} /></IconButton></div>{git ? <><dl className="fact-list"><div><dt>Availability</dt><dd>{git.availability}</dd></div><div><dt>Branch</dt><dd>{git.branch || "detached"}</dd></div><div><dt>Working tree</dt><dd>{git.dirty ? `${git.dirty_paths?.length ?? 0}${git.omitted_paths ? `+${git.omitted_paths}` : ""} changed paths` : "clean"}</dd></div><div><dt>Write mode</dt><dd>{git.write_mode}</dd></div></dl>{git.dirty_paths && git.dirty_paths.length > 0 && <div className="changed-paths">{git.dirty_paths.slice(0, 16).map((path) => <code key={path}>{path}</code>)}{git.dirty_paths.length > 16 && <small>+{git.dirty_paths.length - 16 + (git.omitted_paths ?? 0)} paths omitted from this view</small>}</div>}</> : <p>No checkout is loaded in this bounded scope.</p>}</section>{notice && <div className="notice" role="status">{notice}</div>}{agentRun ? <button className="secondary-button" onClick={() => inspectRun(agentRun)}><TerminalSquare size={15} />Open run details</button> : <div className="quiet-line"><Clock3 size={14} />No current run</div>}<footer><span>Definition revision {agent.revision}</span><span>{agent.enabled ? "Enabled" : "Disabled"}</span></footer></> : task && <><div className="inspector-status"><StatusPill value={task.task.status} /><span>Priority {task.task.priority}</span></div><section><h3>Description</h3><p>{task.task.description || "No additional description."}</p></section><section><h3>Readiness</h3><p>{task.readiness.ready ? "Ready to run." : task.readiness.reasons.join(" · ") || "Not ready."}</p></section><section><h3>Assignment</h3><p>{data.agents.find((candidate) => candidate.id === task.task.assigned_agent_id)?.name ?? "Unassigned"}</p></section><footer><span>Revision {task.task.revision}</span><span>Updated {displayTime(task.task.updated_at)}</span></footer></>}
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
