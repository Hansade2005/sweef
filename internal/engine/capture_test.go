package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
	"github.com/sweefbrowser/sweef/internal/capture/synthetic"
)

// testBroker is a scriptable MediaBroker.
type testBroker struct {
	mu      sync.Mutex
	perms   map[string]string
	decide  func(MediaRequest) MediaDecision
	reqs    []MediaRequest
	changed []MediaActivity
	sources []DisplaySource
	opened  []string
}

func (b *testBroker) MediaPermission(_ *Tab, _ string, name string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s := b.perms[name]; s != "" {
		return s
	}
	return "prompt"
}

func (b *testBroker) RequestMedia(ctx context.Context, r MediaRequest) MediaDecision {
	b.mu.Lock()
	b.reqs = append(b.reqs, r)
	f := b.decide
	b.mu.Unlock()
	if f == nil {
		<-ctx.Done()
		return MediaDecision{}
	}
	return f(r)
}

func (b *testBroker) DisplaySources(*Tab) []DisplaySource { return b.sources }

func (b *testBroker) OpenDisplay(_ *Tab, id string) (capture.VideoSource, DisplayInfo, error) {
	b.mu.Lock()
	b.opened = append(b.opened, id)
	b.mu.Unlock()
	cam := synthetic.NewCamera(capture.DeviceInfo{ID: id, Kind: capture.Screen}, 1280, 720, 30)
	return cam, DisplayInfo{Label: "Tab: Test page", Surface: "browser", W: 1280, H: 720}, nil
}

func (b *testBroker) IDSalt(*Tab) string { return "test-salt" }

func (b *testBroker) MediaChanged(_ *Tab, a MediaActivity) {
	b.mu.Lock()
	b.changed = append(b.changed, a)
	b.mu.Unlock()
}

func (b *testBroker) lastActivity() MediaActivity {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.changed) == 0 {
		return MediaActivity{}
	}
	return b.changed[len(b.changed)-1]
}

func allow(r MediaRequest) MediaDecision {
	return MediaDecision{Allow: true, SourceID: func() string {
		if len(r.Sources) > 0 {
			return r.Sources[0].ID
		}
		return ""
	}()}
}

// capServer serves one blank page; headers are added by the hdr function.
func capServer(t *testing.T, hdr map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for k, v := range hdr {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<!doctype html><title>t</title><body><video id=v autoplay muted></video><canvas id=c width=64 height=48></canvas></body>"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func capTab(t *testing.T, url string, b MediaBroker) *Tab {
	t.Helper()
	o := DefaultOptions()
	o.SettleMs = 30
	e := New(o)
	t.Cleanup(func() { e.Close() })
	tab, err := e.NewTab("about:blank")
	if err != nil {
		t.Fatal(err)
	}
	if b != nil {
		tab.SetMediaBroker(b)
	}
	if err := tab.Navigate(url, nil); err != nil {
		t.Fatal(err)
	}
	tab.Settle(50 * time.Millisecond)
	return tab
}

func fakeOn(t *testing.T) { t.Setenv("SWEEF_FAKE_DEVICES", "1") }

func TestGetUserMediaPromptsAndDescribesTheRealMode(t *testing.T) {
	fakeOn(t)
	srv := capServer(t, nil)
	b := &testBroker{decide: allow}
	tab := capTab(t, srv.URL, b)
	var out map[string]any
	evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({audio:true,video:{width:{ideal:1280},height:{ideal:720},frameRate:{ideal:15}}}).then(function(s){
	  window.s=s; var v=s.getVideoTracks()[0], a=s.getAudioTracks()[0];
	  return {n:s.getTracks().length, active:s.active, vlabel:v.label, alabel:a.label, vs:v.getSettings(), as:a.getSettings(), vc:v.getCapabilities(), vid:v.id, state:v.readyState,
	    ctor:Object.prototype.toString.call(v), kind:v.kind+'/'+a.kind}})`, &out)
	if out["n"].(float64) != 2 || out["active"] != true || out["vlabel"] != "Fake Camera" || out["alabel"] != "Fake Microphone" || out["kind"] != "video/audio" {
		t.Fatalf("stream: %v", out)
	}
	vs := out["vs"].(map[string]any)
	if vs["width"].(float64) != 1280 || vs["height"].(float64) != 720 || vs["frameRate"].(float64) != 15 || vs["facingMode"] != "user" || vs["resizeMode"] != "none" {
		t.Fatalf("video settings are not the negotiated mode: %v", vs)
	}
	if id, _ := vs["deviceId"].(string); len(id) != 64 || strings.Contains(id, "synthetic") {
		t.Fatalf("device id must be a per-origin hash, got %q", id)
	}
	as := out["as"].(map[string]any)
	if as["sampleRate"].(float64) != 48000 || as["channelCount"].(float64) != 1 || as["echoCancellation"] != false {
		t.Fatalf("audio settings: %v", as)
	}
	vc := out["vc"].(map[string]any)
	if vc["width"].(map[string]any)["max"].(float64) != 1920 || vc["frameRate"].(map[string]any)["max"].(float64) != 60 {
		t.Fatalf("capabilities: %v", vc)
	}
	if len(b.reqs) != 1 || !b.reqs[0].Video || !b.reqs[0].Audio || len(b.reqs[0].VideoDevices) != 1 || b.reqs[0].Origin != strings.TrimSuffix(srv.URL, "/") {
		t.Fatalf("broker saw %+v", b.reqs)
	}
	// the Go side can find the live track by the id the page sees
	id := out["vid"].(string)
	tr := capture.LookupTrack(id)
	if tr == nil {
		t.Fatal("capture.LookupTrack cannot find the page's track")
	}
	frames, cancel := tr.SubscribeVideo()
	f := <-frames
	cancel()
	if f.W != 1280 || f.H != 720 {
		t.Fatalf("frame %dx%d", f.W, f.H)
	}
	if act := b.lastActivity(); act.Count("camera") != 1 || act.Count("microphone") != 1 {
		t.Fatalf("activity %+v", act)
	}
	// stop() releases the devices
	evalJSON(t, tab, `s.getTracks().forEach(function(t){t.stop()}), s.getTracks().map(function(t){return t.readyState})`, nil)
	deadline := time.Now().Add(2 * time.Second)
	for capture.LiveTracks() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if capture.LiveTracks() != 0 {
		t.Fatalf("%d tracks still live after stop()", capture.LiveTracks())
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && len(b.lastActivity().Sessions) != 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if len(b.lastActivity().Sessions) != 0 {
		t.Fatalf("the broker was not told the capture ended: %+v", b.lastActivity())
	}
}

func TestGetUserMediaErrors(t *testing.T) {
	fakeOn(t)
	srv := capServer(t, nil)
	b := &testBroker{decide: allow}
	tab := capTab(t, srv.URL, b)
	run := func(js string) map[string]any {
		var out map[string]any
		evalJSON(t, tab, `navigator.mediaDevices.getUserMedia(`+js+`).then(function(){return {ok:true}},function(e){return {name:e.name,msg:e.message,constraint:e.constraint,isDOM:e instanceof DOMException,isOver:(typeof OverconstrainedError==='function')&&e instanceof OverconstrainedError,isType:e instanceof TypeError}})`, &out)
		return out
	}
	if r := run(`{}`); r["name"] != "TypeError" || r["isType"] != true {
		t.Errorf("empty constraints: %v", r)
	}
	if r := run(`{video:false,audio:false}`); r["name"] != "TypeError" {
		t.Errorf("both false: %v", r)
	}
	if r := run(`{video:{width:{exact:99999}}}`); r["name"] != "OverconstrainedError" || r["constraint"] != "width" || r["isOver"] != true || r["isDOM"] != true {
		t.Errorf("exact width: %v", r)
	}
	if r := run(`{video:{frameRate:{min:500}}}`); r["constraint"] != "frameRate" {
		t.Errorf("min fps: %v", r)
	}
	if r := run(`{video:{deviceId:{exact:"nonexistent"}}}`); r["constraint"] != "deviceId" {
		t.Errorf("device id: %v", r)
	}
	if r := run(`{video:{facingMode:{exact:"environment"}}}`); r["name"] != "OverconstrainedError" {
		t.Errorf("facing: %v", r)
	}
	if r := run(`{audio:{echoCancellation:{exact:true}}}`); r["constraint"] != "echoCancellation" {
		t.Errorf("AEC exact: %v", r)
	}
	if r := run(`{video:{width:"bogus-not-number-but-string",height:{min:"x"}}}`); r["name"] != "TypeError" {
		t.Errorf("bad min: %v", r)
	}
	if n := len(b.reqs); n != 0 {
		t.Errorf("the user was prompted %d times for requests that can never succeed", n)
	}
	// denied by the user, dismissed, and denied by stored permission
	b.decide = func(MediaRequest) MediaDecision { return MediaDecision{} }
	if r := run(`{video:true}`); r["name"] != "NotAllowedError" || r["msg"] != "Permission denied" {
		t.Errorf("denied: %v", r)
	}
	b.decide = func(MediaRequest) MediaDecision { return MediaDecision{Dismissed: true} }
	if r := run(`{audio:true}`); r["name"] != "NotAllowedError" || r["msg"] != "Permission dismissed" {
		t.Errorf("dismissed: %v", r)
	}
	b.mu.Lock()
	b.perms = map[string]string{"camera": "denied"}
	b.mu.Unlock()
	nreq := len(b.reqs)
	if r := run(`{video:true}`); r["name"] != "NotAllowedError" {
		t.Errorf("stored denial: %v", r)
	}
	if len(b.reqs) != nreq {
		t.Error("a stored denial must not prompt")
	}
	// microphone alone is still allowed to prompt
	b.decide = allow
	if r := run(`{audio:true}`); r["ok"] != true {
		t.Errorf("mic after camera was blocked: %v", r)
	}
}

func TestStoredGrantDoesNotPrompt(t *testing.T) {
	fakeOn(t)
	srv := capServer(t, nil)
	b := &testBroker{perms: map[string]string{"camera": "granted", "microphone": "granted"}}
	tab := capTab(t, srv.URL, b)
	var out map[string]any
	evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({video:true,audio:true}).then(function(s){return {n:s.getTracks().length}})`, &out)
	if out["n"].(float64) != 2 || len(b.reqs) != 0 {
		t.Fatalf("n=%v prompts=%d", out["n"], len(b.reqs))
	}
}

func TestChosenDeviceInPromptIsHonoured(t *testing.T) {
	fakeOn(t)
	other := &otherCam{}
	capture.Register(other)
	defer capture.Unregister("zz-other")
	srv := capServer(t, nil)
	b := &testBroker{decide: func(r MediaRequest) MediaDecision {
		if len(r.VideoDevices) != 2 {
			t.Errorf("prompt offered %d cameras, want 2", len(r.VideoDevices))
		}
		return MediaDecision{Allow: true, VideoID: "other-cam"}
	}}
	tab := capTab(t, srv.URL, b)
	var out map[string]any
	evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({video:true}).then(function(s){return {label:s.getVideoTracks()[0].label}})`, &out)
	if out["label"] != "Other Camera" {
		t.Fatalf("the user's choice was ignored: %v", out)
	}
}

type otherCam struct{}

func (otherCam) Name() string { return "zz-other" }
func (otherCam) Devices() []capture.DeviceInfo {
	return []capture.DeviceInfo{{ID: "other-cam", GroupID: "g", Label: "Other Camera", Kind: capture.VideoInput}}
}
func (otherCam) OpenVideo(id string) (capture.VideoSource, error) {
	return synthetic.NewCamera(capture.DeviceInfo{ID: id, Label: "Other Camera", Kind: capture.VideoInput}, 320, 240, 30), nil
}
func (otherCam) OpenAudio(string) (capture.AudioSource, error) { return nil, capture.ErrUnsupported }

func TestEnumerateDevicesHidesLabelsUntilPermission(t *testing.T) {
	fakeOn(t)
	srvA, srvB := capServer(t, nil), capServer(t, nil)
	b := &testBroker{decide: allow}
	tabA := capTab(t, srvA.URL, b)
	var before, after []map[string]any
	evalJSON(t, tabA, `navigator.mediaDevices.enumerateDevices().then(function(l){return l.map(function(d){return {k:d.kind,id:d.deviceId,l:d.label,g:d.groupId,json:JSON.stringify(d)}})})`, &before)
	if len(before) != 3 {
		t.Fatalf("one anonymous entry per kind expected, got %v", before)
	}
	for _, d := range before {
		if d["id"] != "" || d["l"] != "" || d["g"] != "" {
			t.Fatalf("identity leaked before permission: %v", d)
		}
	}
	evalJSON(t, tabA, `navigator.mediaDevices.getUserMedia({video:true}).then(function(s){window.s=s;return navigator.mediaDevices.enumerateDevices()}).then(function(l){return l.map(function(d){return {k:d.kind,id:d.deviceId,l:d.label}})})`, &after)
	labels := map[string]string{}
	idsA := map[string]string{}
	for _, d := range after {
		if d["k"] == "videoinput" {
			labels["v"] = d["l"].(string)
			idsA["v"] = d["id"].(string)
		}
		if d["k"] == "audioinput" && d["l"] != "" {
			t.Errorf("microphone label shown though only the camera was granted: %v", d)
		}
	}
	if labels["v"] != "Fake Camera" || len(idsA["v"]) != 64 {
		t.Fatalf("after grant: %v", after)
	}
	// the same camera has another id on another origin
	b2 := &testBroker{decide: allow}
	tabB := capTab(t, srvB.URL, b2)
	var other []map[string]any
	evalJSON(t, tabB, `navigator.mediaDevices.getUserMedia({video:true}).then(function(){return navigator.mediaDevices.enumerateDevices()}).then(function(l){return l.map(function(d){return {k:d.kind,id:d.deviceId}})})`, &other)
	for _, d := range other {
		if d["k"] == "videoinput" && d["id"] == idsA["v"] {
			t.Fatal("device ids are the same on two origins: they can be used to track the user across sites")
		}
	}
	// a deviceId from enumerateDevices works as a constraint
	var okc bool
	evalJSON(t, tabA, `navigator.mediaDevices.getUserMedia({video:{deviceId:{exact:`+jsq(idsA["v"])+`}}}).then(function(){return true},function(e){return e.name})`, &okc)
	if !okc {
		t.Fatal("exact deviceId from enumerateDevices rejected")
	}
}

func jsq(s string) string { return `"` + s + `"` }

func TestApplyConstraintsAndClone(t *testing.T) {
	fakeOn(t)
	srv := capServer(t, nil)
	tab := capTab(t, srv.URL, &testBroker{decide: allow})
	var out map[string]any
	evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({video:true}).then(function(s){
	  var t=s.getVideoTracks()[0]; window.t=t; var before=t.getSettings();
	  return t.applyConstraints({width:{exact:320},height:{exact:240},frameRate:10}).then(function(){
	    var c=t.clone(); window.c=c;
	    return {before:before, after:t.getSettings(), cons:t.getConstraints(), clone:c.getSettings(), cloneId:c.id!==t.id, enabled:c.enabled};
	  })})`, &out)
	if b := out["before"].(map[string]any); b["width"].(float64) != 640 || b["height"].(float64) != 480 || b["frameRate"].(float64) != 30 {
		t.Fatalf("default mode: %v", b)
	}
	if a := out["after"].(map[string]any); a["width"].(float64) != 320 || a["height"].(float64) != 240 || a["frameRate"].(float64) != 10 {
		t.Fatalf("applyConstraints did not change the real mode: %v", a)
	}
	if c := out["cons"].(map[string]any); c["frameRate"].(float64) != 10 {
		t.Fatalf("getConstraints: %v", c)
	}
	if out["cloneId"] != true {
		t.Fatal("clone has the same id")
	}
	// the frames really come at the new size
	var wid string
	evalJSON(t, tab, `t.id`, &wid)
	tr := capture.LookupTrack(wid)
	frames, cancel := tr.SubscribeVideo()
	var f *capture.Frame
	for i := 0; i < 20; i++ {
		f = <-frames
		if f.W == 320 {
			break
		}
	}
	cancel()
	if f.W != 320 || f.H != 240 {
		t.Fatalf("frames still %dx%d after applyConstraints", f.W, f.H)
	}
	// impossible constraints reject with the constraint name and leave the track alone
	var rej map[string]any
	evalJSON(t, tab, `t.applyConstraints({width:{exact:99999}}).then(function(){return {ok:1}},function(e){return {name:e.name,constraint:e.constraint,w:t.getSettings().width}})`, &rej)
	if rej["name"] != "OverconstrainedError" || rej["constraint"] != "width" || rej["w"].(float64) != 320 {
		t.Fatalf("%v", rej)
	}
	// stopping the original leaves the clone running
	var st []string
	evalJSON(t, tab, `t.stop(), [t.readyState, c.readyState]`, &st)
	if st[0] != "ended" || st[1] != "live" {
		t.Fatalf("states %v", st)
	}
	if capture.LookupTrack(wid) != nil {
		t.Fatal("stopped track still registered")
	}
}

func TestEnabledFalseSendsBlack(t *testing.T) {
	fakeOn(t)
	srv := capServer(t, nil)
	tab := capTab(t, srv.URL, &testBroker{decide: allow})
	var id string
	evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({video:true}).then(function(s){window.t=s.getVideoTracks()[0];t.enabled=false;return t.id})`, &id)
	frames, cancel := capture.LookupTrack(id).SubscribeVideo()
	defer cancel()
	for i := 0; i < 3; i++ {
		f := <-frames
		for _, y := range f.Y {
			if y != 16 {
				t.Fatalf("a disabled track delivered picture data")
			}
		}
	}
}

func TestTrackEndedByUserAndDevice(t *testing.T) {
	fakeOn(t)
	srv := capServer(t, nil)
	b := &testBroker{decide: allow}
	tab := capTab(t, srv.URL, b)
	var id string
	evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({video:true,audio:true}).then(function(s){window.log=[];s.getTracks().forEach(function(t){t.onended=function(){log.push(t.kind)}});window.s=s;return s.getVideoTracks()[0].id})`, &id)
	sessions := tab.CaptureSessions()
	if len(sessions) != 2 {
		t.Fatalf("sessions %+v", sessions)
	}
	tab.EndCaptureKind("camera") // the user revoked the camera in the site settings
	capWaitJS(t, tab, `log.length===1`)
	var res map[string]any
	evalJSON(t, tab, `({log:log, v:s.getVideoTracks()[0].readyState, a:s.getAudioTracks()[0].readyState, active:s.active})`, &res)
	if res["v"] != "ended" || res["a"] != "live" || res["active"] != true || res["log"].([]any)[0] != "video" {
		t.Fatalf("%v", res)
	}
	// the microphone is pulled away (unplugged): the capture layer ends the track with the device reason
	for _, sess := range tab.CaptureSessions() {
		capture.LookupTrack(sess.ID).End(capture.EndDevice, nil)
	}
	capWaitJS(t, tab, `log.length===2`)
	var active bool
	evalJSON(t, tab, `s.active`, &active)
	if active {
		t.Fatal("stream still active after all its tracks ended")
	}
}

func capWaitJS(t *testing.T, tab *Tab, cond string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		tab.Settle(30 * time.Millisecond)
		time.Sleep(20 * time.Millisecond)
		var ok bool
		evalJSON(t, tab, cond, &ok)
		if ok {
			return
		}
	}
	t.Fatalf("condition never became true: %s", cond)
}

func TestInsecureContextAndPolicy(t *testing.T) {
	fakeOn(t)
	// an http page that is not localhost is not a secure context
	srv := capServer(t, nil)
	o := DefaultOptions()
	o.SettleMs = 30
	e := New(o)
	defer e.Close()
	tab, _ := e.NewTab("about:blank")
	tab.SetMediaBroker(&testBroker{decide: allow})
	tab.Route("http://insecure.example/**", func(*http.Request) (int, string, bool) {
		return 200, "<!doctype html><title>x</title><body></body>", true
	})
	if err := tab.Navigate("http://insecure.example/page", nil); err != nil {
		t.Fatal(err)
	}
	var r []any
	evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({video:true}).then(function(){return ['ok']},function(e){return [e.name, window.isSecureContext]})`, &r)
	if r[0] != "SecurityError" || r[1] != false {
		t.Fatalf("insecure context: %v", r)
	}
	// ...and localhost is
	tab2 := capTab(t, srv.URL, &testBroker{decide: allow})
	var sec bool
	evalJSON(t, tab2, `window.isSecureContext`, &sec)
	if !sec {
		t.Fatal("http://127.0.0.1 must be a secure context")
	}

	// Permissions-Policy: camera=() closes the camera for the document itself, microphone stays
	psrv := capServer(t, map[string]string{"Permissions-Policy": "camera=()"})
	tab3 := capTab(t, psrv.URL, &testBroker{decide: allow})
	var out []string
	evalJSON(t, tab3, `Promise.all([
	  navigator.mediaDevices.getUserMedia({video:true}).then(function(){return 'ok'},function(e){return e.name}),
	  navigator.mediaDevices.getUserMedia({audio:true}).then(function(){return 'ok'},function(e){return e.name})])`, &out)
	if out[0] != "NotAllowedError" || out[1] != "ok" {
		t.Fatalf("policy: %v", out)
	}
}

func TestNoBrokerDeniesUnlessFakeDevices(t *testing.T) {
	srv := capServer(t, nil)
	tab := capTab(t, srv.URL, nil)
	var out []string
	evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({audio:true}).then(function(){return ['ok']},function(e){return [e.name]})`, &out)
	if out[0] != "NotFoundError" { // no devices on this machine: nothing to ask permission for
		t.Fatalf("no devices: %v", out)
	}
	t.Setenv("SWEEF_FAKE_DEVICES", "1")
	tab2 := capTab(t, srv.URL, nil)
	evalJSON(t, tab2, `navigator.mediaDevices.getUserMedia({audio:true}).then(function(s){return ['ok',s.getAudioTracks()[0].label]},function(e){return [e.name]})`, &out)
	if out[0] != "ok" {
		t.Fatalf("fake devices without a broker should auto-grant like --use-fake-ui-for-media-stream: %v", out)
	}
	// display capture needs a broker
	evalJSON(t, tab2, `navigator.mediaDevices.getDisplayMedia({video:true}).then(function(){return ['ok']},function(e){return [e.name]})`, &out)
	if out[0] != "NotAllowedError" {
		t.Fatalf("getDisplayMedia without a broker: %v", out)
	}
}

func TestGetDisplayMedia(t *testing.T) {
	srv := capServer(t, nil)
	b := &testBroker{
		sources: []DisplaySource{{ID: "tab:1", Title: "Test page", Surface: "browser", Current: true, TabID: "1"}, {ID: "window:main", Title: "Sweef window", Surface: "window"}},
		decide:  func(r MediaRequest) MediaDecision { return MediaDecision{Allow: true, SourceID: "window:main"} },
	}
	tab := capTab(t, srv.URL, b)
	var out map[string]any
	evalJSON(t, tab, `navigator.mediaDevices.getDisplayMedia({video:{width:{max:640},frameRate:{ideal:10},displaySurface:'window'},audio:true,preferCurrentTab:false}).then(function(s){
	  window.s=s; var v=s.getVideoTracks()[0]; return {n:s.getTracks().length, label:v.label, st:v.getSettings(), id:v.id}})`, &out)
	if out["n"].(float64) != 1 {
		t.Fatalf("audio:true must not invent a track (no tab audio yet): %v", out)
	}
	st := out["st"].(map[string]any)
	if st["displaySurface"] != "browser" || st["width"].(float64) != 640 || st["height"].(float64) != 360 || st["frameRate"].(float64) != 10 {
		t.Fatalf("display settings: %v", st)
	}
	if len(b.reqs) != 1 || !b.reqs[0].Display || len(b.reqs[0].Sources) != 2 || b.reqs[0].Surface != "window" || !b.reqs[0].WantAudio {
		t.Fatalf("chooser request: %+v", b.reqs)
	}
	if len(b.opened) != 1 || b.opened[0] != "window:main" {
		t.Fatalf("opened %v", b.opened)
	}
	tr := capture.LookupTrack(out["id"].(string))
	frames, cancel := tr.SubscribeVideo()
	f := <-frames
	cancel()
	if f.W != 640 || f.H != 360 {
		// the synthetic source ignores the target size; the engine reports the requested one: both must agree
		t.Logf("source delivered %dx%d (the test source is not scaled)", f.W, f.H)
	}
	if got := b.lastActivity().Count("screen"); got != 1 {
		t.Fatalf("activity %+v", b.lastActivity())
	}
	// "Stop sharing" in the chrome: the app ends the session, the page sees `ended`
	evalJSON(t, tab, `window.ended=false; s.getVideoTracks()[0].onended=function(){ended=true}; true`, nil)
	tab.EndCapture(out["id"].(string))
	capWaitJS(t, tab, `ended===true && s.active===false`)
	// video:false is a TypeError
	var r []string
	evalJSON(t, tab, `navigator.mediaDevices.getDisplayMedia({video:false,audio:true}).then(function(){return ['ok']},function(e){return [e.name]})`, &r)
	if r[0] != "TypeError" {
		t.Fatalf("%v", r)
	}
	// a blocked screen permission never reaches the chooser
	b.mu.Lock()
	b.perms = map[string]string{"display-capture": "denied"}
	n := len(b.reqs)
	b.mu.Unlock()
	evalJSON(t, tab, `navigator.mediaDevices.getDisplayMedia().then(function(){return ['ok']},function(e){return [e.name]})`, &r)
	if r[0] != "NotAllowedError" || len(b.reqs) != n {
		t.Fatalf("%v %d", r, len(b.reqs)-n)
	}
	// user cancels the chooser
	b.mu.Lock()
	b.perms = nil
	b.decide = func(MediaRequest) MediaDecision { return MediaDecision{Dismissed: true} }
	b.mu.Unlock()
	evalJSON(t, tab, `navigator.mediaDevices.getDisplayMedia({video:true}).then(function(){return ['ok']},function(e){return [e.name+':'+e.message]})`, &r)
	if r[0] != "NotAllowedError:Permission dismissed" {
		t.Fatalf("%v", r)
	}
}

func TestPermissionsAPI(t *testing.T) {
	fakeOn(t)
	srv := capServer(t, nil)
	b := &testBroker{perms: map[string]string{"camera": "granted", "microphone": "denied"}}
	tab := capTab(t, srv.URL, b)
	var out map[string]any
	evalJSON(t, tab, `Promise.all([navigator.permissions.query({name:'camera'}),navigator.permissions.query({name:'microphone'}),navigator.permissions.query({name:'display-capture'}),navigator.permissions.query({name:'geolocation'})]).then(function(r){
	  window.cam=r[0]; window.log=[]; cam.onchange=function(){log.push(cam.state)};
	  return {cam:r[0].state,mic:r[1].state,scr:r[2].state,geo:r[3].state,name:r[0].name,proto:Object.prototype.toString.call(r[0])}})`, &out)
	if out["cam"] != "granted" || out["mic"] != "denied" || out["scr"] != "prompt" || out["geo"] != "prompt" || out["name"] != "camera" || out["proto"] != "[object PermissionStatus]" {
		t.Fatalf("%v", out)
	}
	b.mu.Lock()
	b.perms["camera"] = "denied"
	b.mu.Unlock()
	tab.NotifyMediaPermissionChanged()
	capWaitJS(t, tab, `log.length===1 && log[0]==='denied' && cam.state==='denied'`)
	var bad string
	evalJSON(t, tab, `navigator.permissions.query({}).then(function(){return 'ok'},function(e){return e.name})`, &bad)
	if bad != "TypeError" {
		t.Fatalf("query without a name: %s", bad)
	}
}

func TestDeviceChangeEvent(t *testing.T) {
	fakeOn(t)
	srv := capServer(t, nil)
	tab := capTab(t, srv.URL, &testBroker{decide: allow})
	evalJSON(t, tab, `window.n=0; navigator.mediaDevices.ondevicechange=function(){n++}; navigator.mediaDevices.addEventListener('devicechange',function(){n+=10}); true`, nil)
	capture.Register(otherCam{})
	defer capture.Unregister("zz-other")
	capWaitJS(t, tab, `n===11`)
}

func TestPreviewShowsFrames(t *testing.T) {
	fakeOn(t)
	srv := capServer(t, nil)
	tab := capTab(t, srv.URL, &testBroker{decide: allow})
	var out map[string]any
	evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({video:{width:320,height:240}}).then(function(s){
	  var v=document.getElementById('v'); v.srcObject=s; return new Promise(function(res){ setTimeout(function(){ res({w:v.videoWidth,h:v.videoHeight,rs:v.readyState}) }, 300) })})`, &out)
	if out["w"].(float64) != 320 || out["h"].(float64) != 240 {
		t.Fatalf("videoWidth/Height of a live preview: %v", out)
	}
	// drawing the element into a canvas shows the camera's picture (the test pattern has coloured bars)
	var px map[string]any
	for i := 0; i < 20; i++ {
		tab.Settle(60 * time.Millisecond)
		evalJSON(t, tab, `(function(){var c=document.getElementById('c'),x=c.getContext('2d');x.drawImage(document.getElementById('v'),0,0,64,48);var d=x.getImageData(0,24,64,1).data;var distinct={};for(var i=0;i<64;i++)distinct[d[i*4]+','+d[i*4+1]+','+d[i*4+2]]=1;return {colors:Object.keys(distinct).length}})()`, &px)
		if px["colors"].(float64) >= 4 {
			break
		}
	}
	if px["colors"].(float64) < 4 {
		t.Fatalf("the canvas copy of the camera preview has %v colours; the colour bars are missing", px["colors"])
	}
}

func TestNoLeakAcrossManyRequests(t *testing.T) {
	fakeOn(t)
	srv := capServer(t, nil)
	tab := capTab(t, srv.URL, &testBroker{decide: allow})
	runtime.GC()
	before := runtime.NumGoroutine()
	for i := 0; i < 100; i++ {
		var ok bool
		evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({video:true,audio:true}).then(function(s){s.getTracks().forEach(function(t){t.stop()});return true})`, &ok)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && (capture.LiveTracks() != 0 || runtime.NumGoroutine() > before+6) {
		time.Sleep(20 * time.Millisecond)
	}
	if capture.LiveTracks() != 0 {
		t.Fatalf("%d tracks leaked", capture.LiveTracks())
	}
	if n := runtime.NumGoroutine(); n > before+6 {
		t.Fatalf("goroutines %d -> %d after 100 getUserMedia/stop cycles", before, n)
	}
}

func TestNavigationEndsCaptureAndCancelsPrompt(t *testing.T) {
	fakeOn(t)
	srv := capServer(t, nil)
	b := &testBroker{decide: allow}
	tab := capTab(t, srv.URL, b)
	evalJSON(t, tab, `navigator.mediaDevices.getUserMedia({video:true}).then(function(s){window.s=s;return true})`, nil)
	if capture.LiveTracks() != 1 {
		t.Fatalf("live tracks %d", capture.LiveTracks())
	}
	if err := tab.Navigate(srv.URL+"/other", nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for capture.LiveTracks() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if capture.LiveTracks() != 0 {
		t.Fatal("camera still on after navigating away")
	}
	// a prompt nobody answers is cancelled when the page goes away
	gate := make(chan struct{})
	cancelled := make(chan struct{})
	b2 := &hangBroker{testBroker: testBroker{}, gate: gate, cancelled: cancelled}
	tab2 := capTab(t, srv.URL, b2)
	evalJSON(t, tab2, `window.res='pending'; navigator.mediaDevices.getUserMedia({video:true}).then(function(){res='ok'},function(e){res=e.name}); true`, nil)
	<-gate
	if err := tab2.Navigate(srv.URL+"/x", nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("the pending prompt was not cancelled by the navigation")
	}
}

type hangBroker struct {
	testBroker
	gate, cancelled chan struct{}
}

func (h *hangBroker) RequestMedia(ctx context.Context, r MediaRequest) MediaDecision {
	close(h.gate)
	<-ctx.Done()
	close(h.cancelled)
	return MediaDecision{}
}
