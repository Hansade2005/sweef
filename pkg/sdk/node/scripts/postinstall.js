'use strict';
// Best-effort engine download at `npm install`. Never fails the install; skipped with SWEEF_SKIP_DOWNLOAD
// (and not run at all under `npm --ignore-scripts`). The engine is also fetched lazily on first use.
(async () => {
  try {
    const sk = process.env.SWEEF_SKIP_DOWNLOAD;
    if (sk && sk !== '0' && sk.toLowerCase() !== 'false') return;
    const { lookupBinary, downloadEngine } = require('../install.js');
    if (lookupBinary()) return;
    const t = setTimeout(() => process.exit(0), 180000);
    t.unref();
    await downloadEngine();
  } catch (e) {
    process.stderr.write(`sweef: engine not downloaded at install time (${e.message}); it will be fetched on first use, or run sweef-install.\n`);
  }
  process.exit(0);
})();
