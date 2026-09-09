// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// WP-B3's engine half: simulating a cable cut (GET /links/{id}/impact).
//
// Read-only. Nothing here submits a form — a simulation changes nothing, which
// is the point of it, so this needs neither the disposable opt-in nor the
// hostname denylist docs/E2E.md describes for the writing specs.
//
// The Go suite already asserts the page renders, that the asset page offers the
// link, that an unknown cable is a 404 and that an Observer may read it. This
// file exists for the two claims that specifically need a real browser:
//
//   1. THE PAGE IS REACHED BY CLICKING, from the patching table on an asset,
//      rather than by fetching a URL. This project has shipped a 404 on a
//      button with every handler test green, because a handler test injects
//      router params by hand and never asks the router whether anything can
//      reach it (docs/E2E.md, CLAUDE.md's "Green CI is not evidence").
//   2. THE PAGE RENDERS CLEAN in a browser — no console error, no failed
//      request. The circuit version of this page shipped a 500 from a missing
//      template field and no test caught it, because none of them fetched it.
//      A Go test now fetches this one; only a browser can say the rendered
//      result does not also throw.
//
// It also pins the one thing the three outcomes exist for: EXACTLY ONE of them
// renders. store.CircuitCut's doc comment is explicit that conflating any two
// is a lie the page would tell — "joins nothing" and "cutting this changes
// nothing" look identical in a Result and need opposite sentences.
import { test, expect } from '@playwright/test';

const BASE_URL = process.env.INV_E2E_BASE_URL;
const describe = BASE_URL ? test.describe : test.describe.skip;

// The three mutually exclusive verdicts, exactly as the template writes them.
const OUTCOMES = [
  'joins nothing that is modelled',
  'separates the estate',
  'Another path survives this',
];

describe('cable cut simulation', () => {
  test('is reached by clicking "If it is cut" in an asset\'s patching table', async ({ page }) => {
    // Find an asset that actually has a patched port. Resolved by walking the
    // UI rather than by naming a fixture: which of the demo's assets is cabled
    // is a seed detail, and pinning one would fail for a reason that has
    // nothing to do with this feature.
    await page.goto('/assets', { waitUntil: 'networkidle' });

    const assetLinks = page.locator('#asset-table a.id');
    const count = await assetLinks.count();
    expect(count, 'no assets on /assets').toBeGreaterThan(0);

    let cutLink = null;
    for (let i = 0; i < count && !cutLink; i += 1) {
      await page.goto('/assets', { waitUntil: 'networkidle' });
      await page.locator('#asset-table a.id').nth(i).click();
      await page.waitForLoadState('networkidle');
      const candidate = page.locator('a:text-is("If it is cut")').first();
      if ((await candidate.count()) > 0) {
        cutLink = candidate;
      }
    }
    expect(cutLink,
      'no asset in the estate offers "If it is cut" on any port. Either the ' +
      'seed patches no cables — a fixture problem — or the control has been ' +
      'lost from the patching table, which is the regression this checks.')
      .not.toBeNull();

    await cutLink.click();
    await page.waitForLoadState('networkidle');

    await expect(page.locator('h1')).toContainText('is cut');

    // Exactly one verdict, never two and never none.
    const rendered = [];
    for (const outcome of OUTCOMES) {
      if (await page.getByText(outcome).count()) {
        rendered.push(outcome);
      }
    }
    expect(rendered,
      'the page must render exactly one of the three cut outcomes. A cable ' +
      'that joins nothing and a cable whose loss changes nothing read ' +
      'identically in a Result and need opposite sentences.').toHaveLength(1);
  });

  test('renders with no console error and no failed request', async ({ page }) => {
    const problems = [];
    page.on('console', (m) => {
      if (m.type() === 'error') {
        problems.push(`console: ${m.text()}`);
      }
    });
    page.on('requestfailed', (r) => {
      problems.push(`request failed: ${r.url()}`);
    });
    page.on('response', (r) => {
      if (r.status() >= 400) {
        problems.push(`${r.status()} on ${r.url()}`);
      }
    });

    await page.goto('/assets', { waitUntil: 'networkidle' });
    const count = await page.locator('#asset-table a.id').count();
    for (let i = 0; i < count; i += 1) {
      await page.goto('/assets', { waitUntil: 'networkidle' });
      await page.locator('#asset-table a.id').nth(i).click();
      await page.waitForLoadState('networkidle');
      const candidate = page.locator('a:text-is("If it is cut")').first();
      if ((await candidate.count()) > 0) {
        await candidate.click();
        await page.waitForLoadState('networkidle');
        break;
      }
    }

    expect(problems, 'the cable-cut page did not render cleanly').toEqual([]);
  });
});
