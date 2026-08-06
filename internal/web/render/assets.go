// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"sync"
)

// Asset fingerprinting: /static/app.js?v=<hash of the file>.
//
// Without this, a deploy is invisible to anybody who has already loaded the
// page. The static handler sets `Cache-Control: public, max-age=14400`, and a
// CDN in front of it honours that literally: measured on the public demo,
// Cloudflare served a four-hour-old app.js from its edge (cf-cache-status HIT,
// age 838) while the origin had the fix. The symptom was the worst kind --
// Alpine could not find a component that plainly existed in the source, so a
// perfectly correct fix looked broken, and would have kept looking broken for
// four hours.
//
// The hash is of the file's own bytes, so the URL changes when and only when
// the asset does: a redeploy that does not touch app.js keeps its cache, and
// one that does busts it everywhere at once. Caches key on the full URL
// including the query, so no CDN configuration is involved and nothing has to
// be purged by hand.
type assets struct {
	fsys fs.FS
	// dev skips the cache, so editing app.js during `make dev` shows up on the
	// next reload rather than after a restart.
	dev  bool
	hash map[string]string
	mu   sync.RWMutex
}

func newAssets(fsys fs.FS, dev bool) *assets {
	return &assets{fsys: fsys, dev: dev, hash: map[string]string{}}
}

// version returns a short content hash for one asset, computing it at most
// once per file per process. A miss returns "", which renders the plain URL --
// a missing fingerprint must never be a broken page.
func (a *assets) version(name string) string {
	if !a.dev {
		a.mu.RLock()
		v, ok := a.hash[name]
		a.mu.RUnlock()
		if ok {
			return v
		}
	}

	f, err := a.fsys.Open(name)
	if err != nil {
		return ""
	}
	// Read-only, so a close error tells us nothing actionable.
	defer func() { _ = f.Close() }()

	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return ""
	}
	v := hex.EncodeToString(sum.Sum(nil))[:10]

	a.mu.Lock()
	a.hash[name] = v
	a.mu.Unlock()
	return v
}
