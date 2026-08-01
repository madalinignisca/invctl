package store

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/gabriel/invctl/internal/domain"
)

// ---------- endpoints ----------

// EndpointRow is an endpoint plus the service that owns it.
type EndpointRow struct {
	domain.Endpoint
	ServiceCode string `db:"service_code"`
	ServiceName string `db:"service_name"`
	AddrText    string `db:"addr_text"`
}

const endpointSelect = `
	SELECT e.*, s.code AS service_code, s.name AS service_name,
	       COALESCE(ip.addr_text, '') AS addr_text
	FROM endpoint e
	JOIN service s ON s.id = e.service_id
	LEFT JOIN ip_address ip ON ip.id = e.ip_address_id`

// ListEndpointsByService returns every listening socket of a service.
func (s *SQLStore) ListEndpointsByService(ctx context.Context, serviceID string) ([]EndpointRow, error) {
	var rows []EndpointRow
	if err := s.read(ctx, &rows, endpointSelect+` WHERE e.service_id = ? ORDER BY e.name`, serviceID); err != nil {
		return nil, fmt.Errorf("listing endpoints of service %s: %w", serviceID, err)
	}
	return rows, nil
}

// ListAllEndpoints returns every LIVE endpoint, for the dependency picker.
//
// Filtered, per the read audit in migration 00020: a withdrawn socket is not
// something a new dependency can start resolving through.
func (s *SQLStore) ListAllEndpoints(ctx context.Context) ([]EndpointRow, error) {
	var rows []EndpointRow
	if err := s.read(ctx, &rows, endpointSelect+`
		WHERE e.lifecycle = ? ORDER BY s.code, e.name`, domain.LifecycleActive); err != nil {
		return nil, fmt.Errorf("listing endpoints: %w", err)
	}
	return rows, nil
}

// GetEndpoint loads one endpoint.
func (s *SQLStore) GetEndpoint(ctx context.Context, id string) (*EndpointRow, error) {
	var row EndpointRow
	if err := s.readOne(ctx, &row, endpointSelect+` WHERE e.id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting endpoint %s: %w", id, err)
	}
	return &row, nil
}

// CreateEndpoint inserts a listening socket.
func (s *SQLStore) CreateEndpoint(ctx context.Context, actor domain.Actor, e *domain.Endpoint) error {
	// The row the INSERT just wrote is version 1 (the column default).
	// Without this a caller that creates and then updates the SAME struct
	// compares 0 against 1 and gets a conflict against itself.
	e.RowVersion = 1
	if err := e.Validate(); err != nil {
		return err
	}
	at := domain.FormatTime(s.now())
	e.CreatedAt, e.UpdatedAt = &at, &at
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO endpoint (id, service_id, name, l4_proto, port, unix_path, bind_scope,
			                      ip_address_id, l7_proto, tls_mode, certificate_id, exposure,
			                      lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, e.ServiceID, e.Name, e.L4Proto, e.Port, e.UnixPath, e.BindScope,
			e.IPAddressID, e.L7Proto, e.TLSMode, e.CertificateID, e.Exposure,
			e.Lifecycle, e.CreatedAt, e.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating endpoint")
		}
		return t.logCreate(ctx, "endpoint", e.ID, e)
	})
}

// UpdateEndpoint persists field changes.
func (s *SQLStore) UpdateEndpoint(ctx context.Context, actor domain.Actor, e *domain.Endpoint) error {
	if err := e.Validate(); err != nil {
		return err
	}
	before, err := s.GetEndpoint(ctx, e.ID)
	if err != nil {
		return err
	}
	// A RETIRED SERVICE'S SHAPE IS HISTORY. An endpoint has no lifecycle of its
	// own -- it exists because its service declares it -- so the fact that
	// makes it unamendable belongs to the service. Rewriting the port a
	// withdrawn service used to listen on changes what the record says it was,
	// and somebody reading an old incident is the only person who will ever
	// look. Correct it the way change_log is corrected: by adding the right
	// thing, not by editing the wrong one.
	//
	// The same rule the cost line and the placement follow, reached one level
	// up because that is where the lifecycle lives.
	svc, err := s.GetService(ctx, before.ServiceID)
	if err != nil {
		return fmt.Errorf("checking the service of endpoint %s: %w", e.ID, err)
	}
	if svc.Lifecycle == domain.LifecycleRetired {
		return fmt.Errorf("endpoint %s belongs to a retired service and cannot be amended: %w",
			e.ID, domain.ErrConflict)
	}
	// And a socket withdrawn in its own right is history, the same as a
	// retired cost line: correct it by declaring the right one.
	if before.Retired() {
		return fmt.Errorf("endpoint %s is withdrawn and cannot be amended: %w",
			e.ID, domain.ErrConflict)
	}
	at := domain.FormatTime(s.now())
	e.UpdatedAt = &at
	return s.write(ctx, actor, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE endpoint SET name = ?, l4_proto = ?, port = ?, unix_path = ?, bind_scope = ?,
			                    ip_address_id = ?, l7_proto = ?, tls_mode = ?, certificate_id = ?,
			                    exposure = ?, lifecycle = ?, updated_at = ?,
			                    row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			e.Name, e.L4Proto, e.Port, e.UnixPath, e.BindScope,
			e.IPAddressID, e.L7Proto, e.TLSMode, e.CertificateID, e.Exposure,
			e.Lifecycle, at, e.ID, e.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating endpoint")
		}
		if err := requireVersion(res, "endpoint", e.ID, &e.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "endpoint", e.ID, &before.Endpoint, e)
	})
}

// requireLiveProvider refuses an edge that resolves through a withdrawn socket.
//
// THE MIRROR OF RetireEndpoint, and without it that guard only holds in one
// direction: you cannot retire a socket with live dependants, but nothing
// stopped you declaring a dependant on an already-retired socket afterwards.
// Two ordinary admin requests in the wrong order, no race required -- and the
// result is the failure RetireEndpoint's own comment forbids. LoadGraph drops
// retired sockets, the impact engine skips any dependency whose provider is
// missing from the map, and the edge stays on the service page (dependencySelect
// is deliberately unfiltered) while quietly vanishing from every simulation.
//
// A route is checked through its frontend for the same reason the retire guard
// counts routes: the socket a route fronts is the socket the traffic reaches.
func requireLiveProvider(ctx context.Context, t *tx, endpointID, routeID *string) error {
	var live int
	err := t.get(ctx, &live, `
		SELECT COUNT(*) FROM endpoint e
		WHERE e.lifecycle = ?
		  AND (e.id = ?
		    OR e.id = (SELECT r.frontend_endpoint_id FROM route r WHERE r.id = ?))`,
		domain.LifecycleActive, derefString(endpointID), derefString(routeID))
	if err != nil {
		return fmt.Errorf("checking the provider socket: %w", err)
	}
	if live == 0 {
		return fmt.Errorf("the provider socket is withdrawn: %w", domain.ErrConflict)
	}
	return nil
}

// RetireEndpoint withdraws a listening socket.
//
// REFUSED WHILE ANYTHING LIVE STILL RESOLVES THROUGH IT. A dependency naming
// this endpoint, or a route fronting it, describes traffic somebody believes is
// flowing; withdrawing the socket under them would leave an active edge
// pointing at a thing declared not to exist, and the impact engine -- which
// now skips retired sockets -- would quietly stop propagating an outage along
// an edge that is still on screen. Retire the edge first, which is a decision
// somebody has to make rather than one this method can make for them.
//
// Serializable, for the reason CreateLink is: the COUNT below asserts an
// invariant this transaction is about to depend on, and at read-committed a
// dependency created concurrently is invisible to it.
func (s *SQLStore) RetireEndpoint(ctx context.Context, actor domain.Actor, id string) error {
	before, err := s.GetEndpoint(ctx, id)
	if err != nil {
		return err
	}
	if before.Retired() {
		// Already withdrawn: nothing to record, and a second audit entry
		// would claim a withdrawal that did not happen.
		return nil
	}
	at := domain.FormatTime(s.now())

	return s.writeSerializable(ctx, actor, func(t *tx) error {
		var live int
		if err := t.get(ctx, &live, `
			SELECT COUNT(*) FROM dependency d
			LEFT JOIN route r ON r.id = d.provider_route_id
			WHERE d.lifecycle = ?
			  AND (d.provider_endpoint_id = ? OR r.frontend_endpoint_id = ?)`,
			domain.LifecycleActive, id, id); err != nil {
			return fmt.Errorf("checking what depends on endpoint %s: %w", id, err)
		}
		if live > 0 {
			return fmt.Errorf("endpoint %s still has %d live %s resolving through it: %w",
				id, live, pluralDependencies(live), domain.ErrConflict)
		}

		// AND POOL MEMBERSHIP, which the first version of this guard missed.
		// A socket can be reached without being named by any dependency: as a
		// backend behind a route. LoadGraph keeps the membership row whose
		// endpoint has vanished from the map and the engine skips it silently,
		// so withdrawing a member shrinks a pool's capacity -- possibly to
		// zero -- with nothing anywhere marked down. Found by a security
		// review; the read audit in migration 00020 never mentioned
		// backend_member at all, which by its own standard reads as forgotten
		// rather than decided.
		//
		// Refused rather than cascaded: removing a backend from a pool is a
		// capacity decision, and this method cannot tell whether the pool has
		// anything left to serve with.
		var pools int
		if err := t.get(ctx, &pools,
			`SELECT COUNT(*) FROM backend_member WHERE endpoint_id = ?`, id); err != nil {
			return fmt.Errorf("checking pool membership of endpoint %s: %w", id, err)
		}
		if pools > 0 {
			return fmt.Errorf("endpoint %s is still a backend in %d pool(s): %w",
				id, pools, domain.ErrConflict)
		}

		res, err := t.exec(ctx, `
			UPDATE endpoint SET lifecycle = ?, updated_at = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			domain.LifecycleRetired, at, id, before.RowVersion)
		if err != nil {
			return translateWriteErr(err, "retiring endpoint")
		}
		version := before.RowVersion
		if err := requireVersion(res, "endpoint", id, &version); err != nil {
			return err
		}
		diff := fmt.Sprintf(`{"lifecycle":{"old":%q,"new":%q}}`,
			before.Lifecycle, domain.LifecycleRetired)
		return t.log(ctx, "endpoint", id, domain.ActionRetire, diff)
	})
}

func pluralDependencies(n int) string {
	if n == 1 {
		return "dependency"
	}
	return "dependencies"
}

// ---------- pools and routes ----------

// CreateBackendPool inserts a pool.
func (s *SQLStore) CreateBackendPool(ctx context.Context, actor domain.Actor, p *domain.BackendPool) error {
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx,
			`INSERT INTO backend_pool (id, service_id, name, lb_algorithm) VALUES (?, ?, ?, ?)`,
			p.ID, p.ServiceID, p.Name, p.LBAlgorithm)
		if err != nil {
			return translateWriteErr(err, "creating backend pool")
		}
		return t.logCreate(ctx, "backend_pool", p.ID, p)
	})
}

// AddBackendMember puts an endpoint into a pool.
func (s *SQLStore) AddBackendMember(ctx context.Context, actor domain.Actor, m *domain.BackendMember) error {
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx,
			`INSERT INTO backend_member (pool_id, endpoint_id, weight, is_backup) VALUES (?, ?, ?, ?)`,
			m.PoolID, m.EndpointID, m.Weight, m.IsBackup)
		if err != nil {
			return translateWriteErr(err, "adding backend member")
		}
		return t.logCreate(ctx, "backend_member", m.PoolID+"/"+m.EndpointID, m)
	})
}

// CreateRoute inserts an L7 routing rule.
func (s *SQLStore) CreateRoute(ctx context.Context, actor domain.Actor, r *domain.Route) error {
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO route (id, frontend_endpoint_id, match_type, match_value,
			                   backend_pool_id, tls_termination, priority)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.FrontendEndpointID, r.MatchType, r.MatchValue,
			r.BackendPoolID, r.TLSTermination, r.Priority)
		if err != nil {
			return translateWriteErr(err, "creating route")
		}
		return t.logCreate(ctx, "route", r.ID, r)
	})
}

// RouteRow is a route with the names needed to render it.
type RouteRow struct {
	domain.Route
	FrontendService string `db:"frontend_service"`
	FrontendName    string `db:"frontend_name"`
	PoolName        string `db:"pool_name"`
	PoolService     string `db:"pool_service"`
	MemberCount     int    `db:"member_count"`
}

const routeSelect = `
	SELECT r.*,
	       fs.code AS frontend_service, fe.name AS frontend_name,
	       bp.name AS pool_name, ps.code AS pool_service,
	       (SELECT COUNT(*) FROM backend_member bm WHERE bm.pool_id = r.backend_pool_id) AS member_count
	FROM route r
	JOIN endpoint fe ON fe.id = r.frontend_endpoint_id
	JOIN service fs ON fs.id = fe.service_id
	JOIN backend_pool bp ON bp.id = r.backend_pool_id
	JOIN service ps ON ps.id = bp.service_id`

// ListRoutesByService returns the routes whose frontend belongs to a service.
func (s *SQLStore) ListRoutesByService(ctx context.Context, serviceID string) ([]RouteRow, error) {
	var rows []RouteRow
	err := s.read(ctx, &rows, routeSelect+` WHERE fe.service_id = ? ORDER BY r.priority, r.match_value`, serviceID)
	if err != nil {
		return nil, fmt.Errorf("listing routes of service %s: %w", serviceID, err)
	}
	return rows, nil
}

// ListAllRoutes returns every route, for the dependency picker.
func (s *SQLStore) ListAllRoutes(ctx context.Context) ([]RouteRow, error) {
	var rows []RouteRow
	if err := s.read(ctx, &rows, routeSelect+` ORDER BY fs.code, r.priority`); err != nil {
		return nil, fmt.Errorf("listing routes: %w", err)
	}
	return rows, nil
}

// ---------- dependencies ----------

// dependencyAudit is the audited shape of a dependency: the row itself plus the
// data classes it carries.
//
// Those classes live in a child table, so a change to them produced no diff on
// the dependency struct and therefore no change_log entry at all -- an edge
// could acquire `chd` in complete silence, which is the single most
// scope-relevant event this system records. Folding them into one audited value
// keeps the edge and what it carries in a single entry, which is also how an
// auditor wants to read it.
type dependencyAudit struct {
	domain.Dependency
	// Sorted and joined so that re-submitting the same set in a different
	// order is not reported as a change.
	DataClasses string `db:"data_classes"`
}

func auditedDependency(d *domain.Dependency, classes []string) *dependencyAudit {
	sorted := append([]string(nil), classes...)
	sort.Strings(sorted)
	return &dependencyAudit{Dependency: *d, DataClasses: strings.Join(sorted, ",")}
}

// DependencyRow is a dependency with both ends resolved to names, which is all
// the two panels on the service page need.
type DependencyRow struct {
	domain.Dependency
	ConsumerCode string  `db:"consumer_code"`
	ConsumerName string  `db:"consumer_name"`
	ProviderKind string  `db:"provider_kind"` // "endpoint" or "route"
	ProviderName string  `db:"provider_name"`
	ProviderPort *int    `db:"provider_port"`
	ProviderSvc  string  `db:"provider_service_id"`
	ProviderCode string  `db:"provider_code"`
	IdentityName *string `db:"identity_name"`
	// VerifiedByName resolves the opaque id in verified_by for display. It is
	// nil when the account was scrubbed or never existed, in which case the UI
	// falls back to saying only that somebody verified it.
	VerifiedByName *string `db:"verified_by_name"`
}

// VerifiedByDisplay names whoever attested to this edge, degrading gracefully
// when the id no longer resolves.
func (d *DependencyRow) VerifiedByDisplay() string {
	if d.VerifiedByName != nil && *d.VerifiedByName != "" {
		return *d.VerifiedByName
	}
	if d.VerifiedBy != nil && *d.VerifiedBy != "" {
		return "a since-removed account"
	}
	return "unknown"
}

// NatureDescription exposes the failure semantics to templates.
func (d *DependencyRow) NatureDescription() string { return domain.NatureDescription(d.Nature) }

// A dependency's provider is either an endpoint or a route, and the route case
// has to resolve through the pool to name the service actually providing the
// capability. Doing it in one query with two LEFT JOINs keeps the two
// dependency panels to a single round trip each.
const dependencySelect = `
	SELECT d.*,
	       cs.code AS consumer_code,
	       cs.name AS consumer_name,
	       CASE WHEN d.provider_endpoint_id IS NOT NULL THEN 'endpoint' ELSE 'route' END AS provider_kind,
	       COALESCE(pe.name, re.name, '') AS provider_name,
	       COALESCE(pe.port, re.port) AS provider_port,
	       COALESCE(pes.id, res.id, '') AS provider_service_id,
	       COALESCE(pes.code, res.code, '') AS provider_code,
	       idn.name AS identity_name,
	       COALESCE(vu.display_name, vu.username) AS verified_by_name
	FROM dependency d
	JOIN service cs ON cs.id = d.consumer_service_id
	LEFT JOIN endpoint pe ON pe.id = d.provider_endpoint_id
	LEFT JOIN service pes ON pes.id = pe.service_id
	LEFT JOIN route r ON r.id = d.provider_route_id
	LEFT JOIN endpoint re ON re.id = r.frontend_endpoint_id
	LEFT JOIN service res ON res.id = re.service_id
	LEFT JOIN identity idn ON idn.id = d.identity_id
	LEFT JOIN app_user vu ON vu.id = d.verified_by`

// ListUpstream returns what a service depends on.
func (s *SQLStore) ListUpstream(ctx context.Context, serviceID string) ([]DependencyRow, error) {
	var rows []DependencyRow
	err := s.read(ctx, &rows, dependencySelect+`
		WHERE d.consumer_service_id = ? AND d.lifecycle = 'active'
		ORDER BY d.nature, provider_code`, serviceID)
	if err != nil {
		return nil, fmt.Errorf("listing upstream dependencies of %s: %w", serviceID, err)
	}
	return rows, nil
}

// ListDownstream returns what depends on a service -- through its endpoints
// and through any route fronting them.
func (s *SQLStore) ListDownstream(ctx context.Context, serviceID string) ([]DependencyRow, error) {
	var rows []DependencyRow
	err := s.read(ctx, &rows, dependencySelect+`
		WHERE d.lifecycle = 'active'
		  AND (pe.service_id = ? OR re.service_id = ?)
		ORDER BY d.nature, consumer_code`, serviceID, serviceID)
	if err != nil {
		return nil, fmt.Errorf("listing downstream dependencies of %s: %w", serviceID, err)
	}
	return rows, nil
}

// GetDependency loads one edge.
func (s *SQLStore) GetDependency(ctx context.Context, id string) (*DependencyRow, error) {
	var row DependencyRow
	if err := s.readOne(ctx, &row, dependencySelect+` WHERE d.id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting dependency %s: %w", id, err)
	}
	return &row, nil
}

// CreateDependency inserts an edge and its data classes.
func (s *SQLStore) CreateDependency(ctx context.Context, actor domain.Actor, d *domain.Dependency, dataClasses []string) error {
	// The row the INSERT just wrote is version 1 (the column default).
	// Without this a caller that creates and then updates the SAME struct
	// compares 0 against 1 and gets a conflict against itself.
	d.RowVersion = 1
	if err := d.Validate(); err != nil {
		return err
	}
	// Rule 7, enforced at the entry point rather than by every caller.
	if err := domain.CheckProvenanceWrite(actor, d.Source); err != nil {
		return err
	}
	d.Confidence = domain.ConfidenceFor(actor, d.Confidence)
	// Serializable, as the retire guard is: the check below asserts an
	// invariant this transaction depends on, and at read-committed a
	// concurrent retirement is invisible to it.
	return s.writeSerializable(ctx, actor, func(t *tx) error {
		if err := requireLiveProvider(ctx, t, d.ProviderEndpointID, d.ProviderRouteID); err != nil {
			return err
		}
		_, err := t.exec(ctx, `
			INSERT INTO dependency (id, consumer_service_id, consumer_instance_id,
			                        provider_endpoint_id, provider_route_id, nature,
			                        tolerance_seconds, failure_mode, identity_id, auth_method,
			                        firewall_rule_ref, source, confidence, first_seen, last_seen,
			                        verified_by, verified_at, lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			d.ID, d.ConsumerServiceID, d.ConsumerInstanceID,
			d.ProviderEndpointID, d.ProviderRouteID, d.Nature,
			d.ToleranceSeconds, d.FailureMode, d.IdentityID, d.AuthMethod,
			d.FirewallRuleRef, d.Source, d.Confidence, d.FirstSeen, d.LastSeen,
			d.VerifiedBy, d.VerifiedAt, d.Lifecycle, d.CreatedAt, d.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating dependency")
		}
		if err := setDataClasses(ctx, t, d.ID, dataClasses); err != nil {
			return err
		}
		return t.logCreate(ctx, "dependency", d.ID, auditedDependency(d, dataClasses))
	})
}

// UpdateDependency persists field changes to an edge.
func (s *SQLStore) UpdateDependency(ctx context.Context, actor domain.Actor, d *domain.Dependency, dataClasses []string) error {
	if err := d.Validate(); err != nil {
		return err
	}
	// Rule 7 covers the flip as well as the create: "Flipping an edge between
	// declared and discovered_* is an operator act with a change_log row."
	// Laundering an existing discovered edge is the cheaper attack of the two,
	// because the edge already exists and looks established.
	if err := domain.CheckProvenanceWrite(actor, d.Source); err != nil {
		return err
	}
	d.Confidence = domain.ConfidenceFor(actor, d.Confidence)
	before, err := s.GetDependency(ctx, d.ID)
	if err != nil {
		return err
	}
	// Read the existing classes before the write, so a change to them alone
	// still produces a diff.
	beforeClasses, err := s.DataClassesFor(ctx, []string{d.ID})
	if err != nil {
		return err
	}
	d.CreatedAt = before.CreatedAt
	d.UpdatedAt = domain.FormatTime(s.now())

	return s.writeSerializable(ctx, actor, func(t *tx) error {
		// Re-pointing an edge is declaring it, so it faces the same check. No
		// route reaches this today; that is not a reason to leave the hole.
		if err := requireLiveProvider(ctx, t, d.ProviderEndpointID, d.ProviderRouteID); err != nil {
			return err
		}
		res, err := t.exec(ctx, `
			UPDATE dependency
			SET consumer_instance_id = ?, provider_endpoint_id = ?, provider_route_id = ?,
			    nature = ?, tolerance_seconds = ?, failure_mode = ?, identity_id = ?,
			    auth_method = ?, firewall_rule_ref = ?, source = ?, confidence = ?,
			    verified_by = ?, verified_at = ?, lifecycle = ?, updated_at = ?,
			    row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			d.ConsumerInstanceID, d.ProviderEndpointID, d.ProviderRouteID,
			d.Nature, d.ToleranceSeconds, d.FailureMode, d.IdentityID,
			d.AuthMethod, d.FirewallRuleRef, d.Source, d.Confidence,
			d.VerifiedBy, d.VerifiedAt, d.Lifecycle, d.UpdatedAt, d.ID, d.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating dependency")
		}
		if err := requireVersion(res, "dependency", d.ID, &d.RowVersion); err != nil {
			return err
		}
		if err := setDataClasses(ctx, t, d.ID, dataClasses); err != nil {
			return err
		}
		after := dataClasses
		if after == nil {
			// nil means "not managing classes here", so they are unchanged.
			after = beforeClasses[d.ID]
		}
		return t.logUpdate(ctx, "dependency", d.ID,
			auditedDependency(&before.Dependency, beforeClasses[d.ID]),
			auditedDependency(d, after))
	})
}

// RetireDependency withdraws an edge without losing the record that it once
// existed -- which is exactly what an auditor asks about.
func (s *SQLStore) RetireDependency(ctx context.Context, actor domain.Actor, id string) error {
	before, err := s.GetDependency(ctx, id)
	if err != nil {
		return err
	}
	if before.Lifecycle == domain.LifecycleRetired {
		return nil
	}
	at := domain.FormatTime(s.now())
	return s.write(ctx, actor, func(t *tx) error {
		if _, err := t.exec(ctx,
			`UPDATE dependency SET lifecycle = 'retired', updated_at = ?,
			                       row_version = row_version + 1 WHERE id = ?`, at, id); err != nil {
			return translateWriteErr(err, "retiring dependency")
		}
		diff := fmt.Sprintf(`{"lifecycle":{"old":%q,"new":"retired"}}`, before.Lifecycle)
		return t.log(ctx, "dependency", id, domain.ActionRetire, diff)
	})
}

// VerifyDependency records that a human confirmed the edge is real. Declared
// data is authoritative, but a verification date is what distinguishes
// "someone wrote this down once" from "this is still true".
func (s *SQLStore) VerifyDependency(ctx context.Context, actor domain.Actor, id string) error {
	// verified_by/verified_at are a person putting their name to an edge. A
	// machine signing one off is a rubber stamp on an undocumented chd edge and
	// on the firewall rule justified by it.
	if err := domain.CheckAttestationWrite(actor); err != nil {
		return err
	}
	before, err := s.GetDependency(ctx, id)
	if err != nil {
		return err
	}
	at := domain.FormatTime(s.now())
	return s.write(ctx, actor, func(t *tx) error {
		if _, err := t.exec(ctx,
			`UPDATE dependency SET verified_by = ?, verified_at = ?, updated_at = ?,
			                       row_version = row_version + 1 WHERE id = ?`,
			actor.ID, at, at, id); err != nil {
			return translateWriteErr(err, "verifying dependency")
		}
		after := before.Dependency
		after.VerifiedBy = &actor.ID
		after.VerifiedAt = &at
		return t.logUpdate(ctx, "dependency", id, &before.Dependency, &after)
	})
}

func setDataClasses(ctx context.Context, t *tx, dependencyID string, classes []string) error {
	if classes == nil {
		return nil
	}
	if _, err := t.exec(ctx, `DELETE FROM dependency_data_class WHERE dependency_id = ?`, dependencyID); err != nil {
		return translateWriteErr(err, "clearing data classes")
	}
	for _, c := range classes {
		if c == "" {
			continue
		}
		// The checkbox group reaches here straight from r.Form, so this is the
		// first and only place a submitted class is validated in Go. Before
		// migration 00004 the CHECK constraint was the sole enforcement and the
		// failure was an opaque driver error; now the FK enforces it and this
		// read makes the message name the field.
		if err := t.requireVocabulary(ctx, vocabDataClass, "data_class", c); err != nil {
			return err
		}
		_, err := t.exec(ctx,
			`INSERT INTO dependency_data_class (dependency_id, data_class) VALUES (?, ?)`,
			dependencyID, c)
		if err != nil {
			return translateWriteErr(err, "adding data class")
		}
	}
	return nil
}

// DataClassesFor returns the data classes carried by a set of dependencies.
func (s *SQLStore) DataClassesFor(ctx context.Context, dependencyIDs []string) (map[string][]string, error) {
	if len(dependencyIDs) == 0 {
		return map[string][]string{}, nil
	}
	var rows []struct {
		DependencyID string `db:"dependency_id"`
		DataClass    string `db:"data_class"`
	}
	err := s.read(ctx, &rows,
		`SELECT dependency_id, data_class FROM dependency_data_class
		 WHERE dependency_id IN (`+placeholders(len(dependencyIDs))+`)`, anySlice(dependencyIDs)...)
	if err != nil {
		return nil, fmt.Errorf("loading data classes: %w", err)
	}
	out := make(map[string][]string, len(rows))
	for _, r := range rows {
		out[r.DependencyID] = append(out[r.DependencyID], r.DataClass)
	}
	return out, nil
}

// ---------- identities ----------

// CreateIdentity inserts a principal.
// realmOrEmpty normalises an unset realm to the empty string.
//
// identity.realm became NOT NULL in 00003 because a NULL one made
// UNIQUE (realm, name) silently not fire -- NULL <> NULL, so two realm-less
// identities called 'svc-orders' were both accepted, which is exactly the pair
// the constraint existed to stop. Normalising here rather than at every call
// site keeps "no realm" expressible in Go as a nil pointer.
func realmOrEmpty(realm *string) string {
	if realm == nil {
		return ""
	}
	return *realm
}

func (s *SQLStore) CreateIdentity(ctx context.Context, actor domain.Actor, i *domain.Identity) error {
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO identity (id, kind, name, realm, secret_ref, rotation_days,
			                      last_rotated, team_id, lifecycle)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			i.ID, i.Kind, i.Name, realmOrEmpty(i.Realm), i.SecretRef, i.RotationDays,
			i.LastRotated, i.TeamID, i.Lifecycle)
		if err != nil {
			return translateWriteErr(err, "creating identity")
		}
		return t.logCreate(ctx, "identity", i.ID, i)
	})
}

// ListIdentities returns every principal.
func (s *SQLStore) ListIdentities(ctx context.Context) ([]domain.Identity, error) {
	var identities []domain.Identity
	if err := s.read(ctx, &identities, `SELECT * FROM identity ORDER BY realm, name`); err != nil {
		return nil, fmt.Errorf("listing identities: %w", err)
	}
	return identities, nil
}

// ---------- change log ----------

// changeLogSelect resolves actor -> app_user.display_name/username with a
// LEFT JOIN, never an inner one: the seeder and system writes have no
// app_user row at all, and a scrubbed user must degrade to the raw id rather
// than break the page (docs/DECISIONS.md, 2026-07-28 decisions).
const changeLogSelect = `
	SELECT cl.*, COALESCE(u.display_name, u.username) AS actor_name
	FROM change_log cl
	LEFT JOIN app_user u ON u.id = cl.actor`

// ListChangesForEntity returns the declared audit history of one row, newest
// first.
//
// No page calls this any more, and that is deliberate. It answers "what changed
// about this one row", which is the easy half of the question an incident asks:
// it omits the observed transitions for the same row, and it omits the one-hop
// declared neighbours -- the host that was retired, the port that was unpatched
// -- which is usually where the cause is. The entity detail pages call
// TimelineForEntityAndNeighbours instead (docs/AUDIT.md rule 15).
//
// It stays because it is the sharpest assertion a test can make about the audit
// trail: exactly these entries, for exactly this row, with nothing folded in.
// Most of internal/store's "every mutation writes a change_log row" coverage is
// built on it, and a broader read would make those tests weaker.
func (s *SQLStore) ListChangesForEntity(ctx context.Context, entityType, entityID string, limit int) ([]domain.ChangeLog, error) {
	if limit <= 0 {
		limit = 50
	}
	var changes []domain.ChangeLog
	err := s.read(ctx, &changes, changeLogSelect+`
		WHERE cl.entity_type = ? AND cl.entity_id = ?
		ORDER BY cl.at DESC, cl.id DESC
		LIMIT ?`, entityType, entityID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing changes for %s %s: %w", entityType, entityID, err)
	}
	return changes, nil
}

// ListRecentChanges returns the global activity feed.
func (s *SQLStore) ListRecentChanges(ctx context.Context, limit int) ([]domain.ChangeLog, error) {
	if limit <= 0 {
		limit = 25
	}
	var changes []domain.ChangeLog
	err := s.read(ctx, &changes, changeLogSelect+` ORDER BY cl.at DESC, cl.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("listing recent changes: %w", err)
	}
	return changes, nil
}
