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
			if n > pcm.Frames() {
				n = pcm.Frames()
			}
			var sig, noise float64
			for i := 0; i < n; i++ {
				for k := 0; k < ch; k++ {
					r := float64(int16(binary.LittleEndian.Uint16(ref[(i*ch+k)*2:]))) / 32768
					d := float64(pcm.Data[k][i]) - r
					sig += r * r
					noise += d * d
				}
			}
			snr := 10 * math.Log10(sig/(noise+1e-30))
			t.Logf("%s: rate=%d ch=%d frames=%d (ref %d) SNR=%.1f dB", c.stream, pcm.SampleRate, ch, pcm.Frames(), len(ref)/2/ch, snr)
			if snr < 40 {
				t.Errorf("SNR %.1f dB below 40", snr)
			}
		})
	}
}
