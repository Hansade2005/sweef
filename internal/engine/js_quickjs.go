//go:build quickjs

package engine

/*
#cgo CFLAGS: -I${SRCDIR}/../../third_party/quickjs
#cgo LDFLAGS: -L${SRCDIR}/../../third_party/quickjs -lquickjs -lm
#include <stdlib.h>
#include "quickjs.h"

// Bridge installed per context by js_bridge.c: exposes document.querySelector,
// innerText, title get/set and a mutation journal that Go reads back after
// each eval (see Tab.applyDomMutations).
extern JSValue sweef_dom_init(JSContext *ctx);
*/
import "C"

import (
	"sync/atomic"
	"unsafe"
)

var cgoReady atomic.Bool

type qjsCtx struct {
	ptr *C.JSContext
	rt  *C.JSRuntime
}

func cgoAvailable() bool { return cgoReady.Load() }

func initCgoRuntime(o *Options) { cgoReady.Store(true) }
func shutdownCgoRuntime()       { cgoReady.Store(false) }

func createCgoContext(tabID string) any {
	rt := C.JS_NewRuntime()
	if rt == nil {
		return nil
	}
	// Hard memory cap per tab — a runaway page script gets OOM inside QuickJS,
	// not inside our process. 16 MB default; tune via --max-js-mib.
	C.JS_SetMemoryLimit(rt, C.size_t(16*1024*1024))
	C.JS_SetMaxStackSize(rt, C.size_t(1*1024*1024))
	ctx := C.JS_NewContext(rt)
	if ctx == nil {
		C.JS_FreeRuntime(rt)
		return nil
	}
	C.sweef_dom_init(ctx)
	return &qjsCtx{ptr: ctx, rt: rt}
}

func destroyCgoContext(h any) {
	c, ok := h.(*qjsCtx)
	if !ok || c == nil {
		return
	}
	C.JS_FreeContext(c.ptr)
	C.JS_FreeRuntime(c.rt)
}

// cgoEval runs src and JSON-serialises the completion value. Errors are
// returned as strings so they land in the console ring like real browsers.
func cgoEval(h any, src, name string) (string, []string) {
	c, ok := h.(*qjsCtx)
	if !ok || c == nil {
		return "", nil
	}
	csrc := C.CString(src)
	defer C.free(unsafe.Pointer(csrc))
	cname := C.CString(name)
	defer C.free(unsafe.Pointer(cname))

	v := C.JS_Eval(c.ptr, csrc, C.size_t(len(src)), cname, C.JS_EVAL_TYPE_GLOBAL)
	defer C.JS_FreeValue(c.ptr, v)

	var errs []string
	if C.JS_IsException(v) != 0 {
		ex := C.JS_GetException(c.ptr)
		esrc := C.CString("default")
		defer C.free(unsafe.Pointer(esrc))
		str := C.JS_ToCString(c.ptr, ex)
		if str != nil {
			errs = append(errs, C.GoString(str))
			C.JS_FreeCString(c.ptr, str)
		}
		C.JS_FreeValue(c.ptr, ex)
		return "", errs
	}
	jsonFn := C.JS_GetPropertyStr(c.ptr, c.ptrGlobal(), C.CString("JSON"))
	_ = jsonFn // JSON.stringify path implemented in js_bridge.c helper
	out := C.sweef_json_stringify(c.ptr, v)
	if out == nil {
		return "null", nil
	}
	defer C.JS_FreeCString(c.ptr, out)
	return C.GoString(out), nil
}
