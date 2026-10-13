// Package vp8enc is a pure Go VP8 video encoder (RFC 6386) for real-time use.
//
// It produces key frames and inter frames with 16x16 macroblocks: intra
// DC/V/H/TM prediction, inter prediction from the last, golden and altref
// frames with zero, nearest, near and new motion vectors (full, half and
// quarter pel), the integer DCT and Walsh-Hadamard transforms, dead-zone
// quantisation at an adjustable quantiser, the normal loop filter, the boolean
// entropy coder with per-frame optimised token probabilities, up to eight
// token partitions, and rate control that targets a bitrate the caller can
// change at any time.
//
// Analysis (mode decision, transform, quantisation, reconstruction and token
// generation) runs in parallel over macroblock rows in a wavefront; entropy
// coding of the token partitions runs in parallel too, and the first partition
// and the loop filter are serial. Not implemented: split motion vectors, 4x4
// intra prediction (B_PRED), segmentation, temporal layers, multi-pass encoding.
//
// The encoder keeps the same reconstruction as a decoder (it shares the
// decoder's loop filter and tables from internal/media/vp8), so there is no
// drift. The tests decode its output with that decoder and with ffmpeg.
package vp8enc

import (
	"errors"
	"math"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sweefbrowser/sweef/internal/media/vp8"
)

var dcTab, acTab = vp8.QuantTables()

// Config configures an Encoder.
type Config struct {
	Width, Height int
	// FrameRate is the nominal frame rate used by rate control (default 30).
	FrameRate float64
	// Bitrate is the target in bits per second (default 800000).
	Bitrate int
	// KeyInterval inserts a key frame every N frames (0: only the first and on request).
	KeyInterval int
	// Threads limits the analysis goroutines (0: GOMAXPROCS).
	Threads int
	// MinQ and MaxQ bound the quantiser index (0..127); defaults 2 and 120.
	MinQ, MaxQ int
	// Speed sets the search effort: 0 fastest, 2 best quality; -1 (default) adapts to keep the
	// encode time below the frame interval.
	Speed int
	// TokenPartitions is log2 of the number of token partitions (0..3); -1 picks by resolution.
	TokenPartitions int
}

// Image is a planar I420 picture.
type Image struct {
	W, H              int
	Y, U, V           []byte
	YStride, UVStride int
}

// Packet is one encoded frame.
type Packet struct {
	Data     []byte
	Key      bool
	Q        int           // base quantiser index used
	EncTime  time.Duration // wall time spent encoding
	Duration time.Duration
}

// Stats are cumulative counters.
type Stats struct {
	Frames, KeyFrames, Dropped int
	Bytes                      int64
	EncodeTime                 time.Duration
	LastQ                      int
	Speed                      int
}

// Encoder encodes a sequence of I420 pictures of one size.
type Encoder struct {
	cfg Config

	mbw, mbh   int
	w16, h16   int
	miStride   int
	threads    int
	log2parts  int
	src        *frame // padded copy of the source (border 0 unused)
	cur        *frame
	last       *frame
	golden     *frame
	alt        *frame
	pool       []*frame
	probs      vp8.CoeffProbs // probabilities in force after the last committed frame
	defProbs   vp8.CoeffProbs
	updProbs   vp8.CoeffProbs
	mbOut      []mbOut
	infos      []mbInfo
	mvField    []mv
	prevField  []mv
	above      []nzCtx
	rowToks    [][]tokEntry
	workers    []*worker
	prog       []atomic.Int32
	waiters    atomic.Int32
	mu         sync.Mutex
	cond       *sync.Cond
	nextRow    atomic.Int32
	key        bool
	qs         quantSet
	q          int
	lambda     int
	staticSAD  int
	searchStep int
	speed      int
	intraBias  int

	intraCheckSAD int
	roundDC       int
	roundAC       int
	roundACInter  int

	frameNum      int
	sinceKey      int
	sinceGolden   int
	wantKey       bool
	rc            rateCtl
	st            Stats
	encEMA        float64 // smoothed encode seconds
	autoSpeed     bool
	partBufs      [][]byte
	firstBuf      []byte
	lastPackets   int
	refreshGolden bool
	nextProbs     vp8.CoeffProbs
	haveReference bool
}

// New creates an Encoder.
func New(cfg Config) (*Encoder, error) {
	if cfg.Width < 16 || cfg.Height < 16 || cfg.Width > 16383 || cfg.Height > 16383 {
		return nil, errors.New("vp8enc: unsupported frame size")
	}
	if cfg.FrameRate <= 0 {
		cfg.FrameRate = 30
	}
	if cfg.Bitrate <= 0 {
		cfg.Bitrate = 800000
	}
	if cfg.MinQ <= 0 {
		cfg.MinQ = 2
	}
	if cfg.MaxQ <= 0 || cfg.MaxQ > 127 {
		cfg.MaxQ = 120
	}
	e := &Encoder{cfg: cfg}
	e.cond = sync.NewCond(&e.mu)
	e.threads = cfg.Threads
	if e.threads <= 0 {
		e.threads = runtime.GOMAXPROCS(0)
	}
	e.defProbs = vp8.DefaultCoeffProbs()
	e.updProbs = vp8.CoeffUpdateProbs()
	e.autoSpeed = cfg.Speed < 0
	e.speed = cfg.Speed
	if e.autoSpeed {
		e.speed = 1
	}
	e.rc.init(float64(cfg.Bitrate), cfg.FrameRate, cfg.MinQ, cfg.MaxQ, cfg.Width*cfg.Height)
	e.resize(cfg.Width, cfg.Height)
	return e, nil
}

func (e *Encoder) resize(w, h int) {
	e.cfg.Width, e.cfg.Height = w, h
	e.mbw, e.mbh = (w+15)>>4, (h+15)>>4
	e.w16, e.h16 = e.mbw*16, e.mbh*16
	e.miStride = e.mbw + 1
	e.src = newFrame(e.w16, e.h16)
	e.pool = nil
	for i := 0; i < 4; i++ {
		e.pool = append(e.pool, newFrame(e.w16, e.h16))
	}
	e.cur, e.last, e.golden, e.alt = nil, nil, nil, nil
	n := e.mbw * e.mbh
	e.mbOut = make([]mbOut, n)
	e.infos = make([]mbInfo, e.miStride*(e.mbh+1))
	e.mvField = make([]mv, n)
	e.prevField = make([]mv, n)
	e.above = make([]nzCtx, e.mbw)
	e.rowToks = make([][]tokEntry, e.mbh)
	e.prog = make([]atomic.Int32, e.mbh)
	e.workers = nil
	for i := 0; i < e.threads; i++ {
		e.workers = append(e.workers, &worker{e: e})
	}
	switch {
	case e.cfg.TokenPartitions >= 0 && e.cfg.TokenPartitions <= 3 && e.cfg.TokenPartitions != 0:
		e.log2parts = e.cfg.TokenPartitions
	case e.cfg.TokenPartitions < 0 || e.cfg.TokenPartitions == 0 && e.cfg.Width*e.cfg.Height > 640*360:
		switch {
		case e.mbh >= 40:
			e.log2parts = 2
		case e.mbh >= 22:
			e.log2parts = 1
		default:
			e.log2parts = 0
		}
	}
	e.partBufs = make([][]byte, 1<<e.log2parts)
	e.probs = e.defProbs
	e.wantKey = true
	e.haveReference = false
}

// SetBitrate changes the target bitrate (bits per second).
func (e *Encoder) SetBitrate(bps int) {
	if bps < 20000 {
		bps = 20000
	}
	e.cfg.Bitrate = bps
	e.rc.setBitrate(float64(bps))
}

// SetFrameRate changes the nominal frame rate used by rate control.
func (e *Encoder) SetFrameRate(fps float64) {
	if fps < 1 {
		fps = 1
	}
	e.cfg.FrameRate = fps
	e.rc.setFPS(fps)
}

// RequestKeyFrame makes the next frame a key frame.
func (e *Encoder) RequestKeyFrame() { e.wantKey = true }

// Resize changes the frame size; the next frame is a key frame.
func (e *Encoder) Resize(w, h int) error {
	if w < 16 || h < 16 || w > 16383 || h > 16383 {
		return errors.New("vp8enc: unsupported frame size")
	}
	if w == e.cfg.Width && h == e.cfg.Height {
		return nil
	}
	e.resize(w, h)
	e.rc.setPixels(w * h)
	return nil
}

// Size returns the configured frame size.
func (e *Encoder) Size() (int, int) { return e.cfg.Width, e.cfg.Height }

// Stats returns the cumulative counters.
func (e *Encoder) Stats() Stats {
	s := e.st
	s.Speed = e.speed
	return s
}

func (e *Encoder) getFrame() *frame {
	for _, f := range e.pool {
		if f != e.last && f != e.golden && f != e.alt {
			return f
		}
	}
	f := newFrame(e.w16, e.h16)
	e.pool = append(e.pool, f)
	return f
}

// load copies img into the macroblock-aligned source planes, replicating the edges.
func (e *Encoder) load(img *Image) {
	copyPlane := func(dst *plane, src []byte, stride, w, h int) {
		for y := 0; y < dst.h; y++ {
			sy := y
			if sy >= h {
				sy = h - 1
			}
			row := dst.pix[dst.idx(0, y) : dst.idx(0, y)+dst.w]
			copy(row, src[sy*stride:sy*stride+w])
			for x := w; x < dst.w; x++ {
				row[x] = row[w-1]
			}
		}
	}
	copyPlane(e.src.y, img.Y, img.YStride, img.W, img.H)
	cw, ch := (img.W+1)/2, (img.H+1)/2
	copyPlane(e.src.u, img.U, img.UVStride, cw, ch)
	copyPlane(e.src.v, img.V, img.UVStride, cw, ch)
}

func (e *Encoder) waitRow(row, need int) {
	if int(e.prog[row].Load()) >= need {
		return
	}
	e.mu.Lock()
	e.waiters.Add(1)
	for int(e.prog[row].Load()) < need {
		e.cond.Wait()
	}
	e.waiters.Add(-1)
	e.mu.Unlock()
}

func (e *Encoder) publishRow(row, v int) {
	e.prog[row].Store(int32(v))
	if e.waiters.Load() > 0 {
		e.mu.Lock()
		e.cond.Broadcast()
		e.mu.Unlock()
	}
}

// Encode encodes one picture. It returns a nil packet (and nil error) when rate control drops the frame.
func (e *Encoder) Encode(img *Image) (*Packet, error) {
	if img.W != e.cfg.Width || img.H != e.cfg.Height {
		return nil, errors.New("vp8enc: picture size differs from the encoder size (call Resize)")
	}
	t0 := time.Now()
	e.load(img)

	key := e.wantKey || !e.haveReference || (e.cfg.KeyInterval > 0 && e.sinceKey >= e.cfg.KeyInterval)
	if !key && e.rc.shouldDrop() {
		e.rc.dropped()
		e.st.Dropped++
		return nil, nil
	}
	e.applySpeed()

	q := e.rc.startQ(key)
	var data []byte
	target := e.rc.target(key)
	attempts := 0
	for {
		attempts++
		e.setQ(q)
		data = e.encodeFrame(key)
		bits := float64(len(data) * 8)
		if attempts >= 3 {
			break
		}
		ratio := bits / target
		retry := false
		var nq int
		if key {
			if ratio > 1.4 && q < e.cfg.MaxQ {
				retry, nq = true, q+int(math.Ceil(22*math.Log(ratio/1.15)))
			} else if ratio < 0.55 && q > e.cfg.MinQ {
				retry, nq = true, q+int(math.Floor(22*math.Log(ratio/0.8)))
			}
		} else if ratio > 3 && q < e.cfg.MaxQ-4 {
			retry, nq = true, q+int(math.Ceil(22*math.Log(ratio/1.8)))
		}
		if !retry {
			break
		}
		nq = clampI(nq, e.cfg.MinQ, e.cfg.MaxQ)
		if nq == q {
			break
		}
		q = nq
	}
	e.commit(key)
	bits := float64(len(data) * 8)
	e.rc.update(key, q, bits, target)

	dt := time.Since(t0)
	e.st.Frames++
	e.st.Bytes += int64(len(data))
	e.st.EncodeTime += dt
	e.st.LastQ = q
	if key {
		e.st.KeyFrames++
	}
	e.encEMA = 0.9*e.encEMA + 0.1*dt.Seconds()
	if e.frameNum == 0 {
		e.encEMA = dt.Seconds()
	}
	e.frameNum++
	return &Packet{Data: data, Key: key, Q: q, EncTime: dt, Duration: time.Duration(float64(time.Second) / e.cfg.FrameRate)}, nil
}

func (e *Encoder) applySpeed() {
	if !e.autoSpeed {
		return
	}
	budget := 1 / e.cfg.FrameRate
	switch {
	case e.encEMA > 0.85*budget && e.speed > 0:
		e.speed--
		e.encEMA *= 0.9
	case e.encEMA < 0.35*budget && e.speed < 2 && e.frameNum > 10:
		e.speed++
		e.encEMA *= 1.2
	}
}

func (e *Encoder) setQ(q int) {
	e.q = clampI(q, 0, 127)
	e.qs = newQuantSet(e.q)
	ac := int(e.qs.y1ac)
	e.lambda = ac/6 + 1
	e.staticSAD = clampI(8*ac, 256, 700)
	e.intraCheckSAD = 3 * 256
	e.intraBias = 20 * e.lambda
	e.roundDC = 118
	e.roundAC = 104
	e.roundACInter = 88
	switch e.speed {
	case 0:
		e.searchStep = 2
	case 1:
		e.searchStep = 4
	default:
		e.searchStep = 8
	}
}

// lfLevel picks the loop filter strength from the quantiser.
func lfLevel(q int, key bool) int {
	l := q * 5 / 12
	if q < 8 {
		l = 0
	}
	if l > 63 {
		l = 63
	}
	return l
}

// encodeFrame analyses and packs one frame at the current quantiser, leaving the reconstruction in e.cur.
func (e *Encoder) encodeFrame(key bool) []byte {
	e.key = key
	e.cur = e.getFrame()
	e.cur.y.intraBorders()
	e.cur.u.intraBorders()
	e.cur.v.intraBorders()
	for i := range e.above {
		e.above[i] = nzCtx{}
	}
	for i := range e.prog {
		e.prog[i].Store(0)
	}
	e.nextRow.Store(0)
	for _, w := range e.workers {
		w.cnt = tokenCounts{}
		w.nIntra, w.nSkip, w.nNoSkip, w.newMV = 0, 0, 0, 0
		w.nRef = [4]int{}
	}
	var wg sync.WaitGroup
	for _, w := range e.workers {
		wg.Add(1)
		go func(w *worker) {
			defer wg.Done()
			for {
				r := int(e.nextRow.Add(1)) - 1
				if r >= e.mbh {
					return
				}
				w.codeRow(r)
			}
		}(w)
	}
	wg.Wait()

	// statistics
	var cnt tokenCounts
	var nIntra, nSkip, nNoSkip int
	var nRef [4]int
	for _, w := range e.workers {
		cnt.add(&w.cnt)
		nIntra += w.nIntra
		nSkip += w.nSkip
		nNoSkip += w.nNoSkip
		for i := range nRef {
			nRef[i] += w.nRef[i]
		}
	}
	data := e.pack(key, &cnt, nIntra, nSkip, nNoSkip, nRef)

	// loop filter over the reconstruction
	if lvl := lfLevel(e.q, key); lvl > 0 {
		e.filter(lvl, key)
	}
	e.cur.extend()
	return data
}

func (e *Encoder) filter(level int, key bool) {
	n := e.mbw * e.mbh
	levels := make([]uint8, n)
	inner := make([]bool, n)
	for i := range levels {
		levels[i] = uint8(level)
		inner[i] = !e.mbOut[i].skip
	}
	img := imageOf(e.cur)
	vp8.FilterFrame(img, vp8.FilterParams{KeyFrame: key, MBW: e.mbw, MBH: e.mbh, Level: levels, Inner: inner})
}

// commit makes the just-encoded frame the new reference state.
func (e *Encoder) commit(key bool) {
	cur := e.cur
	if key {
		e.last, e.golden, e.alt = cur, cur, cur
		e.sinceKey = 0
		e.sinceGolden = 0
		e.wantKey = false
		e.haveReference = true
	} else {
		e.sinceKey++
		e.sinceGolden++
		if e.refreshGolden {
			e.alt = e.golden
			e.golden = cur
			e.sinceGolden = 0
		}
		e.last = cur
	}
	e.probs = e.nextProbs
	e.mvField, e.prevField = e.prevField, e.mvField
	if key {
		for i := range e.prevField {
			e.prevField[i] = mv{}
		}
	}
}
