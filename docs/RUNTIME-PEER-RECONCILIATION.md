# Runtime peer reconciliation and DDNS endpoint supervision

Status: implementation contract and release-readiness specification. Last
reviewed 22 August 2026.

## 1. Motivation

A long-running wg-quic interface may carry several independent peers. Before
runtime reconciliation, adding, removing, or changing one peer required
replacing the process-wide immutable configuration snapshot and restarting the
interface. A restart interrupts every healthy peer, even when only one peer
changed.

This is unsuitable for controllers that maintain a small full mesh or a
hub-and-spoke network. A topology update must not reset unrelated sessions.

wg-quic already has an endpoint supervisor that resolves hostnames, selects a
numeric endpoint, owns outer-route leases, refreshes routes after host network
changes, and rolls a failed endpoint switch back. Runtime peer mutation and
DDNS refresh must remain under that same ownership. A separate public API that
writes directly to the core would split desired peer state, DNS selection,
transport keys, route leases, and rollback responsibility between processes.

This document specifies one platform-neutral reconciliation contract and the
platform adapters required to expose it on every supported wg-quic platform.

## 2. Support scope and platform contract

Runtime reconciliation is a property of `wg-quic-quick`, not of systemd or any
other service manager. The quick supervisor exposes the same local management
protocol wherever it runs. Service managers only start, stop, and optionally
invoke a convenient reload command.

The implementation covers this platform-family matrix; the evidence level of
each exact OS/architecture/package tuple is tracked separately below:

| Platform | Privileged management transport | Service integration | Persistent source | Crash-persistent route ownership |
| --- | --- | --- | --- | --- |
| Linux with systemd | root-only Unix socket | `ExecReload` calls `wg-quic-quick reload` | `/etc/wg-quic/<name>.conf` | ordinary peer routes die with the TUN; no v1 hot policy-rule mutation |
| Linux with OpenRC, runit, s6, dinit, SysV, or an external supervisor | the same root-only Unix socket | the shipped OpenRC adapter or administrator calls the same CLI; no systemd requirement | `/etc/wg-quic/<name>.conf` | same Linux TUN ownership |
| OpenWrt | root-only Unix socket | procd `reload_service` reconciles each affected instance | `/etc/wg-quic/<name>.conf`, selected by UCI | same Linux TUN ownership; automatic full-tunnel changes restart only |
| FreeBSD | root-only Unix socket | rc.d `reload` calls the same CLI for each instance | `/usr/local/etc/wg-quic/<name>.conf` | TUN peer routes plus a root-owned outer endpoint-route ledger |
| OPNsense | root-only Unix socket | configd renders candidates and invokes reconcile/reload per instance | plugin-generated `/usr/local/etc/wg-quic/quicN.conf` | FreeBSD ownership model under the plugin lifecycle |
| Windows | ACL-protected named pipe owned by the per-tunnel LocalSystem supervisor | the installed manager and desktop forward authorized operations without replacing the SCM tunnel service | `%ProgramData%\wg-quic\interfaces\<name>.conf` | protected endpoint-route ledger and per-tunnel peer-route transaction journal keyed by LUID/compartment |

“Full-platform support” means identical reconciliation, generation,
idempotency, redaction, and rollback semantics. It does not require quick to
know how every third-party Linux init system starts a service. A tunnel started
with `wg-quic-quick run` under any competent supervisor still supports direct
`check`, `show`, `reconcile`, `reload`, and `refresh-endpoints` operations.

The runtime design is architecture-neutral. Release claims follow the package
matrix: Linux, FreeBSD, and Windows amd64/arm64; OpenWrt armsr/armv8 and x86/64;
and the amd64 OPNsense release trains carried by CI. Cross-compilation proves
build support, not runtime support. Documentation and capability reporting must
distinguish cross-built, emulated, and native lifecycle-tested targets.

No platform may silently turn an unsupported runtime change into a restart.
It must advertise capabilities and return a structured `restart_required` or
`unsupported_capability` result before mutation.

Support claims use four deliberately different labels:

1. `build-supported`: the target cross-compiles and its package is produced;
2. `unit-verified`: shared contracts and the adapter's pure planning and
   serialization tests pass;
3. `runtime-verified`: the target boots, creates the TUN, reconciles peers, and
   passes rollback/failure injection in a native or emulated environment; and
4. `integration-verified`: the installed service manager, package permissions,
   upgrade path, and UI/manager forwarding pass end to end.

Release notes and the README use the strongest label actually proved for each
OS/architecture. A cross-built Windows ARM64 executable or OpenWrt IPK is not,
by itself, a runtime-support claim. The live management endpoint remains
authoritative: labels never override its negotiated capability set.

Support has four independent dimensions and documentation must not collapse
them into one “supported” boolean:

- **data plane**: TUN creation, QUIC, WireGuard, FEC, obfuscation, and traffic;
- **runtime control**: management transport, peer transaction, DDNS, routes,
  rollback, and crash recovery;
- **service/package**: installed permissions, init/SCM/configd/procd behavior,
  upgrade, and removal; and
- **architecture**: the exact CPU/OS ABI on which the preceding dimensions
  were exercised.

For example, native Windows x64 service coverage does not make Windows arm64
runtime-verified, and an OpenWrt x86_64 SDK build does not prove procd reload in
an x86_64 guest. Conversely, use of OpenRC instead of systemd changes only the
service/package dimension; it does not create a different reconciliation
protocol.

The capability decision is made in this order: secure platform initialization
and crash recovery, quick-to-core version negotiation, route/backend
availability, then field-level diff validation. `peer_reconcile_v1` is absent
if a required platform primitive failed to initialize. A capability may be
present while a particular request still receives `restart_required` for an
immutable or full-tunnel change.

## 3. Goals and guarantees

The implementation contract provides:

1. Reconciliation of a complete desired peer set without replacing the TUN or
   resetting peers absent from the diff.
2. Compare-and-swap protection against stale controllers.
3. Idempotent recovery after a lost response or client disconnect.
4. Runtime add, update, and removal of peers, AllowedIPs, endpoints,
   persistent keepalive, and, after the transport-policy phase, per-peer FEC
   policy.
5. DDNS refresh integrated with peer identity, transport keys, route ownership,
   and reconciliation.
6. Candidate resource preparation before an endpoint switch, generation-bound
   authenticated readiness, retention of old resources until success, and
   compensating rollback after failure.
7. Manager-neutral reload commands with systemd, non-systemd Linux, procd,
   rc.d, configd, and Windows SCM integrations.
8. Non-secret status sufficient to diagnose desired state, persistence drift,
   DNS selection, sessions, and failed transactions.
9. Conservative crash recovery that never deletes unowned host state.

The transaction is atomic at the supported operator boundary: it has one
visible commit result and one desired-state generation. Operating-system route
APIs, WireGuard state, and transport sessions cannot be changed in one hardware
transaction. The implementation therefore uses prepare, a short serialized
commit, and compensating rollback.

The exact continuity guarantee is:

- peers absent from the diff keep their WireGuard objects, routes, and QUIC
  sessions throughout successful and failed reconciliation;
- a changed peer may experience a bounded interruption while its single
  WireGuard endpoint or AllowedIP ownership changes;
- old endpoint sessions, keys, and route leases are retained until the changed
  peer proves readiness or rollback completes; and
- no zero-packet-loss guarantee is made for a peer that is itself being
  changed.

This is prepare-before-switch with rollback. It must not be described as true
dual-path make-before-break unless the data plane later supports two
simultaneously usable WireGuard endpoints for one peer.

## 4. Non-goals and restart boundaries

The first peer-reconciliation implementation does not hot-change:

- the interface private key, addresses, listen port, MTU, DNS, route table,
  fwmark policy, or lifecycle hooks;
- interface-wide carrier, congestion controller, FEC mode, obfuscation mode,
  FEC framing limits, or queue sizes;
- the preshared key of an already active peer;
- remote HTTP management;
- SRV-based dynamic ports;
- network-wide distributed transactions;
- multipath scheduling, port hopping, or a TCP carrier; or
- persistent configuration without an explicit controller or reload workflow.

`PreUp`, `PostUp`, `PreDown`, and `PostDown` remain interface lifecycle hooks.
Peer reconciliation and canonical reload never execute them. Changing hook
content is an interface-wide change and returns `restart_required`; a service
adapter must not perform that restart unless its caller explicitly requested
one.

An attempted immutable change returns `restart_required` before any peer is
prepared. AllowedIP changes that introduce the first default route or remove
the last default route also return `restart_required` in the first
implementation: those transitions change interface-wide fwmark, policy rules,
DNS routing, and full-tunnel behavior. They may become hot changes only after a
separate policy-routing transaction is implemented on every platform.

## 5. Ownership and control topology

`wg-quic-quick` owns:

- the complete desired peer projection;
- host addresses and the interface-level network policy;
- inner route ownership derived from AllowedIPs;
- configured endpoint hostnames and numeric candidate selection;
- outer-route leases;
- dynamic Salamander key leases and per-peer transport policy;
- the supervised core process; and
- transaction epoch, generation, idempotency results, and recovery state.

The core owns portable data-plane mechanics. It may expose versioned
prepare/commit/rollback/finalize operations to quick, but they are not the
operator API. The existing read-only status endpoint remains read-only.

Each running interface has two logical control surfaces:

1. A public, non-secret, read-only status endpoint where the platform already
   supports one.
2. A privileged quick management endpoint for mutation and detailed
   transaction status.

On Unix-like systems the mutation socket is mode `0600`, lives under the
platform run directory, and is created by the quick supervisor. On Windows it
is a local named pipe created with first-instance protection and an ACL that
allows only LocalSystem and authorized administrators to mutate state. The
installed Windows management service may forward an already authorized request
to the per-tunnel supervisor, but an unprivileged WebView never receives direct
mutation access.

The pipe security descriptor pins Owner to the actual privileged creator. In
the installed lifecycle that creator is LocalSystem. A deliberately elevated
same-user debug/test supervisor instead owns the pipe with that administrator's
user SID; its client accepts only LocalSystem or its own enabled-Administrator
SID, never an arbitrary administrator or unelevated user. This avoids requiring
`SeRestorePrivilege` merely to assign LocalSystem ownership outside the service
lifecycle while retaining owner verification against pipe squatting.

The quick-to-core control channel remains separate and inaccessible to public
controllers. This prevents a controller from bypassing DNS, host-route, or
transport-key ownership.

The core also receives a one-way process-lifetime primitive from quick. Linux,
OpenWrt, FreeBSD, and OPNsense use an inherited non-secret pipe whose write end
exists only in the quick supervisor; EOF cancels the core and closes its TUN.
This works under systemd, OpenRC, procd, rc.d, or a direct supervisor and avoids
Linux `PDEATHSIG` being tied to whichever Go runtime thread created the child.
FreeBSD additionally uses `Pdeathsig` as defense in depth. Windows assigns the
core to a kill-on-close Job Object owned by the per-tunnel LocalSystem
supervisor. A service manager's process-group or cgroup cleanup is useful but
is not the only mechanism preventing an orphaned core.

Both management transports carry the same bounded, versioned protocol. One
connection or pipe instance accepts one JSON request and returns one JSON
response; long-running work is subsequently recoverable by request ID instead
of keeping transport identity as transaction identity. Every request includes
`protocol_version`, `operation`, and the interface identity. Version 1 rejects
malformed requests, unsupported operations, and unmet explicitly requested
capabilities with a structured error. Fields marked optional by the protocol
may be added compatibly, and clients ignore unknown response fields. The status
response advertises the management, quick-to-core, and peer-reconciliation
capabilities independently.

At startup quick negotiates a version and capability set with its supervised
core. It refuses mutation if the core cannot provide every primitive required
by the proposed diff. This check occurs before resource preparation, so a mixed
binary upgrade cannot accidentally fall back to the mutating text UAPI.

## 6. Desired peer model and canonicalization

The WireGuard public key is the stable identity of a peer. A reconciliation
request contains the complete desired peer set, not an imperative sequence of
add and remove commands.

Conceptually, each desired peer contains:

```json
{
  "public_key": "BASE64_WIREGUARD_PUBLIC_KEY",
  "preshared_key": "OPTIONAL_BASE64_SECRET",
  "allowed_ips": ["10.254.0.2/32"],
  "endpoint": "node-b.example.net:12580",
  "persistent_keepalive": 25,
  "fec_policy": "balanced"
}
```

An empty endpoint means the peer accepts an authenticated inbound session and
is not actively dialed. Hostnames remain desired state and are never replaced
by the currently selected numeric address.

Before diffing, quick constructs a canonical model:

- keys use strict canonical base64;
- peers are sorted by decoded public key;
- prefixes are masked, deduplicated within a peer, and sorted by address family,
  prefix length, and address;
- numeric endpoints use canonical `netip` spelling;
- DNS names are compared case-insensitively with one documented trailing-dot
  policy while preserving an operator-facing spelling; and
- absent optional values and their explicit defaults compare equal.

Reordering peers, DNS answers, or AllowedIPs is a no-op.

Validation rejects at least:

- malformed or duplicate public keys;
- malformed endpoints, unspecified addresses, or zero ports;
- malformed or duplicate AllowedIPs;
- the exact same prefix assigned to multiple peers;
- unsupported peer transport policies;
- an attempted active-peer preshared-key rotation;
- a full-tunnel mode transition not supported at runtime; and
- any interface-wide change that requires restart.

Nested prefixes are not automatically conflicts. WireGuard longest-prefix
semantics are valid, so `10.0.0.0/8` and `10.1.0.0/16` may belong to different
peers. Exact duplicate ownership is rejected because its owner would depend on
mutation order.

For an existing peer, an omitted preshared key means “retain the active key.” A
specified key must compare equal to the active secret or the request is
rejected. A newly added peer may specify a key or no key. Secret comparisons
are constant-time.

Quick computes two digests:

- a process-keyed HMAC over the semantic mutation identity—the interface-local
  expected epoch/generation tuple and complete canonical desired projection,
  including secrets—used only for request-ID collision detection; and
- a stable digest of the non-secret desired projection, exposed in status.

No raw digest over a private or preshared key is exposed.

Transport framing is not mutation identity. The request ID itself, protocol
envelope, requested wait deadline, and equivalent capability assertions are
excluded from that HMAC. The first accepted use of a request ID owns the
clamped operation deadline. A retry may therefore use a later socket deadline
and still join or recover exactly the same transaction; changing its CAS tuple
or desired projection remains a collision.

## 7. Epochs, generations, requests, and idempotency

Each quick supervisor starts with a random 128-bit `epoch`. Its initial parsed
peer set is desired generation 1. Every successful non-no-op desired-state
change increases the generation exactly once. Endpoint selection has a
separate per-peer `endpoint_generation` and does not affect desired generation.

A mutation request contains:

```json
{
  "protocol_version": 1,
  "operation": "reconcile",
  "interface": "wg0",
  "required_capabilities": ["peer_reconcile_v1"],
  "expected_epoch": "6fc1...",
  "expected_generation": 12,
  "request_id": "controller-generated-unique-id",
  "deadline_unix_millis": 1787313700000,
  "candidate_path": "/etc/wg-quic/.candidates/wg0.request-id.conf"
}
```

The local management envelope never carries secret-bearing peer projections.
For `reconcile`, `candidate_path` names a protected full profile that quick
opens and validates through the platform secure-open path. `reload` omits
`candidate_path` and uses the canonical profile. Unknown JSON fields,
including an inline `desired_peers` projection, are rejected in protocol v1.

Request IDs are length-bounded and scoped to one interface epoch. Processing
order is mandatory:

1. Canonicalize enough input to compute the process-keyed request fingerprint.
2. Look up `request_id` before checking generation.
3. If the ID names an in-progress request with the same fingerprint, return or
   wait on that same transaction; never start another one.
4. If it names a completed request with the same fingerprint, return the
   original result even though generation has advanced.
5. If it names a different fingerprint, return `request_id_conflict`.
6. Only a new request proceeds to epoch and generation CAS validation.

Successful results and terminal failures are cached. Retrying a cached failure
returns the same failure; a controller that intentionally wants a new attempt
uses a new request ID. The cache is bounded by count and age. If an entry has
expired, an ordinary submission is treated as a new request and still passes
through CAS. A separate `transaction-status` query may return
`unknown_request_id`; an ordinary new reconciliation request never does.

Submitting the active canonical desired set is a no-op, returns `changed=false`,
and does not increase generation.

The server clamps client deadlines to configured minimum and maximum operation
limits. Once accepted, a reconciliation runs under a supervisor-owned context,
not the socket lifetime. Client disconnect does not cancel it. The client can
reconnect and query by request ID. This is required because endpoint readiness
can exceed the existing short core-control timeout.

DDNS refresh, route rebinding, roaming, health-triggered candidate rotation,
and reconnect attempts are operational changes. They update endpoint
generation or status, never desired generation.

## 8. Concurrency and runtime state

One interface executes at most one desired-set reconciliation transaction at a
time. The coordinator also maintains a per-peer operation lock.

The locking rules are:

- reconciliation computes the affected public keys, sorts them, and reserves
  only those peer locks;
- scheduled DDNS and administrative endpoint refresh take the same peer lock;
- an unrelated peer may continue DDNS refresh while a reconciliation waits on
  affected peers;
- host inner-route commit uses a short platform route-plan lock;
- route-change notification handling never holds a route-manager lock while
  waiting for WireGuard readiness; and
- no control-plane lock is held while writing a response to a client.

Each dynamic peer owns a cancellable refresh worker. A newly added worker starts
only after commit. A removed worker is cancelled and joined while its peer lock
is reserved, before its state is detached. A refresh worker must never index a
peer map after removal without holding a stable peer reference.

Transaction states are externally visible:

```text
accepted -> validating -> preparing -> switching -> committing -> finalizing
                                      \-> rolling_back
```

Terminal states are `committed`, `no_op`, `rejected`, `rolled_back`, and
`degraded`. `degraded` means rollback or finalization failed and status must be
consulted for actual state.

## 9. Core and host transaction primitives

The stock WireGuard text UAPI mutates as it parses and is not a transaction
boundary. Runtime reconciliation must not serialize a full
`replace_peers=true` request and hope to undo it. The wireguard-go fork and core
need typed, prevalidated operations over affected peers.

The internal core surface should provide the equivalent of:

```text
PreparePeerSet(transaction, peer_diff) -> prepared_core_lease
PrepareEndpointCandidate(transaction, peer, endpoint_generation, endpoint)
WaitAuthenticated(transaction, peer, endpoint_generation)
CommitPeerSet(prepared_core_lease)
RollbackPeerSet(prepared_core_lease)
FinalizePeerSet(prepared_core_lease)
```

Preparation may create a new peer with no active AllowedIPs, keepalive disabled,
and candidate transport resources. It must not replace existing peer objects or
close unrelated sessions. Updating a peer snapshots only the affected mutable
fields. Finalization destroys removed peers and obsolete resources after commit.

The platform host surface should provide the equivalent of:

```text
PlanPeerRoutes(old_desired, new_desired) -> route_plan
PreparePeerRoutes(route_plan) -> prepared_route_lease
CommitPeerRoutes(prepared_route_lease)
RollbackPeerRoutes(prepared_route_lease)
FinalizePeerRoutes(prepared_route_lease)
```

A prepared lease owns every object it created and retains failed cleanup work
for retry. Rollback and finalization are idempotent. Implementations may use
netlink, routing sockets, IP Helper APIs, or existing command wrappers, but the
ownership contract is identical.

## 10. Reconciliation algorithm and commit ordering

From the caller's point of view, reconciliation is one transaction:

1. Securely read one immutable candidate snapshot.
2. Parse, canonicalize, and validate the complete configuration.
3. Reject immutable changes before peer mutation.
4. Apply request-ID, epoch, and generation checks.
5. Compute add, update, remove, inner-route, endpoint, key, and policy diffs.
6. Reserve affected peer locks in public-key order.
7. Resolve new or changed hostname endpoints and record all candidates.
8. Acquire candidate outer-route leases.
9. Prepare dynamic obfuscation keys, peer cryptographic state, keepalive,
   AllowedIP ownership, transport policy, and the host route plan.
10. Pre-dial candidate QUIC paths where useful, without treating an anonymous
    QUIC handshake as peer readiness.
11. For a new actively dialed peer or an endpoint change, install a tentative
    endpoint generation, probe it, and wait for generation-bound authenticated
    WireGuard readiness. Keep the old session, association, and route lease
    available for rollback.
12. Enter the short commit section and apply affected WireGuard ownership and
    host route changes in the safe order below.
13. Publish the new quick desired set and increase desired generation once.
14. Start new refresh workers and detach removed workers.
15. Finalize removed peers, stale endpoint sessions, dynamic keys, and obsolete
    route leases.

For route additions, core AllowedIP ownership is committed before exposing a
new host route, so packets are not routed into the TUN before WireGuard can
select a peer. For route removals, the host route is withdrawn before the last
core AllowedIP owner is removed. Moving an exact prefix between peers does not
change the host route because it still points at the same TUN; only WireGuard
ownership changes in the serialized core commit.

If any pre-commit step fails, quick rolls back every prepared lease and leaves
desired generation unchanged. If a commit step fails, quick attempts
compensating rollback to the prior desired set. If rollback succeeds, the
result is `rolled_back`. If rollback fails, the result is `degraded`, and status
reports the actual peer, endpoint, route, and lease state rather than claiming
either desired set is fully active.

Finalization failure after desired commit does not decrement generation or
claim rollback. The result remains committed with `cleanup_pending=true`, and
the supervisor retries only the precisely owned stale resources. A failure
that leaves security-sensitive peer access active is instead `degraded` and
must be surfaced prominently.

A peer with no configured endpoint cannot prove remote readiness during local
reconciliation. Its successful prepared result is `configured/listening`. A
later authenticated inbound session changes operational status only.

Higher-level controllers remain responsible for draining application routes
before peer removal. Local reconciliation does not implement application-level
traffic draining.

## 11. Inner-route and full-tunnel semantics

Host routes are derived from the union of active AllowedIPs, while WireGuard
cryptokey routing retains per-peer ownership. The platform route plan therefore
distinguishes:

- prefix added to or removed from the interface-wide union;
- exact prefix ownership moved between peers;
- nested-prefix changes that preserve longest-prefix behavior; and
- full-tunnel mode transitions.

`Table=off` changes only WireGuard ownership and creates no host inner routes.
An explicit non-auto table uses that same table for incremental changes.

For the first implementation, adding the first or removing the last IPv4 or
IPv6 default route is `restart_required` when it changes automatic policy
routing. Adding another default-route owner is already rejected as duplicate
exact ownership. A later implementation may hot-change full-tunnel state only
after Linux/OpenWrt policy rules, FreeBSD/OPNsense routing, and Windows IP Helper
backends all have tested transactional support.

Each platform backend keeps a narrow ownership ledger for operations that can
survive a process crash. It never deletes a route, rule, DNS policy, or firewall
object merely because it looks similar. Cleanup requires a matching interface,
transaction/epoch record, and platform identity where available.

The v1 persistence boundary is intentionally narrow:

- Linux/OpenWrt peer routes are attached to the TUN and disappear with that
  interface. Automatic full-tunnel policy-rule transitions are restart-only,
  so runtime peer reconciliation creates no independently persistent Linux
  policy-rule or firewall object.
- FreeBSD/OPNsense outer endpoint host routes can outlive quick. Their ledger
  records address family, exact destination, gateway or egress interface,
  owning wg-quic interface, operation state, schema, generation, and checksum.
  Recovery deletes only an `active` or `pending-delete` record whose live route
  identity still matches. A `pending-add` is ambiguous and is deliberately
  leaked and diagnosed instead of guessed.
- Windows outer endpoint pins use the protected shared `routes-v1.json`
  ledger. Runtime inner peer-route transactions use a separate per-tunnel
  `peer-routes-v1-<name-hash>.json` journal. It stores only canonical prefix
  projections, transaction ID, before/after sets, phase, generation, LUID,
  compartment, schema, and checksum—never peer or interface keys. Runtime
  inner routes are changed through IP Helper using the exact compartment,
  interface LUID, destination, and zero next hop. The journal is durable before
  each removal/addition phase and records completion only after that entire
  phase succeeds.

  On startup, a completed projection is proven wg-quic ownership and can be
  removed before canonical configuration is re-applied. A route observed after
  an interrupted pending add is ambiguous: it is adopted only if the canonical
  file now requests the exact prefix; otherwise it is retained and diagnosed.
  `down --repair`, after proving the exact tunnel service is stopped, is the
  explicit stronger authority that may remove such an exact journalled route.
  Identity mismatch never triggers deletion.

Process-local cleanup jobs retain typed core, endpoint, and route leases and
retry only incomplete finalizers. Durable ledgers cover only host objects that
can survive process death; opaque core transactions, secrets, QUIC sessions,
and request-cache entries are never serialized.

## 12. Dynamic transport keys and per-peer FEC policy

With Salamander enabled, adding a WireGuard peer also adds a derived transport
key. Runtime support therefore needs a reference-counted key registry, not only
an endpoint-to-key outbound cache.

Preparation registers a candidate key for inbound decode and outbound endpoint
association. Commit attaches it to the peer runtime. Removal first prevents new
sessions, then retains the key while an accepted or configured session can
still deliver packets, and finally removes it after peer/session finalization.
The same key may be referenced by candidate and active endpoint generations.

Preshared-key rotation of an active peer remains restart-required because the
WireGuard handshake secret and derived Salamander key must change together.

Per-peer FEC policies are `latency`, `balanced`, and `throughput`. They are
implemented as a dynamic encoder policy selected by authenticated peer
identity, within the interface-wide FEC wire format and framing limits. The
decoder accepts all valid framing allowed by the interface-wide configuration.

Outbound endpoint associations map a session to a peer policy. For an inbound
or roamed endpoint, the policy is attached only after WireGuard authenticates
which peer owns that path. A policy change takes effect at an FEC group boundary
after flushing or expiring the old encoder group; it must not reinterpret an
in-flight group. If that mapping and group-boundary transition are not yet
implemented, per-peer FEC mutation is advertised as unsupported while all
other peer reconciliation remains available.

The v1 policy mapping is deterministic and only narrows or schedules the
interface-wide encoder limits:

| Policy | data shards per group | interleave | flush deadline |
| --- | ---: | ---: | ---: |
| `latency` | `min(interface_data_shards, 4)` | `1` | `min(interface_deadline, 1 ms)` |
| `balanced` | interface value | interface value | interface value |
| `throughput` | interface value | `max(interface_interleave, 2)` | `max(interface_deadline, 4 ms)` |

All values remain bounded by the negotiated WGQF framing maxima. The session
send worker owns a policy transition: it flushes and transmits the old groups,
changes the profile, and only then accepts a frame into the new group. It does
not close QUIC, reset the decoder, or change the wire version. If old-group
flush or transmission fails, ordinary transport recovery runs and the control
plane must not report a half-applied encoder profile as committed.

## 13. Authenticated readiness and endpoint migration

QUIC uses ephemeral outer TLS and does not authenticate WireGuard peer identity.
`session=established` is therefore insufficient readiness.

Every tentative endpoint update has an `endpoint_generation`. The core records
authenticated activity together with the generation and numeric endpoint that
carried it. Readiness requires one of:

- a completed WireGuard handshake whose authenticated response was received on
  the tentative generation; or
- an authenticated WireGuard data packet received on that generation after
  the candidate was installed.

Historical handshake or activity timestamps from an older generation never
satisfy readiness. Successful local transmission alone is insufficient because
it does not prove the remote authenticated or received the packet.

Endpoint migration proceeds as follows:

1. Acquire the candidate outer-route lease and dynamic key association.
2. Pre-dial the candidate QUIC path when possible.
3. Retain the old endpoint session, association, and lease.
4. Install the tentative WireGuard endpoint generation and send a probe.
5. Wait for generation-bound authenticated readiness.
6. On success, publish the selected endpoint and retire old resources.
7. On timeout or error, restore the old endpoint with a new rollback generation
   and release candidate resources.

Because WireGuard currently has one configured endpoint per peer, step 4 may
briefly direct the changed peer away from the old endpoint. Retaining the old
resources makes rollback fast but does not create simultaneous dual-path
WireGuard forwarding.

## 14. DDNS and health-triggered candidate rotation

The supervisor keeps configured hostname, selected numeric endpoint, and the
last resolved candidate set as separate state.

Required DDNS behavior:

- refresh according to resolver TTL when available, clamped by configurable
  minimum and maximum intervals;
- retain the conservative fallback interval when the system resolver does not
  expose TTL;
- apply jitter so a mesh does not refresh all endpoints simultaneously;
- retain the selected endpoint after timeout, SERVFAIL, or NXDOMAIN;
- keep a healthy selected address when only DNS answer ordering changes;
- support multiple A and AAAA candidates without oscillation;
- acquire a route lease and verify authenticated readiness before publishing a
  switch;
- roll back endpoint and route ownership when a candidate is not ready; and
- use bounded exponential backoff after failed resolution or migration.

The selected address may remain in DNS while its path is unusable. Candidate
rotation is therefore also triggered by a configurable threshold of consecutive
transport reconnect failures or absence of authenticated recovery after a
probe window. A healthy selected address stays sticky; latency alone never
causes switching. Rotation state remembers recently failed candidates and uses
backoff so A/AAAA answers do not oscillate.

Scheduled refresh, health-triggered rotation, route-change reconciliation, and
administrative refresh all take the affected peer operation lock and use the
same endpoint transaction. They never write a core endpoint behind the route
lease owner's back.

Administrative operations are:

```text
RefreshEndpoints(interface)
RefreshEndpoint(interface, public_key)
```

They do not change desired generation.

## 15. Persistent configuration and secure snapshots

Persistent configuration remains file-owned. Runtime reconciliation and file
durability are deliberately separate because an operating-system rename and a
live multi-process transaction cannot be one atomic operation.

There are two supported workflows.

### Candidate reconciliation

An external controller:

1. writes a candidate in a protected staging location;
2. asks quick to securely open, validate, and reconcile that exact snapshot;
3. receives or recovers the transaction result by request ID; and
4. only after runtime commit, atomically promotes the same bytes to the
   canonical path and fsyncs the containing directory where supported.

Runtime commit before promotion is not durable. If quick and core restart in
that window, canonical configuration wins and the old desired set returns. A
controller must not report a durable topology update until promotion succeeds.

### Canonical reload

An administrator or platform controller atomically replaces the canonical
file and calls `reload`. Quick reconciles it against live desired state. If
runtime reconciliation fails, status reports both canonical and runtime
digests with `persistent_drift=true`; it must not pretend the file was rolled
back. Status securely refreshes the canonical projection, so an external atomic
promotion becomes visible without a second mutation request. An unreadable or
invalid canonical file retains the last valid digest, sets `canonical_error`,
and forces `persistent_drift=true`. A later reload or service restart retries
the canonical desired state.

Unix candidate and canonical reads must use a new shared secure-open path:

- reject symlinks and non-regular files;
- pin one descriptor and parse only those bytes;
- require the expected privileged owner;
- reject group/world-writable files and directories in the trusted path;
- enforce a bounded file size and peer/prefix counts; and
- avoid reopening by pathname after validation.

Windows uses the existing protected ProgramData directory/file lease model and
opens by a handle whose owner, DACL, reparse state, and identity remain pinned
through parsing. Windows candidate files live in the protected `interfaces`
directory under random non-canonical names; the manager deletes them after the
transaction and atomically replaces `<name>.conf` only after runtime commit.
OPNsense and OpenWrt render candidates with root-only modes before invoking
quick.

Secrets never appear in process arguments. A path may appear in arguments, but
the supervisor reads the content through the protected management boundary.

## 16. CLI and service-manager integration

The manager-neutral operator surface is:

```sh
# Validate and reconcile a candidate peer projection.
wg-quic-quick reconcile wg0 /path/to/candidate.conf \
  --expected-epoch EPOCH --expected-generation 12 --request-id ID --json

# Re-read the canonical config and reconcile mutable fields.
wg-quic-quick reload wg0 --json

# Recover an accepted transaction after disconnect.
wg-quic-quick transaction-status wg0 --request-id ID --json

# Trigger operational endpoint refresh.
wg-quic-quick refresh-endpoints wg0 --json
wg-quic-quick refresh-endpoints wg0 --peer PUBLIC_KEY --json

# Inspect desired, persistent, and operational state.
wg-quic-quick show wg0 --json
```

Platform adapters use those commands:

- systemd adds `ExecReload=/.../wg-quic-quick reload %i --json`;
- the shipped OpenRC instance adapter and FreeBSD rc.d script expose a reload
  action that invokes quick for the selected instance;
- runit, s6, dinit, SysV, and custom supervisors may invoke the CLI directly;
- OpenWrt `reload_service` determines affected UCI instances and calls reload
  without stopping procd unless restart is required and explicitly approved;
  the canonical profile is intentionally not registered as a procd `file`
  restart dependency, because an atomic controller rename must reach the
  management transaction instead of racing an automatic process restart;
- OPNsense configd renders candidates, reconciles running instances, starts new
  instances, and drains/stops removed instances according to plugin policy; and
- the Windows manager accepts an authorized reload/reconcile request, securely
  stages or reads ProgramData configuration, and forwards it to the existing
  per-tunnel LocalSystem supervisor pipe.

The desktop UI is a client of the same manager operation. It does not perform
routes, key installation, or reconciliation in the WebView process.

None of these reload adapters invokes interface lifecycle hooks. When reload
returns `restart_required`, the adapter reports that result and leaves the
running interface intact unless a separate, explicitly authorized restart
operation is used.

## 17. Response and status model

A reconciliation result contains machine-readable commit information:

```json
{
  "epoch": "6fc1...",
  "request_id": "...",
  "state": "committed",
  "generation": 13,
  "changed": true,
  "added": ["FULL_PUBLIC_KEY"],
  "updated": ["FULL_PUBLIC_KEY"],
  "removed": ["FULL_PUBLIC_KEY"],
  "restart_required": false,
  "cleanup_pending": false,
  "peer_results": []
}
```

Machine-readable peer identifiers are full public keys. Human messages and
logs may use a short public-key prefix.

Interface status exposes at least:

```json
{
  "supervisor_epoch": "6fc1...",
  "desired_generation": 13,
  "desired_digest": "sha256:...",
  "canonical_digest": "sha256:...",
  "persistent_drift": false,
  "recovery": {
    "state": "clean",
    "retained_ambiguous_objects": 0
  },
  "transaction": null,
  "last_transaction": {
    "request_id": "...",
    "state": "committed"
  },
  "capabilities": [
    "management_protocol_v1",
    "peer_reconcile_v1",
    "dynamic_obfs_keys",
    "authenticated_endpoint_generation",
    "session_telemetry_v1",
    "recent_session_telemetry_v1",
    "session_events_v1",
    "receive_queue_overflow_v1"
  ]
}
```

Per-peer status exposes non-secret desired and operational state:

```json
{
  "public_key": "...",
  "configured_endpoint": "node-b.example.net:12580",
  "selected_endpoint": "203.0.113.10:12580",
  "endpoint": "198.51.100.27:43192",
  "current_endpoint": "198.51.100.27:43192",
  "dns_candidates": ["203.0.113.10", "203.0.113.11"],
  "last_resolved_at": "2026-08-21T12:00:00Z",
  "next_refresh_at": "2026-08-21T12:01:00Z",
  "last_resolution_error": null,
  "session": "ready",
  "endpoint_generation": 4,
  "authenticated_endpoint_generation": 4,
  "latest_handshake": 1787313600,
  "last_rx": 1787313601,
  "last_tx": 1787313601,
  "last_activity": 1787313601,
  "last_activity_direction": "received",
  "reconnect_attempts": 2,
  "transfer_rx": 1234,
  "transfer_tx": 5678,
  "fec_policy": "balanced",
  "cleanup_pending": false
}
```

When `session_telemetry_v1` is advertised, status also exposes every active
outer connection independently. The association list is intentionally plural:
multiple configured WireGuard peers can share one endpoint and therefore one
QUIC session.

```json
{
  "telemetry_version": 1,
  "session_id": 27,
  "session_generation": 3,
  "role": "outbound",
  "state": "established",
  "configured_endpoint": "203.0.113.10:12580",
  "current_endpoint": "198.51.100.27:43192",
  "established_at": "2026-08-24T10:00:00Z",
  "sampled_at": "2026-08-24T10:00:30Z",
  "peers": [
    {
      "public_key": "...",
      "endpoint_generation": 4,
      "configured": true,
      "authenticated": true
    }
  ],
  "stats": {
    "wire_tx_packets": 1200,
    "wire_rx_packets": 1188,
    "quic_packets_lost": 12,
    "quic_spurious_loss_packets": 2,
    "quic_pto_count": 1,
    "quic_smoothed_rtt_us": 74200,
    "quic_rttvar_us": 8300,
    "quic_congestion_window_bytes": 65536,
    "send_queue_depth": 0,
    "queue_drops": 0
  }
}
```

Session counters begin at connection creation and disappear from the active
set when the session closes. A collector keys deltas by supervisor epoch,
session ID, and session generation. Linux/OpenWrt, FreeBSD/OPNsense, and
Windows use this same management schema. Platform-specific kernel counters
must carry explicit support/source metadata; absence is never encoded as a
zero event count. The active-session list is bounded and prioritizes configured
outbound connections; `session_telemetry_omitted` reports the excluded count.

When `recent_session_telemetry_v1` is advertised, status also carries a
separate immutable `recent_sessions` list. It never represents usable active
state. The core retains at most 64 records and expires each after five minutes;
`recent_sessions_evicted_total` is cumulative for both capacity and TTL
eviction. History is process-local and disappears at a core/event-stream
boundary.

```json
{
  "telemetry_version": 1,
  "final_sequence": 8,
  "session_id": 27,
  "session_generation": 3,
  "role": "outbound",
  "state": "closed",
  "configured_endpoint": "203.0.113.10:12580",
  "current_endpoint": "198.51.100.27:43192",
  "closed_at": "2026-08-25T02:00:00Z",
  "sampled_at": "2026-08-25T02:00:00Z",
  "peers": [{
    "public_key": "...",
    "endpoint_generation": 4,
    "configured": true,
    "authenticated": true
  }],
  "close_reason": "endpoint_replaced",
  "error_class": "",
  "replaced_by_session_id": 31,
  "final": true,
  "final_stats": {
    "quic_packets_lost": 12,
    "quic_pto_count": 1,
    "quic_congestion_window_bytes": 65536
  }
}
```

Stable close reasons are `local_shutdown`, `remote_close`, `idle_timeout`,
`handshake_timeout`, `transport_error`, `endpoint_replaced`,
`configuration_removed`, and `unknown`. `last_error` is single-line, redacted,
and bounded; automation should branch on `close_reason`/`error_class`, not
parse that message. `replaces_session_id` and `replaced_by_session_id` join
reconnect generations when known.

`stats.receive_queue_overflow` is interface-scoped because every QUIC session
shares the UDP socket:

```json
{
  "supported": true,
  "source": "linux_so_rxq_ovfl",
  "platform": "linux",
  "packets": 3
}
```

Linux extends the kernel's 32-bit `SO_RXQ_OVFL` value into a process-lifetime
monotonic counter and preserves monotonicity across carrier socket recreation.
FreeBSD/OPNsense, Windows, and other unsupported receive paths return
`supported=false`, `source="unavailable"`, their `platform`, and a zero value;
that is an availability statement, not evidence of zero kernel drops.

The read-only `events` operation pages the bounded event stream without
restarting a peer or interface:

```json
{
  "protocol_version": 1,
  "operation": "events",
  "interface": "wg0",
  "required_capabilities": ["session_events_v1"],
  "event_stream_id": "f44c...",
  "after_sequence": 72,
  "event_limit": 1024
}
```

The first request may omit `event_stream_id` and use sequence zero. Subsequent
requests must send the returned stream ID with the last consumed sequence.
The response contains `first_available_sequence`, `last_sequence`,
`events_dropped_total`, the core `sampled_at`/`monotonic_elapsed_ns` clock
sample, and up to 1,024 events. The core ring retains at most 4,096 records. A
changed supervisor epoch or event stream is an explicit series boundary; if
`first_available_sequence` skips the caller cursor, the caller has an event
gap and must not present the timeline as complete.

Every event contains telemetry version, stream ID, global sequence, session ID
and generation, type/reason, wall time, monotonic elapsed nanoseconds, and
typed optional `before`/`after` metric snapshots. The supervisor attaches its
epoch to the response batch. Current low-volume event types cover session
lifecycle, controller state, cwnd reduction, PTO, loss/spurious loss, path RTT,
FEC policy, endpoint migration, local queue drop, and interface receive
overflow. There is no per-ACK production event stream and no application
payload in status or events.

The portable reference collector consumes these two read-only operations:

```sh
wg-quic-quick collect wg0 \
  --peer 'FULL_BASE64_PUBLIC_KEY' \
  --duration 30s --interval 100ms --max-bytes 16M \
  --output /var/lib/wg-quic/traces/TRIAL_ID
```

It selects the sole authenticated session first, falls back to a sole
configured association, and rejects ambiguity unless `--session-id` pins one
exact active session. Raw status is retained next to derived CSV rows. Counter
deltas are keyed by `(supervisor_epoch, session_id, session_generation)`; on a
replacement, the old final snapshot is emitted before the new series starts.
A missing/omitted target, counter regression, cursor gap, epoch/stream change,
or output bound violation makes the bundle incomplete instead of fabricating
zero observations. Linux/OpenWrt, FreeBSD/OPNsense, and Windows share this
collector contract and use platform-appropriate root/Administrator-only
output protection.

Endpoint status deliberately has three separate meanings. `configured_endpoint`
is the canonical user configuration and may contain a hostname;
`selected_endpoint` is the numeric candidate owned by `endpoint_generation`;
`current_endpoint` is WireGuard's authenticated live path and can temporarily
differ after QUIC roaming or an outer NAT rebinding. Candidate readiness compares
the selected and authenticated generation/endpoint, while reconnect, rollback,
and resource ownership use the selected endpoint. Neither operation may promote
a transient current endpoint into desired state. The legacy core and quick CLI
`show` field `endpoint` remains a compatibility alias of the live/current
endpoint; the additive `selected_endpoint` field exposes the transactional
target and `current_endpoint` gives management clients an unambiguous name.

The last failed transaction summary is bounded and redacted. It contains stage,
error code, affected public-key prefix, retryability, and whether rollback was
complete, but never candidate configuration text or secrets.

Support labels are build/release metadata, not mutable tunnel state. Runtime
status instead reports authoritative negotiated capabilities and recovery
health. `degraded` recovery or retained ambiguous host objects remain visible
even when ordinary peer reconciliation can continue.

## 18. Error model

Errors use a stable object rather than requiring log parsing:

```json
{
  "code": "readiness_timeout",
  "stage": "switching",
  "peer": "BASE64_PUBLIC_KEY",
  "retryable": true,
  "committed": false,
  "degraded": false,
  "message": "peer AbCd1234 did not authenticate on endpoint generation 4"
}
```

Codes include at least:

- `validation_failed`;
- `stale_epoch`;
- `stale_generation`;
- `request_id_conflict`;
- `transaction_in_progress`;
- `unsupported_capability`;
- `restart_required`;
- `endpoint_resolution_failed`;
- `route_lease_failed`;
- `peer_prepare_failed`;
- `readiness_timeout`;
- `commit_failed`;
- `rollback_failed`;
- `cleanup_pending`;
- `persistent_drift`; and
- `unknown_request_id` for transaction-status queries only.

## 19. Security requirements

- Mutation endpoints and CLI operations are root-only on Unix-like systems.
- Windows mutation requires LocalSystem or an administrator authorized by the
  installed management service; named-pipe impersonation and client identity
  checks remain mandatory.
- Read-only status access never implies mutation access.
- Private and preshared keys never appear in process arguments, public status,
  logs, error payloads, request-cache keys, or stable exposed digests.
- Candidate files use the secure snapshot rules in section 15.
- Desired peer count, total AllowedIPs, candidate size, request-ID length,
  transaction cache, and concurrent status waiters are bounded.
- AllowedIPs continue to enforce authenticated inner source ownership.
- QUIC establishment alone is never peer readiness.
- Outer-route pinning is installed before candidate traffic so endpoint traffic
  cannot recurse into the tunnel it is establishing.
- Rollback never releases an old route or transport key until the core is known
  not to use it; ambiguous ownership is retained and reported.
- Error joining and debug logging pass only redacted typed state, never raw
  candidate lines.

## 20. Crash recovery and reconciliation after restart

An epoch change tells controllers that all process-local request results and
operational endpoint generations expired. Canonical configuration is the
startup source of truth.

Prepared core peers, sessions, and in-memory key leases disappear with the core
process. Host state may survive, so each platform backend maintains a narrow
ownership ledger for persistent side effects that do not disappear with the
TUN.

On startup quick:

1. opens and validates canonical configuration;
2. loads only its own ledger records;
3. verifies each recorded object still has the expected platform identity;
4. repairs or removes abandoned owned state conservatively;
5. starts a new core and applies canonical desired state; and
6. publishes a new epoch only after the management endpoint can report actual
   recovery state.

Linux/OpenWrt v1 performs no hot policy-rule/table or firewall transition;
ordinary peer routes disappear with the TUN and therefore need no durable
journal. FreeBSD/OPNsense verify the recorded outer-route gateway/interface
identity. Windows uses the protected endpoint ledger plus the per-tunnel inner
route journal and IP Helper identity checks. A missing or corrupt ledger causes
a diagnostic and conservative non-deletion, not broad route cleanup. A corrupt
Windows journal is quarantined under the same protected directory and that
startup refuses mutation; a subsequent clean startup may create a new journal.
An otherwise valid peer-route journal whose recorded LUID or compartment no
longer matches is also quarantined, with its routes retained and recovery
reported as degraded; the mismatch evidence is not silently discarded.

If rollback cannot complete before shutdown, the service exits non-zero with
the ledger intact so the service manager and next startup can retry. Abrupt
termination tests validate recovery on the next epoch; they do not pretend a
killed process executed rollback code.

Graceful and abrupt shutdown deliberately take different paths. Graceful
shutdown first closes admission to new reconciliations, cancels the active
operation, waits for its bounded compensating rollback, and propagates a
`degraded` rollback as a non-zero service result before host/core teardown.
Abrupt death cannot run those steps: the core-lifetime primitive releases the
TUN and all process-local leases, while only durable platform journals are
interpreted by the next epoch.

The durable phase recovery rule is:

| Last observable phase | State that may survive | Next-epoch action |
| --- | --- | --- |
| validation, prepare, or endpoint switch | no portable core/session state; a Windows route journal may be `prepared` but no new route is proven | discard process-local work, remove only previously journalled routes, retain any unproven candidate object, then apply canonical state |
| route removal / core commit | Windows journal is `removing` or `removed`; FreeBSD outer records may be `active` | verify exact LUID/compartment or gateway/interface identity, remove proven stale ownership, retain identity mismatches |
| route addition | Windows journal is `adding`; a live candidate route is ambiguous until the phase completion bit is durable | adopt it only when canonical state requests the exact prefix; otherwise retain and diagnose it |
| compensating rollback | Windows journal records `rollback-additions` or `rollback-removals` plus completion bits | delete only additions proved complete, restore canonical state from a fresh core, and never infer ownership from a prefix alone |
| commit finalization | Windows journal is `committed` or `active`; process-local stale sessions/keys/cleanup jobs disappear with core | clean the proven route projection, reapply canonical state, and publish cleanup/recovery diagnostics before the new epoch becomes mutable |

Linux/OpenWrt peer routes are TUN-bound, so the table has no hidden Linux
policy-rule phase in v1. FreeBSD/OPNsense peer routes are likewise TUN-bound;
their durable phase machine applies to outer endpoint pins. Windows covers
both outer endpoint pins and the separate inner peer-route journal.

A binary or package upgrade that restarts quick also starts a new epoch.
In-progress request-cache entries are not carried across that boundary. The
controller re-reads status, observes canonical desired state and any reported
owned cleanup work, then submits a new request ID against the new epoch and
generation. Persisting opaque in-flight transactions across executable or
protocol versions is intentionally unsupported.

## 21. Acceptance and platform tests

Shared unit and privileged tests must demonstrate:

1. Adding a peer during sustained TCP and UDP traffic does not interrupt an
   unrelated peer.
2. Removing or updating one peer does not replace unrelated peer objects or
   close their QUIC sessions.
3. Updating ordinary AllowedIPs, endpoint, and keepalive does not restart the
   interface.
4. Per-peer FEC policy changes at a group boundary without changing the global
   wire format once that capability is advertised.
5. Invalid desired state performs no preparation and leaves desired generation
   unchanged.
6. A failed prepared transaction restores the prior desired peer and owned host
   state; operational counters may continue advancing.
7. Stale epoch and generation requests are rejected without mutation.
8. A repeated request ID returns or joins the original transaction; reusing an
   ID with different content is rejected.
9. Client disconnect and lost response do not cancel or duplicate a transaction.
10. A DDNS address change publishes only after generation-bound WireGuard
    authentication.
11. An unreachable candidate restores the old endpoint and retains its route.
12. DNS answer reordering does not switch a healthy endpoint.
13. A failed selected endpoint rotates to another A/AAAA candidate even while
    the failed address remains in DNS.
14. NXDOMAIN and resolver timeouts retain an established selected endpoint.
15. Network route change, NAT rebinding, peer restart, and supervisor restart
    retain existing recovery behavior.
16. Introducing or removing automatic full-tunnel mode returns
    `restart_required` without partial mutation.
17. Status, request caches, journals, and all failure paths remain free of
    private and preshared keys.
18. Abrupt termination during prepare, switch, commit, rollback, and finalization
    is recovered conservatively on the next epoch.

Platform coverage must include:

- Linux systemd reload and direct manager-neutral CLI operation;
- at least one OpenRC environment and one runit or generic external-supervisor
  environment, proving no runtime systemd dependency;
- OpenWrt procd on ARM64 and x86_64;
- FreeBSD rc.d on amd64 and an arm64 runtime environment when available;
- OPNsense configd on every release train carried by the package workflow;
- installed Windows x64 manager, desktop, SCM service, ACL, upgrade, and
  lifecycle tests; and
- build/unit parity for Windows arm64 until native CI hardware is available,
  followed by native lifecycle coverage before claiming it runtime-verified.

The privileged fixture includes packet loss, delay, jitter, DNS answer changes,
lost management responses, concurrent controllers, process termination, route
command failure, and cleanup retry.

Evidence is intentionally split by failure boundary. The shared executor and
endpoint suites inject failures in prepare/commit/rollback/finalize operations;
the Linux privileged fixture kills quick without killing its container/init
process and proves that the inherited lifetime pipe removes the core and TUN;
Windows native tests replay every durable peer-route journal phase; FreeBSD
native tests cover pending-add, active, and pending-delete outer-route
recovery. Exact installed-package kill/reboot runs remain claim-gate evidence
rather than being inferred from these portable tests.

The portable acceptance trace is kept close to the code rather than in an
external checklist:

| Acceptance items | Primary executable evidence |
| --- | --- |
| 1–3 | `tests/container/test.sh` keeps TCP/UDP active while a third peer is added, updated, and removed; it asserts unchanged supervisor/session/endpoint generations for the unrelated peer |
| 4 | `internal/transport/fec` group-flush tests, `internal/bind` live policy replacement tests, and `internal/core` FEC transaction commit/rollback tests |
| 5–6 | model validation plus `internal/quick/reconcile_executor_test.go` failure injection at every mutation, rollback, and finalization boundary |
| 7–9 | coordinator CAS/disconnect/cache tests plus the privileged stale-generation, ID-collision, and changed-deadline retry path |
| 10–14 | endpoint generation/readiness, candidate rotation, route-lease rollback, DNS reorder, NXDOMAIN, and timeout tests under `internal/endpoint` |
| 15 | privileged MTU/loss/NAT/peer-restart tests, parent-death core/TUN teardown, route-notification tests, and platform startup recovery suites |
| 16 | canonical diff tests prove automatic default-route transitions reject before executor preparation |
| 17 | secure snapshot, protocol redaction, status, journal codec, and secret-like journal rejection tests |
| 18 | graceful coordinator shutdown rollback tests, the Linux quick `SIGKILL` fixture, every Windows peer-route journal phase, and every FreeBSD outer-route ledger phase |

This table identifies the portable gate only. A release support label still
requires the exact package/service/image evidence in sections 21 and 26; a
source-level test name cannot promote an artifact by itself.

Acceptance is gated in two layers:

1. **portable gate** on every change: shared model/coordinator tests, core and
   endpoint failure injection, route-journal codec tests, race tests, shell and
   package static checks, cross-compilation for every published tuple, and
   package-path `runtime-smoke` in available OpenWrt guests; this last check
   validates the current binaries but does not substitute for installing the
   target APK;
2. **claim gate** before a release label is raised: boot the exact target,
   install its actual package, create a TUN, pass unrelated-peer continuity,
   reload through its service adapter, kill during every durable phase, reboot,
   and verify traffic plus conservative recovery.

The claim matrix is tracked per tuple rather than per source directory:

| Tuple | Required claim fixture |
| --- | --- |
| Linux amd64 | native privileged namespaces plus systemd and OpenRC/direct-supervisor lifecycle |
| Linux arm64 | arm64 native or full-system emulation before runtime-verified; cross-build otherwise |
| OpenWrt armsr/armv8 | official firmware QEMU, installed APK, procd reload, reboot, hooks, traffic, rollback |
| OpenWrt x86/64 | the same tests in an x86_64 guest; SDK output alone remains build-supported |
| FreeBSD amd64 | native rc.d reload, route failure injection, traffic, and restart recovery |
| FreeBSD arm64 | native/emulated lifecycle before runtime-verified; cross-build otherwise |
| OPNsense amd64 | every carried FreeBSD release train, installed plugin, configd render/reload, traffic, reboot |
| Windows amd64 | installed MSI, LocalSystem manager/SCM service, named-pipe ACL, IP Helper journal kill/recovery, upgrade |
| Windows arm64 | native service lifecycle before runtime-verified; cross-build/unit parity otherwise |

An unavailable native runner is a release-claim limitation, not permission to
skip tests while keeping the stronger label.

## 22. Capability-gated implementation sequence

The feature is delivered in cross-platform capability slices, not as a Linux
implementation followed by unrelated ports:

1. Add canonical desired models, dry-run diff, quick management transports,
   epoch/generation, request cache, structured errors, and status on every
   platform.
2. Add typed core peer prepare/commit/rollback and dynamic Salamander key
   leases, with add/remove/update tests that keep unrelated sessions alive.
3. Add incremental inner-route plans everywhere and durable ownership only for
   effects that survive the TUN: FreeBSD/OPNsense outer pins and Windows outer
   plus inner routes.
4. Add generation-bound authenticated endpoint switching, DDNS health rotation,
   and refresh worker lifecycle.
5. Add manager adapters: systemd, non-systemd example scripts, procd, rc.d,
   configd, and Windows manager/desktop forwarding.
6. Add dynamic per-peer FEC policy and advertise that capability only after its
   identity mapping and group-boundary semantics are implemented.

During staged development, `show --json` advertises exact capabilities. A
controller may use supported mutable fields while an unsupported field receives
`unsupported_capability` or `restart_required` before mutation. No release may
describe a tuple as runtime- or integration-verified until its claim gate
passes. A running binary advertises `peer_reconcile_v1` only after
its local security, core, endpoint, and route primitives initialize
successfully; this live capability is not a substitute for the release support
label.

## 23. Controller sequencing outside this API

This API guarantees one local interface transaction. It does not make a
network-wide topology update atomic. A mesh controller should sequence a new
link as follows:

1. Add the listening peer on the target node.
2. Add the endpoint-bearing peer on the initiating node.
3. Wait for authenticated readiness on both nodes.
4. Publish application routes that use the link.
5. Drain old application routes before removing an obsolete peer.

This keeps application routing convergence separate from wg-quic peer
lifecycle while avoiding interface-wide restarts and preserving unaffected
sessions.

## 24. UI and platform-controller contract

LuCI, the OPNsense plugin, and the Windows desktop are presentation and
persistence controllers over the same local protocol. They do not receive a
second mutation API and must not derive success from process exit alone.

A controller editing a running interface follows this state machine:

```text
edited -> candidate_written -> runtime_validating -> runtime_committed
       -> canonical_promoted -> durable
                         \-> rejected (old runtime and canonical remain)
       -> canonical_promotion_failed (new runtime, old file, drift=true)
```

Required controller behavior:

- obtain epoch/generation immediately before submission and preserve one
  request ID across timeout retries;
- write candidates using root/LocalSystem-only atomic creation, never through
  shell arguments or a world-writable directory;
- show structured per-peer and restart-required errors without stopping the
  interface;
- promote exactly the validated bytes, not a newly rendered approximation;
- surface `persistent_drift`, `cleanup_pending`, and degraded recovery until
  resolved;
- require a distinct confirmation for a restart or service removal;
- keep private and preshared keys out of browser state, telemetry, status, and
  logs; and
- sequence create/update/delete of multiple local interfaces explicitly; there
  is no hidden host-wide transaction.

For OpenWrt, UCI selects enabled instances but the root-only rendered profile
remains the candidate consumed by quick. A future LuCI page calls ubus/rpcd,
which renders and reconciles server-side; JavaScript never connects to the Unix
socket. For OPNsense, configd performs the equivalent render/reconcile/promote
sequence. On Windows, the LocalSystem manager creates the protected candidate
and forwards the bounded request to the existing per-tunnel supervisor rather
than replacing its SCM service.

## 25. Compatibility and rollback policy

Management protocol v1 is additive for optional status fields. A client ignores
unknown response fields, but servers reject unknown request fields and an
unsupported protocol version. Capability names are immutable contracts: a
semantic break requires a new capability/version rather than silently changing
`peer_reconcile_v1`.

Rolling upgrades restart quick and therefore create a new epoch. The canonical
file must be valid for both the old and new binary during a package rollback.
Durable ledgers are schema-versioned and checksum-protected. A newer binary may
migrate a ledger only through an atomic write with a recoverable previous copy;
an older binary that cannot understand the schema refuses mutation and leaves
owned objects intact. Package uninstall must first stop every instance and run
normal or explicit repair; it must not delete the state directory while live or
ambiguous route ownership remains.

## 26. Support evidence and release promotion

Platform support is evidence attached to an exact artifact, not a permanent
property inferred from a source directory or an older release. Each release
candidate maintains one record per claim-matrix tuple containing:

- source commit and version;
- archive or package SHA-256, including the desktop installer where relevant;
- OS image/release, architecture, kernel, and native-versus-emulated runner;
- service adapter and fixture revision;
- completed portable and claim gates, with durable log or artifact links; and
- the strongest resulting label for each of the four support dimensions.

A runtime or integration result is reusable only when the tested package hash
is the released hash. Installing locally copied binaries into package paths is
useful portable evidence but cannot promote the package dimension. Manual QEMU
or hardware runs are acceptable when hosted CI lacks the target, but they must
produce the same machine-readable record and retain the exact commands and
image checksum.

Evidence becomes stale when a later commit changes a component inside its
claim boundary. Core, transport, quick transaction, route, secure-path, service
adapter, package, or fixture changes invalidate the affected runtime or
integration records. Documentation-only changes do not. A platform-specific
change invalidates that platform; a portable transaction or data-plane change
invalidates every runtime tuple. A release workflow may preserve a lower
build/unit label while stronger evidence is rerun, but it must never carry a
stale runtime/integration label forward silently.

README and release-note tables are projections of these records. CI verifies
that every stronger label names current evidence and that every published
artifact tuple has at least build evidence. Missing hardware therefore appears
as an explicit lower label and pending claim gate, rather than as either a
false universal-support claim or an untracked exception.

## 27. Admission contract for a new operating-system family

The all-platform contract in this document covers the platform families in
section 2. It does not currently include macOS, iOS/iPadOS, Android, or another
OS merely because the portable Go packages compile there. Those systems have
different packet-interface, privilege, background-lifecycle, routing, and
application-signing models. Until an exact tuple passes the gates below it is
`unsupported`, not an unlisted form of build support.

A new operating-system family enters the support matrix only after it supplies
all of these adapters without weakening the portable transaction semantics:

1. A production packet interface and data-plane path. For example, a macOS
   command-line port may use `utun`, while an App Store or mobile port normally
   requires a Network Extension or Android `VpnService`; a test-only TUN shim
   is insufficient.
2. A least-privilege management boundary that authenticates the local
   controller, bounds every request, protects candidate bytes, and preserves
   the same epoch/generation/request-ID protocol. A sandboxed application must
   use the OS broker or extension IPC model rather than exposing the Unix root
   socket to its UI process.
3. Atomic, secret-safe canonical and candidate storage. Private and preshared
   keys remain outside browser/UI state and ordinary logs; platform keychain or
   app-group integration must still produce one exact, auditable candidate
   projection for reconciliation.
4. Incremental inner- and outer-route transactions with explicit ownership,
   prepare/commit/rollback ordering, and conservative recovery for every effect
   that can survive the packet interface or process. Broad route deletion or
   inference from destination prefix alone is not acceptable.
5. A native lifecycle/package adapter for the platform's supported execution
   model, including install, upgrade, restart, sleep/network-change handling,
   and removal while a tunnel is active. A foreground developer binary proves
   only the dimensions it actually exercises.
6. Capability negotiation and all shared failure-injection tests, followed by
   an exact-artifact claim record covering traffic continuity, authenticated
   endpoint migration, transaction rollback, forced termination, reboot or
   extension restart, and cleanup on a supported OS release and architecture.

Platform-specific limitations are returned as structured capability or
`restart_required` results. A new adapter may initially expose a strict subset
of mutable fields, but it may not emulate a successful hot update by silently
restarting the tunnel or dropping unrelated peers. Native UI work begins only
after the privileged controller contract is usable independently; this keeps a
future Network Extension, Android service, or other presentation layer from
becoming a second owner of runtime state.
