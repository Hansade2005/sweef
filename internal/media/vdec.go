package media

import (
	"image"
)

// VideoDecoder decodes video access units one at a time, in decode order,
// with a pure-Go decoder. It is what the player drives from a TrackStore.
type VideoDecoder struct {
	dec   vdecoder
	pd    picDecoder
	codec string
	cfg   []byte
}

// Picture is a decoded frame that has not been converted to RGB yet. The
// conversion happens only for frames that are shown.
type Picture struct {
	p    vpic
	w, h int
}

// Valid reports whether the picture holds pixels.
func (p Picture) Valid() bool { return p.p.valid() }

// Wait blocks until the picture is completely decoded (pictures of a decoder that works on several frames at
// once are returned while their last rows are still being reconstructed).
func (p Picture) Wait() {
	if p.p.frm != nil {
		p.p.frm.Wait()
	}
}

// Size is the picture size in pixels.
func (p Picture) Size() (int, int) { return p.w, p.h }

// RGBA converts the picture for display (BT.709 for HD, BT.601 below).
func (p Picture) RGBA() *image.RGBA {
	if !p.p.valid() {
		return nil
	}
	return p.p.rgba(p.h >= 720)
}

// NewVideoDecoder creates a decoder for the track configuration.
func NewVideoDecoder(c TrackConfig) (*VideoDecoder, error) {
	vs := &vstream{codec: c.CodecName, cfg: c.Cfg, w: c.Width, h: c.Height}
	dec := newNativeDecoder(vs)
	if dec == nil {
		return nil, ErrUnsupported
	}
	d := &VideoDecoder{dec: dec, codec: c.CodecName, cfg: c.Cfg}
	d.pd, _ = dec.(picDecoder)
	return d, nil
}

// Codec is the codec name the decoder was made for.
func (d *VideoDecoder) Codec() string { return d.codec }

// Decode decodes one access unit. The returned picture is invalid when the
// unit does not produce a displayable picture (a hidden alt-ref frame, a
// parameter-set-only unit).
func (d *VideoDecoder) Decode(data []byte) (Picture, error) {
	if d.pd != nil {
		p, err := d.pd.DecodePic(data)
		if err != nil {
			return Picture{}, err
		}
		pic := Picture{p: p}
		pic.w, pic.h = pictureSize(p)
		return pic, nil
	}
	ycc, err := d.dec.Decode(data)
	if err != nil {
		return Picture{}, err
	}
	p := vpic{ycc: ycc}
	pic := Picture{p: p}
	pic.w, pic.h = pictureSize(p)
	return pic, nil
}

// Disposable reports whether no later picture is predicted from this access unit, so a player that is running
// late can skip decoding it (only H.264 is recognised: a slice NAL with nal_ref_idc 0, as non-reference
// B-frames are coded; everything else reports false).
func (d *VideoDecoder) Disposable(data []byte) bool {
	if d.codec != "h264" || len(d.cfg) < 5 {
		return false
	}
	ls := int(d.cfg[4]&3) + 1
	seen := false
	for len(data) > ls {
		n := 0
		for i := 0; i < ls; i++ {
			n = n<<8 | int(data[i])
		}
		data = data[ls:]
		if n <= 0 || n > len(data) {
			return false
		}
		h := data[0]
		switch typ := h & 0x1f; {
		case typ == 5 || (typ >= 2 && typ <= 4):
			return false
		case typ == 1:
			if h>>5&3 != 0 {
				return false
			}
			seen = true
		}
		data = data[n:]
	}
	return seen
}

// Reset drops reference pictures: the next unit must be a sync sample.
func (d *VideoDecoder) Reset() { d.dec.Reset() }

func pictureSize(p vpic) (int, int) {
	switch {
	case p.ycc != nil:
		b := p.ycc.Bounds()
		return b.Dx(), b.Dy()
	case p.frm != nil:
		return p.frm.Width, p.frm.Height
	}
	return 0, 0
}
