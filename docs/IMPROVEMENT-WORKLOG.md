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

Validation records below distinguish local execution, cross-compilation and
CI-only platform coverage. A throughput improvement requires an end-to-end
measurement; allocation and synthetic benchmark improvements are reported as
such.
