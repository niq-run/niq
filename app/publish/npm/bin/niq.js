#!/usr/bin/env node
// niq launcher: forwards to the platform-specific binary installed as an
// optional dependency (@niq.run/niq-<os>-<arch>).

'use strict';

const os = require('os');
const path = require('path');
const { spawnSync } = require('child_process');

const platform = os.platform();
const arch = os.arch();

// npm arch names match Go except x64 (amd64) and arm64.
const archMap = { x64: 'x64', arm64: 'arm64' };
const osMap = { darwin: 'darwin', linux: 'linux', win32: 'win32' };

const pkgOs = osMap[platform];
const pkgArch = archMap[arch];
if (!pkgOs || !pkgArch) {
  console.error(`niq: unsupported platform ${platform}/${arch}`);
  process.exit(1);
}

let subpkg;
try {
  subpkg = require.resolve(`@niq.run/niq-${pkgOs}-${pkgArch}/package.json`);
} catch (e) {
  console.error(
    `niq: binary package @niq.run/niq-${pkgOs}-${pkgArch} is not installed.\n` +
    'Try reinstalling with:\n\n  npm install @niq.run/niq --force\n'
  );
  process.exit(1);
}

const bin = path.join(path.dirname(subpkg), 'bin', 'niq');
const binExe = platform === 'win32' ? bin + '.exe' : bin;

const result = spawnSync(binExe, process.argv.slice(2), {
  stdio: 'inherit',
});
if (result.error) {
  console.error(`niq: failed to launch binary: ${result.error.message}`);
  process.exit(1);
}
process.exit(result.status === null ? 1 : result.status);
