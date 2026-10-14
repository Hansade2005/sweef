package engine

// wasm_bin.go — the little of the WebAssembly binary format the JS API needs outside wazero: reading the
// type/import/function/export/custom sections (Module.imports/exports/customSections, call signatures) and
// assembling the tiny provider and aggregator modules that let a WebAssembly.Memory/Table/Global created from
// JS, or an export of one instance, be imported by another. Validation of code is wazero's job.

import (
	"errors"
	"fmt"
	"strings"
)

const (
	wvI32  byte = 0x7f
	wvI64  byte = 0x7e
	wvF32  byte = 0x7d
	wvF64  byte = 0x7c
	wvV128 byte = 0x7b
	wvFunc byte = 0x70
	wvExt  byte = 0x6f

	wkFunc   byte = 0
	wkTable  byte = 1
	wkMemory byte = 2
	wkGlobal byte = 3
	wkTag    byte = 4
)

type wFuncType struct {
	params, results []byte
	raw             []byte // the 0x60 form exactly as it appears in the type section
}

func (t wFuncType) sig() string { return string(t.params) + ">" + string(t.results) }

type wImport struct {
	module, name string
	kind         byte
	desc         []byte // raw descriptor after the kind byte
	typeIdx      int    // functions
}

type wExport struct {
	name string
	kind byte
	idx  int
}

type wCustom struct {
	name string
	data []byte
}

type wInfo struct {
	types       []wFuncType
	imports     []wImport
	exports     []wExport
	funcType    []int // type index of every function (imports first)
	nImpFunc    int
	nImpTable   int
	nImpMemory  int
	nImpGlobal  int
	customs     []wCustom
	hasMemory64 bool
	// definesShared: the module's own memory section declares a shared memory (threads proposal)
	definesShared bool
}

type wReader struct {
	b   []byte
	off int
	err error
}

func (r *wReader) fail(msg string) {
	if r.err == nil {
		r.err = errors.New(msg)
	}
}

func (r *wReader) byte() byte {
	if r.off >= len(r.b) {
		r.fail("unexpected end")
		return 0
	}
	c := r.b[r.off]
	r.off++
	return c
}

func (r *wReader) u32() uint32 {
	var v uint64
	var shift uint
	for i := 0; i < 5; i++ {
		c := r.byte()
		v |= uint64(c&0x7f) << shift
		if c&0x80 == 0 {
			return uint32(v)
		}
		shift += 7
	}
	r.fail("invalid LEB128")
	return 0
}

func (r *wReader) bytes(n int) []byte {
	if n < 0 || r.off+n > len(r.b) {
		r.fail("unexpected end")
		return nil
	}
	s := r.b[r.off : r.off+n]
	r.off += n
	return s
}

func (r *wReader) name() string {
	n := int(r.u32())
	return string(r.bytes(n))
}

// limits reads a limits descriptor and returns its flag byte (bit 0: has maximum, bit 1: shared, bit 2: memory64).
func (r *wReader) limits(info *wInfo) byte {
	flag := r.byte()
	if flag&4 != 0 {
		info.hasMemory64 = true
	}
	r.u32()
	if flag&1 != 0 {
		r.u32()
	}
	return flag
}

func parseWasm(b []byte) (*wInfo, error) {
	if len(b) < 4 || string(b[:4]) != "\x00asm" {
		got := b
		if len(got) > 4 {
			got = got[:4]
		}
		var parts []string
		for _, c := range got {
			parts = append(parts, fmt.Sprintf("%02x", c))
		}
		if len(got) == 0 {
			return nil, errors.New("BufferSource argument is empty")
		}
		return nil, fmt.Errorf("expected magic word 00 61 73 6d, found %s @+0", strings.Join(parts, " "))
	}
	if len(b) < 8 || b[4] != 1 || b[5] != 0 || b[6] != 0 || b[7] != 0 {
		return nil, errors.New("expected version 01 00 00 00 @+4")
	}
	info := &wInfo{}
	r := &wReader{b: b, off: 8}
	var funcSec []uint32
	for r.off < len(b) && r.err == nil {
		id := r.byte()
		size := int(r.u32())
		start := r.off
		body := r.bytes(size)
		if r.err != nil {
			return nil, fmt.Errorf("section (code %d) extends past end of the module (length %d, remaining bytes %d) @+%d", id, size, len(b)-start, start)
		}
		s := &wReader{b: body}
		switch id {
		case 0:
			name := s.name()
			if s.err == nil {
				info.customs = append(info.customs, wCustom{name: name, data: body[s.off:]})
			}
		case 1:
			n := int(s.u32())
			for i := 0; i < n && s.err == nil; i++ {
				from := s.off
				if f := s.byte(); f != 0x60 {
					s.fail(fmt.Sprintf("unsupported type form 0x%02x", f))
					break
				}
				var t wFuncType
				np := int(s.u32())
				t.params = append([]byte(nil), s.bytes(np)...)
				nr := int(s.u32())
				t.results = append([]byte(nil), s.bytes(nr)...)
				t.raw = body[from:s.off]
				info.types = append(info.types, t)
			}
		case 2:
			n := int(s.u32())
			for i := 0; i < n && s.err == nil; i++ {
				im := wImport{module: s.name(), name: s.name(), kind: s.byte(), typeIdx: -1}
				from := s.off
				switch im.kind {
				case wkFunc:
					im.typeIdx = int(s.u32())
					if im.typeIdx >= len(info.types) {
						s.fail("type index out of bounds")
					}
					info.funcType = append(info.funcType, im.typeIdx)
					info.nImpFunc++
				case wkTable:
					s.byte()
					s.limits(info)
					info.nImpTable++
				case wkMemory:
					s.limits(info)
					info.nImpMemory++
				case wkGlobal:
					s.byte()
					s.byte()
					info.nImpGlobal++
				case wkTag:
					s.byte()
					s.u32()
				default:
					s.fail(fmt.Sprintf("invalid import kind %d", im.kind))
				}
				if s.err == nil {
					im.desc = body[from:s.off]
					info.imports = append(info.imports, im)
				}
			}
		case 3:
			n := int(s.u32())
			for i := 0; i < n && s.err == nil; i++ {
				funcSec = append(funcSec, s.u32())
			}
		case 5:
			n := int(s.u32())
			for i := 0; i < n && s.err == nil; i++ {
				if s.limits(info)&2 != 0 {
					info.definesShared = true
				}
			}
		case 7:
			n := int(s.u32())
			for i := 0; i < n && s.err == nil; i++ {
				info.exports = append(info.exports, wExport{name: s.name(), kind: s.byte(), idx: int(s.u32())})
			}
		}
		if s.err != nil {
			return nil, fmt.Errorf("section (code %d): %v @+%d", id, s.err, start)
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	for _, ti := range funcSec {
		if int(ti) >= len(info.types) {
			return nil, errors.New("function type index out of bounds")
		}
		info.funcType = append(info.funcType, int(ti))
	}
	return info, nil
}

// funcIndexOfImport maps "the i-th import" to its index in the function index space (functions are numbered in
// import order).
func (info *wInfo) funcIndexOfImport(i int) int {
	n := 0
	for k := 0; k < i; k++ {
		if info.imports[k].kind == wkFunc {
			n++
		}
	}
	return n
}

func kindName(k byte) string {
	switch k {
	case wkFunc:
		return "function"
	case wkTable:
		return "table"
	case wkMemory:
		return "memory"
	case wkGlobal:
		return "global"
	case wkTag:
		return "tag"
	}
	return "unknown"
}

// ---- assembling ---------------------------------------------------------------

func uleb(v uint64) []byte {
	var out []byte
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			out = append(out, c|0x80)
			continue
		}
		return append(out, c)
	}
}

func sleb(v int64) []byte {
	var out []byte
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if (v == 0 && c&0x40 == 0) || (v == -1 && c&0x40 != 0) {
			return append(out, c)
		}
		out = append(out, c|0x80)
	}
}

func wname(s string) []byte { return append(uleb(uint64(len(s))), s...) }

func wsection(id byte, body []byte) []byte {
	out := []byte{id}
	out = append(out, uleb(uint64(len(body)))...)
	return append(out, body...)
}

func wvec(items [][]byte) []byte {
	out := uleb(uint64(len(items)))
	for _, it := range items {
		out = append(out, it...)
	}
	return out
}

var wasmHeader = []byte{0, 'a', 's', 'm', 1, 0, 0, 0}

func wlimits(min uint32, max int64) []byte {
	if max < 0 {
		return append([]byte{0}, uleb(uint64(min))...)
	}
	out := append([]byte{1}, uleb(uint64(min))...)
	return append(out, uleb(uint64(max))...)
}

// asmMemory is a module exporting one memory as "m".
func asmMemory(min uint32, max int64) []byte {
	b := append([]byte(nil), wasmHeader...)
	b = append(b, wsection(5, wvec([][]byte{wlimits(min, max)}))...)
	b = append(b, wsection(7, wvec([][]byte{append(wname("m"), wkMemory, 0)}))...)
	return b
}

// asmSharedMemory is a module exporting one shared memory (threads proposal) as "m".
func asmSharedMemory(min uint32, max uint32) []byte {
	b := append([]byte(nil), wasmHeader...)
	lim := append([]byte{3}, uleb(uint64(min))...)
	lim = append(lim, uleb(uint64(max))...)
	b = append(b, wsection(5, wvec([][]byte{lim}))...)
	b = append(b, wsection(7, wvec([][]byte{append(wname("m"), wkMemory, 0)}))...)
	return b
}

// asmTable is a module exporting one funcref table as "t".
func asmTable(min uint32, max int64) []byte {
	b := append([]byte(nil), wasmHeader...)
	b = append(b, wsection(4, wvec([][]byte{append([]byte{wvFunc}, wlimits(min, max)...)}))...)
	b = append(b, wsection(7, wvec([][]byte{append(wname("t"), wkTable, 0)}))...)
	return b
}

// asmGlobal is a module exporting one global as "g". init holds the value bits (f32/f64 as IEEE bits).
func asmGlobal(vt byte, mutable bool, init uint64) []byte {
	var expr []byte
	switch vt {
	case wvI32:
		expr = append([]byte{0x41}, sleb(int64(int32(uint32(init))))...)
	case wvI64:
		expr = append([]byte{0x42}, sleb(int64(init))...)
	case wvF32:
		expr = []byte{0x43, byte(init), byte(init >> 8), byte(init >> 16), byte(init >> 24)}
	case wvF64:
		expr = []byte{0x44, byte(init), byte(init >> 8), byte(init >> 16), byte(init >> 24), byte(init >> 32), byte(init >> 40), byte(init >> 48), byte(init >> 56)}
	}
	expr = append(expr, 0x0b)
	m := byte(0)
	if mutable {
		m = 1
	}
	b := append([]byte(nil), wasmHeader...)
	b = append(b, wsection(6, wvec([][]byte{append([]byte{vt, m}, expr...)}))...)
	b = append(b, wsection(7, wvec([][]byte{append(wname("g"), wkGlobal, 0)}))...)
	return b
}

type aggImport struct {
	from, field string // the provider
	as          string // exported name
	kind        byte
	desc        []byte
	ftype       []byte // raw functype (functions)
}

// asmAggregator re-exports a set of provider items under the names a guest module imports them by. The import
// descriptors are copied from the guest's own, so wazero's limit and type checks happen between provider and
// aggregator, and the guest's check against the aggregator passes trivially.
func asmAggregator(items []aggImport) []byte {
	var types [][]byte
	typeIdx := map[string]int{}
	var imports, exports [][]byte
	counts := map[byte]int{}
	for _, it := range items {
		imp := append(wname(it.from), wname(it.field)...)
		imp = append(imp, it.kind)
		if it.kind == wkFunc {
			k := string(it.ftype)
			ix, ok := typeIdx[k]
			if !ok {
				ix = len(types)
				typeIdx[k] = ix
				types = append(types, it.ftype)
			}
			imp = append(imp, uleb(uint64(ix))...)
		} else {
			imp = append(imp, it.desc...)
		}
		imports = append(imports, imp)
		exports = append(exports, append(append(wname(it.as), it.kind), uleb(uint64(counts[it.kind]))...))
		counts[it.kind]++
	}
	b := append([]byte(nil), wasmHeader...)
	if len(types) > 0 {
		b = append(b, wsection(1, wvec(types))...)
	}
	b = append(b, wsection(2, wvec(imports))...)
	b = append(b, wsection(7, wvec(exports))...)
	return b
}
