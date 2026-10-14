package engine

// wasm_qjs.go — zero-copy ArrayBuffer access for the WebAssembly bridge.
//
// modernc.org/quickjs has no public way to hand an ArrayBuffer to Go or to create one around Go memory: a JS
// ArrayBuffer crossing the host boundary arrives as a JSON string. WebAssembly.Memory.buffer has to be an
// ArrayBuffer that IS the wasm linear memory (emscripten, wasm-bindgen and every other toolchain build typed
// array views over it and expect wasm stores to be visible through them, and the other way round), so this file
// reaches below the public API:
//
//   - the QuickJS C runtime lives in modernc.org/libquickjs, a transpiled C library whose pointers are real
//     process addresses, so JS_GetArrayBuffer returns a pointer Go can read and JS_NewArrayBuffer accepts a
//     pointer into Go memory (with no free function, so QuickJS never frees it);
//   - the (tls, ctx) pair those C entry points need and the raw JSValue inside a quickjs.Value are unexported,
//     so they are read with reflect + unsafe. The layout is checked once (wasmQJS) and the feature switches
//     itself off, with a failing test, if a quickjs upgrade changes it.
//
// Lifetime rule: the Go slice behind an external ArrayBuffer is kept referenced by wasmMem until the buffer is
// detached (on memory.grow or instance close), so the garbage collector can never reuse it under the VM.

import (
	"fmt"
	"reflect"
	"sync"
	"time"
	"unsafe"

	"modernc.org/libc"
	lib "modernc.org/libquickjs"
	"modernc.org/quickjs"
)

type qjsRaw struct {
	tls *libc.TLS
	ctx uintptr
}

var (
	qjsOnce    sync.Once
	qjsErr     error
	qjsVMCtx   uintptr // offset of VM.cContext
	qjsVMRT    uintptr // offset of VM.runtime
	qjsRTTLS   uintptr // offset of runtime.tls (inside the pointed-to struct)
	qjsValueV  uintptr // offset of Value.v
	qjsValueVM uintptr // offset of Value.vm
	qjsVMInt   uintptr // offset of VM.interruptData
)

// wasmQJS reports whether the unsafe bridge matches the linked quickjs version.
func wasmQJS() error {
	qjsOnce.Do(func() {
		vt := reflect.TypeOf(quickjs.VM{})
		f, ok := vt.FieldByName("cContext")
		if !ok || f.Type.Kind() != reflect.Uintptr {
			qjsErr = fmt.Errorf("quickjs.VM.cContext not found")
			return
		}
		qjsVMCtx = f.Offset
		f, ok = vt.FieldByName("runtime")
		if !ok || f.Type.Kind() != reflect.Pointer {
			qjsErr = fmt.Errorf("quickjs.VM.runtime not found")
			return
		}
		qjsVMRT = f.Offset
		rf, ok := f.Type.Elem().FieldByName("tls")
		if !ok || rf.Type != reflect.TypeOf((*libc.TLS)(nil)) {
			qjsErr = fmt.Errorf("quickjs runtime.tls not found")
			return
		}
		qjsRTTLS = rf.Offset
		valT := reflect.TypeOf(quickjs.Value{})
		vf, ok := valT.FieldByName("v")
		if !ok || vf.Type != reflect.TypeOf(lib.TJSValue{}) {
			qjsErr = fmt.Errorf("quickjs.Value.v not found")
			return
		}
		qjsValueV = vf.Offset
		vm, ok := valT.FieldByName("vm")
		if !ok || vm.Type != reflect.TypeOf((*quickjs.VM)(nil)) {
			qjsErr = fmt.Errorf("quickjs.Value.vm not found")
			return
		}
		qjsValueVM = vm.Offset
		f, ok = vt.FieldByName("interruptData")
		if !ok || f.Type.Kind() != reflect.Uintptr {
			qjsErr = fmt.Errorf("quickjs.VM.interruptData not found")
			return
		}
		qjsVMInt = f.Offset
		qjsErr = qjsSelfTest()
	})
	return qjsErr
}

func rawOf(vm *quickjs.VM) qjsRaw {
	p := unsafe.Pointer(vm)
	rt := *(*unsafe.Pointer)(unsafe.Add(p, qjsVMRT))
	return qjsRaw{
		tls: *(**libc.TLS)(unsafe.Add(rt, qjsRTTLS)),
		ctx: *(*uintptr)(unsafe.Add(p, qjsVMCtx)),
	}
}

// qjsInterrupt mirrors modernc.org/quickjs's unexported interruptData (the struct the VM's C interrupt handler
// reads); the self-test below proves the mirror still matches.
type qjsInterrupt struct {
	interrupt int32
	timeLimit int64
}

func (r qjsRaw) timeLimit(vm *quickjs.VM) *int64 {
	base := *(*unsafe.Pointer)(unsafe.Add(unsafe.Pointer(vm), qjsVMInt))
	return (*int64)(unsafe.Add(base, unsafe.Offsetof(qjsInterrupt{}.timeLimit)))
}

// forgive moves the VM's script-timeout deadline forward by d (see wasmHost.guard). interruptData is
// {interrupt int32; timeLimit int64}; the self-test checks the layout.
func (r qjsRaw) forgive(vm *quickjs.VM, d time.Duration) {
	if d <= 0 {
		return
	}
	limit := r.timeLimit(vm)
	if *limit != 0 {
		*limit += int64(d)
	}
}

func jsvOf(v *quickjs.Value) lib.TJSValue {
	return *(*lib.TJSValue)(unsafe.Add(unsafe.Pointer(v), qjsValueV))
}

func mkValue(vm *quickjs.VM, jv lib.TJSValue) quickjs.Value {
	var v quickjs.Value
	*(**quickjs.VM)(unsafe.Add(unsafe.Pointer(&v), qjsValueVM)) = vm
	*(*lib.TJSValue)(unsafe.Add(unsafe.Pointer(&v), qjsValueV)) = jv
	return v
}

// uptr turns an address handed out by the C side into a pointer. The address is outside the Go heap (the C
// heap) or inside a slice the caller keeps alive, so vet's uintptr->Pointer warning does not apply.
func uptr(a uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&a)) }

// bufferBytes returns the bytes of an ArrayBuffer value without copying. They are valid until the next call
// into JavaScript (which could detach the buffer). ok is false for anything that is not a live ArrayBuffer.
func (r qjsRaw) bufferBytes(v *quickjs.Value) (b []byte, ok bool) {
	buf := r.tls.Alloc(8)
	defer r.tls.Free(8)
	p := lib.XJS_GetArrayBuffer(r.tls, r.ctx, buf, jsvOf(v))
	n := *(*uint64)(uptr(buf))
	if p == 0 {
		// an empty buffer may have a nil data pointer; a pending exception means it was not a buffer at all
		r.clearException()
		return nil, n == 0
	}
	return unsafe.Slice((*byte)(uptr(p)), int(n)), true
}

func (r qjsRaw) clearException() {
	ex := lib.XJS_GetException(r.tls, r.ctx)
	lib.XFreeValue(r.tls, r.ctx, ex)
}

// newExternalBuffer wraps b (which the caller keeps alive) in an ArrayBuffer that aliases it.
func (r qjsRaw) newExternalBuffer(vm *quickjs.VM, b []byte) quickjs.Value {
	jv := lib.XJS_NewArrayBuffer(r.tls, r.ctx, uintptr(unsafe.Pointer(unsafe.SliceData(b))), lib.Tsize_t(len(b)), 0, 0, 0)
	return mkValue(vm, jv)
}

// newCopyBuffer returns a fresh ArrayBuffer holding a copy of b.
func (r qjsRaw) newCopyBuffer(vm *quickjs.VM, b []byte) quickjs.Value {
	var p uintptr
	if len(b) > 0 {
		p = uintptr(unsafe.Pointer(unsafe.SliceData(b)))
	}
	jv := lib.XJS_NewArrayBufferCopy(r.tls, r.ctx, p, lib.Tsize_t(len(b)))
	return mkValue(vm, jv)
}

func (r qjsRaw) detach(v *quickjs.Value) {
	lib.XJS_DetachArrayBuffer(r.tls, r.ctx, jsvOf(v))
}

// qjsSelfTest exercises the bridge on a scratch VM: Go->JS aliasing, JS->Go reading, detach.
func qjsSelfTest() error {
	vm, err := quickjs.NewVM()
	if err != nil {
		return err
	}
	defer vm.Close()
	r := rawOf(vm)
	if r.tls == nil || r.ctx == 0 {
		return fmt.Errorf("quickjs raw handles are empty")
	}
	backing := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	ab := r.newExternalBuffer(vm, backing)
	if err := vm.RegisterFunc("__selftest_probe", func(v quickjs.Value) any {
		b, ok := r.bufferBytes(&v)
		if !ok || len(b) != 8 {
			return -1
		}
		b[0] = 99 // write through: must be visible in backing
		return int(b[7])
	}, false); err != nil {
		return err
	}
	g, err := vm.EvalValue("globalThis", quickjs.EvalGlobal)
	if err != nil {
		return err
	}
	defer g.Free()
	atom, err := vm.NewAtom("buf")
	if err != nil {
		return err
	}
	if err := vm.SetPropertyValue(g, atom, ab); err != nil {
		return err
	}
	backing[1] = 42
	got, err := vm.Eval(`(function(){ globalThis.u = new Uint8Array(buf); var seen = u[1]; u[2] = 77; return seen + ':' + __selftest_probe(buf) + ':' + u[0]; })()`, quickjs.EvalGlobal)
	if err != nil {
		return err
	}
	if got != "42:8:99" || backing[2] != 77 || backing[0] != 99 {
		return fmt.Errorf("aliasing self-test failed: %v %v", got, backing)
	}
	// the script-timeout clock moves when asked to
	vm.SetEvalTimeout(time.Hour)
	if _, err := vm.Eval("1", quickjs.EvalGlobal); err != nil {
		return err
	}
	lim := r.timeLimit(vm)
	if d := time.Until(time.Unix(0, *lim)); d < 59*time.Minute || d > 61*time.Minute {
		return fmt.Errorf("interrupt deadline layout self-test failed (%v)", d)
	}
	r.forgive(vm, 10*time.Minute)
	if d := time.Until(time.Unix(0, *lim)); d < 69*time.Minute || d > 71*time.Minute {
		return fmt.Errorf("interrupt deadline forgive self-test failed (%v)", d)
	}
	vm.SetEvalTimeout(0)
	r.detach(&ab)
	got, err = vm.Eval(`u.length + ':' + buf.byteLength`, quickjs.EvalGlobal)
	if err != nil || got != "0:0" {
		return fmt.Errorf("detach self-test failed: %v %v", got, err)
	}
	return nil
}
