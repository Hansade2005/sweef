package engine

// jsfonts.go: the page-facing font API: document.fonts (FontFaceSet) and the
// FontFace constructor. The JavaScript half is fonts_prelude.js; this file
// holds the host functions it calls (__sw.fontFace*, __sw.fonts*).

import (
	_ "embed"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"

	"github.com/sweefbrowser/sweef/internal/font"
	"github.com/sweefbrowser/sweef/internal/style"
)

//go:embed fonts_prelude.js.z
var fontsPreludeJSZ string

func init() {
	registerPreludeZ("webfonts", fontsPreludeJSZ)
	registerHostExtension("webfonts", func(j *jsVM) map[string]hostFn { return (&fontHost{j: j}).fns() })
}

const maxScriptFaces = 1024 // FontFace objects a document may create

type fontHost struct{ j *jsVM }

// jsFace is a face known to page script.
type jsFace struct {
	wf    *font.WebFace
	src   string
	props map[string]string
}

func (h *fontHost) web() *webFonts {
	if env := h.j.tab.ds.env.Load(); env != nil {
		return env.fonts
	}
	return nil
}

func (w *webFonts) jsID(f *font.WebFace) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ids == nil {
		w.ids = map[*font.WebFace]int{}
	}
	if id, ok := w.ids[f]; ok {
		return id
	}
	w.jsNext++
	w.js[w.jsNext] = &jsFace{wf: f}
	w.ids[f] = w.jsNext
	return w.jsNext
}

func (w *webFonts) jsFace(id int) *jsFace {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.js[id]
}

func (h *fontHost) fns() map[string]hostFn {
	return map[string]hostFn{
		"fontFaceNew":    h.faceNew,
		"fontFaceInfo":   h.faceInfo,
		"fontFaceUpdate": h.faceUpdate,
		"fontFaceLoad":   h.faceLoad,
		"fontFaceStatus": h.faceStatus,
		"fontsAdd":       h.setAdd,
		"fontsDelete":    h.setDelete,
		"fontsClear":     h.setClear,
		"fontsList":      h.setList,
		"fontsLoad":      h.setLoad,
		"fontsCheck":     h.setCheck,
		"fontsPending":   h.pending,
		"fontsSettle":    h.settle,
	}
}

var cssDescriptorNames = map[string]string{
	"style": "font-style", "weight": "font-weight", "stretch": "font-stretch", "unicodeRange": "unicode-range",
	"display": "font-display", "featureSettings": "font-feature-settings", "variationSettings": "font-variation-settings",
}

func propsFromJS(m map[string]any) map[string]string {
	out := map[string]string{}
	for js, css := range cssDescriptorNames {
		if v, ok := m[js]; ok && v != nil {
			out[css] = argS([]any{v}, 0)
		}
	}
	return out
}

// faceNew(family, src|null, descriptors, base64Data|null) -> {id} | {error}
func (h *fontHost) faceNew(a []any) (any, error) {
	w := h.web()
	if w == nil {
		return map[string]any{"error": "no document"}, nil
	}
	props := map[string]string{}
	if m, ok := normalizeJS(a[2]).(map[string]any); ok {
		props = propsFromJS(m)
	}
	src := ""
	if len(a) > 1 && a[1] != nil {
		src = argS(a, 1)
	}
	w.mu.Lock()
	tooMany := len(w.js) >= maxScriptFaces
	w.mu.Unlock()
	if tooMany {
		return map[string]any{"error": "Too many FontFace objects in this document.", "kind": "src"}, nil
	}
	d, err := style.NewFaceDesc(argS(a, 0), src, props)
	if err != nil {
		return map[string]any{"error": err.Error(), "kind": "descriptor"}, nil
	}
	if src != "" && len(d.Src) == 0 {
		return map[string]any{"error": "The source provided ('" + src + "') could not be parsed as a value list.", "kind": "src"}, nil
	}
	d.Src = resolveFaceSources(d.Src, w.doc.BaseURL())
	wf := font.NewWebFace(d)
	id := w.jsID(wf)
	w.mu.Lock()
	w.js[id].src, w.js[id].props = src, props
	w.mu.Unlock()
	if len(a) > 3 && a[3] != nil {
		raw, err := base64.StdEncoding.DecodeString(argS(a, 3))
		if err != nil {
			wf.SetFailed(err)
		} else if wf.BeginLoad() {
			go func() {
				defer func() {
					if r := recover(); r != nil {
						wf.SetFailed(errors.New("font decoder failure"))
					}
				}()
				sfnt, err := w.decode(raw)
				if err != nil {
					wf.SetFailed(err)
					return
				}
				_ = wf.SetData(sfnt)
			}()
		}
	}
	return map[string]any{"id": id}, nil
}

func descString(d font.FaceDesc) map[string]any {
	weight := strconv.Itoa(d.WMin)
	if d.WMax != d.WMin {
		weight += " " + strconv.Itoa(d.WMax)
	}
	st := "normal"
	switch d.Style {
	case font.StyleItalic:
		st = "italic"
	case font.StyleOblique:
		st = "oblique"
	}
	stretch := "100%"
	if d.SMin != 100 || d.SMax != 100 {
		stretch = strconv.FormatFloat(d.SMin, 'f', -1, 64) + "%"
		if d.SMax != d.SMin {
			stretch += " " + strconv.FormatFloat(d.SMax, 'f', -1, 64) + "%"
		}
	}
	ur := "U+0-10FFFF"
	if len(d.Ranges) > 0 {
		var parts []string
		for _, r := range d.Ranges {
			p := "U+" + strings.ToUpper(strconv.FormatInt(int64(r.Lo), 16))
			if r.Hi != r.Lo {
				p += "-" + strings.ToUpper(strconv.FormatInt(int64(r.Hi), 16))
			}
			parts = append(parts, p)
		}
		ur = strings.Join(parts, ", ")
	}
	disp := d.Display
	if disp == "" {
		disp = "auto"
	}
	feat, vari := d.Features, d.Variations
	if feat == "" {
		feat = "normal"
	}
	if vari == "" {
		vari = "normal"
	}
	return map[string]any{"family": d.Family, "style": st, "weight": weight, "stretch": stretch, "unicodeRange": ur,
		"display": disp, "featureSettings": feat, "variationSettings": vari, "variant": "normal"}
}

func (h *fontHost) face(a []any) *jsFace {
	w := h.web()
	if w == nil {
		return nil
	}
	return w.jsFace(int(argI(a, 0)))
}

func (h *fontHost) faceInfo(a []any) (any, error) {
	jf := h.face(a)
	if jf == nil {
		return nil, nil
	}
	m := descString(jf.wf.Desc)
	m["status"] = jf.wf.State().String()
	return m, nil
}

func (h *fontHost) faceUpdate(a []any) (any, error) {
	jf := h.face(a)
	if jf == nil {
		return "no such face", nil
	}
	m, _ := normalizeJS(a[1]).(map[string]any)
	props := propsFromJS(m)
	for k, v := range jf.props {
		if _, ok := props[k]; !ok {
			props[k] = v
		}
	}
	fam := jf.wf.Desc.Family
	if v, ok := m["family"]; ok {
		fam = argS([]any{v}, 0)
	}
	d, err := style.NewFaceDesc(fam, jf.src, props)
	if err != nil {
		return err.Error(), nil
	}
	d.Src = jf.wf.Desc.Src
	jf.props = props
	jf.wf.Redescribe(d)
	return nil, nil
}

func (h *fontHost) faceLoad(a []any) (any, error) {
	jf := h.face(a)
	if jf == nil {
		return "error", nil
	}
	w := h.web()
	if jf.wf.State() == font.Unloaded {
		if len(jf.wf.Desc.Src) == 0 {
			jf.wf.SetFailed(errors.New("no source"))
		} else {
			jf.wf.Trigger = 0
			w.startFace(jf.wf)
		}
	}
	return jf.wf.State().String(), nil
}

func (h *fontHost) faceStatus(a []any) (any, error) {
	jf := h.face(a)
	if jf == nil {
		return "error", nil
	}
	return jf.wf.State().String(), nil
}

func (h *fontHost) setAdd(a []any) (any, error) {
	jf := h.face(a)
	w := h.web()
	if jf == nil || w == nil {
		return false, nil
	}
	if set := w.ensureSet(); set != nil {
		set.Add(jf.wf)
	}
	return true, nil
}

func (h *fontHost) setDelete(a []any) (any, error) {
	jf := h.face(a)
	w := h.web()
	if jf == nil || w == nil {
		return false, nil
	}
	set := w.webSet()
	return set != nil && set.Delete(jf.wf), nil
}

func (h *fontHost) setClear(a []any) (any, error) {
	if set := h.web().webSet(); set != nil {
		set.Clear()
	}
	return nil, nil
}

func (h *fontHost) setList(a []any) (any, error) {
	w := h.web()
	set := h.synced()
	out := []any{}
	if set == nil {
		return out, nil
	}
	for _, f := range set.Faces() {
		out = append(out, w.jsID(f))
	}
	return out, nil
}

// parseFontShorthand reads the CSS font shorthand the FontFaceSet methods take
// ("italic 700 16px Roboto, sans-serif"): weight, italic and the family list.
func parseFontShorthand(s string) (weight int, italic bool, fams []string, err error) {
	s = strings.TrimSpace(s)
	weight = 400
	var toks []string
	var cur strings.Builder
	var q byte
	flush := func() {
		if cur.Len() > 0 {
			toks = append(toks, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case q != 0:
			cur.WriteByte(c)
			if c == q {
				q = 0
			}
		case c == '"' || c == '\'':
			q = c
			cur.WriteByte(c)
		case c == ' ' || c == '\t' || c == '\n':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	flush()
	isSize := func(t string) bool {
		if i := strings.IndexByte(t, '/'); i > 0 {
			t = t[:i]
		}
		switch strings.ToLower(t) {
		case "xx-small", "x-small", "small", "medium", "large", "x-large", "xx-large", "xxx-large", "larger", "smaller":
			return true
		}
		if t == "" || t[0] < '0' || t[0] > '9' && t[0] != '.' {
			return false
		}
		for _, u := range []string{"px", "pt", "em", "rem", "%", "ex", "ch", "vw", "vh", "pc", "cm", "mm", "in", "q", "vmin", "vmax"} {
			if strings.HasSuffix(strings.ToLower(t), u) {
				return true
			}
		}
		return false
	}
	size := -1
	for i, t := range toks {
		lt := strings.ToLower(t)
		switch {
		case lt == "italic" || lt == "oblique":
			italic = true
		case lt == "normal" || lt == "small-caps" || strings.HasSuffix(lt, "condensed") || strings.HasSuffix(lt, "expanded"):
		case lt == "bold" || lt == "bolder":
			weight = 700
		case lt == "lighter":
			weight = 300
		case isSize(t):
			size = i
		default:
			if n, e := strconv.Atoi(lt); e == nil && n >= 1 && n <= 1000 {
				weight = n
			} else {
				return 0, false, nil, errors.New("Failed to parse font shorthand: " + s)
			}
		}
		if size >= 0 {
			break
		}
	}
	if size < 0 || size == len(toks)-1 {
		return 0, false, nil, errors.New("Failed to parse font shorthand: " + s)
	}
	for _, f := range strings.Split(strings.Join(toks[size+1:], " "), ",") {
		f = strings.TrimSpace(f)
		if f != "" {
			fams = append(fams, strings.Trim(f, `"'`))
		}
	}
	return weight, italic, fams, nil
}

// fontsLoad(font, text) -> {error} | {ids: [face ids]}: demands every face the text needs and lists them.
func (h *fontHost) setLoad(a []any) (any, error) {
	w := h.web()
	weight, italic, fams, err := parseFontShorthand(argS(a, 0))
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	set := h.synced()
	ids := []any{}
	if set != nil {
		for _, fam := range fams {
			for _, f := range set.DemandText(fam, font.Style{Weight: weight, Italic: italic}, argS(a, 1)) {
				ids = append(ids, w.jsID(f))
			}
		}
	}
	return map[string]any{"ids": ids}, nil
}

// fontsCheck(font, text) -> {error} | {ok}: false while a face the text needs is not loaded.
func (h *fontHost) setCheck(a []any) (any, error) {
	w := h.web()
	weight, italic, fams, err := parseFontShorthand(argS(a, 0))
	if err != nil {
		return map[string]any{"error": err.Error()}, nil
	}
	set := h.synced()
	if set == nil {
		return map[string]any{"ok": true}, nil
	}
	_ = w
	text := argS(a, 1)
	if text == "" {
		text = " "
	}
	for _, fam := range fams {
		for _, f := range set.Match(fam, font.Style{Weight: weight, Italic: italic}) {
			covers := false
			for _, r := range text {
				if f.Desc.Covers(r) {
					covers = true
					break
				}
			}
			if covers && (f.State() == font.Unloaded || f.State() == font.Loading) {
				return map[string]any{"ok": false}, nil
			}
		}
	}
	return map[string]any{"ok": true}, nil
}

func (h *fontHost) pending(a []any) (any, error) {
	w := h.web()
	if set := w.webSet(); set != nil {
		return set.Pending(), nil
	}
	return 0, nil
}

// synced returns the document's font set after reading its stylesheets.
func (h *fontHost) synced() *font.WebSet { return h.j.tab.ds.env.Load().syncNow() }

// settle lays the document out (if that has not happened since the last
// change) so that the fonts its text needs have been requested, then reports
// how many are loading: document.fonts.ready is about those. A document
// without web fonts is not laid out at all, and a script that polls the
// property while it mutates the page does not get a layout per access: at
// most maxForcedFontLayouts per document, each only when the DOM changed.
func (h *fontHost) settle(a []any) (any, error) {
	t := h.j.tab
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	w := h.web()
	if d == nil || w == nil {
		return 0, nil
	}
	set := h.synced()
	if set == nil {
		return 0, nil
	}
	gen := d.MutationGen()
	w.mu.Lock()
	force := w.forced < maxForcedFontLayouts && (w.forcedGen != gen || w.forced == 0)
	if force {
		w.forced++
		w.forcedGen = gen
	}
	w.mu.Unlock()
	if force {
		wd, _, _, _, _ := t.Viewport()
		if wd <= 0 {
			wd = 1280
		}
		d.Layout(wd)
	}
	return set.Pending(), nil
}

const maxForcedFontLayouts = 2
