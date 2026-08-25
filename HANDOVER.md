# HANDOVER — Infrastructure Inventory (working name: `invctl`)

Status: **pre-implementation**. This document is the design record and starting point for a proof of concept. Nothing has been built yet.

Audience: whoever picks up the build — human or AI assistant. Read this first, then `CLAUDE.md` for the working rules.

---

## 1. What this is

A CMDB for a segmented infrastructure estate, covering four stacked layers:

1. **Physical network** — firewalls, switches, ports (RJ45/SFP/SFP28/QSFP), speeds, cabling
2. **Physical compute** — servers, mostly hypervisors
3. **Virtualization** — clusters, nodes, VMs
4. **Workloads** — services running as systemd units, Windows services, containers, or Kubernetes workloads

The distinguishing feature versus a spreadsheet or a generic asset tracker is that **relationships are first-class**. The system exists to answer questions that require traversing the graph:

- *If I reboot node 3, what breaks?*
- *What talks to this database, and over which port, using which credential?*
- *Which assets sit in more than one environment?*
- *Which firewall rules have no service dependency justifying them?*

If a feature doesn't serve one of those, it's out of scope for the POC.

### Non-goals

- Not a monitoring system. It stores *intended* state and *last observed* state; it does not poll for health or alert.
- Not a configuration management system. It does not push changes.
- Not a ticketing system.
- Not an IPAM replacement on day one — but it needs enough IP/prefix modelling to resolve an address to an asset.

---

## 2. Prior art — read before starting

**NetBox** (and Nautobot) already implement layers 1–3 well: DCIM, IPAM, virtualization, custom fields, change logging, dynamic Ansible inventory, plugin API. What they model poorly is layer 4: systemd units, Windows services with run-as accounts, containers, k8s workloads, and service-to-service dependencies with failure semantics.

The decision has been made to **build rather than extend**, on the grounds that the service/dependency layer is the centerpiece and shouldn't be a bolt-on to someone else's data model. That decision is recorded, not re-litigated. But the honest trade is: this project now owns IPAM and DCIM code forever. Budget for it, and keep layers 1–3 deliberately minimal.

---

## 3. Core design decisions

These are load-bearing. Changing one invalidates a lot of downstream code.

### 3.1 Containment is a tree; everything else is a graph

Two orthogonal relationship types, stored differently:

- **Containment** — site → rack → chassis → hypervisor → VM. A strict tree. Stored as `parent_id` plus a **closure table** (`asset_closure`).
- **Everything else** — cabling, dependencies, backup coverage, identity usage — is a graph with cycles. Stored as explicit typed edge tables.

Conflating them is the standard failure mode of home-grown CMDBs.

### 3.2 Logical service ≠ running instance

`service` is logical. `service_instance` is a running copy on a specific host.

A three-node Vault cluster is **one** `service` with **three** `service_instance` rows. Dependencies, ownership, firewall justification, and SLOs attach to the *service*. Placement, runtime config, and observed state attach to the *instance*.

Get this wrong and every dependency edge has to be written once per replica.

### 3.3 Availability policy makes impact analysis meaningful

Each service declares how many instances it needs:

| Policy | Healthy when | Example |
|---|---|---|
| `standalone` | ≥1 instance alive | a lone Windows service |
| `active_active` | surviving ≥ `min_healthy` | HAProxy pair, k8s Deployment |
| `active_passive` | primary alive, or standby promotable | Patroni, Veeam proxy pair |
| `quorum` | surviving ≥ ⌊n/2⌋+1 | Vault Raft, Ceph mon, etcd |
| `sharded` | every shard has ≥1 replica | Mimir ingesters |

Without this, "reboot node 3" reports everything on node 3 as down, which is useless. With it, losing one of three Vault nodes reports `ok`.

### 3.4 Dependency nature makes propagation honest

| Nature | Consumer behaviour when provider is down |
|---|---|
| `hard` | Down immediately |
| `soft` | Degraded, keeps serving |
| `startup` | Fine now, **will not restart** |
| `async` | Fine until `tolerance_seconds`, then degraded |
| `optional` | No effect (metrics scrape, telemetry) |

`startup` is the highest-value one and the one people forget. A service running happily with a dead startup dependency is a landmine — the outage lands weeks later when something unrelated triggers a restart. The maintenance-window report must surface these separately.

### 3.5 Declared vs discovered, never overwritten

Every fact carries `source`, `last_seen`, and `confidence`. Hand-declared data is never silently overwritten by a discovery agent. Disagreements go to a conflict queue.

This is what makes drift detection possible: a discovered dependency with no declared counterpart is an undocumented dependency (and possibly an undocumented firewall rule). A declared dependency never observed is either dead or unmonitored.

### 3.6 Environment is a membership, not a column

Most assets belong to one environment. Some are shared. A `transit` environment brokers anything that must cross. So it's a many-to-many table, and "which assets span environments" is a query you will run constantly.

### 3.7 Soft delete only

Nothing is ever hard-deleted. `lifecycle` moves to `retired`. The decommissioned server is exactly what an auditor asks about six months later.

---

## 4. Portability constraints (SQLite primary, PostgreSQL supported)

**This section is the one most likely to be violated by accident. Read it twice.**

The stack targets SQLite for the POC and single-node deployments, PostgreSQL for larger installs. One schema, one query set, both engines. That rules out a lot of what you'd reach for on Postgres alone.

### Forbidden — Postgres-only features

| Don't use | Use instead |
|---|---|
| `inet` / `cidr` / `macaddr` | `TEXT` canonical form + integer range columns (§4.1) |
| `ltree` | closure table (§4.2) |
| Native arrays (`text[]`) | junction table, or JSON text handled in Go |
| `ENUM` types | `TEXT` + `CHECK (col IN (...))` |
| `jsonb` operators (`@>`, `->>` in WHERE) | store JSON as `TEXT`, parse in Go, never query inside it |
| `SERIAL` / `IDENTITY` | UUIDv7 as `TEXT`, generated in Go |
| Exclusion constraints | application-level validation + a lint rule |
| `plpgsql` functions | application code in Go |
| `num_nonnulls()`, `generate_series()` | explicit CHECK expressions; loops in Go |
| `NOW()` / `CURRENT_TIMESTAMP` defaults | timestamps generated in Go, passed as parameters |
| `RETURNING` on multi-row updates | single-row `RETURNING` only (both support it); otherwise re-select |

### Safe on both

Recursive CTEs, window functions, `ON CONFLICT ... DO UPDATE`, partial indexes (`CREATE INDEX ... WHERE`), `ON DELETE CASCADE`, `CHECK` constraints, `BOOLEAN` with `TRUE`/`FALSE` literals, `COALESCE`, `CASE`.

### 4.1 IP addresses without `inet`

Store three columns and normalize in Go on write:

```
addr_text   TEXT NOT NULL     -- '10.20.30.5' or '2001:db8::1', canonical form
addr_family INTEGER NOT NULL  -- 4 or 6
addr_start  BLOB NOT NULL     -- big-endian bytes, 4 or 16 wide
addr_end    BLOB NOT NULL     -- same as start for a host; broadcast for a prefix
```

`BLOB` comparison is bytewise in both engines, so "which prefix contains this address" becomes:

```sql
SELECT * FROM prefix
WHERE addr_family = ?
  AND addr_start <= ?
  AND addr_end   >= ?
ORDER BY length(addr_start) DESC, addr_start DESC
LIMIT 1;
```

MAC addresses: `TEXT`, lowercase, colon-separated, normalized in Go.

### 4.2 Containment without `ltree`

```sql
CREATE TABLE asset_closure (
  ancestor_id   TEXT NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
  descendant_id TEXT NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
  depth         INTEGER NOT NULL,
  PRIMARY KEY (ancestor_id, descendant_id)
);
```

Every asset has a self-row at `depth = 0`. Rebuild the affected subtree in a transaction on insert or reparent — don't try to be clever with incremental updates.

"Is asset A at or below asset B" becomes an index lookup:

```sql
SELECT 1 FROM asset_closure WHERE ancestor_id = ? AND descendant_id = ?;
```

### 4.3 SQLite operational settings

Per connection: `PRAGMA foreign_keys = ON` (off by default — foreign keys silently do nothing without it), `PRAGMA journal_mode = WAL`, `PRAGMA busy_timeout = 5000`, `PRAGMA synchronous = NORMAL`.

Use **two connection pools**: a writer pool capped at `SetMaxOpenConns(1)` and a reader pool. SQLite permits one writer; without this you will get intermittent `SQLITE_BUSY` under concurrent handlers.

### 4.4 Search

Abstract behind a `Search` interface with two implementations: FTS5 virtual table on SQLite, `pg_trgm` + `tsvector` on Postgres. This is the one place where dialect-specific code is expected and acceptable.

---

## 5. Schema — core tables

Portable DDL. This is the POC subset, not the full model.

```sql
-- ---------- environments ----------
CREATE TABLE environment (
  id          TEXT PRIMARY KEY,
  code        TEXT NOT NULL UNIQUE,
  name        TEXT NOT NULL,
  role        TEXT NOT NULL CHECK (role IN ('production','staging','dev','transit','shared','dr')),
  in_scope    BOOLEAN NOT NULL DEFAULT FALSE,
  criticality INTEGER NOT NULL DEFAULT 3,
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);

-- ---------- assets (layers 1-3) ----------
CREATE TABLE asset (
  id          TEXT PRIMARY KEY,
  kind        TEXT NOT NULL CHECK (kind IN (
                'site','rack','pdu','firewall','switch','patch_panel',
                'server','hypervisor','cluster','vm','k8s_node','storage')),
  name        TEXT NOT NULL,
  parent_id   TEXT REFERENCES asset(id),
  serial      TEXT,
  asset_tag   TEXT,
  vendor      TEXT,
  model       TEXT,
  lifecycle   TEXT NOT NULL DEFAULT 'active'
                CHECK (lifecycle IN ('planned','active','maintenance','deprecated','retired')),
  owner_team  TEXT,
  attrs       TEXT NOT NULL DEFAULT '{}',   -- opaque JSON, never queried in SQL
  created_at  TEXT NOT NULL,
  updated_at  TEXT NOT NULL
);
CREATE INDEX idx_asset_kind   ON asset(kind);
CREATE INDEX idx_asset_parent ON asset(parent_id);

CREATE TABLE asset_closure (
  ancestor_id   TEXT NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
  descendant_id TEXT NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
  depth         INTEGER NOT NULL,
  PRIMARY KEY (ancestor_id, descendant_id)
);
CREATE INDEX idx_closure_desc ON asset_closure(descendant_id);

CREATE TABLE asset_environment (
  asset_id       TEXT NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
  environment_id TEXT NOT NULL REFERENCES environment(id) ON DELETE CASCADE,
  note           TEXT,
  PRIMARY KEY (asset_id, environment_id)
);

-- ---------- network ----------
CREATE TABLE interface (
  id           TEXT PRIMARY KEY,
  asset_id     TEXT NOT NULL REFERENCES asset(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  form_factor  TEXT NOT NULL CHECK (form_factor IN
                 ('rj45','sfp','sfp+','sfp28','qsfp+','qsfp28','virtual','lag','loopback')),
  speed_mbps   INTEGER,
  mac          TEXT,
  mtu          INTEGER,
  lag_parent_id TEXT REFERENCES interface(id),
  is_mgmt      BOOLEAN NOT NULL DEFAULT FALSE,
  enabled      BOOLEAN NOT NULL DEFAULT TRUE,
  UNIQUE (asset_id, name)
);

CREATE TABLE link (
  id             TEXT PRIMARY KEY,
  a_interface_id TEXT NOT NULL REFERENCES interface(id) ON DELETE CASCADE,
  b_interface_id TEXT NOT NULL REFERENCES interface(id) ON DELETE CASCADE,
  medium         TEXT,
  length_m       INTEGER,
  CHECK (a_interface_id <> b_interface_id)
);

CREATE TABLE prefix (
  id             TEXT PRIMARY KEY,
  cidr_text      TEXT NOT NULL,
  addr_family    INTEGER NOT NULL CHECK (addr_family IN (4,6)),
  addr_start     BLOB NOT NULL,
  addr_end       BLOB NOT NULL,
  vlan_id        INTEGER,
  environment_id TEXT REFERENCES environment(id),
  role           TEXT,
  UNIQUE (cidr_text)
);

CREATE TABLE ip_address (
  id           TEXT PRIMARY KEY,
  addr_text    TEXT NOT NULL,
  addr_family  INTEGER NOT NULL CHECK (addr_family IN (4,6)),
  addr_start   BLOB NOT NULL,
  interface_id TEXT REFERENCES interface(id) ON DELETE SET NULL,
  role         TEXT NOT NULL DEFAULT 'primary'
                 CHECK (role IN ('primary','secondary','vip','mgmt','floating')),
  UNIQUE (addr_text, interface_id)
);
CREATE INDEX idx_ip_start ON ip_address(addr_family, addr_start);

-- ---------- services (layer 4) ----------
CREATE TABLE application (
  id         TEXT PRIMARY KEY,
  code       TEXT NOT NULL UNIQUE,
  name       TEXT NOT NULL,
  owner_team TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE service (
  id             TEXT PRIMARY KEY,
  application_id TEXT REFERENCES application(id),
  code           TEXT NOT NULL UNIQUE,
  name           TEXT NOT NULL,
  kind           TEXT NOT NULL CHECK (kind IN
                   ('db','cache','queue','web','api','proxy','auth','batch',
                    'agent','storage','infra','monitoring')),
  environment_id TEXT NOT NULL REFERENCES environment(id),
  availability   TEXT NOT NULL CHECK (availability IN
                   ('standalone','active_active','active_passive','quorum','sharded')),
  min_healthy    INTEGER,
  failover_mode  TEXT CHECK (failover_mode IN ('auto','manual','none')),
  tier           INTEGER NOT NULL DEFAULT 3 CHECK (tier BETWEEN 1 AND 4),
  rto_minutes    INTEGER,
  rpo_minutes    INTEGER,
  owner_team     TEXT,
  lifecycle      TEXT NOT NULL DEFAULT 'active'
                   CHECK (lifecycle IN ('planned','active','deprecated','retired')),
  attrs          TEXT NOT NULL DEFAULT '{}',
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);
CREATE INDEX idx_service_env ON service(environment_id);

CREATE TABLE service_instance (
  id             TEXT PRIMARY KEY,
  service_id     TEXT NOT NULL REFERENCES service(id) ON DELETE CASCADE,
  host_asset_id  TEXT NOT NULL REFERENCES asset(id),
  runtime_type   TEXT NOT NULL CHECK (runtime_type IN
                   ('systemd','windows_service','container','k8s_workload','appliance')),
  role           TEXT,
  ordinal        INTEGER NOT NULL DEFAULT 0,
  desired_state  TEXT NOT NULL DEFAULT 'running'
                   CHECK (desired_state IN ('running','stopped','disabled')),
  observed_state TEXT,
  observed_at    TEXT,
  source         TEXT NOT NULL DEFAULT 'declared',
  created_at     TEXT NOT NULL,
  updated_at     TEXT NOT NULL,
  UNIQUE (service_id, host_asset_id, ordinal)
);
CREATE INDEX idx_instance_host ON service_instance(host_asset_id);

CREATE TABLE rt_systemd (
  instance_id  TEXT PRIMARY KEY REFERENCES service_instance(id) ON DELETE CASCADE,
  unit_name    TEXT NOT NULL,
  unit_type    TEXT,
  exec_start   TEXT,
  run_as_user  TEXT,
  run_as_group TEXT,
  restart      TEXT,
  unit_after   TEXT NOT NULL DEFAULT '[]',   -- JSON array, parsed in Go
  unit_requires TEXT NOT NULL DEFAULT '[]',
  drop_ins     TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE rt_windows (
  instance_id       TEXT PRIMARY KEY REFERENCES service_instance(id) ON DELETE CASCADE,
  service_name      TEXT NOT NULL,
  display_name      TEXT,
  binary_path       TEXT,
  start_type        TEXT CHECK (start_type IN ('auto','auto_delayed','manual','disabled')),
  logon_identity_id TEXT REFERENCES identity(id),
  depends_on_svc    TEXT NOT NULL DEFAULT '[]',
  recovery_action   TEXT
);

CREATE TABLE rt_container (
  instance_id     TEXT PRIMARY KEY REFERENCES service_instance(id) ON DELETE CASCADE,
  engine          TEXT CHECK (engine IN ('docker','podman')),
  container_name  TEXT,
  compose_project TEXT,
  compose_service TEXT,
  image_repo      TEXT,
  image_tag       TEXT,
  image_digest    TEXT,
  restart_policy  TEXT,
  network_mode    TEXT,
  rootless        BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE TABLE rt_k8s (
  instance_id      TEXT PRIMARY KEY REFERENCES service_instance(id) ON DELETE CASCADE,
  cluster_asset_id TEXT REFERENCES asset(id),
  namespace        TEXT,
  workload_kind    TEXT,
  workload_name    TEXT,
  replicas_desired INTEGER,
  service_account  TEXT,
  image_digest     TEXT
);

-- ---------- endpoints & routing (layers 4-7) ----------
CREATE TABLE endpoint (
  id             TEXT PRIMARY KEY,
  service_id     TEXT NOT NULL REFERENCES service(id) ON DELETE CASCADE,
  name           TEXT NOT NULL,
  l4_proto       TEXT NOT NULL CHECK (l4_proto IN ('tcp','udp','sctp','unix')),
  port           INTEGER,
  unix_path      TEXT,
  bind_scope     TEXT NOT NULL CHECK (bind_scope IN
                   ('loopback','host','vip','cluster_ip','node_port','ingress','unix')),
  ip_address_id  TEXT REFERENCES ip_address(id),
  l7_proto       TEXT,
  tls_mode       TEXT NOT NULL DEFAULT 'none'
                   CHECK (tls_mode IN ('none','tls','mtls','starttls')),
  certificate_id TEXT,
  exposure       TEXT NOT NULL DEFAULT 'internal'
                   CHECK (exposure IN ('internal','environment','cross_env','external')),
  UNIQUE (service_id, name),
  CHECK ((l4_proto = 'unix' AND unix_path IS NOT NULL AND port IS NULL)
      OR (l4_proto <> 'unix' AND port IS NOT NULL AND unix_path IS NULL))
);

CREATE TABLE backend_pool (
  id           TEXT PRIMARY KEY,
  service_id   TEXT NOT NULL REFERENCES service(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  lb_algorithm TEXT,
  UNIQUE (service_id, name)
);

CREATE TABLE backend_member (
  pool_id     TEXT NOT NULL REFERENCES backend_pool(id) ON DELETE CASCADE,
  endpoint_id TEXT NOT NULL REFERENCES endpoint(id) ON DELETE CASCADE,
  weight      INTEGER NOT NULL DEFAULT 1,
  is_backup   BOOLEAN NOT NULL DEFAULT FALSE,
  PRIMARY KEY (pool_id, endpoint_id)
);

CREATE TABLE route (
  id                   TEXT PRIMARY KEY,
  frontend_endpoint_id TEXT NOT NULL REFERENCES endpoint(id) ON DELETE CASCADE,
  match_type           TEXT NOT NULL CHECK (match_type IN
                         ('sni','host_header','path_prefix','default')),
  match_value          TEXT,
  backend_pool_id      TEXT NOT NULL REFERENCES backend_pool(id),
  tls_termination      TEXT CHECK (tls_termination IN
                         ('passthrough','terminate','reencrypt')),
  priority             INTEGER NOT NULL DEFAULT 100
);

-- ---------- identities ----------
CREATE TABLE identity (
  id            TEXT PRIMARY KEY,
  kind          TEXT NOT NULL CHECK (kind IN
                  ('service_account','machine_account','api_token','cert_subject','human')),
  name          TEXT NOT NULL,
  realm         TEXT,
  secret_ref    TEXT,        -- Vault path or similar. NEVER the secret itself.
  rotation_days INTEGER,
  last_rotated  TEXT,
  owner_team    TEXT,
  lifecycle     TEXT NOT NULL DEFAULT 'active',
  UNIQUE (realm, name)
);

-- ---------- the dependency edge ----------
CREATE TABLE dependency (
  id                   TEXT PRIMARY KEY,
  consumer_service_id  TEXT NOT NULL REFERENCES service(id) ON DELETE CASCADE,
  consumer_instance_id TEXT REFERENCES service_instance(id) ON DELETE CASCADE,
  provider_endpoint_id TEXT REFERENCES endpoint(id) ON DELETE CASCADE,
  provider_route_id    TEXT REFERENCES route(id) ON DELETE CASCADE,
  nature               TEXT NOT NULL CHECK (nature IN
                         ('hard','soft','startup','async','optional')),
  tolerance_seconds    INTEGER,
  failure_mode         TEXT NOT NULL,
  identity_id          TEXT REFERENCES identity(id),
  auth_method          TEXT,
  firewall_rule_ref    TEXT,
  source               TEXT NOT NULL DEFAULT 'declared' CHECK (source IN
                         ('declared','discovered_netstat','discovered_systemd',
                          'discovered_k8s','discovered_config')),
  confidence           REAL,
  first_seen           TEXT,
  last_seen            TEXT,
  verified_by          TEXT,
  verified_at          TEXT,
  created_at           TEXT NOT NULL,
  updated_at           TEXT NOT NULL,
  CHECK ((provider_endpoint_id IS NOT NULL AND provider_route_id IS NULL)
      OR (provider_endpoint_id IS NULL AND provider_route_id IS NOT NULL))
);
CREATE INDEX idx_dep_consumer ON dependency(consumer_service_id);
CREATE INDEX idx_dep_endpoint ON dependency(provider_endpoint_id);

CREATE TABLE dependency_data_class (
  dependency_id TEXT NOT NULL REFERENCES dependency(id) ON DELETE CASCADE,
  data_class    TEXT NOT NULL CHECK (data_class IN
                  ('chd','sad','pii','credential','telemetry','config','none')),
  PRIMARY KEY (dependency_id, data_class)
);

-- ---------- audit ----------
CREATE TABLE change_log (
  id          TEXT PRIMARY KEY,
  entity_type TEXT NOT NULL,
  entity_id   TEXT NOT NULL,
  action      TEXT NOT NULL CHECK (action IN ('create','update','delete','retire')),
  actor       TEXT NOT NULL,
  actor_kind  TEXT NOT NULL CHECK (actor_kind IN ('user','agent','system')),
  diff        TEXT NOT NULL,      -- JSON
  ticket_ref  TEXT,
  at          TEXT NOT NULL
);
CREATE INDEX idx_changelog_entity ON change_log(entity_type, entity_id, at);

-- ---------- auth ----------
CREATE TABLE app_user (
  id            TEXT PRIMARY KEY,
  username      TEXT NOT NULL UNIQUE,
  display_name  TEXT,
  email         TEXT,
  source        TEXT NOT NULL CHECK (source IN ('local','ldap')),
  password_hash TEXT,             -- argon2id; NULL for LDAP users
  is_active     BOOLEAN NOT NULL DEFAULT TRUE,
  last_login_at TEXT,
  created_at    TEXT NOT NULL
);
```

Note the forward reference: `rt_windows.logon_identity_id` references `identity`, so create `identity` before `rt_windows` in the migration ordering.

---

## 6. The impact engine

Lives in `internal/impact`, written in Go, **not** in SQL. It's a fixed-point iteration, not a single traversal, because a service's status depends on the aggregate of all its inbound edges — and cycles are normal (app → db → auth → app).

Signature:

```go
type Request struct {
    DownAssetIDs   []string
    WindowSeconds  int    // a 3-minute reboot and a 45-minute one differ
}

type Result struct {
    Services      []ServiceImpact  // status: ok | degraded | down
    WontRestart   []ServiceImpact  // startup deps unmet — running now, won't come back
    Cycles        [][]string       // dependency cycles found during traversal
    SafeOrder     []string         // suggested shutdown order
}
```

### Phase 1 — placement

Resolve downed assets to lost instances via the closure table. One query handles "reboot this VM", "reboot this hypervisor", "this rack loses power", and "this PDU fails" identically:

```sql
SELECT si.id, si.service_id
FROM service_instance si
JOIN asset_closure c ON c.descendant_id = si.host_asset_id
WHERE c.ancestor_id IN (/* down asset ids */);
```

### Phase 2 — capacity

For each affected service, count surviving instances and evaluate against `availability`:

- `standalone` → surviving 0 = `down`
- `quorum` → surviving < ⌊total/2⌋+1 = `down`
- `active_active` → surviving < `min_healthy` = `degraded`; 0 = `down`
- `active_passive` → primary lost + `failover_mode='manual'` = `degraded`; all lost = `down`
- `sharded` → any shard at 0 replicas = `degraded`

### Phase 3 — propagation to fixed point

Loop until no status changes, guard at 20 iterations. For each dependency whose provider is not `ok`:

| Nature | Provider `down` | Provider `degraded` |
|---|---|---|
| `hard` | consumer `down` | consumer `degraded` |
| `soft` | consumer `degraded` | no change |
| `async` | `degraded` if `tolerance_seconds < WindowSeconds` | no change |
| `startup` | no status change — append to `WontRestart` | no change |
| `optional` | no change | no change |

Status is monotonic within a run (`ok` → `degraded` → `down`, never back), which guarantees termination.

Routes are **nodes in the graph, not passthroughs**. A dependency on a route resolves to the route's pool, and the pool's health is derived from its members. This is what lets you detect "the proxy is up but every backend is on node 3".

### Derived reports on the same engine

- **Safe reboot order** — topological sort of the affected subgraph on `hard` edges, leaf-first. A cycle found here is a real finding, not a bug — report it.
- **Maintenance eligibility** — given an asset and a maximum acceptable impact tier, return yes/no plus blocking services.

---

## 7. POC milestones

Each milestone should end with something demonstrable.

**M0 — skeleton (1–2 days)**
Go module, config from env, migrations running on SQLite and Postgres, health endpoint, base layout template, Tailwind build, session middleware, local login with a seeded admin.

**M1 — assets and environments**
CRUD for `environment` and `asset`, closure table maintenance on insert/reparent, environment membership, list views with filter, detail pages. Change log written on every mutation.

**M2 — services and instances**
CRUD for `application`, `service`, `service_instance` and the four `rt_*` tables. Service detail page: header, instances table, placement.

**M3 — endpoints and dependencies**
CRUD for `endpoint`, `dependency`. The two dependency panels on the service page — upstream and downstream. This is the first point where the tool is more useful than a spreadsheet.

**M4 — impact engine**
`internal/impact` implemented and unit-tested against a fixture graph. "Simulate outage" button on every asset detail page rendering the affected set grouped by status, plus the `WontRestart` list.

**M5 — search and LDAP**
Global search box resolving IP, MAC, serial, hostname, service code, port. LDAP bind authentication alongside local users.

Anything beyond M5 — discovery agents, lint engine, firewall reconciliation, Ansible inventory endpoint — is post-POC. Do not start them early.

---

## 8. Repository layout

```
/cmd/invctl/main.go            entrypoint, wiring only
/internal/config               env parsing, validation at startup
/internal/domain               entities + business rules, zero external deps
/internal/store                Store interface, shared SQL
  /sqlite                      SQLite-specific: FTS5, pragmas
  /postgres                    Postgres-specific: pg_trgm
  /migrations                  goose SQL files, shared where possible
/internal/impact               the impact engine
/internal/auth                 local + LDAP authenticators, sessions, RBAC
/internal/web
  /handlers                    one file per resource
  /middleware                  auth, CSRF, logging, recovery
  /render                      template helpers, HTMX partial dispatch
/web/templates                 html/template; layouts/, pages/, partials/
/web/static                    vendored htmx.min.js, alpine.min.js, app.css
/testdata                      fixture graphs for impact tests
Makefile
HANDOVER.md
CLAUDE.md
```

Templates and static assets are embedded with `go:embed` so the deliverable is a single binary.

---

## 9. Getting started

```bash
make dev          # migrate + seed + run with live Tailwind rebuild
make test         # go test ./...
make migrate      # apply migrations to $INV_DB_DSN
make seed         # load testdata fixture
make build        # CGO_ENABLED=0 static binary
```

Minimum environment:

```bash
INV_DB_DRIVER=sqlite            # or postgres
INV_DB_DSN=file:invctl.db?_txlock=immediate
INV_LISTEN=:8080
INV_SESSION_KEY=<32 random bytes, base64>
INV_ADMIN_USERS=gabriel,nikolaj # POC RBAC: comma-separated admin usernames
INV_AUTH_LOCAL=true
INV_AUTH_LDAP=false
```

A custom field's value is folded into `change_log` as a plain change counter
rather than as text (`internal/store/customvalues.go`, `docs/AUDIT.md`) --
which field, how many times it has changed, never the value itself. There is
no key involved and nothing to configure.

---

## 10. Seed data

Build a fixture that exercises the interesting cases, not a toy. It should include:

- Three environments: one production and in-scope, one out-of-scope, one `transit`
- One shared switch belonging to two environments (exercises the span-detection query)
- A three-node quorum service (exercises `quorum` — losing one node must report `ok`)
- An active/passive database pair with `failover_mode='manual'` (must report `degraded`, not `down`)
- A proxy with a route and a two-member pool where both members sit on the same host (exercises the route-as-node logic)
- One `startup` dependency and one `async` dependency with a 300-second tolerance
- A dependency cycle (must not hang the engine)

The impact engine's unit tests assert against this fixture. If the fixture doesn't contain a case, the engine isn't tested for it.

---

## 11. Open questions

Decide these before the milestone that needs them, not now.

1. **k8s granularity** — one `service_instance` per pod, or one per workload with a replica count? Per-pod is correct for impact analysis but means the discovery reconciler owns those rows entirely and nobody hand-edits them. *Needed by M2.*
2. **Interface identity across reboots** — is `(asset_id, name)` stable enough, or is MAC the real key? Affects reconciler idempotency. *Needed by M1.*
3. **Change log granularity** — full-row snapshots or field-level diffs? Diffs are smaller and support point-in-time reconstruction; snapshots are simpler. *Needed by M1.*
4. **Multi-tenancy of the transit zone** — can a single asset be in two transit environments? Probably yes, but it affects the span-detection query's definition of a false positive. *Needed by M1.*
5. **Where certificates live** — full entity, or just a `certificate_id` string reference to an external system? *Needed by M3.*

---

## 12. Glossary

| Term | Meaning here |
|---|---|
| Asset | Anything physical or virtual in layers 1–3: rack, switch, server, VM |
| Service | A logical workload. One row regardless of how many replicas run |
| Service instance | One running copy of a service on one host |
| Endpoint | A listening socket belonging to a service |
| Route | An L7 matching rule mapping a frontend endpoint to a backend pool |
| Dependency | A directed edge from a consumer service to a provider endpoint or route |
| Nature | The failure semantics of a dependency: hard, soft, startup, async, optional |
| Availability policy | How many instances a service needs to be considered healthy |
| Environment | A segmentation boundary. Membership is many-to-many |
| Transit | An environment whose role is to broker traffic between other environments |
| Declared | Data entered by an operator. Authoritative |
| Discovered | Data written by a reconciler. Never overwrites declared |
| Closure table | The containment tree flattened into ancestor/descendant pairs |
