// Package omnibox classifies address-bar input and produces ranked
// suggestions from history, bookmarks, open tabs and search templates.
package omnibox

import (
	"net"
	"net/url"
	"strings"
)

// SearchEngine is a URL template; %s is replaced by the query-escaped text.
type SearchEngine struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Keyword  string `json:"keyword"`
	Template string `json:"template"`
}

// Engines is the built-in list. DuckDuckGo is the default.
var Engines = []SearchEngine{
	{"duckduckgo", "DuckDuckGo", "ddg", "https://duckduckgo.com/?q=%s"},
	{"google", "Google", "g", "https://www.google.com/search?q=%s"},
	{"bing", "Bing", "b", "https://www.bing.com/search?q=%s"},
	{"brave", "Brave Search", "br", "https://search.brave.com/search?q=%s"},
	{"kagi", "Kagi", "k", "https://kagi.com/search?q=%s"},
}

// DefaultEngine is DuckDuckGo.
func DefaultEngine() SearchEngine { return Engines[0] }

// EngineByID finds an engine; unknown ids give the default.
func EngineByID(id string) SearchEngine {
	for _, e := range Engines {
		if e.ID == id {
			return e
		}
	}
	return DefaultEngine()
}

// SearchURL builds the results URL for q.
func (e SearchEngine) SearchURL(q string) string {
	return strings.Replace(e.Template, "%s", url.QueryEscape(q), 1)
}

// Kind is what the input means.
type Kind int

const (
	KindEmpty Kind = iota
	KindURL
	KindSearch
	KindAsk      // send to the agent
	KindInternal // sweef:// page
)

func (k Kind) String() string {
	return [...]string{"empty", "url", "search", "ask", "internal"}[k]
}

// Result is a classified input.
type Result struct {
	Kind  Kind
	URL   string // navigation target (KindURL, KindSearch, KindInternal)
	Query string // search text or agent prompt
}

var schemes = map[string]bool{
	"http": true, "https": true, "file": true, "about": true, "sweef": true,
	"view-source": true, "data": true, "blob": true, "mailto": true, "tel": true, "ftp": true,
}

// Classify decides what the user meant. eng is used for searches.
func Classify(input string, eng SearchEngine) Result {
	s := strings.TrimSpace(input)
	if s == "" {
		return Result{}
	}
	// explicit agent prompts: "? text", "ask text", "/ask text", "@agent text"
	if q, ok := askPrefix(s); ok {
		return Result{Kind: KindAsk, Query: q}
	}
	// search keywords: "g cats", "@google cats"
	if q, e, ok := keywordSearch(s); ok {
		return Result{Kind: KindSearch, URL: e.SearchURL(q), Query: q}
	}
	if i := strings.Index(s, ":"); i > 0 && !strings.ContainsAny(s[:i], " /.") {
		sch := strings.ToLower(s[:i])
		rest := s[i+1:]
		switch {
		case sch == "javascript":
			return Result{Kind: KindSearch, URL: eng.SearchURL(s), Query: s}
		case schemes[sch] && (strings.HasPrefix(rest, "//") || sch == "about" || sch == "data" || sch == "mailto" || sch == "tel" || sch == "view-source" || sch == "blob"):
			if sch == "sweef" {
				return Result{Kind: KindInternal, URL: normalizeInternal(s)}
			}
			if sch == "view-source" {
				return Result{Kind: KindURL, URL: "view-source:" + Normalize(strings.TrimPrefix(s[i+1:], "//"))}
			}
			return Result{Kind: KindURL, URL: sch + ":" + rest}
		}
	}
	if !strings.ContainsAny(s, " \t") {
		if u, ok := looksLikeURL(s); ok {
			return Result{Kind: KindURL, URL: u}
		}
	}
	return Result{Kind: KindSearch, URL: eng.SearchURL(s), Query: s}
}

func askPrefix(s string) (string, bool) {
	for _, p := range []string{"? ", "/ask ", "ask: ", "@agent ", "@ask "} {
		if len(s) > len(p) && strings.EqualFold(s[:len(p)], p) {
			return strings.TrimSpace(s[len(p):]), true
		}
	}
	if len(s) > 1 && s[0] == '?' && s[1] != ' ' {
		return strings.TrimSpace(s[1:]), true
	}
	return "", false
}

func keywordSearch(s string) (string, SearchEngine, bool) {
	i := strings.IndexByte(s, ' ')
	if i <= 0 || i == len(s)-1 {
		return "", SearchEngine{}, false
	}
	k := strings.ToLower(s[:i])
	k = strings.TrimPrefix(k, "@")
	for _, e := range Engines {
		if k == e.Keyword || k == e.ID {
			if strings.HasPrefix(s, "@") || len(e.Keyword) <= 3 {
				// bare short keywords ("g cats") only; avoid hijacking "b is for bee"
				return strings.TrimSpace(s[i+1:]), e, true
			}
		}
	}
	return "", SearchEngine{}, false
}

func normalizeInternal(s string) string {
	rest := strings.TrimPrefix(s[len("sweef:"):], "//")
	rest = strings.TrimLeft(rest, "/")
	return "sweef://" + rest
}

// Normalize turns a typed host/URL into a full URL (see looksLikeURL).
func Normalize(s string) string {
	if u, ok := looksLikeURL(strings.TrimSpace(s)); ok {
		return u
	}
	return s
}

// looksLikeURL reports whether s (no spaces) is host[:port][/path] and
// returns the full URL with a scheme.
func looksLikeURL(s string) (string, bool) {
	if strings.HasPrefix(s, "//") {
		s = s[2:]
	}
	hostport := s
	if i := strings.IndexAny(hostport, "/?#"); i >= 0 {
		hostport = hostport[:i]
	}
	rest := s[len(hostport):]
	if at := strings.LastIndexByte(hostport, '@'); at >= 0 { // user:pass@host
		hostport = hostport[at+1:]
	}
	host, port := hostport, ""
	if strings.HasPrefix(host, "[") { // [::1]:8080
		end := strings.IndexByte(host, ']')
		if end < 0 {
			return "", false
		}
		if ip := net.ParseIP(host[1:end]); ip == nil {
			return "", false
		}
		if len(host) > end+1 {
			if host[end+1] != ':' {
				return "", false
			}
			port = host[end+2:]
		}
		host = host[:end+1]
	} else if i := strings.LastIndexByte(host, ':'); i >= 0 {
		host, port = host[:i], host[i+1:]
		if port == "" || !allDigits(port) || len(port) > 5 {
			return "", false
		}
	}
	if host == "" {
		return "", false
	}
	scheme := "https://"
	lower := strings.ToLower(host)
	switch {
	case strings.HasPrefix(host, "["):
		scheme = "http://"
	case net.ParseIP(host) != nil:
		scheme = "http://"
	case lower == "localhost" || strings.HasSuffix(lower, ".localhost") || strings.HasSuffix(lower, ".local") || strings.HasSuffix(lower, ".test") || strings.HasSuffix(lower, ".internal"):
		scheme = "http://"
	case isIPv4Like(host):
		return "", false // 999.1.1.1 and friends are not hosts
	case !strings.Contains(host, "."):
		// single label: only a URL if it carries a port or a path ("intranet/wiki")
		if port == "" && !(strings.HasPrefix(rest, "/") && len(rest) > 1) {
			return "", false
		}
		scheme = "http://"
	default:
		if !validDomain(lower) {
			return "", false
		}
	}
	full := hostport
	if at := strings.LastIndexByte(s, '@'); at >= 0 && at < len(s)-len(rest) {
		full = s[:len(s)-len(rest)]
	}
	return scheme + full + rest, true
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

func isIPv4Like(h string) bool {
	parts := strings.Split(h, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if !allDigits(p) {
			return false
		}
	}
	return net.ParseIP(h) == nil
}

// validDomain checks label syntax and requires an alphabetic TLD of 2+ chars
// (or an xn-- punycode TLD), so "file.txt" style words still look like
// domains only if the "extension" could be a TLD; this is the usual browser
// trade-off.
func validDomain(h string) bool {
	h = strings.TrimSuffix(h, ".")
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, r := range l {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r > 0x7f) {
				return false
			}
		}
	}
	tld := labels[len(labels)-1]
	if strings.HasPrefix(tld, "xn--") {
		return true
	}
	if len(tld) < 2 {
		return false
	}
	for _, r := range tld {
		if !(r >= 'a' && r <= 'z') && r <= 0x7f {
			return false
		}
	}
	return !commonFileExt[tld]
}

// A few extensions that are far more likely a file name than a TLD.
var commonFileExt = map[string]bool{
	"txt": true, "md": true, "go": true, "js": true, "json": true, "png": true, "jpg": true,
	"gif": true, "css": true, "html": true, "htm": true, "pdf": true, "zip": true, "exe": true,
	"py": true, "rs": true, "c": true, "h": true, "cpp": true, "yml": true, "yaml": true, "log": true,
	"sh": true, "doc": true, "docx": true, "xls": true, "csv": true, "mp3": true, "mp4": true,
}
