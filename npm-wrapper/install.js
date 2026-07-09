#!/usr/bin/env node
// Downloads the platform-appropriate liveurl release archive from GitHub
// Releases and extracts it into vendor/, the same pattern esbuild/swc use
// to ship a compiled binary through npm without bundling every platform's
// build into the published package itself.
'use strict';

const fs = require('fs');
const https = require('https');
const path = require('path');
const tar = require('tar');

const pkg = require('./package.json');

const PLATFORM_MAP = { linux: 'linux', darwin: 'darwin', win32: 'windows' };
const ARCH_MAP = { x64: 'amd64', arm64: 'arm64' };

function resolveTarget() {
  const os = PLATFORM_MAP[process.platform];
  const arch = ARCH_MAP[process.arch];
  if (!os || !arch) {
    throw new Error(
      `liveurl: unsupported platform ${process.platform}/${process.arch}. ` +
      `Supported: linux/darwin/win32 on x64/arm64. Build from source instead: ` +
      `https://github.com/Tehman700/liveurl#getting-started`
    );
  }
  return { os, arch };
}

function get(url, redirectsLeft = 5) {
  return new Promise((resolve, reject) => {
    https.get(url, { headers: { 'User-Agent': 'liveurl-npm-installer' } }, (res) => {
      if ([301, 302, 303, 307, 308].includes(res.statusCode) && res.headers.location) {
        if (redirectsLeft <= 0) return reject(new Error('too many redirects'));
        res.resume();
        return resolve(get(res.headers.location, redirectsLeft - 1));
      }
      if (res.statusCode !== 200) {
        res.resume();
        return reject(new Error(`download failed with status ${res.statusCode}: ${url}`));
      }
      resolve(res);
    }).on('error', reject);
  });
}

async function main() {
  const { os, arch } = resolveTarget();
  const version = pkg.version;
  const tag = 'v' + version;
  const archiveName = `liveurl_${version}_${os}_${arch}.tar.gz`;
  const url = `https://github.com/Tehman700/liveurl/releases/download/${tag}/${archiveName}`;

  const vendorDir = path.join(__dirname, 'vendor');
  fs.mkdirSync(vendorDir, { recursive: true });

  console.log(`liveurl: downloading ${archiveName}...`);
  const stream = await get(url);
  await new Promise((resolve, reject) => {
    stream.pipe(tar.x({ cwd: vendorDir })).on('finish', resolve).on('error', reject);
  });

  for (const bin of ['liveurl', 'liveurld']) {
    const binPath = path.join(vendorDir, os === 'windows' ? `${bin}.exe` : bin);
    if (fs.existsSync(binPath) && process.platform !== 'win32') {
      fs.chmodSync(binPath, 0o755);
    }
  }

  console.log('liveurl: installed successfully.');
}

main().catch((err) => {
  console.error(err.message || err);
  process.exit(1);
});
