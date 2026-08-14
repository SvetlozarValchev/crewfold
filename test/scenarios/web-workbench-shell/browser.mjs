import fs from "node:fs";

const [, , debuggerPort, repositoryPath, outputPath] = process.argv;
if (!debuggerPort || !repositoryPath || !outputPath) throw new Error("usage: browser.mjs <debugger-port> <repository-path> <output-path>");

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
socket.addEventListener("message", (event) => {
  const message = JSON.parse(String(event.data));
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
  const result = await command("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true });
  if (result.exceptionDetails) throw new Error(result.exceptionDetails.text || "browser evaluation failed");
  return result.result?.value;
}

async function waitFor(expression, label, timeout = 15000) {
  const deadline = Date.now() + timeout;
  while (Date.now() < deadline) {
    if (await evaluate(expression)) return;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  const body = await evaluate("document.body.innerText");
  throw new Error(`timed out waiting for ${label}\n${body}`);
}

await command("Runtime.enable");
await waitFor("document.body.innerText.includes('Bring your repository into the workbench.')", "onboarding");
await waitFor("document.body.innerText.includes('Herdr interactive runtime') && document.querySelector('.advanced-runtime select')?.value === 'herdr'", "Herdr-first onboarding default");

const onboarding = `(() => {
  const set = (element, value) => {
    const prototype = element instanceof HTMLSelectElement ? HTMLSelectElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(prototype, 'value').set.call(element, value);
    element.dispatchEvent(new Event('input', { bubbles: true }));
    element.dispatchEvent(new Event('change', { bubbles: true }));
  };
  const form = document.querySelector('.onboarding-form');
  const inputs = form.querySelectorAll('input');
  const selects = form.querySelectorAll('select');
  set(inputs[0], ${JSON.stringify(repositoryPath)});
  set(inputs[1], 'personal');
  set(inputs[2], 'world-engine-2');
  set(inputs[3], 'builder');
  set(selects[0], 'fixture-mcp');
  set(selects[1], 'direct');
  form.requestSubmit();
  return true;
})()`;
await evaluate(onboarding);
await waitFor("document.body.innerText.includes('What should the crew accomplish?')", "workbench after onboarding");

await evaluate(`(() => {
  const area = document.querySelector('textarea[aria-label="Owner instruction"]');
  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(area, 'Build the first playable world loop and report exact evidence');
  area.dispatchEvent(new Event('input', { bubbles: true }));
  area.dispatchEvent(new Event('change', { bubbles: true }));
  area.form.requestSubmit();
  return true;
})()`);
await waitFor("document.body.innerText.includes('Committed four receipted effects and requested the selected agent launch')", "receipted owner act", 20000);
await waitFor("document.body.innerText.includes('builder')", "created crew");

await evaluate(`(() => {
  [...document.querySelectorAll('.intent-mode button')].find((button) => button.textContent.trim() === 'plan').click();
  const area = document.querySelector('textarea[aria-label="Owner instruction"]');
  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(area, 'Build a deterministic successor level');
  area.dispatchEvent(new Event('input', { bubbles: true }));
  area.dispatchEvent(new Event('change', { bubbles: true }));
  area.form.requestSubmit();
  return true;
})()`);
await waitFor("document.body.innerText.includes('Edit task, agent, and budget')", "editable frozen plan");
await waitFor(`(() => {
  if (document.querySelector('.plan-editor')) return true;
  const button = [...document.querySelectorAll('button')].find((candidate) => candidate.textContent.includes('Edit task, agent, and budget') && !candidate.disabled);
  if (button) button.click();
  return false;
})()`, "plan editor");
await evaluate(`(() => {
  const priority = document.querySelector('.plan-editor input[type="number"]');
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(priority, '720');
  priority.dispatchEvent(new Event('input', { bubbles: true }));
  priority.dispatchEvent(new Event('change', { bubbles: true }));
  [...document.querySelectorAll('button')].find((button) => button.textContent.includes('Seal reviewed revision')).click();
  return true;
})()`);
await waitFor("document.body.innerText.includes('Sealed reviewed plan revision 2')", "sealed edited plan");
await evaluate(`(() => { [...document.querySelectorAll('button')].find((button) => button.textContent.includes('Execute reviewed plan')).click(); return true; })()`);
await waitFor("document.body.innerText.includes('Committed the objective, created and assigned its first task')", "edited plan execution", 20000);

await evaluate(`(() => { [...document.querySelectorAll('nav button')].find((button) => button.textContent.includes('Crew')).click(); return true; })()`);
await waitFor("document.body.innerText.includes('Inspect agent')", "crew inspector action");
await evaluate(`(() => { [...document.querySelectorAll('button')].find((button) => button.textContent.includes('Inspect agent')).click(); return true; })()`);
await waitFor("document.body.innerText.includes('REPOSITORY OBSERVATION')", "agent repository observation");

await evaluate(`(() => { document.querySelector('button[aria-label="Close inspector"]').click(); [...document.querySelectorAll('nav button')].find((button) => button.textContent.includes('Briefing')).click(); return true; })()`);
await waitFor("document.body.innerText.includes('Project briefing')", "briefing view");
await evaluate(`(() => { [...document.querySelectorAll('button')].find((button) => button.textContent.includes('Generate briefing')).click(); return true; })()`);
await waitFor("document.body.innerText.includes('Event cut #')", "bounded project briefing", 20000);

const result = await evaluate(`(() => ({
  title: document.title,
  body: document.body.innerText,
  iconCount: document.querySelectorAll('svg.lucide').length,
  unnamedButtons: [...document.querySelectorAll('button')].filter((button) => !button.getAttribute('aria-label') && !button.textContent.trim()).length,
  unlabelledControls: [...document.querySelectorAll('input, textarea, select')].filter((control) => !control.getAttribute('aria-label') && !control.closest('label')).length,
  leakedHandle: ['runtime_handle', 'provider_handle', 'node.key', 'capability/', 'capabilities/'].some((value) => document.documentElement.innerHTML.includes(value)),
  completedBrowserActions: ['onboarding', 'owner-act', 'plan-edit', 'plan-execute', 'crew-inspection', 'project-briefing'],
}))()`);
if (result.title !== "Crewfold Workbench") throw new Error(`unexpected title ${result.title}`);
if (result.iconCount < 8) throw new Error(`expected Lucide icon pack, found ${result.iconCount} rendered icons`);
if (result.unnamedButtons || result.unlabelledControls) throw new Error(`unnamed browser controls: buttons=${result.unnamedButtons}, fields=${result.unlabelledControls}`);
if (result.leakedHandle) throw new Error("private runtime authority leaked into the browser document");
for (const expected of ["Project briefing", "Event cut #"]) {
  if (!result.body.includes(expected)) throw new Error(`browser result omitted ${expected}`);
}
fs.writeFileSync(outputPath, JSON.stringify(result, null, 2));
socket.close();
