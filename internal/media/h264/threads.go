package h264

// Frame threading. With SetThreads(n) a decoder reconstructs up to n pictures at the same time, the way
// FFmpeg's frame threads do: the calling goroutine reads the NAL units, parses the slice headers, builds the
// reference lists and does the reference marking (all of it needs only headers), and hands the macroblock
// data of each picture to a goroutine of its own. That goroutine decodes the macroblocks, deblocks the rows
// behind them and publishes how many rows of the picture are final; a picture that refers to it waits (for
// the few rows its motion vectors reach) instead of for the whole picture. The result is bit-identical to
// the plain decoder (tests compare them on every stream in the repository).
//
// Only the pictures this is safe and worthwhile for are threaded: frame pictures without MBAFF, 4:2:0,
// without slice groups, data partitioning, SP/SI slices or separate colour planes. Anything else waits for
// every picture in flight and is decoded on the calling goroutine as before.

import (
	"sync"
)

// progDone is the progress of a picture that is complete.
const progDone = 1 << 30

// waitRows blocks until the first n luma rows of the picture are final.
func (p *Picture) waitRows(n int) {
	if p.prog.Load() >= int32(n) {
		return
	}
	p.pmu.Lock()
	for p.prog.Load() < int32(n) {
		p.pcond.Wait()
	}
	p.pmu.Unlock()
}

// setProgress publishes that the first n luma rows of the picture are final.
func (p *Picture) setProgress(n int) {
	p.pmu.Lock()
	p.prog.Store(int32(n))
	p.pcond.Broadcast()
	p.pmu.Unlock()
}

// complete reports whether the picture is entirely reconstructed.
func (p *Picture) complete() bool { return p.prog.Load() == progDone }

// tablePool recycles the per-macroblock tables of threaded pictures.
type tablePool struct {
	mu   sync.Mutex
	free []*planeTables
}

func (tp *tablePool) get(n int) *planeTables {
	tp.mu.Lock()
	for len(tp.free) > 0 {
		t := tp.free[len(tp.free)-1]
		tp.free = tp.free[:len(tp.free)-1]
		if len(t.sliceTable) == n {
			tp.mu.Unlock()
			return t
		}
	}
	tp.mu.Unlock()
	t := &planeTables{
		sliceTable:      make([]uint16, n),
		qpTable:         make([]int8, n),
		nz:              make([]uint8, n*48),
		cbpTable:        make([]uint16, n),
		chromaPredTable: make([]uint8, n),
		ipm:             make([]int8, n*16),
		directTable:     make([]uint8, n*4),
		mbNZMask:        make([]uint16, n),
		mbBypass:        make([]bool, n),
	}
	for l := 0; l < 2; l++ {
		t.mvd[l] = make([][2]uint8, n*16)
	}
	return t
}

func (tp *tablePool) put(t *planeTables) {
	tp.mu.Lock()
	if len(tp.free) < 8 {
		tp.free = append(tp.free, t)
	}
	tp.mu.Unlock()
}

// clear zeroes the tables, so that a recycled set is what a new one would be.
func (t *planeTables) clear() {
	clear(t.sliceTable)
	clear(t.qpTable)
	clear(t.nz)
	clear(t.cbpTable)
	clear(t.chromaPredTable)
	clear(t.ipm)
	clear(t.directTable)
	clear(t.mbNZMask)
	clear(t.mbBypass)
	clear(t.mvd[0])
	clear(t.mvd[1])
}

// sliceJob is the macroblock data of one slice, with the decoder state it is decoded with (a copy of the
// calling decoder's state made when the slice header had been processed).
type sliceJob struct {
	d *Decoder
	r *bitReader
	h *sliceHeader
}

// picCtx is one picture being reconstructed on its own goroutine.
type picCtx struct {
	pic        *Picture
	jobs       chan sliceJob
	done       chan struct{}
	pool       *tablePool
	tabs       *planeTables
	mbW, mbH   int
	mbDone     int  // macroblocks decoded so far (in raster order while lag is set)
	deblocked  int  // macroblock rows deblocked
	lag        bool // slices arrived in raster order: rows are deblocked right behind the decoding
	decoded    int  // macroblocks decoded in all slices
	failed     bool
	last       *Decoder // the state of the last slice (or of the picture start when there was none)
	concealSrc *Picture
	closed     bool // jobs is closed (main goroutine only)
}

// SetThreads sets how many pictures may be reconstructed at the same time (0 or 1: none, the plain decoder).
func (d *Decoder) SetThreads(n int) {
	if n < 2 {
		n = 0
	}
	if n != d.threads {
		d.Flush()
	}
	d.threads = n
}

// Flush waits for every picture that is still being reconstructed.
func (d *Decoder) Flush() {
	d.abortCtx()
	for _, c := range d.inflight {
		<-c.done
	}
	d.inflight = nil
}

// abortCtx ends the picture whose macroblock data is still being collected (the stream was reset or broke
// off): whatever was queued is finished and its rows are published, so that nothing waits for it forever.
func (d *Decoder) abortCtx() {
	if c := d.ctx; c != nil {
		d.ctx = nil
		if !c.closed {
			c.closed = true
			close(c.jobs)
		}
	}
}

// canThread reports whether the picture the slice starts can be reconstructed on its own goroutine.
func (d *Decoder) canThread(h *sliceHeader) bool {
	sps := h.sps
	return d.threads >= 2 && h.structure == picFrame && !sps.MBAFF && !sps.SeparatePlanes && sps.ChromaArrayType() == 1 &&
		h.pps.NumSliceGroups <= 1 && (h.nalType == 1 || h.nalType == 5) && h.sliceType != sliceSP && h.sliceType != sliceSI &&
		!d.firstField
}

// beginThreaded sets the picture just started up for its own goroutine: private macroblock tables, a clean
// slice table, progress 0, and a free slot among the pictures in flight.
func (d *Decoder) beginThreaded() {
	// at most d.threads pictures at a time
	for len(d.inflight) > 0 {
		select {
		case <-d.inflight[0].done:
			d.inflight = d.inflight[1:]
			continue
		default:
		}
		if len(d.inflight) < d.threads {
			break
		}
		<-d.inflight[0].done
		d.inflight = d.inflight[1:]
	}
	n := len(d.sliceTable)
	if d.tpool == nil {
		d.tpool = new(tablePool)
	}
	t := d.tpool.get(n)
	t.clear()
	d.loadTables(t)
	d.pooled = true
	for i := range d.sliceTable {
		d.sliceTable[i] = 0xffff
	}
	pic := d.curPic
	pic.pcond = sync.NewCond(&pic.pmu)
	pic.prog.Store(0)
	c := &picCtx{
		pic:  pic,
		jobs: make(chan sliceJob, 64),
		done: make(chan struct{}),
		pool: d.tpool,
		tabs: t,
		mbW:  d.mbW,
		mbH:  d.mbH,
		lag:  true,
	}
	d.ctx = c
	d.inflight = append(d.inflight, c)
	go c.run() // from here on Flush can wait for it, whatever happens to the rest of startPicture
}

// cloneForSlice copies the decoder state for the goroutine that decodes a slice. Everything that is mutated
// in place afterwards by the calling goroutine (the reference lists, the slice parameters) is copied; the
// tables and pictures are shared on purpose.
func (d *Decoder) cloneForSlice() *Decoder {
	c := new(Decoder)
	*c = *d
	for l := 0; l < 2; l++ {
		c.refList[l] = append([]refEntry(nil), d.refList[l]...)
		c.refListField[l] = nil
	}
	c.slices = append([]sliceParams(nil), d.slices...)
	c.shortRef = nil
	c.inflight = nil
	c.decodedMB = 0
	c.pending = nil
	return c
}

// threadedSlice hands the macroblock data of the slice (its header processed) to the picture's goroutine.
func (d *Decoder) threadedSlice(h *sliceHeader, r *bitReader) error {
	if h.nalType != 1 && h.nalType != 5 || h.pps.NumSliceGroups > 1 || h.sliceType == sliceSP || h.sliceType == sliceSI {
		return ErrUnsupported // a picture that started threaded cannot take it
	}
	c := d.ctx
	cl := d.cloneForSlice()
	cl.ctx = c
	c.jobs <- sliceJob{d: cl, r: r, h: h}
	return nil
}

func (c *picCtx) run() {
	defer close(c.done)
	defer c.pool.put(c.tabs)
	defer c.pic.setProgress(progDone)
	for j := range c.jobs {
		if !c.failed {
			c.runJob(j)
		}
	}
	c.finish()
}

func (c *picCtx) runJob(j sliceJob) {
	defer func() {
		if r := recover(); r != nil {
			c.failed = true
			reportPanic(r)
		}
	}()
	c.last = j.d
	if j.h.firstMB < c.deblocked*c.mbW {
		// a slice that starts in rows already published (slices of a conforming stream do not overlap):
		// other pictures may be reading those rows, so it is not decoded
		return
	}
	if j.h.firstMB != c.mbDone {
		c.lag = false // not the next macroblock in raster order: deblock when all slices are in
	}
	j.d.runSlice(j.r) // an error ends the slice; what it did not cover is concealed at the end
	c.decoded += j.d.decodedMB
}

// finish conceals what no slice covered and deblocks the rows that are still waiting.
func (c *picCtx) finish() {
	defer func() {
		if r := recover(); r != nil {
			reportPanic(r)
		}
	}()
	d := c.last
	if d == nil {
		return // the picture was abandoned before its first slice was handed over
	}
	if c.decoded < c.mbW*c.mbH {
		d.conceal(c.pic, c.concealSrc)
	}
	for r := c.deblocked; r < c.mbH; r++ {
		c.deblockRow(d, r)
	}
}

// mbDecoded is called after every macroblock of a threaded picture: once a row is complete the row above it
// is deblocked (intra prediction of the next row still needs the unfiltered samples of this one).
func (c *picCtx) mbDecoded(d *Decoder) {
	c.mbDone++
	if c.lag && c.mbDone%c.mbW == 0 {
		if r := c.mbDone/c.mbW - 1; r >= 1 && r-1 == c.deblocked {
			c.deblockRow(d, r-1)
		}
	}
}

// deblockRow deblocks macroblock row r and publishes the rows that are final as a result: the filtering of
// row r+1 still changes the last three lines of row r.
func (c *picCtx) deblockRow(d *Decoder, r int) {
	mbx, mby, field, off, str := d.mbx, d.mby, d.mbField, d.mbOff, d.mbStr
	fast := deblockFast && d.f.cat <= 1
	for x := 0; x < c.mbW; x++ {
		if fast {
			d.deblockMBFast(x, r)
		} else {
			d.deblockMB(x, r)
		}
	}
	d.mbx, d.mby, d.mbField, d.mbOff, d.mbStr = mbx, mby, field, off, str
	c.deblocked = r + 1
	if r+1 < c.mbH {
		c.pic.setProgress((r+1)*16 - 3)
	}
}

func reportPanic(r any) {
	if DebugPanic != nil {
		DebugPanic(r, nil)
	}
}

// threadedFinish ends the macroblock data of the current picture: the goroutine conceals, deblocks the last
// rows and publishes the picture. The reference marking that follows does not wait for it.
func (d *Decoder) threadedFinish() {
	c := d.ctx
	d.ctx = nil
	c.concealSrc = d.concealSource(c.pic)
	c.closed = true
	close(c.jobs)
}

// wait blocks until the picture is complete.
func (c *picCtx) wait() { <-c.done }
