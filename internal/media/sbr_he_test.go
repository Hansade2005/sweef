package media

import (
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// heBase encodes a synthetic signal with ffmpeg's native AAC-LC encoder and
// returns the raw access units.
func heBase(t *testing.T, rate, ch int, dur float64, seed int) [][]byte {
	t.Helper()
	exprs := []string{
		"0.25*sin(2*PI*(300+200*sin(2*PI*0.5*t))*t)+0.15*sin(2*PI*880*t)*sin(2*PI*3*t)+0.1*sin(2*PI*2500*t)+0.08*(random(0)-0.5)",
		"0.2*sin(2*PI*(500+300*sin(2*PI*0.7*t))*t)+0.12*sin(2*PI*1760*t)*sin(2*PI*2*t)+0.08*sin(2*PI*3300*t)+0.08*(random(1)-0.5)",
	}
	expr := strings.Join(exprs[:ch], "|")
	tmp := filepath.Join(t.TempDir(), "base.aac")
	args := []string{"-v", "error", "-y", "-nostdin", "-f", "lavfi", "-i", fmt.Sprintf("aevalsrc='%s':s=%d:d=%g:c=%s", expr, rate, dur, map[int]string{1: "mono", 2: "stereo"}[ch]),
		"-c:a", "aac", "-b:a", strconv.Itoa(64000 * ch), "-cutoff", strconv.Itoa(rate/2 - rate/20), "-f", "adts", tmp}
	_ = seed
	if out, err := exec.Command(FFmpegPath(), args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	data, _ := os.ReadFile(tmp)
	as, err := adtsAudio(data)
	if err != nil {
		t.Fatal(err)
	}
	return as.packets
}

// heStream builds the HE-AAC MP4 for a scenario.
func heStream(t *testing.T, base [][]byte, coreRate, ch int, mode string, cfg sbrGenCfg) []byte {
	t.Helper()
	baseASC := ascHE("implicit", coreRate, 2*coreRate, ch)
	bc, err := parseASC(baseASC)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := newAACDecoder(bc)
	if err != nil {
		t.Fatal(err)
	}
	dec.pcm = make([][]float32, dec.nch)
	g := newSBRGen(cfg, coreRate)
	var out [][]byte
	for _, p := range base {
		if err := dec.decodeFrame(p); err != nil {
			t.Fatal(err)
		}
		fill := buildFill(g.frameBits(), cfg.crc)
		out = append(out, injectFill(p, dec.endBit, fill))
	}
	outRate := 2 * coreRate
	if mode == "down" {
		outRate = coreRate
		mode = "explicit-sbr"
		return buildMP4(ascHE(mode, coreRate, outRate, ch), outRate, ch, out)
	}
	chOut := ch
	if strings.HasSuffix(mode, "-ps") {
		chOut = 2
	}
	return buildMP4(ascHE(mode, coreRate, outRate, ch), coreRate, chOut, out)
}

func f32pcm(raw []byte, ch, rate int) *PCM {
	n := len(raw) / (4 * ch)
	p := &PCM{SampleRate: rate, Data: make([][]float32, ch)}
	for c := range p.Data {
		p.Data[c] = make([]float32, n)
		for i := 0; i < n; i++ {
			p.Data[c][i] = math.Float32frombits(uint32(raw[(i*ch+c)*4]) | uint32(raw[(i*ch+c)*4+1])<<8 | uint32(raw[(i*ch+c)*4+2])<<16 | uint32(raw[(i*ch+c)*4+3])<<24)
		}
	}
	return p
}

// ffRef decodes with ffmpeg at the given layout.
func ffRef(t *testing.T, data []byte, ch, rate int) *PCM {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "he.m4a")
	os.WriteFile(tmp, data, 0o644)
	out, err := exec.Command(FFmpegPath(), "-v", "error", "-nostdin", "-i", tmp, "-map", "0:a:0", "-f", "f32le", "-ac", strconv.Itoa(ch), "-ar", strconv.Itoa(rate), "pipe:1").Output()
	if err != nil {
		t.Fatalf("ffmpeg decode: %v", err)
	}
	return f32pcm(out, ch, rate)
}

// bestSNR returns the SNR over the overlap for the lag in [-maxLag, maxLag] giving the best value.
func bestSNR(got, ref *PCM, maxLag int) (float64, int) {
	best, bl := -1e9, 0
	for lag := -maxLag; lag <= maxLag; lag++ {
		var sig, noise float64
		n := 0
		for c := range ref.Data {
			if c >= len(got.Data) {
				break
			}
			for i := 4096; i < len(ref.Data[c])-4096 && i+lag < len(got.Data[c]) && i+lag >= 0; i++ {
				r := float64(ref.Data[c][i])
				d := float64(got.Data[c][i+lag]) - r
				sig += r * r
				noise += d * d
				n++
			}
		}
		if n == 0 {
			continue
		}
		snr := 200.0
		if noise > 0 {
			snr = 10 * math.Log10(sig/noise)
		}
		if snr > best {
			best, bl = snr, lag
		}
		if maxLag > 0 && lag == 0 && best > 60 {
			return best, 0
		}
	}
	return best, bl
}

type heCase struct {
	name     string
	coreRate int
	ch       int
	mode     string
	cfg      sbrGenCfg
	frames   float64
}

func defaultCfg() sbrGenCfg {
	return sbrGenCfg{seed: 1, startFreq: 5, stopFreq: 9, xover: 0, freqScale: 2, alterScale: 1, noiseBands: 2,
		limBands: 2, limGains: 2, interpol: 1, smoothing: 1, extra1: true, extra2: true, headerEvery: 20, harmProb: 30, noiseVal: -1}
}

func runHECase(t *testing.T, c heCase) {
	base := heBase(t, c.coreRate, chBase(c), c.frames, 0)
	data := heStream(t, base, c.coreRate, chBase(c), c.mode, c.cfg)
	if dir := os.Getenv("SBR_DUMP"); dir != "" {
		os.WriteFile(filepath.Join(dir, c.name+".m4a"), data, 0o644)
	}
	info, err := ProbeBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeNativeCodec(data, info)
	if err != nil {
		t.Fatalf("%s: native decode: %v", c.name, err)
	}
	// ffmpeg reports mono HE-AAC with undecided PS as two (identical) channels;
	// decode at that layout and compare the channels we produce
	ref := ffRef(t, data, 2, got.SampleRate)
	ref.Data = ref.Data[:len(got.Data)]
	snr, lag := bestSNR(got, ref, 0)
	if snr < 50 {
		snr, lag = bestSNR(got, ref, 2200)
	}
	t.Logf("%-28s rate=%d ch=%d frames=%d (ffmpeg %d) SNR=%.1f dB lag=%d", c.name, got.SampleRate, len(got.Data), got.Frames(), ref.Frames(), snr, lag)
	if snr < 50 || lag != 0 {
		t.Errorf("%s: SNR %.1f dB at lag %d", c.name, snr, lag)
	}
}

func chBase(c heCase) int {
	if strings.HasSuffix(c.mode, "-ps") {
		return 1
	}
	return c.ch
}

func variant(f func(c *sbrGenCfg)) sbrGenCfg {
	c := defaultCfg()
	f(&c)
	return c
}

// pickSpec returns the first valid (start, stop, xover) combination for the
// sampling rate and band layout, searching from the requested stop value.
func pickSpec(rate, fs, as, nb int, stop []int, xover int) (int, int, int) {
	for _, sp := range stop {
		for st := 3; st < 12; st++ {
			for xo := xover; xo < 8; xo++ {
				s := newSBRElem(false, rate, false)
				s.spec = sbrSpec{st, sp, xo, fs, as, nb}
				if s.resetTables() == nil {
					return st, sp, xo
				}
			}
		}
	}
	panic(fmt.Sprintf("no valid sbr spec for %d %d %d", rate, fs, as))
}

func heCases() []heCase {
	var cs []heCase
	add := func(name string, rate, ch int, mode string, f func(c *sbrGenCfg)) {
		cfg := variant(func(c *sbrGenCfg) {
			c.seed = int64(len(cs) + 1)
			c.cpe = ch == 2
			c.startFreq, c.stopFreq, c.xover = pickSpec(rate, c.freqScale, c.alterScale, c.noiseBands, []int{9, 8, 10, 7, 11, 6, 12}, 0)
		})
		if f != nil {
			f(&cfg)
		}
		cs = append(cs, heCase{name: name, coreRate: rate, ch: ch, mode: mode, cfg: cfg, frames: 6})
	}
	respec := func(c *sbrGenCfg, rate int, stops []int, xover int) {
		fs, as, nb := c.freqScale, c.alterScale, c.noiseBands
		if !c.extra1 {
			fs, as, nb = 2, 1, 2
		}
		c.startFreq, c.stopFreq, c.xover = pickSpec(rate, fs, as, nb, stops, xover)
	}
	add("sbr_mono_24k", 24000, 1, "explicit-sbr", nil)
	add("sbr_mono_24k_bc", 24000, 1, "bc-sbr", nil)
	add("sbr_mono_24k_implicit", 24000, 1, "implicit", nil)
	add("sbr_stereo_24k", 24000, 2, "explicit-sbr", nil)
	add("sbr_stereo_coupled", 24000, 2, "explicit-sbr", func(c *sbrGenCfg) { c.coupling = 1 })
	add("sbr_stereo_mixed", 24000, 2, "explicit-sbr", func(c *sbrGenCfg) { c.coupling = 2 })
	for _, cl := range []int{0, 1, 2, 3} {
		cl := cl
		add(fmt.Sprintf("sbr_class%d", cl), 24000, 1, "explicit-sbr", func(c *sbrGenCfg) { c.classes = []int{cl}; c.headerEvery = 0 })
	}
	add("sbr_mixed_classes", 24000, 1, "explicit-sbr", func(c *sbrGenCfg) { c.classes = []int{1, 2, 3} })
	add("sbr_stereo_classes", 24000, 2, "explicit-sbr", func(c *sbrGenCfg) { c.coupling = 2; c.classes = []int{1, 2, 3} })
	add("sbr_ampres", 24000, 1, "explicit-sbr", func(c *sbrGenCfg) { c.ampRes = 1 })
	add("sbr_coupled_ampres", 24000, 2, "explicit-sbr", func(c *sbrGenCfg) { c.coupling = 1; c.ampRes = 1 })
	add("sbr_noextra", 24000, 1, "explicit-sbr", func(c *sbrGenCfg) { c.extra1 = false; c.extra2 = false })
	for fs := 0; fs < 4; fs++ {
		for as := 0; as < 2; as++ {
			fs, as := fs, as
			add(fmt.Sprintf("sbr_freqscale%d_alter%d", fs, as), 24000, 1, "explicit-sbr", func(c *sbrGenCfg) {
				c.freqScale, c.alterScale, c.noiseBands = fs, as, (fs+as)&3
				respec(c, 24000, []int{9, 8, 10, 7, 11, 6, 12}, 0)
			})
		}
	}
	add("sbr_stop14", 24000, 1, "explicit-sbr", func(c *sbrGenCfg) { respec(c, 24000, []int{14}, 0) })
	add("sbr_stop15", 24000, 1, "explicit-sbr", func(c *sbrGenCfg) { respec(c, 24000, []int{15}, 0) })
	add("sbr_xover3", 24000, 1, "explicit-sbr", func(c *sbrGenCfg) { respec(c, 24000, []int{9, 8, 10, 7}, 3) })
	add("sbr_limiter0", 24000, 1, "explicit-sbr", func(c *sbrGenCfg) { c.limBands = 0; c.limGains = 0 })
	add("sbr_limiter1", 24000, 1, "explicit-sbr", func(c *sbrGenCfg) { c.limBands = 1; c.limGains = 1 })
	add("sbr_limiter3", 24000, 1, "explicit-sbr", func(c *sbrGenCfg) { c.limBands = 3; c.limGains = 3 })
	add("sbr_nosmooth_nointerp", 24000, 1, "explicit-sbr", func(c *sbrGenCfg) { c.smoothing = 0; c.interpol = 0 })
	add("sbr_noise0", 24000, 1, "explicit-sbr", func(c *sbrGenCfg) { c.noiseBands = 0 })
	add("sbr_noise3", 24000, 1, "explicit-sbr", func(c *sbrGenCfg) { c.noiseBands = 3; c.harmProb = 100 })
	add("sbr_switch", 24000, 1, "explicit-sbr", func(c *sbrGenCfg) { c.switchAt = 3; c.headerEvery = 2 })
	add("sbr_crc", 24000, 1, "explicit-sbr", func(c *sbrGenCfg) { c.crc = true })
	for _, r := range []int{16000, 22050, 32000, 44100, 48000} {
		r := r
		add(fmt.Sprintf("sbr_mono_%d", r), r, 1, "explicit-sbr", func(c *sbrGenCfg) { respec(c, r, []int{9, 8, 7, 6, 10, 11}, 0) })
	}
	add("sbr_stereo_16k", 16000, 2, "explicit-sbr", func(c *sbrGenCfg) { c.coupling = 2; respec(c, 16000, []int{9, 8, 7}, 0) })
	// parametric stereo
	psCase := func(name, mode string, iid, icc int, f func(c *sbrGenCfg)) {
		add(name, 24000, 1, mode, func(c *sbrGenCfg) {
			c.ps = &psGenCfg{iidMode: iid, iccMode: icc, headerEvery: 12}
			if f != nil {
				f(c)
			}
		})
	}
	psCase("ps_20band", "explicit-ps", 1, 1, nil)
	psCase("ps_10band", "explicit-ps", 0, 0, nil)
	psCase("ps_34band", "explicit-ps", 2, 2, nil)
	psCase("ps_fine20", "explicit-ps", 4, 4, nil)
	psCase("ps_fine34", "explicit-ps", 5, 5, nil)
	psCase("ps_fine10", "explicit-ps", 3, 3, nil)
	psCase("ps_fineiid_icc20", "explicit-ps", 4, 1, nil)
	psCase("ps_iid20_fineicc", "explicit-ps", 1, 4, nil)
	psCase("ps_iid_only", "explicit-ps", 1, -1, nil)
	psCase("ps_icc_only", "explicit-ps", -1, 1, nil)
	psCase("ps_mixed_10_34", "explicit-ps", 0, 2, nil)
	psCase("ps_mixed_20_34", "explicit-ps", 1, 2, nil)
	psCase("ps_mixed_34_20", "explicit-ps", 2, 1, nil)
	psCase("ps_mixed_34_10", "explicit-ps", 2, 0, nil)
	psCase("ps_mixed_10_20", "explicit-ps", 0, 1, nil)
	psCase("ps_ipd_20", "explicit-ps", 1, 1, func(c *sbrGenCfg) { c.ps.ipd = true })
	psCase("ps_ipd_10", "explicit-ps", 0, 0, func(c *sbrGenCfg) { c.ps.ipd = true })
	psCase("ps_ipd_34", "explicit-ps", 2, 2, func(c *sbrGenCfg) { c.ps.ipd = true })
	psCase("ps_fixed_class", "explicit-ps", 1, 1, func(c *sbrGenCfg) { c.ps.classes = []int{0} })
	psCase("ps_var_class", "explicit-ps", 1, 1, func(c *sbrGenCfg) { c.ps.classes = []int{1} })
	psCase("ps_switch_20_34", "explicit-ps", 1, 1, func(c *sbrGenCfg) { c.ps.switchAt = 3; c.ps.switchIID = 2; c.ps.switchICC = 2 })
	psCase("ps_switch_34_20", "explicit-ps", 2, 2, func(c *sbrGenCfg) { c.ps.switchAt = 3; c.ps.switchIID = 1; c.ps.switchICC = 1 })
	psCase("ps_bc", "bc-ps", 1, 1, nil)
	psCase("ps_implicit", "implicit", 1, 1, nil)
	psCase("ps_explicit_sbr_only", "explicit-sbr", 1, 1, nil)
	add("sbr_down_48k", 48000, 1, "down", func(c *sbrGenCfg) { respec(c, 48000, []int{8, 7, 6, 9}, 0) })
	add("sbr_down_44k_stereo", 44100, 2, "down", func(c *sbrGenCfg) { c.coupling = 2; respec(c, 44100, []int{8, 7, 6, 9}, 0) })
	return cs
}

func TestHEAACMatchesFFmpeg(t *testing.T) {
	if FFmpegPath() == "" {
		t.Skip("no ffmpeg for the reference decode")
	}
	only := os.Getenv("HE_ONLY")
	for _, c := range heCases() {
		if only != "" && !strings.Contains(c.name, only) {
			continue
		}
		c := c
		t.Run(c.name, func(t *testing.T) { runHECase(t, c) })
	}
}
