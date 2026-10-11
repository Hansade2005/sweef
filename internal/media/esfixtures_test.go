package media

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sweefbrowser/sweef/internal/media/h264"
)

// esFixtureSpecs lists the interlaced / long-term conformance streams (from
// the JVT / FFmpeg FATE suites) that are committed truncated to a few access
// units, with the number of access units kept.
var esFixtureSpecs = []struct {
	file string
	aus  int
}{
	{"Sharp_MP_PAFF_1r2.jvt", 6},     // PAFF field pictures
	{"Sharp_MP_Field_1_B.jvt", 4},    // field pictures, B
	{"CAMACI3_Sony_C.jsv", 8},        // MBAFF + constrained intra
	{"CAMA1_Sony_C.jsv", 4},          // MBAFF
	{"camp_mot_picaff0_full.26l", 4}, // PAFF + weighted prediction
	{"MR6_BT_B.h264", 8},             // field MMCO / long-term fields
	{"MR9_BT_B.h264", 10},            // MBAFF multi reference
	{"CVFC1_Sony_C.jsv", 3},          // frame cropping
}

// TestGenH264ESFixtures writes the truncated streams and ffmpeg's reference
// MD5s into SWEEF_GEN_ES, reading the full streams from SWEEF_H264_CONF.
func TestGenH264ESFixtures(t *testing.T) {
	out := os.Getenv("SWEEF_GEN_ES")
	root := os.Getenv("SWEEF_H264_CONF")
	if out == "" || root == "" {
		t.Skip()
	}
	skipNoFF(t)
	for _, sp := range esFixtureSpecs {
		es, err := os.ReadFile(filepath.Join(root, sp.file))
		if err != nil {
			t.Logf("skip %s: %v", sp.file, err)
			continue
		}
		aus := h264.SplitAccessUnits(es)
		if len(aus) > sp.aus {
			aus = aus[:sp.aus]
		}
		data := bytes.Join(aus, nil)
		base := strings.TrimSuffix(sp.file, filepath.Ext(sp.file))
		p := filepath.Join(out, "es_"+strings.ToLower(base)+".264")
		os.WriteFile(p, data, 0o644)
		cmd := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-strict", "1", "-flags", "unaligned", "-i", p, "-fps_mode", "passthrough", "-f", "rawvideo", "-")
		ref, err := cmd.Output()
		if err != nil {
			t.Fatal(err)
		}
		frames, err := decodeES(t, aus)
		if err != nil || len(frames) == 0 {
			t.Fatal(err)
		}
		fs := len(frames[0])
		if len(ref) != fs*len(frames) {
			t.Fatalf("%s: frame count mismatch", sp.file)
		}
		var sb strings.Builder
		for i := range frames {
			sum := md5.Sum(ref[i*fs : (i+1)*fs])
			sb.WriteString(hex.EncodeToString(sum[:]) + "\n")
		}
		os.WriteFile(strings.TrimSuffix(p, ".264")+".md5", []byte(sb.String()), 0o644)
	}
}

// TestH264ESFixtures decodes the committed Annex B streams without ffmpeg.
func TestH264ESFixtures(t *testing.T) {
	files, _ := filepath.Glob("testdata/fmt/es_*.264")
	if len(files) == 0 {
		t.Fatal("no fixtures")
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			es, _ := os.ReadFile(f)
			want, _ := os.ReadFile(strings.TrimSuffix(f, ".264") + ".md5")
			wl := strings.Fields(string(want))
			frames, err := decodeES(t, h264.SplitAccessUnits(es))
			if err != nil {
				t.Fatal(err)
			}
			if len(frames) != len(wl) {
				t.Fatalf("decoded %d frames, want %d", len(frames), len(wl))
			}
			for i, fr := range frames {
				sum := md5.Sum(fr)
				if hex.EncodeToString(sum[:]) != wl[i] {
					t.Fatalf("frame %d differs", i)
				}
			}
		})
	}
}
