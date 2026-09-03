# Two-ended UI (link and dependency) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development.

**Goal:** Surface in the browser the two-ended write capability the store already grants. A project owner who owns BOTH ends of a dependency or a cable can currently do so only by direct POST; no control is ever rendered for them.

**Architecture:** No new authorization rule. Every control is gated on exactly the predicate the store enforces — `permit.Covers(...)` on BOTH subjects — reached through `Base.CanWriteEntity`. Option pickers are filtered in Go with that same predicate, so a picker and the rule it reflects cannot drift.

**Spec:** merged `docs/ROADMAP.md` follow-up; `docs/rbac-design.md` §4. User rulings, 2026-09-02: **filter** the create pickers to in-scope options (do not offer-and-refuse), and do all three pieces in one branch.

## Global Constraints

- `?` placeholders, `sqlx.Rebind`, both engines unmodified. No Postgres-only features.
- Declared-state mutation writes a `change_log` row in the same transaction. Soft delete only.
- `internal/domain` has zero external dependencies. `domain.Permit` stays width-locked to three methods.
- Validation failure → 422 with the form partial re-rendered; branch on `HX-Request` via `render.Respond`.
- Alpine is the CSP build: `x-data` names a registered component, values arrive as `data-*`. **No inline `on*=` handlers** — a test now forbids them outright.
- AGPL header on new files, blank line before `package`.
- `make lint` is the gate (golangci-lint), not `staticcheck` alone. `make test` runs both engines.

### THE ONE THING THAT MUST NOT BREAK

`depRowData.SecretRef` is gated on **isAdmin**, and that is deliberately NARROWER than the new
two-ended CanWrite. `secretRefDisplay` returns `domain.Redacted` to anyone who is not a full
Administrator, because `identity.secret_ref` is a Vault path and CLAUDE.md forbids it reaching a
non-administrator. **Do not fold SecretRef into the new predicate.** A project owner who owns both
ends of a dependency gains the Edit/Retire/Verify controls and MUST NOT gain the secret ref.
There is an existing test for this; if there is not, add one.

---

### Task 1: dependency row controls

**Files:** `internal/web/handlers/forms.go`, `internal/web/handlers/deps.go`, `web/templates/partials/rows.html`.

`DependencyRow.ProviderSvc` is ALREADY populated (`dependencySelect` resolves it through
`endpoint→service` and `route→frontend_endpoint→service`, the same derivation
`authorizeDependencySubjects` performs). **No store change is needed.** `depRowData`'s current
comment claims otherwise and is false — correct it, do not preserve it.

- [ ] Change `depRows` to take the caller's write predicate as well as `isAdmin`, and set
      `CanWrite` = covers `service`/`ConsumerServiceID` AND covers `service`/`ProviderSvc`.
      Keep `SecretRef` on `isAdmin`. Update `DependencyVerify`'s single-row render the same way.
- [ ] Tests: a project owner owning both ends sees the controls; owning one end does not; owning
      neither does not; an Administrator always does; and **a project owner who sees the controls
      still gets `domain.Redacted` for the secret ref**.
- [ ] Mutation: make `CanWrite` one-ended, watch the one-end case go red. Restore.

### Task 2: unpatch (retire a link)

**Files:** `internal/store/network.go`, `web/templates/pages/asset_detail.html`.

`InterfaceRow.PeerAsset` is a NAME. Add `PeerAssetID` (`peer_asset_id`) to the same query that
already resolves the peer — one column, no new round trip.

- [ ] Gate Unpatch on `CanWriteEntity "asset" $.Asset.ID` AND `CanWriteEntity "asset" .PeerAssetID`.
- [ ] `asset_detail.html:244`'s comment says link "is ScopeTopology and stays Administrator-only".
      False since `438c848`. Rewrite it to describe the two-ended gate.
- [ ] Tests including an unpatched port (no peer) and a cable to a foreign asset.
- [ ] Mutation: drop the peer half of the gate, watch a named test go red.

### Task 3: filtered create pickers

**Files:** `internal/store/deps.go` (`routeSelect`), `internal/web/handlers/forms.go`,
`web/templates/partials/forms.html`.

**Filter in Go using `permit.Covers`, not in SQL.** `domain.Permit` exposes no enumerable set —
`Covers` is the only accessor — and using it means the picker and the store rule are the same
predicate and cannot disagree. Option lists here are small.

- [ ] `RouteRow` carries `frontend_service` as a CODE. Add `fs.id AS frontend_service_id` and a
      `FrontendServiceID` field.
- [ ] Filter `.Targets` by `Covers("asset", opt.AssetID)` (`InterfaceOption` embeds
      `domain.Interface`, so `AssetID` is already there).
- [ ] Filter `.AllEndpoints` by `Covers("service", ep.ServiceID)`.
- [ ] Filter `.AllRoutes` by `Covers("service", rt.FrontendServiceID)`.
- [ ] Gate each create form on the near end the operator is already on.
- [ ] When filtering empties a picker, say so in the existing field-hint style — an empty dropdown
      with no explanation reads as a broken page.
- [ ] Tests: a project owner's pickers contain only their own estate; an Administrator's are
      unchanged; **and the server still refuses a forged out-of-scope submission** — filtering is a
      courtesy, never the enforcement.
- [ ] Mutation: remove each filter, watch a named test go red.

### Task 4: E2E

**Files:** `tests/e2e/specs/`.

Read `docs/E2E.md` first. Fixture needs `INV_SEED=true` and `INV_SEED_E2E_PROJECT_OWNER=true`;
never against a shared deployment; fresh browser context for mutating specs.

- [ ] A project owner cables two ports on their own assets **through the UI**, and unpatches one.
- [ ] The far-end picker does not list a foreign asset's ports.
- [ ] A project owner declares a dependency between two services they own, through the UI.
- [ ] An Administrator's pickers still list the whole estate.
- [ ] **No runtime skips.** A missing fixture row fails loudly.
- [ ] Prove a new spec can fail.

## Final gate

- [ ] `make lint` — 0 issues
- [ ] `make test` — exit 0 on BOTH engines, verified per-subtest, run alone
- [ ] Full Playwright suite green
- [ ] Evidence: what would be true if this were broken, and what was run to show it isn't
