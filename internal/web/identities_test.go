// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package web_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
	"github.com/madalinignisca/invctl/internal/web/handlers"
)

// TestADependencyNamingARetiredIdentityKeepsIt is the regression the filter
// change could have introduced. See the comment at the two ListIdentities call
// sites: an option that vanishes from a <select> does not leave the field
// unchanged, it clears it.
func TestADependencyNamingARetiredIdentityKeepsIt(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)

	identity, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-still-named",
		Realm: strPtr("vault"),
	})
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, identity); err != nil {
		t.Fatalf("creating identity: %v", err)
	}

	consumer := h.refs.Services["orders-api"]
	endpoint := h.lookup(`SELECT id FROM endpoint ORDER BY id LIMIT 1`)
	dep, err := domain.NewDependency(store.NewID(), domain.DependencySpec{
		ConsumerServiceID:  consumer,
		ProviderEndpointID: &endpoint,
		Nature:             domain.NatureHard,
		FailureMode:        "orders cannot authenticate",
		IdentityID:         &identity.ID,
	}, h.store.Now())
	if err != nil {
		t.Fatalf("building dependency: %v", err)
	}
	if err := h.store.CreateDependency(ctx, admin, dep, nil); err != nil {
		t.Fatalf("creating dependency: %v", err)
	}

	// The dependency correction row opens on ?dep=, not ?edit= -- ?edit= is
	// spent on the service form itself (services.go's own comment: "this page
	// already spends EditRow on the service itself AND on an endpoint row").
	page := "/services/" + consumer + "?dep=" + dep.ID
	marker := `value="` + identity.ID + `"`

	// POSITIVE CONTROL: the option is there while the credential is live, so
	// the assertion after the withdrawal is about the filter and not about
	// the picker never having rendered.
	before := body(t, h.get(page, false))
	if !strings.Contains(before, marker) {
		t.Fatalf("the correction row does not offer %s even while it is live; this test "+
			"would then prove nothing", identity.Name)
	}

	if err := h.store.RetireIdentity(ctx, admin, identity.ID); err != nil {
		t.Fatalf("withdrawing the credential: %v", err)
	}

	after := body(t, h.get(page, false))
	if !strings.Contains(after, marker) {
		t.Fatalf("the correction row no longer offers the RETIRED credential the " +
			"dependency names. An option that vanishes from a <select> does not leave " +
			"the field unchanged -- the browser falls back to the empty first option, " +
			"and saving the row silently clears identity_id on the edges of exactly " +
			"the credential somebody is mid-incident about. ListIdentities is called " +
			"with IncludeRetired: true at internal/web/handlers/deps.go and " +
			"services.go for this reason.")
	}
	// And it is still the SELECTED one on the correction row, not merely
	// present somewhere on the page. The identity <select> appears twice --
	// once on the "add a dependency" create form (forms.html:646, which never
	// marks a selection because nothing is being corrected there) and once on
	// this dependency's own inline correction row (rows.html:69, which does).
	// Scanning every occurrence of the marker rather than only the first is
	// what makes this assertion about the CORRECTION row specifically, rather
	// than about whichever <select> the page happens to render first.
	foundSelected := false
	for idx := strings.Index(after, marker); idx != -1; {
		option := after[idx:min(idx+200, len(after))]
		if strings.Contains(option, "selected") {
			foundSelected = true
			break
		}
		rest := strings.Index(after[idx+1:], marker)
		if rest == -1 {
			break
		}
		idx = idx + 1 + rest
	}
	if !foundSelected {
		t.Error("the retired credential is offered but not selected on the correction " +
			"row, so saving it would post a different value than the one stored")
	}
}

// identityRowContaining returns the <tr>...</tr> holding marker, or "" if marker does
// not appear in an intact row. Used so an assertion about the vocabulary a
// single row does NOT carry cannot accidentally pass by reading a sibling row
// -- "due in" legitimately appears elsewhere on the same page.
func identityRowContaining(page, marker string) string {
	idx := strings.Index(page, marker)
	if idx == -1 {
		return ""
	}
	start := strings.LastIndex(page[:idx], "<tr>")
	end := strings.Index(page[idx:], "</tr>")
	if start == -1 || end == -1 {
		return ""
	}
	return page[start : idx+end]
}

// TestTheIdentityListNeverRendersASecretPath is the spec's hardest read rule:
// "One screen listing every credential path in the estate is the reconnaissance
// gift ... in its most convenient possible form, and it is one CSV export away
// from leaving the building." Not for an Administrator either -- for ANYBODY.
func TestTheIdentityListNeverRendersASecretPath(t *testing.T) {
	h := newHarness(t)
	// A POSITIVE CONTROL FIRST: the fixture must actually hold a path, or this
	// test passes by finding nothing because there was nothing.
	stored := h.lookup(`SELECT secret_ref FROM identity WHERE secret_ref IS NOT NULL LIMIT 1`)
	if stored == "" {
		t.Fatal("no seeded identity carries a secret_ref, so this test proves nothing")
	}
	for i, who := range []struct{ user, pass string }{
		{"admin", "admin-password"},
		{"viewer", "viewer-password"},
	} {
		if i > 0 {
			h.logout()
		}
		h.login(who.user, who.pass)
		page := body(t, h.get("/identities", false))
		if strings.Contains(page, stored) {
			t.Errorf("the identity list rendered %q to %s. The list shows only WHETHER a "+
				"path is recorded; the path renders on the detail page, to an "+
				"Administrator, one credential at a time.", stored, who.user)
		}
		if !strings.Contains(page, "recorded") {
			t.Errorf("the list does not say whether a path is recorded at all for %s", who.user)
		}
	}
}

// TestSecretRefOnTheDetailPageIsAdministratorOnly. depRowData.SecretRef's own
// comment is the reasoning: "a template-side {{if .IsAdmin}} around
// .Dep.IdentitySecretRef is one {{end}} away from leaking it, and it does
// nothing at all for a CSV export, which never passes through a template." So
// it is computed in the view model, and this drives both sides.
func TestSecretRefOnTheDetailPageIsAdministratorOnly(t *testing.T) {
	h := newHarness(t)

	// The fixture builds its own credential rather than leaning on the seed, so
	// this task's tests do not depend on Task 6 having landed. The path is
	// distinctive enough that finding it in a page cannot be a coincidence.
	const path = "kv/prod/identity-disclosure-test/db"
	ninetyDays := 90
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)
	identity, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-disclosure-test",
		Realm: strPtr("vault"), SecretRef: strPtr(path), RotationDays: &ninetyDays,
	})
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, identity); err != nil {
		t.Fatalf("creating identity: %v", err)
	}

	t.Run("an administrator sees the path", func(t *testing.T) {
		h.login("admin", "admin-password")
		page := body(t, h.get("/identities/"+identity.ID, false))
		if !strings.Contains(page, path) {
			t.Errorf("the detail page does not render the secret path to an Administrator. " +
				"The path renders HERE, one credential at a time -- that is the whole " +
				"trade the list page's blanket refusal buys.")
		}
	})

	t.Run("a read-only user sees the page and not the path", func(t *testing.T) {
		h.logout()
		h.login("viewer", "viewer-password")
		resp := h.get("/identities/"+identity.ID, false)
		page := body(t, resp)
		// The GET is deliberately NOT gated: name, realm, kind, team and
		// rotation status are what somebody needs mid-incident.
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET the detail page as viewer returned %d, want 200 -- the read "+
				"surface is open to any authenticated user", resp.StatusCode)
		}
		if !strings.Contains(page, identity.Name) {
			t.Errorf("the page does not name the credential at all, so the assertion " +
				"below would pass on an error page rather than on redaction")
		}
		if strings.Contains(page, path) {
			t.Errorf("the detail page rendered %q to a read-only user. The gate lives in "+
				"the handler's view model, not the template -- depRowData.SecretRef's "+
				"comment: a template-side {{if .IsAdmin}} is one {{end}} away from "+
				"leaking it, and it does nothing at all for a CSV export.", path)
		}
	})
}

// TestTheIdentityListRendersEveryRotationState is the reason the page exists.
// Five identities, five pills, and RotationUnreadable must NOT read as healthy.
func TestTheIdentityListRendersEveryRotationState(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)
	now := h.store.Now()
	ninety := 90

	mk := func(name string, rotationDays *int) *domain.Identity {
		t.Helper()
		i, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
			Kind: domain.IdentityServiceAccount, Name: name, Realm: strPtr("vault"),
			RotationDays: rotationDays,
		})
		if err != nil {
			t.Fatalf("building %s: %v", name, err)
		}
		if err := h.store.CreateIdentity(ctx, admin, i); err != nil {
			t.Fatalf("creating %s: %v", name, err)
		}
		return i
	}

	mk("svc-state-unmanaged", nil)
	mk("svc-state-never-recorded", &ninety)

	within := mk("svc-state-within", &ninety)
	if err := h.store.RecordIdentityRotation(ctx, admin, within.ID,
		domain.FormatDate(now.AddDate(0, 0, -30))); err != nil {
		t.Fatalf("rotating within: %v", err)
	}

	overdue := mk("svc-state-overdue", &ninety)
	if err := h.store.RecordIdentityRotation(ctx, admin, overdue.ID,
		domain.FormatDate(now.AddDate(0, 0, -200))); err != nil {
		t.Fatalf("rotating overdue: %v", err)
	}

	unreadable := mk("svc-state-unreadable", &ninety)
	// THE ONLY WAY TO REACH RotationUnreadable: written past the Go layer with
	// a value the CHECK allows (ten characters, dashes in positions 5 and 8)
	// and ParseDate refuses (there is no 31st of February). RecordIdentityRotation
	// and NewIdentity both refuse this value on the way in, by design.
	//
	// THIS IS WHY TASK 4 TESTS THE STATE AND TASK 6 DOES NOT SEED IT -- a
	// seeded corrupt row would teach every reader of the demo that the estate
	// produces them, which it does not and must not.
	if _, err := h.store.DB().Writer.ExecContext(ctx,
		h.store.DB().Writer.Rebind(`UPDATE identity SET last_rotated = ? WHERE id = ?`),
		"2026-02-31", unreadable.ID); err != nil {
		t.Fatalf("writing the unreadable date past the Go layer: %v", err)
	}

	page := body(t, h.get("/identities", false))
	for _, want := range []string{"no policy", "never recorded", "due in", "overdue by", "date unreadable"} {
		if !strings.Contains(page, want) {
			t.Errorf("the list never renders %q", want)
		}
	}

	// The unreadable row must not carry ANY of the healthy vocabulary. Asserted
	// against the row rather than the page, because "due in" legitimately
	// appears on the within-window row three lines above it.
	unreadableRow := identityRowContaining(page, `href="/identities/`+unreadable.ID+`"`)
	if unreadableRow == "" {
		t.Fatal("could not find the unreadable identity's own row on the page")
	}
	for _, healthy := range []string{"due in", "within", "no policy"} {
		if strings.Contains(unreadableRow, healthy) {
			t.Errorf("the row for a credential whose stored date will not parse renders "+
				"%q. A state that cannot be READ must never render as a state that is "+
				"FINE -- the boolean this replaced returned false on a parse error, "+
				"which is exactly how an unreadable value read as healthy.", healthy)
		}
	}
}

// TestTheIdentityDetailPageStatesAllThreeRotationFactsPlainly: the policy, the
// last recorded rotation (never a blank -- "never recorded"), and the derived
// state.
func TestTheIdentityDetailPageStatesAllThreeRotationFactsPlainly(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)
	now := h.store.Now()

	mk := func(t *testing.T, name string, rotationDays int, rotatedDaysAgo int) string {
		t.Helper()
		spec := domain.IdentitySpec{
			Kind: domain.IdentityServiceAccount, Name: name, Realm: strPtr("vault"),
		}
		if rotationDays > 0 {
			spec.RotationDays = &rotationDays
		}
		i, err := domain.NewIdentity(store.NewID(), spec)
		if err != nil {
			t.Fatalf("building %s: %v", name, err)
		}
		if err := h.store.CreateIdentity(ctx, admin, i); err != nil {
			t.Fatalf("creating %s: %v", name, err)
		}
		if rotatedDaysAgo > 0 {
			date := domain.FormatDate(now.AddDate(0, 0, -rotatedDaysAgo))
			if err := h.store.RecordIdentityRotation(ctx, admin, i.ID, date); err != nil {
				t.Fatalf("rotating %s: %v", name, err)
			}
		}
		return i.ID
	}

	withinID := mk(t, "svc-within", 90, 30)
	neverID := mk(t, "svc-never", 90, 0)
	unmanagedID := mk(t, "svc-unmanaged", 0, 0)

	t.Run("a rotated credential states the policy, the date and the state", func(t *testing.T) {
		page := body(t, h.get("/identities/"+withinID, false))
		for _, want := range []string{
			"every 90 days", // the policy
			domain.FormatDate(now.AddDate(0, 0, -30)), // the last recorded rotation
			"due on", // the derived state
		} {
			if !strings.Contains(page, want) {
				t.Errorf("the rotation panel does not state %q. All three facts are stated "+
					"plainly and none is collapsed into another.", want)
			}
		}
	})

	t.Run("a credential with a policy and no record says so, never a blank", func(t *testing.T) {
		page := body(t, h.get("/identities/"+neverID, false))
		if !strings.Contains(page, "every 90 days") {
			t.Error("the policy is not stated, so a reader cannot tell there is a rule at all")
		}
		if !strings.Contains(page, "never recorded") {
			t.Error("the last rotation reads as something other than 'never recorded'. A " +
				"BLANK here is the defect: it reads as a field nobody filled in rather " +
				"than as the estate having a rule with no evidence it was ever followed.")
		}
	})

	t.Run("an unmanaged credential says there is no policy, and is not overdue", func(t *testing.T) {
		page := body(t, h.get("/identities/"+unmanagedID, false))
		if !strings.Contains(page, "no rotation policy") {
			t.Error("the panel does not say there is no rotation policy")
		}
		// The two states this page must never confuse. `unmanaged` has no rule
		// to break; reporting it as late is how a page teaches people to ignore
		// it, which is the reasoning EstateFindings already applies to expected
		// power convergence.
		for _, wrong := range []string{"overdue", "never recorded"} {
			if strings.Contains(page, wrong) {
				t.Errorf("a credential with no rotation policy renders %q. There is nothing "+
					"for it to be late for, and nothing anybody failed to record.", wrong)
			}
		}
	})
}

// TestTheIdentityDetailPageNamesWhatWouldNotice drives the used-by panel,
// including the case the spec names: a LIVE dependency naming a RETIRED identity
// still displays it, marked retired.
func TestTheIdentityDetailPageNamesWhatWouldNotice(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)
	ninetyDays := 90

	t.Run("a Windows service naming a credential appears", func(t *testing.T) {
		// THE SEEDED ESTATE ALREADY HAS THIS ONE, and it is the real thing
		// rather than a fixture: seed_services.go:260 makes svc-backup$ the
		// logon account of a Windows service, because "the run-as account is
		// the usual reason a Windows service dies after a credential rotation".
		id := h.lookup(`SELECT id FROM identity WHERE name = ?`, "svc-backup$")
		if id == "" {
			t.Fatal("svc-backup$ is not in the fixture, so the rt_windows join is " +
				"checked nowhere -- seed it rather than letting this pass")
		}
		page := body(t, h.get("/identities/"+id, false))
		if !strings.Contains(page, "Veeam") {
			t.Error("the used-by panel does not name the Windows service that logs on as " +
				"this credential. That panel is what makes withdrawal an informed act.")
		}
	})

	t.Run("a live dependency naming a RETIRED credential still shows it", func(t *testing.T) {
		// Built here rather than taken from the seed, so this task does not
		// depend on Task 6. The state is normal by design: migration 00003's
		// response to a compromise is retire-then-replace, so edges keep naming
		// the withdrawn credential until they are re-pointed.
		identity, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
			Kind: domain.IdentityServiceAccount, Name: "svc-compromised",
			Realm: strPtr("vault"), RotationDays: &ninetyDays,
		})
		if err != nil {
			t.Fatalf("building identity: %v", err)
		}
		if err := h.store.CreateIdentity(ctx, admin, identity); err != nil {
			t.Fatalf("creating identity: %v", err)
		}

		consumer := h.refs.Services["orders-api"]
		// Any seeded endpoint will do: this test is about the identity panel,
		// not about which edge it is. lookup fails the test loudly if the
		// fixture has none, rather than substituting an id that resolves to
		// nothing and producing a page that renders empty for the wrong reason.
		endpoint := h.lookup(`SELECT id FROM endpoint ORDER BY id LIMIT 1`)
		dep, err := domain.NewDependency(store.NewID(), domain.DependencySpec{
			ConsumerServiceID:  consumer,
			ProviderEndpointID: &endpoint,
			Nature:             domain.NatureHard,
			FailureMode:        "orders cannot authenticate",
			IdentityID:         &identity.ID,
		}, h.store.Now())
		if err != nil {
			t.Fatalf("building dependency: %v", err)
		}
		if err := h.store.CreateDependency(ctx, admin, dep, nil); err != nil {
			t.Fatalf("creating dependency: %v", err)
		}
		if err := h.store.RetireIdentity(ctx, admin, identity.ID); err != nil {
			t.Fatalf("withdrawing the credential: %v", err)
		}

		page := body(t, h.get("/identities/"+identity.ID, false))
		if !strings.Contains(page, "orders-api") {
			t.Error("the used-by panel is empty for a WITHDRAWN credential that a live " +
				"dependency still names. RetireEnvironment's ruling carries over: what " +
				"is stored keeps displaying. Hiding it here is how somebody concludes " +
				"the withdrawal was clean when a service is still authenticating with it.")
		}
		if !strings.Contains(page, "retired") {
			t.Error("the page does not mark the credential retired, so a reader cannot " +
				"tell the difference between a live credential and a withdrawn one")
		}
	})
}

// TestTheIdentityListFiltersFindARetiredCredential: "so a retired credential can
// be found".
func TestTheIdentityListFiltersFindARetiredCredential(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)

	identity, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-findable-after-withdrawal",
		Realm: strPtr("vault"),
	})
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, identity); err != nil {
		t.Fatalf("creating identity: %v", err)
	}

	// A positive control: it is on the default list BEFORE withdrawal, so the
	// assertion after it is about the filter and not about the row never having
	// rendered at all.
	if page := body(t, h.get("/identities", false)); !strings.Contains(page, identity.Name) {
		t.Fatalf("%s is not on the default list before withdrawal; this test would then "+
			"prove nothing about the filter", identity.Name)
	}
	if err := h.store.RetireIdentity(ctx, admin, identity.ID); err != nil {
		t.Fatalf("withdrawing: %v", err)
	}

	if page := body(t, h.get("/identities", false)); strings.Contains(page, identity.Name) {
		t.Errorf("%s is still on the DEFAULT list after withdrawal. The default is what a "+
			"picker starts from, and a withdrawn credential must not be offered as a new "+
			"choice.", identity.Name)
	}
	page := body(t, h.get("/identities?lifecycle=retired", false))
	if !strings.Contains(page, identity.Name) {
		t.Errorf("%s cannot be found under the retired filter. A credential somebody "+
			"withdrew by mistake, or one an incident review needs to look at, is "+
			"unreachable through the application if this filter does not work -- and "+
			"there is no restore path by design, so finding it is the only recourse.",
			identity.Name)
	}
}

// TestTheLifecycleFilterSurvivesAReload is the registration proof the final
// whole-branch review asked for (residual #2 -- finding #7 was correctly
// fixed, but nothing protected it): the re-reviewer reverted
// identityListPage.Lifecycle and all three `selected` bindings in the
// lifecycle <select>, and `go test ./internal/web/...` stayed green.
// TestTheIdentityListFiltersFindARetiredCredential (above) proves the
// rows are right; it never once looks at the control itself, so a control
// that always renders "active" selected -- regardless of what was asked
// for -- would still pass it.
func TestTheLifecycleFilterSurvivesAReload(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")

	for _, tc := range []struct {
		query string
		want  string // the option VALUE that must carry `selected`
	}{
		{"/identities", ""},
		{"/identities?lifecycle=retired", "retired"},
		{"/identities?lifecycle=any", "any"},
	} {
		t.Run("lifecycle="+tc.want, func(t *testing.T) {
			page := body(t, h.get(tc.query, false))
			sel := lifecycleSelectTag(t, page)
			for _, opt := range []string{"", "retired", "any"} {
				tag := optionTag(t, sel, opt)
				selected := strings.Contains(tag, "selected")
				switch {
				case opt == tc.want && !selected:
					t.Errorf("%s: <option value=%q> is not marked selected, so a "+
						"refresh or a shared link renders the wrong control -- the "+
						"exact regression this test exists to catch.", tc.query, opt)
				case opt != tc.want && selected:
					t.Errorf("%s: <option value=%q> is marked selected but should not "+
						"be; only one option may carry it.", tc.query, opt)
				}
			}
		})
	}
}

// lifecycleSelectTag returns the identity list's lifecycle <select>...</select>,
// so an assertion about which option is selected cannot be answered by some
// other control on the page.
func lifecycleSelectTag(t *testing.T, page string) string {
	t.Helper()
	i := strings.Index(page, `id="ilife"`)
	if i < 0 {
		t.Fatal("no lifecycle select (#ilife) on the identity list page")
	}
	end := strings.Index(page[i:], "</select>")
	if end < 0 {
		t.Fatal("the lifecycle select is never closed")
	}
	return page[i : i+end]
}

// optionTag returns one <option ...> opening tag from sel, identified by its
// value attribute, so `selected` can be checked against exactly that option
// and not against text belonging to a sibling option.
func optionTag(t *testing.T, sel, value string) string {
	t.Helper()
	marker := `value="` + value + `"`
	i := strings.Index(sel, marker)
	if i < 0 {
		t.Fatalf("no <option %s> in the lifecycle select", marker)
	}
	end := strings.Index(sel[i:], ">")
	if end < 0 {
		t.Fatal("the option tag is never closed")
	}
	return sel[i : i+end]
}

// ---------- the write surface (WP-J8 Task 5) ----------

// TestRecordingARotationThroughTheRoute is the end-to-end of the whole
// package: POST the rotation route, see the date and a "within window" state
// on the detail page, and see the change_log entry it produced in the
// timeline -- change_log IS the rotation history (the table itself keeps
// only the latest date).
func TestRecordingARotationThroughTheRoute(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)
	ninety := 90

	identity, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-rotate-through-route",
		Realm: strPtr("vault"), RotationDays: &ninety,
	})
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, identity); err != nil {
		t.Fatalf("creating identity: %v", err)
	}

	path := "/identities/" + identity.ID
	token := h.csrfToken(path)
	today := domain.FormatDate(h.store.Now())
	resp := h.post(path+"/rotation", url.Values{
		"csrf_token":   {token},
		"last_rotated": {today},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("recording a rotation returned %d, want 303", resp.StatusCode)
	}

	page := body(t, h.get(path, false))
	if !strings.Contains(page, today) {
		t.Error("the detail page does not show the just-recorded rotation date")
	}
	if !strings.Contains(page, "due on") {
		t.Error("the detail page does not show a within-window state right after a fresh rotation")
	}
	// The TIMELINE shows the rotation as a last_rotated change, with an actor.
	if !strings.Contains(page, "last_rotated") {
		t.Error("the timeline does not show the rotation as a last_rotated change. " +
			"change_log IS the rotation history -- the table keeps only the latest " +
			"date -- so a rotation that does not appear here is a rotation nobody " +
			"can audit afterwards.")
	}
}

// TestAFutureRotationIs422WithTheFormReRendered. CLAUDE.md's rule and the
// spec's: never a 200 with the error buried, never a flash-and-redirect.
func TestAFutureRotationIs422WithTheFormReRendered(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)

	identity, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-future-rotation",
		Realm: strPtr("vault"),
	})
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, identity); err != nil {
		t.Fatalf("creating identity: %v", err)
	}

	// Captured BEFORE the refused POST, and compared as a delta rather than
	// against a literal 0: the seeded estate (WP-J8 Task 6) now carries two
	// real rotations of its own (svc-orders, svc-sso), so an absolute count
	// would fail on a fixture that behaves correctly and start passing again
	// only if the seeder regressed to writing none at all.
	before := h.count(`SELECT COUNT(*) FROM identity WHERE last_rotated IS NOT NULL`)

	path := "/identities/" + identity.ID
	token := h.csrfToken(path)
	tomorrow := domain.FormatDate(h.store.Now().AddDate(0, 0, 1))
	resp := h.post(path+"/rotation", url.Values{
		"csrf_token":   {token},
		"last_rotated": {tomorrow},
	}, false)
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("a post-dated rotation returned %d, want 422", resp.StatusCode)
	}
	page := body(t, resp)
	// the typed value survives, and the message is against the field
	if !strings.Contains(page, tomorrow) {
		t.Error("the refused form did not hand back what the operator typed")
	}
	if !strings.Contains(page, "cannot be recorded in the future") {
		t.Errorf("the refusal does not say why the date was rejected, so it reads as a " +
			"generic failure on a date that is well-formed")
	}
	// and nothing new was written
	if after := h.count(`SELECT COUNT(*) FROM identity WHERE last_rotated IS NOT NULL`); after != before {
		t.Errorf("last_rotated count went from %d to %d after a refused rotation. A 422 that "+
			"still wrote would be worse than no refusal at all, because the page says "+
			"it failed.", before, after)
	}
}

// TestARotationAgainstARetiredIdentityIs422AndNamesIt.
func TestARotationAgainstARetiredIdentityIs422AndNamesIt(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)
	ninetyDays := 90

	identity, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-withdrawn-rotation",
		Realm: strPtr("vault"), RotationDays: &ninetyDays,
	})
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, identity); err != nil {
		t.Fatalf("creating identity: %v", err)
	}
	if err := h.store.RetireIdentity(ctx, admin, identity.ID); err != nil {
		t.Fatalf("withdrawing: %v", err)
	}

	path := "/identities/" + identity.ID
	token := h.csrfToken(path)
	today := domain.FormatDate(h.store.Now())
	resp := h.post(path+"/rotation", url.Values{
		"csrf_token":   {token},
		"last_rotated": {today},
	}, false)
	page := body(t, resp)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("recording a rotation against a withdrawn credential returned %d, want "+
			"422. A 303 would tell the operator it worked; a 500 would tell them the "+
			"software broke. Neither is true.", resp.StatusCode)
	}
	if !strings.Contains(page, identity.Name) {
		t.Errorf("the refusal does not name %q. An operator with several credentials "+
			"open needs to know which one was withdrawn.", identity.Name)
	}
	if !strings.Contains(page, "withdrawn") {
		t.Errorf("the refusal does not say the credential was withdrawn, so it reads as " +
			"a malformed-date error for a date that is fine")
	}
	// The form comes back, with what was typed still in it.
	if !strings.Contains(page, today) {
		t.Errorf("the refused form did not hand back the date the operator typed")
	}
	// And nothing was written.
	if n := h.count(`SELECT COUNT(*) FROM identity WHERE id = ? AND last_rotated IS NOT NULL`,
		identity.ID); n != 0 {
		t.Errorf("last_rotated was written despite the refusal")
	}
}

// TestAStaleIdentityCorrectionIs409. refusalStatus separates the two reasons a
// save comes back: 422 says "what you typed is wrong", 409 says "somebody else
// got there first".
func TestAStaleIdentityCorrectionIs409(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)

	identity, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-concurrent",
		Realm: strPtr("vault"),
	})
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, identity); err != nil {
		t.Fatalf("creating identity: %v", err)
	}

	path := "/identities/" + identity.ID
	// BOTH SUBMISSIONS CARRY row_version = 1, which is what two operators who
	// opened the form at the same time would send. The token comes from the
	// page each of them is looking at, not from the row.
	form := func(name string) url.Values {
		return url.Values{
			"csrf_token":  {h.csrfToken(path)},
			"kind":        {domain.IdentityServiceAccount},
			"name":        {name},
			"realm":       {"vault"},
			"row_version": {"1"},
		}
	}

	first := h.post(path, form("svc-concurrent-first"), false)
	first.Body.Close()
	if first.StatusCode != http.StatusSeeOther {
		t.Fatalf("the first correction returned %d, want 303 -- if this did not succeed "+
			"the second one is not stale and this test proves nothing", first.StatusCode)
	}

	second := h.post(path, form("svc-concurrent-second"), false)
	page := body(t, second)
	if second.StatusCode != http.StatusConflict {
		t.Fatalf("the second correction returned %d, want 409. refusalStatus separates "+
			"the two reasons a save comes back: 422 says what you typed is wrong, 409 "+
			"says it was fine and somebody else got there first. Answering this with "+
			"422 tells the operator to fix input that has nothing wrong with it.",
			second.StatusCode)
	}
	if !strings.Contains(page, "svc-concurrent-second") {
		t.Errorf("the 409 did not hand back what the second operator typed. Their text " +
			"is the thing they are about to re-apply, and discarding it makes the " +
			"message 'go and read the other edit first' into 'retype everything'.")
	}
	// The first operator's write stands: the loser overwrites nobody.
	stored := h.lookup(`SELECT name FROM identity WHERE id = ?`, identity.ID)
	if stored != "svc-concurrent-first" {
		t.Errorf("stored name = %q, want the FIRST operator's. A stale write that landed "+
			"is the silent revert this token exists to prevent, and change_log would "+
			"record it as a deliberate act by whoever was slower.", stored)
	}
}

// TestTheIdentityWriteRoutesAreAdministratorOnly is the route-gate assertion in
// its own right, beside the generated census in rbac_boundary_test.go. identity
// is ScopeEstateConfig, so tx.log would refuse a project owner's write anyway --
// this DIVERGES DELIBERATELY and gates at the door, because a correction form
// has to RENDER secret_ref to be a correction form, and a form that silently
// omits a field blanks the column on save. Refusing before the form is filled in
// is also the honest order.
func TestTheIdentityWriteRoutesAreAdministratorOnly(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)

	identity, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-gate-check",
		Realm: strPtr("vault"),
	})
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, identity); err != nil {
		t.Fatalf("creating identity: %v", err)
	}
	id := identity.ID

	h.login("viewer", "viewer-password")
	for _, path := range []string{
		"/identities", "/identities/" + id, "/identities/" + id + "/retire",
		"/identities/" + id + "/rotation",
	} {
		token := h.csrfToken("/identities")
		resp := h.post(path, url.Values{"csrf_token": {token}}, false)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("POST %s as a read-only user returned %d, want 403. All four writes "+
				"are writeAdminOnly, and the reason is secret_ref: a correction form has "+
				"to RENDER the stored path to be a correction form.", path, resp.StatusCode)
		}
	}
	// and the GETs are NOT gated
	for _, path := range []string{"/identities", "/identities/" + id} {
		resp := h.get(path, false)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s as a read-only user returned %d, want 200. The GETs stay "+
				"readable by any authenticated user: name, realm, kind, team and "+
				"rotation status are what somebody needs mid-incident, and none of it "+
				"is sensitive.", path, resp.StatusCode)
		}
	}
}

// TestTheIdentityEditFormIsNotRenderedToANonAdministrator.
func TestTheIdentityEditFormIsNotRenderedToANonAdministrator(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)
	ninetyDays := 90

	identity, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-form-visibility",
		Realm: strPtr("vault"), SecretRef: strPtr("kv/prod/form-visibility"),
		RotationDays: &ninetyDays,
	})
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, identity); err != nil {
		t.Fatalf("creating identity: %v", err)
	}
	path := "/identities/" + identity.ID

	// The three controls, each identified by the route it posts to rather than
	// by button text, so a reworded button does not silently empty this test.
	controls := map[string]string{
		"the correction form": `action="` + path + `"`,
		"the rotation form":   `action="` + path + `/rotation"`,
		"the withdraw button": `action="` + path + `/retire"`,
	}

	// A POSITIVE CONTROL FIRST. If the admin page does not carry these, the
	// viewer assertions below pass because the markup does not exist at all,
	// which is the vacuous-pass shape this repo keeps finding.
	h.login("admin", "admin-password")
	adminPage := body(t, h.get(path, false))
	for name, marker := range controls {
		if !strings.Contains(adminPage, marker) {
			t.Fatalf("%s is absent from the page for an Administrator (looking for %q). "+
				"The checks below would then prove nothing.", name, marker)
		}
	}

	h.logout()
	h.login("viewer", "viewer-password")
	resp := h.get(path, false)
	viewerPage := body(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s as viewer returned %d, want 200 -- the read surface is open",
			path, resp.StatusCode)
	}
	for name, marker := range controls {
		if strings.Contains(viewerPage, marker) {
			t.Errorf("%s is rendered to a read-only user. Every one of the four POSTs is "+
				"writeAdminOnly, so a click answers 403 -- offering a form whose only "+
				"outcome is a refusal is the same defect /imports was fixed for. Hiding "+
				"is not the enforcement; the enforcement is "+
				"middleware.RequireAdministrator, and this is the half that stops the "+
				"page lying about what the reader can do.", name)
		}
	}
	// The correction form is the one that would RENDER the secret path, which
	// is why the write gate on these routes is stricter than the permit layer
	// alone would require. Asserted here as well as in the disclosure test,
	// because this is the mechanism and that one is the outcome.
	if strings.Contains(viewerPage, "kv/prod/form-visibility") {
		t.Error("the secret path reached a read-only user through the edit form")
	}
}

// TestTheIdentityRetireButtonPairsHxConfirmWithHxPost is a template-level
// guard for a bug this repo has shipped before: hx-confirm on a plain
// method="post" form (no hx-post beside it) confirms and then submits a real
// browser navigation, which is harmless here but silently defeats the point
// of hx-confirm being asked for at all -- the confirmation dialog becomes
// decorative because the request it is meant to gate is never the one that
// runs.
//
// No general census of this shape exists anywhere in internal/web today
// (checked: no file matches "hx-confirm requires hx-post" or an equivalent
// scan). This is deliberately narrow -- one form, read from source -- rather
// than a new package-wide scan invented for this task; extending it into a
// general census is a separate decision for whoever next needs one.
func TestTheIdentityRetireButtonPairsHxConfirmWithHxPost(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "web", "templates", "partials", "identities.html")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	page := string(raw)

	idx := strings.Index(page, `action="/identities/{{.Identity.ID}}/retire"`)
	if idx == -1 {
		t.Fatal("the withdraw form is not on the page at all; this test would then " +
			"prove nothing about it")
	}
	// The whole opening <form ...> tag, wherever it starts relative to the
	// action attribute found above.
	start := strings.LastIndex(page[:idx], "<form")
	end := strings.Index(page[idx:], ">")
	if start == -1 || end == -1 {
		t.Fatal("could not isolate the withdraw form's opening tag")
	}
	tag := page[start : idx+end]

	if !strings.Contains(tag, "hx-confirm=") {
		t.Fatal("the withdraw form carries no hx-confirm at all; this test would then " +
			"prove nothing about pairing it with hx-post")
	}
	if !strings.Contains(tag, "hx-post=") {
		t.Error("the withdraw form carries hx-confirm with no hx-post beside it. " +
			"Without hx-post, htmx never intercepts the submit and the confirmation " +
			"dialog guards nothing -- a plain form POST runs regardless of what the " +
			"operator answers.")
	}
}

// ---------- coverage for the isConflict branch (fix round 1) ----------
//
// The brief's given IdentityCreate/IdentityUpdate bodies fall through a
// UNIQUE (realm, name) violation to handleStoreError's generic 409 page.
// The auth review ruled the isConflict branch this implementation added
// instead should stand, which means it needs the coverage the brief never
// asked for.

// TestADuplicateNameThroughCreateIs409: declaring a second live credential
// under a realm and name a live one already holds is a 409 naming the
// field, typed values echoed, and nothing written.
func TestADuplicateNameThroughCreateIs409(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)

	existing, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-dup-create",
		Realm: strPtr("vault"),
	})
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, existing); err != nil {
		t.Fatalf("creating identity: %v", err)
	}

	before := h.count(`SELECT COUNT(*) FROM change_log`)
	token := h.csrfToken("/identities")
	resp := h.post("/identities", url.Values{
		"csrf_token": {token},
		"kind":       {domain.IdentityServiceAccount},
		"name":       {"svc-dup-create"},
		"realm":      {"vault"},
	}, false)
	page := body(t, resp)

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("declaring a duplicate (realm, name) returned %d, want 409", resp.StatusCode)
	}
	if !strings.Contains(page, "already exists") {
		t.Error("the refusal does not say a matching identity already exists")
	}
	// Typed values survive: this is a create-form refusal, so .Spec carries
	// them (renderIdentityList's own contract), not a stored row's fields.
	if !strings.Contains(page, `value="svc-dup-create"`) {
		t.Error("the refused create form did not hand back the name that was typed")
	}
	if after := h.count(`SELECT COUNT(*) FROM change_log`); after != before {
		t.Errorf("change_log grew from %d to %d on a refused create", before, after)
	}
	if n := h.count(`SELECT COUNT(*) FROM identity WHERE name = ? AND lifecycle = ?`,
		"svc-dup-create", domain.LifecycleActive); n != 1 {
		t.Errorf("%d live rows named svc-dup-create, want exactly the one seeded above", n)
	}
}

// TestADuplicateNameThroughUpdateIs409: correcting one live credential to
// collide with another live credential's (realm, name) is the same refusal.
func TestADuplicateNameThroughUpdateIs409(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)

	first, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-dup-target",
		Realm: strPtr("vault"),
	})
	if err != nil {
		t.Fatalf("building first identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, first); err != nil {
		t.Fatalf("creating first identity: %v", err)
	}
	second, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-dup-mover",
		Realm: strPtr("vault"),
	})
	if err != nil {
		t.Fatalf("building second identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, second); err != nil {
		t.Fatalf("creating second identity: %v", err)
	}

	before := h.count(`SELECT COUNT(*) FROM change_log`)
	path := "/identities/" + second.ID
	token := h.csrfToken(path)
	resp := h.post(path, url.Values{
		"csrf_token":  {token},
		"kind":        {domain.IdentityServiceAccount},
		"name":        {"svc-dup-target"},
		"realm":       {"vault"},
		"row_version": {"1"},
	}, false)
	page := body(t, resp)

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("correcting into a duplicate (realm, name) returned %d, want 409", resp.StatusCode)
	}
	if !strings.Contains(page, "already exists") {
		t.Error("the refusal does not say a matching identity already exists")
	}
	if !strings.Contains(page, `value="svc-dup-target"`) {
		t.Error("the refused correction form did not hand back the name that was typed")
	}
	if after := h.count(`SELECT COUNT(*) FROM change_log`); after != before {
		t.Errorf("change_log grew from %d to %d on a refused correction", before, after)
	}
	stored := h.lookup(`SELECT name FROM identity WHERE id = ?`, second.ID)
	if stored != "svc-dup-mover" {
		t.Errorf("stored name = %q, want the credential's own name unchanged", stored)
	}
}

// TestARetiredIdentitysNameIsImmediatelyReusable is the entire point of the
// live-scoped uniqueness index (migration 00003): a withdrawn credential's
// (realm, name) does not conflict with a new declaration under the same
// name, which is what makes "retire it and create its replacement under the
// same name" -- the documented response to a compromised credential -- work
// during an incident rather than fail with the very refusal this task just
// added coverage for.
func TestARetiredIdentitysNameIsImmediatelyReusable(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)

	original, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-compromised-reuse",
		Realm: strPtr("vault"),
	})
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, original); err != nil {
		t.Fatalf("creating identity: %v", err)
	}
	if err := h.store.RetireIdentity(ctx, admin, original.ID); err != nil {
		t.Fatalf("withdrawing: %v", err)
	}

	token := h.csrfToken("/identities")
	resp := h.post("/identities", url.Values{
		"csrf_token": {token},
		"kind":       {domain.IdentityServiceAccount},
		"name":       {"svc-compromised-reuse"},
		"realm":      {"vault"},
	}, false)
	page := body(t, resp)
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("declaring a replacement under a WITHDRAWN credential's own name "+
			"returned %d, want 303. The live-scoped uniqueness index exists precisely "+
			"so retire-then-replace works during an incident: %s", resp.StatusCode, page)
	}
	if n := h.count(`SELECT COUNT(*) FROM identity WHERE name = ? AND lifecycle = ?`,
		"svc-compromised-reuse", domain.LifecycleActive); n != 1 {
		t.Errorf("%d live rows named svc-compromised-reuse, want exactly the replacement", n)
	}
	if n := h.count(`SELECT COUNT(*) FROM identity WHERE name = ? AND lifecycle = ?`,
		"svc-compromised-reuse", domain.LifecycleRetired); n != 1 {
		t.Errorf("%d retired rows named svc-compromised-reuse, want the original still there", n)
	}
}

// ---------- fix round 1: an omitted secret_ref must not clear it ----------
//
// identitySpecFromForm originally read secret_ref through the plain
// optionalString every other create-only field uses, which is nil whether
// the field was absent from the form OR submitted empty. IdentityUpdate now
// resolves it through submittedString instead, exactly as team_id already
// does two lines below it -- secret_ref specifically, because it is in
// domain.RedactedFields: change_log records THAT it changed and never what
// from, so an accidental clear here is the one loss among these six fields
// the audit trail cannot undo afterwards.

// TestAnOmittedSecretRefOnCorrectionDoesNotClearIt.
func TestAnOmittedSecretRefOnCorrectionDoesNotClearIt(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)

	identity, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-secret-preserve",
		Realm: strPtr("vault"), SecretRef: strPtr("kv/prod/secret-preserve/db"),
	})
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, identity); err != nil {
		t.Fatalf("creating identity: %v", err)
	}

	path := "/identities/" + identity.ID
	token := h.csrfToken(path)
	// secret_ref IS DELIBERATELY ABSENT: a correction about the credential's
	// name must not also blank the path it never touched.
	resp := h.post(path, url.Values{
		"csrf_token":  {token},
		"kind":        {domain.IdentityServiceAccount},
		"name":        {"svc-secret-preserve-corrected"},
		"realm":       {"vault"},
		"row_version": {"1"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("the correction returned %d, want 303", resp.StatusCode)
	}
	if n := h.count(`SELECT COUNT(*) FROM identity WHERE id = ? AND secret_ref = ?`,
		identity.ID, "kv/prod/secret-preserve/db"); n != 1 {
		t.Error("secret_ref was cleared by a correction form that never rendered the " +
			"field. An accidental omission destroying the only recorded path to a " +
			"credential's material is not recoverable from change_log, which redacts " +
			"secret_ref by design.")
	}
}

// TestAnExplicitlyEmptiedSecretRefClearsIt: the flip side of the test above
// -- an operator deliberately blanking the field must still work.
func TestAnExplicitlyEmptiedSecretRefClearsIt(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)

	identity, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-secret-clear",
		Realm: strPtr("vault"), SecretRef: strPtr("kv/prod/secret-clear/db"),
	})
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, identity); err != nil {
		t.Fatalf("creating identity: %v", err)
	}

	path := "/identities/" + identity.ID
	token := h.csrfToken(path)
	resp := h.post(path, url.Values{
		"csrf_token":  {token},
		"kind":        {domain.IdentityServiceAccount},
		"name":        {"svc-secret-clear"},
		"realm":       {"vault"},
		"secret_ref":  {""},
		"row_version": {"1"},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("the correction returned %d, want 303", resp.StatusCode)
	}
	if n := h.count(`SELECT COUNT(*) FROM identity WHERE id = ? AND secret_ref IS NULL`,
		identity.ID); n != 1 {
		t.Error("secret_ref was NOT cleared by a correction that explicitly emptied the " +
			"field -- submittedString must still honour a deliberate clearance, the same " +
			"rule its own doc comment states for team_id")
	}
}

// ---------- fix round 1: the no-op rotation flash must tell the truth ----------

// TestANoOpRotationFlashesAccurately: recording a rotation whose date equals
// the one already stored writes nothing at all (RecordIdentityRotation's own
// doc comment: no UPDATE, no change_log row, no row_version bump). The
// confirmation must say so plainly rather than claim an action that did not
// happen.
func TestANoOpRotationFlashesAccurately(t *testing.T) {
	h := newHarness(t)
	h.login("admin", "admin-password")
	ctx := context.Background()
	admin := domain.AdministratorPermit(domain.SystemActor)

	identity, err := domain.NewIdentity(store.NewID(), domain.IdentitySpec{
		Kind: domain.IdentityServiceAccount, Name: "svc-noop-rotation",
		Realm: strPtr("vault"),
	})
	if err != nil {
		t.Fatalf("building identity: %v", err)
	}
	if err := h.store.CreateIdentity(ctx, admin, identity); err != nil {
		t.Fatalf("creating identity: %v", err)
	}
	already := domain.FormatDate(h.store.Now().AddDate(0, 0, -10))
	if err := h.store.RecordIdentityRotation(ctx, admin, identity.ID, already); err != nil {
		t.Fatalf("seeding a rotation: %v", err)
	}

	path := "/identities/" + identity.ID
	token := h.csrfToken(path)
	resp := h.post(path+"/rotation", url.Values{
		"csrf_token":   {token},
		"last_rotated": {already},
	}, false)
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("resubmitting the already-recorded date returned %d, want 303", resp.StatusCode)
	}

	page := body(t, h.get(path, false))
	if !strings.Contains(page, "Already recorded as rotated on "+already) {
		t.Errorf("the confirmation does not say the date was already recorded; a flash " +
			"reading \"Rotation recorded.\" over a write that did not happen claims an " +
			"action RecordIdentityRotation's own no-op rule refused to perform")
	}
	if strings.Contains(page, "flash-success") {
		t.Error("a no-op rotation rendered as a success flash rather than an info one")
	}
}

// TestIdentityPanelNeverRendersIdentitySecretRef is the guard the final
// whole-branch review asked for (blocking #4). identity_panel.html reads the
// GATED .SecretRef -- never .Identity.SecretRef -- in both the "What it is"
// panel and the correction form's secret_ref input; see
// internal/web/handlers/identities.go's header on why the gate lives in the
// handler and not the template.
//
// NO BEHAVIOURAL TEST THROUGH THE REAL HANDLER CAN CATCH A REGRESSION HERE.
// The two values cannot diverge for an Administrator -- renderIdentityWith
// always sets .SecretRef from .Identity.SecretRef when IsAdmin is true -- and
// a non-Administrator never reaches the correction form that would expose the
// difference at all. Driving the real handler therefore proves nothing about
// which binding the template actually uses.
//
// So this drives the template directly, respond_partial_test.go's own
// technique of exercising a template in isolation, against a value the real
// handler can never produce: an Administrator whose .Identity carries a
// secret_ref and whose gated .SecretRef is deliberately empty. The probe type
// is not handlers.identityPage -- that type is unexported outside the
// handlers package -- but html/template resolves fields by name through
// reflection, not by type identity, so a local struct exposing the same
// exported field names drives the exact same template paths.
//
// Mutation: change either `.SecretRef` reference in
// web/templates/partials/identities.html back to `.Identity.SecretRef` and
// this goes red on `strings.Contains(page, "SENTINEL")`; restore to green.
func TestIdentityPanelNeverRendersIdentitySecretRef(t *testing.T) {
	r, err := testRenderer(t)
	if err != nil {
		t.Fatalf("building renderer: %v", err)
	}

	rotDays := 90
	probe := struct {
		handlers.Base
		Errors        map[string]string
		Identity      *store.IdentityRow
		SecretRef     string
		Rotation      string
		DueOn         string
		Usage         *store.IdentityUsageRows
		Timeline      []store.TimelineEntry
		Kinds         []string
		Teams         []store.TeamRow
		RotationInput string
		Today         string
	}{
		Base: handlers.Base{IsAdmin: true, CSRF: "csrf-token"},
		Identity: &store.IdentityRow{
			Identity: domain.Identity{
				ID: "id-1", Kind: domain.IdentityServiceAccount, Name: "svc-probe",
				SecretRef: strPtr("SENTINEL"), RotationDays: &rotDays,
				Lifecycle: domain.LifecycleActive, RowVersion: 1,
			},
		},
		// The divergence a real request cannot produce: an Administrator, a
		// stored secret_ref, and a gated SecretRef that is empty anyway.
		SecretRef:     "",
		Rotation:      string(domain.RotationNeverRecorded),
		Usage:         &store.IdentityUsageRows{},
		Kinds:         domain.IdentityKinds,
		RotationInput: "2026-09-15",
		Today:         "2026-09-15",
	}

	rec := httptest.NewRecorder()
	r.Partial(rec, http.StatusOK, "identity_panel", probe)
	page := rec.Body.String()
	if strings.Contains(page, "SENTINEL") {
		t.Error("identity_panel rendered .Identity.SecretRef rather than the gated " +
			".SecretRef. This is the one rule in the identity surface whose violation " +
			"leaks a credential path to a reader the handler never vetted for it, and " +
			"the only one with no behavioural signal -- see this test's own header.")
	}
}
