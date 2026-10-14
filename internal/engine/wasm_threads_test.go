package engine

import (
	"encoding/base64"
	"runtime"
	"strings"
	"testing"
	"time"
)

// ---- a tiny threaded module, assembled by hand ------------------------------------------------------------
//
//	(import "env" "memory" (memory 1 2 shared))
//	(func (export "inc")    (param addr n i32)           ;; n times i32.atomic.rmw.add addr 1
//	(func (export "wait")   (param addr exp i32) (param timeout i64) (result i32)   ;; memory.atomic.wait32
//	(func (export "notify") (param addr count i32) (result i32)                      ;; memory.atomic.notify
//	(func (export "load")   (param addr i32) (result i32)                            ;; i32.atomic.load
//	(func (export "store")  (param addr val i32)                                     ;; i32.atomic.store
//	(func (export "spin")   (param addr exp i32)         ;; busy-loops until i32.atomic.load addr == exp

func importMemShared(m, n string, min, max byte) []byte {
	b := append(append(wname(m), wname(n)...), wkMemory)
	return append(b, 0x03, min, max)
}

func wasmThreadsModule() []byte {
	atomic := func(op byte) []byte { return []byte{0xfe, op, 0x02, 0x00} } // align 4, offset 0
	cat := func(parts ...[]byte) []byte {
		var o []byte
		for _, p := range parts {
			o = append(o, p...)
		}
		return o
	}
	inc := body(cat(
		[]byte{0x02, 0x40, 0x20, 0x01, 0x45, 0x0d, 0x00, 0x03, 0x40},
		[]byte{0x20, 0x00, 0x41, 0x01}, atomic(0x1e), []byte{0x1a},
		[]byte{0x20, 0x01, 0x41, 0x01, 0x6b, 0x22, 0x01, 0x0d, 0x00, 0x0b, 0x0b, 0x0b})...)
	wait := body(cat([]byte{0x20, 0x00, 0x20, 0x01, 0x20, 0x02}, atomic(0x01), []byte{0x0b})...)
	notify := body(cat([]byte{0x20, 0x00, 0x20, 0x01}, atomic(0x00), []byte{0x0b})...)
	load := body(cat([]byte{0x20, 0x00}, atomic(0x10), []byte{0x0b})...)
	store := body(cat([]byte{0x20, 0x00, 0x20, 0x01}, atomic(0x17), []byte{0x0b})...)
	spin := body(cat([]byte{0x03, 0x40, 0x20, 0x00}, atomic(0x10), []byte{0x20, 0x01, 0x47, 0x0d, 0x00, 0x0b, 0x0b})...)
	return mod(
		types(ft("ii", ""), []byte{0x60, 0x03, 0x7f, 0x7f, 0x7e, 0x01, 0x7f}, ft("ii", "i"), ft("i", "i")),
		wsection(2, wvec([][]byte{importMemShared("env", "memory", 1, 2)})),
		funcs(0, 1, 2, 3, 0, 0),
		exports(export("inc", wkFunc, 0), export("wait", wkFunc, 1), export("notify", wkFunc, 2), export("load", wkFunc, 3), export("store", wkFunc, 4), export("spin", wkFunc, 5)),
		codes(inc, wait, notify, load, store, spin),
	)
}

// A module that declares its own shared memory and exports it.
func wasmOwnSharedMemoryModule() []byte {
	lim := []byte{0x03, 0x01, 0x02}
	return mod(
		types(ft("i", "i")),
		funcs(0),
		wsection(5, wvec([][]byte{lim})),
		exports(export("mem", wkMemory, 0), export("load", wkFunc, 0)),
		codes(body(0x20, 0x00, 0xfe, 0x10, 0x02, 0x00, 0x0b)),
	)
}

func wb64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

const threadWorker = `
let inst = null;
onmessage = async (e) => {
  const d = e.data;
  if (d.cmd === 'init') { inst = new WebAssembly.Instance(d.module, { env: { memory: d.memory } }); postMessage({ ready: true, shared: d.memory.buffer instanceof SharedArrayBuffer }); return; }
  if (d.cmd === 'inc') { inst.exports.inc(d.addr, d.n); postMessage({ done: 'inc', total: inst.exports.load(d.addr) }); return; }
  if (d.cmd === 'wait') { postMessage({ state: 'waiting' }); const r = inst.exports.wait(d.addr, d.exp, d.timeout); postMessage({ done: 'wait', r }); return; }
  if (d.cmd === 'jswait') { const a = new Int32Array(d.memory.buffer); postMessage({ state: 'waiting' }); const r = Atomics.wait(a, d.index, d.exp, 8000); postMessage({ done: 'jswait', r }); return; }
  if (d.cmd === 'storenotify') { inst.exports.store(d.addr, 1); postMessage({ done: 'storenotify', n: inst.exports.notify(d.addr, 1) }); return; }
  if (d.cmd === 'notify') { postMessage({ done: 'notify', n: inst.exports.notify(d.addr, d.count) }); return; }
  if (d.cmd === 'spin') { postMessage({ state: 'spinning' }); try { inst.exports.spin(d.addr, d.exp); postMessage({ done: 'spin' }); } catch (x) { postMessage({ done: 'spin', trap: x.name }); } return; }
};`

const threadHelpers = sabHelpers + `
const BYTES = (s) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
const msgs = (w) => { const q = []; const waiters = []; w.onmessage = (e) => { const x = waiters.shift(); if (x) x(e.data); else q.push(e.data); }; return () => new Promise((res) => { if (q.length) res(q.shift()); else waiters.push(res); }); };
`

func TestWasmThreadsSharedMemoryAndAtomicsAcrossWorkers(t *testing.T) {
	testWasmThreads(t)
}

func TestWasmThreadsOnTheInterpreter(t *testing.T) {
	wasmForceInterpreter = true
	t.Cleanup(func() { wasmForceInterpreter = false })
	testWasmThreads(t)
}

func testWasmThreads(t *testing.T) {
	srv := sabServer(t, map[string]string{"/thread.js": threadWorker})
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	got := js(t, tab, threadHelpers+`
const out = {};
const module = new WebAssembly.Module(BYTES("`+wb64(wasmThreadsModule())+`"));
const memory = new WebAssembly.Memory({ initial: 1, maximum: 2, shared: true });
const i32 = new Int32Array(memory.buffer);
out.buffer = [memory.buffer instanceof SharedArrayBuffer, memory.buffer.byteLength, memory.buffer === memory.buffer, Object.prototype.toString.call(memory)];
const inst = new WebAssembly.Instance(module, { env: { memory } });
const ws = [new Worker('/thread.js'), new Worker('/thread.js')];
const q = ws.map(msgs);
ws.forEach((w) => w.postMessage({ cmd: 'init', module, memory }));
out.ready = await Promise.all(q.map((n) => n()));
// 1. three threads (the page and two workers) add to the same word
ws.forEach((w) => w.postMessage({ cmd: 'inc', addr: 0, n: 20000 }));
inst.exports.inc(0, 20000);
const done = await Promise.all(q.map((n) => n()));
out.total = [Atomics.load(i32, 0), inst.exports.load(0), done.every((d) => d.done === 'inc')];
// 2. a wasm waiter in a worker is woken by JS Atomics.notify
ws[0].postMessage({ cmd: 'wait', addr: 8, exp: 0, timeout: -1n });
await q[0](); await sleep(200);
Atomics.store(i32, 2, 1);
out.jsWakesWasm = [Atomics.notify(i32, 2, 1), (await q[0]()).r];
// 3. a JS Atomics.wait in a worker is woken by wasm memory.atomic.notify
ws[1].postMessage({ cmd: 'jswait', memory, index: 3, exp: 0 });
await q[1](); await sleep(200);
i32[3] = 1;
out.wasmWakesJs = [inst.exports.notify(12, 1), (await q[1]()).r];
// 4. wasm wait: value differs, and timeout
out.waitCodes = [inst.exports.wait(0, 1, 100n) === 1 ? 'not-equal' : 'other', inst.exports.wait(16, 0, 20000000n)];
// 5. grow: the old buffer keeps its length, the new one sees the added page, workers still share it
const old = memory.buffer;
out.grow = [memory.grow(1), old.byteLength, memory.buffer.byteLength, old === memory.buffer, new Int32Array(memory.buffer)[0] === 60000];
// 6. Atomics.waitAsync on the memory in the page, woken by wasm in a worker
const wa = Atomics.waitAsync(i32, 5, 0);
ws[0].postMessage({ cmd: 'storenotify', addr: 20 });
out.waitAsync = [wa.async, await wa.value, (await q[0]()).done];
// 7. a Memory that is not shared cannot be posted, a shared one needs the page to be isolated (it is)
try { ws[0].postMessage(new WebAssembly.Memory({ initial: 1 })); out.plain = 'posted'; } catch (x) { out.plain = x.name; }
try { new WebAssembly.Memory({ initial: 1, shared: true }); } catch (x) { out.noMax = x.name; }
ws.forEach((w) => w.terminate());
return JSON.stringify(out);`)
	want := `{"buffer":[true,65536,true,"[object WebAssembly.Memory]"],"ready":[{"ready":true,"shared":true},{"ready":true,"shared":true}],"total":[60000,60000,true],"jsWakesWasm":[1,0],"wasmWakesJs":[1,"ok"],"waitCodes":["not-equal",2],"grow":[1,65536,131072,false,true],"waitAsync":[true,"ok","storenotify"],"plain":"DataCloneError","noMax":"TypeError"}`
	if got != want {
		t.Fatalf("got  %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab))
	}
}

// A module that declares its own shared memory works in a page that is not isolated, and the memory is shared.
func TestWasmSharedMemoryDefinedByTheModule(t *testing.T) {
	srv := newCOIServer(t, nil, nil)
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	got := js(t, tab, threadHelpers+`
const m = new WebAssembly.Module(BYTES("`+wb64(wasmOwnSharedMemoryModule())+`"));
const inst = new WebAssembly.Instance(m, {});
const mem = inst.exports.mem;
const a = new Int32Array(mem.buffer); a[1] = 77;
let post = 'none'; try { new Worker('data:text/javascript,1').postMessage(mem); post = 'posted'; } catch (x) { post = x.name; }
return JSON.stringify({ isSAB: Object.prototype.toString.call(mem.buffer), hasCtor: typeof SharedArrayBuffer, load: inst.exports.load(4), validate: WebAssembly.validate(BYTES("`+wb64(wasmThreadsModule())+`")), post });`)
	// the page is not isolated: the SharedArrayBuffer constructor is absent but the memory's buffer is one, and it cannot be posted
	if want := `{"isSAB":"[object SharedArrayBuffer]","hasCtor":"undefined","load":77,"validate":true,"post":"DataCloneError"}`; got != want {
		t.Fatalf("got  %s\nwant %s\nconsole:\n%s", got, want, consoleText(tab))
	}
}

// Leaving a page whose threads are blocked or spinning in wasm retires the shared memory: the threads trap, the
// goroutines end and every host closes.
func TestWasmThreadsAreStoppedWhenThePageGoes(t *testing.T) {
	srv := sabServer(t, map[string]string{"/thread.js": threadWorker})
	e := newFeatureEngine(t)
	tab := openTab(t, e, srv.URL+"/a.html")
	baseHosts, baseG := wasmLive.Load(), runtime.NumGoroutine()
	got := js(t, tab, threadHelpers+`
const module = new WebAssembly.Module(BYTES("`+wb64(wasmThreadsModule())+`"));
const memory = new WebAssembly.Memory({ initial: 1, maximum: 2, shared: true });
const ws = [new Worker('/thread.js'), new Worker('/thread.js')];
const q = ws.map(msgs);
ws.forEach((w) => w.postMessage({ cmd: 'init', module, memory }));
await Promise.all(q.map((n) => n()));
ws[0].postMessage({ cmd: 'wait', addr: 0, exp: 0, timeout: -1n });   // blocked in memory.atomic.wait32 for ever
ws[1].postMessage({ cmd: 'spin', addr: 4, exp: 1 });                  // spinning in wasm for ever
await q[0](); await q[1](); await sleep(150);
return 'running';`)
	if got != "running" {
		t.Fatalf("got %s\nconsole:\n%s", got, consoleText(tab))
	}
	if n := wasmLive.Load(); n < baseHosts+3 {
		t.Fatalf("expected the page and two workers to hold wasm hosts, have %d (was %d)", n, baseHosts)
	}
	if err := tab.Navigate(srv.URL+"/b.html", nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) && (wasmLive.Load() > baseHosts || runtime.NumGoroutine() > baseG+3) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := wasmLive.Load(); n > baseHosts {
		t.Errorf("%d wasm hosts still open after the page left (was %d)", n, baseHosts)
	}
	if n := runtime.NumGoroutine(); n > baseG+3 {
		buf := make([]byte, 1<<18)
		buf = buf[:runtime.Stack(buf, true)]
		parts := strings.Split(string(buf), "\n\n")
		t.Errorf("goroutines %d -> %d\n%s", baseG, n, strings.Join(parts[:min(10, len(parts))], "\n\n"))
	}
}
