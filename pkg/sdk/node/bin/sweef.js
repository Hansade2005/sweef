#!/usr/bin/env node
'use strict';
// SWEEF_SDK_LAUNCHER: `npm i -g @sweef/browser` puts this `sweef` on PATH. It resolves the engine
// (PATH, SWEEF_BIN, cache, automatic download) and runs it with stdio inherited.
const { spawnSync } = require('child_process');
const { ensureBinary } = require('../install.js');

ensureBinary().then((exe) => {
  const r = spawnSync(exe, process.argv.slice(2), { stdio: 'inherit' });
  if (r.error) { process.stderr.write(`sweef: ${r.error.message}\n`); process.exit(127); }
  if (r.signal) process.kill(process.pid, r.signal);
  process.exit(r.status === null ? 1 : r.status);
}, (e) => { process.stderr.write(`sweef: ${e.message}\n`); process.exit(1); });
