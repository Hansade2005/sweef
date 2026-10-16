package engine

import (
	"encoding/json"
	"fmt"
)

// DevEvalResult is what the Console's prompt prints for an expression.
type DevEvalResult struct {
	OK   bool   `json:"ok"`
	Type string `json:"type,omitempty"` // typeof of the value, "null", "array", "node", ...
	Text string `json:"text"`
}

// devEvalJS runs the expression as a global script (var and function declarations stick, a trailing expression is
// the value, like the console of other browsers) and describes the outcome as a short string. A promise is awaited
// by Tab.Evaluate, so the settled value is what comes back.
const devEvalJS = `(function(src){
  function q(s){ return JSON.stringify(s); }
  function errText(e) { var m; try { m = String(e); } catch (x) { m = "error"; } var st = e && e.stack ? String(e.stack) : ""; return st && st.indexOf(m) < 0 ? m + "\n" + st.replace(/\s+$/, "") : (st || m); }
  function fmt(v, d, seen) {
    var t = typeof v;
    if (v === null) return "null";
    if (t === "undefined") return "undefined";
    if (t === "string") return d ? q(v.length > 200 ? v.slice(0, 200) + "..." : v) : v;
    if (t === "bigint") return String(v) + "n";
    if (t === "symbol") return v.toString();
    if (t === "function") { var s = Function.prototype.toString.call(v); return s.length > 160 ? s.slice(0, 160) + "..." : s; }
    if (t !== "object") return String(v);
    if (seen.indexOf(v) >= 0) return "[Circular]";
    if (v instanceof Error) return errText(v);
    if (typeof v.nodeType === "number" && v.nodeName) {
      if (v.nodeType === 1) { var id = v.id ? "#" + v.id : "", cl = v.className && typeof v.className === "string" ? "." + v.className.trim().split(/\s+/).join(".") : ""; return "<" + v.nodeName.toLowerCase() + id + cl + ">"; }
      return "#" + v.nodeName.toLowerCase().replace("#", "");
    }
    if (d > 2) return Array.isArray(v) ? "Array(" + v.length + ")" : "{...}";
    seen = seen.concat([v]);
    var out, i, ks;
    if (Array.isArray(v)) {
      out = []; for (i = 0; i < v.length && i < 30; i++) out.push(fmt(v[i], d + 1, seen));
      return "(" + v.length + ") [" + out.join(", ") + (v.length > 30 ? ", ..." : "") + "]";
    }
    if (typeof Map !== "undefined" && v instanceof Map) { out = []; v.forEach(function (x, k) { if (out.length < 20) out.push(fmt(k, d + 1, seen) + " => " + fmt(x, d + 1, seen)); }); return "Map(" + v.size + ") {" + out.join(", ") + "}"; }
    if (typeof Set !== "undefined" && v instanceof Set) { out = []; v.forEach(function (x) { if (out.length < 20) out.push(fmt(x, d + 1, seen)); }); return "Set(" + v.size + ") {" + out.join(", ") + "}"; }
    if (v instanceof Date) return v.toISOString ? v.toISOString() : String(v);
    if (v instanceof RegExp) return String(v);
    ks = Object.keys(v); out = [];
    for (i = 0; i < ks.length && i < 30; i++) { var x; try { x = fmt(v[ks[i]], d + 1, seen); } catch (e) { x = "<error>"; } out.push(ks[i] + ": " + x); }
    var cn = v.constructor && v.constructor.name && v.constructor.name !== "Object" ? v.constructor.name + " " : "";
    return cn + "{" + out.join(", ") + (ks.length > 30 ? ", ..." : "") + "}";
  }
  function kind(v) { return v === null ? "null" : Array.isArray(v) ? "array" : (v && typeof v === "object" && typeof v.nodeType === "number") ? "node" : v instanceof Error ? "error" : typeof v; }
  function ok(v) { var s; try { s = fmt(v, 0, []); } catch (e) { s = String(v); } return { ok: true, type: kind(v), text: s.length > 20000 ? s.slice(0, 20000) + "..." : s }; }
  function bad(e) { return { ok: false, type: "error", text: errText(e) }; }
  var r;
  try { r = (0, eval)(src); } catch (e) { return bad(e); }
  if (r && typeof r.then === "function") return r.then(ok, bad);
  return ok(r);
})(`

// DevEval evaluates a Console expression in the page and describes the result. Errors the expression throws are
// part of the result, not Go errors.
func (t *Tab) DevEval(expr string) (DevEvalResult, error) {
	q, _ := json.Marshal(expr)
	raw, err := t.Evaluate(devEvalJS + string(q) + ")")
	if err != nil {
		return DevEvalResult{OK: false, Type: "error", Text: err.Error()}, nil
	}
	var r DevEvalResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return DevEvalResult{}, fmt.Errorf("unreadable result: %w", err)
	}
	return r, nil
}

// DevStorage lists the page's localStorage and sessionStorage (name, value), for the Application panel.
func (t *Tab) DevStorage() (local, session [][2]string) {
	read := func(which string) [][2]string {
		raw, err := t.Evaluate(`(function(){try{var s=` + which + `,o=[];for(var i=0;i<s.length&&i<500;i++){var k=s.key(i);var v=s.getItem(k);o.push([k,v==null?"":String(v).slice(0,4000)])}return o}catch(e){return []}})()`)
		if err != nil {
			return nil
		}
		var out [][2]string
		_ = json.Unmarshal(raw, &out)
		return out
	}
	return read("localStorage"), read("sessionStorage")
}
