/**
 * link-playwright.cjs — postinstall script
 *
 * Resolves the @playwright/test package from the system `playwright` binary
 * and creates symlinks in node_modules so ESM/CJS imports resolve to the
 * same instance as the runner.
 *
 * Why this is needed:
 *   - On NixOS, `playwright` is installed system-wide (not in node_modules),
 *     and browsers come from the Nix store via PLAYWRIGHT_BROWSERS_PATH.
 *   - Installing @playwright/test locally would create a second instance,
 *     causing "Playwright Test did not expect test() to be called here"
 *     errors when spec files resolve a different instance than the runner.
 *   - Symlinking the system packages into node_modules ensures a single
 *     instance shared by both the CLI and the spec files.
 *
 * Environments:
 *   - NixOS (native): resolves from the `playwright` binary on PATH and
 *     symlinks @playwright/test, playwright, playwright-core into node_modules.
 *   - Non-NixOS (npm install of the pinned devDependency): the package is
 *     already present after `npm install`/`npm ci`, so nothing happens.
 *
 * The version pinned in package.json must match the nixpkgs playwright-driver
 * (see maxos/tools/playwright.nix) so the browser revisions agree.
 */

'use strict';

const { execSync } = require('child_process');
const fs = require('fs');
const path = require('path');

const nodeModules = path.resolve(__dirname, '../node_modules');

// Packages to symlink from the system playwright installation
const PACKAGES = [
  { scope: '@playwright', name: 'test', dir: '@playwright/test' },
  { scope: null,          name: 'playwright',      dir: 'playwright' },
  { scope: null,          name: 'playwright-core', dir: 'playwright-core' },
];

/**
 * Find the system playwright node_modules root (NixOS store), if any.
 */
function findSystemPlaywrightRoot() {
  try {
    const bin = execSync('which playwright 2>/dev/null', { encoding: 'utf8' }).trim();
    if (bin) {
      const root = path.resolve(bin, '../../lib/node_modules');
      if (fs.existsSync(path.join(root, '@playwright', 'test'))) {
        return root;
      }
    }
  } catch { /* not on PATH */ }
  return null;
}

const systemRoot = findSystemPlaywrightRoot();

if (systemRoot) {
  console.log(`[link-playwright] Symlinking Playwright packages from: ${systemRoot}`);
  for (const pkg of PACKAGES) {
    const src = path.join(systemRoot, pkg.dir);
    const linkPath = pkg.scope
      ? path.join(nodeModules, pkg.scope, pkg.name)
      : path.join(nodeModules, pkg.name);
    if (!fs.existsSync(src)) {
      console.warn(`  ⚠ Source not found, skipping: ${src}`);
      continue;
    }
    if (pkg.scope) fs.mkdirSync(path.dirname(linkPath), { recursive: true });
    fs.rmSync(linkPath, { recursive: true, force: true });
    fs.symlinkSync(src, linkPath);
    console.log(`  ✓ ${linkPath} → ${src}`);
  }
  console.log('[link-playwright] Done.');
} else {
  console.log('[link-playwright] No system Playwright found — using node_modules (npm) install.');
}
