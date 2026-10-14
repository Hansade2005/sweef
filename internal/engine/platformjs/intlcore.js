// Shared by the Intl modules (intlnum, intldate, intlmisc): locale negotiation, locale data from CLDR (see
// tools/intlgen and internal/intl), plural rules, and exact decimal arithmetic. Published as H.intl.
H.load('intl'); // Intl.Locale
var sw = g.__sw, Locale = g.Intl.Locale;
var I = H.intl = {};

// ---- global data and locale data -----------------------------------------------------------------------------------------
var globalData = null, localeSet = null, langSet = null;
function G() {
  if (!globalData) {
    globalData = JSON.parse(sw.intlGlobal());
    localeSet = Object.create(null); langSet = Object.create(null);
    globalData.locales.forEach(function (t) { localeSet[t] = 1; langSet[t.split('-')[0]] = 1; });
  }
  return globalData;
}
I.G = G;
function isPlain(v) { return v !== null && typeof v === 'object' && !Array.isArray(v); }
function merge(a, b, childFirst) {
  if (!isPlain(a) || !isPlain(b)) return b;
  var out = {}, k;
  // null in the child removes what the parent has
  if (childFirst) { // availableFormats: the order is ICU's (the locale's own patterns before its parent's), see tools/intlgen
    for (k in b) if (b[k] !== null) out[k] = b[k];
    for (k in a) if (!(k in b)) out[k] = a[k];
    return out;
  }
  for (k in a) if (!(k in b) || b[k] !== null) out[k] = a[k];
  for (k in b) if (b[k] !== null) out[k] = k in a ? merge(a[k], b[k], k === 'af') : b[k];
  return out;
}
I.merge = function (a, b) { return merge(a, b); };
var dataCache = Object.create(null);
function dataFor(tag) {
  if (dataCache[tag]) return dataCache[tag];
  var s = sw.intlLocale(tag);
  if (!s) return null;
  var d = JSON.parse(s);
  if (d.parent) d = merge(dataFor(d.parent), d);
  dataCache[tag] = d;
  return d;
}
I.data = dataFor;
I.hasLanguage = function (lang) { G(); return !!langSet[lang]; };
// the shipped locale whose data answers for a (canonical, extension-free) tag
I.dataTag = function (tag) {
  G();
  var l = new Locale(tag), lang = l.language, script = l.script, region = l.region;
  if (lang === 'zh' && !script) script = region === 'TW' || region === 'HK' || region === 'MO' ? 'Hant' : 'Hans';
  var cands = [];
  if (script && region) cands.push(lang + '-' + script + '-' + region);
  if (script) cands.push(lang + '-' + script);
  if (region) cands.push(lang + '-' + region);
  cands.push(lang);
  var parents = globalData.parents;
  for (var i = 0; i < cands.length; i++) {
    var t = cands[i], guard = 0;
    while (t && guard++ < 8) {
      if (localeSet[t]) return t;
      if (t === 'zh-Hans' || t.indexOf('zh-Hans-') === 0) { if (localeSet.zh) return 'zh'; }
      var p = parents[t];
      if (p) { t = p; continue; }
      var cut = t.lastIndexOf('-');
      t = cut > 0 ? t.slice(0, cut) : '';
    }
  }
  return 'en';
};
I.localeData = function (tag) { return dataFor(I.dataTag(tag)); };
// the default numbering system of a locale: its data's, unless CLDR gives the region another (ar-EG and ar-SA count in Arabic-Indic digits)
I.defaultNu = function (tag, data) {
  var t = G().nu;
  if (t) {
    var l = new Locale(tag), lang = l.language, cands = [];
    if (l.script && l.region) cands.push(lang + '-' + l.script + '-' + l.region);
    if (l.script) cands.push(lang + '-' + l.script);
    if (l.region) cands.push(lang + '-' + l.region);
    for (var i = 0; i < cands.length; i++) if (t[cands[i]]) return t[cands[i]];
  }
  return data.n.nu;
};

// ---- locale lists -------------------------------------------------------------------------------------------------------------
function badLocale() { return new RangeError('Incorrect locale information provided'); }
I.canon = function (tag) {
  var s = String(tag);
  if (!/^[A-Za-z0-9-]+$/.test(s)) throw badLocale();
  return new Locale(s).toString();
};
I.canonList = function (locales) {
  if (locales === undefined) return [];
  var list = [];
  if (typeof locales === 'string' || locales instanceof Locale) list = [locales];
  else {
    if (locales === null) throw new TypeError('Cannot convert undefined or null to object');
    var o = Object(locales), len = Math.min(o.length >>> 0, 0xffffffff);
    for (var k = 0; k < len; k++) if (k in o) list.push(o[k]);
  }
  var out = [];
  list.forEach(function (v) {
    if (typeof v !== 'string' && (v === null || typeof v !== 'object')) throw new TypeError('Incorrect locale information provided');
    var t = I.canon(v instanceof Locale ? v.toString() : v);
    if (out.indexOf(t) < 0) out.push(t);
  });
  return out;
};
function stripExt(tag) { var i = tag.search(/-[a-wyz0-9]-/i); var u = tag.indexOf('-u-'); var cut = -1; if (u >= 0) cut = u; var x = tag.indexOf('-x-'); if (x >= 0 && (cut < 0 || x < cut)) cut = x; return cut >= 0 ? tag.slice(0, cut) : tag; }
I.baseOf = function (tag) {
  var l = new Locale(tag);
  return l.baseName;
};
I.defaultLocale = 'en-US';
// supportedLocalesOf: the requested tags whose language has data
I.supportedLocalesOf = function (locales, options) {
  var req = I.canonList(locales);
  if (options !== undefined) {
    options = Object(options);
    var m = options.localeMatcher;
    if (m !== undefined && ['lookup', 'best fit'].indexOf(String(m)) < 0) throw new RangeError("Value " + m + " out of range for Intl.NumberFormat options property localeMatcher");
  }
  var out = [];
  req.forEach(function (t) { if (I.hasLanguage(new Locale(t).language)) out.push(t); });
  return out;
};
// ResolveLocale: requested tags, the extension keys the constructor understands (ca, co, hc, kf, kn, nu) and the values
// the options gave for them. Returns {locale, base, ext: {key: value}}.
var EXT_VALID = {
  nu: function (v) { return !!G().digits[v]; },
  ca: function (v) { return ['gregory', 'gregorian', 'iso8601', 'buddhist', 'japanese', 'roc', 'persian', 'islamic', 'islamic-civil', 'islamicc', 'islamic-tbla', 'islamic-umalqura', 'islamic-rgsa', 'hebrew', 'indian', 'coptic', 'ethiopic', 'ethiopic-amete-alem', 'ethioaa'].indexOf(v) >= 0; },
  hc: function (v) { return ['h11', 'h12', 'h23', 'h24'].indexOf(v) >= 0; },
  co: function (v) { return ['default', 'phonebk', 'pinyin', 'stroke', 'trad', 'emoji', 'eor', 'search', 'standard', 'unihan', 'zhuyin'].indexOf(v) >= 0; },
  kn: function (v) { return v === '' || v === 'true' || v === 'false'; },
  kf: function (v) { return ['upper', 'lower', 'false'].indexOf(v) >= 0; }
};
I.resolveLocale = function (requested, keys, optionValues) {
  var found = null;
  for (var i = 0; i < requested.length && !found; i++) {
    var l = new Locale(requested[i]);
    if (I.hasLanguage(l.language)) found = l;
  }
  var base = found ? found.baseName : I.defaultLocale, ext = {}, kept = [];
  keys.forEach(function (k) {
    var v;
    if (found) {
      var name = { ca: 'calendar', co: 'collation', hc: 'hourCycle', kf: 'caseFirst', nu: 'numberingSystem' }[k];
      v = k === 'kn' ? (found.numeric ? '' : (/-kn-false/.test(String(found)) ? 'false' : undefined)) : found[name];
      if (k === 'kn' && v === undefined && /-kn(-|$)/.test(String(found)) && found.numeric) v = '';
    }
    var keep = v !== undefined && EXT_VALID[k](v === '' ? '' : v);
    if (keep) { ext[k] = v; }
    var o = optionValues && optionValues[k];
    if (o !== undefined && o !== null && EXT_VALID[k](String(o))) {
      if (keep && String(o) === v) kept.push(k);
      else keep = false;
      ext[k] = String(o);
    } else if (keep) kept.push(k);
  });
  var locale = base;
  if (kept.length) {
    kept.sort();
    locale += '-u' + kept.map(function (k) { return '-' + k + (ext[k] === '' || ext[k] === 'true' ? '' : '-' + ext[k]); }).join('');
  }
  var keptSet = {};
  kept.forEach(function (k) { keptSet[k] = true; });
  return { locale: locale, base: base, ext: ext, kept: keptSet }; // kept: the extension keys that came from the locale itself, not from an option
};

// ---- options -------------------------------------------------------------------------------------------------------------------------
I.getOption = function (options, name, type, values, fallback) {
  var v = options[name];
  if (v === undefined) return fallback;
  if (type === 'boolean') v = !!v;
  else {
    v = String(v);
    if (values && values.indexOf(v) < 0) throw new RangeError('Value ' + v + ' out of range for Intl options property ' + name);
  }
  return v;
};
I.getNumberOption = function (options, name, min, max, fallback) {
  var v = options[name];
  if (v === undefined) return fallback;
  v = Number(v);
  if (v !== v || v < min || v > max) throw new RangeError(name + ' value is out of range.');
  return Math.floor(v);
};
I.coerceOptions = function (options) {
  if (options === undefined) return Object.create(null);
  if (options === null) throw new TypeError('Cannot convert undefined or null to object');
  return Object(options);
};

// ---- numbering systems -------------------------------------------------------------------------------------------------------
I.digitsOf = function (nu) { var d = G().digits[nu]; return d ? Array.from(d) : null; };
I.localDigits = function (str, nu) {
  if (!nu || nu === 'latn') return str;
  var d = I.digitsOf(nu);
  if (!d) return str;
  return str.replace(/[0-9]/g, function (c) { return d[c.charCodeAt(0) - 48]; });
};

// ---- exact decimals -----------------------------------------------------------------------------------------------------------------
// {neg, d, e}: the value is (-1)^neg * int(d) * 10^e, d without leading zeros ("0" for zero). Specials: {nan:true}, {inf:true, neg}.
var DEC = I.dec = {};
function stripZeros(d) { var i = 0; while (i < d.length - 1 && d.charCodeAt(i) === 48) i++; return i ? d.slice(i) : d; }
DEC.zero = function (neg) { return { neg: !!neg, d: '0', e: 0 }; };
DEC.parse = function (s) { // a decimal literal; null when it is not one
  var m = /^\s*([+-])?(?:(\d+)(?:\.(\d*))?|\.(\d+))(?:[eE]([+-]?\d+))?\s*$/.exec(s);
  if (!m) {
    var t = String(s).trim();
    if (/^[+-]?Infinity$/.test(t)) return { inf: true, neg: t.charAt(0) === '-' };
    if (/^0[xX][0-9a-fA-F]+$/.test(t)) return DEC.fromBigInt(BigInt(t));
    if (/^0[bB][01]+$/.test(t) || /^0[oO][0-7]+$/.test(t)) return DEC.fromBigInt(BigInt(t));
    if (t === '') return DEC.zero(false);
    return null;
  }
  var ip = m[2] !== undefined ? m[2] : '', fp = m[3] !== undefined ? m[3] : (m[4] || '');
  var d = stripZeros(ip + fp), e = (m[5] ? parseInt(m[5], 10) : 0) - fp.length;
  if (/^0*$/.test(d)) return { neg: m[1] === '-', d: '0', e: 0 };
  return { neg: m[1] === '-', d: d, e: e };
};
DEC.fromNumber = function (x) {
  if (x !== x) return { nan: true };
  if (x === Infinity || x === -Infinity) return { inf: true, neg: x < 0 };
  if (x === 0) return DEC.zero(1 / x < 0);
  return DEC.parse(String(x));
};
DEC.fromBigInt = function (b) { var neg = b < 0n, s = (neg ? -b : b).toString(); return { neg: neg, d: s, e: 0 }; };
// any value an Intl method accepts: numbers, bigints, numeric strings (which are not rounded through a double)
DEC.from = function (v) {
  if (typeof v === 'bigint') return DEC.fromBigInt(v);
  if (typeof v === 'string') { var p = DEC.parse(v); return p || { nan: true }; }
  if (typeof v === 'object' && v !== null) {
    var prim = v[Symbol.toPrimitive] || v.valueOf ? (typeof v.valueOf === 'function' ? v.valueOf() : v) : v;
    if (typeof prim === 'object') prim = String(v);
    return DEC.from(prim);
  }
  return DEC.fromNumber(Number(v));
};
DEC.norm = function (x) { // drop trailing zeros of d
  if (x.nan || x.inf) return x;
  var d = x.d, e = x.e, n = d.length;
  while (n > 1 && d.charCodeAt(n - 1) === 48) { n--; e++; }
  return { neg: x.neg, d: d.slice(0, n), e: d === '0' ? 0 : e };
};
DEC.isZero = function (x) { return !x.nan && !x.inf && x.d === '0'; };
DEC.mag = function (x) { return x.d.length + x.e - 1; }; // exponent of the leading digit
DEC.scale = function (x, n) { return x.nan || x.inf || x.d === '0' ? x : { neg: x.neg, d: x.d, e: x.e + n }; };
DEC.toNumber = function (x) { return x.nan ? NaN : x.inf ? (x.neg ? -Infinity : Infinity) : (x.neg ? -1 : 1) * Number(x.d + 'e' + x.e); };
function incr(d) { // d + 1 on a digit string
  var a = d.split(''), i = a.length - 1;
  while (i >= 0) { if (a[i] === '9') { a[i] = '0'; i--; } else { a[i] = String.fromCharCode(a[i].charCodeAt(0) + 1); return a.join(''); } }
  return '1' + a.join('');
}
// round to the digit at position pos (10^pos); mode as in Intl.NumberFormat roundingMode
DEC.roundAt = function (x, pos, mode) {
  if (x.nan || x.inf || x.d === '0') return x;
  if (x.e >= pos) return x;
  var drop = pos - x.e, d = x.d;
  var kept, dropped;
  if (drop >= d.length) { kept = '0'; dropped = new Array(drop - d.length + 1).join('0') + d; }
  else { kept = d.slice(0, d.length - drop); dropped = d.slice(d.length - drop); }
  if (/^0*$/.test(dropped)) return DEC.fix({ neg: x.neg, d: kept, e: pos });
  var half = '5' + new Array(drop).join('0'), cmp = dropped < half ? -1 : dropped === half ? 0 : 1;
  var neg = x.neg, up;
  switch (mode) {
    case 'ceil': up = !neg; break;
    case 'floor': up = neg; break;
    case 'expand': up = true; break;
    case 'trunc': up = false; break;
    case 'halfCeil': up = cmp > 0 || (cmp === 0 && !neg); break;
    case 'halfFloor': up = cmp > 0 || (cmp === 0 && neg); break;
    case 'halfTrunc': up = cmp > 0; break;
    case 'halfEven': up = cmp > 0 || (cmp === 0 && ((kept.charCodeAt(kept.length - 1) - 48) & 1) === 1); break;
    default: up = cmp >= 0; // halfExpand
  }
  if (up) kept = incr(kept);
  return DEC.fix({ neg: neg, d: kept, e: pos });
};
DEC.fix = function (x) { x.d = stripZeros(x.d); if (x.d === '0') x.e = 0; return x; };
// round to a number of significant digits
DEC.roundSig = function (x, sig, mode) {
  if (x.nan || x.inf || x.d === '0') return x;
  return DEC.roundAt(x, DEC.mag(x) - sig + 1, mode);
};
// round to a multiple of inc * 10^pos
DEC.roundIncrement = function (x, pos, inc, mode) {
  if (x.nan || x.inf || x.d === '0') return x;
  // value in units of 10^pos, as an exact integer fraction: scale then round to the nearest multiple of inc
  var v = x.e >= pos ? BigInt(x.d) * (10n ** BigInt(x.e - pos)) : null, scaled, rem;
  var B = BigInt(inc);
  if (v === null) {
    // x has digits below pos: q = floor(value / 10^pos), r = what is left as a fraction of 10^pos
    var drop = pos - x.e, d = x.d, intPart, frac;
    if (drop >= d.length) { intPart = 0n; frac = new Array(drop - d.length + 1).join('0') + d; } else { intPart = BigInt(d.slice(0, d.length - drop)); frac = d.slice(d.length - drop); }
    v = intPart; rem = frac;
  } else rem = '';
  var neg = x.neg, q = v / B, r = v % B; // r in [0, B)
  // r + rem/10^len is the position inside the interval [q*B, (q+1)*B); compare with half of B
  var hasRem = /[1-9]/.test(rem), twice = r * 2n;
  // position of r + 0.rem against B/2: compare 2r + 2*0.rem with B
  var fHalf = !hasRem ? -1 : (rem < '5' + new Array(rem.length).join('0') ? -1 : rem === '5' + new Array(rem.length).join('0') ? 0 : 1); // 0.rem against 0.5
  var cmp;
  if (twice >= B + 1n) cmp = 1;
  else if (twice + 1n < B) cmp = -1;
  else if (twice === B) cmp = hasRem ? 1 : 0;
  else if (twice === B - 1n) cmp = !hasRem ? -1 : (fHalf < 0 ? -1 : fHalf === 0 ? 0 : 1);
  else cmp = -1;
  var exact = r === 0n && !hasRem, up;
  if (exact) up = false;
  else switch (mode) {
    case 'ceil': up = !neg; break;
    case 'floor': up = neg; break;
    case 'expand': up = true; break;
    case 'trunc': up = false; break;
    case 'halfCeil': up = cmp > 0 || (cmp === 0 && !neg); break;
    case 'halfFloor': up = cmp > 0 || (cmp === 0 && neg); break;
    case 'halfTrunc': up = cmp > 0; break;
    case 'halfEven': up = cmp > 0 || (cmp === 0 && (q & 1n) === 1n); break;
    default: up = cmp >= 0;
  }
  var res = (up ? q + 1n : q) * B;
  return DEC.fix({ neg: neg, d: res.toString(), e: pos });
};
// digits of the integer part and the fraction part, each as a string (integer part "0" when empty)
DEC.split = function (x) {
  var d = x.d, e = x.e, ip, fp;
  if (e >= 0) { ip = d + new Array(e + 1).join('0'); fp = ''; }
  else if (-e >= d.length) { ip = '0'; fp = new Array(-e - d.length + 1).join('0') + d; }
  else { ip = d.slice(0, d.length + e); fp = d.slice(d.length + e); }
  return { i: stripZeros(ip), f: fp };
};

// ---- plural rules ---------------------------------------------------------------------------------------------------------------------
var pluralData = null;
function PD() { return pluralData || (pluralData = JSON.parse(sw.intlPlurals())); }
var ruleCache = Object.create(null);
function compileRule(src) {
  var ors = src.split(/\s+or\s+/).map(function (a) {
    return a.split(/\s+and\s+/).map(function (r) {
      var m = /^([nivwftce])(?:\s*%\s*(\d+))?\s*(!=|=)\s*(.+)$/.exec(r.trim());
      if (!m) return function () { return false; };
      var ranges = m[4].split(',').map(function (x) {
        var p = x.trim().split('..');
        return p.length === 2 ? [+p[0], +p[1], true] : [+p[0], +p[0], false];
      });
      var op = m[1], mod = m[2] ? +m[2] : 0, neg = m[3] === '!=';
      return function (o) {
        var x = o[op];
        if (mod) x = x % mod;
        var hit = ranges.some(function (r) { return r[2] ? (x === Math.floor(x) && x >= r[0] && x <= r[1]) : x === r[0]; });
        return neg ? !hit : hit;
      };
    });
  });
  return function (o) { return ors.some(function (ands) { return ands.every(function (f) { return f(o); }); }); };
}
var CAT_ORDER = ['zero', 'one', 'two', 'few', 'many'];
function rulesFor(type, tag) {
  var key = type + ':' + tag;
  if (ruleCache[key]) return ruleCache[key];
  var table = PD()[type === 'ordinal' ? 'o' : 'c'], l = new Locale(tag), cands = [l.language + (l.region ? '-' + l.region : ''), l.language], src = null, k = null;
  for (var i = 0; i < cands.length && !src; i++) if (table[cands[i]]) { src = table[cands[i]]; k = cands[i]; }
  var rules = [];
  if (src) CAT_ORDER.forEach(function (c) { if (src[c]) rules.push([c, compileRule(src[c])]); });
  return (ruleCache[key] = { rules: rules, lang: k });
}
I.pluralLocale = function (type, tag) { return rulesFor(type, tag).lang || new Locale(tag).language; };
I.pluralCategories = function (type, tag) {
  var r = rulesFor(type, tag), out = [];
  r.rules.forEach(function (x) { out.push(x[0]); });
  out.push('other');
  return out;
};
// operands from the digits as shown: {i: integer part string, f: fraction part string}; e is the compact exponent
I.operands = function (ip, fp, e) {
  var tr = fp.replace(/0+$/, '');
  return { n: Number(ip + (fp ? '.' + fp : '')), i: Number(ip), v: fp.length, w: tr.length, f: fp ? Number(fp) : 0, t: tr ? Number(tr) : 0, c: e || 0, e: e || 0 };
};
I.plural = function (type, tag, ops) {
  var rules = rulesFor(type, tag).rules;
  for (var i = 0; i < rules.length; i++) if (rules[i][1](ops)) return rules[i][0];
  return 'other';
};
I.pluralRange = function (tag, a, b) {
  var l = new Locale(tag), r = PD().r, t = r[l.language + (l.region ? '-' + l.region : '')] || r[l.language];
  if (!t) return 'other';
  return t[a + ':' + b] || 'other';
};
I.supportedValues = function (key) {
  G();
  switch (key) {
    case 'calendar': return ['buddhist', 'chinese', 'coptic', 'dangi', 'ethioaa', 'ethiopic', 'gregory', 'hebrew', 'indian', 'islamic', 'islamic-civil', 'islamic-rgsa', 'islamic-tbla', 'islamic-umalqura', 'iso8601', 'japanese', 'persian', 'roc'];
    case 'collation': return ['compat', 'dict', 'emoji', 'eor', 'phonebk', 'phonetic', 'pinyin', 'searchjl', 'stroke', 'trad', 'unihan', 'zhuyin'];
    case 'currency': return globalData.currencies.filter(function (c) { return /^[A-Z]{3}$/.test(c); }).sort();
    case 'numberingSystem': return Object.keys(globalData.digits).sort();
    case 'timeZone': return globalData.tz.slice().sort();
    case 'unit': return ['acre', 'bit', 'byte', 'celsius', 'centimeter', 'day', 'degree', 'fahrenheit', 'fluid-ounce', 'foot', 'gallon', 'gigabit', 'gigabyte', 'gram', 'hectare', 'hour', 'inch', 'kilobit', 'kilobyte', 'kilogram', 'kilometer', 'liter', 'megabit', 'megabyte', 'meter', 'microsecond', 'mile', 'mile-scandinavian', 'milliliter', 'millimeter', 'millisecond', 'minute', 'month', 'nanosecond', 'ounce', 'percent', 'petabyte', 'pound', 'second', 'stone', 'terabit', 'terabyte', 'week', 'yard', 'year'];
  }
  return null;
};
