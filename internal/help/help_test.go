// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package help

import (
	"testing"

	"github.com/madalinignisca/invctl/internal/domain"
)

// TestTopicsCoverTheirEnums is the guard that makes this package worth having
// rather than a second place for the truth to rot.
//
// Every value the domain accepts must be explained, and every value explained
// must be one the domain accepts. Without both directions, help text drifts
// from the code it describes -- and help that confidently describes a value
// the engine no longer has is worse than no help at all.
func TestTopicsCoverTheirEnums(t *testing.T) {
	for _, tc := range []struct {
		key   string
		codes []string
	}{
		{"availability", domain.Availabilities},
		{"failover_mode", domain.FailoverModes},
		{"nature", domain.Natures},
		{"bind_scope", domain.BindScopes},
		{"lifecycle", domain.AssetLifecycles},
		{"runtime_type", domain.RuntimeTypes},
		{"plane", domain.Planes},
	} {
		topic, ok := Lookup(tc.key)
		if !ok {
			t.Errorf("no help topic %q", tc.key)
			continue
		}
		described := map[string]bool{}
		for _, term := range topic.Terms {
			if term.Description == "" || term.Label == "" {
				t.Errorf("%s/%s has no %s", tc.key, term.Code,
					map[bool]string{true: "description", false: "label"}[term.Description == ""])
			}
			described[term.Code] = true
		}
		for _, code := range tc.codes {
			if !described[code] {
				t.Errorf("the domain accepts %s=%q and nothing explains it", tc.key, code)
			}
		}
		accepted := map[string]bool{}
		for _, code := range tc.codes {
			accepted[code] = true
		}
		for _, term := range topic.Terms {
			if !accepted[term.Code] {
				t.Errorf("%s explains %q, which the domain does not accept", tc.key, term.Code)
			}
		}
	}
}

func TestEveryTopicIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, topic := range Topics {
		if topic.Key == "" || topic.Title == "" || topic.Intro == "" {
			t.Errorf("topic %q is missing a key, title or intro", topic.Key)
		}
		if seen[topic.Key] {
			t.Errorf("duplicate topic key %q", topic.Key)
		}
		seen[topic.Key] = true
		if len(topic.Terms) == 0 {
			t.Errorf("topic %q explains nothing", topic.Key)
		}
	}
}
