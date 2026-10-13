package engine

// imgload.go: images that are not in the document. `new Image().src = url`
// (tracking pixels, preloading, canvas sources, sprite sheets) makes a real,
// asynchronous request, honours the Data Saver policy and fires load or error on
// the element. Images in the document are fetched by layout (lazy.go,
// render_glue.go); this path is only for elements the document does not hold.

import (
	"bytes"
	_ "embed"
	"image"
	"strings"
	"sync/atomic"
)

//go:embed imgload_prelude.js
var imgloadPreludeJS string

func init() {
	registerPrelude("imgload", imgloadPreludeJS)
	registerHostExtension("imgload", func(j *jsVM) map[string]hostFn {
		return map[string]hostFn{"imageLoad": j.hostImageLoad}
	})
}

var detachedImgToken atomic.Int64

// hostImageLoad starts the request for a detached image and returns its token;
// the outcome arrives later as an "img" async event (see drainAsync).
func (j *jsVM) hostImageLoad(a []any) (any, error) {
	raw := argS(a, 0)
	token := detachedImgToken.Add(1)
	t := j.tab
	r := j.rt()
	r.imgPending.Add(1)
	go func() {
		w, h, ok := t.loadDetachedImage(raw)
		typ := "load"
		if !ok {
			typ = "error"
		}
		r.push(asyncEvent{kind: "img", id: token, typ: typ, code: w, binary: false, data: itoa(h)})
		r.imgPending.Add(-1)
		t.wake()
	}()
	return token, nil
}

// loadDetachedImage fetches raw (an absolute URL or a data: URL) the way an image request is
// fetched, and returns its natural size. ok is false when the request failed or the policy withheld it.
func (t *Tab) loadDetachedImage(raw string) (w, h int, ok bool) {
	var data []byte
	switch {
	case strings.HasPrefix(raw, "data:"):
		_, b, err := decodeDataURL(raw)
		if err != nil {
			return 0, 0, false
		}
		data = b
	case t.IsOfflineMode():
		return 0, 0, false
	default:
		pol := t.policy()
		if pol.NoImages {
			t.noteSkipped(KindImage, raw, t.knownSizeURL(raw), -1, "strict", 0)
			return 0, 0, false
		}
		if t.suspended() {
			return 0, 0, false
		}
		o := fetchOpts{kind: KindImage}
		if pol.ImageMaxBytes > 0 {
			o.maxBody = int64(pol.ImageMaxBytes)
		}
		body, status, err := t.fetchRes(raw, o)
		if err != nil || status >= 400 {
			return 0, 0, false
		}
		data = []byte(body)
	}
	if len(data) == 0 {
		return 0, 0, true // an empty 200 (a 1x1 beacon answered with 204/200) still counts as delivered
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		if strings.Contains(strings.ToLower(string(data[:min(len(data), 512)])), "<svg") {
			return 300, 150, true
		}
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

// SetWakeHook registers f, called (from any goroutine) when asynchronous results are waiting for the
// page's JavaScript (a detached image finished). A window answers by giving the tab's event loop a slice.
func (t *Tab) SetWakeHook(f func()) {
	if f == nil {
		t.onWake.Store(nil)
		return
	}
	t.onWake.Store(&f)
}

func (t *Tab) wake() {
	if t.closed.Load() {
		return
	}
	if f := t.onWake.Load(); f != nil {
		(*f)()
	}
}
