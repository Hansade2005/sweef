package vp8

import (
	"os"
	"testing"
)

func TestVP8Coverage(t *testing.T) {
	if os.Getenv("SWEEF_NO_FFMPEG") != "" {
		t.Skip()
	}
	ivf := genVP8(t, "mandelbrot=rate=15:size=320x180", "320x180", "-t", "3", "-b:v", "500k", "-auto-alt-ref", "1", "-lag-in-frames", "10", "-g", "100")
	d := NewDecoder()
	var modes [10]int
	var refs [4]int
	hidden := 0
	for _, f := range readIVF(t, ivf) {
		img, err := d.Decode(f)
		if err != nil {
			t.Fatal(err)
		}
		if img == nil {
			hidden++
		}
		for _, m := range d.mbs {
			modes[m.mode]++
			refs[m.ref]++
		}
	}
	t.Logf("modes %v refs %v hidden %d", modes, refs, hidden)
	if modes[mbSplit] == 0 || modes[mbNew] == 0 || refs[refGolden]+refs[refAlt] == 0 {
		t.Errorf("clip does not exercise inter tools")
	}
}

func BenchmarkDecode640x360(b *testing.B) {
	t := &testing.T{}
	_ = t
	dir := b.TempDir()
	out := dir + "/o.ivf"
	ffmpeg(b, "-f", "lavfi", "-i", "mandelbrot=rate=30:size=640x360", "-t", "3", "-c:v", "libvpx", "-b:v", "1M", "-y", out)
	data, _ := os.ReadFile(out)
	frames := readIVF(b, data)
	b.ResetTimer()
	n := 0
	for i := 0; i < b.N; i++ {
		d := NewDecoder()
		for _, f := range frames {
			d.Decode(f)
			n++
		}
	}
	b.ReportMetric(float64(n)/b.Elapsed().Seconds(), "fps")
}
