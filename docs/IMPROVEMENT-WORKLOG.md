# Review follow-up

This work tracks the September 2026 engineering and interaction review. Each
substantial feature is committed separately. Existing platform privilege
boundaries and protocol semantics remain the baseline.

Planned delivery:

- Lossless configuration editing, multiple peer selection, source editing,
  persistent drafts, and realistic editor interaction checks.
- Honest runtime states and explicit saved/applied/restart-required feedback.
- Bounded concurrent status collection and stable incremental rendering.
- FEC late-shard idempotence and decoder invariant coverage.
- Session admission, authentication deadlines and resource accounting.
- Measured status/data-path improvements and clearer module boundaries.
- Chinese UI, public-key copying, peer rates, session history and diagnostics.
- Shared contract fixtures, repeatable validation and retained performance
evidence.

## Lossless editor

Existing documents are patched only where the selected form fields change;
untouched documents round-trip byte for byte, including CRLF and a missing
final newline. Multiple peers have independent projections; source editing
supports additions, removals and advanced directives. Refresh leaves editing
DOM untouched, failed saves keep input and errors, and asynchronous generated
keys cannot overwrite a newer editor session. Cancel/navigation asks before
discarding changes. Secret fields are cleared when the editor closes.

Validation: TypeScript checks and frontend unit tests; real Chromium interaction
smoke against the built assets with a substituted native boundary. The same
interaction scenario is included in the packaged WebKit/WebView smoke. Native
installed-platform execution is separate from this browser check.

## FEC accounting

Late reconstructed shards now clear their accounting bit after the first
arrival. Repeated arrivals are idempotent; malformed payloads and changed epochs
cannot alter completed-group counters. Received feedback enforces
`recovered <= missing <= total`, with the existing bounded unknown-dimensions
sentinel retained. Regression tests cover duplicates, malformed input and
counter bounds. A five-second local fuzz run completed 134,362 executions.

## Runtime status semantics

`wg-quic-quick desktop-status NAME` reads only the public status endpoint and
returns a versioned envelope with stable error codes. Missing endpoints are
inactive; access errors, timeouts and malformed responses remain unknown.
Desktop controls no longer suggest activation when observation failed. The
UI separates interface preparation, waiting, dialing, reconnecting, transport
authentication and authenticated/partial peer connectivity. A live QUIC session
alone is insufficient to show Connected.

Validation: Go status classification tests, Rust protocol and view mapping
tests, TypeScript state tests and a real-browser status recovery scenario.

## Saving and applying

Saving/importing records pending changes without interrupting traffic. The
desktop invokes a fixed privileged apply command that submits a reload with
the current epoch/generation and an opaque request ID. Applied, restart-required,
failed and unknown outcomes are distinct data states. An unknown result is
queried by its original ID; a lost transaction record is not proof of failure.
Explicit restart confirmation explains the traffic interruption. Pending flags
survive app restarts without persisting keys or diagnostic messages.

Validation: common Go CAS/result tests, Windows helper/broker test
cross-compilation, Rust backend tests, frontend checks and a Chromium scenario
covering save, declined/accepted restart and unknown-result recovery. Windows
privileged execution remains a native CI requirement.

## Bounded desktop observations

Four fixed workers collect status, with at most 64 queued reads and a six-second
per-process timeout. Snapshots return available results after at most 500 ms of
status waiting. The selected profile is scheduled first and cached for one second;
background profiles refresh every ten seconds. Observations older than fifteen
seconds become unknown. Windows broker probes have a separate fifteen-second
cache and one-second timeout. Mutations invalidate in-flight observations in
both the backend and renderer. Tunnel list elements retain keyboard focus across
refreshes; observation errors cannot leave a permanently green connection.

Validation: eleven Rust tests, including blocked readers and mutation races;
frontend tests and Chromium smoke including actual list focus retention.

## Session resource budgets

Admission now precedes session queues/FEC allocation: defaults are 1,024 total,
256 inbound and 32 unauthenticated inbound sessions per bind. The separate
inbound budget leaves capacity for configured outbound peers. An inbound QUIC
connection must authenticate a WireGuard packet within ten seconds. Atomic
authentication transitions return the pending reservation exactly once;
authenticated sessions are exempt from the deadline. Pending inbound traffic
can reserve at most 32 entries (or one quarter of a smaller shared queue).
Status exposes admission rejections and authentication timeouts, and closed
session history records the authentication-timeout reason.

These limits are tunable through `armorbind.Config`; the quick configuration
syntax is unchanged. They bound application sessions after QUIC acceptance,
not every kernel or TLS-handshake allocation. Authenticated peers retain the
existing shared queue behavior. Validation: bind/core race tests, concurrent
authentication/close accounting, queue reservation release on errors, and a
real QUIC authentication deadline test.

## Measured observations and receive metadata

Endpoint status construction indexes live sessions once instead of rescanning
them for every peer. The implementation is isolated in `endpoint_status.go`.
QUIC queue metadata captures UDP addresses by value; an additive owned receive
API avoids per-packet address allocations while preserving the existing API.
See [retained raw measurements](benchmarks/2026-09-review/README.md) for the
allocation result, the status-query time/memory tradeoff, reproducible commands,
and the limited loopback throughput evidence. No receive batching or buffer
ownership experiment from the earlier negative study was reintroduced.

Validation records below distinguish local execution, cross-compilation and
CI-only platform coverage. A throughput improvement requires an end-to-end
measurement; allocation and synthetic benchmark improvements are reported as
such.
