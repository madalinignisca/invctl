// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Playwright config for the E2E suite. See docs/E2E.md before running this --
// these suites fail in ways that look nothing like their cause.
//
// INV_E2E_BASE_URL IS THE ONLY OPT-IN. Unset, every spec's own
// `test.describe.skip` marks the whole suite skipped -- a legitimate runtime
// skip on an explicit env-var precondition (CLAUDE.md's testing policy).
// Set, the suite must run and fail on a real problem: it must never skip
// because a page did not load or login failed. globalSetup below throws
// rather than skips when BASE_URL is set and the login it attempts does not
// work, which is what turns "app is unreachable" into a failure instead of a
// quiet pass.
import { defineConfig, devices } from '@playwright/test';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

// Resolved from this file's own location, not process.cwd() -- `make e2e`
// invokes Playwright from the repo root, `npm test` from here, and a path
// that only works from one of those would be the exact kind of failure this
// suite exists to catch, not cause.
const here = path.dirname(fileURLToPath(import.meta.url));
const authStatePath = path.join(here, 'auth-state.json');

const baseURL = process.env.INV_E2E_BASE_URL || undefined;

export default defineConfig({
  testDir: './specs',
  timeout: 30_000,
  expect: { timeout: 10_000 },
  // Small suite against a single shared instance (often the public demo) --
  // serial execution is deliberate so runs are deterministic and don't
  // contend with each other's login session or rate limits.
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  // No retries. A flaky test hidden behind a retry is a test that has stopped
  // proving anything; CLAUDE.md's testing policy is to stabilise or drop it,
  // never to paper over it.
  retries: 0,
  reporter: [['list']],
  outputDir: 'test-results',
  globalSetup: './global-setup.js',
  use: {
    baseURL,
    // Written by global-setup.js when BASE_URL is set; absent otherwise, so
    // a skipped run never touches a file that was never created.
    storageState: baseURL ? authStatePath : undefined,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
  ],
});
