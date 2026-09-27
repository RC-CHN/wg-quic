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

Validation records below distinguish local execution, cross-compilation and
CI-only platform coverage. A throughput improvement requires an end-to-end
measurement; allocation and synthetic benchmark improvements are reported as
such.
