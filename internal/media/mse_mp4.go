package media

import (
	"fmt"
)

// codedFrame is one frame as a byte-stream parser produces it, before the
// MediaSource coded frame processing (timestampOffset, append window ...).
type codedFrame struct {
	track         int
	dts, pts, dur float64
	key           bool
	data          []byte
}

// initSegment is the parsed initialization segment of a byte stream.
type initSegment struct {
	duration float64 // seconds, 0 when unknown
	tracks   []TrackConfig
}

// segParser turns appended bytes into initialization data and coded frames.
// It keeps incomplete boxes/elements for the next call.
type segParser interface {
	// feed consumes data. It returns a new init segment when one completed,
	// the frames of every media segment (or part of one) that completed.
	feed(data []byte) (*initSegment, []codedFrame, error)
	// reset drops partial input (SourceBuffer.abort).
	reset()
}

// ---- fragmented MP4 -----------------------------------------------------------

type mp4Parser struct {
	pending []byte
	pos     int64 // absolute stream offset of pending[0]
	moov    *MP4Moov
	byID    map[int]*mp4Track
	next    map[int]int64 // next decode time per track when a fragment has no tfdt
	// a moof waits for its mdat
	moof      []byte
	moofStart int64
}

func newMP4Parser() *mp4Parser { return &mp4Parser{byID: map[int]*mp4Track{}, next: map[int]int64{}} }

func (p *mp4Parser) reset() {
	p.pending = nil
	p.moof = nil
}

func (p *mp4Parser) feed(data []byte) (*initSegment, []codedFrame, error) {
	p.pending = append(p.pending, data...)
	var init *initSegment
	var frames []codedFrame
	off := 0
	for {
		buf := p.pending[off:]
		if len(buf) < 8 {
			break
		}
		sz := int64(be32(buf))
		typ := string(buf[4:8])
		hdr := int64(8)
		switch sz {
		case 1:
			if len(buf) < 16 {
				goto done
			}
			sz = int64(be64(buf[8:]))
			hdr = 16
		case 0:
			// box extends to the end of the stream: wait for more data is impossible to detect; take what we have
			sz = int64(len(buf))
		}
		if sz < hdr {
			return init, frames, fmt.Errorf("mse: bad box size in %q", typ)
		}
		if int64(len(buf)) < sz {
			// an enormous box we cannot buffer is an error; mdat may be big but is bounded by the caller's quota
			if sz > 1<<30 {
				return init, frames, fmt.Errorf("mse: box %q too large", typ)
			}
			break
		}
		body := buf[hdr:sz]
		boxStart := p.pos + int64(off)
		switch typ {
		case "moov":
			m, err := parseMoov(body)
			if err != nil {
				return init, frames, err
			}
			p.moov = m
			p.byID = map[int]*mp4Track{}
			var cfgs []TrackConfig
			for _, t := range m.Tracks {
				p.byID[t.ID] = t
				cfgs = append(cfgs, mp4TrackConfig(t))
			}
			init = &initSegment{duration: m.Duration, tracks: cfgs}
			if m.Duration == 0 && m.FragDur > 0 {
				init.duration = m.FragDur
			}
		case "moof":
			if p.moov == nil {
				return init, frames, fmt.Errorf("mse: media segment before initialization segment")
			}
			p.moof = append(p.moof[:0], buf[:sz]...)
			p.moofStart = boxStart
		case "mdat":
			if p.moof != nil {
				fs, err := p.frames(p.moof[hdrOf(p.moof):], p.moofStart, buf[:sz], boxStart, int(hdr))
				if err != nil {
					return init, frames, err
				}
				frames = append(frames, fs...)
				p.moof = nil
			}
		}
		off += int(sz)
	}
done:
	p.pos += int64(off)
	p.pending = append([]byte(nil), p.pending[off:]...)
	if init == nil && p.moov == nil && len(p.pending) == 0 && off == 0 && len(data) > 0 {
		return nil, nil, fmt.Errorf("mse: unrecognised data")
	}
	return init, frames, nil
}

func hdrOf(box []byte) int {
	if be32(box) == 1 {
		return 16
	}
	return 8
}

func mp4TrackConfig(t *mp4Track) TrackConfig {
	c := TrackConfig{ID: t.ID, Kind: t.Kind, Codec: t.Codec, CodecName: t.CodecName,
		Width: t.Width, Height: t.Height, FrameRate: t.FrameRate, SampleRate: t.SampleRate, Channels: t.Channels}
	switch t.Kind {
	case "video":
		c.Cfg = t.vcfg
	case "audio":
		switch t.CodecName {
		case "aac":
			c.Cfg, c.CfgKind = esdsDecoderSpecific(t.cfg), "asc"
		case "opus":
			c.Cfg, c.CfgKind = t.cfg, "dops"
		}
	}
	return c
}

// frames decodes one moof (body without the box header) with the mdat that
// follows it. moofAbs/mdatAbs are absolute stream offsets of the boxes.
func (p *mp4Parser) frames(moof []byte, moofAbs int64, mdat []byte, mdatAbs int64, mdatHdr int) ([]codedFrame, error) {
	var out []codedFrame
	var prevEnd int64 = -1
	for _, tf := range findBoxes(parseBoxes(moof), "traf") {
		kids := parseBoxes(tf.data)
		tfhd := findBox(kids, "tfhd")
		if tfhd == nil || len(tfhd.data) < 8 {
			continue
		}
		flags := be32(tfhd.data) & 0xffffff
		id := int(be32(tfhd.data[4:]))
		trk := p.byID[id]
		if trk == nil || trk.timescale == 0 {
			continue
		}
		q := 8
		base := moofAbs
		haveBase := false
		if flags&1 != 0 && q+8 <= len(tfhd.data) {
			base = int64(be64(tfhd.data[q:]))
			q += 8
			haveBase = true
		}
		if flags&2 != 0 {
			q += 4
		}
		defDur, defSize, defFlags := trk.trex.dur, trk.trex.size, trk.trex.flags
		if flags&8 != 0 && q+4 <= len(tfhd.data) {
			defDur = be32(tfhd.data[q:])
			q += 4
		}
		if flags&0x10 != 0 && q+4 <= len(tfhd.data) {
			defSize = be32(tfhd.data[q:])
			q += 4
		}
		if flags&0x20 != 0 && q+4 <= len(tfhd.data) {
			defFlags = be32(tfhd.data[q:])
		}
		if !haveBase && flags&0x20000 == 0 && prevEnd >= 0 {
			base = prevEnd // old files: data follows the previous traf's
		}
		dts, ok := p.next[id]
		if tfdt := findBox(kids, "tfdt"); tfdt != nil && len(tfdt.data) >= 8 {
			if tfdt.data[0] == 1 && len(tfdt.data) >= 12 {
				dts = int64(be64(tfdt.data[4:]))
			} else {
				dts = int64(be32(tfdt.data[4:]))
			}
		} else if !ok {
			dts = 0
		}
		ts := float64(trk.timescale)
		// an edit list that starts at media_time > 0 (B-frame composition offset, AAC priming) moves
		// the track's timeline so that the first shown frame is at 0, like the other players do
		shift := 0.0
		if trk.editStart > 0 {
			shift = float64(trk.editStart) / ts
		}
		cur := base
		audio := trk.Kind == "audio"
		for _, tr := range findBoxes(kids, "trun") {
			if len(tr.data) < 8 {
				continue
			}
			ver := tr.data[0]
			fl := be32(tr.data) & 0xffffff
			cnt := int(be32(tr.data[4:]))
			q := 8
			if fl&1 != 0 {
				if q+4 > len(tr.data) {
					break
				}
				cur = base + int64(int32(be32(tr.data[q:])))
				q += 4
			}
			firstFlags := defFlags
			hasFirst := false
			if fl&4 != 0 {
				if q+4 > len(tr.data) {
					break
				}
				firstFlags = be32(tr.data[q:])
				hasFirst = true
				q += 4
			}
			for i := 0; i < cnt; i++ {
				dur, sz, sf := defDur, defSize, defFlags
				if i == 0 && hasFirst {
					sf = firstFlags
				}
				var cto int64
				if fl&0x100 != 0 {
					if q+4 > len(tr.data) {
						return out, fmt.Errorf("mse: truncated trun")
					}
					dur = be32(tr.data[q:])
					q += 4
				}
				if fl&0x200 != 0 {
					if q+4 > len(tr.data) {
						return out, fmt.Errorf("mse: truncated trun")
					}
					sz = be32(tr.data[q:])
					q += 4
				}
				if fl&0x400 != 0 {
					if q+4 > len(tr.data) {
						return out, fmt.Errorf("mse: truncated trun")
					}
					sf = be32(tr.data[q:])
					q += 4
				}
				if fl&0x800 != 0 {
					if q+4 > len(tr.data) {
						return out, fmt.Errorf("mse: truncated trun")
					}
					if ver == 0 {
						cto = int64(be32(tr.data[q:]))
					} else {
						cto = int64(int32(be32(tr.data[q:])))
					}
					q += 4
				}
				// payload inside the mdat that follows the moof
				rel := cur - (mdatAbs + int64(mdatHdr))
				if rel < 0 || rel+int64(sz) > int64(len(mdat)-mdatHdr) {
					return out, fmt.Errorf("mse: sample outside mdat (offset %d size %d)", rel, sz)
				}
				payload := mdat[mdatHdr+int(rel) : mdatHdr+int(rel)+int(sz)]
				key := sf&0x10000 == 0
				if audio {
					key = true
				}
				out = append(out, codedFrame{
					track: id, dts: float64(dts)/ts - shift, pts: float64(dts+cto)/ts - shift, dur: float64(dur) / ts,
					key: key, data: append([]byte(nil), payload...),
				})
				cur += int64(sz)
				dts += int64(dur)
			}
		}
		prevEnd = cur
		p.next[id] = dts
	}
	return out, nil
}
