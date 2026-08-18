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
  throw new Error(`timed out waiting for ${label}\n${await evaluate("document.body.innerText")}`);
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
    const prototype = element instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(prototype, 'value').set.call(element, value);
    element.dispatchEvent(new Event('input', { bubbles: true }));
    element.dispatchEvent(new Event('change', { bubbles: true }));
  };
  const inputs = form.querySelectorAll('input');
  const selects = form.querySelectorAll('select');
  set(inputs[0], ${JSON.stringify(repositoryPath)});
  set(inputs[1], 'personal');
  set(inputs[2], 'm22-live-domain');
  set(inputs[3], 'orchid');
  set(inputs[4], 'owner-created coordinator');
  set(selects[0], 'codex');
  set(selects[1], 'herdr');
  form.requestSubmit();
  return true;
})()`);
await waitFor("document.querySelector('.m22-console') && document.body.innerText.includes('orchid')", "domain console");

await clickText("button", "add durable agent");
await waitFor("document.querySelector('.m22-agent-create')", "agent creation");
await evaluate(`(() => {
  const form = document.querySelector('.m22-agent-create form');
  const set = (element, value) => {
    const prototype = element instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(prototype, 'value').set.call(element, value);
    element.dispatchEvent(new Event('input', { bubbles: true }));
    element.dispatchEvent(new Event('change', { bubbles: true }));
  };
  const inputs = form.querySelectorAll('input');
  const selects = form.querySelectorAll('select');
  set(inputs[0], 'fern');
  set(inputs[1], 'independent domain peer');
  set(selects[0], '');
  set(selects[2], 'codex');
  set(selects[3], 'herdr');
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
await waitFor("document.body.innerText.toLowerCase().includes('codex conversation · codex') && document.querySelector('.m22-composer textarea')", "opened real Codex thread");

const orchidInstruction = `Read your canonical Crewfold domain context. From it find the active staffing grant and the durable agent named fern. Send fern a durable inform message with subject "live M22" and body "orchid-live-message". Then use that active grant to create a continuing durable child named moss-live with role independent-reviewer, provider codex, runtime herdr, max concurrency 1, task class review, and budget token limit 1000, cost cents 0, time seconds 600. After both Crewfold operations succeed, answer exactly LIVE_M22_OK.`;
await evaluate(`(() => {
  const input = document.querySelector('.m22-composer textarea');
  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(input, ${JSON.stringify(orchidInstruction)});
  input.dispatchEvent(new Event('input', { bubbles: true }));
  return true;
})()`);
await waitFor("!document.querySelector('.m22-composer button').disabled", "enabled orchid send");
await clickText(".m22-composer button", "send");
await waitFor("[...document.querySelectorAll('.m22-thread-item.agentMessage p')].some((item) => item.textContent.trim() === 'LIVE_M22_OK')", "orchid tool-backed response");
await waitFor("[...document.querySelectorAll('.m22-agent-row')].some((candidate) => candidate.textContent.includes('moss-live'))", "provider-created durable child");
await capture("01-orchid-real-session");

await clickText(".m22-agent-row", "fern");
await waitFor("document.body.innerText.includes('start Codex session')", "unbound fern session");
await clickText("button", "start Codex session");
await waitFor("document.querySelector('.m22-composer textarea') && document.body.innerText.toLowerCase().includes('codex conversation · codex')", "opened fern thread");
const fernInstruction = `Read your canonical Crewfold domain context and confirm your delivered inbox contains the exact body "orchid-live-message" from orchid. If and only if it does, answer exactly LIVE_FERN_ACK.`;
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
await capture("02-fern-real-session");

await command("Page.reload", { ignoreCache: true });
await waitFor("document.querySelector('.m22-console') && document.body.innerText.includes('moss-live')", "canonical tree after browser reload");
await clickText(".m22-agent-row", "orchid");
await waitFor("document.body.innerText.includes('LIVE_M22_OK')", "orchid conversation after reload");
await capture("03-reloaded-session");

const result = await evaluate(`(() => ({
  orchidReply: [...document.querySelectorAll('.m22-thread-item.agentMessage p')].some((item) => item.textContent.trim() === 'LIVE_M22_OK'),
  fernReply: ${JSON.stringify(fernReplySeen)},
  childVisible: [...document.querySelectorAll('.m22-agent-row')].some((candidate) => candidate.textContent.includes('moss-live')),
  legacyExecutive: document.body.innerText.includes('project-executive'),
  leakedPrivateBinding: ['thread_id', 'node_fingerprint', 'runtime_handle', 'provider_handle'].some((value) => document.documentElement.innerHTML.includes(value)),
}))()`);
result.browserExceptions = browserExceptions;
if (!result.orchidReply || !result.fernReply || !result.childVisible || result.legacyExecutive || result.leakedPrivateBinding || browserExceptions.length) {
  throw new Error(`live M22 invariant failed: ${JSON.stringify(result)}`);
}
fs.writeFileSync(outputPath, JSON.stringify(result, null, 2));
socket.close();
