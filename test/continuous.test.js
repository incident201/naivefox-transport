"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const {WakeLatch, activityState, runLifecycle, activeExchange} = require("../site/lifecycle.js");

test("combined active exchanges retain exact capacities without a second HTTP request", async () => {
  for (const state of ["interactive", "download", "upload", "mixed"]) {
    for (const duplex of [false, true]) {
      const requests = [], reply = {status: duplex ? 200 : 204}, fetched = {};
      const uploading = state === "upload" || state === "mixed";
      const down = state === "download" || state === "mixed" ? 65536 : 8192;
      const result = await activeExchange({
        send: async (capacity, path) => { requests.push([capacity, path]); return reply; },
        fetch: async path => { requests.push(path); return fetched; },
        receive: async (response, capacity) => { assert.equal(response, duplex ? reply : fetched); assert.equal(capacity, down); return "idle"; },
      }, state, duplex);
      assert.equal(result, "idle");
      assert.equal(requests.length, duplex ? 1 : 2);
      assert.deepEqual(requests[0], [uploading ? 131072 : 4096, duplex ? "/api/exchange/" + state : uploading ? "/api/upload/chunk" : "/api/sync"]);
    }
  }
});

test("failed active exchange cannot silently consume a downstream slot", async () => {
  for (const duplex of [false, true]) {
    await assert.rejects(activeExchange({send: async () => ({status: 400}), fetch() { assert.fail(); }, receive() { assert.fail(); }}, "interactive", duplex), /active sync/);
  }
});

test("activity states select fixed classes, not a byte-exact response size", () => {
  assert.equal(activityState({bytes: 0, controls: 0}, "idle"), "idle");
  assert.equal(activityState({bytes: 0, controls: 1}, "idle"), "interactive");
  assert.equal(activityState({bytes: 1, controls: 0}, "idle"), "interactive");
  assert.equal(activityState({bytes: 32768, controls: 0}, "idle"), "upload");
  assert.equal(activityState({bytes: 0, controls: 0}, "download"), "download");
  assert.equal(activityState({bytes: 1000000, controls: 0}, "download"), "mixed");
});

test("wake latch preserves events before registration and releases cancelled waiters", async () => {
  const wake = new WakeLatch();
  const before = wake.version;
  wake.notify();
  await wake.after(before).promise;
  const waiting = wake.after(wake.version);
  assert.throws(() => wake.after(wake.version), /overlapping/);
  waiting.cancel();
  assert.equal(wake.pending, null);
  const next = wake.after(wake.version);
  wake.notify();
  await next.promise;
  assert.equal(wake.pending, null);
});

test("a lease has four slots even when pressure changes after its first slot", async () => {
  const states = [], slots = [];
  let active = true, bytes = 65536;
  await runLifecycle({
    alive: () => active,
    pressure: async () => ({bytes, controls: 0}),
    state: value => states.push(value),
    exchange: async value => { slots.push(value); bytes = 0; return "idle"; },
    idle: async () => { active = false; return "idle"; },
  }, new WakeLatch());
  assert.deepEqual(slots, Array(4).fill("upload"));
  assert.deepEqual(states, ["upload", "idle"]);
});

test("short leases keep two fixed slots and reject arbitrary per-packet counts", async () => {
  let active = true, bytes = 65536;
  const slots = [];
  await runLifecycle({
    alive: () => active,
    pressure: async () => ({bytes, controls: 0}),
    state() {},
    exchange: async state => { slots.push(state); bytes = 0; return "idle"; },
    idle: async () => { active = false; return "idle"; },
  }, new WakeLatch(), 2);
  assert.deepEqual(slots, ["upload", "upload"]);
  for (const length of [0, 1, 3, 2.5, 5]) await assert.rejects(runLifecycle({}, new WakeLatch(), length), /invalid activity lease/);
});

test("server work wakes idle and starts a lease without completing the application", async () => {
  let active = true, idle = 0, slots = 0;
  await runLifecycle({
    alive: () => active,
    pressure: async () => ({bytes: 0, controls: 0}),
    state() {},
    idle: async () => { if (++idle === 2) active = false; return idle === 1 ? "download" : "idle"; },
    exchange: async state => { assert.equal(state, "download"); slots++; return "idle"; },
  }, new WakeLatch());
  assert.equal(slots, 4);
  assert.equal(idle, 2);
});
