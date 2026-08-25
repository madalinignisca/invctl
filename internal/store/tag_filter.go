// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

// Filtering an estate list by tag, piece 3 of WP-G4a (docs/tags-design.md
// §5). Joins the filter structs the list handlers already build
// (AssetFilter, ServiceFilter / assetFilterFrom, serviceFilterFrom) rather
// than inventing a second filtering path.
//
// Multi-tag filtering is AND, not OR (design §5): "tagged dr AND pci", never
// "tagged dr OR pci" -- OR is reachable by asking twice. The shape is fixed
// by the design doc rather than left to the implementer, because it is "the
// one place an implementer will invent something wrong": count the DISTINCT
// matching tags per entity and require it to equal how many were asked for.

// tagFilterClause builds the AND-membership subquery design.md §5 fixes:
//
//	<column> IN (
//	  SELECT et.entity_id FROM entity_tag et
//	  WHERE et.entity_type = ? AND et.tag_id IN (?, ?, ...)
//	  GROUP BY et.entity_id
//	  HAVING COUNT(DISTINCT et.tag_id) = ?
//	)
//
// column is the caller's own primary key column ("a.id", "s.id", ...), never
// assembled from a request value -- every call site below passes a literal.
//
// THE EMPTY CASE IS GUARDED HERE, EXPLICITLY, BEFORE ANY QUERY IS BUILT
// (design §5's own warning): with no tags asked for, `HAVING COUNT(...) = 0`
// would match every row with no entity_tag join needed at all, silently
// turning "I did not ask for a tag filter" into "show me nothing filtered",
// which reads the same as correct until you count rows. ids is deduplicated
// first, because a duplicate tag id in the IN list would demand
// COUNT(DISTINCT ...) equal to a count no entity could ever satisfy --
// requiring 2 distinct tags for a list of [x, x] is a filter matching
// nothing, silently, for a caller who asked for exactly one tag twice.
//
// Returns ("", nil) to mean "do not filter" -- callers append the clause to
// `where` only when it is non-empty, so an empty ids list adds nothing to
// the query at all, which is the only way "no filter" and "filter, but
// finds nothing" cannot be confused by construction.
func tagFilterClause(column, entityType string, ids []string) (string, []any) {
	ids = dedupeIDs(ids)
	if len(ids) == 0 {
		return "", nil
	}
	clause := column + ` IN (
		SELECT et.entity_id FROM entity_tag et
		WHERE et.entity_type = ? AND et.tag_id IN (` + placeholders(len(ids)) + `)
		GROUP BY et.entity_id
		HAVING COUNT(DISTINCT et.tag_id) = ?
	)`
	args := append([]any{entityType}, anySlice(ids)...)
	args = append(args, len(ids))
	return clause, args
}
