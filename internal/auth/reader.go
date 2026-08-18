// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// The read-only credential half of the machine surface (WP-A2).
//
// Reader is modelled on Agent, but a Reader never writes: it carries no
// Vocabulary (rule 13 only matters to a writer of observations) and no
// domain.Actor (there is no audit identity to misuse, because there is
// nothing for it to write). Consult docs/AUDIT.md rule 6 for the principal
// separation this mirrors.
//
// Nothing here logs a token, and once a request is authenticated the digest
// comparison has done its job -- a Reader handed to a handler never holds the
// secret that produced it.

package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"sort"

	"github.com/madalinignisca/invctl/internal/config"
	"github.com/madalinignisca/invctl/internal/domain"
)

// Reader is an authenticated read-only API credential.
//
// It carries no token: the registry keeps only a digest, and once a request
// is authenticated the secret has done its job. Handing a handler something
// that still holds the token is how a token ends up in a debug log.
type Reader struct {
	// ID is the credential id.
	ID string
	// Environments is the explicit scope this credential may read.
	Environments domain.EnvironmentScope
}

// String renders a reader for a log line. There is no token to leak, and the
// method exists so that the reflex to print the struct produces something
// deliberate.
func (r *Reader) String() string {
	return fmt.Sprintf("reader %s (environments=%s)", r.ID, r.Environments)
}

// readerEntry pairs a credential with the digest of its token.
type readerEntry struct {
	reader Reader
	digest [sha256.Size]byte
}

// ReaderRegistry holds the configured read-only credentials and answers "is
// this bearer token one of them, and which".
//
// A nil or empty registry authenticates nobody, and the router does not mount
// the read-only API at all in that case: an estate with no readers configured
// should not be carrying the surface.
type ReaderRegistry struct {
	entries []readerEntry
}

// NewReaderRegistry validates the configured credentials and builds the
// lookup.
//
// Every failure here is a startup failure. The alternative -- skipping a
// credential that will not build -- means a client that authenticates fine
// today stops authenticating after a config edit, with nothing in the logs
// naming the edit.
func NewReaderRegistry(creds []config.ReaderCredential) (*ReaderRegistry, error) {
	r := &ReaderRegistry{entries: make([]readerEntry, 0, len(creds))}
	for _, c := range creds {
		scope, err := domain.NewEnvironmentScope(c.Environments)
		if err != nil {
			return nil, fmt.Errorf("scoping read credential %q: %w", c.ID, err)
		}
		r.entries = append(r.entries, readerEntry{
			reader: Reader{ID: c.ID, Environments: scope},
			digest: sha256.Sum256([]byte(c.Token)),
		})
	}
	sort.Slice(r.entries, func(i, j int) bool { return r.entries[i].reader.ID < r.entries[j].reader.ID })
	return r, nil
}

// Enabled reports whether any credential is configured.
func (r *ReaderRegistry) Enabled() bool { return r != nil && len(r.entries) > 0 }

// IDs lists the configured credential ids, sorted.
func (r *ReaderRegistry) IDs() []string {
	if r == nil {
		return nil
	}
	out := make([]string, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e.reader.ID)
	}
	return out
}

// Authenticate resolves a bearer token to the credential that owns it.
//
// The comparison is over SHA-256 digests, using subtle.ConstantTimeCompare,
// exactly as AgentRegistry.Authenticate does. The loop compares against every
// entry and does not break on a match: Task 1 now makes duplicate tokens
// impossible, but the shape stays deliberate so the time taken never reveals
// how many credentials are configured or which one matched.
func (r *ReaderRegistry) Authenticate(token string) (*Reader, bool) {
	if r == nil || token == "" {
		return nil, false
	}
	got := sha256.Sum256([]byte(token))
	var found *Reader
	for i := range r.entries {
		if subtle.ConstantTimeCompare(got[:], r.entries[i].digest[:]) == 1 {
			// A copy: a handler must not be able to widen the scope of a live
			// credential by appending to the slice it was handed.
			reader := r.entries[i].reader
			reader.Environments = domain.EnvironmentScope(reader.Environments.Codes())
			found = &reader
		}
	}
	return found, found != nil
}
