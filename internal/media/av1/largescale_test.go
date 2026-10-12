package av1

import (
	"encoding/binary"
	"os"
	"strconv"
	"testing"
)

// ivfFrames splits an IVF file into frame payloads.
func ivfFrames(t testing.TB, data []byte) [][]byte {
	if len(data) < 32 || string(data[:4]) != "DKIF" {
		t.Fatal("not an IVF file")
	}
	var out [][]byte
	for p := int(binary.LittleEndian.Uint16(data[6:8])); p+12 <= len(data); {
		n := int(binary.LittleEndian.Uint32(data[p:]))
		p += 12
		if p+n > len(data) {
			t.Fatal("truncated IVF")
		}
		out = append(out, data[p:p+n])
		p += n
	}
	return out
}

// decodeTileListFile runs a libaom style tile list file: nref anchor frames,
// the camera frame header and then tile lists, each giving one picture.
func decodeTileListFile(t testing.TB, data []byte, nref int) []*Picture {
	frames := ivfFrames(t, data)
	d := NewDecoder()
	for i := 0; i < nref; i++ {
		if _, err := d.Decode(frames[i]); err != nil {
			t.Fatalf("anchor %d: %v", i, err)
		}
	}
	d.LargeScale = true
	if _, err := d.Decode(frames[nref]); err != nil {
		t.Fatalf("camera frame header: %v", err)
	}
	var pics []*Picture
	for _, f := range frames[nref+1:] {
		ps, err := d.Decode(f)
		if err != nil {
			t.Fatalf("tile list: %v", err)
		}
		pics = append(pics, ps...)
	}
	return pics
}

// TestLargeScaleFile decodes the tile list file $AV1_LS_IVF (with $AV1_LS_REFS
// anchor frames) and compares the output pictures with the raw raster output
// $AV1_LS_YUV of libaom's lightfield_tile_list_decoder (development helper).
func TestLargeScaleFile(t *testing.T) {
	file := os.Getenv("AV1_LS_IVF")
	if file == "" {
		t.Skip("AV1_LS_IVF not set")
	}
	data, _ := os.ReadFile(file)
	nref, _ := strconv.Atoi(os.Getenv("AV1_LS_REFS"))
	want, _ := os.ReadFile(os.Getenv("AV1_LS_YUV"))
	pics := decodeTileListFile(t, data, nref)
	pos := 0
	for i, p := range pics {
		b := picBytes(p)
		if pos+len(b) > len(want) || string(b) != string(want[pos:pos+len(b)]) {
			if pos+len(b) <= len(want) {
				// report the differing 64x64 luma tiles
				bad := map[int]int{}
				for y := 0; y < p.Height; y++ {
					for x := 0; x < p.Width; x++ {
						if b[y*p.Width+x] != want[pos+y*p.Width+x] {
							bad[(y/64)*(p.Width/64)+x/64]++
						}
					}
				}
				t.Logf("picture %d: %d tiles with luma differences: %v", i, len(bad), bad)
			}
			t.Fatalf("picture %d differs from libaom", i)
		}
		pos += len(b)
	}
	if pos != len(want) || len(pics) == 0 {
		t.Fatalf("%d pictures (%d bytes), libaom wrote %d bytes", len(pics), pos, len(want))
	}
}
