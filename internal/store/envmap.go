package store

import (
	"context"
	"fmt"

	"github.com/gabriel/invctl/internal/domain"
)

// The environment map: everything in one environment, all three layers.
//
// The walkthrough view, and the one that gets dense fast -- which is why it
// carries the same node budget as the neighbourhood and reports its cut the
// same way. Everything below the asset list is the neighbourhood's own
// loaders: links with both ends inside, workloads on those hosts, and the
// declared dependencies among them.

// EnvironmentMap loads one environment's whole picture.
func (s *SQLStore) EnvironmentMap(ctx context.Context, envID string, maxAssets int) (*NeighbourhoodGraph, error) {
	if maxAssets <= 0 {
		maxAssets = NeighbourhoodDefaultMaxAssets
	}
	g := &NeighbourhoodGraph{}

	// Ordered by name so the budget's cut is stable and the seed ordering the
	// layout consumes is a pure function of the data. Retired assets are kept
	// forever but are noise in a live map.
	var assets []NeighbourAsset
	err := s.read(ctx, &assets, `
		SELECT a.id, 0 AS hop, a.kind, a.name, a.lifecycle,
		       COALESCE(a.parent_id, '') AS parent_id
		FROM asset a
		JOIN asset_environment ae ON ae.asset_id = a.id
		WHERE ae.environment_id = ? AND a.lifecycle <> ?
		ORDER BY a.name, a.id`, envID, domain.LifecycleRetired)
	if err != nil {
		return nil, fmt.Errorf("loading the assets of %s: %w", envID, err)
	}
	if len(assets) > maxAssets {
		g.AssetsElided = len(assets) - maxAssets
		assets = assets[:maxAssets]
	}
	g.Assets = assets
	if len(assets) == 0 {
		return g, nil
	}

	ids := make([]string, len(assets))
	for i, a := range assets {
		ids[i] = a.ID
	}
	if g.Links, err = s.neighbourhoodLinks(ctx, ids); err != nil {
		return nil, err
	}
	if err := s.neighbourhoodWorkloads(ctx, ids, g); err != nil {
		return nil, err
	}
	return g, nil
}
