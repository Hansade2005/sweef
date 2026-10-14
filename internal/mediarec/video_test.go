package mediarec

import (
	"math"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
	"github.com/sweefbrowser/sweef/internal/media"
)

// moving returns frame n of a test picture: a gradient with a square that moves, smooth enough that VP8 at a
// modest bitrate reproduces it closely.
func moving(w, h, n int) *capture.Frame {
	f := capture.NewFrame(w, h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			f.Y[y*f.YStride+x] = byte(40 + (x*100/w + y*60/h))
		}
	}
	sx, sy := (n*6)%(w-40), (n*3)%(h-40)
	for y := sy; y < sy+40; y++ {
		for x := sx; x < sx+40; x++ {
			f.Y[y*f.YStride+x] = 220
		}
	}
	for y := 0; y < (h+1)/2; y++ {
		for x := 0; x < (w+1)/2; x++ {
			f.U[y*f.UVStride+x] = byte(100 + x*40/w)
			f.V[y*f.UVStride+x] = byte(150 - y*40/h)
		}
	}
	return f
}

// frameSource paces frames in real time and remembers what it sent.
type frameSource struct {
	mu   sync.Mutex
	sent []*capture.Frame
}

func newFrames(w, h int, fps float64) (*VideoIn, *frameSource) {
	fs := &frameSource{}
	c := make(chan *capture.Frame, 3)
	stop := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(c)
		tk := time.NewTicker(time.Duration(float64(time.Second) / fps))
		defer tk.Stop()
		for n := 0; ; n++ {
			select {
			case <-stop:
				return
			case <-tk.C:
			}
			f := moving(w, h, n)
			fs.mu.Lock()
			fs.sent = append(fs.sent, f)
			fs.mu.Unlock()
			select {
			case c <- f:
			default:
			}
		}
	}()
	return &VideoIn{C: c, Cancel: func() { once.Do(func() { close(stop) }) }, W: w, H: h, FPS: fps}, fs
}

func psnr(a, b *media.WCPicture, f *capture.Frame) float64 {
	var se float64
	n := 0
	for y := 0; y < f.H; y++ {
		for x := 0; x < f.W; x++ {
			d := float64(a.Planes[0][y*a.Strides[0]+x]) - float64(f.Y[y*f.YStride+x])
			se += d * d
			n++
		}
	}
	if se == 0 {
		return 99
	}
	return 10 * math.Log10(255*255*float64(n)/se)
}

func TestVideoAudioWebMRoundTrip(t *testing.T) {
	vin, fs := newFrames(320, 240, 24)
	ain, _ := toneSource(48000, 1, 440)
	col := record(t, Config{Container: "webm", AudioBPS: 48000, VideoBPS: 1500000}, []*AudioIn{ain}, vin, 2*time.Second)
	data := col.all()
	if p := os.Getenv("SWEEF_REC_DUMP"); p != "" {
		_ = os.WriteFile(p+"/va.webm", data, 0o644)
	}
	info, err := media.ProbeBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if info.Duration < 1.7 || info.Duration > 2.6 {
		t.Errorf("duration %.2f s, want about 2", info.Duration)
	}
	vs, err := media.ExtractVideoSamples(data)
	if err != nil {
		t.Fatal(err)
	}
	if vs.Codec != "vp8" || len(vs.Chunks) < 30 {
		t.Fatalf("video track: codec %q, %d chunks", vs.Codec, len(vs.Chunks))
	}
	if !vs.Chunks[0].Key {
		t.Error("first video chunk is not a key frame")
	}
	dec, err := media.NewWCVideoDecoder("vp8", nil)
	if err != nil {
		t.Fatal(err)
	}
	fs.mu.Lock()
	sent := append([]*capture.Frame(nil), fs.sent...)
	fs.mu.Unlock()
	worst := 99.0
	prevTS := int64(-1)
	for i, ch := range vs.Chunks {
		pics, err := dec.Decode(ch.Data)
		if err != nil || len(pics) != 1 {
			t.Fatalf("chunk %d: %v (%d pictures)", i, err, len(pics))
		}
		if ch.PTS < prevTS {
			t.Errorf("chunk %d timestamp %d goes back", i, ch.PTS)
		}
		prevTS = ch.PTS
		if pics[0].Width != 320 || pics[0].Height != 240 {
			t.Fatalf("decoded %dx%d", pics[0].Width, pics[0].Height)
		}
		best := 0.0
		for _, s := range sent {
			if p := psnr(pics[0], nil, s); p > best {
				best = p
			}
		}
		if best < worst {
			worst = best
		}
	}
	t.Logf("worst-frame PSNR against the closest source frame: %.1f dB over %d frames", worst, len(vs.Chunks))
	if worst < 28 {
		t.Errorf("worst PSNR %.1f dB, want at least 28", worst)
	}
	p := decode(t, data)
	mid := p.Data[0][len(p.Data[0])/4 : len(p.Data[0])*3/4]
	if f := freq(mid, p.SampleRate); math.Abs(f-440) > 6 {
		t.Errorf("tone at %.1f Hz", f)
	}
}

func TestVideoResizesLateFrames(t *testing.T) {
	c := make(chan *capture.Frame, 3)
	in := &VideoIn{C: c, Cancel: func() {}, W: 160, H: 120, FPS: 15}
	col := &collector{}
	r, err := New(Config{Container: "webm"}, nil, in, col.add)
	if err != nil {
		t.Fatal(err)
	}
	r.Start()
	for i := 0; i < 12; i++ {
		w, h := 160, 120
		if i >= 6 {
			w, h = 320, 240
		}
		c <- moving(w, h, i)
		time.Sleep(70 * time.Millisecond)
	}
	r.Stop()
	<-r.Done()
	vs, err := media.ExtractVideoSamples(col.all())
	if err != nil || len(vs.Chunks) < 8 {
		t.Fatalf("%v, %d chunks", err, len(vs.Chunks))
	}
	dec, _ := media.NewWCVideoDecoder("vp8", nil)
	for i, ch := range vs.Chunks {
		pics, err := dec.Decode(ch.Data)
		if err != nil || len(pics) != 1 || pics[0].Width != 160 || pics[0].Height != 120 {
			t.Fatalf("chunk %d: %v %v", i, err, pics)
		}
	}
}

func TestOnlyDeclaredTracksAndSizes(t *testing.T) {
	if _, err := New(Config{Container: "webm"}, nil, nil, func(Chunk) {}); err == nil {
		t.Error("a recorder without tracks must be refused")
	}
	if _, err := New(Config{Container: "webm"}, nil, &VideoIn{W: 8, H: 8}, func(Chunk) {}); err == nil {
		t.Error("8x8 video must be refused")
	}
	if _, err := New(Config{Container: "ogg"}, nil, &VideoIn{W: 64, H: 64}, func(Chunk) {}); err == nil {
		t.Error("Ogg holds no video")
	}
}
