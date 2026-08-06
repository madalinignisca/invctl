// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// What a monitoring credential is allowed to say, and what it is allowed to
// say it about.
//
// docs/AUDIT.md rule 6 -- "Authorization is separate, and separation means a
// different principal type" -- and rule 13 -- "Observed state is never indexed
// and never free text". Both halves live here rather than in the HTTP layer
// because both are rules about the estate, not about a request: a credential
// scoped to `dev` may not write health for a production asset no matter which
// transport carried the report, and `firing` is not a health state no matter
// who said it.
//
// Zero external dependencies, like every other file in this package. Nothing
// here reads a token: a token is a secret and never reaches domain, never
// reaches the store, and never reaches a log line.

package domain

import (
	"fmt"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Environment scope (rule 6)
// ---------------------------------------------------------------------------

// EnvironmentScope is the explicit set of environment codes a credential may
// write observations for.
//
// "Explicit" is the operative word. There is no wildcard and no empty-means-all
// -- an empty scope permits nothing, and a credential with no scope refuses to
// start (see config). The failure this prevents is concrete: a development
// collector, which is the one most likely to be running on a laptop with its
// token in a shell history, must not be able to write health for an `in_scope`
// environment and thereby steer what an operator believes during an audit.
//
// Codes are lower case because domain.NewEnvironment lower-cases every code it
// stores, so a scope that differs only in case would silently match nothing.
type EnvironmentScope []string

// NewEnvironmentScope normalises and validates a credential's environment set.
//
// It rejects an empty set rather than defaulting to "everything": a default
// that widens authorization is how a scope becomes decorative.
func NewEnvironmentScope(codes []string) (EnvironmentScope, error) {
	ve := &ValidationError{}
	seen := make(map[string]bool, len(codes))
	out := make(EnvironmentScope, 0, len(codes))
	for _, raw := range codes {
		code := strings.ToLower(strings.TrimSpace(raw))
		if code == "" {
			continue
		}
		if strings.ContainsAny(code, ",:| \t\n") {
			ve.Add("environments", "%q is not an environment code", code)
			continue
		}
		if seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	if len(out) == 0 {
		ve.Add("environments", "a monitoring credential must be scoped to at least one environment; there is no wildcard")
	}
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	// Sorted so that a log line, an error message and a test all see the same
	// order regardless of how the operator wrote the variable.
	sort.Strings(out)
	return out, nil
}

// IsEmpty reports whether this scope permits nothing.
func (s EnvironmentScope) IsEmpty() bool { return len(s) == 0 }

// Allows reports whether one environment code is inside the scope.
func (s EnvironmentScope) Allows(code string) bool {
	code = strings.ToLower(strings.TrimSpace(code))
	for _, c := range s {
		if c == code {
			return true
		}
	}
	return false
}

// AllowsAny reports whether the scope covers at least one of an entity's
// environments.
//
// Any rather than all, deliberately: an asset that sits in both `prod` and
// `dev` -- the seeded core switches do -- is legitimately watched by either
// side's collector, and requiring the full set would mean no credential could
// report on a boundary device at all. An entity in *no* environment is covered
// by nobody, which is a data gap surfaced as a denial rather than an implicit
// allow.
func (s EnvironmentScope) AllowsAny(codes []string) bool {
	for _, code := range codes {
		if s.Allows(code) {
			return true
		}
	}
	return false
}

// AllowsAll reports whether the scope covers EVERY environment given.
//
// This, not AllowsAny, is what rule 6 requires of an observation: "a dev
// collector's token must not be able to write health for an in_scope
// environment". An entity sits in a set of environments, and a reading written
// about it is visible in all of them -- so the most permissive environment must
// not decide. The seeded estate has exactly the shape that makes the difference
// bite: sw-core-1 and sw-core-2 are in {prod, dev}, prod is in_scope, and under
// AllowsAny a laptop-resident dev token could assert that a production core
// switch was up or down and steer what an operator saw during an audit.
//
// An empty set is not vacuously allowed -- the caller refuses an entity with no
// environments before reaching here, because that is a data gap worth surfacing
// rather than an implicit allow.
func (s EnvironmentScope) AllowsAll(codes []string) bool {
	if len(codes) == 0 {
		return false
	}
	for _, code := range codes {
		if !s.Allows(code) {
			return false
		}
	}
	return true
}

// Codes returns a copy, so a caller cannot widen a credential's scope by
// appending to the slice it was handed.
func (s EnvironmentScope) Codes() []string {
	return append([]string(nil), s...)
}

// String renders the scope for a log line or an error message. Environment
// codes are not personal data and naming them is what makes a 403 diagnosable.
func (s EnvironmentScope) String() string { return strings.Join(s, ",") }

// ---------------------------------------------------------------------------
// Vendor vocabularies (rule 13)
// ---------------------------------------------------------------------------

// VocabularyInvctl is the identity mapping: a reporter that already speaks the
// four canonical states. It is the default for a credential that does not
// declare one.
const VocabularyInvctl = "invctl"

// HealthVocabulary maps one reporter's words onto HealthState.
//
// Rule 13: "Vendor vocabularies (`firing`, `NotReady`, `2`) are mapped at the
// adapter per reporter; an unmapped value is 422 with the offending value
// echoed, never coerced, never stored raw. A column an external credential
// authors and an operator reads during an incident is the last place to accept
// arbitrary strings."
//
// The mappings are Go constants rather than configuration on purpose. A
// mapping decides what an operator sees at 03:00 -- "firing means down" is a
// judgement about someone else's alerting semantics -- and a judgement of that
// kind belongs somewhere it is reviewed and tested, not in an environment
// variable that can be edited during an incident by the person the reading is
// about.
//
// Matching is exact: no trimming, no case folding. Normalising an input is
// itself a mapping decision, so `NotReady` and `notready` are different words
// until a vocabulary says otherwise, and a reporter that starts emitting a new
// spelling gets a 422 naming it rather than a silent reinterpretation.
type HealthVocabulary struct {
	name  string
	words map[string]HealthState
}

// Name identifies the vocabulary in configuration and in an error message.
func (v HealthVocabulary) Name() string { return v.name }

// IsZero reports whether this is the unset vocabulary, which maps nothing.
func (v HealthVocabulary) IsZero() bool { return v.name == "" }

// EchoLimit caps how much of an unmapped value is quoted back.
//
// The value is attacker-controlled and lands in an error response and in the
// caller's own logs; a reporter that sends a megabyte of prose should learn
// that it was rejected without the rejection itself becoming the amplification.
const EchoLimit = 64

// Map converts one reporter's word into a health state.
//
// The offending value is echoed on failure -- truncated -- so a reporter's
// adapter can be fixed from the response alone, which is the whole reason the
// rule asks for it rather than a bare "invalid state".
func (v HealthVocabulary) Map(raw string) (HealthState, error) {
	if state, ok := v.words[raw]; ok {
		return state, nil
	}
	ve := &ValidationError{}
	ve.Add("state", "%q is not a state in the %s vocabulary; map it at the adapter to one of %s",
		Echo(raw), v.vocabularyName(), strings.Join(HealthStateNames(), ", "))
	return "", ve
}

func (v HealthVocabulary) vocabularyName() string {
	if v.name == "" {
		return "(unset)"
	}
	return v.name
}

// Words returns the vocabulary's accepted inputs, sorted. For an error message
// and for the test that asserts every vocabulary maps only to real states.
func (v HealthVocabulary) Words() []string {
	out := make([]string, 0, len(v.words))
	for w := range v.words {
		out = append(out, w)
	}
	sort.Strings(out)
	return out
}

// Echo truncates an untrusted value for quoting back to its sender.
func Echo(v string) string {
	if len(v) <= EchoLimit {
		return v
	}
	return v[:EchoLimit] + "..."
}

// healthVocabularies is the closed set. Adding one is a code review, which is
// the point.
var healthVocabularies = map[string]HealthVocabulary{
	// The canonical four. A reporter written for this system speaks these.
	VocabularyInvctl: {
		name: VocabularyInvctl,
		words: map[string]HealthState{
			string(HealthUp):       HealthUp,
			string(HealthDegraded): HealthDegraded,
			string(HealthDown):     HealthDown,
			string(HealthUnknown):  HealthUnknown,
		},
	},
	// Alertmanager alert states. `pending` is degraded rather than down: the
	// rule has fired but not held for its `for` duration, so asserting `down`
	// would make every transient blip an outage in the ledger.
	"prometheus": {
		name: "prometheus",
		words: map[string]HealthState{
			"firing":   HealthDown,
			"pending":  HealthDegraded,
			"inactive": HealthUp,
			"resolved": HealthUp,
		},
	},
	// Kubernetes node/pod readiness. `Unknown` is Kubernetes' own positive
	// assertion of ignorance -- the kubelet stopped reporting -- and maps to
	// ours rather than to `down`, because "we cannot see it" and "it is not
	// serving" are different findings during an incident.
	"kubernetes": {
		name: "kubernetes",
		words: map[string]HealthState{
			"Ready":              HealthUp,
			"NotReady":           HealthDown,
			"SchedulingDisabled": HealthDegraded,
			"Unknown":            HealthUnknown,
		},
	},
	// Nagios/Icinga plugin exit codes, which arrive as text over a webhook.
	"nagios": {
		name: "nagios",
		words: map[string]HealthState{
			"0": HealthUp,
			"1": HealthDegraded,
			"2": HealthDown,
			"3": HealthUnknown,
		},
	},
}

// LookupVocabulary resolves a configured vocabulary name.
//
// An unknown name is a startup failure rather than a fallback to the identity
// mapping: falling back would leave a reporter's `firing` unmapped and every
// report 422, which reads as a broken collector rather than as a typo in the
// deployment.
func LookupVocabulary(name string) (HealthVocabulary, error) {
	if strings.TrimSpace(name) == "" {
		return healthVocabularies[VocabularyInvctl], nil
	}
	v, ok := healthVocabularies[name]
	if !ok {
		return HealthVocabulary{}, fmt.Errorf("looking up state vocabulary: %q is not one of %s: %w",
			Echo(name), strings.Join(VocabularyNames(), ", "), ErrInvalid)
	}
	return v, nil
}

// VocabularyNames lists the closed set, sorted, for an error message.
func VocabularyNames() []string {
	out := make([]string, 0, len(healthVocabularies))
	for name := range healthVocabularies {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
