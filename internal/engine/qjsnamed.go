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
	qjsOffOnce             sync.Once
	qjsCtxOff, qjsRtOff    uintptr
	qjsTLSOff              uintptr
	qjsOffOK               bool
	errNamedCompileUnavail = errors.New("named compile unavailable")
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

// compileNamed compiles src as a module (or script) named name and returns its bytecode. On any failure,
// including a syntax error, it returns an error and the caller compiles the usual way (which reports the error).
//
// The compile runs in a scratch VM of its own: compiling a module registers it in the context's module list,
// and the copy the page VM then reads back from the bytecode would be a second module of the same name (a chunk
// importing the entry module would find the first one, never evaluated, and run the module again).
func compileNamed(_ *quickjs.VM, src, name string, flags int) ([]byte, error) {
	if !qjsOffsets() {
		return nil, errNamedCompileUnavail
	}
	vm, err := quickjs.NewVM()
	if err != nil {
		return nil, err
	}
	defer vm.Close()
	base := unsafe.Pointer(vm)
	ctx := *(*uintptr)(unsafe.Add(base, qjsCtxOff))
	rt := *(*unsafe.Pointer)(unsafe.Add(base, qjsRtOff))
	if ctx == 0 || rt == nil {
		return nil, errNamedCompileUnavail
	}
	tls := *(**libc.TLS)(unsafe.Add(rt, qjsTLSOff))
	if tls == nil {
		return nil, errNamedCompileUnavail
	}
	ps, err := libc.CString(src)
	if err != nil {
		return nil, err
	}
	defer libc.Xfree(tls, ps)
	pn, err := libc.CString(name)
	if err != nil {
		return nil, err
	}
	defer libc.Xfree(tls, pn)
	v := lib.XJS_Eval(tls, ctx, ps, libc.Tsize_t(len(src)), pn, int32(flags|lib.MJS_EVAL_FLAG_COMPILE_ONLY))
	if v.Ftag == lib.EJS_TAG_EXCEPTION {
		lib.XFreeValue(tls, ctx, lib.XJS_GetException(tls, ctx)) // the caller's plain compile reports it
		return nil, errNamedCompileUnavail
	}
	defer lib.XFreeValue(tls, ctx, v)
	usize := int(unsafe.Sizeof(libc.Tsize_t(0)))
	plen := tls.Alloc(usize)
	defer tls.Free(usize)
	p := lib.XJS_WriteObject(tls, ctx, plen, v, int32(lib.MJS_WRITE_OBJ_BYTECODE))
	if p == 0 {
		return nil, errNamedCompileUnavail
	}
	var n uint64
	for i, b := range libc.GoBytes(plen, usize) { // size_t, little-endian on every target
		n |= uint64(b) << (8 * i)
	}
	out := append([]byte(nil), libc.GoBytes(p, int(n))...) // GoBytes aliases C memory: copy before freeing it
	lib.Xjs_free(tls, ctx, p)
	return out, nil
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
