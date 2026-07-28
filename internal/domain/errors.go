// Package domain holds entities and business rules for the inventory.
//
// It has zero external dependencies: no database driver, no HTTP, no ID or
// clock source. Constructors take the id and timestamp as parameters so that
// tests are deterministic and so that the layering stays honest — if this
// package ever imports a driver, something has gone wrong.
package domain

import (
	"errors"
	"fmt"
	"strings"
)

// Sentinel errors. The HTTP layer maps these to status codes; a raw driver
// error must never reach a handler.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	ErrInvalid  = errors.New("invalid")
)

// FieldError is a single field-level validation failure.
type FieldError struct {
	Field   string
	Message string
}

// ValidationError collects field errors so a form can be re-rendered with all
// of them at once rather than one per round trip.
type ValidationError struct {
	Fields []FieldError
}

func (v *ValidationError) Error() string {
	parts := make([]string, 0, len(v.Fields))
	for _, f := range v.Fields {
		parts = append(parts, fmt.Sprintf("%s: %s", f.Field, f.Message))
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// Is makes errors.Is(err, ErrInvalid) true for any validation failure, so
// handlers can branch on the sentinel without type-switching.
func (v *ValidationError) Is(target error) bool { return target == ErrInvalid }

// Add records a field failure.
func (v *ValidationError) Add(field, format string, args ...any) {
	v.Fields = append(v.Fields, FieldError{Field: field, Message: fmt.Sprintf(format, args...)})
}

// OrNil returns nil when nothing failed, so constructors can `return v.OrNil()`.
func (v *ValidationError) OrNil() error {
	if len(v.Fields) == 0 {
		return nil
	}
	return v
}

// Messages returns field -> message, for template rendering.
func (v *ValidationError) Messages() map[string]string {
	m := make(map[string]string, len(v.Fields))
	for _, f := range v.Fields {
		if _, ok := m[f.Field]; !ok {
			m[f.Field] = f.Message
		}
	}
	return m
}

// AsValidation extracts a *ValidationError from an error chain.
func AsValidation(err error) (*ValidationError, bool) {
	var v *ValidationError
	if errors.As(err, &v) {
		return v, true
	}
	return nil, false
}

// oneOf reports whether v is in allowed.
func oneOf(v string, allowed []string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// checkEnum validates a TEXT-with-CHECK column against its Go constant set.
// The DB CHECK constraint is the second line of defence, not the first.
func checkEnum(ve *ValidationError, field, value string, allowed []string) {
	if !oneOf(value, allowed) {
		ve.Add(field, "must be one of %s", strings.Join(allowed, ", "))
	}
}

func checkRequired(ve *ValidationError, field, value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		ve.Add(field, "is required")
	}
	return trimmed
}

// RedactedFields are database column names whose values must never reach the
// audit trail, keyed by the `db` tag.
//
// The change log is read by more people, and kept far longer, than the tables
// it describes: it renders to every authenticated reader including read-only
// ones, and nothing is ever pruned from it. `CreateUser` already redacts a
// password hash by hand for exactly this reason; doing it structurally means
// the next sensitive column is covered by default rather than by whoever
// remembers.
//
// `secret_ref` holds a path rather than a secret, but a complete, permanent,
// widely-readable map of where every credential lives is a reconnaissance gift.
// The audit trail records *that* it changed, never what to.
//
// `dependency.verified_by` is deliberately absent: it holds an opaque
// app_user.id, not a username, so it carries no personal data.
var RedactedFields = map[string]bool{
	"password_hash": true,
	"secret_ref":    true,
}

// RedactedFieldsByEntity covers columns that are sensitive only on certain
// entities, keyed by the Go type name of the audited value.
//
// A column name alone cannot decide this: `display_name` is a person's name on
// app_user and a Windows service's description on rt_windows. Redacting by name
// globally would either leak the first or destroy the second.
//
// The account's own row keeps id, source and is_active, and the change_log
// entity_id is the user id — so the entry still says "this account was created"
// and still resolves to a name while the app_user row exists. Once that row is
// scrubbed it stops resolving, which is exactly what an erasure request should
// achieve without rewriting history.
var RedactedFieldsByEntity = map[string]map[string]bool{
	"AppUser": {
		"username":     true,
		"display_name": true,
		"email":        true,
	},
}

// IsRedacted reports whether a column must be masked in the audit trail.
// entity is the Go type name of the value being audited.
func IsRedacted(entity, column string) bool {
	if RedactedFields[column] {
		return true
	}
	return RedactedFieldsByEntity[entity][column]
}

// Redacted is the placeholder written in place of a sensitive value.
const Redacted = "[redacted]"
