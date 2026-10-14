package media

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// ISO BMFF (MP4/MOV/M4A/fMP4) parsing.

type mp4box struct {
	typ   string
	start int64 // offset of the box header
	hdr   int   // header length
	size  int64 // total size incl. header (0 = to end)
	data  []byte
}

// parseBoxes splits an in-memory buffer into boxes.
func parseBoxes(b []byte) []mp4box {
	var out []mp4box
	var off int64
	for off+8 <= int64(len(b)) {
		sz := int64(be32(b[off:]))
		typ := string(b[off+4 : off+8])
		hdr := 8
		if sz == 1 {
			if off+16 > int64(len(b)) {
				break
			}
			sz = int64(be64(b[off+8:]))
			hdr = 16
		} else if sz == 0 {
			sz = int64(len(b)) - off
		}
		if sz < int64(hdr) || off+sz > int64(len(b)) {
			if sz >= int64(hdr) && off+int64(hdr) <= int64(len(b)) {
				sz = int64(len(b)) - off // truncated tail: keep what we have
			} else {
				break
			}
		}
		out = append(out, mp4box{typ: typ, start: off, hdr: hdr, size: sz, data: b[off+int64(hdr) : off+sz]})
		off += sz
	}
	return out
}

func findBox(bs []mp4box, typ string) *mp4box {
	for i := range bs {
		if bs[i].typ == typ {
			return &bs[i]
		}
	}
	return nil
}

func findBoxes(bs []mp4box, typ string) []mp4box {
	var o []mp4box
	for _, b := range bs {
		if b.typ == typ {
			o = append(o, b)
		}
	}
	return o
}

type mp4Sample struct {
	off  int64
	size uint32
	dts  int64
	cts  int64
	sync bool
}

type mp4Track struct {
	Track
	timescale uint32
	mdhdDur   uint64
	samples   []mp4Sample
	fourcc    string
	editDur   int         // elst segment duration (movie timescale), -1 none
	editStart int         // elst media_time of the first non-empty edit (-1 none)
	cfg       []byte      // esds payload (AAC) or dOps (Opus) of the first sample entry
	vcfg      []byte      // avcC / vpcC / av1C payload of a video sample entry
	prot      *Protection // the sample entry is encv/enca: its sinf information
	// fragmented
	trex struct{ dur, size, flags uint32 }
}

// MP4Moov is the parsed movie header; used by MSE for init segments.
type MP4Moov struct {
	Timescale  uint32
	Duration   float64
	Tracks     []*mp4Track
	Fragmented bool
	FragDur    float64
}

func parseMoov(b []byte) (*MP4Moov, error) {
	m := &MP4Moov{}
	kids := parseBoxes(b)
	if mv := findBox(kids, "mvhd"); mv != nil && len(mv.data) >= 20 {
		d := mv.data
		var dur uint64
		if d[0] == 1 && len(d) >= 32 {
			m.Timescale = be32(d[20:])
			dur = be64(d[24:])
		} else {
			m.Timescale = be32(d[12:])
			dur = uint64(be32(d[16:]))
		}
		if m.Timescale > 0 {
			m.Duration = float64(dur) / float64(m.Timescale)
		}
	}
	for _, t := range findBoxes(kids, "trak") {
		tr, err := parseTrak(t.data)
		if err != nil || tr == nil {
			continue
		}
		m.Tracks = append(m.Tracks, tr)
	}
	if mvex := findBox(kids, "mvex"); mvex != nil {
		m.Fragmented = true
		mk := parseBoxes(mvex.data)
		if mehd := findBox(mk, "mehd"); mehd != nil && len(mehd.data) >= 8 && m.Timescale > 0 {
			var fd uint64
			if mehd.data[0] == 1 && len(mehd.data) >= 12 {
				fd = be64(mehd.data[4:])
			} else {
				fd = uint64(be32(mehd.data[4:]))
			}
			m.FragDur = float64(fd) / float64(m.Timescale)
		}
		for _, tx := range findBoxes(mk, "trex") {
			if len(tx.data) >= 24 {
				id := int(be32(tx.data[4:]))
				for _, tr := range m.Tracks {
					if tr.ID == id {
						tr.trex.dur = be32(tx.data[12:])
						tr.trex.size = be32(tx.data[16:])
						tr.trex.flags = be32(tx.data[20:])
					}
				}
			}
		}
	}
	if len(m.Tracks) == 0 {
		return nil, fmt.Errorf("media: mp4 without tracks")
	}
	return m, nil
}

func parseTrak(b []byte) (*mp4Track, error) {
	t := &mp4Track{}
	kids := parseBoxes(b)
	if tk := findBox(kids, "tkhd"); tk != nil && len(tk.data) >= 84 {
		d := tk.data
		if d[0] == 1 {
			t.ID = int(be32(d[20:]))
			if len(d) >= 96 {
				t.Width = int(be32(d[88:]) >> 16)
				t.Height = int(be32(d[92:]) >> 16)
			}
		} else {
			t.ID = int(be32(d[12:]))
			t.Width = int(be32(d[76:]) >> 16)
			t.Height = int(be32(d[80:]) >> 16)
		}
		t.Default = true
	}
	mdia := findBox(kids, "mdia")
	if mdia == nil {
		return nil, nil
	}
	mk := parseBoxes(mdia.data)
	if h := findBox(mk, "mdhd"); h != nil && len(h.data) >= 24 {
		d := h.data
		var lang uint16
		if d[0] == 1 && len(d) >= 36 {
			t.timescale = be32(d[20:])
			t.mdhdDur = be64(d[24:])
			lang = uint16(be16(d[32:]))
		} else {
			t.timescale = be32(d[12:])
			t.mdhdDur = uint64(be32(d[16:]))
			lang = uint16(be16(d[20:]))
		}
		if lang != 0 && lang != 0x7fff {
			t.Language = string([]byte{byte(lang>>10&31) + 0x60, byte(lang>>5&31) + 0x60, byte(lang&31) + 0x60})
			if t.Language == "und" {
				t.Language = ""
			}
		}
		if t.timescale > 0 {
			t.Duration = float64(t.mdhdDur) / float64(t.timescale)
		}
	}
	if h := findBox(mk, "hdlr"); h != nil && len(h.data) >= 24 {
		switch string(h.data[8:12]) {
		case "vide":
			t.Kind = "video"
		case "soun":
			t.Kind = "audio"
		case "text", "sbtl", "subt", "clcp":
			t.Kind = "text"
		default:
			t.Kind = "other"
		}
		if len(h.data) > 25 {
			n := strings.TrimRight(string(h.data[24:]), "\x00")
			if len(n) > 0 && n[0] < 32 && int(n[0]) == len(n)-1 {
				n = n[1:]
			}
			t.Name = n
		}
	}
	t.editStart, t.editDur = -1, -1
	if edts := findBox(kids, "edts"); edts != nil {
		if el := findBox(parseBoxes(edts.data), "elst"); el != nil && len(el.data) >= 8 {
			n := int(be32(el.data[4:]))
			ver := el.data[0]
			p := 8
			for i := 0; i < n; i++ {
				var mt, sd int64
				if ver == 1 {
					if p+20 > len(el.data) {
						break
					}
					mt = int64(be64(el.data[p+8:]))
					sd = int64(be64(el.data[p:]))
					p += 20
				} else {
					if p+12 > len(el.data) {
						break
					}
					mt = int64(int32(be32(el.data[p+4:])))
					sd = int64(be32(el.data[p:]))
					p += 12
				}
				if mt >= 0 {
					t.editStart, t.editDur = int(mt), int(sd)
					break
				}
			}
		}
	}
	minf := findBox(mk, "minf")
	if minf == nil {
		return t, nil
	}
	stbl := findBox(parseBoxes(minf.data), "stbl")
	if stbl == nil {
		return t, nil
	}
	sk := parseBoxes(stbl.data)
	if sd := findBox(sk, "stsd"); sd != nil && len(sd.data) >= 16 {
		parseStsd(t, sd.data[4:])
	}
	buildSamples(t, sk)
	if t.Kind == "video" && t.timescale > 0 && len(t.samples) > 1 {
		last := t.samples[len(t.samples)-1]
		total := float64(last.dts) / float64(t.timescale)
		if total > 0 {
			t.FrameRate = round3(float64(len(t.samples)-1) / total)
			// prefer exact avg: frames / duration
			if t.Duration > 0 {
				t.FrameRate = round3(float64(len(t.samples)) / t.Duration)
			}
		}
	}
	t.Frames = len(t.samples)
	if t.Duration > 0 && len(t.samples) > 0 {
		var bytes int64
		for _, s := range t.samples {
			bytes += int64(s.size)
		}
		t.BitRate = int(float64(bytes) * 8 / t.Duration)
	}
	return t, nil
}

func parseStsd(t *mp4Track, d []byte) {
	// d: entry_count(4) then entries; take the first.
	if len(d) < 12 {
		return
	}
	entries := parseBoxes(d[4:])
	if len(entries) == 0 {
		return
	}
	e := entries[0]
	t.fourcc = e.typ
	ed := e.data
	switch t.Kind {
	case "video":
		if len(ed) >= 78 {
			w, h := be16(ed[24:]), be16(ed[26:])
			if w > 0 && h > 0 {
				t.Width, t.Height = w, h
			}
			kids := parseBoxes(ed[78:])
			t.parseVideoConfig(t.unprotect(e.typ, kids), kids)
		}
	case "audio":
		if len(ed) >= 28 {
			ver := be16(ed[8:])
			t.Channels = be16(ed[16:])
			t.BitsPerSample = be16(ed[18:])
			t.SampleRate = int(be32(ed[24:]) >> 16)
			off := 28
			if ver == 1 {
				off += 16
			} else if ver == 2 {
				off += 36
				if len(ed) >= 28+36 {
					t.Channels = int(be32(ed[28+24:]))
				}
			}
			if off <= len(ed) {
				kids := parseBoxes(ed[off:])
				t.parseAudioConfig(t.unprotect(e.typ, kids), kids)
			}
		}
	}
	if t.CodecName == "" {
		t.Codec = strings.TrimSpace(e.typ)
		t.CodecName = strings.ToLower(strings.TrimSpace(e.typ))
	}
}

// unprotect maps an encrypted sample entry (encv, enca) to the format it was before encryption and records the
// protection scheme; other entries are returned as they are.
func (t *mp4Track) unprotect(typ string, kids []mp4box) string {
	if typ != "encv" && typ != "enca" {
		return typ
	}
	if sinf := findBox(kids, "sinf"); sinf != nil {
		if orig, p := parseSinf(*sinf); orig != "" {
			t.prot = p
			return orig
		}
	}
	return typ
}

func (t *mp4Track) parseVideoConfig(typ string, kids []mp4box) {
	switch typ {
	case "avc1", "avc3", "avc2", "avc4":
		t.CodecName = "h264"
		t.Codec = typ
		if c := findBox(kids, "avcC"); c != nil && len(c.data) >= 4 {
			t.Codec = fmt.Sprintf("%s.%02x%02x%02x", typ, c.data[1], c.data[2], c.data[3])
			t.vcfg = c.data
		}
	case "hvc1", "hev1":
		t.CodecName = "hevc"
		t.Codec = typ
		if c := findBox(kids, "hvcC"); c != nil && len(c.data) >= 13 {
			t.Codec = hevcCodecString(typ, c.data)
			t.vcfg = c.data
		}
	case "vp08":
		t.CodecName, t.Codec = "vp8", "vp8"
	case "vp09":
		t.CodecName, t.Codec = "vp9", "vp09"
		if c := findBox(kids, "vpcC"); c != nil && len(c.data) >= 8 {
			t.vcfg = c.data
			t.Codec = fmt.Sprintf("vp09.%02d.%02d.%02d", c.data[4], c.data[5], c.data[6]>>4)
		}
	case "av01":
		t.CodecName, t.Codec = "av1", "av01"
		if c := findBox(kids, "av1C"); c != nil && len(c.data) >= 4 {
			t.vcfg = c.data
			prof := c.data[1] >> 5
			lvl := c.data[1] & 31
			tier := "M"
			if c.data[2]>>7 == 1 {
				tier = "H"
			}
			depth := 8
			if c.data[2]>>6&1 == 1 {
				depth = 10
				if c.data[2]>>5&1 == 1 {
					depth = 12
				}
			}
			t.Codec = fmt.Sprintf("av01.%d.%02d%s.%02d", prof, lvl, tier, depth)
		}
	case "mp4v":
		t.CodecName, t.Codec = "mpeg4", "mp4v.20"
	case "jpeg", "mjpa", "mjpb":
		t.CodecName, t.Codec = "mjpeg", typ
	case "s263":
		t.CodecName, t.Codec = "h263", typ
	}
}

func hevcCodecString(typ string, d []byte) string {
	space := ""
	switch d[1] >> 6 {
	case 1:
		space = "A"
	case 2:
		space = "B"
	case 3:
		space = "C"
	}
	prof := d[1] & 31
	compat := uint32(d[2])<<24 | uint32(d[3])<<16 | uint32(d[4])<<8 | uint32(d[5])
	var rev uint32
	for i := 0; i < 32; i++ {
		if compat&(1<<uint(i)) != 0 {
			rev |= 1 << uint(31-i)
		}
	}
	tier := "L"
	if d[1]>>5&1 == 1 {
		tier = "H"
	}
	cons := d[6:12]
	end := 6
	for i := 5; i >= 0; i-- {
		if cons[i] != 0 {
			end = i + 1
			break
		}
	}
	if end == 6 && cons[5] == 0 {
		end = 0
	}
	var cs []string
	for i := 0; i < end; i++ {
		cs = append(cs, fmt.Sprintf("%X", cons[i]))
	}
	s := fmt.Sprintf("%s.%s%d.%X.%s%d", typ, space, prof, rev, tier, d[12])
	if len(cs) > 0 {
		s += "." + strings.Join(cs, ".")
	}
	return s
}

func (t *mp4Track) parseAudioConfig(typ string, kids []mp4box) {
	switch typ {
	case "mp4a":
		t.CodecName = "aac"
		t.Codec = "mp4a.40.2"
		if e := findBox(kids, "esds"); e != nil && len(e.data) > 4 {
			t.cfg = e.data[4:]
			oti, aot := parseEsds(e.data[4:])
			switch oti {
			case 0x40:
				t.CodecName = "aac"
				if aot == 0 {
					aot = 2
				}
				t.Codec = fmt.Sprintf("mp4a.40.%d", aot)
				if aot == 42 {
					t.nativeNo = !usacConfigSupported(esdsDecoderSpecific(e.data[4:]))
				}
				// HE-AAC: report the SBR output rate, like ffprobe and browsers do
				if c, err := parseASC(esdsDecoderSpecific(e.data[4:])); err == nil && c.sbr && c.extRate > 0 {
					t.SampleRate = c.extRate
				}
			case 0x6b:
				t.CodecName, t.Codec = "mp3", "mp4a.6B"
			case 0x69:
				t.CodecName, t.Codec = "mp3", "mp4a.69"
			case 0xa5:
				t.CodecName, t.Codec = "ac3", "ac-3"
			case 0xdd:
				t.CodecName, t.Codec = "vorbis", "vorbis"
			default:
				t.Codec = fmt.Sprintf("mp4a.%02X", oti)
				t.CodecName = "unknown"
			}
		}
	case ".mp3":
		t.CodecName, t.Codec = "mp3", "mp3"
	case "Opus":
		t.CodecName, t.Codec = "opus", "opus"
		if d := findBox(kids, "dOps"); d != nil && len(d.data) >= 4 {
			t.cfg = d.data
			t.Channels = int(d.data[1])
			t.SampleRate = 48000
		}
	case "fLaC":
		t.CodecName, t.Codec = "flac", "flac"
	case "ac-3":
		t.CodecName, t.Codec = "ac3", "ac-3"
	case "ec-3":
		t.CodecName, t.Codec = "eac3", "ec-3"
	case "alac":
		t.CodecName, t.Codec = "alac", "alac"
	case "sowt", "twos", "in24", "in32", "fl32", "fl64", "lpcm", "raw ":
		t.CodecName, t.Codec = "pcm", typ
	case "samr", "sawb":
		t.CodecName, t.Codec = "amr", typ
	}
}

// parseEsds walks the ES descriptor to find objectTypeIndication and AAC AOT.
func parseEsds(b []byte) (oti, aot int) {
	readLen := func(p int) (int, int) {
		n := 0
		for i := 0; i < 4 && p < len(b); i++ {
			c := int(b[p])
			p++
			n = n<<7 | c&0x7f
			if c&0x80 == 0 {
				break
			}
		}
		return n, p
	}
	p := 0
	for p < len(b) {
		tag := b[p]
		p++
		_, np := readLen(p)
		p = np
		switch tag {
		case 3: // ES_Descriptor
			if p+3 > len(b) {
				return
			}
			flags := b[p+2]
			p += 3
			if flags&0x80 != 0 {
				p += 2
			}
			if flags&0x40 != 0 && p < len(b) {
				p += 1 + int(b[p])
			}
			if flags&0x20 != 0 {
				p += 2
			}
		case 4: // DecoderConfigDescriptor
			if p+13 > len(b) {
				return
			}
			oti = int(b[p])
			p += 13
		case 5: // DecoderSpecificInfo
			if p+2 <= len(b) {
				aot = int(b[p] >> 3)
				if aot == 31 && p+2 <= len(b) {
					aot = 32 + (int(b[p]&7)<<3 | int(b[p+1])>>5)
				}
			}
			return
		default:
			return
		}
	}
	return
}

func buildSamples(t *mp4Track, sk []mp4box) {
	stts := findBox(sk, "stts")
	stsz := findBox(sk, "stsz")
	if stsz == nil {
		stsz = findBox(sk, "stz2")
	}
	stsc := findBox(sk, "stsc")
	stco := findBox(sk, "stco")
	co64 := findBox(sk, "co64")
	stss := findBox(sk, "stss")
	ctts := findBox(sk, "ctts")
	if stts == nil || stsz == nil || stsc == nil || (stco == nil && co64 == nil) {
		return
	}
	// sizes
	var sizes []uint32
	if stsz.typ == "stsz" && len(stsz.data) >= 12 {
		fixed := be32(stsz.data[4:])
		n := int(be32(stsz.data[8:]))
		if n > 20_000_000 {
			return
		}
		sizes = make([]uint32, n)
		if fixed != 0 {
			for i := range sizes {
				sizes[i] = fixed
			}
		} else {
			for i := 0; i < n && 12+4*i+4 <= len(stsz.data); i++ {
				sizes[i] = be32(stsz.data[12+4*i:])
			}
		}
	} else {
		return
	}
	// chunk offsets
	var chunks []int64
	if stco != nil && len(stco.data) >= 8 {
		n := int(be32(stco.data[4:]))
		for i := 0; i < n && 8+4*i+4 <= len(stco.data); i++ {
			chunks = append(chunks, int64(be32(stco.data[8+4*i:])))
		}
	} else if co64 != nil && len(co64.data) >= 8 {
		n := int(be32(co64.data[4:]))
		for i := 0; i < n && 8+8*i+8 <= len(co64.data); i++ {
			chunks = append(chunks, int64(be64(co64.data[8+8*i:])))
		}
	}
	// stsc runs
	type run struct{ first, per uint32 }
	var runs []run
	if len(stsc.data) >= 8 {
		n := int(be32(stsc.data[4:]))
		for i := 0; i < n && 8+12*i+12 <= len(stsc.data); i++ {
			runs = append(runs, run{be32(stsc.data[8+12*i:]), be32(stsc.data[8+12*i+4:])})
		}
	}
	samples := make([]mp4Sample, len(sizes))
	si := 0
	for ci := range chunks {
		per := uint32(0)
		for _, r := range runs {
			if r.first <= uint32(ci+1) {
				per = r.per
			}
		}
		off := chunks[ci]
		for k := uint32(0); k < per && si < len(sizes); k++ {
			samples[si].off = off
			samples[si].size = sizes[si]
			off += int64(sizes[si])
			si++
		}
	}
	samples = samples[:si]
	// durations
	if len(stts.data) >= 8 {
		n := int(be32(stts.data[4:]))
		var dts int64
		idx := 0
		for i := 0; i < n && 8+8*i+8 <= len(stts.data) && idx < len(samples); i++ {
			cnt := int(be32(stts.data[8+8*i:]))
			delta := int64(be32(stts.data[8+8*i+4:]))
			for k := 0; k < cnt && idx < len(samples); k++ {
				samples[idx].dts = dts
				samples[idx].cts = dts
				dts += delta
				idx++
			}
		}
	}
	if ctts != nil && len(ctts.data) >= 8 {
		n := int(be32(ctts.data[4:]))
		idx := 0
		for i := 0; i < n && 8+8*i+8 <= len(ctts.data) && idx < len(samples); i++ {
			cnt := int(be32(ctts.data[8+8*i:]))
			off := int64(int32(be32(ctts.data[8+8*i+4:])))
			for k := 0; k < cnt && idx < len(samples); k++ {
				samples[idx].cts = samples[idx].dts + off
				idx++
			}
		}
	}
	if stss == nil {
		for i := range samples {
			samples[i].sync = true
		}
	} else if len(stss.data) >= 8 {
		n := int(be32(stss.data[4:]))
		for i := 0; i < n && 8+4*i+4 <= len(stss.data); i++ {
			s := int(be32(stss.data[8+4*i:])) - 1
			if s >= 0 && s < len(samples) {
				samples[s].sync = true
			}
		}
	}
	t.samples = samples
}

// topBox is a top-level box located in the file (header only).
type topBox struct {
	typ   string
	start int64
	size  int64
	hdr   int
}

func scanTopLevel(r io.ReaderAt, size int64) []topBox {
	var out []topBox
	var off int64
	for off+8 <= size {
		h, err := readFull(r, off, 16)
		if len(h) < 8 {
			_ = err
			break
		}
		sz := int64(be32(h))
		hdr := 8
		if sz == 1 && len(h) >= 16 {
			sz = int64(be64(h[8:]))
			hdr = 16
		} else if sz == 0 {
			sz = size - off
		}
		if sz < int64(hdr) {
			break
		}
		if off+sz > size {
			sz = size - off
		}
		out = append(out, topBox{string(h[4:8]), off, sz, hdr})
		off += sz
	}
	return out
}

func probeMP4(r io.ReaderAt, size int64) (*Info, error) {
	boxes := scanTopLevel(r, size)
	info := &Info{Container: "mp4", Duration: -1}
	var moov *MP4Moov
	var mdatStart, mdatEnd int64 = -1, 0
	var moofs []topBox
	for _, b := range boxes {
		switch b.typ {
		case "ftyp":
			if d, err := readFull(r, b.start+8, 8); err == nil {
				info.Brand = strings.TrimSpace(string(d[:4]))
				if info.Brand == "qt" {
					info.Container = "mp4" // MOV treated as mp4 family
				}
			}
		case "moov":
			if b.size > 256<<20 {
				return nil, fmt.Errorf("media: moov too large")
			}
			d, err := readFull(r, b.start+int64(b.hdr), int(b.size)-b.hdr)
			if err != nil {
				return nil, err
			}
			m, err := parseMoov(d)
			if err != nil {
				return nil, err
			}
			moov = m
		case "mdat":
			if mdatStart < 0 {
				mdatStart = b.start + int64(b.hdr)
			}
			mdatEnd = b.start + b.size
		case "moof":
			moofs = append(moofs, b)
		}
	}
	if moov == nil {
		return nil, fmt.Errorf("media: mp4 has no moov box")
	}
	for _, t := range moov.Tracks {
		if t.Kind == "other" {
			continue
		}
		tr := t.Track
		tr.Default = false
		info.Tracks = append(info.Tracks, tr)
	}
	if len(info.Tracks) > 0 {
		for _, k := range []string{"video", "audio"} {
			for i := range info.Tracks {
				if info.Tracks[i].Kind == k {
					info.Tracks[i].Default = true
					break
				}
			}
		}
	}
	info.Duration = moov.Duration
	// longest track duration wins when mvhd is zero or shorter
	if info.Duration <= 0 && !moov.Fragmented {
		for _, t := range moov.Tracks {
			if t.Duration > info.Duration {
				info.Duration = t.Duration
			}
		}
	}
	if mdatStart >= 0 {
		info.DataStart, info.DataEnd = mdatStart, mdatEnd
	}
	if moov.Fragmented || len(moofs) > 0 {
		info.Fragmented = true
		var end float64
		for _, mb := range moofs {
			if mb.size > 64<<20 {
				continue
			}
			d, err := readFull(r, mb.start+int64(mb.hdr), int(mb.size)-mb.hdr)
			if err != nil {
				continue
			}
			frs := parseMoof(d, moov)
			for _, f := range frs {
				if f.end > end {
					end = f.end
				}
				if f.trackIndex == primaryTrack(moov) {
					info.Seek = append(info.Seek, SeekPoint{Time: f.start, Offset: mb.start})
				}
			}
		}
		if end > 0 {
			info.Duration = end
		} else if moov.FragDur > 0 {
			info.Duration = moov.FragDur
		}
		if len(moofs) > 0 && info.DataStart == 0 {
			info.DataStart = moofs[0].start
		}
		if info.DataEnd == 0 {
			info.DataEnd = size
		}
	} else {
		// Seek table from the primary track's sync samples.
		pi := primaryTrack(moov)
		if pi >= 0 {
			t := moov.Tracks[pi]
			for _, s := range t.samples {
				if s.sync && t.timescale > 0 {
					info.Seek = append(info.Seek, SeekPoint{Time: float64(s.cts) / float64(t.timescale), Offset: s.off})
				}
			}
			sort.SliceStable(info.Seek, func(a, b int) bool { return info.Seek[a].Time < info.Seek[b].Time })
		}
	}
	if info.Duration < 0 {
		info.Duration = 0
	}
	return info, nil
}

func primaryTrack(m *MP4Moov) int {
	best := -1
	for i, t := range m.Tracks {
		if t.Kind == "video" {
			return i
		}
		if best < 0 && t.Kind == "audio" {
			best = i
		}
	}
	return best
}

// Fragment is one moof's span of a track (seconds) used by probing and MSE.
type Fragment struct {
	trackIndex int
	TrackID    int
	start, end float64
	Start, End float64
	Samples    int
	Keyframe   bool
}

// parseMoof decodes a moof payload into per-track time spans.
func parseMoof(d []byte, moov *MP4Moov) []Fragment {
	var out []Fragment
	for _, tf := range findBoxes(parseBoxes(d), "traf") {
		kids := parseBoxes(tf.data)
		tfhd := findBox(kids, "tfhd")
		if tfhd == nil || len(tfhd.data) < 8 {
			continue
		}
		flags := be32(tfhd.data) & 0xffffff
		id := int(be32(tfhd.data[4:]))
		p := 8
		if flags&1 != 0 {
			p += 8
		}
		if flags&2 != 0 {
			p += 4
		}
		var defDur, defSize, defFlags uint32
		var hasDur bool
		if flags&8 != 0 && p+4 <= len(tfhd.data) {
			defDur = be32(tfhd.data[p:])
			hasDur = true
			p += 4
		}
		if flags&0x10 != 0 && p+4 <= len(tfhd.data) {
			defSize = be32(tfhd.data[p:])
			p += 4
		}
		if flags&0x20 != 0 && p+4 <= len(tfhd.data) {
			defFlags = be32(tfhd.data[p:])
		}
		ti := -1
		var trk *mp4Track
		for i, t := range moov.Tracks {
			if t.ID == id {
				ti, trk = i, t
			}
		}
		if trk == nil || trk.timescale == 0 {
			continue
		}
		if !hasDur {
			defDur = trk.trex.dur
		}
		_ = defSize
		if flags&0x20 == 0 {
			defFlags = trk.trex.flags
		}
		var base uint64
		if tfdt := findBox(kids, "tfdt"); tfdt != nil && len(tfdt.data) >= 8 {
			if tfdt.data[0] == 1 && len(tfdt.data) >= 12 {
				base = be64(tfdt.data[4:])
			} else {
				base = uint64(be32(tfdt.data[4:]))
			}
		}
		var total uint64
		n := 0
		key := false
		for _, tr := range findBoxes(kids, "trun") {
			if len(tr.data) < 8 {
				continue
			}
			fl := be32(tr.data) & 0xffffff
			cnt := int(be32(tr.data[4:]))
			q := 8
			if fl&1 != 0 {
				q += 4
			}
			firstFlags := defFlags
			if fl&4 != 0 {
				if q+4 <= len(tr.data) {
					firstFlags = be32(tr.data[q:])
				}
				q += 4
			}
			for i := 0; i < cnt; i++ {
				dur := defDur
				if fl&0x100 != 0 {
					if q+4 > len(tr.data) {
						break
					}
					dur = be32(tr.data[q:])
					q += 4
				}
				if fl&0x200 != 0 {
					q += 4
				}
				sf := defFlags
				if i == 0 && fl&4 != 0 {
					sf = firstFlags
				}
				if fl&0x400 != 0 {
					if q+4 > len(tr.data) {
						break
					}
					sf = be32(tr.data[q:])
					q += 4
				}
				if fl&0x800 != 0 {
					q += 4
				}
				if i == 0 && n == 0 {
					key = sf&0x10000 == 0
				}
				total += uint64(dur)
				n++
			}
		}
		ts := float64(trk.timescale)
		out = append(out, Fragment{trackIndex: ti, TrackID: id, start: float64(base) / ts, end: float64(base+total) / ts,
			Start: float64(base) / ts, End: float64(base+total) / ts, Samples: n, Keyframe: key})
	}
	return out
}

// MP4Init summarises an fMP4 initialisation segment for MSE.
type MP4Init struct {
	moov   *MP4Moov
	Tracks []Track
	Dur    float64
}

// ParseMP4Init parses an initialisation segment (ftyp+moov).
func ParseMP4Init(b []byte) (*MP4Init, error) {
	m := findBox(parseBoxes(b), "moov")
	if m == nil {
		return nil, fmt.Errorf("media: no moov in init segment")
	}
	mv, err := parseMoov(m.data)
	if err != nil {
		return nil, err
	}
	in := &MP4Init{moov: mv, Dur: mv.Duration}
	for _, t := range mv.Tracks {
		in.Tracks = append(in.Tracks, t.Track)
	}
	return in, nil
}

// ParseMP4Segment returns per-track time spans of a media segment (moof+mdat...).
// The init segment is needed for timescales.
func ParseMP4Segment(b []byte, init *MP4Init) ([]Fragment, error) {
	if init == nil {
		return nil, fmt.Errorf("media: segment before init")
	}
	var out []Fragment
	found := false
	for _, bx := range parseBoxes(b) {
		if bx.typ == "moof" {
			found = true
			out = append(out, parseMoof(bx.data, init.moov)...)
		}
	}
	if !found {
		return nil, fmt.Errorf("media: no moof in segment")
	}
	return out, nil
}

// MP4Samples returns the sample table (offset, size, time, sync) of a track
// of a progressive file; used to pull MJPEG frames.
type SampleRef struct {
	Offset int64
	Size   int
	Time   float64
	Sync   bool
}

func MP4Samples(r io.ReaderAt, size int64, trackID int) (fourcc string, refs []SampleRef, err error) {
	for _, b := range scanTopLevel(r, size) {
		if b.typ != "moov" {
			continue
		}
		d, e := readFull(r, b.start+int64(b.hdr), int(b.size)-b.hdr)
		if e != nil {
			return "", nil, e
		}
		m, e := parseMoov(d)
		if e != nil {
			return "", nil, e
		}
		for _, t := range m.Tracks {
			if t.ID == trackID || (trackID == 0 && t.Kind == "video") {
				for _, s := range t.samples {
					refs = append(refs, SampleRef{s.off, int(s.size), float64(s.cts) / float64(t.timescale), s.sync})
				}
				return t.fourcc, refs, nil
			}
		}
	}
	return "", nil, fmt.Errorf("media: track not found")
}
