import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';
import test from 'node:test';

const source = readFileSync(new URL('../src/voip-capture-core.js', import.meta.url), 'utf8');
const context = {
  ArrayBuffer,
  DataView,
  Uint8Array,
};
context.globalThis = context;
runInNewContext(source, context);
const core = context.WAVoipCaptureCore;

test('extracts an independent video upgrade state', () => {
  const node = {
    tag: 'call',
    attrs: { from: 'peer@lid', id: 'stanza-1' },
    content: [{
      tag: 'video',
      attrs: {
        'call-id': 'CALL123',
        'call-creator': 'peer@lid',
        state: '11',
        device_orientation: '3',
        dec: 'H264',
      },
    }],
  };

  const event = core.extractSignaling('in', node);
  assert.equal(event.callId, 'CALL123');
  assert.equal(event.signals[0].video.state, 11);
  assert.equal(event.signals[0].video.stateName, 'UpgradeRequestV2');
  assert.equal(event.signals[0].video.orientation, 3);
  assert.equal(event.signals[0].video.decoder, 'H264');
});

test('keeps complete relay secrets and binary settings', () => {
  const node = {
    tag: 'call',
    attrs: { to: 'peer@lid' },
    content: [{
      tag: 'offer',
      attrs: { 'call-id': 'CALL456' },
      content: [
        { tag: 'voip_settings', attrs: { uncompressed: '1' }, content: new Uint8Array([1, 2, 3]) },
        { tag: 'relay', attrs: { uuid: 'relay-1' }, content: [
          { tag: 'te2', attrs: { relay_id: '0' }, content: new Uint8Array([127, 0, 0, 1, 0x0d, 0x96]) },
          { tag: 'token', attrs: { id: '0' }, content: new Uint8Array([9, 8, 7]) },
        ] },
      ],
    }],
  };

  const event = core.extractSignaling('out', node);
  assert.match(event.rawXml, /<relay uuid="relay-1">/);
  assert.match(event.rawXml, /<te2 relay_id="0">/);
  assert.match(event.rawXml, /010203/);
  assert.match(event.rawXml, /090807/);
  assert.equal(event.relays[0].tokens[0].binary.hex, '090807');
  assert.equal(event.relays[0].tokens[0].binary.base64, 'CQgH');
  assert.deepEqual(
    JSON.parse(JSON.stringify(event.relays[0].endpoints[0].endpoint)),
    { family: 'ipv4', ip: '127.0.0.1', port: 3478 },
  );
  assert.equal(event.node.content[0].content[0].content.$binary.hex, '010203');
});

test('decodes IPv6 te2 endpoints', () => {
  const endpoint = core.endpointFromBytes(new Uint8Array([
    0x20, 0x01, 0x0d, 0xb8, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1,
    0x01, 0xbb,
  ]));
  assert.equal(endpoint.family, 'ipv6');
  assert.equal(endpoint.ip, '2001:db8:0:0:0:0:0:1');
  assert.equal(endpoint.port, 443);
});

test('ignores unrelated messaging stanzas', () => {
  const node = { tag: 'message', attrs: { id: 'message-1' }, content: [] };
  assert.equal(core.extractSignaling('in', node), null);
});
