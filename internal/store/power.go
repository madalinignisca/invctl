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
	"fmt"
	"sort"

	"github.com/madalinignisca/invctl/internal/domain"
)

// The power chain, and the findings it exists to produce. See migration 00023.

// ---------- panels ----------

// PowerPanelRow is a panel with its location and how much hangs off it.
type PowerPanelRow struct {
	domain.PowerPanel
	SiteName string `db:"site_name"`
	Feeds    int    `db:"feeds"`
}

const powerPanelSelect = `
	SELECT p.*, a.name AS site_name,
	       (SELECT COUNT(*) FROM power_feed f
	        WHERE f.panel_id = p.id AND f.lifecycle <> '` + domain.LifecycleRetired + `') AS feeds
	FROM power_panel p
	JOIN asset a ON a.id = p.site_id`

// CreatePowerPanel inserts a panel and its audit row.
func (s *SQLStore) CreatePowerPanel(ctx context.Context, actor domain.Actor, p *domain.PowerPanel) error {
	p.RowVersion = 1
	if err := p.Validate(); err != nil {
		return err
	}
	return s.write(ctx, actor, func(t *tx) error {
		if err := requireLiveAsset(ctx, t, "site_id", p.SiteID); err != nil {
			return err
		}
		if err := requireUniquePowerName(ctx, t,
			`power_panel`, `site_id`, p.SiteID, p.Name, p.Lifecycle, p.ID); err != nil {
			return err
		}
		_, err := t.exec(ctx, `
			INSERT INTO power_panel (id, site_id, name, voltage, amperage, phase, notes,
			                         lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.SiteID, p.Name, p.Voltage, p.Amperage, p.Phase, p.Notes,
			p.Lifecycle, p.CreatedAt, p.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating power panel")
		}
		return t.logCreate(ctx, "power_panel", p.ID, p)
	})
}

// UpdatePowerPanel persists field changes. The site is not editable: moving a
// panel between locations rewrites where every feed under it lands, which is a
// different act from correcting its rating.
func (s *SQLStore) UpdatePowerPanel(ctx context.Context, actor domain.Actor, p *domain.PowerPanel) error {
	before, err := s.GetPowerPanel(ctx, p.ID)
	if err != nil {
		return err
	}
	p.SiteID = before.SiteID
	if err := p.Validate(); err != nil {
		return err
	}
	p.CreatedAt = before.CreatedAt
	p.UpdatedAt = domain.FormatTime(s.now())

	return s.write(ctx, actor, func(t *tx) error {
		if err := requireUniquePowerName(ctx, t,
			`power_panel`, `site_id`, p.SiteID, p.Name, p.Lifecycle, p.ID); err != nil {
			return err
		}
		res, err := t.exec(ctx, `
			UPDATE power_panel SET name = ?, voltage = ?, amperage = ?, phase = ?, notes = ?,
			                       lifecycle = ?, updated_at = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			p.Name, p.Voltage, p.Amperage, p.Phase, p.Notes, p.Lifecycle, p.UpdatedAt,
			p.ID, p.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating power panel")
		}
		if err := requireVersion(res, "power_panel", p.ID, &p.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "power_panel", p.ID, &before.PowerPanel, p)
	})
}

// GetPowerPanel loads one by id.
func (s *SQLStore) GetPowerPanel(ctx context.Context, id string) (*PowerPanelRow, error) {
	var row PowerPanelRow
	if err := s.readOne(ctx, &row, powerPanelSelect+` WHERE p.id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting power panel %s: %w", id, err)
	}
	return &row, nil
}

// ListPowerPanels returns panels, by site then name.
func (s *SQLStore) ListPowerPanels(ctx context.Context, includeRetired bool) ([]PowerPanelRow, error) {
	query := powerPanelSelect
	var args []any
	if !includeRetired {
		query += ` WHERE p.lifecycle <> ?`
		args = append(args, domain.LifecycleRetired)
	}
	query += ` ORDER BY a.name, p.name`
	var rows []PowerPanelRow
	if err := s.read(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("listing power panels: %w", err)
	}
	return rows, nil
}

// RetirePowerPanel soft-deletes a panel.
//
// Refused while it still carries live feeds, the same way a manufacturer with
// live models is: the feeds would be filed under a panel no list shows, and a
// feed whose panel has vanished cannot be traced -- which is the one thing this
// model exists to do.
func (s *SQLStore) RetirePowerPanel(ctx context.Context, actor domain.Actor, id string) error {
	before, err := s.GetPowerPanel(ctx, id)
	if err != nil {
		return err
	}
	if before.Feeds > 0 {
		return fmt.Errorf("panel %s still carries %d live %s: %w",
			before.Name, before.Feeds, pluralWord(before.Feeds, "feed", "feeds"), domain.ErrConflict)
	}
	return s.retirePanelRow(ctx, actor, &before.PowerPanel)
}

// ---------- feeds ----------

// PowerFeedRow is a feed with its panel, and what is allocated on it.
type PowerFeedRow struct {
	domain.PowerFeed
	PanelName string `db:"panel_name"`
	SiteID    string `db:"site_id"`
	SiteName  string `db:"site_name"`
	// Inputs is how many live inputs draw from it; AllocatedVA is their declared
	// draw, and UndeclaredInputs is how many of them said nothing.
	//
	// The last one is what keeps the utilisation figure honest: 40% of a feed
	// over three inputs means something quite different when two of them have no
	// figure recorded at all.
	Inputs           int `db:"inputs"`
	AllocatedVA      int `db:"allocated_va"`
	UndeclaredInputs int `db:"undeclared_inputs"`
}

const powerFeedSelect = `
	SELECT f.*, p.name AS panel_name, p.site_id, a.name AS site_name,
	       (SELECT COUNT(*) FROM power_input i
	        WHERE i.feed_id = f.id AND i.lifecycle <> '` + domain.LifecycleRetired + `') AS inputs,
	       (SELECT COALESCE(SUM(i.draw_va), 0) FROM power_input i
	        WHERE i.feed_id = f.id AND i.lifecycle <> '` + domain.LifecycleRetired + `') AS allocated_va,
	       (SELECT COUNT(*) FROM power_input i
	        WHERE i.feed_id = f.id AND i.lifecycle <> '` + domain.LifecycleRetired + `'
	          AND i.draw_va IS NULL) AS undeclared_inputs
	FROM power_feed f
	JOIN power_panel p ON p.id = f.panel_id
	JOIN asset a ON a.id = p.site_id`

// CreatePowerFeed inserts a feed and its audit row.
func (s *SQLStore) CreatePowerFeed(ctx context.Context, actor domain.Actor, f *domain.PowerFeed) error {
	f.RowVersion = 1
	if err := f.Validate(); err != nil {
		return err
	}
	return s.write(ctx, actor, func(t *tx) error {
		if err := requireLivePanel(ctx, t, f.PanelID); err != nil {
			return err
		}
		if err := requireUniquePowerName(ctx, t,
			`power_feed`, `panel_id`, f.PanelID, f.Name, f.Lifecycle, f.ID); err != nil {
			return err
		}
		_, err := t.exec(ctx, `
			INSERT INTO power_feed (id, panel_id, name, voltage, amperage, phase,
			                        max_utilisation, notes, lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			f.ID, f.PanelID, f.Name, f.Voltage, f.Amperage, f.Phase,
			f.MaxUtilisation, f.Notes, f.Lifecycle, f.CreatedAt, f.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating power feed")
		}
		return t.logCreate(ctx, "power_feed", f.ID, f)
	})
}

// UpdatePowerFeed persists field changes. The panel is not editable: which
// panel a feed comes off IS the fact the redundancy finding reads, and moving it
// silently re-answers that question for every asset on the feed.
func (s *SQLStore) UpdatePowerFeed(ctx context.Context, actor domain.Actor, f *domain.PowerFeed) error {
	before, err := s.GetPowerFeed(ctx, f.ID)
	if err != nil {
		return err
	}
	f.PanelID = before.PanelID
	if err := f.Validate(); err != nil {
		return err
	}
	f.CreatedAt = before.CreatedAt
	f.UpdatedAt = domain.FormatTime(s.now())

	return s.write(ctx, actor, func(t *tx) error {
		if err := requireUniquePowerName(ctx, t,
			`power_feed`, `panel_id`, f.PanelID, f.Name, f.Lifecycle, f.ID); err != nil {
			return err
		}
		res, err := t.exec(ctx, `
			UPDATE power_feed SET name = ?, voltage = ?, amperage = ?, phase = ?,
			                      max_utilisation = ?, notes = ?, lifecycle = ?, updated_at = ?,
			                      row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			f.Name, f.Voltage, f.Amperage, f.Phase, f.MaxUtilisation, f.Notes,
			f.Lifecycle, f.UpdatedAt, f.ID, f.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating power feed")
		}
		if err := requireVersion(res, "power_feed", f.ID, &f.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "power_feed", f.ID, &before.PowerFeed, f)
	})
}

// GetPowerFeed loads one by id.
func (s *SQLStore) GetPowerFeed(ctx context.Context, id string) (*PowerFeedRow, error) {
	var row PowerFeedRow
	if err := s.readOne(ctx, &row, powerFeedSelect+` WHERE f.id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting power feed %s: %w", id, err)
	}
	return &row, nil
}

// PowerFeedFilter narrows a feed list.
type PowerFeedFilter struct {
	PanelID        string
	IncludeRetired bool
}

// ListPowerFeeds returns feeds, by site, panel then name.
func (s *SQLStore) ListPowerFeeds(ctx context.Context, f PowerFeedFilter) ([]PowerFeedRow, error) {
	query := powerFeedSelect
	var args []any
	var where []string
	if f.PanelID != "" {
		where = append(where, `f.panel_id = ?`)
		args = append(args, f.PanelID)
	}
	if !f.IncludeRetired {
		where = append(where, `f.lifecycle <> ?`)
		args = append(args, domain.LifecycleRetired)
	}
	query += whereClause(where) + ` ORDER BY a.name, p.name, f.name`
	var rows []PowerFeedRow
	if err := s.read(ctx, &rows, query, args...); err != nil {
		return nil, fmt.Errorf("listing power feeds: %w", err)
	}
	return rows, nil
}

// RetirePowerFeed soft-deletes a feed.
//
// Refused while live inputs still draw from it. Unlike a retired device type --
// which keeps answering for the assets already of it -- a withdrawn feed would
// leave assets claiming power from a circuit the model says is gone, and the
// redundancy finding would then read those assets as single-fed. Decommission
// the inputs first; that is the real-world order too.
func (s *SQLStore) RetirePowerFeed(ctx context.Context, actor domain.Actor, id string) error {
	before, err := s.GetPowerFeed(ctx, id)
	if err != nil {
		return err
	}
	if before.Inputs > 0 {
		return fmt.Errorf("feed %s still carries %d live %s: %w",
			before.Name, before.Inputs, pluralWord(before.Inputs, "input", "inputs"), domain.ErrConflict)
	}
	return s.retireFeedRow(ctx, actor, &before.PowerFeed)
}

// ---------- inputs ----------

// PowerInputRow is an input with the feed and panel behind it.
type PowerInputRow struct {
	domain.PowerInput
	AssetName string `db:"asset_name"`
	FeedName  string `db:"feed_name"`
	PanelID   string `db:"panel_id"`
	PanelName string `db:"panel_name"`
}

const powerInputSelect = `
	SELECT i.*, a.name AS asset_name, f.name AS feed_name,
	       f.panel_id, p.name AS panel_name
	FROM power_input i
	JOIN asset a ON a.id = i.asset_id
	JOIN power_feed f ON f.id = i.feed_id
	JOIN power_panel p ON p.id = f.panel_id`

// CreatePowerInput attaches an asset to a feed.
func (s *SQLStore) CreatePowerInput(ctx context.Context, actor domain.Actor, i *domain.PowerInput) error {
	i.RowVersion = 1
	if err := i.Validate(); err != nil {
		return err
	}
	return s.write(ctx, actor, func(t *tx) error {
		if err := requireLiveAsset(ctx, t, "asset_id", i.AssetID); err != nil {
			return err
		}
		if err := requireLiveFeed(ctx, t, i.FeedID); err != nil {
			return err
		}
		if err := requireUniquePowerName(ctx, t,
			`power_input`, `asset_id`, i.AssetID, i.Name, i.Lifecycle, i.ID); err != nil {
			return err
		}
		_, err := t.exec(ctx, `
			INSERT INTO power_input (id, asset_id, feed_id, name, draw_va, notes,
			                         lifecycle, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			i.ID, i.AssetID, i.FeedID, i.Name, i.DrawVA, i.Notes,
			i.Lifecycle, i.CreatedAt, i.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating power input")
		}
		return t.logCreate(ctx, "power_input", i.ID, i)
	})
}

// UpdatePowerInput persists field changes.
//
// The ASSET is not editable -- that is what it is attached to. The FEED is,
// because re-patching a lead to another circuit is the commonest real change
// here and it is exactly what somebody would come to fix after reading a false
// redundancy finding.
func (s *SQLStore) UpdatePowerInput(ctx context.Context, actor domain.Actor, i *domain.PowerInput) error {
	before, err := s.GetPowerInput(ctx, i.ID)
	if err != nil {
		return err
	}
	i.AssetID = before.AssetID
	if err := i.Validate(); err != nil {
		return err
	}
	i.CreatedAt = before.CreatedAt
	i.UpdatedAt = domain.FormatTime(s.now())

	return s.write(ctx, actor, func(t *tx) error {
		if err := requireLiveFeed(ctx, t, i.FeedID); err != nil {
			return err
		}
		if err := requireUniquePowerName(ctx, t,
			`power_input`, `asset_id`, i.AssetID, i.Name, i.Lifecycle, i.ID); err != nil {
			return err
		}
		res, err := t.exec(ctx, `
			UPDATE power_input SET feed_id = ?, name = ?, draw_va = ?, notes = ?,
			                       lifecycle = ?, updated_at = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			i.FeedID, i.Name, i.DrawVA, i.Notes, i.Lifecycle, i.UpdatedAt, i.ID, i.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating power input")
		}
		if err := requireVersion(res, "power_input", i.ID, &i.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "power_input", i.ID, &before.PowerInput, i)
	})
}

// GetPowerInput loads one by id.
func (s *SQLStore) GetPowerInput(ctx context.Context, id string) (*PowerInputRow, error) {
	var row PowerInputRow
	if err := s.readOne(ctx, &row, powerInputSelect+` WHERE i.id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting power input %s: %w", id, err)
	}
	return &row, nil
}

// PowerInputsFor lists the live inputs of one asset.
func (s *SQLStore) PowerInputsFor(ctx context.Context, assetID string) ([]PowerInputRow, error) {
	var rows []PowerInputRow
	err := s.read(ctx, &rows, powerInputSelect+
		` WHERE i.asset_id = ? AND i.lifecycle <> ? ORDER BY i.name`,
		assetID, domain.LifecycleRetired)
	if err != nil {
		return nil, fmt.Errorf("listing power inputs for %s: %w", assetID, err)
	}
	return rows, nil
}

// RetirePowerInput soft-deletes an input.
func (s *SQLStore) RetirePowerInput(ctx context.Context, actor domain.Actor, id string) error {
	before, err := s.GetPowerInput(ctx, id)
	if err != nil {
		return err
	}
	return s.retireInputRow(ctx, actor, &before.PowerInput)
}

// ---------- shared guards ----------

// The three soft deletes are written out rather than sharing one helper with
// the table name as a parameter.
//
// The shared version was written first and TestNoAssembledWriteReachesChangeLog
// refused it: a statement that names its own table can eventually be pointed at
// change_log, which is why the retention prune takes no table name. The codebase
// does allowlist that shape in four files where the dynamic target is genuinely
// load-bearing -- three cost tables behind one rollup, two project link tables.
// This is not one of those: the table is known at each call site, the statements
// will not diverge because a soft delete is the same act for all three, and an
// allowlist entry is a claim somebody has to defend in review forever. Fifteen
// lines is the cheaper side of that trade.

// RetirePowerPanelRow performs the panel soft delete.
func (s *SQLStore) retirePanelRow(ctx context.Context, actor domain.Actor, before *domain.PowerPanel) error {
	at := domain.FormatTime(s.now())
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx,
			`UPDATE power_panel SET lifecycle = ?, updated_at = ?, row_version = row_version + 1
			 WHERE id = ?`, domain.LifecycleRetired, at, before.ID)
		if err != nil {
			return translateWriteErr(err, "retiring power panel")
		}
		after := *before
		after.Lifecycle, after.UpdatedAt = domain.LifecycleRetired, at
		return t.logUpdate(ctx, "power_panel", before.ID, before, &after)
	})
}

func (s *SQLStore) retireFeedRow(ctx context.Context, actor domain.Actor, before *domain.PowerFeed) error {
	at := domain.FormatTime(s.now())
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx,
			`UPDATE power_feed SET lifecycle = ?, updated_at = ?, row_version = row_version + 1
			 WHERE id = ?`, domain.LifecycleRetired, at, before.ID)
		if err != nil {
			return translateWriteErr(err, "retiring power feed")
		}
		after := *before
		after.Lifecycle, after.UpdatedAt = domain.LifecycleRetired, at
		return t.logUpdate(ctx, "power_feed", before.ID, before, &after)
	})
}

func (s *SQLStore) retireInputRow(ctx context.Context, actor domain.Actor, before *domain.PowerInput) error {
	at := domain.FormatTime(s.now())
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx,
			`UPDATE power_input SET lifecycle = ?, updated_at = ?, row_version = row_version + 1
			 WHERE id = ?`, domain.LifecycleRetired, at, before.ID)
		if err != nil {
			return translateWriteErr(err, "retiring power input")
		}
		after := *before
		after.Lifecycle, after.UpdatedAt = domain.LifecycleRetired, at
		return t.logUpdate(ctx, "power_input", before.ID, before, &after)
	})
}

// requireUniquePowerName enforces the natural key with a MESSAGE, before the
// partial unique index enforces it with a status code. Same division of labour
// as requireUniqueSiblingName, and the same reason: the index is the guarantee,
// this is what an operator reads.
func requireUniquePowerName(ctx context.Context, t *tx,
	table, parentColumn, parentID, name, lifecycle, excludeID string) error {

	if lifecycle == domain.LifecycleRetired {
		return nil
	}
	var n int
	err := t.get(ctx, &n,
		`SELECT COUNT(*) FROM `+table+
			` WHERE `+parentColumn+` = ? AND name = ? AND lifecycle <> ? AND id <> ?`,
		parentID, name, domain.LifecycleRetired, excludeID)
	if err != nil {
		return fmt.Errorf("checking for a duplicate name: %w", err)
	}
	if n == 0 {
		return nil
	}
	ve := &domain.ValidationError{}
	ve.Add("name", "something here is already called %q", name)
	return ve
}

func requireLiveAsset(ctx context.Context, t *tx, field, id string) error {
	var lifecycle string
	if err := t.get(ctx, &lifecycle, `SELECT lifecycle FROM asset WHERE id = ?`, id); err != nil {
		ve := &domain.ValidationError{}
		ve.Add(field, "choose an asset")
		return ve
	}
	if lifecycle == domain.LifecycleRetired {
		ve := &domain.ValidationError{}
		ve.Add(field, "that asset has been retired")
		return ve
	}
	return nil
}

func requireLivePanel(ctx context.Context, t *tx, id string) error {
	var lifecycle string
	if err := t.get(ctx, &lifecycle, `SELECT lifecycle FROM power_panel WHERE id = ?`, id); err != nil {
		ve := &domain.ValidationError{}
		ve.Add("panel_id", "choose a panel")
		return ve
	}
	if lifecycle == domain.LifecycleRetired {
		ve := &domain.ValidationError{}
		ve.Add("panel_id", "that panel has been retired")
		return ve
	}
	return nil
}

func requireLiveFeed(ctx context.Context, t *tx, id string) error {
	var lifecycle string
	if err := t.get(ctx, &lifecycle, `SELECT lifecycle FROM power_feed WHERE id = ?`, id); err != nil {
		ve := &domain.ValidationError{}
		ve.Add("feed_id", "choose a feed")
		return ve
	}
	if lifecycle == domain.LifecycleRetired {
		ve := &domain.ValidationError{}
		ve.Add("feed_id", "that feed has been retired")
		return ve
	}
	return nil
}

// sortedStrings returns a sorted copy, so a finding reads the same twice.
func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
