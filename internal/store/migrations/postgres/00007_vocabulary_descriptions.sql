-- invctl — infrastructure inventory
-- Copyright (C) 2026 Madalin Ignisca <hi@madalin.me>
--
-- Licensed under the GNU Affero General Public License, version 3 only —
-- no later version applies. See LICENSE for the full text.
--
-- SPDX-License-Identifier: AGPL-3.0-only

-- Dialect-split despite being byte-identical in both directories, and that is
-- not an oversight. The seven lookup tables are created by the DIALECT
-- migration 00004, and Migrate applies every shared migration before any
-- dialect one -- so a shared migration touching these tables runs before they
-- exist. Measured, not assumed: as shared/00010 this failed with "no such
-- table: asset_kind". Placement follows the dependency, not the SQL.
--
-- Every vocabulary term gets a sentence saying what it means.
--
-- The reason is a real conversation rather than a tidiness urge. Asked to
-- explain the twelve service kinds, four of them turned out to be genuinely
-- ambiguous to an experienced systems administrator:
--
--   * `proxy` covered both a forward proxy (Squid) and a reverse proxy or L7
--     load balancer (HAProxy), which are opposite directions of traffic;
--   * `auth` covered both an identity provider (Keycloak) and a secret manager
--     (Vault), which are different products solving different problems;
--   * `storage` did not say whether it meant a SAN or an S3-compatible
--     service -- made worse by asset_kind ALSO having a `storage`;
--   * `web` was clear, which is how we know the others were not.
--
-- A label of one or two words cannot carry that. The next person to open a
-- dropdown has exactly the question this column answers, and answering it in a
-- README nobody has open at the time is answering it nowhere.
--
-- Portable as written: ADD COLUMN with a constant default is supported by both
-- engines and needs no table rebuild, so this is additive and safe over data.
--
-- The text is DATA, not code. It is seeded here so a fresh install is useful
-- immediately, and an operator may rewrite any of it -- these are descriptions
-- of an estate's own conventions, and one shop's `infra` is another's `platform`.
-- Enums whose meaning the ENGINE defines (availability, failover_mode,
-- runtime_type, dependency nature) are deliberately NOT here: their help text
-- lives in Go, because a description you can edit for a behaviour you cannot is
-- a lie waiting to happen.

-- +goose Up
ALTER TABLE asset_kind            ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE service_kind          ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE interface_form_factor ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE environment_role      ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE ip_address_role       ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE data_class            ADD COLUMN description TEXT NOT NULL DEFAULT '';
ALTER TABLE container_engine      ADD COLUMN description TEXT NOT NULL DEFAULT '';

-- Asset kinds. The two behaviour columns are named where they matter, because
-- "why can I not put a service on a rack" is the question they answer.
UPDATE asset_kind SET description = 'A building or data centre. Contains racks; hosts nothing itself.' WHERE code = 'site';
UPDATE asset_kind SET description = 'A physical rack. Losing it takes everything mounted in it, which is why power and cooling faults are modelled here rather than on each box.' WHERE code = 'rack';
UPDATE asset_kind SET description = 'Power distribution. Nothing is contained by it, so simulating a PDU failure means selecting the rack it feeds, not the PDU alone.' WHERE code = 'pdu';
UPDATE asset_kind SET description = 'A firewall or security gateway. Forwards traffic, so it can be a member of a forwarder group; can also run services of its own.' WHERE code = 'firewall';
UPDATE asset_kind SET description = 'A network switch. The usual member of a forwarder group — an MC-LAG pair is two of these in one group.' WHERE code = 'switch';
UPDATE asset_kind SET description = 'Passive patch panel. Present for cable documentation; carries no configuration and hosts nothing.' WHERE code = 'patch_panel';
UPDATE asset_kind SET description = 'Physical server. Can host service instances and can attach to a network directly.' WHERE code = 'server';
UPDATE asset_kind SET description = 'Virtualisation host. Its guests inherit its network attachment through containment, which is why a VM needs no attachment row of its own.' WHERE code = 'hypervisor';
UPDATE asset_kind SET description = 'A group of machines addressed as one — a Kubernetes control plane, a database cluster. Use it when the members are interchangeable to whatever consumes them.' WHERE code = 'cluster';
UPDATE asset_kind SET description = 'A virtual machine. Runs on a hypervisor and reaches the network through that host''s bridge.' WHERE code = 'vm';
UPDATE asset_kind SET description = 'A Kubernetes worker. Distinct from a plain VM so that workload placement reads clearly on the diagram.' WHERE code = 'k8s_node';
UPDATE asset_kind SET description = 'A Linux bridge, an OVS bridge or a vSwitch. An asset in its own right because a guest reaches the network THROUGH it: it is what a veth lands on and what carries the uplink to the physical port, and losing it cuts off every guest attached to it.' WHERE code = 'bridge';
UPDATE asset_kind SET description = 'Storage HARDWARE — a SAN, a NAS head, a disk array. The box, not the service. An S3-compatible service running on it is a separate service row with kind `storage`.' WHERE code = 'storage';

-- Service kinds, including the three the conversation showed were unclear.
UPDATE service_kind SET description = 'A persistent stateful store — PostgreSQL, MySQL, MongoDB. The thing that holds the data, and usually the thing with the strictest recovery objectives.' WHERE code = 'db';
UPDATE service_kind SET description = 'A volatile store — Redis, Memcached, Valkey. Losing it costs latency and load, not data. If losing it loses data, it is a `db`.' WHERE code = 'cache';
UPDATE service_kind SET description = 'A message broker or event bus — RabbitMQ, Kafka, NATS. Work in flight lives here, so an outage is felt as delay before it is felt as failure.' WHERE code = 'queue';
UPDATE service_kind SET description = 'A browser-facing application: the thing a person opens in a tab. A web SERVER that only forwards is a `proxy` or an `lb`.' WHERE code = 'web';
UPDATE service_kind SET description = 'A machine-facing interface — REST, gRPC, GraphQL. Consumed by other software rather than by a person.' WHERE code = 'api';
UPDATE service_kind SET description = 'A FORWARD proxy: outbound traffic on behalf of clients — Squid, an egress filter, a corporate web gateway. For an inbound reverse proxy or load balancer use `lb`.' WHERE code = 'proxy';
UPDATE service_kind SET description = 'Identity: who someone is and what they may do — Keycloak, an LDAP directory, an OIDC provider. For a secret store use `secrets`.' WHERE code = 'auth';
UPDATE service_kind SET description = 'Scheduled or triggered work rather than a continuously serving process — ETL, nightly reports, cron-driven jobs. Health means "did the last run succeed", not "is it listening".' WHERE code = 'batch';
UPDATE service_kind SET description = 'A per-host resident process — a backup agent, a log shipper, a metrics exporter. One instance per machine, and each instance is about the machine it sits on rather than interchangeable with the others.' WHERE code = 'agent';
UPDATE service_kind SET description = 'A storage SERVICE consumed over the network — MinIO, Ceph RGW, an NFS export. The array it runs on is an asset with kind `storage`; this is the thing that serves it.' WHERE code = 'storage';
UPDATE service_kind SET description = 'Plumbing the estate needs but nobody calls directly — DNS, NTP, DHCP, PKI. Often depended on by everything and named by nothing.' WHERE code = 'infra';
UPDATE service_kind SET description = 'Observability — metrics, logs, traces, alerting. Worth recording as a service because the thing that watches everything else can itself be down, and nothing will say so.' WHERE code = 'monitoring';

-- Interface form factors: what a port physically is.
UPDATE interface_form_factor SET description = 'Copper Ethernet, 8P8C. Management ports and low-speed access.' WHERE code = 'rj45';
UPDATE interface_form_factor SET description = 'Small form-factor pluggable, 1G.' WHERE code = 'sfp';
UPDATE interface_form_factor SET description = 'SFP+, 10G.' WHERE code = 'sfp+';
UPDATE interface_form_factor SET description = 'SFP28, 25G. Common host uplink.' WHERE code = 'sfp28';
UPDATE interface_form_factor SET description = 'QSFP+, 40G.' WHERE code = 'qsfp+';
UPDATE interface_form_factor SET description = 'QSFP28, 100G. Typical switch-to-switch and MC-LAG peer link.' WHERE code = 'qsfp28';
UPDATE interface_form_factor SET description = 'A virtual port — a veth, a bridge port, a tap. Exists in software on the host, and is what makes a guest''s path down to a physical cable drawable.' WHERE code = 'virtual';
UPDATE interface_form_factor SET description = 'A link aggregation group — a bond or port-channel. Not a socket: it is the logical port its members are enslaved to, and it is what a bridge lands on rather than one of the cables.' WHERE code = 'lag';
UPDATE interface_form_factor SET description = 'The host''s own loopback. A service bound here never leaves the machine, which is why such a binding makes no reachability claim.' WHERE code = 'loopback';

-- Environment roles. `is_transit` is behaviour, so it is described as such.
UPDATE environment_role SET description = 'Live service to real users. In audit scope by default.' WHERE code = 'production';
UPDATE environment_role SET description = 'A rehearsal of production, usually with production-shaped data. In scope when that data is real.' WHERE code = 'staging';
UPDATE environment_role SET description = 'Where people build and break things. Deliberately out of audit scope, which is exactly why an asset spanning dev and production is a finding.' WHERE code = 'dev';
UPDATE environment_role SET description = 'A brokering zone — DMZ, edge, interconnect. Marked as transit, which means the span report does not treat it as a boundary violation: carrying traffic between two environments is its whole job.' WHERE code = 'transit';
UPDATE environment_role SET description = 'Infrastructure used by several environments at once — shared monitoring, shared directory. Not transit: it is a place things live, not a place traffic passes through.' WHERE code = 'shared';
UPDATE environment_role SET description = 'Standby capacity for failover. Normally idle, and worth recording precisely because "we have DR" and "DR is cabled and current" are different claims.' WHERE code = 'dr';

-- IP address roles.
UPDATE ip_address_role SET description = 'The interface''s own primary address.' WHERE code = 'primary';
UPDATE ip_address_role SET description = 'An additional address on the same interface.' WHERE code = 'secondary';
UPDATE ip_address_role SET description = 'A service address shared by a cluster and answered by whichever member currently owns it. "Who has the VIP" is an incident question.' WHERE code = 'vip';
UPDATE ip_address_role SET description = 'Out-of-band management address. Reachable when the data plane is not, which is the point of it — and why a path is never routed through management cabling.' WHERE code = 'mgmt';
UPDATE ip_address_role SET description = 'An address that moves between hosts on failover, carried rather than owned. Distinct from a VIP in that it follows a resource, not a cluster.' WHERE code = 'floating';

-- Data classes. These decide audit scope, so the wording is careful. They
-- describe what an EDGE carries, not what a service stores: the same database
-- reached by two different consumers can be in scope for one and not the other.
UPDATE data_class SET description = 'Cardholder data under PCI DSS — the PAN and what travels with it. Every hop carrying it is in assessment scope.' WHERE code = 'chd';
UPDATE data_class SET description = 'Sensitive authentication data under PCI DSS — full track data, CAV2/CVC2/CVV2/CID, PINs. Must never be stored after authorisation, so an edge carrying it is worth knowing about.' WHERE code = 'sad';
UPDATE data_class SET description = 'Personal data under GDPR. Recorded on the edge rather than the service because it is the flow that puts both ends in scope.' WHERE code = 'pii';
UPDATE data_class SET description = 'Credentials, keys or tokens in transit. Never the values themselves — this says what an edge carries, and the database stores no secret.' WHERE code = 'credential';
UPDATE data_class SET description = 'Metrics, logs and traces. Usually low sensitivity, and usually the thing that turns out to contain personal data nobody expected.' WHERE code = 'telemetry';
UPDATE data_class SET description = 'Configuration and operational metadata. Not personal, but often enough to map an estate.' WHERE code = 'config';
UPDATE data_class SET description = 'Explicitly nothing sensitive. Recorded deliberately so that "nobody classified this edge" and "somebody classified it as harmless" stay distinguishable.' WHERE code = 'none';

-- Container engines.
UPDATE container_engine SET description = 'Docker Engine.' WHERE code = 'docker';
UPDATE container_engine SET description = 'Podman, usually rootless.' WHERE code = 'podman';

-- +goose Down
ALTER TABLE asset_kind            DROP COLUMN description;
ALTER TABLE service_kind          DROP COLUMN description;
ALTER TABLE interface_form_factor DROP COLUMN description;
ALTER TABLE environment_role      DROP COLUMN description;
ALTER TABLE ip_address_role       DROP COLUMN description;
ALTER TABLE data_class            DROP COLUMN description;
ALTER TABLE container_engine      DROP COLUMN description;
