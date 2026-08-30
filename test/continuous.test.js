"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const {DeliveryFence, shouldDeferDelivery, WakeLatch, activityState, runLifecycle, activeExchange, activeDuplex} = require("../site/lifecycle.js");

test("interactive-only combined responses do not change upload or mixed states",()=>{
  for(const state of ["interactive","download","upload","mixed","bulk"]){
    assert.equal(activeDuplex({},state),false);
    assert.equal(activeDuplex({live_duplex:true},state),true);
    assert.equal(activeDuplex({bulk_duplex:true},state),state==="bulk");
    assert.equal(activeDuplex({bulk_duplex:true,interactive_duplex:true},state),state==="bulk"||state==="interactive");
  }
});

test("selective deferred delivery leaves small states, startup and frames acknowledged", () => {
  for(const phase of ["startup","idle","interactive","upload","mixed","bulk"]){
    assert.equal(shouldDeferDelivery({},phase,false),false);
    assert.equal(shouldDeferDelivery({deferred_ack:true},phase,false),phase!=="startup");
    assert.equal(shouldDeferDelivery({deferred_ack:true,bulk_ack_only:true},phase,false),phase==="bulk");
    assert.equal(shouldDeferDelivery({deferred_ack:true,bulk_ack_only:true},phase,true),false);
  }
});

test("deferred delivery permits one cell until the next ordered command reply", () => {
  const fence=new DeliveryFence(),sent=[];
  const socket={send:body=>sent.push(body)};
  fence.send(socket,new Uint8Array(262145));
  assert.throws(()=>fence.send(socket,new Uint8Array(17)),/bound/);
  assert.equal(sent.length,1);
  fence.acknowledge();
  fence.send(socket,new Uint8Array(17));
  assert.equal(sent.length,2);
  assert.equal(fence.pending,true);
});

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

test("selective bulk duplex retains capacity and avoids only the bulk GET", async () => {
  const reply={status:200};let sent=0;
  const hint=await activeExchange({
    send:async(capacity,path)=>{sent++;assert.equal(capacity,16384);assert.equal(path,"/api/sync/bulk");return reply;},
    fetch:()=>assert.fail("extra GET"),
    receive:async(response,capacity)=>{assert.equal(response,reply);assert.equal(capacity,262144);return "download";},
  },"bulk",true);
  assert.equal(sent,1);assert.equal(hint,"download");
});

test("failed active exchange cannot silently consume a downstream slot", async () => {
  for (const duplex of [false, true]) {
    await assert.rejects(activeExchange({send: async () => ({status: 400}), fetch() { assert.fail(); }, receive() { assert.fail(); }}, "interactive", duplex), /active sync/);
  }
});

test("one bulk transaction preserves the four-slot download lease budget", async () => {
  let up = 0, down = 0, active = true, polls = 0, exchanges = 0;
  await runLifecycle({
    alive: () => active,
    pressure: async () => ({bytes: 0, controls: 0}),
    state() {},
    idle: async () => { if (++polls === 2) active = false; return "download"; },
    exchange: async state => {
      assert.equal(state, "bulk"); exchanges++;
      return activeExchange({
        send: async (capacity, path) => { up += capacity; assert.equal(path, "/api/sync/bulk"); return {status: 204}; },
        fetch: async path => { assert.equal(path, "/api/data/bulk"); return {}; },
        receive: async (_, capacity) => { down += capacity; return "idle"; },
      }, state, false);
    },
  }, new WakeLatch(), 4, true);
  assert.equal(exchanges, 1);
  assert.equal(up, 4 * 4096);
  assert.equal(down, 4 * 65536);
});

test("bulk profile does not coalesce upload, mixed or interactive leases", async () => {
  for (const [bytes, remote, expected] of [[32768, "idle", "upload"], [32768, "download", "mixed"], [1, "idle", "interactive"]]) {
    let active = true, calls = 0, pressureCalls = 0;
    await runLifecycle({
      alive: () => active,
      pressure: async () => { pressureCalls++; return {bytes: pressureCalls === 1 ? 0 : bytes, controls: 0}; },
      state() {},
      idle: async () => remote,
      exchange: async state => { assert.equal(state, expected); if (++calls === 4) active = false; return "idle"; },
    }, new WakeLatch(), 4, true);
    assert.equal(calls, 4);
  }
});

test("per-state short leases change only the selected state", async () => {
  for(const short of ["interactive","upload"]){
    for(const [initialBytes,remote,expected] of [[1,"idle","interactive"],[32768,"idle","upload"],[32768,"download","mixed"]]){
      let active=true,calls=0,queries=0,polls=0,bytes=initialBytes;
      await runLifecycle({
        alive:()=>active,
        pressure:async()=>({bytes:++queries===1?0:bytes,controls:0}),state(){},
        idle:async()=>{if(++polls===2)active=false;return remote;},
        exchange:async(state)=>{assert.equal(state,expected);calls++;bytes=0;return "idle";},
      },new WakeLatch(),4,true,short);
      assert.equal(calls,expected===short?1:4);
    }
  }
  await assert.rejects(runLifecycle({alive:()=>false},new WakeLatch(),4,true,"mixed"),/short state/);
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
