package media

import (
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// nativeData loads a fixture from testdata/ (small, committed).
func nativeData(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// snrDB is the signal-to-noise ratio of got against ref over all channels.
func snrDB(got, ref *PCM) (float64, int) {
	var sig, noise float64
	n := got.Frames()
	if ref.Frames() < n {
		n = ref.Frames()
	}
	for c := range ref.Data {
		if c >= len(got.Data) {
			break
		}
		for i := 0; i < n; i++ {
			r := float64(ref.Data[c][i])
			d := float64(got.Data[c][i]) - r
			sig += r * r
			noise += d * d
		}
	}
	if noise == 0 {
		return 200, n
	}
	return 10 * math.Log10(sig/noise), n
}

func TestOpusMatchesFFmpeg(t *testing.T) {
	if FFmpegPath() == "" {
		t.Skip("no ffmpeg for the reference decode")
	}
	files := []string{"opus_silk_nb.opus", "opus_silk_wb60.opus", "opus_hybrid.opus", "opus_celt_st.opus",
		"opus_celt_2p5.opus", "opus_noise.opus", "opus_40ms.opus", "opus_st.webm", "opus_st.mp4", "opus_51.opus"}
	for _, f := range files {
		data := nativeData(t, f)
		info, err := ProbeBytes(data)
		if err != nil {
			t.Fatal(f, err)
		}
		if !NativeAudio(info) {
			t.Errorf("%s not native", f)
			continue
		}
		start := time.Now()
		got, err := DecodeAudio(data)
		el := time.Since(start)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		ch := len(got.Data)
		ref := libopusRef(t, data, ch)
		snr, n := snrDB(got, ref)
		t.Logf("%-22s ch=%d frames=%d (ffmpeg %d) SNR=%.1f dB  %.0fx realtime", f, ch, got.Frames(), ref.Frames(), snr, got.Duration()/el.Seconds())
		if d := got.Frames() - ref.Frames(); d < -960 || d > 960 {
			t.Errorf("%s: length %d vs ffmpeg %d", f, got.Frames(), ref.Frames())
		}
		if snr < 40 {
			t.Errorf("%s: SNR %.1f dB < 40 (compared %d frames)", f, snr, n)
		}
	}
}

// libopusRef decodes with ffmpeg's libopus wrapper (the bit-exact reference).
func libopusRef(t *testing.T, data []byte, ch int) *PCM {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "in.bin")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(FFmpegPath(), "-v", "error", "-nostdin", "-c:a", "libopus", "-i", tmp, "-map", "0:a:0", "-f", "f32le", "-ac", strconv.Itoa(ch), "-ar", "48000", "pipe:1").Output()
	if err != nil {
		t.Fatal(err)
	}
	n := len(out) / (4 * ch)
	p := &PCM{SampleRate: 48000, Data: make([][]float32, ch)}
	for c := range p.Data {
		p.Data[c] = make([]float32, n)
		for i := 0; i < n; i++ {
			p.Data[c][i] = math.Float32frombits(binary.LittleEndian.Uint32(out[(i*ch+c)*4:]))
		}
	}
	return p
}

func TestOpusNativeWithoutFFmpeg(t *testing.T) {
	SetFFmpegEnabled(false)
	defer SetFFmpegEnabled(true)
	for _, f := range []string{"opus_celt_st.opus", "opus_st.webm", "opus_st.mp4"} {
		p, err := DecodeAudio(nativeData(t, f))
		if err != nil || p.Frames() < 20000 || len(p.Data) != 2 {
			t.Errorf("%s: %v %v", f, err, p)
		}
	}
	if CanPlayType(`audio/ogg; codecs="opus"`) != "probably" || CanPlayType(`audio/webm; codecs="opus"`) != "probably" {
		t.Error("opus canPlayType must not need ffmpeg")
	}
}

func TestAACMatchesFFmpeg(t *testing.T) {
	if FFmpegPath() == "" {
		t.Skip("no ffmpeg for the reference decode")
	}
	files := []string{"aac_st_ms.m4a", "aac_transient.m4a", "aac_noise_pns.m4a", "aac_lowrate.aac", "aac_st_hi.aac",
		"aac_51.m4a", "aac_speech16k.m4a", "aac_frag.mp4"}
	for _, f := range files {
		data := nativeData(t, f)
		info, err := ProbeBytes(data)
		if err != nil {
			t.Fatal(f, err)
		}
		if !NativeAudio(info) {
			t.Errorf("%s not native", f)
			continue
		}
		start := time.Now()
		got, err := DecodeAudio(data)
		el := time.Since(start)
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		ref, err := DecodeAudioFFmpeg(data, got.SampleRate, len(got.Data))
		if err != nil {
			t.Fatal(err)
		}
		snr, _ := snrDB(got, ref)
		t.Logf("%-20s rate=%d ch=%d frames=%d (ffmpeg %d) SNR=%.1f dB %.0fx realtime stats=%+v", f, got.SampleRate, len(got.Data), got.Frames(), ref.Frames(), snr, got.Duration()/el.Seconds(), lastAACStats)
		if got.Frames() != ref.Frames() {
			t.Errorf("%s: length %d vs ffmpeg %d", f, got.Frames(), ref.Frames())
		}
		if snr < 60 {
			t.Errorf("%s: SNR %.1f dB < 60", f, snr)
		}
	}
}

func TestAACSampleRates(t *testing.T) {
	if FFmpegPath() == "" {
		t.Skip("no ffmpeg")
	}
	for _, r := range []int{96000, 64000, 48000, 32000, 24000, 22050, 16000, 12000, 11025, 8000} {
		tmp := t.TempDir()
		in := filepath.Join(tmp, "in.m4a")
		src := "aevalsrc='0.3*sin(2*PI*(200+2500*t)*t)+0.1*sin(2*PI*3100*t)':s=" + strconv.Itoa(r) + ":d=0.5"
		if err := exec.Command(FFmpegPath(), "-v", "error", "-y", "-nostdin", "-f", "lavfi", "-i", src, "-c:a", "aac", "-b:a", "64k", in).Run(); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(in)
		got, err := DecodeAudio(data)
		if err != nil {
			t.Errorf("%d: %v", r, err)
			continue
		}
		ref, _ := DecodeAudioFFmpeg(data, got.SampleRate, 1)
		if snr, _ := snrDB(got, ref); snr < 60 || got.SampleRate != r {
			t.Errorf("rate %d: SNR %.1f (rate %d)", r, snr, got.SampleRate)
		}
	}
}

func TestAACNativeWithoutFFmpeg(t *testing.T) {
	SetFFmpegEnabled(false)
	defer SetFFmpegEnabled(true)
	for _, f := range []string{"aac_st_ms.m4a", "aac_lowrate.aac", "aac_51.m4a"} {
		p, err := DecodeAudio(nativeData(t, f))
		if err != nil || p.Frames() < 20000 {
			t.Errorf("%s: %v", f, err)
		}
	}
	for mime, want := range map[string]string{
		`audio/mp4; codecs="mp4a.40.2"`: "probably", `audio/aac`: "maybe", `audio/aac; codecs="mp4a.40.2"`: "probably",
		`audio/mp4; codecs="mp4a.40.5"`: "", `video/mp4; codecs="avc1.64001e, mp4a.40.2"`: CanPlayType(`video/mp4; codecs="avc1.64001e, mp4a.40.2"`),
	} {
		if got := CanPlayType(mime); got != want {
			t.Errorf("canPlayType(%s) = %q want %q", mime, got, want)
		}
	}
	if !IsTypeSupportedMSE(`audio/mp4; codecs="mp4a.40.2"`) {
		t.Error("MSE isTypeSupported for AAC-LC audio/mp4")
	}
}
