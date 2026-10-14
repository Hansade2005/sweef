// Measures the break dictionaries of the ICU build inside this Node binary (icudt77l.dat): the cost, in bytes, of word segmentation
// for Thai, Lao, Khmer, Burmese and Chinese/Japanese, which Sweef does not ship (docs/compat.md). No data is extracted or written.
//   node icudict.js
'use strict';
const fs = require('fs'), zlib = require('zlib');
const buf = fs.readFileSync(process.execPath);
const needle = Buffer.from('brkitr/thaidict.dict');
let at = buf.indexOf(needle);
if (at < 0) { console.log('no brkitr/thaidict.dict in', process.execPath); process.exit(1); }
// the table of contents of a common data file: a header (magic da 27), then count, then (nameOffset, dataOffset) pairs
let hdr = -1;
for (let i = at; i > at - 4_000_000 && i > 0; i--) if (buf[i] === 0xda && buf[i + 1] === 0x27 && buf.toString('latin1', i + 10, i + 14) === 'CmnD') { hdr = i - 2; break; }
if (hdr < 0) { console.log('no common data header before the dictionary names'); process.exit(1); }
const headerSize = buf.readUInt16LE(hdr), toc = hdr + headerSize, count = buf.readUInt32LE(toc);
const entries = [];
for (let i = 0; i < count; i++) {
  const no = buf.readUInt32LE(toc + 4 + i * 8), doff = buf.readUInt32LE(toc + 8 + i * 8);
  let e = toc + no; while (buf[e]) e++;
  entries.push({ name: buf.toString('latin1', toc + no, e), off: toc + doff });
}
const end = (i) => (i + 1 < count ? entries[i + 1].off : buf.length);
for (let i = 0; i < count; i++) {
  if (!/brkitr\/.*\.(dict|brk)$/.test(entries[i].name) || !/dict$/.test(entries[i].name)) continue;
  const data = buf.subarray(entries[i].off, end(i));
  console.log(entries[i].name.padEnd(28), String(data.length).padStart(8), 'B raw', String(zlib.gzipSync(data, { level: 9 }).length).padStart(8), 'B gzip');
}
