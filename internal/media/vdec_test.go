package media

import "testing"

// A late player skips H.264 access units nothing refers to. Skipping one that is a reference would corrupt
// every picture after it, so the check must be exact: decoding all but the disposable units must give the
// same pictures for the units that were decoded in both runs.
func TestH264DisposableFramesCanBeSkipped(t *testing.T) {
	b := load(t, "h264_wave_320x192.mp4")
	info, err := ProbeBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	v, _, err := OpenTracks(BytesSource(b), info)
	if err != nil || v == nil {
		t.Fatalf("open: %v", err)
	}
	ss := v.Snapshot()
	decode := func(skip bool) map[float64][]byte {
		dec, err := NewVideoDecoder(v.Config())
		if err != nil {
			t.Fatal(err)
		}
		out := map[float64][]byte{}
		for _, s := range ss {
			data, _ := s.Bytes()
			if skip && dec.Disposable(data) {
				if s.Key {
					t.Fatalf("a key frame (pts %.3f) reported as disposable", s.PTS)
				}
				continue
			}
			pic, err := dec.Decode(data)
			if err != nil {
				t.Fatal(err)
			}
			if pic.Valid() {
				out[s.PTS] = append([]byte(nil), pic.RGBA().Pix...)
			}
		}
		return out
	}
	full, skipped := decode(false), decode(true)
	if len(skipped) == len(full) || len(skipped) < len(full)/3 {
		t.Fatalf("pictures: %d of %d decoded after skipping disposable units (the clip has B-frames)", len(skipped), len(full))
	}
	for pts, pix := range skipped {
		if string(pix) != string(full[pts]) {
			t.Fatalf("picture at %.3f differs when disposable units are skipped: one of them was referenced", pts)
		}
	}
}
