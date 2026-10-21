"use strict";

// Fetch streaming keeps JWT in the Authorization header. No token in URLs.
const TaskEvents = (() => {
  const terminal = (status) => ["succeeded", "failed", "cancelled"].includes(status);

  function parser(onEvent, onRetry = () => {}) {
    let buffer = "", data = [], type = "", id;
    function line(value) {
      if (!value) {
        if (data.length) onEvent({ type: type || "message", id, data: data.join("\n") });
        data = []; type = ""; id = undefined;
        return;
      }
      if (value.startsWith(":")) return;
      const colon = value.indexOf(":");
      const field = colon < 0 ? value : value.slice(0, colon);
      let text = colon < 0 ? "" : value.slice(colon + 1);
      if (text.startsWith(" ")) text = text.slice(1);
      if (field === "data") data.push(text);
      else if (field === "event") type = text;
      else if (field === "id" && !text.includes("\0")) id = text;
      else if (field === "retry" && /^\d+$/.test(text)) onRetry(Number(text));
    }
    return (chunk) => {
      buffer += chunk;
      if (buffer.length + data.join("\n").length > 65536) throw new Error("事件过大");
      let match;
      while ((match = /[\r\n]/.exec(buffer))) {
        const end = match.index;
        if (buffer[end] === "\r" && end === buffer.length - 1) break;
        const width = buffer[end] === "\r" && buffer[end + 1] === "\n" ? 2 : 1;
        line(buffer.slice(0, end));
        buffer = buffer.slice(end + width);
      }
    };
  }

  function follow(id, { getToken, onEvent, onConnection = () => {}, onAuthError = () => {}, onFatal = () => {}, retryMS = 2000, idleMS = 45000 }) {
    let stopped = false, cursor = null, controller, reader, idleTimer, retryTimer, wake;
    let delay = retryMS, failures = 0;
    const stop = () => {
      stopped = true;
      controller?.abort();
      reader?.cancel().catch(() => {});
      clearTimeout(idleTimer); clearTimeout(retryTimer);
      wake?.();
    };
    const pause = (ms) => new Promise((resolve) => { wake = resolve; retryTimer = setTimeout(resolve, ms); });
    const touch = () => {
      clearTimeout(idleTimer);
      idleTimer = setTimeout(() => controller?.abort(), idleMS);
    };
    async function run() {
      while (!stopped) {
        const token = getToken();
        if (!token) { stop(); return; }
        controller = new AbortController();
        onConnection(failures ? "reconnecting" : "connecting");
        try {
          touch();
          const headers = { Accept: "text/event-stream", Authorization: `Bearer ${token}` };
          if (cursor !== null) headers["Last-Event-ID"] = cursor;
          const response = await fetch(`/api/tasks/${encodeURIComponent(id)}/events`, { headers, signal: controller.signal, cache: "no-store" });
          if (stopped || getToken() !== token) { stop(); return; }
          if (response.status === 401) { onAuthError(); stop(); return; }
          if ([400, 403, 404].includes(response.status)) { onFatal("任务进度不可访问，请重新登录或从历史中查看。"); stop(); return; }
          if (response.status === 409) cursor = null; // Restore from a fresh consistent snapshot.
          if (response.status === 429) delay = Math.min(15000, Math.max(delay, Number(response.headers.get("Retry-After")) * 1000 || 0));
          if (!response.ok || !response.headers.get("Content-Type")?.includes("text/event-stream") || !response.body) throw new Error("stream unavailable");
          reader = response.body.getReader();
          const decoder = new TextDecoder();
          const feed = parser((event) => {
            if (stopped || getToken() !== token) return;
            if (event.type === "stream_error") throw new Error("stream interrupted");
            if (!/^\d+$/.test(event.id || "")) throw new Error("invalid event id");
            const sequence = Number(event.id);
            if (!Number.isSafeInteger(sequence)) throw new Error("invalid sequence");
            if (event.type !== "snapshot" && cursor !== null && sequence <= Number(cursor)) return;
            const data = JSON.parse(event.data);
            if (event.type === "snapshot" && (!data.task || data.last_event_id !== sequence)) throw new Error("invalid snapshot");
            onEvent({ ...event, data });
            cursor = event.id;
            failures = 0;
            onConnection("live");
            if (terminal(event.type === "snapshot" ? data.task.status : data.status)) stop();
          }, (ms) => { delay = Math.min(15000, Math.max(500, ms)); });
          while (!stopped) {
            const { value, done } = await reader.read();
            if (done) break;
            touch();
            feed(decoder.decode(value, { stream: true }));
          }
          // An incomplete final frame is deliberately discarded, then replayed.
        } catch { /* Transport failures reconnect the existing task only. */ }
        finally {
          clearTimeout(idleTimer);
          try { await reader?.cancel(); } catch { /* already closed */ }
          reader = null;
          controller.abort();
        }
        if (!stopped) {
          failures++;
          onConnection("reconnecting");
          await pause(Math.min(15000, delay * 2 ** Math.min(failures - 1, 3)));
        }
      }
    }
    void run();
    return stop;
  }
  return { parser, follow, terminal };
})();

const TaskProgress = (() => {
  function render(target, task, progress) {
    target.replaceChildren();
    target.hidden = false;
    const steps = task.mode === "chat" ? [["chat", "生成回答"]] : [["planner", "规划问题"], ["search", "检索资料"], ["writer", "撰写报告"]];
    const current = steps.findIndex(([stage]) => stage === progress?.stage);
    for (const [index, [stage, label]] of steps.entries()) {
      const item = document.createElement("li");
      let detail = "等待执行", status = "pending";
      if (task.status === "succeeded" || index < current) { detail = "已完成"; status = "done"; }
      else if (index === current) {
        detail = stage === "search" ? `已结束 ${progress.completed} / ${progress.total} 项搜索` : progress.completed === progress.total ? "已完成" : "进行中";
        status = progress.completed === progress.total ? "done" : "active";
      }
      if (task.status === "queued") { detail = "等待执行"; status = "pending"; }
      if (TaskEvents.terminal(task.status) && task.status !== "succeeded" && status !== "done") { detail = task.status === "cancelled" ? "已停止" : "未完成"; status = "pending"; }
      item.className = `progress-step ${status}`;
      const title = document.createElement("strong"), text = document.createElement("span");
      title.textContent = label; text.textContent = detail;
      item.append(title, text); target.append(item);
    }
  }
  return { render };
})();
