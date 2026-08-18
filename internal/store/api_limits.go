// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

// APIDefaultLimit and APIMaxLimit expose api.go's page-size bounds so that
// internal/api can assert its own api.DefaultLimit and api.MaxLimit agree with
// them. store cannot import internal/api without a cycle, so the assertion has
// to live on the api side; these two functions are the minimal surface that
// makes it possible without duplicating the numbers a second time or making
// apiDefaultLimit/apiMaxLimit themselves part of the package's exported API.
func APIDefaultLimit() int { return apiDefaultLimit }

// APIMaxLimit is api.go's clamp ceiling for a page. See APIDefaultLimit.
func APIMaxLimit() int { return apiMaxLimit }
