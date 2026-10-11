package media

import (
	"fmt"
	"math"
	"math/rand"
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

// heStream builds the HE-AAC MP4 for a one element scenario.
func heStream(t *testing.T, base [][]byte, coreRate, ch int, mode string, cfg sbrGenCfg) []byte {
	t.Helper()
	return heStreamElems(t, base, coreRate, ch, mode, []sbrGenCfg{cfg})
}

// heStreamElems splices SBR (and PS) payloads into every access unit of the
// AAC-LC stream base: cfgs holds one configuration per SCE/CPE element in
// stream order (the element type is taken from the stream).
func heStreamElems(t *testing.T, base [][]byte, coreRate, chCfg int, mode string, cfgs []sbrGenCfg) []byte {
	t.Helper()
	out, nch := hePackets(t, base, coreRate, chCfg, cfgs)
	ch := nch
	outRate := 2 * coreRate
	if mode == "down" {
		outRate = coreRate
		mode = "explicit-sbr"
		return buildMP4(ascHE(mode, coreRate, outRate, chCfg), outRate, ch, out)
	}
	if strings.HasSuffix(mode, "-ps") {
		ch = 2
	}
	return buildMP4(ascHE(mode, coreRate, outRate, chCfg), coreRate, ch, out)
}

// hePackets splices the payloads into the access units and returns them with
// the channel count of the core stream.
func hePackets(t *testing.T, base [][]byte, coreRate, chCfg int, cfgs []sbrGenCfg) ([][]byte, int) {
	t.Helper()
	baseASC := ascHE("implicit", coreRate, 2*coreRate, chCfg)
	bc, err := parseASC(baseASC)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := newAACDecoder(bc)
	if err != nil {
		t.Fatal(err)
	}
	dec.pcm = make([][]float32, dec.nch)
	gens := make([]*sbrGen, len(cfgs))
	var out [][]byte
	for _, p := range base {
		if err := dec.decodeFrame(p); err != nil {
			t.Fatal(err)
		}
		w := &bitW{}
		pos, ord := 0, 0
		copyBits := func(to int) {
			for ; pos < to; pos++ {
				w.put(int(p[pos>>3]>>(7-uint(pos&7)))&1, 1)
			}
		}
		for i, end := range dec.elemEnds {
			copyBits(end)
			if dec.elemKind[i] == 2 {
				continue
			}
			if ord >= len(cfgs) {
				t.Fatalf("stream has more elements than configurations")
			}
			if gens[ord] == nil {
				c := cfgs[ord]
				c.cpe = dec.elemKind[i] == 1
				gens[ord] = newSBRGen(c, coreRate)
			}
			w.append(buildFill(gens[ord].frameBits(), gens[ord].cfg.crc))
			ord++
		}
		copyBits(dec.endBit)
		w.put(7, 3)
		out = append(out, w.b)
	}
	return out, dec.nch
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

// TestHEAACMultichannelMatchesFFmpeg adds SBR to the 5.1 fixture (SCE + 2 CPE + LFE).
func TestHEAACMultichannelMatchesFFmpeg(t *testing.T) {
	if FFmpegPath() == "" {
		t.Skip("no ffmpeg for the reference decode")
	}
	data := nativeData(t, "aac_51.m4a")
	info, _ := ProbeBytes(data)
	as, err := loadAudioStream(data, info)
	if err != nil {
		t.Fatal(err)
	}
	cfg := defaultCfg()
	cfg.startFreq, cfg.stopFreq, cfg.xover = pickSpec(48000, 2, 1, 2, []int{6, 7, 5}, 0)
	cfgs := []sbrGenCfg{cfg, cfg, cfg}
	for i := range cfgs {
		cfgs[i].seed = int64(i + 1)
		cfgs[i].coupling = 2
	}
	he := heStreamElems(t, as.packets, 48000, 6, "explicit-sbr", cfgs)
	hinfo, err := ProbeBytes(he)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeNativeCodec(he, hinfo)
	if err != nil {
		t.Fatal(err)
	}
	ref := ffRef(t, he, len(got.Data), got.SampleRate)
	for c := range got.Data {
		snr, lag := bestSNR(&PCM{Data: got.Data[c : c+1]}, &PCM{Data: ref.Data[c : c+1]}, 0)
		t.Logf("5.1 SBR channel %d: SNR %.1f dB lag %d (rate %d, frames %d vs %d)", c, snr, lag, got.SampleRate, got.Frames(), ref.Frames())
		if snr < 50 {
			t.Errorf("channel %d: SNR %.1f dB", c, snr)
		}
	}
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

// ---- tests that need no ffmpeg --------------------------------------------------

// fixtureHE splices SBR (and PS) into an AAC-LC fixture.
func fixtureHE(t *testing.T, fixture, mode string, cfgs []sbrGenCfg) (data []byte, coreRate, chCfg int, packets int) {
	t.Helper()
	raw := nativeData(t, fixture)
	info, err := ProbeBytes(raw)
	if err != nil {
		t.Fatal(err)
	}
	as, err := loadAudioStream(raw, info)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := parseASC(as.cfg)
	if err != nil {
		t.Fatal(err)
	}
	return heStreamElems(t, as.packets, cfg.rate, cfg.chCfg, mode, cfgs), cfg.rate, cfg.chCfg, len(as.packets)
}

// adtsFromPackets wraps access units in ADTS frames (AAC-LC profile).
func adtsFromPackets(packets [][]byte, rate, chCfg int) []byte {
	sfi := 0
	for i, r := range aacRates {
		if r == rate {
			sfi = i
		}
	}
	var out []byte
	for _, p := range packets {
		n := len(p) + 7
		out = append(out, 0xff, 0xf1, byte(1<<6|sfi<<2|chCfg>>2), byte(chCfg&3<<6|n>>11), byte(n>>3&0xff), byte(n&7<<5|0x1f), 0xfc)
		out = append(out, p...)
	}
	return out
}

// highBandFraction is the share of energy above frac*Nyquist.
func highBandFraction(x []float32, frac float64) float64 {
	n := 8192
	if len(x) < 3*n {
		return 0
	}
	var hi, tot float64
	for off := n; off+n <= len(x)-n; off += n {
		buf := make([]complex128, n)
		for i := range buf {
			w := 0.5 - 0.5*math.Cos(2*math.Pi*float64(i)/float64(n))
			buf[i] = complex(float64(x[off+i])*w, 0)
		}
		fftc(buf)
		for k := 1; k < n/2; k++ {
			e := real(buf[k])*real(buf[k]) + imag(buf[k])*imag(buf[k])
			tot += e
			if float64(k) >= frac*float64(n/2) {
				hi += e
			}
		}
	}
	if tot == 0 {
		return 0
	}
	return hi / tot
}

func fftc(x []complex128) {
	n := len(x)
	for i, j := 1, 0; i < n; i++ {
		bit := n >> 1
		for ; j&bit != 0; bit >>= 1 {
			j ^= bit
		}
		j ^= bit
		if i < j {
			x[i], x[j] = x[j], x[i]
		}
	}
	for l := 2; l <= n; l <<= 1 {
		w := complex(math.Cos(2*math.Pi/float64(l)), -math.Sin(2*math.Pi/float64(l)))
		for i := 0; i < n; i += l {
			c := complex(1, 0)
			for k := 0; k < l/2; k++ {
				u, v := x[i+k], x[i+k+l/2]*c
				x[i+k], x[i+k+l/2] = u+v, u-v
				c *= w
			}
		}
	}
}

func TestHEAACNativeWithoutFFmpeg(t *testing.T) {
	SetFFmpegEnabled(false)
	defer SetFFmpegEnabled(true)
	psCfg := defaultCfg()
	psCfg.startFreq, psCfg.stopFreq, psCfg.xover = pickSpec(16000, 2, 1, 2, []int{9, 8, 7}, 0)
	psCfg.ps = &psGenCfg{iidMode: 2, iccMode: 2, headerEvery: 8, ipd: true}
	sbrCfg := defaultCfg()
	sbrCfg.startFreq, sbrCfg.stopFreq, sbrCfg.xover = pickSpec(16000, 2, 1, 2, []int{9, 8, 7}, 0)
	stCfg := defaultCfg()
	stCfg.coupling = 2
	stCfg.startFreq, stCfg.stopFreq, stCfg.xover = pickSpec(44100, 2, 1, 2, []int{9, 8, 7, 6}, 0)
	cases := []struct {
		name, fixture, mode string
		cfgs                []sbrGenCfg
		wantCh              int
	}{
		{"sbr mono explicit", "aac_speech16k.m4a", "explicit-sbr", []sbrGenCfg{sbrCfg}, 1},
		{"sbr mono implicit", "aac_speech16k.m4a", "implicit", []sbrGenCfg{sbrCfg}, 1},
		{"sbr mono backward compatible", "aac_speech16k.m4a", "bc-sbr", []sbrGenCfg{sbrCfg}, 1},
		{"ps explicit", "aac_speech16k.m4a", "explicit-ps", []sbrGenCfg{psCfg}, 2},
		{"ps implicit", "aac_speech16k.m4a", "implicit", []sbrGenCfg{psCfg}, 2},
		{"sbr stereo coupled", "aac_st_ms.m4a", "explicit-sbr", []sbrGenCfg{stCfg}, 2},
		{"sbr stereo downsampled", "aac_st_ms.m4a", "down", []sbrGenCfg{stCfg}, 2},
	}
	for _, c := range cases {
		data, core, _, packets := fixtureHE(t, c.fixture, c.mode, c.cfgs)
		info, err := ProbeBytes(data)
		if err != nil {
			t.Fatal(c.name, err)
		}
		if !NativeAudio(info) {
			t.Errorf("%s: not decoded natively (codec %s)", c.name, info.Audio().Codec)
			continue
		}
		p, err := DecodeAudio(data)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		wantRate, wantFrames := 2*core, 2048*packets
		if c.mode == "down" {
			wantRate, wantFrames = core, 1024*packets
		}
		if p.SampleRate != wantRate || len(p.Data) != c.wantCh || p.Frames() != wantFrames {
			t.Errorf("%s: got %d Hz %d ch %d frames, want %d Hz %d ch %d frames", c.name, p.SampleRate, len(p.Data), p.Frames(), wantRate, c.wantCh, wantFrames)
			continue
		}
		for ch, d := range p.Data {
			for i, v := range d {
				if v != v || v > 8 || v < -8 {
					t.Fatalf("%s: channel %d sample %d = %v", c.name, ch, i, v)
				}
			}
		}
		if c.mode != "down" {
			// the high half of the output spectrum only exists thanks to SBR
			if f := highBandFraction(p.Data[0], 0.5); f < 1e-5 {
				t.Errorf("%s: no energy above the core band (%.2e)", c.name, f)
			}
		}
		if c.wantCh == 2 && c.cfgs[0].ps != nil {
			var d, s float64
			for i := range p.Data[0] {
				a, b := float64(p.Data[0][i]), float64(p.Data[1][i])
				d += (a - b) * (a - b)
				s += a*a + b*b
			}
			if d < 1e-3*s {
				t.Errorf("%s: PS output channels are identical", c.name)
			}
		}
	}
	// implicit signalling inside an ADTS stream (no sample entry or ASC to tell)
	raw := nativeData(t, "aac_speech16k.m4a")
	rinfo, _ := ProbeBytes(raw)
	ras, _ := loadAudioStream(raw, rinfo)
	pk, _ := hePackets(t, ras.packets, 16000, 1, []sbrGenCfg{sbrCfg})
	adts := adtsFromPackets(pk, 16000, 1)
	if ainfo, err := ProbeBytes(adts); err != nil || ainfo.Container != "aac" || !NativeAudio(ainfo) {
		t.Fatalf("adts HE-AAC probe: %v %+v", err, ainfo)
	}
	if p, err := DecodeAudio(adts); err != nil || p.SampleRate != 32000 || len(p.Data) != 1 || p.Frames() != 2048*len(pk) || highBandFraction(p.Data[0], 0.5) < 1e-5 {
		t.Errorf("adts implicit HE-AAC: %v %v", err, p)
	}
	for mime, want := range map[string]string{
		`audio/mp4; codecs="mp4a.40.5"`: "probably", `audio/mp4; codecs="mp4a.40.29"`: "probably",
		`audio/mp4; codecs="mp4a.40.2"`: "probably", `video/mp4; codecs="mp4a.40.5"`: CanPlayType(`video/mp4; codecs="mp4a.40.5"`),
	} {
		if got := CanPlayType(mime); got != want {
			t.Errorf("canPlayType(%s) = %q want %q", mime, got, want)
		}
	}
	for _, m := range []string{`audio/mp4; codecs="mp4a.40.5"`, `audio/mp4; codecs="mp4a.40.29"`} {
		if !IsTypeSupportedMSE(m) {
			t.Errorf("MSE isTypeSupported(%s) must not need ffmpeg", m)
		}
	}
}

// Damaged SBR / PS payloads must never crash the decoder.
func TestHEAACDamagedPayloads(t *testing.T) {
	psCfg := defaultCfg()
	psCfg.startFreq, psCfg.stopFreq, psCfg.xover = pickSpec(16000, 2, 1, 2, []int{9, 8, 7}, 0)
	psCfg.ps = &psGenCfg{iidMode: 2, iccMode: 2, headerEvery: 4, ipd: true}
	stCfg := defaultCfg()
	stCfg.coupling = 2
	stCfg.classes = []int{0, 1, 2, 3}
	stCfg.startFreq, stCfg.stopFreq, stCfg.xover = pickSpec(44100, 2, 1, 2, []int{9, 8, 7, 6}, 0)
	for _, tc := range []struct {
		fixture string
		cfg     sbrGenCfg
	}{{"aac_speech16k.m4a", psCfg}, {"aac_st_ms.m4a", stCfg}} {
		data, _, _, _ := fixtureHE(t, tc.fixture, "explicit-ps", []sbrGenCfg{tc.cfg})
		if tc.fixture != "aac_speech16k.m4a" {
			data, _, _, _ = fixtureHE(t, tc.fixture, "explicit-sbr", []sbrGenCfg{tc.cfg})
		}
		info, err := ProbeBytes(data)
		if err != nil {
			t.Fatal(err)
		}
		as, err := loadAudioStream(data, info)
		if err != nil {
			t.Fatal(err)
		}
		rng := rand.New(rand.NewSource(7))
		for iter := 0; iter < 150; iter++ {
			pk := make([][]byte, len(as.packets))
			for i, p := range as.packets {
				pk[i] = append([]byte(nil), p...)
			}
			flips := 1 + rng.Intn(6)
			for f := 0; f < flips; f++ {
				i := rng.Intn(len(pk))
				// damage the tail of the access unit, where the fill element sits
				lo := len(pk[i]) / 2
				pk[i][lo+rng.Intn(len(pk[i])-lo)] ^= byte(1 << uint(rng.Intn(8)))
			}
			if iter%10 == 0 {
				pk[rng.Intn(len(pk))] = pk[rng.Intn(len(pk))][:20]
			}
			bad := *as
			bad.packets = pk
			if _, err := decodeAACStream(data, &bad); err != nil && strings.Contains(err.Error(), "fault") {
				t.Fatalf("%s iteration %d: %v", tc.fixture, iter, err)
			}
		}
	}
}

// Random bits fed to the SBR / PS parsers and the processing chain must never panic.
func TestSBRRandomPayloads(t *testing.T) {
	rng := rand.New(rand.NewSource(3))
	in := [][]float32{make([]float32, 1024), make([]float32, 1024)}
	for i := range in[0] {
		in[0][i] = float32(0.3 * math.Sin(float64(i)*0.07))
		in[1][i] = float32(0.2 * math.Sin(float64(i)*0.11))
	}
	out := [][]float32{make([]float32, 2048), make([]float32, 2048)}
	for _, cpe := range []bool{false, true} {
		for _, rate := range []int{16000, 22050, 24000, 32000, 44100, 48000} {
			el := newSBRElem(cpe, rate, false)
			el.psOK = !cpe
			n := 1
			if cpe {
				n = 2
			}
			for iter := 0; iter < 400; iter++ {
				b := make([]byte, 1+rng.Intn(80))
				rng.Read(b)
				if iter%3 == 0 { // make a header likely
					b[0] |= 0x40
				}
				func() {
					defer func() {
						if r := recover(); r != nil {
							t.Fatalf("cpe=%v rate=%d iter %d: panic %v", cpe, rate, iter, r)
						}
					}()
					el.parse(&aacBits{b: b}, len(b)*8, iter%7 == 0)
					el.apply(in[:n], out)
					for _, v := range out[0] {
						if v != v {
							t.Fatalf("NaN output")
						}
					}
				}()
			}
		}
	}
}

func TestPSRandomPayloads(t *testing.T) {
	rng := rand.New(rand.NewSource(5))
	p := newPS()
	var x, l, r [38][64][2]float64
	started := 0
	for iter := 0; iter < 4000; iter++ {
		for k := 0; k < 38; k++ {
			for b := 0; b < 64; b++ {
				x[k][b] = [2]float64{rng.NormFloat64(), rng.NormFloat64()}
			}
		}
		b := make([]byte, 2+rng.Intn(60))
		rng.Read(b)
		if iter%2 == 0 {
			b[0] |= 0x80 // ps header
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("iter %d: panic %v", iter, r)
				}
			}()
			p.parse(&aacBits{b: b}, len(b)*8)
			if p.start {
				started++
				p.apply(&x, &l, &r, 20+rng.Intn(40))
			}
		}()
	}
	if started < 100 {
		t.Errorf("only %d random payloads started PS processing", started)
	}
}

// TestWriteHEFixtures writes the HE-AAC fixtures of the engine tests
// (HE_WRITE_FIXTURES=<dir> go test ./internal/media -run TestWriteHEFixtures).
func TestWriteHEFixtures(t *testing.T) {
	dir := os.Getenv("HE_WRITE_FIXTURES")
	if dir == "" {
		t.Skip("set HE_WRITE_FIXTURES to regenerate the fixtures")
	}
	st := defaultCfg()
	st.coupling = 2
	st.classes = []int{0, 1, 2, 3}
	st.startFreq, st.stopFreq, st.xover = pickSpec(44100, 2, 1, 2, []int{9, 8, 7, 6}, 0)
	ps := defaultCfg()
	ps.startFreq, ps.stopFreq, ps.xover = pickSpec(16000, 2, 1, 2, []int{9, 8, 7}, 0)
	ps.ps = &psGenCfg{iidMode: 2, iccMode: 2, headerEvery: 8, ipd: true}
	for name, f := range map[string]struct {
		fixture, mode string
		cfg           sbrGenCfg
	}{"he_sbr_st.m4a": {"aac_st_ms.m4a", "explicit-sbr", st}, "he_ps.m4a": {"aac_speech16k.m4a", "explicit-ps", ps}} {
		data, _, _, _ := fixtureHE(t, f.fixture, f.mode, []sbrGenCfg{f.cfg})
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}
