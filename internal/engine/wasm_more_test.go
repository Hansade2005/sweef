package engine

import "testing"

// wasmMulti: multi-value results and f32/f64 values. swap(i32,f64)->(f64,i32), fadd(f32,f32)->f32,
// three()->(i32,i32,i32); sum() calls the imported env.pair()->(i32,i32) and adds the two results.
func wasmMulti() []byte {
	return mod(
		types(ft("id", "di"), ft("ff", "f"), ft("", "iii"), ft("", "ii"), ft("", "i")),
		wsection(2, wvec([][]byte{importFn("env", "pair", 3)})),
		funcs(0, 1, 2, 4),
		exports(export("swap", wkFunc, 1), export("fadd", wkFunc, 2), export("three", wkFunc, 3), export("sum", wkFunc, 4)),
		codes(
			body(0x20, 0x01, 0x20, 0x00, 0x0b),
			body(0x20, 0x00, 0x20, 0x01, 0x92, 0x0b),
			body(0x41, 0x01, 0x41, 0x02, 0x41, 0x03, 0x0b),
			body(0x10, 0x00, 0x6a, 0x0b),
		),
	)
}

func TestWasmMultiValueAndFloats(t *testing.T) {
	tab := wasmTab(t, nil)
	got := runWasm(t, tab, `
    const ex = (await WebAssembly.instantiate(`+jsBytes(wasmMulti())+`, { env: { pair: () => [4, 5] } })).instance.exports;
    const sum2 = (await WebAssembly.instantiate(`+jsBytes(wasmMulti())+`, { env: { pair: () => new Set([30, 12]) } })).instance.exports.sum();
    let bad = ''; try { (await WebAssembly.instantiate(`+jsBytes(wasmMulti())+`, { env: { pair: () => [1] } })).instance.exports.sum(); } catch (e) { bad = e.name; }
    return { swap: ex.swap(7, 2.5), fadd: ex.fadd(1.1, 2.2) === Math.fround(Math.fround(1.1) + Math.fround(2.2)), three: ex.three(), sum: ex.sum(), sum2, bad,
             isArray: Array.isArray(ex.three()), lens: [ex.swap.length, ex.fadd.length, ex.three.length] };`)
	wantJSON(t, got, `{"swap":[2.5,7],"fadd":true,"three":[1,2,3],"sum":9,"sum2":42,"bad":"TypeError","isArray":true,"lens":[2,2,0]}`)
}

// wasmTableHost imports env.hostf (i32->i32) and the table env.tbl; its element segment puts hostf and its own
// add1 into the table, and callidx(idx, arg) calls through it.
func wasmTableHost() []byte {
	tblImp := append(append(append(wname("env"), wname("tbl")...), wkTable, wvFunc), wlimits(2, -1)...)
	elem := append([]byte{0x00, 0x41, 0x00, 0x0b}, wvec([][]byte{{0x00}, {0x01}})...)
	return mod(
		types(ft("i", "i"), ft("ii", "i")),
		wsection(2, wvec([][]byte{importFn("env", "hostf", 0), tblImp})),
		funcs(0, 1),
		exports(export("callidx", wkFunc, 2)),
		wsection(9, wvec([][]byte{elem})),
		codes(
			body(0x20, 0x00, 0x41, 0x01, 0x6a, 0x0b),
			body(0x20, 0x01, 0x20, 0x00, 0x11, 0x00, 0x00, 0x0b),
		),
	)
}

func TestWasmImportedTableHoldsHostAndForeignFunctions(t *testing.T) {
	tab := wasmTab(t, nil)
	got := runWasm(t, tab, `
    const tbl = new WebAssembly.Table({ element: 'anyfunc', initial: 2 });
    const a = (await WebAssembly.instantiate(`+jsBytes(wasmTableHost())+`, { env: { tbl, hostf: (x) => x * 100 } })).instance.exports;
    const viaWasm = [a.callidx(0, 3), a.callidx(1, 3)];                 // JS function and wasm function through call_indirect
    const viaJS = [tbl.get(0)(7), tbl.get(1)(7)];
    const dbl = (await WebAssembly.instantiate(`+jsBytes(wasmCallback())+`, { env: { cb: (x) => x } })).instance.exports.double;
    tbl.set(1, dbl);                                                      // a function of another instance
    const swapped = [a.callidx(1, 21), tbl.get(1)(4), tbl.length];
    // exported functions can be imported into another instance, with a type check
    const user = (await WebAssembly.instantiate(`+jsBytes(wasmCallback())+`, { env: { cb: dbl } })).instance.exports.run(5);
    const fac = (await WebAssembly.instantiate(`+jsBytes(wasmFactorial())+`)).instance.exports.fac;
    let mismatch = ''; try { await WebAssembly.instantiate(`+jsBytes(wasmCallback())+`, { env: { cb: fac } }); } catch (e) { mismatch = e.name; }
    return { viaWasm, viaJS, swapped, user, mismatch };`)
	wantJSON(t, got, `{"viaWasm":[300,4],"viaJS":[700,8],"swapped":[42,8,2],"user":11,"mismatch":"LinkError"}`)
}

// wasmGrowAny has a memory without a declared maximum and exports grow(n).
func wasmGrowAny() []byte {
	return mod(
		types(ft("i", "i")),
		funcs(0),
		wsection(5, wvec([][]byte{wlimits(1, -1)})),
		exports(export("grow", wkFunc, 0), export("mem", wkMemory, 0)),
		codes(body(0x20, 0x00, 0x40, 0x00, 0x0b)),
	)
}

func TestWasmMemoryLimitAppliesInsideWasm(t *testing.T) {
	tab := wasmTab(t, nil)
	got := runWasm(t, tab, `
    const ex = (await WebAssembly.instantiate(`+jsBytes(wasmGrowAny())+`)).instance.exports;
    const tooMany = ex.grow(8192);       // 1 + 8192 pages would pass the 512 MiB cap
    const fits = ex.grow(2);
    return { tooMany, fits, pages: ex.mem.buffer.byteLength / 65536 };`)
	wantJSON(t, got, `{"tooMany":-1,"fits":1,"pages":3}`)
}

// The binary reader sits in front of wazero, so it must survive any input: every single-byte corruption and every
// truncation of the sample modules is parsed without a panic.
func TestWasmReaderNeverPanics(t *testing.T) {
	for name, m := range map[string][]byte{
		"factorial": wasmFactorial(), "memory": wasmMemory(), "callback": wasmCallback(), "table": wasmTable(),
		"tableHost": wasmTableHost(), "multi": wasmMulti(), "globals": wasmGlobals(), "hello": wasmHello(),
		"custom": withCustom(wasmFactorial(), "xyz"),
	} {
		if _, err := parseWasm(m); err != nil {
			t.Fatalf("%s: sample does not parse: %v", name, err)
		}
		for i := range m {
			parseWasm(m[:i])
			for _, v := range []byte{0x00, 0x01, 0x7f, 0x80, 0xff} {
				c := append([]byte(nil), m...)
				c[i] = v
				parseWasm(c)
			}
		}
	}
}
