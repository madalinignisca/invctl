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
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Domain vocabularies: the seven lookup tables migration 00004 created.
//
// A vocabulary describes the estate rather than steering code. Nothing switches
// on a form factor or a container engine, so a new one is a fact to record, not
// a code path to write, and requiring a migration and a deploy to record that a
// rack now has 400G optics buys nothing. These tables exist so that adding a
// value is an INSERT.
//
// That only works if the value can then be *used*, which is why the readers
// below exist: a form that offers domain.FormFactors offers the Go slice, which
// is frozen at build time, and the newly inserted value would be storable but
// unofferable -- the change would look like it worked and be useless. Every
// form that names one of these columns feeds its <select> from here.
//
// The queries are full literals rather than a table name concatenated into a
// string. The table names are package constants and could not carry injection,
// but "the SQL a reader sees is the SQL that runs" is worth more than seven
// saved lines, and it keeps the SQL greppable.
const (
	vocabAssetKind           = "asset_kind"
	vocabServiceKind         = "service_kind"
	vocabInterfaceFormFactor = "interface_form_factor"
	vocabEnvironmentRole     = "environment_role"
	vocabIPAddressRole       = "ip_address_role"
	vocabDataClass           = "data_class"
	vocabContainerEngine     = "container_engine"
	vocabCostKind            = "cost_kind"
	vocabResponsibilityRole  = "responsibility_role"
	vocabStorageKind         = "storage_kind"
)

// VocabularyTerm is one row of a lookup table.
//
// Code is the value stored in the entity column and referenced by the foreign
// key; Label is what a person reads. SortOrder is the estate's own ordering --
// physical nesting for asset kinds, regulatory weight for data classes -- in
// steps of ten, so a new value lands between two existing ones without
// renumbering. Never sort these alphabetically: "site, rack, pdu, ..." is a
// containment order that an operator scans down, and the alphabet destroys it.
type VocabularyTerm struct {
	Code      string `db:"code"`
	Label     string `db:"label"`
	SortOrder int    `db:"sort_order"`
	// Description is what the term MEANS, in a sentence, for the person
	// looking at a dropdown who has to choose one. Seeded by migration and
	// editable, because these describe an estate's own conventions -- one
	// shop's `infra` is another's `platform`. Empty is legal: a term somebody
	// added by hand has no description until they write one.
	Description string `db:"description"`
}

// Codes flattens terms to their stored values.
func Codes(terms []VocabularyTerm) []string {
	out := make([]string, 0, len(terms))
	for _, t := range terms {
		out = append(out, t.Code)
	}
	return out
}

// vocabularyQueries is the one place a lookup table is named in SQL. Ordering
// is (sort_order, code) rather than sort_order alone so that two rows sharing a
// sort order -- which nothing prevents, since sort_order is not unique -- render
// in a stable order instead of whatever the engine's scan happens to produce.
var vocabularyQueries = map[string]string{
	vocabAssetKind:           `SELECT code, label, sort_order, description FROM asset_kind ORDER BY sort_order, code`,
	vocabServiceKind:         `SELECT code, label, sort_order, description FROM service_kind ORDER BY sort_order, code`,
	vocabInterfaceFormFactor: `SELECT code, label, sort_order, description FROM interface_form_factor ORDER BY sort_order, code`,
	vocabEnvironmentRole:     `SELECT code, label, sort_order, description FROM environment_role ORDER BY sort_order, code`,
	vocabIPAddressRole:       `SELECT code, label, sort_order, description FROM ip_address_role ORDER BY sort_order, code`,
	vocabDataClass:           `SELECT code, label, sort_order, description FROM data_class ORDER BY sort_order, code`,
	vocabContainerEngine:     `SELECT code, label, sort_order, description FROM container_engine ORDER BY sort_order, code`,
	vocabCostKind:            `SELECT code, label, sort_order, description FROM cost_kind ORDER BY sort_order, code`,
	vocabResponsibilityRole:  `SELECT code, label, sort_order, description FROM responsibility_role ORDER BY sort_order, code`,
	vocabStorageKind:         `SELECT code, label, sort_order, description FROM storage_kind ORDER BY sort_order, code`,
}

// StorageKinds returns the storage replication vocabulary in display order.
//
// The ratio is deliberately NOT carried here: VocabularyTerm is the shape every
// picker in the product renders, and a field only one of them uses would be
// blank on nine others. A pool resolves its own ratio through the join in
// ListStoragePools, which is where the number is actually needed.
func (s *SQLStore) StorageKinds(ctx context.Context) ([]VocabularyTerm, error) {
	return s.listVocabulary(ctx, vocabStorageKind)
}

// AssetKinds returns the asset.kind vocabulary in display order.
func (s *SQLStore) AssetKinds(ctx context.Context) ([]VocabularyTerm, error) {
	return s.listVocabulary(ctx, vocabAssetKind)
}

// ServiceKinds returns the service.kind vocabulary in display order.
func (s *SQLStore) ServiceKinds(ctx context.Context) ([]VocabularyTerm, error) {
	return s.listVocabulary(ctx, vocabServiceKind)
}

// InterfaceFormFactors returns the interface.form_factor vocabulary in display
// order.
func (s *SQLStore) InterfaceFormFactors(ctx context.Context) ([]VocabularyTerm, error) {
	return s.listVocabulary(ctx, vocabInterfaceFormFactor)
}

// EnvironmentRoles returns the environment.role vocabulary in display order.
func (s *SQLStore) EnvironmentRoles(ctx context.Context) ([]VocabularyTerm, error) {
	return s.listVocabulary(ctx, vocabEnvironmentRole)
}

// IPAddressRoles returns the ip_address.role vocabulary in display order.
func (s *SQLStore) IPAddressRoles(ctx context.Context) ([]VocabularyTerm, error) {
	return s.listVocabulary(ctx, vocabIPAddressRole)
}

// DataClassVocabulary returns the dependency_data_class.data_class vocabulary
// in display order.
//
// Named for the vocabulary rather than DataClasses, which is already taken by
// DataClassesFor's sibling reading of which classes a given edge carries. The
// two answer different questions and confusing them on a PCI scope report would
// be a bad afternoon.
func (s *SQLStore) DataClassVocabulary(ctx context.Context) ([]VocabularyTerm, error) {
	return s.listVocabulary(ctx, vocabDataClass)
}

// ContainerEngines returns the rt_container.engine vocabulary in display order.
//
// No form offers it today -- runtime detail is written by the seeder and, later,
// by a discovery agent. It is here because the column is a FOREIGN KEY like the
// other six, and the reader is what makes the next writer able to offer the
// current list instead of hardcoding two values.
func (s *SQLStore) ContainerEngines(ctx context.Context) ([]VocabularyTerm, error) {
	return s.listVocabulary(ctx, vocabContainerEngine)
}

// CostKinds lists the kinds of spend. Descriptive rather than behavioural: no
// code branches on which kind a line is, so a new one is data. The PERIOD is the
// half the code reads, and it is a CHECK with a Go constant set.
func (s *SQLStore) CostKinds(ctx context.Context) ([]VocabularyTerm, error) {
	return s.listVocabulary(ctx, vocabCostKind)
}

func (s *SQLStore) listVocabulary(ctx context.Context, table string) ([]VocabularyTerm, error) {
	query, ok := vocabularyQueries[table]
	if !ok {
		return nil, fmt.Errorf("reading vocabulary: %q is not a lookup table", table)
	}
	var terms []VocabularyTerm
	if err := s.read(ctx, &terms, query); err != nil {
		return nil, fmt.Errorf("reading the %s vocabulary: %w", table, err)
	}
	return terms, nil
}

// requireVocabulary asserts that a value exists in its lookup table, inside the
// transaction that is about to store it.
//
// The FOREIGN KEY is the authority and this does not replace it -- the FK still
// runs, and it is what closes the window between this read and the INSERT. What
// this buys is the error. SQLite reports a violated FK as "FOREIGN KEY
// constraint failed" with no column in it, so translateWriteErr can only turn
// that into a bare 422 with no field and no message; a form submitted with an
// unknown value would come back as "That request was not valid" with nothing
// highlighted. Reading the table first gives the operator the same field-level
// error they had when this was a CHECK, listing the values that exist *now*.
//
// This is not the Go slice coming back in another form. The list is read from
// the same table the FK points at, on every call, so a value inserted a second
// ago is accepted a second later -- which is the entire point of the change.
func (t *tx) requireVocabulary(ctx context.Context, table, field, value string) error {
	query, ok := vocabularyQueries[table]
	if !ok {
		return fmt.Errorf("checking vocabulary: %q is not a lookup table", table)
	}
	var terms []VocabularyTerm
	if err := t.tx.SelectContext(ctx, &terms, t.rebind(query)); err != nil {
		return fmt.Errorf("reading the %s vocabulary: %w", table, err)
	}
	codes := Codes(terms)
	for _, code := range codes {
		if code == value {
			return nil
		}
	}
	ve := &domain.ValidationError{}
	ve.Add(field, "must be one of %s", strings.Join(codes, ", "))
	return ve
}

// KindBehaviour is what an asset kind is allowed to do.
//
// The two flags travel with the vocabulary row rather than living in a Go
// switch, and that is the whole difference between a vocabulary that is
// extensible and one that merely looks it. Before these columns existed, a kind
// added by INSERT was storable and then silently non-hosting and
// non-attachable: 'bridge' could be recorded and could then carry nothing and
// take no network attachment, which is precisely the case the lookup tables
// were introduced to serve.
//
// An unknown kind reads as all-false rather than erroring. It cannot be
// inserted -- the foreign key sees to that -- so the only way to reach here
// with one is a row that predates its own vocabulary entry, and answering
// "this may not host anything" is the safe reading of that.
type KindBehaviour struct {
	CanHostInstances bool `db:"can_host_instances"`
	IsAttachable     bool `db:"is_attachable"`
}

// AssetKindBehaviour reads what a kind may do.
func (s *SQLStore) AssetKindBehaviour(ctx context.Context, kind string) (KindBehaviour, error) {
	var b KindBehaviour
	err := s.readOne(ctx, &b,
		`SELECT can_host_instances, is_attachable FROM asset_kind WHERE code = ?`, kind)
	if errors.Is(err, domain.ErrNotFound) {
		return KindBehaviour{}, nil
	}
	if err != nil {
		return KindBehaviour{}, fmt.Errorf("reading behaviour of asset kind %q: %w", kind, err)
	}
	return b, nil
}

// RoleIsTransit reports whether an environment role brokers between segments.
//
// A transit zone's entire purpose is to sit between other environments, so an
// asset in one is not a segmentation exception. Hardcoding the literal
// 'transit' meant a brokering role added as data turned every asset spanning it
// into a false positive on the one report whose job is finding real exceptions.
func (s *SQLStore) RoleIsTransit(ctx context.Context, role string) (bool, error) {
	var transit bool
	err := s.readOne(ctx, &transit,
		`SELECT is_transit FROM environment_role WHERE code = ?`, role)
	if errors.Is(err, domain.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("reading transit flag of environment role %q: %w", role, err)
	}
	return transit, nil
}

// ---------------------------------------------------------------------------
// Writing vocabularies
//
// A vocabulary term is DECLARED state -- somebody decided the estate should
// have this word -- so every mutation writes a change_log row in the same
// transaction, exactly like an asset or a service. That is not ceremony: these
// rows are foreign keys, so adding or rewording one changes what the estate
// can express, and "who added `vrf` and when" is a question an audit will ask.
//
// There is no delete. A term in use is protected by its foreign keys anyway,
// and a term not in use costs nothing but a row -- while removing one silently
// breaks any saved filter or bookmark that named it. Retiring vocabulary is a
// bigger design question than this POC needs to answer.

// VocabularyTables names every lookup table a caller may write to. Only these
// seven exist, and the map is the allow-list: a table name never comes from a
// request, it comes from here.
func VocabularyTables() []string {
	names := make([]string, 0, len(vocabularyQueries))
	for name := range vocabularyQueries {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// UpsertVocabularyTerm adds a term or updates the label, order and description
// of an existing one.
//
// The code is the primary key and is never rewritten: changing it would orphan
// every row pointing at it, and the foreign keys would refuse anyway. Somebody
// who wants a different code is adding a term, not editing one.
func (s *SQLStore) UpsertVocabularyTerm(ctx context.Context, p domain.Permit, table string, term VocabularyTerm) error {
	if _, ok := vocabularyQueries[table]; !ok {
		return fmt.Errorf("writing vocabulary: %q is not a lookup table: %w", table, domain.ErrInvalid)
	}
	term.Code = strings.ToLower(strings.TrimSpace(term.Code))
	term.Label = strings.TrimSpace(term.Label)
	term.Description = strings.TrimSpace(term.Description)
	// A *ValidationError, not a wrapped sentinel. validationErrors() extracts
	// field messages with errors.As and only recognises this type; a plain
	// wrapped ErrInvalid falls through to handleStoreError, which answers 422
	// with the bare string "That request was not valid." -- no form, no field
	// highlighted, and whatever was typed lost. The template already has the
	// per-field hooks; they were unreachable from this path. Found by review.
	ve := &domain.ValidationError{}
	if term.Code == "" {
		ve.Add("code", "is required")
	}
	if term.Label == "" {
		ve.Add("label", "is required")
	}
	if err := ve.OrNil(); err != nil {
		return err
	}

	return s.write(ctx, p, func(t *tx) error {
		var before VocabularyTerm
		err := t.get(ctx, &before,
			`SELECT code, label, sort_order, description FROM `+table+` WHERE code = ?`, term.Code)
		isNew := errors.Is(err, sql.ErrNoRows)
		if err != nil && !isNew {
			return fmt.Errorf("reading the existing term: %w", err)
		}

		if isNew {
			if _, err := t.exec(ctx,
				`INSERT INTO `+table+` (code, label, sort_order, description) VALUES (?, ?, ?, ?)`,
				term.Code, term.Label, term.SortOrder, term.Description); err != nil {
				return translateWriteErr(err, "adding a vocabulary term")
			}
			return t.logCreate(ctx, table, term.Code, term)
		}

		if _, err := t.exec(ctx,
			`UPDATE `+table+` SET label = ?, sort_order = ?, description = ? WHERE code = ?`,
			term.Label, term.SortOrder, term.Description, term.Code); err != nil {
			return translateWriteErr(err, "updating a vocabulary term")
		}
		return t.logUpdate(ctx, table, term.Code, before, term)
	})
}
