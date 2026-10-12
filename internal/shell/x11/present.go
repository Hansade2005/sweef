//go:build linux

package x11

import (
	"image"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sweefbrowser/sweef/internal/shell/pixconv"
	"golang.org/x/sys/unix"
)

// shmState tracks the MIT-SHM segment backing the shadow buffer.
type shmState struct {
	major, completionEvent uint8
	seg                    uint32 // X-side segment id (0 = none)
	sysvID                 int
	mem                    []byte

	cmu         sync.Mutex
	ccond       *sync.Cond
	outstanding int
}

// disableSHM forces the core PutImage path (tests and SWEEF_X11_NOSHM=1).
var disableSHM atomic.Bool

func init() {
	if os.Getenv("SWEEF_X11_NOSHM") != "" {
		disableSHM.Store(true)
	}
}

const (
	shmMinorQueryVersion = 0
	shmMinorAttach       = 1
	shmMinorDetach       = 2
	shmMinorPutImage     = 3
)

// setupSHM probes MIT-SHM. It is only used when the server is on the same
// machine (unix socket) and a test attach succeeds.
func (w *Window) setupSHM() {
	w.shm.ccond = sync.NewCond(&w.shm.cmu)
	if !w.c.isUnix || disableSHM.Load() {
		return
	}
	ext, err := w.c.queryExtension("MIT-SHM")
	if err != nil || !ext.present {
		return
	}
	b := request(ext.major, shmMinorQueryVersion, 4)
	if _, err := w.c.do(b); err != nil {
		return
	}
	w.shm.major = ext.major
	w.shm.completionEvent = ext.firstEvent
	// Probe: create and attach a tiny segment; BadAccess/BadValue arrives as
	// an asynchronous error that we detect after a sync.
	id, mem, err := sysvAlloc(4096)
	if err != nil {
		return
	}
	seg := w.c.allocID()
	w.errMu.Lock()
	w.lastErr = nil
	w.errMu.Unlock()
	w.shmAttach(seg, id)
	if err := w.c.sync(); err != nil || w.LastError() != nil {
		w.errMu.Lock()
		w.lastErr = nil
		w.errMu.Unlock()
		sysvFree(id, mem)
		return
	}
	d := request(w.shm.major, shmMinorDetach, 8)
	le.PutUint32(d[4:], seg)
	w.c.send(d, false)
	sysvFree(id, mem)
	w.useSHM = true
}

func (w *Window) shmAttach(seg uint32, sysvID int) {
	b := request(w.shm.major, shmMinorAttach, 16)
	le.PutUint32(b[4:], seg)
	le.PutUint32(b[8:], uint32(sysvID))
	b[12] = 1 // read-only for the server
	w.c.send(b, false)
}

// sysvAlloc creates a SysV segment, maps it, and marks it for removal so it
// disappears automatically when both sides detach (or we crash).
func sysvAlloc(size int) (int, []byte, error) {
	id, err := unix.SysvShmGet(unix.IPC_PRIVATE, size, unix.IPC_CREAT|0o600)
	if err != nil {
		return 0, nil, err
	}
	mem, err := unix.SysvShmAttach(id, 0, 0)
	if err != nil {
		unix.SysvShmCtl(id, unix.IPC_RMID, nil)
		return 0, nil, err
	}
	// Linux keeps a removed segment usable by id while it has attachments,
	// so the X server can still attach after this.
	unix.SysvShmCtl(id, unix.IPC_RMID, nil)
	return id, mem, nil
}

func sysvFree(id int, mem []byte) {
	if mem != nil {
		unix.SysvShmDetach(mem)
	}
}

func (w *Window) shmCompleted() {
	s := &w.shm
	s.cmu.Lock()
	if s.outstanding > 0 {
		s.outstanding--
	}
	s.ccond.Broadcast()
	s.cmu.Unlock()
}

func (w *Window) shmWake() {
	s := &w.shm
	if s.ccond == nil {
		return
	}
	s.cmu.Lock()
	s.ccond.Broadcast()
	s.cmu.Unlock()
}

// waitSHM blocks until the server finished reading the segment (so it is
// safe to write), or until a timeout/shutdown.
func (w *Window) waitSHM() {
	s := &w.shm
	s.cmu.Lock()
	if s.outstanding == 0 {
		s.cmu.Unlock()
		return
	}
	timedOut := false
	t := time.AfterFunc(2*time.Second, func() {
		s.cmu.Lock()
		timedOut = true
		s.ccond.Broadcast()
		s.cmu.Unlock()
	})
	for s.outstanding > 0 && !timedOut && !w.closed.Load() && !w.c.isClosed() {
		s.ccond.Wait()
	}
	if timedOut {
		s.outstanding = 0
	}
	s.cmu.Unlock()
	t.Stop()
}

func (w *Window) shmFree() {
	w.pmu.Lock()
	defer w.pmu.Unlock()
	if w.shm.seg != 0 {
		d := request(w.shm.major, shmMinorDetach, 8)
		le.PutUint32(d[4:], w.shm.seg)
		w.c.send(d, false)
		sysvFree(w.shm.sysvID, w.shm.mem)
		w.shm.seg, w.shm.mem = 0, nil
	}
	w.shadow = nil
}

// ensureShadow makes the shadow buffer at least dw x dh pixels, preserving
// existing contents. Caller holds pmu and has waited for SHM idleness.
func (w *Window) ensureShadow(dw, dh int) {
	if dw <= w.capW && dh <= w.capH && w.shadow != nil {
		return
	}
	ncw, nch := (max(dw, w.capW)+63)&^63, (max(dh, w.capH)+63)&^63
	size := ncw * nch * 4
	var nbuf []byte
	var nseg uint32
	var nid int
	if w.useSHM {
		id, mem, err := sysvAlloc(size)
		if err == nil {
			seg := w.c.allocID()
			w.shmAttach(seg, id)
			nbuf, nseg, nid = mem, seg, id
		} else {
			w.useSHM = false // fall back to PutImage
		}
	}
	if nbuf == nil {
		nbuf = make([]byte, size)
	}
	// carry over the old picture so Expose keeps working across resizes
	if w.shadow != nil {
		rows := min(w.capH, nch)
		n := min(w.capW, ncw) * 4
		for y := 0; y < rows; y++ {
			copy(nbuf[y*ncw*4:y*ncw*4+n], w.shadow[y*w.capW*4:y*w.capW*4+n])
		}
	}
	if w.shm.seg != 0 {
		d := request(w.shm.major, shmMinorDetach, 8)
		le.PutUint32(d[4:], w.shm.seg)
		w.c.send(d, false)
		sysvFree(w.shm.sysvID, w.shm.mem)
	}
	w.shadow, w.capW, w.capH = nbuf, ncw, nch
	w.shm.seg, w.shm.sysvID = nseg, nid
	if w.useSHM {
		w.shm.mem = nbuf
	} else {
		w.shm.mem = nil
	}
}

// Present implements shell.Window.
func (w *Window) Present(frame *image.RGBA, damage image.Rectangle) {
	if frame == nil || w.closed.Load() {
		return
	}
	r := damage
	if r.Empty() {
		r = frame.Rect
	}
	r = r.Intersect(frame.Rect)
	dw, dh := w.devSize()
	r = r.Intersect(image.Rect(0, 0, dw, dh))
	if r.Empty() {
		return
	}
	w.pmu.Lock()
	defer w.pmu.Unlock()
	if w.useSHM {
		w.waitSHM()
	}
	w.ensureShadow(dw, dh)
	pixconv.ConvertRect(w.shadow, w.capW*4, frame.Pix, frame.Stride, frame.Rect.Min.X, frame.Rect.Min.Y,
		r.Min.X, r.Min.Y, r.Max.X, r.Max.Y, w.rgbx)
	w.putRegion(r)
}

// redraw re-sends part of the shadow buffer (Expose).
func (w *Window) redraw(r image.Rectangle) {
	dw, dh := w.devSize()
	r = r.Intersect(image.Rect(0, 0, dw, dh))
	if r.Empty() {
		return
	}
	w.pmu.Lock()
	defer w.pmu.Unlock()
	if w.useSHM {
		w.waitSHM()
	}
	w.ensureShadow(dw, dh)
	w.putRegion(r)
}

// putRegion sends r of the shadow buffer to the window. Caller holds pmu.
func (w *Window) putRegion(r image.Rectangle) {
	if w.useSHM && w.shm.seg != 0 {
		b := request(w.shm.major, shmMinorPutImage, 40)
		le.PutUint32(b[4:], w.win)
		le.PutUint32(b[8:], w.gc)
		le.PutUint16(b[12:], uint16(w.capW))
		le.PutUint16(b[14:], uint16(w.capH))
		le.PutUint16(b[16:], uint16(r.Min.X))
		le.PutUint16(b[18:], uint16(r.Min.Y))
		le.PutUint16(b[20:], uint16(r.Dx()))
		le.PutUint16(b[22:], uint16(r.Dy()))
		le.PutUint16(b[24:], uint16(r.Min.X))
		le.PutUint16(b[26:], uint16(r.Min.Y))
		b[28] = w.c.screen.rootDepth
		b[29] = 2 // ZPixmap
		b[30] = 1 // send_event: ShmCompletion when the server is done reading
		le.PutUint32(b[32:], w.shm.seg)
		le.PutUint32(b[36:], 0)
		w.shm.cmu.Lock()
		w.shm.outstanding++
		w.shm.cmu.Unlock()
		if _, err := w.c.send(b, false); err != nil {
			w.shmCompleted()
		}
		return
	}
	// Core PutImage in strips that fit the server's maximum request size.
	rw := r.Dx()
	rowBytes := rw * 4
	maxRows := (w.c.maxReqBytes - 24) / rowBytes
	if maxRows < 1 {
		// extremely wide region: split by columns
		half := image.Rect(r.Min.X, r.Min.Y, r.Min.X+rw/2, r.Max.Y)
		w.putRegion(half)
		w.putRegion(image.Rect(half.Max.X, r.Min.Y, r.Max.X, r.Max.Y))
		return
	}
	for y := r.Min.Y; y < r.Max.Y; y += maxRows {
		rows := min(maxRows, r.Max.Y-y)
		size := 24 + rows*rowBytes
		if cap(w.putBuf) < size {
			w.putBuf = make([]byte, size)
		}
		b := w.putBuf[:size]
		b[0], b[1] = opPutImage, 2 // ZPixmap
		le.PutUint16(b[2:], uint16(size/4))
		le.PutUint32(b[4:], w.win)
		le.PutUint32(b[8:], w.gc)
		le.PutUint16(b[12:], uint16(rw))
		le.PutUint16(b[14:], uint16(rows))
		le.PutUint16(b[16:], uint16(r.Min.X))
		le.PutUint16(b[18:], uint16(y))
		b[20] = 0
		b[21] = w.c.screen.rootDepth
		b[22], b[23] = 0, 0
		for i := 0; i < rows; i++ {
			so := (y+i)*w.capW*4 + r.Min.X*4
			copy(b[24+i*rowBytes:], w.shadow[so:so+rowBytes])
		}
		if _, err := w.c.send(b, false); err != nil {
			return
		}
	}
}
