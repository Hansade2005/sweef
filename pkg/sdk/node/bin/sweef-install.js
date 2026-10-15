#!/usr/bin/env node
'use strict';
// SWEEF_SDK_LAUNCHER: `npx -p @sweef/browser sweef-install [--print-path]` downloads the engine ahead
// of time (a no-op when it is already available) and prints where it is.
const { ensureBinary, cachedBinaryPath, engineVersion } = require('../install.js');

const args = process.argv.slice(2);
if (args.includes('-h') || args.includes('--help')) {
  process.stdout.write('usage: sweef-install [--print-path]\n  Ensures the sweef engine is installed (PATH, SWEEF_BIN or the per-user cache, else downloads it) and prints its path.\n  --print-path  print the path only; never download (exit 1 when absent)\n');
  process.exit(0);
}
if (args.includes('--print-path')) {
  const { lookupBinary } = require('../install.js');
  const p = lookupBinary();
  if (!p) { process.stderr.write(`sweef: engine ${engineVersion()} is not installed (would be ${cachedBinaryPath()}); run sweef-install\n`); process.exit(1); }
  process.stdout.write(p + '\n');
  process.exit(0);
}
ensureBinary().then((p) => { process.stdout.write(p + '\n'); }, (e) => { process.stderr.write(`sweef: ${e.message}\n`); process.exit(1); });
