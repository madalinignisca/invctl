-- An endpoint can stop existing.
--
-- Until now it could not: there was no soft delete for a listening socket, so a
-- service that moved from :8080 to :8443 left you two bad options -- edit the
-- row, losing the fact that :8080 was ever true, or add a second row and leave
-- a phantom socket listed forever. An inventory whose job is saying what is
-- there cannot only ever gain sockets.
--
-- NARROW ON PURPOSE: active or retired, the same two values a placement gets.
-- An endpoint either exists or it does not; 'planned' and 'deprecated' are
-- states of the SERVICE, and offering them here would invite writing them
-- somewhere nothing reads them.
--
-- THE READ AUDIT. Fifteen queries touch this table and adding a lifecycle
-- means each had to decide what a retired socket means to it. Recorded here
-- because the decisions are not derivable from the code -- a missing WHERE
-- looks identical whether it was considered or forgotten.
--
--   FILTERED -- a retired socket must not be offered or traversed:
--     LoadGraph (graph.go)            impact must not simulate through a
--                                     socket that does not exist
--     searchStructured port lookup    "what listens on :443" answering with a
--                                     dead socket sends somebody to the wrong box
--     ListAllEndpoints (deps.go)      the dependency picker: you cannot start
--                                     depending on something withdrawn
--     neighbourhoodWorkloads          the diagram draws what is there
--
--   NOT FILTERED -- the row is a fact about something else that still exists:
--     endpointSelect                  the service page SHOWS retired sockets,
--                                     struck through, like a withdrawn placement
--     dependencySelect (x2)           a dependency names its provider; hiding
--                                     the join blanks the provider on an edge
--                                     that still exists. The DEPENDENCY's own
--                                     lifecycle governs whether it counts.
--     routeSelect                     same: a route's frontend is a fact about
--                                     the route
--     serviceNeighbours (x3)          the timeline is history, and a retired
--                                     socket's history is exactly what is wanted
--     ProjectExternalDependencies(x2) provider naming, as dependencySelect
--     neighbourhood dependency joins  an ACTIVE dependency through a retired
--                                     socket is a finding worth seeing, not
--                                     hiding
--
-- BACKEND_MEMBER IS NOT A READ OF THIS TABLE, and was missing from the list
-- above until a security review asked why. A socket can be reached without any
-- dependency naming it -- as a backend behind a route -- and LoadGraph keeps a
-- membership row whose endpoint has gone from the map, which the engine skips
-- in silence. So the decision is not a filter but a refusal: RetireEndpoint
-- will not withdraw a socket that is still a pool member, because removing a
-- backend is a capacity decision and nothing here can tell whether the pool has
-- anything left to serve with.
--
-- NO NEW INDEX. The filtered queries are either whole-table scans that were
-- already whole-table scans (LoadGraph) or already selective on another column.
-- Adding a partial index on a guess is how the last two index migrations got
-- written and then measured back out.

-- +goose Up
ALTER TABLE endpoint ADD COLUMN lifecycle TEXT NOT NULL DEFAULT 'active';
ALTER TABLE endpoint ADD CONSTRAINT endpoint_lifecycle_check
  CHECK (lifecycle IN ('active','retired'));

-- +goose Down
ALTER TABLE endpoint DROP CONSTRAINT endpoint_lifecycle_check;
ALTER TABLE endpoint DROP COLUMN lifecycle;
