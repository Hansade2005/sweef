package mediarec

import "strings"

// Spec is a parsed recording MIME type.
type Spec struct {
	Container string // "webm" or "ogg"
	Type      string // "audio/webm", "video/webm", "audio/ogg"
	Audio     bool   // the type names (or defaults to) an Opus track
	Video     bool   // the type names (or defaults to) a VP8 track
}

// ParseMime answers MediaRecorder.isTypeSupported(): ok is true only for what this build can really record
// (WebM with VP8 and/or Opus, Ogg with Opus). An empty string is accepted and means "choose for me".
func ParseMime(s string) (sp Spec, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Spec{}, true
	}
	parts := strings.Split(s, ";")
	typ := strings.ToLower(strings.TrimSpace(parts[0]))
	var codecs []string
	hasCodecs := false
	for _, p := range parts[1:] {
		k, v, found := strings.Cut(p, "=")
		if !found || strings.ToLower(strings.TrimSpace(k)) != "codecs" {
			continue
		}
		hasCodecs = true
		v = strings.Trim(strings.TrimSpace(v), `"'`)
		for _, c := range strings.Split(v, ",") {
			if c = strings.ToLower(strings.TrimSpace(c)); c != "" {
				codecs = append(codecs, c)
			}
		}
	}
	switch typ {
	case "audio/webm", "video/webm":
		sp.Container = "webm"
	case "audio/ogg":
		sp.Container = "ogg"
	default:
		return Spec{}, false
	}
	sp.Type = typ
	for _, c := range codecs {
		switch c {
		case "opus":
			sp.Audio = true
		case "vp8", "vp8.0":
			if typ != "video/webm" {
				return Spec{}, false
			}
			sp.Video = true
		default:
			return Spec{}, false
		}
	}
	if hasCodecs && len(codecs) == 0 {
		return Spec{}, false
	}
	if len(codecs) == 0 { // a bare container: whatever the stream holds, with this build's codecs
		sp.Audio = true
		sp.Video = typ == "video/webm"
	}
	return sp, true
}

// DefaultMime picks the type for a stream with the given kinds of tracks, the way Chromium does.
func DefaultMime(hasAudio, hasVideo bool) string {
	switch {
	case hasVideo && hasAudio:
		return "video/webm;codecs=vp8,opus"
	case hasVideo:
		return "video/webm;codecs=vp8"
	default:
		return "audio/webm;codecs=opus"
	}
}
