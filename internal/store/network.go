package store

import (
	"context"
	"fmt"

	"github.com/gabriel/invctl/internal/domain"
)

// ---------- interfaces ----------

// InterfaceRow is an interface with its addresses and the far end of its cable
// already resolved -- everything the asset detail page shows in one query set.
type InterfaceRow struct {
	domain.Interface
	AssetName string `db:"asset_name"`
	Addresses []domain.IPAddress
	PeerAsset string
	PeerPort  string
}

// ListInterfaces returns the ports on an asset, with addresses attached.
func (s *SQLStore) ListInterfaces(ctx context.Context, assetID string) ([]InterfaceRow, error) {
	var ifaces []domain.Interface
	err := s.read(ctx, &ifaces,
		`SELECT * FROM interface WHERE asset_id = ? ORDER BY is_mgmt DESC, name`, assetID)
	if err != nil {
		return nil, fmt.Errorf("listing interfaces of %s: %w", assetID, err)
	}
	if len(ifaces) == 0 {
		return nil, nil
	}

	rows := make([]InterfaceRow, len(ifaces))
	ids := make([]string, len(ifaces))
	index := make(map[string]int, len(ifaces))
	for i, iface := range ifaces {
		rows[i] = InterfaceRow{Interface: iface}
		ids[i] = iface.ID
		index[iface.ID] = i
	}

	var addrs []domain.IPAddress
	err = s.read(ctx, &addrs,
		`SELECT * FROM ip_address WHERE interface_id IN (`+placeholders(len(ids))+`) ORDER BY addr_text`,
		anySlice(ids)...)
	if err != nil {
		return nil, fmt.Errorf("loading addresses: %w", err)
	}
	for _, a := range addrs {
		if a.InterfaceID == nil {
			continue
		}
		if i, ok := index[*a.InterfaceID]; ok {
			rows[i].Addresses = append(rows[i].Addresses, a)
		}
	}

	// The far end of each cable. A link is undirected in meaning but stored
	// with an a/b side, so both orientations have to be considered.
	type peer struct {
		NearID    string `db:"near_id"`
		PeerPort  string `db:"peer_port"`
		PeerAsset string `db:"peer_asset"`
	}
	var peers []peer
	err = s.read(ctx, &peers, `
		SELECT l.a_interface_id AS near_id, bi.name AS peer_port, ba.name AS peer_asset
		FROM link l
		JOIN interface bi ON bi.id = l.b_interface_id
		JOIN asset ba ON ba.id = bi.asset_id
		WHERE l.a_interface_id IN (`+placeholders(len(ids))+`)`, anySlice(ids)...)
	if err != nil {
		return nil, fmt.Errorf("loading link peers (a side): %w", err)
	}
	var peersB []peer
	err = s.read(ctx, &peersB, `
		SELECT l.b_interface_id AS near_id, ai.name AS peer_port, aa.name AS peer_asset
		FROM link l
		JOIN interface ai ON ai.id = l.a_interface_id
		JOIN asset aa ON aa.id = ai.asset_id
		WHERE l.b_interface_id IN (`+placeholders(len(ids))+`)`, anySlice(ids)...)
	if err != nil {
		return nil, fmt.Errorf("loading link peers (b side): %w", err)
	}
	for _, p := range append(peers, peersB...) {
		if i, ok := index[p.NearID]; ok {
			rows[i].PeerAsset = p.PeerAsset
			rows[i].PeerPort = p.PeerPort
		}
	}
	return rows, nil
}

// CreateInterface inserts a port.
func (s *SQLStore) CreateInterface(ctx context.Context, actor domain.Actor, i *domain.Interface) error {
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO interface (id, asset_id, name, form_factor, speed_mbps, mac, mtu,
			                       lag_parent_id, is_mgmt, enabled)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			i.ID, i.AssetID, i.Name, i.FormFactor, i.SpeedMbps, i.MAC, i.MTU,
			i.LagParentID, i.IsMgmt, i.Enabled)
		if err != nil {
			return translateWriteErr(err, "creating interface")
		}
		return t.logCreate(ctx, "interface", i.ID, i)
	})
}

// CreateLink cables two interfaces together.
func (s *SQLStore) CreateLink(ctx context.Context, actor domain.Actor, l *domain.Link) error {
	return s.write(ctx, actor, func(t *tx) error {
		// A port has one cable. Catching the second one here gives a usable
		// error instead of a silently duplicated topology.
		var n int
		err := t.get(ctx, &n, `
			SELECT COUNT(*) FROM link
			WHERE a_interface_id IN (?, ?) OR b_interface_id IN (?, ?)`,
			l.AInterfaceID, l.BInterfaceID, l.AInterfaceID, l.BInterfaceID)
		if err != nil {
			return fmt.Errorf("checking existing links: %w", err)
		}
		if n > 0 {
			return fmt.Errorf("cabling %s to %s: one of those ports is already patched: %w",
				l.AInterfaceID, l.BInterfaceID, domain.ErrConflict)
		}
		_, err = t.exec(ctx,
			`INSERT INTO link (id, a_interface_id, b_interface_id, medium, length_m)
			 VALUES (?, ?, ?, ?, ?)`,
			l.ID, l.AInterfaceID, l.BInterfaceID, l.Medium, l.LengthM)
		if err != nil {
			return translateWriteErr(err, "creating link")
		}
		return t.logCreate(ctx, "link", l.ID, l)
	})
}

// ---------- addressing ----------

// CreateIPAddress assigns an address.
func (s *SQLStore) CreateIPAddress(ctx context.Context, actor domain.Actor, a *domain.IPAddress) error {
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx,
			`INSERT INTO ip_address (id, addr_text, addr_family, addr_start, interface_id, role)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			a.ID, a.AddrText, a.AddrFamily, a.AddrStart, a.InterfaceID, a.Role)
		if err != nil {
			return translateWriteErr(err, "creating ip address")
		}
		return t.logCreate(ctx, "ip_address", a.ID, a)
	})
}

// CreatePrefix inserts a network.
func (s *SQLStore) CreatePrefix(ctx context.Context, actor domain.Actor, p *domain.Prefix) error {
	return s.write(ctx, actor, func(t *tx) error {
		_, err := t.exec(ctx, `
			INSERT INTO prefix (id, cidr_text, addr_family, addr_start, addr_end,
			                    vlan_id, environment_id, role)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			p.ID, p.CIDRText, p.AddrFamily, p.AddrStart, p.AddrEnd,
			p.VLANID, p.EnvironmentID, p.Role)
		if err != nil {
			return translateWriteErr(err, "creating prefix")
		}
		if err := t.logCreate(ctx, "prefix", p.ID, p); err != nil {
			return err
		}
		return s.indexEntity(ctx, t, searchDoc{
			EntityType: "prefix", EntityID: p.ID,
			Title: p.CIDRText, Subtitle: derefString(p.Role), Body: p.CIDRText,
		})
	})
}

// PrefixRow is a prefix with its environment and utilisation resolved.
type PrefixRow struct {
	domain.Prefix
	EnvironmentCode string `db:"environment_code"`
	AddressCount    int    `db:"address_count"`
}

// ListPrefixes returns every network, narrowest last.
func (s *SQLStore) ListPrefixes(ctx context.Context) ([]PrefixRow, error) {
	var rows []PrefixRow
	err := s.read(ctx, &rows, `
		SELECT p.*, COALESCE(e.code, '') AS environment_code,
		       (SELECT COUNT(*) FROM ip_address ip
		         WHERE ip.addr_family = p.addr_family
		           AND ip.addr_start >= p.addr_start
		           AND ip.addr_start <= p.addr_end) AS address_count
		FROM prefix p
		LEFT JOIN environment e ON e.id = p.environment_id
		ORDER BY p.addr_family, p.addr_start`)
	if err != nil {
		return nil, fmt.Errorf("listing prefixes: %w", err)
	}
	return rows, nil
}

// ResolveAddress finds the most specific prefix containing an address.
//
// This is the query the four-column pattern exists for: a bytewise range scan
// over an index, with no inet type and no engine-specific operator.
func (s *SQLStore) ResolveAddress(ctx context.Context, addr string) (*domain.Prefix, error) {
	av, err := domain.ParseAddr(addr)
	if err != nil {
		return nil, fmt.Errorf("resolving address: %w", err)
	}
	var p domain.Prefix
	err = s.readOne(ctx, &p, `
		SELECT * FROM prefix
		WHERE addr_family = ? AND addr_start <= ? AND addr_end >= ?
		ORDER BY addr_start DESC
		LIMIT 1`, av.Family, av.Start, av.Start)
	if err != nil {
		return nil, fmt.Errorf("resolving address %s: %w", addr, err)
	}
	return &p, nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
