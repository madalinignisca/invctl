package domain

import (
	"strings"
	"time"
)

// Who looks after a thing, and in what capacity.
//
// TEAMS, NEVER PEOPLE. Nothing in this model names an individual, and that is a
// constraint rather than an omission. A CMDB is kept for the life of an estate
// and its change_log is append-only, so anything personal recorded here is
// effectively unerasable -- the same argument that made change_log.actor an
// opaque app_user id rather than a username. A team survives the people in it,
// which is also why it is the more useful answer at three in the morning.
//
// TWO AXES. TeamID says WHO; ManagerRole says IN WHAT CAPACITY. A role without a
// team is not expressible, and that is deliberate: a capacity floating free of
// the group that holds it means nothing.
//
// ROLE IS DOCUMENTATION, NOT AUTHORIZATION. "Who can manage it" means who to
// call. Nothing reads ManagerRole to decide anything and authz.CanWrite does not
// consult it. When LDAP group roles arrive this is where they will look, which
// is a reason to record it accurately and none at all to enforce it now.

// Team is a group answerable for part of the estate.
type Team struct {
	ID   string `db:"id"`
	Code string `db:"code"`
	Name string `db:"name"`
	// Description is prose for a human deciding whether this is the team they
	// meant. Not parsed, never queried.
	Description *string `db:"description"`
	// ContactRef is a GROUP address, a ticket queue or a channel -- a mailing
	// list, #platform-oncall, QUEUE-INFRA. Never an individual.
	//
	// This package cannot tell a distribution list from a person's mailbox and
	// does not try; a validator that guessed would be wrong in both directions.
	// The rule lives in the form hint, the help text and docs/AUDIT.md, the same
	// way identity.secret_ref is documented to hold a path and never a secret.
	ContactRef *string `db:"contact_ref"`
	Lifecycle  string  `db:"lifecycle"`
	CreatedAt  string  `db:"created_at"`
	UpdatedAt  string  `db:"updated_at"`
	// RowVersion is the optimistic-concurrency token; see version.go.
	RowVersion int `db:"row_version"`
}

// TeamLifecycles reuses the estate-wide set, so a team reads the way an asset
// does and the existing lifecycle help topic covers it.
var TeamLifecycles = []string{
	LifecyclePlanned, LifecycleActive, LifecycleDeprecated, LifecycleRetired,
}

// TeamSpec is what a caller supplies.
type TeamSpec struct {
	Code        string
	Name        string
	Description *string
	ContactRef  *string
	Lifecycle   string
}

// NewTeam validates and constructs.
func NewTeam(id string, spec TeamSpec, now time.Time) (*Team, error) {
	ve := &ValidationError{}
	code := strings.ToLower(strings.TrimSpace(spec.Code))
	code = checkRequired(ve, "code", code)
	name := checkRequired(ve, "name", spec.Name)

	lifecycle := strings.TrimSpace(spec.Lifecycle)
	if lifecycle == "" {
		lifecycle = LifecycleActive
	}
	if !containsString(TeamLifecycles, lifecycle) {
		ve.Add("lifecycle", "must be one of %s", strings.Join(TeamLifecycles, ", "))
	}
	if err := ve.OrNil(); err != nil {
		return nil, err
	}

	return &Team{
		ID:          id,
		Code:        code,
		Name:        name,
		Description: spec.Description,
		ContactRef:  spec.ContactRef,
		Lifecycle:   lifecycle,
		CreatedAt:   FormatTime(now),
		UpdatedAt:   FormatTime(now),
	}, nil
}

// IsRetired reports whether this team has been disbanded.
func (t *Team) IsRetired() bool { return t.Lifecycle == LifecycleRetired }

// Validate re-checks a team after field updates.
func (t *Team) Validate() error {
	ve := &ValidationError{}
	t.Code = strings.ToLower(checkRequired(ve, "code", t.Code))
	t.Name = checkRequired(ve, "name", t.Name)
	checkEnum(ve, "lifecycle", t.Lifecycle, TeamLifecycles)
	return ve.OrNil()
}

// Responsibility is the pair as a page renders it: who, and in what capacity.
// Either half may be absent, and the absence is a real answer -- "nobody wrote
// it down" -- rather than a gap to be filled with a default.
type Responsibility struct {
	TeamID      *string `db:"team_id"`
	ManagerRole *string `db:"manager_role"`
}

// checkResponsibility validates the pair in place.
//
// A role WITHOUT a team is rejected: it says a capacity is held and refuses to
// say by whom, which reads as recorded when it is not. A team without a role is
// fine and common -- most estates know whose box it is long before they have
// agreed who in the team answers for it.
func checkResponsibility(ve *ValidationError, teamID, managerRole *string) (*string, *string) {
	teamID = blankToNil(teamID)
	managerRole = blankToNil(managerRole)
	if managerRole != nil {
		*managerRole = strings.ToLower(strings.TrimSpace(*managerRole))
		if teamID == nil {
			ve.Add("manager_role", "needs a team: a role says who in a team is "+
				"answerable, so it means nothing on its own")
		}
	}
	return teamID, managerRole
}

// blankToNil turns an empty form field into an absent value. A cleared select
// posts "", which is the absence of a choice rather than a choice of nothing.
func blankToNil(v *string) *string {
	if v == nil || strings.TrimSpace(*v) == "" {
		return nil
	}
	trimmed := strings.TrimSpace(*v)
	return &trimmed
}
