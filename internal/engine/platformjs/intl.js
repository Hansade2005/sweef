// Intl.Locale: BCP 47 parsing, canonical casing, Unicode extension keywords, maximize/minimize
// (likely-subtags for the major languages).
var LIKELY = {
  en: 'Latn-US', fr: 'Latn-FR', de: 'Latn-DE', es: 'Latn-ES', it: 'Latn-IT', pt: 'Latn-BR', nl: 'Latn-NL', sv: 'Latn-SE', da: 'Latn-DK', nb: 'Latn-NO', no: 'Latn-NO', fi: 'Latn-FI',
  pl: 'Latn-PL', cs: 'Latn-CZ', sk: 'Latn-SK', hu: 'Latn-HU', ro: 'Latn-RO', tr: 'Latn-TR', id: 'Latn-ID', vi: 'Latn-VN', ms: 'Latn-MY', ca: 'Latn-ES', hr: 'Latn-HR',
  ru: 'Cyrl-RU', uk: 'Cyrl-UA', bg: 'Cyrl-BG', sr: 'Cyrl-RS', el: 'Grek-GR', he: 'Hebr-IL', ar: 'Arab-EG', fa: 'Arab-IR', ur: 'Arab-PK', hi: 'Deva-IN', bn: 'Beng-BD',
  th: 'Thai-TH', ja: 'Jpan-JP', ko: 'Kore-KR', zh: 'Hans-CN', ta: 'Taml-IN', te: 'Telu-IN'
};
var ZH = { TW: 'Hant', HK: 'Hant', MO: 'Hant' };
var $ = Symbol('locale');
var KEY_FOR_OPTION = { calendar: 'ca', collation: 'co', hourCycle: 'hc', caseFirst: 'kf', numeric: 'kn', numberingSystem: 'nu' };

function bad() { return new RangeError('Incorrect locale information provided'); }
function parseTag(tag) {
  var parts = tag.split(/[-_]/), i = 0, o = { variants: [], keywords: {}, attrs: [], other: '' };
  if (!parts[0] || !/^([a-zA-Z]{2,3}|[a-zA-Z]{5,8})$/.test(parts[0])) throw bad();
  o.language = parts[i++].toLowerCase();
  if (parts[i] && /^[a-zA-Z]{4}$/.test(parts[i])) { o.script = parts[i].charAt(0).toUpperCase() + parts[i].slice(1).toLowerCase(); i++; }
  if (parts[i] && /^([a-zA-Z]{2}|[0-9]{3})$/.test(parts[i])) { o.region = parts[i].toUpperCase(); i++; }
  while (parts[i] && /^([a-zA-Z0-9]{5,8}|[0-9][a-zA-Z0-9]{3})$/.test(parts[i])) o.variants.push(parts[i++].toLowerCase());
  var other = [];
  while (i < parts.length) {
    var s = parts[i].toLowerCase();
    if (s === 'u') {
      i++;
      var key = null;
      while (i < parts.length && parts[i].length > 1) {
        var p = parts[i].toLowerCase();
        if (/^[a-z0-9][a-z]$/.test(p)) { key = p; if (!(key in o.keywords)) o.keywords[key] = []; }
        else if (key) o.keywords[key].push(p);
        else o.attrs.push(p);
        i++;
      }
    } else if (s.length === 1 && /[a-wyz0-9]/.test(s) || s === 'x') {
      var ext = [s]; i++;
      while (i < parts.length && parts[i].length > 1) ext.push(parts[i++].toLowerCase());
      if (ext.length < 2) throw bad();
      other.push(ext.join('-'));
    } else throw bad();
  }
  o.other = other.sort().join('-');
  Object.keys(o.keywords).forEach(function (k) { o.keywords[k] = o.keywords[k].join('-'); if (o.keywords[k] === 'true') o.keywords[k] = ''; });
  return o;
}
function baseNameOf(o) { return [o.language, o.script, o.region].concat(o.variants).filter(Boolean).join('-'); }
function fullTag(o) {
  var t = baseNameOf(o), keys = Object.keys(o.keywords).sort();
  if (keys.length || o.attrs.length) t += '-u' + o.attrs.map(function (a) { return '-' + a; }).join('') + keys.map(function (k) { return '-' + k + (o.keywords[k] ? '-' + o.keywords[k] : ''); }).join('');
  if (o.other) t += '-' + o.other;
  return t;
}
function likely(lang, script, region) {
  var l = LIKELY[lang];
  if (!l) return { script: script, region: region };
  var ds = l.split('-')[0], dr = l.split('-')[1];
  if (lang === 'zh') { ds = script || ZH[region] || 'Hans'; dr = region || (ds === 'Hant' ? 'TW' : 'CN'); }
  return { script: script || ds, region: region || dr };
}

function Locale(tag, options) {
  if (!new.target) throw new TypeError("Constructor Intl.Locale requires 'new'");
  if (typeof tag !== 'string' && !(tag && typeof tag === 'object')) throw new TypeError('First argument to Intl.Locale constructor can\'t be empty or missing');
  var o = parseTag(tag && tag[$] ? fullTag(tag[$]) : String(tag));
  if (options !== undefined) {
    if (options === null) throw new TypeError('Cannot convert null to object');
    options = Object(options);
    if (options.language !== undefined) { if (!/^([a-zA-Z]{2,3}|[a-zA-Z]{5,8})$/.test(String(options.language))) throw bad(); o.language = String(options.language).toLowerCase(); }
    if (options.script !== undefined) { if (!/^[a-zA-Z]{4}$/.test(String(options.script))) throw bad(); var sc = String(options.script); o.script = sc.charAt(0).toUpperCase() + sc.slice(1).toLowerCase(); }
    if (options.region !== undefined) { if (!/^([a-zA-Z]{2}|[0-9]{3})$/.test(String(options.region))) throw bad(); o.region = String(options.region).toUpperCase(); }
    Object.keys(KEY_FOR_OPTION).forEach(function (name) {
      if (options[name] === undefined) return;
      var v = name === 'numeric' ? (options[name] ? '' : 'false') : String(options[name]).toLowerCase();
      if (name !== 'numeric' && !/^[a-z0-9]{3,8}(-[a-z0-9]{3,8})*$/.test(v)) throw bad();
      o.keywords[KEY_FOR_OPTION[name]] = v;
    });
    if (options.hourCycle !== undefined && ['h11', 'h12', 'h23', 'h24'].indexOf(String(options.hourCycle)) < 0) throw new RangeError('Value ' + options.hourCycle + ' out of range for Intl.Locale options property hourCycle');
  }
  Object.defineProperty(this, $, { value: o });
}
function slot(x) { var s = x !== null && typeof x === 'object' ? x[$] : undefined; if (!s) throw new TypeError('Illegal invocation'); return s; }
function kw(name) { return function () { var v = slot(this).keywords[KEY_FOR_OPTION[name]]; return v === undefined ? undefined : v; }; }
H.attrs(Locale.prototype, {
  baseName: function () { return baseNameOf(slot(this)); },
  language: function () { return slot(this).language; },
  script: function () { return slot(this).script; },
  region: function () { return slot(this).region; },
  calendar: kw('calendar'),
  collation: kw('collation'),
  hourCycle: kw('hourCycle'),
  caseFirst: kw('caseFirst'),
  numberingSystem: kw('numberingSystem'),
  numeric: function () { var v = slot(this).keywords.kn; return v === '' || v === 'true'; }
});
function clone(o, patch) { var c = JSON.parse(JSON.stringify(o)); for (var k in patch) c[k] = patch[k]; return new Locale(fullTag(c)); }
H.define(Locale.prototype, 'toString', function toString() { return fullTag(slot(this)); });
H.define(Locale.prototype, 'maximize', function maximize() {
  var o = slot(this), l = likely(o.language, o.script, o.region);
  return clone(o, { script: l.script, region: l.region });
});
H.define(Locale.prototype, 'minimize', function minimize() {
  var o = slot(this), full = likely(o.language, o.script, o.region);
  function same(s, r) { var l = likely(o.language, s, r); return l.script === full.script && l.region === full.region; }
  if (same(undefined, undefined)) return clone(o, { script: undefined, region: undefined });
  if (same(undefined, full.region)) return clone(o, { script: undefined, region: o.region || full.region });
  if (same(full.script, undefined)) return clone(o, { script: o.script || full.script, region: undefined });
  return clone(o, {});
});
Object.defineProperty(Locale.prototype, Symbol.toStringTag, { value: 'Intl.Locale', configurable: true });
H.define(g.Intl, 'Locale', Locale);
