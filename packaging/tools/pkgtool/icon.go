package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image/png"
	"os"
	"sort"
)

type pngFile struct {
	size int // square pixel size
	data []byte
}

func loadPNGs(paths []string) ([]pngFile, error) {
	var out []pngFile
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, err
		}
		cfg, err := png.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		if cfg.Width != cfg.Height {
			return nil, fmt.Errorf("%s: icon must be square, got %dx%d", p, cfg.Width, cfg.Height)
		}
		out = append(out, pngFile{cfg.Width, b})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].size < out[j].size })
	return out, nil
}

// ICNS OSTypes whose payload is a PNG, by pixel size (retina variants reuse the
// same pixels as the next size down, per Apple's iconutil output).
var icnsTypes = map[int][]string{
	16:   {"icp4"},
	32:   {"icp5", "ic11"},
	64:   {"icp6", "ic12"},
	128:  {"ic07"},
	256:  {"ic08", "ic13"},
	512:  {"ic09", "ic14"},
	1024: {"ic10"},
}

func buildICNS(pngs []pngFile) ([]byte, error) {
	type entry struct {
		typ  string
		data []byte
	}
	var es []entry
	for _, p := range pngs {
		ts, ok := icnsTypes[p.size]
		if !ok {
			return nil, fmt.Errorf("no ICNS type for %dpx PNG (use 16,32,64,128,256,512,1024)", p.size)
		}
		for _, t := range ts {
			es = append(es, entry{t, p.data})
		}
	}
	if len(es) == 0 {
		return nil, fmt.Errorf("no PNG inputs")
	}
	total := 8
	for _, e := range es {
		total += 8 + len(e.data)
	}
	var b bytes.Buffer
	b.WriteString("icns")
	binary.Write(&b, binary.BigEndian, uint32(total))
	for _, e := range es {
		b.WriteString(e.typ)
		binary.Write(&b, binary.BigEndian, uint32(8+len(e.data)))
		b.Write(e.data)
	}
	return b.Bytes(), nil
}

func cmdIcns(args []string) error {
	fs := flag.NewFlagSet("icns", flag.ContinueOnError)
	out := fs.String("o", "", "output .icns")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" || fs.NArg() == 0 {
		return fmt.Errorf("usage: icns -o OUT.icns PNG...")
	}
	pngs, err := loadPNGs(fs.Args())
	if err != nil {
		return err
	}
	b, err := buildICNS(pngs)
	if err != nil {
		return err
	}
	return os.WriteFile(*out, b, 0o644)
}

func buildICO(pngs []pngFile) ([]byte, error) {
	if len(pngs) == 0 || len(pngs) > 255 {
		return nil, fmt.Errorf("need 1..255 PNG inputs")
	}
	var b bytes.Buffer
	le := binary.LittleEndian
	binary.Write(&b, le, [3]uint16{0, 1, uint16(len(pngs))})
	off := 6 + 16*len(pngs)
	for _, p := range pngs {
		if p.size > 256 {
			return nil, fmt.Errorf("ICO supports up to 256px, got %d", p.size)
		}
		w := byte(p.size)
		if p.size == 256 {
			w = 0
		}
		b.Write([]byte{w, w, 0, 0})
		binary.Write(&b, le, uint16(1))  // planes
		binary.Write(&b, le, uint16(32)) // bit count
		binary.Write(&b, le, uint32(len(p.data)))
		binary.Write(&b, le, uint32(off))
		off += len(p.data)
	}
	for _, p := range pngs {
		b.Write(p.data)
	}
	return b.Bytes(), nil
}

func cmdIco(args []string) error {
	fs := flag.NewFlagSet("ico", flag.ContinueOnError)
	out := fs.String("o", "", "output .ico")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" || fs.NArg() == 0 {
		return fmt.Errorf("usage: ico -o OUT.ico PNG...")
	}
	pngs, err := loadPNGs(fs.Args())
	if err != nil {
		return err
	}
	b, err := buildICO(pngs)
	if err != nil {
		return err
	}
	return os.WriteFile(*out, b, 0o644)
}
