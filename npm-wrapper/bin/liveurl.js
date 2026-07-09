#!/usr/bin/env node
// Thin shim: exec the real liveurl binary that install.js downloaded into
// vendor/, forwarding argv, stdio, and the process exit code.
'use strict';

const path = require('path');
const { spawnSync } = require('child_process');

const binName = process.platform === 'win32' ? 'liveurl.exe' : 'liveurl';
const binPath = path.join(__dirname, '..', 'vendor', binName);

const result = spawnSync(binPath, process.argv.slice(2), { stdio: 'inherit' });

if (result.error) {
  console.error(`liveurl: failed to run ${binPath}: ${result.error.message}`);
  console.error('Try reinstalling: npm install -g liveurl');
  process.exit(1);
}
process.exit(result.status === null ? 1 : result.status);
