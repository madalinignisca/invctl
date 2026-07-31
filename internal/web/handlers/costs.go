package handlers

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/store"
	"github.com/gabriel/invctl/internal/web/render"
)

// Cost lines, on the three things that can carry one.
//
// The three surfaces are one handler each because their redirect target and
// their store method differ and nothing else does. Resisting a generic
// "/costs/{type}/{id}" is deliberate: an entity type arriving in a URL is an
// entity type arriving from a request, and the table name it selects would then
// be one validation slip away from being attacker-chosen.

// CostAddToAsset attaches a cost line to an asset.
func (a *App) CostAddToAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, err := costFromForm(r, a.Store.Now())
	if err == nil {
		err = a.Store.AddAssetCost(r.Context(), actor(r), id, c)
	}
	a.afterCostWrite(w, r, err, "/assets/"+id)
}

// CostAddToService attaches a cost line to a service.
func (a *App) CostAddToService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, err := costFromForm(r, a.Store.Now())
	if err == nil {
		err = a.Store.AddServiceCost(r.Context(), actor(r), id, c)
	}
	a.afterCostWrite(w, r, err, "/services/"+id)
}

// CostAddToProject attaches a cost line to a project directly: the money that
// belongs to no box and no service anybody here runs.
func (a *App) CostAddToProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, err := costFromForm(r, a.Store.Now())
	if err == nil {
		err = a.Store.AddProjectCost(r.Context(), actor(r), id, c)
	}
	a.afterCostWrite(w, r, err, "/projects/"+id)
}

// CostRetireOnAsset soft-deletes a line on an asset.
func (a *App) CostRetireOnAsset(w http.ResponseWriter, r *http.Request) {
	err := a.Store.RetireAssetCost(r.Context(), actor(r), r.PathValue("costID"))
	a.afterCostWrite(w, r, err, "/assets/"+r.PathValue("id"))
}

// CostRetireOnService soft-deletes a line on a service.
func (a *App) CostRetireOnService(w http.ResponseWriter, r *http.Request) {
	err := a.Store.RetireServiceCost(r.Context(), actor(r), r.PathValue("costID"))
	a.afterCostWrite(w, r, err, "/services/"+r.PathValue("id"))
}

// CostRetireOnProject soft-deletes a line on a project.
func (a *App) CostRetireOnProject(w http.ResponseWriter, r *http.Request) {
	err := a.Store.RetireProjectCost(r.Context(), actor(r), r.PathValue("costID"))
	a.afterCostWrite(w, r, err, "/projects/"+r.PathValue("id"))
}

// afterCostWrite sends the operator back where they were, or tells them why not.
//
// A validation failure flashes and redirects rather than re-rendering the form
// partial, which is the one place this feature departs from the house pattern.
// The cost form lives inside a detail page assembled from a dozen queries; the
// 422-with-the-form-partial rule exists so an operator does not lose what they
// typed, and here the honest trade is that they lose one short row rather than
// the handler growing a second full page assembly it would then have to keep in
// step. The message names the field either way.
func (a *App) afterCostWrite(w http.ResponseWriter, r *http.Request, err error, back string) {
	if err != nil {
		if messages, ok := validationErrors(err); ok {
			a.setFlash(r, "error", "That cost line was not accepted: "+joinMessages(messages))
			render.Redirect(w, r, back)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, back)
}

// costFromForm reads a cost line out of a submitted form.
func costFromForm(r *http.Request, now time.Time) (*domain.Cost, error) {
	amount, amountErr := parseAmountMinor(formValue(r, "amount"))

	spec := domain.CostSpec{
		Kind:        formValue(r, "kind"),
		Period:      formValue(r, "period"),
		AmountMinor: amount,
		Note:        optional(formValue(r, "note")),
		ValidFrom:   optional(formValue(r, "valid_from")),
		ValidUntil:  optional(formValue(r, "valid_until")),
	}
	c, err := domain.NewCost(store.NewID(), spec, now)
	if err != nil {
		return nil, err
	}
	if amountErr != nil {
		return nil, amountErr
	}
	return c, nil
}

// parseAmountMinor turns what a person typed into minor units.
//
// It accepts "1200", "1200.50" and "1 200,50", because an operator typing a
// price into a web form is not going to think about which of those this expects
// and being told off for a space is the kind of friction that stops a field
// being filled in at all. What it will NOT do is guess at "1.200,50" versus
// "1,200.50" when both separators appear: that is genuinely ambiguous, differs
// by a factor of a thousand, and a wrong guess is a silently wrong budget.
func parseAmountMinor(raw string) (int64, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0, nil
	}
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, " ", "")

	hasComma, hasDot := strings.Contains(s, ","), strings.Contains(s, ".")
	if hasComma && hasDot {
		return 0, amountError("use either a comma or a point for the decimals, not both")
	}
	s = strings.Replace(s, ",", ".", 1)

	whole, frac, split := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	major, err := strconv.ParseInt(whole, 10, 64)
	if err != nil || major < 0 {
		return 0, amountError("must be a number, like 1200 or 1200.50")
	}
	var minor int64
	if split {
		switch len(frac) {
		case 0:
			// "1200." is a person mid-typing, not an error.
		case 1:
			frac += "0"
			fallthrough
		case 2:
			minor, err = strconv.ParseInt(frac, 10, 64)
			if err != nil {
				return 0, amountError("must be a number, like 1200 or 1200.50")
			}
		default:
			return 0, amountError("has more than two decimal places")
		}
	}
	// Overflow would wrap silently into a negative amount, which the domain
	// then rejects with a message about negatives that would baffle anybody.
	const maxMajor = (1 << 62) / 100
	if major > maxMajor {
		return 0, amountError("is larger than this system can hold")
	}
	return major*100 + minor, nil
}

func amountError(message string) error {
	ve := &domain.ValidationError{}
	ve.Add("amount", "%s", message)
	return ve.OrNil()
}

func joinMessages(messages map[string]string) string {
	parts := make([]string, 0, len(messages))
	for field, message := range messages {
		parts = append(parts, field+" "+message)
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}
