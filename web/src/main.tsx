import { StrictMode, type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { Archive, Bot, ChevronRight, FileText, LoaderCircle, MessageSquare, Plus, Send, Users, X } from "lucide-react";
import "./styles.css";

type Connection = "connecting" | "connected" | "unauthorized" | "failed";
type Room = { id: string; slug: string; title: string; topic: string; status: "open" | "archived"; steward_id?: string; created_at: string; updated_at: string; last_sequence: number };
type Participant = { id: string; room_id: string; handle: string; display_name: string; kind: "agent" | "steward"; working_directory?: string; status: "invited" | "joined" | "left"; context?: string; context_updated_at?: string; last_read_sequence: number; joined_at: string; last_seen_at: string; unread_count: number };
type Document = { id: string; room_id: string; participant_id?: string; name: string; media_type: string; byte_size: number; sha256: string; created_at: string };
type Message = { sequence: number; id: string; room_id: string; participant_id?: string; sender_handle: string; sender_name: string; sender_kind: "owner" | "agent" | "steward" | "system"; kind: "message" | "context" | "document" | "system"; body: string; document?: Document; created_at: string };
type Snapshot = { room: Room; participants: Participant[]; messages: Message[]; documents: Document[] };
type RPCResponse<T> = { id: string; result?: T; error?: string };
type OpenDocument = { document: Document; content: string };

const tokenKey = "crewfold-room-session";

async function authenticate(): Promise<string> {
  const existing = sessionStorage.getItem(tokenKey);
  if (existing) return existing;
  const bootstrap = new URLSearchParams(location.hash.slice(1)).get("bootstrap");
  if (!bootstrap) throw new Error("Open Crewfold with `crewfold open` to create an owner-local browser session.");
  const response = await fetch("/api/session", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ bootstrap }) });
  if (!response.ok) throw new Error(await response.text());
  const result = await response.json() as { token: string };
  sessionStorage.setItem(tokenKey, result.token);
  history.replaceState(null, "", "/");
  return result.token;
}

async function rpc<T>(token: string, method: string, params: unknown): Promise<T> {
  const id = crypto.randomUUID();
  const response = await fetch("/api/rpc", { method: "POST", headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` }, body: JSON.stringify({ id, method, params }) });
  if (response.status === 401) { sessionStorage.removeItem(tokenKey); throw new Error("Owner session expired. Run `crewfold open` again."); }
  if (!response.ok) throw new Error(await response.text());
  const envelope = await response.json() as RPCResponse<T>;
  if (envelope.error) throw new Error(envelope.error);
  if (envelope.result === undefined) throw new Error("Crewfold returned no result.");
  return envelope.result;
}

function decodeBase64(value: string) {
  const binary = atob(value);
  const bytes = Uint8Array.from(binary, (character) => character.charCodeAt(0));
  return new TextDecoder().decode(bytes);
}

function formatTime(value: string) {
  const date = new Date(value);
  return new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" }).format(date);
}

function formatSize(bytes: number) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(bytes < 10 * 1024 ? 1 : 0)} KB`;
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`;
}

function MarkdownInline({ text }: { text: string }) {
  const tokens = text.split(/(`[^`]+`|\*\*[^*]+\*\*|https?:\/\/[^\s]+)/g);
  return <>{tokens.map((token, index) => {
    if (token.startsWith("`") && token.endsWith("`")) return <code key={index}>{token.slice(1, -1)}</code>;
    if (token.startsWith("**") && token.endsWith("**")) return <strong key={index}>{token.slice(2, -2)}</strong>;
    if (token.startsWith("http://") || token.startsWith("https://")) return <a key={index} href={token} target="_blank" rel="noreferrer">{token}</a>;
    return token;
  })}</>;
}

function Markdown({ text }: { text: string }) {
  const lines = text.replaceAll("\r\n", "\n").split("\n");
  const blocks: ReactNode[] = [];
  let index = 0;
  while (index < lines.length) {
    if (!lines[index].trim()) { index++; continue; }
    if (lines[index].trimStart().startsWith("```")) {
      const body: string[] = []; index++;
      while (index < lines.length && !lines[index].trimStart().startsWith("```")) body.push(lines[index++]);
      index++; blocks.push(<pre key={`code-${index}`}><code>{body.join("\n")}</code></pre>); continue;
    }
    const heading = lines[index].match(/^(#{1,4})\s+(.+)$/);
    if (heading) { blocks.push(heading[1].length <= 2 ? <h2 key={index}><MarkdownInline text={heading[2]} /></h2> : <h3 key={index}><MarkdownInline text={heading[2]} /></h3>); index++; continue; }
    if (/^\s*[-*]\s+/.test(lines[index])) {
      const items: string[] = [];
      while (index < lines.length) { const match = lines[index].match(/^\s*[-*]\s+(.+)$/); if (!match) break; items.push(match[1]); index++; }
      blocks.push(<ul key={`list-${index}`}>{items.map((item, itemIndex) => <li key={itemIndex}><MarkdownInline text={item} /></li>)}</ul>); continue;
    }
    const paragraph: string[] = [];
    while (index < lines.length && lines[index].trim() && !/^(#{1,4})\s+/.test(lines[index]) && !/^\s*[-*]\s+/.test(lines[index]) && !lines[index].trimStart().startsWith("```")) paragraph.push(lines[index++].trim());
    blocks.push(<p key={`p-${index}`}><MarkdownInline text={paragraph.join(" ")} /></p>);
  }
  return <div className="markdown">{blocks}</div>;
}

function CreateRoom({ token, close, created }: { token: string; close: () => void; created: (snapshot: Snapshot) => void }) {
  const [slug, setSlug] = useState(""); const [title, setTitle] = useState(""); const [topic, setTopic] = useState(""); const [steward, setSteward] = useState("");
  const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  const submit = async (event: React.FormEvent) => {
    event.preventDefault(); setBusy(true); setError("");
    try { created(await rpc<Snapshot>(token, "room.create", { slug, title, topic, steward_handle: steward })); }
    catch (reason) { setError(reason instanceof Error ? reason.message : "Could not create room."); }
    finally { setBusy(false); }
  };
  return <div className="modal-backdrop"><form className="modal" onSubmit={submit}>
    <header><div><span>NEW SHARED ROOM</span><h1>Create a room</h1></div><button type="button" onClick={close} aria-label="Close"><X size={18} /></button></header>
    <label>ROOM HANDLE<input autoFocus required maxLength={63} value={slug} onChange={(event) => setSlug(event.target.value.toLowerCase().replace(/[^a-z0-9-]/g, ""))} placeholder="tire-slip" /></label>
    <label>TITLE<input required maxLength={120} value={title} onChange={(event) => setTitle(event.target.value)} placeholder="Tire slip model" /></label>
    <label>WHAT ARE THEY SHARING?<textarea required maxLength={2048} value={topic} onChange={(event) => setTopic(event.target.value)} placeholder="Compare the new tire slip model across both simulations." /></label>
    <label>OPTIONAL STEWARD HANDLE<input maxLength={63} value={steward} onChange={(event) => setSteward(event.target.value.toLowerCase())} placeholder="slip-steward" /><small>The steward is invited but not launched. Join it from any external agent session.</small></label>
    {error && <p className="error">{error}</p>}
    <footer><button type="button" onClick={close}>cancel</button><button className="primary" disabled={busy || !slug || !title || !topic}>{busy ? <LoaderCircle className="spin" size={15} /> : <Plus size={15} />} create room</button></footer>
  </form></div>;
}

function MessageRow({ message, openDocument }: { message: Message; openDocument: (document: Document) => void }) {
  if (message.kind === "system") return <div className="system-message"><span>#{message.sequence}</span>{message.body}</div>;
  return <article className={`message ${message.sender_kind}`}>
    <div className="message-mark">{message.sender_kind === "steward" ? <Bot size={15} /> : message.sender_handle.slice(0, 2).toUpperCase()}</div>
    <div className="message-content"><header><strong>{message.sender_name}</strong><span>@{message.sender_handle}</span><time>{formatTime(message.created_at)}</time>{message.kind === "context" && <em>context</em>}</header><p>{message.body}</p>
      {message.document && <button className="attachment" onClick={() => openDocument(message.document!)}><FileText size={17} /><span><strong>{message.document.name}</strong><small>{message.document.media_type} · {formatSize(message.document.byte_size)}</small></span><ChevronRight size={16} /></button>}
    </div>
  </article>;
}

function App() {
  const [connection, setConnection] = useState<Connection>("connecting"); const [token, setToken] = useState(""); const [rooms, setRooms] = useState<Room[]>([]);
  const [selected, setSelected] = useState(""); const [snapshot, setSnapshot] = useState<Snapshot | null>(null); const [createOpen, setCreateOpen] = useState(false);
  const [message, setMessage] = useState(""); const [sending, setSending] = useState(false); const [error, setError] = useState(""); const [document, setDocument] = useState<OpenDocument | null>(null);
  const feed = useRef<HTMLDivElement>(null); const selectedRoom = selected || rooms.find((room) => room.status === "open")?.slug || rooms[0]?.slug || "";
  const loadRooms = useCallback(async (auth: string) => { const result = await rpc<Room[]>(auth, "room.list", {}); setRooms(result); return result; }, []);
  const loadSnapshot = useCallback(async (auth: string, room: string) => { if (!room) { setSnapshot(null); return; } setSnapshot(await rpc<Snapshot>(auth, "room.snapshot", { room, limit: 500 })); }, []);
  useEffect(() => { void authenticate().then(async (auth) => { setToken(auth); const list = await loadRooms(auth); setSelected(list.find((room) => room.status === "open")?.slug ?? list[0]?.slug ?? ""); setConnection("connected"); }).catch((reason) => { setConnection(String(reason).includes("expired") ? "unauthorized" : "failed"); setError(reason instanceof Error ? reason.message : "Could not connect."); }); }, [loadRooms]);
  useEffect(() => { if (!token || !selectedRoom) return; void loadSnapshot(token, selectedRoom).catch((reason) => setError(reason instanceof Error ? reason.message : "Could not read room.")); const timer = setInterval(() => void Promise.all([loadSnapshot(token, selectedRoom), loadRooms(token)]).catch(() => undefined), 1200); return () => clearInterval(timer); }, [token, selectedRoom, loadSnapshot, loadRooms]);
  const lastSequence = snapshot?.room.last_sequence;
  useEffect(() => { if (!feed.current) return; feed.current.scrollTop = feed.current.scrollHeight; }, [lastSequence]);
  const send = async (event: React.FormEvent) => { event.preventDefault(); if (!message.trim() || !snapshot) return; setSending(true); setError(""); try { await rpc(token, "message.send", { room: snapshot.room.id, owner: true, body: message.trim() }); setMessage(""); await loadSnapshot(token, snapshot.room.id); } catch (reason) { setError(reason instanceof Error ? reason.message : "Could not send."); } finally { setSending(false); } };
  const openDocument = async (item: Document) => { setError(""); try { const result = await rpc<{ document: Document; content_base64: string }>(token, "document.read", { room: item.room_id, document: item.id }); setDocument({ document: result.document, content: decodeBase64(result.content_base64) }); } catch (reason) { setError(reason instanceof Error ? reason.message : "Could not open document."); } };
  const participantByID = useMemo(() => new Map(snapshot?.participants.map((participant) => [participant.id, participant]) ?? []), [snapshot?.participants]);

  if (connection !== "connected") return <main className="gate"><div className="brand-mark">CF</div><h1>{connection === "connecting" ? "Connecting to Crewfold…" : "Crewfold is not available"}</h1>{error && <p>{error}</p>}</main>;
  return <div className={`app ${snapshot ? "" : "no-room"}`}>
    <header className="topbar"><div className="brand"><span>CF</span><strong>Crewfold</strong><small>shared rooms</small></div><div className="current-room">{snapshot ? `# ${snapshot.room.slug}` : "No room selected"}</div><div className="online"><i />local</div></header>
    <aside className="room-rail"><div className="rail-heading"><span>ROOMS</span><button onClick={() => setCreateOpen(true)} aria-label="Create room"><Plus size={16} /></button></div>{rooms.map((room) => <button key={room.id} className={selectedRoom === room.slug ? "selected" : ""} onClick={() => setSelected(room.slug)}><MessageSquare size={15} /><span><strong>{room.title}</strong><small>#{room.slug} · {room.last_sequence} messages</small></span>{room.status === "archived" && <Archive size={13} />}</button>)}{!rooms.length && <p>No rooms yet. Create one to connect independent agent sessions.</p>}</aside>
    <main className={`room-main ${snapshot ? "" : "room-empty"}`}>{snapshot ? <>
      <header className="room-header"><div><span>SHARED ROOM</span><h1>{snapshot.room.title}</h1><p>{snapshot.room.topic}</p></div><div><strong>{snapshot.participants.filter((item) => item.status === "joined").length}</strong><span>joined</span></div><div><strong>{snapshot.documents.length}</strong><span>documents</span></div></header>
      <div className="feed" ref={feed}>{snapshot.messages.map((item) => <MessageRow key={item.id} message={item} openDocument={openDocument} />)}</div>
      <form className="composer" onSubmit={send}><span>›</span><textarea rows={1} value={message} onChange={(event) => setMessage(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} placeholder={`Message #${snapshot.room.slug}`} /><button aria-label="Send message" disabled={sending || !message.trim()}>{sending ? <LoaderCircle className="spin" size={16} /> : <Send size={16} />}</button></form>
      {error && <div className="toast error">{error}</div>}
    </> : <div className="empty"><MessageSquare size={25} /><h1>Create a shared room</h1><p>Independent agent sessions join through the Crewfold CLI.</p><button className="primary" onClick={() => setCreateOpen(true)}><Plus size={15} /> new room</button></div>}</main>
    {snapshot && <aside className="room-context"><>
      <section><h2><Users size={14} /> PARTICIPANTS <span>{snapshot.participants.length}</span></h2>{snapshot.participants.map((participant) => <article className="participant" key={participant.id}><header><i className={participant.status} /><strong>@{participant.handle}</strong>{participant.kind === "steward" && <em>steward</em>}<span>{participant.unread_count ? `${participant.unread_count} unread` : participant.status}</span></header>{participant.working_directory && <code title={participant.working_directory}>{participant.working_directory}</code>}{participant.context && <p>{participant.context}</p>}</article>)}</section>
      <section className="join-help"><h2><MessageSquare size={14} /> CONNECT AN AGENT</h2><code>crewfold room join {snapshot.room.slug} --handle &lt;name&gt;</code><p>Run this inside the agent's working folder. Crewfold records the folder and stable handle; it does not launch or wrap the agent.</p></section>
      <section><h2><FileText size={14} /> DOCUMENTS <span>{snapshot.documents.length}</span></h2>{snapshot.documents.map((item) => <button className="document-row" key={item.id} onClick={() => void openDocument(item)}><FileText size={15} /><span><strong>{item.name}</strong><small>{participantByID.get(item.participant_id ?? "")?.handle ?? "owner"} · {formatSize(item.byte_size)}</small></span></button>)}{!snapshot.documents.length && <p className="quiet">No shared documents yet.</p>}</section>
    </></aside>}
    {createOpen && <CreateRoom token={token} close={() => setCreateOpen(false)} created={(created) => { setCreateOpen(false); setSelected(created.room.slug); void loadRooms(token); setSnapshot(created); }} />}
    {document && <div className="document-panel"><header><div><span>SHARED DOCUMENT</span><h1>{document.document.name}</h1><p>{document.document.media_type} · {formatSize(document.document.byte_size)}</p></div><button onClick={() => setDocument(null)} aria-label="Close document"><X size={18} /></button></header><Markdown text={document.content} /><footer>sha256:{document.document.sha256}</footer></div>}
  </div>;
}

createRoot(document.getElementById("root")!).render(<StrictMode><App /></StrictMode>);
