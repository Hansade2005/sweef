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
