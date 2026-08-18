import fs from "node:fs";
import path from "node:path";

const [, , debuggerPort, repositoryPath, outputPath] = process.argv;
if (!debuggerPort || !repositoryPath || !outputPath) throw new Error("usage: browser-m22.mjs <debugger-port> <repository-path> <output-path>");

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
  if (message.error) waiter.reject(new Error(message.error.message));
  else waiter.resolve(message.result);
});
function command(method, params = {}) {
  const id = nextID++;
  socket.send(JSON.stringify({ id, method, params }));
  return new Promise((resolve, reject) => pending.set(id, { resolve, reject }));
}
async function evaluate(expression) {
  let result;
  try {
    result = await command("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });
  } catch (error) {
    throw new Error(`browser evaluation failed for ${expression.slice(0, 160)}: ${error.message}`);
  }
  if (result.exceptionDetails) throw new Error(result.exceptionDetails.text || "browser evaluation failed");
  return result.result?.value;
}
async function waitFor(expression, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (await evaluate(`Boolean(${expression})`)) return;
    await new Promise((resolve) => setTimeout(resolve, 50));
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

await command("Runtime.enable");
await command("Page.enable");
await waitFor("document.body?.innerText.includes('Bring your repository into the workbench.')", "domain onboarding");
await waitFor("document.body.innerText.includes('First durable agent') && document.body.innerText.includes('Owner-reviewed operating charter')", "reviewed first-agent fields");
await capture("01-domain-onboarding");

await evaluate(`(() => {
  const set = (element, value) => {
    const prototype = element instanceof HTMLSelectElement ? HTMLSelectElement.prototype : element instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(prototype, 'value').set.call(element, value);
    element.dispatchEvent(new Event('input', { bubbles: true }));
    element.dispatchEvent(new Event('change', { bubbles: true }));
  };
  const form = document.querySelector('.onboarding-form');
  const inputs = form.querySelectorAll('input');
  const textareas = form.querySelectorAll('textarea');
  const selects = form.querySelectorAll('select');
  set(inputs[0], ${JSON.stringify(repositoryPath)});
  set(inputs[1], 'personal');
  set(inputs[2], 'world-engine');
  set(inputs[3], 'orchid');
  set(inputs[4], 'owner-facing coordinator');
  set(textareas[0], 'Coordinate this domain and delegate durable review work when staffing authority is available.');
  set(textareas[1], 'Maintain the domain overview, communicate material boundaries, and delegate continuing specialist work through exact reviewed staffing grants.');
  set(selects[0], 'delegation_first');
  set(selects[1], 'fixture-mcp');
  set(selects[2], 'direct');
  form.requestSubmit();
  return true;
})()`);
await waitFor("document.querySelector('.m22-console') && document.body.innerText.includes('world-engine')", "domain console after onboarding");
await waitFor("document.body.innerText.includes('orchid') && document.body.innerText.includes('owner-facing coordinator')", "canonical arbitrary agent");
if (await evaluate("document.body.innerText.includes('project-executive')")) throw new Error("legacy hardcoded project-executive leaked into M22 onboarding");
await capture("02-domain-home");

await evaluate(`(() => {
  const button = [...document.querySelectorAll('button')].find((candidate) => candidate.textContent.includes('new workstream'));
  button?.click();
  return Boolean(button);
})()`);
await waitFor("document.querySelector('.m22-rail-create')", "canonical workstream creation");
await evaluate(`(() => {
  const form = document.querySelector('.m22-rail-create');
  const set = (element, value) => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(element, value);
    element.dispatchEvent(new Event('input', { bubbles: true }));
  };
  const inputs = form.querySelectorAll('input');
  set(inputs[0], 'Independent review');
  form.requestSubmit();
  return true;
})()`);
await waitFor("document.body.innerText.includes('Independent review')", "canonical workstream");

await evaluate(`(() => {
  const button = [...document.querySelectorAll('button')].find((candidate) => candidate.textContent.includes('add durable agent'));
  button?.click();
  return Boolean(button);
})()`);
await waitFor("document.querySelector('.m22-agent-create') && document.body.innerText.includes('Create a durable agent')", "owner durable-agent creation");
await capture("03-agent-create");
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
  set(inputs[0], 'moss-reviewer');
  set(inputs[1], 'independent review');
  set(textareas[0], 'Independently review this domain workstream and report exact findings.');
  set(textareas[1], 'Review assigned changes independently, preserve evidence, and escalate material defects without taking over implementation.');
  set(selects[0], 'hands_on');
  set(selects[1], '');
  set(selects[2], selects[2].options[1].value);
  set(selects[3], 'fixture-mcp');
  set(selects[4], 'direct');
  set(inputs[2], '1');
  form.requestSubmit();
  return true;
})()`);
await waitFor("[...document.querySelectorAll('.m22-agent-row')].some((candidate) => candidate.textContent.includes('moss-reviewer'))", "atomic child attached to domain tree");
await waitFor("[...document.querySelectorAll('.m22-workstream-group h3')].some((candidate) => candidate.textContent.includes('Independent review'))", "objective-backed hierarchy grouping");

await evaluate(`(() => {
  const button = [...document.querySelectorAll('.m22-agent-row')].find((candidate) => candidate.textContent.includes('orchid'));
  button?.click();
  return Boolean(button);
})()`);
await waitFor("document.querySelector('.m22-agent-center') && document.body.innerText.includes('Start this durable agent') && document.body.innerText.includes('start Codex session')", "honest unbound durable session");
await capture("04-agent-session");

await evaluate(`(() => {
  const button = [...document.querySelectorAll('.m22-tabs button')].find((candidate) => candidate.textContent.trim() === 'staffing');
  button?.click();
  return Boolean(button);
})()`);
await waitFor("document.body.innerText.includes('This agent cannot create durable children')", "empty explicit staffing authority");
await waitFor("[...document.querySelectorAll('.m22-staffing button')].some((candidate) => candidate.textContent.includes('grant staffing') && !candidate.disabled)", "mutable staffing control");
await evaluate(`(() => {
  const button = [...document.querySelectorAll('.m22-staffing button')].find((candidate) => candidate.textContent.includes('grant staffing'));
  button?.click();
  return Boolean(button);
})()`);
await waitFor("document.querySelector('.m22-staffing-form') && document.body.innerText.includes('Exact effect')", "staffing grant review form");
await capture("05-staffing-review");
await evaluate("document.querySelector('.m22-staffing-form').requestSubmit(); true");
await waitFor("document.querySelector('.m22-grant') && document.body.innerText.includes('active · up to 4 descendants')", "active exact staffing grant");
await capture("06-staffing-active");

await evaluate(`(() => {
  const button = [...document.querySelectorAll('.m22-tabs button')].find((candidate) => candidate.textContent.trim() === 'assignment');
  button?.click();
  return Boolean(button);
})()`);
await waitFor("document.body.innerText.includes('No task is assigned to this agent.')", "empty exact assignment");
await capture("07-agent-assignment");

await command("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
await evaluate("window.scrollTo(0, 0)");
await waitFor("getComputedStyle(document.querySelector('.m22-context')).display === 'none'", "narrow context rail collapse");
await capture("08-narrow-console");

const result = await evaluate(`(() => ({
  title: document.title,
  iconCount: document.querySelectorAll('svg.lucide').length,
  domains: document.querySelectorAll('.m22-domain-row').length,
  durableAgents: document.querySelectorAll('.m22-agent-row').length,
  unnamedButtons: [...document.querySelectorAll('button')].filter((button) => !button.getAttribute('aria-label') && !button.textContent.trim()).length,
  unlabelledControls: [...document.querySelectorAll('input, textarea, select')].filter((control) => !control.getAttribute('aria-label') && !control.closest('label')).length,
  leakedHandle: ['runtime_handle', 'provider_handle', 'node.key', 'capability/', 'capabilities/'].some((value) => document.documentElement.innerHTML.includes(value)),
  legacyExecutive: document.body.innerText.includes('project-executive'),
  completedBrowserActions: ['domain-onboarding', 'domain-home', 'durable-agent-selection', 'literal-assignment-view', 'narrow-layout'],
}))()`);
if (result.title !== "Crewfold Workbench") throw new Error(`unexpected title ${result.title}`);
if (result.domains !== 1 || result.durableAgents !== 2) throw new Error(`unexpected domain console cardinality: ${JSON.stringify(result)}`);
if (result.unnamedButtons || result.unlabelledControls || result.leakedHandle || result.legacyExecutive) throw new Error(`M22 browser invariant failed: ${JSON.stringify(result)}`);
if (browserExceptions.length) throw new Error(`browser exceptions: ${browserExceptions.join(" | ")}`);
fs.writeFileSync(outputPath, JSON.stringify(result, null, 2));
socket.close();
