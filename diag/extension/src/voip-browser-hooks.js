(function installVoipBrowserHooks() {
  'use strict';

  if (window.__waVoipBrowserHooksInstalled) return;
  window.__waVoipBrowserHooksInstalled = true;

  const state = window.__waVoipDiagnosticState || {
    sessionId: crypto.randomUUID(),
    sequence: 0,
  };
  window.__waVoipDiagnosticState = state;

  function fullValue(value, depth = 0, seen = new WeakSet(), budget = { properties: 20000 }) {
    if (value == null || typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') return value;
    if (typeof value === 'bigint') return value.toString();
    if (typeof value === 'function') return `[function ${value.name || 'anonymous'}]`;
    const core = window.WAVoipCaptureCore;
    if (core?.isBytes(value)) return { $binary: core.binaryValue(value) };
    if (typeof Blob !== 'undefined' && value instanceof Blob) {
      return { $blob: { size: value.size, type: value.type } };
    }
    if (value instanceof Error) {
      return { name: value.name, message: value.message, stack: value.stack, cause: fullValue(value.cause, depth + 1, seen, budget) };
    }
    if (depth >= 6) return '[max depth]';
    if (typeof value !== 'object') return String(value);
    if (seen.has(value)) return '[circular]';
    seen.add(value);
    if (Array.isArray(value)) {
      const output = value.map(item => fullValue(item, depth + 1, seen, budget));
      seen.delete(value);
      return output;
    }
    const output = {};
    for (const key of Object.getOwnPropertyNames(value)) {
      if (budget.properties-- <= 0) {
        output.$truncated = 'property budget exhausted';
        break;
      }
      try {
        const descriptor = Object.getOwnPropertyDescriptor(value, key);
        output[key] = descriptor && 'value' in descriptor
          ? fullValue(descriptor.value, depth + 1, seen, budget)
          : '[accessor]';
      } catch (error) {
        output[key] = `[unreadable: ${error}]`;
      }
    }
    seen.delete(value);
    return output;
  }

  function emit(event) {
    const ts = Date.now();
    const payload = {
      schema: 'wa-voip-diag/v2',
      sessionId: state.sessionId,
      sequence: ++state.sequence,
      ts,
      monotonicMs: performance.now(),
      isoTime: new Date(ts).toISOString(),
      webVersion: window.Debug?.VERSION || null,
      pageUrl: location.href,
      ...event,
    };
    window.postMessage({ type: 'WA_VOIP_CAPTURE_EVENT', payload }, '*');
  }

  window.__waVoipDiagnostic = { emit, fullValue };

  function packetDetails(value) {
    const bytes = window.WAVoipCaptureCore?.bytesOf(value);
    if (!bytes) return { payload: fullValue(value), packet: null };
    const packet = { byteLength: bytes.length, kind: 'unknown' };
    if (bytes.length >= 20 && (bytes[0] & 0xc0) === 0) {
      packet.kind = 'stun';
      packet.messageType = (bytes[0] << 8) | bytes[1];
      packet.messageLength = (bytes[2] << 8) | bytes[3];
      packet.magicCookie = window.WAVoipCaptureCore.bytesToHex(bytes.slice(4, 8));
      packet.transactionId = window.WAVoipCaptureCore.bytesToHex(bytes.slice(8, 20));
    } else if (bytes.length >= 12 && (bytes[0] & 0xc0) === 0x80) {
      const payloadType = bytes[1] & 0x7f;
      packet.kind = payloadType >= 64 && payloadType <= 95 ? 'rtcp-or-reserved' : 'rtp';
      packet.marker = Boolean(bytes[1] & 0x80);
      packet.payloadType = payloadType;
      packet.sequenceNumber = (bytes[2] << 8) | bytes[3];
      packet.timestamp = ((bytes[4] << 24) | (bytes[5] << 16) | (bytes[6] << 8) | bytes[7]) >>> 0;
      packet.ssrc = ((bytes[8] << 24) | (bytes[9] << 16) | (bytes[10] << 8) | bytes[11]) >>> 0;
    }
    return { payload: { $binary: window.WAVoipCaptureCore.binaryValue(bytes) }, packet };
  }

  async function dataDetails(value) {
    if (typeof Blob !== 'undefined' && value instanceof Blob) {
      const buffer = await value.arrayBuffer();
      return { ...packetDetails(buffer), blobType: value.type };
    }
    return packetDetails(value);
  }

  let nextPcId = 0;
  let nextChannelId = 0;
  const peerConnections = new Map();
  const instrumentedChannels = new WeakSet();

  function trackDetails(track) {
    if (!track) return null;
    let settings = null;
    let constraints = null;
    let capabilities = null;
    try { settings = fullValue(track.getSettings?.()); } catch {}
    try { constraints = fullValue(track.getConstraints?.()); } catch {}
    try { capabilities = fullValue(track.getCapabilities?.()); } catch {}
    return {
      id: track.id, kind: track.kind, label: track.label,
      enabled: track.enabled, muted: track.muted, readyState: track.readyState,
      contentHint: track.contentHint, settings, constraints, capabilities,
    };
  }

  function instrumentTrack(track, context) {
    if (!track || track.__waVoipDiagnosticTrack) return;
    try { Object.defineProperty(track, '__waVoipDiagnosticTrack', { value: true }); } catch {}
    for (const name of ['ended', 'mute', 'unmute']) {
      track.addEventListener?.(name, () => emit({
        source: 'media-track', event: name, context, track: trackDetails(track),
      }));
    }
    emit({ source: 'media-track', event: 'observed', context, track: trackDetails(track) });
  }

  function instrumentDataChannel(channel, pcId, origin) {
    if (!channel || instrumentedChannels.has(channel)) return channel;
    instrumentedChannels.add(channel);
    const channelId = ++nextChannelId;
    const metadata = () => ({
      pcId, channelId, origin, label: channel.label, id: channel.id,
      protocol: channel.protocol, negotiated: channel.negotiated,
      ordered: channel.ordered, maxPacketLifeTime: channel.maxPacketLifeTime,
      maxRetransmits: channel.maxRetransmits, readyState: channel.readyState,
      bufferedAmount: channel.bufferedAmount, binaryType: channel.binaryType,
    });
    const originalSend = channel.send;
    channel.send = function (data) {
      const result = Reflect.apply(originalSend, this, [data]);
      setTimeout(() => void dataDetails(data).then(details => emit({
        source: 'rtc-datachannel', event: 'send', channel: metadata(), ...details,
      })), 0);
      return result;
    };
    channel.addEventListener('message', event => {
      void dataDetails(event.data).then(details => emit({
        source: 'rtc-datachannel', event: 'receive', channel: metadata(), ...details,
      }));
    });
    for (const name of ['open', 'close', 'closing', 'error', 'bufferedamountlow']) {
      channel.addEventListener(name, event => emit({
        source: 'rtc-datachannel', event: name, channel: metadata(),
        error: event?.error ? String(event.error?.stack || event.error) : null,
      }));
    }
    emit({ source: 'rtc-datachannel', event: 'created', channel: metadata() });
    return channel;
  }

  function descriptionValue(description) {
    if (!description) return null;
    return { type: description.type, sdp: description.sdp };
  }

  function instrumentPeerConnection(pc, configuration) {
    const pcId = ++nextPcId;
    peerConnections.set(pcId, pc);
    emit({ source: 'peer-connection', event: 'created', pcId, configuration: fullValue(configuration) });

    const stateSnapshot = () => ({
      connectionState: pc.connectionState,
      signalingState: pc.signalingState,
      iceConnectionState: pc.iceConnectionState,
      iceGatheringState: pc.iceGatheringState,
      localDescription: descriptionValue(pc.localDescription),
      remoteDescription: descriptionValue(pc.remoteDescription),
      currentLocalDescription: descriptionValue(pc.currentLocalDescription),
      currentRemoteDescription: descriptionValue(pc.currentRemoteDescription),
    });
    for (const name of ['connectionstatechange', 'signalingstatechange', 'iceconnectionstatechange', 'icegatheringstatechange', 'negotiationneeded']) {
      pc.addEventListener(name, () => emit({ source: 'peer-connection', event: name, pcId, state: stateSnapshot() }));
    }
    pc.addEventListener('icecandidate', event => emit({
      source: 'peer-connection', event: 'icecandidate', pcId,
      candidate: event.candidate?.toJSON?.() || fullValue(event.candidate),
      url: event.url || null,
    }));
    pc.addEventListener('icecandidateerror', event => emit({
      source: 'peer-connection', event: 'icecandidateerror', pcId,
      address: event.address, port: event.port, url: event.url,
      errorCode: event.errorCode, errorText: event.errorText,
    }));
    pc.addEventListener('track', event => {
      instrumentTrack(event.track, { pcId, direction: 'remote' });
      emit({
        source: 'peer-connection', event: 'track', pcId,
        track: trackDetails(event.track), streams: event.streams?.map(stream => ({ id: stream.id })) || [],
        receiver: fullValue(event.receiver), transceiver: fullValue(event.transceiver),
      });
    });
    pc.addEventListener('datachannel', event => instrumentDataChannel(event.channel, pcId, 'remote'));

    const createDataChannel = pc.createDataChannel;
    pc.createDataChannel = function (...args) {
      return instrumentDataChannel(Reflect.apply(createDataChannel, this, args), pcId, 'local');
    };
    for (const method of ['createOffer', 'createAnswer', 'setLocalDescription', 'setRemoteDescription', 'addIceCandidate', 'addTrack', 'addTransceiver', 'restartIce', 'close']) {
      const original = pc[method];
      if (typeof original !== 'function') continue;
      pc[method] = function (...args) {
        const before = stateSnapshot();
        let result;
        try { result = Reflect.apply(original, this, args); }
        catch (error) {
          emit({ source: 'peer-connection', event: 'method-error', pcId, method, error: String(error?.stack || error) });
          throw error;
        }
        setTimeout(() => emit({
          source: 'peer-connection', event: 'method-call', pcId, method,
          args: fullValue(args), state: before,
        }), 0);
        Promise.resolve(result).then(
          value => emit({ source: 'peer-connection', event: 'method-result', pcId, method, result: fullValue(value), state: stateSnapshot() }),
          error => emit({ source: 'peer-connection', event: 'method-error', pcId, method, error: String(error?.stack || error), state: stateSnapshot() }),
        );
        return result;
      };
    }
    return pc;
  }

  function replaceConstructor(name, instrument) {
    const Native = window[name];
    if (typeof Native !== 'function') return;
    window[name] = new Proxy(Native, {
      construct(target, args, newTarget) {
        return instrument(Reflect.construct(target, args, newTarget), args);
      },
    });
  }

  replaceConstructor('RTCPeerConnection', (pc, args) => instrumentPeerConnection(pc, args[0]));
  if (window.webkitRTCPeerConnection && window.webkitRTCPeerConnection !== window.RTCPeerConnection) {
    replaceConstructor('webkitRTCPeerConnection', (pc, args) => instrumentPeerConnection(pc, args[0]));
  }

  const mediaDevices = navigator.mediaDevices;
  if (mediaDevices?.getUserMedia) {
    const originalGetUserMedia = mediaDevices.getUserMedia.bind(mediaDevices);
    mediaDevices.getUserMedia = function (constraints) {
      const result = originalGetUserMedia(constraints);
      setTimeout(() => emit({ source: 'media-devices', event: 'getUserMedia-call', constraints: fullValue(constraints) }), 0);
      result.then(stream => {
        const tracks = stream.getTracks().map(track => {
          instrumentTrack(track, { streamId: stream.id, direction: 'local' });
          return trackDetails(track);
        });
        emit({ source: 'media-devices', event: 'getUserMedia-result', streamId: stream.id, active: stream.active, tracks });
      }, error => {
        emit({ source: 'media-devices', event: 'getUserMedia-error', constraints: fullValue(constraints), error: String(error?.stack || error) });
      });
      return result;
    };
    mediaDevices.addEventListener?.('devicechange', async () => {
      let devices = null;
      try { devices = fullValue(await mediaDevices.enumerateDevices()); } catch {}
      emit({ source: 'media-devices', event: 'devicechange', devices });
    });
  }

  for (const method of ['getDisplayMedia', 'enumerateDevices', 'selectAudioOutput', 'getSupportedConstraints']) {
    const original = mediaDevices?.[method];
    if (typeof original !== 'function') continue;
    mediaDevices[method] = function (...args) {
      emit({ source: 'media-devices', event: `${method}-call`, args: fullValue(args) });
      let result;
      try { result = Reflect.apply(original, this, args); }
      catch (error) {
        emit({ source: 'media-devices', event: `${method}-error`, error: String(error?.stack || error) });
        throw error;
      }
      Promise.resolve(result).then(value => emit({
        source: 'media-devices', event: `${method}-result`, result: fullValue(value),
      }), error => emit({
        source: 'media-devices', event: `${method}-error`, error: String(error?.stack || error),
      }));
      return result;
    };
  }

  for (const name of ['AudioContext', 'webkitAudioContext']) {
    replaceConstructor(name, (context, args) => {
      const contextId = crypto.randomUUID();
      const snapshot = () => ({
        contextId, state: context.state, sampleRate: context.sampleRate,
        baseLatency: context.baseLatency, outputLatency: context.outputLatency,
        currentTime: context.currentTime, sinkId: context.sinkId,
      });
      context.addEventListener?.('statechange', () => emit({ source: 'audio-context', event: 'statechange', context: snapshot() }));
      emit({ source: 'audio-context', event: 'created', args: fullValue(args), context: snapshot() });
      return context;
    });
  }

  setInterval(async () => {
    for (const [pcId, pc] of peerConnections) {
      if (pc.connectionState === 'closed') {
        peerConnections.delete(pcId);
        continue;
      }
      try {
        const report = await pc.getStats();
        const stats = [];
        report.forEach(stat => stats.push(fullValue(stat)));
        emit({
          source: 'peer-connection', event: 'stats', pcId, stats,
          state: {
            connectionState: pc.connectionState,
            signalingState: pc.signalingState,
            iceConnectionState: pc.iceConnectionState,
            iceGatheringState: pc.iceGatheringState,
          },
          senders: fullValue(pc.getSenders?.()),
          receivers: fullValue(pc.getReceivers?.()),
          transceivers: fullValue(pc.getTransceivers?.()),
        });
      } catch (error) {
        emit({ source: 'peer-connection', event: 'stats-error', pcId, error: String(error?.stack || error) });
      }
    }
  }, 500);
  window.addEventListener('error', event => emit({
    source: 'window', event: 'error', message: event.message,
    filename: event.filename, lineno: event.lineno, colno: event.colno,
    error: fullValue(event.error),
  }));
  window.addEventListener('unhandledrejection', event => emit({
    source: 'window', event: 'unhandledrejection', reason: fullValue(event.reason),
  }));

  emit({
    source: 'capture', event: 'ready', hook: 'browser-media-transports',
    userAgent: navigator.userAgent, platform: navigator.platform,
    hardwareConcurrency: navigator.hardwareConcurrency,
  });
})();
