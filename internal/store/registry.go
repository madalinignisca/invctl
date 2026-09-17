// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"bytes"
	"context"
	"fmt"
	"math/big"

	"github.com/madalinignisca/invctl/internal/domain"
)

// The layer above prefixes: what was delegated, by whom, and how much is spent.

// AggregateRow is a delegation with its registry and its usage resolved.
type AggregateRow struct {
	domain.Aggregate
	RIRName   string `db:"rir_name"`
	IsPrivate bool   `db:"is_private"`
	// Prefixes is how many declared networks fall inside this block, and
	// Allocated how many addresses they account for. Both derived from the
	// range scan at read time, stored nowhere -- a delegation's utilisation
	// changes whenever somebody declares a prefix, and a written-back figure
	// would be stale before the page finished rendering.
	Prefixes  int
	Allocated *big.Int
}

// UtilPercent is how much of the delegation has been carved into prefixes.
func (a AggregateRow) UtilPercent() float64 {
	size := a.Size()
	if size == nil || size.Sign() == 0 || a.Allocated == nil {
		return 0
	}
	num := new(big.Float).SetInt(a.Allocated)
	den := new(big.Float).SetInt(size)
	pct, _ := new(big.Float).Quo(num, den).Float64()
	return pct * 100
}

// Unused reports a delegation nobody has carved anything out of. On private
// space that is a tidiness note; on a registry allocation it is money.
func (a AggregateRow) Unused() bool { return a.Prefixes == 0 }

// ListAggregates returns every delegation with what has been carved from it.
//
// Two queries and the arithmetic in Go, for the reason the prefix tree gives:
// a /32 aggregate of v6 holds more addresses than any integer type here, and
// summing prefix sizes is not something either engine can do over a BLOB range.
func (s *SQLStore) ListAggregates(ctx context.Context) ([]AggregateRow, error) {
	var rows []AggregateRow
	err := s.read(ctx, &rows, `
		SELECT a.*, COALESCE(r.name, '') AS rir_name,
		       COALESCE(r.is_private, FALSE) AS is_private
		FROM aggregate a
		LEFT JOIN rir r ON r.id = a.rir_id
		WHERE a.lifecycle <> 'retired'
		ORDER BY a.addr_family, a.addr_start`)
	if err != nil {
		return nil, fmt.Errorf("listing aggregates: %w", err)
	}
	if len(rows) == 0 {
		return rows, nil
	}

	var flat []domain.Prefix
	err = s.read(ctx, &flat, `SELECT * FROM prefix`)
	if err != nil {
		return nil, fmt.Errorf("reading prefixes: %w", err)
	}
	// The TREE, not the flat list, and the difference is a figure over 100%.
	//
	// A delegation contains a prefix AND every prefix nested inside it, so
	// summing them all counts the same addresses at every level: 10.20.0.0/16
	// plus its four /24s is 66560 of a 65536-address block, which the live demo
	// duly reported as 101.6% used. Only the SHALLOWEST prefixes in the block
	// count -- a child's addresses are already inside its parent, exactly as an
	// address inside a child prefix is the child's and not the parent's.
	nodes := domain.BuildPrefixTree(flat, map[string]int{})
	byID := make(map[string]domain.PrefixNode, len(nodes))
	for _, n := range nodes {
		byID[n.ID] = n
	}

	for i := range rows {
		inside := func(n domain.PrefixNode) bool {
			return n.AddrFamily == rows[i].AddrFamily &&
				withinRange(n.AddrStart, n.AddrEnd, rows[i].AddrStart, rows[i].AddrEnd)
		}
		total := new(big.Int)
		count := 0
		for _, n := range nodes {
			if !inside(n) {
				continue
			}
			count++
			// Its parent is in the block too, so its addresses are already
			// counted there.
			if n.ParentID != nil {
				if parent, ok := byID[*n.ParentID]; ok && inside(parent) {
					continue
				}
			}
			if size := domain.PrefixSize(n.CIDRText); size != nil {
				total.Add(total, size)
			}
		}
		rows[i].Prefixes = count
		rows[i].Allocated = total
	}
	return rows, nil
}

// withinRange reports whether an inner range sits inside an outer one.
//
// THE WIDTH CHECK IS THE PROTECTION, and it is not defensive tidying. A 4-byte
// v4 range and a 16-byte v6 one compare byte by byte perfectly happily: the
// bytes of a64:100::/64 are 0a64 0100 …, which sit between the v4 aggregate
// 10.100.0.0/16's 0a640000 and 0a64ffff. Without this, a v6 /64 is counted
// inside a v4 /16 and the delegation reports a utilisation nobody would
// question. Mutating it fails the test.
//
// The family filter in the caller is a cheap early skip for the same case, not
// a second guarantee -- mutating THAT alone leaves the suite green, because
// this check still catches it. Said plainly so nobody deletes the wrong one.
func withinRange(innerStart, innerEnd, outerStart, outerEnd []byte) bool {
	if len(innerStart) != len(outerStart) || len(innerEnd) != len(outerEnd) {
		return false
	}
	return bytes.Compare(innerStart, outerStart) >= 0 && bytes.Compare(innerEnd, outerEnd) <= 0
}

// CreateAggregate declares a delegation.
func (s *SQLStore) CreateAggregate(ctx context.Context, p domain.Permit, a *domain.Aggregate) error {
	if err := a.Validate(); err != nil {
		return err
	}
	a.RowVersion = 1
	at := domain.FormatTime(s.now())
	a.CreatedAt, a.UpdatedAt = &at, &at
	return s.write(ctx, p, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO aggregate (id, cidr_text, addr_family, addr_start, addr_end,
			                       rir_id, allocated_on, description, lifecycle,
			                       created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.ID, a.CIDRText, a.AddrFamily, a.AddrStart, a.AddrEnd,
			a.RIRID, a.AllocatedOn, a.Description, a.Lifecycle, a.CreatedAt, a.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating aggregate")
		}
		if err := t.logCreate(ctx, "aggregate", a.ID, a); err != nil {
			return err
		}
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "aggregate", EntityID: a.ID,
			Title: a.CIDRText, Subtitle: "delegation", Body: a.CIDRText,
		})
	})
}

// GetAggregate loads one delegation by id.
func (s *SQLStore) GetAggregate(ctx context.Context, id string) (*domain.Aggregate, error) {
	var a domain.Aggregate
	if err := s.readOne(ctx, &a, `SELECT * FROM aggregate WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting aggregate %s: %w", id, err)
	}
	return &a, nil
}

// UpdateAggregate corrects a delegation's descriptive attributes: cidr_text
// (and the addr_* columns SetCIDR derives from it), rir_id, allocated_on and
// description.
//
// lifecycle IS PINNED FROM THE STORED ROW, never taken from the caller. This
// method would otherwise be a second withdrawal path with none of
// RetireAggregate's own gate, letting a correction form silently reactivate or
// retire a delegation as a side effect of fixing a typo in its allocation
// date -- the same reasoning UpdateVLAN and UpdateNetGroup give for pinning
// theirs.
func (s *SQLStore) UpdateAggregate(ctx context.Context, p domain.Permit, a *domain.Aggregate) error {
	before, err := s.GetAggregate(ctx, a.ID)
	if err != nil {
		return err
	}
	a.Lifecycle = before.Lifecycle
	a.CreatedAt = before.CreatedAt
	if err := a.Validate(); err != nil {
		return err
	}
	at := domain.FormatTime(s.now())
	a.UpdatedAt = &at

	return s.write(ctx, p, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE aggregate SET cidr_text = ?, addr_family = ?, addr_start = ?, addr_end = ?,
			                     rir_id = ?, allocated_on = ?, description = ?,
			                     updated_at = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			a.CIDRText, a.AddrFamily, a.AddrStart, a.AddrEnd,
			a.RIRID, a.AllocatedOn, a.Description, at, a.ID, a.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating aggregate")
		}
		if err := requireVersion(res, "aggregate", a.ID, &a.RowVersion); err != nil {
			return err
		}
		if err := t.logUpdate(ctx, "aggregate", a.ID, before, a); err != nil {
			return err
		}
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "aggregate", EntityID: a.ID,
			Title: a.CIDRText, Subtitle: "delegation", Body: a.CIDRText,
		})
	})
}

// RetireAggregate withdraws a delegation.
func (s *SQLStore) RetireAggregate(ctx context.Context, p domain.Permit, id string) error {
	var before domain.Aggregate
	if err := s.readOne(ctx, &before, `SELECT * FROM aggregate WHERE id = ?`, id); err != nil {
		return fmt.Errorf("getting aggregate %s: %w", id, err)
	}
	if before.Lifecycle == domain.LifecycleRetired {
		return nil
	}
	at := domain.FormatTime(s.now())
	after := before
	after.Lifecycle = domain.LifecycleRetired
	after.UpdatedAt = &at
	return s.write(ctx, p, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE aggregate SET lifecycle = 'retired', updated_at = ?,
			                     row_version = row_version + 1
			WHERE id = ? AND row_version = ?`, at, id, before.RowVersion)
		if err != nil {
			return translateWriteErr(err, "retiring aggregate")
		}
		if err := requireVersion(res, "aggregate", id, &before.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "aggregate", id, &before, &after)
	})
}

// ---------- registries ----------

// ListRIRs returns every live registry.
func (s *SQLStore) ListRIRs(ctx context.Context) ([]domain.RIR, error) {
	var rows []domain.RIR
	err := s.read(ctx, &rows,
		`SELECT * FROM rir WHERE lifecycle <> 'retired' ORDER BY is_private, name`)
	if err != nil {
		return nil, fmt.Errorf("listing registries: %w", err)
	}
	return rows, nil
}

// CreateRIR declares a registry.
func (s *SQLStore) CreateRIR(ctx context.Context, p domain.Permit, r *domain.RIR) error {
	if err := r.Validate(); err != nil {
		return err
	}
	r.RowVersion = 1
	at := domain.FormatTime(s.now())
	r.CreatedAt, r.UpdatedAt = &at, &at
	return s.write(ctx, p, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO rir (id, name, is_private, description, lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.Name, r.IsPrivate, r.Description, r.Lifecycle, r.CreatedAt, r.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating registry")
		}
		return t.logCreate(ctx, "rir", r.ID, r)
	})
}

// GetRIR loads one registry by id.
func (s *SQLStore) GetRIR(ctx context.Context, id string) (*domain.RIR, error) {
	var r domain.RIR
	if err := s.readOne(ctx, &r, `SELECT * FROM rir WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting registry %s: %w", id, err)
	}
	return &r, nil
}

// UpdateRIR corrects a registry's descriptive attributes: name, is_private
// and description.
//
// lifecycle IS PINNED FROM THE STORED ROW, never taken from the caller, for
// the reason UpdateAggregate's own comment gives: this method must not become
// a second, guardless withdrawal path. RetireRIR (Task 4) is the only thing
// allowed to withdraw one, and it refuses while a live aggregate still
// references it.
func (s *SQLStore) UpdateRIR(ctx context.Context, p domain.Permit, r *domain.RIR) error {
	before, err := s.GetRIR(ctx, r.ID)
	if err != nil {
		return err
	}
	r.Lifecycle = before.Lifecycle
	r.CreatedAt = before.CreatedAt
	if err := r.Validate(); err != nil {
		return err
	}
	at := domain.FormatTime(s.now())
	r.UpdatedAt = &at

	return s.write(ctx, p, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE rir SET name = ?, is_private = ?, description = ?,
			               updated_at = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			r.Name, r.IsPrivate, r.Description, at, r.ID, r.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating registry")
		}
		if err := requireVersion(res, "rir", r.ID, &r.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "rir", r.ID, before, r)
	})
}

// RetireRIR withdraws a registry.
//
// REFUSED, NOT CASCADED, for the reason RetirePrefix and RetireVLAN both give:
// a live aggregate naming this RIR is a fact somebody else declared -- an
// operator recorded that a specific delegation came from this registry -- and
// silently orphaning that record (or worse, silently retiring the aggregate
// underneath them) would write a change_log entry nobody asked for. The
// registry withdrawal has to come after the delegations it covers are gone,
// the same order retiring a prefix already requires of what lives inside it.
//
// Serializable, for the reason RetireVLAN's own comment gives: the COUNT below
// asserts an invariant this transaction is about to depend on, and at
// read-committed a live aggregate declared concurrently against this RIR is
// invisible to it.
func (s *SQLStore) RetireRIR(ctx context.Context, p domain.Permit, id string) error {
	before, err := s.GetRIR(ctx, id)
	if err != nil {
		return err
	}
	if before.Lifecycle == domain.LifecycleRetired {
		// Already withdrawn: a second audit entry would claim a withdrawal
		// that did not happen. RetireIdentity does the same.
		return nil
	}
	at := domain.FormatTime(s.now())
	after := *before
	after.Lifecycle = domain.LifecycleRetired
	after.UpdatedAt = &at

	return s.writeSerializable(ctx, p, func(t *tx) error {
		var aggregates int
		if err := t.get(ctx, &aggregates, `
			SELECT COUNT(*) FROM aggregate WHERE rir_id = ? AND lifecycle <> ?`,
			id, domain.LifecycleRetired); err != nil {
			return fmt.Errorf("checking registry %s for delegations: %w", id, err)
		}
		if aggregates > 0 {
			return fmt.Errorf("registry %s cannot be withdrawn while %d live aggregate(s) name it: %w",
				before.Name, aggregates, domain.ErrConflict)
		}

		res, err := t.exec(ctx, `
			UPDATE rir SET lifecycle = ?, updated_at = ?,
			               row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			domain.LifecycleRetired, at, id, before.RowVersion)
		if err != nil {
			return translateWriteErr(err, "retiring registry")
		}
		if err := requireVersion(res, "rir", id, &before.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "rir", id, before, &after)
	})
}

// ---------- autonomous systems ----------

// ASNRow is an AS number with its registry resolved.
type ASNRow struct {
	domain.ASN
	RIRName string `db:"rir_name"`
}

// IsPrivate reports whether the number falls in a private-use range.
func (a ASNRow) IsPrivate() bool { return domain.IsPrivateASN(a.Number) }

// ListASNs returns every live AS number.
func (s *SQLStore) ListASNs(ctx context.Context) ([]ASNRow, error) {
	var rows []ASNRow
	err := s.read(ctx, &rows, `
		SELECT a.*, COALESCE(r.name, '') AS rir_name
		FROM asn a
		LEFT JOIN rir r ON r.id = a.rir_id
		WHERE a.lifecycle <> 'retired'
		ORDER BY a.number`)
	if err != nil {
		return nil, fmt.Errorf("listing AS numbers: %w", err)
	}
	return rows, nil
}

// CreateASN declares an AS number.
func (s *SQLStore) CreateASN(ctx context.Context, p domain.Permit, a *domain.ASN) error {
	if err := a.Validate(); err != nil {
		return err
	}
	a.RowVersion = 1
	at := domain.FormatTime(s.now())
	a.CreatedAt, a.UpdatedAt = &at, &at
	return s.write(ctx, p, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO asn (id, number, name, rir_id, description, lifecycle,
			                 created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			a.ID, a.Number, a.Name, a.RIRID, a.Description, a.Lifecycle,
			a.CreatedAt, a.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating AS number")
		}
		if err := t.logCreate(ctx, "asn", a.ID, a); err != nil {
			return err
		}
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "asn", EntityID: a.ID,
			Title:    fmt.Sprintf("AS%d", a.Number),
			Subtitle: derefString(a.Name),
			Body:     fmt.Sprintf("AS%d %s", a.Number, derefString(a.Name)),
		})
	})
}

// GetASN loads one AS number by id.
func (s *SQLStore) GetASN(ctx context.Context, id string) (*domain.ASN, error) {
	var a domain.ASN
	if err := s.readOne(ctx, &a, `SELECT * FROM asn WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting AS number %s: %w", id, err)
	}
	return &a, nil
}

// UpdateASN corrects an AS number's declared attributes.
//
// THE NUMBER ITSELF IS CORRECTABLE, unlike a route's frontend or a pool's
// service: the roadmap entry this closes says it plainly -- "a mistyped AS
// number is withdraw-and-redeclare" -- and that IS the gap. An AS number is a
// value somebody typed, not an identity another row depends on the way a
// route depends on its pool, so there is no seizure to guard against.
//
// Uniqueness still applies: asn_number_key (migration 00033) is a partial
// unique index scoped to live rows, so this method can hand a number already
// declared by another live row straight to it rather than pre-checking and
// racing. What is NOT acceptable is surfacing the resulting constraint
// failure as a raw domain.ErrConflict, which a form has no field to hang off
// -- so a unique violation here is caught and turned into a *ValidationError
// naming "number", the same 422-able shape every other refused input takes.
//
// lifecycle IS PINNED FROM THE STORED ROW, for the reason every other Update
// method in this file pins theirs: RetireASN is the only withdrawal path, and
// it is the only one with a guard.
func (s *SQLStore) UpdateASN(ctx context.Context, p domain.Permit, a *domain.ASN) error {
	before, err := s.GetASN(ctx, a.ID)
	if err != nil {
		return err
	}
	a.Lifecycle = before.Lifecycle
	a.CreatedAt = before.CreatedAt
	if err := a.Validate(); err != nil {
		return err
	}
	at := domain.FormatTime(s.now())
	a.UpdatedAt = &at

	return s.write(ctx, p, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE asn SET number = ?, name = ?, rir_id = ?, description = ?,
			               updated_at = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			a.Number, a.Name, a.RIRID, a.Description, at, a.ID, a.RowVersion)
		if err != nil {
			if isUniqueViolation(err) {
				ve := &domain.ValidationError{}
				ve.Add("number", "AS%d is already declared by another AS number", a.Number)
				return ve
			}
			return translateWriteErr(err, "updating AS number")
		}
		if err := requireVersion(res, "asn", a.ID, &a.RowVersion); err != nil {
			return err
		}
		if err := t.logUpdate(ctx, "asn", a.ID, before, a); err != nil {
			return err
		}
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "asn", EntityID: a.ID,
			Title:    fmt.Sprintf("AS%d", a.Number),
			Subtitle: derefString(a.Name),
			Body:     fmt.Sprintf("AS%d %s", a.Number, derefString(a.Name)),
		})
	})
}

// RetireASN withdraws an AS number.
func (s *SQLStore) RetireASN(ctx context.Context, p domain.Permit, id string) error {
	var before domain.ASN
	if err := s.readOne(ctx, &before, `SELECT * FROM asn WHERE id = ?`, id); err != nil {
		return fmt.Errorf("getting AS number %s: %w", id, err)
	}
	if before.Lifecycle == domain.LifecycleRetired {
		return nil
	}
	at := domain.FormatTime(s.now())
	after := before
	after.Lifecycle = domain.LifecycleRetired
	after.UpdatedAt = &at
	return s.write(ctx, p, func(t *tx) error {
		res, err := t.exec(ctx, `
			UPDATE asn SET lifecycle = 'retired', updated_at = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`, at, id, before.RowVersion)
		if err != nil {
			return translateWriteErr(err, "retiring AS number")
		}
		if err := requireVersion(res, "asn", id, &before.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "asn", id, &before, &after)
	})
}
