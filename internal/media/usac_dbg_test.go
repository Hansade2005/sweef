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
	snr, _, lag := dbgCompare2(pcm, ref, maxLag)
	return snr, lag
}

// dbgCompare2 returns the SNR with round-to-nearest and with libxaac's
// truncation towards zero.
func dbgCompare2(pcm *PCM, ref []byte, maxLag int) (float64, float64, int) {
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
	var sig, noise, noiseT float64
	// libxaac's fixed point QMF synthesis saturates and wraps near full
	// scale: leave out the neighbourhood of every sample above 30000
	near := make([]bool, pcm.Frames())
	for j := 0; j < pcm.Frames(); j++ {
		for k := 0; k < ch; k++ {
			if math.Abs(float64(pcm.Data[k][j])*32768) > 30000 {
				for m := j - 320; m <= j+320; m++ {
					if m >= 0 && m < len(near) {
						near[m] = true
					}
				}
			}
		}
	}
	for i := 4096; i < n; i++ {
		j := i + bl
		if j < 0 || j >= pcm.Frames() || near[j] {
			continue
		}
		for k := 0; k < ch; k++ {
			r := rs(i, k)
			v := math.Round(float64(pcm.Data[k][j]) * 32768)
			if v > 32765 || v < -32766 || r > 32765 || r < -32766 {
				continue // libxaac wraps on overflow, we saturate
			}
			sig += r * r
			noise += (v - r) * (v - r)
			vt := math.Trunc(float64(pcm.Data[k][j]) * 32768)
			noiseT += (vt - r) * (vt - r)
		}
	}
	return 10 * math.Log10(sig/(noise+1e-9)), 10 * math.Log10(sig/(noiseT+1e-9)), bl
}

// TestUSACDbgRaw decodes libxaac raw+meta streams (scratch use).
func TestUSACDbgRaw(t *testing.T) {
	names := os.Getenv("USAC_RAW")
	if names == "" {
		t.Skip()
	}
	dir := os.Getenv("USAC_XDIR")
	usacPrimePreroll = os.Getenv("USAC_NOPRIME") == ""
	mpsForcePS = os.Getenv("MPS_FORCE_PS") != ""
	mpsTestHook = nil
	mpsTestCfgHook = nil
	mpsTestHighRate = nil
	mpsTestPseudoLR = os.Getenv("MPS_CFG_PLR") != ""
	mpsTestFreqRes = nil
	if v := os.Getenv("MPS_FR_FROM"); v != "" {
		var from, idx int
		fmt.Sscan(v, &from)
		fmt.Sscan(os.Getenv("MPS_FR_IDX"), &idx)
		mpsTestFreqRes = func(frame int) int {
			if frame > from {
				return idx
			}
			return 1
		}
	}
	if v := os.Getenv("MPS_HR_FROM"); v != "" {
		var from int
		fmt.Sscan(v, &from)
		mpsTestHighRate = func(frame int) bool { return frame > from }
	}
	if os.Getenv("MPS_CFG_FR") != "" || os.Getenv("MPS_CFG_GAIN") != "" || os.Getenv("MPS_CFG_DC") != "" || os.Getenv("MPS_CFG_HR") != "" {
		mpsTestCfgHook = func(c *usacMpsCfg) {
			if v := os.Getenv("MPS_CFG_FR"); v != "" {
				fmt.Sscan(v, &c.freqRes)
			}
			if v := os.Getenv("MPS_CFG_GAIN"); v != "" {
				fmt.Sscan(v, &c.fixedGain)
			}
			if v := os.Getenv("MPS_CFG_DC"); v != "" {
				fmt.Sscan(v, &c.decorrConfig)
			}
			if v := os.Getenv("MPS_CFG_HR"); v != "" {
				c.highRateMode = v == "1"
			}
		}
	}
	if v := os.Getenv("MPS_FORCE_BANDS"); v != "" {
		var nb int
		fmt.Sscan(v, &nb)
		prev := mpsTestHook
		mpsTestHook = func(m *mps212) {
			f := &m.frame
			oldC, oldI := f.cldIdx, f.iccIdx
			for ps := 0; ps <= m.numSets && ps < mpsMaxSets; ps++ {
				for b := 0; b < nb; b++ {
					f.cldIdx[ps][b] = oldC[ps][b*28/nb]
					f.iccIdx[ps][b] = oldI[ps][b*28/nb]
				}
			}
			m.paramBands = nb
			switch nb {
			case 4:
				m.hybToBand = mpsHybBand4[:]
			case 5:
				m.hybToBand = mpsHybBand5[:]
			case 7:
				m.hybToBand = mpsHybBand7[:]
			case 10:
				m.hybToBand = mpsHybBand10[:]
			case 14:
				m.hybToBand = mpsHybBand14[:]
			case 20:
				m.hybToBand = mpsHybBand20[:]
			}
			if prev != nil {
				prev(m)
			}
		}
	}
	if v := os.Getenv("MPS_FORCE_SETS"); v != "" {
		var n, last int
		fmt.Sscan(v, &n)
		fmt.Sscan(os.Getenv("MPS_FORCE_LAST"), &last)
		smooth := os.Getenv("MPS_FORCE_SMOOTH") != ""
		bandsHook := mpsTestHook
		mpsTestHook = func(m *mps212) {
			if bandsHook != nil {
				bandsHook(m)
			}
			f := &m.frame
			var baseC, baseI [mpsMaxBands]int
			for b := 0; b < m.paramBands; b++ {
				baseC[b], baseI[b] = f.cldIdx[0][b], f.iccIdx[0][b]
			}
			m.numSets = n
			for ps := 0; ps < n; ps++ {
				m.paramSlots[ps] = (ps+1)*(last+1)/n - 1
				for b := 0; b < m.paramBands; b++ {
					f.cldIdx[ps][b] = mpsBound(baseC[b]+(ps*7+b*3)%9-4, -15, 15)
					f.iccIdx[ps][b] = mpsBound(baseI[b]+(ps*5+b*2)%5-2, 0, 7)
					m.smoothingData[ps][b] = 0
					if smooth {
						m.smoothingData[ps][b] = (b + ps) % 2
					}
				}
				m.smoothingTime[ps] = 256
				if smooth {
					m.smoothingTime[ps] = 64 << (ps % 4)
				}
				m.invSmoothingTime[ps] = 1 / float32(m.smoothingTime[ps])
			}
			m.extFrame = m.paramSlots[n-1] != m.timeSlots-1
			if m.extFrame {
				f.cldIdx[n] = f.cldIdx[n-1]
				f.iccIdx[n] = f.iccIdx[n-1]
				m.smoothingData[n] = m.smoothingData[n-1]
				m.smoothingTime[n] = m.smoothingTime[n-1]
				m.invSmoothingTime[n] = m.invSmoothingTime[n-1]
			}
		}
	}
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
		snr, snrT, lag := dbgCompare2(pcm, ref, 4000)
		if os.Getenv("USAC_BLK") != "" {
			ch := len(pcm.Data)
			bs := 1024
			if v := os.Getenv("USAC_BS"); v != "" {
				fmt.Sscan(v, &bs)
			}
			for b := 0; b*bs+bs <= pcm.Frames() && b < 4000; b++ {
				var e, s float64
				for i := b * bs; i < b*bs+bs; i++ {
					for k := 0; k < ch; k++ {
						j := i - lag
						if j < 0 || (j*ch+k)*2+1 >= len(ref) {
							continue
						}
						r := float64(int16(uint16(ref[(j*ch+k)*2]) | uint16(ref[(j*ch+k)*2+1])<<8))
						v := float64(pcm.Data[k][i]) * 32768
						if v > 32767 || v < -32768 {
							continue
						}
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
		fmt.Printf("%s: rate=%d ch=%d frames=%d (ref %d) lag=%d SNR=%.1f dB (libxaac truncation: %.1f dB) stats=%+v\n", name, pcm.SampleRate, len(pcm.Data), pcm.Frames(), len(ref)/2/len(pcm.Data), lag, snr, snrT, lastUSACStats)
	}
}
