package handlers

import (
	"encoding/json"
	"net/http"

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
	Changes []domain.ChangeLog
}

// ChangeLog renders the global audit trail.
func (a *App) ChangeLog(w http.ResponseWriter, r *http.Request) {
	changes, err := a.Store.ListRecentChanges(r.Context(), 200)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Page(w, http.StatusOK, "changes", changesPage{
		Base:    a.base(r, "Change log", "changes"),
		Changes: changes,
	})
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
	json.NewEncoder(w).Encode(status)
}
