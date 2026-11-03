#!/usr/bin/env node
// Writes niq's shell completion into a directory the shell already scans, so a
// fresh `npm install -g @niq.run/niq` leaves tab completion working without
// anyone editing ~/.zshrc.
//
// Best effort by design: a completion file is a convenience, and an install
// must not fail because one could not be written. NIQ_NO_COMPLETION=1 turns it
// off; Windows has no equivalent directory, so it is skipped there.

'use strict'

const os = require('os')
const path = require('path')
const { spawnSync } = require('child_process')

if (process.env.NIQ_NO_COMPLETION === '1' || process.platform === 'win32') {
  process.exit(0)
}

// Same lookup as bin/niq.js: the binary lives in the platform subpackage.
const archMap = { x64: 'x64', arm64: 'arm64' }
const osMap = { darwin: 'darwin', linux: 'linux' }
const pkgOs = osMap[os.platform()]
const pkgArch = archMap[os.arch()]
if (!pkgOs || !pkgArch) {
  process.exit(0)
}

let subpkg
try {
  subpkg = require.resolve(`@niq.run/niq-${pkgOs}-${pkgArch}/package.json`)
} catch (e) {
  process.exit(0) // optional dependency missing: nothing to run, and niq.js says so when used
}

const binExe = path.join(path.dirname(subpkg), 'bin', 'niq')
const result = spawnSync(binExe, ['completion', 'install'], { encoding: 'utf8' })
if (result.status === 0 && result.stdout) {
  process.stdout.write(result.stdout)
}
process.exit(0)
