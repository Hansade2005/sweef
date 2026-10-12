package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Resource type IDs.
const (
	rtIcon      = 3
	rtGroupIcon = 14
	rtVersion   = 16
	rtManifest  = 24
	langEnUS    = 0x0409
)

type resource struct {
	typ, id, lang uint16
	data          []byte
}

func utf16z(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, 2*len(u)+2)
	for _, c := range u {
		b = append(b, byte(c), byte(c>>8))
	}
	return append(b, 0, 0)
}

func pad4(b []byte) []byte {
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

// verBlock encodes one VS_VERSIONINFO node (VersionInfo, StringFileInfo, StringTable, String, VarFileInfo, Var).
func verBlock(key string, value []byte, valueLen int, text bool, children ...[]byte) []byte {
	b := make([]byte, 6)
	b = append(b, utf16z(key)...)
	b = pad4(b)
	b = append(b, value...)
	for _, c := range children {
		b = pad4(b)
		b = append(b, c...)
	}
	binary.LittleEndian.PutUint16(b[0:], uint16(len(b)))
	binary.LittleEndian.PutUint16(b[2:], uint16(valueLen))
	if text {
		binary.LittleEndian.PutUint16(b[4:], 1)
	}
	return b
}

type versionInfo struct {
	version                  [4]uint16
	prerelease               bool
	name, desc, company      string
	copyright, original, ver string
}

func buildVersionInfo(v versionInfo) []byte {
	le := binary.LittleEndian
	ff := make([]byte, 52)
	le.PutUint32(ff[0:], 0xFEEF04BD)
	le.PutUint32(ff[4:], 0x00010000)
	ms := uint32(v.version[0])<<16 | uint32(v.version[1])
	ls := uint32(v.version[2])<<16 | uint32(v.version[3])
	le.PutUint32(ff[8:], ms)
	le.PutUint32(ff[12:], ls)
	le.PutUint32(ff[16:], ms)
	le.PutUint32(ff[20:], ls)
	le.PutUint32(ff[24:], 0x3f) // FileFlagsMask
	if v.prerelease {
		le.PutUint32(ff[28:], 0x2) // VS_FF_PRERELEASE
	}
	le.PutUint32(ff[32:], 0x00040004) // VOS_NT_WINDOWS32
	le.PutUint32(ff[36:], 1)          // VFT_APP
	var strs [][]byte
	kv := [][2]string{
		{"CompanyName", v.company}, {"FileDescription", v.desc}, {"FileVersion", v.ver},
		{"InternalName", v.name}, {"LegalCopyright", v.copyright}, {"OriginalFilename", v.original},
		{"ProductName", v.name}, {"ProductVersion", v.ver},
	}
	for _, p := range kv {
		if p[1] == "" {
			continue
		}
		val := utf16z(p[1])
		strs = append(strs, verBlock(p[0], val, len(val)/2, true))
	}
	table := verBlock("040904B0", nil, 0, true, strs...)
	sfi := verBlock("StringFileInfo", nil, 0, true, table)
	tr := make([]byte, 4)
	le.PutUint32(tr, 0x04B00409)
	vfi := verBlock("VarFileInfo", nil, 0, true, verBlock("Translation", tr, 4, false))
	return verBlock("VS_VERSION_INFO", ff, 52, false, sfi, vfi)
}

// buildGroupIcon converts an .ico (PNG or BMP payload entries) into RT_GROUP_ICON plus RT_ICON resources.
func buildIconResources(ico []byte) ([]resource, error) {
	le := binary.LittleEndian
	if len(ico) < 6 || le.Uint16(ico[0:]) != 0 || le.Uint16(ico[2:]) != 1 {
		return nil, fmt.Errorf("not an .ico file")
	}
	n := int(le.Uint16(ico[4:]))
	var res []resource
	grp := make([]byte, 6, 6+14*n)
	le.PutUint16(grp[2:], 1)
	le.PutUint16(grp[4:], uint16(n))
	for i := 0; i < n; i++ {
		e := ico[6+16*i:]
		if len(e) < 16 {
			return nil, fmt.Errorf("truncated .ico")
		}
		size, off := le.Uint32(e[8:]), le.Uint32(e[12:])
		if uint64(off)+uint64(size) > uint64(len(ico)) {
			return nil, fmt.Errorf("ico entry %d out of range", i)
		}
		id := uint16(i + 1)
		res = append(res, resource{rtIcon, id, langEnUS, ico[off : off+size]})
		g := make([]byte, 14)
		copy(g[0:8], e[0:8]) // width,height,colors,reserved,planes,bitcount
		le.PutUint32(g[8:], size)
		le.PutUint16(g[12:], id)
		grp = append(grp, g...)
	}
	res = append(res, resource{rtGroupIcon, 1, langEnUS, grp})
	return res, nil
}

// buildSyso lays the resources out as a COFF object with one .rsrc section; the
// linker applies the ADDR32NB relocations on each data entry.
func buildSyso(arch string, res []resource) ([]byte, error) {
	var machine, relType uint16
	switch arch {
	case "amd64":
		machine, relType = 0x8664, 3 // IMAGE_REL_AMD64_ADDR32NB
	case "arm64":
		machine, relType = 0xAA64, 2 // IMAGE_REL_ARM64_ADDR32NB
	case "386":
		machine, relType = 0x14c, 7 // IMAGE_REL_I386_DIR32NB
	default:
		return nil, fmt.Errorf("unsupported arch %q", arch)
	}
	le := binary.LittleEndian
	// group: type -> name -> lang
	sort.Slice(res, func(i, j int) bool {
		a, b := res[i], res[j]
		if a.typ != b.typ {
			return a.typ < b.typ
		}
		if a.id != b.id {
			return a.id < b.id
		}
		return a.lang < b.lang
	})
	type nameGrp struct {
		id    uint16
		langs []resource
	}
	type typeGrp struct {
		id    uint16
		names []*nameGrp
	}
	var types []*typeGrp
	for _, r := range res {
		if len(types) == 0 || types[len(types)-1].id != r.typ {
			types = append(types, &typeGrp{id: r.typ})
		}
		t := types[len(types)-1]
		if len(t.names) == 0 || t.names[len(t.names)-1].id != r.id {
			t.names = append(t.names, &nameGrp{id: r.id})
		}
		n := t.names[len(t.names)-1]
		n.langs = append(n.langs, r)
	}
	// compute sizes: directory tables, then data entries, then data.
	dirSize := func(n int) int { return 16 + 8*n }
	total := dirSize(len(types))
	for _, t := range types {
		total += dirSize(len(t.names))
		for _, n := range t.names {
			total += dirSize(len(n.langs))
		}
	}
	nData := len(res)
	entriesBase := total
	dataBase := alignUp32(entriesBase + 16*nData)
	buf := make([]byte, dataBase)
	var relocs []uint32 // offsets of OffsetToData fields

	put := func(off int, v ...uint32) {
		for i, x := range v {
			le.PutUint32(buf[off+4*i:], x)
		}
	}
	dirHdr := func(off, n int) {
		put(off, 0, 0, 0)
		le.PutUint16(buf[off+12:], 0)
		le.PutUint16(buf[off+14:], uint16(n))
	}

	// emit tables breadth-first with computed offsets
	pos := dirSize(len(types))
	dirHdr(0, len(types))
	typeOff := make([]int, len(types))
	for i, t := range types {
		typeOff[i] = pos
		pos += dirSize(len(t.names))
		for _, n := range t.names {
			pos += dirSize(len(n.langs))
		}
	}
	dataOff := dataBase
	entry := 0
	for i, t := range types {
		put(16+8*i, uint32(t.id), 0x80000000|uint32(typeOff[i]))
		dirHdr(typeOff[i], len(t.names))
		nameOff := typeOff[i] + dirSize(len(t.names))
		for j, n := range t.names {
			put(typeOff[i]+16+8*j, uint32(n.id), 0x80000000|uint32(nameOff))
			dirHdr(nameOff, len(n.langs))
			for k, l := range n.langs {
				eoff := entriesBase + 16*entry
				put(nameOff+16+8*k, uint32(l.lang), uint32(eoff))
				// data entry: RVA (reloc target, in-place addend = section offset), size, codepage, reserved
				put(eoff, uint32(dataOff), uint32(len(l.data)), 0, 0)
				relocs = append(relocs, uint32(eoff))
				buf = append(buf, l.data...)
				for len(buf)%4 != 0 {
					buf = append(buf, 0)
				}
				dataOff = len(buf)
				entry++
			}
			nameOff += dirSize(len(n.langs))
		}
	}
	sect := buf

	var out bytes.Buffer
	const hdrLen, sectHdr = 20, 40
	rawOff := hdrLen + sectHdr
	relOff := rawOff + len(sect)
	symOff := relOff + 10*len(relocs)
	w := func(v any) { binary.Write(&out, le, v) }
	w(machine)
	w(uint16(1))
	w(uint32(0)) // timestamp: 0 for reproducibility
	w(uint32(symOff))
	w(uint32(2)) // symbol + 1 aux
	w(uint16(0))
	w(uint16(0))
	out.Write(append([]byte(".rsrc"), 0, 0, 0))
	w(uint32(0)) // VirtualSize
	w(uint32(0)) // VirtualAddress
	w(uint32(len(sect)))
	w(uint32(rawOff))
	w(uint32(relOff))
	w(uint32(0))
	w(uint16(len(relocs)))
	w(uint16(0))
	w(uint32(0xC0300040)) // INITIALIZED_DATA | ALIGN_4BYTES | MEM_READ | MEM_WRITE
	out.Write(sect)
	for _, r := range relocs {
		w(r)
		w(uint32(0)) // symbol index 0 = the .rsrc section symbol
		w(relType)
	}
	// symbol table: section symbol + aux record
	out.Write(append([]byte(".rsrc"), 0, 0, 0))
	w(uint32(0))
	w(int16(1))
	w(uint16(0))
	out.WriteByte(3) // IMAGE_SYM_CLASS_STATIC
	out.WriteByte(1) // aux count
	w(uint32(len(sect)))
	w(uint16(len(relocs)))
	w(uint16(0))
	w(uint32(0))
	w(uint16(0))
	out.WriteByte(0)
	out.Write(make([]byte, 3))
	w(uint32(4)) // empty string table
	return out.Bytes(), nil
}

func alignUp32(n int) int { return (n + 3) &^ 3 }

func parseVersion4(s string) ([4]uint16, error) {
	var v [4]uint16
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return v, fmt.Errorf("version %q must be a.b.c.d", s)
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 65535 {
			return v, fmt.Errorf("version %q: component %q out of range", s, p)
		}
		v[i] = uint16(n)
	}
	return v, nil
}

func cmdSyso(args []string) error {
	fs := flag.NewFlagSet("syso", flag.ContinueOnError)
	arch := fs.String("arch", "amd64", "amd64 or arm64")
	out := fs.String("o", "", "output .syso")
	manifest := fs.String("manifest", "", "application manifest XML")
	ico := fs.String("ico", "", "application .ico")
	ver := fs.String("version", "", "numeric version a.b.c.d")
	display := fs.String("display-version", "", "ProductVersion string (default: -version)")
	name := fs.String("name", "Sweef", "product name")
	desc := fs.String("desc", "Sweef Browser", "file description")
	company := fs.String("company", "Sweef", "company name")
	copyright := fs.String("copyright", "", "legal copyright")
	original := fs.String("original", "sweef.exe", "original filename")
	pre := fs.Bool("prerelease", false, "mark as pre-release")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" || *ver == "" {
		return fmt.Errorf("-o and -version are required")
	}
	v4, err := parseVersion4(*ver)
	if err != nil {
		return err
	}
	dv := *display
	if dv == "" {
		dv = *ver
	}
	var res []resource
	if *manifest != "" {
		b, err := os.ReadFile(*manifest)
		if err != nil {
			return err
		}
		res = append(res, resource{rtManifest, 1, langEnUS, b})
	}
	if *ico != "" {
		b, err := os.ReadFile(*ico)
		if err != nil {
			return err
		}
		ir, err := buildIconResources(b)
		if err != nil {
			return err
		}
		res = append(res, ir...)
	}
	res = append(res, resource{rtVersion, 1, langEnUS, buildVersionInfo(versionInfo{
		version: v4, prerelease: *pre, name: *name, desc: *desc, company: *company,
		copyright: *copyright, original: *original, ver: dv,
	})})
	b, err := buildSyso(*arch, res)
	if err != nil {
		return err
	}
	return os.WriteFile(*out, b, 0o644)
}
