package seed

import (
	"fmt"

	"github.com/gabriel/invctl/internal/domain"
	"github.com/gabriel/invctl/internal/store"
)

// ---------- services and placement ----------

type svcSpec struct {
	code, name, kind, env, availability string
	tier                                int
	minHealthy                          int
	failover                            string
	owner                               string
	rto, rpo                            int
}

type instSpec struct {
	service, host, runtime, role, shard string
	ordinal                             int
	unit                                string // systemd unit or container name
}

func (b *builder) services() {
	if !b.ok() {
		return
	}

	app, err := domain.NewApplication(store.NewID(), "orders", "Orders Platform", b.now)
	if err != nil {
		b.fail(fmt.Errorf("building application: %w", err))
		return
	}
	app.OwnerTeam = str("commerce")
	if err := b.store.CreateApplication(b.ctx, Actor, app); err != nil {
		b.fail(fmt.Errorf("seeding application: %w", err))
		return
	}

	specs := []svcSpec{
		// Three-node Raft cluster. Losing one node must report ok -- this is
		// the case that makes availability policy worth modelling at all.
		{"vault", "HashiCorp Vault", domain.SvcAuth, "prod", domain.AvailQuorum, 1, 0, "", "platform", 15, 0},

		// Patroni pair with manual promotion: losing the primary is degraded,
		// not down, because a human can promote the standby.
		{"pgsql-core", "PostgreSQL (core)", domain.SvcDB, "prod", domain.AvailActivePassive, 1, 0, domain.FailoverManual, "dba", 30, 5},

		// The edge proxy itself is healthy independently of its backends --
		// which is precisely why route health has to be derived from the pool.
		{"haproxy-edge", "HAProxy (edge)", domain.SvcProxy, "prod", domain.AvailActiveActive, 1, 1, "", "network", 10, 0},

		// Two backend services that both happen to live on vm-app-1. Nothing
		// about their individual definitions says that; only placement does.
		{"orders-api", "Orders API", domain.SvcAPI, "prod", domain.AvailActiveActive, 2, 1, "", "commerce", 30, 0},
		{"orders-web", "Orders Web", domain.SvcWeb, "prod", domain.AvailStandalone, 2, 0, "", "commerce", 60, 0},

		{"sso", "Keycloak SSO", domain.SvcAuth, "prod", domain.AvailStandalone, 1, 0, "", "platform", 30, 0},
		{"rabbitmq", "RabbitMQ", domain.SvcQueue, "prod", domain.AvailStandalone, 2, 0, "", "platform", 30, 15},

		// Sharded: each shard owns a slice of the series keyspace, so losing
		// every replica of one shard degrades the service without taking it
		// down.
		{"mimir-ingester", "Mimir Ingesters", domain.SvcMonitoring, "prod", domain.AvailSharded, 3, 0, "", "observability", 120, 60},

		// Consumes the proxy's route rather than any backend directly. This
		// is the service that proves route-as-node works.
		{"partner-gateway", "Partner Gateway", domain.SvcAPI, "prod", domain.AvailStandalone, 2, 0, "", "commerce", 60, 0},

		// Out of scope, on production hardware.
		{"orders-api-dev", "Orders API (dev)", domain.SvcAPI, "dev", domain.AvailStandalone, 4, 0, "", "developers", 0, 0},

		// A Windows service with a run-as account, to exercise rt_windows.
		{"backup-agent", "Veeam Backup Agent", domain.SvcAgent, "prod", domain.AvailStandalone, 3, 0, "", "platform", 240, 0},

		// Two shippers, one of which runs on a box with no production
		// cabling. Tier 4 and active/active so the pair is the natural shape
		// and losing one is survivable -- what is NOT survivable is not
		// knowing that one of them has never shipped a line.
		{"log-shipper", "Log Shipper", domain.SvcMonitoring, "prod", domain.AvailActiveActive, 4, 1, "", "observability", 120, 0},
	}

	for _, spec := range specs {
		if !b.ok() {
			return
		}
		def := domain.ServiceSpec{
			Code: spec.code, Name: spec.name, Kind: spec.kind,
			EnvironmentID: b.env(spec.env), Availability: spec.availability,
			Tier: spec.tier, ApplicationID: &app.ID,
		}
		if spec.minHealthy > 0 {
			def.MinHealthy = num(spec.minHealthy)
		}
		if spec.failover != "" {
			def.FailoverMode = str(spec.failover)
		}
		if spec.owner != "" {
			def.OwnerTeam = str(spec.owner)
		}
		if spec.rto > 0 {
			def.RTOMinutes = num(spec.rto)
		}
		if spec.rpo > 0 {
			def.RPOMinutes = num(spec.rpo)
		}
		svc, err := domain.NewService(store.NewID(), def, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building service %s: %w", spec.code, err))
			return
		}
		if err := b.store.CreateService(b.ctx, Actor, svc); err != nil {
			b.fail(fmt.Errorf("seeding service %s: %w", spec.code, err))
			return
		}
		b.refs.Services[spec.code] = svc.ID
	}

	instances := []instSpec{
		{"vault", "vm-vault-1", domain.RuntimeSystemd, "", "", 0, "vault.service"},
		{"vault", "vm-vault-2", domain.RuntimeSystemd, "", "", 0, "vault.service"},
		{"vault", "vm-vault-3", domain.RuntimeSystemd, "", "", 0, "vault.service"},

		{"pgsql-core", "vm-db-1", domain.RuntimeSystemd, domain.RolePrimary, "", 0, "patroni.service"},
		{"pgsql-core", "vm-db-2", domain.RuntimeSystemd, domain.RoleStandby, "", 0, "patroni.service"},

		{"haproxy-edge", "vm-proxy-1", domain.RuntimeSystemd, "", "", 0, "haproxy.service"},

		// Both replicas of orders-api on the same host: the placement mistake
		// this whole tool exists to surface.
		{"orders-api", "vm-app-1", domain.RuntimeContainer, "", "", 0, "orders-api-1"},
		{"orders-api", "vm-app-1", domain.RuntimeContainer, "", "", 1, "orders-api-2"},
		{"orders-web", "vm-app-1", domain.RuntimeContainer, "", "", 0, "orders-web-1"},

		{"sso", "vm-sso-1", domain.RuntimeSystemd, "", "", 0, "keycloak.service"},
		{"rabbitmq", "vm-queue-1", domain.RuntimeSystemd, "", "", 0, "rabbitmq-server.service"},

		{"mimir-ingester", "vm-k8s-1", domain.RuntimeK8sWorkload, "", "shard-a", 0, "mimir-ingester"},
		{"mimir-ingester", "vm-k8s-2", domain.RuntimeK8sWorkload, "", "shard-b", 0, "mimir-ingester"},

		{"partner-gateway", "vm-k8s-1", domain.RuntimeK8sWorkload, "", "", 0, "partner-gateway"},
		{"orders-api-dev", "vm-dev-1", domain.RuntimeContainer, "", "", 0, "orders-api-dev"},
		{"backup-agent", "vm-app-1", domain.RuntimeWindowsService, "", "", 0, "VeeamEndpointBackupSvc"},

		// The same agent directly on the hypervisor -- bare metal, no guest in
		// between. Estates have these (the box that runs the VMs needs backing
		// up too), and it is the one placement shape the rest of the fixture
		// lacked: on the neighbourhood diagram its line spans the service band
		// to the physical band with the whole virtual band in the way, which
		// is exactly the routed-channel case. A demo estate without one would
		// never show it.
		{"backup-agent", "hv-01", domain.RuntimeSystemd, "", "", 0, "veeamagent.service"},

		// The stranded pair. Both instances of the log shipper are declared;
		// only one of them can reach anything. The path view draws the other
		// as a box connected to nothing and names it, which is the finding.
		//
		// A service of its own rather than a second backup-agent: every
		// existing service's full-loss status is asserted somewhere in the
		// impact suite, and a surviving sibling on an unmodelled host changes
		// exactly that -- including hv-01's "6 services", which
		// reach_guard_test says is "derived, never adjusted".
		{"log-shipper", "vm-queue-1", domain.RuntimeSystemd, "", "", 0, "vector.service"},
		{"log-shipper", "srv-backup-proxy-1", domain.RuntimeSystemd, "", "", 1, "vector.service"},
	}

	for _, spec := range instances {
		if !b.ok() {
			return
		}
		serviceID, ok := b.refs.Services[spec.service]
		if !ok {
			b.fail(fmt.Errorf("seeding instance: unknown service %s", spec.service))
			return
		}
		hostID, ok := b.refs.Assets[spec.host]
		if !ok {
			b.fail(fmt.Errorf("seeding instance of %s: unknown host %s", spec.service, spec.host))
			return
		}
		si, err := domain.NewServiceInstance(store.NewID(), serviceID, hostID, spec.runtime, spec.ordinal, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building instance of %s: %w", spec.service, err))
			return
		}
		if spec.role != "" {
			si.Role = str(spec.role)
		}
		if spec.shard != "" {
			si.Shard = str(spec.shard)
		}
		// No observed health is seeded. A fixture that invents a reading has to
		// invent a reporter and a cadence to go with it (docs/AUDIT.md rules 5
		// and 8), and provenance fabricated in the demo data is the one thing
		// this milestone exists to prevent. The panels honestly show that
		// nothing has reported yet.
		if err := b.store.CreateInstance(b.ctx, Actor, si); err != nil {
			b.fail(fmt.Errorf("seeding instance of %s on %s: %w", spec.service, spec.host, err))
			return
		}
		b.runtimeDetail(si, spec)
	}
}

// runtimeDetail fills whichever rt_* table matches the instance's runtime.
func (b *builder) runtimeDetail(si *domain.ServiceInstance, spec instSpec) {
	if !b.ok() {
		return
	}
	switch spec.runtime {
	case domain.RuntimeSystemd:
		rt := &domain.RTSystemd{
			InstanceID: si.ID, UnitName: spec.unit, UnitType: str("simple"),
			RunAsUser: str(serviceUser(spec.service)), Restart: str("always"),
			UnitAfter:    `["network-online.target"]`,
			UnitRequires: `[]`,
			DropIns:      `{}`,
		}
		b.fail(b.store.SetRuntimeSystemd(b.ctx, Actor, rt))
	case domain.RuntimeContainer:
		rt := &domain.RTContainer{
			InstanceID: si.ID, Engine: str("docker"), ContainerName: str(spec.unit),
			ComposeProject: str(spec.service), ComposeService: str(spec.service),
			ImageRepo: str("registry.internal/" + spec.service), ImageTag: str("1.4.2"),
			RestartPolicy: str("unless-stopped"), NetworkMode: str("bridge"),
		}
		b.fail(b.store.SetRuntimeContainer(b.ctx, Actor, rt))
	case domain.RuntimeK8sWorkload:
		rt := &domain.RTK8s{
			InstanceID: si.ID, Namespace: str("observability"),
			WorkloadKind: str("StatefulSet"), WorkloadName: str(spec.unit),
			ReplicasDesired: num(1), ServiceAccount: str(spec.service),
		}
		if clusterID, ok := b.refs.Assets[spec.host]; ok {
			rt.ClusterAssetID = &clusterID
		}
		b.fail(b.store.SetRuntimeK8s(b.ctx, Actor, rt))
	case domain.RuntimeWindowsService:
		rt := &domain.RTWindows{
			InstanceID: si.ID, ServiceName: spec.unit,
			DisplayName: str("Veeam Endpoint Backup Service"),
			BinaryPath:  str(`C:\Program Files\Veeam\Endpoint Backup\Veeam.EndPoint.Service.exe`),
			StartType:   str("auto"), DependsOnSvc: `["RpcSs"]`,
			RecoveryAction: str("restart"),
		}
		// The run-as account is the usual reason a Windows service dies after
		// a credential rotation, so it is a real foreign key.
		if id, ok := b.identityIDs["svc-backup$"]; ok {
			rt.LogonIdentityID = &id
		}
		b.fail(b.store.SetRuntimeWindows(b.ctx, Actor, rt))
	}
}

func serviceUser(code string) string {
	switch code {
	case "pgsql-core":
		return "postgres"
	case "haproxy-edge":
		return "haproxy"
	default:
		return code
	}
}

// ---------- endpoints ----------

func (b *builder) endpoints() {
	specs := []struct {
		service, name, proto string
		port                 int
		bindScope, exposure  string
		tls                  string
		l7                   string
	}{
		{"vault", "api", domain.ProtoTCP, 8200, domain.BindVIP, "environment", "tls", "http"},
		{"pgsql-core", "sql", domain.ProtoTCP, 5432, domain.BindVIP, "environment", "tls", "postgres"},
		{"haproxy-edge", "https", domain.ProtoTCP, 443, domain.BindVIP, "external", "tls", "http"},
		{"orders-api", "http", domain.ProtoTCP, 8080, domain.BindHost, "internal", "none", "http"},
		{"orders-web", "http", domain.ProtoTCP, 8081, domain.BindHost, "internal", "none", "http"},
		{"sso", "https", domain.ProtoTCP, 8443, domain.BindHost, "environment", "tls", "http"},
		{"rabbitmq", "amqp", domain.ProtoTCP, 5672, domain.BindHost, "environment", "none", "amqp"},
		{"mimir-ingester", "grpc", domain.ProtoTCP, 9095, domain.BindClusterIP, "environment", "none", "grpc"},
		{"partner-gateway", "https", domain.ProtoTCP, 8443, domain.BindClusterIP, "cross_env", "mtls", "http"},
		{"orders-api-dev", "http", domain.ProtoTCP, 8080, domain.BindHost, "internal", "none", "http"},
	}

	for _, spec := range specs {
		if !b.ok() {
			return
		}
		serviceID, ok := b.refs.Services[spec.service]
		if !ok {
			b.fail(fmt.Errorf("seeding endpoint: unknown service %s", spec.service))
			return
		}
		port := spec.port
		e, err := domain.NewEndpoint(store.NewID(), serviceID, spec.name, spec.proto, &port, spec.bindScope)
		if err != nil {
			b.fail(fmt.Errorf("building endpoint %s/%s: %w", spec.service, spec.name, err))
			return
		}
		e.Exposure = spec.exposure
		e.TLSMode = spec.tls
		e.L7Proto = str(spec.l7)
		if spec.tls != "none" {
			// An opaque reference to whatever issues certificates -- the POC
			// does not model certificates as an entity.
			e.CertificateID = str("vault-pki/" + spec.service)
		}
		if err := b.store.CreateEndpoint(b.ctx, Actor, e); err != nil {
			b.fail(fmt.Errorf("seeding endpoint %s/%s: %w", spec.service, spec.name, err))
			return
		}
		b.refs.Endpoints[spec.service+"/"+spec.name] = e.ID
	}

	// A unix socket, to prove the port/path exclusivity constraint is real.
	if b.ok() {
		serviceID := b.refs.Services["pgsql-core"]
		e := &domain.Endpoint{
			ID: store.NewID(), ServiceID: serviceID, Name: "local",
			L4Proto: domain.ProtoUnix, UnixPath: str("/var/run/postgresql/.s.PGSQL.5432"),
			BindScope: domain.BindUnix, TLSMode: "none", Exposure: "internal",
		}
		if err := b.store.CreateEndpoint(b.ctx, Actor, e); err != nil {
			b.fail(fmt.Errorf("seeding unix endpoint: %w", err))
			return
		}
		b.refs.Endpoints["pgsql-core/local"] = e.ID
	}
}

// ---------- routing ----------

func (b *builder) routing() {
	if !b.ok() {
		return
	}
	// One pool, two members -- and both members' instances live on vm-app-1.
	// Neither the pool nor the route says so; only placement does, which is
	// exactly the finding the impact engine has to produce.
	pool := &domain.BackendPool{
		ID: store.NewID(), ServiceID: b.refs.Services["haproxy-edge"],
		Name: "orders-pool", LBAlgorithm: str("roundrobin"),
	}
	if err := b.store.CreateBackendPool(b.ctx, Actor, pool); err != nil {
		b.fail(fmt.Errorf("seeding backend pool: %w", err))
		return
	}
	b.poolIDs["orders-pool"] = pool.ID

	for _, endpointKey := range []string{"orders-api/http", "orders-web/http"} {
		endpointID, ok := b.refs.Endpoints[endpointKey]
		if !ok {
			b.fail(fmt.Errorf("seeding pool member: unknown endpoint %s", endpointKey))
			return
		}
		member := &domain.BackendMember{PoolID: pool.ID, EndpointID: endpointID, Weight: 1}
		if err := b.store.AddBackendMember(b.ctx, Actor, member); err != nil {
			b.fail(fmt.Errorf("seeding pool member %s: %w", endpointKey, err))
			return
		}
	}

	route, err := domain.NewRoute(store.NewID(), b.refs.Endpoints["haproxy-edge/https"],
		"host_header", pool.ID)
	if err != nil {
		b.fail(fmt.Errorf("building route: %w", err))
		return
	}
	route.MatchValue = str("orders.example.com")
	route.TLSTermination = str("terminate")
	route.Priority = 10
	if err := b.store.CreateRoute(b.ctx, Actor, route); err != nil {
		b.fail(fmt.Errorf("seeding route: %w", err))
		return
	}
	b.refs.Routes["orders.example.com"] = route.ID
}

// ---------- dependencies ----------

func (b *builder) dependencies() {
	type depSpec struct {
		consumer     string
		endpoint     string // "service/endpoint", or empty when routing
		route        string // route match value
		nature       string
		tolerance    int
		failureMode  string
		identity     string
		authMethod   string
		firewallRule string
		dataClasses  []string
	}

	specs := []depSpec{
		// Hard: no database, no orders.
		{consumer: "orders-api", endpoint: "pgsql-core/sql", nature: domain.NatureHard,
			failureMode: "Order writes fail immediately; the API returns 503",
			identity:    "svc-orders", authMethod: "scram-sha-256",
			firewallRule: "PROD-APP-TO-DB-5432", dataClasses: []string{"pii", "chd"}},

		// Soft: sessions already issued keep working, new logins do not.
		{consumer: "orders-api", endpoint: "sso/https", nature: domain.NatureSoft,
			failureMode: "Existing sessions keep working; new sign-ins fail",
			authMethod:  "oidc", firewallRule: "PROD-APP-TO-SSO-8443",
			dataClasses: []string{"pii", "credential"}},

		// Async with a 300-second tolerance: a 3-minute reboot is invisible,
		// a 45-minute one is not. This is the edge that makes WindowSeconds
		// mean something.
		{consumer: "orders-api", endpoint: "rabbitmq/amqp", nature: domain.NatureAsync,
			tolerance:   300,
			failureMode: "Order events buffer locally for 5 minutes, then are dropped",
			authMethod:  "plain", firewallRule: "PROD-APP-TO-MQ-5672",
			dataClasses: []string{"pii"}},

		// Optional: losing metrics is not an outage, and reporting it as one
		// trains people to ignore the report.
		{consumer: "orders-api", endpoint: "mimir-ingester/grpc", nature: domain.NatureOptional,
			failureMode: "Metrics are not scraped; no user-visible effect",
			dataClasses: []string{"telemetry"}},

		// Startup: the landmine. SSO is serving right now with Vault down,
		// and will fail to start the next time anything restarts it.
		{consumer: "sso", endpoint: "vault/api", nature: domain.NatureStartup,
			failureMode: "Reads its database credential and signing keys at boot; will not start without Vault",
			identity:    "svc-sso", authMethod: "approle",
			firewallRule: "PROD-SSO-TO-VAULT-8200", dataClasses: []string{"credential"}},

		// ...and the other half of the cycle: humans log in to Vault through
		// SSO. app -> auth -> app, exactly the shape §6 warns about.
		{consumer: "vault", endpoint: "sso/https", nature: domain.NatureSoft,
			failureMode: "Human OIDC login unavailable; token and AppRole auth unaffected",
			authMethod:  "oidc", dataClasses: []string{"credential"}},

		{consumer: "sso", endpoint: "pgsql-core/sql", nature: domain.NatureHard,
			failureMode: "Realm and session storage unavailable; SSO stops serving",
			identity:    "svc-sso", authMethod: "scram-sha-256",
			firewallRule: "PROD-SSO-TO-DB-5432", dataClasses: []string{"pii", "credential"}},

		{consumer: "orders-web", endpoint: "orders-api/http", nature: domain.NatureHard,
			failureMode: "The storefront renders an error page",
			dataClasses: []string{"pii"}},

		// The startup landmine, reachable by losing one host. The storefront
		// reads the OIDC discovery document and signing keys once at boot and
		// caches them, so it keeps serving with SSO down and fails to start
		// the next time anything restarts it.
		{consumer: "orders-web", endpoint: "sso/https", nature: domain.NatureStartup,
			failureMode: "Reads OIDC discovery and signing keys at boot; will not start without SSO",
			authMethod:  "oidc", firewallRule: "PROD-WEB-TO-SSO-8443",
			dataClasses: []string{"credential"}},

		// The route-as-node case: the partner gateway depends on the routed
		// hostname, not on any individual backend. HAProxy can be perfectly
		// healthy while this dependency is unsatisfiable.
		{consumer: "partner-gateway", route: "orders.example.com", nature: domain.NatureHard,
			failureMode: "Partner order submission fails with a gateway error",
			authMethod:  "mtls", firewallRule: "TRANSIT-PARTNER-TO-EDGE-443",
			dataClasses: []string{"pii", "chd"}},

		// Patroni stores its cluster state in Vault's KV. Hard, and it is
		// what makes losing all of Vault interesting.
		{consumer: "pgsql-core", endpoint: "vault/api", nature: domain.NatureStartup,
			failureMode: "Patroni reads TLS material and replication credentials at boot",
			identity:    "svc-orders", authMethod: "approle",
			dataClasses: []string{"credential"}},

		{consumer: "backup-agent", endpoint: "pgsql-core/sql", nature: domain.NatureOptional,
			failureMode: "Backup job fails and retries on the next window",
			dataClasses: []string{"pii"}},
	}

	for _, spec := range specs {
		if !b.ok() {
			return
		}
		consumerID, ok := b.refs.Services[spec.consumer]
		if !ok {
			b.fail(fmt.Errorf("seeding dependency: unknown consumer %s", spec.consumer))
			return
		}

		var endpointID, routeID *string
		if spec.endpoint != "" {
			id, ok := b.refs.Endpoints[spec.endpoint]
			if !ok {
				b.fail(fmt.Errorf("seeding dependency of %s: unknown endpoint %s", spec.consumer, spec.endpoint))
				return
			}
			endpointID = &id
		}
		if spec.route != "" {
			id, ok := b.refs.Routes[spec.route]
			if !ok {
				b.fail(fmt.Errorf("seeding dependency of %s: unknown route %s", spec.consumer, spec.route))
				return
			}
			routeID = &id
		}

		def := domain.DependencySpec{
			ConsumerServiceID:  consumerID,
			ProviderEndpointID: endpointID,
			ProviderRouteID:    routeID,
			Nature:             spec.nature,
			FailureMode:        spec.failureMode,
		}
		if spec.tolerance > 0 {
			def.ToleranceSeconds = num(spec.tolerance)
		}
		if spec.authMethod != "" {
			def.AuthMethod = str(spec.authMethod)
		}
		if spec.firewallRule != "" {
			def.FirewallRuleRef = str(spec.firewallRule)
		}
		if spec.identity != "" {
			if id, ok := b.identityIDs[spec.identity]; ok {
				def.IdentityID = &id
			}
		}
		d, err := domain.NewDependency(store.NewID(), def, b.now)
		if err != nil {
			b.fail(fmt.Errorf("building dependency %s -> %s%s: %w",
				spec.consumer, spec.endpoint, spec.route, err))
			return
		}
		if err := b.store.CreateDependency(b.ctx, Actor, d, spec.dataClasses); err != nil {
			b.fail(fmt.Errorf("seeding dependency %s -> %s%s: %w",
				spec.consumer, spec.endpoint, spec.route, err))
			return
		}
	}
}
