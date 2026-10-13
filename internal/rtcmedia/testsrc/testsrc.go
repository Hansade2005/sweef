// Package testsrc provides deterministic synthetic capture sources for tests and for the engine's fake
// devices: a moving test pattern with a frame counter drawn into the picture, and a sine tone.
package testsrc

import (
	"image"
	"math"
	"sync"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
)

// counterBits is the number of bits of the frame counter drawn into every picture.
const counterBits = 16

// Video is a capture.VideoSource producing a moving pattern at a fixed rate.
type Video struct {
	W, H int
	FPS  float64
	// Keep stores the last N frames by counter (for PSNR comparisons in tests); 0 keeps none.
	Keep int

	mu      sync.Mutex
	started bool
	stop    chan struct{}
	done    chan struct{}
	n       uint32
	kept    map[uint16]*capture.Frame
	order   []uint16
	err     error
	Label   string
}

// NewVideo returns a source of w x h frames at fps.
func NewVideo(w, h int, fps float64) *Video {
	return &Video{W: w, H: h, FPS: fps, kept: map[uint16]*capture.Frame{}, Label: "Synthetic camera"}
}

func (v *Video) Info() capture.DeviceInfo {
	return capture.DeviceInfo{ID: "synthetic-camera", GroupID: "synthetic", Label: v.Label, Kind: capture.VideoInput, Facing: "user"}
}

// Frames returns how many frames have been produced.
func (v *Video) Frames() uint32 {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.n
}

// Kept returns the stored frame with a counter value, if still kept.
func (v *Video) Kept(counter uint16) *capture.Frame {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.kept[counter]
}

func (v *Video) Start(c capture.VideoConstraints) (<-chan *capture.Frame, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.started {
		return nil, capture.ErrInUse
	}
	v.started = true
	if c.Width >= 16 && c.Height >= 16 {
		v.W, v.H = c.Width&^1, c.Height&^1
	}
	if c.FrameRate >= 1 && c.FrameRate <= 120 {
		v.FPS = c.FrameRate
	}
	if v.FPS <= 0 {
		v.FPS = 30
	}
	v.stop, v.done = make(chan struct{}), make(chan struct{})
	ch := make(chan *capture.Frame, 2)
	go v.run(ch, v.stop, v.done)
	return ch, nil
}

func (v *Video) run(ch chan<- *capture.Frame, stop, done chan struct{}) {
	defer close(done)
	defer close(ch)
	start := time.Now()
	interval := time.Duration(float64(time.Second) / v.FPS)
	next := start
	for {
		select {
		case <-stop:
			return
		case <-time.After(time.Until(next)):
		}
		v.mu.Lock()
		n := v.n
		v.n++
		w, h := v.W, v.H
		v.mu.Unlock()
		f := Pattern(w, h, n)
		f.PTS = time.Since(start)
		if v.Keep > 0 {
			v.mu.Lock()
			c := uint16(n)
			v.kept[c] = f
			v.order = append(v.order, c)
			for len(v.order) > v.Keep {
				delete(v.kept, v.order[0])
				v.order = v.order[1:]
			}
			v.mu.Unlock()
		}
		select {
		case ch <- f:
		case <-stop:
			return
		default: // a slow consumer loses frames, like a real camera
		}
		next = next.Add(interval)
		if d := time.Since(next); d > 5*interval {
			next = time.Now()
		}
	}
}

func (v *Video) Stop() error {
	v.mu.Lock()
	stop, done := v.stop, v.done
	v.stop = nil
	v.mu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}
	return nil
}

func (v *Video) Err() error { return v.err }

// sinTab is one period of sine in 4096 steps, scaled to 0..255 (so a pattern costs about a nanosecond a pixel).
var sinTab = func() (t [4096]uint8) {
	for i := range t {
		t[i] = uint8(127.5 + 127.5*math.Sin(2*math.Pi*float64(i)/4096))
	}
	return
}()

// Pattern renders frame n of the test sequence: a diagonal colour gradient that scrolls, a bouncing box
// and, in the top-left, the 16-bit frame counter as black/white blocks.
func Pattern(w, h int, n uint32) *capture.Frame {
	w, h = w&^1, h&^1
	cw, ch := w/2, h/2
	f := &capture.Frame{W: w, H: h, Y: make([]byte, w*h), U: make([]byte, cw*ch), V: make([]byte, cw*ch), YStride: w, UVStride: cw}
	t := float64(n)
	// luma: 60 + 120*(0.5+0.5*sin(2*pi*(1.5*x/w + 0.7*y/h) + 0.08*t)), phases in 1/4096 of a turn
	dx := int(1.5 * 4096 / float64(w) * 65536)
	dy := int(0.7 * 4096 / float64(h) * 65536)
	p0 := int((0.08 * t / (2 * math.Pi) * 4096) * 65536)
	for y := 0; y < h; y++ {
		ph := p0 + y*dy
		row := f.Y[y*w : y*w+w]
		for x := range row {
			row[x] = byte(60 + int(sinTab[(ph>>16)&4095])*120/255)
			ph += dx
		}
	}
	for y := 0; y < ch; y++ {
		pv := int((float64(y)/float64(ch)*6 - t*0.03) / (2 * math.Pi) * 4096 * 65536)
		pu0 := int((t * 0.05) / (2 * math.Pi) * 4096 * 65536)
		du := int(6 / float64(cw) / (2 * math.Pi) * 4096 * 65536)
		ur := f.U[y*cw : y*cw+cw]
		vr := f.V[y*cw : y*cw+cw]
		vv := byte(128 + (int(sinTab[((pv>>16)+1024)&4095])-128)*50/128)
		for x := range ur {
			ur[x] = byte(128 + (int(sinTab[((pu0+x*du)>>16)&4095])-128)*50/128)
			vr[x] = vv
		}
	}
	// bouncing box
	bw, bh := w/6, h/5
	bx := int(math.Abs(math.Mod(t*float64(w)/90, float64(2*(w-bw)))-float64(w-bw))) % (w - bw)
	by := int(math.Abs(math.Mod(t*float64(h)/70, float64(2*(h-bh)))-float64(h-bh))) % (h - bh)
	for y := by; y < by+bh; y++ {
		for x := bx; x < bx+bw; x++ {
			f.Y[y*w+x] = 220
		}
	}
	for y := by / 2; y < (by+bh)/2; y++ {
		for x := bx / 2; x < (bx+bw)/2; x++ {
			f.U[y*cw+x], f.V[y*cw+x] = 90, 200
		}
	}
	drawCounter(f, uint16(n))
	return f
}

// counter geometry in fractions of the picture, so it survives scaling
func counterRect(w, h, i int) (x0, y0, x1, y1 int) {
	bw := float64(w) * 0.05
	bh := float64(h) * 0.09
	left := float64(w) * 0.03
	top := float64(h) * 0.04
	x0 = int(left + float64(i)*bw)
	x1 = int(left + float64(i+1)*bw)
	y0, y1 = int(top), int(top+bh)
	return
}

func drawCounter(f *capture.Frame, c uint16) {
	cw := f.W / 2
	for i := 0; i < counterBits; i++ {
		bit := c>>(uint(counterBits-1-i))&1 != 0
		v, u := byte(16), byte(128)
		if bit {
			v = 235
		}
		x0, y0, x1, y1 := counterRect(f.W, f.H, i)
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				f.Y[y*f.W+x] = v
			}
		}
		for y := y0 / 2; y < y1/2; y++ {
			for x := x0 / 2; x < x1/2; x++ {
				f.U[y*cw+x], f.V[y*cw+x] = u, 128
			}
		}
	}
}

// ReadCounter reads the frame counter drawn by Pattern from a luma plane of any size. ok is false when
// the blocks are not clearly black or white (a damaged or non-pattern picture).
func ReadCounter(img *image.YCbCr) (counter uint16, ok bool) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	ok = true
	for i := 0; i < counterBits; i++ {
		x0, y0, x1, y1 := counterRect(w, h, i)
		// the central half of the block
		cx0, cx1 := x0+(x1-x0)/4, x1-(x1-x0)/4
		cy0, cy1 := y0+(y1-y0)/4, y1-(y1-y0)/4
		if cx1 <= cx0 {
			cx1 = cx0 + 1
		}
		if cy1 <= cy0 {
			cy1 = cy0 + 1
		}
		sum, n := 0, 0
		for y := cy0; y < cy1; y++ {
			for x := cx0; x < cx1; x++ {
				sum += int(img.Y[(b.Min.Y+y)*img.YStride+b.Min.X+x])
				n++
			}
		}
		avg := sum / n
		if avg > 70 && avg < 180 {
			ok = false
		}
		if avg >= 125 {
			counter |= 1 << uint(counterBits-1-i)
		}
	}
	return
}

// Audio is a capture.AudioSource producing a sine tone in real time.
type Audio struct {
	Rate     int
	Channels int
	Hz       float64
	Amp      float64 // 0..1
	Chunk    time.Duration

	mu    sync.Mutex
	stop  chan struct{}
	done  chan struct{}
	phase float64
	Label string
}

// NewTone returns a mono 48 kHz sine tone source.
func NewTone(hz float64) *Audio {
	return &Audio{Rate: 48000, Channels: 1, Hz: hz, Amp: 0.4, Chunk: 10 * time.Millisecond, Label: "Synthetic microphone"}
}

func (a *Audio) Info() capture.DeviceInfo {
	return capture.DeviceInfo{ID: "synthetic-microphone", GroupID: "synthetic", Label: a.Label, Kind: capture.AudioInput}
}

func (a *Audio) Start(c capture.AudioConstraints) (<-chan *capture.AudioChunk, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.stop != nil {
		return nil, capture.ErrInUse
	}
	a.stop, a.done = make(chan struct{}), make(chan struct{})
	ch := make(chan *capture.AudioChunk, 8)
	go a.run(ch, a.stop, a.done)
	return ch, nil
}

func (a *Audio) run(ch chan<- *capture.AudioChunk, stop, done chan struct{}) {
	defer close(done)
	defer close(ch)
	start := time.Now()
	n := int(float64(a.Rate) * a.Chunk.Seconds())
	next := start
	var pts time.Duration
	for {
		select {
		case <-stop:
			return
		case <-time.After(time.Until(next)):
		}
		pcm := make([]int16, n*a.Channels)
		for i := 0; i < n; i++ {
			v := int16(a.Amp * 32767 * math.Sin(a.phase))
			a.phase += 2 * math.Pi * a.Hz / float64(a.Rate)
			if a.phase > 2*math.Pi {
				a.phase -= 2 * math.Pi
			}
			for c := 0; c < a.Channels; c++ {
				pcm[i*a.Channels+c] = v
			}
		}
		select {
		case ch <- &capture.AudioChunk{Rate: a.Rate, Channels: a.Channels, PCM: pcm, PTS: pts}:
		case <-stop:
			return
		}
		pts += a.Chunk
		next = next.Add(a.Chunk)
	}
}

func (a *Audio) Stop() error {
	a.mu.Lock()
	stop, done := a.stop, a.done
	a.stop = nil
	a.mu.Unlock()
	if stop != nil {
		close(stop)
		<-done
	}
	return nil
}

func (a *Audio) Err() error { return nil }

// ToneLevel returns the share (0..1) of the signal's energy at hz, measured with a Goertzel filter on
// mono PCM, used by tests to check that the tone arrived.
func ToneLevel(pcm []int16, channels, rate int, hz float64) float64 {
	if channels < 1 || len(pcm) < channels*64 {
		return 0
	}
	n := len(pcm) / channels
	w := 2 * math.Pi * hz / float64(rate)
	coeff := 2 * math.Cos(w)
	var s0, s1, s2, energy float64
	for i := 0; i < n; i++ {
		x := float64(pcm[i*channels]) / 32768
		energy += x * x
		s0 = x + coeff*s1 - s2
		s2, s1 = s1, s0
	}
	if energy == 0 {
		return 0
	}
	p := s1*s1 + s2*s2 - coeff*s1*s2
	return math.Min(1, 2*p/float64(n)/energy)
}
