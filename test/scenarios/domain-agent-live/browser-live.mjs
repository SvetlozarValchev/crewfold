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
    fs.writeFileSync(path.join(diagnostics, "failure-body.txt"), String(body ?? ""));
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
  set(inputs[2], 'm22-live-domain');
  set(selects[0], 'domain-coordinator');
  return true;
})()`);
await clickText("button", "Draft reviewed specification");
await waitFor("document.querySelectorAll('.onboarding-form input')[3].value.length > 0 && document.querySelectorAll('.onboarding-form textarea')[1].value.length > 0", "real ephemeral Codex specification draft");
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

const orchidInstruction = `Coordinate this domain end to end according to your operating charter. Use your current Crewfold staffing grant to create exactly one continuing durable child named m22-reviewer with the descriptive role "independent fixture reviewer", hands_on policy, Codex through Herdr, task class review, one concurrent run, the attached checkout, and a bounded charter to inspect the fixture repository read-only and report exact evidence. Send fern one durable inform message that independent review has been staffed. Then submit one inert Crewfold work proposal under your current grant: objective "Verify the M22 fixture", one task with key review, title "Review the M22 fixture", task class review, priority 100, the m22-reviewer launch profile, no dependencies, and a description requiring it to read README.md without changing repository files, report the exact heading as evidence, and complete through Crewfold. Use sensible bounded token/time budgets within your grant. Do not use provider-local temporary helpers. Once the child, message, and pending proposal are all confirmed by exact Crewfold tool receipts, answer exactly LIVE_M22_OK and explain that the owner must accept the displayed graph before anything runs.`;
await evaluate(`(() => {
  const input = document.querySelector('.m22-composer textarea');
  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(input, ${JSON.stringify(orchidInstruction)});
  input.dispatchEvent(new Event('input', { bubbles: true }));
  return true;
})()`);
await waitFor("!document.querySelector('.m22-composer button').disabled", "enabled orchid send");
await clickText(".m22-composer button", "send");
await waitFor("[...document.querySelectorAll('.m22-thread-item.agentMessage p')].some((item) => item.textContent.trim().startsWith('LIVE_M22_OK'))", "orchid tool-backed response");
await waitFor("document.querySelectorAll('.m22-agent-row').length >= 3", "charter-driven durable child delegation");
const orchidReplySeen = await evaluate("[...document.querySelectorAll('.m22-thread-item.agentMessage p')].some((item) => item.textContent.trim().startsWith('LIVE_M22_OK'))");
await capture("01-orchid-real-session");

await clickText(".m22-domain-row", "m22-live-domain");
await waitFor("document.querySelector('.m22-work-proposal.pending')?.textContent.includes('Verify the M22 fixture') && document.querySelector('.m22-work-proposals')?.textContent.includes('Conversation alone has changed nothing')", "inert coordinator work proposal");
await capture("02-pending-exact-work-graph");
await clickText(".m22-work-proposal button", "accept exact graph");
await waitFor("[...document.querySelectorAll('.m22-domain-home .m22-block h2')].some((heading) => heading.textContent.trim() === 'active workstreams') && !document.querySelector('.m22-work-proposal.pending')", "accepted workstream graph");
const proposalAccepted = true;
await clickText(".m22-agent-row", "m22-reviewer");
await waitFor("document.querySelector('.m22-agent-center h1')?.textContent.trim() === 'm22-reviewer'", "durable reviewer selection");
await clickText(".m22-tabs button", "assignment");
await waitFor("document.querySelector('.m22-agent-center')?.textContent.includes('No canonical task is assigned to this agent.')", "durable reviewer terminalization");
await clickText(".m22-domain-row", "m22-live-domain");
await waitFor("[...document.querySelectorAll('.m22-domain-home .m22-line')].some((item) => item.textContent.includes('Verify the M22 fixture') && item.textContent.includes('0 open tasks')) && ![...document.querySelectorAll('.m22-domain-home .m22-block h2')].some((heading) => heading.textContent.trim() === 'needs attention')", "dependency-free durable reviewer completion");
const workerCompleted = await evaluate("[...document.querySelectorAll('.m22-domain-home .m22-line')].some((item) => item.textContent.includes('Verify the M22 fixture') && item.textContent.includes('0 open tasks')) && ![...document.querySelectorAll('.m22-domain-home .m22-block h2')].some((heading) => heading.textContent.trim() === 'needs attention')");
await capture("03-completed-durable-work");

await clickText(".m22-agent-row", "fern");
await waitFor("document.body.innerText.includes('start Codex session')", "unbound fern session");
await clickText("button", "start Codex session");
await waitFor("document.querySelector('.m22-composer textarea') && document.body.innerText.toLowerCase().includes('codex conversation · epoch 1 · codex')", "opened fern thread");
const fernInstruction = `Read your canonical Crewfold domain context and confirm your delivered inbox contains the durable coordination message from orchid about staffing independent review. If and only if it does, answer exactly LIVE_FERN_ACK.`;
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
await capture("04-fern-real-session");

await command("Page.reload", { ignoreCache: true });
await waitFor("document.querySelector('.m22-console') && document.querySelectorAll('.m22-agent-row').length >= 3", "canonical tree after browser reload");
await clickText(".m22-agent-row", "orchid");
await waitFor("document.body.innerText.includes('LIVE_M22_OK')", "orchid conversation after reload");
await capture("05-reloaded-session");

await evaluate("document.querySelector('.m22-session-lifecycle summary').click(); true");
await waitFor("[...document.querySelectorAll('.m22-session-lifecycle-actions button')].some((button) => button.textContent.includes('compact and recycle host') && !button.disabled)", "idle native compaction control");
await clickText(".m22-session-lifecycle-actions button", "compact and recycle host");
await waitFor("[...document.querySelectorAll('.m22-session-lifecycle-actions button')].some((button) => button.textContent.includes('compact and recycle host') && !button.disabled) && document.body.innerText.includes('epoch 1 current')", "native Codex compaction and host recycle");
await clickText(".m22-session-lifecycle-actions button", "hand off to fresh epoch");
await waitFor("document.body.innerText.toLowerCase().includes('codex conversation · epoch 2 · codex') && document.body.innerText.includes('epoch 1 archived')", "fresh canonical handoff epoch");
await clickText(".m22-session-epochs button", "epoch 1");
await waitFor("document.body.innerText.includes('This epoch is immutable history') && document.body.innerText.includes('LIVE_M22_OK')", "readable inert archived epoch");
const epochLineage = true;
await capture("06-archived-epoch");

const result = await evaluate(`(() => ({
  orchidReply: ${JSON.stringify(orchidReplySeen)},
  fernReply: ${JSON.stringify(fernReplySeen)},
  childVisible: [...document.querySelectorAll('.m22-agent-row')].some((candidate) => candidate.textContent.includes('m22-reviewer')),
  proposalAccepted: ${JSON.stringify(proposalAccepted)},
  workerCompleted: ${JSON.stringify(workerCompleted)},
  legacyExecutive: document.body.innerText.includes('project-executive'),
  leakedPrivateBinding: ['thread_id', 'node_fingerprint', 'runtime_handle', 'provider_handle'].some((value) => document.documentElement.innerHTML.includes(value)),
  providerLocalHelper: document.querySelector('.m22-thread-item.collabAgentToolCall, .m22-thread-item.subAgentActivity') !== null,
  epochLineage: ${JSON.stringify(epochLineage)},
}))()`);
result.browserExceptions = browserExceptions;
if (!result.orchidReply || !result.fernReply || !result.childVisible || !result.proposalAccepted || !result.workerCompleted || !result.epochLineage || result.legacyExecutive || result.leakedPrivateBinding || result.providerLocalHelper || browserExceptions.length) {
  throw new Error(`live M22 invariant failed: ${JSON.stringify(result)}`);
}
fs.writeFileSync(outputPath, JSON.stringify(result, null, 2));
socket.close();
