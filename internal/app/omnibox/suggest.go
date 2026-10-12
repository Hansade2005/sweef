package omnibox

import (
	"net/url"
	"sort"
	"strings"
)

// Item types (the chrome page styles them differently).
const (
	TypeNavigate = "navigate"
	TypeSearch   = "search"
	TypeAsk      = "ask"
	TypeTab      = "tab"
	TypeBookmark = "bookmark"
	TypeHistory  = "history"
	TypeKeyword  = "keyword"
)

// Item is one suggestion row.
type Item struct {
	Type  string  `json:"type"`
	Title string  `json:"title"`
	URL   string  `json:"url,omitempty"`
	Text  string  `json:"text"` // what Enter puts in the omnibox / acts on
	Tab   string  `json:"tab,omitempty"`
	Score float64 `json:"score"`
}

// Response is what Go sends as `omni.suggestions`.
type Response struct {
	ID     any    `json:"id"`
	Items  []Item `json:"items"`
	Inline string `json:"inline,omitempty"` // full text to autocomplete to (input is its prefix)
}

// HistoryHit / BookmarkHit / TabHit are the source row types.
type HistoryHit struct {
	URL, Title string
	Frecency   float64
	Typed      int
}
type BookmarkHit struct{ URL, Title string }
type TabHit struct{ ID, URL, Title string }

// Sources abstracts the data the suggester merges.
type Sources struct {
	History   func(q string, limit int) []HistoryHit
	Bookmarks func(q string, limit int) []BookmarkHit
	Tabs      func() []TabHit
	Engine    func() SearchEngine
}

// Suggest returns ranked suggestions for input.
func Suggest(src Sources, input string, limit int) Response {
	if limit <= 0 {
		limit = 8
	}
	in := strings.TrimSpace(input)
	if in == "" {
		return Response{}
	}
	eng := DefaultEngine()
	if src.Engine != nil {
		eng = src.Engine()
	}
	res := Classify(in, eng)
	var items []Item
	seen := map[string]bool{}
	add := func(it Item) {
		key := it.Type + "|" + it.Text
		if it.URL != "" {
			key = canonical(it.URL)
		}
		if it.Type == TypeTab { // several tabs may show the same URL; each is listed once
			if seen["tab|"+it.Tab] {
				return
			}
			seen["tab|"+it.Tab] = true
			seen[key] = true
			items = append(items, it)
			return
		}
		if seen[key] {
			return
		}
		seen[key] = true
		items = append(items, it)
	}

	// 1. the literal row: what Enter will do
	switch res.Kind {
	case KindURL, KindInternal:
		add(Item{Type: TypeNavigate, Title: in, URL: res.URL, Text: in, Score: 1000})
	case KindAsk:
		add(Item{Type: TypeAsk, Title: "Ask the agent: " + res.Query, Text: in, Score: 1000})
	case KindSearch:
		t := TypeSearch
		title := "Search " + eng.Name + " for " + res.Query
		if res.URL != eng.SearchURL(in) {
			t, title = TypeKeyword, "Search for "+res.Query
		}
		add(Item{Type: t, Title: title, URL: res.URL, Text: in, Score: 1000})
	}

	lower := strings.ToLower(in)
	// 2. open tabs
	if src.Tabs != nil {
		for _, t := range src.Tabs() {
			if matchesAll(lower, t.Title+" "+t.URL) {
				sc := 900.0
				if strings.HasPrefix(strings.ToLower(hostPath(t.URL)), lower) {
					sc += 50
				}
				add(Item{Type: TypeTab, Title: orStr(t.Title, t.URL), URL: t.URL, Text: t.URL, Tab: t.ID, Score: sc})
			}
		}
	}
	// 3. bookmarks
	if src.Bookmarks != nil {
		for i, b := range src.Bookmarks(in, limit) {
			add(Item{Type: TypeBookmark, Title: orStr(b.Title, b.URL), URL: b.URL, Text: b.URL, Score: 800 - float64(i)})
		}
	}
	// 4. history (already ranked by tier+frecency)
	var inline string
	if src.History != nil {
		hits := src.History(in, limit*2)
		for i, h := range hits {
			add(Item{Type: TypeHistory, Title: orStr(h.Title, h.URL), URL: h.URL, Text: h.URL, Score: 700 - float64(i) + typedBoost(h.Typed)})
		}
		inline = inlineCandidate(lower, hits)
	}
	// 5. the ask row for plain-text queries (not explicit asks)
	if res.Kind == KindSearch && len(strings.Fields(in)) >= 2 {
		add(Item{Type: TypeAsk, Title: "Ask the agent: " + in, Text: "? " + in, Score: 100})
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].Score > items[j].Score })
	if len(items) > limit {
		items = items[:limit]
	}
	return Response{Items: items, Inline: inline}
}

func typedBoost(n int) float64 {
	if n > 3 {
		n = 3
	}
	return float64(n) * 0.25
}

func orStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func matchesAll(lowerQ, hay string) bool {
	hay = strings.ToLower(hay)
	for _, t := range strings.Fields(lowerQ) {
		if !strings.Contains(hay, t) {
			return false
		}
	}
	return true
}

func canonical(u string) string {
	u = strings.ToLower(u)
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	u = strings.TrimPrefix(u, "www.")
	return strings.TrimSuffix(u, "/")
}

func hostPath(u string) string {
	pu, err := url.Parse(u)
	if err != nil || pu.Host == "" {
		return u
	}
	return strings.TrimPrefix(pu.Host, "www.") + pu.EscapedPath()
}

// inlineCandidate returns the best history completion for what the user has
// typed so far: a host (or host/path) that begins with the input, so the
// omnibox can show "example.com/" with the tail selected. Only applies to
// single-token input without a scheme; a trailing slash is added for bare
// hosts, as browsers do.
func inlineCandidate(lower string, hits []HistoryHit) string {
	if lower == "" || strings.ContainsAny(lower, " \t") || strings.Contains(lower, "://") {
		return ""
	}
	for _, h := range hits {
		hp := strings.ToLower(hostPath(h.URL))
		if !strings.HasPrefix(hp, lower) || len(hp) == len(lower) {
			continue
		}
		pu, err := url.Parse(h.URL)
		if err != nil {
			continue
		}
		// complete to the host when the input is still inside the host,
		// otherwise to the full host+path
		host := strings.TrimPrefix(strings.ToLower(pu.Host), "www.")
		if len(lower) <= len(host) {
			if len(lower) == len(host) {
				return host + "/"
			}
			return host + "/"
		}
		return hp
	}
	return ""
}
