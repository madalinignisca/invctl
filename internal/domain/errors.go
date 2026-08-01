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
	"unicode"
)

// Sentinel errors. The HTTP layer maps these to status codes; a raw driver
// error must never reach a handler.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	// ErrStale is a conflict with a DIFFERENT cause and a different answer: the
	// row moved between the form being rendered and the save arriving. Wrapped
	// so errors.Is(err, ErrConflict) still holds -- a caller that only cares
	// "this did not go in" keeps working -- while a caller that can say
	// something more useful than "that already exists" is able to tell the
	// difference. Reporting a stale write as a duplicate sends an operator
	// looking for a name clash that does not exist.
	ErrStale   = fmt.Errorf("changed by somebody else: %w", ErrConflict)
	ErrInvalid = errors.New("invalid")
	// ErrForbidden is authenticated-but-not-permitted, and it is deliberately
	// distinct from ErrInvalid: a monitoring credential reporting on an
	// environment outside its scope has sent a well-formed request that it is
	// not allowed to make (docs/AUDIT.md rule 6). 422 would tell its operator
	// to fix the payload; 403 tells them to fix the credential.
	ErrForbidden = errors.New("forbidden")
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

// NewValidation builds a one-field validation failure, for a check a handler
// makes before the domain gets a chance to -- a form value that is not the
// right SHAPE, rather than not the right value.
func NewValidation(field, message string) *ValidationError {
	ve := &ValidationError{}
	ve.Add(field, "%s", message)
	return ve
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
//
// This is for BEHAVIOURAL enums only -- the ones whose value selects a code
// path. `nature` decides how failure propagates, `availability` decides how the
// impact engine counts capacity, `plane` decides which anchor an attachment
// folds into. A value no switch handles must never reach the database, because
// the switch would fall through to its default silently and produce a wrong
// answer during an incident with nothing in the logs. For those, a new value
// SHOULD require a release: the release is where the code that understands it
// gets written. Domain vocabularies use checkVocabulary instead.
func checkEnum(ve *ValidationError, field, value string, allowed []string) {
	if !oneOf(value, allowed) {
		ve.Add(field, "must be one of %s", strings.Join(allowed, ", "))
	}
}

// MaxVocabularyCodeLen bounds a lookup-table code. Nothing in the estate needs
// more; a longer value is a pasted sentence in the wrong field.
const MaxVocabularyCodeLen = 64

// checkVocabulary validates a column whose permitted values live in a lookup
// TABLE (migration 00004) rather than in a CHECK constraint.
//
// It deliberately does NOT check membership. The lookup tables exist so that a
// new form factor, asset kind or container engine arrives as an INSERT instead
// of a migration and a deploy; a constructor holding a fixed Go slice would
// refuse the new value the instant it was added and defeat the entire change.
// Existence is the FOREIGN KEY's job, and that is a strengthening rather than a
// weakening: a FK cannot drift from the list the way a hand-maintained slice
// can, because it *is* the list. The store additionally reads the table before
// a write so the operator gets a field-level 422 rather than a driver error --
// see (*tx).requireVocabulary -- but that read is of the same table the FK
// points at, not a second copy of it.
//
// What is left here is shape. A vocabulary code is a stable machine identifier:
// it is a primary key, it travels in query strings (`/assets?kind=vm`), and both
// engines compare it byte for byte. So: present, bounded, and free of the
// whitespace and control characters that make a code silently unequal to the
// one that looks identical next to it. Whether `bridge` is a real asset kind is
// a question only the table can answer.
func checkVocabulary(ve *ValidationError, field, value string) string {
	trimmed := strings.TrimSpace(value)
	switch {
	case trimmed == "":
		ve.Add(field, "is required")
	case len(trimmed) > MaxVocabularyCodeLen:
		ve.Add(field, "must be %d characters or fewer", MaxVocabularyCodeLen)
	case !isVocabularyCode(trimmed):
		ve.Add(field, "must not contain spaces or control characters")
	}
	return trimmed
}

func isVocabularyCode(value string) bool {
	for _, r := range value {
		if unicode.IsSpace(r) || !unicode.IsPrint(r) {
			return false
		}
	}
	return true
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
	// A team's contact. Global rather than per-entity because the name is
	// unambiguous wherever it appears -- unlike `display_name`, which is a
	// person on app_user and a Windows service description on rt_windows.
	//
	// The rule says a contact must be a group address, a queue or a channel and
	// never an individual, and the application cannot check that. The form hint
	// is adequate for the `team` row and for search_index, because both hold
	// only the CURRENT value and an erasure request is answered by editing the
	// team. It is NOT adequate for change_log, which is append-only: a
	// predictable operator mistake would be permanent, and -- found by a
	// security review -- CORRECTING it wrote the personal value a second time,
	// as the `old` side of the update diff.
	//
	// So the audit trail records THAT the contact changed and never what to,
	// which is the treatment secret_ref already gets for the same reason.
	"contact_ref": true,
	// A certificate's private-key reference. Same rule as secret_ref, and the
	// same reason: it is a path to something secret, and a path is a lead. A
	// certificate is also the entity where somebody is most likely to paste
	// something worse than a path into a field, so the audit trail must not be
	// the place that keeps it forever.
	"key_ref": true,
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
