package main

import (
	"bytes"
	"debug/macho"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"sort"
)

const (
	fatMagic  = 0xcafebabe
	fatAlign  = 14 // 2^14 = 16 KiB, what Apple's lipo uses (required for arm64)
	fatHdrLen = 8
	fatArchSz = 20
)

type thin struct {
	cpu  macho.Cpu
	sub  uint32
	data []byte
	name string
}

func cmdLipo(args []string) error {
	fs := flag.NewFlagSet("lipo", flag.ContinueOnError)
	out := fs.String("o", "", "output file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" || fs.NArg() < 1 {
		return fmt.Errorf("usage: lipo -o OUT THIN...")
	}
	var ts []thin
	for _, p := range fs.Args() {
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		t, err := parseThin(p, b)
		if err != nil {
			return err
		}
		ts = append(ts, t)
	}
	fat, err := buildFat(ts)
	if err != nil {
		return err
	}
	return os.WriteFile(*out, fat, 0o755)
}

func parseThin(name string, b []byte) (thin, error) {
	f, err := macho.NewFile(bytes.NewReader(b))
	if err != nil {
		return thin{}, fmt.Errorf("%s: not a thin Mach-O: %w", name, err)
	}
	defer f.Close()
	if f.Type != macho.TypeExec {
		return thin{}, fmt.Errorf("%s: Mach-O is not an executable", name)
	}
	return thin{cpu: f.Cpu, sub: f.SubCpu, data: b, name: name}, nil
}

func alignUp(n, a uint64) uint64 { return (n + a - 1) &^ (a - 1) }

func buildFat(ts []thin) ([]byte, error) {
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].cpu < ts[j].cpu })
	for i := 1; i < len(ts); i++ {
		if ts[i].cpu == ts[i-1].cpu {
			return nil, fmt.Errorf("%s and %s have the same architecture", ts[i-1].name, ts[i].name)
		}
	}
	align := uint64(1) << fatAlign
	off := alignUp(uint64(fatHdrLen+fatArchSz*len(ts)), align)
	var buf bytes.Buffer
	be := binary.BigEndian
	hdr := make([]byte, fatHdrLen)
	be.PutUint32(hdr[0:], fatMagic)
	be.PutUint32(hdr[4:], uint32(len(ts)))
	buf.Write(hdr)
	offs := make([]uint64, len(ts))
	for i, t := range ts {
		offs[i] = off
		if off+uint64(len(t.data)) > 1<<32-1 {
			return nil, fmt.Errorf("fat32 offsets overflow; slice too large")
		}
		e := make([]byte, fatArchSz)
		be.PutUint32(e[0:], uint32(t.cpu))
		be.PutUint32(e[4:], uint32(t.sub))
		be.PutUint32(e[8:], uint32(off))
		be.PutUint32(e[12:], uint32(len(t.data)))
		be.PutUint32(e[16:], fatAlign)
		buf.Write(e)
		off = alignUp(off+uint64(len(t.data)), align)
	}
	for i, t := range ts {
		buf.Write(make([]byte, int(offs[i])-buf.Len())) // zero padding, deterministic
		buf.Write(t.data)
	}
	return buf.Bytes(), nil
}

func cmdLipoInfo(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: lipo-info FILE")
	}
	if ff, err := macho.OpenFat(args[0]); err == nil {
		defer ff.Close()
		fmt.Printf("fat file with %d architectures:", len(ff.Arches))
		for _, a := range ff.Arches {
			fmt.Printf(" %s", cpuName(a.Cpu))
		}
		fmt.Println()
		return nil
	}
	f, err := macho.Open(args[0])
	if err != nil {
		return err
	}
	defer f.Close()
	fmt.Printf("thin file: %s\n", cpuName(f.Cpu))
	return nil
}

func cpuName(c macho.Cpu) string {
	switch c {
	case macho.CpuAmd64:
		return "x86_64"
	case macho.CpuArm64:
		return "arm64"
	}
	return c.String()
}
