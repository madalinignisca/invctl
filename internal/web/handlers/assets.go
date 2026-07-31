package handlers

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/impact"
	"github.com/gabriel/invctl/internal/store"
	"github.com/gabriel/invctl/internal/web/render"
)

// ---------- environments ----------

type environmentsPage struct {
	Base
	Environments []domain.Environment
	FormData     environmentFormData
}

type environmentForm struct {
	Code        string
	Name        string
	Role        string
	InScope     bool
	Criticality int
}

// EnvironmentList renders the environments page.
func (a *App) EnvironmentList(w http.ResponseWriter, r *http.Request) {
	envs, err := a.Store.ListEnvironments(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	roles, err := a.Store.EnvironmentRoles(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Page(w, http.StatusOK, "environment_list", environmentsPage{
		Base:         a.base(r, "Environments", "environments"),
		Environments: envs,
		FormData: environmentFormData{
			Base:   a.base(r, "Environments", "environments"),
			Errors: map[string]string{},
			Roles:  roles,
			// The default is a value the code knows by name, which is a
			// different thing from the set of values it accepts.
			Form: environmentForm{Role: domain.EnvRoleProduction, Criticality: 3},
		},
	})
}

// EnvironmentCreate adds an environment.
func (a *App) EnvironmentCreate(w http.ResponseWriter, r *http.Request) {
	form := environmentForm{
		Code:        formValue(r, "code"),
		Name:        formValue(r, "name"),
		Role:        formValue(r, "role"),
		InScope:     checkbox(r, "in_scope"),
		Criticality: intValue(r, "criticality", 3),
	}

	env, err := domain.NewEnvironment(store.NewID(), form.Code, form.Name, form.Role,
		form.InScope, form.Criticality, a.Store.Now())
	if err == nil {
		err = a.Store.CreateEnvironment(r.Context(), actor(r), env)
	}
	if err != nil {
		// A validation failure re-renders the form with error state and
		// returns 422 -- never a 200 with the message buried in the body.
		if messages, ok := validationErrors(err); ok {
			a.renderEnvironments(w, r, http.StatusUnprocessableEntity, messages, form)
			return
		}
		if isConflict(err) {
			a.renderEnvironments(w, r, http.StatusUnprocessableEntity,
				map[string]string{"code": "an environment with that code already exists"}, form)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}

	a.setFlash(r, "success", "Environment "+env.Code+" created.")
	render.Redirect(w, r, "/environments")
}

func (a *App) renderEnvironments(w http.ResponseWriter, r *http.Request, status int, messages map[string]string, form environmentForm) {
	envs, err := a.Store.ListEnvironments(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	roles, err := a.Store.EnvironmentRoles(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	formData := environmentFormData{
		Base:   a.base(r, "Environments", "environments"),
		Errors: orEmpty(messages),
		Roles:  roles,
		Form:   form,
	}
	if render.IsHTMX(r) {
		a.Render.Partial(w, status, "environment_form", formData)
		return
	}
	a.Render.Page(w, status, "environment_list", environmentsPage{
		Base:         a.base(r, "Environments", "environments"),
		Environments: envs,
		FormData:     formData,
	})
}

// ---------- assets ----------

type assetListPage struct {
	Base
	Assets       []store.AssetRow
	Environments []domain.Environment
	Kinds        []store.VocabularyTerm
	Filter       store.AssetFilter
	FormData     assetFormData
}

// AssetList renders the asset inventory with filters.
func (a *App) AssetList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.AssetFilter{
		Kind:           q.Get("kind"),
		EnvironmentID:  q.Get("environment"),
		Lifecycle:      q.Get("lifecycle"),
		Query:          q.Get("q"),
		IncludeRetired: q.Get("retired") == "1",
	}

	assets, err := a.Store.ListAssets(r.Context(), filter)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	envs, err := a.Store.ListEnvironments(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	kinds, err := a.Store.AssetKinds(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	data := assetListPage{
		Base:         a.base(r, "Assets", "assets"),
		Assets:       assets,
		Environments: envs,
		Kinds:        kinds,
		Filter:       filter,
		FormData:     a.newAssetForm(r, nil, envs, kinds, assets),
	}
	// Filtering swaps only the table, so typing in the filter box does not
	// rebuild the page around it.
	a.Render.Respond(w, r, http.StatusOK, "asset_list", "asset_table", data)
}

type assetDetailPage struct {
	Base
	Asset *store.AssetRow
	// Costs are the lines attached to this asset, retired ones included so the
	// page can show them struck through. Totalling excludes them.
	Costs       []store.CostRow
	CostTotals  domain.CostTotals
	CostKinds   []store.VocabularyTerm
	CostPeriods []string
	Ancestors   []domain.Asset
	Children    []store.AssetRow
	Interfaces  []store.InterfaceRow
	Instances   []store.InstanceRow
	// Health is what the estate reports about this asset, with staleness
	// applied and any operator override alongside it -- never merged into it.
	Health *store.EntityHealth
	// InstanceHealth is the same for each workload placed here, keyed by
	// instance id. Every placement has an entry, including the ones nothing
	// watches: a missing key and an unobserved entity render identically and
	// mean completely different things.
	InstanceHealth map[string]store.EntityHealth
	// Timeline folds this asset's declared history with its one-hop declared
	// neighbours' and with the observed transitions for the same rows. "What
	// changed just before this broke" is the 03:00 question and it is not
	// answerable from one entity's history.
	Timeline      []store.TimelineEntry
	Environments  []domain.Environment
	Kinds         []store.VocabularyTerm
	Lifecycles    []string
	InterfaceForm interfaceFormData
	IPAddressForm ipAddressFormData
	LinkForm      linkFormData
	OverrideForm  overrideFormData
}

// AssetDetail renders one asset with its containment, ports and workloads.
func (a *App) AssetDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	asset, err := a.Store.GetAsset(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	ancestors, err := a.Store.Ancestors(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	children, err := a.Store.Children(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	interfaces, err := a.Store.ListInterfaces(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	instances, err := a.Store.ListInstancesByHost(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	health, err := a.Store.GetEntityHealth(r.Context(), domain.ObservableAsset, id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	instanceHealth, err := a.Store.EntityHealthFor(r.Context(), domain.ObservableServiceInstance,
		instanceIDs(instances))
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	timeline, _, err := a.Store.TimelineForEntityAndNeighbours(r.Context(), "asset", id, timelineLimit)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	targets, err := a.assetOverrideTargets(r.Context(), asset, instances)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	envs, err := a.Store.ListEnvironments(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	// Candidates for the "patch to" dropdown -- every unpatched interface in
	// the estate, this asset's own ports included. Excluding nothing by asset
	// keeps the query simple; CreateLink's uniqueness check is what actually
	// prevents a bad cable.
	linkTargets, err := a.Store.ListAvailableInterfaces(r.Context(), "")
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	kinds, err := a.Store.AssetKinds(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	formFactors, err := a.Store.InterfaceFormFactors(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	ipRoles, err := a.Store.IPAddressRoles(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	costs, err := a.Store.ListAssetCosts(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	costKinds, err := a.Store.CostKinds(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	a.Render.Page(w, http.StatusOK, "asset_detail", assetDetailPage{
		Base:           a.base(r, asset.Name, "assets"),
		Asset:          asset,
		Costs:          costs,
		CostTotals:     store.TotalCosts(costs, domain.FormatDate(a.Store.Now())),
		CostKinds:      costKinds,
		CostPeriods:    domain.CostPeriods,
		Ancestors:      ancestors,
		Children:       children,
		Interfaces:     interfaces,
		Instances:      instances,
		Health:         health,
		InstanceHealth: instanceHealth,
		Timeline:       timeline,
		Environments:   envs,
		Kinds:          kinds,
		Lifecycles:     domain.AssetLifecycles,
		InterfaceForm:  a.newInterfaceForm(r, id, nil, formFactors),
		IPAddressForm:  a.newIPAddressForm(r, id, nil, interfaces, ipRoles),
		LinkForm:       a.newLinkForm(r, id, nil, interfaces, linkTargets),
		OverrideForm:   a.newOverrideForm(r, targets, nil, overrideForm{}),
	})
}

// timelineLimit is one screen of folded history. It is larger than the old
// per-entity change list because the timeline covers the neighbourhood as well,
// and a page that shows the neighbours but truncates before reaching them would
// be worse than not folding at all.
const timelineLimit = 60

func instanceIDs(rows []store.InstanceRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.ID)
	}
	return out
}

// AssetCreate adds an asset.
func (a *App) AssetCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	parentID := optionalString(r, "parent_id")

	asset, err := domain.NewAsset(store.NewID(), formValue(r, "kind"), formValue(r, "name"),
		parentID, a.Store.Now())
	if err == nil {
		asset.Serial = optionalString(r, "serial")
		asset.AssetTag = optionalString(r, "asset_tag")
		asset.Vendor = optionalString(r, "vendor")
		asset.Model = optionalString(r, "model")
		asset.TeamID = optionalString(r, "team_id")
		asset.ManagerRole = optionalString(r, "manager_role")
		asset.EOLDate = optionalString(r, "eol_date")
		err = a.Store.CreateAsset(r.Context(), actor(r), asset, submittedEnvironments(r))
	}
	if err != nil {
		if messages, ok := validationErrors(err); ok {
			a.renderAssetFormError(w, r, messages)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}

	a.setFlash(r, "success", "Asset "+asset.Name+" created.")
	render.Redirect(w, r, "/assets/"+asset.ID)
}

// AssetUpdate saves field changes.
func (a *App) AssetUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	existing, err := a.Store.GetAsset(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}

	updated := existing.Asset
	updated.Name = formValue(r, "name")
	updated.Kind = formValue(r, "kind")
	updated.Lifecycle = formValue(r, "lifecycle")
	updated.Serial = optionalString(r, "serial")
	updated.AssetTag = optionalString(r, "asset_tag")
	updated.Vendor = optionalString(r, "vendor")
	updated.Model = optionalString(r, "model")
	// submittedString, not optionalString: a picker that failed to render must
	// not read as an operator clearing the field. See its doc comment.
	updated.TeamID = submittedString(r, "team_id", updated.TeamID)
	updated.ManagerRole = submittedString(r, "manager_role", updated.ManagerRole)
	updated.EOLDate = optionalString(r, "eol_date")

	if err := a.Store.UpdateAsset(r.Context(), actor(r), &updated, submittedEnvironments(r)); err != nil {
		if messages, ok := validationErrors(err); ok {
			a.renderAssetFormError(w, r, messages)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}

	a.setFlash(r, "success", "Asset updated.")
	render.Redirect(w, r, "/assets/"+id)
}

// AssetRetire soft-deletes an asset. There is no hard delete anywhere.
func (a *App) AssetRetire(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := a.Store.RetireAsset(r.Context(), actor(r), id); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Asset retired. Its history is kept.")
	render.Redirect(w, r, "/assets/"+id)
}

// AssetReparent moves an asset in the containment tree, rebuilding the closure
// rows for its subtree.
func (a *App) AssetReparent(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	parentID := optionalString(r, "parent_id")

	if err := a.Store.ReparentAsset(r.Context(), actor(r), id, parentID); err != nil {
		if messages, ok := validationErrors(err); ok {
			text := "That move is not allowed."
			for _, m := range messages {
				text = m
				break
			}
			a.setFlash(r, "error", text)
			render.Redirect(w, r, "/assets/"+id)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Asset moved.")
	render.Redirect(w, r, "/assets/"+id)
}

func (a *App) renderAssetFormError(w http.ResponseWriter, r *http.Request, messages map[string]string) {
	envs, err := a.Store.ListEnvironments(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	parents, err := a.Store.ListAssets(r.Context(), store.AssetFilter{})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	kinds, err := a.Store.AssetKinds(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Partial(w, http.StatusUnprocessableEntity, "asset_form",
		a.newAssetForm(r, messages, envs, kinds, parents))
}

// ---------- impact simulation ----------

// maxOutageAssets bounds the simulated set.
//
// Every id becomes a placeholder in the closure and instance queries, so an
// unbounded repeated parameter is a cheap way for any signed-in reader to make
// the server build an enormous statement. Twenty-five is far more than an
// honest question needs: the biggest real outage is a rack or a site, and that
// is one id because containment expands it.
const maxOutageAssets = 25

// impactAsset is one member of the simulated outage set, with the link that
// takes it back out again.
type impactAsset struct {
	Asset *store.AssetRow
	// RemoveURL is empty for the last remaining member. Simulating the loss of
	// nothing answers nothing, and an operator who removed the final entry
	// would land on a page whose question had silently changed underneath the
	// answer.
	RemoveURL string
}

type impactPage struct {
	Base
	// Asset is the first member of the set -- the one named in the URL path.
	// Every link the page builds hangs off it, which is what keeps a
	// single-asset simulation byte-for-byte what it was before sets existed.
	Asset *store.AssetRow
	// Assets is the whole set, in the order it was built. The page renders
	// every one of them, always: an impact answer read against the wrong
	// question is worse than no answer, and the only defence is putting the
	// question next to it.
	Assets []impactAsset
	// Extra carries the non-primary ids so both toolbars can round-trip the
	// set through hidden fields. Without it, changing the outage window would
	// quietly narrow the simulation back to the one asset in the path and
	// answer a question nobody asked.
	Extra []string
	// Candidates are the assets not already in the set, for the picker that
	// widens it.
	Candidates []store.AssetRow
	Multiple   bool
	Result     impact.Result
	Window     int
	Windows    []windowOption
	// HasImpact is true when a service is affected. HasNetworkFinding is true
	// when the network has something to say that no service status carries --
	// an isolated asset, a partitioned edge, a group left without redundancy.
	// They are separate because "Nothing breaks" printed above a list of
	// isolated assets is the exact contradiction this feature was built to
	// remove: an operator who reads the headline and stops is being told the
	// opposite of what the page below it says.
	HasImpact         bool
	HasNetworkFinding bool
}

type windowOption struct {
	Seconds int
	Label   string
}

// windows are the outage lengths worth distinguishing. They exist because an
// async dependency with a 300-second buffer behaves differently across them,
// and that difference is invisible without offering the choice.
var windows = []windowOption{
	{Seconds: 180, Label: "3 min (quick reboot)"},
	{Seconds: 900, Label: "15 min (patch and reboot)"},
	{Seconds: 2700, Label: "45 min (hardware swap)"},
	{Seconds: 28800, Label: "8 h (extended outage)"},
}

// AssetImpact simulates losing one or more assets, and everything they contain.
//
// The path id is the primary member; every repeated ?asset= parameter widens
// the set. Several at once is not a convenience: a redundant pair only tells
// the truth when both halves can be taken away in the same run, so "what
// happens once redundancy is exhausted" -- the one question a pair exists to
// answer -- is unaskable one asset at a time.
//
// The engine needed nothing for this. impact.Request.DownAssetIDs has always
// been a set, and SubtreeIDs already expands several ancestors in one closure
// query with overlapping subtrees collapsing on their own.
func (a *App) AssetImpact(w http.ResponseWriter, r *http.Request) {
	// The path id goes first, so a bare /assets/{id}/impact is exactly what it
	// always was, and a ?asset= repeating the path id collapses into it rather
	// than naming one asset twice.
	ids := dedupeStrings(append([]string{r.PathValue("id")}, queryStrings(r, "asset")...))
	if len(ids) > maxOutageAssets {
		http.Error(w, fmt.Sprintf("An outage simulation covers at most %d assets at once.", maxOutageAssets),
			http.StatusUnprocessableEntity)
		return
	}
	window := queryInt(r, "window", 180)

	assets := make([]impactAsset, 0, len(ids))
	for i, id := range ids {
		asset, err := a.Store.GetAsset(r.Context(), id)
		if err != nil {
			// An id that resolves to nothing is a 404, never a quietly dropped
			// parameter. Ignoring it would report the impact of a smaller
			// outage than the operator asked about, under a heading naming the
			// outage they wanted -- "nothing breaks" about a scenario nobody
			// simulated is the most dangerous answer this tool can give.
			a.handleStoreError(w, r, err)
			return
		}
		assets = append(assets, impactAsset{
			Asset:     asset,
			RemoveURL: impactURL(withoutIndex(ids, i), window),
		})
	}

	result, err := a.Store.Simulate(r.Context(), impact.Request{
		DownAssetIDs:  ids,
		WindowSeconds: window,
	})
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	candidates, err := a.Store.ListAssets(r.Context(), store.AssetFilter{})
	if err != nil {
		a.serverError(w, r, err)
		return
	}

	data := impactPage{
		Base:       a.base(r, impactTitle(assets), "assets"),
		Asset:      assets[0].Asset,
		Assets:     assets,
		Extra:      ids[1:],
		Candidates: excludeAssets(candidates, ids),
		Multiple:   len(assets) > 1,
		Result:     result,
		Window:     window,
		Windows:    windows,
		HasImpact:  len(result.Services) > 0 || len(result.WontRestart) > 0,
		HasNetworkFinding: len(result.Isolated) > 0 || len(result.Partitions) > 0 ||
			len(result.Unreachable) > 0 || len(result.RedundancyLost) > 0,
	}
	a.Render.Respond(w, r, http.StatusOK, "impact", "impact_result", data)
}

// impactURL builds this page's own address for a given outage set: the first
// id takes the path, the rest ride as repeated parameters, and the window
// comes along so changing one half of the question never resets the other.
func impactURL(ids []string, window int) string {
	if len(ids) == 0 {
		return ""
	}
	q := url.Values{"window": {strconv.Itoa(window)}}
	for _, id := range ids[1:] {
		q.Add("asset", id)
	}
	return "/assets/" + url.PathEscape(ids[0]) + "/impact?" + q.Encode()
}

// withoutIndex returns ids with the element at i removed, leaving the original
// untouched. A one-element set yields nil, which is what suppresses the remove
// link on the last member.
func withoutIndex(ids []string, i int) []string {
	if len(ids) < 2 {
		return nil
	}
	out := make([]string, 0, len(ids)-1)
	out = append(out, ids[:i]...)
	return append(out, ids[i+1:]...)
}

// excludeAssets drops the ones already being simulated, so the picker only
// offers something that would actually change the answer.
func excludeAssets(rows []store.AssetRow, ids []string) []store.AssetRow {
	chosen := make(map[string]bool, len(ids))
	for _, id := range ids {
		chosen[id] = true
	}
	out := make([]store.AssetRow, 0, len(rows))
	for _, row := range rows {
		if !chosen[row.ID] {
			out = append(out, row)
		}
	}
	return out
}

// impactTitle names the set in the browser tab without letting a long one run
// away with the title bar. The page itself always names every member.
func impactTitle(assets []impactAsset) string {
	if len(assets) == 1 {
		return "Impact: " + assets[0].Asset.Name
	}
	return fmt.Sprintf("Impact: %s + %d more", assets[0].Asset.Name, len(assets)-1)
}

func isConflict(err error) bool {
	return errors.Is(err, domain.ErrConflict)
}
