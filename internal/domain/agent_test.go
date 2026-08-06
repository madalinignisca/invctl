// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

import (
	"errors"
	"strings"
	"testing"
)

// The credential's two vocabularies: what it may speak about (rule 6) and what
// words it may use (rule 13).

func TestEnvironmentScopeNormalisesAndRefusesEmpty(t *testing.T) {
	cases := []struct {
		name    string
		in      []string
		want    []string
		wantErr bool
	}{
		{name: "sorted and deduplicated", in: []string{"prod", "dev", "prod"}, want: []string{"dev", "prod"}},
		// Environment codes are lower-cased on the way into the database, so a
		// scope written in capitals would otherwise match nothing at all --
		// silently, and only for the credential nobody tested.
		{name: "lower cased", in: []string{"Prod", " TRANSIT "}, want: []string{"prod", "transit"}},
		{name: "blanks dropped", in: []string{"", "  ", "prod"}, want: []string{"prod"}},
		{name: "nothing at all is refused", in: nil, wantErr: true},
		{name: "only blanks is refused", in: []string{"", " "}, wantErr: true},
		// There is no wildcard, and the most likely way somebody would reach
		// for one is a separator that survived the parse.
		{name: "a separator inside a code is refused", in: []string{"prod|dev"}, wantErr: true},
		{name: "a colon inside a code is refused", in: []string{"prod:dev"}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NewEnvironmentScope(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("accepted %v, want a validation error", tc.in)
				}
				if !errors.Is(err, ErrInvalid) {
					t.Errorf("error = %v, want ErrInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewEnvironmentScope(%v): %v", tc.in, err)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Errorf("scope = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestEnvironmentScopeAllowsAny. Any rather than all: the fixture's core
// switches sit in both prod and dev, and requiring the full set would mean no
// credential could report on a boundary device at all.
func TestEnvironmentScopeAllowsAny(t *testing.T) {
	scope, err := NewEnvironmentScope([]string{"prod", "transit"})
	if err != nil {
		t.Fatalf("building scope: %v", err)
	}

	cases := []struct {
		name   string
		entity []string
		want   bool
	}{
		{name: "exactly one environment inside the scope", entity: []string{"prod"}, want: true},
		{name: "one of several inside the scope", entity: []string{"dev", "prod"}, want: true},
		{name: "case does not matter", entity: []string{"PROD"}, want: true},
		{name: "wholly outside the scope", entity: []string{"dev"}},
		// An entity in no environment is covered by nobody. Not by an empty
		// scope, and not by a wide one.
		{name: "no environments at all", entity: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scope.AllowsAny(tc.entity); got != tc.want {
				t.Errorf("AllowsAny(%v) = %v, want %v", tc.entity, got, tc.want)
			}
		})
	}
}

// TestScopeCodesCannotBeWidenedByItsHolder. Codes returns a copy, so a handler
// that is handed a credential's scope cannot append to it and grant itself an
// environment.
func TestScopeCodesCannotBeWidenedByItsHolder(t *testing.T) {
	scope, err := NewEnvironmentScope([]string{"prod"})
	if err != nil {
		t.Fatalf("building scope: %v", err)
	}
	codes := scope.Codes()
	codes[0] = "dev"
	codes = append(codes, "transit")
	_ = codes

	if !scope.Allows("prod") || scope.Allows("dev") || scope.Allows("transit") {
		t.Errorf("scope = %v: mutating the returned slice changed the credential", scope)
	}
}

// TestVocabularyMapsPerReporter. Rule 13: vendor vocabularies are mapped at the
// adapter per reporter, and the three the rule names by example -- firing,
// NotReady, 2 -- are each a different reporter's word for a different state.
func TestVocabularyMapsPerReporter(t *testing.T) {
	cases := []struct {
		vocabulary string
		raw        string
		want       HealthState
		wantErr    bool
	}{
		{vocabulary: "invctl", raw: "up", want: HealthUp},
		{vocabulary: "invctl", raw: "degraded", want: HealthDegraded},
		{vocabulary: "prometheus", raw: "firing", want: HealthDown},
		{vocabulary: "prometheus", raw: "pending", want: HealthDegraded},
		{vocabulary: "prometheus", raw: "resolved", want: HealthUp},
		{vocabulary: "kubernetes", raw: "NotReady", want: HealthDown},
		{vocabulary: "kubernetes", raw: "Ready", want: HealthUp},
		{vocabulary: "nagios", raw: "2", want: HealthDown},
		{vocabulary: "nagios", raw: "0", want: HealthUp},

		// Per reporter means per reporter. A word one vocabulary knows is
		// nonsense in another, and coercing across them is exactly what rule 13
		// forbids: `firing` from a reporter that does not speak Alertmanager is
		// a misconfiguration, not a down.
		{vocabulary: "invctl", raw: "firing", wantErr: true},
		{vocabulary: "prometheus", raw: "up", wantErr: true},
		{vocabulary: "nagios", raw: "down", wantErr: true},

		// No trimming and no case folding: normalising an input is itself a
		// mapping decision, and it belongs in a vocabulary where it is reviewed.
		{vocabulary: "invctl", raw: "UP", wantErr: true},
		{vocabulary: "invctl", raw: " up ", wantErr: true},
		{vocabulary: "kubernetes", raw: "notready", wantErr: true},
		{vocabulary: "invctl", raw: "", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.vocabulary+"/"+tc.raw, func(t *testing.T) {
			v, err := LookupVocabulary(tc.vocabulary)
			if err != nil {
				t.Fatalf("looking up %s: %v", tc.vocabulary, err)
			}
			got, err := v.Map(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("mapped %q to %q, want a rejection", tc.raw, got)
				}
				if !errors.Is(err, ErrInvalid) {
					t.Errorf("error = %v, want ErrInvalid", err)
				}
				// Rule 13: the offending value is echoed, so a reporter's
				// adapter can be fixed from the response alone.
				if tc.raw != "" && !strings.Contains(err.Error(), tc.raw) {
					t.Errorf("error %q does not echo the offending value %q", err, tc.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("Map(%q): %v", tc.raw, err)
			}
			if got != tc.want {
				t.Errorf("Map(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// TestEveryVocabularyMapsOnlyToRealStates. A vocabulary is the one place a
// string becomes a value a CHECK constraint has to accept; a typo in one would
// surface as a constraint failure inside a webhook rather than here.
func TestEveryVocabularyMapsOnlyToRealStates(t *testing.T) {
	for _, name := range VocabularyNames() {
		v, err := LookupVocabulary(name)
		if err != nil {
			t.Fatalf("looking up %s: %v", name, err)
		}
		if len(v.Words()) == 0 {
			t.Errorf("vocabulary %s maps nothing", name)
		}
		for _, word := range v.Words() {
			state, err := v.Map(word)
			if err != nil {
				t.Fatalf("%s: Map(%q): %v", name, word, err)
			}
			if !state.Valid() {
				t.Errorf("%s maps %q to %q, which is not a health state", name, word, state)
			}
		}
	}
}

// TestUnknownVocabularyIsAStartupFailure. Falling back to the identity mapping
// would leave a reporter's `firing` unmapped and every one of its reports a
// 422, which reads as a broken collector rather than as a typo in the
// deployment.
func TestUnknownVocabularyIsAStartupFailure(t *testing.T) {
	// The misspelling is the subject of the test, not a typo in it. golangci-lint
	// --fix silently "corrected" it to "prometheus" once, which inverted the
	// assertion into "a VALID name is rejected" and turned the test red. Left as
	// a marker: an autofix that edits test data can change what a test means.
	if _, err := LookupVocabulary("promethues"); err == nil { //nolint:misspell // deliberate typo under test
		t.Fatal("accepted a misspelt vocabulary name")
	} else if !errors.Is(err, ErrInvalid) {
		t.Errorf("error = %v, want ErrInvalid", err)
	}

	// The empty name is the documented default rather than an error: a
	// credential that does not declare a vocabulary speaks the canonical one.
	v, err := LookupVocabulary("")
	if err != nil {
		t.Fatalf("empty vocabulary name: %v", err)
	}
	if v.Name() != VocabularyInvctl {
		t.Errorf("default vocabulary = %q, want %q", v.Name(), VocabularyInvctl)
	}
}

// TestEchoTruncates. The echoed value is attacker-controlled and lands in a
// response and in the caller's logs; the rejection must not become the
// amplification.
func TestEchoTruncates(t *testing.T) {
	long := strings.Repeat("x", 4096)
	got := Echo(long)
	if len(got) >= len(long) {
		t.Errorf("Echo returned %d bytes for a %d byte input", len(got), len(long))
	}
	if !strings.HasPrefix(got, "xxx") || !strings.HasSuffix(got, "...") {
		t.Errorf("Echo = %q, want a truncated prefix", got)
	}
	if short := Echo("firing"); short != "firing" {
		t.Errorf("Echo(%q) = %q, want it unchanged", "firing", short)
	}
}
