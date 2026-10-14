// The smaller Intl services on CLDR data: PluralRules, RelativeTimeFormat, ListFormat, DisplayNames, DurationFormat, Collator,
// Segmenter, getCanonicalLocales and supportedValuesOf. NumberFormat and DateTimeFormat live in intlnum.js / intldate.js.
H.load('intlnum');
var I = H.intl, Intl = g.Intl, Locale = Intl.Locale;

function requireNew(self, C, name) {
  if (!(self instanceof C)) throw new TypeError("Constructor Intl." + name + " requires 'new'");
}
function bad(name, prop, v) { return new RangeError('Value ' + v + ' out of range for Intl.' + name + ' options property ' + prop); }
function resolveBase(locales, options, name, keys, vals) {
  var requested = I.canonList(locales);
  I.getOption(options, 'localeMatcher', 'string', ['lookup', 'best fit'], 'best fit');
  return I.resolveLocale(requested, keys || [], vals);
}
function slotOf(o, SLOT, cls, m) {
  var s = o !== null && typeof o === 'object' ? o[SLOT] : undefined;
  if (!s) throw new TypeError('Method Intl.' + cls + '.prototype.' + m + ' called on incompatible receiver ' + (o === null ? 'null' : typeof o === 'object' ? '#<Object>' : String(o)));
  return s;
}
function defineClass(C, name, length) {
  Object.defineProperty(C, 'name', { value: name, configurable: true });
  Object.defineProperty(C, 'length', { value: length, configurable: true });
  Object.defineProperty(C.prototype, Symbol.toStringTag, { value: 'Intl.' + name, configurable: true });
  H.define(C, 'supportedLocalesOf', function supportedLocalesOf(locales, options) { return I.supportedLocalesOf(locales, options); });
  H.define(Intl, name, C);
}
function patternParts(pattern, a, b) { // "{0} ago" -> pieces: literal strings and 0 / 1 placeholders
  var out = [];
  pattern.split(/(\{0\}|\{1\})/).forEach(function (p) {
    if (p === '{0}') out.push(0); else if (p === '{1}') out.push(1); else if (p) out.push(p);
  });
  return out;
}
function mergeLiterals(parts) {
  var out = [];
  parts.forEach(function (p) {
    var last = out[out.length - 1];
    if (p.type === 'literal' && last && last.type === 'literal' && last.unit === p.unit && last.source === p.source) out[out.length - 1] = Object.assign({}, last, { value: last.value + p.value });
    else out.push(p);
  });
  return out;
}

// ---- getCanonicalLocales, supportedValuesOf ---------------------------------------------------------------------------------------------
H.define(Intl, 'getCanonicalLocales', function getCanonicalLocales(locales) { return I.canonList(locales); });
H.define(Intl, 'supportedValuesOf', function supportedValuesOf(key) {
  key = String(key);
  var v = I.supportedValues(key);
  if (!v) throw new RangeError('Invalid key : ' + key);
  return v.slice();
});

// ---- PluralRules ------------------------------------------------------------------------------------------------------------------------
var PR = Symbol('PluralRules');
var NF_KEYS = ['minimumIntegerDigits', 'minimumFractionDigits', 'maximumFractionDigits', 'minimumSignificantDigits', 'maximumSignificantDigits', 'roundingPriority', 'roundingIncrement', 'roundingMode', 'trailingZeroDisplay'];
function PluralRules(locales, options) {
  requireNew(this, PluralRules, 'PluralRules');
  var requested = I.canonList(locales);
  options = I.coerceOptions(options);
  I.getOption(options, 'localeMatcher', 'string', ['lookup', 'best fit'], 'best fit');
  var type = I.getOption(options, 'type', 'string', ['cardinal', 'ordinal'], 'cardinal');
  var r = I.resolveLocale(requested, [], {});
  var nfo = { useGrouping: false, numberingSystem: 'latn' };
  NF_KEYS.forEach(function (k) { if (options[k] !== undefined) nfo[k] = options[k]; });
  var nf = new Intl.NumberFormat(r.base, nfo);
  this[PR] = { locale: I.pluralLocale(type, r.base), base: r.base, type: type, nf: nf, ro: nf.resolvedOptions() };
}
function pluralOf(s, x) {
  if (x !== x || x === Infinity || x === -Infinity) return 'other';
  var ip = '', fp = '';
  s.nf.formatToParts(x).forEach(function (p) { if (p.type === 'integer') ip += p.value; else if (p.type === 'fraction') fp += p.value; });
  return I.plural(s.type, s.base, I.operands(ip || '0', fp));
}
H.define(PluralRules.prototype, 'select', function select(n) { return pluralOf(slotOf(this, PR, 'PluralRules', 'select'), Number(n)); });
H.define(PluralRules.prototype, 'selectRange', function selectRange(a, b) {
  var s = slotOf(this, PR, 'PluralRules', 'selectRange');
  if (a === undefined || b === undefined) throw new TypeError('start or end is undefined');
  a = Number(a); b = Number(b);
  if (a !== a || b !== b) throw new RangeError('Invalid start or end');
  return I.pluralRange(s.base, pluralOf(s, a), pluralOf(s, b));
});
H.define(PluralRules.prototype, 'resolvedOptions', function resolvedOptions() {
  var s = slotOf(this, PR, 'PluralRules', 'resolvedOptions'), ro = s.ro;
  var o = { locale: s.locale, type: s.type, minimumIntegerDigits: ro.minimumIntegerDigits };
  ['minimumFractionDigits', 'maximumFractionDigits', 'minimumSignificantDigits', 'maximumSignificantDigits'].forEach(function (k) { if (ro[k] !== undefined) o[k] = ro[k]; });
  var cats = I.pluralCategories(s.type, s.base).filter(function (c) { return c !== 'other'; }).sort();
  cats.push('other');
  o.pluralCategories = cats;
  ['roundingIncrement', 'roundingMode', 'roundingPriority', 'trailingZeroDisplay'].forEach(function (k) { if (ro[k] !== undefined) o[k] = ro[k]; });
  return o;
});
defineClass(PluralRules, 'PluralRules', 0);

// ---- RelativeTimeFormat -----------------------------------------------------------------------------------------------------------------
var RT = Symbol('RelativeTimeFormat');
var RT_UNITS = ['second', 'minute', 'hour', 'day', 'week', 'month', 'quarter', 'year'];
function RelativeTimeFormat(locales, options) {
  requireNew(this, RelativeTimeFormat, 'RelativeTimeFormat');
  var requested = I.canonList(locales);
  options = I.coerceOptions(options);
  I.getOption(options, 'localeMatcher', 'string', ['lookup', 'best fit'], 'best fit');
  var nu = options.numberingSystem;
  if (nu !== undefined) { nu = String(nu); if (!/^[a-z0-9]{3,8}(-[a-z0-9]{3,8})*$/i.test(nu)) throw new RangeError('Invalid numberingSystem : ' + nu); nu = nu.toLowerCase(); }
  var r = I.resolveLocale(requested, ['nu'], { nu: nu });
  var style = I.getOption(options, 'style', 'string', ['long', 'short', 'narrow'], 'long');
  var numeric = I.getOption(options, 'numeric', 'string', ['always', 'auto'], 'always');
  var data = I.localeData(r.base), nus = r.ext.nu && I.digitsOf(r.ext.nu) ? r.ext.nu : data.n.nu;
  this[RT] = { locale: r.locale, base: r.base, style: style, numeric: numeric, nu: nus, data: data, nf: new Intl.NumberFormat(r.base, { numberingSystem: nus }), pr: new PluralRules(r.base) };
}
function rtRecord(s, unit) {
  var rec = s.data.r && s.data.r[unit];
  if (!rec) return null;
  var chain = s.style === 'narrow' ? ['n', 's', 'l'] : s.style === 'short' ? ['s', 'l'] : ['l'];
  return chain.map(function (k) { return rec[k]; }).filter(Boolean);
}
function rtParts(s, value, unit) {
  value = Number(value);
  if (value !== value || value === Infinity || value === -Infinity) throw new RangeError('Value need to be finite number for Intl.RelativeTimeFormat.prototype.format()');
  unit = String(unit);
  var u = unit.replace(/s$/, '');
  if (RT_UNITS.indexOf(u) < 0) throw new RangeError("Invalid unit argument for format() '" + unit + "'");
  var recs = rtRecord(s, u) || [];
  if (s.numeric === 'auto') {
    var key = Object.is(value, -0) || value === 0 ? 'r0' : Math.floor(value) === value ? 'r' + value : null;
    if (key) for (var i = 0; i < recs.length; i++) if (recs[i][key] !== undefined) return [{ type: 'literal', value: recs[i][key] }];
  }
  var past = value < 0 || Object.is(value, -0), abs = Math.abs(value), rec = recs[0];
  if (!rec) return [{ type: 'literal', value: String(value) }];
  var cat = pluralOf(s.pr[PR], abs), table = past ? rec.p : rec.f;
  var pattern = table[cat] || table.other, out = [];
  patternParts(pattern).forEach(function (p) {
    if (p === 0) s.nf.formatToParts(abs).forEach(function (x) { out.push({ type: x.type, value: x.value, unit: u }); });
    else out.push({ type: 'literal', value: p });
  });
  return mergeLiterals(out);
}
H.define(RelativeTimeFormat.prototype, 'format', function format(value, unit) {
  var s = slotOf(this, RT, 'RelativeTimeFormat', 'format'), out = '';
  rtParts(s, value, unit).forEach(function (p) { out += p.value; });
  return out;
});
H.define(RelativeTimeFormat.prototype, 'formatToParts', function formatToParts(value, unit) { return rtParts(slotOf(this, RT, 'RelativeTimeFormat', 'formatToParts'), value, unit); });
H.define(RelativeTimeFormat.prototype, 'resolvedOptions', function resolvedOptions() {
  var s = slotOf(this, RT, 'RelativeTimeFormat', 'resolvedOptions');
  return { locale: s.locale, style: s.style, numeric: s.numeric, numberingSystem: s.nu };
});
defineClass(RelativeTimeFormat, 'RelativeTimeFormat', 0);

// ---- ListFormat -------------------------------------------------------------------------------------------------------------------------
var LF = Symbol('ListFormat');
function ListFormat(locales, options) {
  requireNew(this, ListFormat, 'ListFormat');
  var requested = I.canonList(locales);
  options = I.coerceOptions(options);
  I.getOption(options, 'localeMatcher', 'string', ['lookup', 'best fit'], 'best fit');
  var r = I.resolveLocale(requested, [], {});
  var type = I.getOption(options, 'type', 'string', ['conjunction', 'disjunction', 'unit'], 'conjunction');
  var style = I.getOption(options, 'style', 'string', ['long', 'short', 'narrow'], 'long');
  var data = I.localeData(r.base), k = (type === 'conjunction' ? 'standard' : type === 'disjunction' ? 'or' : 'unit') + ':' + style[0];
  this[LF] = { lang: new Locale(r.base).language, locale: r.locale, type: type, style: style, pat: (data.l && data.l[k]) || ['{0}, {1}', '{0}, {1}', '{0}, {1}', '{0}, {1}'] };
}
// words ICU swaps in front of some following words: Hebrew "ו" takes a hyphen before non-Hebrew text, Spanish "y"/"o"
// become "e"/"u" before a word that starts with i (hi) / o (ho, 8)
function adjustWord(lang, type, lit, next) {
  if (lang === 'he' && /ו$/.test(lit) && next && !/^[\u0590-\u05ff]/.test(next)) return lit + '-';
  if (lang === 'es') {
    if (/(^|\s)y(\s|$)/.test(lit) && /^(hi(?!e)|i(?!e))/i.test(next)) return lit.replace(/y(\s*)$/, 'e$1').replace(/^(\s*)y/, '$1e');
    if (/(^|\s)o(\s|$)/.test(lit) && /^(ho|o|8)/i.test(next)) return lit.replace(/o(\s*)$/, 'u$1').replace(/^(\s*)o/, '$1u');
  }
  return lit;
}
function listItems(list) {
  var items = [];
  if (list === undefined) return items;
  if (list === null || typeof list[Symbol.iterator] !== 'function') throw new TypeError(String(list) + ' is not iterable');
  for (var v of list) {
    if (typeof v !== 'string') throw new TypeError('Iterable yielded ' + (typeof v === 'symbol' ? v.toString() : String(v)) + ' which is not a string');
    items.push(v);
  }
  return items;
}
function listParts(s, list) {
  var items = listItems(list), n = items.length;
  if (n === 0) return [];
  var el = function (v) { return [{ type: 'element', value: v }]; };
  if (n === 1) return el(items[0]);
  var lang = s.lang;
  var apply = function (pattern, left, right) {
    var out = [];
    var nextText = right[0] ? right[0].value : '';
    patternParts(pattern).forEach(function (p) {
      if (typeof p === 'string') p = adjustWord(lang, s.type, p, nextText);
      if (p === 0) out = out.concat(left); else if (p === 1) out = out.concat(right); else out.push({ type: 'literal', value: p });
    });
    return out;
  };
  var cur;
  if (n === 2) cur = apply(s.pat[3], el(items[0]), el(items[1]));
  else {
    cur = apply(s.pat[0], el(items[0]), el(items[1]));
    for (var i = 2; i < n - 1; i++) cur = apply(s.pat[1], cur, el(items[i]));
    cur = apply(s.pat[2], cur, el(items[n - 1]));
  }
  return mergeLiterals(cur);
}
H.define(ListFormat.prototype, 'format', function format(list) {
  var s = slotOf(this, LF, 'ListFormat', 'format'), out = '';
  listParts(s, list).forEach(function (p) { out += p.value; });
  return out;
});
H.define(ListFormat.prototype, 'formatToParts', function formatToParts(list) { return listParts(slotOf(this, LF, 'ListFormat', 'formatToParts'), list); });
H.define(ListFormat.prototype, 'resolvedOptions', function resolvedOptions() {
  var s = slotOf(this, LF, 'ListFormat', 'resolvedOptions');
  return { locale: s.locale, type: s.type, style: s.style };
});
defineClass(ListFormat, 'ListFormat', 0);

// ---- DisplayNames -----------------------------------------------------------------------------------------------------------------------
var DN = Symbol('DisplayNames');
var DN_FIELDS = ['era', 'year', 'quarter', 'month', 'weekOfYear', 'weekday', 'day', 'dayPeriod', 'hour', 'minute', 'second', 'timeZoneName'];
function DisplayNames(locales, options) {
  requireNew(this, DisplayNames, 'DisplayNames');
  var requested = I.canonList(locales);
  if (options === undefined) throw new TypeError("Required option 'type' is missing"); // spec: GetOptionsObject
  options = I.coerceOptions(options);
  I.getOption(options, 'localeMatcher', 'string', ['lookup', 'best fit'], 'best fit');
  var r = I.resolveLocale(requested, [], {});
  var style = I.getOption(options, 'style', 'string', ['narrow', 'short', 'long'], 'long');
  var type = I.getOption(options, 'type', 'string', ['language', 'region', 'script', 'currency', 'calendar', 'dateTimeField'], undefined);
  if (type === undefined) throw new TypeError("Required option 'type' is missing");
  var fallback = I.getOption(options, 'fallback', 'string', ['code', 'none'], 'code');
  var ld = I.getOption(options, 'languageDisplay', 'string', ['dialect', 'standard'], 'dialect');
  this[DN] = { locale: r.locale, base: r.base, style: style, type: type, fallback: fallback, languageDisplay: ld, data: I.localeData(r.base) };
}
function title(s) { return s.charAt(0).toUpperCase() + s.slice(1).toLowerCase(); }
function dnOf(s, code) {
  var d = s.data.dn || {}, short = s.style !== 'long', name;
  code = String(code);
  switch (s.type) {
    case 'region': {
      if (!/^([a-z]{2}|[0-9]{3})$/i.test(code)) throw new RangeError('invalid_argument');
      code = code.toUpperCase();
      name = (short && d.rgs && d.rgs[code]) || (d.rg && d.rg[code]);
      return name || (s.fallback === 'code' ? code : undefined);
    }
    case 'script': {
      if (!/^[a-z]{4}$/i.test(code)) throw new RangeError('invalid_argument');
      code = title(code);
      name = d.sc && d.sc[code];
      return name || (s.fallback === 'code' ? code : undefined);
    }
    case 'currency': {
      if (!/^[a-z]{3}$/i.test(code)) throw new RangeError('invalid_argument');
      code = code.toUpperCase();
      var rec = s.data.n['cur$'] && s.data.n['cur$'][code];
      if (rec) name = rec[2]; // ICU gives the long name in every style
      return name || (s.fallback === 'code' ? code : undefined);
    }
    case 'calendar': {
      if (!/^[a-z0-9]{3,8}(-[a-z0-9]{3,8})*$/i.test(code)) throw new RangeError('invalid_argument');
      code = code.toLowerCase();
      name = d.ca && (d.ca[code] || (code === 'gregory' ? d.ca.gregorian : undefined));
      return name || (s.fallback === 'code' ? code : undefined);
    }
    case 'dateTimeField': {
      if (DN_FIELDS.indexOf(code) < 0) throw new RangeError('invalid_argument');
      var t = s.data.r && s.data.r.dn;
      name = t && (t[code + ':' + (s.style === 'long' ? 'l' : s.style === 'short' ? 's' : 'n')] || t[code + ':s'] || t[code + ':l']);
      return name || (s.fallback === 'code' ? code : undefined);
    }
  }
  // language
  if (!/^[a-z]{2,3}$|^[a-z]{5,8}$|^[a-z]{2,3}(-[a-z]{4})?(-([a-z]{2}|[0-9]{3}))?(-([a-z0-9]{5,8}|[0-9][a-z0-9]{3}))*$/i.test(code) || /_/.test(code)) throw new RangeError('invalid_argument');
  var canon;
  try { canon = I.canon(code); } catch (e) { throw new RangeError('invalid_argument'); }
  var l = new Locale(canon), lg = d.lg || {}, lgs = d.lgs || {};
  var lookup = function (k) { return (short && lgs[k]) || lg[k]; };
  if (s.languageDisplay === 'dialect') { name = lookup(canon); if (name) return name; }
  var langName, extras = [];
  var tryTag = s.languageDisplay === 'dialect' ? function (k) { return lookup(k); } : function (k) { return lg[k]; };
  var base = l.language, script = l.script, region = l.region;
  if (!langName) langName = lg[base];
  if (!langName) return s.fallback === 'code' ? canon : undefined;
  if (script) extras.push((d.sc && d.sc[script]) || script);
  if (region) extras.push(((short && d.rgs && d.rgs[region]) || (d.rg && d.rg[region])) || region);
  var pt = d.pt || ['{0} ({1})', '{0}, {1}', '{0}: {1}'];
  if (!extras.length) return langName;
  var tail = extras.length === 2 ? pt[1].replace('{0}', extras[0]).replace('{1}', extras[1]) : extras[0];
  return pt[0].replace('{0}', langName).replace('{1}', tail);
}
H.define(DisplayNames.prototype, 'of', function of(code) {
  var s = slotOf(this, DN, 'DisplayNames', 'of');
  return dnOf(s, code);
});
H.define(DisplayNames.prototype, 'resolvedOptions', function resolvedOptions() {
  var s = slotOf(this, DN, 'DisplayNames', 'resolvedOptions'), o = { locale: s.locale, style: s.style, type: s.type, fallback: s.fallback };
  if (s.type === 'language') o.languageDisplay = s.languageDisplay;
  return o;
});
defineClass(DisplayNames, 'DisplayNames', 2);

// ---- DurationFormat ---------------------------------------------------------------------------------------------------------------------
var DF = Symbol('DurationFormat');
var DUR = ['years', 'months', 'weeks', 'days', 'hours', 'minutes', 'seconds', 'milliseconds', 'microseconds', 'nanoseconds'];
var DUR_UNIT = { years: 'year', months: 'month', weeks: 'week', days: 'day', hours: 'hour', minutes: 'minute', seconds: 'second', milliseconds: 'millisecond', microseconds: 'microsecond', nanoseconds: 'nanosecond' };
function DurationFormat(locales, options) {
  requireNew(this, DurationFormat, 'DurationFormat');
  var requested = I.canonList(locales);
  options = I.coerceOptions(options);
  I.getOption(options, 'localeMatcher', 'string', ['lookup', 'best fit'], 'best fit');
  var nu = options.numberingSystem;
  if (nu !== undefined) { nu = String(nu); if (!/^[a-z0-9]{3,8}(-[a-z0-9]{3,8})*$/i.test(nu)) throw new RangeError('Invalid numberingSystem : ' + nu); nu = nu.toLowerCase(); }
  var r = I.resolveLocale(requested, ['nu'], { nu: nu });
  var data = I.localeData(r.base), nus = r.ext.nu && I.digitsOf(r.ext.nu) ? r.ext.nu : data.n.nu;
  var style = I.getOption(options, 'style', 'string', ['long', 'short', 'narrow', 'digital'], 'short');
  var s = { locale: r.locale, base: r.base, nu: nus, style: style, units: {} }, prev = '';
  DUR.forEach(function (u, i) {
    var text = i < 4, time = i >= 4 && i < 7;
    var allowed = text ? ['long', 'short', 'narrow'] : time ? ['long', 'short', 'narrow', 'numeric', '2-digit'] : ['long', 'short', 'narrow', 'numeric'];
    var st = options[u];
    if (st !== undefined) { st = String(st); if (allowed.indexOf(st) < 0) throw bad('DurationFormat', u, st); }
    var dd = 'auto';
    if (st === undefined) {
      if (style === 'digital') st = text ? 'short' : 'numeric';
      else if (prev === 'numeric' || prev === '2-digit' || prev === 'fractional') st = text ? style : u === 'minutes' || u === 'seconds' ? 'numeric' : !time ? 'numeric' : style;
      else st = style;
      if (style === 'digital' && (u === 'minutes' || u === 'seconds')) dd = 'always';
    }
    if (!text && (prev === 'numeric' || prev === '2-digit') && (u === 'minutes' || u === 'seconds')) { if (st === 'numeric') st = '2-digit'; }
    if (!time && !text && (prev === 'numeric' || prev === '2-digit' || prev === 'fractional')) st = 'fractional';
    var disp = I.getOption(options, u + 'Display', 'string', ['auto', 'always'], dd);
    s.units[u] = { style: st, display: disp };
    prev = st;
  });
  var fd = options.fractionalDigits;
  if (fd !== undefined) { fd = Number(fd); if (fd !== fd || fd < 0 || fd > 9) throw new RangeError('fractionalDigits value is out of range.'); fd = Math.floor(fd); }
  s.fractionalDigits = fd;
  this[DF] = s;
}
function durationRecord(d) {
  if (typeof d === 'string') {
    var m = /^([+-])?P(?:(\d+)Y)?(?:(\d+)M)?(?:(\d+)W)?(?:(\d+)D)?(?:T(?:(\d+)H)?(?:(\d+)M)?(?:(\d+)(?:[.,](\d{1,9}))?S)?)?$/i.exec(d);
    if (!m || /^[+-]?P$/i.test(d) || /T$/i.test(d)) throw new RangeError('Invalid duration string');
    var sign = m[1] === '-' ? -1 : 1, rec = {};
    ['years', 'months', 'weeks', 'days', 'hours', 'minutes', 'seconds'].forEach(function (u, i) { rec[u] = sign * (m[i + 2] ? Number(m[i + 2]) : 0); });
    var fr = (m[9] || '') + '000000000'; rec.milliseconds = sign * Number(fr.slice(0, 3)); rec.microseconds = sign * Number(fr.slice(3, 6)); rec.nanoseconds = sign * Number(fr.slice(6, 9));
    return rec;
  }
  if (d === null || typeof d !== 'object') throw new TypeError('Invalid duration');
  var out = {}, any = false, sgn = 0;
  DUR.forEach(function (u) {
    var v = d[u];
    if (v === undefined) { out[u] = 0; return; }
    v = Number(v);
    if (v !== v || v === Infinity || v === -Infinity || Math.floor(v) !== v) throw new RangeError('Invalid duration: ' + u + ' must be an integer');
    if (v !== 0) { var sg = v < 0 ? -1 : 1; if (sgn && sg !== sgn) throw new RangeError('Invalid duration: mixed signs'); sgn = sg; }
    out[u] = v; any = true;
  });
  if (!any) throw new TypeError('Invalid duration: no duration fields');
  return out;
}
function durParts(s, d) {
  var rec = durationRecord(d), elements = [], U = s.units, neg = false;
  DUR.forEach(function (u) { if (rec[u] < 0) neg = true; });
  var lstyle = s.style === 'digital' ? 'short' : s.style;
  var numericBlock = [], i, u;
  var nfo = function (o) { return new Intl.NumberFormat(s.base, Object.assign({ numberingSystem: s.nu }, o)); };
  for (i = 0; i < DUR.length; i++) {
    u = DUR[i];
    var cfg = U[u], v = rec[u];
    if (cfg.style === 'fractional') continue;
    var isTime = u === 'hours' || u === 'minutes' || u === 'seconds';
    var numeric = cfg.style === 'numeric' || cfg.style === '2-digit';
    // seconds absorb the sub-second units that are fractional
    var secs = null;
    if (u === 'seconds' && numeric) {
      var frac = '', nsTotal = Math.abs(rec.milliseconds) * 1e6 + Math.abs(rec.microseconds) * 1e3 + Math.abs(rec.nanoseconds);
      var mf = U.milliseconds.style === 'fractional' ? (U.microseconds.style === 'fractional' ? (U.nanoseconds.style === 'fractional' ? 9 : 6) : 3) : 0;
      var digits = s.fractionalDigits === undefined ? mf : s.fractionalDigits;
      secs = { whole: Math.abs(v) + (mf ? Math.floor(nsTotal / 1e9) : 0), nano: mf ? nsTotal % 1e9 : 0, digits: mf ? digits : 0 };
      if (mf && s.fractionalDigits === undefined) { // default: as many digits as the units provide, trailing zeros trimmed
        var ns = String(secs.nano); while (ns.length < 9) ns = '0' + ns;
        ns = ns.replace(/0+$/, ''); secs.digits = ns.length; secs.min = 0;
      }
    }
    var show = cfg.display === 'always' || v !== 0 || (secs && (secs.nano !== 0)) || (numeric && numericBlock.length && isTime);
    if (!show) continue;
    if (numeric && isTime) {
      var o = { useGrouping: false, minimumIntegerDigits: cfg.style === '2-digit' ? 2 : 1 };
      var value = Math.abs(v);
      if (secs) { o.minimumFractionDigits = secs.min === undefined ? secs.digits : secs.min; o.maximumFractionDigits = secs.digits; o.roundingMode = 'trunc'; value = Number(secs.whole + '.' + (String(secs.nano) + '').padStart(9, '0')); if (!secs.nano) value = secs.whole; }
      numericBlock.push({ unit: DUR_UNIT[u], parts: nfo(o).formatToParts(value) });
      continue;
    }
    if (numeric) { // a lone numeric sub-second unit
      elements.push(nfo({ useGrouping: false }).formatToParts(Math.abs(v)).map(function (p) { return { type: p.type, value: p.value, unit: DUR_UNIT[u] }; }));
      continue;
    }
    var nf = nfo({ style: 'unit', unit: DUR_UNIT[u], unitDisplay: cfg.style });
    elements.push(nf.formatToParts(neg && !elements.length && !numericBlock.length ? -Math.abs(v) : Math.abs(v)).map(function (p) { return { type: p.type, value: p.value, unit: DUR_UNIT[u] }; }));
  }
  if (numericBlock.length) {
    var sepSym = ':', merged = [];
    numericBlock.forEach(function (b, k) {
      if (k) merged.push({ type: 'literal', value: sepSym });
      b.parts.forEach(function (p) { merged.push({ type: p.type, value: p.value, unit: b.unit }); });
    });
    if (neg && !elements.length) merged.unshift({ type: 'minusSign', value: '-', unit: numericBlock[0].unit });
    // the block sits where its first unit is: after the text units that precede it
    elements.push(merged);
  }
  if (!elements.length) return [];
  var lf = new ListFormat(s.base, { type: 'unit', style: lstyle });
  var lpat = lf[LF].pat, out;
  var items = elements.map(function (e) { return e; });
  if (items.length === 1) return mergeLiterals(items[0]);
  var apply = function (pattern, left, right) {
    var res = [];
    patternParts(pattern).forEach(function (p) {
      if (p === 0) res = res.concat(left); else if (p === 1) res = res.concat(right); else res.push({ type: 'literal', value: p });
    });
    return res;
  };
  if (items.length === 2) out = apply(lpat[3], items[0], items[1]);
  else {
    out = apply(lpat[0], items[0], items[1]);
    for (i = 2; i < items.length - 1; i++) out = apply(lpat[1], out, items[i]);
    out = apply(lpat[2], out, items[items.length - 1]);
  }
  return mergeLiterals(out);
}
H.define(DurationFormat.prototype, 'format', function format(d) {
  var s = slotOf(this, DF, 'DurationFormat', 'format'), out = '';
  durParts(s, d).forEach(function (p) { out += p.value; });
  return out;
});
H.define(DurationFormat.prototype, 'formatToParts', function formatToParts(d) { return durParts(slotOf(this, DF, 'DurationFormat', 'formatToParts'), d); });
H.define(DurationFormat.prototype, 'resolvedOptions', function resolvedOptions() {
  var s = slotOf(this, DF, 'DurationFormat', 'resolvedOptions'), o = { locale: s.locale, numberingSystem: s.nu, style: s.style };
  DUR.forEach(function (u) { o[u] = s.units[u].style; o[u + 'Display'] = s.units[u].display; });
  if (s.fractionalDigits !== undefined) o.fractionalDigits = s.fractionalDigits;
  return o;
});
defineClass(DurationFormat, 'DurationFormat', 0);

// ---- Collator ---------------------------------------------------------------------------------------------------------------------------
// A multi-level comparison in the spirit of the Unicode Collation Algorithm: whitespace < punctuation < symbols < digits <
// letters (Latin, then other scripts by code point); accents are the second level, case the third (lower case first). Locales
// with letters of their own (sv, da, es, pl, cs, tr, ...) get CLDR's tailorings. East Asian orderings (pinyin, stroke) and
// contractions beyond those listed are not implemented: those scripts order by code point.
var CO = Symbol('Collator');
var TAILOR = {
  sv: [['z', ['å', ['ä', 'æ'], ['ö', 'ø']]]], fi: [['z', ['å', ['ä', 'æ'], ['ö', 'ø']]]],
  da: [['z', [['æ', 'ä'], ['ø', 'ö'], 'å']]], nb: [['z', [['æ', 'ä'], ['ø', 'ö'], 'å']]], nn: [['z', [['æ', 'ä'], ['ø', 'ö'], 'å']]], no: [['z', [['æ', 'ä'], ['ø', 'ö'], 'å']]],
  es: [['n', ['ñ']]], gl: [['n', ['ñ']]], ast: [['n', ['ñ']]],
  pl: [['a', ['ą']], ['c', ['ć']], ['e', ['ę']], ['l', ['ł']], ['n', ['ń']], ['o', ['ó']], ['s', ['ś']], ['z', ['ź', 'ż']]],
  cs: [['c', ['č']], ['h', ['ch']], ['r', ['ř']], ['s', ['š']], ['z', ['ž']]], sk: [['a', ['ä']], ['c', ['č']], ['h', ['ch']], ['o', ['ô']], ['r', ['ř']], ['s', ['š']], ['z', ['ž']]],
  tr: [['c', ['ç']], ['g', ['ğ']], ['h', ['ı']], ['o', ['ö']], ['s', ['ş']], ['u', ['ü']]], az: [['c', ['ç']], ['e', ['ə']], ['g', ['ğ']], ['h', ['x']], ['i', ['ı']], ['o', ['ö']], ['s', ['ş']], ['u', ['ü']]],
  hu: [['o', ['ö', 'ő']], ['u', ['ü', 'ű']]],
  ro: [['a', ['ă', 'â']], ['i', ['î']], ['s', ['ș', 'ş']], ['t', ['ț', 'ţ']]],
  hr: [['c', ['č', 'ć']], ['d', ['đ']], ['s', ['š']], ['z', ['ž']]], bs: [['c', ['č', 'ć']], ['d', ['đ']], ['s', ['š']], ['z', ['ž']]], sl: [['c', ['č']], ['s', ['š']], ['z', ['ž']]], sr: [['c', ['č', 'ć']], ['d', ['đ']], ['s', ['š']], ['z', ['ž']]],
  sq: [['c', ['ç']], ['e', ['ë']]], et: [['s', ['š']], ['z', ['ž']], ['z', ['õ', 'ä', 'ö', 'ü']]], lt: [['c', ['č']], ['s', ['š']], ['z', ['ž']]], lv: [['c', ['č']], ['g', ['ģ']], ['k', ['ķ']], ['l', ['ļ']], ['n', ['ņ']], ['s', ['š']], ['z', ['ž']]],
  is: [['d', ['ð']], ['z', ['þ', 'æ', 'ö']]], vi: [['a', ['ă', 'â']], ['d', ['đ']], ['e', ['ê']], ['o', ['ô', 'ơ']], ['u', ['ư']]]
};
var SPECIAL = { 'ß': ['s', 's'], 'æ': ['a', 'e'], 'œ': ['o', 'e'], 'ĳ': ['i', 'j'], 'ŉ': ['n', 'ʼ'], 'ǆ': ['d', 'z'], 'ǉ': ['l', 'j'], 'ǌ': ['n', 'j'] };
var STROKE = { 'ø': 'o', 'đ': 'd', 'ł': 'l', 'ħ': 'h', 'ŧ': 't', 'ƀ': 'b', 'ı': 'i' }; // no decomposition: their own primary right after the plain letter
var PUNCT_ORDER = '_-,;:!?.\'"()[]{}@*/\\&#%`^+<=>|~$';
var ND_RE = /\p{Nd}/u, P_RE = /\p{P}/u, S_RE = /\p{S}/u, Z_RE = /[\s\p{Z}]/u, M_RE = /\p{M}/u, C_RE = /[\p{Cc}\p{Cf}]/u, L_RE = /\p{L}/u;
// secondary weights of combining marks (CLDR root order of U+0300..036F)
var MARK_ORDER = '34f 332 313 343 314 301 341 300 340 306 302 30c 30a 342 308 344 30b 303 307 338 327 328 304 30d 30e 312 315 31a 33d 33e 33f 346 34a 34b 34c 350 351 352 357 35b 35d 35e 316 317 318 319 31c 31d 31e 31f 320 329 32a 32b 32c 32f 333 33a 33b 33c 347 348 349 34d 34e 353 354 355 356 359 35a 35c 35f 362 336 337 335 305 309 30f 310 311 31b 321 322 323 324 325 326 32d 32e 330 331 334 339 345 358 360 361 363 368 369 364 36a 365 36b 366 36c 36d 367 36e 36f'.split(' ');
var markRankMap = null;
function markRank(cp) {
  if (!markRankMap) { markRankMap = Object.create(null); MARK_ORDER.forEach(function (h, i) { markRankMap[parseInt(h, 16)] = i + 1; }); }
  return markRankMap[cp] || 200 + (cp & 1023);
}
var tailorCache = Object.create(null);
function tailorFor(lang, script) {
  var key = lang + (script ? '-' + script : '');
  if (tailorCache[key]) return tailorCache[key];
  var spec = TAILOR[lang], map = Object.create(null), digraphs = [];
  if (lang === 'sr' && script === 'Cyrl') spec = null;
  if (spec) spec.forEach(function (entry) {
    var after = entry[0], base = 5e6 + (after.charCodeAt(0) - 97) * 1000 + 10;
    if (lang === 'tr' && after === 'h') base = 5e6 + (104 - 97) * 1000 + 500; // dotless i sits between h and i
    entry[1].forEach(function (c, i) {
      var group = Array.isArray(c) ? c : [c];
      group.forEach(function (ch, v) {
        map[ch] = { w: base + i * 10, v: v };
        if (ch.length > 1) digraphs.push(ch);
      });
    });
  });
  digraphs.sort(function (a, b) { return b.length - a.length; });
  return (tailorCache[key] = { map: map, digraphs: digraphs });
}
function Collator(locales, options) {
  var self = this instanceof Collator ? this : Object.create(Collator.prototype);
  var requested = I.canonList(locales);
  options = I.coerceOptions(options);
  var usage = I.getOption(options, 'usage', 'string', ['sort', 'search'], 'sort');
  I.getOption(options, 'localeMatcher', 'string', ['lookup', 'best fit'], 'best fit');
  var collation = options.collation;
  if (collation !== undefined) { collation = String(collation); if (!/^[a-z0-9]{3,8}(-[a-z0-9]{3,8})*$/i.test(collation)) throw new RangeError('Invalid collation : ' + collation); }
  var numeric = options.numeric === undefined ? undefined : !!options.numeric;
  var caseFirst = I.getOption(options, 'caseFirst', 'string', ['upper', 'lower', 'false'], undefined);
  var r = I.resolveLocale(requested, ['co', 'kn', 'kf'], { co: collation, kn: numeric === undefined ? undefined : String(numeric), kf: caseFirst });
  var l = new Locale(r.base);
  var sens = I.getOption(options, 'sensitivity', 'string', ['base', 'accent', 'case', 'variant'], 'variant');
  var ip = options.ignorePunctuation;
  ip = ip === undefined ? l.language === 'th' : !!ip;
  var kn = numeric !== undefined ? numeric : r.ext.kn !== undefined ? r.ext.kn !== 'false' : false;
  var kf = caseFirst !== undefined ? caseFirst : r.ext.kf || 'false';
  var co = r.ext.co && r.ext.co !== 'default' && r.ext.co !== 'standard' && r.ext.co !== 'search' ? r.ext.co : 'default';
  var locale = r.locale;
  // the extension keys the collator resolved appear in the tag only when they came from it
  self[CO] = { locale: locale, usage: usage, sensitivity: sens, ignorePunctuation: ip, collation: co, numeric: kn, caseFirst: kf, lang: l.language, script: l.script, tailor: tailorFor(l.language, l.script), keys: new Map(), bound: null, backwards: l.language === 'fr' && l.region === 'CA' };
  return self;
}
function digitValue(ch) {
  var cp = ch.codePointAt(0);
  if (cp >= 48 && cp <= 57) return cp - 48;
  var v = 0;
  while (v < 9 && ND_RE.test(String.fromCodePoint(cp - v - 1))) v++;
  return v;
}
function buildKey(s, str) {
  var p = [], sec = [], ter = [], tl = s.tailor;
  var chars = [], comp = [];
  Array.from(str.normalize('NFC')).forEach(function (c) {
    var n = c.normalize('NFKC');
    if (n === c) { chars.push(c); comp.push(0); } else Array.from(n).forEach(function (x) { chars.push(x); comp.push(1); });
  });
  var upperFirst = s.caseFirst === 'upper';
  var push = function (w, secv, terv) { p.push(w); sec.push(secv); ter.push(terv); };
  for (var i = 0; i < chars.length; i++) {
    var ch = chars[i], lower = ch.toLowerCase(), isUp = lower !== ch && ch.toUpperCase() === ch;
    var tv = (isUp ? (upperFirst ? 0 : 1) : (upperFirst ? 1 : 0)) + (comp[i] ? 4 : 0);
    if (tl.digraphs.length) {
      var matched = false;
      for (var d = 0; d < tl.digraphs.length && !matched; d++) {
        var dg = tl.digraphs[d];
        if (str.length && chars.slice(i, i + dg.length).join('').toLowerCase() === dg) {
          var rec = tl.map[dg]; push(rec.w, 0, tv); i += dg.length - 1; matched = true;
        }
      }
      if (matched) continue;
    }
    if (tl.map[lower]) { push(tl.map[lower].w, tl.map[lower].v, tv); continue; }
    if (C_RE.test(ch)) continue;
    if (Z_RE.test(ch)) { if (!s.ignorePunctuation) push(1e6 + (ch === ' ' ? 0 : ch.codePointAt(0)), 0, 0); continue; }
    if (ND_RE.test(ch)) {
      if (s.numeric) {
        var digs = '';
        while (i < chars.length && ND_RE.test(chars[i])) { digs += digitValue(chars[i]); i++; }
        i--;
        digs = digs.replace(/^0+(?=.)/, '');
        push(4e6 + digs.length, 0, 0);
        for (var q = 0; q < digs.length; q++) push(4e6 + 100 + Number(digs[q]), 0, 0);
      } else push(4e6 + 100 + digitValue(ch), 0, 0);
      continue;
    }
    var cp = ch.codePointAt(0);
    if (cp < 128 && !L_RE.test(ch)) {
      var idx = PUNCT_ORDER.indexOf(ch);
      if (idx < 0) idx = 100 + cp;
      if (idx <= PUNCT_ORDER.indexOf('}')) { if (!s.ignorePunctuation) push(2e6 + idx, 0, 0); } else push(3e6 + idx, 0, 0);
      continue;
    }
    if (!L_RE.test(ch) && !M_RE.test(ch)) {
      if (P_RE.test(ch)) { if (!s.ignorePunctuation) push(2e6 + 1000 + cp, 0, 0); } else push(3e6 + 1000 + cp, 0, 0);
      continue;
    }
    if (SPECIAL[lower]) {
      var sp = SPECIAL[lower];
      push(5e6 + (sp[0].charCodeAt(0) - 97) * 1000, 1, tv);
      push(sp[1].charCodeAt(0) >= 97 && sp[1].charCodeAt(0) <= 122 ? 5e6 + (sp[1].charCodeAt(0) - 97) * 1000 : 6e6 + sp[1].charCodeAt(0), 1, tv);
      continue;
    }
    if (STROKE[lower]) { push(5e6 + (STROKE[lower].charCodeAt(0) - 97) * 1000 + 500, 0, tv); continue; }
    var dec = lower.normalize('NFD'), base = dec.codePointAt(0), marks = 0, k, c;
    for (k = String.fromCodePoint(base).length; k < dec.length; k++) { c = dec.charCodeAt(k); if (M_RE.test(dec[k])) marks = marks * 1024 + markRank(dec.codePointAt(k)); }
    // combining marks that follow a base character belong to it
    while (i + 1 < chars.length && M_RE.test(chars[i + 1]) && !tl.map[chars[i + 1]]) { marks = marks * 1024 + markRank(chars[i + 1].normalize('NFD').codePointAt(0)); i++; }
    if (M_RE.test(String.fromCodePoint(base))) { if (sec.length) sec[sec.length - 1] = sec[sec.length - 1] * 1024 + marks; continue; }
    var w;
    if (base >= 97 && base <= 122) w = 5e6 + (base - 97) * 1000;
    else if (base >= 0x30a1 && base <= 0x30f6) { w = 6e6 + base - 0x60; tv = tv || 2; } // katakana sorts with hiragana, after it
    else if (base < 0x250) w = 5e6 + 26000 + base;
    else w = 6e6 + scriptFirst(s.lang, base);
    push(w, marks, tv);
  }
  return { p: p, s: sec, t: ter };
}
// locales that sort their own script ahead of Latin (CLDR reorder codes)
function scriptFirst(lang, cp) {
  var cyr = cp >= 0x400 && cp <= 0x52f, grk = cp >= 0x370 && cp <= 0x3ff, han = cp >= 0x4e00 && cp <= 0x9fff, hang = cp >= 0xac00 && cp <= 0xd7af;
  if (cyr && (lang === 'ru' || lang === 'uk' || lang === 'bg' || lang === 'be' || lang === 'mk' || lang === 'kk')) return cp - 0x400 - 14e5;
  if (grk && lang === 'el') return cp - 0x370 - 14e5;
  if (lang === 'ko' && hang) return cp - 0xac00 - 14e5;
  if (lang === 'ko' && han) return cp - 0x4e00 - 13e5;
  return cp;
}
function cmpArr(a, b, backwards) {
  var n = Math.min(a.length, b.length), i;
  if (backwards) {
    var ia = a.length - 1, ib = b.length - 1;
    while (ia >= 0 && ib >= 0) { if (a[ia] !== b[ib]) return a[ia] < b[ib] ? -1 : 1; ia--; ib--; }
    return a.length === b.length ? 0 : a.length < b.length ? -1 : 1;
  }
  for (i = 0; i < n; i++) if (a[i] !== b[i]) return a[i] < b[i] ? -1 : 1;
  return a.length === b.length ? 0 : a.length < b.length ? -1 : 1;
}
function keyOf(s, str) {
  var k = s.keys.get(str);
  if (!k) { if (s.keys.size > 4000) s.keys.clear(); k = buildKey(s, str); s.keys.set(str, k); }
  return k;
}
function collate(s, a, b) {
  if (a === b) return 0;
  var ka = keyOf(s, a), kb = keyOf(s, b), c = cmpArr(ka.p, kb.p);
  if (c) return c;
  var sens = s.sensitivity;
  if (sens === 'base') return 0;
  if (sens === 'accent' || sens === 'variant') { c = cmpArr(ka.s, kb.s, s.backwards); if (c) return c; }
  if (sens === 'case' || sens === 'variant') { c = cmpArr(ka.t, kb.t); if (c) return c; }
  return 0;
}
H.attrs(Collator.prototype, {
  compare: function () {
    var s = slotOf(this, CO, 'Collator', 'compare');
    if (!s.bound) { s.bound = function (a, b) { return collate(s, String(a), String(b)); }; Object.defineProperty(s.bound, 'name', { value: '', configurable: true }); }
    return s.bound;
  }
});
H.define(Collator.prototype, 'resolvedOptions', function resolvedOptions() {
  var s = slotOf(this, CO, 'Collator', 'resolvedOptions');
  return { locale: s.locale, usage: s.usage, sensitivity: s.sensitivity, ignorePunctuation: s.ignorePunctuation, collation: s.collation, numeric: s.numeric, caseFirst: s.caseFirst };
});
defineClass(Collator, 'Collator', 0);

// ---- Segmenter --------------------------------------------------------------------------------------------------------------------------
var SG = Symbol('Segmenter'), SGS = Symbol('Segments');
function Segmenter(locales, options) {
  requireNew(this, Segmenter, 'Segmenter');
  var requested = I.canonList(locales);
  options = I.coerceOptions(options);
  I.getOption(options, 'localeMatcher', 'string', ['lookup', 'best fit'], 'best fit');
  var r = I.resolveLocale(requested, [], {});
  this[SG] = { locale: r.locale, granularity: I.getOption(options, 'granularity', 'string', ['grapheme', 'word', 'sentence'], 'grapheme') };
}
var WORDLIKE = /[\p{L}\p{N}]/u;
function boundaries(seg) {
  if (!seg.b) seg.b = g.__sw.intlSegment(seg.granularity, seg.input) || [];
  return seg.b;
}
function segmentAt(seg, k) { // the k-th segment
  var b = boundaries(seg), start = b[k], end = b[k + 1];
  var o = { segment: seg.input.slice(start, end), index: start, input: seg.input };
  if (seg.granularity === 'word') o.isWordLike = WORDLIKE.test(o.segment);
  return o;
}
H.define(Segmenter.prototype, 'segment', function segment(str) {
  var s = slotOf(this, SG, 'Segmenter', 'segment');
  var o = Object.create(SegmentsProto);
  Object.defineProperty(o, SGS, { value: { granularity: s.granularity, input: String(str), b: null } });
  return o;
});
H.define(Segmenter.prototype, 'resolvedOptions', function resolvedOptions() {
  var s = slotOf(this, SG, 'Segmenter', 'resolvedOptions');
  return { locale: s.locale, granularity: s.granularity };
});
var SegmentsProto = {};
H.define(SegmentsProto, 'containing', function containing(index) {
  var seg = this[SGS];
  if (!seg) throw new TypeError('Method %Segments.prototype%.containing called on incompatible receiver');
  var n = index === undefined ? 0 : Number(index);
  n = n !== n ? 0 : Math.trunc(n);
  if (n < 0 || n >= seg.input.length) return undefined;
  var b = boundaries(seg), lo = 0, hi = b.length - 2;
  while (lo < hi) { var mid = (lo + hi + 1) >> 1; if (b[mid] <= n) lo = mid; else hi = mid - 1; }
  return segmentAt(seg, lo);
});
var SegmentIteratorProto = Object.create(Object.getPrototypeOf(Object.getPrototypeOf([][Symbol.iterator]())));
H.define(SegmentIteratorProto, 'next', function next() {
  var st = this[SGS];
  if (!st) throw new TypeError('Method %SegmentIterator.prototype%.next called on incompatible receiver');
  var b = boundaries(st.seg);
  if (st.k >= b.length - 1) return { value: undefined, done: true };
  return { value: segmentAt(st.seg, st.k++), done: false };
});
Object.defineProperty(SegmentIteratorProto, Symbol.toStringTag, { value: 'Segmenter String Iterator', configurable: true });
Object.defineProperty(SegmentsProto, Symbol.iterator, {
  value: function () {
    var seg = this[SGS];
    if (!seg) throw new TypeError('Method %Segments.prototype%[@@iterator] called on incompatible receiver');
    var it = Object.create(SegmentIteratorProto);
    Object.defineProperty(it, SGS, { value: { seg: seg, k: 0 } });
    return it;
  }, writable: true, configurable: true
});
defineClass(Segmenter, 'Segmenter', 0);
