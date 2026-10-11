package engine

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/sweefbrowser/sweef/internal/media"
)

type waResult struct {
	Name   string         `json:"name"`
	Rate   float64        `json:"rate"`
	Length int            `json:"length"`
	Ch     []string       `json:"ch"`
	Extra  map[string]any `json:"extra"`
	Error  string         `json:"error"`
}

func runWA(t *testing.T, name string) *waResult {
	t.Helper()
	srv := mediaServer(t)
	o := DefaultOptions()
	o.SettleMs = 30
	e := New(o)
	defer e.Close()
	tab, err := e.NewTab(srv.URL + "/wa.html?s=" + name)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		tab.Settle(50 * time.Millisecond)
		if v, _ := tab.Evaluate("window.__done"); string(v) == "true" {
			break
		}
	}
	raw, err := tab.Evaluate("JSON.stringify(window.__result)")
	if err != nil {
		t.Fatal(err)
	}
	var s string
	json.Unmarshal(raw, &s)
	var r waResult
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		t.Fatalf("%s: bad result %q", name, s)
	}
	return &r
}

func loadWARef(t *testing.T, name string) *waResult {
	b, err := os.ReadFile(filepath.Join("testdata/media/.ref", "wa_"+name+".json"))
	if err != nil {
		t.Skipf("no reference for %s", name)
	}
	var r waResult
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

func f32s(s string) []float32 {
	b, _ := base64.StdEncoding.DecodeString(s)
	out := make([]float32, len(b)/4)
	for i := range out {
		out[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return out
}

func maxAbsErr(a, b []float32) (float64, int) {
	var m float64
	at := 0
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		d := math.Abs(float64(a[i]) - float64(b[i]))
		if math.IsNaN(d) {
			d = 10
		}
		if d > m {
			m, at = d, i
		}
	}
	return m, at
}

// tolerance per scenario (max abs sample error against Chromium). bufsource_rate: the last
// sample of a non-looping buffer differs by 0.014 (Chromium interpolates past the end).
// waveshaper: 2x/4x oversampling uses a different (zero-latency) resampler.
var waTol = map[string]float64{
	"osc_sine": 1e-3, "osc_types": 1e-3, "osc_types_low": 1e-3, "osc_freq_ramp": 1e-3, "osc_freq_ramp_late": 3.0, "osc_periodic": 1e-3, "osc_biquad_gain": 1e-3,
	"biquad_types": 1e-3, "bufsource_rate": 2e-2, "bufsource_loop": 1e-3, "bufsource_offset": 1e-3, "gain_automation": 1e-3,
	"param_audio_rate": 1e-3, "delay": 1e-3, "convolver": 1e-3, "convolver_raw": 1e-3, "waveshaper": 3.0, "stereopanner": 1e-3,
	"panner": 1e-3, "iir": 1e-3, "mixing": 1e-3, "suspend_resume": 1e-3, "decode": 1e-5, "decode_mp3": 1e-3,
	// the compressor follows Chromium's structure but is not a bit-exact port
	"compressor": 0.5,
}

func TestWebAudioOfflineMatchesChromium(t *testing.T) {
	names := []string{"osc_sine", "osc_types", "osc_types_low", "osc_freq_ramp", "osc_freq_ramp_late", "osc_periodic", "osc_biquad_gain", "biquad_types", "bufsource_rate", "bufsource_loop", "bufsource_offset", "gain_automation", "param_audio_rate", "delay", "convolver", "convolver_raw", "waveshaper", "stereopanner", "panner", "iir", "mixing", "suspend_resume", "decode", "decode_mp3", "compressor"}
	var report []string
	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			ref := loadWARef(t, name)
			got := runWA(t, name)
			if got.Error != "" {
				t.Fatalf("sweef error: %s", got.Error)
			}
			if got.Length != ref.Length || len(got.Ch) != len(ref.Ch) {
				t.Fatalf("shape %dx%d vs Chromium %dx%d", got.Length, len(got.Ch), ref.Length, len(ref.Ch))
			}
			worst := 0.0
			for c := range ref.Ch {
				e, at := maxAbsErr(f32s(got.Ch[c]), f32s(ref.Ch[c]))
				if e > worst {
					worst = e
				}
				if e > waTol[name] {
					t.Errorf("channel %d: max abs error %.3g at frame %d (tolerance %.3g)", c, e, at, waTol[name])
				}
			}
			report = append(report, fmt.Sprintf("%-18s max abs err vs Chromium %.3e", name, worst))
			t.Logf("max abs error %.3e", worst)
		})
	}
	t.Log("\n" + joinLines(report))
}

func joinLines(l []string) string {
	s := ""
	for _, x := range l {
		s += x + "\n"
	}
	return s
}

func TestWebAudioExtras(t *testing.T) {
	// analyser output, frequency responses, suspend timing, decode errors, API shape
	for _, name := range []string{"biquad_response", "iir", "analyser", "suspend_resume", "decode", "decode_bad", "api_shape", "compressor"} {
		name := name
		t.Run(name, func(t *testing.T) {
			ref := loadWARef(t, name)
			got := runWA(t, name)
			if got.Error != "" {
				t.Fatalf("sweef error: %s", got.Error)
			}
			switch name {
			case "biquad_response", "iir":
				for _, k := range []string{"mag", "phase"} {
					a, b := got.Extra[k].([]any), ref.Extra[k].([]any)
					for i := range b {
						if fmt.Sprint(b[i]) == "NaN" {
							if fmt.Sprint(a[i]) != "NaN" {
								t.Errorf("%s[%d] = %v want NaN", k, i, a[i])
							}
							continue
						}
						if math.Abs(a[i].(float64)-b[i].(float64)) > 1e-4 {
							t.Errorf("%s[%d] = %v want %v", k, i, a[i], b[i])
						}
					}
				}
			case "analyser":
				gf, rf := f32s(asStr(got.Extra["freq"])), f32s(asStr(ref.Extra["freq"]))
				e, at := maxAbsErr(finite(gf), finite(rf))
				t.Logf("analyser float freq max dB error %.3g at bin %d", e, at)
				if e > 0.05 {
					t.Errorf("analyser dB error %.3g", e)
				}
				gt, rt := f32s(asStr(got.Extra["time"])), f32s(asStr(ref.Extra["time"]))
				if e, _ := maxAbsErr(gt, rt); e > 1e-3 {
					t.Errorf("analyser time domain error %.3g", e)
				}
				diff := 0
				ga, ra := got.Extra["bfreq"].([]any), ref.Extra["bfreq"].([]any)
				for i := range ra {
					if math.Abs(ga[i].(float64)-ra[i].(float64)) > 1 {
						diff++
					}
				}
				if diff > 2 {
					t.Errorf("%d byte-frequency bins differ by more than 1", diff)
				}
			case "suspend_resume":
				if !reflect.DeepEqual(got.Extra["times"], ref.Extra["times"]) || got.Extra["state"] != ref.Extra["state"] {
					t.Errorf("suspend: %v vs %v", got.Extra, ref.Extra)
				}
			case "decode":
				for _, k := range []string{"len", "rate", "ch", "dur"} {
					if got.Extra[k] != ref.Extra[k] {
						t.Errorf("decode %s = %v want %v", k, got.Extra[k], ref.Extra[k])
					}
				}
			case "decode_bad":
				if !reflect.DeepEqual(got.Extra, ref.Extra) {
					t.Errorf("%v vs %v", got.Extra, ref.Extra)
				}
			case "compressor":
				g, r := got.Extra["reduction"].(float64), ref.Extra["reduction"].(float64)
				t.Logf("compressor reduction sweef %.2f dB, Chromium %.2f dB", g, r)
				if math.Abs(g-r) > 4 {
					t.Errorf("reduction %.2f vs %.2f", g, r)
				}
			case "api_shape":
				ge, re := got.Extra, ref.Extra
				for k, rv := range re {
					if k == "state0" || k == "rate" || k == "dest" {
						continue
					}
					if !reflect.DeepEqual(ge[k], rv) {
						t.Errorf("api_shape.%s = %v, Chromium %v", k, ge[k], rv)
					}
				}
			}
		})
	}
}

func asStr(v any) string { s, _ := v.(string); return s }

func finite(f []float32) []float32 {
	out := make([]float32, len(f))
	for i, v := range f {
		if math.IsInf(float64(v), 0) {
			v = -200
		}
		if v < -100 { // below the analyser's default minDecibels the exact floor is irrelevant
			v = -100
		}
		out[i] = v
	}
	return out
}

// Opus (Ogg, WebM and MP4) must decode through decodeAudioData with ffmpeg
// disabled: the native decoders run before any ffmpeg fallback.
func TestWebAudioDecodeOpusWithoutFFmpeg(t *testing.T) {
	media.SetFFmpegEnabled(false)
	defer media.SetFFmpegEnabled(true)
	cases := []struct {
		file string
		ch   float64
		dur  float64
	}{
		{"opus_celt_st.opus", 2, 1.0}, {"opus_silk_nb.opus", 1, 1.0}, {"opus_st.webm", 2, 0.6}, {"opus_st.mp4", 2, 0.6}, {"opus_51.opus", 6, 0.5},
	}
	for _, c := range cases {
		c := c
		t.Run(c.file, func(t *testing.T) {
			got := runWA(t, "decode_file&f="+c.file)
			if got.Error != "" {
				t.Fatalf("decodeAudioData failed without ffmpeg: %s", got.Error)
			}
			if got.Extra["ch"] != c.ch {
				t.Errorf("channels %v want %v", got.Extra["ch"], c.ch)
			}
			if d, _ := got.Extra["dur"].(float64); math.Abs(d-c.dur) > 0.02 {
				t.Errorf("duration %v want %v", d, c.dur)
			}
		})
	}
}
