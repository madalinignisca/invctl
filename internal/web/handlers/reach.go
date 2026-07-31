package handlers

import (
	"net/http"

	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/store"
	"github.com/gabriel/invctl/internal/web/render"
)

// ---------- /network: forwarder groups, uplinks, attachments, anchors ----------
//
// M2 is the data model and its CRUD; the reachability algorithm itself is M3.
// This page shows the topology as declared and a coverage summary, plus a
// propose-only derivation review. It never writes anything on its own --
// derivation is read-only, and the tables it reads from are the only place
// state changes.

type networkListPage struct {
	Base
	Groups         []store.NetGroupRow
	Anchors        []store.NetAnchorRow
	Coverage       store.NetworkCoverage
	GroupForm      netGroupFormData
	MemberForm     netGroupMemberFormData
	UplinkForm     netUplinkFormData
	AttachmentForm netAttachmentFormData
	AnchorForm     netAnchorFormData
}

type netGroupFormData struct {
	Base
	Errors         map[string]string
	Spec           domain.NetGroupSpec
	Kinds          []string
	Roles          []string
	Availabilities []string
	FailoverModes  []string
}

type netGroupMemberFormData struct {
	Base
	Errors      map[string]string
	Groups      []store.NetGroupRow
	Assets      []store.AssetRow
	MemberRoles []string
}

type netUplinkFormData struct {
	Base
	Errors map[string]string
	Groups []store.NetGroupRow
	Planes []string
}

type netAttachmentFormData struct {
	Base
	Errors map[string]string
	Groups []store.NetGroupRow
	Assets []store.AssetRow
	Planes []string
}

type netAnchorFormData struct {
	Base
	Errors map[string]string
	Groups []store.NetGroupRow
	Scopes []string
}

func (a *App) newNetGroupForm(r *http.Request, errs map[string]string, spec domain.NetGroupSpec) netGroupFormData {
	return netGroupFormData{
		Base: a.base(r, "Networks", "network"), Errors: orEmpty(errs), Spec: spec,
		Kinds: domain.NetGroupKinds, Roles: domain.NetGroupRoles,
		Availabilities: domain.NetGroupAvailabilities, FailoverModes: domain.FailoverModes,
	}
}

func (a *App) newNetGroupMemberForm(r *http.Request, errs map[string]string, groups []store.NetGroupRow, assets []store.AssetRow) netGroupMemberFormData {
	return netGroupMemberFormData{
		Base: a.base(r, "Networks", "network"), Errors: orEmpty(errs),
		Groups: groups, Assets: assets, MemberRoles: domain.NetGroupMemberRoles,
	}
}

func (a *App) newNetUplinkForm(r *http.Request, errs map[string]string, groups []store.NetGroupRow) netUplinkFormData {
	return netUplinkFormData{
		Base: a.base(r, "Networks", "network"), Errors: orEmpty(errs), Groups: groups, Planes: domain.Planes,
	}
}

func (a *App) newNetAttachmentForm(r *http.Request, errs map[string]string, groups []store.NetGroupRow, assets []store.AssetRow) netAttachmentFormData {
	return netAttachmentFormData{
		Base: a.base(r, "Networks", "network"), Errors: orEmpty(errs),
		Groups: groups, Assets: assets, Planes: domain.Planes,
	}
}

func (a *App) newNetAnchorForm(r *http.Request, errs map[string]string, groups []store.NetGroupRow) netAnchorFormData {
	return netAnchorFormData{
		Base: a.base(r, "Networks", "network"), Errors: orEmpty(errs), Groups: groups, Scopes: domain.AnchorScopes,
	}
}

// attachableAssets filters to the kinds a net_attachment may name -- a rack or
// a site is not network-attached.
func attachableAssets(assets []store.AssetRow) []store.AssetRow {
	out := make([]store.AssetRow, 0, len(assets))
	for _, a := range assets {
		if a.IsAttachable() {
			out = append(out, a)
		}
	}
	return out
}

// NetworkList renders the topology page: groups with their members, uplinks
// and anchors, and a coverage summary.
func (a *App) NetworkList(w http.ResponseWriter, r *http.Request) {
	page, err := a.buildNetworkListPage(r, http.StatusOK, nil, nil, nil, nil, nil)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Page(w, http.StatusOK, "network_list", page)
}

func (a *App) buildNetworkListPage(r *http.Request, status int,
	groupErrs, memberErrs, uplinkErrs, attachErrs, anchorErrs map[string]string,
) (networkListPage, error) {
	ctx := r.Context()
	groups, err := a.Store.ListNetGroups(ctx)
	if err != nil {
		return networkListPage{}, err
	}
	anchors, err := a.Store.ListNetAnchors(ctx)
	if err != nil {
		return networkListPage{}, err
	}
	coverage, err := a.Store.NetworkCoverage(ctx)
	if err != nil {
		return networkListPage{}, err
	}
	assets, err := a.Store.ListAssets(ctx, store.AssetFilter{})
	if err != nil {
		return networkListPage{}, err
	}

	return networkListPage{
		Base:           a.base(r, "Networks", "network"),
		Groups:         groups,
		Anchors:        anchors,
		Coverage:       coverage,
		GroupForm:      a.newNetGroupForm(r, groupErrs, domain.NetGroupSpec{}),
		MemberForm:     a.newNetGroupMemberForm(r, memberErrs, groups, assets),
		UplinkForm:     a.newNetUplinkForm(r, uplinkErrs, groups),
		AttachmentForm: a.newNetAttachmentForm(r, attachErrs, groups, attachableAssets(assets)),
		AnchorForm:     a.newNetAnchorForm(r, anchorErrs, groups),
	}, nil
}

// NetworkGroupCreate declares a forwarder group.
func (a *App) NetworkGroupCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	spec := domain.NetGroupSpec{
		Code: formValue(r, "code"), Name: formValue(r, "name"),
		Kind: formValue(r, "kind"), Role: formValue(r, "role"),
		Availability: formValue(r, "availability"),
		MinHealthy:   optionalInt(r, "min_healthy"),
		FailoverMode: optionalString(r, "failover_mode"),
	}
	g, err := domain.NewNetGroup(store.NewID(), spec, a.Store.Now())
	if err == nil {
		err = a.Store.CreateNetGroup(r.Context(), actor(r), g)
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{"code": "a forwarder group with that code already exists"}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.rerenderNetworkForm(w, r, "net_group_form", "group", messages)
		return
	}
	a.setFlash(r, "success", "Forwarder group "+g.Code+" declared.")
	render.Redirect(w, r, "/network")
}

// NetworkGroupMemberCreate places an asset into a forwarder group.
func (a *App) NetworkGroupMemberCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	groupID := r.PathValue("id")
	m, err := domain.NewNetGroupMember(groupID, formValue(r, "asset_id"), formValue(r, "role"), a.Store.Now())
	if err == nil {
		err = a.Store.AddNetGroupMember(r.Context(), actor(r), m)
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{"asset_id": "that asset already belongs to a forwarder group"}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.rerenderNetworkForm(w, r, "net_member_form", "member", messages)
		return
	}
	a.setFlash(r, "success", "Group membership added.")
	render.Redirect(w, r, "/network")
}

// NetworkUplinkCreate declares that one group uplinks to another.
func (a *App) NetworkUplinkCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	groupID := r.PathValue("id")
	u, err := domain.NewNetUplink(store.NewID(), groupID, formValue(r, "upstream_group_id"),
		formValue(r, "plane"), a.Store.Now())
	if err == nil {
		err = a.Store.CreateNetUplink(r.Context(), actor(r), u)
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{"upstream_group_id": "an active uplink to that group already exists"}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.rerenderNetworkForm(w, r, "net_uplink_form", "uplink", messages)
		return
	}
	a.setFlash(r, "success", "Uplink declared.")
	render.Redirect(w, r, "/network")
}

// NetworkAttachmentCreate declares a host's attachment to a forwarder group.
//
// Chassis pinning (net_attachment_member) is not exposed in this form: an
// attachment with no pinned members means "attached to the group as a whole",
// which is a legitimate and often-correct declaration on its own. Pinning to
// specific chassis is supported by the store and covered at that layer; a
// dedicated UI for it is follow-up work, not part of M2's data-entry surface.
func (a *App) NetworkAttachmentCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	na, err := domain.NewNetAttachment(store.NewID(), formValue(r, "asset_id"), formValue(r, "group_id"),
		formValue(r, "plane"), a.Store.Now())
	if err == nil {
		err = a.Store.CreateNetAttachment(r.Context(), actor(r), na, nil)
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{"asset_id": "that asset already has an active attachment to that group on that plane"}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.rerenderNetworkForm(w, r, "net_attachment_form", "attach", messages)
		return
	}
	a.setFlash(r, "success", "Attachment declared.")
	render.Redirect(w, r, "/network")
}

// NetworkAnchorCreate declares where a reachability scope enters the estate.
func (a *App) NetworkAnchorCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	na, err := domain.NewNetAnchor(store.NewID(), formValue(r, "code"), formValue(r, "name"),
		formValue(r, "scope"), formValue(r, "group_id"), a.Store.Now())
	if err == nil {
		err = a.Store.CreateNetAnchor(r.Context(), actor(r), na)
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{"code": "an anchor with that code already exists"}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.rerenderNetworkForm(w, r, "net_anchor_form", "anchor", messages)
		return
	}
	a.setFlash(r, "success", "Anchor "+na.Code+" declared.")
	render.Redirect(w, r, "/network")
}

// rerenderNetworkForm re-renders one embedded form partial with error state,
// mapping which slot of buildNetworkListPage the errors belong to.
func (a *App) rerenderNetworkForm(w http.ResponseWriter, r *http.Request, partial, which string, messages map[string]string) {
	var groupErrs, memberErrs, uplinkErrs, attachErrs, anchorErrs map[string]string
	switch which {
	case "group":
		groupErrs = messages
	case "member":
		memberErrs = messages
	case "uplink":
		uplinkErrs = messages
	case "attach":
		attachErrs = messages
	case "anchor":
		anchorErrs = messages
	}
	page, err := a.buildNetworkListPage(r, http.StatusUnprocessableEntity, groupErrs, memberErrs, uplinkErrs, attachErrs, anchorErrs)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	var data any
	switch which {
	case "group":
		data = page.GroupForm
	case "member":
		data = page.MemberForm
	case "uplink":
		data = page.UplinkForm
	case "attach":
		data = page.AttachmentForm
	case "anchor":
		data = page.AnchorForm
	}
	a.Render.Partial(w, http.StatusUnprocessableEntity, partial, data)
}

// ---------- derivation (propose-only) ----------

type networkDerivePage struct {
	Base
	Proposal *store.DerivationProposal
}

// NetworkDerive computes a derivation proposal from `link` and renders it for
// review. It never writes anything: `link` is evidence, not authority, and
// this handler has no write path into any of the topology tables at all.
func (a *App) NetworkDerive(w http.ResponseWriter, r *http.Request) {
	proposal, err := a.Store.DeriveNetworkProposal(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	data := networkDerivePage{Base: a.base(r, "Networks", "network"), Proposal: proposal}
	a.Render.Respond(w, r, http.StatusOK, "network_derive", "network_derive_result", data)
}
