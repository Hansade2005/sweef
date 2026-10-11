package av1

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// picBytes serialises a picture like ffmpeg's rawvideo output (yuv420p,
// yuv420p10le, gray, ...): planar, 8 bits per sample or 16-bit little endian.
func picBytes(p *Picture) []byte {
	var out []byte
	n := 3
	if p.Mono {
		n = 1
	}
	for pl := 0; pl < n; pl++ {
		w, h := p.Width, p.Height
		if pl > 0 {
			w = (w + p.SubX) >> uint(p.SubX)
			h = (h + p.SubY) >> uint(p.SubY)
		}
		for y := 0; y < h; y++ {
			row := p.Planes[pl][y*p.Stride[pl] : y*p.Stride[pl]+w]
			for _, v := range row {
				if p.BitDepth == 8 {
					out = append(out, byte(v))
				} else {
					out = append(out, byte(v), byte(v>>8))
				}
			}
		}
	}
	return out
}

// splitTemporalUnits splits a low-overhead OBU stream at temporal delimiters.
func splitTemporalUnits(data []byte) [][]byte {
	var tus [][]byte
	start := 0
	pos := 0
	for pos < len(data) {
		b := &bitReader{buf: data[pos:]}
		b.f(1)
		typ := b.f(4)
		ext := b.flag()
		hasSize := b.flag()
		b.f(1)
		if ext {
			b.f(8)
		}
		if !hasSize {
			break
		}
		size := b.leb128()
		n := b.pos/8 + size
		if typ == obuTemporalDelimiter && pos > start {
			tus = append(tus, data[start:pos])
			start = pos
		}
		pos += n
	}
	if start < len(data) {
		tus = append(tus, data[start:])
	}
	return tus
}

func fixtureList(t *testing.T) []string {
	files, _ := filepath.Glob("../testdata/av1_*.obu")
	if len(files) == 0 {
		t.Fatal("no AV1 fixtures in ../testdata")
	}
	return files
}

func md5s(pics []*Picture) []string {
	var out []string
	for _, p := range pics {
		s := md5.Sum(picBytes(p))
		out = append(out, hex.EncodeToString(s[:]))
	}
	return out
}

// TestFixtures decodes every committed clip and compares each output frame
// with the MD5 of the frame libdav1d produced (the .md5 files were written
// from ffmpeg -c:v libdav1d rawvideo output). The clips are decoded both as a
// whole and one temporal unit at a time.
func TestFixtures(t *testing.T) {
	resetCoverage()
	for _, f := range fixtureList(t) {
		f := f
		name := strings.TrimSuffix(filepath.Base(f), ".obu")
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			wantRaw, err := os.ReadFile(strings.TrimSuffix(f, ".obu") + ".md5")
			if err != nil {
				t.Fatal(err)
			}
			want := strings.Fields(string(wantRaw))
			pics, err := NewDecoder().Decode(data)
			if err != nil {
				t.Fatal(err)
			}
			got := md5s(pics)
			if len(got) != len(want) {
				t.Fatalf("decoded %d frames, reference has %d", len(got), len(want))
			}
			for i := range got {
				if got[i] != want[i] {
					t.Fatalf("frame %d: md5 %s want %s", i, got[i], want[i])
				}
			}
			d := NewDecoder()
			var tuPics []*Picture
			for _, tu := range splitTemporalUnits(data) {
				ps, err := d.Decode(tu)
				if err != nil {
					t.Fatal(err)
				}
				tuPics = append(tuPics, ps...)
			}
			got2 := md5s(tuPics)
			if fmt.Sprint(got2) != fmt.Sprint(want) {
				t.Fatalf("temporal-unit-wise decoding differs from whole-stream decoding")
			}
		})
	}
}

// TestFixtureCoverage checks that, between them, the fixtures exercise the
// coding tools the decoder claims to implement.
func TestFixtureCoverage(t *testing.T) {
	resetCoverage()
	for _, f := range fixtureList(t) {
		data, _ := os.ReadFile(f)
		NewDecoder().Decode(data)
	}
	need := []int{covPalette, covPaletteUV, covIntrabc, covCfl, covFilterIntra, covDirectional, covEdgeUpsample,
		covSmooth, covPaeth, covOBMC, covLocalWarp, covGlobalWarp, covWedge, covDiffwtd, covDistance, covInterintra,
		covCompoundAvg, covScaledRef, covSkipMode, covSegmentation, covDeltaQ, covDeltaLF, covQmatrix, covTx64,
		covIdtx, covLRWiener, covLRSgr, covSuperres, covFilmGrain, covCdef, covLoopFilter, covMfmv, covTiles,
		covInterpDual, covLossless, covIntraEdgeFilter, covRefMvTemporal,
		covNonUniformTiles, covShortSignaling, covSwitchFrame, covIntraOnly, covErrorRes, covSpatialLayers, covTemporalLayers}
	for _, c := range need {
		if coverage[c] == 0 {
			t.Errorf("no fixture exercises %s", covNames[c])
		}
	}
}

// TestCorruptStreams flips bits in real streams: the decoder must return
// (possibly with an error) and never panic or hang.
func TestCorruptStreams(t *testing.T) {
	for _, name := range []string{"av1_lossless", "av1_still", "av1_odd"} {
		data, err := os.ReadFile("../testdata/" + name + ".obu")
		if err != nil {
			t.Fatal(err)
		}
		rnd := uint32(12345)
		next := func() uint32 { rnd = rnd*1664525 + 1013904223; return rnd >> 8 }
		for it := 0; it < 40; it++ {
			m := append([]byte(nil), data...)
			for k := 0; k < 1+int(next()%6); k++ {
				lim := len(m)
				if it%2 == 0 && lim > 80 {
					lim = 80 // headers
				}
				m[int(next())%lim] ^= byte(1 << (next() % 8))
			}
			NewDecoder().Decode(m)
		}
		// truncations
		for cut := 1; cut < len(data); cut += 1 + len(data)/25 {
			NewDecoder().Decode(data[:cut])
		}
	}
}

// TestFileCompare decodes $AV1_FILE and compares with the raw output in
// $AV1_YUV (development helper, skipped by default).
func TestFileCompare(t *testing.T) {
	file := os.Getenv("AV1_FILE")
	if file == "" {
		t.Skip("AV1_FILE not set")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	resetCoverage()
	dec := NewDecoder()
	if v, e := strconv.Atoi(os.Getenv("AV1_OP")); e == nil {
		dec.OperatingPoint = v
	}
	dec.AllLayers = os.Getenv("AV1_ALL") == "1"
	var pics []*Picture
	if tus := os.Getenv("AV1_TU"); tus == "1" {
		for _, tu := range splitTemporalUnits(data) {
			ps, e := dec.Decode(tu)
			if e != nil {
				err = e
			}
			pics = append(pics, ps...)
		}
	} else {
		pics, err = dec.Decode(data)
	}
	if err != nil {
		t.Logf("decode error after %d pictures: %v", len(pics), err)
	}
	yuv, _ := os.ReadFile(os.Getenv("AV1_YUV"))
	pos, bad := 0, 0
	for i, p := range pics {
		b := picBytes(p)
		if pos+len(b) > len(yuv) {
			t.Logf("frame %d: reference too short", i)
			break
		}
		ref := yuv[pos : pos+len(b)]
		pos += len(b)
		diff, first := 0, -1
		for j := range b {
			if b[j] != ref[j] {
				diff++
				if first < 0 {
					first = j
				}
			}
		}
		if diff != 0 {
			bad++
			t.Logf("frame %d (%dx%d): %d/%d bytes differ, first at byte %d", i, p.Width, p.Height, diff, len(b), first)
		}
	}
	t.Logf("%d pictures, %d mismatching", len(pics), bad)
	var cv string
	for i, n := range covNames {
		if coverage[i] > 0 {
			cv += fmt.Sprintf(" %s=%d", n, coverage[i])
		}
	}
	t.Logf("coverage:%s", cv)
	if bad != 0 || err != nil {
		t.Fail()
	}
}

func BenchmarkDecode(b *testing.B) {
	file := os.Getenv("AV1_FILE")
	if file == "" {
		file = "../testdata/av1_tools.obu"
	}
	data, err := os.ReadFile(file)
	if err != nil {
		b.Skip(err)
	}
	frames := 0
	for i := 0; i < b.N; i++ {
		pics, err := NewDecoder().Decode(data)
		if err != nil {
			b.Fatal(err)
		}
		frames += len(pics)
	}
	b.ReportMetric(float64(frames)/b.Elapsed().Seconds(), "fps")
}

func lebBytes(v int) []byte {
	var out []byte
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			out = append(out, c|0x80)
		} else {
			return append(out, c)
		}
	}
}

// toAnnexB rewrites a low-overhead stream as Annex B temporal units. With
// keepSize the OBUs keep their obu_size fields (allowed by the format),
// otherwise they are stripped.
func toAnnexB(data []byte, keepSize bool) []byte {
	var out []byte
	for _, tu := range splitTemporalUnits(data) {
		var units [][]byte // frame units
		var cur []byte
		frames := 0
		for pos := 0; pos < len(tu); {
			b := &bitReader{buf: tu[pos:]}
			b.f(1)
			typ := b.f(4)
			ext := b.flag()
			b.flag()
			b.f(1)
			var extb byte
			if ext {
				extb = byte(b.f(8))
			}
			size := b.leb128()
			hdrLen := b.pos / 8
			obu := tu[pos : pos+hdrLen+size]
			if !keepSize {
				h := []byte{tu[pos] &^ 2}
				if ext {
					h = append(h, extb)
				}
				obu = append(h, tu[pos+hdrLen:pos+hdrLen+size]...)
			}
			if (typ == obuFrame || typ == obuFrameHeader) && frames > 0 {
				units = append(units, cur)
				cur = nil
			}
			if typ == obuFrame || typ == obuFrameHeader {
				frames++
			}
			cur = append(cur, lebBytes(len(obu))...)
			cur = append(cur, obu...)
			pos += hdrLen + size
		}
		units = append(units, cur)
		var body []byte
		for _, u := range units {
			body = append(body, lebBytes(len(u))...)
			body = append(body, u...)
		}
		out = append(out, lebBytes(len(body))...)
		out = append(out, body...)
	}
	return out
}

// TestAnnexB decodes every fixture rewritten in the Annex B format (with and
// without obu_size fields) and requires the same pictures.
func TestAnnexB(t *testing.T) {
	for _, f := range fixtureList(t) {
		name := strings.TrimSuffix(filepath.Base(f), ".obu")
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > 30000 {
			continue // keep the test short
		}
		want, err := NewDecoder().Decode(data)
		if err != nil {
			t.Fatal(name, err)
		}
		for _, keep := range []bool{false, true} {
			ab := toAnnexB(data, keep)
			if !IsAnnexB(ab) {
				t.Fatalf("%s keep=%v: not detected as Annex B", name, keep)
			}
			if !keep {
				got, err := NewDecoder().Decode(ab)
				if err != nil {
					t.Fatalf("%s: %v", name, err)
				}
				if fmt.Sprint(md5s(got)) != fmt.Sprint(md5s(want)) {
					t.Fatalf("%s: pictures differ", name)
				}
				continue
			}
			// one temporal unit per call, OBUs keeping their obu_size
			d := NewDecoder()
			var all []*Picture
			for p := 0; p < len(ab); {
				sz, n, _ := lebAt(ab, p)
				ps, err := d.Decode(ab[p : p+n+sz])
				if err != nil {
					t.Fatal(err)
				}
				all = append(all, ps...)
				p += n + sz
			}
			if fmt.Sprint(md5s(all)) != fmt.Sprint(md5s(want)) {
				t.Fatalf("%s: per-temporal-unit decoding differs", name)
			}
		}
		if IsAnnexB(data) {
			t.Fatalf("%s: section 5 stream detected as Annex B", name)
		}
	}
}

// decodeMD5File decodes data with dec and compares the pictures with the md5
// list in file.
func checkMD5(t *testing.T, dec *Decoder, data []byte, md5file string) {
	t.Helper()
	wantRaw, err := os.ReadFile(md5file)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Fields(string(wantRaw))
	pics, err := dec.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	got := md5s(pics)
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%d pictures, reference has %d (or content differs): %v vs %v", len(got), len(want), got, want)
	}
}

// TestLayers decodes the scalable fixtures with the layer options named by
// the md5 file suffix: .all.md5 outputs every spatial layer, .opN.md5 selects
// operating point N (the references are libdav1d and libaom output).
func TestLayers(t *testing.T) {
	files, _ := filepath.Glob("../testdata/av1_svc_*.*.md5")
	if len(files) == 0 {
		t.Fatal("no layer fixtures")
	}
	for _, f := range files {
		base := filepath.Base(f)
		parts := strings.Split(strings.TrimSuffix(base, ".md5"), ".")
		data, err := os.ReadFile("../testdata/" + parts[0] + ".obu")
		if err != nil {
			t.Fatal(err)
		}
		dec := NewDecoder()
		switch {
		case parts[1] == "all":
			dec.AllLayers = true
		case strings.HasPrefix(parts[1], "op"):
			dec.OperatingPoint, _ = strconv.Atoi(parts[1][2:])
		}
		t.Run(base, func(t *testing.T) { checkMD5(t, dec, data, f) })
	}
}

// TestAnnexBFixture decodes a stream written by aomenc --annexb=1 (libdav1d
// reads it with its annexb demuxer and gives the same pictures).
func TestAnnexBFixture(t *testing.T) {
	data, err := os.ReadFile("../testdata/avb_annexb.obu")
	if err != nil {
		t.Fatal(err)
	}
	if !IsAnnexB(data) {
		t.Fatal("not detected as Annex B")
	}
	checkMD5(t, NewDecoder(), data, "../testdata/avb_annexb.md5")
	// temporal unit by temporal unit
	d := NewDecoder()
	var all []*Picture
	for p := 0; p < len(data); {
		sz, n, _ := lebAt(data, p)
		ps, err := d.Decode(data[p : p+n+sz])
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, ps...)
		p += n + sz
	}
	want, _ := os.ReadFile("../testdata/avb_annexb.md5")
	if fmt.Sprint(md5s(all)) != fmt.Sprint(strings.Fields(string(want))) {
		t.Fatal("per temporal unit decoding differs")
	}
}

// TestLargeScaleTile decodes a libaom lightfield tile list file (4 anchor
// frames, the camera frame header and 3 tile lists of tiles taken from 16
// views) and compares the assembled pictures with the output of libaom's
// lightfield_tile_list_decoder. libdav1d does not implement large scale
// tile decoding.
func TestLargeScaleTile(t *testing.T) {
	data, err := os.ReadFile("../testdata/av1ls_tiles.ivf")
	if err != nil {
		t.Fatal(err)
	}
	wantRaw, _ := os.ReadFile("../testdata/av1ls_tiles.md5")
	want := strings.Fields(string(wantRaw))
	got := md5s(decodeTileListFile(t, data, 4))
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", got, want)
	}
}
