package mediarec

import (
	"bytes"
	"math"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
	"github.com/sweefbrowser/sweef/internal/media"
)

// toneSource delivers a sine in real time, in 10 ms chunks, like a microphone.
func toneSource(rate, ch int, freq float64) (*AudioIn, func()) {
	c := make(chan *capture.AudioChunk, 16)
	stop := make(chan struct{})
	var once sync.Once
	go func() {
		defer close(c)
		n := rate / 100
		tk := time.NewTicker(10 * time.Millisecond)
		defer tk.Stop()
		for k := 0; ; k++ {
			select {
			case <-stop:
				return
			case <-tk.C:
			}
			pcm := make([]int16, n*ch)
			for i := 0; i < n; i++ {
				v := int16(0.25 * 32767 * math.Sin(2*math.Pi*freq*float64(k*n+i)/float64(rate)))
				for j := 0; j < ch; j++ {
					pcm[i*ch+j] = v
				}
			}
			select {
			case c <- &capture.AudioChunk{Rate: rate, Channels: ch, PCM: pcm}:
			default:
			}
		}
	}()
	cancel := func() { once.Do(func() { close(stop) }) }
	return &AudioIn{C: c, Cancel: cancel, Rate: rate, Channels: ch}, cancel
}

type collector struct {
	mu     sync.Mutex
	chunks []Chunk
}

func (c *collector) add(ch Chunk) { c.mu.Lock(); c.chunks = append(c.chunks, ch); c.mu.Unlock() }
func (c *collector) all() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b []byte
	for _, ch := range c.chunks {
		b = append(b, ch.Data...)
	}
	return b
}

func record(t *testing.T, cfg Config, a []*AudioIn, v *VideoIn, d time.Duration) *collector {
	t.Helper()
	col := &collector{}
	r, err := New(cfg, a, v, col.add)
	if err != nil {
		t.Fatal(err)
	}
	r.Start()
	time.Sleep(d)
	r.Stop()
	select {
	case <-r.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("recorder did not finish")
	}
	return col
}

// freq estimates the dominant frequency of mono samples by counting rising zero crossings.
func freq(s []float32, rate int) float64 {
	cross, first, last := 0, -1, 0
	for i := 1; i < len(s); i++ {
		if s[i-1] < 0 && s[i] >= 0 {
			if first < 0 {
				first = i
			}
			last = i
			cross++
		}
	}
	if cross < 2 {
		return 0
	}
	return float64(cross-1) * float64(rate) / float64(last-first)
}

func rms(s []float32) float64 {
	var a float64
	for _, v := range s {
		a += float64(v) * float64(v)
	}
	return math.Sqrt(a / float64(len(s)))
}

func decode(t *testing.T, b []byte) *media.PCM {
	t.Helper()
	p, err := media.DecodeAudio(b)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	return p
}

func TestAudioRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name, container string
		rate, ch        int
	}{
		{"webm 48k mono", "webm", 48000, 1},
		{"ogg 48k mono", "ogg", 48000, 1},
		{"webm 44.1k mono resampled", "webm", 44100, 1},
		{"ogg 16k mono resampled", "ogg", 16000, 1},
		{"webm 48k stereo", "webm", 48000, 2},
		{"ogg 96k mono downsampled", "ogg", 96000, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, _ := toneSource(tc.rate, tc.ch, 440)
			col := record(t, Config{Container: tc.container, AudioBPS: 64000}, []*AudioIn{in}, nil, 1500*time.Millisecond)
			data := col.all()
			info, err := media.ProbeBytes(data)
			if err != nil {
				t.Fatalf("probe: %v", err)
			}
			if info.Duration < 1.2 || info.Duration > 2.0 {
				t.Errorf("probed duration %.3f s, want about 1.5", info.Duration)
			}
			p := decode(t, data)
			secs := float64(len(p.Data[0])) / float64(p.SampleRate)
			if secs < 1.2 || secs > 2.0 {
				t.Errorf("decoded %.3f s, want about 1.5", secs)
			}
			mid := p.Data[0][len(p.Data[0])/4 : len(p.Data[0])*3/4]
			if r := rms(mid); r < 0.12 || r > 0.22 { // 0.25 peak sine is 0.177 rms
				t.Errorf("rms %.3f, want about 0.177", r)
			}
			if f := freq(mid, p.SampleRate); math.Abs(f-440) > 6 {
				t.Errorf("tone at %.1f Hz, want 440", f)
			}
		})
	}
}

func TestChunksConcatenateAndTimeslice(t *testing.T) {
	for _, container := range []string{"webm", "ogg"} {
		t.Run(container, func(t *testing.T) {
			in, _ := toneSource(48000, 1, 440)
			col := record(t, Config{Container: container, AudioBPS: 32000, Timeslice: 300 * time.Millisecond}, []*AudioIn{in}, nil, 1700*time.Millisecond)
			col.mu.Lock()
			n := len(col.chunks)
			prev := -1.0
			for i, c := range col.chunks {
				if len(c.Data) == 0 {
					t.Errorf("chunk %d is empty", i)
				}
				if c.TimeMs < prev {
					t.Errorf("chunk %d timecode %.0f goes back from %.0f", i, c.TimeMs, prev)
				}
				prev = c.TimeMs
			}
			col.mu.Unlock()
			if n < 4 || n > 8 {
				t.Errorf("%d chunks for 1.7 s at a 300 ms timeslice", n)
			}
			if d := os.Getenv("SWEEF_REC_DUMP"); d != "" {
				_ = os.WriteFile(d+"/slice."+container, col.all(), 0o644)
			}
			p := decode(t, col.all())
			if secs := float64(len(p.Data[0])) / float64(p.SampleRate); secs < 1.4 || secs > 2.2 {
				t.Errorf("decoded %.2f s from the concatenated chunks", secs)
			}
		})
	}
}

func TestPauseResumeSkipsTheGap(t *testing.T) {
	in, _ := toneSource(48000, 1, 440)
	col := &collector{}
	r, err := New(Config{Container: "webm"}, []*AudioIn{in}, nil, col.add)
	if err != nil {
		t.Fatal(err)
	}
	r.Start()
	time.Sleep(700 * time.Millisecond)
	r.Pause()
	time.Sleep(900 * time.Millisecond)
	r.Resume()
	time.Sleep(700 * time.Millisecond)
	r.Stop()
	<-r.Done()
	p := decode(t, col.all())
	secs := float64(len(p.Data[0])) / float64(p.SampleRate)
	if secs < 1.2 || secs > 1.7 {
		t.Errorf("recorded %.2f s with a 0.9 s pause in 2.3 s of wall time, want about 1.4", secs)
	}
}

func TestStalledSourceIsPaddedToKeepTime(t *testing.T) {
	c := make(chan *capture.AudioChunk, 4)
	in := &AudioIn{C: c, Rate: 48000, Channels: 1, Cancel: func() {}}
	col := &collector{}
	r, err := New(Config{Container: "ogg"}, []*AudioIn{in}, nil, col.add)
	if err != nil {
		t.Fatal(err)
	}
	r.Start()
	feed := func(ms int) {
		for i := 0; i < ms/10; i++ {
			pcm := make([]int16, 480)
			for k := range pcm {
				pcm[k] = 3000
			}
			c <- &capture.AudioChunk{Rate: 48000, Channels: 1, PCM: pcm}
			time.Sleep(10 * time.Millisecond)
		}
	}
	feed(300)
	time.Sleep(700 * time.Millisecond) // the microphone stalls
	feed(300)
	r.Stop()
	<-r.Done()
	p := decode(t, col.all())
	if secs := float64(len(p.Data[0])) / float64(p.SampleRate); secs < 1.1 || secs > 1.6 {
		t.Errorf("a 0.7 s stall inside 1.3 s left %.2f s of audio", secs)
	}
}

func TestMixTwoSources(t *testing.T) {
	a, _ := toneSource(48000, 1, 440)
	b, _ := toneSource(48000, 1, 440)
	col := record(t, Config{Container: "ogg"}, []*AudioIn{a, b}, nil, 1000*time.Millisecond)
	p := decode(t, col.all())
	mid := p.Data[0][len(p.Data[0])/4 : len(p.Data[0])*3/4]
	if r := rms(mid); r < 0.30 || r > 0.40 { // two coherent 0.177 rms tones
		t.Errorf("mixed rms %.3f, want about 0.35", r)
	}
}

func TestResamplerKeepsLevelAndTone(t *testing.T) {
	for _, rate := range []int{8000, 16000, 22050, 32000, 44100, 96000} {
		rs := NewResampler(rate, 48000, 1)
		in := make([]float32, rate)
		for i := range in {
			in[i] = float32(0.5 * math.Sin(2*math.Pi*1000*float64(i)/float64(rate)))
		}
		var out []float32
		for i := 0; i < len(in); i += 441 {
			out = append(out, rs.Process(in[i:min(i+441, len(in))])...)
		}
		if n := len(out); n < 47000 || n > 48100 {
			t.Errorf("%d Hz: %d output samples for 1 s", rate, n)
		}
		mid := out[len(out)/4 : len(out)*3/4]
		if r := rms(mid); math.Abs(r-0.3536) > 0.01 {
			t.Errorf("%d Hz: rms %.4f, want 0.3536", rate, r)
		}
		if f := freq(mid, 48000); math.Abs(f-1000) > 3 {
			t.Errorf("%d Hz: tone %.1f Hz", rate, f)
		}
	}
	// a tone above the output Nyquist must not alias into the band
	rs := NewResampler(96000, 48000, 1)
	in := make([]float32, 96000)
	for i := range in {
		in[i] = float32(0.5 * math.Sin(2*math.Pi*30000*float64(i)/96000))
	}
	out := rs.Process(in)
	if r := rms(out[len(out)/4 : len(out)*3/4]); r > 0.01 {
		t.Errorf("30 kHz tone leaks through the downsampler at rms %.4f", r)
	}
}

func TestParseMime(t *testing.T) {
	yes := []string{"", "audio/webm", "audio/webm;codecs=opus", `audio/webm; codecs="opus"`, "AUDIO/WEBM;CODECS=OPUS", "video/webm", "video/webm;codecs=vp8",
		"video/webm;codecs=vp8,opus", "video/webm;codecs=opus,vp8", "video/webm;codecs=opus", "video/webm;codecs=vp8.0", "audio/ogg", "audio/ogg;codecs=opus"}
	no := []string{"audio/mp4", "video/mp4", "audio/mpeg", "video/webm;codecs=vp9", "video/webm;codecs=av01", "video/webm;codecs=avc1.42E01E", "audio/webm;codecs=vorbis",
		"audio/webm;codecs=vp8", "audio/ogg;codecs=vorbis", "audio/ogg;codecs=vp8", "video/ogg", "text/plain", "audio/webm;codecs=", "audio/wav", "image/png", "banana"}
	for _, s := range yes {
		if _, ok := ParseMime(s); !ok {
			t.Errorf("%q should be supported", s)
		}
	}
	for _, s := range no {
		if _, ok := ParseMime(s); ok {
			t.Errorf("%q should not be supported", s)
		}
	}
}

func TestOggPagesAreValid(t *testing.T) {
	o := NewOggOpus(1, 48000, 312)
	for i := 0; i < 300; i++ {
		o.AddPacket(bytes.Repeat([]byte{byte(i)}, 40+i%7*50), 960)
	}
	o.Finish(300*960 - 100)
	b := o.Take()
	pages, seq := 0, uint32(0)
	var last uint64
	for len(b) > 0 {
		if string(b[:4]) != "OggS" {
			t.Fatalf("page %d: no capture pattern", pages)
		}
		ns := int(b[26])
		size := 27 + ns
		for _, s := range b[27 : 27+ns] {
			size += int(s)
		}
		page := append([]byte(nil), b[:size]...)
		want := uint32(page[22]) | uint32(page[23])<<8 | uint32(page[24])<<16 | uint32(page[25])<<24
		page[22], page[23], page[24], page[25] = 0, 0, 0, 0
		var c uint32
		for _, x := range page {
			c = c<<8 ^ oggCRC[byte(c>>24)^x]
		}
		if c != want {
			t.Fatalf("page %d: crc %08x, want %08x", pages, c, want)
		}
		if got := uint32(b[18]) | uint32(b[19])<<8 | uint32(b[20])<<16 | uint32(b[21])<<24; got != seq {
			t.Fatalf("page %d has sequence %d", pages, got)
		}
		last = 0
		for i := 7; i >= 0; i-- {
			last = last<<8 | uint64(b[6+i])
		}
		seq++
		pages++
		b = b[size:]
	}
	if last != 312+300*960-100 {
		t.Errorf("final granule %d, want %d", last, 312+300*960-100)
	}
	if pages < 5 {
		t.Errorf("only %d pages", pages)
	}
}
