(() => {
  "use strict";
  if (window.__meowcallerDiagnostics) return;
  window.__meowcallerDiagnostics = true;

  const session = crypto.randomUUID();
  const signalTags = new Set([
    "accept", "auth_token", "call", "enc_rekey", "hbh_key", "key", "link",
    "mute", "mute_v2", "offer", "offer_notice", "preaccept", "receipt",
    "reject", "relay", "relaylatency", "te", "te2", "terminate", "token",
    "transport", "video", "voip_settings",
  ]);
  let sequence = 0;
  let previousCall = "";

  const emit = (source, event, data = {}) => window.postMessage({
    type: "MEOWCALLER_DIAGNOSTIC",
    payload: {
      schema: "meowcaller-wa-diagnostics/v1",
      session,
      sequence: ++sequence,
      ts: Date.now(),
      source,
      event,
      ...data,
    },
  }, "*");

  const bytesOf = value => value instanceof ArrayBuffer
    ? new Uint8Array(value)
    : ArrayBuffer.isView(value)
      ? new Uint8Array(value.buffer, value.byteOffset, value.byteLength)
      : null;

  function base64(bytes) {
    let binary = "";
    for (let offset = 0; offset < bytes.length; offset += 0x8000) {
      binary += String.fromCharCode(...bytes.subarray(offset, offset + 0x8000));
    }
    return btoa(binary);
  }

  function clean(value, depth = 0, seen = new WeakSet()) {
    if (value == null || ["string", "number", "boolean"].includes(typeof value)) return value;
    if (typeof value === "bigint") return String(value);
    const bytes = bytesOf(value);
    if (bytes) return { $binary: { byteLength: bytes.length, base64: base64(bytes) } };
    if (typeof value !== "object") return String(value);
    if (depth >= 6 || seen.has(value)) return "[truncated]";
    seen.add(value);
    const output = Array.isArray(value) ? [] : {};
    for (const key of Object.getOwnPropertyNames(value).slice(0, 100)) {
      try {
        const descriptor = Object.getOwnPropertyDescriptor(value, key);
        output[key] = descriptor && "value" in descriptor
          ? clean(descriptor.value, depth + 1, seen)
          : "[accessor]";
      } catch {
        output[key] = "[unreadable]";
      }
    }
    seen.delete(value);
    return output;
  }

  function read(node, key) {
    const value = node?.[key];
    if (typeof value !== "function") return value;
    try { return value.call(node); } catch { return undefined; }
  }

  const tag = node => String(read(node, "tag") || "");
  const attrs = node => {
    let value = read(node, "attrs");
    try { value = value?.toJSON?.() || value; } catch {}
    return clean(value);
  };
  const children = node => {
    const content = read(node, "content");
    return Array.isArray(content) ? content.filter(item => item && typeof item === "object" && !bytesOf(item)) : [];
  };

  function containsSignal(node, depth = 0, seen = new WeakSet()) {
    if (!node || typeof node !== "object" || depth > 16 || seen.has(node)) return false;
    seen.add(node);
    if (signalTags.has(tag(node))) return true;
    return children(node).some(child => containsSignal(child, depth + 1, seen));
  }

  function nodeValue(node, depth = 0, seen = new WeakSet()) {
    if (node == null || typeof node !== "object" || depth > 16 || seen.has(node)) return clean(node);
    const bytes = bytesOf(node);
    if (bytes) return clean(bytes);
    seen.add(node);
    const content = read(node, "content");
    const result = {
      tag: tag(node),
      attrs: attrs(node),
      content: Array.isArray(content)
        ? content.map(child => nodeValue(child, depth + 1, seen))
        : clean(content),
    };
    seen.delete(node);
    return result;
  }

  function callSnapshot() {
    try {
      const call = window.require("WAWebCallCollection").activeCall;
      if (!call) return null;
      return {
        id: call.id || null,
        peer: call.peerJid?.toString?.() || null,
        outgoing: Boolean(call.outgoing),
        state: call.getState?.() || null,
        isVideo: Boolean(call.isVideo),
        selfVideoState: call.selfVideoState ?? null,
        peerVideoState: call.peerVideoState ?? null,
        selfMicMuted: Boolean(call.selfMicMuted),
        peerMicMuted: Boolean(call.peerMicMuted),
      };
    } catch { return null; }
  }

  function wrapWap(wap) {
    if (wap.__meowcallerDiagnostics) return;
    Object.defineProperty(wap, "__meowcallerDiagnostics", { value: true });
    for (const [method, stage, nodeIndex] of [["encodeStanza", "encode", 0], ["decodeStanza", "decode", -1]]) {
      const original = wap[method];
      if (typeof original !== "function") continue;
      wap[method] = function (...args) {
        const result = Reflect.apply(original, this, args);
        Promise.resolve(result).then(value => {
          const node = nodeIndex < 0 ? value : args[nodeIndex];
          if (containsSignal(node)) emit("wawap", "signaling", { stage, node: nodeValue(node), call: callSnapshot() });
        }, () => {});
        return result;
      };
    }
    emit("capture", "ready", { hook: "WAWap" });
  }

  function wrapStack(stack) {
    if (!stack || stack.__meowcallerDiagnostics) return;
    Object.defineProperty(stack, "__meowcallerDiagnostics", { value: true });
    const methods = new Set();
    for (let object = stack, depth = 0; object && depth < 5; object = Object.getPrototypeOf(object), depth++) {
      Object.getOwnPropertyNames(object).forEach(name => methods.add(name));
    }
    for (const method of methods) {
      if (!/call|video|audio|camera|microphone|mute|upgrade|stream|reaction/i.test(method) ||
          /packet|frame|sample|process|handle|dispatch|emit|callback|listener/i.test(method)) continue;
      const original = stack[method];
      if (typeof original !== "function") continue;
      stack[method] = function (...args) {
        emit("voip-stack", "call", { method, args: clean(args), call: callSnapshot() });
        try {
          const result = Reflect.apply(original, this, args);
          Promise.resolve(result).then(
            value => emit("voip-stack", "result", { method, result: clean(value), call: callSnapshot() }),
            error => emit("voip-stack", "error", { method, error: String(error?.stack || error) }),
          );
          return result;
        } catch (error) {
          emit("voip-stack", "error", { method, error: String(error?.stack || error) });
          throw error;
        }
      };
    }
    emit("capture", "ready", { hook: "WAWebVoipStackInterface" });
  }

  function observeTrack(track) {
    const snapshot = () => ({
      id: track.id, kind: track.kind, label: track.label, enabled: track.enabled,
      muted: track.muted, readyState: track.readyState, settings: clean(track.getSettings?.()),
    });
    emit("media-track", "observed", { track: snapshot() });
    for (const event of ["mute", "unmute", "ended"]) {
      track.addEventListener(event, () => emit("media-track", event, { track: snapshot() }));
    }
  }

  const getUserMedia = navigator.mediaDevices?.getUserMedia?.bind(navigator.mediaDevices);
  if (getUserMedia) navigator.mediaDevices.getUserMedia = function (constraints) {
    emit("media", "getUserMedia", { constraints: clean(constraints) });
    const result = getUserMedia(constraints);
    result.then(stream => stream.getTracks().forEach(observeTrack),
      error => emit("media", "error", { error: String(error?.stack || error) }));
    return result;
  };

  async function install() {
    while (typeof window.require !== "function") await new Promise(resolve => setTimeout(resolve, 200));
    for (;;) {
      try { wrapWap(window.require("WAWap")); } catch {}
      try { wrapStack(await window.require("WAWebVoipStackInterface").getVoipStackInterface()); } catch {}
      try {
        const logger = window.require("WAWebLoggerImpl").Logger;
        if (logger?.logImpl && !logger.logImpl.__meowcallerDiagnostics) {
          const original = logger.logImpl;
          const wrapped = function (...args) {
            const result = Reflect.apply(original, this, args);
            const text = args.map(String).join(" ");
            if (/voip|call|video|audio|camera|microphone|relay|rtp|rtcp|stun|srtp|mlow|neteq/i.test(text)) {
              emit("wa-logger", "log", { args: clean(args) });
            }
            return result;
          };
          Object.defineProperty(wrapped, "__meowcallerDiagnostics", { value: true });
          logger.logImpl = wrapped;
          emit("capture", "ready", { hook: "WAWebLoggerImpl" });
        }
      } catch {}
      await new Promise(resolve => setTimeout(resolve, 1000));
    }
  }

  setInterval(() => {
    const call = callSnapshot();
    const serialized = JSON.stringify(call);
    if (serialized !== previousCall) {
      previousCall = serialized;
      emit("call-model", "state", { call });
    }
  }, 500);

  emit("capture", "ready", { hook: "page", version: window.Debug?.VERSION || null });
  void install();
})();
