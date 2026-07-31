package handlers

import (
	"net/http"
	"strings"

	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/store"
	"github.com/gabriel/invctl/internal/web/render"
)

// Certificates: what expires, what it covers, and where it is deployed.
//
// The page is built around the expiry, because that is why anybody opens it.
// Everything else — the names, the deployments, the team — exists to turn a date
// into somebody who can act on it.

type certificateListPage struct {
	Base
	Errors       map[string]string
	Certificates []store.CertificateRow
	Teams        []store.TeamRow
	Roles        []store.VocabularyTerm
	Spec         domain.CertificateSpec
	Filter       store.CertificateFilter
}

type certificatePage struct {
	Base
	Errors      map[string]string
	Certificate *store.CertificateRow
	Assets      []store.CertificateDeployment
	Services    []store.CertificateDeployment
	// Pickers for the deployment forms and the edit form.
	AllAssets   []store.AssetRow
	AllServices []store.ServiceRow
	Teams       []store.TeamRow
	Roles       []store.VocabularyTerm
	Lifecycles  []string
}

// CertificateList shows every certificate, soonest expiry first.
func (a *App) CertificateList(w http.ResponseWriter, r *http.Request) {
	a.renderCertificateList(w, r, http.StatusOK, nil, domain.CertificateSpec{})
}

func (a *App) renderCertificateList(w http.ResponseWriter, r *http.Request, status int,
	errs map[string]string, spec domain.CertificateSpec) {

	q := r.URL.Query()
	filter := store.CertificateFilter{
		Query:  q.Get("q"),
		Host:   q.Get("host"),
		TeamID: q.Get("team"),
	}
	certificates, err := a.Store.ListCertificates(r.Context(), filter)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	teams, roles := a.responsibilityOptions(r)

	a.Render.Respond(w, r, status, "certificate_list", "certificate_list_panel", certificateListPage{
		Base:         a.base(r, "Certificates", "certificates"),
		Errors:       orEmpty(errs),
		Certificates: certificates,
		Teams:        teams,
		Roles:        roles,
		Spec:         spec,
		Filter:       filter,
	})
}

// CertificateCreate stores a new certificate.
func (a *App) CertificateCreate(w http.ResponseWriter, r *http.Request) {
	spec := certificateSpecFromForm(r)
	c, err := domain.NewCertificate(store.NewID(), spec, a.Store.Now())
	if err == nil {
		err = a.Store.CreateCertificate(r.Context(), actor(r), c, spec.SANs)
	}
	if err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderCertificateList(w, r, http.StatusUnprocessableEntity, errs, spec)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/certificates/"+c.ID)
}

// CertificateDetail is the page somebody opens from the expiry report.
func (a *App) CertificateDetail(w http.ResponseWriter, r *http.Request) {
	a.renderCertificate(w, r, http.StatusOK, nil)
}

func (a *App) renderCertificate(w http.ResponseWriter, r *http.Request, status int, errs map[string]string) {
	id := r.PathValue("id")
	certificate, err := a.Store.GetCertificate(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	assets, err := a.Store.ListCertificateAssets(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	services, err := a.Store.ListCertificateServices(r.Context(), id)
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	allAssets, err := a.Store.ListAssets(r.Context(), store.AssetFilter{})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	allServices, err := a.Store.ListServices(r.Context(), store.ServiceFilter{})
	if err != nil {
		a.serverError(w, r, err)
		return
	}
	teams, roles := a.responsibilityOptions(r)

	a.Render.Respond(w, r, status, "certificate_detail", "certificate_panel", certificatePage{
		Base:        a.base(r, "Certificate: "+certificate.SubjectCN, "certificates"),
		Errors:      orEmpty(errs),
		Certificate: certificate,
		Assets:      assets,
		Services:    services,
		AllAssets:   allAssets,
		AllServices: allServices,
		Teams:       teams,
		Roles:       roles,
		Lifecycles:  domain.CertificateLifecycles,
	})
}

// CertificateUpdate edits a certificate and the names it covers.
func (a *App) CertificateUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	existing, err := a.Store.GetCertificate(r.Context(), id)
	if err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	spec := certificateSpecFromForm(r)

	updated := existing.Certificate
	updated.SubjectCN = spec.SubjectCN
	updated.Issuer = spec.Issuer
	updated.Fingerprint = spec.Fingerprint
	updated.Serial = spec.Serial
	updated.NotBefore = spec.NotBefore
	updated.NotAfter = spec.NotAfter
	updated.KeyRef = spec.KeyRef
	// submittedString, not the spec: a picker that failed to render must not
	// read as an operator clearing the field. Same rule as assets and services.
	updated.TeamID = submittedString(r, "team_id", existing.TeamID)
	updated.ManagerRole = submittedString(r, "manager_role", existing.ManagerRole)
	updated.Lifecycle = formValue(r, "lifecycle")

	if err := a.Store.UpdateCertificate(r.Context(), actor(r), &updated, spec.SANs); err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderCertificate(w, r, http.StatusUnprocessableEntity, errs)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/certificates/"+id)
}

// CertificateRetire soft-deletes a certificate.
func (a *App) CertificateRetire(w http.ResponseWriter, r *http.Request) {
	if err := a.Store.RetireCertificate(r.Context(), actor(r), r.PathValue("id")); err != nil {
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/certificates")
}

// CertificateDeployAsset records that a certificate is used on an asset.
func (a *App) CertificateDeployAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := a.Store.DeployCertificateToAsset(r.Context(), actor(r), id,
		formValue(r, "asset_id"), optional(formValue(r, "note")))
	a.afterCertificateWrite(w, r, err, id)
}

// CertificateDeployService records that a certificate is used on a service.
func (a *App) CertificateDeployService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := a.Store.DeployCertificateToService(r.Context(), actor(r), id,
		formValue(r, "service_id"), optional(formValue(r, "note")))
	a.afterCertificateWrite(w, r, err, id)
}

// CertificateUndeployAsset retires a deployment.
func (a *App) CertificateUndeployAsset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := a.Store.UndeployCertificateFromAsset(r.Context(), actor(r), id, r.PathValue("assetID"))
	a.afterCertificateWrite(w, r, err, id)
}

// CertificateUndeployService retires a deployment.
func (a *App) CertificateUndeployService(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	err := a.Store.UndeployCertificateFromService(r.Context(), actor(r), id, r.PathValue("serviceID"))
	a.afterCertificateWrite(w, r, err, id)
}

func (a *App) afterCertificateWrite(w http.ResponseWriter, r *http.Request, err error, id string) {
	if err != nil {
		if errs, ok := validationErrors(err); ok {
			a.renderCertificate(w, r, http.StatusUnprocessableEntity, errs)
			return
		}
		a.handleStoreError(w, r, err)
		return
	}
	render.Redirect(w, r, "/certificates/"+id)
}

// certificateSpecFromForm reads a certificate out of a submitted form.
func certificateSpecFromForm(r *http.Request) domain.CertificateSpec {
	return domain.CertificateSpec{
		SubjectCN:   formValue(r, "subject_cn"),
		Issuer:      optionalString(r, "issuer"),
		Fingerprint: optionalString(r, "fingerprint"),
		Serial:      optionalString(r, "serial"),
		NotBefore:   optionalString(r, "not_before"),
		NotAfter:    optionalString(r, "not_after"),
		KeyRef:      optionalString(r, "key_ref"),
		TeamID:      optionalString(r, "team_id"),
		ManagerRole: optionalString(r, "manager_role"),
		Lifecycle:   formValue(r, "lifecycle"),
		SANs:        splitNames(formValue(r, "sans")),
	}
}

// splitNames reads the additional names out of one textarea.
//
// A textarea rather than a repeating field, because pasting the SAN list out of
// `openssl x509 -text` is how anybody actually fills this in, and that arrives
// as newlines or commas depending on how they cleaned it up. Accepting both is
// one line here and saves an operator a fight with a form.
func splitNames(raw string) []string {
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == ' ' || r == '\t' || r == ';'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		// `DNS:orders.example.com` is what openssl prints; taking the name
		// after the type prefix means a paste works unedited.
		if _, name, ok := strings.Cut(f, ":"); ok {
			f = name
		}
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}
