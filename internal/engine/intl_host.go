package engine

import (
	"github.com/sweefbrowser/sweef/internal/intl"
	"github.com/sweefbrowser/sweef/internal/intl/segment"
)

// intl_host.go: the natives behind Intl (platformjs/intl*.js): CLDR locale data, time zone offsets and the Unicode
// segmentation rules. Everything here is read-only and shared by all VMs of the process.

func init() {
	registerHostExtension("intl", func(j *jsVM) map[string]hostFn {
		return map[string]hostFn{
			// intlLocale(tag) -> the JSON of the locale's data (see tools/intlgen), or null
			"intlLocale": func(a []any) (any, error) {
				if s, ok := intl.Locale(argS(a, 0)); ok {
					return s, nil
				}
				return nil, nil
			},
			"intlGlobal":  func(a []any) (any, error) { return intl.Global(), nil },
			"intlPlurals": func(a []any) (any, error) { return intl.Plurals(), nil },
			// intlZoneOffset(name, ms) -> [offset in seconds, daylight saving] or null for an unknown zone
			"intlZoneOffset": func(a []any) (any, error) {
				off, dst, ok := intl.ZoneOffset(argS(a, 0), argI(a, 1))
				if !ok {
					return nil, nil
				}
				return []any{off, dst}, nil
			},
			"intlZoneValid": func(a []any) (any, error) { return intl.ValidZone(argS(a, 0)), nil },
			"intlLocalZone": func(a []any) (any, error) { return intl.LocalZone(), nil },
			// intlSegment(kind, text) -> boundaries as UTF-16 offsets (including 0 and the length)
			"intlSegment": func(a []any) (any, error) {
				rs := []rune(argS(a, 1))
				b := segment.Boundaries(argS(a, 0), rs)
				if len(b) == 0 {
					return []any{}, nil
				}
				// rune index -> UTF-16 offset
				units := make([]int, len(rs)+1)
				for i, r := range rs {
					units[i+1] = units[i] + 1
					if r >= 0x10000 {
						units[i+1]++
					}
				}
				out := make([]any, len(b))
				for i, x := range b {
					out[i] = units[x]
				}
				return out, nil
			},
		}
	})
}
