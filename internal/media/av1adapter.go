package media

import (
	"image"

	"github.com/sweefbrowser/sweef/internal/media/av1"
	"github.com/sweefbrowser/sweef/internal/media/yuv"
)

// av1Adapter plugs the pure-Go AV1 decoder into the native frame tier. Each
// sample is one temporal unit in the low-overhead OBU format; the
// configuration OBUs of the av1C box (or WebM CodecPrivate) carry the
// sequence header and are replayed whenever the decoder is reset so that
// decoding can restart at any key frame.
type av1Adapter struct {
	d   *av1.Decoder
	cfg []byte
}

func newAV1Adapter(cfg []byte) *av1Adapter {
	a := &av1Adapter{d: av1.NewDecoder()}
	if len(cfg) > 4 && cfg[0]&0x80 != 0 { // av1C: marker bit, 4 byte header, configOBUs
		a.cfg = cfg[4:]
	}
	a.feedConfig()
	return a
}

func (a *av1Adapter) feedConfig() {
	if len(a.cfg) > 0 {
		a.d.Decode(a.cfg) // only a sequence header: no pictures, errors are not fatal
	}
}

func (a *av1Adapter) Reset() {
	a.d.Reset()
	a.feedConfig()
}

// DecodePic decodes one temporal unit and returns the picture it outputs.
func (a *av1Adapter) DecodePic(data []byte) (vpic, error) {
	pics, err := a.d.Decode(data)
	if len(pics) == 0 {
		return vpic{}, err
	}
	return framePic(av1Frame(pics[len(pics)-1])), nil
}

// Decode implements vdecoder for 8-bit 4:2:0 and monochrome pictures; other
// layouts are only available through DecodePic.
func (a *av1Adapter) Decode(data []byte) (*image.YCbCr, error) {
	p, err := a.DecodePic(data)
	if err != nil {
		return nil, err
	}
	if p.ycc != nil {
		return p.ycc, nil
	}
	if p.frm != nil {
		if img, ok := p.frm.ToYCbCr(); ok {
			return img, nil
		}
		return nil, ErrUnsupported
	}
	return nil, nil
}

// av1Frame wraps a decoded AV1 picture as a yuv.Frame without copying.
func av1Frame(p *av1.Picture) *yuv.Frame {
	f := &yuv.Frame{Depth: p.BitDepth, Width: p.Width, Height: p.Height, Matrix: p.MatrixCoefficients, FullRange: p.FullRange}
	switch {
	case p.Mono:
		f.Format = yuv.F400
	case p.SubX == 1 && p.SubY == 1:
		f.Format = yuv.F420
	case p.SubX == 1:
		f.Format = yuv.F422
	default:
		f.Format = yuv.F444
	}
	f.Planes, f.Stride = p.Planes, p.Stride
	if f.Format != yuv.F400 {
		f.CWidth = (p.Width + p.SubX) >> uint(p.SubX)
		f.CHeight = (p.Height + p.SubY) >> uint(p.SubY)
	}
	return f
}
