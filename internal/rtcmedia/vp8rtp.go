package rtcmedia

import "errors"

// RFC 7741 VP8 RTP payload descriptor.
//
//	 0 1 2 3 4 5 6 7
//	+-+-+-+-+-+-+-+-+
//	|X|R|N|S|R| PID |   X: extension present, N: non-reference frame, S: start of partition
//	+-+-+-+-+-+-+-+-+
//	|I|L|T|K| RSV   |   (when X)
//	+-+-+-+-+-+-+-+-+
//	|M| PictureID   |   (when I; 15 bits when M)
//	+-+-+-+-+-+-+-+-+

const vp8DescSize = 4 // X byte + extension byte + 15-bit picture id

// vp8Packetize splits an encoded frame into RTP payloads, each starting with a descriptor that carries
// the 15-bit picture id. The frame is cut into near-equal parts no larger than maxPayload; partStarts
// lists the byte offsets of the VP8 partitions so that S and PID are set exactly as the RFC says.
func vp8Packetize(frame []byte, partStarts []int, pictureID uint16, nonRef bool, maxPayload int) [][]byte {
	room := maxPayload - vp8DescSize
	if room < 100 || len(frame) == 0 {
		return nil
	}
	n := (len(frame) + room - 1) / room
	size := (len(frame) + n - 1) / n
	out := make([][]byte, 0, n)
	for start := 0; start < len(frame); start += size {
		end := start + size
		if end > len(frame) {
			end = len(frame)
		}
		pid, s := 0, 0
		for k, off := range partStarts {
			if off <= start {
				pid = k
				if off == start {
					s = 1
				}
			}
		}
		if len(partStarts) == 0 {
			if start == 0 {
				s = 1
			}
		}
		if pid > 7 {
			pid = 7
		}
		p := make([]byte, vp8DescSize+end-start)
		p[0] = 0x80 | byte(s)<<4 | byte(pid)
		if nonRef {
			p[0] |= 0x20
		}
		p[1] = 0x80
		p[2] = 0x80 | byte(pictureID>>8)&0x7f
		p[3] = byte(pictureID)
		copy(p[vp8DescSize:], frame[start:end])
		out = append(out, p)
	}
	return out
}

// vp8Desc is a parsed payload descriptor.
type vp8Desc struct {
	start     bool // S bit
	pid       int
	nonRef    bool
	pictureID int // -1 when absent
	tid       int // -1 when absent
	hdrLen    int
}

var errShortVP8 = errors.New("rtcmedia: short VP8 payload")

func parseVP8Desc(b []byte) (vp8Desc, error) {
	d := vp8Desc{pictureID: -1, tid: -1}
	if len(b) < 1 {
		return d, errShortVP8
	}
	x := b[0]&0x80 != 0
	d.nonRef = b[0]&0x20 != 0
	d.start = b[0]&0x10 != 0
	d.pid = int(b[0] & 7)
	i := 1
	if x {
		if len(b) < 2 {
			return d, errShortVP8
		}
		ext := b[1]
		i = 2
		if ext&0x80 != 0 { // I
			if len(b) <= i {
				return d, errShortVP8
			}
			if b[i]&0x80 != 0 {
				if len(b) <= i+1 {
					return d, errShortVP8
				}
				d.pictureID = int(b[i]&0x7f)<<8 | int(b[i+1])
				i += 2
			} else {
				d.pictureID = int(b[i] & 0x7f)
				i++
			}
		}
		if ext&0x40 != 0 { // L
			i++
		}
		if ext&0x30 != 0 { // T or K
			if len(b) <= i {
				return d, errShortVP8
			}
			if ext&0x20 != 0 {
				d.tid = int(b[i] >> 6)
			}
			i++
		}
	}
	if i >= len(b) {
		return d, errShortVP8
	}
	d.hdrLen = i
	return d, nil
}
