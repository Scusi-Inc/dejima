#!/usr/bin/env node
// Launcher for the `dejima` CLI. The real binary ships INSIDE a per-platform
// package (@dejima/cli-<platform>-<arch>) declared as an optionalDependency, so
// npm installs only the one matching this host (os/cpu fields) and skips the
// rest. There is NO postinstall download — that was blocked by npm 11's default
// script-blocking and left the CLI non-functional. This shim resolves the
// platform package and execs its binary, forwarding argv, stdio, and exit code.
//
// DEJIMA_BINARY overrides the resolved path (offline installs / CI / a binary
// you provide yourself, e.g. when installed with --no-optional).
'use strict';

const fs = require('fs');
const path = require('path');
const { spawnSync } = require('child_process');

// node's process.platform is already darwin|linux|win32; process.arch is
// x64|arm64 — the exact suffixes our platform packages use.
const pkgName = `@dejima/cli-${process.platform}-${process.arch}`;
const exe = process.platform === 'win32' ? 'dejima.exe' : 'dejima';

function resolveBinary() {
  if (process.env.DEJIMA_BINARY) {
    return process.env.DEJIMA_BINARY;
  }
  // Resolve the package's own package.json (always present + resolvable) and
  // join the binary path, rather than require.resolve-ing the extensionless
  // binary directly — more robust across node versions and bundlers. esbuild,
  // swc and turbo resolve their platform binaries the same way.
  try {
    const pkgJson = require.resolve(`${pkgName}/package.json`);
    return path.join(path.dirname(pkgJson), 'bin', exe);
  } catch (_) {
    return null;
  }
}

const binary = resolveBinary();
if (!binary || !fs.existsSync(binary)) {
  console.error(`dejima: no prebuilt binary for ${process.platform}-${process.arch}.`);
  console.error(`Expected the optional dependency ${pkgName} to be installed.`);
  console.error('This usually means the platform is unsupported, or the package was');
  console.error('installed with --no-optional / --omit=optional. Alternatives:');
  console.error('  • curl -fsSL https://dejima.tech/install-client.sh | bash');
  console.error('  • brew install aoos/dejima/dejima');
  console.error('  • set DEJIMA_BINARY to the path of a dejima binary you provide.');
  process.exit(1);
}

const result = spawnSync(binary, process.argv.slice(2), { stdio: 'inherit' });
if (result.error) {
  console.error(`dejima: ${result.error.message}`);
  process.exit(1);
}
process.exit(result.status === null ? 1 : result.status);
