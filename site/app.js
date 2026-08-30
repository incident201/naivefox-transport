"use strict";
(() => {
  const params = new URLSearchParams(location.hash.slice(1));
  const mode = new URLSearchParams(location.search).get("mode");
  const rounds = Math.max(12, Math.min(256, Number(params.get("rounds")) || 16));
  let socket, pending, sequence = 0, running = false;
  const chart = document.getElementById("chart").getContext("2d");
  const status = document.getElementById("status");
  function ipc(body) {
    if (pending) throw new Error("ipc overlap");
    return new Promise((resolve, reject) => { pending = {resolve, reject}; socket.send(body); });
  }
  function emptyCell(capacity) {
    const body = new Uint8Array(capacity);
    for (let offset=0; offset<body.length; offset+=65536) crypto.getRandomValues(body.subarray(offset, Math.min(offset+65536,body.length)));
    body.set([78,70,67,49]);
    const view=new DataView(body.buffer);view.setUint32(4,sequence++);view.setUint32(8,16);view.setUint32(12,0);
    return body;
  }
  async function run() {
    if (running) return; running=true;window.__NFC_DONE__=false;window.__NFC_ERROR__=null;
    try {
      document.getElementById("progress").max=rounds;
      for(let round=0;round<rounds;round++) {
        const upload = params.get("upload")==="1";
        const capacity=upload?131072:4096;
        let body;
        if(socket) { const request=new Uint8Array(5);request[0]=1;new DataView(request.buffer).setUint32(1,capacity);body=await ipc(request); }
        else body=emptyCell(capacity);
        const sent=await fetch(upload?"/api/upload/chunk":"/api/sync",{method:"POST",body,credentials:"same-origin"});
        if(sent.status!==204)throw new Error("sync");
        const path=round<2||round>=rounds-2?"/api/events":"/media/chunk/"+round;
        const response=await fetch(path,{credentials:"same-origin"});
        if(!response.ok)throw new Error("events");
        const result=new Uint8Array(await response.arrayBuffer());
        const declared=Number(response.headers.get("X-App-Capacity"));
        if(result.length<declared || (mode!=="append"&&result.length!==declared))throw new Error("capacity");
        if(socket){const message=new Uint8Array(result.length+1);message[0]=2;message.set(result,1);await ipc(message);}
        chart.fillStyle=round%2?"#c0d69b":"#779989";chart.fillRect(round*640/rounds,120-(round+1)*100/rounds,640/rounds-2,(round+1)*100/rounds);
        document.getElementById("progress").value=round+1;status.textContent=`Archive segment ${round+1} of ${rounds}`;
        window.__NFC_ROUND__=round+1;
        await new Promise(resolve=>requestAnimationFrame(resolve));
      }
      status.textContent="Archive synchronized.";window.__NFC_DONE__=true;
    } catch (_) { status.textContent="Synchronization unavailable.";window.__NFC_ERROR__="application-or-transport"; }
    finally { running=false; }
  }
  document.getElementById("refresh").addEventListener("click",run);
  (async()=>{
    try {
      if(params.has("bridge")) {
        const bridge=new URL(params.get("bridge"));
        if(bridge.protocol!=="wss:" || !["localhost","127.0.0.1","[::1]"].includes(bridge.hostname))throw new Error("local bridge");
        socket=new WebSocket(bridge);socket.binaryType="arraybuffer";
        await new Promise((resolve,reject)=>{socket.onopen=resolve;socket.onerror=reject;});
        socket.onmessage=event=>{const next=pending;pending=null;if(next)next.resolve(event.data);};
        socket.onclose=()=>{if(pending){pending.reject(new Error("ipc closed"));pending=null;}};
      }
      window.__NFC_READY__=true;
      if(params.get("hold")!=="1") await run();
    } catch (_) {window.__NFC_ERROR__="bridge-start";}
  })();
  window.__NFC_RUN__=run;
})();
