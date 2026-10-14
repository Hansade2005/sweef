package engine

import (
	"fmt"
	"os"
	"testing"
)

// wasmAdd: add(a,b) = a + b, plus addBig(a,b) on i64.
func wasmAdd() []byte {
	return mod(
		types(ft("ii", "i"), ft("ll", "l")),
		funcs(0, 1),
		exports(export("add", wkFunc, 0), export("addBig", wkFunc, 1)),
		codes(
			body(0x20, 0x00, 0x20, 0x01, 0x6a, 0x0b),
			body(0x20, 0x00, 0x20, 0x01, 0x7c, 0x0b),
		),
	)
}

// wasmCrunch exports crunch(n): a loop of n iterations of integer mixing, entirely inside wasm.
func wasmCrunch() []byte {
	code := []byte{
		0x02, 0x40, // block
		0x03, 0x40, //  loop
		0x20, 0x01, 0x20, 0x00, 0x4e, // i >= n (i32.ge_s)
		0x0d, 0x01, // br_if 1 (exit)
		0x20, 0x02, 0x41, 0xe7, 0x07, 0x6c, // acc * 999
		0x20, 0x01, 0x6a, // + i
		0x21, 0x02, // acc = ...
		0x20, 0x01, 0x41, 0x01, 0x6a, 0x21, 0x01, // i++
		0x0c, 0x00, //  br 0
		0x0b, 0x0b, // end loop, end block
		0x20, 0x02, 0x0b, // acc
	}
	b := append([]byte{0x01, 0x02, 0x7f}, code...) // one local group: locals 1 and 2 are i32
	fn := append(uleb(uint64(len(b))), b...)
	return mod(types(ft("i", "i")), funcs(0), exports(export("crunch", wkFunc, 0)), codes(fn))
}

// wasmBig generates a module with n functions, each a straight-line mix of arithmetic, to measure compile cost.
func wasmBig(n int) []byte {
	var bodies [][]byte
	var idx []byte
	var exp [][]byte
	for i := 0; i < n; i++ {
		code := []byte{0x20, 0x00}
		for k := 0; k < 40; k++ {
			code = append(code, 0x41, byte(k+1), 0x6a, 0x41, 0x03, 0x6c, 0x20, 0x00, 0x73) // (x + k) * 3 ^ param
		}
		code = append(code, 0x0b)
		bodies = append(bodies, body(code...))
		idx = append(idx, 0)
		if i < 3 {
			exp = append(exp, export(fmt.Sprintf("f%d", i), wkFunc, byte(i)))
		}
	}
	return mod(types(ft("i", "i")), wsection(3, append(uleb(uint64(n)), idx...)), exports(exp...), wsection(10, wvec(bodies)))
}

// TestWasmMeasure prints the overhead numbers quoted in docs/webassembly.md:
//
//	SWEEF_WASM_MEASURE=1 go test ./internal/engine -run TestWasmMeasure -v -count=1
func TestWasmMeasure(t *testing.T) {
	if os.Getenv("SWEEF_WASM_MEASURE") == "" {
		t.Skip("set SWEEF_WASM_MEASURE=1 to print the overhead measurements")
	}
	tab := wasmTab(t, func(o *Options) { o.JSTimeoutMs = 120000 })
	measure := func(name, setup, loop string, n int) {
		src := fmt.Sprintf(`(async function(){ %s; const N=%d; const t0=performance.now(); %s; return performance.now()-t0; })()`, setup, n, loop)
		raw, err := tab.Evaluate(src)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var ms float64
		fmt.Sscan(string(raw), &ms)
		t.Logf("%-36s %10.1f ns/op   (n=%d, %.1f ms total)", name, ms*1e6/float64(n), n, ms)
	}
	measure("first touch of WebAssembly (install)", ``, `typeof WebAssembly.Module;`, 1)
	measure("JS loop, same arithmetic (QuickJS)", ``, `let acc=0; for(let i=0;i<N;i++){ acc = (Math.imul(acc, 999) + i) | 0; }`, 5000000)
	add := `const ex=(await WebAssembly.instantiate(` + jsBytes(wasmAdd()) + `)).instance.exports; const jsAdd=(a,b)=>a+b|0;`
	measure("JS function call (baseline)", add, `let s=0; for(let i=0;i<N;i++) s=jsAdd(s,i);`, 200000)
	measure("host call floor (performance.now)", add, `let s=0; for(let i=0;i<N;i++) s=performance.now();`, 200000)
	measure("JS -> wasm add(i32,i32)", add, `let s=0; for(let i=0;i<N;i++) s=ex.add(s,i);`, 200000)
	measure("JS -> wasm addBig(i64,i64)", add, `let s=0n; for(let i=0n;i<BigInt(N);i++) s=ex.addBig(s,i);`, 50000)
	cb := `let acc=0; const ex=(await WebAssembly.instantiate(` + jsBytes(wasmCallback()) + `,{env:{cb(x){acc+=x; return x;}}})).instance.exports;`
	measure("JS -> wasm -> JS (import call)", cb, `for(let i=0;i<N;i++) ex.run(i);`, 100000)
	crunch := `const ex=(await WebAssembly.instantiate(` + jsBytes(wasmCrunch()) + `)).instance.exports;`
	measure("wasm loop, 100M iterations", crunch, `ex.crunch(N);`, 100000000)
	mem := `const ex=(await WebAssembly.instantiate(` + jsBytes(wasmMemory()) + `)).instance.exports;`
	measure("memory.buffer getter", mem, `let b; for(let i=0;i<N;i++) b=ex.memory.buffer;`, 200000)
	measure("typed-array write + wasm load", mem, `const u=new Uint32Array(ex.memory.buffer); let s=0; for(let i=0;i<N;i++){u[i&1023]=i; s+=ex.load((i&1023)*4);}`, 100000)
	big := wasmBig(1500)
	t.Logf("generated module: %d bytes, 1500 functions", len(big))
	bigSetup := `globalThis.__big = ` + jsBytes(big)
	measure("(build the 550 KB byte array in JS)", ``, bigSetup, 1)
	measure("compile+instantiate big module", ``, `await WebAssembly.instantiate(__big);`, 1)
	measure("same bytes again", ``, `await WebAssembly.instantiate(__big);`, 1)
	measure("validate big module", ``, `WebAssembly.validate(__big);`, 1)
	measure("instantiate tiny module", `const m=new WebAssembly.Module(`+jsBytes(wasmAdd())+`)`, `for(let i=0;i<N;i++) new WebAssembly.Instance(m);`, 200)
	measure("new WebAssembly.Module(tiny)", ``, `for(let i=0;i<N;i++) new WebAssembly.Module(`+jsBytes(wasmAdd())+`);`, 200)
}
