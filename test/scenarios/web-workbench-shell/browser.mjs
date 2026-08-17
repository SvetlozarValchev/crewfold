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
const browserExceptions = [];
socket.addEventListener("message", (event) => {
  const message = JSON.parse(String(event.data));
  if (message.method === "Runtime.exceptionThrown") {
    browserExceptions.push(message.params?.exceptionDetails?.exception?.description ?? message.params?.exceptionDetails?.text ?? "unknown browser exception");
  }
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
await waitFor("document.body.innerText.includes('Talk to your project executive')", "workbench after onboarding");

await evaluate(`(() => {
  const area = document.querySelector('textarea[aria-label="Message to project executive"]');
  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(area, 'Build the first playable world loop and report exact evidence');
  area.dispatchEvent(new Event('input', { bubbles: true }));
  area.dispatchEvent(new Event('change', { bubbles: true }));
  area.form.requestSubmit();
  return true;
})()`);
await waitFor("document.body.innerText.includes('I reviewed the durable project context and prepared one exact proposal for owner review. No project effect has executed.')", "durable executive response", 20000);
await waitFor("document.body.innerText.includes('1 typed proposal is ready for explicit review.')", "linked typed proposal");
await waitFor("document.body.innerText.includes('Executive response recorded. Its short-lived provider session is finishing; the durable conversation remains here.')", "terminal executive exchange notice");
await waitFor("document.body.innerText.includes('No implementation work proposed yet') && [...document.querySelectorAll('.metric-row strong')].at(0)?.textContent === '0'", "standing executive task excluded from implementation work");

await evaluate(`(() => { [...document.querySelectorAll('nav button')].find((button) => button.textContent.includes('Decisions')).click(); return true; })()`);
await waitFor("document.body.innerText.includes('Executive proposals') && document.body.innerText.includes('Create One Exact Bounded Implementation Task For Owner Review.')", "exact executive proposal");
await waitFor("document.body.innerText.includes('1 exact operation') && document.body.innerText.includes('Implement the next bounded project step')", "typed proposal operation");
await evaluate(`(() => {
  const button = [...document.querySelectorAll('button')].find((candidate) => candidate.textContent.includes('Accept exact proposal') && !candidate.disabled);
  if (button) button.click();
  return Boolean(button);
})()`);
await waitFor("document.body.innerText.includes('Recorded owner decision') && document.body.innerText.includes('Accepted the exact reviewed executive proposal.')", "recorded exact proposal acceptance", 20000);

await evaluate(`(() => { [...document.querySelectorAll('nav button')].find((button) => button.textContent.includes('Workbench')).click(); return true; })()`);
await waitFor("document.body.innerText.includes('PROACTIVE EXECUTIVE REVIEW OF WORKER ACTIVITY THROUGH EVENT #') && document.body.innerText.includes('Executive is caught up')", "automatic executive review of worker completion", 20000);

await evaluate(`(() => { [...document.querySelectorAll('nav button')].find((button) => button.textContent.includes('Work graph')).click(); return true; })()`);
await waitFor("document.body.innerText.includes('Implement the next bounded project step')", "accepted proposal task in work graph");
await evaluate(`(() => {
  const task = [...document.querySelectorAll('.kanban-column button')].find((button) => button.textContent.includes('Implement the next bounded project step'));
  if (task) task.click();
  return Boolean(task);
})()`);
await waitFor("document.body.innerText.includes('READINESS') && document.body.innerText.includes('ASSIGNMENT')", "task readiness inspector");
if (browserExceptions.length) throw new Error(`browser exceptions after task inspection: ${browserExceptions.join(" | ")}`);
await evaluate(`(() => { document.querySelector('button[aria-label="Close inspector"]')?.click(); return true; })()`);

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
  completedBrowserActions: ['onboarding', 'executive-exchange', 'proposal-review', 'proposal-acceptance', 'worker-review', 'crew-inspection', 'project-briefing'],
}))()`);
if (result.title !== "Crewfold Workbench") throw new Error(`unexpected title ${result.title}`);
if (result.iconCount < 8) throw new Error(`expected Lucide icon pack, found ${result.iconCount} rendered icons`);
if (result.unnamedButtons || result.unlabelledControls) throw new Error(`unnamed browser controls: buttons=${result.unnamedButtons}, fields=${result.unlabelledControls}`);
if (result.leakedHandle) throw new Error("private runtime authority leaked into the browser document");
if (browserExceptions.length) throw new Error(`browser exceptions: ${browserExceptions.join(" | ")}`);
for (const expected of ["Project briefing", "Event cut #"]) {
  if (!result.body.includes(expected)) throw new Error(`browser result omitted ${expected}`);
}
fs.writeFileSync(outputPath, JSON.stringify(result, null, 2));
socket.close();
