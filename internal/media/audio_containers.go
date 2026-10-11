package media

import (
	"bytes"
	"fmt"
	"image/gif"
	"io"
	"math"
)

// ---- Ogg --------------------------------------------------------------------

type oggStream struct {
	serial    uint32
	kind      string
	codec     string
	name      string
	channels  int
	rate      int
	bitrate   int
	width     int
	height    int
	fps       float64
	preskip   int
	kfgshift  uint
	theoraVer int
	lastGran  int64
	haveLast  bool
}

func probeOgg(r io.ReaderAt, size int64) (*Info, error) {
	info := &Info{Container: "ogg", Duration: -1}
	streams := map[uint32]*oggStream{}
	var order []uint32
	var off int64
	// Read the leading pages: BOS pages hold the identification packets.
	for pages := 0; pages < 64 && off < size; pages++ {
		h, err := readFull(r, off, 27)
		if err != nil || string(h[:4]) != "OggS" {
			break
		}
		htype := h[5]
		serial := le32(h[14:])
		nseg := int(h[26])
		segs, err := readFull(r, off+27, nseg)
		if err != nil {
			break
		}
		plen := 0
		for _, s := range segs {
			plen += int(s)
		}
		if htype&2 != 0 { // BOS
			first := segs[0]
			_ = first
			n := plen
			if n > 128 {
				n = 128
			}
			pkt, _ := readFull(r, off+27+int64(nseg), n)
			st := identifyOgg(pkt)
			if st != nil {
				st.serial = serial
				streams[serial] = st
				order = append(order, serial)
			}
		} else if len(streams) > 0 {
			// header pages of the logical streams are done once data pages start
			allKnown := true
			for _, s := range streams {
				if s.kind == "" {
					allKnown = false
				}
			}
			if allKnown && pages > 2 {
				info.DataStart = off
				break
			}
		}
		if info.DataStart == 0 && htype&2 == 0 {
			info.DataStart = off
		}
		off += 27 + int64(nseg) + int64(plen)
	}
	if len(streams) == 0 {
		return nil, fmt.Errorf("media: ogg without recognised streams")
	}
	// last granule: scan the tail
	tail := int64(128 << 10)
	if tail > size {
		tail = size
	}
	buf, _ := readFull(r, size-tail, int(tail))
	for i := len(buf) - 27; i >= 0; i-- {
		if buf[i] == 'O' && i+27 <= len(buf) && string(buf[i:i+4]) == "OggS" {
			serial := le32(buf[i+14:])
			g := int64(le64(buf[i+6:]))
			if st, ok := streams[serial]; ok && !st.haveLast && g >= 0 {
				st.lastGran = g
				st.haveLast = true
			}
		}
	}
	var dur float64
	for _, s := range order {
		st := streams[s]
		t := Track{ID: int(st.serial), Kind: st.kind, Codec: st.codec, CodecName: st.name, Channels: st.channels, SampleRate: st.rate, BitRate: st.bitrate, Width: st.width, Height: st.height, FrameRate: st.fps, Default: true}
		d := 0.0
		if st.haveLast {
			switch st.name {
			case "opus":
				d = float64(st.lastGran-int64(st.preskip)) / 48000
			case "theora":
				g := st.lastGran
				frames := (g >> st.kfgshift) + (g & ((1 << st.kfgshift) - 1))
				if st.fps > 0 {
					d = float64(frames) / st.fps
				}
			default:
				if st.rate > 0 {
					d = float64(st.lastGran) / float64(st.rate)
				}
			}
		}
		t.Duration = d
		if d > dur {
			dur = d
		}
		info.Tracks = append(info.Tracks, t)
	}
	info.Duration = dur
	if info.Duration <= 0 {
		info.Duration = -1
	}
	return info, nil
}

func identifyOgg(p []byte) *oggStream {
	switch {
	case len(p) >= 30 && p[0] == 1 && string(p[1:7]) == "vorbis":
		return &oggStream{kind: "audio", codec: "vorbis", name: "vorbis", channels: int(p[11]), rate: int(le32(p[12:])), bitrate: int(int32(le32(p[20:])))}
	case len(p) >= 19 && string(p[:8]) == "OpusHead":
		return &oggStream{kind: "audio", codec: "opus", name: "opus", channels: int(p[9]), rate: 48000, preskip: le16(p[10:])}
	case len(p) >= 42 && p[0] == 0x80 && string(p[1:7]) == "theora":
		st := &oggStream{kind: "video", codec: "theora", name: "theora"}
		st.width = be24(p[14:])
		st.height = be24(p[17:])
		frn, frd := be32(p[22:]), be32(p[26:])
		if frd > 0 {
			st.fps = round3(float64(frn) / float64(frd))
		}
		v := uint(be16(p[40:]))
		st.kfgshift = (v >> 5) & 0x1f
		return st
	case len(p) >= 13 && p[0] == 0x7f && string(p[1:5]) == "FLAC":
		st := &oggStream{kind: "audio", codec: "flac", name: "flac"}
		if len(p) >= 51 {
			si := p[13+4+0:]
			_ = si
			// STREAMINFO starts at 13+4: skip header(4) to reach fields
			sr := (uint32(p[27])<<12 | uint32(p[28])<<4 | uint32(p[29])>>4)
			st.rate = int(sr)
			st.channels = int(p[29]>>1&7) + 1
		}
		return st
	case len(p) >= 8 && string(p[:8]) == "Speex   ":
		return &oggStream{kind: "audio", codec: "speex", name: "speex"}
	}
	return &oggStream{kind: "other"}
}

// ---- WAV -------------------------------------------------------------------

type WAVFormat struct {
	Tag, Channels, SampleRate, BlockAlign, Bits int
	DataOff, DataLen                            int64
}

func parseWAV(r io.ReaderAt, size int64) (*WAVFormat, error) {
	var f WAVFormat
	off := int64(12)
	haveFmt, haveData := false, false
	for off+8 <= size {
		h, err := readFull(r, off, 8)
		if err != nil {
			break
		}
		id := string(h[:4])
		sz := int64(le32(h[4:]))
		body := off + 8
		switch id {
		case "fmt ":
			d, err := readFull(r, body, int(minI64(sz, 40)))
			if err != nil || len(d) < 16 {
				return nil, fmt.Errorf("media: bad wav fmt")
			}
			f.Tag = le16(d)
			f.Channels = le16(d[2:])
			f.SampleRate = int(le32(d[4:]))
			f.BlockAlign = le16(d[12:])
			f.Bits = le16(d[14:])
			if f.Tag == 0xFFFE && len(d) >= 26 {
				f.Tag = le16(d[24:])
			}
			haveFmt = true
		case "data":
			f.DataOff = body
			f.DataLen = sz
			if sz == 0xFFFFFFFF || body+sz > size || sz == 0 {
				f.DataLen = size - body
			}
			haveData = true
		}
		if haveFmt && haveData {
			break
		}
		off = body + sz + sz&1
		if id == "data" {
			break
		}
	}
	if !haveFmt || !haveData || f.Channels == 0 || f.SampleRate == 0 {
		return nil, fmt.Errorf("media: incomplete wav")
	}
	return &f, nil
}

func probeWAV(r io.ReaderAt, size int64) (*Info, error) {
	f, err := parseWAV(r, size)
	if err != nil {
		return nil, err
	}
	name := "pcm_s16le"
	switch f.Tag {
	case 1:
		switch f.Bits {
		case 8:
			name = "pcm_u8"
		case 16:
			name = "pcm_s16le"
		case 24:
			name = "pcm_s24le"
		case 32:
			name = "pcm_s32le"
		}
	case 3:
		name = "pcm_f32le"
		if f.Bits == 64 {
			name = "pcm_f64le"
		}
	case 6:
		name = "pcm_alaw"
	case 7:
		name = "pcm_mulaw"
	default:
		name = fmt.Sprintf("wav_0x%x", f.Tag)
	}
	codec := fmt.Sprint(f.Tag)
	dur := 0.0
	if f.BlockAlign > 0 {
		dur = float64(f.DataLen/int64(f.BlockAlign)) / float64(f.SampleRate)
	}
	return &Info{Container: "wav", Duration: dur, DataStart: f.DataOff, DataEnd: f.DataOff + f.DataLen,
		Tracks: []Track{{ID: 1, Kind: "audio", Codec: codec, CodecName: name, Channels: f.Channels, SampleRate: f.SampleRate, BitsPerSample: f.Bits, BitRate: f.SampleRate * f.BlockAlign * 8, Duration: dur, Default: true}}}, nil
}

// ---- MP3 / AAC (ADTS) --------------------------------------------------------

var mp3Bitrates = [2][3][16]int{
	{ // MPEG1: L1, L2, L3
		{0, 32, 64, 96, 128, 160, 192, 224, 256, 288, 320, 352, 384, 416, 448, 0},
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 384, 0},
		{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0},
	},
	{ // MPEG2/2.5
		{0, 32, 48, 56, 64, 80, 96, 112, 128, 144, 160, 176, 192, 224, 256, 0},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
		{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0},
	},
}
var mp3Rates = [3][3]int{{44100, 48000, 32000}, {22050, 24000, 16000}, {11025, 12000, 8000}}

type mp3Header struct {
	version, layer int // version: 0 = MPEG1, 1 = MPEG2, 2 = MPEG2.5; layer 1..3
	bitrate, rate  int
	channels       int
	frameLen, spf  int
	padding        int
	protection     bool
}

func isMP3Sync(b []byte) bool {
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return false
	}
	_, ok := parseMP3Header(b)
	return ok
}

func parseMP3Header(b []byte) (mp3Header, bool) {
	var h mp3Header
	if len(b) < 4 || b[0] != 0xFF || b[1]&0xE0 != 0xE0 {
		return h, false
	}
	vbits := int(b[1]>>3) & 3
	lbits := int(b[1]>>1) & 3
	if vbits == 1 || lbits == 0 {
		return h, false
	}
	switch vbits {
	case 3:
		h.version = 0
	case 2:
		h.version = 1
	case 0:
		h.version = 2
	}
	h.layer = 4 - lbits
	bi := int(b[2] >> 4)
	ri := int(b[2]>>2) & 3
	if bi == 0 || bi == 15 || ri == 3 {
		return h, false
	}
	vi := 0
	if h.version > 0 {
		vi = 1
	}
	h.bitrate = mp3Bitrates[vi][h.layer-1][bi] * 1000
	h.rate = mp3Rates[h.version][ri]
	h.padding = int(b[2]>>1) & 1
	h.channels = 2
	if b[3]>>6 == 3 {
		h.channels = 1
	}
	h.protection = b[1]&1 == 0
	switch h.layer {
	case 1:
		h.spf = 384
		h.frameLen = (12*h.bitrate/h.rate + h.padding) * 4
	case 2:
		h.spf = 1152
		h.frameLen = 144*h.bitrate/h.rate + h.padding
	default:
		if h.version == 0 {
			h.spf = 1152
			h.frameLen = 144*h.bitrate/h.rate + h.padding
		} else {
			h.spf = 576
			h.frameLen = 72*h.bitrate/h.rate + h.padding
		}
	}
	return h, h.frameLen > 4
}

func isADTS(b []byte) bool {
	return len(b) >= 7 && b[0] == 0xFF && b[1]&0xF6 == 0xF0 && (b[2]>>2)&0xF < 13
}

var adtsRates = []int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}

func probeMPEGAudio(r io.ReaderAt, size int64) (*Info, error) {
	start := int64(0)
	head, _ := readFull(r, 0, 10)
	if len(head) >= 10 && string(head[:3]) == "ID3" {
		sz := int64(head[6]&0x7f)<<21 | int64(head[7]&0x7f)<<14 | int64(head[8]&0x7f)<<7 | int64(head[9]&0x7f)
		start = 10 + sz
		if head[5]&0x10 != 0 {
			start += 10
		}
	}
	// find first frame
	scanLen := int64(256 << 10)
	if start+scanLen > size {
		scanLen = size - start
	}
	if scanLen <= 4 {
		return nil, ErrUnknownFormat
	}
	buf, _ := readFull(r, start, int(scanLen))
	for i := 0; i+8 < len(buf); i++ {
		if buf[i] != 0xFF {
			continue
		}
		if isADTS(buf[i:]) && !(buf[i+1]&0x06 != 0) {
			if inf := probeADTS(r, size, start+int64(i)); inf != nil {
				return inf, nil
			}
		}
		h, ok := parseMP3Header(buf[i:])
		if !ok || h.layer != 3 && h.layer != 2 && h.layer != 1 {
			continue
		}
		// confirm with the next frame header
		nxt := i + h.frameLen
		if nxt+4 <= len(buf) {
			h2, ok2 := parseMP3Header(buf[nxt:])
			if !ok2 || h2.rate != h.rate || h2.layer != h.layer {
				continue
			}
		}
		return mp3FromFrame(r, size, start+int64(i), h), nil
	}
	return nil, ErrUnknownFormat
}

func mp3FromFrame(r io.ReaderAt, size, first int64, h mp3Header) *Info {
	info := &Info{Container: "mp3", DataStart: first, DataEnd: size}
	// ID3v1 tail
	if t, err := readFull(r, size-128, 3); err == nil && string(t) == "TAG" {
		info.DataEnd = size - 128
	}
	name := "mp3"
	codec := "mp3"
	switch h.layer {
	case 1:
		name, codec = "mp1", "mp4a.6B"
	case 2:
		name, codec = "mp2", "mp4a.6B"
	}
	frames := int64(0)
	var toc []byte
	var xingBytes int64
	// Xing/Info/VBRI
	side := 32
	if h.version == 0 && h.channels == 1 {
		side = 17
	} else if h.version != 0 && h.channels == 2 {
		side = 17
	} else if h.version != 0 && h.channels == 1 {
		side = 9
	}
	fb, _ := readFull(r, first, minInt(h.frameLen, 4096))
	if len(fb) > 4+side+16 {
		o := 4 + side
		if h.protection {
			o += 2
		}
		if o+16 < len(fb) {
			tag := string(fb[o : o+4])
			if tag == "Xing" || tag == "Info" {
				flags := be32(fb[o+4:])
				p := o + 8
				if flags&1 != 0 && p+4 <= len(fb) {
					frames = int64(be32(fb[p:]))
					p += 4
				}
				if flags&2 != 0 && p+4 <= len(fb) {
					xingBytes = int64(be32(fb[p:]))
					p += 4
				}
				if flags&4 != 0 && p+100 <= len(fb) {
					toc = fb[p : p+100]
				}
				info.DataStart = first + int64(h.frameLen) // skip the info frame
				info.XingFrame = true
				if o+144 <= len(fb) && (string(fb[o+120:o+124]) == "LAME" || string(fb[o+120:o+124]) == "Lavc" || string(fb[o+120:o+124]) == "Lavf") {
					info.SkipStart = int(fb[o+141])<<4 | int(fb[o+142])>>4
					info.SkipEnd = int(fb[o+142]&0xf)<<8 | int(fb[o+143])
				}
			}
		}
		if string(fb[4+32:4+32+4]) == "VBRI" && len(fb) >= 4+32+18 {
			xingBytes = int64(be32(fb[4+32+10:]))
			frames = int64(be32(fb[4+32+14:]))
			info.DataStart = first + int64(h.frameLen)
		}
	}
	br := h.bitrate
	if frames > 0 {
		info.Duration = float64(frames) * float64(h.spf) / float64(h.rate)
		if xingBytes == 0 {
			xingBytes = info.DataEnd - info.DataStart
		}
		if info.Duration > 0 {
			br = int(float64(xingBytes) * 8 / info.Duration)
		}
		if toc != nil {
			span := info.DataEnd - info.DataStart
			for i := 0; i < 100; i++ {
				info.Seek = append(info.Seek, SeekPoint{Time: info.Duration * float64(i) / 100, Offset: info.DataStart + int64(float64(toc[i])/256*float64(span))})
			}
		}
	} else if h.bitrate > 0 {
		info.Duration = float64(info.DataEnd-first) * 8 / float64(h.bitrate)
	}
	info.Tracks = []Track{{ID: 1, Kind: "audio", Codec: codec, CodecName: name, Channels: h.channels, SampleRate: h.rate, BitRate: br, Duration: info.Duration, Default: true}}
	return info
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func probeADTS(r io.ReaderAt, size, first int64) *Info {
	hd, _ := readFull(r, first, 7)
	if !isADTS(hd) {
		return nil
	}
	profile := int(hd[2]>>6) + 1
	sri := int(hd[2]>>2) & 0xf
	ch := int(hd[2]&1)<<2 | int(hd[3]>>6)
	// walk frames; cap the walk and extrapolate on huge files
	pos := first
	frames := 0
	walkEnd := size
	capped := false
	if size-first > 4<<20 {
		walkEnd = first + 4<<20
		capped = true
	}
	for pos+7 <= walkEnd {
		h, err := readFull(r, pos, 7)
		if err != nil || !isADTS(h) {
			break
		}
		fl := int(h[3]&3)<<11 | int(h[4])<<3 | int(h[5])>>5
		if fl < 7 {
			break
		}
		pos += int64(fl)
		frames++
	}
	if frames < 2 {
		return nil
	}
	if capped {
		frames = int(float64(frames) * float64(size-first) / float64(pos-first))
	}
	rate := 44100
	if sri < len(adtsRates) {
		rate = adtsRates[sri]
	}
	dur := float64(frames) * 1024 / float64(rate)
	return &Info{Container: "aac", Duration: dur, DataStart: first, DataEnd: size,
		Tracks: []Track{{ID: 1, Kind: "audio", Codec: fmt.Sprintf("mp4a.40.%d", profile), CodecName: "aac", Channels: ch, SampleRate: rate, Duration: dur, Default: true}}}
}

// ---- FLAC ----------------------------------------------------------------------

type FLACStreamInfo struct {
	MinBlock, MaxBlock int
	SampleRate         int
	Channels, Bits     int
	TotalSamples       int64
	FirstFrame         int64
	Seek               []SeekPoint
}

func parseFLAC(r io.ReaderAt, size int64) (*FLACStreamInfo, error) {
	var si FLACStreamInfo
	off := int64(4)
	have := false
	var seekRaw []byte
	for off+4 <= size {
		h, err := readFull(r, off, 4)
		if err != nil {
			break
		}
		last := h[0]&0x80 != 0
		typ := h[0] & 0x7f
		ln := int64(be24(h[1:]))
		if typ == 0 && ln >= 34 {
			d, err := readFull(r, off+4, 34)
			if err != nil {
				return nil, err
			}
			si.MinBlock = be16(d)
			si.MaxBlock = be16(d[2:])
			v := be64(d[10:])
			si.SampleRate = int(v >> 44)
			si.Channels = int(v>>41&7) + 1
			si.Bits = int(v>>36&31) + 1
			si.TotalSamples = int64(v & (1<<36 - 1))
			have = true
		} else if typ == 3 && ln < 1<<20 {
			seekRaw, _ = readFull(r, off+4, int(ln))
		}
		off += 4 + ln
		if last {
			break
		}
	}
	if !have || si.SampleRate == 0 {
		return nil, fmt.Errorf("media: flac without STREAMINFO")
	}
	si.FirstFrame = off
	for i := 0; i+18 <= len(seekRaw); i += 18 {
		sn := be64(seekRaw[i:])
		if sn == math.MaxUint64 {
			continue
		}
		si.Seek = append(si.Seek, SeekPoint{Time: float64(sn) / float64(si.SampleRate), Offset: off + int64(be64(seekRaw[i+8:]))})
	}
	return &si, nil
}

func probeFLAC(r io.ReaderAt, size int64) (*Info, error) {
	si, err := parseFLAC(r, size)
	if err != nil {
		return nil, err
	}
	dur := 0.0
	if si.TotalSamples > 0 {
		dur = float64(si.TotalSamples) / float64(si.SampleRate)
	}
	return &Info{Container: "flac", Duration: dur, DataStart: si.FirstFrame, DataEnd: size, Seek: si.Seek,
		Tracks: []Track{{ID: 1, Kind: "audio", Codec: "flac", CodecName: "flac", Channels: si.Channels, SampleRate: si.SampleRate, BitsPerSample: si.Bits, Duration: dur, Default: true}}}, nil
}

// ---- AVI (MJPEG) -----------------------------------------------------------------

func probeAVI(r io.ReaderAt, size int64) (*Info, error) {
	buf, err := readFull(r, 0, int(minI64(size, 64<<10)))
	if err != nil && len(buf) < 100 {
		return nil, err
	}
	info := &Info{Container: "avi", Duration: -1}
	var usPerFrame, total uint32
	var w, h uint32
	i := bytes.Index(buf, []byte("avih"))
	if i >= 0 && i+8+40 <= len(buf) {
		a := buf[i+8:]
		usPerFrame = le32(a)
		total = le32(a[16:])
		w, h = le32(a[32:]), le32(a[36:])
	}
	// stream headers
	p := 0
	for {
		j := bytes.Index(buf[p:], []byte("strh"))
		if j < 0 {
			break
		}
		j += p
		if j+8+56 > len(buf) {
			break
		}
		s := buf[j+8:]
		typ, handler := string(s[:4]), string(s[4:8])
		scale, rate, length := le32(s[20:]), le32(s[24:]), le32(s[32:])
		t := Track{ID: len(info.Tracks) + 1, Default: true}
		switch typ {
		case "vids":
			t.Kind = "video"
			t.CodecName = "mjpeg"
			t.Codec = handler
			if handler != "MJPG" && handler != "mjpg" {
				t.CodecName = handler
			}
			t.Width, t.Height = int(w), int(h)
			if scale > 0 {
				t.FrameRate = round3(float64(rate) / float64(scale))
				t.Duration = float64(length) * float64(scale) / float64(rate)
			}
		case "auds":
			t.Kind = "audio"
			t.CodecName = "pcm"
			if rate > 0 {
				t.Duration = float64(length) * float64(scale) / float64(rate)
			}
		default:
			p = j + 4
			continue
		}
		info.Tracks = append(info.Tracks, t)
		p = j + 4
	}
	if usPerFrame > 0 && total > 0 {
		info.Duration = float64(usPerFrame) * float64(total) / 1e6
	} else if v := info.Video(); v != nil {
		info.Duration = v.Duration
	}
	if len(info.Tracks) == 0 {
		return nil, fmt.Errorf("media: avi without streams")
	}
	if i := bytes.Index(buf, []byte("movi")); i >= 0 {
		info.DataStart = int64(i + 4)
	}
	return info, nil
}

// ---- GIF -----------------------------------------------------------------------------

func probeGIF(r io.ReaderAt, size int64) (*Info, error) {
	if size > 64<<20 {
		return nil, fmt.Errorf("media: gif too large")
	}
	b, err := readFull(r, 0, int(size))
	if err != nil {
		return nil, err
	}
	g, err := gif.DecodeAll(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	dur := 0.0
	for _, d := range g.Delay {
		dur += float64(d) / 100
	}
	w, h := g.Config.Width, g.Config.Height
	return &Info{Container: "gif", Duration: dur, DataStart: 0, DataEnd: size,
		Tracks: []Track{{ID: 1, Kind: "video", Codec: "gif", CodecName: "gif", Width: w, Height: h, Frames: len(g.Image), FrameRate: round3(float64(len(g.Image)) / math.Max(dur, 0.01)), Duration: dur, Default: true}}}, nil
}
