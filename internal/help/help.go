// Package help holds the explanations for vocabularies whose meaning the
// ENGINE defines, as opposed to the ones an estate defines for itself.
//
// The split is the point of this package existing at all. Seven vocabularies
// -- asset kind, service kind, form factor, environment role, address role,
// data class, container engine -- are lookup tables with a `description`
// column, seeded by migration and editable, because they describe an estate's
// own conventions: one shop's `infra` is another's `platform`, and neither is
// wrong.
//
// The values here are different in kind. `quorum` does not mean whatever an
// operator writes next to it; it means what internal/impact does when a member
// of a quorum service goes away. A description you can edit for a behaviour
// you cannot is a lie waiting to happen, so these live in Go, ship with the
// binary, and change only when the code they describe changes.
//
// No external dependencies and no imports from the rest of the app: this is
// text about the domain, and the domain is the layer that must not acquire
// dependencies.
package help

// Term is one value and what it means.
type Term struct {
	Code  string
	Label string
	// Description says what the ENGINE does with this value, not what the
	// word means in general. "Losing one is survivable" is useful; "a group of
	// servers" is not.
	Description string
}

// Topic is a named set of values that belong together in one explanation.
type Topic struct {
	Key   string
	Title string
	// Intro frames the whole set. Several of these vocabularies only make
	// sense as a set -- availability is a choice between five answers to one
	// question -- and explaining the values without the question is half an
	// answer.
	Intro string
	Terms []Term
}

// Topics is every engine-defined vocabulary, in the order a person meets them:
// what a service promises, how it fails over, how it runs, what it depends on,
// where it listens, and the two that describe the estate around it.
var Topics = []Topic{
	{
		Key:   "availability",
		Title: "Availability policy",
		Intro: "How many instances a service needs before it is healthy. This is the single " +
			"most consequential field on a service: it is what turns 'two of three instances " +
			"are gone' into 'degraded' or 'down', and getting it wrong makes every impact " +
			"answer wrong in the same direction.",
		Terms: []Term{
			{"standalone", "Standalone", "One instance is the service. Losing it is an outage, and there is nothing to fail over to."},
			{"active_active", "Active/active", "Every instance serves at once. Losing one is survivable while at least min_healthy remain; below that the service is down."},
			{"active_passive", "Active/passive", "One instance serves and the others wait. Losing the active one is degraded rather than down when a standby exists — but see failover mode, because a standby nobody promotes is not a survivor."},
			{"quorum", "Quorum", "The cluster needs a majority to function — Raft, etcd, a three-node Vault. Losing one of three is fine; losing two is total, and the second loss is far worse than the first."},
			{"sharded", "Sharded", "Each instance owns a slice of the data. Losing one does not degrade the service evenly: it takes out that shard completely, so the verdict is 'at least one shard has no surviving replica' rather than a fraction."},
		},
	},
	{
		Key:   "failover_mode",
		Title: "Failover mode",
		Intro: "What has to happen for a standby to take over. It is the difference between " +
			"an outage that ends by itself and one that waits for a person.",
		Terms: []Term{
			{"auto", "Automatic", "The system promotes a standby on its own. Losing the primary is a blip."},
			{"manual", "Manual", "A human must promote. The service is degraded rather than down, and stays that way until somebody acts — which is why this is reported differently."},
			{"none", "None", "No failover exists. Whatever the topology suggests, losing the active instance is an outage."},
		},
	},
	{
		Key:   "nature",
		Title: "Dependency nature",
		Intro: "What happens to the consumer when the provider goes away. This is the edge's " +
			"failure semantics, and it is what the impact engine propagates along — the same " +
			"two services joined by a hard edge and an optional edge give completely " +
			"different answers.",
		Terms: []Term{
			{"hard", "Hard", "The consumer cannot work without it. The provider's outage becomes the consumer's outage."},
			{"soft", "Soft", "The consumer degrades but keeps serving — a cache it can miss, a feature it can disable."},
			{"startup", "Startup", "Needed to boot, not to run. A running consumer survives the provider's loss and will not come back after a restart, which is the quietest dangerous state in this whole model."},
			{"async", "Asynchronous", "Reached through a queue or a batch. The consumer keeps serving and work accumulates; the pain is delayed rather than absent."},
			{"optional", "Optional", "Genuinely optional. Recorded so the edge is visible, but it transmits no failure."},
		},
	},
	{
		Key:   "runtime_type",
		Title: "Runtime type",
		Intro: "How an instance is actually run on its host. It decides which runtime detail " +
			"table carries the specifics — a unit name, a container image, a workload — and " +
			"nothing else.",
		Terms: []Term{
			{"systemd", "systemd unit", "A Linux service unit. Carries unit name, restart policy and ordering."},
			{"windows_service", "Windows service", "A Windows service, with its run-as account and display name."},
			{"container", "Container", "A container on a host engine, with image, tag and restart policy. Not a Kubernetes workload."},
			{"k8s_workload", "Kubernetes workload", "Scheduled by Kubernetes rather than pinned to a host, and recorded against the node it runs on."},
			{"appliance", "Appliance", "A black box that serves something without a runtime anyone here manages — a hardware load balancer, a vendor box."},
		},
	},
	{
		Key:   "bind_scope",
		Title: "Endpoint bind scope",
		Intro: "Where a listening socket can be reached from. It is not decoration: the " +
			"impact engine exempts purely local binds from network reasoning entirely, " +
			"because a loopback socket cannot be cut off by a switch failure.",
		Terms: []Term{
			{"host", "Host", "Listening on every address of the host — 0.0.0.0. The ordinary case, and a fact rather than a missing value."},
			{"loopback", "Loopback", "127.0.0.1 only. Traffic never leaves the machine, so this makes no reachability claim at all."},
			{"unix", "Unix socket", "A filesystem socket. Same as loopback for reachability: intra-host by definition."},
			{"vip", "VIP", "A cluster address that moves between hosts. Reachable while somebody holds it."},
			{"cluster_ip", "Cluster IP", "A Kubernetes service address, reachable inside the cluster."},
			{"node_port", "Node port", "A port opened on every node of a cluster."},
			{"ingress", "Ingress", "Published through an ingress controller or edge proxy rather than directly."},
		},
	},
	{
		Key:   "plane",
		Title: "Network plane",
		Intro: "Which network a thing is attached to. Planes are evaluated separately and " +
			"only one of them decides service status — losing the management network makes " +
			"an estate unmanageable without making a single service unhealthy.",
		Terms: []Term{
			{"data", "Data", "Production traffic. This is the plane that decides whether a service is reachable."},
			{"mgmt", "Management", "Out-of-band access — consoles, IPMI, the OOB switch. Reaches everything by design, which is exactly why a path is never routed through it."},
			{"storage", "Storage", "A dedicated storage fabric, kept separate so that losing it is not confused with losing the data network."},
		},
	},
	{
		Key:   "certificate",
		Title: "Certificates",
		Intro: "What expires, what it covers, and where it is deployed. A certificate " +
			"lapses every ninety days where hardware support lapses every five years, " +
			"so this is usually the most urgent thing on the expiry report.",
		Terms: []Term{
			{Code: "covers", Label: "Covers", Description: "Every name the certificate is " +
				"valid for, the subject included. Searching by host resolves wildcards the " +
				"way a TLS client does: *.example.com covers orders.example.com, and does " +
				"NOT cover example.com or a.b.example.com. Anything looser would tell you " +
				"that you are covered while you are about to serve a name error."},
			{Code: "deployed", Label: "Deployed", Description: "Every place it is in use. One " +
				"wildcard on four load balancers is four outages on the day it expires, and " +
				"that count is the difference between a date and a decision."},
			{Code: "key_ref", Label: "Key reference", Description: "A PATH to the private key " +
				"— a vault reference, a file location — and never the key itself. This is an " +
				"inventory, not a secret store. The public certificate body is not held either: " +
				"a field that accepts certificate-shaped text is where a key eventually gets " +
				"pasted."},
			{Code: "no_expiry", Label: "No expiry recorded", Description: "Worse than a date in " +
				"the past. Every certificate has an expiry, so a blank means nobody looked " +
				"rather than that the question does not apply — which is why the report counts " +
				"these separately."},
		},
	},
	{
		Key:   "responsibility",
		Title: "Team and role",
		Intro: "Who looks after a thing, and in what capacity. Both are optional: a small " +
			"estate where everyone looks after everything gains nothing from filling them in. " +
			"Neither is a permission — this is who to call, not who the application will let " +
			"through.",
		Terms: []Term{
			{Code: "team", Label: "Team", Description: "The group answerable for it. Teams, never " +
				"people: a team survives the people in it, and a database kept forever with an " +
				"append-only audit trail should carry nothing anybody could ask to have erased. " +
				"A team's contact is a group address, a queue or a channel — never an individual."},
			{Code: "role", Label: "Managed as", Description: "In what capacity that team is " +
				"answerable: owner, operator, approver, on call, data custodian, vendor managed. " +
				"A role needs a team, because a capacity floating free of the group that holds it " +
				"means nothing. Add your own at Vocabularies."},
			{Code: "unrecorded", Label: "Nobody recorded", Description: "A real answer, and the " +
				"one most rows start with. It is shown plainly rather than as a blank, because a " +
				"gap somebody has not filled in and a gap nobody noticed look identical otherwise."},
		},
	},
	{
		Key:   "project_relation",
		Title: "Project relation",
		Intro: "How a project claims a thing. Two relations, and the asymmetry between " +
			"them is what lets this tool say whose estate a box belongs to — and, once " +
			"cost measurement lands, whose budget it lands on.",
		Terms: []Term{
			{"owns", "Owns", "The thing exists FOR this project. At most one project may own a given asset or service, so it is the anchor a cost model can attribute the whole of something to. Ownership also derives the footprint: what is inside an owned asset, and what runs on it, is this project's concern."},
			{"uses", "Uses", "The project depends on it but shares it with others. Any number of projects may use one thing, and nothing is derived from a `uses` link — what is inside somebody else's hypervisor is their footprint, not yours."},
		},
	},
	{
		Key:   "lifecycle",
		Title: "Lifecycle",
		Intro: "Whether a row describes something that exists. Nothing is ever deleted here — " +
			"retirement is a state, so the audit trail keeps its integrity and history stays " +
			"answerable.",
		Terms: []Term{
			{"planned", "Planned", "Recorded before it exists. Not part of the estate yet, and not counted in impact."},
			{"active", "Active", "In service."},
			{"maintenance", "Maintenance", "Deliberately out of service for work. Declared intent — not the same as something a monitor reports as down."},
			{"deprecated", "Deprecated", "Still running, should not be depended on further."},
			{"retired", "Retired", "Gone from the estate. Kept forever, excluded from every live view, and never deleted."},
		},
	},
}

// Lookup returns one topic by key.
func Lookup(key string) (Topic, bool) {
	for _, t := range Topics {
		if t.Key == key {
			return t, true
		}
	}
	return Topic{}, false
}
