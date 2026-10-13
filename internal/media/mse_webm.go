package media

import (
	"fmt"
)

// webmParser reads a WebM byte stream incrementally: the head (EBML header,
// Segment, Info, Tracks) is the initialization segment, every Cluster after
// it is a media segment. Clusters of unknown size and clusters delivered in
// several appends are handled block by block.
type webmParser struct {
	pending  []byte
	haveInit bool
	scale    uint64
	duration float64
	tracks   map[int]*mkvTrack
	order    []int

	inSegment bool
	inCluster bool
	clusterTC uint64
	clusterEn int // bytes of the cluster body still to come; -1 when its size is unknown

	last map[int]*codedFrame // one frame per track held back until its duration is known
	dur  map[int]float64     // last known duration per track
}

func newWebMParser() *webmParser {
	return &webmParser{scale: 1000000, tracks: map[int]*mkvTrack{}, last: map[int]*codedFrame{}, dur: map[int]float64{}}
}

func (p *webmParser) reset() {
	p.pending = nil
	p.inCluster = false
	p.last = map[int]*codedFrame{}
}

func (p *webmParser) feed(data []byte) (*initSegment, []codedFrame, error) {
	p.pending = append(p.pending, data...)
	var init *initSegment
	var frames []codedFrame
	off := 0
	for off < len(p.pending) {
		buf := p.pending[off:]
		id, il, sz, sl, ok := parseElHeader(buf)
		if !ok {
			if len(buf) >= 4+8 {
				return init, frames, fmt.Errorf("mse: malformed WebM element")
			}
			break
		}
		hdr := il + sl
		if !p.haveInit {
			switch id {
			case idEBML:
				if sz < 0 || len(buf) < hdr+int(sz) {
					goto done
				}
				off += hdr + int(sz)
				continue
			case idSegment:
				p.inSegment = true
				off += hdr
				continue
			case idInfo, idTracks:
				if sz < 0 || len(buf) < hdr+int(sz) {
					goto done
				}
				body := buf[hdr : hdr+int(sz)]
				if id == idInfo {
					mi := &mkvInfo{timecodeScale: 1000000}
					parseMkvInfo(body, mi)
					p.scale = mi.timecodeScale
					p.duration = mi.seconds(mi.duration)
				} else {
					for _, t := range parseMkvTracks(body) {
						p.tracks[t.num] = t
						p.order = append(p.order, t.num)
					}
					// the Tracks element completes the initialization segment
					p.haveInit = true
					init = p.initSegment()
				}
				off += hdr + int(sz)
				continue
			case idCluster:
				return init, frames, fmt.Errorf("mse: WebM cluster before tracks")
			default:
				if id == 0x1A45DFA3 || sz < 0 {
					return init, frames, fmt.Errorf("mse: unexpected WebM element %x", id)
				}
				if len(buf) < hdr+int(sz) {
					goto done
				}
				off += hdr + int(sz) // SeekHead, Void, Cues ...
				continue
			}
		}
		if !p.inCluster {
			if id == idCluster {
				p.inCluster = true
				p.clusterTC = 0
				if sz < 0 {
					p.clusterEn = -1
				} else {
					p.clusterEn = int(sz)
				}
				off += hdr
				continue
			}
			// a later EBML header means a new init segment (re-init)
			if id == idEBML {
				p.haveInit = false
				p.inSegment = false
				p.tracks = map[int]*mkvTrack{}
				p.order = nil
				continue
			}
			if sz < 0 || len(buf) < hdr+int(sz) {
				goto done
			}
			off += hdr + int(sz)
			continue
		}
		// inside a cluster
		if p.clusterEn < 0 && (id == idCluster || id == idCues || id == 0x1254C367 /* Tags */ || id == idSeekHead || id == idTracks || id == idInfo) {
			p.endCluster(&frames)
			continue
		}
		if sz < 0 {
			return init, frames, fmt.Errorf("mse: unknown-size element %x in cluster", id)
		}
		if len(buf) < hdr+int(sz) {
			break
		}
		body := buf[hdr : hdr+int(sz)]
		switch id {
		case idClusterTC:
			p.clusterTC = ebmlUint(body)
		case idSimpleBlock:
			if blk, ok := decodeBlock(body); ok {
				p.block(&frames, blk, blk.Key, 0, int64(p.clusterTC))
			}
		case idBlockGroup:
			var blk WebMBlock
			have := false
			var dur float64
			ref := false
			for _, g := range ebmlChildren(body) {
				switch g.id {
				case idBlock:
					blk, have = decodeBlock(g.data)
				case idBlockDur:
					dur = float64(ebmlUint(g.data))
				case 0xFB:
					ref = true
				}
			}
			if have {
				p.block(&frames, blk, !ref, dur, int64(p.clusterTC))
			}
		}
		off += hdr + int(sz)
		if p.clusterEn >= 0 {
			p.clusterEn -= hdr + int(sz)
			if p.clusterEn <= 0 {
				p.endCluster(&frames)
			}
		}
	}
done:
	p.pending = append([]byte(nil), p.pending[off:]...)
	// hold-back frames are released at the end of every append so buffered
	// ranges include them; their duration is an estimate until the next one
	p.flush(&frames)
	return init, frames, nil
}

func (p *webmParser) endCluster(out *[]codedFrame) {
	p.inCluster = false
	p.flush(out)
}

func (p *webmParser) initSegment() *initSegment {
	in := &initSegment{duration: p.duration}
	for _, num := range p.order {
		t := p.tracks[num]
		c := TrackConfig{ID: t.num, Kind: t.Kind, Codec: t.Codec, CodecName: t.CodecName, Width: t.Width, Height: t.Height,
			FrameRate: t.FrameRate, SampleRate: t.SampleRate, Channels: t.Channels, Cfg: t.priv}
		switch t.CodecName {
		case "opus":
			c.CfgKind = "opushead"
		case "aac":
			c.CfgKind = "asc"
		case "vorbis":
			c.CfgKind = "vorbis"
		}
		in.tracks = append(in.tracks, c)
	}
	return in
}

// block queues one block; the previous block of the track is completed with
// the time difference as its duration.
func (p *webmParser) block(out *[]codedFrame, b WebMBlock, key bool, durUnits float64, tc int64) {
	t := p.tracks[b.Track]
	if t == nil || t.Kind == "text" || t.Kind == "other" {
		return
	}
	at := (float64(tc) + b.Time) * float64(p.scale) / 1e9
	f := &codedFrame{track: b.Track, dts: at, pts: at, key: key || t.Kind == "audio", data: append([]byte(nil), b.Data...)}
	if durUnits > 0 {
		f.dur = durUnits * float64(p.scale) / 1e9
	} else if t.defDurNs > 0 {
		f.dur = float64(t.defDurNs) / 1e9
	} else if t.CodecName == "opus" && len(b.Data) > 0 {
		f.dur = float64(opusPacketSamples(b.Data)) / 48000
	}
	if prev := p.last[b.Track]; prev != nil {
		if prev.dur == 0 {
			if d := at - prev.pts; d > 0 {
				prev.dur = d
			}
		}
		p.finish(out, prev)
	}
	p.last[b.Track] = f
}

func (p *webmParser) finish(out *[]codedFrame, f *codedFrame) {
	if f.dur <= 0 {
		f.dur = p.dur[f.track]
	}
	if f.dur <= 0 {
		if t := p.tracks[f.track]; t != nil && t.Kind == "audio" {
			f.dur = 0.02
		} else {
			f.dur = 1.0 / 30
		}
	}
	p.dur[f.track] = f.dur
	*out = append(*out, *f)
}

func (p *webmParser) flush(out *[]codedFrame) {
	for _, num := range p.order {
		if f := p.last[num]; f != nil {
			p.finish(out, f)
			p.last[num] = nil
		}
	}
}
