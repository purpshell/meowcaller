import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';
import test from 'node:test';

const coreSource = readFileSync(new URL('../src/voip-capture-core.js', import.meta.url), 'utf8');
const hookSource = readFileSync(new URL('../src/voip-capture.js', import.meta.url), 'utf8');

test('observes signaling and stack methods without invoking call controls', async () => {
  const posted = [];
  let upgradeCalls = 0;
  const nativeUpgradeResult = Promise.resolve(0);
  const call = {
    id: 'CALL789',
    peerJid: { toString: () => 'peer@lid' },
    outgoing: true,
    isVideo: false,
    selfVideoState: 0,
    peerVideoState: 0,
    selfMicMuted: false,
    peerMicMuted: false,
    getState: () => 6,
  };
  const stack = {
    type: 'web',
    requestVideoUpgrade() {
      upgradeCalls++;
      return nativeUpgradeResult;
    },
  };
  const wap = {
    encodeStanza: node => `encoded:${node.tag}`,
    decodeStanza: value => Promise.resolve(value),
  };
  const modules = {
    WAWap: wap,
    WAWebCallCollection: { activeCall: call },
    WAWebVoipStackInterface: { getVoipStackInterface: async () => stack },
  };
  const unrefTimeout = (callback, delay) => {
    const timer = setTimeout(callback, delay);
    timer.unref();
    return timer;
  };
  const context = {
    ArrayBuffer,
    DataView,
    Uint8Array,
    console,
    crypto: { randomUUID },
    performance,
    setInterval: () => 0,
    setTimeout: unrefTimeout,
    clearTimeout,
    Debug: { VERSION: '2.3000.0' },
    postMessage: message => posted.push(message),
    require: name => modules[name],
  };
  context.globalThis = context;
  context.window = context;

  runInNewContext(coreSource, context);
  runInNewContext(hookSource, context);
  await new Promise(resolve => setTimeout(resolve, 20));

  assert.equal(upgradeCalls, 0, 'installing the observer must not request an upgrade');
  const node = {
    tag: 'call',
    attrs: { to: 'peer@lid' },
    content: [{
      tag: 'video',
      attrs: { 'call-id': 'CALL789', state: '11', device_orientation: '1' },
    }],
  };
  assert.equal(wap.encodeStanza(node), 'encoded:call');
  await new Promise(resolve => setTimeout(resolve, 5));

  const signaling = posted.find(item =>
    item.type === 'WA_VOIP_CAPTURE_EVENT' && item.payload?.event === 'signaling');
  assert.equal(signaling.payload.direction, 'out');
  assert.equal(signaling.payload.callId, 'CALL789');
  assert.equal(signaling.payload.signals[0].video.stateName, 'UpgradeRequestV2');

  await assert.rejects(wap.decodeStanza(Promise.reject(new Error('decode failed'))), /decode failed/);
  await new Promise(resolve => setTimeout(resolve, 5));
  const decodeError = posted.find(item =>
    item.type === 'WA_VOIP_CAPTURE_EVENT' &&
    item.payload?.operation === 'decode-signaling');
  assert.match(decodeError.payload.error, /decode failed/);

  const upgradeResult = stack.requestVideoUpgrade();
  assert.equal(upgradeResult, nativeUpgradeResult, 'the observer must preserve promise identity');
  assert.equal(await upgradeResult, 0);
  assert.equal(upgradeCalls, 1, 'the wrapped method must call the original exactly once');
  await new Promise(resolve => setTimeout(resolve, 5));
  const methodEvents = posted.filter(item =>
    item.type === 'WA_VOIP_CAPTURE_EVENT' && item.payload?.method === 'requestVideoUpgrade');
  assert.deepEqual(methodEvents.map(item => item.payload.phase), ['call', 'result']);
});
