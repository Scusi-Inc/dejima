#!/usr/bin/env node
// Postinstall hook for the `dejima` npm package.
//
// Downloads the dejima CLI binary that matches this package's version from the
// GitHub Release, verifies its SHA256 against the release's SHA256SUMS, and
// unpacks it into ./binary/. The bin shim (bin/dejima.js) execs it.
//
// This package ships only the CLI *client* — the cross-platform binary you
// drive a Dejima host with. The daemon (dejimad) and the island image are
// Unix-host-only; install those with `curl -fsSL https://dejima.tech/install.sh
// | bash` or Homebrew. See README.md.
//
// Knobs:
//   DEJIMA_SKIP_DOWNLOAD=1   skip the download (e.g. offline CI); provide your
//                            own binary via DEJIMA_BINARY at runtime instead.
'use strict';

const fs = require('fs');
const path = require('path');
const https = require('https');
const crypto = require('crypto');
const { execFileSync } = require('child_process');

const REPO = 'aoos/dejima';
const pkg = require('./package.json');
const version = String(pkg.version).replace(/^v/, '');
const tag = `v${version}`;

if (process.env.DEJIMA_SKIP_DOWNLOAD === '1') {
  console.log('dejima: DEJIMA_SKIP_DOWNLOAD=1 set — skipping binary download.');
  process.exit(0);
}

function mapPlatform() {
  switch (process.platform) {
    case 'darwin': return 'darwin';
    case 'linux': return 'linux';
    case 'win32': return 'windows';
    default: throw new Error(`unsupported platform: ${process.platform}`);
  }
}
function mapArch() {
  switch (process.arch) {
    case 'x64': return 'amd64';
    case 'arm64': return 'arm64';
    default: throw new Error(`unsupported arch: ${process.arch}`);
  }
}

const plat = mapPlatform();
const arch = mapArch();
const ext = plat === 'windows' ? 'zip' : 'tar.gz';
const asset = `dejima_${tag}_${plat}_${arch}.${ext}`;
const base = `https://github.com/${REPO}/releases/download/${tag}`;

// GET that follows redirects (GitHub release assets 302 to a CDN) and buffers
// the body. Caps redirects so a misconfigured mirror can't loop forever.
function get(url, redirects = 0) {
  return new Promise((resolve, reject) => {
    if (redirects > 10) return reject(new Error('too many redirects'));
    https
      .get(url, { headers: { 'User-Agent': 'dejima-npm-installer' } }, (res) => {
        const { statusCode, headers } = res;
        if (statusCode >= 300 && statusCode < 400 && headers.location) {
          res.resume();
          resolve(get(headers.location, redirects + 1));
          return;
        }
        if (statusCode !== 200) {
          res.resume();
          reject(new Error(`GET ${url} -> HTTP ${statusCode}`));
          return;
        }
        const chunks = [];
        res.on('data', (c) => chunks.push(c));
        res.on('end', () => resolve(Buffer.concat(chunks)));
      })
      .on('error', reject);
  });
}

async function verifyChecksum(tarball) {
  let sums;
  try {
    sums = (await get(`${base}/SHA256SUMS`)).toString('utf8');
  } catch (e) {
    console.warn(`dejima: could not fetch SHA256SUMS (${e.message}) — skipping checksum.`);
    return;
  }
  const row = sums
    .split('\n')
    .map((l) => l.trim().split(/\s+/))
    .find((p) => p[1] === asset);
  if (!row) {
    console.warn(`dejima: ${asset} not listed in SHA256SUMS — skipping checksum.`);
    return;
  }
  const got = crypto.createHash('sha256').update(tarball).digest('hex');
  if (got !== row[0]) {
    throw new Error(`checksum mismatch for ${asset}\n  want ${row[0]}\n  got  ${got}`);
  }
  console.log('dejima: checksum OK');
}

async function main() {
  const binDir = path.join(__dirname, 'binary');
  fs.mkdirSync(binDir, { recursive: true });

  console.log(`dejima: downloading ${asset} …`);
  let tarball;
  try {
    tarball = await get(`${base}/${asset}`);
  } catch (e) {
    // A 404 here almost always means there's no published Release for this exact
    // version/platform yet (or the tag isn't out). Say so plainly instead of
    // surfacing a raw "HTTP 404" the way a generic failure would.
    if (/HTTP 404/.test(e.message)) {
      throw new Error(
        `no prebuilt binary for this version/platform yet — ${asset} isn't in ` +
          `release ${tag}.\n  See https://github.com/${REPO}/releases for what's published.`
      );
    }
    throw e;
  }
  await verifyChecksum(tarball);

  // Unpack with the system tar. bsdtar (Windows 10+ `tar.exe`) reads .zip too;
  // gnutar/bsdtar read .tar.gz on Unix. Archives carry dejima (+ dejimad on
  // Unix, which we leave unused) and LICENSE/README.
  const archivePath = path.join(binDir, asset);
  fs.writeFileSync(archivePath, tarball);
  const flags = ext === 'tar.gz' ? ['-xzf'] : ['-xf'];
  execFileSync('tar', [...flags, archivePath, '-C', binDir], { stdio: 'inherit' });
  fs.unlinkSync(archivePath);

  const exe = plat === 'windows' ? 'dejima.exe' : 'dejima';
  const exePath = path.join(binDir, exe);
  if (!fs.existsSync(exePath)) {
    throw new Error(`expected ${exe} in ${asset}, but it was not found after extraction`);
  }
  if (plat !== 'windows') fs.chmodSync(exePath, 0o755);

  // Drop the extras the archive carries (dejimad, LICENSE, README) — this
  // package is the CLI client only, so keep just the one binary.
  for (const f of fs.readdirSync(binDir)) {
    if (f !== exe) fs.rmSync(path.join(binDir, f), { force: true });
  }

  // Strip the macOS quarantine xattr — binaries are unsigned until notarization
  // lands, so Gatekeeper would otherwise block the downloaded executable.
  if (plat === 'darwin') {
    try {
      execFileSync('xattr', ['-d', 'com.apple.quarantine', exePath], { stdio: 'ignore' });
    } catch (_) {
      /* attribute may be absent; ignore */
    }
  }

  console.log(`dejima ${version} installed → ${exePath}`);
}

main().catch((err) => {
  console.error(`\ndejima: install failed: ${err.message}\n`);
  console.error('Alternatives:');
  console.error('  • curl -fsSL https://dejima.tech/install-client.sh | bash');
  console.error('  • brew install aoos/dejima/dejima');
  console.error('  • set DEJIMA_SKIP_DOWNLOAD=1 and point DEJIMA_BINARY at a dejima binary.');
  process.exit(1);
});
