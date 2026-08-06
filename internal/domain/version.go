// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package domain

// VersionField is the name of the optimistic-concurrency token, in the database
// and in the form that carries it back.
//
// Every entity that can be updated carries a RowVersion: an integer the store
// increments on each write, and compares against the value the caller was
// working from. It exists because an update in this codebase is a read on one
// connection followed by a write on another, and nothing in between stops
// somebody else writing the same row. Two people editing one asset both read
// version 4, both write, and the second silently reverts the first -- while
// change_log records the revert as a deliberate act by whoever was slower.
//
// WHY NOT A TIMESTAMP. updated_at is the obvious token and it is not good
// enough: FormatTime truncates to the second, so two writes inside one second
// carry the same value and the guard passes for both. An integer that
// increments always changes and depends on no clock.
//
// WHERE THE VALUE COMES FROM. Not from the row the handler just read -- that
// compares the row against itself and detects nothing. It is rendered into the
// edit form as a hidden field and travels back with the submission, so the
// comparison is against what the operator was actually looking at.
//
// A mismatch is ErrConflict, never a lost write and never a 404: the row is
// there, it simply moved. Handlers show what was typed alongside the news that
// somebody else got there first.
const VersionField = "row_version"
