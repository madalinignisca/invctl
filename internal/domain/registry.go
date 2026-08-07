// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"math/big"
	"strings"
)

// Where address space came from, and how much of it is spent.

// MinASN and MaxASN bound a 32-bit autonomous system number. 0 is reserved and
// 4294967295 is the last-resort AS.
const (
	MinASN = 1
	MaxASN = 4294967294
)

// RIR is a registry that delegates address space -- or, for RFC1918 and
// friends, the fact that nobody did.
type RIR struct {
	ID          string  `db:"id"`
	Name        string  `db:"name"`
	IsPrivate   bool    `db:"is_private"`
	Description *string `db:"description"`
	Lifecycle   string  `db:"lifecycle"`
	CreatedAt   *string `db:"created_at"`
	UpdatedAt   *string `db:"updated_at"`
	RowVersion  int     `db:"row_version"`
}

// NewRIR validates and constructs a registry.
func NewRIR(id, name string, isPrivate bool) (*RIR, error) {
	r := &RIR{ID: id, Name: strings.TrimSpace(name), IsPrivate: isPrivate, Lifecycle: LifecycleActive}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return r, nil
}

// Validate checks a registry.
func (r *RIR) Validate() error {
	ve := &ValidationError{}
	if strings.TrimSpace(r.Name) == "" {
		ve.Add("name", "a registry needs a name")
	}
	if r.Lifecycle != LifecycleActive && r.Lifecycle != LifecycleRetired {
		ve.Add("lifecycle", "%q is not a lifecycle", r.Lifecycle)
	}
	return ve.OrNil()
}

// Aggregate is a block a registry delegated.
//
// NOT A PREFIX, deliberately. A prefix is something you route and address hosts
// from; an aggregate is registry paperwork, and the useful question about it is
// how much has been carved out of it. Modelling it as a prefix would put the
// paperwork into the tree the allocator walks and offer somebody the first
// address of a /22 nobody has subnetted.
type Aggregate struct {
	ID          string  `db:"id"`
	CIDRText    string  `db:"cidr_text"`
	AddrFamily  int     `db:"addr_family"`
	AddrStart   []byte  `db:"addr_start"`
	AddrEnd     []byte  `db:"addr_end"`
	RIRID       *string `db:"rir_id"`
	AllocatedOn *string `db:"allocated_on"`
	Description *string `db:"description"`
	Lifecycle   string  `db:"lifecycle"`
	CreatedAt   *string `db:"created_at"`
	UpdatedAt   *string `db:"updated_at"`
	RowVersion  int     `db:"row_version"`
}

// NewAggregate parses a CIDR into the stored representation.
func NewAggregate(id, cidr string) (*Aggregate, error) {
	pv, err := ParsePrefix(cidr)
	if err != nil {
		ve := &ValidationError{}
		ve.Add("cidr_text", "%s", err.Error())
		return nil, ve
	}
	return &Aggregate{
		ID: id, CIDRText: pv.Text, AddrFamily: pv.Family,
		AddrStart: pv.Start, AddrEnd: pv.End, Lifecycle: LifecycleActive,
	}, nil
}

// SetCIDR reparses and rewrites all four columns together, for the reason
// Prefix.SetCIDR does.
func (a *Aggregate) SetCIDR(cidr string) error {
	pv, err := ParsePrefix(cidr)
	if err != nil {
		ve := &ValidationError{}
		ve.Add("cidr_text", "%s", err.Error())
		return ve
	}
	a.CIDRText, a.AddrFamily = pv.Text, pv.Family
	a.AddrStart, a.AddrEnd = pv.Start, pv.End
	return nil
}

// Validate checks an aggregate.
func (a *Aggregate) Validate() error {
	ve := &ValidationError{}
	if len(a.AddrStart) == 0 || len(a.AddrEnd) == 0 {
		ve.Add("cidr_text", "the aggregate has no range")
	}
	a.AllocatedOn = checkDate(ve, "allocated_on", a.AllocatedOn)
	if a.Lifecycle != LifecycleActive && a.Lifecycle != LifecycleRetired {
		ve.Add("lifecycle", "%q is not a lifecycle", a.Lifecycle)
	}
	return ve.OrNil()
}

// Size is how many addresses the aggregate covers.
func (a Aggregate) Size() *big.Int { return PrefixSize(a.CIDRText) }

// ASN is an autonomous system number.
type ASN struct {
	ID          string  `db:"id"`
	Number      int64   `db:"number"`
	Name        *string `db:"name"`
	RIRID       *string `db:"rir_id"`
	Description *string `db:"description"`
	Lifecycle   string  `db:"lifecycle"`
	CreatedAt   *string `db:"created_at"`
	UpdatedAt   *string `db:"updated_at"`
	RowVersion  int     `db:"row_version"`
}

// NewASN validates and constructs.
func NewASN(id string, number int64) (*ASN, error) {
	a := &ASN{ID: id, Number: number, Lifecycle: LifecycleActive}
	if err := a.Validate(); err != nil {
		return nil, err
	}
	return a, nil
}

// Validate checks the number.
func (a *ASN) Validate() error {
	ve := &ValidationError{}
	if a.Number < MinASN || a.Number > MaxASN {
		ve.Add("number", "an AS number is between %d and %d; 0 and 4294967295 are reserved",
			MinASN, MaxASN)
	}
	if a.Lifecycle != LifecycleActive && a.Lifecycle != LifecycleRetired {
		ve.Add("lifecycle", "%q is not a lifecycle", a.Lifecycle)
	}
	return ve.OrNil()
}

// IsPrivateASN reports whether a number falls in a reserved-for-private-use
// range: 64512-65534 for 16-bit and 4200000000-4294967294 for 32-bit.
//
// Worth knowing because a private ASN appearing in a route somebody is
// advertising to a transit provider is a misconfiguration, not a design.
func IsPrivateASN(n int64) bool {
	return (n >= 64512 && n <= 65534) || (n >= 4200000000 && n <= 4294967294)
}
