"use strict";
const assert=require("node:assert/strict");
const test=require("node:test");
const {bulkPair,runLifecycle,WakeLatch}=require("../site/lifecycle.js");
const deferred=()=>{let resolve;const promise=new Promise(value=>resolve=value);return {promise,resolve};};

test("paired overlap waits for first headers and upload IPC, then delivers in order",async()=>{
  const trace=[],headers=deferred(),prepared=deferred(),delivered=deferred(),secondHeaders=deferred();
  const first={status:200,id:1},second={status:200,id:2};
  const pending=bulkPair({
    send:async(capacity,path)=>{assert.equal(capacity,16384);assert.equal(path,"/api/sync/bulk");trace.push("send1");return headers.promise;},
    prepare:async capacity=>{assert.equal(capacity,16384);trace.push("prepare");return prepared.promise;},
    post:(body,path)=>{assert.equal(body.byteLength,16384);assert.equal(path,"/api/sync/bulk");trace.push("send2");return secondHeaders.promise;},
    receive:async(response,capacity)=>{assert.equal(capacity,262144);trace.push("receive"+response.id);if(response.id===1)await delivered.promise;return "idle";},
  },true);
  await Promise.resolve();assert.deepEqual(trace,["send1"]);
  headers.resolve(first);await new Promise(setImmediate);assert.deepEqual(trace,["send1","prepare"]);
  prepared.resolve(new Uint8Array(16384));await new Promise(setImmediate);
  assert.deepEqual(trace,["send1","prepare","send2","receive1"]);
  secondHeaders.resolve(second);await new Promise(setImmediate);
  assert.equal(trace.includes("receive2"),false);
  delivered.resolve();assert.equal(await pending,"idle");
  assert.deepEqual(trace,["send1","prepare","send2","receive1","receive2"]);
});

test("serial pair has the same two fixed transactions without overlap",async()=>{
  const trace=[];let count=0;
  await bulkPair({
    send:async(capacity,path)=>{assert.equal(capacity,16384);assert.equal(path,"/api/sync/bulk");trace.push("send"+(++count));return {status:200,id:count};},
    prepare:()=>assert.fail("unexpected preparation"),post:()=>assert.fail("unexpected concurrent fetch"),
    receive:async(response,capacity)=>{assert.equal(capacity,262144);trace.push("receive"+response.id);return "idle";},
  },false);
  assert.deepEqual(trace,["send1","receive1","send2","receive2"]);
});

test("paired delivery failure aborts the outstanding second request",async()=>{
  let aborted=false,cancelled=0;
  await assert.rejects(bulkPair({
    send:async()=>({status:200,body:{cancel:async()=>{cancelled++;}}}),
    prepare:async()=>new Uint8Array(16384),
    post:(_,__,signal)=>new Promise((resolve,reject)=>signal.addEventListener("abort",()=>{aborted=true;reject(new Error("aborted"));})),
    receive:async()=>{throw new Error("bad cell");},
  },true),/bad cell/);
  assert.equal(aborted,true);assert.equal(cancelled,1);
});

test("paired failures cancel received bodies and never deliver an invalid response",async()=>{
  for(const failure of ["first-status","prepare","second-status","second-fetch","second-delivery"]){
    let posts=0,delivered=0,cancelled=0;
    const response=status=>({status,body:{cancel:async()=>{cancelled++;}}});
    await assert.rejects(bulkPair({
      send:async()=>response(failure==="first-status"?400:200),
      prepare:async()=>{if(failure==="prepare")throw new Error("prepare");return new Uint8Array(16384);},
      post:async()=>{posts++;if(failure==="second-fetch")throw new Error("fetch");return response(failure==="second-status"?400:200);},
      receive:async()=>{if(++delivered===2)throw new Error("delivery");return "idle";},
    },true));
    assert.equal(posts,failure==="first-status"||failure==="prepare"?0:1);
    assert.equal(delivered,failure==="first-status"||failure==="prepare"?0:failure==="second-delivery"?2:1);
    assert.ok(cancelled>=1);
  }
});

test("lifecycle reevaluates state only after the bounded bulk pair",async()=>{
  let alive=true,polls=0,pairs=0,queries=0;
  await runLifecycle({alive:()=>alive,pressure:async()=>{queries++;return {bytes:0,controls:0};},state(){},
    idle:async()=>{if(++polls===2)alive=false;return "download";},
    bulkLease:async()=>{pairs++;return "idle";},exchange:()=>assert.fail("single exchange"),
  },new WakeLatch(),4,true);
  assert.equal(pairs,1);assert.equal(queries,3);
});
