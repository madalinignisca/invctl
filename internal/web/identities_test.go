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
	"strings"
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
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
			"due in", // the derived state
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
