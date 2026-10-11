package vp9

import (
	"os"
	"testing"
)

func TestVP9Coverage(t *testing.T) {
	if os.Getenv("SWEEF_NO_FFMPEG") != "" {
		t.Skip()
	}
	ivf := genTwoPass(t, "mandelbrot=rate=15:size=256x144", "256x144", "-t", "4", "-b:v", "400k", "-g", "100", "-cpu-used", "0", "-auto-alt-ref", "1", "-lag-in-frames", "16")
	d := NewDecoder()
	var comp, sub8, inter, hidden, super, seg int
	filt := map[uint8]int{}
	for _, f := range readIVF(t, ivf) {
		fr, _ := splitSuperframe(f)
		if len(fr) > 1 {
			super++
		}
		img, err := d.Decode(f)
		if err != nil {
			t.Fatal(err)
		}
		if img == nil {
			hidden++
		}
		for _, m := range d.prevMi {
			if m.isInter {
				inter++
				filt[m.filter]++
				if m.refFrame[1] > 0 {
					comp++
				}
				if m.size < block8x8 {
					sub8++
				}
			}
		}
		if d.seg.enabled {
			seg++
		}
	}
	t.Logf("inter %d compound %d sub8x8 %d hidden %d superframes %d filters %v seg %d", inter, comp, sub8, hidden, super, filt, seg)
	if comp == 0 || sub8 == 0 || super == 0 {
		t.Errorf("clip does not exercise all tools")
	}
}

func BenchmarkDecode640x360(b *testing.B) {
	dir := b.TempDir()
	out := dir + "/o.ivf"
	ffmpeg(b, "-f", "lavfi", "-i", "mandelbrot=rate=30:size=640x360", "-vf", "noise=alls=6:allf=t", "-t", "3", "-pix_fmt", "yuv420p", "-c:v", "libvpx-vp9", "-b:v", "1M", "-cpu-used", "2", "-y", out)
	data, _ := os.ReadFile(out)
	frames := readIVF(b, data)
	b.ResetTimer()
	n := 0
	for i := 0; i < b.N; i++ {
		d := NewDecoder()
		for _, f := range frames {
			if _, err := d.Decode(f); err != nil {
				b.Fatal(err)
			}
			n++
		}
	}
	b.ReportMetric(float64(n)/b.Elapsed().Seconds(), "fps")
}
