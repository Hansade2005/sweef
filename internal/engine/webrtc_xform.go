package engine

// webrtc_xform.go: encoded transforms (RTCRtpScriptTransform and RTCRtpSender/Receiver.createEncodedStreams).
//
// The media pipelines (internal/rtcmedia) call a FrameHook for every encoded frame: on the send side between the
// encoder and the packetiser, on the receive side between the depacketiser and the decoder. The hook sent here
// pushes the frame to the page as an "encframe" event and waits for the page's transform to hand the (possibly
// rewritten) payload back through rtcEncResult. A frame the page does not answer within xformTimeout is dropped,
// like a frame whose decryption failed; with no transform attached frames pass untouched.

import (
	"encoding/base64"
	"sync/atomic"
	"time"

	"github.com/sweefbrowser/sweef/internal/rtcmedia"
)

const xformTimeout = 1500 * time.Millisecond

type xres struct {
	data []byte
	ok   bool
}

// xformHook returns the hook for one side ("send" or "recv") of a transceiver.
func (p *rtcPeer) xformHook(x *rtcTR, side string) rtcmedia.FrameHook {
	kind := x.t.Kind().String()
	return func(f *rtcmedia.EncodedFrame) ([]byte, bool) {
		if p.closed.Load() {
			return f.Data, true
		}
		id := atomic.AddUint64(&p.xnext, 1)
		ch := make(chan xres, 1)
		p.xmu.Lock()
		if p.xpending == nil {
			p.xpending = map[uint64]chan xres{}
		}
		p.xpending[id] = ch
		p.xmu.Unlock()
		p.emit("encframe", map[string]any{
			"tr": x.id, "side": side, "id": id, "kind": kind, "key": f.Key, "ts": f.Timestamp, "ssrc": f.SSRC, "pt": f.PayloadType,
			"w": f.Width, "h": f.Height, "data": base64.StdEncoding.EncodeToString(f.Data),
		})
		t := time.NewTimer(xformTimeout)
		defer t.Stop()
		select {
		case r := <-ch:
			return r.data, r.ok
		case <-t.C:
		case <-p.done:
		}
		p.xmu.Lock()
		delete(p.xpending, id)
		p.xmu.Unlock()
		return nil, false
	}
}

// encResult delivers the page's answer for a frame.
func (p *rtcPeer) encResult(id uint64, data []byte, ok bool) {
	p.xmu.Lock()
	ch := p.xpending[id]
	delete(p.xpending, id)
	p.xmu.Unlock()
	if ch != nil {
		ch <- xres{data: data, ok: ok}
	}
}

// setXform attaches (on) or removes the transform of one side of a transceiver and wires it into the pipelines
// that exist now; pipelines created later pick it up in ensurePipeline / onRemoteTrack.
func (p *rtcPeer) setXform(x *rtcTR, side string, on bool) {
	var h rtcmedia.FrameHook
	if on {
		h = p.xformHook(x, side)
	}
	if side == "send" {
		x.smu.Lock()
		x.sendXform = on
		layers := x.videoLayersLocked()
		as := x.as
		x.smu.Unlock()
		for _, l := range layers {
			if l.vs != nil {
				l.vs.SetHook(h)
			}
		}
		if as != nil {
			as.SetHook(h)
		}
		return
	}
	x.rmu.Lock()
	x.recvXform = on
	vr, ar := x.vr, x.ar
	x.rmu.Unlock()
	if vr != nil {
		vr.SetHook(h)
	}
	if ar != nil {
		ar.SetHook(h)
	}
}

func (h *rtcHost) xformFns(f map[string]hostFn) {
	f["rtcEncEnable"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, err
		}
		if x := p.tr(argI(a, 1)); x != nil {
			p.setXform(x, argS(a, 2), argB(a, 3))
		}
		return nil, nil
	}
	f["rtcEncResult"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, nil
		}
		data, derr := base64.StdEncoding.DecodeString(argS(a, 2))
		p.encResult(uint64(argI(a, 1)), data, derr == nil && argB(a, 3))
		return nil, nil
	}
	f["rtcJitterTarget"] = func(a []any) (any, error) { // pc, tr, milliseconds (0 clears)
		p, err := h.peer(a)
		if err != nil {
			return nil, nil
		}
		x := p.tr(argI(a, 1))
		if x == nil {
			return nil, nil
		}
		sec := argF(a, 2) / 1000
		x.rmu.Lock()
		x.minDelay = sec
		vr, ar := x.vr, x.ar
		x.rmu.Unlock()
		if vr != nil {
			vr.SetMinDelay(sec)
		}
		if ar != nil {
			ar.SetMinDelay(sec)
		}
		return nil, nil
	}
	f["rtcEncKeyFrame"] = func(a []any) (any, error) {
		p, err := h.peer(a)
		if err != nil {
			return nil, nil
		}
		x := p.tr(argI(a, 1))
		if x == nil {
			return nil, nil
		}
		if argS(a, 2) == "send" {
			for _, l := range x.videoLayers() {
				if l.vs != nil {
					l.vs.RequestKeyFrame()
				}
			}
			return nil, nil
		}
		x.rmu.Lock()
		vr := x.vr
		x.rmu.Unlock()
		if vr != nil {
			vr.RequestKeyFrame()
		}
		return nil, nil
	}
}
