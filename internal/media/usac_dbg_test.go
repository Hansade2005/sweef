package media

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func dbgLoadRaw(base string) (*audioStream, error) {
	raw, err := os.ReadFile(base + ".raw")
	if err != nil {
		return nil, err
	}
	meta, err := os.ReadFile(base + ".meta")
	if err != nil {
		return nil, err
	}
	var initLen int
	var sizes []int
	for _, ln := range strings.Split(string(meta), "\n") {
		var v int
		if _, e := fmt.Sscanf(ln, "-dec_info_init:%d", &v); e == nil {
			initLen = v
		} else if _, e := fmt.Sscanf(ln, "-ia_mp4_stsz_size:%d", &v); e == nil {
			sizes = append(sizes, v)
		}
	}
	as := &audioStream{codec: "aac", cfgKind: "asc", cfg: raw[:initLen], skip: -1, editDur: -1, lastGran: -1}
	p := initLen
	for _, s := range sizes {
		as.packets = append(as.packets, raw[p:p+s])
		p += s
	}
	return as, nil
}

func dbgCompare(pcm *PCM, ref []byte, maxLag int) (float64, int) {
	ch := len(pcm.Data)
	n := len(ref) / 2 / ch
	rs := func(i, k int) float64 {
		return float64(int16(uint16(ref[(i*ch+k)*2]) | uint16(ref[(i*ch+k)*2+1])<<8))
	}
	best, bl := math.Inf(1), 0
	for lag := -maxLag; lag <= maxLag; lag++ {
		var e float64
		for i := 20000; i < 50000; i++ {
			j := i + lag
			if j < 0 || j >= pcm.Frames() || i >= n {
				continue
			}
			d := float64(pcm.Data[0][j])*32768 - rs(i, 0)
			e += d * d
		}
		if e < best {
			best, bl = e, lag
		}
	}
	var sig, noise float64
	for i := 4096; i < n; i++ {
		j := i + bl
		if j < 0 || j >= pcm.Frames() {
			continue
		}
		for k := 0; k < ch; k++ {
			r := rs(i, k)
			v := math.Round(float64(pcm.Data[k][j]) * 32768)
			v = math.Max(-32768, math.Min(32767, v))
			sig += r * r
			noise += (v - r) * (v - r)
		}
	}
	return 10 * math.Log10(sig/(noise+1e-9)), bl
}

// TestUSACDbgRaw decodes libxaac raw+meta streams (scratch use).
func TestUSACDbgRaw(t *testing.T) {
	names := os.Getenv("USAC_RAW")
	if names == "" {
		t.Skip()
	}
	dir := os.Getenv("USAC_XDIR")
	lpdDbg = os.Getenv("LPD_DBG") != ""
	usacPrimePreroll = os.Getenv("USAC_NOPRIME") == ""
	for _, name := range strings.Split(names, ",") {
		as, err := dbgLoadRaw(filepath.Join(dir, "xin", name))
		if err != nil {
			t.Fatal(err)
		}
		ref, err := os.ReadFile(filepath.Join(dir, "xref", name+".s16"))
		if err != nil {
			t.Fatal(err)
		}
		pcm, err := decodeUSACStream(as)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		snr, lag := dbgCompare(pcm, ref, 4000)
		if os.Getenv("USAC_BLK") != "" {
			ch := len(pcm.Data)
			for b := 0; b*1024+1024 <= pcm.Frames() && b < 400; b++ {
				var e, s float64
				for i := b * 1024; i < b*1024+1024; i++ {
					for k := 0; k < ch; k++ {
						j := i - lag
						if j < 0 || (j*ch+k)*2+1 >= len(ref) {
							continue
						}
						r := float64(int16(uint16(ref[(j*ch+k)*2]) | uint16(ref[(j*ch+k)*2+1])<<8))
						v := math.Max(-32768, math.Min(32767, float64(pcm.Data[k][i])*32768))
						e += (v - r) * (v - r)
						s += r * r
					}
				}
				fmt.Printf("blk %d snr %.1f\n", b, 10*math.Log10((s+1)/(e+1)))
			}
		}
		if o := os.Getenv("USAC_OUT"); o != "" {
			ch := len(pcm.Data)
			buf := make([]byte, 0, pcm.Frames()*ch*2)
			for i := 0; i < pcm.Frames(); i++ {
				for k := 0; k < ch; k++ {
					v := math.Max(-32768, math.Min(32767, math.Round(float64(pcm.Data[k][i])*32768)))
					buf = append(buf, byte(int16(v)), byte(int16(v)>>8))
				}
			}
			os.WriteFile(filepath.Join(o, name+".go.s16"), buf, 0o644)
		}
		fmt.Printf("%s: rate=%d ch=%d frames=%d (ref %d) lag=%d SNR=%.1f dB stats=%+v\n", name, pcm.SampleRate, len(pcm.Data), pcm.Frames(), len(ref)/2/len(pcm.Data), lag, snr, lastUSACStats)
	}
}
