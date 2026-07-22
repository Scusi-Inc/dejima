#!/usr/bin/env node
// Thin shim: exec the dejima binary that install.js placed in ../binary/,
// forwarding argv, stdio, and the exit code. Lets `npm install -g dejima`
// expose a real `dejima` command on PATH.
'use strict';

const fs = require('fs');
const path = require('path');
const { spawnSync } = require('child_process');

const exe = process.platform === 'win32' ? 'dejima.exe' : 'dejima';
const binary = process.env.DEJIMA_BINARY || path.join(__dirname, '..', 'binary', exe);

if (!fs.existsSync(binary)) {
  console.error(`dejima: binary not found at ${binary}`);
  console.error('The postinstall download may have failed. Reinstall the package, or');
  console.error('set DEJIMA_BINARY to the path of a dejima binary you provide yourself.');
  process.exit(1);
}

const result = spawnSync(binary, process.argv.slice(2), { stdio: 'inherit' });
if (result.error) {
  console.error(`dejima: ${result.error.message}`);
  process.exit(1);
}
process.exit(result.status === null ? 1 : result.status);
