"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const {readCarrier} = require("../site/read-cell.js");

function cell(capacity = 128, append = false) {
  const used = 39;
  const body = new Uint8Array(capacity + (append ? used - 16 : 0));
  const view = new DataView(body.buffer);
  view.setUint32(0, 0x4e464331);
  view.setUint32(4, 7);
  view.setUint32(8, used);
  view.setUint16(12, 1);
  body[16] = 2;
  view.setUint32(20, 1);
  view.setUint32(28, used - 32);
  body.fill(42, 32, used);
  body.fill(123, used);
  return body;
}

function response(body, length = body.length, capacity = 128) {
  return new Response(body, {headers: {"Content-Length": String(length), "X-App-Capacity": String(capacity)}});
}

test("all split boundaries and byte-at-a-time delivery preserve the used prefix", async () => {
  const body = cell();
  for (let split = 1; split < body.length; split++) {
    let received;
    const stream = new ReadableStream({start(controller) {
      controller.enqueue(body.slice(0, split));
      controller.enqueue(body.slice(split));
      controller.close();
    }});
    await readCarrier(response(stream, body.length), 128, 7, true, async prefix => { received = prefix.slice(); });
    assert.deepEqual(received, body.slice(0, 39));
  }
  let offset = 0, calls = 0;
  const stream = new ReadableStream({pull(controller) {
    if (offset === body.length) controller.close();
    else controller.enqueue(body.slice(offset, ++offset));
  }});
  await readCarrier(response(stream, body.length), 128, 7, true, async prefix => {
    calls++;
    assert.equal(prefix.length, 39);
  });
  assert.equal(calls, 1);
  assert.equal(offset, body.length);
});

test("prefix is delivered before EOF but completion waits for filler and sink", async () => {
  const body = cell();
  let controller, prefixReady, releaseSink, settled = false;
  const delivered = new Promise(resolve => { prefixReady = resolve; });
  const sink = new Promise(resolve => { releaseSink = resolve; });
  const stream = new ReadableStream({start(value) { controller = value; }});
  const run = readCarrier(response(stream, body.length), 128, 7, true, async prefix => {
    assert.deepEqual(prefix, body.slice(0, 39));
    prefixReady();
    await sink;
  }).then(value => { settled = true; return value; });
  controller.enqueue(body.slice(0, 39));
  await delivered;
  assert.equal(settled, false);
  releaseSink();
  await Promise.resolve();
  assert.equal(settled, false);
  controller.enqueue(body.slice(39));
  controller.close();
  assert.equal(await run, 89);
});

test("buffered control does not deliver a prefix before EOF", async () => {
  const body = cell();
  let controller, calls = 0;
  const stream = new ReadableStream({start(value) { controller = value; }});
  const run = readCarrier(response(stream, body.length), 128, 7, false, async value => {
    calls++;
    assert.deepEqual(value, body);
  });
  controller.enqueue(body.slice(0, 39));
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(calls, 0);
  controller.enqueue(body.slice(39));
  controller.close();
  assert.equal(await run, 0);
  assert.equal(calls, 1);
});

test("truncated filler invalidates a response after early delivery", async () => {
  let calls = 0;
  await assert.rejects(readCarrier(response(cell().slice(0, 39), 128), 128, 7, true, async () => { calls++; }), /truncated/);
  assert.equal(calls, 1);
});

test("malformed headers and frames never reach the sink", async () => {
  const mutations = [
    body => { body[0] = 0; },
    body => { body[7] = 8; },
    body => { body[14] = 1; },
    body => { body[11] = 255; },
    body => { body[13] = 2; },
    body => { body[16] = 9; },
    body => { body[17] = 1; },
    body => { body[31] = 255; },
  ];
  for (const mutate of mutations) {
    for (const streaming of [false, true]) {
      const body = cell();
      mutate(body);
      let calls = 0;
      await assert.rejects(readCarrier(response(body), 128, 7, streaming, async () => { calls++; }));
      assert.equal(calls, 0);
    }
  }
});

test("invalid envelopes, overflow and short header are rejected", async () => {
  const encoded = response(cell());
  encoded.headers.set("Content-Encoding", "gzip");
  const bad = [encoded, response(cell(), 262145), response(cell(), 128, 256), response(cell(), 127), response(cell(), 129), response(cell().slice(0, 9), 128), response(new Uint8Array(129), 128)];
  for (const value of bad) {
    await assert.rejects(readCarrier(value, 128, 7, true, async () => {}));
  }
});

test("append ablation preserves its declared fixed base", async () => {
  for (const streaming of [false, true]) {
    const body = cell(128, true);
    await readCarrier(response(body), 128, 7, streaming, async value => {
      assert.deepEqual(value, streaming ? body.slice(0, 39) : body);
    });
  }
});

test("sink failure cancels the unread response", async () => {
  let cancelled = false;
  const stream = new ReadableStream({
    start(controller) { controller.enqueue(cell().slice(0, 39)); },
    cancel() { cancelled = true; },
  });
  await assert.rejects(readCarrier(response(stream, 128), 128, 7, true, async () => { throw new Error("sink failed"); }), /sink failed/);
  assert.equal(cancelled, true);
});
