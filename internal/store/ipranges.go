// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"fmt"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Reservations, and the "what is still free" query they feed.

// IPRangeRow is a reservation with its VRF resolved.
type IPRangeRow struct {
	domain.IPRange
	VRFName string `db:"vrf_name"`
}

// ListIPRanges returns every live reservation, lowest first.
func (s *SQLStore) ListIPRanges(ctx context.Context) ([]IPRangeRow, error) {
	var rows []IPRangeRow
	err := s.read(ctx, &rows, `
		SELECT r.*, COALESCE(v.name, '') AS vrf_name
		FROM ip_range r
		LEFT JOIN vrf v ON v.id = r.vrf_id
		WHERE r.lifecycle <> 'retired'
		ORDER BY r.addr_family, r.addr_start, r.addr_end`)
	if err != nil {
		return nil, fmt.Errorf("listing ip ranges: %w", err)
	}
	return rows, nil
}

// GetIPRange loads one reservation.
func (s *SQLStore) GetIPRange(ctx context.Context, id string) (*domain.IPRange, error) {
	var r domain.IPRange
	if err := s.readOne(ctx, &r, `SELECT * FROM ip_range WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting ip range %s: %w", id, err)
	}
	return &r, nil
}

// CreateIPRange declares a reservation.
func (s *SQLStore) CreateIPRange(ctx context.Context, p domain.Permit, r *domain.IPRange) error {
	if err := r.Validate(); err != nil {
		return err
	}
	// The row the INSERT just wrote is version 1 (the column default).
	r.RowVersion = 1
	at := domain.FormatTime(s.now())
	r.CreatedAt, r.UpdatedAt = &at, &at
	return s.write(ctx, p, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO ip_range (id, start_text, end_text, addr_family, addr_start, addr_end,
			                      vrf_id, role, description, lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.StartText, r.EndText, r.AddrFamily, r.AddrStart, r.AddrEnd,
			r.VRFID, r.Role, r.Description, r.Lifecycle, r.CreatedAt, r.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating ip range")
		}
		if err := t.logCreate(ctx, "ip_range", r.ID, r); err != nil {
			return err
		}
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "ip_range", EntityID: r.ID,
			Title:    r.StartText + " – " + r.EndText,
			Subtitle: derefString(r.Role),
			Body:     r.StartText + " " + r.EndText,
		})
	})
}

// UpdateIPRange corrects a reservation.
func (s *SQLStore) UpdateIPRange(ctx context.Context, p domain.Permit, r *domain.IPRange) error {
	if err := r.Validate(); err != nil {
		return err
	}
	before, err := s.GetIPRange(ctx, r.ID)
	if err != nil {
		return err
	}
	at := domain.FormatTime(s.now())
	r.UpdatedAt = &at

	return s.write(ctx, p, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE ip_range SET start_text = ?, end_text = ?, addr_family = ?,
			                    addr_start = ?, addr_end = ?, vrf_id = ?,
			                    role = ?, description = ?,
			                    updated_at = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			r.StartText, r.EndText, r.AddrFamily, r.AddrStart, r.AddrEnd,
			r.VRFID, r.Role, r.Description, at, r.ID, r.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating ip range")
		}
		if err := requireVersion(res, "ip_range", r.ID, &r.RowVersion); err != nil {
			return err
		}
		if err := t.logUpdate(ctx, "ip_range", r.ID, before, r); err != nil {
			return err
		}
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "ip_range", EntityID: r.ID,
			Title:    r.StartText + " – " + r.EndText,
			Subtitle: derefString(r.Role),
			Body:     r.StartText + " " + r.EndText,
		})
	})
}

// RetireIPRange withdraws a reservation. Soft, like every other entity: the
// space becomes allocatable again and the record that it once was not stays.
func (s *SQLStore) RetireIPRange(ctx context.Context, p domain.Permit, id string) error {
	before, err := s.GetIPRange(ctx, id)
	if err != nil {
		return err
	}
	if before.Lifecycle == domain.LifecycleRetired {
		// Already withdrawn: a second audit entry would claim a withdrawal
		// that did not happen.
		return nil
	}
	at := domain.FormatTime(s.now())
	after := *before
	after.Lifecycle = domain.LifecycleRetired
	after.UpdatedAt = &at

	return s.write(ctx, p, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE ip_range SET lifecycle = 'retired', updated_at = ?,
			                    row_version = row_version + 1
			WHERE id = ? AND row_version = ?`, at, id, before.RowVersion)
		if err != nil {
			return translateWriteErr(err, "retiring ip range")
		}
		if err := requireVersion(res, "ip_range", id, &before.RowVersion); err != nil {
			return err
		}
		// The index entry stays. No retire path in this codebase purges one --
		// assets, services and endpoints all leave theirs -- and inventing a
		// purge for this one entity type would make "is it searchable" depend
		// on which table it is in.
		return t.logUpdate(ctx, "ip_range", id, before, &after)
	})
}

// NextFree reports the lowest unallocated address in a prefix.
type NextFree struct {
	Address string
	// Found is false when everything usable is spoken for. A prefix with no
	// free address is a real answer and a common one; an empty string with no
	// flag beside it reads as "not computed", which is a different thing.
	Found bool
}

// NextFreeAddress finds the lowest address in a prefix that nothing has taken.
//
// THE EXCLUSIONS ARE THE WHOLE ANSWER, and there are three kinds. Assigned
// addresses are the obvious one. The other two follow from the allocation rule
// the tree already applies: a CHILD PREFIX has been delegated, and a
// RESERVATION belongs to something that will issue from it without asking this
// system. Offering an address out of either is how the same address reaches two
// hosts a fortnight apart.
//
// Scoped to the prefix's VRF throughout. A reservation in another tenant's
// 10.0.0.0/8 says nothing about this one.
func (s *SQLStore) NextFreeAddress(ctx context.Context, prefixID string) (NextFree, error) {
	p, err := s.GetPrefix(ctx, prefixID)
	if err != nil {
		return NextFree{}, err
	}

	// One row shape for all three sources: they differ in what they mean and
	// not at all in what the allocator needs from them.
	type span struct {
		Start []byte `db:"addr_start"`
		End   []byte `db:"addr_end"`
	}
	var spans []span

	// vrfMatch is NULL-safe equality written portably, and it took three tries.
	// `IS NOT DISTINCT FROM` is PostgreSQL-only; SQLite's two-column `IS` is not
	// accepted there; and spelling the comparison out as `? IS NULL AND ...`
	// fails on PostgreSQL with "could not determine data type of parameter",
	// because a bare placeholder next to IS NULL gives the planner nothing to
	// infer from. COALESCE to a sentinel puts the parameter in text context,
	// which both engines accept -- and the empty string cannot collide with a
	// real value because every id here is a UUID.
	const vrfMatch = `COALESCE(%[1]s.vrf_id, '') = COALESCE(?, '')`

	var children []span
	err = s.read(ctx, &children, fmt.Sprintf(`
		SELECT c.addr_start, c.addr_end FROM prefix c
		WHERE c.addr_family = ? AND c.id <> ?
		  AND c.addr_start >= ? AND c.addr_end <= ?
		  AND `+vrfMatch, "c"),
		p.AddrFamily, p.ID, p.AddrStart, p.AddrEnd, p.VRFID)
	if err != nil {
		return NextFree{}, fmt.Errorf("reading child prefixes of %s: %w", p.CIDRText, err)
	}
	spans = append(spans, children...)

	var ranges []span
	err = s.read(ctx, &ranges, fmt.Sprintf(`
		SELECT r.addr_start, r.addr_end FROM ip_range r
		WHERE r.addr_family = ? AND r.lifecycle <> 'retired'
		  AND r.addr_end >= ? AND r.addr_start <= ?
		  AND `+vrfMatch, "r"),
		p.AddrFamily, p.AddrStart, p.AddrEnd, p.VRFID)
	if err != nil {
		return NextFree{}, fmt.Errorf("reading reservations in %s: %w", p.CIDRText, err)
	}
	spans = append(spans, ranges...)

	// Assignments. ip_address carries no VRF of its own -- it is scoped by the
	// interface it sits on -- so this is the one source the VRF filter cannot
	// narrow, and it is deliberately left wide: counting an address that might
	// be another tenant's costs one address, and missing one that is ours
	// hands out a duplicate.
	var addrs []span
	err = s.read(ctx, &addrs, `
		SELECT ip.addr_start, ip.addr_start AS addr_end FROM ip_address ip
		WHERE ip.addr_family = ? AND ip.addr_start >= ? AND ip.addr_start <= ?`,
		p.AddrFamily, p.AddrStart, p.AddrEnd)
	if err != nil {
		return NextFree{}, fmt.Errorf("reading assignments in %s: %w", p.CIDRText, err)
	}
	spans = append(spans, addrs...)

	taken := make([]domain.AddrSpan, 0, len(spans))
	for _, sp := range spans {
		taken = append(taken, domain.AddrSpan{Start: sp.Start, End: sp.End})
	}
	addr, ok := domain.FirstFreeAddress(p.CIDRText, taken)
	return NextFree{Address: addr, Found: ok}, nil
}
