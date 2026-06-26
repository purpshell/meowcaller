package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"

	"github.com/rs/zerolog"
)

// videoBridge is an ephemeral, localhost-only HTTP server that pipes a call's H.264 video
// to and from a browser tab via WebCodecs: the browser decodes (display on a canvas) and
// encodes (camera) the H.264; the CLI carries it over the WhatsApp relay through the
// meowcaller Call API. This is a demo/dev tool and lives in the example, not the library —
// the library exposes only Call.OnVideoFrame / Call.SendVideoFrame / Call.OnVideoState.
//
// Mirrors WaCalls's React + WebCodecs client (see README Credits), collapsed into one
// self-contained Go file with no JS build step.
type videoBridge struct {
	ln  net.Listener
	srv *http.Server
	log zerolog.Logger

	mu          sync.Mutex
	subs        map[chan vbMsg]struct{}
	onFrame     func([]byte)
	orientation int
	closed      bool
}

// vbMsg is one SSE payload to the page: a video frame (event "") or an orientation update
// (event "orient").
type vbMsg struct {
	event string
	data  []byte
}

// newVideoBridge starts a bridge on a free 127.0.0.1 port.
func newVideoBridge(log zerolog.Logger) (*videoBridge, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("video bridge listen: %w", err)
	}
	vb := &videoBridge{ln: ln, log: log, subs: make(map[chan vbMsg]struct{})}
	mux := http.NewServeMux()
	mux.HandleFunc("/", vb.handleIndex)
	mux.HandleFunc("/in", vb.handleIn)
	mux.HandleFunc("/out", vb.handleOut)
	vb.srv = &http.Server{Handler: mux}
	go func() {
		if err := vb.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			vb.log.Debug().Err(err).Msg("video bridge stopped")
		}
	}()
	return vb, nil
}

// URL is the address to open in a browser.
func (vb *videoBridge) URL() string { return "http://" + vb.ln.Addr().String() }

func (vb *videoBridge) broadcast(m vbMsg) {
	vb.mu.Lock()
	defer vb.mu.Unlock()
	for ch := range vb.subs {
		select {
		case ch <- m:
		default: // page can't keep up — drop (recovers on the next keyframe)
		}
	}
}

// WriteFrame pushes one received Annex-B H.264 access unit to every connected page.
func (vb *videoBridge) WriteFrame(annexB []byte) {
	if len(annexB) == 0 {
		return
	}
	f := make([]byte, len(annexB))
	copy(f, annexB)
	vb.broadcast(vbMsg{data: f})
}

// SetOrientation pushes the peer's video device orientation (0..3) so the page can rotate
// the canvas to display upright.
func (vb *videoBridge) SetOrientation(orientation int) {
	vb.mu.Lock()
	if orientation == vb.orientation {
		vb.mu.Unlock()
		return
	}
	vb.orientation = orientation
	vb.mu.Unlock()
	vb.broadcast(vbMsg{event: "orient", data: []byte(strconv.Itoa(orientation))})
}

// OnFrame registers a callback fired per Annex-B access unit the page captures.
func (vb *videoBridge) OnFrame(fn func([]byte)) {
	vb.mu.Lock()
	vb.onFrame = fn
	vb.mu.Unlock()
}

// Close stops the server and releases page subscriptions.
func (vb *videoBridge) Close() error {
	vb.mu.Lock()
	if vb.closed {
		vb.mu.Unlock()
		return nil
	}
	vb.closed = true
	for ch := range vb.subs {
		close(ch)
		delete(vb.subs, ch)
	}
	vb.mu.Unlock()
	return vb.srv.Close()
}

func (vb *videoBridge) handleIn(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flush", http.StatusInternalServerError)
		return
	}
	ch := make(chan vbMsg, 16)
	vb.mu.Lock()
	if vb.closed {
		vb.mu.Unlock()
		return
	}
	vb.subs[ch] = struct{}{}
	orient := vb.orientation
	vb.mu.Unlock()
	defer func() {
		vb.mu.Lock()
		if _, ok := vb.subs[ch]; ok {
			delete(vb.subs, ch)
			close(ch)
		}
		vb.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	fmt.Fprintf(w, "event: orient\ndata: %d\n\n", orient) // send current orientation up front
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case m, ok := <-ch:
			if !ok {
				return
			}
			if m.event != "" {
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", m.event, m.data)
			} else {
				fmt.Fprintf(w, "data: %s\n\n", base64.StdEncoding.EncodeToString(m.data))
			}
			flusher.Flush()
		}
	}
}

func (vb *videoBridge) handleOut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, "read", http.StatusBadRequest)
		return
	}
	vb.mu.Lock()
	fn := vb.onFrame
	vb.mu.Unlock()
	if fn != nil && len(body) > 0 {
		fn(body)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (vb *videoBridge) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	io.WriteString(w, videoBridgePage)
}

const videoBridgePage = `<!doctype html>
<html><head><meta charset="utf-8"><title>meowcaller video</title>
<style>body{font-family:sans-serif;background:#111;color:#eee;margin:0;padding:1rem}
.wrap{display:flex;gap:1rem;flex-wrap:wrap}.box{position:relative}
canvas,video{background:#000;border-radius:8px;width:480px}#remote{transition:transform .2s}
button{padding:.5rem 1rem;margin-top:.5rem}#log{font:12px monospace;color:#9a9;white-space:pre-wrap}</style></head>
<body>
<h3>meowcaller video bridge</h3>
<div class="wrap">
  <div class="box"><div>peer</div><canvas id="remote" width="480" height="360"></canvas></div>
  <div class="box"><div>you</div><video id="local" autoplay muted playsinline></video></div>
</div>
<button id="cam">Start camera (send video)</button>
<div id="log"></div>
<script>
const log = (...a) => { document.getElementById('log').textContent += a.join(' ') + '\n'; };
if (!('VideoDecoder' in window)) log('WebCodecs not supported — use Chrome/Edge over http://127.0.0.1');

// ---- orientation: rotate the peer canvas to display upright ----
const remote = document.getElementById('remote');
const es = new EventSource('/in');
es.addEventListener('orient', e => { remote.style.transform = 'rotate(' + ((+e.data) * 90) + 'deg)'; });

// ---- inbound: peer H.264 (Annex-B) -> WebCodecs decode -> canvas ----
const ctx = remote.getContext('2d');
let decoder = null, started = false;
function hasKeyNAL(d){
  for (let i=0;i+4<d.length;i++){
    if (d[i]===0&&d[i+1]===0&&d[i+2]===1){ const t=d[i+3]&0x1f; if(t===5||t===7) return true; }
    else if (d[i]===0&&d[i+1]===0&&d[i+2]===0&&d[i+3]===1){ const t=d[i+4]&0x1f; if(t===5||t===7) return true; }
  }
  return false;
}
function ensureDecoder(){
  if (decoder && decoder.state!=='closed') return decoder;
  decoder = new VideoDecoder({
    output: f => { remote.width=f.displayWidth; remote.height=f.displayHeight; ctx.drawImage(f,0,0); f.close(); },
    error: e => log('decoder', e.message),
  });
  decoder.configure({ codec:'avc1.42E01F', optimizeForLatency:true });
  return decoder;
}
es.onmessage = ev => {
  const au = Uint8Array.from(atob(ev.data), c => c.charCodeAt(0));
  const key = hasKeyNAL(au);
  if (!started && !key) return;
  started = true;
  try { ensureDecoder().decode(new EncodedVideoChunk({ type: key?'key':'delta', timestamp: performance.now()*1000, data: au })); }
  catch(e){ log('decode', e.message); started=false; }
};
es.onerror = () => log('sse disconnected');

// ---- outbound: camera -> WebCodecs encode (Annex-B) -> POST /out ----
document.getElementById('cam').onclick = async () => {
  try {
    const stream = await navigator.mediaDevices.getUserMedia({ video:{width:640,height:480,frameRate:15} });
    document.getElementById('local').srcObject = stream;
    const track = stream.getVideoTracks()[0];
    const enc = new VideoEncoder({
      output: chunk => { const b=new Uint8Array(chunk.byteLength); chunk.copyTo(b); fetch('/out',{method:'POST',body:b}); },
      error: e => log('encoder', e.message),
    });
    enc.configure({ codec:'avc1.42E01F', avc:{format:'annexb'}, width:640, height:480, framerate:15, bitrate:500000, latencyMode:'realtime' });
    const reader = new MediaStreamTrackProcessor({track}).readable.getReader();
    let n=0;
    for(;;){ const {value:frame,done}=await reader.read(); if(done)break; enc.encode(frame,{keyFrame:(n++%30===0)}); frame.close(); }
  } catch(e){ log('camera', e.message); }
};
</script></body></html>
`
