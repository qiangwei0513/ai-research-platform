"use strict";

// 账号与历史只通过同源 API 访问。密码不持久化，JWT 只留在当前标签页。
const Account = (() => {
  const $ = (id) => document.getElementById(id);
  const storageKey = "zhixu.session.v1";
  const pageSize = 8;
  let session = null;
  let needsLogin = false;
  let authMode = "login";
  let authBusy = false;
  let researchBusy = false;
  let expiryTimer;
  let pollTimer;
  let revision = 0;
  let page = 1;
  let total = 0;
  let selectedTask = null;
  let historyBusy = false;
  let callbacks = {};
  let eventSupport;
  let stopDetailEvents;

  async function canStream() {
    if (eventSupport === undefined) {
      try { eventSupport = Boolean((await api("/health", { auth: "none" })).task_events); }
      catch { return false; }
    }
    return eventSupport;
  }

  function followTask(id, options) {
    const active = currentSession();
    const token = active?.accessToken;
    return TaskEvents.follow(id, {
      ...options,
      getToken: () => currentSession()?.accessToken === token ? token : null,
      onAuthError: () => { if (session?.accessToken === token) endSession(true); },
    });
  }

  function stopDetail() { stopDetailEvents?.(); stopDetailEvents = null; }

  function node(tag, className, text) {
    const el = document.createElement(tag);
    if (className) el.className = className;
    if (text != null) el.textContent = text;
    return el;
  }

  function date(value) {
    const parsed = new Date(value);
    return Number.isFinite(parsed.getTime()) ? parsed.toLocaleString("zh-CN", { hour12: false }) : "时间未知";
  }

  function statusBadge(status) {
    const labels = { succeeded: "已完成", failed: "未完成", running: "进行中", queued: "等待中", cancelled: "已取消" };
    return node("span", `task-status ${Object.hasOwn(labels, status) ? status : "unknown"}`, labels[status] || "状态未知");
  }

  function notice(message = "") {
    $("account-notice").textContent = message;
    $("account-notice").hidden = !message;
  }

  function updateAccount() {
    $("account-label").textContent = session ? session.user.email : "登录 / 注册";
    $("account-button").title = session ? `管理账号：${session.user.email}` : "登录或注册账号";
    $("account-button").setAttribute("aria-label", $("account-button").title);
    $("account-button").classList.toggle("signed-in", Boolean(session));
    $("save-hint").textContent = session ? "本次结果将保存到我的历史" : "匿名体验 · 登录后自动保存";
    $("privacy-note").textContent = session
      ? "登录后提交的任务会保存；可在「我的历史」查看状态和报告。"
      : "匿名结果仅在本页显示；登录后可在「我的历史」中回看。";
    $("auth-view").hidden = Boolean(session);
    $("session-view").hidden = !session;
    $("account-title").textContent = session ? "你的研究空间" : authMode === "register" ? "开启你的研究空间" : "欢迎回到知序";
    if (session) {
      $("session-email").textContent = session.user.email;
      $("session-expiry").textContent = `本次登录有效至 ${date(session.expiresAt)}。`;
    }
  }

  function remember() {
    try {
      if (session) sessionStorage.setItem(storageKey, JSON.stringify(session));
      else sessionStorage.removeItem(storageKey);
    } catch {
      if (session) notice("浏览器未允许保存登录状态；本页仍可使用，刷新后需重新登录。");
    }
  }

  function clearHistory() {
    stopDetail();
    revision++;
    clearTimeout(pollTimer);
    historyBusy = false;
    selectedTask = null;
    page = 1;
    total = 0;
    $("history-list").replaceChildren();
    $("history-detail").replaceChildren();
    $("history-error").hidden = true;
    $("history-loading").hidden = true;
    $("history-dialog").close();
  }

  function endSession(expired = false) {
    const hadSession = Boolean(session);
    session = null;
    needsLogin = expired;
    clearTimeout(expiryTimer);
    clearHistory();
    remember();
    updateAccount();
    if (hadSession) callbacks.onSessionEnd?.();
    notice(expired ? "登录已过期，请重新登录后继续保存研究；也可以在登录窗口选择匿名使用。" : "");
  }

  function currentSession() {
    if (session && Date.now() >= session.expiresAt) endSession(true);
    return session;
  }

  function startSession(data) {
    if (!data || typeof data.access_token !== "string" || !data.access_token ||
        typeof data.user?.id !== "string" || typeof data.user.email !== "string" ||
        !Number.isFinite(data.expires_in) || data.expires_in <= 0) {
      throw new Error("登录响应不完整，请稍后重试。");
    }
    clearHistory();
    clearTimeout(expiryTimer);
    session = { accessToken: data.access_token, user: { id: data.user.id, email: data.user.email }, expiresAt: Date.now() + Math.min(data.expires_in, 86400) * 1000 };
    needsLogin = false;
    notice();
    remember();
    updateAccount();
    expiryTimer = setTimeout(() => endSession(true), session.expiresAt - Date.now());
  }

  function setAuthMode(mode) {
    authMode = mode;
    const register = mode === "register";
    $("auth-login").setAttribute("aria-pressed", String(!register));
    $("auth-register").setAttribute("aria-pressed", String(register));
    $("auth-password").autocomplete = register ? "new-password" : "current-password";
    $("auth-confirm").disabled = !register;
    $("auth-confirm").required = register;
    $("confirm-field").hidden = !register;
    $("auth-error").hidden = true;
    $("auth-submit-label").textContent = register ? "注册并登录" : "登录并继续";
    updateAccount();
  }

  function openAccount(message = "") {
    currentSession();
    if (!session) setAuthMode("login");
    updateAccount();
    $("auth-error").textContent = message;
    $("auth-error").hidden = !message;
    if (!$("account-dialog").open) $("account-dialog").showModal();
    if (!session) $("auth-email").focus();
  }

  function errorMessage(response, data) {
    if (response.status === 429) {
      const seconds = Number(response.headers.get("Retry-After"));
      return `当前请求较多，${seconds > 0 ? `请约 ${Math.min(Math.ceil(seconds), 3600)} 秒后再试。` : "请稍后再试。"}`;
    }
    if (response.status === 504) return "本次处理超时了。试试缩小研究范围，或稍后重新尝试。";
    if (data?.code === "no_sources") return "这次没有找到可用的搜索资料，试试更具体的关键词。";
    if (data?.code === "search_failed") return "搜索服务暂时未能取得资料，请稍后重试。";
    if (response.status === 413) return "问题内容过长，请精简后重新发送。";
    if ([400, 401, 409, 503].includes(response.status) && typeof data?.error === "string") return data.error;
    if (response.status === 404) return "该功能或记录暂不可用，请确认数据服务已启用，或刷新后重试。";
    return "服务暂时无法完成请求，请稍后再试。";
  }

  async function api(path, { method = "GET", body, auth = "required", timeout = 12000, idempotencyKey } = {}) {
    // 过期凭据不能悄悄变成匿名请求，否则用户会误以为报告已经保存。
    const active = auth === "none" ? null : currentSession();
    if (auth !== "none" && (needsLogin || (auth === "required" && !active))) {
      openAccount("请先登录，才能保存或查看个人研究历史。");
      throw new Error("请先登录后重试；本次研究尚未提交。");
    }
    const token = active?.accessToken;
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), timeout);
    try {
      const headers = { Accept: "application/json" };
      if (body !== undefined) headers["Content-Type"] = "application/json";
      if (idempotencyKey) headers["Idempotency-Key"] = idempotencyKey;
      if (token) headers.Authorization = `Bearer ${token}`;
      const response = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body), signal: controller.signal, cache: "no-store" });
      let data;
      try { data = await response.json(); } catch { data = null; }
      if (token && response.status === 401 && session?.accessToken === token) {
        endSession(true);
        openAccount("登录已失效，请重新登录。");
      }
      if (!response.ok) {
        const error = new Error(errorMessage(response, data));
        error.status = response.status;
        error.taskId = data?.task_id || response.headers.get("X-Task-ID");
        throw error;
      }
      // 已退出或换了账号时，丢弃旧请求，避免跨账号显示缓存的私有内容。
      if (token && currentSession()?.accessToken !== token) throw new Error("登录状态已变化，请重新登录后在历史中查看结果。");
      if (!data || typeof data !== "object") throw new Error("服务返回的内容不完整，请稍后重试。");
      return data;
    } catch (error) {
      if (controller.signal.aborted) throw new Error("等待时间过长，请稍后重试；已登录的研究可先在历史中检查状态。");
      if (error instanceof TypeError) throw new Error("暂时连接不上服务，请检查网络后重试。");
      throw error;
    } finally { clearTimeout(timer); }
  }

  function lockAuth(busy) {
    authBusy = busy;
    $("auth-fields").disabled = busy;
    for (const id of ["auth-login", "auth-register", "auth-submit", "close-account", "continue-guest"]) $(id).disabled = busy;
    $("auth-submit-label").textContent = busy ? "请稍候…" : authMode === "register" ? "注册并登录" : "登录并继续";
  }

  async function submitAuth(event) {
    event.preventDefault();
    if (authBusy) return;
    const email = $("auth-email").value.trim();
    const password = $("auth-password").value;
    const bytes = new TextEncoder().encode(password).length;
    let registered = false;
    $("auth-error").hidden = true;
    try {
      if (bytes < 8 || bytes > 72) throw new Error("密码需要 8～72 字节，请调整长度后重试。");
      if (authMode === "register" && password !== $("auth-confirm").value) throw new Error("两次输入的密码不一致。");
      lockAuth(true);
      if (authMode === "register") {
        await api("/api/auth/register", { method: "POST", body: { email, password }, auth: "none" });
        registered = true;
      }
      const data = await api("/api/auth/login", { method: "POST", body: { email, password }, auth: "none" });
      startSession(data);
      $("auth-password").value = "";
      $("auth-confirm").value = "";
      $("account-dialog").close();
      callbacks.onNotice?.(registered ? "注册成功，已登录。之后提交的研究会自动保存。" : "已登录，可以开始研究或查看历史。");
    } catch (error) {
      if (registered) setAuthMode("login");
      $("auth-error").textContent = `${registered ? "账号已注册，请登录。" : ""}${error.message}`;
      $("auth-error").hidden = false;
    } finally { lockAuth(false); }
  }

  function setHistoryBusy(busy) {
    historyBusy = busy;
    $("history-dialog").setAttribute("aria-busy", String(busy));
    $("refresh-history").disabled = busy;
    $("history-prev").disabled = busy || page <= 1;
    $("history-next").disabled = busy || page * pageSize >= total;
    $("open-saved-report").disabled = busy;
    $("reuse-question").disabled = busy;
    $("cancel-task").disabled = busy;
  }

  function renderList(items) {
    const list = $("history-list");
    list.replaceChildren();
    for (const task of items) {
      const button = node("button", "history-item");
      button.type = "button";
      const heading = node("span", "history-item-heading");
      heading.append(node("span", "history-query", task.query), statusBadge(task.status));
      button.append(heading, node("span", "history-item-meta", `${task.mode === "research" ? "深入研究" : "快速问答"} · ${date(task.started_at)}`));
      button.addEventListener("click", () => { selectedTask = task; loadHistory(); });
      list.append(button);
    }
    $("history-empty").hidden = items.length > 0;
    $("history-pagination").hidden = total <= pageSize;
    $("history-page").textContent = `第 ${page} / ${Math.max(1, Math.ceil(total / pageSize))} 页 · 共 ${total} 条`;
  }

  function renderDetail(task, runs, attempts = [], progress = null) {
    const detail = $("history-detail");
    const meta = node("div", "history-detail-meta");
    meta.append(statusBadge(task.status), node("span", "", task.mode === "research" ? "深入研究" : "快速问答"));
    detail.replaceChildren(node("h3", "history-detail-query", task.query), meta, node("p", "field-hint", `提交于 ${date(task.started_at)}`));
    if (task.finished_at) detail.append(node("p", "field-hint", `结束于 ${date(task.finished_at)}`));
    detail.append(node("p", "task-identifier", `任务编号 ${task.id}`));
    if (task.async && eventSupport) {
      const steps = node("ol", "task-progress");
      steps.setAttribute("aria-label", "任务执行阶段");
      TaskProgress.render(steps, task, progress);
      detail.append(steps);
    }
    if (task.status === "running" || task.status === "queued") {
      detail.append(node("p", "help-note", task.async
        ? (task.status === "queued" ? "任务正在排队或等待重试。你可以离开页面，稍后在历史中查看结果。" : "任务正在后台处理。你可以离开页面，也可以取消这项任务。")
        : "这是一项同步请求，请保持提交问题的页面打开。"));
      if (task.async) detail.append(node("p", "field-hint", `已开始 ${task.attempt || 0} / ${task.max_attempts || 3} 次执行`));
      if (task.status === "queued" && task.error_message) detail.append(node("p", "field-hint", `上次未完成：${task.error_message}`));
    } else if (task.status === "failed" || task.status === "cancelled") {
      detail.append(node("p", "task-failure", task.error_message || "本次研究没有完成，可以用此问题重新提交。"));
      if (task.error_code) detail.append(node("p", "field-hint", `错误代码：${task.error_code}`));
    }
    if (runs.length) {
      const stages = { planner: "规划问题", search: "检索资料", writer: "撰写报告", chat: "快速问答", research: "研究流程" };
      const list = node("ol", "run-list");
      for (const run of runs) {
        const item = node("li", "run-item");
        const duration = Math.max(0, new Date(run.finished_at) - new Date(run.started_at));
        item.append(node("span", "run-name", stages[run.stage] || run.stage), statusBadge(run.status), node("span", "run-duration", Number.isFinite(duration) ? `${(duration / 1000).toFixed(1)} 秒` : ""));
        if (run.error_code) item.append(node("small", "run-error", run.error_code));
        list.append(item);
      }
      detail.append(node("h4", "run-title", "执行记录"), list);
    }
    $("open-saved-report").hidden = task.status !== "succeeded";
    $("reuse-question").hidden = ["queued", "running"].includes(task.status);
    $("cancel-task").hidden = !task.async || !["queued", "running"].includes(task.status);
    if (attempts.length) {
      const list = node("ol", "run-list");
      for (const attempt of attempts) {
        const item = node("li", "run-item");
        item.append(node("span", "run-name", `第 ${attempt.attempt} 次执行`), statusBadge(attempt.status));
        if (attempt.error_message) item.append(node("small", "run-error", attempt.error_message));
        list.append(item);
      }
      detail.append(node("h4", "run-title", "尝试记录"), list);
    }
  }

  async function loadHistory(quiet = false) {
    stopDetail();
    clearTimeout(pollTimer);
    const ticket = ++revision;
    const id = selectedTask?.id;
    const active = () => ticket === revision && $("history-dialog").open && Boolean(session);
    $("history-list-view").hidden = Boolean(id);
    $("history-detail-view").hidden = !id;
    $("history-error").hidden = true;
    $("history-loading").hidden = quiet;
    if (!quiet && id) $("history-detail").replaceChildren();
    if (!quiet && !id) {
      $("history-list").replaceChildren();
      $("history-empty").hidden = true;
      $("history-pagination").hidden = true;
    }
    $("open-saved-report").hidden = true;
    $("cancel-task").hidden = true;
    $("reuse-question").hidden = Boolean(id);
    setHistoryBusy(true);
    let running = false;
    try {
      if (id) {
        const [task, records] = await Promise.all([api(`/api/tasks/${encodeURIComponent(id)}`), api(`/api/tasks/${encodeURIComponent(id)}/runs`)]);
        if (!active()) return;
        const attempts = task.async ? await api(`/api/tasks/${encodeURIComponent(id)}/attempts`) : { items: [] };
        const streaming = task.async && await canStream();
        if (!active()) return;
        selectedTask = task;
        renderDetail(task, Array.isArray(records.items) ? records.items : [], Array.isArray(attempts.items) ? attempts.items : []);
        running = ["queued", "running"].includes(task.status);
        if (streaming && running) {
          running = false;
          stopDetailEvents = followTask(id, {
            onEvent: (event) => {
              if (!active()) return;
              const task = event.type === "snapshot" ? event.data.task : { ...selectedTask, ...event.data };
              selectedTask = task;
              renderDetail(task, records.items || [], attempts.items || [], event.data.progress);
              if (TaskEvents.terminal(task.status)) void loadHistory(true);
            },
            onConnection: (status) => {
              if (!active()) return;
              $("history-error").hidden = status !== "reconnecting";
              $("history-error").textContent = "进度连接中断，正在恢复；后台任务继续执行。";
            },
            onFatal: (message) => { if (active()) { $("history-error").textContent = message; $("history-error").hidden = false; } },
          });
        }
      } else {
        const data = await api(`/api/tasks?page=${page}&page_size=${pageSize}`);
        if (!active()) return;
        total = Number(data.total) || 0;
        const items = Array.isArray(data.items) ? data.items : [];
        renderList(items);
        running = items.some((task) => ["queued", "running"].includes(task.status));
      }
    } catch (error) {
      if (!active()) return;
      $("history-error").textContent = error.message;
      $("history-error").hidden = false;
    } finally {
      if (active()) {
        $("history-loading").hidden = true;
        setHistoryBusy(false);
        if (running && !document.hidden) pollTimer = setTimeout(() => loadHistory(true), 5000);
      }
    }
  }

  async function cancelSelectedTask() {
    if (historyBusy || !selectedTask?.async) return;
    const id = selectedTask.id;
    const ticket = ++revision;
    clearTimeout(pollTimer);
    setHistoryBusy(true);
    try {
      await api(`/api/tasks/${encodeURIComponent(id)}/cancel`, { method: "POST" });
      if (ticket === revision && $("history-dialog").open) await loadHistory();
    } catch (error) {
      if (ticket === revision && $("history-dialog").open) {
        $("history-error").textContent = error.message;
        $("history-error").hidden = false;
      }
    } finally { if (ticket === revision) setHistoryBusy(false); }
  }

  function openHistory() {
    if (researchBusy) return;
    if (!currentSession()) {
      openAccount("登录后即可查看自己的研究历史；匿名结果不会保存。");
      return;
    }
    $("account-dialog").close();
    selectedTask = null;
    page = 1;
    $("history-dialog").showModal();
    loadHistory();
  }

  async function openReport() {
    if (historyBusy || !selectedTask || selectedTask.status !== "succeeded") return;
    const task = selectedTask;
    const ticket = ++revision;
    setHistoryBusy(true);
    $("history-error").hidden = true;
    try {
      const report = await api(`/api/tasks/${encodeURIComponent(task.id)}/report`);
      if (ticket !== revision || !$("history-dialog").open) return;
      callbacks.onOpenReport?.(task, report);
      $("history-dialog").close();
    } catch (error) {
      if (ticket !== revision) return;
      $("history-error").textContent = error.message;
      $("history-error").hidden = false;
    } finally { if (ticket === revision) setHistoryBusy(false); }
  }

  function setBusy(busy) {
    researchBusy = busy;
    $("account-button").disabled = busy;
    $("history-button").disabled = busy;
  }

  function init(options) {
    callbacks = options;
    try {
      const stored = JSON.parse(sessionStorage.getItem(storageKey));
      if (typeof stored?.accessToken === "string" && stored.accessToken && typeof stored.user?.id === "string" && typeof stored.user.email === "string" && Number.isFinite(stored.expiresAt)) {
        session = stored;
        currentSession();
        if (session) expiryTimer = setTimeout(() => endSession(true), Math.max(0, Math.min(session.expiresAt - Date.now(), 86400000)));
      } else sessionStorage.removeItem(storageKey);
    } catch { /* 禁用存储或损坏的数据不妨碍匿名使用。 */ }
    updateAccount();
    $("account-button").addEventListener("click", () => openAccount());
    $("close-account").addEventListener("click", () => $("account-dialog").close());
    $("account-dialog").addEventListener("cancel", (event) => { if (authBusy) event.preventDefault(); });
    $("account-dialog").addEventListener("close", () => { $("auth-password").value = ""; $("auth-confirm").value = ""; });
    $("auth-login").addEventListener("click", () => setAuthMode("login"));
    $("auth-register").addEventListener("click", () => setAuthMode("register"));
    $("auth-form").addEventListener("submit", submitAuth);
    $("continue-guest").addEventListener("click", () => { needsLogin = false; notice(); $("account-dialog").close(); });
    $("logout-button").addEventListener("click", () => { endSession(); $("account-dialog").close(); callbacks.onNotice?.("已退出登录，个人历史仍保存在原账号下。"); });
    $("history-button").addEventListener("click", openHistory);
    $("account-history").addEventListener("click", openHistory);
    $("close-history").addEventListener("click", () => $("history-dialog").close());
    $("history-dialog").addEventListener("close", () => { revision++; clearTimeout(pollTimer); stopDetail(); });
    $("refresh-history").addEventListener("click", () => loadHistory());
    $("history-back").addEventListener("click", () => { selectedTask = null; loadHistory(); });
    $("history-prev").addEventListener("click", () => { if (page > 1) { page--; loadHistory(); } });
    $("history-next").addEventListener("click", () => { if (page * pageSize < total) { page++; loadHistory(); } });
    $("open-saved-report").addEventListener("click", openReport);
    $("cancel-task").addEventListener("click", cancelSelectedTask);
    $("reuse-question").addEventListener("click", () => {
      if (!selectedTask || historyBusy) return;
      const task = selectedTask;
      $("history-dialog").close();
      callbacks.onReuseQuestion?.(task);
    });
    document.addEventListener("visibilitychange", () => {
      clearTimeout(pollTimer);
      if (!document.hidden && $("history-dialog").open && !historyBusy) loadHistory(true);
    });
    window.addEventListener("pagehide", stopDetail);
  }

  return { init, api, setBusy, openHistory, followTask, canStream, setEventSupport: (value) => { eventSupport = Boolean(value); }, currentUser: () => currentSession()?.user || null };
})();
