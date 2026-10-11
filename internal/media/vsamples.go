package media

import (
	"fmt"
	"sort"
)

// vsample is one coded video access unit in decode order.
type vsample struct {
	off  int64
	size int
	pts  float64
	key  bool
	data []byte // set when the payload is already in memory
}

// vstream is the video track of a resource as a flat list of access units.
type vstream struct {
	codec   string // vp8 vp9 h264 av1
	cfg     []byte // avcC / vpcC / av1C
	w, h    int
	samples []vsample
}

func (v *vstream) payload(src Source, i int) ([]byte, error) {
	s := &v.samples[i]
	if s.data != nil {
		return s.data, nil
	}
	return readFull(src, s.off, s.size)
}

// loadVideoStream indexes the first video track of an MP4 (progressive or
// fragmented) or WebM/Matroska resource.
func loadVideoStream(src Source, info *Info) (*vstream, error) {
	v := info.Video()
	if v == nil {
		return nil, fmt.Errorf("media: no video track")
	}
	vs := &vstream{codec: v.CodecName, w: v.Width, h: v.Height}
	switch info.Container {
	case "mp4":
		if err := vs.loadMP4(src); err != nil {
			return nil, err
		}
	case "webm", "matroska":
		if err := vs.loadWebM(src, info, v); err != nil {
			return nil, err
		}
	default:
		return nil, ErrUnsupported
	}
	if len(vs.samples) == 0 {
		return nil, fmt.Errorf("media: no video samples")
	}
	return vs, nil
}

func (vs *vstream) loadWebM(src Source, info *Info, trk *Track) error {
	if vs.codec == "av1" {
		vs.cfg = trk.priv // av1C: configuration OBUs (sequence header)
	}
	off := info.DataStart
	if off < 0 {
		off = 0
	}
	buf, err := readFull(src, off, int(src.Size()-off))
	if len(buf) == 0 {
		return err
	}
	for _, b := range ParseWebMBlocksScale(buf, 1000000) {
		if b.Track != trk.ID || len(b.Data) == 0 {
			continue
		}
		key := b.Key
		switch vs.codec {
		case "vp8":
			key = b.Data[0]&1 == 0
		case "vp9":
			key = vp9IsKey(b.Data)
		}
		vs.samples = append(vs.samples, vsample{size: len(b.Data), pts: b.Time, key: key, data: b.Data})
	}
	return nil
}

func (vs *vstream) loadMP4(src Source) error {
	size := src.Size()
	var moov *MP4Moov
	var moofs []topBox
	for _, b := range scanTopLevel(src, size) {
		switch b.typ {
		case "moov":
			d, err := readFull(src, b.start+int64(b.hdr), int(b.size)-b.hdr)
			if err != nil {
				return err
			}
			m, err := parseMoov(d)
			if err != nil {
				return err
			}
			moov = m
		case "moof":
			moofs = append(moofs, b)
		}
	}
	if moov == nil {
		return fmt.Errorf("media: no moov")
	}
	var trk *mp4Track
	for _, t := range moov.Tracks {
		if t.Kind == "video" {
			trk = t
			break
		}
	}
	if trk == nil || trk.timescale == 0 {
		return fmt.Errorf("media: no mp4 video track")
	}
	vs.cfg = trk.vcfg
	if trk.Width > 0 {
		vs.w, vs.h = trk.Width, trk.Height
	}
	ts := float64(trk.timescale)
	if len(trk.samples) > 0 {
		shift := int64(0)
		if trk.editStart > 0 {
			shift = int64(trk.editStart)
		}
		for _, s := range trk.samples {
			vs.samples = append(vs.samples, vsample{off: s.off, size: int(s.size), pts: float64(s.cts-shift) / ts, key: s.sync})
		}
		return nil
	}
	// Fragmented: walk each moof.
	for _, mf := range moofs {
		d, err := readFull(src, mf.start+int64(mf.hdr), int(mf.size)-mf.hdr)
		if err != nil {
			continue
		}
		for _, tf := range findBoxes(parseBoxes(d), "traf") {
			kids := parseBoxes(tf.data)
			tfhd := findBox(kids, "tfhd")
			if tfhd == nil || len(tfhd.data) < 8 || int(be32(tfhd.data[4:])) != trk.ID {
				continue
			}
			flags := be32(tfhd.data) & 0xffffff
			p := 8
			base := mf.start
			if flags&1 != 0 && p+8 <= len(tfhd.data) {
				base = int64(be64(tfhd.data[p:]))
				p += 8
			}
			if flags&2 != 0 {
				p += 4
			}
			defDur, defSize, defFlags := trk.trex.dur, trk.trex.size, trk.trex.flags
			if flags&8 != 0 && p+4 <= len(tfhd.data) {
				defDur = be32(tfhd.data[p:])
				p += 4
			}
			if flags&0x10 != 0 && p+4 <= len(tfhd.data) {
				defSize = be32(tfhd.data[p:])
				p += 4
			}
			if flags&0x20 != 0 && p+4 <= len(tfhd.data) {
				defFlags = be32(tfhd.data[p:])
			}
			var dts int64
			if tfdt := findBox(kids, "tfdt"); tfdt != nil && len(tfdt.data) >= 8 {
				if tfdt.data[0] == 1 && len(tfdt.data) >= 12 {
					dts = int64(be64(tfdt.data[4:]))
				} else {
					dts = int64(be32(tfdt.data[4:]))
				}
			}
			next := base
			for _, tr := range findBoxes(kids, "trun") {
				if len(tr.data) < 8 {
					continue
				}
				ver := tr.data[0]
				fl := be32(tr.data) & 0xffffff
				cnt := int(be32(tr.data[4:]))
				q := 8
				cur := next
				if fl&1 != 0 && q+4 <= len(tr.data) {
					cur = base + int64(int32(be32(tr.data[q:])))
					q += 4
				}
				firstFlags := defFlags
				hasFirst := false
				if fl&4 != 0 {
					if q+4 <= len(tr.data) {
						firstFlags = be32(tr.data[q:])
						hasFirst = true
					}
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
							break
						}
						dur = be32(tr.data[q:])
						q += 4
					}
					if fl&0x200 != 0 {
						if q+4 > len(tr.data) {
							break
						}
						sz = be32(tr.data[q:])
						q += 4
					}
					if fl&0x400 != 0 {
						if q+4 > len(tr.data) {
							break
						}
						sf = be32(tr.data[q:])
						q += 4
					}
					if fl&0x800 != 0 {
						if q+4 > len(tr.data) {
							break
						}
						if ver == 0 {
							cto = int64(be32(tr.data[q:]))
						} else {
							cto = int64(int32(be32(tr.data[q:])))
						}
						q += 4
					}
					vs.samples = append(vs.samples, vsample{off: cur, size: int(sz), pts: float64(dts+cto) / ts, key: sf&0x10000 == 0})
					cur += int64(sz)
					dts += int64(dur)
				}
				next = cur
			}
		}
	}
	return nil
}

// order returns the sample indices sorted by presentation time.
func (vs *vstream) order() []int {
	o := make([]int, len(vs.samples))
	for i := range o {
		o[i] = i
	}
	sort.SliceStable(o, func(a, b int) bool { return vs.samples[o[a]].pts < vs.samples[o[b]].pts })
	return o
}
