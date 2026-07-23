// MV3 service worker. Streams focused page, worker, and CDP diagnostics to localhost.

const contentPorts = new Map();
const VOIP_CAPTURE_ENDPOINT = 'http://127.0.0.1:3219/events';
const CDP_VERSION = '1.3';
const CDP_BINDING = '__waVoipCdpEmit';
const MAX_BATCH_BYTES = 8 * 1024 * 1024;
const MAX_BATCH_EVENTS = 100;
const MAX_QUEUE_BYTES = 64 * 1024 * 1024;
const MAX_QUEUE_EVENTS = 5000;

const voipCaptureQueue = [];
const attachedTabs = new Set();
let voipCaptureQueueBytes = 0;
let voipCaptureFlushTimer = null;
let voipCaptureFlushing = false;
let workerHookSourcePromise = null;

function nowEnvelope(event) {
  const ts = Date.now();
  return {
    schema: 'wa-voip-diag/v2',
    ts,
    isoTime: new Date(ts).toISOString(),
    ...event,
  };
}

function sendVoipCaptureEvent(event, tabId) {
  const captured = { ...event, tabId: tabId ?? event.tabId ?? null };
  let bytes;
  try { bytes = JSON.stringify(captured).length + 1; }
  catch { return; }
  voipCaptureQueue.push({ captured, bytes });
  voipCaptureQueueBytes += bytes;
  trimCaptureQueue();
  scheduleVoipCaptureFlush();
}

function trimCaptureQueue() {
  while (voipCaptureQueue.length > MAX_QUEUE_EVENTS || voipCaptureQueueBytes > MAX_QUEUE_BYTES) {
    const dropped = voipCaptureQueue.shift();
    if (!dropped) break;
    voipCaptureQueueBytes -= dropped.bytes;
  }
}

function scheduleVoipCaptureFlush(delay = 25) {
  if (voipCaptureFlushTimer != null) return;
  voipCaptureFlushTimer = setTimeout(() => {
    voipCaptureFlushTimer = null;
    void flushVoipCaptureEvents();
  }, delay);
}

function takeCaptureBatch() {
  const batch = [];
  let bytes = 2;
  while (voipCaptureQueue.length > 0 && batch.length < MAX_BATCH_EVENTS) {
    const next = voipCaptureQueue[0];
    if (batch.length > 0 && bytes + next.bytes > MAX_BATCH_BYTES) break;
    voipCaptureQueue.shift();
    voipCaptureQueueBytes -= next.bytes;
    batch.push(next.captured);
    bytes += next.bytes;
  }
  return batch;
}

async function flushVoipCaptureEvents() {
  if (voipCaptureFlushing || voipCaptureQueue.length === 0) return;
  voipCaptureFlushing = true;
  const batch = takeCaptureBatch();
  try {
    const response = await fetch(VOIP_CAPTURE_ENDPOINT, {
      method: 'POST',
      headers: { 'content-type': 'application/json' },
      body: JSON.stringify(batch),
    });
    if (!response.ok) throw new Error(`collector returned ${response.status}`);
  } catch {
    const restored = batch.map(captured => {
      const bytes = JSON.stringify(captured).length + 1;
      voipCaptureQueueBytes += bytes;
      return { captured, bytes };
    });
    voipCaptureQueue.unshift(...restored);
    trimCaptureQueue();
    scheduleVoipCaptureFlush(1000);
  } finally {
    voipCaptureFlushing = false;
    if (voipCaptureQueue.length > 0) scheduleVoipCaptureFlush();
  }
}

function debuggerError() {
  return chrome.runtime.lastError?.message || null;
}

function attachDebugger(target) {
  return new Promise((resolve, reject) => {
    chrome.debugger.attach(target, CDP_VERSION, () => {
      const error = debuggerError();
      if (error) reject(new Error(error));
      else resolve();
    });
  });
}

function sendCommand(target, method, params = {}) {
  return new Promise((resolve, reject) => {
    chrome.debugger.sendCommand(target, method, params, result => {
      const error = debuggerError();
      if (error) reject(new Error(error));
      else resolve(result);
    });
  });
}

async function workerHookSource() {
  if (!workerHookSourcePromise) {
    workerHookSourcePromise = fetch(chrome.runtime.getURL('src/voip-worker-hooks.js')).then(response => response.text());
  }
  return workerHookSourcePromise;
}

function shouldCaptureCdpEvent(method) {
  return method === 'Runtime.exceptionThrown' ||
    method === 'Log.entryAdded' ||
    method === 'Performance.metrics' ||
    method === 'Security.certificateError' ||
    method === 'Target.targetCrashed' ||
    method === 'Target.detachedFromTarget' ||
    method === 'Network.loadingFailed' ||
    method.startsWith('Network.webSocket') ||
    method.startsWith('Network.webTransport') ||
    method.startsWith('Network.directTCPSocket') ||
    method.startsWith('Network.directUDPSocket') ||
    method.startsWith('Media.') ||
    method.startsWith('WebAudio.');
}

async function recordCommand(target, method, params = {}) {
  try {
    return await sendCommand(target, method, params);
  } catch (error) {
    sendVoipCaptureEvent(nowEnvelope({
      source: 'cdp-command', event: 'error', cdpMethod: method,
      cdpSessionId: target.sessionId || null, params,
      error: String(error?.stack || error),
    }), target.tabId);
    return null;
  }
}

async function installRuntimeHook(target) {
  await recordCommand(target, 'Runtime.addBinding', { name: CDP_BINDING });
  const source = await workerHookSource();
  await recordCommand(target, 'Runtime.evaluate', {
    expression: source,
    includeCommandLineAPI: false,
    awaitPromise: false,
    returnByValue: false,
  });
}

async function enableTarget(target, isRoot = false) {
  const commands = [
    ['Runtime.enable'],
    ['Log.enable'],
    ['Network.enable', {
      maxTotalBufferSize: 64 * 1024 * 1024,
      maxResourceBufferSize: 8 * 1024 * 1024,
      maxPostDataSize: 8 * 1024 * 1024,
      reportDirectSocketTraffic: true,
      enableDurableMessages: true,
    }],
    ['Performance.enable'],
    ['Security.enable'],
    ['Audits.enable'],
    ['Media.enable'],
    ['WebAudio.enable'],
    ['ServiceWorker.enable'],
  ];
  if (isRoot) {
    commands.push(
      ['Page.enable'],
      ['DOMStorage.enable'],
      ['Target.setDiscoverTargets', { discover: true }],
    );
  }
  commands.push(['Target.setAutoAttach', {
    autoAttach: true,
    waitForDebuggerOnStart: false,
    flatten: true,
  }]);
  for (const [method, params] of commands) await recordCommand(target, method, params);
  await installRuntimeHook(target);
  await recordCommand(target, 'Runtime.runIfWaitingForDebugger');
}

async function ensureDebuggerAttached(tabId) {
  if (!tabId || attachedTabs.has(tabId)) return;
  const target = { tabId };
  try {
    await attachDebugger(target);
    attachedTabs.add(tabId);
    sendVoipCaptureEvent(nowEnvelope({ source: 'cdp', event: 'attached' }), tabId);
    await enableTarget(target, true);
  } catch (error) {
    sendVoipCaptureEvent(nowEnvelope({
      source: 'cdp', event: 'attach-error', error: String(error?.stack || error),
    }), tabId);
  }
}

chrome.debugger.onEvent.addListener((target, method, params) => {
  if (shouldCaptureCdpEvent(method)) {
    sendVoipCaptureEvent(nowEnvelope({
      source: 'cdp', event: 'protocol-event', cdpMethod: method,
      cdpSessionId: target.sessionId || null, params,
    }), target.tabId);
  }

  if (method === 'Target.attachedToTarget' && params.sessionId) {
    const child = { tabId: target.tabId, sessionId: params.sessionId };
    void enableTarget(child);
  } else if (method === 'Runtime.bindingCalled' && params.name === CDP_BINDING) {
    let payload = params.payload;
    try { payload = JSON.parse(payload); } catch {}
    sendVoipCaptureEvent(nowEnvelope({
      source: 'worker-hook', event: 'binding',
      cdpSessionId: target.sessionId || null,
      executionContextId: params.executionContextId, payload,
    }), target.tabId);
  }
});

chrome.debugger.onDetach.addListener((target, reason) => {
  if (target.tabId) attachedTabs.delete(target.tabId);
  sendVoipCaptureEvent(nowEnvelope({ source: 'cdp', event: 'detached', reason }), target.tabId);
});

chrome.tabs.onUpdated.addListener((tabId, changeInfo, tab) => {
  if (changeInfo.status === 'loading' && tab.url?.startsWith('https://web.whatsapp.com/')) {
    void ensureDebuggerAttached(tabId);
  }
});

chrome.tabs.onRemoved.addListener(tabId => {
  attachedTabs.delete(tabId);
  contentPorts.delete(tabId);
});

void chrome.tabs.query({ url: 'https://web.whatsapp.com/*' }, tabs => {
  for (const tab of tabs) void ensureDebuggerAttached(tab.id);
});


chrome.runtime.onConnect.addListener(port => {
  if (port.name !== 'walog-content') return;
  const tabId = port.sender?.tab?.id;
  if (!tabId) {
    port.disconnect();
    return;
  }
  contentPorts.set(tabId, port);
  void ensureDebuggerAttached(tabId);

  port.onMessage.addListener(message => {
    if (message?.type === 'WA_VOIP_CAPTURE_EVENT') {
      sendVoipCaptureEvent(message.payload || {}, tabId);
    } else if (message?.type === 'WA_LOGGER_LOG') {
      sendVoipCaptureEvent(nowEnvelope({
        source: 'wa-logger-raw',
        event: 'row',
        row: message.payload,
      }), tabId);
    }
  });
  port.onDisconnect.addListener(() => contentPorts.delete(tabId));
});
