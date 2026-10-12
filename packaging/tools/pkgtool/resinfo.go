package main

import (
	"debug/pe"
	"encoding/binary"
	"fmt"
	"os"
	"unicode/utf16"
)

type resEntry struct {
	typ, id, lang uint32
	data          []byte
}

// readResources walks the .rsrc section of a PE image or COFF object.
func readResources(path string) ([]resEntry, error) {
	f, err := pe.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := f.Section(".rsrc")
	if s == nil {
		return nil, fmt.Errorf("no .rsrc section")
	}
	d, err := s.Data()
	if err != nil {
		return nil, err
	}
	// In a linked image OffsetToData is an RVA; in an object it is a section offset (VirtualAddress==0).
	base := s.VirtualAddress
	le := binary.LittleEndian
	var out []resEntry
	var walk func(off uint32, level int, ids [3]uint32) error
	walk = func(off uint32, level int, ids [3]uint32) error {
		if int(off)+16 > len(d) {
			return fmt.Errorf("directory out of range")
		}
		n := int(le.Uint16(d[off+12:])) + int(le.Uint16(d[off+14:]))
		for i := 0; i < n; i++ {
			e := off + 16 + uint32(8*i)
			if int(e)+8 > len(d) {
				return fmt.Errorf("entry out of range")
			}
			id, target := le.Uint32(d[e:]), le.Uint32(d[e+4:])
			ids[level] = id
			if target&0x80000000 != 0 {
				if level >= 2 {
					return fmt.Errorf("resource tree too deep")
				}
				if err := walk(target&0x7fffffff, level+1, ids); err != nil {
					return err
				}
				continue
			}
			if int(target)+16 > len(d) {
				return fmt.Errorf("data entry out of range")
			}
			rva, size := le.Uint32(d[target:]), le.Uint32(d[target+4:])
			o := rva - base
			if uint64(o)+uint64(size) > uint64(len(d)) {
				return fmt.Errorf("resource data out of range (rva %#x)", rva)
			}
			out = append(out, resEntry{ids[0], ids[1], ids[2], d[o : o+size]})
		}
		return nil
	}
	if err := walk(0, 0, [3]uint32{}); err != nil {
		return nil, err
	}
	return out, nil
}

func utf16At(b []byte) string {
	var u []uint16
	for i := 0; i+1 < len(b); i += 2 {
		c := binary.LittleEndian.Uint16(b[i:])
		if c == 0 {
			break
		}
		u = append(u, c)
	}
	return string(utf16.Decode(u))
}

func cmdResInfo(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: resinfo FILE")
	}
	rs, err := readResources(args[0])
	if err != nil {
		return err
	}
	names := map[uint32]string{rtIcon: "ICON", rtGroupIcon: "GROUP_ICON", rtVersion: "VERSION", rtManifest: "MANIFEST"}
	for _, r := range rs {
		n := names[r.typ]
		if n == "" {
			n = fmt.Sprint(r.typ)
		}
		fmt.Printf("%-10s id=%d lang=%#x %d bytes\n", n, r.id, r.lang, len(r.data))
		switch r.typ {
		case rtManifest:
			os.Stdout.Write(r.data)
			fmt.Println()
		case rtVersion:
			for _, kv := range versionStrings(r.data) {
				fmt.Printf("    %s = %s\n", kv[0], kv[1])
			}
		}
	}
	return nil
}

// versionStrings extracts String key/value pairs from a VS_VERSIONINFO blob.
func versionStrings(d []byte) [][2]string {
	var out [][2]string
	le := binary.LittleEndian
	var node func(b []byte, depth int)
	node = func(b []byte, depth int) {
		if len(b) < 6 {
			return
		}
		length, vlen, typ := int(le.Uint16(b[0:])), int(le.Uint16(b[2:])), le.Uint16(b[4:])
		if length > len(b) || length < 6 {
			return
		}
		b = b[:length]
		key, i := "", 6
		for i+1 < len(b) && (b[i] != 0 || b[i+1] != 0) {
			i += 2
		}
		key = utf16At(b[6:])
		i += 2
		i = (i + 3) &^ 3
		valBytes := vlen
		if typ == 1 {
			valBytes = vlen * 2
		}
		if depth == 3 && typ == 1 && i+valBytes <= len(b) {
			out = append(out, [2]string{key, utf16At(b[i : i+valBytes])})
		}
		i += valBytes
		for {
			i = (i + 3) &^ 3
			if i+6 > len(b) {
				return
			}
			cl := int(le.Uint16(b[i:]))
			if cl < 6 || i+cl > len(b) {
				return
			}
			node(b[i:i+cl], depth+1)
			i += cl
		}
	}
	node(d, 0)
	return out
}
