package media

import (
	"strings"
)

// ParseMIME splits "type/subtype; codecs=\"a, b\"" into its parts. ok is
// false for malformed types; quoted reports whether codecs was quoted.
func ParseMIME(s string) (typ string, codecs []string, hasCodecs bool, ok bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return
	}
	parts := splitParams(s)
	typ = strings.ToLower(strings.TrimSpace(parts[0]))
	if !strings.Contains(typ, "/") || strings.ContainsAny(typ, " \t") {
		return "", nil, false, false
	}
	ok = true
	for _, p := range parts[1:] {
		p = strings.TrimSpace(p)
		eq := strings.IndexByte(p, '=')
		if eq < 0 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(p[:eq]))
		v := strings.TrimSpace(p[eq+1:])
		if k != "codecs" {
			continue
		}
		hasCodecs = true
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			v = v[1 : len(v)-1]
		} else if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
			v = v[1 : len(v)-1]
		} else {
			// unquoted codecs lists with a single token are accepted
			if strings.ContainsAny(v, ", ") {
				return typ, nil, true, false
			}
		}
		for _, c := range strings.Split(v, ",") {
			c = strings.TrimSpace(c)
			if c != "" {
				codecs = append(codecs, c)
			}
		}
	}
	return
}

func splitParams(s string) []string {
	var out []string
	var cur strings.Builder
	inQ := false
	for _, r := range s {
		switch {
		case r == '"':
			inQ = !inQ
			cur.WriteRune(r)
		case r == ';' && !inQ:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	out = append(out, cur.String())
	return out
}

type codecClass int

const (
	ccUnknown codecClass = iota
	ccFree               // always supported (parse + clock + frames/audio where feasible)
	ccFFmpeg             // proprietary: only with ffmpeg
	ccNo                 // known but unsupported (theora, speex ...)
)

type codecInfo struct {
	kind   string // audio or video
	class  codecClass
	native string // ffprobe-style name of a video codec that may have a pure-Go decoder
}

func classifyCodec(c string) codecInfo {
	lc := strings.ToLower(c)
	switch {
	case lc == "vp8" || lc == "vp8.0":
		return codecInfo{"video", ccFFmpeg, "vp8"}
	case lc == "vp9" || strings.HasPrefix(lc, "vp09."):
		return codecInfo{"video", ccFFmpeg, "vp9"}
	case strings.HasPrefix(lc, "av01."):
		return codecInfo{"video", ccFFmpeg, "av1"}
	case strings.HasPrefix(lc, "avc1") || strings.HasPrefix(lc, "avc3"):
		return codecInfo{"video", ccFFmpeg, "h264"}
	case strings.HasPrefix(lc, "hvc1") || strings.HasPrefix(lc, "hev1"):
		return codecInfo{"video", ccFFmpeg, "hevc"}
	case lc == "theora":
		return codecInfo{"video", ccNo, ""}
	case lc == "vorbis" || lc == "opus" || lc == "flac" || lc == "mp3" || lc == "1":
		return codecInfo{"audio", ccFree, ""}
	case lc == "mp4a.6b" || lc == "mp4a.69":
		return codecInfo{"audio", ccFree, ""}
	case lc == "mp4a.40.2" || lc == "mp4a.40.02":
		return codecInfo{"audio", ccFree, ""} // AAC-LC: pure-Go decoder
	case lc == "mp4a.40.5" || lc == "mp4a.40.05" || lc == "mp4a.40.29":
		return codecInfo{"audio", ccFree, ""} // HE-AAC v1/v2 (SBR, PS): pure-Go decoder
	case lc == "mp4a.40.42":
		return codecInfo{"audio", ccFree, ""} // USAC / xHE-AAC: pure-Go FD core (usac_*.go); LPD, eSBR, MPS212 fall back to ffmpeg
	case strings.HasPrefix(lc, "mp4a.40."):
		return codecInfo{"audio", ccFFmpeg, ""}
	case lc == "speex" || lc == "0":
		return codecInfo{"audio", ccNo, ""}
	}
	return codecInfo{"", ccUnknown, ""}
}

// containerRule describes which codec kinds a MIME type may carry.
type containerRule struct {
	video, audio bool
	def          string // result when no codecs are listed
	implied      string // codec implied by the type itself ("" for none)
	proprietary  bool   // type only available with ffmpeg
	only         map[string]bool
}

var aacOnly = map[string]bool{"mp4a.40.2": true, "mp4a.40.02": true, "mp4a.40.1": true, "mp4a.40.5": true, "mp4a.40.29": true, "mp4a.40.42": true}

var mimeRules = map[string]containerRule{
	"audio/wav":       {audio: true, def: "maybe", only: map[string]bool{"1": true}},
	"audio/x-wav":     {audio: true, def: "maybe", only: map[string]bool{"1": true}},
	"audio/mpeg":      {audio: true, def: "probably", implied: "mp3", only: map[string]bool{"mp3": true}},
	"audio/mp3":       {audio: true, def: "probably", implied: "mp3", only: map[string]bool{"mp3": true}},
	"audio/x-mp3":     {audio: true, def: "probably", implied: "mp3", only: map[string]bool{"mp3": true}},
	"audio/ogg":       {audio: true, def: "maybe", only: map[string]bool{"vorbis": true, "opus": true, "flac": true}},
	"application/ogg": {audio: true, video: true, def: "maybe", only: map[string]bool{"vorbis": true, "opus": true, "flac": true}},
	"video/ogg":       {audio: true, video: true, def: "maybe", only: map[string]bool{"vorbis": true, "opus": true, "flac": true}},
	"audio/webm":      {audio: true, def: "maybe", only: map[string]bool{"vorbis": true, "opus": true}},
	"video/webm":      {audio: true, video: true, def: "maybe"},
	"video/mp4":       {audio: true, video: true, def: "maybe"},
	"audio/mp4":       {audio: true, def: "maybe"},
	"audio/flac":      {audio: true, def: "probably", implied: "flac", only: map[string]bool{"flac": true}},
	"audio/x-m4a":     {audio: true, def: "maybe", only: aacOnly},
	"audio/aac":       {audio: true, def: "maybe", only: aacOnly},
}

// CanPlayType implements HTMLMediaElement.canPlayType truthfully for what
// Sweef can decode or render. The answers match Chromium (checked in
// media_test.go against a recorded matrix) for the royalty-free formats;
// H.264/AAC answer only when ffmpeg is available.
func CanPlayType(mime string) string {
	typ, codecs, hasCodecs, ok := ParseMIME(mime)
	if !ok {
		return ""
	}
	rule, known := mimeRules[typ]
	if !known {
		return ""
	}
	if (typ == "audio/mp3" || typ == "audio/x-mp3") && hasCodecs {
		return ""
	}
	ff := FFmpegPath() != ""
	if rule.proprietary && !ff {
		return ""
	}
	if !hasCodecs || len(codecs) == 0 {
		if hasCodecs {
			return ""
		}
		return rule.def
	}
	maybe := false
	for _, c := range codecs {
		ci := classifyCodec(c)
		if ci.class == ccUnknown || ci.class == ccNo {
			return ""
		}
		if ci.class == ccFFmpeg && !ff {
			switch nativeCodecSupport(strings.ToLower(c), ci.native) {
			case "":
				return ""
			case "maybe":
				maybe = true
			}
		}
		if ci.kind == "video" && !rule.video {
			return ""
		}
		if ci.kind == "audio" && !rule.audio {
			return ""
		}
		lc := strings.ToLower(c)
		if rule.only != nil && !rule.only[lc] {
			return ""
		}
		// container/codec pairings
		switch typ {
		case "video/webm":
			if ci.kind == "audio" && lc != "vorbis" && lc != "opus" {
				return ""
			}
			if ci.kind == "video" && (strings.HasPrefix(lc, "avc") || strings.HasPrefix(lc, "hvc1") || strings.HasPrefix(lc, "hev1")) {
				return ""
			}
		case "video/mp4", "audio/mp4":
			if lc == "vp8" || lc == "vorbis" {
				return ""
			}
		}
	}
	if maybe {
		return "maybe"
	}
	return "probably"
}

// nativeCodecSupport reports how well the pure-Go decoders cover a codecs=
// string whose codec is otherwise ffmpeg-only: "" when there is no native
// decoder, "maybe" when some streams of that profile still need ffmpeg,
// "probably" otherwise.
func nativeCodecSupport(lc, native string) string {
	if native == "" || !HasNativeVideo(native) {
		return ""
	}
	// The Extended profile (avc1.58) is covered, data partitioning included;
	// only SP/SI slices fall back to ffmpeg.
	if strings.HasPrefix(lc, "hvc1") || strings.HasPrefix(lc, "hev1") {
		// hvc1.<profile>.<compat>.<tier+level>...: Main (1), Main 10 (2) and
		// Main Still Picture (3) are covered; Range Extensions streams may use
		// tools the native decoder lacks, and other profiles (SCC, 3D,
		// multilayer) are not decoded natively.
		parts := strings.Split(lc, ".")
		prof := ""
		if len(parts) > 1 {
			prof = strings.TrimLeft(parts[1], "abc")
		}
		switch prof {
		case "1", "2", "3":
			return "probably"
		case "4":
			return "maybe"
		}
		return ""
	}
	return "probably"
}

// IsTypeSupportedMSE is MediaSource.isTypeSupported: only fragmented
// containers the segment parsers handle.
func IsTypeSupportedMSE(mime string) bool {
	typ, _, hasCodecs, ok := ParseMIME(mime)
	if !ok {
		return false
	}
	switch typ {
	case "video/webm", "audio/webm", "video/mp4", "audio/mp4":
	default:
		return false
	}
	if !hasCodecs {
		return false
	}
	return CanPlayType(mime) == "probably"
}

// Playable reports whether a probed resource can be played by an element.
// reason is empty when playable.
func Playable(info *Info) (bool, string) {
	switch info.Container {
	case "mp4", "webm", "matroska", "ogg", "wav", "mp3", "flac":
	case "aac":
		if FFmpegPath() == "" && !nativeAAC(info.Audio()) {
			return false, "AAC needs ffmpeg"
		}
	default:
		return false, "container " + info.Container + " is not supported"
	}
	ff := FFmpegPath() != ""
	for _, t := range info.Tracks {
		switch t.Kind {
		case "video":
			switch t.CodecName {
			case "vp8", "vp9", "av1", "h264", "hevc":
				if !ff && !HasNativeVideo(t.CodecName) {
					return false, t.CodecName + " needs ffmpeg"
				}
			default:
				return false, "video codec " + t.CodecName + " is not supported"
			}
		case "audio":
			switch t.CodecName {
			case "vorbis", "opus", "flac", "mp3":
			case "aac":
				if !ff && !nativeAAC(&t) {
					return false, "AAC needs ffmpeg"
				}
			default:
				if len(t.CodecName) >= 3 && t.CodecName[:3] == "pcm" {
					break
				}
				return false, "audio codec " + t.CodecName + " is not supported"
			}
		}
	}
	if len(info.Tracks) == 0 {
		return false, "no tracks"
	}
	return true, ""
}
