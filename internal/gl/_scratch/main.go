package main

import (
	"fmt"
	"time"

	"modernc.org/quickjs"
)

func main() {
	vm, _ := quickjs.NewVM()
	defer vm.Close()
	var last any
	vm.RegisterHostFunc("h", func(a []any) (any, error) { last = a[0]; return nil, nil })
	vm.RegisterHostFunc("b64", func(a []any) (any, error) { return string(make([]byte, 4915200)), nil })
	tm := func(name, js string) {
		t := time.Now()
		_, err := vm.Eval(js, quickjs.EvalGlobal)
		fmt.Println(name, time.Since(t), err)
	}
	tm("init", `var big=new Uint8Array(3686400); big.fill(7); var f=new Float32Array(big.buffer); `)
	tm("toBase64", `var s=big.toBase64(); h(s.length)`)
	tm("toBase64 view", `var s2=new Uint8Array(f.buffer,f.byteOffset,f.byteLength).toBase64(); h(s2.length)`)
	tm("h", `h(s)`)
	tm("fromBase64", `var q=Uint8Array.fromBase64(s); h(q.length)`)
	tm("fromHost+fromBase64", `var q=Uint8Array.fromBase64(b64()); h(q.length)`)
	fmt.Println(last)
	tm("setFromBase64", `var q=new Uint8Array(3686400); q.setFromBase64(b64()); h(q.length)`)
}
