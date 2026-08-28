// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Package handlers holds one file per resource.
package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/alexedwards/scs/v2"
	"github.com/justinas/nosurf"

	"github.com/madalinignisca/invctl/internal/auth"
	"github.com/madalinignisca/invctl/internal/config"
	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/version"
	"github.com/madalinignisca/invctl/internal/web/middleware"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// App carries everything the handlers need.
type App struct {
	Store    *store.SQLStore
	Render   *render.Renderer
	Sessions *scs.SessionManager
	Auth     auth.Authenticator
	Authz    *auth.Authorizer
	Config   *config.Config
	// Agents is the configured credential list, used to show a credential that
	// has never checked in. Without it the reporters panel is built purely from
	// rows that exist, so a collector provisioned and never deployed -- or one
	// removed and later pruned -- is simply absent, and a panel whose whole job
	// is "a dead collector is one alertable event" reads as a covered estate.
	Agents *auth.AgentRegistry

	// imports is the background import queue, created lazily so a handler set
	// built without one still works -- a test that never imports should not have
	// to start a worker.
	imports     *importRunner
	importsOnce sync.Once
}

// importer returns the runner, starting it on first use.
func (a *App) importer() *importRunner {
	a.importsOnce.Do(func() { a.imports = newImportRunner(a.Store) })
	return a.imports
}

// Flash is a one-shot message shown after a mutation.
type Flash struct {
	Kind string // "success" | "error" | "info"
	Text string
}

// Base is the data every page needs. Page structs embed it.
type Base struct {
	Title string
	Nav   string
	// Version is the build in service, rendered in the rail's footer for
	// signed-in users. A constant for the process's lifetime, so it is set here
	// rather than plumbed through every page struct.
	Version string
	// CSVLink is this exact page as a download: the same path and the same
	// query with format=csv added.
	//
	// BUILT FROM THE REQUEST rather than assembled in each template, because a
	// hand-built link is a link that forgets a filter -- and an export that
	// silently ignores the filters is the worst kind of wrong, since the file
	// looks right. Empty on a page that offers no export; the template checks.
	CSVLink string
	// NavGroups is the rail, with the group holding this page already open.
	// Built per request rather than being a package-level value the layout
	// reaches for, so two concurrent requests cannot race on which section is
	// expanded. See nav.go for why the grouping lives in Go at all.
	NavGroups []NavGroup
	User      *domain.AppUser
	CanWrite  bool
	// permit backs CanWriteEntity below -- the request's own write permit,
	// resolved once by App.entityPermit and carried on Base rather than
	// exposed directly, so a template can only ever reach it through the
	// Covers-shaped method, never by type-asserting or otherwise widening
	// what it authorizes. Unexported: html/template only ever needs the
	// method, and an unexported field does not stop html/template calling
	// an exported method on the value that holds it (see CanWriteEntity).
	permit domain.Permit
	// IsAdmin is narrower than CanWrite -- see Authorizer.IsAdministrator's
	// doc comment for why the two must stay separate. Used wherever a page
	// decides whether to show a value rather than whether to accept a write,
	// starting with identity.secret_ref on the service page (WP-G1 Task 5).
	IsAdmin bool
	// CanSeeCosts is CanWrite's read-only sibling for money. Since WP-G1 Task
	// 4 it is true for Administrators unconditionally, and for everyone else
	// only when app_user.can_see_costs is granted (docs/rbac-design.md §3) --
	// the whole point of routing every check through Authorizer.CanSeeCosts is
	// that this change was its function body, not every template that renders
	// an amount.
	CanSeeCosts bool
	CSRF        string
	Flash       *Flash
	// EditRow is the id of the one row the operator opened for editing.
	//
	// ONE PARAMETER FOR EVERY EDITOR ON A PAGE, deliberately. The asset page
	// alone offers ports, addresses and cost lines; they share `?edit=<id>`
	// because ids are UUIDv7 and unique across tables, so an id names exactly
	// one row wherever it appears. Splitting it into ?edit_port=/?edit_cost=
	// would look tidier and break every URL bookmarked or tested against today
	// for no gain. Each template still checks CanWrite before comparing.
	EditRow string
	// RepriceRow is the id of the one cost line opened for repricing (WP-J2).
	// Separate from EditRow because they are different verbs on the same row and
	// one field would make them mutually exclusive by accident rather than by
	// decision -- opening "reprice" would silently close an edit in progress.
	RepriceRow string
}

// CanWriteEntity answers "may THIS person write THIS row", for the three
// entity types a project owner can ever own (docs/rbac-design.md §4): asset,
// service, circuit. CanWrite keeps meaning "may this person write at all"
// and is exactly right for an Administrator and an Observer; for a project
// owner it is uniformly false today (WP-G1 Task 13 has not landed) even
// though they DO own specific rows, so a template deciding whether to show
// an asset's own Edit button must ask this instead -- see WP-G1 Task 17.
// Every other control (estate config, topology) keeps asking .CanWrite
// unchanged; converting a control that is not entity-specific to this
// method would be wrong, not merely unnecessary, because CanWriteEntity
// only ever recognises "asset", "service" and "circuit" -- anything else
// asks the underlying permit's Covers, which denies every other entity type
// for a project owner by construction (domain.ScopedPermit.Covers).
//
// A METHOD, NOT A FUNC-TYPED FIELD -- deliberately, and not a style choice.
// html/template's field syntax never invokes a function-valued struct
// field with arguments (`{{.Field "a" "b"}}` on a field, as opposed to a
// method, fails to parse as a call at all -- proved the hard way, by every
// page in this suite 500ing the first time this was tried as a field). A
// method on the dot's type is the one shape text/template actually calls
// with arguments.
//
// BACKED BY THE REQUEST'S OWN RESOLVED PERMIT, not a fresh lookup: b.permit
// is set once by App.entityPermit when Base is built. Nothing here touches
// the database -- resolving per call, from inside a list template's
// {{range}}, would turn a 500-row list into 2000 queries
// (auth.Authorizer.Permit runs up to four per call for a project owner). A
// nil permit (Base built directly by a test rather than through a.base)
// answers false rather than panicking, the same nil-safety every other
// accessor on this page (e.g. editState's) already promises a template.
func (b Base) CanWriteEntity(entityType, id string) bool {
	if b.permit == nil {
		return false
	}
	return b.permit.Covers(entityType, id)
}

// submittedVersion reads the optimistic-concurrency token out of a form.
//
// THE POINT IS THAT IT COMES FROM THE FORM. The handler has just read the row,
// so using that row's version would compare it against itself and detect
// nothing; the token has to be the one rendered into the page the operator was
// looking at. See internal/domain/version.go.
//
// A form with no token falls back to the stored value, which means no
// protection rather than a refusal -- the same reasoning as submittedString: a
// field that failed to render must not break a save. That is only safe because
// TestEveryEditFormCarriesItsVersion enumerates the forms and fails if one
// stops emitting it, so the fallback cannot quietly become the normal path.
func submittedVersion(r *http.Request, stored int) int {
	raw := formValue(r, domain.VersionField)
	if raw == "" {
		return stored
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return stored
	}
	return n
}

// editState is a rejected inline edit on its way back to the page it came from.
//
// The house rule is 422 with the form re-rendered in error state, and an inline
// row editor has to honour it the same as a standalone form: the row reopens,
// showing WHAT THE OPERATOR TYPED rather than what is stored, with the message
// against the field it belongs to. Reopening with the stored values instead is
// the failure mode this exists to prevent -- the operator sees the old number
// where they just typed a new one and cannot tell whether it saved.
//
// Nil means nothing was rejected, and every accessor is nil-safe so a template
// can call it unconditionally.
type editState struct {
	ID     string
	Errors map[string]string
	Values map[string]string
	// Multi holds fields that repeat -- a set of ticked checkboxes rather than
	// one value. Kept separate because the fallback rules differ: a missing
	// single field means "not rendered", a missing set means "all unticked".
	Multi map[string][]string
}

// Value returns the submitted value for a field, or the stored one.
func (e *editState) Value(field, stored string) string {
	if e == nil {
		return stored
	}
	if v, ok := e.Values[field]; ok {
		return v
	}
	return stored
}

// Checked is Value for a checkbox. An unticked box submits NOTHING, so a
// missing key on a rejected form means false, not "fall back to stored" --
// otherwise unticking a box and failing validation elsewhere would silently
// re-tick it.
func (e *editState) Checked(field string, stored bool) bool {
	if e == nil || e.Values == nil {
		return stored
	}
	return e.Values[field] != ""
}

// Err returns the message against one field, if any.
func (e *editState) Err(field string) string {
	if e == nil {
		return ""
	}
	return e.Errors[field]
}

// rejected builds the state for a form that was refused, capturing exactly the
// fields the caller names. Only named fields are captured: a form post carries
// the CSRF token too, and nothing rejected should be echoed back untouched.
func rejected(r *http.Request, id string, errs map[string]string, fields ...string) *editState {
	values := make(map[string]string, len(fields)+1)
	for _, f := range fields {
		values[f] = formValue(r, f)
	}
	// ALWAYS the version, whatever the caller listed. A refused form that
	// redraws with a FRESH token turns the next click of Save into a blind
	// force-overwrite of the very edit the operator was just warned about --
	// and the message tells them to go and read that edit first, which the
	// mechanics would have made optional. Keeping the stale token means a
	// resubmit is refused again until they actually reopen the row.
	//
	// Harmless on an ordinary validation failure: nothing moved, so the token
	// they sent is still the current one and the corrected save goes through.
	values[domain.VersionField] = formValue(r, domain.VersionField)
	return &editState{ID: id, Errors: orEmpty(errs), Values: values}
}

// serviceSpec rebuilds what the operator typed into a service form. Only used
// to redraw a refused submission, so a field the form does not carry is simply
// absent rather than cleared.
func (e *editState) serviceSpec() *domain.ServiceSpec {
	if e == nil {
		return nil
	}
	// A value that will not parse cannot be shown in an int field, so the
	// caller refuses BEFORE redrawing rather than letting it arrive here as a
	// zero — see the tierNumeric check in ServiceUpdate. Zero here therefore
	// means "the operator submitted nothing", which is the only case that
	// reaches this line.
	atoi := func(k string) int {
		n, err := strconv.Atoi(e.Values[k])
		if err != nil {
			return 0
		}
		return n
	}
	optInt := func(k string) *int {
		if e.Values[k] == "" {
			return nil
		}
		n := atoi(k)
		return &n
	}
	opt := func(k string) *string {
		if e.Values[k] == "" {
			return nil
		}
		v := e.Values[k]
		return &v
	}
	return &domain.ServiceSpec{
		Code: e.Values["code"], Name: e.Values["name"], Kind: e.Values["kind"],
		EnvironmentID: e.Values["environment_id"], Availability: e.Values["availability"],
		Tier: atoi("tier"), MinHealthy: optInt("min_healthy"),
		FailoverMode: opt("failover_mode"), RTOMinutes: optInt("rto_minutes"),
		RPOMinutes: optInt("rpo_minutes"), TeamID: opt("team_id"),
		ManagerRole: opt("manager_role"), EOLDate: opt("eol_date"),
	}
}

// withMulti records a repeating field, so a refused form redraws the boxes the
// operator actually ticked.
func (e *editState) withMulti(name string, values []string) *editState {
	if e.Multi == nil {
		e.Multi = map[string][]string{}
	}
	e.Multi[name] = values
	return e
}

const (
	flashTextKey = "flash_text"
	flashKindKey = "flash_kind"
)

// base builds the common page data and consumes any pending flash.
func (a *App) base(r *http.Request, title, nav string) Base {
	user := middleware.UserFrom(r.Context())
	b := Base{
		Title:       title,
		Nav:         nav,
		Version:     version.Short(),
		CSVLink:     csvLinkFor(r),
		NavGroups:   NavFor(nav),
		User:        user,
		CanWrite:    a.Authz.CanWrite(user),
		IsAdmin:     a.Authz.IsAdministrator(user),
		CanSeeCosts: a.Authz.CanSeeCosts(user),
		permit:      a.entityPermit(r),
		CSRF:        nosurf.Token(r),
		// Which row the operator asked to edit, if any. In the query string
		// rather than in HTMX state so that an edit form is a plain link: the
		// page re-renders either way, which it must, because correcting an
		// amount moves the totals at the top of the same panel. A fragment swap
		// would leave those stale and stale money is worse than a page load.
		EditRow:    r.URL.Query().Get("edit"),
		RepriceRow: r.URL.Query().Get("reprice"),
	}
	if kind, text := a.takeFlash(r); text != "" {
		b.Flash = &Flash{Kind: kind, Text: text}
	}
	return b
}

// takeFlash consumes the pending flash, at most once per request.
//
// A handler often builds page data more than once -- the list page and the
// form context embedded in it both want the common fields. Popping the session
// value on each call meant the first Base swallowed the message and the second
// one, the Base the layout actually renders, showed nothing. The per-request
// state makes the read idempotent.
func (a *App) takeFlash(r *http.Request) (kind, text string) {
	state := middleware.StateFrom(r.Context())
	if state == nil {
		// No middleware (a unit test constructing data directly). Fall back to
		// reading the session, which is correct if slightly less careful.
		return a.popFlash(r)
	}
	if !state.FlashLoaded {
		state.FlashLoaded = true
		state.FlashKind, state.FlashText = a.popFlash(r)
	}
	return state.FlashKind, state.FlashText
}

func (a *App) popFlash(r *http.Request) (kind, text string) {
	text = a.Sessions.PopString(r.Context(), flashTextKey)
	if text == "" {
		return "", ""
	}
	kind = a.Sessions.PopString(r.Context(), flashKindKey)
	if kind == "" {
		kind = "info"
	}
	return kind, text
}

// setFlash queues a message for the next full page render.
func (a *App) setFlash(r *http.Request, kind, text string) {
	a.Sessions.Put(r.Context(), flashTextKey, text)
	a.Sessions.Put(r.Context(), flashKindKey, kind)
}

// oobFlash builds an out-of-band flash fragment to append to a response.
//
// Out-of-band means a handler can report what happened without the caller
// having arranged anywhere to put the message, which is what lets a handler
// that swaps one table row still confirm the action.
func oobFlash(kind, text string) render.OOB {
	return render.OOB{Template: "flash_oob", Data: Flash{Kind: kind, Text: text}}
}

// serverError logs the detail and shows the client nothing useful.
func (a *App) serverError(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("handler failed", "error", err, "path", r.URL.Path, "method", r.Method)
	http.Error(w, "Something went wrong.", http.StatusInternalServerError)
}

// handleStoreError maps domain sentinels onto status codes. A raw driver error
// never reaches the client.
func (a *App) handleStoreError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrNotFound):
		a.notFound(w, r)
	case errors.Is(err, domain.ErrForbidden):
		http.Error(w, "You are not allowed to do that.", http.StatusForbidden)
	case errors.Is(err, domain.ErrConflict):
		http.Error(w, "That conflicts with something that already exists.", http.StatusConflict)
	case errors.Is(err, domain.ErrInvalid):
		http.Error(w, "That request was not valid.", http.StatusUnprocessableEntity)
	default:
		a.serverError(w, r, err)
	}
}

func (a *App) notFound(w http.ResponseWriter, r *http.Request) {
	a.Render.Page(w, http.StatusNotFound, "error", struct {
		Base
		Code    int
		Message string
	}{
		Base:    a.base(r, "Not found", ""),
		Code:    http.StatusNotFound,
		Message: "There is no such thing here.",
	})
}

// formValue trims whitespace, because a trailing space in a service code is a
// bug report waiting to happen.
func formValue(r *http.Request, key string) string {
	return strings.TrimSpace(r.PostFormValue(key))
}

// optionalString returns nil for an empty field, so an untouched form field
// stores NULL rather than an empty string. The difference matters: NULL means
// "not recorded", empty string means "recorded as nothing".
func optionalString(r *http.Request, key string) *string {
	v := formValue(r, key)
	if v == "" {
		return nil
	}
	return &v
}

// submittedString reads an optional field that must only change when the form
// actually carried it.
//
// optionalString cannot tell "the operator cleared this select" from "this field
// never rendered": both arrive as an empty value and both become nil. For the
// team and role pickers that difference is the difference between a deliberate
// edit and a silent one -- responsibilityOptions degrades to EMPTY pickers when
// its store read fails, so a transient database error would otherwise turn a
// save of some unrelated field into "this asset no longer has a team", written
// to change_log under the name of whoever pressed the button. Found by a
// security review.
//
// A key that is present and empty is still a clearance: that is the operator
// choosing the blank option, and it must keep working.
func submittedString(r *http.Request, key string, current *string) *string {
	// Parsed here rather than relying on an earlier formValue having done it:
	// this returning `current` because PostForm happened to be nil would be the
	// same silent no-op in the other direction, and it would depend on the
	// order of assignments in the caller.
	if r.PostForm == nil {
		_ = r.ParseForm()
	}
	if !r.PostForm.Has(key) {
		return current
	}
	return optionalString(r, key)
}

// optionalInt parses an optional numeric field.
// optionalInt reads a whole number that may legitimately be absent.
//
// ok is FALSE only when the field was present and is not a number — the same
// contract intValue got, and for the same reason, arrived at the same way: an
// error swallowed here returns nil, nil is a VALID value for every field this
// is used on, so Validate passes, the store writes NULL and the response says
// "updated". An operator who types 1000mbps into a speed loses the speed with
// no warning.
//
// Fixed at the constructor rather than at the seventeen call sites, because
// the call sites are not where the mistake is: a helper that cannot express
// "that was garbage" makes every caller wrong by default, and the next
// optional numeric field would be wrong too.
func optionalInt(r *http.Request, key string) (*int, bool) {
	v := formValue(r, key)
	if v == "" {
		return nil, true
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil, false
	}
	return &n, true
}

// numbers collects the optional numeric fields of one form and remembers which
// of them arrived as something that is not a number.
//
// The alternative is an `ok` check beside every field, which is noise at the
// seventeen call sites and gets skipped at the eighteenth. This keeps each
// field one line and makes the refusal a single check at the end.
type numbers struct {
	r   *http.Request
	bad []string
}

func optionalNumbers(r *http.Request) *numbers { return &numbers{r: r} }

// opt reads one field, recording it if it was garbage.
func (n *numbers) opt(key string) *int {
	v, ok := optionalInt(n.r, key)
	if !ok {
		n.bad = append(n.bad, key)
	}
	return v
}

// ratio reads an overcommit typed the way operators say it -- "3", "1.5",
// "2.5:1" -- and returns hundredths.
//
// THE SAME ARGUMENT AS kilos, ARRIVED AT THE SAME WAY. The column stores
// hundredths so the arithmetic is exact; nobody types 300 for a 3:1 ratio, and
// a form that demanded it would be answered wrong at least once. Parsed by
// hand rather than through a float for kilos's reason: 1.5 * 100 through a
// float is 149.999... on the way to an int.
func (n *numbers) ratio(key string) *int {
	v := strings.TrimSpace(formValue(n.r, key))
	// ":1" is how the ratio is written everywhere else in this product, so it
	// is accepted rather than refused -- somebody copying the figure off the
	// capacity panel is not making a mistake.
	v = strings.TrimSuffix(v, ":1")
	if v == "" {
		return nil
	}
	whole, frac, hasFrac := strings.Cut(v, ".")
	hundredths, err := strconv.Atoi(whole)
	if err != nil {
		n.bad = append(n.bad, key)
		return nil
	}
	hundredths *= 100
	if hasFrac {
		if frac == "" || len(frac) > 2 {
			frac = (frac + "00")[:2]
		}
		for len(frac) < 2 {
			frac += "0"
		}
		f, err := strconv.Atoi(frac)
		if err != nil {
			n.bad = append(n.bad, key)
			return nil
		}
		hundredths += f
	}
	return &hundredths
}

// sub reads a number the way submittedString reads a string: a field the
// rendered form did not carry leaves the stored value alone.
//
// NEEDED THE MOMENT A FIELD BECAME CONDITIONAL. The capacity inputs appear only
// on kinds that can carry a workload, so a plain opt() would read "absent" as
// "cleared" and a hypervisor edited through any other variant of this form
// would silently lose its size -- taking every capacity figure for its cluster
// with it, and reporting the loss as an unmeasured host rather than as a bug.
func (n *numbers) sub(key string, current *int) *int {
	if n.r.PostForm == nil {
		_ = n.r.ParseForm()
	}
	if !n.r.PostForm.Has(key) {
		return current
	}
	return n.opt(key)
}

// kilos reads a weight typed in KILOGRAMS and returns it in grams.
//
// THE FORM ASKS FOR WHAT PEOPLE KNOW AND THE COLUMN STORES WHAT SUMS EXACTLY.
// A datasheet says 8.5 kg and nobody types 8500, so the input is kilograms with
// one decimal; the column is grams for the reason money is minor units, since
// twenty boxes rounded to whole kilograms lose ten kilograms off a rack total.
//
// Parsed by hand rather than through a float. "8.5" via ParseFloat then
// multiplied by 1000 is 8499.999... on the way to an int, which truncates to
// 8499 -- a gram nobody would notice and an arithmetic sin that spreads.
func (n *numbers) kilos(key string) *int {
	v := strings.TrimSpace(formValue(n.r, key))
	if v == "" {
		return nil
	}
	whole, frac, hasFrac := strings.Cut(v, ".")
	grams, err := strconv.Atoi(whole)
	if err != nil {
		n.bad = append(n.bad, key)
		return nil
	}
	grams *= 1000
	if hasFrac {
		// One decimal place is the precision a datasheet publishes. More is
		// accepted and truncated rather than refused: somebody pasting 8.53
		// means 8.5 kg, not an error message.
		if frac == "" || len(frac) > 3 {
			frac = (frac + "000")[:3]
		}
		for len(frac) < 3 {
			frac += "0"
		}
		f, err := strconv.Atoi(frac)
		if err != nil {
			n.bad = append(n.bad, key)
			return nil
		}
		grams += f
	}
	return &grams
}

// messages is nil when every field was usable, and otherwise names each one
// that was not — so an operator is told which box to look at rather than that
// something, somewhere, was wrong.
func (n *numbers) messages() map[string]string {
	if len(n.bad) == 0 {
		return nil
	}
	msgs := make(map[string]string, len(n.bad))
	for _, key := range n.bad {
		msgs[key] = "must be a whole number, or blank"
	}
	return msgs
}

// intValue reads a whole number from a form.
//
// ok is FALSE only when the field was present and is not a number. That case
// used to return the fallback silently, so a hand-written POST of tier=abc
// saved the stored value and answered 303: the operator is told their edit went
// in and it did not. Blank is not that case — a field left empty, or one that
// never rendered, legitimately means "use what was there", which is the same
// reasoning submittedString is built on.
//
// Returning two values rather than swallowing it makes every caller decide,
// which is the point: there is no correct universal answer, only a per-field
// one, and eight call sites had all silently taken the wrong one.
func intValue(r *http.Request, key string, fallback int) (int, bool) {
	v := formValue(r, key)
	if v == "" {
		return fallback, true
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback, false
	}
	return n, true
}

// notANumber is the message for a field that arrived as something else.
func notANumber(field string) map[string]string {
	return map[string]string{field: "must be a whole number"}
}

// queryInt reads a whole number from the query string, clamped to a range.
//
// CLAMPED RATHER THAN REFUSED, unlike a submitted form field. A query
// parameter is part of a URL somebody may have bookmarked, pasted into a
// ticket or shortened; answering a stale link with 422 helps nobody, and these
// values only ever choose how much of a report to show. So an out-of-range
// horizon becomes the nearest sensible one instead of being silently dropped
// to the default — which is what it did before, so ?months=-5 and ?months=6
// produced the same page with no way to tell them apart.
//
// A form field is different and is refused: see intValue.
func queryInt(r *http.Request, key string, fallback, min, max int) int {
	v := strings.TrimSpace(r.URL.Query().Get(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}

// queryStrings reads a repeated query parameter -- ?asset=a&asset=b -- and
// returns every value, trimmed, with blanks and duplicates dropped and the
// caller's order kept.
//
// Order is preserved because the page that uses this names the set back to the
// operator in the order they built it. Duplicates are dropped because the same
// id twice is one thing, and naming it twice under a heading that claims two
// would be a lie about what was simulated.
func queryStrings(r *http.Request, key string) []string {
	return dedupeStrings(r.URL.Query()[key])
}

// dedupeStrings trims, drops blanks, and keeps the first occurrence of each
// value in its original order.
func dedupeStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	return out
}

// checkbox reads an HTML checkbox, which is absent rather than false when
// unticked.
func checkbox(r *http.Request, key string) bool {
	v := formValue(r, key)
	return v == "on" || v == "true" || v == "1"
}

// permit mints the write authorization handed to store methods.
//
// WP-G1 TASK 12: this is now the one seam every scoped write route goes
// through. Every call site across this package still says `a.permit(r)` --
// the same shape Task 10 left them in, `a.permit(r)` reached through the App
// they already hold -- so wiring a real, request-scoped, project-owner-aware
// decision in is a change to THIS function's body only. See
// internal/auth/permit.go's Authorizer.Permit for what "real" means: it asks
// a.Authz once per request, fresh, and never caches (see that method's own
// comment for why).
//
// The error branch below is fail-closed rather than assumed unreachable: every
// route that reaches here already sits behind RequireWrite
// (internal/web/routes.go), and until Task 13 flips CanWrite for a project
// owner that means only an Administrator, whom Authorizer.Permit never
// refuses, can reach this function at all -- so today this branch cannot
// fire. It is written anyway, and fails to a permit that Covers nothing
// rather than to domain.AdministratorPermit, because assuming "cannot happen"
// stays true forever is exactly the assumption WP-G1 exists to stop making.
func (a *App) permit(r *http.Request) domain.Permit {
	p, err := a.resolvePermit(r)
	if err != nil {
		slog.Error("permit refused for a request behind RequireWrite",
			"error", err, "path", r.URL.Path)
	}
	return p
}

// entityPermit resolves the request's write permit for Base.CanWriteEntity
// (WP-G1 Task 17), and caches it on middleware.RequestState so a handler that
// builds Base more than once for the same request -- the list-page-plus-
// embedded-form shape a.takeFlash's own comment describes -- pays
// auth.Authorizer.Permit's cost (up to four queries, for a project owner)
// once, not per call.
//
// DELIBERATELY NOT a.permit(r). That function's error branch logs at
// slog.Error because every caller of a.permit sits behind RequireWrite, so
// reaching the error branch there means something already went wrong. base()
// runs on every GET, for every persona, including an Observer or an
// unauthenticated visitor -- for whom auth.Authorizer.Permit returning
// domain.ErrForbidden is the ORDINARY case, not a break-glass fallback.
// Logging it here would turn every page an Observer opens into an error-log
// entry. The failure value is the same permit-covering-nothing a.permit
// falls back to, for the same reason: a template asking Covers on it must
// get false, never a nil-pointer panic.
//
// ALSO DELIBERATELY NOT A CALL TO THE PACKAGE-LEVEL actor(r) HELPER, even
// though the fallback below needs exactly what that helper returns.
// internal/web/routescan's call-graph walker (WP-G1 Task 6) treats any
// identifier literally named actor as the write path's actor resolution,
// and follows every call reachable from a handler by NAME, one level deep,
// with no static-type resolution -- so a call to actor( here would make
// EVERY handler that calls a.base (which is all of them, including the
// render-only write-bucket GETs routescan_test.go names by hand) appear to
// "reach actor(", collapsing the exact distinction that package exists to
// draw. The logic is duplicated inline instead -- three lines, not worth a
// second named function for the walker to also have to special-case.
func (a *App) entityPermit(r *http.Request) domain.Permit {
	p, _ := a.resolvePermit(r)
	return p
}

// resolvePermit is the ONE place a request's permit is resolved, cached on
// the request state so a handler that builds Base more than once -- or calls
// both permit(r) and entityPermit(r) in the same request -- pays
// auth.Authorizer.Permit's four queries once.
//
// It returns the error as well as the permit because its two callers react
// to a refusal differently, and that difference is the only reason they are
// separate functions. permit(r) runs on write routes behind RequireWrite,
// where a refusal is anomalous and worth an error log. entityPermit(r) runs
// on read pages, where an Observer being refused a WRITE permit is the
// ordinary case and logging it would be noise on every page view.
//
// Both get the same fail-closed fallback: a scoped permit covering nothing.
// Keeping that fallback in one place matters -- two hand-written copies
// would be two things to keep in step, and a fallback that drifts open is
// the failure mode this whole work package exists to prevent.
func (a *App) resolvePermit(r *http.Request) (domain.Permit, error) {
	state := middleware.StateFrom(r.Context())
	if state != nil && state.PermitLoaded {
		return state.Permit, state.PermitErr
	}
	user := middleware.UserFrom(r.Context())
	p, err := a.Authz.Permit(r.Context(), user)
	if err != nil {
		fallback := domain.SystemActor
		if user != nil {
			fallback = domain.UserActor(user)
		}
		p = domain.ScopedPermit(fallback, nil, nil)
	}
	if state != nil {
		state.Permit = p
		state.PermitErr = err
		state.PermitLoaded = true
	}
	return p, err
}

// actor identifies the signed-in user for the audit trail.
func actor(r *http.Request) domain.Actor {
	if user := middleware.UserFrom(r.Context()); user != nil {
		return domain.UserActor(user)
	}
	return domain.SystemActor
}

// validationErrors extracts field messages for re-rendering a form.
func validationErrors(err error) (map[string]string, bool) {
	if ve, ok := domain.AsValidation(err); ok {
		return ve.Messages(), true
	}
	return nil, false
}

// csvLinkFor is the current request as a CSV download.
//
// It preserves every parameter except the ones that would make the file
// something other than "this list": format itself, and the inline-edit marker,
// which names a row being edited and has no meaning in a file.
func csvLinkFor(r *http.Request) string {
	q := r.URL.Query()
	q.Del("edit")
	q.Set("format", "csv")
	return r.URL.Path + "?" + q.Encode()
}

// customFieldsCSVLinkFor is the current request's filter, pointed at the
// custom-field-values download for that resource -- ITS OWN ROUTE, not a
// query flag on this one. Custom-field columns are not importable the way
// ExportAssets/ExportServices are (see store.ExportAssetCustomFields), so
// this deliberately does not reuse csvLinkFor's format=csv on the same path:
// a shared format flag would invite somebody to assume the two files carry
// the same round-trip guarantee, which is exactly the assumption the defect
// this route exists to fix was built on.
//
// path is the dedicated route (e.g. "/assets/custom-fields.csv"), not
// r.URL.Path, since that always names the list page this link is rendered
// from.
func customFieldsCSVLinkFor(path string, r *http.Request) string {
	q := r.URL.Query()
	q.Del("edit")
	q.Del("format")
	return path + "?" + q.Encode()
}
