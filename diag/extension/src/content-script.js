const port = chrome.runtime.connect({ name: 'walog-content' });

function onWindowMessage(event) {
  if (event.source !== window) return;
  const { type, payload } = event.data || {};
  if (type !== 'WA_VOIP_CAPTURE_EVENT' && type !== 'WA_LOGGER_LOG') return;
  try { port.postMessage({ type, payload }); } catch {}
}
window.addEventListener('message', onWindowMessage);

(() => {
  const scripts = [
    ['src/wam.js', 'whatsapp-wam-data'],
    ['src/voip-capture-core.js', 'whatsapp-voip-capture-core'],
    ['src/voip-browser-hooks.js', 'whatsapp-voip-browser-hooks'],
    ['src/main.js', 'whatsapp-web-logger'],
    ['src/voip-capture.js', 'whatsapp-voip-capture'],
  ];
  const parent = document.documentElement || document.head;
  for (const [path, id] of scripts) {
    const script = document.createElement('script');
    script.src = chrome.runtime.getURL(path);
    script.id = id;
    script.async = false;
    parent.appendChild(script);
  }
})();

window.addEventListener('pagehide', () => {
  try { window.removeEventListener('message', onWindowMessage); } catch {}
});
