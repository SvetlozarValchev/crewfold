import fs from "node:fs";
import path from "node:path";

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

async function capture(label) {
  const directory = process.env.CREWFOLD_SCREENSHOT_DIR;
  if (!directory) return;
  fs.mkdirSync(directory, { recursive: true });
  const metrics = await command("Page.getLayoutMetrics");
  const content = metrics.cssContentSize ?? metrics.contentSize;
  const result = await command("Page.captureScreenshot", { format: "png", captureBeyondViewport: true, fromSurface: true, clip: { x: 0, y: 0, width: content.width, height: Math.min(content.height, 5000), scale: 1 } });
  fs.writeFileSync(path.join(directory, `${label}.png`), Buffer.from(result.data, "base64"));
}

await command("Runtime.enable");
await command("Page.enable");
await waitFor("document.body?.innerText.includes('Bring your repository into the workbench.')", "onboarding");
await waitFor("document.body.innerText.includes('Herdr interactive runtime') && document.querySelector('.advanced-runtime select')?.value === 'herdr'", "Herdr-first onboarding default");
await capture("01-onboarding");

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
await waitFor("document.body.innerText.includes('No implementation work accepted yet') && document.body.innerText.includes('worker runs')", "executive and implementation surfaces are separated");

await evaluate(`(() => {
  const area = document.querySelector('textarea[aria-label="Message to project executive"]');
  Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value').set.call(area, 'Build the first playable world loop and report exact evidence');
  area.dispatchEvent(new Event('input', { bubbles: true }));
  area.dispatchEvent(new Event('change', { bubbles: true }));
  area.form.requestSubmit();
  return true;
})()`);
await waitFor("document.body.innerText.includes('I reviewed the durable project context and prepared one exact proposal for owner review. No project effect has executed.')", "durable executive response", 20000);
await waitFor("document.body.innerText.includes('1 proposal is ready. Review exactly what will change.')", "linked typed proposal");
await waitFor("document.body.innerText.includes('Executive response recorded. The durable conversation remains here; its short-lived provider session is not project authority.')", "terminal executive exchange notice");
await waitFor("document.body.innerText.includes('No implementation work accepted yet') && [...document.querySelectorAll('.metric-row strong')].at(0)?.textContent === '0'", "standing executive task excluded from implementation work");
await capture("02-executive-conversation");

await evaluate(`(() => { [...document.querySelectorAll('nav button')].find((button) => button.textContent.includes('Decisions')).click(); return true; })()`);
await waitFor("document.body.innerText.includes('Needs your review') && document.body.innerText.includes('Create one exact bounded implementation task for owner review.')", "exact executive proposal");
await waitFor("document.body.innerText.toLowerCase().includes('1 proposed implementation task') && document.body.innerText.includes('Implement the next bounded project step')", "plain-language proposal impact");
await capture("03-decision-review");
await evaluate(`(() => {
  const button = [...document.querySelectorAll('button')].find((candidate) => candidate.textContent.includes('Accept these tasks') && !candidate.disabled);
  if (button) button.click();
  return Boolean(button);
})()`);
await waitFor("document.body.innerText.includes('Nothing needs your decision') && document.body.innerText.includes('Earlier decisions and rejected drafts')", "recorded exact proposal acceptance", 20000);
await evaluate(`(() => { document.querySelector('.decision-history > summary')?.click(); return true; })()`);
await waitFor("document.body.innerText.includes('Recorded owner decision') && document.body.innerText.includes('Accepted the exact reviewed executive proposal.')", "accepted proposal history");
await capture("03b-decision-history");

await evaluate(`(() => { [...document.querySelectorAll('nav button')].find((button) => button.textContent.includes('Workbench')).click(); return true; })()`);
await waitFor("document.body.innerText.includes('PROACTIVE EXECUTIVE REVIEW OF WORKER ACTIVITY THROUGH EVENT #') && document.body.innerText.includes('Executive is caught up')", "automatic executive review of worker completion", 20000);

await evaluate(`(() => { [...document.querySelectorAll('nav button')].find((button) => button.textContent.includes('Work graph')).click(); return true; })()`);
await waitFor("document.body.innerText.includes('Implement the next bounded project step')", "accepted proposal task in work graph");
await capture("04-work-graph");
await evaluate(`(() => {
  const task = [...document.querySelectorAll('.work-item')].find((button) => button.textContent.includes('Implement the next bounded project step'));
  if (task) task.click();
  return Boolean(task);
})()`);
await waitFor("document.body.innerText.includes('READINESS') && document.body.innerText.includes('ASSIGNMENT')", "task readiness inspector");
await capture("04b-task-inspector");
if (browserExceptions.length) throw new Error(`browser exceptions after task inspection: ${browserExceptions.join(" | ")}`);
await evaluate(`(() => { document.querySelector('button[aria-label="Close inspector"]')?.click(); return true; })()`);

await evaluate(`(() => { [...document.querySelectorAll('nav button')].find((button) => button.textContent.includes('Crew')).click(); return true; })()`);
await waitFor("document.body.innerText.includes('Project executive') && document.body.innerText.includes('Implementation workers') && document.body.innerText.includes('Inspect worker')", "crew roles and inspector action");
await capture("05-crew-roles");
await evaluate(`(() => { [...document.querySelectorAll('button')].find((button) => button.textContent.includes('Inspect worker')).click(); return true; })()`);
await waitFor("document.body.innerText.includes('REPOSITORY OBSERVATION')", "agent repository observation");

await evaluate(`(() => { document.querySelector('button[aria-label="Close inspector"]').click(); [...document.querySelectorAll('nav button')].find((button) => button.textContent.includes('Briefing')).click(); return true; })()`);
await waitFor("document.body.innerText.includes('Project briefing')", "briefing view");
await evaluate(`(() => { [...document.querySelectorAll('button')].find((button) => button.textContent.includes('Generate briefing')).click(); return true; })()`);
await waitFor("document.body.innerText.includes('Event cut #')", "bounded project briefing", 20000);

await command("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 1, mobile: true });
await evaluate(`(() => { [...document.querySelectorAll('nav button')].find((button) => button.textContent.includes('Workbench')).click(); window.scrollTo(0, 0); return true; })()`);
await waitFor("getComputedStyle(document.querySelector('.mobile-menu')).display !== 'none' && document.querySelector('.sidebar').getBoundingClientRect().right <= 0", "collapsed mobile navigation");
await capture("06-mobile-workbench");

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
if (!result.body.includes("Talk to your project executive")) throw new Error("mobile result omitted the project executive workbench");
fs.writeFileSync(outputPath, JSON.stringify(result, null, 2));
socket.close();
