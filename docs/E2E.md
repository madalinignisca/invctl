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

## This suite is read-only by default -- not never writes

**This claim used to be absolute and is no longer true; treat any version of
this file that says "never" as stale.** Most of this suite is read-only
navigation: it fills the login form once (`global-setup.js`) and otherwise
only follows links and reads the rendered DOM, including the retired-option
spec, which opens `?edit=<id>#edit` to read the form's markup and **never
submits it**. That matters beyond the usual "don't corrupt test data"
reason: entities in this system are soft-delete only (CLAUDE.md: "Soft
delete only, for entities... Never delete an asset"), this suite may run
against the shared public demo, and any write there is a **permanent
artefact** on an instance other people are shown.

A handful of specs are the declared exception, because the flow they guard
genuinely cannot be proven without a real mutation. Each states its own
write loudly at the top of the file, per the rule below, and each is gated
so it refuses to run somewhere that write would be a lasting problem --
but the two guard shapes in this suite are not the same, and the difference
matters:

- **`user-administration.spec.js`** and the two `rbac-project-owner-*.spec.js`
  specs use a **denylist**: they refuse only a host that *looks like* the
  shared public demo (a hostname match on `invctl.madalin.me`), and
  otherwise run against whatever `INV_E2E_BASE_URL` is already set to. That
  is a reasonable bar for what they write -- a throwaway account promoted
  and demoted within the same run, or a project-owner fixture the seeder
  itself refuses to create without an explicit flag and password
  (`INV_SEED_E2E_PROJECT_OWNER`, its own section above) -- and it means
  these specs run in the normal `INV_E2E_BASE_URL=<local instance>` case
  with no extra opt-in.
- **`saved-views.spec.js`** uses a stricter **positive opt-in**,
  `INV_E2E_DISPOSABLE=true`, and refuses to run at all without it -- a
  saved view is a soft-delete-only row like everything else in this system,
  so even this spec's own "remove" step leaves a permanently retired row
  behind on whatever instance it ran against, and a hostname denylist only
  ever knows about hosts someone thought to list. Without the opt-in the
  spec reports itself skipped (with the reason in the skipped test's own
  title, since a skipped `describe` block never runs a `beforeAll` that
  could print anything); with the opt-in set but `INV_E2E_BASE_URL` pointed
  anywhere other than `localhost`/`127.0.0.1`, it **fails loudly** instead
  of skipping or running -- see the spec's own header for why that
  combination specifically must not be treated as "probably fine".

If a future spec needs to exercise a mutation, follow whichever of these two
shapes actually fits what it writes, state the constraint loudly at the top
of that spec (not assumed), and never write against a shared or public
instance.

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

## The project-owner fixture (WP-G1 Task 17)

`rbac-project-owner-edit-boundary.spec.js` and
`rbac-project-owner-create-in-project.spec.js` need a real, loggable-in
project owner assigned to a real project -- something no HTTP route in this
codebase can create yet (`internal/store/user_projects.go`'s `AssignProject`
has no handler in front of it; every Go test that needs one calls it
directly against `*store.SQLStore`, which a browser cannot do). Start the
target instance with the fixture opted in, in addition to the usual seed:

```
INV_SEED=true INV_SEED_E2E_PROJECT_OWNER=true \
  INV_E2E_PROJECT_OWNER_PASSWORD='<pick one>' make dev
```

Then run the specs with the **same** password in the environment:

```
INV_E2E_PROJECT_OWNER_PASSWORD='<the same one>' npm run e2e
```

This stages `seed.StageE2EProjectOwner` (`internal/seed/seed_e2e_fixture.go`)
after the seeded administrator exists: an account named `e2e-project-owner`
(overridable via `INV_E2E_PROJECT_OWNER_USERNAME`), assigned to the
"platform" project -- which owns `hv-01`/`hv-02`/`hv-03` in the BASE fixture,
so `INV_SEED_COMPANY` is not required for these two specs specifically.

**There is no default password, and setting the flag alone stages nothing.**
`INV_E2E_PROJECT_OWNER_PASSWORD` has no fallback in the binary or in the
specs: the seeder refuses to create a login-capable account without one, and
the specs refuse to run without one. Pick a throwaway value per instance.

That is deliberate. The password was a published constant until an auth
review of WP-G1 Task 17 pointed out that the exposure is not only the write
access Task 13 will add, but the **read** access the account has already: an
authenticated session over the entire CMDB -- every asset, address, circuit,
topology edge, `secret_ref` path and the change log. On a real deployment the
administrator password is strong, and that one was in the repository.

**`INV_SEED_E2E_PROJECT_OWNER` still must never be set on a shared or public
deployment.** Note it is a one-way ratchet: unsetting it later does NOT
remove an account it already created. The seeder logs a warning every time it
stages or finds the fixture, which is the signal to look for.
`config.Config.SeedE2EProjectOwner`'s own comment carries the same warning;
nothing in the Makefile's `make dev`/`make demo` defaults sets it.

Both specs write (their second test only), so both refuse to run against the
shared public demo the same way `user-administration.spec.js` does, and
neither touches the `name` field of a named fixture another spec resolves by
name (`hv-01`, `sw-oob-1`) -- see each spec's own header.

**Both specs' second test is a deliberate, declared failure until WP-G1 Task
13 lands.** `auth.CanWrite(RoleProjectOwner)` is still `false`, so
`middleware.RequireWrite` refuses the write with 403 before the entity-scope
check it guards is ever reached. Each uses Playwright's `test.fail()` rather
than skipping: the test genuinely runs and genuinely fails today, for the
right reason (403 from the role gate, not a missing selector or a 404), and
the moment Task 13 flips `CanWrite` and the write starts succeeding,
Playwright reports the test as an *error* -- an unexpected pass -- rather
than silently going green. That is the trigger for whoever lands Task 13 to
come back to these two files and remove the `test.fail()` calls.

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

- **`rbac-project-owner-edit-boundary.spec.js`** and
  **`rbac-project-owner-create-in-project.spec.js`** -- WP-G1 Task 17's two
  critical role-aware-UI flows: a project owner's edit control surviving a
  real signed-in session on their own project's asset and staying absent on
  one outside it, with a direct write to the outside one refused
  server-side; and creating an asset from their own project page through the
  route only a project owner may reach. See "The project-owner fixture"
  above -- both need `INV_SEED_E2E_PROJECT_OWNER=true` and both write.

- **`saved-views.spec.js`** -- WP-G4b: a signed-in person can save the
  filters currently applied to `/assets` under a name, reopen them from the
  Views menu, and remove them. Filters `/assets` to `kind=firewall`, opens
  the Views menu (which lives inside the `#asset-table` fragment the filter
  toolbar swaps, so it has to be opened *after* filtering, not before -- an
  open menu closes on every swap), saves it, navigates back to a genuinely
  clean unfiltered `/assets` (not `page.reload()`, which would just refresh
  a URL that already carries the filter and prove nothing), reopens the
  menu, follows the saved view's plain `<a href>` link, and asserts the
  filter is applied both in the rendered rows and in the URL -- then removes
  the view and confirms it no longer appears. This is the one spec in the
  suite gated by `INV_E2E_DISPOSABLE=true` rather than the shared-demo
  denylist the other writing specs use -- see "This suite is read-only by
  default" above and the spec's own header for why.

## Adding a spec

Keep it small. Four spec files covering critical paths is the right size for
this project; forty would not be. Before adding one, check whether a
handler-level Go test already covers the behaviour -- if it does, this is
redundant maintenance burden for no real safety gain and the test belongs
there instead. A new spec earns its place only if it exercises something
that specifically requires a real browser: routing reachability, DOM state a
browser derives from markup (like the retired-option case), or a
cross-page flow.
