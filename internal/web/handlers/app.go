// Package handlers holds one file per resource.
package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/alexedwards/scs/v2"
	"github.com/justinas/nosurf"

	"github.com/gabriel/invctl/internal/auth"
	"github.com/gabriel/invctl/internal/config"
	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/store"
	"github.com/gabriel/invctl/internal/web/middleware"
	"github.com/gabriel/invctl/internal/web/render"
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
}

// Flash is a one-shot message shown after a mutation.
type Flash struct {
	Kind string // "success" | "error" | "info"
	Text string
}

// Base is the data every page needs. Page structs embed it.
type Base struct {
	Title    string
	Nav      string
	User     *domain.AppUser
	CanWrite bool
	CSRF     string
	Flash    *Flash
}

const (
	flashTextKey = "flash_text"
	flashKindKey = "flash_kind"
)

// base builds the common page data and consumes any pending flash.
func (a *App) base(r *http.Request, title, nav string) Base {
	user := middleware.UserFrom(r.Context())
	b := Base{
		Title:    title,
		Nav:      nav,
		User:     user,
		CanWrite: a.Authz.CanWrite(user),
		CSRF:     nosurf.Token(r),
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

// optionalInt parses an optional numeric field.
func optionalInt(r *http.Request, key string) *int {
	v := formValue(r, key)
	if v == "" {
		return nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	return &n
}

func intValue(r *http.Request, key string, fallback int) int {
	v := formValue(r, key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func queryInt(r *http.Request, key string, fallback int) int {
	v := strings.TrimSpace(r.URL.Query().Get(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
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
