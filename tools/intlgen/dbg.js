// dbg.js: evaluates an expression with Sweef's Intl modules loaded (H, I, ours available)  e.g. node dbg.js "I.G().hc['IN']"
'use strict';
const h = require('./harness');
const ours = h.make(['intlcore', 'intlnum', 'intldate', 'intlmisc']);
const H = h.H, I = H.intl, native = h.NativeIntl;
const r = eval(process.argv[2]);
console.log(typeof r === 'string' ? r : JSON.stringify(r, null, 1));
