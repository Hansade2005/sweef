package media

import (
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
)

// WebM / Matroska parsing.

const (
	idEBML        = 0x1A45DFA3
	idDocType     = 0x4282
	idSegment     = 0x18538067
	idSeekHead    = 0x114D9B74
	idInfo        = 0x1549A966
	idTimecodeScl = 0x2AD7B1
	idDuration    = 0x4489
	idTitle       = 0x7BA9
	idTracks      = 0x1654AE6B
	idTrackEntry  = 0xAE
	idTrackNumber = 0xD7
	idTrackType   = 0x83
	idCodecID     = 0x86
	idCodecPriv   = 0x63A2
	idDefaultDur  = 0x23E383
	idLanguage    = 0x22B59C
	idTrackName   = 0x536E
	idFlagDefault = 0x88
	idVideo       = 0xE0
	idPixelW      = 0xB0
	idPixelH      = 0xBA
	idAudio       = 0xE1
	idSampFreq    = 0xB5
	idChannels    = 0x9F
	idBitDepth    = 0x6264
	idCues        = 0x1C53BB6B
	idCuePoint    = 0xBB
	idCueTime     = 0xB3
	idCueTrackPos = 0xB7
	idCueTrack    = 0xF7
	idCueCluster  = 0xF1
	idCluster     = 0x1F43B675
	idClusterTC   = 0xE7
	idSimpleBlock = 0xA3
	idBlockGroup  = 0xA0
	idBlock       = 0xA1
	idBlockDur    = 0x9B
)

type ebmlEl struct {
	id      uint32
	hdrLen  int
	size    int64 // -1 unknown
	dataOff int64 // absolute for reader, relative for buffer
}

func readVint(b []byte, keepMarker bool) (val uint64, n int, unknown bool) {
	if len(b) == 0 {
		return 0, 0, false
	}
	first := b[0]
	if first == 0 {
		return 0, 0, false
	}
	n = 1
	mask := byte(0x80)
	for first&mask == 0 {
		n++
		mask >>= 1
	}
	if len(b) < n {
		return 0, 0, false
	}
	v := uint64(first)
	if !keepMarker {
		v = uint64(first & (mask - 1))
	}
	allOnes := first&(mask-1) == mask-1
	for i := 1; i < n; i++ {
		v = v<<8 | uint64(b[i])
		if b[i] != 0xff {
			allOnes = false
		}
	}
	if !keepMarker && allOnes {
		unknown = true
	}
	return v, n, unknown
}

func parseElHeader(b []byte) (id uint32, idLen int, size int64, szLen int, ok bool) {
	v, n, _ := readVint(b, true)
	if n == 0 || n > 4 {
		return
	}
	id = uint32(v)
	s, m, unk := readVint(b[n:], false)
	if m == 0 {
		return
	}
	if unk {
		size = -1
	} else {
		size = int64(s)
	}
	return id, n, size, m, true
}

// ebmlChildren parses a flat list of elements inside a buffer.
func ebmlChildren(b []byte) []ebmlBuf {
	var out []ebmlBuf
	p := 0
	for p < len(b) {
		id, il, sz, sl, ok := parseElHeader(b[p:])
		if !ok {
			break
		}
		start := p + il + sl
		end := start + int(sz)
		if sz < 0 || end > len(b) {
			end = len(b)
		}
		if start > len(b) {
			break
		}
		out = append(out, ebmlBuf{id: id, data: b[start:end]})
		p = end
	}
	return out
}

type ebmlBuf struct {
	id   uint32
	data []byte
}

func ebmlUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

func ebmlFloat(b []byte) float64 {
	switch len(b) {
	case 4:
		return float64(math.Float32frombits(be32(b)))
	case 8:
		return math.Float64frombits(be64(b))
	}
	return 0
}

type mkvTrack struct {
	Track
	num      int
	typ      int
	defDurNs uint64
	priv     []byte
}

type mkvInfo struct {
	timecodeScale uint64
	duration      float64 // in timecode units
	title         string
	tracks        []*mkvTrack
}

func (m *mkvInfo) seconds(units float64) float64 {
	return units * float64(m.timecodeScale) / 1e9
}

func parseMkvInfo(b []byte, m *mkvInfo) {
	m.timecodeScale = 1000000
	for _, e := range ebmlChildren(b) {
		switch e.id {
		case idTimecodeScl:
			m.timecodeScale = ebmlUint(e.data)
		case idDuration:
			m.duration = ebmlFloat(e.data)
		case idTitle:
			m.title = string(e.data)
		}
	}
}

func parseMkvTracks(b []byte) []*mkvTrack {
	var out []*mkvTrack
	for _, te := range ebmlChildren(b) {
		if te.id != idTrackEntry {
			continue
		}
		t := &mkvTrack{}
		t.Default = true
		for _, e := range ebmlChildren(te.data) {
			switch e.id {
			case idTrackNumber:
				t.num = int(ebmlUint(e.data))
				t.ID = t.num
			case idTrackType:
				t.typ = int(ebmlUint(e.data))
				switch t.typ {
				case 1:
					t.Kind = "video"
				case 2:
					t.Kind = "audio"
				case 17:
					t.Kind = "text"
				default:
					t.Kind = "other"
				}
			case idCodecID:
				t.setCodec(strings.TrimRight(string(e.data), "\x00"))
			case idCodecPriv:
				t.priv = e.data
			case idDefaultDur:
				t.defDurNs = ebmlUint(e.data)
				if t.defDurNs > 0 {
					t.FrameRate = round3(1e9 / float64(t.defDurNs))
				}
			case idLanguage:
				t.Language = strings.TrimRight(string(e.data), "\x00")
				if t.Language == "und" {
					t.Language = ""
				}
			case idTrackName:
				t.Name = string(e.data)
			case idFlagDefault:
				t.Default = ebmlUint(e.data) != 0
			case idVideo:
				for _, v := range ebmlChildren(e.data) {
					switch v.id {
					case idPixelW:
						t.Width = int(ebmlUint(v.data))
					case idPixelH:
						t.Height = int(ebmlUint(v.data))
					}
				}
			case idAudio:
				for _, v := range ebmlChildren(e.data) {
					switch v.id {
					case idSampFreq:
						t.SampleRate = int(ebmlFloat(v.data))
					case idChannels:
						t.Channels = int(ebmlUint(v.data))
					case idBitDepth:
						t.BitsPerSample = int(ebmlUint(v.data))
					}
				}
			}
		}
		if t.Kind != "other" {
			t.refineCodec()
		}
		out = append(out, t)
	}
	return out
}

func (t *mkvTrack) setCodec(id string) {
	switch {
	case id == "V_VP8":
		t.Codec, t.CodecName = "vp8", "vp8"
	case id == "V_VP9":
		t.Codec, t.CodecName = "vp9", "vp9"
	case id == "V_AV1":
		t.Codec, t.CodecName = "av01", "av1"
	case id == "V_MPEG4/ISO/AVC":
		t.Codec, t.CodecName = "avc1", "h264"
	case id == "V_MPEGH/ISO/HEVC":
		t.Codec, t.CodecName = "hvc1", "hevc"
	case id == "A_VORBIS":
		t.Codec, t.CodecName = "vorbis", "vorbis"
	case id == "A_OPUS":
		t.Codec, t.CodecName = "opus", "opus"
	case strings.HasPrefix(id, "A_AAC"):
		t.Codec, t.CodecName = "mp4a.40.2", "aac"
	case id == "A_FLAC":
		t.Codec, t.CodecName = "flac", "flac"
	case id == "A_MPEG/L3":
		t.Codec, t.CodecName = "mp3", "mp3"
	case strings.HasPrefix(id, "A_PCM"):
		t.Codec, t.CodecName = "1", "pcm"
	case id == "A_AC3":
		t.Codec, t.CodecName = "ac-3", "ac3"
	case id == "S_TEXT/UTF8", id == "S_TEXT/WEBVTT":
		t.Codec, t.CodecName = "webvtt", "subrip"
	default:
		t.Codec, t.CodecName = id, strings.ToLower(id)
	}
}

func (t *mkvTrack) refineCodec() {
	if t.CodecName == "h264" && len(t.priv) >= 4 {
		t.Codec = fmt.Sprintf("avc1.%02x%02x%02x", t.priv[1], t.priv[2], t.priv[3])
	}
	if t.CodecName == "opus" {
		if t.SampleRate == 0 || t.SampleRate == 8000 {
			t.SampleRate = 48000
		}
	}
}

type mkvCue struct {
	timeUnits uint64
	cluster   uint64
	track     int
}

func parseMkvCues(b []byte) []mkvCue {
	var out []mkvCue
	for _, cp := range ebmlChildren(b) {
		if cp.id != idCuePoint {
			continue
		}
		var c mkvCue
		for _, e := range ebmlChildren(cp.data) {
			switch e.id {
			case idCueTime:
				c.timeUnits = ebmlUint(e.data)
			case idCueTrackPos:
				for _, v := range ebmlChildren(e.data) {
					switch v.id {
					case idCueTrack:
						c.track = int(ebmlUint(v.data))
					case idCueCluster:
						c.cluster = ebmlUint(v.data)
					}
				}
			}
		}
		out = append(out, c)
	}
	return out
}

func probeEBML(r io.ReaderAt, size int64) (*Info, error) {
	hdr, err := readFull(r, 0, 64)
	if len(hdr) < 8 {
		return nil, err
	}
	_, il, sz, sl, ok := parseElHeader(hdr)
	if !ok || sz < 0 {
		return nil, fmt.Errorf("media: bad EBML header")
	}
	headEnd := int64(il+sl) + sz
	body, err := readFull(r, int64(il+sl), int(sz))
	if err != nil {
		return nil, err
	}
	doc := "matroska"
	for _, e := range ebmlChildren(body) {
		if e.id == idDocType {
			doc = strings.TrimRight(string(e.data), "\x00")
		}
	}
	info := &Info{Container: "matroska", Duration: -1}
	if doc == "webm" {
		info.Container = "webm"
	}
	// Segment header
	sh, err := readFull(r, headEnd, 16)
	if len(sh) < 5 {
		return nil, err
	}
	sid, sil, ssz, ssl, ok := parseElHeader(sh)
	if !ok || sid != idSegment {
		return nil, fmt.Errorf("media: no Segment")
	}
	segData := headEnd + int64(sil+ssl)
	segEnd := size
	if ssz >= 0 && segData+ssz < size {
		segEnd = segData + ssz
	}
	mi := &mkvInfo{timecodeScale: 1000000}
	var cues []mkvCue
	var clusterOffs []int64
	var clusterTCs []uint64
	var firstCluster int64 = -1
	pos := segData
	cuesPos := int64(-1)
	for pos+4 < segEnd {
		h, _ := readFull(r, pos, 12)
		if len(h) < 3 {
			break
		}
		id, il2, esz, sl2, ok := parseElHeader(h)
		if !ok {
			break
		}
		dataOff := pos + int64(il2+sl2)
		end := dataOff + esz
		if esz < 0 || end > segEnd {
			end = segEnd
		}
		switch id {
		case idSeekHead:
			if d, err := readFull(r, dataOff, int(end-dataOff)); err == nil {
				for _, sk := range ebmlChildren(d) {
					if sk.id != 0x4DBB {
						continue
					}
					var sid uint32
					var spos uint64
					for _, e := range ebmlChildren(sk.data) {
						if e.id == 0x53AB {
							sid = uint32(ebmlUint(e.data))
						} else if e.id == 0x53AC {
							spos = ebmlUint(e.data)
						}
					}
					if sid == idCues {
						cuesPos = segData + int64(spos)
					}
				}
			}
		case idInfo:
			if d, err := readFull(r, dataOff, int(end-dataOff)); err == nil {
				parseMkvInfo(d, mi)
			}
		case idTracks:
			if d, err := readFull(r, dataOff, int(end-dataOff)); err == nil {
				mi.tracks = parseMkvTracks(d)
			}
		case idCues:
			if d, err := readFull(r, dataOff, int(end-dataOff)); err == nil {
				cues = parseMkvCues(d)
			}
			cuesPos = -2
		case idCluster:
			if firstCluster < 0 {
				firstCluster = pos
			}
			ch, _ := readFull(r, dataOff, 16)
			var tc uint64
			if len(ch) > 3 {
				for _, e := range ebmlChildrenShallow(ch) {
					if e.id == idClusterTC {
						tc = ebmlUint(e.data)
					}
				}
			}
			clusterOffs = append(clusterOffs, pos)
			clusterTCs = append(clusterTCs, tc)
			// If Cues are known and duration present we can stop walking.
			if (cuesPos != -1 || len(cues) > 0) && mi.duration > 0 && len(mi.tracks) > 0 && len(clusterOffs) >= 1 {
				if cuesPos >= 0 && len(cues) == 0 {
					if h2, _ := readFull(r, cuesPos, 12); len(h2) > 3 {
						cid, cil, csz, csl, ok := parseElHeader(h2)
						if ok && cid == idCues && csz >= 0 && csz < 64<<20 {
							if d, err := readFull(r, cuesPos+int64(cil+csl), int(csz)); err == nil {
								cues = parseMkvCues(d)
							}
						}
					}
				}
				goto done
			}
			if len(clusterOffs) > 5000 {
				goto done
			}
		}
		if esz < 0 {
			break
		}
		pos = end
	}
done:
	info.Title = mi.title
	for _, t := range mi.tracks {
		if t.Kind == "other" {
			continue
		}
		tr := t.Track
		info.Tracks = append(info.Tracks, tr)
	}
	if firstCluster >= 0 {
		info.DataStart = firstCluster
	}
	info.DataEnd = segEnd
	if mi.duration > 0 {
		info.Duration = mi.seconds(mi.duration)
	}
	if len(cues) > 0 {
		for _, c := range cues {
			info.Seek = append(info.Seek, SeekPoint{Time: mi.seconds(float64(c.timeUnits)), Offset: segData + int64(c.cluster)})
		}
		sort.SliceStable(info.Seek, func(a, b int) bool { return info.Seek[a].Time < info.Seek[b].Time })
		info.Seek = dedupSeek(info.Seek)
	} else {
		for i := range clusterOffs {
			info.Seek = append(info.Seek, SeekPoint{Time: mi.seconds(float64(clusterTCs[i])), Offset: clusterOffs[i]})
		}
	}
	if info.Duration <= 0 && len(clusterOffs) > 0 {
		// no Duration element: estimate from the last cluster's blocks.
		last := clusterOffs[len(clusterOffs)-1]
		lastEnd := segEnd
		if d, err := readFull(r, last, int(minI64(lastEnd-last, 8<<20))); err == nil {
			blocks := ParseWebMBlocks(d)
			var mx float64
			for _, b := range blocks {
				if b.Time+b.Duration > mx {
					mx = b.Time + b.Duration
				}
			}
			info.Duration = mi.seconds(float64(clusterTCs[len(clusterTCs)-1])) + mx
			if len(blocks) > 0 {
				_ = blocks
			}
		}
		if info.Duration <= 0 {
			info.Duration = mi.seconds(float64(clusterTCs[len(clusterTCs)-1]))
		}
	}
	if len(info.Tracks) == 0 {
		return nil, fmt.Errorf("media: matroska without tracks")
	}
	for i := range info.Tracks {
		info.Tracks[i].Duration = info.Duration
	}
	return info, nil
}

func minI64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func dedupSeek(s []SeekPoint) []SeekPoint {
	out := s[:0]
	for _, p := range s {
		if len(out) > 0 && out[len(out)-1].Offset == p.Offset {
			continue
		}
		out = append(out, p)
	}
	return out
}

// ebmlChildrenShallow tolerates a truncated tail (used for cluster heads).
func ebmlChildrenShallow(b []byte) []ebmlBuf {
	var out []ebmlBuf
	p := 0
	for p < len(b) {
		id, il, sz, sl, ok := parseElHeader(b[p:])
		if !ok {
			break
		}
		start := p + il + sl
		if sz < 0 || start+int(sz) > len(b) {
			break
		}
		out = append(out, ebmlBuf{id: id, data: b[start : start+int(sz)]})
		p = start + int(sz)
	}
	return out
}

// WebMBlock is one (Simple)Block: time is seconds relative to the cluster
// start only when called from ParseWebMBlocks; WebMSegment resolves absolute.
type WebMBlock struct {
	Track    int
	Time     float64 // absolute seconds
	Duration float64
	Key      bool
	Data     []byte
}

// ParseWebMBlocks decodes the Clusters in b (a buffer starting at a Cluster
// element or at a Segment's children) with the default 1 ms timecode scale.
func ParseWebMBlocks(b []byte) []WebMBlock { return ParseWebMBlocksScale(b, 1000000) }

// ParseWebMBlocksScale is ParseWebMBlocks with an explicit timecode scale.
func ParseWebMBlocksScale(b []byte, scale uint64) []WebMBlock {
	var out []WebMBlock
	sec := func(u float64) float64 { return u * float64(scale) / 1e9 }
	p := 0
	for p < len(b) {
		id, il, sz, sl, ok := parseElHeader(b[p:])
		if !ok {
			break
		}
		start := p + il + sl
		end := start + int(sz)
		if sz < 0 || end > len(b) {
			end = len(b)
		}
		if id == idCluster {
			var tc uint64
			for _, e := range ebmlChildrenShallow(b[start:end]) {
				switch e.id {
				case idClusterTC:
					tc = ebmlUint(e.data)
				case idSimpleBlock:
					if blk, ok := decodeBlock(e.data); ok {
						blk.Time = sec(float64(tc) + blk.Time)
						blk.Key = e.data[3]&0x80 != 0 || blk.Key
						out = append(out, blk)
					}
				case idBlockGroup:
					var dur float64
					var blk WebMBlock
					have := false
					for _, g := range ebmlChildren(e.data) {
						if g.id == idBlock {
							blk, have = decodeBlock(g.data)
						} else if g.id == idBlockDur {
							dur = float64(ebmlUint(g.data))
						}
					}
					if have {
						blk.Time = sec(float64(tc) + blk.Time)
						blk.Duration = sec(dur)
						blk.Key = true // blocks in groups without ReferenceBlock are keyframes; approximation
						for _, g := range ebmlChildren(e.data) {
							if g.id == 0xFB { // ReferenceBlock
								blk.Key = false
							}
						}
						out = append(out, blk)
					}
				}
			}
		}
		p = end
	}
	return out
}

func decodeBlock(d []byte) (WebMBlock, bool) {
	tn, n, _ := readVint(d, false)
	if n == 0 || len(d) < n+3 {
		return WebMBlock{}, false
	}
	rel := int16(be16(d[n:]))
	flags := d[n+2]
	return WebMBlock{Track: int(tn), Time: float64(rel), Key: flags&0x80 != 0, Data: d[n+3:]}, true
}

// WebMInit describes an init segment (EBML header + Segment{Info,Tracks}).
type WebMInit struct {
	Scale  uint64
	Dur    float64
	Tracks []Track
}

// ParseWebMInit parses the head of a WebM stream up to the first Cluster.
func ParseWebMInit(b []byte) (*WebMInit, error) {
	p := 0
	var segStart int = -1
	for p < len(b) {
		id, il, sz, sl, ok := parseElHeader(b[p:])
		if !ok {
			break
		}
		if id == idSegment {
			segStart = p + il + sl
			break
		}
		p += il + sl + int(sz)
	}
	if segStart < 0 {
		return nil, fmt.Errorf("media: no Segment in init")
	}
	mi := &mkvInfo{timecodeScale: 1000000}
	q := segStart
	for q < len(b) {
		id, il, sz, sl, ok := parseElHeader(b[q:])
		if !ok {
			break
		}
		st := q + il + sl
		en := st + int(sz)
		if sz < 0 || en > len(b) {
			en = len(b)
		}
		switch id {
		case idInfo:
			parseMkvInfo(b[st:en], mi)
		case idTracks:
			mi.tracks = parseMkvTracks(b[st:en])
		case idCluster:
			q = len(b)
			continue
		}
		q = en
	}
	if len(mi.tracks) == 0 {
		return nil, fmt.Errorf("media: no tracks in init")
	}
	in := &WebMInit{Scale: mi.timecodeScale}
	if mi.duration > 0 {
		in.Dur = mi.seconds(mi.duration)
	}
	for _, t := range mi.tracks {
		in.Tracks = append(in.Tracks, t.Track)
	}
	return in, nil
}
