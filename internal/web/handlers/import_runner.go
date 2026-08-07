// invctl — infrastructure inventory
// Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
//
// Licensed under the GNU Affero General Public License, version 3 only —
// no later version applies. See LICENSE for the full text.
//
// SPDX-License-Identifier: AGPL-3.0-only

package handlers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/madalinignisca/invctl/internal/domain"
	"github.com/madalinignisca/invctl/internal/store"
)

// Running an import after the request that started it.
//
// ONE WORKER, and that is not a throttle -- it is the truth about the storage.
// SQLite takes a single writer, so a second concurrent import would spend its
// life waiting for the first to release the connection while holding a queue
// slot and looking busy. A queue of one says the same thing honestly and makes
// the wait visible on the page rather than invisible in a connection pool.
//
// It also means an import blocks other people's saves for its duration --
// measured at about seven seconds for five thousand rows. Admin-only and rare,
// so it is accepted and written down rather than engineered around; the
// alternative is committing in batches, which would trade the property the
// whole feature rests on (whole file or nothing) for a latency nobody has
// complained about.

// importWork is one queued file, parsed and waiting.
type importWork struct {
	job    store.ImportJob
	assets []store.AssetImportRow
	types  []store.DeviceTypeImportRow
	// actor is captured at SUBMIT time and carried here. It is never re-derived
	// when the work runs and is never the system actor: the audit trail names
	// the person who uploaded the file.
	actor domain.Actor
}

// importRunner owns the queue, the single worker, and the live progress figures.
//
// PROGRESS IS IN MEMORY, NOT IN THE DATABASE, and that is not an optimisation.
// The first version wrote rows_done as it went and deadlocked instantly: the
// SQLite writer pool is one connection, the import's transaction holds it, and
// the progress update queued for a connection that could only be released by
// the thing it was reporting on. It took every other write in the process with
// it until a restart.
//
// So the number a page shows while a job runs is read from here. It is lost if
// the process dies -- which is correct, because a job that was running when the
// process died is marked failed anyway and had written nothing.
type importRunner struct {
	store *store.SQLStore
	queue chan importWork

	mu   sync.Mutex
	done map[string]int
}

// importQueueDepth is how many files may wait. Beyond it, submitting blocks the
// request briefly rather than dropping the upload -- a refusal an operator can
// see beats an acceptance that quietly went nowhere.
const importQueueDepth = 16

func newImportRunner(s *store.SQLStore) *importRunner {
	r := &importRunner{
		store: s,
		queue: make(chan importWork, importQueueDepth),
		done:  map[string]int{},
	}
	go r.work()
	return r
}

// submit queues a parsed file.
func (r *importRunner) submit(w importWork) { r.queue <- w }

// work runs queued imports, one at a time, for the life of the process.
func (r *importRunner) work() {
	for w := range r.queue {
		r.run(w)
	}
}

func (r *importRunner) run(w importWork) {
	// A background context on purpose. The request that submitted this is long
	// gone -- that is the entire point -- so tying the work to its context would
	// cancel every import the moment the operator closed the tab.
	ctx := context.Background()

	if err := r.store.MarkImportRunning(ctx, w.job.ID); err != nil {
		slog.Error("marking import running", "error", err, "job", w.job.ID)
	}
	progress := func(done int) {
		r.mu.Lock()
		r.done[w.job.ID] = done
		r.mu.Unlock()
	}
	defer func() {
		r.mu.Lock()
		delete(r.done, w.job.ID)
		r.mu.Unlock()
	}()

	var report *store.ImportReport
	var err error
	switch w.job.Kind {
	case store.ImportKindAssets:
		// Batched, not one transaction: at measured speed a big file would hold
		// SQLite's single writer for half a minute and stop everybody else
		// saving anything. Both kinds batch -- a catalogue is small, but an
		// import that sometimes holds the database is a rule with an exception
		// in it. See ImportAssetsBatched for what the batching costs.
		report, err = r.store.ImportAssetsBatched(ctx, w.actor, w.assets, progress)
	case store.ImportKindDeviceTypes:
		report, err = r.store.ImportDeviceTypesBatched(ctx, w.actor, w.types, progress)
	default:
		err = errUnknownImportKind
	}

	switch {
	case err != nil:
		// OURS, not theirs. A failure here is the database or this process, and
		// the message says so rather than implying the file was wrong.
		slog.Error("import failed", "error", err, "job", w.job.ID, "kind", w.job.Kind)
		r.finish(ctx, w.job.ID, store.ImportFailed, 0,
			"the import could not be carried out: "+err.Error(), nil)
	case report.PartialRows > 0:
		// THE ONE OUTCOME THAT LEAVES THE ESTATE HALF-CHANGED, and it gets its
		// own message rather than being folded into "refused". It means the file
		// validated and then something moved underneath it -- almost always
		// somebody else taking a name mid-import. Re-running is safe: import
		// creates and never updates, so the rows that landed are skipped.
		r.finish(ctx, w.job.ID, store.ImportPartial, report.PartialRows,
			fmt.Sprintf("%d rows were written before this stopped. Re-run the same file — "+
				"import never updates, so what already landed is skipped.", report.PartialRows),
			report.Problems)
	case len(report.Problems) > 0:
		// THEIRS. The file was read and answered no; nothing was written.
		r.finish(ctx, w.job.ID, store.ImportRefused, 0,
			"nothing was imported — the whole file is applied or none of it is", report.Problems)
	default:
		r.finish(ctx, w.job.ID, store.ImportSucceeded, len(report.Created), "", nil)
	}
}

func (r *importRunner) finish(ctx context.Context, id, status string, created int,
	message string, problems []store.ImportProblem) {

	if err := r.store.FinishImportJob(ctx, id, status, created, message, problems); err != nil {
		slog.Error("recording import outcome", "error", err, "job", id, "status", status)
	}
}

// progressOf is how far through the file a running job has got, and whether it
// is this process running it at all.
func (r *importRunner) progressOf(id string) (int, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.done[id]
	return n, ok
}

// pollEvery is how often a running job's page refreshes itself.
//
// Two seconds: fast enough that a person believes something is happening, slow
// enough that a page left open overnight is not a load generator.
const pollEvery = 2 * time.Second

// errUnknownImportKind cannot happen through the routes -- the kind comes from
// a package constant, never from a request -- and is a real error rather than a
// silent no-op so that it cannot start happening quietly.
var errUnknownImportKind = errors.New("unknown import kind")
