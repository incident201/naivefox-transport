"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const {WakeLatch, activityState, runLifecycle} = require("../site/lifecycle.js");

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
