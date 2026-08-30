"use strict";

class DeliveryFence {
  constructor() { this.pending = false; }
  send(socket, body) {
    if (this.pending) throw new Error("unacknowledged delivery bound");
    this.pending = true;
    socket.send(body);
  }
  acknowledge() { this.pending = false; }
}

function shouldDeferDelivery(profile, phase, framing) {
  return !!(profile.deferred_ack && !framing && phase !== "startup" && (!profile.bulk_ack_only || phase === "bulk"));
}

class WakeLatch {
  constructor() { this.version = 0; this.pending = null; }
  notify() {
    this.version++;
    if (this.pending) { const resolve = this.pending; this.pending = null; resolve(); }
  }
  after(version) {
    let resolve;
    const promise = new Promise(value => { resolve = value; });
    if (version !== this.version) resolve();
    else {
      if (this.pending) throw new Error("overlapping wake wait");
      this.pending = resolve;
    }
    return {promise, cancel: () => { if (this.pending === resolve) this.pending = null; }};
  }
}

function activityState(pressure, remote) {
  if (pressure.bytes >= 32768) return remote === "download" ? "mixed" : "upload";
  if (remote === "download") return "download";
  if (pressure.bytes > 0 || pressure.controls > 0 || remote === "interactive") return "interactive";
  return "idle";
}

async function runLifecycle(io, wake, slots = 4, bulk = false, shortState = "") {
  if (![2, 4].includes(slots)) throw new Error("invalid activity lease");
  if (!["", "interactive", "upload"].includes(shortState)) throw new Error("invalid short state");
  let remote = "idle";
  while (io.alive()) {
    const observed = wake.version;
    let state = activityState(await io.pressure(), remote);
    if (bulk && state === "download") state = "bulk";
    io.state(state);
    if (state === "idle") remote = await io.idle(observed);
    else if (state === "bulk" && io.bulkLease) remote = await io.bulkLease();
    else {
      const length = state === "bulk" || state === shortState ? 1 : slots;
      for (let slot = 0; slot < length && io.alive(); slot++) remote = await io.exchange(state);
    }
  }
}

// Two finite transactions per application lease. The second upload cannot
// precede the first headers (server sequencing), or overlap local delivery IPC.
async function bulkPair(io, pipeline) {
  if (!pipeline) {
    await activeExchange(io, "bulk", true);
    return activeExchange(io, "bulk", true);
  }
  let first, following;
  const abort = new AbortController();
  const cancel = async response => { if (response && response.body) await response.body.cancel().catch(() => {}); };
  try {
    first = await io.send(16384, "/api/sync/bulk");
    if (first.status !== 200) throw new Error("first bulk sync");
    const body = await io.prepare(16384);
    following = io.post(body, "/api/sync/bulk", abort.signal).then(value => ({value}), error => ({error}));
    await io.receive(first, 262144);
    const second = await following;
    if (second.error) throw second.error;
    if (second.value.status !== 200) throw new Error("second bulk sync");
    return await io.receive(second.value, 262144);
  } catch (error) {
    abort.abort();
    await cancel(first);
    if (following) await cancel((await following).value);
    throw error;
  }
}

async function activeExchange(io, state, duplex) {
  const uploading = state === "upload" || state === "mixed";
  const capacity = state === "bulk" ? 262144 : state === "download" || state === "mixed" ? 65536 : 8192;
  const endpoint = state === "bulk" ? "/api/sync/bulk" : duplex ? "/api/exchange/" + state : uploading ? "/api/upload/chunk" : "/api/sync";
  const sent = await io.send(state === "bulk" ? 16384 : uploading ? 131072 : 4096, endpoint);
  if (sent.status !== (duplex ? 200 : 204)) throw new Error("active sync");
  return io.receive(duplex ? sent : await io.fetch("/api/data/" + state), capacity);
}

if (typeof module !== "undefined") module.exports = {DeliveryFence, shouldDeferDelivery, WakeLatch, activityState, runLifecycle, activeExchange, bulkPair};
