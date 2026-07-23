(function installVoipCaptureCore(root) {
  'use strict';

  const VIDEO_STATES = Object.freeze({
    0: 'Disabled', 1: 'Enabled', 2: 'Paused', 3: 'UpgradeRequest',
    4: 'UpgradeAccept', 5: 'UpgradeReject', 6: 'Stopped',
    7: 'UpgradeRejectByTimeout', 8: 'UpgradeCancel',
    9: 'UpgradeCancelByTimeout', 10: 'UnknownPeer',
    11: 'UpgradeRequestV2', 20: 'Error',
  });

  const SIGNAL_TAGS = new Set([
    'accept', 'auth_token', 'enc_rekey', 'hbh_key', 'key', 'link',
    'mute', 'mute_v2', 'offer', 'offer_notice', 'preaccept', 'receipt',
    'reject', 'relay', 'relaylatency', 'te', 'te2', 'terminate', 'token',
    'transport', 'video', 'voip_settings',
  ]);

  function isBytes(value) {
    return value instanceof ArrayBuffer ||
      (ArrayBuffer.isView(value) && !(value instanceof DataView));
  }

  function bytesOf(value) {
    if (value instanceof ArrayBuffer) return new Uint8Array(value);
    if (ArrayBuffer.isView(value)) {
      return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
    }
    return null;
  }

  function bytesToHex(value) {
    const bytes = bytesOf(value);
    if (!bytes) return null;
    return Array.from(bytes, byte => byte.toString(16).padStart(2, '0')).join('');
  }

  function bytesToBase64(value) {
    const bytes = bytesOf(value);
    if (!bytes) return null;
    const alphabet = 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';
    let output = '';
    for (let index = 0; index < bytes.length; index += 3) {
      const a = bytes[index];
      const hasB = index + 1 < bytes.length;
      const hasC = index + 2 < bytes.length;
      const b = hasB ? bytes[index + 1] : 0;
      const c = hasC ? bytes[index + 2] : 0;
      output += alphabet[a >> 2];
      output += alphabet[((a & 3) << 4) | (b >> 4)];
      output += hasB ? alphabet[((b & 15) << 2) | (c >> 6)] : '=';
      output += hasC ? alphabet[c & 63] : '=';
    }
    return output;
  }

  function binaryValue(value) {
    const bytes = bytesOf(value);
    if (!bytes) return null;
    return {
      byteLength: bytes.byteLength,
      hex: bytesToHex(bytes),
      base64: bytesToBase64(bytes),
    };
  }

  function readValue(target, key) {
    if (target == null) return undefined;
    const value = target[key];
    if (typeof value !== 'function') return value;
    try { return value.call(target); } catch { return undefined; }
  }

  function tagOf(node) {
    const tag = readValue(node, 'tag');
    return tag == null ? '' : String(tag);
  }

  function attrsOf(node) {
    let attrs = readValue(node, 'attrs');
    if (attrs && typeof attrs.toJSON === 'function') {
      try { attrs = attrs.toJSON(); } catch {}
    }
    if (!attrs || typeof attrs !== 'object') return {};
    const normalized = {};
    for (const [key, value] of Object.entries(attrs)) {
      if (value == null || typeof value === 'function') continue;
      try { normalized[key] = String(value); } catch { normalized[key] = '[unprintable]'; }
    }
    return normalized;
  }

  function contentOf(node) {
    return readValue(node, 'content');
  }

  function childrenOf(node) {
    const content = contentOf(node);
    if (!Array.isArray(content)) return [];
    return content.filter(item => item && typeof item === 'object' && !isBytes(item));
  }

  function escapeText(value) {
    return String(value).replace(/[&<>]/g, char => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;',
    })[char]);
  }

  function escapeAttr(value) {
    return String(value).replace(/[&<>"']/g, char => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&apos;',
    })[char]);
  }

  function serializeNode(node, depth = 0, seen = new WeakSet()) {
    const indent = '  '.repeat(depth);
    if (node == null) return '';
    if (isBytes(node)) return `${indent}${bytesToHex(node)}`;
    if (typeof node !== 'object') return indent + escapeText(node);
    if (seen.has(node)) return `${indent}<!-- circular -->`;
    seen.add(node);

    const tag = tagOf(node) || 'node';
    const attrs = Object.entries(attrsOf(node))
      .map(([key, value]) => ` ${key}="${escapeAttr(value)}"`)
      .join('');
    const content = contentOf(node);
    if (content == null || content === '' || (Array.isArray(content) && content.length === 0)) {
      seen.delete(node);
      return `${indent}<${tag}${attrs} />`;
    }

    let body;
    if (Array.isArray(content)) {
      body = content.map(item => serializeNode(item, depth + 1, seen)).filter(Boolean).join('\n');
    } else if (isBytes(content)) {
      body = `${'  '.repeat(depth + 1)}${bytesToHex(content)}`;
    } else {
      body = `${'  '.repeat(depth + 1)}${escapeText(content)}`;
    }
    seen.delete(node);
    return `${indent}<${tag}${attrs}>\n${body}\n${indent}</${tag}>`;
  }

  function nodeToJSON(node, depth = 0, seen = new WeakSet()) {
    if (node == null) return null;
    if (isBytes(node)) return { $binary: binaryValue(node) };
    if (typeof node !== 'object') return node;
    if (seen.has(node)) return { $circular: true };
    if (depth > 16) return { $maxDepth: true };
    seen.add(node);
    const content = contentOf(node);
    let normalizedContent = null;
    if (Array.isArray(content)) {
      normalizedContent = content.map(item => nodeToJSON(item, depth + 1, seen));
    } else if (isBytes(content)) {
      normalizedContent = { $binary: binaryValue(content) };
    } else if (content != null) {
      normalizedContent = content;
    }
    const output = { tag: tagOf(node), attrs: attrsOf(node), content: normalizedContent };
    seen.delete(node);
    return output;
  }

  function walk(node, visit, depth = 0, seen = new WeakSet()) {
    if (!node || typeof node !== 'object' || seen.has(node) || depth > 16) return;
    seen.add(node);
    visit(node, depth);
    for (const child of childrenOf(node)) walk(child, visit, depth + 1, seen);
  }

  function numberAttr(attrs, ...names) {
    for (const name of names) {
      if (!(name in attrs)) continue;
      const value = Number(attrs[name]);
      if (Number.isFinite(value)) return value;
    }
    return null;
  }

  function describeVideo(attrs) {
    const state = numberAttr(attrs, 'state');
    return {
      state,
      stateName: state == null ? null : (VIDEO_STATES[state] || `Unknown(${state})`),
      orientation: numberAttr(attrs, 'device_orientation', 'orientation'),
      decoder: attrs.dec || null,
      encoder: attrs.enc || null,
      width: numberAttr(attrs, 'width', 'screen_width'),
      height: numberAttr(attrs, 'height', 'screen_height'),
    };
  }

  function endpointFromBytes(value) {
    const bytes = bytesOf(value);
    if (!bytes || (bytes.length !== 6 && bytes.length !== 18)) return null;
    const portOffset = bytes.length - 2;
    const port = (bytes[portOffset] << 8) | bytes[portOffset + 1];
    if (bytes.length === 6) {
      return { family: 'ipv4', ip: Array.from(bytes.slice(0, 4)).join('.'), port };
    }
    const groups = [];
    for (let index = 0; index < 16; index += 2) {
      groups.push(((bytes[index] << 8) | bytes[index + 1]).toString(16));
    }
    return { family: 'ipv6', ip: groups.join(':'), port };
  }

  function signalFromNode(node, depth) {
    const tag = tagOf(node);
    const attrs = attrsOf(node);
    if (!SIGNAL_TAGS.has(tag)) return null;
    if (tag === 'video' && depth > 1 && attrs.state == null && attrs['call-id'] == null) return null;
    const signal = {
      tag,
      attrs,
      childTags: childrenOf(node).map(tagOf).filter(Boolean),
    };
    if (tag === 'video') signal.video = describeVideo(attrs);
    const content = contentOf(node);
    if (isBytes(content)) signal.binary = binaryValue(content);
    else if (typeof content === 'string') signal.text = content;
    if (tag === 'te2' || tag === 'te') signal.endpoint = endpointFromBytes(content);
    return signal;
  }

  function relayFromNode(node) {
    const relay = { attrs: attrsOf(node), tokens: [], authTokens: [], endpoints: [] };
    for (const child of childrenOf(node)) {
      const tag = tagOf(child);
      const attrs = attrsOf(child);
      const content = contentOf(child);
      if (tag === 'token') relay.tokens.push({ attrs, binary: binaryValue(content), text: typeof content === 'string' ? content : null });
      else if (tag === 'auth_token') relay.authTokens.push({ attrs, binary: binaryValue(content), text: typeof content === 'string' ? content : null });
      else if (tag === 'key') relay.key = isBytes(content) ? { binary: binaryValue(content) } : { text: content == null ? null : String(content) };
      else if (tag === 'hbh_key') relay.hbhKey = isBytes(content) ? { binary: binaryValue(content) } : { text: content == null ? null : String(content) };
      else if (tag === 'te2' || tag === 'te') relay.endpoints.push({ tag, attrs, endpoint: endpointFromBytes(content), binary: binaryValue(content) });
      else if (tag === 'participant') (relay.participants ||= []).push({ attrs });
    }
    return relay;
  }

  function classifyFlow(stage, node) {
    const tag = tagOf(node);
    const attrs = attrsOf(node);
    if (stage === 'encode' && attrs.to) return 'network_out';
    if (stage === 'decode' && attrs.from) return 'network_in';
    if (stage === 'decode' && tag !== 'call' && tag !== 'ack' && tag !== 'receipt') return 'stack_out';
    if (stage === 'encode') return 'stack_in';
    return 'internal';
  }

  function extractSignaling(stage, node, wireBytes = null) {
    const signals = [];
    const relays = [];
    walk(node, (candidate, depth) => {
      const signal = signalFromNode(candidate, depth);
      if (signal) signals.push(signal);
      if (tagOf(candidate) === 'relay') relays.push(relayFromNode(candidate));
    });
    if (signals.length === 0) return null;

    const rootAttrs = attrsOf(node);
    const callSignal = signals.find(signal => signal.attrs['call-id']) || signals[0];
    return {
      schema: 'wa-voip-diag/v2',
      source: 'wawap',
      event: 'signaling',
      stage,
      flow: classifyFlow(stage, node),
      direction: classifyFlow(stage, node) === 'network_out' ? 'out' :
        classifyFlow(stage, node) === 'network_in' ? 'in' : 'internal',
      stanzaTag: tagOf(node),
      stanzaId: rootAttrs.id || null,
      from: rootAttrs.from || null,
      to: rootAttrs.to || null,
      callId: callSignal.attrs['call-id'] || rootAttrs['call-id'] || null,
      callCreator: callSignal.attrs['call-creator'] || rootAttrs['call-creator'] || null,
      signals,
      relays,
      wireBytes: binaryValue(wireBytes),
      node: nodeToJSON(node),
      rawXml: serializeNode(node),
    };
  }

  root.WAVoipCaptureCore = Object.freeze({
    VIDEO_STATES, attrsOf, binaryValue, bytesOf, bytesToBase64, bytesToHex,
    childrenOf, classifyFlow, endpointFromBytes, extractSignaling, isBytes,
    nodeToJSON, serializeNode, tagOf,
  });
})(globalThis);
