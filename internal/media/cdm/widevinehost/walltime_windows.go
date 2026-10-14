//go:build (amd64 || arm64) && windows

package widevinehost

import (
	"encoding/binary"
	"errors"
	"math"
	"runtime"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// UNVERIFIED: compiled for Windows but never run there.
//
// syscall.NewCallback has no floating point support: it can neither return a double (xmm0) nor read one from
// xmm0..xmm3. Two cdm::Host methods need that, so on windows/amd64 a few bytes of machine code are generated at
// start up:
//
//   - GetCurrentWallTime: a stub that loads a double from a memory cell into xmm0 and returns.
//     48 B8 <imm64>      mov rax, &cell
//     F2 0F 10 00        movsd xmm0, [rax]
//     C3                 ret
//     The cell is refreshed before every call into the CDM and every 100 ms.
//   - OnExpirationChange(this, id, id_size, double): the double is the fourth argument, so Windows x64 passes it in
//     xmm3. A stub copies it to r9 (where the fourth integer argument would be) and jumps to an ordinary
//     syscall.NewCallback target that reads r9 as raw bits.
//     66 49 0F 7E D9    movq r9, xmm3
//     48 B8 <imm64>     mov rax, target
//     FF E0             jmp rax
//
// windows/arm64 would need different code and is not supported.

var wallCell uint64 // math.Float64bits of the last wall time; a package variable, so its address is stable

func nowSeconds() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func touchWallTime() { atomic.StoreUint64(&wallCell, math.Float64bits(nowSeconds())) }

func startWallClock(stop <-chan struct{}) {
	touchWallTime()
	go func() {
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				touchWallTime()
			case <-stop:
				return
			}
		}
	}()
}

var errNoWindowsARM64 = errors.New("widevinehost: the floating point stubs are only implemented for windows/amd64")

func execMemory(code []byte) (uintptr, error) {
	p, err := windows.VirtualAlloc(0, uintptr(len(code)), windows.MEM_COMMIT|windows.MEM_RESERVE, windows.PAGE_EXECUTE_READWRITE)
	if err != nil {
		return 0, err
	}
	copy(unsafe.Slice((*byte)(ptrAt(p)), len(code)), code)
	return p, nil
}

func imm64(v uintptr) []byte {
	b := make([]byte, 8)
	binary.LittleEndian.PutUint64(b, uint64(v))
	return b
}

func newWallTimeCallback() (uintptr, error) {
	if runtime.GOARCH != "amd64" {
		return 0, errNoWindowsARM64
	}
	touchWallTime()
	code := []byte{0x48, 0xB8}
	code = append(code, imm64(uintptr(unsafe.Pointer(&wallCell)))...)
	code = append(code, 0xF2, 0x0F, 0x10, 0x00, 0xC3)
	return execMemory(code)
}

func newExpirationCallback(h func(this, sid, size uintptr, t float64)) (uintptr, error) {
	if runtime.GOARCH != "amd64" {
		return 0, errNoWindowsARM64
	}
	target := syscall.NewCallback(func(this, sid, size, bits uintptr) uintptr {
		h(this, sid, size, math.Float64frombits(uint64(bits)))
		return 0
	})
	code := []byte{0x66, 0x49, 0x0F, 0x7E, 0xD9, 0x48, 0xB8}
	code = append(code, imm64(target)...)
	code = append(code, 0xFF, 0xE0)
	return execMemory(code)
}
