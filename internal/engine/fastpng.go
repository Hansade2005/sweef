package engine

// fastpng.go: the screenshot encoder.
//
// image/png tries all five row filters on every row and keeps the best; for a 1280x800 screenshot that was
// 35 ms per capture even at BestSpeed, a third of the time a screenshot took. Screenshots go to files an agent
// or a person looks at, so encoding speed matters more than the last few percent of size: every row uses the
// Up filter (one subtraction per byte; text and flat colour compress well with it), deflate runs at
// BestSpeed, and an image with no transparent pixel is written as RGB.

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"hash/crc32"
	"image"
	"io"
)

// encodePNGFast writes img as a PNG.
func encodePNGFast(w io.Writer, img *image.RGBA) error {
	b := img.Bounds()
	wd, ht := b.Dx(), b.Dy()
	opaque := img.Opaque()
	bpp := 4
	ctype := byte(6)
	if opaque {
		bpp, ctype = 3, 2
	}
	bw := bufio.NewWriterSize(w, 64<<10)
	bw.Write([]byte("\x89PNG\r\n\x1a\n"))
	var ihdr [13]byte
	binary.BigEndian.PutUint32(ihdr[0:], uint32(wd))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(ht))
	ihdr[8], ihdr[9] = 8, ctype // bit depth, colour type; compression, filter and interlace 0
	writeChunk(bw, "IHDR", ihdr[:])
	var z bytes.Buffer
	zw, _ := zlib.NewWriterLevel(&z, zlib.BestSpeed)
	row := make([]byte, 1+wd*bpp)
	prev := make([]byte, wd*bpp)
	cur := make([]byte, wd*bpp)
	for y := 0; y < ht; y++ {
		src := img.Pix[(y)*img.Stride : (y)*img.Stride+wd*4]
		if opaque {
			for x, j := 0, 0; x < wd*4; x, j = x+4, j+3 {
				cur[j], cur[j+1], cur[j+2] = src[x], src[x+1], src[x+2]
			}
		} else { // image.RGBA is alpha-premultiplied, PNG is not
			for x := 0; x < wd*4; x += 4 {
				a := src[x+3]
				switch a {
				case 255:
					copy(cur[x:x+4], src[x:x+4])
				case 0:
					cur[x], cur[x+1], cur[x+2], cur[x+3] = 0, 0, 0, 0
				default:
					a16 := uint32(a) * 0x101 // as color.NRGBAModel converts (what image/png writes)
					cur[x] = uint8((uint32(src[x]) * 0x101 * 0xffff / a16) >> 8)
					cur[x+1] = uint8((uint32(src[x+1]) * 0x101 * 0xffff / a16) >> 8)
					cur[x+2] = uint8((uint32(src[x+2]) * 0x101 * 0xffff / a16) >> 8)
					cur[x+3] = a
				}
			}
		}
		row[0] = 2 // Up
		for i, c := range cur {
			row[1+i] = c - prev[i]
		}
		if _, err := zw.Write(row); err != nil {
			return err
		}
		prev, cur = cur, prev
	}
	if err := zw.Close(); err != nil {
		return err
	}
	writeChunk(bw, "IDAT", z.Bytes())
	writeChunk(bw, "IEND", nil)
	return bw.Flush()
}

func writeChunk(w *bufio.Writer, typ string, data []byte) {
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(data)))
	copy(hdr[4:], typ)
	w.Write(hdr[:])
	w.Write(data)
	crc := crc32.NewIEEE()
	crc.Write(hdr[4:])
	crc.Write(data)
	var c [4]byte
	binary.BigEndian.PutUint32(c[:], crc.Sum32())
	w.Write(c[:])
}
