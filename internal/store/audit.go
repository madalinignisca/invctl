// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// The audit read side: paging the change log, and the incident timeline
// (docs/AUDIT.md rule 15).
//
// Two problems, one file.
//
// The first is volume. `ListRecentChanges` was `ORDER BY at DESC LIMIT ?` with
// no filter, capped at 200 on /changes and 12 on the dashboard. Rule 10 already
// keeps observed transitions out of change_log, so a flapping estate can no
// longer flood it -- but a busy Tuesday still can, and the entry that explains
// an incident is the one that falls off the bottom. Hence a date range and
// pagination, both of which have to be here rather than in a handler, because a
// LIMIT applied after the rows are read is not pagination.
//
// The second is that "what changed just before this broke" is not answerable
// from one entity's history. The change that broke a service is usually a
// change to something beside it: the host it runs on, the port it is patched
// into, the dependency somebody re-pointed. So the timeline folds the entity's
// own history with its ONE-HOP DECLARED NEIGHBOURS, and unions in the observed
// transitions for the same rows -- a declared change at 02:58 followed by a
// transition to `down` at 03:01 is the whole answer, and it is invisible if the
// two ledgers are read on separate pages.
//
// The fold is done in Go. `diff` is JSON and querying inside a JSON column is
// banned here, so grouping or filtering by what changed is not available and
// should not be faked with a LIKE.
//
// Everything below is a read. Nothing in this file writes anything, and nothing
// in this file acts on the estate.

package store

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
)

// ---------------------------------------------------------------------------
// Paging the change log
// ---------------------------------------------------------------------------

// DefaultChangePageSize is one screen of audit entries.
const DefaultChangePageSize = 50

// MaxChangePageSize bounds what a caller can ask for in one request. A reader
// who can request an unbounded page can ask the server to materialise the whole
// change log, which after a year is the one table guaranteed to be large.
const MaxChangePageSize = 200

// ChangeCursor is a position in the change log.
//
// It is the ORDER BY key -- (at, id) -- and not an offset, because an offset
// silently skips or repeats rows when something is written between two page
// requests, and the audit trail is written to constantly. `id` breaks
// same-millisecond ties and is UUIDv7, so within one timestamp it orders by
// insertion.
type ChangeCursor struct {
	At string
	ID string
}

// String renders a cursor for a URL. The separator is a space because RFC3339
// timestamps and UUIDs contain none.
func (c ChangeCursor) String() string {
	if c.At == "" || c.ID == "" {
		return ""
	}
	return c.At + " " + c.ID
}

// ParseChangeCursor reads one back. A malformed cursor is not an error worth
// showing anybody -- it means a hand-edited URL -- so it degrades to "no
// cursor", which is the first page.
func ParseChangeCursor(v string) (ChangeCursor, bool) {
	at, id, ok := strings.Cut(strings.TrimSpace(v), " ")
	if !ok || at == "" || id == "" {
		return ChangeCursor{}, false
	}
	// THE TIMESTAMP HAS TO BE ONE. Checking only for a space accepted
	// "notatime <uuid>", which then went into a WHERE clause comparing
	// lexicographically against RFC3339 text -- so it did not error, it just
	// silently selected a window nobody asked for. An external review found it
	// by typing exactly that.
	if _, err := domain.ParseTime(at); err != nil {
		return ChangeCursor{}, false
	}
	return ChangeCursor{At: at, ID: id}, true
}

// GetChange loads one audit entry.
//
// Exists so an entry can be LINKED TO. An incident write-up that says "the
// config changed at 03:12" is an assertion; one that links to the entry is
// evidence, and the log is append-only so the link cannot rot.
func (s *SQLStore) GetChange(ctx context.Context, id string) (*domain.ChangeLog, error) {
	var row domain.ChangeLog
	if err := s.readOne(ctx, &row, changeLogSelect+` WHERE cl.id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting change %s: %w", id, err)
	}
	return &row, nil
}

// ChangeFilter selects a window of the change log.
//
// From and To are RFC3339 UTC TEXT, inclusive at both ends, and compare
// lexicographically -- which is the whole reason timestamps are stored in that
// format. Before and After are mutually exclusive; supplying both is a caller
// bug and Before wins.
type ChangeFilter struct {
	EntityType string
	// From and To bound the range. Either may be empty for "unbounded".
	From string
	To   string
	// Before pages towards older entries, After towards newer. Both are
	// exclusive of the cursor row itself.
	Before *ChangeCursor
	After  *ChangeCursor
	Limit  int
}

// ChangePage is one screen of entries plus the links either side of it.
type ChangePage struct {
	Changes []domain.ChangeLog
	// Newer and Older are the cursors for the adjacent pages, nil when there is
	// nothing that way. A template renders a link only when one is present, so
	// a dead "next" button is not reachable.
	Newer *ChangeCursor
	Older *ChangeCursor
	// Total is how many entries match the range, ignoring the cursor. It is what
	// lets a reader see that a filter has narrowed 40,000 entries to 12 rather
	// than believing 12 is all there is.
	Total int
	Limit int
}

// ListChanges returns one page of the change log.
//
// The count and the page are two statements deliberately: a window function
// would do it in one, and window functions are exactly the kind of thing that
// works on PostgreSQL and needs checking on SQLite, for a saving nobody would
// notice.
func (s *SQLStore) ListChanges(ctx context.Context, filter ChangeFilter) (*ChangePage, error) {
	limit := filter.Limit
	switch {
	case limit <= 0:
		limit = DefaultChangePageSize
	case limit > MaxChangePageSize:
		limit = MaxChangePageSize
	}

	where, args := changeRangePredicate(filter)

	var total int
	countQuery := `SELECT COUNT(*) FROM change_log cl` + where
	if err := s.readOne(ctx, &total, countQuery, args...); err != nil {
		return nil, fmt.Errorf("counting changes: %w", err)
	}

	// Ask for one more than the page: whether a further page exists is a fact
	// about the data, and guessing it from "the page came back full" is wrong
	// exactly when the last page is exactly full.
	pageArgs := append([]any{}, args...)
	pageWhere := where
	order := ` ORDER BY cl.at DESC, cl.id DESC`
	backwards := false

	switch {
	case filter.Before != nil:
		pageWhere += conjunction(pageWhere) + `(cl.at < ? OR (cl.at = ? AND cl.id < ?))`
		pageArgs = append(pageArgs, filter.Before.At, filter.Before.At, filter.Before.ID)
	case filter.After != nil:
		pageWhere += conjunction(pageWhere) + `(cl.at > ? OR (cl.at = ? AND cl.id > ?))`
		pageArgs = append(pageArgs, filter.After.At, filter.After.At, filter.After.ID)
		// Reading towards newer entries means reading upwards and turning the
		// result over, so that the page still renders newest-first like every
		// other page of this log.
		order = ` ORDER BY cl.at ASC, cl.id ASC`
		backwards = true
	}
	pageArgs = append(pageArgs, limit+1)

	var rows []domain.ChangeLog
	err := s.read(ctx, &rows, changeLogSelect+pageWhere+order+` LIMIT ?`, pageArgs...)
	if err != nil {
		return nil, fmt.Errorf("listing changes: %w", err)
	}

	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	if backwards {
		for i, j := 0, len(rows)-1; i < j; i, j = i+1, j-1 {
			rows[i], rows[j] = rows[j], rows[i]
		}
	}

	page := &ChangePage{Changes: rows, Total: total, Limit: limit}
	if len(rows) == 0 {
		return page, nil
	}
	first := ChangeCursor{At: rows[0].At, ID: rows[0].ID}
	last := ChangeCursor{At: rows[len(rows)-1].At, ID: rows[len(rows)-1].ID}

	switch {
	case backwards:
		// Came from an older page, so there is always something below; whether
		// there is anything further up is what the extra row answered.
		page.Older = &last
		if more {
			page.Newer = &first
		}
	default:
		if more {
			page.Older = &last
		}
		if filter.Before != nil {
			// Arrived here from a newer page, so one exists.
			page.Newer = &first
		}
	}
	return page, nil
}

// changeRangePredicate builds the filters that apply to both the count and the
// page. The cursor is deliberately not here: it moves within the range and must
// not change the total, or the pager would report a shrinking log as a reader
// walked down it.
func changeRangePredicate(filter ChangeFilter) (string, []any) {
	var (
		clauses []string
		args    []any
	)
	if filter.EntityType != "" {
		clauses = append(clauses, `cl.entity_type = ?`)
		args = append(args, filter.EntityType)
	}
	if filter.From != "" {
		clauses = append(clauses, `cl.at >= ?`)
		args = append(args, filter.From)
	}
	if filter.To != "" {
		clauses = append(clauses, `cl.at <= ?`)
		args = append(args, filter.To)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return ` WHERE ` + strings.Join(clauses, ` AND `), args
}

// conjunction returns the keyword that continues a predicate, so a clause can
// be appended without knowing whether anything came before it.
func conjunction(where string) string {
	if where == "" {
		return ` WHERE `
	}
	return ` AND `
}

// ChangeEntityTypes are the entity types the /changes filter offers.
//
// Derived from the classification census rather than typed out again: a table
// that gains a column has to be classified anyway (rule 1's "a new column
// cannot default into the gap"), and this way a new audited entity type appears
// in the filter without anybody remembering to add it.
func ChangeEntityTypes() []string {
	tables := domain.ClassifiedTables()
	out := make([]string, 0, len(tables))
	for _, t := range tables {
		if domain.ObservedTables[t] && t != prunableTable {
			// Observed tables are not in change_log by construction (rule 10),
			// so offering them as a filter would offer a guaranteed empty page.
			//
			// observed_transition is the exception, and it is the one worth
			// having: it never carries a transition here, but every retention
			// prune writes exactly one row against it, and "when was history
			// last deleted, by whom, and how much" is precisely the question
			// somebody asks after finding a gap.
			continue
		}
		out = append(out, t)
	}
	return out
}

// ---------------------------------------------------------------------------
// The incident timeline
// ---------------------------------------------------------------------------

// TimelineEntry is one row of the incident timeline: either a declared change
// or an observed transition, in one ordering.
//
// The two ledgers are separate tables on purpose (rule 10) and are folded only
// at read time. That is the trade rule 15 describes: the split costs nothing to
// read, because both columns are RFC3339 UTC TEXT and sort correctly on both
// engines, and buys a physical guarantee at write time that a heartbeat cannot
// bury a configuration change.
type TimelineEntry struct {
	// Source is TimelineDeclared or TimelineObserved.
	Source     string `db:"source"`
	ID         string `db:"id"`
	At         string `db:"at"`
	EntityType string `db:"entity_type"`
	EntityID   string `db:"entity_id"`
	// Label is the change action for a declared row and the transition kind for
	// an observed one.
	Label string `db:"label"`
	// Actor and Kind are empty on an observed row as they come out of the
	// database, and are filled in from Reporter afterwards -- see fillActor.
	Actor     string `db:"actor"`
	Kind      string `db:"actor_kind"`
	ActorName string `db:"actor_name"`
	// Detail is the change diff. Empty on an observed row: a transition's whole
	// content is its two states.
	Detail    string `db:"detail"`
	Reporter  string `db:"reporter"`
	FromState string `db:"from_state"`
	ToState   string `db:"to_state"`

	// Rule 9's accounting, carried on a flap_close row and zero everywhere else.
	// Compression is only acceptable because the closing row says how much it
	// hid, and a timeline that showed the bracket without the number would be
	// the suppression the rule forbids wearing the label of the compression it
	// permits.
	FlapCount   int    `db:"flap_count"`
	WindowStart string `db:"window_start"`
	WindowEnd   string `db:"window_end"`

	// Subject is true when the row is about the entity the timeline was asked
	// for, rather than one of its neighbours. A neighbour's change rendered
	// as if it were the subject's own is worse than not showing it.
	Subject bool
	// EntityLabel is a human name for the row's entity, resolved from the
	// neighbour set. Empty when the row is about something no longer reachable
	// from the subject -- a placement that has since moved, say -- because the
	// audit trail outlives the relationships it describes.
	EntityLabel string
	// Relation says how the row's entity relates to the subject: "parent",
	// "host", "placement" and so on. Empty for the subject itself.
	Relation string
}

// Timeline source values.
const (
	TimelineDeclared = "change"
	TimelineObserved = "observed"
	// TimelineJournal is a note a person wrote. A THIRD SOURCE rather than a
	// change_log action, because it is a different kind of statement: the audit
	// trail says what the software did, and a note says what somebody knows.
	// Rendering them identically would let one be mistaken for the other, which
	// is the laundering rule 7 forbids in the other direction.
	TimelineJournal = "journal"
)

// JournalTimelineBudget is how many notes one timeline may carry.
//
// A FIXED SHARE, not a slice of the declared budget. Notes are the scarcest
// thing on the page and the most deliberately placed, so they are never made to
// compete with the churn they are usually written about.
const JournalTimelineBudget = 25

// DisplayActor is what a template renders, matching domain.ChangeLog's.
func (e TimelineEntry) DisplayActor() string {
	if e.ActorName != "" {
		return e.ActorName
	}
	return e.Actor
}

// ActorKind lets a timeline row render through the shared actor_cell partial,
// so rule 5 holds here the same way it holds on the change log: a reader can
// tell a monitor from a human at a glance in the view an incident review
// actually opens.
func (e TimelineEntry) ActorKind() string { return e.Kind }

// IsObserved reports whether this row came from the transition ledger.
func (e TimelineEntry) IsObserved() bool { return e.Source == TimelineObserved }

// IsJournal reports whether this row is a note somebody wrote.
func (e TimelineEntry) IsJournal() bool { return e.Source == TimelineJournal }

// IsFlapClose reports whether this row closes a compressed episode, and is
// therefore the row that accounts for what compression hid (rule 9).
func (e TimelineEntry) IsFlapClose() bool {
	return e.Source == TimelineObserved && e.Label == domain.TransitionFlapClose
}

// IsFlapOpen reports whether this row opens a compressed episode.
func (e TimelineEntry) IsFlapOpen() bool {
	return e.Source == TimelineObserved && e.Label == domain.TransitionFlapOpen
}

// EntityRef names one row of the estate, with enough context to render it.
type EntityRef struct {
	Type string
	ID   string
	// Label is a human name -- an asset's name, a service's code. It may be
	// empty; the timeline falls back to the type and the id.
	Label string
	// Relation is how this row relates to the entity the set was built for.
	Relation string
}

// Neighbour relations, as rendered.
const (
	RelationSubject   = ""
	RelationParent    = "contained by"
	RelationChild     = "contains"
	RelationInterface = "port"
	RelationPlacement = "placement"
	RelationService   = "runs"
	RelationHost      = "runs on"
	RelationEndpoint  = "endpoint"
	RelationDepends   = "dependency"
)

// NeighbourRefs resolves an entity's one-hop declared neighbours.
//
// "One hop" is measured across declared edges, and a `service_instance` counts
// as an EDGE rather than a node: the whole content of that row is "this service,
// that asset", so the asset on the far side of a placement is one hop from the
// service, not two. Any other reading makes the single most useful answer --
// "the host this thing runs on was retired four minutes ago" -- unreachable,
// which would leave the fold doing the easy half of its job.
//
// The subject itself is always the first element, so a caller can pass the
// result straight to the timeline without assembling it twice.
//
// Retired rows are included. An incident review is usually looking for exactly
// the thing that was retired, and hiding it because it was retired is the one
// failure this method must not have.
func (s *SQLStore) NeighbourRefs(ctx context.Context, entityType, entityID string) ([]EntityRef, error) {
	switch entityType {
	case "asset":
		return s.assetNeighbours(ctx, entityID)
	case "service":
		return s.serviceNeighbours(ctx, entityID)
	default:
		// Not a failure: an entity type with no declared neighbourhood defined
		// still has its own history, and returning just the subject keeps the
		// timeline honest rather than empty.
		return []EntityRef{{Type: entityType, ID: entityID}}, nil
	}
}

// labelled is the shape every neighbour query returns.
type labelled struct {
	ID    string `db:"id"`
	Label string `db:"label"`
}

func (s *SQLStore) assetNeighbours(ctx context.Context, id string) ([]EntityRef, error) {
	refs := []EntityRef{{Type: "asset", ID: id}}

	groups := []struct {
		entityType string
		relation   string
		query      string
	}{
		{"asset", RelationParent, `
			SELECT p.id AS id, p.name AS label
			FROM asset a JOIN asset p ON p.id = a.parent_id
			WHERE a.id = ?`},
		{"asset", RelationChild, `
			SELECT id AS id, name AS label FROM asset WHERE parent_id = ? ORDER BY name`},
		{"interface", RelationInterface, `
			SELECT id AS id, name AS label FROM interface WHERE asset_id = ? ORDER BY name`},
		{"service_instance", RelationPlacement, `
			SELECT si.id AS id, s.code AS label
			FROM service_instance si JOIN service s ON s.id = si.service_id
			WHERE si.host_asset_id = ? ORDER BY s.code, si.ordinal`},
		{"service", RelationService, `
			SELECT DISTINCT s.id AS id, s.code AS label
			FROM service_instance si JOIN service s ON s.id = si.service_id
			WHERE si.host_asset_id = ? ORDER BY s.code`},
	}
	for _, g := range groups {
		found, err := s.neighbourGroup(ctx, g.entityType, g.relation, g.query, id)
		if err != nil {
			return nil, err
		}
		refs = append(refs, found...)
	}
	return refs, nil
}

func (s *SQLStore) serviceNeighbours(ctx context.Context, id string) ([]EntityRef, error) {
	refs := []EntityRef{{Type: "service", ID: id}}

	groups := []struct {
		entityType string
		relation   string
		query      string
	}{
		{"service_instance", RelationPlacement, `
			SELECT si.id AS id, a.name AS label
			FROM service_instance si JOIN asset a ON a.id = si.host_asset_id
			WHERE si.service_id = ? ORDER BY a.name, si.ordinal`},
		{"asset", RelationHost, `
			SELECT DISTINCT a.id AS id, a.name AS label
			FROM service_instance si JOIN asset a ON a.id = si.host_asset_id
			WHERE si.service_id = ? ORDER BY a.name`},
		{"endpoint", RelationEndpoint, `
			SELECT id AS id, name AS label FROM endpoint WHERE service_id = ? ORDER BY name`},
		{"dependency", RelationDepends, `
			SELECT d.id AS id, cs.code AS label
			FROM dependency d JOIN service cs ON cs.id = d.consumer_service_id
			WHERE d.consumer_service_id = ? ORDER BY cs.code`},
		// The other direction: edges that name one of this service's endpoints
		// or routes as the provider. Written as two joins rather than a UNION so
		// the shape matches every other group here.
		{"dependency", RelationDepends, `
			SELECT d.id AS id, cs.code AS label
			FROM dependency d
			JOIN service cs ON cs.id = d.consumer_service_id
			LEFT JOIN endpoint pe ON pe.id = d.provider_endpoint_id
			LEFT JOIN route r ON r.id = d.provider_route_id
			LEFT JOIN endpoint re ON re.id = r.frontend_endpoint_id
			WHERE pe.service_id = ? OR re.service_id = ?
			ORDER BY cs.code`},
	}
	for _, g := range groups {
		args := []any{id}
		if strings.Count(g.query, "?") == 2 {
			args = append(args, id)
		}
		found, err := s.neighbourGroup(ctx, g.entityType, g.relation, g.query, args...)
		if err != nil {
			return nil, err
		}
		refs = append(refs, found...)
	}
	return dedupeRefs(refs), nil
}

func (s *SQLStore) neighbourGroup(ctx context.Context, entityType, relation, query string, args ...any) ([]EntityRef, error) {
	var rows []labelled
	if err := s.read(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("resolving %s neighbours: %w", entityType, err)
	}
	out := make([]EntityRef, 0, len(rows))
	for _, row := range rows {
		out = append(out, EntityRef{Type: entityType, ID: row.ID, Label: row.Label, Relation: relation})
	}
	return out, nil
}

// dedupeRefs keeps the first occurrence of each (type, id). The two dependency
// queries overlap for a service that depends on itself through a proxy, and one
// row appearing twice in the timeline would read as two changes.
func dedupeRefs(refs []EntityRef) []EntityRef {
	seen := make(map[EntityRef]bool, len(refs))
	out := make([]EntityRef, 0, len(refs))
	for _, ref := range refs {
		key := EntityRef{Type: ref.Type, ID: ref.ID}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, ref)
	}
	return out
}

// EntityTimeline folds one entity's declared history with its observed
// transitions, newest first. It is the narrow form: no neighbours.
func (s *SQLStore) EntityTimeline(ctx context.Context, entityType, entityID string, limit int) ([]TimelineEntry, error) {
	return s.Timeline(ctx, []EntityRef{{Type: entityType, ID: entityID}}, limit)
}

// TimelineForEntityAndNeighbours is the 03:00 query: this entity's history and
// its one-hop declared neighbours', with the observed transitions for the same
// rows folded in and everything in one ordering.
func (s *SQLStore) TimelineForEntityAndNeighbours(ctx context.Context, entityType, entityID string, limit int) ([]TimelineEntry, []EntityRef, error) {
	refs, err := s.NeighbourRefs(ctx, entityType, entityID)
	if err != nil {
		return nil, nil, err
	}
	entries, err := s.Timeline(ctx, refs, limit)
	if err != nil {
		return nil, nil, err
	}
	return entries, refs, nil
}

// MaxTimelineRefs bounds how many entities one timeline may cover.
//
// Every ref becomes a placeholder in both arms of the UNION, and a hypervisor
// holding two hundred guests would otherwise build a statement with a thousand
// of them. The subject and its nearest neighbours are what answer the question;
// truncating the tail costs a rack-level page some breadth and costs nothing on
// any page anybody opens at 03:00.
const MaxTimelineRefs = 120

// Timeline reads both ledgers for a set of entities and returns them merged.
//
// The UNION ALL is what rule 15 asks for, and it is genuinely portable: every
// column is TEXT, both `at` columns are RFC3339 UTC so they sort correctly as
// strings, and the ORDER BY names output columns rather than table columns.
// Nothing is cast, and no NULL is asked to acquire a type from the other arm --
// the declared branch supplies empty strings where the observed branch has real
// columns, so a reader (and PostgreSQL's type resolution) never has to guess.
func (s *SQLStore) Timeline(ctx context.Context, refs []EntityRef, limit int) ([]TimelineEntry, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 60
	}
	if len(refs) > MaxTimelineRefs {
		refs = refs[:MaxTimelineRefs]
	}

	declaredWhere, declaredArgs := refPredicate("cl", refs, nil)
	if declaredWhere == "" {
		return nil, nil
	}

	declaredQuery := `
		SELECT '` + TimelineDeclared + `' AS source, cl.id AS id, cl.at AS at,
		       cl.entity_type AS entity_type, cl.entity_id AS entity_id,
		       cl.action AS label, cl.actor AS actor, cl.actor_kind AS actor_kind,
		       COALESCE(u.display_name, u.username, '') AS actor_name,
		       cl.diff AS detail, '' AS reporter, '' AS from_state, '' AS to_state,
		       0 AS flap_count, '' AS window_start, '' AS window_end
		FROM change_log cl
		LEFT JOIN app_user u ON u.id = cl.actor
		WHERE ` + declaredWhere + `
		ORDER BY cl.at DESC, cl.id DESC
		LIMIT ?`

	// The observed arm covers only what can be observed at all. Passing an
	// `endpoint` id to it would be harmless and would also be a placeholder
	// nobody can justify reading.
	observedWhere, observedArgs := refPredicate("ot", refs, domain.ObservableEntityTypes)
	observedQuery := `
		SELECT '` + TimelineObserved + `' AS source, ot.id AS id, ot.at AS at,
		       ot.entity_type AS entity_type, ot.entity_id AS entity_id,
		       ot.kind AS label, '' AS actor, '' AS actor_kind, '' AS actor_name,
		       '' AS detail, ot.reporter AS reporter,
		       COALESCE(ot.from_state, '') AS from_state, ot.to_state AS to_state,
		       COALESCE(ot.flap_count, 0) AS flap_count,
		       COALESCE(ot.window_start, '') AS window_start,
		       COALESCE(ot.window_end, '') AS window_end
		FROM observed_transition ot
		WHERE ` + observedWhere + `
		ORDER BY ot.at DESC, ot.id DESC
		LIMIT ?`

	// The journal arm. Notes are declared state and are written by people, so
	// they are the least numerous thing on this page by orders of magnitude --
	// which is exactly why they get a budget of their own rather than sharing
	// the declared one. A note somebody wrote during an incident being evicted
	// by the edits that incident caused would defeat the reason for writing it.
	journalWhere, journalArgs := refPredicate("j", refs, nil)
	journalQuery := `
		SELECT '` + TimelineJournal + `' AS source, j.id AS id, j.created_at AS at,
		       j.entity_type AS entity_type, j.entity_id AS entity_id,
		       j.kind AS label, j.author AS actor, 'user' AS actor_kind,
		       COALESCE(u.display_name, u.username, '') AS actor_name,
		       j.body AS detail, '' AS reporter, '' AS from_state, '' AS to_state,
		       0 AS flap_count, '' AS window_start, '' AS window_end
		FROM journal_entry j
		LEFT JOIN app_user u ON u.id = j.author
		WHERE j.lifecycle = ? AND ` + journalWhere + `
		ORDER BY j.created_at DESC, j.id DESC
		LIMIT ?`

	// Each arm gets its own budget, and that is the whole point.
	//
	// A single LIMIT across a UNION lets whichever side is noisier evict the
	// other, and observed rows are unboundedly noisier by construction: forty
	// instances transitioning once already exceeds a 60-row page. Rule 15 is
	// about exactly this failure on /changes, and keeping one shared LIMIT here
	// merely moved it onto the entity page -- the screen an incident review
	// actually opens. Measured before the fix: 75 ordinary transitions,
	// deliberately below FlapThreshold so nothing was even compressed, pushed
	// every declared row off an asset's timeline including an edit made seconds
	// earlier.
	//
	// If either side comes back short the other keeps what it found, so a quiet
	// estate still fills the page.
	declaredBudget, observedBudget := timelineBudget(limit, observedWhere != "")

	var entries []TimelineEntry
	if err := s.read(ctx, &entries, declaredQuery, append(append([]any{}, declaredArgs...), declaredBudget)...); err != nil {
		return nil, fmt.Errorf("reading declared timeline for %d entities: %w", len(refs), err)
	}
	if observedWhere != "" {
		var observed []TimelineEntry
		if err := s.read(ctx, &observed, observedQuery, append(append([]any{}, observedArgs...), observedBudget)...); err != nil {
			return nil, fmt.Errorf("reading observed timeline for %d entities: %w", len(refs), err)
		}
		entries = append(entries, observed...)
	}
	if journalWhere != "" {
		var notes []TimelineEntry
		args := append([]any{domain.LifecycleActive}, journalArgs...)
		if err := s.read(ctx, &notes, journalQuery, append(args, JournalTimelineBudget)...); err != nil {
			return nil, fmt.Errorf("reading journal timeline for %d entities: %w", len(refs), err)
		}
		entries = append(entries, notes...)
	}
	// Merged in Go rather than in SQL. Both timestamps are RFC3339 UTC TEXT and
	// sort correctly as strings; id is the tiebreak, so the order is total and
	// identical on both engines.
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].At != entries[j].At {
			return entries[i].At > entries[j].At
		}
		return entries[i].ID > entries[j].ID
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}

	byRef := make(map[EntityRef]EntityRef, len(refs))
	for _, ref := range refs {
		byRef[EntityRef{Type: ref.Type, ID: ref.ID}] = ref
	}
	subject := refs[0]
	for i := range entries {
		fillActor(&entries[i])
		entries[i].Subject = entries[i].EntityType == subject.Type && entries[i].EntityID == subject.ID
		if ref, ok := byRef[EntityRef{Type: entries[i].EntityType, ID: entries[i].EntityID}]; ok {
			entries[i].EntityLabel = ref.Label
			entries[i].Relation = ref.Relation
		}
	}
	return entries, nil
}

// timelineBudget splits a page between the two ledgers.
//
// The declared share is a floor, not a quota: it is what observed noise may
// never take, and the observed arm gets the rest. Half was chosen because the
// question the panel answers -- "what changed just before this broke" -- needs
// both halves legible, and a declared change is the rarer and more expensive
// one to lose.
func timelineBudget(limit int, hasObserved bool) (declared, observed int) {
	if !hasObserved {
		return limit, 0
	}
	declared = limit / 2
	if declared < 1 {
		declared = 1
	}
	return declared, limit - declared
}

// fillActor gives an observed row the attribution the transition ledger does
// not store.
//
// `observed_transition` has no actor column and should not gain one: the
// reporter IS the writer, and duplicating it would create two places for the
// same fact to disagree. But rule 5 requires every rendered actor to carry its
// kind, so the display value is derived here from the one column that holds it
// -- and it is derived in Go rather than as a SQL literal, so the kind comes
// from domain.ActorKindAgent and cannot drift from the CHECK constraint.
func fillActor(e *TimelineEntry) {
	if e.Source != TimelineObserved {
		return
	}
	e.Actor = e.Reporter
	e.Kind = domain.ActorKindAgent
}

// refPredicate builds "(alias.entity_type = ? AND alias.entity_id IN (?,?))"
// groups joined by OR, restricted to onlyTypes when it is non-nil.
//
// Grouping by type rather than emitting one pair per ref keeps the statement
// short and lets the (entity_type, entity_id, at) index do the work on both
// engines. Types are sorted so the same set produces the same statement, which
// matters for reading a slow query log.
func refPredicate(alias string, refs []EntityRef, onlyTypes []string) (string, []any) {
	allowed := map[string]bool{}
	for _, t := range onlyTypes {
		allowed[t] = true
	}

	byType := map[string][]string{}
	for _, ref := range refs {
		if onlyTypes != nil && !allowed[ref.Type] {
			continue
		}
		byType[ref.Type] = append(byType[ref.Type], ref.ID)
	}
	if len(byType) == 0 {
		return "", nil
	}

	types := make([]string, 0, len(byType))
	for t := range byType {
		types = append(types, t)
	}
	sort.Strings(types)

	var (
		clauses []string
		args    []any
	)
	for _, t := range types {
		ids := byType[t]
		clauses = append(clauses, fmt.Sprintf(`(%s.entity_type = ? AND %s.entity_id IN (%s))`,
			alias, alias, placeholders(len(ids))))
		args = append(args, t)
		args = append(args, anySlice(ids)...)
	}
	return `(` + strings.Join(clauses, ` OR `) + `)`, args
}
