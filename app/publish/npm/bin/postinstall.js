#!/usr/bin/env node
// Two conveniences after `npm install -g @niq.run/niq`:
//
//   macOS   ~/.niq/bin/niq pointing at this install's binary — a GUI client
//           inherits no PATH and cannot run a node shim, so the macOS app (in the
//           clients repository) looks there
//   all     the completion script in a directory the shell already scans, so a
//           fresh install leaves tab completion working without editing ~/.zshrc
//
// Best effort by design: an install must not fail because a convenience could
// not be written. NIQ_NO_APP=1 / NIQ_NO_COMPLETION=1 turn them off; Windows has
// neither, so both are skipped there.

'use strict'

const os = require('os')
const fs = require('fs')
const path = require('path')
const { spawnSync } = require('child_process')

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

if (process.platform === 'darwin' && process.env.NIQ_NO_APP !== '1') {
  linkCLI(binExe)
}

if (process.env.NIQ_NO_COMPLETION !== '1' && process.platform !== 'win32') {
  const result = spawnSync(binExe, ['completion', 'install'], { encoding: 'utf8' })
  if (result.status === 0 && result.stdout) {
    process.stdout.write(result.stdout)
  }
}
process.exit(0)

// linkCLI puts the binary somewhere a GUI client can find it. ~/.niq/bin is on
// the macOS app's list before it falls back to a login shell, which a GUI launch
// cannot use to run a node shim anyway. A real file there belongs to a non-npm
// install (install.sh), and is left alone.
function linkCLI(binExe) {
  try {
    const dir = path.join(os.homedir(), '.niq', 'bin')
    fs.mkdirSync(dir, { recursive: true })
    const link = path.join(dir, 'niq')
    let existing = null
    try {
      existing = fs.lstatSync(link)
    } catch (e) {
      // nothing there yet
    }
    // A real file there belongs to a non-npm install (install.sh); leave it.
    if (!existing || existing.isSymbolicLink()) {
      fs.symlinkSync(binExe, link)
    }
  } catch (e) {
    console.error(`niq: could not link the CLI for the app: ${e.message}`)
  }
}
