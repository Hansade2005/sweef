package media

import (
	"fmt"
)

// audioStream is the codec configuration and compressed access units of the
// first audio track of a complete resource, as handed to the native decoders.
type audioStream struct {
	codec    string // ffprobe style codec name: opus, aac
	cfg      []byte // OpusHead / dOps / AudioSpecificConfig, container dependent (see cfgKind)
	cfgKind  string // "opushead", "dops", "asc", ""
	packets  [][]byte
	lastGran int64 // ogg: granule position of the last page (-1 when unknown)
	skip     int   // mp4 edit-list start (samples at the track rate), -1 unknown
	endTrim  int   // webm DiscardPadding at 48 kHz
	editDur  int   // mp4 edit-list duration in samples, -1 unknown
}

// ---- Ogg ---------------------------------------------------------------------

// oggPackets reassembles the packets of the first logical stream whose first
// packet starts with magic. The identification and comment packets are
// returned separately (hdr, up to nHdr packets).
func oggPackets(data []byte, magic string, nHdr int) (hdr [][]byte, pkts [][]byte, lastGran int64, err error) {
	lastGran = -1
	var serial uint32
	have := false
	var cur []byte
	off := 0
	for off+27 <= len(data) {
		if string(data[off:off+4]) != "OggS" {
			// resync
			n := off + 1
			for n+4 <= len(data) && string(data[n:n+4]) != "OggS" {
				n++
			}
			off = n
			continue
		}
		htype := data[off+5]
		gran := int64(le64(data[off+6:]))
		ser := le32(data[off+14:])
		nseg := int(data[off+26])
		if off+27+nseg > len(data) {
			break
		}
		segs := data[off+27 : off+27+nseg]
		body := off + 27 + nseg
		total := 0
		for _, s := range segs {
			total += int(s)
		}
		if body+total > len(data) {
			total = len(data) - body
		}
		if !have && htype&2 != 0 && total >= len(magic) && string(data[body:body+len(magic)]) == magic {
			serial, have = ser, true
		}
		if have && ser == serial {
			if htype&1 == 0 && len(cur) > 0 {
				cur = cur[:0]
			}
			p := body
			for _, s := range segs {
				if p+int(s) > len(data) {
					break
				}
				cur = append(cur, data[p:p+int(s)]...)
				p += int(s)
				if s < 255 {
					pk := append([]byte(nil), cur...)
					cur = cur[:0]
					if len(hdr) < nHdr {
						hdr = append(hdr, pk)
					} else {
						pkts = append(pkts, pk)
					}
				}
			}
			if gran >= 0 {
				lastGran = gran
			}
		}
		off = body + total
	}
	if !have {
		return nil, nil, -1, fmt.Errorf("media: ogg stream %q not found", magic)
	}
	return hdr, pkts, lastGran, nil
}

// ---- Matroska / WebM -----------------------------------------------------------

func webmAudio(data []byte) (*audioStream, error) {
	p := 0
	var body []byte
	for p < len(data) {
		id, il, sz, sl, ok := parseElHeader(data[p:])
		if !ok {
			break
		}
		st := p + il + sl
		if id == idSegment {
			en := st + int(sz)
			if sz < 0 || en > len(data) {
				en = len(data)
			}
			body = data[st:en]
			break
		}
		if sz < 0 {
			break
		}
		p = st + int(sz)
	}
	if body == nil {
		return nil, fmt.Errorf("media: no Segment")
	}
	var tracks []*mkvTrack
	scale := uint64(1000000)
	q := 0
	for q < len(body) {
		id, il, sz, sl, ok := parseElHeader(body[q:])
		if !ok {
			break
		}
		st := q + il + sl
		en := st + int(sz)
		if sz < 0 || en > len(body) {
			en = len(body)
		}
		switch id {
		case idTracks:
			tracks = parseMkvTracks(body[st:en])
		case idInfo:
			mi := &mkvInfo{}
			parseMkvInfo(body[st:en], mi)
			scale = mi.timecodeScale
		case idCluster:
			q = len(body)
			continue
		}
		q = en
	}
	var tr *mkvTrack
	for _, t := range tracks {
		if t.Kind == "audio" {
			tr = t
			break
		}
	}
	if tr == nil {
		return nil, fmt.Errorf("media: no audio track")
	}
	as := &audioStream{codec: tr.CodecName, lastGran: -1, skip: -1, editDur: -1}
	switch tr.CodecName {
	case "opus":
		as.cfg, as.cfgKind = tr.priv, "opushead"
	case "aac":
		as.cfg, as.cfgKind = tr.priv, "asc"
	}
	// DiscardPadding (0x75A2, ns) of the last block group trims the end
	for q := 0; q < len(body); {
		id, il, sz, sl, ok := parseElHeader(body[q:])
		if !ok {
			break
		}
		st := q + il + sl
		en := st + int(sz)
		if sz < 0 || en > len(body) {
			en = len(body)
		}
		if id == idCluster {
			for _, e := range ebmlChildrenShallow(body[st:en]) {
				if e.id != idBlockGroup {
					continue
				}
				for _, g := range ebmlChildren(e.data) {
					if g.id == 0x75A2 && len(g.data) > 0 && len(g.data) <= 8 {
						v := int64(0)
						for _, c := range g.data {
							v = v<<8 | int64(c)
						}
						if g.data[0]&0x80 != 0 { // signed
							v -= 1 << (8 * uint(len(g.data)))
						}
						as.endTrim = int(v * 48 / 1000000)
					}
				}
			}
		}
		q = en
	}
	for _, b := range ParseWebMBlocksScale(body, scale) {
		if b.Track == tr.num {
			as.packets = append(as.packets, b.Data)
		}
	}
	return as, nil
}

// ---- MP4 ---------------------------------------------------------------------

func mp4Audio(data []byte) (*audioStream, error) {
	boxes := parseBoxes(data)
	mb := findBox(boxes, "moov")
	if mb == nil {
		return nil, fmt.Errorf("media: no moov")
	}
	moov, err := parseMoov(mb.data)
	if err != nil {
		return nil, err
	}
	var tr *mp4Track
	for _, t := range moov.Tracks {
		if t.Kind == "audio" {
			tr = t
			break
		}
	}
	if tr == nil {
		return nil, fmt.Errorf("media: no audio track")
	}
	as := &audioStream{codec: tr.CodecName, cfg: tr.cfg, lastGran: -1, skip: tr.editStart, editDur: -1}
	if tr.editDur >= 0 && moov.Timescale > 0 && tr.SampleRate > 0 {
		as.editDur = int(int64(tr.editDur) * int64(tr.SampleRate) / int64(moov.Timescale))
	}
	switch tr.CodecName {
	case "opus":
		as.cfgKind = "dops"
	case "aac":
		as.cfgKind = "asc"
		as.cfg = esdsDecoderSpecific(tr.cfg)
	}
	add := func(off int64, size int) {
		if off >= 0 && size >= 0 && off+int64(size) <= int64(len(data)) {
			as.packets = append(as.packets, data[off:off+int64(size)])
		}
	}
	for _, s := range tr.samples {
		add(s.off, int(s.size))
	}
	if len(tr.samples) == 0 {
		for _, bx := range boxes {
			if bx.typ != "moof" {
				continue
			}
			for _, tf := range findBoxes(parseBoxes(bx.data), "traf") {
				kids := parseBoxes(tf.data)
				tfhd := findBox(kids, "tfhd")
				if tfhd == nil || len(tfhd.data) < 8 || int(be32(tfhd.data[4:])) != tr.ID {
					continue
				}
				fl := be32(tfhd.data) & 0xffffff
				pp := 8
				base := bx.start
				if fl&1 != 0 && pp+8 <= len(tfhd.data) {
					base = int64(be64(tfhd.data[pp:]))
					pp += 8
				}
				if fl&2 != 0 {
					pp += 4
				}
				if fl&8 != 0 {
					pp += 4
				}
				defSize := tr.trex.size
				if fl&0x10 != 0 && pp+4 <= len(tfhd.data) {
					defSize = be32(tfhd.data[pp:])
				}
				next := int64(-1)
				for _, trun := range findBoxes(kids, "trun") {
					d := trun.data
					if len(d) < 8 {
						continue
					}
					f := be32(d) & 0xffffff
					cnt := int(be32(d[4:]))
					q := 8
					pos := next
					if f&1 != 0 && q+4 <= len(d) {
						pos = base + int64(int32(be32(d[q:])))
						q += 4
					}
					if pos < 0 {
						pos = base
					}
					if f&4 != 0 {
						q += 4
					}
					for i := 0; i < cnt; i++ {
						if f&0x100 != 0 {
							q += 4
						}
						sz := defSize
						if f&0x200 != 0 {
							if q+4 > len(d) {
								break
							}
							sz = be32(d[q:])
							q += 4
						}
						if f&0x400 != 0 {
							q += 4
						}
						if f&0x800 != 0 {
							q += 4
						}
						add(pos, int(sz))
						pos += int64(sz)
					}
					next = pos
				}
			}
		}
	}
	return as, nil
}

// esdsDecoderSpecific returns the DecoderSpecificInfo (AudioSpecificConfig)
// of an esds payload (after the version/flags word).
func esdsDecoderSpecific(b []byte) []byte {
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
		l, np := readLen(p)
		p = np
		switch tag {
		case 3:
			if p+3 > len(b) {
				return nil
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
		case 4:
			p += 13
		case 5:
			if p+l > len(b) {
				l = len(b) - p
			}
			if l < 0 {
				return nil
			}
			return b[p : p+l]
		default:
			p += l
		}
	}
	return nil
}

// loadAudioStream extracts the first audio track from a container.
func loadAudioStream(data []byte, info *Info) (*audioStream, error) {
	switch info.Container {
	case "mp4":
		return mp4Audio(data)
	case "webm", "matroska":
		return webmAudio(data)
	case "ogg":
		if a := info.Audio(); a != nil && a.CodecName == "opus" {
			hdr, pk, gran, err := oggPackets(data, "OpusHead", 2)
			if err != nil || len(hdr) < 1 {
				return nil, fmt.Errorf("media: bad ogg opus")
			}
			return &audioStream{codec: "opus", cfg: hdr[0], cfgKind: "opushead", packets: pk, lastGran: gran, skip: -1, editDur: -1}, nil
		}
	case "aac":
		return adtsAudio(data)
	}
	return nil, ErrUnsupported
}

// adtsAudio splits an ADTS stream into raw access units and synthesises the
// AudioSpecificConfig from the first header.
func adtsAudio(data []byte) (*audioStream, error) {
	p := 0
	if len(data) >= 10 && string(data[:3]) == "ID3" {
		p = 10 + (int(data[6]&0x7f)<<21 | int(data[7]&0x7f)<<14 | int(data[8]&0x7f)<<7 | int(data[9]&0x7f))
	}
	as := &audioStream{codec: "aac", cfgKind: "asc", lastGran: -1, skip: -1, editDur: -1}
	for p+7 <= len(data) {
		if data[p] != 0xff || data[p+1]&0xf6 != 0xf0 {
			p++
			continue
		}
		fl := int(data[p+3]&3)<<11 | int(data[p+4])<<3 | int(data[p+5])>>5
		hl := 7
		if data[p+1]&1 == 0 {
			hl = 9
		}
		if fl < hl || p+fl > len(data) {
			break
		}
		if data[p+6]&3 != 0 {
			return nil, ErrUnsupported // several raw data blocks per frame
		}
		if as.cfg == nil {
			aot := int(data[p+2]>>6) + 1
			sfi := int(data[p+2]>>2) & 0xf
			ch := int(data[p+2]&1)<<2 | int(data[p+3]>>6)
			v := aot<<11 | sfi<<7 | ch<<3
			as.cfg = []byte{byte(v >> 8), byte(v)}
		}
		as.packets = append(as.packets, data[p+hl:p+fl])
		p += fl
	}
	if len(as.packets) == 0 {
		return nil, ErrUnsupported
	}
	return as, nil
}
