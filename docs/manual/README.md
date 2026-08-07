# invctl — operator manual

Written for somebody who has to use this during an incident, not evaluate it.

| | |
|---|---|
| [Getting started](parts/00-getting-started.md) | signing in, the overview, finding your way around |
| [The estate](parts/20-estate.md) | assets, the hardware catalogue, power, clusters |
| [Addressing](parts/30-addressing.md) | prefixes, reservations, VLANs, allocations |
| [Network](parts/40-network.md) | topology, circuits, overlays, redundancy groups |
| [Reports](parts/50-reports.md) | outage simulation, what expires, power findings, the change log |

Screenshots are of the public demo at `https://invctl.madalin.me`, which has a
small Norwegian estate in it. Your estate will look nothing like it; the
software works the same.

---

## For whoever maintains this

The manual is **fragments**, not a document. Each one declares the source paths
it describes and the commit it was written against, so staleness is a question
with an answer rather than a judgement:

```
make manual-stale        # which fragments describe code that has changed
make manual-stale-v      # ...and which files changed
```

Regenerate **only** what that names. The contract for doing so — capture rules,
what not to invent, how to update the manifest — is in
[REGENERATING.md](REGENERATING.md), and it is written for an agent driving a browser.

The registry is [MANIFEST.yaml](MANIFEST.yaml). If a fragment starts describing
something new, add the path there; a `depends_on` that is missing a path is the
one way this arrangement fails silently.
