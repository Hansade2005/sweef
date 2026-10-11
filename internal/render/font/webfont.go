package font

import (
	"bytes"
	"errors"
	"strings"
	"sync"

	"golang.org/x/image/font/sfnt"
)

// Web fonts (@font-face and the CSS Font Loading API) are registered under
// the family name the page declared. They take precedence over system
// fonts of the same name, as in browsers. Registration is process-wide:
// two pages declaring the same family with different files see the most
// recent registration for each (weight, style) pair.

// Decoder turns a font resource (WOFF, WOFF2, ...) into raw sfnt bytes.
// Packages that implement container formats register one at init.
type Decoder func(data []byte) ([]byte, bool)

var (
	webMu      sync.RWMutex
	webFams    = map[string][]*source{}
	decodersMu sync.RWMutex
	decoders   []Decoder
)

// RegisterDecoder adds a font container decoder.
func RegisterDecoder(d Decoder) {
	decodersMu.Lock()
	decoders = append(decoders, d)
	decodersMu.Unlock()
}

// ErrUnsupportedFont is returned for data no decoder understands.
var ErrUnsupportedFont = errors.New("font: unsupported font data")

// RegisterWebFont makes data (TTF/OTF, or any format a registered Decoder
// handles) available as family at the given weight and style. Weight 0
// means 400. It replaces an earlier registration of the same family,
// weight and style.
func RegisterWebFont(family string, weight int, italic bool, data []byte) error {
	raw := data
	if !isSFNT(raw) {
		decodersMu.RLock()
		ds := decoders
		decodersMu.RUnlock()
		ok := false
		for _, d := range ds {
			if out, good := d(data); good {
				raw, ok = out, true
				break
			}
		}
		if !ok {
			return ErrUnsupportedFont
		}
	}
	if _, err := sfnt.Parse(raw); err != nil {
		if _, err2 := sfnt.ParseCollection(raw); err2 != nil {
			return err
		}
	}
	if weight == 0 {
		weight = 400
	}
	key := "webfont:" + strings.ToLower(family)
	embeddedMu.Lock()
	id := key + ":" + itoa(weight) + map[bool]string{true: "i", false: "n"}[italic]
	embedded[id] = raw
	embeddedMu.Unlock()
	s := &source{family: family, file: "embedded:" + id, weight: weight, italic: italic}
	if !s.load() {
		return s.err
	}
	k := strings.ToLower(strings.TrimSpace(family))
	webMu.Lock()
	fam := webFams[k][:0:0]
	for _, o := range webFams[k] {
		if o.weight != weight || o.italic != italic {
			fam = append(fam, o)
		}
	}
	webFams[k] = append(fam, s)
	webMu.Unlock()
	// Fallback answers may change now that a new face exists.
	fallbackCache.Range(func(k, _ any) bool { fallbackCache.Delete(k); return true })
	return nil
}

// HasWebFont reports whether family was registered as a web font.
func HasWebFont(family string) bool {
	return len(webFamily(strings.ToLower(strings.TrimSpace(family)))) > 0
}

func webFamily(lower string) []*source {
	webMu.RLock()
	defer webMu.RUnlock()
	return webFams[lower]
}

func isSFNT(b []byte) bool {
	return len(b) >= 4 && (bytes.Equal(b[:4], []byte{0, 1, 0, 0}) || string(b[:4]) == "OTTO" || string(b[:4]) == "true" || string(b[:4]) == "ttcf")
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
