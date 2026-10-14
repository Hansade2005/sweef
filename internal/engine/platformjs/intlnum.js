// Intl.NumberFormat (ECMA-402 2023, "NumberFormat v3") on CLDR data. Numbers are handled as exact decimals
// (intlcore.js), so rounding modes, increments, bigints and decimal strings behave as in ICU. Formatting itself:
// round to the digit options, split into integer and fraction digits, group, then lay the pieces out with the
// locale's patterns (prefix, number, suffix) into parts.
H.load('intlcore');
var I = H.intl, DEC = I.dec, Locale = g.Intl.Locale;
var SLOT = Symbol('NumberFormat');

var SANCTIONED = ['acre', 'bit', 'byte', 'celsius', 'centimeter', 'day', 'degree', 'fahrenheit', 'fluid-ounce', 'foot', 'gallon', 'gigabit', 'gigabyte', 'gram', 'hectare', 'hour', 'inch', 'kilobit', 'kilobyte', 'kilogram', 'kilometer', 'liter', 'megabit', 'megabyte', 'meter', 'microsecond', 'mile', 'mile-scandinavian', 'milliliter', 'millimeter', 'millisecond', 'minute', 'month', 'nanosecond', 'ounce', 'percent', 'petabyte', 'pound', 'second', 'stone', 'terabit', 'terabyte', 'week', 'yard', 'year'];
var INCREMENTS = [1, 2, 5, 10, 20, 25, 50, 100, 200, 250, 500, 1000, 2000, 2500, 5000];
var MODES = ['ceil', 'floor', 'expand', 'trunc', 'halfCeil', 'halfFloor', 'halfExpand', 'halfTrunc', 'halfEven'];

function unitOK(u) {
  if (SANCTIONED.indexOf(u) >= 0) return true;
  var i = u.indexOf('-per-');
  return i > 0 && SANCTIONED.indexOf(u.slice(0, i)) >= 0 && SANCTIONED.indexOf(u.slice(i + 5)) >= 0;
}
function slot(o, m) {
  var s = o !== null && typeof o === 'object' ? o[SLOT] : undefined;
  if (!s) throw new TypeError('Method Intl.NumberFormat.prototype.' + m + ' called on incompatible receiver ' + (o === null ? 'null' : typeof o === 'object' ? '#<Object>' : String(o)));
  return s;
}

// ---- construction -----------------------------------------------------------------------------------------------------------------------
function currencyDigits(code) {
  var t = I.G().cdig[code] || I.G().cdig.DEFAULT;
  return t[0];
}
function initialize(nf, locales, options) {
  var requested = I.canonList(locales);
  options = I.coerceOptions(options);
  I.getOption(options, 'localeMatcher', 'string', ['lookup', 'best fit'], 'best fit');
  var nu = options.numberingSystem;
  if (nu !== undefined) { nu = String(nu); if (!/^[a-z0-9]{3,8}(-[a-z0-9]{3,8})*$/i.test(nu)) throw new RangeError('Invalid numberingSystem : ' + nu); nu = nu.toLowerCase(); }
  var r = I.resolveLocale(requested, ['nu'], { nu: nu });
  var data = I.localeData(r.base);
  var s = { locale: r.locale, data: data, dataTag: I.dataTag(r.base) };
  s.nu = r.ext.nu && I.digitsOf(r.ext.nu) ? r.ext.nu : I.defaultNu(r.base, data);
  if (data.n.fm && data.n.fm[s.nu]) { var nn = I.merge(data.n, data.n.fm[s.nu]); s.data = Object.assign({}, data, { n: nn }); } // the patterns of another numbering system
  s.style = I.getOption(options, 'style', 'string', ['decimal', 'percent', 'currency', 'unit'], 'decimal');
  var currency = options.currency;
  if (currency !== undefined) {
    currency = String(currency);
    if (!/^[A-Za-z]{3}$/.test(currency)) throw new RangeError('Invalid currency code : ' + currency);
    currency = currency.toUpperCase();
  }
  if (s.style === 'currency' && currency === undefined) throw new TypeError('Currency code is required with currency style.');
  var cd = I.getOption(options, 'currencyDisplay', 'string', ['code', 'symbol', 'narrowSymbol', 'name'], 'symbol');
  var cs = I.getOption(options, 'currencySign', 'string', ['standard', 'accounting'], 'standard');
  var unit = options.unit;
  if (unit !== undefined) { unit = String(unit); if (!unitOK(unit)) throw new RangeError('Invalid unit argument for Intl.NumberFormat() \'' + unit + '\''); }
  if (s.style === 'unit' && unit === undefined) throw new TypeError('Unit is required with unit style.');
  var ud = I.getOption(options, 'unitDisplay', 'string', ['short', 'narrow', 'long'], 'short');
  if (s.style === 'currency') { s.currency = currency; s.currencyDisplay = cd; s.currencySign = cs; }
  if (s.style === 'unit') { s.unit = unit; s.unitDisplay = ud; }
  s.notation = I.getOption(options, 'notation', 'string', ['standard', 'scientific', 'engineering', 'compact'], 'standard');
  var mnfdDefault, mxfdDefault;
  if (s.style === 'currency') { var cdg = currencyDigits(currency); mnfdDefault = mxfdDefault = cdg; } // (V8 rounds scientific and engineering currencies to the currency's digits too)
  else { mnfdDefault = 0; mxfdDefault = s.style === 'percent' ? 0 : 3; }
  setDigitOptions(s, options, mnfdDefault, mxfdDefault, s.notation);
  var compactDisplay = I.getOption(options, 'compactDisplay', 'string', ['short', 'long'], 'short');
  if (s.notation === 'compact') s.compactDisplay = compactDisplay;
  var defaultGrouping = s.notation === 'compact' ? 'min2' : 'auto';
  var ug = options.useGrouping;
  if (ug === undefined) ug = defaultGrouping;
  else if (ug === true || ug === 'true') ug = ug === true ? 'always' : defaultGrouping;
  else if (ug === false) ug = false;
  else if (ug === 'false') ug = defaultGrouping;
  else {
    ug = String(ug);
    if (['min2', 'auto', 'always'].indexOf(ug) < 0) throw new RangeError('Value ' + ug + ' out of range for Intl.NumberFormat options property useGrouping');
  }
  if (options.useGrouping === 0 || options.useGrouping === '' || options.useGrouping === null) ug = false;
  s.useGrouping = ug;
  s.signDisplay = I.getOption(options, 'signDisplay', 'string', ['auto', 'never', 'always', 'exceptZero', 'negative'], 'auto');
  nf[SLOT] = s;
}
function setDigitOptions(s, options, mnfdDefault, mxfdDefault, notation) {
  var mnid = I.getNumberOption(options, 'minimumIntegerDigits', 1, 21, 1);
  var mnfd = options.minimumFractionDigits, mxfd = options.maximumFractionDigits, mnsd = options.minimumSignificantDigits, mxsd = options.maximumSignificantDigits;
  s.minInt = mnid;
  var inc = I.getNumberOption(options, 'roundingIncrement', 1, 5000, 1);
  if (INCREMENTS.indexOf(inc) < 0) throw new RangeError('roundingIncrement value is out of range.');
  var mode = I.getOption(options, 'roundingMode', 'string', MODES, 'halfExpand');
  var prio = I.getOption(options, 'roundingPriority', 'string', ['auto', 'morePrecision', 'lessPrecision'], 'auto');
  var tzd = I.getOption(options, 'trailingZeroDisplay', 'string', ['auto', 'stripIfInteger'], 'auto');
  if (inc !== 1) mxfdDefault = mnfdDefault;
  s.roundingIncrement = inc; s.roundingMode = mode; s.trailingZeroDisplay = tzd;
  var hasSd = mnsd !== undefined || mxsd !== undefined, hasFd = mnfd !== undefined || mxfd !== undefined;
  var needSd = true, needFd = true;
  if (prio === 'auto') { needSd = hasSd; if (needSd || (!hasFd && notation === 'compact')) needFd = false; }
  function dflt(v, min, max, fb) {
    if (v === undefined) return fb;
    v = Number(v);
    if (v !== v || v < min || v > max) throw new RangeError('Value out of range for Intl.NumberFormat options property');
    return Math.floor(v);
  }
  if (needSd) {
    if (hasSd) { mnsd = dflt(mnsd, 1, 21, 1); mxsd = dflt(mxsd, mnsd, 21, 21); s.minSD = mnsd; s.maxSD = mxsd; }
    else { s.minSD = 1; s.maxSD = 21; }
  }
  if (needFd) {
    if (hasFd) {
      mnfd = dflt(mnfd, 0, 100, undefined); mxfd = dflt(mxfd, 0, 100, undefined);
      if (mnfd === undefined) mnfd = Math.min(mnfdDefault, mxfd);
      else if (mxfd === undefined) mxfd = Math.max(mxfdDefault, mnfd);
      else if (mnfd > mxfd) throw new RangeError('maximumFractionDigits value is out of range.');
      s.minFD = mnfd; s.maxFD = mxfd;
    } else { s.minFD = mnfdDefault; s.maxFD = mxfdDefault; }
  }
  if (!needSd && !needFd) { s.minFD = 0; s.maxFD = 0; s.minSD = 1; s.maxSD = 2; s.roundingType = 'morePrecision'; s.roundingPriority = 'morePrecision'; }
  else if (prio === 'morePrecision' || prio === 'lessPrecision') { s.roundingType = prio; s.roundingPriority = prio; }
  else if (hasSd) { s.roundingType = 'significantDigits'; s.roundingPriority = 'auto'; }
  else { s.roundingType = 'fractionDigits'; s.roundingPriority = 'auto'; }
  if (inc !== 1) {
    if (s.roundingType !== 'fractionDigits') throw new TypeError('roundingIncrement is only valid with fractionDigits rounding');
    if (s.maxFD !== s.minFD) throw new RangeError('maximumFractionDigits value is out of range.');
  }
}

// ---- patterns -------------------------------------------------------------------------------------------------------------------------------
// A CLDR number pattern ("¤#,##0.00;(¤#,##0.00)", "0K", "#E0") parsed into token lists. Number tokens: 'n'. Others: literal 'l',
// currency 'c' (v = count of ¤), percent 'p', permille 'm', minus 'mi', plus 'pl'.
var patCache = Object.create(null);
function parsePattern(src) {
  if (patCache[src]) return patCache[src];
  var halves = [], cur = '', q = false;
  for (var i = 0; i < src.length; i++) {
    var ch = src[i];
    if (ch === "'") { q = !q; cur += ch; continue; }
    if (ch === ';' && !q) { halves.push(cur); cur = ''; continue; }
    cur += ch;
  }
  halves.push(cur);
  var pos = parseHalf(halves[0]);
  var out = { pos: pos, neg: halves[1] !== undefined ? parseHalf(halves[1]) : null };
  return (patCache[src] = out);
}
function parseHalf(h) {
  var toks = [], lit = '', i = 0, numDone = false, info = { zeros: 0, minFrac: 0, group1: 0, group2: 0, sci: false };
  function flush() { if (lit) { toks.push({ t: 'l', v: lit }); lit = ''; } }
  while (i < h.length) {
    var ch = h[i];
    if (ch === "'") {
      if (h[i + 1] === "'") { lit += "'"; i += 2; continue; }
      var j = h.indexOf("'", i + 1); if (j < 0) j = h.length;
      lit += h.slice(i + 1, j); i = j + 1; continue;
    }
    if (!numDone && (ch === '#' || ch === '0' || ch === ',' || ch === '.')) {
      flush();
      var j2 = i;
      while (j2 < h.length && '#0,.'.indexOf(h[j2]) >= 0) j2++;
      var num = h.slice(i, j2), dot = num.indexOf('.'), ip = dot < 0 ? num : num.slice(0, dot), fp = dot < 0 ? '' : num.slice(dot + 1);
      info.zeros = (ip.match(/0/g) || []).length; info.minFrac = (fp.match(/0/g) || []).length; info.maxFrac = fp.length;
      var groups = ip.split(',');
      if (groups.length > 1) { info.group1 = groups[groups.length - 1].length; info.group2 = groups.length > 2 ? groups[groups.length - 2].length : info.group1; }
      if (h[j2] === 'E') { info.sci = true; j2++; if (h[j2] === '+') j2++; while (h[j2] === '0') j2++; }
      toks.push({ t: 'n' }); numDone = true; i = j2; continue;
    }
    if (ch === '¤') { flush(); var k = i; while (h[k] === '¤') k++; toks.push({ t: 'c', v: k - i }); i = k; continue; }
    if (ch === '%') { flush(); toks.push({ t: 'p' }); i++; continue; }
    if (ch === '‰') { flush(); toks.push({ t: 'm' }); i++; continue; }
    if (ch === '-') { flush(); toks.push({ t: 'mi' }); i++; continue; }
    if (ch === '+') { flush(); toks.push({ t: 'pl' }); i++; continue; }
    lit += ch; i++;
  }
  flush();
  return { toks: toks, info: info };
}

// ---- formatting -------------------------------------------------------------------------------------------------------------------------------
function symbolsOf(s) {
  var sym = s.data.n.sym, latn = sym.latn || sym[s.data.n.nu], b = sym[s.nu] || (I.G().rootSym[s.nu] ? Object.assign({}, latn, I.G().rootSym[s.nu]) : latn);
  if (s.style === 'currency' && (b.cg || b.cd)) { b = Object.assign({}, b); if (b.cg) b.g = b.cg; if (b.cd) b.d = b.cd; }
  return b;
}
function roundDigits(s, x) { // the digit options applied to a (scaled) decimal; returns {x, minFD, minSD?}
  var mode = s.roundingMode;
  if (s.roundingType === 'fractionDigits') {
    var r = s.roundingIncrement !== 1 ? DEC.roundIncrement(x, -s.maxFD, s.roundingIncrement, mode) : DEC.roundAt(x, -s.maxFD, mode);
    return { x: r, minFD: s.minFD };
  }
  if (s.roundingType === 'significantDigits') return { x: DEC.roundSig(x, s.maxSD, mode), minSD: s.minSD };
  // morePrecision / lessPrecision: both candidates, pick by the position of the last digit kept
  var zero = DEC.isZero(x);
  var sdPos = zero ? 0 : DEC.mag(x) - s.maxSD + 1, fdPos = -s.maxFD;
  var useSd = s.roundingType === 'morePrecision' ? sdPos <= fdPos : sdPos >= fdPos;
  if (useSd) return { x: DEC.roundSig(x, s.maxSD, mode), minSD: s.minSD };
  return { x: DEC.roundAt(x, -s.maxFD, mode), minFD: s.minFD };
}
function digitStrings(s, r) { // integer digits and fraction digits to show
  var x = r.x, sp = DEC.split(x), ip = sp.i, fp = sp.f.replace(/0+$/, '');
  var minFD = r.minFD || 0;
  if (r.minSD !== undefined) {
    var sig = ip === '0' ? (fp ? fp.replace(/^0+/, '').length : 1) : ip.length + fp.length;
    var need = r.minSD - sig;
    if (need > 0) minFD = Math.max(minFD, fp.length + need);
  }
  while (fp.length < minFD) fp += '0';
  if (s.trailingZeroDisplay === 'stripIfInteger' && /^0*$/.test(fp)) fp = '';
  if (ip.length < s.minInt) ip = new Array(s.minInt - ip.length + 1).join('0') + ip;
  return { i: ip, f: fp };
}
function groupInteger(s, ip, pat, symbols) {
  var info = pat.info, mode = s.useGrouping;
  if (mode === false || (pat.noGroup)) return [ip];
  var g1 = info.group1 || 3, g2 = info.group2 || g1;
  if (!info.group1 && !pat.forceGroup) { if (s.notation !== 'compact') return [ip]; g1 = g2 = 3; }
  var minGroup = mode === 'min2' ? 2 : (mode === 'always' ? 1 : (s.data.n.mgd || 1));
  if (ip.length < g1 + minGroup) return [ip];
  var parts = [], rest = ip, first = true;
  var sz = g1;
  while (rest.length > sz) { parts.unshift(rest.slice(rest.length - sz)); rest = rest.slice(0, rest.length - sz); sz = g2; }
  parts.unshift(rest);
  return parts;
}
function pushNumber(s, parts, dg, pat, symbols, exp) {
  var nu = s.nu, ds = function (str) { return I.localDigits(str, nu); };
  var groups = groupInteger(s, dg.i, pat, symbols);
  groups.forEach(function (gp, k) {
    if (k) parts.push({ type: 'group', value: symbols.g });
    parts.push({ type: 'integer', value: ds(gp) });
  });
  if (dg.f) {
    parts.push({ type: 'decimal', value: symbols.d });
    parts.push({ type: 'fraction', value: ds(dg.f) });
  }
  if (exp !== undefined) {
    parts.push({ type: 'exponentSeparator', value: symbols.e });
    if (exp < 0) parts.push({ type: 'exponentMinusSign', value: symbols.mi });
    parts.push({ type: 'exponentInteger', value: ds(String(Math.abs(exp))) });
  }
}
function currencyText(s) {
  var d = s.data, rec = d.n.cur$ && d.n.cur$[s.currency], code = s.currency;
  switch (s.currencyDisplay) {
    case 'code': return code;
    case 'narrowSymbol': return rec ? (rec[1] || rec[0]) : code;
    case 'name': return rec ? (rec[3] && rec[3].other) || rec[2] : code;
    default: return rec ? rec[0] : code;
  }
}
function currencyName(s, plural) {
  var rec = s.data.n.cur$ && s.data.n.cur$[s.currency];
  if (!rec) return s.currency;
  var cn = rec[3] || {};
  return cn[plural] || cn.other || rec[2];
}
// CLDR currencySpacing: between a currency symbol that does not end in a symbol or separator and a digit
// where a sign goes: inside the unit / currency name text that surrounds the number, so after a leading one
function frontIndex(s, out) {
  var last = -1, i;
  for (i = 0; i < out.length; i++) {
    var p = out[i];
    if (p.type === 'unit' || (p.type === 'currency' && s.currencyDisplay === 'name')) last = i;
    else if (p.type !== 'literal') break;
  }
  i = last + 1;
  while (last >= 0 && i < out.length && out[i].type === 'literal') i++;
  return i;
}
// white space and bidi marks at the edge of a sign, symbol, unit or compact text are literals of their own
var KEEP = { integer: 1, group: 1, decimal: 1, fraction: 1, nan: 1, infinity: 1, exponentInteger: 1 };
var EDGE = /^([\s\u200e\u200f\u061c]*)([\s\S]*?)([\s\u200e\u200f\u061c]*)$/;
function splitEdges(parts) {
  var out = [];
  parts.forEach(function (p) {
    if (p.type === 'literal' || KEEP[p.type]) { out.push(p); return; }
    var m = EDGE.exec(p.value);
    if (!m[2]) { out.push({ type: 'literal', value: p.value }); return; }
    if (m[1]) out.push({ type: 'literal', value: m[1] });
    out.push({ type: p.type, value: m[2] });
    if (m[3]) out.push({ type: 'literal', value: m[3] });
  });
  for (var i = out.length - 1; i > 0; i--) if (out[i].type === 'literal' && out[i - 1].type === 'literal') { out[i - 1].value += out[i].value; out.splice(i, 1); }
  return out;
}
function spaceCurrency(s, out) {
  var cs = s.data.n.cs; if (!cs) return out;
  for (var i = 0; i < out.length; i++) {
    if (out[i].type !== 'currency' || s.currencyDisplay === 'name') continue;
    var v = out[i].value, nx = out[i + 1], pv = out[i - 1];
    var isNum = function (p) { return p && (p.type === 'integer' || p.type === 'fraction' || p.type === 'exponentInteger'); };
    if (isNum(nx) && /[^\p{S}\p{Z}]/u.test(v.slice(-1)) && /[\p{Nd}]/u.test(nx.value.charAt(0))) { out.splice(i + 1, 0, { type: 'literal', value: cs.b || '\u00a0' }); i++; }
    else if (isNum(pv) && /[^\p{S}\p{Z}]/u.test(v.charAt(0)) && /[\p{Nd}]/u.test(pv.value.slice(-1))) { out.splice(i, 0, { type: 'literal', value: cs.a || '\u00a0' }); i++; }
  }
  return out;
}
function isAlpha(ch) { return !!ch && /[\p{L}\p{N}]/u.test(ch); }

// the layout tokens of a format: prefix tokens, the number, suffix tokens (each token already resolved to parts)
// true: the negative subpattern; 'soft' (a plus sign or an approximately sign): only when that subpattern has a minus sign to replace
function wantNeg(neg, pat) { return !!(pat.neg && (neg === true || (neg === 'soft' && pat.neg.toks.some(function (t) { return t.t === 'mi'; })))); }
function negMode(sign, approx) { return sign === 'minus' ? true : (sign === 'plus' || approx) ? 'soft' : false; }
function approxSymbol(s) { return symbolsOf(s).ap || '~'; }
function layout(s, neg, plural, symbols, compactPattern) {
  var d = s.data, n = d.n, signStr = null;
  var sign = neg ? 'minus' : 'plus';
  var tokens;
  if (s.style === 'unit' && s.unit === 'percent' && s.unitDisplay !== 'long') {
    var pp = parsePattern(n.pct), uh = wantNeg(neg, pp) && s.signDisplay !== 'never' ? pp.neg : pp.pos;
    return { toks: uh.toks, info: uh.info, signAt: 'pattern', usedNeg: false };
  }
  if (s.style === 'unit') {
    var tpl = unitTemplate(s, plural);
    var pieces = tpl.split('{0}');
    if (pieces.length === 1) return { toks: [{ t: 'unit', v: tpl }], info: {}, signAt: 'none', noNumber: true };
    tokens = [];
    var nums = { t: 'n' };
    pieces.forEach(function (p, k) { if (p) tokens.push(unitPieces(s, p, tpl)); if (k < pieces.length - 1) tokens.push(nums); });
    tokens = [].concat.apply([], tokens.map(function (t) { return Array.isArray(t) ? t : [t]; }));
    return { toks: tokens, info: parsePattern(n.dec).pos.info, signAt: 'number' };
  }
  var src;
  if (s.style === 'percent') src = n.pct;
  else if (s.style === 'currency') {
    var acc = s.currencySign === 'accounting', txt = currencyText(s);
    src = acc ? n.acc : n.cur;
    if (s.currencyDisplay === 'name') src = n.dec;
    var pat = parsePattern(src), useNeg = wantNeg(neg, pat) && s.signDisplay !== 'never';
    var half = useNeg ? pat.neg : pat.pos;
    var out = [];
    half.toks.forEach(function (t) {
      if (t.t === 'c') out.push({ t: 'cur', v: t.v });
      else out.push(t);
    });
    if (s.currencyDisplay === 'name') {
      var nm = currencyName(s, plural), cup = (n.cup && (n.cup[plural] || n.cup.other)) || '{0} {1}';
      var toks2 = [];
      cup.split(/(\{0\}|\{1\})/).forEach(function (piece) {
        if (piece === '{0}') toks2.push({ t: 'n' });
        else if (piece === '{1}') toks2.push({ t: 'curname', v: nm });
        else if (piece) toks2.push({ t: 'l', v: piece });
      });
      return { toks: toks2, info: half.info, signAt: 'front', usedNeg: false };
    }
    return { toks: out, info: half.info, signAt: useNeg ? 'pattern' : 'front', usedNeg: useNeg };
  }
  else src = n.dec;
  if (compactPattern) src = compactPattern;
  var pat2 = parsePattern(src), useNeg2 = wantNeg(neg, pat2) && s.signDisplay !== 'never';
  var half2 = useNeg2 ? pat2.neg : pat2.pos;
  return { toks: half2.toks, info: half2.info, signAt: useNeg2 ? 'pattern' : 'front', usedNeg: useNeg2 };
}
function unitTemplate(s, plural) {
  var u = s.data.u, w = s.unitDisplay === 'long' ? 'l' : s.unitDisplay === 'narrow' ? 'n' : 's';
  function simple(name, width, pl) {
    var rec = u.s[name]; if (!rec) return null;
    var wr = rec[width] || rec.s || rec.l; if (!wr) return null;
    return wr.p[pl] || wr.p.other || null;
  }
  var unit = s.unit, i = unit.indexOf('-per-');
  if (i < 0) return simple(unit, w, plural) || simple(unit, 's', plural) || simple(unit, 'l', plural) || '{0} ' + unit;
  if (u.s[unit]) { var direct = simple(unit, w, plural); if (direct) return direct; }
  var num = unit.slice(0, i), den = unit.slice(i + 5), numT = simple(num, w, plural) || simple(num, 's', plural) || '{0} ' + num;
  var rec = u.s[den] && (u.s[den][w] || u.s[den].s), perU = rec && rec.u;
  if (perU) return perU.replace('{0}', numT);
  var denWord = (simple(den, w, 'one') || den).replace('{0}', '').trim();
  var per = (u.per && (u.per[w] || u.per.s)) || '{0}/{1}';
  return per.replace('{0}', numT).replace('{1}', denWord);
}
function unitPieces(s, text) { return { t: 'unit', v: text }; }

function resolveSign(s, neg, zero) {
  switch (s.signDisplay) {
    case 'never': return null;
    case 'always': return neg ? 'minus' : 'plus';
    case 'exceptZero': return zero ? null : (neg ? 'minus' : 'plus');
    case 'negative': return neg && !zero ? 'minus' : null;
    default: return neg ? 'minus' : null;
  }
}
function numberParts(s, x, approx) {
  var symbols = symbolsOf(s), d = s.data, n = d.n;
  var neg = !!x.neg, parts = [];
  var special = x.nan ? 'nan' : x.inf ? 'inf' : null;
  var dg = null, exp, plural = 'other', zero = false;
  var y = x;
  if (special) { if (x.nan) neg = false; }
  else {
    if (s.style === 'percent') y = DEC.scale(y, 2 + (s.rangeScale || 0));
    if (s.notation === 'scientific' || s.notation === 'engineering') {
      var e0 = DEC.isZero(y) ? 0 : DEC.mag(y);
      if (s.notation === 'engineering') e0 = Math.floor(e0 / 3) * 3;
      var mant = DEC.scale(y, -e0), rr = roundDigits(s, mant);
      if (!DEC.isZero(rr.x) && DEC.mag(rr.x) >= (s.notation === 'engineering' ? 3 : 1)) { e0 += s.notation === 'engineering' ? 3 : 1; mant = DEC.scale(y, -e0); rr = roundDigits(s, mant); }
      exp = e0; dg = digitStrings(s, rr); zero = DEC.isZero(rr.x);
    } else {
      var rd = roundDigits(s, y);
      dg = digitStrings(s, rd); zero = DEC.isZero(rd.x);
    }
    if (exp !== undefined) { // scientific: the digits shown, moved back by the exponent, with the exponent as the compact exponent
      var si = dg.i, sf = dg.f;
      if (exp > 0) { sf = sf + new Array(Math.max(0, exp - sf.length) + 1).join('0'); si = si + sf.slice(0, exp); sf = sf.slice(exp); }
      else if (exp < 0) { si = new Array(Math.max(0, -exp - si.length) + 1).join('0') + si; sf = si.slice(si.length + exp) + sf; si = si.slice(0, si.length + exp) || '0'; }
      plural = I.plural('cardinal', s.dataTag, I.operands(si.replace(/^0+(?=\d)/, ''), sf, exp));
    }
    else plural = I.plural('cardinal', s.dataTag, I.operands(dg.i, dg.f, 0));
  }
  s.lastPlural = plural; if (s.forcePlural) plural = s.forcePlural;
  var sign = resolveSign(s, neg, zero && !special || (special === 'nan' && s.signDisplay === 'exceptZero'));
  var lay = layout(s, negMode(sign, approx), plural, symbols, null);
  if (lay.noNumber) return [{ type: 'unit', value: lay.toks[0].v }];
  var pat = { info: lay.info || {}, forceGroup: false };
  // sign: pattern-provided negative forms carry their own minus; otherwise a sign goes in front of the first token
  var toks = lay.toks.slice();
  var hasPatternSign = toks.some(function (t) { return t.t === 'mi'; });
  var signPart = null;
  if (sign === 'minus') signPart = { type: 'minusSign', value: symbols.mi };
  else if (sign === 'plus') signPart = { type: 'plusSign', value: symbols.pl };
  var apPart = approx ? { type: 'approximatelySign', value: approxSymbol(s) } : null;
  var signDone = false;
  var out = [];
  var numberAt = toks.findIndex(function (t) { return t.t === 'n'; });
  toks.forEach(function (t, idx) {
    switch (t.t) {
      case 'l': out.push({ type: 'literal', value: t.v }); break;
      case 'mi': if (lay.usedNeg && (signPart || apPart)) { if (apPart) out.push(apPart); if (signPart) out.push(signPart); signDone = true; } else if (sign === 'minus') { out.push({ type: 'minusSign', value: symbols.mi }); signDone = true; } break;
      case 'pl': if (sign === 'plus') { out.push({ type: 'plusSign', value: symbols.pl }); signDone = true; } break;
      case 'p': out.push({ type: s.style === 'unit' ? 'unit' : 'percentSign', value: symbols.p }); break;
      case 'm': out.push({ type: 'literal', value: symbols.pm }); break;
      case 'cur': out.push({ type: 'currency', value: currencyText(s) }); break;
      case 'curname': out.push({ type: 'currency', value: t.v }); break;
      case 'unit': out.push({ type: 'unit', value: t.v }); break;
      case 'n':
        if (special === 'nan') out.push({ type: 'nan', value: symbols.nan });
        else if (special === 'inf') out.push({ type: 'infinity', value: symbols.inf });
        else pushNumber(s, out, dg, pat, symbols, exp);
        break;
    }
  });
  if (!signDone) { var fi = frontIndex(s, out); if (signPart && !(sign === 'minus' && lay.usedNeg)) out.splice(fi, 0, signPart); if (apPart) out.splice(fi, 0, apPart); }
  return splitEdges(spaceCurrency(s, out));
}
// For compact notation the whole layout is built from the compact pattern; handle it in one place.
function formatToPartsInternal(s, v, approx) {
  var x = DEC.from(v);
  if (s.notation === 'compact') return numberPartsCompact(s, x, symbolsOf(s), s.data.n, approx);
  return numberParts(s, x, approx);
}
function numberPartsCompact(s, x, symbols, n, approx) {
  var special = x.nan ? 'nan' : x.inf ? 'inf' : null;
  var y = s.style === 'percent' ? DEC.scale(x, 2) : x;
  var neg = x.nan ? false : !!x.neg;
  var curShort = s.style === 'currency' && s.currencyDisplay !== 'name' && n.cpc;
  var table = curShort ? n.cpc : s.compactDisplay === 'long' ? n.cpd.l : n.cpd.s;
  var res = null, dg = null, pat = null, zero = false, exp = 0;
  if (!special) {
    var keys = Object.keys(table).map(Number).sort(function (a, b) { return a - b; });
    var attempt = function (m) {
      var best = null;
      for (var k = 0; k < keys.length; k++) if (Math.round(Math.log10(keys[k])) <= m) best = keys[k];
      if (best === null) { var r0 = roundDigits(s, y); return { pat: null, r: r0, shift: 0, keyExp: 0 }; }
      var ent = table[best], keyExp = Math.round(Math.log10(best));
      var posOf = function (t) { return t.split(';')[0]; };
      var zeros = (posOf(ent.other).match(/0/g) || []).length, shift = keyExp - (zeros - 1);
      if (/^¤?\s*0+$/.test(posOf(ent.other)) && /^0+$/.test(posOf(ent.other).replace(/[¤\s]/g, ''))) { var r1 = roundDigits(s, y); return { pat: null, r: r1, shift: 0, keyExp: 0 }; }
      var r = roundDigits(s, DEC.scale(y, -shift)), shown = digitStrings(s, r);
      var cat = I.plural('cardinal', s.dataTag, I.operands(shown.i, shown.f, keyExp));
      var p = ent[cat] || ent.other, z2 = (posOf(p).match(/0/g) || []).length;
      if (z2 !== zeros) { shift = keyExp - (z2 - 1); r = roundDigits(s, DEC.scale(y, -shift)); shown = digitStrings(s, r); cat = I.plural('cardinal', s.dataTag, I.operands(shown.i, shown.f, keyExp)); p = ent[cat] || ent.other; }
      return { pat: p, r: r, shift: shift, keyExp: keyExp };
    };
    var m = DEC.isZero(y) ? 0 : DEC.mag(y);
    res = attempt(m);
    var over = res.pat ? DEC.mag(res.r.x) + res.shift > m : DEC.mag(res.r.x) > m;
    if (over && !DEC.isZero(res.r.x)) res = attempt(m + 1);
    dg = digitStrings(s, res.r); pat = res.pat; zero = DEC.isZero(res.r.x); exp = res.pat ? res.keyExp : 0;
  }
  var sign = resolveSign(s, neg, zero && !special || (special === 'nan' && s.signDisplay === 'exceptZero'));
  var plural = special ? 'other' : I.plural('cardinal', s.dataTag, I.operands(dg.i, dg.f, exp));
  s.lastPlural = plural; if (s.forcePlural) plural = s.forcePlural;
  var whole = pat && s.style === 'currency' && s.currencyDisplay !== 'name' && n.cpc;
  var pctUnit = null;
  if (s.style === 'percent') { // compact percent is written with the unit "percent" pattern ("{0} %", "{0} pct."), not the percent format
    var ut = unitTemplate({ data: s.data, unit: 'percent', unitDisplay: 'short' }, plural).split('{0}');
    var ptoks = [];
    if (ut[0]) ptoks.push({ t: 'unit', v: ut[0] });
    ptoks.push({ t: 'n' });
    if (ut[1]) ptoks.push({ t: 'unit', v: ut[1] });
    pctUnit = { toks: ptoks, info: parsePattern(n.dec).pos.info, usedNeg: false };
  }
  var lay = pctUnit ? pctUnit : whole ? { toks: parsePattern(pat).pos.toks, info: parsePattern(n.dec).pos.info, usedNeg: false } : layout(s, negMode(sign, approx), plural, symbols, null);
  var out = [], signDone = false, numSeen = false;
  var signPart = sign === 'minus' ? { type: 'minusSign', value: symbols.mi } : sign === 'plus' ? { type: 'plusSign', value: symbols.pl } : null;
  var apPart = approx ? { type: 'approximatelySign', value: approxSymbol(s) } : null;
  // a compact pattern may carry its own negative form ("elfu -0"), which then places the sign
  var cpat = pat && !whole ? parsePattern(pat) : null, chalf = cpat ? (wantNeg(negMode(sign, approx), cpat) ? cpat.neg : cpat.pos) : null;
  var cSign = !!(chalf && chalf.toks.some(function (t) { return t.t === 'mi'; }));
  lay.toks.forEach(function (t) {
    switch (t.t) {
      case 'l': out.push({ type: whole && /[^\s\u200e\u200f\u061c]/.test(t.v) ? 'compact' : 'literal', value: t.v }); break;
      case 'mi': if (cSign) break; if (lay.usedNeg && (signPart || apPart)) { if (apPart) out.push(apPart); if (signPart) out.push(signPart); signDone = true; } else if (sign === 'minus') { out.push({ type: 'minusSign', value: symbols.mi }); signDone = true; } break;
      case 'pl': if (sign === 'plus') { out.push({ type: 'plusSign', value: symbols.pl }); signDone = true; } break;
      case 'p': out.push({ type: s.style === 'unit' ? 'unit' : 'percentSign', value: symbols.p }); break;
      case 'cur': case 'c': out.push({ type: 'currency', value: currencyText(s) }); break;
      case 'curname': out.push({ type: 'currency', value: t.v }); break;
      case 'unit': out.push({ type: 'unit', value: t.v }); break;
      case 'n':
        numSeen = true;
        if (special === 'nan') out.push({ type: 'nan', value: symbols.nan });
        else if (special === 'inf') out.push({ type: 'infinity', value: symbols.inf });
        else if (pat && !whole) {
          // currency compact patterns carry the symbol: "¤0K"; the symbol is already placed by the layout, so only the
          // number and the text after it are used here
          var cp = chalf, seen = false;
          cp.toks.forEach(function (ct) {
            if (ct.t === 'mi') { if (apPart) out.push(apPart); if (signPart) out.push(signPart); signDone = true; return; }
            if (ct.t === 'n') { pushNumber(s, out, dg, { info: parsePattern(n.dec).pos.info, forceGroup: true }, symbols); seen = true; }
            else if (ct.t === 'l') out.push({ type: /^[\s\u200e\u200f\u061c]+$/.test(ct.v) ? 'literal' : 'compact', value: ct.v });
          });
        }
        else pushNumber(s, out, dg, { info: lay.info && lay.info.group1 ? lay.info : parsePattern(n.dec).pos.info, forceGroup: true }, symbols);
        break;
    }
  });
  if (!signDone) { var fi2 = frontIndex(s, out); if (signPart && !(sign === 'minus' && lay.usedNeg)) out.splice(fi2, 0, signPart); if (apPart) out.splice(fi2, 0, apPart); }
  return splitEdges(spaceCurrency(s, out));
}

// ---- the public class --------------------------------------------------------------------------------------------------------------
function NumberFormat(locales, options) {
  var self = this instanceof NumberFormat ? this : Object.create(NumberFormat.prototype);
  initialize(self, locales, options);
  return self;
}
function joined(parts) { var s = ''; for (var i = 0; i < parts.length; i++) s += parts[i].value; return s; }
H.attrs(NumberFormat.prototype, {
  format: function () {
    var s = slot(this, 'format');
    if (!s.bound) {
      var self = this;
      s.bound = function (v) { return joined(formatToPartsInternal(s, v)); };
      Object.defineProperty(s.bound, 'name', { value: '', configurable: true });
    }
    return s.bound;
  }
});
H.define(NumberFormat.prototype, 'formatToParts', function formatToParts(v) { return formatToPartsInternal(slot(this, 'formatToParts'), v); });
function rangeParts(s, a, b) {
  var a0 = a, x = DEC.from(a), y = DEC.from(b);
  if (x.nan || y.nan) throw new RangeError('Invalid number');
  var pa = formatToPartsInternal(s, a), la = s.lastPlural, pb = formatToPartsInternal(s, b), lb = s.lastPlural;
  if (!(x.inf || y.inf)) { // both ends are written with the plural form of the range
    s.forcePlural = I.pluralRange(s.dataTag, la, lb);
    try { pa = formatToPartsInternal(s, a); pb = formatToPartsInternal(s, b); } finally { s.forcePlural = null; s.rangeScale = 0; }
  }
  var sa = joined(pa), sb = joined(pb), mp = s.data.n.mp || { ap: '~{0}', r: '{0}–{1}' };
  var out = [];
  function tag(parts, src) { return parts.map(function (p) { return { type: p.type, value: p.value, source: src }; }); }
  if (sa === sb) {
    // V8 scales a percent twice when an equal-looking range in scientific notation is shown with an approximately sign
    if (s.style === 'percent' && (s.notation === 'scientific' || s.notation === 'engineering')) s.rangeScale = 2;
    try { return tag(formatToPartsInternal(s, a0, true), 'shared'); } finally { s.rangeScale = 0; }
  }
  var rt = mp.r || '{0}–{1}', i0 = rt.indexOf('{0}'), i1 = rt.indexOf('{1}'), mid = rt.slice(i0 + 3, i1);
  // ICU's range collapsing: each formatted number is split into the outer modifier (unit / currency name), the middle one (sign, currency
  // symbol, percent, compact text) and the number itself; modifiers both ends share are written once around the range. The middle one is
  // only collapsed when it is longer than one code point, and the number's own exponent never is.
  var A = splitMods(pa, s), B = splitMods(pb, s);
  var cO = sameMod(A.op, B.op) && sameMod(A.os, B.os), cM = false;
  if (cO) cM = sameMod(A.mp, B.mp) && sameMod(A.ms, B.ms) && modLen(A.mp) + modLen(A.ms) > 1;
  var repeat = A.exp || B.exp || (!cO && (A.op.length + A.os.length) > 0) || (!cM && (modLen(A.mp) + modLen(A.ms)) > 0);
  var sepText = mid;
  if (repeat) {
    var last = A.core.length ? A.core[A.core.length - 1].value : '';
    if (!/\s$/.test(last) && !/^\s/.test(sepText)) sepText = ' ' + sepText;
    if (!/^\s/.test(B.core.length ? B.core[0].value : '') && !/\s$/.test(sepText)) sepText += ' ';
  }
  var res = [];
  function shared(ps) { ps.forEach(function (p) { res.push({ type: p.type, value: p.value, source: 'shared' }); }); }
  if (cO) shared(A.op);
  if (cM) shared(A.mp);
  if (!cO) res = res.concat(tag(A.op, 'startRange'));
  if (!cM) res = res.concat(tag(A.mp, 'startRange'));
  res = res.concat(tag(A.core, 'startRange'));
  if (!cM) res = res.concat(tag(A.ms, 'startRange'));
  if (!cO) res = res.concat(tag(A.os, 'startRange'));
  res.push({ type: 'literal', value: sepText, source: 'shared' });
  if (!cO) res = res.concat(tag(B.op, 'endRange'));
  if (!cM) res = res.concat(tag(B.mp, 'endRange'));
  res = res.concat(tag(B.core, 'endRange'));
  if (!cM) res = res.concat(tag(B.ms, 'endRange'));
  if (!cO) res = res.concat(tag(B.os, 'endRange'));
  if (cM) shared(A.ms);
  if (cO) shared(A.os);
  return res;
}
var CORE = { integer: 1, group: 1, decimal: 1, fraction: 1, nan: 1, infinity: 1, exponentSeparator: 1, exponentMinusSign: 1, exponentInteger: 1 };
function splitMods(parts, s) { // {op, mp, core, exp, ms, os}: outer/middle prefix, the number, outer/middle suffix
  var f = -1, l = -1, i;
  for (i = 0; i < parts.length; i++) if (CORE[parts[i].type]) { if (f < 0) f = i; l = i; }
  if (f < 0) return { op: [], mp: parts.slice(), core: [], exp: false, ms: [], os: [] };
  var pre = parts.slice(0, f), suf = parts.slice(l + 1), exp = false;
  for (i = f; i <= l; i++) if (parts[i].type === 'exponentSeparator') exp = true;
  var isOuter = function (p) { return (p.type === 'unit' && !(s.style === 'unit' && s.unit === 'percent')) || (p.type === 'currency' && s.style === 'currency' && s.currencyDisplay === 'name'); };
  var op = [], mp = pre, os = [], ms = suf, u;
  for (u = pre.length - 1; u >= 0 && !isOuter(pre[u]); u--);
  if (u >= 0) { u++; while (u < pre.length && pre[u].type === 'literal') u++; op = pre.slice(0, u); mp = pre.slice(u); }
  for (u = 0; u < suf.length && !isOuter(suf[u]); u++);
  if (u < suf.length) { while (u > 0 && suf[u - 1].type === 'literal') u--; ms = suf.slice(0, u); os = suf.slice(u); }
  return { op: op, mp: mp, core: parts.slice(f, l + 1), exp: exp, ms: ms, os: os };
}
function sameMod(x, y) { return x.length === y.length && x.every(function (p, i) { return p.type === y[i].type && p.value === y[i].value; }); }
function modLen(m) { var n = 0; m.forEach(function (p) { n += Array.from(p.value).length; }); return n; }
H.define(NumberFormat.prototype, 'formatRange', function formatRange(a, b) {
  var s = slot(this, 'formatRange');
  if (arguments.length < 2 || a === undefined || b === undefined) throw new TypeError('start or end is undefined');
  return joined(rangeParts(s, a, b));
});
H.define(NumberFormat.prototype, 'formatRangeToParts', function formatRangeToParts(a, b) {
  var s = slot(this, 'formatRangeToParts');
  if (arguments.length < 2 || a === undefined || b === undefined) throw new TypeError('start or end is undefined');
  return rangeParts(s, a, b);
});
H.define(NumberFormat.prototype, 'resolvedOptions', function resolvedOptions() {
  var s = slot(this, 'resolvedOptions'), o = {};
  o.locale = s.locale; o.numberingSystem = s.nu; o.style = s.style;
  if (s.style === 'currency') { o.currency = s.currency; o.currencyDisplay = s.currencyDisplay; o.currencySign = s.signDisplay === 'never' ? 'standard' : s.currencySign; }
  if (s.style === 'unit') { o.unit = s.unit; o.unitDisplay = s.unitDisplay; }
  o.minimumIntegerDigits = s.minInt;
  if (s.minFD !== undefined) { o.minimumFractionDigits = s.minFD; o.maximumFractionDigits = s.maxFD; }
  if (s.minSD !== undefined) { o.minimumSignificantDigits = s.minSD; o.maximumSignificantDigits = s.maxSD; }
  o.useGrouping = s.useGrouping; o.notation = s.notation;
  if (s.notation === 'compact') o.compactDisplay = s.compactDisplay;
  o.signDisplay = s.signDisplay; o.roundingIncrement = s.roundingIncrement; o.roundingMode = s.roundingMode;
  o.roundingPriority = s.trailingZeroDisplay === 'stripIfInteger' ? 'auto' : s.roundingPriority; /* V8 reports auto whenever trailing zeros are stripped */ o.trailingZeroDisplay = s.trailingZeroDisplay;
  return o;
});
Object.defineProperty(NumberFormat.prototype, Symbol.toStringTag, { value: 'Intl.NumberFormat', configurable: true });
H.define(NumberFormat, 'supportedLocalesOf', function supportedLocalesOf(locales, options) { return I.supportedLocalesOf(locales, options); });
Object.defineProperty(NumberFormat, 'length', { value: 0, configurable: true });
H.define(g.Intl, 'NumberFormat', NumberFormat);
