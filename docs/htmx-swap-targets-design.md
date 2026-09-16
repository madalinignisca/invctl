# HTMX swap targets — design

**Work package unassigned.** Status: spec, awaiting approval. Not a plan —
sequencing is argued here, tasks are not listed.
Census measured 2026-09-16; independently re-checked while writing this
(see "What was verified").

## What this is

Closing a gap between a rule CLAUDE.md already states and the markup that does not
follow it. **"HTTP and HTMX conventions":**

> Swap targets are declared in the template, not chosen by the handler. Default to
> `hx-swap="outerHTML"` on a wrapping element with a stable id.

This work proposes no convention. It applies one the project wrote down, does not
enforce, and violates in 38 of 90 places.

## Why it earns its place

The rule is not decoration — it interlocks with the other one beside it, and the
two of them are what make a refusal visible to an operator.

`web/static/app.js:649-654` force-swaps a **422 only**, and its comment says why:

> HTMX 2 does not swap a 4xx by default, which quietly defeats the convention this
> codebase uses everywhere: a validation failure returns 422 with the form partial
> re-rendered… Without this the server does the right thing and the operator sees
> nothing happen at all.

So a 422 *is* swapped here. An untargeted `<form>` then swallows that swap into its
own `innerHTML`, and an untargeted `<button>` into the button. The handler did the
right thing, the response arrived, the force-swap fired, and what the operator sees
is a whole panel nested inside the control they clicked.

**This was proven in a browser, not theorised.** WP-J8's rotation form had exactly
this shape. A Playwright pass confirmed the 422 body — the whole `#identity` partial
— landing inside the form, every panel heading rendered twice, layout broken. Fixed
in `068d91c`; the comment left behind at `web/templates/partials/identities.html:199-214`
is the reference text for this whole document, including the sentence that settles
the exemption question below.

The handlers behind the untargeted elements genuinely refuse: `certificates.go` has
**3** 422 sites, `teams.go` **5**, `health.go` **1** — all counted, all reachable.

## What was verified

The census file is `.superpowers/hx-target-census.txt` (git-ignored). It is accurate.

| Claim | Status |
|---|---|
| 90 `hx-post` elements; 52 with own target; 38 without | internally consistent (33 + 5 = 38; 52 + 38 = 90) |
| 33 `<form>`, 5 `<button>` untargeted | matches the file's own listing |
| 0 inherit a target from an ancestor | **spot-checked, consistent** — 78 `hx-target` occurrences in the tree, none on a `div`/`table`/`tbody`/`section`/`body` wrapper. Not exhaustively proven; the census's own scan is the stronger evidence |
| certificates 3 / teams 5 / health 1 refusal sites | verified by grep |

Two facts the original brief did not carry, both load-bearing, both found while
checking:

1. **Every partial these handlers re-render on 422 already has a stable wrapping
   id.** `#identity`, `#team`, `#team-list`, `#team-retire-confirm`,
   `#certificate-list`, `#override-form`. The *target* half of the convention is
   already satisfied everywhere; only the *naming* half is missing. That makes most
   of the 38 a genuinely mechanical fix.
2. **Two of them are not mechanical, and targeting them naïvely makes things
   worse.** See "The real risk", (a).

## The rules

### 1. "Declared" means `hx-target` *and* `hx-swap`, on the element itself

**Both attributes, not just the target.** HTMX's default swap is `innerHTML`. A form
declaring `hx-target="#team"` and no swap puts the re-rendered `#team` partial
*inside* `#team` — the same duplicated-heading failure one level up, and harder to
see because the outer shell still looks right. Worse, the fragment's root carries
`id="team"` itself, so the DOM then holds two elements with that id and every later
`hx-target="#team"` resolves to whichever the browser finds first. A half-declared
target is a slower version of the bug.

`outerHTML` is the **default, not the requirement**. CLAUDE.md says "default to",
and this document keeps that word. A different swap value is an argued choice that
satisfies the rule; a *missing* swap value is not. `hx-swap="none"` is a legitimate
declaration, not an exemption.

Cost of the stricter rule is close to nil: the tree carries 78 `hx-swap` attributes
against 78 `hx-target`s, all but two `outerHTML`, so the 52 already-correct elements
appear to carry both already. Not verified pairwise — an implementer will find out,
and if a handful of the 52 carry a target and no swap, they are in scope too.

### 2. Inheritance is not accepted, and the reason is not "HTMX doesn't do it"

HTMX genuinely inherits `hx-target` down the DOM, so a census rejecting inheritance
is stricter than HTMX's own semantics and would flag markup that works. That is the
honest case for accepting it, and it is why this needs an argument rather than an
assertion. Three reasons to reject it anyway, in ascending order of force:

- **It costs nothing today.** 0 of 90 rely on it. The strict rule outlaws nothing
  that exists.
- **It is not statically decidable in this tree.** These partials are composed:
  `certificate_form` is a standalone `{{define}}` included by a page, so a form's
  ancestors depend on who included it, and there may be more than one includer. A
  census accepting inheritance would have to resolve the template inclusion graph to
  know an element's ancestors — and would be wrong for any partial rendered
  standalone as a 422 body, where the ancestor chain does not exist at all.
- **It contradicts a rule already in force.** CLAUDE.md: *"A partial must be
  renderable standalone — that's the whole point. If a partial only works when its
  parent has already rendered, it's wrong."* A partial whose swap target comes from
  a parent is precisely a partial that behaves differently depending on its parent.
  Inheritance is not merely unverifiable here; it is already forbidden by a rule
  written about something else.

### 3. One rule for buttons and forms

The blast radius differs and that is not a reason to split the rule.

It is worth being exact about how it differs, because "small" is the wrong word: the
*response* is identical in both cases — the same whole-panel partial — and only the
container it lands in is smaller. A panel inside a `<button>` is more obviously
broken than a panel inside a `<form>`, not less. Either way the operator does not
see the refusal they need to see. This is a different wrong, not a lesser one.

The real argument for one rule is that a two-rule census has to decide which rule
applies from the tag name, and the population is not stable:
`web/templates/partials/rows.html:154` is already a `<button hx-post>` carrying its
own target and swap. The codebase treats the two alike wherever it does this
correctly. A rule needing judgement per element is a rule that gets re-argued per
element in every review. Buying a judgement-free rule costs 5 elements.

### 4. When "no target" is legitimate — and why the obvious criterion fails

There must be an exemption path. Every census in this repo has one, each entry
argued rather than assumed, each comment saying **empty is the goal**:
`versionExemptRoutes` (`internal/web/edit_form_version_test.go:50-56`, currently
`map[string]string{}`), the store's `unreachableRepairPaths` (emptied by the work
that closed it), `refusalFlashExceptions`.

**The candidate criterion — "a route that can never return a force-swapped status" —
is rejected, on two grounds.**

*It is not knowable from the template alone.* The census reads templates; the status
a route can return is a fact about a handler. Deriving it would need a call-graph
walk harder than `handlersReadingAVersion`'s, because a status is data-dependent
rather than a call.

*It would not be sufficient even if it were knowable*, and this repo has already
ruled on exactly this. `identities.html:318-324`, written when the same question came
up on the withdraw form:

> Set them because the convention says so, not because the only conflict status
> happens not to trigger the default today.

The set of force-swapped statuses is one listener and one comparison in `app.js`.
Widening it to 409 — an obvious candidate, reachable from every versioned edit form
and from `handleStoreError`'s `ErrConflict` branch — would silently re-arm every
element exempted on that basis. An exemption whose validity rests on a line in a
different file that nobody consulted is not an exemption; it is a fuse.

**So the criterion is narrow and stated in the map's own comment:** an entry claims
*this element's response has no in-band body under any status its handler can
produce*, and that claim is argued from the handler, not from `app.js`'s current
listener. Whoever adds an entry writes down what they read to believe it.

**The map should be empty at merge**, which is where `versionExemptRoutes` already
sits.

One objection a reviewer will raise, answered here rather than left to them: *a
target that is never exercised is inert markup, and this repo objected to inert
markup* — the identity withdraw-form amendment removed a `row_version` input on
exactly that ground. The two are not alike, and the asymmetry is the answer. An inert
`row_version` **misrepresents a guard that is not there**; a reader sees optimistic
locking where none exists. An `hx-target` that never fires misrepresents nothing — it
names where a response would land if one came. Inert and misleading are different
faults, and only one of them was being objected to.

## The real risk, stated plainly

Some of these 38 "work" today by accident. Adding a target changes where their
response lands. Three ways that can go wrong, in descending order of severity.

### (a) The silent-refusal inversion — measured, not hypothetical

**This is the finding that contradicts the original framing, and it is why this is
not a markup-only change.**

`CertificateCreate` (`internal/web/handlers/certificates.go:83-98`) answers a
validation failure with `renderCertificateList(..., 422, errs, spec)`, which
`Respond`s the partial **`certificate_list_panel`** — `#certificate-list`,
`partials/certificates.html:10-11`. The create form that renders `.Errors` and
re-fills `.Spec` is **`certificate_form`**, a different `{{define}}`, included
separately by the page (`pages/certificate_list.html:47,50`). It is not inside the
partial its own refusal re-renders.

`TeamCreate` is the same shape: 422 → `team_list_panel` (`#team-list`), create form at
`pages/team_list.html:39`, outside it.

Today the operator sees a mangled list panel nested inside the form — wrong, but it
at least *says something happened*. Add `hx-target="#certificate-list"
hx-swap="outerHTML"` and the list panel replaces cleanly, the form is never touched,
and **the refusal becomes invisible**: nothing moves, the typed values sit there, no
error appears. That is worse than the bug being fixed. It is also the exact failure
`app.js`'s comment was written about — "the server does the right thing and the
operator sees nothing happen at all" — reintroduced by the fix for it.

Compounding it: `certificate_form` has no wrapping id (`partials/certificates.html:64`
is a bare `<div class="panel">`), so there is nothing correct to target even if the
handler cooperated.

**These two need a decision, not a markup sweep** — see Open questions 1. They should
not ride in the same commits as the mechanical ones.

### (b) A 422 whose body is one sentence, not a form

`handleStoreError` (`internal/web/handlers/app.go:441-442`) maps `domain.ErrInvalid`
to **422 via `http.Error`** — body `"That request was not valid."`, plain text.
`app.js` force-swaps on **status**, not on body, so that sentence is swapped in like
any partial.

Untargeted, it wipes the form's insides. **Targeted at a panel, it replaces the whole
panel with one line of text.** For the retire forms — most of the 33 — that panel is
the page's main content. So for this class of refusal, declaring a target strictly
*enlarges* the damage.

This does not change the recommendation: today's behaviour is not acceptable either,
and an operator who sees their form replaced by a bare sentence at least learns that
the request was refused. But it is a real cost of the change, it is measurable, and
it is the strongest argument in the room for handling the shared `ErrInvalid` path as
part of this work rather than after it. Open questions 2.

### (c) A target id that is not on the page at render time

If the named id is inside a conditional the element is not (`{{if .IsAdmin}}`,
`{{if .Certificates}}`), htmx finds nothing and does not swap — silent again. Every
target chosen has to be an id present on every render that can show the element.
Checkable by eye within a file; needs judgement across files.

### What would be true if this were broken, and what shows it is not

Breakage here has exactly two visible signatures. Either **a refusal produces no
visible change**, or **the DOM ends up holding two elements with the target id**.
Both are cheap to assert and neither is what a diff review looks at.

So the evidence is per-surface and behavioural, on the surfaces with a *reachable*
refusal — certificates create, team create, team reassign-retire, health override
clear, and identity rotation as the already-fixed control. For each: drive a real
refusal in a browser and assert (i) the field error is in the DOM, (ii) the typed
value is still in the input, (iii) `document.querySelectorAll('#<target>').length === 1`.
Not all 38 — the ones whose handler can actually refuse.

Plus the static census, plus the mutation check CLAUDE.md requires: remove one
element's `hx-target`, watch the census go red, restore, and say so in the PR body.
A census that has never been observed failing is a claim, not a check.

## Enforcement — a new test, not an extension

`internal/web/edit_form_version_test.go`'s `TestEveryEditFormCarriesItsVersion` is the
right precedent and the wrong host. Its own header makes this work's case for static
scanning better than this document could:

> Driving every edit form through the browser would need a fixture in the right state
> for each of thirty-odd routes, and the ones that are hardest to set up are the ones
> most likely to be missed — so the coverage would thin out precisely where it
> matters.

That holds here identically, and more strongly: several of the 38 are retire forms on
entities that need a live fixture in a specific state before a refusal is even
reachable.

**A separate file and a separate test**, for three reasons:

- **Different population.** That test derives its population from *routes whose
  handler reaches `submittedVersion`*, then joins forms to routes. This one's
  population is *every `hx-post` element in the template tree* — buttons included,
  unversioned retire routes included, elements the route census would never name.
  ~90 against ~30-odd, overlapping only in part.
- **Different failure.** That test protects an optimistic-concurrency guard; this
  protects where a response lands. One file would hold two unrelated exemption maps
  argued on different grounds, and a reader hitting a failure would have to work out
  which rule they broke.
- **Different matching.** `matchRoutes` and its asymmetric-wildcard rule exist
  *because* that test joins templates to routes. This census joins nothing: an element
  either declares the attributes or it does not. Importing that machinery would
  import complexity this problem does not have.

**What it must reuse, and this is the part that matters more than the file layout:**

- **A positive control.** Every census in this repo has one — *"a scan that matches
  nothing reports success."* If the element count comes back under ~80, the scan has
  stopped matching the markup and the test must `Fatal`, not pass.
- **Glob per subdirectory, not `filepath.Walk`** — gosec G122 rejects a file read
  inside a Walk callback, and `respond_partial_test.go` and the version census both
  already made this swap.

Suggested, not mandated: `internal/web/hx_target_test.go`,
`TestEveryPostingElementDeclaresItsSwapTarget`.

**The second half — whether the census also proves the named id exists anywhere in
the tree — is a decision, not a given.** It catches failure mode (c) cheaply, and it
is the same "other direction" discipline that keeps the version census honest. It also
needs both sides normalised (`#user-row-{{.User.ID}}` → `#user-row-*`) the way
`matchRoutes` normalises actions, and it will not catch a conditional id. Open
questions 3.

## Sequencing — fix first, census last, census starts empty

**Against census-first with 38 exemptions.** It would make the rule visible
immediately, which is the genuine case for it. But a 38-entry exemption map is not a
backlog in this repo's sense. `versionExemptRoutes` is *empty* and says "empty is the
goal"; `unreachableRepairPaths` held single figures and existed to keep a known gap
visible until the work that emptied it. Those maps hold **argued** entries — the
comment demands it: *"an entry is a claim that this particular write does not need
the guard, and it has to be argued rather than assumed."* Thirty-eight unargued
entries would make that sentence false on arrival. And the deeper problem: a census
passing with 38 exemptions is **green**, and green is what people read. The signal
would be inverted on day one.

**Against one 38-element commit.** Also real. Each fix requires identifying the
partial the handler re-renders on 422 — not mechanical, as (a) proves — and no
reviewer checks 38 of those in one sitting. A 5,000-line commit is already a failure
of the implement stage under CLAUDE.md's own rule.

**Resolution: fix in surface-sized commits, census last and empty.** The natural unit
is the template file, because a file's elements share a partial and therefore share a
target — `partials/teams.html`'s five all resolve to `#team` or
`#team-retire-confirm`; `pages/net_group_detail.html`'s four share the group detail
partial. That is roughly fifteen commits, each reviewable alone, each with its
reasoning in the markup the way `identities.html` already carries it. The census is
the final commit and cannot pass until the others have landed. The "new violations
slip in meanwhile" window is one branch, not months.

The certificates/teams class from (a) is lifted out and decided separately. It is a
handler question wearing a template question's clothes.

## Success criteria

1. Every `hx-post` element in `web/templates` declares `hx-target` **and** `hx-swap`
   on itself. The census counts 0 without, and its exemption map is `{}`.
2. The census carries a positive control that fails if it matches fewer than ~80
   elements.
3. The census has been **observed failing**: one element's `hx-target` removed, test
   red, restored — recorded in the PR body, not asserted.
4. On every surface with a reachable refusal, a browser-driven refusal shows the field
   error and the operator's typed value, and the DOM holds exactly one element with
   the target id.
5. **No refusal is silent.** For each of the 38, the partial the handler re-renders on
   422 contains the error state the element's form renders. Where it does not
   (certificates, teams — at minimum), that is fixed, or deferred with the route named
   and the reason written down.
6. `make test` green on **both** engines. `gofmt`, `go vet`, `staticcheck` clean. No
   new dependency. AGPL-3.0-only header on the new test file, blank line before
   `package`.

## Open questions — answer before planning

1. **The certificates/teams silent-refusal class.** Two shapes are available: the
   handler re-renders a partial that contains the form, or the form becomes its own
   `{{define}}` with a stable id that the handler names. Both are more than markup;
   the second adds a partial name per form. Which — and is it inside this work or
   split out ahead of it? (It must be decided *before* those two forms get a target,
   because targeting them alone makes the failure invisible.)
2. **`handleStoreError`'s `ErrInvalid` → 422 with a plain-text body.** Declaring
   targets turns this from "form contents wiped" into "whole panel replaced by one
   sentence". Is changing that path in scope, out of scope, or a separate decision?
   This is arguably an architecture call about what a 422 means in this codebase.
3. **Does the census also verify each named id exists in the tree?** Worth it for
   failure mode (c); costs a second normalised scan and will not catch a conditional
   id.
4. **Shared partials serving many hosts.** `partials/journal.html:48` is one form
   serving eight routes (the version census's `matchRoutes` comment records this), and
   `partials/costs.html`'s three elements post to `{{$.Action}}`. Can each name a
   stable id the partial itself owns, or does the id have to arrive through the dict?
   Probably the former; it is the one place it might not be.
5. **Is ~80 the right positive-control floor**, given the population is 90 today and
   will grow?

## Not this work

**Widening `app.js`'s force-swap beyond 422.** 409 is the obvious candidate — every
versioned edit form can return one, and so can `handleStoreError`'s `ErrConflict`
branch — and it is exactly the change that would silently re-arm any status-based
exemption, which is why the exemption criterion above refuses to depend on it. It also
changes the behaviour of all 90 elements at once and needs its own evidence. Nothing
here makes it harder; nothing here should be read as a step towards it.

**Plain `method="post"` forms with no `hx-post`.** They are outside this census
entirely and they are correct by a different route: they perform a full page
navigation and never swap. `partials/health.html:143`'s amend form is one. Named here
so the next reader does not think they were missed. Their count is unmeasured.

**Redesigning which partial each handler re-renders on 422, in general.** Only the two
surfaces where targeting alone inverts the failure are in scope, and only because
targeting them without deciding is actively harmful.

**Out-of-band flash coordination**, `hx-boost`, and any move away from
`HX-Redirect`-on-success. `render.Redirect` makes success navigate the whole page
regardless of target and swap, which is why this document is about refusals only.

**A behavioural test per element.** Argued above: the ones hardest to set up are the
ones most likely to be missed.

## Global constraints

CLAUDE.md's, in full, and binding rather than background:

- **Server-rendered HTML only. No JSON to the UI.** Handlers return HTML fragments.
- Stack locked: `net/http.ServeMux`, `html/template` embedded via `go:embed`, HTMX
  2.x and Alpine.js 3.x vendored into `web/static` — never a CDN.
- **Alpine is local UI state only** — dropdowns, modals, tab selection,
  disable-on-submit. It does not fetch, does not hold domain state, does not talk to
  the server. No inline `<script>` beyond `x-data`.
- **No business logic in a template.** Escaping is `html/template`'s job; never
  concatenate HTML, never `template.HTML` on anything derived from user input.
- **A partial must be renderable standalone.** If it only works once its parent has
  rendered, it is wrong. (Rule 2 above is a direct consequence.)
- **Validation failure returns HTTP 422 with the form partial re-rendered** — never a
  200 with an error buried in the body.
- Every mutating handler branches on `HX-Request` through `render.Respond`; success
  redirects with `HX-Redirect`, never a 302.
- **Every non-GET route is behind CSRF and `RequireWrite`** (or
  `RequireAdministrator` where the surface genuinely needs one). Nothing in this work
  adds, removes or moves a route.
- **AGPL-3.0-only header on every new file** (`.go`, `.html`, `.css`, `.js`), with a
  **blank line** before the package clause in Go or the licence becomes the package
  doc. `internal/license` fails otherwise.
- **No new dependency.**
- **`make test` green on both engines.** `go test ./...` alone silently skips the
  Postgres half.
