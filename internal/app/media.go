package app

// media.go: camera, microphone and screen-share permissions and sources.
//
// The engine solves constraints and talks to devices (internal/engine/capture_host.go); this file is the
// embedding side (engine.MediaBroker): it keeps the per-origin decisions in the profile's site rules, shows the
// prompt through the chrome page, lists what getDisplayMedia can share and tells the chrome which tabs are using
// the camera, the microphone or sharing.
//
// The prompt is state, not an event: while a request waits, `state.media.prompt` describes it for the active tab,
// so switching tabs, reloading the chrome page or opening a popover never loses it. The chrome answers with
// `media.answer`.

import (
	"context"
	"errors"
	"image"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/app/state"
	"github.com/sweefbrowser/sweef/internal/app/store"
	"github.com/sweefbrowser/sweef/internal/capture"
	"github.com/sweefbrowser/sweef/internal/capture/screen"
	"github.com/sweefbrowser/sweef/internal/engine"
)

const windowSourceID = "window:main"

type stateTab = state.Tab

type mediaPrompt struct {
	id   string
	e    *etab
	req  engine.MediaRequest
	resp chan engine.MediaDecision
}

// appMedia is the engine.MediaBroker of an App.
type appMedia struct {
	a       *App
	mu      sync.Mutex
	seq     int
	pending []*mediaPrompt
	acts    map[string]engine.MediaActivity // tab id -> live sessions
}

func newAppMedia(a *App) *appMedia { return &appMedia{a: a, acts: map[string]engine.MediaActivity{}} }

func (m *appMedia) etabOf(t *engine.Tab) *etab {
	m.a.mu.Lock()
	defer m.a.mu.Unlock()
	for _, e := range m.a.pages {
		if e.t == t {
			return e
		}
	}
	return nil
}

// siteName maps a permission name of the Permissions API to the stored rule name.
func siteName(perm string) string {
	if perm == engine.PermDisplay {
		return "screen"
	}
	return perm
}

// stored returns the decision for an origin as the tab sees it: a private tab reads its own (in-memory) rules
// first and then the regular profile's, and never writes to the latter.
func (a *App) storedMedia(incog bool, origin, name string) string {
	if incog {
		if v := a.profileFor(true).Sites.Media(origin, name); v != "" {
			return v
		}
	}
	return a.prof.Sites.Media(origin, name)
}

func (m *appMedia) MediaPermission(t *engine.Tab, origin, name string) string {
	e := m.etabOf(t)
	incog := e != nil && e.incog
	switch m.a.storedMedia(incog, origin, siteName(name)) {
	case "allow":
		return "granted"
	case "block":
		return "denied"
	}
	return "prompt"
}

func (m *appMedia) IDSalt(t *engine.Tab) string {
	e := m.etabOf(t)
	return m.a.profileFor(e != nil && e.incog).Sites.Salt()
}

// RequestMedia parks the request in the pending list and waits for media.answer.
func (m *appMedia) RequestMedia(ctx context.Context, r engine.MediaRequest) engine.MediaDecision {
	e := m.etabOf(r.Tab)
	if e == nil {
		return engine.MediaDecision{}
	}
	m.mu.Lock()
	m.seq++
	p := &mediaPrompt{id: "mp" + itoa(m.seq), e: e, req: r, resp: make(chan engine.MediaDecision, 1)}
	m.pending = append(m.pending, p)
	m.mu.Unlock()
	m.a.post(m.a.markState)
	var d engine.MediaDecision
	select {
	case d = <-p.resp:
	case <-ctx.Done():
		d = engine.MediaDecision{Dismissed: true}
	case <-m.a.quit:
	}
	m.mu.Lock()
	for i, q := range m.pending {
		if q == p {
			m.pending = append(m.pending[:i], m.pending[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
	m.a.post(m.a.markState)
	return d
}

// current returns the prompt to show for a tab (the oldest pending one), or nil.
func (m *appMedia) current(tabID string) *mediaPrompt {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.pending {
		if p.e.id == tabID {
			return p
		}
	}
	return nil
}

func (m *appMedia) byID(id string) *mediaPrompt {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.pending {
		if p.id == id {
			return p
		}
	}
	return nil
}

// answer applies the user's choice. action is "once", "always", "block" or "dismiss".
func (m *appMedia) answer(id, action, video, audio, source string) {
	p := m.byID(id)
	if p == nil {
		return
	}
	a := m.a
	d := engine.MediaDecision{VideoID: video, AudioID: audio, SourceID: source}
	prof := a.profileFor(p.e.incog)
	origin := store.OriginOf(p.req.URL)
	switch action {
	case "once", "always":
		d.Allow = true
		if action == "always" && !p.req.Display && origin != "" {
			if p.req.Video {
				prof.Sites.SetMedia(origin, "camera", "allow")
			}
			if p.req.Audio {
				prof.Sites.SetMedia(origin, "microphone", "allow")
			}
		}
	case "block":
		if origin != "" {
			if p.req.Display {
				prof.Sites.SetMedia(origin, "screen", "block")
			}
			if p.req.Video {
				prof.Sites.SetMedia(origin, "camera", "block")
			}
			if p.req.Audio {
				prof.Sites.SetMedia(origin, "microphone", "block")
			}
		}
	default:
		d.Dismissed = true
	}
	select {
	case p.resp <- d:
	default:
	}
	if action == "always" || action == "block" {
		a.mediaPermissionsChanged(origin)
	}
	a.markState()
}

// mediaPermissionsChanged tells every tab on an origin that a stored permission changed (Permissions API change
// events) and cuts off captures whose permission was just revoked.
func (a *App) mediaPermissionsChanged(origin string) {
	a.mu.Lock()
	var es []*etab
	for _, e := range a.pages {
		es = append(es, e)
	}
	a.mu.Unlock()
	for _, e := range es {
		if store.OriginOf(e.t.URL()) != origin {
			continue
		}
		for _, kind := range [][2]string{{"camera", "camera"}, {"microphone", "microphone"}, {"screen", "screen"}} {
			if a.storedMedia(e.incog, origin, kind[0]) == "block" {
				e.t.EndCaptureKind(kind[1])
			}
		}
		e.t.NotifyMediaPermissionChanged()
	}
}

// ---- display sources ------------------------------------------------------------------------------------------

func (m *appMedia) DisplaySources(t *engine.Tab) []engine.DisplaySource {
	asking := m.etabOf(t)
	var out []engine.DisplaySource
	for _, id := range m.a.model.Order(m.a.winID) {
		st, ok := m.a.model.Tab(id)
		if !ok || st.Discarded || m.a.page(id) == nil {
			continue
		}
		title := st.Title
		if title == "" {
			title = st.URL
		}
		out = append(out, engine.DisplaySource{ID: "tab:" + id, Title: title, URL: st.URL, Surface: "browser", Current: asking != nil && asking.id == id, TabID: id})
	}
	out = append(out, engine.DisplaySource{ID: windowSourceID, Title: "Sweef window", Surface: "window"})
	return out
}

func (m *appMedia) OpenDisplay(t *engine.Tab, id string) (capture.VideoSource, engine.DisplayInfo, error) {
	a := m.a
	if id == windowSourceID {
		w, h, _ := a.comp.Size()
		var buf *image.RGBA
		grab := func(prev int) (*image.RGBA, int, error) {
			if a.closing.Load() {
				return nil, 0, errors.New("the window closed")
			}
			var g int
			buf, g = a.comp.SnapshotWindow(buf)
			if g == prev {
				return nil, g, nil
			}
			return buf, g, nil
		}
		info := engine.DisplayInfo{Label: "Sweef window", Surface: "window", W: w, H: h}
		return screen.New(capture.DeviceInfo{ID: id, Label: info.Label, Kind: capture.Screen}, w, h, grab), info, nil
	}
	tabID := strings.TrimPrefix(id, "tab:")
	e := a.page(tabID)
	st, ok := a.model.Tab(tabID)
	if e == nil || !ok || !strings.HasPrefix(id, "tab:") {
		return nil, engine.DisplayInfo{}, capture.ErrNotFound
	}
	vp := a.comp.Viewport()
	w, h := max(vp.Dx(), 2), max(vp.Dy(), 2)
	var buf *image.RGBA
	grab := func(prev int) (*image.RGBA, int, error) {
		if e.closed.Load() || a.closing.Load() {
			return nil, 0, errors.New("the tab closed")
		}
		if a.model.Active(a.winID) == tabID {
			var g int
			buf, g = a.comp.SnapshotPage(buf)
			if g == prev {
				return nil, g, nil
			}
			return buf, g, nil
		}
		return a.paintBackground(e, prev)
	}
	title := st.Title
	if title == "" {
		title = st.URL
	}
	info := engine.DisplayInfo{Label: title, Surface: "browser", W: w, H: h}
	return screen.New(capture.DeviceInfo{ID: id, Label: title, Kind: capture.Screen}, w, h, grab), info, nil
}

// paintBackground renders a tab that is not on screen. The compositor only holds the active tab's texture, so a
// shared background tab is painted on its own worker (the one place the engine may be touched) whenever its
// document changed.
func (a *App) paintBackground(e *etab, prev int) (*image.RGBA, int, error) {
	type result struct {
		img *image.RGBA
		gen int
	}
	ch := make(chan result, 1)
	ok := e.w.do(func() {
		gen := int(e.t.Generation())
		if gen == prev {
			ch <- result{nil, gen}
			return
		}
		img, ok := e.t.PaintRGBA(e.vpApplied.w, e.vpApplied.h, e.vpApplied.dsf, false)
		if !ok {
			img = nil
		}
		ch <- result{img, gen}
	})
	if !ok {
		return nil, 0, errors.New("the tab closed")
	}
	select {
	case r := <-ch:
		return r.img, r.gen, nil
	case <-time.After(3 * time.Second):
		return nil, prev, nil // a busy tab: keep the last frame
	}
}

// ---- activity ---------------------------------------------------------------------------------------------------

func (m *appMedia) MediaChanged(t *engine.Tab, act engine.MediaActivity) {
	e := m.etabOf(t)
	if e == nil {
		return
	}
	m.mu.Lock()
	if len(act.Sessions) == 0 {
		delete(m.acts, e.id)
	} else {
		m.acts[e.id] = act
	}
	m.mu.Unlock()
	cam, mic, share := act.Count("camera") > 0, act.Count("microphone") > 0, act.Count("screen") > 0
	m.a.post(func() {
		m.a.model.Update(e.id, func(tb *stateTab) { tb.Cam, tb.Mic, tb.Share = cam, mic, share })
		m.a.markState()
	})
}

func (m *appMedia) sessions(tabID string) []engine.MediaSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]engine.MediaSession(nil), m.acts[tabID].Sessions...)
}

// ---- UI state ---------------------------------------------------------------------------------------------------

// mediaState is state.media for the active tab: the pending prompt, the live sessions and the site's stored
// decisions (for the site-info popover).
func (a *App) mediaState(act stateTab, ok bool) map[string]any {
	out := map[string]any{}
	if !ok {
		return out
	}
	m := a.media
	if p := m.current(act.ID); p != nil {
		out["prompt"] = a.promptJSON(p)
	}
	ses := []map[string]any{}
	for _, s := range m.sessions(act.ID) {
		ses = append(ses, map[string]any{"id": s.ID, "kind": s.Kind, "label": s.Label, "surface": s.Surface})
	}
	out["sessions"] = ses
	if origin := store.OriginOf(act.URL); origin != "" && strings.HasPrefix(act.URL, "http") {
		site := map[string]any{"origin": origin}
		for _, n := range []string{"camera", "microphone", "screen"} {
			v := a.storedMedia(act.Incognito, origin, n)
			switch v {
			case "allow", "block":
			default:
				v = "ask"
			}
			site[n] = v
		}
		out["site"] = site
	}
	return out
}

func devJSON(ds []capture.DeviceInfo) []map[string]any {
	out := []map[string]any{}
	for _, d := range ds {
		out = append(out, map[string]any{"id": d.ID, "label": d.Label})
	}
	return out
}

func (a *App) promptJSON(p *mediaPrompt) map[string]any {
	r := p.req
	host := r.Origin
	if u, err := url.Parse(r.Origin); err == nil && u.Host != "" {
		host = u.Host
	}
	j := map[string]any{"id": p.id, "origin": r.Origin, "host": host, "incognito": p.e.incog, "tab": p.e.id}
	if r.Display {
		srcs := []map[string]any{}
		for _, s := range r.Sources {
			srcs = append(srcs, map[string]any{"id": s.ID, "title": s.Title, "url": s.URL, "surface": s.Surface, "current": s.Current,
				"favicon": a.iconFor(s.URL)})
		}
		j["kind"] = "display"
		j["sources"] = srcs
		j["preferCurrentTab"] = r.PreferCurrentTab
		j["wantAudio"] = r.WantAudio
		j["surface"] = r.Surface
		return j
	}
	j["kind"] = "devices"
	j["video"], j["audio"] = r.Video, r.Audio
	j["cameras"], j["mics"] = devJSON(r.VideoDevices), devJSON(r.AudioDevices)
	j["remember"] = !p.e.incog // "Always allow" is not offered in a private window: nothing is written there
	return j
}

// ---- messages ---------------------------------------------------------------------------------------------------

// onMediaMessage handles media.* and site.media; it reports whether typ was one of them.
func (a *App) onMediaMessage(typ string, raw []byte) bool {
	switch typ {
	case "media.answer":
		p := decode[struct {
			ID     string `json:"id"`
			Action string `json:"action"`
			Video  string `json:"video"`
			Audio  string `json:"audio"`
			Source string `json:"source"`
		}](raw)
		a.media.answer(p.ID, p.Action, p.Video, p.Audio, p.Source)
	case "media.stop":
		id := decode[idArg](raw).ID
		a.mu.Lock()
		var es []*etab
		for _, e := range a.pages {
			es = append(es, e)
		}
		a.mu.Unlock()
		for _, e := range es {
			e.t.EndCapture(id)
		}
	case "site.media":
		p := decode[struct {
			Origin string `json:"origin"`
			Name   string `json:"name"`
			Value  string `json:"value"` // allow | block | "" (ask)
		}](raw)
		if p.Name != "camera" && p.Name != "microphone" && p.Name != "screen" {
			return true
		}
		a.profileFor(a.activeIncognito()).Sites.SetMedia(p.Origin, p.Name, p.Value)
		a.mediaPermissionsChanged(p.Origin)
		a.markState()
	default:
		return false
	}
	return true
}

func (a *App) activeIncognito() bool {
	t, ok := a.activeTab()
	return a.cfg.Incognito || (ok && t.Incognito)
}

// MediaOrigins lists the origins with stored capture decisions (settings page, tests).
func (a *App) MediaOrigins() []string {
	o := a.prof.Sites.MediaOrigins()
	sort.Strings(o)
	return o
}
