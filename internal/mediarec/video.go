package mediarec

import (
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
	"github.com/sweefbrowser/sweef/internal/media"
)

// vloop encodes the video source with VP8. It keeps at most maxVideoFPS frames per second, always encodes the
// newest frame when it has fallen behind, and scales frames whose size differs from the first one.
type vloop struct {
	r    *Recorder
	in   *VideoIn
	w, h int
	enc  *media.WCVP8Encoder

	mu       sync.Mutex
	ended    bool
	busy     bool
	last     time.Duration
	haveLast bool
	needKey  bool
	scratch  *capture.Frame
}

func newVLoop(r *Recorder, in *VideoIn) (*vloop, error) {
	w, h := in.W&^1, in.H&^1
	if w < 16 || h < 16 || w > 16383 || h > 16383 {
		return nil, fmt.Errorf("unsupported video size %dx%d (VP8 records 16 to 16383)", in.W, in.H)
	}
	fps := in.FPS
	if fps <= 0 || fps > maxVideoFPS {
		fps = maxVideoFPS
	}
	bps := r.cfg.VideoBPS
	if bps <= 0 {
		bps = 2500000
	}
	enc, err := media.NewWCVP8Encoder(w, h, bps, fps, int(fps*3))
	if err != nil {
		return nil, err
	}
	return &vloop{r: r, in: in, w: w, h: h, enc: enc, needKey: true}, nil
}

func (v *vloop) end() {
	v.mu.Lock()
	if v.ended {
		v.mu.Unlock()
		return
	}
	v.ended = true
	v.mu.Unlock()
	if v.in.Cancel != nil {
		v.in.Cancel()
	}
	v.r.marks[trkVideo].Store(math.MaxInt64)
}

// idleMark tells the interleaver that nothing older than a little while ago will come from the video track.
func (v *vloop) idleMark(now time.Duration) {
	v.mu.Lock()
	idle := !v.busy && !v.ended
	v.mu.Unlock()
	if idle {
		if m := int64(now-300*time.Millisecond) / 1000; m > v.r.marks[trkVideo].Load() {
			v.r.marks[trkVideo].Store(m)
		}
	}
}

func (v *vloop) run() {
	defer v.r.wg.Done()
	minGap := time.Second/maxVideoFPS - 5*time.Millisecond
	for {
		var f *capture.Frame
		select {
		case fr, ok := <-v.in.C:
			if !ok {
				v.end()
				return
			}
			f = fr
		case <-v.r.quit:
			return
		}
	newest:
		for { // fell behind: take the newest
			select {
			case fr, ok := <-v.in.C:
				if !ok {
					break newest
				}
				f = fr
			default:
				break newest
			}
		}
		if v.r.paused.Load() || f == nil || f.W < 2 || f.H < 2 {
			continue
		}
		ts := v.r.clk.Now()
		if v.haveLast && ts-v.last < minGap {
			continue
		}
		v.mu.Lock()
		v.busy = true
		v.mu.Unlock()
		v.r.marks[trkVideo].Store(int64(ts) / 1000)
		v.encode(f, ts)
		v.mu.Lock()
		v.busy = false
		v.mu.Unlock()
		v.last, v.haveLast = ts, true
	}
}

func (v *vloop) encode(f *capture.Frame, ts time.Duration) {
	y, u, vv, ys, cs := f.Y, f.U, f.V, f.YStride, f.UVStride
	if f.W != v.w || f.H != v.h {
		if v.scratch == nil {
			v.scratch = capture.NewFrame(v.w, v.h)
		}
		scaleI420(v.scratch, f)
		y, u, vv, ys, cs = v.scratch.Y, v.scratch.U, v.scratch.V, v.scratch.YStride, v.scratch.UVStride
	}
	data, key, err := v.enc.Encode(v.w, v.h, y, u, vv, ys, cs, v.needKey)
	if err != nil || len(data) == 0 {
		return
	}
	v.needKey = false
	v.r.put(trkVideo, int64(ts)/1000, key, data, 0)
}

// scaleI420 resizes src into dst (nearest neighbour; this path is for sources that change size mid-recording).
func scaleI420(dst, src *capture.Frame) {
	plane := func(d []byte, dw, dh, ds int, s []byte, sw, sh, ss int) {
		for y := 0; y < dh; y++ {
			sy := y * sh / dh
			for x := 0; x < dw; x++ {
				d[y*ds+x] = s[sy*ss+x*sw/dw]
			}
		}
	}
	plane(dst.Y, dst.W, dst.H, dst.YStride, src.Y, src.W, src.H, src.YStride)
	plane(dst.U, (dst.W+1)/2, (dst.H+1)/2, dst.UVStride, src.U, (src.W+1)/2, (src.H+1)/2, src.UVStride)
	plane(dst.V, (dst.W+1)/2, (dst.H+1)/2, dst.UVStride, src.V, (src.W+1)/2, (src.H+1)/2, src.UVStride)
}
