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
	t.Setenv("INV_API_TOKENS", "ansible:tok-a,grafana:tok-g")
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
	t.Setenv("INV_API_TOKENS", "ansible:tok-a")
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
	t.Setenv("INV_API_TOKENS", "ansible:tok-a")
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
	t.Setenv("INV_API_TOKENS", "ansible:tok-a,ansible:tok-b")
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
