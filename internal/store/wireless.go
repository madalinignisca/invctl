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
	"strings"

	"github.com/madalinignisca/invctl/internal/domain"
)

// ---------- wireless LANs ----------

// WirelessLANRow is an SSID with everything the list needs resolved: its
// security label, its scope, its VLAN and how many radios broadcast it.
type WirelessLANRow struct {
	domain.WirelessLAN
	SecurityLabel   string `db:"security_label"`
	ScopeAssetName  string `db:"scope_asset_name"`
	VLANName        string `db:"vlan_name"`
	VLANVID         *int   `db:"vlan_vid"`
	AuthServiceName string `db:"auth_service_name"`
	// RadioCount is what turns a record into a broadcast SSID on screen. An
	// SSID with none is declared and on the air nowhere, which is a finding
	// rather than a gap in this query.
	RadioCount int `db:"radio_count"`
	// AssetCount is DISTINCT assets, not radios: that is the number the
	// impact engine counts, and showing radios beside a finding that counts
	// boxes would make the page disagree with the simulator.
	AssetCount int `db:"asset_count"`
}

// ListWirelessLANs returns every live SSID, scope name first.
func (s *SQLStore) ListWirelessLANs(ctx context.Context) ([]WirelessLANRow, error) {
	var rows []WirelessLANRow
	err := s.read(ctx, &rows, `
		SELECT w.*,
		       COALESCE(sec.label, '') AS security_label,
		       COALESCE(a.name, '')    AS scope_asset_name,
		       COALESCE(v.name, '')    AS vlan_name,
		       v.vid                   AS vlan_vid,
		       COALESCE(svc.name, '')  AS auth_service_name,
		       (SELECT COUNT(*) FROM interface_wlan iw WHERE iw.wireless_lan_id = w.id) AS radio_count,
		       (SELECT COUNT(DISTINCT i.asset_id) FROM interface_wlan iw
		          JOIN interface i ON i.id = iw.interface_id
		         WHERE iw.wireless_lan_id = w.id) AS asset_count
		FROM wireless_lan w
		LEFT JOIN wireless_security sec ON sec.code = w.security
		LEFT JOIN asset a   ON a.id   = w.scope_asset_id
		LEFT JOIN vlan v    ON v.id   = w.vlan_id
		LEFT JOIN service svc ON svc.id = w.auth_service_id
		WHERE w.lifecycle <> 'retired'
		ORDER BY COALESCE(a.name, ''), w.ssid`)
	if err != nil {
		return nil, fmt.Errorf("listing wireless lans: %w", err)
	}
	return rows, nil
}

// GetWirelessLAN loads one SSID by id.
func (s *SQLStore) GetWirelessLAN(ctx context.Context, id string) (*domain.WirelessLAN, error) {
	var w domain.WirelessLAN
	if err := s.readOne(ctx, &w, `SELECT * FROM wireless_lan WHERE id = ?`, id); err != nil {
		return nil, fmt.Errorf("getting wireless lan %s: %w", id, err)
	}
	return &w, nil
}

// CreateWirelessLAN declares an SSID.
func (s *SQLStore) CreateWirelessLAN(ctx context.Context, p domain.Permit, w *domain.WirelessLAN) error {
	if err := w.Validate(); err != nil {
		return err
	}
	w.RowVersion = 1
	at := domain.FormatTime(s.now())
	w.CreatedAt, w.UpdatedAt = &at, &at
	return s.write(ctx, p, func(t *tx) error {
		if err := t.requireVocabulary(ctx, vocabWirelessSecurity, "security", w.Security); err != nil {
			return err
		}
		_, err := t.exec(ctx, `
			INSERT INTO wireless_lan (id, name, ssid, security, scope_asset_id, vlan_id,
			                          auth_service_id, psk_ref, notes, lifecycle,
			                          created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			w.ID, w.Name, w.SSID, w.Security, w.ScopeAssetID, w.VLANID,
			w.AuthServiceID, w.PSKRef, w.Notes, w.Lifecycle, w.CreatedAt, w.UpdatedAt)
		if err != nil {
			return translateWriteErr(err, "creating wireless lan")
		}
		if err := t.logCreate(ctx, "wireless_lan", w.ID, w); err != nil {
			return err
		}
		// The SSID is exactly what somebody types into search during an
		// incident ("who broadcasts warehouse-scan"), which is the whole
		// reason to index it beside the name.
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "wireless_lan", EntityID: w.ID,
			Title: w.SSID + " — " + w.Name,
			Body:  w.SSID + " " + w.Name,
		})
	})
}

// UpdateWirelessLAN corrects an SSID's declared configuration.
//
// lifecycle is not in the SET list -- retirement is RetireWirelessLAN's job,
// exactly as UpdateVLAN leaves it out.
func (s *SQLStore) UpdateWirelessLAN(ctx context.Context, p domain.Permit, w *domain.WirelessLAN) error {
	if err := w.Validate(); err != nil {
		return err
	}
	before, err := s.GetWirelessLAN(ctx, w.ID)
	if err != nil {
		return err
	}
	at := domain.FormatTime(s.now())
	w.UpdatedAt = &at

	return s.write(ctx, p, func(t *tx) error {
		if err := t.requireVocabulary(ctx, vocabWirelessSecurity, "security", w.Security); err != nil {
			return err
		}
		res, err := t.exec(ctx, `
			UPDATE wireless_lan SET name = ?, ssid = ?, security = ?, scope_asset_id = ?,
			                        vlan_id = ?, auth_service_id = ?, psk_ref = ?, notes = ?,
			                        updated_at = ?, row_version = row_version + 1
			WHERE id = ? AND row_version = ?`,
			w.Name, w.SSID, w.Security, w.ScopeAssetID, w.VLANID, w.AuthServiceID,
			w.PSKRef, w.Notes, at, w.ID, w.RowVersion)
		if err != nil {
			return translateWriteErr(err, "updating wireless lan")
		}
		if err := requireVersion(res, "wireless_lan", w.ID, &w.RowVersion); err != nil {
			return err
		}
		if err := t.logUpdate(ctx, "wireless_lan", w.ID, before, w); err != nil {
			return err
		}
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "wireless_lan", EntityID: w.ID,
			Title: w.SSID + " — " + w.Name,
			Body:  w.SSID + " " + w.Name,
		})
	})
}

// RetireWirelessLAN withdraws an SSID.
//
// It refuses while any radio still broadcasts it, the same rule and the
// same reason RetireVLAN gives: a retired SSID with radios on it is a
// record saying "this does not exist" beside several saying "and here is
// what carries it" -- and soft delete means the contradiction is permanent
// rather than merely wrong.
func (s *SQLStore) RetireWirelessLAN(ctx context.Context, p domain.Permit, id string) error {
	before, err := s.GetWirelessLAN(ctx, id)
	if err != nil {
		return err
	}
	if before.Retired() {
		return nil
	}
	at := domain.FormatTime(s.now())
	after := *before
	after.Lifecycle = domain.LifecycleRetired
	after.UpdatedAt = &at

	return s.writeSerializable(ctx, p, func(t *tx) error {
		var radios int
		if err := t.get(ctx, &radios,
			`SELECT COUNT(*) FROM interface_wlan WHERE wireless_lan_id = ?`, id); err != nil {
			return fmt.Errorf("counting radios broadcasting wireless lan %s: %w", id, err)
		}
		if radios > 0 {
			return fmt.Errorf("%w: %d radio(s) still broadcast this SSID", domain.ErrConflict, radios)
		}
		res, err := t.exec(ctx, `
			UPDATE wireless_lan SET lifecycle = 'retired', updated_at = ?,
			                        row_version = row_version + 1
			WHERE id = ? AND row_version = ?`, at, id, before.RowVersion)
		if err != nil {
			return translateWriteErr(err, "retiring wireless lan")
		}
		if err := requireVersion(res, "wireless_lan", id, &before.RowVersion); err != nil {
			return err
		}
		return t.logUpdate(ctx, "wireless_lan", id, before, &after)
	})
}

// ---------- radio membership ----------

// WLANRadio is one radio broadcasting an SSID, with the box it is in.
type WLANRadio struct {
	InterfaceID   string `db:"interface_id"`
	InterfaceName string `db:"interface_name"`
	FormFactor    string `db:"form_factor"`
	Enabled       bool   `db:"enabled"`
	AssetID       string `db:"asset_id"`
	AssetName     string `db:"asset_name"`
}

// ListWLANRadios returns every radio broadcasting an SSID, with the box it is
// in.
//
// THIS IS THE EDGE. A wireless LAN with a VLAN and no radios is a record;
// this is what makes it a broadcast domain, and it is a fact no cable trace
// can produce -- two laptops on `corp` can reach each other and no cable
// joins them.
func (s *SQLStore) ListWLANRadios(ctx context.Context, wlanID string) ([]WLANRadio, error) {
	var rows []WLANRadio
	err := s.read(ctx, &rows, `
		SELECT iw.interface_id, i.name AS interface_name, i.form_factor, i.enabled,
		       a.id AS asset_id, a.name AS asset_name
		FROM interface_wlan iw
		JOIN interface i ON i.id = iw.interface_id
		JOIN asset a ON a.id = i.asset_id
		WHERE iw.wireless_lan_id = ?
		ORDER BY a.name, i.name`, wlanID)
	if err != nil {
		return nil, fmt.Errorf("listing radios on wireless lan %s: %w", wlanID, err)
	}
	return rows, nil
}

// ListRadioOptions returns every radio-form-factor port, with its box, for
// a "broadcast this SSID from" picker.
//
// The filter is `form_factor LIKE 'radio%'`, NOT a hardcoded IN-list of the
// three seeded codes: form factors are a vocabulary and radio_60g will
// arrive as an INSERT, so an IN-list would make it storable and unofferable
// -- the exact failure vocabulary.go's own header describes for a value
// that "would be storable but unofferable". LIKE with a literal pattern
// behaves identically on both engines; no ESCAPE clause is needed because
// the pattern has no metacharacter beyond the trailing %.
func (s *SQLStore) ListRadioOptions(ctx context.Context) ([]InterfaceOption, error) {
	var opts []InterfaceOption
	err := s.read(ctx, &opts, `
		SELECT i.*, a.name AS asset_name
		FROM interface i
		JOIN asset a ON a.id = i.asset_id
		WHERE a.lifecycle <> 'retired' AND i.form_factor LIKE 'radio%'
		ORDER BY a.name, i.name`)
	if err != nil {
		return nil, fmt.Errorf("listing radios: %w", err)
	}
	return opts, nil
}

// ListInterfaceWLANMembers returns a radio's current SSID membership, in the
// shape SetInterfaceWLANs takes -- so a caller adding one SSID reads,
// appends and writes back rather than reconstructing what was already
// there.
func (s *SQLStore) ListInterfaceWLANMembers(ctx context.Context, interfaceID string) ([]domain.InterfaceWLAN, error) {
	return s.currentWLANMembership(ctx, interfaceID)
}

// interfaceWLANAudit is the audited shape of a radio: the port row plus the
// SSIDs it broadcasts.
//
// THE `db` TAGS ARE THE WHOLE POINT. diffJSON compares every db-TAGGED field
// and ignores the rest (internal/store/diff.go), and writing to
// interface_wlan changes no column of the interface row -- so an audit that
// diffed a plain domain.Interface would compare a row that did not change
// and record SILENCE. The radio moved from `corp` to `guest` and change_log
// says nothing happened.
//
// THAT FAILURE HAS BEEN MADE FOUR TIMES IN THIS CODEBASE, and the fourth was
// in SetInterfaceVLANs -- the function this one copies. interfaceVLANAudit's
// own comment names it: "an audit struct whose membership fields carried
// only json tags diffed nothing but the interface id -- which never
// changes." A json tag here would compile, run, write an entry, and record
// nothing.
//
// SSID NAMES rather than row ids, and a joined string rather than a slice,
// both for the reason assetAudit gives: an audit entry is read by people,
// and "corp,guest" is a sentence where two UUIDs are a lookup exercise.
//
// EMBEDDED BY VALUE, never by pointer: auditFields panics on an anonymous
// pointer embed, because the shape that made the certificate audit record
// nothing at all matched neither branch and dropped every embedded column
// while still writing an entry.
type interfaceWLANAudit struct {
	domain.Interface
	WirelessLANs string `db:"wireless_lans"` // db, NOT json
}

func auditedInterfaceWLANs(i *domain.Interface, ssids []string) *interfaceWLANAudit {
	names := append([]string(nil), ssids...)
	sort.Strings(names)
	return &interfaceWLANAudit{
		Interface:    *i,
		WirelessLANs: strings.Join(names, ","),
	}
}

// SetInterfaceWLANs replaces a radio's whole set of broadcast SSIDs.
//
// REPLACED WHOLESALE, like interface_vlan and asset_environment, and
// audited on the INTERFACE -- the membership belongs to the radio and has
// no life of its own. The parent's change_log entry has to record it, or a
// radio moving from `corp` to `guest` produces no diff at all: the failure
// CLAUDE.md names three times over and this codebase has now made four.
// See interfaceWLANAudit for the mechanism that prevents a fifth.
func (s *SQLStore) SetInterfaceWLANs(ctx context.Context, p domain.Permit,
	interfaceID string, members []domain.InterfaceWLAN) error {

	if err := domain.ValidateWLANMembership(members); err != nil {
		return err
	}
	iface, err := s.GetInterface(ctx, interfaceID)
	if err != nil {
		return err
	}
	beforeSSIDs, err := s.listInterfaceWLANSSIDs(ctx, interfaceID)
	if err != nil {
		return err
	}
	before := auditedInterfaceWLANs(iface, beforeSSIDs)

	// WLAN membership is a write to the INTERFACE -- the audited value folds
	// the SSID list into the interface row and the change_log entry below
	// names entity_type "interface". So it takes the same subject derivation
	// every other interface write takes, or a project owner could edit an
	// interface on their own asset and then be refused when setting its
	// SSIDs, which is the same permission expressed two ways.
	//
	// NO NEW MINTER: authorizeInterfaceSubject is already in
	// storePermitMinters. A wireless-specific one would be a
	// security-relevant change requiring auth review and sign-off
	// (permit_source_test.go), and there is nothing here it could check that
	// this does not (D8).
	//
	// Reached from HTTP through AddRadioToWLAN and RemoveRadioFromWLAN, which
	// both delegate here -- POST /wireless/{id}/radios and its remove
	// sibling.
	ifacePermit, err := authorizeInterfaceSubject(p, iface.AssetID, interfaceID)
	if err != nil {
		return err
	}
	at := domain.FormatTime(s.now())

	return s.write(ctx, ifacePermit, func(t *tx) error {
		if _, err := t.exec(ctx,
			`DELETE FROM interface_wlan WHERE interface_id = ?`, interfaceID); err != nil {
			return translateWriteErr(err, "clearing wireless membership")
		}
		for _, m := range members {
			_, err := t.exec(ctx,
				`INSERT INTO interface_wlan (interface_id, wireless_lan_id) VALUES (?, ?)`,
				interfaceID, m.WirelessLANID)
			if err != nil {
				return translateWriteErr(err, "setting wireless membership")
			}
		}
		// The interface's own row carries the audit, so its timestamp moves
		// with the change it is recording.
		if _, err := t.exec(ctx, `
			UPDATE interface SET updated_at = ?, row_version = row_version + 1
			WHERE id = ?`, at, interfaceID); err != nil {
			return translateWriteErr(err, "touching interface")
		}

		// Read the resulting SSIDs back INSIDE the transaction. Composing
		// them from `members` in Go would look equivalent and is not:
		// members carries ids, the audit carries names, and resolving names
		// outside the transaction reads a wireless_lan row that another
		// transaction may be renaming.
		var ssids []string
		err := t.selectAll(ctx, &ssids, `
			SELECT w.ssid
			FROM interface_wlan iw JOIN wireless_lan w ON w.id = iw.wireless_lan_id
			WHERE iw.interface_id = ? ORDER BY w.ssid`, interfaceID)
		if err != nil {
			return fmt.Errorf("reading wireless membership: %w", err)
		}
		updated := *iface
		updated.UpdatedAt = &at
		updated.RowVersion = iface.RowVersion + 1
		after := auditedInterfaceWLANs(&updated, ssids)
		return t.logUpdate(ctx, "interface", interfaceID, before, after)
	})
}

// AddRadioToWLAN puts one radio on one SSID, keeping its other memberships.
//
// The set table is replaced wholesale, so adding one member means reading
// the radio's current set and writing it back with the new one -- which is
// why this goes through SetInterfaceWLANs rather than inserting directly.
// Doing the INSERT here would skip the validation AND the audit fold, and
// the audit fold is the thing this codebase has now got wrong four times.
func (s *SQLStore) AddRadioToWLAN(ctx context.Context, p domain.Permit, wlanID, interfaceID string) error {
	current, err := s.currentWLANMembership(ctx, interfaceID)
	if err != nil {
		return err
	}
	for _, m := range current {
		if m.WirelessLANID == wlanID {
			return nil // already broadcasting it; nothing to record
		}
	}
	current = append(current, domain.InterfaceWLAN{
		InterfaceID: interfaceID, WirelessLANID: wlanID,
	})
	return s.SetInterfaceWLANs(ctx, p, interfaceID, current)
}

// RemoveRadioFromWLAN takes one radio off one SSID, keeping the rest.
func (s *SQLStore) RemoveRadioFromWLAN(ctx context.Context, p domain.Permit, wlanID, interfaceID string) error {
	current, err := s.currentWLANMembership(ctx, interfaceID)
	if err != nil {
		return err
	}
	kept := make([]domain.InterfaceWLAN, 0, len(current))
	for _, m := range current {
		if m.WirelessLANID != wlanID {
			kept = append(kept, m)
		}
	}
	if len(kept) == len(current) {
		return nil // not broadcasting it; nothing to record
	}
	return s.SetInterfaceWLANs(ctx, p, interfaceID, kept)
}

func (s *SQLStore) currentWLANMembership(ctx context.Context, interfaceID string) ([]domain.InterfaceWLAN, error) {
	var rows []domain.InterfaceWLAN
	err := s.read(ctx, &rows,
		`SELECT interface_id, wireless_lan_id FROM interface_wlan WHERE interface_id = ?`,
		interfaceID)
	if err != nil {
		return nil, fmt.Errorf("reading wireless membership: %w", err)
	}
	return rows, nil
}

func (s *SQLStore) listInterfaceWLANSSIDs(ctx context.Context, interfaceID string) ([]string, error) {
	var ssids []string
	err := s.read(ctx, &ssids, `
		SELECT w.ssid
		FROM interface_wlan iw JOIN wireless_lan w ON w.id = iw.wireless_lan_id
		WHERE iw.interface_id = ? ORDER BY w.ssid`, interfaceID)
	if err != nil {
		return nil, fmt.Errorf("reading wireless membership: %w", err)
	}
	return ssids, nil
}

// AssetRadio, ListAssetRadios and the wireless HTTP surface belong to
// WP-F1 Task 7/7b (UI) and are deliberately not part of this file -- this
// work package stops at Task 6, the impact-graph join.
