//go:build amd64 || arm64

package widevinehost

import (
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
)

// Memory handed to C must not move or be collected while the CDM may use it. Go's heap never moves objects, so it
// is enough to keep a reference: everything C can see lives in this registry until released. (Locals are not
// enough: a callback from the CDM can run Go code on the calling goroutine, which can grow and move the stack.)

var pins sync.Map // uintptr (address) -> any

func pin(addr uintptr, v any) { pins.Store(addr, v) }
func unpin(addr uintptr)      { pins.Delete(addr) }

func lookup[T any](addr uintptr) (T, bool) {
	v, ok := pins.Load(addr)
	if !ok {
		var zero T
		return zero, false
	}
	t, ok := v.(T)
	return t, ok
}

// ptrAt converts an address owned by C (or by the pin registry) to an unsafe.Pointer without tripping go vet's
// unsafeptr check, which is about Go pointers round-tripped through uintptr and does not apply here.
func ptrAt(a uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&a)) }

func readPtr(a uintptr) uintptr    { return *(*uintptr)(ptrAt(a)) }
func readU32(a uintptr) uint32     { return *(*uint32)(ptrAt(a)) }
func writeU32(a uintptr, v uint32) { *(*uint32)(ptrAt(a)) = v }

// cBytes copies n bytes from C memory (nil for a null pointer or n == 0).
func cBytes(p uintptr, n uint32) []byte {
	if p == 0 || n == 0 {
		return nil
	}
	return append([]byte(nil), unsafe.Slice((*byte)(ptrAt(p)), int(n))...)
}

func cString(p uintptr, n uint32) string { return string(cBytes(p, n)) }

// arena allocates C-visible memory for the duration of one call into the CDM.
type arena struct{ addrs []uintptr }

func (a *arena) keep(addr uintptr, v any) uintptr {
	pin(addr, v)
	a.addrs = append(a.addrs, addr)
	return addr
}

// bytes returns the address of a heap copy of b (never nil, so empty input still yields a valid pointer; the
// caller passes size 0 where the CDM API says "empty").
func (a *arena) bytes(b []byte) uintptr {
	cp := make([]byte, len(b)+1)
	copy(cp, b)
	return a.keep(uintptr(unsafe.Pointer(&cp[0])), cp)
}

func (a *arena) release() {
	for _, p := range a.addrs {
		unpin(p)
	}
	a.addrs = nil
}

// newObj allocates a zeroed T on the heap and pins it.
func newObj[T any](a *arena) (*T, uintptr) {
	p := new(T)
	addr := uintptr(unsafe.Pointer(p))
	a.keep(addr, p)
	return p, addr
}

// vcall calls virtual method slot of the C++ object obj: obj is the this pointer, the table pointer is its first
// word.
func vcall(obj uintptr, slot int, args ...uintptr) uintptr {
	vtbl := readPtr(obj)
	fn := readPtr(vtbl + uintptr(slot)*unsafe.Sizeof(uintptr(0)))
	all := make([]uintptr, 0, len(args)+1)
	all = append(all, obj)
	all = append(all, args...)
	r1, _, _ := purego.SyscallN(fn, all...)
	return r1
}

func b2u(b bool) uintptr {
	if b {
		return 1
	}
	return 0
}

// nextID hands out promise ids (the CDM API wants nonzero ids; 0 is never used).
type idGen struct{ n atomic.Uint32 }

func (g *idGen) next() uint32 {
	for {
		if v := g.n.Add(1); v != 0 {
			return v
		}
	}
}
