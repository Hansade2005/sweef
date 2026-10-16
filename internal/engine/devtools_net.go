package engine

// devtools_net.go: the network recorder behind the Developer Tools Network and Sources panels.
//
// The tab's netRing (Network.request / Network.response events) is what the agent CLI reads and is
// kept as it is. The recorder here is richer: one entry per request with its type, timing, sizes,
// cache outcome, headers and, for the kinds a person wants to read (documents, scripts, styles and
// text responses), a bounded copy of the body. It adds no network access: bodies are the bytes the
// page's own requests already fetched.

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	devMaxEntries  = 800       // entries kept per tab
	devBodyCap     = 1 << 20   // one stored body, bytes
	devImageCap    = 512 << 10 // images are stored smaller: only to preview them
	devBodyBudget  = 24 << 20  // all stored bodies of a tab while Developer Tools is open
	devIdleBudget  = 4 << 20   // ... and while it is not: the Sources panel still has the page's scripts and styles to show
	devPostBodyCap = 64 << 10
)

// NetEntry is one recorded request. Bodies are not part of the value; see Tab.DevNetBody.
type NetEntry struct {
	ID          int64             `json:"id"`
	Nav         int64             `json:"nav"` // navigation number: it grows with every top-level document request
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	FinalURL    string            `json:"finalUrl,omitempty"`
	Type        string            `json:"type"` // document|script|style|image|font|media|xhr|frame|websocket|other
	Status      int               `json:"status"`
	StartMs     int64             `json:"start"` // unix ms
	DurMs       int64             `json:"dur"`   // -1 while pending
	Transfer    int64             `json:"transfer"`
	BodySize    int64             `json:"size"`
	MIME        string            `json:"mime,omitempty"`
	Cache       string            `json:"cache,omitempty"` // the x-sweef-cache value: hit, revalidated, miss: no-store, ...
	Blocked     bool              `json:"blocked,omitempty"`
	Reason      string            `json:"reason,omitempty"` // why it was blocked
	Error       string            `json:"error,omitempty"`
	Pending     bool              `json:"pending,omitempty"`
	ReqHeaders  map[string]string `json:"reqHeaders,omitempty"`
	RespHeaders map[string]string `json:"respHeaders,omitempty"`
	PostData    string            `json:"postData,omitempty"`
	HasBody     bool              `json:"hasBody,omitempty"`
	Truncated   bool              `json:"truncated,omitempty"`
}

type dtEntry struct {
	NetEntry
	body []byte
}

// dtLog is a tab's recorder. Frames and the hidden tabs of workers share their page's.
type dtLog struct {
	mu      sync.Mutex
	seq     int64
	ver     int64 // changes whenever an entry is added or completes
	nav     int64
	list    []*dtEntry
	bytes   int
	blocked map[string]string // url -> reason, set by the embedder's request filter just before the abort
	// attached: Developer Tools is open for the tab, so bodies of every kind are kept, not only the
	// documents, scripts and styles the Sources panel needs.
	attached bool
}

func newDtLog() *dtLog { return &dtLog{blocked: map[string]string{}} }

// dtRec is a request in flight. A nil *dtRec is a no-op, so call sites need no guard.
type dtRec struct {
	l *dtLog
	e *dtEntry
}

func (l *dtLog) begin(kind DataKind, method, rawurl string, hdrs map[string]string, post string) *dtRec {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if kind == KindDocument {
		l.nav++
		if !l.attached { // a new document: nobody is looking, so the bodies of the earlier ones are not kept
			for _, o := range l.list {
				o.body, o.HasBody = nil, false
			}
			l.bytes = 0
		}
	}
	l.seq++
	l.ver++
	e := &dtEntry{NetEntry: NetEntry{ID: l.seq, Nav: l.nav, Method: method, URL: rawurl, Type: devType(kind, ""),
		StartMs: time.Now().UnixMilli(), DurMs: -1, Pending: true, ReqHeaders: hdrs}}
	if len(post) > devPostBodyCap {
		post = post[:devPostBodyCap]
	}
	e.PostData = post
	l.list = append(l.list, e)
	if len(l.list) > devMaxEntries {
		old := l.list[0]
		l.bytes -= len(old.body)
		l.list = append([]*dtEntry(nil), l.list[1:]...)
	}
	return &dtRec{l: l, e: e}
}

// devType maps the engine's request kind (and, to tell apart what XHR-class requests returned, the MIME type) to the
// type column of the Network panel.
func devType(kind DataKind, mime string) string {
	switch kind {
	case KindDocument:
		return "document"
	case KindFrame:
		return "frame"
	case KindScript:
		return "script"
	case KindStyle:
		return "style"
	case KindImage:
		return "image"
	case KindFont:
		return "font"
	case KindMedia:
		return "media"
	case KindXHR:
		return "xhr"
	}
	m := strings.ToLower(mime)
	switch {
	case strings.HasPrefix(m, "image/"):
		return "image"
	case strings.Contains(m, "javascript"):
		return "script"
	case strings.HasPrefix(m, "text/css"):
		return "style"
	case strings.HasPrefix(m, "font/") || strings.Contains(m, "font-"):
		return "font"
	}
	return "other"
}

func textMIME(m string) bool {
	m = strings.ToLower(m)
	return m == "" || strings.HasPrefix(m, "text/") || strings.Contains(m, "json") || strings.Contains(m, "javascript") ||
		strings.Contains(m, "xml") || strings.Contains(m, "svg") || strings.Contains(m, "x-www-form-urlencoded") || strings.Contains(m, "ecmascript")
}

// done completes the entry with a response.
func (r *dtRec) done(status int, hdr http.Header, cache string, transfer int64, body []byte, finalURL string) {
	if r == nil {
		return
	}
	l := r.l
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ver++
	e := r.e
	e.Pending = false
	e.Status = status
	e.DurMs = time.Now().UnixMilli() - e.StartMs
	e.Cache = cache
	e.Transfer = transfer
	e.BodySize = int64(len(body))
	if finalURL != e.URL {
		e.FinalURL = finalURL
	}
	e.RespHeaders = flattenHeaders(hdr)
	e.MIME = strings.TrimSpace(strings.SplitN(hdr.Get("Content-Type"), ";", 2)[0])
	if t := devType(DataKind(e.Type), e.MIME); e.Type == "other" || e.Type == "xhr" && strings.HasPrefix(e.MIME, "image/") {
		e.Type = t
	}
	if cl := hdr.Get("Content-Length"); transfer == 0 && cl != "" && cache == "" {
		e.Transfer = int64(len(body))
	}
	if len(body) == 0 {
		return
	}
	keep := false
	capN := devBodyCap
	switch e.Type {
	case "document", "script", "style", "frame":
		keep = true
	case "image":
		keep, capN = l.attached, devImageCap
	default:
		keep = l.attached && textMIME(e.MIME)
	}
	if !keep {
		return
	}
	if len(body) > capN {
		body = body[:capN]
		e.Truncated = true
	}
	// a stored body is shared, not copied: the caller does not touch it again
	e.body, e.HasBody = body, true
	l.bytes += len(body)
	l.evictLocked()
}

// fail completes the entry without a response.
func (r *dtRec) fail(err string) {
	if r == nil {
		return
	}
	l := r.l
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ver++
	e := r.e
	e.Pending = false
	e.DurMs = time.Now().UnixMilli() - e.StartMs
	e.Error = err
	if why, ok := l.blocked[e.URL]; ok {
		e.Blocked, e.Reason = true, why
		delete(l.blocked, e.URL)
	} else if strings.Contains(err, errRouteAbort.Error()) {
		e.Blocked, e.Reason = true, "blocked by a request route"
	}
}

// evictLocked drops the oldest bodies until the budget holds.
func (l *dtLog) evictLocked() {
	budget := devBodyBudget
	if !l.attached {
		budget = devIdleBudget
	}
	for i := 0; l.bytes > budget && i < len(l.list); i++ {
		if e := l.list[i]; e.body != nil {
			l.bytes -= len(e.body)
			e.body, e.HasBody = nil, false
		}
	}
}

// DevNoteBlocked tells the recorder that the embedder's request filter (the tracker blocker) is about to refuse a
// request, so the entry that follows is shown as blocked by it.
func (t *Tab) DevNoteBlocked(rawurl, reason string) {
	l := t.dev
	l.mu.Lock()
	if len(l.blocked) > 256 {
		l.blocked = map[string]string{}
	}
	l.blocked[rawurl] = reason
	l.mu.Unlock()
}

// DevAttach marks Developer Tools as open (or closed) for the tab: while open, bodies of text responses and images
// are kept for the Network panel's preview. Closing drops those bodies.
func (t *Tab) DevAttach(on bool) {
	l := t.dev
	l.mu.Lock()
	defer l.mu.Unlock()
	l.attached = on
	if !on {
		for _, e := range l.list {
			if e.body != nil && e.Type != "document" && e.Type != "script" && e.Type != "style" && e.Type != "frame" {
				l.bytes -= len(e.body)
				e.body, e.HasBody = nil, false
			}
		}
	}
}

// DevNetwork returns the recorded requests newer than sinceID (0 = all), oldest first. Bodies are left out.
func (t *Tab) DevNetwork(sinceID int64) []NetEntry {
	l := t.dev
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]NetEntry, 0, len(l.list))
	for _, e := range l.list {
		if e.ID > sinceID {
			out = append(out, e.NetEntry)
		}
	}
	return out
}

// DevNetUpdates returns the entries that changed since the caller last looked: new ones and ones that were pending
// at sinceID. The caller passes the highest ID it has and the IDs it still sees pending.
func (t *Tab) DevNetUpdates(sinceID int64, pending []int64) []NetEntry {
	l := t.dev
	l.mu.Lock()
	defer l.mu.Unlock()
	want := map[int64]bool{}
	for _, id := range pending {
		want[id] = true
	}
	var out []NetEntry
	for _, e := range l.list {
		if e.ID > sinceID || want[e.ID] {
			out = append(out, e.NetEntry)
		}
	}
	return out
}

// DevNetBody returns a stored response body. ok is false when none is stored (never kept, evicted, or the response had no body).
func (t *Tab) DevNetBody(id int64) (body []byte, e NetEntry, ok bool) {
	l := t.dev
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, x := range l.list {
		if x.ID == id {
			return x.body, x.NetEntry, x.body != nil
		}
	}
	return nil, NetEntry{}, false
}

// DevSignals are cheap counters that change when Developer Tools has something new to fetch: console entries, network
// entries (added or completed), document mutations and navigations.
type DevSignals struct {
	Console int64
	Net     int64
	Doc     uint64
	Nav     int64
}

// DevSignals reads the counters.
func (t *Tab) DevSignals() DevSignals {
	l := t.dev
	l.mu.Lock()
	s := DevSignals{Net: l.ver, Nav: l.nav}
	l.mu.Unlock()
	s.Console = t.consoleRing.Seq()
	if d, err := t.devDoc(); err == nil {
		s.Doc = d.MutationGen()
	}
	return s
}

// DevNav is the number of the tab's current navigation as the recorder counts them.
func (t *Tab) DevNav() int64 {
	l := t.dev
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.nav
}

// DevNetEntry returns one recorded request.
func (t *Tab) DevNetEntry(id int64) (NetEntry, bool) {
	l := t.dev
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, x := range l.list {
		if x.ID == id {
			return x.NetEntry, true
		}
	}
	return NetEntry{}, false
}

// DevClearNetwork forgets the recorded requests.
func (t *Tab) DevClearNetwork() {
	l := t.dev
	l.mu.Lock()
	l.list, l.bytes = nil, 0
	l.mu.Unlock()
}

// ---- Sources -----------------------------------------------------------------------------------

// Source is one script or stylesheet of the page, for the Sources panel.
type Source struct {
	Key    string `json:"key"`  // handle for DevSource: "net:<id>" or "inline:<n>"
	URL    string `json:"url"`  // address, or "(inline script)" / "(inline style)"
	Kind   string `json:"kind"` // script | style | document
	Size   int64  `json:"size"`
	Inline bool   `json:"inline,omitempty"`
	Nav    int64  `json:"nav"`
	// Missing: the address is linked by the page but its text was not recorded (it was blocked or failed).
	Missing bool   `json:"missing,omitempty"`
	Status  int    `json:"status,omitempty"`
	Error   string `json:"error,omitempty"`
}

// DevSources lists the scripts and stylesheets of the current document: what was fetched (from the recorder) and
// the inline <script> and <style> elements of its tree.
func (t *Tab) DevSources() []Source {
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	l := t.dev
	l.mu.Lock()
	nav := l.nav
	var out []Source
	seen := map[string]bool{}
	for _, e := range l.list {
		if e.Nav != nav {
			continue
		}
		if e.Type == "document" && e.Status > 0 {
			out = append(out, Source{Key: "net:" + itoa(int(e.ID)), URL: devShowURL(e), Kind: "document", Size: e.BodySize, Nav: e.Nav, Status: e.Status})
			seen[e.URL] = true
			continue
		}
		if e.Type != "script" && e.Type != "style" {
			continue
		}
		if seen[e.URL] && e.Status >= 200 && e.Status < 300 { // the same sheet fetched twice shows once
			continue
		}
		s := Source{Key: "net:" + itoa(int(e.ID)), URL: devShowURL(e), Kind: e.Type, Size: e.BodySize, Nav: e.Nav, Status: e.Status, Error: e.Error}
		if !e.HasBody {
			s.Missing = true
		}
		seen[e.URL] = true
		out = append(out, s)
	}
	l.mu.Unlock()
	if d != nil {
		n := 0
		for _, s := range d.ScriptElements() {
			if s.Src == "" && s.Source != "" {
				n++
				out = append(out, Source{Key: "inline:s" + itoa(n), URL: "(inline script " + itoa(n) + ")", Kind: "script", Size: int64(len(s.Source)), Inline: true, Nav: nav})
			}
		}
		for i, txt := range devInlineStyles(d) {
			out = append(out, Source{Key: "inline:c" + itoa(i+1), URL: "(inline style " + itoa(i+1) + ")", Kind: "style", Size: int64(len(txt)), Inline: true, Nav: nav})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return devSrcRank(out[i]) < devSrcRank(out[j]) })
	return out
}

func devSrcRank(s Source) int {
	switch {
	case s.Kind == "document":
		return 0
	case s.Kind == "script" && !s.Inline:
		return 1
	case s.Kind == "style" && !s.Inline:
		return 2
	case s.Kind == "script":
		return 3
	}
	return 4
}

func devShowURL(e *dtEntry) string {
	if e.FinalURL != "" {
		return e.FinalURL
	}
	return e.URL
}

// DevSource returns the text of a source listed by DevSources.
func (t *Tab) DevSource(key string) (text string, err error) {
	switch {
	case strings.HasPrefix(key, "net:"):
		id := int64(atoiSafe(key[4:]))
		body, _, ok := t.DevNetBody(id)
		if !ok {
			return "", ErrNotFound
		}
		return toValidUTF8(body), nil
	case strings.HasPrefix(key, "inline:s"):
		n := atoiSafe(key[8:])
		t.mu.RLock()
		d := t.dom
		t.mu.RUnlock()
		if d == nil {
			return "", ErrNotFound
		}
		i := 0
		for _, s := range d.ScriptElements() {
			if s.Src == "" && s.Source != "" {
				if i++; i == n {
					return s.Source, nil
				}
			}
		}
	case strings.HasPrefix(key, "inline:c"):
		n := atoiSafe(key[8:])
		t.mu.RLock()
		d := t.dom
		t.mu.RUnlock()
		if d == nil {
			return "", ErrNotFound
		}
		if sh := devInlineStyles(d); n >= 1 && n <= len(sh) {
			return sh[n-1], nil
		}
	}
	return "", ErrNotFound
}

func toValidUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return strings.ToValidUTF8(string(b), "�")
}

// ---- Application ------------------------------------------------------------------------------

// DevCookie is a cookie the page's URL would send, for the Application panel.
type DevCookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Domain   string `json:"domain"`
	Path     string `json:"path"`
	Expires  int64  `json:"expires"`
	HttpOnly bool   `json:"httpOnly"`
	Secure   bool   `json:"secure"`
	SameSite string `json:"sameSite"`
}

// DevCookies lists the cookies of the tab's context that apply to its current URL (the httpOnly ones too: this is
// the person's own browser, and the page cannot see this list).
func (t *Tab) DevCookies() []DevCookie {
	c := t.Context()
	if c == nil {
		return nil
	}
	u, err := url.Parse(t.URL())
	if err != nil || u.Host == "" {
		return nil
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	var out []DevCookie
	for _, ck := range c.JarRef().All("") {
		if ck.Partition != "" {
			continue
		}
		d := strings.TrimPrefix(strings.ToLower(ck.Domain), ".")
		if host != d && !strings.HasSuffix(host, "."+d) {
			continue
		}
		if ck.Path != "" && ck.Path != "/" && !strings.HasPrefix(u.Path+"/", strings.TrimSuffix(ck.Path, "/")+"/") {
			continue
		}
		ss := ""
		switch ck.SameSite {
		case 1:
			ss = "Strict"
		case 2:
			ss = "Lax"
		case 3:
			ss = "None"
		}
		out = append(out, DevCookie{Name: ck.Name, Value: ck.Value, Domain: ck.Domain, Path: ck.Path, Expires: ck.Expires, HttpOnly: ck.HttpOnly, Secure: ck.Secure, SameSite: ss})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
