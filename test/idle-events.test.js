"use strict";
const test=require("node:test"),assert=require("node:assert/strict");
const {receiveIdle}=require("../lab/browser-application/lifecycle.js");

test("empty idle heartbeat does not deliver a cell",async()=>{
  const response={status:204,headers:new Headers(),arrayBuffer:async()=>new ArrayBuffer(0)};
  assert.equal(await receiveIdle(response,true,()=>assert.fail("heartbeat codec")),"idle");
});

test("idle event and legacy timeout keep their declared capacity",async()=>{
  for(const events of [false,true]){
    const response={status:200};let received=0;
    assert.equal(await receiveIdle(response,events,async(value,capacity)=>{received++;assert.equal(value,response);assert.equal(capacity,events?8192:512);return "download";}),"download");
    assert.equal(received,1);
  }
});

test("malformed heartbeat and event statuses fail before codec dispatch",async()=>{
  for(const response of [
    {status:204,headers:new Headers({"X-App-Capacity":"512"}),arrayBuffer:async()=>new ArrayBuffer(0)},
    {status:204,headers:new Headers(),arrayBuffer:async()=>new ArrayBuffer(1)},
    {status:206},{status:400},
  ])await assert.rejects(receiveIdle(response,true,()=>assert.fail("invalid dispatch")),/idle/);
});
