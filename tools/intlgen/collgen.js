// Collation tables for Sweef's Intl.Collator, written to <data>/coll.json.gz. ICU's root collation puts scripts in an order and
// the letters of each script in an order that code point order does not give (Arabic, Hebrew, the Indic scripts, Thai, Greek,
// Cyrillic, kana, symbols and punctuation), so the order is taken from ICU itself (Node's Intl.Collator("und")):
//   g: the base characters of those scripts in collation order. "\u0001" starts a new secondary difference inside one primary
//      weight, "\u0002" a tertiary one, "\n" a new primary weight.
//   m: marks that carry no primary weight (vowel points, tone marks, ...) in their secondary order.
//   z / p / s: spaces, punctuation and symbols in order, in the same encoding as g.
//   a: the locales that have collation data of their own (a Collator resolves to the longest of these that fits the request).
//   zh: with --han, the pinyin order of the common Han characters (CLDR "zh").
// Usage (called from gen.js): require('./collgen')(dataDir)
'use strict';
const fs = require('fs'), path = require('path'), zlib = require('zlib');

const root = new Intl.Collator('und', { sensitivity: 'variant' }), base = new Intl.Collator('und', { sensitivity: 'base' }), accent = new Intl.Collator('und', { sensitivity: 'accent' });
const SEC = '\u0001', TER = '\u0002';
const KANA_MARKS = /[\u3005\u3031-\u3035\u303b\u309d\u309e\u30fc-\u30fe]/u; // modifier letters of the kana block that sort with the symbols

// the blocks of scripts whose order is not code point order, as [first, last]
const LETTER_BLOCKS = [
  [0x370, 0x3ff], [0x400, 0x52f], [0x531, 0x58f], [0x5d0, 0x5ff], [0x600, 0x6ff], [0x750, 0x77f], [0x780, 0x7bf], [0x8a0, 0x8ff],
  [0x900, 0xdff], [0xe00, 0xeff], [0xf00, 0xfff], [0x1000, 0x109f], [0x10a0, 0x10ff], [0x1780, 0x17ff], [0x1c80, 0x1c8f], [0x1f00, 0x1fff], [0x2c00, 0x2c5f], [0x2d00, 0x2d2f],
  [0x3040, 0x30ff], [0x31f0, 0x31ff], [0x1100, 0x11ff], [0x3130, 0x318f],
];
const SYMBOL_BLOCKS = [[0x3040, 0x30ff], [0x9, 0xd], [0x20, 0x7e], [0x85, 0x85], [0xa0, 0xbf], [0xd7, 0xd7], [0xf7, 0xf7], [0x2000, 0x2bff], [0x3000, 0x303f], [0xfe10, 0xfe6f], [0xff01, 0xff65]];

const assigned = (cp) => /\P{Cn}/u.test(String.fromCodePoint(cp));
function chars(blocks, test, keepDecomposed) {
  const out = [];
  for (const [a, b] of blocks) for (let cp = a; cp <= b; cp++) {
    if (cp >= 0xd800 && cp <= 0xdfff) continue;
    const ch = String.fromCodePoint(cp);
    if (!assigned(cp) || !test(ch)) continue;
    const d = ch.normalize('NFKD');
    if (d !== ch) { // decomposed by the collator before it looks the character up, unless ICU gives it weights of its own (Arabic hamza forms, ...)
      if (ch.normalize('NFD') !== d || !keepDecomposed || base.compare(ch, String.fromCodePoint(d.codePointAt(0))) === 0) continue; // (a letter and a mark that ICU keeps at one primary weight)
    }
    out.push(ch);
  }
  return out;
}
// the characters in order, as one string: primary groups separated by "\n", secondary and tertiary steps marked
function encode(list) {
  list.sort((a, b) => root.compare(a, b) || (a < b ? -1 : 1));
  let out = '';
  list.forEach((ch, i) => {
    if (i) {
      const prev = list[i - 1];
      if (base.compare(prev, ch) !== 0) out += '\n';
      else if (accent.compare(prev, ch) !== 0) out += SEC;
      else if (root.compare(prev, ch) !== 0) out += TER;
      else out += TER; // (identical at every level: kept in code point order)
    }
    out += ch;
  });
  return out;
}

// the common Han characters in the order of one of CLDR's Chinese collations: GB 2312 (simplified, pinyin) or the 5,401 characters of
// Big5 level 1 (traditional, stroke)
function han(tag, encoding, ranges) {
  const dec = new TextDecoder(encoding), list = [];
  for (const [hi0, hi1, lo0, lo1] of ranges) for (let hi = hi0; hi <= hi1; hi++) for (let lo = lo0; lo <= lo1; lo++) {
    if (encoding === 'big5' && lo > 0x7e && lo < 0xa1) continue;
    const ch = dec.decode(new Uint8Array([hi, lo]));
    if (ch.length === 1 && ch !== '\ufffd' && /\p{Script=Han}/u.test(ch)) list.push(ch);
  }
  const full = new Intl.Collator(tag, { sensitivity: 'variant' }), primary = new Intl.Collator(tag, { sensitivity: 'base' });
  list.sort((a, b) => full.compare(a, b) || (a < b ? -1 : 1));
  let out = '';
  list.forEach((ch, i) => {
    if (i) out += primary.compare(list[i - 1], ch) !== 0 ? '\n' : full.compare(list[i - 1], ch) !== 0 ? SEC : TER;
    out += ch;
  });
  return out;
}

function available() {
  // the tags ICU has collation data for: a region or script variant that resolves to itself
  const regions = [], dn = new Intl.DisplayNames('en', { type: 'region' });
  for (let a = 65; a <= 90; a++) for (let b = 65; b <= 90; b++) { const r = String.fromCharCode(a, b); if (dn.of(r) !== r) regions.push(r); }
  const langs = ['af', 'ar', 'az', 'bg', 'bn', 'bs', 'ca', 'cs', 'da', 'de', 'el', 'en', 'es', 'et', 'fa', 'fi', 'fil', 'fr', 'gu', 'he', 'hi', 'hr', 'hu', 'id', 'it', 'ja', 'kk', 'km', 'kn', 'ko', 'lt', 'lv', 'ml', 'mr', 'ms', 'my', 'nb', 'ne', 'nl', 'pa', 'pl', 'pt', 'ro', 'ru', 'si', 'sk', 'sl', 'sr', 'sv', 'sw', 'ta', 'te', 'th', 'tr', 'uk', 'ur', 'uz', 'vi', 'zh', 'ps', 'no', 'nn'];
  const scripts = ['Latn', 'Cyrl', 'Arab', 'Guru', 'Hans', 'Hant'];
  const out = [];
  const resolved = (t) => { try { return new Intl.Collator(t).resolvedOptions().locale; } catch (e) { return ''; } };
  for (const l of langs) {
    for (const s of scripts) { const t = l + '-' + s; if (resolved(t) === t) out.push(t); for (const r of regions) { const t2 = l + '-' + s + '-' + r; if (resolved(t2) === t2) out.push(t2); } }
    for (const r of regions) { const t = l + '-' + r; if (resolved(t) === t) out.push(t); }
  }
  return out;
}

module.exports = function (dataDir, opts) {
  opts = opts || {};
  const data = {};
  const letters = chars(LETTER_BLOCKS, (c) => /[\p{L}\p{M}]/u.test(c) && !KANA_MARKS.test(c) && base.compare(c, '') !== 0, true);
  data.g = encode(letters.slice());
  // the Arabic-script locales order the Arabic letters differently from the root (hamza forms, Persian and Urdu letters)
  data.ta = {};
  for (const l of ['ar', 'fa', 'ur']) {
    const col = new Intl.Collator(l, { sensitivity: 'variant' }), colBase = new Intl.Collator(l, { sensitivity: 'base' }), colAcc = new Intl.Collator(l, { sensitivity: 'accent' });
    const list = letters.filter((c) => /\p{Script=Arabic}/u.test(c)).sort((a, b) => col.compare(a, b) || (a < b ? -1 : 1));
    let out = '';
    list.forEach((ch, i) => { if (i) out += colBase.compare(list[i - 1], ch) !== 0 ? '\n' : colAcc.compare(list[i - 1], ch) !== 0 ? SEC : TER; out += ch; });
    data.ta[l] = out;
  }
  // marks without a primary weight, in secondary order (compared after a letter)
  const marks = chars([[0x300, 0x36f], [0x483, 0x489], [0x591, 0x5c7], [0x610, 0x61a], [0x64b, 0x65f], [0x670, 0x670], [0x6d6, 0x6ed], [0x900, 0x903], [0x93a, 0x94f], [0x951, 0x957], [0x962, 0x963], [0x981, 0x983], [0x9bc, 0x9d7], [0xa01, 0xa03], [0xa3c, 0xa51], [0xa81, 0xa83], [0xabc, 0xacd], [0xb01, 0xb03], [0xb3c, 0xb57], [0xb82, 0xb82], [0xbbe, 0xbcd], [0xc00, 0xc04], [0xc3c, 0xc56], [0xc81, 0xc83], [0xcbc, 0xcd6], [0xd00, 0xd03], [0xd3b, 0xd4d], [0xd81, 0xd83], [0xdca, 0xddf], [0xe31, 0xe31], [0xe34, 0xe3a], [0xe47, 0xe4e], [0xeb1, 0xeb1], [0xeb4, 0xebc], [0xec8, 0xecd], [0x1037, 0x1037], [0x103a, 0x103a], [0x17c6, 0x17d3], [0x3099, 0x309a]], (c) => /\p{M}/u.test(c) && base.compare(c, '') === 0);
  marks.sort((a, b) => accent.compare('a' + a, 'a' + b) || (a < b ? -1 : 1));
  data.m = marks.join('');
  data.z = encode(chars(SYMBOL_BLOCKS, (c) => /[\p{Z}\t-\r\u0085]/u.test(c)));
  data.p = encode(chars(SYMBOL_BLOCKS, (c) => /\p{P}/u.test(c)));
  data.s = encode(chars(SYMBOL_BLOCKS, (c) => /\p{S}/u.test(c) || KANA_MARKS.test(c)));
  data.a = available().join(' ');
  if (opts.han) { data.zh = han('zh', 'gb2312', [[0xb0, 0xf7, 0xa1, 0xfe]]); data.zt = han('zh-Hant', 'big5', [[0xa4, 0xc6, 0x40, 0xfe]]); }
  const json = JSON.stringify(data);
  const gz = zlib.gzipSync(Buffer.from(json), { level: 9 });
  fs.writeFileSync(path.join(dataDir, 'coll.json.gz'), gz);
  return [json.length, gz.length, Object.fromEntries(Object.entries(data).map(([k, v]) => [k, Buffer.byteLength(typeof v === 'string' ? v : JSON.stringify(v))]))];
};
if (require.main === module) {
  const r = module.exports(process.argv[2] || '.', { han: process.argv.includes('--han') });
  console.log('coll', r[0], 'raw', r[1], 'gz', JSON.stringify(r[2]));
}
