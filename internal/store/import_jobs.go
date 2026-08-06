// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/madalinignisca/invctl/internal/domain"
)

// Import jobs. See migration 00025 for why they exist and what the numbers on
// them do and do not mean.

// Job kinds and statuses. Go constant sets with matching CHECKs.
const (
	ImportKindAssets      = "assets"
	ImportKindDeviceTypes = "device_types"

	ImportQueued    = "queued"
	ImportRunning   = "running"
	ImportSucceeded = "succeeded"
	// Refused means the file was read and answered no: every problem is the
	// operator's to fix. Distinct from failed, which is ours.
	ImportRefused = "refused"
	ImportFailed  = "failed"
	// Partial is the only status that means the estate is half-changed. It is
	// separate from both of the above because the action it calls for is
	// different: not "fix your file" and not "tell us", but "run it again".
	ImportPartial = "partial"
)

// ImportJob is one run.
type ImportJob struct {
	ID        string `db:"id"`
	Kind      string `db:"kind"`
	Filename  string `db:"filename"`
	Actor     string `db:"actor"`
	ActorKind string `db:"actor_kind"`
	Status    string `db:"status"`
	RowsTotal int    `db:"rows_total"`
	// RowsDone counts rows EXAMINED, not rows you have. The import is one
	// transaction, so a job halfway through has written nothing anybody can see.
	RowsDone int     `db:"rows_done"`
	Created  int     `db:"created"`
	Message  *string `db:"message"`
	// Problems is the report's problem list as JSON. Rendered, never queried.
	Problems   *string `db:"problems"`
	CreatedAt  string  `db:"created_at"`
	StartedAt  *string `db:"started_at"`
	FinishedAt *string `db:"finished_at"`
}

// Done reports whether the job has stopped, one way or another.
func (j ImportJob) Done() bool {
	return j.Status == ImportSucceeded || j.Status == ImportRefused || j.Status == ImportFailed
}

// Percent is progress through the FILE, for a progress bar. It is not a
// percentage of your estate: see RowsDone.
func (j ImportJob) Percent() int {
	if j.RowsTotal <= 0 {
		return 0
	}
	p := j.RowsDone * 100 / j.RowsTotal
	if p > 100 {
		p = 100
	}
	return p
}

// DecodedProblems renders the stored problem list for a template.
func (j ImportJob) DecodedProblems() []ImportProblem {
	if j.Problems == nil || *j.Problems == "" {
		return nil
	}
	var out []ImportProblem
	if err := json.Unmarshal([]byte(*j.Problems), &out); err != nil {
		// A problem list that will not parse is still worth saying something
		// about, and silence here would be the shape this codebase keeps
		// finding: an error discarded and replaced with an empty answer.
		return []ImportProblem{{Message: "the recorded problem list could not be read: " + err.Error()}}
	}
	return out
}

// CreateImportJob records a queued run.
//
// No change_log row. The assets it creates each write their own, naming the
// same actor -- "who put this box in the inventory" stays answerable per box,
// which is the obligation. A second entry for the batch would say nothing the
// per-asset rows do not.
func (s *SQLStore) CreateImportJob(ctx context.Context, j *ImportJob) error {
	_, err := s.db.Writer.ExecContext(ctx, s.db.Rebind(`
		INSERT INTO import_job (id, kind, filename, actor, actor_kind, status,
		                        rows_total, rows_done, created, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, 0, ?)`),
		j.ID, j.Kind, j.Filename, j.Actor, j.ActorKind, j.Status, j.RowsTotal, j.CreatedAt)
	if err != nil {
		return translateWriteErr(err, "recording the import job")
	}
	return nil
}

// GetImportJob loads one.
func (s *SQLStore) GetImportJob(ctx context.Context, id string) (*ImportJob, error) {
	var j ImportJob
	if err := s.readOne(ctx, &j, `SELECT * FROM import_job WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting import job %s: %w", id, err)
	}
	return &j, nil
}

// ListImportJobs returns recent runs, newest first.
func (s *SQLStore) ListImportJobs(ctx context.Context, limit int) ([]ImportJob, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var jobs []ImportJob
	err := s.read(ctx, &jobs,
		`SELECT * FROM import_job ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("listing import jobs: %w", err)
	}
	return jobs, nil
}

// MarkImportRunning stamps the start.
func (s *SQLStore) MarkImportRunning(ctx context.Context, id string) error {
	_, err := s.db.Writer.ExecContext(ctx, s.db.Rebind(
		`UPDATE import_job SET status = ?, started_at = ? WHERE id = ?`),
		ImportRunning, domain.FormatTime(s.now()), id)
	return err
}

// Progress is DELIBERATELY NOT WRITTEN HERE, and this comment is the headstone
// of a bug worth remembering.
//
// There was an ImportProgress method that updated rows_done on db.Writer while
// the import ran, with a comment explaining that it wrote "on its own
// connection outside the import's transaction". That was false: the SQLite
// writer pool is ONE connection, held by the import for its whole duration, so
// the progress update queued for a connection that could not be released until
// the thing it was reporting on had finished. The first real import deadlocked
// and took every other write in the process with it.
//
// A running job's progress now lives in memory in the runner and is read from
// there. This table records only what survives a restart -- queued, running,
// and the outcome -- and rows_done is set once, at the end.

// FinishImportJob records the outcome.
func (s *SQLStore) FinishImportJob(ctx context.Context, id, status string, created int,
	message string, problems []ImportProblem) error {

	var encoded *string
	if len(problems) > 0 {
		if b, err := json.Marshal(problems); err == nil {
			text := string(b)
			encoded = &text
		}
	}
	var msg *string
	if message != "" {
		msg = &message
	}
	_, err := s.db.Writer.ExecContext(ctx, s.db.Rebind(`
		UPDATE import_job
		SET status = ?, created = ?, message = ?, problems = ?, finished_at = ?, rows_done = rows_total
		WHERE id = ?`),
		status, created, msg, encoded, domain.FormatTime(s.now()), id)
	return err
}

// FailStaleImportJobs marks anything left running by a previous process.
//
// Called at startup. A job that was running when this process died did NOT
// commit -- the transaction went with it -- so leaving the row saying "running"
// would have the page poll forever for work nobody is doing. Saying so plainly
// is the honest answer, and it is also the only one: there is nothing to resume,
// because there is nothing half-written to resume from.
func (s *SQLStore) FailStaleImportJobs(ctx context.Context) (int, error) {
	res, err := s.db.Writer.ExecContext(ctx, s.db.Rebind(`
		UPDATE import_job
		SET status = ?, message = ?, finished_at = ?
		WHERE status IN (?, ?)`),
		ImportFailed,
		"invctl restarted while this import was running. It was one transaction, "+
			"so nothing was written — upload the file again.",
		domain.FormatTime(s.now()), ImportQueued, ImportRunning)
	if err != nil {
		return 0, fmt.Errorf("clearing stale import jobs: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
