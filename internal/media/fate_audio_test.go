package media

import (
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// TestFateAudioConformance decodes MPEG conformance streams (HE-AAC and USAC,
// from FFmpeg's FATE sample suite) with the native decoders and compares them
// with the reference PCM that ships with the streams. Set SWEEF_FATE_DIR to a
// directory laid out as heaac/ and usac/ (see testdata/fate/README.md).
func TestFateAudioConformance(t *testing.T) {
	dir := os.Getenv("SWEEF_FATE_DIR")
	if dir == "" {
		t.Skip("SWEEF_FATE_DIR not set")
	}
	SetFFmpegEnabled(false)
	defer SetFFmpegEnabled(true)
	cases := []struct{ stream, ref string }{
		{"heaac/al_sbr_cm_48_2.mp4", "heaac/al_sbr_hq_cm_48_2.s16"},
		{"heaac/al_sbr_cm_48_5.1.mp4", "heaac/al_sbr_hq_cm_48_5.1.s16"},
		{"heaac/al_sbr_ps_04_new.mp4", "heaac/al_sbr_ps_04_ur.s16"},
		{"heaac/al_sbr_ps_06_new.mp4", "heaac/al_sbr_ps_06_ur.s16"},
		{"heaac/al_sbr_sr_48_2_fsaac48.mp4", "heaac/al_sbr_hq_sr_48_2_fsaac48.s16"},
	}
	for _, c := range cases {
		c := c
		t.Run(filepath.Base(c.stream), func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, c.stream))
			if err != nil {
				t.Skip(err)
			}
			ref, err := os.ReadFile(filepath.Join(dir, c.ref))
			if err != nil {
				t.Skip(err)
			}
			pcm, err := DecodeAudio(data)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			ch := len(pcm.Data)
			n := len(ref) / 2 / ch
			if n != pcm.Frames() {
				t.Errorf("length %d frames, reference %d", pcm.Frames(), n)
			}
			if n > pcm.Frames() {
				n = pcm.Frames()
			}
			refAt := func(i, k int) float64 {
				return float64(int16(binary.LittleEndian.Uint16(ref[(i*ch+k)*2:]))) / 32768
			}
			// The reference decoders do not agree on the channel order of PCE
			// layouts, so pair every decoded channel with the reference channel
			// it is closest to (one to one) and report the pairing.
			perm := make([]int, ch)
			used := make([]bool, ch)
			for k := 0; k < ch; k++ {
				if ch <= 2 { // stereo and mono have a fixed order
					perm[k], used[k] = k, true
					continue
				}
				best, bi := math.Inf(1), -1
				for j := 0; j < ch; j++ {
					if used[j] {
						continue
					}
					var e float64
					for i := 0; i < n && i < 200000; i++ {
						d := float64(pcm.Data[k][i]) - refAt(i, j)
						e += d * d
					}
					if e < best {
						best, bi = e, j
					}
				}
				perm[k] = bi
				used[bi] = true
			}
			var sig, noise float64
			for i := 0; i < n; i++ {
				for k := 0; k < ch; k++ {
					r := refAt(i, perm[k])
					d := float64(pcm.Data[k][i]) - r
					sig += r * r
					noise += d * d
				}
			}
			snr := 10 * math.Log10(sig/(noise+1e-30))
			t.Logf("%s: rate=%d ch=%d frames=%d (ref %d) SNR=%.1f dB channel map %v", c.stream, pcm.SampleRate, ch, pcm.Frames(), len(ref)/2/ch, snr, perm)
			if snr < 60 {
				t.Errorf("SNR %.1f dB below 60", snr)
			}
		})
	}
}

// usacCase is a USAC conformance stream with its reference PCM.
type usacCase struct {
	stream, ref string
	target      float64 // loudness normalisation target (0: none)
}

var usacCases = []usacCase{
	{"Fd_1_c1_0x03.mp4", "Fd_1_c1_0x03.s16", 0},
	{"Fd_1_c1_0x04.mp4", "Fd_1_c1_0x04.s16", 0},
	{"Fd_2_c1_0x03.mp4", "Fd_2_c1_0x03.s16", 0},
	{"Fd_2_c1_0x05.mp4", "Fd_2_c1_0x05.s16", 0},
	{"Fd_2_c1_Tns_0x04.mp4", "Fd_2_c1_Tns_0x04.s16", 0},
	{"Fd_2_c1_Ms_0x01.mp4", "Fd_2_c1_Ms_0x01.s16", 0},
	{"Fd_2_c1_Ms_0x04.mp4", "Fd_2_c1_Ms_0x04.s16", 0},
	{"xhe_target_level.m4a", "xhe_target_level.s16", -24},
	{"Ext_2_c1_Ln_0x03.mp4", "Ext_2_c1_Ln_0x03__Lou-16.s16", -16},
	{"Ext_2_c1_Ln_0x03.mp4", "Ext_2_c1_Ln_0x03__Lou-24.s16", -24},
	{"Ext_2_c1_Ln_0x03.mp4", "Ext_2_c1_Ln_0x03__Lou-31.s16", -31},
}

func checkUSAC(t *testing.T, data, ref []byte, target float64) {
	SetFFmpegEnabled(false)
	defer SetFFmpegEnabled(true)
	SetUSACTargetLoudness(target)
	defer SetUSACTargetLoudness(0)
	pcm, err := DecodeAudio(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	ch := len(pcm.Data)
	n := len(ref) / 2 / ch
	if n > pcm.Frames() {
		n = pcm.Frames()
	}
	var sig, noise float64
	for i := 0; i < n; i++ {
		for k := 0; k < ch; k++ {
			r := float64(int16(binary.LittleEndian.Uint16(ref[(i*ch+k)*2:])))
			v := float64(pcm.Data[k][i]) * 32768
			v = math.Max(-32768, math.Min(32767, v))
			sig += r * r
			noise += (v - r) * (v - r)
		}
	}
	snr := 10 * math.Log10(sig/(noise+1e-9))
	t.Logf("rate=%d ch=%d frames=%d (ref %d) SNR=%.1f dB", pcm.SampleRate, ch, pcm.Frames(), len(ref)/2/ch, snr)
	if snr < 60 {
		t.Errorf("SNR %.1f dB below 60", snr)
	}
}

// TestFateUSAC decodes the USAC conformance streams of FFmpeg's FATE suite
// (SWEEF_FATE_DIR/usac/) and compares them with the reference PCM.
func TestFateUSAC(t *testing.T) {
	dir := os.Getenv("SWEEF_FATE_DIR")
	if dir == "" {
		t.Skip("SWEEF_FATE_DIR not set")
	}
	for _, c := range usacCases {
		c := c
		t.Run(c.ref, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, "usac", c.stream))
			if err != nil {
				t.Skip(err)
			}
			ref, err := os.ReadFile(filepath.Join(dir, "usac", c.ref))
			if err != nil {
				t.Skip(err)
			}
			checkUSAC(t, data, ref, c.target)
		})
	}
}

// TestUSACXheTargetLevel runs the small xHE-AAC stream shipped in testdata.
func TestUSACXheTargetLevel(t *testing.T) {
	data, err := os.ReadFile("testdata/usac/xhe_target_level.m4a")
	if err != nil {
		t.Skip(err)
	}
	ref, _ := os.ReadFile("testdata/usac/xhe_target_level.s16")
	checkUSAC(t, data, ref, -24)
}
