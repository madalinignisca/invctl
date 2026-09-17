// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Guards docs/htmx-swap-targets-design.md's whole point: static coverage
// (internal/web/hx_target_test.go's census) can prove every hx-post element
// DECLARES a swap target, but it cannot see the two signatures that matter
// when one is wrong -- a refusal that produces NO VISIBLE CHANGE (target
// missing or pointed at the wrong element, so the operator sees nothing
// happen), and the DOM ending up with TWO elements sharing the target id (an
// innerHTML swap nesting a fragment whose own root carries the id it was
// targeted at -- htmx would then be unable to tell which one is "the" target
// on the NEXT swap). Both are cheap to assert in a browser and neither is
// what a diff review looks at.
//
// EVERY TEST IN THIS FILE WRITES NOTHING. Every refusal driven below is a
// validation failure or a store-invariant refusal, and both return before any
// store mutation -- that is what makes them refusals rather than successes.
// So, unlike saved-views.spec.js or correction-paths.spec.js, this file needs
// neither the INV_E2E_DISPOSABLE opt-in nor a hostname denylist: there is
// nothing here for either guard to protect against. Each test says so again,
// briefly, at its own top, per this suite's rule that a spec states its own
// writes (or their absence) loudly rather than leaving a reader to infer it.
//
// PREREQUISITE: INV_SEED_COMPANY=true, in addition to the usual INV_SEED=true
// (docs/E2E.md). The team-reassign-retire test needs a team that owns at
// least one entity ("commerce", from the company fixture) and the invariant
// test needs an existing certificate ("sso.example.com", same fixture) --
// neither exists in the BASE-only seed.
import { test, expect } from '@playwright/test';
import { resolveTeamPath, resolveCertificatePath } from '../helpers/resolve.js';

const BASE_URL = process.env.INV_E2E_BASE_URL;
const describe = BASE_URL ? test.describe : test.describe.skip;

describe('refusal targets', () => {
  // certificates create -- both fields are type="date" with no min/max, so a
  // browser submits a backwards range unedited; no vocabulary fixture needed.
  test('certificate create: an invalid date range is refused in place, nothing written', async ({ page }) => {
    await page.goto('/certificates', { waitUntil: 'networkidle' });

    await page.fill('#c-subject', 'backwards.example.com');
    await page.fill('#c-from', '2030-01-01');
    await page.fill('#c-until', '2020-01-01');
    await page.locator('#certificate-form button[type=submit]').click();
    await page.waitForLoadState('networkidle');

    // 1. the refusal is visible
    await expect(page.locator('#certificate-form .field-error')).toContainText(
      'cannot be before the date it becomes valid',
    );
    // 2. the operator keeps what they typed
    await expect(page.locator('#c-subject')).toHaveValue('backwards.example.com');
    await expect(page.locator('#c-from')).toHaveValue('2030-01-01');
    await expect(page.locator('#c-until')).toHaveValue('2020-01-01');
    // 3. the DOM holds exactly one element with the target id -- the one
    // assertion a nesting bug cannot pass by accident.
    await expect(page.locator('#certificate-form')).toHaveCount(1);
  });

  // team create -- `required` only rejects empty, and checkRequired trims, so
  // a single space clears the required-attribute gate and still refuses
  // server-side. The NAME field is server-trimmed to empty and so is not
  // repopulated (there is nothing to repopulate); CODE is what the brief
  // calls out as the value that must survive, and it does.
  test('team create: a blank name is refused in place, the code survives', async ({ page }) => {
    await page.goto('/teams', { waitUntil: 'networkidle' });

    await page.fill('#t-code', 'netsec');
    await page.fill('#t-name', ' ');
    await page.locator('#team-create-form button[type=submit]').click();
    await page.waitForLoadState('networkidle');

    await expect(page.locator('#team-create-form .field-error')).toContainText('is required');
    await expect(page.locator('#t-code')).toHaveValue('netsec');
    await expect(page.locator('#team-create-form')).toHaveCount(1);
  });

  // user create -- a too-short password is real and browser-submittable:
  // type="password" required has no minlength attribute, so nothing client
  // side blocks this. Browsers do not repopulate a password input on a DOM
  // replace regardless of what the server does (and the template sets no
  // value= on it either), so the meaningful "did this write?" check here is
  // the roster, not the input -- matching the brief's own choice of
  // assertion for this surface.
  test('user create: a too-short password is refused in place, no account is created', async ({ page }) => {
    await page.goto('/users', { waitUntil: 'networkidle' });

    await page.fill('#u-username', 'sweep-e2e');
    await page.fill('#u-password', 'short');
    await page.locator('#user-form button[type=submit]').click();
    await page.waitForLoadState('networkidle');

    await expect(page.locator('#user-form .field-error')).toContainText('at least 12 characters');
    await expect(page.locator('#u-username')).toHaveValue('sweep-e2e');
    await expect(page.locator('#user-form')).toHaveCount(1);

    await page.goto('/users?q=sweep-e2e', { waitUntil: 'networkidle' });
    await expect(page.locator('body')).not.toContainText('sweep-e2e');
  });

  // team reassign-retire -- FLAGGED, read this before changing it.
  //
  // The only refusal this route can produce without a write is an empty
  // target_team_id, and <select required> with an empty first option blocks
  // that client-side; TeamOptions also excludes retired teams, so there is no
  // selectable option that is itself refusable. The only OTHER way to reach
  // this route's 422 is to choose a real target team, which -- on success --
  // permanently reassigns every entity the source team owns and retires it,
  // an irreversible write against change_log that docs/E2E.md forbids against
  // a shared instance regardless of which instance this happens to be run
  // against today.
  //
  // So this test sets `form.noValidate = true` on the live form and submits
  // it with the select left at its default, empty value. That bypasses only
  // the CLIENT-SIDE required gate -- the request, the 422, the target and the
  // swap that follow are all real and are the entire point of this test. It
  // does not choose a team, does not select an option, and writes nothing.
  test('team reassign-retire: an empty target is refused in place, nothing reassigned', async ({ page }) => {
    const teamPath = await resolveTeamPath(page, 'commerce');
    await page.goto(`${teamPath}/retire`, { waitUntil: 'networkidle' });

    const targetSelect = page.locator('#rr-target');
    await expect(
      targetSelect,
      '"commerce" must own at least one entity for this panel to render -- ' +
        'see docs/E2E.md, INV_SEED_COMPANY=true',
    ).toHaveCount(1);
    await expect(targetSelect).toHaveValue('');

    await page.evaluate(() => {
      document.querySelector('#rr-target').closest('form').noValidate = true;
    });
    await page.locator('form:has(#rr-target) button[type=submit]').click();
    await page.waitForLoadState('networkidle');

    await expect(page.locator('#team-retire-confirm .field-error')).toContainText(
      'Choose a team to reassign to.',
    );
    // The select is still there, still unset -- there was never a typed value
    // to preserve, only a choice not to have made one, and that is intact.
    await expect(page.locator('#rr-target')).toHaveValue('');
    await expect(page.locator('#team-retire-confirm')).toHaveCount(1);

    // Nothing was reassigned: the panel still offers the same reassign-then-
    // retire form for the same team, which only renders while the team is
    // active and still owns something.
    await expect(page.locator('.panel-head h2').first()).toContainText('looks after');
  });

  // identity rotation -- the control. Already correct since 068d91c; this
  // test exists so that a red run anywhere else in this file means a real
  // defect rather than a broken spec (docs/htmx-swap-targets-design.md's
  // "what would be true if this were broken" section). Bypasses the date
  // input's own max="{{.Today}}" the same way the certificate dates need no
  // bypass at all -- see the form's own comment for why this client-side gate
  // exists and why this test removes it on purpose.
  test('identity rotation (control): a future date is refused in place', async ({ page }) => {
    await page.goto('/identities', { waitUntil: 'networkidle' });
    const firstIdentity = page.locator('#identity-list a', { hasText: /.+/ }).first();
    await firstIdentity.click();
    await page.waitForLoadState('networkidle');

    const rotationInput = page.locator('#i-rot');
    await expect(
      rotationInput,
      'at least one identity must exist for this control to run -- see docs/E2E.md',
    ).toHaveCount(1);

    await page.evaluate(() => document.querySelector('#i-rot').removeAttribute('max'));
    await page.fill('#i-rot', '2099-01-01');
    await page.locator('form:has(#i-rot) button[type=submit]').click();
    await page.waitForLoadState('networkidle');

    await expect(page.locator('#identity .field-error')).toContainText(
      'a rotation cannot be recorded in the future',
    );
    await expect(page.locator('#i-rot')).toHaveValue('2099-01-01');
    await expect(page.locator('#identity')).toHaveCount(1);
  });

  // handleStoreError's ErrInvalid path (Task 4): 422 + HX-Reswap: none + an
  // out-of-band flash, rather than a plain sentence that app.js's forced
  // 422-swap would have dropped straight into #certificate. HX-Reswap
  // appears nowhere in this codebase outside vendored htmx.min.js, so whether
  // htmx honours it HERE, composed with app.js's forced swap, is a question
  // only a browser answers.
  //
  // What would be true if this were broken: either the panel this form lives
  // in is replaced by one sentence (HX-Reswap ignored), or nothing happens at
  // all and the operator is told nothing (the flash dropped).
  //
  // The certificate's own <select> only ever lists real services
  // (AllServices), so there is no genuinely nonexistent service_id a person
  // could pick from the rendered markup. This test injects one bogus
  // <option> into that live <select> and selects it -- the DOM it submits
  // through is the certificate's own real form, the request that follows is
  // a real POST /certificates/{id}/services, and the 422 it receives is the
  // server refusing a service_id that does not exist, which is the actual
  // subject of this test. Deploying to a nonexistent service writes nothing.
  test('an invariant refusal (nonexistent service_id) flashes and destroys nothing', async ({ page }) => {
    const certPath = await resolveCertificatePath(page, 'sso.example.com');
    await page.goto(certPath, { waitUntil: 'networkidle' });

    const serviceSelect = page.locator('#cd-service');
    await expect(
      serviceSelect,
      'the signed-in session must be an admin, and "sso.example.com" must ' +
        'exist, for the deploy-to-service form to render -- see docs/E2E.md',
    ).toHaveCount(1);

    const headingsBefore = await page.locator('#certificate .panel-head h2').allTextContents();
    expect(headingsBefore[0]).toBe('What it is');

    await page.evaluate(() => {
      const select = document.querySelector('#cd-service');
      const bogus = document.createElement('option');
      bogus.value = '01a0ac07-0000-7000-8000-000000000000';
      bogus.textContent = 'nonexistent-service (injected by this test)';
      select.appendChild(bogus);
      select.value = bogus.value;
    });
    await page.locator('form:has(#cd-service) button[type=submit]').click();
    await page.waitForLoadState('networkidle');

    await expect(page.locator('#flash-dock .flash-error')).toBeVisible();
    // Not replaced by a sentence: the panel is still the whole certificate
    // view, starting with its own first heading.
    await expect(page.locator('#certificate')).toHaveCount(1);
    await expect(page.locator('#certificate .panel-head h2').first()).toHaveText('What it is');
  });
});
