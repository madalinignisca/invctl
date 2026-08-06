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
	"strconv"

	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// Vocabulary administration: the lookup tables an estate owns.
//
// Admin-only and audited, because a vocabulary term is declared state that
// other rows point at with a foreign key -- adding or rewording one changes
// what the estate can express. The engine-defined enums are deliberately
// absent: they are explained at /help and cannot be edited, because their
// meaning is what the impact code does with them.

type vocabPage struct {
	Base
	Errors map[string]string
	// Table is the selected lookup table; Tables is the picker.
	Table  string
	Title  string
	Tables []vocabChoice
	Terms  []store.VocabularyTerm
	// Editing is the code being edited, empty when adding.
	Editing string
}

type vocabChoice struct {
	Key, Title string
	Count      int
}

// VocabularyList shows one lookup table and the form to add or edit a term.
func (a *App) VocabularyList(w http.ResponseWriter, r *http.Request) {
	a.renderVocab(w, r, http.StatusOK, nil, r.URL.Query().Get("edit"))
}

// VocabularyUpsert adds a term or updates an existing one.
func (a *App) VocabularyUpsert(w http.ResponseWriter, r *http.Request) {
	table := a.vocabTable(r)
	if table == "" {
		a.notFound(w, r)
		return
	}
	order, _ := strconv.Atoi(formValue(r, "sort_order"))
	term := store.VocabularyTerm{
		Code:        formValue(r, "code"),
		Label:       formValue(r, "label"),
		SortOrder:   order,
		Description: formValue(r, "description"),
	}

	if err := a.Store.UpsertVocabularyTerm(r.Context(), actor(r), table, term); err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderVocab(w, r, http.StatusUnprocessableEntity, errs, term.Code)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/vocabularies?table="+table)
}

// vocabTable resolves the requested table against the store's allow-list, so a
// table name never reaches SQL from a request.
func (a *App) vocabTable(r *http.Request) string {
	want := r.URL.Query().Get("table")
	if want == "" {
		want = formValue(r, "table")
	}
	if want == "" {
		return "service_kind" // The one the conversation started with.
	}
	for _, name := range store.VocabularyTables() {
		if name == want {
			return name
		}
	}
	return ""
}

func (a *App) renderVocab(w http.ResponseWriter, r *http.Request, status int, errs map[string]string, editing string) {
	table := a.vocabTable(r)
	if table == "" {
		a.notFound(w, r)
		return
	}

	page := vocabPage{
		Base:    a.base(r, "Vocabularies", "vocabularies"),
		Errors:  orEmpty(errs),
		Table:   table,
		Editing: editing,
	}
	for _, v := range a.vocabTopics() {
		terms, err := v.Read(r)
		if err != nil {
			a.serverError(w, r, err)
			return
		}
		page.Tables = append(page.Tables, vocabChoice{Key: v.Key, Title: v.Title, Count: len(terms)})
		if v.Key == table {
			page.Title, page.Terms = v.Title, terms
		}
	}
	a.Render.Respond(w, r, status, "vocabularies", "vocabulary_panel", page)
}
