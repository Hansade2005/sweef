package rtcmedia

import "sync/atomic"

// EncodedFrame is one encoded media frame on its way between the codec and the RTP layer: the VP8 frame or the
// Opus packet before packetisation on the send side, and as reassembled from RTP before decoding on the receive
// side. It is what RTCRtpScriptTransform and createEncodedStreams hand to a page.
type EncodedFrame struct {
	Video       bool
	Key         bool
	Timestamp   uint32 // RTP timestamp
	SSRC        uint32
	PayloadType uint8
	Width       int // video only; 0 when unknown
	Height      int
	Data        []byte
}

// FrameHook processes one encoded frame. It runs on the media goroutine and may block (the engine waits for the
// page's transform to answer); it returns the payload to use instead, or ok=false to drop the frame (a transform
// that failed, e.g. could not decrypt). A nil hook leaves frames alone.
type FrameHook func(f *EncodedFrame) (data []byte, ok bool)

type hookSlot struct{ p atomic.Pointer[FrameHook] }

func (h *hookSlot) set(f FrameHook) {
	if f == nil {
		h.p.Store(nil)
		return
	}
	h.p.Store(&f)
}

func (h *hookSlot) get() FrameHook {
	if p := h.p.Load(); p != nil {
		return *p
	}
	return nil
}
