(function installVoipCapture() {
  'use strict';

  if (window.__waVoipCaptureInstalled) return;
  window.__waVoipCaptureInstalled = true;

  const wrappedStacks = new WeakSet();
  let previousModelSnapshot = '';

  function sleep(ms) {
    return new Promise(resolve => setTimeout(resolve, ms));
  }

  async function waitFor(check, timeoutMs = 120000) {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
      try {
        const value = check();
        if (value) return value;
      } catch {}
      await sleep(200);
    }
    throw new Error('WhatsApp module wait timed out');
  }

  function safeValue(value, depth = 0, seen = new WeakSet()) {
    if (window.__waVoipDiagnostic?.fullValue) return window.__waVoipDiagnostic.fullValue(value, depth, seen);
    return value == null ? value : String(value);
  }

  function activeCallSnapshot() {
    try {
      const call = window.require('WAWebCallCollection').activeCall;
      if (!call) return null;
      const selfVideoState = call.selfVideoState ?? null;
      const peerVideoState = call.peerVideoState ?? null;
      const names = window.WAVoipCaptureCore.VIDEO_STATES;
      return {
        id: call.id || null,
        peerJid: call.peerJid?.toString?.() || null,
        outgoing: Boolean(call.outgoing),
        isVideo: Boolean(call.isVideo),
        state: typeof call.getState === 'function' ? call.getState() : null,
        selfVideoState,
        selfVideoStateName: selfVideoState == null ? null : (names[selfVideoState] || `Unknown(${selfVideoState})`),
        peerVideoState,
        peerVideoStateName: peerVideoState == null ? null : (names[peerVideoState] || `Unknown(${peerVideoState})`),
        peerVideoJid: call.peerVideoJid?.toString?.() || null,
        selfMicMuted: Boolean(call.selfMicMuted),
        peerMicMuted: Boolean(call.peerMicMuted),
      };
    } catch {
      return null;
    }
  }

  function emit(event) {
    if (window.__waVoipDiagnostic?.emit) {
      window.__waVoipDiagnostic.emit(event);
      return;
    }
    window.postMessage({ type: 'WA_VOIP_CAPTURE_EVENT', payload: event }, '*');
  }

  function captureSignaling(stage, node, wireBytes) {
    try {
      const event = window.WAVoipCaptureCore.extractSignaling(stage, node, wireBytes);
      if (event) emit({ ...event, call: activeCallSnapshot() });
    } catch (error) {
      emit({
        source: 'capture',
        event: 'error',
        operation: `${stage}-signaling`,
        error: String(error?.stack || error),
      });
    }
  }

  function deferCapture(callback) {
    setTimeout(() => {
      try { callback(); } catch (error) {
        emit({ source: 'capture', event: 'error', operation: 'deferred-capture', error: String(error?.stack || error) });
      }
    }, 0);
  }

  function wrapWap(wap) {
    if (wap.__waVoipCaptureWrapped) return;
    Object.defineProperty(wap, '__waVoipCaptureWrapped', { value: true });

    const originalEncode = wap.encodeStanza;
    wap.encodeStanza = function (...args) {
      const encoded = Reflect.apply(originalEncode, this, args);
      deferCapture(() => captureSignaling('encode', args[0], encoded));
      return encoded;
    };

    const originalDecode = wap.decodeStanza;
    wap.decodeStanza = function (...args) {
      const result = Reflect.apply(originalDecode, this, args);
      if (result && typeof result.then === 'function') {
        result.then(
          decoded => deferCapture(() => captureSignaling('decode', decoded, args[0])),
          error => deferCapture(() => emit({
            source: 'capture', event: 'error', operation: 'decode-signaling',
            error: String(error?.stack || error),
          })),
        );
        return result;
      }
      deferCapture(() => captureSignaling('decode', result, args[0]));
      return result;
    };

    emit({ source: 'capture', event: 'ready', hook: 'WAWap.encodeStanza/decodeStanza' });
  }

  function wrapStackMethod(stack, method) {
    const original = stack[method];
    if (typeof original !== 'function' || original.__waVoipCaptureWrapped) return;

    function wrapped(...args) {
      const startedAt = performance.now();
      let result;
      try {
        result = Reflect.apply(original, this, args);
      } catch (error) {
        emit({
          source: 'voip-stack', event: 'method', phase: 'error', method,
          durationMs: performance.now() - startedAt,
          error: String(error?.stack || error), call: activeCallSnapshot(),
        });
        throw error;
      }

      deferCapture(() => emit({
        source: 'voip-stack', event: 'method', phase: 'call', method,
        args: safeValue(args), call: activeCallSnapshot(),
      }));

      if (result && typeof result.then === 'function') {
        result.then(
          value => deferCapture(() => emit({
            source: 'voip-stack', event: 'method', phase: 'result', method,
            durationMs: performance.now() - startedAt,
            result: safeValue(value), call: activeCallSnapshot(),
          })),
          error => deferCapture(() => emit({
            source: 'voip-stack', event: 'method', phase: 'error', method,
            durationMs: performance.now() - startedAt,
            error: String(error?.stack || error), call: activeCallSnapshot(),
          })),
        );
      } else {
        deferCapture(() => emit({
          source: 'voip-stack', event: 'method', phase: 'result', method,
          durationMs: performance.now() - startedAt,
          result: safeValue(result), call: activeCallSnapshot(),
        }));
      }
      return result;
    }

    Object.defineProperty(wrapped, '__waVoipCaptureWrapped', { value: true });
    stack[method] = wrapped;
  }

  function wrapStack(stack) {
    if (!stack || wrappedStacks.has(stack)) return;
    wrappedStacks.add(stack);
    const methodNames = new Set();
    let current = stack;
    for (let depth = 0; current && depth < 5; depth++, current = Object.getPrototypeOf(current)) {
      for (const name of Object.getOwnPropertyNames(current)) {
        if (name !== 'constructor') methodNames.add(name);
      }
    }
    const diagnosticMethods = Array.from(methodNames).filter(method =>
      /call|video|audio|camera|microphone|mute|upgrade|stream|reaction/i.test(method)
      && !/packet|frame|sample|process|handle|dispatch|emit|callback|listener/i.test(method)
    );
    for (const method of diagnosticMethods) {
      try { wrapStackMethod(stack, method); } catch (error) {
        emit({ source: 'capture', event: 'hook-error', hook: 'WAWebVoipStackInterface', method, error: String(error?.stack || error) });
      }
    }
    emit({
      source: 'capture', event: 'ready', hook: 'WAWebVoipStackInterface',
      stackType: stack.type || null, methods: diagnosticMethods.sort(),
      ownProperties: Object.getOwnPropertyNames(stack),
    });
  }

  async function pollStack() {
    const stackModule = await waitFor(() => window.require('WAWebVoipStackInterface'));
    for (;;) {
      try { wrapStack(await stackModule.getVoipStackInterface()); } catch {}
      await sleep(1000);
    }
  }

  function pollCallModel() {
    setInterval(() => {
      const call = activeCallSnapshot();
      const snapshot = JSON.stringify(call);
      if (snapshot === previousModelSnapshot) return;
      previousModelSnapshot = snapshot;
      emit({ source: 'call-model', event: 'state', call });
    }, 500);
  }

  async function hookWhatsAppLogger() {
    const loggerModule = await waitFor(() => window.require('WAWebLoggerImpl'));
    const logger = loggerModule.Logger;
    if (!logger || typeof logger.logImpl !== 'function' || logger.logImpl.__waVoipCaptureWrapped) return;
    const original = logger.logImpl;
    function wrapped(...args) {
      const result = Reflect.apply(original, this, args);
      const [level, message, arg1, arg2, area] = args;
      const text = [area, message, arg1, arg2].filter(value => value != null).map(String).join(' ');
      if (/voip|call|video|audio|camera|microphone|relay|rtp|rtcp|stun|warp|srtp|datachannel/i.test(text)) {
        deferCapture(() => emit({
          source: 'wa-logger', event: 'log', level, message: safeValue(message),
          arg1: safeValue(arg1), arg2: safeValue(arg2), area: safeValue(area),
        }));
      }
      return result;
    }
    Object.defineProperty(wrapped, '__waVoipCaptureWrapped', { value: true });
    logger.logImpl = wrapped;
    emit({ source: 'capture', event: 'ready', hook: 'WAWebLoggerImpl.Logger.logImpl' });
  }

  async function main() {
    await waitFor(() => window.WAVoipCaptureCore && typeof window.require === 'function');
    const wap = await waitFor(() => window.require('WAWap'));
    wrapWap(wap);
    pollCallModel();
    void pollStack().catch(error => emit({
      source: 'capture', event: 'error', operation: 'poll-stack',
      error: String(error?.stack || error),
    }));
    void hookWhatsAppLogger().catch(error => emit({
      source: 'capture', event: 'error', operation: 'hook-logger',
      error: String(error?.stack || error),
    }));

    for (const name of ['click', 'pointerdown', 'pointerup', 'keydown', 'keyup', 'visibilitychange', 'orientationchange']) {
      window.addEventListener(name, event => emit({
        source: 'page-input', event: name,
        target: event.target ? {
          tagName: event.target.tagName || null,
          id: event.target.id || null,
          className: String(event.target.className || ''),
          ariaLabel: event.target.getAttribute?.('aria-label') || null,
          title: event.target.getAttribute?.('title') || null,
        } : null,
        key: event.key || null, code: event.code || null,
        button: event.button ?? null, buttons: event.buttons ?? null,
        clientX: event.clientX ?? null, clientY: event.clientY ?? null,
        visibilityState: document.visibilityState,
        screenOrientation: safeValue(screen.orientation),
      }), true);
    }
  }

  main().catch(error => emit({
    source: 'capture',
    event: 'error',
    operation: 'install',
    error: String(error?.stack || error),
  }));
})();
