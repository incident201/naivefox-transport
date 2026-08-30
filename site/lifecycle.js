"use strict";

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
    else {
      const length = state === "bulk" || state === shortState ? 1 : slots;
      for (let slot = 0; slot < length && io.alive(); slot++) remote = await io.exchange(state);
    }
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

if (typeof module !== "undefined") module.exports = {WakeLatch, activityState, runLifecycle, activeExchange};
