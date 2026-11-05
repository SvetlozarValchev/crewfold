// Provider-free browser regression: node scripts/test-web-live.mjs /absolute/path/to/crewfold
// Starts only disposable Crewfold/Chrome children; never connects to live rooms.
import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { spawn } from "node:child_process";

const binary = path.resolve(process.argv[2] || "");
if (!process.argv[2] || !fs.statSync(binary).isFile()) throw Error("Supply a freshly built Crewfold binary");
const temp = fs.mkdtempSync(path.join(os.tmpdir(), "crewfold-web-live-"));
const state = path.join(temp, "state");
const daemon = spawn(binary, ["daemon", "run", "--data-dir", state, "--socket", path.join(temp, "room.sock"), "--web-address", "127.0.0.1:0"], { stdio: ["ignore", "ignore", "pipe"] });
let chrome, ws, daemonErrors = "", chromeErrors = "";
daemon.stderr.on("data", data => { daemonErrors += data; });
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
async function waitFor(check, label) {
  for (let n = 0; n < 120; n++) { if (await check()) return; await delay(100); }
  throw Error("Timed out: " + label);
}
try {
  await waitFor(() => fs.existsSync(path.join(state, "web-address")), "isolated daemon: " + daemonErrors);
  const origin = "http://" + fs.readFileSync(path.join(state, "web-address"), "utf8").trim();
  const token = fs.readFileSync(path.join(state, "web-owner-token"), "utf8").trim();
  async function rpc(method, params) {
    const response = await fetch(origin + "/api/rpc", { method: "POST", headers: { "Content-Type": "application/json", Origin: origin, Authorization: "Bearer " + token }, body: JSON.stringify({ id: crypto.randomUUID(), method, params }) });
    const value = await response.json();
    if (!response.ok || value.error) throw Error(JSON.stringify(value.error || response.status));
    return value.result;
  }
  const room = "browser-regression";
  const mutate = (method, fields) => rpc(method, { room, owner: true, operation_id: crypto.randomUUID(), ...fields });
  await rpc("room.create", { slug: room, title: "Browser regression", topic: "Disposable UI fixture; no agents or deliveries." });
  fs.mkdirSync(path.join(temp, "agent"));
  await rpc("participant.join", { room, handle: "assigned-agent", display_name: "Assigned agent", working_directory: path.join(temp, "agent"), delivery: "none" });
  const created = await mutate("thread.create", { title: "Long discussion", notes: "The initial post remains part of this discussion." });
  assert.deepEqual(created.item.subscribers, [], "human creator must not become an agent subscriber");
  const key = created.item.key;
  for (let n = 1; n <= 55; n++) await mutate("thread.reply", { key, body: "Update " + n + "\n\nThe trial keeps the same scope. We are checking the room view, readable paragraphs, and independent work.\n\n- Keep the discussion visible.\n- Preserve context and drafts." });
  await mutate("task.create", { title: "Check the long thread", notes: "Keep the task description separate from posts.", blocker: "Waiting for evidence", status: "in-progress" });
  chrome = spawn(process.env.CHROME_BIN || "google-chrome", ["--headless", "--no-sandbox", "--disable-gpu", "--disable-dev-shm-usage", "--no-first-run", "--no-default-browser-check", "--user-data-dir=" + path.join(temp, "chrome"), "--remote-debugging-port=0", "--window-size=1600,1100", origin], { stdio: ["ignore", "ignore", "pipe"] });
  let devtools = "";
  chrome.stderr.on("data", data => { chromeErrors += data; devtools = chromeErrors.match(/DevTools listening on (ws:\/\/[^\s]+)/)?.[1] || ""; });
  await waitFor(() => !!devtools, "Chrome DevTools");
  const endpoint = new URL(devtools);
  const pages = await (await fetch("http://" + endpoint.host + "/json")).json();
  ws = new WebSocket(pages.find(page => page.type === "page").webSocketDebuggerUrl);
  await new Promise(resolve => ws.addEventListener("open", resolve, { once: true }));
  let id = 0; const pending = new Map(), exceptions = [];
  ws.addEventListener("message", ({ data }) => {
    const m = JSON.parse(data);
    if (m.id) { const p = pending.get(m.id); pending.delete(m.id); m.error ? p.reject(m.error) : p.resolve(m.result); }
    else if (m.method === "Runtime.exceptionThrown") exceptions.push(m.params.exceptionDetails.text);
  });
  const cdp = (method, params = {}) => new Promise((resolve, reject) => { const request = ++id; pending.set(request, { resolve, reject }); ws.send(JSON.stringify({ id: request, method, params })); });
  async function js(expression) {
    const result = await cdp("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });
    if (result.exceptionDetails) throw Error(JSON.stringify(result.exceptionDetails));
    return result.result.value;
  }
  const wait = expression => waitFor(() => js(expression), expression);
  const click = selector => js("document.querySelector(" + JSON.stringify(selector) + ").click()");
  const button = (text, scope = "document") => js("[..." + scope + ".querySelectorAll('button')].find(b => b.textContent.trim() === " + JSON.stringify(text) + ").click()");
  const fill = (selector, value) => js("(()=>{const e=document.querySelector(" + JSON.stringify(selector) + ");Object.getOwnPropertyDescriptor(e.tagName==='TEXTAREA'?HTMLTextAreaElement.prototype:HTMLInputElement.prototype,'value').set.call(e," + JSON.stringify(value) + ");e.dispatchEvent(new Event('input',{bubbles:true}));})()");
  const bottom = () => js("(()=>{const d=document.querySelector('dialog');d.scrollTop=d.scrollHeight;})()");
  const distanceFromBottom = () => js("(()=>{const d=document.querySelector('dialog');return d.scrollHeight-d.clientHeight-d.scrollTop;})()");
  await cdp("Page.enable"); await cdp("Runtime.enable");
  await wait("location.origin===" + JSON.stringify(origin) + " && document.readyState==='complete'");
  await js("localStorage.setItem('crewfold-room-session'," + JSON.stringify(token) + ");location.reload()");
  await wait("!!document.querySelector('.room-tabs')");
  await button("Board"); await wait("!!document.querySelector('.board-threads .board-card')");
  await click(".board-threads .board-card"); await wait("!!document.querySelector('dialog .thread-post')");
  await js("window.originalDialog=document.querySelector('dialog');void 0");
  assert.equal(await js("document.querySelectorAll('dialog .thread-post').length"), 50);
  await button("Load earlier posts", "document.querySelector('dialog')");
  await wait("document.querySelectorAll('dialog .thread-post').length===56");

  await js("(()=>{const d=document.querySelector('dialog');d.scrollTop=1800;window.anchor=[...d.querySelectorAll('.thread-post')].find(p=>p.getBoundingClientRect().top>d.querySelector('header').getBoundingClientRect().bottom);window.anchorTop=window.anchor.getBoundingClientRect().top;})()");
  await mutate("thread.reply", { key, body: "Incoming update while reading earlier posts." });
  await wait("document.querySelector('dialog').textContent.includes('Incoming update while reading earlier posts.')");
  assert.equal(await js("window.originalDialog===document.querySelector('dialog')"), true, "poll remounted dialog");
  assert.ok(Math.abs(await js("window.anchor.getBoundingClientRect().top-window.anchorTop")) < 3, "incoming post moved reading anchor");
  assert.equal(await js("document.querySelectorAll('dialog .thread-post').length"), 57, "loaded history lost");

  await bottom();
  await fill(".thread-composer textarea", "Keep this unsent draft.");
  await mutate("thread.reply", { key, body: "Incoming update while writing a draft." });
  await wait("document.querySelector('dialog').textContent.includes('Incoming update while writing a draft.')");
  assert.equal(await js("document.querySelector('.thread-composer textarea').value"), "Keep this unsent draft.");
  assert.ok(await distanceFromBottom() < 3, "bottom reader did not follow new post");
  await button("Send", "document.querySelector('dialog .thread-composer')");
  await wait("document.querySelector('.thread-composer textarea').value===''");
  assert.equal(await js("window.originalDialog===document.querySelector('dialog')"), true, "reply remounted dialog");
  assert.ok(await distanceFromBottom() < 3, "own reply jumped away from bottom");

  const question = await mutate("thread.ask", { key, body: "Extend the trial?", options: ["Extend", "Wait"], human_input: true });
  await wait("!!document.querySelector('dialog .thread-answer')");
  await bottom();
  await click(".thread-answer input[type=radio]");
  await button("Send answer", "document.querySelector('dialog')");
  await wait("!!document.querySelector('dialog .thread-composer')");
  assert.equal(await js("window.originalDialog===document.querySelector('dialog')"), true, "answer remounted dialog");
  assert.ok(await distanceFromBottom() < 3, "answer jumped to top");
  assert.equal(await js("document.querySelectorAll('dialog .thread-post').length>=60"), true, "answer dropped loaded earlier posts");
  assert.equal(await js("document.querySelector('dialog').textContent.includes('Extend')"), true);
  assert.ok(question.item.pending_questions === 1);

  const followup = await mutate("thread.ask", { key, body: "A second decision?", options: ["Old choice", "Wait"], human_input: true });
  const postNumber = followup.entries.at(-1).number;
  await wait("!!document.querySelector('.thread-answer')");
  await fill(".thread-answer input[type=text]", "A written draft that must survive.");
  await mutate("thread.edit-post", { key, post: postNumber, version: followup.item.version, body: "Updated second decision?", options: ["New choice", "Wait"] });
  await wait("document.querySelector('dialog').textContent.includes('Updated second decision?')");
  assert.equal(await js("document.querySelector('.thread-answer input[type=text]').value"), "A written draft that must survive.");
  assert.equal(await js("document.querySelector('.thread-answer').textContent.includes('Old choice')"), false);
  await bottom();
  await button("Send answer", "document.querySelector('dialog')");
  await wait("!!document.querySelector('.thread-composer')");
  const countBeforeBurst = await js("document.querySelectorAll('dialog .thread-post').length");
  for (let n = 0; n < 55; n++) await mutate("thread.reply", { key, body: "Burst update " + n });
  await wait("document.querySelector('dialog').textContent.includes('Burst update 54')");
  assert.equal(await js("document.querySelectorAll('dialog .thread-post').length"), countBeforeBurst + 55, "burst skipped a page of posts");

  await button("Edit", "document.querySelector('dialog')");
  assert.equal(await js("document.querySelector('dialog').textContent.includes('Assignee')"), false, "threads must not have assignment controls");
  await fill(".board-edit-form input", "Unsaved title draft");
  let current = await rpc("thread.show", { room, key });
  await mutate("thread.update", { key, title: "Title changed by a collaborator", version: current.item.version });
  await wait("!!document.querySelector('.board-change-notice')");
  assert.equal(await js("document.querySelector('.board-edit-form input').value"), "Unsaved title draft");
  assert.equal(await js("document.querySelector('.board-edit-form button[type=submit]').disabled"), true);
  await button("Cancel edit", "document.querySelector('dialog')");
  await wait("!!document.querySelector('.thread-composer')");
  assert.equal(await js("!!document.querySelector('.board-change-notice')"), false);

  await cdp("Emulation.setDeviceMetricsOverride", { width: 390, height: 1000, deviceScaleFactor: 1, mobile: false });
  assert.equal(await js("[...document.querySelectorAll('.thread-post-content')].every(e=>e.getBoundingClientRect().width>240)"), true);
  assert.equal(await js("getComputedStyle(document.querySelector('.thread-post .markdown')).fontSize"), "16px");
  assert.equal(await js("document.querySelector('dialog').scrollWidth<=document.querySelector('dialog').clientWidth"), true);
  await bottom();
  const mobile = await cdp("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(temp, "thread-mobile.png"), Buffer.from(mobile.data, "base64"));
  await click("dialog button[aria-label=Close]");
  for (const width of [390, 768, 1024, 1181, 1600]) {
    await cdp("Emulation.setDeviceMetricsOverride", { width, height: 1000, deviceScaleFactor: 1, mobile: false });
    assert.equal(await js("document.documentElement.scrollWidth<=innerWidth"), true, "page overflow at " + width);
    assert.equal(await js("document.querySelector('.board-view').scrollWidth<=document.querySelector('.board-view').clientWidth"), true, "board overflow at " + width);
  }
  const desktop = await cdp("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(temp, "board-desktop.png"), Buffer.from(desktop.data, "base64"));
  await js("[...document.querySelectorAll('.board-card')].find(e=>e.querySelector('h3')?.textContent==='Check the long thread').click()");
  await wait("!!document.querySelector('dialog')");
  await button("Edit", "document.querySelector('dialog')");
  const assigneeSelect = "[...document.querySelectorAll('.board-edit-form label')].find(e=>e.textContent.startsWith('Assignee')).querySelector('select')";
  assert.equal(await js(assigneeSelect + ".multiple"), false, "task assignee must be single-select");
  assert.equal(await js(assigneeSelect + ".value"), "owner", "creator must be initial assignee");
  await js("(()=>{const e=" + assigneeSelect + "; e.value='assigned-agent'; e.dispatchEvent(new Event('change',{bubbles:true}));})()");
  await button("Save changes", "document.querySelector('dialog')");
  await wait("!document.querySelector('.board-edit-form') && document.querySelector('dialog').textContent.includes('Assignee: @assigned-agent')");
  assert.equal(await js("document.querySelector('dialog').textContent.includes('Subscribers: @assigned-agent')"), true);
  await click("dialog button[aria-label=Close]");
  await button("Conversation");
  await wait("!!document.querySelector('.message-markdown')");
  assert.equal(await js("getComputedStyle(document.querySelector('.message-markdown')).fontSize"), "16px");
  const conversation = await cdp("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(temp, "conversation-desktop.png"), Buffer.from(conversation.data, "base64"));
  assert.deepEqual(exceptions, []);
  console.log("PASS: live posts, stable dialog/scroll, retained draft and older pages, replies/answers, metadata conflict, readable responsive layout.");
  console.log("Screenshots and isolated fixture: " + temp);
  await cdp("Browser.close");
} finally {
  ws?.close();
  chrome?.kill("SIGTERM");
  daemon.kill("SIGTERM");
}
