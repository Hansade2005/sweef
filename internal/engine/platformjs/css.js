// CSS Custom Highlight API (CSS.highlights, Highlight, HighlightRegistry), @property registration
// (CSS.registerProperty, CSSPropertyRule) and ContentVisibilityAutoStateChangeEvent.
// Highlights and registered properties are stored and queryable; nothing is painted.
var $ = Symbol('css');

// ---- AbstractRange as the base of Range and StaticRange --------------------------------------------------
function AbstractRange() { throw new TypeError('Illegal constructor'); }
H.tag(AbstractRange, 'AbstractRange');
if (g.Range && Object.getPrototypeOf(g.Range.prototype) === Object.prototype) Object.setPrototypeOf(g.Range.prototype, AbstractRange.prototype);
if (g.StaticRange && Object.getPrototypeOf(g.StaticRange.prototype) === Object.prototype) Object.setPrototypeOf(g.StaticRange.prototype, AbstractRange.prototype);
function isRange(r) { return r instanceof AbstractRange || (g.Range && r instanceof g.Range) || (g.StaticRange && r instanceof g.StaticRange); }

// ---- Highlight -------------------------------------------------------------------------------------------------------
var TYPES = ['highlight', 'spelling-error', 'grammar-error'];
function Highlight() {
  if (!new.target) throw new TypeError("Failed to construct 'Highlight': Please use the 'new' operator, this DOM object constructor cannot be called as a function.");
  var set = new Set();
  Object.defineProperty(this, $, { value: { set: set, priority: 0, type: 'highlight' } });
  for (var i = 0; i < arguments.length; i++) {
    if (!isRange(arguments[i])) throw new TypeError("Failed to construct 'Highlight': parameter " + (i + 1) + " is not of type 'AbstractRange'.");
    set.add(arguments[i]);
  }
}
function hs(o) { var s = o !== null && typeof o === 'object' ? o[$] : undefined; if (!s || !s.set) throw new TypeError('Illegal invocation'); return s; }
H.attrs(Highlight.prototype, {
  size: function () { return hs(this).set.size; },
  priority: { get: function () { return hs(this).priority; }, set: function (v) { hs(this).priority = Math.trunc(Number(v)) || 0; } },
  type: {
    get: function () { return hs(this).type; },
    set: function (v) { v = String(v); if (TYPES.indexOf(v) >= 0) hs(this).type = v; }
  }
});
H.define(Highlight.prototype, 'add', function add(r) {
  var s = hs(this);
  if (!isRange(r)) throw new TypeError("Failed to execute 'add' on 'Highlight': parameter 1 is not of type 'AbstractRange'.");
  s.set.add(r); return this;
});
H.define(Highlight.prototype, 'delete', function (r) { return hs(this).set.delete(r); });
H.define(Highlight.prototype, 'has', function has(r) { return hs(this).set.has(r); });
H.define(Highlight.prototype, 'clear', function clear() { hs(this).set.clear(); });
H.define(Highlight.prototype, 'forEach', function forEach(cb, thisArg) {
  if (typeof cb !== 'function') throw new TypeError("Failed to execute 'forEach' on 'Highlight': parameter 1 is not of type 'Function'.");
  var self = this;
  hs(this).set.forEach(function (v) { cb.call(thisArg, v, v, self); });
});
H.define(Highlight.prototype, 'entries', function entries() { return hs(this).set.entries(); });
H.define(Highlight.prototype, 'keys', function keys() { return hs(this).set.values(); });
H.define(Highlight.prototype, 'values', function values() { return hs(this).set.values(); });
H.define(Highlight.prototype, Symbol.iterator, Highlight.prototype.values);
H.tag(Highlight, 'Highlight');

// ---- HighlightRegistry ---------------------------------------------------------------------------------------------------
function HighlightRegistry() { throw new TypeError('Illegal constructor'); }
function rs(o) { var s = o !== null && typeof o === 'object' ? o[$] : undefined; if (!s || !s.map) throw new TypeError('Illegal invocation'); return s; }
H.attrs(HighlightRegistry.prototype, { size: function () { return rs(this).map.size; } });
H.define(HighlightRegistry.prototype, 'set', function set(name, h) {
  var s = rs(this);
  if (!(h instanceof Highlight)) throw new TypeError("Failed to execute 'set' on 'HighlightRegistry': parameter 2 is not of type 'Highlight'.");
  s.map.set(String(name), h); return this;
});
H.define(HighlightRegistry.prototype, 'get', function get(name) { return rs(this).map.get(String(name)); });
H.define(HighlightRegistry.prototype, 'has', function has(name) { return rs(this).map.has(String(name)); });
H.define(HighlightRegistry.prototype, 'delete', function (name) { return rs(this).map.delete(String(name)); });
H.define(HighlightRegistry.prototype, 'clear', function clear() { rs(this).map.clear(); });
H.define(HighlightRegistry.prototype, 'forEach', function forEach(cb, thisArg) {
  if (typeof cb !== 'function') throw new TypeError("Failed to execute 'forEach' on 'HighlightRegistry': parameter 1 is not of type 'Function'.");
  var self = this;
  rs(this).map.forEach(function (v, k) { cb.call(thisArg, v, k, self); });
});
H.define(HighlightRegistry.prototype, 'entries', function entries() { return rs(this).map.entries(); });
H.define(HighlightRegistry.prototype, 'keys', function keys() { return rs(this).map.keys(); });
H.define(HighlightRegistry.prototype, 'values', function values() { return rs(this).map.values(); });
H.define(HighlightRegistry.prototype, Symbol.iterator, HighlightRegistry.prototype.entries);
H.tag(HighlightRegistry, 'HighlightRegistry');
var registry = Object.create(HighlightRegistry.prototype);
Object.defineProperty(registry, $, { value: { map: new Map() } });

// ---- CSSRule / CSSPropertyRule ----------------------------------------------------------------------------------------------
var CSSRule;
if (!H.missing(g, 'CSSRule')) CSSRule = g.CSSRule;
else {
  CSSRule = function CSSRule() { throw new TypeError('Illegal constructor'); };
  [['STYLE_RULE', 1], ['CHARSET_RULE', 2], ['IMPORT_RULE', 3], ['MEDIA_RULE', 4], ['FONT_FACE_RULE', 5], ['PAGE_RULE', 6], ['KEYFRAMES_RULE', 7], ['KEYFRAME_RULE', 8], ['NAMESPACE_RULE', 10], ['COUNTER_STYLE_RULE', 11], ['SUPPORTS_RULE', 12]].forEach(function (c) {
    Object.defineProperty(CSSRule, c[0], { value: c[1], enumerable: true });
    Object.defineProperty(CSSRule.prototype, c[0], { value: c[1], enumerable: true });
  });
  H.tag(CSSRule, 'CSSRule');
  H.glob('CSSRule', CSSRule);
}
function CSSPropertyRule() { throw new TypeError('Illegal constructor'); }
Object.setPrototypeOf(CSSPropertyRule.prototype, CSSRule.prototype);
Object.setPrototypeOf(CSSPropertyRule, CSSRule);
function ps(o) { var s = o !== null && typeof o === 'object' ? o[$] : undefined; if (!s || !s.prop) throw new TypeError('Illegal invocation'); return s.prop; }
H.attrs(CSSPropertyRule.prototype, {
  name: function () { return ps(this).name; },
  syntax: function () { return ps(this).syntax; },
  inherits: function () { return ps(this).inherits; },
  initialValue: function () { var v = ps(this).initialValue; return v === undefined ? null : v; },
  cssText: function () {
    var p = ps(this);
    return '@property ' + p.name + ' { syntax: "' + p.syntax + '"; inherits: ' + (p.inherits ? 'true' : 'false') + ';' + (p.initialValue !== undefined ? ' initial-value: ' + p.initialValue + ';' : '') + ' }';
  },
  type: function () { return 0; },
  parentRule: function () { return null; },
  parentStyleSheet: function () { return null; }
});
H.tag(CSSPropertyRule, 'CSSPropertyRule');

// ---- CSS.registerProperty ---------------------------------------------------------------------------------------------------------
var DATA_TYPES = 'length percentage length-percentage number integer color image url angle time resolution transform-function transform-list custom-ident string'.split(' ');
function parseSyntax(syntax) {
  syntax = syntax.trim();
  if (syntax === '*') return { universal: true, alts: [] };
  var alts = syntax.split('|').map(function (a) { return a.trim(); });
  var out = [];
  for (var i = 0; i < alts.length; i++) {
    var a = alts[i], m = /^<([a-z-]+)>([+#]?)$/.exec(a);
    if (m) { if (DATA_TYPES.indexOf(m[1]) < 0) return null; out.push({ type: m[1], mult: m[2] }); continue; }
    if (/^-?[A-Za-z_][\w-]*$/.test(a)) { out.push({ ident: a }); continue; }
    return null;
  }
  return out.length ? { universal: false, alts: out } : null;
}
var LENGTH = /^[+-]?(\d+\.?\d*|\.\d+)(e[+-]?\d+)?(px|em|rem|ex|ch|vw|vh|vmin|vmax|cm|mm|in|pt|pc|q|%|lh|svh|dvh|lvh)$|^0$/i;
var NUMBER = /^[+-]?(\d+\.?\d*|\.\d+)(e[+-]?\d+)?$/i;
function valueMatches(term, v) {
  if (term.ident) return v === term.ident;
  switch (term.type) {
    case 'length': return LENGTH.test(v) && !/%$/.test(v);
    case 'percentage': return /^[+-]?(\d+\.?\d*|\.\d+)%$/.test(v);
    case 'length-percentage': return LENGTH.test(v);
    case 'number': return NUMBER.test(v);
    case 'integer': return /^[+-]?\d+$/.test(v);
    case 'angle': return /^[+-]?(\d+\.?\d*|\.\d+)(deg|grad|rad|turn)$/.test(v);
    case 'time': return /^[+-]?(\d+\.?\d*|\.\d+)(s|ms)$/.test(v);
    case 'resolution': return /^[+-]?(\d+\.?\d*|\.\d+)(dpi|dpcm|dppx|x)$/.test(v);
    case 'color': return /^(#[0-9a-f]{3,8}|[a-z]+|(rgb|rgba|hsl|hsla|hwb|lab|lch|oklab|oklch|color)\(.*\))$/i.test(v);
    case 'url': return /^url\(.*\)$/.test(v);
    case 'custom-ident': return /^-?[A-Za-z_][\w-]*$/.test(v);
    case 'string': return /^("[^"]*"|'[^']*')$/.test(v);
    default: return true;
  }
}
function validInitial(parsed, v) {
  v = v.trim();
  if (parsed.universal) return true;
  return parsed.alts.some(function (t) {
    if (t.mult === '+') return v.split(/\s+/).every(function (x) { return valueMatches(t, x); });
    if (t.mult === '#') return v.split(/\s*,\s*/).every(function (x) { return valueMatches(t, x); });
    return valueMatches(t, v);
  });
}
var props = new Map();
function registerProperty(def) {
  if (def === null || typeof def !== 'object') throw new TypeError("Failed to execute 'registerProperty' on 'CSS': parameter 1 is not of type 'PropertyDefinition'.");
  var name = def.name === undefined ? undefined : String(def.name);
  if (name === undefined) throw new TypeError("Failed to execute 'registerProperty' on 'CSS': Failed to read the 'name' property from 'PropertyDefinition': Required member is undefined.");
  if (def.inherits === undefined) throw new TypeError("Failed to execute 'registerProperty' on 'CSS': Failed to read the 'inherits' property from 'PropertyDefinition': Required member is undefined.");
  if (!/^--./.test(name)) throw H.exc("Failed to execute 'registerProperty' on 'CSS': Invalid property name '" + name + "': custom property names must start with '--'.", 'SyntaxError');
  var syntax = def.syntax === undefined ? '*' : String(def.syntax);
  var parsed = parseSyntax(syntax);
  if (!parsed) throw H.exc("Failed to execute 'registerProperty' on 'CSS': The syntax provided ('" + syntax + "') is not a valid CSS syntax string.", 'SyntaxError');
  var initial = def.initialValue === undefined ? undefined : String(def.initialValue);
  if (!parsed.universal && initial === undefined) throw new TypeError("Failed to execute 'registerProperty' on 'CSS': An initial value must be provided if the syntax is not '*'.");
  if (initial !== undefined && !validInitial(parsed, initial)) throw H.exc("Failed to execute 'registerProperty' on 'CSS': The initial value provided ('" + initial + "') does not parse for the given syntax ('" + syntax + "').", 'SyntaxError');
  if (props.has(name)) throw H.exc("Failed to execute 'registerProperty' on 'CSS': The name provided ('" + name + "') has already been registered.", 'InvalidModificationError');
  var rule = Object.create(CSSPropertyRule.prototype);
  Object.defineProperty(rule, $, { value: { prop: { name: name, syntax: syntax, inherits: !!def.inherits, initialValue: initial } } });
  props.set(name, rule);
}
// CSS is a plain namespace object in the base prelude.
H.define(g.CSS, 'registerProperty', registerProperty);
Object.defineProperty(g.CSS, 'highlights', { get: function () { return registry; }, enumerable: true, configurable: true });
H.define(g.CSS, 'registeredProperty', function (name) { return props.get(String(name)) || null; });

H.eventClass('ContentVisibilityAutoStateChangeEvent', { skipped: false });
['AbstractRange', 'Highlight', 'HighlightRegistry', 'CSSPropertyRule'].forEach(function (n) { H.glob(n, { AbstractRange: AbstractRange, Highlight: Highlight, HighlightRegistry: HighlightRegistry, CSSPropertyRule: CSSPropertyRule }[n]); });
