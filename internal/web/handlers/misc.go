package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/store"
)

type dashboardPage struct {
	Base
	Environments  []domain.Environment
	Services      []store.ServiceRow
	Spanning      []store.AssetRow
	Changes       []domain.ChangeLog
	AssetCount    int
	ServiceCount  int
	DepCount      int
	UnverifiedDep int
	// Reporters is rule 8's one alertable event. Per-entity staleness alone
	// turns a dead collector into a thousand entities going quietly unknown,
	// which is a thousand things to notice and therefore nothing anybody
	// notices. One line per credential is the whole point.
	Reporters []store.ReporterStatus
	// Overrides is what is silenced right now. It sits on the landing page
	// because an operator reading a green estate needs to know how much of that
	// green somebody asserted rather than observed.
	Overrides []store.HealthOverrideRow
	// Drift is what reporters are naming that the inventory does not hold.
	// Rule 6 calls this "a finding, not noise" -- an asset the estate has and
	// the inventory does not. It was queued from the first webhook and read by
	// nothing, which is the same as dropping the report except that it also
	// grows.
	Drift []domain.UnmatchedObservation
}

// SilentReporters is how many collectors have stopped, for the panel heading.
func (p dashboardPage) SilentReporters() int {
	n := 0
	for _, r := range p.Reporters {
		if r.Silent {
			n++
		}
	}
	return n
}

// Dashboard is the landing page: what exists, what spans a boundary, and what
// changed recently.
func (a *App) Dashboard(w http.ResponseWriter, r *http.Request) {
	envs, err := a.Store.ListEnvironments(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	services, err := a.Store.ListServices(r.Context(), store.ServiceFilter{})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	assets, err := a.Store.ListAssets(r.Context(), store.AssetFilter{})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	spanning, err := a.Store.SpanningAssets(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	changes, err := a.Store.ListRecentChanges(r.Context(), 12)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	reporters, err := a.Store.ListReporterStatus(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if a.Agents != nil {
		reporters = store.WithConfiguredReporters(reporters, a.Agents.IDs())
	}
	overrides, err := a.Store.ListActiveOverrides(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	drift, err := a.Store.ListUnmatchedObservations(r.Context(), 12)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	// Tier-1 services first: the dashboard should lead with what matters.
	tier1 := make([]store.ServiceRow, 0, 8)
	for _, s := range services {
		if s.Tier <= 1 {
			tier1 = append(tier1, s)
		}
	}

	a.Render.Page(w, http.StatusOK, "dashboard", dashboardPage{
		Base:         a.base(r, "Overview", "dashboard"),
		Environments: envs,
		Services:     tier1,
		Spanning:     spanning,
		Changes:      changes,
		AssetCount:   len(assets),
		ServiceCount: len(services),
		Reporters:    reporters,
		Overrides:    overrides,
		Drift:        drift,
	})
}

type searchPage struct {
	Base
	Query   string
	Results []store.SearchResult
}

// Search resolves an IP, MAC, serial, hostname, service code or port.
func (a *App) Search(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")

	var results []store.SearchResult
	if query != "" {
		var err error
		results, err = a.Store.Search(r.Context(), query, 25)
		if err != nil {
			a.serverError(w, r, err)
			return
		}
	}

	data := searchPage{
		Base:    a.base(r, "Search", "search"),
		Query:   query,
		Results: results,
	}
	a.Render.Respond(w, r, http.StatusOK, "search", "search_results", data)
}

type spanningPage struct {
	Base
	Assets []store.AssetRow
}

// SpanningReport lists assets in more than one non-transit environment.
func (a *App) SpanningReport(w http.ResponseWriter, r *http.Request) {
	assets, err := a.Store.SpanningAssets(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Page(w, http.StatusOK, "spanning", spanningPage{
		Base:   a.base(r, "Environment spans", "reports"),
		Assets: assets,
	})
}

type changesPage struct {
	Base
	Page        *store.ChangePage
	EntityTypes []string
	Filter      changesFilterForm
	// NewerURL and OlderURL are empty when there is no page that way, so a
	// template renders a link only when following it would show something.
	NewerURL string
	OlderURL string
	// Errors carries a bad date back to the form. A filter is a form, and a
	// form that silently ignores what was typed is worse than one that refuses.
	Errors map[string]string
}

// changesFilterForm is what the operator typed, echoed back so the controls
// still show the filter that produced the page.
type changesFilterForm struct {
	EntityType string
	From       string
	To         string
}

// ChangeLog renders the global audit trail, paginated and bounded by a date
// range (docs/AUDIT.md rule 15).
//
// The old version was ListRecentChanges(200) with no filter. Rule 10 already
// keeps observed transitions out of change_log, so a flapping estate can no
// longer flood this page -- but a busy day still can, and the entry that
// explains an incident is the one that falls off the bottom. Paging is a store
// concern rather than a handler one, because a LIMIT applied after the rows are
// read is not paging.
func (a *App) ChangeLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	form := changesFilterForm{
		EntityType: q.Get("entity_type"),
		From:       q.Get("from"),
		To:         q.Get("to"),
	}

	filter := store.ChangeFilter{EntityType: form.EntityType, Limit: store.DefaultChangePageSize}
	errors := map[string]string{}
	if v, ok := parseDayStart(form.From); ok {
		filter.From = v
	} else if form.From != "" {
		errors["from"] = "use a date like 2026-07-28"
	}
	if v, ok := parseDayEnd(form.To); ok {
		filter.To = v
	} else if form.To != "" {
		errors["to"] = "use a date like 2026-07-28"
	}
	if cursor, ok := store.ParseChangeCursor(q.Get("before")); ok {
		filter.Before = &cursor
	} else if cursor, ok := store.ParseChangeCursor(q.Get("after")); ok {
		filter.After = &cursor
	}

	status := http.StatusOK
	if len(errors) > 0 {
		// A malformed range is a validation failure, so it is a 422 with the
		// controls re-rendered rather than a 200 showing a page the operator
		// did not ask for.
		status = http.StatusUnprocessableEntity
		filter.From, filter.To = "", ""
		filter.Before, filter.After = nil, nil
	}

	page, err := a.Store.ListChanges(r.Context(), filter)
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	data := changesPage{
		Base:        a.base(r, "Change log", "changes"),
		Page:        page,
		EntityTypes: store.ChangeEntityTypes(),
		Filter:      form,
		Errors:      errors,
	}
	if page.Newer != nil {
		data.NewerURL = changesURL(form, "after", *page.Newer)
	}
	if page.Older != nil {
		data.OlderURL = changesURL(form, "before", *page.Older)
	}
	// The pager and the filter both swap the same wrapper, so following a page
	// link does not rebuild the shell around it.
	a.Render.Respond(w, r, status, "changes", "change_log", data)
}

// changesURL builds a page link that carries the filter with it. Losing the
// filter on page two would answer a different question under the same heading.
func changesURL(form changesFilterForm, direction string, cursor store.ChangeCursor) string {
	q := url.Values{}
	if form.EntityType != "" {
		q.Set("entity_type", form.EntityType)
	}
	if form.From != "" {
		q.Set("from", form.From)
	}
	if form.To != "" {
		q.Set("to", form.To)
	}
	q.Set(direction, cursor.String())
	return "/changes?" + q.Encode()
}

// parseDayStart and parseDayEnd turn a date from an <input type="date"> into
// the RFC3339 UTC TEXT the column actually holds.
//
// The range is inclusive at both ends and covers whole days, because that is
// what somebody typing two dates means. Everything stored is UTC to the second,
// so the bounds are exact rather than approximate.
func parseDayStart(v string) (string, bool) {
	day, err := time.Parse("2006-01-02", strings.TrimSpace(v))
	if err != nil {
		return "", false
	}
	return domain.FormatTime(day.UTC()), true
}

func parseDayEnd(v string) (string, bool) {
	day, err := time.Parse("2006-01-02", strings.TrimSpace(v))
	if err != nil {
		return "", false
	}
	return domain.FormatTime(day.UTC().Add(24*time.Hour - time.Second)), true
}

// Health reports readiness. It touches the database, because a process that
// is running but cannot reach its database is not healthy in any useful sense.
func (a *App) Health(w http.ResponseWriter, r *http.Request) {
	status := map[string]any{"status": "ok"}
	code := http.StatusOK

	if _, err := a.Store.CountUsers(r.Context()); err != nil {
		status["status"] = "unhealthy"
		status["database"] = "unreachable"
		code = http.StatusServiceUnavailable
	} else {
		status["database"] = a.Store.DB().Driver
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	// Same reasoning as render.writeBuffered: the status is already sent, so a
	// write failure cannot become an error response, and the usual cause is a
	// client that went away. Recorded rather than dropped.
	if err := json.NewEncoder(w).Encode(status); err != nil {
		slog.Debug("health response truncated", "error", err)
	}
}
