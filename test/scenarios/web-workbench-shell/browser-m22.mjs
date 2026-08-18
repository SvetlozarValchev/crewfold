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
const failedResources = [];
socket.addEventListener("message", (event) => {
  const message = JSON.parse(String(event.data));
  if (message.method === "Runtime.exceptionThrown") browserExceptions.push(message.params?.exceptionDetails?.exception?.description ?? message.params?.exceptionDetails?.text ?? "unknown browser exception");
  if (message.method === "Network.responseReceived" && message.params?.response?.status >= 400) failedResources.push(`${message.params.response.status} ${message.params.response.url}`);
  if (message.method === "Network.loadingFailed" && !message.params?.canceled && message.params?.errorText !== "net::ERR_ABORTED") failedResources.push(`${message.params?.errorText ?? "load failed"} ${message.params?.requestId ?? "unknown request"}`);
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
async function clickUntil(buttonExpression, targetExpression, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (await evaluate(`Boolean(${targetExpression})`)) return;
    await evaluate(`(() => { const button = ${buttonExpression}; if (button && !button.disabled) button.click(); return Boolean(button); })()`);
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  throw new Error(`timed out activating ${label}\n${await evaluate("document.body.innerText")}`);
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
await command("Network.enable");
await command("Page.reload", { ignoreCache: true });
await waitFor("document.body?.innerText.includes('Bring your repository into the workbench.')", "domain onboarding");
await waitFor("document.body.innerText.includes('First durable agent') && document.body.innerText.includes('Owner-reviewed operating charter')", "reviewed first-agent fields");
for (const [key, fragment] of [
  ["domain-coordinator", "Coordinate the shared domain"],
  ["workstream-coordinator", "Own one bounded workstream outcome"],
  ["implementer", "Implement bounded assigned work"],
  ["independent-reviewer", "Independently review assigned changes"],
  ["verification-qa", "Verify assigned outcomes"],
  ["knowledge-maintainer", "Maintain shared domain knowledge"],
  ["integration-release", "Coordinate cross-repository interfaces"],
]) {
  await evaluate(`(() => {
    const template = document.querySelector('.onboarding-form .m22-agent-template select');
    Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set.call(template, ${JSON.stringify(key)});
    template.dispatchEvent(new Event('change', { bubbles: true }));
    return true;
  })()`);
  await waitFor(`document.querySelector('.onboarding-form textarea').value.includes(${JSON.stringify(fragment)})`, `${key} ownership template prefill`);
}
await evaluate(`(() => {
  const template = document.querySelector('.onboarding-form .m22-agent-template select');
  Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set.call(template, 'domain-coordinator');
  template.dispatchEvent(new Event('change', { bubbles: true }));
  return true;
})()`);
await waitFor("document.querySelector('.onboarding-form textarea').value.includes('Coordinate the shared domain')", "editable ownership template prefill");
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
  set(selects[0], 'domain-coordinator');
  set(textareas[0], 'Coordinate this domain and delegate durable review work when staffing authority is available.');
  set(textareas[1], 'Maintain the domain overview, communicate material boundaries, and delegate continuing specialist work through exact reviewed staffing grants.');
  set(selects[1], 'delegation_first');
  set(selects[2], 'fixture-mcp');
  set(selects[3], 'direct');
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
await waitFor("document.querySelector('.m22-workstream-create') && document.body.innerText.includes('Create a workstream')", "canonical workstream creation");
await capture("03-workstream-create");
await evaluate(`(() => {
  const form = document.querySelector('.m22-workstream-create form');
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
await waitFor("document.body.innerText.includes('was created. It is empty') && [...document.querySelectorAll('.m22-workstream-group')].some((group) => group.textContent.includes('Independent review') && group.textContent.includes('no agents'))", "visible empty workstream confirmation");
await capture("04-workstream-created");
await evaluate(`(() => {
  const button = [...document.querySelectorAll('button')].find((candidate) => candidate.textContent.includes('new workstream'));
  button?.click();
  return Boolean(button);
})()`);
await waitFor("document.querySelector('.m22-workstream-create')", "second workstream review");
await evaluate(`(() => {
  const input = document.querySelector('.m22-workstream-create input');
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(input, 'Independent review');
  input.dispatchEvent(new Event('input', { bubbles: true }));
  document.querySelector('.m22-workstream-create form').requestSubmit();
  return true;
})()`);
await waitFor("document.querySelector('.m22-workstream-create [role=alert]')?.textContent.includes('already exists')", "duplicate workstream refusal");
await evaluate("document.querySelector('.m22-workstream-create button[aria-label=\"Close workstream creation\"]').click(); true");

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
  set(selects[0], 'independent-reviewer');
  set(selects[1], 'hands_on');
  set(selects[2], '');
  set(selects[3], selects[3].options[1].value);
  set(selects[4], 'fixture-mcp');
  set(selects[5], 'direct');
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
await clickUntil("document.querySelector('.m22-placement .m22-command')", "document.querySelector('.m22-placement form')", "existing agent placement editor");
await evaluate(`(() => {
  const form = document.querySelector('.m22-placement form');
  const workstream = form.querySelectorAll('select')[1];
  Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, 'value').set.call(workstream, workstream.options[1].value);
  workstream.dispatchEvent(new Event('change', { bubbles: true }));
  form.requestSubmit();
  return true;
})()`);
await waitFor("document.body.innerText.includes('Placement updated in the canonical hierarchy.') && [...document.querySelectorAll('.m22-workstream-group')].some((group) => group.textContent.includes('Independent review') && group.textContent.includes('orchid'))", "existing agent grouped into workstream");
await capture("04-agent-session");

await evaluate(`(() => {
  const button = [...document.querySelectorAll('.m22-tabs button')].find((candidate) => candidate.textContent.trim() === 'staffing');
  button?.click();
  return Boolean(button);
})()`);
await waitFor("document.body.innerText.includes('This agent cannot create durable children')", "empty explicit staffing authority");
await waitFor("[...document.querySelectorAll('.m22-staffing button')].some((candidate) => candidate.textContent.includes('grant staffing') && !candidate.disabled)", "mutable staffing control");
await clickUntil("[...document.querySelectorAll('.m22-staffing button')].find((candidate) => candidate.textContent.includes('grant staffing'))", "document.querySelector('.m22-staffing-form') && document.body.innerText.includes('Exact effect')", "staffing grant review form");
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
if (failedResources.length) throw new Error(`failed browser resources: ${failedResources.join(" | ")}`);
fs.writeFileSync(outputPath, JSON.stringify(result, null, 2));
socket.close();
