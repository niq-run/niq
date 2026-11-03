#!/usr/bin/env node
// Two conveniences after `npm install -g @niq.run/niq`:
//
//   macOS   niq.app in ~/Applications, and ~/.niq/bin/niq pointing at this
//           install's binary — a GUI launch inherits no PATH, so the app could
//           not find the npm shim on its own
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
  installApp(path.resolve(__dirname, '..'), binExe)
}

if (process.env.NIQ_NO_COMPLETION !== '1' && process.platform !== 'win32') {
  const result = spawnSync(binExe, ['completion', 'install'], { encoding: 'utf8' })
  if (result.status === 0 && result.stdout) {
    process.stdout.write(result.stdout)
  }
}
process.exit(0)

// installApp puts the bundle where Finder and Spotlight look, and links the CLI
// somewhere the app looks: ~/.niq/bin is on its list before it falls back to a
// login shell, which a GUI launch cannot use to run a node shim anyway.
function installApp(pkgRoot, binExe) {
  const src = path.join(pkgRoot, 'niq.app')
  const dest = path.join(os.homedir(), 'Applications', 'niq.app')
  if (fs.existsSync(path.join(src, 'Contents', 'Info.plist'))) {
    try {
      fs.rmSync(dest, { recursive: true, force: true })
      fs.mkdirSync(path.dirname(dest), { recursive: true })
      const copied = spawnSync('/bin/cp', ['-R', src, dest], { encoding: 'utf8' })
      if (copied.status === 0) {
        console.log(`niq: installed ${dest}`)
      } else {
        console.error(`niq: could not copy the app: ${copied.stderr.trim()}`)
      }
    } catch (e) {
      console.error(`niq: could not install the app to ${dest}: ${e.message}`)
    }
  }

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
