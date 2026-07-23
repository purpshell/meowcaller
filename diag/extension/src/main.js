const injectLoggers = () => {
    window.AuthStore = {};
    window.waLogs = [];

    function isByteBuffer(value) {
      return value instanceof ArrayBuffer ||
        (ArrayBuffer.isView(value) && !(value instanceof DataView));
    }

    function byteView(value) {
      if (value instanceof ArrayBuffer) return new Uint8Array(value);
      return new Uint8Array(value.buffer, value.byteOffset, value.byteLength);
    }

    function bytesToHex(bytes) {
      return Array.from(bytes, b => b.toString(16).padStart(2, "0")).join("");
    }

    function escapeXmlText(value) {
      return String(value).replace(/[&<>]/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;" }[c]));
    }

    function escapeXmlAttr(value) {
      return String(value).replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&apos;" }[c]));
    }

    function rawXmlValue(value, depth = 0, seen = new WeakSet()) {
      const indent = "\t".repeat(depth);
      if (value == null) return "";
      if (isByteBuffer(value)) return indent + bytesToHex(byteView(value));
      if (typeof value === "string" || typeof value === "number" || typeof value === "boolean" || typeof value === "bigint") {
        return indent + escapeXmlText(value);
      }
      if (Array.isArray(value)) {
        return value.map(item => rawXmlValue(item, depth, seen)).filter(Boolean).join("\n");
      }
      if (typeof value === "object") return rawStanzaXml(value, depth, seen);
      return "";
    }

    function rawStanzaXml(node, depth = 0, seen = new WeakSet()) {
      const indent = "\t".repeat(depth);
      if (!node || typeof node !== "object") return rawXmlValue(node, depth, seen);
      if (seen.has(node)) return indent + "[Circular]";
      seen.add(node);

      const tag = node.tag || "node";
      const attrs = node.attrs && typeof node.attrs === "object"
        ? Object.entries(node.attrs)
            .filter(([, value]) => value !== undefined && value !== null)
            .map(([key, value]) => ` ${key}="${escapeXmlAttr(value)}"`)
            .join("")
        : "";
      const content = node.content;
      const hasContent = Array.isArray(content) ? content.length > 0 : content !== undefined && content !== null && content !== "";
      if (!hasContent) {
        seen.delete(node);
        return `${indent}<${tag}${attrs} />`;
      }

      const body = rawXmlValue(content, depth + 1, seen);
      seen.delete(node);
      return `${indent}<${tag}${attrs}>\n${body}\n${indent}</${tag}>`;
    }

    (function setupBridge() {
      if (!Array.isArray(window.waLogs)) window.waLogs = [];
      const origPush = window.waLogs.push.bind(window.waLogs);

      function emit(log) {

        origPush(log);
        try {
          window.postMessage({ type: 'WA_LOGGER_LOG', payload: log }, window.location.origin);
        } catch {}
      }

      window.waLogs.push = function (...items) {
        items.forEach(emit);
        return window.waLogs.length;
      };

      window.addEventListener('message', (e) => {
        if (e.source !== window) return;
        if (e.data?.type === 'WA_LOGGER_DUMP') {
          try {
            window.postMessage(
              { type: 'WA_LOGGER_DUMP_RESULT', payload: window.waLogs },
              window.location.origin
            );
          } catch {}
        }
      });

      // Optional: expose an emitter to bypass push if needed:
      window.__waEmit = (row) => {
        emit(row);
      };
    })();

    window.injectToFunction = (target, callback) => {
        const module = window.require(target.module);
        const originalFunction = module[target.function];
        const modifiedFunction = (...args) => callback(originalFunction, ...args);
        module[target.function] = modifiedFunction;
    };

    require("WAWap").enableXMLFormat()

    window.injectToFunction(
        { module: 'WAWap', function: 'encodeStanza' },
        (func, ...args) => {
            const [outgoing] = args;
            let stack = ""
          try {
            try {
              throw new Error("outgoing")
            } catch (err) {
              stack = window.WAAnnotateStack.annotate(err.stack)
            }
            if (outgoing?.tag === "iq" && outgoing.attrs?.["xmlns"] === "w:stats") {
              window.waLogs.push([Date.now(), "wam_out", window.WAMRunner.processPayload(outgoing.content?.[0]?.content)])
            }
            window.waLogs.push([Date.now(), "out", outgoing?.toString?.(), stack]);
            window.waLogs.push([Date.now(), "raw_out", rawStanzaXml(outgoing), stack]);
          } catch {}
          return func(...args);
        }
    );

    window.injectToFunction(
        { module: 'WAWebClientPayload', function: 'getClientPayloadForRegistration' },
      async (func, ...args) => {
        const result = await func(...args)
        const decoded = require("decodeProtobuf").decodeProtobuf(require("WAWebProtobufsWa6.pb").ClientPayloadSpec, result)
        window.waLogs.push([Date.now(), "out_reg", JSON.stringify({decoded, props: require("decodeProtobuf").decodeProtobuf(require("WAWebProtobufsCompanionReg.pb").DevicePropsSpec, decoded.devicePairingData.deviceProps)}, null, 2)])
        console.log([Date.now(), "out_reg", JSON.stringify({decoded, props: require("decodeProtobuf").decodeProtobuf(require("WAWebProtobufsCompanionReg.pb").DevicePropsSpec, decoded.devicePairingData.deviceProps)}, null, 2)])
        return result
      }
    )

    window.injectToFunction(
      {
      module: 'decodeProtobuf', function: 'decodeProtobuf'
    }, (func, ...args) => {
        const decoded = func(...args)
        window.waLogs.push([Date.now(), "in_proto", decoded.toString()])
      console.log("dec proto", decoded)
      return decoded
    })


    window.injectToFunction(
      {
      module: 'encodeProtobuf', function: 'encodeProtobuf'
    }, (func, ...args) => {
        console.log("enc proto", ...args)
        window.waLogs.push([Date.now(), "out_proto", ...args])
      return  func(...args)
    })

    window.injectToFunction(
        { module: 'WAWap', function: 'decodeStanza' },
        async (func, ...args) => {
            const decoded = await func(...args);

          try {
            window.waLogs.push([Date.now(), "in", decoded?.toString?.()]);
            window.waLogs.push([Date.now(), "raw_in", rawStanzaXml(decoded)]);
          } catch {}
            return decoded;
        }
    );

    // if (!window.loggerSet) {
    //     const _logger = window.require("WAWebLoggerImpl").Logger
    //     const _origLogImpl = _logger.logImpl.bind(_logger)
    //     const logLevel = {
    //         0: "debug",
    //         1: "info",
    //         2: "log",
    //         3: "warn",
    //         4: "error",
    //         5: "error",
    //     }


    //     _logger.logImpl = (level, message, a, b, area) => {
    //         const logArgs = []
    //         if (area) logArgs.push(`[${area}]`)
    //         logArgs.push(message)
    //         if (a || b) logArgs.push([a, b])
    //         window.waLogs.push([Date.now(), "log_" + logLevel[level], logArgs.join(" ")]);

    //         _origLogImpl(level, message, a, b, area)
    //     }
    //     window.loggerSet = true
    // }

  window.injectToFunction(
    { module: 'WAWebSendMsgCommonApi', function: 'encodeAndPad' },
    (orig, ...args) => {
      const result = orig(...args)
      window.waLogs.push([Date.now(), "message_out", ...args]);
      return result
    });

    window.injectToFunction(
      { module: 'WAWebWamCodegenUtils', function: 'defineEvents' },
      (orig, ...args) => {
        const ctor = orig(...args);               // this is the constructor you saw as ƒ b(b)
        if (typeof ctor !== 'function') return ctor; // safety

        // Wrap the constructor so 'new' still works and we can log the instance
        const wrapped = new Proxy(ctor, {
          construct(target, a, newTarget) {
            const inst = Reflect.construct(target, a, newTarget);
              if (!!inst) {
                const name = inst && (inst.$className || target.name || 'WamEvent');
                let stack = ""
              try {
                throw new Error("wam write")
              } catch (err) {
                stack = window.WAAnnotateStack.annotate(err.stack)
              }
                const data = inst.all && typeof inst.all === "object" ? { ...inst.all } : inst.all
                window.waLogs.push([Date.now(), "wam_event", JSON.stringify({ name, id: inst.id, data, commitTime: inst.commitTime, eventTime: inst.eventTime }, undefined, 2), stack])

              }

            return inst;
          },
        });

        return wrapped; // IMPORTANT: return a constructable value
      }
    );

    window.AuthStore.AppState = window.require('WAWebSocketModel').Socket;

    (function setupExperimentDecode(){
      function hexToArrayBuffer(hexString) {
        hexString = String(hexString).replace(/[\s]/g, '');
        const buffer = new ArrayBuffer(hexString.length / 2);
        const view = new Uint8Array(buffer);
        for (let i=0;i<hexString.length;i+=2) view[i/2] = parseInt(hexString.substr(i,2),16);
        return buffer;
      }
      function cleanObject(obj) {
        for (const key in obj) {
          if (!Object.prototype.hasOwnProperty.call(obj,key)) continue;
          if (key === '$$unknownFieldCount' && obj[key] === 0) delete obj[key];
          else if (typeof obj[key] === 'object' && obj[key] !== null) cleanObject(obj[key]);
        }
      }

      window.addEventListener('message', (e) => {
        if (e.source !== window) return;
        if (e.data?.type === 'WA_EXPERIMENT_DECODE') {
          try {
            const hex = e.data.payload;
            const ab = hexToArrayBuffer(hex);
            const dec = window.require("decodeProtobuf").decodeProtobuf(
              window.require("WAWebProtobufsE2E.pb").MessageSpec, ab
            );
            cleanObject(dec);
            window.postMessage({ type:'EXPERIMENT_DECODE_RESULT', ok:true, result: dec }, window.location.origin);
          } catch (err) {
            window.postMessage({ type:'EXPERIMENT_DECODE_RESULT', ok:false, error: String(err) }, window.location.origin);
          }
        }
      });

      // // Relay back to extension via content script
      // window.addEventListener('message', (e) => {
      //   if (e.source !== window) return;
      //   if (e.data?.type === 'EXPERIMENT_DECODE_RESULT') {
      //     try { chrome?.runtime?.sendMessage?.(e.data); } catch {}
      //   }
      // });
    })();
}


const loadTest = () => window.Debug?.VERSION != undefined && parseInt(window.Debug.VERSION.split(".")?.[1]) >= 3000;

function waitTillPass(selector) {
    return new Promise(r => setTimeout(() => {
        if (selector()) {
            r();
        } else {
            waitTillPass(selector).then(r);
        }
    }, 200));
};


const Cache = new Map(); // url -> { text, lineOffsets:number[], modules:{pos:number,name:string}[] }

  // Try to ensure we only touch URLs that are already loaded by the page
  // (This avoids any cache-miss delays.)
  const loadedURLs = (function collectLoadedURLs() {
    const set = new Set();
    // Any <script src=...> on the page:
    document.querySelectorAll('script[src]').forEach(s => set.add(s.src));
    // ResourceTiming entries:
    if (performance && performance.getEntriesByType) {
      for (const e of performance.getEntriesByType('resource')) {
        if (e.initiatorType === 'script' || /\/rsrc\.php\//.test(e.name)) set.add(e.name);
      }
    }
    return set;
  })();

  function parseStack(stackText) {
    const lines = String(stackText).split('\n');
    return lines.map(raw => {
      // at Func (https://.../file.js:LINE:COL)
      let m = raw.match(/\s*at\s+([^(\n]+?)\s+\((https?:\/\/[^\s)]+):(\d+):(\d+)\)/);
      if (m) return { raw, func: m[1].trim(), url: m[2], line: +m[3], col: +m[4] };
      // at https://.../file.js:LINE:COL
      m = raw.match(/\s*at\s+(https?:\/\/[^\s)]+):(\d+):(\d+)/);
      if (m) return { raw, func: '(anonymous)', url: m[1], line: +m[2], col: +m[3] };
      return { raw, func: null, url: null, line: null, col: null };
    });
  }

  function fetchTextSync(url) {
    try {
      if (!loadedURLs.has(url)) return null; // do not touch network for unknown URLs
      const xhr = new XMLHttpRequest();
      xhr.open('GET', url, false); // sync
      // Let the browser serve from memory/disk cache; no-cache headers not set.
      xhr.send(null);
      if (xhr.status >= 200 && xhr.status < 300) return xhr.responseText;
    } catch (e) {}
    return null;
  }

  function prepareFileSync(url) {
    if (Cache.has(url)) return Cache.get(url);
    const text = fetchTextSync(url);
    if (text == null) { Cache.set(url, null); return null; }

    // Precompute line start offsets
    const lineOffsets = [0];
    for (let i = 0; i < text.length; ) {
      const nl = text.indexOf('\n', i);
      if (nl === -1) break;
      lineOffsets.push(nl + 1);
      i = nl + 1;
    }

    // Collect module declarations for BOTH forms
    const modules = [];

    // Form A: __d("Name", [...]
    {
      const reA = /__d\(\s*["']([^"']+)["']\s*,/g;
      let m;
      while ((m = reA.exec(text))) modules.push({ pos: m.index, name: m[1] });
    }

    // Form B: __d(function ... ,"Name"
    {
      const reB = /__d\(\s*function[\s\S]*?,\s*["']([^"']+)["']/g;
      let m;
      while ((m = reB.exec(text))) modules.push({ pos: m.index, name: m[1] });
    }

    // Sort & dedupe by position
    modules.sort((a, b) => a.pos - b.pos);
    const deduped = [];
    for (let i = 0; i < modules.length; i++) {
      if (i && modules[i].pos === modules[i - 1].pos) continue;
      deduped.push(modules[i]);
    }

    const rec = { text, lineOffsets, modules: deduped };
    Cache.set(url, rec);
    return rec;
  }

  function toOffset(fileRec, line, col) {
    const start = fileRec.lineOffsets[Math.max(0, line - 1)] ?? 0;
    return start + Math.max(0, (col || 1) - 1);
  }

  function nearestModule(fileRec, offset) {
    const arr = fileRec.modules;
    if (!arr || !arr.length) return null;
    // binary search for last module with pos <= offset
    let lo = 0, hi = arr.length - 1, ans = -1;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      if (arr[mid].pos <= offset) { ans = mid; lo = mid + 1; }
      else { hi = mid - 1; }
    }
    return ans >= 0 ? arr[ans].name : null;
  }

  function annotate(stackText) {
    const frames = parseStack(stackText);
    const urls = Array.from(new Set(frames.filter(f => f.url).map(f => f.url)));

    // Build caches for known loaded script URLs only (avoids any potential network miss)
    for (const url of urls) prepareFileSync(url);

    const annotated = frames.map(f => {
      const trace = f.raw.slice(7);
      if (!f.url || !f.line) return `    at unknown -> ${trace}`;

      const rec = Cache.get(f.url);
      if (!rec) return `    at unknown -> ${trace}`;

      const off = toOffset(rec, f.line, f.col);
      const mod = nearestModule(rec, off) ?? 'unknown';
      return `    at ${mod} -> ${trace}`;
    });

    return annotated.slice(3).join('\n');
}

// Expose globally
window.WAAnnotateStack = { annotate, _Cache: Cache };

//main
(async () => {
  await waitTillPass(loadTest);

  window.injectWAM();
  injectLoggers();
})();
