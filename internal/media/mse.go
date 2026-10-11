package media

import (
	"fmt"
	"sort"
	"strings"
)

// MSEBuffer parses the byte stream appended to one SourceBuffer (fragmented
// MP4 or WebM) and reports initialisation data and per-track time spans.
type MSEBuffer struct {
	Mime     string
	webm     bool
	mp4init  *MP4Init
	wInit    *WebMInit
	pending  []byte
	All      []byte // everything accepted so far (used for ffmpeg frames)
	haveInit bool
}

// MSEInit is reported once, with the first initialisation segment.
type MSEInit struct {
	Duration float64
	Tracks   []Track
}

// MSESpan is a covered time range of one track.
type MSESpan struct {
	Track      int
	Start, End float64
}

// MSEResult is what one append produced.
type MSEResult struct {
	Init  *MSEInit
	Spans []MSESpan
}

// NewMSEBuffer creates a parser for a SourceBuffer of the given MIME type.
func NewMSEBuffer(mime string) *MSEBuffer {
	t := strings.ToLower(mime)
	return &MSEBuffer{Mime: mime, webm: strings.Contains(t, "webm")}
}

// Append consumes a chunk. It errors on malformed data.
func (b *MSEBuffer) Append(data []byte) (*MSEResult, error) {
	if b.webm {
		return b.appendWebM(data)
	}
	return b.appendMP4(data)
}

func (b *MSEBuffer) appendMP4(data []byte) (*MSEResult, error) {
	buf := append(b.pending, data...)
	b.pending = nil
	res := &MSEResult{}
	var off int
	for off+8 <= len(buf) {
		sz := int(be32(buf[off:]))
		typ := string(buf[off+4 : off+8])
		hdr := 8
		if sz == 1 {
			if off+16 > len(buf) {
				break
			}
			sz = int(be64(buf[off+8:]))
			hdr = 16
		} else if sz == 0 {
			sz = len(buf) - off
		}
		if sz < hdr {
			return nil, fmt.Errorf("mse: bad box size")
		}
		if off+sz > len(buf) {
			break // incomplete box: wait for more data
		}
		body := buf[off+hdr : off+sz]
		switch typ {
		case "moov":
			in, err := ParseMP4Init(buf[off : off+sz])
			if err != nil {
				return nil, err
			}
			b.mp4init = in
			if !b.haveInit {
				b.haveInit = true
				res.Init = &MSEInit{Duration: in.Dur, Tracks: in.Tracks}
			}
		case "moof":
			if b.mp4init == nil {
				return nil, fmt.Errorf("mse: media segment before initialization segment")
			}
			for _, f := range parseMoof(body, b.mp4init.moov) {
				res.Spans = append(res.Spans, MSESpan{Track: f.TrackID, Start: f.Start, End: f.End})
			}
		case "ftyp", "styp", "sidx", "mdat", "free", "skip", "emsg", "prft", "mfra", "pssh":
		default:
			// unknown top-level boxes are ignored
		}
		b.All = append(b.All, buf[off:off+sz]...)
		off += sz
	}
	if off < len(buf) {
		b.pending = append([]byte(nil), buf[off:]...)
	}
	if len(buf) > 0 && off == 0 && len(buf) < 8 {
		return res, nil
	}
	if !b.haveInit && b.mp4init == nil && off == 0 && len(b.pending) == 0 {
		return nil, fmt.Errorf("mse: unrecognised data")
	}
	return res, nil
}

func (b *MSEBuffer) appendWebM(data []byte) (*MSEResult, error) {
	buf := append(b.pending, data...)
	b.pending = nil
	res := &MSEResult{}
	if !b.haveInit {
		// the init segment ends at the first Cluster element
		ci := indexCluster(buf)
		if ci < 0 {
			if len(buf) > 4<<20 {
				return nil, fmt.Errorf("mse: no Cluster found")
			}
			if len(buf) >= 4 && !(buf[0] == 0x1A && buf[1] == 0x45 && buf[2] == 0xDF && buf[3] == 0xA3) {
				return nil, fmt.Errorf("mse: not a WebM stream")
			}
			b.pending = buf
			return res, nil
		}
		in, err := ParseWebMInit(buf[:ci])
		if err != nil {
			return nil, err
		}
		b.wInit = in
		b.haveInit = true
		b.All = append(b.All, buf[:ci]...)
		res.Init = &MSEInit{Duration: in.Dur, Tracks: in.Tracks}
		buf = buf[ci:]
	}
	if len(buf) == 0 {
		return res, nil
	}
	// split complete clusters
	var off int
	for off < len(buf) {
		id, il, sz, sl, ok := parseElHeader(buf[off:])
		if !ok {
			break
		}
		start := off + il + sl
		end := start + int(sz)
		if sz < 0 {
			end = len(buf)
		} else if end > len(buf) {
			break
		}
		if id == idCluster {
			blocks := ParseWebMBlocksScale(buf[off:end], b.wInit.Scale)
			res.Spans = append(res.Spans, blockSpans(blocks, b.wInit)...)
		}
		b.All = append(b.All, buf[off:end]...)
		off = end
	}
	if off < len(buf) {
		b.pending = append([]byte(nil), buf[off:]...)
	}
	return res, nil
}

func indexCluster(b []byte) int {
	for i := 0; i+4 < len(b); i++ {
		if b[i] == 0x1F && b[i+1] == 0x43 && b[i+2] == 0xB6 && b[i+3] == 0x75 {
			return i
		}
	}
	return -1
}

func blockSpans(blocks []WebMBlock, in *WebMInit) []MSESpan {
	by := map[int][]float64{}
	for _, bl := range blocks {
		by[bl.Track] = append(by[bl.Track], bl.Time)
	}
	var out []MSESpan
	for tr, ts := range by {
		sort.Float64s(ts)
		iv := 0.02
		if len(ts) > 1 {
			iv = (ts[len(ts)-1] - ts[0]) / float64(len(ts)-1)
		} else {
			for _, t := range in.Tracks {
				if t.ID == tr && t.FrameRate > 0 {
					iv = 1 / t.FrameRate
				}
			}
		}
		out = append(out, MSESpan{Track: tr, Start: ts[0], End: ts[len(ts)-1] + iv})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Track < out[j].Track })
	return out
}
