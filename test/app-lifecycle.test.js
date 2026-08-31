"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");
const {webcrypto} = require("node:crypto");

async function application(commit, failAction = false, realtime = false) {
  const profile = {rounds: 20, down: 65536, duplex: false, streaming: false, paint_every: 2, commit,
    slots: [8192, 8192, 8192, 8192, 32768, 32768, ...Array(12).fill(65536), 8192, 8192]};
  const reader = fs.readFileSync(path.join(__dirname, "../site/read-cell.js"), "utf8");
  const lifecycle = fs.readFileSync(path.join(__dirname, "../site/lifecycle.js"), "utf8");
  const source = fs.readFileSync(path.join(__dirname, "../site/app.js"), "utf8")
    .replace("__NFC_PROFILE__", JSON.stringify(profile)).replace("__NFC_READER__", reader).replace("__NFC_LIFECYCLE__", lifecycle);
  const events = [], nodes = new Map();
  const connected = Promise.withResolvers();
  let realtimeSocket;
  let sequence = 0, uploads = 0, downloads = 0, upSequence = 0;
  const context = {
    location: {hash: realtime ? "#hold=1&realtime" : "#hold=1", href: "https://example.test/"}, URLSearchParams, URL, Uint8Array, DataView, crypto: webcrypto,
    WebSocket: class {
      constructor(url, protocol) {
        assert.equal(String(url), "wss://example.test/api/realtime");
        assert.equal(protocol, "nfc1.hybrid.v1");
        events.push("websocket");realtimeSocket=this;
        queueMicrotask(()=>this.onopen());
      }
      close() {const close=this.onclose;this.onclose=null;if(close)close();}
    },
    setInterval(callback, delay) {assert.equal(delay, 25000);connected.resolve();return 1;},
    clearInterval() {},
    document: {readyState: "complete", getElementById(id) {
      if (!nodes.has(id)) nodes.set(id, {addEventListener() {}, getContext() { return {fillRect() {}}; }});
      return nodes.get(id);
    }},
    requestAnimationFrame(callback) { events.push("paint"); queueMicrotask(() => callback(0)); },
    async fetch(endpoint, options) {
      events.push(endpoint);
      if (options.method === "POST") {
        uploads += options.body.length;
        assert.equal(new DataView(options.body.buffer).getUint32(4), upSequence++);
        if (endpoint !== "/api/action") return new Response(null, {status: 204});
      }
      if (failAction && endpoint === "/api/action") throw new Error("failed confirmation");
      const capacity = endpoint === "/api/action" ? 4096 : endpoint.endsWith("/brief") ? 8192 : endpoint.endsWith("/state") ? 32768 : 65536;
      const body = new Uint8Array(capacity);
      const view = new DataView(body.buffer);
      view.setUint32(0, 0x4e464331); view.setUint32(4, sequence++); view.setUint32(8, 16);
      downloads += capacity;
      return new Response(body, {headers: {"Content-Length": String(capacity), "X-App-Capacity": String(capacity)}});
    },
  };
  context.window = context;
  vm.runInNewContext(source, context);
  const running = context.__NFC_RUN__();
  if(realtime)await connected.promise;else await running;
  return {context, events, uploads, downloads, running, realtimeSocket};
}

test("terminal confirmation follows final paint and preserves the declared budget", async () => {
  const {context, events, uploads, downloads} = await application(true);
  assert.equal(context.__NFC_DONE__, true);
  assert.equal(context.__NFC_ACTION_DONE__, true);
  assert.equal(context.__NFC_ROUND__, 20);
  assert.equal(uploads, 86016);
  assert.equal(downloads, 905216);
  assert.equal(events.filter(value => value.startsWith("/")).length, 41);
  assert.deepEqual(events.slice(-2), ["paint", "/api/action"]);
});

test("control profile does not gain an undeclared confirmation", async () => {
  const {context, events, uploads, downloads} = await application(false);
  assert.equal(context.__NFC_DONE__, true);
  assert.equal(context.__NFC_ACTION_DONE__, false);
  assert.equal(events.includes("/api/action"), false);
  assert.equal(uploads, 81920);
  assert.equal(downloads, 901120);
});

test("failed terminal confirmation never marks a completed application", async () => {
  const {context} = await application(true, true);
  assert.equal(context.__NFC_DONE__, false);
  assert.equal(context.__NFC_ACTION_DONE__, false);
  assert.equal(context.__NFC_ERROR__, "application-or-transport");
});

test("realtime opens only after all twenty complete startup exchanges and final paint", async () => {
  const result = await application(false, false, true);
  try {
    assert.equal(result.context.__NFC_PHASE__, "realtime");
    assert.equal(result.context.__NFC_DONE__, true);
    assert.equal(result.context.__NFC_ALIVE__, true);
    assert.equal(result.context.__NFC_ROUND__, 20);
    assert.equal(result.uploads, 81920);
    assert.equal(result.downloads, 901120);
    assert.deepEqual(result.events.slice(-3), ["/api/events/brief", "paint", "websocket"]);
    assert.equal(result.events.filter(value => value.startsWith("/")).length, 40);
  } finally { result.realtimeSocket.close();await result.running; }
});
