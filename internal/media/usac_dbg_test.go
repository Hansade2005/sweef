package media

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUSACDbgConfig(t *testing.T) {
	dir := os.Getenv("SWEEF_FATE_DIR")
	if dir == "" {
		t.Skip()
	}
	files, _ := filepath.Glob(filepath.Join(dir, "usac", "*.m*"))
	for _, f := range files {
		data, _ := os.ReadFile(f)
		info, err := ProbeBytes(data)
		if err != nil {
			t.Log(f, err)
			continue
		}
		as, err := loadAudioStream(data, info)
		if err != nil {
			t.Log(f, err)
			continue
		}
		fmt.Printf("%s: codec=%s packets=%d skip=%d editDur=%d cfg=% x\n", filepath.Base(f), info.Audio().Codec, len(as.packets), as.skip, as.editDur, as.cfg)
		c, err := parseUsacASC(as.cfg)
		if err != nil {
			fmt.Println("  err", err)
			continue
		}
		fmt.Printf("  %+v\n", *c)
	}
}

// TestUSACDbgDump writes libxaac "raw" test inputs (scratch use only).
func TestUSACDbgDump(t *testing.T) {
	dir := os.Getenv("SWEEF_FATE_DIR")
	out := os.Getenv("USAC_DUMP_DIR")
	if dir == "" || out == "" {
		t.Skip()
	}
	files, _ := filepath.Glob(filepath.Join(dir, "usac", "*.m*"))
	more, _ := filepath.Glob(filepath.Join(dir, "aac", "*.mp4"))
	files = append(files, more...)
	for _, f := range files {
		data, _ := os.ReadFile(f)
		info, err := ProbeBytes(data)
		if err != nil || info.Audio() == nil || info.Audio().Codec != "mp4a.40.42" {
			continue
		}
		as, err := loadAudioStream(data, info)
		if err != nil {
			continue
		}
		base := filepath.Join(out, filepath.Base(f))
		raw := append([]byte{}, as.cfg...)
		for _, p := range as.packets {
			raw = append(raw, p...)
		}
		os.WriteFile(base+".raw", raw, 0o644)
		m := fmt.Sprintf("-dec_info_init:%d\n-g_track_count:1\n-movie_time_scale:48000\n-media_time_scale:48000\n-ia_mp4_stsz_entries:%d\n-playTimeInSamples:%d\n-startOffsetInSamples:%d\n-useEditlist:1\n", len(as.cfg), len(as.packets), as.editDur, as.skip)
		for _, p := range as.packets {
			m += fmt.Sprintf("-ia_mp4_stsz_size:%d\n", len(p))
		}
		os.WriteFile(base+".meta", []byte(m), 0o644)
	}
}

func TestUSACDbgDecode(t *testing.T) {
	dir := os.Getenv("SWEEF_FATE_DIR")
	if dir == "" {
		t.Skip()
	}
	cases := []struct{ f, ref string }{
		{"Fd_1_c1_0x03.mp4", "Fd_1_c1_0x03.s16"},
		{"Fd_1_c1_0x04.mp4", "Fd_1_c1_0x04.s16"},
		{"Fd_2_c1_0x03.mp4", "Fd_2_c1_0x03.s16"},
		{"Fd_2_c1_0x05.mp4", "Fd_2_c1_0x05.s16"},
		{"Fd_2_c1_Tns_0x04.mp4", "Fd_2_c1_Tns_0x04.s16"},
		{"Fd_2_c1_Ms_0x01.mp4", "Fd_2_c1_Ms_0x01.s16"},
		{"Fd_2_c1_Ms_0x04.mp4", "Fd_2_c1_Ms_0x04.s16"},
	}
	cases = append(cases, struct{ f, ref string }{"xhe_target_level.m4a", "xhe_target_level.s16"})
	cases = append(cases, struct{ f, ref string }{"Ext_2_c1_Ln_0x03.mp4", "Ext_2_c1_Ln_0x03__Lou-16.s16"})
	cases = append(cases, struct{ f, ref string }{"Ext_2_c1_Ln_0x03.mp4", "Ext_2_c1_Ln_0x03__Lou-24.s16"})
	cases = append(cases, struct{ f, ref string }{"Ext_2_c1_Ln_0x03.mp4", "Ext_2_c1_Ln_0x03__Lou-31.s16"})
	SetFFmpegEnabled(false)
	for _, c := range cases {
		if strings.Contains(c.ref, "Lou-") {
			var tl float64
			fmt.Sscanf(c.ref[strings.Index(c.ref, "Lou")+3:], "%f", &tl)
			SetUSACTargetLoudness(tl)
		} else if strings.Contains(c.f, "xhe") {
			SetUSACTargetLoudness(-24)
		} else {
			SetUSACTargetLoudness(0)
		}
		data, err := os.ReadFile(filepath.Join(dir, "usac", c.f))
		if err != nil {
			t.Log(err)
			continue
		}
		ref, _ := os.ReadFile(filepath.Join(dir, "usac", c.ref))
		pcm, err := DecodeAudio(data)
		if err != nil {
			t.Errorf("%s: %v", c.f, err)
			continue
		}
		ch := len(pcm.Data)
		n := len(ref) / 2 / ch
		if n > pcm.Frames() {
			n = pcm.Frames()
		}
		var sig, noise float64
		for i := 0; i < n; i++ {
			for k := 0; k < ch; k++ {
				r := float64(int16(uint16(ref[(i*ch+k)*2]) | uint16(ref[(i*ch+k)*2+1])<<8))
				v := float64(pcm.Data[k][i]) * 32768
				if v > 32767 {
					v = 32767
				} else if v < -32768 {
					v = -32768
				}
				sig += r * r
				noise += (v - r) * (v - r)
			}
		}
		fmt.Printf("%s: rate=%d ch=%d frames=%d (ref %d) SNR=%.1f dB stats=%+v\n", c.f, pcm.SampleRate, ch, pcm.Frames(), len(ref)/2/ch, 10*math.Log10(sig/(noise+1e-9)), lastUSACStats)
	}
}

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

// dbgCompare returns (snr dB, lag) of pcm against a s16 reference.
func dbgCompare(pcm *PCM, ref []byte, maxLag int) (float64, int) {
	ch := len(pcm.Data)
	n := len(ref) / 2 / ch
	rs := func(i, k int) float64 {
		return float64(int16(uint16(ref[(i*ch+k)*2]) | uint16(ref[(i*ch+k)*2+1])<<8))
	}
	best, bl := math.Inf(1), 0
	const win = 30000
	for lag := -maxLag; lag <= maxLag; lag++ {
		var e float64
		for i := 20000; i < 20000+win; i++ {
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
			if v > 32767 {
				v = 32767
			} else if v < -32768 {
				v = -32768
			}
			sig += r * r
			noise += (v - r) * (v - r)
		}
	}
	return 10 * math.Log10(sig/(noise+1e-9)), bl
}

func TestUSACDbgRaw(t *testing.T) {
	names := os.Getenv("USAC_RAW")
	if names == "" {
		t.Skip()
	}
	dir := os.Getenv("USAC_XDIR")
	for _, name := range strings.Split(names, ",") {
		as, err := dbgLoadRaw(filepath.Join(dir, "xin", name))
		if err != nil {
			t.Fatal(err)
		}
		ref, err := os.ReadFile(filepath.Join(dir, "xref", name+".s16"))
		if err != nil {
			t.Fatal(err)
		}
		usacPrimePreroll = os.Getenv("USAC_NOPRIME") == ""
		if v := os.Getenv("USAC_TARGET"); v != "" {
			var tl float64
			fmt.Sscanf(v, "%f", &tl)
			SetUSACTargetLoudness(tl)
		}
		pcm, err := decodeUSACStream(as)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		snr, lag := dbgCompare(pcm, ref, 4000)
		if o := os.Getenv("USAC_OUT"); o != "" {
			ch := len(pcm.Data)
			buf := make([]byte, 0, pcm.Frames()*ch*2)
			for i := 0; i < pcm.Frames(); i++ {
				for k := 0; k < ch; k++ {
					v := math.Round(float64(pcm.Data[k][i]) * 32768)
					if v > 32767 {
						v = 32767
					} else if v < -32768 {
						v = -32768
					}
					buf = append(buf, byte(int16(v)), byte(int16(v)>>8))
				}
			}
			os.WriteFile(filepath.Join(o, name+".go.s16"), buf, 0o644)
		}
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
						v := float64(pcm.Data[k][i]) * 32768
						if v > 32767 {
							v = 32767
						} else if v < -32768 {
							v = -32768
						}
						e += (v - r) * (v - r)
						s += r * r
					}
				}
				fmt.Printf("blk %d snr %.1f\n", b, 10*math.Log10((s+1)/(e+1)))
			}
		}
		fmt.Printf("%s: rate=%d ch=%d frames=%d (ref %d) lag=%d SNR=%.1f dB stats=%+v\n", name, pcm.SampleRate, len(pcm.Data), pcm.Frames(), len(ref)/2/len(pcm.Data), lag, snr, lastUSACStats)
	}
}

func TestUSACDbgSub(t *testing.T) {
	names := os.Getenv("USAC_RAW")
	blk := 0
	fmt.Sscanf(os.Getenv("USAC_SUB"), "%d", &blk)
	if names == "" || blk == 0 {
		t.Skip()
	}
	dir := os.Getenv("USAC_XDIR")
	as, _ := dbgLoadRaw(filepath.Join(dir, "xin", names))
	ref, _ := os.ReadFile(filepath.Join(dir, "xref", names+".s16"))
	pcm, _ := decodeUSACStream(as)
	ch := len(pcm.Data)
	for k := 0; k < ch; k++ {
		for s := 0; s < 8; s++ {
			var e, sg float64
			for i := blk*1024 + s*128; i < blk*1024+s*128+128; i++ {
				r := float64(int16(uint16(ref[(i*ch+k)*2]) | uint16(ref[(i*ch+k)*2+1])<<8))
				v := float64(pcm.Data[k][i]) * 32768
				e += (v - r) * (v - r)
				sg += r * r
			}
			fmt.Printf("blk %d ch %d sub %d snr %.1f rms %.0f\n", blk, k, s, 10*math.Log10((sg+1)/(e+1)), math.Sqrt(sg/128))
			if os.Getenv("USAC_DUMP") != "" && k == 0 && s == 2 {
				for i := blk*1024 + s*128; i < blk*1024+s*128+128; i += 4 {
					r := float64(int16(uint16(ref[(i*ch+k)*2]) | uint16(ref[(i*ch+k)*2+1])<<8))
					fmt.Printf("  %d ref %.0f got %.1f\n", i, r, float64(pcm.Data[k][i])*32768)
				}
			}
		}
	}
}

func TestUSACDbgCfg(t *testing.T) {
	names := os.Getenv("USAC_RAW")
	if names == "" {
		t.Skip()
	}
	for _, name := range strings.Split(names, ",") {
		as, _ := dbgLoadRaw(filepath.Join(os.Getenv("USAC_XDIR"), "xin", name))
		c, err := parseUsacASC(as.cfg)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("%s: rate %d ccfl %d chcfg %d sbr %d\n", name, c.rate, c.coreLen, c.chCfgIdx, c.sbrRatioIdx)
		for _, e := range c.elems {
			fmt.Printf("   type %d tw %v nf %v stereo %d ext %d fraglen %d frag %v\n", e.typ, e.twMDCT, e.noiseFill, e.stereoCfg, e.extType, e.extDefLen, e.extFrag)
		}
		fmt.Printf("   cfg ext loud %v streamid %d\n", c.hasLoudnessExt, c.streamID)
		for _, l := range c.loudness {
			fmt.Printf("   loud: drc %d eq %d dmx %d peak %d tp %d n %d def %v val %v meas %v\n", l.drcSetID, l.eqSetID, l.downmixID, l.samplePeak, l.truePeak, l.nMeas, l.methodDef[:l.nMeas], l.methodVal[:l.nMeas], l.measurement[:l.nMeas])
		}
	}
}
