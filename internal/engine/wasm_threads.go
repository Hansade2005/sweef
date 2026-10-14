package engine

// wasm_threads.go — WebAssembly threads: shared memories, atomics, and sharing them with Workers.
//
// What wazero provides: the threads proposal (shared memory, the atomic instructions, memory.atomic.wait/notify)
// behind experimental.CoreFeaturesThreads. What it does not provide is a way for two runtimes to see one memory,
// and every Worker has a runtime of its own (docs/webassembly.md). So the instances that use threads live in one
// process-wide *shared runtime* instead:
//
//   - a module that defines a shared memory, imports a memory created in the shared runtime, or needs the
//     threads feature to compile, is compiled and instantiated in the shared runtime, whichever VM asks;
//   - a WebAssembly.Memory({shared: true}) is a provider module there. Instances in different VMs (the page and its
//     workers) that import it resolve to the very same wazero MemoryInstance: one buffer, one wait queue, and
//     memory.grow seen by all of them;
//   - the guest is still driven from the VM that instantiated it: calls into it run on that host's runner
//     goroutine and its JS imports are served by that VM's thread, exactly as for private instances;
//   - Memory.buffer of a shared memory is a SharedArrayBuffer over the memory (sab.go), so JavaScript Atomics
//     and memory.atomic.wait/notify use the memory's own wait queue (sabWasm);
//   - a WebAssembly.Module and a shared Memory are posted to workers by reference (a share id in the agent
//     cluster, sab.go); the receiver compiles the same bytes (the compilation cache makes that cheap) or
//     imports the same memory.
//
// Lifetime. Threads block inside wasm (pthread_cond_wait is memory.atomic.wait32) for as long as the program
// likes, and a native call cannot be interrupted, so
//   - a host that runs threaded instances has no call time budget (a worker thread's start routine is one
//     call that returns when the thread exits);
//   - when the last VM that uses a shared memory goes away the memory is retired: its length is set to zero and
//     every waiter is woken, so a thread that is still running or waiting traps on its next access and its
//     goroutine ends. Terminating one worker that is blocked in wasm does not free it before that.

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/experimental"
)

const (
	// wasmSharedMemLimitPages caps a shared memory (it is reserved up front, because it can never move): 16384
	// pages = 1 GiB of address space, committed by the operating system only as it is touched.
	wasmSharedMemLimitPages = 16384
)

var wasmSharedRT struct {
	mu   sync.Mutex
	rt   wazero.Runtime
	gen  *wasmCacheGen
	refs int
}

var wasmGlobalSeq atomic.Int64

func acquireSharedRuntime() wazero.Runtime {
	s := &wasmSharedRT
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rt == nil {
		s.gen = acquireWasmCache()
		base := wazero.NewRuntimeConfig()
		if wasmForceInterpreter {
			base = wazero.NewRuntimeConfigInterpreter()
		}
		cfg := base.
			WithCoreFeatures(api.CoreFeaturesV2 | experimental.CoreFeaturesTailCall | experimental.CoreFeaturesExtendedConst | experimental.CoreFeaturesExceptionHandling | experimental.CoreFeaturesThreads).
			WithMemoryLimitPages(wasmSharedMemLimitPages).
			WithCompilationCache(s.gen.c)
		s.rt = wazero.NewRuntimeWithConfig(context.Background(), cfg)
	}
	s.refs++
	return s.rt
}

func releaseSharedRuntime() {
	s := &wasmSharedRT
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refs--
	if s.refs == 0 && s.rt != nil {
		_ = s.rt.Close(context.Background())
		s.gen.release()
		s.rt, s.gen = nil, nil
	}
}

// sharedRuntime is the shared runtime for this host (acquired on first use, released when the host closes). A
// host that uses it runs threads: its calls get no time budget.
func (h *wasmHost) sharedRuntime() wazero.Runtime {
	if h.srt == nil {
		h.srt = acquireSharedRuntime()
	}
	h.threads = true
	return h.srt
}

// ---- shared memory objects -------------------------------------------------------------------------------

// wasmSharedMem wraps a wazero shared MemoryInstance. It implements sabWasm.
type wasmSharedMem struct {
	mem     api.Memory
	full    []byte // the whole reserved region (cap of the instance's Buffer)
	refs    atomic.Int32
	retired atomic.Bool

	wait32, wait64, notifyM reflect.Value
	rd32, rd64              reflect.Value
	waiters                 *sync.Map
	lenp                    *uintptr
}

var wasmSharedMems sync.Map // api.Memory -> *wasmSharedMem

// sharedMemOf returns the shared-memory object for mem (nil when wazero's internals do not look as expected: the
// threads layer then reports an error rather than guessing).
func sharedMemOf(mem api.Memory) (m *wasmSharedMem) {
	if v, ok := wasmSharedMems.Load(mem); ok {
		return v.(*wasmSharedMem)
	}
	defer func() {
		if recover() != nil {
			m = nil
		}
	}()
	rv := reflect.ValueOf(mem)
	if rv.Kind() != reflect.Pointer {
		return nil
	}
	el := rv.Elem()
	buf := el.FieldByName("Buffer")
	if !buf.IsValid() || buf.Kind() != reflect.Slice || !el.FieldByName("Shared").Bool() {
		return nil
	}
	m = &wasmSharedMem{mem: mem}
	m.full = buf.Slice(0, buf.Cap()).Bytes()
	m.wait32, m.wait64, m.notifyM = rv.MethodByName("Wait32"), rv.MethodByName("Wait64"), rv.MethodByName("Notify")
	if !m.wait32.IsValid() || !m.wait64.IsValid() || !m.notifyM.IsValid() {
		return nil
	}
	m.rd32 = reflect.MakeFunc(m.wait32.Type().In(3), func(a []reflect.Value) []reflect.Value {
		return []reflect.Value{reflect.ValueOf(atomic.LoadUint32((*uint32)(unsafe.Pointer(&m.full[a[1].Uint()]))))}
	})
	m.rd64 = reflect.MakeFunc(m.wait64.Type().In(3), func(a []reflect.Value) []reflect.Value {
		return []reflect.Value{reflect.ValueOf(atomic.LoadUint64((*uint64)(unsafe.Pointer(&m.full[a[1].Uint()]))))}
	})
	if wf := el.FieldByName("waiters"); wf.IsValid() {
		m.waiters = reflect.NewAt(wf.Type(), unsafe.Pointer(wf.UnsafeAddr())).Interface().(*sync.Map)
	}
	m.lenp = (*uintptr)(unsafe.Add(unsafe.Pointer(buf.UnsafeAddr()), unsafe.Sizeof(uintptr(0))))
	if act, loaded := wasmSharedMems.LoadOrStore(mem, m); loaded {
		return act.(*wasmSharedMem)
	}
	return m
}

func (m *wasmSharedMem) wait(off uint32, size int, exp int64, timeoutNs int64) int {
	var out []reflect.Value
	if size == 8 {
		out = m.wait64.Call([]reflect.Value{reflect.ValueOf(off), reflect.ValueOf(uint64(exp)), reflect.ValueOf(timeoutNs), m.rd64})
	} else {
		if exp < math.MinInt32 || exp > math.MaxInt32 {
			return 1 // "not-equal": no 32-bit cell can hold the expected value
		}
		out = m.wait32.Call([]reflect.Value{reflect.ValueOf(off), reflect.ValueOf(uint32(int32(exp))), reflect.ValueOf(timeoutNs), m.rd32})
	}
	return int(out[0].Uint())
}

func (m *wasmSharedMem) notify(off uint32, count uint32) uint32 {
	return uint32(m.notifyM.Call([]reflect.Value{reflect.ValueOf(off), reflect.ValueOf(count)})[0].Uint())
}

// retire is called when the last VM that uses the memory is gone.
func (m *wasmSharedMem) retire() {
	if !m.retired.CompareAndSwap(false, true) {
		return
	}
	atomic.StoreUintptr(m.lenp, 0) // every later access, also by compiled code, is out of bounds
	if m.waiters != nil {
		m.waiters.Range(func(k, _ any) bool {
			if off, ok := k.(uint32); ok {
				m.notify(off, 1<<31)
			}
			return true
		})
	}
	wasmSharedMems.Delete(m.mem)
}

func (m *wasmSharedMem) acquire() { m.refs.Add(1) }

func (m *wasmSharedMem) release() {
	if m.refs.Add(-1) <= 0 {
		m.retire()
	}
}

// ---- host-side bookkeeping --------------------------------------------------------------------------------

// holdShared records that this host uses sm (once), so closing the host releases its reference.
func (h *wasmHost) holdShared(sm *wasmSharedMem) {
	if h.smems == nil {
		h.smems = map[*wasmSharedMem]bool{}
	}
	if !h.smems[sm] {
		h.smems[sm] = true
		sm.acquire()
	}
}

// ownShared records a module this host created in the shared runtime; it is closed with the host.
func (h *wasmHost) ownShared(m api.Module) {
	if m != nil {
		h.sOwned = append(h.sOwned, m)
	}
}

// releaseSharedMems drops this host's references to shared memories (the last one retires the memory).
func (h *wasmHost) releaseSharedMems() {
	sms := h.smems
	h.smems = nil
	for sm := range sms {
		sm.release()
	}
}

func (h *wasmHost) closeSharedUse() {
	for i := len(h.sOwned) - 1; i >= 0; i-- {
		_ = h.sOwned[i].Close(h.ctx)
	}
	h.sOwned = nil
	for _, cm := range h.sCompile {
		_ = cm.Close(h.ctx)
	}
	h.sCompile = nil
	h.releaseSharedMems()
	if h.srt != nil {
		h.srt = nil
		releaseSharedRuntime()
	}
}

// isThreadsError tells a compile failure caused by the threads feature being disabled in the private runtime.
func isThreadsError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, `feature "threads" is disabled`) || strings.Contains(s, "threads feature not enabled") || strings.Contains(s, "shared memory requested")
}

// ---- sharing with other VMs (postMessage) -----------------------------------------------------------------

func (h *wasmHost) cluster() *agentCluster {
	if h.jsv == nil {
		return nil
	}
	return h.jsv.agentCluster()
}

// memShare(key) -> share id of a shared memory
func (h *wasmHost) memShare(a []any) (any, error) {
	r := h.refs[argS(a, 0)]
	cl := h.cluster()
	if r == nil || r.kind != wkMemory || cl == nil {
		return nil, fmt.Errorf("TypeError:not a memory")
	}
	if !r.shared {
		return nil, fmt.Errorf("DataCloneError:#<Memory> could not be cloned.")
	}
	sm := sharedMemOf(r.memory())
	if sm == nil {
		return nil, fmt.Errorf("DataCloneError:#<Memory> could not be cloned.")
	}
	return int(cl.putObj(&sharedMemShare{mod: r.mod, name: r.name, sm: sm})), nil
}

type sharedMemShare struct {
	mod  api.Module
	name string
	sm   *wasmSharedMem
}

// memOpen(shareId) -> key of a memory in this host that is the same memory
func (h *wasmHost) memOpen(a []any) (any, error) {
	cl := h.cluster()
	if cl == nil {
		return nil, fmt.Errorf("DataCloneError:no agent cluster")
	}
	sh, _ := cl.getObj(uint64(argI(a, 0))).(*sharedMemShare)
	if sh == nil {
		return nil, fmt.Errorf("DataCloneError:the memory is not available to this agent cluster")
	}
	h.sharedRuntime()
	key := fmt.Sprintf("swx%d/%s", h.id(), sh.name)
	h.refs[key] = &wref{key: key, kind: wkMemory, mod: sh.mod, name: sh.name, shared: true, shRT: true}
	h.holdShared(sh.sm)
	return key, nil
}

// modShare(modId) -> share id of a compiled module (its bytes)
func (h *wasmHost) modShare(a []any) (any, error) {
	m := h.mods[int(argI(a, 0))]
	cl := h.cluster()
	if m == nil || cl == nil {
		return nil, fmt.Errorf("TypeError:not a module")
	}
	return int(cl.putObj(m.bytes)), nil
}

// modOpen(shareId) -> {id, imports, exports} like compile
func (h *wasmHost) modOpen(a []any) (any, error) {
	cl := h.cluster()
	if cl == nil {
		return nil, fmt.Errorf("DataCloneError:no agent cluster")
	}
	b, _ := cl.getObj(uint64(argI(a, 0))).([]byte)
	if b == nil {
		return nil, fmt.Errorf("DataCloneError:the module is not available to this agent cluster")
	}
	return h.compileSource(b), nil
}

// compileModule compiles src in the private runtime, or in the shared one when the module needs threads.
func (h *wasmHost) compileModule(src []byte, info *wInfo) (cm wazero.CompiledModule, shared bool, err error) {
	if !info.definesShared {
		cm, err = h.rt.CompileModule(h.ctx, src)
		if err == nil || !isThreadsError(err) {
			return cm, false, err
		}
	}
	cm, err = h.sharedRuntime().CompileModule(h.ctx, src)
	return cm, true, err
}

// compiledFor returns wm compiled for the shared runtime (compiling again from its bytes if it was compiled
// privately: the compilation cache makes the second compile cheap).
func (h *wasmHost) compiledFor(wm *wmodule, shared bool) (wazero.CompiledModule, error) {
	if shared == wm.shared {
		return wm.compiled, nil
	}
	if wm.other == nil {
		var cm wazero.CompiledModule
		var err error
		if shared {
			cm, err = h.sharedRuntime().CompileModule(h.ctx, wm.bytes)
			if err == nil {
				h.sCompile = append(h.sCompile, cm)
			}
		} else {
			cm, err = h.rt.CompileModule(h.ctx, wm.bytes)
		}
		if err != nil {
			return nil, err
		}
		wm.other = cm
	}
	return wm.other, nil
}
