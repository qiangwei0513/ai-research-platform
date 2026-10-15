"use strict";

// 页面状态只留在内存中；所有模型调用都通过同源 Go 接口完成。
const $ = (id) => document.getElementById(id);
const form = $("research-form");
const queryInput = $("query");
const tabs = [...document.querySelectorAll("[data-tab]")];
const state = { busy: false, result: null, query: "", mode: "research", startedAt: 0 };
let toastTimer;

function currentMode() {
  return form.elements.mode.value;
}

function updateInput() {
  const length = Array.from(queryInput.value.trim()).length;
  $("query-count").textContent = `${length} / 5000`;
  $("query-count").classList.toggle("over-limit", length > 5000);
  queryInput.setCustomValidity(length > 5000 ? "问题不能超过 5000 个字符。" : "");
  $("form-error").hidden = true;
}

function updateMode() {
  const research = currentMode() === "research";
  $("submit-label").textContent = research ? "生成研究报告" : "发送问题";
  $("mode-description").textContent = research
    ? "规划搜索，交叉整理资料，生成带来源的报告。"
    : "直接回答，必要时搜索。每次提问独立处理。";
}

function setView(view) {
  for (const name of ["empty", "waiting", "error", "success"]) {
    $(`${name}-state`).hidden = name !== view;
  }
  $("result-placeholder").hidden = view !== "empty";
  $("result-actions").hidden = view !== "success";
  $("result-card").setAttribute("aria-busy", String(view === "waiting"));
}

function setBusy(busy) {
  state.busy = busy;
  queryInput.disabled = busy;
  $("mode-switch").disabled = busy;
  $("submit-button").disabled = busy;
  document.querySelectorAll("[data-example]").forEach((button) => { button.disabled = busy; });
  if (busy) $("submit-label").textContent = state.mode === "research" ? "正在研究…" : "正在回答…";
  else updateMode();
}

function notify(message) {
  clearTimeout(toastTimer);
  $("toast").textContent = message;
  $("toast").hidden = false;
  toastTimer = setTimeout(() => { $("toast").hidden = true; }, 3200);
}

function announce(message) {
  $("live-status").textContent = message;
}

function formatElapsed(milliseconds) {
  const seconds = Math.floor(milliseconds / 1000);
  return `${String(Math.floor(seconds / 60)).padStart(2, "0")}:${String(seconds % 60).padStart(2, "0")}`;
}

function safeURL(value) {
  if (typeof value !== "string") return null;
  try {
    const url = new URL(value);
    if (!["https:", "http:"].includes(url.protocol) || url.username || url.password) return null;
    return url;
  } catch { return null; }
}

function element(tag, className, content) {
  const node = document.createElement(tag);
  if (className) node.className = className;
  if (typeof content === "string") node.textContent = content;
  return node;
}

function renderMarkdown(markdown) {
  const panel = $("panel-report");
  // Markdown 转换器不负责安全过滤；所有输出都先经过严格白名单清洗。
  // 去掉图片、样式、事件、表单，避免自动加载第三方资源。
  const fragment = DOMPurify.sanitize(marked.parse(markdown, { gfm: true, breaks: false }), {
    RETURN_DOM_FRAGMENT: true,
    ALLOWED_TAGS: ["h1", "h2", "h3", "h4", "h5", "h6", "p", "br", "hr", "strong", "em", "del", "s", "ul", "ol", "li", "blockquote", "pre", "code", "table", "thead", "tbody", "tr", "th", "td", "a"],
    ALLOWED_ATTR: ["href", "title", "start"],
    ALLOW_DATA_ATTR: false,
    ALLOW_ARIA_ATTR: false,
  });
  fragment.querySelectorAll("a").forEach((link) => {
    const url = safeURL(link.getAttribute("href"));
    if (!url) {
      link.replaceWith(...link.childNodes);
      return;
    }
    link.href = url.href;
    link.target = "_blank";
    link.rel = "noopener noreferrer";
  });
  panel.replaceChildren(fragment);
}

function renderPlan(data) {
  const panel = $("panel-plan");
  panel.replaceChildren();
  const goal = element("div", "plan-goal");
  goal.append(element("h3", "", "研究目标"), element("p", "", data.plan.goal || "未返回研究目标。"));
  panel.append(goal);
  const tasks = Array.isArray(data.search?.tasks) ? data.search.tasks : [];
  data.plan.searches.forEach((item, index) => {
    const task = tasks[index];
    const card = element("article", "plan-item");
    const header = element("div", "plan-item-header");
    header.append(element("h3", "", `搜索 ${String(index + 1).padStart(2, "0")}`));
    if (task) {
      const status = ["success", "empty", "failed"].includes(task.status) ? task.status : "unknown";
      header.append(element("span", `task-status ${status}`, { success: "已获得资料", empty: "暂无资料", failed: "搜索未完成" }[status] || "状态未知"));
    }
    card.append(header, element("p", "plan-query", item.query), element("p", "plan-reason", item.reason));
    if (task?.error) card.append(element("p", "task-error", "本次搜索未能完成，请结合其他来源阅读报告。"));
    if (task?.summary) {
      const details = element("details", "task-summary");
      details.append(element("summary", "", "查看搜索摘要"), element("p", "", task.summary));
      card.append(details);
    }
    panel.append(card);
  });
}

function renderSources(sources) {
  const panel = $("panel-sources");
  panel.replaceChildren(element("p", "source-intro", "以下为搜索返回的来源与片段。点击标题可以前往原始网页，核对完整内容。"));
  sources.forEach((source, index) => {
    const card = element("article", "source-card");
    const content = element("div");
    const heading = element("h3");
    const url = safeURL(source.url);
    const title = typeof source.title === "string" && source.title.trim() ? source.title : `来源 ${index + 1}`;
    if (url) {
      const link = element("a", "source-link", title);
      link.href = url.href;
      link.target = "_blank";
      link.rel = "noopener noreferrer";
      heading.append(link);
    } else heading.textContent = title;
    content.append(heading, element("p", "source-domain", url ? url.hostname : "来源地址不可用"));
    const snippets = Array.isArray(source.snippets) ? source.snippets.filter((value) => typeof value === "string" && value.trim()) : [];
    if (snippets.length) {
      const details = element("details");
      details.append(element("summary", "", `查看搜索片段 · ${snippets.length} 条`));
      snippets.forEach((snippet) => details.append(element("p", "", snippet)));
      content.append(details);
    }
    card.append(element("span", "source-number", String(index + 1).padStart(2, "0")), content);
    panel.append(card);
  });
  if (!sources.length) panel.append(element("p", "source-intro", "本次响应没有独立的参考来源列表。"));
}

function selectTab(name, focus = false) {
  tabs.forEach((tab) => {
    const selected = tab.dataset.tab === name;
    tab.setAttribute("aria-selected", String(selected));
    tab.tabIndex = selected ? 0 : -1;
    $(`panel-${tab.dataset.tab}`).hidden = !selected;
    if (selected && focus) tab.focus();
  });
}

function showResult(data) {
  if (!data || typeof data.result !== "string" || !data.result.trim()) throw new Error("服务没有返回有效内容，请稍后重试。");
  const research = state.mode === "research";
  const hasPlan = research && data.plan && Array.isArray(data.plan.searches);
  const hasSources = research && Array.isArray(data.search?.sources);
  const sources = hasSources ? data.search.sources.filter((item) => item && typeof item === "object") : [];
  renderMarkdown(data.result);
  $("panel-plan").replaceChildren();
  $("panel-sources").replaceChildren();
  if (hasPlan) renderPlan(data);
  if (hasSources) renderSources(sources);
  $("tab-plan").hidden = !hasPlan;
  $("tab-sources").hidden = !hasSources;
  $("tab-report").textContent = research ? "研究报告" : "回答内容";
  $("sources-count").textContent = String(sources.length);
  $("result-query").textContent = state.query;
  $("result-mode").textContent = research ? "深入研究" : "快速问答";
  $("result-duration").textContent = `用时 ${formatElapsed(Date.now() - state.startedAt)}`;
  $("result-source-count").textContent = hasSources ? `${sources.length} 个参考来源` : "";
  $("partial-notice").hidden = data.search?.status !== "partial";
  state.result = data.result;
  selectTab("report");
  setView("success");
  announce(research ? "研究已完成，可以查看报告、计划和参考来源。" : "回答已生成。");
}

function responseError(response, data) {
  if (response.status === 429) {
    const wait = Number(response.headers.get("Retry-After"));
    const suffix = Number.isFinite(wait) && wait > 0 ? `请约 ${Math.min(Math.ceil(wait), 3600)} 秒后再试。` : "请稍后再试。";
    return data?.code === "rate_limited" ? `当前请求次数已达到上限，${suffix}` : `当前使用人数较多，${suffix}`;
  }
  if (response.status === 504) return "本次处理超时了。试试缩小研究范围，或稍后重新尝试。";
  if (data?.code === "no_sources") return "这次没有找到可用的搜索资料。试试更具体的关键词，或换一种提问方式。";
  if (data?.code === "search_failed") return "搜索服务暂时未能取得资料，请稍后重试。";
  if (response.status === 413) return "问题内容过长，请精简后重新发送。";
  if (response.status === 400) return typeof data?.error === "string" ? data.error : "问题格式不正确，请修改后重试。";
  return "研究服务暂时无法完成请求，请稍后再试。";
}

async function submitResearch(event) {
  event.preventDefault();
  if (state.busy) return;
  const query = queryInput.value.trim();
  if (!query || Array.from(query).length > 5000) {
    $("form-error").textContent = !query ? "先写下一个你想了解的问题吧。" : "问题不能超过 5000 个字符，请精简后再试。";
    $("form-error").hidden = false;
    queryInput.focus();
    return;
  }
  state.query = query;
  state.mode = currentMode();
  state.result = null;
  state.startedAt = Date.now();
  $("form-error").hidden = true;
  $("waiting-title").textContent = state.mode === "research" ? "正在研究你的问题" : "正在整理回答";
  $("waiting-description").textContent = state.mode === "research" ? "报告、计划和参考来源将在完成后一起呈现。" : "答案将在处理完成后呈现，请稍等片刻。";
  $("waiting-note").textContent = state.mode === "research" ? "深入研究最多等待约 5 分钟，请保持页面打开。" : "快速问答最多等待约 2 分钟，请保持页面打开。";
  $("elapsed").textContent = "00:00";
  setBusy(true);
  setView("waiting");
  announce($("waiting-title").textContent);
  if (window.matchMedia("(max-width: 800px)").matches) $("result-card").scrollIntoView({ behavior: "auto", block: "start" });
  const timer = setInterval(() => { $("elapsed").textContent = formatElapsed(Date.now() - state.startedAt); }, 1000);
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), state.mode === "research" ? 320000 : 140000);
  try {
    const response = await fetch("/api/research", {
      method: "POST",
      headers: { "Content-Type": "application/json", "Accept": "application/json" },
      body: JSON.stringify({ query, mode: state.mode }),
      signal: controller.signal,
      cache: "no-store",
    });
    let data;
    try { data = await response.json(); } catch { data = null; }
    if (!response.ok) throw new Error(responseError(response, data));
    showResult(data);
  } catch (error) {
    let message = error instanceof Error ? error.message : "请求失败，请稍后重试。";
    if (controller.signal.aborted) message = "等待时间过长，请稍后重新尝试，或缩小研究范围。";
    else if (error instanceof TypeError) message = "暂时连接不上服务，请检查网络后重试。";
    $("error-message").textContent = message;
    setView("error");
    announce(`请求未完成。${message}`);
  } finally {
    clearInterval(timer);
    clearTimeout(timeout);
    setBusy(false);
    if (!$("error-state").hidden) $("error-title").focus({ preventScroll: true });
  }
}

async function copyReport() {
  if (!state.result) return;
  try {
    if (navigator.clipboard && window.isSecureContext) await navigator.clipboard.writeText(state.result);
    else {
      const scratch = element("textarea", "sr-only");
      scratch.value = state.result;
      document.body.append(scratch);
      scratch.select();
      let copied;
      try { copied = document.execCommand("copy"); } finally { scratch.remove(); $("copy-button").focus(); }
      if (!copied) throw new Error("copy unavailable");
    }
    notify("已复制 Markdown，可粘贴到笔记或文档中。");
  } catch { notify("浏览器未允许复制，可以使用下载按钮保存报告。"); }
}

function downloadReport() {
  if (!state.result) return;
  const name = Array.from(state.query.replace(/[\\/:*?"<>|\u0000-\u001f]/g, "")).slice(0, 40).join("").trim() || "研究报告";
  const url = URL.createObjectURL(new Blob([state.result], { type: "text/markdown;charset=utf-8" }));
  const link = element("a");
  link.href = url;
  link.download = `${name}.md`;
  document.body.append(link);
  link.click();
  link.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
  notify("已发起下载，请查看浏览器下载列表。");
}

async function checkService() {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), 5000);
  try {
    const response = await fetch("/ping", { signal: controller.signal, cache: "no-store" });
    const data = await response.json();
    if (!response.ok || data.message !== "pong") throw new Error("unavailable");
    $("service-status").className = "service-status connected";
    $("service-label").textContent = "服务已连接";
    $("service-status").title = "网页已连接到服务入口；模型和搜索服务状态以实际请求为准。";
  } catch {
    $("service-status").className = "service-status unavailable";
    $("service-label").textContent = "服务暂不可达";
    $("service-status").title = "暂时无法连接服务入口，请检查网络或联系站点维护者。";
  } finally { clearTimeout(timer); }
}

form.addEventListener("submit", submitResearch);
queryInput.addEventListener("input", updateInput);
queryInput.addEventListener("keydown", (event) => {
  if ((event.metaKey || event.ctrlKey) && event.key === "Enter" && !event.isComposing) {
    event.preventDefault();
    form.requestSubmit();
  }
});
$("mode-switch").addEventListener("change", updateMode);
document.querySelectorAll("[data-example]").forEach((button) => button.addEventListener("click", () => {
  queryInput.value = button.dataset.example;
  updateInput();
  queryInput.focus();
}));
tabs.forEach((tab) => {
  tab.addEventListener("click", () => selectTab(tab.dataset.tab));
  tab.addEventListener("keydown", (event) => {
    const visible = tabs.filter((item) => !item.hidden);
    let index = visible.indexOf(tab);
    if (event.key === "ArrowRight") index = (index + 1) % visible.length;
    else if (event.key === "ArrowLeft") index = (index - 1 + visible.length) % visible.length;
    else if (event.key === "Home") index = 0;
    else if (event.key === "End") index = visible.length - 1;
    else return;
    event.preventDefault();
    selectTab(visible[index].dataset.tab, true);
  });
});
$("retry-button").addEventListener("click", () => form.requestSubmit());
$("copy-button").addEventListener("click", copyReport);
$("download-button").addEventListener("click", downloadReport);
$("reset-button").addEventListener("click", () => {
  state.result = null;
  state.query = "";
  queryInput.value = "";
  for (const name of ["report", "plan", "sources"]) $(`panel-${name}`).replaceChildren();
  setView("empty");
  updateInput();
  queryInput.focus();
  announce("可以开始新的研究。");
});
$("help-button").addEventListener("click", () => $("help-dialog").showModal());
$("close-help").addEventListener("click", () => $("help-dialog").close());
$("help-dialog").addEventListener("click", (event) => {
  if (event.target === $("help-dialog")) {
    const rect = $("help-dialog").getBoundingClientRect();
    if (event.clientX < rect.left || event.clientX > rect.right || event.clientY < rect.top || event.clientY > rect.bottom) $("help-dialog").close();
  }
});
window.addEventListener("online", checkService);
window.addEventListener("offline", () => {
  $("service-status").className = "service-status unavailable";
  $("service-label").textContent = "网络已断开";
});
$("shortcut-hint").textContent = /Mac|iPhone|iPad/.test(navigator.platform) ? "⌘ ↵" : "Ctrl ↵";
updateMode();
updateInput();
checkService();
