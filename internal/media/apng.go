package media

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/draw"
	"image/png"
)

// APNG: split into per-frame PNG streams, decode each with image/png and
// composite according to dispose/blend ops.

type apngFrame struct {
	w, h, x, y     int
	delayN, delayD int
	dispose, blend byte
	idat           [][]byte // IDAT payload chunks (fdAT with the sequence number stripped)
}

func newAPNG(data []byte) FrameSource {
	if len(data) < 8 || string(data[1:4]) != "PNG" {
		return nil
	}
	p := 8
	var ihdr []byte
	var others [][]byte // chunks copied into every frame (PLTE, tRNS ...)
	var frames []*apngFrame
	var cur *apngFrame
	var defaultIDAT [][]byte
	isAnim := false
	plays := 0
	for p+12 <= len(data) {
		n := int(binary.BigEndian.Uint32(data[p:]))
		typ := string(data[p+4 : p+8])
		if p+12+n > len(data) {
			break
		}
		body := data[p+8 : p+8+n]
		switch typ {
		case "IHDR":
			ihdr = body
		case "acTL":
			isAnim = true
			if len(body) >= 8 {
				plays = int(binary.BigEndian.Uint32(body[4:]))
			}
		case "fcTL":
			if len(body) >= 26 {
				cur = &apngFrame{
					w: int(binary.BigEndian.Uint32(body[4:])), h: int(binary.BigEndian.Uint32(body[8:])),
					x: int(binary.BigEndian.Uint32(body[12:])), y: int(binary.BigEndian.Uint32(body[16:])),
					delayN: int(binary.BigEndian.Uint16(body[20:])), delayD: int(binary.BigEndian.Uint16(body[22:])),
					dispose: body[24], blend: body[25],
				}
				frames = append(frames, cur)
			}
		case "IDAT":
			if cur != nil {
				cur.idat = append(cur.idat, body)
			} else {
				defaultIDAT = append(defaultIDAT, body)
			}
		case "fdAT":
			if cur != nil && len(body) > 4 {
				cur.idat = append(cur.idat, body[4:])
			}
		case "PLTE", "tRNS", "gAMA", "sRGB", "cHRM":
			others = append(others, data[p:p+12+n])
		}
		p += 12 + n
	}
	if !isAnim || len(frames) < 2 || ihdr == nil || len(ihdr) < 13 {
		return nil
	}
	cw := int(binary.BigEndian.Uint32(ihdr))
	ch := int(binary.BigEndian.Uint32(ihdr[4:]))
	canvas := image.NewNRGBA(image.Rect(0, 0, cw, ch))
	a := &animFrames{loop: plays == 0}
	var tcum float64
	chunk := func(buf *bytes.Buffer, typ string, body []byte) {
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(body)))
		buf.Write(l[:])
		buf.WriteString(typ)
		buf.Write(body)
		c := crc32.NewIEEE()
		c.Write([]byte(typ))
		c.Write(body)
		binary.BigEndian.PutUint32(l[:], c.Sum32())
		buf.Write(l[:])
	}
	for _, f := range frames {
		var buf bytes.Buffer
		buf.Write(data[:8])
		h := append([]byte(nil), ihdr...)
		binary.BigEndian.PutUint32(h[0:], uint32(f.w))
		binary.BigEndian.PutUint32(h[4:], uint32(f.h))
		chunk(&buf, "IHDR", h)
		for _, o := range others {
			buf.Write(o)
		}
		for _, d := range f.idat {
			chunk(&buf, "IDAT", d)
		}
		chunk(&buf, "IEND", nil)
		img, err := png.Decode(&buf)
		if err != nil {
			return nil
		}
		var prev *image.NRGBA
		if f.dispose == 2 {
			prev = image.NewNRGBA(canvas.Rect)
			copy(prev.Pix, canvas.Pix)
		}
		r := image.Rect(f.x, f.y, f.x+f.w, f.y+f.h)
		op := draw.Over
		if f.blend == 0 {
			op = draw.Src
		}
		draw.Draw(canvas, r, img, img.Bounds().Min, op)
		snap := image.NewNRGBA(canvas.Rect)
		copy(snap.Pix, canvas.Pix)
		a.frames = append(a.frames, snap)
		d := float64(f.delayN)
		if f.delayD == 0 {
			d /= 100
		} else {
			d /= float64(f.delayD)
		}
		if d <= 0 {
			d = 0.1
		}
		tcum += d
		a.ends = append(a.ends, tcum)
		switch f.dispose {
		case 1:
			draw.Draw(canvas, r, image.Transparent, image.Point{}, draw.Src)
		case 2:
			if prev != nil {
				copy(canvas.Pix, prev.Pix)
			}
		}
	}
	_ = defaultIDAT
	return a
}
