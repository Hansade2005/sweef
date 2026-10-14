package rtcmedia

import (
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"

	"github.com/sweefbrowser/sweef/internal/capture"
	"github.com/sweefbrowser/sweef/internal/media/vp8"
	"github.com/sweefbrowser/sweef/internal/media/vp8enc"
	"github.com/sweefbrowser/sweef/internal/rtcmedia/testsrc"
)

// newTestReceiver builds a VideoReceiver without a transport and without goroutines, so the reassembly,
// ordering and loss policy can be driven packet by packet with a clock the test controls.
// testSentMu guards the RTCP a test receiver sent: PLIs go out from their own goroutine.
var testSentMu sync.Mutex

func newTestReceiver() (*VideoReceiver, *[]rtcp.Packet) {
	mu := &testSentMu
	var sent []rtcp.Packet
	r := &VideoReceiver{
		ssrc: 1234, usePLI: true, pkts: map[uint32]*vpkt{}, missing: map[uint32]*nackInfo{}, nackRTT: 0.05,
		dec: vp8.NewDecoder(), muted: true, needKey: true, delay: 0.03,
		deliverSig: make(chan struct{}, 1), outSig: make(chan struct{}, 1), done: make(chan struct{}),
	}
	r.writeRTCP = func(p []rtcp.Packet) error {
		mu.Lock()
		sent = append(sent, p...)
		mu.Unlock()
		return nil
	}
	return r, &sent
}

// stream is a sequence of encoded frames as RTP packets.
type stream struct {
	pkts   []*rtp.Packet
	frames [][]int // indexes into pkts per frame
	keys   []bool
}

func makeStream(t *testing.T, n int, keyAt map[int]bool, firstSeq uint16) *stream {
	enc, err := vp8enc.New(vp8enc.Config{Width: 160, Height: 96, Bitrate: 400000, FrameRate: 30, Speed: 1})
	if err != nil {
		t.Fatal(err)
	}
	s := &stream{}
	seq := firstSeq
	ts := uint32(1000)
	pid := uint16(0)
	for i := 0; i < n; i++ {
		if keyAt[i] {
			enc.RequestKeyFrame()
		}
		f := testsrc.Pattern(160, 96, uint32(i))
		pk, err := enc.Encode(&vp8enc.Image{W: f.W, H: f.H, Y: f.Y, U: f.U, V: f.V, YStride: f.YStride, UVStride: f.UVStride})
		if err != nil || pk == nil {
			t.Fatal(err)
		}
		pls := vp8Packetize(pk.Data, pk.PartStarts, pid, false, 300) // small payloads: several packets per frame
		pid = (pid + 1) & 0x7fff
		var idx []int
		for j, pl := range pls {
			p := &rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: ts, Marker: j == len(pls)-1}, Payload: pl}
			seq++
			idx = append(idx, len(s.pkts))
			s.pkts = append(s.pkts, p)
		}
		s.frames = append(s.frames, idx)
		s.keys = append(s.keys, pk.Key)
		ts += 3000
	}
	return s
}

// drive runs the receiver's decode step at time now.
func drive(r *VideoReceiver, now time.Time) int {
	n := 0
	for {
		f := r.next(now)
		if f == nil {
			return n
		}
		r.decode(f, now)
		n++
	}
}

func TestReceiverInOrder(t *testing.T) {
	s := makeStream(t, 12, nil, 65530) // the sequence numbers wrap inside the stream
	r, sent := newTestReceiver()
	now := time.Now()
	decoded := 0
	for _, p := range s.pkts {
		r.insert(p, now)
		now = now.Add(time.Millisecond)
		decoded += drive(r, now)
	}
	if decoded != 12 || r.st.FramesDecoded != 12 || r.st.KeyFramesDecoded != 1 || r.st.FramesDropped != 0 {
		t.Fatalf("decoded %d (stats %+v)", decoded, r.st)
	}
	if len(r.outQ) != 12 {
		t.Fatalf("%d frames queued for playout", len(r.outQ))
	}
	if n := len(*sent); n != 0 {
		t.Errorf("sent %d RTCP packets for a clean stream", n)
	}
	if len(r.pkts) != 0 {
		t.Errorf("%d packets left in the store", len(r.pkts))
	}
}

func TestReceiverReorderAndDuplicates(t *testing.T) {
	s := makeStream(t, 12, nil, 100)
	rnd := rand.New(rand.NewSource(3))
	order := make([]int, len(s.pkts))
	for i := range order {
		order[i] = i
	}
	// shuffle inside windows of 4 packets: network reordering
	for i := 0; i+4 <= len(order); i += 4 {
		rnd.Shuffle(4, func(a, b int) { order[i+a], order[i+b] = order[i+b], order[i+a] })
	}
	r, _ := newTestReceiver()
	now := time.Now()
	for k, i := range order {
		r.insert(s.pkts[i], now)
		if k%5 == 0 {
			r.insert(s.pkts[i], now) // a duplicate
		}
		now = now.Add(time.Millisecond)
		drive(r, now)
	}
	if r.st.FramesDecoded != 12 || r.st.FramesDropped != 0 {
		t.Fatalf("decoded %d dropped %d (%+v)", r.st.FramesDecoded, r.st.FramesDropped, r.st)
	}
	if r.st.PacketsDiscarded == 0 {
		t.Errorf("duplicates were not discarded")
	}
}

func TestReceiverLossAndKeyFrameResync(t *testing.T) {
	s := makeStream(t, 24, map[int]bool{14: true}, 5000)
	r, sent := newTestReceiver()
	now := time.Now()
	lost := s.frames[5][1] // a packet in the middle of frame 5 is lost for good
	for i, p := range s.pkts {
		if i == lost {
			continue
		}
		r.insert(p, now)
		now = now.Add(2 * time.Millisecond)
		drive(r, now)
	}
	// let the wait budget run out
	for k := 0; k < 40; k++ {
		now = now.Add(20 * time.Millisecond)
		drive(r, now)
	}
	if !r.haveRef || r.needKey {
		t.Fatalf("did not resynchronise on the key frame (haveRef %v needKey %v)", r.haveRef, r.needKey)
	}
	countPLI := func() int {
		testSentMu.Lock()
		defer testSentMu.Unlock()
		n := 0
		for _, p := range *sent {
			if _, ok := p.(*rtcp.PictureLossIndication); ok {
				n++
			}
		}
		return n
	}
	// the PLI is written from its own goroutine: give a slow scheduler (Windows runners) time to run it
	for i := 0; i < 200 && countPLI() == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	plis := countPLI()
	if plis == 0 {
		t.Errorf("no PLI was sent after the loss")
	}
	// frames 0-4 decoded, 5-13 not (5 lost, the rest depend on it), 14.. decoded
	if r.st.FramesDecoded != 5+(24-14) {
		t.Errorf("decoded %d frames, want %d", r.st.FramesDecoded, 5+(24-14))
	}
	if r.st.KeyFramesDecoded != 2 || r.st.DecodeErrors != 0 {
		t.Errorf("key frames %d, decode errors %d", r.st.KeyFramesDecoded, r.st.DecodeErrors)
	}
	t.Logf("%d frames decoded, %d dropped, %d PLI, %d NACK requests", r.st.FramesDecoded, r.st.FramesDropped, plis, r.st.NACKCount)
}

func TestReceiverNackSchedule(t *testing.T) {
	s := makeStream(t, 6, nil, 900)
	r, _ := newTestReceiver()
	now := time.Now()
	gap := s.frames[2][0]
	for i, p := range s.pkts {
		if i == gap {
			continue
		}
		r.insert(p, now)
	}
	if _, ok := r.missing[uint32(s.pkts[gap].SequenceNumber)+1<<16]; !ok {
		t.Fatalf("the missing packet is not tracked: %v", r.missing)
	}
	if p := r.collectNacks(now.Add(5 * time.Millisecond)); p != nil {
		t.Errorf("asked too early (reordering grace): %v", p)
	}
	first := r.collectNacks(now.Add(15 * time.Millisecond))
	if len(first) != 1 || first[0].PacketID != s.pkts[gap].SequenceNumber {
		t.Fatalf("first request %v, want seq %d", first, s.pkts[gap].SequenceNumber)
	}
	if p := r.collectNacks(now.Add(30 * time.Millisecond)); p != nil {
		t.Errorf("repeated before a round trip: %v", p)
	}
	tries := 1
	for k := 1; k < 30; k++ {
		if p := r.collectNacks(now.Add(time.Duration(15+70*k) * time.Millisecond)); p != nil {
			tries++
		}
	}
	if tries != 6 {
		t.Errorf("%d attempts, want 6", tries)
	}
	// the packet arrives: no more requests
	r2, _ := newTestReceiver()
	for i, p := range s.pkts {
		if i != gap {
			r2.insert(p, now)
		}
	}
	r2.collectNacks(now.Add(15 * time.Millisecond))
	r2.insert(s.pkts[gap], now.Add(60*time.Millisecond))
	if len(r2.missing) != 0 {
		t.Errorf("still missing after the retransmission arrived: %v", r2.missing)
	}
	if r2.nackRTT <= 0.0 || r2.nackRTT > 0.06 {
		t.Errorf("rtt estimate %v", r2.nackRTT)
	}
}

func TestReceiverHugeGapAsksForKeyFrame(t *testing.T) {
	s := makeStream(t, 3, nil, 10)
	r, sent := newTestReceiver()
	now := time.Now()
	r.insert(s.pkts[0], now)
	far := *s.pkts[len(s.pkts)-1]
	far.SequenceNumber += 3000
	r.insert(&far, now)
	if !r.needKey || len(r.missing) != 0 {
		t.Errorf("needKey %v, missing %d", r.needKey, len(r.missing))
	}
	time.Sleep(10 * time.Millisecond)
	found := false
	for _, p := range *sent {
		if _, ok := p.(*rtcp.PictureLossIndication); ok {
			found = true
		}
	}
	if !found {
		t.Errorf("no PLI after a 3000 packet gap")
	}
}

var _ = capture.Frame{}
