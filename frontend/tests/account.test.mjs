import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { runInContext } from "node:vm";
import { JSDOM } from "jsdom";

const root = new URL("../", import.meta.url);
const html = await readFile(new URL("index.html", root), "utf8");
const scripts = await Promise.all(["vendor/marked.umd.js", "vendor/purify.min.js", "events.js", "account.js", "app.js"].map((name) => readFile(new URL(`assets/${name}`, root), "utf8")));
const key = "zhixu.session.v1";
const user = { id: "a".repeat(32), email: "reader@example.test" };
const session = () => ({ accessToken: "test-token", expiresAt: Date.now() + 60000, user });
const task = { id: "b".repeat(32), query: "已保存的问题", mode: "research", status: "succeeded", started_at: "2026-10-19T00:00:00Z", finished_at: "2026-10-19T00:00:32Z" };
const report = { result: "## 已保存的报告\n\n[来源](https://go.dev)\n\n<script>alert(1)</script><img src=x onerror=alert(1)>", mode: "research", plan: { goal: "测试", searches: [] }, search: { status: "completed", sources: [{ title: "官方文档", url: "https://go.dev", snippets: ["测试片段"] }] } };
const ok = (data, status = 200) => Response.json(data, { status });

async function until(condition) {
  for (let attempt = 0; attempt < 100; attempt++) {
    if (condition()) return;
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  assert.ok(condition(), "UI condition did not become true");
}

function page(t, handler, stored, fastPolling = false, activeTask = null) {
  const dom = new JSDOM(html, { url: "http://localhost/", runScripts: "outside-only", pretendToBeVisual: true });
  const w = dom.window;
  t.after(() => w.close());
  w.TextEncoder = TextEncoder;
  w.TextDecoder = TextDecoder;
  w.matchMedia = () => ({ matches: false });
  w.HTMLElement.prototype.scrollIntoView = function () {};
  w.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
  w.HTMLDialogElement.prototype.close = function () { if (this.open) { this.open = false; this.dispatchEvent(new w.Event("close")); } };
  if (fastPolling) {
    const setTimer = w.setTimeout.bind(w);
    w.setTimeout = (callback, delay, ...args) => setTimer(callback, delay === 5000 ? 20 : delay, ...args);
  }
  if (stored) w.sessionStorage.setItem(key, JSON.stringify(stored));
  if (activeTask) w.sessionStorage.setItem("zhixu.activeTask.v1", JSON.stringify(activeTask));
  const requests = [];
  w.fetch = async (url, options = {}) => {
    if (url === "/ping") return ok({ message: "pong" });
    if (url === "/health" && !fastPolling) return ok({ async_tasks: false });
    const request = { url, ...options, data: options.body ? JSON.parse(options.body) : null };
    requests.push(request);
    return handler(request);
  };
  for (const script of scripts) runInContext(script, dom.getInternalVMContext());
  const $ = (id) => w.document.getElementById(id);
  const submit = (id) => $(id).dispatchEvent(new w.Event("submit", { bubbles: true, cancelable: true }));
  return { w, $, submit, requests };
}

function eventChannel() {
  let writer;
  const body = new ReadableStream({ start(c) { writer = c; } });
  return {
    response: new Response(body, { headers: { "Content-Type": "text/event-stream" } }),
    send: (id, type, data) => writer.enqueue(new TextEncoder().encode(`id: ${id}\nevent: ${type}\ndata: ${JSON.stringify(data)}\n\n`)),
  };
}

test("SSE 实时搜索计数、请求头认证与终态报告读取", async (t) => {
  const channel = eventChannel();
  let finished = false;
  const p = page(t, ({ url, headers }) => {
    if (url === "/health") return ok({ async_tasks: true, task_events: true });
    if (url === "/api/tasks") return ok({ task_id: task.id }, 202);
    if (url.endsWith("/events")) { assert.equal(headers.Authorization, "Bearer test-token"); assert.ok(!url.includes("test-token")); return channel.response; }
    if (url.endsWith("/report")) return ok(report);
    return ok({ ...task, async: true, status: finished ? "succeeded" : "running" });
  }, session(), true);
  p.$("query").value = "测试实时进度";
  p.submit("research-form");
  await until(() => p.requests.some((r) => r.url.endsWith("/events")));
  channel.send(2, "snapshot", { last_event_id: 2, task: { ...task, status: "running", async: true, attempt: 1, max_attempts: 3 } });
  channel.send(3, "search_progress", { status: "running", attempt: 1, progress: { stage: "search", completed: 1, total: 3 } });
  await until(() => p.$("task-progress").textContent.includes("1 / 3"));
  assert.equal(p.$("success-state").hidden, true);
  finished = true;
  channel.send(4, "succeeded", { status: "succeeded", attempt: 1 });
  await until(() => !p.$("success-state").hidden);
  assert.equal(p.requests.filter((r) => r.url === "/api/tasks").length, 1);
  assert.equal(p.w.sessionStorage.getItem("zhixu.activeTask.v1"), null);
});

test("刷新恢复原任务并订阅进度，退出取消订阅和本地恢复记录", async (t) => {
  const channel = eventChannel();
  const running = { ...task, status: "running", async: true, attempt: 1 };
  const p = page(t, ({ url }) => {
    if (url === "/health") return ok({ task_events: true });
    if (url.endsWith("/events")) return channel.response;
    return ok(running);
  }, session(), true, { id: task.id, ownerId: user.id });
  await until(() => p.requests.some((r) => r.url.endsWith("/events")));
  channel.send(8, "snapshot", { last_event_id: 8, task: running, progress: { stage: "writer", completed: 0, total: 1 } });
  await until(() => p.$("task-progress").textContent.includes("进行中"));
  assert.equal(p.$("query").value, task.query);
  assert.equal(p.requests.filter((r) => r.method === "POST").length, 0);
  p.$("account-button").click(); p.$("logout-button").click();
  assert.equal(p.w.sessionStorage.getItem("zhixu.activeTask.v1"), null);
  assert.equal(p.$("empty-state").hidden, false);
  assert.equal(p.requests.find((r) => r.url.endsWith("/events")).signal.aborted, true);
});

test("历史详情订阅阶段事件，关闭详情停止连接", async (t) => {
  const channel = eventChannel();
  const running = { ...task, async: true, status: "running" };
  const p = page(t, ({ url }) => {
    if (url === "/health") return ok({ task_events: true });
    if (url.includes("?page=")) return ok({ items: [running], total: 1 });
    if (url.endsWith("/runs") || url.endsWith("/attempts")) return ok({ items: [] });
    if (url.endsWith("/events")) return channel.response;
    return ok(running);
  }, session(), true);
  p.$("history-button").click();
  await until(() => p.$("history-list").children.length === 1);
  p.$("history-list").firstElementChild.click();
  await until(() => p.requests.some((r) => r.url.endsWith("/events")));
  channel.send(4, "snapshot", { last_event_id: 4, task: running, progress: { stage: "search", completed: 2, total: 3 } });
  await until(() => p.$("history-detail").textContent.includes("2 / 3"));
  p.$("close-history").click();
  assert.equal(p.requests.find((r) => r.url.endsWith("/events")).signal.aborted, true);
});

test("注册校验密码、自动登录，并且只保存令牌而不保存密码", async (t) => {
  const p = page(t, ({ url }) => url.endsWith("register") ? ok({ user }, 201) : ok({ user, access_token: "test-token", expires_in: 86400 }));
  p.$("account-button").click();
  p.$("auth-register").click();
  p.$("auth-email").value = user.email;
  p.$("auth-password").value = "TestPassword2026!";
  p.$("auth-confirm").value = "different";
  p.submit("auth-form");
  await until(() => !p.$("auth-error").hidden);
  assert.match(p.$("auth-error").textContent, /不一致/);
  assert.equal(p.requests.length, 0);
  p.$("auth-confirm").value = "TestPassword2026!";
  p.submit("auth-form");
  await until(() => p.$("account-label").textContent === user.email);
  assert.deepEqual(p.requests.map((r) => r.url), ["/api/auth/register", "/api/auth/login"]);
  assert.equal(p.$("account-dialog").open, false);
  const stored = p.w.sessionStorage.getItem(key);
  assert.ok(!stored.includes("TestPassword"));
  assert.equal(JSON.parse(stored).accessToken, "test-token");
  assert.equal(p.$("auth-password").value, "");
});

test("按 UTF-8 字节校验密码，错误登录保持匿名状态", async (t) => {
  const p = page(t, () => ok({ error: "邮箱或密码错误" }, 401));
  p.$("account-button").click();
  p.$("auth-email").value = user.email;
  p.$("auth-password").value = "密".repeat(25);
  p.submit("auth-form");
  await until(() => !p.$("auth-error").hidden);
  assert.equal(p.requests.length, 0);
  p.$("auth-password").value = "WrongPassword2026";
  p.submit("auth-form");
  await until(() => p.$("auth-error").textContent.includes("邮箱或密码错误"));
  assert.equal(p.w.sessionStorage.getItem(key), null);
  assert.equal(p.$("account-dialog").open, true);
});

test("匿名研究发送 fresh 开关且不带 JWT，Markdown 和来源经过安全过滤", async (t) => {
  const p = page(t, () => ok(report));
  p.$("query").value = "新问题";
  p.$("fresh-search").checked = true;
  p.submit("research-form");
  await until(() => !p.$("success-state").hidden);
  assert.equal(p.requests[0].headers.Authorization, undefined);
  assert.equal(p.requests[0].data.fresh, true);
  assert.equal(p.$("panel-report").querySelectorAll("script,img,[onerror]").length, 0);
  assert.equal(p.$("panel-report").querySelector("a").rel, "noopener noreferrer");
  assert.match(p.$("result-saved").textContent, /匿名结果/);
});

test("恢复标签页登录，分页读取历史并打开旧报告，退出会清除私有内容", async (t) => {
  const p = page(t, ({ url, headers }) => {
    assert.equal(headers.Authorization, "Bearer test-token");
    if (url.includes("page=1")) return ok({ items: [task], total: 9 });
    if (url.includes("page=2")) return ok({ items: [{ ...task, query: "第二页的问题" }], total: 9 });
    if (url.endsWith("/runs")) return ok({ items: [{ stage: "writer", status: "succeeded", started_at: task.started_at, finished_at: task.finished_at }] });
    if (url.endsWith("/report")) return ok(report);
    return ok(task);
  }, session());
  p.$("history-button").click();
  await until(() => p.$("history-list").children.length === 1);
  assert.match(p.$("history-page").textContent, /第 1 \/ 2 页/);
  p.$("history-next").click();
  await until(() => p.$("history-list").textContent.includes("第二页"));
  p.$("history-list").firstChild.click();
  await until(() => !p.$("open-saved-report").hidden && !p.$("open-saved-report").disabled);
  assert.match(p.$("history-detail").textContent, /32.0 秒/);
  p.$("open-saved-report").click();
  await until(() => !p.$("success-state").hidden);
  assert.equal(p.$("result-duration").textContent, "用时 00:32");
  assert.match(p.$("result-saved").textContent, /已保存/);
  p.$("account-button").click();
  p.$("logout-button").click();
  assert.equal(p.w.sessionStorage.getItem(key), null);
  assert.equal(p.$("panel-report").textContent, "");
  assert.equal(p.$("history-list").textContent, "");
  assert.equal(p.$("query").value, "");
});

test("401 不会自动重发匿名研究，必须登录或明确选择匿名", async (t) => {
  const p = page(t, ({ headers }) => headers.Authorization ? ok({ error: "登录凭据无效或已过期" }, 401) : ok(report), session());
  p.$("query").value = "需要保存的问题";
  p.submit("research-form");
  await until(() => !p.$("error-state").hidden);
  assert.equal(p.requests.length, 1);
  assert.equal(p.w.sessionStorage.getItem(key), null);
  p.$("account-dialog").close();
  p.submit("research-form");
  await until(() => !p.$("submit-button").disabled);
  assert.equal(p.requests.length, 1);
  p.$("continue-guest").click();
  p.submit("research-form");
  await until(() => !p.$("success-state").hidden);
  assert.equal(p.requests.length, 2);
  assert.equal(p.requests[1].headers.Authorization, undefined);
});

test("过期会话在发出请求前被拦截，存储不可用时仍可登录使用", async (t) => {
  const p = page(t, () => ok({ user, access_token: "test-token", expires_in: 86400 }), { ...session(), expiresAt: Date.now() - 1000 });
  p.$("query").value = "问题";
  p.submit("research-form");
  await until(() => !p.$("error-state").hidden);
  assert.equal(p.requests.length, 0);
  Object.defineProperty(p.w, "sessionStorage", { get() { throw new Error("storage blocked"); } });
  p.$("auth-email").value = user.email;
  p.$("auth-password").value = "TestPassword2026!";
  p.submit("auth-form");
  await until(() => p.$("account-label").textContent === user.email);
  assert.match(p.$("account-notice").textContent, /未允许保存登录状态/);
});

test("退出后丢弃仍在返回的历史请求，防止旧账号内容重新出现", async (t) => {
  let release;
  const p = page(t, () => new Promise((resolve) => { release = resolve; }), session());
  p.$("history-button").click();
  await until(() => Boolean(release));
  p.$("close-history").click();
  p.$("account-button").click();
  p.$("logout-button").click();
  release(ok({ items: [task], total: 1 }));
  await new Promise((resolve) => setTimeout(resolve, 20));
  assert.equal(p.$("history-list").textContent, "");
  assert.equal(p.$("history-dialog").open, false);
});

test("失败详情显示阶段原因，再研究只填写问题，不自动产生新调用", async (t) => {
  const failed = { ...task, status: "failed", error_code: "search_failed", error_message: "搜索服务暂不可用" };
  const p = page(t, ({ url }) => {
    if (url.includes("page=")) return ok({ items: [failed], total: 1 });
    if (url.endsWith("/runs")) return ok({ items: [{ stage: "search", status: "failed", error_code: "search_failed", started_at: task.started_at, finished_at: task.finished_at }] });
    return ok(failed);
  }, session());
  p.$("history-button").click();
  await until(() => p.$("history-list").children.length === 1);
  p.$("history-list").firstChild.click();
  await until(() => !p.$("reuse-question").hidden && !p.$("reuse-question").disabled);
  assert.match(p.$("history-detail").textContent, /搜索服务暂不可用/);
  assert.equal(p.$("open-saved-report").hidden, true);
  p.$("reuse-question").click();
  assert.equal(p.$("query").value, failed.query);
  assert.equal(p.requests.some((r) => r.url === "/api/research"), false);
});

test("进行中任务自动更新，终态和关闭历史窗口都会停止轮询", async (t) => {
  let count = 0;
  const p = page(t, () => {
    count++;
    return ok({ items: [{ ...task, status: count === 1 ? "running" : "failed" }], total: 1 });
  }, session(), true);
  p.$("history-button").click();
  await until(() => p.$("history-list").textContent.includes("未完成"));
  await new Promise((resolve) => setTimeout(resolve, 60));
  assert.equal(count, 2);
  count = 0;
  p.$("refresh-history").click();
  await until(() => p.$("history-list").textContent.includes("进行中"));
  p.$("close-history").click();
  await new Promise((resolve) => setTimeout(resolve, 60));
  assert.equal(count, 1);
});

test("登录后异步提交、释放表单并轮询到报告，不占用同步接口", async (t) => {
  let reads = 0;
  const p = page(t, ({ url, method }) => {
    if (url === "/health") return ok({ async_tasks: true });
    if (url === "/api/tasks" && method === "POST") return ok({ task_id: task.id, status: "queued" }, 202);
    if (url.endsWith("/report")) return ok(report);
    if (url === `/api/tasks/${task.id}`) return ok({ ...task, async: true, status: ++reads < 3 ? "running" : "succeeded", attempt: 1, max_attempts: 3 });
    throw new Error(`unexpected ${url}`);
  }, session(), true);
  p.$("query").value = "异步研究";
  p.submit("research-form");
  await until(() => !p.$("async-actions").hidden);
  assert.equal(p.$("submit-button").disabled, false);
  assert.match(p.$("waiting-note").textContent, /后台|离开页面/);
  await until(() => !p.$("success-state").hidden);
  const create = p.requests.find((r) => r.method === "POST");
  assert.ok(create.headers["Idempotency-Key"].length >= 16);
  assert.equal(create.headers.Authorization, "Bearer test-token");
  assert.equal(p.requests.some((r) => r.url === "/api/research"), false);
  assert.match(p.$("result-saved").textContent, /已保存/);
});

test("异步提交响应丢失后，原问题重试复用幂等键", async (t) => {
  let attempts = 0;
  const keys = [];
  const p = page(t, ({ url, headers, method }) => {
    if (url === "/health") return ok({ async_tasks: true });
    if (url === "/api/tasks" && method === "POST") {
      keys.push(headers["Idempotency-Key"]);
      if (++attempts === 1) throw new Error("network lost after acceptance");
      return ok({ task_id: task.id }, 202);
    }
    return ok({ ...task, async: true, status: "queued" });
  }, session(), true);
  p.$("query").value = "原问题";
  p.submit("research-form");
  await until(() => !p.$("error-state").hidden);
  p.submit("research-form");
  await until(() => !p.$("async-actions").hidden);
  assert.equal(keys.length, 2);
  assert.equal(keys[0], keys[1]);
  assert.equal(p.requests.some((r) => r.url === "/api/research"), false);
});

test("执行中可以取消，取消后停止轮询且不请求报告", async (t) => {
  let cancelled = false;
  const p = page(t, ({ url, method }) => {
    if (url === "/health") return ok({ async_tasks: true });
    if (url === "/api/tasks" && method === "POST") return ok({ task_id: task.id }, 202);
    if (url.endsWith("/cancel")) { cancelled = true; return ok({ ...task, status: "cancelled" }); }
    return ok({ ...task, async: true, status: cancelled ? "cancelled" : "running", error_message: cancelled ? "用户取消任务" : "" });
  }, session(), true);
  p.$("query").value = "取消测试";
  p.submit("research-form");
  await until(() => !p.$("async-actions").hidden);
  p.$("cancel-current-task").click();
  await until(() => !p.$("error-state").hidden);
  assert.match(p.$("error-message").textContent, /取消/);
  assert.equal(p.$("async-actions").hidden, true);
  const count = p.requests.length;
  await new Promise((resolve) => setTimeout(resolve, 70));
  assert.equal(p.requests.length, count);
  assert.equal(p.requests.some((r) => r.url.endsWith("/report")), false);
});

test("异步任务轮询期间退出，迟到的报告不会重新显示", async (t) => {
  let release;
  const p = page(t, ({ url, method }) => {
    if (url === "/health") return ok({ async_tasks: true });
    if (url === "/api/tasks" && method === "POST") return ok({ task_id: task.id }, 202);
    if (url.endsWith("/report")) return new Promise((resolve) => { release = resolve; });
    return ok({ ...task, async: true });
  }, session(), true);
  p.$("query").value = "私有结果";
  p.submit("research-form");
  await until(() => Boolean(release));
  p.$("account-button").click();
  p.$("logout-button").click();
  release(ok(report));
  await new Promise((resolve) => setTimeout(resolve, 30));
  assert.equal(p.$("success-state").hidden, true);
  assert.equal(p.$("panel-report").textContent, "");
});

test("历史显示异步尝试记录，并允许取消排队任务", async (t) => {
  let cancelled = false;
  const current = () => ({ ...task, async: true, status: cancelled ? "cancelled" : "queued", attempt: 1, max_attempts: 3 });
  const p = page(t, ({ url }) => {
    if (url.endsWith("/cancel")) { cancelled = true; return ok(current()); }
    if (url.startsWith("/api/tasks?")) return ok({ items: [current()], total: 1 });
    if (url.endsWith("/attempts")) return ok({ items: [{ attempt: 1, status: "failed", error_code: "worker_lost", error_message: "执行进程中断" }] });
    if (url.endsWith("/runs")) return ok({ items: [] });
    return ok(current());
  }, session(), true);
  p.$("history-button").click();
  await until(() => p.$("history-list").children.length === 1);
  p.$("history-list").firstChild.click();
  await until(() => !p.$("cancel-task").hidden);
  assert.match(p.$("history-detail").textContent, /执行进程中断/);
  p.$("cancel-task").click();
  await until(() => cancelled && p.$("cancel-task").hidden);
  assert.match(p.$("history-detail").textContent, /取消/);
});
