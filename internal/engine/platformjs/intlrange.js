// Intl.DateTimeFormat formatRange / formatRangeToParts: a port of ICU's DateIntervalFormat (what V8 calls). The skeleton of the
// formatter's pattern is matched against the locale's intervalFormats; the largest calendar field that differs between the two
// dates picks the interval pattern ("MMM d – d, y"), and without one the two dates are written in full with the fallback pattern
// ("{0} – {1}"). Published as H.range; platformjs/intldate.js passes in the pieces of its formatter through H.dt.
var D = H.dt, P = H.pat;
var ERA = 0, YEAR = 1, MONTH = 2, DATE = 3, AM_PM = 4, HOUR = 5, MINUTE = 6, SECOND = 7, MILLI = 8;
var FIELD_LETTER = ['G', 'y', 'M', 'd', 'a', 'h', 'm', 's', 'S'];
var FIELD_LEVEL = [0, 10, 20, 30, 40, 50, 60, 70, 80];
var CHAR_LEVEL = { G: 0, y: 10, Y: 10, u: 10, U: 10, r: 10, Q: 20, q: 20, M: 20, L: 20, l: 20, w: 20, W: 30, D: 20, d: 30, F: 30, E: 30, e: 30, c: 30, a: 40, h: 50, H: 50, K: 50, k: 50, m: 60, s: 70, S: 80, A: 80, z: 0, Z: 0, v: 0, V: 0, O: 0, x: 0, X: 0 };
var isLetter = function (c) { return /[A-Za-z]/.test(c); };
var rep = function (c, n) { return new Array(n + 1).join(c); };

function widths(sk) { var w = {}, i; for (i = 0; i < sk.length; i++) w[sk[i]] = (w[sk[i]] || 0) + 1; return w; }

// DateIntervalFormat::getDateTimeSkeleton: the date and time halves of a skeleton, and their normalized forms (one d, one h, one m, ...)
function splitSkeleton(sk) {
  var date = '', ndate = '', time = '', ntime = '', c = { E: 0, d: 0, M: 0, y: 0, h: 0, H: 0, m: 0, z: 0, v: 0 }, i, ch;
  for (i = 0; i < sk.length; i++) {
    ch = sk[i];
    switch (ch) {
      case 'E': date += ch; c.E++; break;
      case 'd': date += ch; c.d++; break;
      case 'M': date += ch; c.M++; break;
      case 'y': date += ch; c.y++; break;
      case 'G': case 'Y': case 'u': case 'U': case 'Q': case 'q': case 'L': case 'l': case 'w': case 'W': case 'D': case 'F': case 'g': case 'c': case 'e':
        ndate += ch; date += ch; break;
      case 'a': time += ch; break; // (left out of the normalized form)
      case 'h': case 'K': time += ch; c.h++; break;
      case 'H': case 'k': time += ch; c.H++; break;
      case 'm': time += ch; c.m++; break;
      case 'z': time += ch; c.z++; break;
      case 'v': time += ch; c.v++; break;
      case 'V': case 'Z': case 'j': case 'J': case 's': case 'S': case 'A': case 'B': time += ch; ntime += ch; break;
    }
  }
  if (c.y) ndate += rep('y', c.y);
  if (c.M) ndate += c.M < 3 ? 'M' : rep('M', Math.min(c.M, 5));
  if (c.E) ndate += c.E <= 3 ? 'E' : rep('E', Math.min(c.E, 5));
  if (c.d) ndate += 'd';
  if (c.H) ntime += 'H'; else if (c.h) ntime += 'h';
  if (c.m) ntime += 'm';
  if (c.z) ntime += 'z';
  if (c.v) ntime += 'v';
  return { date: date, ndate: ndate, time: time, ntime: ntime };
}

// DateIntervalInfo::getBestSkeleton: the interval skeleton closest to the request, and how it differs (0 same, 1 widths, 2 only v/z,
// -1 other fields)
function bestSkeleton(iv, input) {
  var inW = widths(input), replaced = false, best = null, bestD = Infinity, info = 0, key;
  ['z', 'V'].forEach(function (z) { if (inW[z]) { inW.v = (inW.v || 0) + inW[z]; delete inW[z]; replaced = true; } });
  for (key in iv) {
    if (key === '_') continue;
    var kw = widths(key), d = 0, diff = 1, all = {}, ch;
    for (ch in inW) all[ch] = 1;
    for (ch in kw) all[ch] = 1;
    for (ch in all) {
      var a = inW[ch] || 0, b = kw[ch] || 0;
      if (a === b) continue;
      if (a === 0 || b === 0) { diff = -1; d += 0x1000; }
      else if (ch === 'M' && ((a <= 2 && b >= 3) || (a >= 3 && b <= 2))) d += 0x100;
      else d += Math.abs(a - b);
    }
    if (d < bestD) { bestD = d; best = key; info = diff; }
    if (d === 0) { info = 0; break; }
  }
  if (best === null) return null;
  if (replaced && (info === 0 || info === 1)) info = 2;
  return { key: best, info: info };
}
// the interval pattern of a skeleton for a calendar field ('' when there is none)
function intervalPattern(iv, key, field) {
  var rec = iv[key];
  if (!rec) return '';
  switch (field) {
    case ERA: return rec.G || '';
    case YEAR: return rec.y || '';
    case MONTH: return rec.M || '';
    case DATE: return rec.d || '';
    case AM_PM: return rec.a || rec.B || '';
    case HOUR: return rec.h || rec.H || '';
    case MINUTE: return rec.m || '';
  }
  return '';
}
function adjustFieldWidth(input, best, pattern) {
  var iw = widths(input), bw = widths(best), s = pattern, inQuote = false, prev = '', count = 0, i, ch;
  var fix = function (at) {
    var sc = prev === 'L' ? 'M' : prev, fc = bw[sc] || 0, ic = iw[sc] || 0;
    if (fc === count && ic > fc) { var add = ic - fc; s = s.slice(0, at) + rep(prev, add) + s.slice(at); return add; }
    return 0;
  };
  for (i = 0; i < s.length; ++i) {
    ch = s.charAt(i);
    if (ch !== prev && count > 0) { i += fix(i); count = 0; }
    if (ch === "'") { if (i + 1 < s.length && s.charAt(i + 1) === "'") ++i; else inQuote = !inQuote; }
    else if (!inQuote && isLetter(ch)) { if (ch === prev) ++count; else { prev = ch; count = 1; } }
  }
  if (count > 0) fix(s.length);
  return s;
}
// DateIntervalFormat::splitPatternInto2Part: where the first repeated field starts
function splitAt(p) {
  var inQuote = false, prev = '', count = 0, seen = {}, found = false, i, ch;
  for (i = 0; i < p.length; ++i) {
    ch = p.charAt(i);
    if (ch !== prev && count > 0) { if (!seen[prev]) seen[prev] = true; else { found = true; break; } count = 0; }
    if (ch === "'") { if (i + 1 < p.length && p.charAt(i + 1) === "'") ++i; else inQuote = !inQuote; }
    else if (!inQuote && isLetter(ch)) { prev = ch; ++count; }
  }
  if (count > 0 && !found && !seen[prev]) count = 0;
  return i - count;
}
function info(pattern, later) { // {first, second, later}
  if (!pattern) return null;
  var l = later, m = /^(latestFirst|earliestFirst):/.exec(pattern);
  if (m) { pattern = pattern.slice(m[0].length); l = m[1] === 'latestFirst'; }
  var at = splitAt(pattern);
  return { first: pattern.slice(0, at), second: pattern.slice(at), later: l };
}
// SimpleDateFormat::isFieldUnitIgnored: the pattern has nothing as fine as the field that differs
function ignored(pattern, field) {
  var fl = FIELD_LEVEL[field], inQuote = false, prev = '', count = 0, i, ch;
  for (i = 0; i < pattern.length; ++i) {
    ch = pattern.charAt(i);
    if (ch !== prev && count > 0) { if (fl <= (CHAR_LEVEL[prev] === undefined ? -1 : CHAR_LEVEL[prev])) return false; count = 0; }
    if (ch === "'") { if (i + 1 < pattern.length && pattern.charAt(i + 1) === "'") ++i; else inQuote = !inQuote; }
    else if (!inQuote && isLetter(ch)) { prev = ch; ++count; }
  }
  if (count > 0 && fl <= (CHAR_LEVEL[prev] === undefined ? -1 : CHAR_LEVEL[prev])) return false;
  return true;
}

// ---- setting up the patterns of a formatter ----------------------------------------------------------------------------------------------
function plan(s) {
  if (s.itv) return s.itv;
  var cd = D.calData(s), gen = D.generator(s), iv = cd.iv || {}, tokens = s.tokens;
  var skeleton = P.skeletonOf(P.str(tokens)), sp = splitSkeleton(skeleton);
  var fallback = iv._ || '{0} – {1}';
  var pl = { iv: iv, fallback: fallback, laterFirst: fallback.indexOf('{1}') >= 0 && fallback.indexOf('{1}') < fallback.indexOf('{0}'), fields: [], date: '', time: '', glue: '' };
  var hc = /K/.test(sp.time) ? 'h11' : /k/.test(sp.time) ? 'h24' : null; // the hour letter the skeleton asks for
  var best1 = function (sk) { var p = gen.best(sk, true); return hc && p ? P.replaceHourPattern(p, hc) : p; };
  pl.hc = hc;
  pl.pattern = best1(skeleton) || P.str(tokens);
  var hasDate = !!sp.date, hasTime = !!sp.time;
  if (hasDate && hasTime) {
    pl.date = best1(sp.ndate); pl.time = best1(sp.ntime); pl.dateRaw = best1(sp.date); // (the normalized ones write the dates and times of a fallback; the date next to an interval is the requested one)
    pl.glue = (cd.dtf && cd.dtf.medium) || (cd.dtfAt && cd.dtfAt.medium) || '{1} {0}'; // (the general date-time pattern is the medium one)
  }
  var f = pl.fields, later = pl.laterFirst;
  var setIv = function (field, sk, best, extend) { // DateIntervalFormat::setIntervalPattern; returns the (maybe extended) skeleton pair
    var pat = intervalPattern(iv, best.key, field), asked = sk, bk = best;
    if (!pat) {
      if (ignored(best.key, field)) return null;
      if (field === AM_PM) pat = intervalPattern(iv, best.key, HOUR);
      else if (extend && (field === MONTH || field === YEAR) && sk.indexOf(FIELD_LETTER[field]) < 0 && sk.indexOf('d') >= 0) {
        asked = FIELD_LETTER[field] + sk;
        var eb = bestSkeleton(iv, asked);
        if (eb && eb.info !== -1) { bk = eb; pat = intervalPattern(iv, eb.key, field); }
      }
      if (!pat) return null;
    }
    if (bk.info !== 0) pat = adjustFieldWidth(asked, bk.key, pat);
    if (bk.info === 2 && /z/.test(asked)) pat = pat.replace(/v/g, 'z'); // (a time zone asked for as z stands where the data has v)
    if (pl.hc && /[hH]/.test(pat)) pat = P.replaceHourPattern(pat, pl.hc);
    f[field] = info(pat, later);
    return asked === sk ? null : { sk: asked, best: bk };
  };
  var fallbackFor = function (field, sk) { var p = best1(sk); if (p) f[field] = { first: '', second: p, later: later }; };
  var skel = hasTime ? sp.ntime : sp.ndate, best = skel ? bestSkeleton(iv, skel) : null;
  if (best && best.info !== -1) { // found
    if (!hasTime) {
      setIv(ERA, skel, best, false);
      setIv(DATE, skel, best, false);
      var ext = setIv(MONTH, skel, best, true);
      if (ext) { skel = ext.sk; best = ext.best; }
      setIv(YEAR, skel, best, true);
    } else {
      setIv(AM_PM, skel, best, false); setIv(HOUR, skel, best, false); setIv(MINUTE, skel, best, false);
      if (!hasDate) { // a date next to the time when the dates differ
        var full = best1('yMd' + sp.time);
        [DATE, MONTH, YEAR].forEach(function (fd) { f[fd] = { first: '', second: full, later: later }; });
      } else {
        var sk2 = skeleton;
        if (sp.date.indexOf('d') < 0) { sk2 = 'd' + sk2; fallbackFor(DATE, sk2); }
        if (sp.date.indexOf('M') < 0) { sk2 = 'M' + sk2; fallbackFor(MONTH, sk2); }
        if (sp.date.indexOf('y') < 0) { sk2 = 'y' + sk2; fallbackFor(YEAR, sk2); }
        [AM_PM, HOUR, MINUTE].forEach(function (fd) { // the date once, then the time interval
          var t = f[fd];
          if (!t || !t.first) return;
          var combined = pl.glue.replace(/\{([01])\}/g, function (m, d) { return d === '0' ? t.first + t.second : pl.dateRaw; });
          f[fd] = info(combined, later);
        });
      }
    }
  } else if (hasTime && !hasDate) {
    var full2 = best1('yMd' + sp.time);
    [DATE, MONTH, YEAR].forEach(function (fd) { f[fd] = { first: '', second: full2, later: later }; });
  }
  return (s.itv = pl);
}

// ---- formatting ----------------------------------------------------------------------------------------------------------------------------
function differ(fa, fb) {
  if (fa.era !== fb.era) return ERA;
  if (fa.y !== fb.y) return YEAR;
  if (fa.m !== fb.m) return MONTH;
  if (fa.d !== fb.d) return DATE;
  if ((fa.h >= 12) !== (fb.h >= 12)) return AM_PM;
  if (fa.h % 12 !== fb.h % 12) return HOUR;
  if (fa.mi !== fb.mi) return MINUTE;
  if (fa.s !== fb.s) return SECOND;
  if (fa.ms !== fb.ms) return MILLI;
  return -1;
}
function part(parts, origin) { return parts.map(function (p) { return { type: p.type, value: p.value, o: origin }; }); }
function lit(text) { return text ? [{ type: 'literal', value: text, o: null }] : []; }
function render(s, pattern, f, origin) { return part(D.renderTokens(s, P.parse(pattern), f), origin); }

function rangeParts(s, a, b) {
  var ta = D.timeValue(a), tb = D.timeValue(b), fa = D.fieldsOf(ta, s.timeZone, s.calendar), fb = D.fieldsOf(tb, s.timeZone, s.calendar);
  var pl = plan(s), field = differ(fa, fb), out;
  if (field < 0) return D.single(s, fa);
  else {
    var ip = pl.fields[field], sameDay = field >= AM_PM;
    if (!ip || (!ip.first && !ip.second)) {
      if (ignored(pl.pattern, field)) return D.single(s, fa);
      out = fallback(s, pl, fa, fb, sameDay, pl.pattern);
    } else if (!ip.first) out = fallback(s, pl, fa, fb, sameDay, ip.second);
    else {
      var fc = ip.later ? fb : fa, sc = ip.later ? fa : fb, fo = ip.later ? 'e' : 's', so = ip.later ? 's' : 'e';
      out = render(s, ip.first, fc, fo);
      if (ip.second) out = out.concat(render(s, ip.second, sc, so));
    }
  }
  return merge(label(out));
}
function merge(parts) { // neighbouring literals with one source are one part
  var out = [];
  parts.forEach(function (p) { var l = out[out.length - 1]; if (l && l.type === 'literal' && p.type === 'literal' && l.source === p.source) l.value += p.value; else out.push(p); });
  return out;
}
// DateIntervalFormat::fallbackFormat: both dates in full, or the date once and the two times when the dates are on one day
function fallback(s, pl, fa, fb, sameDay, pattern) {
  var range = function (pat) {
    var out = [];
    pl.fallback.split(/(\{0\}|\{1\})/).forEach(function (piece) {
      if (piece === '{0}') out = out.concat(render(s, pat, fa, 's'));
      else if (piece === '{1}') out = out.concat(render(s, pat, fb, 'e'));
      else if (piece) out = out.concat(lit(P.parse(piece).map(function (t) { return t.lit || ''; }).join('')));
    });
    return out;
  };
  if (!(sameDay && pl.date && pl.time)) return range(pattern);
  var out = [];
  pl.glue.split(/(\{0\}|\{1\})/).forEach(function (piece) {
    if (piece === '{0}') out = out.concat(range(pl.time));
    else if (piece === '{1}') out = out.concat(render(s, pl.date, fa, null));
    else if (piece) out = out.concat(lit(P.parse(piece).map(function (t) { return t.lit || ''; }).join('')));
  });
  return out;
}
// FormattedValueFieldPositionIteratorImpl::addOverlapSpans: the first field that appears twice starts the second span; both spans hold the
// same number of fields, and what lies outside them is shared
function label(parts) {
  var fields = [], i, j, k;
  parts.forEach(function (p, idx) { if (p.type !== 'literal') fields.push(idx); });
  var a0 = -1, b0 = -1;
  outer: for (i = 0; i < fields.length; i++) for (j = i + 1; j < fields.length; j++) if (parts[fields[i]].type === parts[fields[j]].type) { a0 = i; b0 = j; break outer; }
  var res = parts.map(function (p) { return { type: p.type, value: p.value, source: 'shared' }; });
  if (a0 < 0) return res;
  var kinds = {}, e = b0;
  for (k = a0; k < b0; k++) kinds[parts[fields[k]].type] = 1;
  for (k = b0; k < fields.length; k++) if (kinds[parts[fields[k]].type]) e = k; // the second span ends with its last repeated field
  var aFrom = fields[a0], aTo = fields[b0 - 1], bFrom = fields[b0], bTo = fields[e];
  var src = function (o) { return o === 'e' ? 'endRange' : 'startRange'; };
  for (k = aFrom; k <= aTo; k++) res[k].source = src(parts[aFrom].o);
  for (k = bFrom; k <= bTo; k++) res[k].source = src(parts[bFrom].o);
  return res;
}
H.range = { parts: rangeParts };
