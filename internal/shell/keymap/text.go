package keymap

import (
	"fmt"
	"math"
	"strings"
	"unicode/utf16"
	"unsafe"
)

// UTF16 converts s to UTF-16 code units without a terminator.
func UTF16(s string) []uint16 { return utf16.Encode([]rune(s)) }

// UTF16Z converts s to a NUL-terminated UTF-16 slice (for Win32 W APIs).
// Embedded NULs are dropped so the string cannot be truncated silently.
func UTF16Z(s string) []uint16 {
	s = strings.ReplaceAll(s, "\x00", "")
	return append(UTF16(s), 0)
}

// FromUTF16 decodes UTF-16 code units (lone surrogates become U+FFFD).
func FromUTF16(u []uint16) string { return string(utf16.Decode(u)) }

// FromUTF16Z decodes a NUL-terminated UTF-16 string at p. max bounds the
// scan in code units (0 = 1<<20).
func FromUTF16Z(p *uint16, max int) string {
	if p == nil {
		return ""
	}
	if max <= 0 {
		max = 1 << 20
	}
	var out []uint16
	for i := 0; i < max; i++ {
		c := *(*uint16)(unsafe.Add(unsafe.Pointer(p), i*2))
		if c == 0 {
			break
		}
		out = append(out, c)
	}
	return FromUTF16(out)
}

// SurrogateJoiner assembles WM_CHAR code units (which deliver astral
// characters as two messages) into runes.
type SurrogateJoiner struct{ hi uint16 }

// Feed returns (rune, true) when a complete character is available.
func (j *SurrogateJoiner) Feed(u uint16) (rune, bool) {
	if u >= 0xD800 && u < 0xDC00 {
		j.hi = u
		return 0, false
	}
	if u >= 0xDC00 && u < 0xE000 {
		if j.hi != 0 {
			r := utf16.DecodeRune(rune(j.hi), rune(u))
			j.hi = 0
			return r, true
		}
		return 0xFFFD, true
	}
	j.hi = 0
	return rune(u), true
}

// BaseDPI is the Windows/X11 reference DPI for a scale of 1.0.
const BaseDPI = 96.0

// ScaleFromDPI converts a DPI value to a device pixel ratio, clamped to a
// sane range. WM_DPICHANGED passes LOWORD(wParam); X11 passes Xft.dpi.
func ScaleFromDPI(dpi float64) float64 {
	if dpi <= 0 || math.IsNaN(dpi) {
		return 1
	}
	return clampScale(dpi / BaseDPI)
}

func clampScale(s float64) float64 {
	if s < 0.5 {
		return 0.5
	}
	if s > 8 {
		return 8
	}
	return s
}

// ToDevice converts logical pixels to device pixels (rounded, >= 1).
func ToDevice(logical int, scale float64) int {
	v := int(math.Round(float64(logical) * scale))
	if v < 1 {
		v = 1
	}
	return v
}

// ToLogical converts device pixels to (fractional) logical pixels.
func ToLogical(device float64, scale float64) float64 {
	if scale <= 0 {
		scale = 1
	}
	return device / scale
}

// LogicalSize returns the logical size for a device-pixel size, rounded to
// the nearest integer (what EvResize reports).
func LogicalSize(dw, dh int, scale float64) (int, int) {
	if scale <= 0 {
		scale = 1
	}
	return int(math.Round(float64(dw) / scale)), int(math.Round(float64(dh) / scale))
}

// ParseXftDPI extracts Xft.dpi from an X RESOURCE_MANAGER string.
func ParseXftDPI(res string) (float64, bool) {
	for _, line := range strings.Split(res, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "Xft.dpi") {
			continue
		}
		i := strings.IndexByte(line, ':')
		if i < 0 {
			continue
		}
		v := strings.TrimSpace(line[i+1:])
		var f float64
		if _, err := fmtSscan(v, &f); err == nil && f > 0 {
			return f, true
		}
	}
	return 0, false
}

// ParseScaleEnv parses GDK_SCALE / SWEEF_SCALE style values ("2", "1.5").
func ParseScaleEnv(v string) (float64, bool) {
	var f float64
	if _, err := fmtSscan(strings.TrimSpace(v), &f); err != nil || f <= 0 {
		return 0, false
	}
	return clampScale(f), true
}

func fmtSscan(s string, f *float64) (int, error) { return fmt.Sscan(s, f) }
