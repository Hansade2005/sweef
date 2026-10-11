package av1

// Annex B length-delimited bitstream format (spec Annex B): temporal units,
// frame units and OBUs are each preceded by a leb128 size and the OBUs
// normally omit obu_size. Decode detects the format and handles both it and
// the low-overhead format of section 5.

// lebAt reads a leb128 value at data[p:].
func lebAt(data []byte, p int) (v, n int, ok bool) {
	for i := 0; i < 8; i++ {
		if p+i >= len(data) {
			return 0, 0, false
		}
		c := data[p+i]
		v |= int(c&0x7f) << uint(7*i)
		if c&0x80 == 0 {
			return v, i + 1, true
		}
	}
	return 0, 0, false
}

// obuHeaderOK checks an OBU header and returns its type.
func obuHeaderOK(o []byte) (typ int, ok bool) {
	if len(o) == 0 || o[0]&0x80 != 0 {
		return 0, false
	}
	return int(o[0]>>3) & 15, true
}

// annexBValid reports whether data parses exactly as a series of Annex B
// temporal units whose first OBU is a temporal delimiter (a requirement of
// the format that also makes the detection reliable).
func annexBValid(data []byte) bool {
	p := 0
	first := true
	for p < len(data) {
		tuSize, n, ok := lebAt(data, p)
		if !ok || tuSize == 0 || p+n+tuSize > len(data) {
			return false
		}
		p += n
		end := p + tuSize
		for p < end {
			fuSize, n, ok := lebAt(data, p)
			if !ok || fuSize == 0 || p+n+fuSize > end {
				return false
			}
			p += n
			fend := p + fuSize
			for p < fend {
				obuLen, n, ok := lebAt(data, p)
				if !ok || obuLen == 0 || p+n+obuLen > fend {
					return false
				}
				p += n
				typ, ok := obuHeaderOK(data[p : p+obuLen])
				if !ok {
					return false
				}
				if first && typ != obuTemporalDelimiter {
					return false
				}
				first = false
				p += obuLen
			}
		}
	}
	return !first
}

// IsAnnexB reports whether data is an Annex B stream rather than a
// low-overhead (section 5) one. A stream is taken to be Annex B when all its
// nested sizes are consistent and its first OBU is a temporal delimiter, which
// a section 5 stream (starting 0x12 0x00 with its own temporal delimiter, or
// with a sequence header or frame) practically never satisfies.
func IsAnnexB(data []byte) bool {
	if len(data) >= 2 && data[0] == 0x12 && data[1] == 0 {
		return false
	}
	return annexBValid(data)
}

// decodeAnnexB decodes Annex B temporal units.
func (d *Decoder) decodeAnnexB(data []byte) error {
	cov(covAnnexB)
	p := 0
	for p < len(data) {
		tuSize, n, _ := lebAt(data, p)
		p += n
		end := p + tuSize
		for p < end {
			fuSize, n, _ := lebAt(data, p)
			p += n
			fend := p + fuSize
			for p < fend {
				obuLen, n, _ := lebAt(data, p)
				p += n
				if _, err := d.decodeOBU(data[p : p+obuLen]); err != nil {
					d.seenFrameHeader = false
					d.fd = nil
					return err
				}
				p += obuLen
			}
		}
	}
	return nil
}
