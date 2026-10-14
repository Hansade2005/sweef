// nd.js <locale> '<options json>' <ms...>: Node's own output (and parts) for quick experiments
const loc = process.argv[2], o = JSON.parse(process.argv[3] || '{}'), vals = process.argv.slice(4).map(Number);
const esc = (s) => String(s).replace(/[^\x20-\x7e]/g, (c) => '\\u' + c.charCodeAt(0).toString(16).padStart(4, '0'));
const f = new Intl.DateTimeFormat(loc, o);
console.log(JSON.stringify(f.resolvedOptions()));
for (const v of vals.length ? vals : [Date.UTC(2025, 9, 14, 15, 4, 5, 678)]) {
  console.log(esc(f.format(v)));
  if (process.env.PARTS) console.log(esc(JSON.stringify(f.formatToParts(v))));
}
