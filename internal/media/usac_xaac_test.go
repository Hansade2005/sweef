package media

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Fixtures encoded with libxaac's USAC encoder (Ittiam, Apache-2.0) and
// decoded with libxaac's decoder; the .s16 files are that decoder's 16 bit
// output. Each stream is a few access units cut from a longer encode of
// music, speech or transient test signals (xaacenc -aot:42 with, per stream:
// -usac:1 -ccfl_idx:0 (fd768); -usac:2 -esbr:0 -ccfl_idx:1 (lpd_speech);
// -usac:0 -esbr:0 -ccfl_idx:1 (sw_music); -usac:0 -esbr:0 -ccfl_idx:0
// (sw768); -usac:1 -ccfl_idx:2 (sbr83); -harmonic_sbr:1 -ccfl_idx:3 and 4
// (sbr_hbe, sbr41_hbe); -pvc_enc:1 -harmonic_sbr:1 -usac:0 (pvc); -mps:1
// -ccfl_idx:3 and 4 (mps*)). xaac_tes was encoded with an encoder patched to
// select inter-TES modes, which libxaac's encoder never chooses itself.
// xaac_mps_rand carries random but valid MPS212 side information (every
// parameter coding mode); the other streams use the encoder's own choices.
var usacXaacCases = []struct {
	name string
	note string
	min  float64 // minimum SNR in dB against the libxaac output
}{
	{"xaac_fd768", "FD core, 768 sample frames, no SBR", 75},
	{"xaac_lpd_speech", "LPD only (ACELP/TCX), 24 kHz stereo", 75},
	{"xaac_sw_music", "switched FD/LPD, 24 kHz stereo", 75},
	{"xaac_sw768", "switched FD/LPD, 768 sample frames", 75},
	{"xaac_sbr83", "eSBR 8:3, mono", 75},
	{"xaac_sbr_hbe", "eSBR 2:1 with harmonic transposer, stereo", 75},
	{"xaac_sbr41_hbe", "eSBR 4:1 with harmonic transposer, stereo", 75},
	{"xaac_pvc", "eSBR with PVC, switched core, stereo", 75},
	{"xaac_tes", "eSBR with inter-TES, mono", 75},
	{"xaac_mps", "MPS212 (stereoConfigIndex 2) over eSBR 2:1, FD core", 75},
	{"xaac_mps_rand", "MPS212 with random side information (all coding modes: PCM, 1-D/2-D, time/frequency pairs, coarse and fine, band strides)", 75},
	{"xaac_mps_lpd", "MPS212 over eSBR 2:1, LPD core", 75},
	{"xaac_mps41", "MPS212 over eSBR 4:1", 75},
}

// usacSNR compares planar PCM with 16 bit interleaved reference samples after
// the first skip frames. libxaac converts its float output to 16 bit by
// truncating towards zero (this decoder keeps floats), which the comparison
// reproduces; it also wraps on integer overflow where this decoder
// saturates, so samples at or next to the 16 bit limits are left out.
func usacSNR(pcm *PCM, ref []byte, skip int) float64 {
	ch := len(pcm.Data)
	n := len(ref) / 2 / ch
	if n > pcm.Frames() {
		n = pcm.Frames()
	}
	var sig, noise float64
	for i := skip; i < n; i++ {
		for k := 0; k < ch; k++ {
			r := float64(int16(binary.LittleEndian.Uint16(ref[(i*ch+k)*2:])))
			v := math.Trunc(float64(pcm.Data[k][i]) * 32768)
			if v > 32765 || v < -32766 || r > 32765 || r < -32766 {
				continue
			}
			sig += r * r
			noise += (v - r) * (v - r)
		}
	}
	return 10 * math.Log10(sig/(noise+1e-9))
}

func TestUSACXaacFixtures(t *testing.T) {
	SetFFmpegEnabled(false)
	defer SetFFmpegEnabled(true)
	// libxaac decodes the AudioPreRoll frames of a stream's first access
	// unit; the output then carries their overlap
	usacPrimePreroll = true
	defer func() { usacPrimePreroll = false }()
	for _, c := range usacXaacCases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "usac", c.name+".m4a"))
			if err != nil {
				t.Skip(err)
			}
			ref, err := os.ReadFile(filepath.Join("testdata", "usac", c.name+".s16"))
			if err != nil {
				t.Skip(err)
			}
			info, err := ProbeBytes(data)
			if err != nil {
				t.Fatal(err)
			}
			if ok, why := Playable(info); !ok {
				t.Fatalf("not playable without ffmpeg: %s", why)
			}
			pcm, err := DecodeAudio(data)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			snr := usacSNR(pcm, ref, 4096)
			t.Logf("%s: rate=%d ch=%d frames=%d (ref %d) SNR=%.1f dB", c.note, pcm.SampleRate, len(pcm.Data), pcm.Frames(), len(ref)/2/len(pcm.Data), snr)
			if snr < c.min {
				t.Errorf("SNR %.1f dB below %.0f", snr, c.min)
			}
		})
	}
}

// patchUSACConfig flips the bits at the given positions of the AudioSpecificConfig of an
// m4a file written by the fixture generator and returns the patched file.
func patchUSACConfig(t *testing.T, data []byte, bits ...int) []byte {
	t.Helper()
	i := bytes.Index(data, []byte{0x05, 0x80, 0x80, 0x80, 0x19})
	if i < 0 {
		t.Fatal("AudioSpecificConfig not found")
	}
	out := append([]byte(nil), data...)
	for _, b := range bits {
		out[i+5+b/8] ^= 0x80 >> uint(b%8)
	}
	return out
}

// TestUSACConfigSupport checks the open-time detection of tools the native
// decoder lacks.
func TestUSACConfigSupport(t *testing.T) {
	SetFFmpegEnabled(false)
	defer SetFFmpegEnabled(true)
	data, err := os.ReadFile("testdata/usac/xaac_mps.m4a")
	if err != nil {
		t.Skip(err)
	}
	info, err := ProbeBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if ok, why := Playable(info); !ok {
		t.Fatalf("unmodified stream: %s", why)
	}
	// the MPS212 element configuration starts at bit 67: freqRes(3) fixedGain(3)
	// tempShapeConfig(2) decorrConfig(2) highRateMode phaseCoding
	// ottBandsPhasePresent residualBands(5) pseudoLR
	for _, c := range []struct {
		name string
		bit  int
	}{
		{"residual bands", 84},
		{"phase coding", 78},
	} {
		info, err := ProbeBytes(patchUSACConfig(t, data, c.bit))
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if ok, _ := Playable(info); ok {
			t.Errorf("%s: playable without ffmpeg", c.name)
		}
		if nativeUSAC(info.Audio()) {
			t.Errorf("%s: reported as native", c.name)
		}
	}
}

func TestUSACChannelOrder(t *testing.T) {
	for idx := 1; idx <= 14; idx++ {
		n, _, ok := usacChannelsForConfig(idx)
		if !ok {
			continue
		}
		o, ok := usacChanOrder(idx)
		if !ok {
			t.Errorf("config %d: no channel order", idx)
			continue
		}
		seen := map[int]bool{}
		for _, v := range o {
			if v < 0 || v >= n || seen[v] {
				t.Errorf("config %d: invalid order %v", idx, o)
			}
			seen[v] = true
		}
	}
	// FFmpeg native order FL FR FC LFE BL BR (BC | SL SR | TFL TFR)
	for idx, want := range map[int][]int{
		11: {2, 0, 1, 4, 5, 6, 3},
		12: {2, 0, 1, 6, 7, 4, 5, 3},
		14: {2, 0, 1, 4, 5, 3, 6, 7},
	} {
		got, _ := usacChanOrder(idx)
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("config %d: order %v, want %v", idx, got, want)
		}
	}
}

func TestUSACCanPlayType(t *testing.T) {
	SetFFmpegEnabled(false)
	defer SetFFmpegEnabled(true)
	if FFmpegPath() != "" {
		t.Skip("ffmpeg available")
	}
	// some xHE-AAC streams need tools only ffmpeg has: never a plain "probably"
	if got := CanPlayType(`audio/mp4; codecs="mp4a.40.42"`); got != "maybe" {
		t.Errorf("mp4a.40.42: %q, want maybe", got)
	}
	if got := CanPlayType(`video/webm; codecs="mp4a.40.42"`); got != "" {
		t.Errorf("webm with mp4a.40.42: %q, want empty", got)
	}
}

// TestUSACCorruptStreams flips bits in the fixtures: the decoder may fail or
// output garbage but must neither panic nor hang.
func TestUSACCorruptStreams(t *testing.T) {
	SetFFmpegEnabled(false)
	defer SetFFmpegEnabled(true)
	for _, c := range usacXaacCases {
		data, err := os.ReadFile(filepath.Join("testdata", "usac", c.name+".m4a"))
		if err != nil {
			t.Skip(err)
		}
		rng := rand.New(rand.NewSource(7))
		for it := 0; it < 12; it++ {
			d := append([]byte(nil), data...)
			for k := 0; k < 1+rng.Intn(6); k++ {
				d[len(d)/3+rng.Intn(len(d)*2/3)] ^= byte(1 << uint(rng.Intn(8)))
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				DecodeAudio(d)
			}()
			select {
			case <-done:
			case <-time.After(30 * time.Second):
				t.Fatalf("%s: decoder hangs on corrupt input (iteration %d)", c.name, it)
			}
		}
	}
}
