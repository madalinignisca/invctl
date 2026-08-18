// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// The read path behind the token-scoped inventory API (WP-A2).
//
// EVERY QUERY IN THIS FILE IS A SELECT, and that is a property of the file
// rather than an accident of what has been needed so far. Adding an INSERT,
// UPDATE or DELETE here is a design error, not an oversight: the API is
// read-only by contract (docs/api-design.md §1), nothing here mutates declared
// state and therefore nothing here writes a change_log row -- so a mutation
// added in this file would be a mutation with no audit entry, which is the one
// failure CLAUDE.md's audit rule exists to prevent. A write belongs in the
// file that owns the entity, where the transaction and its audit row already
// live.
//
// The other property worth stating: the scope predicate is applied INSIDE
// every paginated query, never to the page after it has been fetched.
// Filtering a fetched page returns short pages and eventually an empty one
// while rows still remain -- a bug that only appears for a scoped token
// against a large estate, which is exactly the case nobody tests by hand.

package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
)

// ErrOutOfScope is an entity that exists but lies outside the caller's
// environment scope. It WRAPS domain.ErrNotFound deliberately: every existing
// caller's errors.Is(err, domain.ErrNotFound) keeps working and the HTTP status
// is unchanged, so the client cannot tell the two apart -- that
// indistinguishability is the security property (docs/api-design.md §3: a 403
// would be an existence oracle over the estate).
//
// The distinction exists ONLY so the server can log which one happened. §3
// promises an operator-side security event naming the credential and the
// entity as the entire mitigation for a deliberately unhelpful 404, and a
// store that returned a bare ErrNotFound for both cases would make that
// promised log line unwriteable.
var ErrOutOfScope = fmt.Errorf("out of scope: %w", domain.ErrNotFound)

// errEmptyScope is the refusal for a scope that names no environment.
//
// It cannot arrive through configuration -- domain.NewEnvironmentScope rejects
// an empty set with "there is no wildcard" -- and it is refused here anyway
// because of what the SQL would otherwise do: placeholders(0) renders
// `NOT IN (NULL)`, whose three-valued logic makes the NOT EXISTS subquery
// vacuously false and therefore admits EVERY asset. A predicate that fails open
// on an empty input is worth one explicit guard.
var errEmptyScope = fmt.Errorf("a scope naming no environment permits nothing: %w", domain.ErrInvalid)

// apiDefaultLimit and apiMaxLimit bound a page at the store boundary.
//
// internal/api clamps the client's ?limit= to the same numbers before it gets
// here. Repeated rather than imported because a store method is callable
// without going through the HTTP layer, and "the caller already clamped it" is
// not a property the store can assert about itself. Two constants agreeing is
// cheaper than one unbounded query.
const (
	apiDefaultLimit = 100
	apiMaxLimit     = 500
)

func apiLimit(n int) int {
	if n <= 0 {
		return apiDefaultLimit
	}
	if n > apiMaxLimit {
		return apiMaxLimit
	}
	return n
}

// ---------------------------------------------------------------------------
// The scope predicate
// ---------------------------------------------------------------------------

// assetScopeSQL renders domain.EnvironmentScope.AllowsAll as a SQL predicate
// over one asset alias.
//
// An asset is visible iff it is in AT LEAST ONE environment and EVERY
// environment it is in is inside the token's scope.
//
// AllowsAll, not AllowsAny. sw-core-1 sits in {prod, dev}; a {dev} token that
// could read it would learn the name, site, rack and addresses of a
// production-facing device by way of its least sensitive membership. The write
// side reasons exactly this way for observations (see domain.AllowsAll) and
// disclosure is the mirror image of that argument.
//
// The `NOT EXISTS ... NOT IN` pair is the "every" half; the plain EXISTS is the
// "at least one" half, and without it an asset in no environment would be
// vacuously allowed -- the opposite of the existing rule that an entity in no
// environment is covered by nobody, a data gap surfaced as a denial rather than
// an implicit allow.
//
// Portable by construction: two correlated subqueries, a join and an IN list of
// bound `?` placeholders. No array, no ANY, no dialect-specific operator.
func assetScopeSQL(alias string, scope domain.EnvironmentScope) (string, []any) {
	codes := scope.Codes()
	sql := `EXISTS (SELECT 1 FROM asset_environment ae WHERE ae.asset_id = ` + alias + `.id)
	  AND NOT EXISTS (
	    SELECT 1 FROM asset_environment ae2
	    JOIN environment e2 ON e2.id = ae2.environment_id
	    WHERE ae2.asset_id = ` + alias + `.id AND e2.code NOT IN (` + placeholders(len(codes)) + `))`
	return sql, anySlice(codes)
}

// serviceScopeSQL is the same rule over a service's single environment.
//
// A service carries one environment_id rather than a set, so AllowsAll over a
// one-element slice is the same as Allows. It is written in the AllowsAll shape
// anyway -- "exists an environment, and none of them is outside the scope" --
// so that a future many-to-many service/environment change cannot quietly widen
// disclosure by leaving a predicate that only ever considered the first row.
func serviceScopeSQL(alias string, scope domain.EnvironmentScope) (string, []any) {
	codes := scope.Codes()
	sql := `EXISTS (SELECT 1 FROM environment se WHERE se.id = ` + alias + `.environment_id)
	  AND NOT EXISTS (
	    SELECT 1 FROM environment se2
	    WHERE se2.id = ` + alias + `.environment_id AND se2.code NOT IN (` + placeholders(len(codes)) + `))`
	return sql, anySlice(codes)
}

// ---------------------------------------------------------------------------
// Rows
// ---------------------------------------------------------------------------

// APIAssetRow carries only what the published asset DTO needs. It is not
// AssetRow: the UI's row grows a field whenever a list view needs one, and this
// one may only grow when somebody deliberately publishes a field.
type APIAssetRow struct {
	ID        string `db:"id"`
	Kind      string `db:"kind"`
	Name      string `db:"name"`
	Lifecycle string `db:"lifecycle"`
	// Site and Rack are resolved through asset_closure rather than through one
	// hop of parent_id: a VM's parent is its hypervisor, so a single hop finds
	// neither, and docs/api-design.md §4's own Ansible example publishes
	// invctl_site for a VM.
	Site *string `db:"site"`
	Rack *string `db:"rack"`
	Role *string `db:"role"`

	Environments []string `db:"-"`
	Addresses    []string `db:"-"`
	Services     []string `db:"-"`
}

// APIServiceRow is the published view of a service. Criticality is the schema's
// `tier`, renamed at the contract boundary.
type APIServiceRow struct {
	ID              string `db:"id"`
	Code            string `db:"code"`
	Name            string `db:"name"`
	Kind            string `db:"kind"`
	Lifecycle       string `db:"lifecycle"`
	EnvironmentCode string `db:"environment_code"`
	Criticality     int    `db:"criticality"`

	Assets []string `db:"-"`
}

// APIAddressRow is the published view of an address and the asset holding it.
//
// AssetID and AssetName are pointers because the DTO publishes them as
// nullable, not because this query can return a row without one: an address
// reaches an asset only through an interface, and one that reaches no asset
// reaches no environment and is therefore visible to nobody.
type APIAddressRow struct {
	ID         string  `db:"id"`
	AddrText   string  `db:"addr_text"`
	AddrFamily int     `db:"addr_family"`
	AssetID    *string `db:"asset_id"`
	AssetName  *string `db:"asset_name"`

	Environments []string `db:"-"`
}

// APIAssetFilter is the API's own filter. Separate from AssetFilter because the
// UI's list is ordered by name for a human reading it and this one must be
// ordered by id, which is what makes the keyset cursor work.
type APIAssetFilter struct {
	Scope domain.EnvironmentScope
	// After is the id of the last row of the previous page. A well-formed but
	// nonexistent id is not an error: it simply selects the rows after it,
	// which for an id past the end of the estate is none.
	After string
	Limit int
	// Kind and EnvironmentCode narrow WITHIN the scope and can never widen it:
	// the scope predicate is ANDed in first and these select a subset of what
	// survived it.
	Kind            string
	EnvironmentCode string
	// Lifecycle names the one lifecycle to return. Empty is not "all": it is
	// the default exclusion of retired assets.
	//
	// ONE CONTROL, not a filter plus a boolean. `?lifecycle=retired` returns
	// only retired assets, exactly as `?kind=vm` returns only VMs -- a filter
	// names a value. A separate IncludeRetired flag could express "retired and
	// also everything else", a combination the contract does not have and which
	// two callers would read two ways.
	Lifecycle string
}

// ---------------------------------------------------------------------------
// Assets
// ---------------------------------------------------------------------------

// apiAssetSelect is the projection shared by the list and the single fetch, so
// the two cannot drift into publishing different fields.
//
// Site and rack come from asset_closure, which is how every containment
// question in this codebase is answered. The ORDER BY depth picks the nearest
// ancestor of that kind; there is only ever one, and asking for the nearest
// costs nothing and removes the question.
//
// PLACEMENT LABELS ARE PUBLISHED UNSCOPED, AND THAT IS THE CONTRACT RATHER
// THAN AN OVERSIGHT. Neither subquery carries the scope predicate, so a {prod}
// token reads Site == "dc-1" for an asset whose site row it could not fetch by
// id -- APIGetAsset would answer that same id with ErrOutOfScope. Deliberate,
// and decided rather than inherited:
//
//   - docs/api-design.md §4 publishes `site` and `rack` in the asset DTO and
//     `invctl_site` in the Ansible view. They are part of the surface.
//   - Only a NAME crosses. No id, no addresses, no lifecycle, nothing that can
//     be used to enumerate or reach the building.
//   - Scoping them would gut the field rather than protect it. A site or a rack
//     is typically in `shared` or in no environment at all, and under the rule
//     that an entity in no environment is visible to nobody, placement would
//     come back null for essentially every reader -- destroying the feature to
//     hide the name of a building.
//
// This is NOT the cross-entity rule applied loosely. That rule (see
// decorateAPIAssets and decorateAPIServices) exists because a service or a host
// is a first-class entity whose disclosure the token was never granted; a
// placement label is an attribute of an asset the token IS allowed to read.
// The next reader will see the scope predicate carefully applied a few lines
// below and should not conclude that its absence here was forgotten.
const apiAssetSelect = `
	SELECT a.id, a.kind, a.name, a.lifecycle,
	       (SELECT sa.name FROM asset_closure ac
	          JOIN asset sa ON sa.id = ac.ancestor_id
	         WHERE ac.descendant_id = a.id AND sa.kind = 'site'
	         ORDER BY ac.depth LIMIT 1) AS site,
	       (SELECT ra.name FROM asset_closure ac
	          JOIN asset ra ON ra.id = ac.ancestor_id
	         WHERE ac.descendant_id = a.id AND ra.kind = 'rack'
	         ORDER BY ac.depth LIMIT 1) AS rack,
	       a.manager_role AS role
	FROM asset a
	WHERE `

// APIListAssets returns one page of the assets visible to a token, ascending by
// id.
//
// Keyset on id alone. IDs are UUIDv7 and therefore time-sortable as text, so a
// single column gives creation order with no tiebreak -- unlike ChangeCursor,
// which needs {At, ID} because change_log is ordered by a timestamp that can
// repeat. The dependency on UUIDv7 is pinned by TestPageOrderDependsOnUUIDv7,
// because a future non-v7 id would break page ordering silently.
func (s *SQLStore) APIListAssets(ctx context.Context, f APIAssetFilter) ([]APIAssetRow, error) {
	if f.Scope.IsEmpty() {
		return nil, fmt.Errorf("listing assets for the api: %w", errEmptyScope)
	}

	scopeSQL, args := assetScopeSQL("a", f.Scope)
	where := []string{scopeSQL}

	if f.After != "" {
		where = append(where, `a.id > ?`)
		args = append(args, f.After)
	}
	if f.Kind != "" {
		where = append(where, `a.kind = ?`)
		args = append(args, f.Kind)
	}
	if f.EnvironmentCode != "" {
		where = append(where, `EXISTS (
			SELECT 1 FROM asset_environment ae3
			JOIN environment e3 ON e3.id = ae3.environment_id
			WHERE ae3.asset_id = a.id AND e3.code = ?)`)
		args = append(args, f.EnvironmentCode)
	}
	if f.Lifecycle != "" {
		where = append(where, `a.lifecycle = ?`)
		args = append(args, f.Lifecycle)
	} else {
		// Soft delete means the row is still there. An inventory that silently
		// targets decommissioned hosts is worse than one that omits them.
		where = append(where, `a.lifecycle <> ?`)
		args = append(args, domain.LifecycleRetired)
	}

	query := apiAssetSelect + strings.Join(where, " AND ") + ` ORDER BY a.id LIMIT ?`
	args = append(args, apiLimit(f.Limit))

	var rows []APIAssetRow
	if err := s.read(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("listing assets for the api: %w", err)
	}
	if err := s.decorateAPIAssets(ctx, f.Scope, rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// APIGetAsset loads one asset if the token may see it.
//
// It returns ErrOutOfScope when the row exists but fails the scope predicate
// and a plain domain.ErrNotFound when there is no such row. Both wrap
// domain.ErrNotFound and the handler renders an identical 404 for each; the
// difference exists only so that a scope miss can be logged on the operator's
// side.
//
// No lifecycle filter. This route resolves an id a metric already carries, and
// answering "that host was retired last month" is the useful answer during the
// incident that made somebody look it up.
func (s *SQLStore) APIGetAsset(ctx context.Context, scope domain.EnvironmentScope, id string) (*APIAssetRow, error) {
	if scope.IsEmpty() {
		return nil, fmt.Errorf("getting asset %s for the api: %w", id, ErrOutOfScope)
	}
	scopeSQL, args := assetScopeSQL("a", scope)
	args = append(args, id)

	var row APIAssetRow
	err := s.readOne(ctx, &row, apiAssetSelect+scopeSQL+` AND a.id = ?`, args...)
	if err != nil {
		return nil, s.explainAPIMiss(ctx, "asset", id, err)
	}
	rows := []APIAssetRow{row}
	if err := s.decorateAPIAssets(ctx, scope, rows); err != nil {
		return nil, err
	}
	return &rows[0], nil
}

// explainAPIMiss turns "the scoped query found nothing" into the narrower of
// the two sentinels when the row does in fact exist.
//
// The second query runs ONLY on a miss, and its result never reaches the
// client: same status, same body either way.
func (s *SQLStore) explainAPIMiss(ctx context.Context, table, id string, cause error) error {
	if !errors.Is(cause, domain.ErrNotFound) {
		return fmt.Errorf("getting %s %s for the api: %w", table, id, cause)
	}
	var found string
	// table is a compile-time constant at every call site, never a request
	// value -- there is no interpolation of anything a caller supplied.
	err := s.readOne(ctx, &found, `SELECT id FROM `+table+` WHERE id = ?`, id)
	switch {
	case err == nil:
		return fmt.Errorf("getting %s %s for the api: %w", table, id, ErrOutOfScope)
	case errors.Is(err, domain.ErrNotFound):
		return fmt.Errorf("getting %s %s for the api: %w", table, id, domain.ErrNotFound)
	default:
		return fmt.Errorf("checking whether %s %s exists: %w", table, id, err)
	}
}

// decorateAPIAssets resolves a page's environments, addresses and services in
// three queries, not three per row.
func (s *SQLStore) decorateAPIAssets(ctx context.Context, scope domain.EnvironmentScope, rows []APIAssetRow) error {
	if len(rows) == 0 {
		return nil
	}
	ids := make([]string, len(rows))
	index := make(map[string]int, len(rows))
	for i := range rows {
		rows[i].Environments = []string{}
		rows[i].Addresses = []string{}
		rows[i].Services = []string{}
		ids[i] = rows[i].ID
		index[rows[i].ID] = i
	}

	// Environments. No scope filter needed: every environment of an asset that
	// survived the predicate is inside the scope by construction, which is what
	// AllowsAll means.
	type envMember struct {
		AssetID string `db:"asset_id"`
		Code    string `db:"code"`
	}
	var members []envMember
	for _, chunk := range chunkIDs(ids) {
		var part []envMember
		err := s.read(ctx, &part, `
			SELECT ae.asset_id, e.code
			FROM asset_environment ae
			JOIN environment e ON e.id = ae.environment_id
			WHERE ae.asset_id IN (`+placeholders(len(chunk))+`)`, anySlice(chunk)...)
		if err != nil {
			return fmt.Errorf("resolving api asset environments: %w", err)
		}
		members = append(members, part...)
	}
	for _, m := range members {
		if i, ok := index[m.AssetID]; ok {
			rows[i].Environments = append(rows[i].Environments, m.Code)
		}
	}

	// Addresses.
	type assetAddr struct {
		AssetID  string `db:"asset_id"`
		AddrText string `db:"addr_text"`
	}
	var addrs []assetAddr
	for _, chunk := range chunkIDs(ids) {
		var part []assetAddr
		err := s.read(ctx, &part, `
			SELECT i.asset_id, ip.addr_text
			FROM ip_address ip
			JOIN interface i ON i.id = ip.interface_id
			WHERE i.asset_id IN (`+placeholders(len(chunk))+`)`, anySlice(chunk)...)
		if err != nil {
			return fmt.Errorf("resolving api asset addresses: %w", err)
		}
		addrs = append(addrs, part...)
	}
	for _, a := range addrs {
		if i, ok := index[a.AssetID]; ok {
			rows[i].Addresses = append(rows[i].Addresses, a.AddrText)
		}
	}

	// Services, through their placements.
	//
	// SCOPE-FILTERED AGAIN, and deliberately. A visible asset's environments are
	// all in scope, but a service placed on it carries its OWN environment_id,
	// which the schema does not require to be one of them -- so an out-of-scope
	// service code could reach a client by way of an in-scope host. The extra
	// predicate costs one subquery and closes that route.
	type hostedService struct {
		AssetID string `db:"host_asset_id"`
		Code    string `db:"code"`
	}
	svcScope, scopeArgs := serviceScopeSQL("sv", scope)
	var hosted []hostedService
	for _, chunk := range chunkIDs(ids) {
		args := append(anySlice(chunk), scopeArgs...)
		args = append(args, domain.LifecycleRetired, domain.LifecycleRetired)
		var part []hostedService
		err := s.read(ctx, &part, `
			SELECT si.host_asset_id, sv.code
			FROM service_instance si
			JOIN service sv ON sv.id = si.service_id
			WHERE si.host_asset_id IN (`+placeholders(len(chunk))+`)
			  AND `+svcScope+`
			  AND si.lifecycle <> ?
			  AND sv.lifecycle <> ?`, args...)
		if err != nil {
			return fmt.Errorf("resolving api asset services: %w", err)
		}
		hosted = append(hosted, part...)
	}
	for _, h := range hosted {
		if i, ok := index[h.AssetID]; ok {
			rows[i].Services = append(rows[i].Services, h.Code)
		}
	}

	for i := range rows {
		// All three go through sortedUnique. An asset can carry the same
		// addr_text on two interfaces, and a repeat in a published list reads
		// as two facts where there is one.
		rows[i].Environments = sortedUnique(rows[i].Environments)
		rows[i].Addresses = sortedUnique(rows[i].Addresses)
		rows[i].Services = sortedUnique(rows[i].Services)
	}
	return nil
}

// sortedUnique collapses repeats. A service with three replicas on one host is
// one service on that host, not three.
func sortedUnique(in []string) []string {
	if len(in) == 0 {
		return in
	}
	sort.Strings(in)
	out := in[:1]
	for _, v := range in[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Services
// ---------------------------------------------------------------------------

const apiServiceSelect = `
	SELECT sv.id, sv.code, sv.name, sv.kind, sv.lifecycle,
	       sv.tier AS criticality,
	       (SELECT e.code FROM environment e WHERE e.id = sv.environment_id) AS environment_code
	FROM service sv
	WHERE `

// APIListServices returns one page of the services visible to a token,
// ascending by id.
func (s *SQLStore) APIListServices(ctx context.Context, scope domain.EnvironmentScope,
	after string, limit int) ([]APIServiceRow, error) {

	if scope.IsEmpty() {
		return nil, fmt.Errorf("listing services for the api: %w", errEmptyScope)
	}
	scopeSQL, args := serviceScopeSQL("sv", scope)
	where := []string{scopeSQL}
	if after != "" {
		where = append(where, `sv.id > ?`)
		args = append(args, after)
	}
	where = append(where, `sv.lifecycle <> ?`)
	args = append(args, domain.LifecycleRetired)

	query := apiServiceSelect + strings.Join(where, " AND ") + ` ORDER BY sv.id LIMIT ?`
	args = append(args, apiLimit(limit))

	var rows []APIServiceRow
	if err := s.read(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("listing services for the api: %w", err)
	}
	if err := s.decorateAPIServices(ctx, scope, rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// APIGetService loads one service if the token may see it, with the same
// two-sentinel behaviour as APIGetAsset.
func (s *SQLStore) APIGetService(ctx context.Context, scope domain.EnvironmentScope, id string) (*APIServiceRow, error) {
	if scope.IsEmpty() {
		return nil, fmt.Errorf("getting service %s for the api: %w", id, ErrOutOfScope)
	}
	scopeSQL, args := serviceScopeSQL("sv", scope)
	args = append(args, id)

	var row APIServiceRow
	if err := s.readOne(ctx, &row, apiServiceSelect+scopeSQL+` AND sv.id = ?`, args...); err != nil {
		return nil, s.explainAPIMiss(ctx, "service", id, err)
	}
	rows := []APIServiceRow{row}
	if err := s.decorateAPIServices(ctx, scope, rows); err != nil {
		return nil, err
	}
	return &rows[0], nil
}

// decorateAPIServices attaches the hosts a service runs on, in one query.
//
// The asset scope predicate is applied to the hosts as well as the service:
// an in-scope service can legitimately be placed on a host whose environment
// set is wider than the token's, and naming that host would disclose the
// boundary device by another route.
func (s *SQLStore) decorateAPIServices(ctx context.Context, scope domain.EnvironmentScope, rows []APIServiceRow) error {
	if len(rows) == 0 {
		return nil
	}
	ids := make([]string, len(rows))
	index := make(map[string]int, len(rows))
	for i := range rows {
		rows[i].Assets = []string{}
		ids[i] = rows[i].ID
		index[rows[i].ID] = i
	}

	type placement struct {
		ServiceID string `db:"service_id"`
		Name      string `db:"name"`
	}
	assetScope, scopeArgs := assetScopeSQL("a", scope)
	var placements []placement
	for _, chunk := range chunkIDs(ids) {
		args := append(anySlice(chunk), scopeArgs...)
		args = append(args, domain.LifecycleRetired)
		var part []placement
		err := s.read(ctx, &part, `
			SELECT si.service_id, a.name
			FROM service_instance si
			JOIN asset a ON a.id = si.host_asset_id
			WHERE si.service_id IN (`+placeholders(len(chunk))+`)
			  AND `+assetScope+`
			  AND si.lifecycle <> ?`, args...)
		if err != nil {
			return fmt.Errorf("resolving api service placements: %w", err)
		}
		placements = append(placements, part...)
	}
	for _, p := range placements {
		if i, ok := index[p.ServiceID]; ok {
			rows[i].Assets = append(rows[i].Assets, p.Name)
		}
	}
	for i := range rows {
		rows[i].Assets = sortedUnique(rows[i].Assets)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Addresses
// ---------------------------------------------------------------------------

// APIListAddresses returns one page of the addresses visible to a token,
// ascending by address id.
//
// An address is scoped by the environments of the asset holding it, reached
// through its interface. Both joins are INNER, which is the whole rule: an FHRP
// virtual address carries fhrp_group_id and no interface_id, so it reaches no
// asset, therefore no environment, and is visible to nobody -- the same denial
// an asset in no environment gets, for the same reason.
//
// An address on a RETIRED asset is excluded, matching /services and the default
// of /assets. Consistency across the collections is itself a contract property,
// and the address of a decommissioned host is worse than absent in an Ansible
// or observability join: it may since have been reassigned to something else,
// so publishing it points a consumer at the wrong machine.
func (s *SQLStore) APIListAddresses(ctx context.Context, scope domain.EnvironmentScope,
	after string, limit int) ([]APIAddressRow, error) {

	if scope.IsEmpty() {
		return nil, fmt.Errorf("listing addresses for the api: %w", errEmptyScope)
	}
	scopeSQL, args := assetScopeSQL("a", scope)
	where := []string{scopeSQL}
	if after != "" {
		where = append(where, `ip.id > ?`)
		args = append(args, after)
	}
	where = append(where, `a.lifecycle <> ?`)
	args = append(args, domain.LifecycleRetired)
	query := `
		SELECT ip.id, ip.addr_text, ip.addr_family,
		       a.id AS asset_id, a.name AS asset_name
		FROM ip_address ip
		JOIN interface i ON i.id = ip.interface_id
		JOIN asset a ON a.id = i.asset_id
		WHERE ` + strings.Join(where, " AND ") + ` ORDER BY ip.id LIMIT ?`
	args = append(args, apiLimit(limit))

	var rows []APIAddressRow
	if err := s.read(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("listing addresses for the api: %w", err)
	}
	if err := s.decorateAPIAddresses(ctx, rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// decorateAPIAddresses attaches each address's inherited environments in one
// query keyed by the page's asset ids.
func (s *SQLStore) decorateAPIAddresses(ctx context.Context, rows []APIAddressRow) error {
	if len(rows) == 0 {
		return nil
	}
	ids := make([]string, 0, len(rows))
	seen := make(map[string]bool, len(rows))
	for i := range rows {
		rows[i].Environments = []string{}
		if rows[i].AssetID == nil {
			continue
		}
		if !seen[*rows[i].AssetID] {
			seen[*rows[i].AssetID] = true
			ids = append(ids, *rows[i].AssetID)
		}
	}
	if len(ids) == 0 {
		return nil
	}

	type envMember struct {
		AssetID string `db:"asset_id"`
		Code    string `db:"code"`
	}
	byAsset := make(map[string][]string, len(ids))
	for _, chunk := range chunkIDs(ids) {
		var part []envMember
		err := s.read(ctx, &part, `
			SELECT ae.asset_id, e.code
			FROM asset_environment ae
			JOIN environment e ON e.id = ae.environment_id
			WHERE ae.asset_id IN (`+placeholders(len(chunk))+`)`, anySlice(chunk)...)
		if err != nil {
			return fmt.Errorf("resolving api address environments: %w", err)
		}
		for _, m := range part {
			byAsset[m.AssetID] = append(byAsset[m.AssetID], m.Code)
		}
	}
	for k := range byAsset {
		byAsset[k] = sortedUnique(byAsset[k])
	}
	for i := range rows {
		if rows[i].AssetID == nil {
			continue
		}
		// A COPY PER ROW. Several addresses commonly sit on one asset, and
		// handing them all the same backing slice means one append by a future
		// DTO mapper silently rewrites its siblings.
		shared := byAsset[*rows[i].AssetID]
		rows[i].Environments = append(make([]string, 0, len(shared)), shared...)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Environments
// ---------------------------------------------------------------------------

// APIListEnvironments returns the environments inside a token's scope.
//
// Unpaginated: the set is bounded and small, and it is the vocabulary a
// consumer needs before it can use the ?env= filter at all. A scope naming an
// environment that does not exist yields nothing for that code rather than an
// error -- the scope is a statement about what a credential may see, not a
// claim that the estate has been built yet.
func (s *SQLStore) APIListEnvironments(ctx context.Context, scope domain.EnvironmentScope) ([]domain.Environment, error) {
	if scope.IsEmpty() {
		return nil, fmt.Errorf("listing environments for the api: %w", errEmptyScope)
	}
	codes := scope.Codes()
	var envs []domain.Environment
	err := s.read(ctx, &envs,
		`SELECT * FROM environment WHERE code IN (`+placeholders(len(codes))+`) ORDER BY code`,
		anySlice(codes)...)
	if err != nil {
		return nil, fmt.Errorf("listing environments for the api: %w", err)
	}
	return envs, nil
}
