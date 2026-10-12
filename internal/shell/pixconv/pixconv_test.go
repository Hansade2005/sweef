package pixconv

import (
	"math/rand"
	"testing"
)

func refSwap(src []byte) []byte {
	out := make([]byte, len(src))
	for i := 0; i+4 <= len(src); i += 4 {
		out[i], out[i+1], out[i+2], out[i+3] = src[i+2], src[i+1], src[i], src[i+3]
	}
	return out
}

func TestSwapRB(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for _, px := range []int{1, 2, 3, 4, 5, 7, 8, 63, 64, 65, 1920} {
		for _, off := range []int{0, 4, 8} { // alignment variants
			base := make([]byte, px*4+16)
			rng.Read(base)
			src := base[off : off+px*4]
			dbase := make([]byte, px*4+16)
			dst := dbase[off : off+px*4]
			SwapRB(dst, src)
			want := refSwap(src)
			for i := range want {
				if dst[i] != want[i] {
					t.Fatalf("px=%d off=%d mismatch at %d", px, off, i)
				}
			}
		}
	}
}

func BenchmarkSwapRB1080p(b *testing.B) {
	src := make([]byte, 1920*1080*4)
	dst := make([]byte, len(src))
	rand.Read(src)
	b.SetBytes(int64(len(src)))
	for i := 0; i < b.N; i++ {
		SwapRB(dst, src)
	}
}

func TestUnpremultiply(t *testing.T) {
	px := []byte{50, 100, 25, 100, 10, 20, 30, 255, 9, 9, 9, 0}
	Unpremultiply(px)
	if px[0] != 127 || px[1] != 255 || px[2] != 63 || px[3] != 100 {
		t.Fatalf("%v", px[:4])
	}
	if px[4] != 10 || px[8] != 9 {
		t.Fatalf("opaque/transparent pixels must not change: %v", px)
	}
}

func TestConvertRect(t *testing.T) {
	src := make([]byte, 4*4*4)
	for i := range src {
		src[i] = byte(i)
	}
	dst := make([]byte, 8*4*4) // dst wider than src
	ConvertRect(dst, 8*4, src, 4*4, 0, 0, 1, 1, 3, 3, false)
	// pixel (1,1): src bytes 20..23 = R,G,B,A -> dst at (1,1) = B,G,R,A
	o := 1*32 + 1*4
	if dst[o] != 22 || dst[o+1] != 21 || dst[o+2] != 20 || dst[o+3] != 23 {
		t.Fatalf("%v", dst[o:o+4])
	}
	if dst[0] != 0 || dst[3*32+3*4] != 0 {
		t.Fatal("wrote outside the rectangle")
	}
	ConvertRect(dst, 8*4, src, 4*4, 0, 0, 0, 0, 1, 1, true)
	if dst[0] != 0 || dst[1] != 1 || dst[2] != 2 {
		t.Fatal("rgbx copy")
	}
}
