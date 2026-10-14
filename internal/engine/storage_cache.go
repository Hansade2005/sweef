package engine

// storage_cache.go: Cache Storage host functions (see cache_prelude.js and internal/sitestore/cache.go).

import (
	_ "embed"
	"strings"
	"unicode/utf8"

	"github.com/sweefbrowser/sweef/internal/sitestore"
)

//go:embed cache_prelude.js.z
var cachePreludeJSZ string

func init() {
	registerPreludeZ("storage_cache", cachePreludeJSZ)
	registerHostExtension("storage_cache", func(j *jsVM) map[string]hostFn { return (&storageHost{j: j}).cacheFns() })
}

// bytesToBinStr writes bytes as a string with one character (<= U+00FF) per byte.
func bytesToBinStr(b []byte) string {
	ascii := true
	for _, c := range b {
		if c >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return string(b)
	}
	var sb strings.Builder
	sb.Grow(len(b) * 2)
	for _, c := range b {
		sb.WriteRune(rune(c))
	}
	return sb.String()
}

func binStrToBytes(s string) []byte {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return []byte(s)
	}
	out := make([]byte, 0, len(s))
	for _, r := range s {
		out = append(out, byte(r))
	}
	return out
}

func headerPairs(v any) [][2]string {
	var out [][2]string
	for _, p := range asSlice(v) {
		kv := asSlice(p)
		if len(kv) == 2 {
			k, _ := kv[0].(string)
			val, _ := kv[1].(string)
			out = append(out, [2]string{k, val})
		}
	}
	return out
}

func headerAny(hs [][2]string) []any {
	out := make([]any, 0, len(hs))
	for _, h := range hs {
		out = append(out, []any{h[0], h[1]})
	}
	return out
}

func cacheQueryArg(v any) sitestore.CacheQuery {
	a := asSlice(v)
	q := sitestore.CacheQuery{}
	if len(a) >= 4 {
		q.URL, _ = a[0].(string)
		q.Method, _ = a[1].(string)
		q.IgnoreSearch, _ = a[2].(bool)
		q.IgnoreMethod, _ = a[3].(bool)
	}
	return q
}

func (h *storageHost) caches() (*sitestore.CacheStore, error) {
	o, err := h.site()
	if err != nil {
		return nil, err
	}
	return o.Caches()
}

func (h *storageHost) cacheFns() map[string]hostFn {
	return map[string]hostFn{
		// text <-> binary string conversions done natively (the prelude's are per-character JS)
		"utf8ToBin": func(a []any) (any, error) { return bytesToBinStr([]byte(argS(a, 0))), nil },
		"binToUtf8": func(a []any) (any, error) {
			return strings.ToValidUTF8(string(binStrToBytes(argS(a, 0))), "\uFFFD"), nil
		},
		"cachesOpen": func(a []any) (any, error) {
			c, err := h.caches()
			if err != nil {
				return nil, err
			}
			return c.Open(argS(a, 0))
		},
		"cachesHas": func(a []any) (any, error) {
			c, err := h.caches()
			if err != nil {
				return nil, err
			}
			return c.Has(argS(a, 0)), nil
		},
		"cachesDelete": func(a []any) (any, error) {
			c, err := h.caches()
			if err != nil {
				return nil, err
			}
			return c.Delete(argS(a, 0)), nil
		},
		"cachesKeys": func(a []any) (any, error) {
			c, err := h.caches()
			if err != nil {
				return nil, err
			}
			out := []any{}
			for _, n := range c.Keys() {
				out = append(out, n)
			}
			return out, nil
		},
		"cachePut": func(a []any) (any, error) {
			c, err := h.caches()
			if err != nil {
				return nil, err
			}
			m, _ := normalizeJS(argAt(a, 1)).(map[string]any)
			e := sitestore.CacheEntry{
				URL: str(m["url"]), Method: str(m["method"]), ReqHeaders: headerPairs(m["reqHeaders"]),
				Status: int(argI([]any{m["status"]}, 0)), StatusText: str(m["statusText"]),
				ResHeaders: headerPairs(m["resHeaders"]), Type: str(m["type"]), ResURL: str(m["resURL"]),
			}
			return nil, c.Put(argS(a, 0), e, binStrToBytes(argS(a, 2)))
		},
		"cacheMatch": func(a []any) (any, error) {
			c, err := h.caches()
			if err != nil {
				return nil, err
			}
			ms := c.Match(argS(a, 0), argB(a, 3), cacheQueryArg(argAt(a, 1)), argB(a, 2))
			out := make([]any, 0, len(ms))
			for _, m := range ms {
				e := m.Entry
				out = append(out, map[string]any{
					"url": e.URL, "method": e.Method, "reqHeaders": headerAny(e.ReqHeaders), "status": e.Status,
					"statusText": e.StatusText, "resHeaders": headerAny(e.ResHeaders), "type": e.Type, "resURL": e.ResURL,
					"body": bytesToBinStr(m.Body),
				})
			}
			return out, nil
		},
		"cacheDelete": func(a []any) (any, error) {
			c, err := h.caches()
			if err != nil {
				return nil, err
			}
			return c.DeleteEntries(argS(a, 0), cacheQueryArg(argAt(a, 1)))
		},
		"cacheKeys": func(a []any) (any, error) {
			c, err := h.caches()
			if err != nil {
				return nil, err
			}
			es, err := c.Requests(argS(a, 0), cacheQueryArg(argAt(a, 1)))
			if err != nil {
				return nil, err
			}
			out := make([]any, 0, len(es))
			for _, e := range es {
				out = append(out, map[string]any{"url": e.URL, "method": e.Method, "reqHeaders": headerAny(e.ReqHeaders)})
			}
			return out, nil
		},
	}
}

var _ = utf8.RuneError
