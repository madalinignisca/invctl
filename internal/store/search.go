package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/gabriel/invctl/internal/domain"
)

// Search is the one place where dialect-specific SQL is expected and
// acceptable (HANDOVER §4.4): FTS5 on SQLite, pg_trgm on PostgreSQL. Both
// implementations read and write the same logical search_index table, so the
// indexing side stays shared and only the query differs.

// searchDoc is one indexed entity.
type searchDoc struct {
	EntityType string
	EntityID   string
	Title      string
	Subtitle   string
	Body       string
}

// SearchResult is one hit, in a form the UI can link to directly.
type SearchResult struct {
	EntityType string `db:"entity_type"`
	EntityID   string `db:"entity_id"`
	Title      string `db:"title"`
	Subtitle   string `db:"subtitle"`
	Body       string `db:"body"`
	// Why explains the match for structured hits ("IP 10.20.30.5 on eth0"),
	// which is far more useful than a highlighted snippet when the operator
	// pasted an address they found in a log.
	Why string `db:"-"`
}

// indexEntity upserts a document. FTS5 virtual tables have no unique
// constraint to conflict on, so SQLite gets delete-then-insert while
// PostgreSQL gets a real upsert.
func (s *SQLStore) indexEntity(ctx context.Context, t *tx, doc searchDoc) error {
	if s.db.Driver == DriverPostgres {
		_, err := t.exec(ctx, `
			INSERT INTO search_index (entity_type, entity_id, title, subtitle, body)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (entity_type, entity_id)
			DO UPDATE SET title = EXCLUDED.title, subtitle = EXCLUDED.subtitle, body = EXCLUDED.body`,
			doc.EntityType, doc.EntityID, doc.Title, doc.Subtitle, doc.Body)
		if err != nil {
			return fmt.Errorf("indexing %s %s: %w", doc.EntityType, doc.EntityID, err)
		}
		return nil
	}

	if _, err := t.exec(ctx,
		`DELETE FROM search_index WHERE entity_type = ? AND entity_id = ?`,
		doc.EntityType, doc.EntityID); err != nil {
		return fmt.Errorf("clearing index entry for %s %s: %w", doc.EntityType, doc.EntityID, err)
	}
	_, err := t.exec(ctx, `
		INSERT INTO search_index (entity_type, entity_id, title, subtitle, body)
		VALUES (?, ?, ?, ?, ?)`,
		doc.EntityType, doc.EntityID, doc.Title, doc.Subtitle, doc.Body)
	if err != nil {
		return fmt.Errorf("indexing %s %s: %w", doc.EntityType, doc.EntityID, err)
	}
	return nil
}

// indexAsset builds the searchable document for an asset. Serial, asset tag
// and model go into the body because "which box is FCH2033V0YR" is a question
// people genuinely arrive with.
func (s *SQLStore) indexAsset(ctx context.Context, t *tx, a *domain.Asset) error {
	body := []string{a.Kind}
	for _, v := range []*string{a.Serial, a.AssetTag, a.Vendor, a.Model, a.OwnerTeam} {
		if v != nil && *v != "" {
			body = append(body, *v)
		}
	}
	return s.indexEntity(ctx, t, searchDoc{
		EntityType: "asset", EntityID: a.ID,
		Title: a.Name, Subtitle: a.Kind, Body: strings.Join(body, " "),
	})
}

// Search resolves a query string to entities.
//
// Structured lookups run first and are exact: an IP address, a MAC, or a bare
// port number resolves to the thing that owns it rather than to whatever
// happens to mention the string. The text index is the fallback.
func (s *SQLStore) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 25
	}

	var results []SearchResult

	structured, err := s.searchStructured(ctx, query)
	if err != nil {
		return nil, err
	}
	results = append(results, structured...)

	// Over-fetched deliberately. The engines order their own results by
	// relevance -- bm25 on SQLite, trigram similarity on PostgreSQL -- and
	// neither agrees with the other or with what an operator expects. Ranking
	// happens in Go below, so the database LIMIT has to be loose enough that
	// the row which will rank first is still in the set. Cutting at `limit`
	// first and sorting afterwards is how an exact match ends up on page two.
	textual, err := s.searchText(ctx, query, limit*textOverfetch)
	if err != nil {
		return nil, err
	}
	rankResults(query, textual)

	// Structured hits win; drop the duplicate text hit for the same entity.
	seen := make(map[string]bool, len(results))
	for _, r := range results {
		seen[r.EntityType+"/"+r.EntityID] = true
	}
	for _, r := range textual {
		if !seen[r.EntityType+"/"+r.EntityID] {
			results = append(results, r)
		}
	}
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// searchStructured handles the identifier shapes that deserve an exact answer.
func (s *SQLStore) searchStructured(ctx context.Context, query string) ([]SearchResult, error) {
	var results []SearchResult

	// An IP address resolves to the interface that holds it, and to the
	// prefix that contains it -- both are what you want when you paste an
	// address out of a firewall log.
	if av, err := domain.ParseAddr(query); err == nil {
		type ipHit struct {
			AssetID       string  `db:"asset_id"`
			AssetName     string  `db:"asset_name"`
			AssetKind     string  `db:"asset_kind"`
			InterfaceName string  `db:"interface_name"`
			AddrText      string  `db:"addr_text"`
			Role          string  `db:"role"`
			IPID          *string `db:"ip_id"`
		}
		var hits []ipHit
		err := s.read(ctx, &hits, `
			SELECT a.id AS asset_id, a.name AS asset_name, a.kind AS asset_kind,
			       i.name AS interface_name, ip.addr_text, ip.role, ip.id AS ip_id
			FROM ip_address ip
			JOIN interface i ON i.id = ip.interface_id
			JOIN asset a ON a.id = i.asset_id
			WHERE ip.addr_family = ? AND ip.addr_start = ?`, av.Family, av.Start)
		if err != nil {
			return nil, fmt.Errorf("resolving address %s: %w", query, err)
		}
		for _, h := range hits {
			results = append(results, SearchResult{
				EntityType: "asset", EntityID: h.AssetID,
				Title: h.AssetName, Subtitle: h.AssetKind,
				Why: fmt.Sprintf("%s is %s on %s", h.AddrText, h.Role, h.InterfaceName),
			})
		}

		// Most specific containing prefix. Ordering by addr_start descending
		// picks the narrowest range, because a longer prefix has a higher
		// network address within the same containing range.
		var prefixHit struct {
			ID       string  `db:"id"`
			CIDRText string  `db:"cidr_text"`
			Role     *string `db:"role"`
			VLANID   *int    `db:"vlan_id"`
		}
		err = s.readOne(ctx, &prefixHit, `
			SELECT id, cidr_text, role, vlan_id FROM prefix
			WHERE addr_family = ? AND addr_start <= ? AND addr_end >= ?
			ORDER BY addr_start DESC
			LIMIT 1`, av.Family, av.Start, av.Start)
		if err == nil {
			why := "contains " + av.Text
			if prefixHit.VLANID != nil {
				why += fmt.Sprintf(" (VLAN %d)", *prefixHit.VLANID)
			}
			subtitle := "prefix"
			if prefixHit.Role != nil {
				subtitle = *prefixHit.Role
			}
			results = append(results, SearchResult{
				EntityType: "prefix", EntityID: prefixHit.ID,
				Title: prefixHit.CIDRText, Subtitle: subtitle, Why: why,
			})
		} else if !errors.Is(err, domain.ErrNotFound) {
			return nil, fmt.Errorf("resolving prefix for %s: %w", query, err)
		}
	}

	// A MAC resolves to the interface holding it.
	if mac, err := domain.ParseMAC(query); err == nil {
		type macHit struct {
			AssetID       string `db:"asset_id"`
			AssetName     string `db:"asset_name"`
			AssetKind     string `db:"asset_kind"`
			InterfaceName string `db:"interface_name"`
		}
		var hits []macHit
		err := s.read(ctx, &hits, `
			SELECT a.id AS asset_id, a.name AS asset_name, a.kind AS asset_kind, i.name AS interface_name
			FROM interface i JOIN asset a ON a.id = i.asset_id
			WHERE i.mac = ?`, mac)
		if err != nil {
			return nil, fmt.Errorf("resolving mac %s: %w", mac, err)
		}
		for _, h := range hits {
			results = append(results, SearchResult{
				EntityType: "asset", EntityID: h.AssetID,
				Title: h.AssetName, Subtitle: h.AssetKind,
				Why: fmt.Sprintf("%s is on %s", mac, h.InterfaceName),
			})
		}
	}

	// A bare number is a port: "who listens on 5432".
	if port, err := strconv.Atoi(query); err == nil && port > 0 && port <= 65535 {
		type portHit struct {
			ServiceID   string `db:"service_id"`
			ServiceName string `db:"service_name"`
			ServiceCode string `db:"service_code"`
			Endpoint    string `db:"endpoint_name"`
			Proto       string `db:"l4_proto"`
		}
		var hits []portHit
		err := s.read(ctx, &hits, `
			SELECT s.id AS service_id, s.name AS service_name, s.code AS service_code,
			       e.name AS endpoint_name, e.l4_proto
			FROM endpoint e JOIN service s ON s.id = e.service_id
			WHERE e.port = ?
			ORDER BY s.code`, port)
		if err != nil {
			return nil, fmt.Errorf("resolving port %d: %w", port, err)
		}
		for _, h := range hits {
			results = append(results, SearchResult{
				EntityType: "service", EntityID: h.ServiceID,
				Title: h.ServiceName, Subtitle: h.ServiceCode,
				Why: fmt.Sprintf("listens on %s/%d (%s)", h.Proto, port, h.Endpoint),
			})
		}
	}

	// An exact serial number.
	var serialHits []SearchResult
	err := s.read(ctx, &serialHits, `
		SELECT 'asset' AS entity_type, id AS entity_id, name AS title, kind AS subtitle,
		       COALESCE(serial, '') AS body
		FROM asset WHERE serial = ? OR asset_tag = ?`, query, query)
	if err != nil {
		return nil, fmt.Errorf("resolving serial %s: %w", query, err)
	}
	for i := range serialHits {
		serialHits[i].Why = "serial or asset tag matches exactly"
	}
	results = append(results, serialHits...)

	exact, err := s.searchExactNames(ctx, query)
	if err != nil {
		return nil, err
	}
	results = append(results, exact...)

	return results, nil
}

// searchExactNames resolves a name somebody typed in full.
//
// Ranking the text results (rankResults) already floats an exact match to the
// top of what came back -- but only of what came back. In an estate where two
// hundred rows contain the string, the exact one can fall outside the query's
// LIMIT before Go ever sees it, and no amount of sorting recovers a row that
// was never fetched. So a name typed in full is treated as the identifier it
// is, alongside a serial or a MAC, and looked up directly.
//
// Case-insensitive on both engines via LOWER on each side, which is the one
// comparison both agree on without a collation argument.
func (s *SQLStore) searchExactNames(ctx context.Context, query string) ([]SearchResult, error) {
	q := lower(query)
	var results []SearchResult

	var assets []SearchResult
	if err := s.read(ctx, &assets, `
		SELECT 'asset' AS entity_type, id AS entity_id, name AS title, kind AS subtitle,
		       '' AS body
		FROM asset WHERE LOWER(name) = ? AND lifecycle <> ?
		ORDER BY name, id`, q, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("resolving asset name %s: %w", query, err)
	}
	for i := range assets {
		assets[i].Why = "name matches exactly"
	}
	results = append(results, assets...)

	var services []SearchResult
	if err := s.read(ctx, &services, `
		SELECT 'service' AS entity_type, id AS entity_id, name AS title, code AS subtitle,
		       '' AS body
		FROM service WHERE (LOWER(code) = ? OR LOWER(name) = ?) AND lifecycle <> ?
		ORDER BY code, id`, q, q, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("resolving service name %s: %w", query, err)
	}
	for i := range services {
		services[i].Why = "code or name matches exactly"
	}
	results = append(results, services...)

	var projects []SearchResult
	if err := s.read(ctx, &projects, `
		SELECT 'project' AS entity_type, id AS entity_id, name AS title, code AS subtitle,
		       '' AS body
		FROM project WHERE (LOWER(code) = ? OR LOWER(name) = ?) AND lifecycle <> ?
		ORDER BY code, id`, q, q, domain.LifecycleRetired); err != nil {
		return nil, fmt.Errorf("resolving project name %s: %w", query, err)
	}
	for i := range projects {
		projects[i].Why = "code or name matches exactly"
	}
	return append(results, projects...), nil
}

// textOverfetch is how many extra rows the text query asks for so that Go
// ranking has something to work with. Four is enough that an exact name behind
// three pages of prefix matches still surfaces, and small enough that the
// query stays a query.
const textOverfetch = 4

// rankResults orders text hits the way somebody who typed a name expects,
// identically on both engines.
//
// This exists because the two engines disagreed and BOTH were defensible.
// SQLite's bm25 favours short documents, so searching `hv-01` put the bridge
// `hv-01-br0` above the hypervisor `hv-01` -- the bridge's indexed body is
// shorter, having no serial, vendor or model. PostgreSQL's trigram similarity
// put the exact match first. An operator during an incident should not get a
// different first result depending on which engine the deployment runs, and the
// portable answer to "which of these is more relevant" is to decide it in Go.
//
// The order is: exact name, exact code, name starts with the query, name
// contains it, everything else. Ties break on the shorter name -- `hv-01` is a
// more specific answer to `hv-01` than `hv-01-br0` is -- and then
// alphabetically, so the result is stable rather than merely usually right.
func rankResults(query string, results []SearchResult) {
	q := lower(strings.TrimSpace(query))
	sort.SliceStable(results, func(i, j int) bool {
		a, b := rankOf(results[i], q), rankOf(results[j], q)
		if a != b {
			return a < b
		}
		ti, tj := results[i].Title, results[j].Title
		if len(ti) != len(tj) {
			return len(ti) < len(tj)
		}
		if ti != tj {
			return ti < tj
		}
		return results[i].EntityID < results[j].EntityID
	})
}

// Ranks, lowest first.
const (
	rankExactTitle = iota
	rankExactSubtitle
	rankTitlePrefix
	rankTitleContains
	rankOther
)

func rankOf(r SearchResult, q string) int {
	// A service's subtitle is its code, which is what an operator types.
	if rank := nameRank(r.Title, q); rank != rankOther {
		return rank
	}
	if lower(r.Subtitle) == q {
		return rankExactSubtitle
	}
	return rankOther
}

// nameRank scores one name against a lowercased query. Shared with the list
// filters, which face the same question the moment somebody types in their
// search box -- a filtered list IS a search, whatever the box is called, and
// two different answers to "which of these did you mean" on two pages of the
// same application is worse than either answer alone.
func nameRank(name, q string) int {
	name = lower(name)
	switch {
	case name == q:
		return rankExactTitle
	case strings.HasPrefix(name, q):
		return rankTitlePrefix
	case strings.Contains(name, q):
		return rankTitleContains
	default:
		return rankOther
	}
}

// rankNames orders a filtered list so the thing somebody named comes first.
//
// It returns a less function rather than sorting, because the two callers hold
// different slice types and reflection to paper over that would be a panic
// waiting for the first caller who passes something that is not a slice.
//
// The list pages order for BROWSING -- assets by kind, services by tier -- which
// is right until a query is typed, at which point the reader is not browsing.
// sort.SliceStable keeps the browse order as the tie-break, so a query matching
// fifty things equally still reads the way the page always does.
func rankNames(query string, nameAt func(int) string) func(i, j int) bool {
	q := lower(strings.TrimSpace(query))
	return func(i, j int) bool {
		ri, rj := nameRank(nameAt(i), q), nameRank(nameAt(j), q)
		if ri != rj {
			return ri < rj
		}
		return len(nameAt(i)) < len(nameAt(j))
	}
}

// searchText runs the dialect-specific free-text query.
func (s *SQLStore) searchText(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	var results []SearchResult
	if s.db.Driver == DriverPostgres {
		// Trigram similarity rather than tsvector: the queries that matter
		// here are identifier fragments, which word-stemming handles badly.
		pattern := "%" + escapeLike(lower(query)) + "%"
		err := s.read(ctx, &results, `
			SELECT entity_type, entity_id, title, subtitle, body
			FROM search_index
			WHERE LOWER(title) LIKE ? ESCAPE '\' OR LOWER(subtitle) LIKE ? ESCAPE '\'
			   OR LOWER(body) LIKE ? ESCAPE '\' 
			ORDER BY similarity(title, ?) DESC, title ASC
			LIMIT ?`, pattern, pattern, pattern, query, limit)
		if err != nil {
			return nil, fmt.Errorf("searching: %w", err)
		}
		return results, nil
	}

	err := s.read(ctx, &results, `
		SELECT entity_type, entity_id, title, subtitle, body
		FROM search_index
		WHERE search_index MATCH ?
		ORDER BY rank
		LIMIT ?`, fts5Query(query), limit)
	if err != nil {
		return nil, fmt.Errorf("searching: %w", err)
	}
	return results, nil
}

// escapeLike neutralises the two LIKE metacharacters so a query containing them
// matches them literally.
//
// Not a security fix -- the pattern is a bound parameter either way -- but a
// correctness one: an asset tag containing an underscore is a real thing, and
// searching for it currently matches any character in that position. The
// backslash escape is spelled out with ESCAPE at the call site because SQLite
// has no default escape character and PostgreSQL's is backslash only when
// standard_conforming_strings is off.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`)
	return r.Replace(s)
}

// fts5Query turns user input into a safe FTS5 prefix query.
//
// FTS5 has its own query language, and unescaped operator characters in user
// input are at best a syntax error and at worst a surprise. Quoting every
// token neutralises the syntax; appending * keeps type-ahead useful.
func fts5Query(query string) string {
	fields := strings.FieldsFunc(query, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ',' || r == ';'
	})
	tokens := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.ReplaceAll(f, `"`, `""`)
		if f == "" {
			continue
		}
		tokens = append(tokens, `"`+f+`"*`)
	}
	if len(tokens) == 0 {
		return `""`
	}
	return strings.Join(tokens, " ")
}
