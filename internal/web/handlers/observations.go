// The observed-state webhook: the one route a machine credential can reach.
//
// This is the most security-sensitive file in the project, because it is where
// a credential that is not a person first touches the database. Everything
// about its shape is docs/AUDIT.md rule 6:
//
//   - Its store field is store.ObservedStore -- two methods -- and never
//     *store.SQLStore. That is not a convention: *SQLStore does not satisfy the
//     interface, so a future edit reaching for RetireAsset here does not
//     compile. Rule 1: "If a boundary can only be described as 'must not call
//     X', it is not implemented."
//   - It is a separate type from App on purpose. App holds the concrete store,
//     the session manager and the authorizer; a method on App would have all
//     three in scope and the compile-error boundary above would be worth
//     nothing.
//   - Attribution comes from the authenticated credential and nowhere else
//     (rule 5). The request body is decoded with DisallowUnknownFields, so a
//     payload carrying `reporter`, `actor`, `source` or `confidence` is a 422
//     naming the field rather than a value quietly ignored -- self-attested
//     provenance is not a control (rule 7), and the loudest way to say so is to
//     refuse the request.
//   - The reporter's vocabulary is mapped here, per credential, before anything
//     reaches the store (rule 13). An unmapped word is 422 with the offending
//     value echoed, never coerced and never stored raw.
//
// It returns JSON. CLAUDE.md's "no JSON API for the UI" is untouched -- this is
// not the UI, and the amended definition of done in docs/AUDIT.md carves out
// exactly this route. Its caller is a monitoring system that cannot render a
// form partial.
//
// Nothing here acts on the estate. A report changes what is displayed, never
// what is running.

package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gabriel/invctl/internal/auth"
	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/store"
	"github.com/gabriel/invctl/internal/web/middleware"
	"github.com/gabriel/invctl/internal/web/render"
)

// Caps on one request (rule 6: "caps body size and batch size").
//
// Both are needed and neither implies the other: 64 KiB of JSON is roughly 400
// observations, so the body cap alone would still let one request open 400
// write transactions, and the batch cap alone would still let a caller stream a
// gigabyte of whitespace at the decoder.
const (
	// MaxObservationBody is the largest request body accepted.
	MaxObservationBody = 64 << 10
	// MaxObservationBatch is the most observations one request may carry.
	MaxObservationBatch = 100
)

// ObservationAPI serves the observed-state webhook.
//
// The store field is unexported and set once by the constructor: a narrow type
// that anything can reassign is a narrow type until the first time somebody
// needs a shortcut.
type ObservationAPI struct {
	store store.ObservedStore
}

// NewObservationAPI builds the webhook over the observed write path.
//
// The parameter type is the boundary. *store.SQLStore deliberately does not
// satisfy store.ObservedStore, so this constructor cannot be handed the whole
// store even by someone trying to.
func NewObservationAPI(observed store.ObservedStore) *ObservationAPI {
	return &ObservationAPI{store: observed}
}

// observationRequest is the wire shape.
//
// A batch rather than a single report because a collector watching an estate
// has one answer per entity at the same instant, and one request per entity
// would be a thousand authentications a minute for the same credential.
type observationRequest struct {
	Observations []observationItem `json:"observations"`
}

// observationItem is one report.
//
// What is absent is as deliberate as what is present. No reporter: attribution
// is derived from the credential (rule 5). No source and no confidence:
// provenance is set by the store from the credential, and a machine may never
// assert `declared` (rule 7). No last_report_at: a caller must never write the
// field that decides how fresh its own data looks (rule 3). Sending any of them
// is a 422 naming the field, because DisallowUnknownFields is set below.
type observationItem struct {
	// ObservationID is the reporter's own idempotency key. Optional.
	ObservationID string `json:"observation_id"`
	EntityType    string `json:"entity_type"`
	EntityID      string `json:"entity_id"`
	// State is in the reporter's own vocabulary and is mapped below.
	State string `json:"state"`
	// ReportedAt is the reporter's clock: display, and ordering.
	ReportedAt string `json:"reported_at"`
	// IntervalSeconds is the declared cadence, and it is mandatory: staleness
	// is computed from it at read time, and a reading with no cadence can never
	// be shown to have gone quiet (rule 8).
	IntervalSeconds int `json:"interval_seconds"`
}

// observationOutcome is what one report did.
//
// It reports Display as well as State because those differ exactly when the
// reading is stale, and a collector that can see its own reports rendering as
// `unknown` can find a clock or cadence problem without anybody opening the UI.
type observationOutcome struct {
	Index       int    `json:"index"`
	EntityType  string `json:"entity_type"`
	EntityID    string `json:"entity_id"`
	Disposition string `json:"disposition"`
	State       string `json:"state"`
	Display     string `json:"display"`
	Stale       bool   `json:"stale"`
	StateSince  string `json:"state_since,omitempty"`
}

type observationResponse struct {
	Applied int                  `json:"applied"`
	Results []observationOutcome `json:"results"`
}

// observationFailure is a rejection. It carries Applied for the same reason the
// success shape does: when entry 7 of a batch is refused, entries 0-6 are
// already durable, and a collector needs to be told that rather than left to
// infer it. Retrying the whole batch is safe -- observations are idempotent.
type observationFailure struct {
	render.APIError
	Applied int `json:"applied"`
}

// Record applies a batch of observations from one monitoring credential.
func (o *ObservationAPI) Record(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	agent := middleware.AgentFrom(ctx)
	if agent == nil {
		// Only reachable if this route were mounted without RequireAgent. That
		// is a wiring bug in routes.go, and it deserves to look like one rather
		// than like a rejected collector.
		slog.Error("observation route reached with no monitoring credential; it is mounted without RequireAgent",
			"path", r.URL.Path)
		render.JSONError(w, http.StatusInternalServerError, "this route is misconfigured")
		return
	}

	if !isJSON(r.Header.Get("Content-Type")) {
		render.JSONError(w, http.StatusUnsupportedMediaType, "send application/json")
		return
	}

	req, problem := decodeObservations(w, r)
	if problem != nil {
		if problem.status == http.StatusRequestEntityTooLarge {
			auth.LogSecurityEvent(ctx, slog.LevelWarn, auth.EventAgentOversized,
				"credential", agent.ID, "reason", problem.body.Error)
		}
		render.JSON(w, problem.status, observationFailure{APIError: problem.body})
		return
	}

	// Map every state before applying any of them. A batch that is half applied
	// and then rejected for a typo in its last entry is far harder to reason
	// about than one that is rejected whole, and observations are idempotent so
	// the retry costs nothing.
	specs := make([]domain.ObservationSpec, len(req.Observations))
	for i, item := range req.Observations {
		state, err := agent.Vocabulary.Map(item.State)
		if err != nil {
			// Rule 13: 422 with the offending value echoed, never coerced.
			render.JSON(w, http.StatusUnprocessableEntity, observationFailure{APIError: render.APIError{
				Error: firstMessage(err, fmt.Sprintf("%q is not a state this reporter may send", domain.Echo(item.State))),
				Field: "state",
				Value: domain.Echo(item.State),
				Index: indexOf(i),
			}})
			return
		}
		specs[i] = domain.ObservationSpec{
			ObservationID:   item.ObservationID,
			EntityType:      item.EntityType,
			EntityID:        item.EntityID,
			State:           string(state),
			ReportedAt:      item.ReportedAt,
			IntervalSeconds: item.IntervalSeconds,
		}
	}

	results := make([]observationOutcome, 0, len(specs))
	for i, spec := range specs {
		// Actor and scope both come from the authenticated credential. Neither
		// is anywhere in the payload, which is why neither can be forged.
		res, err := o.store.RecordObservation(ctx, agent.Actor(), agent.Environments, spec)
		if err != nil {
			o.reportFailure(w, r, agent, i, spec, len(results), err)
			return
		}
		results = append(results, observationOutcome{
			Index:       i,
			EntityType:  spec.EntityType,
			EntityID:    spec.EntityID,
			Disposition: string(res.Disposition),
			State:       string(res.Reading.State),
			Display:     string(res.Reading.Display),
			Stale:       res.Reading.Stale,
			StateSince:  res.Reading.StateSince,
		})
	}

	render.JSON(w, http.StatusOK, observationResponse{Applied: len(results), Results: results})
}

// reportFailure turns a store error into a status, a body and -- where rule 11
// calls for it -- a security event.
//
// Earlier entries in the batch have already been applied. That is stated in the
// response rather than hidden, and it is safe: every observation is idempotent
// on (entity, reporter, observation_id), so the honest instruction to a
// collector is "fix the entry and send the batch again".
func (o *ObservationAPI) reportFailure(w http.ResponseWriter, r *http.Request, agent *auth.Agent,
	index int, spec domain.ObservationSpec, applied int, err error) {
	ctx := r.Context()
	body := observationFailure{APIError: render.APIError{Index: indexOf(index)}, Applied: applied}

	switch {
	case errors.Is(err, domain.ErrForbidden):
		// Rule 6: an out-of-scope report is 403 *and recorded*. A dev
		// collector reaching for a production asset is a finding whether it is
		// a misconfiguration or a stolen token, and a silent drop is how it
		// stays one for months.
		auth.LogSecurityEvent(ctx, slog.LevelWarn, auth.EventAgentScopeDenied,
			"credential", agent.ID, "scope", agent.Environments.String(),
			"entity_type", spec.EntityType, "entity_id", spec.EntityID,
			"remote", r.RemoteAddr)
		body.Error = "this credential is not scoped to that entity's environment"
		render.JSON(w, http.StatusForbidden, body)

	case errors.Is(err, domain.ErrNotFound):
		// Rule 6: 404, never created. The store has already queued it as drift
		// -- an asset the estate has and the inventory does not is a finding,
		// not noise -- but a token walking an id space looks exactly like this,
		// so the attempt is a security event too.
		auth.LogSecurityEvent(ctx, slog.LevelWarn, auth.EventAgentUnknownEntity,
			"credential", agent.ID, "entity_type", spec.EntityType,
			"entity_id", spec.EntityID, "remote", r.RemoteAddr)
		body.Error = "this inventory has no such entity; the report was queued as drift and nothing was created"
		render.JSON(w, http.StatusNotFound, body)

	case errors.Is(err, domain.ErrInvalid):
		body.Error = firstMessage(err, "that observation was not valid")
		body.Field = firstField(err)
		render.JSON(w, http.StatusUnprocessableEntity, body)

	default:
		slog.Error("recording observation failed", "error", err,
			"credential", agent.ID, "entity_type", spec.EntityType, "entity_id", spec.EntityID)
		body.Error = "something went wrong"
		render.JSON(w, http.StatusInternalServerError, body)
	}
}

// decodeFailure is a rejected request: the status and the body to send.
type decodeFailure struct {
	status int
	body   render.APIError
}

// decodeObservations reads and validates the envelope.
//
// DisallowUnknownFields is the load-bearing line. Without it a payload carrying
// "reporter":"someone-else" or "source":"declared" would be silently dropped by
// the decoder and the request would succeed -- which is indistinguishable, from
// the caller's side, from the field having been honoured.
func decodeObservations(w http.ResponseWriter, r *http.Request) (*observationRequest, *decodeFailure) {
	body := http.MaxBytesReader(w, r.Body, MaxObservationBody)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()

	var req observationRequest
	if err := dec.Decode(&req); err != nil {
		return nil, decodeError(err)
	}
	// A second JSON document in the same body is not a payload this endpoint
	// understands, and accepting the first while ignoring the rest is how two
	// parties end up disagreeing about what was sent.
	if dec.More() {
		return nil, &decodeFailure{
			status: http.StatusBadRequest,
			body:   render.APIError{Error: "the body must be a single JSON object"},
		}
	}

	switch {
	case len(req.Observations) == 0:
		return nil, &decodeFailure{
			status: http.StatusUnprocessableEntity,
			body:   render.APIError{Error: "send at least one observation", Field: "observations"},
		}
	case len(req.Observations) > MaxObservationBatch:
		return nil, &decodeFailure{
			status: http.StatusRequestEntityTooLarge,
			body: render.APIError{
				Error: fmt.Sprintf("a batch carries at most %d observations; this one carried %d",
					MaxObservationBatch, len(req.Observations)),
				Field: "observations",
			},
		}
	}
	return &req, nil
}

// decodeError classifies a decoder failure.
func decodeError(err error) *decodeFailure {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return &decodeFailure{
			status: http.StatusRequestEntityTooLarge,
			body: render.APIError{Error: fmt.Sprintf("the request body may be at most %d bytes",
				MaxObservationBody)},
		}
	}

	// encoding/json reports an unknown field as a plain error with no type to
	// match on, so the prefix is the only handle there is. It is worth handling
	// separately: an unknown field is very often somebody trying to set
	// something this endpoint refuses to take from a payload, and telling them
	// which field is what makes that legible rather than mysterious.
	const unknownField = "json: unknown field "
	if msg := err.Error(); strings.HasPrefix(msg, unknownField) {
		field := strings.Trim(strings.TrimPrefix(msg, unknownField), `"`)
		return &decodeFailure{
			status: http.StatusUnprocessableEntity,
			body: render.APIError{
				Error: "that field is not accepted here; the reporter, the source, the confidence and " +
					"the server-side timestamps are all derived from the credential, never from the payload",
				Field: domain.Echo(field),
			},
		}
	}

	return &decodeFailure{
		status: http.StatusBadRequest,
		body:   render.APIError{Error: "the body must be a JSON object"},
	}
}

// isJSON checks the media type without insisting on an exact header.
func isJSON(contentType string) bool {
	media, _, _ := strings.Cut(contentType, ";")
	return strings.EqualFold(strings.TrimSpace(media), "application/json")
}

func indexOf(i int) *int { return &i }

// firstMessage pulls a field message out of a validation error, so the caller
// is told which of its fields is wrong rather than "invalid".
func firstMessage(err error, fallback string) string {
	if ve, ok := domain.AsValidation(err); ok && len(ve.Fields) > 0 {
		return fmt.Sprintf("%s %s", ve.Fields[0].Field, ve.Fields[0].Message)
	}
	return fallback
}

func firstField(err error) string {
	if ve, ok := domain.AsValidation(err); ok && len(ve.Fields) > 0 {
		return ve.Fields[0].Field
	}
	return ""
}
