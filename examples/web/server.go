package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"

	"github.com/rs/zerolog"
	qrcode "github.com/skip2/go-qrcode"
)

// videoBridge is an ephemeral, localhost-only HTTP server that pipes a call's H.264 video
// to and from a browser tab via WebCodecs: the browser decodes (display on a canvas) and
// encodes (camera) the H.264; the CLI carries it over the WhatsApp relay through the
// meowcaller Call API. This is a demo/dev tool and lives in the example, not the library —
// the library exposes only Call.OnVideoFrame / Call.SendVideoFrame / Call.OnVideoState.
//
// The page is self-contained and uses WebCodecs directly, with no JS build step.
type videoBridge struct {
	ln  net.Listener
	srv *http.Server
	log zerolog.Logger

	mu          sync.Mutex
	subs        map[chan vbMsg]struct{}
	onFrame     func([]byte)
	onControl   func(vbControl) error
	orientation int
	qrPNG       []byte
	state       []byte
	closed      bool
}

// vbMsg is one SSE payload to the page: a video frame (event "") or an orientation update
// (event "orient").
type vbMsg struct {
	event string
	data  []byte
}

type vbControl struct {
	Action      string `json:"action"`
	Target      string `json:"target,omitempty"`
	Emoji       string `json:"emoji,omitempty"`
	Orientation int    `json:"orientation,omitempty"`
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
	mux.HandleFunc("/control", vb.handleControl)
	mux.HandleFunc("/qr.png", vb.handleQRCode)
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

func (vb *videoBridge) OnControl(fn func(vbControl) error) {
	vb.mu.Lock()
	vb.onControl = fn
	vb.mu.Unlock()
}

func (vb *videoBridge) PublishState(state any) {
	data, err := json.Marshal(state)
	if err == nil {
		vb.mu.Lock()
		vb.state = append(vb.state[:0], data...)
		vb.mu.Unlock()
		vb.broadcast(vbMsg{event: "state", data: data})
	}
}

func (vb *videoBridge) PublishEvent(event any) {
	data, err := json.Marshal(event)
	if err == nil {
		vb.broadcast(vbMsg{event: "state", data: data})
	}
}

func (vb *videoBridge) RequestKeyframe() {
	vb.broadcast(vbMsg{event: "keyframe", data: []byte("1")})
}

func (vb *videoBridge) SetQRCode(code string) error {
	png, err := qrcode.Encode(code, qrcode.Medium, 320)
	if err != nil {
		return err
	}
	vb.setQRCodePNG(png)
	return nil
}

func (vb *videoBridge) setQRCodePNG(png []byte) {
	vb.mu.Lock()
	vb.qrPNG = append(vb.qrPNG[:0], png...)
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
	state := append([]byte(nil), vb.state...)
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
	if len(state) > 0 {
		fmt.Fprintf(w, "event: state\ndata: %s\n\n", state)
	}
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

func (vb *videoBridge) handleControl(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var command vbControl
	decoder := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&command); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	valid := map[string]bool{
		"dial_audio": true, "dial_video": true, "answer": true, "reject": true,
		"start_video": true, "accept_video": true, "stop_video": true,
		"hangup": true, "orientation": true, "reaction": true,
	}
	if !valid[command.Action] {
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}
	vb.mu.Lock()
	fn := vb.onControl
	vb.mu.Unlock()
	if fn == nil {
		http.Error(w, "call controller unavailable", http.StatusServiceUnavailable)
		return
	}
	if err := fn(command); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (vb *videoBridge) handleQRCode(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	vb.mu.Lock()
	png := append([]byte(nil), vb.qrPNG...)
	vb.mu.Unlock()
	if len(png) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(png)
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
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>meowcaller call console</title>
<style>
:root{color-scheme:dark;--bg:#111315;--panel:#1b1f22;--line:#353b40;--text:#f1f3f4;--muted:#a7afb5;--green:#39b87f;--red:#e25d5d;--amber:#d5a542}
*{box-sizing:border-box}body{margin:0;background:var(--bg);color:var(--text);font:14px system-ui,sans-serif;letter-spacing:0}
header{height:56px;padding:0 20px;border-bottom:1px solid var(--line);display:flex;align-items:center;gap:16px}h1{font-size:16px;margin:0}.state{color:var(--muted);margin-left:auto}
main{max-width:1280px;margin:auto;padding:16px}.toolbar{display:grid;grid-template-columns:minmax(180px,1fr) repeat(2,auto);gap:8px;margin-bottom:10px}
input,button{height:38px;border:1px solid var(--line);border-radius:6px;background:#202529;color:var(--text);font:inherit;letter-spacing:0}
input{padding:0 11px;min-width:0}button{padding:0 13px;cursor:pointer;white-space:nowrap}button:hover{border-color:#68727a}button.primary{background:#176846;border-color:#25865e}button.danger{background:#7b2929;border-color:#a83b3b}
.actions{display:flex;gap:8px;overflow-x:auto;padding-bottom:10px}.reaction-picker{display:flex;gap:6px;padding-bottom:12px}.reaction-picker button{width:38px;padding:0;font-size:20px}.media{display:grid;grid-template-columns:1fr 1fr;gap:12px;border-top:1px solid var(--line);padding-top:14px}
.pane{min-width:0}.pane-head{height:42px;color:var(--muted);font-size:12px;text-transform:uppercase;display:flex;align-items:center;justify-content:space-between}.pane-head button{height:32px}.remote-wrap,.local-wrap{position:relative;display:grid;place-items:center;width:100%;aspect-ratio:4/3;overflow:hidden;background:#050607;border:1px solid var(--line);border-radius:6px}.reactions{position:absolute;inset:0;overflow:hidden;pointer-events:none;display:flex;align-items:center;justify-content:center}.reaction{position:absolute;font-size:64px;animation:reaction-rise 1.8s ease-out forwards}@keyframes reaction-rise{0%{opacity:0;transform:translateY(24px) scale(.7)}20%{opacity:1;transform:translateY(0) scale(1)}75%{opacity:1}100%{opacity:0;transform:translateY(-80px) scale(1.15)}}
canvas,video{display:block;width:auto;height:auto;max-width:100%;max-height:100%;object-fit:contain;background:#050607}
#log{height:150px;overflow:auto;margin-top:12px;padding:10px;border:1px solid var(--line);background:#0b0d0e;color:#b8d8c8;font:12px ui-monospace,monospace;white-space:pre-wrap}
.pairing{display:flex;align-items:center;gap:18px;padding:12px 0 16px;border-bottom:1px solid var(--line);margin-bottom:14px}.pairing[hidden]{display:none}.pairing img{width:180px;height:180px;background:#fff;border-radius:6px}.pairing strong{display:block;margin-bottom:5px}.pairing span{color:var(--muted)}
@media(max-width:760px){header{padding:0 12px}.toolbar{grid-template-columns:1fr 1fr}.toolbar input{grid-column:1/-1}.media{grid-template-columns:1fr}.actions{flex-wrap:wrap}.actions button,.toolbar button{flex:1 0 auto}}
</style></head><body>
<header><h1>meowcaller call console</h1><div id="state" class="state">idle</div></header>
<main>
  <section id="pairing" class="pairing" hidden><img id="qr" alt="WhatsApp linked-device QR"><div><strong>Link WhatsApp</strong><span>WhatsApp > Linked devices > Link a device</span></div></section>
  <div class="toolbar"><input id="target" inputmode="tel" placeholder="WhatsApp number or LID"><button id="dialAudio">Dial audio</button><button id="dialVideo" class="primary">Dial video</button></div>
  <div class="actions"><button id="answer">Answer</button><button id="reject">Reject</button><button id="startVideo">Upgrade to video</button><button id="acceptVideo" hidden>Accept video</button><button id="stopVideo">Stop video</button><button id="hangup" class="danger">Hang up</button></div>
  <div class="reaction-picker"><button data-reaction="👍" aria-label="Thumbs up">👍</button><button data-reaction="❤️" aria-label="Heart">❤️</button><button data-reaction="😂" aria-label="Laugh">😂</button><button data-reaction="😮" aria-label="Surprised">😮</button><button data-reaction="😢" aria-label="Sad">😢</button><button data-reaction="🙏" aria-label="Thanks">🙏</button><button data-reaction="" aria-label="Remove reaction">×</button></div>
  <div class="media">
    <section class="pane"><div class="pane-head"><span>WhatsApp peer</span><span id="remoteMeta">waiting</span></div><div class="remote-wrap"><canvas id="remote" width="640" height="480"></canvas><div id="reactions" class="reactions" aria-live="polite"></div></div></section>
    <section class="pane"><div class="pane-head"><span>Local camera</span><button id="cam">Start camera</button></div><div class="local-wrap"><video id="local" autoplay muted playsinline></video></div></section>
  </div>
  <div id="log"></div>
</main>
<script>
const $=id=>document.getElementById(id), log=(...a)=>{$('log').textContent+=a.join(' ')+'\n';$('log').scrollTop=$('log').scrollHeight};
const control=async(action,extra={})=>{const r=await fetch('/control',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({action,...extra})});if(!r.ok)throw Error((await r.text()).trim())};
const invoke=(action,extra)=>control(action,extra).catch(e=>log(action,e.message));
$('dialAudio').onclick=()=>invoke('dial_audio',{target:$('target').value.trim()});$('dialVideo').onclick=()=>invoke('dial_video',{target:$('target').value.trim()});
$('answer').onclick=()=>invoke('answer');$('reject').onclick=()=>invoke('reject');$('startVideo').onclick=()=>invoke('start_video');$('acceptVideo').onclick=()=>invoke('accept_video');$('stopVideo').onclick=()=>invoke('stop_video');$('hangup').onclick=()=>invoke('hangup');
document.querySelectorAll('[data-reaction]').forEach(b=>b.onclick=()=>invoke('reaction',{emoji:b.dataset.reaction}));
if(!('VideoDecoder'in window))log('WebCodecs unavailable in this browser');
const remote=$('remote'),paint=remote.getContext('2d'),es=new EventSource('/in');let decoder=null,decodeStarted=false,forceKeyframe=true,remoteVideoActive=true,remoteOrientation=0;
function keyNAL(d){for(let i=0;i+4<d.length;i++){let p=-1;if(d[i]===0&&d[i+1]===0&&d[i+2]===1)p=i+3;else if(d[i]===0&&d[i+1]===0&&d[i+2]===0&&d[i+3]===1)p=i+4;if(p>=0){const t=d[p]&31;if(t===5||t===7)return true}}return false}
function setRemoteVideoActive(active){remoteVideoActive=active;if(active){$('remoteMeta').textContent='waiting'}else{paint.clearRect(0,0,remote.width,remote.height);$('remoteMeta').textContent='off'}}
function drawRemoteFrame(f){const w=f.displayWidth,h=f.displayHeight,q=((remoteOrientation%4)+4)%4,portrait=q%2===1;remote.width=portrait?h:w;remote.height=portrait?w:h;paint.save();if(q===1){paint.translate(remote.width,0);paint.rotate(Math.PI/2)}else if(q===2){paint.translate(remote.width,remote.height);paint.rotate(Math.PI)}else if(q===3){paint.translate(0,remote.height);paint.rotate(-Math.PI/2)}paint.drawImage(f,0,0,w,h);paint.restore();$('remoteMeta').textContent=remote.width+'x'+remote.height}
function getDecoder(){if(decoder&&decoder.state!=='closed')return decoder;decoder=new VideoDecoder({output:f=>{if(remoteVideoActive)drawRemoteFrame(f);f.close()},error:e=>log('decoder',e.message)});decoder.configure({codec:'avc1.42E01F',optimizeForLatency:true});return decoder}
function showReaction(emoji){if(!emoji)return;const el=document.createElement('span');el.className='reaction';el.textContent=emoji;$('reactions').appendChild(el);setTimeout(()=>el.remove(),1800)}
es.onmessage=e=>{const au=Uint8Array.from(atob(e.data),c=>c.charCodeAt(0)),key=keyNAL(au);if(!decodeStarted&&!key)return;decodeStarted=true;try{getDecoder().decode(new EncodedVideoChunk({type:key?'key':'delta',timestamp:performance.now()*1000,data:au}))}catch(err){log('decode',err.message);decodeStarted=false}};
es.addEventListener('orient',e=>{remoteOrientation=+e.data||0});es.addEventListener('keyframe',()=>{forceKeyframe=true;log('peer requested keyframe')});
es.addEventListener('state',e=>{const s=JSON.parse(e.data);if(s.event!=='reaction')$('state').textContent=s.event+(s.peer?' / '+s.peer:'');if(s.event==='pairing'){$('pairing').hidden=false;$('qr').src='/qr.png?t='+Date.now()}else if(s.event==='idle')$('pairing').hidden=true;if(s.event==='video_state'){if(s.video_state===0||s.video_state===6)setRemoteVideoActive(false);else if(s.video_state===1)setRemoteVideoActive(true);$('acceptVideo').hidden=!(s.video_state===3||s.video_state===11)}else if(s.event==='reaction'&&!s.removed)showReaction(s.emoji);log(new Date().toLocaleTimeString(),JSON.stringify(s))});es.onerror=()=>log('event stream disconnected');
let stream=null,encoder=null,reader=null,upload=Promise.resolve();
async function stopCamera(){if(reader)await reader.cancel().catch(()=>{});if(encoder&&encoder.state!=='closed')encoder.close();if(stream)stream.getTracks().forEach(t=>t.stop());stream=encoder=reader=null;$('local').srcObject=null;$('cam').textContent='Start camera'}
$('cam').onclick=async()=>{if(stream){await stopCamera();return}try{stream=await navigator.mediaDevices.getUserMedia({video:{frameRate:{ideal:15,max:15}}});$('local').srcObject=stream;$('cam').textContent='Stop camera';const track=stream.getVideoTracks()[0];encoder=new VideoEncoder({output:chunk=>{const b=new Uint8Array(chunk.byteLength);chunk.copyTo(b);upload=upload.then(()=>fetch('/out',{method:'POST',body:b})).then(r=>{if(!r.ok)throw Error('video upload '+r.status)}).catch(e=>log(e.message))},error:e=>log('encoder',e.message)});reader=new MediaStreamTrackProcessor({track}).readable.getReader();let n=0,encodedWidth=0,encodedHeight=0;for(;;){const{value:f,done}=await reader.read();if(done)break;if(f.displayWidth!==encodedWidth||f.displayHeight!==encodedHeight){encodedWidth=f.displayWidth;encodedHeight=f.displayHeight;encoder.configure({codec:'avc1.42E01F',avc:{format:'annexb'},width:encodedWidth,height:encodedHeight,framerate:15,bitrate:Math.max(250000,Math.min(2000000,encodedWidth*encodedHeight*2)),latencyMode:'realtime'});forceKeyframe=true}if(encoder.encodeQueueSize<2){const key=forceKeyframe||n%15===0;forceKeyframe=false;encoder.encode(f,{keyFrame:key});n++}f.close()}}catch(e){log('camera',e.message);await stopCamera()}};
</script></body></html>`
