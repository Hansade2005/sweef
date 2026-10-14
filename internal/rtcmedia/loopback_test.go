package rtcmedia

import (
	"image"
	"math"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"

	"github.com/sweefbrowser/sweef/internal/capture"
	"github.com/sweefbrowser/sweef/internal/rtcmedia/testsrc"
)

func testSettings() webrtc.SettingEngine {
	se := webrtc.SettingEngine{}
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	se.SetIncludeLoopbackCandidate(true)
	// "lo" on Linux, "lo0" on macOS, "Loopback Pseudo-Interface 1" on Windows
	se.SetInterfaceFilter(func(name string) bool { return strings.HasPrefix(strings.ToLower(name), "lo") })
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	return se
}

// pair is two connected peer connections on loopback, signalled by the test.
type pair struct {
	a, b       *webrtc.PeerConnection
	apiA, apiB *API
}

func newPair(t testing.TB, setup func(a, b *pair)) *pair {
	t.Helper()
	p := &pair{}
	var err error
	if p.apiA, err = NewAPI(testSettings(), true); err != nil {
		t.Fatal(err)
	}
	if p.apiB, err = NewAPI(testSettings(), true); err != nil {
		t.Fatal(err)
	}
	if p.a, err = p.apiA.NewPeerConnection(webrtc.Configuration{}); err != nil {
		t.Fatal(err)
	}
	if p.b, err = p.apiB.NewPeerConnection(webrtc.Configuration{}); err != nil {
		t.Fatal(err)
	}
	return p
}

// connect negotiates a -> b (offer from a) without trickle.
func (p *pair) connect(t testing.TB) {
	t.Helper()
	offer, err := p.a.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	done := webrtc.GatheringCompletePromise(p.a)
	if err := p.a.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	gathered(t, done)
	if err := p.b.SetRemoteDescription(*p.a.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	answer, err := p.b.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	doneB := webrtc.GatheringCompletePromise(p.b)
	if err := p.b.SetLocalDescription(answer); err != nil {
		t.Fatal(err)
	}
	gathered(t, doneB)
	if err := p.a.SetRemoteDescription(*p.b.LocalDescription()); err != nil {
		t.Fatal(err)
	}
}

// gathered waits for ICE gathering, but never forever: a missing candidate must fail the test, not hang it.
func gathered(t testing.TB, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("ICE gathering did not complete in 30 s (no loopback candidates?)")
	}
}

func (p *pair) close() { _ = p.a.Close(); _ = p.b.Close() }

// frameRecorder is a FrameSink that keeps the frames' counters.
type frameRecorder struct {
	mu       sync.Mutex
	n        int
	last     *image.YCbCr
	counters []uint16
	bad      int
	w, h     int
}

func (r *frameRecorder) PushFrame(img *image.YCbCr) {
	c, ok := testsrc.ReadCounter(img)
	r.mu.Lock()
	r.n++
	r.last = img
	b := img.Bounds()
	r.w, r.h = b.Dx(), b.Dy()
	if ok {
		r.counters = append(r.counters, c)
	} else {
		r.bad++
	}
	r.mu.Unlock()
}

func (r *frameRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

func waitFor(t testing.TB, d time.Duration, what string, cond func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func psnrToFrame(img *image.YCbCr, ref *capture.Frame) float64 {
	b := img.Bounds()
	if b.Dx() != ref.W || b.Dy() != ref.H {
		return -1
	}
	var se float64
	for y := 0; y < ref.H; y++ {
		for x := 0; x < ref.W; x++ {
			d := float64(img.Y[(b.Min.Y+y)*img.YStride+b.Min.X+x]) - float64(ref.Y[y*ref.YStride+x])
			se += d * d
		}
	}
	if se == 0 {
		return 99
	}
	return 10 * math.Log10(255*255*float64(ref.W*ref.H)/se)
}

// TestVideoLoopback sends the synthetic pattern from one peer to the other and checks what arrives.
func TestVideoLoopback(t *testing.T) {
	p := newPair(t, nil)
	defer p.close()
	lt := NewLocalTrack(webrtc.RTPCodecTypeVideo, "v0", "s0")
	rs, err := p.a.AddTrack(lt)
	if err != nil {
		t.Fatal(err)
	}
	lt.ReadyWhenConnected(rs)
	est := <-p.apiA.Estimator
	vs := NewVideoSender(lt, est)
	go RunRTCP(rs, vs.HandleRTCP)
	defer vs.Close()
	src := testsrc.NewVideo(640, 360, 30)
	src.Keep = 200
	if err := vs.SetSource(src, capture.VideoConstraints{Width: 640, Height: 360, FrameRate: 30}); err != nil {
		t.Fatal(err)
	}
	rec := &frameRecorder{}
	var vr *VideoReceiver
	gotTrack := make(chan struct{})
	p.b.OnTrack(func(tr *webrtc.TrackRemote, rcv *webrtc.RTPReceiver) {
		vr = NewVideoReceiver(tr, rcv, func(pk []rtcp.Packet) error { return p.b.WriteRTCP(pk) }, VideoCallbacks{})
		vr.AddSink(rec)
		close(gotTrack)
	})
	p.connect(t)
	waitFor(t, 10*time.Second, "first track", func() bool {
		select {
		case <-gotTrack:
			return true
		default:
			return false
		}
	})
	defer vr.Close()
	waitFor(t, 10*time.Second, "frames", func() bool { return rec.count() > 10 })
	start := rec.count()
	time.Sleep(5 * time.Second)
	got := rec.count() - start
	rec.mu.Lock()
	cs := append([]uint16(nil), rec.counters...)
	last := rec.last
	w, h := rec.w, rec.h
	rec.mu.Unlock()
	if w != 640 || h != 360 {
		t.Errorf("remote size %dx%d (adaptation may have reduced it)", w, h)
	}
	// progression: counters must be increasing, with <5% of the source frames missing
	missing := 0
	for i := 1; i < len(cs); i++ {
		d := int(int16(cs[i] - cs[i-1]))
		if d <= 0 {
			t.Fatalf("counter went backwards: %d then %d", cs[i-1], cs[i])
		}
		missing += d - 1
	}
	total := len(cs) + missing
	t.Logf("received %d frames in 5 s (%.1f fps); %d of %d source frames skipped (%.1f%%); bad counters %d", got, float64(got)/5, missing, total, 100*float64(missing)/float64(total), rec.bad)
	if float64(missing) > 0.05*float64(total) {
		t.Errorf("too many dropped frames: %d of %d", missing, total)
	}
	if c, ok := testsrc.ReadCounter(last); ok {
		if ref := src.Kept(c); ref != nil {
			ps := psnrToFrame(last, ref)
			t.Logf("PSNR-Y of the last remote frame against the source frame %d: %.2f dB", c, ps)
			if ps < 28 {
				t.Errorf("PSNR %.2f dB", ps)
			}
		}
	}
	st := vs.Stats()
	t.Logf("sender: %d frames encoded (%d key), %.1f fps, target %.0f kbps, q %d, encode %.1f ms/frame, reason %s",
		st.FramesEncoded, st.KeyFramesEncoded, st.FramesPerSecond, st.TargetBitrate/1000, st.Q, 1000*st.TotalEncodeTime/float64(st.FramesEncoded), st.QualityLimitationReason)
	rst := vr.Stats()
	t.Logf("receiver: %d decoded, %d dropped, jitter buffer delay %.0f ms, pli %d", rst.FramesDecoded, rst.FramesDropped, 1000*rst.JitterBufferDelay/float64(max(rst.JitterBufferEmittedCount, 1)), rst.PLICount)
}
