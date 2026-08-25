# E2E test suite

`tests/e2e/` is a small Playwright suite that checks a handful of things in a
real browser that no lower-level test can: that the router actually serves
the page a link points at, that the page doesn't throw in the console, and
that a fix to a template genuinely survives what a browser does with the
markup (the retired-option spec exists because of exactly that last one --
see below). It is not a substitute for the Go test suite and is not
exhaustive UI coverage; CLAUDE.md's testing policy is explicit that E2E here
means a small number of tests on critical paths, not "from 0 to hero" on
every screen.

Read this before running it. These suites fail in ways that look nothing
like their cause -- a red run is almost always either the wrong
`INV_E2E_BASE_URL`, an instance that hasn't been seeded, or (rarely) a real
bug. Knowing which one you're looking at up front saves the debugging.

## Running it

```
INV_E2E_BASE_URL=http://localhost:8088 make e2e
```

`make e2e` is **not** part of `make test` and never will be: `make test` is
the Go-only gate that CI runs on every commit, has no business requiring a
running instance or a Node toolchain, and must stay exactly that fast and
that self-contained. `make e2e` is a separate, opt-in check you run against
something that is actually up.

Node/Playwright are a **test-time** dependency of `tests/e2e/` only.
`make build` stays `CGO_ENABLED=0` with no Node runtime anywhere in the
pipeline -- see `tests/e2e/package.json` and `.node-version` (resolved via
`mise` shims per the global environment convention; nothing here needs
`nvm`).

Without `INV_E2E_BASE_URL` set, the whole suite reports itself **skipped**,
not passed and not failed:

```
$ make e2e
INV_E2E_BASE_URL is not set -- the suite will report itself skipped.
  -  1 › login and version › ...
  -  2 › custom fields panel › ...
  -  3 › retired custom-field option invariant › ...
  -  4 › smoke: main sections load clean › ...
  4 skipped
```

This is the **one legitimate skip** in this suite, and it is legitimate
specifically because it is an explicit env-var opt-in
(CLAUDE.md's testing policy: "A runtime skip is legitimate only on an
explicitly declared precondition: an env-var opt-in, or a genuinely optional
external service"). Nothing in this suite skips itself for any other
reason. If `INV_E2E_BASE_URL` **is** set and the target is unreachable, or
login fails, the run **fails** -- it does not skip. `global-setup.js` logs in
once before any spec runs and throws if that doesn't work, which aborts the
whole run rather than letting every spec quietly 404 or bounce to `/login`
in a way that would look like an unrelated failure.

## What it expects: the demo estate

This suite resolves every entity it touches by **name**, never by a
hardcoded UUID -- IDs are UUIDv7s generated fresh on every seed run
(`internal/domain`, CLAUDE.md's database rules), so pinning one would pass
against the exact database it was written on and fail everywhere else for a
reason that has nothing to do with a real bug. `tests/e2e/helpers/resolve.js`
looks an asset up by name through `/assets?q=<name>`, the same filter a
person would use, and fails with a clear message naming the missing fixture
rather than a confusing locator timeout if the asset isn't there.

The named fixtures it expects are `hv-01`, `fw-dev-1`, `sw-tor-a2` and
`sw-oob-1`, which come from the demo estate seed:

```
INV_SEED=true INV_SEED_COMPANY=true
```

`make dev` and `make demo` already set `INV_SEED=true`; `INV_SEED_COMPANY` is
what loads the named assets and the custom-field data the retired-option and
custom-fields-panel specs need. **Pointing this suite at a local instance is
the normal case.** The public demo
(`INV_E2E_BASE_URL=https://invctl.madalin.me`, `admin` / `demo-password`,
both public by design -- see `docs/DEMO.md`) is the exception, useful for a
quick check without standing anything up locally, not the primary target.

## This suite never writes

Every spec is read-only navigation: it fills the login form once
(`global-setup.js`) and otherwise only follows links and reads the rendered
DOM, including the retired-option spec, which opens `?edit=<id>#edit` to
read the form's markup and **never submits it**. This matters beyond the
usual "don't corrupt test data" reason: assets in this system are
soft-delete only (CLAUDE.md: "Soft delete only, for entities... Never
delete an asset"), this suite may run against the shared public demo, and
any write there is a **permanent artefact** on an instance other people are
shown. If a future spec needs to exercise a mutation, it must run against a
disposable local instance only, and that constraint should be stated loudly
at the top of that spec, not assumed.

## Where the ownership report's mutation is covered instead

`ownership-report.spec.js` checks that the report renders its three findings,
that an admin is offered a fix path with a CSRF token and selectable rows, and
that no target team is offered while retired. It does **not** submit the
assignment form, for the reason above: bulk assignment writes, and a stray
assignment on the shared demo is a permanent change to an estate other people
are looking at.

The mutation is covered by the Go web suite, which runs against a disposable
database on both engines: `TestOwnershipAssignMovesExactlyTheSelectedIDs`,
`TestOwnershipAssignSkipsAnEntityClaimedInTheMeantime`,
`TestOwnershipAssignRefusesARetiredTarget` and
`TestOwnershipAssignRequiresAdmin` in `internal/web/ownership_assign_test.go`.

That split is deliberate rather than a gap: the Go tests can assert on what
reached the database, which is the thing that matters for a mutation, and this
spec asserts on what a browser actually rendered, which is the thing they
cannot see.

## What each spec guards

- **`login-and-version.spec.js`** -- the first thing a real person hits: can
  they reach the login page, sign in, and see the nav render with the build
  version invctl actually is (`internal/version`, wired to `git describe` at
  build time -- see the Makefile's `VERSION` note). Uses a clean
  (unauthenticated) browser context deliberately, so it exercises the login
  form itself rather than skipping past it on the session `global-setup.js`
  already saved.

- **`custom-fields-panel.spec.js`** -- the "Defined by your organisation"
  panel (`web/templates/partials/custom_fields_show.html`) renders real
  recorded values, at least one field description, and the link to the
  custom-fields registry, on the actual page in the actual layout -- not just
  in a template-package unit test that never renders inside the real page.

- **`retired-option-invariant.spec.js`** -- the crown jewel of this suite,
  and the reason it exists. Guards the fix for a Critical where a retired
  custom-field select option was silently destroyed: with no matching
  `<option>` for the value an entity actually held, a browser falls back to
  its own blank "not set" choice, and the next unrelated save on that form
  quietly wiped the stored value. Nothing about that shows up in a Go test
  against the template package -- it only exists in what a real browser does
  with a `<select>` whose current value has no matching option, which is
  exactly why this has to be a browser test. Checks both directions on real
  assets: the option is present, marked `(retired)`, and `selected` on the
  asset that holds it; entirely absent (not merely unselected) on one that
  doesn't. Does not submit the form -- see above.

- **`smoke.spec.js`** -- every main section (overview, assets, an asset's
  detail/impact/neighbourhood pages, services, changes, custom fields) loads
  with no console errors, no failed or 4xx/5xx requests, and no request to
  any host other than the one under test. This is the layer that catches
  wiring and routing failures that handler-level Go tests are structurally
  blind to: those call handlers directly and inject router params by hand,
  so the request URL is decorative and the router itself is never consulted
  (CLAUDE.md's evidence-gate note describes exactly this shape of bug
  shipping behind a fully green CI run).

## Adding a spec

Keep it small. Four spec files covering critical paths is the right size for
this project; forty would not be. Before adding one, check whether a
handler-level Go test already covers the behaviour -- if it does, this is
redundant maintenance burden for no real safety gain and the test belongs
there instead. A new spec earns its place only if it exercises something
that specifically requires a real browser: routing reachability, DOM state a
browser derives from markup (like the retired-option case), or a
cross-page flow.
