import { StrictMode, useEffect, useState } from "react";
import { createRoot } from "react-dom/client";
import "./styles.css";

type ConnectionState = "connecting" | "connected" | "unauthorized" | "failed";

type DaemonStatus = {
  schema: string;
  type: "workbench_status";
  status: "ok";
  protocol: number;
  pid: number;
  started_at: string;
  uptime_ms: number;
  server_version: {
    version: string;
  };
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

async function exchangeBootstrap(token: string): Promise<SessionResponse> {
  const response = await fetch("/api/v1/session", {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ bootstrap: token }),
  });
  if (!response.ok) throw new Error(`bootstrap exchange failed (${response.status})`);
  const result = (await response.json()) as SessionResponse;
  if (result.schema !== expectedSessionSchema || result.type !== "workbench_session" || result.status !== "authenticated") {
    throw new Error("bootstrap exchange returned an unknown contract");
  }
  return result;
}

async function loadStatus(apiBase: string): Promise<DaemonStatus> {
  const response = await fetch(`${apiBase}/status`, { credentials: "same-origin" });
  if (response.status === 401) throw new Error("unauthorized");
  if (!response.ok) throw new Error(`status request failed (${response.status})`);
  const result = (await response.json()) as DaemonStatus;
  if (result.schema !== expectedStatusSchema || result.type !== "workbench_status" || result.status !== "ok") {
    throw new Error("status response returned an unknown contract");
  }
  return result;
}

function bootstrapFromFragment(): string {
  const fragment = new URLSearchParams(window.location.hash.replace(/^#/, ""));
  const token = fragment.get("bootstrap") ?? "";
  if (token) history.replaceState(null, "", `${window.location.pathname}${window.location.search}`);
  return token;
}

function App() {
  const [connection, setConnection] = useState<ConnectionState>("connecting");
  const [status, setStatus] = useState<DaemonStatus | null>(null);
  const [detail, setDetail] = useState("Exchanging the one-time owner grant…");

  useEffect(() => {
    let active = true;
    const connect = async () => {
      try {
        const bootstrap = bootstrapFromFragment();
        let apiBase = sessionStorage.getItem("crewfold_api_base") ?? "";
        if (bootstrap) {
          const session = await exchangeBootstrap(bootstrap);
          apiBase = session.api_base;
          sessionStorage.setItem("crewfold_api_base", apiBase);
        }
        if (!/^\/api\/v1\/session\/[0-9a-f]{64}$/.test(apiBase)) throw new Error("unauthorized");
        const current = await loadStatus(apiBase);
        if (!active) return;
        setStatus(current);
        setConnection("connected");
        setDetail("Authenticated over the owner-local workbench boundary.");
      } catch (error) {
        if (!active) return;
        const message = error instanceof Error ? error.message : "connection failed";
        if (message === "unauthorized") sessionStorage.removeItem("crewfold_api_base");
        setConnection(message === "unauthorized" ? "unauthorized" : "failed");
        setDetail(message === "unauthorized" ? "Run crewfold open to obtain a fresh one-time grant." : message);
      }
    };
    void connect();
    return () => {
      active = false;
    };
  }, []);

  return (
    <div className="app-shell">
      <header className="topbar">
        <div className="brand"><span className="mark">CF</span><span>Crewfold <small>local</small></span></div>
        <div className="crumbs"><span>personal workbench</span><span>/</span><strong>Getting started</strong></div>
        <div className={`service-state ${connection}`}><i /><span>{connection === "connected" ? "Service healthy · local only" : connection}</span></div>
      </header>

      <aside className="sidebar">
        <div className="workspace-card"><div className="workspace-glyph">CF</div><div><strong>Your workbench</strong><span>Exact local state</span></div></div>
        <nav aria-label="Primary navigation">
          <button className="active">⌁ <span>Workbench</span></button>
          <button disabled>◇ <span>Work graph</span></button>
          <button disabled>◉ <span>Crew</span></button>
          <button disabled>↘ <span>Inbox</span></button>
          <button disabled>✓ <span>Decisions</span></button>
          <button disabled>▤ <span>Evidence</span></button>
          <button disabled>⌇ <span>Activity</span></button>
        </nav>
        <div className="sidebar-spacer" />
        <p className="sidebar-note">M21 foundation<br />Onboarding is the next delivery slice.</p>
      </aside>

      <main>
        <section className="hero">
          <div className="eyebrow">M21 · local web workbench</div>
          <h1>One place to direct and understand your crew.</h1>
          <p>This first live slice proves the service, browser boundary, and daemon connection. It deliberately shows no invented project, task, or agent state.</p>
        </section>

        <section className="connection-card" aria-live="polite">
          <div className={`connection-icon ${connection}`}>{connection === "connected" ? "✓" : connection === "connecting" ? "…" : "!"}</div>
          <div>
            <div className="eyebrow">Owner session</div>
            <h2>{connection === "connected" ? "Connected to Crewfold" : connection === "connecting" ? "Connecting securely" : "Workbench access required"}</h2>
            <p>{detail}</p>
          </div>
          {status && <dl>
            <div><dt>Daemon</dt><dd>{status.server_version.version}</dd></div>
            <div><dt>Protocol</dt><dd>{status.protocol}</dd></div>
            <div><dt>PID</dt><dd>{status.pid}</dd></div>
          </dl>}
        </section>

        <section className="next-card">
          <div>
            <div className="eyebrow">Next implementation slice</div>
            <h2>Open a repository and create the first workspace</h2>
            <p>The onboarding flow will discover a local Git repository, diagnose Codex subscription access, select direct or Herdr runtime, and freeze the initial policy without exposing socket paths or entity IDs.</p>
          </div>
          <button disabled>Onboarding not implemented yet</button>
        </section>

        <section className="principles">
          <article><span>01</span><h3>Daemon is authority</h3><p>The browser never reads SQLite, provider homes, Git, or runtime sockets directly.</p></article>
          <article><span>02</span><h3>Effects stay visible</h3><p>Future conversational actions will render exact committed receipts and approval gates.</p></article>
          <article><span>03</span><h3>Herdr stays optional</h3><p>Direct runs work headlessly; interactive terminal hosting adds capability, not truth.</p></article>
        </section>
      </main>
    </div>
  );
}

createRoot(document.getElementById("root")!).render(<StrictMode><App /></StrictMode>);
