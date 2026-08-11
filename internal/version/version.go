// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Package version reports which build this is.
//
// WHY A PACKAGE RATHER THAN A CONSTANT SOMEBODY EDITS. A version bumped by hand
// is a version that is wrong for the window between the release and the person
// remembering, and the window has no upper bound. These are set by the linker
// from the git tag at build time, so an untagged build says so instead of
// claiming to be the last release.
//
// It is also the first question asked of a deployment that is behaving oddly,
// and "which build is this" must be answerable from the binary alone -- not
// from the directory it was copied out of, and not from a filename somebody
// renamed.
package version

import (
	"fmt"
	"runtime"
	"runtime/debug"
)

// Set by -ldflags at build time; see the Makefile and the release workflow.
//
// The defaults are what a plain `go build ./cmd/invctl` produces, and they are
// deliberately not "0.1.0": a developer build must never be mistakable for a
// release in a bug report.
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// String is the one-line answer.
func String() string {
	return fmt.Sprintf("invctl %s (commit %s, built %s, %s/%s, %s)",
		Version, Commit, Date, runtime.GOOS, runtime.GOARCH, runtime.Version())
}

// Short is the version alone, for a page footer or a log line.
func Short() string { return Version }

// Revision falls back to the VCS stamp the Go toolchain embeds when nothing was
// passed on the command line.
//
// A `go install` from source carries no ldflags, so Commit would read "unknown"
// on a build that Go itself knows the revision of. This recovers it, and marks
// a dirty tree, because a binary built from uncommitted changes is not the
// commit it claims.
func Revision() string {
	if Commit != "unknown" {
		return Commit
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Commit
	}
	rev, dirty := "", false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		}
	}
	if rev == "" {
		return Commit
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	if dirty {
		return rev + "-dirty"
	}
	return rev
}
