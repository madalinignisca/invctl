// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package auth

import (
	"context"
	"log/slog"
)

// Security events (docs/AUDIT.md rule 11).
//
// "Authentication success and failure, authorization denial and agent-token
// rejection are security events, never heartbeats, and are never subject to
// transition-only logging."
//
// They are log lines rather than `change_log` rows on purpose. `change_log`
// records changes to declared state, and a refused request changed nothing;
// putting refusals there would mean a brute-force attempt filling an
// append-only table nothing may ever prune. What matters is that they are
// emitted every time -- no deduplication, no "only on transition" -- under one
// stable message so a log pipeline can alert on the class rather than on a
// dozen ad-hoc phrasings.
//
// Nothing passed to these may be a credential. Not a password, not a bearer
// token, not a session id, not the contents of identity.secret_ref. A username
// is acceptable in a log line -- it is what makes a failed sign-in
// investigable -- but it is never what reaches change_log.actor, which holds an
// opaque id.
const (
	// SecurityMessage is the single slog message every security event carries,
	// so one filter finds all of them.
	SecurityMessage = "security event"

	// EventSignInSucceeded: a person authenticated.
	EventSignInSucceeded = "sign_in_succeeded"
	// EventSignInFailed: a person's credentials were refused.
	EventSignInFailed = "sign_in_failed"
	// EventSignInError: authentication could not be completed -- the directory
	// is unreachable, the database is down. Not a credential failure, and
	// distinguishing them matters because only one of the two is an attack.
	EventSignInError = "sign_in_error"
	// EventWriteDenied: an authenticated reader attempted a write.
	EventWriteDenied = "authorization_denied"
	// EventCSRFRejected: an unsafe request arrived without a valid token.
	EventCSRFRejected = "csrf_rejected"

	// EventAgentRejected: a bearer token on the machine-facing route did not
	// match any configured credential, or was absent or malformed.
	EventAgentRejected = "agent_token_rejected"
	// EventAgentSessionConfusion: a request to the machine-facing route carried
	// a browser session. Confusion between principal types is the vulnerability
	// rule 6 exists to prevent, so it is reported as its own event rather than
	// folded into a generic rejection.
	EventAgentSessionConfusion = "agent_session_confusion"
	// EventAgentThrottled: a credential exceeded its rate limit.
	EventAgentThrottled = "agent_rate_limited"
	// EventAgentScopeDenied: a credential reported on an environment outside
	// its explicit scope. Recorded, not silently dropped -- a dev collector
	// reaching for a production asset is the finding, whether it is a
	// misconfiguration or a stolen token.
	EventAgentScopeDenied = "agent_scope_denied"
	// EventAgentUnknownEntity: a credential reported on something this
	// inventory does not have. The report is queued as drift; the attempt is
	// still worth seeing, because a token probing for ids looks exactly like
	// this and nothing else does.
	EventAgentUnknownEntity = "agent_unknown_entity"
	// EventAgentOversized: a request exceeded the body or batch cap.
	EventAgentOversized = "agent_request_too_large"

	// EventReaderRejected is a read credential that did not authenticate.
	EventReaderRejected = "reader_token_rejected"
	// EventReaderSessionConfusion is a browser session arriving on the API.
	EventReaderSessionConfusion = "reader_session_confusion"
	// EventReaderThrottled is a read credential over its rate limit.
	EventReaderThrottled = "reader_rate_limited"
	// EventReaderScopeDenied is a read credential asking for an entity outside
	// its environment scope. The response is a 404 indistinguishable from an
	// absent entity, so this log line is the ONLY place the difference is
	// visible -- an operator debugging "the API returns nothing" finds the
	// answer here and nowhere else.
	EventReaderScopeDenied = "reader_scope_denied"
)

// LogSecurityEvent emits one security event.
//
// The level is the caller's, because these are not all equally alarming: a
// successful sign-in is Info and a rejected token is Warn, and flattening them
// to one level either buries the rejections or pages on every login.
func LogSecurityEvent(ctx context.Context, level slog.Level, event string, attrs ...any) {
	slog.Log(ctx, level, SecurityMessage, append([]any{"event", event}, attrs...)...)
}
