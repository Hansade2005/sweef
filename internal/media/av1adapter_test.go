package media

import (
	"crypto/md5"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// TestAV1Containers decodes the AV1 MP4 and WebM fixtures through the
// container demuxers and the native adapter and compares each picture with
// the libdav1d output hash.
func TestAV1Containers(t *testing.T) {
	for _, name := range []string{"av1_inter.mp4", "av1_arf.webm"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile("testdata/" + name)
			if err != nil {
				t.Fatal(err)
			}
			want, err := os.ReadFile("testdata/" + name[:strings.LastIndex(name, ".")] + ".md5")
			if err != nil {
				t.Fatal(err)
			}
			wantL := strings.Fields(string(want))
			info, err := ProbeBytes(data)
			if err != nil {
				t.Fatal(err)
			}
			src := BytesSource(data)
			vs, err := loadVideoStream(src, info)
			if err != nil {
				t.Fatal(err)
			}
			if vs.codec != "av1" || len(vs.cfg) == 0 {
				t.Fatalf("codec %q cfg %d bytes", vs.codec, len(vs.cfg))
			}
			dec := newNativeDecoder(vs).(picDecoder)
			var got []string
			for i := range vs.samples {
				b, err := vs.payload(src, i)
				if err != nil {
					t.Fatal(err)
				}
				p, err := dec.DecodePic(b)
				if err != nil {
					t.Fatalf("sample %d: %v", i, err)
				}
				if p.ycc == nil {
					continue
				}
				h := md5.New()
				y := p.ycc
				w, hh := y.Rect.Dx(), y.Rect.Dy()
				for r := 0; r < hh; r++ {
					h.Write(y.Y[r*y.YStride : r*y.YStride+w])
				}
				for _, pl := range [][]byte{y.Cb, y.Cr} {
					for r := 0; r < (hh+1)/2; r++ {
						h.Write(pl[r*y.CStride : r*y.CStride+(w+1)/2])
					}
				}
				got = append(got, hex.EncodeToString(h.Sum(nil)))
			}
			if len(got) != len(wantL) {
				t.Fatalf("%d pictures, want %d", len(got), len(wantL))
			}
			for i := range got {
				if got[i] != wantL[i] {
					t.Errorf("picture %d differs", i)
				}
			}
		})
	}
}
