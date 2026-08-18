// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package config

import (
	"strings"
	"testing"
)

func TestReaderCredentialsLoadFromTheTwoVariables(t *testing.T) {
	t.Setenv("INV_API_TOKENS", "ansible:"+longToken("a")+",grafana:"+longToken("g"))
	t.Setenv("INV_API_SCOPES", "ansible:prod|staging,grafana:prod")

	creds, err := loadReaderCredentials()
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	if len(creds) != 2 {
		t.Fatalf("got %d credentials, want 2", len(creds))
	}
	// Sorted by id, so a test and a log line agree regardless of how the
	// operator wrote the variable.
	if creds[0].ID != "ansible" || creds[1].ID != "grafana" {
		t.Fatalf("got ids %q/%q", creds[0].ID, creds[1].ID)
	}
	if got := strings.Join(creds[0].Environments, ","); got != "prod,staging" {
		t.Fatalf("got environments %q, want prod,staging", got)
	}
}

func TestAReaderCredentialWithoutAScopeRefusesToStart(t *testing.T) {
	t.Setenv("INV_API_TOKENS", "ansible:"+longToken("a"))
	t.Setenv("INV_API_SCOPES", "")

	_, err := loadReaderCredentials()
	if err == nil {
		t.Fatal("a credential with no scope must refuse to start")
	}
	if !strings.Contains(err.Error(), "ansible") {
		t.Fatalf("the error must name the credential; got %v", err)
	}
}

func TestAScopeForAnUnknownReaderRefusesToStart(t *testing.T) {
	t.Setenv("INV_API_TOKENS", "ansible:"+longToken("a"))
	t.Setenv("INV_API_SCOPES", "ansible:prod,typo:prod")

	_, err := loadReaderCredentials()
	if err == nil {
		t.Fatal("a scope naming a credential that does not exist must refuse to start")
	}
	if !strings.Contains(err.Error(), "typo") {
		t.Fatalf("the error must name the unknown credential; got %v", err)
	}
}

func TestADuplicateReaderIDRefusesToStart(t *testing.T) {
	t.Setenv("INV_API_TOKENS", "ansible:"+longToken("a")+",ansible:"+longToken("b"))
	t.Setenv("INV_API_SCOPES", "ansible:prod")

	if _, err := loadReaderCredentials(); err == nil {
		t.Fatal("a duplicate credential id must refuse to start")
	}
}

func TestNoReaderCredentialsIsNotAnError(t *testing.T) {
	t.Setenv("INV_API_TOKENS", "")
	t.Setenv("INV_API_SCOPES", "")

	creds, err := loadReaderCredentials()
	if err != nil {
		t.Fatalf("an estate with no integrations must start: %v", err)
	}
	if len(creds) != 0 {
		t.Fatalf("got %d credentials, want 0", len(creds))
	}
}

func TestNeitherReaderVariableSetStartsFine(t *testing.T) {
	t.Setenv("INV_API_TOKENS", "")
	t.Setenv("INV_API_SCOPES", "")

	creds, err := loadReaderCredentials()
	if err != nil {
		t.Fatalf("an estate with neither reader variable set must start: %v", err)
	}
	if len(creds) != 0 {
		t.Fatalf("got %d credentials, want 0", len(creds))
	}
}

func TestScopesWithoutTokensRefusesToStart(t *testing.T) {
	t.Setenv("INV_API_TOKENS", "")
	t.Setenv("INV_API_SCOPES", "ansible:prod")

	_, err := loadReaderCredentials()
	if err == nil {
		t.Fatal("INV_API_SCOPES set with INV_API_TOKENS empty must refuse to start")
	}
	if !strings.Contains(err.Error(), "INV_API_SCOPES") || !strings.Contains(err.Error(), "INV_API_TOKENS") {
		t.Fatalf("the error must name both variables; got %v", err)
	}
}

// TestAMalformedReaderTokenEntryDoesNotLeakTheTokenIntoTheError is the
// regression test for the review's Critical finding: a bare INV_API_TOKENS
// entry with no id: prefix is exactly the mistake a redacted message exists
// to protect against, the same as it already does for INV_AGENT_TOKENS.
func TestAMalformedReaderTokenEntryDoesNotLeakTheTokenIntoTheError(t *testing.T) {
	const bareToken = "sup3rsecrettoken-that-is-definitely-long-enough"
	t.Setenv("INV_API_TOKENS", bareToken)
	t.Setenv("INV_API_SCOPES", "")

	_, err := loadReaderCredentials()
	if err == nil {
		t.Fatal("an entry with no colon must refuse to start")
	}
	if strings.Contains(err.Error(), bareToken) {
		t.Fatalf("the error must not contain the raw token; got %v", err)
	}
	if !strings.Contains(err.Error(), "no colon") {
		t.Fatalf("the error must say the entry has no colon; got %v", err)
	}
}

// TestTwoReaderCredentialsSharingATokenRefusesToStart is the regression test
// for the review's first Important finding: an ambiguous token must not
// silently resolve to whichever credential a lookup happens to find.
func TestTwoReaderCredentialsSharingATokenRefusesToStart(t *testing.T) {
	shared := longToken("shared")
	t.Setenv("INV_API_TOKENS", "ansible:"+shared+",grafana:"+shared)
	t.Setenv("INV_API_SCOPES", "ansible:prod,grafana:dev")

	_, err := loadReaderCredentials()
	if err == nil {
		t.Fatal("two reader credentials sharing a token must refuse to start")
	}
	if !strings.Contains(err.Error(), "share a token") {
		t.Fatalf("the error must say the credentials share a token; got %v", err)
	}
	if !strings.Contains(err.Error(), "ansible") || !strings.Contains(err.Error(), "grafana") {
		t.Fatalf("the error must name both credentials; got %v", err)
	}
	if strings.Contains(err.Error(), shared) {
		t.Fatalf("the error must not contain the shared token; got %v", err)
	}
}

// TestAShortReaderTokenRefusesToStart is the regression test for the
// review's second Important finding: a reader token below MinAgentTokenLength
// is the entire authentication for an API exposing topology, hostnames and
// IPs, and must not pass validation.
func TestAShortReaderTokenRefusesToStart(t *testing.T) {
	t.Setenv("INV_API_TOKENS", "ansible:short")
	t.Setenv("INV_API_SCOPES", "ansible:prod")

	_, err := loadReaderCredentials()
	if err == nil {
		t.Fatal("a short reader token must refuse to start")
	}
	if !strings.Contains(err.Error(), "ansible") {
		t.Fatalf("the error must name the credential; got %v", err)
	}
	if !strings.Contains(err.Error(), "characters") {
		t.Fatalf("the error must name the length; got %v", err)
	}
}

// TestAnUnmatchedScopeErrorIsDeterministic pins a small but real operator
// problem: the check iterated a map, so with two unmatched scope ids the
// startup error named a different one on every run. An operator who fixed the
// one it named would be told about the other and could reasonably conclude
// the fix had not taken. Sorted, the same run reports the same id, and the
// lowest one first.
func TestAnUnmatchedScopeErrorIsDeterministic(t *testing.T) {
	t.Setenv("INV_API_TOKENS", "ansible:"+longToken("a"))
	t.Setenv("INV_API_SCOPES", "ansible:prod,zulu:prod,alpha:prod")

	for i := 0; i < 20; i++ {
		_, err := loadReaderCredentials()
		if err == nil {
			t.Fatal("a scope naming a credential that is not in INV_API_TOKENS must refuse to start")
		}
		if !strings.Contains(err.Error(), `"alpha"`) {
			t.Fatalf("run %d named a different unmatched id; the error must be deterministic: %v", i, err)
		}
	}
}
