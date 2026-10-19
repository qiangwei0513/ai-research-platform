import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { runInContext } from "node:vm";
import { JSDOM } from "jsdom";

const root = new URL("../", import.meta.url);
const html = await readFile(new URL("index.html", root), "utf8");
const scripts = await Promise.all(["vendor/marked.umd.js", "vendor/purify.min.js", "account.js", "app.js"].map((name) => readFile(new URL(`assets/${name}`, root), "utf8")));
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

function page(t, handler, stored, fastPolling = false) {
  const dom = new JSDOM(html, { url: "http://localhost/", runScripts: "outside-only", pretendToBeVisual: true });
  const w = dom.window;
  t.after(() => w.close());
  w.TextEncoder = TextEncoder;
  w.matchMedia = () => ({ matches: false });
  w.HTMLElement.prototype.scrollIntoView = function () {};
  w.HTMLDialogElement.prototype.showModal = function () { this.open = true; };
  w.HTMLDialogElement.prototype.close = function () { if (this.open) { this.open = false; this.dispatchEvent(new w.Event("close")); } };
  if (fastPolling) {
    const setTimer = w.setTimeout.bind(w);
    w.setTimeout = (callback, delay, ...args) => setTimer(callback, delay === 5000 ? 20 : delay, ...args);
  }
  if (stored) w.sessionStorage.setItem(key, JSON.stringify(stored));
  const requests = [];
  w.fetch = async (url, options = {}) => {
    if (url === "/ping") return ok({ message: "pong" });
    const request = { url, ...options, data: options.body ? JSON.parse(options.body) : null };
    requests.push(request);
    return handler(request);
  };
  for (const script of scripts) runInContext(script, dom.getInternalVMContext());
  const $ = (id) => w.document.getElementById(id);
  const submit = (id) => $(id).dispatchEvent(new w.Event("submit", { bubbles: true, cancelable: true }));
  return { w, $, submit, requests };
}

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
