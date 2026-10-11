// Package yuv defines the planar picture type shared by the pure-Go video
// decoders for formats that image.YCbCr cannot hold: sample depths above 8
// bits, 4:0:0 and the less common chroma layouts. Samples are always stored
// as uint16 so one conversion path serves every depth.
package yuv

import (
	"image"
)

// Format is the chroma sampling layout.
type Format uint8

const (
	F420 Format = iota // 4:2:0
	F422               // 4:2:2
	F444               // 4:4:4
	F440               // 4:4:0
	F400               // luma only (chroma planes absent)
)

// Subsampling returns the horizontal and vertical chroma shift of f.
func (f Format) Subsampling() (sx, sy uint) {
	switch f {
	case F420:
		return 1, 1
	case F422:
		return 1, 0
	case F440:
		return 0, 1
	}
	return 0, 0
}

// Matrix coefficient codes (ITU-T H.273 matrix_coefficients) used here.
const (
	MatrixIdentity    = 0 // GBR / RGB carried as 4:4:4
	MatrixBT709       = 1
	MatrixUnspecified = 2
	MatrixBT601       = 5 // also 6 (SMPTE 170M)
	MatrixBT2020      = 9
)

// Frame is a decoded picture. Plane k has Stride[k] samples per row; the
// visible area of plane 0 is Width x Height and of planes 1, 2 is
// CWidth x CHeight. Planes may be shared with decoder internals; a Frame
// must not be modified once returned.
type Frame struct {
	Format        Format
	Depth         int // bits per sample, 8..16
	Width, Height int
	CWidth        int
	CHeight       int
	Planes        [3][]uint16
	Stride        [3]int
	Matrix        int  // H.273 matrix_coefficients; MatrixUnspecified if unknown
	FullRange     bool // full-range (JPEG) samples
}

// New allocates a frame of the given geometry.
func New(f Format, depth, w, h int) *Frame {
	fr := &Frame{Format: f, Depth: depth, Width: w, Height: h, Matrix: MatrixUnspecified}
	sx, sy := f.Subsampling()
	fr.CWidth, fr.CHeight = (w+(1<<sx)-1)>>sx, (h+(1<<sy)-1)>>sy
	fr.Stride[0] = w
	fr.Planes[0] = make([]uint16, w*h)
	if f != F400 {
		for i := 1; i < 3; i++ {
			fr.Stride[i] = fr.CWidth
			fr.Planes[i] = make([]uint16, fr.CWidth*fr.CHeight)
		}
	}
	return fr
}

// ToYCbCr narrows an 8-bit frame to an image.YCbCr. It reports false when
// the frame is not 8-bit or its chroma layout has no YCbCr equivalent.
// Monochrome frames get neutral chroma at 4:2:0.
func (f *Frame) ToYCbCr() (*image.YCbCr, bool) {
	if f.Depth != 8 {
		return nil, false
	}
	var ratio image.YCbCrSubsampleRatio
	switch f.Format {
	case F420, F400:
		ratio = image.YCbCrSubsampleRatio420
	case F422:
		ratio = image.YCbCrSubsampleRatio422
	case F444:
		ratio = image.YCbCrSubsampleRatio444
	case F440:
		ratio = image.YCbCrSubsampleRatio440
	default:
		return nil, false
	}
	img := image.NewYCbCr(image.Rect(0, 0, f.Width, f.Height), ratio)
	for y := 0; y < f.Height; y++ {
		src := f.Planes[0][y*f.Stride[0] : y*f.Stride[0]+f.Width]
		dst := img.Y[y*img.YStride : y*img.YStride+f.Width]
		for x, v := range src {
			dst[x] = uint8(v)
		}
	}
	if f.Format == F400 {
		for i := range img.Cb {
			img.Cb[i], img.Cr[i] = 128, 128
		}
		return img, true
	}
	for p, dstp := range [2][]uint8{img.Cb, img.Cr} {
		sp := f.Planes[p+1]
		for y := 0; y < f.CHeight; y++ {
			src := sp[y*f.Stride[p+1] : y*f.Stride[p+1]+f.CWidth]
			dst := dstp[y*img.CStride : y*img.CStride+f.CWidth]
			for x, v := range src {
				dst[x] = uint8(v)
			}
		}
	}
	return img, true
}

// FromYCbCr widens an 8-bit image.YCbCr into a Frame.
func FromYCbCr(img *image.YCbCr) *Frame {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	var fm Format
	switch img.SubsampleRatio {
	case image.YCbCrSubsampleRatio420:
		fm = F420
	case image.YCbCrSubsampleRatio422:
		fm = F422
	case image.YCbCrSubsampleRatio440:
		fm = F440
	default:
		fm = F444
	}
	fr := New(fm, 8, w, h)
	for y := 0; y < h; y++ {
		o := img.YOffset(b.Min.X, b.Min.Y+y)
		for x := 0; x < w; x++ {
			fr.Planes[0][y*w+x] = uint16(img.Y[o+x])
		}
		if y < fr.CHeight {
			co := img.COffset(b.Min.X, b.Min.Y+y<<(h2s(fm)))
			for x := 0; x < fr.CWidth; x++ {
				cx := x
				if fm == F420 || fm == F422 {
					cx = x
				}
				fr.Planes[1][y*fr.CWidth+x] = uint16(img.Cb[co+cx])
				fr.Planes[2][y*fr.CWidth+x] = uint16(img.Cr[co+cx])
			}
		}
	}
	return fr
}

func h2s(f Format) uint {
	if f == F420 || f == F440 {
		return 1
	}
	return 0
}
