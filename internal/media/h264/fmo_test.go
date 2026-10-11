package h264

import "testing"

func sgHeader(pps *PPS, w, h int, cycle int) (*Decoder, *sliceHeader) {
	sps := &SPS{MbW: w, MbH: h, FrameMbsOnly: true}
	return &Decoder{mbW: w, mbH: h}, &sliceHeader{sps: sps, pps: pps, sliceGroupCycle: cycle}
}

func TestSliceGroupMaps(t *testing.T) {
	str := func(m []uint8) string {
		b := make([]byte, len(m))
		for i, v := range m {
			b[i] = '0' + v
		}
		return string(b)
	}
	cases := []struct {
		name  string
		pps   PPS
		w, h  int
		cycle int
		want  string
	}{
		{"interleaved", PPS{NumSliceGroups: 2, SliceGroupType: 0, RunLength: [8]int{3, 5}}, 4, 2, 0, "00011111"},
		{"dispersed", PPS{NumSliceGroups: 2, SliceGroupType: 1}, 4, 2, 0, "01011010"},
		{"foreground", PPS{NumSliceGroups: 3, SliceGroupType: 2, TopLeft: [8]int{5, 0}, BotRght: [8]int{6, 1}}, 4, 3, 0, "112220022222"},
		{"raster", PPS{NumSliceGroups: 2, SliceGroupType: 4, ChangeRate: 2}, 4, 2, 2, "00001111"},
		{"raster-rev", PPS{NumSliceGroups: 2, SliceGroupType: 4, ChangeRate: 2, ChangeDirection: true}, 4, 2, 2, "11110000"},
		{"wipe", PPS{NumSliceGroups: 2, SliceGroupType: 5, ChangeRate: 3}, 3, 2, 1, "001011"},
		{"explicit", PPS{NumSliceGroups: 2, SliceGroupType: 6, SliceGroupID: []uint8{1, 0, 0, 1}}, 2, 2, 0, "1001"},
	}
	for _, c := range cases {
		pps := c.pps
		d, h := sgHeader(&pps, c.w, c.h, c.cycle)
		if got := str(d.sliceGroupMap(h)); got != c.want {
			t.Errorf("%s: %s want %s", c.name, got, c.want)
		}
	}
	// box-out: group 0 grows from the centre, mapUnitsInSliceGroup0 units
	pps := PPS{NumSliceGroups: 2, SliceGroupType: 3, ChangeRate: 1}
	d, h := sgHeader(&pps, 4, 4, 5)
	m := d.sliceGroupMap(h)
	n0 := 0
	for _, v := range m {
		if v == 0 {
			n0++
		}
	}
	if n0 != 5 {
		t.Errorf("box-out: %d units in group 0, want 5", n0)
	}
}
