// one.js <ctor> <locale> '<options json>' <value...>: shows ours and Node's output with escapes.
'use strict';
const { make, NativeIntl } = require('./harness');
const [ctor, loc, opts, ...vals] = process.argv.slice(2);
const ours = make(['intlcore', 'intlnum', 'intldate', 'intlmisc'].filter((m) => require('fs').existsSync(require('path').join(__dirname, '..', '..', 'internal', 'engine', 'platformjs', m + '.js'))));
const esc = (s) => String(s).replace(/[^\x20-\x7e]/g, (c) => '\\u' + c.charCodeAt(0).toString(16).padStart(4, '0'));
const o = opts ? JSON.parse(opts) : undefined;
for (const v of (vals.length ? vals : [undefined])) {
  const val = v === undefined ? undefined : (isNaN(Number(v)) ? v : Number(v));
  let a, b;
  const call = (I) => { const f = new I[ctor](loc, o); return f.format(val); };
  try { a = call(ours); } catch (e) { a = 'THROW ' + e.message; }
  try { b = call(NativeIntl); } catch (e) { b = 'THROW ' + e.message; }
  console.log(String(v), '\n  ours:', esc(a), '\n  node:', esc(b), a === b ? ' (same)' : ' (DIFF)');
}
