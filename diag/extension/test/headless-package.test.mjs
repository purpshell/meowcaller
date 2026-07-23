import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

const manifest = JSON.parse(readFileSync(new URL('../manifest.json', import.meta.url), 'utf8'));
const background = readFileSync(new URL('../src/bg.js', import.meta.url), 'utf8');
const content = readFileSync(new URL('../src/content-script.js', import.meta.url), 'utf8');
const main = readFileSync(new URL('../src/main.js', import.meta.url), 'utf8');

test('packages the v4 capture path without its UI or wslc workflow', () => {
  assert.deepEqual(manifest.permissions, ['tabs', 'debugger']);
  assert.equal(manifest.action, undefined);
  assert.equal(manifest.background.service_worker, 'src/bg.js');

  const resources = manifest.web_accessible_resources[0].resources;
  for (const path of [
    'src/wam.js',
    'src/voip-capture-core.js',
    'src/voip-browser-hooks.js',
    'src/main.js',
    'src/voip-capture.js',
  ]) {
    assert.ok(resources.includes(path), `${path} must be injected`);
  }
  assert.doesNotMatch(JSON.stringify(manifest), /viewer|composer|experiments|wslc/i);
  assert.match(background, /schema: 'wa-voip-diag\/v2'/);
  assert.doesNotMatch(background, /wa-voip-diag\/v3/);
  assert.match(background, /message\?\.type === 'WA_VOIP_CAPTURE_EVENT'/);
  assert.match(background, /message\?\.type === 'WA_LOGGER_LOG'/);
  assert.match(background, /MAX_QUEUE_BYTES = 64 \* 1024 \* 1024/);
  assert.match(background, /MAX_QUEUE_EVENTS = 5000/);
  assert.match(background, /trimCaptureQueue\(\)/);
  assert.match(background, /'src\/voip-worker-hooks\.js'/);
  assert.match(content, /'src\/wam\.js'/);
  assert.match(main, /window\.postMessage\(\{ type: 'WA_LOGGER_LOG', payload: log \}, window\.location\.origin\)/);
  assert.doesNotMatch(main, /corruptUint8Array/);
});
