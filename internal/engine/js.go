package engine

// JS runtime abstraction.
//
// Sweef embeds QuickJS (cgo build tag `quickjs`) for real ES2020 execution —
// ~1.5 MiB idle heap vs V8's ~8+ MiB, and it compiles to static binaries that
// run on 1-core ARM boxes. When built without cgo (pure-Go fallback used in CI
// and ultra-tiny targets), we ship a small expression evaluator sufficient for
// document.title / querySelector reads so tooling still functions.
//
// The API is deliberately tiny: one context per tab, synchronous eval, JSON
// in/out. That keeps the bridge surface auditable (~200 LoC) which matters
// when an AI agent is going to be running arbitrary page scripts.

import (
	"encoding/json"
	"strings"
)

type jsRuntime struct {
	disabled bool
}

var globalRuntime = &jsRuntime{}

func runtimeInit(opts *Options) {
	globalRuntime.disabled = opts.DisableJS
	initCgoRuntime(opts) // no-op in pure-Go builds
}

func shutdownRuntime() { shutdownCgoRuntime() }

func runtimeCreateContext(tabID string) any { return createCgoContext(tabID) }
func runtimeDestroyContext(ctx any)          { destroyCgoContext(ctx) }

// runtimeEval returns (jsonResult, consoleErrors). In the cgo build this calls
// into QuickJS with DOM-bridge globals installed; here we provide the fallback.
func runtimeEval(ctx any, src, name string) (string, []string) {
	if globalRuntime.disabled || strings.TrimSpace(src) == "" {
		return "", nil
	}
	if cgoAvailable() {
		return cgoEval(ctx, src, name)
	}
	return fallbackEval(src)
}

// fallbackEval implements a minimal read-only interpreter for the handful of
// expressions agents use most: JSON literals, document.title, and simple
// arithmetic-free string equality. Anything else reports as unsupported rather
// than silently returning wrong data — safety over cleverness.
func fallbackEval(src string) (string, []string) {
	s := strings.TrimSpace(src)
	switch {
	case s == "document.title":
		return jsonOr(""), nil
	case strings.HasPrefix(s, "JSON.parse("):
		var v any
		inner := strings.TrimSuffix(strings.TrimPrefix(s, "JSON.parse("), ")")
		if err := json.Unmarshal([]byte(inner), &v); err != nil {
			return "", []string{"SyntaxError: " + err.Error()}
		}
		b, _ := json.Marshal(v)
		return string(b), nil
	default:
		return "null", []string{"Sweef: quickjs not compiled in; rebuild with -tags quickjs for full JS"}
	}
}

func jsonOr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
