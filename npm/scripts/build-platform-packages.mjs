#!/usr/bin/env node
// Build the per-platform npm packages (@dejima/cli-<platform>-<arch>) that carry
// the prebuilt dejima binary, plus stamp the main `dejima` package + its
// optionalDependencies to the release version. Run by the release workflow after
// the GitHub Release exists; runnable locally against a `dist/` of archives.
//
// Usage:
//   node npm/scripts/build-platform-packages.mjs <version> <distDir> [outDir]
//
//   <version>  release version, with or without a leading v (e.g. 0.7.2 or v0.7.2)
//   <distDir>  dir holding dejima_v<version>_<os>_<arch>.{tar.gz,zip} + SHA256SUMS
//   [outDir]   where to scaffold platform packages (default: npm/platforms)
//
// It extracts + CHECKSUM-VERIFIES each binary (the integrity guard the old
// postinstall did at install time, moved to publish time), writes each platform
// package, and rewrites npm/package.json's version + optionalDependencies so they
// pin the exact release version. Prints the list of package dirs (one per line)
// so the workflow can publish each, then the main package.
'use strict';

import { execFileSync } from 'node:child_process';
import crypto from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const npmDir = path.resolve(__dirname, '..'); // the npm/ package root

// node platform/arch suffix → release archive (os/arch) + archive shape. Keep in
// lockstep with the Makefile's release-binaries matrix and bin/dejima.js's
// `@dejima/cli-${process.platform}-${process.arch}` resolution.
const TARGETS = [
  { pkg: 'darwin-arm64', os: 'darwin', arch: 'arm64', rel: 'darwin_arm64', ext: 'tar.gz', exe: 'dejima' },
  { pkg: 'darwin-x64', os: 'darwin', arch: 'x64', rel: 'darwin_amd64', ext: 'tar.gz', exe: 'dejima' },
  { pkg: 'linux-arm64', os: 'linux', arch: 'arm64', rel: 'linux_arm64', ext: 'tar.gz', exe: 'dejima' },
  { pkg: 'linux-x64', os: 'linux', arch: 'x64', rel: 'linux_amd64', ext: 'tar.gz', exe: 'dejima' },
  { pkg: 'win32-arm64', os: 'win32', arch: 'arm64', rel: 'windows_arm64', ext: 'zip', exe: 'dejima.exe' },
  { pkg: 'win32-x64', os: 'win32', arch: 'x64', rel: 'windows_amd64', ext: 'zip', exe: 'dejima.exe' },
];

function die(msg) {
  console.error(`build-platform-packages: ${msg}`);
  process.exit(1);
}

const [, , rawVersion, rawDist, rawOut] = process.argv;
if (!rawVersion || !rawDist) {
  die('usage: build-platform-packages.mjs <version> <distDir> [outDir]');
}
const version = String(rawVersion).replace(/^v/, '');
const tag = `v${version}`;
const distDir = path.resolve(rawDist);
const outDir = path.resolve(rawOut || path.join(npmDir, 'platforms'));

if (!fs.existsSync(distDir)) die(`distDir not found: ${distDir}`);

// Parse SHA256SUMS once so every binary is integrity-checked before we ship it.
const sumsPath = path.join(distDir, 'SHA256SUMS');
const sums = new Map();
if (fs.existsSync(sumsPath)) {
  for (const line of fs.readFileSync(sumsPath, 'utf8').split('\n')) {
    const m = line.trim().split(/\s+/);
    if (m.length === 2) sums.set(m[1], m[0]);
  }
} else {
  console.warn('build-platform-packages: SHA256SUMS not found — skipping checksum verification.');
}

fs.rmSync(outDir, { recursive: true, force: true });
fs.mkdirSync(outDir, { recursive: true });

const built = [];
for (const t of TARGETS) {
  const asset = `dejima_${tag}_${t.rel}.${t.ext}`;
  const archive = path.join(distDir, asset);
  if (!fs.existsSync(archive)) die(`missing release archive: ${archive}`);

  // Verify the archive against the release checksums before unpacking it.
  if (sums.size) {
    const want = sums.get(asset);
    if (!want) die(`${asset} not listed in SHA256SUMS`);
    const got = crypto.createHash('sha256').update(fs.readFileSync(archive)).digest('hex');
    if (got !== want) die(`checksum mismatch for ${asset}\n  want ${want}\n  got  ${got}`);
  }

  const pkgDir = path.join(outDir, t.pkg);
  const binDir = path.join(pkgDir, 'bin');
  fs.mkdirSync(binDir, { recursive: true });

  // Extract just the CLI binary out of the archive. We run in CI on Linux, where
  // GNU tar handles .tar.gz but NOT .zip — so use `unzip` for the Windows
  // archives. (The archives also carry dejimad/LICENSE/README; we copy out only
  // the one binary below.)
  const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'dejima-pkg-'));
  try {
    if (t.ext === 'zip') {
      execFileSync('unzip', ['-o', '-q', archive, t.exe, '-d', tmp], { stdio: 'inherit' });
    } else {
      execFileSync('tar', ['-xzf', archive, '-C', tmp], { stdio: 'inherit' });
    }
    const src = path.join(tmp, t.exe);
    if (!fs.existsSync(src)) die(`${t.exe} not found after extracting ${asset}`);
    const dst = path.join(binDir, t.exe);
    fs.copyFileSync(src, dst);
    if (t.os !== 'win32') fs.chmodSync(dst, 0o755);
  } finally {
    fs.rmSync(tmp, { recursive: true, force: true });
  }

  // The platform package: os/cpu constrain it so npm installs ONLY the matching
  // one (and treats the others as skipped, not failed). No scripts, no deps.
  const pkgJson = {
    name: `@dejima/cli-${t.pkg}`,
    version,
    description: `Dejima CLI binary for ${t.os}-${t.arch}.`,
    license: 'Apache-2.0',
    os: [t.os],
    cpu: [t.arch],
    files: [`bin/${t.exe}`],
    homepage: 'https://dejima.tech/',
    repository: { type: 'git', url: 'git+https://github.com/aoos/dejima.git', directory: 'npm' },
  };
  fs.writeFileSync(path.join(pkgDir, 'package.json'), JSON.stringify(pkgJson, null, 2) + '\n');
  fs.writeFileSync(
    path.join(pkgDir, 'README.md'),
    `# @dejima/cli-${t.pkg}\n\nPlatform binary for [\`dejima\`](https://www.npmjs.com/package/dejima) ` +
      `(${t.os}-${t.arch}). Installed automatically as an optional dependency of \`dejima\`; ` +
      `you don't install this directly.\n`
  );
  built.push(pkgDir);
}

// Stamp the main package: version + every optionalDependency pinned EXACTLY to
// this release, so a fresh `npm i dejima` pulls the matching platform binary.
const mainPath = path.join(npmDir, 'package.json');
const main = JSON.parse(fs.readFileSync(mainPath, 'utf8'));
main.version = version;
for (const t of TARGETS) main.optionalDependencies[`@dejima/cli-${t.pkg}`] = version;
fs.writeFileSync(mainPath, JSON.stringify(main, null, 2) + '\n');

// One package dir per line on stdout (for the workflow); human notes on stderr.
console.error(`build-platform-packages: built ${built.length} platform packages for ${tag}`);
for (const d of built) console.log(d);
