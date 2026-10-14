package engine

import (
	"encoding/base64"
	"strings"
	"unicode/utf8"
)

// b64host.go: btoa and atob in Go. The page-side versions loop over every character in JavaScript (about 0.7 s
// and 1.4 s per MiB without a JIT), and every binary path of the platform goes through them: WebSocket and fetch
// bodies, WebCrypto, WebCodecs, MSE appends, canvas and file APIs.
//
// Both work on "binary strings": a JavaScript string whose code units are all below 256, one per byte. The VM
// boundary carries strings as UTF-8, so a byte above 0x7F travels as two bytes; these functions convert at the
// edge.

func init() {
	registerHostExtension("b64", func(j *jsVM) map[string]hostFn {
		return map[string]hostFn{
			// b64enc(binaryString) -> base64, or null when a character is above U+00FF
			"b64enc": func(a []any) (any, error) {
				s := argS(a, 0)
				b := make([]byte, 0, len(s))
				for i := 0; i < len(s); {
					c := s[i]
					if c < utf8.RuneSelf {
						b = append(b, c)
						i++
						continue
					}
					r, n := utf8.DecodeRuneInString(s[i:])
					if r > 0xFF || r == utf8.RuneError {
						return nil, nil
					}
					b = append(b, byte(r))
					i += n
				}
				return base64.StdEncoding.EncodeToString(b), nil
			},
			// b64dec(base64) -> binary string, or null when the input is not valid forgiving-base64 (HTML spec)
			"b64dec": func(a []any) (any, error) {
				s := strings.Map(func(r rune) rune {
					switch r {
					case '\t', '\n', '\f', '\r', ' ':
						return -1
					}
					return r
				}, argS(a, 0))
				if len(s)%4 == 0 {
					s = strings.TrimSuffix(strings.TrimSuffix(s, "="), "=")
				}
				if len(s)%4 == 1 || strings.ContainsAny(s, "=") {
					return nil, nil
				}
				b, err := base64.RawStdEncoding.DecodeString(s)
				if err != nil {
					return nil, nil
				}
				var sb strings.Builder
				sb.Grow(len(b) + len(b)/4)
				for _, c := range b {
					sb.WriteRune(rune(c))
				}
				return sb.String(), nil
			},
		}
	})
}
