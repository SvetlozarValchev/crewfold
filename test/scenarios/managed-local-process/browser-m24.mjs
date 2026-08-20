import fs from "node:fs";

const [, , debuggerPort, mode, repositoryPath, outputPath] = process.argv;
if (!debuggerPort || !mode || !repositoryPath || !outputPath) throw new Error("usage: browser-m24.mjs <debugger-port> <setup|inspect> <repository-path> <output-path>");

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
const exceptions = [];
const failedResources = [];
socket.addEventListener("message", (event) => {
  const message = JSON.parse(String(event.data));
  if (message.method === "Runtime.exceptionThrown") exceptions.push(message.params?.exceptionDetails?.exception?.description ?? message.params?.exceptionDetails?.text ?? "browser exception");
  if (message.method === "Network.responseReceived" && message.params?.response?.status >= 400) failedResources.push(`${message.params.response.status} ${message.params.response.url}`);
  if (message.method === "Network.loadingFailed" && !message.params?.canceled && message.params?.errorText !== "net::ERR_ABORTED") failedResources.push(`${message.params?.errorText ?? "load failed"} ${message.params?.requestId ?? "unknown"}`);
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
  if (result.exceptionDetails) throw new Error(result.exceptionDetails.exception?.description ?? result.exceptionDetails.text ?? "browser evaluation failed");
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

await command("Runtime.enable");
await command("Network.enable");
await command("Page.enable");
if (mode === "setup") {
  await waitFor("document.body?.innerText.includes('Bring your repository into the workbench.')", "domain onboarding");
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
    set(inputs[2], 'generic-process');
    set(inputs[3], 'process-coordinator');
    set(inputs[4], 'local process coordinator');
    set(selects[0], 'domain-coordinator');
    set(textareas[0], 'Coordinate this local process acceptance domain.');
    set(textareas[1], 'Maintain exact process scope and report health without inventing authority.');
    set(selects[1], 'hands_on');
    set(selects[2], 'fixture-mcp');
    set(selects[3], 'direct');
    form.requestSubmit();
    return true;
  })()`);
  await waitFor("document.querySelector('.m22-console') && document.body.innerText.includes('generic-process')", "created domain console");
  if (exceptions.length) throw new Error(`browser exceptions: ${exceptions.join(" | ")}`);
  if (failedResources.length) throw new Error(`failed browser resources: ${failedResources.join(" | ")}`);
  fs.writeFileSync(outputPath, JSON.stringify({ schema: "urn:crewfold:test:managed-local-process-browser:v1", setup: true, exceptions, failedResources }, null, 2) + "\n");
  socket.close();
  process.exit(0);
}
if (mode !== "inspect") throw new Error(`unknown browser mode ${mode}`);
await waitFor("document.body?.innerText.toLowerCase().includes('managed local processes') && document.body.innerText.includes('fixture-http')", "managed process owner surface");
const initial = await evaluate(`(() => ({
  text: document.body.innerText,
  invalid: document.querySelectorAll(':invalid').length,
  horizontalOverflow: document.documentElement.scrollWidth > document.documentElement.clientWidth,
  restartEnabled: [...document.querySelectorAll('button')].some((button) => button.textContent.trim() === 'restart' && !button.disabled),
  stopEnabled: [...document.querySelectorAll('button')].some((button) => button.textContent.trim() === 'stop' && !button.disabled),
}))()`);
const initialText = initial.text.toLowerCase();
for (const expected of ["generic python http fixture", "healthy", "restart", "stop"]) {
  if (!initialText.includes(expected)) throw new Error(`managed process browser surface omitted ${expected}`);
}
if (initial.invalid) throw new Error(`managed process browser surface has ${initial.invalid} invalid controls`);
if (initial.horizontalOverflow) throw new Error("managed process browser surface overflows horizontally");
if (!initial.restartEnabled || !initial.stopEnabled) throw new Error("managed process browser controls are unavailable for a healthy process");

await evaluate(`(() => {
  const button = [...document.querySelectorAll('button')].find((candidate) => candidate.textContent.trim() === 'logs');
  button?.click();
  return Boolean(button);
})()`);
await waitFor("document.body.innerText.includes('fixture-ready') && document.body.innerText.toLowerCase().includes('live logs')", "managed process logs");
if (exceptions.length) throw new Error(`browser exceptions: ${exceptions.join(" | ")}`);
if (failedResources.length) throw new Error(`failed browser resources: ${failedResources.join(" | ")}`);
if (process.env.CREWFOLD_SCREENSHOT_DIR) {
  fs.mkdirSync(process.env.CREWFOLD_SCREENSHOT_DIR, { recursive: true });
  const screenshot = await command("Page.captureScreenshot", { format: "png", captureBeyondViewport: false, fromSurface: true });
  fs.writeFileSync(`${process.env.CREWFOLD_SCREENSHOT_DIR}/managed-local-process.png`, Buffer.from(screenshot.data, "base64"));
}

fs.writeFileSync(outputPath, JSON.stringify({
  schema: "urn:crewfold:test:managed-local-process-browser:v1",
  process: "fixture-http",
  state: "healthy",
  logsVisible: true,
  restartEnabled: initial.restartEnabled,
  stopEnabled: initial.stopEnabled,
  horizontalOverflow: initial.horizontalOverflow,
  exceptions,
  failedResources,
}, null, 2) + "\n");
socket.close();
