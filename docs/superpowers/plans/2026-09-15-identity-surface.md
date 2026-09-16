# Identity surface — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** An operator can declare a credential reference, correct it, withdraw it, and — the point of the work package — **record that it was rotated**. `rotation_days` and `last_rotated` stop being columns nothing can write and become the estate finding that says which credentials are past their own declared rule.

**Architecture:** One migration adding `row_version` and a date-shape `CHECK`. The domain type gains a spec-taking constructor and a five-valued `RotationStatus` replacing a two-valued boolean that answered `false` for every row in the demo estate. `last_rotated` acquires exactly one writer, `RecordIdentityRotation`, enforced by an AST scan in the shape of `TestTheOnlyFactDeletingStatementIsThePrune`. Two server-rendered pages, four `writeAdminOnly` POSTs, three findings folded in Go from the list page's own query.

**Tech Stack:** Go 1.26, `jmoiron/sqlx` with hand-written SQL, `pressly/goose/v3` migrations (dialect-split), `html/template` + HTMX, SQLite (`modernc.org/sqlite` v1.54.0) and PostgreSQL.

**Spec:** `docs/identity-surface-design.md` — **the binding authority. Where this plan and the spec disagree, the spec wins.**

## Global Constraints

Copied verbatim from the spec's "Global constraints" section. Every task's requirements implicitly include these.

- `?` placeholders only, `sqlx.Rebind` before execution, every query runs
  unmodified on **both** engines; `make test` green on SQLite **and** Postgres —
  `go test ./...` alone silently skips the Postgres half.
- UUIDv7 `TEXT` ids and RFC3339 UTC `TEXT` timestamps generated **in Go, never in
  SQL**; `last_rotated` is a `YYYY-MM-DD` date, also generated in Go.
- Enums are `TEXT` + `CHECK` plus a matching Go constant set.
- **Every declared mutation writes a `change_log` row in the same transaction.**
  Since WP-G1 this is an authorization invariant, not only an audit rule: a
  declared mutation that does not log is an unauthorized change that succeeded.
- **Soft delete only.** `lifecycle = 'retired'`, never a `DELETE`.
- Constructors validate; the DB `CHECK` is the second line of defence.
- Validation failure returns **HTTP 422** with the form partial re-rendered —
  never a 200 with an error buried in the body.
- Every non-GET route is behind **CSRF** and `RequireWrite` (here:
  `RequireAdministrator`, argued above).
- **AGPL-3.0-only header on every new file** (`.go`, `.sql`, `.html`), with a
  **blank line** before the package clause in Go or the licence becomes the
  package doc. `git add` before the licence scan.
- **No new dependency.**

Operational additions, from `CLAUDE.md` and this repo's own build notes:

- Postgres for local runs: `INV_TEST_POSTGRES_DSN="postgres://invctl:invctl@127.0.0.1:5433/invctl?sslmode=disable"`, container via `make compose-up`. `make test` sets it itself.
- `make lint` needs `/usr/local/go/bin` and `$HOME/go/bin` on PATH.
- **Capture `make test`'s exit code directly, never through a pipeline** — the status you read from `make test | tail` is `tail`'s, and a red gate reports green.
- Commit per task, message saying WHY, ending with `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`.

## File Structure

**Create:**
- `internal/store/migrations/sqlite/00069_identity_row_version.sql`
- `internal/store/migrations/postgres/00069_identity_row_version.sql` — same statements, differing only in the header comment about what had to be measured
- `internal/store/identities.go` — the identity read/write surface, moved out of `deps.go`
- `internal/store/identities_test.go`
- `internal/store/last_rotated_source_test.go` — the one-writer AST guard
- `internal/store/rotation_findings.go`
- `internal/web/handlers/identities.go`
- `internal/web/identities_test.go`
- `web/templates/pages/identity_list.html`
- `web/templates/pages/identity_detail.html`
- `web/templates/partials/identities.html`

**Modify:**
- `internal/domain/endpoint.go` — `Identity`, `IdentitySpec`, `NewIdentity`, `Validate`, `RotationState`, delete `RotationOverdue`
- `internal/domain/classification.go:307-310` — `row_version` joins the `"identity"` list
- `internal/store/deps.go:871-908` — `CreateIdentity` and `ListIdentities` move out; `realmOrEmpty` moves with them
- `internal/store/bulk_ownership.go:298-304` — the identity branch bumps
- `internal/store/team_reassignment.go:120-129, 210-226` — same, and the comment is amended
- `internal/store/write_surface_test.go:138-159` — `writeSurfaceUnbuilt` emptied
- `internal/store/findings.go` — register the rotation findings
- `internal/web/routes.go` — two reads, four `writeAdminOnly` writes
- `internal/web/handlers/nav.go` — an "Identities" entry
- `internal/web/handlers/deps.go:238`, `internal/web/handlers/services.go:340` — `ListIdentities` call sites
- `internal/web/routescan/testdata/write_routes.txt` — regenerate, never hand-edit
- `internal/web/rbac_boundary_test.go:1216, 930` — the two pinned counts
- `internal/web/detail_pages_render_test.go:47-62` — an `identity` row
- `internal/seed/seed.go:888-915` — `b.identities()`, and a new `b.identityHistory()` phase
- `docs/ownership-report-design.md` §4 — amended as superseded
- `docs/AUDIT.md` — the classification table gains `identity.last_rotated`, `identity.row_version`
- `docs/ROADMAP.md` — WP-J8

---

### Task 1: The column, the classification, and the two writers that must bump it

The spec says three things move in one commit and names them: the migration, the two bulk-ownership branches, and the amendment to `docs/ownership-report-design.md` §4. This task is those three plus the two things that make the tree compile: `domain.Identity` needs the field, because `s.read` is **not** `sqlx.Unsafe` (`internal/store/store.go:362`) and `SELECT * FROM identity` into a struct with no `row_version` field fails with `missing destination name row_version` the moment the migration lands.

**Files:**
- Create: `internal/store/migrations/sqlite/00069_identity_row_version.sql`, `internal/store/migrations/postgres/00069_identity_row_version.sql`
- Modify: `internal/domain/endpoint.go` (the struct only), `internal/domain/classification.go`, `internal/store/deps.go`, `internal/store/bulk_ownership.go`, `internal/store/team_reassignment.go`, `docs/ownership-report-design.md`, `docs/AUDIT.md`
- Test: `internal/store/identities_test.go` (new)

**Interfaces:**
- Produces: `domain.Identity.RowVersion int \`db:"row_version"\``
- Changes: `CreateIdentity` inserts `row_version` explicitly **and drops `last_rotated` from its column list** (see Task 3's one-writer rule; doing it here means the AST guard has nothing to find when it arrives)

- [ ] **Step 1: Re-measure `ALTER TABLE … ADD CONSTRAINT … CHECK` against the pinned driver**

`00011_eol.sql`'s header records that this was measured against `modernc.org/sqlite` v1.54.0 / SQLite 3.53.3. `go.mod:16` still pins v1.54.0, so the claim is *probably* still true — **do not trust it, re-run it.** Non-standard SQLite DDL is exactly the thing that changes under you.

```bash
mkdir -p /tmp/claude-1000/measure && cd /tmp/claude-1000/measure
cat > main.go <<'EOF'
package main

import (
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
)

func main() {
	db, err := sql.Open("sqlite", "file:m.db?mode=memory")
	must(err)
	var v string
	must(db.QueryRow(`SELECT sqlite_version()`).Scan(&v))
	fmt.Println("sqlite_version:", v)

	must2(db.Exec(`CREATE TABLE identity (id TEXT PRIMARY KEY, last_rotated TEXT)`))
	must2(db.Exec(`INSERT INTO identity (id, last_rotated) VALUES ('a', NULL)`))
	must2(db.Exec(`ALTER TABLE identity ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1`))
	must2(db.Exec(`ALTER TABLE identity ADD CONSTRAINT identity_last_rotated_check
	  CHECK (last_rotated IS NULL OR (length(last_rotated) = 10
	         AND substr(last_rotated, 5, 1) = '-' AND substr(last_rotated, 8, 1) = '-'))`))

	var ddl string
	must(db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'identity'`).Scan(&ddl))
	fmt.Println("ddl carries the constraint:", ddl)

	if _, err := db.Exec(`INSERT INTO identity (id, last_rotated) VALUES ('b', '2026-9-8')`); err == nil {
		panic("a malformed date was ACCEPTED: the CHECK is not enforcing")
	} else {
		fmt.Println("malformed rejected:", err)
	}
	must2(db.Exec(`INSERT INTO identity (id, last_rotated) VALUES ('c', '2026-09-08')`))
	var rv int
	must(db.QueryRow(`SELECT row_version FROM identity WHERE id = 'a'`).Scan(&rv))
	fmt.Println("existing row backfilled to row_version:", rv)
}

func must(err error)                  { if err != nil { panic(err) } }
func must2(_ sql.Result, err error)   { if err != nil { panic(err) } }
EOF
cat > go.mod <<'EOF'
module measure

go 1.26.6
EOF
GOFLAGS=-mod=mod go mod tidy && go run .
```

Expected: a version line, a DDL string containing `identity_last_rotated_check`, a rejection of `2026-9-8`, and `row_version: 1` on the pre-existing row. **If any of that fails, stop and raise it** — the fallback is a table rebuild in the SQLite half, which is a different migration and a different conversation.

Record the measured version in the SQLite migration's header, replacing 00011's numbers with today's.

- [ ] **Step 2: Write the migration, both engines**

`internal/store/migrations/sqlite/00069_identity_row_version.sql`:

```sql
-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- identity acquires the optimistic-concurrency token, for the reason 00066 gave
-- link: it is growing a CORRECTION PATH (WP-J8), and two operators fixing the
-- same credential's realm at the same time must not silently overwrite each
-- other. link had likewise never carried one; DEFAULT 1 plus a migration is how
-- link answered the drift objection and is how this one does.
--
-- THIS SUPERSEDES docs/ownership-report-design.md §4 FOR A NEW CIRCUMSTANCE, and
-- does not retract it. That section refused a token for BULK REASSIGNMENT, where
-- `UPDATE ... WHERE team_id IS NULL` is itself the atomic eligibility check and a
-- version would add nothing -- which remains true, and the guards in
-- bulk_ownership.go and team_reassignment.go are UNCHANGED by this migration.
-- The condition §4 set was "do not add a token you are not going to maintain
-- everywhere", and nobody was offering to at the time. WP-J8 pays that condition:
-- there are exactly three statements that write identity in this codebase and all
-- three now maintain the column. A fourth must too, or the token stops being one.
-- SIGNED OFF by Gabriel, 2026-09-15.
--
-- NO created_at AND NO updated_at, and that is the decision rather than the
-- omission. 00066 is the precedent and it is exact: link also grew a correction
-- path and got row_version ONLY. A backfill would have to invent a creation date
-- for every existing row, and a fabricated date in a CMDB is worse than none.
-- change_log already answers both questions precisely and permanently -- the
-- create entry is when it was declared, the newest entry is when it last changed,
-- and both carry who. And this table's meaningful timestamp is last_rotated,
-- which is a FACT ABOUT THE CREDENTIAL; an updated_at beside it would be a
-- second, weaker date that invites the wrong reading at 03:00.
--
-- last_rotated gets a DATE SHAPE CHECK, exactly as 00011 gave eol_date. `length`
-- and `substr` are the two string functions both engines agree on; a real parse
-- happens in Go, where domain.ParseDate rejects 2027-02-31 and this cannot. Named,
-- per the 2026-07-29 rule: an unnamed inline constraint is one of the exactly
-- three shapes SQLite cannot alter later.
--
-- THE CHECK VALIDATES EXISTING ROWS AND THAT IS SAFE HERE BY CONSTRUCTION: no
-- code path has ever written last_rotated, so it is NULL in every deployment. A
-- hand-edited database with a malformed value fails the migration loudly, which
-- is the correct outcome.
--
-- RE-MEASURED against the pinned driver on 2026-09-15 (modernc.org/sqlite
-- vX.YY.Z, SQLite A.BB.C -- SUBSTITUTE THE NUMBERS STEP 1 PRINTED): ADD COLUMN
-- then ADD CONSTRAINT ... CHECK both succeed, the constraint validates existing
-- rows, a malformed date is rejected, and the resulting DDL carries the
-- constraint with no table rebuild. 00011 recorded the same measurement and its
-- own header says to re-take it rather than trust the line; this is that.
--
-- idx_identity_team COMES BACK, shaped for the query that now exists. 00016
-- dropped it and named this moment: "When the team page grows an identities
-- section it can come back, shaped for whatever that query turns out to be
-- rather than guessed at now." The list page (WP-J8) filters on team and
-- excludes retired rows, so the index carries lifecycle as idx_asset_team does.

-- +goose Up
ALTER TABLE identity ADD COLUMN row_version INTEGER NOT NULL DEFAULT 1;

ALTER TABLE identity ADD CONSTRAINT identity_last_rotated_check
  CHECK (last_rotated IS NULL OR (length(last_rotated) = 10
         AND substr(last_rotated, 5, 1) = '-' AND substr(last_rotated, 8, 1) = '-'));

CREATE INDEX idx_identity_team ON identity(team_id, lifecycle) WHERE team_id IS NOT NULL;

-- +goose Down
DROP INDEX idx_identity_team;
ALTER TABLE identity DROP CONSTRAINT identity_last_rotated_check;
ALTER TABLE identity DROP COLUMN row_version;
```

`internal/store/migrations/postgres/00069_identity_row_version.sql`: **byte-identical statements**. The header differs in exactly one paragraph, replacing the re-measurement note with 00011's postgres counterpart wording:

```
-- Both statements are ordinary DDL here. The SQLite half is the one that had to
-- be measured, and it turned out to accept exactly the same three statements --
-- so the two files differ in their comments and nowhere else.
```

- [ ] **Step 3: Add the struct field and classify it**

`internal/domain/endpoint.go`, on `Identity`, after `Lifecycle`:

```go
	Lifecycle    string  `db:"lifecycle"`
	// RowVersion is the optimistic-concurrency token, migration 00069. See
	// internal/domain/version.go. auditFields (internal/store/diff.go:80-86)
	// excludes it from every diff: "an audit entry reading row_version: 4 -> 5
	// tells a reader nothing they can use".
	RowVersion int `db:"row_version"`
```

`internal/domain/classification.go:307-310`:

```go
	"identity": {
		"id", "kind", "name", "realm", "secret_ref", "rotation_days",
		// last_rotated is DECLARED and the naming is the trap this table exists
		// for: it reads like a fact the estate reports about itself and it is
		// not. Somebody rotated a credential and somebody typed the date. No
		// monitoring credential may write it -- a machine that could would be
		// able to silence an overdue finding for a rotation that never happened
		// (docs/AUDIT.md rule 6, and the spec's backdate/post-date asymmetry).
		"last_rotated", "team_id", "lifecycle",
		// Bookkeeping, declared under this file's own rule above: an observed
		// writer may touch none of it.
		"row_version",
	},
```

`docs/AUDIT.md`, in the column-classification table beside the `asset.eol_date` row:

```
| `identity.last_rotated`, `identity.row_version` | **declared** | when a credential was last rotated, migration `00069` (WP-J8). Reads like telemetry and is not: somebody rotated it and somebody typed the date, exactly as `asset.eol_date` is somebody reading a contract. It has **exactly one writer**, `RecordIdentityRotation`, enforced structurally by `internal/store/last_rotated_source_test.go` — a create form that also stamped a date would bury the rotation inside a create snapshot where no reader will find it, and an agent that could write it could hide an overdue finding for a rotation that never happened. `row_version` is bookkeeping, declared alongside the fields it guards |
```

- [ ] **Step 4: `CreateIdentity` writes the token and stops writing the date**

`internal/store/deps.go:887-899`. Two changes to one statement:

```go
func (s *SQLStore) CreateIdentity(ctx context.Context, p domain.Permit, i *domain.Identity) error {
	return s.write(ctx, p, func(t *tx) error {
		// last_rotated IS DELIBERATELY ABSENT from this column list and must
		// stay absent. It has exactly one writer, RecordIdentityRotation
		// (WP-J8, docs/identity-surface-design.md): declaring a credential that
		// already exists and recording when it was last rotated are TWO ACTS
		// and two audit entries, both of which are true. A create that also
		// stamped the date would bury the rotation inside a create snapshot
		// where no reader looking for a rotation will ever find it.
		// internal/store/last_rotated_source_test.go fails on any other writer.
		//
		// row_version is written explicitly rather than left to DEFAULT 1, so
		// the Go struct and the row agree from the first read -- the same shape
		// every other create in this package uses.
		_, err := t.exec(ctx, `
			INSERT INTO identity (id, kind, name, realm, secret_ref, rotation_days,
			                      team_id, lifecycle, row_version)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			i.ID, i.Kind, i.Name, realmOrEmpty(i.Realm), i.SecretRef, i.RotationDays,
			i.TeamID, i.Lifecycle, i.RowVersion)
		if err != nil {
			return translateWriteErr(err, "creating identity")
		}
		return t.logCreate(ctx, "identity", i.ID, i)
	})
}
```

And `NewIdentity` (still positional at this task) sets it, so `i.RowVersion` is never a zero that fails the `NOT NULL` default's intent:

```go
	return &Identity{ID: id, Kind: kind, Name: name, Lifecycle: LifecycleActive, RowVersion: 1}, nil
```

- [ ] **Step 5: Both bulk writers bump, and keep every guard they have**

`internal/store/bulk_ownership.go:298-304` — replace the `case "identity":` body:

```go
	case "identity":
		// THE GUARD IS UNCHANGED AND IS STILL THE WHOLE ELIGIBILITY CHECK.
		// `WHERE team_id IS NULL` is what produces the per-item
		// assigned / no_longer_unowned outcome the ownership report argues for
		// on its own merits -- "All-or-nothing would punish the operator for
		// someone else's correctly-made edit". DO NOT "simplify" this into a
		// row_version comparison: that silently converts a skip-and-report into
		// a 409, which is the opposite behaviour for the same event.
		//
		// THE BUMP IS ADDITIVE, migration 00069. Without it a bulk assignment
		// changes team_id under an open correction form whose token still
		// validates, and the form's save silently reverts the assignment.
		// Still no updated_at: identity has none, by 00069's decision.
		do = func(t *tx) (sql.Result, error) {
			return t.exec(ctx,
				`UPDATE identity SET team_id = ?, row_version = row_version + 1
				 WHERE id = ? AND team_id IS NULL`, toTeamID, id)
		}
```

`internal/store/team_reassignment.go:212-226` — same shape, guard `AND team_id = ?` kept:

```go
	// identity carries no updated_at (00003) but now carries row_version
	// (00069, WP-J8's correction path). The guard below is UNCHANGED and is
	// still the whole eligibility check -- see the function doc; the bump is
	// additive, so an open correction form's token stops validating after a
	// reassignment moved the row underneath it.
	...
			return t.exec(ctx,
				`UPDATE identity SET team_id = ?, row_version = row_version + 1
				 WHERE id = ? AND team_id = ?`,
				toTeamID, c.ID, fromTeamID)
```

And amend the function doc at `internal/store/team_reassignment.go:120-129`, replacing the parenthetical that currently reads *"this was decided against a first draft that gave identity a row_version it had never had, and that decision is not to be revisited here"*:

```go
// EACH UPDATE IS GUARDED BY THE CONDITION THAT MADE IT ELIGIBLE --
// `WHERE team_id = fromTeamID` (or `owner_team_id` for custom_field) -- never
// by row_version, even for asset, service, project and now identity, all of
// which carry one. Zero rows affected means the entity is no longer this
// team's, which is ReassignStale, not an error. custom_field needs no
// row_version to make this atomic and neither does identity: the same guard is
// the whole eligibility check either way (design §4).
//
// identity ACQUIRED a row_version in migration 00069 (WP-J8), and the
// statements here bump it -- but they still do not GUARD on it, for the reason
// above. design §4 refused the column for THIS path and that refusal stands
// unchanged; what changed is that identity grew a correction form elsewhere,
// which is the circumstance 00066 added a token to `link` for. The condition
// §4 set was "do not add a token you are not going to maintain everywhere";
// WP-J8 pays it, and this is one of the three places paying it.
```

- [ ] **Step 6: Amend `docs/ownership-report-design.md` §4**

Replace the paragraph beginning **"`identity` has no `row_version`"** (currently at line 112) with — and read the spec's "Amend §4 to say superseded, never 'wrong'" section before editing, because the wording is the point:

```markdown
**`identity` had no `row_version` when this was written, and the bulk-assignment
guard still does not use one.** The first draft preferred adding one for *this*
path. That was the wrong tool for this job and the review was right to refuse it:
`UPDATE ... WHERE team_id IS NULL` is itself an atomic eligibility check, zero
rows affected IS the "no longer unowned" outcome, and a token here would convert
a skip-and-report into a 409 — the opposite behaviour for the same event. That
reasoning is unchanged and this paragraph is not a retraction of it.

The refusal carried a condition: **do not add a token you are not going to
maintain everywhere.** Nobody was offering to maintain one at the time, so no
was the right answer. **WP-J8 pays that condition and the column now exists**
(migration `00069`) — not for bulk assignment, but for the **correction form**
the identity surface adds, which is the exact circumstance `00066` added a token
to `link` for: an entity that had likewise never had one, growing a repair path,
where "two operators correcting the same row at the same time silently overwrite
each other". There are exactly three statements that write `identity` in this
codebase and all three now maintain the column — `CreateIdentity` inserts `1`,
and both bulk branches do `row_version = row_version + 1` **beside** their
existing `WHERE` guards, which are untouched. Adding a token to *this* path
would still be wrong. SIGNED OFF by Gabriel, 2026-09-15.
```

- [ ] **Step 7: Write the failing tests**

New file `internal/store/identities_test.go`. Licence header, blank line, `package store`.

```go
// TestTheIdentityDateShapeCheckIsEnforcedByBothEngines is 00069's second
// statement, asserted rather than assumed. The Go constructor is the first line
// of defence and this is the second; a shape check that silently does nothing on
// one engine is the portability failure this suite exists for.
func TestTheIdentityDateShapeCheckIsEnforcedByBothEngines(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			identity, err := domain.NewIdentity(NewID(), domain.IdentityServiceAccount, "svc-shape")
			if err != nil {
				t.Fatalf("building identity: %v", err)
			}
			if err := s.CreateIdentity(ctx, testPermit, identity); err != nil {
				t.Fatalf("creating identity: %v", err)
			}
			for _, bad := range []string{"2026-9-8", "08/09/2026", "2026-09-08T00:00:00Z", ""} {
				_, err := s.db.Writer.ExecContext(ctx,
					s.db.Writer.Rebind(`UPDATE identity SET last_rotated = ? WHERE id = ?`),
					bad, identity.ID)
				if err == nil {
					t.Errorf("the database accepted last_rotated = %q; the shape CHECK is not "+
						"enforcing on %s", bad, e.Name)
				}
			}
			if _, err := s.db.Writer.ExecContext(ctx,
				s.db.Writer.Rebind(`UPDATE identity SET last_rotated = ? WHERE id = ?`),
				"2026-09-08", identity.ID); err != nil {
				t.Errorf("the database refused a well-formed date: %v", err)
			}
		})
	}
}

// TestAnIdentityIsCreatedWithNoRecordedRotation pins the create half of the
// one-writer rule behaviourally, beside the AST scan that pins it structurally
// (last_rotated_source_test.go, Task 3). Declaring a credential and recording
// when it was last rotated are two acts.
func TestAnIdentityIsCreatedWithNoRecordedRotation(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			s, ctx := newStore(t, e)
			identity, err := domain.NewIdentity(NewID(), domain.IdentityServiceAccount, "svc-fresh")
			if err != nil {
				t.Fatalf("building identity: %v", err)
			}
			identity.LastRotated = strPtr("2020-01-01") // deliberately set, deliberately ignored
			if err := s.CreateIdentity(ctx, testPermit, identity); err != nil {
				t.Fatalf("creating identity: %v", err)
			}
			var stored *string
			if err := s.readOne(ctx, &stored,
				`SELECT last_rotated FROM identity WHERE id = ?`, identity.ID); err != nil {
				t.Fatalf("reading it back: %v", err)
			}
			if stored != nil {
				t.Errorf("last_rotated = %q after a create; create must never write it", *stored)
			}
			var version int
			if err := s.readOne(ctx, &version,
				`SELECT row_version FROM identity WHERE id = ?`, identity.ID); err != nil {
				t.Fatalf("reading row_version: %v", err)
			}
			if version != 1 {
				t.Errorf("row_version = %d after a create, want 1", version)
			}
		})
	}
}

// TestBulkOwnershipBumpsIdentityRowVersion is the failure 00069 exists to
// prevent, from the other direction: a bulk assignment moving team_id under an
// open correction form whose token still validates, so the form's save silently
// reverts the assignment.
func TestBulkOwnershipBumpsIdentityRowVersion(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			team := f.team(t, "bump-team", "", strp("bump@example.com"))
			id := f.identity(t, "bump-identity", nil, "")

			before := f.rowVersion(t, id)
			if _, err := f.s.BulkAssignOwnership(f.ctx, testPermit, "identity",
				[]string{id}, team); err != nil {
				t.Fatalf("BulkAssignOwnership: %v", err)
			}
			if after := f.rowVersion(t, id); after != before+1 {
				t.Errorf("row_version = %d after a bulk assignment, want %d. An open "+
					"correction form's token still validates, so its save silently "+
					"reverts the assignment.", after, before+1)
			}

			// The guard is unchanged: a second assignment of a now-owned row is
			// skipped and reported, never 409'd, and does not bump again.
			mid := f.rowVersion(t, id)
			other := f.team(t, "bump-team-2", "", strp("bump2@example.com"))
			outcomes, err := f.s.BulkAssignOwnership(f.ctx, testPermit, "identity",
				[]string{id}, other)
			if err != nil {
				t.Fatalf("second BulkAssignOwnership: %v", err)
			}
			if len(outcomes) != 1 || outcomes[0].Result != ReassignStale {
				t.Errorf("outcome = %+v, want one ReassignStale -- the WHERE team_id IS NULL "+
					"guard is still the whole eligibility check", outcomes)
			}
			if after := f.rowVersion(t, id); after != mid {
				t.Errorf("row_version moved on a skipped assignment: %d -> %d", mid, after)
			}
		})
	}
}

// TestReassignTeamOwnershipBumpsIdentityRowVersion is the same property for the
// team-retirement path, which uses WHERE team_id = ? rather than IS NULL.
func TestReassignTeamOwnershipBumpsIdentityRowVersion(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newOwnershipFixture(t, e)
			from := f.team(t, "from-team", "", strp("from@example.com"))
			to := f.team(t, "to-team", "", strp("to@example.com"))
			id := f.identity(t, "reassigned-identity", &from, "")

			before := f.rowVersion(t, id)
			outcomes, err := f.s.ReassignTeamOwnership(f.ctx, testPermit, from, to)
			if err != nil {
				t.Fatalf("ReassignTeamOwnership: %v", err)
			}
			if len(outcomes) == 0 {
				t.Fatal("no outcomes at all; the reassignment found nothing to move and " +
					"the assertion below would pass on an untouched row")
			}
			if after := f.rowVersion(t, id); after != before+1 {
				t.Errorf("row_version = %d after a team reassignment, want %d. The guard "+
					"here is WHERE team_id = fromTeamID and stays that way -- the bump is "+
					"additive, so an open correction form's token stops validating once "+
					"the row moved underneath it.", after, before+1)
			}
			// The guard is untouched: reassigning again from the OLD team finds
			// nothing and reports it, rather than 409'ing or bumping.
			mid := f.rowVersion(t, id)
			again, err := f.s.ReassignTeamOwnership(f.ctx, testPermit, from, to)
			if err != nil {
				t.Fatalf("second ReassignTeamOwnership: %v", err)
			}
			for _, o := range again {
				if o.EntityType == "identity" && o.Result != ReassignStale {
					t.Errorf("outcome = %+v, want ReassignStale -- the WHERE team_id = ? "+
						"guard is still the whole eligibility check", o)
				}
			}
			if after := f.rowVersion(t, id); after != mid {
				t.Errorf("row_version moved %d -> %d on a reassignment that matched nothing",
					mid, after)
			}
		})
	}
}
```

`f.rowVersion` is a small helper added to `ownershipFixture` in `internal/store/ownership_test.go`:

```go
func (f *ownershipFixture) rowVersion(t *testing.T, identityID string) int {
	t.Helper()
	var v int
	if err := f.s.readOne(f.ctx, &v,
		`SELECT row_version FROM identity WHERE id = ?`, identityID); err != nil {
		t.Fatalf("reading row_version for %s: %v", identityID, err)
	}
	return v
}
```

- [ ] **Step 8: Run them and watch them fail**

```bash
git add -A
INV_TEST_POSTGRES_DSN="postgres://invctl:invctl@127.0.0.1:5433/invctl?sslmode=disable" \
  go test ./internal/store/ -run 'TestTheIdentityDateShape|TestAnIdentityIsCreated|BumpsIdentityRowVersion|TestEveryColumnIsClassified' -count=1
```

Expected before the migration: compile failure on `identity.RowVersion`. After the struct field but before the migration: `missing destination name row_version` on every `SELECT * FROM identity` — that is the sequencing hazard this task exists to hold together, and seeing it is worth the thirty seconds.

- [ ] **Step 9: Run them and watch them pass, on both engines**

```bash
INV_TEST_POSTGRES_DSN="..." go test ./internal/store/ ./internal/domain/ -count=1
```

`TestEveryColumnIsClassified` is the one that catches a missed column and **it only fails on the engine it ran against** — a SQLite-only run will not show a Postgres failure.

- [ ] **Step 10: Prove each guard can fail**

| Mutation | Test that must go red |
|---|---|
| Delete `"row_version"` from `classification.go`'s `"identity"` list | `TestEveryColumnIsClassified/sqlite` **and** `/postgres` |
| Delete `row_version = row_version + 1` from `bulk_ownership.go`'s identity branch | `TestBulkOwnershipBumpsIdentityRowVersion` |
| Replace the SQLite migration's `length(last_rotated) = 10` with `length(last_rotated) >= 1` | `TestTheIdentityDateShapeCheckIsEnforcedByBothEngines/sqlite` (on `"2026-9-8"`, which is 8 characters) |
| Put `last_rotated` back on `CreateIdentity`'s INSERT, binding `i.LastRotated` | `TestAnIdentityIsCreatedWithNoRecordedRotation` |

**Note the layer for the third one.** If you instead delete the whole `ADD CONSTRAINT` statement, the migration template is cached per process (`sqliteTemplate`, `engines_test.go:99`) — run with `-count=1` and a fresh process or you will be testing a stale template and reading a false green.

- [ ] **Step 11: Commit**

```bash
git add -A && make lint
git commit   # why: identity grows a correction path, so it needs the token link got
             # in 00066 -- and the two bulk writers must maintain it or it is not one
```

---

### Task 2: The domain type — a spec constructor and a rotation state that cannot lie

Pure domain, no database. It is where the defect the whole package exists to fix actually lives: `RotationOverdue` (`internal/domain/endpoint.go:255-266`) returns `false` for "policy says 90 days, nobody has ever recorded a rotation" **and** `false` on a parse error, so an unreadable value reads as healthy.

**Files:**
- Modify: `internal/domain/endpoint.go`, `internal/seed/seed.go:888-915`
- Modify (call-site conversion): `internal/store/boundary_test.go:735,746`, `internal/store/ownership_test.go:150`, `internal/store/store_test.go:853`, `internal/web/users_test.go:458`, `internal/web/dep_row_controls_test.go:124`
- Test: `internal/domain/identity_test.go` (new)

**Interfaces:**
- Produces: `domain.IdentitySpec`, `domain.NewIdentity(id string, spec IdentitySpec) (*Identity, error)`, `func (i *Identity) Validate() error`, `domain.RotationState` and its five constants, `func (i *Identity) RotationStatus(now time.Time) RotationState`, `func (i *Identity) RotationDueOn() *string`
- **Deletes:** `domain.Identity.RotationOverdue`

- [ ] **Step 1: Write the failing test**

`internal/domain/identity_test.go`:

```go
func TestRotationStatus(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	days := func(n int) *int { return &n }
	on := func(s string) *string { return &s }

	for _, tc := range []struct {
		name  string
		days  *int
		last  *string
		want  RotationState
	}{
		// The two the old boolean collapsed into one answer, and the whole
		// reason this is a state. They are OPPOSITE FACTS.
		{"no policy is unmanaged", nil, nil, RotationUnmanaged},
		{"no policy and a date is still unmanaged", nil, on("2026-06-01"), RotationUnmanaged},
		{"policy with no record is not fine", days(90), nil, RotationNeverRecorded},

		{"inside the window", days(90), on("2026-08-20"), RotationWithinWindow},
		{"exactly on the due day is still inside", days(90), on("2026-06-17"), RotationWithinWindow},
		{"a day past the due day is overdue", days(90), on("2026-06-16"), RotationOverdue},
		{"long overdue", days(90), on("2026-01-01"), RotationOverdue},

		// A state that cannot be READ must never render as a state that is
		// FINE. RotationOverdue returned false here, which is how an
		// unparseable stored value read as healthy.
		{"unparseable is unreadable, never healthy", days(90), on("not-a-date"), RotationUnreadable},
		{"a real-looking impossible date is unreadable", days(90), on("2026-02-31"), RotationUnreadable},
		{"a timestamp where a date belongs is unreadable", days(90), on("2026-06-01T00:00:00Z"), RotationUnreadable},

		// A future date cannot arrive through the handler (422) but can
		// arrive through a hand-edited database, and it must not read as a
		// policy being met by something that has not happened.
		{"a future date is within the window, not overdue", days(90), on("2026-12-01"), RotationWithinWindow},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := &Identity{RotationDays: tc.days, LastRotated: tc.last}
			if got := i.RotationStatus(now); got != tc.want {
				t.Errorf("RotationStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRotationDueOn(t *testing.T) {
	days := func(n int) *int { return &n }
	on := func(s string) *string { return &s }
	for _, tc := range []struct {
		name string
		days *int
		last *string
		want string // "" means nil
	}{
		{"both set", days(90), on("2026-06-01"), "2026-08-30"},
		{"no policy", nil, on("2026-06-01"), ""},
		{"no record", days(90), nil, ""},
		// A due date computed from a value that will not parse is a lie with a
		// date on it, which is worse than no answer.
		{"unreadable", days(90), on("not-a-date"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := (&Identity{RotationDays: tc.days, LastRotated: tc.last}).RotationDueOn()
			switch {
			case tc.want == "" && got != nil:
				t.Errorf("RotationDueOn = %q, want nil", *got)
			case tc.want != "" && got == nil:
				t.Errorf("RotationDueOn = nil, want %q", tc.want)
			case tc.want != "" && *got != tc.want:
				t.Errorf("RotationDueOn = %q, want %q", *got, tc.want)
			}
		})
	}
}

func TestNewIdentityValidates(t *testing.T) {
	ok := IdentitySpec{Kind: IdentityServiceAccount, Name: "svc-orders"}
	for _, tc := range []struct {
		name    string
		spec    IdentitySpec
		wantErr string // the field the error must name; "" means no error
	}{
		{"a valid service account", ok, ""},
		{"a blank name is refused", IdentitySpec{Kind: IdentityServiceAccount}, "name"},
		{"whitespace is not a name", IdentitySpec{Kind: IdentityServiceAccount, Name: "   "}, "name"},
		{"an unknown kind is refused", IdentitySpec{Kind: "robot", Name: "svc-orders"}, "kind"},
		{"a zero rotation policy is refused", func() IdentitySpec {
			s := ok
			n := 0
			s.RotationDays = &n
			return s
		}(), "rotation_days"},
		{"a negative rotation policy is refused", func() IdentitySpec {
			s := ok
			n := -1
			s.RotationDays = &n
			return s
		}(), "rotation_days"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewIdentity("id-1", tc.spec)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("NewIdentity: %v", err)
				}
				if got.Lifecycle != LifecycleActive || got.RowVersion != 1 {
					t.Errorf("a new identity is %s/v%d, want active/v1", got.Lifecycle, got.RowVersion)
				}
				if got.LastRotated != nil {
					t.Error("NewIdentity set last_rotated; only RecordIdentityRotation writes it")
				}
				return
			}
			// AsValidation, not errors.As by hand: it is the accessor the rest
			// of this package and every handler already use
			// (internal/domain/errors.go:114), and Messages() is the field ->
			// message map. ValidationError.Fields is a SLICE of FieldError, not
			// a map, so indexing it is a compile error rather than a lookup.
			ve, ok := AsValidation(err)
			if !ok {
				t.Fatalf("NewIdentity(%+v) = %v (%T), want a *ValidationError -- the "+
					"handler maps that to 422 with the form re-rendered, and anything "+
					"else to a 500", tc.spec, err, err)
			}
			if _, named := ve.Messages()[tc.wantErr]; !named {
				t.Errorf("the refusal names %v, not %q. A message on the wrong field "+
					"lands nowhere near the input the operator has to fix.",
					ve.Messages(), tc.wantErr)
			}
		})
	}
}

// TestValidateIsReachableWithoutTheConstructor is Environment.Validate's own
// lesson, restated for the entity it is being copied onto: "the checks lived
// inside NewEnvironment, so UpdateEnvironment wrote whatever it was handed and
// the table CHECK was the only thing standing between a form and a blank name."
func TestValidateIsReachableWithoutTheConstructor(t *testing.T) {
	// A value that already exists and is then corrupted by an update path,
	// which is exactly what UpdateIdentity is handed. The constructor is not
	// involved, so if the checks lived inside it this would pass silently.
	for _, tc := range []struct {
		name  string
		spoil func(i *Identity)
		field string
	}{
		{"a blank name", func(i *Identity) { i.Name = "" }, "name"},
		{"whitespace for a name", func(i *Identity) { i.Name = "  " }, "name"},
		{"an unknown kind", func(i *Identity) { i.Kind = "robot" }, "kind"},
		{"a zero rotation policy", func(i *Identity) { n := 0; i.RotationDays = &n }, "rotation_days"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := &Identity{
				ID: "id-1", Kind: IdentityServiceAccount, Name: "svc",
				Lifecycle: LifecycleActive, RowVersion: 3,
			}
			tc.spoil(i)

			err := i.Validate()
			if err == nil {
				t.Fatalf("Validate accepted %s on an existing value. UpdateIdentity would "+
					"write it and the DB CHECK would be the only thing standing between "+
					"a form and a bad row -- the defect Environment.Validate's own doc "+
					"comment records.", tc.name)
			}
			ve, ok := AsValidation(err)
			if !ok {
				t.Fatalf("error = %v (%T), want a *ValidationError", err, err)
			}
			if _, named := ve.Messages()[tc.field]; !named {
				t.Errorf("the refusal names %v, not %q", ve.Messages(), tc.field)
			}
		})
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

`go test ./internal/domain/ -run 'TestRotation|TestNewIdentity|TestValidateIsReachable' -count=1`
Expected: FAIL, `undefined: IdentitySpec`, `undefined: RotationUnmanaged`.

- [ ] **Step 3: Implement**

In `internal/domain/endpoint.go`, replacing `NewIdentity` and `RotationOverdue` (lines 244-266):

```go
// IdentitySpec is everything a person declares about a credential reference.
//
// A SPEC RATHER THAN A POSITIONAL SIGNATURE, and this repo has now recorded the
// reason twice -- CableBundleSpec: "a positional signature had to be replaced
// mid-branch when it could not accept a required field, so a constructor that
// cannot pass a required value cannot build a valid one." NewIdentity(id, kind,
// name) could accept none of realm, secret_ref, rotation_days or team_id, so the
// seeder assigned four fields AFTER construction and the validation the
// constructor performed was not the validation the row got.
//
// IT DELIBERATELY HAS NO LastRotated. last_rotated has exactly one writer,
// RecordIdentityRotation (docs/identity-surface-design.md, "One writer for
// last_rotated"): declaring a credential that already exists and recording when
// it was last rotated are two acts and two audit entries, both of which are
// true. A field here would be a fifth way to stamp it.
type IdentitySpec struct {
	Kind, Name   string
	Realm        *string
	SecretRef    *string
	RotationDays *int
	TeamID       *string
}

// NewIdentity validates and constructs a principal.
//
// NO `now` PARAMETER, unlike almost every other constructor in this package,
// because the table has no timestamp to stamp -- migration 00069 gave identity
// row_version ONLY, for the reasons 00066 gave link. Worth saying out loud, or
// the next person adds the parameter back out of habit and then has to invent a
// column for it to fill.
func NewIdentity(id string, spec IdentitySpec) (*Identity, error) {
	i := &Identity{
		ID: id, Kind: spec.Kind, Name: spec.Name,
		Realm: spec.Realm, SecretRef: spec.SecretRef,
		RotationDays: spec.RotationDays, TeamID: spec.TeamID,
		Lifecycle:  LifecycleActive,
		RowVersion: 1,
	}
	if err := i.Validate(); err != nil {
		return nil, err
	}
	return i, nil
}

// Validate checks an identity against its business rules and normalises what the
// constructor always normalised.
//
// SEPARATE FROM THE CONSTRUCTOR because the update path has to run the same
// rules. Environment.Validate is the shape and its doc comment names the defect
// this prevents: the checks lived inside NewEnvironment, so UpdateEnvironment
// wrote whatever it was handed and the table CHECK was the only thing standing
// between a form and a blank name.
func (i *Identity) Validate() error {
	ve := &ValidationError{}
	i.Name = checkRequired(ve, "name", i.Name)
	checkEnum(ve, "kind", i.Kind, IdentityKinds)
	// Matches identity_rotation_days_check (migration 00003): a policy of zero
	// days is not a policy, it is a row that is overdue the moment it is saved.
	checkPositive(ve, "rotation_days", i.RotationDays)
	return ve.OrNil()
}

// RotationState is what this credential's rotation policy currently says about
// it. FIVE STATES RATHER THAN A BOOLEAN, and the split that matters is the first
// two: `rotation_days IS NULL` means nobody asked for this to be rotated and
// there is nothing to be late for, while `rotation_days` set with no recorded
// rotation means THE ESTATE HAS A RULE FOR THIS CREDENTIAL AND NO EVIDENCE IT
// HAS EVER BEEN FOLLOWED. Those are opposite facts. The boolean this replaced
// answered `false` to both, which is how a credential that has never been
// rotated in four years rendered identically to one nobody ever intended to
// rotate -- and all three identities in the demo estate were in the second state.
type RotationState string

const (
	// RotationUnmanaged: no policy. A cert_subject or a human row is often
	// legitimately here, and it is NOT a finding -- flagging every one would
	// swamp the page and teach people to ignore it.
	RotationUnmanaged RotationState = "unmanaged"
	// RotationNeverRecorded: a policy, and nothing has ever recorded a
	// rotation against it. Either it has never been rotated since the day it
	// was created, or it has and nobody wrote it down. invctl cannot tell
	// which, and both are worth somebody's attention -- which is exactly why
	// the finding for it is a Gap and not a Fault.
	RotationNeverRecorded RotationState = "never_recorded"
	RotationWithinWindow  RotationState = "within_window"
	RotationOverdue       RotationState = "overdue"
	// RotationUnreadable: the stored value will not parse. It exists because
	// the alternative is the failure this repo keeps finding -- the boolean
	// this replaced returned `false` on a parse error, so an unreadable value
	// read as HEALTHY. A state that cannot be read must never render as a
	// state that is fine.
	RotationUnreadable RotationState = "unreadable"
)

// RotationStatus answers what the policy says about this credential now.
//
// THE FIRST TWO STATES ARE OPPOSITE FACTS AND MUST NEVER BE COLLAPSED, which is
// the whole reason this returns a state rather than a bool. `unmanaged` means
// there is NO RULE TO BREAK -- nobody asked for this credential to be rotated,
// and a cert_subject or a human row is often legitimately here. `never_recorded`
// means THE ESTATE HAS A RULE FOR THIS CREDENTIAL AND NO EVIDENCE IT HAS EVER
// BEEN FOLLOWED: either it has never been rotated since the day it was created,
// or it has and nobody wrote it down, and invctl cannot tell which. Both are
// worth somebody's attention; neither is "fine".
//
// The boolean this replaced answered `false` to both, which is how a credential
// that has never been rotated in four years rendered identically to one nobody
// ever intended to rotate -- and all three identities in the demo estate were in
// the second state, so the surface would have reported the whole estate as
// healthy. Collapsing them again is the defect this work package exists to kill.
func (i *Identity) RotationStatus(now time.Time) RotationState {
	if i.RotationDays == nil {
		return RotationUnmanaged
	}
	if i.LastRotated == nil {
		return RotationNeverRecorded
	}
	last, err := ParseDate(*i.LastRotated)
	if err != nil {
		return RotationUnreadable
	}
	if now.UTC().After(last.AddDate(0, 0, *i.RotationDays)) {
		return RotationOverdue
	}
	return RotationWithinWindow
}

// RotationDueOn is the date the next rotation is due, or nil when the question
// has no answer: no policy, no record, or a stored value that will not parse.
// The last of those is the one worth naming -- a due date computed from an
// unreadable value is a lie with a date on it.
func (i *Identity) RotationDueOn() *string {
	if i.RotationDays == nil || i.LastRotated == nil {
		return nil
	}
	last, err := ParseDate(*i.LastRotated)
	if err != nil {
		return nil
	}
	due := FormatDate(last.AddDate(0, 0, *i.RotationDays))
	return &due
}
```

**Delete `RotationOverdue` entirely.** The spec is explicit: *"a two-valued answer to a three-valued question is what produced the defect; leaving it in place leaves the next caller a way to reintroduce it."* It has zero callers today, so the deletion costs nothing.

- [ ] **Step 4: Convert every `NewIdentity` call site — in this task, not a later cleanup**

`grep -rn "NewIdentity(" --include=*.go .` gives seven. All become spec-taking.

`internal/seed/seed.go:888-915` is the production one and the reason the constructor is changing:

```go
func (b *builder) identities() {
	identities := []struct{ kind, name, realm, secretRef string }{
		{domain.IdentityServiceAccount, "svc-orders", "vault", "kv/prod/orders/db"},
		{domain.IdentityServiceAccount, "svc-sso", "vault", "kv/prod/sso/db"},
		{domain.IdentityMachineAccount, "svc-backup$", "AD", "kv/prod/backup/windows"},
	}
	for _, i := range identities {
		if !b.ok() {
			return
		}
		identity, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
			Kind:  i.kind,
			Name:  i.name,
			Realm: str(i.realm),
			// A path, never a secret. If this field ever held a credential the
			// whole database would become a secret store, which it must not be.
			SecretRef:    str(i.secretRef),
			RotationDays: num(90),
			TeamID:       b.team("platform"),
		})
		if err != nil {
			b.fail(fmt.Errorf("building identity %s: %w", i.name, err))
			return
		}
		if err := b.store.CreateIdentity(b.ctx, Permit, identity); err != nil {
			b.fail(fmt.Errorf("seeding identity %s: %w", i.name, err))
			return
		}
		b.identityIDs[i.name] = identity.ID
	}
}
```

The five test call sites become `domain.NewIdentity(NewID(), domain.IdentitySpec{Kind: ..., Name: ...})`, with the fields they previously assigned after construction folded in. **Do not leave any post-construction field assignment behind** — that is the defect the spec is fixing, and a test that still does it teaches the pattern back.

- [ ] **Step 5: Run the tests**

```bash
INV_TEST_POSTGRES_DSN="..." go test ./internal/domain/ ./internal/store/ ./internal/seed/ ./internal/web/... -count=1
```

- [ ] **Step 6: Prove the tests can fail**

| Mutation | Test that must go red |
|---|---|
| In `RotationStatus`, return `RotationWithinWindow` instead of `RotationUnreadable` on the `ParseDate` error | `TestRotationStatus/unparseable_is_unreadable,_never_healthy` — **this is the mutation that matters most in this plan**; it reproduces the exact defect the spec was written to kill |
| In `RotationStatus`, return `RotationUnmanaged` when `LastRotated == nil` | `TestRotationStatus/policy_with_no_record_is_not_fine` |
| Delete `checkEnum(ve, "kind", ...)` from `Validate` | `TestNewIdentityValidates/an_unknown_kind_is_refused` |
| In `RotationDueOn`, compute from `time.Time{}` on a parse error instead of returning nil | `TestRotationDueOn/unreadable` |

- [ ] **Step 7: Commit**

```bash
git add -A && make lint
git commit   # why: a boolean answered "fine" to two opposite facts and to an
             # unreadable value -- the surface would have reported all three
             # demo identities as healthy
```

---

### Task 3: The store — one writer for `last_rotated`, and the AST scan that keeps it that way

**Files:**
- Create: `internal/store/identities.go`, `internal/store/last_rotated_source_test.go`
- Modify: `internal/store/deps.go` (remove the identity block), `internal/store/write_surface_test.go`, `internal/store/identities_test.go`, `internal/web/handlers/deps.go:238`, `internal/web/handlers/services.go:340`

**Interfaces:**
- Produces:
  - `type IdentityRow struct { domain.Identity; TeamCode, TeamName string; DependencyCount, WindowsCount int }`
  - `type IdentityFilter struct { Query, Kind, TeamID string; Rotation domain.RotationState; IncludeRetired bool }`
  - `ListIdentities(ctx context.Context, f IdentityFilter) ([]IdentityRow, error)` — **signature change**
  - `GetIdentity(ctx context.Context, id string) (*IdentityRow, error)`
  - `UpdateIdentity(ctx context.Context, p domain.Permit, i *domain.Identity) error`
  - `RetireIdentity(ctx context.Context, p domain.Permit, id string) error`
  - `RecordIdentityRotation(ctx context.Context, p domain.Permit, id, date string) error`
  - `IdentityUsage(ctx context.Context, id string) (*IdentityUsageRows, error)`

- [ ] **Step 1: Write the failing tests**

Add to `internal/store/identities_test.go`. Every one runs `for _, e := range Engines(t)`.

First the fixture the behavioural ones share. It builds the smallest estate that
can hold a dependency naming a credential, reusing `mustEnvironment`
(`internal/store/store_test.go:31`) and `mustAsset` (`:44`) rather than
inventing new helpers:

```go
// identityFixture is an estate small enough to reason about and large enough to
// hold an edge: one environment, one host, a consumer service, a provider
// service with an endpoint, and whatever identities a test declares.
type identityFixture struct {
	s          *SQLStore
	ctx        context.Context
	consumerID string
	endpointID string
}

func newIdentityFixture(t *testing.T, e Engine) *identityFixture {
	t.Helper()
	s, ctx := newStore(t, e)
	envID := mustEnvironment(t, s, ctx, "prod", domain.EnvRoleProduction)
	mustAsset(t, s, ctx, domain.KindServer, "app-01", nil, envID)

	mkService := func(code string) string {
		svc, err := domain.NewService(NewID(), domain.ServiceSpec{
			Code: code, Name: code, Kind: domain.SvcAPI,
			EnvironmentID: envID, Availability: domain.AvailStandalone, Tier: 2,
		}, s.Now())
		if err != nil {
			t.Fatalf("building service %s: %v", code, err)
		}
		if err := s.CreateService(ctx, testPermit, svc); err != nil {
			t.Fatalf("creating service %s: %v", code, err)
		}
		return svc.ID
	}
	consumer, provider := mkService("orders"), mkService("orders-db")

	port := 5432
	ep, err := domain.NewEndpoint(NewID(), provider, "sql", domain.ProtoTCP, &port, domain.BindHost)
	if err != nil {
		t.Fatalf("building endpoint: %v", err)
	}
	if err := s.CreateEndpoint(ctx, testPermit, ep); err != nil {
		t.Fatalf("creating endpoint: %v", err)
	}
	return &identityFixture{s: s, ctx: ctx, consumerID: consumer, endpointID: ep.ID}
}

// identity declares a credential. rotationDays of 0 means no policy at all.
func (f *identityFixture) identity(t *testing.T, name string, rotationDays int) *domain.Identity {
	t.Helper()
	spec := domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: name, Realm: strPtr("vault"),
		SecretRef: strPtr("kv/prod/" + name),
	}
	if rotationDays > 0 {
		spec.RotationDays = &rotationDays
	}
	i, err := domain.NewIdentity(NewID(), spec)
	if err != nil {
		t.Fatalf("building identity %s: %v", name, err)
	}
	if err := f.s.CreateIdentity(f.ctx, testPermit, i); err != nil {
		t.Fatalf("creating identity %s: %v", name, err)
	}
	return i
}

// dependency declares an edge from the consumer to the provider endpoint,
// authenticating as identityID.
func (f *identityFixture) dependency(t *testing.T, identityID string) *domain.Dependency {
	t.Helper()
	endpoint, identity := f.endpointID, identityID
	d, err := domain.NewDependency(NewID(), domain.DependencySpec{
		ConsumerServiceID:  f.consumerID,
		ProviderEndpointID: &endpoint,
		Nature:             domain.NatureHard,
		FailureMode:        "orders cannot be written",
		IdentityID:         &identity,
		AuthMethod:         strPtr("scram-sha-256"),
	}, f.s.Now())
	if err != nil {
		t.Fatalf("building dependency: %v", err)
	}
	if err := f.s.CreateDependency(f.ctx, testPermit, d, nil); err != nil {
		t.Fatalf("creating dependency: %v", err)
	}
	return d
}

// rowVersionOf and lastRotatedOf read the two columns these tests assert on,
// straight from the row rather than through GetIdentity -- a bug in the read
// path must not be able to make a write assertion pass.
func (f *identityFixture) rowVersionOf(t *testing.T, id string) int {
	t.Helper()
	var v int
	if err := f.s.readOne(f.ctx, &v, `SELECT row_version FROM identity WHERE id = ?`, id); err != nil {
		t.Fatalf("reading row_version for %s: %v", id, err)
	}
	return v
}

func (f *identityFixture) lastRotatedOf(t *testing.T, id string) *string {
	t.Helper()
	var v *string
	if err := f.s.readOne(f.ctx, &v, `SELECT last_rotated FROM identity WHERE id = ?`, id); err != nil {
		t.Fatalf("reading last_rotated for %s: %v", id, err)
	}
	return v
}

func (f *identityFixture) nameOf(t *testing.T, id string) string {
	t.Helper()
	var v string
	if err := f.s.readOne(f.ctx, &v, `SELECT name FROM identity WHERE id = ?`, id); err != nil {
		t.Fatalf("reading name for %s: %v", id, err)
	}
	return v
}

func (f *identityFixture) auditCount(t *testing.T, id string) int {
	t.Helper()
	var n int
	if err := f.s.readOne(f.ctx, &n,
		`SELECT COUNT(*) FROM change_log WHERE entity_type = ? AND entity_id = ?`,
		"identity", id); err != nil {
		t.Fatalf("counting audit entries for %s: %v", id, err)
	}
	return n
}
```

**Every assertion below sits outside any `if err == nil` block.** A reviewer's
patch for one of these once put the assertions *inside* one, so an error made the
test silently pass — the same defect class the test was written to catch. The
rule is: fail loudly on the error with `t.Fatalf`, then assert unconditionally.


```go
// TestRecordIdentityRotationWritesExactlyOneChangeLogRow is the audit shape the
// spec specifies literally: a one-field diff carrying WHEN the rotation happened
// (new), WHEN it was recorded (change_log.at) and WHO recorded it
// (change_log.actor). action stays 'update' -- VerifyDependency is the precedent
// in every respect, and adding a value to the change_log CHECK would mean
// rebuilding the table on SQLite (docs/AUDIT.md rule 10).
func TestRecordIdentityRotationWritesExactlyOneChangeLogRow(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)
			i := f.identity(t, "svc-audited", 90)
			before := f.auditCount(t, i.ID)

			const rotated = "2026-09-08"
			if err := f.s.RecordIdentityRotation(f.ctx, testPermit, i.ID, rotated); err != nil {
				t.Fatalf("recording the rotation: %v", err)
			}

			if got := f.auditCount(t, i.ID); got != before+1 {
				t.Fatalf("change_log went %d -> %d, want exactly one new entry. Every "+
					"declared mutation writes one row in the same transaction; two would "+
					"mean the write is happening twice.", before, got)
			}
			changes, err := f.s.ListChangesForEntity(f.ctx, "identity", i.ID, 50)
			if err != nil {
				t.Fatalf("reading the audit trail: %v", err)
			}
			newest := changes[0]
			if newest.Action != domain.ActionUpdate {
				t.Errorf("action = %q, want %q. The change_log CHECK allows exactly "+
					"create|update|delete|retire, and adding a value to it would mean "+
					"rebuilding the table on SQLite (docs/AUDIT.md rule 10). "+
					"VerifyDependency is the precedent: a distinct method and route that "+
					"stamp one column and log as an ordinary update.",
					newest.Action, domain.ActionUpdate)
			}
			// The one-field diff the spec specifies literally. It carries WHEN
			// the rotation happened (new), and change_log supplies when it was
			// recorded (at) and who recorded it (actor).
			const want = `{"last_rotated":{"old":null,"new":"2026-09-08"}}`
			if newest.Diff != want {
				t.Errorf("diff = %s, want %s. A wider diff means the rotation path is "+
					"writing columns it should not.", newest.Diff, want)
			}
			if newest.Actor == "" {
				t.Error("the entry has no actor, so the audit trail cannot say who " +
					"recorded the rotation -- which is half of what it is for")
			}
			if got := f.rowVersionOf(t, i.ID); got != 2 {
				t.Errorf("row_version = %d after one rotation, want 2. The rotation action "+
					"carries no token and BUMPS one, following VerifyDependency: a token "+
					"that does not move when the row changes is worse than a spurious 409.",
					got)
			}
			if got := f.lastRotatedOf(t, i.ID); got == nil || *got != rotated {
				t.Errorf("last_rotated = %v, want %q", derefOr(got, "NULL"), rotated)
			}
		})
	}
}

// TestRecordingTheStoredRotationDateWritesNothingAtAll is the rule the spec
// spells out and the reason is an AUTHORIZATION one, not a tidiness one:
// without the short-circuit, diffJSON returns ok=false, logUpdate skips the
// entry, and the UPDATE still moves row_version -- a declared-state write with
// no audit row, which since WP-G1 is an authorization bypass rather than merely
// an untraceable change. RetireEnvironment's "already retired returns nil" is
// the same shape, for the same reason: never claim a thing that did not happen.
func TestRecordingTheStoredRotationDateWritesNothingAtAll(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)
			i := f.identity(t, "svc-idempotent", 90)

			const rotated = "2026-09-08"
			if err := f.s.RecordIdentityRotation(f.ctx, testPermit, i.ID, rotated); err != nil {
				t.Fatalf("recording the first rotation: %v", err)
			}
			auditAfterFirst := f.auditCount(t, i.ID)
			versionAfterFirst := f.rowVersionOf(t, i.ID)

			// The same date again -- two operators recording the same rotation,
			// or one double-submitting a form.
			if err := f.s.RecordIdentityRotation(f.ctx, testPermit, i.ID, rotated); err != nil {
				t.Fatalf("re-recording the stored date must return nil, like "+
					"RetireEnvironment does for an already-retired row: %v", err)
			}

			if got := f.auditCount(t, i.ID); got != auditAfterFirst {
				t.Errorf("change_log grew %d -> %d on a no-op rotation. An audit trail "+
					"full of entries for things that did not happen is worse than one "+
					"without them.", auditAfterFirst, got)
			}
			if got := f.rowVersionOf(t, i.ID); got != versionAfterFirst {
				t.Errorf("row_version moved %d -> %d WITH NO change_log ROW. That is a "+
					"declared-state write with no audit entry, which since WP-G1 is an "+
					"authorization bypass and not merely an untraceable change -- and it "+
					"would also invalidate every open edit form for nothing.",
					versionAfterFirst, got)
			}
		})
	}
}

// TestARotationMayBeBackdatedAndNeverPostDated. The asymmetry is the whole
// argument: a past date can only ever make a finding worse and can never hide
// anything, while a future date hides an overdue finding for a rotation that has
// not happened -- the one direction that turns this feature into a way of
// silencing itself.
func TestARotationMayBeBackdatedAndNeverPostDated(t *testing.T) {
	// A FIXED CLOCK, so "tomorrow" is a constant rather than something computed
	// from the wall clock at both ends -- a test that derives its input and its
	// expectation from the same moving source can pass while the boundary is
	// wrong by a day.
	fixed := time.Date(2026, 9, 15, 11, 30, 0, 0, time.UTC)

	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			for _, tc := range []struct {
				name, date string
				wantErr    bool
			}{
				{"today is fine", "2026-09-15", false},
				{"yesterday is fine -- somebody catching up on Thursday", "2026-09-14", false},
				{"long ago is fine", "2020-01-01", false},
				{"tomorrow is refused", "2026-09-16", true},
				{"next year is refused", "2027-01-01", true},
				{"a malformed date is refused before it reaches the CHECK", "2026-9-8", true},
				{"a timestamp is refused", "2026-09-15T00:00:00Z", true},
				{"an empty date is refused", "", true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					f := newIdentityFixture(t, e)
					f.s = f.s.WithClock(func() time.Time { return fixed })
					i := f.identity(t, "svc-dated", 90)

					err := f.s.RecordIdentityRotation(f.ctx, testPermit, i.ID, tc.date)

					if !tc.wantErr {
						if err != nil {
							t.Fatalf("RecordIdentityRotation(%q): %v. Refusing a backdated "+
								"rotation forces an operator to record a date they know is "+
								"wrong, which is worse than the thing the refusal was "+
								"protecting.", tc.date, err)
						}
						got := f.lastRotatedOf(t, i.ID)
						if got == nil || *got != tc.date {
							t.Errorf("last_rotated = %v, want %q",
								derefOr(got, "NULL"), tc.date)
						}
						return
					}

					// Fail loudly, then assert unconditionally.
					if err == nil {
						t.Fatalf("RecordIdentityRotation(%q) was accepted. A future date "+
							"hides an overdue finding for a rotation that has not "+
							"happened, which is the one direction that turns this "+
							"feature into a way of silencing itself.", tc.date)
					}
					ve, ok := domain.AsValidation(err)
					if !ok {
						t.Fatalf("error = %v (%T), want a *domain.ValidationError so the "+
							"handler returns 422 with the form re-rendered", err, err)
					}
					if _, named := ve.Messages()["last_rotated"]; !named {
						t.Errorf("the refusal names %v, not last_rotated -- the message has "+
							"to land on the field the operator was filling in",
							ve.Messages())
					}
					if got := f.lastRotatedOf(t, i.ID); got != nil {
						t.Errorf("last_rotated = %q after a refused rotation, want NULL", *got)
					}
				})
			}

			// NO MONOTONICITY RULE: a date EARLIER than the stored one is
			// allowed, because the stored one may simply have been wrong.
			// change_log records both values and the actor; the audit trail is
			// the control here, not a constraint. (Compare inflation_rate,
			// "corrected in place rather than superseded... a revised index for
			// 2024 was always one figure somebody had wrong.")
			t.Run("a correction may move the date backwards", func(t *testing.T) {
				f := newIdentityFixture(t, e)
				f.s = f.s.WithClock(func() time.Time { return fixed })
				i := f.identity(t, "svc-corrected", 90)

				if err := f.s.RecordIdentityRotation(f.ctx, testPermit, i.ID, "2026-09-10"); err != nil {
					t.Fatalf("recording the first date: %v", err)
				}
				if err := f.s.RecordIdentityRotation(f.ctx, testPermit, i.ID, "2026-09-01"); err != nil {
					t.Fatalf("an EARLIER correction was refused: %v. The stored date may "+
						"simply have been wrong, and a monotonicity rule would leave the "+
						"operator no way to fix it.", err)
				}
				got := f.lastRotatedOf(t, i.ID)
				if got == nil || *got != "2026-09-01" {
					t.Errorf("last_rotated = %v, want the corrected earlier date",
						derefOr(got, "NULL"))
				}
				// Both values survive in the audit trail, which is what makes
				// the permissiveness safe.
				changes, err := f.s.ListChangesForEntity(f.ctx, "identity", i.ID, 50)
				if err != nil {
					t.Fatalf("reading the audit trail: %v", err)
				}
				if len(changes) < 2 {
					t.Fatalf("got %d audit entries, want one per recorded rotation -- the "+
						"log IS the rotation history", len(changes))
				}
				if !strings.Contains(changes[0].Diff, "2026-09-10") {
					t.Errorf("the correction's diff is %s and does not carry the value it "+
						"replaced, so nobody can see what was corrected", changes[0].Diff)
				}
			})
		})
	}
}

// TestRecordIdentityRotationRefusesARetiredIdentity. Rotating a withdrawn
// credential is not a thing that happened. The message names the identity, the
// shape SetBundleMembers uses for a retired bundle.
func TestRecordIdentityRotationRefusesARetiredIdentity(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)
			i := f.identity(t, "svc-withdrawn", 90)
			if err := f.s.RetireIdentity(f.ctx, testPermit, i.ID); err != nil {
				t.Fatalf("retiring identity: %v", err)
			}
			auditBefore := f.auditCount(t, i.ID)
			versionBefore := f.rowVersionOf(t, i.ID)

			err := f.s.RecordIdentityRotation(f.ctx, testPermit, i.ID,
				domain.FormatDate(f.s.Now()))

			// Fail loudly first, then assert unconditionally. An assertion
			// nested in an `if err == nil` would pass on every error, which is
			// the shape this whole file exists to avoid.
			if err == nil {
				t.Fatal("a rotation was recorded against a withdrawn credential. " +
					"Rotating a credential nobody may use any more is not a thing that " +
					"happened, and recording it would put a false fact in change_log " +
					"permanently.")
			}
			ve, ok := domain.AsValidation(err)
			if !ok {
				t.Fatalf("error = %v (%T), want a *domain.ValidationError so the handler "+
					"can return 422 with the form re-rendered rather than a 500", err, err)
			}
			msg, named := ve.Messages()["last_rotated"]
			if !named {
				t.Fatalf("the refusal names fields %v, not last_rotated -- the message has "+
					"to land on the field the operator was filling in", ve.Messages())
			}
			if !strings.Contains(msg, i.Name) {
				t.Errorf("the message is %q and does not name %q. SetBundleMembers names "+
					"the retired bundle for the same reason: an operator with several tabs "+
					"open needs to know WHICH one was withdrawn.", msg, i.Name)
			}

			// And nothing was written. A refusal that still moved the row would
			// be worse than no refusal, because the page would look unchanged.
			if got := f.lastRotatedOf(t, i.ID); got != nil {
				t.Errorf("last_rotated = %q after a refused rotation, want NULL", *got)
			}
			if got := f.rowVersionOf(t, i.ID); got != versionBefore {
				t.Errorf("row_version moved %d -> %d on a refused rotation", versionBefore, got)
			}
			if got := f.auditCount(t, i.ID); got != auditBefore {
				t.Errorf("change_log grew %d -> %d on a refused rotation", auditBefore, got)
			}
		})
	}
}

// TestRetireIdentityRefusesNothingAndRewritesNothing is migration 00003's case
// carried forward: "the natural response to a compromised credential is to
// retire it and create its replacement under the same name". Blocking retirement
// until every dependency has been re-pointed would mean, at exactly the wrong
// moment, that the compromised credential cannot be withdrawn.
func TestRetireIdentityRefusesNothingAndRewritesNothing(t *testing.T) {
	// identity named by a live dependency AND an rt_windows logon account
	if err := s.RetireIdentity(ctx, testPermit, id); err != nil {
		t.Fatalf("retirement was refused while a live dependency named it: %v", err)
	}
	// The dependency still names it, and its row_version did NOT move: nothing
	// was rewritten, so nothing was misattributed to whoever clicked withdraw.
	// And the (realm, name) pair is immediately reusable -- the live-scoped
	// unique index is what makes a restore path unnecessary.
	replacement := ...same realm, same name...
	if err := s.CreateIdentity(ctx, testPermit, replacement); err != nil {
		t.Errorf("the withdrawn name is not reusable, so an operator is stranded: %v", err)
	}
	// A second retire returns nil and writes nothing.
}

// TestIdentityUsageNamesWhatWouldNotice is what makes withdrawal an informed act
// rather than a blind one -- the shape TeamOwnershipCounts feeds the
// team-retirement screen.
func TestIdentityUsageNamesWhatWouldNotice(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)
			named := f.identity(t, "svc-named", 90)
			unused := f.identity(t, "svc-unused", 90)

			live := f.dependency(t, named.ID)
			withdrawn := f.dependency(t, named.ID)
			if err := f.s.RetireDependency(f.ctx, testPermit, withdrawn.ID); err != nil {
				t.Fatalf("retiring the second dependency: %v", err)
			}

			usage, err := f.s.IdentityUsage(f.ctx, named.ID)
			if err != nil {
				t.Fatalf("IdentityUsage: %v", err)
			}
			if len(usage.Dependencies) != 1 {
				t.Fatalf("IdentityUsage returned %d dependencies, want 1. A RETIRED edge is "+
					"history; the question the withdrawal screen asks is what is still "+
					"running, and counting history would overstate the blast radius.",
					len(usage.Dependencies))
			}
			got := usage.Dependencies[0]
			if got.DependencyID != live.ID {
				t.Errorf("the surviving dependency is %s, want the live one %s",
					got.DependencyID, live.ID)
			}
			// The panel has to say something an operator can act on, not an id.
			if got.ConsumerCode != "orders" {
				t.Errorf("consumer code = %q, want %q -- the panel names WHO would notice",
					got.ConsumerCode, "orders")
			}
			if got.ProviderName != "sql" {
				t.Errorf("provider name = %q, want %q", got.ProviderName, "sql")
			}
			if got.AuthMethod != "scram-sha-256" {
				t.Errorf("auth method = %q, want %q", got.AuthMethod, "scram-sha-256")
			}

			// A credential nothing names must report nothing, or the panel says
			// "this is in use" about every credential in the estate.
			empty, err := f.s.IdentityUsage(f.ctx, unused.ID)
			if err != nil {
				t.Fatalf("IdentityUsage for an unused credential: %v", err)
			}
			if len(empty.Dependencies) != 0 || len(empty.Windows) != 0 {
				t.Errorf("an unused credential reports %d dependencies and %d windows "+
					"services, want none of either",
					len(empty.Dependencies), len(empty.Windows))
			}

			// THE rt_windows HALF IS COVERED IN internal/web, NOT HERE, and the
			// reason is that the seeded estate already has the real thing:
			// seed_services.go:260 makes svc-backup$ the logon account of a
			// Windows service, because "the run-as account is the usual reason a
			// Windows service dies after a credential rotation". Rebuilding a
			// service_instance and an rt_windows row by hand here to assert the
			// same join would be a second, weaker fixture of something the
			// fixture suite already carries -- see
			// TestTheIdentityDetailPageNamesWhatWouldNotice in Task 4.
		})
	}
}

// TestListIdentitiesFilters covers each filter and, specifically, that a retired
// identity is findable: "what is stored keeps displaying".
func TestListIdentitiesFilters(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)

			// Five rows spanning every axis the page filters on.
			orders := f.identity(t, "svc-orders", 90)   // within window, once rotated
			sso := f.identity(t, "svc-sso", 90)         // overdue
			backup := f.identity(t, "svc-backup", 90)   // never recorded
			metrics := f.identity(t, "metrics-scrape", 0) // unmanaged
			legacy := f.identity(t, "svc-legacy", 90)   // retired

			now := f.s.Now()
			if err := f.s.RecordIdentityRotation(f.ctx, testPermit, orders.ID,
				domain.FormatDate(now.AddDate(0, 0, -30))); err != nil {
				t.Fatalf("rotating svc-orders: %v", err)
			}
			if err := f.s.RecordIdentityRotation(f.ctx, testPermit, sso.ID,
				domain.FormatDate(now.AddDate(0, 0, -200))); err != nil {
				t.Fatalf("rotating svc-sso: %v", err)
			}
			// metrics-scrape is an api_token so the kind filter has something to
			// separate; correcting it here rather than widening the fixture.
			mk := *metrics
			mk.Kind = domain.IdentityAPIToken
			if err := f.s.UpdateIdentity(f.ctx, testPermit, &mk); err != nil {
				t.Fatalf("setting the kind on metrics-scrape: %v", err)
			}
			if err := f.s.RetireIdentity(f.ctx, testPermit, legacy.ID); err != nil {
				t.Fatalf("retiring svc-legacy: %v", err)
			}

			names := func(rows []IdentityRow) []string {
				out := make([]string, 0, len(rows))
				for _, r := range rows {
					out = append(out, r.Name)
				}
				sort.Strings(out)
				return out
			}

			for _, tc := range []struct {
				name   string
				filter IdentityFilter
				want   []string
			}{
				{
					// The default omits the retired one, because the default is
					// what a picker and a list both start from.
					"the default is live rows only",
					IdentityFilter{},
					[]string{"metrics-scrape", "svc-backup", "svc-orders", "svc-sso"},
				},
				{
					// "so a retired credential can be found" -- what is stored
					// keeps displaying; it just stops being newly selectable.
					"IncludeRetired finds the withdrawn one",
					IdentityFilter{IncludeRetired: true},
					[]string{"metrics-scrape", "svc-backup", "svc-legacy", "svc-orders", "svc-sso"},
				},
				{"kind", IdentityFilter{Kind: domain.IdentityAPIToken}, []string{"metrics-scrape"}},
				{"name substring", IdentityFilter{Query: "orders"}, []string{"svc-orders"}},
				{"realm substring", IdentityFilter{Query: "vault"},
					[]string{"metrics-scrape", "svc-backup", "svc-orders", "svc-sso"}},
				{"query is case-insensitive", IdentityFilter{Query: "ORDERS"}, []string{"svc-orders"}},
				{"rotation: overdue", IdentityFilter{Rotation: domain.RotationOverdue},
					[]string{"svc-sso"}},
				{"rotation: never recorded", IdentityFilter{Rotation: domain.RotationNeverRecorded},
					[]string{"svc-backup"}},
				{"rotation: within window", IdentityFilter{Rotation: domain.RotationWithinWindow},
					[]string{"svc-orders"}},
				{"rotation: unmanaged", IdentityFilter{Rotation: domain.RotationUnmanaged},
					[]string{"metrics-scrape"}},
				{
					// Two filters compose rather than one winning. The findings
					// page links in with a rotation state and the operator then
					// narrows by name, which is this case.
					"rotation and query compose",
					IdentityFilter{Rotation: domain.RotationNeverRecorded, Query: "backup"},
					[]string{"svc-backup"},
				},
				{"a filter matching nothing returns nothing, not everything",
					IdentityFilter{Query: "no-such-credential"}, []string{}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					rows, err := f.s.ListIdentities(f.ctx, tc.filter)
					if err != nil {
						t.Fatalf("ListIdentities(%+v): %v", tc.filter, err)
					}
					if got := names(rows); !slices.Equal(got, tc.want) {
						t.Errorf("ListIdentities(%+v) = %v, want %v", tc.filter, got, tc.want)
					}
				})
			}

			// The team filter needs a team, which the fixture has none of --
			// built here so the assertion is about the filter rather than about
			// the fixture's shape.
			t.Run("team", func(t *testing.T) {
				team, err := domain.NewTeam(NewID(), domain.TeamSpec{
					Code: "platform", Name: "Platform",
					ContactRef: strPtr("platform@example.com"),
				}, f.s.Now())
				if err != nil {
					t.Fatalf("building team: %v", err)
				}
				if err := f.s.CreateTeam(f.ctx, testPermit, team); err != nil {
					t.Fatalf("creating team: %v", err)
				}
				owned := *backup
				owned.TeamID = &team.ID
				if err := f.s.UpdateIdentity(f.ctx, testPermit, &owned); err != nil {
					t.Fatalf("assigning the team: %v", err)
				}
				rows, err := f.s.ListIdentities(f.ctx, IdentityFilter{TeamID: team.ID})
				if err != nil {
					t.Fatalf("ListIdentities by team: %v", err)
				}
				if got := names(rows); !slices.Equal(got, []string{"svc-backup"}) {
					t.Errorf("ListIdentities by team = %v, want [svc-backup]", got)
				}
			})
		})
	}
}

// TestUpdateIdentityRefusesAStaleToken.
func TestUpdateIdentityRefusesAStaleToken(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)
			i := f.identity(t, "svc-contended", 90)

			// TWO READS OF THE SAME ROW, which is what two operators with the
			// form open at the same time are holding.
			first, err := f.s.GetIdentity(f.ctx, i.ID)
			if err != nil {
				t.Fatalf("first read: %v", err)
			}
			second, err := f.s.GetIdentity(f.ctx, i.ID)
			if err != nil {
				t.Fatalf("second read: %v", err)
			}

			a := first.Identity
			a.Name = "svc-contended-first"
			if err := f.s.UpdateIdentity(f.ctx, testPermit, &a); err != nil {
				t.Fatalf("the first write must succeed, or the second is not stale and "+
					"this test proves nothing: %v", err)
			}

			b := second.Identity
			b.Name = "svc-contended-second"
			err = f.s.UpdateIdentity(f.ctx, testPermit, &b)

			if err == nil {
				t.Fatal("the second write succeeded against a token from before the first. " +
					"That is the silent revert row_version exists to prevent, and " +
					"change_log would record it as a deliberate act by whoever was slower.")
			}
			if !errors.Is(err, domain.ErrStale) {
				t.Fatalf("error = %v, want domain.ErrStale. The handler maps ErrStale to "+
					"409 and everything else to 422 or 500 -- a plain ErrConflict here "+
					"would tell the operator to choose a different name, which is not "+
					"the problem.", err)
			}
			// Never a 404: the row is there, it simply moved.
			if errors.Is(err, domain.ErrNotFound) {
				t.Error("a stale write reported ErrNotFound, which would tell the operator " +
					"their credential had vanished")
			}

			stored := f.nameOf(t, i.ID)
			if stored != "svc-contended-first" {
				t.Errorf("stored name = %q, want the first writer's", stored)
			}

			// And the caller's own token advanced on the write that DID land, so
			// a second save from the same struct is not a conflict against
			// nobody (requireVersion's doc comment).
			if a.RowVersion != first.RowVersion+1 {
				t.Errorf("the successful writer's RowVersion is %d, want %d -- a caller "+
					"updating the same struct twice would otherwise compare a stale token "+
					"against a row it moved itself", a.RowVersion, first.RowVersion+1)
			}
		})
	}
}

// TestUpdateIdentityNeverWritesLifecycleOrLastRotated. UpdateEnvironment pins
// lifecycle for exactly this reason: a correction path that can also withdraw is
// a second withdrawal path with none of RetireIdentity's audit shape.
func TestUpdateIdentityNeverWritesLifecycleOrLastRotated(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)
			i := f.identity(t, "svc-pinned", 90)

			// A REAL rotation first, so last_rotated is non-NULL and an attempt
			// to overwrite it has something to overwrite. Pinning a NULL against
			// a NULL proves nothing.
			const rotated = "2026-06-01"
			if err := f.s.RecordIdentityRotation(f.ctx, testPermit, i.ID, rotated); err != nil {
				t.Fatalf("recording the first rotation: %v", err)
			}
			stored, err := f.s.GetIdentity(f.ctx, i.ID)
			if err != nil {
				t.Fatalf("reading it back: %v", err)
			}

			// NOW ATTEMPT THE FORBIDDEN THING. A test that simply never submits
			// these fields passes whether or not the pinning exists, which makes
			// it decoration. This submission carries a changed lifecycle, a
			// changed last_rotated AND a changed name -- the name so that the
			// update genuinely happens, because a write that was rejected
			// wholesale would leave both pinned fields unmoved for the wrong
			// reason and the test would pass on a no-op.
			attempt := stored.Identity
			attempt.Name = "svc-pinned-renamed"
			attempt.Lifecycle = domain.LifecycleRetired
			attempt.LastRotated = strPtr("2020-01-01")

			if err := f.s.UpdateIdentity(f.ctx, testPermit, &attempt); err != nil {
				t.Fatalf("UpdateIdentity: %v", err)
			}

			after, err := f.s.GetIdentity(f.ctx, i.ID)
			if err != nil {
				t.Fatalf("reading after the update: %v", err)
			}

			// The legitimate change landed, so the update was not a no-op.
			if after.Name != "svc-pinned-renamed" {
				t.Fatalf("name = %q, want the corrected one -- the update did not happen at "+
					"all, so the two assertions below would pass for the wrong reason",
					after.Name)
			}
			// lifecycle is pinned from the stored row. UpdateEnvironment and
			// UpdateInterface pin theirs for the identical reason: this method
			// would otherwise be a SECOND WITHDRAWAL PATH with none of
			// RetireIdentity's audit shape -- it would log a plain field diff
			// rather than a withdrawal, and the change_log entry an auditor
			// looks for would not be there.
			if after.Lifecycle != domain.LifecycleActive {
				t.Errorf("lifecycle = %q after a correction submitting 'retired'. A "+
					"correction form must not be able to withdraw a credential: that is "+
					"RetireIdentity's job and it writes a different audit entry.",
					after.Lifecycle)
			}
			// last_rotated is pinned because it has exactly one writer and this
			// is not it. A correction form that could also stamp the date would
			// let somebody silence an overdue finding through the edit screen,
			// with the change buried in a multi-field diff.
			if after.LastRotated == nil || *after.LastRotated != rotated {
				t.Errorf("last_rotated = %v after a correction submitting 2020-01-01, want %q. "+
					"RecordIdentityRotation is the only writer (see "+
					"internal/store/last_rotated_source_test.go).",
					derefOr(after.LastRotated, "NULL"), rotated)
			}

			// And the audit entry for the correction mentions neither pinned
			// column, so a reader of change_log is not told about a change that
			// did not happen.
			changes, err := f.s.ListChangesForEntity(f.ctx, "identity", i.ID, 10)
			if err != nil {
				t.Fatalf("reading the audit trail: %v", err)
			}
			if len(changes) == 0 {
				t.Fatal("no audit entries at all; this assertion is checking nothing")
			}
			newest := changes[0].Diff
			if strings.Contains(newest, "lifecycle") || strings.Contains(newest, "last_rotated") {
				t.Errorf("the correction's diff is %s and names a pinned column. The diff "+
					"must describe what actually changed, or the audit trail reports a "+
					"withdrawal or a rotation that never happened.", newest)
			}
		})
	}
}
```

And the structural guard, `internal/store/last_rotated_source_test.go`:

```go
// invctl — infrastructure inventory
// ... licence header ...

package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestOnlyRecordIdentityRotationWritesLastRotated keeps the spec's "One writer
// for last_rotated" true structurally rather than by review, in the shape
// TestTheOnlyFactDeletingStatementIsThePrune (prune_test.go) and
// permit_source_test.go already use for the two other rules in this package
// that are one edit away from being quietly broken.
//
// WHY IT NEEDS A TEST AT ALL. The rule is not obvious from any one call site:
// a create form that also stamps a date looks like a convenience, and it buries
// the rotation inside a create snapshot where no reader looking for a rotation
// will find it. Declaring a credential that already exists and recording when
// it was last rotated are TWO ACTS and two audit entries, both of which are
// true. That is the cost and it is the right one.
//
// IT SCANS FOR ANY MENTION, not only writes. Today no read query names the
// column either -- the list page's rotation-state filter and RotationFindings
// both fold in Go from domain.Identity.RotationStatus, exactly as
// CertificateFilter.Host matches in Go rather than in SQL -- so the strict form
// costs nothing and a future read query is a deliberate edit to the map below
// rather than a diff nobody reads.
var lastRotatedWriters = map[string]string{
	"RecordIdentityRotation": "the rotation action itself, and the only thing in " +
		"this codebase permitted to write identity.last_rotated. It stamps the " +
		"column, logs through logUpdate as an ordinary 'update' (VerifyDependency " +
		"is the precedent in every respect), and returns nil without writing " +
		"anything at all when the submitted date already matches the stored one.",
}

func TestOnlyRecordIdentityRotationWritesLastRotated(t *testing.T) {
	const column = "last_" + "rotated"

	root := repoRoot(t)
	dir := filepath.Join(root, "internal", "store")
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing internal/store: %v", err)
	}

	seen := map[string]bool{}
	found := 0
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					lit, ok := n.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						return true
					}
					s, err := strconv.Unquote(lit.Value)
					if err != nil || !strings.Contains(strings.ToLower(s), column) {
						return true
					}
					found++
					name := fn.Name.Name
					seen[name] = true
					if _, allowed := lastRotatedWriters[name]; !allowed {
						t.Errorf("%s (%s) names %s in a SQL literal.\n"+
							"last_rotated has exactly ONE writer, RecordIdentityRotation "+
							"(docs/identity-surface-design.md, \"One writer for "+
							"last_rotated\"). Not create, not correct. If this is a "+
							"legitimate READ, add it to lastRotatedWriters in this test "+
							"with the reason -- a fourth writer, or a fourth reader "+
							"nobody argued for, is what this scan exists to stop.",
							name, rel, column)
					}
					return true
				})
			}
		}
	}

	// A positive control, for the reason every census in this repository has
	// one: a scan that matches nothing reports success.
	if found == 0 {
		t.Fatalf("no SQL literal in internal/store mentions %s at all. Either the "+
			"rotation action has been deleted or this parse has stopped matching "+
			"the package -- either way this test is checking nothing.", column)
	}
	// And the other direction: an entry that stops applying fails as loudly as
	// a writer that is not listed, so the map cannot rot into decoration.
	for name, why := range lastRotatedWriters {
		if !seen[name] {
			t.Errorf("%s is listed as a %s writer (%q) and names it nowhere. A census "+
				"describing code that has moved on is one nobody can rely on.",
				name, column, why)
		}
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

```bash
git add -A
INV_TEST_POSTGRES_DSN="..." go test ./internal/store/ -run 'Identity|LastRotated|Rotation' -count=1
```
Expected: FAIL, `s.RecordIdentityRotation undefined`, and the AST test fails its own positive control (`no SQL literal ... mentions last_rotated at all`) — which is the control doing its job.

- [ ] **Step 3: Implement `internal/store/identities.go`**

Move `realmOrEmpty`, `CreateIdentity` and `ListIdentities` out of `deps.go:871-908` into the new file. Leave `deps.go`'s `---------- identities ----------` banner behind with a one-line pointer.

```go
// Credential references: what a service authenticates as, who looks after it,
// and when somebody last rotated it.
//
// secret_ref holds a PATH, never the material. The audit trail is already
// covered and must not be "improved": domain.RedactedFields holds secret_ref
// globally and both snapshotJSON and diffJSON honour it, so change_log records
// THAT it changed and never what to (TestSnapshotRedactsSecretRef,
// TestSecretRefNeverReachesTheAuditTrail). The READ path is a separate gate and
// is not free -- it lives in the handler's view model (identities.go in
// internal/web/handlers), Administrator-only, exactly where depRowData.SecretRef
// lives and for the reason that field's comment gives.

type IdentityRow struct {
	domain.Identity
	TeamCode string `db:"team_code"`
	TeamName string `db:"team_name"`
	// What would notice if this credential died. Counted for the list; listed
	// on the detail page by IdentityUsage. It is what makes withdrawal an
	// informed act rather than a blind one.
	DependencyCount int `db:"dependency_count"`
	WindowsCount    int `db:"windows_count"`
}

const identitySelect = `
	SELECT i.*,
	       COALESCE(tm.code, '') AS team_code,
	       COALESCE(tm.name, '') AS team_name,
	       (SELECT COUNT(*) FROM dependency d
	         WHERE d.identity_id = i.id AND d.lifecycle <> 'retired') AS dependency_count,
	       (SELECT COUNT(*) FROM rt_windows w
	          JOIN service_instance si ON si.id = w.instance_id
	         WHERE w.logon_identity_id = i.id AND si.lifecycle <> 'retired') AS windows_count
	FROM identity i
	LEFT JOIN team tm ON tm.id = i.team_id`

// IdentityFilter narrows an identity list.
type IdentityFilter struct {
	// Query matches name or realm, substring, ASCII-folded.
	Query  string
	Kind   string
	TeamID string
	// Rotation filters on the DERIVED state, so it is applied in Go after the
	// read rather than in SQL. Two reasons, and neither is laziness. The state
	// depends on today's date, so a SQL predicate would embed a clock in a
	// query (CLAUDE.md: never NOW()); and it depends on whether the stored
	// value PARSES, which is how RotationUnreadable is reachable at all and
	// which no portable SQL expression can decide. CertificateFilter.Host does
	// the same, for the same class of reason.
	Rotation domain.RotationState
	// IncludeRetired: a withdrawn credential must stay FINDABLE, because what
	// is stored keeps displaying (RetireEnvironment's ruling, carried over).
	// What changes on retirement is only that it stops being offered as a NEW
	// choice.
	IncludeRetired bool
}

func (s *SQLStore) ListIdentities(ctx context.Context, f IdentityFilter) ([]IdentityRow, error) {
	var where []string
	var args []any
	if !f.IncludeRetired {
		where = append(where, `i.lifecycle <> ?`)
		args = append(args, domain.LifecycleRetired)
	}
	if f.Kind != "" {
		where = append(where, `i.kind = ?`)
		args = append(args, f.Kind)
	}
	if f.TeamID != "" {
		where = append(where, `i.team_id = ?`)
		args = append(args, f.TeamID)
	}
	if f.Query != "" {
		// ASCII-only folding, the same trade ListCertificates documents: `lower`
		// folds bytes A-Z to match SQLite's LOWER(), while PostgreSQL's is
		// locale-aware. A principal name is ASCII in practice; a realm can be a
		// Windows domain and is too.
		where = append(where, `(LOWER(i.name) LIKE ? ESCAPE '\' OR LOWER(i.realm) LIKE ? ESCAPE '\')`)
		like := "%" + escapeLike(lower(f.Query)) + "%"
		args = append(args, like, like)
	}

	query := identitySelect
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	// Ordered in SQL as a hint and SORTED IN GO as the authority. expiry.go
	// states the rule -- "which of two rows sharing a date comes first must not
	// depend on the server's collation" -- and ListCertificates records that the
	// agreement observed in CI is an artefact of the Alpine/musl PostgreSQL
	// image, which implements no locale collation and so degenerates to byte
	// order.
	query += ` ORDER BY i.realm, i.name`

	var rows []IdentityRow
	if err := s.read(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("listing identities: %w", err)
	}

	if f.Rotation != "" {
		now := s.now()
		kept := rows[:0]
		for _, r := range rows {
			if r.RotationStatus(now) == f.Rotation {
				kept = append(kept, r)
			}
		}
		rows = kept
	}
	sort.SliceStable(rows, func(a, b int) bool {
		x, y := rows[a], rows[b]
		xr, yr := derefOr(x.Realm, ""), derefOr(y.Realm, "")
		if xr != yr {
			return xr < yr
		}
		if x.Name != y.Name {
			return x.Name < y.Name
		}
		return x.ID < y.ID
	})
	if f.Query != "" {
		sort.SliceStable(rows, rankNames(f.Query, func(i int) string { return rows[i].Name }))
	}
	return rows, nil
}

func (s *SQLStore) GetIdentity(ctx context.Context, id string) (*IdentityRow, error) {
	var row IdentityRow
	if err := s.readOne(ctx, &row, identitySelect+` WHERE i.id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting identity %s: %w", id, err)
	}
	return &row, nil
}

// UpdateIdentity corrects a credential reference.
//
// NOT lifecycle, and NOT last_rotated. lifecycle is pinned from the stored row
// for the reason UpdateEnvironment and UpdateInterface pin theirs: this method
// would otherwise be a second withdrawal path with none of RetireIdentity's
// audit shape. last_rotated is pinned because it has exactly one writer and
// this is not it -- see this file's header and
// internal/store/last_rotated_source_test.go.
func (s *SQLStore) UpdateIdentity(ctx context.Context, p domain.Permit, i *domain.Identity) error {
	if err := i.Validate(); err != nil {
		return err
	}
	before, err := s.GetIdentity(ctx, i.ID)
	if err != nil {
		return err
	}
	i.Lifecycle = before.Lifecycle
	i.LastRotated = before.LastRotated

	return s.write(ctx, p, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE identity
			   SET kind = ?, name = ?, realm = ?, secret_ref = ?, rotation_days = ?,
			       team_id = ?, row_version = row_version + 1
			 WHERE id = ? AND row_version = ?`,
			i.Kind, i.Name, realmOrEmpty(i.Realm), i.SecretRef, i.RotationDays,
			i.TeamID, i.ID, i.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating identity")
		}
		if err := requireVersion(res, "identity", i.ID, &i.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "identity", i.ID, &before.Identity, i)
	})
}

// RetireIdentity withdraws a credential reference.
//
// REFUSES NOTHING AND REWRITES NOTHING, and migration 00003 states the case:
// "the natural response to a compromised credential is to retire it and create
// its replacement under the same name", which is why the uniqueness index is
// scoped to lifecycle = 'active'. Blocking retirement until every dependency has
// been re-pointed would mean, at exactly the wrong moment, that the compromised
// credential cannot be withdrawn. And rewriting dependency.identity_id or
// rt_windows.logon_identity_id automatically would write change_log entries
// attributing a dependency change to whoever clicked withdraw -- the
// misattribution RetireInterface and RetireEnvironment both refuse to commit.
//
// WHAT STAYS TRUE: what is STORED keeps displaying. A dependency still naming a
// retired identity shows it, marked retired. What changes is that it stops being
// offered as a NEW choice.
//
// NO RESTORE PATH, and no trap in that: the live-scoped unique index means a
// retired identity's (realm, name) is immediately available to a replacement, so
// nobody is ever stranded -- and a restore would put two live rows in contention
// for one name.
func (s *SQLStore) RetireIdentity(ctx context.Context, p domain.Permit, id string) error {
	before, err := s.GetIdentity(ctx, id)
	if err != nil {
		return err
	}
	if before.Lifecycle == domain.LifecycleRetired {
		// Already withdrawn: a second audit entry would claim a withdrawal that
		// did not happen. RetireEnvironment and RetireTeam do the same.
		return nil
	}
	after := before.Identity
	after.Lifecycle = domain.LifecycleRetired

	return s.write(ctx, p, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE identity SET lifecycle = ?, row_version = row_version + 1
			 WHERE id = ? AND row_version = ?`,
			domain.LifecycleRetired, id, before.RowVersion)
		if err != nil {
			return translateWriteErr(err, "retiring identity")
		}
		v := before.RowVersion
		if err := requireVersion(res, "identity", id, &v); err != nil {
			return err
		}
		return t.logUpdate(ctx, "identity", id, &before.Identity, &after)
	})
}

// RecordIdentityRotation stamps the day somebody rotated this credential.
//
// THIS IS THE FEATURE, not a side effect of an edit form, because it is the
// thing somebody actually does. VerifyDependency is the precedent in every
// respect: a distinct store method and a distinct route that stamp one column
// and log through logUpdate, with action staying 'update' -- the change_log
// CHECK allows exactly create|update|delete|retire and adding a value to it
// would mean rebuilding the table on SQLite (docs/AUDIT.md rule 10).
//
// THE LOG *IS* THE HISTORY. The table keeps only the latest date; change_log
// keeps all of them, permanently, append-only, each with its actor. The detail
// page's timeline shows a rotation as a last_rotated change. Do NOT build a
// rotation-specific history query: that means a predicate inside `diff`, and
// querying inside JSON is banned (rule 15 -- fold in Go).
//
// NO ATTESTATION CHECK, deliberately, unlike VerifyDependency. Recording a
// rotation is a statement of fact about a credential, not a person putting their
// name to an edge; no column stores who rotated it, only change_log.actor does,
// and that is unforgeable by construction (rule 5). Applying CheckAttestationWrite
// would also break the seeder, which writes as SystemActor. Agent credentials
// never reach this path anyway -- rule 6 forbids an agent-reachable handler from
// returning an identity row at all.
func (s *SQLStore) RecordIdentityRotation(ctx context.Context, p domain.Permit, id, date string) error {
	date = strings.TrimSpace(date)
	when, err := domain.ParseDate(date)
	if err != nil {
		ve := &domain.ValidationError{}
		ve.Add("last_rotated", "must be a real date in the form %s", domain.DateFormat)
		return ve
	}
	// BACKDATED YES, POST-DATED NEVER, and the asymmetry is the whole argument.
	// Somebody rotated the credential last Tuesday and is catching up on
	// Thursday; refusing that forces them to record a date they know is wrong.
	// A PAST date can only ever make a finding worse -- it can move a credential
	// from "within window" to "overdue" and can never hide anything. A FUTURE
	// date hides an overdue finding for a rotation that has not happened, which
	// is the one direction that turns this feature into a way of silencing
	// itself. No monotonicity rule: an earlier date than the stored one is
	// allowed, because the stored one may simply have been wrong, and change_log
	// records both values and the actor.
	today := s.now().UTC().Truncate(24 * time.Hour)
	if when.After(today) {
		ve := &domain.ValidationError{}
		ve.Add("last_rotated", "a rotation cannot be recorded in the future; "+
			"today is %s", domain.FormatDate(s.now()))
		return ve
	}

	before, err := s.GetIdentity(ctx, id)
	if err != nil {
		return err
	}
	if before.Lifecycle == domain.LifecycleRetired {
		// Rotating a withdrawn credential is not a thing that happened. Refused
		// before anything opens a transaction, with a field message naming the
		// identity -- the shape SetBundleMembers uses for a retired bundle.
		ve := &domain.ValidationError{}
		ve.Add("last_rotated", "%s has been withdrawn", before.Name)
		return ve
	}
	if before.LastRotated != nil && *before.LastRotated == date {
		// NOTHING AT ALL: no UPDATE, no change_log row, no row_version bump.
		// Without this, diffJSON returns ok=false, logUpdate skips the entry,
		// and the UPDATE still moves row_version -- a declared-state write with
		// NO AUDIT ROW, which since WP-G1 is an authorization bypass and not
		// merely an untraceable change. Returns nil the way RetireEnvironment
		// does for an already-retired row, rather than claiming a rotation that
		// did not happen.
		return nil
	}

	after := before.Identity
	after.LastRotated = &date

	return s.write(ctx, p, func(t *tx) error {
		// NO row_version GUARD, and it BUMPS. VerifyDependency exactly: two
		// operators recording a rotation of the same credential is not a
		// conflict worth a 409. The cost is stated rather than hidden -- an
		// operator with a correction form already open gets a spurious 409
		// after somebody else records a rotation, even though the form shows no
		// field that moved. That is accepted, because the alternative (a token
		// that does not move when the row changes) is worse, and because
		// requireVersion's contract is about the ROW, not a subset of its fields.
		if _, err := t.exec(ctx,
			`UPDATE identity SET last_rotated = ?, row_version = row_version + 1
			  WHERE id = ?`, date, id); err != nil {
			return translateWriteErr(err, "recording identity rotation")
		}
		return t.logUpdate(ctx, "identity", id, &before.Identity, &after)
	})
}

// IdentityUsageRows is what still names a credential.
type IdentityUsageRows struct {
	Dependencies []IdentityDependencyUse
	Windows      []IdentityWindowsUse
}

type IdentityDependencyUse struct {
	DependencyID  string `db:"dependency_id"`
	ConsumerID    string `db:"consumer_id"`
	ConsumerCode  string `db:"consumer_code"`
	ProviderName  string `db:"provider_name"`
	AuthMethod    string `db:"auth_method"`
}

type IdentityWindowsUse struct {
	InstanceID  string `db:"instance_id"`
	ServiceID   string `db:"service_id"`
	ServiceCode string `db:"service_code"`
	ServiceName string `db:"service_name"`
}

// IdentityUsage answers "what would notice if this credential died".
//
// LIVE ROWS ONLY. A retired dependency naming this identity is history, and the
// question the withdrawal screen asks is about what is still running.
func (s *SQLStore) IdentityUsage(ctx context.Context, id string) (*IdentityUsageRows, error) {
	var deps []IdentityDependencyUse
	if err := s.read(ctx, &deps, `
		SELECT d.id AS dependency_id,
		       COALESCE(cs.id, '')   AS consumer_id,
		       COALESCE(cs.code, '') AS consumer_code,
		       COALESCE(pe.name, '') AS provider_name,
		       COALESCE(d.auth_method, '') AS auth_method
		FROM dependency d
		LEFT JOIN service  cs ON cs.id = d.consumer_service_id
		LEFT JOIN endpoint pe ON pe.id = d.provider_endpoint_id
		WHERE d.identity_id = ? AND d.lifecycle <> ?`,
		id, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("listing dependencies naming identity %s: %w", id, err)
	}
	var wins []IdentityWindowsUse
	if err := s.read(ctx, &wins, `
		SELECT w.instance_id AS instance_id,
		       COALESCE(sv.id, '')   AS service_id,
		       COALESCE(sv.code, '') AS service_code,
		       w.service_name        AS service_name
		FROM rt_windows w
		JOIN service_instance si ON si.id = w.instance_id
		LEFT JOIN service sv ON sv.id = si.service_id
		WHERE w.logon_identity_id = ? AND si.lifecycle <> ?`,
		id, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("listing windows services naming identity %s: %w", id, err)
	}
	// Sorted in Go, for the collation reason ListIdentities states.
	// Sorted in Go, for the collation reason ListIdentities states. Ties broken
	// on the id so the order is total: two edges from the same consumer to the
	// same endpoint are legitimate, and an unstable order between them makes a
	// golden test flap on one engine and not the other.
	sort.SliceStable(deps, func(a, b int) bool {
		x, y := deps[a], deps[b]
		if x.ConsumerCode != y.ConsumerCode {
			return x.ConsumerCode < y.ConsumerCode
		}
		if x.ProviderName != y.ProviderName {
			return x.ProviderName < y.ProviderName
		}
		return x.DependencyID < y.DependencyID
	})
	sort.SliceStable(wins, func(a, b int) bool {
		x, y := wins[a], wins[b]
		if x.ServiceCode != y.ServiceCode {
			return x.ServiceCode < y.ServiceCode
		}
		if x.ServiceName != y.ServiceName {
			return x.ServiceName < y.ServiceName
		}
		return x.InstanceID < y.InstanceID
	})
	return &IdentityUsageRows{Dependencies: deps, Windows: wins}, nil
}
```

- [ ] **Step 4: Convert the two existing `ListIdentities` call sites — behaviour-preserving, and read the comment**

`internal/web/handlers/deps.go:238` and `internal/web/handlers/services.go:340`:

```go
	// IncludeRetired, deliberately, and this is NOT the identity list page's
	// default. This slice feeds the dependency identity <select> -- both the
	// create form (partials/forms.html:646) and the inline correction row
	// (partials/rows.html:69). A dependency may legitimately name a RETIRED
	// credential: RetireIdentity refuses nothing and rewrites nothing, so
	// "what is stored keeps displaying" (migration 00003, and RetireEnvironment's
	// ruling). Narrowing this to live rows would drop the currently-selected
	// option out of the <select>, the browser would fall back to the empty first
	// option, and SAVING THE CORRECTION ROW WOULD SILENTLY CLEAR identity_id --
	// a data change nobody asked for, on a form about something else.
	//
	// AND THE REASON A LIVE EDGE NAMING A RETIRED CREDENTIAL IS NORMAL rather
	// than a mess to tidy up: migration 00003 records that "the natural response
	// to a compromised credential is to retire it and create its replacement
	// under the same name", which is exactly why the uniqueness index is scoped
	// to lifecycle = 'active'. So the sequence the estate is BUILT for -- retire
	// the compromised credential now, re-point the edges as the services are
	// redeployed over the following days -- leaves live dependencies naming a
	// retired identity for as long as that takes, by design. RetireIdentity
	// refuses nothing and rewrites nothing precisely so that it can. The edit
	// form must therefore keep displaying what is STORED, marked retired, the
	// same rule RetireEnvironment states for every label that behaves this way.
	//
	// Without that reasoning written here, the next person narrows this to live
	// rows believing it is tidy-up, and the tidy-up silently clears identity_id
	// on the edges of exactly the credential somebody is mid-incident about.
	//
	// The consequence is that a retired credential is still offered on the
	// CREATE form, which is the pre-WP-J8 behaviour (ListIdentities took no
	// filter and returned everything). Narrowing only the create half needs two
	// slices and the "marked retired, not newly selectable" treatment the
	// custom_field_option / environment pickers get; that is a separate piece of
	// work and is deliberately not done here.
	identities, err := a.Store.ListIdentities(r.Context(), store.IdentityFilter{IncludeRetired: true})
```

Add a regression test for exactly this, in `internal/web/identities_test.go`:

```go
// TestADependencyNamingARetiredIdentityKeepsIt is the regression the filter
// change could have introduced. See the comment at the two ListIdentities call
// sites: an option that vanishes from a <select> does not leave the field
// unchanged, it clears it.
func TestADependencyNamingARetiredIdentityKeepsIt(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)

	identity, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-still-named",
		Realm: strPtr("vault"),
	})
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, identity); err != nil {
		t.Fatalf("creating identity: %v", err)
	}

	consumer := h.refs.Services["orders-api"]
	endpoint := h.lookup(`SELECT id FROM endpoint ORDER BY id LIMIT 1`)
	dep, err := domain.NewDependency(store.NewID(), domain.DependencySpec{
		ConsumerServiceID:  consumer,
		ProviderEndpointID: &endpoint,
		Nature:             domain.NatureHard,
		FailureMode:        "orders cannot authenticate",
		IdentityID:         &identity.ID,
	}, h.store.Now())
	if err != nil {
		t.Fatalf("building dependency: %v", err)
	}
	if err := h.store.CreateDependency(ctx, admin, dep, nil); err != nil {
		t.Fatalf("creating dependency: %v", err)
	}

	page := "/services/" + consumer + "?edit=" + dep.ID
	marker := `value="` + identity.ID + `"`

	// POSITIVE CONTROL: the option is there while the credential is live, so
	// the assertion after the withdrawal is about the filter and not about the
	// picker never having rendered.
	before := body(t, h.get(page, false))
	if !strings.Contains(before, marker) {
		t.Fatalf("the correction row does not offer %s even while it is live; this test "+
			"would then prove nothing", identity.Name)
	}

	if err := h.store.RetireIdentity(ctx, admin, identity.ID); err != nil {
		t.Fatalf("withdrawing the credential: %v", err)
	}

	after := body(t, h.get(page, false))
	if !strings.Contains(after, marker) {
		t.Fatalf("the correction row no longer offers the RETIRED credential the "+
			"dependency names. An option that vanishes from a <select> does not leave "+
			"the field unchanged -- the browser falls back to the empty first option, "+
			"and saving the row silently clears identity_id on the edges of exactly "+
			"the credential somebody is mid-incident about. ListIdentities is called "+
			"with IncludeRetired: true at internal/web/handlers/deps.go and "+
			"services.go for this reason.")
	}
	// And it is still the SELECTED one, not merely present in the list.
	idx := strings.Index(after, marker)
	option := after[idx:min(idx+200, len(after))]
	if !strings.Contains(option, "selected") {
		t.Errorf("the retired credential is offered but not selected, so the form would "+
			"still save a different value than the one stored: %q", option)
	}
}
```

- [ ] **Step 5: Empty `writeSurfaceUnbuilt`**

`internal/store/write_surface_test.go:138-159`. `UpdateIdentity` and `RetireIdentity` now exist, and the map is two-directional — *"an entity that grows both verbs while still listed here fails"* — so the entry must go in this commit or the build breaks. Replace the map body, keeping the type and the doc comment that explains what the third answer is for:

```go
// WP-J8 CLOSED THIS CENSUS TO ZERO on 2026-09-15, and the empty map stays
// rather than being deleted: it is the third answer, and an entity that ships
// with no surface at all in future needs somewhere to be recorded that is not
// "a working feature missing a repair path". Identity was the only entry and it
// left when internal/store/identities.go gave it UpdateIdentity, RetireIdentity
// and RecordIdentityRotation, and internal/web/routes.go put four routes in
// front of them.
var writeSurfaceUnbuilt = map[string]string{}
```

If the census asserts a non-empty population anywhere, check for a `len(writeSurfaceUnbuilt) > 0` guard and convert it — a map that can only shrink reaching zero is the success condition, not a vacuous test. (`writeSurfaceGaps` and the alias maps remain the real populations.)

- [ ] **Step 6: Run everything, both engines**

```bash
git add -A
INV_TEST_POSTGRES_DSN="..." go test ./internal/store/ ./internal/web/... ./internal/seed/ -count=1
```

- [ ] **Step 7: Prove each guard can fail**

| Mutation | Test that must go red |
|---|---|
| Delete the `before.LastRotated != nil && *before.LastRotated == date` short-circuit | `TestRecordingTheStoredRotationDateWritesNothingAtAll` — **on the `change_log` count assertion**, and this is the one to watch: the `row_version` assertion also goes red, and they are different failures. The audit-row assertion is the property; the version assertion is the mechanism. |
| Change `when.After(today)` to `when.Before(today)` | `TestARotationMayBeBackdatedAndNeverPostDated/tomorrow_is_refused` **and** `/yesterday_is_fine` — one in each direction, which is what proves the asymmetry rather than merely a bound |
| Delete the `before.Lifecycle == domain.LifecycleRetired` check in `RecordIdentityRotation` | `TestRecordIdentityRotationRefusesARetiredIdentity` |
| Replace `t.logUpdate(...)` in `RecordIdentityRotation` with `return nil` | `TestRecordIdentityRotationWritesExactlyOneChangeLogRow` |
| Add `last_rotated` to `UpdateIdentity`'s `SET` clause, binding `i.LastRotated` | `TestOnlyRecordIdentityRotationWritesLastRotated` (AST) **and** `TestUpdateIdentityNeverWritesLifecycleOrLastRotated` (behavioural). **These fail at different layers and both matter**: the AST one fails even if no test ever calls `UpdateIdentity` with a changed date, which is the whole reason it exists. |
| Remove `AND row_version = ?` from `UpdateIdentity` | `TestUpdateIdentityRefusesAStaleToken` |
| Make `RetireIdentity` return `domain.ErrConflict` when `DependencyCount > 0` | `TestRetireIdentityRefusesNothingAndRewritesNothing` |
| Restore the `writeSurfaceUnbuilt` entry | `TestWriteSurfaceCensus` (whatever the two-directional check is named in `write_surface_test.go`) |

- [ ] **Step 8: Commit**

```bash
git add -A && make lint
git commit   # why: rotation_days is meaningless until something can record a
             # rotation, and last_rotated needs exactly one writer or the audit
             # entry stops being findable
```

---

### Task 4: The read surface — two pages that make the rotation state visible

Read-only, so it leaves the branch demoable with no write routes yet: `/identities` lists the three seeded credentials with their rotation pills, and `/identities/{id}` opens.

**Files:**
- Create: `internal/web/handlers/identities.go`, `web/templates/pages/identity_list.html`, `web/templates/pages/identity_detail.html`, `web/templates/partials/identities.html`, `internal/web/identities_test.go` (already started in Task 3)
- Modify: `internal/web/routes.go`, `internal/web/handlers/nav.go`, `internal/web/detail_pages_render_test.go`

**Interfaces:**
- Produces: `GET /identities` → `app.IdentityList`, `GET /identities/{id}` → `app.IdentityDetail`, both registered with `read(...)`.

- [ ] **Step 1: Write the failing tests**

```go
// TestTheIdentityListNeverRendersASecretPath is the spec's hardest read rule:
// "One screen listing every credential path in the estate is the reconnaissance
// gift ... in its most convenient possible form, and it is one CSV export away
// from leaving the building." Not for an Administrator either -- for ANYBODY.
func TestTheIdentityListNeverRendersASecretPath(t *testing.T) {
	h := newHarness(t)
	// A POSITIVE CONTROL FIRST: the fixture must actually hold a path, or this
	// test passes by finding nothing because there was nothing.
	stored := h.lookup(`SELECT secret_ref FROM identity WHERE secret_ref IS NOT NULL LIMIT 1`)
	if stored == "" {
		t.Fatal("no seeded identity carries a secret_ref, so this test proves nothing")
	}
	for _, who := range []struct{ user, pass string }{
		{"admin", "admin-password"},
		{"viewer", "viewer-password"},
	} {
		h.login(who.user, who.pass)
		page := body(t, h.get("/identities", false))
		if strings.Contains(page, stored) {
			t.Errorf("the identity list rendered %q to %s. The list shows only WHETHER a "+
				"path is recorded; the path renders on the detail page, to an "+
				"Administrator, one credential at a time.", stored, who.user)
		}
		if !strings.Contains(page, "recorded") {
			t.Errorf("the list does not say whether a path is recorded at all for %s", who.user)
		}
	}
}

// TestSecretRefOnTheDetailPageIsAdministratorOnly. depRowData.SecretRef's own
// comment is the reasoning: "a template-side {{if .IsAdmin}} around
// .Dep.IdentitySecretRef is one {{end}} away from leaking it, and it does
// nothing at all for a CSV export, which never passes through a template." So
// it is computed in the view model, and this drives both sides.
func TestSecretRefOnTheDetailPageIsAdministratorOnly(t *testing.T) {
	h := newHarness(t)

	// The fixture builds its own credential rather than leaning on the seed, so
	// this task's tests do not depend on Task 6 having landed. The path is
	// distinctive enough that finding it in a page cannot be a coincidence.
	const path = "kv/prod/identity-disclosure-test/db"
	// strPtr already lives in this package (users_test.go:29). There is no
	// intPtr, and adding one for two call sites is worse than a named
	// variable that says what the number means.
	ninetyDays := 90
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)
	identity, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-disclosure-test",
		Realm: strPtr("vault"), SecretRef: strPtr(path), RotationDays: &ninetyDays,
	})
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, identity); err != nil {
		t.Fatalf("creating identity: %v", err)
	}

	t.Run("an administrator sees the path", func(t *testing.T) {
		h.login("admin", "admin-password")
		page := body(t, h.get("/identities/"+identity.ID, false))
		if !strings.Contains(page, path) {
			t.Errorf("the detail page does not render the secret path to an Administrator. "+
				"The path renders HERE, one credential at a time -- that is the whole "+
				"trade the list page's blanket refusal buys.")
		}
	})

	t.Run("a read-only user sees the page and not the path", func(t *testing.T) {
		h.login("viewer", "viewer-password")
		resp := h.get("/identities/"+identity.ID, false)
		page := body(t, resp)
		// The GET is deliberately NOT gated: name, realm, kind, team and
		// rotation status are what somebody needs mid-incident.
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET the detail page as viewer returned %d, want 200 -- the read "+
				"surface is open to any authenticated user", resp.StatusCode)
		}
		if !strings.Contains(page, identity.Name) {
			t.Errorf("the page does not name the credential at all, so the assertion " +
				"below would pass on an error page rather than on redaction")
		}
		if strings.Contains(page, path) {
			t.Errorf("the detail page rendered %q to a read-only user. The gate lives in "+
				"the handler's view model, not the template -- depRowData.SecretRef's "+
				"comment: a template-side {{if .IsAdmin}} is one {{end}} away from "+
				"leaking it, and it does nothing at all for a CSV export.", path)
		}
	})
}

// TestTheIdentityListRendersEveryRotationState is the reason the page exists.
// Five identities, five pills, and RotationUnreadable must NOT read as healthy.
func TestTheIdentityListRendersEveryRotationState(t *testing.T) {
	// builds five identities directly through the store, including one whose
	// last_rotated is written past the Go layer to an unparseable value -- the
	// only way to reach RotationUnreadable, since the CHECK and ParseDate both
	// refuse it on the way in. Use the raw writer with a value the CHECK allows
	// but ParseDate does not: "2026-02-31" is ten characters with dashes in
	// positions 5 and 8, so the database accepts it and Go will not parse it.
	// THAT COMBINATION IS THE WHOLE POINT of the state existing.
	//
	// THIS IS WHY TASK 4 TESTS THE STATE AND TASK 6 DOES NOT SEED IT. A test may
	// write 2026-02-31 straight past the Go layer because a test is entitled to
	// manufacture a corrupt row in order to prove the software survives one; the
	// demo estate deliberately seeds four of the five states and never this one,
	// because a seeded corrupt row teaches every reader of the demo that the
	// estate produces them, which it does not and must not.
	for _, want := range []string{"no policy", "never recorded", "due in", "overdue by", "date unreadable"} {
		if !strings.Contains(page, want) { t.Errorf("the list never renders %q", want) }
	}
	// The unreadable row must not carry ANY of the healthy vocabulary. Asserted
	// against the row rather than the page, because "due in" legitimately
	// appears on the within-window row three lines above it.
	for _, healthy := range []string{"due in", "within", "no policy"} {
		if strings.Contains(unreadableRow, healthy) {
			t.Errorf("the row for a credential whose stored date will not parse renders "+
				"%q. A state that cannot be READ must never render as a state that is "+
				"FINE -- the boolean this replaced returned false on a parse error, "+
				"which is exactly how an unreadable value read as healthy.", healthy)
		}
	}
}

// TestTheIdentityDetailPageStatesAllThreeRotationFactsPlainly: the policy, the
// last recorded rotation (never a blank -- "never recorded"), and the derived
// state.
func TestTheIdentityDetailPageStatesAllThreeRotationFactsPlainly(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)
	now := h.store.Now()

	mk := func(t *testing.T, name string, rotationDays int, rotatedDaysAgo int) string {
		t.Helper()
		spec := domain.IdentitySpec{
			Kind: domain.IdentityServiceAccount, Name: name, Realm: strPtr("vault"),
		}
		if rotationDays > 0 {
			spec.RotationDays = &rotationDays
		}
		i, err := domain.NewIdentity(store.NewID(), spec)
		if err != nil {
			t.Fatalf("building %s: %v", name, err)
		}
		if err := h.store.CreateIdentity(ctx, admin, i); err != nil {
			t.Fatalf("creating %s: %v", name, err)
		}
		if rotatedDaysAgo > 0 {
			date := domain.FormatDate(now.AddDate(0, 0, -rotatedDaysAgo))
			if err := h.store.RecordIdentityRotation(ctx, admin, i.ID, date); err != nil {
				t.Fatalf("rotating %s: %v", name, err)
			}
		}
		return i.ID
	}

	withinID := mk(t, "svc-within", 90, 30)
	neverID := mk(t, "svc-never", 90, 0)
	unmanagedID := mk(t, "svc-unmanaged", 0, 0)

	t.Run("a rotated credential states the policy, the date and the state", func(t *testing.T) {
		page := body(t, h.get("/identities/"+withinID, false))
		for _, want := range []string{
			"every 90 days",                                        // the policy
			domain.FormatDate(now.AddDate(0, 0, -30)),              // the last recorded rotation
			"due in",                                               // the derived state
		} {
			if !strings.Contains(page, want) {
				t.Errorf("the rotation panel does not state %q. All three facts are stated "+
					"plainly and none is collapsed into another.", want)
			}
		}
	})

	t.Run("a credential with a policy and no record says so, never a blank", func(t *testing.T) {
		page := body(t, h.get("/identities/"+neverID, false))
		if !strings.Contains(page, "every 90 days") {
			t.Error("the policy is not stated, so a reader cannot tell there is a rule at all")
		}
		if !strings.Contains(page, "never recorded") {
			t.Error("the last rotation reads as something other than 'never recorded'. A " +
				"BLANK here is the defect: it reads as a field nobody filled in rather " +
				"than as the estate having a rule with no evidence it was ever followed.")
		}
	})

	t.Run("an unmanaged credential says there is no policy, and is not overdue", func(t *testing.T) {
		page := body(t, h.get("/identities/"+unmanagedID, false))
		if !strings.Contains(page, "no rotation policy") {
			t.Error("the panel does not say there is no rotation policy")
		}
		// The two states this page must never confuse. `unmanaged` has no rule
		// to break; reporting it as late is how a page teaches people to ignore
		// it, which is the reasoning EstateFindings already applies to expected
		// power convergence.
		for _, wrong := range []string{"overdue", "never recorded"} {
			if strings.Contains(page, wrong) {
				t.Errorf("a credential with no rotation policy renders %q. There is nothing "+
					"for it to be late for, and nothing anybody failed to record.", wrong)
			}
		}
	})
}

// TestTheIdentityDetailPageNamesWhatWouldNotice drives the used-by panel,
// including the case the spec names: a LIVE dependency naming a RETIRED identity
// still displays it, marked retired.
func TestTheIdentityDetailPageNamesWhatWouldNotice(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)
	ninetyDays := 90

	t.Run("a Windows service naming a credential appears", func(t *testing.T) {
		// THE SEEDED ESTATE ALREADY HAS THIS ONE, and it is the real thing
		// rather than a fixture: seed_services.go:260 makes svc-backup$ the
		// logon account of a Windows service, because "the run-as account is
		// the usual reason a Windows service dies after a credential rotation".
		id := h.lookup(`SELECT id FROM identity WHERE name = ?`, "svc-backup$")
		if id == "" {
			t.Fatal("svc-backup$ is not in the fixture, so the rt_windows join is " +
				"checked nowhere -- seed it rather than letting this pass")
		}
		page := body(t, h.get("/identities/"+id, false))
		if !strings.Contains(page, "Veeam") {
			t.Error("the used-by panel does not name the Windows service that logs on as " +
				"this credential. That panel is what makes withdrawal an informed act.")
		}
	})

	t.Run("a live dependency naming a RETIRED credential still shows it", func(t *testing.T) {
		// Built here rather than taken from the seed, so this task does not
		// depend on Task 6. The state is normal by design: migration 00003's
		// response to a compromise is retire-then-replace, so edges keep naming
		// the withdrawn credential until they are re-pointed.
		identity, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
			Kind: domain.IdentityServiceAccount, Name: "svc-compromised",
			Realm: strPtr("vault"), RotationDays: &ninetyDays,
		})
		if err != nil {
			t.Fatalf("building identity: %v", err)
		}
		if err := h.store.CreateIdentity(ctx, admin, identity); err != nil {
			t.Fatalf("creating identity: %v", err)
		}

		consumer := h.refs.Services["orders-api"]
		// Any seeded endpoint will do: this test is about the identity panel,
		// not about which edge it is. lookup fails the test loudly if the
		// fixture has none, rather than substituting an id that resolves to
		// nothing and producing a page that renders empty for the wrong reason.
		endpoint := h.lookup(`SELECT id FROM endpoint ORDER BY id LIMIT 1`)
		dep, err := domain.NewDependency(store.NewID(), domain.DependencySpec{
			ConsumerServiceID:  consumer,
			ProviderEndpointID: &endpoint,
			Nature:             domain.NatureHard,
			FailureMode:        "orders cannot authenticate",
			IdentityID:         &identity.ID,
		}, h.store.Now())
		if err != nil {
			t.Fatalf("building dependency: %v", err)
		}
		if err := h.store.CreateDependency(ctx, admin, dep, nil); err != nil {
			t.Fatalf("creating dependency: %v", err)
		}
		if err := h.store.RetireIdentity(ctx, admin, identity.ID); err != nil {
			t.Fatalf("withdrawing the credential: %v", err)
		}

		page := body(t, h.get("/identities/"+identity.ID, false))
		if !strings.Contains(page, "orders-api") {
			t.Error("the used-by panel is empty for a WITHDRAWN credential that a live " +
				"dependency still names. RetireEnvironment's ruling carries over: what " +
				"is stored keeps displaying. Hiding it here is how somebody concludes " +
				"the withdrawal was clean when a service is still authenticating with it.")
		}
		if !strings.Contains(page, "retired") {
			t.Error("the page does not mark the credential retired, so a reader cannot " +
				"tell the difference between a live credential and a withdrawn one")
		}
	})
}

// TestTheIdentityListFiltersFindARetiredCredential: "so a retired credential can
// be found".
func TestTheIdentityListFiltersFindARetiredCredential(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)

	identity, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-findable-after-withdrawal",
		Realm: strPtr("vault"),
	})
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, identity); err != nil {
		t.Fatalf("creating identity: %v", err)
	}

	// A positive control: it is on the default list BEFORE withdrawal, so the
	// assertion after it is about the filter and not about the row never having
	// rendered at all.
	if page := body(t, h.get("/identities", false)); !strings.Contains(page, identity.Name) {
		t.Fatalf("%s is not on the default list before withdrawal; this test would then "+
			"prove nothing about the filter", identity.Name)
	}
	if err := h.store.RetireIdentity(ctx, admin, identity.ID); err != nil {
		t.Fatalf("withdrawing: %v", err)
	}

	if page := body(t, h.get("/identities", false)); strings.Contains(page, identity.Name) {
		t.Errorf("%s is still on the DEFAULT list after withdrawal. The default is what a "+
			"picker starts from, and a withdrawn credential must not be offered as a new "+
			"choice.", identity.Name)
	}
	page := body(t, h.get("/identities?lifecycle=retired", false))
	if !strings.Contains(page, identity.Name) {
		t.Errorf("%s cannot be found under the retired filter. A credential somebody "+
			"withdrew by mistake, or one an incident review needs to look at, is "+
			"unreachable through the application if this filter does not work -- and "+
			"there is no restore path by design, so finding it is the only recourse.",
			identity.Name)
	}
}
```

Add the row to `internal/web/detail_pages_render_test.go:47-62`:

```go
		{"identity", "/identities/", `SELECT id FROM identity WHERE lifecycle = 'active' LIMIT 1`},
```

- [ ] **Step 2: Run them and watch them fail**

`go test ./internal/web/ -run 'Identity' -count=1` → 404 on `/identities`.

- [ ] **Step 3: Implement the handlers**

`internal/web/handlers/identities.go`. The two view-model decisions are the load-bearing part:

```go
// Credential references: what a service authenticates as, and whether anybody
// has recorded rotating it.
//
// TWO DIFFERENT ANSWERS TO secret_ref ON TWO PAGES, and the difference is not a
// stylistic one.
//
// The LIST carries no path at all, for anybody, which is why identityListRow
// exists as a PROJECTION rather than the page simply holding []store.IdentityRow
// and the template declining to print one field. A row type with no SecretRef
// field cannot leak one through a template edit, a CSV export, or a debug dump.
// One screen listing every credential path in the estate is a reconnaissance
// gift in its most convenient possible form (domain.RedactedFields' own comment,
// and docs/AUDIT.md rule 12), and it is one export away from leaving the
// building.
//
// The DETAIL page renders the path, to an Administrator, one credential at a
// time -- computed HERE, in the handler, exactly where depRowData.SecretRef is
// computed (internal/web/handlers/forms.go:846-854) and for the reason that
// field's comment gives: "a template-side {{if .IsAdmin}} around
// .Dep.IdentitySecretRef is one {{end}} away from leaking it, and it does
// nothing at all for a CSV export, which never passes through a template."

type identityListRow struct {
	ID        string
	Name      string
	Realm     string
	Kind      string
	TeamID    string
	TeamCode  string
	Lifecycle string
	// Rotation is the derived state, and DueOn is non-empty only for the two
	// states where a due date is meaningful.
	Rotation string
	DueOn    string
	// DaysUntilDue is signed: positive is "due in n", negative is "overdue by
	// n". Computed in Go because a template must not do arithmetic on dates.
	DaysUntilDue int
	// HasSecretRef says WHETHER a path is recorded. Never the path.
	HasSecretRef bool
	Uses         int
}

type identityListPage struct {
	Base
	Errors     map[string]string
	Identities []identityListRow
	Teams      []store.TeamRow
	Kinds      []string
	States     []string
	Filter     store.IdentityFilter
	Spec       domain.IdentitySpec // Task 5's create form; empty here
}

type identityPage struct {
	Base
	Errors   map[string]string
	Identity *store.IdentityRow
	// SecretRef is the stored path, ALREADY GATED: empty for anyone who is not
	// a full Administrator, and empty when the credential carries none. See
	// this file's header for why the gate is here and not in the template.
	SecretRef string
	Rotation  string
	DueOn     string
	// Today backs the rotation form's default value, generated in Go from the
	// store's clock -- never from the browser, which would let a client's wrong
	// clock post-date a rotation past a check made against the server's.
	Today    string
	Usage    *store.IdentityUsageRows
	Timeline []store.TimelineEntry
	Teams    []store.TeamRow
	Kinds    []string
}

func (a *App) IdentityList(w http.ResponseWriter, r *http.Request) {
	a.renderIdentityList(w, r, http.StatusOK, nil, domain.IdentitySpec{})
}

func (a *App) renderIdentityList(w http.ResponseWriter, r *http.Request, status int,
	errs map[string]string, spec domain.IdentitySpec) {

	q := r.URL.Query()
	filter := store.IdentityFilter{
		Query:          q.Get("q"),
		Kind:           q.Get("kind"),
		TeamID:         q.Get("team"),
		Rotation:       domain.RotationState(q.Get("rotation")),
		IncludeRetired: q.Get("lifecycle") == domain.LifecycleRetired || q.Get("lifecycle") == "any",
	}
	rows, err := a.Store.ListIdentities(r.Context(), filter)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if q.Get("lifecycle") == domain.LifecycleRetired {
		kept := rows[:0]
		for _, row := range rows {
			if row.Lifecycle == domain.LifecycleRetired {
				kept = append(kept, row)
			}
		}
		rows = kept
	}
	teams, _ := a.responsibilityOptions(r)
	now := a.Store.Now()

	out := make([]identityListRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, identityListRow{
			ID: row.ID, Name: row.Name, Realm: derefOr(row.Realm),
			Kind: row.Kind, TeamID: derefOr(row.TeamID), TeamCode: row.TeamCode,
			Lifecycle:    row.Lifecycle,
			Rotation:     string(row.RotationStatus(now)),
			DueOn:        derefOr(row.RotationDueOn()),
			DaysUntilDue: daysUntil(now, row.RotationDueOn()),
			HasSecretRef: row.SecretRef != nil,
			Uses:         row.DependencyCount + row.WindowsCount,
		})
	}

	a.Render.Respond(w, r, status, "identity_list", "identity_list_panel", identityListPage{
		Base:       a.base(r, "Identities", "identities"),
		Errors:     orEmpty(errs),
		Identities: out,
		Teams:      teams,
		Kinds:      domain.IdentityKinds,
		States:     domain.RotationStates, // add the slice beside IdentityKinds
		Filter:     filter,
		Spec:       spec,
	})
}

func (a *App) IdentityDetail(w http.ResponseWriter, r *http.Request) {
	a.renderIdentity(w, r, http.StatusOK, nil)
}

func (a *App) renderIdentity(w http.ResponseWriter, r *http.Request, status int, errs map[string]string) {
	id := r.PathValue("id")
	identity, err := a.Store.GetIdentity(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	usage, err := a.Store.IdentityUsage(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	// NeighbourRefs has no `identity` case, so this degrades to the subject's
	// own history (internal/store/audit.go) -- which is exactly what the spec
	// asks for: "the entity's change_log, where a rotation reads as a
	// last_rotated change with its actor and actor_kind". The used-by panel
	// answers the neighbourhood question from the other direction, and widening
	// NeighbourRefs is deliberately not part of this work.
	timeline, _, err := a.Store.TimelineForEntityAndNeighbours(r.Context(), "identity", id, timelineLimit)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	teams, _ := a.responsibilityOptions(r)
	now := a.Store.Now()

	secretRef := ""
	if a.base(r, "", "").IsAdmin && identity.SecretRef != nil {
		secretRef = *identity.SecretRef
	}
	// ^ resolve IsAdmin once via the Base you are about to build rather than
	//   calling a.base twice; the shape above is illustrative. Use:
	//     base := a.base(r, "Identity: "+identity.Name, "identities")
	//     if base.IsAdmin && identity.SecretRef != nil { secretRef = *identity.SecretRef }
	//   a.base consumes the flash, and consuming it twice loses the message
	//   (see takeFlash's doc comment -- it is idempotent per request via
	//   middleware state, but a unit test with no middleware falls back to the
	//   session and WOULD pop twice).

	a.Render.Respond(w, r, status, "identity_detail", "identity_panel", identityPage{
		Base: base, Errors: orEmpty(errs), Identity: identity,
		SecretRef: secretRef,
		Rotation:  string(identity.RotationStatus(now)),
		DueOn:     derefOr(identity.RotationDueOn()),
		Today:     domain.FormatDate(now),
		Usage:     usage, Timeline: timeline,
		Teams: teams, Kinds: domain.IdentityKinds,
	})
}
```

Add to `internal/domain/endpoint.go`, beside `IdentityKinds`:

```go
// RotationStates is the Go side of the rotation-state filter's option list. Not
// a database CHECK -- the state is DERIVED, never stored.
var RotationStates = []string{
	string(RotationUnmanaged), string(RotationNeverRecorded),
	string(RotationWithinWindow), string(RotationOverdue), string(RotationUnreadable),
}
```

- [ ] **Step 4: The templates**

`web/templates/partials/identities.html` defines `identity_list_panel`, `identity_panel`, and (Task 5) `identity_form`. The list panel's rotation cell, rendering the five states directly:

```html
<td>
  {{if eq .Rotation "unmanaged"}}
    <span class="pill pill-muted" title="Nobody asked for this credential to be rotated.">no policy</span>
  {{else if eq .Rotation "never_recorded"}}
    <span class="pill pill-degraded"
          title="The estate has a rule for this credential and no evidence it has ever been followed.">never recorded</span>
  {{else if eq .Rotation "within_window"}}
    <span class="pill pill-ok">due in {{.DaysUntilDue}} d</span>
    <div style="color:var(--muted);font-size:11.5px">{{.DueOn}}</div>
  {{else if eq .Rotation "overdue"}}
    <span class="pill pill-bad">overdue by {{neg .DaysUntilDue}} d</span>
    <div style="color:var(--muted);font-size:11.5px">was due {{.DueOn}}</div>
  {{else}}
    {{/* A state that cannot be READ must never render as a state that is FINE.
         This pill is deliberately alarming rather than muted. */}}
    <span class="pill pill-bad" title="The stored date will not parse.">date unreadable</span>
  {{end}}
</td>
```

`neg` is a one-line template func beside `add` in the renderer's FuncMap if one does not already exist; otherwise compute the absolute value into a second field on `identityListRow` and drop the func. **Prefer the second** — no business logic in templates.

The secret-ref column, which is the whole list-page rule in two lines:

```html
<td>
  {{/* WHETHER, never WHAT. The path renders on the detail page, to an
       Administrator, one credential at a time -- see the handler's header. */}}
  {{if .HasSecretRef}}<span class="pill pill-muted">recorded</span>
  {{else}}<span class="pill pill-degraded" title="Nobody has recorded where the material lives">none</span>{{end}}
</td>
```

`identity_panel` holds the rotation panel (all three facts, never collapsed, `never recorded` rather than a blank), the used-by panel, `secret_ref` for an Administrator via `{{.SecretRef}}` — **never `{{.Identity.SecretRef}}`** — and `{{template "timeline_panel" .Timeline}}`.

Both page files (`identity_list.html`, `identity_detail.html`) follow `certificate_list.html` / `certificate_detail.html` exactly: licence comment block, `{{define "content"}}`, page head, toolbar form with `hx-get`/`hx-target`/`hx-push-url`, then `{{template "identity_list_panel" .}}`.

**A partial must be renderable standalone.** `identity_list_panel` is the `hx-target`, so it takes the whole page value and reaches nothing its parent computed.

- [ ] **Step 5: Routes and nav**

`internal/web/routes.go`, beside the `/certificates` reads (line 165-166):

```go
	// Credential references (WP-J8). THE GETs ARE READABLE BY ANY AUTHENTICATED
	// USER, deliberately: name, realm, kind, team and rotation status are what
	// somebody needs mid-incident and none of it is sensitive. secret_ref is
	// gated inside the detail handler's view model, not by the route -- see
	// internal/web/handlers/identities.go's header. The four POSTs are
	// writeAdminOnly and are registered below.
	read("GET /identities", app.IdentityList)
	read("GET /identities/{id}", app.IdentityDetail)
```

`internal/web/handlers/nav.go`, in the `Services` group beside Certificates:

```go
	{Label: "Services", Links: []NavLink{
		{Label: "Services", Href: "/services", Nav: "services"},
		{Label: "Certificates", Href: "/certificates", Nav: "certificates"},
		// What a service authenticates AS, beside what it is served OVER. Not
		// AdminOnly: the GETs are readable by anybody, and the one sensitive
		// field is gated in the handler, not by hiding the link (hiding is not
		// enforcement -- see AdminOnly's own comment).
		{Label: "Identities", Href: "/identities", Nav: "identities"},
	}},
```

- [ ] **Step 6: Run the web suite**

```bash
INV_TEST_POSTGRES_DSN="..." go test ./internal/web/... -count=1
```
`TestEveryDetailPageRenders` and `golden_test.go` are the two most likely to move.

- [ ] **Step 7: Prove the two disclosure guards can fail**

| Mutation | Test that must go red |
|---|---|
| Add `SecretRef string` to `identityListRow`, populate it, and render it in the list template | `TestTheIdentityListNeverRendersASecretPath` — for **both** users |
| Drop `base.IsAdmin &&` from the detail handler's `secretRef` assignment | `TestSecretRefOnTheDetailPageIsAdministratorOnly` (the viewer half) |
| In the template, render the `unreadable` branch with `pill-ok` and the text `within window` | `TestTheIdentityListRendersEveryRotationState` |

**Layer note for the first mutation:** if you instead delete the whole secret-ref column from the template, the test still passes — it asserts absence. That is why the test opens with a positive control asserting the fixture *has* a path, and asserts the `recorded` marker is present. State that when you run it.

- [ ] **Step 8: Commit**

```bash
git add -A && make lint
git commit   # why: a rotation policy nobody can see is the same as no policy;
             # and the one screen that could list every credential path must not
```

---

### Task 5: The write surface — four `writeAdminOnly` POSTs

**Files:**
- Modify: `internal/web/handlers/identities.go`, `internal/web/routes.go`, `web/templates/partials/identities.html`, `internal/web/routescan/testdata/write_routes.txt`, `internal/web/rbac_boundary_test.go`, `internal/web/identities_test.go`

**Interfaces:**
- Produces:
  - `POST /identities` → `app.IdentityCreate`
  - `POST /identities/{id}` → `app.IdentityUpdate`
  - `POST /identities/{id}/retire` → `app.IdentityRetire`
  - `POST /identities/{id}/rotation` → `app.IdentityRecordRotation`

- [ ] **Step 1: Write the failing tests**

```go
// TestRecordingARotationThroughTheRoute is the end-to-end of the whole package.
func TestRecordingARotationThroughTheRoute(t *testing.T) {
	// POST /identities/{id}/rotation with last_rotated = today -> 303
	// the detail page now shows the date and a "within window" state
	// the TIMELINE shows the rotation as a last_rotated change, with an actor
	if !strings.Contains(page, "last_rotated") {
		t.Error("the timeline does not show the rotation as a last_rotated change. " +
			"change_log IS the rotation history -- the table keeps only the latest " +
			"date -- so a rotation that does not appear here is a rotation nobody " +
			"can audit afterwards.")
	}
}

// TestAFutureRotationIs422WithTheFormReRendered. CLAUDE.md's rule and the
// spec's: never a 200 with the error buried, never a flash-and-redirect.
func TestAFutureRotationIs422WithTheFormReRendered(t *testing.T) {
	tomorrow := ...
	resp := h.post("/identities/"+id+"/rotation", url.Values{"last_rotated": {tomorrow}}, false)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a post-dated rotation returned %d, want 422", resp.StatusCode)
	}
	page := body(t, resp)
	// the typed value survives, and the message is against the field
	if !strings.Contains(page, tomorrow) {
		t.Error("the refused form did not hand back what the operator typed")
	}
	if !strings.Contains(page, "cannot be recorded in the future") {
		t.Errorf("the refusal does not say why the date was rejected, so it reads as a " +
			"generic failure on a date that is well-formed")
	}
	// and nothing was written
	if n := h.count(`SELECT COUNT(*) FROM identity WHERE last_rotated IS NOT NULL`); n != 0 {
		t.Errorf("%d identities carry a last_rotated after a refused rotation. A 422 that "+
			"still wrote would be worse than no refusal at all, because the page says "+
			"it failed.", n)
	}
}

// TestARotationAgainstARetiredIdentityIs422AndNamesIt.
func TestARotationAgainstARetiredIdentityIs422AndNamesIt(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)
	ninetyDays := 90

	identity, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-withdrawn-rotation",
		Realm: strPtr("vault"), RotationDays: &ninetyDays,
	})
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, identity); err != nil {
		t.Fatalf("creating identity: %v", err)
	}
	if err := h.store.RetireIdentity(ctx, admin, identity.ID); err != nil {
		t.Fatalf("withdrawing: %v", err)
	}

	path := "/identities/" + identity.ID
	token := h.csrfToken(path)
	today := domain.FormatDate(h.store.Now())
	resp := h.post(path+"/rotation", url.Values{
		"csrf_token":   {token},
		"last_rotated": {today},
	}, false)
	page := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("recording a rotation against a withdrawn credential returned %d, want "+
			"422. A 303 would tell the operator it worked; a 500 would tell them the "+
			"software broke. Neither is true.", resp.StatusCode)
	}
	if !strings.Contains(page, identity.Name) {
		t.Errorf("the refusal does not name %q. An operator with several credentials "+
			"open needs to know which one was withdrawn.", identity.Name)
	}
	if !strings.Contains(page, "withdrawn") {
		t.Errorf("the refusal does not say the credential was withdrawn, so it reads as "+
			"a malformed-date error for a date that is fine")
	}
	// The form comes back, with what was typed still in it.
	if !strings.Contains(page, today) {
		t.Errorf("the refused form did not hand back the date the operator typed")
	}
	// And nothing was written.
	if n := h.count(`SELECT COUNT(*) FROM identity WHERE id = ? AND last_rotated IS NOT NULL`,
		identity.ID); n != 0 {
		t.Errorf("last_rotated was written despite the refusal")
	}
}

// TestAStaleIdentityCorrectionIs409. refusalStatus separates the two reasons a
// save comes back: 422 says "what you typed is wrong", 409 says "somebody else
// got there first".
func TestAStaleIdentityCorrectionIs409(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)

	identity, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-concurrent",
		Realm: strPtr("vault"),
	})
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, identity); err != nil {
		t.Fatalf("creating identity: %v", err)
	}

	path := "/identities/" + identity.ID
	// BOTH SUBMISSIONS CARRY row_version = 1, which is what two operators who
	// opened the form at the same time would send. The token comes from the
	// page each of them is looking at, not from the row.
	form := func(name string) url.Values {
		return url.Values{
			"csrf_token":  {h.csrfToken(path)},
			"kind":        {domain.IdentityServiceAccount},
			"name":        {name},
			"realm":       {"vault"},
			"row_version": {"1"},
		}
	}

	first := h.post(path, form("svc-concurrent-first"), false)
	first.Body.Close()
	if first.StatusCode != http.StatusSeeOther {
		t.Fatalf("the first correction returned %d, want 303 -- if this did not succeed "+
			"the second one is not stale and this test proves nothing", first.StatusCode)
	}

	second := h.post(path, form("svc-concurrent-second"), false)
	page := body(t, second)
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("the second correction returned %d, want 409. refusalStatus separates "+
			"the two reasons a save comes back: 422 says what you typed is wrong, 409 "+
			"says it was fine and somebody else got there first. Answering this with "+
			"422 tells the operator to fix input that has nothing wrong with it.",
			second.StatusCode)
	}
	if !strings.Contains(page, "svc-concurrent-second") {
		t.Errorf("the 409 did not hand back what the second operator typed. Their text " +
			"is the thing they are about to re-apply, and discarding it makes the " +
			"message 'go and read the other edit first' into 'retype everything'.")
	}
	// The first operator's write stands: the loser overwrites nobody.
	stored := h.lookup(`SELECT name FROM identity WHERE id = ?`, identity.ID)
	if stored != "svc-concurrent-first" {
		t.Errorf("stored name = %q, want the FIRST operator's. A stale write that landed "+
			"is the silent revert this token exists to prevent, and change_log would "+
			"record it as a deliberate act by whoever was slower.", stored)
	}
}

// TestTheIdentityWriteRoutesAreAdministratorOnly is the route-gate assertion in
// its own right, beside the generated census in rbac_boundary_test.go. identity
// is ScopeEstateConfig, so tx.log would refuse a project owner's write anyway --
// this DIVERGES DELIBERATELY and gates at the door, because a correction form
// has to RENDER secret_ref to be a correction form, and a form that silently
// omits a field blanks the column on save. Refusing before the form is filled in
// is also the honest order.
func TestTheIdentityWriteRoutesAreAdministratorOnly(t *testing.T) {
	h.login("viewer", "viewer-password")
	for _, path := range []string{
		"/identities", "/identities/" + id, "/identities/" + id + "/retire",
		"/identities/" + id + "/rotation",
	} {
		token := h.csrfToken("/identities")
		resp := h.post(path, url.Values{"csrf_token": {token}}, false)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("POST %s as a read-only user returned %d, want 403. All four writes "+
				"are writeAdminOnly, and the reason is secret_ref: a correction form has "+
				"to RENDER the stored path to be a correction form.", path, resp.StatusCode)
		}
	}
	// and the GETs are NOT gated
	for _, path := range []string{"/identities", "/identities/" + id} {
		resp := h.get(path, false)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s as a read-only user returned %d, want 200. The GETs stay "+
				"readable by any authenticated user: name, realm, kind, team and "+
				"rotation status are what somebody needs mid-incident, and none of it "+
				"is sensitive.", path, resp.StatusCode)
		}
	}
}

// TestTheIdentityEditFormIsNotRenderedToANonAdministrator.
func TestTheIdentityEditFormIsNotRenderedToANonAdministrator(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)
	ninetyDays := 90

	identity, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-form-visibility",
		Realm: strPtr("vault"), SecretRef: strPtr("kv/prod/form-visibility"),
		RotationDays: &ninetyDays,
	})
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, identity); err != nil {
		t.Fatalf("creating identity: %v", err)
	}
	path := "/identities/" + identity.ID

	// The three controls, each identified by the route it posts to rather than
	// by button text, so a reworded button does not silently empty this test.
	controls := map[string]string{
		"the correction form": `action="` + path + `"`,
		"the rotation form":   `action="` + path + `/rotation"`,
		"the withdraw button": `action="` + path + `/retire"`,
	}

	// A POSITIVE CONTROL FIRST. If the admin page does not carry these, the
	// viewer assertions below pass because the markup does not exist at all,
	// which is the vacuous-pass shape this repo keeps finding.
	h.login("admin", "admin-password")
	adminPage := body(t, h.get(path, false))
	for name, marker := range controls {
		if !strings.Contains(adminPage, marker) {
			t.Fatalf("%s is absent from the page for an Administrator (looking for %q). "+
				"The checks below would then prove nothing.", name, marker)
		}
	}

	h.login("viewer", "viewer-password")
	resp := h.get(path, false)
	viewerPage := body(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s as viewer returned %d, want 200 -- the read surface is open",
			path, resp.StatusCode)
	}
	for name, marker := range controls {
		if strings.Contains(viewerPage, marker) {
			t.Errorf("%s is rendered to a read-only user. Every one of the four POSTs is "+
				"writeAdminOnly, so a click answers 403 -- offering a form whose only "+
				"outcome is a refusal is the same defect /imports was fixed for. Hiding "+
				"is not the enforcement; the enforcement is "+
				"middleware.RequireAdministrator, and this is the half that stops the "+
				"page lying about what the reader can do.", name)
		}
	}
	// The correction form is the one that would RENDER the secret path, which
	// is why the write gate on these routes is stricter than the permit layer
	// alone would require. Asserted here as well as in the disclosure test,
	// because this is the mechanism and that one is the outcome.
	if strings.Contains(viewerPage, "kv/prod/form-visibility") {
		t.Error("the secret path reached a read-only user through the edit form")
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

- [ ] **Step 3: Implement the handlers**

```go
func (a *App) IdentityCreate(w http.ResponseWriter, r *http.Request) {
	spec := identitySpecFromForm(r)
	i, err := domain.NewIdentity(store.NewID(), spec)
	if err == nil {
		err = a.Store.CreateIdentity(r.Context(), a.permit(r), i)
	}
	if err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderIdentityList(w, r, refusalStatus(err), errs, spec)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/identities/"+i.ID)
}

func (a *App) IdentityUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := a.Store.GetIdentity(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	spec := identitySpecFromForm(r)

	updated := existing.Identity
	updated.Kind = spec.Kind
	updated.Name = spec.Name
	updated.Realm = spec.Realm
	updated.SecretRef = spec.SecretRef
	updated.RotationDays = spec.RotationDays
	// submittedString, not the spec: a picker that failed to render must not
	// read as an operator clearing the field. Same rule as assets and services.
	updated.TeamID = submittedString(r, "team_id", existing.TeamID)
	// NOT lifecycle and NOT last_rotated. UpdateIdentity pins both from the
	// stored row anyway; assigning neither here is the second statement of the
	// same rule, at the layer where a future edit is most likely to add one.
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdateIdentity(r.Context(), a.permit(r), &updated); err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderIdentity(w, r, refusalStatus(err), errs)
			return
		}
		if isStale(err) {
			a.renderIdentity(w, r, http.StatusConflict, staleMessage("name"))
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/identities/"+id)
}

func (a *App) IdentityRetire(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.Store.RetireIdentity(r.Context(), a.permit(r), id); err != nil {
		if isStale(err) {
			// The withdraw form carries the token like every other edit form, so
			// a stale withdrawal is 409 rather than a silent second retire of a
			// row somebody else has already changed.
			a.renderIdentity(w, r, http.StatusConflict, staleMessage("name"))
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	// Back to the list rather than the detail page: the credential the operator
	// was looking at is gone from the default view, and leaving them on a page
	// that now says "retired" reads as a failed action.
	render.Redirect(w, r, "/identities")
}

func (a *App) IdentityRecordRotation(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := a.Store.RecordIdentityRotation(r.Context(), a.permit(r), id, formValue(r, "last_rotated"))
	if err != nil {
		if errs, ok := validationErrors(err); ok {
			// 422 with the form re-rendered, never a flash-and-redirect --
			// TestARefusalIsRenderedNotFlashed AST-scans this package for
			// exactly that and will fail you.
			a.renderIdentity(w, r, refusalStatus(err), errs)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/identities/"+id)
}

// identitySpecFromForm reads a credential reference out of a submitted form.
//
// NO last_rotated FIELD, and that is the rule rather than an omission: it has
// exactly one writer and this is not it. See internal/store/identities.go's
// header and internal/store/last_rotated_source_test.go.
func identitySpecFromForm(r *http.Request) domain.IdentitySpec {
	return domain.IdentitySpec{
		Kind:         formValue(r, "kind"),
		Name:         formValue(r, "name"),
		Realm:        optionalString(r, "realm"),
		SecretRef:    optionalString(r, "secret_ref"),
		RotationDays: optionalInt(r, "rotation_days"),
		TeamID:       optionalString(r, "team_id"),
	}
}
```

`optionalInt` — check `internal/web/handlers/app.go` for an existing one; there is `submittedVersion` and `optionalString`, so add `optionalInt` beside the latter if absent, returning nil for an empty field and nil-with-no-error for an unparseable one is **wrong**: an unparseable number must reach `Validate` as a refusal, not vanish. If no helper exists, parse in the handler and add `rotation_days` to the errors map directly before calling the store.

The refused list-page render must hand back what was typed. `renderIdentityList` already takes `spec`; the create form reads its values from `.Spec`.

- [ ] **Step 4: The forms**

In `web/templates/partials/identities.html`. Three things must be right:

1. **Every edit form carries its token.** `<input type="hidden" name="row_version" value="{{.Identity.RowVersion}}">` on the correction form **and** the retire form. `TestEveryEditFormCarriesItsVersion` derives its population from handlers that reach `submittedVersion`, and 00066's header warns that a correction path built without a token is not flagged by that census — **it is simply absent from it**.
2. **The rotation form carries NO token**, matching `VerifyDependency`. If `TestEveryEditFormCarriesItsVersion` picks `POST /identities/{id}/rotation` up (it will not — `IdentityRecordRotation` never calls `submittedVersion`), that is the signal you wired a token you did not mean to.
3. **`hx-confirm` needs `hx-post` beside it** on the retire button, or the confirm is decorative — a bug this repo has shipped before.

```html
<form class="panel-body" method="post" action="/identities/{{.Identity.ID}}/rotation"
      hx-post="/identities/{{.Identity.ID}}/rotation">
  <input type="hidden" name="csrf_token" value="{{.CSRF}}">
  <div class="field">
    <label for="i-rot">Rotated on</label>
    {{/* Defaults to today, computed server-side. A browser-supplied default
         would let a client's wrong clock post-date a rotation past a check
         made against the server's. max is a courtesy, not the enforcement:
         RecordIdentityRotation refuses a future date and returns 422. */}}
    <input id="i-rot" type="date" name="last_rotated"
           value="{{.Today}}" max="{{.Today}}" required>
    {{with index .Errors "last_rotated"}}<div class="field-error">{{.}}</div>{{end}}
    <div class="field-hint">
      Backdating is fine — record the day it actually happened. A future date is refused.
    </div>
  </div>
  <button class="btn btn-primary" type="submit">Record rotation</button>
</form>
```

The whole write half of the partial sits inside `{{if .IsAdmin}}`.

- [ ] **Step 5: Register the routes**

`internal/web/routes.go`, beside the `/users` block:

```go
	// Credential references (WP-J8). writeAdminOnly, NOT write, and identity is
	// ScopeEstateConfig so tx.log would refuse a project owner's write
	// regardless of the route gate -- `team` relies on exactly that and uses
	// plain write(). THIS DIVERGES DELIBERATELY, and the reason is secret_ref: a
	// correction form has to RENDER the stored path to be a correction form, and
	// a form that silently omits a field blanks the column on save. Gating at
	// the door keeps the only surface that renders a secret path behind the same
	// gate that already protects it on the dependency page, and makes the
	// refusal honest BEFORE a form is filled in rather than after it is
	// submitted. /users and /users/{id}/projects take writeAdminOnly for the
	// same class of reason.
	//
	// The two GETs are registered with read() further up: name, realm, kind,
	// team and rotation status are what somebody needs mid-incident.
	writeAdminOnly("POST /identities", app.IdentityCreate)
	writeAdminOnly("POST /identities/{id}", app.IdentityUpdate)
	writeAdminOnly("POST /identities/{id}/retire", app.IdentityRetire)
	writeAdminOnly("POST /identities/{id}/rotation", app.IdentityRecordRotation)
```

- [ ] **Step 6: Regenerate the route inventory and move the two pins**

```bash
go test ./internal/web/routescan/ -run TestTheCommittedRouteInventoryMatchesTheRouter -write-inventory
```
Do not hand-edit `internal/web/routescan/testdata/write_routes.txt`.

Then `internal/web/rbac_boundary_test.go`:

- Line 1216, `pinnedNoSessionRouteCount`: **219 → 223**, appending to the running commentary:
  ```
  // 219 -> 223: identity-surface plan, Task 5 -- POST /identities,
  // /identities/{id}, /identities/{id}/retire and /identities/{id}/rotation.
  // The store methods had no route at all until this task; the fourth is the
  // one the work package exists for, and it is the only route in the router
  // whose whole job is to stamp a date.
  ```
- Line ~930, `administratorGate`: **14 → 18**, with:
  ```
  // 14 -> 18: the four /identities POSTs. writeAdminOnly rather than write,
  // and the reason is secret_ref on the correction form -- see routes.go.
  // identity is ScopeEstateConfig, so a project owner would be refused by
  // permit.Covers anyway; gating at the door means they are refused BEFORE
  // filling in a form, and means the one surface that renders a credential
  // path never renders it to them at all.
  ```
  and update the `want 14 (six import routes and eight /users routes)` message text to name the four new ones.

`permitGate` does **not** move: a project owner is refused at middleware on these four, so they land in `administratorGate` and never reach `tx.log`.

- [ ] **Step 7: Run the web suite and the AST scans**

```bash
INV_TEST_POSTGRES_DSN="..." go test ./internal/web/... -count=1
```
The three that most often catch a mistake here: `TestARefusalIsRenderedNotFlashed`, `TestEveryEditFormCarriesItsVersion`, `TestNoWriteRouteIsReachableWithNoSessionAtAll`.

- [ ] **Step 8: Prove the guards can fail**

| Mutation | Test that must go red |
|---|---|
| Change the four registrations from `writeAdminOnly` to `write` | `TestEveryWriteRouteRefusesAProjectOwner`'s `administratorGate != 18` **and** its per-route `route.Gate` assertion, **and** `TestTheIdentityWriteRoutesAreAdministratorOnly`. **Different layers on purpose**: the first two are the generated census, the third is this feature's own assertion, and a census pin can be "fixed" by editing a number while the third cannot. |
| Replace `refusalStatus(err)` with `http.StatusOK` in `IdentityRecordRotation` | `TestAFutureRotationIs422WithTheFormReRendered` |
| Replace the 422 branch with `a.flash(...); render.Redirect(...)` | `TestARefusalIsRenderedNotFlashed` (AST) — and note it fails **without any request being made**, which is why it catches handlers no behavioural test reaches |
| Delete the `row_version` hidden input from the identity correction form | `TestEveryEditFormCarriesItsVersion` |
| Delete `hx-post` from the retire button, keeping `hx-confirm` | whichever `hx-confirm` census exists in `internal/web` — if there is none, say so and add the assertion to `TestTheIdentityWriteRoutesAreAdministratorOnly`'s file as a template scan |

- [ ] **Step 9: Commit**

```bash
git add -A && make lint
git commit   # why: "record a rotation" is the feature, not a side effect of an
             # edit form -- and the only page that renders a credential path is
             # gated at the door, not after the form is submitted
```

---

### Task 6: The demo estate shows all five states

**A requirement, not a nicety.** Two features have now shipped rendering as empty pages, and this one starts from three identities that are all in the *same* state — a fresh estate would demonstrate one of five rotation states and one of three findings.

**Files:**
- Modify: `internal/seed/seed.go` (`b.identities()`, `Load`'s phase order), `internal/seed/seed_services.go` (one dependency spec)
- Create: `internal/seed/seed_identities_test.go`

- [ ] **Step 1: Write the failing test**

`internal/seed/seed_identities_test.go`:

```go
// THE HARNESS HERE IS internal/seed's OWN, not the store suite's. package
// store's Engines lives in its test files and is not importable, and
// seed_test.go's header records the scoping decision: SQLite only, because the
// seeder writes no SQL of its own -- every statement it issues comes from a
// store method the store suite already runs against both engines. These tests
// therefore use newFixture / eachEngine (internal/seed/seed_test.go:46,69).

// TestTheSeededEstateShowsEveryRotationState is the "two features shipped as
// empty pages" rule, applied before the page exists rather than after somebody
// notices. Four of the five states and a retired credential still named by a
// live dependency, so every pill and all three findings have a row.
func TestTheSeededEstateShowsEveryRotationState(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		rows, err := f.store.ListIdentities(f.ctx, store.IdentityFilter{IncludeRetired: true})
		if err != nil {
			t.Fatalf("listing identities: %v", err)
		}
		if len(rows) == 0 {
			t.Fatal("the fixture seeds no identities at all; this test proves nothing")
		}

		now := f.store.Now()
		seen := map[domain.RotationState]string{}
		for _, r := range rows {
			if r.Lifecycle == domain.LifecycleRetired {
				continue
			}
			seen[r.RotationStatus(now)] = r.Name
		}
		for _, want := range []domain.RotationState{
			domain.RotationUnmanaged,
			domain.RotationNeverRecorded,
			domain.RotationWithinWindow,
			domain.RotationOverdue,
		} {
			if seen[want] == "" {
				t.Errorf("no seeded identity is in state %q, so the demo shows that pill "+
					"-- and any finding built on it -- nowhere. A fresh estate "+
					"demonstrating one of five states is how two features have already "+
					"shipped rendering as empty pages.", want)
			}
		}
		// RotationUnreadable is DELIBERATELY NOT SEEDED: reaching it needs a
		// value the database CHECK accepts and domain.ParseDate rejects, which
		// is a corrupt row, and seeding one would teach every reader of the demo
		// that the estate produces them. It is covered at the unit layer
		// (TestRotationStatus) and the web layer
		// (TestTheIdentityListRendersEveryRotationState).
		if _, ok := seen[domain.RotationUnreadable]; ok {
			t.Errorf("the fixture seeds a credential whose stored date will not parse. " +
				"That is a corrupt row, and a demo estate must not contain one.")
		}

		// A retired credential still named by a live dependency: the third
		// finding, and the "what is stored keeps displaying" rule, both visible
		// on a fresh estate.
		reader := f.store.DB().Reader
		var n int
		if err := reader.Get(&n, reader.Rebind(`
			SELECT COUNT(*) FROM dependency d
			JOIN identity i ON i.id = d.identity_id
			WHERE d.lifecycle <> 'retired' AND i.lifecycle = 'retired'`)); err != nil {
			t.Fatalf("counting live edges on withdrawn credentials: %v", err)
		}
		if n == 0 {
			t.Error("no live dependency names a retired identity, so the finding that " +
				"catches a withdrawn credential still in use has nothing to show, and " +
				"neither does the used-by panel's most interesting case")
		}
	})
}

// TestASeededRotationHasARealChangeLogEntry. At least one rotation goes through
// RecordIdentityRotation rather than the create call, so the demo detail page
// shows a real rotation in its history -- and so the seeder is not the first
// caller tempted to set last_rotated on the struct.
func TestASeededRotationHasARealChangeLogEntry(t *testing.T) {
	eachEngine(t, func(t *testing.T, f *fixture) {
		rows, err := f.store.ListIdentities(f.ctx, store.IdentityFilter{IncludeRetired: true})
		if err != nil {
			t.Fatalf("listing identities: %v", err)
		}

		rotated := 0
		for _, r := range rows {
			if r.LastRotated == nil {
				continue
			}
			rotated++
			changes, err := f.store.ListChangesForEntity(f.ctx, "identity", r.ID, 50)
			if err != nil {
				t.Fatalf("reading the audit trail for %s: %v", r.Name, err)
			}
			found := false
			for _, c := range changes {
				if strings.Contains(c.Diff, "last_rotated") {
					found = true
					if c.ActorKind == "" {
						t.Errorf("%s's rotation entry has no actor_kind. Every view "+
							"rendering actor renders actor_kind beside it.", r.Name)
					}
					break
				}
			}
			if !found {
				t.Errorf("%s carries last_rotated = %q and NO change_log entry naming "+
					"last_rotated. The date was written on the struct instead of through "+
					"RecordIdentityRotation, so the demo's detail page shows a rotation "+
					"with no history behind it -- which is the exact failure the "+
					"one-writer rule exists to prevent.", r.Name, *r.LastRotated)
			}
		}
		if rotated == 0 {
			t.Fatal("no seeded identity has a recorded rotation at all, so the demo " +
				"detail page has no rotation history to show and this test is checking " +
				"nothing")
		}
	})
}

// TestTheSeededRotationDatesAreRelativeToTheClock is the mutation-proof for the
// "never literals" rule, and it is the ONLY shape that works. A literal date
// passes on the day it is written and fails months later -- this repo has
// already shipped a test with exactly that defect, and production was unaffected
// while only the calendar exposed it. Loading the estate into a store whose
// clock is years away makes a literal fail IMMEDIATELY.
func TestTheSeededRotationDatesAreRelativeToTheClock(t *testing.T) {
	future := time.Date(2029, 3, 4, 10, 0, 0, 0, time.UTC)

	dsn := "file:" + filepath.Join(t.TempDir(), "seed-future.db")
	db, err := store.Open(store.DriverSQLite, dsn)
	if err != nil {
		t.Fatalf("opening database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	// WithClock BEFORE Load, so b.now is the future date and every seeded date
	// is derived from it.
	s := store.New(db).WithClock(func() time.Time { return future })
	if _, err := seed.Load(ctx, s); err != nil {
		t.Fatalf("seeding: %v", err)
	}

	rows, err := s.ListIdentities(ctx, store.IdentityFilter{IncludeRetired: true})
	if err != nil {
		t.Fatalf("listing identities: %v", err)
	}
	seen := map[domain.RotationState]bool{}
	for _, r := range rows {
		if r.Lifecycle == domain.LifecycleRetired {
			continue
		}
		seen[r.RotationStatus(future)] = true
	}

	if !seen[domain.RotationWithinWindow] {
		t.Error("no identity is within its window when the estate is seeded in 2029. " +
			"A literal date in b.identityHistory() drifts: the within-window row " +
			"silently becomes overdue some weeks after the fixture was written, and " +
			"the demo stops demonstrating the state it was built for. Dates are " +
			"relative to b.now, the rule lifetimes() already follows.")
	}
	if !seen[domain.RotationOverdue] {
		t.Error("no identity is overdue when the estate is seeded in 2029, so a " +
			"literal date has replaced the b.now-relative one and the Fault finding " +
			"has nothing to show on any demo reset after that date")
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Expected: `no seeded identity is in state "unmanaged"` and the three siblings — all three seeded identities are `never_recorded` today, exactly as the spec says.

- [ ] **Step 3: Extend `b.identities()`**

```go
func (b *builder) identities() {
	identities := []struct {
		kind, name, realm, secretRef string
		rotationDays                 int // 0 means no policy at all
	}{
		{domain.IdentityServiceAccount, "svc-orders", "vault", "kv/prod/orders/db", 90},
		{domain.IdentityServiceAccount, "svc-sso", "vault", "kv/prod/sso/db", 90},
		{domain.IdentityMachineAccount, "svc-backup$", "AD", "kv/prod/backup/windows", 90},
		// UNMANAGED, and legitimately so: a metrics scrape token nobody has
		// asked to be rotated on a schedule. The spec is explicit that
		// rotation_days IS NULL is NOT a finding -- flagging every cert_subject
		// and human row would swamp the page and teach people to ignore it --
		// so this row exists to show what "no policy" looks like beside the
		// rows that have one.
		{domain.IdentityAPIToken, "metrics-scrape", "vault", "kv/prod/observability/scrape", 0},
		// THE ONE THAT GETS WITHDRAWN, in b.identityHistory() below, while a
		// live dependency still names it. Migration 00003's scenario exactly:
		// "the natural response to a compromised credential is to retire it and
		// create its replacement under the same name."
		{domain.IdentityServiceAccount, "svc-legacy-etl", "vault", "kv/prod/etl/legacy", 90},
	}
	for _, i := range identities {
		...
		spec := domain.IdentitySpec{
			Kind: i.kind, Name: i.name, Realm: str(i.realm),
			SecretRef: str(i.secretRef), TeamID: b.team("platform"),
		}
		if i.rotationDays > 0 {
			spec.RotationDays = num(i.rotationDays)
		}
		identity, err := domain.NewIdentity(store.NewID(), spec)
		...
	}
}
```

- [ ] **Step 4: Add the late phase that rotates and withdraws**

```go
// identityHistory records what has happened to the seeded credentials since
// they were declared: two rotations and one withdrawal.
//
// IT IS A SEPARATE, LATE PHASE and it has to be. Two reasons.
//
// THE ROTATIONS GO THROUGH RecordIdentityRotation, never through the create
// call, because last_rotated has exactly one writer
// (internal/store/last_rotated_source_test.go) and because the demo detail page
// must show a REAL rotation entry in its change history -- a date that appeared
// in a create snapshot demonstrates nothing about the feature. The seeder is the
// first caller that would otherwise be tempted to set the field on the struct.
//
// THE WITHDRAWAL RUNS AFTER dependencies(), because the fact being demonstrated
// is a LIVE dependency still naming a RETIRED credential: the finding that
// catches a withdrawn credential still in use, and the "what is stored keeps
// displaying" rule. Retiring it in identities() would leave the dependency
// pointing at a row that was already dead when the edge was declared, which is a
// different and less interesting story.
//
// DATES ARE RELATIVE TO b.now AND NEVER LITERALS, the rule lifetimes() already
// follows. A literal would drift: the within-window row silently becomes overdue
// some weeks after this fixture was written, and the demo stops demonstrating
// the state it was built for. TestTheSeededRotationDatesAreRelativeToTheClock
// seeds into a store whose clock is years away, which is the only shape that
// catches a literal on the day it is written.
func (b *builder) identityHistory() {
	rotations := []struct {
		name    string
		daysAgo int
		why     string
	}{
		// WITHIN WINDOW: rotated a month ago against a 90-day policy.
		{"svc-orders", 30, "rotated last month, comfortably inside its window"},
		// OVERDUE: 200 days against a 90-day policy, so the Fault finding has a
		// row and the pill reads "overdue by 110 d".
		{"svc-sso", 200, "rotated over six months ago against a 90-day rule"},
		// svc-backup$ is deliberately NOT rotated: policy set, nothing ever
		// recorded, which is the Gap finding and the state all three seeded
		// identities were in before WP-J8.
	}
	for _, r := range rotations {
		if !b.ok() {
			return
		}
		id, ok := b.identityIDs[r.name]
		if !ok {
			b.fail(fmt.Errorf("rotating %s: no such seeded identity", r.name))
			return
		}
		date := domain.FormatDate(b.now.AddDate(0, 0, -r.daysAgo))
		if err := b.store.RecordIdentityRotation(b.ctx, Permit, id, date); err != nil {
			b.fail(fmt.Errorf("seeding rotation for %s: %w", r.name, err))
			return
		}
	}

	if id, ok := b.identityIDs["svc-legacy-etl"]; ok {
		if err := b.store.RetireIdentity(b.ctx, Permit, id); err != nil {
			b.fail(fmt.Errorf("withdrawing svc-legacy-etl: %w", err))
			return
		}
	}
}
```

And in `Load`, immediately after `b.dependencies()`:

```go
	b.dependencies()
	// What has happened to the credentials since they were declared. After
	// dependencies() on purpose -- see the function's own comment.
	b.identityHistory()
```

- [ ] **Step 5: Point one live dependency at the credential that gets withdrawn**

In `internal/seed/seed_services.go`'s dependency spec list, add `identity: "svc-legacy-etl"` to one spec that currently names none — pick an edge where a legacy ETL account is plausible, and say so in a comment:

```go
			// Named by a credential the estate has since WITHDRAWN
			// (b.identityHistory). The dependency stays exactly as it is: 
			// RetireIdentity refuses nothing and rewrites nothing, because
			// rewriting it would attribute a dependency change to whoever
			// clicked withdraw. What the demo shows is the inventory
			// contradicting itself -- either the edge is stale or this service
			// is authenticating with a withdrawn credential, and it is not
			// knowable from here. That is the Gap finding.
			identity:   "svc-legacy-etl",
			authMethod: "scram-sha-256",
```

- [ ] **Step 6: Run everything**

```bash
INV_TEST_POSTGRES_DSN="..." go test ./... -count=1 -timeout 30m
```

**Expect fallout and read it rather than patching numbers.** Two seeded identities and one retirement move: `TeamOwnershipCounts` for `platform` (3 → 5 live), anything asserting the seeded `change_log` size, and the dashboard's findings list once Task 7 lands. Each of those is a real count moving for a real reason — update the assertion *and* say why in the same diff.

- [ ] **Step 7: Prove the seed guard can fail**

| Mutation | Test that must go red |
|---|---|
| Replace `domain.FormatDate(b.now.AddDate(0, 0, -30))` with the literal it evaluates to today | `TestTheSeededRotationDatesAreRelativeToTheClock` — **and this is the layer note**: `TestTheSeededEstateShowsEveryRotationState` keeps passing, because it runs against `s.Now()` and the literal is correct *today*. That is precisely the shape of the bug this repo already shipped ("a test passed the day it was written and failed the next morning"), and the clock-shifted test is the only one that catches it. |
| Delete `b.identityHistory()` from `Load` | `TestTheSeededEstateShowsEveryRotationState` (`within_window` and `overdue` both absent) **and** `TestASeededRotationHasARealChangeLogEntry` |
| Set `identity.LastRotated` directly in `b.identities()` instead of calling `RecordIdentityRotation` | `TestASeededRotationHasARealChangeLogEntry` — the date is there, the audit entry is not, which is exactly the failure the one-writer rule exists to prevent |
| Retire `svc-legacy-etl` inside `b.identities()` instead of the late phase | nothing goes red, and that is worth saying out loud: the dependency would still name a retired identity. The ordering is about the *story*, not a property a test can assert. Keep the comment. |

- [ ] **Step 8: Commit**

```bash
git add -A && make lint
git commit   # why: two features have now shipped rendering as empty pages, and
             # a fresh estate would have demonstrated one of five rotation states
```

---

### Task 7: Findings, the roadmap, and the full gate

**Files:**
- Create: `internal/store/rotation_findings.go`, `internal/store/rotation_findings_test.go`
- Modify: `internal/store/findings.go`, `docs/ROADMAP.md`

**Interfaces:**
- Produces: `RotationFindings(ctx context.Context) ([]Finding, error)`, registered in `EstateFindings` beside `TemplateDriftFindings`.

- [ ] **Step 1: Write the failing test**

```go
// TestRotationFindings covers the three findings and, as importantly, the two
// non-findings: an unmanaged credential produces nothing, and a within-window
// one produces nothing.
func TestRotationFindings(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)
			now := f.s.Now()

			// Neither of these is a finding: no rule to break, and a rule that
			// is being met.
			f.identity(t, "metrics-scrape", 0)
			current := f.identity(t, "svc-current", 90)
			if err := f.s.RecordIdentityRotation(f.ctx, testPermit, current.ID,
				domain.FormatDate(now.AddDate(0, 0, -10))); err != nil {
				t.Fatalf("rotating svc-current: %v", err)
			}

			// A rule the estate set for itself and has not met for 110 days.
			overdue := f.identity(t, "svc-overdue", 90)
			if err := f.s.RecordIdentityRotation(f.ctx, testPermit, overdue.ID,
				domain.FormatDate(now.AddDate(0, 0, -200))); err != nil {
				t.Fatalf("rotating svc-overdue: %v", err)
			}

			// A rule with no evidence it has ever been followed.
			f.identity(t, "svc-unrecorded", 90)

			// A withdrawn credential a live edge still names.
			withdrawn := f.identity(t, "svc-withdrawn", 90)
			f.dependency(t, withdrawn.ID)
			if err := f.s.RetireIdentity(f.ctx, testPermit, withdrawn.ID); err != nil {
				t.Fatalf("withdrawing svc-withdrawn: %v", err)
			}

			findings, err := f.s.RotationFindings(f.ctx)
			if err != nil {
				t.Fatalf("RotationFindings: %v", err)
			}

			byLabel := map[string]Finding{}
			for _, x := range findings {
				if _, dup := byLabel[x.Label]; dup {
					t.Errorf("two findings share the label %q. One row per KIND is the "+
						"rule -- forty overdue credentials is one decision.", x.Label)
				}
				byLabel[x.Label] = x
			}
			if len(findings) != 3 {
				t.Fatalf("got %d findings, want 3 (overdue, never recorded, withdrawn "+
					"but still named): %+v", len(findings), findings)
			}

			for _, tc := range []struct {
				what         string
				wantSeverity string
				wantCount    int
				wantExample  string
				why          string
			}{
				{
					what: "past its own rotation rule", wantSeverity: FindingFault,
					wantCount: 1, wantExample: "svc-overdue",
					why: "the estate's own declared rule says 90 days and it has been 200. " +
						"Something is wrong NOW -- the same shape as a contract having " +
						"lapsed, which findings.go names as the archetypal Fault.",
				},
				{
					what: "no rotation ever recorded", wantSeverity: FindingGap,
					wantCount: 1, wantExample: "svc-unrecorded",
					why: "the inventory does not know when this was last rotated, so it " +
						"cannot say whether the rule is met. Calling it a Fault would " +
						"claim knowledge nobody has; Gap is the severity that makes the " +
						"other two trustworthy.",
				},
				{
					what: "withdrawn credential", wantSeverity: FindingGap,
					wantCount: 1, wantExample: "svc-withdrawn",
					why: "the inventory contradicts itself: either the edge is stale or " +
						"the service is authenticating with a withdrawn credential, and " +
						"it is not knowable from here.",
				},
			} {
				t.Run(tc.what, func(t *testing.T) {
					var got Finding
					var found bool
					for label, x := range byLabel {
						if strings.Contains(label, tc.what) ||
							strings.Contains(x.Detail, tc.wantExample) {
							got, found = x, true
							break
						}
					}
					if !found {
						t.Fatalf("no finding for %q. %s", tc.what, tc.why)
					}
					if got.Severity != tc.wantSeverity {
						t.Errorf("severity = %q, want %q. %s",
							got.Severity, tc.wantSeverity, tc.why)
					}
					if got.Count != tc.wantCount {
						t.Errorf("count = %d, want %d", got.Count, tc.wantCount)
					}
					if !strings.Contains(got.Detail, tc.wantExample) {
						t.Errorf("detail = %q and does not name %q. One row per kind is "+
							"exactly why the row has to carry a concrete example, or it "+
							"is only a number.", got.Detail, tc.wantExample)
					}
					if got.Href == "" {
						t.Error("the finding has no Href, so the dashboard row is a dead " +
							"end and the reader has to go and find the credential by hand")
					}
				})
			}

			// The two non-findings, asserted rather than assumed: neither the
			// unmanaged credential nor the current one may appear anywhere.
			for _, x := range findings {
				for _, quiet := range []string{"metrics-scrape", "svc-current"} {
					if strings.Contains(x.Detail, quiet) {
						t.Errorf("finding %q names %q. A credential nobody intended to "+
							"rotate is not a problem, and one that is inside its window "+
							"is the rule being MET.", x.Label, quiet)
					}
				}
			}
		})
	}
}

// TestAnUnmanagedCredentialIsNotAFinding, on its own, because it is the
// judgement most likely to be "improved" by somebody later. "A credential nobody
// intended to rotate is not a problem, and flagging every cert_subject and human
// row would swamp the page" -- the reasoning EstateFindings already applies to
// expected power convergence and template_drift applies to extra components.
func TestAnUnmanagedCredentialIsNotAFinding(t *testing.T) {
	for _, e := range Engines(t) {
		t.Run(e.Name, func(t *testing.T) {
			f := newIdentityFixture(t, e)
			now := f.s.Now()

			// An estate of credentials nobody asked to have rotated, plus one
			// that is rotated and current. Neither is a problem.
			f.identity(t, "metrics-scrape", 0)
			f.identity(t, "cert-subject-web", 0)
			f.identity(t, "human-oncall", 0)
			current := f.identity(t, "svc-current", 90)
			if err := f.s.RecordIdentityRotation(f.ctx, testPermit, current.ID,
				domain.FormatDate(now.AddDate(0, 0, -10))); err != nil {
				t.Fatalf("rotating svc-current: %v", err)
			}

			findings, err := f.s.RotationFindings(f.ctx)
			if err != nil {
				t.Fatalf("RotationFindings: %v", err)
			}
			if len(findings) != 0 {
				t.Fatalf("an estate with three unmanaged credentials and one current one "+
					"produced %d findings: %+v.\nA credential nobody intended to rotate "+
					"is not a problem. Flagging every cert_subject and human row would "+
					"swamp the page and teach people to ignore it -- the reasoning "+
					"EstateFindings already applies to expected power convergence and "+
					"template_drift applies to extra components. It renders as `no "+
					"policy` on the list, which is enough.", len(findings), findings)
			}

			// Now one credential with a rule and no evidence it was followed.
			// EXACTLY ONE finding must appear, and it must be the never-recorded
			// one -- which is also what proves the three unmanaged rows are
			// still contributing nothing rather than having been folded in.
			f.identity(t, "svc-unrecorded", 90)
			findings, err = f.s.RotationFindings(f.ctx)
			if err != nil {
				t.Fatalf("RotationFindings after adding an unrecorded credential: %v", err)
			}
			if len(findings) != 1 {
				t.Fatalf("got %d findings, want exactly 1: %+v", len(findings), findings)
			}
			got := findings[0]
			if got.Count != 1 {
				t.Errorf("finding count = %d, want 1 -- the three unmanaged credentials "+
					"have been folded into this finding", got.Count)
			}
			if got.Severity != FindingGap {
				t.Errorf("severity = %q, want %q. Calling it a Fault would claim knowledge "+
					"nobody has: it might have been rotated last week by somebody who did "+
					"not write it down. Gap is the severity that makes the other two "+
					"trustworthy.", got.Severity, FindingGap)
			}
			if !strings.Contains(got.Detail, "svc-unrecorded") {
				t.Errorf("detail = %q and names no example. One row per KIND of finding is "+
					"the rule, which is exactly why the row has to carry a concrete "+
					"example or it is only a number.", got.Detail)
			}
		})
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

- [ ] **Step 3: Implement**

```go
// What the rotation policy says about this estate, as findings.
//
// IT DERIVES, IT DOES NOT DECIDE. Every count here comes from
// ListIdentities -- the same store method the /identities page uses -- and
// folds in Go through domain.Identity.RotationStatus, so a finding cannot
// appear here and not there, or say something different in the two places.
// That is the failure mode a summary invites, and findings.go's own header
// names it.
//
// FOLDED IN GO RATHER THAN QUERIED, and not only for that reason: the state
// depends on today's date (CLAUDE.md forbids a clock in SQL) and on whether
// the stored value PARSES, which no portable SQL expression can decide. It
// also keeps last_rotated out of every SQL literal outside
// RecordIdentityRotation, which is what
// internal/store/last_rotated_source_test.go asserts.
//
// rotation_days IS NULL IS NOT A FINDING. A credential nobody intended to
// rotate is not a problem, and flagging every cert_subject and human row
// would swamp the page -- "teach people to ignore the page", the reasoning
// EstateFindings already applies to expected power convergence. It renders as
// `no policy` on the list, which is enough.
//
// NO "DUE SOON" BAND. It would need a horizon constant nobody has asked for,
// and the list already sorts by days remaining. Add it when somebody asks,
// with ExpiryHorizonMonths as the precedent for where the number lives.
func (s *SQLStore) RotationFindings(ctx context.Context) ([]Finding, error) {
	rows, err := s.ListIdentities(ctx, IdentityFilter{IncludeRetired: true})
	if err != nil {
		return nil, fmt.Errorf("gathering rotation findings: %w", err)
	}
	now := s.now()

	var overdue, never int
	var firstOverdue, firstNever string
	var overdueHref, neverHref string
	for _, r := range rows {
		if r.Lifecycle == domain.LifecycleRetired {
			continue
		}
		switch r.RotationStatus(now) {
		case domain.RotationOverdue:
			overdue++
			if firstOverdue == "" {
				firstOverdue = fmt.Sprintf("%s was last rotated %s, against a %d-day rule",
					r.Name, derefOr(r.LastRotated, "?"), derefOrInt(r.RotationDays))
				overdueHref = "/identities/" + r.ID
			}
		case domain.RotationNeverRecorded:
			never++
			if firstNever == "" {
				firstNever = fmt.Sprintf("%s has a %d-day rule and no rotation has ever been recorded",
					r.Name, derefOrInt(r.RotationDays))
				neverHref = "/identities/" + r.ID
			}
		case domain.RotationUnreadable:
			// Folded into `never`: the inventory cannot say when this was last
			// rotated, which is the same claim and the same severity. Counting
			// it as overdue would assert a lapse from a value nobody can read.
			never++
			if firstNever == "" {
				firstNever = fmt.Sprintf("%s has a rotation date that will not parse", r.Name)
				neverHref = "/identities/" + r.ID
			}
		}
	}

	var out []Finding
	if overdue > 0 {
		// FAULT: the estate's own declared rule says 90 days and it has been
		// 200. Something is wrong NOW -- the same shape as "a contract has
		// lapsed", which findings.go's severity comment names as the archetypal
		// Fault.
		out = append(out, Finding{Severity: FindingFault, Count: overdue,
			Label: "credential past its own rotation rule", Detail: firstOverdue,
			Href: overdueHref})
	}
	if never > 0 {
		// GAP, not Fault, and this is the call that makes the other two
		// trustworthy. The inventory does not know when this was last rotated,
		// so it cannot say whether the rule is met; calling it a Fault would
		// claim knowledge nobody has -- it might have been rotated last week by
		// somebody who did not write it down. "A report that cannot say 'I do
		// not know' is a report that guesses."
		out = append(out, Finding{Severity: FindingGap, Count: never,
			Label: "credential with a rotation rule and no rotation ever recorded",
			Detail: firstNever, Href: neverHref})
	}

	// The third: a LIVE dependency naming a RETIRED credential. GAP, for the
	// call template_drift made for the same reason -- the inventory contradicts
	// itself: either the edge is stale or the service is authenticating with a
	// withdrawn credential, and it is not knowable from here. The
	// counter-argument, that a withdrawn credential still in use is sharper than
	// a missing port template and should be a Fault, is real; it is recorded
	// here rather than left for the next person to re-make.
	var stale []struct {
		IdentityID   string `db:"identity_id"`
		IdentityName string `db:"identity_name"`
		ConsumerCode string `db:"consumer_code"`
	}
	if err := s.read(ctx, &stale, `
		SELECT i.id AS identity_id, i.name AS identity_name,
		       COALESCE(cs.code, '') AS consumer_code
		FROM dependency d
		JOIN identity i ON i.id = d.identity_id
		LEFT JOIN service cs ON cs.id = d.consumer_service_id
		WHERE d.lifecycle <> ? AND i.lifecycle = ?`,
		domain.LifecycleRetired, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("gathering withdrawn-credential findings: %w", err)
	}
	if len(stale) > 0 {
		// Go, for collation -- and the first row becomes the finding's example
		// and its Href, so an unstable order would make the dashboard link
		// somewhere different on each render.
		sort.SliceStable(stale, func(a, b int) bool {
			x, y := stale[a], stale[b]
			if x.ConsumerCode != y.ConsumerCode {
				return x.ConsumerCode < y.ConsumerCode
			}
			if x.IdentityName != y.IdentityName {
				return x.IdentityName < y.IdentityName
			}
			return x.IdentityID < y.IdentityID
		})
		out = append(out, Finding{Severity: FindingGap, Count: len(stale),
			Label:  "live dependency naming a withdrawn credential",
			Detail: fmt.Sprintf("%s still authenticates as %s", stale[0].ConsumerCode, stale[0].IdentityName),
			Href:   "/identities/" + stale[0].IdentityID})
	}
	return out, nil
}
```

Register in `internal/store/findings.go`, beside `TemplateDriftFindings` (~line 214):

```go
	// Credential rotation (WP-J8). Three findings and two deliberate
	// non-findings; rotation_findings.go's header argues each. This is the only
	// place a rotation rule the estate set for itself becomes visible without
	// somebody opening /identities.
	rotation, err := s.RotationFindings(ctx)
	if err != nil {
		return nil, fmt.Errorf("gathering rotation findings: %w", err)
	}
	out = append(out, rotation...)
```

- [ ] **Step 4: Run the tests, both engines**

- [ ] **Step 5: Prove the severities can fail**

| Mutation | Test that must go red |
|---|---|
| Change the `never_recorded` finding's severity to `FindingFault` | `TestRotationFindings`'s severity assertion — this is the judgement the spec argues hardest for, so it gets its own assertion rather than being folded into a count |
| Emit a finding for `RotationUnmanaged` | `TestAnUnmanagedCredentialIsNotAFinding` |
| Change `RotationUnreadable` to fall into the `overdue` bucket | `TestRotationFindings`'s unreadable case — and note the direction: unreadable in the Fault bucket *asserts a lapse from a value nobody can read* |
| Drop the `i.lifecycle = ?` predicate from the stale-credential query | `TestRotationFindings`'s third-finding count |

- [ ] **Step 6: Update `docs/ROADMAP.md`**

WP-J8 (line 1185) goes **DONE** with the date, and says plainly what is *not* covered, because this entry has already been found once marked with its scope unbuilt and the fix is not to repeat the pattern in the other direction:

```markdown
**WP-J8 · Identity surface** — S — **DONE 2026-09-15**

List and detail pages, create, correct, withdraw, and `POST /identities/{id}/rotation`
— the explicit "record a rotation" action, which is the feature rather than a side
effect of an edit form. `last_rotated` has exactly one writer, enforced by
`internal/store/last_rotated_source_test.go`. Rotation is five-valued
(`domain.RotationState`); the two-valued `RotationOverdue` is deleted, because it
answered `false` both to "policy set, nothing ever recorded" and to an unparseable
stored value. Migration `00069` added `row_version` and a date-shape `CHECK`.
`writeSurfaceUnbuilt` is at **zero**. The demo estate seeds four of the five
rotation states plus a retired credential still named by a live dependency.

**NOT covered, deliberately:** a machine-recorded rotation (a Vault webhook
stamping `last_rotated` needs the whole declared/observed argument redone from
`docs/AUDIT.md` rules 1-7, and rule 6 currently forbids an agent-reachable
handler from returning an identity row at all); indexing identities for global
search (**`secret_ref` must never enter `search_index`**, which every
authenticated user can read); a restore path (a retired identity's `(realm, name)`
is immediately reusable by design, and a restore would put two live rows in
contention for one name); a "due soon" finding band; and joining `cert_subject`
identities to `certificate`, which has a real question in it — which of the two
owns the expiry date.
```

- [ ] **Step 7: Run the full gate**

```bash
make lint
make test; echo "make test exit: $?"
```

**Capture `make test`'s exit code directly, not through a pipeline** — the status you read from `make test | tail` is `tail`'s, and a red gate reports green. That has cost this project a release tag before.

- [ ] **Step 8: Manual check against the demo**

The branch is deployable to the demo box (`invctl-demo.service`, port 8088). Before calling it done, open `/identities` as the administrator and confirm, by eye:

- four distinct rotation pills across five rows, none of them blank;
- the secret-ref column says `recorded` / `none` and **no path appears anywhere on the list**;
- `svc-legacy-etl` is findable under the retired filter and shows the dependency that still names it;
- `svc-orders`' detail page shows a `last_rotated` entry in its timeline with an actor beside an `actor_kind`;
- recording a rotation dated tomorrow comes back as a 422 with the date still in the field;
- the dashboard's findings list carries one Fault and two Gaps from this feature.

- [ ] **Step 9: Commit**

```bash
git add -A && make lint
git commit   # why: a rotation rule nobody is told about is a rule nobody follows;
             # Gap rather than Fault for "never recorded" is what makes the
             # Fault trustworthy
```

---

## Self-review notes

- **Spec coverage:** migration + classification + bulk bumps + §4 amendment (T1); spec constructor, `Validate`, five-state rotation, `RotationOverdue` deleted (T2); one-writer rule, AST guard, backdate/post-date, the no-op-writes-nothing rule, `IdentityUsage`, `writeSurfaceUnbuilt` → zero (T3); list and detail, `secret_ref` display gate (T4); four `writeAdminOnly` routes, 422/409 (T5); demo seed (T6); three findings, roadmap (T7). Non-goals — machine-recorded rotation, search indexing, a restore path, a "due soon" band, certificates — appear in no task, deliberately.
- **Both engines everywhere:** every store task's verification step names both. The failure this prevents is a green SQLite run hiding a Postgres-only classification failure.
- **The sequencing hazards, stated:** `domain.Identity.RowVersion` must land **in the same commit** as the migration, because `s.read` is not `sqlx.Unsafe` and `SELECT * FROM identity` fails the moment the column exists without the field (T1). `writeSurfaceUnbuilt`'s entry must be deleted **in the same commit** that adds `UpdateIdentity` and `RetireIdentity`, because the map is two-directional (T3). Both `ListIdentities` call sites convert **in the task that changes the signature** (T3), not later.
- **`RotationUnreadable` is never seeded**, and that is a decision: reaching it needs a value the DB `CHECK` accepts and `ParseDate` rejects (`2026-02-31`), which is a corrupt row. It is covered by unit tests (T2) and a web test that writes it past the Go layer (T4).

---
