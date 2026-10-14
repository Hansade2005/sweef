package intl

import (
	"bytes"
	"compress/gzip"
	"embed"
	"io"
	"sync"
)

//go:embed data/*.json.gz
var dataFS embed.FS

var (
	cacheMu sync.Mutex
	cache   = map[string]string{}
)

func load(name string) (string, bool) {
	cacheMu.Lock()
	if s, ok := cache[name]; ok {
		cacheMu.Unlock()
		return s, s != ""
	}
	cacheMu.Unlock()
	raw, err := dataFS.ReadFile("data/" + name + ".json.gz")
	s := ""
	if err == nil {
		if zr, err := gzip.NewReader(bytes.NewReader(raw)); err == nil {
			if b, err := io.ReadAll(zr); err == nil {
				s = string(b)
			}
		}
	}
	cacheMu.Lock()
	cache[name] = s
	cacheMu.Unlock()
	return s, s != ""
}

// Locale returns the JSON data of a locale tag ("en", "en-GB", "zh-Hant"); ok is false for a tag without data.
// Regional locales carry only their differences from the "parent" they name; the JavaScript side merges them.
func Locale(tag string) (string, bool) {
	for _, r := range tag {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return "", false
		}
	}
	return load(tag)
}

// Global returns the locale-independent tables (numbering systems, currency digits, hour cycles, week data, time
// zone names and aliases, metazone history).
func Global() string { s, _ := load("global"); return s }

// Plurals returns the CLDR plural rules and plural ranges.
func Plurals() string { s, _ := load("plurals"); return s }
