package engine

// jsvm.go — the per-tab JavaScript virtual machine.
//
// Engine: modernc.org/quickjs, QuickJS transpiled to pure Go (ES2023, no
// CGO). The Go DOM stays the source of truth; dom_prelude.js implements the
// browser API in JavaScript on top of the host object `__sw` registered here.
// The contract between the two sides is documented in jsbridge.md.
//
// Threading: a quickjs VM is not goroutine-safe, so every entry into the VM
// goes through jsVM.mu. Host functions run while that lock is held and may
// take Tab.mu for DOM access, so code that holds Tab.mu must never call into
// the VM (see Tab.SetDocument and actions.go, which release it first).

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	fnet "github.com/sweefbrowser/sweef/internal/net"
	"modernc.org/quickjs"
)

//go:embed dom_prelude.js
var domPrelude string

// The prelude is compiled to QuickJS bytecode once per process; loading
// bytecode into a fresh VM takes ~3 ms against ~40 ms to parse the source,
// which is most of the cost of a navigation.
var (
	preludeOnce     sync.Once
	preludeBytecode []byte
)

func compiledPrelude() []byte {
	preludeOnce.Do(func() {
		vm, err := quickjs.NewVM()
		if err != nil {
			return
		}
		defer vm.Close()
		if bc, err := vm.Compile(domPrelude, quickjs.EvalGlobal); err == nil {
			preludeBytecode = bc
		}
	})
	return preludeBytecode
}

type jsVM struct {
	mu     sync.Mutex
	vm     *quickjs.VM
	tab    *Tab
	start  time.Time
	closed bool

	pendingNav  string
	scriptQueue []int64        // script element ids inserted by page code
	executed    map[int64]bool // scripts already run
	session     map[string]string
	currentDoc  *fnet.Doc
}

// newJSVM creates a VM bound to the tab's current document, installs the host
// object and evaluates the prelude. Returns nil (and logs) when the VM cannot
// be created, in which case the tab works in fetch+DOM mode.
func newJSVM(t *Tab, doc *fnet.Doc, docURL string) *jsVM {
	vm, err := quickjs.NewVM()
	if err != nil {
		t.LogConsole("error", "sweef: cannot create JS VM: "+err.Error())
		return nil
	}
	o := t.eng.opts
	mem := o.JSMemoryMiB
	if mem <= 0 {
		mem = 64
	}
	vm.SetMemoryLimit(uintptr(mem) << 20)
	vm.SetMaxStackSize(1 << 20)
	to := time.Duration(o.JSTimeoutMs) * time.Millisecond
	if to <= 0 {
		to = 5 * time.Second
	}
	vm.SetEvalTimeout(to)
	j := &jsVM{vm: vm, tab: t, start: time.Now(), executed: map[int64]bool{}, session: map[string]string{}, currentDoc: doc}
	if err := j.registerHost(); err != nil {
		t.LogConsole("error", "sweef: host registration failed: "+err.Error())
	}
	j.installModuleLoader(docURL)
	loaded := false
	if bc := compiledPrelude(); bc != nil {
		if _, err := vm.EvalBytecode(bc); err == nil {
			loaded = true
		}
	}
	if !loaded {
		if _, err := vm.Eval(domPrelude, quickjs.EvalGlobal); err != nil {
			t.LogConsole("error", "sweef: prelude failed: "+err.Error())
		}
	}
	if _, err := vm.Call("__swSetDocument", docURL, "loading"); err != nil {
		t.LogConsole("error", "sweef: __swSetDocument failed: "+err.Error())
	}
	return j
}

func (j *jsVM) close() {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return
	}
	j.closed = true
	j.vm.SetModuleLoader(nil, nil)
	j.vm.Close()
}

func (j *jsVM) nowMs() float64 { return float64(time.Since(j.start).Microseconds()) / 1000 }

// ---- VM entry points (all take j.mu) -----------------------------------------

// run evaluates classic script source. Errors are logged to the console ring
// with the script name, like a browser's uncaught-exception report.
func (j *jsVM) run(src, name string) (any, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil, ErrClosed
	}
	r, err := j.vm.Eval(src, quickjs.EvalGlobal)
	if err != nil {
		j.tab.LogConsole("error", jsErrorText(err, name))
	}
	return r, err
}

func (j *jsVM) runModule(src, name string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return ErrClosed
	}
	if _, err := j.vm.Eval(src, quickjs.EvalModule); err != nil {
		j.tab.LogConsole("error", jsErrorText(err, name))
		return err
	}
	return nil
}

func jsErrorText(err error, name string) string {
	var qe *quickjs.Error
	if errors.As(err, &qe) {
		s := qe.Name + ": " + qe.Message
		if qe.Stack != "" {
			s += "\n" + strings.TrimRight(qe.Stack, "\n")
		}
		if name != "" {
			s += "\n    in " + name
		}
		return s
	}
	if name != "" {
		return err.Error() + " (in " + name + ")"
	}
	return err.Error()
}

// call invokes a global prelude function and returns its JSON-ish result.
func (j *jsVM) call(fn string, args ...any) (any, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil, ErrClosed
	}
	return j.vm.Call(fn, args...)
}

// jobs drains the microtask queue.
func (j *jsVM) jobs() {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return
	}
	for i := 0; i < 64; i++ {
		n, err := j.vm.ExecutePendingJobs()
		if err != nil {
			j.tab.LogConsole("error", jsErrorText(err, "microtask"))
		}
		if n == 0 {
			return
		}
	}
}

// dispatch fires a DOM event through the prelude; true means the default
// action should proceed (preventDefault was not called).
func (j *jsVM) dispatch(id int64, typ string, init map[string]any) bool {
	if j == nil {
		return true
	}
	if init == nil {
		init = map[string]any{}
	}
	r, err := j.call("__swDispatch", id, typ, init)
	if err != nil {
		j.tab.LogConsole("error", jsErrorText(err, "dispatch "+typ))
		return true
	}
	b, ok := r.(bool)
	return !ok || b
}

func truthyAny(v any) bool {
	switch x := v.(type) {
	case nil, quickjs.Undefined:
		return false
	case bool:
		return x
	case int:
		return x != 0
	case float64:
		return x != 0
	case string:
		return x != ""
	}
	return true
}

// ---- event loop ----------------------------------------------------------------

// pump runs queued scripts, microtasks and due timers until the page is idle
// or the budget is spent. It performs a navigation requested by page code
// (location.href = …, form.submit()) and returns true when one happened.
func (j *jsVM) pump(budget time.Duration) bool {
	if j == nil {
		return false
	}
	deadline := time.Now().Add(budget)
	for {
		j.drainScripts()
		j.jobs()
		next := j.runTimers()
		j.jobs()
		j.flushObservers()
		if nav := j.takeNav(); nav != "" {
			if strings.HasPrefix(nav, "form://") {
				id, _ := strconv.ParseInt(strings.TrimPrefix(nav, "form://"), 10, 64)
				_ = j.tab.SubmitFormBackend(id)
			} else {
				_ = j.tab.Navigate(nav, nil)
			}
			return true
		}
		j.mu.Lock()
		more := len(j.scriptQueue) > 0
		j.mu.Unlock()
		if more {
			continue
		}
		if next < 0 {
			return false
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		wait := time.Duration(next) * time.Millisecond
		if wait > remaining {
			wait = remaining
		}
		if wait > 30*time.Millisecond {
			wait = 30 * time.Millisecond
		}
		if wait > 0 {
			time.Sleep(wait)
		}
	}
}

// settle is the short pump used after every action.
func (t *Tab) settle(d time.Duration) {
	t.mu.RLock()
	j := t.js
	t.mu.RUnlock()
	if j != nil {
		j.pump(d)
	}
}

func (j *jsVM) runTimers() float64 {
	r, err := j.call("__swRunTimers", j.nowMs())
	if err != nil {
		j.tab.LogConsole("error", jsErrorText(err, "timers"))
		return -1
	}
	switch v := r.(type) {
	case int:
		return float64(v)
	case float64:
		return v
	}
	return -1
}

func (j *jsVM) flushObservers() {
	_, _ = j.call("__swFlushMutationObservers")
}

func (j *jsVM) takeNav() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	n := j.pendingNav
	j.pendingNav = ""
	return n
}

func (j *jsVM) lifecycle(name string) {
	if _, err := j.call("__swFireLifecycle", name); err != nil {
		j.tab.LogConsole("error", jsErrorText(err, name))
	}
}

// ---- evaluation for agents ------------------------------------------------------

var errEvalTimeout = errors.New("evaluate: promise did not settle in time")

// evaluate runs an agent expression, awaits a returned promise, and returns the
// JSON-serialised completion value.
func (j *jsVM) evaluate(expr string) (json.RawMessage, error) {
	src := strings.TrimSpace(expr)
	if src == "" {
		return json.RawMessage("null"), nil
	}
	hasAwait := strings.Contains(src, "await ")
	candidates := []string{}
	if hasAwait {
		candidates = append(candidates, "(async () => { return (\n"+src+"\n); })()", "(async () => {\n"+src+"\n})()")
	}
	candidates = append(candidates, "(\n"+src+"\n)", src)

	j.mu.Lock()
	if j.closed {
		j.mu.Unlock()
		return nil, ErrClosed
	}
	var val quickjs.Value
	var err error
	for i, c := range candidates {
		val, err = j.vm.EvalValue(c, quickjs.EvalGlobal)
		if err == nil {
			break
		}
		var qe *quickjs.Error
		if errors.As(err, &qe) && qe.Name == "SyntaxError" && i < len(candidates)-1 {
			continue
		}
		break
	}
	if err != nil {
		j.mu.Unlock()
		return nil, fmt.Errorf("%s", jsErrorText(err, ""))
	}
	defer val.Free()

	// Await thenables.
	isPromise, _ := j.vm.Call("__swIsThenable", val)
	settled := false
	var result any
	var rejected error
	if b, _ := isPromise.(bool); b {
		_, terr := val.Then(func(v quickjs.Value) {
			settled = true
			result, _ = j.serializeLocked(v)
		}, func(v quickjs.Value) {
			settled = true
			rejected = fmt.Errorf("%s", j.errorStringLocked(v))
		})
		if terr != nil {
			j.mu.Unlock()
			return nil, terr
		}
		j.mu.Unlock()
		deadline := time.Now().Add(10 * time.Second)
		for !settled {
			if j.pump(50 * time.Millisecond) {
				return json.RawMessage("null"), nil // navigated away
			}
			j.mu.Lock()
			done := settled
			j.mu.Unlock()
			if done {
				break
			}
			if time.Now().After(deadline) {
				return nil, errEvalTimeout
			}
		}
		if rejected != nil {
			return nil, rejected
		}
		b, err := json.Marshal(result)
		return b, err
	}
	result, err = j.serializeLocked(val)
	j.mu.Unlock()
	if err != nil {
		return nil, err
	}
	b, err := json.Marshal(result)
	if err != nil {
		return json.RawMessage(fmt.Sprintf("%q", fmt.Sprint(result))), nil
	}
	return b, nil
}

// serializeLocked converts a JS value into plain Go data through the prelude's
// __swSerialize (DOM nodes, functions, cycles handled). Caller holds j.mu.
func (j *jsVM) serializeLocked(v quickjs.Value) (any, error) {
	r, err := j.vm.Call("__swSerialize", v)
	if err != nil {
		a, aerr := v.Any()
		if aerr != nil {
			return nil, err
		}
		return normalizeJS(a), nil
	}
	return normalizeJS(r), nil
}

func (j *jsVM) errorStringLocked(v quickjs.Value) string {
	if e := quickjs.ErrorFromValue(v); e != nil {
		return e.Name + ": " + e.Message
	}
	r, err := j.vm.Call("String", v)
	if err != nil {
		return "rejected"
	}
	return fmt.Sprint(r)
}

// normalizeJS turns quickjs result types into json-friendly Go values.
func normalizeJS(v any) any {
	switch x := v.(type) {
	case quickjs.Undefined, quickjs.Unsupported:
		return nil
	case *quickjs.Object:
		var out any
		b, err := x.MarshalJSON()
		if err != nil || json.Unmarshal(b, &out) != nil {
			return x.String()
		}
		return out
	case []any:
		for i := range x {
			x[i] = normalizeJS(x[i])
		}
		return x
	case map[string]any:
		for k := range x {
			x[k] = normalizeJS(x[k])
		}
		return x
	}
	return v
}

// ---- module loader ---------------------------------------------------------------

func (j *jsVM) installModuleLoader(docURL string) {
	j.vm.SetModuleLoader(func(_ *quickjs.VM, name string) (string, error) {
		if !strings.HasPrefix(name, "http://") && !strings.HasPrefix(name, "https://") {
			return "", fmt.Errorf("cannot resolve bare module specifier %q", name)
		}
		body, status, err := j.tab.fetchResource(name)
		if err != nil {
			return "", err
		}
		if status >= 400 {
			return "", fmt.Errorf("module %s: HTTP %d", name, status)
		}
		return body, nil
	}, func(_ *quickjs.VM, base, name string) (string, error) {
		if strings.HasPrefix(name, "http://") || strings.HasPrefix(name, "https://") {
			return name, nil
		}
		b := base
		if !strings.HasPrefix(b, "http://") && !strings.HasPrefix(b, "https://") {
			b = j.tab.URL()
			if b == "about:blank" {
				b = docURL
			}
		}
		if strings.HasPrefix(name, "./") || strings.HasPrefix(name, "../") || strings.HasPrefix(name, "/") {
			return resolveURL(b, name)
		}
		return "", fmt.Errorf("bare module specifier %q is not supported (no import map)", name)
	})
}

// ---- host object -----------------------------------------------------------------

func argI(a []any, i int) int64 {
	if i >= len(a) {
		return 0
	}
	switch v := a[i].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return 0
}

func argS(a []any, i int) string {
	if i >= len(a) || a[i] == nil {
		return ""
	}
	if s, ok := a[i].(string); ok {
		return s
	}
	return fmt.Sprint(a[i])
}

func argB(a []any, i int) bool {
	if i >= len(a) {
		return false
	}
	return truthyAny(a[i])
}

func idsToAny(ids []int64) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

func (j *jsVM) withDoc(fn func(d *fnet.Doc) (any, error)) (any, error) {
	t := j.tab
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if d == nil {
		return nil, fmt.Errorf("no document")
	}
	return fn(d)
}

func (j *jsVM) registerHost() error {
	reg := func(name string, fn func(a []any) (any, error)) error {
		return j.vm.RegisterHostFunc("__sw_"+name, fn)
	}
	type h struct {
		name string
		fn   func(a []any) (any, error)
	}
	doc := func(fn func(d *fnet.Doc, a []any) (any, error)) func([]any) (any, error) {
		return func(a []any) (any, error) {
			return j.withDoc(func(d *fnet.Doc) (any, error) { return fn(d, a) })
		}
	}
	hs := []h{
		// tree
		{"docId", doc(func(d *fnet.Doc, a []any) (any, error) { return d.DocID(), nil })},
		{"nodeType", doc(func(d *fnet.Doc, a []any) (any, error) { return d.NodeType(argI(a, 0)), nil })},
		{"tagName", doc(func(d *fnet.Doc, a []any) (any, error) { return d.TagName(argI(a, 0)), nil })},
		{"namespace", doc(func(d *fnet.Doc, a []any) (any, error) { return d.Namespace(argI(a, 0)), nil })},
		{"parent", doc(func(d *fnet.Doc, a []any) (any, error) { return d.ParentID(argI(a, 0)), nil })},
		{"children", doc(func(d *fnet.Doc, a []any) (any, error) { return idsToAny(d.ChildIDs(argI(a, 0))), nil })},
		{"firstChild", doc(func(d *fnet.Doc, a []any) (any, error) { return d.FirstChildID(argI(a, 0)), nil })},
		{"lastChild", doc(func(d *fnet.Doc, a []any) (any, error) { return d.LastChildID(argI(a, 0)), nil })},
		{"nextSibling", doc(func(d *fnet.Doc, a []any) (any, error) { return d.NextSiblingID(argI(a, 0)), nil })},
		{"prevSibling", doc(func(d *fnet.Doc, a []any) (any, error) { return d.PrevSiblingID(argI(a, 0)), nil })},
		{"createElement", doc(func(d *fnet.Doc, a []any) (any, error) { return d.CreateElement(argS(a, 0), argS(a, 1)), nil })},
		{"createText", doc(func(d *fnet.Doc, a []any) (any, error) { return d.CreateText(argS(a, 0)), nil })},
		{"createComment", doc(func(d *fnet.Doc, a []any) (any, error) { return d.CreateComment(argS(a, 0)), nil })},
		{"createFragment", doc(func(d *fnet.Doc, a []any) (any, error) { return d.CreateFragment(), nil })},
		{"appendChild", doc(func(d *fnet.Doc, a []any) (any, error) {
			p, c := argI(a, 0), argI(a, 1)
			if err := d.AppendChild(p, c); err != nil {
				return nil, err
			}
			j.noteInserted(d, c)
			return c, nil
		})},
		{"insertBefore", doc(func(d *fnet.Doc, a []any) (any, error) {
			p, c := argI(a, 0), argI(a, 1)
			if err := d.InsertBefore(p, c, argI(a, 2)); err != nil {
				return nil, err
			}
			j.noteInserted(d, c)
			return c, nil
		})},
		{"removeChild", doc(func(d *fnet.Doc, a []any) (any, error) {
			return argI(a, 1), d.RemoveChild(argI(a, 0), argI(a, 1))
		})},
		{"cloneNode", doc(func(d *fnet.Doc, a []any) (any, error) { return d.CloneNode(argI(a, 0), argB(a, 1)), nil })},
		{"attached", doc(func(d *fnet.Doc, a []any) (any, error) { return d.IsConnected(argI(a, 0)), nil })},
		{"refOf", doc(func(d *fnet.Doc, a []any) (any, error) { return d.RefForBackend(argI(a, 0)), nil })},
		// content
		{"nodeValue", doc(func(d *fnet.Doc, a []any) (any, error) { return d.NodeValue(argI(a, 0)), nil })},
		{"setNodeValue", doc(func(d *fnet.Doc, a []any) (any, error) { d.SetNodeValue(argI(a, 0), argS(a, 1)); return nil, nil })},
		{"textContent", doc(func(d *fnet.Doc, a []any) (any, error) { return d.TextContent(argI(a, 0)), nil })},
		{"setTextContent", doc(func(d *fnet.Doc, a []any) (any, error) { d.SetTextContent(argI(a, 0), argS(a, 1)); return nil, nil })},
		{"innerHTML", doc(func(d *fnet.Doc, a []any) (any, error) {
			s, err := d.InnerHTML(argI(a, 0))
			return s, err
		})},
		{"setInnerHTML", doc(func(d *fnet.Doc, a []any) (any, error) { return nil, d.SetInnerHTMLCtx(argI(a, 0), argS(a, 1)) })},
		{"outerHTML", doc(func(d *fnet.Doc, a []any) (any, error) { return d.SerializeNode(argI(a, 0)), nil })},
		{"insertAdjacentHTML", doc(func(d *fnet.Doc, a []any) (any, error) {
			return nil, d.InsertAdjacentHTML(argI(a, 0), argS(a, 1), argS(a, 2))
		})},
		// attributes
		{"getAttr", doc(func(d *fnet.Doc, a []any) (any, error) {
			v, ok := d.GetAttr(argI(a, 0), argS(a, 1))
			if !ok {
				return nil, nil
			}
			return v, nil
		})},
		{"setAttr", doc(func(d *fnet.Doc, a []any) (any, error) {
			d.SetAttr(argI(a, 0), argS(a, 1), argS(a, 2))
			return nil, nil
		})},
		{"removeAttr", doc(func(d *fnet.Doc, a []any) (any, error) { d.RemoveAttr(argI(a, 0), argS(a, 1)); return nil, nil })},
		{"hasAttr", doc(func(d *fnet.Doc, a []any) (any, error) { _, ok := d.GetAttr(argI(a, 0), argS(a, 1)); return ok, nil })},
		{"attrNames", doc(func(d *fnet.Doc, a []any) (any, error) {
			names := d.AttrNames(argI(a, 0))
			out := make([]any, len(names))
			for i, n := range names {
				out[i] = n
			}
			return out, nil
		})},
		// queries
		{"querySelector", doc(func(d *fnet.Doc, a []any) (any, error) { return d.QueryFrom(argI(a, 0), argS(a, 1)) })},
		{"querySelectorAll", doc(func(d *fnet.Doc, a []any) (any, error) {
			ids, err := d.QueryAllFrom(argI(a, 0), argS(a, 1))
			return idsToAny(ids), err
		})},
		{"matches", doc(func(d *fnet.Doc, a []any) (any, error) { return d.Matches(argI(a, 0), argS(a, 1)) })},
		{"getElementById", doc(func(d *fnet.Doc, a []any) (any, error) { return d.ElementByID(argS(a, 0)), nil })},
		{"elementsByTag", doc(func(d *fnet.Doc, a []any) (any, error) {
			return idsToAny(d.ElementsBy(argI(a, 0), "tag", argS(a, 1))), nil
		})},
		{"elementsByClass", doc(func(d *fnet.Doc, a []any) (any, error) {
			return idsToAny(d.ElementsBy(argI(a, 0), "class", argS(a, 1))), nil
		})},
		{"elementsByName", doc(func(d *fnet.Doc, a []any) (any, error) { return idsToAny(d.ElementsBy(0, "name", argS(a, 0))), nil })},
		// form state
		{"getValue", doc(func(d *fnet.Doc, a []any) (any, error) { return d.GetValue(argI(a, 0)), nil })},
		{"setValue", doc(func(d *fnet.Doc, a []any) (any, error) { return nil, d.SetValue(argI(a, 0), argS(a, 1)) })},
		{"getChecked", doc(func(d *fnet.Doc, a []any) (any, error) { return d.GetChecked(argI(a, 0)), nil })},
		{"setChecked", doc(func(d *fnet.Doc, a []any) (any, error) { d.SetCheckedRaw(argI(a, 0), argB(a, 1)); return nil, nil })},
		{"selectedIndex", doc(func(d *fnet.Doc, a []any) (any, error) { return d.SelectedIndex(argI(a, 0)), nil })},
		{"setSelectedIndex", doc(func(d *fnet.Doc, a []any) (any, error) {
			d.SetSelectedIndex(argI(a, 0), int(argI(a, 1)))
			return nil, nil
		})},
		{"formOf", doc(func(d *fnet.Doc, a []any) (any, error) { return d.FormOf(argI(a, 0)), nil })},
		{"submitForm", func(a []any) (any, error) {
			j.queueFormSubmit(argI(a, 0))
			return nil, nil
		}},
		{"box", doc(func(d *fnet.Doc, a []any) (any, error) {
			b, err := d.BoundingBox(argI(a, 0))
			if err != nil {
				return map[string]any{"x": 0, "y": 0, "width": 0, "height": 0}, nil
			}
			return map[string]any{"x": b[0], "y": b[1], "width": b[2], "height": b[3]}, nil
		})},
		{"elementFromPoint", func(a []any) (any, error) {
			id, err := j.tab.hitTest(int(argI(a, 0)), int(argI(a, 1)))
			if err != nil {
				return 0, nil
			}
			return id, nil
		}},
		// environment
		{"log", func(a []any) (any, error) {
			level := argS(a, 0)
			if level == "" {
				level = "log"
			}
			j.tab.LogConsole(level, argS(a, 1))
			return nil, nil
		}},
		{"url", func(a []any) (any, error) { return j.tab.URL(), nil }},
		{"navigate", func(a []any) (any, error) {
			u, err := resolveURL(j.tab.URL(), argS(a, 0))
			if err != nil {
				return nil, err
			}
			j.pendingNav = u // j.mu is held by the running eval
			return nil, nil
		}},
		{"replaceUrl", func(a []any) (any, error) {
			u, err := resolveURL(j.tab.URL(), argS(a, 0))
			if err != nil {
				return nil, err
			}
			j.tab.setURLOnly(u)
			return nil, nil
		}},
		{"userAgent", func(a []any) (any, error) { return j.tab.UserAgent(), nil }},
		{"viewport", func(a []any) (any, error) {
			w, h, dsf, mobile, _ := j.tab.Viewport()
			return map[string]any{"width": w, "height": h, "dsf": dsf, "mobile": mobile, "colorScheme": j.tab.ColorSchemeName()}, nil
		}},
		{"now", func(a []any) (any, error) { return j.nowMs(), nil }},
		{"http", func(a []any) (any, error) {
			hdr := map[string]string{}
			if m, ok := a[2].(map[string]any); ok {
				for k, v := range m {
					hdr[k] = fmt.Sprint(v)
				}
			}
			body := ""
			if len(a) > 3 && a[3] != nil {
				body = argS(a, 3)
			}
			return j.tab.httpForScript(argS(a, 0), argS(a, 1), hdr, body)
		}},
		{"storageGet", func(a []any) (any, error) {
			kind, key := argS(a, 0), argS(a, 1)
			if kind == "session" {
				if v, ok := j.session[key]; ok {
					return v, nil
				}
				return nil, nil
			}
			if v, ok := j.tab.Context().GetStorageValue(j.tab.origin(), key); ok {
				return v, nil
			}
			return nil, nil
		}},
		{"storageSet", func(a []any) (any, error) {
			kind, key, val := argS(a, 0), argS(a, 1), argS(a, 2)
			if kind == "session" {
				j.session[key] = val
			} else {
				j.tab.Context().SetStorageValue(j.tab.origin(), key, val)
			}
			return nil, nil
		}},
		{"storageRemove", func(a []any) (any, error) {
			kind, key := argS(a, 0), argS(a, 1)
			if kind == "session" {
				delete(j.session, key)
			} else {
				j.tab.Context().RemoveStorageValue(j.tab.origin(), key)
			}
			return nil, nil
		}},
		{"storageClear", func(a []any) (any, error) {
			if argS(a, 0) == "session" {
				j.session = map[string]string{}
			} else {
				j.tab.Context().ClearStorage(j.tab.origin())
			}
			return nil, nil
		}},
		{"storageKeys", func(a []any) (any, error) {
			var keys []string
			if argS(a, 0) == "session" {
				for k := range j.session {
					keys = append(keys, k)
				}
			} else {
				keys = j.tab.Context().StorageKeys(j.tab.origin())
			}
			out := make([]any, len(keys))
			for i, k := range keys {
				out[i] = k
			}
			return out, nil
		}},
		{"cookie", func(a []any) (any, error) { return j.tab.Context().JarRef().ForURL(j.tab.URL()), nil }},
		{"setCookie", func(a []any) (any, error) {
			if c := parseCookie(argS(a, 0), j.tab.eng.now()); c != nil {
				if c.Domain == "" {
					c.Domain = hostOf(j.tab.URL())
				}
				j.tab.Context().JarRef().Set(c)
			}
			return nil, nil
		}},
		{"dialog", func(a []any) (any, error) {
			acc, txt := j.tab.HandleDialog(Dialog{Kind: argS(a, 0), Message: argS(a, 1)})
			if argS(a, 0) == "prompt" && txt == "" && acc {
				txt = argS(a, 2)
			}
			return map[string]any{"accepted": acc, "value": txt}, nil
		}},
		{"title", func(a []any) (any, error) { return j.tab.Title(), nil }},
		{"setTitle", func(a []any) (any, error) {
			j.tab.mu.Lock()
			j.tab.title = argS(a, 0)
			j.tab.mu.Unlock()
			return nil, nil
		}},
		{"mutation", func(a []any) (any, error) { return nil, nil }},
	}
	for _, x := range hs {
		if err := reg(x.name, x.fn); err != nil {
			return err
		}
	}
	// Collect the flat __sw_* functions into one object for the prelude.
	var sb strings.Builder
	sb.WriteString("globalThis.__sw = {")
	for i, x := range hs {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(x.name + ":__sw_" + x.name)
	}
	sb.WriteString("};")
	for _, x := range hs {
		sb.WriteString("delete globalThis.__sw_" + x.name + ";")
	}
	_, err := j.vm.Eval(sb.String(), quickjs.EvalGlobal)
	return err
}

// noteInserted queues newly connected <script> elements for execution, the
// way browsers run scripts inserted by page code (webpack chunk loading,
// analytics tags, lazy widgets).
func (j *jsVM) noteInserted(d *fnet.Doc, id int64) {
	if !d.IsConnected(id) {
		return
	}
	var scripts []int64
	if d.TagName(id) == "script" {
		scripts = append(scripts, id)
	} else {
		scripts = append(scripts, d.ElementsBy(id, "tag", "script")...)
	}
	for _, s := range scripts {
		if !j.executed[s] {
			j.scriptQueue = append(j.scriptQueue, s)
		}
	}
}

func (j *jsVM) queueFormSubmit(formID int64) {
	t := j.tab
	t.mu.RLock()
	d := t.dom
	t.mu.RUnlock()
	if d == nil {
		return
	}
	if d.InfoByBackend(formID) == nil {
		return
	}
	j.pendingNav = "form://" + strconv.FormatInt(formID, 10)
}

// httpForScript performs fetch/XHR on behalf of page code: cookies, UA,
// extra headers, route interception and the network ring all apply.
func (t *Tab) httpForScript(method, rawurl string, headers map[string]string, body string) (map[string]any, error) {
	if method == "" {
		method = "GET"
	}
	abs, err := resolveURL(t.URL(), rawurl)
	if err != nil {
		return nil, err
	}
	if t.IsOfflineMode() {
		return nil, fmt.Errorf("TypeError: Failed to fetch (offline)")
	}
	if code, rbody, matched := t.interceptGet(abs); matched {
		if code == 0 {
			return nil, fmt.Errorf("TypeError: Failed to fetch (aborted by route)")
		}
		return map[string]any{"status": code, "statusText": http.StatusText(code), "headers": map[string]any{}, "body": rbody, "url": abs}, nil
	}
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.eng.ctx, strings.ToUpper(method), abs, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", t.UserAgent())
	req.Header.Set("Accept", "*/*")
	for k, v := range t.ExtraHeaders() {
		req.Header.Set(k, v)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if ck := t.Context().JarRef().ForURL(abs); ck != "" {
		req.Header.Set("Cookie", ck)
	}
	rec := fnet.RequestRecord{Method: req.Method, URL: abs, Headers: flattenHeaders(req.Header)}
	t.netRing.Push(Event{Tab: t.ID, Kind: EvRequest, At: time.Now(), Data: rec})
	start := time.Now()
	res, err := t.eng.client().Do(req)
	if err != nil {
		t.netRing.Push(Event{Tab: t.ID, Kind: EvFailure, At: time.Now(), Data: map[string]string{"url": abs, "error": err.Error()}})
		return nil, fmt.Errorf("TypeError: Failed to fetch (%s)", err.Error())
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	for _, sc := range res.Header.Values("Set-Cookie") {
		if c := parseCookie(sc, t.eng.now()); c != nil {
			if c.Domain == "" {
				c.Domain = res.Request.URL.Hostname()
			}
			t.Context().JarRef().Set(c)
		}
	}
	rec.Status = res.StatusCode
	rec.FinalURL = res.Request.URL.String()
	rec.DurationMs = time.Since(start).Milliseconds()
	rec.ResponseHeaders = flattenHeaders(res.Header)
	t.netRing.Push(Event{Tab: t.ID, Kind: EvResponse, At: time.Now(), Data: rec})
	hdr := map[string]any{}
	for k, v := range res.Header {
		hdr[strings.ToLower(k)] = strings.Join(v, ", ")
	}
	return map[string]any{"status": res.StatusCode, "statusText": http.StatusText(res.StatusCode), "headers": hdr, "body": string(data), "url": rec.FinalURL}, nil
}

// fetchResource GETs a sub-resource (script, module) as text.
func (t *Tab) fetchResource(rawurl string) (string, int, error) {
	r, err := t.httpForScript("GET", rawurl, nil, "")
	if err != nil {
		return "", 0, err
	}
	status, _ := r["status"].(int)
	body, _ := r["body"].(string)
	return body, status, nil
}

func (t *Tab) origin() string {
	u, err := url.Parse(t.URL())
	if err != nil || u.Host == "" {
		return t.URL()
	}
	return u.Scheme + "://" + u.Host
}

// setURLOnly updates the tab URL without a navigation (history.pushState).
func (t *Tab) setURLOnly(raw string) {
	u, err := url.Parse(raw)
	if err != nil {
		return
	}
	t.mu.Lock()
	t.url = u
	if t.hIdx+1 < len(t.history) {
		t.history = t.history[:t.hIdx+1]
	}
	t.history = append(t.history, u)
	t.hIdx = len(t.history) - 1
	t.mu.Unlock()
}
