// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"strings"
	"time"
)

// A project is the business view of the estate: who owns what, and who is
// standing on somebody else's work.
//
// Everything else in this model answers a technical question -- an environment
// says where a thing runs, containment says what it sits inside, a dependency
// says what it needs. None of them answers "what does this project consist
// of", which is the question a product owner, a CTO or a budget holder asks,
// and it is deliberately a different shape: a project cuts ACROSS environments
// and racks rather than nesting inside them.

// Project relations. Two, and the asymmetry between them is the point.
const (
	// ProjectOwns: the thing exists FOR this project. At most one project may
	// own a given asset or service -- enforced by a partial unique index, not
	// by hope -- so it is the anchor a later cost model attributes 100% of a
	// thing's cost to. Ownership is also what the derived footprint follows:
	// what is inside an owned asset, and what runs on it, is this project's
	// concern.
	ProjectOwns = "owns"
	// ProjectUses: the project depends on it and shares it with others. Any
	// number of projects may use one thing, and NOTHING is derived from a
	// `uses` link -- what is inside somebody else's hypervisor is their
	// footprint, not yours.
	ProjectUses = "uses"
)

// ProjectRelations is the Go side of the project_asset.relation and
// project_service.relation CHECK constraints.
var ProjectRelations = []string{ProjectOwns, ProjectUses}

// ProjectLifecycles reuses the estate-wide set, so a project reads the same
// way an asset does and the existing lifecycle help topic covers it.
var ProjectLifecycles = []string{
	LifecyclePlanned, LifecycleActive, LifecycleDeprecated, LifecycleRetired,
}

// Project is a business grouping of assets and services.
type Project struct {
	ID   string `db:"id"`
	Code string `db:"code"`
	Name string `db:"name"`
	// Description is prose for a human deciding whether this is the project
	// they meant. Not a vocabulary, not parsed, never queried.
	Description *string `db:"description"`
	TeamID      *string `db:"team_id"`
	Lifecycle   string  `db:"lifecycle"`
	CreatedAt   string  `db:"created_at"`
	UpdatedAt   string  `db:"updated_at"`
	// RowVersion is the optimistic-concurrency token; see version.go.
	RowVersion int `db:"row_version"`
}

// ProjectSpec is what a caller supplies to create or update a project.
type ProjectSpec struct {
	Code        string
	Name        string
	Description *string
	TeamID      *string
	Lifecycle   string
}

// NewProject validates and constructs a project.
func NewProject(id string, spec ProjectSpec, now time.Time) (*Project, error) {
	ve := &ValidationError{}
	code := strings.ToLower(strings.TrimSpace(spec.Code))
	code = checkRequired(ve, "code", code)
	name := checkRequired(ve, "name", spec.Name)

	lifecycle := strings.TrimSpace(spec.Lifecycle)
	if lifecycle == "" {
		lifecycle = LifecycleActive
	}
	if !containsString(ProjectLifecycles, lifecycle) {
		ve.Add("lifecycle", "must be one of %s", strings.Join(ProjectLifecycles, ", "))
	}
	if err := ve.OrNil(); err != nil {
		return nil, err
	}

	return &Project{
		ID:          id,
		Code:        code,
		Name:        name,
		Description: spec.Description,
		TeamID:      blankToNil(spec.TeamID),
		Lifecycle:   lifecycle,
		CreatedAt:   FormatTime(now),
		UpdatedAt:   FormatTime(now),
	}, nil
}

// IsRetired reports whether this project has been retired.
func (p *Project) IsRetired() bool { return p.Lifecycle == LifecycleRetired }

// ProjectAssetLink joins a project to an asset with one of the two relations.
type ProjectAssetLink struct {
	ProjectID string  `db:"project_id"`
	AssetID   string  `db:"asset_id"`
	Relation  string  `db:"relation"`
	Note      *string `db:"note"`
	Lifecycle string  `db:"lifecycle"`
	CreatedAt string  `db:"created_at"`
	UpdatedAt string  `db:"updated_at"`
}

// NewProjectAssetLink validates and constructs the link.
func NewProjectAssetLink(projectID, assetID, relation string, note *string, now time.Time) (*ProjectAssetLink, error) {
	ve := &ValidationError{}
	checkRequired(ve, "project_id", projectID)
	checkRequired(ve, "asset_id", assetID)
	relation = checkRelation(ve, relation)
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return &ProjectAssetLink{
		ProjectID: projectID, AssetID: assetID, Relation: relation, Note: note,
		Lifecycle: LifecycleActive,
		CreatedAt: FormatTime(now), UpdatedAt: FormatTime(now),
	}, nil
}

// ProjectServiceLink joins a project to a service with one of the two
// relations.
type ProjectServiceLink struct {
	ProjectID string  `db:"project_id"`
	ServiceID string  `db:"service_id"`
	Relation  string  `db:"relation"`
	Note      *string `db:"note"`
	Lifecycle string  `db:"lifecycle"`
	CreatedAt string  `db:"created_at"`
	UpdatedAt string  `db:"updated_at"`
}

// NewProjectServiceLink validates and constructs the link.
func NewProjectServiceLink(projectID, serviceID, relation string, note *string, now time.Time) (*ProjectServiceLink, error) {
	ve := &ValidationError{}
	checkRequired(ve, "project_id", projectID)
	checkRequired(ve, "service_id", serviceID)
	relation = checkRelation(ve, relation)
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return &ProjectServiceLink{
		ProjectID: projectID, ServiceID: serviceID, Relation: relation, Note: note,
		Lifecycle: LifecycleActive,
		CreatedAt: FormatTime(now), UpdatedAt: FormatTime(now),
	}, nil
}

// ProjectCircuitLink joins a project to a circuit with one of the two
// relations.
//
// THE THIRD LINK, AND IT EXISTS BECAUSE A NUMBER WAS WRONG. The project cost
// rollup gathered lines from assets and services and stopped; circuits carry a
// monthly rate and an install fee, and nothing said which project a circuit
// served. Every project depending on connectivity reported less than it cost.
//
// `uses` is the ordinary relation here, more so than for assets: one transit
// circuit commonly serves everything in a rack. `owns` is what puts the cost in
// a project's Own total, and the partial unique index in migration 00041 allows
// only one owner -- two would land the same monthly rate in two rollups.
type ProjectCircuitLink struct {
	ProjectID string  `db:"project_id"`
	CircuitID string  `db:"circuit_id"`
	Relation  string  `db:"relation"`
	Note      *string `db:"note"`
	Lifecycle string  `db:"lifecycle"`
	CreatedAt string  `db:"created_at"`
	UpdatedAt string  `db:"updated_at"`
}

// NewProjectCircuitLink validates and constructs the link.
func NewProjectCircuitLink(projectID, circuitID, relation string, note *string, now time.Time) (*ProjectCircuitLink, error) {
	ve := &ValidationError{}
	checkRequired(ve, "project_id", projectID)
	checkRequired(ve, "circuit_id", circuitID)
	relation = checkRelation(ve, relation)
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return &ProjectCircuitLink{
		ProjectID: projectID, CircuitID: circuitID, Relation: relation, Note: note,
		Lifecycle: LifecycleActive,
		CreatedAt: FormatTime(now), UpdatedAt: FormatTime(now),
	}, nil
}

// checkRelation normalises and validates a relation for either link type.
// Shared because two constructors disagreeing about what `owns` means is the
// kind of drift that only shows up as a cost report nobody can reconcile.
func checkRelation(ve *ValidationError, relation string) string {
	relation = strings.ToLower(strings.TrimSpace(relation))
	if relation == "" {
		ve.Add("relation", "is required")
		return relation
	}
	if !containsString(ProjectRelations, relation) {
		ve.Add("relation", "must be one of %s", strings.Join(ProjectRelations, ", "))
	}
	return relation
}
