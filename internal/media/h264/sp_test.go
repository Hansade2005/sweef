package h264

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// decodeToI420 decodes an Annex B stream access unit by access unit and
// returns the raw 4:2:0 frames in decoding order, which is the output order
// of streams without B pictures (the SP conformance streams are I/P/SP only;
// the output order logic of the application itself is not under test here).
func decodeToI420(t testing.TB, es []byte) [][]byte {
	t.Helper()
	d := NewDecoder()
	var res [][]byte
	for i, au := range SplitAccessUnits(es) {
		fr, err := d.DecodeFrame(au)
		if err != nil && fr == nil {
			t.Fatalf("AU %d: %v", i, err)
		}
		if fr == nil {
			continue
		}
		img, ok := fr.ToYCbCr()
		if !ok {
			t.Fatalf("AU %d: not 4:2:0", i)
		}
		var b []byte
		r := img.Rect
		for y := 0; y < r.Dy(); y++ {
			b = append(b, img.Y[y*img.YStride:y*img.YStride+r.Dx()]...)
		}
		for _, pl := range [][]byte{img.Cb, img.Cr} {
			for y := 0; y < (r.Dy()+1)/2; y++ {
				b = append(b, pl[y*img.CStride:y*img.CStride+(r.Dx()+1)/2]...)
			}
		}
		res = append(res, b)
	}
	return res
}

// TestSPConformance decodes the JVT Extended profile SP conformance streams
// (SP1_BT_A, sp2_bt_b) and compares every frame with the reference YUV of the
// conformance package. SWEEF_H264_SP is a directory holding the extracted
// *.h264 / *.yuv pairs (searched recursively).
func TestSPConformance(t *testing.T) {
	root := os.Getenv("SWEEF_H264_SP")
	if root == "" {
		t.Skip("SWEEF_H264_SP not set")
	}
	n := 0
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() || filepath.Ext(p) != ".h264" || !strings.HasPrefix(strings.ToLower(filepath.Base(p)), "sp") {
			return nil
		}
		ref := p[:len(p)-len(".h264")] + ".yuv"
		want, err := os.ReadFile(ref)
		if err != nil {
			return nil
		}
		n++
		t.Run(filepath.Base(p), func(t *testing.T) {
			es, _ := os.ReadFile(p)
			frames := decodeToI420(t, es)
			if len(frames) == 0 {
				t.Fatal("no frames")
			}
			fs := len(frames[0])
			if len(want) != fs*len(frames) {
				t.Fatalf("reference has %d bytes, decoded %d frames of %d", len(want), len(frames), fs)
			}
			bad := 0
			for i, f := range frames {
				if !bytes.Equal(f, want[i*fs:(i+1)*fs]) {
					if bad == 0 {
						j := 0
						for f[j] == want[i*fs+j] {
							j++
						}
						t.Errorf("frame %d differs first at byte %d (got %d want %d)", i, j, f[j], want[i*fs+j])
					}
					bad++
				}
			}
			if bad > 0 {
				t.Fatalf("%d of %d frames differ", bad, len(frames))
			}
		})
		// SP slices carried in data partitions A, B and C (Extended profile
		// allows both tools together) must decode to the same pictures
		t.Run(filepath.Base(p)+"/partitioned", func(t *testing.T) {
			es, _ := os.ReadFile(p)
			aus := SplitAccessUnits(es)
			if len(aus) > 25 {
				aus = aus[:25] // two SP pictures
			}
			checkPartitioned(t, bytes.Join(aus, nil))
		})
		return nil
	})
	if n == 0 {
		t.Skip("no stream/yuv pairs found")
	}
}
