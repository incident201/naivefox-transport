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

async function runLifecycle(io, wake) {
  let remote = "idle";
  while (io.alive()) {
    const observed = wake.version;
    const state = activityState(await io.pressure(), remote);
    io.state(state);
    if (state === "idle") remote = await io.idle(observed);
    else {
      for (let slot = 0; slot < 4 && io.alive(); slot++) remote = await io.exchange(state);
    }
  }
}

if (typeof module !== "undefined") module.exports = {WakeLatch, activityState, runLifecycle};
