// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/web/render"
)

// The inflation series (WP-J2): reference data an operator maintains.
//
// A PAGE RATHER THAN A SETTING, because it is a table with a row per year and
// each row wants a source beside it. It sits under Settings with the
// vocabularies, which is the other reference data typed by a person.

type inflationPage struct {
	Base
	Rates  []domain.InflationRate
	Errors map[string]string
}

// InflationList shows the series and the form to add a year.
func (a *App) InflationList(w http.ResponseWriter, r *http.Request) {
	a.renderInflation(w, r, http.StatusOK, nil)
}

func (a *App) renderInflation(w http.ResponseWriter, r *http.Request,
	status int, errs map[string]string) {

	rates, err := a.Store.ListInflationRates(r.Context())
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	a.Render.Respond(w, r, status, "inflation", "inflation_table", inflationPage{
		Base:   a.base(r, "Inflation", "inflation"),
		Rates:  rates,
		Errors: orEmpty(errs),
	})
}

// InflationSet records or corrects one year.
func (a *App) InflationSet(w http.ResponseWriter, r *http.Request) {
	year, yearErr := strconv.Atoi(strings.TrimSpace(formValue(r, "year")))
	bp, bpErr := parsePercentBasisPoints(formValue(r, "percent"))

	errs := map[string]string{}
	if yearErr != nil {
		errs["year"] = "must be a four-digit year"
	}
	if bpErr != nil {
		errs["percent"] = bpErr.Error()
	}
	if len(errs) > 0 {
		a.renderInflation(w, r, http.StatusUnprocessableEntity, errs)
		return
	}

	rate := &domain.InflationRate{
		Year: year, BasisPoints: bp, Source: optional(formValue(r, "source")),
	}
	if err := a.Store.SetInflationRate(r.Context(), a.permit(r), rate); err != nil {
		var ve *domain.ValidationError
		if errors.As(err, &ve) {
			a.renderInflation(w, r, http.StatusUnprocessableEntity, ve.Messages())
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/inflation")
}

// parsePercentBasisPoints turns what a person typed into hundredths of a
// percent.
//
// It accepts "3.2", "3,2" and "3" for the same reason parseAmountMinor accepts
// three shapes of money: somebody typing a rate off a statistics page is not
// going to think about which separator this expects. The result is an integer,
// because rates get compounded and floats drift in the last place.
func parsePercentBasisPoints(raw string) (int, error) {
	s := strings.TrimSpace(strings.ReplaceAll(raw, ",", "."))
	s = strings.TrimSuffix(s, "%")
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("is required, as a percentage")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, errors.New("must be a percentage, like 3.2")
	}
	// Rounded rather than truncated: 3.209 typed from a source quoting three
	// decimals should become 3.21, not 3.20.
	return int(math.Round(f * 100)), nil
}
