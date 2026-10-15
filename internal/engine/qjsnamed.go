//go:build !(386 || arm)

package engine

// qjsnamed.go: compiling a module under its URL.
//
// modernc.org/quickjs names every source it evaluates or compiles "<eval>". For a module that is more than a
// cosmetic problem: the name is the module's identity and its base URL, so a dynamic import("./chunk.js") in an
// entry module resolved against whatever script happened to run last (the loader's fallback), import.meta.url
// was "<eval>" (webpack's automatic publicPath gives up: GitHub's code view), and a module imported again by
// one of its chunks ran twice. QuickJS itself takes a file name; the wrapper only hides it. compileNamed calls
// the C API with the name, reaching the VM's context and thread state through their (reflect-checked) field
// offsets. Bytecode keeps the name, so the replay from the bytecode cache (EvalBytecode) is named too.

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"unsafe"

	"modernc.org/libc"
	lib "modernc.org/libquickjs"
	"modernc.org/quickjs"
)

var (
	qjsOffOnce          sync.Once
	qjsCtxOff, qjsRtOff uintptr
	qjsTLSOff           uintptr
	qjsOffOK            bool
)

func qjsOffsets() bool {
	qjsOffOnce.Do(func() {
		vt := reflect.TypeOf(quickjs.VM{})
		c, ok1 := vt.FieldByName("cContext")
		r, ok2 := vt.FieldByName("runtime")
		if !ok1 || !ok2 || c.Type.Kind() != reflect.Uintptr || r.Type.Kind() != reflect.Pointer {
			return
		}
		tf, ok3 := r.Type.Elem().FieldByName("tls")
		if !ok3 || tf.Type != reflect.TypeOf((*libc.TLS)(nil)) {
			return
		}
		qjsCtxOff, qjsRtOff, qjsTLSOff, qjsOffOK = c.Offset, r.Offset, tf.Offset, true
	})
	return qjsOffOK
}

// runModuleNamed compiles src as the module name in vm and runs it, as EvalBytecode does for a module (resolve,
// import.meta, evaluate, await), returning its bytecode for the cache. handled is false when it could not
// compile (a syntax error, or the C API is out of reach): nothing ran, and the caller compiles the usual way,
// which reports the error. The compile happens in vm itself because compiling a module resolves its static
// imports through the VM's loader; the compiled module is then run directly rather than read back from its
// bytecode, which would register a second module of the same name.
func runModuleNamed(vm *quickjs.VM, src, name string) (bc []byte, err error, handled bool) {
	if vm == nil || !qjsOffsets() {
		return nil, nil, false
	}
	base := unsafe.Pointer(vm)
	ctx := *(*uintptr)(unsafe.Add(base, qjsCtxOff))
	rt := *(*unsafe.Pointer)(unsafe.Add(base, qjsRtOff))
	if ctx == 0 || rt == nil {
		return nil, nil, false
	}
	tls := *(**libc.TLS)(unsafe.Add(rt, qjsTLSOff))
	if tls == nil {
		return nil, nil, false
	}
	ps, cerr := libc.CString(src)
	if cerr != nil {
		return nil, nil, false
	}
	defer libc.Xfree(tls, ps)
	pn, cerr := libc.CString(name)
	if cerr != nil {
		return nil, nil, false
	}
	defer libc.Xfree(tls, pn)
	v := lib.XJS_Eval(tls, ctx, ps, libc.Tsize_t(len(src)), pn, int32(quickjs.EvalModule|lib.MJS_EVAL_FLAG_COMPILE_ONLY))
	if v.Ftag == lib.EJS_TAG_EXCEPTION {
		lib.XFreeValue(tls, ctx, lib.XJS_GetException(tls, ctx))
		return nil, nil, false
	}
	// bytecode for the next visit (a fresh VM reads it back once, named)
	usize := int(unsafe.Sizeof(libc.Tsize_t(0)))
	plen := tls.Alloc(usize)
	if p := lib.XJS_WriteObject(tls, ctx, plen, v, int32(lib.MJS_WRITE_OBJ_BYTECODE)); p != 0 {
		var n uint64
		for i, b := range libc.GoBytes(plen, usize) { // size_t, little-endian on every target
			n |= uint64(b) << (8 * i)
		}
		bc = append([]byte(nil), libc.GoBytes(p, int(n))...) // GoBytes aliases C memory: copy before freeing it
		lib.Xjs_free(tls, ctx, p)
	}
	tls.Free(usize)
	// run it the way EvalBytecodeValue runs a module
	if lib.XJS_ResolveModule(tls, ctx, v) < 0 {
		lib.XFreeValue(tls, ctx, v)
		return bc, takeException(vm, tls, ctx), true
	}
	lib.Xjs_module_set_import_meta(tls, ctx, v, 0, 1)
	_, _ = vm.Eval("0", quickjs.EvalGlobal) // arms the VM's evaluation timeout from now, as its own Eval* calls do
	r := lib.XJS_EvalFunction(tls, ctx, v)  // takes v
	r = lib.Xjs_std_await(tls, ctx, r)
	if r.Ftag == lib.EJS_TAG_EXCEPTION {
		lib.XFreeValue(tls, ctx, r)
		return bc, takeException(vm, tls, ctx), true
	}
	lib.XFreeValue(tls, ctx, r)
	return bc, nil, true
}

// takeException turns the pending exception into the *quickjs.Error the public API returns, by rethrowing it
// from script.
func takeException(vm *quickjs.VM, tls *libc.TLS, ctx uintptr) error {
	exc := lib.XJS_GetException(tls, ctx)
	g := lib.XJS_GetGlobalObject(tls, ctx)
	key, err := libc.CString("__swModErr")
	if err != nil {
		lib.XFreeValue(tls, ctx, exc)
		lib.XFreeValue(tls, ctx, g)
		return errors.New("module evaluation failed")
	}
	lib.XJS_SetPropertyStr(tls, ctx, g, key, exc) // takes exc
	libc.Xfree(tls, key)
	lib.XFreeValue(tls, ctx, g)
	_, rerr := vm.Eval("(function(){var e=globalThis.__swModErr; delete globalThis.__swModErr; throw e;})()", quickjs.EvalGlobal)
	if rerr == nil {
		rerr = errors.New("module evaluation failed")
	}
	return rerr
}

// withImportMetaURL gives a module's import.meta its url. QuickJS leaves import.meta empty unless the host fills
// it, and nothing did: code that derives its base from import.meta.url (webpack's automatic publicPath, Vite's
// asset URLs, new URL('./x', import.meta.url)) failed. The assignment goes in front of the module body on the
// first line, so line numbers in stack traces stay right.
func withImportMetaURL(src, url string) string {
	if strings.HasPrefix(src, "#!") {
		return src
	}
	q, _ := json.Marshal(url) // a JS string literal (strconv.Quote may write \U escapes)
	return "import.meta.url=" + string(q) + ";" + src
}

// maxPendingImports bounds how many queued import() jobs one loader call looks at.
const maxPendingImports = 256

// pendingDynamicImports lists the (base, specifier) of the import() calls whose jobs are queued in vm's runtime
// and not yet run. QuickJS loads a dynamic import from its job, one job at a time, so the chunks of a
// Promise.all over import() calls (how webpack and rspack load a route) would download one after another; the
// loader prefetches these instead. A dynamic-import job is the one whose arguments are (resolve, reject,
// base string, specifier string, attributes); other jobs never carry two strings there.
func pendingDynamicImports(vm *quickjs.VM) [][2]string {
	if vm == nil || !qjsOffsets() {
		return nil
	}
	base := unsafe.Pointer(vm)
	ctx := *(*uintptr)(unsafe.Add(base, qjsCtxOff))
	r := *(*unsafe.Pointer)(unsafe.Add(base, qjsRtOff))
	if ctx == 0 || r == nil {
		return nil
	}
	tls := *(**libc.TLS)(unsafe.Add(r, qjsTLSOff))
	crt := (*lib.TJSContext)(cmem(ctx)).Frt
	if tls == nil || crt == 0 {
		return nil
	}
	head := crt + unsafe.Offsetof(lib.TJSRuntime{}.Fjob_list)
	entSize := unsafe.Sizeof(lib.TJSJobEntry{})
	valSize := unsafe.Sizeof(lib.TJSValue{})
	str := func(v lib.TJSValue) (string, bool) {
		if v.Ftag != lib.EJS_TAG_STRING {
			return "", false
		}
		p := lib.XJS_ToCStringLen2(tls, ctx, 0, v, 0)
		if p == 0 {
			return "", false
		}
		s := libc.GoString(p)
		lib.XJS_FreeCString(tls, ctx, p)
		return s, true
	}
	var out [][2]string
	n := 0
	for p := (*lib.Tlist_head)(cmem(head)).Fnext; p != 0 && p != head && n < maxPendingImports; p = (*lib.Tlist_head)(cmem(p)).Fnext {
		n++
		e := (*lib.TJSJobEntry)(cmem(p))
		if e.Fargc != 5 || e.Frealm != ctx {
			continue
		}
		argv := p + entSize
		b, ok1 := str(*(*lib.TJSValue)(cmem(argv + 2*valSize)))
		spec, ok2 := str(*(*lib.TJSValue)(cmem(argv + 3*valSize)))
		if ok1 && ok2 {
			out = append(out, [2]string{b, spec})
		}
	}
	return out
}

// cmem is the address of QuickJS's C-heap memory (allocated by modernc.org/libc, never moved or collected by Go)
// as a pointer to read it through.
func cmem(p uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&p)) }

// preludeStripsSource reports whether the prelude is compiled without its source text (stripSource works here).
func preludeStripsSource() bool { return qjsOffsets() }

// stripSource makes vm's runtime compile functions without their source text until the returned func is
// called (JS_SetStripInfo): such functions print as "[native code]", as a browser's built-ins do.
func stripSource(vm *quickjs.VM) (restore func()) {
	if vm == nil || !qjsOffsets() {
		return func() {}
	}
	base := unsafe.Pointer(vm)
	ctx := *(*uintptr)(unsafe.Add(base, qjsCtxOff))
	r := *(*unsafe.Pointer)(unsafe.Add(base, qjsRtOff))
	if ctx == 0 || r == nil {
		return func() {}
	}
	tls := *(**libc.TLS)(unsafe.Add(r, qjsTLSOff))
	crt := (*lib.TJSContext)(cmem(ctx)).Frt
	if tls == nil || crt == 0 {
		return func() {}
	}
	old := lib.XJS_GetStripInfo(tls, crt)
	lib.XJS_SetStripInfo(tls, crt, old|lib.MJS_STRIP_SOURCE)
	return func() { lib.XJS_SetStripInfo(tls, crt, old) }
}
