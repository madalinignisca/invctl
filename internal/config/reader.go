// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// The read-credential half of the machine surface (WP-A2).
//
// Two variables rather than the three the monitoring credentials use: a reader
// has no vocabulary, because it never writes an observation and therefore never
// maps a reporter's words onto a HealthState.
//
// Nothing here logs a token, and ReaderCredential deliberately cannot be
// printed with its secret -- see String and LogValue below.

package config

import (
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
)

// ReaderCredential is one read-only API credential as configured.
type ReaderCredential struct {
	ID           string
	Token        string
	Environments []string
}

// String renders a credential without its token.
func (c ReaderCredential) String() string {
	return fmt.Sprintf("reader %s (environments=%s)", c.ID, strings.Join(c.Environments, "|"))
}

// LogValue keeps the token out of a structured log line even when somebody logs
// the whole struct.
func (c ReaderCredential) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("id", c.ID),
		slog.String("environments", strings.Join(c.Environments, "|")),
	)
}

// loadReaderCredentials assembles the credentials from the two variables that
// describe them.
//
// Every failure is a startup failure with the credential id in the message. The
// alternative -- skipping a credential that will not build -- means an
// integration that authenticates today stops authenticating after a config
// edit, with nothing in the logs naming the edit.
func loadReaderCredentials() ([]ReaderCredential, error) {
	tokens, err := splitPairs("INV_API_TOKENS", os.Getenv("INV_API_TOKENS"))
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		// A non-empty INV_API_SCOPES with no INV_API_TOKENS is not "no readers
		// configured" -- it is very likely a typo'd or removed tokens variable
		// with the scopes line left behind, and the honest response is to
		// refuse to start rather than silently mount no read surface at all.
		scopes, err := splitPairs("INV_API_SCOPES", os.Getenv("INV_API_SCOPES"))
		if err != nil {
			return nil, err
		}
		if len(scopes) > 0 {
			return nil, fmt.Errorf(
				"validating config: INV_API_SCOPES is set but INV_API_TOKENS is empty; " +
					"every scope must belong to a credential, so either set INV_API_TOKENS or clear INV_API_SCOPES")
		}
		return nil, nil
	}
	scopes, err := splitPairs("INV_API_SCOPES", os.Getenv("INV_API_SCOPES"))
	if err != nil {
		return nil, err
	}

	byID := make(map[string]string, len(scopes))
	for _, p := range scopes {
		byID[p.key] = p.value
	}

	seen := make(map[string]bool, len(tokens))
	creds := make([]ReaderCredential, 0, len(tokens))
	for _, p := range tokens {
		if seen[p.key] {
			return nil, fmt.Errorf("validating config: INV_API_TOKENS names credential %q twice", p.key)
		}
		seen[p.key] = true

		scope, ok := byID[p.key]
		if !ok {
			return nil, fmt.Errorf(
				"validating config: read credential %q has no entry in INV_API_SCOPES; "+
					"there is no wildcard, so every credential must name the environments it may read",
				p.key)
		}
		envs := make([]string, 0, 2)
		for _, code := range strings.Split(scope, scopeSeparator) {
			if code = strings.TrimSpace(code); code != "" {
				envs = append(envs, code)
			}
		}
		if len(envs) == 0 {
			return nil, fmt.Errorf(
				"validating config: read credential %q has an empty environment scope in INV_API_SCOPES", p.key)
		}
		creds = append(creds, ReaderCredential{ID: p.key, Token: p.value, Environments: envs})
	}

	for id := range byID {
		if !seen[id] {
			return nil, fmt.Errorf(
				"validating config: INV_API_SCOPES names credential %q, which is not in INV_API_TOKENS", id)
		}
	}

	sort.Slice(creds, func(i, j int) bool { return creds[i].ID < creds[j].ID })
	return creds, nil
}
