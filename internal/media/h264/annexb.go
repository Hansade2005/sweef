package h264

// SplitAccessUnits splits an Annex B elementary stream into access units
// (each returned with start codes, ready for DecodeFrame). Picture
// boundaries follow 7.4.1.2.4 using the parsed slice headers, so streams
// with arbitrary slice order, slice groups or separate colour planes are
// split correctly.
func SplitAccessUnits(es []byte) [][]byte {
	d := NewDecoder()
	var aus [][]byte
	var cur []byte
	var first *sliceHeader
	sawVCL := false
	flush := func() {
		if len(cur) > 0 {
			aus = append(aus, cur)
		}
		cur, first, sawVCL = nil, nil, false
	}
	for _, n := range splitAnnexB(es) {
		if len(n) == 0 {
			continue
		}
		typ := int(n[0] & 31)
		switch typ {
		case 9:
			flush()
		case 7, 8, 6, 14, 15:
			if sawVCL {
				flush()
			}
			switch typ {
			case 7:
				if s, err := parseSPS(unescape(n[1:])); err == nil {
					d.setSPS(s)
				}
			case 8:
				if p, err := parsePPS(unescape(n[1:]), &d.sps); err == nil {
					d.pps[p.ID] = p
				}
			}
		case 1, 2, 5:
			h, err := d.parseSliceHeader(newBitReader(unescape(n[1:])), typ, int(n[0]>>5)&3)
			if err == nil {
				if first != nil && d.isNewPictureFrom(first, h) {
					flush()
				}
				if first == nil {
					first = h
				}
			}
			sawVCL = true
		}
		cur = append(cur, 0, 0, 0, 1)
		cur = append(cur, n...)
	}
	flush()
	return aus
}
