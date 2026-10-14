package engine

// capture_host.go: getUserMedia, getDisplayMedia, enumerateDevices, the Permissions API for camera and microphone,
// and the bridge from MediaStreamTracks to internal/capture sources.
//
// The JavaScript half lives in media_prelude.js; this file is the host side. A request runs on its own goroutine
// (it may wait for the user to answer a prompt for minutes), and its outcome is queued for the event loop like a
// WebSocket frame (asyncEvent kind "cap") and delivered to __swCapEvent. Nothing here calls into the VM.
//
// Who decides what: the engine solves constraints (internal/capture/constraints) and talks to devices
// (internal/capture); the embedding app, through MediaBroker, owns permissions, the prompt and the screen
// sources. Without a broker the engine behaves like a browser started with --use-fake-ui-for-media-stream when
// fake devices are on (SWEEF_FAKE_DEVICES=1) and denies everything otherwise, so a headless agent can never open
// a real camera by accident.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
	"github.com/sweefbrowser/sweef/internal/capture/constraints"
	"github.com/sweefbrowser/sweef/internal/capture/policy"
	"github.com/sweefbrowser/sweef/internal/capture/synthetic"

	_ "github.com/sweefbrowser/sweef/internal/capture/platform" // registers the OS cameras and microphones
)

// Permission names used by the broker and the Permissions API.
const (
	PermCamera     = "camera"
	PermMicrophone = "microphone"
	PermDisplay    = "display-capture"
)

// MediaBroker is implemented by the application that embeds the engine.
type MediaBroker interface {
	// MediaPermission returns the stored decision for an origin: "granted", "denied" or "prompt".
	MediaPermission(t *Tab, origin, name string) string
	// RequestMedia asks the user. It blocks until the user answers or ctx is cancelled (the page went away).
	RequestMedia(ctx context.Context, r MediaRequest) MediaDecision
	// DisplaySources lists what getDisplayMedia may share from this tab's window: the tab itself, the other tabs
	// and the browser window.
	DisplaySources(t *Tab) []DisplaySource
	// OpenDisplay returns a video source for a display source id and the surface's native size.
	OpenDisplay(t *Tab, sourceID string) (capture.VideoSource, DisplayInfo, error)
	// IDSalt is the secret mixed into the per-origin device ids. It must be stable across restarts.
	IDSalt(t *Tab) string
	// MediaChanged reports the live capture sessions of a tab after every change (may be called from any goroutine).
	MediaChanged(t *Tab, act MediaActivity)
}

// MediaRequest is what the prompt shows.
type MediaRequest struct {
	Tab    *Tab
	Origin string
	URL    string
	// Video and Audio say which kinds the page asked for; the device lists hold the candidates that satisfy the
	// constraints (the solver's pick first) so the prompt can offer a choice.
	Video, Audio               bool
	VideoDevices, AudioDevices []capture.DeviceInfo
	// Display requests: the sources to choose from and the page's hints.
	Display          bool
	Sources          []DisplaySource
	PreferCurrentTab bool
	WantAudio        bool
	Surface          string // the page's displaySurface hint: "browser", "window" or ""
}

// MediaDecision is the user's answer.
type MediaDecision struct {
	Allow     bool
	Dismissed bool   // closed without choosing (reported as "Permission dismissed")
	VideoID   string // chosen camera (a real device id), "" = the solver's pick
	AudioID   string
	SourceID  string // chosen display source
}

// DisplaySource is one thing getDisplayMedia can share.
type DisplaySource struct {
	ID      string
	Title   string
	URL     string
	Surface string // "browser" (a tab) or "window" (the whole browser window)
	Current bool   // the tab that is asking
	TabID   string
}

// DisplayInfo describes an opened display source.
type DisplayInfo struct {
	Label   string
	Surface string
	W, H    int // native size in pixels
}

// MediaSession is one live capture track.
type MediaSession struct {
	ID      string
	Kind    string // "camera", "microphone" or "screen"
	Label   string
	Surface string
}

// MediaActivity is the live capture state of a tab.
type MediaActivity struct {
	Sessions []MediaSession
}

// Count returns the number of live sessions of a kind.
func (a MediaActivity) Count(kind string) int {
	n := 0
	for _, s := range a.Sessions {
		if s.Kind == kind {
			n++
		}
	}
	return n
}

// ---- Tab API ------------------------------------------------------------------------------------------------

// SetMediaBroker attaches the application's permission and display-source handler.
func (t *Tab) SetMediaBroker(b MediaBroker) {
	t.mediaBroker.Store(&b)
}

func (t *Tab) broker() MediaBroker {
	if p := t.mediaBroker.Load(); p != nil {
		return *p
	}
	return nil
}

func (t *Tab) capHost() *capHost {
	t.mu.RLock()
	j := t.js
	t.mu.RUnlock()
	if j == nil {
		return nil
	}
	return j.capture
}

// CaptureSessions lists the live capture tracks of the current document.
func (t *Tab) CaptureSessions() []MediaSession {
	if h := t.capHost(); h != nil {
		return h.sessions()
	}
	return nil
}

// EndCapture ends one capture session ("Stop sharing", a revoked permission). The page sees the track end.
func (t *Tab) EndCapture(sessionID string) {
	if h := t.capHost(); h != nil {
		h.endSession(sessionID, capture.EndUser)
	}
}

// EndCaptureKind ends every session of a kind ("camera", "microphone", "screen"), e.g. after the user revokes the
// permission in the site settings.
func (t *Tab) EndCaptureKind(kind string) {
	if h := t.capHost(); h != nil {
		for _, s := range h.sessions() {
			if s.Kind == kind {
				h.endSession(s.ID, capture.EndUser)
			}
		}
	}
}

// NotifyMediaPermissionChanged tells the page that a stored permission changed (PermissionStatus change events).
func (t *Tab) NotifyMediaPermissionChanged() {
	if h := t.capHost(); h != nil {
		h.push(0, "perm", "")
	}
}

// setDocPolicy remembers the document's Permissions-Policy.
func (t *Tab) setDocPolicy(permissionsPolicy, featurePolicy string) {
	h := permissionsPolicy
	if h == "" {
		h = featurePolicy
	}
	t.docPolicy.Store(h)
}

// ---- host ---------------------------------------------------------------------------------------------------

type capTrack struct {
	t        *capture.Track
	kind     string // camera | microphone | screen
	real     string // real device id
	label    string
	settings map[string]any
	caps     map[string]any
	vdevs    []constraints.VideoDevice // for applyConstraints on cameras
	cancel   func()
}

type capHost struct {
	j        *jsVM
	mu       sync.Mutex
	tracks   map[string]*capTrack
	reqs     map[int64]context.CancelFunc
	closed   bool
	watch    chan struct{}
	previews map[int64]func()
}

func init() {
	registerHostExtension("capture", func(j *jsVM) map[string]hostFn {
		h := &capHost{j: j, tracks: map[string]*capTrack{}, reqs: map[int64]context.CancelFunc{}}
		j.capture = h
		return h.fns()
	})
}

func (h *capHost) tab() *Tab { return h.j.tab }

func (h *capHost) fns() map[string]hostFn {
	return map[string]hostFn{
		"capRequest": h.hostRequest,
		"capEnumerate": func(a []any) (any, error) {
			return h.enumerate(), nil
		},
		"capStop": func(a []any) (any, error) {
			h.endSession(argS(a, 0), capture.EndStopped)
			return nil, nil
		},
		"capEnable": func(a []any) (any, error) {
			if ct := h.track(argS(a, 0)); ct != nil {
				ct.t.SetEnabled(argB(a, 1))
			}
			return nil, nil
		},
		"capClone": func(a []any) (any, error) {
			ct := h.track(argS(a, 0))
			if ct == nil {
				return nil, nil
			}
			c := ct.t.Clone()
			if c == nil {
				return nil, nil
			}
			n := *ct
			n.t = c
			h.add(&n)
			return h.trackJSON(&n), nil
		},
		"capPerm": func(a []any) (any, error) { return h.permState(argS(a, 0)), nil },
		"capWatch": func(a []any) (any, error) {
			h.setWatch(argB(a, 0))
			return nil, nil
		},
		"capBindPreview":   h.hostBindPreview,
		"capUnbindPreview": h.hostUnbindPreview,
	}
}

func (h *capHost) push(tok int64, typ, data string) {
	r := h.j.rt()
	r.push(asyncEvent{kind: "cap", id: tok, typ: typ, data: data})
	if t := h.j.tab; t != nil {
		t.wake()
	}
}

func (h *capHost) track(id string) *capTrack {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.tracks[id]
}

func (h *capHost) sessions() []MediaSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]MediaSession, 0, len(h.tracks))
	for _, ct := range h.tracks {
		out = append(out, MediaSession{ID: ct.t.ID, Kind: ct.kind, Label: ct.label, Surface: ct.t.Surface})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (h *capHost) notify() {
	if b := h.tab().broker(); b != nil {
		b.MediaChanged(h.tab(), MediaActivity{Sessions: h.sessions()})
	}
}

// add registers a track and wires its end into the page.
func (h *capHost) add(ct *capTrack) {
	h.mu.Lock()
	h.tracks[ct.t.ID] = ct
	h.mu.Unlock()
	go func() {
		<-ct.t.Ended()
		reason, _ := ct.t.EndInfo()
		h.mu.Lock()
		delete(h.tracks, ct.t.ID)
		closed := h.closed
		h.mu.Unlock()
		if ct.cancel != nil {
			ct.cancel()
		}
		if !closed {
			b, _ := json.Marshal(map[string]any{"id": ct.t.ID, "reason": reason})
			h.push(0, "end", string(b))
		}
		h.notify()
	}()
	h.notify()
}

func (h *capHost) endSession(id, reason string) {
	if ct := h.track(id); ct != nil {
		ct.t.End(reason, nil)
	}
}

// closeAll runs when the document goes away: every track ends and every pending prompt is cancelled.
func (h *capHost) closeAll() {
	h.mu.Lock()
	h.closed = true
	cancels := h.reqs
	h.reqs = map[int64]context.CancelFunc{}
	var tr []*capTrack
	for _, ct := range h.tracks {
		tr = append(tr, ct)
	}
	if h.watch != nil {
		close(h.watch)
		h.watch = nil
	}
	h.mu.Unlock()
	for _, c := range cancels {
		c()
	}
	h.mu.Lock()
	pv := h.previews
	h.previews = nil
	h.mu.Unlock()
	for _, c := range pv {
		c()
	}
	for _, ct := range tr {
		ct.t.End(capture.EndStopped, nil)
	}
}

// ---- identifiers --------------------------------------------------------------------------------------------

func (h *capHost) salt() string {
	if b := h.tab().broker(); b != nil {
		if s := b.IDSalt(h.tab()); s != "" {
			return s
		}
	}
	return processSalt
}

var processSalt = func() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}()

// hashID is the id a page sees: stable for one origin on one profile, unrelated across origins.
func hashID(salt, origin, kind, id string) string {
	sum := sha256.Sum256([]byte(salt + "\x00" + origin + "\x00" + kind + "\x00" + id))
	return hex.EncodeToString(sum[:])
}

// ---- enumeration --------------------------------------------------------------------------------------------

type devEntry struct {
	Kind     string `json:"kind"`
	DeviceID string `json:"deviceId"`
	GroupID  string `json:"groupId"`
	Label    string `json:"label"`
	real     string
}

// secure reports whether the document may use capture: https, localhost, or an internal page.
func (t *Tab) secureContext() bool {
	u, err := url.Parse(t.URL())
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "https", "sweef", "wss":
		return true
	case "http", "ws":
		h := u.Hostname()
		return h == "localhost" || h == "127.0.0.1" || h == "::1" || strings.HasSuffix(h, ".localhost")
	}
	return false
}

// labelsVisible: labels and ids are withheld until the page has (or had) permission for that kind.
func (h *capHost) labelsVisible(kind string) bool {
	h.mu.Lock()
	for _, ct := range h.tracks {
		if (kind == "videoinput" && (ct.kind == "camera")) || (kind == "audioinput" && ct.kind == "microphone") || (kind == "audiooutput" && ct.kind == "microphone") {
			h.mu.Unlock()
			return true
		}
	}
	h.mu.Unlock()
	switch kind {
	case "videoinput":
		return h.permState(PermCamera) == "granted"
	case "audioinput", "audiooutput":
		return h.permState(PermMicrophone) == "granted"
	}
	return false
}

func (h *capHost) listDevices() []devEntry {
	origin := h.tab().origin()
	salt := h.salt()
	var out []devEntry
	for _, k := range []capture.Kind{capture.VideoInput, capture.AudioInput, capture.AudioOutput} {
		ds := capture.Devices(k)
		if len(ds) == 0 {
			continue
		}
		vis := h.labelsVisible(string(k))
		if !vis {
			// per spec: a single anonymous entry per kind, no id, no label
			out = append(out, devEntry{Kind: string(k)})
			continue
		}
		for _, d := range ds {
			out = append(out, devEntry{Kind: string(k), DeviceID: hashID(salt, origin, string(k), d.ID),
				GroupID: hashID(salt, origin, "group", d.GroupID), Label: d.Label, real: d.ID})
		}
	}
	return out
}

func (h *capHost) enumerate() any {
	b, _ := json.Marshal(h.listDevices())
	return string(b)
}

// setWatch starts or stops the hot-plug poll behind devicechange events: the device lists are compared once a
// second while a page listens.
func (h *capHost) setWatch(on bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	if !on {
		if h.watch != nil {
			close(h.watch)
			h.watch = nil
		}
		return
	}
	if h.watch != nil {
		return
	}
	stop := make(chan struct{})
	h.watch = stop
	sig := func() string {
		var b strings.Builder
		for _, k := range []capture.Kind{capture.VideoInput, capture.AudioInput, capture.AudioOutput} {
			for _, d := range capture.Devices(k) {
				b.WriteString(string(k) + "|" + d.ID + "|" + d.Label + "\n")
			}
		}
		return b.String()
	}
	last := sig() // the baseline is taken now, so a device that appears right after the listener is added is seen
	go func() {
		tk := time.NewTicker(time.Second)
		defer tk.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tk.C:
				if s := sig(); s != last {
					last = s
					h.push(0, "devicechange", "")
				}
			}
		}
	}()
}

// ---- permissions --------------------------------------------------------------------------------------------

func (h *capHost) permState(name string) string {
	t := h.tab()
	if b := t.broker(); b != nil {
		s := b.MediaPermission(t, t.origin(), name)
		if s == "granted" || s == "denied" {
			return s
		}
		return "prompt"
	}
	if synthetic.Enabled() && name != PermDisplay {
		return "granted"
	}
	return "prompt"
}

func (h *capHost) policyAllows(feature string) bool {
	hdr, _ := h.tab().docPolicy.Load().(string)
	return policy.ParseHeader(hdr).Allowed(feature, h.tab().origin())
}

// ---- requests -----------------------------------------------------------------------------------------------

type capErr struct {
	Name       string `json:"name"`
	Message    string `json:"message"`
	Constraint string `json:"constraint,omitempty"`
}

func (e *capErr) Error() string { return e.Name + ": " + e.Message }

func failed(name, msg string) *capErr { return &capErr{Name: name, Message: msg} }

// hostRequest(token, kind, json): starts gum | gdm | apply on a goroutine.
func (h *capHost) hostRequest(a []any) (any, error) {
	tok, kind, payload := argI(a, 0), argS(a, 1), argS(a, 2)
	ctx, cancel := context.WithCancel(context.Background())
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		cancel()
		return nil, nil
	}
	h.reqs[tok] = cancel
	h.mu.Unlock()
	go func() {
		defer func() {
			h.mu.Lock()
			delete(h.reqs, tok)
			h.mu.Unlock()
			cancel()
		}()
		var res any
		var err *capErr
		switch kind {
		case "gum":
			res, err = h.getUserMedia(ctx, payload)
		case "gdm":
			res, err = h.getDisplayMedia(ctx, payload)
		case "apply":
			res, err = h.applyConstraints(payload)
		default:
			err = failed("TypeError", "unknown capture request")
		}
		if ctx.Err() != nil {
			return // the document is gone: nobody is waiting
		}
		if err != nil {
			b, _ := json.Marshal(err)
			h.push(tok, "err", string(b))
			return
		}
		b, _ := json.Marshal(res)
		h.push(tok, "ok", string(b))
	}()
	return nil, nil
}

func (h *capHost) trackJSON(ct *capTrack) map[string]any {
	return map[string]any{"id": ct.t.ID, "kind": map[string]string{"camera": "video", "screen": "video", "microphone": "audio"}[ct.kind],
		"label": ct.label, "settings": ct.settings, "capabilities": ct.caps, "source": ct.kind}
}

type gumRequest struct {
	Audio any `json:"audio"`
	Video any `json:"video"`
}

func wantsMedia(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	}
	return true
}

func parseCons(v any) (constraints.Constraints, *capErr) {
	c, err := constraints.Parse(v)
	if err != nil {
		return c, failed("TypeError", err.Error())
	}
	return c, nil
}

func overconstrained(err error) *capErr {
	var oc *constraints.Overconstrained
	if errors.As(err, &oc) {
		return &capErr{Name: "OverconstrainedError", Message: "", Constraint: oc.Constraint}
	}
	return failed("NotReadableError", err.Error())
}

func toVideoDevices(ds []capture.DeviceInfo, h *capHost, origin, salt string) []constraints.VideoDevice {
	out := make([]constraints.VideoDevice, 0, len(ds))
	for _, d := range ds {
		caps := capture.VideoCapsOf(d.ID)
		vd := constraints.VideoDevice{ID: hashID(salt, origin, "videoinput", d.ID), GroupID: hashID(salt, origin, "group", d.GroupID), Facing: d.Facing}
		for _, m := range caps.Modes {
			vd.Modes = append(vd.Modes, constraints.Mode{W: m.W, H: m.H, FPSMin: m.FPSMin, FPSMax: m.FPSMax})
		}
		out = append(out, vd)
	}
	return out
}

func toAudioDevices(ds []capture.DeviceInfo, origin, salt string) []constraints.AudioDevice {
	out := make([]constraints.AudioDevice, 0, len(ds))
	for _, d := range ds {
		c := capture.AudioCapsOf(d.ID)
		out = append(out, constraints.AudioDevice{ID: hashID(salt, origin, "audioinput", d.ID), GroupID: hashID(salt, origin, "group", d.GroupID),
			Rates: c.Rates, MinChannels: max(c.MinChannels, 1), MaxChannels: max(c.MaxChannels, 1), DefaultRate: c.DefaultRate, DefaultChans: c.DefaultChanns})
	}
	return out
}

func (h *capHost) getUserMedia(ctx context.Context, payload string) (any, *capErr) {
	var req gumRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return nil, failed("TypeError", "invalid constraints")
	}
	wantV, wantA := wantsMedia(req.Video), wantsMedia(req.Audio)
	if !wantV && !wantA {
		return nil, failed("TypeError", "Failed to execute 'getUserMedia' on 'MediaDevices': At least one of audio and video must be requested")
	}
	t := h.tab()
	if !t.secureContext() {
		return nil, failed("SecurityError", "getUserMedia is only available in secure contexts (https, localhost)")
	}
	if wantV && !h.policyAllows(policy.Camera) || wantA && !h.policyAllows(policy.Microphone) {
		return nil, failed("NotAllowedError", "Permission denied by Permissions Policy")
	}
	vc, e := parseCons(req.Video)
	if e != nil {
		return nil, e
	}
	ac, e := parseCons(req.Audio)
	if e != nil {
		return nil, e
	}
	origin, salt := t.origin(), h.salt()
	var vdevs, adevs []capture.DeviceInfo
	if wantV {
		if vdevs = capture.Devices(capture.VideoInput); len(vdevs) == 0 {
			return nil, failed("NotFoundError", "Requested device not found")
		}
	}
	if wantA {
		if adevs = capture.Devices(capture.AudioInput); len(adevs) == 0 {
			return nil, failed("NotFoundError", "Requested device not found")
		}
	}
	var vsol constraints.VideoResult
	var asol constraints.AudioResult
	solve := func(vd, ad []capture.DeviceInfo) *capErr {
		if wantV {
			r, err := constraints.SolveVideo(vc, toVideoDevices(vd, h, origin, salt))
			if err != nil {
				return overconstrained(err)
			}
			vsol = r
		}
		if wantA {
			r, err := constraints.SolveAudio(ac, toAudioDevices(ad, origin, salt))
			if err != nil {
				return overconstrained(err)
			}
			asol = r
		}
		return nil
	}
	if e := solve(vdevs, adevs); e != nil {
		return nil, e
	}
	vpick, apick := capture.DeviceInfo{}, capture.DeviceInfo{}
	if wantV {
		vpick = vdevs[vsol.Device]
	}
	if wantA {
		apick = adevs[asol.Device]
	}

	// permission
	vstate, astate := "granted", "granted"
	if wantV {
		vstate = h.permState(PermCamera)
	}
	if wantA {
		astate = h.permState(PermMicrophone)
	}
	if vstate == "denied" || astate == "denied" {
		return nil, failed("NotAllowedError", "Permission denied")
	}
	if vstate == "prompt" || astate == "prompt" {
		b := t.broker()
		if b == nil {
			return nil, failed("NotAllowedError", "Permission denied")
		}
		// offer every device that could satisfy the request, the solver's pick first
		order := func(ds []capture.DeviceInfo, pick capture.DeviceInfo) []capture.DeviceInfo {
			out := []capture.DeviceInfo{pick}
			for _, d := range ds {
				if d.ID != pick.ID {
					out = append(out, d)
				}
			}
			return out
		}
		mr := MediaRequest{Tab: t, Origin: origin, URL: t.URL(), Video: wantV, Audio: wantA}
		if wantV {
			mr.VideoDevices = order(vdevs, vpick)
		}
		if wantA {
			mr.AudioDevices = order(adevs, apick)
		}
		dec := b.RequestMedia(ctx, mr)
		if ctx.Err() != nil {
			return nil, failed("AbortError", "The request was cancelled")
		}
		if !dec.Allow {
			if dec.Dismissed {
				return nil, failed("NotAllowedError", "Permission dismissed")
			}
			return nil, failed("NotAllowedError", "Permission denied")
		}
		// the user may have picked another device: solve again restricted to it
		pickDev := func(ds []capture.DeviceInfo, id string) []capture.DeviceInfo {
			for _, d := range ds {
				if d.ID == id {
					return []capture.DeviceInfo{d}
				}
			}
			return ds
		}
		if dec.VideoID != "" && dec.VideoID != vpick.ID {
			vdevs = pickDev(vdevs, dec.VideoID)
		}
		if dec.AudioID != "" && dec.AudioID != apick.ID {
			adevs = pickDev(adevs, dec.AudioID)
		}
		if e := solve(vdevs, adevs); e != nil {
			return nil, e
		}
		if wantV {
			vpick = vdevs[vsol.Device]
		}
		if wantA {
			apick = adevs[asol.Device]
		}
	}

	// open
	var tracks []*capTrack
	fail := func(e *capErr) (any, *capErr) {
		for _, ct := range tracks {
			ct.t.End(capture.EndStopped, nil)
		}
		return nil, e
	}
	if wantV {
		r := vsol
		vcons := capture.VideoConstraints{Width: r.W, Height: r.H, FrameRate: r.FPS}
		lease, err := capture.AcquireVideo(vpick.ID, vcons, capture.DefaultStartTimeout)
		if err != nil {
			return fail(deviceErr(err, "video"))
		}
		ct := h.newVideoTrack(lease, vpick, vdevs[r.Device:r.Device+1], origin, salt, vsol)
		tracks = append(tracks, ct)
	}
	if wantA {
		r := asol
		lease, err := capture.AcquireAudio(apick.ID, capture.AudioConstraints{Rate: r.Rate, Channels: r.Channels}, capture.DefaultStartTimeout)
		if err != nil {
			return fail(deviceErr(err, "audio"))
		}
		ct := h.newAudioTrack(lease, apick, origin, salt)
		tracks = append(tracks, ct)
	}
	if ctx.Err() != nil {
		return fail(failed("AbortError", "The request was cancelled"))
	}
	var js []any
	for _, ct := range tracks {
		h.add(ct)
		js = append(js, h.trackJSON(ct))
	}
	return map[string]any{"tracks": js}, nil
}

func deviceErr(err error, what string) *capErr {
	switch {
	case errors.Is(err, capture.ErrInUse):
		return failed("NotReadableError", "Could not start "+what+" source")
	case errors.Is(err, capture.ErrTimeout):
		return failed("NotReadableError", "Timeout starting "+what+" source")
	case errors.Is(err, capture.ErrNotFound):
		return failed("AbortError", "Starting "+what+" input failed")
	}
	return failed("NotReadableError", "Could not start "+what+" source")
}

func rangeJSON(lo, hi float64) map[string]any { return map[string]any{"min": lo, "max": hi} }

func (h *capHost) newVideoTrack(lease *capture.VideoLease, dev capture.DeviceInfo, vd []capture.DeviceInfo, origin, salt string, sol constraints.VideoResult) *capTrack {
	m := lease.Mode()
	if m.W == 0 {
		m = capture.VideoMode{W: sol.W, H: sol.H, FPS: sol.FPS}
	}
	devs := toVideoDevices(vd, h, origin, salt)
	cap := capture.VideoCapsOf(dev.ID)
	minW, maxW, minH, maxH, minF, maxF := cap.Bounds()
	facing := dev.Facing
	if facing == "" {
		facing = "user"
	}
	id, gid := hashID(salt, origin, "videoinput", dev.ID), hashID(salt, origin, "group", dev.GroupID)
	ct := &capTrack{kind: "camera", real: dev.ID, label: dev.Label, vdevs: devs,
		settings: map[string]any{"deviceId": id, "groupId": gid, "width": m.W, "height": m.H, "frameRate": m.FPS,
			"aspectRatio": float64(m.W) / float64(max(m.H, 1)), "facingMode": facing, "resizeMode": "none"},
		caps: map[string]any{"deviceId": id, "groupId": gid, "width": map[string]any{"min": minW, "max": maxW}, "height": map[string]any{"min": minH, "max": maxH},
			"frameRate": rangeJSON(minF, maxF), "aspectRatio": rangeJSON(float64(minW)/float64(max(maxH, 1)), float64(maxW)/float64(max(minH, 1))),
			"facingMode": []string{facing}, "resizeMode": []string{"none"}}}
	ct.t = capture.NewVideoTrack(lease, dev, origin, nil)
	return ct
}

func (h *capHost) newAudioTrack(lease *capture.AudioLease, dev capture.DeviceInfo, origin, salt string) *capTrack {
	m := lease.Mode()
	c := capture.AudioCapsOf(dev.ID)
	id, gid := hashID(salt, origin, "audioinput", dev.ID), hashID(salt, origin, "group", dev.GroupID)
	ct := &capTrack{kind: "microphone", real: dev.ID, label: dev.Label,
		settings: map[string]any{"deviceId": id, "groupId": gid, "sampleRate": m.Rate, "sampleSize": 16, "channelCount": m.Channels,
			"echoCancellation": false, "autoGainControl": false, "noiseSuppression": false, "latency": 0.01},
		caps: map[string]any{"deviceId": id, "groupId": gid, "sampleRate": rangeJSON(float64(m.Rate), float64(m.Rate)), "sampleSize": rangeJSON(16, 16),
			"channelCount": rangeJSON(float64(max(c.MinChannels, 1)), float64(max(c.MaxChannels, 1))), "echoCancellation": []bool{false},
			"autoGainControl": []bool{false}, "noiseSuppression": []bool{false}, "latency": rangeJSON(0.01, 0.01)}}
	ct.t = capture.NewAudioTrack(lease, dev, origin, nil)
	return ct
}

// ---- getDisplayMedia ----------------------------------------------------------------------------------------

type gdmRequest struct {
	Video            any    `json:"video"`
	Audio            any    `json:"audio"`
	PreferCurrentTab bool   `json:"preferCurrentTab"`
	Surface          string `json:"surface"`
}

func (h *capHost) getDisplayMedia(ctx context.Context, payload string) (any, *capErr) {
	var req gdmRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return nil, failed("TypeError", "invalid constraints")
	}
	if b, ok := req.Video.(bool); ok && !b {
		return nil, failed("TypeError", "Failed to execute 'getDisplayMedia' on 'MediaDevices': Audio only requests are not supported")
	}
	t := h.tab()
	if !t.secureContext() {
		return nil, failed("SecurityError", "getDisplayMedia is only available in secure contexts (https, localhost)")
	}
	if !h.policyAllows(policy.DisplayCapture) {
		return nil, failed("NotAllowedError", "Permission denied by Permissions Policy")
	}
	vc, e := parseCons(req.Video)
	if e != nil {
		return nil, e
	}
	b := t.broker()
	if b == nil {
		return nil, failed("NotAllowedError", "Permission denied")
	}
	if b.MediaPermission(t, t.origin(), PermDisplay) == "denied" {
		return nil, failed("NotAllowedError", "Permission denied")
	}
	srcs := b.DisplaySources(t)
	if len(srcs) == 0 {
		return nil, failed("NotFoundError", "Requested device not found")
	}
	dec := b.RequestMedia(ctx, MediaRequest{Tab: t, Origin: t.origin(), URL: t.URL(), Display: true, Sources: srcs,
		PreferCurrentTab: req.PreferCurrentTab, WantAudio: wantsMedia(req.Audio), Surface: req.Surface})
	if ctx.Err() != nil {
		return nil, failed("AbortError", "The request was cancelled")
	}
	if !dec.Allow {
		if dec.Dismissed {
			return nil, failed("NotAllowedError", "Permission dismissed")
		}
		return nil, failed("NotAllowedError", "Permission denied")
	}
	src, info, err := b.OpenDisplay(t, dec.SourceID)
	if err != nil {
		return nil, failed("NotReadableError", "Could not start screen capture: "+err.Error())
	}
	sol, err := constraints.SolveDisplay(vc, info.W, info.H)
	if err != nil {
		return nil, overconstrained(err)
	}
	key := "display:" + hashID(processSalt, dec.SourceID, "n", fmt.Sprint(time.Now().UnixNano()))
	lease, err := capture.AcquireVideoFrom(key, func() (capture.VideoSource, error) { return src, nil },
		capture.VideoConstraints{Width: sol.W, Height: sol.H, FrameRate: sol.FPS}, capture.DefaultStartTimeout)
	if err != nil {
		_ = src.Stop()
		return nil, deviceErr(err, "screen")
	}
	m := lease.Mode()
	if m.W == 0 {
		m = capture.VideoMode{W: sol.W, H: sol.H, FPS: sol.FPS}
	}
	dev := capture.DeviceInfo{ID: key, Label: info.Label, Kind: capture.Screen}
	salt := h.salt()
	id := hashID(salt, t.origin(), "screen", dec.SourceID)
	ct := &capTrack{kind: "screen", real: dec.SourceID, label: info.Label,
		settings: map[string]any{"deviceId": id, "width": m.W, "height": m.H, "frameRate": m.FPS, "aspectRatio": float64(m.W) / float64(max(m.H, 1)),
			"displaySurface": info.Surface, "logicalSurface": true, "cursor": "never", "resizeMode": "none"},
		caps: map[string]any{"deviceId": id, "width": rangeJSON(1, float64(info.W)), "height": rangeJSON(1, float64(info.H)), "frameRate": rangeJSON(1, 60),
			"displaySurface": []string{info.Surface}}}
	ct.t = capture.NewVideoTrack(lease, dev, t.origin(), nil)
	ct.t.Surface = info.Surface
	h.add(ct)
	return map[string]any{"tracks": []any{h.trackJSON(ct)}}, nil
}

// ---- applyConstraints ---------------------------------------------------------------------------------------

type applyRequest struct {
	ID          string `json:"id"`
	Constraints any    `json:"constraints"`
}

// applyConstraints re-solves against the track's own device and switches the camera mode when needed.
func (h *capHost) applyConstraints(payload string) (any, *capErr) {
	var req applyRequest
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		return nil, failed("TypeError", "invalid constraints")
	}
	ct := h.track(req.ID)
	if ct == nil {
		return nil, failed("InvalidStateError", "The track has ended")
	}
	c, e := parseCons(req.Constraints)
	if e != nil {
		return nil, e
	}
	switch ct.kind {
	case "camera":
		sol, err := constraints.SolveVideo(c, ct.vdevs)
		if err != nil {
			return nil, overconstrained(err)
		}
		cur := ct.t.VideoMode()
		if sol.W != cur.W || sol.H != cur.H || (sol.FPS != cur.FPS && sol.FPS > 0) {
			if err := ct.t.Reopen(capture.VideoConstraints{Width: sol.W, Height: sol.H, FrameRate: sol.FPS}); err != nil {
				return nil, deviceErr(err, "video")
			}
			// the shared stream may have been started with another mode: report what is really running
		}
		m := ct.t.VideoMode()
		if m.W == 0 {
			m = capture.VideoMode{W: sol.W, H: sol.H, FPS: sol.FPS}
		}
		ct.settings["width"], ct.settings["height"], ct.settings["frameRate"] = m.W, m.H, m.FPS
		ct.settings["aspectRatio"] = float64(m.W) / float64(max(m.H, 1))
	case "screen":
		w, hgt := int(ct.t.VideoMode().W), int(ct.t.VideoMode().H)
		if _, err := constraints.SolveDisplay(c, w, hgt); err != nil {
			return nil, overconstrained(err)
		}
	case "microphone":
		// only the properties we always report can be satisfied
		if _, err := constraints.SolveAudio(c, []constraints.AudioDevice{{ID: fmt.Sprint(ct.settings["deviceId"]), GroupID: fmt.Sprint(ct.settings["groupId"]),
			Rates: []int{ct.t.AudioMode().Rate}, MinChannels: 1, MaxChannels: ct.t.AudioMode().Channels, DefaultChans: ct.t.AudioMode().Channels}}); err != nil {
			return nil, overconstrained(err)
		}
	}
	return map[string]any{"settings": ct.settings}, nil
}
