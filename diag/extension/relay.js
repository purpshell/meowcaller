window.addEventListener("message", event => {
  if (event.source === window && event.data?.type === "MEOWCALLER_DIAGNOSTIC") {
    chrome.runtime.sendMessage(event.data.payload).catch(() => {});
  }
});

const script = document.createElement("script");
script.src = chrome.runtime.getURL("hooks.js");
script.async = false;
script.onload = () => script.remove();
(document.documentElement || document.head).appendChild(script);
