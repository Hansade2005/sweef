package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ---- hand-assembled modules --------------------------------------------------------------------------------
//
// No toolchain is involved: every module below is bytes written out by hand, section by section. The helpers
// only add the section framing (id, LEB128 size, vector counts) that is noise to read.

// ft encodes a function type from letters: i=i32 l=i64 f=f32 d=f64.
func ft(params, results string) []byte {
	l := map[byte]byte{'i': 0x7f, 'l': 0x7e, 'f': 0x7d, 'd': 0x7c}
	out := []byte{0x60, byte(len(params))}
	for i := 0; i < len(params); i++ {
		out = append(out, l[params[i]])
	}
	out = append(out, byte(len(results)))
	for i := 0; i < len(results); i++ {
		out = append(out, l[results[i]])
	}
	return out
}

// body is one function body: no locals, then the instructions (which must end with 0x0b).
func body(code ...byte) []byte {
	b := append([]byte{0x00}, code...)
	return append(uleb(uint64(len(b))), b...)
}

func mod(sections ...[]byte) []byte {
	out := append([]byte(nil), wasmHeader...)
	for _, s := range sections {
		out = append(out, s...)
	}
	return out
}

func types(ts ...[]byte) []byte   { return wsection(1, wvec(ts)) }
func funcs(idx ...byte) []byte    { return wsection(3, append([]byte{byte(len(idx))}, idx...)) }
func codes(bs ...[]byte) []byte   { return wsection(10, wvec(bs)) }
func exports(es ...[]byte) []byte { return wsection(7, wvec(es)) }
func export(name string, kind byte, idx byte) []byte {
	return append(append(wname(name), kind), idx)
}
func importFn(m, n string, typeIdx byte) []byte {
	return append(append(append(wname(m), wname(n)...), wkFunc), typeIdx)
}

// factorial: (func $fac (param i64) (result i64)  if n == 0 then 1 else n * fac(n-1))
func wasmFactorial() []byte {
	return mod(
		types(ft("l", "l")),
		funcs(0),
		exports(export("fac", wkFunc, 0)),
		codes(body(
			0x20, 0x00, // local.get 0
			0x50,       // i64.eqz
			0x04, 0x7e, // if (result i64)
			0x42, 0x01, //   i64.const 1
			0x05,       // else
			0x20, 0x00, //   local.get 0
			0x20, 0x00, //   local.get 0
			0x42, 0x01, //   i64.const 1
			0x7d,       //   i64.sub
			0x10, 0x00, //   call 0
			0x7e, //   i64.mul
			0x0b, // end
			0x0b,
		)),
	)
}

// wasmMemory: one page, max 3; exports memory plus store(addr,val), load(addr), grow(n), size().
func wasmMemory() []byte {
	return mod(
		types(ft("ii", ""), ft("i", "i"), ft("", "i")),
		funcs(0, 1, 1, 2),
		wsection(5, wvec([][]byte{wlimits(1, 3)})),
		exports(
			export("memory", wkMemory, 0), export("store", wkFunc, 0), export("load", wkFunc, 1),
			export("grow", wkFunc, 2), export("size", wkFunc, 3),
		),
		codes(
			body(0x20, 0x00, 0x20, 0x01, 0x36, 0x02, 0x00, 0x0b), // i32.store
			body(0x20, 0x00, 0x28, 0x02, 0x00, 0x0b),             // i32.load
			body(0x20, 0x00, 0x40, 0x00, 0x0b),                   // memory.grow
			body(0x3f, 0x00, 0x0b),                               // memory.size
		),
	)
}

// wasmCallback: imports env.cb (i32)->i32 and exports run(x) = cb(x) + 1 and double(x) = x + x.
func wasmCallback() []byte {
	return mod(
		types(ft("i", "i")),
		wsection(2, wvec([][]byte{importFn("env", "cb", 0)})),
		funcs(0, 0),
		exports(export("run", wkFunc, 1), export("double", wkFunc, 2)),
		codes(
			body(0x20, 0x00, 0x10, 0x00, 0x41, 0x01, 0x6a, 0x0b), // cb(x) + 1
			body(0x20, 0x00, 0x20, 0x00, 0x6a, 0x0b),             // x + x
		),
	)
}

// wasmTable: a funcref table of 2 filled by an element segment with (x+1) and (x*2); exports the table and
// callidx(idx, arg) which uses call_indirect.
func wasmTable() []byte {
	elem := append([]byte{0x00, 0x41, 0x00, 0x0b}, wvec([][]byte{{0x00}, {0x01}})...) // active, offset 0, funcs 0 and 1
	return mod(
		types(ft("i", "i"), ft("ii", "i")),
		funcs(0, 0, 1),
		wsection(4, wvec([][]byte{append([]byte{wvFunc}, wlimits(2, 4)...)})),
		exports(export("tbl", wkTable, 0), export("callidx", wkFunc, 2)),
		wsection(9, wvec([][]byte{elem})),
		codes(
			body(0x20, 0x00, 0x41, 0x01, 0x6a, 0x0b),             // f0: x + 1
			body(0x20, 0x00, 0x41, 0x02, 0x6c, 0x0b),             // f1: x * 2
			body(0x20, 0x01, 0x20, 0x00, 0x11, 0x00, 0x00, 0x0b), // call_indirect (type 0) table 0
		),
	)
}

// wasmTraps: divide(a,b) = a / b (traps on b == 0), boom() = unreachable, spin() = busy-wait until byte 0 of
// the exported memory becomes non-zero (a runaway loop the test can end).
func wasmTraps() []byte {
	return mod(
		types(ft("ii", "i"), ft("", ""), ft("", "")),
		funcs(0, 1, 2),
		wsection(5, wvec([][]byte{wlimits(1, -1)})),
		exports(export("divide", wkFunc, 0), export("boom", wkFunc, 1), export("spin", wkFunc, 2), export("mem", wkMemory, 0)),
		codes(
			body(0x20, 0x00, 0x20, 0x01, 0x6d, 0x0b),
			body(0x00, 0x0b), // unreachable
			body(0x02, 0x40, 0x03, 0x40, 0x41, 0x00, 0x2d, 0x00, 0x00, 0x0d, 0x01, 0x0c, 0x00, 0x0b, 0x0b, 0x0b), // block loop: br_if mem[0]; br
		),
	)
}

// wasmGlobals: exports a mutable i32 global (7) and an immutable i64 global (-5), plus bump() = g += 1.
func wasmGlobals() []byte {
	g1 := []byte{0x7f, 0x01, 0x41, 0x07, 0x0b}
	g2 := []byte{0x7e, 0x00, 0x42, 0x7b, 0x0b}
	return mod(
		types(ft("", "")),
		funcs(0),
		wsection(6, wvec([][]byte{g1, g2})),
		exports(export("counter", wkGlobal, 0), export("big", wkGlobal, 1), export("bump", wkFunc, 0)),
		codes(body(0x23, 0x00, 0x41, 0x01, 0x6a, 0x24, 0x00, 0x0b)),
	)
}

// wasmImportMemory imports env.mem (1 page min) and exports get(addr) reading it.
func wasmImportMemory() []byte {
	imp := append(append(append(wname("env"), wname("mem")...), wkMemory), wlimits(1, -1)...)
	return mod(
		types(ft("i", "i")),
		wsection(2, wvec([][]byte{imp})),
		funcs(0),
		exports(export("get", wkFunc, 0)),
		codes(body(0x20, 0x00, 0x2d, 0x00, 0x00, 0x0b)), // i32.load8_u
	)
}

// wasmStartTrap has a start function that executes unreachable.
func wasmStartTrap() []byte {
	return mod(types(ft("", "")), funcs(0), wsection(8, []byte{0}), codes(body(0x00, 0x0b)))
}

// wasmHello is what a C toolchain emits for puts("hello wasm"): a data segment, imported memory-reading
// callback, exported main. The string lives at offset 16 of the module's own exported memory.
func wasmHello() []byte {
	data := append([]byte{0x00, 0x41, 0x10, 0x0b}, append(uleb(10), []byte("hello wasm")...)...)
	return mod(
		types(ft("ii", ""), ft("", "")),
		wsection(2, wvec([][]byte{importFn("env", "puts", 0)})),
		funcs(1),
		wsection(5, wvec([][]byte{wlimits(1, -1)})),
		exports(export("memory", wkMemory, 0), export("main", wkFunc, 1)),
		codes(body(0x41, 0x10, 0x41, 0x0a, 0x10, 0x00, 0x0b)), // puts(16, 10)
		wsection(11, wvec([][]byte{data})),
	)
}

// withCustom appends a custom section "meta" holding the given payload.
func withCustom(m []byte, payload string) []byte {
	return append(append([]byte(nil), m...), wsection(0, append(wname("meta"), payload...))...)
}

func jsBytes(b []byte) string {
	parts := make([]string, len(b))
	for i, c := range b {
		parts[i] = fmt.Sprint(c)
	}
	return "new Uint8Array([" + strings.Join(parts, ",") + "])"
}

// ---- harness -----------------------------------------------------------------------------------------------

func wasmServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<!doctype html><html><body><p id="o">-</p><script>1</script></body></html>`)
	})
	mux.HandleFunc("/fac.wasm", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/wasm")
		w.Write(wasmFactorial())
	})
	mux.HandleFunc("/fac-wrongtype.wasm", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(wasmFactorial())
	})
	mux.HandleFunc("/bin.wasm", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/wasm")
		w.Write(withCustom(wasmFactorial(), "\xff\xfe\x80\x00\xc3"))
	})
	mux.HandleFunc("/worker.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/javascript")
		// The worker announces itself first: a message posted to a worker whose script has not finished
		// evaluating is dropped (an existing property of the worker support, unrelated to WebAssembly).
		io.WriteString(w, `postMessage('ready');
onmessage = function (e) {
  if (e.data === 'stream') {
    WebAssembly.instantiateStreaming(fetch('/bin.wasm')).then(function (r) {
      postMessage({fac: String(r.instance.exports.fac(5n)), bytes: 'ok'});
    }, function (err) { postMessage({err: String(err)}); });
    return;
  }
  WebAssembly.instantiate(new Uint8Array(e.data)).then(function (r) {
    postMessage({fac: String(r.instance.exports.fac(10n)), kind: typeof WebAssembly.Memory});
  }, function (err) { postMessage({err: String(err)}); });
};`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func wasmTab(t *testing.T, mutate func(*Options)) *Tab {
	t.Helper()
	srv := wasmServer(t)
	o := DefaultOptions()
	o.SettleMs = 30
	if mutate != nil {
		mutate(&o)
	}
	e := New(o)
	t.Cleanup(func() { e.Close() })
	tab, err := e.NewTab(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	return tab
}

// run evaluates an async function body and returns its JSON result, failing the test on a JS error.
func runWasm(t *testing.T, tab *Tab, body string) string {
	t.Helper()
	raw, err := tab.Evaluate("(async function(){ " + body + " })()")
	if err != nil {
		t.Fatalf("evaluate: %v\n%s", err, body)
	}
	return string(raw)
}

func wantJSON(t *testing.T, got, want string) {
	t.Helper()
	var a, b any
	if err := json.Unmarshal([]byte(got), &a); err != nil {
		t.Fatalf("result %q is not JSON: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatal(err)
	}
	ga, _ := json.Marshal(a)
	gb, _ := json.Marshal(b)
	if string(ga) != string(gb) {
		t.Fatalf("got %s want %s", ga, gb)
	}
}

// workerWasm runs /worker.js, waits for its ready message, then posts the factorial module to it.
func workerWasm() string {
	return `
    const w = new Worker('/worker.js');
    const res = await new Promise((resolve) => {
      w.onmessage = (e) => { if (e.data === 'ready') w.postMessage(Array.from(` + jsBytes(wasmFactorial()) + `)); else resolve(e.data); };
    });
    w.terminate();`
}

// ---- tests -------------------------------------------------------------------------------------------------

func TestWasmBridgeSelfTest(t *testing.T) {
	if err := wasmQJS(); err != nil {
		t.Fatalf("the quickjs ArrayBuffer bridge does not match the linked quickjs version: %v", err)
	}
}

func TestWasmFactorialAndBigInt(t *testing.T) {
	tab := wasmTab(t, nil)
	got := runWasm(t, tab, `
    const {instance, module} = await WebAssembly.instantiate(`+jsBytes(wasmFactorial())+`);
    const f = instance.exports.fac;
    let typeErr = '';
    try { f(5); } catch (e) { typeErr = e.name; }
    return {
      f5: String(f(5n)), f20: String(f(20n)), f25wrapped: String(f(25n)),
      isFn: typeof f, name: f.name, len: f.length,
      big: typeof f(3n), typeErr,
      exports: WebAssembly.Module.exports(module), imports: WebAssembly.Module.imports(module),
      tag: Object.prototype.toString.call(WebAssembly),
    };`)
	wantJSON(t, got, `{"f5":"120","f20":"2432902008176640000","f25wrapped":"7034535277573963776","isFn":"function","name":"0","len":1,"big":"bigint","typeErr":"TypeError","exports":[{"name":"fac","kind":"function"}],"imports":[],"tag":"[object WebAssembly]"}`)
}

func TestWasmMemoryIsSharedAndGrows(t *testing.T) {
	tab := wasmTab(t, nil)
	got := runWasm(t, tab, `
    const {instance} = await WebAssembly.instantiate(`+jsBytes(wasmMemory())+`);
    const ex = instance.exports, mem = ex.memory;
    const b1 = mem.buffer, v1 = new Uint32Array(b1);
    const sameBuffer = mem.buffer === b1;
    ex.store(8, 0xCAFE);                        // wasm writes ...
    const seenByJS = v1[2];
    v1[3] = 0xBEEF;                             // ... JS writes
    const seenByWasm = ex.load(12);
    const sizeBefore = ex.size(), len1 = b1.byteLength;
    const grew = ex.grow(1);                    // wasm-initiated growth
    const detached = b1.byteLength, viewLen = v1.length;
    const b2 = mem.buffer, kept = new Uint32Array(b2)[2], len2 = b2.byteLength;
    const jsGrow = mem.grow(1);                 // JS-initiated growth up to the max of 3
    const fail = (function(){ try { mem.grow(1); return 'no'; } catch (e) { return e.name; } })();
    const wasmFail = ex.grow(1);
    return {sameBuffer, seenByJS, seenByWasm, sizeBefore, len1, grew, detached, viewLen,
            len2, kept, jsGrow, len3: mem.buffer.byteLength, fail, wasmFail,
            after: new Uint32Array(mem.buffer)[3], differs: b1 !== b2};`)
	wantJSON(t, got, `{"sameBuffer":true,"seenByJS":51966,"seenByWasm":48879,"sizeBefore":1,"len1":65536,"grew":1,"detached":0,"viewLen":0,"len2":131072,"kept":51966,"jsGrow":2,"len3":196608,"fail":"RangeError","wasmFail":-1,"after":48879,"differs":true}`)
}

func TestWasmImportedJSCallbackAndReentrancy(t *testing.T) {
	tab := wasmTab(t, nil)
	got := runWasm(t, tab, `
    let seen = [], inst;
    const imports = { env: { cb: function (x) { seen.push(x); return inst.exports.double(x) + 100; } } };   // wasm -> JS -> wasm
    const r = await WebAssembly.instantiate(`+jsBytes(wasmCallback())+`, imports);
    inst = r.instance;
    const out = [inst.exports.run(5), inst.exports.run(-3)];
    // an exception thrown by the import crosses the wasm frame unchanged
    const marker = new RangeError('from js');
    const thrower = (await WebAssembly.instantiate(`+jsBytes(wasmCallback())+`, { env: { cb() { throw marker; } } })).instance;
    let same = false; try { thrower.exports.run(1); } catch (e) { same = e === marker; }
    // a non-numeric return is coerced like ToInt32
    const coerced = (await WebAssembly.instantiate(`+jsBytes(wasmCallback())+`, { env: { cb() { return '41'; } } })).instance.exports.run(0);
    return {out, seen, same, coerced};`)
	wantJSON(t, got, `{"out":[111,95],"seen":[5,-3],"same":true,"coerced":42}`)
}

func TestWasmExportedTable(t *testing.T) {
	tab := wasmTab(t, nil)
	got := runWasm(t, tab, `
    const {instance} = await WebAssembly.instantiate(`+jsBytes(wasmTable())+`);
    const ex = instance.exports, tbl = ex.tbl;
    const viaWasm = [ex.callidx(0, 41), ex.callidx(1, 21)];
    const f1 = tbl.get(1), f0 = tbl.get(0);
    const viaJS = [f0(41), f1(21)];
    const same = tbl.get(1) === f1;
    // swap the entries from JS and call through call_indirect again
    tbl.set(0, f1);
    const swapped = ex.callidx(0, 50);
    tbl.set(1, null);
    let trap = ''; try { ex.callidx(1, 1); } catch (e) { trap = e.name + ':' + e.message; }
    const len = tbl.length, grown = tbl.grow(2), len2 = tbl.length;
    let oob = ''; try { tbl.get(99); } catch (e) { oob = e.name; }
    let notFn = ''; try { tbl.set(0, function(){}); } catch (e) { notFn = e.name; }
    return {viaWasm, viaJS, same, swapped, trap, len, grown, len2, nullEntry: tbl.get(1), oob, notFn};`)
	wantJSON(t, got, `{"viaWasm":[42,42],"viaJS":[42,42],"same":true,"swapped":100,"trap":"RuntimeError:table index is out of bounds or null function","len":2,"grown":2,"len2":4,"nullEntry":null,"oob":"RangeError","notFn":"TypeError"}`)
}

func TestWasmTrapsAreRuntimeErrors(t *testing.T) {
	tab := wasmTab(t, nil)
	got := runWasm(t, tab, `
    const ex = (await WebAssembly.instantiate(`+jsBytes(wasmTraps())+`)).instance.exports;
    const catchIt = (f) => { try { f(); return 'no throw'; } catch (e) { return [e instanceof WebAssembly.RuntimeError, e instanceof Error, e.name, e.message]; } };
    return { div: ex.divide(7, 2), zero: catchIt(() => ex.divide(1, 0)), unreachable: catchIt(() => ex.boom()),
             overflow: catchIt(() => ex.divide(-2147483648, -1)),
             again: ex.divide(9, 3) };`)
	wantJSON(t, got, `{"div":3,"zero":[true,true,"RuntimeError","divide by zero"],"unreachable":[true,true,"RuntimeError","unreachable"],"overflow":[true,true,"RuntimeError","divide result unrepresentable"],"again":3}`)
}

func TestWasmRunawayCallIsAbandoned(t *testing.T) {
	// 100 ms of page-script budget gives a wasm call 600 ms
	tab := wasmTab(t, func(o *Options) { o.JSTimeoutMs = 100 })
	base := wasmLive.Load()
	got := runWasm(t, tab, `
    const ex = (await WebAssembly.instantiate(`+jsBytes(wasmTraps())+`)).instance.exports;
    const flag = new Uint8Array(ex.mem.buffer);
    let first; try { ex.spin(); first = 'returned'; } catch (e) { first = e.name; }
    flag[0] = 1;                       // lets the abandoned call finish, so the test leaves no spinning goroutine
    let again; try { ex.divide(4, 2); again = 'worked'; } catch (e) { again = e.name; }
    let mem; try { mem = ex.mem.buffer.byteLength; } catch (e) { mem = e.name; }
    return {first, again, pageStillRuns: [1, 2, 3].map(x => x * 2), mem};`)
	wantJSON(t, got, `{"first":"RuntimeError","again":"RuntimeError","pageStillRuns":[2,4,6],"mem":65536}`)
	// the abandoned runtime is released as soon as the runaway call ends and the page is gone
	tab.eng.CloseTab(tab.ID)
	deadline := time.Now().Add(5 * time.Second)
	for wasmLive.Load() != base && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := wasmLive.Load(); n != base {
		t.Fatalf("hosts open: %d, want %d", n, base)
	}
}

func TestWasmInvalidModules(t *testing.T) {
	tab := wasmTab(t, nil)
	good := wasmFactorial()
	truncated := good[:len(good)-3]
	badBody := mod(types(ft("", "i")), funcs(0), codes(body(0x0b))) // function leaves nothing on the stack
	got := runWasm(t, tab, `
    const kinds = [];
    for (const bytes of [new Uint8Array([1,2,3,4,5,6,7,8]), new Uint8Array(0), `+jsBytes(truncated)+`, `+jsBytes(badBody)+`]) {
      try { new WebAssembly.Module(bytes); kinds.push('compiled'); } catch (e) { kinds.push(e.name + ':' + (e instanceof WebAssembly.CompileError) + ':' + (e instanceof Error)); }
    }
    let typeErr = ''; try { new WebAssembly.Module('nope'); } catch (e) { typeErr = e.name; }
    let rej = ''; try { await WebAssembly.compile(new Uint8Array([0,97,115,109,2,0,0,0])); } catch (e) { rej = e.name; }
    return {kinds, typeErr, rej, v: [WebAssembly.validate(`+jsBytes(good)+`), WebAssembly.validate(`+jsBytes(truncated)+`), WebAssembly.validate(new Uint8Array(3))]};`)
	wantJSON(t, got, `{"kinds":["CompileError:true:true","CompileError:true:true","CompileError:true:true","CompileError:true:true"],"typeErr":"TypeError","rej":"CompileError","v":[true,false,false]}`)
}

func TestWasmLinkErrors(t *testing.T) {
	tab := wasmTab(t, nil)
	got := runWasm(t, tab, `
    const cbMod = new WebAssembly.Module(`+jsBytes(wasmCallback())+`);
    const memMod = new WebAssembly.Module(`+jsBytes(wasmImportMemory())+`);
    const name = (f) => { try { f(); return 'ok'; } catch (e) { return e.name; } };
    return {
      noImports: name(() => new WebAssembly.Instance(cbMod)),
      noModule: name(() => new WebAssembly.Instance(cbMod, {})),
      notCallable: name(() => new WebAssembly.Instance(cbMod, {env: {cb: 1}})),
      notMemory: name(() => new WebAssembly.Instance(memMod, {env: {mem: {}}})),
      tooSmall: name(() => new WebAssembly.Instance(memMod, {env: {mem: new WebAssembly.Memory({initial: 0})}})),
      fine: name(() => new WebAssembly.Instance(memMod, {env: {mem: new WebAssembly.Memory({initial: 1})}})),
      startTrap: name(() => new WebAssembly.Instance(new WebAssembly.Module(`+jsBytes(wasmStartTrap())+`))),
    };`)
	wantJSON(t, got, `{"noImports":"TypeError","noModule":"TypeError","notCallable":"LinkError","notMemory":"LinkError","tooSmall":"LinkError","fine":"ok","startTrap":"RuntimeError"}`)
}

func TestWasmSharedMemoryBetweenInstances(t *testing.T) {
	tab := wasmTab(t, nil)
	got := runWasm(t, tab, `
    const mem = new WebAssembly.Memory({initial: 1, maximum: 4});
    const a = (await WebAssembly.instantiate(`+jsBytes(wasmImportMemory())+`, {env: {mem}})).instance;
    const b = (await WebAssembly.instantiate(`+jsBytes(wasmImportMemory())+`, {env: {mem}})).instance;
    new Uint8Array(mem.buffer)[100] = 77;
    const first = [a.exports.get(100), b.exports.get(100)];
    mem.grow(1);
    new Uint8Array(mem.buffer)[70000] = 9;
    const second = [a.exports.get(70000), b.exports.get(70000)];
    // an exported memory can be imported by another instance, and it is the same object
    const own = (await WebAssembly.instantiate(`+jsBytes(wasmMemory())+`)).instance;
    const user = (await WebAssembly.instantiate(`+jsBytes(wasmImportMemory())+`, {env: {mem: own.exports.memory}})).instance;
    own.exports.store(0, 0x41424344);
    return {first, second, reexport: user.exports.get(0), sameObj: own.exports.memory === own.exports.memory,
            instanceOf: mem instanceof WebAssembly.Memory, pages: mem.buffer.byteLength / 65536};`)
	wantJSON(t, got, `{"first":[77,77],"second":[9,9],"reexport":68,"sameObj":true,"instanceOf":true,"pages":2}`)
}

func TestWasmGlobals(t *testing.T) {
	tab := wasmTab(t, nil)
	got := runWasm(t, tab, `
    const ex = (await WebAssembly.instantiate(`+jsBytes(wasmGlobals())+`)).instance.exports;
    const c = ex.counter, big = ex.big;
    const v0 = c.value; ex.bump(); ex.bump(); const v2 = c.value; c.value = 100; ex.bump();
    let imm = ''; try { big.value = 1n; } catch (e) { imm = e.name; }
    const g = new WebAssembly.Global({value: 'i64', mutable: true}, 1n << 40n); g.value += 1n;
    const f = new WebAssembly.Global({value: 'f32'}, 1.1);
    return {v0, v2, afterSet: c.value, big: String(big.value), imm, g: String(g.value), f32: f.value, valueOf: Number(c), same: ex.counter === c};`)
	wantJSON(t, got, `{"v0":7,"v2":9,"afterSet":101,"big":"-5","imm":"TypeError","g":"1099511627777","f32":1.100000023841858,"valueOf":101,"same":true}`)
}

func TestWasmGlobalImportFromJS(t *testing.T) {
	// imports an immutable i32 global as a plain number
	imp := append(append(append(wname("env"), wname("k")...), wkGlobal), 0x7f, 0x00)
	m := mod(
		types(ft("", "i")),
		wsection(2, wvec([][]byte{imp})),
		funcs(0),
		exports(export("k", wkFunc, 0)),
		codes(body(0x23, 0x00, 0x0b)),
	)
	tab := wasmTab(t, nil)
	got := runWasm(t, tab, `
    const mk = (v) => (new WebAssembly.Instance(new WebAssembly.Module(`+jsBytes(m)+`), {env: {k: v}})).exports.k();
    let bad = ''; try { mk('x'); } catch (e) { bad = e.name; }
    return {num: mk(41), glob: mk(new WebAssembly.Global({value: 'i32'}, 7)), bad};`)
	wantJSON(t, got, `{"num":41,"glob":7,"bad":"LinkError"}`)
}

func TestWasmCustomSectionsAndMetadata(t *testing.T) {
	m := withCustom(withCustom(wasmFactorial(), "alpha"), "beta")
	tab := wasmTab(t, nil)
	got := runWasm(t, tab, `
    const mod = new WebAssembly.Module(`+jsBytes(m)+`);
    const secs = WebAssembly.Module.customSections(mod, 'meta').map(b => new TextDecoder().decode(b));
    const none = WebAssembly.Module.customSections(mod, 'other').length;
    const cb = new WebAssembly.Module(`+jsBytes(wasmCallback())+`);
    return {secs, none, imports: WebAssembly.Module.imports(cb), isModule: mod instanceof WebAssembly.Module,
            str: String(mod), inst: await WebAssembly.instantiate(mod) instanceof WebAssembly.Instance};`)
	wantJSON(t, got, `{"secs":["alpha","beta"],"none":0,"imports":[{"module":"env","name":"cb","kind":"function"}],"isModule":true,"str":"[object WebAssembly.Module]","inst":true}`)
}

func TestWasmTypedArrayViewAsSource(t *testing.T) {
	// bytes taken from the middle of a larger buffer (byteOffset != 0) must be honoured
	tab := wasmTab(t, nil)
	fac := wasmFactorial()
	got := runWasm(t, tab, `
    const bytes = `+jsBytes(fac)+`;
    const big = new Uint8Array(bytes.length + 20); big.set(bytes, 13);
    const view = new Uint8Array(big.buffer, 13, bytes.length);
    return {ok: WebAssembly.validate(view), run: String((await WebAssembly.instantiate(view)).instance.exports.fac(4n)),
            buf: WebAssembly.validate(bytes.buffer)};`)
	wantJSON(t, got, `{"ok":true,"run":"24","buf":true}`)
}

// A toolchain-shaped program: data segment, memory read through a view, TextDecoder, callback into JS.
func TestWasmHelloSmoke(t *testing.T) {
	tab := wasmTab(t, nil)
	got := runWasm(t, tab, `
    let out = '', mem;
    const {instance} = await WebAssembly.instantiate(`+jsBytes(wasmHello())+`, { env: { puts(ptr, len) { out += new TextDecoder().decode(new Uint8Array(mem.buffer, ptr, len)); } } });
    mem = instance.exports.memory;
    instance.exports.main();
    return out;`)
	wantJSON(t, got, `"hello wasm"`)
}

func TestWasmInstantiateStreaming(t *testing.T) {
	tab := wasmTab(t, nil)
	got := runWasm(t, tab, `
    const r = await WebAssembly.instantiateStreaming(fetch('/fac.wasm'), {});
    const m = await WebAssembly.compileStreaming(fetch('/fac.wasm'));
    let wrong = ''; try { await WebAssembly.instantiateStreaming(fetch('/fac-wrongtype.wasm')); } catch (e) { wrong = e.name; }
    let notResp = ''; try { await WebAssembly.compileStreaming({}); } catch (e) { notResp = e.name; }
    return {fac: String(r.instance.exports.fac(6n)), module: m instanceof WebAssembly.Module, wrong, notResp};`)
	wantJSON(t, got, `{"fac":"720","module":true,"wrong":"TypeError","notResp":"TypeError"}`)
}

func TestWasmInsideWorker(t *testing.T) {
	tab := wasmTab(t, nil)
	raw := runWasm(t, tab, `
`+workerWasm()+`
    return res;`)
	wantJSON(t, raw, `{"fac":"3628800","kind":"function"}`)
}

func TestWasmStreamingInsideWorker(t *testing.T) {
	// the worker's fetch must give exact bytes: this module carries a custom section that is not valid UTF-8
	tab := wasmTab(t, nil)
	raw := runWasm(t, tab, `
    const w = new Worker('/worker.js');
    const res = await new Promise((resolve) => {
      w.onmessage = (e) => { if (e.data === 'ready') w.postMessage('stream'); else resolve(e.data); };
    });
    w.terminate();
    return res;`)
	wantJSON(t, raw, `{"fac":"120","bytes":"ok"}`)
}

func TestWasmNamespaceShape(t *testing.T) {
	tab := wasmTab(t, nil)
	got := runWasm(t, tab, `
    const names = Object.getOwnPropertyNames(WebAssembly).sort();
    return {names, enumerable: Object.keys(WebAssembly).length, type: typeof WebAssembly, inWindow: 'WebAssembly' in window,
            ctorErr: (() => { try { WebAssembly.Module(new Uint8Array(8)); } catch (e) { return e.name; } })()};`)
	wantJSON(t, got, `{"names":["CompileError","Global","Instance","LinkError","Memory","Module","RuntimeError","Table","compile","compileStreaming","instantiate","instantiateStreaming","validate"],"enumerable":0,"type":"object","inWindow":true,"ctorErr":"TypeError"}`)
}

// Teardown: closing the engine closes every wazero runtime and leaves no goroutines behind.
func TestWasmTeardownFreesEverything(t *testing.T) {
	baseLive := wasmLive.Load()
	base := runtime.NumGoroutine()
	{
		srv := wasmServer(t)
		o := DefaultOptions()
		o.SettleMs = 30
		e := New(o)
		for i := 0; i < 3; i++ {
			tab, err := e.NewTab(srv.URL + "/")
			if err != nil {
				t.Fatal(err)
			}
			got := runWasm(t, tab, `
        const m = (await WebAssembly.instantiate(`+jsBytes(wasmMemory())+`)).instance.exports;
        const keep = m.memory.buffer; m.store(0, 5);
`+workerWasm()+`
        return [m.load(0), res.fac];`)
			wantJSON(t, got, `[5,"3628800"]`)
		}
		if n := wasmLive.Load(); n <= baseLive {
			t.Fatalf("expected live wasm hosts while tabs are open, have %d (base %d)", n, baseLive)
		}
		e.Close()
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && wasmLive.Load() != baseLive {
		time.Sleep(10 * time.Millisecond)
	}
	if n := wasmLive.Load(); n != baseLive {
		t.Fatalf("wasm hosts still open after the engine closed: %d (base %d)", n, baseLive)
	}
	var n int
	for time.Now().Before(deadline.Add(5 * time.Second)) {
		runtime.GC()
		if n = runtime.NumGoroutine(); n <= base+1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	buf := make([]byte, 1<<16)
	buf = buf[:runtime.Stack(buf, true)]
	t.Fatalf("goroutines leaked: %d now, %d before\n%s", n, base, buf)
}

func TestWasmMemoryLimit(t *testing.T) {
	tab := wasmTab(t, nil)
	got := runWasm(t, tab, `
    let tooBig = ''; try { new WebAssembly.Memory({initial: 60000}); } catch (e) { tooBig = e.name; }
    const m = new WebAssembly.Memory({initial: 1});
    let grew = 'yes'; try { m.grow(70000); } catch (e) { grew = e.name; }
    return {tooBig, grew, still: m.buffer.byteLength};`)
	wantJSON(t, got, `{"tooBig":"RangeError","grew":"RangeError","still":65536}`)
}

// Navigating away and closing a tab each release the page's wasm runtime (native code and linear memory).
func TestWasmNavigationAndTabCloseFreeTheRuntime(t *testing.T) {
	srv := wasmServer(t)
	o := DefaultOptions()
	o.SettleMs = 30
	e := New(o)
	t.Cleanup(func() { e.Close() })
	base := wasmLive.Load()
	waitLive := func(want int64, what string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for wasmLive.Load() != want && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if n := wasmLive.Load(); n != want {
			t.Fatalf("%s: %d wasm hosts open, want %d", what, n, want)
		}
	}
	tab, err := e.NewTab(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	use := `const m = (await WebAssembly.instantiate(` + jsBytes(wasmMemory()) + `)).instance.exports; m.store(0, 1); return m.load(0);`
	wantJSON(t, runWasm(t, tab, use), `1`)
	waitLive(base+1, "after use")
	if err := tab.Navigate(srv.URL+"/?second", nil); err != nil {
		t.Fatal(err)
	}
	waitLive(base, "after navigation")
	wantJSON(t, runWasm(t, tab, use), `1`)
	waitLive(base+1, "second page")
	if err := e.CloseTab(tab.ID); err != nil {
		t.Fatal(err)
	}
	waitLive(base, "after closing the tab")
}

// A function re-entered through JavaScript (wasm -> JS -> the same wasm function) many levels deep.
func TestWasmSameFunctionReentrancy(t *testing.T) {
	tab := wasmTab(t, nil)
	got := runWasm(t, tab, `
    let inst, depth = 0, maxDepth = 0;
    const imports = { env: { cb(x) { depth++; maxDepth = Math.max(maxDepth, depth); const r = x <= 0 ? 0 : inst.exports.run(x - 1); depth--; return r; } } };
    inst = (await WebAssembly.instantiate(`+jsBytes(wasmCallback())+`, imports)).instance;
    const v = inst.exports.run(40);       // run(x) = cb(x) + 1, cb(x) = run(x-1): 41 nested activations of the same function
    return {v, maxDepth};`)
	wantJSON(t, got, `{"v":41,"maxDepth":41}`)
}
