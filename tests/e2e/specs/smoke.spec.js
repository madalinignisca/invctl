// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// The wiring test: every main section actually loads, with no console
// errors, no failed or 4xx/5xx requests, and no request to a host other than
// the one under test. CLAUDE.md's evidence-gate note is specifically about
// this class of bug -- handler unit tests call handlers directly and never
// consult the router, so a 404 on a real navigation path can sit behind an
// all-green CI run. This is the layer that would have caught that.
//
// Deliberately shallow: it does not assert on page CONTENT (other specs do
// that), only that the page came back clean. That is the right amount of
// coverage for a smoke test -- CLAUDE.md's testing policy is a small number
// of E2E tests on critical paths, not exhaustive UI coverage.
import { test, expect } from '@playwright/test';
import { resolveAssetPath } from '../helpers/resolve.js';

const BASE_URL = process.env.INV_E2E_BASE_URL;
const describe = BASE_URL ? test.describe : test.describe.skip;

const ASSET_NAME = 'hv-01';

describe('smoke: main sections load clean', () => {
  test('no console errors, no failed requests, no external hosts', async ({ page }) => {
    const assetPath = await resolveAssetPath(page, ASSET_NAME);
    const assetID = assetPath.split('/').pop();

    const sections = [
      ['Overview', '/'],
      ['Assets list', '/assets'],
      [`Asset detail (${ASSET_NAME})`, assetPath],
      [`Asset impact (${ASSET_NAME})`, `${assetPath}/impact`],
      ['Services', '/services'],
      [`Topology / neighbourhood (${ASSET_NAME})`, `${assetPath}/neighbourhood`],
      ['Changes / journal', '/changes'],
      ['Custom fields', '/custom-fields'],
    ];

    // The allowed host is derived from BASE_URL itself, not hardcoded to the
    // public demo's hostname -- this suite is meant to run against a local
    // `make dev` just as well (docs/E2E.md).
    const allowedHost = new URL(BASE_URL).hostname;

    for (const [name, path] of sections) {
      const consoleErrors = [];
      const pageErrors = [];
      const failedRequests = [];
      const externalRequests = [];

      const onConsole = (msg) => {
        if (msg.type() === 'error') consoleErrors.push(msg.text());
      };
      const onPageError = (err) => pageErrors.push(err.message);
      const onRequestFailed = (req) =>
        failedRequests.push(`${req.method()} ${req.url()} -> ${req.failure()?.errorText}`);
      const onResponse = (res) => {
        let hostname;
        try {
          hostname = new URL(res.url()).hostname;
        } catch {
          return;
        }
        if (hostname !== allowedHost) externalRequests.push(res.url());
        if (res.status() >= 400) failedRequests.push(`HTTP ${res.status()} ${res.url()}`);
      };

      page.on('console', onConsole);
      page.on('pageerror', onPageError);
      page.on('requestfailed', onRequestFailed);
      page.on('response', onResponse);

      try {
        const resp = await page.goto(path, { waitUntil: 'networkidle', timeout: 20_000 });
        expect(resp?.status(), `${name} (${path}) navigation status`).toBeLessThan(400);
      } finally {
        page.off('console', onConsole);
        page.off('pageerror', onPageError);
        page.off('requestfailed', onRequestFailed);
        page.off('response', onResponse);
      }

      expect(consoleErrors, `${name} console errors`).toEqual([]);
      expect(pageErrors, `${name} page errors`).toEqual([]);
      expect(failedRequests, `${name} failed/4xx/5xx requests`).toEqual([]);
      expect(externalRequests, `${name} requests to a host other than ${allowedHost}`).toEqual([]);
    }
  });
});
