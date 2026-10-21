import { test } from "node:test";
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import { runInNewContext } from "node:vm";

const source = await readFile(new URL("../assets/events.js", import.meta.url), "utf8");
function events(fetch) {
  return runInNewContext(`${source}\nTaskEvents`, { fetch, TextDecoder, AbortController, setTimeout, clearTimeout });
}
async function until(check) {
  for (let i = 0; i < 100; i++) { if (check()) return; await new Promise((resolve) => setTimeout(resolve, 10)); }
  assert.ok(check());
}

test("SSE parser handles CRLF split, comments, multiline data and incomplete tails", () => {
  const received = [], retries = [];
  const parse = events().parser((event) => received.push(event), (retry) => retries.push(retry));
  const wire = ": heartbeat\r\nretry: 1500\r\n\r\nid: 4\r\nevent: search_progress\r\ndata: 第一行\r\ndata: 第二行\r\n\r\nid: 5\ndata: incomplete";
  for (const char of wire) parse(char);
  assert.equal(received.length, 1);
  assert.equal(received[0].data, "第一行\n第二行");
  assert.equal(received[0].id, "4");
  assert.deepEqual(retries, [1500]);
});

test("reconnect sends Last-Event-ID, deduplicates replay and decodes split UTF-8", async () => {
  const requests = [], received = [];
  const api = events(async (url, options) => {
    requests.push({ url, ...options });
    const text = requests.length === 1
      ? 'id: 1\nevent: running\ndata: {"status":"running","text":"中文"}\n\n'
      : 'id: 1\nevent: running\ndata: {"status":"running"}\n\nid: 2\nevent: succeeded\ndata: {"status":"succeeded"}\n\n';
    const bytes = new TextEncoder().encode(text);
    return new Response(new ReadableStream({ start(c) { for (const byte of bytes) c.enqueue(Uint8Array.of(byte)); c.close(); } }), { headers: { "Content-Type": "text/event-stream" } });
  });
  const stop = api.follow("task", { getToken: () => "secret", retryMS: 1, onEvent: (e) => received.push(e) });
  await until(() => received.length === 2);
  stop();
  assert.equal(received[0].data.text, "中文");
  assert.equal(requests[1].headers["Last-Event-ID"], "1");
  assert.equal(requests[1].headers.Authorization, "Bearer secret");
  assert.equal(requests.length, 2);
});

test("401 ends authentication; idle timeout and explicit stop release the reader", async () => {
  let auth = 0;
  events(async () => new Response("", { status: 401 })).follow("t", { getToken: () => "token", onEvent() {}, onAuthError() { auth++; } });
  await until(() => auth === 1);
  let cancelled = 0;
  const api = events(async (_, { signal }) => new Response(new ReadableStream({
    start(c) { signal.addEventListener("abort", () => c.error(new Error("aborted"))); },
    cancel() { cancelled++; },
  }), { headers: { "Content-Type": "text/event-stream" } }));
  let reconnects = 0;
  const stop = api.follow("t", { getToken: () => "token", onEvent() {}, retryMS: 1, idleMS: 10, onConnection(s) { if (s === "reconnecting") reconnects++; } });
  await until(() => reconnects > 0);
  stop();
  assert.ok(reconnects > 0);
});
