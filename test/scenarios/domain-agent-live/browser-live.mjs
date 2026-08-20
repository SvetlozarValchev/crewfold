import fs from "node:fs";
import path from "node:path";

const [, , debuggerPort, repositoryPath, outputPath] = process.argv;
if (!debuggerPort || !repositoryPath || !outputPath) throw new Error("usage: browser-live.mjs <debugger-port> <repository-path> <output-path>");

const targets = await fetch(`http://127.0.0.1:${debuggerPort}/json/list`).then((response) => response.json());
const target = targets.find((candidate) => candidate.type === "page");
if (!target?.webSocketDebuggerUrl) throw new Error("Chrome did not expose a page target");
const socket = new WebSocket(target.webSocketDebuggerUrl);
await new Promise((resolve, reject) => {
  socket.addEventListener("open", resolve, { once: true });
  socket.addEventListener("error", () => reject(new Error("Chrome debugger connection failed")), { once: true });
});

let nextID = 1;
const pending = new Map();
const browserExceptions = [];
socket.addEventListener("message", (event) => {
  const message = JSON.parse(String(event.data));
  if (message.method === "Runtime.exceptionThrown") browserExceptions.push(message.params?.exceptionDetails?.exception?.description ?? message.params?.exceptionDetails?.text ?? "unknown browser exception");
  const waiter = pending.get(message.id);
  if (!waiter) return;
  pending.delete(message.id);
  if (message.error) waiter.reject(new Error(`${waiter.method}: ${message.error.message}`));
  else waiter.resolve(message.result);
});
function command(method, params = {}) {
  const id = nextID++;
  socket.send(JSON.stringify({ id, method, params }));
  return new Promise((resolve, reject) => pending.set(id, { resolve, reject, method }));
}
async function evaluate(expression) {
  const result = await command("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });
  if (result.exceptionDetails) throw new Error(result.exceptionDetails.text || "browser evaluation failed");
  return result.result?.value;
}
async function waitFor(expression, label, timeout = 300000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (await evaluate(`Boolean(${expression})`)) return;
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  const body = await evaluate("document.body.innerText");
  const diagnostics = process.env.CREWFOLD_SCREENSHOT_DIR;
  if (diagnostics) {
    fs.mkdirSync(diagnostics, { recursive: true });
    const url = await evaluate("location.href");
    fs.writeFileSync(
      path.join(diagnostics, "failure-body.txt"),
      `URL: ${String(url ?? "")}\n\nBROWSER EXCEPTIONS:\n${browserExceptions.join("\n\n") || "none"}\n\nBODY:\n${String(body ?? "")}`,
    );
    await capture("failure-timeout");
  }
  throw new Error(`timed out waiting for ${label}\n${body}`);
}
async function capture(label) {
  const directory = process.env.CREWFOLD_SCREENSHOT_DIR;
  if (!directory) return;
  fs.mkdirSync(directory, { recursive: true });
  const result = await command("Page.captureScreenshot", { format: "png", captureBeyondViewport: false, fromSurface: true });
  fs.writeFileSync(path.join(directory, `${label}.png`), Buffer.from(result.data, "base64"));
}
async function clickText(selector, text) {
  const found = await evaluate(`(() => {
    const element = [...document.querySelectorAll(${JSON.stringify(selector)})].find((candidate) => candidate.textContent.trim().includes(${JSON.stringify(text)}));
    element?.click();
    return Boolean(element);
  })()`);
  if (!found) throw new Error(`could not find ${selector} containing ${text}`);
}
await command("Runtime.enable");
await command("Page.enable");
await waitFor("document.body?.innerText.includes('Bring your repository into the workbench.')", "onboarding");
await evaluate(`(() => {
  const form = document.querySelector('.onboarding-form');
  const set = (element, value) => {
    const prototype = element instanceof HTMLSelectElement ? HTMLSelectElement.prototype : element instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(prototype, 'value').set.call(element, value);
    element.dispatchEvent(new Event('input', { bubbles: true }));
    element.dispatchEvent(new Event('change', { bubbles: true }));
  };
  const inputs = form.querySelectorAll('input');
  const textareas = form.querySelectorAll('textarea');
  const selects = form.querySelectorAll('select');
  set(inputs[0], ${JSON.stringify(repositoryPath)});
  set(inputs[1], 'personal');
  set(inputs[2], 'm23-live-domain');
  set(selects[0], 'domain-coordinator');
  return true;
})()`);
await evaluate(`(() => {
  const form = document.querySelector('.onboarding-form');
  const set = (element, value) => {
    const prototype = element instanceof HTMLSelectElement ? HTMLSelectElement.prototype : element instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(prototype, 'value').set.call(element, value);
    element.dispatchEvent(new Event('input', { bubbles: true }));
    element.dispatchEvent(new Event('change', { bubbles: true }));
  };
  const inputs = form.querySelectorAll('input');
  const textareas = form.querySelectorAll('textarea');
  const selects = form.querySelectorAll('select');
  set(inputs[3], 'orchid');
  set(inputs[4], 'owner-created coordinator');
  set(textareas[1], 'Coordinate the domain, keep peers informed, and delegate continuing implementation or independent review through exact staffing grants before doing that work yourself. Escalate missing authority and material cross-agent conflicts to the owner.');
  set(selects[1], 'delegation_first');
  set(selects[2], 'codex');
  set(selects[3], 'herdr');
  form.requestSubmit();
  return true;
})()`);
await waitFor("document.querySelector('.m22-console') && document.body.innerText.includes('orchid')", "domain console");

await clickText("button", "add durable agent");
await waitFor("document.querySelector('.m22-agent-create')", "agent creation");
await evaluate(`(() => {
  const form = document.querySelector('.m22-agent-create form');
  const set = (element, value) => {
    const prototype = element instanceof HTMLSelectElement ? HTMLSelectElement.prototype : element instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(prototype, 'value').set.call(element, value);
    element.dispatchEvent(new Event('input', { bubbles: true }));
    element.dispatchEvent(new Event('change', { bubbles: true }));
  };
  const inputs = form.querySelectorAll('input');
  const textareas = form.querySelectorAll('textarea');
  const selects = form.querySelectorAll('select');
  set(inputs[0], 'fern');
  set(inputs[1], 'independent domain peer');
  set(textareas[0], 'Remain an independent durable peer and receive exact coordination messages from other domain agents.');
  set(textareas[1], 'Read canonical domain context, respond to durable peer messages, and report material coordination gaps without taking over unrelated implementation.');
  set(selects[0], 'independent-reviewer');
  set(selects[1], 'hands_on');
  set(selects[2], '');
  set(selects[3], '');
  set(selects[4], 'codex');
  set(selects[5], 'herdr');
  form.requestSubmit();
  return true;
})()`);
await waitFor("[...document.querySelectorAll('.m22-agent-row')].some((candidate) => candidate.textContent.includes('fern'))", "second durable agent");

await clickText(".m22-agent-row", "orchid");
await waitFor("document.querySelector('.m22-agent-center')", "orchid selection");
await clickText(".m22-tabs button", "staffing");
await waitFor("[...document.querySelectorAll('.m22-staffing button')].some((candidate) => candidate.textContent.includes('grant staffing') && !candidate.disabled)", "staffing control");
await clickText(".m22-staffing button", "grant staffing");
await waitFor("document.querySelector('.m22-staffing-form')", "staffing review");
await evaluate("document.querySelector('.m22-staffing-form').requestSubmit(); true");
await waitFor("document.querySelector('.m22-grant') && document.body.innerText.includes('active · up to 4 descendants')", "active staffing grant");

await clickText(".m22-tabs button", "session");
await waitFor("document.body.innerText.includes('start Codex session')", "unbound orchid session");
await clickText("button", "start Codex session");
await waitFor("document.body.innerText.toLowerCase().includes('codex conversation · epoch 1 · codex') && document.querySelector('.m22-composer textarea')", "opened real Codex thread");

const orchidInstruction = `Coordinate this domain end to end according to your operating charter. Submit one inert work proposal for the one attached writable checkout. Describe exactly four new durable agents: m23-implementer (task class implementation), m23-reviewer (task class review), m23-remediator (task class implementation), and m23-verifier (task class verification). Give each proposal-local agent a concise charter limited to its named stage and key each task to its logical assignee. Do not call crewfold_create_durable_child: none of these deliverable-specific agents may exist before acceptance. Do not invent or send grant IDs, checkout IDs or revisions, manager keys, membership/profile references, providers, runtimes, or budgets; Crewfold must resolve those exact authority fields from your current context. Send fern one durable inform message that the exact M23 delivery team and graph have been proposed. Use objective "Deliver the M23 checkout chain" and these four assigned tasks in order:
1. key implement, title "Create the M23 fixture delivery", implementation. Create M23_DELIVERY.txt with a short implementation line, preserve README.md, inspect the diff, and complete through Crewfold with changed_paths and a check so a structured handoff is available.
2. key review, title "Independently review the M23 delivery", review, depending on implement with handoff_with_evidence. Read the predecessor output from the Crewfold briefing, inspect README.md and M23_DELIVERY.txt without editing, and complete with reviewed paths, checks, and an exact handoff.
3. key remediate, title "Apply the M23 review handoff", implementation, depending on review with handoff_with_evidence. Read the reviewer output from the Crewfold briefing, append one remediation acknowledgement to M23_DELIVERY.txt if absent, inspect the diff, and complete with changed paths, checks, and handoff.
4. key verify, title "Verify the M23 checkout chain", verification, depending on remediate with handoff_with_evidence. Read the predecessor output from the Crewfold briefing, verify both delivery lines and the preserved README.md without editing, then complete with reviewed paths, checks, and a final handoff.
Do not use provider-local temporary helpers and do not perform the implementation yourself. Once the fern message and the one pending proposal are confirmed by exact Crewfold receipts, answer exactly LIVE_M23_OK and explain that the four agents and all work remain nonexistent until the owner accepts the displayed graph.`;
await evaluate(`(() => {
  const input = document.querySelector('.m22-composer textarea');
  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(input, ${JSON.stringify(orchidInstruction)});
  input.dispatchEvent(new Event('input', { bubbles: true }));
  return true;
})()`);
await waitFor("!document.querySelector('.m22-composer button').disabled", "enabled orchid send");
await clickText(".m22-composer button", "send");
await waitFor("[...document.querySelectorAll('.m22-thread-item.agentMessage p')].some((item) => item.textContent.trim().startsWith('LIVE_M23_OK'))", "orchid tool-backed response", 600000);
await waitFor("!document.querySelector('.m22-turn-progress') && document.querySelector('.m22-session-state .status-pill')?.textContent.trim().toLowerCase() === 'idle'", "completed orchid provider turn", 600000);
const orchidReplySeen = await evaluate("[...document.querySelectorAll('.m22-thread-item.agentMessage p')].some((item) => item.textContent.trim().startsWith('LIVE_M23_OK'))");
const proposalToolFailed = await evaluate("[...document.querySelectorAll('.m22-thread-item')].some((item) => item.textContent.toLowerCase().includes('submit work proposal') && item.textContent.toLowerCase().includes('failed'))");
if (proposalToolFailed) {
  const proposalFailures = await evaluate("[...document.querySelectorAll('.m22-thread-item')].filter((item) => item.textContent.toLowerCase().includes('submit work proposal') && item.textContent.toLowerCase().includes('failed')).map((item) => item.innerText).join('\\n---\\n')");
  await capture("failure-proposal-tool-retry");
  throw new Error(`the concise work proposal required a failed internal-field retry\n${proposalFailures}`);
}
const inertTeamBeforeAcceptance = await evaluate("document.querySelectorAll('.m22-agent-row').length === 2 && !['m23-implementer', 'm23-reviewer', 'm23-remediator', 'm23-verifier'].some((name) => [...document.querySelectorAll('.m22-agent-row')].some((candidate) => candidate.textContent.includes(name)))");
if (!inertTeamBeforeAcceptance) throw new Error("proposed team became durable before acceptance");
await capture("01-orchid-real-session");

await clickText(".m22-domain-row", "m23-live-domain");
await waitFor("document.querySelector('.m22-work-proposal.pending')?.textContent.includes('Deliver the M23 checkout chain') && document.querySelector('.m22-work-proposal.pending')?.textContent.includes('Proposed team') && document.querySelector('.m22-work-proposals')?.textContent.includes('Conversation alone has changed nothing')", "inert coordinator team and work proposal");
await capture("02-pending-exact-work-graph");
await clickText(".m22-work-proposal button", "accept exact graph");
await waitFor("[...document.querySelectorAll('.m22-domain-home .m22-block h2')].some((heading) => heading.textContent.trim() === 'active workstreams') && !document.querySelector('.m22-work-proposal.pending') && document.querySelectorAll('.m22-agent-row').length >= 6", "atomically accepted team and workstream graph");
const proposalAccepted = true;
await waitFor("[...document.querySelectorAll('.m22-domain-home .m22-line')].some((item) => item.textContent.includes('Deliver the M23 checkout chain') && item.textContent.includes('0 open tasks')) && ![...document.querySelectorAll('.m22-domain-home .m22-block h2')].some((heading) => heading.textContent.trim() === 'needs attention')", "checkout-bound implement-review-remediate-verify completion", 900000);
await evaluate(`(() => { const section = [...document.querySelectorAll('.m22-domain-home .m22-block')].find((candidate) => candidate.querySelector('h2')?.textContent.trim() === 'active workstreams'); const row = [...(section?.querySelectorAll('.m22-line') ?? [])].find((candidate) => candidate.textContent.includes('Deliver the M23 checkout chain')); row?.click(); return Boolean(row); })()`);
const expectedTaskOutcomes = ['completed', 'review passed', 'completed', 'verification passed'];
await waitFor("document.querySelector('.m22-workstream-graph') && JSON.stringify([...document.querySelectorAll('.m22-workstream-graph .m22-line strong')].map((item) => item.textContent.trim())) === JSON.stringify(['Create the M23 fixture delivery', 'Independently review the M23 delivery', 'Apply the M23 review handoff', 'Verify the M23 checkout chain']) && JSON.stringify([...document.querySelectorAll('.m22-workstream-graph .m22-line .status-pill')].map((item) => item.textContent.trim().toLowerCase())) === " + JSON.stringify(JSON.stringify(expectedTaskOutcomes)) + " && document.body.innerText.includes(" + JSON.stringify(repositoryPath) + ") && [...document.querySelectorAll('.m22-workstream-group')].some((group) => group.textContent.includes('Deliver the M23 checkout chain') && ['m23-implementer', 'm23-reviewer', 'm23-remediator', 'm23-verifier'].every((name) => group.textContent.includes(name)))", "completed exact workstream chain with semantic review and verification outcomes");
const workerCompleted = await evaluate("JSON.stringify([...document.querySelectorAll('.m22-workstream-graph .m22-line strong')].map((item) => item.textContent.trim())) === JSON.stringify(['Create the M23 fixture delivery', 'Independently review the M23 delivery', 'Apply the M23 review handoff', 'Verify the M23 checkout chain']) && JSON.stringify([...document.querySelectorAll('.m22-workstream-graph .m22-line .status-pill')].map((item) => item.textContent.trim().toLowerCase())) === " + JSON.stringify(JSON.stringify(expectedTaskOutcomes)) + " && [...document.querySelectorAll('.m22-workstream-group')].some((group) => group.textContent.includes('Deliver the M23 checkout chain') && ['m23-implementer', 'm23-reviewer', 'm23-remediator', 'm23-verifier'].every((name) => group.textContent.includes(name)))");
await waitFor("document.querySelector('.m24-delivery')?.textContent.includes('Verified — awaiting your acceptance') && [...document.querySelectorAll('.m24-delivery button')].some((button) => button.textContent.includes('accept exact delivery') && !button.disabled)", "verified delivery awaiting exact owner acceptance");
await capture("03-verified-awaiting-owner-acceptance");
await clickText(".m24-delivery button", "accept exact delivery");
await waitFor("document.querySelector('.m24-delivery')?.textContent.includes('Accepted by the local owner')", "accepted exact workstream delivery");
const deliveryAccepted = true;
await capture("04-accepted-durable-work");

await evaluate("document.querySelector('.m22-lifecycle-review button[aria-label=\"Close workstream lifecycle\"]').click(); true");
await clickText(".m22-agent-row", "m23-reviewer");
await waitFor("document.querySelector('.m24-agent-records')?.textContent.includes('same durable coworker') && document.querySelector('.m24-agent-records')?.textContent.includes('Independently review the M23 delivery') && document.querySelector('.m24-agent-records')?.textContent.includes('run.completed')", "reviewer task work in unified durable coworker timeline");
await evaluate(`(() => {
  const button = [...document.querySelectorAll('.m24-agent-records button')].find((candidate) => candidate.textContent.includes('Independently review the M23 delivery') && candidate.textContent.includes('run.completed'));
  button?.click();
  return Boolean(button);
})()`);
await waitFor("document.querySelector('.inspector')?.textContent.includes('Independently review the M23 delivery') && document.querySelector('.inspector')?.textContent.includes('Readable agent activity')", "reviewer execution activity inspector");
await capture("05-reviewer-unified-activity");
await evaluate("document.querySelector('.inspector button[aria-label=\"Close inspector\"]')?.click(); true");

await clickText(".m22-agent-row", "fern");
await waitFor("document.body.innerText.includes('start Codex session')", "unbound fern session");
await clickText("button", "start Codex session");
await waitFor("document.querySelector('.m22-composer textarea') && document.body.innerText.toLowerCase().includes('codex conversation · epoch 1 · codex')", "opened fern thread");
const fernInstruction = `Read your canonical Crewfold domain context and confirm your delivered inbox contains the durable coordination message from orchid about proposing the exact M23 delivery team and graph. If and only if it does, answer exactly LIVE_FERN_ACK.`;
await evaluate(`(() => {
  const input = document.querySelector('.m22-composer textarea');
  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(input, ${JSON.stringify(fernInstruction)});
  input.dispatchEvent(new Event('input', { bubbles: true }));
  return true;
})()`);
await waitFor("!document.querySelector('.m22-composer button').disabled", "enabled fern send");
await clickText(".m22-composer button", "send");
await waitFor("[...document.querySelectorAll('.m22-thread-item.agentMessage p')].some((item) => item.textContent.trim() === 'LIVE_FERN_ACK')", "fern inbox acknowledgement");
const fernReplySeen = await evaluate("[...document.querySelectorAll('.m22-thread-item.agentMessage p')].some((item) => item.textContent.trim() === 'LIVE_FERN_ACK')");
await capture("06-fern-real-session");

await command("Page.reload", { ignoreCache: true });
await waitFor("document.querySelector('.m22-console') && document.querySelectorAll('.m22-agent-row').length >= 6", "canonical tree after browser reload");
await clickText(".m22-agent-row", "orchid");
await waitFor("document.body.innerText.includes('LIVE_M23_OK')", "orchid conversation after reload");
await capture("07-reloaded-session");

await evaluate("document.querySelector('.m22-session-lifecycle summary').click(); true");
await waitFor("[...document.querySelectorAll('.m22-session-lifecycle-actions button')].some((button) => button.textContent.includes('compact and recycle host') && !button.disabled)", "idle native compaction control");
await clickText(".m22-session-lifecycle-actions button", "compact and recycle host");
await waitFor("[...document.querySelectorAll('.m22-session-lifecycle-actions button')].some((button) => button.textContent.includes('compact and recycle host') && !button.disabled) && document.body.innerText.includes('epoch 1 current')", "native Codex compaction and host recycle");
await clickText(".m22-session-lifecycle-actions button", "hand off to fresh epoch");
await waitFor("document.body.innerText.toLowerCase().includes('codex conversation · epoch 2 · codex') && document.body.innerText.includes('epoch 1 archived')", "fresh canonical handoff epoch");
await clickText(".m22-session-epochs button", "epoch 1");
await waitFor("document.body.innerText.includes('This epoch is immutable history') && document.body.innerText.includes('LIVE_M23_OK')", "readable inert archived epoch");
const epochLineage = true;
await capture("08-archived-epoch");

const result = await evaluate(`(() => ({
  orchidReply: ${JSON.stringify(orchidReplySeen)},
  inertTeamBeforeAcceptance: ${JSON.stringify(inertTeamBeforeAcceptance)},
  fernReply: ${JSON.stringify(fernReplySeen)},
  childVisible: ['m23-implementer', 'm23-reviewer', 'm23-remediator', 'm23-verifier'].every((name) => [...document.querySelectorAll('.m22-agent-row')].some((candidate) => candidate.textContent.includes(name))),
  proposalAccepted: ${JSON.stringify(proposalAccepted)},
  deliveryAccepted: ${JSON.stringify(deliveryAccepted)},
  workerCompleted: ${JSON.stringify(workerCompleted)},
  legacyExecutive: document.body.innerText.includes('project-executive'),
  leakedPrivateBinding: ['thread_id', 'node_fingerprint', 'runtime_handle', 'provider_handle'].some((value) => document.documentElement.innerHTML.includes(value)),
  providerLocalHelper: document.querySelector('.m22-thread-item.collabAgentToolCall, .m22-thread-item.subAgentActivity') !== null,
  proposalToolFailed: ${JSON.stringify(proposalToolFailed)},
  epochLineage: ${JSON.stringify(epochLineage)},
}))()`);
result.browserExceptions = browserExceptions;
if (!result.orchidReply || !result.fernReply || !result.inertTeamBeforeAcceptance || !result.childVisible || !result.proposalAccepted || !result.deliveryAccepted || !result.workerCompleted || !result.epochLineage || result.legacyExecutive || result.leakedPrivateBinding || result.providerLocalHelper || result.proposalToolFailed || browserExceptions.length) {
  throw new Error(`live M23 invariant failed: ${JSON.stringify(result)}`);
}
fs.writeFileSync(outputPath, JSON.stringify(result, null, 2));
socket.close();
