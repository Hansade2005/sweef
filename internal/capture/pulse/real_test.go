package pulse

import (
	"errors"
	"math"
	"os"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/capture"
)

// TestRealServerPipeSource talks to a real PulseAudio (or PipeWire-pulse) server. It is skipped unless
// SWEEF_TEST_PULSE_FIFO names the named pipe that feeds a source of the server under test:
//
//	mkfifo /tmp/mic.fifo
//	pulseaudio -n --daemonize=no --exit-idle-time=-1 --load=module-native-protocol-unix \
//	  "--load=module-pipe-source file=/tmp/mic.fifo source_name=testmic rate=48000 channels=1 format=s16le" \
//	  --load="module-null-sink sink_name=nullout"
//	SWEEF_TEST_PULSE_FIFO=/tmp/mic.fifo PULSE_SERVER=unix:$XDG_RUNTIME_DIR/pulse/native go test -run RealServer ./internal/capture/pulse
//
// (the server runs as another user in the author's check: set PULSE_COOKIE to its cookie file.) The test writes a
// 440 Hz tone into the pipe and checks that the tone arrives through the record stream.
func TestRealServerPipeSource(t *testing.T) {
	fifo := os.Getenv("SWEEF_TEST_PULSE_FIFO")
	if fifo == "" {
		t.Skip("SWEEF_TEST_PULSE_FIFO not set; no real server to test against")
	}
	p := NewProvider()
	p.TTL = time.Millisecond
	devs := p.Devices()
	var mic *capture.DeviceInfo
	for i, d := range devs {
		t.Logf("device %q (%s) group %q", d.Label, d.ID, d.GroupID)
		if d.ID == "testmic" {
			mic = &devs[i]
		}
		if len(d.ID) > 8 && d.ID[len(d.ID)-8:] == ".monitor" {
			t.Errorf("monitor source %q listed as a microphone", d.ID)
		}
	}
	if mic == nil {
		t.Fatalf("testmic not listed: %+v", devs)
	}
	caps, ok := p.AudioCaps("testmic")
	if !ok || len(caps.Rates) != 1 || caps.Rates[0] != 48000 {
		t.Fatalf("caps %+v", caps)
	}
	src, err := p.OpenAudio("testmic")
	if err != nil {
		t.Fatal(err)
	}
	ch, err := src.Start(capture.AudioConstraints{Channels: 1})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	stop := make(chan struct{})
	wdone := make(chan struct{})
	go func() {
		defer close(wdone)
		f, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		defer f.Close()
		const rate, freq = 48000, 440.0
		n := 0
		tick := time.NewTicker(10 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
			}
			buf := make([]byte, 480*2)
			for i := 0; i < 480; i++ {
				v := int16(8000 * math.Sin(2*math.Pi*freq*float64(n)/rate))
				buf[2*i], buf[2*i+1] = byte(v), byte(uint16(v)>>8)
				n++
			}
			_ = f.SetWriteDeadline(time.Now().Add(100 * time.Millisecond)) // a suspended source stops reading the pipe
			_, _ = f.Write(buf)
		}
	}()
	stopWriter := func() { close(stop); <-wdone }

	var pcm []int16
	var last time.Duration = -1
	deadline := time.After(10 * time.Second)
	for len(pcm) < 48000 { // one second of audio
		select {
		case c, ok := <-ch:
			if !ok {
				t.Fatalf("stream closed early: %v", src.Err())
			}
			if c.Rate != 48000 || c.Channels != 1 || len(c.PCM) != 480 {
				t.Fatalf("chunk rate=%d ch=%d n=%d", c.Rate, c.Channels, len(c.PCM))
			}
			if c.PTS <= last {
				t.Fatalf("pts not increasing: %v after %v", c.PTS, last)
			}
			last = c.PTS
			pcm = append(pcm, c.PCM...)
		case <-deadline:
			t.Fatalf("only %d samples in 10 s", len(pcm))
		}
	}
	// the server may start the stream mid-tone and drop a little at the start: measure the frequency from the
	// last half second with rising zero crossings
	tail := pcm[len(pcm)-24000:]
	cross := 0
	for i := 1; i < len(tail); i++ {
		if tail[i-1] < 0 && tail[i] >= 0 {
			cross++
		}
	}
	if cross < 215 || cross > 225 { // 440 Hz * 0.5 s = 220
		t.Fatalf("%d rising crossings in 0.5 s, want about 220 (a 440 Hz tone)", cross)
	}
	var peak int16
	for _, s := range tail {
		if s > peak {
			peak = s
		}
	}
	if peak < 7000 || peak > 9000 {
		t.Errorf("peak %d, the tone was written with amplitude 8000", peak)
	}
	stopWriter()
	if err := src.Stop(); err != nil {
		t.Fatal(err)
	}
	for range ch {
	}

	// a stereo stream at another rate (the null sink's monitor is silent but always there)
	c, err := Dial(Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	st, err := c.openRecord("nullout.monitor", 44100, 2, 3528)
	if err != nil {
		t.Fatalf("stereo stream: %v", err)
	}
	select {
	case blk := <-st.data:
		if len(blk)%4 != 0 || len(blk) == 0 {
			t.Fatalf("stereo block of %d bytes", len(blk))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no stereo data")
	}
	st.close()

	// a source that does not exist maps to ErrNotFound
	bad := &MicSource{opts: Options{}, src: Source{Name: "no-such-source", Rate: 48000, Channels: 1}}
	if _, err := bad.Start(capture.AudioConstraints{}); !errors.Is(err, capture.ErrNotFound) {
		t.Fatalf("unknown source: %v", err)
	}
}
