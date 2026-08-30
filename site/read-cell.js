"use strict";

async function readCarrier(response, capacity, sequence, streaming, deliver) {
  const length = Number(response.headers.get("Content-Length"));
  const declared = Number(response.headers.get("X-App-Capacity"));
  const encoding = response.headers.get("Content-Encoding");
  if (!Number.isInteger(length) || length < 16 || length > 262144 ||
      declared !== capacity || length < capacity || (encoding && encoding !== "identity")) {
    throw new Error("cell response envelope");
  }
  function usedLength(body) {
    const view = new DataView(body.buffer, body.byteOffset, body.byteLength);
    const used = view.getUint32(8);
    if (view.getUint32(0) !== 0x4e464331 || view.getUint32(4) !== sequence ||
        used < 16 || used > capacity || view.getUint16(12) > 4096 || view.getUint16(14) !== 0 ||
        (length !== capacity && length !== capacity + used - 16)) {
      throw new Error("cell header");
    }
    return used;
  }
  function validateFrames(body) {
    const view = new DataView(body.buffer, body.byteOffset, body.byteLength);
    let offset = 16;
    for (let count = view.getUint16(12); count > 0; count--) {
      if (offset + 16 > body.length || body[offset] < 1 || body[offset] > 7 ||
          body[offset + 1] || body[offset + 2] || body[offset + 3]) throw new Error("cell frame");
      offset += 16 + view.getUint32(offset + 12);
      if (offset > body.length) throw new Error("cell frame length");
    }
    if (offset !== body.length) throw new Error("cell frame count");
  }
  if (!streaming) {
    const body = new Uint8Array(await response.arrayBuffer());
    if (body.length !== length) throw new Error("cell body length");
    const used = usedLength(body);
    validateFrames(body.subarray(0, used));
    await deliver(body);
    return 0;
  }
  if (streaming === "frames") {
    const reader = response.body.getReader();
    const prefix = new Uint8Array(capacity);
    let received = 0, used = 0, offset = 16, parsed = 0, sent = 0, earlyBytes = 0;
    try {
      while (true) {
        const {done, value} = await reader.read();
        if (done) break;
        if (received + value.length > length) throw new Error("cell body overflow");
        if (received < capacity) prefix.set(value.subarray(0, capacity - received), received);
        received += value.length;
        if (!used && received >= 16) used = usedLength(prefix);
        if (!used) continue;
        const view = new DataView(prefix.buffer);
        const count = view.getUint16(12);
        while (parsed < count && offset + 16 <= Math.min(received, used)) {
          const size = view.getUint32(offset + 12);
          if (prefix[offset] < 1 || prefix[offset] > 7 || prefix[offset+1] || prefix[offset+2] || prefix[offset+3] || offset + 16 + size > used) throw new Error("cell frame");
          if (offset + 16 + size > received) break;
          offset += 16 + size;
          parsed++;
        }
        if (received >= used && (offset !== used || parsed !== count)) throw new Error("cell frame count");
        // Batch whatever complete frames this read supplied; no waiting for
        // another network chunk or timer. Filler never enters local IPC.
        if (offset > 16 && offset > sent) {
          await deliver(prefix.subarray(sent, offset), {final: false, remaining: Math.max(0, used - received)});
          if (!sent) earlyBytes = Math.max(0, used - received);
          sent = offset;
        }
      }
      if (!used || received !== length || offset !== used || parsed !== new DataView(prefix.buffer).getUint16(12)) throw new Error("cell body truncated");
      await deliver(prefix.subarray(sent, used), {final: true, remaining: 0});
      return earlyBytes;
    } catch (error) {
      await reader.cancel().catch(() => {});
      throw error;
    } finally { reader.releaseLock(); }
  }
  const reader = response.body.getReader();
  const prefix = new Uint8Array(capacity);
  let received = 0, used = 0, delivered = false, earlyBytes = 0;
  try {
    while (true) {
      const {done, value} = await reader.read();
      if (done) break;
      if (received + value.length > length) throw new Error("cell body overflow");
      if (!delivered) {
        prefix.set(value.subarray(0, Math.max(0, capacity - received)), received);
      }
      received += value.length;
      if (!used && received >= 16) used = usedLength(prefix);
      if (!delivered && received >= used && used) {
        const body = prefix.subarray(0, used);
        validateFrames(body);
        await deliver(body);
        delivered = true;
        if (used > 16) earlyBytes = length - received;
      }
    }
    if (!delivered || received !== length) throw new Error("cell body truncated");
    return earlyBytes;
  } catch (error) {
    await reader.cancel().catch(() => {});
    throw error;
  } finally {
    reader.releaseLock();
  }
}

if (typeof module !== "undefined") module.exports = {readCarrier};
