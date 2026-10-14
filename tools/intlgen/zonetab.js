// zonetab.js <locale> [zones...]: Node's zone names next to the metazone records of the data, to study the ICU rules
'use strict';
const h = require('./harness');
h.make(['intlcore', 'intlnum', 'intldate']);
const H = h.H, I = H.intl, G = I.G();
const loc = process.argv[2] || 'en';
const zones = process.argv.length > 3 ? process.argv.slice(3) : ['America/New_York', 'America/Chicago', 'America/Toronto', 'America/Vancouver', 'Asia/Kathmandu', 'Asia/Kolkata', 'Asia/Tokyo', 'Europe/Paris', 'Europe/London', 'Europe/Dublin', 'Europe/Lisbon', 'Europe/Moscow', 'Europe/Istanbul', 'Australia/Perth', 'Australia/Sydney', 'Pacific/Auckland', 'Asia/Shanghai', 'Asia/Hong_Kong', 'Asia/Dubai', 'Asia/Singapore', 'America/Sao_Paulo', 'America/Argentina/Buenos_Aires', 'America/Mexico_City', 'Africa/Lagos', 'Africa/Cairo', 'Africa/Johannesburg', 'Africa/Nairobi', 'Pacific/Honolulu', 'America/Anchorage', 'America/Phoenix', 'America/Denver', 'America/Los_Angeles', 'Atlantic/Reykjavik', 'America/St_Johns', 'Asia/Karachi', 'Asia/Dhaka', 'Asia/Bangkok', 'Asia/Seoul', 'Asia/Jerusalem', 'Asia/Tehran', 'America/Caracas', 'Europe/Kiev', 'Europe/Kyiv', 'UTC', 'Etc/GMT+5', 'Antarctica/Troll'];
const D = [Date.UTC(2025, 0, 15, 12), Date.UTC(2025, 6, 15, 12)];
const esc = (s) => String(s).replace(/[^\x20-\x7e]/g, (c) => '\\u' + c.charCodeAt(0).toString(16).padStart(4, '0'));
const data = I.localeData(loc);
for (const z of zones) {
  let row = z.padEnd(34);
  for (const d of D) {
    const out = [];
    for (const o of ['short', 'long', 'shortGeneric', 'longGeneric']) {
      try { out.push(new Intl.DateTimeFormat(loc, { timeZone: z, timeZoneName: o, hour: 'numeric' }).formatToParts(d).find((p) => p.type === 'timeZoneName').value); } catch (e) { out.push('ERR'); }
    }
    row += ' | ' + esc(out.join(' ; '));
  }
  const mz = G.mz[z] || G.mz[G.tzAlias[z]];
  console.log(row, ' mz=', mz && mz.map((e) => e[0]).join(','));
}
