package domain

import "time"

// L4 protocols.
const (
	ProtoTCP  = "tcp"
	ProtoUDP  = "udp"
	ProtoSCTP = "sctp"
	ProtoUnix = "unix"
)

// L4Protos is the Go side of the endpoint.l4_proto CHECK constraint.
var L4Protos = []string{ProtoTCP, ProtoUDP, ProtoSCTP, ProtoUnix}

// Bind scopes describe how reachable a listening socket is. This is what makes
// "who can actually talk to this?" answerable without reading firewall rules.
const (
	BindLoopback  = "loopback"
	BindHost      = "host"
	BindVIP       = "vip"
	BindClusterIP = "cluster_ip"
	BindNodePort  = "node_port"
	BindIngress   = "ingress"
	BindUnix      = "unix"
)

// BindScopes is the Go side of the endpoint.bind_scope CHECK constraint.
var BindScopes = []string{
	BindLoopback, BindHost, BindVIP, BindClusterIP, BindNodePort, BindIngress, BindUnix,
}

// TLSModes is the Go side of the endpoint.tls_mode CHECK constraint.
var TLSModes = []string{"none", "tls", "mtls", "starttls"}

// Exposures is the Go side of the endpoint.exposure CHECK constraint.
// cross_env is the interesting value: it marks traffic that must traverse a
// segmentation boundary and therefore needs a transit path and a firewall rule.
var Exposures = []string{"internal", "environment", "cross_env", "external"}

// Endpoint is a listening socket belonging to a service.
type Endpoint struct {
	ID            string  `db:"id"`
	ServiceID     string  `db:"service_id"`
	Name          string  `db:"name"`
	L4Proto       string  `db:"l4_proto"`
	Port          *int    `db:"port"`
	UnixPath      *string `db:"unix_path"`
	BindScope     string  `db:"bind_scope"`
	IPAddressID   *string `db:"ip_address_id"`
	L7Proto       *string `db:"l7_proto"`
	TLSMode       string  `db:"tls_mode"`
	CertificateID *string `db:"certificate_id"`
	Exposure      string  `db:"exposure"`
}

// NewEndpoint validates and constructs a listening socket.
//
// The port/unix_path exclusivity mirrors the table CHECK: a unix socket has a
// path and no port, everything else has a port and no path.
func NewEndpoint(id, serviceID, name, proto string, port *int, bindScope string) (*Endpoint, error) {
	e := &Endpoint{
		ID: id, ServiceID: serviceID, Name: name, L4Proto: proto,
		Port: port, BindScope: bindScope, TLSMode: "none", Exposure: "internal",
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	return e, nil
}

// Validate checks an endpoint against its business rules.
func (e *Endpoint) Validate() error {
	ve := &ValidationError{}
	e.Name = checkRequired(ve, "name", e.Name)
	checkRequired(ve, "service_id", e.ServiceID)
	checkEnum(ve, "l4_proto", e.L4Proto, L4Protos)
	checkEnum(ve, "bind_scope", e.BindScope, BindScopes)
	checkEnum(ve, "tls_mode", e.TLSMode, TLSModes)
	checkEnum(ve, "exposure", e.Exposure, Exposures)

	if e.L4Proto == ProtoUnix {
		if e.UnixPath == nil || *e.UnixPath == "" {
			ve.Add("unix_path", "is required for a unix socket")
		}
		if e.Port != nil {
			ve.Add("port", "must be empty for a unix socket")
		}
	} else {
		if e.Port == nil {
			ve.Add("port", "is required for %s", e.L4Proto)
		} else if *e.Port < 1 || *e.Port > 65535 {
			ve.Add("port", "must be between 1 and 65535")
		}
		if e.UnixPath != nil && *e.UnixPath != "" {
			ve.Add("unix_path", "must be empty for %s", e.L4Proto)
		}
	}
	return ve.OrNil()
}

// Addr renders the endpoint for display.
func (e *Endpoint) Addr() string {
	if e.L4Proto == ProtoUnix && e.UnixPath != nil {
		return *e.UnixPath
	}
	if e.Port == nil {
		return e.L4Proto
	}
	return e.L4Proto + "/" + itoa(*e.Port)
}

// BackendPool is a set of endpoints behind a proxy.
type BackendPool struct {
	ID          string  `db:"id"`
	ServiceID   string  `db:"service_id"`
	Name        string  `db:"name"`
	LBAlgorithm *string `db:"lb_algorithm"`
}

// BackendMember places an endpoint into a pool.
type BackendMember struct {
	PoolID     string `db:"pool_id"`
	EndpointID string `db:"endpoint_id"`
	Weight     int    `db:"weight"`
	IsBackup   bool   `db:"is_backup"`
}

// MatchTypes is the Go side of the route.match_type CHECK constraint.
var MatchTypes = []string{"sni", "host_header", "path_prefix", "default"}

// TLSTerminations is the Go side of the route.tls_termination CHECK constraint.
var TLSTerminations = []string{"passthrough", "terminate", "reencrypt"}

// Route is an L7 matching rule mapping a frontend endpoint to a backend pool.
//
// Routes are nodes in the dependency graph, not passthroughs (HANDOVER §6): a
// dependency on a route resolves to its pool, and the pool's health derives
// from its members. That is what surfaces "the proxy is up but every backend
// sits on the node you are about to reboot".
type Route struct {
	ID                 string  `db:"id"`
	FrontendEndpointID string  `db:"frontend_endpoint_id"`
	MatchType          string  `db:"match_type"`
	MatchValue         *string `db:"match_value"`
	BackendPoolID      string  `db:"backend_pool_id"`
	TLSTermination     *string `db:"tls_termination"`
	Priority           int     `db:"priority"`
}

// NewRoute validates and constructs a routing rule.
func NewRoute(id, frontendEndpointID, matchType, backendPoolID string) (*Route, error) {
	ve := &ValidationError{}
	checkRequired(ve, "frontend_endpoint_id", frontendEndpointID)
	checkRequired(ve, "backend_pool_id", backendPoolID)
	checkEnum(ve, "match_type", matchType, MatchTypes)
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return &Route{
		ID: id, FrontendEndpointID: frontendEndpointID,
		MatchType: matchType, BackendPoolID: backendPoolID, Priority: 100,
	}, nil
}

// Identity kinds.
const (
	IdentityServiceAccount = "service_account"
	IdentityMachineAccount = "machine_account"
	IdentityAPIToken       = "api_token"
	IdentityCertSubject    = "cert_subject"
	IdentityHuman          = "human"
)

// IdentityKinds is the Go side of the identity.kind CHECK constraint.
var IdentityKinds = []string{
	IdentityServiceAccount, IdentityMachineAccount, IdentityAPIToken,
	IdentityCertSubject, IdentityHuman,
}

// Identity is a principal used to authenticate a dependency.
//
// SecretRef holds a *path* (a Vault path or similar), never a secret value.
// If a code path would put an actual secret here, that is a bug to raise, not
// to work around.
type Identity struct {
	ID           string  `db:"id"`
	Kind         string  `db:"kind"`
	Name         string  `db:"name"`
	Realm        *string `db:"realm"`
	SecretRef    *string `db:"secret_ref"`
	RotationDays *int    `db:"rotation_days"`
	LastRotated  *string `db:"last_rotated"`
	OwnerTeam    *string `db:"owner_team"`
	Lifecycle    string  `db:"lifecycle"`
}

// NewIdentity validates and constructs a principal.
func NewIdentity(id, kind, name string) (*Identity, error) {
	ve := &ValidationError{}
	name = checkRequired(ve, "name", name)
	checkEnum(ve, "kind", kind, IdentityKinds)
	if err := ve.OrNil(); err != nil {
		return nil, err
	}
	return &Identity{ID: id, Kind: kind, Name: name, Lifecycle: LifecycleActive}, nil
}

// RotationOverdue reports whether the credential is past its rotation window.
// A nil rotation policy is not overdue — it is simply unmanaged.
func (i *Identity) RotationOverdue(now time.Time) bool {
	if i.RotationDays == nil || i.LastRotated == nil {
		return false
	}
	last, err := ParseTime(*i.LastRotated)
	if err != nil {
		return false
	}
	return now.UTC().After(last.AddDate(0, 0, *i.RotationDays))
}

// itoa avoids pulling strconv into every call site for small positive ints.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
