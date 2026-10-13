package engine

// webfonts.go: @font-face loading for one document.
//
// What lives where:
//   - internal/style/fontface.go parses the rules; internal/font/webfont.go
//     holds the document's WebSet, matches faces (CSS Fonts font matching,
//     unicode-range) and decides when a face is needed (font.Demand, called
//     by layout for a character no loaded face has);
//   - this file fetches a needed face (the first usable src, through the
//     counted HTTP client and its cache, with the CORS rule for fonts and the
//     Data Saver gates), decodes WOFF2/WOFF/TrueType/OpenType with the
//     hardened decoders of internal/font, and tells the document to lay out
//     again;
//   - jsfonts.go is the page API (document.fonts, FontFace).
//
// Painting never waits: text is laid out with the next family of the list
// (font-display: swap for every face) and laid out again when a font arrives.
// Headless commands (Layout without a window: shot, snapshot, batch, MCP) do
// wait for the fonts their first layout asked for, up to the settle budget,
// so their output is deterministic.

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sweefbrowser/sweef/internal/font"
	fnet "github.com/sweefbrowser/sweef/internal/net"
	"github.com/sweefbrowser/sweef/internal/raster"
	"github.com/sweefbrowser/sweef/internal/render"
	"github.com/sweefbrowser/sweef/internal/style"
)

// Limits on what a page may make the engine download and decode as fonts.
const (
	fontMaxBytes     = 16 << 20 // one font file, transfer size, any policy
	fontPageMaxBytes = 64 << 20 // all fonts of a page
	fontMaxFaces     = 96       // faces fetched per page
	fontConcurrency  = 6
	fontFetchTimeout = 20 * time.Second
	fontCacheBytes   = 24 << 20 // decoded fonts kept per browser context
	fontRelayoutWait = 40 * time.Millisecond
	maxFontRounds    = 6 // headless layout rounds waiting for fonts
)

// webFonts is the font state of one document (one per tabEnv).
type webFonts struct {
	t   *Tab
	doc *fnet.Doc

	mu      sync.Mutex
	set     *font.WebSet
	parsed  map[string][]font.FaceDesc // sheet text + base -> faces (env independent part parsed once)
	closed  bool
	syncSig string // last syncNow input (see there)

	sem      chan struct{}
	spent    atomic.Int64 // transfer bytes of fonts fetched for this page
	started  atomic.Int64 // faces whose load began
	wake     chan struct{}
	gaveUp   atomic.Bool // the headless wait ran out of budget: do not wait again for this document
	deferred atomic.Bool // a load was refused because the tab is in the background
	timer    atomic.Bool // a relayout is scheduled

	flights  map[string]*fontFlight // raw font files being fetched, by URL
	budgetMu sync.Mutex             // serialises budgeted fetches
	qmu      sync.Mutex             // the in-order queue used with a page budget
	queue    []func()
	qrun     bool

	js     map[int]*jsFace // faces page script knows (jsfonts.go)
	ids    map[*font.WebFace]int
	jsNext int
}

type fontFlight struct {
	done chan struct{}
	raw  []byte
	xfer int64
	err  error
}

func newWebFonts(t *Tab, doc *fnet.Doc) *webFonts {
	return &webFonts{t: t, doc: doc, parsed: map[string][]font.FaceDesc{}, sem: make(chan struct{}, fontConcurrency),
		wake: make(chan struct{}, 1), flights: map[string]*fontFlight{}, js: map[int]*jsFace{}}
}

// webSet is the document's set, nil while the document has declared no web font.
func (w *webFonts) webSet() *font.WebSet {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.set
}

// ensureSet creates the set on first use (pages without web fonts never get one).
func (w *webFonts) ensureSet() *font.WebSet {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	if w.set == nil {
		w.set = font.NewWebSet(w.load, w.changed)
	}
	return w.set
}

func (w *webFonts) close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.closed = true
	s := w.set
	w.flights = map[string]*fontFlight{} // the raw files are not needed any more
	w.mu.Unlock()
	if s != nil {
		s.Close()
	}
}

// hasFontFace is a case-insensitive check for the at-rule, cheap on big sheets.
func hasFontFace(css string) bool {
	for i := 0; i < len(css); {
		j := strings.IndexByte(css[i:], '@')
		if j < 0 {
			return false
		}
		i += j
		if len(css)-i >= 10 && strings.EqualFold(css[i:i+10], "@font-face") {
			return true
		}
		i++
	}
	return false
}

// sync brings the set in line with the @font-face rules of the document's
// stylesheets (those whose media apply) and returns the set to hand to layout,
// nil when the document has no web fonts at all.
func (w *webFonts) sync(sheets []render.Sheet, width, height, dpr float64, scheme string) *font.WebSet {
	if w == nil {
		return nil
	}
	var descs []font.FaceDesc
	env := &style.Env{Width: width, Height: height, DPR: dpr, ColorScheme: scheme}
	for _, sh := range sheets {
		if sh.UserAgent || !hasFontFace(sh.Text) {
			continue
		}
		if sh.Media != "" && !style.MediaMatches(sh.Media, env) {
			continue
		}
		// @font-face rules inside @media blocks depend on the environment: the cache key has it
		key := fmt.Sprintf("%s\x00%g,%g,%g,%s\x00%s", sh.BaseURL, width, height, dpr, scheme, sh.Text)
		w.mu.Lock()
		ds, ok := w.parsed[key]
		w.mu.Unlock()
		if !ok {
			for _, ff := range style.ParseFontFaces(sh.Text, env) {
				d := ff.Desc
				d.Src = resolveFaceSources(d.Src, sh.BaseURL)
				d.Base = sh.BaseURL
				ds = append(ds, d)
			}
			w.mu.Lock()
			if len(w.parsed) > 64 {
				w.parsed = map[string][]font.FaceDesc{}
			}
			w.parsed[key] = ds
			w.mu.Unlock()
		}
		descs = append(descs, ds...)
	}
	w.mu.Lock()
	set := w.set
	w.mu.Unlock()
	if len(descs) == 0 && set == nil {
		return nil
	}
	if set == nil {
		if set = w.ensureSet(); set == nil {
			return nil
		}
	}
	set.SetSheetFaces(descs)
	return set
}

func resolveFaceSources(src []font.Source, base string) []font.Source {
	out := make([]font.Source, 0, len(src))
	for _, s := range src {
		if s.URL != "" && !strings.HasPrefix(s.URL, "data:") {
			if abs, err := resolveURL(base, s.URL); err == nil {
				s.URL = abs
			}
		}
		out = append(out, s)
	}
	return out
}

// ---- change notification -----------------------------------------------------

// changed is the set's onChange: a face loaded or the declarations changed.
func (w *webFonts) changed() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
	w.mu.Lock()
	closed := w.closed
	w.mu.Unlock()
	if closed || w.t.closed.Load() {
		return
	}
	// the document lays out again (a window repaints) shortly after fonts arrive
	if !w.timer.CompareAndSwap(false, true) {
		return
	}
	time.AfterFunc(fontRelayoutWait, func() {
		w.timer.Store(false)
		w.t.mu.RLock()
		d := w.t.dom
		w.t.mu.RUnlock()
		if w.t.closed.Load() || d == nil || d != w.doc {
			return
		}
		d.Touch()
		if f := w.t.onUpdate.Load(); f != nil {
			(*f)()
		}
	})
}

// waitLoaded blocks until no face is loading or the budget is spent; it
// reports whether anything finished meanwhile.
func (w *webFonts) waitLoaded(set *font.WebSet, budget time.Duration) bool {
	if set == nil || set.Pending() == 0 {
		return false
	}
	deadline := time.NewTimer(budget)
	defer deadline.Stop()
	gen := set.Gen()
	for set.Pending() > 0 {
		select {
		case <-w.wake:
		case <-deadline.C:
			w.gaveUp.Store(true)
			return set.Gen() != gen
		}
	}
	return true
}

// ---- loading -------------------------------------------------------------------

var errFontSkipped = errors.New("font skipped by the Data Saver policy")

// load is the WebSet loader: called with a face that just became necessary.
// It starts the work and returns at once.
func (w *webFonts) load(f *font.WebFace) {
	if w.started.Add(1) > fontMaxFaces {
		f.SetFailed(errors.New("too many web fonts on this page"))
		return
	}
	job := func() {
		defer func() {
			if r := recover(); r != nil {
				f.SetFailed(fmt.Errorf("font loader: %v", r))
			}
		}()
		w.run(f)
	}
	if w.t.policy().FontTotalBytes > 0 {
		// a page budget is spent in the order the text asked for the fonts: one at a time
		w.enqueue(job)
		return
	}
	go func() {
		w.sem <- struct{}{}
		defer func() { <-w.sem }()
		job()
	}()
}

// enqueue runs jobs one after the other, in order, on a worker goroutine.
func (w *webFonts) enqueue(job func()) {
	w.qmu.Lock()
	w.queue = append(w.queue, job)
	if w.qrun {
		w.qmu.Unlock()
		return
	}
	w.qrun = true
	w.qmu.Unlock()
	go func() {
		for {
			w.qmu.Lock()
			if len(w.queue) == 0 {
				w.qrun = false
				w.qmu.Unlock()
				return
			}
			j := w.queue[0]
			w.queue = w.queue[1:]
			w.qmu.Unlock()
			j()
		}
	}()
}

// StartFace begins loading a face explicitly (FontFace.load()).
func (w *webFonts) startFace(f *font.WebFace) {
	if f.BeginLoad() {
		w.load(f)
	}
}

func (w *webFonts) run(f *font.WebFace) {
	t := w.t
	if t.closed.Load() {
		f.SetFailed(errors.New("tab closed"))
		return
	}
	d := f.Desc
	pol := t.policy()
	icon := looksLikeIconFace(d, f.Trigger)
	var lastErr error
	cands := w.candidates(d, pol, icon)
	if len(cands) == 0 {
		for _, s := range d.Src {
			if s.URL != "" && !strings.HasPrefix(s.URL, "data:") && pol.FontWOFFOnly {
				k := w.knownSizeOf(s.URL)
				t.noteSkipped(KindFont, s.URL, k, k, "font-format", 0)
				break
			}
		}
	}
	for _, src := range cands {
		sfnt, err := w.fetchSource(f, src, pol, icon)
		if err == nil {
			if err = f.SetData(sfnt); err == nil {
				return
			}
			t.LogConsole("warning", "Failed to decode downloaded font: "+srcLabel(src)+" ("+err.Error()+")")
		}
		lastErr = err
		if errors.Is(err, errBackground) {
			// a background tab makes no page requests: ask again when it comes back
			f.Retry()
			w.deferred.Store(true)
			return
		}
		if errors.Is(err, errFontSkipped) {
			break // the policy applies to every source of the face
		}
	}
	if lastErr == nil {
		lastErr = errors.New("no usable font source")
	}
	f.SetFailed(lastErr)
}

func srcLabel(s font.Source) string {
	if s.URL != "" {
		if strings.HasPrefix(s.URL, "data:") {
			return "data URL"
		}
		return s.URL
	}
	return "local(" + s.Local + ")"
}

// formatOf is the container a source is expected to be: its format() hint, else the file extension.
func formatOf(s font.Source) string {
	f := strings.TrimSuffix(s.Format, "-variations")
	switch f {
	case "woff2", "woff", "truetype", "opentype":
		return f
	case "":
		u := s.URL
		if strings.HasPrefix(u, "data:") {
			switch {
			case strings.Contains(u[:min(len(u), 60)], "woff2"):
				return "woff2"
			case strings.Contains(u[:min(len(u), 60)], "woff"):
				return "woff"
			case strings.Contains(u[:min(len(u), 60)], "ttf"), strings.Contains(u[:min(len(u), 60)], "truetype"):
				return "truetype"
			case strings.Contains(u[:min(len(u), 60)], "otf"), strings.Contains(u[:min(len(u), 60)], "opentype"):
				return "opentype"
			}
			return ""
		}
		if pu, err := url.Parse(u); err == nil {
			switch strings.ToLower(path.Ext(pu.Path)) {
			case ".woff2":
				return "woff2"
			case ".woff":
				return "woff"
			case ".ttf":
				return "truetype"
			case ".otf":
				return "opentype"
			case ".eot":
				return "embedded-opentype"
			case ".svg":
				return "svg"
			}
		}
		return ""
	}
	return f // collection, embedded-opentype, svg, unknown: not supported
}

var unsupportedTech = map[string]bool{
	"color-colrv0": true, "color-colrv1": true, "color-svg": true, "color-sbix": true, "color-cbdt": true,
	"palettes": true, "incremental": true, "features-aat": true, "features-graphite": true,
}

func formatRank(f string) int {
	switch f {
	case "woff2":
		return 0
	case "woff":
		return 1
	case "truetype", "opentype":
		return 2
	}
	return 3
}

// candidates orders the usable sources of a face: local() entries that come
// before the first url(), then the url() sources by format (WOFF2, WOFF,
// TrueType/OpenType; the file the CSS lists first among equals), then the
// remaining local() entries. Formats the engine cannot read, sources with
// unsupported tech() and, under the Data Saver's WOFF-only rule, plain
// TrueType/OpenType files (icon fonts excepted) are left out.
func (w *webFonts) candidates(d font.FaceDesc, pol DataPolicy, icon bool) []font.Source {
	var head, urls, tail []font.Source
	seenURL := false
	for _, s := range d.Src {
		bad := false
		for _, tc := range s.Tech {
			if unsupportedTech[tc] {
				bad = true
			}
		}
		if bad {
			continue
		}
		if s.Local != "" {
			if seenURL {
				tail = append(tail, s)
			} else {
				head = append(head, s)
			}
			continue
		}
		seenURL = true
		fm := formatOf(s)
		switch fm {
		case "woff2", "woff", "truetype", "opentype", "":
		default:
			continue
		}
		if pol.FontWOFFOnly && !icon && (fm == "truetype" || fm == "opentype") {
			continue
		}
		urls = append(urls, s)
	}
	// stable insertion sort by rank
	for i := 1; i < len(urls); i++ {
		for j := i; j > 0 && formatRank(formatOf(urls[j])) < formatRank(formatOf(urls[j-1])); j-- {
			urls[j], urls[j-1] = urls[j-1], urls[j]
		}
	}
	return append(append(head, urls...), tail...)
}

// looksLikeIconFace guesses whether a face draws icons rather than text. The
// Data Saver's strict level keeps these (without them a page shows ligature
// words such as "arrow_back" or empty boxes); see docs/data-saver-engine.md
// for how the rules were measured.
func looksLikeIconFace(d font.FaceDesc, trigger rune) bool {
	if isIconFamilyName(d.Family) {
		return true
	}
	if len(d.Ranges) > 0 {
		all := true
		for _, r := range d.Ranges {
			if !(r.Lo >= 0xe000 && r.Hi <= 0xf8ff) && !(r.Lo >= 0xf0000) {
				all = false
			}
		}
		if all {
			return true
		}
	}
	return isPrivateUse(trigger)
}

func isPrivateUse(r rune) bool {
	return r >= 0xe000 && r <= 0xf8ff || r >= 0xf0000 && r <= 0x10fffd
}

// Names of icon fonts, from a survey of the usual libraries (Material Icons and
// Symbols, Font Awesome, Glyphicons, Ionicons, Bootstrap Icons, Remix, Boxicons,
// Themify, Dashicons, Octicons, Codicons, Feather, Lucide, Tabler, Phosphor,
// IcoMoon/Fontello exports, Linearicons, Typicons, Entypo, Elusive, Genericons...).
// A family matches when it contains one of the words, or is (starts with) one of
// the short tokens.
var iconFamilyWords = []string{
	"icon", "glyph", "symbol", "awesome", "fontello", "icomoon", "dashicons", "codicon", "octicon", "feather",
	"entypo", "typicons", "themify", "boxicons", "remixicon", "ionicons", "elusive", "genericons", "lucide", "tabler",
	"simple-line", "pictogram", "devicon", "zocial", "socicon", "heroicons", "phosphor",
}

var iconFamilyExact = map[string]bool{"fa": true, "fas": true, "far": true, "fab": true, "fal": true, "fad": true, "mdi": true}

var iconFamilyPrefix = []string{"fa-", "fa ", "mdi-", "mdi ", "pe-", "wi-", "ti-"}

func isIconFamilyName(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if iconFamilyExact[n] {
		return true
	}
	for _, p := range iconFamilyPrefix {
		if strings.HasPrefix(n, p) {
			return true
		}
	}
	for _, w := range iconFamilyWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// fetchSource gets, decodes and validates one source of a face.
func (w *webFonts) fetchSource(f *font.WebFace, src font.Source, pol DataPolicy, icon bool) ([]byte, error) {
	if src.Local != "" {
		st := font.Style{Weight: f.Desc.WMin, Italic: f.Desc.Style != font.StyleNormal}
		lf := font.Local(src.Local, st)
		if lf == nil {
			return nil, fmt.Errorf("local(%s) is not installed", src.Local)
		}
		return w.decode(lf.Data())
	}
	var raw []byte
	var err error
	switch {
	case strings.HasPrefix(src.URL, "data:"):
		_, raw, err = decodeDataURL(src.URL)
		if err != nil {
			return nil, err
		}
	case strings.HasPrefix(src.URL, "file:"):
		raw, err = readFileFont(src.URL)
		if err != nil {
			return nil, err
		}
	default:
		raw, err = w.fetchRaw(src.URL, pol, icon)
		if err != nil {
			return nil, err
		}
	}
	return w.decode(raw)
}

func readFileFont(raw string) ([]byte, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	p := u.Path
	if len(p) > 2 && p[0] == '/' && p[2] == ':' { // file:///C:/Windows/...
		p = p[1:]
	}
	fi, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if fi.Size() > fontMaxBytes {
		return nil, errors.New("font file too large")
	}
	return os.ReadFile(p)
}

// fetchRaw downloads a font file: policy gates, the request through the
// engine's HTTP client and cache, the CORS check, accounting.
func (w *webFonts) fetchRaw(abs string, pol DataPolicy, icon bool) ([]byte, error) {
	t := w.t
	key, _ := t.eng.res.key(t.Context(), abs, "")
	known := t.knownSize(key)
	// Data Saver gates
	if pol.BlockFonts && !icon {
		t.noteSkipped(KindFont, abs, known, known, "font", 0)
		return nil, errFontSkipped
	}
	// one request per URL: two faces (a variable font declared for several weights) share it
	w.mu.Lock()
	if fl := w.flights[abs]; fl != nil {
		w.mu.Unlock()
		<-fl.done
		return fl.raw, fl.err
	}
	fl := &fontFlight{done: make(chan struct{})}
	w.flights[abs] = fl
	w.mu.Unlock()
	fl.raw, fl.xfer, fl.err = w.fetchLimited(abs, pol, icon, known)
	if errors.Is(fl.err, errBackground) { // transient: not remembered
		w.mu.Lock()
		delete(w.flights, abs)
		w.mu.Unlock()
	}
	close(fl.done)
	return fl.raw, fl.err
}

// fetchLimited applies the size limits (per font, per page budget) and fetches.
// With a page budget the fetches of fonts run one at a time so that the
// remaining budget is exact when the next one starts.
func (w *webFonts) fetchLimited(abs string, pol DataPolicy, icon bool, known int64) ([]byte, int64, error) {
	t := w.t
	budgeted := pol.FontTotalBytes > 0 && !icon
	if budgeted {
		w.budgetMu.Lock()
		defer w.budgetMu.Unlock()
	}
	maxBody := int64(fontMaxBytes)
	reason := "font-size"
	if pol.FontMaxBytes > 0 {
		if known > int64(pol.FontMaxBytes) {
			t.noteSkipped(KindFont, abs, known, known, "font-size", 0)
			return nil, 0, errFontSkipped
		}
		maxBody = int64(pol.FontMaxBytes)
	}
	if budgeted {
		left := int64(pol.FontTotalBytes) - w.spent.Load()
		if left <= 0 || known > left {
			t.noteSkipped(KindFont, abs, known, known, "font-budget", 0)
			return nil, 0, errFontSkipped
		}
		if left < maxBody {
			maxBody, reason = left, "font-budget"
		}
	}
	if left := int64(fontPageMaxBytes) - w.spent.Load(); left < maxBody {
		maxBody = left
	}
	if maxBody <= 0 {
		return nil, 0, errors.New("font budget of the page is used up")
	}
	return w.fetchNet(abs, pol, maxBody, known, reason)
}

// fontOrigin is the origin used for the CORS comparison.
func fontOrigin(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "null"
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host)
}

// fontCORSAllowed is the rule browsers apply to @font-face: a font from
// another origin is used only when its response says the page's origin may
// read it (Access-Control-Allow-Origin: * or that origin).
func fontCORSAllowed(page, fontURL string, h http.Header) bool {
	pu, err1 := url.Parse(page)
	fu, err2 := url.Parse(fontURL)
	if err1 != nil || err2 != nil {
		return false
	}
	if fu.Scheme == "data" || fu.Scheme == "file" || pu.Scheme == "file" {
		return true
	}
	po, fo := fontOrigin(page), fontOrigin(fontURL)
	if po == fo {
		return true
	}
	acao := strings.TrimSpace(h.Get("Access-Control-Allow-Origin"))
	return acao == "*" || acao == po
}

func (w *webFonts) fetchNet(abs string, pol DataPolicy, maxBody, known int64, reason string) ([]byte, int64, error) {
	t := w.t
	page := t.URL()
	hdr := map[string]string{}
	if fontOrigin(page) != fontOrigin(abs) && !strings.HasPrefix(page, "file:") {
		hdr["Origin"] = fontOrigin(page) // a CORS request: the server decides with Access-Control-Allow-Origin
	}
	t0 := time.Now()
	defer func() { t.tspan("font.fetch", abs, t0) }()
	ctx, cancel := context.WithTimeout(t.eng.ctx, fontFetchTimeout)
	defer cancel()
	type res struct {
		r   *httpResp
		err error
	}
	ch := make(chan res, 1)
	go func() {
		r, err := t.httpDo(httpOpts{method: "GET", url: abs, kind: KindFont, headers: hdr, maxBody: maxBody})
		ch <- res{r, err}
	}()
	var r *httpResp
	select {
	case x := <-ch:
		if x.err != nil {
			return nil, 0, x.err
		}
		r = x.r
	case <-ctx.Done():
		return nil, 0, errors.New("font request timed out")
	}
	if r.aborted {
		t.noteSkipped(KindFont, abs, -2, r.declared, reason, 0)
		return nil, 0, errFontSkipped
	}
	cost := r.xfer
	if r.fromCache {
		cost = r.cachedXfer
	}
	w.spent.Add(cost)
	if r.status < 200 || r.status >= 300 {
		return nil, cost, fmt.Errorf("HTTP %d", r.status)
	}
	if !fontCORSAllowed(page, abs, r.header) {
		t.LogConsole("error", "Access to font at '"+abs+"' from origin '"+fontOrigin(page)+"' has been blocked by CORS policy: No 'Access-Control-Allow-Origin' header is present on the requested resource.")
		return nil, cost, errors.New("blocked by CORS policy")
	}
	if !r.fromCache && r.status == http.StatusOK {
		t.Context().data().observe(KindFont, r.xfer)
	}
	return r.body, cost, nil
}

// ---- decoded font cache (per browser context) -------------------------------------

type decodedFont struct {
	sfnt []byte
	kind font.FontKind
}

type fontStore struct {
	mu    sync.Mutex
	m     map[uint64]*decodedFont
	order []uint64
	bytes int
}

func (c *BrowserContext) fontStore() *fontStore {
	d := c.data()
	if s := d.fonts.Load(); s != nil {
		return s
	}
	s := &fontStore{m: map[uint64]*decodedFont{}}
	if d.fonts.CompareAndSwap(nil, s) {
		return s
	}
	return d.fonts.Load()
}

func hashBytes(b []byte) uint64 {
	h := fnv.New64a()
	h.Write(b)
	return h.Sum64() ^ uint64(len(b))<<48
}

// decode turns downloaded bytes into a validated sfnt, memoised per context
// by content (a font used by many pages or faces is decoded once).
func (w *webFonts) decode(raw []byte) ([]byte, error) {
	st := w.t.Context().fontStore()
	k := hashBytes(raw)
	st.mu.Lock()
	if e := st.m[k]; e != nil {
		st.mu.Unlock()
		return e.sfnt, nil
	}
	st.mu.Unlock()
	t0 := time.Now()
	sfnt, kind, err := font.Decode(raw, font.DefaultLimits)
	w.t.tspan("font.decode", string(kind)+" "+itoa(len(raw))+"B", t0)
	if err != nil {
		return nil, err
	}
	st.mu.Lock()
	if st.m[k] == nil && len(sfnt) <= fontCacheBytes/2 {
		st.m[k] = &decodedFont{sfnt, kind}
		st.order = append(st.order, k)
		st.bytes += len(sfnt)
		for st.bytes > fontCacheBytes && len(st.order) > 1 {
			old := st.order[0]
			st.order = st.order[1:]
			if e := st.m[old]; e != nil {
				st.bytes -= len(e.sfnt)
				delete(st.m, old)
			}
		}
	}
	st.mu.Unlock()
	return sfnt, nil
}

// settle is called by the layout provider of a document that has no window
// after each layout, with the set generation the layout started from: when
// a font arrived during the layout (some text was measured without it) or fonts
// the layout asked for are still loading, it waits for them (within the
// settle budget) and reports true, which makes the provider lay out again.
func (w *webFonts) settle(set *font.WebSet, gen uint64) bool {
	if w == nil || set == nil {
		return false
	}
	if set.Gen() != gen {
		return true
	}
	if w.gaveUp.Load() || set.Pending() == 0 {
		return false
	}
	budget := w.t.settleBudget()
	if budget < 500*time.Millisecond {
		budget = 500 * time.Millisecond
	}
	return w.waitLoaded(set, budget)
}

func (w *webFonts) knownSizeOf(abs string) int64 {
	key, _ := w.t.eng.res.key(w.t.Context(), abs, "")
	return w.t.knownSize(key)
}

// scopeCanvasFont points the canvas context's current font at the document's
// web fonts: families the document declares get their scoped names, and the
// download of the face the first letter would need starts (canvas text drawn
// before the font arrives uses the fallback, as in browsers).
func (h *cvHost) scopeCanvasFont(x *raster.Context) {
	set := h.j.tab.ds.env.Load().syncNow()
	if set == nil {
		return
	}
	x.MapFamilies(func(f string) string {
		if sc, ok := set.Scope(f); ok {
			return sc
		}
		return f
	})
	spec := x.Font()
	st := font.Style{Weight: spec.Weight, Italic: spec.Italic}
	font.Demand(spec.Families, st, 'x')
}

// syncNow brings the font set in line with the document's stylesheets without
// a layout, for code that needs the declarations before any layout ran
// (document.fonts.load/check, canvas text).
func (e *tabEnv) syncNow() *font.WebSet {
	if e == nil || e.fonts == nil {
		return nil
	}
	w, h, dsf, _, _ := e.t.Viewport()
	if w <= 0 {
		w = 1280
	}
	if h <= 0 {
		h = 720
	}
	if dsf <= 0 {
		dsf = 1
	}
	// the declarations only change with the document: skip the sheet walk when nothing did
	sig := fmt.Sprintf("%d %d %d %g %s", e.doc.MutationGen(), w, h, dsf, e.t.ColorSchemeName())
	e.fonts.mu.Lock()
	if e.fonts.syncSig == sig && e.fonts.set != nil {
		set := e.fonts.set
		e.fonts.mu.Unlock()
		return set
	}
	e.fonts.mu.Unlock()
	set := e.fonts.sync(e.pageSheets(), float64(w), float64(h), dsf, e.t.ColorSchemeName())
	e.fonts.mu.Lock()
	e.fonts.syncSig = sig
	e.fonts.mu.Unlock()
	return set
}

// fontCSSUserAgent is the User-Agent for requests to services that choose the
// font format from it. Google Fonts answers an unknown browser with full TrueType
// files (several hundred KB per weight, no unicode-range subsets) and a modern
// Chrome with WOFF2 subsets (tens of KB), which is what a browser that decodes
// WOFF2 should get; so style sheet requests to its CSS API present a Chrome token.
func fontCSSUserAgent(host, ua string) string {
	switch strings.ToLower(host) {
	case "fonts.googleapis.com":
		return "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36 " + ua
	}
	return ua
}

// resume is called when a background tab comes to the front: fonts whose
// load was refused meanwhile are asked for again by the next layout.
func (w *webFonts) resume() {
	if w != nil && w.deferred.Swap(false) {
		w.changed()
	}
}

// FontsPending is the number of web fonts the page's text asked for that are
// still downloading (a window that waits for a settled page, such as a headless
// screenshot, treats the tab as busy while it is above zero).
func (t *Tab) FontsPending() int {
	if env := t.ds.env.Load(); env != nil {
		return env.fonts.webSet().Pending()
	}
	return 0
}
