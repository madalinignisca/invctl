// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// One round trip through a REAL Keycloak, per
// docs/superpowers/specs/2026-09-18-keycloak-oidc-design.md section 7. The
// unit layer (internal/auth/oidc_test.go, internal/web/oidc_flow_test.go)
// already covers every refusal against a fake issuer -- wrong key, wrong
// audience, replayed state, and so on -- with a fake issuer chosen
// deliberately over a mocked library, because a mock of go-oidc would only
// prove a mock can be written. What NEITHER of those layers can see is
// whether two real, separately-configured servers actually agree with each
// other: does invctl's configured redirect URL match what the realm has on
// file, does the browser's session cookie survive the trip out to Keycloak
// and back, does INV_SECURE_COOKIES=false actually get us a cookie over
// plain HTTP. A mismatched redirect URI is the single most common OIDC
// setup failure in the wild, and it produces a clean, well-formed error
// screen -- AT KEYCLOAK, not at invctl -- that every one of the layers above
// is structurally blind to, because none of them run two processes.
//
// THIS SPEC NEEDS ITS OWN, DEDICATED THROWAWAY INSTANCE. It is not part of
// the usual "point INV_E2E_BASE_URL at a seeded demo estate" flow the rest
// of this suite documents in docs/E2E.md: an OIDC-configured instance has
// INV_AUTH_LOCAL off by default (spec D3), so the admin/demo-password login
// global-setup.js performs for every other spec has nothing to log in
// against here. Bring up:
//
//   1. Keycloak with the realm in tests/e2e/oidc-fixtures/realm-export.json
//      imported (docker-compose.yml's `oidc-e2e` profile does this):
//        docker compose --profile oidc-e2e up -d --wait keycloak
//
//   2. invctl itself, pointed at that realm, on the exact host:port the
//      realm's client was given as its redirect URI and web origin --
//      currently http://localhost:18088, see realm-export.json. This has to
//      be "localhost", not "127.0.0.1": Keycloak treats them as different
//      origins for its redirect-URI check, and the invctl session cookie
//      set while on one is simply not sent back on the other, which fails
//      exactly like a real redirect-URL misconfiguration would (and is how
//      this spec was first proven to fail for the right reason -- see the
//      commit this file landed in). A throwaway SQLite file, not the real
//      database:
//        INV_DB_DRIVER=sqlite \
//        INV_DB_DSN='file:/path/to/throwaway.db?_txlock=immediate' \
//        INV_LISTEN=localhost:18088 \
//        INV_SEED=true \
//        INV_SECURE_COOKIES=false \
//        INV_OIDC_ISSUER=http://localhost:18089/realms/e2e \
//        INV_OIDC_CLIENT_ID=invctl-e2e \
//        INV_OIDC_REDIRECT_URL=http://localhost:18088/auth/oidc/callback \
//        ./bin/invctl
//
//   3. Then, from tests/e2e:
//        INV_E2E_BASE_URL=http://localhost:18088 INV_E2E_OIDC_ONLY=true \
//          npx playwright test specs/oidc-sign-in.spec.js
//      INV_E2E_OIDC_ONLY tells global-setup.js not to attempt the
//      password-form login every other spec relies on -- see its own
//      comment for why that is a second, narrow, explicit opt-in and not a
//      silent "the field wasn't there so let's carry on".
//
// Kill both processes when done; neither is meant to outlive this run.
import { test, expect } from '@playwright/test';

const BASE_URL = process.env.INV_E2E_BASE_URL;
// Unset BASE_URL is this suite's one legitimate skip (see docs/E2E.md and
// playwright.config.js) -- never a skip because a page or element was
// missing. Same convention as every other spec in this directory.
const describe = BASE_URL ? test.describe : test.describe.skip;

// The realm's one seeded user (tests/e2e/oidc-fixtures/realm-export.json).
// Overridable in case a future realm export renames it, but there is
// deliberately no separate "does it default sensibly" concern here the way
// login-and-version.spec.js has for the demo admin: this user exists for no
// purpose other than this spec.
const KEYCLOAK_USERNAME = process.env.INV_E2E_OIDC_USERNAME || 'e2e-user';
const KEYCLOAK_PASSWORD = process.env.INV_E2E_OIDC_PASSWORD || 'e2e-password';

describe('OIDC sign-in through Keycloak', () => {
  // A clean, unauthenticated context in every test here -- global-setup.js
  // performs no login at all for this base URL (INV_E2E_OIDC_ONLY, see the
  // header above), but being explicit about it matches
  // login-and-version.spec.js and makes the intent obvious without having
  // to go read global-setup.js to know why.
  test.use({ storageState: { cookies: [], origins: [] } });

  test('signs in through Keycloak and lands authenticated', async ({ page }) => {
    const resp = await page.goto('/login');
    expect(resp?.status(), 'GET /login').toBeLessThan(400);

    // THE MFA BOUNDARY (spec D3): once OIDC is configured, INV_AUTH_LOCAL
    // defaults to false and the password form must be gone entirely, not
    // merely unused. If this ever finds one, that is a bypass of Keycloak
    // and whatever MFA it enforces, not a cosmetic regression -- assert it
    // BEFORE clicking through, so a future change that resurrects the form
    // fails here rather than being masked by the sign-in below still
    // working via Keycloak.
    await expect(page.locator('input[name=password]')).toHaveCount(0);

    await page.getByRole('link', { name: /keycloak/i }).click();

    // Now on Keycloak's own login page, a different origin entirely -- if
    // the redirect URI configured on either side is wrong, THIS is where it
    // breaks, with Keycloak's own error page, not invctl's.
    await expect(page).toHaveURL(/\/realms\/.+\/protocol\/openid-connect\/auth/);
    await page.locator('#username').fill(KEYCLOAK_USERNAME);
    await page.locator('#password').fill(KEYCLOAK_PASSWORD);
    await Promise.all([
      page.waitForURL((url) => url.origin === new URL(BASE_URL).origin),
      page.locator('#kc-login').click(),
    ]);

    // Back on invctl, authenticated, on the dashboard.
    await expect(page).toHaveURL(/\/$/);
    await expect(page.getByText('What needs a decision')).toBeVisible();

    // web/templates/layouts/base.html renders `.rail-foot .id` only for a
    // signed-in user, same assertion login-and-version.spec.js makes for
    // the password path -- the OIDC path must land in the same
    // authenticated render, not a look-alike page.
    const displayName = page.locator('.rail-foot .id');
    await expect(displayName).toBeVisible();
    expect((await displayName.textContent())?.trim(), 'signed-in display name').not.toBe('');
  });

  test('signing out ends the invctl session', async ({ page }) => {
    // Sign in the same way as above -- this test does not depend on the
    // first (Playwright specs in this suite run serially, but each test
    // still gets its own clean context per test.use above).
    await page.goto('/login');
    await page.getByRole('link', { name: /keycloak/i }).click();
    await page.locator('#username').fill(KEYCLOAK_USERNAME);
    await page.locator('#password').fill(KEYCLOAK_PASSWORD);
    await Promise.all([
      page.waitForURL((url) => url.origin === new URL(BASE_URL).origin),
      page.locator('#kc-login').click(),
    ]);
    await expect(page).toHaveURL(/\/$/);

    // web/templates/layouts/base.html: the sign-out control is a real POST
    // form, not a link -- following it is what actually ends invctl's
    // session (RenewToken + session destroy), unlike merely navigating away.
    await Promise.all([
      page.waitForURL(/\/login/),
      page.getByRole('button', { name: 'Sign out' }).click(),
    ]);

    // Spec D5, asserted precisely and no further: invctl's session is its
    // own, and ending it is what this test checks. It deliberately makes NO
    // claim about Keycloak's own session (its cookie, its SSO state) --
    // that is Keycloak's concern, and asserting on it here would be testing
    // someone else's product through a container we configured, the same
    // reason "Keycloak enforces MFA" is explicitly out of scope for this
    // suite (design spec section 7, "Explicitly not tested").
    const resp = await page.goto('/assets');
    expect(resp?.status(), 'GET /assets after sign-out').toBeLessThan(400);
    await expect(page).toHaveURL(/\/login(\?|$)/);
  });
});
