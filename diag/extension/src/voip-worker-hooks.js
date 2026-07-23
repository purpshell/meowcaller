(function installWaVoipWorkerHooks() {
  'use strict';

  if (globalThis.__waVoipWorkerHooksInstalled) return;
  globalThis.__waVoipWorkerHooksInstalled = true;

  const WRAPPED = Symbol('waVoipWrapped');
  const listenerWrappers = new WeakMap();
  let sequence = 0;

  function bytesOf(value) {
    if (value instanceof ArrayBuffer) return new Uint8Array(value);
    if (ArrayBuffer.isView(value)) return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
    return null;
  }

  function encodeBytes(value) {
    const bytes = bytesOf(value);
    if (!bytes) return null;
    let base64 = '';
    const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';
    for (let index = 0; index < bytes.length; index += 3) {
      const a = bytes[index];
      const hasB = index + 1 < bytes.length;
      const hasC = index + 2 < bytes.length;
      const b = hasB ? bytes[index + 1] : 0;
      const c = hasC ? bytes[index + 2] : 0;
      base64 += alphabet[a >> 2];
      base64 += alphabet[((a & 3) << 4) | (b >> 4)];
      base64 += hasB ? alphabet[((b & 15) << 2) | (c >> 6)] : '=';
      base64 += hasC ? alphabet[c & 63] : '=';
    }
    return { byteLength: bytes.byteLength, base64 };
  }

  function bytesToHex(bytes) {
    let hex = '';
    for (let index = 0; index < bytes.length; index++) hex += bytes[index].toString(16).padStart(2, '0');
    return hex;
  }

  function packetDetails(value) {
    const bytes = bytesOf(value);
    if (!bytes) return null;
    const packet = { byteLength: bytes.length, kind: 'unknown' };
    if (bytes.length >= 20 && (bytes[0] & 0xc0) === 0) {
      packet.kind = 'stun';
      packet.messageType = (bytes[0] << 8) | bytes[1];
      packet.messageLength = (bytes[2] << 8) | bytes[3];
    } else if (bytes.length >= 12 && (bytes[0] & 0xc0) === 0x80) {
      packet.kind = 'rtp-or-rtcp';
      packet.marker = Boolean(bytes[1] & 0x80);
      packet.payloadType = bytes[1] & 0x7f;
      packet.sequenceNumber = (bytes[2] << 8) | bytes[3];
      packet.timestamp = ((bytes[4] << 24) | (bytes[5] << 16) | (bytes[6] << 8) | bytes[7]) >>> 0;
      packet.ssrc = ((bytes[8] << 24) | (bytes[9] << 16) | (bytes[10] << 8) | bytes[11]) >>> 0;
      packet.extension = Boolean(bytes[0] & 0x10);
      if (packet.extension && bytes.length >= 16) {
        const words = (bytes[14] << 8) | bytes[15];
        packet.extensionProfile = (bytes[12] << 8) | bytes[13];
        packet.extensionWords = words;
        packet.extensionHex = bytesToHex(bytes.subarray(16, Math.min(bytes.length, 16 + words * 4)));
      }
    }
    return packet;
  }

  function channelMetadata(channel) {
    return {
      label: channel.label, id: channel.id, protocol: channel.protocol,
      readyState: channel.readyState, bufferedAmount: channel.bufferedAmount,
      binaryType: channel.binaryType, negotiated: channel.negotiated,
      ordered: channel.ordered, maxPacketLifeTime: channel.maxPacketLifeTime,
      maxRetransmits: channel.maxRetransmits,
    };
  }

  function emit(event) {
    const ts = Date.now();
    try {
      globalThis.__waVoipCdpEmit(JSON.stringify({
        schema: 'wa-voip-worker/v2', sequence: ++sequence, ts,
        isoTime: new Date(ts).toISOString(), monotonicMs: performance.now(),
        realm: typeof document === 'undefined' ? 'worker' : 'window',
        location: globalThis.location?.href || null,
        ...event,
      }));
    } catch {}
  }

  function captureData(event, channel, value) {
    const bytes = bytesOf(value);
    emit({
      source: 'rtc-datachannel', event,
      channel: channelMetadata(channel),
      binary: bytes ? encodeBytes(bytes) : null,
      packet: bytes ? packetDetails(bytes) : null,
      valueType: value?.constructor?.name || typeof value,
      text: typeof value === 'string' ? value : null,
      blob: typeof Blob !== 'undefined' && value instanceof Blob
        ? { size: value.size, type: value.type } : null,
    });
    if (typeof Blob !== 'undefined' && value instanceof Blob) {
      void value.arrayBuffer().then(buffer => captureData(`${event}-blob`, channel, buffer));
    }
  }

  const prototype = globalThis.RTCDataChannel?.prototype;
  if (prototype) {
    const originalSend = prototype.send;
    if (typeof originalSend === 'function' && !originalSend[WRAPPED]) {
      function send(data) {
        captureData('send', this, data);
        return Reflect.apply(originalSend, this, arguments);
      }
      Object.defineProperty(send, WRAPPED, { value: true });
      prototype.send = send;
    }

    const originalAdd = prototype.addEventListener;
    const originalRemove = prototype.removeEventListener;
    if (typeof originalAdd === 'function') {
      prototype.addEventListener = function (type, listener, options) {
        if (type !== 'message' || !listener) return Reflect.apply(originalAdd, this, arguments);
        let wrappers = listenerWrappers.get(this);
        if (!wrappers) { wrappers = new WeakMap(); listenerWrappers.set(this, wrappers); }
        let wrapped = wrappers.get(listener);
        if (!wrapped) {
          wrapped = typeof listener === 'function'
            ? function (event) { captureData('receive', event.currentTarget, event.data); return Reflect.apply(listener, this, arguments); }
            : { handleEvent(event) { captureData('receive', event.currentTarget, event.data); return listener.handleEvent(event); } };
          wrappers.set(listener, wrapped);
        }
        return Reflect.apply(originalAdd, this, [type, wrapped, options]);
      };
    }
    if (typeof originalRemove === 'function') {
      prototype.removeEventListener = function (type, listener, options) {
        const wrapped = type === 'message' ? listenerWrappers.get(this)?.get(listener) : null;
        return Reflect.apply(originalRemove, this, [type, wrapped || listener, options]);
      };
    }
    const onmessage = Object.getOwnPropertyDescriptor(prototype, 'onmessage');
    if (onmessage?.set) {
      const assigned = new WeakMap();
      Object.defineProperty(prototype, 'onmessage', {
        configurable: onmessage.configurable,
        enumerable: onmessage.enumerable,
        get() { return assigned.get(this)?.original || onmessage.get?.call(this) || null; },
        set(listener) {
          if (typeof listener !== 'function') return onmessage.set.call(this, listener);
          const wrapped = function (event) {
            captureData('receive', event.currentTarget, event.data);
            return Reflect.apply(listener, this, arguments);
          };
          assigned.set(this, { original: listener, wrapped });
          return onmessage.set.call(this, wrapped);
        },
      });
    }
  }

  globalThis.addEventListener?.('error', event => emit({
    source: 'worker-runtime', event: 'error', message: event.message,
    filename: event.filename, lineno: event.lineno, colno: event.colno,
    stack: event.error?.stack || null,
  }));
  globalThis.addEventListener?.('unhandledrejection', event => emit({
    source: 'worker-runtime', event: 'unhandledrejection',
    reason: String(event.reason?.stack || event.reason),
  }));

  emit({
    source: 'capture', event: 'ready', hook: 'worker-rtc-datachannel',
    hasRTCDataChannel: Boolean(prototype),
  });
})();
