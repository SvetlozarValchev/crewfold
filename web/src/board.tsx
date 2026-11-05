import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { X, Plus, FileText, Check, Archive } from "lucide-react";

export type BoardDocument = { id: string; room_id: string; participant_id?: string; name: string; media_type: string; byte_size: number; sha256: string; archived_at?: string; created_at: string };
type Person = { id: string; handle: string; display_name: string; status: string };
type Item = { id: string; room_id: string; key: string; number: number; kind: "task" | "question"; title: string; notes: string; status: string; workers: string[]; assignee?: string; subscribers?: string[]; blocker: string; human_input: boolean; entry_count: number; pending_questions: number; preview?: string; documents: BoardDocument[]; archived: boolean; version: number; created_by: string; updated_by: string; created_at: string; updated_at: string };
type Entry = { number: number; revision: number; kind: string; actor: string; actor_kind: string; created_at: string; title?: string; body: string; options: string[]; human_input: boolean; reply_to?: number; answered: boolean; legacy?: boolean };
type History = { revision: number; actor: string; action: string; created_at: string; sequence?: number; body?: string };
type Detail = { item: Item; entries: Entry[]; pending_entries: Entry[]; next_entries?: number; history: History[]; next_history?: number };
type Page = { items: Item[]; total: number; next?: number };
type Call = <T>(method: string, params: unknown) => Promise<T>;
type Props = { room: string; roomStatus: string; owner: boolean; humanHandle: string; participants: Person[]; documents: BoardDocument[]; call: Call; markdown: (text: string) => ReactNode; openDocument: (doc: BoardDocument) => void; question: string; questionOpened: () => void };
const states = [{ value: "backlog", label: "Backlog" }, { value: "in-progress", label: "In progress" }, { value: "done", label: "Done" }];
const emptyPage = (): Page => ({ items: [], total: 0 });
const errorText = (reason: unknown) => reason instanceof Error ? reason.message : "Could not update the board.";
const methodFor = (kind: Item["kind"]) => kind === "task" ? "task" : "thread";
const date = (value: string) => new Date(value).toLocaleString();
const newItem = (room: string, kind: Item["kind"], status = "backlog"): Item => ({ id: "", room_id: room, key: "", number: 0, kind, title: "", notes: "", status: kind === "question" ? "open" : status, workers: [], blocker: "", human_input: false, entry_count: 0, pending_questions: 0, documents: [], archived: false, version: 0, created_by: "", updated_by: "", created_at: "", updated_at: "" });
function Signals({ item }: { item: Item }) {
  return <>{item.blocker && <span className="board-signal blocked">! Blocked</span>}{item.human_input && <span className="board-signal awaiting">? Awaiting human input</span>}{item.status === "resolved" && <span className="board-signal resolved">✓ Resolved</span>}{item.archived && <span className="board-signal">Archived</span>}</>;
}

type ThreadScroll = { top: number; bottom: boolean; anchor?: string; offset: number };
function rememberThreadScroll(dialog: HTMLDialogElement | null): ThreadScroll | null {
  if (!dialog) return null;
  const top = dialog.getBoundingClientRect().top;
  const headerBottom = dialog.querySelector("header")?.getBoundingClientRect().bottom ?? top;
  const anchor = [...dialog.querySelectorAll<HTMLElement>(".thread-post")].find(post => post.getBoundingClientRect().bottom > headerBottom + 12);
  return { top: dialog.scrollTop, bottom: dialog.scrollHeight - dialog.scrollTop - dialog.clientHeight < 70, anchor: anchor?.id, offset: anchor ? anchor.getBoundingClientRect().top - top : 0 };
}
function restoreThreadScroll(dialog: HTMLDialogElement | null, saved: ThreadScroll | null) {
  if (!dialog || !saved) return;
  if (saved.bottom) { dialog.scrollTop = dialog.scrollHeight; return; }
  const anchor = saved.anchor ? dialog.querySelector<HTMLElement>("#" + saved.anchor) : null;
  if (anchor) dialog.scrollTop += anchor.getBoundingClientRect().top - dialog.getBoundingClientRect().top - saved.offset;
  else dialog.scrollTop = saved.top;
}
function mergeEntries(previous: Entry[], incoming: Entry[]) {
  const byNumber = new Map(previous.map(entry => [entry.number, entry]));
  for (const entry of incoming) byNumber.set(entry.number, entry);
  const entries = [...byNumber.values()].sort((a, b) => a.number - b.number);
  return entries.map(entry => entry.kind === "question" && !entry.answered && entries.some(answer => answer.kind === "answer" && answer.reply_to === entry.number && (!entry.human_input || ["owner", "member"].includes(answer.actor_kind))) ? { ...entry, answered: true } : entry);
}
function metadata(item: Item) {
  return JSON.stringify([item.title, item.notes, item.status, item.blocker, item.workers, item.documents]);
}

export function RoomBoard(props: Props) {
  const { room, owner, call } = props;
  const [archived, setArchived] = useState(false), [resolved, setResolved] = useState(false), [doneOpen, setDoneOpen] = useState(false);
  const [search, setSearch] = useState(""), [query, setQuery] = useState(""), [worker, setWorker] = useState("");
  const [tasks, setTasks] = useState<Record<string, Page>>({ backlog: emptyPage(), "in-progress": emptyPage(), done: emptyPage() });
  const [threads, setThreads] = useState<Page>(emptyPage());
  const [editor, setEditor] = useState<Detail | null>(null), [error, setError] = useState(""), [loading, setLoading] = useState(true);
  const [refresh, setRefresh] = useState(0), [moreBusy, setMoreBusy] = useState("");
  const generation = useRef(0), openGeneration = useRef(0), moving = useRef(false);
  const closed = props.roomStatus !== "open";
  useEffect(() => { const timer = setTimeout(() => setQuery(search), 200); return () => clearTimeout(timer); }, [search]);
  useEffect(() => {
    let live = true, running = false; const gen = ++generation.current;
    setLoading(true); setError(""); setTasks({ backlog: emptyPage(), "in-progress": emptyPage(), done: emptyPage() }); setThreads(emptyPage());
    const load = async (initial: boolean) => {
      if (running) return; running = true;
      try {
        const pages = await Promise.all(states.map(s => call<Page>("task.list", { room, archived, filter_status: s.value, search: query, worker, limit: s.value === "done" ? 5 : 50 })));
        const qs = await call<Page>("thread.list", { room, filter_status: resolved ? "resolved" : "open", limit: 50 });
        if (!live || generation.current !== gen) return;
        setTasks(current => Object.fromEntries(states.map((s, i) => [s.value, !initial && current[s.value].items.length > (s.value === "done" ? 5 : 50) ? current[s.value] : pages[i]])));
        setThreads(current => !initial && current.items.length > 50 ? current : qs); setError("");
      } catch (reason) { if (live) setError(errorText(reason)); }
      finally { running = false; if (live) setLoading(false); }
    };
    void load(true); const timer = setInterval(() => void load(false), 4000);
    return () => { live = false; clearInterval(timer); };
  }, [room, archived, resolved, query, worker, call, refresh]);
  useEffect(() => () => { ++openGeneration.current; }, []);
  const open = useCallback(async (kind: Item["kind"], key: string) => {
    const request = ++openGeneration.current;
    try { const detail = await call<Detail>(methodFor(kind) + ".show", { room, key }); if (request === openGeneration.current) { setEditor(detail); setError(""); } }
    catch (reason) { if (request === openGeneration.current) setError(errorText(reason)); }
  }, [room, call]);
  useEffect(() => { if (props.question) { void open(props.question.startsWith("T-") ? "task" : "question", props.question); props.questionOpened(); } }, [props.question, props.questionOpened, open]);
  const more = async (kind: Item["kind"], status: string) => {
    if (moreBusy) return; setMoreBusy(status);
    const gen = generation.current, page = kind === "task" ? tasks[status] : threads;
    try { const next = await call<Page>(methodFor(kind) + ".list", { room, filter_status: status, after: page.next, limit: 50, ...(kind === "task" ? { archived, search: query, worker } : {}) });
      if (gen !== generation.current) return;
      const merged = { ...next, items: [...page.items, ...next.items.filter(i => !page.items.some(p => p.id === i.id))] };
      if (kind === "task") setTasks(p => ({ ...p, [status]: merged })); else setThreads(merged);
    } catch (reason) { setError(errorText(reason)); } finally { setMoreBusy(""); }
  };
  const move = async (key: string, status: string) => {
    if (closed || archived || moving.current) return;
    const item = Object.values(tasks).flatMap(p => p.items).find(i => i.key === key); if (!item || item.status === status) return;
    moving.current = true;
    try { await call("task.update", { room, owner, key, status, version: item.version, operation_id: crypto.randomUUID() }); setRefresh(n => n + 1); }
    catch (reason) { setError(errorText(reason)); } finally { moving.current = false; }
  };
  const create = (kind: Item["kind"], status?: string) => { ++openGeneration.current; setEditor({ item: newItem(room, kind, status), history: [], entries: [], pending_entries: [] }); };
  const close = () => { ++openGeneration.current; setEditor(null); };
  const card = (t: Item) => <button className="board-card" key={t.id} draggable={t.kind === "task" && !closed && !archived} onDragStart={e => { e.dataTransfer.setData("text/crewfold-task", t.key); e.dataTransfer.effectAllowed = "move"; }} onClick={() => void open(t.kind, t.key)}><div className="board-card-meta"><span>{t.key}</span><Signals item={t} /></div><h3>{t.title}</h3><p>{(t.kind === "task" ? t.notes : t.preview ?? "").slice(0, 150)}{(t.kind === "task" ? t.notes : t.preview ?? "").length > 150 ? "…" : ""}</p>{t.kind === "task" ? <div className="board-workers">{t.workers.length ? t.workers.map(w => <span key={w}>@{w}{props.participants.find(p => p.handle === w)?.status === "left" ? " · left" : ""}</span>) : <span className="unassigned">No one listed</span>}</div> : <small>Started by @{t.created_by}</small>}<small>{t.entry_count} {t.entry_count === 1 ? "post" : "posts"}{t.pending_questions ? " · " + t.pending_questions + " unanswered" : ""}</small>{t.documents.map(d => <div className="board-document-label" key={d.id}><FileText size={12} />{d.name}</div>)}</button>;
  return <section className="board-view" aria-label="Shared room board">
    {error && <p className="error" role="alert">{error}</p>}{closed && <p className="board-hint">This room is archived. Its board is read-only.</p>}
    <div className="board-layout"><section className="board-tasks" aria-label="Tasks">
      <div className="board-toolbar"><input type="search" placeholder="Search tasks…" aria-label="Search tasks" value={search} onChange={e => setSearch(e.target.value)} /><select aria-label="Filter by worker" value={worker} onChange={e => setWorker(e.target.value)}><option value="">Everyone</option><option value="owner">@owner</option>{props.participants.map(p => <option key={p.id} value={p.handle}>@{p.handle}{p.status === "left" ? " · left" : ""}</option>)}{props.humanHandle && props.humanHandle !== "owner" && <option value={props.humanHandle}>@{props.humanHandle}</option>}</select><button aria-pressed={archived} onClick={() => setArchived(!archived)}>{archived && <Check size={12} />} Archived</button><button onClick={() => setRefresh(n => n + 1)} disabled={loading}>Refresh</button><button className="primary board-add" disabled={closed} onClick={() => create("task")}><Plus size={13} /> Add task</button></div>
      {archived && <div className="board-filter-notice"><span>Viewing archived tasks. Active tasks are hidden.</span><button onClick={() => setArchived(false)}>Back to active tasks</button></div>}
      <div className="board-columns">{states.slice(0, 2).map(s => <section key={s.value} className={"board-column " + s.value} aria-label={s.label} onDragOver={e => { if (!archived && !closed) e.preventDefault(); }} onDrop={e => { e.preventDefault(); void move(e.dataTransfer.getData("text/crewfold-task"), s.value); }}><h2><i />{s.label}<span>{tasks[s.value].total}</span></h2>{tasks[s.value].items.map(card)}{!tasks[s.value].items.length && <p className="board-hint">{loading ? "Loading…" : "No tasks here."}</p>}{tasks[s.value].next && <button disabled={!!moreBusy} onClick={() => void more("task", s.value)}>Load more</button>}{!archived && !closed && <button className="board-column-add" onClick={() => create("task", s.value)}>+ Add task</button>}</section>)}</div>
      <section className="board-done" onDragOver={e => { if (!archived && !closed) e.preventDefault(); }} onDrop={e => { e.preventDefault(); void move(e.dataTransfer.getData("text/crewfold-task"), "done"); }}><button className="board-done-toggle" aria-expanded={doneOpen} onClick={() => setDoneOpen(!doneOpen)}><span>{doneOpen ? "▾" : "▸"} Done</span><span>{tasks.done.total} completed tasks</span></button>{doneOpen && <><div className="board-done-cards">{tasks.done.items.map(card)}</div>{!tasks.done.total && <p className="board-hint">No completed tasks.</p>}{tasks.done.next && <button disabled={!!moreBusy} onClick={() => void more("task", "done")}>Show more completed tasks</button>}</>}</section>
      <p className="board-hint">Anyone in the room can maintain a card. Assignees receive mentions; anyone can contribute. Drag to change status, or open a card.</p>
    </section><aside className="board-threads"><header><h2>{resolved ? "Resolved threads" : "Threads"} <span>{threads.total}</span></h2><button disabled={closed} onClick={() => create("question")}><Plus size={13} /> New thread</button></header><p className="board-hint">Discussions, updates, and questions. Anyone in the room can contribute.</p><button className="board-thread-filter" aria-pressed={resolved} onClick={() => setResolved(!resolved)}>{resolved && <Check size={12} />}{resolved ? "Viewing resolved · show open" : "Show resolved threads"}</button>{threads.items.map(card)}{!threads.items.length && <p className="board-hint">{loading ? "Loading…" : resolved ? "No resolved threads." : "No open threads."}</p>}{threads.next && <button disabled={!!moreBusy} onClick={() => void more("question", resolved ? "resolved" : "open")}>Load more threads</button>}</aside></div>
    {editor && <BoardEditor key={editor.item.id || "new-" + editor.item.kind} {...props} detail={editor} close={close} saved={d => { setEditor(d); setRefresh(n => n + 1); }} reload={() => void open(editor.item.kind, editor.item.key)} />}
  </section>;
}

function BoardEditor(props: Props & { detail: Detail; close: () => void; saved: (d: Detail) => void; reload: () => void }) {
  const { item } = props.detail; const creating = !item.id, thread = item.kind === "question", closed = props.roomStatus !== "open", resolved = thread && item.status === "resolved";
  const [editing, setEditing] = useState(creating);
  const [title, setTitle] = useState(item.title), [notes, setNotes] = useState(item.notes), [status, setStatus] = useState(item.status), [blocker, setBlocker] = useState(item.blocker);
  const [workers, setWorkers] = useState(item.workers), [docs, setDocs] = useState(item.documents.map(d => d.id)), [message, setMessage] = useState("");
  const [history, setHistory] = useState(props.detail.history), [nextHistory, setNextHistory] = useState(props.detail.next_history);
  const [entries, setEntries] = useState(props.detail.entries), [nextEntries, setNextEntries] = useState(props.detail.next_entries);
  const [pageBusy, setPageBusy] = useState(false), [busy, setBusy] = useState(false), [error, setError] = useState(""), [changed, setChanged] = useState(false);
  const dialog = useRef<HTMLDialogElement>(null), lastOperation = useRef({ payload: "", id: "" });
  const scroll = useRef<ThreadScroll | null>(null), resetDraft = useRef(false), clearMessage = useRef(false), posting = useRef(false);
  const currentDetail = useRef(props.detail), editingRef = useRef(editing), previousDetail = useRef(props.detail);
  const onSaved = useRef(props.saved); onSaved.current = props.saved; currentDetail.current = props.detail; editingRef.current = editing;
  const [unseen, setUnseen] = useState(0), [syncError, setSyncError] = useState("");
  useEffect(() => { const d = dialog.current; d?.showModal(); return () => d?.close(); }, []);
  useLayoutEffect(() => {
    if (previousDetail.current === props.detail) return;
    const previous = previousDetail.current; previousDetail.current = props.detail;
    if (!scroll.current) scroll.current = rememberThreadScroll(dialog.current);
    setEntries(old => mergeEntries(old, [...props.detail.pending_entries, ...props.detail.entries]));
    setHistory(old => [...new Map([...old, ...props.detail.history].map(h => [h.revision, h])).values()].sort((a, b) => b.revision - a.revision));
    if (resetDraft.current || !editingRef.current) {
      setTitle(item.title); setNotes(item.notes); setStatus(item.status); setBlocker(item.blocker); setWorkers(item.workers); setDocs(item.documents.map(d => d.id)); setChanged(false);
    } else if (metadata(previous.item) !== metadata(item)) setChanged(true);
    if (resetDraft.current) { setEditing(false); setError(""); resetDraft.current = false; }
    if (clearMessage.current) { setMessage(""); clearMessage.current = false; }
  }, [props.detail, item]);
  useLayoutEffect(() => { restoreThreadScroll(dialog.current, scroll.current); scroll.current = null; }, [entries, history]);
  useEffect(() => {
    if (creating) return;
    let live = true, running = false;
    const refresh = async () => {
      if (running || posting.current) return; running = true;
      try {
        const start = currentDetail.current;
        const next = await props.call<Detail>(methodFor(item.kind) + ".show", { room: props.room, key: item.key });
        const tail = start.entries.at(-1)?.number ?? 0;
        let cursor = next.next_entries;
        // Fill a burst larger than one page; do not silently skip intervening posts.
        while (cursor && tail && next.entries[0]?.number > tail + 1) {
          const page = await props.call<Detail>(methodFor(item.kind) + ".show", { room: props.room, key: item.key, entries_before: cursor });
          if (page.item.version !== next.item.version) return;
          next.entries = [...page.entries, ...next.entries]; cursor = page.next_entries;
        }
        if (!live || posting.current) return;
        setSyncError("");
        if (next.item.version <= currentDetail.current.item.version) return;
        scroll.current = rememberThreadScroll(dialog.current);
        if (!scroll.current?.bottom) setUnseen(n => n + Math.max(0, next.item.entry_count - currentDetail.current.item.entry_count));
        onSaved.current(next); setSyncError("");
      } catch { if (live) setSyncError("Live updates are temporarily unavailable. Retrying automatically; your draft is kept."); }
      finally { running = false; }
    };
    const timer = setInterval(() => void refresh(), 2500);
    return () => { live = false; clearInterval(timer); };
  }, [creating, item.key, item.kind, props.call, props.room]);
  const reload = () => { scroll.current = rememberThreadScroll(dialog.current); resetDraft.current = true; clearMessage.current = true; props.reload(); };
  const jumpToLatest = () => { if (dialog.current) dialog.current.scrollTop = dialog.current.scrollHeight; setUnseen(0); };
  const mutate = async (action: string, fields: Record<string, unknown> = {}) => {
    if (posting.current || closed || (changed && action === "update")) return; posting.current = true; setBusy(true); setError("");
    const input = { room: props.room, owner: props.owner, key: item.key, version: item.version, ...fields };
    const payload = JSON.stringify({ action, input });
    if (lastOperation.current.payload !== payload) lastOperation.current = { payload, id: crypto.randomUUID() };
    try { const detail = await props.call<Detail>(methodFor(item.kind) + "." + action, { ...input, operation_id: lastOperation.current.id }); scroll.current = rememberThreadScroll(dialog.current); resetDraft.current = true; clearMessage.current = action === "reply"; setUnseen(0); props.saved(detail); }
    catch (reason) { setError(errorText(reason)); } finally { posting.current = false; setBusy(false); }
  };
  const cancelEdit = () => { setTitle(item.title); setNotes(item.notes); setStatus(item.status); setBlocker(item.blocker); setWorkers(item.workers); setDocs(item.documents.map(d => d.id)); setError(""); setChanged(false); setEditing(false); };
  const save = (event: React.FormEvent) => { event.preventDefault(); void mutate(creating ? "create" : "update", { title: title.trim(), documents: docs, ...(!thread && workers.length ? { assignee: workers[0] } : {}), ...(!thread || creating ? { notes } : {}), ...(!thread ? { status, blocker } : {}) }); };
  const loadPage = async (historyPage: boolean) => { if (pageBusy) return; setPageBusy(true); try { const page = await props.call<Detail>(methodFor(item.kind) + ".show", { room: props.room, key: item.key, ...(historyPage ? { history_before: nextHistory } : { entries_before: nextEntries }) }); if (page.item.version !== currentDetail.current.item.version) { setSyncError("New activity arrived while loading earlier posts. Try again."); return; } scroll.current = rememberThreadScroll(dialog.current); if (historyPage) { setHistory(old => [...old, ...page.history.filter(h => !old.some(p => p.revision === h.revision))]); setNextHistory(page.next_history); } else { setEntries(old => [...page.entries.filter(e => !old.some(p => p.number === e.number)), ...old]); setNextEntries(page.next_entries); } } catch (reason) { setError(errorText(reason)); } finally { setPageBusy(false); } };
  const workerOptions = [...new Set(["owner", ...props.participants.map(p => p.handle), ...(props.humanHandle ? [props.humanHandle] : []), ...workers])];
  const availableDocs = [...props.documents.filter(d => !d.archived_at || docs.includes(d.id))];
  for (const d of item.documents) if (!availableDocs.some(p => p.id === d.id)) availableDocs.push(d);
  const missingPending = props.detail.pending_entries.filter(e => !entries.some(p => p.number === e.number));
  const post = (e: Entry) => <article className={"thread-post post-" + e.kind} key={e.number} id={"board-post-" + e.number}><div className="thread-avatar" aria-hidden="true">{e.actor === props.humanHandle ? "YOU" : e.actor.split("-").filter(p => !["agent", "developer"].includes(p)).map(p => p[0]).join("").slice(0, 2).toUpperCase()}</div><div className="thread-post-content"><header><strong>@{e.actor}</strong><span>{e.kind === "message" ? "Update" : e.kind === "question" ? "Question" : e.kind === "answer" ? "Answer" : e.kind === "resolved" ? "✓ Resolved" : "Reopened"}</span><time>{date(e.created_at)}</time><small>P-{e.number}</small></header>{e.reply_to && <p className="thread-reply-target">In reply to <a href={"#board-post-" + e.reply_to}>P-{e.reply_to}</a>{entries.find(p => p.number === e.reply_to)?.title ? " · " + entries.find(p => p.number === e.reply_to)?.title : ""}</p>}{e.title && <h3>{e.title}</h3>}{props.markdown(e.body)}{e.kind === "question" && (e.answered || resolved ? <details className="thread-past-choices"><summary>{e.answered ? "Answered" : "Closed"} · {e.options.length ? e.options.length + " original choices" : "free-form question"} · read-only</summary>{e.options.map(o => <p key={o}>{o}</p>)}</details> : <><span className={"board-signal " + (e.human_input ? "awaiting" : "")}>{e.human_input ? "? Awaiting human input · anyone may answer" : "Unanswered · anyone may answer"}</span><PostAnswer entry={e} disabled={busy || closed || changed} submit={body => void mutate("answer", { body, reply_to: e.number })} /></>)}</div></article>;
  return <dialog className="board-editor thread-editor" ref={dialog} onScroll={e => { const d = e.currentTarget; if (d.scrollHeight - d.scrollTop - d.clientHeight < 70) setUnseen(0); }} onCancel={e => { e.preventDefault(); if (!busy) props.close(); }} aria-labelledby="board-editor-title"><header><div><span>{creating ? "NEW SHARED RECORD" : item.key + " · " + (thread ? "Thread" : "Task discussion")}</span><h2 id="board-editor-title">{creating ? thread ? "Start a thread" : "Add a task" : item.title}</h2></div><div className="board-record-actions">{unseen > 0 && !editing && <button className="thread-new-posts" onClick={jumpToLatest}>{unseen} new {unseen === 1 ? "post" : "posts"} ↓</button>}{!creating && !editing && !closed && <>{thread ? <button disabled={busy || changed} onClick={() => void mutate(resolved ? "reopen" : "resolve")}>{resolved ? "Reopen thread" : "Mark resolved"}</button> : <button disabled={busy || changed} onClick={() => void mutate("update", { status: item.status === "done" ? "in-progress" : "done" })}>{item.status === "done" ? "Resume task" : "Mark task done"}</button>}{!resolved && <button disabled={busy} onClick={() => setEditing(true)}>Edit</button>}</>}<button disabled={busy} onClick={props.close} aria-label="Close"><X size={18} /></button></div></header><div className="board-editor-body">
    {!creating && !editing && <div className="thread-live-status" aria-live="polite">{syncError ? <span>{syncError}</span> : <span>Live discussion</span>}</div>}
    {(changed || error) && <div role="alert" className="board-change-notice"><p>{error || "The record details changed while you were editing. Your draft is kept; reload the current details before saving."}</p>{!creating && <button onClick={reload} disabled={busy}>Reload current record (discard draft)</button>}</div>}
    {!editing && <section className="board-record-summary"><div className="board-record-meta">{!thread && <select aria-label="Task status" value={item.status} disabled={busy || closed || changed} onChange={e => void mutate("update", { status: e.target.value })}>{states.map(s => <option key={s.value} value={s.value}>{s.label}</option>)}</select>}{thread && !resolved && <span>Open thread</span>}<Signals item={item} />{thread ? <span>OP: @{item.created_by}</span> : <span>Assignee: @{item.assignee || item.workers[0]}</span>}<span>{item.entry_count} posts · anyone in the room can contribute</span></div>{!thread && item.notes && <div className="task-description">{props.markdown(item.notes)}</div>}{item.blocker && <p className="board-record-blocker">! Blocked: {item.blocker}</p>}{!thread && !closed && <button disabled={busy || changed} onClick={() => void mutate(item.archived ? "restore" : "archive")}><Archive size={12} />{item.archived ? "Restore task" : "Archive task"}</button>}</section>}
    {editing && <form className="board-edit-form" onSubmit={save}><fieldset disabled={busy || closed}><label>Title<input required maxLength={240} value={title} onChange={e => setTitle(e.target.value)} /></label>{(!thread || creating) && <label>{thread ? "First post · Markdown" : "Description · Markdown"}<textarea value={notes} onChange={e => setNotes(e.target.value)} maxLength={16384} rows={4} /></label>}{!thread && <><div className="board-fields"><label>Status<select value={status} onChange={e => setStatus(e.target.value)}>{states.map(s => <option key={s.value} value={s.value}>{s.label}</option>)}</select></label><label>Blocked by · optional<input value={blocker} maxLength={1000} onChange={e => setBlocker(e.target.value)} /></label></div></>}{!thread && <label>Assignee<select value={workers[0] || ""} onChange={e => setWorkers([e.target.value])}>{creating && !workers.length && <option value="">You · creator (default)</option>}{workerOptions.map(w => <option key={w} value={w}>@{w}{props.participants.find(p => p.handle === w)?.status === "left" ? " (left)" : ""}</option>)}</select></label>}
    <details className="board-document-picker"><summary>Linked documents ({docs.length}) · pinned revisions</summary><p className="board-hint">Choose existing revisions. Re-uploading a document does not change this evidence link.</p>{availableDocs.map(d => <label className="board-checkbox" key={d.id}><input type="checkbox" checked={docs.includes(d.id)} onChange={e => setDocs(e.target.checked ? [...docs, d.id] : docs.filter(id => id !== d.id))} />{d.name} · @{props.participants.find(p => p.id === d.participant_id)?.handle ?? "owner"} · {date(d.created_at)}</label>)}{!availableDocs.length && <p className="board-hint">No shared documents yet.</p>}</details><footer>{!creating && <button type="button" onClick={cancelEdit}>Cancel edit</button>}<button type="submit" className="primary" disabled={!title.trim() || changed}>{busy ? "Saving…" : creating ? thread ? "Start & notify room" : "Create task" : "Save changes"}</button></footer></fieldset></form>}
    {!editing && item.documents.map(d => <button key={d.id} className="board-document-open" onClick={() => { props.close(); props.openDocument(d); }}><FileText size={14} />{d.name}</button>)}
    {!creating && !editing && <><section className="thread-timeline" aria-label="Discussion">{nextEntries && <button disabled={pageBusy} onClick={() => void loadPage(false)}>Load earlier posts</button>}{missingPending.length > 0 && <section className="thread-earlier-pending"><h3>Unanswered questions from earlier posts</h3>{missingPending.map(post)}</section>}{entries.map(post)}{!entries.length && <p className="board-hint">No discussion yet. Share an update or add context below.</p>}</section>{resolved ? <p className="board-hint">Resolved · read-only. Reopen this thread to continue.</p> : !item.pending_questions && <form className="thread-composer" onSubmit={e => { e.preventDefault(); void mutate("reply", { body: message.trim() }); }}><fieldset disabled={busy || closed || changed}><label>Reply to this thread<textarea rows={2} maxLength={16384} placeholder="Write a message…" value={message} onChange={e => setMessage(e.target.value)} /></label><footer><button className="primary" disabled={!message.trim()}>Send</button></footer></fieldset></form>}
    <p className="board-hint">Agent participants follow this discussion automatically.{!!item.subscribers?.length && <> Subscribers: {item.subscribers.map(w => "@" + w).join(", ")}.</>}</p><details className="board-history"><summary>Record history <span>r{item.version}</span></summary><p className="board-hint">Created by @{item.created_by}. Description and metadata edits are recorded here, separately from discussion.</p>{history.map(h => <article key={h.revision}><header>r{h.revision} · @{h.actor} · {h.action}<time>{date(h.created_at)}</time></header></article>)}{nextHistory && <button disabled={pageBusy} onClick={() => void loadPage(true)}>Load older history</button>}</details></>}
  </div></dialog>;
}

function PostAnswer({ entry, disabled, submit }: { entry: Entry; disabled: boolean; submit: (body: string) => void }) {
  const [selected, setSelected] = useState<string | null>(null), [answer, setAnswer] = useState("");
  const revision = useRef(entry.revision);
  const [revised, setRevised] = useState(false);
  useEffect(() => { if (revision.current !== entry.revision) { revision.current = entry.revision; setSelected(null); setRevised(true); } }, [entry.revision]);
  const body = selected ?? answer.trim();
  return <form className="thread-answer" onSubmit={e => { e.preventDefault(); if (body) submit(body); }}><fieldset disabled={disabled}>{revised && <p className="board-hint" role="status">This question was updated. Review it before sending; your written draft is kept.</p>}{!!entry.options.length && <fieldset className="board-answer-choices"><legend>Choose an answer</legend>{entry.options.map(option => <label key={option} className="board-answer-choice"><input type="radio" name={"answer-" + entry.number} checked={selected === option} onChange={() => { setSelected(option); setAnswer(""); }} /><span>{option}</span></label>)}</fieldset>}<label>{entry.options.length ? "Or write your own answer" : "Your answer"}<input type="text" maxLength={16384} placeholder="Type an answer…" value={answer} onChange={e => { setAnswer(e.target.value); setSelected(null); }} /></label><footer><small>Answers P-{entry.number} only · notifies the room</small><button className="primary" disabled={!body}>Send answer</button></footer></fieldset></form>;
}
