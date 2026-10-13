# WebAssembly

Sweef implements the WebAssembly JavaScript API on [wazero](https://wazero.io) (pure Go, Apache-2.0, no
dependencies, no cgo). Pages and Web Workers get a global `WebAssembly` that behaves like the one in a browser for
the features listed below; modules run as native code where wazero's compiler supports the platform (linux, darwin,
freebsd, netbsd, dragonfly, solaris and windows on amd64 and arm64) and on wazero's interpreter everywhere else.

The code is in `internal/engine`: `wasm.go` (host, instantiation, memory/table/global), `wasm_run.go` (where wasm runs
and the hand-off to the page thread), `wasm_qjs.go` (ArrayBuffer bridge), `wasm_bin.go` (binary reader/assembler),
`wasm_prelude.js` (the JS API). Tests are `wasm*_test.go`.

## What works

| Area | Status |
|---|---|
| `WebAssembly.Module`, `Instance`, `Memory`, `Table`, `Global` | yes |
| `validate`, `compile`, `instantiate` (bytes overload and Module overload) | yes |
| `compileStreaming`, `instantiateStreaming` | yes: takes a `Response` or a promise of one, checks `Content-Type: application/wasm` and `ok` |
| `CompileError`, `LinkError`, `RuntimeError` | yes, real `Error` subclasses with the spec names |
| `Module.exports()`, `imports()`, `customSections()` | yes |
| Exported functions callable from JS | yes; `name` is the function index, `length` the parameter count |
| JS functions imported by wasm | yes, including wasm -> JS -> wasm re-entrancy (also of the same function) and JS exceptions crossing wasm frames unchanged |
| i64 <-> `BigInt` | yes; a Number passed for an `i64` parameter is a `TypeError`, as in browsers |
| i32, f32, f64, multi-value results | yes (multiple results come back as an `Array`) |
| `Memory.buffer` is an ArrayBuffer over the live linear memory | yes, zero copy (see below); detached and replaced on `grow` |
| `Memory.grow`, `memory.grow` in wasm, `Memory` imported by several instances | yes |
| `Table`: `length`, `get`, `set`, `grow`, importing and exporting funcref tables | yes (not `externref` tables) |
| `Global`: i32, i64, f32, f64, mutable and immutable, plain numbers as immutable global imports | yes |
| Workers | yes: a worker has the same `WebAssembly` (`worker_prelude.js`), its own runtime, and `fetch().arrayBuffer()` returns exact bytes |
| Core features | wasm 2.0 (sign extension, saturating conversions, multi-value, bulk memory, reference types, mutable globals, SIMD) plus tail calls and extended constants; the exception-handling opcodes are enabled in wazero but not covered by the tests |

## What does not

* **Threads and `SharedArrayBuffer`.** Modules with shared memory or atomics fail with a `CompileError`; `new
  WebAssembly.Memory({shared: true})` throws. Not required for this work and wazero's support is partial.
* **`WebAssembly.Tag`, `WebAssembly.Exception`, `JSTag`.** A module may use exception-handling instructions that
  throw and catch inside wasm, but it cannot import or export a tag (a tag import is a `LinkError`, tag exports are
  left out of `exports`).
* **`externref`, `v128` and other reference types at the JS boundary.** A function whose signature mentions them is
  exported, but calling it throws `TypeError`. `Table` with `element: 'externref'` and `Global` of those types throw.
* **Multi-memory, memory64, the GC proposal, stack switching, JS String builtins.**
* **Structured clone of `Module`/`Memory`.** `postMessage` to a worker is JSON in this engine, so a module cannot be
  sent to a worker; the worker compiles its own (the process-wide compilation cache makes that cheap).
* **Garbage collection of instances.** An instance (native code, linear memory) lives until the page navigates or the
  tab closes. A page that makes thousands of instances holds them all.

Small deviations from browsers: `compile` and `instantiate` do their work synchronously and return an already
settled promise; the trap message for a bad or null `call_indirect` is `table index is out of bounds or null
function` (wazero reports both as one error); `table.get(i)` returns a function object per table-entry value, so
`t.get(0) === exportedFn` is false even when it is the same function.

## How it works

**Lazy.** `WebAssembly` is an accessor on the global object. The first access evaluates `wasm_prelude.js`, registers
the host functions and creates the wazero runtime (3.8 ms measured). Pages that never look at `WebAssembly` pay one
registered host function per VM and nothing else, and the prelude bytecode cache is unaffected.

**One wazero runtime per JS VM** (page or worker), configured with the compiler where available, a shared
process-wide in-memory compilation cache (rotated after about 192 MB of compiled input, so it cannot grow without
bound), and a 512 MiB cap on every linear memory. Everything the page instantiates lives in that runtime.

**Memory.buffer.** `modernc.org/quickjs` has no ArrayBuffer API: a JS object that crosses the host boundary arrives as
a JSON string, and an ArrayBuffer cannot be created from Go. True sharing is nevertheless possible, because the QuickJS
C library is transpiled Go (`modernc.org/libquickjs`) whose pointers are real addresses: `wasm_qjs.go` reads the
`(tls, ctx)` pair and the raw `JSValue` out of the unexported fields of `quickjs.VM` and `quickjs.Value` with
`reflect`/`unsafe`, then calls `JS_NewArrayBuffer` with a pointer into wazero's memory slice (no free function, so
QuickJS never frees it) and `JS_GetArrayBuffer`/`JS_DetachArrayBuffer` for the other directions. So `memory.buffer`
is the linear memory: wasm stores are visible through typed arrays immediately and the other way round, there is no
copy-in/copy-out at call boundaries and no cost per byte. The Go slice behind an ArrayBuffer stays referenced until
the buffer is detached, so the garbage collector cannot reuse it under the VM. When the memory grows or moves, the
old ArrayBuffer is detached (length 0, as the spec says) on the next edge back into JS and `memory.buffer` hands out
a new one. The layout assumptions are checked by a self-test the first time `WebAssembly` is used
(`TestWasmBridgeSelfTest`); if a `modernc.org/quickjs` upgrade changes them, `WebAssembly` is simply undefined and
that test fails loudly. A copy-based fallback was not written because the bridge is verified at run time.

**Memory, Table, Global created from JS** are tiny generated modules ("providers") that export the object. Importing
one, or an export of another instance, is wired with a generated module per import module name that re-exports the
right providers under the names the guest imports. The guest's bytes are never rewritten, so a `WebAssembly.Module`
is compiled once however many times it is instantiated. wazero has no public table API: `Table.length/get/set/grow`
use the fields and methods of its internal table type through `reflect`, and `experimental/table.LookupFunction`
with the signatures present in the VM's modules to turn an entry back into a callable function. JS functions placed
in a table by an element segment are recognised by their funcref value. `TestWasmExportedTable` and
`TestWasmImportedTableHoldsHostAndForeignFunctions` pin this, also on the interpreter.

**Calls.** An export is a JS function that coerces its arguments (`|0`, `BigInt.asIntN(64, ...)`, `Math.fround`,
`+x`) and makes one host call with a function id. wasm -> JS goes through a generated host module that calls one JS
dispatcher; an exception thrown by the JS function is stored, the wasm frames are unwound with a Go panic that wazero
turns into an error, and the original exception object is rethrown by the JS wrapper of the outermost call. Traps
become `WebAssembly.RuntimeError` with V8's messages (`unreachable`, `divide by zero`, `memory access out of
bounds`, ...) and stack exhaustion becomes `RangeError`. An `api.Function` owns one native stack and must not be
re-entered, so a re-entrant activation takes a second function object from a small pool.

**Where wasm runs.** Outermost calls run on a runner goroutine per VM; the page thread waits for them and also
executes the JS callbacks they ask for (the QuickJS VM has exactly one thread using it; running callbacks on the
runner measured 2x slower). The hand-off is atomics with a spin of a few tens of microseconds before either side
parks. A call that is already inside wasm (wasm -> JS -> wasm) runs inline. Why not wazero's own interruption: its
`WithCloseOnContextDone` makes the compiler leave native code on every loop iteration, 26x slower on a tight loop
(table below), so it is not used. The cost is that compiled wasm cannot be killed.

## Limits

| Limit | Value | Why |
|---|---|---|
| Linear memory per memory | 8192 pages = 512 MiB; a module that declares more can grow only that far, `grow` fails with -1 / `RangeError` | bounds a page's memory; wazero `WithMemoryLimitPages` |
| Module size | 128 MiB | |
| Time for one call from JS into wasm | six times the page script budget (`JSTimeoutMs`, so 30 s by default); time spent in JS callbacks does not count | watchdog, see below |
| Page script clock | time spent inside wasm is returned to the JS interrupt clock after each call | a long legitimate wasm call must not get the script that made it killed on return |
| Compile | synchronous on the page thread | |

**A runaway call.** If a call exceeds its budget the page thread stops waiting: the call fails with a
`RuntimeError`, the VM's WebAssembly host is poisoned (every later call into it is a `RuntimeError`, memory can no
longer be grown, the wasm code can no longer call into JS, an import it makes ends the call), and the page carries on.
The abandoned call cannot be stopped, so it keeps one CPU busy until it returns by itself; when it does, or when the tab
closes and it has returned, the runtime is freed. `TestWasmRunawayCallIsAbandoned` covers this with a loop the test
ends by writing to the shared memory, which also demonstrates that the abandoned module's memory stays valid.

**Teardown.** Closing the tab, navigating, or closing the engine closes the wazero runtime of the page and of each of
its workers (native code, linear memory, runner goroutine); `TestWasmTeardownFreesEverything` and
`TestWasmNavigationAndTabCloseFreeTheRuntime` assert that no host stays open and that no goroutine is left, and
`TestNoGoroutineLeakAcrossAppLifetimes` in `internal/app` still passes. A process-wide compilation cache outlives
tabs by design and is freed when its last runtime closes after a rotation.

## Measured cost

Linux amd64 virtual machine, 4 vCPU Intel Xeon at 2.1 GHz, Go 1.26, wazero 1.12.0 compiler engine. Reproduce with

    SWEEF_WASM_MEASURE=1 go test ./internal/engine -run TestWasmMeasure -v -count=1

Ranges are the spread over repeated runs on a shared machine (one run under load was about twice as slow).

| What | Cost |
|---|---|
| Plain JS function call (reference) | 0.14-0.17 us |
| Cheapest QuickJS -> Go host call (`performance.now()`) | 1.0-1.3 us |
| JS -> wasm call, `add(i32, i32)` | 3.1-3.5 us |
| JS -> wasm call with `i64` arguments and result (BigInt) | 11-12.5 us |
| JS -> wasm -> JS (an import called once per export call) | 14-15 us |
| `memory.buffer` read | 1.1-1.3 us (no per-byte cost) |
| write through a typed array, then `load` in wasm | 3.3-3.8 us (the call, nothing else) |
| 100 M iterations of an integer loop inside wasm | 1.4-1.6 ns per iteration |
| the same loop in JavaScript on QuickJS | 152 ns per iteration (about 100x slower) |
| the same loop on wazero's interpreter | 78-80 ns per iteration |
| the same loop with wazero's `WithCloseOnContextDone` (not used) | 37-39 ns per iteration |
| first touch of `WebAssembly` in a page | 3.8 ms |
| compile and instantiate a 550 KB, 1500-function module | about 105 ms |
| the same bytes again (compilation cache hit) | 8-11 ms |
| `WebAssembly.validate` of that module | 7 ms |
| `new WebAssembly.Module` for a tiny module (two functions) | 43-47 us |
| `new WebAssembly.Instance` of it | 90-170 us |
| Size of the `sweef` binary | 36.3 MB to 40.0 MB (+3.7 MB, +10%) |

What the numbers say: compute inside wasm is two orders of magnitude faster than the same code in the JS interpreter,
but every crossing of the JS boundary costs microseconds because each crossing is a QuickJS host call (reflection in
`modernc.org/quickjs`) plus argument coercion. Code that calls a tiny wasm function or a tiny JS import tens of
thousands of times per frame (some WebGL-through-wasm loops) will be dominated by that, and a design that crosses
less often is faster. `i64` costs three times an `i32` because BigInt crosses the bridge as text.

The tight loop above is the worst case for the interruption trade-off; programs with more work per loop iteration lose
less with `WithCloseOnContextDone`, but the engine keeps the fast mode and accepts the runaway-call behaviour above.

## Tests

`go test ./internal/engine -run 'TestWasm'` takes about 3 seconds and uses no network. Modules are hand-assembled byte
arrays (factorial on i64, memory with load/store/grow and a data segment, an imported JS callback, call_indirect
through an exported table, globals, traps, multi-value, a start function that traps, a "puts" program in the shape a C
toolchain emits) built with the section helpers at the top of `wasm_test.go`; `instantiateStreaming` runs against an
`httptest` server in the style of `featureServer`; Workers, the interpreter fallback and teardown each have tests.
