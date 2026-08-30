"use strict";
__NFC_READER__
__NFC_LIFECYCLE__
(() => {
  const params = new URLSearchParams(location.hash.slice(1));
  const profile = __NFC_PROFILE__;
  const rounds = Math.max(12, Math.min(256, Number(params.get("rounds")) || profile.rounds));
  let socket, pending, sequence = 0, downloadSequence = 0, running = false;
  const wake = new WakeLatch();
  const deliveryFence = new DeliveryFence();
  let manualWake = false, bridgeClosed = false;
  const chart = document.getElementById("chart").getContext("2d");
  const status = document.getElementById("status");
  function ipc(body) {
    if (pending) throw new Error("ipc overlap");
    return new Promise((resolve, reject) => { pending = {resolve, reject}; socket.send(body); }).then(value=>{deliveryFence.acknowledge();return value;});
  }
  function emptyCell(capacity) {
    const body = new Uint8Array(capacity);
    for (let offset=0; offset<body.length; offset+=65536) crypto.getRandomValues(body.subarray(offset, Math.min(offset+65536,body.length)));
    body.set([78,70,67,49]);
    const view=new DataView(body.buffer);view.setUint32(4,sequence++);view.setUint32(8,16);view.setUint32(12,0);
    return body;
  }
  async function sendSlot(capacity, endpoint) {
    let body;
    if(socket) { const request=new Uint8Array(5);request[0]=1;new DataView(request.buffer).setUint32(1,capacity);body=await ipc(request); }
    else body=emptyCell(capacity);
    return fetch(endpoint,{method:"POST",body,credentials:"same-origin"});
  }
  async function prepareSlot(capacity) {
    if(!socket)return emptyCell(capacity);
    const request=new Uint8Array(5);request[0]=1;new DataView(request.buffer).setUint32(1,capacity);
    return ipc(request);
  }
  async function receiveSlot(response, capacity) {
    if(!response.ok)throw new Error("events");
    const framing=profile.streaming==="frames"&&capacity===262144;
    const streaming=profile.streaming==="frames"?framing&&"frames":profile.streaming;
    const early=await readCarrier(response,capacity,downloadSequence++,streaming,async (result,fragment)=>{
      if(framing){window.__NFC_FRAME_PARTS__++;if(fragment.remaining>0)window.__NFC_EARLY_FRAME_PARTS__++;}
      if(socket){
        const deferAck=shouldDeferDelivery(profile,window.__NFC_PHASE__,framing);
        const message=new Uint8Array(result.length+(framing?2:1));message[0]=framing?6:deferAck?7:2;
        if(framing)message[1]=Number(fragment.final);message.set(result,framing?2:1);
        if(deferAck){if(pending)throw new Error("delivery during ipc");deliveryFence.send(socket,message);window.__NFC_DEFERRED_DELIVERIES__++;}
        else await ipc(message);
      }
    });
    if(early){if(framing)window.__NFC_FRAME_BYTES_PENDING__+=early;else{window.__NFC_EARLY_CELLS__++;window.__NFC_EARLY_FILLER__+=early;}}
    const state=response.headers.get("X-App-State")||"idle";
    if(!["idle","interactive","download"].includes(state))throw new Error("remote state");
    return state;
  }
  async function pressure() {
    if(bridgeClosed)throw new Error("bridge closed");
    const value=socket?JSON.parse(new TextDecoder().decode(await ipc(new Uint8Array([5])))):{bytes:0,controls:0,streams:0};
    if(manualWake)value.bytes=Math.max(value.bytes,1);
    return value;
  }
  async function idle(observed) {
    const abort=new AbortController();
    window.__NFC_IDLE_POLLS__++;
    const response=fetch("/api/events/idle",{credentials:"same-origin",signal:abort.signal});
    const remote=response.then(value=>({response:value}));
    let waiting, complete=false;
    try {
      while(true) {
        waiting=wake.after(observed);
        const ready=await Promise.race([remote,waiting.promise.then(()=>({wake:true}))]);
        waiting.cancel();
        if(ready.response) {
          const state=await receiveIdle(ready.response,profile.idle_events,receiveSlot);complete=true;return state;
        }
        observed=wake.version;
        const value=await pressure();
        if(!value.bytes&&!value.controls)continue;
        manualWake=false;
        const sent=await sendSlot(4096,"/api/sync");
        if(sent.status!==204)throw new Error("idle wake");
        window.__NFC_IDLE_WAKE_POSTS__++;
        const state=await receiveIdle(await response,profile.idle_events,receiveSlot);complete=true;return state;
      }
    } finally {
      if(waiting)waiting.cancel();
      if(!complete){abort.abort();await remote.catch(()=>{});}
    }
  }
  async function run() {
    if (running) return; running=true;window.__NFC_DONE__=false;window.__NFC_ERROR__=null;
    window.__NFC_EARLY_CELLS__=0;window.__NFC_EARLY_FILLER__=0;
    window.__NFC_FRAME_PARTS__=0;window.__NFC_EARLY_FRAME_PARTS__=0;window.__NFC_FRAME_BYTES_PENDING__=0;
    window.__NFC_DEFERRED_DELIVERIES__=0;
    window.__NFC_BULK_PAIRS__=0;window.__NFC_PIPELINED_PAIRS__=0;
    window.__NFC_ACTION_DONE__=false;
    window.__NFC_PHASE__="startup";window.__NFC_ALIVE__=true;window.__NFC_DYNAMIC_ROUNDS__=0;window.__NFC_IDLE_POLLS__=0;window.__NFC_IDLE_WAKE_POSTS__=0;
    try {
      document.getElementById("progress").max=rounds;
      for(let round=0;round<rounds;round++) {
        const upload = params.get("upload")==="1";
        const capacity=upload?131072:4096;
        const media=round>=2&&round<rounds-2;
        const endpoint=upload?"/api/upload/chunk":profile.duplex&&media?"/api/sync/media":"/api/sync";
        const sent=await sendSlot(capacity,endpoint);
        if(sent.status!==(profile.duplex?200:204))throw new Error("sync");
        const slot=profile.slots?profile.slots[round%profile.slots.length]:media?profile.down:24576;
        const path=slot===8192?"/api/events/brief":slot===32768?"/api/events/state":slot===24576?"/api/events":"/media/chunk/"+round;
        const response=profile.duplex?sent:await fetch(path,{credentials:"same-origin"});
        await receiveSlot(response,slot);
        chart.fillStyle=round%2?"#c0d69b":"#779989";chart.fillRect(round*640/rounds,120-(round+1)*100/rounds,640/rounds-2,(round+1)*100/rounds);
        document.getElementById("progress").value=round+1;status.textContent=`Archive segment ${round+1} of ${rounds}`;
        window.__NFC_ROUND__=round+1;
        if((round+1)%profile.paint_every===0||round===rounds-1)await new Promise(resolve=>requestAnimationFrame(resolve));
      }
      if(profile.commit){await receiveSlot(await sendSlot(4096,"/api/action"),4096);window.__NFC_ACTION_DONE__=true;}
      status.textContent="Archive synchronized.";window.__NFC_DONE__=true;
      if(profile.continuous)await runLifecycle({
        alive:()=>!bridgeClosed,
        pressure,
        state:value=>{window.__NFC_PHASE__=value;status.textContent=value==="idle"?"Waiting for updates.":"Synchronizing updates.";},
        idle,
        bulkLease:profile.pair_bulk?async()=>{
          manualWake=false;
          const hint=await bulkPair({send:sendSlot,prepare:prepareSlot,
            post:(body,path,signal)=>fetch(path,{method:"POST",body,credentials:"same-origin",signal}),receive:receiveSlot},profile.pipeline_bulk);
          window.__NFC_BULK_PAIRS__++;if(profile.pipeline_bulk)window.__NFC_PIPELINED_PAIRS__++;
          window.__NFC_DYNAMIC_ROUNDS__+=2;return hint;
        }:undefined,
        exchange:async state=>{
          manualWake=false;
          const hint=await activeExchange({send:sendSlot,receive:receiveSlot,fetch:path=>fetch(path,{credentials:"same-origin"})},state,activeDuplex(profile,state));
          window.__NFC_DYNAMIC_ROUNDS__++;return hint;
        },
      },wake,profile.lease_slots,profile.bulk,profile.short_state||"");
    } catch (_) { if(socket)socket.close();status.textContent="Synchronization unavailable.";window.__NFC_ERROR__="application-or-transport"; }
    finally { running=false;window.__NFC_ALIVE__=false; }
  }
  document.getElementById("refresh").addEventListener("click",()=>{if(profile.continuous&&running){manualWake=true;wake.notify();}else run();});
  (async()=>{
    try {
      if(params.has("bridge")) {
        const bridge=new URL(params.get("bridge"));
        if(bridge.protocol!=="wss:" || !["localhost","127.0.0.1","[::1]"].includes(bridge.hostname))throw new Error("local bridge");
        socket=new WebSocket(bridge);socket.binaryType="arraybuffer";
        await new Promise((resolve,reject)=>{socket.onopen=resolve;socket.onerror=reject;});
        socket.onmessage=event=>{const body=new Uint8Array(event.data);if(body.length===1&&body[0]===4){wake.notify();return;}const next=pending;pending=null;if(next)next.resolve(event.data);};
        socket.onclose=()=>{bridgeClosed=true;wake.notify();if(pending){pending.reject(new Error("ipc closed"));pending=null;}};
      }
      window.__NFC_READY__=true;
      if(params.get("hold")!=="1") await run();
    } catch (_) {window.__NFC_ERROR__="bridge-start";}
  })();
  window.__NFC_RUN__=run;
})();
