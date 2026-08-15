// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"log/slog"
	"net/http"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/render"
)

type clusterListPage struct {
	Base
	Clusters []store.ClusterRow
	Kinds    []string
	Policies []string
	Errors   map[string]string
}

// ClusterList renders every cluster.
func (a *App) ClusterList(w http.ResponseWriter, r *http.Request) {
	a.renderClusters(w, r, http.StatusOK, nil)
}

func (a *App) renderClusters(w http.ResponseWriter, r *http.Request, status int, errs map[string]string) {
	clusters, err := a.Store.ListClusters(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Page(w, status, "cluster_list", clusterListPage{
		Base:     a.base(r, "Clusters", "clusters"),
		Clusters: clusters,
		Kinds:    domain.ClusterKinds,
		Policies: domain.HAPolicies,
		Errors:   orEmpty(errs),
	})
}

// ClusterDetail shows one cluster, its hosts and what its policy means.
func (a *App) ClusterDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cluster, err := a.Store.GetCluster(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	hosts, err := a.Store.ListClusterHosts(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	candidates, err := a.Store.ClusterCandidates(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	// What losing ONE host would do. The arithmetic an operator would
	// otherwise do in their head, and the reason the page exists.
	afterOne := domain.CanRelocate(cluster.HAPolicy, len(hosts)-1, cluster.MinHosts)

	// How much there is and what has been claimed (WP-J3). Logged and absent
	// rather than fatal, like the elevation on an asset page: a capacity panel
	// is worth having and is not worth taking the page down for.
	capacity, err := a.Store.ClusterCapacityFor(r.Context(), id)
	if err != nil {
		slog.Error("resolving cluster capacity", "error", err, "cluster", id)
	}

	// Who holds what share of it (WP-J4). Logged and absent rather than fatal,
	// like the capacity above: this page is opened during an incident.
	attribution, err := a.Store.AttributionFor(r.Context(), id)
	if err != nil {
		slog.Error("dividing the cluster", "error", err, "cluster", id)
	}

	a.Render.Page(w, http.StatusOK, "cluster_detail", struct {
		Base
		Cluster     *domain.Cluster
		Hosts       []store.ClusterHostRow
		Candidates  []store.AssetRow
		AfterOne    domain.Relocation
		PolicyNote  string
		Capacity    *domain.ClusterCapacity
		Attribution *store.Attribution
	}{
		Base:        a.base(r, cluster.Name, "clusters"),
		Cluster:     cluster,
		Hosts:       hosts,
		Candidates:  candidates,
		AfterOne:    afterOne,
		PolicyNote:  domain.HAPolicyDescription(cluster.HAPolicy),
		Capacity:    capacity,
		Attribution: attribution,
	})
}

// ClusterCreate declares a cluster.
func (a *App) ClusterCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	c, err := domain.NewCluster(store.NewID(), formValue(r, "name"), formValue(r, "kind"))
	if err == nil {
		if p := formValue(r, "ha_policy"); p != "" {
			c.HAPolicy = p
		}
		nums := optionalNumbers(r)
		c.MinHosts = nums.opt("min_hosts")
		c.CPUOvercommit = nums.ratio("cpu_overcommit")
		c.Description = optionalString(r, "description")
		if msgs := nums.messages(); msgs != nil {
			a.renderClusters(w, r, http.StatusUnprocessableEntity, msgs)
			return
		}
		err = c.Validate()
		if err == nil {
			err = a.Store.CreateCluster(r.Context(), actor(r), c)
		}
	}
	if err != nil {
		messages, ok := validationErrors(err)
		if !ok {
			if isConflict(err) {
				messages = map[string]string{"name": "a cluster with that name already exists"}
			} else {
				a.handleStoreError(w, r, err)
				return
			}
		}
		a.renderClusters(w, r, http.StatusUnprocessableEntity, messages)
		return
	}
	a.setFlash(r, "success", "Cluster "+c.Name+" declared.")
	render.Redirect(w, r, "/clusters")
}

// ClusterUpdate corrects a cluster, including the policy the engine reads.
func (a *App) ClusterUpdate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	existing, err := a.Store.GetCluster(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	updated := *existing
	updated.Name = formValue(r, "name")
	updated.Kind = formValue(r, "kind")
	updated.HAPolicy = formValue(r, "ha_policy")
	nums := optionalNumbers(r)
	updated.MinHosts = nums.opt("min_hosts")
	// sub-shaped for the same reason as the asset's capacity, through ratio:
	// a form variant without the field must not silently drop the ratio and
	// quietly re-read the cluster at a conservative 1:1.
	if r.PostForm.Has("cpu_overcommit") {
		updated.CPUOvercommit = nums.ratio("cpu_overcommit")
	}
	updated.Description = optionalString(r, "description")
	// 422 with a message rather than a re-rendered form: this page has no
	// per-field error path, and every other refusal here already lands the
	// same way. Refused and said out loud beats accepted and dropped.
	if nums.messages() != nil {
		http.Error(w, "Those numbers were not numbers. The overcommit ratio is "+
			"written the way it reads: 3, or 1.5.", http.StatusUnprocessableEntity)
		return
	}
	updated.RowVersion = submittedVersion(r, updated.RowVersion)

	if err := a.Store.UpdateCluster(r.Context(), actor(r), &updated); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Cluster updated.")
	render.Redirect(w, r, "/clusters/"+id)
}

// ClusterSetHosts replaces which hosts are in the cluster.
func (a *App) ClusterSetHosts(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "Could not read that form.", http.StatusBadRequest)
		return
	}
	id := r.PathValue("id")
	var members []domain.ClusterMember
	for _, assetID := range r.Form["asset_id"] {
		if assetID == "" {
			continue
		}
		members = append(members, domain.ClusterMember{ClusterID: id, AssetID: assetID})
	}
	if err := a.Store.SetClusterMembers(r.Context(), actor(r), id, members); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Cluster hosts updated.")
	render.Redirect(w, r, "/clusters/"+id)
}

// ClusterRetire withdraws a cluster.
func (a *App) ClusterRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireCluster(r.Context(), actor(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	a.setFlash(r, "success", "Cluster withdrawn.")
	render.Redirect(w, r, "/clusters")
}
