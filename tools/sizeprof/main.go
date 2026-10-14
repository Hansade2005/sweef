// Command sizeprof reports where the bytes of a Sweef binary go.
//
//	go build -trimpath -ldflags "-buildid=" -o /tmp/sweef.full ./cmd/sweef   (unstripped: symbols are needed)
//	go run ./tools/sizeprof -bin /tmp/sweef.full [-top 40] [-stripped /tmp/sweef]
//
// It prints (1) the ELF sections of the stripped binary (or of the given one), (2) the symbols from `go tool nm -size`
// aggregated by Go package and by symbol class (code, read-only data, initialised data), and (3) the biggest single
// symbols. Zero-initialised memory (bss) is not part of the file and is ignored. Function metadata (.gopclntab) and
// type descriptors are reported as sections but are not attributed to packages by nm.
package main

import (
	"bufio"
	"debug/elf"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

type sym struct {
	addr uint64
	size uint64
	typ  byte
	name string
}

// pkgOf maps a symbol name to its Go package path ("runtime" for runtime internals, "(linker: go)" for linker-made data).
func pkgOf(name string) string {
	switch {
	case strings.HasPrefix(name, "go:"), strings.HasPrefix(name, "type:"), strings.HasPrefix(name, "gclocals"):
		return "(linker: " + strings.SplitN(name, ":", 2)[0] + ")"
	case strings.HasPrefix(name, "runtime."):
		return "runtime"
	}
	// The package is everything up to the first '.' after the last '/'.
	slash := strings.LastIndexByte(name, '/')
	dot := strings.IndexByte(name[slash+1:], '.')
	if dot < 0 {
		return name
	}
	return name[:slash+1+dot]
}

func class(t byte) string {
	switch t {
	case 'T', 't':
		return "text"
	case 'R', 'r':
		return "rodata"
	case 'D', 'd':
		return "data"
	}
	return ""
}

func main() {
	bin := flag.String("bin", "", "unstripped binary (go build without -s -w)")
	stripped := flag.String("stripped", "", "stripped binary whose sections are reported (default: -bin)")
	top := flag.Int("top", 40, "rows per table")
	flag.Parse()
	if *bin == "" {
		fmt.Fprintln(os.Stderr, "usage: sizeprof -bin <unstripped binary> [-stripped <stripped binary>] [-top N]")
		os.Exit(2)
	}
	if *stripped == "" {
		*stripped = *bin
	}
	if fi, err := os.Stat(*stripped); err == nil {
		fmt.Printf("file size of %s: %d bytes (%.2f MiB)\n\n", *stripped, fi.Size(), float64(fi.Size())/1048576)
	}
	sections(*stripped)
	bss := bssRanges(*bin)

	cmd := exec.Command("go", "tool", "nm", "-size", "-sort", "size", *bin)
	out, err := cmd.StdoutPipe()
	if err != nil {
		panic(err)
	}
	if err := cmd.Start(); err != nil {
		panic(err)
	}
	var syms []sym
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		n, err := strconv.ParseUint(f[1], 10, 64)
		if err != nil {
			continue
		}
		a, _ := strconv.ParseUint(f[0], 16, 64)
		syms = append(syms, sym{a, n, f[2][0], strings.Join(f[3:], " ")})
	}
	cmd.Wait()

	type agg struct{ text, rodata, data uint64 }
	pk := map[string]*agg{}
	var tot agg
	for _, s := range syms {
		c := class(s.typ)
		if c == "" || inBSS(bss, s.addr) { // zero-initialised memory takes no space in the file
			continue
		}
		p := pkgOf(s.name)
		a := pk[p]
		if a == nil {
			a = &agg{}
			pk[p] = a
		}
		switch c {
		case "text":
			a.text += s.size
			tot.text += s.size
		case "rodata":
			a.rodata += s.size
			tot.rodata += s.size
		case "data":
			a.data += s.size
			tot.data += s.size
		}
	}
	type row struct {
		name string
		a    agg
	}
	var rows []row
	for k, v := range pk {
		rows = append(rows, row{k, *v})
	}
	sum := func(r row) uint64 { return r.a.text + r.a.rodata + r.a.data }
	sort.Slice(rows, func(i, j int) bool { return sum(rows[i]) > sum(rows[j]) })
	fmt.Printf("symbols by package (symbol sizes only; pclntab and type metadata are not attributed)\n")
	fmt.Printf("%-62s %10s %10s %10s %10s\n", "package", "total", "text", "rodata", "data")
	for i, r := range rows {
		if i >= *top {
			break
		}
		fmt.Printf("%-62s %10d %10d %10d %10d\n", r.name, sum(r), r.a.text, r.a.rodata, r.a.data)
	}
	fmt.Printf("%-62s %10d %10d %10d %10d\n\n", "TOTAL", tot.text+tot.rodata+tot.data, tot.text, tot.rodata, tot.data)

	fmt.Printf("largest symbols\n")
	n := 0
	for _, s := range syms {
		if class(s.typ) == "" || inBSS(bss, s.addr) {
			continue
		}
		fmt.Printf("%10d %c %s\n", s.size, s.typ, s.name)
		if n++; n >= *top {
			break
		}
	}
}

func sections(path string) {
	f, err := elf.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Println("ELF sections (file bytes)")
	for _, s := range f.Sections {
		if s.Type == elf.SHT_NOBITS || s.Size == 0 || s.Flags&elf.SHF_ALLOC == 0 {
			continue
		}
		fmt.Printf("  %-14s %10d\n", s.Name, s.Size)
	}
	fmt.Println()
}

type rng struct{ lo, hi uint64 }

// bssRanges returns the address ranges of the NOBITS sections (go tool nm reports their symbols as data).
func bssRanges(path string) []rng {
	f, err := elf.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var r []rng
	for _, s := range f.Sections {
		if s.Type == elf.SHT_NOBITS && s.Flags&elf.SHF_ALLOC != 0 {
			r = append(r, rng{s.Addr, s.Addr + s.Size})
		}
	}
	return r
}

func inBSS(r []rng, a uint64) bool {
	for _, x := range r {
		if a >= x.lo && a < x.hi {
			return true
		}
	}
	return false
}
