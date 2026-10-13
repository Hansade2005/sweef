package media

import (
	"fmt"
)

// OpenTracks indexes the first video and the first audio track of a
// progressive resource (MP4 or fragmented MP4, WebM, Matroska) as sample
// stores. Payloads of MP4 samples are read from src on demand; WebM is read
// into memory. A missing track is a nil store.
func OpenTracks(src Source, info *Info) (video, audio *TrackStore, err error) {
	switch info.Container {
	case "mp4":
		return openMP4Tracks(src, info)
	case "webm", "matroska":
		return openWebMTracks(src, info)
	}
	return nil, nil, ErrUnsupported
}

func openMP4Tracks(src Source, info *Info) (video, audio *TrackStore, err error) {
	size := src.Size()
	var moov *MP4Moov
	var moofs []topBox
	for _, b := range scanTopLevel(src, size) {
		switch b.typ {
		case "moov":
			d, err := readFull(src, b.start+int64(b.hdr), int(b.size)-b.hdr)
			if err != nil {
				return nil, nil, err
			}
			m, err := parseMoov(d)
			if err != nil {
				return nil, nil, err
			}
			moov = m
		case "moof":
			moofs = append(moofs, b)
		}
	}
	if moov == nil {
		return nil, nil, fmt.Errorf("media: no moov")
	}
	for _, kind := range []string{"video", "audio"} {
		var trk *mp4Track
		for _, t := range moov.Tracks {
			if t.Kind == kind && t.timescale > 0 {
				trk = t
				break
			}
		}
		if trk == nil {
			continue
		}
		cfg := mp4TrackConfig(trk)
		if kind == "audio" && info != nil {
			if a := info.Audio(); a != nil && a.SampleRate > 0 {
				cfg.SampleRate = a.SampleRate
			}
		}
		st := NewTrackStore(cfg)
		ts := float64(trk.timescale)
		shift := 0.0
		if trk.editStart > 0 {
			shift = float64(trk.editStart) / ts
		}
		var out []Sample
		if len(trk.samples) > 0 {
			ss := trk.samples
			out = make([]Sample, 0, len(ss))
			for i, s := range ss {
				var dur float64
				if i+1 < len(ss) {
					dur = float64(ss[i+1].dts-s.dts) / ts
				} else if i > 0 {
					dur = float64(s.dts-ss[i-1].dts) / ts
				}
				off, n := s.off, int(s.size)
				out = append(out, NewLazySample(float64(s.dts)/ts-shift, float64(s.cts)/ts-shift, dur, s.sync || kind == "audio", n,
					func() ([]byte, error) { return readFull(src, off, n) }))
			}
		} else {
			out = mp4FragmentSamples(src, moofs, trk, shift)
		}
		if len(out) == 0 {
			continue
		}
		st.Append(out)
		if kind == "video" {
			video = st
		} else {
			audio = st
		}
	}
	if video == nil && audio == nil {
		return nil, nil, fmt.Errorf("media: no tracks")
	}
	return video, audio, nil
}

// mp4FragmentSamples walks the moof boxes of a (non-MSE) fragmented file.
func mp4FragmentSamples(src Source, moofs []topBox, trk *mp4Track, shift float64) []Sample {
	var out []Sample
	ts := float64(trk.timescale)
	audio := trk.Kind == "audio"
	next := int64(0)
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
			dts := next
			if tfdt := findBox(kids, "tfdt"); tfdt != nil && len(tfdt.data) >= 8 {
				if tfdt.data[0] == 1 && len(tfdt.data) >= 12 {
					dts = int64(be64(tfdt.data[4:]))
				} else {
					dts = int64(be32(tfdt.data[4:]))
				}
			}
			cur := base
			for _, tr := range findBoxes(kids, "trun") {
				if len(tr.data) < 8 {
					continue
				}
				ver := tr.data[0]
				fl := be32(tr.data) & 0xffffff
				cnt := int(be32(tr.data[4:]))
				q := 8
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
					read := func(dst *uint32) bool {
						if q+4 > len(tr.data) {
							return false
						}
						*dst = be32(tr.data[q:])
						q += 4
						return true
					}
					if fl&0x100 != 0 && !read(&dur) {
						break
					}
					if fl&0x200 != 0 && !read(&sz) {
						break
					}
					if fl&0x400 != 0 && !read(&sf) {
						break
					}
					if fl&0x800 != 0 {
						var v uint32
						if !read(&v) {
							break
						}
						if ver == 0 {
							cto = int64(v)
						} else {
							cto = int64(int32(v))
						}
					}
					off, n := cur, int(sz)
					out = append(out, NewLazySample(float64(dts)/ts-shift, float64(dts+cto)/ts-shift, float64(dur)/ts, sf&0x10000 == 0 || audio, n,
						func() ([]byte, error) { return readFull(src, off, n) }))
					cur += int64(sz)
					dts += int64(dur)
				}
			}
			next = dts
		}
	}
	return out
}

func openWebMTracks(src Source, info *Info) (video, audio *TrackStore, err error) {
	if src.Size() > 512<<20 {
		return nil, nil, fmt.Errorf("media: WebM too large to index")
	}
	buf, err := readFull(src, 0, int(src.Size()))
	if len(buf) == 0 {
		return nil, nil, err
	}
	p := newWebMParser()
	init, frames, perr := p.feed(buf)
	if init == nil {
		return nil, nil, fmt.Errorf("media: WebM without tracks: %v", perr)
	}
	stores := map[int]*TrackStore{}
	for _, c := range init.tracks {
		if c.Kind != "video" && c.Kind != "audio" {
			continue
		}
		st := NewTrackStore(c)
		stores[c.ID] = st
		if c.Kind == "video" && video == nil {
			video = st
		}
		if c.Kind == "audio" && audio == nil {
			audio = st
		}
	}
	by := map[int][]Sample{}
	for _, f := range frames {
		if stores[f.track] == nil {
			continue
		}
		key := f.key
		if v := stores[f.track].Config(); v.Kind == "video" {
			switch v.CodecName {
			case "vp8":
				key = len(f.data) > 0 && f.data[0]&1 == 0
			case "vp9":
				key = vp9IsKey(f.data)
			}
		}
		by[f.track] = append(by[f.track], NewSample(f.dts, f.pts, f.dur, key, f.data))
	}
	for id, ss := range by {
		stores[id].Append(ss)
	}
	if video != nil && video.Len() == 0 {
		video = nil
	}
	if audio != nil && audio.Len() == 0 {
		audio = nil
	}
	if video == nil && audio == nil {
		return nil, nil, fmt.Errorf("media: no samples")
	}
	return video, audio, nil
}
