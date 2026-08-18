// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import "testing"

// TestAPIMaxLimitFitsInsideOneIDChunk pins the assumption decorateAPIAssets,
// decorateAPIServices and decorateAPIAddresses all lean on: a page never has
// more rows than idChunkSize, so chunkIDs never produces more than one chunk
// for a single page's worth of ids and the batched decoration queries stay
// single-chunk-safe. Raising apiMaxLimit without raising idChunkSize would
// silently open a multi-chunk path nothing here exercises.
func TestAPIMaxLimitFitsInsideOneIDChunk(t *testing.T) {
	if apiMaxLimit > idChunkSize {
		t.Fatalf("apiMaxLimit (%d) exceeds idChunkSize (%d): a full page's ids "+
			"would span more than one IN (...) chunk, a path this package has "+
			"never tested", apiMaxLimit, idChunkSize)
	}
}
