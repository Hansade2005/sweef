package engine

import "testing"

// The same behaviour must hold on wazero's interpreter, the fallback on platforms without its compiler.
func TestWasmOnTheInterpreter(t *testing.T) {
	wasmForceInterpreter = true
	t.Cleanup(func() { wasmForceInterpreter = false })
	for name, fn := range map[string]func(*testing.T){
		"factorial":    TestWasmFactorialAndBigInt,
		"memory":       TestWasmMemoryIsSharedAndGrows,
		"callbacks":    TestWasmImportedJSCallbackAndReentrancy,
		"reentrancy":   TestWasmSameFunctionReentrancy,
		"table":        TestWasmExportedTable,
		"hostInTable":  TestWasmImportedTableHoldsHostAndForeignFunctions,
		"traps":        TestWasmTrapsAreRuntimeErrors,
		"globals":      TestWasmGlobals,
		"sharedMemory": TestWasmSharedMemoryBetweenInstances,
		"multiValue":   TestWasmMultiValueAndFloats,
		"hello":        TestWasmHelloSmoke,
	} {
		t.Run(name, fn)
	}
}
