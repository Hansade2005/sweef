package media

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/sweefbrowser/sweef/internal/media/h264"
)

// esAccessUnits splits an Annex B elementary stream into access units: a
// new unit starts at each access unit delimiter, or at a slice that starts a
// new picture (first_mb_in_slice == 0) when no delimiter precedes it.
func esAccessUnits(es []byte) [][]byte {
	type nal struct{ start, end int }
	var nals []nal
	n := len(es)
	prev := -1
	for i := 0; i+3 <= n; i++ {
		if es[i] == 0 && es[i+1] == 0 && es[i+2] == 1 {
			if prev >= 0 {
				e := i
				for e > prev && es[e-1] == 0 {
					e--
				}
				nals = append(nals, nal{prev, e})
			}
			prev = i + 3
			i += 2
		}
	}
	if prev >= 0 && prev < n {
		nals = append(nals, nal{prev, n})
	}
	var aus [][]byte
	var cur []byte
	sawVCL := false
	for _, nl := range nals {
		if nl.end <= nl.start {
			continue
		}
		typ := es[nl.start] & 31
		startNew := false
		switch {
		case typ == 9:
			startNew = len(cur) > 0
		case typ == 1 || typ == 5:
			if sawVCL && len(es[nl.start:nl.end]) > 1 && es[nl.start+1]&0x80 != 0 {
				startNew = true
			}
		case typ == 7 || typ == 8 || typ == 6:
			startNew = sawVCL
		}
		if startNew && len(cur) > 0 {
			aus = append(aus, cur)
			cur = nil
			sawVCL = false
		}
		cur = append(cur, 0, 0, 0, 1)
		cur = append(cur, es[nl.start:nl.end]...)
		if typ == 1 || typ == 5 {
			sawVCL = true
		}
	}
	if len(cur) > 0 {
		aus = append(aus, cur)
	}
	return aus
}

// decodeES decodes an Annex B stream with the pure-Go decoder and returns
// the raw frames in display order.
func decodeES(t testing.TB, aus [][]byte) ([][]byte, error) {
	d := h264.NewDecoder()
	type out struct {
		seq, poc int
		b        []byte
	}
	var outs []out
	h264.DebugPanic = func(v any, st []byte) { fmt.Printf("PANIC %v\n%s\n", v, st) }
	for i, au := range aus {
		fr, err := d.DecodeFrame(au)
		if err != nil && fr == nil {
			return nil, fmt.Errorf("AU %d: %v", i, err)
		}
		if fr != nil {
			outs = append(outs, out{d.OutSeq, d.OutPOC, vpicRaw(framePic(fr))})
		}
	}
	sort.SliceStable(outs, func(i, j int) bool {
		if outs[i].seq != outs[j].seq {
			return outs[i].seq < outs[j].seq
		}
		return outs[i].poc < outs[j].poc
	})
	var res [][]byte
	for _, o := range outs {
		res = append(res, o.b)
	}
	return res, nil
}

func compareES(t *testing.T, path string, maxAU int) (int, error) {
	t.Helper()
	es, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	aus := esAccessUnits(es)
	if maxAU > 0 && len(aus) > maxAU {
		aus = aus[:maxAU]
	}
	trunc := filepath.Join(t.TempDir(), "t.264")
	if err := os.WriteFile(trunc, bytes.Join(aus, nil), 0o644); err != nil {
		return 0, err
	}
	cmd := exec.Command("ffmpeg", "-v", "error", "-nostdin", "-strict", "1", "-i", trunc, "-fps_mode", "passthrough", "-f", "rawvideo", "-")
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	if err := cmd.Run(); err != nil {
		return 0, fmt.Errorf("SKIP ffmpeg: %v %s", err, bytes.TrimSpace(se.Bytes()))
	}
	ref := so.Bytes()
	frames, err := decodeES(t, aus)
	if err != nil {
		return 0, err
	}
	if len(frames) == 0 {
		return 0, fmt.Errorf("no frames decoded")
	}
	fs := len(frames[0])
	if len(ref)%fs != 0 || len(ref)/fs != len(frames) {
		return 0, fmt.Errorf("ffmpeg %d bytes (%.1f frames of %d) vs native %d frames", len(ref), float64(len(ref))/float64(fs), fs, len(frames))
	}
	bad := 0
	first := ""
	for i, f := range frames {
		r := ref[i*fs : (i+1)*fs]
		if !bytes.Equal(r, f) {
			if bad == 0 {
				j := 0
				for r[j] == f[j] {
					j++
				}
				first = fmt.Sprintf("frame %d first differs at byte %d (of %d): ffmpeg %d native %d", i, j, fs, r[j], f[j])
			}
			bad++
		}
	}
	if bad > 0 {
		return len(frames), fmt.Errorf("%d/%d frames differ; %s", bad, len(frames), first)
	}
	return len(frames), nil
}

// TestH264Conformance runs the pure-Go H.264 decoder over a directory of
// conformance streams (SWEEF_H264_CONF, e.g. the FFmpeg FATE h264-conformance
// suite) and compares every frame with ffmpeg's output byte for byte.
// SWEEF_CONF_FRAMES limits the access units per stream (default 20) and
// SWEEF_CONF_FILTER restricts the streams by substring.
func TestH264Conformance(t *testing.T) {
	root := os.Getenv("SWEEF_H264_CONF")
	if root == "" {
		t.Skip("SWEEF_H264_CONF not set")
	}
	skipNoFF(t)
	nau := 20
	if v, err := strconv.Atoi(os.Getenv("SWEEF_CONF_FRAMES")); err == nil && v >= 0 {
		nau = v
	}
	filter := os.Getenv("SWEEF_CONF_FILTER")
	var files []string
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			switch strings.ToLower(filepath.Ext(p)) {
			case ".264", ".jsv", ".avc", ".26l", ".bits", ".h264", ".jvt":
				if filter == "" || strings.Contains(p, filter) {
					files = append(files, p)
				}
			}
		}
		return nil
	})
	for _, f := range files {
		name := filepath.Base(f)
		t.Run(name, func(t *testing.T) {
			n, err := compareES(t, f, nau)
			switch {
			case err == nil:
				fmt.Printf("CONF PASS %s %d frames\n", name, n)
			case strings.HasPrefix(err.Error(), "SKIP"):
				fmt.Printf("CONF SKIP %s %v\n", name, err)
			default:
				fmt.Printf("CONF FAIL %s %v\n", name, err)
				t.Fail()
			}
		})
	}
}
