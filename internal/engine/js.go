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
func runtimeDestroyContext(ctx any)         { destroyCgoContext(ctx) }

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

// currentTab is resolved by the DOM bridge so the fallback evaluator can
// answer common document.* expressions without QuickJS linked in.
var currentTab func() *Tab

// SetEvalTabHook wires the fallback evaluator to a tab's live DOM. The RPC/MCP
// layer calls this around every evaluate so pure-Go builds still return real
// values for the expressions agents use most.
func SetEvalTabHook(f func() *Tab) { currentTab = f }

// fallbackEval implements a minimal read-only interpreter for the handful of
// expressions agents use most: JSON literals, document.title/URL, selector
// reads, and small string helpers. Anything unhandled reports as unsupported
// rather than silently returning wrong data — safety over cleverness.
func fallbackEval(src string) (string, []string) {
	s := strings.TrimSpace(src)
	// trailing semicolons are common from injected code
	s = strings.TrimRight(s, ";")
	s = strings.TrimSpace(s)

	t := ""
	if currentTab != nil {
		if tab := currentTab(); tab != nil {
			t = tab.Title()
		}
	}

	switch {
	case s == "document.title" || s == "window.document.title":
		return jsonOr(t), nil
	case s == "location.href" || s == "document.URL" || s == "window.location.href":
		u := ""
		if currentTab != nil {
			if tab := currentTab(); tab != nil {
				u = tab.URL()
			}
		}
		return jsonOr(u), nil
	case strings.HasPrefix(s, "document.querySelector(") && strings.HasSuffix(s, ")"):
		arg := extractStringArg(s, "document.querySelector(")
		if arg == "" {
			return "null", nil
		}
		if currentTab != nil {
			if tab := currentTab(); tab != nil {
				if ni, err := tab.QuerySelector(arg); err == nil {
					return jsonOr(ni.Text), nil
				}
			}
		}
		return "null", nil
	case strings.HasPrefix(s, "document.querySelectorAll(") && strings.HasSuffix(s, ")"):
		arg := extractStringArg(s, "document.querySelectorAll(")
		out := []string{}
		if arg != "" && currentTab != nil {
			if tab := currentTab(); tab != nil {
				if list, err := tab.QuerySelectorAll(arg); err == nil {
					for _, ni := range list {
						out = append(out, ni.Text)
					}
				}
			}
		}
		b, _ := json.Marshal(out)
		return string(b), nil
	case strings.HasPrefix(s, "JSON.parse("):
		var v any
		inner := strings.TrimSuffix(strings.TrimPrefix(s, "JSON.parse("), ")")
		if err := json.Unmarshal([]byte(inner), &v); err != nil {
			return "", []string{"SyntaxError: " + err.Error()}
		}
		b, _ := json.Marshal(v)
		return string(b), nil
	case s == "navigator.userAgent":
		ua := ""
		if currentTab != nil {
			if tab := currentTab(); tab != nil {
				ua = tab.UserAgent()
			}
		}
		return jsonOr(ua), nil
	default:
		return "null", []string{"Sweef: expression needs full JS; rebuild with -tags quickjs (read-only subset handled natively)"}
	}
}

// extractStringArg pulls the first quoted string argument out of a call like
// document.querySelector("div > p").
func extractStringArg(s, prefix string) string {
	rest := strings.TrimSuffix(strings.TrimPrefix(s, prefix), ")")
	rest = strings.TrimSpace(rest)
	if len(rest) >= 2 {
		q := rest[0]
		if q == '"' || q == '\'' {
			if end := strings.IndexByte(rest[1:], q); end >= 0 {
				return rest[1 : 1+end]
			}
		}
	}
	return ""
}

func jsonOr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
