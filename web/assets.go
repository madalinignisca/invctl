// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

// Package webassets embeds the templates and static files.
//
// The embed directive has to live in the directory it embeds, so this package
// exists purely to make web/templates and web/static available to the binary.
// Embedding them is what lets the deliverable be a single file with no runtime
// filesystem dependency -- which matters for a tool that is expected to run in
// a segmented environment.
package webassets

import "embed"

//go:embed all:templates all:static
var FS embed.FS
