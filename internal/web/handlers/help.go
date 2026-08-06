// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"net/http"
	"sort"

	"github.com/gabriel/invctl/internal/help"
	"github.com/gabriel/invctl/internal/store"
)

// The help panel: what the words on this page mean, without leaving it.
//
// Two sources, and the split is deliberate rather than incidental. The seven
// lookup vocabularies come from the DATABASE, because they describe an
// estate's own conventions and an operator may rewrite them. The engine-defined
// enums come from Go (internal/help), because their meaning is what the impact
// code does with them -- an editable description for a behaviour nobody can
// edit would drift the moment somebody tried to be helpful.
//
// One panel shows both, and says which is which, because "can I change this
// wording?" is a question with two different answers here.

// helpTopic is one explainable vocabulary, whatever its source.
type helpTopic struct {
	Key   string
	Title string
	Intro string
	// Editable marks the DB-backed vocabularies, whose descriptions an admin
	// may change. The panel says so, so nobody files a ticket to reword an
	// enum the engine owns.
	Editable bool
	Terms    []helpTerm
}

type helpTerm struct {
	Code        string
	Label       string
	Description string
}

// vocabTopics maps a panel topic to the store reader behind it. Table-driven
// so adding the eighth lookup table is a line here rather than a new handler.
func (a *App) vocabTopics() []struct {
	Key, Title, Intro string
	Read              func(*http.Request) ([]store.VocabularyTerm, error)
} {
	type entry = struct {
		Key, Title, Intro string
		Read              func(*http.Request) ([]store.VocabularyTerm, error)
	}
	return []entry{
		{"asset_kind", "Asset kinds",
			"What a box IS. Two of these carry behaviour rather than description: whether the kind can host service instances, and whether it can attach to a network directly.",
			func(r *http.Request) ([]store.VocabularyTerm, error) { return a.Store.AssetKinds(r.Context()) }},
		{"service_kind", "Service kinds",
			"What a service DOES. Descriptive only — nothing in the engine behaves differently because of it, so pick the one a colleague would recognise. Availability and declared dependencies are what drive impact.",
			func(r *http.Request) ([]store.VocabularyTerm, error) { return a.Store.ServiceKinds(r.Context()) }},
		{"interface_form_factor", "Interface form factors",
			"What a port physically is, including the two that are not sockets at all: a LAG is the logical port its members are enslaved to, and a virtual port exists only in software on the host.",
			func(r *http.Request) ([]store.VocabularyTerm, error) {
				return a.Store.InterfaceFormFactors(r.Context())
			}},
		{"environment_role", "Environment roles",
			"What an environment is FOR. One carries behaviour: a transit environment is exempt from the span report, because carrying traffic between two environments is its job rather than a boundary violation.",
			func(r *http.Request) ([]store.VocabularyTerm, error) { return a.Store.EnvironmentRoles(r.Context()) }},
		{"ip_address_role", "Address roles",
			"What an address is for on its interface.",
			func(r *http.Request) ([]store.VocabularyTerm, error) { return a.Store.IPAddressRoles(r.Context()) }},
		{"data_class", "Data classes",
			"What a dependency CARRIES. Recorded on the edge rather than the service, because it is the flow that pulls both ends into an audit scope.",
			func(r *http.Request) ([]store.VocabularyTerm, error) {
				return a.Store.DataClassVocabulary(r.Context())
			}},
		{"container_engine", "Container engines",
			"Which engine runs a container instance.",
			func(r *http.Request) ([]store.VocabularyTerm, error) { return a.Store.ContainerEngines(r.Context()) }},
	}
}

type helpPage struct {
	Base
	// Topic is nil on the index.
	Topic  *helpTopic
	Index  []helpIndexEntry
	Return string
}

type helpIndexEntry struct {
	Key, Title string
	Editable   bool
	Count      int
}

// Help renders the panel: an index of topics, or one topic in full.
//
// A GET with no write surface, so it sits behind RequireAuth like every other
// read and needs no CSRF of its own.
func (a *App) Help(w http.ResponseWriter, r *http.Request) {
	page := helpPage{Base: a.base(r, "Help", "")}

	key := r.PathValue("topic")
	if key == "" {
		index, err := a.helpIndex(r)
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		page.Index = index
		a.Render.Respond(w, r, http.StatusOK, "help", "help_panel", page)
		return
	}

	topic, err := a.helpTopicFor(r, key)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	if topic == nil {
		a.notFound(w, r)
		return
	}
	page.Topic = topic
	a.Render.Respond(w, r, http.StatusOK, "help", "help_panel", page)
}

func (a *App) helpIndex(r *http.Request) ([]helpIndexEntry, error) {
	var out []helpIndexEntry
	for _, v := range a.vocabTopics() {
		terms, err := v.Read(r)
		if err != nil {
			return nil, err
		}
		out = append(out, helpIndexEntry{Key: v.Key, Title: v.Title, Editable: true, Count: len(terms)})
	}
	for _, t := range help.Topics {
		out = append(out, helpIndexEntry{Key: t.Key, Title: t.Title, Count: len(t.Terms)})
	}
	// Editable ones first, then alphabetically inside each group: the panel is
	// a reference, and a reference that reorders itself is hard to use twice.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Editable != out[j].Editable {
			return out[i].Editable
		}
		return out[i].Title < out[j].Title
	})
	return out, nil
}

func (a *App) helpTopicFor(r *http.Request, key string) (*helpTopic, error) {
	for _, v := range a.vocabTopics() {
		if v.Key != key {
			continue
		}
		terms, err := v.Read(r)
		if err != nil {
			return nil, err
		}
		topic := &helpTopic{Key: v.Key, Title: v.Title, Intro: v.Intro, Editable: true}
		for _, t := range terms {
			topic.Terms = append(topic.Terms, helpTerm{Code: t.Code, Label: t.Label, Description: t.Description})
		}
		return topic, nil
	}
	if t, ok := help.Lookup(key); ok {
		topic := &helpTopic{Key: t.Key, Title: t.Title, Intro: t.Intro}
		for _, term := range t.Terms {
			topic.Terms = append(topic.Terms, helpTerm{Code: term.Code, Label: term.Label, Description: term.Description})
		}
		return topic, nil
	}
	return nil, nil
}
