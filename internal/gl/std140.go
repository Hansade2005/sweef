package gl

import (
	"encoding/binary"

	"github.com/sweefbrowser/sweef/internal/gl/glsl"
)

func alignUp(x, a int) int { return (x + a - 1) / a * a }

func std140Layout(t glsl.Type) (align, size int) {
	if t.Arr > 0 {
		a, s := std140Layout(t.Elem())
		a = alignUp(a, 16)
		return a, alignUp(s, a) * t.Arr
	}
	switch {
	case t.K == glsl.KStruct:
		off := 0
		for _, f := range t.S.Fields {
			fa, fs := std140Layout(f.T)
			off = alignUp(off, fa)
			off += fs
		}
		return 16, alignUp(off, 16)
	case t.M > 1:
		return 16, 16 * t.M
	case t.N == 1:
		return 4, 4
	case t.N == 2:
		return 8, 8
	case t.N == 3:
		return 16, 12
	}
	return 16, 16
}

// loadStd140 unpacks a value of type t at data[boff:] into registers at roff.
func loadStd140(r []uint32, roff int, t glsl.Type, data []byte, boff int) {
	if t.Arr > 0 {
		e := t.Elem()
		a, s := std140Layout(e)
		stride := alignUp(s, alignUp(a, 16))
		es := e.Size()
		for i := 0; i < t.Arr; i++ {
			loadStd140(r, roff+i*es, e, data, boff+i*stride)
		}
		return
	}
	switch {
	case t.K == glsl.KStruct:
		off := 0
		for _, f := range t.S.Fields {
			fa, fs := std140Layout(f.T)
			off = alignUp(off, fa)
			loadStd140(r, roff+f.Off, f.T, data, boff+off)
			off += fs
		}
	case t.M > 1:
		for c := 0; c < t.M; c++ {
			for k := 0; k < t.N; k++ {
				o := boff + c*16 + k*4
				if o+4 <= len(data) {
					r[roff+c*t.N+k] = binary.LittleEndian.Uint32(data[o:])
				}
			}
		}
	default:
		n := t.N
		for k := 0; k < n; k++ {
			o := boff + k*4
			if o+4 <= len(data) {
				r[roff+k] = binary.LittleEndian.Uint32(data[o:])
			}
		}
	}
}
