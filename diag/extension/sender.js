const endpoint = "http://127.0.0.1:3219/events";
let queue = [];
let timer;

chrome.runtime.onMessage.addListener(event => {
  queue.push(event);
  clearTimeout(timer);
  timer = setTimeout(flush, 100);
});

async function flush() {
  const batch = queue;
  queue = [];
  if (!batch.length) return;
  try {
    const response = await fetch(endpoint, {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(batch),
    });
    if (!response.ok) throw new Error(response.status);
  } catch {
    queue.unshift(...batch);
    timer = setTimeout(flush, 1000);
  }
}
