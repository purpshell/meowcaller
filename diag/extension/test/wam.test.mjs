import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { runInNewContext } from 'node:vm';
import test from 'node:test';

const source = readFileSync(new URL('../src/wam.js', import.meta.url), 'utf8');

test('decodes little-endian sequence numbers and preserves unknown globals', () => {
  const context = { ArrayBuffer, DataView, TextDecoder, Uint8Array };
  context.window = context;
  runInNewContext(source, context);
  context.injectWAM();

  const payload = new Uint8Array([
    0x57, 0x41, 0x4d,
    1,
    0,
    0x34, 0x12,
    0,
    0x10, 250,
    0x31, 251, 0xff,
  ]);
  const decoded = context.WAMRunner.processPayload(payload);

  assert.equal(decoded.header.sequenceNumber, 0x1234);
  assert.equal(decoded.rawEvents[0].name, 'unknown_event');
  assert.equal(decoded.rawEvents[0].globals.unknown_global_250, 0);
});
